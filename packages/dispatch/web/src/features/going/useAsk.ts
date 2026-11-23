import { useQuery, useQueryClient } from "@tanstack/react-query";

import { goingConversationsQuery, whoAmIQuery } from "../../api/queries";
import type { GoingContextInput, GoingLine, GoingRun } from "../../api/types";
import {
  continueRun,
  conversationWorking,
  goingState,
  movePanel,
  newestRun,
  shownState,
  startRun,
  stopRun,
  useGoingValue,
} from "./going-store";
import { type PlanAnswering, useOpenPlan } from "./usePlan";

/** What the bar's box and the panel's box do with the person's words and keys. */
export interface Asking {
  /** The open plan, as this surface answers it. */
  readonly answering: PlanAnswering;
  /** Whether the open conversation's newest run waits on its plan: Enter then answers it. */
  readonly planWaits: boolean;
  /** Whether the open conversation's newest run waits on its plan as its frames show it, which
   *  can come before the plan is read: until then Enter answers nothing and starts no run. */
  readonly runWaits: boolean;
  /** Enter with `words` in the box: approves the waiting plan from an empty box, revises it in
   *  words, continues a run a bound of its level stopped from an empty box (design v2, state 9),
   *  or starts a run of the words, which records `line`, the line on top Enter was pressed on,
   *  where the words were sent from it; answers whether it did any. */
  readonly enter: (words: string, line?: GoingLine) => boolean;
  /** Esc in the box: stops the run that works where the box is empty, else moves the
   *  conversation as Esc does (`panelNext`). */
  readonly escape: (words: string) => void;
  readonly stop: () => void;
  /** Continue on a run a bound stopped: a follow-up of `level`, else of the run's own level. */
  readonly onContinue: (run: string, level?: GoingRun["level"]) => void;
  /** Retry under an answer: a new run of the same `words`, started as Enter starts one, a
   *  follow-up of the conversation where its newest run has ended. */
  readonly retry: (words: string) => void;
}

/**
 * The one behaviour of the conversation's two boxes, the bar over the page and the panel at the
 * side, with the page's `context`: Enter approves a plan that waits (R4: Enter approves) or, with
 * words, says what to change of it; with no plan waiting, it starts a run of the words, a
 * follow-up of the conversation where its newest run has ended, and in the empty box it continues
 * a run a bound of its level stopped (R10, Continue ↵). Esc in the empty box while the run works
 * stops the run (R1, R10). Each run started here is a run History lists, so History is read
 * again once it starts.
 */
export function useAsk(context: GoingContextInput): Asking {
  const queryClient = useQueryClient();
  const login = useQuery(whoAmIQuery()).data?.login;
  const answering = useOpenPlan();
  const planWaits = answering.plan?.state === "waiting";
  const runWaits = useGoingValue((of) => {
    const run = of.place === "closed" ? undefined : newestRun(of);
    return run !== undefined && shownState(run) === "waiting";
  });
  const listed = (start: Promise<boolean>) =>
    void start.then((started) => {
      if (started) {
        void queryClient.invalidateQueries({ queryKey: goingConversationsQuery().queryKey });
      }
    });
  return {
    answering,
    enter: (words, line) => {
      const said = words.trim();
      if (planWaits) {
        if (said === "") answering.approve();
        else answering.revise(said);
        return true;
      }
      if (runWaits || goingState().starting) {
        return false;
      }
      const newest = newestRun();
      if (said === "") {
        if (newest === undefined || shownState(newest) !== "stopped_bound") return false;
        listed(continueRun(newest.id, context, login));
        return true;
      }
      listed(startRun(said, context, login, { line }));
      return true;
    },
    escape: (words) => {
      movePanel(
        words === "" ? { kind: "esc_empty", working: conversationWorking() } : { kind: "esc_full" },
        login
      );
    },
    onContinue: (run, level) => listed(continueRun(run, context, login, level)),
    planWaits,
    retry: (words) => listed(startRun(words, context, login)),
    runWaits,
    stop: () => {
      const run = newestRun();
      if (run !== undefined) stopRun(run.id, login);
    },
  };
}
