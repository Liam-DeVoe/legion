/**
 * The hook Oh My Pi loads for a capture (`omp --hook <this file>`, capture.ts), built from the
 * scratch hook plan task t1 proved on F2 (testdata/CAPTURE.md, "The scratch tools"):
 *
 * - Its `context` handler gives the model the arm's messages: the JSON list of Oh My Pi message
 *   objects in the file SKILL_CUTS_MESSAGES names, in place of the run's own first message (the
 *   placeholder prompt), followed by every message the run itself added after it.
 * - Its `tool_call` handler blocks every tool call, so no tool runs in a capture.
 *
 * It imports nothing beyond Node's own modules, since Oh My Pi loads it from this directory alone.
 */
import { readFileSync } from "node:fs";

/** The variable that names the file of the arm's messages. */
export const MESSAGES_VARIABLE = "SKILL_CUTS_MESSAGES";

/** Why a call was blocked, as the model reads it in the call's result. */
export const BLOCK_REASON = "blocked by the replay hook: no tool runs in a capture";

/** The part of Oh My Pi's hook API this hook uses. */
interface HookApi {
  on(
    event: "context",
    handler: (event: { readonly messages?: readonly unknown[] }) => Promise<{ messages: unknown[] }>
  ): void;
  on(event: "tool_call", handler: () => Promise<{ block: true; reason: string }>): void;
}

export default function replayHook(pi: HookApi): void {
  const file = process.env[MESSAGES_VARIABLE];
  if (file === undefined || file === "") {
    throw new Error(`replay-hook: ${MESSAGES_VARIABLE} names no file of messages`);
  }
  const replayed: unknown = JSON.parse(readFileSync(file, "utf8"));
  if (!Array.isArray(replayed)) throw new Error(`replay-hook: ${file} holds no list of messages`);
  pi.on("context", async (event) => ({
    messages: [...replayed, ...(event.messages ?? []).slice(1)],
  }));
  pi.on("tool_call", async () => ({ block: true, reason: BLOCK_REASON }));
}
