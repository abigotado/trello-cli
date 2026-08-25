---
name: write-tests
description: Add or update trello-cli Go tests for changed behavior. Use after implementing a change, or when coverage is missing for a command, the Trello client, the name resolver, credential handling, or the exit-code and envelope contract.
---

# Write trello-cli Tests

1. Read `.agents/rules/testing.md` and the nearest existing test file, then
   follow its shape.
2. Delegate to `test-writer`.
3. Cover, at minimum: the success path, the error path, empty and
   single-element results, ambiguous match (exit 4 with `candidates`), no match
   (exit 3 with `did_you_mean`), a stale cache entry that 404s, 429 with and
   without `Retry-After`, and a non-JSON error body.
4. Mock at boundaries only — `httptest` for HTTP, an injected fake credential
   store for the keychain, `t.TempDir()` for the filesystem, and `t.Setenv()`
   for the environment. Darwin unit tests cover pure wire-format, status, size,
   and upsert helpers; native CI compile/link checks cover the C bridge and type
   guard. They never reach the real OS keychain.
   The build-tagged cross-binary integration check is the only exception: it
   uses two disposable `SecKeychainRef` values, with A as the explicit
   `kSecMatchSearchList` target and B as the explicit `kSecUseKeychain` add
   target. The second binary unlocks both, never changes the default/user search
   list, prints no marker, and cleans up both on every exit path.
5. Run `go test -race ./...` and report the result.

Do not weaken an assertion to make a failing test pass — report the failure. If
a test cannot fail because of a real bug, do not write it.
