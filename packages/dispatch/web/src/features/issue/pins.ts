import type { DispatchEvent, Marks } from "../../api/types";

const pinnedItemPrefix = "pinned_items:";

export function eventItemId(event: Pick<DispatchEvent, "id">): string {
  return `event:${event.id}`;
}

/** The `pinned_items` entry that records `id` (an `eventItemId`) as pinned. */
export function pinnedItemMarker(id: string): string {
  return `${pinnedItemPrefix}${id}`;
}

export function isPinnedEvent(dismissed: readonly string[], eventId: number): boolean {
  return dismissed.includes(pinnedItemMarker(eventItemId({ id: eventId })));
}

export function pinnedEventIds(dismissed: readonly string[]): string[] {
  const prefix = `${pinnedItemPrefix}event:`;
  return dismissed
    .filter((item) => item.startsWith(prefix))
    .map((item) => item.slice(prefix.length));
}

/** What a Conversation card pins: the conversation itself. Only an ask carries an event id of
 *  its own (`opened_event_id`), and a card built from `/comments`, `/messages` or `/handoffs` has
 *  none at all, so a card is pinned under its own `<kind>:<id>` - the id the list already gives
 *  it - beside the `event:<id>` an Activity row still pins under. A pin made before the
 *  Conversation became a list of conversations is that event's, and stays the margin's to show
 *  and unpin. */
export const CONVERSATION_PIN_KINDS = ["ask", "comment", "message", "handoff"] as const;
export type ConversationPinKind = (typeof CONVERSATION_PIN_KINDS)[number];

export interface PinnedConversation {
  readonly kind: ConversationPinKind;
  readonly id: string;
}

/** A conversation's own id, the one its card and its pin marker both spell: `<kind>:<id>`,
 *  which `pinnedConversations` reads back apart. */
export function conversationItemId(kind: ConversationPinKind, id: string): string {
  return `${kind}:${id}`;
}

/** `conversationId` is a conversation's own id (`ask:<id>`, `comment:<id>`, `message:<id>`,
 *  `handoff:<id>`). */
export function isPinnedConversation(
  dismissed: readonly string[],
  conversationId: string
): boolean {
  return dismissed.includes(pinnedItemMarker(conversationId));
}

export function pinnedConversations(dismissed: readonly string[]): PinnedConversation[] {
  const pinned: PinnedConversation[] = [];
  for (const item of dismissed) {
    if (!item.startsWith(pinnedItemPrefix)) continue;
    const marker = item.slice(pinnedItemPrefix.length);
    const separator = marker.indexOf(":");
    const kind = CONVERSATION_PIN_KINDS.find(
      (candidate) => candidate === marker.slice(0, separator)
    );
    if (kind !== undefined) pinned.push({ id: marker.slice(separator + 1), kind });
  }
  return pinned;
}

/** The viewer's state of one issue with every field present: what their mark of it holds
 *  (`GET /api/v2/me/marks?kind=issue`), which leaves out a field it holds no value for. */
export interface IssueState {
  readonly last_read_seq: number;
  readonly pinned: boolean;
  /** The markers of the issue's pinned items (`pinnedItemMarker`). */
  readonly pinned_items: readonly string[];
  readonly seq: number;
}

/** The viewer's state of an issue, defaulted: no mark and an unvisited issue read alike. */
export function stateForIssue(state: Marks | undefined, issueKey: string): IssueState {
  const mark = state?.[issueKey];
  return {
    last_read_seq: mark?.last_read_seq ?? 0,
    pinned: mark?.pinned ?? false,
    pinned_items: mark?.pinned_items ?? [],
    seq: mark?.seq ?? 0,
  };
}
