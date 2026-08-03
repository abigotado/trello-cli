# Quality gates

## Pre-push hook

`.githooks/pre-push` runs, in order: `gofmt -l` (must be empty), `go vet ./...`,
`go build ./...`, `go test ./...`. It is the last check before a change leaves
the machine.

Install it once per clone:

```bash
.githooks/install
```

This sets `core.hooksPath`. Installing and uninstalling mutate local Git config
and are the developer's decision, never the agent's — which is why they sit in
`ask`, not `allow`, in `.agents/permissions.toml`.

`git push --no-verify` and `git config --local core.hooksPath` are denied. The
`pre_push_guard` hook in `.agents/hooks.toml` is the real enforcement: prefix
rules cannot cover every spelling, so the guard parses the command. Do not work
around it — if the gate is wrong, fix the gate.

## Before finishing any change

Run the smallest validation that actually covers the touched surface:

```bash
gofmt -l .
go vet ./...
go test -race ./...
```

Report what you ran and what you did not. Never describe an untouched surface
as green.

## Harness

The agent harness is compiled, not hand-written. `.claude/` and `.codex/` are
generated and git-ignored; editing them directly is always wrong and the edit
will be pruned on the next compile.

The provider compiler is not vendored in this repository. Use the machine-local
global harness:

```bash
python3 ~/.agents/compiler.py --source .agents --root . --scope project
python3 ~/.agents/compiler.py --source .agents --root . --scope project --check
python3 .agents/scripts/sync-rules.py --check
python3 -m unittest discover -s .agents/tests -p 'test_*.py'
```

After changing anything under `.agents/`, run the compiler and the rule sync,
then confirm `git status --porcelain` is clean and `git ls-files .claude .codex`
is empty.

CI cannot run the compiler — a runner has no `~/.agents`. It enforces what it
can: that the generated mirrors stay untracked, that the Cursor mirrors are in
sync, and that the harness tests pass. Compile determinism is a local check, so
run it before pushing a change to `.agents/`.

Every canonical rule file must be registered in the `RULES` dict in
`.agents/scripts/sync-rules.py`. The script globs `.agents/rules/*.md`
non-recursively and treats an unregistered or nested rule as a hard error.
