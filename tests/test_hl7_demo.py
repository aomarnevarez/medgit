"""The official HL7 R4 f001 examples survive a MedGit round trip."""

from __future__ import annotations

import json
import os
import sys
import threading
import unittest
import urllib.request
from http.server import ThreadingHTTPServer
from pathlib import Path
from tempfile import TemporaryDirectory

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "src"))

from medgit.importhl7 import build_f001_demo  # noqa: E402
from medgit.server import Handler, ensure_demo  # noqa: E402


class Hl7DemoTests(unittest.TestCase):
    def test_official_resources_round_trip(self):
        with TemporaryDirectory() as tmp:
            repo = build_f001_demo(Path(tmp))
            report = repo.verify()
            self.assertTrue(report["ok"], report["errors"])
            self.assertEqual(repo.config()["patient_ref"], "f001")
            self.assertIn("Heuvel", repo.config()["display"])
            bundle = repo.export_fhir()
            by_type: dict[str, list] = {}
            for entry in bundle["entry"]:
                by_type.setdefault(entry["resource"]["resourceType"], []).append(entry["resource"])
            self.assertEqual(by_type["Patient"][0]["id"], "f001")
            glucose = next(
                item
                for item in by_type["Observation"]
                if item["code"]["coding"][0]["code"] == "15074-8"
            )
            self.assertEqual(glucose["valueQuantity"]["value"], 6.3)
            self.assertIsInstance(glucose["valueQuantity"]["value"], float)
            stored = next(
                commit
                for commit in repo.log(f"episodes/f001")
                if any(
                    resource.get("resourceType") == "Observation"
                    for resource in commit["resources"]
                )
            )
            self.assertEqual(stored["resources"][0]["valueQuantity"]["value"], "6.3")
            messages = [row["message"] for row in repo.log("main", all_parents=True)]
            self.assertTrue(any("Heart valve" in message for message in messages))
            self.assertEqual(by_type["Encounter"][0]["status"], "finished")

    def test_http_control_plane_serves_the_demo(self):
        with TemporaryDirectory() as tmp:
            os.environ["MEDGIT_DATA"] = tmp
            ensure_demo()
            server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            port = server.server_address[1]
            try:
                health = json.load(urllib.request.urlopen(f"http://127.0.0.1:{port}/health"))
                self.assertTrue(health["ok"])
                verified = json.load(urllib.request.urlopen(f"http://127.0.0.1:{port}/records/f001/verify"))
                self.assertTrue(verified["ok"])
                listing = json.load(urllib.request.urlopen(f"http://127.0.0.1:{port}/records"))
                self.assertEqual(listing["records"], ["f001"])
            finally:
                server.shutdown()


if __name__ == "__main__":
    unittest.main()
