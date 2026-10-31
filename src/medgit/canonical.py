"""Deterministic JSON for content-addressed clinical objects.

Floats are rejected. Decimal clinical values are stored as strings so two
writers cannot hash the same fact to different digests because of ``1.0``
versus ``1.00`` or binary floating point.
"""

from __future__ import annotations

import hashlib
import json
from typing import Any


def _reject_unstable(value: Any) -> None:
    if value is None or isinstance(value, str):
        return
    if isinstance(value, bool):
        return
    if isinstance(value, int):
        return
    if isinstance(value, float):
        raise TypeError(
            "floats are not allowed in MedGit objects; store decimals as strings"
        )
    if isinstance(value, list):
        for item in value:
            _reject_unstable(item)
        return
    if isinstance(value, dict):
        for key, item in value.items():
            if not isinstance(key, str):
                raise TypeError("object keys must be strings")
            _reject_unstable(item)
        return
    raise TypeError(f"unsupported value type: {type(value).__name__}")


def canonicalize(value: Any) -> bytes:
    """Return UTF-8 JSON with sorted keys and no insignificant whitespace."""
    _reject_unstable(value)
    return json.dumps(
        value, ensure_ascii=False, sort_keys=True, separators=(",", ":")
    ).encode("utf-8")


def object_hash(value: Any) -> str:
    return hashlib.sha256(canonicalize(value)).hexdigest()
