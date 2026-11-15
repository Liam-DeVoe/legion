/**
 * Two of the three waits the `open-asks` hook spends in series, in a module of its own so a test
 * can assert them: `session-hook.ts` runs its mode off `process.argv` at import, so importing that
 * file to read a constant would run a hook.
 *
 * On the slow path the hook waits for the jj chain (`JJ_BASE_CHAIN_TIMEOUT_MS`), then for the
 * session's `!` token command, which `sessionDispatchSettings` runs bounded by envoy-client's
 * `COMMAND_TIMEOUT_MS`, then for the Dispatch read (`OPEN_ASKS_TIMEOUT_MS`). All three are bounded
 * by something outside this package: Claude Code kills the hook at the `timeout`
 * `hooks/hooks.json` declares for it (20 s for `open-asks`). The hook appends `DISPATCH_HOST` and
 * `ENVOY_CONFIG_FILE` to the env file before any of the three, so what a kill costs the session is
 * the jj overlay's `export JJ_CONFIG=` line and the open-ask summary. So the sum of the three is
 * what has to stay inside that timeout, and `tests/hooks-timeout.test.ts` asserts it against the
 * number `hooks.json` itself carries.
 */

/** The bound on the Dispatch read that prints the session's open asks. A Dispatch that does not
 *  answer inside it costs the session the summary and nothing else. */
export const OPEN_ASKS_TIMEOUT_MS = 3_000

/** The bound on resolving jj's own user config chain for the overlay: 3 s of jj, then up to 10 s
 *  of the token command and 3 s of the Dispatch read, still leave the session its summary. Not
 *  `JJ_CONFIG_PATH_TIMEOUT_MS`, which is the Oh My Pi lane's bound inside the 30 s that harness
 *  gives its session-start handler. A jj that never answers costs the session the export and
 *  nothing else. */
export const JJ_BASE_CHAIN_TIMEOUT_MS = 3_000
