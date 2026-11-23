// The rules this workspace's TypeScript reads another service's HTTP status by. `outage` decides
// whether the same request may be answered if it is asked again: Dispatch's keyed writes
// (`retriesUnderKey`), the Dispatch and Envoy clients, the Legion daemon's client and the claim
// boot all read it here. Go's half is `httpoutage.Is` (packages/envoy/httpoutage), the same set.
// `mayHaveReachedServer` decides whether a request a hop answered may have reached the server
// behind it, which the Dispatch client, the Envoy client and the Legion daemon's client read.
// It imports nothing, so any module of the contract can read it.

/**
 * Whether `status` is a server that could not answer the request now, so the same request may be
 * answered if it is asked again: any 5xx (RFC 9110 section 15.6, the server failing to answer), a
 * 408 Request Timeout (section 15.5.9: the server did not receive the whole request in time, and
 * the client may repeat it) and a 429 Too Many Requests (RFC 6585 section 4: try later). Every
 * other status is the server's answer, which asking again repeats. A reader whose own contract
 * says otherwise for one of them says so where it reads it: the Envoy client sends no 429 again,
 * since the listener's 429 is `sender_rate_limited`, whose sentence the agent reads.
 */
export function outage(status: number): boolean {
  return status >= 500 || status === 408 || status === 429;
}

/**
 * Whether a request that a hop in front of the server (a proxy or gateway that answers in its
 * place) answered with `status` may have reached the server first, so a write it carried may have
 * taken effect. After a 5xx it may have: a hop sends one when the server failed, timed out or
 * dropped the connection, possibly once the server had the request. After a 408 (the hop's own
 * timeout on the client's request), a 429 (its own rate limit) or any other status (its own
 * refusal) it did not: a hop sends each before it forwards anything. So it holds of exactly the
 * 5xx: every status it holds of is an outage (`outage`), and the 408 and the 429 are the outages
 * that reached nothing. A server's own answer reached it whatever its status; a reader asks this
 * only of an answer a hop may have written.
 */
export function mayHaveReachedServer(status: number): boolean {
  return status >= 500;
}
