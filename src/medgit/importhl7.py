"""Load the official HL7 FHIR R4 f001 examples into a MedGit record.

The resources stay official. MedGit only decides who filed them and in what
order. Decimal numbers are kept as strings so the object hash does not depend
on floating point.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from medgit.repo import VERSION, Actor, MedRepo

DATA = Path(__file__).resolve().parent / "data" / "hl7" / "f001"

ATTENDING = Actor("att-demo", "Attending (demo operator)", "attending")
RESIDENT = Actor("res-demo", "Resident (demo operator)", "resident")

RESOURCE_TYPES = {
    "Observation": "observation",
    "Condition": "note",
    "Procedure": "procedure",
    "ServiceRequest": "order",
    "MedicationRequest": "medication",
    "DiagnosticReport": "result",
    "DocumentReference": "note",
}


def load_official(name: str) -> Any:
    return json.loads((DATA / name).read_text(encoding="utf-8"), parse_float=str)


def patient_display(patient: dict[str, Any]) -> str:
    names = patient.get("name") or [{}]
    chosen = next((item for item in names if item.get("use") == "official"), names[0])
    given = " ".join(chosen.get("given") or [])
    family = chosen.get("family") or ""
    text = chosen.get("text") or f"{given} {family}".strip()
    return text or patient.get("id") or "Patient"


def resource_label(resource: dict[str, Any]) -> str:
    for key in ("code", "medicationCodeableConcept", "type"):
        coded = resource.get(key)
        label = _coded_text(coded)
        if label:
            return label
    return f"{resource.get('resourceType')}/{resource.get('id')}"


def build_f001_demo(root: str | Path, *, clock=None) -> MedRepo:
    """Create the Pieter van de Heuvel record from the vendored HL7 files."""
    patient = load_official("patient-example-f001-pieter.json")
    encounter = load_official("encounter-example-f001-heart.json")
    repo = MedRepo.init(
        root,
        actor=ATTENDING,
        patient_ref=str(patient["id"]),
        display=patient_display(patient),
        gender=str(patient.get("gender") or "unknown"),
        birth_date=str(patient.get("birthDate") or "1970-01-01"),
        clock=clock,
        resources=[patient],
    )
    reason = resource_label(encounter)
    opened = repo.open_episode(
        actor=RESIDENT,
        reason=reason,
        encounter_class=str((encounter.get("class") or {}).get("code") or "AMB"),
        episode_id=str(encounter["id"]),
        resources=[encounter],
    )
    episode_id = opened["episode_id"]
    for name in (
        "condition-example-f001-heart.json",
        "procedure-example-f001-heart.json",
        "observation-example-f001-glucose.json",
    ):
        resource = load_official(name)
        _commit_one(repo, episode_id, resource)
    bundle = load_official("diagnosticreport-example-f001-bloodexam.json")
    for entry in bundle.get("entry") or []:
        resource = entry.get("resource")
        if isinstance(resource, dict) and resource.get("resourceType"):
            _commit_one(repo, episode_id, resource)
    consult = repo.open_pull(
        actor=RESIDENT,
        episode_id=episode_id,
        title="HL7 f001 — integrar episodio cardiológico de ejemplo",
        body="Recursos oficiales FHIR R4. El operador de la demo no es el autor clínico del ejemplo.",
    )
    repo.review(
        actor=ATTENDING,
        pr_id=consult["id"],
        decision="approve",
        comment="Integrar el tip. Los recursos HL7 no se reescriben.",
    )
    repo.merge(actor=ATTENDING, pr_id=consult["id"])
    return repo


def commit_resource(repo: MedRepo, *, actor: Actor, episode_id: str, resource: dict[str, Any]) -> dict[str, Any]:
    normalized = json.loads(json.dumps(resource), parse_float=str)
    return _commit_one(repo, episode_id, normalized, actor=actor)


def _commit_one(repo: MedRepo, episode_id: str, resource: dict[str, Any], *, actor: Actor = RESIDENT) -> dict[str, Any]:
    commit_type = RESOURCE_TYPES.get(str(resource.get("resourceType")), "note")
    return repo.commit(
        actor=actor,
        episode_id=episode_id,
        commit_type=commit_type,
        message=resource_label(resource),
        resources=[resource],
    )


def _coded_text(coded: Any) -> str:
    if isinstance(coded, list):
        coded = coded[0] if coded else None
    if not isinstance(coded, dict):
        return ""
    if isinstance(coded.get("text"), str):
        return coded["text"]
    coding = coded.get("coding") or []
    if coding and isinstance(coding[0], dict):
        return str(coding[0].get("display") or coding[0].get("code") or "")
    return ""


__all__ = ["ATTENDING", "DATA", "RESIDENT", "VERSION", "build_f001_demo", "commit_resource", "load_official"]
