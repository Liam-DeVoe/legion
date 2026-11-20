import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";

import {
  apiErrorMessage,
  goingPlanApproveRequest,
  goingPlanRejectRequest,
  goingPlanReviseRequest,
} from "../../api/client";
import { goingConversationsQuery, goingPlanQuery, goingWaitingPlansQuery } from "../../api/queries";
import type { GoingPlanAnswer } from "../../api/types";
import { useKeyedWrite } from "../writes/useKeyedWrite";
import { draftOf, goingState, newestRun, useGoing } from "./going-store";
import { approvalOf } from "./plan-rows";

/** What a surface answers a plan with, and how that stands. */
export interface PlanAnswering {
  /** The plan, once read; undefined while it is read, or for a run that proposed none. */
  readonly plan: GoingPlanAnswer | undefined;
  /** Approves the waiting plan with the person's ticks and edits (Enter, or Approve). */
  readonly approve: () => void;
  /** Rejects the waiting plan. */
  readonly reject: () => void;
  /** Says in words what to change; the run proposes another plan in its place. */
  readonly revise: (words: string) => void;
  readonly pending: boolean;
  /** Why Dispatch refused the last answer, in its words. */
  readonly refusal: string | undefined;
}

/**
 * The latest plan of run `run` (`GET /api/v1/going/runs/{id}/plan`), read once the run's stream
 * has carried a plan frame and again at each one after (`planFrames`), and the person's three
 * answers to it: approve, with the ticks and edits the store holds for it, reject, and revise in
 * words. Each answer is the person's write, under the plan's own target, so two surfaces that
 * answer one plan go one at a time.
 */
export function usePlan(run: string | undefined, planFrames: number): PlanAnswering {
  const queryClient = useQueryClient();
  const key = goingPlanQuery(run ?? "").queryKey;
  const plan = useQuery({
    ...goingPlanQuery(run ?? ""),
    enabled: run !== undefined && planFrames > 0,
    retry: false,
  });
  // biome-ignore lint/correctness/useExhaustiveDependencies: each new plan frame is the reason to read the plan again
  useEffect(() => {
    if (run !== undefined && planFrames > 1) {
      void queryClient.invalidateQueries({ exact: true, queryKey: key });
    }
  }, [planFrames]);
  const [refusal, setRefusal] = useState<string>();
  const settle = (answered: GoingPlanAnswer | null) => {
    if (answered !== null) {
      queryClient.setQueryData(key, answered);
    } else {
      void queryClient.invalidateQueries({ exact: true, queryKey: key });
    }
    void queryClient.invalidateQueries({ queryKey: goingWaitingPlansQuery().queryKey });
    void queryClient.invalidateQueries({ queryKey: goingConversationsQuery().queryKey });
  };
  const common = {
    inline: true,
    onError: (error: unknown) =>
      setRefusal(apiErrorMessage(error, "The answer to the plan did not reach Dispatch.")),
    onSuccess: settle,
    target: () => `going-plan:${run ?? ""}`,
  };
  const approve = useKeyedWrite<GoingPlanAnswer, GoingPlanAnswer>({
    ...common,
    label: () => "Approve the command bar's plan",
    request: (answered) =>
      goingPlanApproveRequest(
        answered.run,
        approvalOf(answered, draftOf(answered.run, answered.plan, goingState()))
      ),
  });
  const reject = useKeyedWrite<GoingPlanAnswer, GoingPlanAnswer>({
    ...common,
    label: () => "Reject the command bar's plan",
    request: (answered) => goingPlanRejectRequest(answered.run, { plan: answered.plan }),
  });
  const revise = useKeyedWrite<GoingPlanAnswer, { plan: GoingPlanAnswer; words: string }>({
    ...common,
    label: () => "Revise the command bar's plan",
    request: ({ plan: answered, words }) =>
      goingPlanReviseRequest(answered.run, { plan: answered.plan, words }),
  });
  const shown = plan.data;
  const waiting = shown?.state === "waiting" ? shown : undefined;
  return {
    approve: () => {
      if (waiting === undefined) return;
      setRefusal(undefined);
      approve.mutate(waiting);
    },
    pending: approve.isPending || reject.isPending || revise.isPending,
    plan: shown,
    refusal,
    reject: () => {
      if (waiting === undefined) return;
      setRefusal(undefined);
      reject.mutate(waiting);
    },
    revise: (words) => {
      if (waiting === undefined || words.trim() === "") return;
      setRefusal(undefined);
      revise.mutate({ plan: waiting, words: words.trim() });
    },
  };
}

/** The plan of the conversation open in the bar or in the side panel: its newest run's, while the
 *  conversation is on the screen. A plan that waits in the Inbox draws on no page until its
 *  conversation is opened. */
export function useOpenPlan(): PlanAnswering & { readonly run: string | undefined } {
  const going = useGoing();
  const run = going.place === "closed" ? undefined : newestRun(going);
  const answering = usePlan(run?.id, run?.frames.planFrames ?? 0);
  return { ...answering, run: run?.id };
}
