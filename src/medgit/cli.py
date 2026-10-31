"""Command line for a local MedGit record.

The CLI trusts the actor you pass. A deployed system has to authenticate that
actor before it calls the library.
"""

from __future__ import annotations

import argparse
import json
import sys
from typing import Any

from pathlib import Path

from medgit.errors import IntegrityError, MedGitError, PolicyError, RewriteForbidden
from medgit.repo import Actor, MedRepo


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="medgit", description="Append-only clinical object log")
    parser.add_argument("-C", "--repo", default=".", help="path to the patient record")
    parser.add_argument("--json", action="store_true", dest="as_json")
    sub = parser.add_subparsers(dest="cmd", required=True)

    init = sub.add_parser("init", help="create a longitudinal record")
    _actor(init)
    init.add_argument("--patient-ref", required=True)
    init.add_argument("--display", required=True)
    init.add_argument("--gender", default="unknown")
    init.add_argument("--birth-date", default="1978-04-12")

    episode = sub.add_parser("episode", help="open an episode")
    episode_sub = episode.add_subparsers(dest="episode_cmd", required=True)
    open_ep = episode_sub.add_parser("open")
    _actor(open_ep)
    open_ep.add_argument("--reason", required=True)
    open_ep.add_argument("--class", default="EMER", dest="encounter_class")

    commit = sub.add_parser("commit", help="append a fact to an open episode")
    _actor(commit)
    commit.add_argument("--episode", required=True)
    commit.add_argument("--type", required=True, dest="commit_type")
    commit.add_argument("--message", required=True)
    commit.add_argument("--resource", required=True, help="path to a JSON resource or array")
    commit.add_argument("--sensitivity", default="normal")
    commit.add_argument("--corrects", default=None)

    note = sub.add_parser("note", help="append a progress note")
    _actor(note)
    note.add_argument("--episode", required=True)
    note.add_argument("--text", required=True)

    obs = sub.add_parser("observe", help="append a quantity observation")
    _actor(obs)
    obs.add_argument("--episode", required=True)
    obs.add_argument("--loinc", required=True)
    obs.add_argument("--display", required=True)
    obs.add_argument("--value", required=True)
    obs.add_argument("--unit", required=True)
    obs.add_argument("--ucum", required=True)
    obs.add_argument("--message", default=None)

    qsofa = sub.add_parser("qsofa", help="append a qSOFA screening result")
    _actor(qsofa)
    qsofa.add_argument("--episode", required=True)
    qsofa.add_argument("--rr", type=int, required=True)
    qsofa.add_argument("--sbp", type=int, required=True)
    qsofa.add_argument("--altered", action="store_true")

    consult = sub.add_parser("consult", help="open, review, refresh, or merge a consult")
    consult_sub = consult.add_subparsers(dest="consult_cmd", required=True)
    open_pr = consult_sub.add_parser("open")
    _actor(open_pr)
    open_pr.add_argument("--episode", required=True)
    open_pr.add_argument("--title", required=True)
    open_pr.add_argument("--body", default="")
    review = consult_sub.add_parser("review")
    _actor(review)
    review.add_argument("pr_id")
    review.add_argument("--decision", required=True, choices=["approve", "request-changes", "comment"])
    review.add_argument("--comment", default="")
    refresh = consult_sub.add_parser("refresh")
    _actor(refresh)
    refresh.add_argument("pr_id")
    merge = consult_sub.add_parser("merge")
    _actor(merge)
    merge.add_argument("pr_id")
    merge.add_argument("--acknowledge-conflicts", action="store_true")

    abandon = sub.add_parser("abandon")
    _actor(abandon)
    abandon.add_argument("--episode", required=True)
    abandon.add_argument("--reason", required=True)

    log = sub.add_parser("log")
    log.add_argument("--ref", default="main")
    log.add_argument("--all", action="store_true")

    sub.add_parser("status")
    sub.add_parser("verify")
    export = sub.add_parser("export-fhir")
    export.add_argument("--ref", default="main")
    show = sub.add_parser("show")
    show.add_argument("digest")

    for name, help_text in (
        ("amend", "refused: history is append-only"),
        ("rebase", "refused: history is append-only"),
        ("reset", "refused: refs only move forward"),
    ):
        sub.add_parser(name, help=help_text)

    args = parser.parse_args(argv)
    try:
        payload = _run(args)
    except RewriteForbidden as exc:
        print(f"medgit: refused: {exc}", file=sys.stderr)
        return 2
    except (PolicyError, IntegrityError, MedGitError) as exc:
        print(f"medgit: {exc}", file=sys.stderr)
        return 1
    if args.as_json:
        print(json.dumps(payload, indent=2, ensure_ascii=False, sort_keys=True))
    else:
        _print_human(args.cmd, payload)
    return 0


def _run(args: argparse.Namespace) -> Any:
    if args.cmd == "init":
        repo = MedRepo.init(
            args.repo,
            actor=_actor_from(args),
            patient_ref=args.patient_ref,
            display=args.display,
            gender=args.gender,
            birth_date=args.birth_date,
        )
        return {"path": str(repo.root), "main": repo.status()["main"]}
    if args.cmd in {"amend", "rebase", "reset"}:
        getattr(MedRepo(args.repo), args.cmd)()
    repo = MedRepo(args.repo)
    if args.cmd == "episode":
        return repo.open_episode(
            actor=_actor_from(args), reason=args.reason, encounter_class=args.encounter_class
        )
    if args.cmd == "commit":
        raw = json.loads(Path(args.resource).read_text(encoding="utf-8"))
        resources = raw if isinstance(raw, list) else [raw]
        return repo.commit(
            actor=_actor_from(args),
            episode_id=args.episode,
            commit_type=args.commit_type,
            message=args.message,
            resources=resources,
            sensitivity=args.sensitivity,
            corrects=args.corrects,
        )
    if args.cmd == "note":
        return repo.commit_note(actor=_actor_from(args), episode_id=args.episode, text=args.text)
    if args.cmd == "observe":
        return repo.commit_observation(
            actor=_actor_from(args),
            episode_id=args.episode,
            loinc=args.loinc,
            display=args.display,
            value=args.value,
            unit=args.unit,
            ucum=args.ucum,
            message=args.message,
        )
    if args.cmd == "qsofa":
        return repo.commit_qsofa(
            actor=_actor_from(args),
            episode_id=args.episode,
            respiratory_rate=args.rr,
            systolic_bp=args.sbp,
            altered_mentation=args.altered,
        )
    if args.cmd == "consult":
        actor = _actor_from(args)
        if args.consult_cmd == "open":
            return repo.open_pull(actor=actor, episode_id=args.episode, title=args.title, body=args.body)
        if args.consult_cmd == "review":
            return repo.review(actor=actor, pr_id=args.pr_id, decision=args.decision, comment=args.comment)
        if args.consult_cmd == "refresh":
            return repo.refresh_pull(actor=actor, pr_id=args.pr_id)
        return repo.merge(actor=actor, pr_id=args.pr_id, acknowledge_conflicts=args.acknowledge_conflicts)
    if args.cmd == "abandon":
        return repo.abandon(actor=_actor_from(args), episode_id=args.episode, reason=args.reason)
    if args.cmd == "log":
        return [
            {
                "hash": item["hash"],
                "type": item["type"],
                "author": item["author"]["display"],
                "role": item["author"]["role"],
                "recorded_at": item["recorded_at"],
                "message": item["message"],
                "episode_id": item.get("episode_id"),
            }
            for item in repo.log(args.ref, all_parents=args.all)
        ]
    if args.cmd == "status":
        return repo.status()
    if args.cmd == "verify":
        report = repo.verify()
        if not report["ok"]:
            raise IntegrityError("; ".join(report["errors"]))
        return report
    if args.cmd == "export-fhir":
        return repo.export_fhir(args.ref)
    if args.cmd == "show":
        return {"hash": args.digest, **repo.object(args.digest)}
    raise MedGitError(f"unknown command {args.cmd}")


def _actor(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--actor-id", required=True)
    parser.add_argument("--actor-display", required=True)
    parser.add_argument("--actor-role", required=True, choices=["resident", "attending", "system", "auditor"])


def _actor_from(args: argparse.Namespace) -> Actor:
    return Actor(args.actor_id, args.actor_display, args.actor_role)


def _print_human(cmd: str, payload: Any) -> None:
    if cmd == "log" and isinstance(payload, list):
        for item in payload:
            print(
                f"{item['hash'][:12]}  {item['type']:<16} {item['role']:<10} {item['recorded_at']}  {item['message']}"
            )
        return
    if cmd == "verify":
        print(f"ok  objects={payload['objects']}  refs={payload['refs']}")
        return
    if cmd == "status":
        patient = payload["patient"]
        print(f"{patient['display']}  [{patient['patient_ref']}]")
        print(f"main {payload['main']}")
        for episode in payload["episodes"]:
            print(f"episode {episode['id']}  {episode['state']}  {episode['tip'][:12]}")
        for pull in payload["pulls"]:
            print(f"consult {pull['id']}  {pull['state']}  {pull['title']}")
        return
    if isinstance(payload, dict) and "hash" in payload:
        print(f"{payload['hash'][:12]}  {payload.get('type') or payload.get('kind')}  {payload.get('message') or payload.get('title') or ''}")
        return
    print(json.dumps(payload, indent=2, ensure_ascii=False, sort_keys=True))
