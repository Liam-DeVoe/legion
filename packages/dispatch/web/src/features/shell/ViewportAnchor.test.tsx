import { afterEach, beforeEach, expect, spyOn, test } from "bun:test";
// biome-ignore lint/style/noRestrictedImports: this file sends the pointer's own clientX/clientY and a pointerout's relatedTarget, neither of which user-event's hover and unhover can set.
import { fireEvent, render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef, Fragment, type ReactNode, type RefObject, useLayoutEffect } from "react";

import { type AnchorRow, ViewportAnchor } from "./ViewportAnchor";

const ROW_HEIGHT = 50;
const LIST_TOP = 100;
const VIEWPORT_HEIGHT = 600;

const anchor = createRef<ViewportAnchor>();
const scrollerRef = createRef<HTMLDivElement>();

/** The row the next commit will anchor to, as `getSnapshotBeforeUpdate` will choose it. */
function chosen(): AnchorRow | null {
  const current = anchor.current?.current() ?? null;
  return current === null ? null : { id: current.id, via: current.via };
}

/** The inline `overflow-anchor` of the box the rows scroll in - the scroller where one is
 *  rendered, else the document's root - as each commit of the list saw it, read by `CommitProbe`. */
const anchoringSeen: string[] = [];

/** The box the rows scroll in now: the scroller where one is rendered, else the document's root,
 *  whose `overflow-anchor` the window takes. */
const scrollBox = (): HTMLElement => scrollerRef.current ?? document.documentElement;

/** Reads the scroll box's inline `overflow-anchor` in every commit of the list, once React has
 *  changed the rows and before the anchor keeps its place: a child's layout effect runs ahead of
 *  its parent's `componentDidUpdate`. */
function CommitProbe(): null {
  useLayoutEffect(() => {
    anchoringSeen.push(scrollBox().style.getPropertyValue("overflow-anchor"));
  });
  return null;
}

function List({
  banner = false,
  enabled,
  groups,
  headings,
  leaving,
  rows,
  scroller,
}: {
  /** Whether a banner BANNER_HEIGHT tall stands above the list, as the Inbox's header stands
   *  above its rows. */
  banner?: boolean;
  enabled?: () => boolean;
  /** A row's group; rows default to one shared group. */
  groups?: Readonly<Record<string, string>>;
  /** The rows a heading is drawn right before, as a group's heading is drawn before its first
   *  row. */
  headings?: readonly string[];
  /** The rows a press of the reader's is taking away, as the host names them. */
  leaving?: (id: string) => boolean;
  rows: string[];
  /** When given, the rows scroll in this element instead of the document. */
  scroller?: RefObject<HTMLDivElement | null>;
}): ReactNode {
  const list = (
    <ViewportAnchor
      enabled={enabled}
      group="data-group"
      item="data-row"
      leaving={leaving}
      ref={anchor}
      scroller={scroller}
    >
      {rows.map((id) => (
        <Fragment key={id}>
          {headings?.includes(id) === true ? <div data-heading={id} /> : null}
          <div data-group={groups?.[id] ?? "one"} data-row={id} tabIndex={-1}>
            <button type="button">{id}</button>
          </div>
        </Fragment>
      ))}
      <CommitProbe />
    </ViewportAnchor>
  );
  const page = (
    <>
      {banner ? <div data-banner="" /> : null}
      {list}
    </>
  );
  return scroller === undefined ? (
    page
  ) : (
    <div data-scroller="" ref={scroller}>
      {page}
    </div>
  );
}

// A fixed geometry: rows, and the headings between them, are ROW_HEIGHT tall, stacked from the
// list's top in DOM order, in a VIEWPORT_HEIGHT window that scrolls through `window.scrollY` - or,
// where the list is rendered in a scroller, through that element's `scrollTop`, the scroller
// itself filling the window and never moving. A row's own button is drawn in the row's box. The
// list's top is `listTop`, BANNER_HEIGHT lower while the banner is drawn. `listTop` starts at
// LIST_TOP; a test lowers it to shrink content above the rows without moving what scrolls them,
// which is what a card collapsing in a commit of its own does.
const laidOut = () => [...document.querySelectorAll<HTMLElement>("[data-row], [data-heading]")];
const viewportTop = (id: string) =>
  document.querySelector(`[data-row="${id}"]`)?.getBoundingClientRect().top;
const BANNER_HEIGHT = 52;
const listStart = () =>
  listTop + (document.querySelector("[data-banner]") === null ? 0 : BANNER_HEIGHT);

let listTop = LIST_TOP;
let contentLeft = 0;
let contentWidth = 100;
let scrollYDescriptor: PropertyDescriptor | undefined;
let originalScrollBy: typeof window.scrollBy;
let restoreSpies: (() => void)[] = [];

function setScrollY(value: number): void {
  Object.defineProperty(window, "scrollY", { configurable: true, value, writable: true });
}

/** Resolves once `count` animation frames have run. */
function frames(count: number): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  const step = (left: number) => {
    if (left === 0) resolve();
    else requestAnimationFrame(() => step(left - 1));
  };
  step(count);
  return promise;
}

beforeEach(() => {
  scrollYDescriptor = Object.getOwnPropertyDescriptor(window, "scrollY");
  originalScrollBy = window.scrollBy;
  listTop = LIST_TOP;
  contentLeft = 0;
  contentWidth = 100;
  anchoringSeen.length = 0;
  setScrollY(0);
  window.scrollBy = ((x: number | ScrollToOptions, y?: number) => {
    const top = typeof x === "number" ? y : x.top;
    if (top !== undefined) setScrollY(window.scrollY + top);
  }) as typeof window.scrollBy;
  Object.defineProperty(window, "innerHeight", {
    configurable: true,
    value: VIEWPORT_HEIGHT,
    writable: true,
  });
  const rect = spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
    this: Element
  ) {
    const all = laidOut();
    // Whatever scrolls the rows carries them: the window, or the scroller when one is rendered.
    const offset = scrollerRef.current?.scrollTop ?? window.scrollY;
    const isScroller = this.hasAttribute("data-scroller");
    const item = this.closest("[data-row], [data-heading]");
    const top = isScroller
      ? 0
      : item !== null
        ? listStart() + all.indexOf(item as HTMLElement) * ROW_HEIGHT - offset
        : listStart() - offset;
    const height = isScroller
      ? VIEWPORT_HEIGHT
      : item !== null
        ? ROW_HEIGHT
        : all.length * ROW_HEIGHT;
    return {
      bottom: top + height,
      height,
      left: isScroller ? 0 : contentLeft,
      right: isScroller ? 100 : contentLeft + contentWidth,
      toJSON: () => ({}),
      top,
      width: isScroller ? 100 : contentWidth,
      x: isScroller ? 0 : contentLeft,
      y: top,
    };
  });
  const point = spyOn(document, "elementFromPoint").mockImplementation((_x, y) => {
    if (_x < 0 || _x > 100) return null;
    const offset = scrollerRef.current?.scrollTop ?? window.scrollY;
    const index = Math.floor((offset + y - listStart()) / ROW_HEIGHT);
    return laidOut()[index] ?? null;
  });
  restoreSpies = [() => rect.mockRestore(), () => point.mockRestore()];
});

afterEach(() => {
  for (const restore of restoreSpies) restore();
  window.scrollBy = originalScrollBy;
  if (scrollYDescriptor === undefined) {
    Reflect.deleteProperty(window, "scrollY");
  } else {
    Object.defineProperty(window, "scrollY", scrollYDescriptor);
  }
});

const ids = (count: number, from = 1) => Array.from({ length: count }, (_, i) => `r${from + i}`);

test("a scrolled reader keeps the row at the viewport centre in place when rows are inserted above", () => {
  const view = render(<List rows={ids(12)} />);
  try {
    setScrollY(100);
    // Visible centre: (LIST_TOP - 100 + 600) / 2 = 300 → document y 400 → row index 6 → r7.
    const before = viewportTop("r7");
    expect(chosen()).toEqual({ id: "r7", via: "center" });
    view.rerender(<List rows={["new-a", "new-b", ...ids(12)]} />);
    expect(viewportTop("r7")).toBe(before);
    expect(window.scrollY).toBe(200);
  } finally {
    view.unmount();
  }
});

test("a reader at the top sees new rows arrive without the view following the centre row", () => {
  const view = render(<List rows={ids(12)} />);
  try {
    expect(chosen()).toBeNull();
    view.rerender(<List rows={["new-a", ...ids(12)]} />);
    expect(window.scrollY).toBe(0);
    expect(viewportTop("new-a")).toBe(LIST_TOP);
  } finally {
    view.unmount();
  }
});

test("the row with focus within is the current node even at the top, ahead of the pointer and the centre", async () => {
  const user = userEvent.setup();
  const view = render(<List rows={ids(12)} />);
  try {
    const focused = document.querySelector<HTMLElement>('[data-row="r3"] button');
    if (focused === null) throw new Error("r3 button missing");
    focused.focus();
    const hovered = document.querySelector<HTMLElement>('[data-row="r5"]');
    if (hovered === null) throw new Error("r5 missing");
    await user.hover(hovered);
    const focusedTop = viewportTop("r3");
    const hoveredTop = viewportTop("r5");

    // Two rows land above r3 and one between r3 and r5.
    expect(chosen()).toEqual({ id: "r3", via: "focus" });
    view.rerender(<List rows={["a", "b", "r1", "r2", "r3", "r4", "between", ...ids(8, 5)]} />);

    expect(viewportTop("r3")).toBe(focusedTop);
    expect(viewportTop("r5")).toBe((hoveredTop ?? 0) + ROW_HEIGHT);
    expect(focused).toHaveFocus();
  } finally {
    view.unmount();
  }
});

test("the row under the pointer is the current node ahead of the centre, and is released when the pointer leaves it", async () => {
  const user = userEvent.setup();
  const view = render(<List rows={ids(12)} />);
  try {
    setScrollY(100);
    const hovered = document.querySelector<HTMLElement>('[data-row="r2"]');
    if (hovered === null) throw new Error("r2 missing");
    await user.hover(hovered);
    const hoveredTop = viewportTop("r2");
    const centreTop = viewportTop("r7");

    // One row above r2, one between r2 and the centre row r7.
    expect(chosen()).toEqual({ id: "r2", via: "pointer" });
    view.rerender(<List rows={["a", "r1", "r2", "r3", "between", ...ids(9, 4)]} />);
    expect(viewportTop("r2")).toBe(hoveredTop);
    expect(viewportTop("r7")).toBe((centreTop ?? 0) + ROW_HEIGHT);

    await user.unhover(hovered);
    expect(chosen()?.via).toBe("center");
  } finally {
    view.unmount();
  }
});

test("the pointer moving onto the card a row opened keeps that row the current one, until the card closes", async () => {
  const user = userEvent.setup();
  const view = render(<List rows={ids(12)} />);
  // The one hover card (`features/refs/RefPreview.tsx`): `fixed` over the page and portaled into
  // the body, so it is never inside the list whose row opened it.
  const card = document.createElement("div");
  card.id = "ref-preview";
  document.body.append(card);
  try {
    setScrollY(100);
    const hovered = document.querySelector<HTMLElement>('[data-row="r2"]');
    const reference = hovered?.querySelector("button") ?? null;
    if (hovered === null || reference === null) throw new Error("r2 missing");
    await user.hover(reference);
    expect(chosen()).toEqual({ id: "r2", via: "pointer" });

    // The card opened over the resting pointer. `RefPreview` names its reference by
    // `aria-describedby` for as long as it is shown, which is how the row is known to own it.
    reference.setAttribute("aria-describedby", "ref-preview");
    const hoveredTop = viewportTop("r2");
    // user-event's unhover can set no relatedTarget, which is what names the card the pointer went to.
    fireEvent.pointerOut(reference, { relatedTarget: card });

    // A row arriving above r2 while the pointer reads r2's card leaves r2 where it was.
    expect(chosen()).toEqual({ id: "r2", via: "pointer" });
    view.rerender(<List rows={["a", ...ids(12)]} />);
    expect(viewportTop("r2")).toBe(hoveredTop);

    // The pointer left the card for somewhere else, and the card closed itself: the hand is off
    // the row, and the centre has it again.
    reference.removeAttribute("aria-describedby");
    card.remove();
    expect(chosen()?.via).toBe("center");
  } finally {
    view.unmount();
    card.remove();
  }
});

test("a disabled anchor measures nothing and leaves the scroll alone", () => {
  const view = render(<List enabled={() => false} rows={ids(12)} />);
  try {
    setScrollY(100);
    const rect = spyOn(Element.prototype, "getBoundingClientRect");
    view.rerender(<List enabled={() => false} rows={["new-a", ...ids(12)]} />);
    expect(rect.mock.calls.length).toBe(0);
    expect(window.scrollY).toBe(100);
  } finally {
    view.unmount();
  }
});

test("a current node that left the list scrolls nothing", () => {
  const view = render(<List rows={ids(12)} />);
  try {
    setScrollY(100);
    expect(chosen()).toEqual({ id: "r7", via: "center" });
    view.rerender(<List rows={ids(12).filter((id) => id !== "r7")} />);
    expect(window.scrollY).toBe(100);
  } finally {
    view.unmount();
  }
});

test("a current node that changed group was relocated, not shifted: it is not followed and the view stays", () => {
  const view = render(<List rows={ids(12)} />);
  try {
    setScrollY(100);
    expect(chosen()).toEqual({ id: "r7", via: "center" });
    // r7 leaves its section for the end of the list.
    view.rerender(
      <List groups={{ r7: "other" }} rows={[...ids(12).filter((id) => id !== "r7"), "r7"]} />
    );
    expect(window.scrollY).toBe(100);
    expect(viewportTop("r8")).toBe(LIST_TOP + 6 * ROW_HEIGHT - 100);
  } finally {
    view.unmount();
  }
});

test("a current node reordered within its group is still followed", () => {
  const view = render(<List rows={ids(12)} />);
  try {
    setScrollY(100);
    const before = viewportTop("r7");
    expect(chosen()).toEqual({ id: "r7", via: "center" });
    // r7 is promoted two places up its section.
    view.rerender(<List rows={[...ids(4), "r7", "r5", "r6", ...ids(5, 8)]} />);
    expect(viewportTop("r7")).toBe(before);
    expect(window.scrollY).toBe(0);
  } finally {
    view.unmount();
  }
});

test("rows scrolling in an element are pinned by that element's scrollTop, and the window stays", () => {
  const view = render(<List rows={ids(12)} scroller={scrollerRef} />);
  try {
    const scroller = scrollerRef.current;
    if (scroller === null) throw new Error("scroller missing");
    scroller.scrollTop = 100;
    // The same geometry as the window case: the centre of the scroller's box is r7.
    const before = viewportTop("r7");
    expect(chosen()).toEqual({ id: "r7", via: "center" });
    view.rerender(<List rows={["new-a", "new-b", ...ids(12)]} scroller={scrollerRef} />);
    expect(viewportTop("r7")).toBe(before);
    expect(scroller.scrollTop).toBe(200);
    expect(window.scrollY).toBe(0);
  } finally {
    view.unmount();
  }
});

// The Inbox drawer's defect: the row above the hand shrank inside its own card, a commit this
// anchor never sees, and the browser re-dispatched the boundary events at the pointer's own
// unchanged position. Reading the row's place again there would take the drift for the place
// to keep, and the next commit would pin the row where it had already slid to.
test("a row moving under a pointer that has not moved does not become the place it is kept at", () => {
  const view = render(<List rows={ids(12)} scroller={scrollerRef} />);
  try {
    const scroller = scrollerRef.current;
    if (scroller === null) throw new Error("scroller missing");
    scroller.scrollTop = 100;
    const hovered = document.querySelector<HTMLElement>('[data-row="r4"]');
    if (hovered === null) throw new Error("r4 missing");
    // user-event's hover sends no clientX/clientY of its own, and the pointer's place is what this row measures.
    fireEvent.pointerOver(hovered, { clientX: 20, clientY: 250 });
    const held = viewportTop("r4");

    // Content above the hand's row loses 50 px with no commit of this anchor's, so the row
    // slides up under a pointer that has not moved, and the browser says so again. The box's
    // own scrollTop does not move: the content shrank, the reader did not scroll.
    listTop -= ROW_HEIGHT;
    expect(viewportTop("r4")).toBe((held ?? 0) - ROW_HEIGHT);
    // The browser's own re-dispatch at the unchanged position, which user-event has no form for.
    fireEvent.pointerOver(hovered, { clientX: 20, clientY: 250 });

    view.rerender(<List rows={["a", ...ids(12)]} scroller={scrollerRef} />);
    expect(viewportTop("r4")).toBe(held);
  } finally {
    view.unmount();
  }
});

// A commit landing between a scroll and the scroll event it raises: following a reference from
// a Conversation card scrolls the page, and the thread-open commit arrives before the event
// does. Where the document scrolls the rows nothing is kept for that commit to restore, so the
// reader stays where they put themselves.
test("a window scroll whose event has not arrived yet is not undone by the next commit", () => {
  const view = render(<List rows={ids(12)} />);
  try {
    setScrollY(100);
    const hovered = document.querySelector<HTMLElement>('[data-row="r4"]');
    if (hovered === null) throw new Error("r4 missing");
    // user-event's hover sends no clientX/clientY of its own, and the pointer's place is what this row measures.
    fireEvent.pointerOver(hovered, { clientX: 20, clientY: 250 });

    // The page moved to the reference's target and the `scroll` event has not run yet.
    setScrollY(250);
    const at = viewportTop("r4");

    // A row arrives below the hand's row, which leaves that row where it is.
    view.rerender(<List rows={[...ids(4), "new", ...ids(8, 5)]} />);
    expect(window.scrollY).toBe(250);
    expect(viewportTop("r4")).toBe(at);
  } finally {
    view.unmount();
  }
});

// The reader gave the row under their hand another priority while the Inbox is grouped by it:
// the row left its section in an earlier commit, and the place kept for it is where it used to
// sit. A row that changed group was relocated, not shifted, so the view does not follow it.
test("a held row that changed group is not followed to the section it moved to", () => {
  const view = render(<List rows={ids(12)} scroller={scrollerRef} />);
  try {
    const scroller = scrollerRef.current;
    if (scroller === null) throw new Error("scroller missing");
    scroller.scrollTop = 100;
    const hovered = document.querySelector<HTMLElement>('[data-row="r4"]');
    if (hovered === null) throw new Error("r4 missing");
    // user-event's hover sends no clientX/clientY of its own, and the pointer's place is what this row measures.
    fireEvent.pointerOver(hovered, { clientX: 20, clientY: 250 });

    // r4 leaves its section for the end of the list. The commit that moves it finds it in a
    // group it was not measured in, and leaves the view alone.
    const moved = [...ids(12).filter((id) => id !== "r4"), "r4"];
    view.rerender(<List groups={{ r4: "other" }} rows={moved} scroller={scrollerRef} />);
    expect(scroller.scrollTop).toBe(100);

    // The next commit finds it in its new section, where the kept place is no longer its own.
    view.rerender(
      <List groups={{ r4: "other" }} rows={[...moved, "r13"]} scroller={scrollerRef} />
    );
    expect(scroller.scrollTop).toBe(100);
    expect(viewportTop("r4")).toBe(LIST_TOP + 11 * ROW_HEIGHT - 100);
  } finally {
    view.unmount();
  }
});

test("the centre anchor stays inside a horizontally scrolled table's visible box", () => {
  contentLeft = -800;
  contentWidth = 2000;
  const view = render(<List rows={ids(12)} scroller={scrollerRef} />);
  try {
    const scroller = scrollerRef.current;
    if (!scroller) throw new Error("No scroller");
    scroller.scrollTop = 100;
    expect(chosen()).toEqual({ id: "r7", via: "center" });
    const before = viewportTop("r7");
    view.rerender(<List rows={["new", ...ids(12)]} scroller={scrollerRef} />);
    expect(viewportTop("r7")).toBe(before);
  } finally {
    view.unmount();
  }
});

/** The button in row `id`, which takes the focus as a row's mark takes it from the selection
 *  bar's hand-off. */
function buttonOf(id: string): HTMLElement {
  const button = document.querySelector<HTMLElement>(`[data-row="${id}"] button`);
  if (button === null) throw new Error(`${id} button missing`);
  return button;
}

/** Every element brought into view from now on, named by its row and its tag, with the options
 *  it was asked with, until `restore`. The view itself does not move. A name rather than the
 *  element, so a failed match prints a line rather than the element's whole tree. */
function watchScrollsIntoView(): {
  restore: () => void;
  scrolled: { options: unknown; target: string }[];
} {
  const scrolled: { options: unknown; target: string }[] = [];
  const spy = spyOn(Element.prototype, "scrollIntoView").mockImplementation(function (
    this: Element,
    options?: unknown
  ) {
    const row = this.closest("[data-row]")?.getAttribute("data-row") ?? "no row";
    scrolled.push({ options, target: `${row} ${this.tagName.toLowerCase()}` });
  });
  return { restore: () => spy.mockRestore(), scrolled };
}

// A Dismiss over two marked rows: the selection bar's hand-off has put the focus in the row after
// them while they are still drawn. They go, and the rows after them move up into their place, as
// the reader asked. The view stays where the reader put it. For that commit the browser's own
// scroll anchoring is off on the document's root, whose value the window takes, and it stays off
// through the rendering update after it, since WebKit adjusts at a layout after the commit: it is
// back on two frames later.
test("rows the host names as leaving let the focused row below them move up into their place, with no scroll", async () => {
  const going = (id: string) => id === "r3" || id === "r4";
  const view = render(<List leaving={going} rows={ids(12)} />);
  try {
    setScrollY(100);
    const focused = buttonOf("r6");
    focused.focus();
    const place = viewportTop("r3");
    expect(chosen()).toEqual({ id: "r6", via: "focus" });

    view.rerender(<List leaving={going} rows={ids(12).filter((id) => !going(id))} />);
    expect(viewportTop("r5")).toBe(place);
    expect(viewportTop("r6")).toBe((place ?? 0) + ROW_HEIGHT);
    expect(window.scrollY).toBe(100);
    expect(focused).toHaveFocus();
    expect(anchoringSeen.at(-1)).toBe("none");
    expect(document.documentElement.style.getPropertyValue("overflow-anchor")).toBe("none");
    await frames(1);
    expect(document.documentElement.style.getPropertyValue("overflow-anchor")).toBe("none");
    await frames(1);
    expect(document.documentElement.style.getPropertyValue("overflow-anchor")).toBe("");
  } finally {
    view.unmount();
  }
});

// A live update takes two rows away above the view while the reader's hand is on a row in sight,
// and a press of theirs is still taking a row below that one away. Nobody pressed on the rows that
// went, so the anchor keeps the rows in view where they were, and the browser's own scroll
// anchoring is left on.
test("a live update that takes rows away above the view keeps the rows in view where they were, with the browser's scroll anchoring left on", () => {
  const going = (id: string) => id === "r9";
  const view = render(<List leaving={going} rows={ids(12)} />);
  try {
    setScrollY(275);
    buttonOf("r8").focus();
    const inView = () => ["r6", "r7", "r8", "r9"].map((id) => viewportTop(id));
    const seen = inView();
    // r1 and r2 stand wholly above the window.
    expect((viewportTop("r2") ?? 0) + ROW_HEIGHT).toBeLessThan(0);

    const updated = ids(12).filter((id) => id !== "r1" && id !== "r2");
    view.rerender(<List leaving={going} rows={updated} />);
    expect(inView()).toEqual(seen);
    expect(window.scrollY).toBe(275 - 2 * ROW_HEIGHT);
    expect(anchoringSeen.at(-1)).toBe("");
  } finally {
    view.unmount();
  }
});

// The reader's own Dismiss takes away the row their hand is on, not one above it: the row the
// host names as leaving is the current row. The browser's own scroll anchoring would hold the
// view against that row's going, which is the shift this component lets happen - measured in the
// Inbox at 390x664, a thread's Dismiss moved the window from 437 px to 0 in Chromium, Firefox
// and WebKit - so it is turned off for that commit as it is for a row above, and back on two
// frames later. The anchor makes nothing up for a row that has gone, so it scrolls nothing.
test("the row the host names as leaving being the focused row itself turns the browser's scroll anchoring off for that commit", async () => {
  const going = (id: string) => id === "r6";
  const view = render(<List leaving={going} rows={ids(12)} />);
  try {
    setScrollY(100);
    buttonOf("r6").focus();
    expect(chosen()).toEqual({ id: "r6", via: "focus" });

    view.rerender(<List leaving={going} rows={ids(12).filter((id) => !going(id))} />);
    expect(document.querySelector('[data-row="r6"]')).toBeNull();
    expect(window.scrollY).toBe(100);
    expect(anchoringSeen.at(-1)).toBe("none");
    expect(document.documentElement.style.getPropertyValue("overflow-anchor")).toBe("none");
    await frames(1);
    expect(document.documentElement.style.getPropertyValue("overflow-anchor")).toBe("none");
    await frames(1);
    expect(document.documentElement.style.getPropertyValue("overflow-anchor")).toBe("");
  } finally {
    view.unmount();
  }
});

// The reader's press takes the last rows of a kind away, and a control the page offers only while
// such rows are listed goes with them: below `sm` the Inbox's header loses the line its Mark all
// read stood on. That shrink is the press's doing as much as the rows' going, so the rows move up
// by both and the view does not scroll.
test("content above the list that shrinks in the commit in which named rows leave moves the rows up too, with no scroll", () => {
  const going = (id: string) => id === "r3" || id === "r4";
  const view = render(<List banner leaving={going} rows={ids(12)} />);
  try {
    setScrollY(100);
    buttonOf("r6").focus();
    const before = viewportTop("r6") ?? 0;

    view.rerender(<List leaving={going} rows={ids(12).filter((id) => !going(id))} />);
    expect(window.scrollY).toBe(100);
    expect(viewportTop("r6")).toBe(before - 2 * ROW_HEIGHT - BANNER_HEIGHT);
  } finally {
    view.unmount();
  }
});

// The same banner goes with no row of the reader's press leaving above the focused row: a live
// update took it. The rows in view stay where they were.
test("content above the list that shrinks with no named row leaving above the focused row is compensated, and the rows in view stay", () => {
  const going = (id: string) => id === "r9";
  const view = render(<List banner leaving={going} rows={ids(12)} />);
  try {
    setScrollY(100);
    buttonOf("r6").focus();
    const inView = () => ["r5", "r6", "r7"].map((id) => viewportTop(id));
    const seen = inView();

    view.rerender(<List leaving={going} rows={ids(12)} />);
    expect(inView()).toEqual(seen);
    expect(window.scrollY).toBe(100 - BANNER_HEIGHT);
  } finally {
    view.unmount();
  }
});

// Rows above the focused row are named as leaving for the whole time the press's write is out, so
// a commit can name them while none of them goes. In such a commit the banner goes, by a live
// update: no named row went, so the shrink is no doing of the press's. It is compensated, and the
// rows in view stay where they were.
test("content above the list that shrinks while named rows above the focused row stay is compensated, and the rows in view stay", () => {
  const going = (id: string) => id === "r3" || id === "r4";
  const view = render(<List banner leaving={going} rows={ids(12)} />);
  try {
    setScrollY(100);
    buttonOf("r6").focus();
    const inView = () => ["r5", "r6", "r7"].map((id) => viewportTop(id));
    const seen = inView();

    view.rerender(<List leaving={going} rows={ids(12)} />);
    expect(inView()).toEqual(seen);
    expect(window.scrollY).toBe(100 - BANNER_HEIGHT);
  } finally {
    view.unmount();
  }
});

// Content that grows above the list in the commit in which named rows leave is no going of the
// press's: it is compensated, so the rows move up by the named rows' space alone.
test("content above the list that grows in the commit in which named rows leave is still compensated", () => {
  const going = (id: string) => id === "r3" || id === "r4";
  const view = render(<List leaving={going} rows={ids(12)} />);
  try {
    setScrollY(100);
    buttonOf("r6").focus();
    const before = viewportTop("r6") ?? 0;

    view.rerender(<List banner leaving={going} rows={ids(12).filter((id) => !going(id))} />);
    expect(viewportTop("r6")).toBe(before - 2 * ROW_HEIGHT);
    expect(window.scrollY).toBe(100 + BANNER_HEIGHT);
  } finally {
    view.unmount();
  }
});

// A refetch drops a row the reader did not press on, in the same commit as the row their press
// takes away. That row's going is compensated as any shift they did not cause is, and the
// focused row moves up by the row they pressed on alone.
test("a row the host does not name, going in the same commit as a row it names, is still compensated", () => {
  const going = (id: string) => id === "r3";
  const view = render(<List leaving={going} rows={ids(12)} />);
  try {
    setScrollY(100);
    buttonOf("r8").focus();
    const before = viewportTop("r8") ?? 0;

    // r3 goes by the reader's press, r5 by the refetch.
    const kept = ids(12).filter((id) => id !== "r3" && id !== "r5");
    view.rerender(<List leaving={going} rows={kept} />);
    expect(viewportTop("r8")).toBe(before - ROW_HEIGHT);
    expect(window.scrollY).toBe(100 - ROW_HEIGHT);
  } finally {
    view.unmount();
  }
});

// The bar's Snooze: the host names its rows while the write is out and holds them in their group,
// then moves them into Later, after the focused row, in the commit in which it stops naming them.
test("named rows that move to a group after the focused row, in a commit that no longer names them, let it move up all the same", () => {
  const going = (id: string) => id === "r3" || id === "r4";
  const view = render(<List leaving={going} rows={ids(12)} />);
  try {
    setScrollY(100);
    buttonOf("r6").focus();
    const place = viewportTop("r3");

    const moved = [...ids(12).filter((id) => !going(id)), "r3", "r4"];
    view.rerender(<List groups={{ r3: "later", r4: "later" }} rows={moved} />);
    expect(viewportTop("r5")).toBe(place);
    expect(window.scrollY).toBe(100);
  } finally {
    view.unmount();
  }
});

// The rows the press takes away are the last of their group, whose heading goes with them: the
// next group's heading and rows move up into the place the heading and the rows held.
test("a group's heading that goes with the last named rows under it lets the focused row move up by its place too", () => {
  const going = (id: string) => id === "r3" || id === "r4";
  const headingTop = (id: string) =>
    document.querySelector(`[data-heading="${id}"]`)?.getBoundingClientRect().top;
  const view = render(<List headings={["r3", "r5"]} leaving={going} rows={ids(12)} />);
  try {
    setScrollY(100);
    buttonOf("r6").focus();
    const place = headingTop("r3");

    const kept = ids(12).filter((id) => !going(id));
    view.rerender(<List headings={["r5"]} leaving={going} rows={kept} />);
    expect(headingTop("r5")).toBe(place);
    expect(viewportTop("r5")).toBe((place ?? 0) + ROW_HEIGHT);
    expect(window.scrollY).toBe(100);
  } finally {
    view.unmount();
  }
});

// A press over two rows far apart: one the reader scrolled above the view, one in sight. Each
// frees its whole space, the part above the view included, so the rows in view move up by the
// row above it, and the focused row after both moves up by the two and ends in sight. Nothing
// scrolls: not the window, and not the focused control into view.
test("named rows that stood above the view free their whole space: the focused row moves up by all of it, ends in sight, and nothing scrolls", () => {
  const going = (id: string) => id === "r2" || id === "r7";
  const moves = watchScrollsIntoView();
  const view = render(<List leaving={going} rows={ids(12)} />);
  try {
    setScrollY(275);
    buttonOf("r8").focus();
    const before = viewportTop("r8") ?? 0;
    const inView = viewportTop("r5") ?? 0;
    // r2 stands wholly above the window.
    expect((viewportTop("r2") ?? 0) + ROW_HEIGHT).toBeLessThan(0);

    view.rerender(<List leaving={going} rows={ids(12).filter((id) => !going(id))} />);
    expect(viewportTop("r5")).toBe(inView - ROW_HEIGHT);
    expect(viewportTop("r8")).toBe(before - 2 * ROW_HEIGHT);
    expect(before - 2 * ROW_HEIGHT).toBeGreaterThanOrEqual(0);
    expect(window.scrollY).toBe(275);
    expect(moves.scrolled).toEqual([]);
  } finally {
    view.unmount();
    moves.restore();
  }
});

// The rows the press takes away, one above the view and one just below it, free their whole
// space. The focused row after them moves up across the window's bottom edge and is left partly
// in sight: the least scroll there is brings its control whole into sight at that edge, and the
// window is not scrolled.
test("a focused row the named rows leave partly in sight is brought whole into sight at its nearest edge, and the window is not scrolled", () => {
  Object.defineProperty(window, "innerHeight", { configurable: true, value: 580, writable: true });
  const going = (id: string) => id === "r2" || id === "r16";
  const moves = watchScrollsIntoView();
  const view = render(<List leaving={going} rows={ids(20)} />);
  try {
    setScrollY(245);
    buttonOf("r17").focus();
    const before = viewportTop("r17") ?? 0;
    const after = before - 2 * ROW_HEIGHT;
    expect(after).toBeLessThan(580);
    expect(after + ROW_HEIGHT).toBeGreaterThan(580);

    view.rerender(<List leaving={going} rows={ids(20).filter((id) => !going(id))} />);
    expect(viewportTop("r17")).toBe(after);
    expect(window.scrollY).toBe(245);
    expect(moves.scrolled).toEqual([{ options: { block: "nearest" }, target: "r17 button" }]);
  } finally {
    view.unmount();
    moves.restore();
  }
});

// In a named box the anchor keeps the place it last saw the hand's row at. Once the rows the
// press took away have let that row move up, the kept place is where the row stands now, so the
// next commit keeps it there rather than putting it back where it stood before. The box's own
// scroll anchoring is off from the press's commit until the next commit no press takes rows away
// in, which gives it back the value it had.
test("in a named box the kept place moves up with the focused row, and the next commit keeps the row there", async () => {
  const user = userEvent.setup();
  const going = (id: string) => id === "r3" || id === "r4";
  const kept = ids(12).filter((id) => !going(id));
  const view = render(<List leaving={going} rows={ids(12)} scroller={scrollerRef} />);
  try {
    const scroller = scrollerRef.current;
    if (scroller === null) throw new Error("scroller missing");
    scroller.style.setProperty("overflow-anchor", "auto");
    scroller.scrollTop = 100;
    buttonOf("r6").focus();
    const row = document.querySelector<HTMLElement>('[data-row="r6"]');
    if (row === null) throw new Error("r6 missing");
    // The pointer comes to rest on the row, which keeps its place.
    await user.hover(row);
    const place = viewportTop("r4");

    view.rerender(<List leaving={going} rows={kept} scroller={scrollerRef} />);
    expect(viewportTop("r6")).toBe(place);
    expect(scroller.scrollTop).toBe(100);
    expect(anchoringSeen.at(-1)).toBe("none");
    // Still off: no rendering update has run since. The next commit no press takes rows away in
    // gives it back, with the value the box had.
    expect(scroller.style.getPropertyValue("overflow-anchor")).toBe("none");

    // A row arrives at the end of the list, below the hand's row.
    view.rerender(<List leaving={going} rows={[...kept, "r13"]} scroller={scrollerRef} />);
    expect(viewportTop("r6")).toBe(place);
    expect(scroller.scrollTop).toBe(100);
    expect(anchoringSeen.at(-1)).toBe("auto");
    expect(scroller.style.getPropertyValue("overflow-anchor")).toBe("auto");
  } finally {
    view.unmount();
  }
});
