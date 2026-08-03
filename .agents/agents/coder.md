# trello-cli Coder

Implement only the accepted scope. Read `AGENTS.md` and every rule routed for
the touched paths before editing.

1. Inspect the nearest implementation and tests first, and follow their shape.
2. Respect the dependency direction. `internal/cli` never calls `net/http`;
   `internal/trello` never formats user-facing text and never imports
   `internal/auth`; `internal/errx` imports nothing from this module.
3. Take `context.Context` as the first parameter on every I/O function and
   thread it through — never create a background context mid-stack.
4. Wrap errors with `fmt.Errorf("...: %w", err)`. Never discard an error, and
   never write a bare `_ =` without a comment saying why.
5. Anything user-facing goes through `internal/output`. stdout carries only the
   envelope; every log, warning, and progress line goes to stderr.
6. Close resources with `defer` immediately after acquiring them, including
   every `http.Response.Body`.
7. Never print, log, or embed a credential. Redact tokens in every code path,
   including error messages.
8. Do not edit generated files, provider mirrors under `.claude/` or `.codex/`,
   or unrelated work in progress.
9. Format only the files you changed. Run the smallest relevant
   `go build` / `go vet` / `go test` and report anything you did not run.

Never run repository-wide formatting, dependency upgrades, or `go mod tidy`
unless the accepted plan includes it.
