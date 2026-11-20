import { afterEach, describe, expect, jest, test } from "bun:test";
import { turn, turnsUntil } from "@legion/test-fixtures/fake-clock";
import { settled, until } from "@legion/test-fixtures/wait";
import { type ConnectionOptions, connect, type NatsConnection } from "nats";
import { LoggableError } from "../errors";
import {
  connectNats,
  NATS_DIAL_PLAN,
  type NatsDialPlan,
  natsConnectionOptions,
  natsDialBudget,
} from "../nats-connection";
import { noAuthService } from "../nats-credential";
import { dialOnDrivenClock, heldBound, type Outcome, outcomeOf } from "./driven-dial-clock";
import { type FakeNatsBehaviour, type FakeNatsServer, startFakeNats } from "./fake-nats-server";

// Real nats.js against a local server, with `connectNats` on a clock each row drives
// (`./driven-dial-clock`): the row ends each wait the dial arms, and the bound of a dial the server
// holds silent, so it reads the delay of each wait, the bound of each dial and their order with
// the dials, and no clock. A loaded machine changes none of those. The last row of `connectNats`
// runs the dial on the process's own clock under Bun's fake timers, with a stand-in connect and no
// socket, so the clock every session plugin dials on is held to the same delays.

const servers: FakeNatsServer[] = [];
const connections: NatsConnection[] = [];
afterEach(async () => {
  jest.useRealTimers();
  for (const nc of connections.splice(0)) await nc.close().catch(() => undefined);
  for (const server of servers.splice(0)) server.stop();
});

function fake(behaviour: (n: number) => FakeNatsBehaviour): FakeNatsServer {
  const server = startFakeNats(behaviour);
  servers.push(server);
  return server;
}

function options(url: string): ConnectionOptions {
  return natsConnectionOptions({
    servers: [url],
    name: "retry-test",
    credential: { kind: "none" },
  });
}

/** What a dial on the driven clock did, and how it ended. */
interface DialRun {
  /** Each dial (`dial 2`) and each step of the clock, in order. */
  readonly steps: readonly string[];
  /** The wait each failed dial announced to `onRetry`. */
  readonly delays: readonly number[];
  /** The `timeout` each dial handed nats.js, which for a dial of one server is its bound. */
  readonly bounds: readonly number[];
  /** The driven clock's reading when the dial ended. */
  readonly endedAt: number;
  readonly nc?: NatsConnection;
  readonly error?: { code?: unknown; message: string };
}

/**
 * Runs `connectNats` against `server` with `plan` on a driven clock. The row ends each wait as
 * the dial arms it, and the bound of each dial `silent` names (counting from 1) once the server
 * holds that dial's connection: a server that never answers leaves a dial only its bound.
 */
async function dialWith(
  server: FakeNatsServer,
  plan: NatsDialPlan,
  extra: {
    readonly timeout?: number;
    readonly silent?: (dial: number) => boolean;
    readonly stopAfterFirstFailure?: boolean;
    readonly stoppedFromStart?: boolean;
    /** The token of a broker credential is renewed between dials, and it sets this. */
    readonly ignoreAuthErrorAbort?: boolean;
    /** How far the clock moves as the first wait begins, as for an event loop its host blocked. */
    readonly blockOnRetryMs?: number;
  } = {}
): Promise<DialRun> {
  const delays: number[] = [];
  let stopped = extra.stoppedFromStart === true;
  const run = dialOnDrivenClock(
    connect,
    {
      ...options(server.url),
      ...(extra.timeout === undefined ? {} : { timeout: extra.timeout }),
      ...(extra.ignoreAuthErrorAbort === true ? { ignoreAuthErrorAbort: true } : {}),
    },
    plan,
    {
      stopped: () => stopped,
      onRetry: (_error, delayMs) => {
        delays.push(delayMs);
        if (extra.stopAfterFirstFailure === true) stopped = true;
        if (extra.blockOnRetryMs !== undefined && delays.length === 1) {
          run.driven.advance(extra.blockOnRetryMs);
        }
      },
    }
  );
  const silent = extra.silent ?? (() => false);
  const silentBound = () => silent(run.dials.length) && server.connections() >= run.dials.length;
  for (;;) {
    const next = await until(
      "the dial's next wait, or the bound of a dial the server holds silent, or the dial's end",
      () => run.ended() ?? run.driven.held().find((timer) => timer.kind === "wait" || silentBound())
    );
    if ("kind" in next) {
      run.driven.end(next);
      continue;
    }
    if (next.value !== undefined) connections.push(next.value);
    return {
      steps: run.steps,
      delays,
      bounds: run.dials.map((dial) => dial.options.timeout ?? Number.NaN),
      endedAt: run.driven.now(),
      nc: next.value,
      error: next.error,
    };
  }
}

/** The dials and waits of `steps`, in order: the timers of the bounds left out. */
function dialsAndWaits(steps: readonly string[]): string[] {
  return steps.filter((step) => step.startsWith("dial ") || step.startsWith("wait "));
}

/** What `dialsAndWaits` reads when each redial starts only once a wait of the next of `delays`
 *  was armed and ended. */
function redialsAfter(delays: readonly number[]): string[] {
  return [
    "dial 1",
    ...delays.flatMap((ms, i) => [`wait ${ms} armed`, `wait ${ms} ended`, `dial ${i + 2}`]),
  ];
}

describe("connectNats", () => {
  test("dials again once a wait of 250 ms, then one of 500 ms, has ended, and answers the third dial's connection", async () => {
    const server = fake((n) => (n < 2 ? "refuse" : "serve"));
    const dial = await dialWith(server, NATS_DIAL_PLAN);
    expect(dial.error).toBeUndefined();
    expect(dial.delays).toEqual([250, 500]);
    expect(dialsAndWaits(dial.steps)).toEqual(redialsAfter([250, 500]));
    expect(server.connections()).toBe(3);
  });

  // The refusals take none of the driven clock's time, so the waits alone use the budget: they end
  // at 20, 60, 140, 220, 300, 380 and 460 ms, and the next would end at 540 ms, leaving a dial
  // less than its 500 ms of the 1 000 ms budget.
  test("doubles each wait up to the plan's cap", async () => {
    const server = fake(() => "refuse");
    const dial = await dialWith(server, { firstDelayMs: 20, maxDelayMs: 80, budgetMs: 1_000 });
    expect(dial.error?.code).toBe("AUTHENTICATION_TIMEOUT");
    expect(dial.delays.slice(0, 4)).toEqual([20, 40, 80, 80]);
    expect(Math.max(...dial.delays)).toBe(80);
    expect(dial.delays).toEqual([20, 40, 80, 80, 80, 80, 80]);
    expect(dialsAndWaits(dial.steps)).toEqual(redialsAfter(dial.delays));
  });

  // A refusal, then a server that says nothing for each later dial: the first wait ends at 50 ms,
  // the second dial's whole 1 000 ms bound at 1 050 ms and the second wait at 1 100 ms, so 900 ms
  // of the 2 000 ms budget are left for the third dial, less than its 1 000 ms `timeout`. Its bound
  // ends at the budget, and the next wait would leave no dial time to answer.
  test("gives a later dial only the budget it has left", async () => {
    const server = fake((n) => (n === 0 ? "refuse" : "silent"));
    const dial = await dialWith(
      server,
      { firstDelayMs: 50, maxDelayMs: 50, budgetMs: 2_000 },
      { timeout: 1_000, silent: (n) => n > 1 }
    );
    expect(dial.error?.message).toBe("the NATS dial did not complete within 900 ms");
    expect(dial.bounds).toEqual([1_000, 1_000, 900]);
    expect(dial.endedAt).toBeLessThanOrEqual(2_000);
  });

  // The client writes its CONNECT into the closing socket, so a plain close fails a dial with the
  // socket's own error, or DISCONNECT when it carries none.
  test("ends with the last dial's own error", async () => {
    const server = fake((n) => (n === 0 ? "refuse" : "close"));
    const dial = await dialWith(server, { firstDelayMs: 10, maxDelayMs: 10, budgetMs: 600 });
    expect(["DISCONNECT", "EPIPE", "ECONNRESET"]).toContain(String(dial.error?.code));
    expect(server.connections()).toBeGreaterThanOrEqual(2);
  });

  // A blocked event loop makes a wait run long: the clock moves 700 ms as the 100 ms wait begins,
  // so the wait ends at 800 ms with 400 ms of the 1 200 ms budget left, too little for a dial to
  // answer in, so none starts.
  test("starts no dial when its wait ran past the budget", async () => {
    const server = fake(() => "refuse");
    const dial = await dialWith(
      server,
      { firstDelayMs: 100, maxDelayMs: 100, budgetMs: 1_200 },
      { blockOnRetryMs: 700 }
    );
    expect(dial.delays).toEqual([100]);
    expect(dial.error?.code).toBe("AUTHENTICATION_TIMEOUT");
    expect(server.connections()).toBe(1);
  });

  test("starts no dial once its caller stopped before the first", async () => {
    const server = fake(() => "serve");
    const dial = await dialWith(server, NATS_DIAL_PLAN, { stoppedFromStart: true });
    expect(dial.error).toBeInstanceOf(LoggableError);
    expect(server.connections()).toBe(0);
  });

  test("starts no dial once its caller stops during a wait", async () => {
    const server = fake(() => "refuse");
    const dial = await dialWith(
      server,
      { firstDelayMs: 200, maxDelayMs: 200, budgetMs: 5_000 },
      { stopAfterFirstFailure: true }
    );
    expect(dial.delays).toEqual([200]);
    expect(dial.error?.code).toBe("AUTHENTICATION_TIMEOUT");
    expect(server.connections()).toBe(1);
  });

  test("does not dial again after an authorization refusal", async () => {
    const server = fake(() => "refuse-authorization");
    const dial = await dialWith(server, NATS_DIAL_PLAN);
    expect(dial.error?.code).toBe("AUTHORIZATION_VIOLATION");
    expect(dial.delays).toEqual([]);
    expect(server.connections()).toBe(1);
  });

  // A broker token is renewed between dials, so the hub's refusal of an expired one is cured by
  // the next dial's new token, as nats.js's `ignoreAuthErrorAbort` says for its own reconnects.
  test("dials again after an authorization refusal of a credential renewed between dials", async () => {
    const server = fake((n) => (n === 0 ? "refuse-authorization" : "serve"));
    const dial = await dialWith(
      server,
      { firstDelayMs: 20, maxDelayMs: 20, budgetMs: 2_000 },
      { ignoreAuthErrorAbort: true }
    );
    expect(dial.error).toBeUndefined();
    expect(dial.delays).toEqual([20]);
    expect(server.connections()).toBe(2);
  });

  test("does not dial again after a failure no redial cures", async () => {
    let dials = 0;
    const error = await connectNats(
      async () => {
        dials += 1;
        throw new LoggableError(noAuthService("NATS_AUTH"));
      },
      options("nats://127.0.0.1:4222"),
      { firstDelayMs: 5, maxDelayMs: 5, budgetMs: 2_000 }
    ).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(LoggableError);
    expect(dials).toBe(1);
  });

  test("bounds the first dial at the caller's firstBoundMs", async () => {
    const server = fake(() => "silent");
    const run = dialOnDrivenClock(
      connect,
      options(server.url),
      { ...NATS_DIAL_PLAN, budgetMs: 0 },
      { firstBoundMs: 300 }
    );
    const bound = await heldBound(run, () => server.connections() === 1);
    expect(bound.ms).toBe(300);
    expect(run.dials.map((dial) => dial.options.timeout)).toEqual([300]);
    run.driven.end(bound);
    const dial = await settled("the dial once its bound ended", run.outcome);
    expect(dial.error?.message).toBe("the NATS dial did not complete within 300 ms");
  });

  // The clock every session plugin dials on: under Bun's fake timers, which hold
  // `performance.now()` and `node:timers/promises` as they hold `setTimeout`, the redial starts
  // only when the fake clock reaches the wait's 250 ms, and the second dial fails only when it
  // reaches its 1 000 ms bound. A stand-in connect, since a socket does not fit the fake clock.
  test("with no clock of its caller's, waits and bounds each dial on the process's own timers", async () => {
    jest.useFakeTimers();
    const dials: ConnectionOptions[] = [];
    let waiting = false;
    let stopped = false;
    let ended: Outcome<NatsConnection> | undefined;
    void outcomeOf(
      connectNats(
        async (dialOptions) => {
          dials.push(dialOptions);
          if (dials.length === 1) {
            throw Object.assign(new Error("the socket was reset"), { code: "ECONNRESET" });
          }
          stopped = true;
          return Promise.withResolvers<NatsConnection>().promise;
        },
        { timeout: 1_000 },
        { firstDelayMs: 250, maxDelayMs: 250, budgetMs: 5_000 },
        {
          stopped: () => stopped,
          onRetry: () => {
            waiting = true;
          },
        }
      )
    ).then((outcome) => {
      ended = outcome;
    });
    await turnsUntil("the first dial's failure and its wait", () => waiting);
    jest.advanceTimersByTime(249);
    await turn();
    expect(dials).toHaveLength(1);
    jest.advanceTimersByTime(1);
    await turnsUntil("the second dial", () => dials.length === 2);
    jest.advanceTimersByTime(999);
    await turn();
    expect(ended).toBeUndefined();
    jest.advanceTimersByTime(1);
    await turnsUntil("the second dial's end at its bound", () => ended !== undefined);
    expect(ended?.error?.message).toBe("the NATS dial did not complete within 1000 ms");
  });
});

describe("natsDialBudget", () => {
  test("gives a pass's dial what the pass has left after the rest of it, at most the plan's budget, and none when nothing is left", () => {
    expect(natsDialBudget(90_000, 20_250, NATS_DIAL_PLAN)).toBe(25_000);
    expect(natsDialBudget(30_000, 20_250, NATS_DIAL_PLAN)).toBe(9_750);
    expect(natsDialBudget(7_500, 20_250, NATS_DIAL_PLAN)).toBe(0);
    expect(natsDialBudget(Number.POSITIVE_INFINITY, 20_250, NATS_DIAL_PLAN)).toBe(25_000);
  });
});
