import { type KeyboardEvent, type ReactNode, useEffect, useId, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import type { Artifact } from "../../api/types";
import {
  borderTransparent,
  inputClasses,
  pageDecisionText,
  pageFailedText,
  pageHairBorder,
  pageHeaderControl,
  pageInkBorder,
  pageInkText,
  pageMutedText,
  pagePaperBg,
  pageQuietText,
} from "../../theme/classes";
import { useShortcutHelpOpener } from "../shell/shortcut-help-opener";
import { useMediaQuery } from "../shell/useDialog";
import { presetPath } from "./issue-address";
import { MENU_CLASS, MENU_ITEM_CLASS, NodeMenu } from "./NodeMenu";
import { authorText, pageChrome } from "./page-structure";
import {
  CONTROL_LABELS,
  FILTER_SIGNAL_WORDS,
  PRESET_NAMES,
  PRESET_ORDER,
  type Preset,
} from "./page-words";
import type { PresetSignal } from "./presets";

/**
 * The filter row (dispatch://INFRA-1196 R2, R11, R20, and the prototype's view tabs): five
 * controls, Conversation - the page with no filter on - then Spec, Verification, Plan and
 * Decisions, each with its signal; beside them the version control while a filter is on, the
 * node menu, the filter by words and Keys.
 *
 * One filter is on at a time or none. The row is a toolbar of toggle buttons, each carrying
 * `aria-pressed`: pressing a filter turns it on and any other off, pressing the filter that is
 * on turns it off, and pressing Conversation turns every filter off. Each control's state is an
 * address of its own (`issue-address.ts`'s `presetPath`), so pressing one is navigation and a
 * filter survives a reload and a link. The toolbar is one tab stop, the control that is pressed;
 * the arrow keys, Home and End move between the five, and Enter and Space press.
 *
 * A toggle is the whole drawn box, the prototype's `.tab`, so a press anywhere on what is drawn
 * presses it. It is named by its name part (`aria-labelledby`) - the word a voice user says,
 * which the inventory of the page this one replaced holds each control to - and its signal, a
 * count beside the name, is its description (`aria-describedby`).
 *
 * The row has two shapes and no third. Above 1024 px it is the row of five; at 1024 px and below
 * - every phone - it is one control naming the one on, with its signal, which opens a menu of
 * the five, and below 1024 px the Outline control stands beside it (R2, R14). One query and its
 * negation decide the two (`ONE_CONTROL_QUERY`), so no width answers both shapes or neither.
 * Nothing in either shape scrolls sideways: what does not fit wraps (R18).
 */
export function IssuePresets({
  active,
  artifacts,
  filter,
  isClosed,
  issueKey,
  nodeMenu,
  onFilterChange,
  onLeave,
  outline,
  signals,
  toolbar,
}: {
  active: Preset;
  /** Every artifact of the issue, which the node menu lists and which a title typed into its
   *  New document is held against before the write (R3). */
  artifacts: readonly Artifact[];
  /** What the filter by words holds, which is `?q=`. */
  filter: string;
  /** Whether the issue is closed, which refuses the node menu's upload. */
  isClosed: boolean;
  issueKey: string;
  /** Whether the address opens the node menu, which `/artifacts` does. */
  nodeMenu: boolean;
  onFilterChange: (next: string) => void;
  /** Called before this row leaves the address, so the page records where the reader was. */
  onLeave: () => void;
  /** The Outline control, beside the one control below 1024 px, where the Contents pane is not
   *  drawn (R14). */
  outline?: ReactNode;
  /** What each control draws after its name (`presets.ts`'s `filterSignals`). */
  signals: Readonly<Record<Preset, PresetSignal>>;
  /** The version control: the filter's own while one is on, or the opened document's; none
   *  with no filter on (R2). */
  toolbar?: ReactNode;
}): ReactNode {
  const navigate = useNavigate();
  const openKeys = useShortcutHelpOpener();
  // The row of five is drawn exactly where the one control is not.
  const row = !useMediaQuery(ONE_CONTROL_QUERY);
  // The page opens at most one menu at a time, so which one is open is one value.
  const [open, setOpen] = useState<OpenMenu>(nodeMenu ? "nodes" : undefined);
  const wrapper = useRef<HTMLDivElement>(null);
  const toggles = useRef<Partial<Record<Preset, HTMLButtonElement | null>>>({});
  // The ids a control's name and signal are read by; one prefix per drawn row, since two issue
  // pages in one document would otherwise write the same ids.
  const ids = useId();
  const nameId = (preset: Preset) => `${ids}-name-${preset}`;
  const signalId = (preset: Preset) => `${ids}-signal-${preset}`;

  // An address that opens the node menu opens it on arrival, and leaving that address closes it
  // again, so the menu says what the address says.
  useEffect(() => setOpen(nodeMenu ? "nodes" : undefined), [nodeMenu]);
  useEffect(() => {
    if (open === undefined) return;
    const closeOutside = (event: PointerEvent) => {
      if (!(event.target instanceof Node) || wrapper.current?.contains(event.target) !== true) {
        setOpen(undefined);
      }
    };
    // The press is the open menu's alone: the page's own Escape - which turns a filter off, and
    // closes a document opened in place of the outline - waits for the next one.
    const closeOnEscape = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      setOpen(undefined);
    };
    document.addEventListener("pointerdown", closeOutside);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", closeOutside);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, [open]);

  const go = (preset: Preset) => {
    onLeave();
    navigate(presetPath(issueKey, preset));
  };
  // A press on a filter turns it on, or off where it is the one on; Conversation turns every
  // filter off, and is a press that changes nothing while none is on.
  const press = (preset: Preset) => {
    if (preset === active && preset === "conversation") return;
    go(preset === active ? "conversation" : preset);
  };
  const walk = (event: KeyboardEvent<HTMLElement>, from: Preset) => {
    const at = PRESET_ORDER.indexOf(from);
    const to =
      event.key === "ArrowRight"
        ? (at + 1) % PRESET_ORDER.length
        : event.key === "ArrowLeft"
          ? (at - 1 + PRESET_ORDER.length) % PRESET_ORDER.length
          : event.key === "Home"
            ? 0
            : event.key === "End"
              ? PRESET_ORDER.length - 1
              : undefined;
    const next = to === undefined ? undefined : PRESET_ORDER[to];
    if (next === undefined) return;
    event.preventDefault();
    toggles.current[next]?.focus();
  };
  // The control the toolbar's one tab stop rests on, which the arrow keys move from.
  const [stop, setStop] = useState<Preset>(active);
  useEffect(() => setStop(active), [active]);

  // 128 px in the row of five, so the row holds the version control, the node menu, this box
  // and Keys on one line at 1440 px with a filter on; on the one control's second line it takes
  // what the node menu leaves.
  const words = (
    <input
      aria-label={CONTROL_LABELS.filter}
      className={`min-h-11 min-w-0 rounded-[7px] border px-2 text-sm md:min-h-7 ${
        row ? "mb-1 w-32" : "w-32 flex-1"
      } ${inputClasses(false)}`}
      data-testid="issue-filter"
      onChange={(event) => onFilterChange(event.target.value)}
      type="search"
      value={filter}
    />
  );
  const files = (
    <NodeMenu
      artifacts={artifacts}
      isClosed={isClosed}
      issueKey={issueKey}
      onClose={() => setOpen(undefined)}
      onToggle={() => setOpen((shown) => (shown === "nodes" ? undefined : "nodes"))}
      open={open === "nodes"}
    />
  );

  if (!row) {
    return (
      // 2 px above the one control, so the header's band - measured to the top of this row - stays
      // inside ruling 3's limits at 390 px, where the prototype's `#viewbar` pads 8.
      <div className="flex min-w-0 flex-wrap items-center gap-2 pt-0.5 pb-2" ref={wrapper}>
        {/* Named by what it draws - "Spec 21" - since the signal is inside this control and
            nothing holds it to a name of its own (WCAG 2.5.3). */}
        <div className="relative flex min-w-0 flex-1" data-testid="issue-presets">
          <button
            aria-expanded={open === "presets"}
            aria-haspopup="menu"
            className={`flex min-h-11 min-w-0 flex-1 items-center justify-between gap-2 rounded-lg border px-3 py-1.5 text-sm font-semibold ${pageHairBorder} ${pagePaperBg} ${pageInkText}`}
            onClick={() => setOpen((shown) => (shown === "presets" ? undefined : "presets"))}
            type="button"
          >
            <span className="flex min-w-0 items-baseline gap-1.5">
              <span data-preset-name="" {...pageChrome}>
                {PRESET_NAMES[active]}
              </span>
              <Signal preset={active} signal={signals[active]} />
            </span>
            <span aria-hidden="true" {...pageChrome}>
              ▾
            </span>
          </button>
          {open === "presets" ? (
            <div className={MENU_CLASS} role="menu">
              {PRESET_ORDER.map((preset) => (
                <button
                  aria-describedby={signalId(preset)}
                  aria-labelledby={nameId(preset)}
                  className={`${MENU_ITEM_CLASS} flex items-baseline gap-1.5`}
                  key={preset}
                  onClick={() => {
                    setOpen(undefined);
                    if (preset !== active) go(preset);
                  }}
                  role="menuitem"
                  type="button"
                >
                  <span aria-hidden="true" className={`w-3 shrink-0 ${pageInkText}`}>
                    {preset === active ? "●" : ""}
                  </span>
                  <span id={nameId(preset)} {...pageChrome}>
                    {PRESET_NAMES[preset]}
                  </span>
                  <Signal id={signalId(preset)} preset={preset} signal={signals[preset]} />
                </button>
              ))}
            </div>
          ) : null}
        </div>
        {/* After the one control, as the 390 px picture draws it; the node menu's own menu opens
            from where its control stands. */}
        {outline}
        <div className="flex min-w-0 basis-full flex-wrap items-center gap-2">
          {files}
          {words}
        </div>
        {toolbar}
      </div>
    );
  }

  return (
    <div className="flex min-w-0 flex-wrap items-end gap-x-2" ref={wrapper}>
      <div
        aria-label={CONTROL_LABELS.presets}
        className="flex min-w-0 flex-wrap items-end gap-0.5"
        data-testid="issue-presets"
        role="toolbar"
      >
        {PRESET_ORDER.map((preset) => {
          const on = preset === active;
          return (
            // The prototype's `.tab`: the name and the signal on one baseline, the rule under
            // both, ink and semibold for the one on. The button is named by its name part and
            // described by its signal.
            <button
              aria-describedby={signalId(preset)}
              aria-labelledby={nameId(preset)}
              aria-pressed={on}
              className={`-mb-px inline-flex shrink-0 items-baseline gap-1.5 whitespace-nowrap border-b-2 px-3 pt-[7px] pb-1.5 text-sm leading-5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-page-ink ${
                on
                  ? `font-semibold ${pageInkText} ${pageInkBorder}`
                  : `${borderTransparent} font-medium ${pageMutedText}`
              }`}
              key={preset}
              onClick={() => press(preset)}
              onFocus={() => setStop(preset)}
              onKeyDown={(event) => walk(event, preset)}
              ref={(node) => {
                toggles.current[preset] = node;
              }}
              tabIndex={preset === stop ? 0 : -1}
              type="button"
            >
              <span data-preset-name="" id={nameId(preset)} {...pageChrome}>
                {PRESET_NAMES[preset]}
              </span>
              <Signal id={signalId(preset)} preset={preset} signal={signals[preset]} />
            </button>
          );
        })}
      </div>
      {toolbar === undefined ? null : (
        <div className="mb-1 flex min-w-0 flex-wrap items-center gap-2">{toolbar}</div>
      )}
      <div className="mb-1">{files}</div>
      <span className="flex-1" />
      {words}
      {openKeys === undefined ? null : (
        <button
          className={`mb-[7px] ${pageHeaderControl}`}
          onClick={openKeys}
          type="button"
          {...pageChrome}
        >
          {CONTROL_LABELS.keys}
        </button>
      )}
    </div>
  );
}

/** Which of the row's two menus is open; the page opens at most one at a time. */
type OpenMenu = "nodes" | "presets" | undefined;

/** The widths the row is one control at: 1024 px and below (R2). */
const ONE_CONTROL_QUERY = "(max-width: 1024px)";

/**
 * What one control draws after its name: its count, then the noun the count is in, or what of
 * it asks something of the reader, in the prototype's `.sig` (12 px, the quiet grey; the failed
 * red for what failed and the decision violet for what waits for review, each with its word, so
 * the colour repeats what the word says). The count is the author's number and carries
 * `data-preset-count`, which the checks read it by.
 */
function Signal({
  id,
  preset,
  signal,
}: {
  id?: string;
  preset: Preset;
  signal: PresetSignal;
}): ReactNode {
  const { flag, unit } = signal;
  return (
    <span className={`text-xs leading-4 font-medium ${pageQuietText}`} id={id}>
      <span data-preset-count={preset} {...authorText}>
        {signal.count}
      </span>
      {unit === undefined ? null : <span {...pageChrome}> {FILTER_SIGNAL_WORDS[unit]}</span>}
      {flag === undefined ? null : (
        <span
          className={flag.word === "failing" ? pageFailedText : pageDecisionText}
          data-preset-flag={flag.word}
          {...pageChrome}
        >
          {" · "}
          <span {...authorText}>{flag.count}</span> {FILTER_SIGNAL_WORDS[flag.word]}
        </span>
      )}
    </span>
  );
}
