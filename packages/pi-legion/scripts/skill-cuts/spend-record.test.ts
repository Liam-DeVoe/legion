import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { postMessages } from "../skill-scenarios/messages-api";
import {
  conversationId,
  createSpendRecorder,
  POST_TIMEOUT_MS,
  type SpendRecorder,
} from "../skill-scenarios/spend-record";
import {
  bodyIdentity,
  createGateway,
  gatewaySessionId,
  type MessagesBody,
  type RequestBody,
  type ResponsesBody,
} from "./gateway";
import { type Collector, postedSpan, startCollector } from "./testdata/fixtures";

// The usage records of skill-scenarios/spend-record.ts (README.md, "Usage records"): the rows of
// the spend client's own test of spend_record.py, ported, and the rows of the runner's wiring
// (gateway.ts's `spend`, the request log's lines). Each row posts to a stand-in collector on
// 127.0.0.1 (testdata/fixtures.ts's `startCollector`), named by the environment the recorder is
// given, never this process's own; the replies are F5's and F9's (testdata/CAPTURE.md) or made up,
// and every fetch of a model call is a fake. No row reaches the gateway or a real collector.

const TESTDATA = path.join(import.meta.dir, "testdata");
const SPEND_RECORD = path.join(import.meta.dir, "..", "skill-scenarios", "spend-record.ts");
const scratch = temporaryDirectory("skill-cuts-spend", { real: true });

/** The headers Bun's fetch sends of its own beside the recorder's one. */
const BUN_HEADERS = [
  "accept",
  "accept-encoding",
  "connection",
  "content-length",
  "host",
  "user-agent",
];

/** A Messages answer, spend_record.py's test's own. */
const RESPONSE = {
  id: "msg_01TestResponse",
  type: "message",
  role: "assistant",
  model: "claude-haiku-4-5-20251001",
  content: [{ type: "text", text: "hi" }],
  stop_reason: "end_turn",
  usage: {
    input_tokens: 10,
    cache_read_input_tokens: 200,
    cache_creation_input_tokens: 30,
    cache_creation: { ephemeral_5m_input_tokens: 10, ephemeral_1h_input_tokens: 20 },
    output_tokens: 7,
    service_tier: "standard",
  },
};

/** The resource attributes each row's environment names: spaces, an empty pair, and a harness of
 *  its own, which the recorder's `script` replaces in its place. */
const RESOURCE =
  " theorem.project = spend-test ,theorem.person=a@example.com,,theorem.credential=probe,theorem.harness=omp";

/** F2, the Messages body F5's replies answer (its `metadata.user_id` names its session), and F8,
 *  the Responses body F9's replies answer (its `prompt_cache_key`). */
const BODY = JSON.parse(
  readFileSync(path.join(TESTDATA, "F2-request.json"), "utf8")
) as MessagesBody;
const RESPONSES_BODY = JSON.parse(
  readFileSync(path.join(TESTDATA, "F8-request.json"), "utf8")
) as ResponsesBody;

/** The id of F9-skill-read's response, as its `response.completed` event carries it: the
 *  gateway's long `resp_` id. */
const F9_ID: string = readFileSync(path.join(TESTDATA, "F9-skill-read.sse"), "utf8")
  .split("\n")
  .filter((line) => line.startsWith("data: {"))
  .map((line) => JSON.parse(line.slice("data: ".length)))
  .find((event) => event.type === "response.completed").response.id;

/** The fixture reply `stem` as the gateway sent it: its SSE body and its kept headers. */
function fixtureReply(stem: string): Response {
  const kept = JSON.parse(readFileSync(path.join(TESTDATA, `${stem}.headers.json`), "utf8"));
  return new Response(readFileSync(path.join(TESTDATA, `${stem}.sse`), "utf8"), {
    status: kept.status,
    headers: kept.headers,
  });
}

function text(key: string, value: string) {
  return { key, value: { stringValue: value } };
}

function integer(key: string, value: string) {
  return { key, value: { intValue: value } };
}

let collector: Collector;
beforeEach(() => {
  collector = startCollector();
});
afterEach(() => {
  collector.close();
});

/** A recorder whose environment names the collector (with a trailing "/", which is dropped) and
 *  RESOURCE, and `env` over them; and the warnings it wrote. */
function recorderFor(
  env: Record<string, string | undefined> = {},
  queueMax?: number
): { recorder: SpendRecorder; warnings: string[] } {
  const warnings: string[] = [];
  const recorder = createSpendRecorder({
    env: {
      OTEL_EXPORTER_OTLP_ENDPOINT: `${collector.url}/`,
      OTEL_RESOURCE_ATTRIBUTES: RESOURCE,
      ...env,
    },
    warn: (line) => warnings.push(line),
    ...(queueMax === undefined ? {} : { queueMax }),
  });
  return { recorder, warnings };
}

/** The attributes of the spans the collector took, in order. */
function postedAttributes(): Record<string, string | number>[] {
  return collector.posts.map((post) => postedSpan(post).attributes);
}

/** A port nothing listens on: one a server took and gave up. */
async function closedPort(): Promise<number> {
  const server = Bun.serve({ hostname: "127.0.0.1", port: 0, fetch: () => new Response() });
  const port = server.port;
  await server.stop(true);
  if (port === undefined) throw new Error("the server had no port");
  return port;
}

/** A gateway at a made-up address that records its answers with `spend`, whose fetch answers the
 *  n-th try (from 1) with `answer`. */
function gatewayWith(
  spend: SpendRecorder | null,
  answer: (n: number) => Response | Promise<Response>
) {
  const keyFile = path.join(scratch, `key-${Math.random()}`);
  writeFileSync(keyFile, fixtureSecret("gw"), { mode: 0o600 });
  let tries = 0;
  return createGateway({
    baseUrl: "http://gateway.test",
    keyFile,
    concurrency: 2,
    timeoutMs: 5_000,
    spend,
    fetch: async () => answer(++tries),
    sleep: async () => {},
  });
}

/** OTLP's decimal nanoseconds of epoch milliseconds `ms`. */
function nanosOf(ms: number): bigint {
  return BigInt(ms) * 1_000_000n;
}

describe("the span (spend_record.py's rules)", () => {
  test("a Messages answer gives one span at <endpoint>/v1/traces, of spend_record.py's shape, with the content type its one header", async () => {
    const { recorder, warnings } = recorderFor();
    recorder.record(RESPONSE, {
      model: "claude-haiku-4-5",
      startedAt: 1792824000500,
      endedAt: 1792824001250,
    });
    await recorder.flush();
    expect(warnings).toEqual([]);
    expect(collector.posts).toHaveLength(1);
    const post = collector.posts[0];
    if (post === undefined) throw new Error("no post");
    expect(post.path).toBe("/v1/traces");
    expect(post.headers["content-type"]).toBe("application/json");
    expect(Object.keys(post.headers).filter((name) => !BUN_HEADERS.includes(name))).toEqual([
      "content-type",
    ]);
    const got = JSON.parse(post.body);
    const span = got.resourceSpans[0].scopeSpans[0].spans[0];
    expect(span.traceId).toMatch(/^[0-9a-f]{32}$/);
    expect(span.spanId).toMatch(/^[0-9a-f]{16}$/);
    delete span.traceId;
    delete span.spanId;
    expect(got).toEqual({
      resourceSpans: [
        {
          resource: {
            attributes: [
              text("theorem.project", "spend-test"),
              text("theorem.person", "a@example.com"),
              text("theorem.credential", "probe"),
              text("theorem.harness", "script"),
              text("service.name", "spend-record"),
            ],
          },
          scopeSpans: [
            {
              scope: { name: "spend_record" },
              spans: [
                {
                  name: "chat claude-haiku-4-5",
                  kind: 3,
                  startTimeUnixNano: "1792824000500000000",
                  endTimeUnixNano: "1792824001250000000",
                  attributes: [
                    text("gen_ai.operation.name", "chat"),
                    text("gen_ai.provider.name", "anthropic"),
                    text("gen_ai.request.model", "claude-haiku-4-5"),
                    text("gen_ai.response.model", "claude-haiku-4-5-20251001"),
                    text("gen_ai.response.id", "msg_01TestResponse"),
                    integer("gen_ai.usage.input_tokens", "240"),
                    integer("gen_ai.usage.cache_read.input_tokens", "200"),
                    integer("gen_ai.usage.cache_creation.input_tokens", "30"),
                    integer("gen_ai.usage.cache_creation.ephemeral_5m_input_tokens", "10"),
                    integer("gen_ai.usage.cache_creation.ephemeral_1h_input_tokens", "20"),
                    integer("gen_ai.usage.output_tokens", "7"),
                    text("omp.gen_ai.auth.kind", "api_key"),
                  ],
                },
              ],
            },
          ],
        },
      ],
    });
  });

  test("an answer as JSON text goes to the traces endpoint whole, with its cost, and no header variable reaches the post", async () => {
    const { recorder, warnings } = recorderFor({
      OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: ` ${collector.url}/custom `,
      OTEL_EXPORTER_OTLP_HEADERS: "authorization=Bearer%20not-a-secret-generic",
      OTEL_EXPORTER_OTLP_TRACES_HEADERS: "authorization=Bearer%20not-a-secret-traces",
    });
    recorder.record(JSON.stringify(RESPONSE), {
      costUsd: 0.25,
      startedAt: new Date("2026-10-26T00:00:00.123Z"),
    });
    await recorder.flush();
    expect(warnings).toEqual([]);
    const post = collector.posts[0];
    if (post === undefined) throw new Error("no post");
    expect(collector.posts.map((each) => each.path)).toEqual(["/custom"]);
    expect(post.headers.authorization).toBeUndefined();
    expect(post.body).not.toContain("not-a-secret");
    const { span, attributes } = postedSpan(post);
    expect(attributes["omp.gen_ai.cost.estimated_usd"]).toBe(0.25);
    expect(span.startTimeUnixNano).toBe("1792972800123000000");
  });

  test("a streamed Messages reply is recorded from the whole message it assembles: message_start's message, with message_delta's counts", async () => {
    const { recorder, warnings } = recorderFor();
    const sse = readFileSync(path.join(TESTDATA, "F5-skill-read.sse"), "utf8");
    recorder.record(sse, { model: "claude-sonnet-5-5" });
    await recorder.flush();
    expect(warnings).toEqual([]);
    // F5-skill-read's message_start says 16 output tokens, and its message_delta 84.
    expect(postedAttributes()).toEqual([
      {
        "gen_ai.operation.name": "chat",
        "gen_ai.provider.name": "anthropic",
        "gen_ai.request.model": "claude-sonnet-5-5",
        "gen_ai.response.model": "claude-sonnet-5-5",
        "gen_ai.response.id": "msg_011ChDNkcj5yFJmo1LuDtSv7",
        "gen_ai.usage.input_tokens": "13512",
        "gen_ai.usage.cache_read.input_tokens": "8076",
        "gen_ai.usage.cache_creation.input_tokens": "4836",
        "gen_ai.usage.cache_creation.ephemeral_5m_input_tokens": "0",
        "gen_ai.usage.cache_creation.ephemeral_1h_input_tokens": "4836",
        "gen_ai.usage.output_tokens": "84",
        "omp.gen_ai.auth.kind": "api_key",
      },
    ]);
  });

  test("a chat completion and a Responses answer, the latter whole or streamed, carry their own counts", async () => {
    const chat = {
      id: "chatcmpl-test",
      object: "chat.completion",
      model: "gpt-6.1-sol",
      service_tier: "default",
      choices: [{ message: { role: "assistant", content: "ok" }, finish_reason: "stop" }],
      usage: {
        prompt_tokens: 13820,
        completion_tokens: 300,
        total_tokens: 14120,
        prompt_tokens_details: { cached_tokens: 2287, cache_write_tokens: 11530 },
        completion_tokens_details: { reasoning_tokens: 120 },
      },
    };
    const responses = {
      id: "resp_test",
      object: "response",
      model: "gpt-6.1-sol",
      service_tier: "flex",
      usage: {
        input_tokens: 5000,
        output_tokens: 800,
        total_tokens: 5800,
        input_tokens_details: { cached_tokens: 4096, cache_write_tokens: 0 },
        output_tokens_details: { reasoning_tokens: 500 },
      },
    };
    const { recorder, warnings } = recorderFor();
    recorder.record(chat, { model: "openai/gpt-6.1-sol", costUsd: "0.0123", sessionId: "run-1" });
    await recorder.flush();
    recorder.record(responses);
    await recorder.flush();
    recorder.record(readFileSync(path.join(TESTDATA, "F9-skill-read.sse"), "utf8"));
    await recorder.flush();
    expect(warnings).toEqual([]);
    const spans = collector.posts.map((post) => postedSpan(post).span.attributes);
    expect(spans[0]).toEqual([
      text("gen_ai.operation.name", "chat"),
      text("gen_ai.provider.name", "openai"),
      text("gen_ai.request.model", "openai/gpt-6.1-sol"),
      text("gen_ai.response.model", "gpt-6.1-sol"),
      text("gen_ai.response.id", "chatcmpl-test"),
      text("gen_ai.conversation.id", "run-1"),
      integer("gen_ai.usage.input_tokens", "13820"),
      integer("gen_ai.usage.cache_read.input_tokens", "2287"),
      integer("gen_ai.usage.cache_creation.input_tokens", "11530"),
      integer("gen_ai.usage.output_tokens", "300"),
      integer("gen_ai.usage.reasoning.output_tokens", "120"),
      text("openai.response.service_tier", "default"),
      text("omp.gen_ai.auth.kind", "api_key"),
      { key: "omp.gen_ai.cost.estimated_usd", value: { doubleValue: 0.0123 } },
    ]);
    expect(spans[1]).toEqual([
      text("gen_ai.operation.name", "chat"),
      text("gen_ai.provider.name", "openai"),
      text("gen_ai.response.model", "gpt-6.1-sol"),
      text("gen_ai.response.id", "resp_test"),
      integer("gen_ai.usage.input_tokens", "5000"),
      integer("gen_ai.usage.cache_read.input_tokens", "4096"),
      integer("gen_ai.usage.cache_creation.input_tokens", "0"),
      integer("gen_ai.usage.output_tokens", "800"),
      integer("gen_ai.usage.reasoning.output_tokens", "500"),
      text("openai.response.service_tier", "flex"),
      text("omp.gen_ai.auth.kind", "api_key"),
    ]);
    // F9-skill-read's response.completed: its input count holds the 7982 tokens it wrote to the
    // cache, so it is sent as it is.
    expect(spans[2]).toEqual([
      text("gen_ai.operation.name", "chat"),
      text("gen_ai.provider.name", "openai"),
      text("gen_ai.response.model", "gpt-6.1-sol"),
      text("gen_ai.response.id", F9_ID),
      integer("gen_ai.usage.input_tokens", "7985"),
      integer("gen_ai.usage.cache_read.input_tokens", "0"),
      integer("gen_ai.usage.cache_creation.input_tokens", "7982"),
      integer("gen_ai.usage.output_tokens", "30"),
      integer("gen_ai.usage.reasoning.output_tokens", "0"),
      text("openai.response.service_tier", "default"),
      text("omp.gen_ai.auth.kind", "api_key"),
    ]);
  });

  test("a Claude chat completion is anthropic with the gateway's cache counts; the provider follows the model, and a provider named wins", async () => {
    const claude = {
      id: "chatcmpl-claude",
      object: "chat.completion",
      model: "claude-sonnet-5-5",
      service_tier: null,
      usage: {
        prompt_tokens: 3734,
        completion_tokens: 4,
        total_tokens: 3738,
        prompt_tokens_details: {
          cached_tokens: 0,
          cache_write_tokens: 3723,
          cache_creation_token_details: {
            ephemeral_5m_input_tokens: 0,
            ephemeral_1h_input_tokens: 3723,
          },
        },
        completion_tokens_details: { reasoning_tokens: 0 },
        cache_creation_input_tokens: 3723,
        cache_read_input_tokens: 0,
        service_tier: "standard",
      },
    };
    const bare = {
      id: "chatcmpl-bare",
      model: "o-test",
      service_tier: "default",
      usage: {
        prompt_tokens: 50,
        completion_tokens: 7,
        prompt_tokens_details: { cached_tokens: 40 },
      },
    };
    const { recorder, warnings } = recorderFor();
    recorder.record(claude);
    recorder.record({ ...claude, service_tier: "standard" });
    recorder.record({ ...RESPONSE, model: "gpt-6.1-sol" });
    recorder.record({ ...claude, model: "gpt-6.1-sol" }, { provider: "anthropic" });
    recorder.record(bare);
    recorder.record({
      id: "resp_bare",
      object: "response",
      usage: { input_tokens: 9, output_tokens: 5 },
    });
    recorder.record({ ...RESPONSE, model: "o-test" });
    await recorder.flush();
    expect(warnings).toEqual([]);
    const got = postedAttributes();
    expect(got[0]).toMatchObject({
      "gen_ai.provider.name": "anthropic",
      "gen_ai.usage.input_tokens": "3734",
      "gen_ai.usage.cache_read.input_tokens": "0",
      "gen_ai.usage.cache_creation.input_tokens": "3723",
      "gen_ai.usage.cache_creation.ephemeral_5m_input_tokens": "0",
      "gen_ai.usage.cache_creation.ephemeral_1h_input_tokens": "3723",
      "gen_ai.usage.output_tokens": "4",
    });
    // A service tier at the answer's top level is OpenAI's attribute: a Claude answer sends none.
    expect(got[0]?.["openai.response.service_tier"]).toBeUndefined();
    expect(got[1]?.["openai.response.service_tier"]).toBeUndefined();
    expect(got.slice(2).map((each) => each["gen_ai.provider.name"])).toEqual([
      "openai",
      "anthropic",
      "openai",
      "openai",
      "anthropic",
    ]);
    // A usage alone (no `object`) names a chat completion, whose counts are read so.
    expect(got[4]).toMatchObject({
      "gen_ai.usage.input_tokens": "50",
      "gen_ai.usage.cache_read.input_tokens": "40",
      "gen_ai.usage.output_tokens": "7",
      "openai.response.service_tier": "default",
    });
  });

  test("a cost that is not finite, or text that reads as no number, is left out, and the span is sent all the same", async () => {
    const { recorder, warnings } = recorderFor();
    for (const costUsd of [Number.NaN, Number.POSITIVE_INFINITY, "inf", "n/a", "", "0x10", true]) {
      recorder.record(RESPONSE, { costUsd });
    }
    await recorder.flush();
    expect(warnings).toEqual([]);
    const got = postedAttributes();
    expect(got).toHaveLength(7);
    for (const attributes of got) {
      expect(attributes["omp.gen_ai.cost.estimated_usd"]).toBeUndefined();
      expect(attributes["gen_ai.response.id"]).toBe("msg_01TestResponse");
    }
  });

  test("the resource attributes keep every pair, each key and value trimmed and each value percent-decoded; a pair with no = is skipped", async () => {
    const { recorder } = recorderFor({
      OTEL_RESOURCE_ATTRIBUTES:
        "theorem.project=alpha, theorem.lane = lane-a ,orphan,theorem.person=a%40example.com,theorem.subproject=50%25%zz,theorem.project=beta",
      OTEL_SERVICE_NAME: " runner ",
    });
    recorder.record(RESPONSE);
    await recorder.flush();
    const post = collector.posts[0];
    if (post === undefined) throw new Error("no post");
    expect(JSON.parse(post.body).resourceSpans[0].resource.attributes).toEqual([
      text("theorem.project", "beta"),
      text("theorem.lane", "lane-a"),
      text("theorem.person", "a@example.com"),
      text("theorem.subproject", "50%%zz"),
      text("service.name", "runner"),
      text("theorem.harness", "script"),
    ]);
  });

  test("an answer it cannot read, or a time that is no time, never throws: one warning, and nothing is sent", async () => {
    const { recorder, warnings } = recorderFor();
    const bad = (value: unknown) => value as number;
    for (const answer of ["not json", new TextEncoder().encode("[1]"), null, 42, { usage: "x" }]) {
      recorder.record(answer, { startedAt: bad("bad") });
    }
    recorder.record({ id: "m2", usage: { output_tokens: 1 } }, { startedAt: bad({}) });
    recorder.record("event: ping\ndata: {}\n\n");
    await recorder.flush();
    expect(warnings).toHaveLength(1);
    expect(warnings[0]).toContain("spend-record: could not build the spend record");
    expect(collector.posts).toEqual([]);
  });

  test("no endpoint in the environment never throws, and says so once", async () => {
    const { recorder, warnings } = recorderFor({ OTEL_EXPORTER_OTLP_ENDPOINT: undefined });
    recorder.record(RESPONSE);
    recorder.record(RESPONSE);
    await recorder.flush();
    expect(warnings).toEqual([
      "spend-record: no OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: no spend record sent (further failures are not reported)",
    ]);
    expect(collector.posts).toEqual([]);
  });

  test("an answer of the collector that is not 2xx never throws, and is reported once", async () => {
    collector.status = 500;
    const { recorder, warnings } = recorderFor();
    recorder.record(RESPONSE);
    recorder.record(RESPONSE);
    await recorder.flush();
    expect(collector.posts).toHaveLength(2);
    expect(warnings).toEqual([
      "spend-record: the collector answered HTTP 500 (further failures are not reported)",
    ]);
  });

  test("a full queue drops each span past it with one warning, record never waits, and flush waits for the ones it took", async () => {
    // One sender, a queue of 2, a collector that takes 300 ms a post (a real post on a real clock:
    // the queue fills while one is in flight): of 6 quick records the sender holds one, the queue
    // two, and the other three are dropped.
    collector.delayMs = 300;
    const { recorder, warnings } = recorderFor({}, 2);
    recorder.record(RESPONSE);
    // The sender starts once `record` has returned, and takes the first span off the queue.
    await Promise.resolve();
    const started = performance.now();
    for (let n = 0; n < 5; n++) recorder.record(RESPONSE);
    expect(performance.now() - started).toBeLessThan(50);
    expect(warnings).toEqual([
      "spend-record: 2 spend records are waiting to be sent: this one is dropped (further failures are not reported)",
    ]);
    await recorder.flush(10_000);
    expect(collector.posts).toHaveLength(3);
  });

  test("flush returns within its limit while a post hangs, and the post ends at POST_TIMEOUT_MS, reported once", async () => {
    // A collector that holds each post past POST_TIMEOUT_MS: the limits are of real posts on the
    // real clock, which no fake timer ends.
    collector.delayMs = 10_000;
    const { recorder, warnings } = recorderFor();
    const idle = performance.now();
    await recorder.flush();
    expect(performance.now() - idle).toBeLessThan(50);
    const recorded = performance.now();
    recorder.record(RESPONSE);
    const flushed = performance.now();
    await recorder.flush(300);
    const waited = performance.now() - flushed;
    expect(waited).toBeGreaterThanOrEqual(290);
    expect(waited).toBeLessThan(1_000);
    expect(warnings).toEqual([]);
    await recorder.flush(10_000);
    const ended = performance.now() - recorded;
    expect(ended).toBeGreaterThanOrEqual(POST_TIMEOUT_MS - 50);
    expect(ended).toBeLessThan(POST_TIMEOUT_MS + 1_000);
    expect(warnings).toHaveLength(1);
    expect(warnings[0]).toContain("spend-record: could not reach the collector: TimeoutError");
  });

  test("a proxy variable of the environment never gets the post, which a plain fetch in that environment sends to it", async () => {
    // A process of its own for each environment, since Bun's fetch reads the proxy variables of
    // its process: the recorder posts straight to the collector, and the same process's plain
    // fetch to the collector goes through the stand-in proxy, which answers it itself. A no_proxy
    // of a range changes nothing.
    const proxy = startCollector();
    try {
      const script = path.join(scratch, "proxied.ts");
      writeFileSync(
        script,
        [
          `import { processSpendRecorder } from ${JSON.stringify(SPEND_RECORD)};`,
          "const recorder = processSpendRecorder();",
          `recorder.record(${JSON.stringify(RESPONSE)}, { model: "claude-haiku-4-5" });`,
          "await recorder.flush(5000);",
          'const plain = await fetch(process.env.CONTROL_URL ?? "", { method: "POST", body: "{}" });',
          "process.stdout.write(String(plain.status));",
        ].join("\n")
      );
      for (const variables of [
        { http_proxy: proxy.url },
        { HTTP_PROXY: proxy.url },
        {
          http_proxy: proxy.url,
          HTTP_PROXY: proxy.url,
          no_proxy: "100.64.0.0/10",
          NO_PROXY: "100.64.0.0/10",
        },
      ]) {
        collector.posts.length = 0;
        proxy.posts.length = 0;
        const child = Bun.spawn([process.execPath, script], {
          env: {
            PATH: process.env.PATH ?? "",
            HOME: scratch,
            OTEL_EXPORTER_OTLP_ENDPOINT: collector.url,
            OTEL_RESOURCE_ATTRIBUTES: "theorem.project=spend-test",
            CONTROL_URL: `${collector.url}/control`,
            ...variables,
          },
          stdin: "ignore",
          stdout: "pipe",
          stderr: "pipe",
        });
        const [stdout, stderr, code] = await Promise.all([
          new Response(child.stdout).text(),
          new Response(child.stderr).text(),
          child.exited,
        ]);
        expect({ code, stdout, stderr }).toEqual({ code: 0, stdout: "200", stderr: "" });
        expect(collector.posts.map((post) => post.path)).toEqual(["/v1/traces"]);
        expect(proxy.posts.map((post) => post.path)).toEqual(["/control"]);
      }
    } finally {
      proxy.close();
    }
  }, 30_000);
});

describe("the gateway's records (gateway.ts's `spend`)", () => {
  test("each answer of each API gives exactly one span, with the body's model, its session id, and the try's real start", async () => {
    const { recorder, warnings } = recorderFor();
    const json = {
      ...RESPONSE,
      model: "claude-sonnet-5-5",
      id: "msg_alpha_json",
    };
    const plain: MessagesBody = {
      model: "claude-sonnet-5-5",
      max_tokens: 16,
      messages: [{ role: "user", content: "hi" }],
    };
    const calls: [RequestBody, () => Response][] = [
      [plain, () => Response.json(json)],
      [BODY, () => fixtureReply("F5-skill-read")],
      [RESPONSES_BODY, () => fixtureReply("F9-skill-read")],
    ];
    for (const [at, [body, reply]] of calls.entries()) {
      // Each answer comes 100 ms after its try is sent, on the real clock that the span's times
      // are read from: the span starts when the try was sent, not when its answer ended.
      const gateway = gatewayWith(recorder, async () => {
        await Bun.sleep(100);
        return reply();
      });
      const before = Date.now();
      await gateway.send(body);
      const after = Date.now();
      await recorder.flush();
      const post = collector.posts[at];
      if (post === undefined) throw new Error(`no span of answer ${at + 1}`);
      const start = BigInt(postedSpan(post).span.startTimeUnixNano as string);
      const end = BigInt(postedSpan(post).span.endTimeUnixNano as string);
      expect(start).toBeGreaterThanOrEqual(nanosOf(before));
      expect(end - start).toBeGreaterThanOrEqual(nanosOf(95));
      expect(end).toBeLessThanOrEqual(nanosOf(after));
    }
    expect(warnings).toEqual([]);
    expect(collector.posts).toHaveLength(3);
    const got = postedAttributes();
    expect(got[0]).toMatchObject({
      "gen_ai.provider.name": "anthropic",
      "gen_ai.request.model": "claude-sonnet-5-5",
      "gen_ai.response.id": "msg_alpha_json",
      "gen_ai.usage.input_tokens": "240",
      "gen_ai.usage.output_tokens": "7",
    });
    expect(got[0]?.["gen_ai.conversation.id"]).toBeUndefined();
    // F2's `metadata.user_id` is `{"session_id": …}`: the gateway logs that session id.
    expect(got[1]).toMatchObject({
      "gen_ai.provider.name": "anthropic",
      "gen_ai.request.model": "claude-sonnet-5-5",
      "gen_ai.response.id": "msg_011ChDNkcj5yFJmo1LuDtSv7",
      "gen_ai.conversation.id": "01a1f6b4-84fb-757c-b2ff-8f5bb41915cc",
      "gen_ai.usage.input_tokens": "13512",
      "gen_ai.usage.output_tokens": "84",
    });
    // F8's `prompt_cache_key` is the session id the gateway logs for a Responses body.
    expect(got[2]).toMatchObject({
      "gen_ai.provider.name": "openai",
      "gen_ai.request.model": "gpt-6.1-sol",
      "gen_ai.response.id": F9_ID,
      "gen_ai.conversation.id": "01a1f80e-09d7-75c5-b4ae-a3831a485a57",
      "gen_ai.usage.input_tokens": "7985",
      "gen_ai.usage.output_tokens": "30",
    });
  });

  test("postMessages hands a chat completion's answer to its `spend` too, which records one span", async () => {
    const { recorder, warnings } = recorderFor();
    const chat = {
      id: "chatcmpl-alpha",
      object: "chat.completion",
      model: "gpt-6.1-sol",
      usage: {
        prompt_tokens: 120,
        completion_tokens: 9,
        prompt_tokens_details: { cached_tokens: 100 },
      },
    };
    const answer = await postMessages({
      url: "http://gateway.test",
      path: "/v1/chat/completions",
      auth: "bearer",
      key: async () => "not-a-secret-key",
      body: JSON.stringify({ model: "gpt-6.1-sol", messages: [] }),
      fetch: async () => Response.json(chat),
      sleep: async () => {},
      tries: 1,
      timeoutMs: 5_000,
      accept: (reply) => JSON.parse(reply).id,
      spend: (reply, times) => recorder.record(reply, { model: "gpt-6.1-sol", ...times }),
    });
    expect(answer).toBe("chatcmpl-alpha");
    await recorder.flush();
    expect(warnings).toEqual([]);
    expect(postedAttributes()).toEqual([
      {
        "gen_ai.operation.name": "chat",
        "gen_ai.provider.name": "openai",
        "gen_ai.request.model": "gpt-6.1-sol",
        "gen_ai.response.model": "gpt-6.1-sol",
        "gen_ai.response.id": "chatcmpl-alpha",
        "gen_ai.usage.input_tokens": "120",
        "gen_ai.usage.cache_read.input_tokens": "100",
        "gen_ai.usage.output_tokens": "9",
        "omp.gen_ai.auth.kind": "api_key",
      },
    ]);
  });

  test("every ok answer of a try is recorded, one the reply's reader refuses and a content-policy refusal among them; a 429, and a stream that ends before its answer starts, are not", async () => {
    const { recorder, warnings } = recorderFor();
    const unread = {
      id: "msg_unread",
      model: "claude-sonnet-5-5",
      usage: { input_tokens: 5, output_tokens: 2 },
    };
    const good = { ...RESPONSE, id: "msg_good", model: "claude-sonnet-5-5" };
    const answer = (n: number) =>
      n === 1
        ? new Response('{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}', {
            status: 429,
          })
        : Response.json(n === 2 ? unread : good);
    const plain: MessagesBody = {
      model: "claude-sonnet-5-5",
      max_tokens: 16,
      messages: [{ role: "user", content: "hi" }],
    };
    const reply = await gatewayWith(recorder, answer).send(plain);
    expect(reply.message.id).toBe("msg_good");
    const start = {
      type: "message_start",
      message: {
        id: "msg_refused",
        type: "message",
        role: "assistant",
        model: "claude-sonnet-5-5",
        content: [],
        stop_reason: null,
        usage: { input_tokens: 50, output_tokens: 1 },
      },
    };
    const errorEvent = [
      "event: error",
      'data: {"type":"error","error":{"type":"cyber_policy","message":"refused"}}',
      "",
      "",
    ];
    const streamed = (events: string) =>
      gatewayWith(
        recorder,
        () =>
          new Response(events, { status: 200, headers: { "content-type": "text/event-stream" } })
      );
    const refused = streamed(
      ["event: message_start", `data: ${JSON.stringify(start)}`, "", ...errorEvent].join("\n")
    );
    await expect(refused.send({ ...plain, stream: true })).rejects.toThrow("cyber_policy");
    // A stream that ends in its error before its answer starts holds no usage: no span, and no
    // warning.
    const early = streamed(errorEvent.join("\n"));
    await expect(early.send({ ...plain, stream: true })).rejects.toThrow("cyber_policy");
    await recorder.flush();
    expect(warnings).toEqual([]);
    expect(postedAttributes().map((each) => each["gen_ai.response.id"])).toEqual([
      "msg_unread",
      "msg_good",
      "msg_refused",
    ]);
    expect(postedAttributes()[2]).toMatchObject({
      "gen_ai.usage.input_tokens": "50",
      "gen_ai.usage.output_tokens": "1",
    });
  });

  test("an unreachable collector (a closed port) neither throws nor slows a gateway call by 25 ms, and is reported once", async () => {
    const port = await closedPort();
    const warnings: string[] = [];
    const recorder = createSpendRecorder({
      env: { OTEL_EXPORTER_OTLP_ENDPOINT: `http://127.0.0.1:${port}` },
      warn: (line) => warnings.push(line),
    });
    /** The median milliseconds of 15 sends of F2 answered by F5, recorded with `spend`. */
    const median = async (spend: SpendRecorder | null) => {
      const gateway = gatewayWith(spend, () => fixtureReply("F5-skill-read"));
      const took: number[] = [];
      for (let n = 0; n < 15; n++) {
        const started = performance.now();
        await gateway.send(BODY);
        took.push(performance.now() - started);
      }
      return took.sort((a, b) => a - b)[7] ?? Number.NaN;
    };
    const without = await median(null);
    const recorded = await median(recorder);
    expect(recorded - without).toBeLessThan(25);
    await recorder.flush();
    expect(warnings).toHaveLength(1);
    expect(warnings[0]).toContain("spend-record: could not reach the collector");
  });

  test("the session id a span sends is the one the gateway logs: user_id's session_id, else a Responses body's prompt_cache_key, else its user", () => {
    expect(conversationId('{"session_id":"s-1"}')).toBe("s-1");
    expect(conversationId('{"device_id":"d-1","account_uuid":"","session_id":"s-2"}')).toBe("s-2");
    expect(conversationId("cache-key-3")).toBe("cache-key-3");
    expect(conversationId('{"device_id":"d-1"}')).toBeUndefined();
    expect(conversationId("")).toBeUndefined();
    expect(conversationId(null)).toBeUndefined();
    const messages = { model: "claude-sonnet-5-5", messages: [] };
    expect(gatewaySessionId({ ...messages, metadata: { user_id: '{"session_id":"s-4"}' } })).toBe(
      "s-4"
    );
    expect(gatewaySessionId(messages)).toBeUndefined();
    const responses = { model: "gpt-6.1-sol", input: [] };
    expect(gatewaySessionId({ ...responses, prompt_cache_key: "key-5", user: "user-5" })).toBe(
      "key-5"
    );
    expect(gatewaySessionId({ ...responses, user: "user-6" })).toBe("user-6");
    expect(gatewaySessionId(responses)).toBeUndefined();
  });
});

describe("a line of the request log (`recordLogged`)", () => {
  test("a Messages and a Responses line each give the span of their call, with the line's start, ids and counts, as the live span of that call pairs", async () => {
    const { recorder, warnings } = recorderFor();
    const live = gatewayWith(recorder, (n) =>
      fixtureReply(n === 1 ? "F5-skill-read" : "F9-skill-read")
    );
    const lines: Record<string, unknown>[] = [];
    for (const body of [BODY, RESPONSES_BODY]) {
      const reply = await live.send(body);
      // The fields cli.ts's `loggedGateway` writes, from the same expressions.
      lines.push({
        at: reply.sentAt,
        sentAt: reply.sentAt,
        inFlight: reply.inFlight,
        kind: "replay",
        model: body.model,
        stream: true,
        userId: bodyIdentity(body),
        answeredBy: reply.message.model,
        messageId: reply.message.id,
        requestId: reply.requestId ?? null,
        durationMs: reply.durationMs,
        stopReason: reply.message.stop_reason,
        usage: reply.message.usage,
        usd: reply.spent.usd,
      });
    }
    await recorder.flush();
    lines.push({
      at: "2026-11-23T10:00:00.000Z",
      sentAt: null,
      kind: "replay",
      model: "gpt-6.1-sol",
      userId: null,
      error: "MessagesApiError",
      status: 529,
    });
    for (const line of lines) recorder.recordLogged(JSON.parse(JSON.stringify(line)));
    await recorder.flush();
    expect(warnings).toEqual([]);
    // Two live spans, then two of the lines: the failed call's line gives none.
    expect(collector.posts).toHaveLength(4);
    const [liveMessages, liveResponses, loggedMessages, loggedResponses] = collector.posts.map(
      (post) => postedSpan(post)
    );
    const [messagesLine, responsesLine] = lines;
    for (const [logged, line] of [
      [loggedMessages, messagesLine],
      [loggedResponses, responsesLine],
    ] as const) {
      const start = nanosOf(Date.parse(String(line?.sentAt)));
      expect(logged?.span.startTimeUnixNano).toBe(String(start));
      expect(logged?.span.endTimeUnixNano).toBe(String(start + nanosOf(Number(line?.durationMs))));
    }
    expect(loggedMessages?.attributes).toEqual({
      "gen_ai.operation.name": "chat",
      "gen_ai.provider.name": "anthropic",
      "gen_ai.request.model": "claude-sonnet-5-5",
      "gen_ai.response.model": "claude-sonnet-5-5",
      "gen_ai.response.id": "msg_011ChDNkcj5yFJmo1LuDtSv7",
      "gen_ai.conversation.id": "01a1f6b4-84fb-757c-b2ff-8f5bb41915cc",
      "gen_ai.usage.input_tokens": "13512",
      "gen_ai.usage.cache_read.input_tokens": "8076",
      "gen_ai.usage.cache_creation.input_tokens": "4836",
      "gen_ai.usage.cache_creation.ephemeral_5m_input_tokens": "0",
      "gen_ai.usage.cache_creation.ephemeral_1h_input_tokens": "4836",
      "gen_ai.usage.output_tokens": "84",
      "omp.gen_ai.auth.kind": "api_key",
    });
    expect(loggedResponses?.attributes).toEqual({
      "gen_ai.operation.name": "chat",
      "gen_ai.provider.name": "openai",
      "gen_ai.request.model": "gpt-6.1-sol",
      "gen_ai.response.model": "gpt-6.1-sol",
      "gen_ai.response.id": F9_ID,
      "gen_ai.conversation.id": "01a1f80e-09d7-75c5-b4ae-a3831a485a57",
      "gen_ai.usage.input_tokens": "7985",
      "gen_ai.usage.cache_read.input_tokens": "0",
      "gen_ai.usage.cache_creation.input_tokens": "7982",
      "gen_ai.usage.output_tokens": "30",
      "gen_ai.usage.reasoning.output_tokens": "0",
      "omp.gen_ai.auth.kind": "api_key",
    });
    // What pairs a span with the gateway's record of its call: the model and the provider, the
    // prompt and output tokens, the ids, and a start within 900 s of the call's.
    const pairing = [
      "gen_ai.provider.name",
      "gen_ai.request.model",
      "gen_ai.response.model",
      "gen_ai.response.id",
      "gen_ai.conversation.id",
      "gen_ai.usage.input_tokens",
      "gen_ai.usage.output_tokens",
    ];
    for (const [one, other] of [
      [liveMessages, loggedMessages],
      [liveResponses, loggedResponses],
    ] as const) {
      for (const key of pairing) expect(other?.attributes[key]).toBe(one?.attributes[key] ?? "");
      const apart =
        BigInt(String(one?.span.startTimeUnixNano)) - BigInt(String(other?.span.startTimeUnixNano));
      expect(apart >= 0n && apart < nanosOf(900_000)).toBe(true);
    }
  });

  test("a line that holds no usage or no start never throws: one warning, and nothing is sent", async () => {
    const { recorder, warnings } = recorderFor();
    recorder.recordLogged({ sentAt: "2026-11-23T10:00:00.000Z", model: "claude-sonnet-5-5" });
    recorder.recordLogged({ sentAt: "not a time", usage: { input_tokens: 1, output_tokens: 1 } });
    recorder.recordLogged("not a line");
    await recorder.flush();
    expect(warnings).toHaveLength(1);
    expect(warnings[0]).toContain("spend-record: could not build the spend record");
    expect(collector.posts).toEqual([]);
  });
});
