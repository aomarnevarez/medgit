"""Minimal MCP server (stdio, Content-Length framing) over one local record.

Write tools stay off unless MEDGIT_ALLOW_WRITE=1. The actor still comes from
the caller; this process does not authenticate anyone.
"""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path
from typing import Any

from medgit.errors import MedGitError
from medgit.repo import VERSION, Actor, MedRepo

PROTOCOL = "2024-11-05"


def main() -> int:
    server = Server(os.environ.get("MEDGIT_REPO", "."))
    while True:
        message = _read_message()
        if message is None:
            return 0
        response = server.handle(message)
        if response is not None:
            _write_message(response)
    return 0


class Server:
    def __init__(self, root: str):
        self.root = root

    def handle(self, message: dict[str, Any]) -> dict[str, Any] | None:
        method = message.get("method")
        if message.get("id") is None and method and method.startswith("notifications/"):
            return None
        request_id = message.get("id")
        try:
            if method == "initialize":
                result = {
                    "protocolVersion": PROTOCOL,
                    "capabilities": {"tools": {"listChanged": False}},
                    "serverInfo": {"name": "medgit", "version": VERSION},
                }
            elif method == "tools/list":
                result = {"tools": _tools()}
            elif method == "tools/call":
                params = message.get("params") or {}
                result = self._call(params.get("name"), params.get("arguments") or {})
            elif method == "ping":
                result = {}
            else:
                return _error(request_id, -32601, f"unknown method {method}")
        except MedGitError as exc:
            return _result(request_id, _tool_text(str(exc), is_error=True))
        except Exception as exc:  # noqa: BLE001 — surface a tool error, don't kill the server
            return _result(request_id, _tool_text(str(exc), is_error=True))
        return _result(request_id, result)

    def _call(self, name: str | None, arguments: dict[str, Any]) -> dict[str, Any]:
        repo = MedRepo(self.root)
        if name == "medgit_status":
            return _tool_text(json.dumps(repo.status(), indent=2, sort_keys=True))
        if name == "medgit_log":
            ref = arguments.get("ref", "main")
            rows = repo.log(ref, all_parents=bool(arguments.get("all_parents", False)))
            brief = [
                {
                    "hash": row["hash"],
                    "type": row["type"],
                    "message": row["message"],
                    "author": row["author"]["display"],
                    "recorded_at": row["recorded_at"],
                }
                for row in rows
            ]
            return _tool_text(json.dumps(brief, indent=2))
        if name == "medgit_verify":
            return _tool_text(json.dumps(repo.verify(), indent=2))
        if name == "medgit_export_fhir":
            return _tool_text(json.dumps(repo.export_fhir(), indent=2))
        if name == "medgit_qsofa":
            _require_write()
            actor = _actor(arguments)
            committed = repo.commit_qsofa(
                actor=actor,
                episode_id=arguments["episode_id"],
                respiratory_rate=int(arguments["respiratory_rate"]),
                systolic_bp=int(arguments["systolic_bp"]),
                altered_mentation=bool(arguments["altered_mentation"]),
            )
            return _tool_text(committed["hash"] + " " + committed["message"])
        if name == "medgit_load_demo":
            _require_write()
            from medgit.importhl7 import build_f001_demo

            if (Path(self.root) / "medgit.json").is_file():
                raise MedGitError("record already exists; refusing to overwrite it")
            built = build_f001_demo(self.root)
            report = built.verify()
            return _tool_text(json.dumps({"patient": built.config()["display"], "verify": report}, indent=2))
        if name == "medgit_commit_fhir":
            _require_write()
            from medgit.importhl7 import commit_resource

            resource = arguments.get("resource")
            if not isinstance(resource, dict):
                raise MedGitError("resource must be a FHIR object")
            committed = commit_resource(
                repo,
                actor=_actor(arguments),
                episode_id=str(arguments["episode_id"]),
                resource=resource,
            )
            return _tool_text(committed["hash"] + " " + committed["message"])
        raise MedGitError(f"unknown tool {name}")


def _tools() -> list[dict[str, Any]]:
    return [
        _tool("medgit_status", "Patient, episodes, and open consults.", {}),
        _tool(
            "medgit_log",
            "First-parent log, or the full ancestor set when all_parents is true.",
            {
                "ref": {"type": "string"},
                "all_parents": {"type": "boolean"},
            },
        ),
        _tool("medgit_verify", "Rehash every object and check the parent chain.", {}),
        _tool("medgit_export_fhir", "Project the longitudinal record to a FHIR collection Bundle.", {}),
        _tool(
            "medgit_qsofa",
            "Commit a qSOFA screening result. Requires MEDGIT_ALLOW_WRITE=1.",
            {
                "episode_id": {"type": "string"},
                "respiratory_rate": {"type": "integer"},
                "systolic_bp": {"type": "integer"},
                "altered_mentation": {"type": "boolean"},
                "actor_id": {"type": "string"},
                "actor_display": {"type": "string"},
                "actor_role": {"type": "string"},
            },
            ["episode_id", "respiratory_rate", "systolic_bp", "altered_mentation", "actor_id", "actor_display", "actor_role"],
        ),
        _tool(
            "medgit_load_demo",
            "Build the official HL7 FHIR R4 f001 record in MEDGIT_REPO. Refuses if a record is already there. Requires MEDGIT_ALLOW_WRITE=1.",
            {},
        ),
        _tool(
            "medgit_commit_fhir",
            "Append one FHIR resource to an open episode. Requires MEDGIT_ALLOW_WRITE=1. Does not merge.",
            {
                "episode_id": {"type": "string"},
                "resource": {"type": "object"},
                "actor_id": {"type": "string"},
                "actor_display": {"type": "string"},
                "actor_role": {"type": "string"},
            },
            ["episode_id", "resource", "actor_id", "actor_display", "actor_role"],
        ),
    ]


def _tool(name: str, description: str, properties: dict[str, Any], required: list[str] | None = None) -> dict[str, Any]:
    return {
        "name": name,
        "description": description,
        "inputSchema": {
            "type": "object",
            "properties": properties,
            "required": required or [],
            "additionalProperties": False,
        },
    }


def _actor(arguments: dict[str, Any]) -> Actor:
    return Actor(arguments["actor_id"], arguments["actor_display"], arguments["actor_role"])


def _require_write() -> None:
    if os.environ.get("MEDGIT_ALLOW_WRITE") != "1":
        raise MedGitError("write tools are disabled; set MEDGIT_ALLOW_WRITE=1")


def _tool_text(text: str, *, is_error: bool = False) -> dict[str, Any]:
    return {"content": [{"type": "text", "text": text}], "isError": is_error}


def _result(request_id: Any, result: dict[str, Any]) -> dict[str, Any]:
    return {"jsonrpc": "2.0", "id": request_id, "result": result}


def _error(request_id: Any, code: int, message: str) -> dict[str, Any]:
    return {"jsonrpc": "2.0", "id": request_id, "error": {"code": code, "message": message}}


def _read_message() -> dict[str, Any] | None:
    headers: dict[str, str] = {}
    while True:
        line = sys.stdin.buffer.readline()
        if line == b"":
            return None
        if line in (b"\r\n", b"\n"):
            break
        decoded = line.decode("utf-8").strip()
        if ":" not in decoded:
            continue
        key, value = decoded.split(":", 1)
        headers[key.lower()] = value.strip()
    length = int(headers.get("content-length", "0"))
    if length <= 0:
        return None
    body = sys.stdin.buffer.read(length)
    return json.loads(body.decode("utf-8"))


def _write_message(payload: dict[str, Any]) -> None:
    raw = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    sys.stdout.buffer.write(f"Content-Length: {len(raw)}\r\n\r\n".encode("ascii"))
    sys.stdout.buffer.write(raw)
    sys.stdout.buffer.flush()


if __name__ == "__main__":
    raise SystemExit(main())

