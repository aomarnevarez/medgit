"""Synthetic emergency-department walkthrough.

No real patient data. Run from the repository root:

    PYTHONPATH=src python examples/sepsis_walkthrough.py
"""

from __future__ import annotations

import tempfile
from pathlib import Path

from medgit.repo import Actor, MedRepo, utc_now

ATTENDING = Actor("att-ramirez", "Dra. Elena Ramírez", "attending")
RESIDENT = Actor("res-ortega", "Dr. Luis Ortega", "resident")
LAB = Actor("bot-lis", "LIS", "system")


def main() -> None:
    with tempfile.TemporaryDirectory() as tmp:
        repo = MedRepo.init(
            Path(tmp),
            actor=ATTENDING,
            patient_ref="syn-pat-001",
            display="Paciente Sintético Uno",
            gender="female",
            birth_date="1978-04-12",
        )
        print(f"record opened at {utc_now()}  main={repo.status()['main'][:12]}")
        episode = repo.open_episode(
            actor=RESIDENT,
            reason="Ingreso a urgencias. Disnea y sospecha de infección.",
            encounter_class="EMER",
        )
        episode_id = episode["episode_id"]
        print(f"episode {episode_id} opened by resident")
        repo.commit_observation(
            actor=RESIDENT,
            episode_id=episode_id,
            loinc="8480-6",
            display="Systolic blood pressure",
            value="88",
            unit="mmHg",
            ucum="mm[Hg]",
        )
        repo.commit_observation(
            actor=RESIDENT,
            episode_id=episode_id,
            loinc="9279-1",
            display="Respiratory rate",
            value="24",
            unit="/min",
            ucum="/min",
        )
        repo.commit_note(
            actor=RESIDENT,
            episode_id=episode_id,
            text="Paciente somnolienta. Se solicita lactato y valoración por adscrito.",
        )
        wrong = repo.commit_observation(
            actor=LAB,
            episode_id=episode_id,
            loinc="2524-5",
            display="Lactate",
            value="2.1",
            unit="mmol/L",
            ucum="mmol/L",
            commit_type="result",
            message="Lactato 2.1 mmol/L",
        )
        repo.commit(
            actor=LAB,
            episode_id=episode_id,
            commit_type="correction",
            message="Corrección de transmisión: lactato 4.2 mmol/L",
            corrects=wrong["hash"],
            resources=[
                {
                    "resourceType": "Observation",
                    "id": "lac-corrected",
                    "status": "final",
                    "code": {
                        "coding": [
                            {
                                "system": "http://loinc.org",
                                "code": "2524-5",
                                "display": "Lactate",
                            }
                        ]
                    },
                    "subject": {"reference": "Patient/syn-pat-001"},
                    "effectiveDateTime": wrong["authored_at"],
                    "valueQuantity": {
                        "value": "4.2",
                        "unit": "mmol/L",
                        "system": "http://unitsofmeasure.org",
                        "code": "mmol/L",
                    },
                }
            ],
        )
        print("lab corrected its own result; the original object is still addressable")
        inference = repo.commit_qsofa(
            actor=LAB,
            episode_id=episode_id,
            respiratory_rate=24,
            systolic_bp=88,
            altered_mentation=True,
        )
        print(inference["message"])
        try:
            repo.rebase()
        except Exception as exc:  # noqa: BLE001 — the point is the refusal
            print(f"rebase refused: {exc}")
        consult = repo.open_pull(
            actor=RESIDENT,
            episode_id=episode_id,
            title="qSOFA 3/3. Solicito integración a la historia longitudinal.",
            body="Regla de cribado, no diagnóstico. Lactato corregido a 4.2.",
        )
        repo.review(
            actor=ATTENDING,
            pr_id=consult["id"],
            decision="approve",
            comment="Integrar. Iniciar paquete de sepsis en el episodio de cuidado.",
        )
        merged = repo.merge(actor=ATTENDING, pr_id=consult["id"])
        report = repo.verify()
        bundle = repo.export_fhir()
        print(f"merged {merged['hash'][:12]}  verify={report['ok']}  bundle_entries={len(bundle['entry'])}")
        print("--- first-parent log ---")
        for row in repo.log("main"):
            print(f"{row['hash'][:12]}  {row['type']:<14} {row['message']}")


if __name__ == "__main__":
    main()
