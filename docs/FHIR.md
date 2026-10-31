# FHIR projection

MedGit stores a FHIR-shaped subset and exports FHIR R4 JSON. The subset is what gets hashed. The export is not hashed, which is why it is allowed to contain JSON numbers.

## Stored subset

- `Quantity.value` is a decimal string. The export parses it to a JSON number.
- `Patient`, `Encounter`, `Observation`, `DocumentReference`, and `RiskAssessment` are the resources the reference helpers build. `commit()` accepts any object with a `resourceType`; unknown resources are preserved and re-emitted.
- No `meta.versionId`. The MedGit hash is the version.

## Identifiers in the synthetic record

| Thing | Id |
| --- | --- |
| Patient | the `patient_ref`, also an identifier with system `https://medgit.dev/synthetic` |
| Encounter | the episode id |
| Provenance | `prov-` plus the first 16 hex chars of the commit |

The patient resource carries `meta.security` code `synthetic`. That is a label, not a control.

## What export changes on purpose

- A corrected resource with a `status` is emitted as `entered-in-error` and gains extension `https://medgit.dev/fhir/StructureDefinition/corrected-by`.
- An `Encounter` whose episode has a `merge` commit on `main` is emitted as `finished`, with `period.end` set to that merge's `recorded_at`. The stored encounter is still `in-progress`. Admission did not know the discharge time, and the object is not rewritten to pretend it did.
- Every commit produces a `Provenance`:
  - `recorded` = `recorded_at`
  - `occurredDateTime` = `authored_at`
  - `activity.coding.system` = `https://medgit.dev/activity`, code = the commit type
  - `agent.who` = actor display and id
  - `entity.role` = `source`, identifier system `https://medgit.dev/commit`, value = the full hash

The Bundle type is `collection`. It is a read model, not a transaction to replay onto a server blindly: consumers that care about corrections must honour `entered-in-error`.

## Codes the reference flow uses

| Fact | Code |
| --- | --- |
| Systolic blood pressure | LOINC `8480-6` |
| Respiratory rate | LOINC `9279-1` |
| Lactate | LOINC `2524-5` |
| qSOFA rule | `https://medgit.dev/rules` code `qsofa-ref-1` |
| Qualitative band | HL7 risk-probability `low`, `moderate`, `high` |

There is no `probabilityDecimal` on the RiskAssessment. The rule does not have one.
