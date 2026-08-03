# trello-cli agent rules

This is the provider-neutral entry point for AI work in this repository.
`README.md` is the product reference; the curated files under `.agents/rules/`
turn it and verified repository patterns into scoped working rules.

`trello-cli` is a single Go binary that manages Trello and is driven far more
often by an AI agent shelling out to it than by a human typing. Its command
surface is a machine contract, and most rules here follow from that.

## Rule router

Read the smallest matching rule set before changing code:

| Surface | Required rules |
| --- | --- |
| Package boundaries, a new package, or a dependency change | [architecture](.agents/rules/architecture.md) |
| Exit codes, the JSON envelope, flag names, or output format | [architecture](.agents/rules/architecture.md), [CLI contract](.agents/rules/cli-contract.md) |
| Any Go code | [Go style](.agents/rules/go-style.md) |
| Any behavior change | [testing](.agents/rules/testing.md) |
| Credentials, auth, request construction, logging, or error formatting | [secrets](.agents/rules/secrets.md) |
| Pushing, validating, or anything under `.agents/` | [quality gates](.agents/rules/quality-gates.md) |

When multiple rows match, read all listed files. Nearby implementation and tests
take precedence over generic examples in a rule. Do not apply a scoped rule to
unrelated files merely because it exists.

## Workflow

For non-trivial work:

1. Scope the affected surfaces, risks, and validation.
2. Have `planner` order the work by dependency.
3. Pass load-bearing plans through the read-only `advisor` before any edit. If
   it returns NEEDS-CHANGES, return to `planner`.
4. Use `explorer` to inspect existing patterns instead of inventing new ones.
5. Delegate implementation to `coder`.
6. Delegate behavior tests to `test-writer`.
7. Run `cli-contract-guardian` when the machine contract or dependency
   direction is touched.
8. Run `reviewer`, then an independent `adversarial-review` second opinion.
9. Run the smallest relevant validation and report anything not run.

Small single-file fixes may collapse this, but never skip validation and never
skip the second opinion when the primary review flagged anything non-trivial.

## Generated files

`.agents/` is canonical and committed, and it is the **only** copy of these
files in the repository. `.claude/` and `.codex/` are compiled outputs and are
**git-ignored** — never edit or commit them. Tracked `.cursor/rules/*.mdc` files
are compatibility mirrors that must match the canonical bodies; Cursor has no
compile step, so they are the one generated artifact that is committed.

The provider compiler is **not** vendored here — use the machine-local global
harness, which is shared with the other repositories:

```bash
python3 ~/.agents/compiler.py --source .agents --root . --scope project
python3 ~/.agents/compiler.py --source .agents --root . --scope project --check
python3 .agents/scripts/sync-rules.py --check
python3 -m unittest discover -s .agents/tests -p 'test_*.py'
```

Every canonical rule must be registered in the `RULES` dict in
`.agents/scripts/sync-rules.py`; the script globs `.agents/rules/*.md`
non-recursively and rejects an unregistered or nested rule.

## Never

- Commit `.claude/` or `.codex/`. Hook commands render with an absolute path,
  so committing them bakes one machine's checkout into the repo.
- Print, log, or commit a Trello API key or token. See
  [secrets](.agents/rules/secrets.md).
- Write to stdout anything but the response envelope.
- Bypass the pre-push gate with `--no-verify` or a `core.hooksPath` override.
- Renumber an exit code or rename an envelope field without treating it as a
  breaking change.
- Run repository-wide formatting, `go mod tidy`, or dependency upgrades unless
  the accepted plan includes them.
