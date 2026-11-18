// packages/envoy-client/src/write-outcome.ts
//
// The outcome of a Dispatch write, as one transition table. `DispatchClient`'s write loop
// (`dispatch-http.ts`, `#write`) reads every presentation and attempt as one `WriteEvent`, asks
// `writeStep` what follows, and does only that: settle on the answer, ask again (on the write's
// pacing, at once after a dead token, or on the rolling-release schedule after no route), or end.
// No other code decides how a write ends.
//
// The rule the table holds: once an attempt may have landed, only a 2xx or a refusal the keyed
// layer judged for the write's key (the key echoed back) settles the write. Every other way it ends
// — any other answer, a failure, the deadline, the caller's stop, running out of retries or of
// time — is `DispatchWriteUnsettledError` carrying the key. Before anything may have landed, a
// write that ends is told the event's own error: the refusal, the transport's failure, the
// credential's, the deadline's.

/** Where a write stands between its attempts. A settled write is no state: the call returns. */
export type WriteState = "nothing-sent" | "may-have-landed";

/** What one step of a write met. */
export type WriteEvent =
  /** A 2xx: Dispatch ran the write, or replayed the answer it stored under the key. */
  | "applied"
  /** A non-2xx the keyed layer judged for the key (the key echoed back) and that a repeat cannot
   *  change: `422 KEY_REUSED`, `409 KEY_COMMITTED`, a validation refusal. */
  | "judged"
  /** A non-2xx nothing judged for the key and not asked again after: a page something in
   *  Dispatch's place wrote, a refusal Dispatch's auth made before its keyed layer ran (`401`,
   *  `403 READ_ONLY_TOKEN`), a second dead token, a 1xx. */
  | "unjudged"
  /** A 3xx, `304` included. Never followed: a 301, 302 or 303 would turn the POST into a GET,
   *  and a 307 or 308 would send it again to another origin without its credential. */
  | "redirect"
  /** A 5xx: asked again under the key. It can come after Dispatch committed the write. */
  | "server-error"
  /** Dispatch's `409 KEY_IN_FLIGHT`: an earlier attempt still runs under the key. */
  | "in-flight"
  /** A 408 or 429: asked again under the key; nothing ran. */
  | "try-later"
  /** The attempt was cut off for silence (`DispatchWriteRetry.silenceMs`). */
  | "silence"
  /** A transport failure of an attempt that went out: a reset after Dispatch read the request
   *  is one, so it may have landed. */
  | "reset"
  /** A transport failure that reached no server: a refused connection, a name that did not
   *  resolve (`reachedNothing`). */
  | "unreached"
  /** The credential could not present the attempt, so nothing of it was sent. */
  | "no-credential"
  /** Dispatch's `401 BROKER_TOKEN_NOT_ACTIVE`, the first of the call, where the credential can be
   *  told its token is not live: it is presented afresh and the write sent again at once. */
  | "dead-token"
  /** An old build's `404 NOT_FOUND` "no route for …": sent again on the rolling-release schedule
   *  (`NO_ROUTE_RETRY_DELAYS_MS`). No handler ran. */
  | "no-route"
  /** The call's deadline ended an attempt in flight. */
  | "deadline-in-flight"
  /** The call's deadline ended a pause, a wait or a credential's presentation: nothing in flight. */
  | "deadline"
  /** The caller's stop ended an attempt in flight. */
  | "cancelled-in-flight"
  /** The caller's stop ended a pause, a wait or a presentation: nothing in flight. */
  | "cancelled"
  /** No ask is left after an event that asks again: the attempts or the time are spent, the
   *  Dispatch is not known to run the keyed layer (a repeat there would be a second write), the
   *  rolling-release schedule is spent, or the client asks no route again (`retryNoRoute: false`). */
  | "exhausted";

/**
 * What follows an event: `settle` (the answer is the call's: its payload, or the refusal Dispatch
 * decided), `retry` (ask again on the write's pacing), `retry-at-once` (present afresh and send
 * again now), `retry-route` (send again on the rolling-release schedule), or `end`.
 */
export type WriteAction = "settle" | "retry" | "retry-at-once" | "retry-route" | "end";

/** One cell of the table: the state the write is in after the event, and what follows. */
export interface WriteStep {
  readonly state: WriteState;
  readonly action: WriteAction;
}

/** `action` from `may-have-landed`. */
const landed = (action: WriteAction): WriteStep => ({ state: "may-have-landed", action });
/** `action` from `nothing-sent`. */
const nothing = (action: WriteAction): WriteStep => ({ state: "nothing-sent", action });

/**
 * The table: for each event, its cell from `nothing-sent` and from `may-have-landed`. Read it by
 * columns: from `may-have-landed` only `applied` and `judged` settle, and every `end` there is an
 * end in `may-have-landed`, so `DispatchWriteUnsettledError` with the key.
 */
export const WRITE_TRANSITIONS: {
  readonly [E in WriteEvent]: { readonly [S in WriteState]: WriteStep };
} = {
  applied: { "nothing-sent": nothing("settle"), "may-have-landed": landed("settle") },
  judged: { "nothing-sent": nothing("settle"), "may-have-landed": landed("settle") },
  unjudged: { "nothing-sent": nothing("end"), "may-have-landed": landed("end") },
  redirect: { "nothing-sent": nothing("end"), "may-have-landed": landed("end") },
  "server-error": { "nothing-sent": landed("retry"), "may-have-landed": landed("retry") },
  "in-flight": { "nothing-sent": landed("retry"), "may-have-landed": landed("retry") },
  "try-later": { "nothing-sent": nothing("retry"), "may-have-landed": landed("retry") },
  silence: { "nothing-sent": landed("retry"), "may-have-landed": landed("retry") },
  reset: { "nothing-sent": landed("retry"), "may-have-landed": landed("retry") },
  unreached: { "nothing-sent": nothing("retry"), "may-have-landed": landed("retry") },
  // Before anything is sent, the credential's own error is the answer: it already waited its
  // share of the deadline (`credentialDeadline`). After a possible landing, the write asks again,
  // since a later presentation's answer under the key can still settle it.
  "no-credential": { "nothing-sent": nothing("end"), "may-have-landed": landed("retry") },
  "dead-token": {
    "nothing-sent": nothing("retry-at-once"),
    "may-have-landed": landed("retry-at-once"),
  },
  "no-route": { "nothing-sent": nothing("retry-route"), "may-have-landed": landed("retry-route") },
  "deadline-in-flight": { "nothing-sent": landed("end"), "may-have-landed": landed("end") },
  deadline: { "nothing-sent": nothing("end"), "may-have-landed": landed("end") },
  "cancelled-in-flight": { "nothing-sent": landed("end"), "may-have-landed": landed("end") },
  cancelled: { "nothing-sent": nothing("end"), "may-have-landed": landed("end") },
  exhausted: { "nothing-sent": nothing("end"), "may-have-landed": landed("end") },
};

/**
 * The step `event` takes a write in `state` to. A call that writes nothing (`writes: false`, a
 * conversion such as the markdown round trip) never leaves `nothing-sent`: no attempt of it can
 * land, so it keeps its answers as they are.
 */
export function writeStep(state: WriteState, event: WriteEvent, writes: boolean): WriteStep {
  const step = WRITE_TRANSITIONS[event][state];
  return writes ? step : nothing(step.action);
}
