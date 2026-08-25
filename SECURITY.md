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
creator-only item may require explicit migration. `auth migrate-keychain`
changes only the ACL of the exact entry and never reads or rewrites its value;
`auth login` may migrate while replacing the value supplied by the user. macOS
may show an authorization prompt for each older item either command migrates.
New and migrated items use a stable allow-any-application ACL; this is
consistent with same-user processes being outside the threat model. Every other
operation—including reads, deletes, ordinary saves, and rename—disables
authentication UI and fails closed with an account-specific migration hint. A
blocked or canceled explicit migration also keeps the exact account in its
recovery hint. A CGO-disabled Darwin build is environment-only.

## macOS distribution integrity

macOS is distributed from source rather than as a downloaded executable. The
planned Homebrew package will be a Formula that pins an immutable tag archive
by SHA-256 and builds locally with cgo, selecting the Security.framework
backend. Until that Formula lands, install from a source checkout. The release
pipeline publishes prebuilt archives only for Linux and Windows.

Until Developer ID signing and Apple notarization are available, adding a
prebuilt Darwin archive or cask is a policy violation. Removing
`com.apple.quarantine` is not a substitute for establishing artifact identity;
no supported installer runs `xattr` or otherwise bypasses Gatekeeper.

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
  typed failure to an unattended caller. The only exceptions are explicit
  `auth login`, which may migrate while replacing the user-supplied value, and
  `auth migrate-keychain`, which changes only affected creator-only item ACLs.
- A release or installer path that distributes an unsigned prebuilt Darwin
  executable or removes Gatekeeper quarantine instead of building from source.

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
