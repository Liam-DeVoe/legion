import { useEffect, useRef, useState } from "react";

/** How long the reader may stop typing before what they wrote is saved. Nothing else in
 *  Dispatch asks a person to press Save, so a priority's title and description are saved as
 *  they are written: once the typing rests, and at once when the field is left. */
export const SAVE_AFTER_MS = 500;

/** A field edited where it is read, as its host wires it to whatever element it draws. */
export interface InPlaceDraft {
  /** What the field shows: the reader's own text, or the server's when it changed under a
   *  reader who is not holding the field. */
  readonly draft: string;
  /** The reader typed `next`: it is kept, and saved once the typing rests. */
  readonly type: (next: string) => void;
  /** The reader is holding the field, so the server's text no longer replaces the draft. */
  readonly hold: () => void;
  /** The reader left the field: the draft is written now and `onDone` is told. */
  readonly leave: () => void;
}

/**
 * The draft of a field edited where it is read: no edit mode, no Save button. What the reader
 * types is kept here until it rests, then written once; leaving the field writes it at once,
 * unless the rest already wrote that text, so a rest and then a leave send one write however the
 * reader leaves. The reader's own text stands until `value` itself changes, so a refresh of the
 * row underneath never pulls a half-typed word out from under them and the round trip of their
 * own write never shows them the old text again. `InPlaceField` (one line) and
 * `PriorityDescription` (the shared text field) each draw their own element over it.
 */
export function useInPlaceDraft(
  value: string,
  onSave: (next: string) => void,
  onDone?: () => void
): InPlaceDraft {
  const [draft, setDraft] = useState(value);
  const [editing, setEditing] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  const latest = useRef({ draft, value, onSave });
  latest.current = { draft, value, onSave };
  const seen = useRef(value);
  // The text this field last wrote, until `value` moves. A write is a round trip: `value` is
  // the old text until the answer lands and the row is read again, so a leave inside that trip
  // would compare the draft against the old text and write the same text a second time. Once
  // `value` moves the text is the server's again, and a draft that differs from it is written
  // even when it matches what was written before: another writer may have changed it since.
  const written = useRef<string | undefined>(undefined);

  // The server's text wins when it changes under a reader who is not holding the field:
  // another writer's rename, or this writer's own write coming back. The draft stands until
  // then, because leaving the field sends a write and the write is a round trip: resetting
  // the draft while it is in flight would show the reader their own text being undone, and
  // then written again when the answer lands.
  useEffect(() => {
    if (value === seen.current) return;
    seen.current = value;
    written.current = undefined;
    if (!editing) setDraft(value);
  }, [editing, value]);

  const stop = () => {
    if (timer.current !== undefined) window.clearTimeout(timer.current);
    timer.current = undefined;
  };
  const save = () => {
    stop();
    const { draft: typed, value: saved, onSave: write } = latest.current;
    if (typed === saved || typed === written.current) return;
    written.current = typed;
    write(typed);
  };
  // The timer is cleared through the ref the render that set it wrote to, so the unmount takes
  // no dependency on this render's own `stop`.
  useEffect(
    () => () => {
      if (timer.current !== undefined) window.clearTimeout(timer.current);
    },
    []
  );

  return {
    draft,
    hold: () => setEditing(true),
    leave: () => {
      setEditing(false);
      save();
      onDone?.();
    },
    type: (next: string) => {
      setDraft(next);
      stop();
      timer.current = window.setTimeout(save, SAVE_AFTER_MS);
    },
  };
}
