---
name: trello
description: Manage Trello with the trello-cli binary — read, search, create, update, move, archive, comment on, label, and assign boards, lists, and cards. Use for any Trello request ("what is on my board", "add a card for X", "move this to Done", "who owns this card"), and before shelling out to trello-cli for the first time in a session.
---

# Drive Trello with trello-cli

`trello-cli` is built to be called by an agent. Every invocation prints one JSON
envelope on stdout and exits with a code that names your recovery action. Parse
stdout, branch on the exit code, and follow `hint` — it comes from the binary,
so when it disagrees with this file, `hint` wins. stderr carries logs only.

The one exception is `--help`, which prints human-readable text on stdout and
exits 0. Read it, do not parse it. Naming no command at all (`trello-cli`, or a
group like `trello-cli cards`) is exit 2 with an envelope, not help text.

```json
{"ok":true,"v":1,"data":{},"meta":{"count":3,"truncated":false}}
{"ok":false,"v":1,"error":{"code":"AMBIGUOUS_BOARD","message":"","candidates":[],"did_you_mean":[]},"hint":"pass --board-id"}
```

Output is JSON whenever stdout is not a terminal, so no flag is needed. `meta`
appears only for collections. `meta.truncated` true means the page came back
exactly as long as `--limit`, which is the only signal Trello gives that more
*may* exist — it is not a promise that it does.

What to do about it depends on the command, and only three take a `--limit` at
all. `comments list` and `activity list` page properly: both default to 50 and
report `truncated` true when the page came back full, so walk backwards with
`--before`, carrying the oldest id you got, until a page reports false. On
`activity list` do not reach for `--since` instead — the feed is newest-first
and `--limit` cuts from the old end, so `--since` sets a floor and still hands
you the newest page of a wider window. `search` does not page — the only way to
see more is to re-run it with a larger `--limit`. Every other list command
returns everything and never reports `truncated` true.

## Exit codes

| Code | Meaning | Next move | Concretely |
| --- | --- | --- | --- |
| 0 | ok | proceed | — |
| 1 | internal failure | report, do not retry | re-running fails the same way |
| 2 | usage or validation error | fix the flags | `error.message` names the bad or missing flag |
| 3 | nothing matched | check the name | re-run with a name from `error.did_you_mean`, or list the objects |
| 4 | several objects matched | pick from candidates | re-run with `--board-id`, `--list-id`, or `--card-id` from `error.candidates` |
| 5 | missing or rejected credentials | re-authenticate | stop and ask the user to run `trello-cli auth login` |
| 6 | rate limited or network failure | back off and retry | wait `error.retry_after` if present, else back off yourself |
| 7 | destructive operation not confirmed | add `--yes` | only after the user has agreed |

Re-running 1, 2, 3, or 4 unchanged produces the same failure.

Two fields in that table are optional and you must not assume them.
`error.did_you_mean` appears only when something was close enough to suggest —
on exit 3 with nothing near, the key is absent, not empty. `error.retry_after`
appears only when Trello sent a `Retry-After` header, and it is a duration
string like `"2s"`, not a number. When either is missing, `hint` still tells you
what to do.

## Pass names, not ids

Use the names you already have: `--board "Sprint 12" --list Doing --card "Fix login"`.
Do not spend a call listing objects to find an id.

Resolution never guesses:

- Exact name wins, then a unique case-insensitive prefix.
- Several matches gives exit 4 with every match in `error.candidates`. Re-run
  with `--board-id` / `--list-id` / `--card-id`. Ask the user when the choice is
  not obvious; do not pick for them.
- No match gives exit 3 with near names in `error.did_you_mean`.
- `--fuzzy` adds substring matching. It is off by default because it fails by
  confidently returning one wrong object. Never use it on a write.

A 24-hex id skips resolution entirely, so a card addressed by its id needs no
`--board`. An 8-character shortLink does not skip it — names like `Backlog1`
have the same shape. The reverse is worth knowing too: addressing anything by
*name* always needs a board, so `--card "Fix login"` without `--board` is exit 2
even though the reference lists only `--card`.

Keeping `--board` costs nothing and buys something: `cards get` fills in the
card's `list` only when a board was named on that invocation. Drop it and `list`
comes back empty rather than absent, which reads as "this card is in no list".

Set `TRELLO_CLI_BOARD` and `TRELLO_CLI_LIST` to stop repeating yourself.

## Writing

- Every mutating command honours `--dry-run`: it resolves all names, prints the
  ids they resolved to, and stops without calling Trello. Use it before any
  destructive or repeated change, and to check that a name lands where you think.
- `cards delete` exits 7 without `--yes` and refuses a prefix match — the target
  must be an exact name or an id. Get the user's agreement first; `--yes` is you
  asserting they agreed.
- `cards archive` and `lists archive` are reversible with `--restore`. Prefer
  archiving over `cards delete`.
- `--dry-run` also satisfies the confirmation gate, so you can preview a
  destructive command without `--yes`.
- The tool retries what is safe to retry, and never replays a create that may
  already have been applied. After exit 6 on a create, re-read before creating
  again or you will duplicate.
- `TRELLO_CLI_READONLY` makes every command that would change Trello exit 2. It
  does not gate local file writes, so `skills install` still works. It is the
  user's lock: report it and stop. Never clear it, and never infer its state
  from the environment — read the `READ_ONLY` error instead, because the
  variable is a yes/no value and an unrecognised one is itself exit 2.

## Accounts

Credentials are chosen per invocation and there is no "switch account" command.
Precedence: `--account NAME`, then `TRELLO_API_KEY` plus `TRELLO_TOKEN`, then
`TRELLO_CLI_ACCOUNT`, then the stored default, then the only account when
exactly one exists. `trello-cli auth list` shows the names; name one on every
call when more than one exists.

Never print, log, or paste an API key or token. On exit 5, ask the user to
authenticate; do not go looking for credentials yourself.

## Output economy

The default field set is deliberately small — keep it. Narrow further with
`--fields id,name`.

`-o raw` prints the payload without the envelope and without field projection.
It is **not** a passthrough of Trello's REST response: the binary decodes into
its own types first, so a field trello-cli does not model is not there to be
had — by any route. `--fields` rejects a name it does not know and lists the
ones it has, which is the fastest way to find out what a command can give you.
A card's description is the field people look for. It is modelled, but kept out
of the default output because it can run to paragraphs and a listing of it would
be mostly description. Ask for it by name:

```
trello-cli cards get --card "Fix login" --fields id,name,desc
```

Read it before you write it. `cards update --desc` replaces the description
outright — there is no append — so fetch the current text first unless you mean
to discard it.

## Where to look next

- `reference/commands.md` — every command, its flags, which flags are required,
  and which commands are gated. Generated from the binary.
- `reference/contract.md` — the full envelope and exit-code specification.
  Generated from the binary.
- `trello-cli contract` prints the same exit-code table as JSON at runtime.

Four traps the reference will not shout at you about.

Move a card between lists with `cards move`, not `cards update`.

`checklists add-item` and `checklists toggle` take `--checklist-id` and
`--item-id` from `checklists list`, never names.

**To link one card to another, attach its URL**: there is no "link" command,
because Trello has no such concept — a card link *is* an attachment whose URL
points at a card, and Trello renders it as a linked card.

```
trello-cli attachments add --card sWnoitGR --url https://trello.com/c/17RxKKBZ
```

Do not report card linking as unsupported. It reads as unsupported only because
the command is called `attachments add`.

**`labels add` needs a label the board already has.** It attaches from the
board's existing set, so asking for one that is not defined is exit 3, not a
label being created for you. Check `labels list` first; `labels create --board X
--name Blocked --color red` defines a new one. Colours are Trello's — a wrong
one comes back as exit 2 naming the flag, and `labels list` shows what the board
already uses.
