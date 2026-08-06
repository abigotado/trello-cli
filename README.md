# trello-cli

Manage Trello from the command line, or from an AI agent shelling out to it.

Every invocation prints one JSON envelope on stdout and exits with a code that
names the caller's next move, so an agent can drive it with no model in the
parsing loop. `--help` is the one exception: it is prose, for people.

- **Stable envelope** — `{"ok":true,"v":1,"data":…,"meta":…}` or
  `{"ok":false,"v":1,"error":…,"hint":…}` on stdout, for every invocation except
  `--help`, which is prose for humans. JSON automatically whenever stdout is not
  a terminal, text when you are at one. stderr carries logs only.
- **Documented exit codes** — 0 through 7, one per distinct recovery action.
  `trello-cli contract` prints the table as JSON at runtime.
- **Resolution that never guesses** — pass `--board "Sprint 12" --list Doing`,
  not ids. Exact name wins, then a unique case-insensitive prefix. Two matches
  is exit 4 with every candidate attached, never a coin flip. Substring matching
  exists behind `--fuzzy` and is off by default.
- **Dry-run rails** — every mutating command takes `--dry-run` and reports the
  ids the names resolved to without calling Trello. `cards delete` needs
  `--yes`. `TRELLO_CLI_READONLY` turns every write to Trello into a usage error.
- **Installable skill** — `trello-cli skills install` teaches Claude Code,
  Codex, and Cursor how to call it, reference docs included.

## Install

Homebrew, on macOS or Linux:

```bash
brew install --cask abigotado/tap/trello-cli
```

It is a cask rather than a formula because the release ships prebuilt binaries.
Casks are no longer macOS-only — `binary` is a portable artifact — but Linux
cask support landed in Homebrew 4.6, so `brew update` first if yours is older.

Go 1.24.1 or newer:

```bash
go install github.com/abigotado/trello-cli/cmd/trello-cli@latest
```

Or from a checkout:

```bash
go build -o trello-cli ./cmd/trello-cli
```

Or download a prebuilt binary from
[Releases](https://github.com/abigotado/trello-cli/releases) — darwin, linux,
and windows on amd64 and arm64. Each release carries a checksums file:

```bash
shasum -a 256 -c trello-cli_VERSION_checksums.txt --ignore-missing
```

`trello-cli version` reports the build any of these produced, which is the first
thing to include in a bug report. A download, a Homebrew install, or a local
checkout also reports the commit it was built from; a `go install`
build compiles the module zip, which carries no VCS history, so it reports its
version and leaves `commit` empty.

## Authenticate

Get an API key at <https://trello.com/power-ups/admin> and authorize it to get a
token. Then store the pair in the OS keychain, giving it to `auth login` on
stdin — the key on the first line, the token on the second:

```bash
{ read -rs 'k?API key: '; echo; read -rs 't?Token: '; echo; printf '%s\n%s\n' "$k" "$t" | trello-cli auth login; unset k t; }
```

Paste that as **one line**. Split across several, the shell hands the next line
to `read` instead of waiting for you to type. Nothing echoes while you paste the
key or the token; that is `read -s` doing its job, not the terminal hanging.

Add `--account work` for a second account — repeat the same line, changing only
the name. `trello-cli auth default work` then picks which one is used when no
account is named.

Add `--dry-run` to rehearse: it reports the account it would write to and stores
nothing.

Put the wrong credential under a name? `trello-cli auth rename old new` moves it
without ever displaying it, and carries the default across if it was the
default. It refuses to overwrite a name that already exists.

`--api-key` and `--token` still work and are still part of the contract, but
prefer stdin. A credential passed as a flag lands in the shell history, and for
as long as the process runs it is in the argv that `ps` will print for anything
running as you.

For CI and headless agents, set `TRELLO_API_KEY` and `TRELLO_TOKEN` instead. The
environment is read before the keychain is touched at all, so an unattended run
can never block on a keychain prompt it cannot answer.

Credentials are chosen per invocation, most specific first:

1. `--account NAME`
2. `TRELLO_API_KEY` + `TRELLO_TOKEN`
3. `TRELLO_CLI_ACCOUNT`
4. the account set by `trello-cli auth default <account>`
5. the only stored account, when exactly one exists

There is deliberately no "switch account" command: an active-account setting is
hidden global state that two concurrent invocations would race over, and the
loser would act on the wrong board. `trello-cli auth status` reports what this
invocation would use; `trello-cli auth list` shows the stored names.

## Agent quickstart

```bash
trello-cli skills install --provider claude   # or: codex, cursor, all
```

That writes a `trello` skill — the agent-facing instructions plus the generated
command and contract references — into the directory the provider reads
(`~/.claude/skills`, `~/.codex/skills`, `~/.cursor/skills`). Use
`--scope project` to install into the current repository instead.

Re-running is safe: files this tool wrote are refreshed in place, and it refuses
to touch a file it did not write unless you pass `--yes`. `--dry-run` reports
exactly what would change and writes nothing. `trello-cli skills uninstall`
removes only the files it installed that still match what it installed.

## A worked example

Names go in. When one is ambiguous, the tool stops instead of picking:

```console
$ trello-cli cards list --board Sprint; echo "exit=$?"
{
  "ok": false,
  "v": 1,
  "error": {
    "code": "AMBIGUOUS_BOARD",
    "message": "\"Sprint\" matches 2 boards",
    "candidates": [
      { "id": "5f2b1c9e4a1d2b3c4d5e6f70", "name": "Sprint 12", "kind": "board" },
      { "id": "5f2b1c9e4a1d2b3c4d5e6f71", "name": "Sprint 13", "kind": "board" }
    ]
  },
  "hint": "re-run with --board-id using an id from candidates"
}
exit=4
```

Exit 4 means one thing and has one fix: pick a candidate and re-run with its id.
Exit 3 does the same with `did_you_mean`. Before a write, `--dry-run` shows what
the names resolved to:

```console
$ trello-cli cards create --board-id 5f2b1c9e4a1d2b3c4d5e6f70 \
    --list Doing --name "Fix login" --dry-run
{
  "ok": true,
  "v": 1,
  "data": {
    "action": "cards create",
    "changes": { "name": "Fix login" },
    "dryRun": true,
    "target": {
      "board": "5f2b1c9e4a1d2b3c4d5e6f70",
      "list": "6a2b1c9e4a1d2b3c4d5e6f80"
    }
  }
}
```

Drop `--dry-run` to apply it.

## Reference

- [`docs/commands.md`](docs/commands.md) — every command and flag, which are
  required, and which are gated. Generated from the binary.
- [`docs/contract.md`](docs/contract.md) — the envelope and the exit-code table.
  Generated from the binary.
- `trello-cli <command> --help` for the same, at the terminal.
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — how to build, test, and send a change:
  the validation gate, the generated files, and what counts as a contract break.
- [`AGENTS.md`](AGENTS.md) — the rule router the scoped working rules hang off.
- [`SECURITY.md`](SECURITY.md) — how to report a vulnerability privately.

Environment: `TRELLO_CLI_BOARD` and `TRELLO_CLI_LIST` supply defaults for
`--board` and `--list`; `TRELLO_CLI_READONLY` disables every write to Trello —
it takes `1`/`true`/`yes`/`on` or `0`/`false`/`no`/`off`, and refuses anything
else rather than guess which way you meant it; `TRELLO_CLI_TIMEOUT`,
`TRELLO_CLI_CONCURRENCY`, and `TRELLO_CLI_BASE_URL` tune the rest.
