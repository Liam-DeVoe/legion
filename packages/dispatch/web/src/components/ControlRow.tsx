import {
  Children,
  cloneElement,
  type FocusEvent,
  Fragment,
  isValidElement,
  type KeyboardEvent,
  type ReactElement,
  type ReactNode,
  type RefObject,
  useLayoutEffect,
  useRef,
  useState,
} from "react";

import { focusOnDocument } from "../features/shell/roving";
import { observeStrip } from "../hooks/observe-strip";
import {
  borderDefault,
  borderTransparent,
  card,
  focusVisibleRing,
  textSecondaryOnCanvas,
} from "../theme/classes";
import { AnchoredPanel } from "./AnchoredPanel";
import { BarAction, type BarActionProps, type ViewPanelProps } from "./BarAction";
import { barButton, FUNNEL_GLYPH, INFO_GLYPH, SLIDERS_GLYPH } from "./bar-look";
import { Chip } from "./Chip";
import { ColumnsMenu, type ColumnsMenuProps } from "./ColumnsMenu";
import { CONTROL, type ControlSize, controlIn, controlPadding } from "./control-names";
import { DisplayMenu, type DisplayMenuProps } from "./DisplayMenu";
import { type FilterChip, filterChips } from "./filter-chips";
import { useFocusPlace } from "./focus-place";
import { MoreMenu } from "./MoreMenu";
import { PANEL_FRAME, PanelTrigger, type PanelTriggerProps } from "./PanelTrigger";
import { SearchBox, type SearchBoxProps } from "./SearchBox";
import { SegmentedControl, type SegmentedControlProps } from "./SegmentedControl";
import { ViewPanel } from "./ViewPanel";

/** The bar's own width under which it takes its narrow layout. One width, so that each width
 *  has one layout a check can read. */
const NARROW_BELOW_PX = 640;

/** The panel `+N more` opens: a card over the page, as a filter panel is (`PANEL_FRAME`), that
 *  lists the chips the line has no room for one under another, each at its own width. */
const MORE_PANEL_CLASS = `${PANEL_FRAME} items-start gap-2 p-2 ${card}`;

/** **Clear filters** in the row of a page not yet moved onto the slots, at each size: a word
 *  button at the end of the chips' line, as that row drew it. It keeps the row's touch target,
 *  which the compact size gives up only from `md`, where a mouse is. */
const LEGACY_CLEAR = `min-h-11 shrink-0 rounded-lg px-2 text-xs font-medium whitespace-nowrap outline-none focus-visible:ring-2 ${focusVisibleRing} ${textSecondaryOnCanvas}`;
const LEGACY_CLEAR_FILTERS: Record<ControlSize, string> = {
  comfortable: LEGACY_CLEAR,
  compact: `${LEGACY_CLEAR} md:min-h-8`,
};

/** How far past its room a line of chips may reach and still fit. The widths read are rounded to
 *  the layout's own units, so their sum can miss the laid-out line by a fraction of a pixel. */
const SLACK_PX = 0.5;

/** One slot of the bar: its own single line, which cannot wrap, so whatever it holds grows
 *  sideways and never downward. Every slot keeps its width but the search box, which gives way
 *  where the bar is narrow, down to the width of its placeholder. A slot a page not yet on the
 *  slots fills itself (`children`) may wrap inside itself below `sm` (`wraps`), and at every width
 *  in a box narrower than `sm` (`narrow`), as it did before the bar was one line, so the Artifacts
 *  tab's staged upload stays on a phone's screen until the page draws it under the bar.
 *  `data-row-slot` marks it as the container a focused control in it is counted in, for the
 *  bar's focus hand-off. */
function Slot({
  children,
  className,
  gives = false,
  narrow = false,
  wraps = false,
}: {
  children: ReactNode;
  className?: string;
  gives?: boolean;
  narrow?: boolean;
  wraps?: boolean;
}): ReactNode {
  if (children === undefined || children === null) return null;
  const width = gives
    ? "min-w-0 flex-1 basis-64"
    : wraps
      ? narrow
        ? "min-w-0"
        : "min-w-0 sm:shrink-0"
      : "shrink-0";
  const wrap = wraps ? (narrow ? "flex-wrap" : "flex-wrap sm:flex-nowrap") : "flex-nowrap";
  const own = `flex items-center gap-2 ${wrap} ${width}`;
  return (
    <div className={className === undefined ? own : `${own} ${className}`} data-row-slot="">
      {children}
    </div>
  );
}

/** The chips handed the row, one element each, in their order: a fragment's chips are its
 *  children, so a page not yet on the slots may hand an array, a fragment, or both (the Inbox's
 *  agent chip before the chips `filterChips` draws). Each keeps a key of its own, made from its
 *  fragment's, so a chip the line hands to the panel stays the same chip. Anything that is not an
 *  element draws nothing: a chip is a control the row measures and hands the focus to. */
function chipsOf(chips: ReactNode, within = ""): ReactElement[] {
  return Children.toArray(chips).flatMap((chip) => {
    if (!isValidElement<{ children?: ReactNode }>(chip)) return [];
    const key = `${within}${String(chip.key)}`;
    if (chip.type === Fragment) return chipsOf(chip.props.children, `${key}/`);
    return [within === "" ? chip : cloneElement(chip, { key })];
  });
}

/** The props a page handed a slot, or undefined where it handed an element it drew itself, or
 *  nothing. A page not yet moved onto the slots hands its search box, its trigger and its display
 *  menu drawn; T14 moves every page onto the slots and takes the element away. */
function slotProps<T extends object>(slot: T | ReactNode): T | undefined {
  if (typeof slot !== "object" || slot === null || isValidElement(slot) || Array.isArray(slot)) {
    return undefined;
  }
  return slot as T;
}

/** The chips a page handed as props, or undefined where it handed elements it drew itself. */
function chipPropsOf(chips: readonly FilterChip[] | ReactNode): readonly FilterChip[] | undefined {
  if (!Array.isArray(chips)) return undefined;
  const all: readonly unknown[] = chips;
  return all.every((chip) => slotProps<FilterChip>(chip as FilterChip) !== undefined)
    ? (all as readonly FilterChip[])
    : undefined;
}

/** The chips line as the row last read it. */
interface LineLayout {
  /** The width the line's box gives the chips and `+N more` together. */
  readonly room: number;
  /** The gap between two chips, and between the last chip and `+N more`. */
  readonly gap: number;
  /** `+N more`'s width as last drawn, or 0 before it ever was. */
  readonly more: number;
  /** Each chip's width as last drawn in the line, by its key, kept while the panel holds that
   *  chip. A chip the line has not drawn since it came has none. */
  readonly widths: ReadonlyMap<string, number>;
}

/** What one read of the chips line found: the widths of the chips it draws alone, and `+N more`'s
 *  only while it is drawn. */
type LineReading = Omit<LineLayout, "more"> & { readonly more: number | undefined };

/**
 * Reads the chips line as the page lays it out now: `box` holds the strip of the chips the line
 * draws, whose keys are `drawn` in order, and `more`, `+N more` while it is drawn. Null where the
 * page lays the box out nowhere (no room): a test DOM, or a line in a box the page does not draw,
 * which draws every chip until it has room to share out.
 *
 * A chip's width is its own, not the width `max-w-full` cuts a chip wider than the line to: the
 * strip is read as wide as all it holds, for this read alone.
 */
function readLine(
  box: HTMLElement,
  more: HTMLElement | null,
  drawn: readonly string[]
): LineReading | null {
  const room = box.getBoundingClientRect().width;
  const strip = box.firstElementChild;
  if (room === 0 || !(strip instanceof HTMLElement)) return null;
  // Read before the strip is widened, since neither the gap nor `+N more`, which never shrinks,
  // changes with the strip's width: read after it, they would lay the page out once more.
  const gap = Number.parseFloat(getComputedStyle(strip).columnGap) || 0;
  const moreWidth = more?.getBoundingClientRect().width;
  strip.style.flex = "none";
  strip.style.width = "max-content";
  const widths = new Map<string, number>();
  for (const [index, key] of drawn.entries()) {
    const chip = strip.children[index];
    if (chip !== undefined) widths.set(key, chip.getBoundingClientRect().width);
  }
  strip.style.flex = "";
  strip.style.width = "";
  return { gap, more: moreWidth, room, widths };
}

/** `reading` with `before`'s widths kept for the chips of `keys` it did not draw, or `before`
 *  itself where nothing changed, so a line read again unchanged draws nothing again. */
function merged(
  before: LineLayout | null,
  reading: LineReading | null,
  keys: readonly string[]
): LineLayout | null {
  if (reading === null) return null;
  const widths = new Map<string, number>();
  for (const key of keys) {
    const width = reading.widths.get(key) ?? before?.widths.get(key);
    if (width !== undefined) widths.set(key, width);
  }
  const more = reading.more ?? before?.more ?? 0;
  const same =
    before !== null &&
    before.room === reading.room &&
    before.gap === reading.gap &&
    before.more === more &&
    before.widths.size === widths.size &&
    [...widths].every(([key, width]) => before.widths.get(key) === width);
  return same ? before : { gap: reading.gap, more, room: reading.room, widths };
}

/**
 * How many of the chips of `keys`, from the first, the line draws: every one where all of them
 * fit its room, else as many as fit with `+N more` and its gap after them. The first is drawn
 * whatever its width, cut short as `Chip` cuts its words, so the line never shows `+N more`
 * alone. A chip with no width yet is drawn, so the next read measures it (`probe`), save while
 * the panel is open: a chip the panel holds stays there.
 */
function chipsThatFit(keys: readonly string[], line: LineLayout, probe: boolean): number {
  const { gap, more, room, widths } = line;
  let end = -gap;
  for (const key of keys) end += gap + (widths.get(key) ?? Number.POSITIVE_INFINITY);
  if (end <= room + SLACK_PX) return keys.length;
  const limit = room - gap - more;
  let count = 0;
  end = -gap;
  for (const key of keys) {
    const width = widths.get(key);
    if (width === undefined) return probe ? count + 1 : Math.max(count, 1);
    end += gap + width;
    if (end > limit + SLACK_PX) break;
    count += 1;
  }
  return Math.max(count, 1);
}

/** A removal a click has started, which hands the keyboard focus on once it has committed:
 *  `from` is what was clicked - a chip, or **Clear filters** - and `index` is that chip's place
 *  among all the row's chips, the line's first and then the panel's, or -1 for **Clear
 *  filters**, whose focus goes to the row's filter trigger. `panelPlace` is its place among the
 *  panel's chips, for a chip clicked in the panel, else null. */
interface HandOff {
  readonly from: Element;
  readonly index: number;
  readonly panelPlace: number | null;
}

/** What takes the focus in a chip: a chip is a button or a link itself, and a chip may sit in a
 *  box with other controls (a chip given a link, the Inbox's agent chip with the agent's live
 *  link after it). */
const FOCUSABLE = "a[href], button";

/** The chip a click at `target` landed in: the child of `chips` - the line's strip, or the
 *  panel's list - that holds it, a chip or the box a chip is drawn in, and that child's place
 *  among them; null when `target` is `chips` itself. */
function chipHolding(chips: Element, target: EventTarget): { from: Element; index: number } | null {
  if (!(target instanceof Node)) return null;
  const children = Array.from(chips.children);
  const index = children.findIndex((child) => child.contains(target));
  const from = children[index];
  return from === undefined ? null : { from, index };
}

/** Delete takes a focused chip off as Enter and Space do, which a button does by itself. It
 *  presses a chip's own control alone - a child of the strip or the panel's list itself, or a
 *  `Chip` - and never another control drawn in its chip's box, such as a chip's link. */
function deleteChip(event: KeyboardEvent<HTMLElement>): void {
  const { currentTarget: chips, key, target } = event;
  if (key !== "Delete" || !(target instanceof HTMLElement)) return;
  const own =
    (target.parentElement === chips && target.matches(FOCUSABLE)) ||
    target.getAttribute("data-control") === CONTROL.chip;
  if (!own) return;
  event.preventDefault();
  target.click();
}

/** The element of a chip the focus goes to: the chip itself, or the first link or button in the
 *  box its chip is drawn in. */
function focusableOf(chip: Element): HTMLElement | null {
  if (chip instanceof HTMLElement && chip.matches(FOCUSABLE)) return chip;
  return chip.querySelector<HTMLElement>(FOCUSABLE);
}

/** Where the focus goes once nothing of its own place is left: the row's filter trigger, or the
 *  search box in a row with no trigger. */
function triggerOf(root: Element): HTMLElement | null {
  return controlIn(root, CONTROL.panelTrigger) ?? controlIn(root, CONTROL.searchBox);
}

/** The bar's own width, read as it is laid out and whenever it changes size: the window
 *  resized, the drawer opened. 0 where the page lays it out nowhere, a test DOM. */
function useOwnWidth(ref: RefObject<HTMLElement | null>): number {
  const [width, setWidth] = useState(0);
  useLayoutEffect(() => {
    const bar = ref.current;
    if (bar === null) return;
    const read = () => setWidth(bar.getBoundingClientRect().width);
    read();
    if (typeof ResizeObserver === "undefined") return;
    const sizes = new ResizeObserver(read);
    sizes.observe(bar);
    return () => sizes.disconnect();
  }, [ref]);
  return width;
}

/** Whether `action` stands in **More** at every width: kept there by the page, or a check item. */
function keptInMore(action: BarActionProps): boolean {
  return action.more === true || action.checked !== undefined;
}

const NO_ACTIONS: readonly BarActionProps[] = [];
const NO_CHIPS: readonly ReactElement[] = [];

export interface ControlRowProps<TData = unknown> {
  /** The page's actions, in its order: each a quiet button of the bar's line after its empty
   *  space, and an item of **More** where the page keeps it there (`more`, `checked`) and below
   *  640 px of the bar's width. */
  readonly actions?: readonly BarActionProps[];
  /** The chips of the narrowings that are on, in the bar's line after the trigger, behind one
   *  **+N more** where they do not fit, and at the top of the trigger's panel at every width. A
   *  page not yet moved onto the slots hands its chips drawn, each an element, a fragment's
   *  chips its children; T14 takes that form away. */
  readonly chips?: readonly FilterChip[] | ReactNode;
  /** A page's own controls, drawn after the chips: the form of a page not yet moved onto the
   *  slots, which T14 takes away. */
  readonly children?: ReactNode;
  /** The caller's outer layout alone - where the bar sits on the page, and what space it keeps
   *  from what is above and below it. The order inside it is the bar's. */
  readonly className?: string;
  /** What **Clear filters** does: sets the narrowings back to the page's own. Given only while
   *  something its press sets back differs from the page's own, and drawn only where the bar is
   *  640 px wide or wider. */
  readonly clear?: () => void;
  /** A table's columns menu. */
  readonly columns?: ColumnsMenuProps<TData>;
  /** The display menu, under the label its page gives it (**View**, **Group by**), which marks
   *  the current sort and grouping. A page not yet moved onto the slots hands it drawn; T14
   *  takes that form away. */
  readonly display?: DisplayMenuProps | ReactNode;
  /** The page's (i) hint, a `view-panel` trigger in the bar. */
  readonly hint?: ViewPanelProps;
  /** The page's one main action, the bar's last control and its one filled one. */
  readonly main?: BarActionProps;
  /** The row of a page not yet on the slots stands in a box narrower than `sm` at every window
   *  size, so the slot of its own controls (`children`) wraps as it does below `sm`, at every
   *  width. It goes with `children`. */
  readonly narrow?: boolean;
  /** The bar's narrow display rule, which the Inbox and its drawer set: below 640 px of the
   *  bar's width the display menu is an item of **More** rather than a control of the line,
   *  because the Inbox's bar has no room for it there (the measurement of 2026-11-24). **More**
   *  is then drawn while it holds the display menu alone. */
  readonly narrowDisplayInMore?: boolean;
  /** What **Clear filters** does in the row of a page not yet on the slots, beside the trigger
   *  it drew itself. It goes with that `trigger`. */
  readonly onClear?: () => void;
  /** The page's search box, the bar's first control. A page not yet moved onto the slots hands
   *  it drawn; T14 takes that form away. */
  readonly search?: SearchBoxProps | ReactNode;
  /** How much room the bar's controls take (`ControlSize`): the bar hands it to every control
   *  it draws, whatever size a slot's own props name. */
  readonly size?: ControlSize;
  /** The switch (`SegmentedControl`) and a divider after it. */
  readonly switch?: SegmentedControlProps;
  /** **Filter**, the page's panel trigger, which counts the narrowings that differ from the
   *  page's own (`count`). A page not yet moved onto the slots hands its own `PanelTrigger`
   *  drawn; T14 takes that form away. */
  readonly trigger?: Omit<PanelTriggerProps, "control"> | ReactNode;
}

/**
 * The one control bar a page's controls sit in, laid out as established inboxes lay theirs out
 * (GitHub Notifications, Outlook on the web, Linear's Inbox, Front): one line directly above the
 * list, which never wraps onto a second line and never grows. From its start to its end it
 * holds the search box, the switch and a divider after it, **Filter** and **Clear filters**, the
 * chips of the narrowings that are on, empty space, the page's actions, the hint, the columns
 * menu, the display menu, **More** and the page's one main action - in that order on every page,
 * so a reader who learns one page's bar has learned them all. A page hands each control as that
 * control's props, in a slot of its own kind, and the bar draws each shared component itself,
 * at the page's size.
 *
 * Its buttons are quiet - no border and no fill at rest, as Primer's ActionBar and Apple's
 * toolbars draw theirs (`BarLook`) - but the chosen part of the switch, which has a filled
 * surface and a heavier label, and the main action, the one filled button, last, as Apple puts a
 * toolbar's one primary action on its trailing side. Each control writes its name into
 * `data-control`. Below 1280 px of the window's width each keeps the 44 px touch floor
 * `styles.css` sets for every button.
 *
 * **Filter** reads **Filter · N** while N narrowings differ from the page's own, on a tint, with
 * **Clear filters**, a × button, beside it in the same tinted group: two buttons, not one inside
 * the other. Its panel lists each narrowing that is on at its top, above the page's own fields,
 * each with its remove control, at every width: below 640 px it is where the reader takes one off.
 * **More** (⋯) is drawn only while it holds an action: the actions the page keeps there, check
 * items among them, and every action below 640 px of the bar's width.
 *
 * The bar reads its own width (a `ResizeObserver` on the bar, not the window), so a narrow window
 * and a narrow box, the Inbox drawer, take the same rule, and it has two layouts split at 640 px
 * of that width. From 640 px it is as above. Below it the chips leave the line, and **Filter**
 * shows their count as a badge on its glyph, with no **Clear filters** beside it. A bar with
 * chips and no **Filter** - the priorities table's - keeps one `+N more` chip in the line, and no
 * other chip, which lists them all. Every action moves into **More**, and **Filter**, the display
 * menu, **More** and the main action show their glyphs alone, each keeping its name for a screen
 * reader. The switch keeps its labels, but a part the page gives a short label draws it, its name
 * unchanged (the Inbox's **All** and **You · N**); and where the page sets the bar's narrow
 * display rule (`narrowDisplayInMore`, the Inbox and its drawer) the display menu is an item of
 * **More** too. A bar laid out nowhere, as in a test DOM, takes the wide layout. A panel open
 * when the bar crosses 640 px stays open on its trigger, which the bar keeps.
 *
 * A page not yet moved onto the slots - one that hands the row a control it drew itself (its
 * `trigger`, its own `children`, or an element for the search box, the chips or the display menu)
 * and none of the slots only the bar has - is drawn as the row was before this bar, so its pages
 * and their checks keep every place they read: a first line of its search box, its trigger, its
 * own controls and its display menu, one line from `sm` up and wrapping below it, and while a
 * chip is on a second line of the chips and **Clear filters**, which stands right after the
 * trigger while none is. Its chips are fitted and hand the focus on by the rules below. T14 moves
 * every page onto the slots and takes that row away.
 *
 * The bar never grows, which is the height rule: applying a narrowing, opening a panel, opening
 * the display menu, selecting rows or showing an action each leave the top of the list's first
 * row where it was. The whole arithmetic is these points:
 *
 * - the bar is one flex line (`flex-nowrap`), its items centred, so its height is its tallest
 *   control's, one control at the bar's size;
 * - every slot lays its own nodes out in one line, so a slot handed more to draw grows sideways,
 *   never downward;
 * - the chips stand in that line, in the box the empty space is, which takes the line's room the
 *   rest of it leaves and gives way rather than push a control past the edge. The line draws the
 *   chips that fit that box, from the first, and a chip that does not fit is not drawn in it: one
 *   `+N more` chip at the end of the chips stands for every chip left out;
 * - the panels the bar's triggers open, the panel `+N more` opens and the selection bar take no
 *   room in it: `AnchoredPanel` portals to the body and is placed `fixed`, and `SelectionBar`
 *   draws over the list.
 *
 * The search box never narrows past a placeholder that names what it searches: its slot is never
 * narrower than the placeholder's own width in the box's own font.
 *
 * What fits the chips' box is measured, not guessed: the box the chips and `+N more` share, less
 * `+N more`'s own width and its gap, against each chip's own width and the gaps between them.
 * Where every chip fits the whole box, no `+N more` is drawn. The first chip is drawn whatever its
 * width, cut short as `Chip` cuts its words, so the line never shows `+N more` alone, but in the
 * narrow layout of a bar with no trigger. A chip's width is read when the line draws it and kept
 * while the panel holds it, so the line is drawn right at once when a chip is taken off: a chip
 * after it that now fits moves into the line, and `+N` counts down, or goes once every chip fits.
 * The line is read again after a commit that changed what it draws, and whenever its box, the
 * strip or `+N more` changes size (`observeStrip`): a window resized, a chip's name arriving.
 *
 * `+N more` is named for what it opens (`2 more filters`), and opens one panel (`AnchoredPanel`)
 * that lists the chips the line left out, as the same elements the line draws, each with its own
 * remove action. Each chip is drawn once, in the line or in that panel, so no id is drawn twice
 * there. The panel's focus follows every panel's rules: it opens with the focus on its first
 * chip, and Escape, or Tab or Shift+Tab past its last or first chip, close it and hand the focus
 * back to `+N more`.
 *
 * A chip or **Clear filters** that is taken off hands the keyboard focus on rather than drop it
 * onto the page, where a keyboard reader would start again from the page's first link for each
 * chip. A chip taken off - by Enter, Space or Delete, or by a pointer, under the same rule - hands
 * it to the chip now at its place, else to the chip before it, else to the bar's filter trigger
 * (`data-control="panel-trigger"`, or the search box in a bar with no trigger), and **Clear
 * filters** hands it to the trigger. A chip's place is among all the bar's chips, the line's
 * first and then the panel's, so a chip that moved into the line in its place takes it. A chip
 * taken off in the panel while the panel stays hands it to the panel's chip now at its place, else
 * to the one before it, since the panel is where the reader is. The panel's last chip taken off
 * closes the panel, and the focus goes by the line's rule: to the chip now at its place, else to
 * the last chip of the line. Delete presses a chip's own control alone - the child of the strip
 * or of the panel's list itself, or a `Chip` - and never another control drawn in its chip's box,
 * such as a chip's link. The hand-off waits for the removal to commit, whatever holds the focus
 * meanwhile (a pointer's click in Safari focuses no button, so the focus stays on the body), and
 * then moves the focus only if it is on the page's body, so a reader who moved it elsewhere while
 * the chip was going keeps it there. A chip taken off in the trigger's panel is the panel's own,
 * and `PanelTrigger` hands its focus on inside the panel.
 *
 * Any other focused control a commit takes out of a slot while the bar stays hands the focus the
 * same way (`useFocusPlace`): to the control now at its place in that slot, else to the nearest
 * one before it, passing over every control that takes typed text, else to the trigger, else to
 * the search box, each only while the reader can see it, else to the own stop of the region the
 * bar is in - the search bar's dialog, else the main content - with no scroll. Everything after
 * the empty space is one slot, so an action that removes itself hands the focus to the control
 * after it, and **More** that goes once its last action leaves (**Mark all read** no longer
 * offered) hands it to the main action, else to the control before it. An action of the line
 * that holds the focus when the bar narrows past 640 px, and so moves into **More**, hands the
 * focus to **More**'s trigger. **Clear filters**' own rule, the trigger, comes first wherever it
 * sits. A control of a dialog, a panel or a selection bar a page draws in a slot is that region's
 * and none of the bar's, so the bar hands its focus nowhere. In a dialog or a panel, the page's
 * rescue answers a loss the reader caused inside that region, never with a control of the bar
 * outside it. In a selection bar, the bar's own hand-off (`SelectionBar`) answers a loss the
 * reader caused. The page's rescue leaves a loss there that the reader did not cause as
 * `leaveUncaused` says.
 */
export function ControlRow<TData = unknown>({
  actions = NO_ACTIONS,
  chips,
  children,
  className,
  clear,
  columns,
  display,
  hint,
  main,
  narrow = false,
  narrowDisplayInMore = false,
  onClear,
  search,
  size = "comfortable",
  switch: segments,
  trigger,
}: ControlRowProps<TData>): ReactNode {
  const rowRef = useRef<HTMLDivElement>(null);
  const handOff = useRef<HandOff | null>(null);
  // The box the chips and `+N more` share: state rather than a ref, so its observers start when
  // it is drawn and stop when it goes.
  const [box, setBox] = useState<HTMLDivElement | null>(null);
  const moreRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const [layout, setLayout] = useState<LineLayout | null>(null);
  const [open, setOpen] = useState(false);
  // The action of the bar's line that holds the focus, noted as it takes it, for the hand-off to
  // **More** when the bar narrows and the action moves there.
  const focusedAction = useRef<Element | null>(null);

  const searchProps = slotProps<SearchBoxProps>(search);
  const triggerProps = slotProps<Omit<PanelTriggerProps, "control">>(trigger);
  const displayProps = slotProps<DisplayMenuProps>(display);
  const barChips = chipPropsOf(chips);
  // A page not yet moved onto the slots - one that hands the row controls it drew itself, and
  // none of the slots only the bar has - is drawn as the row was drawn before the one bar.
  const onSlots =
    triggerProps !== undefined ||
    clear !== undefined ||
    segments !== undefined ||
    actions.length > 0 ||
    hint !== undefined ||
    columns !== undefined ||
    main !== undefined;
  const legacy =
    !onSlots &&
    (trigger !== undefined ||
      onClear !== undefined ||
      children !== undefined ||
      (search !== undefined && searchProps === undefined) ||
      (chips !== undefined && barChips === undefined) ||
      (display !== undefined && displayProps === undefined));
  const hasTrigger = trigger !== undefined && trigger !== null;
  const width = useOwnWidth(rowRef);
  const narrowBar = !legacy && width > 0 && width < NARROW_BELOW_PX;

  const drawnChips =
    barChips === undefined
      ? chipsOf(chips as ReactNode)
      : chipsOf(filterChips(barChips, size) ?? null);
  // Below 640 px a trigger whose badge stands for the chips takes them out of the line, and a bar
  // with no trigger keeps every chip behind one `+N more`.
  const all = narrowBar && hasTrigger ? NO_CHIPS : drawnChips;
  const keys = all.map((chip) => String(chip.key));
  const shown = narrowBar ? 0 : layout === null ? all.length : chipsThatFit(keys, layout, !open);
  const inLine = all.slice(0, shown);
  const inPanel = all.slice(shown);
  // `+N more` goes once every chip fits the line, and its panel with it: the next `+N more` opens
  // a panel afresh rather than one left open. The row draws again at once with the panel shut,
  // before anything is committed, so no drawn row holds the panel open with no chip in it.
  if (open && inPanel.length === 0) setOpen(false);
  const moreName = `${inPanel.length} more ${inPanel.length === 1 ? "filter" : "filters"}`;

  const inMore = narrowBar ? actions : actions.filter(keptInMore);
  const inBar = narrowBar ? NO_ACTIONS : actions.filter((action) => !keptInMore(action));
  // The display menu, below 640 px, as **More**'s first item where the page sets the bar's narrow
  // display rule.
  const displayInMore = narrowBar && narrowDisplayInMore && displayProps !== undefined;
  // The display menu as this render draws it, which the effect below reads as the layout changes.
  const displayNow = useRef(displayProps);
  displayNow.current = displayProps;

  // Reads the line again, against what this commit drew. A read lays the page out, so a commit
  // reads it only when what the line draws, or what `+N more` counts, has changed; a change of
  // size the line's observers report reads it whatever it draws.
  const measure = useRef(() => {});
  const measuredFor = useRef("");
  useLayoutEffect(() => {
    const drawn = keys.slice(0, shown);
    measure.current = () => {
      if (box === null) return;
      const reading = readLine(box, moreRef.current, drawn);
      setLayout((before) => merged(before, reading, keys));
    };
    const drawing = `${inPanel.length}\n${drawn.join("\n")}`;
    if (box === null) {
      // A line drawn afresh is read afresh, whatever it draws.
      measuredFor.current = "";
      return;
    }
    if (drawing === measuredFor.current) return;
    measuredFor.current = drawing;
    measure.current();
  });
  useLayoutEffect(() => {
    if (box === null) return;
    return observeStrip(box, () => measure.current(), { childList: true });
  }, [box]);
  // After every commit of the row: a removal a click started hands the focus on once it has
  // committed.
  useLayoutEffect(() => {
    const pending = handOff.current;
    if (pending === null) return;
    // What was clicked is still in the page: its removal commits later, whatever holds the
    // focus meanwhile.
    if (pending.from.isConnected) return;
    handOff.current = null;
    // Gone: a reader who has moved the focus elsewhere meanwhile keeps it there.
    if (!focusOnDocument()) return;
    const root = rowRef.current;
    if (root === null) return;
    // The chips as they are drawn now: the line's, read through the row, and the panel's while
    // it stands. A chip clicked in a panel that still stands is placed among the panel's chips,
    // and any other among all the row's.
    const line = Array.from(root.querySelector("[data-chip-strip]")?.children ?? []);
    const panel = Array.from(panelRef.current?.children ?? []);
    const panelPlace = panel.length > 0 ? pending.panelPlace : null;
    const among = panelPlace === null ? [...line, ...panel] : panel;
    const at = panelPlace ?? pending.index;
    const chip = at === -1 ? undefined : (among[at] ?? among[at - 1]);
    const next = chip === undefined ? triggerOf(root) : focusableOf(chip);
    next?.focus();
  });
  // An action of the line that held the focus as the bar narrowed has moved into **More**: the
  // focus goes to **More**'s trigger, where the action now is. It runs before the slots' hand-off
  // below, which then finds the focus placed.
  useLayoutEffect(() => {
    const held = focusedAction.current;
    if (held === null || held.isConnected) return;
    focusedAction.current = null;
    if (!narrowBar || !focusOnDocument() || rowRef.current === null) return;
    controlIn(rowRef.current, CONTROL.moreMenu)?.focus();
  }, [narrowBar]);
  // An open display menu whose trigger moves into **More** as the bar narrows closes with it -
  // its panel hung off a trigger that is gone, and opened again it would stand over **More**
  // unasked - and the focus it held goes to **More**'s trigger, where the menu now is, as an
  // action's does above.
  const displayWasInMore = useRef(displayInMore);
  useLayoutEffect(() => {
    if (displayWasInMore.current === displayInMore) return;
    displayWasInMore.current = displayInMore;
    const menu = displayNow.current;
    if (!displayInMore || menu === undefined || !menu.open) return;
    menu.onOpenChange(false);
    if (focusOnDocument() && rowRef.current !== null) {
      controlIn(rowRef.current, CONTROL.moreMenu)?.focus();
    }
  }, [displayInMore]);
  // A focused control a commit takes out of a slot hands the focus on too. Its check runs after
  // the chips' hand-off above, so that one's rule for Clear filters wins.
  const noteFocus = useFocusPlace(
    (element) => {
      const slot = element.closest("[data-row-slot]");
      return slot !== null && rowRef.current?.contains(slot) === true ? slot : null;
    },
    () => (rowRef.current === null ? null : triggerOf(rowRef.current))
  );
  const onFocus = (event: FocusEvent<HTMLDivElement>) => {
    noteFocus(event);
    const target = event.target;
    focusedAction.current =
      target instanceof Element && target.closest("[data-bar-actions]") !== null ? target : null;
  };

  /** **Clear filters**: a × button beside the trigger, which hands the focus to the trigger once
   *  its press has committed. */
  const clearButton = (press: () => void, tinted: boolean, joined: boolean) => (
    <button
      aria-label="Clear filters"
      className={barButton(
        size,
        tinted ? "tint" : "quiet",
        true,
        joined ? "rounded-r-lg" : "rounded-lg"
      )}
      onClick={(event) => {
        handOff.current = { from: event.currentTarget, index: -1, panelPlace: null };
        press();
      }}
      title="Clear filters"
      type="button"
    >
      <span aria-hidden="true">×</span>
    </button>
  );

  // The chips, in the bar's empty space: the box takes the room the rest of the line leaves,
  // and gives way rather than push a control after it past the bar's edge. In the row of a page
  // not yet on the slots, it is the chips' own line.
  const chipsBox = (
    <div className="flex min-w-0 flex-1 flex-nowrap items-center gap-2" ref={setBox}>
      {/* `[&>*]:shrink-0`: the line never squeezes a chip it draws. A box a chip is drawn in
          with its link could otherwise shrink narrower than its text, and the next chip would
          sit over its end and take its clicks. The strip itself gives way, so a first `Chip`
          wider than the line is cut short by its own `max-w-full`. */}
      {/* biome-ignore lint/a11y/noStaticElementInteractions: nothing here is made interactive - the handlers only act on a chip of the strip, which is a button or a link of its own: they note which chip a click takes off, and press the focused chip on Delete, so the strip needs no role of its own. */}
      <div
        className="flex min-w-0 flex-nowrap items-center gap-2 [&>*]:shrink-0"
        data-chip-strip=""
        onClickCapture={(event) => {
          const held = chipHolding(event.currentTarget, event.target);
          if (held !== null) handOff.current = { ...held, panelPlace: null };
        }}
        onKeyDown={deleteChip}
      >
        {inLine}
      </div>
      {inPanel.length === 0 ? null : (
        <Chip
          aria-expanded={open}
          aria-haspopup="dialog"
          aria-label={moreName}
          onClick={() => setOpen((was) => !was)}
          ref={moreRef}
          size={size}
        >
          {`+${inPanel.length} more`}
        </Chip>
      )}
      {open ? (
        <AnchoredPanel
          anchorRef={moreRef}
          className={MORE_PANEL_CLASS}
          label={moreName}
          onDismiss={() => setOpen(false)}
          role="dialog"
        >
          {/* The panel's chips, without a box of their own (`contents`), so the panel lays them
              out one under another. */}
          {/* biome-ignore lint/a11y/noStaticElementInteractions: nothing here is made interactive - the handlers only act on a chip of the panel, which is a button or a link of its own: they note which chip a click takes off, and press the focused chip on Delete, so the list needs no role of its own. */}
          <div
            className="contents"
            onClickCapture={(event) => {
              const held = chipHolding(event.currentTarget, event.target);
              if (held === null) return;
              handOff.current = {
                from: held.from,
                index: shown + held.index,
                panelPlace: held.index,
              };
            }}
            onKeyDown={deleteChip}
            ref={panelRef}
          >
            {inPanel}
          </div>
        </AnchoredPanel>
      ) : null}
    </div>
  );

  // The box's own placeholder, drawn with no height and seen by nobody, is what keeps the slot
  // from narrowing past it: its width in the box's font - `text-sm`, and the 16 px `styles.css`
  // gives every field below 1280 px - with the box's own side padding (`controlPadding`'s, with
  // none of its height) and border, and the room a hint takes at the box's end.
  const searchPadding = controlPadding[size]
    .split(" ")
    .filter((name) => name.startsWith("px-"))
    .join(" ");
  const searchSlot =
    searchProps === undefined ? (
      <Slot gives>{search as ReactNode}</Slot>
    ) : (
      <div className="flex flex-1 basis-64 flex-col" data-row-slot="">
        {searchProps.placeholder === undefined ? null : (
          <span
            aria-hidden="true"
            className={`invisible h-0 border-x text-sm whitespace-pre max-xl:text-base ${searchPadding} ${searchProps.hint === undefined ? "" : "pr-8"} ${borderTransparent}`}
          >
            {searchProps.placeholder}
          </span>
        )}
        <SearchBox {...searchProps} className="w-full" size={size} />
      </div>
    );

  if (legacy) {
    // The row of a page not yet moved onto the slots, as it was drawn before the one bar: a
    // first line of the search box, the trigger, the page's own controls and the display menu,
    // one line from `sm` up and wrapping below it (and at every width in a box narrower than
    // `sm`, `narrow`); and while a chip is on, a second line of the chips and **Clear filters**,
    // which stands right after the trigger while none is. T14 moves every page onto the slots
    // and takes this row away.
    const chipsOn = all.length > 0;
    const legacyClear =
      onClear === undefined ? null : (
        <button
          className={LEGACY_CLEAR_FILTERS[size]}
          onClick={(event) => {
            handOff.current = { from: event.currentTarget, index: -1, panelPlace: null };
            onClear();
          }}
          type="button"
        >
          Clear filters
        </button>
      );
    const own = "flex min-w-0 flex-col gap-2";
    return (
      // biome-ignore lint/a11y/noStaticElementInteractions: nothing here is made interactive - the handler only notes which control of a slot took the focus, for the hand-off when a commit takes that control away (`useFocusPlace`), so the row needs no role of its own.
      <div
        className={className === undefined ? own : `${own} ${className}`}
        data-control={CONTROL.controlRow}
        onFocus={onFocus}
        ref={rowRef}
      >
        <div
          className={`flex min-w-0 flex-wrap items-start gap-2 ${narrow ? "" : "sm:flex-nowrap"}`}
        >
          {searchSlot}
          <Slot>
            {chipsOn || legacyClear === null ? (
              (trigger as ReactNode)
            ) : (
              <>
                {trigger as ReactNode}
                {legacyClear}
              </>
            )}
          </Slot>
          <Slot narrow={narrow} wraps>
            {children}
          </Slot>
          <Slot className="ml-auto">{display as ReactNode}</Slot>
        </div>
        {chipsOn ? (
          <div className="flex min-w-0 flex-nowrap items-center gap-2">
            {chipsBox}
            {legacyClear}
          </div>
        ) : null}
      </div>
    );
  }

  // **Filter** and **Clear filters**, one group. The trigger's panel lists the chips at its top,
  // above the page's own fields, each with its remove control.
  let triggerSlot: ReactNode = null;
  if (triggerProps !== undefined) {
    const tinted = triggerProps.count !== undefined && triggerProps.count > 0;
    const clearShown = clear !== undefined && !narrowBar;
    const fields = triggerProps.children;
    triggerSlot = (
      <div className="flex shrink-0 items-stretch" data-row-slot="">
        <PanelTrigger
          {...triggerProps}
          look={{ icon: narrowBar ? FUNNEL_GLYPH : undefined, joined: clearShown }}
          size={size}
        >
          {(close) => (
            <>
              {barChips === undefined || barChips.length === 0 ? null : (
                <div className="flex w-full flex-col items-start gap-2">
                  {filterChips(barChips, size)}
                </div>
              )}
              {fields(close)}
            </>
          )}
        </PanelTrigger>
        {clearShown ? clearButton(clear, tinted, true) : null}
      </div>
    );
  } else if (hasTrigger) {
    // A trigger a page drew itself, beside slots of the bar.
    triggerSlot = <Slot>{trigger as ReactNode}</Slot>;
  }

  // Everything after the empty space is one slot, for the focus hand-off: the actions of the
  // line, the hint, the columns menu, the display menu, **More** and the main action.
  const end: ReactNode[] = [];
  if (inBar.length > 0) {
    end.push(
      <span className="contents" data-bar-actions="" key="actions">
        {inBar.map((action) => (
          <BarAction {...action} key={action.label} size={size} />
        ))}
      </span>
    );
  }
  if (hint !== undefined) {
    end.push(<ViewPanel {...hint} key="hint" look={{ icon: INFO_GLYPH }} size={size} />);
  }
  if (columns !== undefined) {
    end.push(<ColumnsMenu {...columns} key="columns" look={{}} size={size} />);
  }
  // The display menu, in its own place, or below 640 px as **More**'s first item.
  if (displayProps !== undefined && !displayInMore) {
    end.push(
      <DisplayMenu
        {...displayProps}
        key="display"
        look={{ icon: narrowBar ? SLIDERS_GLYPH : undefined }}
        size={size}
      />
    );
  } else if (displayProps === undefined && display !== undefined && display !== null) {
    end.push(<Fragment key="display">{display as ReactNode}</Fragment>);
  }
  if (inMore.length > 0 || displayInMore) {
    end.push(
      <MoreMenu
        actions={inMore}
        display={displayInMore ? displayProps : undefined}
        key="more"
        size={size}
      />
    );
  }
  if (main !== undefined) {
    end.push(<BarAction {...main} filled iconOnly={narrowBar} key="main" size={size} />);
  }

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: nothing here is made interactive - the handler only notes which control of a slot took the focus, for the hand-off when a commit takes that control away (`useFocusPlace`), so the bar needs no role of its own.
    <div
      className={`flex min-w-0 flex-nowrap items-center gap-2${className === undefined ? "" : ` ${className}`}`}
      data-control={CONTROL.controlRow}
      onFocus={onFocus}
      ref={rowRef}
    >
      {searchSlot}
      {segments === undefined ? null : (
        <>
          <Slot>
            <SegmentedControl {...segments} narrow={narrowBar} size={size} />
          </Slot>
          <span
            aria-hidden="true"
            className={`h-6 shrink-0 border-l ${borderDefault}`}
            data-bar-divider=""
          />
        </>
      )}
      {triggerSlot}
      {chipsBox}
      <Slot narrow={narrow} wraps>
        {children}
      </Slot>
      <Slot>{end.length === 0 ? null : end}</Slot>
    </div>
  );
}
