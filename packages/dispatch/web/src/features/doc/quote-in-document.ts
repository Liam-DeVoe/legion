import { useQuery } from "@tanstack/react-query";

import type { ArtifactText, DisplayTree } from "../../api/types";
import type { ComposerAnchor } from "../conversation/composer-model";
import { wordsOf } from "../refs/tree-fragment";
import { liveTextQueryOptions } from "./ProofDocument";

/** `text` with each run of whitespace read as one space. The editor names a selection's words with
 *  a line break between two blocks (`textBetween` in `dispatch-action-bar.ts`), where the
 *  document's words hold one space (`wordsOf`), so the two are compared in this form. */
function spaced(text: string): string {
  return text.replace(/\s+/gu, " ").trim();
}

/** The document's words as its text read gives them (`wordsOf` of `markdown_tree`); undefined for
 *  a read that carries no tree. */
function documentWords(text: ArtifactText): string | undefined {
  const tree = text.markdown_tree as DisplayTree | undefined;
  return tree === undefined ? undefined : spaced(wordsOf(tree));
}

/**
 * Whether the document `anchor` names still holds the words a draft was written for
 * (`anchor.quote`), as the page holds the document's text now: the live text read every open
 * document keeps (`liveTextQueryOptions`, `ProofDocument`), which the page reads again after an
 * event of a comment anchored there (`eventRefreshTargets`). This read asks for nothing itself.
 *
 * The words can leave the document while the draft waits. A selection inside another comment's
 * pending insert suggestion quotes that comment's words, and the comment's delete rejects the
 * insert and takes them out of the document: they are then a deleted post's words, which no
 * surface may draw. So a draft whose words the document no longer holds is drawn without them.
 * False only where the page holds the document's text and it does not hold the words; while it
 * holds none, the words are drawn as before.
 */
export function useQuoteInDocument(anchor: Pick<ComposerAnchor, "artifact" | "quote">): boolean {
  const words = useQuery({
    ...liveTextQueryOptions(anchor.artifact),
    enabled: false,
    select: documentWords,
  }).data;
  return words === undefined || words.includes(spaced(anchor.quote));
}
