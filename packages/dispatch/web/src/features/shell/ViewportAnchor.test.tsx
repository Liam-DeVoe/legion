import { afterEach, beforeEach, expect, spyOn, test } from "bun:test";
// biome-ignore lint/style/noRestrictedImports: this file sends the pointer's own clientX/clientY and a pointerout's relatedTarget, neither of which user-event's hover and unhover can set, and a box's own scroll event, which user-event has no form for.
import { fireEvent, render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef, type ReactNode, type RefObject } from "react";

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

function List({
  enabled,
  groups,
  rows,
  scroller,
}: {
  enabled?: () => boolean;
  /** A row's group; rows default to one shared group. */
  groups?: Readonly<Record<string, string>>;
  rows: string[];
  /** When given, the rows scroll in this element instead of the document. */
  scroller?: RefObject<HTMLDivElement | null>;
}): ReactNode {
  const list = (
    <ViewportAnchor
      enabled={enabled}
      group="data-group"
      item="data-row"
      ref={anchor}
      scroller={scroller}
    >
      {rows.map((id) => (
        <div data-group={groups?.[id] ?? "one"} data-row={id} key={id} tabIndex={-1}>
          <button type="button">{id}</button>
        </div>
      ))}
    </ViewportAnchor>
  );
  return scroller === undefined ? (
    list
  ) : (
    <div data-scroller="" ref={scroller}>
      {list}
    </div>
  );
}

// A fixed geometry: rows are ROW_HEIGHT tall unless a case gives one another height
// (`heights`), stacked from `listTop` in DOM order, in a VIEWPORT_HEIGHT window that scrolls
// through `window.scrollY` - or, where the list is rendered in a scroller, through that element's
// `scrollTop`, the scroller itself filling the window and never moving. A row's control is
// CONTROL_HEIGHT tall and stands at the row's foot. `listTop` starts at LIST_TOP; a test lowers
// it to shrink content above the rows without moving what scrolls them, which is what a card
// collapsing in a commit of its own does.
const CONTROL_HEIGHT = 20;
const rows = () => [...document.querySelectorAll<HTMLElement>("[data-row]")];
const viewportTop = (id: string) =>
  document.querySelector(`[data-row="${id}"]`)?.getBoundingClientRect().top;
const controlTop = (id: string) =>
  document.querySelector(`[data-row="${id}"] button`)?.getBoundingClientRect().top;

let listTop = LIST_TOP;
let contentLeft = 0;
let contentWidth = 100;
/** The rows a case draws at another height than ROW_HEIGHT, by id. */
let heights: Record<string, number> = {};
let scrollYDescriptor: PropertyDescriptor | undefined;
let originalScrollBy: typeof window.scrollBy;
let restoreSpies: (() => void)[] = [];

const heightOf = (row: Element) => heights[row.getAttribute("data-row") ?? ""] ?? ROW_HEIGHT;
const listHeight = () => rows().reduce((sum, row) => sum + heightOf(row), 0);
/** How far below the list's top `row` starts: the heights of the rows before it. */
function rowOffset(row: Element): number {
  let offset = 0;
  for (const one of rows()) {
    if (one === row) break;
    offset += heightOf(one);
  }
  return offset;
}

function setScrollY(value: number): void {
  Object.defineProperty(window, "scrollY", { configurable: true, value, writable: true });
}

beforeEach(() => {
  scrollYDescriptor = Object.getOwnPropertyDescriptor(window, "scrollY");
  originalScrollBy = window.scrollBy;
  listTop = LIST_TOP;
  contentLeft = 0;
  contentWidth = 100;
  heights = {};
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
    // Whatever scrolls the rows carries them: the window, or the scroller when one is rendered.
    const offset = scrollerRef.current?.scrollTop ?? window.scrollY;
    const isScroller = this.hasAttribute("data-scroller");
    const row = this.closest("[data-row]");
    let top = listTop - offset;
    let height: number;
    if (isScroller) {
      top = 0;
      height = VIEWPORT_HEIGHT;
    } else if (row === null) {
      height = listHeight();
    } else if (row === this) {
      top += rowOffset(row);
      height = heightOf(row);
    } else {
      top += rowOffset(row) + heightOf(row) - CONTROL_HEIGHT;
      height = CONTROL_HEIGHT;
    }
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
    let top = listTop - (scrollerRef.current?.scrollTop ?? window.scrollY);
    for (const row of rows()) {
      if (y >= top && y < top + heightOf(row)) return row;
      top += heightOf(row);
    }
    return null;
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

// A long entry above the reader is cut, in no commit of this anchor's, by more than the box
// holds below the view. The browser pulls the box's scrollTop back to its new end and says so
// with a scroll event. That is not the reader scrolling: keeping the row's place there would
// leave the row as far out of place as the room that was below the view.
test("a box the browser pulls back to its end after its content shrank keeps the held row in place", () => {
  // happy-dom's ResizeObserver never calls back: this one is called back where the case says
  // the content changed height.
  const RealResizeObserver = globalThis.ResizeObserver;
  const observers: ResizeObserverCallback[] = [];
  globalThis.ResizeObserver = class {
    constructor(callback: ResizeObserverCallback) {
      observers.push(callback);
    }
    disconnect() {}
    observe() {}
    unobserve() {}
  } as unknown as typeof ResizeObserver;
  const resized = () => {
    for (const callback of observers) callback([], undefined as unknown as ResizeObserver);
  };
  // The long entry, still whole, is the content above the rows.
  listTop = 400;
  const view = render(<List rows={ids(40)} scroller={scrollerRef} />);
  try {
    const scroller = scrollerRef.current;
    if (scroller === null) throw new Error("scroller missing");
    // happy-dom lays nothing out: the box is the window's height, and its content runs from the
    // box's top to the last row's foot.
    Object.defineProperty(scroller, "clientHeight", {
      configurable: true,
      value: VIEWPORT_HEIGHT,
    });
    Object.defineProperty(scroller, "scrollHeight", {
      configurable: true,
      get: () => listTop + rows().length * ROW_HEIGHT,
    });
    const focused = document.querySelector<HTMLElement>('[data-row="r34"] button');
    if (focused === null) throw new Error("r34 button missing");
    focused.focus();
    // The reader scrolls to 50 px short of the box's end (2,400 - 600 = 1,800).
    scroller.scrollTop = 1750;
    fireEvent.scroll(scroller);
    const held = viewportTop("r34");
    expect(held).toBe(300);

    // The entry is cut by 300 px, six times the room below the view, and the browser pulls the
    // box back to its new end. That frame's scroll event runs before its resize observations.
    listTop -= 300;
    scroller.scrollTop = scroller.scrollHeight - scroller.clientHeight;
    expect(scroller.scrollTop).toBe(1500);
    fireEvent.scroll(scroller);
    resized();
    expect(viewportTop("r34")).toBe(held);
    // The scroll event of the anchor's own put-back.
    fireEvent.scroll(scroller);

    // A later change of the content's height still holds the row there.
    listTop -= ROW_HEIGHT;
    resized();
    expect(viewportTop("r34")).toBe(held);
  } finally {
    view.unmount();
    globalThis.ResizeObserver = RealResizeObserver;
  }
});

/**
 * Draws `drawn` in a scroller that keeps its offset as the browser's box does: VIEWPORT_HEIGHT
 * tall, its content running from its top to the last row's foot, and a `scrollTop` that stays
 * between its top and its end. happy-dom keeps any offset it is given, lays nothing out, and
 * never calls a ResizeObserver back, so `settle` stands in for the browser's frames after a
 * change. In each frame the box is pulled back to its end where its content no longer reaches
 * that far, one scroll event is raised for whatever moved it since the frame before, and the
 * root's resize is observed where the list's height changed.
 */
function drawInBox(drawn: string[]) {
  const RealResizeObserver = globalThis.ResizeObserver;
  const observers: ResizeObserverCallback[] = [];
  globalThis.ResizeObserver = class {
    constructor(callback: ResizeObserverCallback) {
      observers.push(callback);
    }
    disconnect() {}
    observe() {}
    unobserve() {}
  } as unknown as typeof ResizeObserver;
  const view = render(<List rows={drawn} scroller={scrollerRef} />);
  const scroller = scrollerRef.current;
  if (scroller === null) throw new Error("scroller missing");
  const end = () => Math.max(0, listTop + listHeight() - VIEWPORT_HEIGHT);
  let offset = 0;
  let moved = false;
  let observed = listHeight();
  Object.defineProperty(scroller, "clientHeight", { configurable: true, value: VIEWPORT_HEIGHT });
  Object.defineProperty(scroller, "scrollHeight", {
    configurable: true,
    get: () => listTop + listHeight(),
  });
  Object.defineProperty(scroller, "scrollTop", {
    configurable: true,
    get: () => offset,
    set: (value: number) => {
      const next = Math.max(0, Math.min(value, end()));
      if (next !== offset) moved = true;
      offset = next;
    },
  });
  const frame = () => {
    // The pull-back: the setter keeps the offset within the content as it is now.
    scroller.scrollTop = offset;
    if (moved) {
      moved = false;
      fireEvent.scroll(scroller);
    }
    if (listHeight() !== observed) {
      observed = listHeight();
      for (const callback of observers) callback([], undefined as unknown as ResizeObserver);
    }
  };
  return {
    rerender: (next: string[]) => view.rerender(<List rows={next} scroller={scrollerRef} />),
    scroller,
    settle: () => {
      frame();
      // The scroll event of a put-back the anchor made in that frame.
      if (moved) frame();
    },
    unmount: () => {
      view.unmount();
      globalThis.ResizeObserver = RealResizeObserver;
    },
  };
}

// A control at the foot of a long row holds the focus while the row's head is above the view,
// as a Reply under an agents' turn still drawn whole does. The row is then cut short by a
// commit of its own, which the anchor sees only as its root resized: that moves the control up
// by the cut and leaves the row's top where it was. The reader is on the control, so the
// control is what stays.
test("a focused control at the foot of a long row keeps its place when the row's own content above it shrinks", () => {
  heights = { r3: 1000 };
  const box = drawInBox(ids(40));
  try {
    const focused = document.querySelector<HTMLElement>('[data-row="r3"] button');
    if (focused === null) throw new Error("r3 button missing");
    focused.focus();
    // The reader scrolls to the control: r3 runs from 200 to 1,200 px down the content, and its
    // control stands at its foot.
    box.scroller.scrollTop = 1000;
    box.settle();
    expect(viewportTop("r3")).toBe(-800);
    const held = controlTop("r3");
    expect(held).toBe(180);

    // r3 is cut to 200 px, which raises its control by 800 px.
    heights = { r3: 200 };
    box.settle();
    expect(controlTop("r3")).toBe(held);
    expect(box.scroller.scrollTop).toBe(200);

    // A commit that adds a row above holds r3 at the top that puts its control back, so the
    // control stays there too.
    box.rerender(["new", ...ids(40)]);
    expect(controlTop("r3")).toBe(held);
    box.settle();
    expect(controlTop("r3")).toBe(held);
  } finally {
    box.unmount();
  }
});

// The reader is at the very end of the box when content above their row shrinks outside a
// commit: the browser pulls the box back by the whole shrink, which leaves the row where it
// was. The place is kept at the offset the box was left at, so when the content above grows
// again, with the box no longer at its end, the row still stays.
test("a box pulled back to its end by the whole shrink above the row keeps the row's place for the next change", () => {
  heights = { r2: 150 };
  const box = drawInBox(ids(40));
  try {
    // The reader scrolls to the box's end (2,200 - 600 = 1,600 px) and rests the pointer on r34.
    box.scroller.scrollTop = 1600;
    box.settle();
    const hovered = document.querySelector<HTMLElement>('[data-row="r34"]');
    if (hovered === null) throw new Error("r34 missing");
    // user-event's hover sends no clientX/clientY of its own, and the pointer's place is what this row measures.
    fireEvent.pointerOver(hovered, { clientX: 20, clientY: 270 });
    expect(viewportTop("r34")).toBe(250);

    // r2 is cut by 100 px, and the browser pulls the box back by as much, to its new end.
    heights = {};
    box.settle();
    expect(box.scroller.scrollTop).toBe(1500);
    expect(viewportTop("r34")).toBe(250);

    // r2 grows back by those 100 px, in no commit of the anchor's.
    heights = { r2: 150 };
    box.settle();
    expect(viewportTop("r34")).toBe(250);
  } finally {
    box.unmount();
  }
});

// Content below the reader's row shrinks outside a commit by more than the room below the view:
// the browser pulls the box back to its new end, which moves the row down, and no scroll can
// put it back. Where the pull-back left the row is where the reader now sees it, and the place
// kept from then on, so a later growth below, which gives the box room again, leaves it there.
// The focus is on a control at the foot of that row, and its own place is kept from there too.
test("a box pulled back past the room below the row keeps the row where the pull-back left it, and the focused control's own place from there", () => {
  heights = { r18: 200, r20: 350 };
  const box = drawInBox(ids(20));
  try {
    const focused = document.querySelector<HTMLElement>('[data-row="r18"] button');
    if (focused === null) throw new Error("r18 button missing");
    focused.focus();
    // The reader scrolls to 100 px short of the box's end (1,550 - 600 = 950 px).
    box.scroller.scrollTop = 850;
    box.settle();
    expect(viewportTop("r18")).toBe(100);
    expect(controlTop("r18")).toBe(280);

    // r20 is cut by 300 px, three times the room below the view: the browser pulls the box back
    // 200 px to its new end, which moves r18 down by as much.
    heights = { r18: 200 };
    box.settle();
    expect(box.scroller.scrollTop).toBe(650);
    expect(viewportTop("r18")).toBe(300);
    expect(controlTop("r18")).toBe(480);

    // r20 grows back, which gives the box room below the view again.
    heights = { r18: 200, r20: 350 };
    box.settle();
    expect(viewportTop("r18")).toBe(300);
    expect(controlTop("r18")).toBe(480);

    // r18's own content above its control is cut by 100 px: the control stays where it was.
    heights = { r18: 100, r20: 350 };
    box.settle();
    expect(controlTop("r18")).toBe(480);
  } finally {
    box.unmount();
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
