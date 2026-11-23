/**
 * The oracle of `focus-landing.e2e.ts`: the rules that file's header states, computed from the
 * page itself, for every case. It reads no app code. It runs in the page, installed before the
 * app's own scripts (`installLanding`), and holds what it records in the page between readings.
 *
 * WHAT IT READS BEFORE ANY RECORD, as the page opens: the control each dialog or panel took the
 * focus from as it opened (the control it was opened from), and the row whose checkbox the reader
 * ticked last (the row marked last). A checkbox in a table's head marks no row.
 *
 * WHAT IT READS BEFORE THE CAUSE (`recordBefore`). The control that holds the focus, the lost
 * control, and around it: its spots - the control and each parent of it up to the body, each with
 * the siblings before and after it, so the place it left is found again once it has gone; its
 * regions, nearest first - each dialog, panel or selection bar around it, then the page's main
 * content; the rows around it (`ROW_SELECTOR`), each with the control's place among that row's
 * controls, its tag, its role and its type; the element it stood in and the toolbar around it; its
 * item (`data-shown-item`); the open modal dialog around it; and, for a dialog or a panel, the
 * control it was opened from, recorded the same way. Then every element in the page, so an element
 * that arrives after the cause is told from one that moved; a `MutationObserver` that notes every
 * element added from then on; the window and every box that scrolls, each with where it stands and
 * up to twelve elements in view in it, the rows nearest the lost control first; and, for a control
 * in a selection bar, the list the bar acts on and the row marked last.
 *
 * HOW IT READS EACH STEP (`judgeAfter`). The control's place is that of the nearest of its spots
 * whose node left its place - gone from the page or its parent, hidden, or moved so another
 * element stands right after its nearest sibling still standing before it - while the parent it
 * stood in still stands: after that sibling, else before its nearest sibling still standing after
 * it, else at the start of the parent. What arrived there is the parent's children between those
 * siblings that the observer noted and the record had not seen. A component's own region around
 * the place - an element that names the item it shows, or a row with a `tabindex` of its own -
 * that stands where it stood, it and every element around it inside the region, is the scope of
 * the steps below, and its own stop the last resort: the element itself where it carries a
 * `tabindex`, else the first labelled `section` or `region` inside it that does. The control now
 * at the place by (i) is the first of the scope's controls in the Tab order after the place, while
 * it stands in one of the lost control's rows, else in its toolbar, else in the element it stood
 * in. The row by (ii) is the first row that stands after the place of the outermost row around
 * the lost control inside the scope that left its place - that row, or the element that holds it
 * alone where that element moved - in its parent, or the first row inside the element standing
 * there. A row that stands elsewhere on screen than it stood, as a list that places its rows by
 * `top` moves one, left its place too, and a row placed `absolute` is read only so, since its
 * page order says nothing of where it stands: the row now there is the one of its parent whose
 * top stands nearest where its top stood, within half its height. A control takes
 * typed text where it is an editable
 * element, a `<textarea>` or an `<input>` of a text, number, date or time type. The reader sees a
 * control where more than half a pixel of it meets the window and every box around it that clips
 * - every box whose overflow is not `visible`, up to the first fixed element. A control is in the
 * Tab order where the engine gives it a `tabIndex` of 0 or more, it is not `disabled`, nothing
 * around it is `hidden` or `inert`, the page draws it and it is visible. A selection bar's list is
 * the `ul`, `ol`, `table` or element of a list or grid role around the marked rows; its rows are
 * those a checkbox marks, none nested in another row of it, each landed on by its checkbox; a row
 * is in sight for the bar where its checkbox stands wholly in sight and the element drawn at the
 * checkbox's centre is the row's. The list's own stop is any element around its rows inside the
 * main content. A component's own region stop, after a loss the reader did not cause, is any
 * element that stood around the lost control and still stands, inside the open modal dialog
 * around it where there is one, other than a control and the main content.
 *
 * WHICH VIEW IT READS. The landing is computed in the view the reader had: every box that moved
 * since the record is put back where it stood while the landing is computed, and then where the
 * page left it, so a page that scrolled to a control does not bring into sight a control that was
 * out of it. Every move is then judged as the page left it.
 *
 * HOW IT READS A SCROLL. Every box that moved by more than half a pixel since the record is listed
 * with the reason its move is allowed, or none. `clamped`: the box stands within a pixel of the
 * farthest it can scroll and stood farther. `view kept`, for a loss the reader did not cause: an
 * element marked in view in that box stands where it stood on screen, within a pixel on each axis
 * that moved. `nearest edge`, for a loss the reader caused: the box holds the landing, the landing
 * as it stood before that box's move - its box and every clipping box inside the moved one moved
 * back - was partly but not wholly in sight, the move went the way that shows it and no further
 * than the least distance that does, plus a pixel, and the landing now stands wholly in sight. One
 * box at most makes that move. `view kept (drawn above)`, for a loss the reader caused: the box
 * holds the landing, or is the window; the landing's top stands where it stood at the record,
 * within a pixel; and the box moved down, not sideways, by the height drawn above the landing,
 * within a pixel. That height is how much the elements before the landing, and before each
 * element around it up to the body, that stand above its top grew in the flow since the record,
 * box and margins - a notice drawn inside the box or outside it. Rows taken away count nothing.
 *
 * WHILE A HELD PRESS IS OUT (`judgeHeld`). The held control is the lost control where it stands in
 * the Tab order, else, where its dialog has gone, the control that dialog was opened from.
 */

import type { ElementHandle, Page } from "@playwright/test";

import { CONTROL } from "../web/src/components/control-names";
import { ROW_SELECTOR } from "../web/src/components/row-marker";

/** Which step of the rules chose a landing. */
export type Rule =
  | "kept"
  | "drawn"
  | "moved-up"
  | "walk-back"
  | "stop"
  | "bar-marked"
  | "bar-first"
  | "uncaused-body"
  | "modal-stop"
  | "own-stop";

/** Why a box that moved is allowed to: the page clamped it, it kept its view, it kept the
 *  landing's view after content was drawn above it, it showed a landing that was partly in sight,
 *  or it moved by no more than the rounding of a box's place. */
export type MoveReason =
  | "clamped"
  | "view kept"
  | "view kept (drawn above)"
  | "nearest edge"
  | "under 1 px";

/** How far one box moved across the cause, and why that move is allowed, or null where it is not. */
export interface Move {
  readonly box: string;
  readonly reason: MoveReason | null;
  readonly x: number;
  readonly y: number;
}

/** What a reading after the cause found. */
export interface Verdict {
  /** The computed landing, named. */
  readonly expected: string;
  /** The step of the rules that chose it. */
  readonly rule: Rule;
  /** What holds the focus, named. */
  readonly got: string;
  /** Whether the focus is on the computed landing. */
  readonly right: boolean;
  readonly onBody: boolean;
  /** Whether the focus is behind the topmost open modal dialog. */
  readonly behindModal: boolean;
  /** What the walk back passed over before its landing, by kind: `text`, `unseen`, `untabbed` and
   *  `other-item`, each with a count and the first names; empty where it passed nothing. */
  readonly passed: string;
  /** Whether the landing a case names by hand is the computed one; null where it names none. */
  readonly named: boolean | null;
  /** Every box that moved by more than half a pixel. */
  readonly moves: readonly Move[];
}

/** What a reading while a held press's write or read is out found. */
export interface HeldVerdict {
  readonly expected: string;
  readonly got: string;
  readonly right: boolean;
  readonly moves: readonly Move[];
}

/** What a record before the cause names, for the case's own messages. */
export interface Recorded {
  /** The lost control, named. */
  readonly control: string;
  /** The region it was lost from, named. */
  readonly region: string;
}

/** The page's half of the oracle, installed in every document before the app's own scripts. */
interface LandingLibrary {
  record(): Recorded;
  judge(caused: boolean, named: readonly Node[]): Verdict;
  held(): HeldVerdict;
}

declare global {
  interface Window {
    __landingLibrary?: LandingLibrary;
  }
}

/** The selectors the page's half reads, handed to it as the init script's argument: it runs in
 *  the page, where nothing of this module is in scope. */
interface LandingConfig {
  readonly barSelector: string;
  readonly dialogSelector: string;
  readonly mainSelector: string;
  readonly modalSelector: string;
  readonly rowSelector: string;
  readonly shownItemAttribute: string;
}

const LANDING_CONFIG: LandingConfig = {
  barSelector: `[data-control="${CONTROL.selectionBar}"]`,
  dialogSelector: '[role="dialog"], dialog, [data-anchored-panel]',
  mainSelector: '[data-testid="main-content"]',
  modalSelector: 'dialog:modal, [role="dialog"][aria-modal="true"]',
  rowSelector: ROW_SELECTOR,
  shownItemAttribute: "data-shown-item",
};

/**
 * Installs the page's half of the oracle in every document `page` opens from now on; a row calls
 * it once, before its first case opens a page. That half runs in the page as an init script, so
 * every function it uses is written inside it, and it reads only `config`. It notes, from the
 * document's first script on, the control each dialog or panel took the focus from as it opened,
 * and the row whose checkbox the reader ticked last, and it hands the page the three readings the
 * module's header describes.
 */
export async function installLanding(page: Page): Promise<void> {
  await page.addInitScript((config: LandingConfig): void => {
    if (window.__landingLibrary !== undefined) return;
    const {
      barSelector,
      dialogSelector,
      mainSelector,
      modalSelector,
      rowSelector,
      shownItemAttribute,
    } = config;
    const regionSelector = `${dialogSelector}, ${barSelector}`;
    const shownItem = `[${shownItemAttribute}]`;
    const listSelector =
      'ul, ol, table, [role="list"], [role="grid"], [role="table"], [role="treegrid"], [role="listbox"]';
    /** Every element the keyboard could reach by its markup: what the walk back reads, before it
     *  asks which of them the Tab order holds. */
    const controlSelector = [
      "a[href]",
      "button",
      'input:not([type="hidden"])',
      "select",
      "textarea",
      "summary",
      "[tabindex]",
      '[contenteditable]:not([contenteditable="false"])',
    ].join(", ");
    /** The `<input>` types whose value the reader types: text and its kin, numbers, dates and
     *  times. */
    const textTypes: Record<string, true> = {
      date: true,
      "datetime-local": true,
      email: true,
      month: true,
      number: true,
      password: true,
      search: true,
      tel: true,
      text: true,
      time: true,
      url: true,
      week: true,
    };

    interface Box {
      top: number;
      left: number;
      bottom: number;
      right: number;
    }
    /** Where a node stood: its parent, and the element siblings before it, nearest first, and after
     *  it, nearest first. */
    interface Spot {
      readonly node: Element;
      readonly parent: Element | null;
      readonly before: readonly Element[];
      readonly after: readonly Element[];
    }
    /** A row around the lost control: where it stood, and the control's place among its controls. */
    interface RowPlace {
      readonly spot: Spot;
      readonly index: number;
      readonly tag: string;
      readonly role: string | null;
      readonly type: string | null;
      /** Where the row stood in its parent, from the parent's top, and its height: a list that
       *  places its rows by `top` (the Agents page's window) moves a row on screen and not in
       *  the page's order. */
      readonly offset: number;
      readonly height: number;
      /** Whether the row is placed `absolute` in its parent, whose page order then says nothing
       *  of where it stands on screen. */
      readonly placed: boolean;
    }
    /** A lost control as the rules read it. */
    interface Subject {
      readonly lost: Element;
      readonly item: string | null;
      readonly regions: readonly Element[];
      readonly spots: readonly Spot[];
      readonly rows: readonly RowPlace[];
      readonly group: Element | null;
      readonly opener: Subject | null;
      readonly modal: Element | null;
    }
    /** A box that scrolls, or the window (`box` null), as it stood, with elements in view in it. */
    interface BoxRecord {
      readonly box: Element | null;
      readonly name: string;
      readonly x: number;
      readonly y: number;
      readonly anchors: readonly { element: Element; left: number; top: number }[];
    }
    /** A selection bar the lost control stood in: the list it acts on and its row marked last. */
    interface BarRecord {
      readonly list: Element | null;
      readonly marked: readonly Spot[] | null;
      readonly stops: readonly Element[];
    }
    interface Snapshot {
      readonly subject: Subject;
      readonly existing: WeakSet<Element>;
      readonly added: Set<Element>;
      readonly observer: MutationObserver;
      readonly boxes: readonly BoxRecord[];
      readonly bar: BarRecord | null;
      /** Where each element of the page stood on screen, from the viewport's top, and the height
       *  it took in the flow of its parent (`flowHeight`). */
      readonly places: WeakMap<Element, { readonly height: number; readonly top: number }>;
    }
    /** A computed landing: the element, or what the focus may be on where the rule names a set. */
    interface Landing {
      readonly element: Element | null;
      readonly accepts: (active: Element) => boolean;
      readonly rule: Rule;
      readonly name: string;
      /** The control whose nearest-edge scroll is allowed, where the rule allows one. */
      readonly scrolls: Element | null;
    }
    type Point =
      | { readonly after: Element }
      | { readonly before: Element }
      | { readonly start: Element };
    /** What the walk back passed over: `add` notes one control by the kind of its passing, and
     *  `text` reads them as a line prints them. */
    interface Passed {
      add(kind: string, element: Element): void;
      text(): string;
    }

    const openers = new WeakMap<Element, Element>();
    let lastFocused: Element | null = null;
    let markedLast: HTMLInputElement | null = null;
    let current: Snapshot | null = null;

    const isListRow = (row: Element | null): row is Element =>
      row !== null && row.closest("thead") === null;

    document.addEventListener(
      "focusin",
      (event) => {
        const target = event.target;
        if (!(target instanceof Element)) return;
        const region = target.closest(dialogSelector);
        if (
          region !== null &&
          !openers.has(region) &&
          lastFocused?.isConnected === true &&
          !region.contains(lastFocused)
        ) {
          openers.set(region, lastFocused);
        }
        lastFocused = target;
      },
      true
    );
    document.addEventListener(
      "change",
      (event) => {
        const target = event.target;
        if (
          target instanceof HTMLInputElement &&
          target.type === "checkbox" &&
          target.checked &&
          target.closest(barSelector) === null &&
          isListRow(target.closest(rowSelector))
        ) {
          markedLast = target;
        }
      },
      true
    );

    const main = (): Element | null => document.querySelector(mainSelector);
    const displayed = (element: Element): boolean =>
      element.closest("[hidden]") === null && getComputedStyle(element).display !== "none";
    const rendered = (element: Element): boolean =>
      element.isConnected &&
      element.closest("[hidden]") === null &&
      element.getClientRects().length > 0;
    const standing = (element: Element): boolean => element.isConnected && displayed(element);
    const regionOf = (element: Element): Element => {
      const region = element.closest(regionSelector);
      if (region !== null) return region;
      const page = main();
      return page?.contains(element) ? page : document.body;
    };
    const inTabOrder = (element: Element): boolean => {
      if (!(element instanceof HTMLElement)) return false;
      if (element.tabIndex < 0 || element.matches(":disabled")) return false;
      if (element.closest("[hidden], [inert]") !== null || !rendered(element)) return false;
      return getComputedStyle(element).visibility === "visible";
    };
    const takesText = (element: Element): boolean =>
      (element instanceof HTMLElement && element.isContentEditable) ||
      element instanceof HTMLTextAreaElement ||
      (element instanceof HTMLInputElement && textTypes[element.type] === true);
    const isControlTag = (element: Element): boolean =>
      element.matches(
        'a[href], button, input, select, textarea, summary, [contenteditable="true"]'
      );
    const itemOf = (element: Element): string | null =>
      element.closest(shownItem)?.getAttribute(shownItemAttribute) ?? null;
    const inAnotherItem = (element: Element, item: string | null): boolean => {
      const own = itemOf(element);
      return own !== null && own !== item;
    };
    const showsAnother = (element: Element, item: string | null): boolean =>
      inAnotherItem(element, item) ||
      [...element.querySelectorAll(shownItem)].some(
        (marked) => marked.getAttribute(shownItemAttribute) !== item
      );
    const controlsIn = (container: Element): Element[] =>
      [...container.querySelectorAll(controlSelector)].filter(inTabOrder);

    /** The boxes around `element` that clip what it draws, nearest first: each with an overflow
     *  other than `visible`, up to the first fixed element, whose ancestors clip nothing of it. */
    const clipsOf = (element: Element): Element[] => {
      const clips: Element[] = [];
      let fixed = getComputedStyle(element).position === "fixed";
      for (
        let box = element.parentElement;
        box !== null && !fixed && box !== document.body && box !== document.documentElement;
        box = box.parentElement
      ) {
        const style = getComputedStyle(box);
        if (style.overflowX !== "visible" || style.overflowY !== "visible") clips.push(box);
        if (style.position === "fixed") fixed = true;
      }
      return clips;
    };
    const isFixed = (element: Element): boolean => {
      for (let node: Element | null = element; node !== null; node = node.parentElement) {
        if (getComputedStyle(node).position === "fixed") return true;
      }
      return false;
    };
    /** `element`'s box, moved back by `shift` where the box moved carries it: what it was before
     *  that box scrolled by `shift`. */
    const boxBefore = (
      element: Element,
      shift: { box: Element | null; dx: number; dy: number } | null
    ): Box => {
      const own = element.getBoundingClientRect();
      const carried =
        shift !== null &&
        (shift.box === null
          ? !isFixed(element)
          : shift.box !== element && shift.box.contains(element));
      const dx = carried && shift !== null ? shift.dx : 0;
      const dy = carried && shift !== null ? shift.dy : 0;
      return {
        bottom: own.bottom + dy,
        left: own.left + dx,
        right: own.right + dx,
        top: own.top + dy,
      };
    };
    /** What the reader can see of the page through the boxes around `element`, read as it stood
     *  before `shift`'s box scrolled, or as it stands now. */
    const sightOf = (
      element: Element,
      shift: { box: Element | null; dx: number; dy: number } | null = null
    ): Box => {
      const sight = { bottom: window.innerHeight, left: 0, right: window.innerWidth, top: 0 };
      for (const clip of clipsOf(element)) {
        const edges = boxBefore(clip, shift);
        sight.top = Math.max(sight.top, edges.top);
        sight.left = Math.max(sight.left, edges.left);
        sight.bottom = Math.min(sight.bottom, edges.bottom);
        sight.right = Math.min(sight.right, edges.right);
      }
      return sight;
    };
    const meets = (own: Box, sight: Box): boolean =>
      Math.min(own.right, sight.right) - Math.max(own.left, sight.left) > 0.5 &&
      Math.min(own.bottom, sight.bottom) - Math.max(own.top, sight.top) > 0.5;
    const whole = (own: Box, sight: Box): boolean =>
      own.top >= sight.top - 1 &&
      own.bottom <= sight.bottom + 1 &&
      own.left >= sight.left - 1 &&
      own.right <= sight.right + 1;
    const inSight = (element: Element): boolean =>
      meets(element.getBoundingClientRect(), sightOf(element));
    const wholeInSight = (element: Element): boolean =>
      whole(element.getBoundingClientRect(), sightOf(element));
    /** Whether nothing is drawn over `element`'s centre but `element` and what `row` holds. */
    const uncovered = (element: Element, row: Element): boolean => {
      const own = element.getBoundingClientRect();
      const hit = document.elementFromPoint((own.left + own.right) / 2, (own.top + own.bottom) / 2);
      return hit !== null && (row.contains(hit) || element.contains(hit));
    };

    const words = (text: string | null | undefined, cap = 48): string =>
      (text ?? "").replace(/\s+/g, " ").trim().slice(0, cap);
    const labelOf = (element: Element): string => {
      const labelledBy = element.getAttribute("aria-labelledby");
      const byIds =
        labelledBy === null
          ? ""
          : labelledBy
              .split(/\s+/)
              .map((id) => document.getElementById(id)?.textContent ?? "")
              .join(" ");
      const labels = (element as HTMLInputElement).labels;
      return words(
        element.getAttribute("aria-label") ||
          byIds ||
          (labels !== undefined && labels !== null && labels.length > 0
            ? labels[0]?.textContent
            : "") ||
          element.getAttribute("title") ||
          element.getAttribute("placeholder") ||
          element.textContent
      );
    };
    const roleOf = (element: Element): string => {
      const role = element.getAttribute("role");
      if (role !== null) return role;
      if (element instanceof HTMLInputElement) {
        if (element.type === "checkbox" || element.type === "radio") return element.type;
        return textTypes[element.type] === true ? "textbox" : element.type;
      }
      const tag = element.tagName.toLowerCase();
      if (tag === "a") return "link";
      if (tag === "select") return "combobox";
      if (tag === "textarea") return "textbox";
      return tag;
    };
    const nameOf = (element: Element | null): string => {
      if (element === null || element === document.body || element === document.documentElement) {
        return "the body";
      }
      if (element === main()) return "the main content";
      const own = `${roleOf(element)} "${labelOf(element)}"`;
      const row = element.parentElement?.closest(rowSelector);
      if (row === null || row === undefined) return own;
      const rowName = words(
        row.getAttribute("data-testid") ??
          row.getAttribute("aria-label") ??
          row.getAttribute("data-inbox-row") ??
          row.getAttribute("data-issue-row") ??
          row.getAttribute("data-agent-row") ??
          row.textContent,
        32
      );
      return `${own} in row "${rowName}"`;
    };

    const siblings = (node: Element, forward: boolean): Element[] => {
      const list: Element[] = [];
      for (
        let sibling = forward ? node.nextElementSibling : node.previousElementSibling;
        sibling !== null;
        sibling = forward ? sibling.nextElementSibling : sibling.previousElementSibling
      ) {
        list.push(sibling);
      }
      return list;
    };
    const spotOf = (node: Element): Spot => ({
      after: siblings(node, true),
      before: siblings(node, false),
      node,
      parent: node.parentElement,
    });
    const spotsUp = (node: Element): Spot[] => {
      const spots: Spot[] = [];
      for (
        let at: Element | null = node;
        at !== null && at !== document.body && at !== document.documentElement;
        at = at.parentElement
      ) {
        spots.push(spotOf(at));
      }
      return spots;
    };

    const subjectOf = (lost: Element, depth: number): Subject => {
      const regions: Element[] = [];
      for (
        let region = lost.closest(regionSelector);
        region !== null;
        region = region.parentElement?.closest(regionSelector) ?? null
      ) {
        regions.push(region);
      }
      const page = main();
      regions.push(page?.contains(lost) ? page : document.body);
      const inner = regions[0] ?? document.body;
      const rows: RowPlace[] = [];
      for (
        let row = lost.closest(rowSelector);
        row !== null && row !== inner && inner.contains(row);
        row = row.parentElement?.closest(rowSelector) ?? null
      ) {
        const box = row.getBoundingClientRect();
        rows.push({
          height: box.height,
          placed: getComputedStyle(row).position === "absolute",
          index: controlsIn(row)
            .filter((control) => regionOf(control) === regionOf(row))
            .indexOf(lost),
          offset: box.top - (row.parentElement?.getBoundingClientRect().top ?? 0),
          role: lost.getAttribute("role"),
          spot: spotOf(row),
          tag: lost.tagName,
          type: lost instanceof HTMLInputElement ? lost.type : null,
        });
      }
      const toolbar = lost.parentElement?.closest('[role="toolbar"]') ?? null;
      const group =
        rows.length > 0
          ? null
          : toolbar !== null && toolbar !== inner && inner.contains(toolbar)
            ? toolbar
            : lost.parentElement;
      const opener = openers.get(inner);
      const modal =
        [...document.querySelectorAll(modalSelector)]
          .filter((open) => rendered(open) && open.contains(lost))
          .at(-1) ?? null;
      return {
        group,
        item: itemOf(lost),
        lost,
        modal,
        opener:
          depth < 3 && inner.matches(dialogSelector) && opener?.isConnected === true
            ? subjectOf(opener, depth + 1)
            : null,
        regions,
        rows,
        spots: spotsUp(lost),
      };
    };

    /** Whether `spot`'s node no longer stands where it stood: gone from the page or its parent,
     *  hidden, or moved, so the old element now right after its nearest standing previous sibling
     *  is another. */
    const leftPlace = (spot: Spot, existing: WeakSet<Element>): boolean => {
      const { node, parent } = spot;
      if (parent === null) return false;
      if (!node.isConnected || node.parentElement !== parent || !displayed(node)) return true;
      const previous = spot.before.find((sibling) => sibling.parentElement === parent) ?? null;
      let at = previous === null ? parent.firstElementChild : previous.nextElementSibling;
      while (at !== null && !existing.has(at)) at = at.nextElementSibling;
      return at !== node;
    };
    /** The nearest of `spots` whose node left its place while the parent it stood in still stands,
     *  or null where every node stands where it stood. */
    const leftSpot = (spots: readonly Spot[], existing: WeakSet<Element>): Spot | null =>
      spots.find(
        (spot) => spot.parent !== null && standing(spot.parent) && leftPlace(spot, existing)
      ) ?? null;
    /** The place `spot`'s node left: after its nearest sibling still standing before it, else
     *  before its nearest one still standing after it, else at the start of its parent. */
    const placeOf = (spot: Spot): Point => {
      const parent = spot.parent as Element;
      const previous = spot.before.find((sibling) => sibling.parentElement === parent);
      if (previous !== undefined) return { after: previous };
      const next = spot.after.find((sibling) => sibling.parentElement === parent);
      if (next !== undefined) return { before: next };
      return { start: parent };
    };
    /** Whether `node` comes before `point` in document order. */
    const precedes = (node: Element, point: Point): boolean => {
      const following = (from: Element, to: Element) =>
        (from.compareDocumentPosition(to) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;
      if ("before" in point) {
        return (
          node !== point.before && !point.before.contains(node) && following(node, point.before)
        );
      }
      if ("after" in point) {
        return node === point.after || point.after.contains(node) || following(node, point.after);
      }
      return node === point.start || (!point.start.contains(node) && following(node, point.start));
    };
    /** The elements of `spot`'s parent at its place now, between the siblings that still stand
     *  around it, that arrived after the record: new to the page, and added since. */
    const arrivedAt = (spot: Spot, record: Snapshot): Element[] => {
      const parent = spot.parent as Element;
      const previous = spot.before.find((sibling) => sibling.parentElement === parent) ?? null;
      const next = spot.after.find((sibling) => sibling.parentElement === parent) ?? null;
      const arrived: Element[] = [];
      for (
        let child = previous === null ? parent.firstElementChild : previous.nextElementSibling;
        child !== null && child !== next;
        child = child.nextElementSibling
      ) {
        if (record.added.has(child) && !record.existing.has(child)) arrived.push(child);
      }
      return arrived;
    };
    /** The first element standing at or after `point` among `parent`'s children, other than
     *  `except`, as a row: itself where it is one, else the first row inside it, as a row a page
     *  draws in a wrapper of its own; null where it holds none. */
    const rowAt = (point: Point, parent: Element, except: Element): Element | null => {
      let child =
        "after" in point
          ? point.after.nextElementSibling
          : "before" in point
            ? point.before
            : parent.firstElementChild;
      for (; child !== null; child = child.nextElementSibling) {
        if (child === except || !standing(child)) continue;
        return child.matches(rowSelector) ? child : child.querySelector(rowSelector);
      }
      return null;
    };
    /** Whether a row around the lost control that stands in the page's order where it stood now
     *  stands elsewhere on screen in its parent, by more than half its height, as a list that
     *  places its rows by `top` (the Agents page's window) moves one. */
    const shifted = (place: RowPlace): boolean => {
      const { node, parent } = place.spot;
      if (parent === null || !node.isConnected || node.parentElement !== parent) return false;
      const now = node.getBoundingClientRect().top - parent.getBoundingClientRect().top;
      return Math.abs(now - place.offset) > place.height / 2;
    };
    /** The component's own region around the lost control's place that still stands where it
     *  stood, inside `region`: the nearest element around the place that names the item it shows
     *  (the Inbox's reading column), or a row that takes the focus by a `tabindex` of its own (the
     *  ask card's row in the Inbox's list). Null where none stands so. */
    const componentAround = (
      subject: Subject,
      region: Element,
      existing: WeakSet<Element>
    ): Element | null => {
      const { spots } = subject;
      for (const [index, spot] of spots.entries()) {
        const { node } = spot;
        if (node === region) return null;
        // A node that went with the cause is no region of its own; the nodes above it are read.
        if (!node.isConnected) continue;
        if (!region.contains(node)) return null;
        const component =
          node !== subject.lost &&
          (node.matches(shownItem) || (node.matches(rowSelector) && node.hasAttribute("tabindex")));
        if (!component || !standing(node)) continue;
        const moved = spots
          .slice(index)
          .some(
            (above) =>
              above.node !== region &&
              region.contains(above.node) &&
              above.parent !== null &&
              standing(above.parent) &&
              leftPlace(above, existing)
          );
        const onScreen = subject.rows.some(
          (place) => place.spot.node.contains(node) && shifted(place)
        );
        if (!moved && !onScreen) return node;
      }
      return null;
    };
    /** A component region's own stop: the region itself where it takes the focus, else the
     *  labelled region inside it that does, as the reading column's Question body. */
    const ownStopOf = (component: Element): Landing => {
      const stop = component.hasAttribute("tabindex")
        ? component
        : ([...component.querySelectorAll("[tabindex]")].find(
            (each) =>
              each.matches('section, [role="region"]') && !isControlTag(each) && standing(each)
          ) ?? component);
      return {
        accepts: (active) => active === stop,
        element: stop,
        name: `the own stop of ${nameOf(component)}, ${nameOf(stop)}`,
        rule: "own-stop",
        scrolls: null,
      };
    };

    /** What the walk back passed over, by kind, each with its count and its first three names. */
    const notes = (): Passed => {
      const kinds = new Map<string, { count: number; names: string[] }>();
      return {
        add: (kind, element) => {
          const entry = kinds.get(kind) ?? { count: 0, names: [] };
          entry.count += 1;
          if (entry.names.length < 3) entry.names.push(nameOf(element));
          kinds.set(kind, entry);
        },
        text: () =>
          [...kinds.entries()]
            .map(([kind, { count, names }]) => `${kind}:${count} (${names.join(", ")})`)
            .join(" "),
      };
    };

    const landingOn = (element: Element, rule: Rule, scrolls = true): Landing => ({
      accepts: (active) => active === element,
      element,
      name: nameOf(element),
      rule,
      scrolls: scrolls ? element : null,
    });

    /** The selection bar's hand-off, once the bar has gone. */
    const barLanding = (record: Snapshot, bar: BarRecord): Landing => {
      const stop: Landing = {
        accepts: (active) => bar.stops.includes(active) && standing(active),
        element: bar.stops.find(standing) ?? null,
        name: "the list's own stop",
        rule: "stop",
        scrolls: null,
      };
      const { list } = bar;
      if (list === null || !standing(list)) return stop;
      // The list's own rows that a checkbox marks: none nested in another row of it, as an ask card
      // in an Inbox row is, and none that marks nothing, as a band's heading.
      const markOf = (row: Element): Element | null =>
        [...row.querySelectorAll('input[type="checkbox"]')].find(inTabOrder) ?? null;
      const rows = [...list.querySelectorAll(rowSelector)].filter((row) => {
        const outer = row.parentElement?.closest(rowSelector) ?? null;
        return (
          isListRow(row) &&
          row.parentElement?.closest(listSelector) === list &&
          (outer === null || !list.contains(outer)) &&
          standing(row) &&
          row.closest(barSelector) === null &&
          markOf(row) !== null
        );
      });
      const seen = (row: Element): Element | null => {
        const mark = markOf(row);
        return mark !== null && wholeInSight(mark) && uncovered(mark, row) ? mark : null;
      };
      if (bar.marked === null) {
        for (const row of rows) {
          const mark = seen(row);
          if (mark !== null) return landingOn(mark, "bar-first", false);
        }
        return stop;
      }
      const markedRow = bar.marked[0]?.node as Element;
      const left = leftSpot(bar.marked, record.existing);
      const point: Point = left === null ? { before: markedRow } : placeOf(left);
      const atPlace =
        left === null ? markedRow : (rows.find((row) => !precedes(row, point)) ?? null);
      const mark = atPlace === null ? null : seen(atPlace);
      if (mark !== null) return landingOn(mark, "bar-marked", false);
      for (const row of rows.filter((each) => precedes(each, point)).reverse()) {
        const before = seen(row);
        if (before !== null) return landingOn(before, "bar-marked", false);
      }
      return stop;
    };

    /** The landing of a loss the reader caused, by the steps of the module's header. */
    const causedLanding = (record: Snapshot, passed: Passed): Landing => {
      let subject = record.subject;
      const { lost } = subject;
      if (lost.isConnected && inTabOrder(lost) && inSight(lost)) return landingOn(lost, "kept");
      let fromOpener = false;
      for (let depth = 0; depth < 3; depth += 1) {
        const inner = subject.regions[0] ?? document.body;
        if (standing(inner)) break;
        if (inner.matches(barSelector) && record.bar !== null)
          return barLanding(record, record.bar);
        if (subject.opener === null) break;
        subject = subject.opener;
        fromOpener = true;
      }
      const region = subject.regions.find(standing) ?? document.body;
      // A component's own region around the place is answered first, and at its own stop where
      // nothing in it lands; the region's steps run in it alone.
      const component = componentAround(subject, region, record.existing);
      const scope = component ?? region;
      const lastResort = (): Landing =>
        component === null ? stopOf(region) : ownStopOf(component);
      const left = leftSpot(subject.spots, record.existing);
      if (left === null && fromOpener) {
        // The opener stands: the walk starts just after it, so it is the first control it meets.
        const found = walkBack(scope, region, { after: subject.lost }, subject.item, passed);
        return found ?? lastResort();
      }
      const point: Point = left === null ? { before: subject.lost } : placeOf(left);
      if (left !== null) {
        const arrived = arrivedAt(left, record);
        if (arrived.length > 0 && !arrived.some((each) => showsAnother(each, subject.item))) {
          const drawn = arrived.flatMap((each) => [
            ...(each.matches(controlSelector) ? [each] : []),
            ...each.querySelectorAll(controlSelector),
          ]);
          const form = arrived
            .map((each) => (each.matches("form") ? each : each.querySelector("form")))
            .find((each) => each !== null);
          const ordered =
            form === undefined || form === null
              ? drawn
              : [
                  ...drawn.filter((each) => form.contains(each)),
                  ...drawn.filter((each) => !form.contains(each)),
                ];
          const first = ordered.find((each) => inTabOrder(each) && inSight(each));
          if (first !== undefined) return landingOn(first, "drawn");
        }
      }
      // (i) A control of the lost control's own row, card or toolbar now first after the place.
      const controls = [...scope.querySelectorAll(controlSelector)].filter(
        (control) => control !== scope && regionOf(control) === region && inTabOrder(control)
      );
      const atPlace = controls.find((control) => !precedes(control, point));
      const groups =
        subject.rows.length > 0 ? subject.rows.map((place) => place.spot.node) : [subject.group];
      if (
        atPlace !== undefined &&
        groups.some((group) => group !== null && standing(group) && group.contains(atPlace)) &&
        inSight(atPlace)
      ) {
        return landingOn(atPlace, "moved-up");
      }
      // (ii) The row now where the outermost row inside the scope that left its place stood: the
      // row itself, or the element that holds it alone, as a page's wrapper of a row of its own.
      for (const place of [...subject.rows].reverse()) {
        const node = place.spot.node;
        // A row that went with the cause is read where it stood: in the parent it stood in.
        const host = place.spot.parent;
        if (node === scope || host === null || !(host === scope || scope.contains(host))) continue;
        const at = subject.spots.findIndex((spot) => spot.node === node);
        let moved: Spot | null = null;
        for (const spot of subject.spots.slice(at)) {
          if (spot.node === scope || (spot.node !== node && spot.node.matches(rowSelector))) break;
          if (spot.parent !== null && standing(spot.parent) && leftPlace(spot, record.existing)) {
            moved = spot;
            break;
          }
        }
        let row: Element | null = null;
        if (!place.placed && moved !== null && moved.parent !== null) {
          row = rowAt(placeOf(moved), moved.parent, moved.node);
        } else {
          // A row that stands elsewhere on screen than it stood, or a row placed `absolute` that
          // went, has left its place too: the row now there is the one of its parent's whose top
          // stands nearest where its top stood, within half its height.
          const parent = place.spot.parent;
          const gone = !node.isConnected || node.parentElement !== parent;
          if (parent === null || !standing(parent) || (!gone && !shifted(place))) continue;
          const top = parent.getBoundingClientRect().top;
          const distance = (element: Element) =>
            Math.abs(element.getBoundingClientRect().top - top - place.offset);
          const there = [...parent.children]
            .filter(
              (child) => child !== node && standing(child) && distance(child) <= place.height / 2
            )
            .sort((one, other) => distance(one) - distance(other))[0];
          if (there === undefined) continue;
          row = there.matches(rowSelector) ? there : there.querySelector(rowSelector);
        }
        if (row === null || showsAnother(row, subject.item)) break;
        // The row's own controls: none of a dialog or panel drawn inside it.
        const own = controlsIn(row).filter((control) => regionOf(control) === regionOf(row));
        const same = place.index < 0 ? undefined : own[place.index];
        const alike =
          same !== undefined &&
          same.tagName === place.tag &&
          same.getAttribute("role") === place.role &&
          (same instanceof HTMLInputElement ? same.type : null) === place.type;
        if (alike && same !== undefined && inSight(same)) return landingOn(same, "moved-up");
        const first = own[0];
        if (first !== undefined && inSight(first)) return landingOn(first, "moved-up");
        break;
      }
      return walkBack(scope, region, point, subject.item, passed) ?? lastResort();
    };
    /** The nearest control before `point` among `scope`'s controls that are `region`'s own, by the
     *  walk back of step 3, noting in `passed` each it passes over. */
    const walkBack = (
      scope: Element,
      region: Element,
      point: Point,
      item: string | null,
      passed: Passed
    ): Landing | null => {
      const before = [...scope.querySelectorAll(controlSelector)]
        .filter(
          (control) => control !== scope && regionOf(control) === region && precedes(control, point)
        )
        .reverse();
      for (const control of before) {
        if (!inTabOrder(control)) passed.add("untabbed", control);
        else if (takesText(control)) passed.add("text", control);
        else if (!inSight(control)) passed.add("unseen", control);
        else if (inAnotherItem(control, item)) passed.add("other-item", control);
        else return landingOn(control, "walk-back");
      }
      return null;
    };
    const stopOf = (region: Element): Landing => ({
      accepts: (active) => active === region,
      element: region,
      name: `the own stop of ${nameOf(region)}`,
      rule: "stop",
      scrolls: null,
    });

    /** Where a loss the reader did not cause may leave the focus. */
    const uncausedLanding = (record: Snapshot, active: Element): Landing => {
      const { subject } = record;
      if (subject.lost.isConnected && inTabOrder(subject.lost) && active === subject.lost) {
        return landingOn(subject.lost, "kept", false);
      }
      const page = main();
      const ownStops = subject.spots
        .map((spot) => spot.node)
        .filter(
          (node) =>
            node !== subject.lost &&
            node !== page &&
            standing(node) &&
            !isControlTag(node) &&
            (subject.modal === null || subject.modal.contains(node))
        );
      if (ownStops.includes(active) && active !== subject.modal) {
        return { ...landingOn(active, "own-stop", false), accepts: () => true };
      }
      const modal = subject.modal;
      if (modal !== null && rendered(modal) && modal.matches(modalSelector)) {
        return {
          ...landingOn(modal, "modal-stop", false),
          name: `the own stop of ${nameOf(modal)}`,
        };
      }
      return {
        accepts: (focused) => focused === document.body || focused === document.documentElement,
        element: document.body,
        name: "the body",
        rule: "uncaused-body",
        scrolls: null,
      };
    };

    const scrollOf = (box: Element | null): [number, number] =>
      box === null ? [window.scrollX, window.scrollY] : [box.scrollLeft, box.scrollTop];
    const farthestOf = (box: Element | null): [number, number] => {
      const scroller = box ?? document.scrollingElement ?? document.documentElement;
      return [
        scroller.scrollWidth - scroller.clientWidth,
        scroller.scrollHeight - scroller.clientHeight,
      ];
    };

    /** The height an element takes in the flow of its parent: its box and its margins, or none
     *  where the page does not draw it or places it out of the flow. */
    const flowHeight = (element: Element): number => {
      if (!rendered(element)) return 0;
      const style = getComputedStyle(element);
      if (style.position === "absolute" || style.position === "fixed") return 0;
      const margins = Number.parseFloat(style.marginTop) + Number.parseFloat(style.marginBottom);
      return element.getBoundingClientRect().height + (Number.isNaN(margins) ? 0 : margins);
    };
    /** Whether a box's move by `dx`, `dy` kept what the reader sees where it stood after content
     *  was drawn above `landing`: the landing's top stands on screen where it stood at the record,
     *  within a pixel, and the box moved down, not sideways, by the height drawn above the
     *  landing, within a pixel. That height is how much the elements before the landing and
     *  before each element around it, up to the body, grew in the flow since the record - one
     *  that arrived grew from nothing - counting those that stand above the landing's top: a
     *  notice drawn inside the box or outside it. Rows the press took away above the landing
     *  count nothing, so a move after them does not match. */
    const drawnAbove = (
      record: Snapshot,
      box: Element | null,
      dx: number,
      dy: number,
      landing: Element
    ): boolean => {
      if (Math.abs(dx) > 1 || (box !== null && !box.contains(landing))) return false;
      const stood = record.places.get(landing);
      const top = landing.getBoundingClientRect().top;
      if (stood === undefined || Math.abs(top - stood.top) > 1) return false;
      let height = 0;
      for (let node: Element | null = landing; node !== null && node !== document.body; ) {
        for (let sibling = node.previousElementSibling; sibling !== null; ) {
          const now = flowHeight(sibling);
          if (now === 0 || sibling.getBoundingClientRect().bottom <= top + 1) {
            height += now - (record.places.get(sibling)?.height ?? 0);
          }
          sibling = sibling.previousElementSibling;
        }
        node = node.parentElement;
      }
      return height > 0.5 && Math.abs(dy - height) <= 1;
    };
    /** Every box that moved by more than half a pixel since the record, each with why its move is
     *  allowed: by the clamp, the rounding, a kept view where `keptAllowed`, the nearest-edge
     *  scroll that shows `edge` whole, or a view `landing` keeps after content drawn above it. */
    const movesSince = (
      record: Snapshot,
      keptAllowed: boolean,
      edge: Element | null,
      landing: Element | null
    ): Move[] => {
      const moves: Move[] = [];
      let edgeTaken = false;
      for (const entry of record.boxes) {
        if (entry.box !== null && !entry.box.isConnected) continue;
        const now = scrollOf(entry.box);
        const dx = now[0] - entry.x;
        const dy = now[1] - entry.y;
        const axes = ([0, 1] as const).filter((axis) => Math.abs([dx, dy][axis] ?? 0) > 0.5);
        if (axes.length === 0) continue;
        const before: [number, number] = [entry.x, entry.y];
        const farthest = farthestOf(entry.box);
        let reason: MoveReason | null = null;
        if (Math.abs(dx) <= 1 && Math.abs(dy) <= 1) reason = "under 1 px";
        else if (
          axes.every(
            (axis) =>
              before[axis] > farthest[axis] + 0.5 && Math.abs(now[axis] - farthest[axis]) <= 1
          )
        ) {
          reason = "clamped";
        } else if (
          keptAllowed &&
          entry.anchors.some(({ element, left, top }) => {
            if (!element.isConnected) return false;
            const own = element.getBoundingClientRect();
            const stands: [number, number] = [own.left, own.top];
            const stood: [number, number] = [left, top];
            return (
              own.height > 0 && axes.every((axis) => Math.abs(stands[axis] - stood[axis]) <= 1)
            );
          })
        ) {
          reason = "view kept";
        } else if (
          edge !== null &&
          !edgeTaken &&
          (entry.box === null || (entry.box !== edge && entry.box.contains(edge)))
        ) {
          const shift = { box: entry.box, dx, dy };
          const was = boxBefore(edge, shift);
          const sightWas = sightOf(edge, shift);
          const partly = meets(was, sightWas) && !whole(was, sightWas);
          const need = (low: number, high: number, from: number, to: number): number =>
            low < from ? low - from : high > to ? high - to : 0;
          const needX = need(was.left, was.right, sightWas.left, sightWas.right);
          const needY = need(was.top, was.bottom, sightWas.top, sightWas.bottom);
          const fits = (moved: number, needed: number) =>
            Math.abs(moved) <= 1 ||
            (Math.sign(moved) === Math.sign(needed) && Math.abs(moved) <= Math.abs(needed) + 1);
          if (partly && fits(dx, needX) && fits(dy, needY) && wholeInSight(edge)) {
            reason = "nearest edge";
            edgeTaken = true;
          }
        }
        if (reason === null && landing !== null && drawnAbove(record, entry.box, dx, dy, landing)) {
          reason = "view kept (drawn above)";
        }
        moves.push({ box: entry.name, reason, x: dx, y: dy });
      }
      return moves;
    };

    window.__landingLibrary = {
      record: () => {
        current?.observer.disconnect();
        const active = document.activeElement ?? document.body;
        const subject = subjectOf(active, 0);
        const existing = new WeakSet<Element>([document.documentElement]);
        for (const element of document.querySelectorAll("*")) existing.add(element);
        const added = new Set<Element>();
        const observer = new MutationObserver((mutations) => {
          for (const mutation of mutations) {
            for (const node of mutation.addedNodes) {
              if (node instanceof Element) added.add(node);
            }
          }
        });
        observer.observe(document.documentElement, { childList: true, subtree: true });
        // The window and every box that scrolls, each with up to twelve elements in view in it,
        // the rows nearest the lost control first: a box that moves while one of them stays where
        // it stood on screen moved by what the layout moved.
        const activeTop = active.getBoundingClientRect().top;
        const boxes: BoxRecord[] = [];
        const scrollers: (Element | null)[] = [null];
        for (const element of document.querySelectorAll("*")) {
          if (element === document.body || element === document.documentElement) continue;
          if (
            element.scrollHeight <= element.clientHeight + 1 &&
            element.scrollWidth <= element.clientWidth + 1
          ) {
            continue;
          }
          const style = getComputedStyle(element);
          if (style.overflowX === "visible" && style.overflowY === "visible") continue;
          scrollers.push(element);
        }
        // Whether each element read so far stays where it is however a box scrolls - a table's
        // head, or a sticky or fixed element - read once for every box.
        const pinning = new Map<Element, boolean>();
        const pinned = (element: Element, root: Element | null): boolean => {
          for (
            let node: Element | null = element;
            node !== null && node !== root;
            node = node.parentElement
          ) {
            let pins = pinning.get(node);
            if (pins === undefined) {
              const { position } = getComputedStyle(node);
              pins = node.tagName === "THEAD" || position === "sticky" || position === "fixed";
              pinning.set(node, pins);
            }
            if (pins) return true;
          }
          return false;
        };
        for (const box of scrollers) {
          const own = box?.getBoundingClientRect();
          const clip =
            own === undefined
              ? { bottom: window.innerHeight, left: 0, right: window.innerWidth, top: 0 }
              : {
                  bottom: Math.min(window.innerHeight, own.bottom),
                  left: Math.max(0, own.left),
                  right: Math.min(window.innerWidth, own.right),
                  top: Math.max(0, own.top),
                };
          const root = box ?? document.body;
          const shown = (selector: string) =>
            [...root.querySelectorAll(selector)].filter((element) => {
              const at = element.getBoundingClientRect();
              return (
                at.height > 0 &&
                at.top >= clip.top - 0.5 &&
                at.bottom <= clip.bottom + 0.5 &&
                at.right > clip.left &&
                at.left < clip.right &&
                !pinned(element, box)
              );
            });
          const rows = shown(rowSelector);
          const anchors = (rows.length > 0 ? rows : shown("*"))
            .map((element) => ({
              distance: Math.abs(element.getBoundingClientRect().top - activeTop),
              element,
            }))
            .sort((one, other) => one.distance - other.distance)
            .slice(0, 12)
            .map(({ element }) => {
              const at = element.getBoundingClientRect();
              return { element, left: at.left, top: at.top };
            });
          const [x, y] = scrollOf(box);
          const name =
            box === null
              ? "window"
              : (
                  box.getAttribute("data-testid") ??
                  box.getAttribute("aria-label") ??
                  box.tagName
                ).toLowerCase();
          boxes.push({ anchors, box, name, x, y });
        }
        let bar: BarRecord | null = null;
        if (subject.regions.some((region) => region.matches(barSelector))) {
          const markedRow =
            markedLast?.isConnected === true && markedLast.checked
              ? markedLast.closest(rowSelector)
              : null;
          const anyMarked =
            markedRow ??
            [...document.querySelectorAll('input[type="checkbox"]:checked')]
              .map((box) => box.closest(rowSelector))
              .find((row) => isListRow(row) && row.closest(barSelector) === null) ??
            null;
          const list = anyMarked?.parentElement?.closest(listSelector) ?? null;
          const stops: Element[] = [];
          const page = main();
          for (
            let stop: Element | null = list;
            stop !== null && stop !== page && stop !== document.body;
            stop = stop.parentElement
          ) {
            stops.push(stop);
          }
          bar = { list, marked: markedRow === null ? null : spotsUp(markedRow), stops };
        }
        const places = new WeakMap<Element, { height: number; top: number }>();
        for (const element of document.body.querySelectorAll("*")) {
          places.set(element, {
            height: flowHeight(element),
            top: element.getBoundingClientRect().top,
          });
        }
        current = { added, bar, boxes, existing, observer, places, subject };
        return { control: nameOf(active), region: nameOf(subject.regions[0] ?? document.body) };
      },
      judge: (caused, named) => {
        const record = current;
        const active = document.activeElement ?? document.body;
        const onBody = active === document.body || active === document.documentElement;
        const topmost = [...document.querySelectorAll(modalSelector)].filter(rendered).at(-1);
        // A dialog or a panel the page drew after the modal one, as a panel a modal dialog's control
        // opens is portalled to the body's end, stands over it: a focus there is not behind it.
        const layer = active.closest(dialogSelector);
        const overModal =
          topmost !== undefined &&
          layer !== null &&
          !topmost.contains(layer) &&
          (topmost.compareDocumentPosition(layer) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0;
        const behindModal =
          topmost !== undefined && !onBody && !topmost.contains(active) && !overModal;
        if (record === null) {
          return {
            behindModal,
            expected: "(no record)",
            got: nameOf(active),
            moves: [],
            named: null,
            onBody,
            passed: "",
            right: false,
            rule: "stop",
          };
        }
        const passed = notes();
        // The landing is computed in the view the reader had: every box that moved since the
        // record is put back where it stood for the computation, and then where it stands now,
        // so a box the page scrolled to its landing does not bring a control into sight that was
        // out of it. The moves themselves are read after, as the page left them.
        const moved = record.boxes.flatMap((entry) => {
          if (entry.box !== null && !entry.box.isConnected) return [];
          const now = scrollOf(entry.box);
          return Math.abs(now[0] - entry.x) > 1 || Math.abs(now[1] - entry.y) > 1
            ? [{ entry, now }]
            : [];
        });
        const scrollTo = (box: Element | null, [left, top]: readonly [number, number]) =>
          (box ?? window).scrollTo({ behavior: "instant", left, top });
        for (const { entry } of moved) scrollTo(entry.box, [entry.x, entry.y]);
        const landing = caused ? causedLanding(record, passed) : uncausedLanding(record, active);
        for (const { entry, now } of [...moved].reverse()) scrollTo(entry.box, now);
        const moves = movesSince(
          record,
          !caused,
          caused ? landing.scrolls : null,
          caused ? landing.element : null
        );
        return {
          behindModal,
          expected: landing.name,
          got: nameOf(active),
          moves,
          named:
            named.length === 0 ? null : landing.element !== null && named.includes(landing.element),
          onBody,
          passed: passed.text(),
          right: landing.accepts(active),
          rule: landing.rule,
        };
      },
      held: () => {
        const record = current;
        const active = document.activeElement ?? document.body;
        if (record === null)
          return { expected: "(no record)", got: nameOf(active), moves: [], right: false };
        const { subject } = record;
        const inner = subject.regions[0] ?? document.body;
        const control =
          subject.lost.isConnected && inTabOrder(subject.lost)
            ? subject.lost
            : !standing(inner) && subject.opener !== null && standing(subject.opener.lost)
              ? subject.opener.lost
              : null;
        const modal =
          control === null
            ? null
            : ([...document.querySelectorAll(modalSelector)]
                .filter((open) => rendered(open) && open.contains(control))
                .at(-1) ?? null);
        const right = control !== null && (active === control || modal?.contains(active) === true);
        return {
          expected:
            control === null
              ? `${nameOf(subject.lost)}, which no longer stands`
              : modal === null
                ? nameOf(control)
                : `${nameOf(control)}, or inside ${nameOf(modal)}`,
          got: nameOf(active),
          moves: movesSince(record, false, null, null),
          right,
        };
      },
    };
  }, LANDING_CONFIG);
}

/** Records, in the page, what the rules need before the cause, with the focus on the control the
 *  cause takes away (the module's header, "WHAT IT READS BEFORE THE CAUSE"). */
export function recordBefore(page: Page): Promise<Recorded> {
  return page.evaluate(() => {
    const library = window.__landingLibrary;
    if (library === undefined) throw new Error("the landing oracle is not installed in this page");
    return library.record();
  });
}

/** Computes, in the page, where the focus is to land after the cause and where it is, with every
 *  box that moved: for a loss the reader caused where `caused`, else for one they did not.
 *  `named` are the elements a case names its landing by, which must hold the computed one. */
export function judgeAfter(
  page: Page,
  caused: boolean,
  named: readonly ElementHandle[]
): Promise<Verdict> {
  return page.evaluate(
    ([isCaused, elements]) => {
      const library = window.__landingLibrary;
      if (library === undefined)
        throw new Error("the landing oracle is not installed in this page");
      return library.judge(isCaused, elements);
    },
    [caused, named] as const
  );
}

/** Reads, in the page, whether the focus is on the control a held press holds, or inside the open
 *  modal dialog around it, and every box that moved since the record. */
export function judgeHeld(page: Page): Promise<HeldVerdict> {
  return page.evaluate(() => {
    const library = window.__landingLibrary;
    if (library === undefined) throw new Error("the landing oracle is not installed in this page");
    return library.held();
  });
}

/** The moves a reading names, as a case's line prints them, each with its reason where it has
 *  one. */
export function scrolledText(moves: readonly Move[]): string {
  const signed = (value: number) => `${value > 0 ? "+" : ""}${Math.round(value)}`;
  return moves.length === 0
    ? "none"
    : moves
        .map(
          ({ box, reason, x, y }) =>
            `${box} ${signed(x)},${signed(y)}px${reason === null ? "" : ` (${reason})`}`
        )
        .join("; ");
}
