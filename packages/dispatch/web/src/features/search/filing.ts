import type { QueryClient } from "@tanstack/react-query";
import { useSyncExternalStore } from "react";

import { api, apiErrorMessage } from "../../api/client";
import { goingConversationsQuery } from "../../api/queries";
import type { GoingContextInput } from "../../api/types";
import { goingBounds } from "../going/going-store";
import { showNotice } from "../going/Notice";
import { STARTED_RUN, withRunFrame } from "../going/run-frames";
import { buildIssuePath } from "../refs/routes";

/**
 * Enter on the line "File as a new issue" (spec R9, design v3, state 13): a new-issue run of the
 * person's words (`POST /api/v1/going/runs` with `kind` `new_issue`, recording the line `file`),
 * which files them at once and waits for nobody. While it runs, the bar's first row reads "Filing
 * in DEMO…"; once its end names the issue it filed, the bar closes and the notice reads "Filed
 * DEMO-46 · Open · Undo" for `FILED_NOTICE_MS`, its Undo the run's own (R6). A run that ends
 * without an issue, or that never starts, stays in the bar as a failure in words, with Try again.
 *
 * The filing is held outside the bar, so it goes on, and its notice still comes, when the person
 * closes the bar before it ends. The run is a run of History like any other, so History lists it
 * with its Undo.
 */

/** How long the notice of a filed issue shows. */
export const FILED_NOTICE_MS = 10_000;

/** The filing the bar shows: out, or ended with nothing filed and why, in words. `project` is the
 *  project it files in, "" where the chip names none. */
export type Filing =
  | { readonly state: "filing"; readonly words: string; readonly project: string }
  | {
      readonly state: "failed";
      readonly words: string;
      readonly project: string;
      readonly reason: string;
    };

/** The filing the bar shows, and how many filings ended filed, which the open bar reads to close
 *  once its filing has. */
interface FilingStore {
  readonly filing: Filing | undefined;
  readonly filed: number;
}

let store: FilingStore = { filed: 0, filing: undefined };
const listeners = new Set<() => void>();

function set(next: FilingStore): void {
  store = next;
  for (const listener of listeners) {
    listener();
  }
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function readStore(): FilingStore {
  return store;
}

/** The filing store, read by a component and drawn again on each change. */
export function useFiling(): FilingStore {
  return useSyncExternalStore(subscribe, readStore, readStore);
}

/** Lets the filing the bar shows go: a failure the person typed past. */
export function dropFiling(): void {
  if (store.filing !== undefined) {
    set({ ...store, filing: undefined });
  }
}

/** Why a new-issue run filed nothing, from its end's reason: the words after the reason's code,
 *  which the person reads, never the code. */
function reasonWords(reason: string): string {
  const words = reason.replace(/^[A-Z][A-Z0-9_]*: */u, "").trim();
  return words === "" ? "Dispatch filed nothing." : words;
}

/**
 * Files `words` as one new issue, with the chip's `context`, in `project` (the chip's, for the
 * bar's words). It does nothing while another filing is out. The run's stream is read to its end,
 * whose `issue` names what was filed; History is read again once the run ends.
 */
export async function fileNewIssue({
  context,
  login,
  project,
  queryClient,
  words,
}: {
  readonly context: GoingContextInput;
  readonly login: string | undefined;
  readonly project: string;
  readonly queryClient: QueryClient;
  readonly words: string;
}): Promise<void> {
  if (store.filing?.state === "filing") {
    return;
  }
  const filing: Filing = { project, state: "filing", words };
  set({ ...store, filing });
  // Each outcome is this filing's only while the store still holds it.
  const settle = (next: FilingStore) => {
    if (store.filing === filing) {
      set(next);
    }
  };
  let frames = STARTED_RUN;
  try {
    const run = await api.createGoingRun(
      { context, kind: "new_issue", line: "file", words },
      goingBounds(login)
    );
    await api.streamGoingRunEvents(
      run.id,
      (name, data) => {
        frames = withRunFrame(frames, name, data);
      },
      new AbortController().signal
    );
    const end = frames.end;
    if (end?.state === "done" && end.issue !== "") {
      settle({ filed: store.filed + 1, filing: undefined });
      showNotice({
        actions: [{ label: "Open", to: buildIssuePath({ key: end.issue, kind: "issue" }) }],
        ms: FILED_NOTICE_MS,
        text: `Filed ${end.issue}`,
        undo: { run: run.id, undone: `Undone · ${end.issue} closed` },
      });
      return;
    }
    settle({
      ...store,
      filing: { ...filing, reason: reasonWords(end?.reason ?? ""), state: "failed" },
    });
  } catch (error) {
    settle({
      ...store,
      filing: {
        ...filing,
        reason: apiErrorMessage(error, "Dispatch could not be reached."),
        state: "failed",
      },
    });
  } finally {
    void queryClient.invalidateQueries({ queryKey: goingConversationsQuery().queryKey });
  }
}
