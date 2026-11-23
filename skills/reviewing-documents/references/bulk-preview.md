# The bulk-action preview

Interactive mode only. Before any bulk action, show one screen of what the agent is about to do, then ask Proceed or Cancel.

## Where it is shown, and over what

"Pending" means pending in the pass being served: the decisions, when serving the routing question; the rest of the batch, when serving the grouped confirmation's option B.

| From | Header | Scope |
| --- | --- | --- |
| Routing option B | `Auto-resolve plan — N findings:` | Every pending finding at 75 or 100 |
| Routing option C | `Append plan — N findings as Open Questions entries:` | Every pending finding at 75 or 100, all under Appending to Open Questions, whatever their recommendation |
| The walk-through's "Auto-resolve with best judgment on the rest" | `Auto-resolve plan — N remaining findings (K already decided):` | The current finding and every undecided one of the running pass |

## Withdraw first

Before you sort findings into buckets, judge each against the decisions already made: earlier answers, and another finding's Apply in this same plan. A finding they resolve or contradict goes under `Withdrawing`, not into an action bucket. A withdrawal by a staged Apply stays provisional: if that Apply fails, the finding returns to scope.

## The preview

Show it as visible text before the question; hidden reasoning does not count, and neither does text placed only inside the question tool.

```
Auto-resolve plan — 5 findings:

Applying (2):
  [P0] Requirements Trace — Renumber R4 (the auth-token requirement) to match its unit
  [P1] Unit 3 Files — Add a read fallback for the renamed report file

Appending to Open Questions (1):
  [P2] Scope Boundaries — Unit 2/3 merge judgment call

Skipping (1):
  [P2] Miscellaneous Notes — Low-confidence style preference

Withdrawing (1):
  [P2] Risks — resolved by the applied fix on "Scope Boundaries"
```

- **Each line** is `[<severity>] <section> — <one-line summary>`: the first sentence of the finding's consequence, paraphrased to fit about 80 columns, under `references/rendering-floor.md`. With no consequence text, use the title.
- **Omit an empty bucket.** A preview of one finding still uses a bucket.
- **Appending unavailable:** move each Defer recommendation to Skip, and say so: `N Defer recommendations downgraded to Skip — document is read-only.`

## The question

Stem by path: `The agent is about to apply the plan above. Proceed?`, `The agent is about to append the findings above to the doc's Open Questions section. Proceed?`, or `The agent is about to resolve the remaining findings above. Proceed?` Options: `Proceed`, `Cancel`.

| Answer | From routing B | From routing C | From the walk-through |
| --- | --- | --- | --- |
| Proceed | Apply set gets the Apply findings for one end-of-run edit pass; Defers append; Skips are recorded; then the completion report | Every finding appends to Open Questions; then the completion report | As routing B, joined with the answers already given. In the grouped confirmation's pass, write and clear the Apply set, then return to the routing question |
| Cancel | Back to the routing question | Back to the routing question | Back to the current finding's question |

- **Cancel changes nothing,** on disk or in memory.
- **Withdrawing** records each finding `withdrawn` with the decision that retired it: no edit, no append.
- **A failure during Proceed** (one append fails) takes the failure path of `references/open-questions-defer.md`; the rest of the plan goes on, and the completion report lists the failure.
