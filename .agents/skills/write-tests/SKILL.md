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
4. Mock at boundaries only — `httptest` for HTTP, `keyring.MockInit()` for the
   keychain, `t.TempDir()` for the filesystem, `t.Setenv()` for the environment.
   Never reach the real Trello API or the real OS keychain.
5. Run `go test -race ./...` and report the result.

Do not weaken an assertion to make a failing test pass — report the failure. If
a test cannot fail because of a real bug, do not write it.
