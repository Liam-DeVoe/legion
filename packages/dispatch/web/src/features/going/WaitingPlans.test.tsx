import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";

import { renderWithQueries } from "../../__tests__/query-provider";
import { answerRoute } from "../../__tests__/send-answers";
import { ApiError, api } from "../../api/client";
import { goingWaitingPlansQuery } from "../../api/queries";
import type { GoingPlanAnswer, GoingWaitingPlan } from "../../api/types";
import { WaitingPlans } from "./WaitingPlans";

// The plans that wait for the person at the head of the Inbox (spec R11): Dispatch's refusal of an
// approval is said under the plan it refused, and only there, so a row whose run proposed another
// plan since shows none of it.

const waiting: GoingWaitingPlan = {
  conversation: "run-1",
  created_at: new Date().toISOString(),
  plan: 1,
  run: "run-1",
  summary: "Split CORE-1 in two.",
  waiting: 3,
  words: "split CORE-1 in two",
};

afterEach(cleanup);

test("a refusal of a run's plan does not show under the plan the run proposes in its place", async () => {
  const user = userEvent.setup();
  let listed: GoingWaitingPlan = waiting;
  const list = spyOn(api, "myGoingPlans").mockImplementation(async () => ({
    limit: 50,
    offset: 0,
    rows: [listed],
    total: 1,
  }));
  const approve = answerRoute<() => Promise<GoingPlanAnswer>>(
    "POST",
    "/api/v1/going/runs/*/plan/approve"
  );
  approve.mockImplementation(async () => {
    throw new ApiError(409, {
      code: "GOING_PLAN_NOT_WAITING",
      error: "the plan was answered meanwhile, and no longer waits",
    });
  });
  // The page's own client, so the test reads the list again as a going_plan.updated reads it.
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  try {
    renderWithQueries(<WaitingPlans />, { wrapper });
    await user.click(await screen.findByTestId("inbox-going-plan-approve"));
    const row = screen.getByTestId("inbox-going-plan");
    expect(await within(row).findByRole("alert")).toHaveTextContent(
      "the plan was answered meanwhile, and no longer waits"
    );

    // The run proposes plan 2, and the list is read again: the row is the same run's, with
    // its new plan.
    listed = { ...waiting, plan: 2, summary: "Split CORE-1 in three." };
    await act(() => client.invalidateQueries({ queryKey: goingWaitingPlansQuery().queryKey }));
    await waitFor(() =>
      expect(screen.getByTestId("inbox-going-plan")).toHaveTextContent("Split CORE-1 in three.")
    );
    expect(within(screen.getByTestId("inbox-going-plan")).queryByRole("alert")).toBeNull();
  } finally {
    list.mockRestore();
  }
});
