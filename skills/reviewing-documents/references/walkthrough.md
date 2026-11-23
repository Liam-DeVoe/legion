# Interactive: confirmation, routing and the walk-through

Interactive mode only. Ask every question with the question tool `references/modes.md` names.

## 1. The grouped confirmation

- **Ask it after the applied changes and before the routing question;** skip it when the batch is empty. It is the only place the batch is applied.
- **Render the batch first,** visibly, in the same turn: exactly the obligations and the Proposed fixes sections. The decisions are not part of it.
- **Name what the batch does in the stem:** `Six fixes: four align the tier vocabulary, two add the missing cross-references. Apply them?`

```
A. Apply all of them (recommended)
B. Choose which to apply
C. Apply none of them
```

- **A:** apply the batch in one pass, and list each under Applied.
- **B:** step through the batch with the per-finding loop below. Its one exit: write the accumulated Apply set to the document, clear the set, and return to the routing question. It never emits the completion report; "Auto-resolve with best judgment on the rest" there covers only the rest of the batch and returns here too.
- **C:** apply none; report each as skipped.

## 2. The routing question

- **Follow a route the user or the calling agent already chose;** ask only when none was chosen. A chosen route waives neither the approval of proposed fixes nor the preview before a bulk action.
- **Present the review in the same turn, before the question** (`references/review-output-template.md`), showing the current state: the batch is settled, only decisions are open.
- **No decision left:** skip the question, and still emit the completion report.

Stem: `What should the agent do with the remaining N findings?` Options, in this order, none marked recommended (the right route depends on what the user wants):

```
A. Review each finding one by one — accept the recommendation or choose another action
B. Auto-resolve with best judgment — apply per-finding edits the agent can defend, surface the rest
C. Append findings to the doc's Open Questions section and proceed
D. Report only — take no further action
```

| Answer | Do |
| --- | --- |
| A | The walk-through below |
| B | `references/bulk-preview.md`, over every pending finding at 75 or 100 |
| C | `references/bulk-preview.md`, with every pending finding under Appending to Open Questions |
| D | The completion report |

When appending is unavailable (`references/open-questions-defer.md`), drop option C and add one line saying why.

## 3. The walk-through

The decisions, P0 first, each with the recommendation synthesis chose. For each, print the block, then ask; never merge the two.

```
## Finding {N} of {M} — {severity} {plain-English title}

Section: {section}

**What's wrong**

{the consequence, then at most two sentences of mechanism}

**Proposed fix**

{one sentence of intent}

**If this is left as-is**

{one sentence: the concrete cost, plainly small when it is small}

{when lenses disagreed: Coherence recommends Apply; scope-guardian recommends Skip. Agent's recommendation: Skip.}
```

The question string repeats the decision in compact form, for a modal dialog that hides the chat:

```
Finding {N} of {M} — {severity} {short handle}.
What's wrong: {one sentence, no opaque identifier}
Proposed fix: {one sentence, or "none"}
If left as-is: {one sentence}
{the recommended action as a yes/no question}?
```

If the tool rejects a multi-line string, retry once with the last line alone. The options are exactly these four, in this order:

```
A. Apply the proposed fix
B. Defer — append to the doc's Open Questions section
C. Skip — don't apply, don't append
D. Auto-resolve with best judgment on the rest
```

- **Mark synthesis's recommendation `(recommended)`** on A, B or C; never on D.
- **Confirm each answer in one line** before the next block: `→ Applied. Edit staged at "Scope Boundaries" section.`, `→ Deferred. Entry appended to "## Deferred / Open Questions".`, `→ Skipped.`
- **One pending finding:** drop `Finding N of M` and option D.
- **Appending unavailable:** drop option B, add one line saying why, and move a Defer recommendation to Skip, saying so on the disagreement line.
- **Competing remedies** (a contradiction kept in synthesis): ask first `This problem is settled; the remedy is not. Which do you want?`, one option per remedy naming what differs. Carry the answer into the menu as the proposed fix. Never ask it with one option, and invent no second one.

## 4. Each answer

| Answer | Do |
| --- | --- |
| Apply, with a suggested_fix | Add it to the Apply set; edit nothing yet |
| Apply, with no suggested_fix | Ask: `Apply isn't executable for this finding — the review surfaced the issue without a concrete fix. How should the agent proceed?` with `A. Defer to Open Questions (recommended)`, `B. Skip — don't apply, don't append`, `C. Acknowledge without applying — record the decision, no document edit` (appending unavailable: drop A, recommend B) |
| Defer | Append now, as `references/open-questions-defer.md` says, and record where |
| Skip | Record it |
| Auto-resolve the rest | `references/bulk-preview.md` over this finding and the undecided rest. Cancel returns to this finding's question |

- **Withdraw a finding that earlier answers settled.** When its turn comes, judge it against every answer so far; if they resolve or contradict it, ask nothing, say in one line what it was and which answer settled it, and record it withdrawn. An explicit answer from the user outranks a withdrawal.
- **A withdrawal by a staged Apply is provisional:** if that Apply fails, report the finding as returned to scope.
- **No freeform fix in the loop.** A user who wants a variant picks Skip and edits outside it, or Defers first.
- **State lives in memory:** the Apply set, the decision list, the position. An interrupted walk-through loses it, and no staged edit has been written.

## 5. The end

1. **Apply the Apply set in one pass,** in the document's own format. Skip an entry with no suggested_fix and list it as a failure: `Apply skipped — no suggested_fix available`.
2. **Emit the completion report,** at every ending: failures first (a failed Apply or append), then one line per finding grouped as Applied, Deferred, Skipped, Acknowledged and Withdrawn (title, severity, and the append location, skip reason, acknowledgement reason or retiring decision), then the counts per bucket, then the FYI and residual concerns, then the verdict: `Ready`, or what still blocks the work. Omit empty buckets.
3. **When nothing was ever put to the reader,** collapse it: `All findings resolved — 3 fixes applied.`, with `(2 FYI observations, 1 residual concern remain in the report.)` when some remain, then `Verdict: Ready.`
