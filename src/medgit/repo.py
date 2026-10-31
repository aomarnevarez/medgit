"""Append-only clinical object log.

A patient is a repository. An episode is a branch. A clinical fact is a
commit. None of those words mean that history can be rewritten: refs only
move forward, corrections are new objects, and a merge keeps every observation.
"""

from __future__ import annotations

import json
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Callable

from medgit.canonical import canonicalize
from medgit.errors import IntegrityError, PolicyError, RewriteForbidden
from medgit.fhirutil import (
    document_reference,
    encounter_resource,
    patient_resource,
    project_resource,
    provenance,
    quantity_observation,
    risk_assessment,
)
from medgit.rules import score_qsofa
from medgit.store import Store

VERSION = "0.2.0"
ROLES = {"resident", "attending", "system", "auditor"}
HUMAN = {"resident", "attending"}
CLINICAL_TYPES = {
    "note",
    "order",
    "result",
    "observation",
    "medication",
    "procedure",
    "feature",
}
SYSTEM_TYPES = {"result", "feature", "inference"}
COMMIT_TYPES = CLINICAL_TYPES | {
    "episode-open",
    "episode-abandon",
    "correction",
    "inference",
    "merge",
    "patient-init",
}
OPEN_PR = {"open", "approved", "changes-requested"}
CONFLICT_WINDOW = timedelta(hours=6)

Clock = Callable[[], str]


def utc_now() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def new_id(prefix: str) -> str:
    return prefix + uuid.uuid4().hex[:12]


def parse_time(value: str) -> datetime:
    text = value[:-1] + "+00:00" if value.endswith("Z") else value
    try:
        parsed = datetime.fromisoformat(text)
    except ValueError as exc:
        raise IntegrityError(f"bad timestamp: {value}") from exc
    if parsed.tzinfo is None:
        raise IntegrityError(f"naive timestamp: {value}")
    return parsed.astimezone(timezone.utc)


class Actor:
    def __init__(self, id: str, display: str, role: str):
        if role not in ROLES:
            raise PolicyError(f"unknown role: {role}")
        if not id or not display:
            raise PolicyError("actor id and display are required")
        self.id = id
        self.display = display
        self.role = role

    def as_dict(self) -> dict[str, str]:
        return {"id": self.id, "display": self.display, "role": self.role}


class MedRepo:
    def __init__(self, root: str | Path, clock: Clock | None = None):
        self.store = Store(Path(root))
        self.clock = clock or utc_now

    @property
    def root(self) -> Path:
        return self.store.root

    @classmethod
    def init(
        cls,
        root: str | Path,
        *,
        actor: Actor,
        patient_ref: str,
        display: str,
        gender: str = "unknown",
        birth_date: str = "1978-04-12",
        clock: Clock | None = None,
    ) -> MedRepo:
        if actor.role not in {"attending", "system"}:
            raise PolicyError("only an attending or a provisioning system can init a record")
        path = Path(root)
        if (path / "medgit.json").exists():
            raise PolicyError(f"record already exists at {path}")
        path.mkdir(parents=True, exist_ok=True)
        config = {
            "medgit": VERSION,
            "patient_ref": patient_ref,
            "display": display,
            "gender": gender,
            "birth_date": birth_date,
        }
        (path / "medgit.json").write_text(
            json.dumps(config, indent=2, sort_keys=True) + "\n", encoding="utf-8"
        )
        repo = cls(path, clock=clock)
        document = repo._commit_document(
            actor=actor,
            commit_type="patient-init",
            message=f"Open longitudinal record for {display}",
            parents=[],
            episode_id=None,
            resources=[
                patient_resource(patient_ref, display, gender=gender, birth_date=birth_date)
            ],
            sensitivity="normal",
            corrects=None,
            notices=[],
            authored_at=None,
        )
        digest = repo.store.put(document)
        repo.store.write_ref("main", digest)
        return repo

    def config(self) -> dict[str, Any]:
        path = self.root / "medgit.json"
        if not path.is_file():
            raise IntegrityError("medgit.json is missing")
        return json.loads(path.read_text(encoding="utf-8"))

    def open_episode(
        self,
        *,
        actor: Actor,
        reason: str,
        encounter_class: str = "EMER",
        episode_id: str | None = None,
    ) -> dict[str, Any]:
        self._require_human(actor, "open an episode")
        if not reason.strip():
            raise PolicyError("an episode needs a clinical reason")
        main = self._require_ref("main")
        identifier = episode_id or new_id("ep_")
        if self.store.read_ref(f"episodes/{identifier}"):
            raise PolicyError(f"episode {identifier} already exists")
        patient_ref = self.config()["patient_ref"]
        when = self.clock()
        document = self._commit_document(
            actor=actor,
            commit_type="episode-open",
            message=reason,
            parents=[main],
            episode_id=identifier,
            resources=[
                encounter_resource(identifier, patient_ref, reason, encounter_class, when)
            ],
            sensitivity="normal",
            corrects=None,
            notices=[],
            authored_at=when,
        )
        digest = self.store.put(document)
        self.store.write_ref(f"episodes/{identifier}", digest)
        return {"hash": digest, "episode_id": identifier, **document}

    def commit(
        self,
        *,
        actor: Actor,
        episode_id: str,
        commit_type: str,
        message: str,
        resources: list[dict[str, Any]],
        sensitivity: str = "normal",
        corrects: str | None = None,
        authored_at: str | None = None,
    ) -> dict[str, Any]:
        self._require_writer(actor)
        if commit_type not in CLINICAL_TYPES | {"correction", "inference"}:
            raise PolicyError(f"{commit_type} is not committed through commit()")
        if sensitivity not in {"normal", "restricted"}:
            raise PolicyError(f"unknown sensitivity: {sensitivity}")
        if not message.strip():
            raise PolicyError("a commit needs a message")
        if not resources:
            raise PolicyError("a clinical commit needs at least one resource")
        state = self.episode_state(episode_id)
        if state != "open" and state != "in-review":
            raise PolicyError(f"episode {episode_id} is {state}; commit on an open episode")
        original = None
        if commit_type == "correction":
            if not corrects:
                raise PolicyError("a correction must name the commit it corrects")
            original = self.object(corrects)
            if original.get("kind") != "commit":
                raise PolicyError("corrections point at commits")
            if original.get("episode_id") != episode_id:
                raise PolicyError("corrections stay on the episode of the original fact")
        elif corrects:
            raise PolicyError("only a correction commit may set corrects")
        if actor.role == "system":
            if commit_type == "correction":
                if original["author"]["role"] != "system":
                    raise PolicyError("a system may only correct its own commits")
            elif commit_type not in SYSTEM_TYPES:
                raise PolicyError(f"a system actor cannot commit {commit_type}")
        if actor.role == "resident" and commit_type == "inference":
            raise PolicyError("inference commits are produced by a system or an attending")
        parent = self._require_ref(f"episodes/{episode_id}")
        document = self._commit_document(
            actor=actor,
            commit_type=commit_type,
            message=message,
            parents=[parent],
            episode_id=episode_id,
            resources=resources,
            sensitivity=sensitivity,
            corrects=corrects,
            notices=[],
            authored_at=authored_at,
        )
        digest = self.store.put(document)
        self.store.write_ref(f"episodes/{episode_id}", digest)
        self._invalidate_reviews(episode_id, digest)
        return {"hash": digest, **document}

    def commit_note(self, *, actor: Actor, episode_id: str, text: str, authored_at: str | None = None) -> dict[str, Any]:
        when = authored_at or self.clock()
        resource = document_reference(
            new_id("note_"),
            self.config()["patient_ref"],
            episode_id,
            text=text,
            author=actor.display,
            when=when,
        )
        return self.commit(
            actor=actor,
            episode_id=episode_id,
            commit_type="note",
            message=text[:120],
            resources=[resource],
            authored_at=when,
        )

    def commit_observation(
        self,
        *,
        actor: Actor,
        episode_id: str,
        loinc: str,
        display: str,
        value: str,
        unit: str,
        ucum: str,
        message: str | None = None,
        authored_at: str | None = None,
        commit_type: str = "observation",
    ) -> dict[str, Any]:
        if commit_type not in {"observation", "result"}:
            raise PolicyError("quantity helpers only write observation or result commits")
        when = authored_at or self.clock()
        resource = quantity_observation(
            new_id("obs_"),
            self.config()["patient_ref"],
            episode_id,
            loinc=loinc,
            display=display,
            value=str(value),
            unit=unit,
            ucum=ucum,
            effective=when,
        )
        return self.commit(
            actor=actor,
            episode_id=episode_id,
            commit_type=commit_type,
            message=message or f"{display} {value} {unit}",
            resources=[resource],
            authored_at=when,
        )

    def commit_qsofa(
        self,
        *,
        actor: Actor,
        episode_id: str,
        respiratory_rate: int,
        systolic_bp: int,
        altered_mentation: bool,
        message: str | None = None,
        authored_at: str | None = None,
    ) -> dict[str, Any]:
        if actor.role not in {"system", "attending"}:
            raise PolicyError("only a system or an attending can commit a rule result")
        scored = score_qsofa(respiratory_rate, systolic_bp, altered_mentation)
        when = authored_at or self.clock()
        resource = risk_assessment(
            new_id("risk_"),
            self.config()["patient_ref"],
            episode_id,
            score=scored.score,
            band=scored.band,
            narrative=scored.narrative,
            when=when,
        )
        return self.commit(
            actor=actor,
            episode_id=episode_id,
            commit_type="inference",
            message=message or scored.narrative,
            resources=[resource],
            authored_at=when,
        )

    def open_pull(self, *, actor: Actor, episode_id: str, title: str, body: str = "") -> dict[str, Any]:
        self._require_human(actor, "open a consult")
        if self.episode_state(episode_id) == "merged":
            raise PolicyError("episode is already on the longitudinal record")
        if self.episode_state(episode_id) == "abandoned":
            raise PolicyError("episode was abandoned")
        if not title.strip():
            raise PolicyError("a consult needs a title")
        for existing in self.pulls():
            if existing["episode_id"] == episode_id and existing["state"] in OPEN_PR:
                raise PolicyError(f"consult {existing['id']} is already open for this episode")
        source = self._require_ref(f"episodes/{episode_id}")
        target = self._require_ref("main")
        identifier = new_id("pr_")
        document = {
            "medgit": VERSION,
            "kind": "pull",
            "id": identifier,
            "episode_id": episode_id,
            "title": title,
            "body": body,
            "author": actor.as_dict(),
            "source_tip": source,
            "target_tip": target,
            "state": "open",
            "reviews": [],
            "conflicts": self.semantic_conflicts(episode_id),
            "previous": None,
            "merge_commit": None,
            "created_at": self.clock(),
            "updated_at": self.clock(),
        }
        digest = self.store.put(document)
        self.store.write_ref(f"pulls/{identifier}", digest)
        return {"hash": digest, **document}

    def review(self, *, actor: Actor, pr_id: str, decision: str, comment: str = "") -> dict[str, Any]:
        if decision not in {"approve", "request-changes", "comment"}:
            raise PolicyError(f"unknown decision: {decision}")
        if actor.role not in HUMAN:
            raise PolicyError("only a clinician can review a consult")
        if decision == "approve" and actor.role != "attending":
            raise PolicyError("only an attending can approve a consult")
        current = self.pull(pr_id)
        if current["state"] not in OPEN_PR:
            raise PolicyError(f"consult is {current['state']}")
        if decision == "approve" and actor.id == current["author"]["id"]:
            raise PolicyError("you cannot approve your own consult")
        reviews = list(current["reviews"])
        reviews.append(
            {
                "actor": actor.as_dict(),
                "decision": decision,
                "comment": comment,
                "at": self.clock(),
            }
        )
        state = current["state"]
        if decision == "approve":
            state = "approved"
        elif decision == "request-changes":
            state = "changes-requested"
        return self._advance_pull(current, state=state, reviews=reviews)

    def refresh_pull(self, *, actor: Actor, pr_id: str) -> dict[str, Any]:
        self._require_human(actor, "refresh a consult")
        current = self.pull(pr_id)
        if current["state"] == "merged":
            raise PolicyError("consult is already merged")
        episode_id = current["episode_id"]
        return self._advance_pull(
            current,
            state="open",
            reviews=[],
            source_tip=self._require_ref(f"episodes/{episode_id}"),
            target_tip=self._require_ref("main"),
            conflicts=self.semantic_conflicts(episode_id),
        )

    def merge(self, *, actor: Actor, pr_id: str, acknowledge_conflicts: bool = False) -> dict[str, Any]:
        if actor.role != "attending":
            raise PolicyError("only an attending can merge onto the longitudinal record")
        current = self.pull(pr_id)
        if current["state"] != "approved":
            raise PolicyError("merge requires an attending approval")
        if not _has_independent_approval(current):
            raise PolicyError("an attending other than the author must approve")
        episode_id = current["episode_id"]
        if self.episode_state(episode_id) == "abandoned":
            raise PolicyError("refusing to merge an abandoned episode")
        source = self._require_ref(f"episodes/{episode_id}")
        target = self._require_ref("main")
        if source != current["source_tip"] or target != current["target_tip"]:
            raise PolicyError("the episode or the longitudinal record moved; refresh the consult")
        conflicts = self.semantic_conflicts(episode_id)
        if conflicts and not acknowledge_conflicts:
            raise PolicyError(
                f"{len(conflicts)} discordant observation(s); acknowledge them to merge"
            )
        document = self._commit_document(
            actor=actor,
            commit_type="merge",
            message=f"Merge {current['id']}: {current['title']}",
            parents=[target, source],
            episode_id=episode_id,
            resources=[],
            sensitivity="normal",
            corrects=None,
            notices=conflicts,
            authored_at=None,
        )
        digest = self.store.put(document)
        self.store.write_ref("main", digest)
        self._advance_pull(current, state="merged", reviews=current["reviews"], merge_commit=digest)
        return {"hash": digest, **document}

    def abandon(self, *, actor: Actor, episode_id: str, reason: str) -> dict[str, Any]:
        self._require_human(actor, "abandon an episode")
        if actor.role != "attending":
            raise PolicyError("only an attending can abandon an episode")
        if self.episode_state(episode_id) != "open" and self.episode_state(episode_id) != "in-review":
            raise PolicyError("episode is not open")
        if not reason.strip():
            raise PolicyError("abandoning an episode requires a reason")
        parent = self._require_ref(f"episodes/{episode_id}")
        document = self._commit_document(
            actor=actor,
            commit_type="episode-abandon",
            message=reason,
            parents=[parent],
            episode_id=episode_id,
            resources=[],
            sensitivity="normal",
            corrects=None,
            notices=[],
            authored_at=None,
        )
        digest = self.store.put(document)
        self.store.write_ref(f"episodes/{episode_id}", digest)
        for pull in self.pulls():
            if pull["episode_id"] == episode_id and pull["state"] in OPEN_PR:
                self._advance_pull(pull, state="closed", reviews=pull["reviews"])
        return {"hash": digest, **document}

    def amend(self, *_args: Any, **_kwargs: Any) -> None:
        raise RewriteForbidden(
            "amend rewrites a clinical fact. Commit a correction that references the original hash."
        )

    def rebase(self, *_args: Any, **_kwargs: Any) -> None:
        raise RewriteForbidden(
            "rebase rewrites history. Episodes merge by union; discordant values become review notices."
        )

    def reset(self, *_args: Any, **_kwargs: Any) -> None:
        raise RewriteForbidden("reset would move a ref backwards. Refs only advance by a new object.")

    def object(self, digest: str) -> dict[str, Any]:
        return self.store.get(digest)

    def episode_state(self, episode_id: str) -> str:
        tip = self.store.read_ref(f"episodes/{episode_id}")
        if tip is None:
            raise PolicyError(f"unknown episode {episode_id}")
        for commit in self.history(tip, all_parents=False):
            if commit["type"] == "episode-abandon":
                return "abandoned"
        main = self.store.read_ref("main")
        if main:
            for commit in self.history(main, all_parents=True):
                if commit["type"] == "merge" and commit.get("episode_id") == episode_id:
                    return "merged"
        for pull in self.pulls():
            if pull["episode_id"] == episode_id and pull["state"] in OPEN_PR:
                return "in-review"
        return "open"

    def history(self, tip: str, *, all_parents: bool = False) -> list[dict[str, Any]]:
        """Newest first. First-parent unless ``all_parents`` is set."""
        order: list[dict[str, Any]] = []
        seen: set[str] = set()

        def walk(digest: str) -> None:
            if digest in seen:
                return
            seen.add(digest)
            document = self.store.get(digest)
            if document.get("kind") != "commit":
                raise IntegrityError(f"{digest} is not a commit")
            parents = document.get("parents") or []
            follow = parents if all_parents else parents[:1]
            for parent in follow:
                walk(parent)
            order.append({"hash": digest, **document})

        walk(tip)
        order.reverse()
        return order

    def log(self, ref: str = "main", *, all_parents: bool = False) -> list[dict[str, Any]]:
        if ref != "main":
            ref = ref if ref.startswith("episodes/") else f"episodes/{ref}"
        tip = self.store.read_ref(ref)
        if tip is None:
            raise PolicyError(f"unknown ref {ref}")
        return self.history(tip, all_parents=all_parents)

    def pulls(self) -> list[dict[str, Any]]:
        found = []
        for name in self.store.list_refs("pulls/"):
            digest = self.store.read_ref(name)
            if digest:
                found.append({"hash": digest, **self.store.get(digest)})
        return found

    def pull(self, pr_id: str) -> dict[str, Any]:
        digest = self.store.read_ref(f"pulls/{pr_id}")
        if digest is None:
            raise PolicyError(f"unknown consult {pr_id}")
        return {"hash": digest, **self.store.get(digest)}

    def semantic_conflicts(self, episode_id: str) -> list[dict[str, str]]:
        episode_tip = self._require_ref(f"episodes/{episode_id}")
        main_tip = self._require_ref("main")
        episode_commits = [
            commit
            for commit in self.history(episode_tip, all_parents=True)
            if commit.get("episode_id") == episode_id
        ]
        main_commits = [
            commit
            for commit in self.history(main_tip, all_parents=True)
            if commit.get("episode_id") != episode_id
        ]
        episode_obs = _observations(episode_commits)
        main_obs = _observations(main_commits)
        notices = []
        for left in episode_obs:
            for right in main_obs:
                if left["key"] != right["key"]:
                    continue
                if left["value"] == right["value"]:
                    continue
                if abs(left["when"] - right["when"]) > CONFLICT_WINDOW:
                    continue
                notices.append(
                    {
                        "code": left["key"],
                        "episode_value": left["value"],
                        "main_value": right["value"],
                        "episode_commit": left["commit"],
                        "main_commit": right["commit"],
                        "severity": "review",
                    }
                )
        return notices

    def status(self) -> dict[str, Any]:
        episodes = []
        for name in self.store.list_refs("episodes/"):
            episode_id = name.split("/", 1)[1]
            episodes.append(
                {
                    "id": episode_id,
                    "state": self.episode_state(episode_id),
                    "tip": self.store.read_ref(name),
                }
            )
        return {
            "patient": self.config(),
            "main": self.store.read_ref("main"),
            "episodes": episodes,
            "pulls": [
                {"id": item["id"], "state": item["state"], "title": item["title"], "episode_id": item["episode_id"]}
                for item in self.pulls()
            ],
        }

    def verify(self) -> dict[str, Any]:
        errors: list[str] = []
        seen: set[str] = set()
        for digest in self.store.iter_object_ids():
            try:
                self.store.get(digest)
                seen.add(digest)
            except IntegrityError as exc:
                errors.append(str(exc))
        refs: dict[str, str] = {}
        for name in self.store.list_refs():
            try:
                target = self.store.read_ref(name)
            except IntegrityError as exc:
                errors.append(str(exc))
                continue
            if target is None:
                continue
            refs[name] = target
            if target not in seen:
                errors.append(f"ref {name} points at missing object {target}")
        commits = []
        for digest in sorted(seen):
            try:
                document = self.store.get(digest)
            except IntegrityError:
                continue
            if document.get("kind") == "commit":
                commits.append((digest, document))
                self._check_commit(digest, document, seen, errors)
            elif document.get("kind") == "pull":
                self._check_pull(digest, document, seen, errors)
            else:
                errors.append(f"{digest} has unknown kind")
        try:
            config = self.config()
        except IntegrityError as exc:
            errors.append(str(exc))
            config = {}
        main = refs.get("main")
        if main is None:
            errors.append("main ref is missing")
        else:
            try:
                found_patient = False
                for commit in self.history(main, all_parents=True):
                    if commit["type"] == "patient-init":
                        found_patient = True
                        resource = next(
                            (item for item in commit["resources"] if item.get("resourceType") == "Patient"),
                            None,
                        )
                        if resource is None or resource.get("id") != config.get("patient_ref"):
                            errors.append("patient-init does not match medgit.json")
                if not found_patient:
                    errors.append("main does not reach a patient-init commit")
            except IntegrityError as exc:
                errors.append(str(exc))
        return {"ok": not errors, "errors": errors, "objects": len(seen), "refs": len(refs)}

    def export_fhir(self, ref: str = "main") -> dict[str, Any]:
        tip = self._require_ref(ref)
        commits = list(reversed(self.history(tip, all_parents=True)))
        corrected: dict[str, str] = {}
        for commit in commits:
            if commit["type"] == "correction" and commit.get("corrects"):
                corrected[commit["corrects"]] = commit["hash"]
        merged: dict[str, dict[str, Any]] = {}
        for commit in commits:
            if commit["type"] == "merge" and commit.get("episode_id"):
                merged[commit["episode_id"]] = commit
        entries = []
        seen_full_url: set[str] = set()
        for commit in commits:
            fix = corrected.get(commit["hash"])
            for resource in commit.get("resources") or []:
                projected = project_resource(resource, corrected_by=fix)
                if (
                    projected.get("resourceType") == "Encounter"
                    and projected.get("id") in merged
                ):
                    projected["status"] = "finished"
                    period = dict(projected.get("period") or {})
                    period["end"] = merged[projected["id"]]["recorded_at"]
                    projected["period"] = period
                full_url = f"{projected.get('resourceType')}/{projected.get('id')}"
                if full_url in seen_full_url:
                    continue
                seen_full_url.add(full_url)
                entries.append({"fullUrl": full_url, "resource": projected})
            targets = [
                f"{item.get('resourceType')}/{item.get('id')}"
                for item in commit.get("resources") or []
                if item.get("resourceType") and item.get("id")
            ]
            prov = provenance(commit["hash"], commit, targets)
            entries.append({"fullUrl": f"Provenance/{prov['id']}", "resource": prov})
        return {
            "resourceType": "Bundle",
            "type": "collection",
            "id": f"bundle-{tip[:12]}",
            "timestamp": self.clock(),
            "entry": entries,
        }

    def _commit_document(
        self,
        *,
        actor: Actor,
        commit_type: str,
        message: str,
        parents: list[str],
        episode_id: str | None,
        resources: list[dict[str, Any]],
        sensitivity: str,
        corrects: str | None,
        notices: list[dict[str, str]],
        authored_at: str | None,
    ) -> dict[str, Any]:
        if commit_type not in COMMIT_TYPES:
            raise PolicyError(f"unknown commit type: {commit_type}")
        recorded = self.clock()
        parse_time(recorded)
        authored = authored_at or recorded
        parse_time(authored)
        for parent in parents:
            previous = self.store.get(parent)
            if parse_time(recorded) < parse_time(previous["recorded_at"]):
                raise IntegrityError("recorded_at moved backwards along the parent chain")
        for resource in resources:
            if not isinstance(resource, dict) or "resourceType" not in resource:
                raise PolicyError("resources must be FHIR-shaped objects")
        document = {
            "authored_at": authored,
            "author": actor.as_dict(),
            "corrects": corrects,
            "episode_id": episode_id,
            "kind": "commit",
            "medgit": VERSION,
            "message": message,
            "notices": notices,
            "parents": parents,
            "recorded_at": recorded,
            "resources": resources,
            "sensitivity": sensitivity,
            "type": commit_type,
        }
        try:
            canonicalize(document)
        except TypeError as exc:
            raise PolicyError(str(exc)) from exc
        return document

    def _advance_pull(self, current: dict[str, Any], *, state: str, reviews: list[dict[str, Any]], source_tip: str | None = None, target_tip: str | None = None, conflicts: list[dict[str, str]] | None = None, merge_commit: str | None = None) -> dict[str, Any]:
        document = {
            "author": current["author"],
            "body": current["body"],
            "conflicts": current["conflicts"] if conflicts is None else conflicts,
            "created_at": current["created_at"],
            "episode_id": current["episode_id"],
            "id": current["id"],
            "kind": "pull",
            "medgit": VERSION,
            "merge_commit": current.get("merge_commit") if merge_commit is None else merge_commit,
            "previous": current["hash"],
            "reviews": reviews,
            "source_tip": current["source_tip"] if source_tip is None else source_tip,
            "state": state,
            "target_tip": current["target_tip"] if target_tip is None else target_tip,
            "title": current["title"],
            "updated_at": self.clock(),
        }
        digest = self.store.put(document)
        self.store.write_ref(f"pulls/{current['id']}", digest)
        return {"hash": digest, **document}

    def _invalidate_reviews(self, episode_id: str, new_tip: str) -> None:
        for pull in self.pulls():
            if pull["episode_id"] != episode_id or pull["state"] not in OPEN_PR:
                continue
            if pull["source_tip"] == new_tip:
                continue
            self._advance_pull(
                pull,
                state="open",
                reviews=[],
                source_tip=new_tip,
                conflicts=self.semantic_conflicts(episode_id),
            )

    def _check_commit(self, digest: str, document: dict[str, Any], seen: set[str], errors: list[str]) -> None:
        if document.get("type") not in COMMIT_TYPES:
            errors.append(f"{digest} has unknown type")
        for parent in document.get("parents") or []:
            if parent not in seen:
                errors.append(f"{digest} parent {parent} is missing")
                continue
            previous = self.store.get(parent)
            try:
                if parse_time(document["recorded_at"]) < parse_time(previous["recorded_at"]):
                    errors.append(f"{digest} recorded_at is earlier than its parent")
            except IntegrityError as exc:
                errors.append(str(exc))
        corrects = document.get("corrects")
        if document.get("type") == "correction" and not corrects:
            errors.append(f"{digest} correction has no target")
        if corrects and corrects not in seen:
            errors.append(f"{digest} corrects missing object {corrects}")
        if document.get("type") == "merge" and len(document.get("parents") or []) != 2:
            errors.append(f"{digest} merge must have two parents")

    def _check_pull(self, digest: str, document: dict[str, Any], seen: set[str], errors: list[str]) -> None:
        previous = document.get("previous")
        if previous is not None and previous not in seen:
            errors.append(f"pull {digest} previous is missing")
        for key in ("source_tip", "target_tip"):
            if document.get(key) not in seen:
                errors.append(f"pull {digest} {key} is missing")
        if document.get("state") == "merged" and document.get("merge_commit") not in seen:
            errors.append(f"pull {digest} merge commit is missing")

    def _require_ref(self, name: str) -> str:
        target = self.store.read_ref(name)
        if target is None:
            raise PolicyError(f"missing ref {name}")
        return target

    @staticmethod
    def _require_human(actor: Actor, action: str) -> None:
        if actor.role not in HUMAN:
            raise PolicyError(f"only a clinician can {action}")

    @staticmethod
    def _require_writer(actor: Actor) -> None:
        if actor.role not in HUMAN | {"system"}:
            raise PolicyError(f"role {actor.role} is read-only")


def _has_independent_approval(pull: dict[str, Any]) -> bool:
    author = pull["author"]["id"]
    return any(
        review["decision"] == "approve"
        and review["actor"]["role"] == "attending"
        and review["actor"]["id"] != author
        for review in pull["reviews"]
    )


def _observations(commits: list[dict[str, Any]]) -> list[dict[str, Any]]:
    corrected = {
        commit["corrects"]
        for commit in commits
        if commit.get("type") == "correction" and commit.get("corrects")
    }
    found = []
    for commit in commits:
        if commit["hash"] in corrected:
            continue
        for resource in commit.get("resources") or []:
            if resource.get("resourceType") != "Observation":
                continue
            coding = (resource.get("code") or {}).get("coding") or [{}]
            key = f"{coding[0].get('system')}|{coding[0].get('code')}"
            quantity = (resource.get("valueQuantity") or {}).get("value")
            effective = resource.get("effectiveDateTime")
            if not effective or quantity is None:
                continue
            found.append(
                {
                    "key": key,
                    "value": str(quantity),
                    "when": parse_time(effective),
                    "commit": commit["hash"],
                }
            )
    return found
