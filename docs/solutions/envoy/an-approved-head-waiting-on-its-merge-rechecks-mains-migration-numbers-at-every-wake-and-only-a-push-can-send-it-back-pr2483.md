---
title: "An approved head waiting on its merge rechecks main's migration numbers at every wake, and the merger can send it back to an earlier step"
category: envoy
tags:
  - migration
  - merge-queue
  - legion-workflow
  - dispatch
date: 2026-10-30
status: active
module: packages/envoy/internal/dispatch/store
related_issues:
  - "sjawhar/legion#2483"
---

# An approved head waiting on its merge rechecks main's migration numbers at every wake, and the merger can send it back to an earlier step

Extends docs/solutions/envoy/a-migration-number-is-checked-against-mains-highest-at-every-push-and-the-local-harness-database-is-recreated-after-a-renumber-INFRA-367.md.

- That note checks the number before every push and every review round. A head in
  `merging` has neither, and two new migration files never conflict as text, so GitHub
  keeps it `MERGEABLE`. Whoever watches the merge (the architect, the deployment lane) compares
  main's highest migration with the branch's on every wake while the head waits.
- The merger can move the issue back to implementing (`request_backward_move`), where the
  implementer merges main forward, renumbers and pushes. After that push, the task's later steps
  run again at the new head, in the task's own order. Expect it, and name the renumber in the
  round's report.
- Prove a renumber-only round by diffing the two fingerprinted diffs line by line: every changed
  line must be the old number replaced by the new.

## Evidence

- sjawhar/legion#2483 was accepted with `0145_asks_anchor_artifact_id` while main a21100c5 added
  `0145_events_person_words`, `0146` and `0147`. The text merged clean and GitHub showed the pull
  request `MERGEABLE`, so nothing re-armed it. Earlier collisions (0137, 0139, 0141, 0142) each
  came with a README conflict and a `CONFLICTING` state; this one came with none.
- The workflow table builds backward rows only for planning through merging
  (`packages/daemon/internal/workflow/table.go:78`), and `Engine.backward` refuses any move no row
  serves with `BACKWARD_REFUSED` (`engine.go:756`). The push to `0148` moved the head, and
  `withdrawMovedReady` (`ready.go:226`) took the issue back to implementing.
- The fingerprint comparison for that round showed 21 changed lines, each `0145` → `0148`.
