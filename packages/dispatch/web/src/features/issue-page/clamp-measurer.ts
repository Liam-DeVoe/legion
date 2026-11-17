/** The prototype's `CUT_LINES`: how many lines of an agent's words or an answered question a row
 *  draws before it cuts them. */
const CUT_LINES = 6;

/** How many words a cut must hide before it is made: fewer are drawn whole. */
const CUT_WORDS = 20;

/** How far past six lines a text may run and still be drawn whole, as the prototype allows. */
const CUT_SLACK = 12;

/** How far past the viewport, above it and below it, a row is near enough to be measured: one
 *  screen each way. */
const NEAR_BAND = "100% 0px";

/** What a row draws of words it cuts: how tall it draws them, how many words it holds back,
 *  and whether the cut was moved to the foot of a row of pictures the sixth line crossed. */
export interface Clamp {
  readonly height: number;
  readonly hidden: number;
  readonly pictures: boolean;
}

/** What a row is told of its words: their cut, or `undefined` where they are drawn whole. */
type Report = (clamp: Clamp | undefined) => void;

/** One row's words, as the measurer holds them. */
interface Watched {
  /** The words' box and the children it held when the row was handed over: the boxes whose
   *  size the measurer watches for this row. */
  readonly boxes: readonly Element[];
  /** Whether the words stand within `NEAR_BAND` of the viewport. */
  near: boolean;
  readonly report: Report;
}

/**
 * The one measurer of every cut the new issue page draws (decision 196 of
 * dispatch://INFRA-1196/artifact/part-decisions-md). Each row that may be cut hands it its words
 * (`useClamp`), and it measures them in one batch a frame, every read before any write. Before
 * it, each row measured its own words from its own layout effect, after the writes the rows
 * before it had made in the same commit, so the browser laid the whole outline out again for
 * each row: on a copy of INFRA-1196, 7 to 28 s of every draw of the outline after a view change
 * (part Q's tester's profiles).
 *
 * It measures only the words within one screen of the viewport, which one
 * `IntersectionObserver` tells it, and measures them again when their box or a block of them
 * changes size (one `ResizeObserver`) or the page's typeface arrives (one `loadingdone`
 * listener). Words that come near are measured in the next batch; words that go away keep the
 * cut they last had. Where the browser has no `IntersectionObserver`, every row is near.
 */
class ClampMeasurer {
  readonly #rows = new Map<Element, Watched>();
  /** The rows the next batch measures. */
  readonly #due = new Set<HTMLElement>();
  /** The row each box the size observer watches belongs to. */
  readonly #owners = new Map<Element, HTMLElement>();
  readonly #fonts: FontFaceSet | undefined = document.fonts;
  readonly #near: IntersectionObserver | undefined;
  readonly #sizes: ResizeObserver | undefined;
  #frame: number | undefined;

  constructor() {
    this.#near =
      typeof IntersectionObserver === "undefined"
        ? undefined
        : new IntersectionObserver(
            (entries) => {
              for (const entry of entries) {
                const watched = this.#rows.get(entry.target);
                if (watched === undefined || !(entry.target instanceof HTMLElement)) continue;
                watched.near = entry.isIntersecting;
                this.#queue(entry.target);
              }
            },
            { rootMargin: NEAR_BAND }
          );
    this.#sizes =
      typeof ResizeObserver === "undefined"
        ? undefined
        : new ResizeObserver((entries) => {
            for (const entry of entries) {
              const element = this.#owners.get(entry.target);
              if (element !== undefined) this.#queue(element);
            }
          });
    // The page's typeface arrives after the first frame (`font-display: swap`), and the words
    // laid out again in it can cross the sixth line where the fallback's did not, or hold back
    // another count of words, with no change of the box a resize would see.
    this.#fonts?.addEventListener("loadingdone", this.#measureNear);
  }

  /** Measures `element`'s words from the next batch on and tells `report` each new cut, until
   *  the answer is called. */
  watch(element: HTMLElement, report: Report): () => void {
    // The words' own box is held to the cut while it is drawn, so their growing - a reading that
    // paints after the row, a picture that loads - is seen on the blocks they are drawn in.
    const boxes = [element, ...element.children];
    this.#rows.set(element, { boxes, near: this.#near === undefined, report });
    this.#near?.observe(element);
    for (const box of boxes) {
      this.#owners.set(box, element);
      this.#sizes?.observe(box);
    }
    this.#queue(element);
    return () => this.#unwatch(element);
  }

  /** Whether no row is watched any more. */
  get idle(): boolean {
    return this.#rows.size === 0;
  }

  /** Stops every observer and the batch still to run. */
  stop(): void {
    this.#near?.disconnect();
    this.#sizes?.disconnect();
    this.#fonts?.removeEventListener("loadingdone", this.#measureNear);
    if (this.#frame !== undefined) window.cancelAnimationFrame(this.#frame);
    this.#frame = undefined;
  }

  #unwatch(element: HTMLElement): void {
    const watched = this.#rows.get(element);
    if (watched === undefined) return;
    this.#rows.delete(element);
    this.#due.delete(element);
    this.#near?.unobserve(element);
    for (const box of watched.boxes) {
      this.#owners.delete(box);
      this.#sizes?.unobserve(box);
    }
  }

  /** Puts `element` in the next batch, where it is near. */
  #queue(element: HTMLElement): void {
    if (this.#rows.get(element)?.near !== true) return;
    this.#due.add(element);
    this.#frame ??= window.requestAnimationFrame(this.#measure);
  }

  readonly #measureNear = (): void => {
    for (const [element, watched] of this.#rows) {
      if (watched.near && element instanceof HTMLElement) this.#queue(element);
    }
  };

  /** The batch: every read, then every write, so the page is laid out once for all of them. */
  readonly #measure = (): void => {
    this.#frame = undefined;
    const read = [...this.#due].map((element) => [element, measureClamp(element)] as const);
    this.#due.clear();
    for (const [element, clamp] of read) this.#rows.get(element)?.report(clamp);
  };
}

/** The measurer while a row is watched: made by the first row and stopped after the last. */
let measurer: ClampMeasurer | undefined;

/** Hands `element`'s words to the page's one measurer (`ClampMeasurer`), which tells `report`
 *  each new cut of them until the answer is called. */
export function watchClamp(element: HTMLElement, report: Report): () => void {
  const held = measurer ?? new ClampMeasurer();
  measurer = held;
  const unwatch = held.watch(element, report);
  return () => {
    unwatch();
    if (!held.idle) return;
    held.stop();
    if (measurer === held) measurer = undefined;
  };
}

/** The cut of the words `element` holds, as the prototype's `measureClamps` reads it: layout
 *  reads alone. */
function measureClamp(element: HTMLElement): Clamp | undefined {
  const pitch = Number.parseFloat(getComputedStyle(element).lineHeight);
  const limit = CUT_LINES * (Number.isFinite(pitch) ? pitch : 24);
  if (element.scrollHeight <= limit + CUT_SLACK) return undefined;
  const top = element.getBoundingClientRect().top;
  // The words a cut would hide, block by block: a block below the line whole, and of a block the
  // line crosses the share of its words below it.
  let hidden = 0;
  for (const block of blocksOf(element)) {
    const box = block.getBoundingClientRect();
    const from = box.top - top;
    const to = box.bottom - top;
    const count = (block.textContent ?? "").split(/\s+/u).filter(Boolean).length;
    if (from >= limit) hidden += count;
    else if (to > limit && box.height > 0) hidden += count * ((to - limit) / box.height);
  }
  const held = Math.round(hidden);
  if (held <= CUT_WORDS) return undefined;
  // A row of pictures the line crosses is drawn whole, and the cut moves to its foot.
  let height = limit;
  let pictures = false;
  for (const picture of element.querySelectorAll("img")) {
    const box = picture.getBoundingClientRect();
    if (box.top - top < limit && box.bottom - top > limit) {
      height = Math.max(height, box.bottom - top + 6);
      pictures = true;
    }
  }
  return { height, hidden: held, pictures };
}

/** The blocks of the words `element` holds: the paragraphs, lists and pictures of a text drawn
 *  from its reading, and the one paragraph of a text drawn as stored. */
function blocksOf(element: HTMLElement): Element[] {
  const drawn = element.querySelector(".dispatch-markdown");
  if (drawn !== null && drawn.children.length > 0) return [...drawn.children];
  const first = element.firstElementChild;
  return first === null ? [] : [first];
}
