# Code map for INFRA-3686 tasks T10, T11 and T12 (read-only, `main` at b4a566cb)

**Where I stopped:** every item in the assignment is mapped below. I changed nothing and ran no tests. Three things I did not check are listed at the end.

## Summary

- **T10.** `roleSend.noHolder` raises the role's wake report, with no condition, before it keeps the message or answers 404 (`packages/envoy/cmd/listener/confirmed.go:228-229`). The envelope it can check is `s.item`. Its `Urgency` and `SourceSession` are copied as they are from the request body (`api.go:571,582`). `reportRoleWake` has this one caller.
  - A second path is not in the plan. The kept-lane sweep raises the role's wake again for every copy the listener kept, whatever its urgency (`role_keep_sweep.go:195-224`). A kept `low` daemon copy would still wake its role this way.
  - The daemon's own notice publisher sends `keep:false` and no `source_session` (`packages/daemon/internal/notify/notify.go:221-228`), so those publishes keep nothing and the sweep never sees them.
- **T11.** `envelope()` (`outbox/publisher.go:1483-1550`) is the one envelope builder. Every destination copies its `item`, and so does the stand-in (`batch.go:869-896`). The actor is `event.Actor` (`model.go:3359`); a session actor already becomes `SourceSession` (`publisher.go:1516-1518`). The ask urgency copy is at `publisher.go:1538-1540`.
  - `legionDaemonSessionPrefix` is defined at `api/issue_collaborators.go:62` and used at `:221` and `api/issue_role_holders.go:96`.
  - `model` can hold it: `model` imports only `contracts` and `fold` (`model.go:4-16`), and both `api` and `outbox` already import `model`.
- **T12.** R6 needs these pieces, and none exists today:
  - a registry read after `loadLookups`, beside `resolveUnpinnedRoleMentions` (`publisher.go:909`);
  - a way to hand its answer to `addPrimaryDocumentAuthors` (`:1239`) and to `addAuthorRoutes`' `considerCredit` (`:1361`);
  - a failed-mention exemption in `addPrimaryDocumentAuthors`;
  - a first pass that also asks for `mentionFallbackKeys` on edits of unclaimed issues (`askTheClaim`, `:1337-1352`, returns early when the issue is unclaimed).

  The registry client to use is `envoy.Client.Session`. It sends `?include=unresponsive` and answers `State` live, unresponsive or gone, `ErrNotFound`, or `ErrUnavailable` (`envoy/client.go:523-575`). No outbox test fakes the registry today.

## Files

### T10 (listener)
- `packages/envoy/cmd/listener/confirmed.go:157-182`: `roleSend`. `item` is the envelope; `keep` (167-171) is the request's own flag and is not on the envelope.
- `confirmed.go:208-227`: `noHolder`'s doc comment. Lines 221-227 state the wake rule ("raises the wake report beside the answer … Both answers need it").
- `confirmed.go:228-244`: `noHolder`. Line 229 is `reportRoleWake(s.d, s.d.delivery.logger, s.role)`; it runs before the `keep && roleKeepable` branch (230-235) and before the 404 (237-243).
- `confirmed.go:380-396,422-424`: `sendToRole`. It stamps the sender from `item.SourceSession` (386) and calls `noHolder` for a first resolution that is not live (394-396) and for a later attempt that finds the role unclaimed (422-424).
- `confirmed.go:264-267`: `kept` replaces `s.item` with the copy's envelope. This happens after the wake call, so the check sees the request's own envelope.
- `wake.go:21-30`: the doc comment of `wakeKindRole` ("The publish route raises it once, beside the 202 kept or the 404 it answers (reportRoleWake)…"). `wake.go:269-277`: `reportRoleWake`, which returns at once on nil deps or an empty role. `wake.go:251-266`: `wakeReporter.report`, asynchronous, a no-op on a nil receiver. `wake.go:74-79`: `wakeReport{Kind, Role, Session, At}`.
- `api.go:39-56`: `messageBody`; `Urgency *string` is at 48 and `SourceSession string` at 41.
- `api.go:521-548`: `validateMessageBody`. A non-nil urgency must be `low`, `med`, `high` or `blocking`; an empty string is refused (537-541).
- `api.go:556-585`: `messageEnvelope`. `SourceSession: body.SourceSession` (571) is not trimmed; `Urgency: optionalString(body.Urgency)` (582); `Source` defaults to `"agent"` (566-569).
- `api.go:1149-1177`: the publish route's doc comment, whose `keep` paragraph is at 1171-1177. `api.go:1178-1323`: `publishHandler`. The validation is at 1245; the envelope is built at 1269; role topics go to `sendToRole` at 1290.
- `brokerauth.go:332-342`: `sessionNotBound`. An empty `source_session` is allowed ("an envelope with no source session", 329-331). A request with no broker token is never refused here.
- `role_keep_sweep.go:168-193,195-224`: `raiseKeptRoleWake` and `keptLaneWakesItsRole`. Any listener-kept copy (`!isOutboxCopy`) younger than `roleKeepWakeAge` wakes the role; urgency is not read.
- `role_keep.go:1317-1319`: `isOutboxCopy`.
- `main.go:70-71,1449,1584`: the `wakes` field, which `main.go` wires; it is nil in tests unless a test sets it.

### T10 tests and helpers
- `send_state_test.go:598-662`: `TestASendRaisesTheWakeWhereTheEntryIsGoneAndNotForAProbeStoppedEntry`. It sets `rig.deps.wakes = newWakeReporter(...)` (602), subscribes on core NATS to `bus.LegionWakeTopic` (603-611), then drains with `NextMsg(500ms)` until `ErrTimeout` to show a report is absent (649-661).
- `send_state_test.go:556-572`: `awaitAgentWake`. `:574-589`: `wakeReportSession`, which decodes the envelope and then the `wakeReport` payload and returns `report.Session` only for `wakeKindAgent`. A role variant would return `report.Role` for `wakeKindRole`.
- `role_keep_test.go:107-112`: `keptRoleDeps`, the publish route over its own stores. It sets no `wakes`, so a test must set `d.wakes`. `:115-127`: `publishToRole`, which decodes `keptPublishAnswer`; that type embeds `contracts.Envelope` (`:30-...`), so `answer.Urgency` is readable on a 202.
- Existing role publishes with no holder:
  - `TestAPublishToAnUnheldRoleIsKeptAndAnsweredWithItsReason` (`role_keep_test.go:131`);
  - `TestAPublishWithKeepFalseToAnUnheldRoleIsRefusedAndKeepsNothing` (`:303-318`);
  - `TestAPublishWithKeepFalseToALapsedClaimIsRefusedAndKeepsNothing` (`:~280`).

  None of the three observes wakes.
- The only test that observes the route's keep wake is `TestAKeptCopyWakesItsRoleUntilTaken` (`role_keep_hour_test.go:1943-2049`). It uses `hourRig.wakes` and `keepWakes` (1895-1935), which read the stream, and `roleMessage(role, sender, msg)` (155-161), whose `sender` may be empty.
- `confirmed_test.go:116-120`: `withPublishRoute`. `:163-...`: `publishThrough(d, body)`.
- `role_keep_hour_test.go:313`: `readStored(ctx, client, subject)`. `role_keep_handover_test.go:454-472`: `sweptRoleWakes`, which reads the stored reports.
- No listener test sends `urgency: low` today. The urgency fixtures are only `high`, `blocking` and refusals (`api_test.go:97,507-511,2738,3373`).

### T10 docs
- `packages/envoy/AGENTS.md:5093-5095`: the urgency note. Line 5095 says "`urgency` is informational: … no part of Envoy orders, holds or delivers a message by it." The listener's wake would now depend on urgency, so this sentence changes.
- `AGENTS.md:3662-3665`: the sweep's re-wake. `:3671`: "Either way it is kept and raises the role's wake report." Related: `:3620-3626` (202/404, "the Legion daemon does" send `keep:false`) and `:4741-4762` (the publish route's `keep`).
- `packages/envoy/docs/architecture.md:405-407` (sweep) and `:418-419` ("Either way it is kept and raises the role's wake report").
- `docs/site/src/content/docs/dispatch/envoy.md:208-210` (sweep re-wake) and `:236-237` ("Either way it is kept and wakes the role").

### T11 (Dispatch urgency)
- `outbox/publisher.go:1471-1482`: the doc comment of `envelope`. `:1483-1550`: `envelope`:
  - `item` is built at 1505-1515;
  - `SourceSession = event.Actor.ID` for `Actor.Kind == "session"` (1516-1518);
  - `in_reply_to` from `reply_to` or `ask_id` (1522-1530);
  - the ask urgency copy at 1538-1540.

  The only other `Urgency` reference in non-test outbox code is none (a grep finds only 1539).
- `envelope()` callers:
  - `resolveDestinations` at `publisher.go:883`, then `item.Validate()`;
  - the fallback pass at `mentions.go:1682`;
  - the test helper `publish` at `publisher_test.go:108`.

  `standInEnvelope` copies `item` with its urgency (`batch.go:869-896`).
- `api/issue_collaborators.go:59-62`: the prefix and its doc. `:221`: the collaborators filter. `api/issue_role_holders.go:91-97`: `legionDaemonSessionOf`, used at `:186`. Tests spell the literal `"legion-daemon:TEST"` (`issue_collaborators_test.go:221,359-360`; `issue_role_holders_test.go:31-32`; `issue_close_asks_test.go:143`).
- `events/broker.go:491-503`: `Notify`. `child.status`, `child.added` and `child.removed` always notify; otherwise only `Actor.Kind == "user"` does.
  - So a daemon `issue.updated` or `message.created` reaches only the owner topic, while a `child.status` also reaches the route.
  - `issue.released` also reaches the previous claimant whatever `Notify` says (`publisher.go:1154-1165`).
- The `asks.urgency` column defaults to `'med'` (`store/migrations/0001_init.up.sql:42`). `seedAsk` sets no urgency (`publisher_test.go:1907-1922`).

### T11 tests and helpers
- `publisher_test.go:281-334`: `TestRunPublishesAskAnswerEnvelope`.
  - The payload is a `model.Ask` with `Urgency: "blocking"` (289-298). It is read back from Postgres as a map, so `payloadString` finds the urgency.
  - The assertion `item.Urgency != "blocking"` is at 317-319.
  - It reads `publisher.all()[0]` (issue topic) and `[1]` (asker) (305-311).
- `publisher_expects_reply_test.go:10-16`: the stale comment ("…an ask event the same branch gives an urgency included."). Its cases carry `"urgency":"high"` (34, 51) but assert only `expects_reply`.
- `expects_reply_never_false_test.go:57-62` sets `urgency: high` and asserts only `expects_reply`.
- Helpers:
  - `appendEvent(t, db, broker, model.Event{IssueKey, Type, Actor, Payload})` (`publisher_test.go:1875-1890`; the broker sets `Notify`);
  - `run(t, db, publisher, broker)` (`:1852-1855`, which uses `withHeldRoles`);
  - `recordingPublisher` and `.all()` (`:192-246`), which records every envelope including role forwards to `roleHolder` (`Request`, `:231-239`);
  - `seedIssue(t, db, key, route)` (`:1892-1904`);
  - `seedAsk` and `seedFollower` (`:1907-1929`);
  - `waitFor` and `publishedAt` (`:1962-1976`);
  - `claimIssue` (`publisher_claimant_test.go:19-30`).
- A `previous_claim` payload shape to copy: `TestPublishPreviousClaimantTellsTheSessionThatLostTheClaim` (`publisher_test.go:533-586`).

### T12 (R6)
**Batch flow** (`publisher.go`):
- `scanBatch` (230) calls `resolveDestinations` (290).
- `resolveDestinations` (876-918):
  - The first pass runs `eventDestinations(..., want)` on a `wanted` lookup that only records ids (895).
  - `loadLookups` runs under `deps.lanes.statement` (899-905).
  - Then `resolveUnpinnedRoleMentions(ctx, deps, found.readings()...)` (909).
  - The second pass runs `eventDestinations(..., found)` (910-915).
- The registry read belongs between 905 and 911, outside the statement gate. `resolveRoles` (`role.go:257-271`) is the pattern for parallel reads (`wg.Go`, a mutex-guarded map).

**`eventDestinations`** (`publisher.go:1057-1091`) adds routes in this order:
- the owner topic;
- under `Notify && !targeted` (1072): the route, the claimant (1078), `addPrimaryDocumentAuthors` (1079), `addAuthorRoutes` (1082);
- then `addFollowerRoutes` (1086) and `addPreviousClaimant` (1089).

The new comment's mention deliveries are read first, only for `comment.created` with mentions (1060-1066).

**`addFollowerRoutes`** (1187-1228). For `comment.created`, `resolved`, `reopened` and `edited` it takes `askID` from the payload's `ask_id`. It reads `look.followers(askID)`, skips the actor, and adds through `addSessions`.

**`addPrimaryDocumentAuthors`** (1230-1256). It is gated by `takesPrimaryAuthors`, reads `look.primaryDocumentAuthors`, filters through a fresh `recipientFilter`, and calls `d.addSessions`. It has no failed-mention exemption today.

**`addAuthorRoutes`** (doc 1258-1301, func 1302-1448):
- `askTheClaim` (1337-1352) sets `claimed` (1338) and returns early when the issue is unclaimed. Otherwise it reads `failedMentions = d.mentions.fallbackKeys` for a created comment (1344), or `look.mentionFallbackKeys(id)` for an edit with mentions (1345-1350).
- `considerCredit` (1361-1369) keeps a credit only when `!claimed`, or when the session is in `failedMentions`, or when `d.mentions.decidesCopyFor` holds.
- The reply arm `case replyTo != ""` (1384-1401): `look.commentRecipients(replyTo)`; a `fromThread` member goes to `consider`, every other row to `considerCredit`.
- The `ask_id` arm (1382-1383) does nothing.
- The document arm calls `considerCredit` for every credit.
- The final add is `d.addSessions(targets, "publish author route to")` (1446).

**`destinations`** (928-936):
- `addSession` (971-981) gives a session whose mention failed that mention's key (`d.mentions.fallbackKeys[sessionID]`).
- `addSessions` (1024-1028) adds each session's own subject.
- One subject is added once (`addDestination`).

**`recipients.go`:**
- `commentRecipientsQuery`: doc 39-54, SQL 55-100. `from_thread` is computed at 96.
- `threadRecipient` (103-109).
- `loadCommentRecipients`: doc 111-122, func 123-128.
- `primaryDocumentAuthorsQuery` (130-144) and `loadPrimaryDocumentAuthors` (146-155).
- `takesPrimaryAuthors`: doc 197-201, func 202-210. It covers `comment.created`/`comment.edited` with `Notify`, an issue key, no claimant and no route; the thread is ignored.
- `takesClaimantRoute` (212-233).
- `recipientFilter` (235-257).

**`lookups.go`:**
- The `lookup` interface (27-43); `wanted` (50-101), whose `mentionFallbackKeys` records `editMentions` (98-101); `lookups` (121-133).
- `followers` (135-141) and `commentRecipients` (175-181) are keyed by `canonicalID`.
- `mentionFallbackKeys` (198-204); `readings` (212-221), which leaves out edit-only reads.
- `loadLookups` (264-297); the thread read at 294.
- `loadFollowers` (428-436) reads `ask_followers`. `asks.Unfollow` deletes the row (`asks/followers.go:51-54`), so an asker that stopped following is not among the followers.

**`mentions.go`:**
- `mentionDeliveries` (69-...); `fallbackKeys` (75-89).
- `decidesCopyFor`: doc 254-286, func 287-289. A zero reading, which an edit has, answers `mentionFailed` for everyone.
- `mentionDecidesTheCopy` (375-377); `leftToMention` (895-897); `payloadHasMentions` (1089-1093).

**Outbox `Deps`** (`publisher.go:79-135`):
- `RoleClaims roles.ClaimStore` and `Sessions roles.SessionLookup` (91-96) are the role and session buckets, judged by Dispatch's own windows (`roles/roles.go:46-67`).
- A Deps built without a new field leaves it nil, which is "the registry does not answer" under the plan.

**`withHeldRoles`** (`role_test.go:56-60`) sets only `RoleClaims` and `Sessions`. Its users and every other `outbox.Deps{}` construction would keep a nil registry client:
- `withHeldRoles` callers: `attempt_test.go:176`; `order_test.go:732,1088`; `publisher_comment_recipients_test.go:528,563,943,995`; `publisher_document_authors_test.go:421`; `publisher_mentions_test.go:339,392,701`; `publisher_test.go:1854`; `refused_test.go:90`; `role_test.go:1109`; `session_answers_test.go:76`.
- Other constructions: `cmd/dispatch/main_test.go:732`; `shutdown_test.go:581`; `cmd/listener/dispatch_kept_role_test.go:99`; `api/approval_versions_test.go:930`; `api/comment_delivery_*_test.go`; `api/session_answers_test.go:169`; `deliveryaudit_test.go:241`.

**The registry read on the claim path:**
- `api/issue_claim.go:181-213`: `fetchHolderLiveness`. A nil `s.deps.Envoy` is a 503; it calls `s.deps.Envoy.Session(ctx, id)`; `ErrNotFound` means no record; `ErrUnavailable` is a 503. It runs with nothing held (189-195).
- `envoy/client.go`:
  - `ErrUnavailable` and `ErrUnreachable` (23-35); `ErrNotFound` (67).
  - `StateLive`, `StateUnresponsive`, `StateGone`, `StateStopped` (111-124); `includeUnresponsive` (126-131).
  - `Session` struct (148-176): `State` is empty → live by `normalize` (199-...).
  - `Client`, `New`, `WithToken`, `WithTimeout`; the default timeout is 5 s (250-292).
  - `Session` (511-525): 200 with state live, unresponsive or gone; 404, 400 or 413 → `ErrNotFound`; other answers → `ErrUnavailable`.
  - A stopped session is `ErrNotFound` from `Session`; only `SessionIncludingStopped` answers `stopped` (527-545).
  - `readSession` (547-575).
  - The package imports only `contracts` (5-21), so `outbox` can import it without a cycle.

**Wiring in `cmd/dispatch/main.go`:**
- The outbox `Deps` is built at 718-729, with `holders := roles.NewBuckets(...)` at 718.
- `main.go` does not import `internal/dispatch/envoy` today (imports at 43-57).
- The API's client comes from `boot.EnvoyURL`, which defaults to `http://127.0.0.1:9020` (64-65, 1312-1320), and `boot.EnvoyToken` (`ENVOY_TOKEN`, 220-221, 1198), passed through `appContextOptions` (1677-1679).
- `api.NewDeps` builds that client as `envoy.New(url, envoy.WithToken(token)[, WithTimeout])` (`api/server.go:287-294`). The outbox would build the same from `boot.EnvoyURL` and `boot.EnvoyToken`.

### T12 tests and helpers
- `publisher_comment_recipients_test.go`, the table test `TestAPersonsCommentOnAnIssueNobodyRoutesOrClaimsReachesItsSpecsSessions` (304-438):
  - Each case runs `seedIssue(tc.route)` and then `seedSpec(t, db, "T-1", specWriter, [specWriter], [alice, {Kind:"session"}], [editor, specWriter])` (428-436). Creator and editors are credited through `seedDocument` (`publisher_document_authors_test.go:22-49`) with `makePrimary` (69-74).
  - "a person's reply reaches the spec's sessions, then the thread's" (344-352): a root by alice, a reply by `replier`, a reply by sami. `appendCommentEvent(..., "comment.created", sami, model.Comment{ID: reply, ReplyTo: &root})`; it expects `[spec, editor, replier]` in order.
  - The ask-thread case (361-369): `seedAsk` by `session-asker`, which auto-follows; then `model.Comment{ID: <uuid>, AskID: &askID}`; it expects `[spec, editor, session-asker]`.
  - The mention case (391-404): `seedMentionDelivery(t, db, comment, editor.ID, "failed")` and `appendCommentEvent(..., editor.ID)` to put the mention in the payload.
- Other helpers:
  - `seedThreadComment(t, db, issueKey, anchoredOn, author, replyTo)` (22-42); anchoring uses `anchorOn` (`publisher_document_authors_test.go:78-80`).
  - `appendCommentEvent` and `appendIssueCommentEvent` (45-66): the payload is `model.CommentEventPayload{Comment}` and mentions come from `mentionOf` (`publisher_claimant_test.go:33-35`).
  - `seedMentionDelivery` and `seedDelivery` (`publisher_mentions_test.go:42-73`).
- How a test reads who received a copy:
  - `checkSessionRecipients` (260-289) runs one `scanBatch` with `Deps{Store, Publisher: &recordingPublisher{}, Broker}`. It takes no deps argument, so a new test needs a variant that sets the registry field.
  - It compares what was sent with `published_destinations` (`readEventRow`, 455-467), then `agentTopics(row.destinations)` (`publisher_document_authors_test.go:68-76`) against `want` in order.
- How events carry the fields T12 reads:
  - `comment.edited` is appended at `api/comment_actions.go:261-265` with `commentEventPayload` (`api/comment_projection.go:80-...`), which embeds the whole `model.Comment`.
  - So `id`, `reply_to`, `ask_id`, `mentions` and `edited_at` are top-level payload keys (`model.go:2650-2690`, `CommentEventPayload` at 2858).
  - The outbox reads them from the payload map with `payloadString(event.Payload, "reply_to" | "ask_id" | "id")` (`publisher.go:1381-1384,1210-1211,1589-1596`).
- A fake registry to copy: the API tests stand up `httptest` listeners that answer `/v1/registry/<id>`. Two to copy:
  - `TestDeliveryResolvesAnUnresponsiveHolderAndRefusesAGoneOne` (`api/comment_delivery_resolution_test.go:587-604`, whose 200 carries `"state":"gone"`);
  - `writeSessionRecord` (`api/server_test.go:338-361`).

  Both live in the `api` package's tests, so `outbox` cannot import them; they need a copy.

### T12 docs
- `packages/envoy/AGENTS.md`:
  - 5897-5904: the ask-scoped follower rule.
  - 5906-5912: the reply and document credits rule.
  - 5914-5922: the primary-document rule ("…a session that wrote the spec without a claim still hears comments on any of the issue's threads, an ask's thread included"). The destination order is at 5918-5919.
  - 5924-5931: the claimed-issue exemption list.
- `.dispatch/architecture/dispatch-outbox.md:48`: the lookups paragraph, which names the "member of the thread or … credit" mark and the primary-document sessions. No other routing-rule paragraph turned up there.
- `docs/site/src/content/docs/dispatch/envoy.md:135-143`: "A person's reply to a comment reaches … every session that wrote the document the thread is on" and "On an issue with no route … reach every session that wrote the issue's spec".
- `docs/site/src/content/docs/dispatch/for-agents.md:249-254` and `:264-265`.
- Code doc comments: `takesPrimaryAuthors` (`recipients.go:197-201`), `addAuthorRoutes` (`publisher.go:1258-1301`), `loadCommentRecipients` (`recipients.go:111-122`), `commentRecipientsQuery` (`recipients.go:39-54`).

## Architecture

- **T10.** The publish route (`api.go:1178`) calls `sendToRole` (`confirmed.go:380`). When the first resolution is not live, it calls `noHolder`, which calls `reportRoleWake`, which hands the report to the asynchronous `wakeReporter` and on to `notifications.legion.wake`. Separately, the sweep (`raiseKeptRoleWake`) raises the report again for listener-kept copies. Dispatch's role-routed events never go through `noHolder`: the outbox forwards or keeps them itself and raises no wake (`AGENTS.md:3972`).
- **T11.** Per batch, `resolveDestinations` builds one `envelope(event)`. Each destination copies `item` and changes only `Topic`/`DedupeKey`, so an urgency set in `envelope()` reaches every route, follower, claimant, kept copy and stand-in.
- **T12.** The batch reads the `wanted` ids, then `loadLookups` gives `found.threads[reply_to]` (with `fromThread`) and `found.asks[ask_id]` (followers). The registry read goes next: one `Session` per distinct thread session, in parallel. Its answer must reach `eventDestinations` through a field on `batchEvent` or `lookups` that the plan does not name. `addPrimaryDocumentAuthors` and `considerCredit` then skip credit-only sessions while a thread session is live or unresponsive, except `failedMentions` and `decidesCopyFor`.

## Points for whoever implements

1. **T10, the sweep.** With only `noHolder` changed, a kept `low` daemon copy (one sent without `keep:false`) is still woken by the sweep within its first 40 minutes (`role_keep_sweep.go:213-215`). The daemon's notice publisher sends `keep:false`, so its notices are never kept. [INFERENCE] The sweep matters only for daemon role messages that the keep path does keep; T5/T7 decide how those are sent.
2. **T10, an empty session.** `SourceSession` is not trimmed (`api.go:571`), while `sessionNotBound` trims (`api.go:1198`). An "empty" check should match whichever the route uses.
3. **T10, the test.** Each case needs its own role, because a wake report names only the role (`wake.go:74-79`). To prove a report is absent, use `TestASendRaises…`'s `NextMsg` drain (`send_state_test.go:649-661`), or read the stream with `readStored`.
4. **T11, what each daemon event reaches.** A daemon `issue.updated` or `message.created` has `Notify` false, so it reaches only the issue topic (`broker.go:491-503`). `child.status` reaches the route too. The T11 test's "every route" means just those.
5. **T12, the test helper.** `publisher_test.go:105-170` (`publish`) repeats `resolveDestinations`' steps by hand (`:117-128`). If R6 runs in `resolveDestinations`, that helper should call the same step, or its tests will bypass R6.
6. **T12, destination order.** `checkSessionRecipients` checks order (`:283-286`). Primary-document authors come before author routes and followers (`publisher.go:1079-1086`), so each new case's `want` must follow that order.
7. **T12, which states count.** `Session` answers `ErrNotFound` for a stopped session or one it does not know, and 200 with `state:"gone"` for a gone one. Count only `live` and `unresponsive`, and treat an empty state as live (`normalize`). `ErrUnavailable` and a nil client mean "does not answer", so the credit copy goes.

## Not checked

- Whether `setupPublishTestClient` and `setupSessionsTest`, which each call `testnats.URL(t)`, share one NATS server within a test.
- Paragraphs of `dispatch-outbox.md` other than line 48.
- `scanPendingEvents`' exact payload decode. I relied on the comment at `events/broker.go:511-515` for "the payload is a map at publish time".