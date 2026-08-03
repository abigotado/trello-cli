---
name: review
description: Review completed trello-cli changes for correctness, architecture, regressions, contract breakage, leaked credentials, and missing tests. Use before merging, or when asked to check work that is already written.
---

# Review trello-cli Change

1. Read `AGENTS.md` and the rules routed for the touched paths, then the diff,
   then the surrounding code the diff assumes.
2. Delegate to `reviewer`.
3. Run `cli-contract-guardian` when exit codes, the envelope, flag names, or
   package dependency direction are touched.
4. Obtain an independent `adversarial-review` second opinion. This is not
   optional for non-trivial work — it is the pass that catches what the primary
   reviewer missed.
5. Confirm validation was actually run, with the command and its output. Do not
   accept "tests pass" as a claim.

Report findings first, most severe first, each with `path:line`. Keep the
summary brief and secondary. Never quote the rule files at length — cite the
path.
