import type { ReactNode } from "react";

import { kbdHint, railHoverBg, railHoverText, railSecondaryText } from "../../theme/classes";
import { ALT_KEY_LABEL, MOD_KEY_LABEL, stepsOf } from "./keymap";
import type { KeymapActionId } from "./keymap-catalogue";
import { useActionKeys } from "./keymap-keys";
import { keyshortcutsOf } from "./ShortcutHelp";

const ON_MAC = MOD_KEY_LABEL === "⌘";

/** How the rail writes each modifier: the Mac's glyphs there, the words elsewhere. */
const MODIFIER_TEXT: Record<string, string> = {
  $mod: MOD_KEY_LABEL,
  Alt: ALT_KEY_LABEL,
  Control: ON_MAC ? "⌃" : "Ctrl",
  Ctrl: ON_MAC ? "⌃" : "Ctrl",
  Meta: ON_MAC ? "⌘" : "Meta",
  Shift: ON_MAC ? "⇧" : "Shift",
};

/** One key as the rail writes it: `⌘K` on a Mac and `Ctrl K` elsewhere for `$mod+k`, a key with
 *  no modifier as it is typed (`c`), and each press of a chord in turn (`g i`). */
function railText(key: string): string {
  return stepsOf(key)
    .map((step) => {
      const parts = step.split("+");
      const last = parts.pop() ?? "";
      if (parts.length === 0) return last;
      const modifiers = parts.map((part) => MODIFIER_TEXT[part] ?? part);
      return [...modifiers, last.length === 1 ? last.toUpperCase() : last].join(ON_MAC ? "" : " ");
    })
    .join(" ");
}

/** A rail control that stands for a keyboard action: its label, then the keys that also run it,
 *  which are the person's own (`useActionKeys`), so a person who has not learned the key can
 *  still reach what it opens. `className` carries the control's place in the rail (its top
 *  margin), which differs per call site. */
export function RailButton({
  action,
  className,
  label,
  onOpen,
}: {
  action: KeymapActionId;
  className: string;
  label: string;
  onOpen: () => void;
}): ReactNode {
  const keys = useActionKeys(action);
  return (
    <button
      aria-keyshortcuts={keyshortcutsOf(keys)}
      className={`${className} flex min-h-11 w-full items-center justify-between rounded-lg px-3 text-left text-sm font-medium ${railSecondaryText} ${railHoverBg} ${railHoverText}`}
      onClick={onOpen}
      type="button"
    >
      <span>{label}</span>
      {keys.length === 0 ? null : (
        <span className="flex items-center gap-1">
          {keys.map((key) => (
            <kbd className={`rounded px-1.5 py-0.5 text-xs font-medium ${kbdHint}`} key={key}>
              {railText(key)}
            </kbd>
          ))}
        </span>
      )}
    </button>
  );
}
