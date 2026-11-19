/**
 * The budget of the extension's `session_shutdown` handler, and the race its deregistration and
 * NATS drain run in: a session whose DELETE never reaches the listener reads live for the live
 * window and unresponsive for the gone horizon after it, with nothing heartbeating it, so a
 * DELETE that fails or is given up says so at warn, naming the session.
 */
import { messageFor } from "@legion/envoy-client/errors";

/** The whole of what the `session_shutdown` handler gives its deregistration and its drain. Oh
 *  My Pi abandons a shutdown handler after 2 s (`SESSION_SHUTDOWN_HANDLER_TIMEOUT_MS`), and the
 *  second this leaves is for the synchronous work ahead of the race and for the host. The
 *  shared `SHUTDOWN_SETTLE_MS` is what that path spends waiting on a registration still on the
 *  wire: `REGISTRATION_SETTLE_MS` is five times this whole deadline, so a registration slower
 *  than a second would mean no DELETE was sent at all and the record outlived this process by
 *  the gone horizon, where a DELETE that races a slow registration only may lose. The quarter
 *  of a second it spends instead leaves 750 ms here (1.75 s of the host's 2 s) for the DELETE
 *  to reach the wire. */
export const SHUTDOWN_DEADLINE_MS = 1_000;

/** A warn line of the host's logger: its message and what it names. */
export type ShutdownWarn = (
  message: string,
  context: { readonly sessionID: string; readonly detail: string }
) => void;

/**
 * Waits for the session's DELETE and the NATS drain together, or for `deadline`, whichever comes
 * first. A DELETE that fails is logged at warn with the session id and its error; one still
 * pending when `deadline` comes is given up, and logged at warn with the session id and that it
 * was still pending at the deadline (a failure it meets later is not logged again). The drain's
 * own failure is the connection's, and the drain is given up at the deadline silently: the
 * connection closes with the process.
 */
export async function deregisterAndDrain(input: {
  readonly sessionID: string;
  readonly deregistration: Promise<void>;
  readonly draining: Promise<void>;
  readonly deadline: Promise<void>;
  readonly warn: ShutdownWarn;
}): Promise<void> {
  const { sessionID, warn } = input;
  let state: "pending" | "settled" | "given up" = "pending";
  const deregistered = input.deregistration.then(
    () => {
      state = "settled";
    },
    (error: unknown) => {
      if (state === "given up") return;
      state = "settled";
      warn("envoy: this session could not be deregistered at shutdown", {
        sessionID,
        detail: messageFor(error),
      });
    }
  );
  await Promise.race([
    Promise.allSettled([deregistered, input.draining.catch(() => undefined)]),
    input.deadline,
  ]);
  if (state !== "pending") return;
  state = "given up";
  warn("envoy: this session's deregistration was given up at the shutdown deadline", {
    sessionID,
    detail: `the DELETE was still pending ${SHUTDOWN_DEADLINE_MS} ms into the shutdown, so the session reads live until the listener's live window passes`,
  });
}
