/**
 * The browser a unit test of the reader's place stands in for (`features/doc/readers-place.ts`,
 * `features/doc/readers-scroll.ts`). happy-dom lays nothing out and has no ResizeObserver, so the
 * geometry is this harness: a run of blocks `BLOCK_HEIGHT` tall whose first block starts
 * `documentTop` under the top of the scrollport, a page that scrolls by `window.scrollY` or a
 * column that scrolls by its own `scrollTop`, a `ResizeObserver` the rows fire, the listeners the
 * modules register on the window held here for the rows to call, and animation frames that pass
 * when a row says so. A file that uses it calls `restoreTheBrowser` after each row.
 */

export const BLOCK_HEIGHT = 100;

/** The page's own placing port, which a browser reads as `document.documentElement.clientHeight`
 *  - the window less a horizontal scrollbar - and happy-dom lays out as 0. A row that is not
 *  about that scrollbar reads the window's own height, as a page with no horizontal scrollbar
 *  does; a row that is sets `height` and has it put back after it. */
export const thePagePort = { height: window.innerHeight };
Object.defineProperty(document.documentElement, "clientHeight", {
  configurable: true,
  get: () => thePagePort.height,
});

export interface Page {
  readonly drawn: HTMLElement;
  /** Grows the document by `pixels` above block `index`, as a panel landing there does. */
  grow(index: number, pixels: number): void;
  /** What the reader does: the page scrolls and tells whoever is listening. */
  scrollTo(top: number): void;
  /** Fires every live `ResizeObserver`, as the browser does after it has laid the page out. */
  resized(): void;
  /** The reader turns the wheel: the page hears of it before the scroll it starts. */
  wheel(): void;
  /** An animation frame passes: every callback asked for before it runs. */
  frame(): void;
  readonly scrollY: () => number;
}

const observers = new Set<() => void>();
let restoreObserver: (() => void) | undefined;
let scrollListeners: ((event: Event) => void)[] = [];
/** Every other listener the modules register on the window, by event type. */
let windowListeners = new Map<string, ((event: Event) => void)[]>();
/** The animation frame callbacks asked for and not yet run. */
let frames = new Map<number, FrameRequestCallback>();
/** What a row put on the page, taken off it again when the row ends: the drawn document on a
 *  document page, the column carrying it in a reading pane. */
let mounted: HTMLElement | undefined;

export function pageOf(blocks: number, documentTop: number): Page {
  let scrollY = 0;
  // How much taller than `BLOCK_HEIGHT` each block is, which is what a panel landing in it adds.
  const grown = new Map<number, number>();
  const topOf = (index: number) => {
    let top = documentTop;
    for (let above = 0; above < index; above += 1) {
      top += BLOCK_HEIGHT + (grown.get(above) ?? 0);
    }
    return top - scrollY;
  };
  const drawn = document.createElement("div");
  for (let index = 0; index < blocks; index += 1) {
    const block = document.createElement("section");
    block.getBoundingClientRect = () =>
      ({
        bottom: topOf(index) + BLOCK_HEIGHT + (grown.get(index) ?? 0),
        top: topOf(index),
      }) as DOMRect;
    drawn.append(block);
  }
  document.body.append(drawn);
  // How tall the page stands, which is what tells a scroll made to answer for a growth from
  // one the reader made (`pageHeight`).
  Object.defineProperty(document.documentElement, "scrollHeight", {
    configurable: true,
    get: () => topOf(blocks) + scrollY,
  });
  const page: Page = {
    drawn,
    frame,
    grow(index, pixels) {
      grown.set(index, (grown.get(index) ?? 0) + pixels);
    },
    resized() {
      for (const fire of [...observers]) fire();
    },
    scrollTo(top) {
      scrollY = top;
      for (const listener of scrollListeners) listener(new Event("scroll"));
    },
    scrollY: () => scrollY,
    wheel,
  };
  installTheBrowser(page);
  mounted = page.drawn;
  return page;
}

/** Where the Inbox's reading column stands on the page, and how tall it is: the column is a box
 *  of its own, so the first line of the document a reader can see is its top edge and not the
 *  window's. */
export const COLUMN_TOP = 200;
export const COLUMN_HEIGHT = 600;

export interface Column extends Page {
  /** How far down the column the reader is, which is what the column scrolls by. */
  readonly scrollTop: () => number;
}

/** The same run of blocks drawn in that column. happy-dom lays nothing out, computes no
 *  overflow and scrolls nothing, so what `whatScrolls` reads to find the box - its overflow and
 *  its content standing taller than it does - is stated here, as are the rectangles. */
export function columnOf(blocks: number, documentTop: number): Column {
  let scrolled = 0;
  const grown = new Map<number, number>();
  const topOf = (index: number) => {
    let top = COLUMN_TOP + documentTop;
    for (let above = 0; above < index; above += 1) {
      top += BLOCK_HEIGHT + (grown.get(above) ?? 0);
    }
    return top - scrolled;
  };
  const column = document.createElement("div");
  column.style.overflowY = "auto";
  column.getBoundingClientRect = () =>
    ({ bottom: COLUMN_TOP + COLUMN_HEIGHT, height: COLUMN_HEIGHT, top: COLUMN_TOP }) as DOMRect;
  Object.defineProperty(column, "clientHeight", { configurable: true, get: () => COLUMN_HEIGHT });
  Object.defineProperty(column, "scrollHeight", {
    configurable: true,
    get: () => topOf(blocks) + scrolled - COLUMN_TOP,
  });
  const drawn = document.createElement("div");
  for (let index = 0; index < blocks; index += 1) {
    const block = document.createElement("section");
    block.getBoundingClientRect = () =>
      ({
        bottom: topOf(index) + BLOCK_HEIGHT + (grown.get(index) ?? 0),
        top: topOf(index),
      }) as DOMRect;
    drawn.append(block);
  }
  column.append(drawn);
  document.body.append(column);
  const page: Column = {
    drawn,
    frame,
    grow(index, pixels) {
      grown.set(index, (grown.get(index) ?? 0) + pixels);
    },
    resized() {
      for (const fire of [...observers]) fire();
    },
    scrollTo(top) {
      scrolled = top;
      for (const listener of scrollListeners) listener(new Event("scroll"));
    },
    scrollTop: () => scrolled,
    scrollY: () => 0,
    wheel,
  };
  // The column's own scroll, which is what the keeper writes (`moveTheBox`): the browser tells
  // the listeners after the write, and this harness tells them as the page's own does.
  Object.defineProperty(column, "scrollTop", {
    configurable: true,
    get: () => scrolled,
    set: (next: number) => page.scrollTo(next),
  });
  installTheBrowser(page);
  mounted = column;
  return page;
}

/** The reader does `event`, which every listener the modules hold for its type hears - a
 *  scroll's among them, which this harness holds apart for the page's own `scrollTo`. */
export function hear(event: Event): void {
  const listeners = event.type === "scroll" ? scrollListeners : windowListeners.get(event.type);
  for (const listener of listeners ?? []) listener(event);
}

/** The reader turns the wheel, which every listener the modules hold for it hears. */
export function wheel(): void {
  hear(new Event("wheel"));
}

/** An animation frame passes: what was asked for before it runs, and what that asks for waits
 *  for the next one. */
export function frame(): void {
  const due = [...frames.values()];
  frames = new Map();
  for (const callback of due) callback(performance.now());
}

/** The browser's own pieces the modules reach for: `ResizeObserver`, the page's scroll offset,
 *  `window.scrollBy`, the listeners they register and the animation frames they ask for. */
export function installTheBrowser(page: Page): void {
  const original = globalThis.ResizeObserver;
  const scrollBy = window.scrollBy;
  const scrollY = Object.getOwnPropertyDescriptor(window, "scrollY");
  const addListener = window.addEventListener;
  const removeListener = window.removeEventListener;
  const requestFrame = window.requestAnimationFrame;
  const cancelFrame = window.cancelAnimationFrame;
  scrollListeners = [];
  windowListeners = new Map();
  frames = new Map();
  class ObserverStub {
    private readonly fire: () => void;
    constructor(callback: ResizeObserverCallback) {
      this.fire = () => callback([], this as unknown as ResizeObserver);
      observers.add(this.fire);
    }
    disconnect(): void {
      observers.delete(this.fire);
    }
    observe(): void {}
    unobserve(): void {}
  }
  globalThis.ResizeObserver = ObserverStub as unknown as typeof ResizeObserver;
  Object.defineProperty(window, "scrollY", { configurable: true, get: page.scrollY });
  window.scrollBy = ((_x: number, y: number) => page.scrollTo(page.scrollY() + y)) as never;
  window.addEventListener = ((type: string, listener: (event: Event) => void) => {
    if (type === "scroll") {
      scrollListeners.push(listener);
      return;
    }
    windowListeners.set(type, [...(windowListeners.get(type) ?? []), listener]);
  }) as never;
  window.removeEventListener = ((type: string, listener: (event: Event) => void) => {
    if (type === "scroll") {
      scrollListeners = scrollListeners.filter((kept) => kept !== listener);
      return;
    }
    windowListeners.set(
      type,
      (windowListeners.get(type) ?? []).filter((kept) => kept !== listener)
    );
  }) as never;
  let lastFrame = 0;
  window.requestAnimationFrame = (callback: FrameRequestCallback) => {
    lastFrame += 1;
    frames.set(lastFrame, callback);
    return lastFrame;
  };
  window.cancelAnimationFrame = (handle: number) => {
    frames.delete(handle);
  };
  restoreObserver = () => {
    globalThis.ResizeObserver = original;
    window.scrollBy = scrollBy;
    window.addEventListener = addListener;
    window.removeEventListener = removeListener;
    window.requestAnimationFrame = requestFrame;
    window.cancelAnimationFrame = cancelFrame;
    if (scrollY !== undefined) Object.defineProperty(window, "scrollY", scrollY);
    (mounted ?? page.drawn).remove();
    mounted = undefined;
  };
}

/** Puts back everything a row's page replaced, and the page's port: each file that uses this
 *  harness runs it after every row. */
export function restoreTheBrowser(): void {
  restoreObserver?.();
  restoreObserver = undefined;
  observers.clear();
  scrollListeners = [];
  windowListeners = new Map();
  frames = new Map();
  thePagePort.height = window.innerHeight;
}
