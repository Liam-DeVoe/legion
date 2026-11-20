import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";

import type { ArtifactText, DisplayTree } from "../../api/types";
import { wordsOf } from "../refs/tree-fragment";
import { liveTextQueryOptions } from "./live-text-query";

/** The words of a document a draft was written for: the document, and the words as the editor
 *  named the reader's selection of them (a margin compose's `ComposerAnchor`). */
export interface QuotedWords {
  readonly artifact: string;
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

/**
 * Whether the document `words` names still holds the words a draft was written for
 * (`words.quote`), as the page holds the document's text now: the live text read every open
 * document keeps (`liveTextQueryOptions`, `ProofDocument`), which the page reads again after an
 * event of a comment anchored there (`eventRefreshTargets`). This read asks for nothing itself.
 *
 * The words can leave the document while the draft waits. A selection inside another comment's
 * pending insert suggestion quotes that comment's words, and the comment's delete rejects the
 * insert and takes them out of the document: they are then a deleted post's words, which no
 * surface may draw. So a draft whose words the document no longer holds is drawn without them.
 *
 * Only a read that landed after `anchoredAt`, when the draft's compose took the words, says so. No
 * keystroke and no update from a collaborator reads the text again, so a read from before then
 * can lack words the reader had just typed and selected, which are on screen. A draft with no
 * such time - one an older release kept - counts as taken before every read. False only where the
 * page holds such a read of the document's text and it does not hold the words; while it holds
 * none, the words are drawn as before.
 */
export function useQuoteInDocument(
  { artifact, quote }: QuotedWords,
  anchoredAt: number | undefined
): boolean {
  const { data: words, dataUpdatedAt } = useQuery({
    ...liveTextQueryOptions(artifact),
    enabled: false,
    select: documentWords,
  });
  return useMemo(
    () =>
      words === undefined ||
      (anchoredAt !== undefined && dataUpdatedAt <= anchoredAt) ||
      words.includes(spaced(quote)),
    [anchoredAt, dataUpdatedAt, quote, words]
  );
}
