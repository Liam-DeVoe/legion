import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import path from "node:path";
import {
  type LegionTake,
  LegionTakeResponse,
  LegionTaskResponse,
} from "@legion/contracts/legion-api";
import type { RelayTaskValue } from "@legion/envoy-client/consumer-delivery";
import {
  type ClaimCaller,
  DAEMON_ANSWER_TIMEOUT_MS,
  DAEMON_RESEND_FIRST_DELAY_MS,
  DAEMON_RESEND_MAX_DELAY_MS,
  LegionDaemonApiError,
} from "./daemon-client";
import { createTaskTake } from "./task-take";

/** A response the daemon's own golden test wrote (`packages/daemon/internal/api`). */
function daemonFixture(name: string): unknown {
  return JSON.parse(
    readFileSync(path.resolve(import.meta.dir, "../../contracts/fixtures/daemon-api", name), "utf8")
  );
}

// The daemon's answers, as its golden tests wrote them and the client reads them.
const ACCEPTED = LegionTakeResponse.parse(daemonFixture("take-accepted.json"));
const REFUSED = LegionTakeResponse.parse(daemonFixture("take-refused.json"));
const DROPPED = LegionTakeResponse.parse(daemonFixture("take-dropped.json"));
const NOTHING_HELD = LegionTaskResponse.parse(daemonFixture("task.json"));
const TEXT = ACCEPTED.outcome === "accepted" ? ACCEPTED.text : "";

const caller: ClaimCaller = {
  claimToken: "legion-legion-legion-209-implementer",
  sessionId: "ses_implementer_209",
  secret: "U2VjcmV0",
  generation: 3,
};
const EPOCH = "2026-11-13T16:21:44.123456789Z";

/** A value the daemon read or relayed: task `number` at `revision` of the bucket's epoch. */
const value = (number: number, revision: number, epoch = EPOCH) => ({ number, epoch, revision });

/** A task frame the listener relayed for this claim. */
const put = (number: number, revision: number, claim = caller.claimToken): RelayTaskValue => ({
  claim,
  epoch: EPOCH,
  revision,
  op: "put",
  task: number,
});

/** What one attempt of a request meets: the daemon's answer, a failure, no answer at all, in
 * which case the attempt waits until its bound aborts it, or an answer that arrives when `late`
 * settles, whether or not the attempt was aborted before. */
type Attempt<T> =
  | { readonly answer: T }
  | { readonly fail: Error }
  | { readonly silent: true }
  | { readonly late: Promise<T> };

interface Call {
  readonly route: "task-seen" | "take";
  readonly body: Record<string, unknown>;
}

/** Lets every promise queued so far settle. */
async function flush(): Promise<void> {
  for (let turn = 0; turn < 50; turn += 1) await Promise.resolve();
}

/**
 * The take over a fake daemon client and a fake clock. The daemon answers each take with the next
 * attempt `takes` holds (a refusal, by default, as a hold would answer it), and each seen report
 * with the next `seens` holds (204, by default). `advance` moves the clock, firing every wait it
 * passes; a wait that fired stops listening for its abort, as `timerWait` does, so a later abort
 * of its signal takes no other wait off the clock. `waits` is every wait between two attempts of
 * one request, in the order they were made.
 */
function rig() {
  const calls: Call[] = [];
  const takes: Attempt<LegionTake>[] = [];
  const seens: Attempt<undefined>[] = [];
  const ran: string[] = [];
  const marks: boolean[] = [];
  const warned: string[] = [];
  const waits: number[] = [];
  let now = 0;
  const timers: { readonly due: number; readonly fire: () => void }[] = [];

  const attempt = <T>(next: Attempt<T> | undefined, fallback: T, signal?: AbortSignal) => {
    if (next === undefined) return Promise.resolve(fallback);
    if ("answer" in next) return Promise.resolve(next.answer);
    if ("fail" in next) return Promise.reject(next.fail);
    if ("late" in next) return next.late;
    const { promise, reject } = Promise.withResolvers<T>();
    signal?.addEventListener("abort", () => reject(signal.reason), { once: true });
    return promise;
  };

  const take = createTaskTake({
    caller,
    daemon: {
      taskSeen: (input, options) => {
        calls.push({ route: "task-seen", body: { ...input } });
        return attempt(seens.shift(), undefined, options?.signal);
      },
      take: (input, options) => {
        calls.push({ route: "take", body: { ...input } });
        return attempt(takes.shift(), REFUSED, options?.signal);
      },
    },
    run: (text) => ran.push(text),
    setPending: (pending) => marks.push(pending),
    log: { info: () => undefined, warn: (message) => warned.push(message) },
    wait: (ms, signal) => {
      const { promise, resolve } = Promise.withResolvers<boolean>();
      if (signal.aborted) {
        resolve(false);
        return promise;
      }
      if (ms !== DAEMON_ANSWER_TIMEOUT_MS) waits.push(ms);
      const stop = () => {
        timers.splice(timers.indexOf(timer), 1);
        resolve(false);
      };
      const timer = {
        due: now + ms,
        fire: () => {
          signal.removeEventListener("abort", stop);
          resolve(true);
        },
      };
      timers.push(timer);
      signal.addEventListener("abort", stop, { once: true });
      return promise;
    },
  });

  const advance = async (ms: number): Promise<void> => {
    const until = now + ms;
    for (;;) {
      await flush();
      timers.sort((a, b) => a.due - b.due);
      const next = timers[0];
      if (next === undefined || next.due > until) break;
      timers.shift();
      now = next.due;
      next.fire();
    }
    now = until;
    await flush();
  };

  const routed = (route: Call["route"]) =>
    calls.filter((call) => call.route === route).map((call) => call.body);

  return { take, calls, takes, seens, ran, marks, warned, waits, advance, routed };
}

const body = (number: number, revision: number) => ({
  ...caller,
  number,
  epoch: EPOCH,
  revision,
});

test("reports a value seen while the host is busy, and asks only once the run ends", async () => {
  const { take, routed, advance, takes } = rig();
  take.ready();
  take.runStarted();
  take.read({ task: value(4, 12), finished: 3 });
  await advance(0);

  expect(routed("task-seen")).toEqual([body(4, 12)]);
  expect(routed("take")).toEqual([]);

  takes.push({ answer: ACCEPTED });
  expect(take.runEnded(false)).toBeUndefined();
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12)]);
});

test("asks no take before the claim's ready has answered, and once after it", async () => {
  const { take, routed, advance } = rig();
  take.read({ task: value(4, 12), finished: 3 });
  await advance(0);
  expect(routed("task-seen")).toEqual([body(4, 12)]);
  expect(routed("take")).toEqual([]);

  take.ready();
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12)]);
});

test("asks once per revision: the same value read and relayed again is neither reported nor asked again", async () => {
  const { take, routed, advance } = rig();
  take.ready();
  take.read({ task: value(4, 12), finished: 3 });
  take.relayed(put(4, 12));
  await advance(0);
  take.read({ task: value(4, 12), finished: 3 });
  take.relayed(put(4, 12));
  await advance(0);

  expect(routed("task-seen")).toEqual([body(4, 12)]);
  expect(routed("take")).toEqual([body(4, 12)]);
});

test("asks nothing again for the same value relayed after a refusal, and once at the hold-end's new revision, running the accepted text", async () => {
  const { take, routed, advance, takes, ran, marks } = rig();
  take.ready();
  take.relayed(put(4, 12));
  await advance(0);
  // The take was refused (a hold stands, `take-refused.json`), so the task's turn is not pending.
  expect(routed("take")).toEqual([body(4, 12)]);
  expect(marks).toEqual([true, false]);

  take.relayed(put(4, 12));
  take.read({ task: value(4, 12), finished: 3 });
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12)]);

  // The hold ended: the daemon wrote the value again, a new revision, which is the only signal.
  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 13));
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12), body(4, 13)]);
  expect(ran).toEqual([TEXT]);
  // The mark stays until the task's turn starts.
  expect(marks).toEqual([true, false, true]);
  take.runStarted();
  expect(marks).toEqual([true, false, true, false]);
  expect(take.runEnded(false)).toBe(4);
});

test("holds the task-turn mark from the registration's value, before the ready and the role claim, until the task's turn starts", async () => {
  const { take, marks, takes, advance } = rig();
  take.read({ task: value(4, 12), finished: 3 });
  expect(marks).toEqual([true]);

  takes.push({ answer: ACCEPTED });
  take.ready();
  await advance(0);
  expect(marks).toEqual([true]);
  take.runStarted();
  expect(marks).toEqual([true, false]);
});

test("takes during a compaction, which leaves the host idle, and the first run after it is the task's turn", async () => {
  const { take, routed, takes, ran, advance } = rig();
  take.ready();
  // A run ends, and the host compacts in its idle maintenance: no run is going, so the host is
  // idle for the take, and the text waits for the compaction (the host's own form, `run`).
  take.runStarted();
  take.runEnded(false);
  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 12));
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12)]);
  expect(ran).toEqual([TEXT]);

  take.runStarted();
  expect(take.runEnded(false)).toBe(4);
  // A run after the task's names no task.
  take.runStarted();
  expect(take.runEnded(false)).toBeUndefined();
});

test("a provider error's end, the wait's end, and exactly one new take at the value the daemon writes again", async () => {
  const { take, routed, takes, ran, advance } = rig();
  take.ready();
  takes.push({ answer: ACCEPTED });
  take.read({ task: value(4, 12), finished: 3 });
  await advance(0);
  take.runStarted();
  // The turn ended in a provider error, which its report names beside the number: the daemon
  // keeps the task under its number and waits before it writes the value again.
  expect(take.runEnded(true)).toBe(4);

  // Nothing new while the wait runs: the same value read or relayed again asks nothing.
  take.relayed(put(4, 12));
  take.read({ task: value(4, 12), finished: 3 });
  await advance(30_000);
  expect(routed("take")).toEqual([body(4, 12)]);

  // The wait ended: the daemon wrote the value again, and the worker takes it once.
  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 13));
  take.read({ task: value(4, 13), finished: 3 });
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12), body(4, 13)]);
  expect(ran).toEqual([TEXT, TEXT]);
  take.runStarted();
  expect(take.runEnded(false)).toBe(4);
});

// The daemon finishes a current claim's task on a report naming its number with no provider
// error, but it may write the same number's value again before that report lands (a daemon
// start writes every owed value again), and it answers a take of the accepted, unfinished number
// with the same text. The take holds the number finished from the end of the run whose report
// carries no provider error, so that newer revision is owed nothing: no take, no second run.
test("a task's turn whose report carries no provider error finishes it at the run's end: a newer revision of its number, written before the report lands, is neither taken nor run again", async () => {
  const { take, routed, takes, ran, advance, marks } = rig();
  take.ready();
  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 12));
  await advance(0);
  take.runStarted();
  expect(take.runEnded(false)).toBe(4);

  // The daemon would answer a take of the newer revision with the same text.
  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 13));
  take.read({ task: value(4, 13), finished: 3 });
  await advance(0);

  expect(routed("take")).toEqual([body(4, 12)]);
  expect(ran).toEqual([TEXT]);
  expect(marks.at(-1)).toBe(false);
});

// A report with a provider error finishes nothing: the daemon takes the task back, and the worker
// takes the number again at the revision the daemon writes, and runs it once more.
test("a task's turn whose report carries a provider error leaves it owed: the newer revision of its number is taken and runs once", async () => {
  const { take, routed, takes, ran, advance } = rig();
  take.ready();
  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 12));
  await advance(0);
  take.runStarted();
  expect(take.runEnded(true)).toBe(4);

  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 13));
  await advance(0);

  expect(routed("take")).toEqual([body(4, 12), body(4, 13)]);
  expect(ran).toEqual([TEXT, TEXT]);
  take.runStarted();
  expect(take.runEnded(false)).toBe(4);
  take.relayed(put(4, 14));
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12), body(4, 13)]);
  expect(ran).toEqual([TEXT, TEXT]);
});

// R2 and c2: each value is reported seen, idle or not. While the run-end mark stands, a value
// naming the marked number is reported seen like any other; only its take waits.
test("a value naming the marked number is reported seen while the mark stands, and only its take waits", async () => {
  const { take, routed, takes, ran, advance } = rig();
  take.ready();
  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 12));
  await advance(0);
  take.runStarted();
  expect(take.runEnded(false)).toBe(4);

  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 13));
  take.read({ task: value(4, 14), finished: 3 });
  await advance(0);

  expect(routed("task-seen")).toEqual([body(4, 12), body(4, 13), body(4, 14)]);
  expect(routed("take")).toEqual([body(4, 12)]);
  expect(ran).toEqual([TEXT]);
});

// The mark lasts only until the turn's report is answered. Here the daemon refuses it with a 4xx,
// which applied nothing: the record still owes the number, so the newer revision it wrote while
// the report was on its way, reported seen and not taken while the mark stood, is taken once the
// refusal comes, as a number the record owes, and its text runs once. That turn's end marks the
// number again, so a revision after it runs nothing.
test("a report refused with a 4xx clears the mark, and the newer revision of n is then taken and runs once", async () => {
  const { take, routed, takes, ran, advance, marks } = rig();
  take.ready();
  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 12));
  await advance(0);
  take.runStarted();
  expect(take.runEnded(false)).toBe(4);

  take.relayed(put(4, 13));
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12)]);
  expect(marks).toEqual([true, false]);

  takes.push({ answer: ACCEPTED });
  take.reportRefused(4);
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12), body(4, 13)]);
  expect(ran).toEqual([TEXT, TEXT]);
  // The task-turn mark is up again from the take until the task's turn starts.
  expect(marks).toEqual([true, false, true]);

  take.runStarted();
  expect(marks).toEqual([true, false, true, false]);
  expect(take.runEnded(false)).toBe(4);
  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 14));
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12), body(4, 13)]);
  expect(ran).toEqual([TEXT, TEXT]);
});

// The run-end mark is kept apart from the number a reading says the record finished: a refusal
// of the report clears the mark alone, and never lowers what a reading said.
test("a refused report clears the run-end mark alone: a number a reading said finished stays finished", async () => {
  const { take, routed, takes, ran, advance } = rig();
  take.ready();
  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 12));
  await advance(0);
  take.runStarted();
  expect(take.runEnded(false)).toBe(4);

  take.read({ task: null, finished: 4 });
  take.reportRefused(4);
  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 13));
  await advance(0);

  expect(routed("take")).toEqual([body(4, 12)]);
  expect(ran).toEqual([TEXT]);
});

// The race: the take is asked on an idle host and the daemon accepts
// it, but a run no take started (a background job's result, a reminder) begins before the answer
// comes back. The daemon gives that run to the take, as the first to start after its accept; the
// worker holds the text, since nothing is sent into a busy worker and a follow-up sent now would
// run inside that run, and reports the run's end with no number, which gives the take its turn
// back on the daemon. The held text then starts the next run, the task's turn on both sides, and
// that run's end names the number.
test("a take accepted while a run it did not start is going holds its text: that run's end names no task, and the text starts the next run, whose end names the task", async () => {
  const { take, takes, ran, advance, marks } = rig();
  take.ready();
  const answer = Promise.withResolvers<LegionTake>();
  takes.push({ late: answer.promise });
  take.relayed(put(4, 12));
  await advance(0);
  // A run starts after the daemon decided the take and before its answer arrives.
  take.runStarted();
  answer.resolve(ACCEPTED);
  await advance(0);
  // The text is held: no follow-up goes into the running run, and the turn is still pending.
  expect(ran).toEqual([]);
  expect(marks).toEqual([true]);

  // That run's end names no task, and the text goes to the now idle host.
  expect(take.runEnded(false)).toBeUndefined();
  expect(ran).toEqual([TEXT]);
  // The run it starts is the task's turn, and only it names the number.
  take.runStarted();
  expect(marks).toEqual([true, false]);
  expect(take.runEnded(false)).toBe(4);
  take.runStarted();
  expect(take.runEnded(false)).toBeUndefined();
  expect(ran).toEqual([TEXT]);
});

// The lost-answer variant: the daemon accepted the take, but its answer was lost (here a reset
// connection), and a run started while the take waited to be sent again. The daemon answers a
// take repeated for the number it accepted with the same text, also after it gave that run to the
// take, so the re-sent take is answered `accepted` during the run: the text is held, that run's
// end names no task, and the text runs as the next run, once.
test("a take whose accepted answer was lost is answered again during a run that started meanwhile: the text is held, and runs next, once", async () => {
  const { take, takes, ran, advance, routed } = rig();
  take.ready();
  takes.push({ fail: transport("ECONNRESET") }, { answer: ACCEPTED });
  take.relayed(put(4, 12));
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12)]);

  // A run starts while the take waits to be sent again, and the re-sent take is answered then.
  take.runStarted();
  await advance(DAEMON_RESEND_FIRST_DELAY_MS);
  expect(routed("take")).toEqual([body(4, 12), body(4, 12)]);
  expect(ran).toEqual([]);

  expect(take.runEnded(false)).toBeUndefined();
  expect(ran).toEqual([TEXT]);
  take.runStarted();
  expect(take.runEnded(false)).toBe(4);
  take.runStarted();
  expect(take.runEnded(false)).toBeUndefined();
  expect(ran).toEqual([TEXT]);
});

test("a take sent again for a number already accepted runs the text once", async () => {
  const { take, takes, ran, advance } = rig();
  take.ready();
  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 12));
  await advance(0);
  // A newer revision of the same number before the turn started: the daemon answers the take of
  // an accepted number with the same text, which is already on its way.
  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 13));
  await advance(0);
  expect(ran).toEqual([TEXT]);
});

// The daemon answers a take of the number it accepted with the same text until that task
// finishes, also once the task's turn has started: a newer revision of the same number, asked
// before the turn and answered during it, is that same task, which is already running.
test("a take of a newer revision of the accepted number, answered during that task's own turn, runs nothing more", async () => {
  const { take, takes, ran, advance, routed } = rig();
  take.ready();
  takes.push({ answer: ACCEPTED });
  take.relayed(put(4, 12));
  await advance(0);
  expect(ran).toEqual([TEXT]);

  const again = Promise.withResolvers<LegionTake>();
  takes.push({ late: again.promise });
  take.relayed(put(4, 13));
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12), body(4, 13)]);
  // The task's turn starts, and the repeated take's answer arrives during it.
  take.runStarted();
  again.resolve(ACCEPTED);
  await advance(0);

  expect(take.runEnded(false)).toBe(4);
  expect(ran).toEqual([TEXT]);
  take.runStarted();
  expect(take.runEnded(false)).toBeUndefined();
  expect(ran).toEqual([TEXT]);
});

test("a dropped take clears the mark, and neither the delete it made nor a stale relay of the dropped value asks again", async () => {
  const { take, routed, takes, marks, advance } = rig();
  take.ready();
  takes.push({ answer: DROPPED });
  take.relayed(put(4, 12));
  await advance(0);
  expect(marks).toEqual([true, false]);

  // The delete the drop made, then a stale relay of the dropped value at its old revision.
  take.relayed({ claim: caller.claimToken, epoch: EPOCH, revision: 13, op: "delete" });
  take.relayed(put(4, 12));
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12)]);
});

test("a value naming a finished number, or another claim's, asks nothing", async () => {
  const { take, routed, advance, marks } = rig();
  take.ready();
  take.read({ task: value(3, 9), finished: 3 });
  take.relayed(put(5, 2, "legion-legion-legion-210-implementer"));
  take.read(NOTHING_HELD);
  await advance(0);

  expect(routed("take")).toEqual([]);
  expect(routed("task-seen")).toEqual([body(3, 9)]);
  expect(marks).toEqual([]);
});

test("a value of another epoch replaces the held one, whatever its revision", async () => {
  const { take, routed, advance } = rig();
  take.ready();
  take.relayed(put(4, 12));
  await advance(0);
  take.read({ task: value(4, 1, "2026-11-14T08:00:00.5Z"), finished: 3 });
  await advance(0);

  expect(routed("take")).toEqual([body(4, 12), { ...body(4, 1), epoch: "2026-11-14T08:00:00.5Z" }]);
});

/** A failure of the transport, as Bun's fetch raises it, carrying `code`. */
const transport = (code: string): Error =>
  Object.assign(new TypeError(`${code}: the request reached no daemon`), { code });

const uncoded = (status: number): Error =>
  new LegionDaemonApiError("POST", "/legion/v1/claims/take", status, undefined, "upstream failed");

for (const [name, attempt] of [
  ["a refused connection", { fail: transport("ECONNREFUSED") }],
  ["a reset connection", { fail: transport("ECONNRESET") }],
  ["a closed connection", { fail: transport("ConnectionClosed") }],
  ["an unreachable host", { fail: transport("EHOSTUNREACH") }],
  ["an unreachable network", { fail: transport("ENETUNREACH") }],
  ["a connect timeout", { fail: transport("ETIMEDOUT") }],
  ["a connect timeout Bun names ConnectionTimeout", { fail: transport("ConnectionTimeout") }],
  // Bun's fetch gives a read that timed out no code, only the name (Bun 1.4.4).
  ["a read timeout", { fail: new DOMException("The operation timed out.", "TimeoutError") }],
  ["a DNS failure", { fail: transport("ENOTFOUND") }],
  ["no answer within 5 minutes", { silent: true }],
  ["a 502 with no code", { fail: uncoded(502) }],
  ["a 503 with no code", { fail: uncoded(503) }],
  ["a 504 with no code", { fail: uncoded(504) }],
] as const) {
  test(`a take that meets ${name} is sent again, the same request, with growing waits and no limit, until it is answered`, async () => {
    const { take, routed, takes, ran, waits, advance, marks } = rig();
    take.ready();
    const failures = 12;
    for (let n = 0; n < failures; n += 1) takes.push(attempt);
    takes.push({ answer: ACCEPTED });
    take.relayed(put(4, 12));
    // Long enough for every attempt's bound and every wait between them.
    for (let n = 0; n <= failures; n += 1) await advance(DAEMON_ANSWER_TIMEOUT_MS);

    expect(routed("take")).toEqual(Array(failures + 1).fill(body(4, 12)));
    expect(waits).toEqual(
      Array.from({ length: failures }, (_, n) =>
        Math.min(DAEMON_RESEND_FIRST_DELAY_MS * 2 ** n, DAEMON_RESEND_MAX_DELAY_MS)
      )
    );
    expect(ran).toEqual([TEXT]);
    // Sending again is the same ask: the mark held throughout, until the turn starts.
    expect(marks).toEqual([true]);
  });

  test(`a seen report that meets ${name} is sent again, the same request, until it is answered`, async () => {
    const { take, routed, seens, advance } = rig();
    seens.push(attempt, attempt, attempt);
    take.relayed(put(4, 12));
    for (let n = 0; n <= 3; n += 1) await advance(DAEMON_ANSWER_TIMEOUT_MS);

    expect(routed("task-seen")).toEqual(Array(4).fill(body(4, 12)));
  });
}

for (const [name, refusal] of [
  ["a 409 with a code", new LegionDaemonApiError("POST", "/p", 409, "TASK_NOT_OWED", "refused")],
  ["a 503 with a code", new LegionDaemonApiError("POST", "/p", 503, "DAEMON_DRAINING", "draining")],
  ["a 400", new LegionDaemonApiError("POST", "/p", 400, undefined, "malformed")],
] as const) {
  test(`a take or a seen report the daemon answers with ${name} is not sent again`, async () => {
    const { take, routed, takes, seens, advance, marks } = rig();
    take.ready();
    takes.push({ fail: refusal });
    seens.push({ fail: refusal });
    take.relayed(put(4, 12));
    await advance(10 * DAEMON_ANSWER_TIMEOUT_MS);

    expect(routed("take")).toEqual([body(4, 12)]);
    expect(routed("task-seen")).toEqual([body(4, 12)]);
    expect(marks).toEqual([true, false]);
  });
}

test("a newer revision ends the re-sending of the older take and seen report, and is asked at once", async () => {
  const { take, routed, takes, seens, ran, advance } = rig();
  take.ready();
  takes.push({ fail: transport("ECONNREFUSED") }, { fail: transport("ECONNREFUSED") });
  seens.push({ fail: transport("ECONNREFUSED") }, { fail: transport("ECONNREFUSED") });
  take.relayed(put(4, 12));
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12)]);

  takes.splice(0);
  takes.push({ answer: ACCEPTED });
  seens.splice(0);
  take.relayed(put(4, 13));
  await advance(10 * DAEMON_RESEND_MAX_DELAY_MS);

  expect(routed("take")).toEqual([body(4, 12), body(4, 13)]);
  expect(routed("task-seen")).toEqual([body(4, 12), body(4, 13)]);
  expect(ran).toEqual([TEXT]);
});

test("a deleted value ends the re-sending of its take", async () => {
  const { take, routed, takes, advance, marks } = rig();
  take.ready();
  for (let n = 0; n < 5; n += 1) takes.push({ fail: transport("ECONNRESET") });
  take.relayed(put(4, 12));
  await advance(0);
  take.relayed({ claim: caller.claimToken, epoch: EPOCH, revision: 13, op: "delete" });
  await advance(10 * DAEMON_RESEND_MAX_DELAY_MS);

  expect(routed("take")).toEqual([body(4, 12)]);
  expect(marks).toEqual([true, false]);
});

// The daemon-launched controller replaces its take at each claim (`takeFor` in
// `packages/pi-legion/src/controller-session.ts`): the replaced take's requests and mark are not
// the new take's to keep.
test("a closed take sends nothing more, drops an answer that still arrives, clears its mark, and does nothing after", async () => {
  const { take, routed, takes, seens, ran, marks, advance } = rig();
  take.ready();
  const late = Promise.withResolvers<LegionTake>();
  takes.push({ late: late.promise });
  seens.push({ fail: transport("ECONNREFUSED") }, { fail: transport("ECONNREFUSED") });
  take.relayed(put(4, 12));
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12)]);
  expect(routed("task-seen")).toEqual([body(4, 12)]);
  expect(marks).toEqual([true]);

  take.close();
  expect(marks).toEqual([true, false]);
  // The seen report's re-sending stops, and the take's answer, arriving now, runs nothing.
  late.resolve(ACCEPTED);
  await advance(10 * DAEMON_RESEND_MAX_DELAY_MS);
  expect(routed("task-seen")).toEqual([body(4, 12)]);
  expect(ran).toEqual([]);

  // Nothing it is handed after it reports, asks or marks anything.
  take.relayed(put(5, 13));
  take.read({ task: value(5, 13), finished: 4 });
  take.ready();
  take.runStarted();
  expect(take.runEnded(false)).toBeUndefined();
  await advance(0);
  expect(routed("take")).toEqual([body(4, 12)]);
  expect(routed("task-seen")).toEqual([body(4, 12)]);
  expect(marks).toEqual([true, false]);
  expect(ran).toEqual([]);
});
