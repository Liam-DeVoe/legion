I mapped most of what you asked, but not all of it. The kind sections below are mostly complete. Part of the test-file search, a few builder details, and the `go list` run are not done. Everything below was read at the workspace (main b4a566cb); I changed nothing. Lines marked **[not read]** are ones I did not open, and **[not grepped]** marks kinds whose tests I did not search for.

**Where I stopped:**
- **Tests:** I did not search tests for `held`, `worker-died`, `worker-silent`, `worker-background-stalled`, `session-lost`, `session-store-fault`, `review-stuck` (notice side), `pr-blocked`, `checks-red` (notice side), `design-approved`, `design-changes-requested`, `carried-gate-opened`, `catch-up`, `tick`, `triage`, `todo`, `slot-free`, `parked-decision` or `update-stalled`.
- **Builders:** I did not read `engine.go:~1370-1395` (where the restart reason comes from), `catch_up.go:304-394` (how the CatchUp fields are filled), `daemon/parked_decision.go:~40-80`, `daemon/repositories.go` around 1420, or where the tick's `OpenCapabilities` is set.
- **Import graph:** this session has no shell tool, so I could not run `go list`. The graph section is taken from the import lines in the source files.

## Summary
- **Where the texts are built.** Most architect notices are `record.Notice` values built in `workflow`. A few come from `admit`, `api`, `daemon` and `record` itself. The 11 controller-only kinds are `record.ControllerNotice`.
- **Role messages.** They are five outbox payloads:
  - `RoleMessage` (CI settlement, design gate moved, review stuck at the reviewer, memory kill).
  - `PhaseEnded`, `HookPrompt`, `UpdateAsk` and `MergeQueueRemoval`. Each of these builds its text with a `Message()` method in `record/outbox.go`.
- **Tasks.** They are strings carried in `SuperviseRequest.Task`, and `supervise.Delivery.message` puts the relaunch, abandoned-session, out-of-memory and provider-error sentences in front of them.
- **Publishing.** Every publish goes through `notify.Publisher.Publish(ctx, topic, message, payload, dedupeKey)`:
  - Notices use the summary `"<kind> on <issue>"` and the Notice as payload.
  - Role messages send the body string as both message and payload.

## Files
- `record/outbox.go:251-386`: the `Notice` struct and its JSON keys.
- `record/outbox.go:624-684`: `ControllerNotice` and the 11 controller kinds.
- `record/outbox.go:1018-1021`: `RoleMessage`.
- `record/outbox.go:1141-1176`: `PhaseEnded` and its `Message()`.
- `record/outbox.go:1189-1240`: `HookPrompt` and its `Message()`.
- `record/outbox.go:1254-1270`: `UpdateAsk` and its `Message()`.
- `record/outbox.go:1305-1330`: `MergeQueueRemoval` and its `Message()`.
- `record/outbox.go:1560-1770`: `validateOutboxPayload`.
- `record/outbox.go:1819-1826`: the `validNoticeKinds` list.
- `record/outbox.go:1876-1884`: `controllerOnlyNoticeKind`.
- `record/owning_architect.go:14-34`: `OwningArchitect` and `NoticeStartsAtParent`.
- `record/parked_decision.go:100-140`: `RelayParkedDecision`, which builds the parked-decision notice. Lines 202-260 hold `DecisionKind` and `SpecDecision`.
- `workflow/effects.go:283-300`: `notice` and `noticeWithController`.
- `workflow/effects.go:392`: `testerVerdictLabel`.
- `workflow/effects.go:931-972`: `task`, `continueLine` and `ResumePhaseTask`.
- `workflow/effects.go:979-1026`: `HeldNotice`, `ChildReenteredNotice` and `ChildStartedNotice`.
- `workflow/engine.go:1515-1683`: `transition`, `noticeFor` and `phaseEnded`.
- `daemon/outbox.go:1447-1772`: the executors `deliverNotice`, `roleMessage`, `phaseEnded`, `hookPrompt`, `updateAsk`, `publishToRole` and `controllerNotice`.
- `daemon/notice_routing.go:21-31`: `noticeSummary` and `parseNoticeSummary`.
- `daemon/notice_routing.go:140-178`: `earlierNoticeFor` and `noticeArchitect`.
- `daemon/notice_exceptions.go:189-276`: `noticeAnswered`.
- `daemon/notice_exceptions.go:314-440`: `reholdNotice`.
- `daemon/notice_exceptions.go:491-514`: `exceptionNotice`.
- `daemon/notice_exceptions.go:541-603`: `releaseWaiting`.
- `daemon/merge_queue_removal.go:129-187`: `mergeQueueRemoval`.
- `daemon/wake.go:217-272`: `wakeForDelivery`.
- `notify/notify.go:174-176`: `ControllerTopic`.
- `notify/notify.go:179-181`: the `Publisher` interface.
- `notify/notify.go:215-266`: `HTTPPublisher.Publish`.

## Architecture
- **Writing a row.** A workflow, admit, api or daemon builder makes a payload, and `record.NewOutboxRow` validates it (`validateOutboxPayload`). The daemon outbox executor then publishes it.
- **Routing a Notice.** `deliverNotice` sends it to `notifications.role.<architect token>`. `noticeArchitect` picks the architect:
  - A catch-up goes to its own issue's architect.
  - Every other notice goes to `record.OwningArchitect`. The walk starts at the parent for `child-closed`, for `child-status`, and for `worker-died`/`worker-stalled` whose Role is architect.
- **Controller notices** go to `notify.ControllerTopic(project)` = `notifications.legion.<project>.controller`.
- **Role messages** go to `notify.RoleTopicPrefix + token`.
- **`noticeWithController`** writes the architect row and a `ControllerNotice(notice)` copy (`effects.go:295-300`). The copy is used for `held`, `session-lost` and a tree architect's own `worker-died`. `daemon/stallbackstop.go:204-211` does the same for a tree architect's `worker-stalled`.

## Report

### 0. Shared publish path
- **`notify.Publisher.Publish(ctx context.Context, topic, message string, payload any, dedupeKey string) error`** (`notify/notify.go:179-181`).
  - `HTTPPublisher.Publish` (`notify.go:215-266`) posts `{topic, message, payload: json(payload) as a string, dedupe_key, keep:false}` to `/v1/messages/publish`.
  - It maps the listener's answers to errors: 404 `holder_lapsed` with unresponsive state → `ErrHolderUnresponsive`; 404 no-holder → `ErrNoHolder`; 504 → `ErrNotDelivered`; 403 capability → `ErrRefusedByHolder`; 403 publish denied → `ErrPublishDenied`.
- **The five test fakes:**
  - `daemon/catch_up_settling_test.go:33` `hookPublisher`
  - `daemon/kept_decision_property_test.go:221` `keptPublisher`
  - `daemon/notice_routing_test.go:36` `holderPublisher`
  - `daemon/outbox_lanes_test.go:138` `gatedPublisher`, which wraps `holderPublisher`
  - `daemon/outbox_test.go:2601` `outboxPublisher`, which records topic, message, key and payload
- **`deliverNotice`** (`daemon/outbox.go:1447-1533`).
  - Summary: `noticeSummary(payload.Kind, row.Issue)` = `"<kind> on <issue>"` (`notice_routing.go:21-23`; the separator `" on "` is at :31).
  - Payload and key: `notice, key := payload.Published(row.ID)` (`record/outbox.go:607-615`). This strips `PacedAt`, `PacedFrom` and `ResendOf`. The key is `OutboxKey(ResendOf or row id)`.
  - Order of checks before the publish: catch-up superseded → child out of the workflow → `stoppedWithTree` (then `parkDecision`) → `wakeForDelivery` → `earlierNoticeFor` fence. The publish is at `:1531`, and the answer goes to `noticeAnswered`.
- **`controllerNotice`** (`outbox.go:1765-1772`): summary `noticeSummary(kind, row.Issue)`, payload `record.Notice(payload)`, key `OutboxKey(row.ID)`.
- **`roleMessage`** (`outbox.go:1547-1584`): `Publish(topic, payload.Body, payload.Body, key)`.
  - The row ends on no holder, on refused-by-holder and on publish-denied.
  - It waits on an unresponsive holder.
- **`publishToRole`** (`outbox.go:1753-1760`): `Publish(topic, message, message, key)`. A no-holder answer ends the row with an Info log line. `phaseEnded` (`:1599-1622`), `hookPrompt` (`:1647-1670`) and `updateAsk` (`:1693-1720`) use it.
- **`mergeQueueRemoval`** (`merge_queue_removal.go:129-187`): `Publish(role topic of the merger, message, message, key)`. It waits on no holder and on an unresponsive holder.
- **`exceptionNotice`** (`notice_exceptions.go:491-514`): decodes the listener's report back into a Notice. It needs `parseNoticeSummary(PayloadSummary)` to match `notice.Kind`. **The summary format is load-bearing here.**
- **`reholdNotice`** (`:314-440`): queues a copy with `Resends+1` and `ResendOf` set.
- **`earlierNoticeFor`** (`notice_routing.go:140-160`): decodes the earlier rows of the tree and holds a notice behind the first one bound for the same architect.
- **`wakeForDelivery`** (`daemon/wake.go:217-272`): resumes a released claim. Its detail strings, which go to logs only, are:
  - `"a notice of "+issue` (`outbox.go:1508`)
  - `"the end of <issue>'s <from> phase"` (`:1611`)
  - `"the <hook> hook's prompt for <issue>"` (`:1659`)
  - `"the merge-queue removal of <issue>"` (`merge_queue_removal.go:162`)
- **`releaseWaiting`** (`notice_exceptions.go:541-603`): when an architect reports ready, it moves its waiting notices forward (`ExpediteOutbox`), but not a catch-up whose `PacedAt` is still in the future.

### 1. Notice JSON keys (`record/outbox.go:258-386`)
`kind, role, phase, summary, verdict, version, reason, topic, failure{cause, detail, launchFailures{count, limit}, deaths, promptFailures, promptRetires, death, startFailure}, escalation, resends, paced_at, paced_from, resend_of, catch_up{generation, launch, handed_over, carried, gates[{step, gated, unregistered, artifact, version, approved, open}], issues[{key, parent, title, phase, status}], decided, kept}, openCapabilities, blocked[{issue, blockedBy}], held, silent[{issue, role, lastStep, silent, backgroundSince}], actor, ref, decision, root, update{round, pod, reasons, waiting[{claim, role, state, answered}]}`.

### 2. Every non-test line matching `NoticeKind *= *"` or `record.NoticeKind("`
- `record/outbox.go:674-684` (the 11 controller constants)
- `workflow/silence.go:18` (`workerSilentNotice = "worker-silent"`)
- `workflow/silence.go:191` (`workerBackgroundNotice = "worker-background-stalled"`)
- `workflow/children_first_notice.go:13` (`ChildrenFirstNoticeKind = record.NoticeKind("children-first")`)
- `workflow/linger.go:140` (`kind := record.NoticeKind("child-status")`)
- Not matched by the pattern, but the same kind of definition: `daemon/stallbackstop.go:242` `const stallBackstopNotice = "worker-stalled"` (untyped).

### 3. The 25 architect notice kinds (`validNoticeKinds`, `record/outbox.go:1819-1826`)

**phase-finished**
- **Builder:** `noticeFor`, `workflow/engine.go:1647-1660`, called from `transition` at `:1615`.
- **Readers:** the owning architect.
- **Variants:**
  - Default (`:1659`): keys `kind, role=RoleFor(from), phase=from, summary=handoff.Summary, verdict=handoff.Verdict`. Summary is the worker's own handoff summary; verdict is the tester's.
  - Gate opened (`:1652`): `kind, phase=admitted`, no role. Summary and verdict are empty because the handoff passed is `PhaseRow{}` (`engine.go:1778`).
  - Backward move (`:1654-1655`): `kind, role, phase, reason="moved back to <to> by the <role>: <reason>"`. The reason is the worker's own words (`fact.Reason`, `engine.go:1466`).
- **No notice** for `TriggerSignOff` or `TriggerPullRequestMerged` (`:1656-1657`).
- **Tests:** `workflow/engine_test.go:1036` (the backward reason).

**worker-died** (`workflow/engine.go`, `claimFailed`)
- Five variants:
  - `:952`: a tree architect's own failure, via `noticeWithController`. Keys `kind, role, phase`; no Failure.
  - `:961-962`: a child's own architect failed. Keys `kind, role, phase, failure, reason`. Reason: `"<key>'s own architect failed; its notices come to you until it runs again, and the daemon starts it again at <key>'s next phase start"`.
  - `:986-988`: the role of a production check. Reason: `"production check look <n> lost its <role>: <reason>"`.
  - `:997-998`: the role does not work this phase. Reason: `"the <role> does not work <key>'s phase <working>; nothing is held"`.
  - `:1021-1022`: a worker's failure. Keys `kind, role, phase=from, failure`.
- **Other authors' words:** `Failure.Detail`, `Death` and `StartFailure` carry runtime, launcher and provider text (`record/outbox.go:407-427`).
- **Routing:** with Role architect, the walk starts at the parent (`owning_architect.go:31-34`).
- **Tests:** [not grepped].

**worker-stalled**
- **Builders:** `daemon/stallbackstop.go:204-211` (`enqueueStallNotice`), with the kind at `:242`. Keys `kind, role, reason`. A tree architect also gets a `ControllerNotice` copy.
- **Reason:** `stallBackstopRecord` / `stallRecordInWords` (`:261-280`):
  - Facts in it: role, last step time, silence length, bound (`worker_stall_backstop_hours`), turn start time, last call (tool, call id, start, end), the end's words (`stallBackstopEnds` `:476-509`, five variants: `EndRelaunched`, `EndRelaunchParked`, `EndRelaunchPending`, `EndSuspended`, `EndFailed`), the charge clause (`:436-444`) and the stop clause (`:332-342`).
  - Fixed sentence: `"The arguments of a call are never recorded, so they are in no part of this notice."`
- **Tests:** `daemon/stallbackstop_test.go:1023-1182`.

**worker-silent**
- **Builder:** `workflow/silence.go:157`, using `workerSilentRecord` (`:184-187`) and `workerSilentDid` (`:169-178`). The did-variants are: behind the entry, the entry given again, and no entry. Keys `kind, role, phase, reason`.
- **Facts:** issue, role, phase, last step (RFC 3339), silence length, `worker_silence_minutes`, and the entry numbers.
- **Tests:** [not grepped].

**worker-background-stalled**
- **Builder:** `silence.go:214`, using `workerBackgroundRecord` (`:241-244`) and `workerBackgroundWork` (the list of reported kinds).
- **Fixed prose:** `workerBackgroundDid` (`:235`).
- **Tests:** [not grepped].

**held**
- **Builders:**
  - `workflow/engine.go:1017` (a failure): `role, phase=from, failure`, via `noticeWithController`.
  - `engine.go:1285-1286` (an escalation): `phase, reason="escalated", escalation=fact.Reason`. The escalation is **the architect's own words**.
  - `HeldNotice` (`effects.go:979-981`): `role, phase, reason=Hold.Reason`. It is called from `admit/admit.go:2517`.
- **Tests:** [not grepped].

**pr-blocked**
- **Builder:** `workflow/review.go:1117`. Reason: `"Issue reached review_round_cap=%d: %d rounds counted, %d conflict rounds not counted."` The same text is also posted to the Dispatch issue (`:1113`).
- **Tests:** [not grepped].

**pr-merged**
- **Builders:**
  - `workflow/pullrequest.go:391-392`: `"pull request #N merged while <key> was in <phase>, not merging"`.
  - `:418-419`: reason from `waitingForTree` (`:402-405`). It lists the issues the close waits for and ends with the fixed `parkRule` sentence (`engine.go:1472`).
- **Tests:** `workflow/early_merge_test.go:32`, `workflow/merge_close_test.go:635`.

**pr-closed-unmerged**
- **Builder:** `pullrequest.go:524-525`.
- **Tests:** `workflow/early_merge_test.go:34`.

**pull-request-missing**
- **Builder:** `pullrequest.go:670-674`.
- **Facts:** key, handoff commit, repo, branch. It ends with the fixed instruction sentences.
- **Tests:** `workflow/pull_request_missing_test.go:47`, `daemon/issue_reads_test.go:102`.

**message-refused**
- **Builders:**
  - `daemon/outbox.go:437-441` (`messagePostRefused`, called at `:361`). Facts: issue, message length in runes, the first line of the daemon's own message (`refusedPreview` `:444-450`, up to 200 runes), and Dispatch's answer.
  - `statusWriteRefused` (`:492-509`). Its variants are status set or not, and reason posted or not. It is reached through `statusRefusalTold` (`:518-535`), which is called at `:965, :990, :1015, :1117`.
- **Tests:** `daemon/outbox_test.go:908, :970, :990`.

**design-approved**
- **Builders:** `workflow/gate.go:213` (registration) and `gate.go:920`, emitted at `:930` (artifact). Keys `kind, version, actor, ref=stepRef(...)`.
- **Tests:** [not grepped].

**design-changes-requested**
- **Builder:** `gate.go:269` (`tellChanges`). Keys `kind, version, reason, actor, ref`. The reason is **the person's own review reason**.
- **Tests:** [not grepped].

**carried-gate-opened**
- **Builder:** `gate.go:923-924`. Keys `kind, version, actor, ref, reason`. Reason: `"a human approved <key>'s spec document at version N, which opens the gate <carried key> carried; any child waiting on it starts"`.
- **Tests:** [not grepped].

**child-closed**
- **Builders:** `workflow/linger.go:140-144` (when the status is done: reason `"<key> is done"`) and `pullrequest.go:489`.
- **Routing:** starts at the parent.

**child-status** (eight builders; routing starts at the parent)
- `linger.go:144`: `"<key> is <status>"`.
- `effects.go:987-989` (`ChildReenteredNotice`).
- `effects.go:1016-1026` (`ChildStartedNotice`). It names the issues waited on, or the waiting-line clause; the ownership clause is fixed. Called at `engine.go:1754`.
- `accepted_parent.go:32-36` (`ChildUnderAcceptedParentNotice`), called at `engine.go:751`.
- `adopt.go:285-288` (`joinedTheTree`), called at `:228, :338`.
- `adopt.go:196` (`tellReparented`; the reason built at `:188-189` is also posted to the issue).
- **Tests:** `admit/adopt_test.go:103,251,285`, `admit/closed_tree_member_test.go:223`, `workflow/blocked_child_test.go:155,197`, `daemon/blockers_scratch_dispatch_test.go:460,672`, `daemon/sub_architect_live_test.go:238-355`, `daemon/child_adoption_live_test.go:575`.

**children-first**
- **Builder:** `ChildrenFirstNotice`, `workflow/children_first_notice.go:35-61`, called from `admit/admit.go:2210`.
- **List:** each open child shown as `key (status)`.
- **Cycles:** `ChildCycleReason` (`:24-26`).
- **Fixed prose:** a long instruction paragraph with `parkRule`.
- **Tests:** `admit/children_first_test.go:241`.

**child-release-stalled**
- **Builders:**
  - `api/architect.go:705`, through `tellReleaseWait` with reasons from `:681-683` and `:1033-1035`.
  - `api/architect.go:783-784` (attempts run out).
  - `daemon/repositories.go:1433-1435` (lists repositories and children) **[context not read]**.
- **Tests:** `api/wave_release_todo_test.go:307-308,493`, `api/release_stalled_owner_test.go:38`, `api/release_stalled_named_test.go:67`, `daemon/mapping_refresh_test.go:1621`, `daemon/release_waits_owner_test.go:50`.

**child-not-recorded**
- **Builder:** `workflow/repository.go:155-156`.
- **Tests:** `workflow/repository_test.go:104`, `workflow/child_architect_routes_test.go:106`.

**catch-up**
- **Builder:** `QueueCatchUp`, `workflow/catch_up.go:126-166`.
- **Keys:** `kind, role, reason, catch_up, paced_at, paced_from`.
- **Reason variants:**
  - Child: `:137`.
  - Root: `:150`.
  - With acceptances waiting: `:152-154`.
  - With a relaunch sentence in front: `:155-157`.
- **CatchUp fields:** come from `treeCatchUp` (`:299-`) **[body not read]**. `Decided` comes from `ParkedDecisionTask`.
- **Routing:** the architect of its own issue only.

**checks-red**
- **Builder:** `engine.go:1650`, with the reason from `review.go:166`: `"the head conflicts with <base>: GitHub runs no checks on it; merge <base> forward"`.

**review-stuck**
- **Builders:** `review.go:884-885` (with `topic = reviewer role topic`) and `review.go:927-928` (merge state, no topic).
- **Summary:** the `by*` constants (`review.go:616-621`).
- **Reason variants:**
  - `undecidedReason` (`:824-834`), which can append the refusal.
  - Approved other code (`:813-815`).
  - `stuckMergeStateReason` (`:718-721`, using `headChecks` `:225-236`).

**status-reasserted**
- **Builder:** `engine.go:537-538`. Reason: `"session <id> set <key> to <status>, and the daemon set it back to <status>"`.
- **Tests:** `admit/status_write_test.go:97,308,362`.

**session-lost**
- **Builder:** `engine.go:1048-1049`, via `noticeWithController`. The reason includes `strconv.Quote(fact.Detail)`; the same text is also posted to Dispatch (`:1044-1045`).

**session-store-fault**
- **Builder:** `engine.go:1068-1069`. It goes to the **controller only**, as `ControllerNotice(Notice{...})`; the same text is also posted to Dispatch.

### 4. The 11 controller-only kinds (`record/outbox.go:674-684`)
- **tick:** `admit/admit.go:222` with `held, blocked, silent`. `openCapabilities` is set elsewhere (`admit.Admission.ReportCapabilities`) **[site not read]**.
- **triage:** `admit.go:276`, no reason.
- **todo:** `admit.go:316`.
- **slot-free:** `admit.go:389`.
- **unslotted:** `admit.go:2308-2316`. Two reason variants, then a fixed tail.
  - **Tests:** `admit/unslotted_test.go`.
- **awaiting-person:** `workflow/awaiting_reconcile.go:525-526`.
- **parked-decision:** `record/parked_decision.go:127-128`. Keys `decision, root, ref, actor, version, reason`.
  - Callers: `workflow/parked_decision.go:31` (version 0, reason `""`) and `daemon/parked_decision.go:71-72` (the notice's version and reason, so a person's changes reason).
- **update-stalled:** `daemon/podupdate.go:1116-1120`, with `update{round, pod, reasons, waiting}`.
  - Reason variants: `noProcessReason`/`twoPodsReason` (`:657-658`), `operatorHoldReason` (`:663-666`), the runtime refused (`:631`), and podless (`:956-958`). All open with `boundPastReason` (`:651`).
- **provider-errors:** `daemon/workflow.go:1933-1952`. The reason is built at `supervise/providererror.go:162-177` and names the count, model and cause.
  - **Tests:** `supervise/providererror_test.go:573`, `admit/controller_wake_test.go:252`, `daemon/provider_error_retry_live_test.go:487`.
- **tree-stalled:** `admit/stalled.go:310-311`, reason from `stalledTreeReason` (`:832-859`). It has one line per member and fixed closing sentences.
  - **Tests:** `admit/tree_stalled_test.go:347`.
- **architect-stalled:** `admit/architect_stall.go:168-170`. The reason is `arm.Words()` (`record/outbox.go:722-733`) + `": "` + why.
  - The why texts are at `architect_stall.go:234-238` (took nothing), `:368-382` (claim unreachable) and `:411-412` (slow before the gate).
  - **Tests:** `admit/architect_wake_test.go:229`, `daemon/architect_sweep_test.go:176`.

### 5. Role messages (proposed names in bold)
- **`ci-settled`**: `tellChecks`, `workflow/review.go:273-315`.
  - Body: `"Pull request #N: <settledChecks>. Nothing moved on it: decide whether to re-run the checks, wait for them, or fix the code."`
  - `settledChecks` (`:192-210`): `"CI settled at <sha>: "` + `namedConclusions` (`:239-250`, check names quoted).
  - Readers: the owning architect, the phase worker and the implementer.
  - Tests: `workflow/checks_red_test.go:71-457`, `workflow/child_architect_routes_test.go:72`.
- **`design-gate-moved`**: `tellGateMoved`, `workflow/gate.go:964-1015`. Open and closed bodies at `:996-1004`. Readers: every phase worker of the tree and the root architect. Test: `workflow/gate_messages_test.go:198-204`.
- **`review-stuck-reviewer`**: `review.go:919-920`. Body: `"The round you approved is stuck: <reason>."` Tests: `workflow/review_test.go:222,271,383`.
- **`memory-kill`**: `memoryGuardKill`, `engine.go:1092-1117`.
  - Variants: `memoryGuardNote` (`:1155-1159`), `kernelKillNote` (`:1165-1167`) and `kernelProtectedKillNote` (`:1174-1176`).
  - Fixed prose: `memoryKillGoesOn`, `memoryKillHeavyWork` and `kernelProtectedKillEnd`.
  - Tests: `workflow/memory_guard_test.go:55,85,116`, `daemon/memory_guard_test.go:170`.
- **`phase-ended`**: `Engine.phaseEnded`, `engine.go:1677-1683` (conflict only). Text from `PhaseEnded.Message` (`record/outbox.go:1162-1171`). Test: `daemon/phase_ended_test.go:40-62`.
- **`hook-prompt`**: `workflow/hooks.go:99-100`. Text from `HookPrompt.Message` (`outbox.go:1222-1240`). The `Prompt` is the deployment's own words. Tests: `record/outbox_test.go:471-494`, `workflow/hook_firing_test.go:197`.
- **`update-ask`**: `daemon/podupdate.go:1222-1223`, with the text from `supervise.UpdateTask` (`supervise/table.go:285-313`). Tests: `supervise/podupdate_test.go:607-615`, `daemon/podupdate_bound_test.go:712,770`, `daemon/podupdate_one_channel_test.go:82,156`, `daemon/podupdate_ask_test.go:21`.
- **`merge-queue-removal`**: `workflow/pullrequest.go:586`. Text from `MergeQueueRemoval.Message` (`outbox.go:1323-1330`), with an Unread variant. Test: `daemon/merge_queue_removal_test.go:172,223`.

### 6. Tasks
- **Phase assignment.** `task` (`effects.go:931-950`) states: title, key, phase, `Handoff: <word> version N (dispatch handoff-read …)`, handoff commit, tester's verdict, `Pull request: #N`, and `Reason:`.
  - Reason sources: the review body (`review.go:865`), the worker's backward reason (`engine.go:1466`) and the conflict sentence. The restart reason (`engine.go:1395`) **[source not read]**.
  - `ResumePhaseTask` (`:970-972`) is used at `effects.go:568,586`, `catch_up.go:275`, `resume.go:246` and `silence.go:153`.
  - Tests: `workflow/engine_test.go:377`, `handoff_test.go:60`, `accepting_test.go:108`, `restart_phase_test.go:37`, `admit/admit_test.go:278-363`, `admit/finished_root_test.go:452`, `daemon/cutover_merger_repair_test.go:740`, `daemon/handoff_records_live_test.go:425`.
- **`resumeTask`** (`awaiting_reconcile.go:655-785`).
  - Cases by `AnsweredBy`: approval (four sub-cases), changes, version, retraction, closed, ask, reply, turn, decline (Dispatch's words and the person's quoted note), and default.
  - Gate clause: six cases.
  - Lists: still with the person; waiting on you.
  - Called from `ResumeAwaitedRoot` (`:580-604`).
  - Tests: `workflow/awaiting_reconcile_test.go:225-344`, `awaiting_person_test.go:648,695`, `awaiting_decline_test.go:102,119`, `admit/awaiting_gate_test.go`, `admit/awaiting_gate_open_test.go`, `daemon/root_hears_main_paths_test.go:506`.
- **`checkTask`** (`workflow/check.go:139-166`).
  - `wokenBy` (`:174-185`) wakes on merge, bound or named check wake. The named-wake detail is the worker's pending summary, or `"the retry of a look nobody reported: <cause>"` (`:276`). It is called from `startLook` `:91,500,538`.
  - Tests: `workflow/check_task_test.go`, `check_test.go:489`, `merge_close_test.go:75-78`.
- **`ArchitectStartTask`** (`architect_start.go:272-294`).
  - Shapes: `writeCarriedPhase` (`:337-358`), `writeGateOpen` (`:377-383`), `writeGateStanding` (`:364-369`), `writeEarlierGeneration` (`:390-397`) and `writeFreshTree` (`:306-316`).
  - Helpers: `writeGateAsk` (`:405-415`), `gateStanding` (`:424-447`).
  - Labels: `:18-34`.
  - `NarrowRootTask` (`:483-489`) is called at `daemon/workflow.go:886`.
  - Tests: `workflow/architect_start_test.go`, `admit/architect_sweep_test.go`, `admit/architect_entry_test.go`, `daemon/fresh_admission_start_task_test.go:44`.
- **`KeptDecisionsTask`** (`parked_decision.go:117-133`) and **`ParkedDecisionTask`** (`:89-108`). Tests: `admit/parked_decision_test.go:213-623`, `admit/gate_steps_test.go:212`, `workflow/catch_up_test.go:138`.
- **`WakeTask`** (`workflow/wake.go:86-108`). Kinds are checks (`settlementDetail` `:179-193`), role message, agent message, backstop (detail at `:210`) and default. Tests: `workflow/wake_test.go:586-617`, `daemon/idle_release_and_wake_test.go:359-389`.
- **Start parts in `supervise`:**
  - `relaunchSentence`/`RelaunchSentence`: `relaunch.go:21,66-137`, with causes at `endedwork.go:131-171`.
  - `interruptedTask`: `delivery.go:1442-1444`.
  - `abandonedTask`: `:1466-1484` (quotes `Abandoned.Why`).
  - `outOfMemoryTask`: `:1503-1508`.
  - `providerErrorTask`: `providererror.go:151-155`.
  - `Delivery.message`: `delivery.go:293-326`.
  - `queueRelaunch`: `relaunch.go:221-229`.
  - Tests: `supervise/relaunch_test.go`, `endedwork_test.go`, `interrupted_test.go`, `deadprocess_test.go:369`, `daemon/relaunch_sentence_test.go`.
- **`ControllerStartMessage`**: `daemon/controller.go:26`, delivered at `:165`.

### 7. Messages posted to Dispatch, not to an agent
`podWentMessage` (`daemon/podupdate.go:1630-1641`, posted as a `MessagePost` at `:988`; test `daemon/podupdate_bound_test.go:404`), `moveReason`, the session-lost/store-fault posts, the reparent post and the review-round cap post.

### 8. Import graph (from import lines, not `go list`)
- `record` imports `claim`, `phase`, `pods`, `supervise`, `treelifecycle` and `ghrepo` (`record/record.go:14-19`). **It does import `supervise`**, although the `UpdateAsk` comment at `outbox.go:1245` says it does not.
- `supervise` imports neither `record` nor `workflow`.
- `workflow` imports `record`, `supervise`, `intake` and `notify`. `review.go:16` is the only `notify` import.
- `admit` imports `workflow`, `supervise` and `record`.
- `api` imports `record` and `supervise`, and does not import `workflow` or `admit`.
- `daemon` imports all of them.
- `notify` imports no internal package.