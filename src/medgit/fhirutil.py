"""FHIR R4 builders and the export projection.

Stored resources keep decimal quantities as strings so the object hash is
stable. ``project_resource`` is the only place those strings become JSON
numbers, and that projection is never hashed.
"""

from __future__ import annotations

import copy
import re
from typing import Any

from medgit.errors import IntegrityError

_DECIMAL = re.compile(r"^-?\d+(\.\d+)?$")
ACTIVITY_SYSTEM = "https://medgit.dev/activity"
COMMIT_SYSTEM = "https://medgit.dev/commit"
RULE_SYSTEM = "https://medgit.dev/rules"
CORRECTION_EXT = "https://medgit.dev/fhir/StructureDefinition/corrected-by"


def patient_resource(
    patient_ref: str, display: str, *, gender: str = "unknown", birth_date: str = "1978-04-12"
) -> dict[str, Any]:
    parts = display.split()
    family = parts[-1] if parts else display
    given = parts[:-1] or [display]
    return {
        "resourceType": "Patient",
        "id": patient_ref,
        "identifier": [
            {
                "system": "https://medgit.dev/synthetic",
                "value": patient_ref,
            }
        ],
        "name": [{"use": "official", "family": family, "given": given, "text": display}],
        "gender": gender,
        "birthDate": birth_date,
        "meta": {"security": [{"system": "https://medgit.dev/synthetic", "code": "synthetic"}]},
    }


def encounter_resource(
    episode_id: str, patient_ref: str, reason: str, encounter_class: str, start: str
) -> dict[str, Any]:
    return {
        "resourceType": "Encounter",
        "id": episode_id,
        "status": "in-progress",
        "class": {
            "system": "http://terminology.hl7.org/CodeSystem/v3-ActCode",
            "code": encounter_class,
        },
        "subject": {"reference": f"Patient/{patient_ref}"},
        "reasonCode": [{"text": reason}],
        "period": {"start": start},
    }


def quantity_observation(
    resource_id: str,
    patient_ref: str,
    episode_id: str,
    *,
    loinc: str,
    display: str,
    value: str,
    unit: str,
    ucum: str,
    effective: str,
) -> dict[str, Any]:
    if not _DECIMAL.match(value):
        raise IntegrityError(f"observation value must be a decimal string, got {value!r}")
    return {
        "resourceType": "Observation",
        "id": resource_id,
        "status": "final",
        "code": {"coding": [{"system": "http://loinc.org", "code": loinc, "display": display}]},
        "subject": {"reference": f"Patient/{patient_ref}"},
        "encounter": {"reference": f"Encounter/{episode_id}"},
        "effectiveDateTime": effective,
        "valueQuantity": {
            "value": value,
            "unit": unit,
            "system": "http://unitsofmeasure.org",
            "code": ucum,
        },
    }


def document_reference(
    resource_id: str, patient_ref: str, episode_id: str, *, text: str, author: str, when: str
) -> dict[str, Any]:
    return {
        "resourceType": "DocumentReference",
        "id": resource_id,
        "status": "current",
        "type": {"text": "Progress note"},
        "subject": {"reference": f"Patient/{patient_ref}"},
        "context": {"encounter": [{"reference": f"Encounter/{episode_id}"}]},
        "date": when,
        "description": text,
        "author": [{"display": author}],
    }


def risk_assessment(
    resource_id: str,
    patient_ref: str,
    episode_id: str,
    *,
    score: int,
    band: str,
    narrative: str,
    when: str,
) -> dict[str, Any]:
    return {
        "resourceType": "RiskAssessment",
        "id": resource_id,
        "status": "final",
        "subject": {"reference": f"Patient/{patient_ref}"},
        "encounter": {"reference": f"Encounter/{episode_id}"},
        "occurrenceDateTime": when,
        "method": {
            "coding": [
                {
                    "system": RULE_SYSTEM,
                    "code": "qsofa-ref-1",
                    "display": "qSOFA reference rule (screening, not a diagnosis)",
                }
            ]
        },
        "prediction": [
            {
                "outcome": {"text": f"qSOFA {score}/3"},
                "qualitativeRisk": {
                    "coding": [
                        {
                            "system": "http://terminology.hl7.org/CodeSystem/risk-probability",
                            "code": band,
                        }
                    ]
                },
            }
        ],
        "note": [{"text": narrative}],
    }


def project_resource(resource: dict[str, Any], *, corrected_by: str | None = None) -> dict[str, Any]:
    """Standards-shaped copy. Not an input to the object hash."""
    projected = copy.deepcopy(resource)
    _restore_quantities(projected)
    if corrected_by:
        if "status" in projected:
            projected["status"] = "entered-in-error"
        extensions = list(projected.get("extension") or [])
        extensions.append({"url": CORRECTION_EXT, "valueString": corrected_by})
        projected["extension"] = extensions
    return projected


def provenance(
    commit_hash: str,
    commit: dict[str, Any],
    target_refs: list[str],
) -> dict[str, Any]:
    author = commit["author"]
    return {
        "resourceType": "Provenance",
        "id": f"prov-{commit_hash[:16]}",
        "target": [{"reference": ref} for ref in target_refs] or [
            {"identifier": {"system": COMMIT_SYSTEM, "value": commit_hash}}
        ],
        "recorded": commit["recorded_at"],
        "occurredDateTime": commit["authored_at"],
        "activity": {
            "coding": [{"system": ACTIVITY_SYSTEM, "code": commit["type"]}]
        },
        "agent": [
            {
                "type": {"text": author["role"]},
                "who": {
                    "display": author["display"],
                    "identifier": {"system": "https://medgit.dev/actor", "value": author["id"]},
                },
            }
        ],
        "entity": [
            {
                "role": "source",
                "what": {"identifier": {"system": COMMIT_SYSTEM, "value": commit_hash}},
            }
        ],
    }


def _restore_quantities(node: Any) -> None:
    if isinstance(node, dict):
        raw = node.get("value")
        if isinstance(raw, str) and "unit" in node and _DECIMAL.match(raw):
            node["value"] = _as_number(raw)
        for child in node.values():
            _restore_quantities(child)
    elif isinstance(node, list):
        for child in node:
            _restore_quantities(child)


def _as_number(value: str) -> int | float:
    if not _DECIMAL.match(value):
        raise IntegrityError(f"cannot project decimal {value!r}")
    if "." in value:
        return float(value)
    return int(value)
