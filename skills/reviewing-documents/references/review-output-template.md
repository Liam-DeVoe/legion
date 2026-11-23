# The review's output

Render only what synthesis retained, each finding as `references/rendering-floor.md` says. Count each finding by its final route: an obligation awaiting confirmation counts as a proposed fix, an applied one only as an applied fix, and there is no separate obligation count.

## The non-interactive result

The text inside the fence is the whole output: the caller parses it, so add no note around it.

```
Document review complete (non-interactive mode).

Applied N fixes:
- <section>: <what was changed> (<reviewer>)

Implementation obligations (already entailed by the document; awaiting one grouped confirmation):

<unit or section name>
  - <consequence, no opaque identifier> — <change as intent>

Proposed fixes (nothing here has landed; awaiting the same grouped confirmation):

[P0] Section: <section> — <consequence-first title> (<reviewer>, confidence <anchor>)
  Recommendation: <Apply | Defer | Skip>
  Consequence if unchanged: <one sentence, no opaque identifier>
  Change: <suggested_fix as intent>
  Basis: <at most two sentences of mechanism, identifiers glossed>

Decisions (requires user judgment):

[P1] Section: <section> — <consequence-first title> (<reviewer>, confidence <anchor>)
  Recommendation: <Apply | Defer | Skip>
  Consequence if unchanged: <one sentence, no opaque identifier>
  Change: <suggested_fix as intent, or "none">
  Basis: <at most two sentences of mechanism, identifiers glossed>

FYI observations (anchor 50, no decision required):

[P3] Section: <section> — <consequence-first title> (<reviewer>, confidence <anchor>)
  Consequence if unchanged: <one sentence, no opaque identifier>

Residual concerns:
- <concern> (<source>)

Deferred questions:
- <question> (<source>)

Coverage: <lens>: <completed | failed: <reason> | malformed>, <N> findings; ...

Dropped: N (anchors 0/25 suppressed)
Restated: N (residual/deferred items suppressed as duplicates of actionable findings)

Review complete
```

- **Omit every section with no item.** Keep the Coverage line always.
- **On a document with no implementation units,** title the obligations `Entailed corrections`, with section names as the group headings.
- **Nothing under Proposed fixes has been confirmed.** Word nothing as if it had: a caller reading it as accepted would treat unapproved edits as done.
- **A caller that names its own output form gets that form,** holding the same routes and fields: applied fixes, proposed fixes awaiting confirmation, decisions, FYI observations, residual concerns, deferred questions and Coverage.
- **End with `Review complete`** as the last line, so the caller detects completion.

## The interactive presentation

Show it as visible text in the same turn, immediately before the first question. A result printed in an earlier turn, or a one-line count, does not count. Group findings by severity, not by lens.

```markdown
## Document Review Results

**Document:** docs/plans/user-auth-plan.md
**Type:** plan
**Reviewers:** coherence, feasibility, security-lens, scope-guardian
- security-lens -- plan adds a public API endpoint with an auth flow
- scope-guardian -- plan has 15 requirements across 3 priority levels

Applied 2 changes. 2 awaiting one confirmation. 1 decision (1 error). 1 FYI observation.

### Applied fixes

- Standardized "pipeline" and "workflow" to "pipeline" throughout (coherence)
- Updated the unit count from "6 units" to "7 units" to match the listed units (coherence)

### Implementation obligations

Already entailed by the plan; confirm as a group.

**Unit 7 — Cutover**

- Executors could take opposite actions at the destructive step, because two passages disagree on whether a production reset may run — align the assumption with the stop condition.

### Proposed fixes

One fix brings a unit in line with a limit the plan sets everywhere else. Confirm as a group.

| # | Section | Issue | Reviewer | Confidence | Tier |
|---|---------|-------|----------|------------|------|
| 1 | Unit 5 — Webhooks | A flood of requests reaches the app, because the public webhook is the only public route with no rate limit | security-lens | 100 | gated_auto |

### P0 — Must Fix

#### Errors

| # | Section | Issue | Reviewer | Confidence | Tier |
|---|---------|-------|----------|------------|------|
| 2 | Requirements Trace | Offline use fails, because the approach assumes a persistent connection while a goal promises offline support | coherence | 100 | manual |

### FYI Observations

| # | Section | Observation | Reviewer | Confidence |
|---|---------|-------------|----------|------------|
| 1 | Verification | Each run spends 15 minutes on manual fixture setup that the existing seed script does in 2 | feasibility | 50 |

### Coverage

| Persona | Status | Findings | Auto | Proposed | Decisions | FYI | Residual |
|---------|--------|----------|------|----------|-----------|-----|----------|
| coherence | completed | 3 | 2 | 0 | 1 | 0 | 0 |
| feasibility | completed | 1 | 0 | 0 | 0 | 1 | 0 |
| security-lens | completed | 1 | 0 | 1 | 0 | 0 | 0 |
| scope-guardian | completed | 1 | 0 | 1 | 0 | 0 | 0 |
| product-lens | not activated | -- | -- | -- | -- | -- | -- |

Dropped: 3 (anchors 0/25 suppressed)
```

- **Summary line:** `Applied N changes. M awaiting one confirmation. K decisions (X errors, Y omissions). Z FYI observations.` Omit each zero clause except the FYI one. Never say an item needs attention when nothing is asked of the reader.
- **Applied fixes:** every fix routed to Apply, with enough substance that the reader need not diff the document, above all for added content.
- **Implementation obligations,** or `Entailed corrections` on a document with no units: one line each, grouped under the unit or section they affect.
- **Proposed fixes:** the rest of the grouped confirmation, shaped as the floor's batch rule says. Nothing from the decision sections belongs here: a decision swept into a batch answer is the failure this split prevents.
- **P0 to P3:** only the decisions the user must still make, each severity split into Errors, then Omissions. Their Tier is `manual`; a `gated_auto` row there means routing went wrong.
- **Residual Concerns and Deferred Questions:** tables of `#`, the concern or question, and its source.
- **Compact mode:** when FYI observations, residual concerns and deferred questions together number 5 or more, render each as a one-line count and a tight bullet list. In both modes, actionable findings stay fully rendered.
- **Coverage, always:** post-synthesis counts, where Findings equals Auto + Proposed + Decisions + FYI exactly, Proposed includes obligations, and a failed lens shows `failed` with its reason. Add no other column. Put the `Dropped:` and `Restated:` footnotes below it, each only when non-zero.
- **Confidence** is the integer anchor, 50, 75 or 100; a promotion shows in the Reviewer column as `(+1 anchor)`.
