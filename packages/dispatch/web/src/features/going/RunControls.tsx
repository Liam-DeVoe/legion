import { type ReactNode, useEffect, useRef } from "react";

import type { GoingRun } from "../../api/types";
import { pillClassName } from "../../components/Pill";
import { PinButton } from "../../components/PinButton";
import {
  borderDefault,
  secondaryButtonCompact,
  textMutedOnSurface,
  textPrimaryOnSurface,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { conversationTitle, conversationWorking, setLevel, useGoingValue } from "./going-store";
import type { Asking } from "./useAsk";

/** The three levels, in the order the control lists them (R10's budgets are the server's). */
const LEVELS: readonly { readonly label: string; readonly level: GoingRun["level"] }[] = [
  { label: "Fast", level: "fast" },
  { label: "Standard", level: "standard" },
  { label: "Deep", level: "deep" },
];

/** How much work the next run may do: Fast, Standard or Deep, beside the box. */
export function LevelControl(): ReactNode {
  const chosen = useGoingValue((of) => of.level);
  return (
    <fieldset
      className={`flex shrink-0 items-center gap-2 text-xs ${textSecondaryOnSurface}`}
      data-testid="going-level"
    >
      <legend className="sr-only">How much work a run may do</legend>
      {LEVELS.map(({ label, level }) => (
        <label className="flex cursor-pointer items-center gap-1" key={level}>
          <input
            checked={chosen === level}
            className="size-3.5 cursor-pointer"
            name="going-level"
            onChange={() => setLevel(level)}
            type="radio"
            value={level}
          />
          {label}
        </label>
      ))}
    </fieldset>
  );
}

/**
 * The header of the conversation, in the bar and in the panel (design v2, state 2): the
 * conversation's title, its first question on one line, then History, a new conversation, the
 * button that moves it (a pin, "Keep at the side", over the page; "Over the page" at the side)
 * and Close. Each press is the surface's own.
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
  readonly move: "Keep at the side" | "Over the page";
  readonly onClose: () => void;
  readonly onHistory: () => void;
  readonly onMove: () => void;
  readonly onNew: () => void;
}): ReactNode {
  const title = useGoingValue(conversationTitle);
  const open = useGoingValue((of) => of.conversation !== undefined || of.starting !== undefined);
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
      {move === "Keep at the side" ? (
        <PinButton label={move} onClick={onMove} pinned={false} />
      ) : (
        <button
          className={secondaryButtonCompact}
          data-testid="going-move"
          onClick={onMove}
          type="button"
        >
          {move}
        </button>
      )}
      <button
        className={secondaryButtonCompact}
        data-testid="going-close"
        onClick={onClose}
        type="button"
      >
        Close
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
 *  state 7), and answers whether there was one. */
export function focusConversationBox(): boolean {
  const box = shownBoxRow?.querySelector<HTMLElement>(
    'input:not([type="radio"]):not([type="checkbox"]), textarea'
  );
  box?.focus();
  return box !== undefined && box !== null;
}

/**
 * The row of the conversation's box, in the bar and in the panel (design v2, state 2): the
 * context chip on its left (`chip`, none where the surface shows its context elsewhere), the box
 * itself (`box`), the level control beside it, and Stop at its right while the run works; under
 * it, the hint of what Enter does (`boxHint`). The box keeps its place among the row's parts in
 * every state, so the element the person types in, and its focus, stay as the row changes. While
 * the row is drawn, `focusConversationBox` puts the cursor in its box.
 */
export function BoxRow({
  box,
  chip,
  hint,
  onStop,
}: {
  readonly box: ReactNode;
  readonly chip: string | undefined;
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
        {chip === undefined ? null : (
          <span
            className={`${pillClassName("label")} max-w-48`}
            data-testid="going-box-chip"
            title={chip}
          >
            <span className="min-w-0 truncate">{chip}</span>
          </span>
        )}
        {box}
        <LevelControl />
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
