import { expect, spyOn, test } from "bun:test";
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { MessagesApiError } from "../skill-scenarios/messages-api";
import type { Fetch } from "../skill-scenarios/production-dispatch";
import {
  BudgetSpent,
  costUsd,
  createGateway,
  GATEWAY_TRIES,
  type GatewayOptions,
  type MessagesBody,
  PRICES,
} from "./gateway";

// The gateway's client, on F5 (testdata/): the model gateway's replies to request bodies made from
// F2, captured by plan task t1 (testdata/CAPTURE.md, "F5") with
// `bun $TMPDIR/infra4676/send.ts <body.json> > F5-<shape>.sse`, a scratch script that posts the
// body to the gateway's `/v1/messages` with the key read inside it; the body sets `stream`, so each
// reply is an SSE stream, kept with its `content-type` and `request-id` headers. Every fetch here
// is a fake: no test reaches a network.

const TESTDATA = path.join(import.meta.dir, "testdata");
const scratch = temporaryDirectory("skill-cuts-gateway", { real: true });

/** The body F5's replies answer: F2, a replayed history Oh My Pi 18.18.6 sent. */
const BODY = JSON.parse(
  readFileSync(path.join(TESTDATA, "F2-request.json"), "utf8")
) as MessagesBody;

/** F5's reply `shape` as the gateway sent it: its SSE body and its kept headers. */
function f5(shape: string): Response {
  const kept = JSON.parse(readFileSync(path.join(TESTDATA, `F5-${shape}.headers.json`), "utf8"));
  return new Response(readFileSync(path.join(TESTDATA, `F5-${shape}.sse`), "utf8"), {
    status: kept.status,
    headers: kept.headers,
  });
}

/** An Anthropic error answer. */
function refusal(status: number, type: string, message: string, headers = {}): Response {
  return new Response(JSON.stringify({ type: "error", error: { type, message } }), {
    status,
    headers: { "content-type": "application/json", ...headers },
  });
}

/** A key file in the scratch directory holding `key`. */
function keyFileHolding(key: string): string {
  const file = path.join(scratch, `key-${key.slice(0, 5)}-${Math.random()}`);
  writeFileSync(file, `${key}\n`, { mode: 0o600 });
  return file;
}

interface Sent {
  readonly url: string;
  readonly key: string | null;
  readonly body: unknown;
  readonly signal: AbortSignal;
}

/** A gateway whose fetch is a fake answering the n-th call (from 1) with `answer`, and the calls
 *  and back-off waits it saw. */
function world(
  answer: (n: number, sent: Sent) => Response | Promise<Response>,
  options: Partial<GatewayOptions> = {}
) {
  const sent: Sent[] = [];
  const slept: number[] = [];
  const fetch: Fetch = async (url, init) => {
    const signal = init.signal;
    if (!(signal instanceof AbortSignal)) throw new Error("the call carries no timeout signal");
    const one = {
      url,
      key: new Headers(init.headers).get("x-api-key"),
      body: JSON.parse(String(init.body)),
      signal,
    };
    sent.push(one);
    return answer(sent.length, one);
  };
  const gateway = createGateway({
    baseUrl: "http://gateway.test/",
    keyFile: keyFileHolding(fixtureSecret("gw")),
    concurrency: 4,
    timeoutMs: 5_000,
    fetch,
    sleep: async (ms) => {
      slept.push(ms);
    },
    ...options,
  });
  return { gateway, sent, slept };
}

/** A fetch answer that comes only when the try's signal ends it: a gateway that never answers. */
function untilAborted(signal: AbortSignal): Promise<Response> {
  const { promise, reject } = Promise.withResolvers<Response>();
  signal.addEventListener("abort", () => reject(signal.reason));
  return promise;
}

/** An answer whose headers came and whose body stops after `first` until the try is ended. */
function stalledBody(signal: AbortSignal, first: string): Response {
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(first));
      signal.addEventListener("abort", () => controller.error(signal.reason));
    },
  });
  return new Response(body, { status: 200, headers: { "content-type": "text/event-stream" } });
}

test("posts the body to <base>/v1/messages with the key file's key and reads a streamed reply whole", async () => {
  const key = fixtureSecret("gw");
  const { gateway, sent } = world(() => f5("skill-read"), { keyFile: keyFileHolding(key) });
  const reply = await gateway.send(BODY);
  expect(sent).toHaveLength(1);
  expect(sent[0]?.url).toBe("http://gateway.test/v1/messages");
  expect(sent[0]?.key).toBe(key);
  expect(sent[0]?.body).toEqual(BODY);
  expect(reply.requestId).toBe("req_011ChDNkcYfVK27QnBd1bAV7");
  expect(reply.message.model).toBe("claude-sonnet-5-5");
  expect(reply.message.stop_reason).toBe("tool_use");
  // The tool call's input arrives as JSON in pieces and is parsed whole when its block stops.
  expect(reply.message.content).toEqual([
    {
      type: "tool_use",
      id: "toolu_01WDxLVjvqwiht3W6X8n4wAn",
      name: "read",
      input: { path: "skill://alpha/references/x.md", i: "Reading alpha reference" },
      caller: { type: "direct" },
    },
  ]);
  // message_delta's counts are the final ones: output 84, where message_start said 16.
  expect(reply.message.usage).toMatchObject({
    input_tokens: 600,
    output_tokens: 84,
    cache_creation_input_tokens: 4836,
    cache_read_input_tokens: 8076,
    cache_creation: { ephemeral_5m_input_tokens: 0, ephemeral_1h_input_tokens: 4836 },
  });
});

test("reads every block of a streamed reply: text, thinking with its signature, and tool calls", async () => {
  const { gateway } = world((n) => f5(["text", "thinking", "skill-read-and-other"][n - 1] ?? ""));
  const text = await gateway.send(BODY);
  expect(text.message.stop_reason).toBe("end_turn");
  expect(text.message.content).toEqual([
    {
      type: "text",
      text: "I haven't read `references/x.md`, which step 1 requires first, and I can't call a tool this turn, so these are the skill's three steps as written, not results from running them:\n\n1. Read `references/x.md` before the first step.\n2. Write each step's result on one line.\n3. Stop after the third step.",
    },
  ]);

  const thinking = await gateway.send(BODY);
  const [thought, said] = thinking.message.content;
  expect(thought?.type).toBe("thinking");
  expect(typeof thought?.thinking === "string" && thought.thinking.length > 0).toBe(true);
  expect(typeof thought?.signature === "string" && thought.signature.length > 0).toBe(true);
  expect(said?.type).toBe("text");
  expect(String(said?.text)).toStartWith("1. Read `references/x.md` before the first step.");

  const four = await gateway.send(BODY);
  expect(four.message.content.map((block) => [block.type, block.name, block.input])).toEqual([
    ["text", undefined, undefined],
    ["tool_use", "read", { path: "skill://beta" }],
    ["tool_use", "bash", { command: "ls" }],
    ["tool_use", "read", { path: "skill://alpha/references/x.md" }],
  ]);
});

test("retries a 429 after the wait its retry-after asks for", async () => {
  const { gateway, sent, slept } = world((n) =>
    n === 1 ? refusal(429, "rate_limit_error", "slow down", { "retry-after": "7" }) : f5("text")
  );
  const reply = await gateway.send(BODY);
  expect(reply.message.stop_reason).toBe("end_turn");
  expect(sent).toHaveLength(2);
  expect(slept).toEqual([7000]);
});

test("backs off on a 5xx: 1 s, then 2 s", async () => {
  const { gateway, sent, slept } = world((n) =>
    n === 1
      ? refusal(503, "overloaded_error", "busy")
      : n === 2
        ? new Response("bad gateway", { status: 502 })
        : f5("text")
  );
  const reply = await gateway.send(BODY);
  expect(reply.message.stop_reason).toBe("end_turn");
  expect(sent).toHaveLength(3);
  expect(slept).toEqual([1000, 2000]);
});

test("does not retry a 400", async () => {
  const { gateway, sent, slept } = world(() =>
    refusal(400, "invalid_request_error", "max_tokens: too large")
  );
  const error = await gateway.send(BODY).catch((caught: unknown) => caught);
  expect(error).toBeInstanceOf(MessagesApiError);
  expect(error).toMatchObject({
    status: 400,
    message: "the model answered 400 invalid_request_error: max_tokens: too large",
  });
  expect(sent).toHaveLength(1);
  expect(slept).toEqual([]);
});

test("gives up after GATEWAY_TRIES tries of a 5xx, saying so", async () => {
  const { gateway, sent } = world(() => refusal(529, "overloaded_error", "busy"));
  await expect(gateway.send(BODY)).rejects.toThrow(
    `the model answered 529 overloaded_error: busy, on each of ${GATEWAY_TRIES} tries`
  );
  expect(sent).toHaveLength(GATEWAY_TRIES);
});

test("ends a call that passes its timeout and tries it again, its body read included", async () => {
  const { gateway, sent } = world(
    (n, { signal }) =>
      n === 1
        ? untilAborted(signal)
        : n === 2
          ? stalledBody(signal, "event: message_start\n")
          : f5("text"),
    // The timeout is messages-api.ts's own `AbortSignal.timeout`, which runs on the real clock.
    { timeoutMs: 50 }
  );
  const reply = await gateway.send(BODY);
  expect(reply.message.stop_reason).toBe("end_turn");
  expect(sent).toHaveLength(3);
  expect(sent[0]?.signal.aborted).toBe(true);
  expect(sent[1]?.signal.aborted).toBe(true);
});

test("tries again a stream cut short before message_stop", async () => {
  const whole = readFileSync(path.join(TESTDATA, "F5-text.sse"), "utf8");
  const cut = whole.slice(0, whole.indexOf("event: message_stop"));
  const { gateway, sent } = world((n) =>
    n === 1 ? new Response(cut, { status: 200 }) : f5("text")
  );
  const reply = await gateway.send(BODY);
  expect(reply.message.stop_reason).toBe("end_turn");
  expect(sent).toHaveLength(2);
});

test("sums usage per model with its cache reads and writes, and the dollars at list price", async () => {
  // F5's cache-read reply is the second send of the skill-read reply's body.
  const { gateway } = world((n) => f5(n === 1 ? "skill-read" : "cache-read"));
  const first = await gateway.send(BODY);
  const second = await gateway.send(BODY);
  // claude-sonnet-5-5 at $2 input, $10 output, $0.10 cache read and $4 a 1-hour cache write, per
  // million tokens. The first: 600 x 2 + 84 x 10 + 8076 x 0.10 + 4836 x 4 (all 1-hour) = 22191.6.
  // The second: 600 x 2 + 56 x 10 + 12912 x 0.10 = 3051.2.
  expect(first.spent).toMatchObject({
    model: "claude-sonnet-5-5",
    input: 600,
    output: 84,
    cacheRead: 8076,
    cacheWrite: 4836,
  });
  expect(first.spent.usd).toBeCloseTo(0.0221916, 10);
  expect(second.spent).toMatchObject({ input: 600, output: 56, cacheRead: 12912, cacheWrite: 0 });
  expect(second.spent.usd).toBeCloseTo(0.0030512, 10);
  const spend = gateway.spend();
  expect(Object.keys(spend.models)).toEqual(["claude-sonnet-5-5"]);
  expect(spend.models["claude-sonnet-5-5"]).toMatchObject({
    calls: 2,
    input: 1200,
    output: 140,
    cacheRead: 20988,
    cacheWrite: 4836,
  });
  expect(spend.models["claude-sonnet-5-5"]?.usd).toBeCloseTo(0.0252428, 10);
  expect(spend.usd).toBeCloseTo(0.0252428, 10);
});

test("prices a 5-minute cache write at its own rate, and a long Haiku prompt at the higher one", () => {
  const usage = {
    input_tokens: 1000,
    output_tokens: 100,
    cache_read_input_tokens: 2000,
    cache_creation_input_tokens: 3000,
    cache_creation: { ephemeral_5m_input_tokens: 1000, ephemeral_1h_input_tokens: 2000 },
  };
  // claude-opus-5-5: 1000 x 4 + 100 x 20 + 2000 x 0.20 + 1000 x 5 + 2000 x 8 = 27400.
  expect(costUsd(usage, PRICES["claude-opus-5-5"])).toBeCloseTo(0.0274, 10);
  // A write the usage does not split counts as 1-hour: 3000 x 8 in place of 1000 x 5 + 2000 x 8.
  const { cache_creation: _, ...unsplit } = usage;
  expect(costUsd(unsplit, PRICES["claude-opus-5-5"])).toBeCloseTo(0.0304, 10);
  // claude-haiku-5-5 over 100,000 prompt tokens: 100000 x 0.50 + 100 x 2.50 + 2000 x 0.05 +
  // 1000 x 0.625 + 2000 x 1 = 52975; at 100,000 or fewer, the lower rates.
  const haiku = PRICES["claude-haiku-5-5"];
  expect(costUsd({ ...usage, input_tokens: 100_000 }, haiku)).toBeCloseTo(0.052975, 10);
  expect(costUsd({ ...usage, input_tokens: 95_000 }, haiku)).toBeCloseTo(
    (95_000 * 0.1 + 100 * 0.5 + 2000 * 0.01 + 1000 * 0.125 + 2000 * 0.2) / 1e6,
    10
  );
});

test("refuses a body whose model has no price before any call, and takes a price passed in", async () => {
  const { gateway, sent } = world(() => f5("text"));
  await expect(gateway.send({ ...BODY, model: "acme-model-1" })).rejects.toThrow(
    "no list price for the model acme-model-1"
  );
  expect(sent).toHaveLength(0);

  const price = { input: 1, output: 1, cacheWrite5m: 1, cacheWrite1h: 1, cacheRead: 1 };
  const priced = world(() => f5("text"), { prices: { "acme-model-1": price } });
  // The reply says claude-sonnet-5-5 answered, so that model's own price counts it.
  const reply = await priced.gateway.send({ ...BODY, model: "acme-model-1" });
  expect(reply.spent.model).toBe("claude-sonnet-5-5");
  expect(priced.sent).toHaveLength(1);
});

test("the budget stops new calls and says so", async () => {
  const { gateway, sent } = world(() => f5("skill-read"), { budgetUsd: 0.02 });
  const first = await gateway.send(BODY);
  expect(first.spent.usd).toBeGreaterThan(0.02);
  const error = await gateway.send(BODY).catch((caught: unknown) => caught);
  expect(error).toBeInstanceOf(BudgetSpent);
  expect(String(error)).toContain(
    "the budget of $0.02 is spent: the replies so far cost $0.0222 at list price, so no new model call starts"
  );
  expect(sent).toHaveLength(1);
  expect(gateway.spend().models["claude-sonnet-5-5"]?.calls).toBe(1);
});

test("bounds the calls in flight", async () => {
  let inFlight = 0;
  let most = 0;
  const releases: (() => void)[] = [];
  // Wakes the test when a call reaches the fake.
  let arrived: (() => void) | undefined;
  const keyFile = keyFileHolding(fixtureSecret("gw"));
  const { gateway } = world(
    async () => {
      inFlight += 1;
      most = Math.max(most, inFlight);
      const { promise, resolve } = Promise.withResolvers<void>();
      releases.push(resolve);
      arrived?.();
      await promise;
      inFlight -= 1;
      return f5("text");
    },
    { concurrency: 2, keyFile }
  );
  const all = Promise.all(Array.from({ length: 5 }, () => gateway.send(BODY)));
  for (let released = 0; released < 5; released += 1) {
    // Two calls in flight, or the one call left.
    while (releases.length < Math.min(released + 2, 5)) {
      const { promise, resolve } = Promise.withResolvers<void>();
      arrived = resolve;
      await promise;
    }
    // A call let through would have read the key and reached the fake by the end of this read.
    await Bun.file(keyFile).text();
    expect(inFlight).toBe(Math.min(2, 5 - released));
    releases[released]?.();
  }
  expect(await all).toHaveLength(5);
  expect(most).toBe(2);
});

test("the key file's text is in no error, log line or thrown message", async () => {
  const key = fixtureSecret("gw");
  const keyFile = keyFileHolding(key);
  const printed = [
    spyOn(console, "log"),
    spyOn(console, "info"),
    spyOn(console, "warn"),
    spyOn(console, "error"),
    spyOn(console, "debug"),
  ];
  const errors: unknown[] = [];
  try {
    // A refusal that quotes the key it got.
    const quoting = world(() => refusal(401, "authentication_error", `bad key ${key}`), {
      keyFile,
    });
    errors.push(await quoting.gateway.send(BODY).catch((caught: unknown) => caught));
    expect(quoting.sent[0]?.key).toBe(key);
    // A connection whose failure names the key, on every try.
    const failing = world(
      () => {
        throw new Error(`socket closed after x-api-key: ${key}`);
      },
      { keyFile }
    );
    errors.push(await failing.gateway.send(BODY).catch((caught: unknown) => caught));
    // A stream that ends in an error naming a piece of the key, on every try.
    const streaming = world(
      () =>
        new Response(
          `event: error\ndata: {"type":"error","error":{"type":"api_error","message":"key ${key.slice(2, 20)}"}}\n\n`,
          { status: 200 }
        ),
      { keyFile }
    );
    errors.push(await streaming.gateway.send(BODY).catch((caught: unknown) => caught));
  } finally {
    for (const spy of printed) spy.mockRestore();
  }
  expect(errors).toHaveLength(3);
  const said = [
    ...errors.flatMap((error) =>
      error instanceof Error
        ? [error.message, String(error.stack), JSON.stringify(error), String(error)]
        : [String(error)]
    ),
    ...printed.flatMap((spy) => spy.mock.calls.map((call) => call.map(String).join(" "))),
  ];
  expect(errors.every((error) => error instanceof Error)).toBe(true);
  expect(said.join("\n")).toContain("<withheld>");
  for (let at = 0; at + 10 <= key.length; at += 1) {
    expect(said.filter((text) => text.includes(key.slice(at, at + 10)))).toEqual([]);
  }
});
