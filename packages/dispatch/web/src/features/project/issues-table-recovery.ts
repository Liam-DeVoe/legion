import { useCallback, useEffect, useRef, useState } from "react";
import { readStore, writeStore } from "../../components/table-storage";
import { newSendKey } from "../../lib/send-key";
import { settledWithLimit } from "../../lib/settled";
import { PAGE_LOCKS } from "../shell/sign-in-channel";
import { useWrites } from "../writes/write-store";
import { tableStorageKey, WRITES_IN_FLIGHT } from "./issues-table-model";

/** The kinds an earlier build recorded for its epic tools, which are gone. A stored record of
 *  one is dropped when it is read, and the layout stored beside it stays. */
const REMOVED_KINDS: readonly string[] = ["newEpic", "merge"];
export interface TableAction {
  id: string;
  /** The one action a reload can interrupt. Every record is written with it, so a record kept
   *  by this build or an earlier one reads back as a Start next (`readTableAction`). */
  kind: "startNext";
  project: string;
  keys: readonly string[];
  /** The id of the tab that began the action (`thisTab`). A record of an earlier build has
   *  none, and is another tab's to every tab. */
  tab?: string;
  /** When the action was begun, or last taken up to finish it (`resume`), as epoch milliseconds.
   *  A record older than `ACTION_LIFETIME_MS` is a tab somebody closed a day ago, not work this
   *  reader left half-done. */
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
    action.kind === "startNext" &&
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
  "Another tab has not finished moving these issues. Finish or Dismiss that move first.";
/** What a bulk action says while this tab's own interrupted action is in the store. */
const OWN_ACTION = "Finish or Dismiss the interrupted action before starting another one.";
/** What a bulk action says while a tab still runs the move of the stored record (`isLeftBehind`),
 *  which no tab can finish or dismiss until that move stops. */
const MOVING_ACTION = "Another tab is still moving these issues. Wait until that move is complete.";

/** The Web Lock a tab holds for as long as it runs the move a record names: Start next, or a
 *  Finish of it. The browser lets the lock go when the move ends, and when the tab closes or
 *  crashes, so a stored record whose lock no tab holds is a move a tab left behind. */
function actionLockName(id: string): string {
  return `dispatch-table-action:${id}`;
}

/** The Web Lock a tab holds while it reads whether a bulk action may begin on the listing
 *  `project` and keeps the action's record (`begin`). It is named for the listing's storage key,
 *  so a tab that takes it after another tab reads the record that tab kept, and is refused. */
function beginLockName(project: string): string {
  return `dispatch-table-begin:${tableStorageKey(project)}`;
}

/** A bulk action begun, with what its move answered, or refused, with the text that says why. */
export type Begun<T> =
  | { readonly kind: "begun"; readonly moved: T }
  | { readonly kind: "refused"; readonly text: string };

/**
 * The record of the bulk action a reload can interrupt, kept in the browser so any tab can finish
 * or discard it. One record is kept for a listing, whichever tab began it. So a bulk action is
 * refused while a record is in the store, and the window's `storage` event brings this table
 * another tab's record and its end at once (D12). A tab reads the store and keeps its record
 * under the listing's lock (`beginLockName`), so two tabs that begin at once never both keep
 * one. The tab that runs a record's move holds the record's lock (`actionLockName`) for as long
 * as the move runs, and a record whose lock no tab holds is one a tab left behind
 * (`isLeftBehind`). Only then does a tab offer to Finish or Dismiss it, so no tab ends a move
 * that is still running. A browser with no lock manager has neither lock, and so neither
 * guarantee. A record left behind is finished or discarded in any tab, and its writes keep their
 * send keys. `tab` names this tab for a test that draws two tabs in one window. Without it the
 * tab's own id is read. `locks` is the browser's lock manager, which a test gives as its own.
 */
export function useTableRecovery(
  project: string,
  report: (text: string) => void,
  { tab, locks = PAGE_LOCKS }: { readonly tab?: string; readonly locks?: LockManager } = {}
) {
  const writes = useWrites();
  const [action, setAction] = useState(() => readTableAction(project));
  const current = useRef(action);
  const alive = useRef(true);
  const reportRef = useRef(report);
  reportRef.current = report;
  const owned = useRef(new Map<number, { issue: string; step?: string; action?: string }>());
  const failed = useRef(new Map<number, string>());
  /** The id of the stored record whose lock no tab held when this tab last looked (`look`). It is
   *  read through the ref, which `setUnheld` keeps with the state, so a `begin` an older render
   *  made reads it as it is now. The state draws the table again when it changes. */
  const unheld = useRef<string | undefined>(undefined);
  const [, drawUnheld] = useState<string>();
  const setUnheld = useCallback((id: string | undefined) => {
    unheld.current = id;
    drawUnheld(id);
  }, []);
  /** Calls off this tab's wait for the lock of the record it looked at last. */
  const waiting = useRef<AbortController | undefined>(undefined);
  /** Looks at whether a tab holds the lock of `record`, the record in hand. A lock held is waited
   *  on: the request is granted once the tab that holds it lets it go - its move ended, or the
   *  tab closed or crashed - and this tab lets it go again at once. Called again whenever the
   *  record changes in the store, since another tab can take up its move. */
  const look = useCallback(
    (record: TableAction | undefined) => {
      waiting.current?.abort();
      waiting.current = undefined;
      if (record === undefined || locks === undefined) return;
      const { id } = record;
      const name = actionLockName(id);
      const wait = new AbortController();
      waiting.current = wait;
      const free = () => {
        if (current.current?.id === id) setUnheld(id);
      };
      locks
        .query()
        .then(({ held }) => {
          if (wait.signal.aborted) return;
          if (!held?.some((lock) => lock.name === name)) {
            free();
            return;
          }
          setUnheld(undefined);
          return locks.request(name, { signal: wait.signal }, free);
        })
        .catch(() => {
          // The wait was called off: the record in hand changed, or the table went.
        });
    },
    [locks, setUnheld]
  );
  const remember = useCallback(
    (next: TableAction | undefined) => {
      writeTableAction(project, next);
      const before = current.current;
      current.current = next;
      if (alive.current) setAction(next);
      if (next?.id !== before?.id) look(next);
    },
    [project, look]
  );
  useEffect(() => {
    alive.current = true;
    current.current = readTableAction(project);
    setAction(current.current);
    look(current.current);
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
      // A write of the layout under the same key leaves the record as it was: nothing to draw.
      const next = readTableAction(project);
      if (JSON.stringify(next) === JSON.stringify(current.current)) return;
      current.current = next;
      setAction(next);
      look(next);
    };
    window.addEventListener("storage", fromAnotherTab);
    return () => {
      alive.current = false;
      observe();
      outcome();
      window.removeEventListener("storage", fromAnotherTab);
      waiting.current?.abort();
    };
  }, [project, writes, remember, look]);

  const active = (id: string) => alive.current && current.current?.id === id;
  /** Whether `record` is a move a tab left behind: no tab holds its lock, as this tab saw last.
   *  A browser with no lock manager (`locks === undefined`) cannot tell a running move from one
   *  left behind, so there every record counts as left behind: another tab offers Finish and
   *  Dismiss for a move that still runs. This one rule decides what this tab offers and what it
   *  refuses a bulk action with. */
  const isLeftBehind = (record: TableAction) => locks === undefined || unheld.current === record.id;
  /** Takes `stored`, the record the store holds now, into this table, and looks at its lock. */
  const adopt = (stored: TableAction | undefined) => {
    if (stored?.id !== current.current?.id) {
      current.current = stored;
      if (alive.current) setAction(stored);
    }
    look(stored);
  };
  /** Why a bulk action may not begin now, or `undefined` where it may. The store is read again
   *  here, since another tab can have begun an action after the last `storage` event. A record
   *  of another tab's is taken into this table, so its offer to finish it shows here too once
   *  that record is left behind. */
  const refusal = (): string | undefined => {
    const stored = readTableAction(project);
    if (stored !== undefined && stored.tab !== (tab ?? thisTab())) {
      adopt(stored);
      return isLeftBehind(stored) ? OTHER_TAB_ACTION : MOVING_ACTION;
    }
    const own = stored ?? current.current;
    if (own === undefined) return undefined;
    return isLeftBehind(own) ? OWN_ACTION : MOVING_ACTION;
  };
  return {
    action,
    /** The record in hand where it is a move left behind, which this tab offers to Finish or
     *  Dismiss. */
    leftBehind: action !== undefined && isLeftBehind(action) ? action : undefined,
    current,
    active,
    refusal,
    /** Begins a bulk action. This tab takes the lock of a new record first, which no other tab
     *  asks for, and under it the listing's lock (`beginLockName`). Under both it reads
     *  `refusal`, and keeps the record only where nothing refuses it. Then it lets the listing's
     *  lock go, and runs `move` on the record while the record's lock is held. So of two tabs
     *  that begin at once, the second reads the first's record and is refused: a record is never
     *  written over another tab's. A tab that reads the record finds its lock held from the
     *  start, so no tab offers to end the move while it runs. A browser with no lock manager runs
     *  the same steps with no lock. */
    begin: async <T>(
      record: Omit<TableAction, "id" | "project" | "tab" | "writes" | "startedAt">,
      move: (record: TableAction) => Promise<T>
    ): Promise<Begun<T>> => {
      const id = newSendKey();
      /** Keeps the new record and answers it, or answers the text that refuses it. */
      const keep = (): TableAction | string => {
        const refused = refusal();
        if (refused !== undefined) return refused;
        const next = {
          ...record,
          id,
          project,
          tab: tab ?? thisTab(),
          startedAt: Date.now(),
          writes: {},
        };
        remember(next);
        return next;
      };
      const run = async (kept: TableAction | string): Promise<Begun<T>> =>
        typeof kept === "string"
          ? { kind: "refused", text: kept }
          : { kind: "begun", moved: await move(kept) };
      if (locks === undefined) return run(keep());
      return await locks.request(actionLockName(id), async () =>
        run(await locks.request(beginLockName(project), keep))
      );
    },
    /** Runs `move`, a Finish of the record `id` left behind, while this tab holds its lock. The
     *  record is stamped as taken up now, so every other tab hears of it (`storage`) and offers
     *  it no more. A record whose lock another tab holds, or one no longer in the store, is not
     *  this tab's to finish: `move` does not run, the answer is `undefined`, and this tab looks
     *  at its lock again. */
    resume: async <T>(id: string, move: () => Promise<T>): Promise<T | undefined> => {
      const run = async (free: boolean): Promise<T | undefined> => {
        const stored = readTableAction(project);
        if (!free || stored?.id !== id) {
          setUnheld(undefined);
          adopt(stored);
          return undefined;
        }
        remember({ ...stored, startedAt: Date.now() });
        look(stored);
        return move();
      };
      return locks === undefined
        ? run(true)
        : await locks.request(actionLockName(id), { ifAvailable: true }, (lock) =>
            run(lock !== null)
          );
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
      // A write still in flight is only waited on. A refused one is sent again, at most
      // `WRITES_IN_FLIGHT` at once, so Finish sends a bounded stream however many writes were
      // refused.
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
    /** Ends the record `id` of the move this tab ran, once the move is done. Called while this
     *  tab holds the record's lock. The record goes only while the store still holds it. In a
     *  browser with no lock manager another tab can dismiss the move and keep a record of its own
     *  meanwhile (`isLeftBehind`): that record stays, and this table takes it in. */
    end: (id: string) => {
      const stored = readTableAction(project);
      if (stored?.id === id) remember(undefined);
      else adopt(stored);
    },
    /** The reader's Dismiss of a record left behind. The offer goes at once, and the record goes
     *  once this tab holds its lock: a move another tab took up meanwhile runs on, and this tab
     *  looks at its lock again. */
    dismiss: () => {
      const held = current.current;
      if (held === undefined) return;
      if (locks === undefined) {
        remember(undefined);
        return;
      }
      setUnheld(undefined);
      void locks
        .request(actionLockName(held.id), { ifAvailable: true }, (lock) => {
          const stored = readTableAction(project);
          if (lock !== null && stored?.id === held.id) remember(undefined);
          else adopt(stored);
        })
        .catch(() => {
          // The browser refused the request, as it does for a page that is going: nothing goes.
        });
    },
  };
}
