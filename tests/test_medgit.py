"""Reference behaviour for the clinical object log."""

from __future__ import annotations

import hashlib
import json
import subprocess
import sys
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from tempfile import TemporaryDirectory

ROOT = Path(__file__).resolve().parents[1]
SRC = ROOT / "src"
sys.path.insert(0, str(SRC))

from medgit.canonical import canonicalize, object_hash  # noqa: E402
from medgit.errors import IntegrityError, PolicyError, RewriteForbidden  # noqa: E402
from medgit.fhirutil import quantity_observation  # noqa: E402
from medgit.repo import Actor, MedRepo, parse_time  # noqa: E402
from medgit.rules import score_qsofa  # noqa: E402


def clock_from(start: str = "2026-04-02T14:00:00Z"):
    current = parse_time(start)

    def tick() -> str:
        nonlocal current
        stamp = current.strftime("%Y-%m-%dT%H:%M:%SZ")
        current += timedelta(seconds=1)
        return stamp

    return tick


ATTENDING = Actor("att-ramirez", "Dra. Elena Ramírez", "attending")
RESIDENT = Actor("res-ortega", "Dr. Luis Ortega", "resident")
LAB = Actor("bot-lis", "LIS", "system")
OTHER = Actor("att-salas", "Dr. Mateo Salas", "attending")


def init_repo(path: Path, clock=None) -> MedRepo:
    return MedRepo.init(
        path,
        actor=ATTENDING,
        patient_ref="syn-pat-001",
        display="Paciente Sintético Uno",
        gender="female",
        birth_date="1978-04-12",
        clock=clock or clock_from(),
    )


class CanonicalTests(unittest.TestCase):
    def test_key_order_does_not_change_the_hash(self):
        self.assertEqual(object_hash({"b": 1, "a": {"d": True, "c": "ñ"}}), object_hash({"a": {"c": "ñ", "d": True}, "b": 1}))

    def test_floats_are_rejected(self):
        with self.assertRaises(TypeError):
            canonicalize({"value": 1.2})


class RuleTests(unittest.TestCase):
    def test_qsofa_high_band_is_not_a_hidden_probability(self):
        scored = score_qsofa(24, 88, True)
        self.assertEqual(scored.score, 3)
        self.assertEqual(scored.band, "high")
        self.assertIn("not a diagnosis", scored.narrative)
        self.assertEqual(score_qsofa(18, 120, False).score, 0)


class RepoTests(unittest.TestCase):
    def test_longitudinal_record_and_role_rules(self):
        with TemporaryDirectory() as tmp:
            repo = init_repo(Path(tmp))
            self.assertTrue(repo.verify()["ok"])
            with self.assertRaises(PolicyError):
                MedRepo.init(Path(tmp) / "other", actor=RESIDENT, patient_ref="x", display="X")
            episode = repo.open_episode(actor=RESIDENT, reason="Ingreso a urgencias")
            with self.assertRaises(PolicyError):
                repo.commit_note(actor=LAB, episode_id=episode["episode_id"], text="no")
            note = repo.commit_note(
                actor=RESIDENT, episode_id=episode["episode_id"], text="Dolor abdominal y disnea."
            )
            self.assertEqual(note["author"]["role"], "resident")
            vitals = repo.commit_observation(
                actor=RESIDENT,
                episode_id=episode["episode_id"],
                loinc="8480-6",
                display="Systolic blood pressure",
                value="88",
                unit="mmHg",
                ucum="mm[Hg]",
                authored_at="2026-04-02T14:10:00Z",
            )
            repo.commit_observation(
                actor=LAB,
                episode_id=episode["episode_id"],
                loinc="2524-5",
                display="Lactate",
                value="4.2",
                unit="mmol/L",
                ucum="mmol/L",
                authored_at="2026-04-02T14:20:00Z",
                commit_type="result",
            )
            inference = repo.commit_qsofa(
                actor=LAB,
                episode_id=episode["episode_id"],
                respiratory_rate=24,
                systolic_bp=88,
                altered_mentation=True,
            )
            self.assertIn("qSOFA 3/3", inference["message"])
            consult = repo.open_pull(
                actor=RESIDENT,
                episode_id=episode["episode_id"],
                title="qSOFA alto — valoración inmediata",
            )
            with self.assertRaises(PolicyError):
                repo.review(actor=RESIDENT, pr_id=consult["id"], decision="approve")
            with self.assertRaises(PolicyError):
                repo.merge(actor=RESIDENT, pr_id=consult["id"])
            approved = repo.review(
                actor=ATTENDING, pr_id=consult["id"], decision="approve", comment="De acuerdo, integrar."
            )
            self.assertEqual(approved["state"], "approved")
            merged = repo.merge(actor=ATTENDING, pr_id=consult["id"])
            self.assertEqual(merged["type"], "merge")
            self.assertEqual(len(merged["parents"]), 2)
            self.assertEqual(repo.episode_state(episode["episode_id"]), "merged")
            bundle = repo.export_fhir()
            types = [entry["resource"]["resourceType"] for entry in bundle["entry"]]
            self.assertIn("Patient", types)
            self.assertIn("Observation", types)
            self.assertIn("RiskAssessment", types)
            self.assertIn("Provenance", types)
            encounter = next(entry["resource"] for entry in bundle["entry"] if entry["resource"]["resourceType"] == "Encounter")
            self.assertEqual(encounter["status"], "finished")
            lactate = next(
                entry["resource"]
                for entry in bundle["entry"]
                if entry["resource"].get("resourceType") == "Observation"
                and entry["resource"]["code"]["coding"][0]["code"] == "2524-5"
            )
            self.assertEqual(lactate["valueQuantity"]["value"], 4.2)
            self.assertIsInstance(vitals["resources"][0]["valueQuantity"]["value"], str)
            self.assertTrue(repo.verify()["ok"])
            with self.assertRaises(PolicyError):
                repo.commit_note(actor=RESIDENT, episode_id=episode["episode_id"], text="tarde")

    def test_rewrite_operations_do_not_exist(self):
        with TemporaryDirectory() as tmp:
            repo = init_repo(Path(tmp))
            for name in ("amend", "rebase", "reset"):
                with self.assertRaises(RewriteForbidden):
                    getattr(repo, name)()

    def test_author_cannot_approve_their_own_consult(self):
        with TemporaryDirectory() as tmp:
            repo = init_repo(Path(tmp))
            episode = repo.open_episode(actor=ATTENDING, reason="Control")
            repo.commit_note(actor=ATTENDING, episode_id=episode["episode_id"], text="Sin datos de alarma.")
            consult = repo.open_pull(actor=ATTENDING, episode_id=episode["episode_id"], title="Cierre")
            with self.assertRaises(PolicyError):
                repo.review(actor=ATTENDING, pr_id=consult["id"], decision="approve")
            repo.review(actor=OTHER, pr_id=consult["id"], decision="approve")
            repo.merge(actor=ATTENDING, pr_id=consult["id"])
            self.assertEqual(repo.episode_state(episode["episode_id"]), "merged")

    def test_new_commit_drops_a_stale_approval(self):
        with TemporaryDirectory() as tmp:
            repo = init_repo(Path(tmp))
            episode = repo.open_episode(actor=RESIDENT, reason="Observación")
            repo.commit_note(actor=RESIDENT, episode_id=episode["episode_id"], text="Inicio.")
            consult = repo.open_pull(actor=RESIDENT, episode_id=episode["episode_id"], title="Cierre")
            repo.review(actor=ATTENDING, pr_id=consult["id"], decision="approve")
            repo.commit_note(actor=RESIDENT, episode_id=episode["episode_id"], text="Nota tardía.")
            refreshed = repo.pull(consult["id"])
            self.assertEqual(refreshed["state"], "open")
            self.assertEqual(refreshed["reviews"], [])
            with self.assertRaises(PolicyError):
                repo.merge(actor=ATTENDING, pr_id=consult["id"])

    def test_discordant_observations_must_be_acknowledged(self):
        with TemporaryDirectory() as tmp:
            repo = init_repo(Path(tmp))
            first = repo.open_episode(actor=RESIDENT, reason="Primer contacto")
            repo.commit_observation(
                actor=RESIDENT,
                episode_id=first["episode_id"],
                loinc="8480-6",
                display="Systolic blood pressure",
                value="120",
                unit="mmHg",
                ucum="mm[Hg]",
                authored_at="2026-04-02T15:00:00Z",
            )
            consult = repo.open_pull(actor=RESIDENT, episode_id=first["episode_id"], title="Integrar primero")
            repo.review(actor=ATTENDING, pr_id=consult["id"], decision="approve")
            repo.merge(actor=ATTENDING, pr_id=consult["id"])
            second = repo.open_episode(actor=RESIDENT, reason="Revaloración")
            repo.commit_observation(
                actor=RESIDENT,
                episode_id=second["episode_id"],
                loinc="8480-6",
                display="Systolic blood pressure",
                value="80",
                unit="mmHg",
                ucum="mm[Hg]",
                authored_at="2026-04-02T16:00:00Z",
            )
            again = repo.open_pull(actor=RESIDENT, episode_id=second["episode_id"], title="Integrar segundo")
            self.assertEqual(len(again["conflicts"]), 1)
            repo.review(actor=ATTENDING, pr_id=again["id"], decision="approve")
            with self.assertRaises(PolicyError):
                repo.merge(actor=ATTENDING, pr_id=again["id"])
            merged = repo.merge(actor=ATTENDING, pr_id=again["id"], acknowledge_conflicts=True)
            self.assertEqual(len(merged["notices"]), 1)
            values = [
                entry["resource"]["valueQuantity"]["value"]
                for entry in repo.export_fhir()["entry"]
                if entry["resource"].get("resourceType") == "Observation"
            ]
            self.assertCountEqual(values, [120, 80])

    def test_correction_keeps_the_original_and_marks_it(self):
        with TemporaryDirectory() as tmp:
            repo = init_repo(Path(tmp))
            episode = repo.open_episode(actor=RESIDENT, reason="Laboratorio")
            wrong = repo.commit(
                actor=LAB,
                episode_id=episode["episode_id"],
                commit_type="result",
                message="Lactato 2.1",
                resources=[
                    quantity_observation(
                        "lac-1",
                        "syn-pat-001",
                        episode["episode_id"],
                        loinc="2524-5",
                        display="Lactate",
                        value="2.1",
                        unit="mmol/L",
                        ucum="mmol/L",
                        effective="2026-04-02T14:30:00Z",
                    )
                ],
                authored_at="2026-04-02T14:30:00Z",
            )
            repo.commit(
                actor=LAB,
                episode_id=episode["episode_id"],
                commit_type="correction",
                message="Lactato corregido a 4.2",
                corrects=wrong["hash"],
                resources=[
                    quantity_observation(
                        "lac-1-fix",
                        "syn-pat-001",
                        episode["episode_id"],
                        loinc="2524-5",
                        display="Lactate",
                        value="4.2",
                        unit="mmol/L",
                        ucum="mmol/L",
                        effective="2026-04-02T14:30:00Z",
                    )
                ],
                authored_at="2026-04-02T14:40:00Z",
            )
            original = repo.object(wrong["hash"])
            self.assertEqual(original["resources"][0]["valueQuantity"]["value"], "2.1")
            bundle = repo.export_fhir(f"episodes/{episode['episode_id']}")
            statuses = {
                entry["resource"]["id"]: entry["resource"]["status"]
                for entry in bundle["entry"]
                if entry["resource"].get("resourceType") == "Observation"
            }
            self.assertEqual(statuses["lac-1"], "entered-in-error")
            self.assertEqual(statuses["lac-1-fix"], "final")

    def test_tamper_and_backwards_clock_are_detected(self):
        with TemporaryDirectory() as tmp:
            repo = init_repo(Path(tmp), clock=clock_from())
            report = repo.verify()
            self.assertTrue(report["ok"])
            object_id = repo.status()["main"]
            path = Path(tmp) / "objects" / object_id[:2] / object_id[2:]
            blob = bytearray(path.read_bytes())
            blob[-2] = (blob[-2] + 1) % 256
            path.write_bytes(blob)
            broken = repo.verify()
            self.assertFalse(broken["ok"])
        with TemporaryDirectory() as tmp:
            repo = init_repo(Path(tmp), clock=clock_from("2026-04-02T14:00:00Z"))
            episode = repo.open_episode(actor=RESIDENT, reason="Tiempo")
            repo.clock = clock_from("2026-04-02T13:00:00Z")
            with self.assertRaises(IntegrityError):
                repo.commit_note(actor=RESIDENT, episode_id=episode["episode_id"], text="atrás")


class CliTests(unittest.TestCase):
    def test_cli_init_verify_and_refused_rebase(self):
        with TemporaryDirectory() as tmp:
            base = [sys.executable, "-m", "medgit", "-C", tmp]
            env = {**dict(**{k: v for k, v in __import__("os").environ.items()}), "PYTHONPATH": str(SRC)}
            init = subprocess.run(
                base
                + [
                    "init",
                    "--patient-ref",
                    "syn-pat-001",
                    "--display",
                    "Paciente Sintético Uno",
                    "--actor-id",
                    "att-ramirez",
                    "--actor-display",
                    "Dra. Elena Ramírez",
                    "--actor-role",
                    "attending",
                ],
                cwd=ROOT,
                env=env,
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertEqual(init.returncode, 0, init.stderr)
            verified = subprocess.run(base + ["verify"], cwd=ROOT, env=env, text=True, capture_output=True, check=False)
            self.assertEqual(verified.returncode, 0, verified.stderr)
            self.assertIn("ok", verified.stdout)
            refused = subprocess.run(base + ["rebase"], cwd=ROOT, env=env, text=True, capture_output=True, check=False)
            self.assertEqual(refused.returncode, 2)
            self.assertIn("rebase", refused.stderr)


class McpTests(unittest.TestCase):
    def test_verify_tool_over_stdio(self):
        with TemporaryDirectory() as tmp:
            init_repo(Path(tmp))
            request = json.dumps(
                {
                    "jsonrpc": "2.0",
                    "id": 1,
                    "method": "tools/call",
                    "params": {"name": "medgit_verify", "arguments": {}},
                }
            ).encode("utf-8")
            framed = f"Content-Length: {len(request)}\r\n\r\n".encode("ascii") + request
            completed = subprocess.run(
                [sys.executable, "-m", "medgit.mcp_server"],
                cwd=ROOT,
                env={"PYTHONPATH": str(SRC), "MEDGIT_REPO": tmp, "PATH": __import__("os").environ.get("PATH", "")},
                input=framed,
                capture_output=True,
                check=False,
            )
            self.assertEqual(completed.returncode, 0, completed.stderr)
            text = completed.stdout.decode("utf-8")
            self.assertIn("Content-Length:", text)
            body = text.split("\r\n\r\n", 1)[1]
            payload = json.loads(body)
            self.assertFalse(payload["result"]["isError"])
            self.assertIn('"ok": true', payload["result"]["content"][0]["text"])


class HashStabilityTests(unittest.TestCase):
    def test_same_document_is_the_same_object(self):
        with TemporaryDirectory() as tmp:
            repo = init_repo(Path(tmp), clock=lambda: "2026-04-02T14:00:00Z")
            first = repo.status()["main"]
            raw = (Path(tmp) / "objects" / first[:2] / first[2:]).read_bytes()
            self.assertEqual(hashlib.sha256(raw).hexdigest(), first)


if __name__ == "__main__":
    unittest.main()
