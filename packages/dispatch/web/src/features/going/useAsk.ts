import { useQuery } from "@tanstack/react-query";

import { whoAmIQuery } from "../../api/queries";
import type { GoingContextInput } from "../../api/types";
import {
  continueRun,
  conversationWorking,
  goingState,
  movePanel,
  newestRun,
  startRun,
  stopRun,
} from "./going-store";
import type { PanelKeys } from "./panel-states";
import { type PlanAnswering, useOpenPlan } from "./usePlan";

/** What the bar's box and the panel's box do with the person's words and keys. */
export interface Asking {
  /** The open plan, as this surface answers it. */
  readonly answering: PlanAnswering;
  /** Whether the open conversation's newest run waits on its plan: Enter then answers it. */
  readonly planWaits: boolean;
  /** Enter with `words` in the box: approves the waiting plan from an empty box, revises it in
   *  words, or starts a run of the words; answers whether it did any. */
  readonly enter: (words: string) => boolean;
  /** Esc in the box: stops the run that works where the box is empty and answers true, else
   *  moves the conversation as Esc does (`panelNext`) and answers false. */
  readonly escape: (words: string, keys?: PanelKeys) => boolean;
  readonly stop: () => void;
  readonly onContinue: (run: string) => void;
}

/**
 * The one behaviour of the conversation's two boxes, the bar over the page and the panel at the
 * side, with the page's `context`: Enter approves a plan that waits (R4: Enter approves) or, with
 * words, says what to change of it; with no plan waiting, it starts a run of the words, a
 * follow-up of the conversation where its newest run has ended. Esc in the empty box while the run
 * works stops the run (R1, R10).
 */
export function useAsk(context: GoingContextInput): Asking {
  const login = useQuery(whoAmIQuery()).data?.login;
  const answering = useOpenPlan();
  const planWaits = answering.plan?.state === "waiting";
  return {
    answering,
    enter: (words) => {
      const said = words.trim();
      if (planWaits) {
        if (said === "") answering.approve();
        else answering.revise(said);
        return true;
      }
      if (said === "" || goingState().starting) {
        return false;
      }
      void startRun(said, context, login);
      return true;
    },
    escape: (words, keys) => {
      const working = conversationWorking();
      if (words === "") {
        movePanel({ kind: "esc_empty", working }, login, keys);
        return working;
      }
      movePanel({ kind: "esc_full" }, login, keys);
      return false;
    },
    onContinue: (run) => void continueRun(run, context, login),
    planWaits,
    stop: () => {
      const run = newestRun();
      if (run !== undefined) stopRun(run.id, login);
    },
  };
}
