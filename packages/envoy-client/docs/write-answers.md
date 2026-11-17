# What an answer of Dispatch becomes

This file uses semantic line breaks: one sentence per line, and a long sentence may break after a clause.
A blank line separates list items.
Add a new item next to the item it belongs with, never at the end of a list unless it belongs there, so two pull requests that add items or change different sentences do not conflict.
`bun scripts/semantic-line-breaks.ts packages/envoy-client/docs/write-answers.md` checks the layout, and `--write` fixes it.

`DispatchClient` (`dispatch-http.ts`) reads each answer of Dispatch as one of the outcomes below, and the `dispatch` command prints each outcome as the last section says.
The package's [README](../README.md) ("Dispatch") says how a write carries its `Idempotency-Key` and when the client asks again under it.
[`ci-results.md`](ci-results.md) says what `dispatch result --project`, a CI job's write, prints for each outcome.

## A refusal

A non-2xx answer is a `DispatchServiceError`.
Dispatch's own refusal is JSON with a string `error`.
The error's message is that text, under its `code`, or under `HTTP_<status>` when Dispatch set no code, and `fromDispatch` is true.

Any other body did not come from Dispatch: a gateway's HTML page, an empty body, or JSON of another shape.
It is a `DispatchGatewayError`, whose `fromDispatch` is false.
Its message names the method and the URL with its query, and leaves out a user and a password that the URL may carry.
The bearer is a header, so no message names it.
The message then names the status and the reason phrase, a one-line plain-text excerpt of the body, and what asking again can do.
The excerpt drops scripts, styles and tags, and it reads at most the first 64 KiB of the body, in linear time.
In the excerpt and in the reason phrase, the client redacts the value after `Authorization:` or `Bearer`.
It also redacts each run of 8 or more characters of its own bearer, and a shorter bearer only whole.
So a copy of the bearer that is cut short, split by markup or overlapped by another copy leaves no piece behind.
The message names a body with no text in the part that the excerpt reads by its size in bytes.
Only a body with nothing in it is "an empty body".

What asking again can do depends on the status (`gatewayAdvice`).
A status that cannot clear gets the same answer until someone fixes the Dispatch URL, or whatever answers in its place.
A 5xx, a 408 or a 429 can clear.
A `GET` that a gateway answered so may succeed when the client asks it again.
So may a write that a gateway answered 408 or 429.
A gateway sends its own timeout or rate limit before it forwards anything (`mayHaveReachedDispatch` is false).
A write that a gateway answered 5xx may or may not have reached Dispatch.
The advice is then to check whether it took effect before you send it again.
A caller that knows more about its own request can give its own advice from the error's `answer`, `advice`, `transient` and `mayHaveReachedDispatch`.
The close path of `dispatch issue-update` does so.
The executor reads a status as Dispatch's meaning only when `fromDispatch` is true.
Such a 404 means no linked issue, no such document, or a server without the route.

## A redirect

No request of the client follows a redirect: not a read, not a write, and not the `/healthz` probe.
Dispatch's API routes answer no request with a 3xx, so a 3xx is a refusal of whatever answered in Dispatch's place.
It is a `DispatchGatewayError`, whatever its body is.
Its message names the request as for any gateway's answer, then the status and the reason phrase, and the origin that the redirect points at.
That origin is the scheme and the host of the `Location`, with a port that is not the scheme's own (`redirectOrigin` in `errors.ts`).
The message holds nothing of the target's path, query, fragment or user, and nothing of the answer's body.
A redirector writes its target into the body too, as text or as the text of a link, and that target can carry a secret.

The listener's client refuses a redirect the same way.
An `EnvoyApiError` of a 3xx names the method, the URL without its user and password, the status and the origin (`transport.ts`).
It names nothing of the body.
The pool identity of `@legion/broker-client` names a 3xx of the security token service by its status alone, as `HTTP <status>`.

## A path value that names nothing

The client refuses a call whose URL path would hold an empty value, before any request, for every caller (`DispatchEmptyPathError`, thrown by `#url`).
Sent, `POST /api/v1/comments//resolve` names no comment, and Dispatch's router answers it with a redirect to another path.
The message names the route with `{empty}` in place of the empty value, and says that nothing was sent.
It adds that a variable that gives the value may be empty or unset.
The `dispatch` command exits 2 on it, as on any refusal of its arguments.

## A write whose outcome is unknown

A write whose attempts run out throws the last attempt's own error, with its usual message.
The exception is a last answer that alone would say that the write never took effect, while an earlier attempt may have reached Dispatch.
Such an answer is a gateway's 408 or 429, Dispatch's own 429, or `409 KEY_IN_FLIGHT`, and the call then ends as a `DispatchWriteUnsettledError`.
That error is deliberately not a `DispatchServiceError`, because a caller reads a `DispatchServiceError` as a refusal that Dispatch decided.
Its message gives the last answer and the number of attempts.
It then says whether an earlier attempt may have reached Dispatch, or whether Dispatch was still running one.
It says that the write may or may not have taken effect.
Where the call saw the keyed layer judge the write, the message says that Dispatch finishes an attempt that it received.
It then says to read the write back before you send it again, because a new command sends it under a new key.
Otherwise it says to check whether the write took effect before you send it again.

A write that ends with no answer at all is a `DispatchWriteUnsettledError` too, and its message names the request.
The client cut off its last attempt for silence ("made no progress for 20 s, so it was cut off").
Or the call's deadline ended it, while an attempt was out or after one that may have reached Dispatch.
The message then says that the write "had no definitive answer when its deadline ended it".
Its `last` is the cut or the timeout.
The client does not send an attempt that the deadline ends before it goes out.
Neither that attempt nor a credential that could not present counts as an attempt that may have reached Dispatch.
When the deadline ends a pause that followed answers that reached no handler, the write ends with the last of those answers.

What the attempts showed is a fact of the whole call (`WriteCall`), not of one series of attempts.
A write carries one key across its no-route retries and its re-send after a dead token.
The keyed layer wraps each route's handler, so it stores nothing under the key for those answers.
Once an attempt may have reached a handler, the call ends as a `DispatchWriteUnsettledError`, unless its last answer is the keyed layer's verdict.
That verdict is a definitive answer that names the key back and that the layer keeps as the key's outcome.
It is the stored result, a replay, a landed answer, or the handler's refusal.
An echoed 401 is no verdict, because it can come after the handler committed.
`422 KEY_REUSED` is no verdict either, because it says only that this request did not run.
A no-route answer, Dispatch's check of the token, a dead token or a credential that could not present proves nothing of the earlier attempt.
The client throws the caller's own cancellation as it is.

`writeOutcomeUnknown(error)` says whether a write's error leaves its outcome unknown.
It does for a `DispatchWriteUnsettledError`, for a gateway's answer with `mayHaveReachedDispatch`, and for Dispatch's own 5xx.
It also does for a transport failure after an attempt that may have reached Dispatch, which the client throws as it is.
A connection that never opened reached nothing: the host refused it, its name did not resolve, or no route reached the host.
A request that Dispatch's check of its broker token turned away before any handler ran reached nothing too.
Those answers are `429 BROKER_TOKEN_RATE_LIMITED` and `503 BROKER_UNAVAILABLE` (`tokenNotChecked`).
A refusal carries its `Retry-After` as `retryAfterMs`.

## A write that landed with its answer gone

Dispatch's keyed layer names the request's `Idempotency-Key` back on every answer that it gives a keyed write.
So an answer that names this call's key back is Dispatch's word about this write.
A landing needs that key.
The client takes a write as made, with its answer gone, only from an answer that names the call's key back.
The rule is the same for a 2xx and for a `409 KEY_COMMITTED` (`#notTheRow`).
A key that a caller gives is the call's key: `docEdit` and the upload of a project's document take one.
An empty key names no request, so no answer names it back, and an answer that names no key is no landing of it.

A write that Dispatch made and whose answer is gone is a `DispatchWriteLandedError`, with `code`, a `DispatchLandedCode`, and `status`.
On a route that stores what it is sent, these answers are a landing when they name the call's key back:

- `409 KEY_COMMITTED`: the write landed before its answer was lost, and Dispatch will not run it again.

- A replayed 2xx (`Idempotency-Replayed: ?1`) that reads `ANSWER_NOT_KEPT`: the first answer was larger than Dispatch keeps (`answerNotKept`, which the page shares).

- A 2xx whose body gives no row: the client's own `ANSWER_LOST` (`DISPATCH_ANSWER_LOST`), since Dispatch answers a write's 2xx only after the write committed.
  A body that is cut, or that stalls past the deadline, is such a landing.
  So is a body that is no JSON whatever its type says, an empty body, and JSON that is not the row.

Every `POST` and `PATCH` of the client reads its 2xx into the row that its callers read (`dispatch-write-rows.ts`).
A row holds those members, in the forms that the route answers.
A 2xx that names the key back and gives the row is the call's answer.

A landing is no refusal and no `DispatchServiceError`, and the client never asks it again.
Its message is the one line that a session reads.
It says that the method and URL were made, and why the answer is gone.
It then says to read back what it made with `dispatch read` or the matching list.

Two routes store nothing: the round trip of a text and the related-item check.
An answer of either that is not its answer made nothing, so it is never a landing.
That holds for a 2xx with no row, a replay whose first answer Dispatch did not keep, and a `409 KEY_COMMITTED`.
Each is a `DispatchAnswerMissingError` (`ANSWER_LOST`), which says that nothing was made and that asking again is safe.

## A 2xx that names no key back

A Dispatch older than the keyed layer names no key back, and this client still writes to it.
A 2xx that names no key back is that Dispatch's answer only when it gives the row and holds what this call sent (`rowHolds`).
Each row declares the values that its answer names back (`Held`, read by `heldBy`):

- A CI job's result (`dispatch result --project`): the project that its path names, and the check, the verdict and the commit that it sent.

- An issue's result (`dispatch result --issue`): the issue that its path names, and the verdict and the commit that it sent.

- Each other write: the key or the id that its path names.
  Examples are an issue's key, a project's key, and the id of a document, an ask, a comment or a message.
  A review's version, a handoff's phase and a delivery's attempt in the path count too.
  So do a new issue's project, a new project's key and an approved plan's version from the body.

The answer must hold each such value as the call sent it, up to the white space around it that Dispatch trims.
The client does not compare a value that the call did not set: one that is absent, null or empty.
The client compares no text, because Dispatch rewrites a text that it stores: it trims it, and it folds the line ends of a body.
Six rows declare no value, and a 2xx with no key that gives one of those rows is the answer.
They are the round trip and the related-item check, an agent's message, a delivery's reply, a named version, and a document edit.

Any other 2xx is no answer that the client can take as Dispatch's to this call.
Such a 2xx names another request's key back.
Or it names no key and gives no row, or it names no key and holds another call's values.
A proxy's page, a server that is not Dispatch, or a proxy or cache that answers one write with another's gives such an answer.
So does a Dispatch that stored the write and answered without the key and without the row.
That is a build older than the key's echo, or one behind a proxy that drops the header.
A `409 KEY_COMMITTED` that names no key back, or another request's key, is no such answer either.
The write may have reached Dispatch behind what answered it, so its outcome is unknown, a `DispatchWriteUnsettledError`.
Check whether it took effect before you send it again.

## What the `dispatch` command prints

A session's `dispatch` command prints what the model reads, and the README ("The `dispatch` command") gives its exit codes.

- The call's answer: the command prints the result, and exits 0.

- A landing: the command prints the landing's one line, exits 0, and records `landed_without_answer: <code>` in its ledger entry.

- A landing of a step before the command's own write: the command fails, exits 1, and prints the line with what it means.
  For a picture upload, the command wrote nothing else, and the call run again uploads the picture as its next version.
  For the closing reason of `issue-update --status done`, the command did not send the close.
  Read the issue's messages for the reason, then close with a reason that points at it.
  The close's own `PATCH` is the command's write, so its landing exits 0.

- Dispatch's refusal: the command prints `<CODE>: <message>`, and exits 1.

- A gateway's answer or a redirect: the command prints `HTTP_<status>: ` and the message, and exits 1.

- A missing answer or an unknown outcome: the command prints the error's message, and exits 1.

- A path value that is empty: the command prints the refusal, and exits 2.
