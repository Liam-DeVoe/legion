import { afterEach, describe, expect, test } from "bun:test";
import { dirname, join } from "node:path";
import {
  recordTimers,
  type TimerOnTheWay,
  type TimerStep,
} from "@legion/test-fixtures/timer-record";
import { settled, span, until } from "@legion/test-fixtures/wait";
import { type ConnectionOptions, connect, Events, type NatsConnection } from "nats";
import { NatsConnectionImpl } from "nats/lib/nats-base-client/nats";
import { setTransportFactory } from "nats/lib/nats-base-client/transport";
import { NodeTransport } from "nats/lib/src/node_transport";
import { LoggableError } from "../errors";
import {
  connectNats,
  NATS_DIAL_TIMEOUT_MS,
  NATS_MIN_REDIAL_BOUND_MS,
  natsConnectionOptions,
} from "../nats-connection";
import { dialOnDrivenClock, heldBound, outcomeOf } from "./driven-dial-clock";
import {
  type FakeNatsBehaviour,
  type FakeNatsServer,
  LATE_INFO_MS,
  startFakeNats,
} from "./fake-nats-server";

// Real nats.js against a local server that speaks only the handshake, so each row reads what
// nats.js 2.29.3, as this repository patches it, does when a server closes or refuses a dial. No
// verdict reads a clock. A row that holds nats.js to a time reads the timers nats.js's own code
// armed and ran on the way (`@legion/test-fixtures/timer-record`), each with the delay nats.js
// chose, which a loaded machine cannot change; a dial that waited out its handshake's 10 s timeout
// shows that timer there. `connectNats`'s own waits and bounds run on a clock the row drives
// (`./driven-dial-clock`), which holds each bound until the row ends it, so a row reads the bound a
// dial armed and what ending it does; one row runs a silent dial to its end on the process's own
// clock and reads which timer ended it. Bun's fake clock does not fit real sockets: moved a
// millisecond each turn of the event loop, it ran 1,356 ms ahead while a refused dial's socket was
// read (merge6's pod run). Each wait for a fact goes through the shared wait, and a row that says
// nothing more happens holds a named span where no state shows it: a dial nats.js did not start
// leaves no state to read, and a closed connection orders no later effect after one.

/** nats.js's own code and this package's, whose timers a row reads. */
const TIMED_ROOTS = [
  dirname(Bun.resolveSync("nats", import.meta.dir)),
  join(import.meta.dir, ".."),
];

/** One dial, with no redial. */
const ONCE = { firstDelayMs: 0, maxDelayMs: 0, budgetMs: 0 };

/** A row's own step in the timer record, which puts its moments in one order with the timers. */
interface RowMark {
  readonly mark: string;
}

const servers: FakeNatsServer[] = [];
const connections: NatsConnection[] = [];
let stopRecording: (() => void) | undefined;
afterEach(async () => {
  stopRecording?.();
  stopRecording = undefined;
  for (const nc of connections.splice(0)) await nc.close().catch(() => undefined);
  for (const server of servers.splice(0)) server.stop();
});

function fake(behaviour: (n: number) => FakeNatsBehaviour, lateInfoMs?: number): FakeNatsServer {
  const server = startFakeNats(behaviour, lateInfoMs);
  servers.push(server);
  return server;
}

function options(urls: readonly string[]) {
  return {
    ...natsConnectionOptions({
      servers: urls,
      name: "handshake-test",
      credential: { kind: "none" },
    }),
    noRandomize: true,
  };
}

/**
 * Records each timer nats.js's code and this package's arm and run, from now to the row's end, in
 * one order with the row's marks; `mark` returns the index of its step.
 */
function recordTimed(): {
  readonly steps: readonly (TimerStep | RowMark)[];
  mark(what: string): number;
} {
  const steps: (TimerStep | RowMark)[] = [];
  stopRecording = recordTimers(TIMED_ROOTS, (step) => steps.push(step));
  return { steps, mark: (what) => steps.push({ mark: what }) - 1 };
}

/**
 * The timers of `steps` whose callback ran between the steps at `after` and `before`, whenever
 * they were armed, each with the delay it was armed with and the line that armed it: what the code
 * waited on there. Refuses a timer the record could not place, as `timersBetween` does, since what
 * it leaves out could only read less than the way waited.
 */
function ranBetween(
  steps: readonly (TimerStep | RowMark)[],
  after: number,
  before: number
): TimerOnTheWay[] {
  const armed = new Map<number, TimerOnTheWay | undefined>();
  const ran: TimerOnTheWay[] = [];
  steps.slice(0, before).forEach((step, index) => {
    if ("timer" in step) armed.set(step.timer, { delayMs: step.delayMs, from: step.from });
    if ("unplaced" in step) armed.set(step.unplaced, undefined);
    if (!("fired" in step) || index <= after || !armed.has(step.fired)) return;
    const timer = armed.get(step.fired);
    if (timer === undefined) {
      throw new Error(`the timer record could not place timer ${step.fired}, which ran on the way`);
    }
    ran.push(timer);
  });
  return ran;
}

/** The sum of the delays `timers` were armed with. */
function delaysOf(timers: readonly TimerOnTheWay[]): number {
  return timers.reduce((sum, timer) => sum + timer.delayMs, 0);
}

/** Counts each status of `type` on `nc`, calling `seen` at each. */
function countStatus(
  nc: NatsConnection,
  type: string,
  seen: () => void = () => {}
): { count: () => number } {
  let count = 0;
  void (async () => {
    for await (const status of nc.status()) {
      if (status.type !== type) continue;
      count += 1;
      seen();
    }
  })();
  return { count: () => count };
}

/**
 * nats.js's `connect`, with the name of each server resolved by `lookup` in place of the system
 * resolver: nats.js's own `connect` sets its transport (and so its resolver) at every call, so a
 * row that needs a name with two addresses, or a slow lookup, opens its connection through the
 * transport factory itself, as that `connect` does.
 */
function connectResolving(lookup: () => Promise<string[]>) {
  return (opts: ConnectionOptions): Promise<NatsConnection> => {
    setTransportFactory({ factory: () => new NodeTransport(), dnsResolveFn: lookup });
    return NatsConnectionImpl.connect(opts);
  };
}

describe("a NATS dial that the server closes during the handshake", () => {
  // "At once" is held to nats.js's own timers: what ran out between the dial and its failure
  // stays under 1 s, where a dial that waited out its handshake's timeout would show its 10 s.
  test("fails at once with the server's authentication timeout", async () => {
    const server = fake(() => "refuse");
    const record = recordTimed();
    const dialed = record.mark("dial");
    const dial = await settled("the refused dial", outcomeOf(connect(options([server.url]))));
    const ran = ranBetween(record.steps, dialed, record.mark("failed"));
    expect(
      delaysOf(ran),
      `the timers that ran out on the way: ${JSON.stringify(ran)}`
    ).toBeLessThan(1_000);
    expect(dial.error?.code).toBe("AUTHENTICATION_TIMEOUT");
  });

  // The client writes its CONNECT into the closing socket, so the socket's own error (EPIPE or
  // ECONNRESET) is usually what the close carries; DISCONNECT when it carries none.
  const CLOSE_CODES = ["DISCONNECT", "EPIPE", "ECONNRESET"];

  test("fails at once with the socket's error, or DISCONNECT, when the server closes with no -ERR", async () => {
    const server = fake(() => "close");
    const record = recordTimed();
    const dialed = record.mark("dial");
    const dial = await settled("the closed dial", outcomeOf(connect(options([server.url]))));
    const ran = ranBetween(record.steps, dialed, record.mark("failed"));
    expect(
      delaysOf(ran),
      `the timers that ran out on the way: ${JSON.stringify(ran)}`
    ).toBeLessThan(1_000);
    expect(CLOSE_CODES).toContain(String(dial.error?.code));
  });

  test("fails at once with the last server's failure, not an earlier server's refusal", async () => {
    const refusing = fake(() => "refuse");
    const closing = fake(() => "close");
    const record = recordTimed();
    const dialed = record.mark("dial");
    const dial = await settled(
      "the dial of both servers",
      outcomeOf(connect(options([refusing.url, closing.url])))
    );
    const ran = ranBetween(record.steps, dialed, record.mark("failed"));
    expect(
      delaysOf(ran),
      `the timers that ran out on the way: ${JSON.stringify(ran)}`
    ).toBeLessThan(1_000);
    expect(closing.connections()).toBe(1);
    expect(CLOSE_CODES).toContain(String(dial.error?.code));
  });

  // Between the drop and the reconnect nats.js waits its reconnect wait twice, 200 ms at most each
  // (it counts each from the dial before), and its refused handshake's timer does not run out.
  test("fails at once on a reconnect: one refused handshake, then exactly one reconnect", async () => {
    const server = fake((n) => (n === 1 ? "refuse" : "serve"));
    const nc = await connect({
      ...options([server.url]),
      reconnectTimeWait: 200,
      reconnectJitter: 0,
    });
    connections.push(nc);
    const record = recordTimed();
    let reconnectedAt: number | undefined;
    const reconnects = countStatus(nc, Events.Reconnect, () => {
      reconnectedAt ??= record.mark("reconnected");
    });
    const dropped = record.mark("dropped");
    server.dropAll();
    const reconnected = await until("a reconnect", () => reconnectedAt);
    const ran = ranBetween(record.steps, dropped, reconnected);
    expect(
      delaysOf(ran),
      `the timers that ran out between the drop and the reconnect: ${JSON.stringify(ran)}`
    ).toBeLessThan(2 * 200 + 1_000);
    expect(server.connections()).toBe(3);
    // A second report of the same reconnect would come at once; none does.
    await span(500, "a second report of one reconnect leaves no state but the count read below");
    expect(reconnects.count()).toBe(1);
  });

  // The close settles with no timer of nats.js's running out on the way: one that waited for the
  // held handshake would wait out that handshake's 10 s timeout, armed before the close.
  test("keeps a close during a reconnect's handshake final", async () => {
    const server = fake((n) => (n === 0 ? "serve" : "hold"));
    const nc = await connect({
      ...options([server.url]),
      reconnectTimeWait: 200,
      reconnectJitter: 0,
    });
    const record = recordTimed();
    server.dropAll();
    await until("the reconnect's dial", () => server.connections() === 2);
    const asked = record.mark("close");
    const closing = outcomeOf(nc.close());
    await settled("the connection's close", nc.closed());
    const ran = ranBetween(record.steps, asked, record.mark("closed"));
    expect(
      delaysOf(ran),
      `the timers that ran out between the close and its end: ${JSON.stringify(ran)}`
    ).toBeLessThan(1_000);
    await closing;
    await span(
      3_000,
      "a dial the close did not end leaves no state but the server's count of connections"
    );
    expect(server.connections()).toBe(2);
  });

  test("keeps the authorization rule: refused reconnects end the connection", async () => {
    const server = fake((n) => (n === 0 ? "serve" : "refuse-authorization"));
    const nc = await connect({
      ...options([server.url]),
      reconnectTimeWait: 200,
      reconnectJitter: 0,
    });
    server.dropAll();
    const closed = await outcomeOf(settled("the close after the refused reconnects", nc.closed()));
    expect(closed.error).toBeUndefined();
    expect(server.connections()).toBeLessThanOrEqual(3);
  });

  // A close() that lands after the reconnect's PONG resolved and before the connection is marked
  // up: the row calls close() inside nats.js's own PONG handling, which is that moment.
  test("keeps a close between a reconnect's PONG and its connection final", async () => {
    const server = fake(() => "serve");
    const nc = await connect({
      ...options([server.url]),
      reconnectTimeWait: 100,
      reconnectJitter: 0,
    });
    const handler = (
      nc as unknown as {
        protocol: {
          connected: boolean;
          processPong: () => void;
          heartbeats: { timer?: unknown };
        };
      }
    ).protocol;
    const processPong = handler.processPong.bind(handler);
    let armed = true;
    handler.processPong = () => {
      processPong();
      if (!armed) return;
      armed = false;
      void nc.close();
    };
    server.dropAll();
    await until("the reconnect's PONG", () => !armed);
    await settled("the connection's close", nc.closed());
    await span(
      300,
      "a reconnect the close did not end leaves no state but the handler's own fields read below"
    );
    expect(handler.connected).toBe(false);
    expect(handler.heartbeats.timer).toBeUndefined();
  });

  test("keeps a close during a reconnect's name lookup final", async () => {
    const server = fake((n) => (n === 0 ? "serve" : "hold"));
    const port = new URL(server.url).port;
    let lookups = 0;
    const inReconnectLookup = Promise.withResolvers<void>();
    // The reconnect's lookup is held until the row has closed the connection, then answers.
    const lookupAnswers = Promise.withResolvers<void>();
    const reconnectLookupDone = Promise.withResolvers<void>();
    const nc = await connectResolving(async () => {
      lookups += 1;
      if (lookups > 1) {
        // The reconnect's lookup: the row closes while it is held here, then lets it answer.
        inReconnectLookup.resolve();
        await lookupAnswers.promise;
        reconnectLookupDone.resolve();
      }
      return ["127.0.0.1"];
    })({ ...options([`nats://fake.test:${port}`]), reconnectTimeWait: 200, reconnectJitter: 0 });
    server.dropAll();
    await settled("the reconnect's name lookup", inReconnectLookup.promise);
    await settled("the connection's close", nc.close());
    lookupAnswers.resolve();
    await settled("the held lookup to answer", reconnectLookupDone.promise);
    // An absence: a dial the close did not end would open its connection within this window.
    await span(
      1_000,
      "a dial the close did not end leaves no state but the server's count of connections"
    );
    expect(server.connections()).toBe(1);
  });

  test("keeps a close during the first of a name's two addresses final", async () => {
    const server = fake((n) => (n === 0 ? "serve" : "hold"));
    const port = new URL(server.url).port;
    const nc = await connectResolving(async () => ["127.0.0.1", "127.0.0.1"])({
      ...options([`nats://fake.test:${port}`]),
      reconnectTimeWait: 200,
      reconnectJitter: 0,
    });
    server.dropAll();
    await until("the reconnect's first address", () => server.connections() === 2);
    await settled("the connection's close", nc.close());
    await span(
      2_000,
      "a dial the close did not end leaves no state but the server's count of connections"
    );
    expect(server.connections()).toBe(2);
  });
});

describe("a NATS dial's bound", () => {
  test("natsConnectionOptions gives each dial NATS_DIAL_TIMEOUT_MS, 10 s, as its timeout", () => {
    expect(NATS_DIAL_TIMEOUT_MS).toBe(10_000);
    expect(options(["nats://127.0.0.1:4222"]).timeout).toBe(10_000);
  });

  // On the process's own clock, to its end: the first timer to run out is the bound connectNats
  // armed, so the dial waited that whole bound, and the bound's error ended it. nats.js's own
  // handshake timer, armed with the same timeout just after it, is due later.
  test("a server that never answers fails the dial when the bound armed with NATS_DIAL_TIMEOUT_MS, 10 s, runs out", async () => {
    const server = fake(() => "silent");
    const record = recordTimed();
    const dialed = record.mark("dial");
    const dial = await settled(
      "the silent dial",
      outcomeOf(connectNats(connect, options([server.url]), ONCE))
    );
    const ran = ranBetween(record.steps, dialed, record.mark("failed"));
    expect(ran[0], `the timers that ran out on the way: ${JSON.stringify(ran)}`).toEqual({
      delayMs: NATS_DIAL_TIMEOUT_MS,
      from: expect.stringContaining("/src/nats-connection.ts:"),
    });
    expect(dial.error?.message).toBe(
      `the NATS dial did not complete within ${NATS_DIAL_TIMEOUT_MS} ms`
    );
  });

  // The bound is ended while nats.js still waits on the first server's half of it.
  test("two servers that never answer fail the dial at its one bound, of which each server has half", async () => {
    const first = fake(() => "silent");
    const second = fake(() => "silent");
    const run = dialOnDrivenClock(connect, options([first.url, second.url]), ONCE);
    const bound = await heldBound(run, () => first.connections() === 1);
    expect(bound.ms).toBe(NATS_DIAL_TIMEOUT_MS);
    expect(run.dials.map((dial) => dial.options.timeout)).toEqual([NATS_DIAL_TIMEOUT_MS / 2]);
    run.driven.end(bound);
    const dial = await settled("the dial once its bound ended", run.outcome);
    expect(dial.error?.message).toBe(
      `the NATS dial did not complete within ${NATS_DIAL_TIMEOUT_MS} ms`
    );
  });

  // nats.js gives each address of a name a `timeout` of its own, so only the bound around the
  // whole `connect()` ends a dial of two silent addresses: one bound of the whole 1 s, which ends
  // the dial while nats.js still waits on the first address.
  test("is whole: a name with two addresses that never answer fails the dial at the bound", async () => {
    const server = fake(() => "silent");
    const port = new URL(server.url).port;
    const run = dialOnDrivenClock(
      connectResolving(async () => ["127.0.0.1", "127.0.0.1"]),
      { ...options([`nats://fake.test:${port}`]), timeout: 1_000 },
      ONCE
    );
    const bound = await heldBound(run, () => server.connections() === 1);
    expect(bound.ms).toBe(1_000);
    run.driven.end(bound);
    const dial = await settled("the dial of a name's two silent addresses", run.outcome);
    expect(dial.error?.message).toBe("the NATS dial did not complete within 1000 ms");
  });

  // The attempt nats.js is still making when the bound expires is abandoned with it: it dials no
  // further address, so a redial does not overlap dials whose result would be closed unread.
  test("is whole: a dial past its bound dials no further address", async () => {
    const server = fake(() => "silent");
    const port = new URL(server.url).port;
    const run = dialOnDrivenClock(
      connectResolving(async () => ["127.0.0.1", "127.0.0.1", "127.0.0.1"]),
      { ...options([`nats://fake.test:${port}`]), timeout: 600 },
      ONCE
    );
    const bound = await heldBound(run, () => server.connections() === 1);
    run.driven.end(bound);
    const dial = await settled("the dial once its bound ended", run.outcome);
    expect(dial.error?.message).toBe("the NATS dial did not complete within 600 ms");
    // The abandoned connect() settles once nats.js stops dialing, after the first address's own
    // 600 ms timeout: a connect() that went on to the other two addresses would open their
    // connections first.
    await settled(
      "the abandoned connect()",
      Promise.all(run.dials.map((abandoned) => outcomeOf(abandoned.connecting)))
    );
    expect(server.connections()).toBe(1);
  });

  // nats.js tries the next server when one fails, and looks its name up first: a dial past its
  // bound looks no further name up, so a lookup that never answers cannot keep it pending. The
  // first name has two silent addresses, each given 500 ms, the floor of a 600 ms bound's half, and
  // the bound ends while nats.js dials the second, which it leaves only once that times out too.
  test("is whole: a dial past its bound looks no further server's name up", async () => {
    const server = fake(() => "silent");
    const port = new URL(server.url).port;
    const looked: string[] = [];
    const lookup = async (host: string): Promise<string[]> => {
      looked.push(host);
      return host === "first.test" ? ["127.0.0.1", "127.0.0.1"] : ["127.0.0.1"];
    };
    const run = dialOnDrivenClock(
      connectResolving(lookup as () => Promise<string[]>),
      { ...options([`nats://first.test:${port}`, `nats://second.test:${port}`]), timeout: 600 },
      ONCE
    );
    const bound = await heldBound(run, () => server.connections() === 2);
    run.driven.end(bound);
    const dial = await settled("the dial once its bound ended", run.outcome);
    expect(dial.error?.message).toBe("the NATS dial did not complete within 600 ms");
    // The abandoned connect() settles once nats.js has left the first server and refused to go
    // on: one that went on would look the second server's name up first.
    await settled(
      "the abandoned connect()",
      Promise.all(run.dials.map((abandoned) => outcomeOf(abandoned.connecting)))
    );
    expect(looked).toEqual(["first.test"]);
  });

  // nats.js looks a name up before its timer starts, so a lookup that never answers would hold
  // the dial for good: the bound, ended while the lookup is held, ends the dial.
  test("is whole: a name lookup that never answers fails the dial at the bound", async () => {
    let lookups = 0;
    const run = dialOnDrivenClock(
      connectResolving(() => {
        lookups += 1;
        return Promise.withResolvers<string[]>().promise;
      }),
      { ...options(["nats://fake.test:4222"]), timeout: 1_000 },
      ONCE
    );
    const bound = await heldBound(run, () => lookups === 1);
    expect(bound.ms).toBe(1_000);
    run.driven.end(bound);
    const dial = await settled("the dial whose name lookup never answers", run.outcome);
    expect(dial.error?.message).toBe("the NATS dial did not complete within 1000 ms");
  });

  // The connect settles only once the dial has failed at its bound, when the row lets it.
  test("is whole: a connect that settles late fails at the bound with the plugin's own error, and its connection is closed", async () => {
    let closed = false;
    const late = Promise.withResolvers<NatsConnection>();
    const run = dialOnDrivenClock(() => late.promise, { timeout: 300 }, ONCE);
    const bound = await heldBound(run);
    expect(bound.ms).toBe(300);
    run.driven.end(bound);
    const dial = await settled("the dial whose connect settles late", run.outcome);
    expect(dial.error?.message).toBe("the NATS dial did not complete within 300 ms");
    late.resolve({
      close: async () => {
        closed = true;
      },
    } as unknown as NatsConnection);
    await until("the late connection's close", () => closed);
  });

  test("shares the bound among the servers, so a dial reaches a second server when the first never answers", async () => {
    const silent = fake(() => "silent");
    const serving = fake(() => "serve");
    const dial = await settled(
      "the dial of a silent server and a serving one",
      outcomeOf(
        connectNats(connect, { ...options([silent.url, serving.url]), timeout: 1_000 }, ONCE)
      )
    );
    if (dial.value !== undefined) connections.push(dial.value);
    expect(dial.error).toBeUndefined();
    expect(serving.connections()).toBe(1);
  });

  // Each of four servers gets a quarter of a 1 200 ms bound, 300 ms, below the 500 ms floor: the
  // floor gives the second server, which sends its INFO 400 ms after it accepts, time to answer.
  // Both margins are 100 ms, so a short stall of the test's own event loop decides nothing.
  test("shares the bound among the servers, and gives each one at least 500 ms", async () => {
    const boundMs = 1_200;
    const infoMs = 400;
    const servers = [
      fake(() => "silent"),
      fake(() => "late-serve", infoMs),
      fake(() => "silent"),
      fake(() => "silent"),
    ];
    // The row tests the floor only while a share without it misses the INFO and one with it does
    // not; a change of the floor or of these numbers that breaks that fails here, not silently.
    expect(boundMs / servers.length).toBeLessThan(infoMs);
    expect(infoMs).toBeLessThan(NATS_MIN_REDIAL_BOUND_MS);
    const dial = await settled(
      "the dial of four servers",
      outcomeOf(
        connectNats(
          connect,
          { ...options(servers.map((server) => server.url)), timeout: boundMs },
          ONCE
        )
      )
    );
    if (dial.value !== undefined) connections.push(dial.value);
    expect(dial.error).toBeUndefined();
    expect(servers[1]?.connections()).toBe(1);
  });

  test("gives a connection that a short bound opened the whole timeout for its reconnects", async () => {
    const server = fake((n) => (n === 0 ? "serve" : "late-serve"));
    const nc = await connectNats(
      connect,
      { ...options([server.url]), reconnectTimeWait: 100, reconnectJitter: 0 },
      ONCE,
      { firstBoundMs: LATE_INFO_MS / 2 }
    );
    connections.push(nc);
    const reconnects = countStatus(nc, Events.Reconnect);
    server.dropAll();
    // A reconnect timed at the dial's 300 ms gives up before the server's 600 ms INFO, every time.
    await until("a reconnect to a server that answers in 600 ms", () => reconnects.count() >= 1);
  });

  test("is whole: an error past the bound is the plugin's own, which a log prints whole", async () => {
    const never = Promise.withResolvers<NatsConnection>();
    // A dial with no bound never settles, and the row fails at its deadline, naming the dial.
    const error = await settled(
      "the dial whose connect never settles",
      connectNats(() => never.promise, { timeout: 50 }, ONCE).catch((e) => e)
    );
    expect(error).toBeInstanceOf(LoggableError);
  });
});
