import { type ReactNode, useEffect } from "react";
import { ChoiceMenu } from "../../components/ChoiceMenu";
import { heldPress } from "../../components/held-press";
import { QueryError } from "../../components/QueryError";
import { badgeLow, secondaryButtonCompact, textMutedOnCanvas } from "../../theme/classes";
import { badgeMenuTrigger, badgeSelectBadge } from "../issue/badge-select";
import { Timestamp } from "../refs/Timestamp";
import { isIndefinite, snoozeChoices, snoozeOf } from "./snooze";
import { useAskSnooze } from "./useAskSnooze";

/**
 * The per-row defer control: the "Snooze" badge, whose press opens the Snooze menu
 * (`ChoiceMenu`) of `snooze.ts`'s presets, the moment computed when the reader picks, not when
 * the page rendered. The Inbox's `b` and `h` press it (`data-inbox-snooze`). On a row already
 * inside its window it also shows the return time and the one button that ends the snooze early,
 * and its menu offers Un-snooze too - nothing else ends it, which is the point: an agent
 * answering a deferred ask leaves it deferred.
 *
 * A snooze folds its row out of the list, which would unmount this control and take its
 * `Snoozing…` and its refusal with it - so, exactly as `AssignToMe` does for the optimistic
 * assignment that can drop a row out of the assignee filter, it tells the Inbox while its write
 * is live and the Inbox keeps the row rendered until it settles. The fold is the feedback for a
 * snooze that lands; a snooze the
 * server refuses rolls the row back with the reason still attached to it. Un-snooze is held
 * while its write is out (`heldPress`).
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
  /** Registers this row while its write is in flight or failed; unregistered once it settles
   *  cleanly or the control unmounts. */
  onLive: (askId: string, live: boolean) => void;
  /** The row's own snooze while its moment is still ahead; null on an ordinary row. */
  snoozedUntil: string | null;
}): ReactNode {
  const write = useAskSnooze(askId);
  const live = write.pending || write.failed;
  useEffect(() => {
    if (!live) return;
    onLive(askId, true);
    return () => onLive(askId, false);
  }, [askId, live, onLive]);
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

  // One tree for both states, the menu at the same place in it: a snooze picked from the trigger
  // hands the focus back to the trigger, then makes the row a snoozed one, and a trigger drawn
  // anew there would take that focus off the page with the one it replaced. On a snoozed row the
  // in-flight label is the Un-snooze button's, beside the return time the write is saving.
  return (
    <>
      {snoozedUntil === null ? null : (
        <span className={`text-xs ${textMutedOnCanvas}`} data-inbox-snoozed-until={snoozedUntil}>
          {isIndefinite(snoozedUntil) ? (
            "Snoozed until you clear it"
          ) : (
            <>
              Back <Timestamp at={snoozedUntil} />
            </>
          )}
        </span>
      )}
      {snoozedUntil === null ? null : (
        <button
          aria-label={`Un-snooze ${label}`}
          className={secondaryButtonCompact}
          data-inbox-action="unsnooze"
          {...heldPress(write.pending, () => write.submit(null))}
          type="button"
        >
          {pendingLabel ?? "Un-snooze"}
        </button>
      )}
      <ChoiceMenu
        choices={snoozeChoices(snoozedUntil !== null)}
        label={`Snooze ${label}`}
        marks={{ "data-inbox-snooze": "" }}
        onChoose={(choice) => write.submit(snoozeOf(choice, new Date()))}
        triggerClassName={badgeMenuTrigger}
      >
        <span className={`${badgeSelectBadge} ${badgeLow.bg} ${badgeLow.text}`}>
          {snoozedUntil === null ? (pendingLabel ?? "Snooze") : "Snooze"}
        </span>
      </ChoiceMenu>
      {failure}
    </>
  );
}
