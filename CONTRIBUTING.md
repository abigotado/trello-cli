# Contributing

`trello-cli` is a single Go binary whose command surface is consumed far more
often by an AI agent shelling out to it than by a human typing. Most of the
rules below follow from that one fact: exit codes, the JSON envelope, and flag
names are a published API, and a caller cannot notice a silent change to them.

## Before you open a pull request

A self-contained bug fix needs no preamble — send it. For anything that adds a
command, changes a flag, changes output, or touches the exit-code table, open
an issue first and agree on the surface. That conversation is cheaper than a
rewritten PR, and a rejected command surface is the most expensive thing to
discover after the code is written.

## Setup

Go 1.24.1 or newer — `go.mod` is the source of truth, and CI reads the version
from it.

```bash
git clone https://github.com/abigotado/trello-cli.git
cd trello-cli
go build ./...
.githooks/install
```

On macOS, a normal source build uses cgo and the macOS SDK so the credential
backend can call Security.framework directly. `CGO_ENABLED=0` still compiles a
Darwin binary, but that binary is environment-only and cannot use stored
credentials. Linux and Windows retain their `go-keyring` platform backends.

`.githooks/install` points `core.hooksPath` at `.githooks/`, so the pre-push
hook runs the gate before anything leaves your machine. It mutates local Git
config, which is why it is a deliberate opt-in rather than something the repo
does to you; `.githooks/uninstall` reverses it.

## The validation gate

```bash
gofmt -l .        # must print nothing
go vet ./...
go test -race ./...
```

`-race` is not optional here. The Trello client paces and retries from shared
state, and a data race in it surfaces as a wrong rate-limit decision rather
than a crash — the kind of bug that reproduces once a week in someone else's
terminal.

Say in the pull request what you ran. Never describe a surface you did not
touch as green.

## Generated files — read this before your first pull request

Three sets of files are generated. Hand-editing any of them fails CI with a
message that will not explain itself, so it is worth two minutes now.

**`.claude/` and `.codex/`** are compiled from `.agents/` and are git-ignored.
Never commit them. The compiler that produces them is deliberately *not*
vendored in this repository — it lives in a machine-local harness — so you
cannot regenerate them and do not need to. CI only checks that they stay
untracked. (Hook commands render with an absolute path, so a committed copy
would bake one machine's checkout into the repository.)

**`.cursor/rules/*.mdc`** are tracked mirrors of `.agents/rules/*.md` and are
the one generated artifact that *is* committed, because Cursor has no compile
step. If you change a canonical rule:

```bash
python3 .agents/scripts/sync-rules.py
python3 -m unittest discover -s .agents/tests -p 'test_*.py'
```

A new rule must also be registered in the `RULES` dict in
`.agents/scripts/sync-rules.py`. The script globs `.agents/rules/*.md`
non-recursively and treats an unregistered or nested rule as a hard error.

**`docs/commands.md`, `docs/contract.md`, and `assets/skills/trello/reference/`**
are generated from the binary itself, which is what makes the claim that the
shipped agent skill cannot drift from the code true. Any change to the command
surface or to `internal/errx` means:

```bash
go generate ./...
```

Commit the result in the same commit as the code. CI regenerates and fails on
any diff.

## Rules that apply to the code

[`AGENTS.md`](AGENTS.md) is the entry point and routes to the scoped rules in
`.agents/rules/`. Read the ones your change touches — they are short, and they
exist because each one has already been broken once:

| Surface | Rules |
| --- | --- |
| Package boundaries or a new dependency | [architecture](.agents/rules/architecture.md) |
| Exit codes, the envelope, flags, output | [architecture](.agents/rules/architecture.md), [CLI contract](.agents/rules/cli-contract.md) |
| Any Go code | [Go style](.agents/rules/go-style.md) |
| Any behavior change | [testing](.agents/rules/testing.md) |
| Credentials, auth, requests, logging, errors | [secrets](.agents/rules/secrets.md) |

Three that catch most first pull requests:

- **stdout carries only the response envelope.** Every log, warning, progress
  line, and prompt goes to stderr. Anything else on stdout corrupts a caller's
  parse.
- **Dependency direction is verified, not assumed.** `internal/cli` never calls
  `net/http`, `internal/trello` never imports `internal/auth`, and
  `internal/errx` imports nothing else from this module.
- **Mutating commands are built with the shared command helper** in
  `internal/cli`. It registers `--dry-run`, `--yes`, and the `mutates`
  annotation that the dry-run, confirmation, and read-only middleware depend on.
  A hand-built command silently opts out of all three rails.

## Contract changes

Adding a field, a command, or an `error.code` is additive and needs no
ceremony. Renaming or removing an envelope field, changing its type, renaming a
flag, or reassigning an exit code is breaking — discuss it in an issue first.

Two specifics worth knowing before you write the code:

- **Prefer a new `error.code` over a new exit code.** An exit code is justified
  only by a *distinct recovery action*. If the caller's next move is the same as
  for an existing code, it is a new `error.code`.
- **`TestEnvelopeKeySetIsPinned` fails on any rename, removal, or addition to
  the envelope key set.** That failing test is the moment to decide whether `v`
  bumps — not an obstacle to route around.

## Tests

Every behavior change needs a test that fails without the change.

Test at boundaries, not internals: `net/http/httptest` for HTTP, an injected
fake credential store for the keychain, `t.TempDir()` for the filesystem, and
`t.Setenv()` for the environment. A test that reaches the real Trello API, the
real OS keychain, or your home directory will not be merged. In particular,
ordinary macOS tests must not exercise Security.framework against the
developer's keychain.

The one native exception is the build-tagged cross-binary Keychain integration
check in CI. The helper at
`internal/auth/keychainintegration/cmd/keychain-helper` exists only under the
`keychainintegration` build tag. It uses a synthetic marker with two newly
created disposable Keychains: A is the explicit `kSecMatchSearchList` target
containing the existing item, while B is the explicit `kSecUseKeychain` add
target. The second test binary unlocks both. The check never changes the
default keychain or the user's search list, never prints the marker, and cleans
up both Keychains on every exit path. Keep those properties if the helper
changes.

Do not weaken an assertion to make a failing test pass. Report the failure.

## Credentials

This binary holds a Trello API key and token that grant full read/write access
to every board the account can see. Treat both as live credentials.

Never commit one — not in source, not in a test fixture, not in a golden file,
not in a comment. Trello takes `key` and `token` as **query parameters**, so
before you paste a command, a log, or `--verbose` output into an issue or a
pull request, check the query strings. See [SECURITY.md](SECURITY.md) if you
think one has already leaked.

## Branches, commits, and merges

- Branch from `main` and name the branch for the change.
- Commit subjects are imperative and say what changes and why it matters, not
  which files moved — for example, *"Stop auth list reading the keychain, defer
  index writes, pin the envelope"*.
- **Pull requests are squash-merged**, so the pull request title becomes the
  commit subject on `main`. Give it the same care as a commit subject.
- One concern per pull request. Repository-wide formatting, `go mod tidy`, and
  dependency upgrades do not ride along with a feature — Dependabot owns the
  last two.

## macOS distribution

Do not add a prebuilt Darwin target or Homebrew cask to GoReleaser. Without a
Developer ID signature and Apple notarization, a downloaded executable is not a
supported macOS distribution, and clearing `com.apple.quarantine` is never an
acceptable substitute.

The planned macOS Homebrew package will be a Formula built locally from a
SHA-256-pinned tag archive with `CGO_ENABLED=1`. Adding or updating it is
necessarily a two-repository operation: first merge and tag the `trello-cli`
source, then update `abigotado/homebrew-tap` with the immutable tag URL and its
exact checksum. The Formula must inject the tag through
`internal/cli.releaseVersion`, verify the machine contract, check
Security.framework linkage, and reject a binary that contains
`/usr/bin/security`. Never point a stable Formula at a branch, placeholder
version, or mutable URL.

## What CI enforces

| Workflow | Check | Enforces |
| --- | --- | --- |
| `go` | Build and test | `gofmt`, `go vet`, `go build`, and `go test -race` on Linux and native macOS, plus `go test -count=1` on Windows; a macOS `CGO_ENABLED=0` suite and isolated cross-binary disposable-keychain check; Ubuntu-only `goreleaser check`, a Linux/Windows release rehearsal that rejects Darwin or Homebrew artifacts, `actionlint` with shellcheck over `.github/workflows`, and verification that `go generate` produces no diff |
| `agent harness` | Harness consistency | `.claude/`/`.codex/` stay untracked, Cursor mirrors are in sync, harness unit tests pass |

Both are required to merge into `main`. If you contribute from a fork, the
first run waits for a maintainer to approve the workflow — that is GitHub's
default for new contributors, not a problem with your pull request.

## License

This project is MIT licensed. By contributing you agree that your contribution
is licensed under the same terms.
