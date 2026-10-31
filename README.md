# MedGit

MedGit is a git-inspired object log for clinical work. A patient is a repository, an episode is a branch, a fact is a commit, and an interconsultation is a pull request.

Version 0.2 is the first version that actually runs. The 0.1 repository was the mapping and the intent. This version is an append-only reference implementation: a Python library, a CLI, a FHIR projection, a small MCP server, and tests. It is deliberately not a wrapper around `git`.

MedGit is a methodology and a reference tool. It is not a medical device, it does not diagnose, and it is not a place to put real patient data.

## Why it is not Git

Git is the right metaphor and the wrong storage engine for a chart.

| Operation | Git | MedGit |
| --- | --- | --- |
| Rewrite a fact | `commit --amend` | Forbidden. A `correction` commit points at the original hash. The original stays. |
| Replay history | `rebase` | Forbidden. A merge is a union. Both values remain. |
| Move a branch backwards | `reset`, force-push | Forbidden. Refs only advance. |
| Conflicting lines | A textual conflict | A review notice when the same LOINC code disagrees inside six hours. The attending has to acknowledge it. The values are not collapsed. |
| Author of a merge | Anyone with push rights | An attending, and not the author of the consult. A later commit drops the approval. |

`recorded_at` is the system time and must not move backwards along a parent edge. `authored_at` is the clinical time and may be earlier: late notes are real. Decimal results are stored as strings so `4.2` has one hash, not one hash per floating-point printer. Export to FHIR is a projection: that is where quantity values become JSON numbers, and where a corrected observation is shown as `entered-in-error` without mutating the stored object.

## Map

| Clinical act | Object | Rule in 0.2 |
| --- | --- | --- |
| Patient | Repository | One longitudinal `main` ref, rooted at a `patient-init` commit. |
| Episode of care | Branch | `episode-open` forks from `main`. States: `open`, `in-review`, `merged`, `abandoned`. |
| Note, order, result | Commit | Content-addressed. A lab system may commit `result`, `feature`, and `inference`, and may correct only its own commits. |
| Discharge / close | Merge | Two parents: previous `main`, and the episode tip. Encounter status becomes `finished` only in the FHIR view. |
| Interconsultation | Pull request | Opens against a source tip and a target tip. If either moves, the approval is void. |
| Resident | `resident` | Writes on open episodes. Cannot approve or merge. |
| Attending | `attending` | Approves and merges. Cannot approve their own consult. |
| LIS / PACS / model | `system` | Allow-listed commit types. The reference "model" is the published qSOFA rule `qsofa-ref-1`, not a weight file. |
| Auditor | `auditor` | Read and `verify` only. |

qSOFA here is a screening rule with a version string. The commit stores the inputs in the narrative and a qualitative band (`low`, `moderate`, `high`). It does not invent a probability of sepsis.

## Quick start

No third-party packages. Python 3.10 or newer.

```bash
python -m venv .venv && . .venv/bin/activate
pip install -e .

medgit init ./syn-pat-001 \
  --patient-ref syn-pat-001 \
  --display "Paciente Sintético Uno" \
  --actor-id att-ramirez \
  --actor-display "Dra. Elena Ramírez" \
  --actor-role attending

medgit -C ./syn-pat-001 episode open \
  --reason "Ingreso a urgencias" \
  --actor-id res-ortega \
  --actor-display "Dr. Luis Ortega" \
  --actor-role resident
```

Every write takes an actor. The CLI trusts that actor. Authentication belongs to the system that calls MedGit, not to this repository.

```bash
PYTHONPATH=src python -m unittest discover -s tests -v
PYTHONPATH=src python examples/sepsis_walkthrough.py
```

Useful commands: `observe`, `note`, `qsofa`, `consult open|review|refresh|merge`, `log`, `status`, `verify`, `export-fhir`, `show`. `medgit rebase`, `medgit amend`, and `medgit reset` exist so they can refuse.

## Layout of a record

```text
syn-pat-001/
  medgit.json
  objects/ab/<sha256 rest>     # canonical JSON, hash is the address
  refs/main
  refs/episodes/<episode_id>
  refs/pulls/<pr_id>
```

Objects are never updated in place. A review is a new pull object whose `previous` field points at the prior one.

## FHIR

FHIR is the vocabulary. MedGit is the lifecycle. A commit carries one or more resources. `medgit export-fhir` walks every ancestor of `main` and emits a collection Bundle plus a Provenance resource per commit (`agent`, `recorded`, activity code, and the commit hash as `entity`). The mapping and the decimal-string rule are in [docs/FHIR.md](docs/FHIR.md). The object shape is [docs/SPEC.md](docs/SPEC.md) and [schemas/commit.schema.json](schemas/commit.schema.json).

## MCP

`python -m medgit.mcp_server` speaks MCP over stdio with `Content-Length` framing. `MEDGIT_REPO` selects the record. `medgit_status`, `medgit_log`, `medgit_verify`, and `medgit_export_fhir` are read-only. `medgit_qsofa` also requires `MEDGIT_ALLOW_WRITE=1`. Tool definitions: [docs/MCP.md](docs/MCP.md).

## What this repository will not do

It will not store real PHI. The synthetic identifier system is `https://medgit.dev/synthetic`. It will not encrypt the object directory: encryption is a storage adapter for the host (device keystore, KMS), and pretending otherwise would be a false control. It will not sign commits yet. `verify` detects tampering after the fact; it does not stop a privileged process from rewriting bytes. Threat model: [SECURITY.md](SECURITY.md).

## Roadmap

- [x] CLI
- [x] Commit object and canonical hash
- [x] FHIR Bundle projection and Provenance
- [x] MCP server for read tools plus one guarded write
- [x] Python reference implementation and tests
- [x] Synthetic walkthrough, including a lab correction and a qSOFA consult
- [x] Role rules, stale-approval invalidation, discordant-value notices
- [ ] Detached signatures (the hash is the signed payload)
- [ ] Encryption-at-rest adapter interface
- [ ] Multi-patient index that never embeds one record inside another
- [ ] LOINC/UCUM validation beyond the structural check

## Contributing

Clinical reviewers, implementers, and people who will argue with the merge rule are all useful. See [CONTRIBUTING.md](CONTRIBUTING.md). Please do not file a feature request for rebase.

## License

MIT. See [LICENSE](LICENSE).
