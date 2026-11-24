import type { ComponentProps, ReactNode } from "react";

import { MarkdownBody } from "./MarkdownBody";
import { MarkdownPreview } from "./MarkdownPreview";
import { DeletedBody } from "./PostMarks";
import type { PostDeletion } from "./post-marks";

/**
 * One post's body: the Markdown its author wrote, or the deleted line in its place. Every
 * surface that prints a comment's or an issue message's whole body goes through here rather
 * than through `MarkdownBody` itself, so the mark is written once.
 */
export function PostBody({
  deleted,
  ...body
}: ComponentProps<typeof MarkdownBody> & { deleted: PostDeletion | null }): ReactNode {
  return deleted === null ? (
    <MarkdownBody {...body} />
  ) : (
    <DeletedBody deleted={deleted} onRendered={body.onRendered} />
  );
}

/** `PostBody` for the surfaces that clamp one rendered line of a post (`MarkdownPreview`): a
 *  hover card, an unfurl card, a reply quote, the margin's collapsed root. */
export function PostPreview({
  deleted,
  ...preview
}: ComponentProps<typeof MarkdownPreview> & { deleted: PostDeletion | null }): ReactNode {
  return deleted === null ? (
    <MarkdownPreview {...preview} />
  ) : (
    <DeletedBody className={preview.className} deleted={deleted} />
  );
}
