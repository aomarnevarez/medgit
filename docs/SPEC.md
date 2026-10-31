# Object spec 0.2.0

A MedGit record is a directory. Identity lives in `medgit.json`. History lives in content-addressed objects. Names of the current tips live in refs.

## Canonical bytes

An object's id is the hex SHA-256 of its canonical JSON:

- UTF-8
- object keys sorted by Unicode code point
- separators `,` and `:` with no extra whitespace
- no JSON floats anywhere in the tree
- booleans, integers, strings, arrays, objects, and null only

Decimals in clinical quantities are strings (`"4.2"`, `"88"`). Two encodings of the same logical document are the same object. `store.put` is idempotent when the bytes match and raises if a path collision carries different bytes.

## Commit

See [schemas/commit.schema.json](../schemas/commit.schema.json).

`parents` is ordered. Parent 0 is the first parent. `history` without `all_parents` follows parent 0 only, which is how `log` tells the longitudinal story after a merge. `export-fhir` and `verify`'s reachability walk follow every parent.

`recorded_at` is set by the record's clock when the object is created. It must be greater than or equal to every parent's `recorded_at`. `authored_at` is the clinical time and is not part of that check.

`sensitivity` is `normal` or `restricted`. 0.2 records it and does not yet encrypt it. Callers can branch policy on it; the reference merge path does not treat `restricted` as a second merge machine. Every merge still needs an independent attending approval.

## Refs

| Ref | Points at |
| --- | --- |
| `main` | Tip of the longitudinal record. Advanced by `patient-init` and by `merge`. |
| `episodes/<id>` | Tip of that episode, including `episode-open` and `episode-abandon`. |
| `pulls/<id>` | Latest pull object. Older pull objects stay in the store via `previous`. |

## Episode state

Derived, never stored as a mutable field:

1. `abandoned` if the episode tip can reach an `episode-abandon` commit.
2. `merged` if `main` can reach a `merge` commit whose `episode_id` matches.
3. `in-review` if a pull for that episode is `open`, `approved`, or `changes-requested`.
4. `open` otherwise.

A new commit on an episode rewrites the pull tip to `state: open` with an empty `reviews` array and the new source tip. Approvals do not survive new facts.

## Pull object

`kind` is `pull`. Fields: `id`, `episode_id`, `title`, `body`, `author`, `source_tip`, `target_tip`, `state`, `reviews`, `conflicts`, `previous`, `merge_commit`, `created_at`, `updated_at`.

`conflicts` is a list of notices: same code system and code, values differ, effective times within six hours, comparing this episode's observations with observations already reachable from `main`. Corrected commits are ignored in favour of the correction. The merge commit copies the notices it acknowledged into `notices`.

## Merge

Allowed when all of the following hold:

- actor role is `attending`
- pull state is `approved`
- some approval is from an attending whose id is not the pull author's id
- episode tip still equals `source_tip` and `main` still equals `target_tip`
- if `conflicts` is non-empty, the caller passed `acknowledge_conflicts`
- the episode is not abandoned

The merge commit has parents `[main, episode_tip]`, type `merge`, and no resources. The episode objects do not move.

## Correction

`type` is `correction` and `corrects` is the hash of a commit on the same episode. A `system` actor may only correct a commit whose author role is `system`. Export marks resources from the corrected commit as `entered-in-error` when they have a status, and adds the extension `https://medgit.dev/fhir/StructureDefinition/corrected-by`. The stored original is not edited. `verify` checks that the target exists.

## Clocks in tests

`MedRepo` takes a `clock()` callable that returns a `Z` timestamp. Production uses UTC now. Tests inject a stepping clock so `recorded_at` is deterministic without sleeping.
