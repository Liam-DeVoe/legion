/** What a pass needs beside the start's wait for its broker token: the dial, its subscribes and
 *  the registration (`TRADE_DEADLINE_MS` is the default pass deadline less this). */
export const PASS_REST_MS = 10_000;

/**
 * How long a session's start waits for its broker token inside a pass of `passDeadlineMs`: the
 * pass less the 10 s the rest of it needs (`PASS_REST_MS`), or half the pass where that rest does
 * not fit. A short pass deadline (one a short `ENVOY_HEARTBEAT_MS` allows, such as 7.5 s) then
 * still gives the trade a share of it, rather than 0 ms and a start that fails "within 0 s" at
 * once. `ready` waits the smaller of this and its own trade deadline (`ReadyOptions.boundMs`).
 */
export function startTradeBoundMs(passDeadlineMs: number): number {
  return Math.max(passDeadlineMs - PASS_REST_MS, passDeadlineMs / 2);
}
