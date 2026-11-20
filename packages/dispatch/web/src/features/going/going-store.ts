import { useSyncExternalStore } from "react";

import { api, apiErrorMessage, REQUEST_DEADLINE_MS, type WriteBounds } from "../../api/client";
import type { GoingContextInput, GoingConversation, GoingRun } from "../../api/types";
import { newSendKey } from "../../lib/send-key";
import { type PanelEvent, type PanelKeys, type PanelPlace, panelNext } from "./panel-states";
import { type RunFrames, STARTED_RUN, stateOfRecord, withRunFrame } from "./run-frames";
import { type RunState, runEnded, runNext } from "./run-states";

/**
 * The command bar's conversation, which outlives every open of the bar: where it is on the screen
 * (`panel-states.ts`), its runs and how each stands as its frames have said (`run-frames.ts`),
 * the level the next run takes, the item a click on the page made the next request's context,
 * the rows of the open plan the page draws, and the person's ticks and edits of that plan. It is
 * one store of the page, held outside React so the bar over the page, the panel at the side, the
 * chip and the five pages that draw a plan all read the one conversation. Each run it holds is
 * followed on its stream until it ends, whether or not the bar is open: a run goes on in the
 * server when the bar closes, and the chip says how it stands.
 */

/** How much work a run may do (R10): Fast, Standard or Deep. */
export type RunLevel = "fast" | "standard" | "deep";

/** One run as the page knows it: its words, its conversation, what its frames say, what its
 *  record said where it was read from History before its frames, whether it was started on this
 *  page, whether the person has seen how it ended, and why its stream could not be read. */
export interface RunEntry {
  readonly id: string;
  readonly words: string;
  readonly conversation: string;
  readonly frames: RunFrames;
  /** How many frames of the run's stream have been read. */
  readonly read: number;
  readonly record: GoingRun | undefined;
  readonly startedHere: boolean;
  readonly seen: boolean;
  readonly unreadable: string | undefined;
}

/** The person's answer to the open plan so far: the rows they unticked, and the values they
 *  edited, by row, for the plan numbered `plan` of `run`. */
export interface PlanDraft {
  readonly run: string;
  readonly plan: number;
  readonly unticked: ReadonlySet<number>;
  readonly edits: ReadonlyMap<number, Readonly<Record<string, unknown>>>;
}

/** The item a click on the page named, and the page it was clicked on. */
export interface ClickedItem {
  readonly context: GoingContextInput;
  readonly path: string;
}

/** Everything the store holds. */
export interface GoingState {
  readonly place: PanelPlace;
  /** The open conversation's runs, oldest first; undefined while none is open. */
  readonly conversation: readonly string[] | undefined;
  readonly runs: ReadonlyMap<string, RunEntry>;
  readonly level: RunLevel;
  /** The item a click on the page named. */
  readonly clicked: ClickedItem | undefined;
  /** The plan rows each page surface draws, by the surface's id. */
  readonly onPage: ReadonlyMap<string, readonly number[]>;
  readonly draft: PlanDraft | undefined;
  /** Whether a run's start is out. */
  readonly starting: boolean;
  /** Why the last start, stop or Continue was refused, in Dispatch's words. */
  readonly refusal: string | undefined;
}

const INITIAL: GoingState = {
  clicked: undefined,
  conversation: undefined,
  draft: undefined,
  level: "standard",
  onPage: new Map(),
  place: "closed",
  refusal: undefined,
  runs: new Map(),
  starting: false,
};

let state: GoingState = INITIAL;
const listeners = new Set<() => void>();
/** The streams being read, by run. */
const following = new Map<string, AbortController>();

function set(next: GoingState): void {
  state = next;
  for (const listener of listeners) {
    listener();
  }
}

function withRun(id: string, change: (run: RunEntry) => RunEntry): void {
  const run = state.runs.get(id);
  if (run === undefined) {
    return;
  }
  set({ ...state, runs: new Map(state.runs).set(id, change(run)) });
}

function bounds(login: string | undefined): WriteBounds {
  return {
    deadlineMs: REQUEST_DEADLINE_MS,
    key: newSendKey(),
    ...(login === undefined ? {} : { login }),
  };
}

/** The store as it stands now. */
export function goingState(): GoingState {
  return state;
}

/** The store, read by a component and drawn again on each change. */
export function useGoing(): GoingState {
  return useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => state,
    () => state
  );
}

/** A run's state as the page shows it: what its frames say once any came, else what its record
 *  said. */
export function shownState(run: RunEntry): RunState {
  return run.read > 0 || run.record === undefined
    ? run.frames.state
    : stateOfRecord(run.record.state, run.record.stop_reason);
}

/** The open conversation's newest run. */
export function newestRun(of: GoingState = state): RunEntry | undefined {
  const id = of.conversation?.at(-1);
  return id === undefined ? undefined : of.runs.get(id);
}

/** Whether the open conversation's newest run works now: what Stop, and Esc in the empty box,
 *  stop. */
export function conversationWorking(of: GoingState = state): boolean {
  const run = newestRun(of);
  return run !== undefined && shownState(run) === "working";
}

/** Reads the run's stream until it ends, folding each frame into the run. */
function follow(id: string): void {
  if (following.has(id)) {
    return;
  }
  const controller = new AbortController();
  following.set(id, controller);
  api
    .streamGoingRunEvents(
      id,
      (name, data) => {
        withRun(id, (run) => ({
          ...run,
          frames: withRunFrame(run.read === 0 ? STARTED_RUN : run.frames, name, data),
          read: run.read + 1,
        }));
      },
      controller.signal
    )
    .catch((error: unknown) => {
      if (controller.signal.aborted) {
        return;
      }
      withRun(id, (run) => ({
        ...run,
        unreadable: apiErrorMessage(error, "The run's steps could not be read."),
      }));
    })
    .finally(() => {
      if (following.get(id) === controller) {
        following.delete(id);
      }
    });
}

/** Moves the conversation as the person's key, press or move asks (`panelNext`), and stops the
 *  run where that says to. */
export function movePanel(event: PanelEvent, login: string | undefined, keys?: PanelKeys): void {
  const transition = panelNext(state.place, event, keys);
  if (transition.place !== state.place) {
    set({ ...state, place: transition.place });
  }
  const run = newestRun();
  if (transition.stopRun === true && run !== undefined) {
    stopRun(run.id, login);
  }
}

/** Puts the conversation where `place` says, as a chip's Open or History's Open does. */
export function setPlace(place: PanelPlace): void {
  if (place !== state.place) {
    set({ ...state, place });
  }
}

/** Sets the level the next run takes. */
export function setLevel(level: RunLevel): void {
  set({ ...state, level });
}

/** Names the item a click on the page was on, which the next request at the side is about. */
export function setClicked(context: GoingContextInput, path: string): void {
  set({ ...state, clicked: { context, path } });
}

/** Closes the open conversation, so the next request starts another. Its runs are still
 *  followed, and History lists it. */
export function newConversation(): void {
  set({ ...state, conversation: undefined, draft: undefined, refusal: undefined });
}

function entryOf(record: GoingRun, startedHere: boolean): RunEntry {
  return {
    conversation: record.conversation,
    frames: STARTED_RUN,
    id: record.id,
    read: 0,
    record,
    seen: !startedHere,
    startedHere,
    unreadable: undefined,
    words: record.words,
  };
}

/**
 * Starts a run of `words` with the page's `context`. It follows up the open conversation's newest
 * run where that run has ended (`POST /api/v1/going/runs` with `continues`), and starts a
 * conversation of its own otherwise: a run that still works or waits goes on beside it.
 */
export async function startRun(
  words: string,
  context: GoingContextInput,
  login: string | undefined,
  continues?: string
): Promise<void> {
  const newest = newestRun();
  const follows =
    continues ?? (newest !== undefined && runEnded(shownState(newest)) ? newest.id : undefined);
  set({ ...state, refusal: undefined, starting: true });
  try {
    const run = await api.createGoingRun(
      {
        context,
        level: state.level,
        words,
        ...(follows === undefined ? {} : { continues: follows }),
      },
      bounds(login)
    );
    const entry = entryOf(run, true);
    const conversation =
      follows !== undefined && state.conversation !== undefined ? state.conversation : [];
    set({
      ...state,
      conversation: [...conversation, run.id],
      draft: undefined,
      runs: new Map(state.runs).set(run.id, entry),
      starting: false,
    });
    follow(run.id);
  } catch (error) {
    set({
      ...state,
      refusal: apiErrorMessage(error, "The run did not start."),
      starting: false,
    });
  }
}

/** Stops the run at once (`POST /api/v1/going/runs/{id}/stop`); its end frame says it stopped. */
export function stopRun(id: string, login: string | undefined): void {
  const run = state.runs.get(id);
  if (run === undefined || runNext(shownState(run), "stop").send !== "stop") {
    return;
  }
  api.stopGoingRun(id, bounds(login)).then(
    (stopped) => withRun(id, (entry) => ({ ...entry, record: stopped })),
    (error: unknown) => set({ ...state, refusal: apiErrorMessage(error, "The run did not stop.") })
  );
}

/** Continue on a run a bound of its level stopped: a follow-up of its conversation with a new
 *  budget of the same level. */
export function continueRun(id: string, context: GoingContextInput, login: string | undefined) {
  const run = state.runs.get(id);
  if (run === undefined || runNext(shownState(run), "continue").send !== "continue") {
    return Promise.resolve();
  }
  return startRun("Continue", context, login, id);
}

/** Opens a conversation History lists: its runs, oldest first, each read again from its stream's
 *  first frame, and the conversation shown over the page where nothing shows it yet. */
export function openConversation(conversation: GoingConversation): void {
  const records = [...(conversation.runs ?? [])].reverse();
  const runs = new Map(state.runs);
  for (const record of records) {
    const known = runs.get(record.id);
    runs.set(record.id, known === undefined ? entryOf(record, false) : { ...known, record });
  }
  set({
    ...state,
    conversation: records.map((record) => record.id),
    draft: undefined,
    place: state.place === "closed" ? "over" : state.place,
    refusal: undefined,
    runs,
  });
  for (const record of records) {
    follow(record.id);
  }
}

/** Opens the conversation of a run the page already follows (the chip's Open). */
export function openRun(id: string): void {
  const run = state.runs.get(id);
  if (run === undefined) {
    return;
  }
  const conversation = [...state.runs.values()]
    .filter((entry) => entry.conversation === run.conversation)
    .map((entry) => entry.id);
  set({
    ...state,
    conversation,
    place: state.place === "closed" ? "over" : state.place,
  });
}

/** Marks that the person has seen how each ended run of the open conversation ended, so the chip
 *  no longer names it. A run that works or waits is not marked: the chip names it again once the
 *  conversation leaves the screen. */
export function markSeen(): void {
  const ids = (state.conversation ?? []).filter((id) => {
    const run = state.runs.get(id);
    return run !== undefined && !run.seen && runEnded(shownState(run));
  });
  if (ids.length === 0) {
    return;
  }
  const runs = new Map(state.runs);
  for (const id of ids) {
    const run = runs.get(id);
    if (run !== undefined) runs.set(id, { ...run, seen: true });
  }
  set({ ...state, runs });
}

/** The plan rows the page surface `surface` draws, or none once it no longer draws any. */
export function drawOnPage(surface: string, rows: readonly number[]): void {
  const now = state.onPage.get(surface);
  if (rows.length === 0 && now === undefined) {
    return;
  }
  if (now !== undefined && now.length === rows.length && now.every((row, at) => row === rows[at])) {
    return;
  }
  const onPage = new Map(state.onPage);
  if (rows.length === 0) onPage.delete(surface);
  else onPage.set(surface, rows);
  set({ ...state, onPage });
}

/** The draft of plan `plan` of `run`: the one held, or an empty one where it is of another plan. */
export function draftOf(run: string, plan: number, of: GoingState = state): PlanDraft {
  return of.draft !== undefined && of.draft.run === run && of.draft.plan === plan
    ? of.draft
    : { edits: new Map(), plan, run, unticked: new Set() };
}

/** Holds the person's answer to the plan so far. */
export function setDraft(draft: PlanDraft): void {
  set({ ...state, draft });
}

/** Clears the store and stops every stream it reads: a test's reset, and a sign-out's. */
export function resetGoingStore(): void {
  for (const controller of following.values()) {
    controller.abort();
  }
  following.clear();
  set(INITIAL);
}
