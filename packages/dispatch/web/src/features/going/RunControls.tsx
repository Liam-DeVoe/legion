import { type KeyboardEvent, type ReactNode, useEffect, useId, useRef, useState } from "react";

import type { GoingRun } from "../../api/types";
import { pillClassName } from "../../components/Pill";
import { PinButton } from "../../components/PinButton";
import {
  bgTransparent,
  borderDefault,
  card,
  focusWithinBorder,
  inputClasses,
  secondaryButtonBorder,
  secondaryButtonCompact,
  secondaryButtonHoverBorder,
  secondaryButtonText,
  surfaceMutedHoverBg,
  textMutedOnSurface,
  textPrimaryOnSurface,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { conversationTitle, conversationWorking, setLevel, useGoingValue } from "./going-store";
import type { Asking } from "./useAsk";

/** The three modes, in the order the menu lists them, each with the one line on what it is for
 *  (design v3, state 12). R10's budgets are the server's: no model and no number shows here. */
const LEVELS: readonly {
  readonly label: string;
  readonly level: GoingRun["level"];
  readonly use: string;
}[] = [
  { label: "Fast", level: "fast", use: "quick lookups" },
  { label: "Standard", level: "standard", use: "most questions" },
  { label: "Deep", level: "deep", use: "big changes and reports; can take minutes" },
];

/** How far the open mode menu stands from its button, in pixels. */
const MENU_GAP = 4;

/** Where the open mode menu stands in the window: its left edge, and its foot or its top. */
interface MenuPlace {
  readonly left: number;
  readonly top?: number;
  readonly bottom?: number;
}

/** Where the mode menu of `button` opens: above it where more of the window is above it than
 *  below, as at the foot of the bar and of the panel, else below it, as at the top of the bar
 *  before Enter. The menu is fixed to the window, so the bar's frame, which cuts off what
 *  overflows it, never cuts the menu. */
function menuPlace(button: HTMLElement): MenuPlace {
  const box = button.getBoundingClientRect();
  return box.top > window.innerHeight - box.bottom
    ? { bottom: window.innerHeight - box.top + MENU_GAP, left: box.left }
    : { left: box.left, top: box.bottom + MENU_GAP };
}

/**
 * The mode control (design v3, state 12): a small text button at the left of the box that names
 * how much work the next run may do, "Standard ▾", and its menu of the three modes, each with
 * one line on what it is for. A change applies from the next message, as the button's hover
 * says. It is a WAI-ARIA menu button: Enter, Space, Down and Up open the menu with the focus on
 * the mode that is on; Up and Down move in it, wrapping, and Home and End go to its ends; a
 * choice puts the cursor back in the box; Escape closes it and gives the focus back to the
 * button, and leaves the bar open; Tab closes it.
 */
export function LevelControl(): ReactNode {
  const chosen = useGoingValue((of) => of.level);
  const [menu, setMenu] = useState<MenuPlace>();
  const button = useRef<HTMLButtonElement>(null);
  const wrapper = useRef<HTMLDivElement>(null);
  const menuId = useId();
  const open = menu !== undefined;
  const named = LEVELS.find((one) => one.level === chosen)?.label ?? chosen;

  useEffect(() => {
    if (!open) return;
    wrapper.current
      ?.querySelector<HTMLElement>('[role="menuitemradio"][aria-checked="true"]')
      ?.focus();
    // A press outside the control, or a window that changes its size under the menu, closes it.
    const closeOutside = (event: PointerEvent) => {
      if (event.target instanceof Node && wrapper.current?.contains(event.target) === true) return;
      setMenu(undefined);
    };
    const closeOnResize = () => setMenu(undefined);
    document.addEventListener("pointerdown", closeOutside);
    window.addEventListener("resize", closeOnResize);
    return () => {
      document.removeEventListener("pointerdown", closeOutside);
      window.removeEventListener("resize", closeOnResize);
    };
  }, [open]);

  const openMenu = () => {
    if (button.current !== null) setMenu(menuPlace(button.current));
  };
  const choose = (level: GoingRun["level"]) => {
    setLevel(level);
    setMenu(undefined);
    if (!focusConversationBox()) button.current?.focus();
  };
  const walk = (event: KeyboardEvent<HTMLElement>) => {
    if (event.key === "Escape") {
      // The menu's own Escape: the bar's dialog and the panel never hear it.
      event.preventDefault();
      event.stopPropagation();
      setMenu(undefined);
      button.current?.focus();
      return;
    }
    if (event.key === "Tab") {
      // Tab moves on from the button, as the items take no tab stop of their own.
      button.current?.focus();
      setMenu(undefined);
      return;
    }
    const items = [...event.currentTarget.querySelectorAll<HTMLElement>('[role="menuitemradio"]')];
    const at = event.target instanceof HTMLElement ? items.indexOf(event.target) : -1;
    const to =
      at < 0
        ? undefined
        : event.key === "ArrowDown"
          ? (at + 1) % items.length
          : event.key === "ArrowUp"
            ? (at - 1 + items.length) % items.length
            : event.key === "Home"
              ? 0
              : event.key === "End"
                ? items.length - 1
                : undefined;
    if (to === undefined) return;
    event.preventDefault();
    items[to]?.focus();
  };
  return (
    <div className="shrink-0" data-testid="going-level" ref={wrapper}>
      <button
        aria-controls={open ? menuId : undefined}
        aria-expanded={open}
        aria-haspopup="menu"
        className={`min-h-11 rounded-lg px-2 py-1 text-xs font-medium whitespace-nowrap sm:min-h-7 ${textSecondaryOnSurface} ${surfaceMutedHoverBg}`}
        data-testid="going-level-button"
        onClick={() => (open ? setMenu(undefined) : openMenu())}
        // Down and Up open the menu as Enter and Space do.
        onKeyDown={(event) => {
          if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
          event.preventDefault();
          openMenu();
        }}
        ref={button}
        title="How much work the next run may do. A change applies from the next message."
        type="button"
      >
        {named} <span aria-hidden="true">▾</span>
      </button>
      {menu === undefined ? null : (
        <div
          aria-label="Mode"
          className={`fixed z-50 w-max max-w-[calc(100vw-2rem)] rounded-lg border p-1 shadow-lg ${card} ${borderDefault}`}
          id={menuId}
          onKeyDown={walk}
          role="menu"
          style={menu}
        >
          {LEVELS.map(({ label, level, use }) => (
            <button
              aria-checked={level === chosen}
              className={`flex min-h-11 w-full items-baseline gap-2 rounded px-2 py-1.5 text-left text-sm sm:min-h-8 ${textPrimaryOnSurface} ${surfaceMutedHoverBg}`}
              data-level={level}
              key={level}
              onClick={() => choose(level)}
              role="menuitemradio"
              tabIndex={-1}
              type="button"
            >
              <span aria-hidden="true" className="w-3 shrink-0">
                {level === chosen ? "✓" : ""}
              </span>
              <span>
                <span className="font-medium">{label}</span> — {use}
              </span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/** The glyph of "Open over the page": the page, and the bar over it. */
function OverThePageIcon(): ReactNode {
  return (
    <svg aria-hidden="true" className="h-4 w-4" fill="none" viewBox="0 0 16 16">
      <rect
        height="11"
        rx="1.5"
        stroke="currentColor"
        strokeWidth="1.25"
        width="13"
        x="1.5"
        y="2.5"
      />
      <rect fill="currentColor" height="4" rx="0.75" width="7" x="4.5" y="5" />
    </svg>
  );
}

/**
 * The header of the conversation, in the bar and in the panel (design v3, states 2 and 10): the
 * conversation's title, its first question on one line, then History, a new conversation, the
 * button that moves it, and Close. The move is an icon with its words as its tooltip, so the
 * title keeps the room: over the page a pin, "Keep at the side", and Close says its word; at the
 * side the page with the bar over it, "Open over the page", and Close is the panel's ×. Each
 * press is the surface's own.
 */
export function ConversationHeader({
  history,
  move,
  onClose,
  onHistory,
  onMove,
  onNew,
}: {
  readonly history: boolean;
  readonly move: "Keep at the side" | "Open over the page";
  readonly onClose: () => void;
  readonly onHistory: () => void;
  readonly onMove: () => void;
  readonly onNew: () => void;
}): ReactNode {
  const title = useGoingValue(conversationTitle);
  const open = useGoingValue((of) => of.conversation !== undefined || of.starting !== undefined);
  const side = move === "Open over the page";
  return (
    <div
      className={`flex shrink-0 items-center gap-2 border-b px-3 py-1.5 ${borderDefault}`}
      data-testid="going-header"
    >
      {title === "" ? (
        <span className="flex-1" />
      ) : (
        <h2
          className={`min-w-0 flex-1 truncate text-sm font-medium ${textPrimaryOnSurface}`}
          data-testid="going-title"
          title={title}
        >
          {title}
        </h2>
      )}
      <button
        aria-pressed={history}
        className={secondaryButtonCompact}
        data-testid="going-history-button"
        onClick={onHistory}
        type="button"
      >
        History
      </button>
      {open ? (
        <button
          className={secondaryButtonCompact}
          data-testid="going-new"
          onClick={onNew}
          type="button"
        >
          New
        </button>
      ) : null}
      {side ? (
        <button
          aria-label={move}
          className={`flex min-h-11 min-w-11 shrink-0 items-center justify-center rounded-lg md:min-h-8 md:min-w-8 ${secondaryButtonBorder} ${secondaryButtonText} ${secondaryButtonHoverBorder}`}
          data-testid="going-move"
          onClick={onMove}
          title={move}
          type="button"
        >
          <OverThePageIcon />
        </button>
      ) : (
        <PinButton label={move} onClick={onMove} pinned={false} />
      )}
      <button
        aria-label={side ? "Close" : undefined}
        className={secondaryButtonCompact}
        data-testid="going-close"
        onClick={onClose}
        title={side ? "Close" : undefined}
        type="button"
      >
        {side ? <span aria-hidden="true">×</span> : "Close"}
      </button>
    </div>
  );
}

/**
 * What the conversation's box says it does now (design v2, rule 5): "Search or ask…" in the bar
 * before Enter ("Ask…" in the panel, which has no search), then "Ask a follow-up…", "Say what to
 * change…" while a plan waits, and "Answer…" once the agent asked the person a question (R8).
 */
export function boxWords({
  asked,
  open,
  planWaits,
  search,
}: {
  /** Whether the newest run's answer asks the person a question (`asksPerson`). */
  readonly asked: boolean;
  /** Whether a conversation is open, or its first run's start is out. */
  readonly open: boolean;
  /** Whether the newest run waits on its plan, read or not yet read. */
  readonly planWaits: boolean;
  /** Whether Enter in this box searches before a conversation is open: the bar's, not the
   *  panel's. */
  readonly search: boolean;
}): string {
  if (!open) return search ? "Search or ask…" : "Ask…";
  if (planWaits) return "Say what to change…";
  return asked ? "Answer…" : "Ask a follow-up…";
}

/** What Enter does where the box's words do not say it (design v2, Keys, "Bar, a plan waits"):
 *  with words in the box while a plan waits, Enter sends them instead of approving; before the
 *  plan is read, Enter does nothing yet. */
export function boxHint(
  asking: Pick<Asking, "planWaits" | "runWaits">,
  words: string
): string | undefined {
  if (asking.planWaits) {
    return words.trim() === "" ? undefined : "↵ sends your words instead of approving";
  }
  return asking.runWaits ? "Reading the plan…" : undefined;
}

/** The row of the conversation's box on the screen: the bar's or the panel's, one at a time. */
let shownBoxRow: HTMLElement | undefined;

/** Puts the cursor in the conversation's box on the screen ("Say what to do instead", design v2,
 *  state 7; a choice in the mode menu), and answers whether there was one. */
export function focusConversationBox(): boolean {
  const box = shownBoxRow?.querySelector<HTMLElement>("input, textarea");
  box?.focus();
  return box !== undefined && box !== null;
}

/** The input the person types in, inside the box's frame (`BoxFrame`), which draws its border. */
export const boxInputClassName = `block min-w-0 flex-1 px-1.5 py-2 text-sm outline-none ${bgTransparent}`;

/**
 * The conversation's box, in the bar and in the panel (design v3, states 1, 2 and 10): one frame
 * holding the context chip on its left (`chip`, cut where the chip ends, its whole words in
 * `chipTitle`), then `children` - the input, and in the bar before Enter its Filters button - so
 * the chip stands in the same place on every surface.
 */
export function BoxFrame({
  children,
  chip,
  chipTitle,
}: {
  readonly children: ReactNode;
  readonly chip: string | undefined;
  readonly chipTitle: string | undefined;
}): ReactNode {
  return (
    <div
      className={`flex min-w-48 flex-1 items-center gap-1.5 rounded-lg border px-1.5 ${inputClasses(true)} ${focusWithinBorder}`}
    >
      {chip === undefined ? null : (
        <span
          className={`${pillClassName("label")} max-w-48 shrink-0`}
          data-testid="going-box-chip"
          title={chipTitle}
        >
          <span className="min-w-0 truncate">{chip}</span>
        </span>
      )}
      {children}
    </div>
  );
}

/**
 * The row of the conversation's box, in the bar and in the panel (design v3, states 2 and 12):
 * the mode control at its left, the box itself (`box`, its `BoxFrame` with the context chip), and
 * Stop at its right while the run works; under it, the hint of what Enter does (`boxHint`). The
 * box keeps its place among the row's parts in every state, so the element the person types in,
 * and its focus, stay as the row changes. While the row is drawn, `focusConversationBox` puts the
 * cursor in its box.
 */
export function BoxRow({
  box,
  hint,
  onStop,
}: {
  readonly box: ReactNode;
  readonly hint: string | undefined;
  readonly onStop: () => void;
}): ReactNode {
  const working = useGoingValue(conversationWorking);
  const row = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const shown = row.current ?? undefined;
    shownBoxRow = shown;
    return () => {
      if (shownBoxRow === shown) shownBoxRow = undefined;
    };
  }, []);
  return (
    <div className="space-y-1">
      <div className="flex flex-wrap items-center gap-2" ref={row}>
        <LevelControl />
        {box}
        {working ? (
          <button
            className={secondaryButtonCompact}
            data-testid="going-stop"
            onClick={onStop}
            type="button"
          >
            Stop
          </button>
        ) : null}
      </div>
      {hint === undefined ? null : (
        <p className={`text-xs ${textMutedOnSurface}`} data-testid="going-box-hint">
          {hint}
        </p>
      )}
    </div>
  );
}
