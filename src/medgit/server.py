"""HTTP control plane for one directory of MedGit records.

Meant to run in a container (Cloud Run, Fly, a hospital VM). It does not
authenticate. Put a proxy in front before it can see anything but the demo.
"""

from __future__ import annotations

import json
import os
import re
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.parse import urlparse

from medgit.errors import MedGitError
from medgit.importhl7 import build_f001_demo
from medgit.repo import VERSION, Actor, MedRepo

_ID = re.compile(r"^[A-Za-z0-9_\-]{1,64}$")
MAX_BODY = 1_000_000


def data_root() -> Path:
    return Path(os.environ.get("MEDGIT_DATA", "./data"))


def ensure_demo() -> MedRepo:
    root = data_root() / "f001"
    if (root / "medgit.json").is_file():
        return MedRepo(root)
    root.parent.mkdir(parents=True, exist_ok=True)
    return build_f001_demo(root)


def main() -> None:
    ensure_demo()
    port = int(os.environ.get("PORT", "8080"))
    server = ThreadingHTTPServer(("0.0.0.0", port), Handler)
    print(f"medgit {VERSION} listening on {port}", flush=True)
    server.serve_forever()


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self) -> None:  # noqa: N802
        path = urlparse(self.path).path.rstrip("/") or "/"
        try:
            if path == "/health":
                self._json(200, {"ok": True, "medgit": VERSION})
                return
            if path == "/records":
                root = data_root()
                names = []
                if root.is_dir():
                    names = sorted(item.name for item in root.iterdir() if (item / "medgit.json").is_file())
                self._json(200, {"records": names})
                return
            record, rest = _split(path)
            if record is None:
                self._json(404, {"error": "not found"})
                return
            repo = MedRepo(data_root() / record)
            if rest == "":
                self._json(200, repo.status())
            elif rest == "/log":
                self._json(200, [_brief(row) for row in repo.log("main", all_parents=True)])
            elif rest == "/verify":
                self._json(200, repo.verify())
            elif rest == "/fhir":
                self._json(200, repo.export_fhir())
            else:
                self._json(404, {"error": "not found"})
        except (MedGitError, FileNotFoundError, json.JSONDecodeError) as exc:
            self._json(400, {"error": str(exc)})

    def do_POST(self) -> None:  # noqa: N802
        path = urlparse(self.path).path.rstrip("/") or "/"
        try:
            body = self._body()
            if path == "/records":
                self._create(body)
                return
            record, rest = _split(path)
            if record is None:
                self._json(404, {"error": "not found"})
                return
            repo = MedRepo(data_root() / record)
            actor = _actor(body)
            if rest == "/episode":
                opened = repo.open_episode(actor=actor, reason=str(body.get("reason") or "episode"))
                self._json(201, {"episode_id": opened["episode_id"], "hash": opened["hash"]})
            elif rest == "/commit":
                committed = repo.commit(
                    actor=actor,
                    episode_id=str(body["episode_id"]),
                    commit_type=str(body.get("type") or "note"),
                    message=str(body.get("message") or "commit"),
                    resources=list(body.get("resources") or []),
                )
                self._json(201, {"hash": committed["hash"]})
            elif rest == "/consult":
                opened = repo.open_pull(
                    actor=actor,
                    episode_id=str(body["episode_id"]),
                    title=str(body.get("title") or "consult"),
                    body=str(body.get("body") or ""),
                )
                self._json(201, {"id": opened["id"], "state": opened["state"]})
            elif rest == "/review":
                reviewed = repo.review(
                    actor=actor,
                    pr_id=str(body["pr_id"]),
                    decision=str(body.get("decision") or "comment"),
                    comment=str(body.get("comment") or ""),
                )
                self._json(200, {"id": reviewed["id"], "state": reviewed["state"]})
            elif rest == "/merge":
                merged = repo.merge(
                    actor=actor,
                    pr_id=str(body["pr_id"]),
                    acknowledge_conflicts=bool(body.get("acknowledge_conflicts")),
                )
                self._json(200, {"hash": merged["hash"]})
            else:
                self._json(404, {"error": "not found"})
        except (MedGitError, FileNotFoundError, KeyError, json.JSONDecodeError) as exc:
            self._json(400, {"error": str(exc)})

    def log_message(self, fmt: str, *args: Any) -> None:
        print(f"{self.command} {urlparse(self.path).path} {args[1] if len(args) > 1 else ''}", flush=True)

    def _create(self, body: dict[str, Any]) -> None:
        from medgit.importhl7 import commit_resource

        patient = body.get("patient")
        if not isinstance(patient, dict) or patient.get("resourceType") != "Patient" or not patient.get("id"):
            raise MedGitError("patient must be a FHIR Patient with an id")
        record = str(patient["id"])
        if not _ID.match(record):
            raise MedGitError("patient id is not a safe record id")
        target = data_root() / record
        if (target / "medgit.json").is_file():
            raise MedGitError("record already exists")
        names = patient.get("name") or [{}]
        display = (names[0].get("text") if isinstance(names[0], dict) else None) or record
        repo = MedRepo.init(
            target,
            actor=_actor(body),
            patient_ref=record,
            display=str(display),
            gender=str(patient.get("gender") or "unknown"),
            birth_date=str(patient.get("birthDate") or "1970-01-01"),
            resources=[patient],
        )
        episode_id = None
        for resource in body.get("resources") or []:
            if not isinstance(resource, dict):
                continue
            if resource.get("resourceType") == "Encounter" and episode_id is None:
                opened = repo.open_episode(
                    actor=_actor(body),
                    reason="imported encounter",
                    episode_id=str(resource.get("id") or "imported"),
                    resources=[resource],
                )
                episode_id = opened["episode_id"]
                continue
            if episode_id is None:
                opened = repo.open_episode(actor=_actor(body), reason="imported resources")
                episode_id = opened["episode_id"]
            commit_resource(repo, actor=_actor(body), episode_id=episode_id, resource=resource)
        self._json(201, {"id": record, "episode_id": episode_id})

    def _body(self) -> dict[str, Any]:
        length = int(self.headers.get("Content-Length", "0") or 0)
        if length > MAX_BODY:
            raise MedGitError("body too large")
        if length == 0:
            return {}
        raw = self.rfile.read(length)
        parsed = json.loads(raw.decode("utf-8"), parse_float=str)
        if not isinstance(parsed, dict):
            raise MedGitError("JSON object required")
        return parsed

    def _json(self, status: int, payload: dict[str, Any]) -> None:
        raw = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


def _split(path: str) -> tuple[str | None, str]:
    parts = path.strip("/").split("/")
    if len(parts) < 2 or parts[0] != "records" or not _ID.match(parts[1]):
        return None, ""
    rest = "/" + "/".join(parts[2:]) if len(parts) > 2 else ""
    return parts[1], rest


def _actor(body: dict[str, Any]) -> Actor:
    raw = body.get("actor") or {}
    return Actor(
        str(raw.get("id") or "att-demo"),
        str(raw.get("display") or "Attending (demo operator)"),
        str(raw.get("role") or "attending"),
    )


def _brief(row: dict[str, Any]) -> dict[str, Any]:
    return {
        "hash": row["hash"],
        "type": row["type"],
        "message": row["message"],
        "author": row["author"]["display"],
        "recorded_at": row["recorded_at"],
    }


if __name__ == "__main__":
    main()
