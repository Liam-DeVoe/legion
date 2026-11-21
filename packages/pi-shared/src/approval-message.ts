import type * as host from "@oh-my-pi/pi-coding-agent";

/**
 * How both plugins' `context` handlers read one model request: which message is the approval
 * message, which kind of request it is, and the keys and texts they match messages on. pi-legion
 * places the approval message and pi-envoy its people's-answers message; neither plugin's shipped
 * code imports the other's, so what both read lives here, one body for both.
 */

/**
 * The custom type of the approval message: the message pi-legion places in a request of a root
 * architect's session while a person's approval of its spec stands. The message goes after every
 * message another plugin adds, so pi-envoy puts its answers message before a trailing run of
 * approval messages and never anchors one on it: the approval message stays the request's last
 * message whichever plugin's `context` handler runs first.
 */
export const APPROVAL_MESSAGE_TYPE = "legion-approval";

/** Whether `message` is the approval message: a custom message of `APPROVAL_MESSAGE_TYPE`. */
export function isApprovalMessage(message: unknown): boolean {
  return (
    typeof message === "object" &&
    message !== null &&
    "role" in message &&
    message.role === "custom" &&
    "customType" in message &&
    message.customType === APPROVAL_MESSAGE_TYPE
  );
}

/**
 * `messages` cut before its trailing run of approval messages (`isApprovalMessage`): `head`, every
 * message up to the last one that is not an approval message, and `tail`, the approval messages
 * after it, in order. A request that does not end with one is all `head`, the same array.
 */
export function splitTrailingApproval(messages: readonly unknown[]): {
  readonly head: readonly unknown[];
  readonly tail: readonly unknown[];
} {
  let end = messages.length;
  while (end > 0 && isApprovalMessage(messages[end - 1])) end -= 1;
  return end === messages.length
    ? { head: messages, tail: [] }
    : { head: messages.slice(0, end), tail: messages.slice(end) };
}

/** The text of `message`'s first content block when `message` has `role` and that block has text:
 *  where a handler looks for a message of its own, and how it reads the host's own prompts. */
export function leadingText(message: unknown, role: string): string | undefined {
  if (
    typeof message !== "object" ||
    message === null ||
    !("role" in message) ||
    message.role !== role ||
    !("content" in message) ||
    !Array.isArray(message.content)
  ) {
    return undefined;
  }
  const [first]: unknown[] = message.content;
  if (
    typeof first === "object" &&
    first !== null &&
    "text" in first &&
    typeof first.text === "string"
  ) {
    return first.text;
  }
  return undefined;
}

/** The text of `message` when the host wrote it (`attribution: "agent"`) with `role`. */
function hostText(message: unknown, role: string): string | undefined {
  return typeof message === "object" &&
    message !== null &&
    "attribution" in message &&
    message.attribution === "agent"
    ? leadingText(message, role)
    : undefined;
}

/** A context message as matching reads it: its role and timestamp, and a tool result's call id,
 *  since the tool results of one round can share a timestamp. */
export function messageKey(message: unknown): string {
  if (typeof message !== "object" || message === null) return "";
  const role = "role" in message ? String(message.role) : "";
  const timestamp = "timestamp" in message ? String(message.timestamp) : "";
  const call = "toolCallId" in message ? `#${String(message.toolCallId)}` : "";
  return `${role}@${timestamp}${call}`;
}

/** The first line of Oh My Pi's side-turn notice (`side-channel-no-tools.md`): the `developer`
 *  message `AgentSession.runEphemeralTurn` puts after the conversation, before the side prompt. */
const SIDE_TURN_NOTICE = "Ephemeral side-channel turn; reuses current conversation context.";

/**
 * Whether the request is a side turn: Oh My Pi's `runEphemeralTurn` (a Dispatch BTW, the stop-time
 * self-check, `/btw`, the idle recap) converts `[...conversation, notice, ...history, prompt]`,
 * where the notice is a host `developer` message, the history `/btw`'s earlier questions and
 * answers, and the prompt a host user message.
 */
export function isSideTurn(messages: readonly unknown[]): boolean {
  if (hostText(messages.at(-1), "user") === undefined) return false;
  for (let index = messages.length - 2; index >= 0; index -= 1) {
    const message = messages[index];
    const role =
      typeof message === "object" && message !== null && "role" in message
        ? message.role
        : undefined;
    if (role !== "user" && role !== "assistant") {
      return hostText(message, "developer")?.includes(SIDE_TURN_NOTICE) === true;
    }
  }
  return false;
}

/** How Oh My Pi's handoff prompt (`handoff-document.md`) begins. */
const HANDOFF_PROMPT = "<critical>\nWrite a handoff document for another instance of yourself.";

/** Whether the request is Oh My Pi's handoff prompt: its last message is a host user message that
 *  begins with `HANDOFF_PROMPT`. The same words from the person are their own prompt. */
export function isHandoffPrompt(messages: readonly unknown[]): boolean {
  return hostText(messages.at(-1), "user")?.startsWith(HANDOFF_PROMPT) === true;
}

/** The host's roster of live sessions as a plugin reads it: `host.AgentRegistry`, which a host
 *  build without that export leaves undefined. The caller passes it in, so this module loads where
 *  the host package does not resolve. */
export type HostAgentRegistry = { global(): { list(): readonly host.AgentRef[] } } | undefined;

/** The host's live `AgentSession`, as far as these plugins read it. */
export type LiveAgentSession = NonNullable<host.AgentRef["session"]>;

/**
 * The live `AgentSession` under `session`, its live id, found in the host's roster (`registry`) at
 * every call, since the host keeps one `AgentSession` across `/new`, `/resume` and a fork and
 * changes its id. Undefined on a host without the registry export, or listing no live session
 * under that id.
 */
export function liveAgentSession(
  registry: HostAgentRegistry,
  session: string
): LiveAgentSession | undefined {
  return (
    registry
      ?.global()
      .list()
      .find((ref) => ref.session?.sessionManager.getSessionId() === session)?.session ?? undefined
  );
}

/**
 * Whether Oh My Pi is compacting `session` now, so that the request a `context` handler sees may be
 * a compaction's own: the live `AgentSession`'s `isCompacting` (an automatic or manual compaction,
 * or a handoff), or its `compactionSpeculation` reading `"running"` (a speculative one), getters
 * the declared host surface leaves out. A speculative compaction runs beside the main loop, so a
 * main-loop request made while it runs reads as compacting too. A session the roster does not
 * list reads as not compacting.
 */
export function sessionCompacting(registry: HostAgentRegistry, session: string): boolean {
  const live = liveAgentSession(registry, session);
  return (
    live !== undefined &&
    (("isCompacting" in live && live.isCompacting === true) ||
      ("compactionSpeculation" in live && live.compactionSpeculation === "running"))
  );
}
