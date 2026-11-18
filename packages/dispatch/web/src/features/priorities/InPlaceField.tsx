import { type ReactNode, useEffect, useRef, useState } from "react";
import {
  borderDefault,
  calloutWarningBg,
  calloutWarningBorder,
  calloutWarningText,
  focusVisibleRing,
  surfaceBg,
  textPrimaryOnCanvas,
} from "../../theme/classes";

/** How long the reader may stop typing before what they wrote is saved. Nothing else in
 *  Dispatch asks a person to press Save, so a priority's fields are saved as they are written:
 *  once the typing rests, at once when the field is left, and as the page goes. The
 *  description's editor keeps the same rule (`PriorityDescription`). */
export const SAVE_AFTER_MS = 500;

/**
 * What a field of a priority shows beside itself while it holds another person's text and the
 * reader's own text is not written: that text, and the reader's two choices. Using it puts it
 * back and writes it at once, over the other person's text, which the write names as the text it
 * goes over; keeping theirs lets the reader's text go. Nothing of either is dropped until the
 * reader picks one. It is an alert, since the reader's text has just left the field they were
 * writing in, and it is drawn in spans and buttons alone, since the title's field stands in the
 * page's heading.
 */
export function ChangedMeanwhile({
  label,
  mine,
  onKeepTheirs,
  onUseMine,
}: {
  /** The field's own name, which names the notice. */
  label: string;
  /** The reader's text, drawn as written: every line of it, its spaces kept. */
  mine: string;
  onKeepTheirs: () => void;
  onUseMine: () => void;
}): ReactNode {
  return (
    <span
      aria-label={`${label}, changed by another person`}
      className={`mt-1 block space-y-1 rounded-lg border p-2 text-sm font-normal ${calloutWarningBorder} ${calloutWarningBg} ${calloutWarningText}`}
      role="alert"
    >
      <span className="block">Another person changed this while you wrote. Your text:</span>
      <span className="block whitespace-pre-wrap border-l-2 pl-2">{mine}</span>
      <span className="flex flex-wrap gap-3">
        <button className="font-semibold underline" onClick={onUseMine} type="button">
          Use my text
        </button>
        <button className="underline" onClick={onKeepTheirs} type="button">
          Keep their text
        </button>
      </span>
    </span>
  );
}

/** Stops the write waiting for the typing to rest, if one waits. */
function stopTimer(timer: { current: number | undefined }): void {
  if (timer.current !== undefined) window.clearTimeout(timer.current);
  timer.current = undefined;
}

/**
 * A one-line field edited where it is read: no edit mode, no Save button. What the reader types
 * is kept here until it rests, then written once; leaving the field writes it immediately, and
 * Enter does both, as do the page going (`pagehide`) and the field leaving the page. Only a text
 * that differs from the one the edit started from is written, so leaving a field the reader typed
 * nothing into writes nothing. The texts compared are trimmed, since every field drawn here is
 * stored trimmed (`internal/dispatch/api/priorities.go`), and what is written is trimmed.
 *
 * A change of `value` is the server's text moving under the field. The round trip of one of its
 * own writes changes nothing the reader sees, so their text never seems undone while it
 * travels. Another person's text is taken in at once - with the focus in the field or not -
 * while the reader holds no unwritten typing and no write of theirs is out. Else it is taken in
 * all the same, the waiting write is stopped, and the reader's text is kept beside the field
 * (`ChangedMeanwhile`) until they use it again or let it go.
 */
export function InPlaceField({
  id,
  label,
  onSave,
  options,
  placeholder,
  value,
}: {
  /** The field's own id, where a visible label points at it rather than wrapping it. */
  id?: string;
  label: string;
  onSave: (next: string) => void;
  /** What other rows already hold, offered beside the field: a reader picks one of the owners
   *  the deployment uses, or writes a new one, since nothing bounds what an owner may be. */
  options?: readonly string[];
  placeholder?: string;
  value: string;
}): ReactNode {
  const [draft, setDraft] = useState(value);
  // The reader's own text, kept beside the field while it shows another person's.
  const [kept, setKept] = useState<string>();
  const timer = useRef<number | undefined>(undefined);
  // The server's text the reader's edit started from: the last one taken in, or the last text
  // this field wrote, which the server holds once that write lands.
  const base = useRef(value);
  // The texts this field wrote that the server has not answered with yet, oldest first.
  const sent = useRef<string[]>([]);
  const seen = useRef(value);
  const latest = useRef({ draft, kept, onSave });
  latest.current = { draft, kept, onSave };

  const write = (text: string) => {
    base.current = text;
    sent.current.push(text);
    latest.current.onSave(text);
  };
  const save = () => {
    stopTimer(timer);
    const typed = latest.current.draft.trim();
    if (typed !== base.current) write(typed);
  };
  const saveNow = useRef(save);
  saveNow.current = save;

  useEffect(() => {
    if (value === seen.current) return;
    seen.current = value;
    const { draft: current, kept: mine } = latest.current;
    const typed = current.trim();
    // The server holds what the field holds: nothing is left to write.
    if (value === typed) {
      base.current = value;
      sent.current = [];
      if (mine?.trim() === value) setKept(undefined);
      return;
    }
    // The answer to one of this field's own writes, and to each one sent before it.
    const own = sent.current.indexOf(value);
    if (own !== -1 || value === base.current) {
      sent.current.splice(0, own + 1);
      return;
    }
    // Another person's text. The reader's own, where they hold one, goes beside the field.
    const holding = typed !== base.current || sent.current.length > 0;
    const beside = holding ? current : mine;
    stopTimer(timer);
    setKept(beside?.trim() === value ? undefined : beside);
    base.current = value;
    sent.current = [];
    setDraft(value);
  }, [value]);

  // Leaving the page, or the field leaving it, leaves the field: what the reader typed is
  // written now rather than lost with the timer. A write pressed as the page goes is kept for
  // the reload to make (`features/writes/write-storage.ts`).
  useEffect(() => {
    const leave = () => saveNow.current();
    window.addEventListener("pagehide", leave);
    return () => {
      window.removeEventListener("pagehide", leave);
      leave();
    };
  }, []);

  const takeMine = () => {
    if (kept === undefined) return;
    stopTimer(timer);
    setKept(undefined);
    setDraft(kept);
    const text = kept.trim();
    if (text !== base.current) write(text);
  };
  // A plain bordered field: it is the text's one reading, written where it is read, so nothing
  // hides it until the reader hovers it.
  const listId = options === undefined ? undefined : `${label.replace(/\W+/g, "-")}-options`;
  return (
    <>
      <input
        aria-label={label}
        className={`w-full rounded-md border px-2 py-1 text-sm ${borderDefault} ${surfaceBg} ${textPrimaryOnCanvas} ${focusVisibleRing}`}
        id={id}
        list={listId}
        onBlur={() => {
          save();
          setDraft((typed) => typed.trim());
        }}
        onChange={(event) => {
          setDraft(event.target.value);
          stopTimer(timer);
          timer.current = window.setTimeout(save, SAVE_AFTER_MS);
        }}
        onKeyDown={(event) => {
          if (event.key !== "Enter") return;
          event.preventDefault();
          save();
          event.currentTarget.blur();
        }}
        placeholder={placeholder}
        type="text"
        value={draft}
      />
      {listId === undefined ? null : (
        <datalist id={listId}>
          {(options ?? []).map((option) => (
            <option key={option} value={option} />
          ))}
        </datalist>
      )}
      {kept === undefined ? null : (
        <ChangedMeanwhile
          label={label}
          mine={kept}
          onKeepTheirs={() => setKept(undefined)}
          onUseMine={takeMine}
        />
      )}
    </>
  );
}
