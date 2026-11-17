// packages/broker-client/src/__tests__/retry-loop.test.ts
//
// The token client's one retry loop per session (`BrokerTokenClient.token`), as one table: each
// row is what the broker answers, trade by trade, and what the client must do about it. A row
// asserts the caller's outcome, the trades the broker saw, the waits between them and the lines
// the client logged at each wait. The clock and the waits are the test's own: a wait the client
// asks for passes at once while the row's budget of waits lasts and then holds, so a schedule of
// minutes runs at the test's speed and a loop that trades forever stops where the row reads it.
// A trade's own timeout is real and short (`attemptMs`), since the client sets it on the fetch.

import { expect, test } from "bun:test";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { newDpopKey } from "../dpop";
import type { SubjectToken, TokenSource } from "../source";
import {
  BrokerRefusedError,
  type BrokerToken,
  BrokerTokenClient,
  RegistrationPendingError,
  TradeRetryingError,
} from "../token-client";
import { SUBJECT_TOKEN_TYPE_KUBERNETES } from "../wire";
import { accepted } from "./stub";

/** The session name a pod of these rows trades under. */
const SESSION = "implementer-g1";

/** The broker's refusal of a pod whose session no registration names yet, in its own words
 *  (`registeredPodSession`, packages/envoy/internal/broker/oauth/exchange.go). */
const UNREGISTERED = `pod_session_not_registered: No registration says this pod may write under the session name '${SESSION}'. A pod writes under a session name its deployment registered for it.`;

/** A pod's projected token, read afresh at each trade; random, as a real one is. */
const POD: TokenSource = {
  read: async (): Promise<SubjectToken> => ({
    token: fixtureSecret("pod"),
    tokenType: SUBJECT_TOKEN_TYPE_KUBERNETES,
  }),
  describe: () => "a pod's projected service-account token",
};

/** How long one trade may take before the client reads it as unanswered. */
const ATTEMPT_MS = 25;

/** What the broker does with one trade. */
type Answer = "granted" | "pending" | "503" | "refused" | "connect refused" | "silent";

/** The broker's answer, as the client's fetch returns or throws it. */
async function respond(answer: Answer, signal: AbortSignal | null | undefined): Promise<Response> {
  switch (answer) {
    case "granted":
      return Response.json(accepted(fixtureSecret("granted"), 600));
    case "pending":
      return Response.json(
        { error: "invalid_grant", error_description: UNREGISTERED },
        { status: 400 }
      );
    case "503":
      return new Response("the broker is away", { status: 503 });
    case "refused":
      return Response.json({ error: "invalid_client" }, { status: 401 });
    case "connect refused":
      // A real refused connection, the runtime's own error: nothing listens on port 1.
      return await fetch("http://127.0.0.1:1/", signal === null ? {} : { signal });
    case "silent":
      return await new Promise<Response>((_resolve, reject) => {
        signal?.addEventListener("abort", () => reject(signal.reason), { once: true });
      });
  }
}

/** One row's client and what the row can read of it. */
interface Rig {
  readonly client: BrokerTokenClient;
  readonly clock: { now: number };
  readonly waits: number[];
  readonly lines: string[];
  trades(): number;
  /** Lets the first held wait pass, moving the clock by its length. */
  advance(): void;
}

/** A pod's client against a broker answering `answer(n, now)` to its n-th trade; its first
 *  `budget` waits pass at once, and the rest hold until the row advances them. */
async function rig(answer: (trade: number, now: number) => Answer, budget: number): Promise<Rig> {
  const clock = { now: 1_700_000_000_000 };
  const waits: number[] = [];
  const lines: string[] = [];
  const held: (() => void)[] = [];
  let trades = 0;
  let left = budget;
  const client = new BrokerTokenClient({
    brokerUrl: "http://broker.test",
    key: await newDpopKey(),
    subject: POD,
    session: SESSION,
    now: () => clock.now,
    // The worst case of the jitter: every wait its whole length.
    random: () => 1,
    attemptMs: ATTEMPT_MS,
    sleep: (ms: number, signal?: AbortSignal) => {
      waits.push(ms);
      if (left > 0) {
        left -= 1;
        clock.now += ms;
        return Promise.resolve();
      }
      return new Promise<void>((resolve) => {
        held.push(() => {
          clock.now += ms;
          resolve();
        });
        signal?.addEventListener("abort", () => resolve(), { once: true });
      });
    },
    log: (line: string) => lines.push(line),
    fetch: (async (_url: string | URL | Request, init?: RequestInit) => {
      trades += 1;
      return await respond(answer(trades, clock.now), init?.signal);
    }) as typeof fetch,
  });
  return {
    client,
    clock,
    waits,
    lines,
    trades: () => trades,
    advance: () => held.shift()?.(),
  };
}

/** Turns of the event loop until `ready()` holds, failing the row past `boundMs` of real time. */
async function until(ready: () => boolean, what: string, boundMs = 3_000): Promise<void> {
  const end = Date.now() + boundMs;
  while (!ready()) {
    if (Date.now() > end) throw new Error(`still not ${what} after ${boundMs} ms`);
    await new Promise((resolve) => setTimeout(resolve, 1));
  }
}

/** What `call` settled with by a few turns of the event loop, or "pending". */
async function outcome(call: Promise<BrokerToken>): Promise<unknown> {
  const bound = Promise.withResolvers<"pending">();
  setTimeout(() => bound.resolve("pending"), 5);
  return await Promise.race([
    call.then(
      (token) => token,
      (error: unknown) => error
    ),
    bound.promise,
  ]);
}

/** A row of the table: what the broker answers, the waits that pass at once, and the drive. */
interface Row {
  readonly answer: (trade: number, now: number) => Answer;
  readonly budget: number;
  readonly drive: (rig: Rig) => Promise<void>;
}

const ROWS: [string, Row][] = [
  [
    "pending, then granted: traded again on the short schedule, a line at each wait",
    {
      answer: (n) => (n <= 2 ? "pending" : "granted"),
      budget: 10,
      drive: async ({ client, waits, lines, trades }) => {
        const token = await client.token();
        expect(token.accessToken).toStartWith("granted-");
        expect(trades()).toBe(3);
        expect(waits).toEqual([2_000, 4_000]);
        expect(lines).toHaveLength(2);
        for (const line of lines) {
          expect(line).toContain(`no registration for this pod's session ${SESSION}`);
        }
      },
    },
  ],
  [
    "B1: pending, then a broker that never answers: each trade times out and is traded again, with a line at each wait",
    {
      answer: (n) => (n === 1 ? "pending" : "silent"),
      budget: 4,
      drive: async ({ client, waits, lines, trades }) => {
        const life = new AbortController();
        const call = client.token(life.signal);
        await until(() => waits.length === 5, "at its fifth wait");
        expect(trades()).toBe(5);
        // The short schedule's first wait, then the long schedule's: a different failure starts
        // no schedule over.
        expect(waits).toEqual([2_000, 15_000, 30_000, 60_000, 120_000]);
        expect(lines).toHaveLength(5);
        for (const line of lines.slice(1)) {
          expect(line).toContain(
            `did not answer the trade of a pod's projected service-account token within ${ATTEMPT_MS / 1000} s`
          );
        }
        expect(await outcome(call)).toBe("pending");
        life.abort();
        const ended = await outcome(call);
        expect(ended).toBeInstanceOf(TradeRetryingError);
        expect((ended as Error).message).toContain("did not answer");
        client.close();
      },
    },
  ],
  [
    "B2: pending, then a 503, then granted: the 503 is traded again, and the caller takes the token",
    {
      answer: (n) => (n === 1 ? "pending" : n === 2 ? "503" : "granted"),
      budget: 10,
      drive: async ({ client, waits, lines, trades }) => {
        const token = await client.token(new AbortController().signal);
        expect(token.accessToken).toStartWith("granted-");
        expect(trades()).toBe(3);
        expect(waits).toEqual([2_000, 15_000]);
        expect(lines).toHaveLength(2);
        expect(lines[1]).toContain("HTTP 503");
      },
    },
  ],
  [
    "refused: the loop ends at once, and a call before the next slot is told the same with no trade",
    {
      answer: () => "refused",
      budget: 10,
      drive: async ({ client, clock, lines, trades }) => {
        const first = await client.token().catch((error: unknown) => error);
        expect(first).toBeInstanceOf(BrokerRefusedError);
        expect((first as BrokerRefusedError).permanent).toBe(true);
        expect(trades()).toBe(1);
        expect(lines).toHaveLength(0);
        // A caller at a NATS reconnect's pace, 2 s on: answered at once, with no trade.
        clock.now += 2_000;
        expect(await client.token().catch((error: unknown) => error)).toBe(first);
        expect(trades()).toBe(1);
        // Past the slot, the next call trades again.
        clock.now += 15_000;
        expect(await client.token().catch((error: unknown) => error)).toBeInstanceOf(
          BrokerRefusedError
        );
        expect(trades()).toBe(2);
      },
    },
  ],
  [
    "connect refused twice, then granted: each refused connection is a trade, and the wait doubles",
    {
      answer: (n) => (n <= 2 ? "connect refused" : "granted"),
      budget: 10,
      drive: async ({ client, waits, lines, trades }) => {
        const token = await client.token(new AbortController().signal);
        expect(token.accessToken).toStartWith("granted-");
        expect(trades()).toBe(3);
        expect(waits).toEqual([15_000, 30_000]);
        expect(lines).toHaveLength(2);
        for (const line of lines) expect(line).toContain("http://broker.test");
      },
    },
  ],
  [
    "never answers: a caller's deadline ends its wait, told the signal before a trade has failed and the failure after",
    {
      answer: () => "silent",
      budget: 0,
      drive: async ({ client, waits }) => {
        const early = new AbortController();
        const before = client.token(early.signal);
        early.abort(new Error("the caller's own deadline"));
        expect(((await outcome(before)) as Error).message).toBe("the caller's own deadline");
        const late = new AbortController();
        const after = client.token(late.signal);
        await until(() => waits.length === 1, "at its first wait");
        late.abort();
        const told = await outcome(after);
        expect(told).toBeInstanceOf(TradeRetryingError);
        expect((told as Error).message).toContain("did not answer");
        client.close();
      },
    },
  ],
  [
    "B3: seven callers during a pending wait, a NATS reconnect's every 2 s, wake nothing",
    {
      answer: () => "pending",
      budget: 0,
      drive: async ({ client, clock, waits, trades, advance }) => {
        const first = client.token(new AbortController().signal).catch(() => undefined);
        await until(() => waits.length === 1, "at its first wait");
        for (let call = 0; call < 7; call++) {
          clock.now += 2_000;
          const deadline = new AbortController();
          const joined = client.token(deadline.signal);
          deadline.abort();
          expect(await outcome(joined)).toBeInstanceOf(RegistrationPendingError);
        }
        expect(trades()).toBe(1);
        // The schedule's own slot is what trades again.
        advance();
        await until(() => trades() === 2, "at its second trade");
        client.close();
        await first;
      },
    },
  ],
  [
    "a registration that lands just after a trade at the cap is in hand within 30 s",
    {
      // The registration lands 1 ms after the eighth pending trade, when the wait is at its cap.
      answer: (n, now) => {
        if (n === 8) LANDS_AT.at = now + 1;
        return n <= 8 || now < LANDS_AT.at ? "pending" : "granted";
      },
      budget: 20,
      drive: async ({ client, clock, waits, trades }) => {
        const token = await client.token();
        expect(token.accessToken).toStartWith("granted-");
        expect(trades()).toBe(9);
        const inHand = clock.now - LANDS_AT.at;
        // The worst case of the schedule: the whole cap, with the jitter at its longest.
        expect(inHand).toBeLessThan(30_000);
        expect(waits).toEqual([2_000, 4_000, 8_000, 16_000, 30_000, 30_000, 30_000, 30_000]);
      },
    },
  ],
];

/** When the registration of the last row lands, on the row's clock. */
const LANDS_AT = { at: Number.POSITIVE_INFINITY };

test.each(ROWS)("%s", async (_name, row) => {
  const one = await rig(row.answer, row.budget);
  try {
    await row.drive(one);
  } finally {
    one.client.close();
  }
});
