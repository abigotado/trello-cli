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

## macOS Keychain boundary

The native backend calls Security.framework `SecItem` APIs directly and never
delegates credential access to a subprocess. Service, account, and wire-format
identifiers remain compatible with existing `go-keyring` entries. An older
creator-only item may require one explicit `auth login`; macOS may show an
authorization prompt for each older item it migrates. New and migrated items
use a stable allow-any-application ACL; this is consistent with same-user
processes being outside the threat model. Every other operation—including
reads, deletes, ordinary saves, and rename—disables authentication UI and
fails closed. A
CGO-disabled Darwin build is environment-only.

## macOS release integrity

Prebuilt Darwin binaries are Developer ID signed with hardened runtime and a
secure timestamp, then submitted to Apple's notary service. The release fails
before archiving or publishing unless Apple returns `Accepted`. The Homebrew
cask never clears `com.apple.quarantine`; Gatekeeper remains an enforced trust
boundary. Pull-request snapshots skip notarization because untrusted builds do
not receive release credentials, and those artifacts are deleted rather than
published. Manual release dispatches reject legacy tags that predate this
policy, and the tap workflow separately rejects a quarantine-bypassing cask.

## In scope

- Any path that writes a key or token to disk, a log, stdout, or an error
  message. `*url.Error` carrying a full request URL is the classic one.
- Any path where attacker-controlled Trello content — a board, list, card, or
  label name — changes what the binary executes, or escapes the envelope on
  stdout.
- Anything that makes an invocation act on a different account or board than
  the resolution order in
  [`.agents/rules/secrets.md`](.agents/rules/secrets.md) specifies.
- Credential material reachable by another local OS user at ordinary
  permissions.
- A macOS credential path that invokes `/usr/bin/security`, a password-manager
  helper, or another subprocess instead of Security.framework `SecItem` APIs.
- A macOS keychain query that can open authentication UI instead of returning a
  typed failure to an unattended caller. The sole exception is an explicit
  `auth login`, which may request authorization to replace affected older
  creator-only item ACLs.
- A release or installer path that removes Gatekeeper quarantine instead of
  distributing a Developer ID signed, Apple-notarized macOS binary.

## Out of scope

- Trello's own API behavior, rate limits, and permission model.
- A token you pasted into your own shell history or exported into a shared
  environment.
- Credential access by another process already running as the same local user.
  Calling Security.framework directly removes the subprocess credential path;
  it does not isolate the CLI from other code with the user's own authority.
  New and migrated macOS items deliberately use a stable
  allow-any-application ACL on that basis. Exposure to a different local user
  remains in scope.
- Compromise of the machine, its user account, or its OS keychain. The keychain
  is a trust boundary this tool relies on; it does not replace it.
- Reports produced solely by a scanner, with no path from the finding to an
  exposed credential or a wrong write.
