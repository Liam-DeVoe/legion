import { afterAll, afterEach, beforeAll, expect, test } from "bun:test";

import { hear, pageOf, restoreTheBrowser } from "../../__tests__/readers-place-harness";
import { appKeymap } from "../shell/keymap";
import { keepTheReadersPlace, keepWhatStandsUnder, moveTheBox } from "./readers-place";
import { followThePress, movesTheReader, whatItDoes } from "./readers-scroll";

afterEach(restoreTheBrowser);

// The page's own shortcuts, as the issue page registers them: `s` focuses the issue's Status,
// which scrolls the page to it, `1` writes the priority, and `t c` switches to the Conversation
// tab. Registered in the application's registry, which is the one the table reads
// (`effectOf`, `features/shell/keymap.ts`); no listener runs them here, as none needs to.
let unregister: (() => void) | undefined;
beforeAll(() => {
  unregister = appKeymap.register("global", [
    { effect: "moves the reader", id: "status", keys: "s", label: "Focus the status", run() {} },
    { effect: "moves nothing", id: "set-priority", keys: "1", label: "Set priority", run() {} },
    {
      effect: "moves the reader",
      id: "tab-conversation",
      keys: "t c",
      label: "Conversation tab",
      run() {},
    },
  ]);
});
afterAll(() => unregister?.());

// An engine that animates the scroll a wheel asks for - WebKit does - stops it at the first
// scroll a script makes, so a correction made while it is still moving ends the reader's scroll
// wherever it stands: wheeled 2,000 px up, WebKit stopped 42 px in for a correction of 1.6 px
// (`e2e/deep-links.e2e.ts`, "a press in the document while a comment link is still landing does
// not end the hold").
test("a growth above a reader whose own scroll is still moving leaves that scroll alone", () => {
  const page = pageOf(40, 0);
  page.scrollTo(1_250);
  const release = keepTheReadersPlace(page.drawn);
  try {
    page.wheel();
    // The first frame of the scroll the engine animates for that wheel, and a block above the
    // reader drawn at its own height in the same frame.
    page.scrollTo(1_200);
    page.frame();
    page.grow(3, 2);
    page.resized();

    expect(page.scrollY()).toBe(1_200);
  } finally {
    release();
  }
});

test("once the reader's own scroll has held still for two frames, a growth above them is answered", () => {
  const page = pageOf(40, 0);
  page.scrollTo(1_250);
  const release = keepTheReadersPlace(page.drawn);
  try {
    page.wheel();
    page.scrollTo(1_200);
    page.frame();
    page.scrollTo(1_000);
    page.frame();
    // One frame with no scroll in it is not yet the end of the movement.
    page.frame();
    page.grow(3, 2);
    page.resized();
    expect(page.scrollY()).toBe(1_000);

    page.frame();
    page.grow(3, 30);
    page.resized();

    expect(page.scrollY()).toBe(1_030);
  } finally {
    release();
  }
});

test("a box kept above a reader whose own scroll is still moving leaves that scroll alone", () => {
  const page = pageOf(40, 0);
  page.scrollTo(1_250);
  let height = 120;
  // The space the page holds for its chrome, out of sight above the top of the reader's screen.
  const held = document.createElement("div");
  held.getBoundingClientRect = () => ({ bottom: -100 + height, height, top: -100 }) as DOMRect;
  document.body.append(held);
  const release = keepWhatStandsUnder(held);
  try {
    page.wheel();
    page.scrollTo(1_200);
    page.frame();
    height = 180;
    page.resized();

    expect(page.scrollY()).toBe(1_200);
  } finally {
    release();
    held.remove();
  }
});

/** The reader presses a key, which every listener the module holds for it hears. */
function key(): void {
  hear(new Event("keydown"));
}

// The rows of the table's entry for a keydown no keyboard made: a script's `keydown`, with no key
// to name, moves nothing, so both keepers stay at work through it.
//
// A key that scrolls nothing - a letter typed into an answer box, Shift, a copy - starts no
// movement of the reader's for a correction to end, so the page under them is still this
// module's to keep, in the task the growth is seen in as on a quiet page. Driven in a browser
// with its own scroll anchoring off, on `e2e/large-documents-first-screen.e2e.ts`'s "a panel
// arriving while the reader is in a long document moves what they are looking at by no pixels"
// with Shift pressed as the panel landed: the reader's words moved the panel's 88 px in three
// runs of six, and by under half a pixel in six of six once a key no longer opened the window.
test("a growth above a reader who pressed a key that scrolled nothing is answered", () => {
  const page = pageOf(40, 0);
  page.scrollTo(1_250);
  const release = keepTheReadersPlace(page.drawn);
  try {
    key();
    page.grow(3, 332);
    page.resized();

    expect(page.scrollY()).toBe(1_582);
  } finally {
    release();
  }
});

test("a box kept above a reader who pressed a key that scrolled nothing is answered", () => {
  const page = pageOf(40, 0);
  page.scrollTo(1_250);
  let height = 120;
  // The space the page holds for its chrome, out of sight above the top of the reader's screen.
  const held = document.createElement("div");
  held.getBoundingClientRect = () => ({ bottom: -100 + height, height, top: -100 }) as DOMRect;
  document.body.append(held);
  const release = keepWhatStandsUnder(held);
  try {
    key();
    height = 180;
    page.resized();

    expect(page.scrollY()).toBe(1_310);
  } finally {
    release();
    held.remove();
  }
});

/** A key going down as a browser reports it: the key named, the modifiers held with it, and the
 *  box it was pressed into - the page itself where nothing has the focus. This harness holds the
 *  module's listeners and calls them itself, so the target a browser gives the event is stated. */
function keydown(key: string, held: KeyboardEventInit = {}, into: Element = document.body): Event {
  const event = new KeyboardEvent("keydown", { ...held, key });
  Object.defineProperty(event, "target", { value: into });
  return event;
}

/** The reader presses that key, which every listener the module holds for a key hears. */
function press(key: string, held?: KeyboardEventInit, into?: Element): void {
  hear(keydown(key, held, into));
}

/** The reader presses that key on the page, which hears it as the browser hands it on: the
 *  watches on the capture phase first, then the registry's own listener (`KeymapProvider`),
 *  which takes a shortcut's key and moves a chord on. */
function pressOnThePage(key: string): void {
  const event = keydown(key);
  hear(event);
  appKeymap.handleKeyDown(event as KeyboardEvent);
}

/** A pointer going down as a browser reports it, of the kind `pointerType` names. */
function pointerdown(pointerType: "mouse" | "pen" | "touch"): Event {
  const event = new Event("pointerdown");
  Object.defineProperty(event, "pointerType", { value: pointerType });
  return event;
}

/** A box that takes what is typed into it, as the editor does. */
function anEditor(): HTMLElement {
  const editor = document.createElement("div");
  editor.contentEditable = "true";
  return editor;
}

/** An `<input>` of `type`: a decision block's options are checkboxes and radios
 *  (`features/inbox/AskOptionRow.tsx`). */
function anInput(type: string): HTMLInputElement {
  const input = document.createElement("input");
  input.type = type;
  return input;
}

/** A link the reader has tabbed to. */
function aLink(): HTMLAnchorElement {
  const link = document.createElement("a");
  link.href = "#b-block";
  return link;
}

/**
 * One input of each entry of the table of the reader's inputs (`THE_READERS_INPUTS`,
 * `readers-scroll.ts`), named by the entry it is, and whether it moves the reader - stated here
 * rather than read from the table, so an entry reverted to the other effect turns its rows red.
 * An entry whose effect is unknown until it moves is read as moving nothing at its start, which
 * is the whole of what that effect asks of the keepers: a press or a finger that has gone down
 * and not moved leaves them at work.
 */
const INPUTS: readonly { entry: string; moves: boolean; name: string; act(): void }[] = [
  {
    act: () => hear(pointerdown("touch")),
    entry: "a press of a mouse, a pen or a finger",
    moves: false,
    name: "a finger's pointerdown",
  },
  {
    act: () => hear(pointerdown("mouse")),
    entry: "a press of a mouse, a pen or a finger",
    moves: false,
    name: "a click",
  },
  {
    act: () => {
      hear(pointerdown("mouse"));
      hear(new Event("scroll"));
    },
    entry: "a scroll while a press is held",
    moves: true,
    name: "a press that has begun to scroll",
  },
  {
    act: () => hear(new Event("touchstart")),
    entry: "a finger going down",
    moves: false,
    name: "a touchstart",
  },
  {
    act: () => {
      hear(new Event("touchstart"));
      hear(new Event("touchmove"));
    },
    entry: "a finger moving",
    moves: true,
    name: "a finger that moves",
  },
  { act: () => hear(new Event("wheel")), entry: "a wheel", moves: true, name: "a wheel" },
  {
    act: () => press("s"),
    entry: "a key the page binds to move the reader",
    moves: true,
    name: "a shortcut that focuses a header control",
  },
  {
    act: () => {
      pressOnThePage("t");
      pressOnThePage("c");
    },
    entry: "a key the page binds to move the reader",
    moves: true,
    name: "a tab chord",
  },
  {
    act: () => press("1"),
    entry: "a key the page binds to move nothing",
    moves: false,
    name: "a shortcut that writes the priority",
  },
  {
    act: () => press("t"),
    entry: "a key the page binds to move nothing",
    moves: false,
    name: "the first key of a chord",
  },
  { act: () => press("Tab"), entry: "Tab", moves: true, name: "Tab" },
  { act: () => press("PageDown"), entry: "PageUp or PageDown", moves: true, name: "a scroll key" },
  {
    act: () => press("PageDown", {}, anEditor()),
    entry: "PageUp or PageDown",
    moves: true,
    name: "PageDown in the editor",
  },
  {
    act: () => press("Enter", {}, aLink()),
    entry: "Enter on a link",
    moves: true,
    name: "Enter on a link",
  },
  {
    act: () => press(" ", {}, anEditor()),
    entry: "Space, an arrow, Home or End in a field",
    moves: false,
    name: "Space typed into the editor",
  },
  {
    act: () => press("ArrowDown", {}, document.createElement("textarea")),
    entry: "Space, an arrow, Home or End in a field",
    moves: false,
    name: "an arrow in a comment box",
  },
  {
    act: () => press(" ", {}, document.createElement("button")),
    entry: "Space on a button, a checkbox or a radio",
    moves: false,
    name: "Space on a button",
  },
  {
    act: () => press(" ", {}, anInput("checkbox")),
    entry: "Space on a button, a checkbox or a radio",
    moves: false,
    name: "Space on a checkbox",
  },
  {
    act: () => press(" ", {}, anInput("radio")),
    entry: "Space on a button, a checkbox or a radio",
    moves: false,
    name: "Space on a radio",
  },
  {
    act: () => press("ArrowDown"),
    entry: "Space, an arrow, Home or End anywhere else",
    moves: true,
    name: "an arrow on the page",
  },
  {
    act: () => press("ArrowUp", {}, anInput("checkbox")),
    entry: "Space, an arrow, Home or End anywhere else",
    moves: true,
    name: "an arrow from a checkbox",
  },
  {
    act: () => press("Home", {}, anInput("radio")),
    entry: "Space, an arrow, Home or End anywhere else",
    moves: true,
    name: "Home from a radio",
  },
  {
    act: () => press("End", {}, anInput("button")),
    entry: "Space, an arrow, Home or End anywhere else",
    moves: true,
    name: "End from an input button",
  },
  {
    act: () => press("ArrowDown", {}, document.createElement("button")),
    entry: "Space, an arrow, Home or End anywhere else",
    moves: true,
    name: "an arrow from a button",
  },
  {
    act: () => press("Shift", { shiftKey: true }),
    entry: "every other key",
    moves: false,
    name: "a modifier",
  },
  {
    act: () => press("e", {}, anEditor()),
    entry: "every other key",
    moves: false,
    name: "a letter typed into the editor",
  },
  {
    act: () => {
      press("Control", { ctrlKey: true });
      press("c", { ctrlKey: true });
    },
    entry: "every other key",
    moves: false,
    name: "a copy",
  },
  { act: () => press("Escape"), entry: "every other key", moves: false, name: "Escape" },
];

for (const input of INPUTS) {
  const outcome = input.moves ? "waits for the movement it begins" : "is answered";

  // A panel's arrival: the document's own keeper, and a panel landing above the reader.
  test(`${input.entry}: a panel landing above a reader after ${input.name} ${outcome}`, () => {
    const page = pageOf(40, 0);
    page.scrollTo(1_250);
    const release = keepTheReadersPlace(page.drawn);
    try {
      input.act();
      page.grow(3, 332);
      page.resized();

      expect(page.scrollY()).toBe(input.moves ? 1_250 : 1_582);
    } finally {
      release();
    }
  });

  // The editor's build: the keeper of the box the page holds for its chrome until the editor's
  // first batch (`AfterTheFirstBatch.tsx`), and the chrome landing in it taller than the box.
  test(`${input.entry}: the chrome landing in the editor's build above a reader after ${input.name} ${outcome}`, () => {
    const page = pageOf(40, 0);
    page.scrollTo(1_250);
    let height = 120;
    const held = document.createElement("div");
    held.getBoundingClientRect = () => ({ bottom: -100 + height, height, top: -100 }) as DOMRect;
    document.body.append(held);
    const release = keepWhatStandsUnder(held);
    try {
      input.act();
      height = 180;
      page.resized();

      expect(page.scrollY()).toBe(input.moves ? 1_250 : 1_310);
    } finally {
      release();
      held.remove();
    }
  });
}

// A key that scrolls is a wheel to the engine that animates it. In Playwright's WebKit 27.2 a
// PageDown ran 576 px over 13 frames alone and stopped after 92 to 110 px with one pixel written
// by a script two frames in, and its first scroll came in the key's own frame or the one after
// it: so the frame after the key may pass with no scroll in it and the keeper still stands down.
test("a growth above a reader whose key-driven scroll is still moving leaves that scroll alone", () => {
  const page = pageOf(40, 0);
  page.scrollTo(1_250);
  const release = keepTheReadersPlace(page.drawn);
  try {
    press("PageDown");
    page.frame();
    // The first frame of the scroll the engine animates for that key, a frame after the key,
    // and a block above the reader drawn at its own height in it.
    page.scrollTo(1_342);
    page.frame();
    page.grow(3, 2);
    page.resized();
    expect(page.scrollY()).toBe(1_342);

    // Two frames with no scroll in them end it, as they end a wheel's.
    page.frame();
    page.frame();
    page.grow(3, 30);
    page.resized();

    expect(page.scrollY()).toBe(1_372);
  } finally {
    release();
  }
});

// A drag's first scroll comes after its first `touchmove`, and momentum keeps the window open
// through its own scroll events: the finger that has gone down is no stand-down, and the finger
// that moves is the same stand-down a wheel is.
test("a growth above a reader whose finger is dragging the page leaves the drag alone", () => {
  const page = pageOf(40, 0);
  page.scrollTo(1_250);
  const release = keepTheReadersPlace(page.drawn);
  try {
    hear(new Event("touchstart"));
    hear(new Event("touchmove"));
    page.scrollTo(1_200);
    page.frame();
    page.grow(3, 2);
    page.resized();
    expect(page.scrollY()).toBe(1_200);

    // The momentum after the finger lifts is the drag's own scroll, frame after frame.
    page.scrollTo(1_100);
    page.frame();
    page.grow(3, 2);
    page.resized();

    expect(page.scrollY()).toBe(1_100);
  } finally {
    release();
  }
});

// A press of a mouse is unknown until it moves, and its movement is a scroll while it is held:
// a drag on the scroll bar, which reaches the page as nothing but its press and then its scroll
// (in Playwright's WebKit the `pointerdown` came 11 to 15 ms before the first scroll). The
// keepers stand down at that first scroll, as they do at a finger's first `touchmove`, and take
// the place up again from where the drag left it once it is let go and still.
test("a growth above a reader dragging the scroll bar leaves the drag alone", () => {
  const page = pageOf(40, 0);
  page.scrollTo(1_250);
  const release = keepTheReadersPlace(page.drawn);
  try {
    hear(pointerdown("mouse"));
    page.scrollTo(1_200);
    page.frame();
    page.grow(3, 2);
    page.resized();
    expect(page.scrollY()).toBe(1_200);

    hear(new Event("pointerup"));
    page.frame();
    page.frame();
    page.grow(3, 30);
    page.resized();

    expect(page.scrollY()).toBe(1_230);
  } finally {
    release();
  }
});

// The page's own place-keeping scrolls while a press may be held - the reader holding the
// button on a word as a panel lands - and that scroll moved nobody: read as the press's
// movement it would stand the keepers down for a growth the page itself is answering. The record
// of where the page left the box says which scroll is its own (`thePageLeftItThere`).
test("a scroll the page itself made while a press is held is not the press's movement", () => {
  const scrollY = Object.getOwnPropertyDescriptor(window, "scrollY");
  const scrollBy = window.scrollBy;
  let top = 0;
  Object.defineProperty(window, "scrollY", { configurable: true, get: () => top });
  window.scrollBy = ((_x: number, y: number) => {
    top += y;
  }) as never;
  const releaseThePress = followThePress();
  try {
    window.dispatchEvent(pointerdown("mouse"));
    const scroll = new Event("scroll");
    top = 400;
    expect(whatItDoes(scroll)).toBe("moves the reader");

    moveTheBox(null, 88);
    expect(whatItDoes(scroll)).toBe("moves nothing");

    window.dispatchEvent(new Event("pointerup"));
    top = 900;
    expect(whatItDoes(scroll)).toBe("moves nothing");
  } finally {
    releaseThePress();
    window.scrollBy = scrollBy;
    if (scrollY !== undefined) Object.defineProperty(window, "scrollY", scrollY);
  }
});

// An input that moves the reader opens the keepers' window before anything has scrolled, and
// some of them scroll nothing after all: a wheel at the end of the page, a zoom with Control
// held, Tab to a control already on screen, PageDown at the end. Dropping what grew while that
// window stood left the growth on the reader with nothing to move it back; the window closing
// with no scroll seen answers it against the place held before it opened.
test("a panel landing above a reader whose key scrolled nothing is answered once the window closes", () => {
  const page = pageOf(40, 0);
  page.scrollTo(1_250);
  const release = keepTheReadersPlace(page.drawn);
  try {
    press("PageDown");
    page.grow(3, 332);
    page.resized();
    expect(page.scrollY()).toBe(1_250);

    // The key's own frame, then the two still frames that close the window.
    page.frame();
    page.frame();
    expect(page.scrollY()).toBe(1_250);
    page.frame();

    expect(page.scrollY()).toBe(1_582);
  } finally {
    release();
  }
});

test("the chrome landing above a reader whose wheel scrolled nothing is answered once the window closes", () => {
  const page = pageOf(40, 0);
  page.scrollTo(1_250);
  let height = 120;
  const held = document.createElement("div");
  held.getBoundingClientRect = () => ({ bottom: -100 + height, height, top: -100 }) as DOMRect;
  document.body.append(held);
  const release = keepWhatStandsUnder(held);
  try {
    page.wheel();
    height = 180;
    page.resized();
    expect(page.scrollY()).toBe(1_250);

    page.frame();
    page.frame();
    page.frame();

    expect(page.scrollY()).toBe(1_310);
  } finally {
    release();
    held.remove();
  }
});

/** What `movesTheReader` reads each of `keys` as, pressed with `held` into `into`. */
function whichMove(
  keys: readonly string[],
  into?: Element,
  held?: KeyboardEventInit
): Record<string, boolean> {
  return Object.fromEntries(keys.map((key) => [key, movesTheReader(keydown(key, held, into))]));
}

// The one reading of a key that both watches make: a key moves the reader where its own default
// action scrolls or takes the focus somewhere else. Measured in Playwright's WebKit 27.2 and
// Chromium 156, three presses each on a probe page: Space, an arrow, Home and End scrolled the
// page from the page and moved it by 0 px from an editable box, PageDown moved it from both, and
// Shift and a letter moved it from neither.
test("a key pressed on the page moves the reader where its own action scrolls or moves the focus", () => {
  expect(
    whichMove([
      " ",
      "ArrowDown",
      "ArrowLeft",
      "ArrowRight",
      "ArrowUp",
      "End",
      "Home",
      "PageDown",
      "PageUp",
      "Tab",
    ])
  ).toEqual({
    " ": true,
    ArrowDown: true,
    ArrowLeft: true,
    ArrowRight: true,
    ArrowUp: true,
    End: true,
    Home: true,
    PageDown: true,
    PageUp: true,
    Tab: true,
  });
  expect(whichMove(["Alt", "Control", "Enter", "Escape", "Meta", "Shift", "e"])).toEqual({
    Alt: false,
    Control: false,
    Enter: false,
    Escape: false,
    Meta: false,
    Shift: false,
    e: false,
  });
  // A copy, a paste and a select-all are a letter with a modifier held.
  expect(whichMove(["a", "c", "v"], document.body, { ctrlKey: true })).toEqual({
    a: false,
    c: false,
    v: false,
  });
  expect(whichMove(["a", "c", "v"], document.body, { metaKey: true })).toEqual({
    a: false,
    c: false,
    v: false,
  });
  // Enter follows a link, and presses nothing on the page itself.
  expect(whichMove(["Enter"], aLink())).toEqual({ Enter: true });
});

test("a key typed into a field moves the reader only where it leaves the field or pages it", () => {
  const fields: Record<string, HTMLElement> = {
    "a comment box": document.createElement("textarea"),
    "a search box": anInput("search"),
    "a select": document.createElement("select"),
    "an answer box": document.createElement("input"),
    "the editor": anEditor(),
  };
  for (const [name, field] of Object.entries(fields)) {
    expect({
      [name]: whichMove(
        [" ", "ArrowDown", "ArrowUp", "End", "Home", "PageDown", "PageUp", "Tab", "e"],
        field
      ),
    }).toEqual({
      [name]: {
        " ": false,
        ArrowDown: false,
        ArrowUp: false,
        End: false,
        Home: false,
        PageDown: true,
        PageUp: true,
        Tab: true,
        e: false,
      },
    });
  }
});

// A checkbox, a radio and a button take no typed text, and they are not fields: on a probe page
// in both engines the arrows, Home and End scroll the page from a checkbox or an
// `input[type=button]`, and Home and End from a radio, whose arrows step its group's checked
// option and the focus with it (the review of round 11). Space presses or ticks each, and
// scrolls nothing. A decision block's options are these inputs (`AskOptionRow.tsx`).
test("from a checkbox, a radio or a button the keys that scroll the page move the reader, and Space does not", () => {
  const controls: Record<string, HTMLElement> = {
    "a button": document.createElement("button"),
    "a checkbox": anInput("checkbox"),
    "a radio": anInput("radio"),
    "an input button": anInput("button"),
  };
  for (const [name, control] of Object.entries(controls)) {
    expect({
      [name]: whichMove(
        [" ", "ArrowDown", "ArrowUp", "End", "Home", "PageDown", "Tab", "e"],
        control
      ),
    }).toEqual({
      [name]: {
        " ": false,
        ArrowDown: true,
        ArrowUp: true,
        End: true,
        Home: true,
        PageDown: true,
        Tab: true,
        e: false,
      },
    });
  }
});

test("a press or a finger going down cannot tell yet, and one that moves does", () => {
  expect({
    "a finger's pointerdown": whatItDoes(pointerdown("touch")),
    "a keydown no keyboard made": whatItDoes(new Event("keydown")),
    "a mouse's pointerdown": whatItDoes(pointerdown("mouse")),
    "a pen's pointerdown": whatItDoes(pointerdown("pen")),
    "a scroll with no press held": whatItDoes(new Event("scroll")),
    "a touchmove": whatItDoes(new Event("touchmove")),
    "a touchstart": whatItDoes(new Event("touchstart")),
    "a wheel": whatItDoes(new Event("wheel")),
  }).toEqual({
    "a finger's pointerdown": "unknown until it moves",
    "a keydown no keyboard made": "moves nothing",
    "a mouse's pointerdown": "unknown until it moves",
    "a pen's pointerdown": "unknown until it moves",
    "a scroll with no press held": "moves nothing",
    "a touchmove": "moves the reader",
    "a touchstart": "unknown until it moves",
    "a wheel": "moves the reader",
  });
});

// The page's own keys are read by what each declares where it is bound, not as the letter they
// are: `s` focuses the Status, whose focus scrolls the page to the header, and a chord switches
// the tab; a digit writes the priority and moves nobody.
test("a key the page binds reads as its binding declares, and the same letter unbound as a letter", () => {
  expect({
    "a digit the page binds": whatItDoes(keydown("1")),
    "a letter the page binds": whatItDoes(keydown("s")),
    "a letter the page leaves alone": whatItDoes(keydown("q")),
    "the letter it binds, typed into a field": whatItDoes(keydown("s", {}, anEditor())),
  }).toEqual({
    "a digit the page binds": "moves nothing",
    "a letter the page binds": "moves the reader",
    "a letter the page leaves alone": "moves nothing",
    "the letter it binds, typed into a field": "moves nothing",
  });
});
