# Credentials

This binary holds a Trello API key and token that grant full read/write access
to every board the user can see. Treat both as live credentials.

## Storage

Credentials are stored per named account, one keychain entry each
(`account:<name>`). Selection is **per invocation and never stateful**: there is
deliberately no command that switches an active account. An active-account
setting is hidden global state, two concurrent invocations would race over it,
and the loser would silently act on the wrong board — the failure this tool is
built to prevent everywhere else.

Resolution order, most specific first. Explicit beats implicit, per-invocation
beats stored:

1. `--account NAME`
2. `TRELLO_API_KEY` / `TRELLO_TOKEN`
3. `TRELLO_CLI_ACCOUNT`
4. the stored default account
5. the only account, when exactly one exists

The order matters and is not arbitrary. On macOS a freshly rebuilt, unsigned
binary triggers a modal keychain-access prompt; an agent shelling out to it
would hang on an invisible dialog with no output. The environment path is the
headless, CI, and agent path, and it must be reachable without touching the
keychain at all.

Credentials are **never** written to a config file, a cache file, a log, or the
repository. `internal/config` holds defaults; it does not hold secrets. The
account registry under the user config directory holds account *names* only —
a keychain cannot be enumerated, which is the sole reason that file exists.

## Handling

- Never print a token. `auth status` prints a fingerprint — a short hash prefix
  — and the key's last four characters at most.
- Never put a credential in a URL that gets logged. Trello takes `key` and
  `token` as query parameters, so any request-logging path must redact the query
  string before writing it.
- Redact in error messages too. A wrapped `*url.Error` contains the full URL
  including the token; translate it before it reaches the user or a log.
- `-v/--verbose` output goes to stderr and is subject to the same redaction.
- Never write a real token into a test fixture, a golden file, or a comment.

## Review triggers

Any change touching `internal/auth`, request construction in `internal/trello`,
logging, or error formatting needs an explicit check that no code path can emit
a token. Grep the diff for `token`, `key`, `Authorization`, and `%v` on a
request or URL value.
