import { useCallback, useEffect, useRef, useState } from "react";
import { readStore, writeStore } from "../../components/table-storage";
import { newSendKey } from "../../lib/send-key";
import { settledWithLimit } from "../../lib/settled";
import { useWrites } from "../writes/write-store";
import { tableStorageKey, WRITES_IN_FLIGHT } from "./issues-table-model";

/** The kinds of action a reload can interrupt. The union and the check a stored record passes
 *  are one list, so neither can gain a kind the other refuses. */
export const ACTION_KINDS = ["startNext"] as const;
/** The kinds an earlier build recorded for its epic tools, which are gone. A stored record of
 *  one is dropped when it is read, and the layout stored beside it stays. */
const REMOVED_KINDS: readonly string[] = ["newEpic", "merge"];
export interface TableAction {
  id: string;
  kind: (typeof ACTION_KINDS)[number];
  project: string;
  keys: readonly string[];
  /** The id of the tab that began the action (`thisTab`). A record of an earlier build has
   *  none, and is another tab's to every tab. */
  tab?: string;
  /** When the action was begun, as epoch milliseconds. A record older than `ACTION_LIFETIME_MS`
   *  is a tab somebody closed a day ago, not work this reader left half-done. */
  startedAt?: number;
  writes: Record<string, string>;
}
/** How long a stored action is still the reader's to finish: a day. Past that the banner goes
 *  and a new action may begin, because the writes it named have long since landed or been
 *  discarded in Pending changes. */
export const ACTION_LIFETIME_MS = 24 * 60 * 60 * 1000;

/** The action record a reload interrupted, kept beside the reader's layout under the project's
 *  one storage key, and read back only where every field is the kind it was written as. */
export function readTableAction(project: string): TableAction | undefined {
  const action = readStore(tableStorageKey(project)).action as TableAction | undefined;
  if (REMOVED_KINDS.includes(String(action?.kind))) {
    writeTableAction(project, undefined);
    return undefined;
  }
  if (
    action?.project === project &&
    typeof action.id === "string" &&
    (ACTION_KINDS as readonly string[]).includes(action.kind) &&
    Array.isArray(action.keys) &&
    action.keys.every((key: unknown) => typeof key === "string") &&
    action.writes &&
    typeof action.writes === "object" &&
    !Array.isArray(action.writes) &&
    Object.values(action.writes).every((key) => typeof key === "string") &&
    (action.tab === undefined || typeof action.tab === "string") &&
    (action.startedAt === undefined ||
      (typeof action.startedAt === "number" && Date.now() - action.startedAt < ACTION_LIFETIME_MS))
  )
    return action;
  return undefined;
}

export function writeTableAction(project: string, action: TableAction | undefined): void {
  writeStore(tableStorageKey(project), { action });
}

/** Where a tab keeps its id: its `sessionStorage`, which a reload keeps and no other tab shares. */
const TAB_STORAGE_KEY = "dispatch.table-tab";
/** The id of a tab whose browser refuses `sessionStorage`, which lasts as long as the page. */
let pageTab: string | undefined;

/** This tab's id, made the first time a table asks for it, which is when the reader begins a
 *  bulk action. A record carries the id of the tab that began it, so a reload of that tab knows
 *  the record for its own, and every other tab knows it for another tab's. */
function thisTab(): string {
  try {
    const kept = window.sessionStorage.getItem(TAB_STORAGE_KEY);
    if (kept !== null) return kept;
    const made = newSendKey();
    window.sessionStorage.setItem(TAB_STORAGE_KEY, made);
    return made;
  } catch {
    pageTab ??= newSendKey();
    return pageTab;
  }
}

/** What a bulk action says while another tab's record is in the store (D12). */
const OTHER_TAB_ACTION =
  "Another tab has not finished moving these issues. Finish or discard that move first.";
/** What a bulk action says while this tab's own interrupted action is in the store. */
const OWN_ACTION = "Finish or Dismiss the interrupted action before starting another one.";

/** A bulk action begun, with the record that names it, or refused, with the text that says why. */
export type Begun =
  | { readonly kind: "begun"; readonly record: TableAction }
  | { readonly kind: "refused"; readonly text: string };

/**
 * The record of the bulk action a reload can interrupt, kept in the browser so any tab can finish
 * or discard it. One record is kept for a listing, whichever tab began it. So a bulk action is
 * refused while the record of another tab is in the store, and the window's `storage` event
 * brings this table another tab's record and its end at once (D12). A record a closed tab left
 * is finished or discarded in any tab, and its writes keep their send keys. `tab` names this
 * tab for a test that draws two tabs in one window. Without it the tab's own id is read.
 */
export function useTableRecovery(project: string, report: (text: string) => void, tab?: string) {
  const writes = useWrites();
  const [action, setAction] = useState(() => readTableAction(project));
  const current = useRef(action);
  const alive = useRef(true);
  const reportRef = useRef(report);
  reportRef.current = report;
  const owned = useRef(new Map<number, { issue: string; step?: string; action?: string }>());
  const failed = useRef(new Map<number, string>());
  const remember = useCallback(
    (next: TableAction | undefined) => {
      writeTableAction(project, next);
      current.current = next;
      if (alive.current) setAction(next);
    },
    [project]
  );
  useEffect(() => {
    alive.current = true;
    current.current = readTableAction(project);
    setAction(current.current);
    const observeRows = () => {
      let record = current.current;
      if (!record) return;
      const steps = new Map(Object.entries(record.writes).map(([step, key]) => [key, step]));
      for (const row of writes.rows()) {
        const step: string | undefined =
          steps.get(row.key) ??
          (owned.current.get(row.id)?.action === record.id
            ? owned.current.get(row.id)?.step
            : undefined);
        if (!step) continue;
        owned.current.set(row.id, { issue: row.label, step, action: record.id });
        if (record.writes[step] !== row.key) {
          record = { ...record, writes: { ...record.writes, [step]: row.key } };
          remember(record);
        }
      }
    };
    const observe = writes.subscribe(observeRows);
    observeRows();
    const outcome = writes.onOutcome((event) => {
      if (!owned.current.has(event.write.id)) return;
      const record = readTableAction(project);
      if (record?.id === current.current?.id) {
        current.current = record;
        setAction(record);
      }
      if (event.kind === "refused") {
        failed.current.set(
          event.write.id,
          owned.current.get(event.write.id)?.issue ?? event.write.label
        );
      } else if (failed.current.delete(event.write.id)) {
        // Only a write of the interrupted action's own says anything about finishing it: a
        // write of an action the reader dismissed reports like any other change.
        const ofAction =
          current.current !== undefined &&
          owned.current.get(event.write.id)?.action === current.current.id;
        reportRef.current(
          failed.current.size
            ? `Changes not saved: ${[...failed.current.values()].join(", ")}. Retry them in Pending changes.`
            : event.kind === "discarded"
              ? "The failed change was discarded."
              : ofAction
                ? "The retried changes are saved. Finish the interrupted action to complete its remaining steps."
                : "All changes saved."
        );
      }
      if (event.kind === "discarded") {
        // A discarded write no longer exists, so the record must not go on claiming it: the
        // step goes with it, which keeps `settle` from waiting on a row that is gone and lets
        // the resume work that step out from the server again.
        const owner = owned.current.get(event.write.id);
        const held = current.current;
        if (
          owner?.step &&
          held &&
          owner.action === held.id &&
          held.writes[owner.step] !== undefined
        ) {
          const { [owner.step]: _discarded, ...rest } = held.writes;
          remember({ ...held, writes: rest });
        }
      }
      if (event.kind !== "refused") owned.current.delete(event.write.id);
    });
    // Another tab's record, and its end, reach this tab through the window's `storage` event,
    // which every tab of the browser but the one that wrote is told of.
    const storageKey = tableStorageKey(project);
    const fromAnotherTab = (event: StorageEvent) => {
      if (event.key !== null && event.key !== storageKey) return;
      current.current = readTableAction(project);
      setAction(current.current);
    };
    window.addEventListener("storage", fromAnotherTab);
    return () => {
      alive.current = false;
      observe();
      outcome();
      window.removeEventListener("storage", fromAnotherTab);
    };
  }, [project, writes, remember]);

  const active = (id: string) => alive.current && current.current?.id === id;
  /** Why a bulk action may not begin now, or `undefined` where it may. The store is read again
   *  here, since another tab can have begun an action after the last `storage` event. A record
   *  of another tab's is taken into this table, so its offer to finish it shows here too. */
  const refusal = (): string | undefined => {
    const stored = readTableAction(project);
    if (stored !== undefined && stored.tab !== (tab ?? thisTab())) {
      if (stored.id !== current.current?.id) {
        current.current = stored;
        if (alive.current) setAction(stored);
      }
      return OTHER_TAB_ACTION;
    }
    return stored !== undefined || current.current !== undefined ? OWN_ACTION : undefined;
  };
  return {
    action,
    current,
    active,
    refusal,
    /** Begins a bulk action and keeps its record, unless `refusal` refuses it: a record is
     *  never written over another tab's. */
    begin: (
      record: Omit<TableAction, "id" | "project" | "tab" | "writes" | "startedAt">
    ): Begun => {
      const refused = refusal();
      if (refused !== undefined) return { kind: "refused", text: refused };
      const next = {
        ...record,
        id: newSendKey(),
        project,
        tab: tab ?? thisTab(),
        startedAt: Date.now(),
        writes: {},
      };
      remember(next);
      return { kind: "begun", record: next };
    },
    /** The keys of the writes a step of `id`'s action is about to send, kept with the record so
     *  a resume can wait on them. One storage write and one render for the whole batch: a
     *  250-row action records its keys once, not once per issue. */
    record: (steps: Readonly<Record<string, string>>, id: string) => {
      const held = current.current;
      if (held?.id === id && Object.keys(steps).length)
        remember({ ...held, writes: { ...held.writes, ...steps } });
    },
    watch: (key: string, issue: string) => {
      const row = writes.rows().find((value) => value.key === key);
      if (row) owned.current.set(row.id, { ...owned.current.get(row.id), issue });
    },
    settle: async (record: TableAction) => {
      const rows = writes.rows().filter((row) => Object.values(record.writes).includes(row.key));
      const outcome = (row: (typeof rows)[number]) => {
        const { promise, resolve, reject } = Promise.withResolvers<void>();
        const unsubscribe = writes.onOutcome((event) => {
          if (event.write.id !== row.id) return;
          unsubscribe();
          if (event.kind === "landed") resolve();
          else
            reject(
              event.kind === "refused" ? event.refusal : new Error(`${row.label} was discarded.`)
            );
        });
        return promise;
      };
      // A write still in flight is only waited on; a refused one is sent again, and no more of
      // those run at once than a bulk action sends, so Finish after a 250-row refusal is the
      // same bounded stream the action itself was.
      const waiting = rows.filter((row) => row.status !== "refused").map(outcome);
      const retried = settledWithLimit(
        rows.filter((row) => row.status === "refused"),
        WRITES_IN_FLIGHT,
        async (row) => {
          // Between the read above and this row's turn the reader may have retried it in
          // Pending changes, and a write already on its way answers no second retry: asking
          // for one would leave Finish waiting for an outcome that never comes.
          const now = writes.rows().find((current) => current.id === row.id);
          if (now === undefined) return;
          if (now.status !== "refused") {
            await outcome(row);
            return;
          }
          const landed = outcome(row);
          writes.retry(row.id);
          await landed;
        }
      );
      const [, results] = await Promise.all([Promise.all(waiting), retried]);
      const refused = results.find((result) => result.status === "rejected");
      if (refused?.status === "rejected") throw refused.reason;
    },
    dismiss: () => remember(undefined),
  };
}
