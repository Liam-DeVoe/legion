import {
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
  type SyntheticEvent,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";

import { focusOnDocument } from "../features/shell/roving";
import {
  borderDefault,
  card,
  focusVisibleRing,
  kbdHint,
  selectedCardBg,
  surfaceMutedHoverBg,
  textMutedOnSurface,
  textPrimaryOnSurface,
} from "../theme/classes";
import { AnchoredPanel } from "./AnchoredPanel";

/** One choice of a menu. */
export interface MenuChoice<V extends string> {
  readonly value: V;
  readonly label: string;
  /** The digit that writes this choice at once while the menu is open, drawn beside it. */
  readonly digit?: string;
}

interface ChoiceMenuProps<V extends string> {
  /** The trigger's and the menu's accessible name: "Snooze <name>", "Priority of <KEY>",
   *  "Status". */
  readonly label: string;
  readonly choices: readonly MenuChoice<V>[];
  /** The value a menu of values holds now - a priority, a status. Given, its choices are radios
   *  (`menuitemradio`), it opens on this one, the trigger is described by what it draws, and
   *  choosing this value again writes nothing. Absent, the choices are actions (a snooze), and
   *  the menu opens on the first. */
  readonly checked?: V;
  readonly onChoose: (value: V) => void;
  /** The trigger takes no press and no focus: a closed issue, a card in Done. */
  readonly disabled?: boolean;
  /** The trigger keeps its focus and its Tab stop but opens nothing (`aria-disabled`): a write of
   *  its own is out, and a control disabled under the reader's focus drops it on the document. */
  readonly held?: boolean;
  /** The trigger's marks, by which the keys find it and press it (`data-inbox-snooze`,
   *  `data-priority-menu`, `data-status-menu`). */
  readonly marks: Readonly<Record<`data-${string}`, string>>;
  readonly triggerClassName: string;
  /** What the trigger draws: the badge or the pill that shows the value. */
  readonly children: ReactNode;
}

/** A press inside the trigger or the menu stays there: a board card is a drag activator, and an
 *  Inbox row selects itself on a click, so neither may see the press that opens or picks. The menu
 *  is portalled to the body, but it is a React child of the control, so its presses would bubble
 *  to the same card. */
const keepPress = (event: SyntheticEvent) => event.stopPropagation();

/** Where a key moves the highlight from `index` in a menu of `count`, or undefined for a key that
 *  moves nothing. The arrows wrap at the ends, as the menu button pattern allows. */
function stepTo(key: string, index: number, count: number): number | undefined {
  if (key === "ArrowDown") return index === count - 1 ? 0 : index + 1;
  if (key === "ArrowUp") return index === 0 ? count - 1 : index - 1;
  if (key === "Home") return 0;
  if (key === "End") return count - 1;
  return undefined;
}

/** The digits a menu takes, as its foot line names them: a run ("1–4", "0–3"), else each. */
function digitsText(choices: readonly MenuChoice<string>[]): string | undefined {
  const digits = choices
    .flatMap((choice) => (choice.digit === undefined ? [] : [Number(choice.digit)]))
    .sort((a, b) => a - b);
  const first = digits[0];
  const last = digits.at(-1);
  if (first === undefined || last === undefined) return undefined;
  if (first === last) return `${first}`;
  return digits.every((digit, index) => digit === first + index)
    ? `${first}–${last}`
    : digits.join(" ");
}

/**
 * A control whose press opens a menu of its choices, in the menu button pattern of the WAI-ARIA
 * Authoring Practices: the snooze, the priority and the status. A key that names the control
 * presses its trigger (`b` and `h`, `p`, `s`), so a key, a click and a tap all open the same menu.
 *
 * The menu opens with a choice highlighted and focused - the checked one, else the first - and
 * the arrows, Home and End move the highlight and write nothing. Enter writes the highlighted
 * choice, a click or a tap writes the one it hits, and a digit writes its choice at once, with
 * `preventDefault`, so the page's keymap, which skips a handled key, never reads it as its own.
 * Escape, Tab and a press outside close the menu and write nothing. A pick closes the menu,
 * hands the focus back, then writes, so a write that takes the control off the page - a snooze
 * that folds its row - takes it from where the reader came from.
 *
 * The focus goes back where it was before the menu opened. The trigger's click is where that is
 * read: a click with no pointer (`detail` 0) is a key's - the page's binding pressing the
 * trigger from a row, or Enter or Space on the trigger - and the focus is still where the key
 * was pressed; a pointer's click is on the trigger, which the focus goes back to.
 *
 * A press outside closes the menu in the document's capture phase, before any control under the
 * press can stop it, and hands the focus back there. The browser's own focus on a press is the
 * default action of its `mousedown`, which comes after, so a press on a text box, a button or a
 * link then focuses that control and acts on that one press; a press on nothing that takes the
 * focus leaves it on the document, and the menu hands it back again once the press is over.
 *
 * Each choice draws its digit, and a foot line names the menu's keys. The choices are 44 px high
 * at every width, the height of a tap. The panel is `AnchoredPanel`, so the menu stands over every
 * layer and its keys are its own (`PANEL_ATTRIBUTE`).
 */
export function ChoiceMenu<V extends string>({
  checked,
  children,
  choices,
  disabled = false,
  held = false,
  label,
  marks,
  onChoose,
  triggerClassName,
}: ChoiceMenuProps<V>): ReactNode {
  const id = useId();
  const menuId = `${id}-menu`;
  const faceId = `${id}-face`;
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const choiceRefs = useRef<(HTMLButtonElement | null)[]>([]);
  // The choice the menu opens on, read by the effect that focuses it as the menu opens.
  const opensOn = useRef(0);
  // Where the focus goes back to as the menu closes, recorded as it opens.
  const returnTo = useRef<HTMLElement | null>(null);
  const radio = checked !== undefined;
  const highlighted = Math.min(active, choices.length - 1);

  /** Where the focus goes back to: where it was as the menu opened, or the trigger where that
   *  element has left the page since. */
  const wayBack = (): HTMLElement | null => {
    const recorded = returnTo.current;
    return recorded?.isConnected ? recorded : triggerRef.current;
  };
  const close = () => {
    setOpen(false);
    wayBack()?.focus();
  };
  const choose = (choice: MenuChoice<V>) => {
    close();
    if (choice.value !== checked) onChoose(choice.value);
  };
  const highlight = (index: number) => {
    setActive(index);
    choiceRefs.current[index]?.focus();
  };
  const openFrom = (event: MouseEvent<HTMLButtonElement>) => {
    const focused = document.activeElement;
    returnTo.current =
      event.detail === 0 && focused instanceof HTMLElement && !focusOnDocument()
        ? focused
        : event.currentTarget;
    opensOn.current = Math.max(
      choices.findIndex((choice) => choice.value === checked),
      0
    );
    setActive(opensOn.current);
    setOpen(true);
  };

  useEffect(() => {
    if (open) choiceRefs.current[opensOn.current]?.focus();
  }, [open]);

  // The press outside, in the capture phase: a board card's priority and an ask's hand-on stop
  // their own presses from bubbling, and a press on either must still close this menu.
  useEffect(() => {
    if (!open) return;
    const press = (event: PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Node)) return;
      if (triggerRef.current?.contains(target)) return;
      if (document.getElementById(menuId)?.contains(target)) return;
      setOpen(false);
      const back = wayBack();
      back?.focus();
      // The press's own focus has landed by its `mouseup`, a finger's too: back from the
      // document, and left alone anywhere else. A press the browser cancels (a scroll) moves no
      // focus, and the next press is another press, so either ends the wait.
      const settle = () => {
        stop();
        if (focusOnDocument()) back?.focus();
      };
      const stop = () => {
        document.removeEventListener("mouseup", settle, true);
        document.removeEventListener("pointercancel", stop, true);
        document.removeEventListener("pointerdown", stop, true);
      };
      document.addEventListener("mouseup", settle, true);
      document.addEventListener("pointercancel", stop, true);
      document.addEventListener("pointerdown", stop, true);
    };
    document.addEventListener("pointerdown", press, true);
    return () => document.removeEventListener("pointerdown", press, true);
  });

  const onChoiceKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    if (event.altKey || event.ctrlKey || event.metaKey) return;
    const step = stepTo(event.key, index, choices.length);
    if (step !== undefined) {
      event.preventDefault();
      highlight(step);
      return;
    }
    const choice =
      event.key === "Enter" ? choices[index] : choices.find(({ digit }) => digit === event.key);
    if (choice === undefined) return;
    event.preventDefault();
    choose(choice);
  };

  const digits = digitsText(choices);
  return (
    <>
      <button
        aria-controls={open ? menuId : undefined}
        aria-describedby={radio ? faceId : undefined}
        aria-disabled={held || undefined}
        aria-expanded={open}
        aria-haspopup="menu"
        aria-label={label}
        className={triggerClassName}
        {...marks}
        disabled={disabled}
        onClick={(event) => {
          if (held) return;
          if (open) close();
          else openFrom(event);
        }}
        onMouseDown={keepPress}
        onPointerDown={keepPress}
        onTouchStart={keepPress}
        ref={triggerRef}
        type="button"
      >
        <span className="contents" id={faceId}>
          {children}
        </span>
      </button>
      {open ? (
        <AnchoredPanel
          anchorRef={triggerRef}
          className={`max-h-96 w-56 max-w-[calc(100vw-1rem)] overflow-auto rounded-lg border shadow-lg ${card}`}
          focusOnOpen={false}
          id={menuId}
          label={label}
          onDismiss={() => setOpen(false)}
          returnFocus={wayBack}
          role="menu"
        >
          {/* biome-ignore lint/a11y/noStaticElementInteractions: nothing here is made interactive - the handlers keep a press in the menu out of the card or row the control sits in, and keep the focus on the highlighted choice while the press lands, so the wrapper needs no role of its own. */}
          <div
            className="py-1"
            onMouseDown={(event) => {
              event.preventDefault();
              event.stopPropagation();
            }}
            onPointerDown={keepPress}
            onTouchStart={keepPress}
          >
            {choices.map((choice, index) => (
              <button
                aria-keyshortcuts={choice.digit}
                className={`flex min-h-11 w-full items-center gap-2 px-3 py-2 text-left text-sm outline-none focus-visible:ring-2 focus-visible:ring-inset ${focusVisibleRing} ${textPrimaryOnSurface} ${
                  index === highlighted ? selectedCardBg : surfaceMutedHoverBg
                }`}
                key={choice.value}
                onClick={() => choose(choice)}
                onFocus={() => setActive(index)}
                onKeyDown={(event) => onChoiceKeyDown(event, index)}
                ref={(node) => {
                  choiceRefs.current[index] = node;
                }}
                {...(radio
                  ? { "aria-checked": choice.value === checked, role: "menuitemradio" }
                  : { role: "menuitem" })}
                tabIndex={index === highlighted ? 0 : -1}
                type="button"
              >
                {radio ? (
                  <span aria-hidden="true" className="w-4 shrink-0 text-center">
                    {choice.value === checked ? "✓" : ""}
                  </span>
                ) : null}
                <span className="min-w-0 flex-1 truncate">{choice.label}</span>
                {/* The digit is for the eye; assistive tech hears it as `aria-keyshortcuts`. */}
                {choice.digit === undefined ? null : (
                  <span aria-hidden="true" className="shrink-0">
                    <kbd className={`rounded px-1.5 font-mono text-xs ${kbdHint}`}>
                      {choice.digit}
                    </kbd>
                  </span>
                )}
              </button>
            ))}
            <div
              aria-hidden="true"
              className={`mt-1 border-t px-3 pt-1.5 pb-0.5 text-xs ${borderDefault} ${textMutedOnSurface}`}
            >
              ↑ ↓ Home End to move · Enter{digits === undefined ? "" : ` or ${digits}`} to choose ·
              Esc to close
            </div>
          </div>
        </AnchoredPanel>
      ) : null}
    </>
  );
}
