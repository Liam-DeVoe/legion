import type { ReactNode } from "react";
import { heldPress, useHeldThroughAnswer } from "../../components/held-press";
import { QueryError } from "../../components/QueryError";
import { badgeLow, secondaryButtonCompact, textMutedOnCanvas } from "../../theme/classes";
import { badgeSelectBadge, badgeSelectOverlay, badgeSelectWrapper } from "../issue/badge-select";
import { Timestamp } from "../refs/Timestamp";
import { useLiveRegistration } from "../writes/useKeyedWrite";
import { isIndefinite, SNOOZE_PRESETS } from "./snooze";
import { useAskSnooze } from "./useAskSnooze";

/**
 * The per-row defer control. On an ordinary row it is the "Snooze" badge with the same
 * invisible native `<select>` over it that the priority editor uses, so the menu is one tap on
 * a phone and one keyboard interaction on a desktop; the picks are `snooze.ts`'s presets and
 * the moment is computed when the reader picks, not when the page rendered. On a row already
 * inside its window it is the return time and the one button that ends the snooze early -
 * nothing else does, which is the point: an agent answering a deferred ask leaves it deferred.
 *
 * A snooze moves its row into Later, which starts folded, and an un-snooze moves it out, either
 * of which would unmount this control and take its `Snoozing…` and its refusal with it - so,
 * exactly as `AssignToMe` does for the optimistic assignment that can drop a row out of the
 * assignee filter, it tells the Inbox while its write is live, and the Inbox keeps the row in the
 * band the reader saw it in until the write settles (`heldRow`). The move is the feedback for a
 * snooze that lands, made in the commit in which the Inbox lets go of the row; a snooze the server
 * refuses, or one the reader stops, rolls the row back where it was with the reason still
 * attached to it. The Snooze select and Un-snooze are held from the press until the commit after
 * their write's answer (`useHeldThroughAnswer`), the one in which the Inbox lets go of the row:
 * the select by `aria-disabled`, with a pick then writing nothing, and Un-snooze by `heldPress`.
 * The optimistic moment puts the row inside its window at once, where Un-snooze would be drawn in
 * the select's place; the select stays drawn instead, held, with the return time being saved
 * before it, while its pick's write is out and through the commit after its answer. An
 * un-snooze takes the row out of its window at once the same way, where the select would be drawn
 * in Un-snooze's place, so Un-snooze stays drawn, held, while its write is out and through the
 * commit after its answer. The press is the reader's, and the row's going after the answer is
 * that press's doing (`causedByReader`) only while the control it landed on is still in the page
 * and held: the row and its held control go together in that one commit, so the page's rescue
 * hands the focus on as it does for Dismiss and Assign to me, to the same control in the row that
 * moves up into the row's place. A control taken away at the press would leave the row's going
 * to drop the focus onto the page.
 *
 * The badge's `Snoozing…` is drawn for the eye alone (`aria-hidden`) and the select keeps its
 * name, so the write is said to a screen reader by a polite status of its own while it is out,
 * and that status says nothing once the write is decided.
 *
 * `label` names the row for assistive tech; the Inbox passes the issue key or the document.
 */
export function SnoozeControl({
  askId,
  label,
  onLive,
  snoozedUntil,
}: {
  askId: string;
  label: string;
  /** Registers the row of the ask its write is of while that write is in flight or failed;
   *  unregistered once it settles cleanly or the control unmounts. */
  onLive: (askId: string, live: boolean) => void;
  /** The row's own snooze while its moment is still ahead; null on an ordinary row. */
  snoozedUntil: string | null;
}): ReactNode {
  const write = useAskSnooze(askId);
  // The row held is the one the live snooze is of, which is this row's unless a host has drawn
  // the control for another ask since that press (`useAskSnooze`).
  useLiveRegistration(write.liveAskId, onLive);
  const held = useHeldThroughAnswer(write.pending);
  // A pick's write: a moment, where an un-snooze's is null.
  const pickHeld = useHeldThroughAnswer(typeof write.pendingUntil === "string");
  const unsnoozeHeld = useHeldThroughAnswer(write.pendingUntil === null);
  // `basis-full` inside the header's wrapping row: a refusal is wider than the badge it
  // replaces, so it takes a line of its own rather than drawing over the issue key.
  const failure = write.failed ? (
    <div className="basis-full">
      <QueryError
        message={write.error ?? `Could not snooze ${label}.`}
        onRetry={write.retry}
        retrying={write.pending}
      />
    </div>
  ) : null;
  // The row stays in the band the reader saw it in while the write is out, and a snooze in flight
  // already shows the return time it is saving. What the row cannot say is which way the reader
  // is going, so the in-flight label comes from the write's own variable (a moment is a snooze,
  // null is an un-snooze) and never from `snoozedUntil`, which by then describes the destination.
  // It is read only while a write is live: `variables` outlives the write, so a settled row would
  // otherwise keep reporting the last thing it did.
  const going = !write.pending
    ? undefined
    : write.pendingUntil === null
      ? "Un-snoozing"
      : "Snoozing";
  const pendingLabel = going === undefined ? undefined : `${going}…`;
  // Drawn first in both branches below and empty while no write is out, so it is the one status
  // in the page across a press: a screen reader reads a status already there when its words
  // change.
  const status = (
    <span className="sr-only" role="status">
      {going === undefined ? null : `${going} ${label}…`}
    </span>
  );

  // The row's return time, which a snooze in flight already shows: it is the moment being saved.
  const returnTime =
    snoozedUntil === null ? null : (
      <span className={`text-xs ${textMutedOnCanvas}`} data-inbox-snoozed-until={snoozedUntil}>
        {isIndefinite(snoozedUntil) ? (
          "Snoozed until you clear it"
        ) : (
          <>
            Back <Timestamp at={snoozedUntil} />
          </>
        )}
      </span>
    );

  if (unsnoozeHeld || (snoozedUntil !== null && !pickHeld)) {
    return (
      <>
        {status}
        {returnTime}
        <button
          aria-label={`Un-snooze ${label}`}
          className={secondaryButtonCompact}
          {...heldPress(held, () => write.submit(null))}
          type="button"
        >
          {pendingLabel ?? "Un-snooze"}
        </button>
        {failure}
      </>
    );
  }

  return (
    <>
      {status}
      {returnTime}
      <span className={badgeSelectWrapper}>
        <span aria-hidden="true" className={`${badgeSelectBadge} ${badgeLow.bg} ${badgeLow.text}`}>
          {pendingLabel ?? "Snooze"}
        </span>
        <select
          aria-disabled={held || undefined}
          aria-label={`Snooze ${label}`}
          className={badgeSelectOverlay}
          data-inbox-snooze=""
          onChange={(event) => {
            // Held, a pick writes nothing, and the select, controlled at `""`, shows no pick.
            if (held) return;
            const preset = SNOOZE_PRESETS.find((option) => option.id === event.target.value);
            if (preset !== undefined) write.submit(preset.until(new Date()));
          }}
          value=""
        >
          <option value="">Snooze…</option>
          {SNOOZE_PRESETS.map((preset) => (
            <option key={preset.id} value={preset.id}>
              {preset.label}
            </option>
          ))}
        </select>
      </span>
      {failure}
    </>
  );
}
