import {
  type FocusEvent,
  type ReactNode,
  type RefObject,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import { card } from "../theme/classes";
import { AnchoredPanel } from "./AnchoredPanel";

const TOOLTIP_CLASS = `max-w-72 rounded-lg border p-2 text-xs font-normal shadow-lg ${card}`;

/** What a control that carries a tooltip holds: whether the tooltip is open, the id the control
 *  names it by while it is, and the handlers the control spreads onto itself, which open it on
 *  hover and on keyboard focus and close it as the pointer or the focus leaves. */
export interface TooltipTrigger {
  readonly open: boolean;
  readonly id: string;
  readonly show: () => void;
  readonly hide: () => void;
  readonly props: {
    readonly "aria-describedby": string | undefined;
    readonly onBlur: (event: FocusEvent<HTMLElement>) => void;
    readonly onFocus: () => void;
    readonly onMouseEnter: () => void;
    readonly onMouseLeave: () => void;
  };
}

/**
 * The state of one control's tooltip. While `paused` - a column of the table is dragged - it is
 * shut and neither a hover nor a focus opens it, so the drag ends with no tooltip open that the
 * reader did not ask for again. The focus leaves the control only when it goes outside it: a
 * link inside a claim chip keeps the chip's tooltip open.
 */
export function useTooltip({ paused = false }: { paused?: boolean } = {}): TooltipTrigger {
  const [shown, setShown] = useState(false);
  const id = useId();
  const held = useRef(paused);
  held.current = paused;
  const show = useCallback(() => {
    if (!held.current) setShown(true);
  }, []);
  const hide = useCallback(() => setShown(false), []);
  const leave = useCallback((event: FocusEvent<HTMLElement>) => {
    const next = event.relatedTarget;
    if (next instanceof Node && event.currentTarget.contains(next)) return;
    setShown(false);
  }, []);
  useEffect(() => {
    if (paused) setShown(false);
  }, [paused]);
  const open = shown && !paused;
  return {
    open,
    id,
    show,
    hide,
    props: {
      "aria-describedby": open ? id : undefined,
      onBlur: leave,
      onFocus: show,
      onMouseEnter: show,
      onMouseLeave: hide,
    },
  };
}

/**
 * The one tooltip, after the WAI-ARIA tooltip pattern: a short text or table that hangs off its
 * control while the reader hovers it or the keyboard focuses it, placed by `AnchoredPanel`'s
 * rules for a tooltip (`flip`: under the control, 8 px inside each edge of the window, above it
 * where there is no room under it). It takes no focus, so the control keeps it, and the control
 * names it (`aria-describedby`) while it is open. It closes as the pointer or the focus leaves,
 * on a press outside it, and on Escape wherever the focus is: a tooltip a hover opened hangs off
 * a control the focus may not be on. Every sortable header of a `DataTable`, Time per phase's
 * cell, a Queue cell's number and a claim chip draw it.
 */
export function Tooltip({
  anchorRef,
  children,
  trigger,
}: {
  anchorRef: RefObject<HTMLElement | null>;
  children: ReactNode;
  trigger: TooltipTrigger;
}): ReactNode {
  if (!trigger.open) return null;
  return (
    <OpenTooltip anchorRef={anchorRef} trigger={trigger}>
      {children}
    </OpenTooltip>
  );
}

function OpenTooltip({
  anchorRef,
  children,
  trigger,
}: {
  anchorRef: RefObject<HTMLElement | null>;
  children: ReactNode;
  trigger: TooltipTrigger;
}): ReactNode {
  const { hide } = trigger;
  useEffect(() => {
    // Escape from the control itself is the panel's (`AnchoredPanel`), which takes it; any
    // other Escape closes the tooltip and goes on to whoever owns it.
    const onEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") hide();
    };
    document.addEventListener("keydown", onEscape);
    return () => document.removeEventListener("keydown", onEscape);
  }, [hide]);
  return (
    <AnchoredPanel anchorRef={anchorRef} className={TOOLTIP_CLASS} flip onDismiss={hide}>
      <div id={trigger.id} role="tooltip">
        {children}
      </div>
    </AnchoredPanel>
  );
}
