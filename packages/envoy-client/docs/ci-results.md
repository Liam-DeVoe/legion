# A CI job's result, and its read

These two calls are part of the `dispatch` command, which the package's
[README](../README.md) ("The `dispatch` command") describes.

`dispatch result --project <KEY> --check <id> --verdict pass|fail|could_not_run --commit <sha>`
is a CI job's record of its result of a check of the spec files, and the one call that is no
session's (`ownIdentityResult` in `dispatch-cli.ts`). It reads no `DISPATCH_HOST`, no envoy.json
and no session state, and it asks no AskGate and no listener. A refusal is written to a session's
ledger only where the environment names a session id, as every refusal of the command is. The job
runs it under `agent-secrets result-grant -- …`, which exchanges the run's token at the secrets
broker for the airlock's placeholder of a result grant and gives it to the command in
`DISPATCH_RESULTS_TOKEN`, beside the airlock proxy's settings. Dispatch is `DISPATCH_URL`, and the
credential is that placeholder alone (`ownIdentityDispatch` in `dispatch-config.ts`,
`airlockCredential` in `airlock-credential.ts`): the write carries it as its one `Authorization`
header, `DPoP <placeholder>`, and no proof of its own, and the airlock proxy puts the run's
results token and a proof of its own key in its place. The command reads no bearer and no broker
variable, so it trades nothing, `AGENT_SECRETS_MACHINE_IDENTITY` included, and holds no Dispatch
credential. With no placeholder it sends nothing and exits 2, naming `agent-secrets
result-grant`. The body is `{commit, check, verdict, observed}` and nothing else, and the write
sends that one `POST` and nothing else to Dispatch's host. A result keeps 2000
characters of `observed` (`SPEC_CHECK_OBSERVED_MAX`), so the command sends at most that: a
longer text goes as its first 500 characters (`SPEC_CHECK_OBSERVED_START`), a line that says the
middle was cut (`SPEC_CHECK_OBSERVED_CUT_NOTE`) and its last characters
(`SPEC_CHECK_OBSERVED_END`), since a test's output names the test at its start and says why it
failed at its end, and a test log of any size still writes its result. A NUL, which Dispatch
cannot store, goes as U+FFFD, and so does each byte of a file that is not UTF-8. It prints the
stored result as JSON and exits 0. A write that landed with its answer lost prints
`{"landed_without_answer":"<code>"}` and exits 0: the result is stored, and that includes a 201
that names the request's key back, as Dispatch's does, whose body gives no stored result (cut,
stalled, no JSON, empty, or JSON of another shape: `ANSWER_LOST`), and a `409 KEY_COMMITTED` that
names it back. Dispatch's keyed layer names the request's key back on every answer of a write
route, so only such an answer is the job's stored result. Each other 2xx, one that names another
request's key back or names none, prints `write_outcome_unknown`, whatever it holds: the command
compares nothing of it with what the call sent. So does a `409 KEY_COMMITTED` that does not name
the key back. Such an answer may be a proxy's page, a server that is not Dispatch, a proxy or a
cache that answers one write with another's, or a proxy that drops the header, so the result may
be stored, and a second run is safe. [`write-answers.md`](write-answers.md) gives these rules for
every write. No request of the command follows a redirect: a redirect is a refusal of whatever
answered. The line of a redirect from Dispatch's place names the origin that the redirect points
at: its scheme and host, never its path or query, and nothing of the answer's body, and the
command exits 1. A write that
reached Dispatch with no answer that says whether it stored the result (`writeOutcomeUnknown`)
prints `{"write_outcome_unknown":"<code>"}`, the code of its last failure such as `ECONNRESET` or
`HTTP_200`, and exits 1: the result may be stored, and a second run is safe, because Dispatch keeps
each result. The command refuses an empty `--project`, `--check` or `--commit` before any request,
with exit 2, and names the flag. An empty variable of the step gives such a value.

The airlock proxy can refuse the write itself, and then nothing reached Dispatch. A tunnel it
refuses at the CONNECT reaches the command as Bun's `ERR_PROXY_TUNNEL`, whose message carries the
status line alone: a 407 for a grant that ended, or a proxy password that names none, ends the
write at once and prints `AIRLOCK_TUNNEL_REFUSED: the airlock proxy refused the connection (407
Proxy Authentication Required); nothing was sent to Dispatch`, and a 503 is a connection that
never opened, asked again within the write's bound as a refused connection is, and then printed
the same way. An answer the proxy gives itself inside an open connection carries
`Airlock-Forwarded: false` and its `Airlock-Code`: it prints `<code>: <the proxy's words>;
nothing was stored`, with each copy of the placeholder in the words redacted, and only its
`503 GRANT_UNREADABLE` is asked again within the bound. Each exits 1, and none is a
`write_outcome_unknown`, but where an earlier attempt of the same write may have reached Dispatch,
which leaves its outcome unknown. A call that Dispatch's check of the run's token
(`429 BROKER_TOKEN_RATE_LIMITED`, `503 BROKER_UNAVAILABLE`, `tokenNotChecked`) turns away before
anything is stored is asked again after the answer's `Retry-After`, or a second. Every CI write
reaches Dispatch from a broker pod's address, so the jobs that write at one time share that
check's bucket of the address. The whole call has one bound, 60 s from its start: one timeout,
made where the call starts, is the signal every await of it runs under (`ResultBound`). The write
client's deadline is that signal, its asks again under the key and the command's rounds start no
later than 7 s before its end, a reserve greater than the 5 s Dispatch's check of a token waits on
the broker, and a call turned away until then prints which turned it away with the last answer,
its code named, and exits 1, with no `write_outcome_unknown`: only an attempt that may have
reached a handler prints that line. An attempt the deadline ends before it is sent reached
nothing.

`dispatch results --project` prints its lines and `--json` by the same rule of a runner's
log as the command's own output. The
stored result names the run as its recorder and no person, so the job's log holds no person's
address. It prints Dispatch's refusal as `<CODE>: <message>` and exits 1, such as
`SPEC_FILES_REFUSED` with the first problem of spec files the graph logic refuses. A refusal of
its arguments, or of its environment, sends nothing and exits 2. `--observed`
text that opens with `-`, such as a test's `--- FAIL` line, goes as `--observed=<text>` or through
`--observed-file`. A GitHub runner reads workflow commands from the job's log, so each text the
command did not write (an argument, a variable, a file's name, an answer of the airlock proxy or of
Dispatch) prints by the rule of Go's `runnerlog.Printable` (`runner-log.ts`): each control
character, format character, line or paragraph separator as its escape, `\n` or `\u0085`, and
the `[` of every `##[` as `\x5b`, or as `\u005b` inside JSON, so no such text starts a line of its
own or names a workflow command. A refusal that starts with `::` has that first `:` written as
`\x3a`.
`dispatch results --project <KEY> --commit <sha> [--check <id>]` is a session's read of those
results, one line each, newest first, or the stored results as JSON with `--json`.
