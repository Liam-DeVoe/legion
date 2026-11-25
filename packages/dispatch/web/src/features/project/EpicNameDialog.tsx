import { ISSUE_TITLE_MAX } from "@legion/contracts";
import { type FormEvent, type ReactNode, useId, useRef, useState } from "react";
import { Button } from "../../components/Button";
import { fieldLabelClassName, Input } from "../../components/Field";

import { backdrop50, borderDefault, card, textPrimaryOnSurface } from "../../theme/classes";
import { useDialog } from "../shell/useDialog";

/**
 * Naming an epic, in the page: the same small modal create-issue uses, so naming and renaming
 * an epic trap focus, close on Escape and return focus to the row the reader came from. A
 * browser may suppress `window.prompt` outright, and the prompt took the page with it.
 */
export function EpicNameDialog({
  title,
  initial = "",
  submitLabel,
  onCancel,
  onSubmit,
}: {
  /** The dialog's own heading and accessible name. */
  title: string;
  initial?: string;
  submitLabel: string;
  onCancel: () => void;
  onSubmit: (name: string) => void;
}): ReactNode {
  const nameRef = useRef<HTMLInputElement>(null);
  const dialog = useDialog<HTMLFormElement>({
    initialFocusRef: nameRef,
    onClose: onCancel,
    open: true,
  });
  const [name, setName] = useState(initial);
  const nameId = useId();
  const trimmed = name.trim();
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (trimmed !== "") onSubmit(trimmed);
  };
  return (
    <>
      <div aria-hidden="true" className={`fixed inset-0 z-40 ${backdrop50}`} onClick={onCancel} />
      <div className="pointer-events-none fixed inset-0 z-50 flex items-start justify-center xl:p-4 xl:pt-20">
        <form
          aria-label={title}
          aria-modal="true"
          className={`pointer-events-auto max-h-[100dvh] w-full space-y-3 overflow-y-auto border p-4 shadow-2xl xl:max-h-[calc(100dvh-6rem)] xl:max-w-lg xl:rounded-lg ${card} ${borderDefault}`}
          onSubmit={submit}
          ref={dialog.containerRef}
          role="dialog"
        >
          <h2 className={`text-[15px] font-semibold ${textPrimaryOnSurface}`}>{title}</h2>
          <label className={fieldLabelClassName} htmlFor={nameId}>
            Name
            <Input
              className="mt-1.5 font-normal"
              id={nameId}
              surface="recessed"
              maxLength={ISSUE_TITLE_MAX}
              onChange={(event) => setName(event.target.value)}
              ref={nameRef}
              type="text"
              value={name}
            />
          </label>
          <div className="flex items-center justify-end gap-2">
            <Button onClick={onCancel}>Cancel</Button>
            <Button disabled={trimmed === ""} type="submit" variant="primary">
              {submitLabel}
            </Button>
          </div>
        </form>
      </div>
    </>
  );
}
