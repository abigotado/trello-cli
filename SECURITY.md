# Security policy

`trello-cli` holds a Trello API key and token that grant full read/write access
to every board the account can see. Anything that can expose them, or make the
binary act on a board you did not select, is a security issue rather than a bug.

## Supported versions

The latest release only. This is a `0.x` series: fixes ship in the next
release, and there are no backports to earlier tags.

## Reporting a vulnerability

Report privately through GitHub:
[**Report a vulnerability**](https://github.com/abigotado/trello-cli/security/advisories/new).
That opens a draft advisory visible only to the maintainer.

Do not open a public issue for a suspected vulnerability, and do not describe
one in a pull request.

This is a personally maintained project — expect an acknowledgement within
about a week. A fix ships in the next release, and the advisory is published
with credit once it does, unless you would rather not be named.

## Never include a live credential in a report

A redacted reproduction is always enough. `trello-cli auth status` prints a
fingerprint and the last four characters of the key, which is the right level
of detail for a report.

If you believe a token has already been exposed, revoke it first — Trello
account settings, *Allowed Applications* — and then report. Revoking is
immediate and costs you one `trello-cli auth login`.

## In scope

- Any path that writes a key or token to disk, a log, stdout, or an error
  message. `*url.Error` carrying a full request URL is the classic one.
- Any path where attacker-controlled Trello content — a board, list, card, or
  label name — changes what the binary executes, or escapes the envelope on
  stdout.
- Anything that makes an invocation act on a different account or board than
  the resolution order in
  [`.agents/rules/secrets.md`](.agents/rules/secrets.md) specifies.
- Credential material reachable by another local user or another process at
  ordinary permissions.

## Out of scope

- Trello's own API behavior, rate limits, and permission model.
- A token you pasted into your own shell history or exported into a shared
  environment.
- Compromise of the machine, its user account, or its OS keychain. The keychain
  is a trust boundary this tool relies on; it does not replace it.
- Reports produced solely by a scanner, with no path from the finding to an
  exposed credential or a wrong write.
