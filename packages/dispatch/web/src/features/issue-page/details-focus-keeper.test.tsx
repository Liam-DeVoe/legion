import { expect, spyOn, test } from "bun:test";
import { act, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useLayoutEffect, useRef, useState } from "react";

import { emulateChromiumFocusFixup } from "./chromium-focus-fixup";
import { useDetailsFocusKeeper } from "./details-focus-keeper";

/** Where the focus is when no element holds it. */
const PAGE = "the page";

/** happy-dom lays nothing out, so each drawn element says where it stands: `data-top`, and
 *  `data-height` (24 where it has none), in pixels, 300 wide. An element with no `data-top`, or
 *  inside a `hidden` one, is not drawn. */
function layoutOf(element: Element): DOMRect | null {
  const top = element.getAttribute("data-top");
  if (top === null || !element.isConnected || element.closest("[hidden]") !== null) return null;
  return new DOMRect(0, Number(top), 300, Number(element.getAttribute("data-height") ?? 24));
}

/** What every case runs under: the layout above, and Chromium's drop of a disabled control's
 *  focus to the page (`emulateChromiumFocusFixup`). Returns what removes both. */
function chromiumLayout(): () => void {
  // Stubbed on Element, the prototype that owns both methods.
  const bounds = spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
    this: Element
  ) {
    return layoutOf(this) ?? new DOMRect();
  });
  const rects = spyOn(Element.prototype, "getClientRects").mockImplementation(function (
    this: Element
  ) {
    const rect = layoutOf(this);
    return (rect === null ? [] : [rect]) as unknown as DOMRectList;
  });
  const restoreFocus = emulateChromiumFocusFixup();
  return () => {
    restoreFocus();
    rects.mockRestore();
    bounds.mockRestore();
  };
}

type Draw<S> = (state: S, set: (state: S) => void) => ReactNode;

/** Hands a case the fixture's state setter. */
interface Handle<S> {
  set: (state: S) => void;
}

/** Details as the keeper sees it: a box 560 px tall at the window's top, with its heading and its
 *  Close, that draws the rest from its state; beside it, a control of the page. */
function Fixture<S>({
  active,
  draw,
  handle,
  hidden,
  initial,
}: {
  active: boolean;
  draw: Draw<S>;
  handle: Handle<S>;
  hidden: boolean;
  initial: S;
}): ReactNode {
  const box = useRef<HTMLElement | null>(null);
  const [state, setState] = useState(initial);
  handle.set = setState;
  useDetailsFocusKeeper(box, active);
  return (
    <>
      <button data-top={0} type="button">
        Page control
      </button>
      <aside aria-label="Details" data-height={560} data-top={0} hidden={hidden} ref={box}>
        <h2 data-top={0}>Details</h2>
        <button data-top={24} type="button">
          Close
        </button>
        {draw(state, setState)}
      </aside>
    </>
  );
}

/** What a case does with the box it drew. */
interface Drawn<S> {
  /** Sets the box's state, then waits until every observer and microtask it set off has run. */
  change: (state: S) => Promise<void>;
  /** Draws the box again with the keeper turned on or off, or the box hidden. */
  redraw: (props: { active?: boolean; hidden?: boolean }) => Promise<void>;
  unmount: () => void;
}

function drawBox<S>(active: boolean, initial: S, draw: Draw<S>): Drawn<S> {
  const handle: Handle<S> = { set: () => undefined };
  let props = { active, hidden: false };
  const fixture = () => (
    <Fixture
      active={props.active}
      draw={draw}
      handle={handle}
      hidden={props.hidden}
      initial={initial}
    />
  );
  const view = render(fixture());
  return {
    change: async (state) => {
      await act(async () => handle.set(state));
      await settled();
    },
    redraw: async (next) => {
      props = { ...props, ...next };
      view.rerender(fixture());
      await settled();
    },
    unmount: () => view.unmount(),
  };
}

/** Waits a task, by which every observer callback and microtask the last change set off has run. */
async function settled(): Promise<void> {
  await act(async () => {
    const { promise, resolve } = Promise.withResolvers<void>();
    setTimeout(resolve, 0);
    await promise;
  });
}

/** Where the focus is: its control's name, and the history entry the control stands in. */
function focusName(): string {
  const focus = document.activeElement;
  if (!(focus instanceof HTMLElement) || focus === document.body) return PAGE;
  const name = focus.getAttribute("aria-label") ?? focus.textContent?.trim() ?? "";
  const entry = focus.closest("[data-history-entry]")?.getAttribute("data-history-entry") ?? null;
  return entry === null ? name : `${name} in ${entry}`;
}

/** Save turns disabled while its write is out. Discard, below it, stands nearer, but under the
 *  box's lower edge, out of sight. */
async function saveWhileWriting(active: boolean): Promise<string> {
  const box = drawBox(active, false, (writing) => (
    <>
      <button data-top={420} type="button">
        Edit title
      </button>
      <button data-top={500} disabled={writing} type="button">
        Save
      </button>
      <button data-top={570} type="button">
        Discard
      </button>
    </>
  ));
  try {
    screen.getByRole("button", { name: "Save" }).focus();
    await box.change(true);
    return focusName();
  } finally {
    box.unmount();
  }
}

test("a control disabled while its write is out hands the focus to the nearest control in sight", async () => {
  const restore = chromiumLayout();
  try {
    expect(await saveWhileWriting(false), "with no keeper the focus falls to the page").toBe(PAGE);
    expect(await saveWhileWriting(true), "Discard is nearer, but out of sight").toBe("Edit title");
  } finally {
    restore();
  }
});

/** A turn's controls: Collapse; Show more, which gives way to a loading line while the rest of the
 *  turn is read; a line that counts the turns left; Copy link, further down. With `handsOn`, Copy
 *  link takes the focus itself as the loading line is drawn (`CopyLinkTakesFocus`). */
function showMore(loading: boolean, handsOn = false): ReactNode {
  return (
    <>
      <button data-top={100} type="button">
        Collapse
      </button>
      {loading ? (
        <p data-top={140}>Loading…</p>
      ) : (
        <button data-top={140} type="button">
          Show more
        </button>
      )}
      <p data-top={170}>3 more turns</p>
      {handsOn ? (
        <CopyLinkTakesFocus loading={loading} />
      ) : (
        <button data-top={300} type="button">
          Copy link
        </button>
      )}
    </>
  );
}

/** A site's own hand-on: as Show more gives way to its loading line, a layout effect of that same
 *  commit gives Copy link the focus. */
function CopyLinkTakesFocus({ loading }: { loading: boolean }): ReactNode {
  const button = useRef<HTMLButtonElement>(null);
  useLayoutEffect(() => {
    if (loading) button.current?.focus();
  }, [loading]);
  return (
    <button data-top={300} ref={button} type="button">
      Copy link
    </button>
  );
}

async function showMoreLoading(active: boolean): Promise<string> {
  const box = drawBox(active, false, (loading) => showMore(loading));
  try {
    screen.getByRole("button", { name: "Show more" }).focus();
    await box.change(true);
    return focusName();
  } finally {
    box.unmount();
  }
}

test("a control replaced by a loading line hands the focus to the nearest control that stays", async () => {
  const restore = chromiumLayout();
  try {
    expect(await showMoreLoading(false), "with no keeper the focus falls to the page").toBe(PAGE);
    expect(await showMoreLoading(true)).toBe("Collapse");
  } finally {
    restore();
  }
});

/** Three turns of the history, each with Reply and Pin. `drawing` keys the middle one, so a new
 *  drawing remounts it as new elements under the same `data-history-entry`. */
function turns(drawing: number): ReactNode {
  return (
    <ol data-height={180} data-top={60}>
      {[0, 1, 2].map((turn) => (
        <li
          data-height={60}
          data-history-entry={`turn:${turn}`}
          data-top={60 + turn * 60}
          key={turn === 1 ? `turn-1-${drawing}` : `turn-${turn}`}
        >
          <button data-top={70 + turn * 60} type="button">
            Reply
          </button>
          <button data-top={94 + turn * 60} type="button">
            Pin
          </button>
        </li>
      ))}
    </ol>
  );
}

async function entryRemounted(active: boolean): Promise<string> {
  const box = drawBox(active, 0, (drawing) => turns(drawing));
  try {
    within(screen.getAllByRole("listitem")[1]).getByRole("button", { name: "Pin" }).focus();
    await box.change(1);
    return focusName();
  } finally {
    box.unmount();
  }
}

test("an entry remounted under a new key hands the focus to the same control in the new entry", async () => {
  const restore = chromiumLayout();
  try {
    expect(await entryRemounted(false), "with no keeper the focus falls to the page").toBe(PAGE);
    // The new entry's Reply stands as near to where the lost Pin stood, and comes first.
    expect(await entryRemounted(true)).toBe("Pin in turn:1");
  } finally {
    restore();
  }
});

/** The log's Retry, disabled while its read is out: the reader presses it, the read fails, they
 *  press it again, and it fails again. Returns where the focus is after each step. */
async function retryFailsTwice(active: boolean): Promise<string[]> {
  const box = drawBox(active, false, (reading, setReading) => (
    <>
      <button data-top={300} type="button">
        Copy link
      </button>
      <p data-top={340}>The history could not be read.</p>
      <button data-top={364} disabled={reading} onClick={() => setReading(true)} type="button">
        Retry
      </button>
    </>
  ));
  const seen: string[] = [];
  const press = async () => {
    await userEvent.keyboard("{Enter}");
    await settled();
    seen.push(focusName());
  };
  const fail = async () => {
    await box.change(false);
    seen.push(focusName());
  };
  try {
    screen.getByRole("button", { name: "Retry" }).focus();
    await press();
    await fail();
    await press();
    await fail();
    return seen;
  } finally {
    box.unmount();
  }
}

test("a Retry that fails twice never leaves the focus on the page, and gets it back each time", async () => {
  const restore = chromiumLayout();
  try {
    expect(await retryFailsTwice(false), "with no keeper the focus falls to the page").toEqual([
      PAGE,
      PAGE,
      PAGE,
      PAGE,
    ]);
    expect(await retryFailsTwice(true)).toEqual(["Copy link", "Retry", "Copy link", "Retry"]);
  } finally {
    restore();
  }
});

/** Two turns of the log and, at its end, Load older, which goes away once the log's last page is
 *  read, with no control drawn in its place. */
function logEnd(more: boolean): ReactNode {
  return (
    <>
      <ol data-height={80} data-top={60}>
        <li data-height={40} data-history-entry="turn:1" data-top={60}>
          <button data-top={64} type="button">
            Pin
          </button>
        </li>
        <li data-height={40} data-history-entry="turn:2" data-top={100}>
          <button data-top={104} type="button">
            Pin
          </button>
        </li>
      </ol>
      {more ? (
        <button data-top={148} type="button">
          Load older
        </button>
      ) : null}
    </>
  );
}

async function loadOlderGone(active: boolean): Promise<string> {
  const box = drawBox(active, true, (more) => logEnd(more));
  try {
    screen.getByRole("button", { name: "Load older" }).focus();
    await box.change(false);
    return focusName();
  } finally {
    box.unmount();
  }
}

test("Load older removed at the log's end hands the focus to the control just above it, not the heading", async () => {
  const restore = chromiumLayout();
  try {
    expect(await loadOlderGone(false), "with no keeper the focus falls to the page").toBe(PAGE);
    expect(await loadOlderGone(true)).toBe("Pin in turn:2");
  } finally {
    restore();
  }
});

/** Show more holds the focus, the reader moves it with `move`, and then Show more gives way to its
 *  loading line. */
async function movedThenLoading(move: () => void): Promise<string> {
  const box = drawBox(true, false, (loading) => showMore(loading));
  try {
    screen.getByRole("button", { name: "Show more" }).focus();
    move();
    await settled();
    await box.change(true);
    return focusName();
  } finally {
    box.unmount();
  }
}

test("a focus the reader moved elsewhere stays where they put it", async () => {
  const restore = chromiumLayout();
  try {
    expect(
      await movedThenLoading(() => screen.getByRole("button", { name: "Copy link" }).focus()),
      "to another control of Details"
    ).toBe("Copy link");
    expect(
      await movedThenLoading(() => screen.getByRole("button", { name: "Page control" }).focus()),
      "to a control of the page beside Details"
    ).toBe("Page control");
    expect(
      await movedThenLoading(() => screen.getByRole("button", { name: "Show more" }).blur()),
      "to the page, by a press on no control"
    ).toBe(PAGE);
  } finally {
    restore();
  }
});

test("a site's own hand-on, made in the commit that changed the box, keeps the focus it gave", async () => {
  const restore = chromiumLayout();
  const box = drawBox(true, false, (loading) => showMore(loading, true));
  try {
    screen.getByRole("button", { name: "Show more" }).focus();
    await box.change(true);
    expect(focusName(), "the keeper's own choice, Collapse, is nearer").toBe("Copy link");
  } finally {
    box.unmount();
    restore();
  }
});

test("the keeper does nothing while it is turned off or the box is hidden", async () => {
  const restore = chromiumLayout();
  try {
    const off = drawBox(true, false, (loading) => showMore(loading));
    try {
      screen.getByRole("button", { name: "Show more" }).focus();
      await off.redraw({ active: false });
      await off.change(true);
      expect(focusName(), "turned off after it noted Show more").toBe(PAGE);
    } finally {
      off.unmount();
    }
    const hidden = drawBox(true, false, (loading) => showMore(loading));
    try {
      screen.getByRole("button", { name: "Show more" }).focus();
      await hidden.redraw({ hidden: true });
      await hidden.change(true);
      expect(focusName(), "Show more removed while the box is hidden").toBe(PAGE);
    } finally {
      hidden.unmount();
    }
  } finally {
    restore();
  }
});
