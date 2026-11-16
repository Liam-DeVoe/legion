import { type ReactNode, type RefObject, useId, useState } from "react";
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
  const show = () => setOpen(true);
  const hide = () => setOpen(false);
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
 * open. Time per phase's cell and the measure columns' headers draw it.
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
    <AnchoredPanel anchorRef={anchorRef} className={TOOLTIP_CLASS} flip onDismiss={trigger.hide}>
      <div id={trigger.id} role="tooltip">
        {children}
      </div>
    </AnchoredPanel>
  );
}
