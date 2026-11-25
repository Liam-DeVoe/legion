import type { ReactNode } from "react";

import { buttonClassName } from "../../components/Button";

/** The Reply action on a message turn: an icon-only button whose 44 px tap target shrinks to
 *  32 px on desktop like the copy-reference and pin controls beside it. `disabled` holds it while
 *  the composer it would address is busy with a send. */
export function ReplyButton({
  className = "",
  disabled = false,
  onClick,
}: {
  className?: string;
  disabled?: boolean;
  onClick: () => void;
}): ReactNode {
  return (
    <button
      aria-label="Reply"
      className={`${buttonClassName({ iconOnly: true, size: "sm", variant: "ghost-muted" })} ${className}`}
      disabled={disabled}
      onClick={onClick}
      title="Reply"
      type="button"
    >
      <svg aria-hidden="true" className="size-4 shrink-0" fill="none" viewBox="0 0 20 20">
        <path
          d="M8 5 3.5 9.5 8 14M4 9.5h7.5a4.5 4.5 0 0 1 4.5 4.5V16"
          stroke="currentColor"
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth="1.5"
        />
      </svg>
    </button>
  );
}
