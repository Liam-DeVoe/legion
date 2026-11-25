import type { ReactNode } from "react";

import { Button } from "../../components/Button";
import { backdrop40, card, textPrimaryOnSurface } from "../../theme/classes";
import { useDialog } from "../shell/useDialog";

export interface UnsubscribeDialogProps {
  /** The dialog's accessible name; the action it confirms. */
  label?: string;
  message: string;
  onCancel: () => void;
  onConfirm: () => void;
}

/**
 * Confirms removing a subscriber from an issue's or project document's "Subscribed
 * agents" list (IssueHeader, DocumentPage) or a follower or subscriber from an ask card's
 * "Reaches" list (AskRecipients). Escape, a backdrop click, and Cancel all dismiss without
 * action; Confirm is the only path that removes.
 */
export function UnsubscribeDialog({
  label = "Unsubscribe",
  message,
  onCancel,
  onConfirm,
}: UnsubscribeDialogProps): ReactNode {
  const dialog = useDialog<HTMLDivElement>({ onClose: onCancel, open: true });

  return (
    <>
      <div aria-hidden="true" className={`fixed inset-0 z-40 ${backdrop40}`} onClick={onCancel} />
      <div className="pointer-events-none fixed inset-0 z-40 flex items-center justify-center p-4">
        <div
          aria-label={label}
          aria-modal="true"
          className={`pointer-events-auto w-full max-w-sm space-y-4 rounded-lg border p-4 shadow-xl ${card}`}
          ref={dialog.containerRef}
          role="dialog"
        >
          <p className={`text-sm ${textPrimaryOnSurface}`}>{message}</p>
          <div className="flex justify-end gap-2">
            <Button onClick={onCancel}>Cancel</Button>
            <Button onClick={onConfirm} variant="danger">
              Confirm
            </Button>
          </div>
        </div>
      </div>
    </>
  );
}
