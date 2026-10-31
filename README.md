# MedGit

MedGit keeps one patient chart in one folder. You add a fact. A fact that is already stored stays as it was written.

A resident can write a visit. That visit joins the patient's record when a different attending accepts that exact version.

The program is written in Go. The name is MedGit, and the program does not call the `git` command. It is a reference implementation for synthetic charts. Real patient data does not belong in it.

## Words

| Word in the commands | Meaning |
| --- | --- |
| The folder you pass to `init` or `-C` | One patient. |
| `main` | The accepted chart. |
| episode | One visit. It stays off `main` until an attending accepts it. |
| commit | One fact, such as a note or a result. The file is never edited. Its id is a hash. |
| consult | A request: please accept this visit. |

## Build

You need Go 1.22 or newer. The module has no other libraries.

```bash
go build -o medgit ./cmd/medgit
```

`./medgit` with no arguments prints the command list.

Every write names the person in `--actor-id`, `--actor-display`, and `--actor-role`. MedGit stores that name as given. Checking identity belongs to the program that calls MedGit.

## A synthetic visit

Run the steps in order. When a step prints an id, copy that id into the next step.

`EPISODE` and `CONSULT` in the later commands are those copies. The sample ids `ep_0123456789ab` and `pr_0123456789ab` are only there to show the shape. Yours will differ.

### 1. Create the patient

```bash
./medgit init ./syn-pat-001 \
  --patient-ref syn-pat-001 \
  --display "Synthetic Patient One" \
  --actor-id att-ramirez \
  --actor-display "Elena Ramirez, MD" \
  --actor-role attending
```

```text
created ./syn-pat-001
main 30615197d689b1ab03e7ee369f727657cac7025a2feb87cc12c0eb59a7c06b51
```

`created` is the folder. `main` is the hash of the first fact. The later commands add `-C ./syn-pat-001` so they write in that folder.

### 2. Start a visit

```bash
./medgit -C ./syn-pat-001 episode open \
  --reason "Emergency department admission" \
  --actor-id res-ortega \
  --actor-display "Luis Ortega, MD" \
  --actor-role resident
```

```text
e6fdb41b918f  episode-open  Emergency department admission
episode ep_0123456789ab
```

The second line is the visit id. Copy it. Below, that copy is `EPISODE`.

### 3. Add a note

The note is saved on the visit. It is not on `main` yet.

```bash
./medgit -C ./syn-pat-001 note \
  --episode EPISODE \
  --text "Abdominal pain and dyspnea." \
  --actor-id res-ortega \
  --actor-display "Luis Ortega, MD" \
  --actor-role resident
```

```text
5d13fcc836ab  note  Abdominal pain and dyspnea.
```

### 4. Ask an attending to accept the visit

```bash
./medgit -C ./syn-pat-001 consult open \
  --episode EPISODE \
  --title "Admit and integrate" \
  --actor-id res-ortega \
  --actor-display "Luis Ortega, MD" \
  --actor-role resident
```

```text
2e617398821f  pull  Admit and integrate
consult pr_0123456789ab
```

The second line is the request id. Copy it. Below, that copy is `CONSULT`.

### 5. Accept the visit

Elena can accept this request because she did not open it. A resident cannot approve or merge. The attending who opened a request cannot approve that same request.

```bash
./medgit -C ./syn-pat-001 consult review CONSULT \
  --decision approve \
  --comment "Agreed, integrate." \
  --actor-id att-ramirez \
  --actor-display "Elena Ramirez, MD" \
  --actor-role attending

./medgit -C ./syn-pat-001 consult merge CONSULT \
  --actor-id att-ramirez \
  --actor-display "Elena Ramirez, MD" \
  --actor-role attending
```

Review prints the consult id again. Merge prints one line whose type is `merge`.

### 6. Read the chart

```bash
./medgit -C ./syn-pat-001 status
./medgit -C ./syn-pat-001 log --all
./medgit -C ./syn-pat-001 verify
```

`status`:

```text
Synthetic Patient One  [syn-pat-001]
main <hash of main>
episode EPISODE  merged  <hash of the note>
consult CONSULT  merged  Admit and integrate
```

`merged` means the visit is now part of the accepted chart.

`log` with no extra flag follows one parent of each fact. After a merge, that is the merge line and the line that created the patient. The note sits on the other parent, so use `log --all`. Newest line first:

```text
<hash>  merge            attending  <time>  Merge CONSULT: Admit and integrate
<hash>  note             resident   <time>  Abdominal pain and dyspnea.
<hash>  episode-open     resident   <time>  Emergency department admission
<hash>  patient-init     attending  <time>  Open longitudinal record for Synthetic Patient One
```

`verify` rehashes every file and checks the links. After the steps above:

```text
ok  objects=7  refs=3
```

Add `--json` to any command when you want the stored object instead of these lines.

The rest of this page is reference.

## Rules

1. A correction is a new fact that names the hash of the old fact. A FHIR export labels the old observation `entered-in-error`. The stored original stays as it was.
2. An approval matches one version of the visit and one version of `main`. Another fact on the visit clears that approval. Run `consult refresh`, then review the consult again.
3. `recorded_at` is when MedGit stored the fact. It cannot move earlier along a parent link. `authored_at` is when the clinician says the event happened, and that time can be earlier.
4. `./medgit -C ./syn-pat-001 rebase` prints `refused` and exits 2. `amend` and `reset` do the same.

Two results for the same test, with different values, inside six hours, both stay. Merge then requires `--acknowledge-conflicts`.

A decimal such as 4.2 is stored as the string `"4.2"`. The hash then does not depend on how a language prints floating-point numbers. `export-fhir` is where that string becomes a JSON number.

## Who can do what

| Role | Allowed | Stopped |
| --- | --- | --- |
| Resident | Write on an open visit. Open a consult. | Approve or merge. |
| Attending | Approve someone else's consult. Merge it into `main`. | Approve a consult they opened. |
| System, such as a lab or a rule | Write an allow-listed result. Correct only its own facts. | Open a visit, approve, or merge. |
| Auditor | Read, and run `verify`. | Write. |

## Files

One directory per patient. A new review is a new file, and its `previous` field points at the old consult.

```text
syn-pat-001/
  medgit.json                     patient id and display name
  objects/ab/<rest of the sha256> one JSON object; the hash is the id
  refs/main                       hash of the accepted chart
  refs/episodes/<episode id>      hash of that visit
  refs/pulls/<consult id>         hash of the latest consult object
```

Field definitions are in [docs/SPEC.md](docs/SPEC.md) and [schemas/commit.schema.json](schemas/commit.schema.json). Why the chart is a log is in [docs/WHY.md](docs/WHY.md).

## A published example

```bash
go run ./cmd/medgit -C ./f001 demo
```

```text
created ./f001
patient Pieter van de Heuvel
ok  objects=11  refs=3
```

`demo` files the official HL7 FHIR R4 example: the heart-valve encounter, condition, procedure, glucose observation, and blood-exam bundle. The JSON is copied unchanged from [hl7.org/fhir/R4](https://hl7.org/fhir/R4/). HL7 holds the copyright. See `internal/hl7/data/NOTICE`. The operators `att-demo` and `res-demo` file the log. The clinicians in the example are the authors of the FHIR resources.

The glucose value 6.3 mmol/L is stored as the string `"6.3"`.

## FHIR export

```bash
./medgit -C ./syn-pat-001 export-fhir --json
```

This is a FHIR view for other systems. The folder on disk remains the chart. See [docs/FHIR.md](docs/FHIR.md).

## HTTP

The server is one process, with no database and no login. Put an identity check in front of it. Real patients do not belong on this server.

```bash
docker build -t medgit .
docker run --rm -p 8080:8080 -v medgit-cases:/data medgit
curl -s localhost:8080/records/f001/verify
```

On startup the server builds the Pieter van de Heuvel record when that folder is missing. `PORT` defaults to `8080`. `MEDGIT_DATA` is the directory of records, and in the image that path is `/data`.

| Request | Result |
| --- | --- |
| `GET /health` | The process is up. |
| `GET /records` | Record ids. |
| `GET /records/{id}` | Patient, visits, consults. |
| `GET /records/{id}/log` | History. |
| `GET /records/{id}/verify` | Rehash and link check. |
| `GET /records/{id}/fhir` | FHIR Bundle. |
| `POST /records` | Create a record from a FHIR Patient. |
| `POST /records/{id}/episode` | Open a visit. |
| `POST /records/{id}/commit` | Append resources. |
| `POST /records/{id}/consult` | Open a consult. |
| `POST /records/{id}/review` | Comment, approve, or request changes. |
| `POST /records/{id}/merge` | Attending merge. |

## MCP

`medgit mcp` speaks [MCP](docs/MCP.md) on standard input and output, so an editor model can read one record. Set `MEDGIT_REPO` to the patient folder.

Status, log, verify, and FHIR export are available. Writing stays off unless `MEDGIT_ALLOW_WRITE=1`. The server has no merge tool. The model can propose a fact, and an attending still accepts it.

```bash
MEDGIT_REPO=./syn-pat-001 ./medgit mcp
```

## Other commands

| Command | What it does |
| --- | --- |
| `observe` | Add a numeric result, such as a blood pressure. |
| `qsofa` | Add a qSOFA screening result. A system actor or an attending writes it. The band is `low`, `moderate`, or `high`. The rule id is `qsofa-ref-1`. The stored sentence ends with "Screening rule only — not a diagnosis of sepsis." |
| `consult refresh` | Point the consult at the new tips after another fact, and clear the old approval. |
| `abandon` | Close a visit without merging it. Attending only. |
| `show HASH` | Print one stored object. |
| `log --all` | Include facts off the first-parent line. This is how the visit shows up after a merge. |
| `amend`, `rebase`, `reset` | Print `refused` and exit 2. |

`go run ./examples/sepsis` walks a longer synthetic emergency visit: vitals, a lab value with a later correction, and a qSOFA result.

## Limits

Synthetic identifiers use the system `https://medgit.dev/synthetic`.

The files are plain JSON. `verify` reports a file whose bytes no longer match its hash. Someone who can rewrite the directory and recompute every hash is outside what `verify` can catch. This version has no signatures. The threat model is in [SECURITY.md](SECURITY.md).

## Tests

```bash
go test ./...
go run ./examples/sepsis
```

## Not in this version

- Signatures over the same bytes that already have a hash
- Encryption of those same bytes
- An index of many patients, without putting one record inside another
- A check that a lab code and a unit are a real LOINC and UCUM pair, beyond "the value is a decimal string"

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

MIT. See [LICENSE](LICENSE).
