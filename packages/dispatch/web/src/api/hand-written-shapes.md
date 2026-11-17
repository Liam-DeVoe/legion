# The shapes under `web/src/api` and `e2e` that are still written by hand

The Dispatch HTTP API's request and response shapes are generated: `envoy-dispatch openapi`
reflects the Go route table and the Go types each row takes and answers into
`packages/contracts/schemas/dispatch-openapi.json` and `dispatch-openapi-v2.json`, and
`bun run gen:api` renders those into `packages/contracts/src/dispatch-api.generated.ts` and
`dispatch-api-v2.generated.ts`, which `@legion/contracts` and
`@legion/contracts/dispatch-api-v2` re-export (dispatch://INFRA-1406). The dashboard calls
`/api/v2` alone, so every shape it reads comes from the second of those.

So a request or response shape written by hand here is a finding. This file is the list of what
is left and why each one is not a shape of that kind, so a reader checks the grep's answer rather
than deriving it again:

```
cd packages/dispatch && grep -rnE '^\s*(export )?(interface|type) [A-Za-z]' web/src/api e2e
```

A hit that is not an entry below, or not one of the classes below, is a shape that should be
generated.

## `web/src/api`

- **`client.ts`'s transport** — `FetchImplementation`, `UploadImplementation`,
  `UploadProgress`, `ErrorPayload`, `RequestBounds`, `WriteMethod`, `WriteRequest<T>`,
  `WriteBounds`, `ArtifactOwner`, `WriteVerb`, and the list shapes `ListPage<T>`, `ListPattern`,
  `ItemOf<P>` and `WholeList<P>` that `readWholeList` pages a list route with. These are the
  page's rules about how a request is sent and how its answer is kept, not what either carries:
  the silence deadline, the replay shape a kept write is stored as, which of three routes an
  upload's owner picks, and how the pages of one list are read whole.
- **`client.ts`'s projections of generated types** — `CreateArtifactReviewInput` is
  `Body<"/api/v2/artifacts/{id}/reviews", "post">`, `DeclineAskInput` and `AnswerAskInput` are
  the ask's state write without its `state`, `MarkInput` is `Body<"/api/v2/me/marks/{kind}/{id}",
  "patch">`, and `ErrorCandidate` is the union of the candidate shapes a refusal carries. Each is
  derived from the document rather than declared beside it.
- **`client.ts`'s `ReferenceSourceKind`** — the four kinds a reference source is read as
  (`artifact`, `ask`, `comment`, `message`). `GET /api/v2/asks` takes the source as one string
  (`source=<kind>:<id>`), so the document holds no member these could be read from; the client
  builds that string from this.
- **`types.ts`'s reads of a route that `include=` adds members to** — `IssueDetails` (the issue
  read with every member its `include` asks for), `AskWithThread` (`include=thread`), `InboxRow`
  (`include=thread,mark`) and `ArtifactText` (`include=text`). The document marks each added
  member optional, because the route answers it only as a read asks for it; each of these names
  the read that asked for it, and declares no field the generated row does not.
- **`types.ts`'s `Marks`** — the person's marks of one kind by the id each names, which is how
  `["user-state"]` and `["user-agent-state"]` hold the list `GET /api/v2/me/marks` answers. A
  keying of a generated row, not a shape of its own.
- **`types.ts`'s `IssueCollaboratorLegionAgent`** — the generated `CollaboratorV2` with its
  `legion` entry narrowed to present. The two readers that keep the registry's sessions
  apart from the rest - the Collaborators tab's Legion sections and the composer's "Not running"
  options - narrow to it with a type guard of their own, so the narrowed shape is named once.
  It declares no field the generated row does not; it is a refinement of it, as
  `MyReplyThreadRow` is of `MyReplyRow`.
- **`paths.ts`** — `PathsOf`, `Op`, `Json200`, `Json201`, `Answer`, `PathParams`, `QueryParams`,
  `JsonBodyOf`, `RequestBody`, `FormBody`, `Query`, `Body`, `Result`. These are how a route
  method reads its own shapes **out of** the generated document; each is a projection of
  `paths`, and none declares a field.
- **`live-socket.ts`** — `LiveSocketConnection`, `LiveSocketState`, `DeliveryLiveVerdict`,
  `EventsHandlers`, `AgentHandlers`, `LiveSocket`, `PendingLoad`, the three `*Subscription`s,
  `Subscription`, `LiveSocketPill`. The socket's own state machine, not a wire shape. The frames
  it carries are hand-written too, in `@legion/contracts`, for the reason below.
- **`event-feed.ts`'s `EventPages`, `query-refresh.ts`'s `RefreshTarget`, `RefreshCheck`,
  `DeferredCheck` and `SchedulerState`, `queries.ts`'s `DeliveryLiveBodies`, and
  `reachability.ts`'s `Reachable`** — the page's cache, its refresh scheduler and its
  reachability map.
- **A re-export is a hit of the grep and not a declaration of its own.**
  `IdempotencyLandedCode` in `client.ts` is `@legion/contracts`', and `QueryKey`,
  `QueryFunctionContext`, `RefetchOptions` and `RefetchQueryFilters` in `query-client.ts` are
  TanStack Query's. Each is read here under its own package's name.
- **A test file's fake is not a wire shape either**: `query-refresh.test.ts`'s `ServedQuery`
  and `live-socket.test.ts`'s `FakeLiveSocket` and `FakeLiveSockets` are the stand-ins each
  test drives its subject with.
- **A shape outside these paths that reads a generated one is not a wire shape**:
  `features/issue/pins.ts`'s `IssueState` is the viewer's mark of an issue with every field
  present, which `stateForIssue` defaults from the generated `MarkV2` - a mark holds no field it
  has no value for, and each reader would otherwise default it again. It declares no field the
  mark does not.

## `e2e`

- **`api.ts`'s harness half** — `ApiOptions`, `CachedSession`, `CallArgs`. The lane's base URL,
  the dev sign-in's cookie cache, and the argument record the typed call seam takes.
  `ApprovalRequested` is the `Result<…>` of the approval-request route the lane posts to, read
  from the document under the path `e2e/api.ts` names. That lane still calls the older API,
  which the browser suite's own task moves; the dashboard calls `/api/v2` alone.
- **Every other declaration under `e2e/`** is a fixture or a harness type of the test that holds
  it (`FakeSession`, `Seeded`, `ProbeLine`, the Playwright re-exports). None is a Dispatch
  request or response.

## The wire shapes that stay hand-written in `@legion/contracts`

These are not under the paths the grep reads, but they are the shapes a reader asks about next.
Each is in `packages/contracts/src/dispatch-api.ts`:

- **The live-socket frames** (`LiveSubscribe`, `LiveServerFrame`, `LiveEventFrame`, the
  `DeliveryLive*` and `EventsLive*` families). They travel `GET /ws/live`, which is no row of the
  route table the document is emitted from, and `packages/contracts/fixtures/live/` with the Go
  diff (`delivery_live_diff_test.go`) already holds them against the server.
- **The Going SSE frames** of `GET /api/v2/going-requests/{id}/stream`. A frame union is not an
  OpenAPI response, and `packages/contracts/src/going.ts` holds them.
- **`DispatchEvent` and the event payloads.** Go's `model.Event.Payload` is bytes, so the
  document can only say `payload: unknown`; `DispatchEvent` is the union over the same bytes,
  per event type, and is what every event read is typed as. The payloads themselves travel the
  message bus rather than an HTTP body.
- **The closed string enums** (`AskUrgency`, `IssuePriority`, `CheckWhen`, `IssueRouteStatus`,
  and their kind) and the `const` arrays they are read from. Each array is a value and not only
  a type: `dispatch-tools.ts` builds the MCP tools' `z.enum` from it and the dashboard renders
  its dropdown from it, and a schema in the generated document gives a type and no value. The
  document still widens these fields to `string`, because their Go fields carry no `enum:` tag;
  a surface that needs the closed list narrows a value to it at the boundary rather than
  widening itself. A Go field that does carry an `enum:` tag is the other case and belongs to
  no list here: the emitter renders its set as a closed union, so the field arrives narrowed and
  nothing widens or re-narrows it by hand. `grep -rn 'enum:"' packages/envoy/internal/dispatch`
  is the set, which is why this file names none of them.
- **The helpers no route answers** — `Advised<T>` and `DocumentAdvised<T>` (the advice envelope
  is a merge the document writes as an `allOf`, and the two differ in which advice a write
  carries), `DispatchServiceErrorShape`, `ArtifactVersionBlob`/`ArtifactVersionContent` (the byte
  arm of a version download is a transport value, not a wire shape), `IssueRead`,
  `CreateArtifactInput` (it carries a `Blob`), `AuthenticatedUser`
  (`GET /auth/whoami` is outside the API's own paths).
- **The Inbox row's two narrowings** — `MyReplyThreadRow`, `MyReplyAgentRow` and the
  `inboxThreadRow` that tells them apart. `GET /api/v2/me/threads` answers both kinds of row
  under one Go struct, so the generated `MyReplyRow` carries each kind's fields as optional and
  `kind` as the open string the Go field holds; these two say what a row of each kind always
  carries, as `AskWithThread` does for an ask read with its thread. Neither declares a field
  the generated row does not; each is a refinement of it.
- **`IDCandidate` and `ID_AMBIGUOUS`** are a refusal body that stays hand-written, and the one
  entry here that is a wire shape rather than a helper. `writeAmbiguousID` (`id_prefix.go`)
  answers 409 with `{error, code, candidates}` when an id written as a prefix names two items or
  more, and it is written from the **prefix layer that wraps every route taking an id**, not
  from one route's handler — so no `routeShapes` entry owns it, nothing names it among a route's
  `Refusals`, and the emitter has no route to attach it to. The dashboard reads it from this
  hand-written type, and `scripts/gen-go.ts` renders the code into Go. Giving it a Go type and
  attaching it to the `Refusals` of every route that takes an id would generate it; that is a
  change to which routes declare which refusals, which this issue does not make.

## What is not generated, and is not an exception either

The error **code** strings. The route table records a route's codes only in its prose, so making
them machine-readable is a change to the table's own content, which dispatch://INFRA-1406 puts
out of scope. The error **bodies** are generated, the six refusals richer than `{error, code}`
included.

Headers are not in the document either — `Idempotency-Key`, `Dispatch-Login`,
`Idempotency-Replayed`, `Retry-After`, `If-None-Match`. They are the client's rules about how a
request is sent, and they live in the transport above.
