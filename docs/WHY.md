# Why an object log

Clinical software usually models the chart as a mutable row. The row is current. The history is an audit table someone bolted on, with a different shape, and the audit row can be missing when the write and the audit write are two steps.

Git already taught developers to treat the current view as a pointer into an immutable history. That part transfers. The rest does not.

A chart is not a source tree.

- Two blood pressures are not a conflict to resolve by picking a side. They are two facts. MedGit's merge keeps both and, when they disagree inside a short window, makes the attending say so out loud.
- A wrong lactate is not an amended commit. The lab's original transmission is part of the story of what the team saw at 14:30. The correction is a later object.
- A resident's note is not published because they pushed to `main`. It is published because someone else, with the attending role, approved that exact tip.

The implementation stays small so those rules are readable in `internal/repo/repo.go` rather than implied by a pile of services. Standards plug in at the edge: resources are FHIR-shaped, provenance is generated on export, and an MCP server can read the log without becoming the source of truth.

If a later version adds signatures, the signed bytes should be the canonical object that already has a hash. If it adds encryption, it should encrypt those same bytes. Neither feature should change the parent pointer or the correction rule.
