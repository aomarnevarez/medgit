"""Content-addressed object store and named refs."""

from __future__ import annotations

import hashlib
import json
import os
import re
from pathlib import Path

from medgit.canonical import canonicalize
from medgit.errors import IntegrityError

_REF_NAME = re.compile(r"^(main|episodes/[A-Za-z0-9_\-]+|pulls/[A-Za-z0-9_\-]+)$")
_HEX64 = re.compile(r"^[0-9a-f]{64}$")


class Store:
    def __init__(self, root: Path):
        self.root = root

    def put(self, document: dict) -> str:
        raw = canonicalize(document)
        digest = hashlib.sha256(raw).hexdigest()
        path = self._object_path(digest)
        if path.exists():
            if path.read_bytes() != raw:
                raise IntegrityError(f"existing object {digest} does not match payload")
            return digest
        path.parent.mkdir(parents=True, exist_ok=True)
        temporary = path.with_suffix(".tmp")
        temporary.write_bytes(raw)
        os.replace(temporary, path)
        return digest

    def get(self, digest: str) -> dict:
        if not _HEX64.match(digest):
            raise IntegrityError(f"not a MedGit object id: {digest}")
        path = self._object_path(digest)
        if not path.is_file():
            raise IntegrityError(f"missing object {digest}")
        raw = path.read_bytes()
        if hashlib.sha256(raw).hexdigest() != digest:
            raise IntegrityError(f"object {digest} was modified on disk")
        try:
            document = json.loads(raw.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise IntegrityError(f"object {digest} is not JSON") from exc
        if canonicalize(document) != raw:
            raise IntegrityError(f"object {digest} is not canonical")
        return document

    def has(self, digest: str) -> bool:
        return _HEX64.match(digest) is not None and self._object_path(digest).is_file()

    def iter_object_ids(self):
        objects = self.root / "objects"
        if not objects.is_dir():
            return
        for path in sorted(objects.rglob("*")):
            if not path.is_file() or path.suffix == ".tmp":
                continue
            prefix = path.parent.name
            digest = prefix + path.name
            if _HEX64.match(digest):
                yield digest

    def read_ref(self, name: str) -> str | None:
        self._check_ref(name)
        path = self.root / "refs" / name
        if not path.is_file():
            return None
        text = path.read_text(encoding="utf-8").strip()
        if not _HEX64.match(text):
            raise IntegrityError(f"ref {name} does not point at an object id")
        return text

    def write_ref(self, name: str, digest: str) -> None:
        self._check_ref(name)
        if not _HEX64.match(digest):
            raise IntegrityError("ref target is not an object id")
        if not self.has(digest):
            raise IntegrityError(f"ref {name} would point at missing object {digest}")
        path = self.root / "refs" / name
        path.parent.mkdir(parents=True, exist_ok=True)
        temporary = path.with_suffix(path.suffix + ".tmp")
        temporary.write_text(digest + "\n", encoding="utf-8")
        os.replace(temporary, path)

    def list_refs(self, prefix: str = "") -> list[str]:
        base = self.root / "refs"
        if not base.is_dir():
            return []
        names: list[str] = []
        for path in sorted(base.rglob("*")):
            if not path.is_file() or path.name.endswith(".tmp"):
                continue
            name = path.relative_to(base).as_posix()
            if prefix and not name.startswith(prefix):
                continue
            names.append(name)
        return names

    def _object_path(self, digest: str) -> Path:
        return self.root / "objects" / digest[:2] / digest[2:]

    @staticmethod
    def _check_ref(name: str) -> None:
        if not _REF_NAME.match(name):
            raise IntegrityError(f"illegal ref name: {name}")
