import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  borderDefault,
  calloutWarningBg,
  calloutWarningBorder,
  calloutWarningText,
  focusVisibleRing,
  surfaceBg,
  textPrimaryOnCanvas,
} from "../../theme/classes";
import { arrival, NOTHING_KEPT } from "./field-arrival";

/** How long the reader may stop typing before what they wrote is saved. Nothing else in
 *  Dispatch asks a person to press Save, so a priority's fields are saved as they are written:
 *  once the typing rests, at once when the field is left, and as the page goes. The
 *  description's editor keeps the same rule (`PriorityDescription`). */
export const SAVE_AFTER_MS = 500;

/**
 * What a field of a priority shows beside itself while it holds another person's text and the
 * reader's own is not written: the reader's texts, newest first, since a second change can arrive
 * before they choose, and their choices. Using one puts it back and writes it at once, over the
 * other person's text, and lets the others go; keeping theirs lets every one go. A title's or a
 * description's write names the text it goes over (`usePriorityWrites`), so it is refused if a
 * third text arrived meanwhile; the owner's write names none. Nothing is dropped until the reader
 * picks. It is an alert, since the reader's text has just left the field they were writing in.
 * A one-line field names it as what describes it (`aria-describedby`, by `id`), so the two stay
 * tied wherever it is drawn (`InPlaceField`'s `noticeAt`). It is drawn in spans and buttons
 * alone, since the owner's field on the priority's page, which draws it beside itself, stands in
 * a span.
 */
export function ChangedMeanwhile({
  id,
  label,
  mine,
  onKeepTheirs,
  onUse,
}: {
  /** The notice's own id, which the one-line field it stands for names in `aria-describedby`. */
  id?: string;
  /** The field's own name, which names the notice. */
  label: string;
  /** The reader's texts, newest first, each drawn as written: every line of it, its spaces kept. */
  mine: readonly string[];
  onKeepTheirs: () => void;
  /** Puts `text`, one of `mine`, back and writes it. */
  onUse: (text: string) => void;
}): ReactNode {
  return (
    <span
      aria-label={`${label}, changed by another person`}
      className={`mt-1 block space-y-1 rounded-lg border p-2 text-sm font-normal ${calloutWarningBorder} ${calloutWarningBg} ${calloutWarningText}`}
      id={id}
      role="alert"
    >
      {mine.length === 1 ? (
        <>
          <span className="block">Another person changed this while you wrote. Your text:</span>
          <span className="block whitespace-pre-wrap border-l-2 pl-2">{mine[0]}</span>
          <span className="flex flex-wrap gap-3">
            <button
              className="font-semibold underline"
              onClick={() => onUse(mine[0])}
              type="button"
            >
              Use my text
            </button>
            <button className="underline" onClick={onKeepTheirs} type="button">
              Keep their text
            </button>
          </span>
        </>
      ) : (
        <>
          <span className="block">
            Another person changed this while you wrote. Your texts, newest first:
          </span>
          {mine.map((text) => (
            <span className="block space-y-1" key={text}>
              <span className="block whitespace-pre-wrap border-l-2 pl-2">{text}</span>
              <button className="font-semibold underline" onClick={() => onUse(text)} type="button">
                Use this text
              </button>
            </span>
          ))}
          <button className="block underline" onClick={onKeepTheirs} type="button">
            Keep their text
          </button>
        </>
      )}
    </span>
  );
}

/** Stops the write waiting for the typing to rest, if one waits. */
function stopTimer(timer: { current: number | undefined }): void {
  window.clearTimeout(timer.current);
  timer.current = undefined;
}

/** What a field holds, as it is compared and written: trimmed, and in a `required` field the
 *  reader emptied, the text the edit started from, since an empty one is never written. */
function heldText(draft: string, base: string, required: boolean): string {
  const typed = draft.trim();
  return required && typed === "" ? base : typed;
}

/**
 * A one-line field edited where it is read: no edit mode, no Save button. What the reader types
 * is kept here until it rests, then written once; leaving the field writes it immediately, and
 * Enter does both, as do the page going (`pagehide`) and the field leaving the page. Only a text
 * that differs from the one the edit started from is written, so leaving a field the reader typed
 * nothing into writes nothing. The texts compared are trimmed, since every field drawn here is
 * stored trimmed (`internal/dispatch/api/priorities.go`), and what is written is trimmed. A
 * `required` field never writes an empty text: left empty, it shows the text it started from
 * again.
 *
 * A change of `value` is the server's text moving under the field, which it reads as the
 * description's editor does (`arrival`). The round trip of one of its own writes changes nothing
 * the reader sees, so their text never seems undone while it travels. Another person's text is
 * taken in at once - with the focus in the field or not - while the reader holds no unwritten
 * typing and no write of theirs is out. Else it is taken in all the same, the waiting write is
 * stopped, and the reader's text is kept beside the field (`ChangedMeanwhile`) until they use it
 * again or let it go; where the page gives that notice a place of its own (`noticeAt`), it is
 * drawn there, and the field names it as what describes it either way. Where the page holds
 * those texts (`kept`, `onKeptChange`) they outlive the field, and a field drawn again shows
 * them again; else they go with it.
 */
export function InPlaceField({
  id,
  kept: keptByPage,
  label,
  maxLength,
  noticeAt,
  onKeptChange,
  onSave,
  options,
  placeholder,
  required = false,
  value,
}: {
  /** The field's own id, where a visible label points at it rather than wrapping it. */
  id?: string;
  /** The reader's texts kept beside the field, where the page holds them rather than the field:
   *  `onKeptChange` hears each change of them. */
  kept?: readonly string[];
  label: string;
  /** The most characters the field holds, where Dispatch bounds the text, as it bounds a title
   *  (`ISSUE_TITLE_MAX`): a longer text is never typed into the field, so never sent. */
  maxLength?: number;
  /** Where the notice of the reader's kept texts is drawn, where the page gives it a place of its
   *  own: a field standing in a heading would else draw it inside the heading, whose name would
   *  then carry the notice's words. `null` is a place not drawn yet, which holds no notice until
   *  it is; absent, the notice is drawn beside the field. */
  noticeAt?: HTMLElement | null;
  onKeptChange?: (kept: readonly string[]) => void;
  onSave: (next: string) => void;
  /** What other rows already hold, offered beside the field: a reader picks one of the owners
   *  the deployment uses, or writes a new one, since nothing bounds what an owner may be. */
  options?: readonly string[];
  placeholder?: string;
  /** Whether the text may not be empty, as a title may not: an empty one is never written. */
  required?: boolean;
  value: string;
}): ReactNode {
  const [draft, setDraft] = useState(value);
  // The reader's own texts, kept beside the field while it shows another person's, where the
  // page does not hold them.
  const [ownKept, setOwnKept] = useState(NOTHING_KEPT);
  const kept = keptByPage ?? ownKept;
  const setKept = onKeptChange ?? setOwnKept;
  const timer = useRef<number | undefined>(undefined);
  const noticeId = useId();
  // Where the field stands against the server's text (`FieldStand`): what the edit started from,
  // and the texts written that the server has not answered with yet, oldest first.
  const base = useRef(value);
  const sent = useRef<readonly string[]>([]);
  const seen = useRef(value);
  const latest = useRef({ draft, kept, onSave, required, setKept });
  latest.current = { draft, kept, onSave, required, setKept };

  const write = (text: string) => {
    base.current = text;
    sent.current = [...sent.current, text];
    latest.current.onSave(text);
  };
  const save = () => {
    stopTimer(timer);
    const text = heldText(latest.current.draft, base.current, latest.current.required);
    if (text !== base.current) write(text);
  };
  const saveNow = useRef(save);
  saveNow.current = save;

  useEffect(() => {
    if (value === seen.current) return;
    seen.current = value;
    const { draft: current, kept: mine, required: needed, setKept: keep } = latest.current;
    const next = arrival(value, heldText(current, base.current, needed), {
      base: base.current,
      kept: mine,
      sent: sent.current,
    });
    base.current = next.base;
    sent.current = next.sent;
    if (next.kept !== mine) keep(next.kept);
    if (!next.take) return;
    stopTimer(timer);
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

  // One of the reader's kept texts, put back in the field and written at once.
  const putBack = (text: string) => {
    stopTimer(timer);
    setKept(NOTHING_KEPT);
    setDraft(text);
    if (text !== base.current) write(text);
  };
  // The notice of the reader's kept texts: in the place the page gives it once that place is
  // drawn, else beside the field, and named by the field as what describes it wherever it is.
  const notice =
    kept.length === 0 || noticeAt === null ? null : (
      <ChangedMeanwhile
        id={noticeId}
        label={label}
        mine={kept}
        onKeepTheirs={() => setKept(NOTHING_KEPT)}
        onUse={putBack}
      />
    );
  // A plain bordered field: it is the text's one reading, written where it is read, so nothing
  // hides it until the reader hovers it.
  const listId = options === undefined ? undefined : `${label.replace(/\W+/g, "-")}-options`;
  return (
    <>
      <input
        aria-describedby={notice === null ? undefined : noticeId}
        aria-label={label}
        className={`w-full rounded-md border px-2 py-1 text-sm ${borderDefault} ${surfaceBg} ${textPrimaryOnCanvas} ${focusVisibleRing}`}
        id={id}
        list={listId}
        maxLength={maxLength}
        onBlur={() => {
          save();
          setDraft((typed) => heldText(typed, base.current, required));
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
      {noticeAt ? createPortal(notice, noticeAt) : notice}
    </>
  );
}
