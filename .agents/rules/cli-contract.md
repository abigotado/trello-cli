# Machine contract

The command surface is this project's public API. Its consumers are AI agents
that cannot notice a silent change, so treat every item here as breaking unless
it is purely additive.

**Precedence:** this file is the specification. `internal/errx` is the
implementation of record, and `docs/contract.md` plus
`assets/skills/trello/reference/contract.md` are generated from `internal/errx`
by `go generate`. When the generated output disagrees with this file, this file
wins and `internal/errx` is the thing that gets fixed.

## Envelope

```json
{"ok":true,"v":1,"data":{},"meta":{"count":3,"truncated":false}}
{"ok":false,"v":1,"error":{"code":"AMBIGUOUS_BOARD","message":"","candidates":[]},"hint":"pass --board-id"}
```

- `ok`, `v`, `data`, `meta`, `error.code`, `error.message`, `hint` keep their
  names, types, and nullability.
- Adding a field is additive. Renaming, removing, or changing the type of one
  requires bumping `v`.
- **`v` describes the shape of the envelope, and it must be true of the binary
  that printed it.** What froze at `v0.1.0` is the shape, not the counter: from
  that tag on the shape is published, so any change to it bumps `v` in the same
  commit. A `0.x` series buys freedom in the flags and the command surface,
  never in the truthfulness of `v`.
- `v` and the binary's version are independent counters, coupled in one
  direction only. A `v` bump is a breaking change to published output, so it
  takes the largest bump the series allows — minor while `0.x`, major from
  `1.0.0`. The reverse does not hold: renaming a flag breaks the command
  contract and leaves `v` untouched.
- The envelope key set is pinned by `TestEnvelopeKeySetIsPinned` in
  `internal/output`. It fails on any rename, removal, or addition, so an
  envelope change cannot happen by accident: editing that list is the moment to
  decide about `v`.
- `error.code` is a stable `SCREAMING_SNAKE_CASE` string. It is where new
  granularity goes — prefer a new code over a new exit code.
- `hint` is written for a machine reader: it states the next action
  (`pass --board-id`), not an apology.

## Exit codes

Each code maps to a **distinct recovery action**. That is the test for whether a
new one is justified; if the caller's next move is the same, it is a new
`error.code`, not a new exit code.

| Code | Meaning | Caller's next move |
| --- | --- | --- |
| 0 | ok | proceed |
| 1 | internal failure | report, do not retry |
| 2 | usage / validation | fix flags |
| 3 | not found | check the name; `did_you_mean` is in the envelope |
| 4 | ambiguous | pick from `candidates` |
| 5 | credentials need user action | follow the exact `hint` |
| 6 | retryable (rate limit, network) | back off, retry |
| 7 | confirmation required | add `--yes` |

Two hazards that are easy to reintroduce:

- **A compiled Go binary exits `2` when it panics.** `main` must install a
  `recover()` that maps panics to `1`. Never assign a semantic meaning to `2`
  that would make a crash look like a retryable result.
- **Cobra returns a bare `1` on flag-parse errors** unless the root command sets
  `SilenceUsage` and a `SetFlagErrorFunc` that routes through `errx`.

## Flags

- Names, shorthands, and defaults are contract. A rename ships with a hidden
  alias for the old spelling.
- Name-or-ID pairs are `--board` and `--board-id`, never `--boardName`.
- Output is `-o/--output {text,json,raw}` plus `--fields`. `--json` is a
  retained hidden alias.
- **Output defaults to `json` when stdout is not a TTY.** Do not make an agent
  remember a flag to get parseable output.
- Every mutating command accepts `--dry-run` and `--yes`, registered by the
  shared command helper rather than per command.

## Output economy

Context is the scarce resource. Default output is one compact line per entity
with a minimal field set. A command that dumps unfiltered API responses is a bug.

`-o raw` prints the payload without the envelope and without projection. It is
**not** a passthrough of Trello's response: the client decodes into its own
types first, so a field the tool does not model never reaches the renderer.
Documenting it as a passthrough would send a caller looking for a field that
cannot appear. A real passthrough would require the client to retain the
response bytes; that is a deliberate non-goal, not an oversight.
