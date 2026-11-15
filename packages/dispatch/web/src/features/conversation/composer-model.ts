import type { Actor, DeliveryCapability } from "../../api/types";
import type { RecordMarkPlacement } from "../doc/editor";
import type { DeletablePost } from "../refs/post-marks";

/** The composer's data model: where a draft goes, what kind it is, and what it carries. The
 *  composer (`MentionComposer.tsx`), the request it freezes (`send-request.ts`) and every host read
 *  these from here, so no module imports a type back from one that imports it. */

export type ComposerOwner =
  | { readonly kind: "issue"; readonly issueKey: string }
  | { readonly kind: "artifact"; readonly artifactId: string; readonly project: string }
  | { readonly kind: "session"; readonly sessionId: string };

export type ComposerKind = "ask" | "comment" | "suggestion";

/** The text a margin compose is about. On a document that takes edits, the selection bar wrote a
 *  provisional mark over it, which the send names (`mark_id`) and the margin removes or retypes
 *  with the composer. A closed issue's document takes no edit, so there the bar wrote nothing: the
 *  send names the quote and which `occurrence` of it in the document's text the reader selected
 *  (`quoteAnchor`), and the server writes the comment's mark there. */
export type ComposerAnchor =
  | {
      readonly artifact: string;
      readonly mark_id: string;
      readonly occurrence?: never;
      /** The words the editor read on each side of `quote`, in the block it wrote the mark in, and
       *  how many readings of those words that block held, as it reported when the selection bar made
       *  it. The composer keeps it because the editor keeps nothing a rebuilt document or a reconnect
       *  does not take with it, and the margin hands it back when the mark has to be written again
       *  (`DocumentBridge.remarkMark`), where it is what tells the reader's own reading of those words
       *  from another of them. Absent where the editor had none to give, which leaves that recovery
       *  the quote alone. */
      readonly placement?: RecordMarkPlacement;
      readonly quote: string;
    }
  | {
      readonly artifact: string;
      readonly mark_id?: never;
      readonly occurrence: number;
      /** The server anchors a quote-anchored comment itself, so there is no mark to write again. */
      readonly placement?: never;
      readonly quote: string;
    };

/** Why a compose's mark could not be written again after the server refused its send for a mark
 *  the document does not hold, in the reader's words, kept with the mark it answers: the composer
 *  shows it in place of the server's words, and a newer anchor leaves it behind. */
export interface AnchorRefusal {
  readonly markId: string;
  readonly text: string;
}

export interface ReplyTarget {
  /** The parent's author, which the Replying-to chip draws through `AgentName`. */
  readonly actor: Actor;
  readonly excerpt: string;
  readonly id: string;
  readonly parentKind: "comment" | "message";
  /** Canonical targets carried by a comment parent, prefilled as editable @ mentions. */
  readonly mentions?: readonly { readonly target: string; readonly title: string }[];
  /** Kept while old targeted-message threads remain readable; S2 writes comments only. */
  readonly thread?: {
    readonly delivery: DeliveryCapability;
    readonly target: string;
  };
  readonly to?: string;
}

/** The post a reply quotes as the host's read of it stands now: the marks its author's
 *  corrections leave on it, its deletion and the time of its last edit. `ReplyTarget` is taken
 *  when Reply is pressed and kept with the send (`held-sends.ts`), so a draft open when the
 *  author deletes or edits the post learns it from here: its quote then says the post is deleted
 *  and gives no part of the excerpt it took, or marks the post edited (`ReplyQuote`). */
export type ReplyParent = DeletablePost & { readonly edited_at?: string | null };

/** A post a host's read holds, by its id. */
type ReadPost = ReplyParent & { readonly id: string };

/** The post `target` quotes among a host's reads, as they hold it now: a comment among
 *  `comments`, a message among the roots and replies of `messages`. Undefined with no target, and
 *  where the reads do not hold the post. */
export function replyParentOf(
  target: ReplyTarget | null,
  comments: readonly ReadPost[],
  messages: readonly { readonly message: ReadPost; readonly replies: readonly ReadPost[] | null }[]
): ReplyParent | undefined {
  if (target === null) return undefined;
  if (target.parentKind === "comment") {
    return comments.find((comment) => comment.id === target.id);
  }
  return messages
    .flatMap((read) => [read.message, ...(read.replies ?? [])])
    .find((message) => message.id === target.id);
}

/** One accepted mention: the target, and the span of the body that names it. */
export interface AcceptedMention {
  readonly end: number;
  readonly start: number;
  readonly target: string;
  readonly text: string;
}
