# Round 2 and later: the decision primer, and its checks

Read this when this session already finished at least one review round of the same document. The primer tells each lens what the user already decided, so a new round neither raises a rejected finding again nor assumes an applied fix landed. Decisions do not carry across sessions: a later session starts at round 1, even when earlier sessions deferred findings into the document.

## Render the primer

Put every earlier round's decisions in the `{decision_primer}` slot:

```
<prior-decisions>
Round 1 — applied (N entries):
- {section}: "{title}" ({reviewer}, {confidence})
  Evidence: "{evidence_snippet}"

Round 1 — rejected (M entries):
- {section}: "{title}" — Skipped because {reason}
  Evidence: "{evidence_snippet}"
- {section}: "{title}" — Deferred to Open Questions because {reason or "no reason provided"}
  Evidence: "{evidence_snippet}"
- {section}: "{title}" — Acknowledged without applying because {reason}
  Evidence: "{evidence_snippet}"
- {section}: "{title}" — Withdrawn because {the decision that retired it}
  Evidence: "{evidence_snippet}"
</prior-decisions>
```

- **Every entry carries an `Evidence:` line:** the finding's first evidence quote, cut to about 120 characters on a word boundary, inner quotes escaped. The checks below compare evidence; without it they match on titles alone, and either show rejected findings again or suppress too much.
- **Skip, Defer and Acknowledge are rejected-class.** Acknowledge records a finding the user saw and chose not to act on; for suppression it is a Skip.
- **Withdraw is rejected-class only when a user decision retired the finding:** a settled premise (a Skip or Defer) or a fact the user asserted. A withdrawal triggered by a staged Apply never is: whether that fix landed is checked fresh next round.
- **Applied entries stay on the applied list,** so the next round can check that each fix landed.

## Suppress re-raised rejections

For each finding of this round, compare it with the primer's rejected entries. It matches when its normalized section and title (lowercase, punctuation stripped, whitespace collapsed) equal an entry's, and its evidence overlaps that entry's by more than 50%.

- **Suppress a match** only while the evidence and assumptions behind the rejection still hold. Note it in Coverage as "previously rejected, re-raised this round".
- **Reassess it** when material changes to the document, its sources or the constraints, or new facts, undermine the rejection. An unchanged quote does not prove unchanged evidence. A newly supported problem then goes through the ordinary admission and authority steps; reassessment never licenses reversing a user's commitment.

The lenses get a soft version of this rule through the primer; you make the final call.

## Check that applied fixes landed

For each finding of this round whose normalized section and title match an earlier Applied entry:

| Evidence overlap with the Applied entry | Do |
| --- | --- |
| More than 50% | The fix did not land: report "fix did not land" with the earlier title and this round's evidence, not as a new finding |
| 50% or less, and the finding only confirms the fix landed | Suppress it, and record `Verified: round-{N} '{title}' landed correctly` in Coverage |
| 50% or less, otherwise | A new finding: route it normally |

- **A substantively different concern** in the same section is a new finding, whatever the fingerprint says.
- **A renamed section** is a different location: the finding is new.

## When rounds repeat

Fixed findings drop out on their own, because their evidence is gone from the document. If findings still repeat across rounds after these checks, recommend completion.
