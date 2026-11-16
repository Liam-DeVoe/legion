import { afterEach, expect, test } from "bun:test";
import { cleanup, render } from "@testing-library/react";
import { type ReactNode, useRef } from "react";

import { quotableSelection, quoteSelection, useQuoteTarget } from "./quote-reply";

afterEach(() => {
  window.getSelection()?.removeAllRanges();
  cleanup();
});

/** A text a reader may quote, or, with `quote` null, one whose thread takes no reply now. */
function Text({
  children,
  id,
  quote,
}: {
  children: ReactNode;
  id: string;
  quote: ((blocks: readonly string[]) => void) | null;
}): ReactNode {
  const ref = useRef<HTMLDivElement>(null);
  useQuoteTarget(ref, quote === null ? null : { quote });
  return (
    <div className="dispatch-markdown" data-testid={id} ref={ref}>
      {children}
    </div>
  );
}

/** Selects every word under the element `id` names. */
function selectIn(container: HTMLElement, id: string): void {
  const element = container.querySelector(`[data-testid="${id}"]`);
  if (element === null) throw new Error(`no ${id}`);
  const range = document.createRange();
  range.selectNodeContents(element);
  const selection = window.getSelection();
  selection?.removeAllRanges();
  selection?.addRange(range);
}

test("a selection inside a text a reader may quote hands that text its Markdown blocks", () => {
  const quoted: (readonly string[])[] = [];
  const { container } = render(
    <Text id="comment" quote={(blocks) => quoted.push(blocks)}>
      <p>
        Ship <strong>now</strong>.
      </p>
      <p>
        Run <code>__tests__</code> first.
      </p>
    </Text>
  );
  selectIn(container, "comment");
  expect(quotableSelection()?.blocks).toEqual(["Ship **now**.", "Run `__tests__` first."]);
  expect(quoteSelection()).toBe(true);
  expect(quoted).toEqual([["Ship **now**.", "Run `__tests__` first."]]);
});

test("no selection, and a selection outside every such text, quotes nothing", () => {
  const { container } = render(
    <div>
      <p data-testid="outside">Not a thread.</p>
      <Text id="comment" quote={() => {}}>
        <p>A comment.</p>
      </Text>
    </div>
  );
  expect(quotableSelection()).toBeNull();
  selectIn(container, "outside");
  expect(quotableSelection()).toBeNull();
  expect(quoteSelection()).toBe(false);
});

test("a thread that takes no reply now offers none, whatever text stands around it", () => {
  // A decided suggestion's thread, or one whose send is out, inside a page that quotes.
  const outer: (readonly string[])[] = [];
  const { container } = render(
    <Text id="page" quote={(blocks) => outer.push(blocks)}>
      <p>The page's own words.</p>
      <Text id="decided" quote={null}>
        <p>A decided suggestion.</p>
      </Text>
    </Text>
  );
  selectIn(container, "decided");
  expect(quotableSelection()).toBeNull();
  expect(quoteSelection()).toBe(false);
  expect(outer).toEqual([]);
});

test("text inside a document stays out, read or edited", () => {
  const { container } = render(
    <Text id="pane" quote={() => {}}>
      <div className="ProseMirror">
        <p data-testid="read">A document's paragraph.</p>
      </div>
      <div contentEditable suppressContentEditableWarning>
        <p data-testid="edited">A document being edited.</p>
      </div>
    </Text>
  );
  selectIn(container, "read");
  expect(quotableSelection()).toBeNull();
  selectIn(container, "edited");
  expect(quotableSelection()).toBeNull();
});

test("a thread drawn in a document's own column still quotes its words", () => {
  // The slot under the block a comment is anchored to: the thread's words are its own.
  const { container } = render(
    <div className="ProseMirror">
      <p>The document's paragraph.</p>
      <Text id="thread" quote={() => {}}>
        <p>A comment on it.</p>
      </Text>
    </div>
  );
  selectIn(container, "thread");
  expect(quotableSelection()?.blocks).toEqual(["A comment on it."]);
});

test("while a field has the focus, the words selected are the reader's own", () => {
  const { container } = render(
    <div>
      <Text id="comment" quote={() => {}}>
        <p>A comment.</p>
      </Text>
      <textarea data-testid="box" />
      <input data-testid="option" type="radio" />
    </div>
  );
  selectIn(container, "comment");
  container.querySelector<HTMLTextAreaElement>('[data-testid="box"]')?.focus();
  expect(quotableSelection()).toBeNull();
  // Ticking an option takes no typed text, so the selection is still the reader's to quote.
  container.querySelector<HTMLInputElement>('[data-testid="option"]')?.focus();
  selectIn(container, "comment");
  expect(quotableSelection()).not.toBeNull();
});
