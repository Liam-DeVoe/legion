# Progress of INFRA-3686

## texts API (T1 framework)

Package `github.com/sjawhar/legion/daemon/internal/texts` (`packages/daemon/internal/texts`). Imports: the standard library, `internal/claim`, `internal/phase`. The package doc (`doc.go`) states the contract. It holds all 57 kinds, each with its record type, its readers and, for a notice or role message, its urgency rule. The section "Kinds and records (T1)" below is the table of the kinds and the records.

Types and lookups (`entry.go`):

- `type Kind string`: the name of a kind on the one list.
- `type Form string`: the form of a kind's text.
- `Task` (`Form = "task"`): a prompt, including the parts a start puts before a task.
- `Notice` (`Form = "notice"`): a notice to the architect that owns the issue.
- `ControllerNotice` (`Form = "controller-notice"`): a notice to the controller.
- `RoleMessage` (`Form = "role-message"`): a message the daemon publishes to a role.
- `type Entry struct { Kind Kind; Form Form; Record any; Readers []claim.Role; Urgency func(reader claim.Role, v Variant) Urgency }`: one kind on the list. `Record` is the zero value of the kind's record type. `Readers` are the roles that read the kind (shared slice, do not change it). `Urgency` is nil for a task. A nil rule on a notice or role message gives `Med`.
- `func Lookup(kind Kind) (Entry, bool)`: the entry of a kind, false off the list (map index built from `list` at init).
- `func Entries() []Entry`: a copy of the whole list, in list order.
- `func IsNotice(kind Kind) bool`: on the list as `Notice` or `ControllerNotice`.
- `func IsControllerNotice(kind Kind) bool`: on the list as `ControllerNotice`.
- `func IsRoleMessage(kind Kind) bool`: on the list as `RoleMessage`.
- `func IsTask(kind Kind) bool`: on the list as `Task`.

Urgency (`urgency.go`):

- `type Urgency string`: the envelope's urgency.
- `Low` (`Urgency = "low"`): the reader can act at its next turn.
- `Med` (`Urgency = "med"`): every other notice and role message.
- `type Variant struct { Role claim.Role "role"; Phase phase.Phase "phase"; To phase.Phase "to"; Change string "change"; Hold string "hold" }` (all omitempty): the row's facts that a rule reads. The json names are the payload keys, so `json.Unmarshal(payload, &v)` gives a notice's variant. (T1 kinds round replaced `Verdict`, `Reason`, `ChildStatus`, `Held`, `AllPassed`: no rule reads verdict, reason or all-passed; `Change`/`Hold` are the child-status and held words.)
- `func UrgencyOf(kind Kind, reader claim.Role, v Variant) Urgency`: `""` for a task, the entry's rule otherwise, `Med` for a nil rule or a kind off the list.

Kind constants (`kinds.go`; constant name = CamelCase of the kind, `PR` for `pr-`):

- `PhaseFinished` (`phase-finished`, Notice)
- `WorkerDied` (`worker-died`, Notice)
- `WorkerStalled` (`worker-stalled`, Notice)
- `WorkerSilent` (`worker-silent`, Notice)
- `WorkerBackgroundStalled` (`worker-background-stalled`, Notice)
- `Held` (`held`, Notice)
- `PRBlocked` (`pr-blocked`, Notice)
- `PRMerged` (`pr-merged`, Notice)
- `PRClosedUnmerged` (`pr-closed-unmerged`, Notice)
- `PullRequestMissing` (`pull-request-missing`, Notice)
- `MessageRefused` (`message-refused`, Notice)
- `DesignApproved` (`design-approved`, Notice)
- `DesignChangesRequested` (`design-changes-requested`, Notice)
- `CarriedGateOpened` (`carried-gate-opened`, Notice)
- `ChildClosed` (`child-closed`, Notice)
- `ChildStatus` (`child-status`, Notice)
- `ChildrenFirst` (`children-first`, Notice)
- `ChildReleaseStalled` (`child-release-stalled`, Notice)
- `ChildNotRecorded` (`child-not-recorded`, Notice)
- `CatchUp` (`catch-up`, Notice)
- `ChecksRed` (`checks-red`, Notice)
- `ReviewStuck` (`review-stuck`, Notice)
- `StatusReasserted` (`status-reasserted`, Notice)
- `SessionLost` (`session-lost`, Notice)
- `SessionStoreFault` (`session-store-fault`, Notice)
- `Triage` (`triage`, ControllerNotice)
- `SlotFree` (`slot-free`, ControllerNotice)
- `Todo` (`todo`, ControllerNotice)
- `Tick` (`tick`, ControllerNotice)
- `Unslotted` (`unslotted`, ControllerNotice)
- `AwaitingPerson` (`awaiting-person`, ControllerNotice)
- `ParkedDecision` (`parked-decision`, ControllerNotice)
- `UpdateStalled` (`update-stalled`, ControllerNotice)
- `ProviderErrors` (`provider-errors`, ControllerNotice)
- `TreeStalled` (`tree-stalled`, ControllerNotice)
- `ArchitectStalled` (`architect-stalled`, ControllerNotice)

Words (`words.go`):

- `OneAct = 4000`: UTF-16 bound of the words of one act.
- `EachOfSeveral = 1000`: UTF-16 bound of each act where a text carries several.
- `Summary = 1000`: UTF-16 bound of a worker's summary.
- `type Words struct { Text string; By string; At time.Time; Field string; Rest int; More bool }`: another author's words for a block (no json tags; the reference is a field of the record, not of Words).
- `type WordsMeta struct { By string "by,omitempty"; At time.Time "at,omitzero"; Rest int "rest,omitempty"; More bool "more,omitempty" }`: the entry of a notice payload's `words` object, keyed by field path.
- `func Bound(text string, limit int) (head string, rest int)`: cut to `limit` UTF-16 units; a pair that would cross the limit is left out whole; `rest` is the UTF-16 units cut.
- `func Carry(text string, limit int, more bool) Words`: Bound, then `More: true` and no `Rest` where the event marked its body cut (or a `payload_omitted` stand-in, with `text` empty); otherwise `Rest`. The caller sets `By` and `At`.
- `func Block(name string, w Words) string`: `<tag attrs>\n<words verbatim>\n</tag>`; tag is `name`, or the first `name-N` (N from 1) whose `</name-N` the words do not hold, where they hold `</name`; attributes in the order `by`, `at`, `field`, `rest`, `more`, each only where set; values escape `&`, `<`, `>`, `"` and control characters below U+0020 as XML character references (`&amp;` `&lt;` `&gt;` `&quot;` `&#10;`).
- `func WordsPaths(record any) []string`: the path of each words field of `record` that is set (a non-empty string tagged `texts:"words"`, or a `Words`/`*Words` that is not empty), in declaration order: json names joined by dots, list index from 0 (`summary`, `kept[1].answer`, `catch_up.kept.decisions[1].answer`). Untagged embedded structs are flattened. The `words` object of a notice or role-message payload is keyed by these paths.

Render (`render.go`, encoder in `toon.go`):

- `func Render(record any) (string, error)`: a struct or a pointer to one. Lines: `Lead() string` first where the record has it (skipped when empty); the json-tagged fields in declaration order in TOON 4.1.1 form; each of `Kept() []string` on its own line; the blocks of its `Words`/`*Words` fields in declaration order. No trailing new line. Either receiver works for `Lead` and `Kept`.

Rules a record type must follow (from `render.go` and `toon.go`):

- A field needs a json tag with a name; an untagged or `json:"-"` field is skipped. An embedded struct with no tag is flattened, as encoding/json does; its type must be exported (an unexported embedded struct is refused, since reflect cannot read a time or Words through it).
- Values: string (and named string types: `Kind`, `claim.Role`, `phase.Phase`), bool, any int or uint within ±(2^53−1), `time.Time` (RFC 3339 UTC, `time.RFC3339Nano`, so whole seconds print as `...:10Z`, the form Go's JSON gives a notice payload), a pointer to one (nil prints `null`), a slice or array of those, or a slice of flat structs (a table). A nil or empty slice prints `key: []`. omitempty and omitzero follow encoding/json.
- Refused with an error: a map, a nested struct, a float, a list of lists, a list of bytes, a list of `Words`, rows that are not uniform (a row that omits a key by omitempty that another row has), rows with a list or struct inside, rows with no field, a string that is not valid UTF-8.
- A `Words` or `*Words` field inside a table row is left out of the columns and drawn as a block named by its json name with `field="<list>[i].<name>"`, the index counting from 0 (`kept[1].answer` is the second row), as a payload path does. Zero `Words` (no text, no rest, no more) and a nil `*Words` draw no block.

Tests in `texts`: `TestTheEncoderQuotesEachHostileValueAsToonDoes`, `TestTheEncoderWritesAListAndATableAsToonDoes`, `TestTheEncoderRefusesAMapOfObjectsAndRowsThatDiffer`, `TestTheBoundCountsUTF16UnitsAndSplitsNoPair`, `TestABlockTakesTheFirstNameItsWordsCannotClose`, `TestRenderWritesTheLeadTheFieldsTheKeptSentencesAndTheBlocks`, `TestTheKindsFileHoldsKindNamesAlone`. The new test files are `kinds_test.go`, `render_test.go`, `toon_test.go` and `words_test.go`. The repository's AGENTS.md asks for `.github/scripts/check-ci-filters.sh write` after a new `_test.go` file. That command writes `.github/workflows/ci.yaml`, which was out of this round's scope, so it was not run.

## Kinds and records (T1)

State: DONE. Every one of the 57 kinds of `kinds.go` has its record (`Record: XRecord{}`), its readers and, for a notice or a role message, its urgency rule (`rules.go`; a kind with no rule is Med). `cd packages/daemon && go test -count=1 ./internal/texts && go vet ./internal/texts && gofmt -l internal/texts` pass, gofmt prints nothing, and `go list -deps ./internal/texts | grep sjawhar` names only `texts`, `claim` and `phase`.

Files: `records.go` (heads, `CheckConclusion{check,conclusion}`, `RelaunchFacts`, helpers `PullRequestRef(repo,n)` = `owner/name#n`, `ListPassed(repo,n)` = `gh pr checks <n> --repo <owner/name>`, `EndsRound(head)` = `APPROVE of head <sha>`, `ShortDigest(image)` = first 12 hex of the digest, the reference itself where it carries no digest), `records_task.go`, `records_start.go`, `records_notice.go`, `records_controller.go`, `records_role.go`; `words.go` gained `WordsPaths`; `records_test.go` holds the tests. Heads: `TaskHead{kind, issue}`, `NoticeHead{kind, issue, resends,omitempty}`, `RoleHead{kind, issue}`. A task holds another author's words as `Words`/`*Words` (blocks); a notice or role message holds them as a `string` tagged `texts:"words"` and declares `Words map[string]WordsMeta "words,omitempty"`.

Exported vocabularies (string constants; builders use them, never retype them): root-resume acts `ActApproved` … `ActDeclined`, `ActActed`; `WokenByMerge|Bound|Named|Retry` (`merge bound named retry`); `ShapeFreshTree|CarriedPhase|GateStanding|GateOpen|EarlierGeneration`; `GateUnregistered|Registered|Approved|ChangesRequested|Unanswered`; `StopStopped|Unconfirmed|NotMade`; `SentOwedEntry|PhaseTask`; `RefusedMessage|Status`; `StaysOwnOperator|Ran|CarriesRun|HandedOver`; `StallUnrecordedAncestor|UnrecordedBlocker|AttemptsSpent|RecoveredUnrecorded`; `CauseUndecided|ApprovedOtherCode|MergeState`; `StuckByCompletion|Answer|OtherReview|Push|MergeState|Merge` (review-stuck summary words, copied from `workflow/review.go` `by*`); `HoldsNoProcess|TwoPods|Operator|RuntimeRefused|NoPod`; `GateOpened|GateClosed`, `MergeHeld|MergeReleased`; `KillGuard|Kernel|KernelProtected`; `UpdateAnswerOp` (`ready_for_update`), `UpdateRestartsIn` (`same_session`); `OutOfMemoryReason` (`OOMKilled`); `DesignApprovedWake`; `ControllerStartMessage`. Variant words (rules.go): `ChangeTodo|Started|Joined|Left|BeneathAccepted|Reparented` (`todo started joined left beneath-accepted reparented`), `HoldFailure|Escalation|Carried` (`failure escalation carried`). Constructors: `NewUpdateAsk(issue, round, pod, images, settings, endsAt)` (sets the INFRA-2981 R4 facts), `NewOutOfMemoryNote(issue)`.

Readers in the table: arch = the architect that owns the issue; ctl = the controller; workers = planner, implementer, tester, reviewer, merger; pod = arch + workers; every = pod + ctl. Urgency: — for a task; Med where the kind has no rule.

| Kind | Form | Record | Readers | Builder on main | Facts main writes → field | Words fields | R8 kept | Urgency |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `phase-assignment` | task | `PhaseAssignmentRecord` | workers | `workflow/effects.go:task`, `continueLine`; reasons from `engine.go:transition` (backward), `review.go:settleRound` (review body), `review.go:mergeability` (conflict), `engine.go:retryPhase` (retry, stuck round) | title→`title`; key→`issue`; phase→`phase`; `Handoff: <word> version N`→`handoff_phase`, `handoff_version`; handoff commit→`handoff_commit`; tester's verdict→`tester_verdict`; `Pull request: #N`→`pull_request` (`owner/name#n`); backward move→`moved_back_from`; rejection→`reviewed_head`; conflict reason→`conflicts_with` (base), `head`; retry→`retried` (architect or operator); stuck round→`stuck_round` (a Cause word), `ends_round` | `reason` (backward words by the role, or the architect's retry words), `body` (review body by its author; a person's at OneAct) | — | — |
| `phase-resume` | task | `PhaseResumeRecord` | workers | `effects.go:ResumePhaseTask` | title→`title`; key→`issue`; phase→`phase` | — | — | — |
| `root-resume` | task | `RootResumeRecord` | arch | `workflow/awaiting_reconcile.go:resumeTask` | `Awaiting.AnsweredBy`→`act`; actor, time→`by`, `at`; ask id→`ask` (`dispatch://K/ask/<id>`); step's document→`document` (stepRef link); detail version→`version`; `gateOffAnswer`→`gate_off_at_answer`; closer (`AnsweredByClosed`)→`closed_by`; options chosen→`options`; decline's Dispatch sentences→`dispatch_words`; gate latest version→`document_version`; gate open→`gate_open`; armed→`gate_armed`; told changes version→`later_changes_version`; still with the person→`with_person` (ask links), `approval_request` (version); waiting on you→`waiting_on_you` (ask links); request thread open→`request_open` | `answer`, `reply`, `reason` (changes, OneAct), `note` (decline, OneAct), `later_reason` | `this resume is your design-approved wake` (`Kept()` where `gate_open`) | — |
| `check-look` | task | `CheckLookRecord` | implementer, tester | `workflow/check.go:checkTask`, `wokenBy` | key, title→`Lead()`; looks→`look`; cause→`woken_by`; retry cause→`retry_of`; last→`last`; repo, number→`pull_request`; merge SHA→`merge_commit` (absent where unknown); bound→`bound_days`; last look time→`last_look` | `summary` (the pending summary, by the checking role) | lead `Production check of <KEY>: <title>.` | — |
| `architect-start` | task | `ArchitectStartRecord` | arch | `workflow/architect_start.go:ArchitectStartTask`, `writeFreshTree`, `writeCarriedPhase`, `writeGateStanding`, `writeGateOpen`, `writeEarlierGeneration`, `writeGateAsk`, `gateStanding` | shape→`shape`; generation→`generation`; slot→`holds_slot`; root phase→`phase`; `GateCarried`→`gate_carried`; `PhaseFinished`→`merged`; `RoleFor(phase)`→`daemon_starts_worker`; `MembersRun`→`members_run`; gate step by step→`gates[]{step,document,version,state}`; gated steps→`gated`; first act→`first_act` (words); decomposition→`decompose`; closing sentence→`first_phase_waits_for` (words), `nothing_else_starts_it` | — | INFRA-2614 R3 facts and first act as fields | — |
| `kept-decisions` | task | `KeptDecisionsRecord` | arch | `workflow/parked_decision.go:KeptDecisionsTask`, `ParkedDecisionTask`; `architect_start.go:NarrowRootTask` | each decision ref, issue, kind, actor, time→`kept[]{ref,issue,decision,by,at,options}`; kept approval→`approval_step`, `approved`, `latest` | `kept[i].answer` (EachOfSeveral) | — | — |
| `pod-wake` | task | `PodWakeRecord` | pod | `workflow/wake.go:WakeTask`, `settlementDetail`, `podBackstopDue`, `listenerWake` | wake kind→`wake` (pods words); settlement→`pull_request`, `head`, `phase`, `not_passed[]{check,conclusion}`, `passed`, `list_passed`; role topic→`topic`; agent session→`session`; backstop role→`armed_for`; other→`detail` | — | INFRA-2846 R7 facts as fields: `kept_for`, `kept_card_field`, `kept_card_may_come_first`, `sender_told_of_drop`, `not_kept_with`, `sender_sends_again` | — |
| `controller-start` | task | `ControllerStartRecord` | ctl | `daemon/controller.go:ControllerStartMessage` | none | — | the whole message (`Kept()`, `texts.ControllerStartMessage`) | — |
| `relaunch-note` | task | `RelaunchNoteRecord` | every | `supervise/relaunch.go:relaunchSentence`, `RelaunchSentence` | `EndedWork.Cause`→`cause`; `Turn`→`in_turn`; `TaskTurn`→`task_turn`; `Kinds`→`stopped`; `Background`→`background`; `Reported`→`reported`; in front of a task→`task` | — | — | — |
| `cut-turn-note` | task | `CutTurnNoteRecord` | every | `supervise/delivery.go:interruptedTask` | cause (inside the kept note; `Cause json:"-"`) | — | the whole note word for word (`Kept()`, INFRA-3353 R2) | — |
| `abandoned-session-note` | task | `AbandonedSessionNoteRecord` | every | `supervise/delivery.go:abandonedTask` | `Abandoned.Session`→`session`; `Abandoned.Why`→`why`; issue branch→`branch` (none for the controller) | — | — | — |
| `out-of-memory-note` | task | `OutOfMemoryNoteRecord` | pod | `supervise/delivery.go:outOfMemoryTask` | `OOMKilled`→`reason` (`NewOutOfMemoryNote`) | — | — | — |
| `provider-error-note` | task | `ProviderErrorNoteRecord` | every | `supervise/providererror.go:providerErrorTask`, `providerErrorCause` | HTTP status→`http_status` | — | — | — |
| `phase-finished` | notice | `PhaseFinishedRecord` | arch | `workflow/engine.go:noticeFor` | `RoleFor(from)`→`role`; from→`phase`; to→`to`; summary→`summary`; verdict→`verdict`; handoff→`handoff_phase`, `handoff_version`, `handoff_commit`; backward words→`reason` | `summary` (Summary bound), `reason` (by the role) | — | `phaseFinishedUrgency` |
| `worker-died` | notice | `WorkerDiedRecord` | arch, ctl | `engine.go:claimFailed` | role→`role`; phase→`phase`; working phase→`working`; check look→`look`; `noticeFailure`→`failure` | — | — | Med |
| `worker-stalled` | notice | `WorkerStalledRecord` | arch, ctl | `daemon/stallbackstop.go:enqueueStallNotice`, `stallRecordInWords`, `stallStopClause`, `stallChargeClause` | role→`role`; last step→`last_step`; silence→`silent_for`; bound→`bound`; turn start→`turn_started`; last call→`call_tool`, `call_id`, `call_started`, `call_ended`; end→`end`; wake due→`wake_due`; stop clause→`stop`; launch failures→`launch_failures` | — | — | Med |
| `worker-silent` | notice | `WorkerSilentRecord` | arch | `workflow/silence.go:repairSilence`, `workerSilentRecord`, `workerSilentDid` | role, phase→`role`, `phase`; last step→`last_step`; silence→`silent_for`; bound→`bound`; behind→`sent`; entry→`entry`; given→`given` | — | — | Med |
| `worker-background-stalled` | notice | `WorkerBackgroundStalledRecord` | arch | `silence.go:workerBackgroundStalled`, `workerBackgroundRecord` | role, phase; kinds→`work`; reported→`reported_at`; stood→`stood_for`; bound→`bound` | — | — | Med |
| `held` | notice | `HeldRecord` | arch, ctl | `engine.go:claimFailed`, `retryOrEscalate`; `effects.go:HeldNotice` | role, phase; variant→`hold`; `escalated`/hold reason→`reason`; architect's words→`escalation`; failure→`failure` | `escalation` (by the architect) | — | `heldUrgency` |
| `pr-blocked` | notice | `PRBlockedRecord` | arch | `workflow/review.go:recordRound` | cap→`cap`; counted→`counted`; conflict rounds→`conflict_rounds` | — | — | Med |
| `pr-merged` | notice | `PRMergedRecord` | arch | `workflow/pullrequest.go:merged`, `closeMergedTelling`, `waitingForTree` | number→`pull_request`; phase→`phase`; waiting issues→`waiting_for` | — | — | `lowToArchitect` |
| `pr-closed-unmerged` | notice | `PRClosedUnmergedRecord` | arch | `pullrequest.go:closed` | number→`pull_request`; phase→`phase` | — | — | Med |
| `pull-request-missing` | notice | `PullRequestMissingRecord` | arch | `pullrequest.go:pullRequestMissing` | handoff commit→`handoff_commit`; repo→`repo`; branch→`branch` | — | — | Med |
| `message-refused` | notice | `MessageRefusedRecord` | arch | `daemon/outbox.go:messagePostRefused`, `statusWriteRefused` | message or status→`refused`; Dispatch's answer→`dispatch_answer`; length→`length`; first line→`begins`; status→`status`; status set→`status_set`; reason posted→`reason_posted` | — | — | Med |
| `design-approved` | notice | `DesignApprovedRecord` | arch | `workflow/gate.go:gateRegistered`, `applyArtifact` | version→`version`; reviewer→`actor`; `stepRef`→`ref`; time→`at` | — | — | Med |
| `design-changes-requested` | notice | `DesignChangesRequestedRecord` | arch | `gate.go:tellChanges` | version, actor, ref, at; reason→`reason` | `reason` (OneAct) | — | Med |
| `carried-gate-opened` | notice | `CarriedGateOpenedRecord` | arch | `gate.go:applyArtifact` | version, actor, `ref` (names the approved issue), at | — | — | Med |
| `child-closed` | notice | `ChildClosedRecord` | arch | `workflow/linger.go` (leave), `pullrequest.go:closeMerged` | child key→`issue` | — | — | Med |
| `child-status` | notice | `ChildStatusRecord` | arch | `linger.go`; `effects.go:ChildReenteredNotice`, `ChildStartedNotice`; `accepted_parent.go:ChildUnderAcceptedParentNotice`; `adopt.go:joinedTheTree`, `tellReparented` | variant→`change`; root→`tree`; waited→`waited_on`; from tree→`from_tree`; status→`status`; ancestor→`accepted_ancestor`; reparented root→`child`; Dispatch parent→`parent`; `RanAsRoot`→`stays_own` | — | — | `childStatusUrgency` |
| `children-first` | notice | `ChildrenFirstRecord` | arch | `workflow/children_first_notice.go:ChildrenFirstNotice`, `ChildCycleReason` | parent→`issue`; open children→`children[]{key,status,released,cycle}` | — | — | Med |
| `child-release-stalled` | notice | `ChildReleaseStalledRecord` | arch | `api/architect.go:tellReleaseWait`, release retry (:783); `daemon/repositories.go` (recovery, :1433) | why→`stall`; child→`child`; parent→`parent`; unrecorded ancestor→`ancestor`; blocker→`blocker`, `blocker_status`; repo→`repo`; attempts→`attempts`; repos→`repos`; still unrecorded→`unrecorded[]{child,parent,parent_recorded}` | — | — | Med |
| `child-not-recorded` | notice | `ChildNotRecordedRecord` | arch | `workflow/repository.go:refuseChild` | key→`child`; resolved repo→`resolved`; refusal→`refusal` | — | — | Med |
| `catch-up` | notice | `CatchUpRecord` | arch | `workflow/catch_up.go:QueueCatchUp`, `treeCatchUp`, `awaitingAcceptance`; `parked_decision.go:ParkedDecisionTask` | generation, launch, handed over, carried, gates→`catch_up.*`; issues not done→`catch_up.issues`; done→`catch_up.done` (keys); unreleased children→`catch_up.unreleased`; waiting for acceptance→`catch_up.accepting`; kept decisions→`catch_up.kept`; relaunch sentence→`relaunch` | `catch_up.kept.decisions[i].answer` (EachOfSeveral) | — | Med |
| `checks-red` | notice | `ChecksRedRecord` | arch | `engine.go:noticeFor` (conflict), `review.go:mergeability` | from→`phase`; base→`conflicts_with`; head→`head`; number→`pull_request` | — | — | `lowToArchitect` |
| `review-stuck` | notice | `ReviewStuckRecord` | arch | `review.go:settleRound`, `tellStuckMergeState`, `undecidedReason`, `stuckMergeStateReason`, `headChecks` | role, phase; `by`→`summary`; reviewer topic→`topic`; cause→`cause`; number→`pull_request`; head→`head`; approved head→`approved_head`; `APPROVE of head`→`ends_round`; refusal→`set_aside`; merge state→`merge_state`; head checks→`not_passed`, `passed`, `list_passed` | — | summary words (`StuckBy*`); `APPROVE of head <sha>` (`EndsRound`) | Med |
| `status-reasserted` | notice | `StatusReassertedRecord` | arch | `engine.go:sessionStatusWrite` | session→`session`; status written→`wrote`; status set back→`status` | — | — | Med |
| `session-lost` | notice | `SessionLostRecord` | arch, ctl | `engine.go:sessionLost` | role, phase; detail→`detail` | — | — | Med |
| `session-store-fault` | notice | `SessionStoreFaultRecord` | ctl | `engine.go:sessionStoreFault` | role, phase; detail→`detail`; every minute→`retry_every` | — | — | Med |
| `triage` | controller-notice | `TriageRecord` | ctl | `admit/admit.go:applyFact` (:276) | issue | — | — | Med |
| `slot-free` | controller-notice | `SlotFreeRecord` | ctl | `admit.go:wakeForFreeSlot` | issue | — | — | Med |
| `todo` | controller-notice | `TodoRecord` | ctl | `admit.go:applyFact` (:316) | issue | — | — | Med |
| `tick` | controller-notice | `TickRecord` | ctl | `admit.go:applyFact` (:222), `Admission.ReportCapabilities` | project→`issue`; `openCapabilities`, `blocked`, `held`, `silent` whole (main's shapes) | — | — | Med |
| `unslotted` | controller-notice | `UnslottedRecord` | ctl | `admit.go:releaseUnslotted` | status→`status`; phase→`phase`; why→`unresumable` | — | — | Med |
| `awaiting-person` | controller-notice | `AwaitingPersonRecord` | ctl | `workflow/awaiting_reconcile.go:enterAwaiting` | ask→`ask` (link); spec version→`document` (link), `version` | — | — | `lowToController` |
| `parked-decision` | controller-notice | `ParkedDecisionRecord` | ctl | `record/parked_decision.go:RelayParkedDecision`; `daemon/parked_decision.go` | decision, root, ref, actor→same keys; time→`at`; version→`version`; changes reason→`reason`; answer→`answer`, `options` | `reason`, `answer` (OneAct) | — | Med |
| `update-stalled` | controller-notice | `UpdateStalledRecord` | ctl | `daemon/podupdate.go:tellUpdateStalled`, `tellLateRound`, `tellPodlessRound` | reason variant→`holds`; runtime refusal→`refusal`; round→`update{round,pod,images,settings,reasons,waiting}` | — | — | Med |
| `provider-errors` | controller-notice | `ProviderErrorsRecord` | ctl | `daemon/workflow.go:providerErrorRunReached`; `supervise/providererror.go:tellControllerOfProviderErrors` | role→`role`; run→`turns`; model→`model`; status→`http_status`; `promptRetryMax`→`retry_every` | — | — | Med |
| `tree-stalled` | controller-notice | `TreeStalledRecord` | ctl | `admit/stalled.go:stalledTreeReason` | quiet hours→`quiet_for`; members→`members[]{key,phase,turn,pull_request,pull_request_state}` | — | — | Med |
| `architect-stalled` | controller-notice | `ArchitectStalledRecord` | ctl | `admit/architect_stall.go:architectStalls` and its three arms | role→`role`; arm→`arm`; phase→`phase`; entry→`entry`; first queued→`queued_at`; let-go→`interrupted`; counted since→`since`; claim state→`claim_state`; relaunch failing→`relaunch_failing`; start written→`start_written`; awaiting person→`awaits_person`; bound→`bound` | — | — | Med |
| `ci-settled` | role-message | `CISettledRecord` | pod (arch, phase worker, implementer) | `workflow/review.go:tellChecks`, `settledChecks`, `namedConclusions` | number→`pull_request`; checked head→`head`; phase→`phase`; failed and cancelled→`not_passed[]`; succeeded→`passed`; `list_passed` | — | — | `ciSettledUrgency` |
| `gate-moved` | role-message | `GateMovedRecord` | pod | `gate.go:tellGateMoved` | open→`gate`; step→`step`; document→`document` (link), `artifact`; version→`version`; merge held or released→`merge` | — | — | Med |
| `approved-round-stuck` | role-message | `ApprovedRoundStuckRecord` | reviewer | `review.go:tellStuckMergeState` | number→`pull_request`; head→`head`; merge state→`merge_state`; head checks→`not_passed`, `passed`, `list_passed` | — | — | Med |
| `memory-kill` | role-message | `MemoryKillRecord` | pod | `engine.go:memoryGuardKill`, `memoryGuardNote`, `kernelKillNote`, `kernelProtectedKillNote` | cause→`cause`; executable→`process`; pid→`pid`; size→`size`; reading or counter→`pod_memory`; limit→`limit` | — | — | Med |
| `phase-ended` | role-message | `PhaseEndedRecord` | workers | `engine.go:phaseEnded`; `record/outbox.go:PhaseEnded.Message` | from→`from`; to→`to`; task follows→`task_follows`; conflict reason→`conflicts_with`, `head`, `pull_request` | — | — | Med |
| `hook-prompt` | role-message | `HookPromptRecord` | workers | `workflow/hooks.go:fireHook`; `record/outbox.go:HookPrompt.Message` | hook, issue, from, to (first line, `json:"-"`); prompt (as written); count→`count`; counts→`counts[]{move,count}`; pull request→`pull_request` | the hook's words, as written in the text | first line `Legion hook <hook> on <KEY>: work went back from <from> to <to>.` and the order line, words, facts (`Text()`) | Med |
| `update-ask` | role-message | `UpdateAskRecord` | pod | `supervise/table.go:UpdateTask`; `daemon/podupdate.go` | round→`round`; pod→`pod`; drift→`images` (`ShortDigest`), `settings` (keys), `reasons` (an older round's text); ends at→`ends_at` | — | INFRA-2981 R4 facts as fields (`NewUpdateAsk`): `push_to`, `answer`, `every_answer_brings_it_forward`, `no_answer_delays_it`, `kept_by_push_only`, `goes_with_pod`, `restart`, `next_task_held` | Med |
| `merge-queue-removal` | role-message | `MergeQueueRemovalRecord` | merger | `workflow/pullrequest.go:mergeQueueReadDone`; `record/outbox.go:MergeQueueRemoval.Message` | repo, number→`pull_request`; head→`head`; unread→`unread` | — | — | Med |

Tests (`records_test.go`): `TestEveryEntryHasARecord`, `TestEveryTaskRecordRendersItsKindAndIssueFirst`, `TestEveryNoticeAndRoleMessageRecordMarshalsAnObject`, `TestEachNoticeKeyHasOneShape` (it logs each new key, which is the list below), `TestTheWordsObjectNamesEachWordsFieldByItsPath`, `TestTheSentencesR8KeepsRenderWordForWord`, `TestTheHelpersWriteTheReferencesInOneForm`. The samples set every field by reflection, with values that differ between the two samples. `.github/scripts/check-ci-filters.sh write` was not run for the new test file (it writes `.github/workflows/ci.yaml`, outside T1's files).

Choices against the designs above, for the tasks that build the texts:

- `abandoned-session-note` has no `handoffs` list: the handoff words are the same in every note (R2), so the step that reads the handoff records goes to the prompt (T9).
- No record declares `skipped` (plan decision 14: INFRA-4135 adds it).
- `DesignApprovedWake` is a `Kept()` line of `root-resume` alone. Main's `ArchitectStartTask` never writes it, and the prompts name it for the resume, so `architect-start` keeps no such line.
- A notice whose `role` on main named its reader (the architect: `pr-*`, `pull-request-missing`, `message-refused`, `child-*`, `catch-up`, `checks-red`, `status-reasserted`) declares no `role`. `catch-up` also declares no `reason`; `catch_up.decided` stays a key for rows an older daemon wrote.
- `child-status` has `child` (the reparented root, since `tellReparented` queues its notice on the tree root) beside the design's keys.
- `ci-settled`, `approved-round-stuck`: `not_passed` is omitempty, so a head with no failed check has no empty table.
- T2: the wire order is `record.Notice`'s field order, not the texts record's. Keep `kind`, `role`, `phase` first and adjacent there: `scripts/e2e/stage3-devbox-workflow.sh:963` matches them on consecutive card lines. `issue` goes after them.

### New notice keys for T2

Every key a notice or controller-notice record declares that main's `record.Notice` lacks, with its Go type and the records that declare it (`TestEachNoticeKeyHasOneShape` logs the same list). Each goes on `record.Notice` as an `omitempty` (or `omitzero` for a time) field of that type.

- `issue` string: every notice and controller-notice record.
- `to` phase.Phase: phase-finished.
- `handoff_phase` string, `handoff_version` int: phase-finished. `handoff_commit` string: phase-finished, pull-request-missing.
- `words` map[string]texts.WordsMeta: phase-finished, held, design-changes-requested, catch-up, parked-decision.
- `working` phase.Phase, `look` int: worker-died.
- `last_step` time.Time, `silent_for` string: worker-stalled, worker-silent. `bound` string: worker-stalled, worker-silent, worker-background-stalled, architect-stalled.
- `turn_started`, `call_started`, `call_ended`, `wake_due` time.Time; `call_tool`, `call_id`, `end`, `stop` string; `launch_failures` int: worker-stalled.
- `sent` string, `given` int64: worker-silent. `entry` int64: worker-silent, architect-stalled.
- `work` []string, `reported_at` time.Time, `stood_for` string: worker-background-stalled.
- `hold` string: held.
- `cap`, `counted`, `conflict_rounds` int: pr-blocked.
- `pull_request` string: pr-merged, pr-closed-unmerged, checks-red, review-stuck. `waiting_for` []string: pr-merged.
- `repo` string: pull-request-missing, child-release-stalled. `branch` string: pull-request-missing.
- `refused`, `dispatch_answer`, `begins` string; `length` int; `status_set`, `reason_posted` *bool: message-refused. `status` string: message-refused, child-status, status-reasserted, unslotted.
- `at` time.Time: design-approved, design-changes-requested, carried-gate-opened, parked-decision.
- `change`, `tree`, `from_tree`, `accepted_ancestor`, `stays_own` string; `waited_on` []string: child-status. `child` string: child-status, child-release-stalled, child-not-recorded. `parent` string: child-status, child-release-stalled.
- `children` []texts.OpenChildFacts: children-first.
- `stall`, `ancestor`, `blocker`, `blocker_status` string; `attempts` int; `repos` []string; `unrecorded` []texts.UnrecordedChild: child-release-stalled.
- `resolved` string: child-not-recorded. `refusal` string: child-not-recorded, update-stalled.
- `relaunch` *texts.RelaunchFacts: catch-up.
- `conflicts_with` string: checks-red. `head` string: checks-red, review-stuck.
- `cause`, `approved_head`, `ends_round`, `set_aside`, `merge_state`, `list_passed` string; `not_passed` []texts.CheckConclusion; `passed` *int: review-stuck.
- `session`, `wrote` string: status-reasserted.
- `detail` string: session-lost, session-store-fault. `retry_every` string: session-store-fault, provider-errors.
- `unresumable` string: unslotted.
- `ask`, `document` string: awaiting-person.
- `answer` string, `options` []string: parked-decision.
- `holds` string: update-stalled.
- `turns` int, `model` string, `http_status` int: provider-errors.
- `quiet_for` string, `members` []texts.TreeMember: tree-stalled.
- `arm`, `claim_state` string; `queued_at`, `since` time.Time; `interrupted`, `relaunch_failing`, `start_written`, `awaits_person` bool: architect-stalled.

Main's keys that the records keep, with main's shape and omitempty: `kind`, `role`, `phase`, `summary`, `verdict`, `version`, `reason`, `topic`, `failure`, `escalation`, `resends`, `catch_up`, `openCapabilities`, `blocked`, `held`, `silent`, `actor`, `ref`, `decision`, `root`, `update`. No record declares `paced_at`, `paced_from` or `resend_of`: they stay the daemon's own bookkeeping on `record.Notice`.

### Nested shapes to alias in T2

Each keeps main's JSON keys and omitempty, and adds only omitempty members (`TestEachNoticeKeyHasOneShape` holds this against a copy of main's types). Names differ where a kind constant takes main's name.

- `record.NoticeFailure` = `texts.NoticeFailure`; `record.FailureBudget` = `texts.FailureBudget` (unchanged keys).
- `record.CatchUp` = `texts.CatchUpFacts`: adds `done` []string, `unreleased` []texts.UnreleasedChild{key,parent,status}, `accepting` []string; keeps `decided` (written by no builder now). `record.CatchUpGate` = `texts.CatchUpGate` (`Step` is a string, main's `DesignStep`); `record.CatchUpIssue` = `texts.CatchUpIssue`.
- `record.KeptDecisions` = `texts.KeptFacts` (embeds `texts.KeptApproval`); `record.KeptApproval` = `texts.KeptApproval` (`Step` string); `record.DecisionMark` = `texts.DecisionMark`: adds `options` []string and `answer` string (`texts:"words"`, path `catch_up.kept.decisions[i].answer`). `Decision` is a string (main's `DecisionKind`).
- `record.UpdateStalled` = `texts.UpdateRound`: adds `images`, `settings` []string; keeps `reasons` (not omitempty) and `waiting`; its `MarshalJSON` writes `waiting: []` for nil, as main's does. `record.UpdateWaiting` = `texts.UpdateWaiting`.
- `record.BlockedIssue` = `texts.BlockedIssue` (its `MarshalJSON` writes `blockedBy: []` for nil, as main's does); `record.HeldIssue` = `texts.HeldIssue` (`Reason` string, main's `HeldReason`); `record.SilentIssue` = `texts.SilentIssue`.
- New nested types, no main counterpart: `texts.OpenChildFacts`, `texts.UnrecordedChild`, `texts.CheckConclusion`, `texts.TreeMember`, `texts.RelaunchFacts`.
- Where main's field is a named type (`DesignStep`, `DecisionKind`, `HeldReason`), the alias makes it a string; T2 converts at the few writes or keeps record's own type and copies, whichever its callers need.
