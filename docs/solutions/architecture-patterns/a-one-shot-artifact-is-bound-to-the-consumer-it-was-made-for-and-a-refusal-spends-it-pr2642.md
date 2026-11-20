---
title: "A one-shot artifact is bound to the consumer it was made for, a refusal spends it, and the unconsumed row holds its producer"
category: architecture-patterns
tags:
  - at-most-once
  - handover
  - idempotency
  - data-loss
date: 2026-11-04
status: active
module: packages/daemon
related_issues:
  - "sjawhar/legion#2642"
---

# A one-shot artifact is bound to the consumer it was made for, a refusal spends it, and the unconsumed row holds its producer

For an artifact that may be applied **at most once** — saved work, a migration payload, a
one-time credential — and whose second application would destroy newer state:

- **Record the claim in one statement, and read the payload apart from it.** One `update … where
  <unclaimed> returning <facts>` makes the eligibility test and the spend commit together; a
  read-then-mark lets two consumers both be served and lets a caller drop the mark after reading.
  The statement that changes the row returns the artifact's facts and none of its payload: the
  payload is a read of its own, by the consumer the row names as having spent the attempt, under
  the bound of the hand-over that follows. A call that both changes the row and carries a large
  payload has one bound for two jobs, and its failure cannot say which one failed.
- **Refuse the producer by identity, not by ordering.** The holder the artifact was derived from
  still has the original, so it needs nothing put back — and it asks through the same code path as
  every other consumer. Record that holder's id on the artifact and exclude it, because ordering
  alone cannot tell a recovery start inside the producer from the consumer that replaced it.
- **A refusal spends the one attempt.** When eligibility rests on a comparison that can be wrong,
  an unspent refusal leaves the artifact open for whatever asks next, which is the consumer whose
  state already descends from the work. Make the refusal terminal, record its outcome beside the
  consumer that caused it, and keep the payload for recovery.
- **Say which way the feature fails, beside the constant that decides it.** State the loss ordering
  in the code: here, "the work a role has not pushed is kept at the cost of a pod, never dropped to
  save one", and the clock whose resolution the comparison inherits.
- **"Spent" and "consumed" are different records, keyed differently.** Write the attempt before the
  first byte leaves; delete only on the consumer's own confirmation, keyed on the producing
  episode's id, so a late report of an older episode deletes nothing of a newer one.
- **An unconsumed artifact holds its producer closed.** Check it before any other condition, and
  report it by name, or the next episode saves an artifact of a state that never had the first
  one's work, over the row that does.

## Evidence

In sjawhar/legion#2642 a released Kubernetes pod's unpushed work travels to its replacement as a
git bundle. `store.TakeWorkspaceBundleForRestore` is one `update workspace_bundles set
attempt_pod_uid = $2, attempt_at = now(), attempt_outcome = case … where issue = $1 and
attempt_pod_uid is null and source_pod_uid <> $2 returning attempt_outcome, at, head, tips,
bookmarks, release_id, source_pod_uid`: it spends the attempt and returns no bytes of the bundle.
`store.WorkspaceBundleSentTo` reads those bytes, and only for the pod that took the attempt as
`sent` and for the release it took it of. The two were one statement until a later round found
that the call that changes the row must not also carry the payload: under the short bound of a
call that changes state, a slow read of up to 40 MiB came back as a store fault, and every retry
read the whole payload again; under the hand-over's bound, the read used the time the consumer's
start needed. Split, the take keeps its short bound and its meaning, and a failed read costs the
hand-over and nothing else.

Three review rounds built that shape, each from a traced loss:

- the first version deleted the row when the launcher reassembled the slices, so a restore that
  failed after reassembly lost the work;
- the next deleted by issue, which would have deleted the *next* release's bundle on a late report;
- the third left a refusal unspent, so after a cancel whose delete failed, or a skewed node clock
  refusing the real replacement, the next pod created after the save took the bundle and ran
  `jj bookmark set --allow-backwards` and `jj edit` over a workspace holding hours of pushed work.

`workflow.IdleRelease` answers `IdleHoldBundleUnrestored` before any per-claim condition, so the
issue's pod is held while a bundle nobody restored is in the database, and `docs/kubernetes.md`
gives the operator the command that reads those bytes back out.
