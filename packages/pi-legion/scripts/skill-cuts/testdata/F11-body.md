## Your choices

### Open proposals of #41 and #75 (R5)

These are the changes of the closed pull requests #41 and #75 that `main` does not have and that no other part of this pull request carries. This pull request carries none of these lines: each goes to dispatch://ACME-12, where the reader of dispatch://ACME-13 makes or drops it. To carry one here, write `keep <id>` in your review.

Lines this pull request can carry:

- `41-1.1` (open): before it deletes a rule, an agent finds the rule's reason (the clause beside it, else the commit that added it, with `git log -S`) and deletes the rule only if that reason no longer holds. Recommend keep: without it an agent deletes a rule whose reason still holds. Not carried: write `keep 41-1.1` to carry it.
- `41-3.2` (partly on main): how to review STALE skills (STALE alone is no reason to delete; a removed skill is deleted, not archived). Recommend drop: `writing-agent-skills` §4 already says "it never fires" is no reason by itself, and the rest needs `41-3.1`. Not carried: write `keep 41-3.2` to carry it.
- `41-10.5` (open): a missing word in `CLAUDE.md` ("into a rule all agents" becomes "into a rule for all agents"). Recommend keep: it is a typo fix. Not carried: write `keep 41-10.5` to carry it.
- `75-1` (open; no ruling found): before agents send you a design or a question, they link the existing code and two well-known tools that solve the same problem. Recommend keep: you can then judge the design without a search of your own. Not carried: write `keep 75-1` to carry it.

Lines whose target is gone from this repository (this pull request cannot carry them):

- `41-8.3` (target gone): AskGate would let an agent ask you for the purpose of an instruction. AskGate is now in acme/skills `packages/pi-envoy/extensions/askgate/rules.md`. Recommend drop: ACME-11's architect reads no conflict between AskGate and a question for a reason. Not carried.
- `41-9.12` (target gone): a spec answer recorded with its date, a link and its reason in the agent's words. The line left this repository for `dispatch-writing-specs`. Recommend keep only if you want it there: it then goes to that skill's owner. Not carried.
- `41-9.14` (done in its new home): AskGate's rules paraphrase you instead of quoting you; acme/skills `askgate/rules.md` already does this. Recommend drop. Not carried.

Lines this pull request carries through another requirement:

- `41-8.2`: the new line after line 62 of `CLAUDE.md` (R4).
- `41-13.2`: the `ignoredSkills` comment in `omp/config.yml` changes with R2's hide.

Lines with an item in a broken form:

- `41-0.1` open: the state of this item is not in parentheses.

## What else changes
