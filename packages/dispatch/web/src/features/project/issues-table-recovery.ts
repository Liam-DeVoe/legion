import { useCallback, useEffect, useRef, useState } from "react";
import { readStore, writeStore } from "../../components/table-storage";
import { newSendKey } from "../../lib/send-key";
import { settledWithLimit } from "../../lib/settled";
import { useWrites } from "../writes/write-store";
import { tableStorageKey, WRITES_IN_FLIGHT } from "./issues-table-model";

/** The kinds of action a reload can interrupt. The union and the check a stored record passes
 *  are one list, so neither can gain a kind the other refuses. */
export const ACTION_KINDS = ["startNext"] as const;
export interface TableAction {
  id: string;
  kind: (typeof ACTION_KINDS)[number];
  project: string;
  keys: readonly string[];
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
 *  one storage key, and read back only where every field is the kind it was written as. A
 *  record of a kind this bundle no longer has is dropped. */
export function readTableAction(project: string): TableAction | undefined {
  const action = readStore(tableStorageKey(project)).action as TableAction | undefined;
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
    (action.startedAt === undefined ||
      (typeof action.startedAt === "number" && Date.now() - action.startedAt < ACTION_LIFETIME_MS))
  )
    return action;
  return undefined;
}

export function writeTableAction(project: string, action: TableAction | undefined): void {
  writeStore(tableStorageKey(project), { action });
}

export function useTableRecovery(project: string, report: (text: string) => void) {
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
        // change retried beside one reports like any other change.
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
    return () => {
      alive.current = false;
      observe();
      outcome();
    };
  }, [project, writes, remember]);

  const active = (id: string) => alive.current && current.current?.id === id;
  return {
    action,
    current,
    active,
    begin: (record: Omit<TableAction, "id" | "project" | "writes" | "startedAt">) => {
      const next = { ...record, id: newSendKey(), project, startedAt: Date.now(), writes: {} };
      remember(next);
      return next;
    },
    /** The keys of the writes a step of `id`'s action is about to send, kept with the record so
     *  a resume can wait on them. One storage write and one render for the whole batch: a
     *  250-row action records its keys once, not once per issue. */
    record: (steps: Readonly<Record<string, string>>, id?: string) => {
      const held = current.current;
      if (id && held?.id === id && Object.keys(steps).length)
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
