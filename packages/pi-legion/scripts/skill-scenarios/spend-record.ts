/**
 * Usage records of the model calls a program makes itself: one OTLP/HTTP JSON span, shaped like
 * Oh My Pi's model-response span, for each model answer, posted to the spend collector. A program
 * with no agent harness (Oh My Pi, Claude Code and Codex record their own calls) records each
 * answer itself. This is the spend client's `spend_record.py` in TypeScript, with its rules and its
 * span; skill-cuts/gateway.ts records each answer of the runner's gateway through it
 * (skill-cuts/README.md, "Usage records").
 *
 * Exports:
 * - `createSpendRecorder(options)`: a recorder that reads its environment (`options.env`,
 *   `process.env` by default) as it records. `processSpendRecorder()`: this process's one
 *   recorder, on `process.env`, its warning written to stderr.
 * - `recorder.record(answer, call)`: queues the span of one model answer and returns at once.
 *   `answer` is the answer as an object, or as the text the API sent: one JSON object, or a
 *   streamed (SSE) reply, read as the whole answer it assembles (`wholeAnswer`); a stream that
 *   ended before its answer started (as a refusal can) holds no usage, and records nothing.
 *   `call` says the rest (SpendCall): the model the request named, when the call started and
 *   ended, its cost, its provider, its session id.
 * - `recorder.recordLogged(row)`: queues the span of one call from one line of the runner's
 *   request log (skill-cuts/cli.ts's `loggedGateway`), with the line's own start, duration, model,
 *   ids and usage (`loggedAnswer`); a line of a failed call (`error`) is passed over.
 * - `recorder.flush(timeoutMs)`: waits up to `timeoutMs` in all (FLUSH_MS by default) for the
 *   queue to empty, and says whether it did: a backfill queues no more until it has.
 * - `conversationId(identity)`: the session id in a body's session id field, as the gateway logs
 *   it.
 *
 * Which API answered is read from the answer: `object` "response" is a Responses answer, `object`
 * "chat.completion" or a usage with `prompt_tokens` or `completion_tokens` is a chat completion,
 * and any other answer is a Messages answer. An OpenAI count of input tokens holds its cached part,
 * as Oh My Pi's `gen_ai.usage.input_tokens` does, so it is sent as it is; a Messages answer's
 * `input_tokens` is the uncached part alone, so its cache reads and writes are added to it. A
 * Messages answer sends its cache write's 5-minute and 1-hour split (`cache_creation`), and so
 * does a chat completion that gives it (the gateway's chat completion of a Claude call); a chat
 * completion and a Responses answer send their reasoning tokens.
 *
 * The provider (`gen_ai.provider.name`) is `call.provider` when it names one. Else it is read from
 * the model (the answer's, else the request's, the part after its last "/"): anthropic for a
 * Claude model, openai for a GPT model, since the gateway serves each family on the other's API
 * too; a model of neither name is anthropic on a Messages answer and openai on any other.
 *
 * Where it sends, read from the environment as the OpenTelemetry SDKs read it:
 * - the URL: OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, trimmed, as the whole URL; else
 *   OTEL_EXPORTER_OTLP_ENDPOINT, trimmed, with every trailing "/" removed, and `/v1/traces`;
 * - the headers: `Content-Type: application/json` alone. The collector takes no credential, so no
 *   header variable of the environment (OTEL_EXPORTER_OTLP_HEADERS and the rest) is read;
 * - the route: straight to that URL, never through a proxy. Bun's fetch reads http_proxy,
 *   HTTP_PROXY, https_proxy, ALL_PROXY and the others for every request that does not say
 *   otherwise, so each post sets `proxy: false` (DIRECT), which Bun 1.4.4 reads as "connect
 *   directly, ignoring the proxy environment variables";
 * - the resource attributes: OTEL_RESOURCE_ATTRIBUTES, every pair of it (comma-separated
 *   KEY=VALUE, key and value trimmed, the value percent-decoded; a pair with no "=" or an empty key
 *   is skipped), `service.name` from OTEL_SERVICE_NAME, else `spend-record`, where the pairs name
 *   none, and `theorem.harness` `script`, which the collector reads as a row of a script.
 *
 * Every span sends `omp.gen_ai.auth.kind` `api_key`. It sends a cost (`omp.gen_ai.cost.estimated_usd`)
 * only when `call.costUsd` is a finite number, or text that reads as one; else the collector prices
 * the row from the list price of its model and its token counts.
 *
 * `record` never throws and never waits on the network: it checks that the span can be sent, then
 * builds it, puts it on a queue of at most QUEUE_MAX spans (when full, the span is dropped with a
 * warning) and returns. One sender posts the queue in order, each post ended after
 * POST_TIMEOUT_MS, its answer's body read included. The first failure (an answer it cannot read,
 * an answer with no id, a time that is no time, no endpoint, a full queue, a refused connection, a
 * post past its time, an answer that is not 2xx) is written once (`options.warn`), as
 * `spend-record: <what failed> (further failures are not reported)`; no message names a header.
 * Where two of them meet in one answer, the one written is the one `spend_record.py`'s `record`
 * meets first: it builds the span (and so meets an unreadable answer, a missing id and a bad time)
 * before it reads the endpoint, and reads the endpoint before it queues. A process that exits
 * drops what is still queued: `spend_record.py` waits for it at exit, which a Bun process cannot
 * do on its own, so a program awaits `flush()` on every way out (the runner's `main`s do,
 * skill-cuts/cli.ts's and skill-cuts/build.ts's when run on its own).
 */
import { randomBytes } from "node:crypto";
import { sseData } from "./messages-api";

/** The most spans waiting to be sent; one more is dropped. */
const QUEUE_MAX = 1000;
/** The longest one post may take, its answer's body read included. */
export const POST_TIMEOUT_MS = 2000;
/** The longest `flush()` waits by default, in all. */
export const FLUSH_MS = 2000;

/** What a span says of a call beside its answer. */
export interface SpendCall {
  /** The model the request named (`gen_ai.request.model`): the collector reads it where the answer
   *  names none. */
  readonly model?: string;
  /** When the call started, its request sent, as epoch milliseconds or a Date; `endedAt` by
   *  default. */
  readonly startedAt?: number | Date;
  /** When the call ended, as epoch milliseconds or a Date; now by default. */
  readonly endedAt?: number | Date;
  /** The call's cost in dollars, where the caller knows it, as a number or as text that reads as
   *  one; anything else is left out, and the span is sent all the same. */
  readonly costUsd?: unknown;
  /** The provider the call billed with (`anthropic`, `openai`); read from the answer by default. */
  readonly provider?: string;
  /** The session id the call went with (`gen_ai.conversation.id`), so that the collector groups a
   *  run's rows as one session. */
  readonly sessionId?: string;
}

export interface SpendRecorder {
  /** Queues the span of one model answer: an object, or the text the API sent (one JSON object, or
   *  a streamed reply). Never throws, never waits. */
  record(answer: unknown, call?: SpendCall): void;
  /** Queues the span of the call one line of the runner's request log holds (`loggedAnswer`);
   *  passes over a line of a failed call. Never throws, never waits. */
  recordLogged(row: unknown): void;
  /** Waits up to `timeoutMs` in all (FLUSH_MS by default) for the queue to empty, each queued
   *  span's post ended. True once it has; false when `timeoutMs` passed first, with spans still
   *  queued or a post in flight. Never rejects. */
  flush(timeoutMs?: number): Promise<boolean>;
}

export interface SpendRecorderOptions {
  /** Where the endpoint and the resource attributes are read, as each span is made; `process.env`
   *  by default. */
  readonly env?: Readonly<Record<string, string | undefined>>;
  /** Writes the one warning line; to stderr by default. */
  readonly warn?: (line: string) => void;
  /** The most spans waiting to be sent; QUEUE_MAX by default. */
  readonly queueMax?: number;
}

/** A JSON object. */
type Json = Record<string, unknown>;

/** Bun 1.4.4's fetch option that connects straight to the URL, reading none of the proxy
 *  variables. The bun-types this package installs (1.4.2) do not list `false` yet. */
const DIRECT = { proxy: false } as unknown as RequestInit;

function isJson(value: unknown): value is Json {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function dict(value: unknown): Json {
  return isJson(value) ? value : {};
}

/** A token count the API gave, as an integer; undefined when absent or not a whole number. */
function count(value: unknown): number | undefined {
  return typeof value === "number" && Number.isSafeInteger(value) ? value : undefined;
}

/** A finite cost in dollars, from a number or its decimal text; undefined for anything else. */
function costOf(value: unknown): number | undefined {
  const cost =
    typeof value === "number"
      ? value
      : typeof value === "string" && /^[+-]?(\d+\.?\d*|\.\d+)(e[+-]?\d+)?$/i.test(value.trim())
        ? Number(value.trim())
        : undefined;
  return cost !== undefined && Number.isFinite(cost) ? cost : undefined;
}

/** Epoch milliseconds or a Date as OTLP's decimal nanoseconds. Throws on anything else. */
function nanos(time: number | Date): string {
  const ms = time instanceof Date ? time.getTime() : time;
  if (typeof ms !== "number" || !Number.isFinite(ms)) {
    throw new TypeError("a time is epoch milliseconds or a Date");
  }
  // The fraction apart: ms * 1e6 as one float loses the nanoseconds of a 2026 time.
  const whole = Math.trunc(ms);
  return (BigInt(whole) * 1_000_000n + BigInt(Math.round((ms - whole) * 1e6))).toString();
}

/** `text` with each run of %XX bytes decoded as UTF-8 (a byte that is no UTF-8 as U+FFFD) and every
 *  other character kept, a "%" that starts no such byte among them, as Python's `unquote` reads
 *  it. */
function percentDecoded(text: string): string {
  return text.replace(/(?:%[0-9a-f]{2})+/gi, (run) => {
    const bytes = new Uint8Array(run.length / 3);
    for (let at = 0; at < bytes.length; at++) {
      bytes[at] = Number.parseInt(run.slice(at * 3 + 1, at * 3 + 3), 16);
    }
    return new TextDecoder().decode(bytes);
  });
}

/** The pairs of OTEL_RESOURCE_ATTRIBUTES: comma-separated KEY=VALUE, key and value trimmed and the
 *  value percent-decoded; a pair with no "=" or an empty key is skipped. A key named again takes
 *  the later value in the earlier place. */
function resourcePairs(text: string): Map<string, string> {
  const pairs = new Map<string, string>();
  for (const pair of text.split(",")) {
    const at = pair.indexOf("=");
    if (at < 0) continue;
    const key = pair.slice(0, at).trim();
    if (key !== "") pairs.set(key, percentDecoded(pair.slice(at + 1).trim()));
  }
  return pairs;
}

/** Where a span is posted, or undefined when the environment names no endpoint. */
function endpointUrl(env: Readonly<Record<string, string | undefined>>): string | undefined {
  const full = (env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT ?? "").trim();
  if (full !== "") return full;
  const base = (env.OTEL_EXPORTER_OTLP_ENDPOINT ?? "").trim();
  if (base !== "") return `${base.replace(/\/+$/, "")}/v1/traces`;
  return undefined;
}

/** The kind of a failure, for its warning: an error's name, with its code where it has a text one
 *  (Bun's `ConnectionRefused`). */
function failureName(error: unknown): string {
  if (!(error instanceof Error)) return typeof error;
  const code: unknown = Reflect.get(error, "code");
  return typeof code === "string" ? `${error.name} (${code})` : error.name;
}

/** The last events of a Responses stream, each of which carries the whole response. */
const RESPONSE_ENDS = ["response.completed", "response.incomplete", "response.failed"];
/** Text that each event a stream's usage is read from holds, its type quoted (a Messages stream's
 *  start and its deltas, and RESPONSE_ENDS): the events whose text lacks it are passed over
 *  unparsed. */
const ANSWER_EVENT = new RegExp(
  `"(${["message_start", "message_delta", ...RESPONSE_ENDS].map((type) => type.replaceAll(".", "\\.")).join("|")})"`
);

/**
 * The whole answer a streamed (SSE) reply assembles, as far as its usage goes: a Messages stream's
 * `message_start` message with the counts of each `message_delta` laid over its usage (a count a
 * delta leaves out or sends null stays) and the delta's own fields over the message's; or the
 * response a Responses stream's last event (`response.completed`, `response.incomplete` or
 * `response.failed`) carries whole. An event whose data is no JSON is passed over. Undefined for a
 * stream that holds neither: one that ended before its answer started, as a refusal can, holds no
 * usage to record.
 */
function streamedAnswer(text: string): Json | undefined {
  let message: Json | undefined;
  let response: Json | undefined;
  for (const data of sseData(text, ANSWER_EVENT)) {
    let event: unknown;
    try {
      event = JSON.parse(data);
    } catch {
      continue;
    }
    if (!isJson(event)) continue;
    if (event.type === "message_start" && isJson(event.message)) {
      message = { ...event.message };
    } else if (event.type === "message_delta" && message !== undefined) {
      const counts = { ...dict(message.usage) };
      for (const [field, value] of Object.entries(dict(event.usage))) {
        if (value !== null && value !== undefined) counts[field] = value;
      }
      message = { ...message, ...dict(event.delta), usage: counts };
    } else if (RESPONSE_ENDS.includes(String(event.type)) && isJson(event.response)) {
      response = { object: "response", ...event.response };
    }
  }
  return response ?? message;
}

/** The answer `answer` holds as one JSON object: the object itself; or its text (or UTF-8 bytes),
 *  a streamed reply read whole (`streamedAnswer`, undefined for a stream that holds no answer)
 *  where it starts with an SSE field, else one JSON object. Throws on anything else. */
function wholeAnswer(answer: unknown): Json | undefined {
  const text = answer instanceof Uint8Array ? new TextDecoder().decode(answer) : answer;
  if (typeof text === "string") {
    if (/^(event|data|id|retry)?:/.test(text.trimStart())) return streamedAnswer(text);
    const parsed: unknown = JSON.parse(text);
    if (isJson(parsed)) return parsed;
  } else if (isJson(text)) {
    return text;
  }
  throw new TypeError("the answer is not a JSON object");
}

/** The API that answered: the Responses API, chat completions, or the Messages API. */
type ApiShape = "responses" | "chat" | "messages";

/** Which API answered (the file's header). */
function shapeOf(answer: Json, usage: Json): ApiShape {
  if (answer.object === "response") return "responses";
  if (
    answer.object === "chat.completion" ||
    "prompt_tokens" in usage ||
    "completion_tokens" in usage
  ) {
    return "chat";
  }
  return "messages";
}

/** anthropic for a Claude model, openai for a GPT model, else by the API that answered. */
function providerOf(shape: ApiShape, model: unknown): string {
  const name = (
    String(model || "")
      .split("/")
      .pop() ?? ""
  ).toLowerCase();
  if (name.startsWith("claude")) return "anthropic";
  if (name.startsWith("gpt")) return "openai";
  return shape === "messages" ? "anthropic" : "openai";
}

/** An answer's counts: all of its input, its cache read, its cache write, the write's 5-minute and
 *  1-hour parts, its output, and the reasoning among the output. */
interface Counts {
  readonly input: number | undefined;
  readonly read: number | undefined;
  readonly write: number | undefined;
  readonly fiveMinutes: number | undefined;
  readonly oneHour: number | undefined;
  readonly output: number | undefined;
  readonly reasoning: number | undefined;
}

/** A Messages usage's counts: its input is the uncached part alone, so the cache's are added. */
function messagesCounts(usage: Json): Counts {
  const uncached = count(usage.input_tokens);
  const read = count(usage.cache_read_input_tokens);
  const write = count(usage.cache_creation_input_tokens);
  const split = dict(usage.cache_creation);
  return {
    input:
      uncached === undefined && read === undefined && write === undefined
        ? undefined
        : (uncached ?? 0) + (read ?? 0) + (write ?? 0),
    read,
    write,
    fiveMinutes: count(split.ephemeral_5m_input_tokens),
    oneHour: count(split.ephemeral_1h_input_tokens),
    output: count(usage.output_tokens),
    reasoning: undefined,
  };
}

/** An OpenAI usage's counts: its input already holds the cached part. The gateway's chat
 *  completion of a Claude call adds Anthropic's cache counts to the usage, and their 5-minute and
 *  1-hour split to `prompt_tokens_details`. */
function openaiCounts(shape: Exclude<ApiShape, "messages">, usage: Json): Counts {
  if (shape === "chat") {
    const details = dict(usage.prompt_tokens_details);
    const split = dict(details.cache_creation_token_details);
    return {
      input: count(usage.prompt_tokens),
      read: count(details.cached_tokens) ?? count(usage.cache_read_input_tokens),
      write: count(usage.cache_creation_input_tokens) ?? count(details.cache_write_tokens),
      fiveMinutes: count(split.ephemeral_5m_input_tokens),
      oneHour: count(split.ephemeral_1h_input_tokens),
      output: count(usage.completion_tokens),
      reasoning: count(dict(usage.completion_tokens_details).reasoning_tokens),
    };
  }
  const details = dict(usage.input_tokens_details);
  return {
    input: count(usage.input_tokens),
    read: count(details.cached_tokens),
    write: count(details.cache_write_tokens),
    fiveMinutes: undefined,
    oneHour: undefined,
    output: count(usage.output_tokens),
    reasoning: count(dict(usage.output_tokens_details).reasoning_tokens),
  };
}

function text(key: string, value: unknown) {
  return { key, value: { stringValue: String(value) } };
}

function integer(key: string, value: number) {
  return { key, value: { intValue: String(value) } };
}

/** A span's start and end, as OTLP's decimal nanoseconds. */
interface SpanTimes {
  readonly start: string;
  readonly end: string;
}

/** The OTLP/HTTP JSON body of the span of `answer` (the file's header), which starts and ends at
 *  `times`. */
function spanOf(
  answer: Json,
  call: SpendCall,
  times: SpanTimes,
  env: Readonly<Record<string, string | undefined>>
): Json {
  const usage = dict(answer.usage);
  const shape = shapeOf(answer, usage);
  const provider = call.provider || providerOf(shape, answer.model || call.model);
  const attributes: Json[] = [
    text("gen_ai.operation.name", "chat"),
    text("gen_ai.provider.name", provider),
  ];
  if (call.model) attributes.push(text("gen_ai.request.model", call.model));
  if (answer.model) attributes.push(text("gen_ai.response.model", answer.model));
  if (answer.id) attributes.push(text("gen_ai.response.id", answer.id));
  if (call.sessionId) attributes.push(text("gen_ai.conversation.id", call.sessionId));
  const counts = shape === "messages" ? messagesCounts(usage) : openaiCounts(shape, usage);
  const named: [string, number | undefined][] = [
    ["gen_ai.usage.input_tokens", counts.input],
    ["gen_ai.usage.cache_read.input_tokens", counts.read],
    ["gen_ai.usage.cache_creation.input_tokens", counts.write],
    ["gen_ai.usage.cache_creation.ephemeral_5m_input_tokens", counts.fiveMinutes],
    ["gen_ai.usage.cache_creation.ephemeral_1h_input_tokens", counts.oneHour],
    ["gen_ai.usage.output_tokens", counts.output],
    ["gen_ai.usage.reasoning.output_tokens", counts.reasoning],
  ];
  for (const [key, value] of named) {
    if (value !== undefined) attributes.push(integer(key, value));
  }
  const tier = answer.service_tier;
  if (provider === "openai" && shape !== "messages" && typeof tier === "string" && tier !== "") {
    attributes.push(text("openai.response.service_tier", tier));
  }
  attributes.push(text("omp.gen_ai.auth.kind", "api_key"));
  const cost = costOf(call.costUsd);
  if (cost !== undefined) {
    attributes.push({ key: "omp.gen_ai.cost.estimated_usd", value: { doubleValue: cost } });
  }
  const resource = resourcePairs(env.OTEL_RESOURCE_ATTRIBUTES ?? "");
  if (!resource.has("service.name")) {
    resource.set("service.name", (env.OTEL_SERVICE_NAME ?? "").trim() || "spend-record");
  }
  resource.set("theorem.harness", "script");
  const name = String(call.model || answer.model || "");
  return {
    resourceSpans: [
      {
        resource: { attributes: [...resource].map(([key, value]) => text(key, value)) },
        scopeSpans: [
          {
            scope: { name: "spend_record" },
            spans: [
              {
                traceId: randomBytes(16).toString("hex"),
                spanId: randomBytes(8).toString("hex"),
                name: `chat ${name}`.trim(),
                kind: 3,
                startTimeUnixNano: times.start,
                endTimeUnixNano: times.end,
                attributes,
              },
            ],
          },
        ],
      },
    ],
  };
}

/** The session id the gateway logs (`session_id`) for a body's session id field `identity` (a
 *  Messages body's `metadata.user_id`, a Responses body's `prompt_cache_key` or `user`): the
 *  `session_id` of a JSON object's text, as Oh My Pi and Claude Code write `user_id`; or the text
 *  itself when it is no JSON object. Undefined for no text, an empty one, or an object that names
 *  no `session_id`. */
export function conversationId(identity: string | null | undefined): string | undefined {
  if (typeof identity !== "string" || identity === "") return undefined;
  let parsed: unknown;
  try {
    parsed = JSON.parse(identity);
  } catch {
    return identity;
  }
  if (!isJson(parsed)) return identity;
  return typeof parsed.session_id === "string" && parsed.session_id !== ""
    ? parsed.session_id
    : undefined;
}

/**
 * The answer and the call that one line of the runner's request log (skill-cuts/cli.ts's
 * `loggedGateway`) holds, or undefined for a line of a failed call (`error`): the answer's id
 * (`messageId`), model (`answeredBy`) and usage as the reply gave them, a Responses answer where
 * the id is a `resp_` one, or where it is no `msg_` one and the usage has `input_tokens_details`,
 * which only a Responses usage has (a Messages usage can have `output_tokens_details`, as the
 * gateway's Claude replies do); the request's model (`model`), its start (`sentAt`, when it was
 * sent) and its end `durationMs` later, and the session id of its `userId` (`conversationId`).
 * Throws on a line with no usage or no readable `sentAt`.
 */
function loggedAnswer(row: unknown): { answer: Json; call: SpendCall } | undefined {
  if (!isJson(row)) throw new TypeError("the line is not a JSON object");
  if (row.error !== undefined) return undefined;
  if (!isJson(row.usage)) throw new TypeError("the line holds no usage");
  const startedAt = typeof row.sentAt === "string" ? Date.parse(row.sentAt) : Number.NaN;
  if (!Number.isFinite(startedAt)) throw new TypeError("the line holds no sentAt time");
  const duration = row.durationMs;
  const endedAt =
    typeof duration === "number" && Number.isFinite(duration) && duration >= 0
      ? startedAt + duration
      : startedAt;
  const id = typeof row.messageId === "string" ? row.messageId : undefined;
  const responses =
    id?.startsWith("resp_") === true ||
    (id?.startsWith("msg_") !== true && "input_tokens_details" in row.usage);
  const answer: Json = {
    ...(responses ? { object: "response" } : {}),
    ...(id === undefined ? {} : { id }),
    ...(typeof row.answeredBy === "string" ? { model: row.answeredBy } : {}),
    usage: row.usage,
  };
  const sessionId = conversationId(typeof row.userId === "string" ? row.userId : undefined);
  const call: SpendCall = {
    ...(typeof row.model === "string" ? { model: row.model } : {}),
    startedAt,
    endedAt,
    ...(sessionId === undefined ? {} : { sessionId }),
  };
  return { answer, call };
}

/** A recorder of the spans of model answers (the file's header). */
export function createSpendRecorder(options: SpendRecorderOptions = {}): SpendRecorder {
  const env = options.env ?? process.env;
  const writeWarning = options.warn ?? ((line: string) => process.stderr.write(`${line}\n`));
  const queueMax = options.queueMax ?? QUEUE_MAX;
  const queue: { readonly url: string; readonly body: string }[] = [];
  let sending: Promise<void> | undefined;
  let warned = false;

  /** Writes the first failure, once. */
  const warn = (message: string): void => {
    if (warned) return;
    warned = true;
    try {
      writeWarning(`spend-record: ${message} (further failures are not reported)`);
    } catch {
      // A warning that cannot be written leaves the caller as it was.
    }
  };

  async function post(url: string, body: string): Promise<void> {
    try {
      const response = await fetch(url, {
        ...DIRECT,
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body,
        signal: AbortSignal.timeout(POST_TIMEOUT_MS),
      });
      await response.arrayBuffer();
      if (!response.ok) warn(`the collector answered HTTP ${response.status}`);
    } catch (error) {
      warn(`could not reach the collector: ${failureName(error)}`);
    }
  }

  /** The one sender: posts the queue in order until it is empty. */
  async function send(): Promise<void> {
    for (let next = queue.shift(); next !== undefined; next = queue.shift()) {
      await post(next.url, next.body);
    }
    sending = undefined;
  }

  /** Queues the span of `answer`, once it is known that the span can be sent: the failures come
   *  in `spend_record.py`'s order (the file's header), so that the one written is its, and the
   *  span is built only after the last of them. */
  function queueSpan(answer: Json, call: SpendCall): void {
    if (!answer.id) {
      warn("the answer has no id, and the collector keeps no row of an answer without one");
    }
    // The call's end, now by default, and its start, the end by default: `nanos` throws on a time
    // that is neither epoch milliseconds nor a Date.
    const end = call.endedAt ?? Date.now();
    const times: SpanTimes = { start: nanos(call.startedAt ?? end), end: nanos(end) };
    const url = endpointUrl(env);
    if (url === undefined) {
      warn(
        "no OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: no spend record sent"
      );
      return;
    }
    if (queue.length >= queueMax) {
      warn(`${queueMax} spend records are waiting to be sent: this one is dropped`);
      return;
    }
    queue.push({ url, body: JSON.stringify(spanOf(answer, call, times, env)) });
    // Started after this call returns, so no part of a post runs inside `record`.
    sending ??= Promise.resolve().then(send);
  }

  /** Whether no answer can change anything now: the one warning is written, and no span can be
   *  queued (no endpoint, or a full queue). An answer is then not read at all. */
  const idle = (): boolean =>
    warned && (queue.length >= queueMax || endpointUrl(env) === undefined);

  return {
    record(answer, call = {}) {
      if (idle()) return;
      try {
        const whole = wholeAnswer(answer);
        if (whole !== undefined) queueSpan(whole, call);
      } catch (error) {
        warn(`could not build the spend record: ${failureName(error)}`);
      }
    },
    recordLogged(row) {
      if (idle()) return;
      try {
        const logged = loggedAnswer(row);
        if (logged !== undefined) queueSpan(logged.answer, logged.call);
      } catch (error) {
        warn(`could not build the spend record: ${failureName(error)}`);
      }
    },
    async flush(timeoutMs = FLUSH_MS) {
      const deadline = Date.now() + timeoutMs;
      for (let pending = sending; pending !== undefined; pending = sending) {
        const left = deadline - Date.now();
        if (left <= 0) return false;
        const { promise: waited, resolve } = Promise.withResolvers<void>();
        const timer = setTimeout(resolve, left);
        await Promise.race([pending, waited]);
        clearTimeout(timer);
      }
      return true;
    },
  };
}

let processRecorder: SpendRecorder | undefined;

/** This process's one recorder: on `process.env`, its warning written to stderr. */
export function processSpendRecorder(): SpendRecorder {
  processRecorder ??= createSpendRecorder();
  return processRecorder;
}
