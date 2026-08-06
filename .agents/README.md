# Agent harness

Canonical, provider-neutral agent configuration for this repository. Everything
under this directory is committed; the provider-specific output is generated.

## Canonical inputs

| Path | Purpose |
| --- | --- |
| `agents.toml` + `agents/*.md` | agent registry and prompts |
| `rules/*.md` | scoped working rules, routed from the root `AGENTS.md` |
| `skills/*/SKILL.md` + `skills/*/agents/openai.yaml` | cross-provider skills |
| `hooks.toml` + `hooks/*` | pre- and post-tool-use hooks |
| `permissions.toml` | explicit allow / ask / deny policy |
| `providers/*.toml` | provider adapter settings |
| `scripts/sync-rules.py` | Cursor rule mirror sync |
| `tests/` | harness unit tests |

This directory is the **only** copy of these files in the repository. Nothing
here is duplicated into a committed provider directory.

## Generated outputs

`.claude/` and `.codex/` are compiled and **git-ignored**. Never edit or commit
them — the next compile prunes the edit, and hook commands render with an
absolute path, so committing them would bake one machine's checkout into the
repo. CI enforces this in `.github/workflows/agent_harness.yaml`.

Tracked `.cursor/rules/*.mdc` files are compatibility mirrors rendered from
`rules/*.md`. They are committed because Cursor has no compile step.

## Commands

```bash
# Provider mirrors, via the machine-local global harness (not vendored here):
python3 ~/.agents/compiler.py --source .agents --root . --scope project
python3 ~/.agents/compiler.py --source .agents --root . --scope project --check

# Cursor mirrors, via the one script this repository does carry:
python3 .agents/scripts/sync-rules.py          # rewrite .cursor/rules/*.mdc
python3 .agents/scripts/sync-rules.py --check  # report drift, write nothing

python3 -m unittest discover -s .agents/tests -p 'test_*.py'
```

`sync-rules.py` defaults its repository root from its own location, so it takes
no arguments. Both scripts need the standard library only; set
`AGENT_HARNESS_PYTHON` when the first `python3` on PATH is not the interpreter
the hooks should use.

## Why the compiler is not vendored, but the rule sync is

The provider compiler is deliberately **not** committed. Vendoring it would put
a second copy of every rule, hook, and skill into `.claude/` and `.codex/` —
28 byte-identical duplicates of files that already live here. Keeping the
generated output git-ignored means this directory is the single source, which
is the whole point.

`scripts/sync-rules.py` **is** committed, for one reason: the global compiler
emits nothing for Cursor, so there is no shared tool to fall back on. Cursor
also has no compile step, which is why `.cursor/rules/*.mdc` is the one
generated artifact the repository tracks.

Two consequences of using the global compiler, worth knowing:

- **CI cannot verify compilation** — a runner has no `~/.agents`. CI checks that
  the mirrors stay untracked, that the Cursor mirrors are in sync, and that the
  harness tests pass. Compile determinism is a local pre-push check.
- **The global compiler emits a `.claude/CLAUDE.md` shim under project scope.**
  Its relative `@` target resolves against `.claude/`, where no `AGENTS.md`
  exists, so the shim is inert. It is git-ignored, so it is local noise rather
  than a defect in this repository — but do not mistake it for the real root
  `CLAUDE.md`.
If the global compiler is ever out of date and a rename leaves stale generated
files behind, the output is git-ignored, so discarding and rebuilding is always
safe:

```bash
rm -rf .claude .codex
python3 ~/.agents/compiler.py --source .agents --root . --scope project
```

## Hook paths

`hooks.toml` commands use the `{claude_hook_dir}` placeholder rather than
`{root}/.agents/hooks`. The compiler mirrors every canonical hook into the
provider's own hook directory, so referencing `.agents/hooks` directly would
leave those mirrors unreferenced. Available placeholders are `{python}`,
`{compiler}`, `{root}`, and `{claude_hook_dir}`.

## Adding a rule

1. Write `rules/<name>.md`. It must be flat — the sync script globs
   `rules/*.md` non-recursively and rejects nesting.
2. Register it in the `RULES` dict in `scripts/sync-rules.py` with a
   description, `always_apply`, and `globs`. An unregistered rule is a hard
   error, not a warning.
3. Add a row to the router table in the root `AGENTS.md`.
4. Run the compiler and the rule sync, then confirm `git status --porcelain` is
   clean.
