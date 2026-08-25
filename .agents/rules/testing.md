# Testing

## Shape

Table-driven with `t.Run()` subtests. Name each case for its condition and
expected result (`"ambiguous name returns candidates"`), not for its index.

```go
tests := []struct {
    name string
    // inputs
    // want
}{...}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) { ... })
}
```

## Boundaries

Test at boundaries, not internals.

- **HTTP**: `net/http/httptest`. Never call the real Trello API from a test.
  Point the client's base URL at the test server via `internal/config`.
- **Keychain**: inject a fake `auth.Store` (or the narrow credential-store
  interface owned by the caller). Ordinary tests never touch the real OS
  keychain. Darwin unit tests exercise pure wire-format, status, size, and
  upsert helpers; native CI compile/link checks cover the C bridge and type
  guard without reading credentials from the developer's keychain.
- **Filesystem**: `t.TempDir()`. Never write to the developer's real cache
  directory.
- **Environment**: `t.Setenv()`. Never mutate `os.Environ` directly.

A test that depends on the developer's home directory, network, or Trello
account is not a test.

The sole native Keychain integration exception is a build-tagged cross-binary
CI check. It creates two fresh disposable `SecKeychainRef` values: Keychain A
is the explicit `kSecMatchSearchList` target containing the existing item, and
Keychain B is the explicit `kSecUseKeychain` add target. The second binary
unlocks both and verifies cross-binary access plus idempotent migration of
already-compatible allow-any ACLs. It deliberately does not create a
creator-only ACL or exercise a prompt-bearing ACL mutation; pure unit tests
cover that policy and status translation. It uses a synthetic marker, never the
default keychain or user search list; emits no marker or credential material;
and removes every item and both temporary Keychains on success, failure, signal,
or timeout. No ordinary test may widen this exception.

## What must be covered

Every behavior change needs a test that fails without the change. Beyond the
happy path, these cases are the ones that actually break:

- Empty and single-element results.
- Ambiguous match → exit 4 with a populated `candidates`.
- No match → exit 3 with `did_you_mean`.
- A cache hit whose object was deleted server-side → falls through to a live
  lookup rather than returning a stale ID.
- 429 with and without `Retry-After`.
- A non-JSON error body. Trello returns 401 as `text/plain`; a client that
  assumes JSON panics there.
- A panic inside a command surfaces as exit 1, never 2.

## Contract tests

Exit codes and the envelope are golden-file tested. When one legitimately
changes, the golden file changes in the same commit as `internal/errx` and the
regenerated `docs/contract.md`, never separately.

## Rules

- `go test -race ./...` must pass before review.
- Do not weaken an assertion to make a failing test pass. Report the failure.
- If a test cannot fail because of a real bug, delete it.
