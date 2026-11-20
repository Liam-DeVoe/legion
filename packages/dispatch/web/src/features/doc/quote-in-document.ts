import { type QueryClient, type QueryKey, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useMemo, useSyncExternalStore } from "react";

import { rowsOf } from "../../api/event-refresh";
import type { ArtifactText, Comment, DisplayTree } from "../../api/types";
import { postDeletion } from "../refs/post-marks";
import { wordsOf } from "../refs/tree-fragment";
import { liveTextQueryOptions } from "./live-text-query";

/** The words of a document a draft was written for: the document, the words as the editor named
 *  the reader's selection of them, and the pending inserts they lie in (a margin compose's
 *  `ComposerAnchor`). */
export interface QuotedWords {
  readonly artifact: string;
  readonly insertMarks?: readonly string[];
  readonly quote: string;
}

/** `text` with each run of whitespace read as one space. The editor names a selection's words with
 *  a line break between two blocks (`textBetween` in `dispatch-action-bar.ts`), where the
 *  document's words hold one space (`wordsOf`), so the two are compared in this form. */
function spaced(text: string): string {
  return text.replace(/\s+/gu, " ").trim();
}

/** Each text read's words (`documentWords`), worked out once for the read whichever surfaces ask:
 *  the open compose and each line that leads a kept draft read the same one. */
const wordsOfRead = new WeakMap<ArtifactText, string>();

/** The document's words as its text read gives them (`wordsOf` of `markdown_tree`); undefined for
 *  a read that carries no tree. */
function documentWords(text: ArtifactText): string | undefined {
  const tree = text.markdown_tree as DisplayTree | undefined;
  if (tree === undefined) return undefined;
  let words = wordsOfRead.get(text);
  if (words === undefined) {
    words = spaced(wordsOf(tree));
    wordsOfRead.set(text, words);
  }
  return words;
}

/** Whether `queryKey` names a comment list: an issue's (`["comments", key, …]`) or a document's
 *  (`["artifact", id, "comments"]`), the reads of the comments family a delete's correction writes
 *  its mark into (`markCorrectedPost`), which the server answers with the deleted comment in its
 *  place. */
function isCommentList(queryKey: QueryKey): boolean {
  return queryKey[0] === "comments" || (queryKey[0] === "artifact" && queryKey[2] === "comments");
}

/** Whether a comment list of `client`'s cache (`isCommentList`) lists, deleted, a comment
 *  anchored in the document `artifact` by one of `marks`: the comment of one of the inserts the
 *  words lie in. */
function holdsDeletedInsert(
  client: QueryClient,
  artifact: string,
  marks: readonly string[]
): boolean {
  return client
    .getQueryCache()
    .getAll()
    .some(
      ({ queryKey, state }) =>
        isCommentList(queryKey) &&
        rowsOf<Comment>(state.data).some(
          (comment) =>
            comment.anchor !== null &&
            comment.anchor.artifact_id === artifact &&
            marks.includes(comment.anchor.mark_id) &&
            postDeletion(comment) !== null
        )
    );
}

/**
 * Whether the document `words` names still holds the words a draft was written for
 * (`words.quote`), as the page holds them now. This read asks for nothing itself.
 *
 * The words can leave the document while the draft waits. A selection inside another comment's
 * pending insert suggestion quotes that comment's words, and the comment's delete rejects the
 * insert and takes them out of the document: they are then a deleted post's words, which no
 * surface may draw. So a draft whose words the document no longer holds is drawn without them.
 *
 * The first fact, which decides alone: the words lie in a pending insert (`words.insertMarks`)
 * whose comment a comment list of the page's cache holds deleted (`holdsDeletedInsert`). The
 * delete's correction writes that into every such list the page holds (`markCorrectedPost`), and
 * the server answers them with the deleted comment in its place, so the page knows it whatever it
 * has read of the document's text, and however old that read is: false then.
 *
 * Otherwise, words that left the document by any other edit: the live text read every open
 * document keeps (`liveTextQueryOptions`, `ProofDocument`), which the page reads again after an
 * event of a comment anchored there (`eventRefreshTargets`). Only a read that landed after
 * `anchoredAt`, when the draft's compose took the words, says they left. No keystroke and no
 * update from a collaborator reads the text again, so a read from before then can lack words the
 * reader had just typed and selected, which are on screen. A draft with no such time - one an
 * older release kept - counts as taken before every read. False only where the page holds such a
 * read of the document's text and it does not hold the words; while it holds none, the words are
 * drawn as before.
 */
export function useQuoteInDocument(
  { artifact, insertMarks, quote }: QuotedWords,
  anchoredAt: number | undefined
): boolean {
  const client = useQueryClient();
  const hasMarks = insertMarks !== undefined && insertMarks.length > 0;
  // The first fact reads only the comment lists, and a change to one sends an event for it, so a
  // draft whose words lie in no insert listens to nothing, and another one to those lists alone.
  const subscribe = useCallback(
    (changed: () => void) =>
      hasMarks
        ? client.getQueryCache().subscribe(({ query }) => {
            if (isCommentList(query.queryKey)) {
              changed();
            }
          })
        : () => {},
    [client, hasMarks]
  );
  const insertDeleted = useSyncExternalStore(
    subscribe,
    () => hasMarks && holdsDeletedInsert(client, artifact, insertMarks)
  );
  const { data: words, dataUpdatedAt } = useQuery({
    ...liveTextQueryOptions(artifact),
    enabled: false,
    select: documentWords,
  });
  return useMemo(
    () =>
      !insertDeleted &&
      (words === undefined ||
        (anchoredAt !== undefined && dataUpdatedAt <= anchoredAt) ||
        words.includes(spaced(quote))),
    [anchoredAt, dataUpdatedAt, insertDeleted, quote, words]
  );
}
