/**
 * The runner's one way to the model gateway (plan, R5 `gateway.ts`). `createGateway` posts an
 * Anthropic Messages API body to `<baseUrl>/v1/messages` through `skill-scenarios/messages-api.ts`
 * (`postMessages`), the route and retry that `label-comments.ts` uses too:
 *
 * - The key is the text of `keyFile`, read inside this process for each try and held
 *   (`@legion/contracts/held-secrets`), so it is never printed, logged or written: every error
 *   `send` throws has the key, and each run of 10 or more of its characters, taken out of its
 *   message, its stack and its fields, whatever echoed it (a refusal that quotes the key it got).
 * - `send(body, {headers})` sends `headers` with the body on every try: the `anthropic-*` headers
 *   Oh My Pi sent beside it (its `anthropic-beta` names the betas that the body's fields need, and
 *   the gateway answers 400 to such a body without it). Only `anthropic-*` names are taken, and a
 *   name that can carry a key (`x-api-key`, `authorization`, any name holding `key`, `token`,
 *   `secret`, `auth`, `cookie`, `credential` or `password`) is refused by name before any call.
 * - A 429 or a 5xx, a failed connection, a try past `timeoutMs` (its body read included; the try is
 *   ended) and a reply that does not read as a Messages API message are tried again, after the
 *   `retry-after` wait or the back-off (1 s, 2 s, 4 s, ...), up to GATEWAY_TRIES tries in all. Any
 *   other status, a 400 among them, throws at once.
 * - At most `concurrency` calls are in flight; the others wait their turn, in the order sent.
 * - A body that sets `stream` gets a streamed (SSE) reply, read whole (`streamedMessage`) before
 *   it counts; any other gets one JSON message.
 * - `spend()` sums each reply's usage (input, output, cache read, cache write) per model, and its
 *   dollars at list price (PRICES, or `prices` for a model the table lacks). A body whose model has
 *   no price is refused before any call, since its dollars could not be counted. A try that failed
 *   is not counted: no usage came back for it.
 * - Once the dollars reach `budgetUsd`, no new call starts: `send` rejects with `BudgetSpent`, which
 *   says so. A call already in flight finishes and is counted, so the spend can pass the budget by
 *   at most the calls in flight.
 */
import { holdSecret, scrubError } from "@legion/contracts/held-secrets";
import { z } from "zod";
import { postMessages, readKeyFile } from "../skill-scenarios/messages-api";
import type { Fetch, Sleep } from "../skill-scenarios/production-dispatch";

/** How many times one call is tried before `send` fails. */
export const GATEWAY_TRIES = 6;

/** What a model charges, in USD per million tokens: prompt tokens not read from the cache, reply
 *  tokens (thinking included), a cache write of 5 minutes and of 1 hour, and a cache read. */
export interface Rates {
  readonly input: number;
  readonly output: number;
  readonly cacheWrite5m: number;
  readonly cacheWrite1h: number;
  readonly cacheRead: number;
}

/** A model's list price: its rates, and the higher rates of a prompt longer than `over` tokens
 *  (input, cache read and cache write together) for a model priced by prompt length. */
export interface Price extends Rates {
  readonly longPrompt?: { readonly over: number; readonly rates: Rates };
}

/** Where PRICES come from. */
export const PRICE_SOURCE =
  "https://platform.claude.com/docs/en/about-claude/pricing, read 2026-11-20 (standard speed, global inference)";

/** Anthropic's list prices by Claude API model id, from PRICE_SOURCE's "Model pricing" table. A
 *  reply of fast mode or of US-only inference costs more than this counts: the bodies Oh My Pi
 *  18.18.6 sends (F2) set neither `speed` nor `inference_geo`, and each F5 reply's usage says
 *  `"speed": "standard"` and `"inference_geo": "global"`. */
export const PRICES: Readonly<Record<string, Price>> = {
  "claude-fable-5-2": { input: 8, output: 40, cacheWrite5m: 10, cacheWrite1h: 16, cacheRead: 0.2 },
  "claude-opus-5-5": { input: 4, output: 20, cacheWrite5m: 5, cacheWrite1h: 8, cacheRead: 0.2 },
  "claude-opus-5": { input: 5, output: 25, cacheWrite5m: 6.25, cacheWrite1h: 10, cacheRead: 0.5 },
  "claude-opus-4-8": { input: 5, output: 25, cacheWrite5m: 6.25, cacheWrite1h: 10, cacheRead: 0.5 },
  "claude-opus-4-7": { input: 5, output: 25, cacheWrite5m: 6.25, cacheWrite1h: 10, cacheRead: 0.5 },
  "claude-opus-4-6": { input: 5, output: 25, cacheWrite5m: 6.25, cacheWrite1h: 10, cacheRead: 0.5 },
  "claude-sonnet-5-5": { input: 2, output: 10, cacheWrite5m: 2.5, cacheWrite1h: 4, cacheRead: 0.1 },
  "claude-sonnet-5": { input: 2, output: 10, cacheWrite5m: 2.5, cacheWrite1h: 4, cacheRead: 0.2 },
  "claude-sonnet-4-6": {
    input: 3,
    output: 15,
    cacheWrite5m: 3.75,
    cacheWrite1h: 6,
    cacheRead: 0.3,
  },
  "claude-haiku-5-5": {
    input: 0.1,
    output: 0.5,
    cacheWrite5m: 0.125,
    cacheWrite1h: 0.2,
    cacheRead: 0.01,
    longPrompt: {
      over: 100_000,
      rates: { input: 0.5, output: 2.5, cacheWrite5m: 0.625, cacheWrite1h: 1, cacheRead: 0.05 },
    },
  },
};

/** A token count, as the API sends one. */
const Count = z.number().int().nonnegative();

/** A reply's `usage`, as the Messages API reports it. Fields it does not name are kept. */
export const Usage = z.looseObject({
  input_tokens: Count,
  output_tokens: Count,
  cache_creation_input_tokens: Count.nullish(),
  cache_read_input_tokens: Count.nullish(),
  /** The cache write split by how long the cache keeps it. */
  cache_creation: z
    .looseObject({
      ephemeral_5m_input_tokens: Count.nullish(),
      ephemeral_1h_input_tokens: Count.nullish(),
    })
    .nullish(),
});
export type Usage = z.infer<typeof Usage>;

/** One block of a reply's content: `text`, `thinking`, `tool_use` and others, kept whole so that a
 *  reply can be sent back as the assistant's message (a thinking block's signature included). */
export const ContentBlock = z.looseObject({ type: z.string() });
export type ContentBlock = z.infer<typeof ContentBlock>;

/** A Messages API reply message. Fields it does not name (`id`, `stop_sequence`, ...) are kept. */
export const ReplyMessage = z.looseObject({
  model: z.string().min(1),
  content: z.array(ContentBlock),
  stop_reason: z.string().nullable(),
  usage: Usage,
});
export type ReplyMessage = z.infer<typeof ReplyMessage>;

/** A Messages API request body: its model and messages, and every other field as the caller set
 *  it (`system`, `tools`, `thinking`, `max_tokens`, `stream`). */
export interface MessagesBody {
  readonly model: string;
  readonly messages: readonly unknown[];
  readonly stream?: boolean;
  readonly [field: string]: unknown;
}

/** What one reply used: the model that answered, its tokens, and their dollars at list price. */
export interface Spent {
  readonly model: string;
  readonly input: number;
  readonly output: number;
  readonly cacheRead: number;
  readonly cacheWrite: number;
  readonly usd: number;
}

/** One model call's answer: the reply message, the gateway's request id when it sent one, how long
 *  the call took from its start (its retries included), and what it used. */
export interface Reply {
  readonly message: ReplyMessage;
  readonly requestId?: string;
  readonly durationMs: number;
  readonly spent: Spent;
}

/** One model's total: the replies counted, their tokens and their dollars at list price. */
export interface ModelSpend {
  readonly calls: number;
  readonly input: number;
  readonly output: number;
  readonly cacheRead: number;
  readonly cacheWrite: number;
  readonly usd: number;
}

/** Every reply counted so far: per model, and the dollars of all of them. */
export interface Spend {
  readonly models: Record<string, ModelSpend>;
  readonly usd: number;
}

export interface GatewayOptions {
  /** The gateway's base URL: calls go to `<baseUrl>/v1/messages`. */
  readonly baseUrl: string;
  /** The file that holds the gateway key. */
  readonly keyFile: string;
  /** The most calls in flight at once. */
  readonly concurrency: number;
  /** The longest one try may take, its reply read whole included. */
  readonly timeoutMs: number;
  /** No new call starts once the dollars at list price reach this. */
  readonly budgetUsd?: number;
  /** Prices for models PRICES lacks, or in place of its own. */
  readonly prices?: Readonly<Record<string, Price>>;
  readonly fetch?: Fetch;
  readonly sleep?: Sleep;
}

/** What one `send` takes beside its body. */
export interface SendOptions {
  /** Headers sent with the body, such as the `anthropic-beta` Oh My Pi sent beside it, which the
   *  body's fields need (adaptive thinking's `block_binding`, `context_management`,
   *  `output_config.effort`). Only `anthropic-*` names; a name that can carry a key is refused. */
  readonly headers?: Readonly<Record<string, string>>;
}

export interface Gateway {
  /** The reply to `body`. Rejects with `BudgetSpent` once the budget is reached. */
  send(body: MessagesBody, options?: SendOptions): Promise<Reply>;
  /** Every reply counted so far, as a copy. */
  spend(): Spend;
}

/** A header name that can carry a credential: the gateway signs each call with the key file's key
 *  itself, and no other credential goes with a call. */
const KEY_BEARING = /key|token|secret|auth|cookie|credential|password/i;
/** The header names a caller may send: Anthropic's own. */
const ANTHROPIC_HEADER = /^anthropic-[a-z0-9-]+$/i;

/** Throws naming the first header of `headers` that is not sent with a call: one whose name can
 *  carry a key, or one that is not an `anthropic-*` header. Never names a value. */
function checkHeaders(headers: Readonly<Record<string, string>>): void {
  for (const name of Object.keys(headers)) {
    if (KEY_BEARING.test(name)) {
      throw new Error(
        `the header ${name} can carry a key, so it is not sent: the gateway signs each call with the key file's key itself`
      );
    }
    if (!ANTHROPIC_HEADER.test(name)) {
      throw new Error(`only anthropic-* headers are sent beside a body, not ${name}`);
    }
  }
}

/** The refusal of a call once the budget is reached. */
export class BudgetSpent extends Error {
  constructor(
    readonly budgetUsd: number,
    readonly spentUsd: number
  ) {
    super(
      `the budget of $${budgetUsd} is spent: the replies so far cost $${spentUsd.toFixed(4)} at list price, so no new model call starts`
    );
    this.name = "BudgetSpent";
  }
}

/** A reply's tokens: input not read from the cache, output, cache reads and cache writes. */
export function tokensOf(usage: Usage): Omit<Spent, "model" | "usd"> {
  return {
    input: usage.input_tokens,
    output: usage.output_tokens,
    cacheRead: usage.cache_read_input_tokens ?? 0,
    cacheWrite: usage.cache_creation_input_tokens ?? 0,
  };
}

/** A reply's dollars at `price`. A cache write the usage does not say is of 5 minutes is priced as
 *  one of 1 hour, the dearer, so that the count never falls short of the bill. */
export function costUsd(usage: Usage, price: Price): number {
  const tokens = tokensOf(usage);
  const prompt = tokens.input + tokens.cacheRead + tokens.cacheWrite;
  const rates =
    price.longPrompt !== undefined && prompt > price.longPrompt.over
      ? price.longPrompt.rates
      : price;
  const write5m = Math.min(tokens.cacheWrite, usage.cache_creation?.ephemeral_5m_input_tokens ?? 0);
  const write1h = tokens.cacheWrite - write5m;
  return (
    (tokens.input * rates.input +
      tokens.output * rates.output +
      tokens.cacheRead * rates.cacheRead +
      write5m * rates.cacheWrite5m +
      write1h * rates.cacheWrite1h) /
    1e6
  );
}

/** Each event of an SSE stream, in order: the JSON of its `data` lines. */
function sseEvents(text: string): unknown[] {
  const events: unknown[] = [];
  for (const chunk of text.split(/\r?\n\r?\n/)) {
    const data = chunk
      .split(/\r?\n/)
      .filter((line) => line.startsWith("data:"))
      .map((line) => line.slice("data:".length).replace(/^ /, ""))
      .join("\n");
    if (data.trim() !== "") events.push(JSON.parse(data));
  }
  return events;
}

const Event = z.looseObject({ type: z.string() });
const Index = z.number().int().nonnegative();
const MessageStart = z.looseObject({
  message: z.looseObject({ usage: z.record(z.string(), z.unknown()) }),
});
const BlockStart = z.looseObject({ index: Index, content_block: ContentBlock });
const BlockDelta = z.looseObject({ index: Index, delta: z.looseObject({ type: z.string() }) });
const BlockStop = z.looseObject({ index: Index });
const MessageDelta = z.looseObject({
  delta: z.looseObject({}),
  usage: z.record(z.string(), z.unknown()).nullish(),
});
const StreamError = z.looseObject({
  error: z.looseObject({ type: z.string().optional(), message: z.string().optional() }),
});

/** The block at `index` of `blocks`, or a throw naming the event that names a block not started. */
function blockAt(blocks: Record<string, unknown>[], index: number, event: string) {
  const block = blocks[index];
  if (block === undefined)
    throw new Error(`the stream's ${event} names block ${index}, never started`);
  return block;
}

/** `text` plus `more`, where `text` is a string or nothing yet. */
function appended(text: unknown, more: unknown): string {
  return `${typeof text === "string" ? text : ""}${typeof more === "string" ? more : ""}`;
}

/**
 * The message a streamed (SSE) reply holds, put together whole as the Messages API's own clients
 * do: `message_start` gives the message, each block is started, grown by its deltas (text,
 * thinking and its signature, citations, and a tool call's input as JSON in pieces, parsed when
 * the block stops) and stopped, and `message_delta` sets the stop reason and the final usage
 * counts. A `ping` and an event of a kind not named here are passed over. Throws on an `error`
 * event, on a delta of a kind it does not know, and on a stream that ends before `message_stop`,
 * so that a reply cut short is never taken for a whole one.
 */
export function streamedMessage(text: string): unknown {
  let message: { usage: Record<string, unknown>; [field: string]: unknown } | undefined;
  const blocks: Record<string, unknown>[] = [];
  const json = new Map<number, string>();
  let stopped = false;
  for (const event of sseEvents(text)) {
    switch (Event.parse(event).type) {
      case "message_start":
        message = { ...MessageStart.parse(event).message };
        break;
      case "content_block_start": {
        const start = BlockStart.parse(event);
        blocks[start.index] = { ...start.content_block };
        break;
      }
      case "content_block_delta": {
        const { index, delta } = BlockDelta.parse(event);
        const block = blockAt(blocks, index, "content_block_delta");
        if (delta.type === "text_delta") block.text = appended(block.text, delta.text);
        else if (delta.type === "thinking_delta") {
          block.thinking = appended(block.thinking, delta.thinking);
        } else if (delta.type === "signature_delta") block.signature = delta.signature;
        else if (delta.type === "input_json_delta") {
          json.set(index, appended(json.get(index), delta.partial_json));
        } else if (delta.type === "citations_delta") {
          const citations = Array.isArray(block.citations) ? block.citations : [];
          block.citations = [...citations, delta.citation];
        } else throw new Error(`the stream holds a delta of a kind not known here: ${delta.type}`);
        break;
      }
      case "content_block_stop": {
        const { index } = BlockStop.parse(event);
        const block = blockAt(blocks, index, "content_block_stop");
        const input = json.get(index);
        if (input !== undefined) block.input = input === "" ? {} : JSON.parse(input);
        break;
      }
      case "message_delta": {
        if (message === undefined) {
          throw new Error("the stream's message_delta came before message_start");
        }
        const { delta, usage } = MessageDelta.parse(event);
        const counts = { ...message.usage };
        // The counts it sends are the totals so far; one it leaves out or sends null stays.
        for (const [field, value] of Object.entries(usage ?? {})) {
          if (value !== null && value !== undefined) counts[field] = value;
        }
        message = { ...message, ...delta, usage: counts };
        break;
      }
      case "message_stop":
        stopped = true;
        break;
      case "error": {
        const { error } = StreamError.parse(event);
        throw new Error(
          `the stream ended in an error: ${error.type ?? "error"}: ${error.message ?? ""}`
        );
      }
    }
  }
  if (message === undefined) throw new Error("the stream holds no message_start");
  if (!stopped) throw new Error("the stream ended before message_stop");
  // A block index the stream never started leaves a hole, which `Object.keys` does not count.
  if (Object.keys(blocks).length !== blocks.length) {
    throw new Error("the stream left a content block out");
  }
  return { ...message, content: blocks };
}

/** The reply message `text` holds, streamed or not. Throws when it is none. */
function replyMessage(text: string, streamed: boolean): ReplyMessage {
  const parsed = ReplyMessage.safeParse(streamed ? streamedMessage(text) : JSON.parse(text));
  if (parsed.success) return parsed.data;
  const issue = parsed.error.issues[0];
  throw new Error(
    `the reply is not a Messages API message: ${issue?.path.join(".") || "the message"}: ${issue?.message}`
  );
}

/** The gateway at `options.baseUrl`. Throws when an option is out of range. */
export function createGateway(options: GatewayOptions): Gateway {
  const { keyFile, concurrency, timeoutMs, budgetUsd } = options;
  let url: URL;
  try {
    url = new URL(options.baseUrl);
  } catch {
    throw new Error(`the gateway's base URL is not a URL: ${options.baseUrl}`);
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error(`the gateway's base URL is not http or https: ${options.baseUrl}`);
  }
  const baseUrl = options.baseUrl.replace(/\/+$/, "");
  if (!Number.isInteger(concurrency) || concurrency < 1) {
    throw new Error(`the gateway's concurrency is a whole number of 1 or more, not ${concurrency}`);
  }
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) {
    throw new Error(`the gateway's timeout is a number of milliseconds above 0, not ${timeoutMs}`);
  }
  if (budgetUsd !== undefined && !(Number.isFinite(budgetUsd) && budgetUsd >= 0)) {
    throw new Error(`the budget is a number of dollars of 0 or more, not ${budgetUsd}`);
  }
  const prices: Readonly<Record<string, Price>> = { ...PRICES, ...options.prices };
  const fetch = options.fetch ?? globalThis.fetch;
  const sleep = options.sleep ?? Bun.sleep;
  // The key, read for each try and held, so that the scrub takes it out of every error.
  const key = async () => holdSecret(await readKeyFile(keyFile));

  const models: Record<string, ModelSpend> = {};
  let totalUsd = 0;
  let running = 0;
  const waiting: (() => void)[] = [];

  /** Counts `message`, the reply to a body whose model `bodyPrice` prices. */
  function count(message: ReplyMessage, bodyPrice: Price): Spent {
    // The model that answered is the one charged; the body's price stands where the table lacks it.
    const price = prices[message.model] ?? bodyPrice;
    const usd = costUsd(message.usage, price);
    const spent = { model: message.model, ...tokensOf(message.usage), usd };
    const total = models[spent.model];
    models[spent.model] = {
      calls: (total?.calls ?? 0) + 1,
      input: (total?.input ?? 0) + spent.input,
      output: (total?.output ?? 0) + spent.output,
      cacheRead: (total?.cacheRead ?? 0) + spent.cacheRead,
      cacheWrite: (total?.cacheWrite ?? 0) + spent.cacheWrite,
      usd: (total?.usd ?? 0) + usd,
    };
    totalUsd += usd;
    return spent;
  }

  async function send(body: MessagesBody, { headers = {} }: SendOptions = {}): Promise<Reply> {
    checkHeaders(headers);
    const bodyPrice = prices[body.model];
    if (bodyPrice === undefined) {
      throw new Error(
        `no list price for the model ${body.model}, so its dollars cannot be counted: add it to PRICES in gateway.ts from ${PRICE_SOURCE}, or pass its price`
      );
    }
    const text = JSON.stringify(body);
    const streamed = body.stream === true;
    // A call's turn: one of `concurrency` places, in the order asked. A call that ends hands its
    // place to the first one waiting, so `running` stays the count of calls in flight.
    if (running < concurrency) running += 1;
    else {
      const { promise, resolve } = Promise.withResolvers<void>();
      waiting.push(resolve);
      await promise;
    }
    try {
      if (budgetUsd !== undefined && totalUsd >= budgetUsd) {
        throw new BudgetSpent(budgetUsd, totalUsd);
      }
      const started = performance.now();
      const { message, requestId } = await postMessages({
        url: baseUrl,
        key,
        body: text,
        headers,
        fetch,
        sleep,
        tries: GATEWAY_TRIES,
        timeoutMs,
        accept: (reply, response) => ({
          message: replyMessage(reply, streamed),
          requestId: response.headers.get("request-id") ?? undefined,
        }),
      });
      const durationMs = Math.round(performance.now() - started);
      const spent = count(message, bodyPrice);
      return requestId === undefined
        ? { message, durationMs, spent }
        : { message, requestId, durationMs, spent };
    } catch (error) {
      // A refusal can quote the key it got: no part of the key leaves in an error.
      throw scrubError(error);
    } finally {
      const next = waiting.shift();
      if (next === undefined) running -= 1;
      else next();
    }
  }

  return {
    send,
    spend: () => ({ models: structuredClone(models), usd: totalUsd }),
  };
}
