/** The reader's own texts a field keeps beside itself while it shows another person's: none. */
export const NOTHING_KEPT: readonly string[] = [];

/** A text a field wrote that no read of the server's has shown it yet, and whether the page said
 *  its write landed (`answered`): one that did is the server's text, and no write of the reader's
 *  still out. */
export interface SentText {
  readonly text: string;
  readonly landed: boolean;
}

/** Where a field of a priority stands against the server's text. */
export interface FieldStand {
  /** The text the field compares the reader's with: the last text it wrote, or the last text of
   *  the server's it took in, whichever came last. Only a text that differs from it is written. */
  readonly base: string;
  /** The texts the field wrote that the server has not answered with yet, oldest first: the
   *  server's answer with one of them is the reader's own write coming back. A text whose write
   *  ended without landing leaves it (`answered`): no answer brings it back. */
  readonly sent: readonly SentText[];
  /** The reader's own texts kept beside the field while it shows another person's
   *  (`ChangedMeanwhile`), newest first, none twice. */
  readonly kept: readonly string[];
}

/** Where a field stands once a new text of the server's arrived, and whether it takes that text
 *  in: shows it in place of what it holds, and stops the write waiting for the typing to rest,
 *  which would write over it. */
export interface Arrival extends FieldStand {
  readonly take: boolean;
}

/** `kept` without `text`, and `kept` itself where it holds no such text. */
function without(kept: readonly string[], text: string): readonly string[] {
  return kept.includes(text) ? kept.filter((one) => one !== text) : kept;
}

/**
 * What a field of a priority makes of `server`, a new text of the server's, while the field holds
 * `held`: the reader's text as the field compares it, trimmed in a one-line field
 * (`InPlaceField`) and the editor's own markdown in the description's (`PriorityDescription`).
 * Both fields decide it here, so they never read one arrival two ways. Whatever they make of it,
 * each takes `server` as the server's text its writes go over from then on (its `from`): a read
 * is the one thing that moves that, and a write never does.
 *
 * - The answer to one of the field's own writes is the reader's text coming back: it answers
 *   that write and each one sent before it, and no later one. The field then compares the
 *   reader's text with the last write still out, so a write waiting for the typing to rest
 *   writes the reader's text again where it differs from that. With none out, it compares with
 *   the answer, unless the reader still holds the text it compared with: one that differs from
 *   the answer is the text of a write that ended without landing (refused, stopped or
 *   discarded), which no save writes again on its own, since the reader discarded it, or the
 *   shell's line holds it, with its Retry. Nothing is taken in, so the reader never sees their
 *   text undone while it travels.
 * - The text the field holds is no change: nothing is left to write. Nor is the text the field
 *   compares with, with no write of the field's out over it: the reader's typing over it is still
 *   theirs to write.
 * - Any other text is another person's, and is taken in. Where the reader holds typing not yet
 *   written, or a write of theirs is still out - one the page has not said landed - their text is
 *   kept beside the field, ahead of any kept there already. One that landed was the server's text
 *   until theirs replaced it, as one a read had shown would have been.
 *
 * A kept text the server now holds is no longer the reader's to put back, and goes.
 */
export function arrival(server: string, held: string, stand: FieldStand): Arrival {
  const kept = without(stand.kept, server);
  const own = stand.sent.findIndex((one) => one.text === server);
  if (own !== -1) {
    const sent = stand.sent.slice(own + 1);
    const base = sent.at(-1)?.text ?? (held === stand.base ? stand.base : server);
    return { base, kept, sent, take: false };
  }
  if (server === held) return { base: server, kept, sent: [], take: false };
  if (server === stand.base) return { ...stand, kept, take: false };
  const holding = held !== stand.base || stand.sent.some((one) => !one.landed);
  return {
    base: server,
    kept: holding ? [held, ...without(kept, held)] : kept,
    sent: [],
    take: true,
  };
}

/** `sent` once the page answered the write that pushed `entry` - that very entry, never another of
 *  the same text: landed, the text stays until a read shows it, and is no write still out; any
 *  other way, it goes, since no answer of the server's brings it back. An entry `sent` no longer
 *  holds - a read answered it, or another person's text ended it - changes nothing. */
export function answered(
  sent: readonly SentText[],
  entry: SentText,
  landed: boolean
): readonly SentText[] {
  const at = sent.indexOf(entry);
  if (at === -1) return sent;
  if (!landed) return [...sent.slice(0, at), ...sent.slice(at + 1)];
  return sent.map((one, place) => (place === at ? { ...one, landed } : one));
}
