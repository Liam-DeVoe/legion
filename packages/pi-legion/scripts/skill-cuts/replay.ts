/**
 * The replay of one arm of a case (plan, R5 `replay.ts`): its captured request body sent to the
 * gateway, and the reply that is the judged step.
 *
 * A reply whose every tool call is a read of a file of the case's skill (a `read` of
 * `skill://<skill>` or `skill://<skill>/<path>`, as cases.ts's `skillReadFile` reads one) is not
 * the step: the agent is still loading the skill. Each of those reads is answered the way the
 * arm's files would answer it (arms.ts's `skillFile`: the file under a `[Skill file: <path>]`
 * header whose path is the skill's installed one, the same in every arm, or Oh My Pi's not-found
 * result for a file the arm lacks), as one `tool_result` block in the shape Oh My Pi sends (F2),
 * and the body goes again with the reply and the answers added; at most `maxSkillRounds` times
 * (MAX_SKILL_ROUNDS). The first reply that holds no tool call, or any other tool call (a read of
 * another skill, a read of a path on disk, a read of a directory of the skill, any other tool), is
 * the judged step, and so is the reply after the last round, whatever it holds.
 */
import { statSync } from "node:fs";
import path from "node:path";
import { type Arm, skillFile } from "./arms";
import { skillReadFile } from "./cases";
import type { ContentBlock, Gateway, MessagesBody, Reply, Spent } from "./gateway";

/** The most times a replay answers a reply's skill reads and sends again. */
export const MAX_SKILL_ROUNDS = 3;

/** One replay: the judged step, how many rounds of skill reads were answered before it, and what
 *  each reply used, in the order sent. */
export interface Replayed {
  readonly step: Reply;
  readonly rounds: number;
  readonly usage: Spent[];
}

export interface ReplayOptions {
  readonly maxSkillRounds?: number;
}

/** A tool call that reads a file of the arm's skill: the call's id and the file, as a path inside
 *  the skill's directory. */
interface SkillRead {
  readonly id: string;
  readonly file: string;
}

/** The skill read `block` is, or undefined when it is any other tool call. A read of a directory
 *  of the skill is none: Oh My Pi answers it with a listing, which the arm does not rebuild. */
function skillRead(block: ContentBlock, arm: Arm): SkillRead | undefined {
  const { id, name, input } = block;
  if (typeof id !== "string" || name !== "read") return undefined;
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

/** Sends `body` for `arm`, answers its skill reads, and returns the judged step. Rejects when a
 *  send does (the gateway's `BudgetSpent` among them). */
export async function replayOnce(
  body: MessagesBody,
  arm: Arm,
  gateway: Gateway,
  { maxSkillRounds = MAX_SKILL_ROUNDS }: ReplayOptions = {}
): Promise<Replayed> {
  const messages = [...body.messages];
  const usage: Spent[] = [];
  for (let rounds = 0; ; rounds += 1) {
    const reply = await gateway.send({ ...body, messages: [...messages] });
    usage.push(reply.spent);
    const calls = reply.message.content.filter((block) => block.type === "tool_use");
    const reads = calls.map((block) => skillRead(block, arm));
    if (
      rounds >= maxSkillRounds ||
      calls.length === 0 ||
      !reads.every((read): read is SkillRead => read !== undefined)
    ) {
      return { step: reply, rounds, usage };
    }
    messages.push(
      { role: "assistant", content: reply.message.content },
      {
        role: "user",
        content: reads.map((read) => {
          const { text, isError } = skillFile(arm, read.file);
          return {
            type: "tool_result",
            tool_use_id: read.id,
            content: [{ type: "text", text }],
            is_error: isError,
          };
        }),
      }
    );
  }
}

/**
 * `n` replays of `body` for `arm`, each as `replayOnce`, each one's outcome kept whether it was a
 * step or a failure, in the order started. The first goes alone and the rest after it, all at
 * once (the gateway bounds how many are in flight): the bodies are the same, so the cache the
 * first one writes serves the others' shared prefix.
 */
export async function replay(
  body: MessagesBody,
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
