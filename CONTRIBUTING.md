# Contributing

MedGit is an open method plus a small reference implementation. Issues and pull requests are welcome.

## What to change

- A workflow that the role rules get wrong. Say which clinician does what, and what must remain true afterwards.
- A FHIR projection that drops a field a real consumer needs. Keep the stored object free of JSON floats.
- A failing `verify` case: tamper, a backwards `recorded_at`, a merge with one parent, a correction aimed at a missing object.

## What not to change

- Do not add `amend`, `rebase`, `reset`, or force-push as working operations. The commands exist to refuse.
- Do not delete or rewrite an object to "fix" a bad result. Add a `correction` commit.
- Do not commit real patient data, identifiers, or facility extracts. Examples stay synthetic.
- Do not replace `qsofa-ref-1` with an unscored pickle or a made-up probability.

## Tests

Go 1.22 or newer.

```bash
go test ./...
go run ./examples/sepsis
```

The library has no runtime dependencies outside the Go standard library. Please keep it that way unless a change cannot be expressed in the standard library, and say why in the pull request.

## Actors

Document the role you tested with. A resident path and an attending path are different code paths; a green attending-only test is not coverage.

## License

Contributions are under the MIT license in `LICENSE`.
