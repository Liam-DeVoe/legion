import { type ReactNode, useLayoutEffect, useRef } from "react";

import type { DisplayTree } from "../../api/types";
import { MarkdownBody } from "../refs/MarkdownBody";
import { TextAskCard } from "../refs/markdown-cards";
import type { TextSource } from "../refs/text-references";
import { authorText } from "./page-structure";

/**
 * A node's own text, as its row in the outline draws it (dispatch://INFRA-1196 R16, R20, R34),
 * in the size and the colour of the words it is drawn among.
 *
 * A text whose read sent the server's reading of it (`tree`) is drawn from that reading, with
 * the renderer every other surface draws a stored text with (`MarkdownBody`): a heading, bold
 * words, a list and a code span are drawn as what they are, never as the `#`, `**`, `-` and
 * backticks the author typed, and the browser parses nothing to draw them. A text whose read
 * sent none is drawn as stored, because the only other way to format it would be to parse it
 * here, which R20 refuses.
 *
 * The words are a reading of the text, as it was when it was drawn as stored: an `ask` block in a
 * document is a node of the tree of its own, where a reader answers it, so the text draws its
 * question and options rather than a second card to answer it in (`TextAskCard`). Whatever the
 * reading draws that takes the focus - a link, a reference, a picture's control - is out of the
 * tab order, as every other control inside a row is: the tree is one tab stop, and a key
 * reaches a row's links instead (R9, `useTreeKeymap.ts`'s `rowLinks`).
 */
export function RowText({
  markdown,
  source,
  tree,
}: {
  /** The text as stored: drawn as it is where `tree` is absent. */
  markdown: string;
  /** The record the text is, which its references link against (`MarkdownBody`'s `source`). */
  source?: TextSource;
  /** The server's reading of `markdown`, where its read sent one. */
  tree?: DisplayTree;
}): ReactNode {
  return tree === undefined ? (
    <p className="whitespace-pre-wrap break-words" {...authorText}>
      {markdown}
    </p>
  ) : (
    <DrawnText markdown={markdown} source={source} tree={tree} />
  );
}

/** What takes the focus in a drawn text: every control a reading can hold. */
const FOCUSABLE = "a[href], area[href], button, input, select, textarea, iframe, [tabindex]";

/** A text drawn from its reading, with every control it holds taken out of the tab order: the
 *  ones it is drawn with, and the ones the reading puts in after - a reference's link once its
 *  target answers, a picture once it loads - as they arrive. */
function DrawnText({
  markdown,
  source,
  tree,
}: {
  markdown: string;
  source: TextSource | undefined;
  tree: DisplayTree;
}): ReactNode {
  const drawn = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const element = drawn.current;
    if (element === null) return;
    const untab = (): void => {
      for (const control of element.querySelectorAll<HTMLElement>(FOCUSABLE)) {
        if (control.tabIndex !== -1) control.tabIndex = -1;
      }
    };
    untab();
    const watching = new MutationObserver(untab);
    watching.observe(element, { childList: true, subtree: true });
    return () => watching.disconnect();
  }, []);
  return (
    <div className="min-w-0 break-words" ref={drawn} {...authorText}>
      <TextAskCard.Provider value={undefined}>
        <MarkdownBody markdown={markdown} source={source} tree={tree} />
      </TextAskCard.Provider>
    </div>
  );
}
