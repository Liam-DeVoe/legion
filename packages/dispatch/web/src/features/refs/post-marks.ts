import type { Actor } from "../../api/types";
import type { AgentText } from "./AgentName";

/**
 * The deletion marks every read of a comment or an issue message carries: `deleted_at` and
 * `deleted_by`, both null on a post nobody deleted. An event payload written before the post
 * corrections carries neither, so both are optional here and a payload reads the same way a
 * row does.
 */
export interface DeletablePost {
  readonly deleted_at?: string | null;
  readonly deleted_by?: Actor | null;
}

/** When a post's author deleted it, and who they are. The server records the actor on every
 *  delete; `by` is null only for a payload that carries the time alone. */
export interface PostDeletion {
  readonly at: string;
  readonly by: Actor | null;
}

/**
 * The post's deletion, or null for a post nobody deleted. One reading of the two fields, so
 * every surface marks the same posts.
 *
 * This module holds no component on purpose: the reference reads (`reference-target.ts`) answer
 * a deletion too, and they are what `MarkdownBody` resolves its references through, so a
 * component here would close an import cycle.
 */
export function postDeletion(post: DeletablePost | undefined): PostDeletion | null {
  const at = post?.deleted_at;
  if (at === undefined || at === null) {
    return null;
  }
  return { at, by: post?.deleted_by ?? null };
}

/** The deleted line's words as plain text, for a place that holds only a string - an
 *  `aria-label`, a `title`, a checkbox's name, a reference link's text, the issue page's model:
 *  `Deleted by <name>`, the actor named by `text.label` - the rule `DeletedBody` names the author
 *  by (`useAgentText`), or, for a caller that reads no registry, `actorLabelText` - or `Deleted`
 *  for a payload that names nobody. The time stays the visible line's. It lives here rather than
 *  beside `DeletedBody` because a reference link (`RefLink`) draws it, and `PostBody.tsx` imports
 *  `MarkdownBody`, which draws that link. */
export function deletedLineText(deleted: PostDeletion, text: Pick<AgentText, "label">): string {
  return deleted.by === null ? "Deleted" : `Deleted by ${text.label(deleted.by)}`;
}

/** The name of a post its author edited - a comment, a message or a block of either - as a link
 *  to it writes the name: `words` then ` · edited`, the mark the post's byline writes
 *  (`EditedMark`). `words` as they are for a post nobody edited (`edited` false), and `""` for no
 *  words, which take no mark. A place that cuts a name to a length cuts `words` and marks what
 *  the cut left, so no cut takes the mark: a link's title (`useReferenceLabel`) and a row's link
 *  on the issue page (`rowReferenceWords`) both mark a name through here, and cannot drift
 *  apart. */
export function editedName(words: string, edited: boolean): string {
  return !edited || words === "" ? words : `${words} · edited`;
}
