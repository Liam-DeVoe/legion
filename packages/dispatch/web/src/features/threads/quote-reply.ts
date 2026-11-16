// Quote reply (prototype, `e2e/ask-card-demo/README.md`): the reader selects words of an ask, a
// comment or a message and presses Quote reply on the selection bar (`SelectionBar.tsx`), or `q`.
// The reply box of that same thread opens, or is used where it is open, and takes the words as a
// Markdown quote at the end of what it holds, with the cursor on the empty line under it.
import { hashKey, type MutationKey } from "@tanstack/react-query";
import { type RefObject, useLayoutEffect } from "react";

import { type KeyBinding, type KeymapScope, useKeymap } from "../shell/keymap";

/**
 * The selected text as Markdown quote lines, read as the page shows it rather than from the
 * source Markdown: each line of the selection becomes a paragraph of the quote, `> ` before it
 * and a bare `>` between two of them, so the lines the page draws apart stay apart where the quote
 * is drawn. Blank and trailing space goes; an empty selection quotes nothing (`""`).
 */
export function quoteMarkdown(selected: string): string {
  return selected
    .replace(/\r\n?/g, "\n")
    .replace(/\u00a0/g, " ")
    .split("\n")
    .map((line) => line.trimEnd())
    .filter((line) => line.trim() !== "")
    .map((line) => `> ${line}`)
    .join("\n>\n");
}

/**
 * What a reply box holds once `selected` is quoted into it: `current`, then a blank line where it
 * holds any text, then the quote and a blank line, and where the cursor goes, the empty line
 * under that. Null where the selection quotes nothing.
 */
export function withQuote(
  current: string,
  selected: string
): { readonly caret: number; readonly text: string } | null {
  const quote = quoteMarkdown(selected);
  if (quote === "") return null;
  const kept = current.trimEnd();
  const text = `${kept === "" ? "" : `${kept}\n\n`}${quote}\n\n`;
  return { caret: text.length, text };
}

/** Grows a reply box to show what a quote put in it, as one the reader typed into grows. */
export function fitToText(field: HTMLTextAreaElement): void {
  const frame = field.offsetHeight - field.clientHeight;
  field.style.height = "auto";
  field.style.height = `${Math.min(field.scrollHeight + frame, Math.round(window.innerHeight / 2))}px`;
}

/** Puts the cursor at `caret` in `field`, with the focus, once the box holds its new text, and
 *  brings the box, grown to show it, into view. */
export function placeCaret(field: HTMLTextAreaElement, caret: number): void {
  fitToText(field);
  field.focus({ preventScroll: true });
  field.setSelectionRange(caret, caret);
  field.scrollIntoView({ block: "nearest" });
}

// ---------------------------------------------------------------------------------------------
// The texts a reader may quote
// ---------------------------------------------------------------------------------------------

/** What one text a reader may quote does with a selection of its words. */
export interface QuoteTarget {
  /** Quotes `selected` into the reply box of the text's own thread: opens it, or takes the one
   *  open, and gives it the focus. */
  readonly quote: (selected: string) => void;
  /** Present where the text offers Define (an ask's words): opens it on the selection. */
  readonly define?: () => void;
}

const targets = new WeakMap<Element, QuoteTarget>();

/** Records `target` for the text drawn under `ref`, again on every commit so it reaches that
 *  render's state; `undefined` records nothing. A selection is the innermost text's that holds it. */
export function useQuoteTarget(
  ref: RefObject<Element | null>,
  target: QuoteTarget | undefined
): void {
  useLayoutEffect(() => {
    const element = ref.current;
    if (element === null || target === undefined) return undefined;
    targets.set(element, target);
    return () => {
      if (targets.get(element) === target) targets.delete(element);
    };
  });
}

/** The `<input>` types that take no typed text: while one of them has the focus, a selection on
 *  the page is still the reader's (ticking an option must not hide the bar). */
const NON_TEXT_INPUTS: Record<string, true> = {
  button: true,
  checkbox: true,
  color: true,
  file: true,
  hidden: true,
  image: true,
  radio: true,
  range: true,
  reset: true,
  submit: true,
};

function typingIn(element: Element | null): boolean {
  if (element instanceof HTMLTextAreaElement || element instanceof HTMLSelectElement) return true;
  if (element instanceof HTMLInputElement) return NON_TEXT_INPUTS[element.type] !== true;
  return element instanceof HTMLElement && element.isContentEditable;
}

/** A selection of words the reader may quote: the text that holds them, the words as the page
 *  shows them, and where they and that text stand on screen: the whole selection's box, its first
 *  line's, and the text's own column. */
export interface QuotableSelection {
  readonly target: QuoteTarget;
  readonly text: string;
  readonly rect: DOMRect;
  readonly firstLine: DOMRect;
  readonly column: DOMRect;
}

/** The selection, where it lies inside one text a reader may quote and no field has the focus:
 *  a document's editor draws its own bar, and a field's words are the reader's own. */
export function quotableSelection(): QuotableSelection | null {
  if (typingIn(document.activeElement)) return null;
  const selection = window.getSelection();
  if (selection === null || selection.rangeCount === 0 || selection.isCollapsed) return null;
  const range = selection.getRangeAt(0);
  for (let node: Node | null = range.commonAncestorContainer; node !== null; ) {
    if (node instanceof HTMLElement && node.isContentEditable) return null;
    const target = node instanceof Element ? targets.get(node) : undefined;
    if (target !== undefined && node instanceof Element) {
      const text = selection.toString();
      if (quoteMarkdown(text) === "") return null;
      // A text drawn without a box of its own (`QuotableText`) stands in its parent's column.
      const box = node.getBoundingClientRect();
      const column = box.width > 0 ? box : (node.parentElement?.getBoundingClientRect() ?? box);
      const rect = range.getBoundingClientRect();
      const firstLine = [...range.getClientRects()].find((line) => line.width > 0) ?? rect;
      return { column, firstLine, rect, target, text };
    }
    node = node.parentNode;
  }
  return null;
}

/** Quotes the selection into its thread's reply box, and says whether there was one to quote. */
export function quoteSelection(): boolean {
  const selected = quotableSelection();
  if (selected === null) return false;
  selected.target.quote(selected.text);
  return true;
}

/** `q`, in `scope`'s section of the shortcut help: quotes the selected words of an ask, a comment
 *  or a message on the page in a reply. */
export function useQuoteReplyKey(scope: KeymapScope): void {
  useKeymap(scope, [quoteReplyKey("Quote selected words in a reply", quoteSelection)]);
}

/** The binding `q` is, for a host that decides when it applies and what it quotes. */
export function quoteReplyKey(
  label: string,
  run: () => void,
  when: () => boolean = () => quotableSelection() !== null
): KeyBinding {
  return {
    id: "quote-reply",
    keys: "q",
    label,
    // The palette's own field takes the selection, so a row of it would quote nothing.
    palette: false,
    run,
    when,
  };
}

// ---------------------------------------------------------------------------------------------
// A composer's quote, on its way
// ---------------------------------------------------------------------------------------------

/** The selection each composer is to quote, by its send's name, until the composer takes it: one
 *  that is mounted at once, one the press opens as it mounts. */
const waiting = new Map<string, string>();
const wakers = new Map<string, () => void>();

/** Hands `selected` to the composer whose send is named `mutationKey`, mounted or about to be. */
export function quoteInto(mutationKey: MutationKey, selected: string): void {
  const name = hashKey(mutationKey);
  waiting.set(name, selected);
  wakers.get(name)?.();
}

/** The selection waiting for the composer named `name`, taken; undefined where none waits. */
export function takeQuote(name: string | undefined): string | undefined {
  if (name === undefined) return undefined;
  const selected = waiting.get(name);
  waiting.delete(name);
  return selected;
}

/** Whether a selection waits for the composer named `name`, left where it is. */
export function quoteWaiting(name: string | undefined): boolean {
  return name !== undefined && waiting.has(name);
}

/** Calls `wake` whenever a selection is handed to the mounted composer named `name`. */
export function onQuote(name: string, wake: () => void): () => void {
  wakers.set(name, wake);
  return () => {
    if (wakers.get(name) === wake) wakers.delete(name);
  };
}
