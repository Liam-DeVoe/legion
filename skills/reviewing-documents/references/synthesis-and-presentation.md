# Judging the findings, and routing them

Run the steps in order over every lens's return. Judge each finding afresh at each step; carry no earlier assumption forward as a shortcut.

## 1. Validate

- **Drop a malformed finding:** a required field missing, or a value outside its enum. Mark that lens's output `malformed` in Coverage.
- **Never narrate schema drift or remapping to the user:** the Coverage mark is all it sees.

## 2. Admit by consequence

- **Retain a concern only** when the document's instructions cannot all be satisfied together, or when following the document, its references and the project's conventions would produce a demonstrated wrong outcome or worthwhile avoidable work.
- **Judge an omission against what a competent implementer can already derive.** A missing restatement, a finer threshold or an extra procedure is no defect when the existing instructions suffice.
- **Investigate before you accept a lens's claim.** Read the code, records or source it rests on. Establish the problem apart from its suggested fix: a useful-looking addition does not prove that anything is missing.
- **Keep the smallest supported correction** that addresses the actual consequence. A verification change must test the required outcome, not merely produce a passing check.
- **Drop rejected claims** from the working set: they need no decision and no edit. Only admitted concerns go on, FYI and deferred questions included. Zero findings is a valid result.

## 3. Gate by confidence

| Anchor | Route |
| --- | --- |
| 0, 25 | Drop silently. Count them in the footnote `Dropped: N (anchors 0/25 suppressed)` |
| 50 | FYI observation, whatever its class: no decision, no bulk action |
| 75, 100 | Actionable: routed by class in step 8 |

## 4. Merge duplicates

- **Two findings are duplicates when one fix resolves both.** Read their titles, sections, consequences, evidence and fixes; wording and a shared section are evidence, never the test.
- **When unsure, do not merge.** A surviving duplicate costs one line; a wrong merge buries a real concern.
- **Opposing recommendations never merge:** step 6 resolves them.
- **A merged finding** keeps the highest severity and anchor (on a tie, the first in document order), unions the evidence, and names every contributing lens. Keep each constituent as a record too: round-to-round matching uses its own section, title and evidence.
- **Attribute it in Coverage** to the lens with the highest anchor (on a tie, the first in document order), and take it off the other lens's counts.
- **Merging never drops a concern.** The merged set is the one record for counts and rendering: each finding appears once.

## 5. Weigh agreement

- **Raise confidence by at most one anchor step,** and only when the combined evidence meets the next anchor's definition. Several lenses noticing a nit does not make it worth fixing; a serious defect needs no second vote.
- **Only separately dispatched lenses count as independent.** Lenses you ran inline never promote. Mark a promotion `(+1 anchor)` in the Reviewer column.
- **A finding dropped at 0 or 25 never returns** through agreement, and agreement never grants permission to edit.

## 6. Resolve contradictions

- **Check the conflicting claims** against the document, the project's evidence and the requested outcome before you ask anyone. Drop a disproven claim, or a preference with no significant benefit, and note why internally so it does not return.
- **Keep one combined manual finding** when several fixes remain and choosing needs an unresolved user preference or scope decision. Hold both views and the decision needed; set its type by the document's actual defect. Never schedule opposing fixes as separate edits.

## 7. Choose who settles the fix, and the recommendation

- **Each retained problem leaves with a fix you can choose, or a question only the user can settle.** A lens's class, or an earlier review's heading, is not a user decision.
- **Use manual only** when you can state the missing input or choice, why the agreed outcome and constraints leave it open, and how the answer changes the work. Where the document already decides the outcome, choose the smallest supported correction: other workable methods do not hand the choice to the user.
- **Recommend Apply** when the correction is justified and concrete, **Defer** when a worthwhile problem cannot yet be resolved, and **Skip** only when a consequential unresolved choice still needs the user; then explain that choice. A recommendation without a specific suggested_fix becomes Defer.
- **Where lenses implied different actions,** keep one line with your choice and its evidence. The walk-through and the bulk preview reuse it, never recompute it.

## 8. Establish edit authority, and route

- **Explicit restrictions win:** read-only, report-only, or a narrower scope. A document you hold only as a copy has no edit authority.
- **By default you may correct a proven defect** that stops a concrete decision already in the document from being carried out. Name that decision and how the defect blocks it. Broad goals (quality, safety, clarity) authorize no particular edit. The correction keeps every user commitment and needs no unresolved input.
- **Such a correction goes to Apply** at anchor 100 with a specific suggested_fix, whether safe_auto or gated_auto.
- **An explicit grant** from the user or the caller covers further technical corrections at 75 or 100 within its named scope. It never covers product outcomes, constraints or choices the user reserved, unless it says so. Confidence, agreement and non-interactive mode never grant authority.

Route everything else by this table:

| Anchor | Class | Route |
| --- | --- | --- |
| 100 | safe_auto | Apply when the request allows default edits; without a suggested_fix, treat it as gated_auto |
| 100 | gated_auto | Grouped confirmation; without a suggested_fix, a decision |
| 75 | safe_auto, gated_auto | Grouped confirmation: unattended edits need 100; without a suggested_fix, a decision |
| 75, 100 | manual | Decision |
| 50 | any | FYI |

- **An obligation** is a retained defect whose remedy follows from a decision the document already made, with a written fix. One still needing approval joins the grouped confirmation, under the unit or section it affects. Grouping changes presentation, not authority.
- **A useful improvement is not an entailed correction.** With no settled decision requiring it and no grant covering it, it goes to the grouped confirmation, not Apply.
- **Check each correction against its problem** before you apply or propose it: it resolves the retained problem, keeps the agreed outcome and adds no requirement. Verify a prescribed mechanism against the project's real interfaces.
- **Never apply an edit that removes or rewords a `session-settled:` note:** send it to the grouped confirmation.

## 9. Sort, and trim the rest

- **Sort** by severity (P0 first), then errors before omissions, then anchor (100 first), then document order.
- **Hold residual risks and deferred questions to step 2,** and drop each one that repeats a final finding, FYI included. Count them in the footnote `Restated: N (residual/deferred items suppressed as duplicates of actionable findings)`, below `Dropped:`. Omit a footnote whose count is zero.
- **Round 2 or later:** suppress re-raised rejections and check that earlier fixes landed, as `references/decision-primer.md` says.

## Apply

- **Apply every finding routed to Apply in one pass,** in the document's own format and structure. For an identified HTML item, mirror its nearest sibling's markup, anchor and visible id.
- **Ask no approval for these:** the routing settled it. Apply nothing routed elsewhere; if such a finding reaches this step, run step 8 again for it.
- **Check each edit** resolves its finding and keeps the document's contract, and list it with its section, what changed, its lens and the decision or grant that authorized it.
