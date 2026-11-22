/**
 * The replay of one arm of a case: its captured request body sent to the gateway, and the reply
 * that is the judged step. The body is of either API the gateway serves:
 * a Messages body, or an OpenAI Responses body (gateway.ts's `isResponsesBody`); every round sends
 * the body in its own API, on the case's own model.
 *
 * A reply whose every tool call reads a skill file is not the step: the agent is still loading
 * skills. A read of a skill file is a `read` of a file of the case's skill (`skill://<skill>` or
 * `skill://<skill>/<path>`, as cases.ts's `skillReadFile` reads one, or a whole read by path of a
 * file of the cut's own copy of the skill, as `pathReadFile` reads one), or of a file of another
 * skill (arms.ts's `otherSkillRead`: a `skill://` read of any other skill, or a whole read by path
 * of a skill the profile lists). Each read of the case's skill is answered the way the arm's files
 * would answer it (arms.ts's `skillFile`: the file under a `[Skill file: <path>]` header whose
 * path is the skill's installed one, the same in every arm, or Oh My Pi's not-found result for a
 * file the arm lacks, or, in `none`, which installs no copy of the skill, Oh My Pi's unknown-skill
 * result; `pathFile` for a read by path: Oh My Pi's result for that path, at the path read, or its
 * not-found result). Each read of another skill is answered from that skill's installed copy
 * (`ReplayOptions.copies`, context.ts's `installedCopies` of the arm's capture's record), the same
 * text in every arm, and a read of a skill the arm does not install gets Oh My Pi's unknown-skill
 * result. The answers go in the form Oh My Pi sends a tool result in the body's API, and the body
 * goes again with the reply and the answers added; at most `maxSkillRounds` times
 * (MAX_SKILL_ROUNDS). A Messages reply goes back as the assistant's message and its answers as one
 * user message of `tool_result` blocks (F2); a Responses reply's output items go back as they came
 * (its reasoning items' encrypted content included), and after all of them, in the order of the
 * reply's calls, one `function_call_output` item per call holding the answer's text (F8: the
 * Responses API takes no error flag, and Oh My Pi sends none).
 *
 * The first reply that holds no tool call, or any tool call that reads no skill file (a read of
 * another path on disk, a read of a directory of a skill, a read with a selector, a read by path
 * of a file Oh My Pi shows in a form the arms do not rebuild, any other tool), ends the run. The
 * judged step (`Replayed.step`) is each answered round's tool calls, the reads, without their
 * answers, then that reply: the judge sees which files the agent read before it acted, so a case
 * whose line governs a read of a file of the skill ("read references/x.md first") passes on that
 * read. A reply after the last answered round whose every tool call still reads a skill file gets
 * no verdict: the replay rejects with ReadsPastLastRound (READS_PAST_LAST_ROUND), and cli.ts makes
 * the run again as any run with no verdict; judged, the read would fail in every arm. Each
 * answered round's reads are recorded (`Replayed.reads`: the skill, the file, how it was read,
 * and where the answer came from).
 *
 * Every round goes with the same headers (`ReplayOptions.headers`), the ones Oh My Pi sent beside
 * the body (none beside a Responses body).
 *
 * Every run of an arm sends the same body but for its session id, so the gateway's prompt cache
 * can serve all but the first run's prefix. A Messages body is cached only up to its last cache
 * breakpoint, and the bodies Oh My Pi builds hold theirs on the tools and the system prompt only:
 * the pilot's runs read about 20,000 tokens of a 340,000-token body from the cache and paid the
 * rest as input. So each run sends a Messages body with a breakpoint on its last message
 * (`withCacheBreakpoint`). The Responses route caches the prefix a body shares with an earlier
 * one by itself (F9: the second send of a body read all but 3 of its 7,985 input tokens from the
 * cache), so a Responses body goes as it is, with no `cache_control`; its reads are counted as
 * `cacheRead`.
 *
 * Each run of a body that names a session id goes with one of its own (gateway.ts's
 * `freshIdentity`: `metadata.user_id`, or a Responses body's `prompt_cache_key`, drawn again in
 * its own form; a body that names none goes with none), the same in every round of the run, and
 * no other byte of the body changes: a captured body holds the id of the one Oh My Pi run that
 * made it, and a gateway that routes or caches by it made it a factor of a whole arm (one body
 * with two fixed ids and with a fresh one per send, interleaved on one deployment: 8, 26 and 18 of
 * 30 runs read the skill's reference). `replayInterleaved` sends the runs of all the arms of a
 * round in one seeded order (`sendOrder`), never one arm's batch after another's.
 */
import { createHash } from "node:crypto";
import { statSync } from "node:fs";
import path from "node:path";
import {
  type Arm,
  type ArmName,
  otherSkillRead,
  pathFile,
  type SkillCopy,
  skillFile,
} from "./arms";
import { pathReadFile, skillReadFile } from "./cases";
import {
  type ContentBlock,
  freshIdentity,
  type Gateway,
  isResponsesBody,
  type MessagesBody,
  type Reply,
  type RequestBody,
  type SendOptions,
  type Spent,
  toolCalls,
} from "./gateway";

/** The most rounds of skill reads a replay answers before a reply that holds another call or none.
 *  Three: of 3,680 posted runs of the behaviour test, 137 had one round answered and none had two;
 *  a read of another skill (dispatch-first, a skill that sends the agent to others), then a read of
 *  the case's skill, then the act, takes two. */
export const MAX_SKILL_ROUNDS = 3;

/** How the reason of a run begins whose reply after the last answered round still only reads
 *  skill files (ReadsPastLastRound): such a run gets no verdict. */
export const READS_PAST_LAST_ROUND = "reads-past-last-round";

/** The most cache breakpoints (`cache_control`) one Messages API request may hold. */
export const MAX_BREAKPOINTS = 4;

/** True for a JSON object, as a body's parts are read. */
export function isObject(value: unknown): value is Record<string, unknown> {
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

/** One read an answered round answered: the skill (the name the profile lists it under, or the
 *  name a `skill://` URL gives a skill the profile does not list), the file inside its directory,
 *  how it was read (`skill` for a `skill://` URL, `path` for a whole read by path), where the
 *  answer came from (`arm`, the arm's own directory; `copy`, another skill's installed copy;
 *  `absent`, Oh My Pi's unknown-skill or not-found result where the arm installs no copy of the
 *  skill: every read of the case's skill in `none`), and whether the answer is an error. */
export interface AnsweredRead {
  readonly skill: string;
  readonly file: string;
  readonly by: "skill" | "path";
  readonly from: "arm" | "copy" | "absent";
  readonly isError: boolean;
}

/** One replay: the judged step (each answered round's tool calls, then the reply that ended the
 *  run; its other fields are that reply's), how many rounds of skill reads were answered before
 *  it and each one's reads, what each reply used, in the order sent, and the session id every
 *  round of the run went with (gateway.ts's `freshIdentity`; null for a body that names none). */
export interface Replayed {
  readonly step: Reply;
  readonly rounds: number;
  readonly reads: readonly (readonly AnsweredRead[])[];
  readonly usage: Spent[];
  readonly userId: string | null;
}

/** What a replay rejects with when the reply after its last answered round still only reads skill
 *  files: the run gets no verdict (see the file's header). `replayed` is the run as it stood, its
 *  `step` that reply after the answered rounds' calls. Its message begins READS_PAST_LAST_ROUND,
 *  and names the reply's reads. */
export class ReadsPastLastRound extends Error {
  constructor(
    readonly replayed: Replayed,
    last: readonly AnsweredRead[]
  ) {
    const files = last.map((read) => `${read.skill} ${read.file}`).join(", ");
    super(
      `${READS_PAST_LAST_ROUND}: after ${replayed.rounds} answered rounds of skill reads, the reply still only reads skill files (${files})`
    );
  }
}

/** How a replay runs: the most rounds of skill reads it answers; the installed copies of the
 *  arm's other skills, which a read of one is answered from (arms.ts's `SkillCopy`, context.ts's
 *  `installedCopies` of the arm's capture's record; none by default, and a read of a skill the
 *  profile lists with no copy here fails the run); and the headers that go with the body on every
 *  round (gateway.ts's `SendOptions`: the `anthropic-*` headers Oh My Pi sent beside a captured
 *  Messages body, whose betas its fields need; none beside a Responses body). */
export interface ReplayOptions extends SendOptions {
  readonly maxSkillRounds?: number;
  readonly copies?: readonly SkillCopy[];
}

/** A tool call that reads a skill file, answered: the call's id, the answer, and its record. */
interface SkillRead {
  readonly id: string;
  readonly text: string;
  readonly isError: boolean;
  readonly read: AnsweredRead;
}

/** The read `target` the tool call `id` makes, answered, or undefined when it reads no skill file
 *  (see the file's header). A read of a directory of the case's skill is none: Oh My Pi answers it
 *  with a listing, which the arm does not rebuild; nor is a read by path that `pathFile` does not
 *  answer, nor one `otherSkillRead` does not. */
function skillRead(
  id: string,
  target: string,
  arm: Arm,
  copies: readonly SkillCopy[]
): SkillRead | undefined {
  const from = arm.skillDir === undefined ? "absent" : "arm";
  const file = skillReadFile(arm.skill, target);
  if (file !== undefined) {
    if (arm.skillDir !== undefined) {
      const stats = statSync(path.resolve(arm.skillDir, file), { throwIfNoEntry: false });
      if (stats?.isDirectory()) return undefined;
    }
    const answer = skillFile(arm, file);
    const read = { skill: arm.skill, file, by: "skill", from, isError: answer.isError } as const;
    return { id, ...answer, read };
  }
  const byPath = pathFile(arm, target);
  if (byPath !== undefined) {
    const read = {
      skill: arm.skill,
      file: pathReadFile(arm, target) ?? target,
      by: "path",
      from,
      isError: byPath.isError,
    } as const;
    return { id, ...byPath, read };
  }
  const other = otherSkillRead(arm, copies, target);
  if (other === undefined) return undefined;
  const { text, ...read } = other;
  return { id, text, isError: other.isError, read };
}

/** The reads a reply's `content` makes, answered, or undefined when it holds no tool call, or a
 *  tool call that reads no skill file. No read is answered unless every call is a `read` of a path
 *  or URL, so a reply that also makes another call never fails on a read the replay cannot
 *  answer (`otherSkillRead`'s throw). */
function roundReads(
  content: readonly ContentBlock[],
  arm: Arm,
  copies: readonly SkillCopy[]
): SkillRead[] | undefined {
  const calls: { id: string; target: string }[] = [];
  for (const { id, name, input } of toolCalls(content)) {
    const target = typeof input === "object" && input !== null ? Reflect.get(input, "path") : null;
    if (id === undefined || name !== "read" || typeof target !== "string") return undefined;
    calls.push({ id, target });
  }
  if (calls.length === 0) return undefined;
  const reads: SkillRead[] = [];
  for (const { id, target } of calls) {
    const read = skillRead(id, target, arm, copies);
    if (read === undefined) return undefined;
    reads.push(read);
  }
  return reads;
}

/** `body` with `content`, a reply whose every tool call is one of `reads`, and the answer to each
 *  read added, in the form of the body's API (see the file's header). */
function answered(
  body: RequestBody,
  content: readonly ContentBlock[],
  reads: readonly SkillRead[]
): RequestBody {
  if (isResponsesBody(body)) {
    const outputs = reads.map(({ id, text }) => ({
      type: "function_call_output",
      call_id: id,
      output: text,
    }));
    return { ...body, input: [...body.input, ...content, ...outputs] };
  }
  const results = reads.map(({ id, text, isError }) => ({
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

/** Sends `sent` for `arm`, the body with its cache breakpoint as `replayOnce` makes it (see the
 *  file's header) and the run's own session id `userId` (`freshIdentity`), which every round of
 *  the run keeps; answers its skill reads, and returns the judged step, or rejects with
 *  ReadsPastLastRound. `sent` is never changed, and every round's body shares its parts.
 *  `firstSettled` is told once the run's first send has its reply or its failure. */
async function replaySent(
  sent: RequestBody,
  userId: string | null,
  arm: Arm,
  gateway: Gateway,
  { maxSkillRounds = MAX_SKILL_ROUNDS, copies = [], headers }: ReplayOptions,
  firstSettled?: () => void
): Promise<Replayed> {
  let body = sent;
  const usage: Spent[] = [];
  const reads: AnsweredRead[][] = [];
  // The answered rounds' tool calls, which the judged step holds before the reply that ends it.
  const calls: ContentBlock[] = [];
  for (let rounds = 0; ; rounds += 1) {
    let reply: Reply;
    try {
      reply = await gateway.send(body, { headers });
    } finally {
      if (rounds === 0) firstSettled?.();
    }
    usage.push(reply.spent);
    const content = reply.message.content;
    const round = roundReads(content, arm, copies);
    const step = { ...reply, message: { ...reply.message, content: [...calls, ...content] } };
    if (round === undefined) return { step, rounds, reads, usage, userId };
    if (rounds >= maxSkillRounds) {
      throw new ReadsPastLastRound(
        { step, rounds, reads, usage, userId },
        round.map(({ read }) => read)
      );
    }
    reads.push(round.map(({ read }) => read));
    // A Messages `tool_use` block, or a Responses `function_call` item.
    calls.push(
      ...content.filter((block) => block.type === "tool_use" || block.type === "function_call")
    );
    body = answered(body, content, round);
  }
}

/** Sends `body` for `arm` with a session id of its own when it names one (`freshIdentity`),
 *  answers its skill reads, and returns the judged step. Rejects when a send does (the gateway's
 *  `BudgetSpent` among them), and with ReadsPastLastRound when the reply after the last answered
 *  round still only reads skill files. */
export function replayOnce(
  body: RequestBody,
  arm: Arm,
  gateway: Gateway,
  options: ReplayOptions = {}
): Promise<Replayed> {
  const own = freshIdentity(isResponsesBody(body) ? body : withCacheBreakpoint(body));
  return replaySent(own.body, own.id, arm, gateway, options);
}

/** One arm's runs in a round: its arm, its captured body, the headers that go with it (its
 *  capture's `anthropic-*` headers, none beside a Responses body), the installed copies of its
 *  other skills (`ReplayOptions.copies`), and how many runs to make. */
export interface ArmRuns {
  readonly arm: Arm;
  readonly body: RequestBody;
  readonly headers?: Readonly<Record<string, string>>;
  readonly copies?: readonly SkillCopy[];
  readonly runs: number;
}

/** One run's place in a round's send order: its arm, and its number among that arm's runs of
 *  the round, from 0. */
export interface Slot {
  readonly arm: ArmName;
  readonly run: number;
}

/** The send order of runs of several kinds (a round's arms, a load test's tasks), `runs` of each
 *  kind `key`: every `{key, run}`, the kinds in the order given and each kind's runs in order,
 *  then shuffled by the seed (a Fisher-Yates shuffle whose numbers are SHA-256 of
 *  `<seed>:send:<counter>`). The same seed and counts give the same order. */
export function sendOrder<K extends string>(
  runs: readonly { readonly key: K; readonly runs: number }[],
  seed: string
): { readonly key: K; readonly run: number }[] {
  type Place = { readonly key: K; readonly run: number };
  const order = runs.flatMap(({ key, runs: n }) =>
    Array.from({ length: n }, (_, run): Place => ({ key, run }))
  );
  let counter = 0;
  const random = () =>
    createHash("sha256").update(`${seed}:send:${counter++}`).digest().readUIntBE(0, 6) / 2 ** 48;
  for (let index = order.length - 1; index > 0; index -= 1) {
    const other = Math.floor(random() * (index + 1));
    [order[index], order[other]] = [order[other] as Place, order[index] as Place];
  }
  return order;
}

/** One run of an interleaved round: its slot, its place in the send order (from 0), and its
 *  outcome, a step or a failure. */
export interface InterleavedRun extends Slot {
  readonly place: number;
  /** The session id every request of the run went with, or null for a body that names none. */
  readonly userId: string | null;
  readonly outcome: PromiseSettledResult<Replayed>;
}

/**
 * The runs of every arm of a round, sent in one order, `sendOrder` under `seed`, never one arm's
 * batch after another's: a gateway's state (its deployment, its cache, its load) then falls on
 * every arm alike. Each run is `replayOnce`'s, with a session id of its own when its body names
 * one. The runs start in that order, all at once but for one wait (the gateway bounds how many are
 * in flight, and gives its places in the order asked): the first run of each arm in the order goes
 * alone among its arm's, the next run in the order starting only once its first send has its
 * reply or has failed, so that the prefix its reply wrote to the cache serves its arm's later
 * runs. Those waits change no place of the order. Each arm's body to send is made once for all
 * its runs, each run's a copy of its top level with its own id (the body itself, when it names no
 * id). Each run's outcome is kept, whether it was a step or a failure, in the order sent.
 */
export async function replayInterleaved(
  arms: readonly ArmRuns[],
  gateway: Gateway,
  seed: string,
  options: Pick<ReplayOptions, "maxSkillRounds"> = {}
): Promise<InterleavedRun[]> {
  const byName = new Map<ArmName, ArmRuns & { readonly sent: RequestBody }>();
  for (const each of arms) {
    if (!Number.isInteger(each.runs) || each.runs < 0) {
      throw new Error(`a replay runs a whole number of times, 0 or more, not ${each.runs}`);
    }
    if (byName.has(each.arm.name)) throw new Error(`the arm ${each.arm.name} is given twice`);
    const sent = isResponsesBody(each.body) ? each.body : withCacheBreakpoint(each.body);
    byName.set(each.arm.name, { ...each, sent });
  }
  const order: Slot[] = sendOrder(
    arms.map((each) => ({ key: each.arm.name, runs: each.runs })),
    seed
  ).map(({ key, run }) => ({ arm: key, run }));
  const started = new Set<ArmName>();
  const runs: {
    slot: Slot;
    place: number;
    userId: string | null;
    replayed: Promise<Replayed>;
  }[] = [];
  for (const [place, slot] of order.entries()) {
    const each = byName.get(slot.arm);
    if (each === undefined) throw new Error(`no arm ${slot.arm}`);
    const first = !started.has(slot.arm);
    started.add(slot.arm);
    const settled = Promise.withResolvers<void>();
    const own = freshIdentity(each.sent);
    const replayed = replaySent(
      own.body,
      own.id,
      each.arm,
      gateway,
      {
        ...options,
        ...(each.headers === undefined ? {} : { headers: each.headers }),
        ...(each.copies === undefined ? {} : { copies: each.copies }),
      },
      settled.resolve
    );
    // Its outcome is read once every run has started; until then a failure is held, not thrown.
    replayed.catch(() => undefined);
    runs.push({ slot, place, userId: own.id, replayed });
    if (first) await settled.promise;
  }
  const outcomes = await Promise.allSettled(runs.map((run) => run.replayed));
  return runs.map(({ slot, place, userId }, index) => ({
    ...slot,
    place,
    userId,
    outcome: outcomes[index] as PromiseSettledResult<Replayed>,
  }));
}
