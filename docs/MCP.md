# MCP server

The reference server is a single process, one record, stdio transport with LSP-style framing:

```text
Content-Length: <bytes>\r\n
\r\n
{json}
```

```bash
MEDGIT_REPO=./syn-pat-001 python -m medgit.mcp_server
```

`initialize` answers protocol `2024-11-05` and server name `medgit`. `tools/list` returns the tools below. `tools/call` returns MCP `content` text. Domain errors come back as `isError: true` rather than a JSON-RPC error, so a client can show them as a refused clinical action.

| Tool | Effect |
| --- | --- |
| `medgit_status` | Patient, episode states, consult states. |
| `medgit_log` | `ref` (default `main`), `all_parents` optional. |
| `medgit_verify` | Full rehash and link check. Read this before trusting a record an agent did not create. |
| `medgit_export_fhir` | Collection Bundle of `main`. |
| `medgit_qsofa` | Writes an `inference` commit. Refused unless `MEDGIT_ALLOW_WRITE=1`. Arguments: `episode_id`, `respiratory_rate`, `systolic_bp`, `altered_mentation`, `actor_id`, `actor_display`, `actor_role`. |
| `medgit_load_demo` | Builds the official HL7 R4 f001 record into `MEDGIT_REPO`. Refuses if a record is already there. Requires `MEDGIT_ALLOW_WRITE=1`. |
| `medgit_commit_fhir` | Appends one FHIR resource to an open episode. Requires `MEDGIT_ALLOW_WRITE=1`. Does not merge. |

The server does not check a password. An agent runtime that wants an attending merge has to enforce that outside this process. v0.2 exposes one write tool on purpose: a screening rule with a fixed definition. Opening arbitrary commits from a model is a later decision, and it should not skip the consult.

A typical agent loop that fits the object model:

1. `medgit_log` on the episode.
2. `medgit_qsofa` if the vitals are already committed and the actor is `system` or `attending`.
3. Stop. A human opens the consult. The agent does not merge.
