import { expect, spyOn, test } from "bun:test";
import { Plugin, TextSelection } from "@milkdown/kit/prose/state";
import type { EditorView } from "@milkdown/kit/prose/view";
import type {
  CreateProofEditorOptions,
  OpenLink,
  ProofEditorAccess,
  ReferenceCandidate,
} from "../src/lib";
import { serverBlockSchema } from "./block-schema-fixture";
import { withMarksEditor } from "./marks-editor";

/** A paragraph holding a link to another site and a `dispatch://` link. It opens at 0, so its
 *  text starts at 1: "the plan" is 6 to 14 and "the other issue" 19 to 34. */
const LINKS =
  "Read [the plan](https://example.com/a) and [the other issue](dispatch://CORE-2) today.";

/** The host's link opener, recording each target it is handed and whether for a new tab. */
function recordedOpener(): {
  readonly openLink: OpenLink;
  readonly opened: Array<[string, boolean]>;
} {
  const opened: Array<[string, boolean]> = [];
  return { openLink: (target, { newTab }) => void opened.push([target, newTab]), opened };
}

/** The link of the document whose words are `text`. */
function linkOf(view: EditorView, text: string): Element {
  const link = Array.from(view.dom.querySelectorAll("a")).find(
    (anchor) => anchor.textContent === text
  );
  if (link === undefined) throw new Error(`no link reads "${text}"`);
  return link;
}

/** Dispatches a mouse event of `type` on `target`, as the browser does: ProseMirror's own
 *  listener hands it to the editor's handlers. Only for a press the editor prevents, or one that
 *  is no click: happy-dom follows an unprevented click on a link by navigating its window. */
function dispatch(target: Element, type: string, init: MouseEventInit = {}): MouseEvent {
  const event = new window.MouseEvent(type, {
    bubbles: true,
    button: 0,
    cancelable: true,
    ...init,
  });
  target.dispatchEvent(event);
  return event;
}

/** Offers a mouse event of `type`, aimed at `target`, to the editor's DOM event handlers as
 *  ProseMirror's own listener does, with no default action to follow, and answers whether one
 *  handled it: for a click the editor leaves to the browser. */
function offer(
  view: EditorView,
  target: Element,
  type: "auxclick" | "click" | "mousedown" | "mouseup",
  init: MouseEventInit = {}
): { readonly event: MouseEvent; readonly handled: boolean } {
  const event = new window.MouseEvent(type, {
    bubbles: true,
    button: 0,
    cancelable: true,
    ...init,
  });
  Object.defineProperty(event, "target", { value: target });
  const handled = view.someProp("handleDOMEvents", (handlers) =>
    (handlers[type] as ((view: EditorView, event: Event) => boolean | undefined) | undefined)?.(
      view,
      event
    )
  );
  return { event, handled: handled === true };
}

function caretAt(view: EditorView, pos: number): void {
  view.dispatch(view.state.tr.setSelection(TextSelection.create(view.state.doc, pos)));
}

/** Offers Alt+Enter (Option+Enter) to the editor's keydown handlers, as ProseMirror's own
 *  listener does before its keymaps, and answers whether one took it. Upstream's task-checkbox
 *  handler reads the page's `KeyboardEvent`, which a browser defines and the test window does
 *  not install, so it is installed for the call. */
function altEnter(view: EditorView): { readonly event: KeyboardEvent; readonly handled: boolean } {
  const event = new window.KeyboardEvent("keydown", {
    altKey: true,
    bubbles: true,
    cancelable: true,
    key: "Enter",
  });
  const previous = Object.getOwnPropertyDescriptor(globalThis, "KeyboardEvent");
  Object.defineProperty(globalThis, "KeyboardEvent", {
    configurable: true,
    value: window.KeyboardEvent,
    writable: true,
  });
  try {
    const handled = view.someProp("handleDOMEvents", (handlers) => handlers.keydown?.(view, event));
    return { event, handled: handled === true };
  } finally {
    if (previous === undefined) Reflect.deleteProperty(globalThis, "KeyboardEvent");
    else Object.defineProperty(globalThis, "KeyboardEvent", previous);
  }
}

test("a plain click opens a link in every access mode, and Ctrl, Cmd and a middle click open it in a new tab", async () => {
  for (const access of ["edit", "comment", "read"] as const satisfies ProofEditorAccess[]) {
    const { openLink, opened } = recordedOpener();
    await withMarksEditor(
      LINKS,
      ({ view }) => {
        const away = linkOf(view, "the plan");
        const issue = linkOf(view, "the other issue");
        const page = window.location.href;

        expect({ access, prevented: dispatch(away, "click").defaultPrevented }).toEqual({
          access,
          prevented: true,
        });
        dispatch(issue, "click");
        dispatch(issue, "click", { ctrlKey: true });
        dispatch(away, "click", { metaKey: true });
        expect(dispatch(issue, "auxclick", { button: 1 }).defaultPrevented).toBe(true);
        // A Shift or Alt click, and a right button's, are the browser's own.
        expect(offer(view, away, "click", { shiftKey: true }).handled).toBe(false);
        expect(offer(view, away, "click", { altKey: true }).handled).toBe(false);
        expect(offer(view, away, "auxclick", { button: 2 }).handled).toBe(false);

        expect({ access, opened }).toEqual({
          access,
          opened: [
            ["https://example.com/a", false],
            ["dispatch://CORE-2", false],
            ["dispatch://CORE-2", true],
            ["https://example.com/a", true],
            ["dispatch://CORE-2", true],
          ],
        });
        // The host opened each one; the browser followed none.
        expect(window.location.href).toBe(page);
      },
      { access, openLink }
    );
  }
});

test("a middle button's press and release on a link are prevented, so the browser pastes nothing", async () => {
  const { openLink } = recordedOpener();
  await withMarksEditor(
    LINKS,
    ({ handle, view }) => {
      const stored = handle.getMarkdown();
      for (const text of ["the plan", "the other issue"]) {
        const link = linkOf(view, text);
        expect({
          text,
          press: dispatch(link, "mousedown", { button: 1 }).defaultPrevented,
        }).toEqual({ press: true, text });
        expect({
          text,
          release: dispatch(link, "mouseup", { button: 1 }).defaultPrevented,
        }).toEqual({ release: true, text });
      }
      // Away from a link a middle press is the editor's as before, and a primary one with no
      // modifier is always.
      const paragraph = view.dom.querySelector("p");
      if (paragraph === null) throw new Error("no paragraph");
      expect(offer(view, paragraph, "mouseup", { button: 1 }).handled).toBe(false);
      expect(offer(view, linkOf(view, "the plan"), "mousedown").handled).toBe(false);
      expect(handle.getMarkdown()).toBe(stored);
    },
    { openLink }
  );
});

test("a click on a link inside commented text opens the link and names no comment", async () => {
  const { openLink, opened } = recordedOpener();
  const clickedMarks: string[] = [];
  await withMarksEditor(
    LINKS,
    ({ view }) => {
      const { schema } = view.state;
      const comment = schema.marks.proofComment?.create({ by: "user:alice", id: "comment-1" });
      if (comment === undefined) throw new Error("no comment mark");
      view.dispatch(view.state.tr.addMark(1, view.state.doc.content.size - 1, comment));
      const away = linkOf(view, "the plan");
      const besideLink = Array.from(view.dom.querySelectorAll('span[data-proof="comment"]')).find(
        (span) => span.closest("a") === null && span.querySelector("a") === null
      );
      if (besideLink === undefined) throw new Error("no commented text outside a link");
      // The release ProseMirror reads as a click, which the margin hears through `handleClick`.
      const release = (target: Element) => {
        const event = new window.MouseEvent("mouseup", { bubbles: true, button: 0 });
        Object.defineProperty(event, "target", { value: target });
        view.someProp("handleClick", (handleClick) => handleClick(view, 8, event));
      };

      release(away);
      expect(clickedMarks).toEqual([]);
      expect(dispatch(away, "click").defaultPrevented).toBe(true);
      expect(opened).toEqual([["https://example.com/a", false]]);

      // The comment stays reachable from the commented words beside the link.
      release(besideLink);
      expect(clickedMarks).toEqual(["comment-1"]);
    },
    { onMarkClick: (markId) => void clickedMarks.push(markId), openLink }
  );
});

/** Where the pointer is for a press, a move, a release or a click, in CSS pixels from the
 *  window's corner: happy-dom lays nothing out, so a row names the place itself. */
function pointer(clientX: number, clientY = 10): MouseEventInit {
  return { clientX, clientY };
}

/** The pointer at `clientX`, `clientY` with the primary button held, as a drag moves it. */
function held(clientX: number, clientY = 10): MouseEventInit {
  return { ...pointer(clientX, clientY), buttons: 1 };
}

/** A plain click on `link` as a browser sends it, the pointer at `point` from the press to the
 *  release: the press and the release, which ProseMirror hears too, then the click, offered to
 *  the editor alone (`offer`), so that one the editor leaves to the browser follows nothing.
 *  Answers whether the editor took the click. */
function plainClick(view: EditorView, link: Element, point: MouseEventInit): boolean {
  dispatch(link, "mousedown", point);
  dispatch(link, "mouseup", point);
  return offer(view, link, "click", point).handled;
}

/** `node`, which a row expects to be a text node of the paragraph. */
function textOf(node: Node | null): Node {
  if (node === null || node.nodeType !== 3) throw new Error("no text node there");
  return node;
}

/** Selects the page's text from `start` in `from` to `end` in `to`, as a drag over it leaves
 *  it, or the reader's selection the selection bar's Comment starts from. */
function selectText(view: EditorView, from: Node, start: number, to: Node, end: number): Selection {
  const selection = view.dom.ownerDocument.getSelection();
  if (selection === null) throw new Error("the page has no selection");
  const range = view.dom.ownerDocument.createRange();
  range.setStart(from, start);
  range.setEnd(to, end);
  selection.removeAllRanges();
  selection.addRange(range);
  return selection;
}

/** Puts the editable editor in a state with `make`, then makes one plain click on "the other
 *  issue" (`plainClick`), and answers whether the editor took that click and every target the
 *  host was handed to open, `make`'s own included. */
async function plainClickAfter(
  make: (view: EditorView, issue: Element) => void
): Promise<{ readonly handled: boolean; readonly opened: Array<[string, boolean]> }> {
  const { openLink, opened } = recordedOpener();
  let handled = false;
  await withMarksEditor(
    LINKS,
    ({ view }) => {
      const issue = linkOf(view, "the other issue");
      make(view, issue);
      handled = plainClick(view, issue, pointer(60));
    },
    { openLink }
  );
  return { handled, opened };
}

// dispatch://INFRA-2160, R1: a plain click opens a link, each time. A click is a primary press
// and its release on one link with the pointer moved no more than 4 px between them, whatever the
// page held before the press; a drag is a move between them. With a sentence holding a link
// selected, the editor read the selection as a drag's end, so one plain click on the link opened
// nothing and only cleared the selection, and a second click opened it.
test("a plain click on a link opens it when a selection holds the link", async () => {
  const clicked = await plainClickAfter((view, issue) => {
    // "and the other issue today", as the reader selects a sentence to comment on it.
    const selection = selectText(
      view,
      textOf(issue.previousSibling),
      1,
      textOf(issue.nextSibling),
      6
    );
    expect(selection.toString()).toBe("and the other issue today");
  });

  expect(clicked).toEqual({ handled: true, opened: [["dispatch://CORE-2", false]] });
});

test("a plain click on a link opens it when a selection elsewhere in the document stands", async () => {
  const clicked = await plainClickAfter((view, issue) => {
    // "today", after the link.
    const after = textOf(issue.nextSibling);
    const selection = selectText(view, after, 1, after, 6);
    expect(selection.toString()).toBe("today");
  });

  expect(clicked).toEqual({ handled: true, opened: [["dispatch://CORE-2", false]] });
});

test("a plain click on a link opens it when the caret is inside the link", async () => {
  const clicked = await plainClickAfter((view, issue) => {
    const selection = view.dom.ownerDocument.getSelection();
    if (selection === null) throw new Error("the page has no selection");
    selection.collapse(textOf(issue.firstChild), 4);
    expect(selection.isCollapsed).toBe(true);
  });

  expect(clicked).toEqual({ handled: true, opened: [["dispatch://CORE-2", false]] });
});

for (const [key, modifier] of [
  ["Ctrl", { ctrlKey: true }],
  ["Cmd", { metaKey: true }],
] as const) {
  test(`a plain click on a link opens it after a ${key} click on the link`, async () => {
    const clicked = await plainClickAfter((view, issue) => {
      // The modified click as a browser sends it, where the plain click lands after it.
      const point = { ...pointer(60), ...modifier };
      dispatch(issue, "mousedown", point);
      dispatch(issue, "mouseup", point);
      offer(view, issue, "click", point);
    });

    expect(clicked).toEqual({
      handled: true,
      opened: [
        ["dispatch://CORE-2", true],
        ["dispatch://CORE-2", false],
      ],
    });
  });
}

test("a plain click on a link opens it after a middle click on the link", async () => {
  const clicked = await plainClickAfter((view, issue) => {
    const point = { ...pointer(60), button: 1 };
    dispatch(issue, "mousedown", point);
    dispatch(issue, "mouseup", point);
    offer(view, issue, "auxclick", point);
  });

  expect(clicked).toEqual({
    handled: true,
    opened: [
      ["dispatch://CORE-2", true],
      ["dispatch://CORE-2", false],
    ],
  });
});

// dispatch://INFRA-2160, R1: a drag on a link selects its words and opens nothing. What the rule
// reads is the press on the link and how far the pointer moved from it before the click: more
// than 4 px along either axis at any moment between the press and the release, as ProseMirror
// reads a drag.
test("a drag that starts on a link and ends on it opens nothing, and a click whose pointer moved 4 px opens it", async () => {
  const { openLink, opened } = recordedOpener();
  await withMarksEditor(
    LINKS,
    ({ view }) => {
      const issue = linkOf(view, "the other issue");
      const words = textOf(issue.firstChild);

      // The drag over "other": the press, the pointer 5 px on, one past the threshold, the
      // words it went over selected, and the release and the click there.
      dispatch(issue, "mousedown", pointer(60));
      dispatch(issue, "mousemove", held(65));
      selectText(view, words, 4, words, 9);
      dispatch(issue, "mouseup", pointer(65));
      const dragged = offer(view, issue, "click", pointer(65)).handled;
      // A click no press came before, as a script or assistive technology makes, opens the link:
      // the drag's click forgot its press.
      const unpressed = offer(view, issue, "click", pointer(0, 0)).handled;

      // A drag along the link and back: the release is 1 px from the press, and the pointer went
      // 20 px away between them. The page's selection is back at the press.
      view.dom.ownerDocument.getSelection()?.collapse(words, 4);
      dispatch(issue, "mousedown", pointer(60));
      dispatch(issue, "mousemove", held(80));
      dispatch(issue, "mousemove", held(61));
      dispatch(issue, "mouseup", pointer(61));
      const returned = offer(view, issue, "click", pointer(61)).handled;

      // A press the browser turns into a drag of its own, for which Chromium sends neither a
      // release nor a click: once the pointer moves with no button held, the press is over, and a
      // click no press came before opens the link.
      dispatch(issue, "mousedown", pointer(60));
      dispatch(issue, "mousemove", held(70));
      dispatch(issue, "mousemove", pointer(60));
      const afterNativeDrag = offer(view, issue, "click", pointer(0, 0)).handled;

      // A hand that trembles 4 px along each axis between the press and the release still clicks.
      dispatch(issue, "mousedown", pointer(60, 10));
      dispatch(issue, "mousemove", held(64, 14));
      dispatch(issue, "mouseup", pointer(64, 14));
      const trembled = offer(view, issue, "click", pointer(64, 14)).handled;

      expect({ afterNativeDrag, dragged, opened, returned, trembled, unpressed }).toEqual({
        afterNativeDrag: true,
        dragged: false,
        opened: [
          ["dispatch://CORE-2", false],
          ["dispatch://CORE-2", false],
          ["dispatch://CORE-2", false],
        ],
        returned: false,
        trembled: true,
        unpressed: true,
      });
    },
    { openLink }
  );
});

// dispatch://INFRA-2160, R1: the editor hears the pointer of a press on a link where ProseMirror
// hears its own press's, on the page the editor is in, until the release: a drag that leaves the
// editor and comes back to the press is a drag, as ProseMirror reads it.
test("a drag that leaves the editor and comes back to the press opens nothing, and the same press and release with no move between them opens the link", async () => {
  const { openLink, opened } = recordedOpener();
  await withMarksEditor(
    LINKS,
    ({ view }) => {
      const issue = linkOf(view, "the other issue");
      const page = view.dom.ownerDocument.body;
      expect(view.dom.contains(page)).toBe(false);

      // The press, the pointer out of the editor 200 px away, back over the link at the press,
      // and the release and the click there.
      dispatch(issue, "mousedown", pointer(60));
      dispatch(page, "mousemove", held(260));
      dispatch(issue, "mousemove", held(60));
      dispatch(issue, "mouseup", pointer(60));
      const left = offer(view, issue, "click", pointer(60)).handled;

      // The same press and release, the pointer staying where it went down.
      const stayed = plainClick(view, issue, pointer(60));

      // A drag released outside the editor: the release ends the press, since no click on the
      // link can follow it, and a click no press came before opens the link.
      dispatch(issue, "mousedown", pointer(60));
      dispatch(page, "mousemove", held(260));
      dispatch(page, "mouseup", pointer(260));
      const releasedOutside = offer(view, issue, "click", pointer(0, 0)).handled;

      expect({ left, opened, releasedOutside, stayed }).toEqual({
        left: false,
        opened: [
          ["dispatch://CORE-2", false],
          ["dispatch://CORE-2", false],
        ],
        releasedOutside: true,
        stayed: true,
      });
    },
    { openLink }
  );
});

/** Reconfigures the editor with one more plugin, as the collab cursor's install
 *  (`installCollabCursorsWhenReady`) and Dispatch's hold on a block's edits (`holdEditsToBlock`)
 *  do while a press can be under way: ProseMirror destroys every plugin view and makes them anew,
 *  and its own press goes on. */
function reconfigure(view: EditorView): void {
  view.updateState(
    view.state.reconfigure({ plugins: [...view.state.plugins, new Plugin({ view: () => ({}) })] })
  );
}

// dispatch://INFRA-2160, R1: the editor's note of a press on a link lasts as long as ProseMirror's
// own press, through a reconfigure of the editor's plugins while the button is held: a drag
// across the reconfigure is a drag, and a click across it a click.
test("a drag on a link across a reconfigure of the editor's plugins opens nothing, and a click across one opens the link", async () => {
  const { openLink, opened } = recordedOpener();
  await withMarksEditor(
    LINKS,
    ({ view }) => {
      const issue = linkOf(view, "the other issue");

      // The press, the plugins reconfigured, the pointer 5 px on, and the release and the click
      // there.
      dispatch(issue, "mousedown", pointer(60));
      reconfigure(view);
      dispatch(issue, "mousemove", held(65));
      dispatch(issue, "mouseup", pointer(65));
      const dragged = offer(view, issue, "click", pointer(65)).handled;

      // The same press and release, the pointer staying where it went down.
      dispatch(issue, "mousedown", pointer(60));
      reconfigure(view);
      dispatch(issue, "mouseup", pointer(60));
      const clicked = offer(view, issue, "click", pointer(60)).handled;

      expect({ clicked, connected: issue.isConnected, dragged, opened }).toEqual({
        clicked: true,
        // The reconfigures drew the link anew neither time.
        connected: true,
        dragged: false,
        opened: [["dispatch://CORE-2", false]],
      });
    },
    { openLink }
  );
});

// dispatch://INFRA-2160, R1: a press on a link stops hearing the page once the view is destroyed
// during it, as ProseMirror's own press does.
test("a view destroyed during a press on a link hears the page's pointer no more", async () => {
  await withMarksEditor(
    LINKS,
    async ({ handle, view }) => {
      const issue = linkOf(view, "the other issue");
      const page = view.dom.ownerDocument;
      const adds = spyOn(page, "addEventListener");
      const removes = spyOn(page, "removeEventListener");
      try {
        dispatch(issue, "mousedown", pointer(60));
        // What the press listens to on the page: the editor's note. happy-dom lays nothing out,
        // so ProseMirror finds the press at no position and makes no press of its own.
        const heard = adds.mock.calls.filter(
          ([type]) => type === "mousemove" || type === "mouseup"
        );
        void handle.destroy();
        await Promise.resolve();

        let thrown: unknown;
        try {
          dispatch(page.body, "mousemove", held(260));
          dispatch(page.body, "mouseup", pointer(260));
        } catch (error) {
          thrown = error;
        }
        const left = heard.filter(
          ([type, listener]) =>
            !removes.mock.calls.some(([gone, removed]) => gone === type && removed === listener)
        );

        expect({
          destroyed: view.isDestroyed,
          heard: heard.map(([type]) => type).sort(),
          left,
          thrown,
        }).toEqual({
          destroyed: true,
          heard: ["mousemove", "mouseup"],
          left: [],
          thrown: undefined,
        });
      } finally {
        adds.mockRestore();
        removes.mockRestore();
      }
    },
    { openLink: recordedOpener().openLink }
  );
});

// dispatch://INFRA-2160, R1: a drag from the words before a link onto it selects them and opens
// nothing. Its press is on the paragraph, not on the link, so no press on the link is noted; its
// release is on the link, and the browser sends the click to the nearest element holding both,
// the paragraph, which is no link.
test("a drag that ends on a link opens nothing, and a Ctrl or a plain click on the link after it opens it", async () => {
  const { openLink, opened } = recordedOpener();
  await withMarksEditor(
    LINKS,
    ({ view }) => {
      const away = linkOf(view, "the plan");
      const paragraph = view.dom.querySelector("p");
      if (paragraph === null || away.parentElement !== paragraph) {
        throw new Error("the link is not drawn straight inside its paragraph");
      }

      // The press on "Read", the pointer across onto "the", the words between selected, and the
      // release on the link.
      dispatch(paragraph, "mousedown", pointer(2));
      dispatch(paragraph, "mousemove", { ...pointer(30), buttons: 1 });
      const selection = selectText(
        view,
        textOf(away.previousSibling),
        0,
        textOf(away.firstChild),
        3
      );
      dispatch(away, "mouseup", pointer(30));
      const dragged = offer(view, paragraph, "click", pointer(30)).handled;

      // A Ctrl click on the link opens it in a new tab, and a plain click in place, the selection
      // the drag made standing through both: the editor takes the Ctrl click's press, and the
      // plain click's moves no further than its press.
      const ctrl = { ...pointer(30), ctrlKey: true };
      dispatch(away, "mousedown", ctrl);
      dispatch(away, "mouseup", ctrl);
      const newTab = offer(view, away, "click", ctrl).handled;
      const plain = plainClick(view, away, pointer(30));

      expect({ dragged, newTab, plain, selected: selection.toString() }).toEqual({
        dragged: false,
        newTab: true,
        plain: true,
        selected: "Read the",
      });
      expect(opened).toEqual([
        ["https://example.com/a", true],
        ["https://example.com/a", false],
      ]);
    },
    { openLink }
  );
});

// dispatch://INFRA-2160, R1: ProseMirror reads a primary press with Ctrl (Cmd on a Mac) as
// selecting the node it lands in, here the paragraph around the link, which took the place of
// the reader's caret or selection when the Ctrl+click opened the link in a new tab. The editor
// takes that press, so the click opens the link and changes nothing in the document's selection.
test("a Ctrl or Cmd click on a link leaves the selection as it was, and a plain click after it opens the link", async () => {
  for (const modifier of [{ ctrlKey: true }, { metaKey: true }] satisfies MouseEventInit[]) {
    const { openLink, opened } = recordedOpener();
    await withMarksEditor(
      LINKS,
      ({ view }) => {
        const issue = linkOf(view, "the other issue");
        const selection = () => ({
          collapsed: view.dom.ownerDocument.getSelection()?.isCollapsed,
          editor: view.state.selection.toJSON(),
        });
        // happy-dom lays nothing out, so ProseMirror finds a press inside no node, where a Ctrl
        // press selects nothing; a browser finds this one in the link's words, in the paragraph
        // that opens at 0.
        view.posAtCoords = () => ({ inside: 0, pos: 25 });
        view.focus();
        caretAt(view, 3);
        const before = selection();

        // The modified click as a browser sends it: the press, the release, then the click.
        const press = dispatch(issue, "mousedown", modifier);
        dispatch(issue, "mouseup", modifier);
        dispatch(issue, "click", modifier);
        const afterModified = selection();
        // The plain click after it, whose press and release ProseMirror hears as before.
        dispatch(issue, "mousedown");
        dispatch(issue, "mouseup");
        const plainOpens = offer(view, issue, "click").handled;
        // With Shift or Alt held too the click is the browser's own, and its press ProseMirror's.
        const shiftOrAltTaken = [{ shiftKey: true }, { altKey: true }].map(
          (keys) => offer(view, issue, "mousedown", { ...modifier, ...keys }).handled
        );

        expect({
          modifier,
          opened,
          plainOpens,
          prevented: press.defaultPrevented,
          selection: afterModified,
          shiftOrAltTaken,
        }).toEqual({
          modifier,
          opened: [
            ["dispatch://CORE-2", true],
            ["dispatch://CORE-2", false],
          ],
          plainOpens: true,
          prevented: true,
          selection: before,
          shiftOrAltTaken: [false, false],
        });
      },
      { openLink }
    );
  }
});

test("an anchor inside a host island is the host's, and one a node view draws as its own is the editor's", async () => {
  const { openLink, opened } = recordedOpener();
  await withMarksEditor(
    LINKS,
    ({ view }) => {
      const paragraph = view.dom.querySelector("p");
      if (paragraph === null) throw new Error("no paragraph");
      // A slot a host draws into: an element the editor holds no text in, its anchors deeper
      // inside; beside it, a leaf's own root holding its link, as Dispatch's picture fallback is.
      const island = view.dom.ownerDocument.createElement("span");
      island.setAttribute("contenteditable", "false");
      island.innerHTML = '<span><a href="https://example.com/thread">a thread link</a></span>';
      const leaf = view.dom.ownerDocument.createElement("span");
      leaf.setAttribute("contenteditable", "false");
      leaf.innerHTML =
        '<a href="/issues/CORE-1/artifacts/notes-md" data-dispatch-href="dispatch://CORE-1/artifact/notes-md">CORE-1 notes-md</a>';
      paragraph.append(island, leaf);
      try {
        expect(offer(view, linkOf(view, "a thread link"), "click").handled).toBe(false);
        expect(offer(view, linkOf(view, "a thread link"), "auxclick", { button: 1 }).handled).toBe(
          false
        );
        expect(offer(view, linkOf(view, "CORE-1 notes-md"), "click").handled).toBe(true);
        expect(opened).toEqual([["dispatch://CORE-1/artifact/notes-md", false]]);
      } finally {
        island.remove();
        leaf.remove();
      }
    },
    { openLink }
  );
});

/** A plan whose task serves a requirement of another issue's spec. A paragraph comes first, so
 *  the editor's first caret has text to stand in. */
const PLAN_TASK =
  'The plan.\n\n:::plan_task{#t1 serves="dispatch://CORE-1/spec#b-r1"}\nShip the change.\n:::\n';

/** A typed block drawn as Dispatch's `typed-block-drawing.ts` draws one: a header the reader
 *  cannot edit, holding the block's first `serves` pointer as a link in a name/value pair, above
 *  the block's content. */
const drawTypedBlock: NonNullable<CreateProofEditorOptions["renderBlock"]> = (node) => {
  const [reference] = node.attrs.serves as string[];
  return [
    "section",
    { "data-proof-block-type": node.type.name },
    [
      "header",
      { contenteditable: "false", "data-proof-block-summary": "" },
      [
        "dl",
        {},
        ["dt", {}, "serves"],
        [
          "dd",
          {},
          [
            "a",
            { "data-dispatch-href": reference, href: "/issues/CORE-1/spec#b-r1" },
            "CORE-1 spec block",
          ],
        ],
      ],
    ],
    ["div", { "data-proof-block-content": "" }, 0],
  ];
};

// dispatch://INFRA-2160, R1: a plan's or a Verification document's `serves` and `needs` pointers
// sit two elements below a header that is `contenteditable="false"`, and a click on one opens it
// through the host as any link of the document does, with no new load of the host's page.
test("a pointer in a typed block's header opens its reference, and Ctrl and a middle click open it in a new tab", async () => {
  const { openLink, opened } = recordedOpener();
  await withMarksEditor(
    PLAN_TASK,
    ({ view }) => {
      const pointer = view.dom.querySelector(
        "header[contenteditable=false] > dl > dd > a[data-dispatch-href]"
      );
      if (pointer === null) throw new Error("the plan's task drew no pointer in its header");

      expect(offer(view, pointer, "click").handled).toBe(true);
      expect(dispatch(pointer, "click").defaultPrevented).toBe(true);
      dispatch(pointer, "click", { ctrlKey: true });
      expect(dispatch(pointer, "mousedown", { button: 1 }).defaultPrevented).toBe(true);
      expect(dispatch(pointer, "mouseup", { button: 1 }).defaultPrevented).toBe(true);
      expect(dispatch(pointer, "auxclick", { button: 1 }).defaultPrevented).toBe(true);
      expect(opened).toEqual([
        ["dispatch://CORE-1/spec#b-r1", false],
        ["dispatch://CORE-1/spec#b-r1", false],
        ["dispatch://CORE-1/spec#b-r1", true],
        ["dispatch://CORE-1/spec#b-r1", true],
      ]);

      // The same header drawn inside a host island - a typed block of a comment a host draws
      // into a thread's slot - is the host's.
      const content = view.dom.querySelector("[data-proof-block-content] p");
      if (content === null) throw new Error("the plan's task holds no paragraph");
      const island = view.dom.ownerDocument.createElement("span");
      island.setAttribute("contenteditable", "false");
      island.innerHTML =
        '<span><header contenteditable="false" data-proof-block-summary=""><dl><dd><a href="/issues/CORE-1/spec#b-r1" data-dispatch-href="dispatch://CORE-1/spec#b-r1">in a thread</a></dd></dl></header></span>';
      content.append(island);
      try {
        expect(offer(view, linkOf(view, "in a thread"), "click").handled).toBe(false);
        expect(opened).toHaveLength(4);
      } finally {
        island.remove();
      }
    },
    { blockSchema: serverBlockSchema, openLink, renderBlock: drawTypedBlock }
  );
});

test("a click on a drawn title opens its reference, and Ctrl and a middle click open it in a new tab", async () => {
  const { openLink, opened } = recordedOpener();
  await withMarksEditor(
    "See CORE-1 here.",
    ({ handle, view }) => {
      handle.setReferences({
        onKeys: () => {},
        scan: (text): ReferenceCandidate[] => {
          const start = text.indexOf("CORE-1");
          return start === -1 ? [] : [{ end: start + 6, key: "CORE-1", start }];
        },
        targets: new Map([
          [
            "CORE-1",
            { href: "dispatch://CORE-1", path: "/issues/CORE-1", title: "CORE-1 Fix the thing" },
          ],
        ]),
      });
      const title = view.dom.querySelector("a.dispatch-reference");
      if (title === null) throw new Error("no title drawn");

      expect(dispatch(title, "click").defaultPrevented).toBe(true);
      dispatch(title, "click", { ctrlKey: true });
      expect(dispatch(title, "mousedown", { button: 1 }).defaultPrevented).toBe(true);
      expect(dispatch(title, "mouseup", { button: 1 }).defaultPrevented).toBe(true);
      expect(dispatch(title, "auxclick", { button: 1 }).defaultPrevented).toBe(true);

      expect(opened).toEqual([
        ["dispatch://CORE-1", false],
        ["dispatch://CORE-1", true],
        ["dispatch://CORE-1", true],
      ]);
    },
    { openLink }
  );
});

test("Alt+Enter at a link is left to the host's key, which opens it and changes no text", async () => {
  const { openLink, opened } = recordedOpener();
  await withMarksEditor(
    LINKS,
    ({ handle, view }) => {
      const stored = handle.getMarkdown();
      // Inside the link, and at either end of it.
      for (const [pos, target] of [
        [8, "https://example.com/a"],
        [6, "https://example.com/a"],
        [14, "https://example.com/a"],
        [25, "dispatch://CORE-2"],
      ] as const) {
        caretAt(view, pos);
        expect({ pos, target: handle.linkAtCaret() }).toEqual({ pos, target });
      }

      caretAt(view, 8);
      // The host's keymap listens on the window: it hears the press unprevented, and takes it.
      const heard: boolean[] = [];
      const host = (event: Event) => {
        heard.push(event.defaultPrevented);
        event.preventDefault();
      };
      window.addEventListener("keydown", host);
      try {
        const press = altEnter(view);
        expect(press.handled).toBe(true);
        window.dispatchEvent(press.event);
        expect(heard).toEqual([false]);
      } finally {
        window.removeEventListener("keydown", host);
      }
      expect(handle.openLinkAtCaret()).toBe(true);
      expect(opened).toEqual([["https://example.com/a", false]]);

      // Where nothing after the editor takes the press, it is prevented once it has gone by, so
      // the browser writes no new line.
      const untaken = altEnter(view);
      expect(untaken.handled).toBe(true);
      window.dispatchEvent(untaken.event);
      expect(untaken.event.defaultPrevented).toBe(true);

      // Away from every link the press is ProseMirror's own, which writes nothing either.
      caretAt(view, 3);
      expect(handle.linkAtCaret()).toBeNull();
      expect(altEnter(view).handled).toBe(false);
      expect(handle.openLinkAtCaret()).toBe(false);

      expect(opened).toHaveLength(1);
      expect(handle.getMarkdown()).toBe(stored);
    },
    { openLink }
  );
});

test("Alt+Enter beside a bare key opens the reference its title stood for", async () => {
  const { openLink, opened } = recordedOpener();
  await withMarksEditor(
    "See CORE-1 here.",
    ({ handle, view }) => {
      handle.setReferences({
        onKeys: () => {},
        scan: (text): ReferenceCandidate[] => {
          const start = text.indexOf("CORE-1");
          return start === -1 ? [] : [{ end: start + 6, key: "CORE-1", start }];
        },
        targets: new Map([
          [
            "CORE-1",
            { href: "dispatch://CORE-1", path: "/issues/CORE-1", title: "CORE-1 Fix the thing" },
          ],
        ]),
      });
      const stored = handle.getMarkdown();
      // "See CORE-1 here." opens at 0, so CORE-1 is 5 to 11: the caret is right after it.
      caretAt(view, 11);
      expect(handle.linkAtCaret()).toBe("dispatch://CORE-1");
      expect(altEnter(view).handled).toBe(true);
      expect(handle.openLinkAtCaret()).toBe(true);
      expect(opened).toEqual([["dispatch://CORE-1", false]]);
      expect(handle.getMarkdown()).toBe(stored);
    },
    { openLink }
  );
});

// dispatch://INFRA-2160, k-link-keys: an arrow key moves the page's caret at once, and the
// editor's state follows only once the page's `selectionchange` has run, or once ProseMirror's own
// handler of the next key reads the page's selection, after the handlers of `handleDOMEvents`. A
// busy page ran Alt+Enter before that event, the press read the caret where it was before the
// arrow, beside no title, and ProseMirror prevented it, so the host's key never opened the key.
test("Alt+Enter straight after an arrow key reads the caret where the page holds it", async () => {
  const { openLink, opened } = recordedOpener();
  await withMarksEditor(
    "See CORE-1 here.",
    ({ handle, view }) => {
      handle.setReferences({
        onKeys: () => {},
        scan: (text): ReferenceCandidate[] => {
          const start = text.indexOf("CORE-1");
          return start === -1 ? [] : [{ end: start + 6, key: "CORE-1", start }];
        },
        targets: new Map([
          [
            "CORE-1",
            { href: "dispatch://CORE-1", path: "/issues/CORE-1", title: "CORE-1 Fix the thing" },
          ],
        ]),
      });
      const stored = handle.getMarkdown();
      view.focus();
      // "See CORE-1 here." opens at 0, so the space before CORE-1 is 4 to 5: the caret is before
      // it, beside no title.
      caretAt(view, 4);
      expect(handle.linkAtCaret()).toBeNull();

      // The arrow's move, past the space onto the key's start. The page has not run its
      // `selectionchange` yet, so the editor's listener does not hear it.
      const before = view.dom.querySelector("p")?.firstChild;
      expect(before?.textContent).toBe("See ");
      if (before === null || before === undefined) throw new Error("the paragraph holds no text");
      const unheard = (event: Event) => event.stopImmediatePropagation();
      view.dom.ownerDocument.addEventListener("selectionchange", unheard, true);
      try {
        window.getSelection()?.collapse(before, 4);
        expect(view.state.selection.head).toBe(4);

        expect(altEnter(view).handled).toBe(true);
        expect(handle.linkAtCaret()).toBe("dispatch://CORE-1");
        expect(handle.openLinkAtCaret()).toBe(true);
      } finally {
        view.dom.ownerDocument.removeEventListener("selectionchange", unheard, true);
      }
      expect(opened).toEqual([["dispatch://CORE-1", false]]);
      expect(handle.getMarkdown()).toBe(stored);
    },
    { openLink }
  );
});

test("a view the reader cannot edit leaves Alt+Enter alone, and opens links on a click all the same", async () => {
  const { openLink, opened } = recordedOpener();
  await withMarksEditor(
    LINKS,
    ({ view }) => {
      caretAt(view, 8);
      expect(altEnter(view).handled).toBe(false);
      expect(dispatch(linkOf(view, "the plan"), "click").defaultPrevented).toBe(true);
      expect(opened).toEqual([["https://example.com/a", false]]);
    },
    { access: "read", openLink }
  );
});

test("an editor with no link opener opens no link", async () => {
  await withMarksEditor(LINKS, ({ handle, view }) => {
    const away = linkOf(view, "the plan");
    const click = offer(view, away, "click");
    expect(click.handled).toBe(false);
    expect(click.event.defaultPrevented).toBe(false);
    expect(offer(view, away, "auxclick", { button: 1 }).handled).toBe(false);
    caretAt(view, 8);
    expect(altEnter(view).handled).toBe(false);
    expect(handle.openLinkAtCaret()).toBe(false);
  });
});

test("the editor's stylesheet holds no Open link card", async () => {
  const stylesheet = await Bun.file(new URL("../src/lib.css", import.meta.url)).text();

  expect(stylesheet).not.toContain("markdown-link-action-card");
  expect(stylesheet).not.toContain("markdown-link-hover-target");
});
