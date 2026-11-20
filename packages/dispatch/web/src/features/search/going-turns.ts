import { useSyncExternalStore } from "react";

import { api, goingUndoRequest } from "../../api/client";
import type { GoingUndoLine } from "../../api/types";
import { defineWriteKind, isLive, type Write, type Writes } from "../writes/write-store";
import { refusalText } from "./offer";

/**
 * The bar's turns: what each run's undo did, kept outside the bar so that the undo key
 * (`$mod+z`, `app.tsx`) works from any page and the shell says what it did.
 *
 * A turn's undo goes out on the run's own line (`goingRunTarget`): it stops any write of the run
 * still waiting its turn there, waits for one that is out, and then asks Dispatch to undo every
 * write of the run that went through (`POST /api/v1/going/runs/{id}/undo`), each by the undo its
 * catalogue row states. An undo that lands has the bar read its hits again (`goingWrite`), so a
 * hit shows what the undo left rather than what the search read before it.
 */

/** Where a turn's undo stands: out, answered with one line per write it undid, or refused as a
 *  whole. `stopped` counts the writes it stopped before they were sent. */
export interface TurnUndo {
  readonly state: "undoing" | "undone" | "refused";
  readonly lines: readonly GoingUndoLine[];
  readonly stopped: number;
  readonly message: string | undefined;
}

/** One run of the bar, as this page undid it. */
export interface Turn {
  readonly runId: string;
  readonly words: string;
  readonly undo: TurnUndo | undefined;
}

/** The undo the undo key made last, which the shell says, since the key works from any page. */
export interface LastUndo extends TurnUndo {
  readonly runId: string;
  readonly words: string;
}

const TARGET_PREFIX = "going-run:";

/**
 * The kind of every turn's undo: a plain write that, once it lands, has the bar read its hits
 * again (`["search", …]`, `useTieredSearch`), since a hit shows the status and title of the item
 * the undo put back, read afresh from Dispatch rather than patched here. A landing no surface
 * awaits, or one without an answer, reads every query again, as a plain write's does.
 */
const goingWrite = defineWriteKind<null>({
  kept: (value): value is null => value === null,
  landed: (client, _write, answer, awaited) => {
    if (!awaited || answer === null) {
      void client.invalidateQueries();
      return;
    }
    void client.invalidateQueries({ queryKey: ["search"] });
  },
  name: "going-write",
});

/** The line every write of one run goes out on, in press order. */
function goingRunTarget(runId: string): string {
  return `${TARGET_PREFIX}${runId}`;
}

function runOfTarget(target: string | undefined): string | undefined {
  return target?.startsWith(TARGET_PREFIX) === true
    ? target.slice(TARGET_PREFIX.length)
    : undefined;
}

let turns: ReadonlyMap<string, Turn> = new Map();
let lastUndo: LastUndo | undefined;
/** The runs whose undo, out now, the shell says as it goes: an undo the key made, or one already
 *  out from a turn's Undo that the key was pressed during. */
const followed = new Set<string>();
const listeners = new Set<() => void>();

function publish(): void {
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function changeTurn(runId: string, words: string, change: (turn: Turn) => Turn): void {
  const current = turns.get(runId) ?? { runId, undo: undefined, words };
  const next = new Map(turns);
  next.set(runId, change(current));
  turns = next;
  publish();
}

/** The undo the undo key made last, until the person dismisses what the shell says of it. */
export function useLastUndo(): LastUndo | undefined {
  return useSyncExternalStore(subscribe, () => lastUndo);
}

export function dismissLastUndo(): void {
  lastUndo = undefined;
  publish();
}

/** Forgets every turn: a test's reset. */
export function forgetTurns(): void {
  turns = new Map();
  lastUndo = undefined;
  followed.clear();
  publish();
}

/** An action as the bar names it: the catalogue's name without its prefix. */
export function actionName(action: string): string {
  return action.replace(/^dispatch_/, "").replaceAll("_", " ");
}

/** The item an invocation's arguments name: the value of `argument`, the argument Dispatch says
 *  names it (`GoingInvocation.target_argument`), and `""` where it names none. */
export function targetOf(args: Readonly<Record<string, unknown>>, argument: string): string {
  const value = argument === "" ? undefined : args[argument];
  return typeof value === "string" ? value : "";
}

/** Stops every write of the runs `inRun` names that waits its turn and was never sent: it has not
 *  reached Dispatch, so undoing the turn is not sending it. Answers how many it stopped. */
function stopWaiting(writes: Writes, inRun: (runId: string) => boolean): number {
  let stopped = 0;
  for (const write of writes.rows()) {
    const runId = runOfTarget(write.target);
    if (runId === undefined || !inRun(runId) || write.status !== "queued" || write.sent) continue;
    if (writes.discard(write.id)) stopped += 1;
  }
  return stopped;
}

/** Settles once no write of the runs `inRun` names is out: each may have reached Dispatch, so the
 *  undo waits for what Dispatch made of it. */
function writesSettled(writes: Writes, inRun: (runId: string) => boolean): Promise<void> {
  const out = () =>
    writes.rows().some((write) => {
      const runId = runOfTarget(write.target);
      return runId !== undefined && inRun(runId) && isLive(write);
    });
  if (!out()) return Promise.resolve();
  const { promise, resolve } = Promise.withResolvers<void>();
  const stop = writes.subscribe(() => {
    if (out()) return;
    stop();
    resolve();
  });
  return promise;
}

/** The reason an issue the turn created is closed with, and the note an ask's retraction and a
 *  document's restoring version carry. */
function undoReason(words: string): string | undefined {
  return words === "" ? undefined : `Undone from the command bar: “${words}”`;
}

function setUndo(runId: string, words: string, undo: TurnUndo, notice: boolean): void {
  if (notice) followed.add(runId);
  if (followed.has(runId)) {
    lastUndo = { ...undo, runId, words };
    if (undo.state !== "undoing") followed.delete(runId);
  }
  changeTurn(runId, words, (turn) => ({ ...turn, undo }));
}

/**
 * Undoes the turn of `runId`: every write of it still waiting its turn is stopped before it is
 * sent, every write that is out is waited for, and Dispatch undoes every write of it that went
 * through, one line each, a field another writer changed since refused in its own line and the
 * rest undone. `notice` says the undo in the shell as well (the undo key's, from any page).
 */
export async function undoGoingTurn(
  writes: Writes,
  runId: string,
  words = "",
  notice = false
): Promise<void> {
  // The words the turn's undo names: the caller's, else the turn's own.
  const said = words === "" ? (turns.get(runId)?.words ?? "") : words;
  const out = turns.get(runId)?.undo;
  if (out?.state === "undoing") {
    // An undo of this turn is out already, so this is that undo and not a second: the shell,
    // where this one would have said itself, follows that one and settles when it does.
    if (notice) {
      followed.add(runId);
      lastUndo = { ...out, runId, words: said };
      publish();
    }
    return;
  }
  const inRun = (id: string) => id === runId;
  const stopped = stopWaiting(writes, inRun);
  setUndo(runId, said, { lines: [], message: undefined, state: "undoing", stopped }, notice);
  await writesSettled(writes, inRun);
  const reason = undoReason(said);
  try {
    const answer = await writes.run({
      detail: null,
      inline: true,
      kind: goingWrite,
      label: "Undo the command bar's turn",
      presentation: "indicator",
      request: goingUndoRequest(runId, reason === undefined ? {} : { reason }),
      target: goingRunTarget(runId),
    });
    setUndo(
      runId,
      said,
      {
        lines: answer?.lines ?? [],
        message:
          answer === null
            ? "Dispatch undid the turn and kept no answer naming each write."
            : undefined,
        state: "undone",
        stopped,
      },
      notice
    );
  } catch (error) {
    setUndo(
      runId,
      said,
      {
        lines: [],
        message: refusalText(error, "the undo did not reach Dispatch."),
        state: "refused",
        stopped,
      },
      notice
    );
  }
}

/**
 * The undo key: undoes the person's newest turn of the bar from whatever page they are on. A turn
 * this page is still writing is that turn; with none, it is the newest undoable run Dispatch
 * holds for them (`GET /api/v1/me/going-runs?undoable=true&limit=1`), so the key works after a
 * reload and from a page the bar was never opened on.
 */
export async function undoNewestGoingTurn(writes: Writes): Promise<void> {
  // A press while an undo is out is that undo's: a second would undo the turn again.
  if (lastUndo?.state === "undoing") return;
  const writing = writes
    .rows()
    .filter((write) => runOfTarget(write.target) !== undefined && isLive(write))
    .reduce<Write | undefined>(
      (newest, write) =>
        newest === undefined || write.pressedAt > newest.pressedAt ? write : newest,
      undefined
    );
  const local = runOfTarget(writing?.target);
  if (local !== undefined) {
    await undoGoingTurn(writes, local, "", true);
    return;
  }
  // What the key says before it knows which turn is the person's.
  const say = (state: TurnUndo["state"], message: string | undefined) => {
    lastUndo = { lines: [], message, runId: "", state, stopped: 0, words: "" };
    publish();
  };
  say("undoing", undefined);
  try {
    const page = await api.myGoingRuns({ limit: 1, undoable: true });
    const run = page.rows?.[0];
    if (run === undefined) {
      say("undone", "Nothing to undo: no turn of the command bar has a write left to undo.");
      return;
    }
    await undoGoingTurn(writes, run.id, run.words, true);
  } catch (error) {
    say("refused", refusalText(error, "Dispatch did not say which turn is yours to undo."));
  }
}

/** How one write's undo went, in the words a line says it in. */
export function undoneText(line: GoingUndoLine): string {
  if (!line.outcome.ok) {
    const fields = (line.fields ?? []).filter((field) => !field.ok);
    return fields.length === 0
      ? `not undone: ${line.outcome.message}`
      : `not undone: ${fields.map((field) => `${field.field}: ${field.message}`).join("; ")}`;
  }
  switch (line.undo) {
    case "write_back":
      return "written back";
    case "reverse":
      return "reversed";
    case "close_created":
      return "closed";
    case "not_undone":
      return "kept, and marked undone";
  }
}
