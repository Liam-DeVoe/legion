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
 * A snooze folds its row out of the list, which would unmount this control and take its
 * `Snoozing…` and its refusal with it - so, exactly as `AssignToMe` does for the optimistic
 * assignment that can drop a row out of the assignee filter, it tells the Inbox while its write
 * is live and the Inbox keeps the row rendered until it settles. The fold is the feedback for a
 * snooze that lands; a snooze the server refuses rolls the row back with the reason still
 * attached to it. The Snooze select and Un-snooze are held from the press until the commit after
 * their write's answer (`useHeldThroughAnswer`), the one in which the Inbox lets go of the row:
 * the select by `aria-disabled`, with a pick then writing nothing, and Un-snooze by `heldPress`.
 * The optimistic moment puts the row inside its window at once, where Un-snooze would be drawn in
 * the select's place; the select stays drawn instead, held, with the return time before it, while
 * its pick's write is out and through the commit after its answer. The pick is the reader's press,
 * and the row's going after the answer is that press's doing (`causedByReader`) only while the
 * control it landed on is still in the page and held: a select taken away at the pick would leave
 * the row's fold to drop the focus onto the page.
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
  // Which band the row sits in is the optimistic update's answer and is right either way -
  // a snooze in flight already shows the return time it is saving. What the row cannot say is
  // which way the reader is going, so the in-flight label comes from the write's own variable
  // (a moment is a snooze, null is an un-snooze) and never from `snoozedUntil`, which by then
  // describes the destination. It is read only while a write is live: `variables` outlives the
  // write, so a settled row would otherwise keep reporting the last thing it did.
  const pendingLabel = !write.pending
    ? undefined
    : write.pendingUntil === null
      ? "Un-snoozing…"
      : "Snoozing…";

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

  if (snoozedUntil !== null && !pickHeld) {
    return (
      <>
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
