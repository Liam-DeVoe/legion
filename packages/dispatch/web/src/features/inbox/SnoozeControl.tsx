import { type ReactNode, useContext, useEffect, useRef } from "react";
import { ChoiceMenu } from "../../components/ChoiceMenu";
import { heldPress } from "../../components/held-press";
import { QueryError } from "../../components/QueryError";
import { badgeLow, secondaryButtonCompact, textMutedOnCanvas } from "../../theme/classes";
import { badgeMenuTrigger, badgeSelectBadge } from "../issue/badge-select";
import { Timestamp } from "../refs/Timestamp";
import { ControlKeyHint, keyshortcutsOf } from "../shell/ShortcutHelp";
import { InboxHandOffContext } from "./pane-selection";
import { isIndefinite, snoozeChoices, snoozeOf } from "./snooze";
import { type SnoozeSettled, useAskSnooze } from "./useAskSnooze";

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
 * snooze that lands, and the Inbox then hands the reader on to the item after the row
 * (`InboxHandOffContext`, R8); a snooze the server refuses rolls the row back with the reason
 * still attached to it, and leaves the reader on it. Un-snooze is held while its write is out
 * (`heldPress`) and moves nobody.
 *
 * `label` names the row for assistive tech; the Inbox passes the issue key or the document.
 */
export function SnoozeControl({
  askId,
  label,
  onLive,
  shortcutKeys,
  snoozedUntil,
  unsnoozeKeys,
}: {
  askId: string;
  label: string;
  /** Registers this row while its write is in flight or failed; unregistered once it settles
   *  cleanly or the control unmounts. */
  onLive: (askId: string, live: boolean) => void;
  /** The keys that open the menu, where the host draws them on the trigger (the reading pane's
   *  head: `b` and `h`, or the person's own). */
  shortcutKeys?: readonly string[];
  /** The row's own snooze while its moment is still ahead; null on an ordinary row. */
  snoozedUntil: string | null;
  /** The keys that press Un-snooze, where the host draws them on it (the reading pane's head,
   *  with a key a person gave it). */
  unsnoozeKeys?: readonly string[];
}): ReactNode {
  const write = useAskSnooze(askId);
  const handOff = useContext(InboxHandOffContext);
  const live = write.pending || write.failed;
  useEffect(() => {
    if (!live) return;
    onLive(askId, true);
    return () => onLive(askId, false);
  }, [askId, live, onLive]);
  /** The ask whose hand-off this control began and whose press has not settled. A press's own
   *  callbacks run only while this control is mounted, and only for its latest press (TanStack's
   *  `mutate`), so a hand-off the settling can no longer reach is dropped here: kept, the Inbox
   *  would give its target the focus at every commit that finds the focus on the page. */
  const unsettled = useRef<string | undefined>(undefined);
  const dropHandOff = handOff?.dropHandOff;
  useEffect(
    () => () => {
      if (unsettled.current !== undefined) dropHandOff?.([unsettled.current]);
      unsettled.current = undefined;
    },
    [dropHandOff]
  );
  /** Begins the hand-off a snooze press makes, at the press, before its optimistic write moves
   *  the row, and says what its settling tells the Inbox: a landing hands the reader on, a
   *  refusal drops it. An un-snooze, and a control outside an Inbox, hand nothing on. Any press
   *  first drops the hand-off of the press before it that has not settled. */
  const handingOn = (until: string | null | undefined): SnoozeSettled | undefined => {
    if (handOff === undefined) return undefined;
    if (unsettled.current !== undefined) handOff.dropHandOff([unsettled.current]);
    unsettled.current = undefined;
    if (until === null || until === undefined) return undefined;
    handOff.beginHandOff([askId]);
    unsettled.current = askId;
    return (landed) => {
      unsettled.current = undefined;
      if (landed) handOff.landHandOff([askId]);
      else handOff.dropHandOff([askId]);
    };
  };
  // `basis-full` inside the header's wrapping row: a refusal is wider than the badge it
  // replaces, so it takes a line of its own rather than drawing over the issue key.
  const failure = write.failed ? (
    <div className="basis-full">
      <QueryError
        message={write.error ?? `Could not snooze ${label}.`}
        onRetry={() => write.retry(handingOn(write.failedUntil))}
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
          aria-keyshortcuts={keyshortcutsOf(unsnoozeKeys)}
          aria-label={`Un-snooze ${label}`}
          className={secondaryButtonCompact}
          data-inbox-action="unsnooze"
          {...heldPress(write.pending, () => write.submit(null, handingOn(null)))}
          type="button"
        >
          {pendingLabel ?? "Un-snooze"}
          <ControlKeyHint keys={unsnoozeKeys} />
        </button>
      )}
      <ChoiceMenu
        choices={snoozeChoices(snoozedUntil !== null)}
        label={`Snooze ${label}`}
        marks={{ "data-inbox-snooze": "" }}
        onChoose={(choice) => {
          const until = snoozeOf(choice, new Date());
          write.submit(until, handingOn(until));
        }}
        shortcutKeys={shortcutKeys}
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
