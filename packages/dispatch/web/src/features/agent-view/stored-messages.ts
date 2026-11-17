import type { Message, MessageThreadV2 } from "../../api/types";

/** Every message of a session's stored thread reads, flat: each root and the replies under it,
 *  in the order the reads hold them. A thread read is its root message's own fields with its
 *  `replies` beside them, and one from a Dispatch that answers no replies carries none, which is
 *  its root alone. */
export function storedMessages(stored: readonly MessageThreadV2[]): Message[] {
  return stored.flatMap(({ replies, ...root }) => [root, ...(replies ?? [])]);
}
