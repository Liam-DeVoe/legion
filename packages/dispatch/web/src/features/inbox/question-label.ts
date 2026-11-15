import { type ReactNode, useRef } from "react";

import type { DisplayTree } from "../../api/types";
import { usePlainHeadline } from "../refs/plain-text";

/** An ask's question, with the reading Go made of it where the read carries one. */
interface Question {
  readonly question: string;
  readonly question_tree?: DisplayTree;
}

/**
 * An ask's question as one line of plain words, for the label of a write on that ask: the line
 * the Writes indicator and its refusals read as text (the reading list's `write-label`,
 * `DISPATCH_READING_SURFACES`). The line is the question's plain-text projection
 * (`usePlainHeadline`), read from `question_tree` where the read carries one and from the
 * question's markdown where it does not, so `**Ship** it? dispatch://CORE-1` reads `Ship it?
 * CORE-1 Fix the thing`: the words the card draws, never their source.
 *
 * A write asks for its label as it goes out, after the render that drew the question, so `line`
 * reads the projection the latest render made rather than the one its own render saw. Until the
 * engine has answered, `line` is the question's own characters, markdown syntax and all: nothing
 * else gives its words without that syntax. The engine answers one pre-paint commit after this
 * mounts on a page that has loaded it, and the card a write is pressed on has loaded it to draw
 * the question, so a press made from that card reads the projection.
 *
 * `reads` draws nothing: it reads the names of the references the line holds, and the caller
 * mounts it wherever it draws.
 */
export function useQuestionLabel(ask: Question): { line: () => string; reads: ReactNode } {
  const { reads, text } = usePlainHeadline(ask.question_tree, ask.question);
  const latest = useRef(ask.question);
  latest.current = text?.text ?? ask.question;
  return { line: () => latest.current, reads };
}
