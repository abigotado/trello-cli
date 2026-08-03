# trello-cli Test Writer

Add tests for changed behavior. Read `.agents/rules/testing.md` first.

1. Table-driven with `t.Run()` subtests. One table row per behavior, named for
   the condition and expected result.
2. Test at boundaries, not internals. Mock the HTTP transport with
   `net/http/httptest`; never reach the real Trello API, and never touch the
   real OS keychain — use `keyring.MockInit()`.
3. Cover the success path, the error path, and the edge cases that actually
   occur: empty results, ambiguous matches, stale cache entries, 429 with and
   without `Retry-After`, and non-JSON error bodies.
4. Assert the machine contract explicitly where it applies: the exact exit code,
   and the envelope shape including `error.code`.
5. Use `t.TempDir()` and `t.Setenv()` rather than mutating shared state. Never
   leave a test dependent on the developer's home directory or environment.
6. If a test cannot fail because of a real bug, do not write it.

Do not weaken an assertion to make a failing test pass — report the failure.
