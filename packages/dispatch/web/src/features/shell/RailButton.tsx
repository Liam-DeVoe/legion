import type { ReactNode } from "react";

import { railHoverBg, railHoverText, railKbd, railSecondaryText } from "../../theme/classes";

/** A rail control that stands for a keyboard binding: its label, then the keys that also run it,
 *  so a person who has not learned the binding can still reach what it opens. `className` carries
 *  the control's place in the rail (its top margin), which differs per call site. */
export function RailButton({
  ariaKeyshortcuts,
  className,
  label,
  onOpen,
  shortcut,
}: {
  ariaKeyshortcuts: string;
  className: string;
  label: string;
  onOpen: () => void;
  shortcut: string;
}): ReactNode {
  return (
    <button
      aria-keyshortcuts={ariaKeyshortcuts}
      className={`${className} flex h-9 w-full items-center justify-between gap-2 rounded-md px-2 text-left text-sm ${railSecondaryText} ${railHoverBg} ${railHoverText}`}
      onClick={onOpen}
      type="button"
    >
      <span>{label}</span>
      <kbd
        className={`inline-flex h-5 min-w-5 items-center justify-center rounded border px-1 font-sans text-xs ${railKbd}`}
      >
        {shortcut}
      </kbd>
    </button>
  );
}
