import type { ReactNode } from "react";

import type { ArtifactOwner } from "../../api/client";
import type { Ask } from "../../api/types";
import type { DocumentOwner } from "../refs/routes";
import type { DwellRef } from "../shell/useDwell";
import type { ThreadOrder } from "../threads/thread-order";
import { AskThread, type AskThreadQuery, type CreateReply } from "./AskThread";
import { AskThreadDisclosure } from "./AskThreadDisclosure";

/** Where a file pasted or dropped into an ask's answer, or into a reply under it, goes: the ask's
 *  issue, else the project of the document it was asked on (`documentOwner`, the host's, where
 *  the ask read carries none), else nowhere, and the box takes a paste as text. */
export function askUploadOwner(
  ask: Pick<Ask, "anchor_artifact" | "issue_key">,
  documentOwner: DocumentOwner | undefined
): ArtifactOwner | undefined {
  if (ask.issue_key !== null) return { issue: ask.issue_key };
  const project = (documentOwner ?? ask.anchor_artifact)?.project;
  return project === undefined ? undefined : { project };
}

/**
 * An ask's exchange: the replies on it and the box a reply is written in, drawn from the read of
 * its thread the host holds (`useAskThreadRead`), inline or behind **Write a reply**
 * (`collapsed`). Both its hosts draw it: an ask's card, under its question and its form or its
 * record (`AskCard`), and the Inbox reading pane's approval request, whose column draws the
 * question and answers it with controls of its own and draws the exchange alone
 * (`ApprovalExchange` in `ReadingPane.tsx`).
 */
export function AskExchange({
  ask,
  collapsed = false,
  createReply,
  currentReplyId,
  embedded = false,
  markNewest,
  order,
  owner,
  repliesWhileOpen = false,
  thread,
  unread,
  uploadOwner,
}: {
  /** The ask as it now stands, which says whether it takes a reply (`takesReply`). */
  readonly ask: Ask;
  /** Draws the thread behind **Write a reply** (`AskThreadDisclosure`), as the Conversation and
   *  a decision block do; inline otherwise. */
  readonly collapsed?: boolean;
  readonly createReply?: CreateReply;
  /** The reply a deep link named; only the Conversation passes one (`AskThread`). */
  readonly currentReplyId?: string;
  /** Drawn inside an open ask's card, in the card's compact flow (`AskThread`). */
  readonly embedded?: boolean;
  /** The shared thread read mark for the ask (`useMarkThreadRead`), placed on its newest reply
   *  where that reply is the newest write the host shows. */
  readonly markNewest?: DwellRef;
  /** The reader's thread order (`threads/thread-order.ts`). */
  readonly order: ThreadOrder;
  /** The project document a document ask belongs to, so each reply names its own reference. */
  readonly owner?: DocumentOwner;
  /** Keeps the reply box under the open ask too, for the host that draws no answer area to ask
   *  back in (`AskThread`'s `repliesWhileOpen`). The collapsed thread takes none. */
  readonly repliesWhileOpen?: boolean;
  readonly thread: AskThreadQuery;
  /** The ids of the replies the reader has not read: an unread reply is never folded away. */
  readonly unread?: ReadonlySet<string>;
  /** Where a file pasted or dropped into a reply goes (`askUploadOwner`). */
  readonly uploadOwner?: ArtifactOwner;
}): ReactNode {
  return collapsed ? (
    <AskThreadDisclosure
      ask={ask}
      createReply={createReply}
      currentReplyId={currentReplyId}
      embedded={embedded}
      markNewest={markNewest}
      order={order}
      owner={owner}
      thread={thread}
      unread={unread}
      uploadOwner={uploadOwner}
    />
  ) : (
    <AskThread
      ask={ask}
      createReply={createReply}
      currentReplyId={currentReplyId}
      embedded={embedded}
      markNewest={markNewest}
      order={order}
      owner={owner}
      repliesWhileOpen={repliesWhileOpen}
      thread={thread}
      unread={unread}
      uploadOwner={uploadOwner}
    />
  );
}
