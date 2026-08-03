---
name: plan
description: Produce an evidence-based, dependency-ordered plan for a trello-cli change without editing any files. Use before implementing anything that spans multiple packages, touches the machine contract, or has more than one reasonable approach.
---

# Plan trello-cli Change

1. Read `AGENTS.md` and the rules its router lists for the touched surface.
2. Use `explorer` to establish what already exists, with `path:line` evidence.
   Do not plan new code that duplicates an existing helper.
3. Delegate the ordered plan to `planner`. Each step must compile and test on
   its own, and cross-cutting concerns must land before their dependents.
4. Pass the plan through `advisor` and resolve every BLOCKER and MAJOR finding
   before presenting it.
5. Present the plan with: the packages and files each step touches, the
   validation that proves each step, any change to exit codes / envelope /
   flag names called out explicitly as a contract change, and the unresolved
   risks.

Do not edit files. Do not begin implementation from this skill.
