/**
 * The replay of one arm of a case (plan, R5 `replay.ts`): its captured request body sent to the
 * gateway, and the reply that is the judged step. The body is of either API the gateway serves:
 * a Messages body, or an OpenAI Responses body (gateway.ts's `isResponsesBody`); every round sends
 * the body in its own API, on the case's own model.
 *
 * A reply whose every tool call is a read of a file of the case's skill (a `read` of
 * `skill://<skill>` or `skill://<skill>/<path>`, as cases.ts's `skillReadFile` reads one) is not
 * the step: the agent is still loading the skill. Each of those reads is answered the way the
 * arm's files would answer it (arms.ts's `skillFile`: the file under a `[Skill file: <path>]`
 * header whose path is the skill's installed one, the same in every arm, or Oh My Pi's not-found
 * result for a file the arm lacks), in the form Oh My Pi sends a tool result in the body's API,
 * and the body goes again with the reply and the answers added; at most `maxSkillRounds` times
 * (MAX_SKILL_ROUNDS). A Messages reply goes back as the assistant's message and its answers as one
 * user message of `tool_result` blocks (F2); a Responses reply's output items go back as they came
 * (its reasoning items' encrypted content included), each followed in the input by a
 * `function_call_output` item holding the answer's text (F8: the Responses API takes no error
 * flag, and Oh My Pi sends none). The first reply that holds no tool call, or any other tool call
 * (a read of another skill, a read of a path on disk, a read of a directory of the skill, any
 * other tool), is the judged step, and so is the reply after the last round, whatever it holds.
 * Every round goes with the same headers (`ReplayOptions.headers`), the ones Oh My Pi sent beside
 * the body (none beside a Responses body).
 *
 * Every run of an arm sends the same body, so the gateway's prompt cache can serve all but the
 * first run's prefix. A Messages body is cached only up to its last cache breakpoint, and the
 * bodies Oh My Pi builds hold theirs on the tools and the system prompt only: the pilot's runs
 * read about 20,000 tokens of a 340,000-token body from the cache and paid the rest as input. So
 * each run sends a Messages body with a breakpoint on its last message (`withCacheBreakpoint`).
 * The Responses route caches the prefix a body shares with an earlier one by itself (F9: the
 * second send of a body read all but 3 of its 7,985 input tokens from the cache), so a Responses
 * body goes as it is, with no `cache_control`; its reads are counted as `cacheRead`. `replay`
 * sends an arm's first run alone, so that the others read what it wrote.
 */
import { statSync } from "node:fs";
import path from "node:path";
import { type Arm, skillFile } from "./arms";
import { skillReadFile } from "./cases";
import {
  type ContentBlock,
  type Gateway,
  isResponsesBody,
  type MessagesBody,
  type Reply,
  type RequestBody,
  type SendOptions,
  type Spent,
  type ToolCall,
  toolCalls,
} from "./gateway";

/** The most times a replay answers a reply's skill reads and sends again. */
export const MAX_SKILL_ROUNDS = 3;

/** The most cache breakpoints (`cache_control`) one Messages API request may hold. */
export const MAX_BREAKPOINTS = 4;

/** True for a JSON object, as a body's parts are read. */
function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** Each object in `value` that holds a cache breakpoint, in the order the request reads them. */
function breakpointsIn(
  value: unknown,
  found: Record<string, unknown>[] = []
): Record<string, unknown>[] {
  if (Array.isArray(value)) {
    for (const item of value) breakpointsIn(item, found);
  } else if (isObject(value)) {
    if (value.cache_control != null) found.push(value);
    for (const field of Object.values(value)) breakpointsIn(field, found);
  }
  return found;
}

/**
 * `body` with a cache breakpoint on the last block of its last message, the end of the prefix
 * that every run of an arm shares, so that the runs after the first read it all from the cache.
 * The block is the last one that can hold a breakpoint: not a thinking block and not an empty
 * text (a last message whose content is a string becomes one text block holding it). The
 * breakpoint keeps its prefix an hour (`{type: "ephemeral", ttl: "1h"}`), or 5 minutes
 * (`{type: "ephemeral"}`) when a breakpoint before it does, since a request may not hold an
 * hour-long one after a shorter one.
 *
 * The body keeps at most MAX_BREAKPOINTS: the earliest breakpoints of its messages are taken out
 * first, and those of its tools and system prompt are kept. A body whose last block holds a
 * breakpoint already, with no more than MAX_BREAKPOINTS in all, a body whose tools and system
 * prompt hold MAX_BREAKPOINTS already, and a body whose last message has no block to hold one
 * come back as they are. Nothing else changes: the body's fields and their order are its own, and
 * `body` itself is never changed.
 */
export function withCacheBreakpoint(body: MessagesBody): MessagesBody {
  const fixed = [...breakpointsIn(body.tools), ...breakpointsIn(body.system)];
  const messages = structuredClone([...body.messages]);
  const last = messages.at(-1);
  if (!isObject(last)) return body;
  if (typeof last.content === "string" && last.content !== "") {
    last.content = [{ type: "text", text: last.content }];
  }
  if (!Array.isArray(last.content)) return body;
  const target = last.content.findLast(
    (block) =>
      isObject(block) &&
      block.type !== "thinking" &&
      block.type !== "redacted_thinking" &&
      !(block.type === "text" && block.text === "")
  );
  if (!isObject(target)) return body;
  const adding = target.cache_control == null;
  const inMessages = breakpointsIn(messages);
  const total = fixed.length + inMessages.length + (adding ? 1 : 0);
  if (total <= MAX_BREAKPOINTS && !adding) return body;
  if (fixed.length + 1 > MAX_BREAKPOINTS) return body;
  const dropped = inMessages
    .filter((holder) => holder !== target)
    .slice(0, Math.max(0, total - MAX_BREAKPOINTS));
  for (const holder of dropped) delete holder.cache_control;
  if (adding) {
    const kept = [...fixed, ...inMessages.filter((holder) => !dropped.includes(holder))];
    const long = kept.every(
      (holder) => isObject(holder.cache_control) && holder.cache_control.ttl === "1h"
    );
    target.cache_control = long ? { type: "ephemeral", ttl: "1h" } : { type: "ephemeral" };
  }
  return { ...body, messages };
}

/** One replay: the judged step, how many rounds of skill reads were answered before it, and what
 *  each reply used, in the order sent. */
export interface Replayed {
  readonly step: Reply;
  readonly rounds: number;
  readonly usage: Spent[];
}

/** How a replay runs: the most rounds of skill reads it answers, and the headers that go with the
 *  body on every round (gateway.ts's `SendOptions`: the `anthropic-*` headers Oh My Pi sent beside
 *  a captured Messages body, whose betas its fields need; none beside a Responses body). */
export interface ReplayOptions extends SendOptions {
  readonly maxSkillRounds?: number;
}

/** A tool call that reads a file of the arm's skill: the call's id and the file, as a path inside
 *  the skill's directory. */
interface SkillRead {
  readonly id: string;
  readonly file: string;
}

/** The skill read `call` is, or undefined when it is any other tool call. A read of a directory
 *  of the skill is none: Oh My Pi answers it with a listing, which the arm does not rebuild. */
function skillRead(call: ToolCall, arm: Arm): SkillRead | undefined {
  const { id, name, input } = call;
  if (id === undefined || name !== "read") return undefined;
  if (typeof input !== "object" || input === null || !("path" in input)) return undefined;
  if (typeof input.path !== "string") return undefined;
  const file = skillReadFile(arm.skill, input.path);
  if (file === undefined) return undefined;
  if (arm.skillDir !== undefined) {
    const stats = statSync(path.resolve(arm.skillDir, file), { throwIfNoEntry: false });
    if (stats?.isDirectory()) return undefined;
  }
  return { id, file };
}

/** `body` with `content`, a reply whose every tool call is one of `reads`, and the arm's answer to
 *  each read added, in the form of the body's API (see the file's header). */
function answered(
  body: RequestBody,
  content: readonly ContentBlock[],
  reads: readonly SkillRead[],
  arm: Arm
): RequestBody {
  const answers = reads.map((read) => ({ id: read.id, ...skillFile(arm, read.file) }));
  if (isResponsesBody(body)) {
    const outputs = answers.map(({ id, text }) => ({
      type: "function_call_output",
      call_id: id,
      output: text,
    }));
    return { ...body, input: [...body.input, ...content, ...outputs] };
  }
  const results = answers.map(({ id, text, isError }) => ({
    type: "tool_result",
    tool_use_id: id,
    content: [{ type: "text", text }],
    is_error: isError,
  }));
  return {
    ...body,
    messages: [
      ...body.messages,
      { role: "assistant", content },
      { role: "user", content: results },
    ],
  };
}

/** Sends `body` for `arm`, answers its skill reads, and returns the judged step. Rejects when a
 *  send does (the gateway's `BudgetSpent` among them). */
export async function replayOnce(
  body: RequestBody,
  arm: Arm,
  gateway: Gateway,
  { maxSkillRounds = MAX_SKILL_ROUNDS, headers }: ReplayOptions = {}
): Promise<Replayed> {
  let sent: RequestBody = isResponsesBody(body) ? body : withCacheBreakpoint(body);
  const usage: Spent[] = [];
  for (let rounds = 0; ; rounds += 1) {
    const reply = await gateway.send(sent, { headers });
    usage.push(reply.spent);
    const calls = toolCalls(reply.message.content);
    const reads = calls.map((call) => skillRead(call, arm));
    if (
      rounds >= maxSkillRounds ||
      calls.length === 0 ||
      !reads.every((read): read is SkillRead => read !== undefined)
    ) {
      return { step: reply, rounds, usage };
    }
    sent = answered(sent, reply.message.content, reads, arm);
  }
}

/**
 * `n` replays of `body` for `arm`, each as `replayOnce`, each one's outcome kept whether it was a
 * step or a failure, in the order started. The first goes alone and the rest after it, all at
 * once (the gateway bounds how many are in flight): the bodies are the same, so the cache the
 * first one writes serves the others' shared prefix.
 */
export async function replay(
  body: RequestBody,
  arm: Arm,
  gateway: Gateway,
  n: number,
  options: ReplayOptions = {}
): Promise<PromiseSettledResult<Replayed>[]> {
  if (!Number.isInteger(n) || n < 1) {
    throw new Error(`a replay runs a whole number of times, 1 or more, not ${n}`);
  }
  const first = await Promise.allSettled([replayOnce(body, arm, gateway, options)]);
  const rest = await Promise.allSettled(
    Array.from({ length: n - 1 }, () => replayOnce(body, arm, gateway, options))
  );
  return [...first, ...rest];
}
