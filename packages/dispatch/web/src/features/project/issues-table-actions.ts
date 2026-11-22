import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError, api, issuePatchRequest, type WriteRequest } from "../../api/client";
import { mergeIssue } from "../../api/issue-cache";
import { advancesIssuesParams } from "../../api/queries";
import type { IssueSummary, UpdateIssueInput } from "../../api/types";
import { newSendKey } from "../../lib/send-key";
import { plainWrite, useWrites } from "../writes/write-store";
import { type IssueListing, listingName } from "./issues-table-data";
import { buildItems, type Item, queuedInOrder, startNextWrites } from "./issues-table-model";
import { type TableAction, useTableRecovery } from "./issues-table-recovery";

/** One step of an action: the issue it writes, its change, and the write key it was given. */
interface Patch {
  key: string;
  input: UpdateIssueInput;
  write: string;
}
interface Notice {
  kind: "success" | "error";
  text: string;
}
/** An action waiting on the reader's yes, with everything the dialog says and does. */
interface Confirmation {
  title: string;
  body: string;
  confirmLabel: string;
  run: () => void;
}

/** What an interrupted action of each kind says it left behind, and how it is finished. The
 *  record's kind union is the only list of kinds: a new one fails to compile until both are
 *  written for it. */
interface ResumableAction {
  describe: (record: TableAction) => string;
  resume: (record: TableAction, current: readonly IssueSummary[]) => Promise<void>;
}

export function useIssueTableActions({
  listing,
  issues,
  items,
  clearSelection,
}: {
  listing: IssueListing;
  issues: readonly IssueSummary[];
  items: readonly Item[];
  clearSelection: () => void;
}) {
  const client = useQueryClient();
  const writes = useWrites();
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<Notice>();
  const [moved, setMoved] = useState<ReadonlySet<string>>(new Set());
  const [confirming, setConfirming] = useState<Confirmation>();
  // What an interrupted action is recorded under, which is the set it was started on: one
  // priority's work table resumes its own action and not the table of the priority beside it.
  const recoveryKey = listingName(listing);
  const recovery = useTableRecovery(recoveryKey, (text) =>
    setNotice({ kind: text.startsWith("Changes not saved") ? "error" : "success", text })
  );
  /** The open issues of this table's listing, read afresh: what Finish resumes Start next
   *  against. Start next moves only Todo issues, so no closed issue takes part in its writes. */
  const listOpen = () =>
    "project" in listing
      ? api.listIssues({ project: listing.project, open: true })
      : api.listIssues(advancesIssuesParams(listing, true));

  const request = <T>(value: WriteRequest<T>, label: string, issue: string, key: string) => {
    const answer = writes.run({
      kind: plainWrite,
      detail: null,
      key,
      label,
      presentation: "indicator",
      request: value,
      target: `issue:${issue}`,
    });
    // Every write is watched, so a refusal names its issue.
    recovery.watch(key, issue);
    return answer;
  };
  /** Sends one step's patch under the write key the step was given: the action recorded every
   *  key in one storage write before the first send, rather than one write per issue. A refusal
   *  throws the text that names the change not saved. */
  const apply = async ({ key, input, write }: Patch) => {
    try {
      const answer = await request(
        issuePatchRequest(key, input),
        `Update ${key} from the issues table`,
        key,
        write
      );
      if (answer !== null) mergeIssue(client, answer);
      setMoved((current) => new Set([...current, key]));
    } catch (error) {
      throw new Error(
        `0 of 1 changes saved. Changes not saved: ${key}. ${String(error)} Retry each failed change in Pending changes.`
      );
    }
  };
  const perform = async (said: string, action: () => Promise<readonly string[] | undefined>) => {
    setBusy(true);
    setNotice(undefined);
    setMoved(new Set());
    // Whether the action may have changed what the list draws: it answered what it changed, or a
    // write landed while it ran, or ended without Dispatch's verdict (the reader stopped it) and
    // so may have landed. A write Dispatch refused (`ApiError`) changed nothing.
    let mayHaveChanged = false;
    const unwatch = writes.onOutcome((outcome) => {
      if (
        outcome.kind === "landed" ||
        (outcome.kind === "refused" && !(outcome.refusal instanceof ApiError))
      )
        mayHaveChanged = true;
    });
    try {
      const changed = await action();
      if (changed !== undefined) {
        mayHaveChanged = true;
        setNotice({ kind: "success", text: said });
        setMoved(new Set(changed));
        clearSelection();
      }
    } catch (error) {
      setNotice({ kind: "error", text: error instanceof Error ? error.message : String(error) });
    } finally {
      unwatch();
      // An action that may have changed the list keeps its controls held (`busy`) through the read
      // of the list that draws what it changed - a row's Start next gone once its issue starts
      // next - so that going is still the press's doing (`causedByReader`); a batch that saved
      // some of its changes before a refusal changed the list too. The read settles either way,
      // and a failed one lets them go. An action that changed nothing - every write refused, or
      // none made - lets them go at once, while the list is still read again behind them.
      const listed = client.invalidateQueries({ queryKey: ["issues"] });
      void client.invalidateQueries({ queryKey: ["issue"] });
      void client.invalidateQueries({ queryKey: ["inbox"] });
      if (mayHaveChanged) await listed;
      setBusy(false);
    }
  };
  const titleOf = (key: string) => issues.find((issue) => issue.key === key)?.title ?? key;
  const named = (keys: readonly string[]) =>
    keys.length === 1 ? `${keys[0]} “${titleOf(keys[0] ?? "")}”` : `${keys.length} issues`;
  /** Whether a bulk action may begin. Where it may not, the notice says why: another tab's move
   *  or this tab's own interrupted action is still in the store (`useTableRecovery`). */
  const canBegin = () => {
    const refused = recovery.refusal();
    if (refused === undefined) return true;
    setNotice({ kind: "error", text: refused });
    return false;
  };
  /** The rank writes of a start-next action, sent in order, stopping the moment the action is
   *  no longer the one in hand: the table went, or, in a browser with no lock manager, another
   *  tab dismissed it. One loop and one guard serve the first attempt and the resume; `failure`
   *  is how the attempt names what did not move. The answer says whether the action is still
   *  the one in hand when the last write landed. */
  const runStartNext = async (
    record: TableAction,
    current: readonly Item[],
    failure: (remaining: readonly string[], error: unknown) => string = (_remaining, error) =>
      error instanceof Error ? error.message : String(error)
  ) => {
    // Every step's key, minted and stored in one write before the first send: 250 issues move
    // up under one storage write rather than 250 read-modify-write cycles of the whole store.
    const steps = startNextWrites(current, record.keys).map((operation) => ({
      ...operation,
      write: newSendKey(),
    }));
    recovery.record(Object.fromEntries(steps.map(({ key, write }) => [key, write])), record.id);
    for (const [index, step] of steps.entries()) {
      if (!recovery.active(record.id)) return false;
      try {
        await apply(step);
      } catch (error) {
        throw new Error(
          failure(
            steps.slice(index).map(({ key }) => key),
            error
          )
        );
      }
    }
    return recovery.active(record.id);
  };
  /** Sends a start-next action for queued issues already checked by `startNext`. The action is
   *  performed only once it is begun, so a begin that is refused sets nothing busy and reads
   *  nothing again: it changed nothing, and its notice says why. */
  const runQueued = (queued: readonly string[]) => {
    if (!startNextWrites(items, queued).length) {
      setNotice({
        kind: "success",
        text: `${named(queued)} already ${queued.length === 1 ? "starts" : "start"} next.`,
      });
      return;
    }
    // Refused here too: another tab can begin a move while the confirm is open.
    void recovery
      .begin({ kind: "startNext", keys: queued }, (record) =>
        perform(
          `${named(queued)} now ${queued.length === 1 ? "starts" : "start"} next.`,
          async () => {
            const ran = await runStartNext(
              record,
              items,
              (remaining, error) =>
                `Did not move up: ${remaining.join(", ")}. ${error instanceof Error ? error.message : String(error)}`
            );
            // Ended while this tab still holds the record's lock, so no tab finds it left behind.
            if (ran) recovery.end();
            return ran ? queued : undefined;
          }
        )
      )
      .then(
        (begun) => {
          if (begun.kind === "refused") setNotice({ kind: "error", text: begun.text });
        },
        (error: unknown) =>
          setNotice({ kind: "error", text: error instanceof Error ? error.message : String(error) })
      );
  };
  /**
   * Starts `keys` next. Start next keeps its own set, the Todo issues labelled `legion` in rank
   * order, and each issue of that set among `keys` moves ahead of the others (`startNextWrites`).
   * A selection with none of the set is refused with a text that names the set. The reader is
   * asked first where some selected issues are outside the set, with how many stay where they
   * are, and where the selection holds rows off the page (`confirmFirst`: `Select all N
   * matching`), since each issue is one write.
   */
  const startNext = (keys: readonly string[], confirmFirst = false) => {
    if (!canBegin()) return;
    const queued = queuedInOrder(items, keys);
    if (!queued.length) {
      setNotice({ kind: "error", text: "Start next moves only Todo issues labelled legion." });
      return;
    }
    const outside = keys.length - queued.length;
    if (confirmFirst || outside > 0) {
      const moves = `${queued.length} ${queued.length === 1 ? "issue moves" : "issues move"} ahead of the other Todo issues labelled legion.`;
      const stays =
        outside === 0
          ? ""
          : outside === 1
            ? " 1 selected issue is not a Todo issue labelled legion. It stays where it is."
            : ` ${outside} selected issues are not Todo issues labelled legion. They stay where they are.`;
      setConfirming({
        title: "Start these next?",
        body: `${moves} Dispatch sends one change for each issue.${stays}`,
        confirmLabel: "Start next",
        run: () => runQueued(queued),
      });
      return;
    }
    runQueued(queued);
  };

  const kinds: Record<TableAction["kind"], ResumableAction> = {
    startNext: {
      describe: (record) =>
        `Start next: ${record.keys.join(", ")} may still need to move up, in that order.`,
      resume: async (record, current) => {
        await runStartNext(
          record,
          buildItems(current, () => "")
        );
      },
    },
  };
  /** Finishes the record left behind, while this tab holds its lock (`recovery.resume`): no
   *  other tab runs that move meanwhile, and none offers to end it. */
  const finish = () => {
    const interrupted = recovery.current.current;
    if (!interrupted) return;
    void perform("Finished the interrupted action.", () =>
      recovery.resume(interrupted.id, async () => {
        await recovery.settle(interrupted);
        if (!recovery.active(interrupted.id)) return undefined;
        const record = recovery.current.current;
        if (!record) return undefined;
        const current = await listOpen();
        if (!recovery.active(record.id)) return undefined;
        await kinds[record.kind].resume(record, current);
        if (!recovery.active(record.id)) return undefined;
        recovery.end();
        return record.keys;
      })
    );
  };
  // Offered only where no tab runs its move: a move another tab still runs is that tab's.
  const interrupted = busy ? undefined : recovery.leftBehind;
  return {
    busy,
    notice,
    moved,
    interrupted,
    interruptedDescription: interrupted && kinds[interrupted.kind].describe(interrupted),
    confirming,
    cancelConfirming: () => setConfirming(undefined),
    confirmed: () => {
      const waiting = confirming;
      setConfirming(undefined);
      waiting?.run();
    },
    finish,
    dismiss: recovery.dismiss,
    startNext,
  };
}
