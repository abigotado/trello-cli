---
name: implement
description: Implement a non-trivial trello-cli change through the complete provider-agnostic workflow. Use for new commands, client or resolver work, contract changes, auth changes, refactors, or any behavior change that needs planning, tests, review, and validation.
---

# Implement trello-cli Change

1. Read `AGENTS.md`, the routed `.agents/rules/`, and the nearest existing
   command and its tests.
2. Delegate dependency-ordered planning to `planner`.
3. For non-trivial scope, obtain `advisor` approval before any edit. If it
   returns NEEDS-CHANGES, return to `planner`.
4. Use `explorer` to replace unresolved assumptions with file-and-line evidence.
5. Delegate implementation to `coder`.
6. Delegate behavior tests to `test-writer` before review.
7. Run `cli-contract-guardian` when exit codes, the JSON envelope, flag names,
   or package dependency direction are touched.
8. Run `reviewer`, resolve material findings, then obtain an independent
   `adversarial-review` second opinion.
9. Run `gofmt -l .`, `go vet ./...`, and `go test -race ./...`. Report anything
   not run.

Do not edit `.claude/` or `.codex/` — they are compiled from `.agents/`. Do not
run repository-wide formatting, `go mod tidy`, or dependency upgrades unless the
accepted plan includes them.
