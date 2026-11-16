import { type ReactNode, type RefObject, useCallback, useEffect, useId, useState } from "react";
import { card } from "../theme/classes";
import { AnchoredPanel } from "./AnchoredPanel";

const TOOLTIP_CLASS = `z-50 max-w-72 rounded-lg border p-2 text-xs font-normal shadow-lg ${card}`;

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
    readonly onBlur: () => void;
    readonly onFocus: () => void;
    readonly onMouseEnter: () => void;
    readonly onMouseLeave: () => void;
  };
}

export function useTooltip(): TooltipTrigger {
  const [open, setOpen] = useState(false);
  const id = useId();
  const show = useCallback(() => setOpen(true), []);
  const hide = useCallback(() => setOpen(false), []);
  return {
    open,
    id,
    show,
    hide,
    props: {
      "aria-describedby": open ? id : undefined,
      onBlur: hide,
      onFocus: show,
      onMouseEnter: show,
      onMouseLeave: hide,
    },
  };
}

/**
 * The one tooltip: a short text or table that hangs off its control while the reader hovers it
 * or the keyboard focuses it, placed and dismissed by `AnchoredPanel`'s rules. It takes no
 * focus, so the control keeps it, and the control names it (`aria-describedby`) while it is
 * open. It closes as the pointer or the focus leaves, and on Escape wherever the focus is: a
 * tooltip a hover opened hangs off a control the focus may not be on. Time per phase's cell,
 * every sortable column header and a Board card's measure facts and open asks draw it.
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
    // Escape from the control itself is the panel's (`AnchoredPanel`), which keeps it; any
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
