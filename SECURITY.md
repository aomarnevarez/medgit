# Security

## What MedGit claims

- Every stored object is canonical JSON addressed by SHA-256. `verify` rehashes the file bytes and checks that parent links, correction targets, and pull-request tips exist.
- `recorded_at` does not move backwards along a parent edge.
- Refs move forward by writing a new object. The library has no API that rewinds them.
- A system actor cannot correct a human commit.

## What it does not claim

- **Not tamper-proof.** A process that can write the object directory can replace a file and the ref together. `verify` will fail if the hash no longer matches, and will also fail if a ref points at a missing object. It will not fail if an attacker rewrites history and recomputes every descendant hash and ref. Stopping that requires signatures, which are not in 0.2. The hash input is the canonical commit with no signature field, so a later detached signature can cover the same bytes.
- **Not encrypted.** There is no at-rest encryption and no key management in this repository. Do not point it at real PHI. A host that already has a keystore or KMS should treat the object directory as plaintext payload.
- **Not an authorization service.** The CLI and the MCP server believe the actor struct they are given. Put authentication in front of them.
- **Not a medical device.** qSOFA output is a versioned screening rule for the reference flow. It is not a diagnosis and it is not a cleared algorithm.

## Reporting

Open a GitHub issue for defects in the object format or in `verify`. Do not include real patient data in the report. A synthetic fixture is enough.
