/**
 * What each tool call of this process spent waiting for a daemon that was not serving, which is
 * the one thing the worker's own stall watch must not count against that call
 * (`src/stall-watchdog.ts`, R1: "the outage charges the worker nothing … no action of its stall
 * watchdog").
 *
 * A `legion` tool call that waits out an hour of daemon restart is a call that has run for an
 * hour with no step, which is exactly the shape the watch judges — so without this record a
 * daemon outage would end the very turn that was waiting for the daemon to come back. The
 * watch therefore judges a call by the time it was open **less** the time that call spent
 * waiting, and the daemon's own backstop floor is raised by the same hour
 * (`packages/daemon/internal/config/stallbackstop.go`, StallLadderFor), so a paused watch still
 * finishes first.
 *
 * Each wait is the waiting call's alone, by its tool call id. A shell call stuck beside it in the
 * same turn is stuck for its own reasons, and so is any call open while a wait no tool call owns
 * runs in the background — the boot's, a role regain's `claims/ready`, a turn's outcome — so
 * none of those pauses anything, and the watch still reaches every call that is not itself
 * waiting.
 *
 * It is module state and not a field, because the two sides never meet: the wait is recorded in
 * `src/daemon-client.ts`, by whichever call is waiting, and read by the watchdog the plugin
 * built at `session_start`. Both run in one bundle and one process (pi-legion's), so one module
 * instance serves both; a `task` subagent's extension instance shares it, as it shares every
 * other process-wide record here. The module imports nothing.
 *
 * Every moment here is read from the caller's own clock — the retry's `now`, the watchdog's
 * `now` — which in production is `Date.now` on both sides.
 */

/** One window a tool call waited in; `to` is undefined while the wait is still running. */
interface DaemonWait {
  readonly toolCallId: string;
  readonly from: number;
  to: number | undefined;
}

/** How many waits the record holds. Nothing clears it for the life of the process, and a call
 * open now cares only about its own waits inside its own window, so the oldest ended wait is
 * dropped once the record is full; a wait that has not ended is never dropped, since that is
 * the one a call open now may be waiting on. */
const MOST_RECORDED_WAITS = 32;

const waits: DaemonWait[] = [];

/** Records a wait of `toolCallId` that began at `from`, and answers the function that ends it.
 * Calling that function twice, or never, is safe: an unended wait reads as running up to
 * whatever moment it is asked about, which is what a process ended mid-wait leaves behind. */
export function daemonWaitBegan(toolCallId: string, from: number): (endedAt: number) => void {
  const wait: DaemonWait = { toolCallId, from, to: undefined };
  waits.push(wait);
  if (waits.length > MOST_RECORDED_WAITS) {
    const oldestEnded = waits.findIndex((recorded) => recorded.to !== undefined);
    if (oldestEnded !== -1) waits.splice(oldestEnded, 1);
  }
  return (endedAt: number) => {
    wait.to = Math.max(endedAt, from);
  };
}

/** How much of `[from, to]` the call `toolCallId` spent waiting for a daemon that was not
 * serving. One call can wait more than once, and two of its waits that overlap stopped it once,
 * not twice, so the windows are merged rather than summed. */
export function daemonWaitMs(toolCallId: string, from: number, to: number): number {
  if (to <= from) return 0;
  const clipped = waits
    .filter((wait) => wait.toolCallId === toolCallId)
    .map((wait) => ({
      from: Math.max(wait.from, from),
      to: Math.min(wait.to ?? to, to),
    }))
    .filter((wait) => wait.to > wait.from)
    .sort((left, right) => left.from - right.from);
  let waited = 0;
  let mergedTo = from;
  for (const wait of clipped) {
    const start = Math.max(wait.from, mergedTo);
    if (wait.to > start) waited += wait.to - start;
    mergedTo = Math.max(mergedTo, wait.to);
  }
  return waited;
}

/** Forgets every recorded wait. The one test seam: nothing in a running session clears the
 * record, which is why it is bounded above. */
export function forgetDaemonWaitsForTests(): void {
  waits.length = 0;
}
