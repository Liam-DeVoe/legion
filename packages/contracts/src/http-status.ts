// The one rule this workspace's TypeScript reads another service's HTTP status by when it decides
// whether the same request may be answered if it is asked again: Dispatch's keyed writes
// (`retriesUnderKey`), the Dispatch and Envoy clients, the Legion daemon's client and the claim
// boot all read it here. Go's half is `httpoutage.Is` (packages/envoy/httpoutage), the same set.
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
