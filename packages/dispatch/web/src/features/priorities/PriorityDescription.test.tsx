import { afterEach, beforeEach, expect, jest, mock, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { displayTree } from "../../__tests__/display-trees";
import { jump, onFakeClock } from "../../__tests__/fake-clock";
import { recordedEditors, selectText, writeAtEnd } from "../../__tests__/recorded-editors";
import type { DisplayTree } from "../../api/types";
import type { CreateEditor } from "../doc/editor";
import { DocumentRuntime } from "../doc/runtime";
import { loadedEngine, warmMarkdownRenderer } from "../refs/markdown-engine";
import {
  flushTasks,
  installFakeParseWorker,
  restoreParseWorker,
} from "../refs/parse-worker-fixture";
import { SAVE_AFTER_MS } from "./InPlaceField";
import { PriorityDescription } from "./PriorityDescription";

const OUTLINE = "# Why\n\n- Every machine runs the daemon\n- Nothing waits on the old build";
/** `OUTLINE` as the editor writes it back: its own bullet, and a closing newline. */
const OUTLINE_READ_BACK =
  "# Why\n\n* Every machine runs the daemon\n* Nothing waits on the old build\n";

const unmounts: (() => void)[] = [];
afterEach(() => {
  for (const unmount of unmounts.splice(0)) unmount();
});

/**
 * The description as a page holds it, in the documents' own editor: `description` is the text
 * the server answered with and `tree` the reading the server sent of it, which `rerender` moves,
 * and every write lands in `onSave`. `imports` is the length of each text the editor took in as
 * markdown (`setMarkdown`), its own parse of a text, which the browser spec counts as a parse of
 * the page (`e2e/parse-counting.ts`). Queries are scoped to this render's container, since bun
 * runs every test file in one document.
 */
function described(description: string, canWrite = true, tree?: DisplayTree) {
  const editors = recordedEditors();
  const imports: number[] = [];
  const createEditor: CreateEditor = async (root, options) => {
    const handle = await editors.runtime.createEditor(root, options);
    const setMarkdown = handle.setMarkdown;
    handle.setMarkdown = (markdown) => {
      imports.push(markdown.length);
      setMarkdown.call(handle, markdown);
    };
    return handle;
  };
  const runtime = { ...editors.runtime, createEditor };
  const onSave = mock((_next: string) => {});
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["whoami"], { event_head: "0", kind: "user", login: "alice" });
  const page = (text: string, writer: boolean, reading: DisplayTree | undefined): ReactNode => (
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <DocumentRuntime.Provider value={runtime}>
          <PriorityDescription
            canWrite={writer}
            description={text}
            descriptionTree={reading}
            label="Description of P1"
            onSave={onSave}
          />
        </DocumentRuntime.Provider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  const view = render(page(description, canWrite, tree));
  unmounts.push(() => {
    view.unmount();
    client.clear();
  });
  return {
    /** The editor once it holds the description, and its handle. */
    editor: () => editors.named("Description of P1", view.container),
    imports,
    onSave,
    rerender: (text: string, writer = canWrite, reading?: DisplayTree) =>
      view.rerender(page(text, writer, reading)),
    scope: within(view.container),
    view,
  };
}

test("the description is the documents' editor, named for it, drawn as the markdown it is", async () => {
  const { editor } = described(`${OUTLINE}\n\nRead [the runbook](https://example.com/rb).`);
  const { box } = await editor();
  // The editor Dispatch's documents are written in, not a field of the page's own.
  expect(box.classList.contains("ProseMirror")).toBe(true);
  expect(box.getAttribute("contenteditable")).toBe("true");
  expect(box.getAttribute("aria-multiline")).toBe("true");
  expect(box.getAttribute("aria-readonly")).toBeNull();
  expect(within(box).getByRole("heading", { name: "Why" })).toBeDefined();
  expect(
    within(box)
      .getAllByRole("listitem")
      .map((item) => item.textContent)
  ).toEqual(["Every machine runs the daemon", "Nothing waits on the old build"]);
  const link = within(box).getByRole("link", { name: "the runbook" });
  expect(link.getAttribute("href")).toBe("https://example.com/rb");
});

test("a selection in it offers no comment, suggestion or ask, and writes no mark", async () => {
  const { editor, onSave, view } = described(OUTLINE);
  const { handle } = await editor();
  handle.view.focus();
  selectText(handle, "Every machine");
  expect(handle.view.state.selection.empty).toBe(false);
  expect(document.querySelector(".dispatch-action-bar")).toBeNull();
  expect(document.querySelector(".mark-popover")).toBeNull();
  for (const name of ["Comment", "Suggest", "Ask"]) {
    expect(within(view.container).queryByRole("button", { name })).toBeNull();
  }
  // Dispatch's Ctrl+Alt+M asks the editor for the same Comment, which it refuses.
  expect(handle.commentOnSelection()).toBe(false);
  act(() => handle.view.dom.blur());
  expect(onSave).not.toHaveBeenCalled();
});

test("an edit is written as markdown once the typing rests, once, with no Save button", async () => {
  const { editor, onSave, view } = described(OUTLINE);
  const { handle } = await editor();
  handle.view.focus();
  await onFakeClock(async () => {
    writeAtEnd(handle, " and is gone", "gone");
    expect(onSave).not.toHaveBeenCalled();
    // One move each, rather than `jump`: Bun's `advanceTimersByTime(0)`, the second round
    // `jump` runs, fires a timer still a millisecond short of its time.
    act(() => jest.advanceTimersByTime(SAVE_AFTER_MS - 1));
    expect(onSave).not.toHaveBeenCalled();
    act(() => jest.advanceTimersByTime(1));
  });
  expect(onSave.mock.calls).toEqual([
    [
      "# Why\n\n* Every machine runs the daemon\n* Nothing waits on the old build and is **gone**\n",
    ],
  ]);
  expect(within(view.container).queryByRole("button", { name: /save/i })).toBeNull();
  // Leaving it writes nothing more: the text was written already.
  act(() => handle.view.dom.blur());
  expect(onSave).toHaveBeenCalledTimes(1);
});

test("leaving the editor writes what it holds at once", async () => {
  const { editor, onSave } = described("Speedruns run again.");
  const { handle } = await editor();
  handle.view.focus();
  writeAtEnd(handle, " More.");
  act(() => handle.view.dom.blur());
  expect(onSave.mock.calls).toEqual([["Speedruns run again. More.\n"]]);
});

test("the editor's own reading of the text is no edit, and writes nothing", async () => {
  const { editor, onSave } = described(OUTLINE);
  const { handle } = await editor();
  expect(handle.getMarkdown()).toBe(OUTLINE_READ_BACK);
  handle.view.focus();
  act(() => handle.view.dom.blur());
  await onFakeClock(() => jump(SAVE_AFTER_MS));
  expect(onSave).not.toHaveBeenCalled();
});

test("closing the editor while a write waits for the typing to rest writes it", async () => {
  const { editor, onSave, view } = described("Speedruns run again.");
  const { handle } = await editor();
  writeAtEnd(handle, " More.");
  view.unmount();
  expect(onSave.mock.calls).toEqual([["Speedruns run again. More.\n"]]);
});

test("the server's text replaces the editor's; under the reader's unwritten change it is shown, and theirs kept beside it until they use it", async () => {
  const { editor, onSave, rerender, scope } = described("Speedruns run again.");
  const { box, handle } = await editor();
  rerender("Written elsewhere.");
  await waitFor(() => expect(box.textContent).toBe("Written elsewhere."));

  handle.view.focus();
  writeAtEnd(handle, " Mine.");
  rerender("Written elsewhere, again.");
  // The editor shows what the server holds, and the reader's text stands beside it.
  expect(box.textContent).toBe("Written elsewhere, again.");
  const notice = scope.getByRole("alert", { name: "Description of P1, changed by another person" });
  expect(notice.textContent).toContain("Another person changed this while you wrote. Your text:");
  expect(within(notice).getByText("Written elsewhere. Mine.")).toBeDefined();
  // Leaving writes nothing: the editor holds the server's text.
  act(() => handle.view.dom.blur());
  expect(onSave).not.toHaveBeenCalled();
  // Using the reader's text puts it back and writes it at once.
  await userEvent.setup().click(within(notice).getByRole("button", { name: "Use my text" }));
  expect(box.textContent).toBe("Written elsewhere. Mine.");
  expect(onSave.mock.calls).toEqual([["Written elsewhere. Mine.\n"]]);
  expect(scope.queryByRole("alert", { name: /changed by another person/ })).toBeNull();
  // The round trip of that write answers with what the editor wrote, which it holds already.
  rerender("Written elsewhere. Mine.\n");
  expect(box.textContent).toBe("Written elsewhere. Mine.");
  expect(onSave).toHaveBeenCalledTimes(1);
});

test("leaving the editor with nothing written takes in the text written elsewhere while the reader was in it", async () => {
  const { editor, onSave, rerender } = described("Speedruns run again.");
  const { box, handle } = await editor();
  handle.view.focus();
  // Another person's write lands while the reader is in the editor; they type nothing, and leave.
  rerender("Written elsewhere.");
  act(() => handle.view.dom.blur());
  expect(onSave).not.toHaveBeenCalled();
  // Nothing of theirs stands now, so the server's text is what the editor shows. Left showing
  // the old text, the reader's next edit writes it back over the other person's, unseen.
  await waitFor(() => expect(box.textContent).toBe("Written elsewhere."));
  handle.view.focus();
  writeAtEnd(handle, " Mine.");
  act(() => handle.view.dom.blur());
  expect(onSave.mock.calls).toEqual([["Written elsewhere. Mine.\n"]]);
});

test("keeping the other person's text lets the reader's go, and writes nothing", async () => {
  const { editor, onSave, rerender, scope } = described("Speedruns run again.");
  const { box, handle } = await editor();
  handle.view.focus();
  writeAtEnd(handle, " Mine.");
  rerender("Written elsewhere.");
  const notice = scope.getByRole("alert", { name: "Description of P1, changed by another person" });
  await userEvent.setup().click(within(notice).getByRole("button", { name: "Keep their text" }));
  expect(scope.queryByRole("alert", { name: /changed by another person/ })).toBeNull();
  expect(box.textContent).toBe("Written elsewhere.");
  // Clicking the button left the editor, and the editor holds the server's text: nothing goes.
  expect(onSave).not.toHaveBeenCalled();
});

test("the page going while a write waits for the typing to rest writes it, once", async () => {
  const { editor, onSave } = described("Speedruns run again.");
  const { handle } = await editor();
  handle.view.focus();
  await onFakeClock(async () => {
    writeAtEnd(handle, " More.");
    expect(onSave).not.toHaveBeenCalled();
    act(() => {
      window.dispatchEvent(new Event("pagehide"));
    });
    expect(onSave.mock.calls).toEqual([["Speedruns run again. More.\n"]]);
    // That was the write waiting for the typing to rest: none goes once the typing has rested.
    await jump(SAVE_AFTER_MS);
  });
  expect(onSave).toHaveBeenCalledTimes(1);
});

test("the keyboard reaches the editor as it reaches any field", async () => {
  const { editor, view } = described(OUTLINE);
  const { box } = await editor();
  const before = view.container.ownerDocument.createElement("button");
  view.container.prepend(before);
  before.focus();
  await userEvent.setup().tab();
  expect(document.activeElement).toBe(box);
});

test("a reader who may not write reads it in the same editor, read-only", async () => {
  const { editor, onSave } = described(OUTLINE, false);
  const { box, handle } = await editor();
  expect(box.classList.contains("ProseMirror")).toBe(true);
  expect(box.getAttribute("contenteditable")).toBe("false");
  expect(box.getAttribute("aria-readonly")).toBe("true");
  expect(within(box).getByRole("heading", { name: "Why" })).toBeDefined();
  expect(handle.view.editable).toBe(false);
  expect(onSave).not.toHaveBeenCalled();
});

test("an empty description says none is written: above the empty editor for a writer, alone for a reader", async () => {
  const writer = described("");
  const { box } = await writer.editor();
  expect(writer.scope.getByText("No description is written. Write one below.")).toBeDefined();
  expect(box.textContent).toBe("");
  writer.view.unmount();

  const reader = described("", false);
  expect(await reader.scope.findByText("No description is written.")).toBeDefined();
  expect(reader.scope.queryByRole("textbox")).toBeNull();
});

test("the server's text in the editor's own reading of the text the edit started from is no change, and leaves the reader's typing", async () => {
  const { editor, onSave, rerender, scope } = described(OUTLINE);
  const { box, handle } = await editor();
  handle.view.focus();
  writeAtEnd(handle, " Mine.");
  // Another writer's editor wrote back the text it read, in the editor's own form: no change of
  // the text, so nothing of the reader's is taken from the editor and kept beside it.
  rerender(OUTLINE_READ_BACK);
  expect(scope.queryByRole("alert", { name: /changed by another person/ })).toBeNull();
  expect(box.textContent).toContain("old build Mine.");
  act(() => handle.view.dom.blur());
  expect(onSave.mock.calls).toEqual([
    ["# Why\n\n* Every machine runs the daemon\n* Nothing waits on the old build Mine.\n"],
  ]);
});

/** Emphasis nested 4,000 deep, 24,001 characters: the shape micromark spends 672,095,992
 *  `attention` steps on, about 55 s (cybermonklabs/legion#4437). */
const NESTED = `${"*a ".repeat(4_000)}b${" c*".repeat(4_000)}`;

/** The server's reading of `NESTED`, as Go's display read writes it for marks nested past its
 *  bound (pmdoc's `maxInlineNesting`, `literalDocument`): one paragraph of the text's own
 *  characters, no mark among them. It is written out here because the page's own engine, which
 *  `displayTree` reads with, is the parser this test keeps the page from running. */
const NESTED_TREE: DisplayTree = {
  content: [{ content: [{ text: NESTED, type: "text" }], type: "paragraph" }],
  type: "doc",
};

beforeEach(async () => {
  await warmMarkdownRenderer();
});

afterEach(restoreParseWorker);

/** Counts every call of the shared read-only engine's `parse`, the one entry a display surface
 *  parses markdown on the page through (`e2e/parse-counting.ts`), until the returned restore:
 *  the length of each text parsed, so a failure names how much and prints none of it. */
function countPageParses(): { parsed: number[]; restore: () => void } {
  const engine = loadedEngine();
  if (engine === undefined) throw new Error("the row needs the shared engine loaded");
  const counted = engine as { parse: typeof engine.parse };
  const parse = counted.parse;
  const parsed: number[] = [];
  counted.parse = (markdown, softBreaks) => {
    parsed.push(markdown.length);
    return parse.call(engine, markdown, softBreaks);
  };
  return {
    parsed,
    restore: () => {
      counted.parse = parse;
    },
  };
}

test.each([
  ["a reader who may not write it, read-only", false],
  ["a person who may write it, in the editor they write in", true],
] as const)("a stored description is drawn from the server's tree for %s, and nothing on the page or in the parse worker parses it", async (_reader, canWrite) => {
  const worker = installFakeParseWorker();
  const { parsed, restore } = countPageParses();
  try {
    const { editor, imports, onSave } = described(NESTED, canWrite, NESTED_TREE);
    // The editor builds the tree it is given with no parser of its own, for a reader who may write
    // it as for one who may not: its markdown taken in instead is the editor's parse.
    const { box, handle } = await editor();
    expect(box.getAttribute("contenteditable")).toBe(String(canWrite));
    expect(box.textContent).toBe(NESTED);
    expect(box.querySelector("em, strong")).toBeNull();
    await act(() => flushTasks());
    expect(worker.requests.map((request) => request.markdown.length)).toEqual([]);
    expect(parsed).toEqual([]);
    expect(imports).toEqual([]);
    // The text it took in is the server's, and no edit of the reader's: leaving writes nothing.
    act(() => handle.view.dom.blur());
    expect(onSave).not.toHaveBeenCalled();
  } finally {
    restore();
  }
});

test("another person's text arrives in a writer's editor from the server's tree, and nothing on the page parses it", async () => {
  const stored = "Speedruns run again.";
  const { editor, imports, onSave, rerender } = described(stored, true, displayTree(stored));
  const { box, handle } = await editor();
  const { parsed, restore } = countPageParses();
  try {
    // Another person writes the nested text, and the server answers with its reading of it.
    rerender(NESTED, true, NESTED_TREE);
    await waitFor(() => expect(box.textContent).toBe(NESTED));
    expect(box.querySelector("em, strong")).toBeNull();
    expect(parsed).toEqual([]);
    expect(imports).toEqual([]);
    // What the editor took in is the server's text, and no edit of the reader's: leaving the
    // editor writes nothing.
    act(() => handle.view.dom.blur());
    expect(onSave).not.toHaveBeenCalled();
  } finally {
    restore();
  }
});

test("a description the reader has just written is drawn from its own text, never from the tree of the stored one", async () => {
  const stored = "The **old** plan.";
  const { editor, onSave } = described(stored, true, displayTree(stored));
  const { box, handle } = await editor();
  expect(within(box).getByText("old").tagName).toBe("STRONG");
  // The reader writes "The *new* plan.": "new" over "old", in the editor's own emphasis, through
  // the editor's own transaction, since happy-dom delivers no keystroke to ProseMirror.
  handle.view.focus();
  selectText(handle, "old");
  const { view } = handle;
  const emphasis = view.state.schema.marks.emphasis;
  if (emphasis === undefined) throw new Error("the editor has no emphasis mark");
  view.dispatch(
    view.state.tr.replaceSelectionWith(view.state.schema.text("new", [emphasis.create()]), false)
  );
  act(() => handle.view.dom.blur());
  expect(onSave.mock.calls).toEqual([["The *new* plan.\n"]]);
  // The server still answers the old text and its tree, and the editor shows what was written.
  expect(within(box).getByText("new").tagName).toBe("EM");
  expect(within(box).queryByText("old")).toBeNull();
});
