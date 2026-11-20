import { afterEach, beforeEach, expect, jest, type Mock, spyOn, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";

import { realClock, tick } from "../../__tests__/fake-clock";
import { answerRoute } from "../../__tests__/send-answers";
import { userOnFakeClock } from "../../__tests__/user";
import { ApiError, api, type WriteBounds } from "../../api/client";
import type {
  GoingDecisionEvent,
  GoingEvent,
  GoingPlanAnswer,
  GoingProposalEvent,
} from "../../api/types";
import {
  type GoingBarSpies,
  BAR_HIT as hit,
  type OpenedStream,
  renderBar,
  spyOnGoingBar,
} from "./going-bar-fixture";
import { forgetTurns } from "./going-turns";
import { OPERATION_OPTION_ID, offerOptionId } from "./OperationRows";
import { optionId } from "./search-model";
import { forgetAnsweredRun } from "./useGoingAnswer";
import { KEYSTROKE_DEBOUNCE_MS, MEANING_PAUSE_MS } from "./useTieredSearch";

// The bar's operations as the person meets them: the operation the decision acts on is a row of
// its own above the hits, which the highlight never moves to by itself, and the bar's submit shows
// the plan its run proposed, which changes nothing until the person approves it; one the person
// typed past goes with its run, though the panel keeps that run's answer; a submit before the
// proposal prepares and shows the plan as it lands; below the threshold the two best are offered
// and only a pick shows the plan. The clock is held, as in AnswerPanel.test.tsx.

let streams: OpenedStream[];
let spies: GoingBarSpies;
let plans: Mock<typeof api.goingPlan>;
type ApproveAnswer = (
  run: string,
  input: { readonly plan: number },
  bounds: WriteBounds
) => Promise<GoingPlanAnswer>;
let approved: Mock<ApproveAnswer>;

/** The plan of `run` as Dispatch answers it: a status change of LEGION-3 and of LEGION-4, in
 *  `state`, each row waiting while the plan does and approved once it is. */
function planOf(run: string, state: GoingPlanAnswer["state"]): GoingPlanAnswer {
  return {
    mode: "ask",
    plan: 1,
    rows: ["LEGION-3", "LEGION-4"].map((issue, index) => ({
      arguments: { issue, status: "in_progress" },
      changed: false,
      claim: null,
      class: "field",
      fields: ["status"],
      issue,
      item: issue,
      needs: [],
      now: { status: "todo" },
      result: null,
      row: index + 1,
      state: state === "waiting" ? "waiting" : "approved",
      tool: "dispatch_issue_update",
      waits: state === "waiting" ? ["ask"] : [],
    })),
    run,
    state,
    summary: "Start LEGION-3 and LEGION-4.",
  };
}

beforeEach(() => {
  jest.useFakeTimers();
  spies = spyOnGoingBar();
  streams = spies.streams;
  plans = spyOn(api, "goingPlan").mockImplementation(async (run) => planOf(run, "waiting"));
  approved = answerRoute<ApproveAnswer>("POST", "/api/v1/going/runs/*/plan/approve");
  approved.mockImplementation(async (run) => planOf(run, "approved"));
});

afterEach(() => {
  spies.restore();
  plans.mockRestore();
  forgetAnsweredRun();
  forgetTurns();
  realClock();
});

/** Types `words`, waits for their hits, and lets the pause ask the question. */
async function ask(user: UserEvent, words: string): Promise<void> {
  const input = screen.getByRole<HTMLInputElement>("combobox", { name: "Search" });
  await user.click(input);
  await user.keyboard(words);
  await tick(KEYSTROKE_DEBOUNCE_MS);
  await waitFor(() => expect(screen.getAllByRole("option").length).toBeGreaterThan(0));
  await tick(MEANING_PAUSE_MS - KEYSTROKE_DEBOUNCE_MS);
  await waitFor(() => expect(streams).toHaveLength(1));
}

function decision(act: boolean, actions: readonly string[]): GoingDecisionEvent {
  return {
    data: {
      act,
      actions: actions.map((action, index) => ({
        action,
        confidence: index === 0 ? 0.9 : null,
        probability: null,
      })),
      context: "named",
      display: { chart: false, choice: "prose", confidence: 0.4 },
    },
    event: "decision",
  };
}

/** A status change of LEGION-3 and LEGION-4, and a comment on LEGION-3. */
const proposal: GoingProposalEvent = {
  data: {
    offers: [
      {
        action: "dispatch_issue_update",
        invocations: [
          {
            arguments: { issue: "LEGION-3", status: "in_progress" },
            automatic: true,
            missing: null,
            target_argument: "issue",
          },
          {
            arguments: { issue: "LEGION-4", status: "in_progress" },
            automatic: true,
            missing: null,
            target_argument: "issue",
          },
        ],
        offered: true,
      },
      {
        action: "dispatch_comment",
        invocations: [
          {
            arguments: { body: "On it.", issue: "LEGION-3" },
            automatic: true,
            missing: null,
            target_argument: "issue",
          },
        ],
        offered: true,
      },
    ],
  },
  event: "proposal",
};

/** The update of LEGION-4 alone, which only an item's stored text asked for: a submit runs none
 *  of it, and the person picks it to run it. */
const storedOnly: GoingProposalEvent = {
  data: {
    offers: [
      {
        action: "dispatch_issue_update",
        invocations: [
          {
            arguments: { issue: "LEGION-4", status: "done" },
            automatic: false,
            missing: null,
            target_argument: "issue",
          },
        ],
        offered: true,
      },
    ],
  },
  event: "proposal",
};

const ended: GoingEvent = { data: { items: 0, words: 3 }, event: "end" };

/** Hands the run's stream `frames`, in order, as the server writes them. */
function stream(...frames: GoingEvent[]): void {
  act(() => {
    for (const frame of frames) streams[0]?.send(frame);
  });
}

/** The bar's submit, from the box. */
async function submit(user: UserEvent): Promise<void> {
  await user.keyboard("{Control>}{Enter}{/Control}");
}

test("at or above the threshold the operation is a row above the hits the highlight never takes, and the submit shows the run's plan, which changes nothing until the person approves it", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update", "dispatch_comment"]), proposal, ended);
    const row = await screen.findByRole("option", {
      name: /^Run issue update on LEGION-3 and LEGION-4/,
    });
    expect(row).toHaveAttribute("id", OPERATION_OPTION_ID);
    expect(within(row).getByRole("button", { name: "Run" })).toBeInTheDocument();
    // The hit stays the head: an Enter now opens it and runs nothing.
    expect(screen.getByRole("combobox", { name: "Search" })).toHaveAttribute(
      "aria-activedescendant",
      optionId(hit)
    );
    expect(plans).not.toHaveBeenCalled();

    await submit(user);

    const plan = await screen.findByTestId("going-plan");
    expect(plans).toHaveBeenCalledWith("run-1");
    expect(plan).toHaveAttribute("data-state", "waiting");
    expect(
      within(plan)
        .getAllByTestId("going-plan-row")
        .map((line) => line.textContent)
    ).toEqual(["issue update LEGION-3: waiting", "issue update LEGION-4: waiting"]);
    await tick(1000);
    expect(approved).not.toHaveBeenCalled();

    act(() => within(plan).getByRole("button", { name: "Approve" }).click());

    await tick(1000);
    expect(approved).toHaveBeenCalledTimes(1);
    expect(approved.mock.calls[0]?.slice(0, 2)).toEqual(["run-1", { plan: 1 }]);
    expect(screen.getByTestId("going-plan")).toHaveAttribute("data-state", "approved");
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  } finally {
    bar.unmount();
  }
});

test("a run that proposed no plan says so, and offers nothing to approve", async () => {
  plans.mockRejectedValue(
    new ApiError(404, { code: "GOING_PLAN_NOT_FOUND", error: "this run has proposed no plan" })
  );
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
    await screen.findByRole("option", { name: /^Run issue update/ });

    await submit(user);

    expect(await screen.findByTestId("going-plan-none")).toHaveTextContent(
      "This run proposed no plan, so nothing runs."
    );
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
    expect(approved).not.toHaveBeenCalled();
  } finally {
    bar.unmount();
  }
});

test("an approval Dispatch refuses says why, and the plan still waits", async () => {
  approved.mockRejectedValue(
    new ApiError(409, {
      code: "GOING_PLAN_NOT_WAITING",
      error: "the plan was answered meanwhile, and no longer waits",
    })
  );
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
    await screen.findByRole("option", { name: /^Run issue update/ });
    await submit(user);
    const plan = await screen.findByTestId("going-plan");

    act(() => within(plan).getByRole("button", { name: "Approve" }).click());

    expect(await screen.findByTestId("going-plan-refused")).toHaveTextContent(
      "the plan was answered meanwhile, and no longer waits"
    );
    expect(screen.getByTestId("going-plan")).toHaveAttribute("data-state", "waiting");
  } finally {
    bar.unmount();
  }
});

test("an operation the person typed past goes with its run while the panel keeps its answer, and the submit shows nothing of it", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(
      decision(true, ["dispatch_issue_update"]),
      { data: { text: "On it." }, event: "text" },
      proposal,
      ended
    );
    await screen.findByRole("option", { name: /^Run issue update/ });

    await user.keyboard("s");

    // The panel keeps the answer, marked as the earlier words'; the operation is not kept.
    expect(await screen.findByTestId("going-answer-stale")).toHaveTextContent("astrolabe");
    expect(screen.queryByRole("option", { name: /^Run issue update/ })).not.toBeInTheDocument();
    await submit(user);
    await tick(1000);
    expect(plans).not.toHaveBeenCalled();
    expect(screen.queryByTestId("going-plan")).not.toBeInTheDocument();
  } finally {
    bar.unmount();
  }
});

test("a submit before the proposal says it is preparing and shows the plan once the proposal lands", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]));
    await screen.findByRole("option", { name: /^Run issue update/ });

    await submit(user);

    expect(await screen.findByTestId("going-operation-preparing")).toBeInTheDocument();
    expect(plans).not.toHaveBeenCalled();
    stream(proposal);
    expect(await screen.findByTestId("going-plan")).toBeInTheDocument();
    expect(plans).toHaveBeenCalledWith("run-1");
    expect(screen.queryByTestId("going-operation-preparing")).not.toBeInTheDocument();
  } finally {
    bar.unmount();
  }
});

test("an upload the proposal withheld runs nothing, and the bar says why in Dispatch's words", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  const reason =
    'dispatch_artifact under "notes" is not offered: issue LEGION-3 already holds a document of that name.';
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_artifact"]));
    await screen.findByRole("option", { name: /^Run artifact/ });

    await submit(user);
    stream(
      {
        data: {
          offers: [
            { action: "dispatch_artifact", invocations: [], offered: false, withheld: [reason] },
          ],
        },
        event: "proposal",
      },
      ended
    );

    expect(await screen.findByTestId("going-operation-unoffered")).toHaveTextContent(
      `Nothing to run: ${reason}`
    );
    expect(plans).not.toHaveBeenCalled();
    expect(screen.queryByRole("option", { name: /^Run artifact/ })).not.toBeInTheDocument();
  } finally {
    bar.unmount();
  }
});

test("an upload the proposal withheld beside one it offered says why, before the submit and after it", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  const reason =
    'dispatch_artifact under "spec.md" is not offered: issue LEGION-3 already holds spec.md (slug spec-md).';
  try {
    await ask(user, "astrolabe");
    stream(
      decision(true, ["dispatch_artifact"]),
      {
        data: {
          offers: [
            {
              action: "dispatch_artifact",
              invocations: [
                {
                  arguments: { content: "# Extra\n", issue: "LEGION-3", name: "extra.md" },
                  automatic: true,
                  missing: null,
                  target_argument: "issue",
                },
              ],
              offered: true,
              withheld: [reason],
            },
          ],
        },
        event: "proposal",
      },
      ended
    );
    await screen.findByRole("option", { name: /^Run artifact on LEGION-3/ });
    expect(await screen.findByTestId("going-operation-withheld")).toHaveTextContent(reason);

    await submit(user);

    await screen.findByTestId("going-plan");
    // The plan shows; the upload withheld still says why it is not among the rows.
    expect(screen.getByTestId("going-operation-withheld")).toHaveTextContent(reason);
  } finally {
    bar.unmount();
  }
});

test("below the threshold the two best are offered, a submit shows neither's plan, and a pick shows it", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(false, ["dispatch_issue_update", "dispatch_comment"]), proposal, ended);
    const comment = await screen.findByRole("option", { name: /^Run comment on LEGION-3/ });
    expect(comment).toHaveAttribute("id", offerOptionId("dispatch_comment"));
    expect(
      screen.getByRole("option", { name: /^Run issue update on LEGION-3 and LEGION-4/ })
    ).toHaveAttribute("id", offerOptionId("dispatch_issue_update"));

    await submit(user);
    await tick(1000);
    expect(plans).not.toHaveBeenCalled();

    await user.click(comment);
    expect(await screen.findByTestId("going-plan")).toBeInTheDocument();
    expect(plans).toHaveBeenCalledWith("run-1");
  } finally {
    bar.unmount();
  }
});

test("a submit whose every invocation only stored text asked for shows no plan, and the person picks it to see the plan", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), storedOnly, ended);
    const pick = await screen.findByRole("option", { name: /^Run issue update on LEGION-4/ });
    expect(pick).toHaveAttribute("id", offerOptionId("dispatch_issue_update"));

    await submit(user);

    expect(await screen.findByTestId("going-operation-picks")).toBeInTheDocument();
    expect(plans).not.toHaveBeenCalled();
    await user.click(pick);
    expect(await screen.findByTestId("going-plan")).toBeInTheDocument();
  } finally {
    bar.unmount();
  }
});

/** Picks DEMO on the bar's project chip, which asks the same words of that project: another
 *  run, whose stream - the next one the bar opens - is handed `frames` once it opens. */
async function askOfAnotherProject(user: UserEvent, ...frames: GoingEvent[]): Promise<void> {
  const opened = streams.length;
  await user.click(screen.getByRole("button", { name: "Project" }));
  await user.click(await screen.findByRole("button", { name: "DEMO" }));
  await tick(MEANING_PAUSE_MS);
  await waitFor(() => expect(streams).toHaveLength(opened + 1));
  act(() => {
    for (const frame of frames) streams[opened]?.send(frame);
  });
}

/** The one project the bar's project chip offers. */
function spyOnProjects(): Mock<typeof api.listProjects> {
  return spyOn(api, "listProjects").mockResolvedValue([
    {
      created_at: "2026-09-10T00:00:00Z",
      gated_steps: [],
      key: "DEMO",
      name: "Demo",
      open_asks: 0,
    },
  ]);
}

/** A step of a press of the run the bar asked first (`press` drives the bar to it, `step` is the
 *  test id of what it shows), and what the run a chip change makes current next streams (`next`)
 *  and draws once its proposal landed (`row`). */
interface PressOfAnEarlierRun {
  readonly next: readonly GoingEvent[];
  readonly press: (user: UserEvent) => Promise<void>;
  readonly row: RegExp;
  readonly step: string;
}

// Every stage of a press is of the run the person pressed on. A chip change with the same words
// makes another run current, whose decision acts: the stage closes, and nothing of the press is
// shown for, or approved on, that run. A submit waiting for the decision; one made inside the
// pause whose request Dispatch has still not answered when the chip changes, so it is of no run;
// the plan a submit showed; and the notes that the answer offered nothing to run, or that its
// items are the person's to pick.
const pressesOfAnEarlierRun: Readonly<Record<string, PressOfAnEarlierRun>> = {
  "a submit waiting for the decision": {
    next: [decision(true, ["dispatch_issue_update"]), proposal, ended],
    press: async (user) => {
      await ask(user, "astrolabe");
      await submit(user);
    },
    row: /^Run issue update/,
    step: "going-operation-preparing",
  },
  "a submit made before its run's id was known": {
    next: [decision(true, ["dispatch_issue_update"]), proposal, ended],
    press: async (user) => {
      await user.click(screen.getByRole("combobox", { name: "Search" }));
      await user.keyboard("astrolabe");
      await tick(KEYSTROKE_DEBOUNCE_MS);
      await waitFor(() => expect(screen.getAllByRole("option").length).toBeGreaterThan(0));
      // Inside the pause nothing is asked yet: the submit asks at once, and Dispatch has not
      // answered that request, so not said the id of its run, when the chip changes.
      expect(spies.createGoingRequest).not.toHaveBeenCalled();
      spies.createGoingRequest.mockReturnValueOnce(new Promise<never>(() => undefined));
      await submit(user);
      await waitFor(() => expect(spies.createGoingRequest).toHaveBeenCalledTimes(1));
    },
    row: /^Run issue update/,
    step: "going-operation-preparing",
  },
  "the plan a submit showed": {
    next: [decision(true, ["dispatch_issue_update"]), proposal, ended],
    press: async (user) => {
      await ask(user, "astrolabe");
      stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
      await screen.findByRole("option", { name: /^Run issue update/ });
      await submit(user);
    },
    row: /^Run issue update/,
    step: "going-plan",
  },
  "a note that the answer offered nothing to run": {
    next: [decision(true, ["dispatch_issue_update"]), proposal, ended],
    press: async (user) => {
      await ask(user, "astrolabe");
      stream(decision(true, ["dispatch_issue_update"]));
      await screen.findByRole("option", { name: /^Run issue update/ });
      await submit(user);
      stream(
        {
          data: { offers: [{ action: "dispatch_issue_update", invocations: [], offered: false }] },
          event: "proposal",
        },
        ended
      );
    },
    row: /^Run issue update on LEGION-3 and LEGION-4/,
    step: "going-operation-unoffered",
  },
  "a note that the items to run are the person's to pick": {
    next: [decision(true, ["dispatch_issue_update"]), proposal, ended],
    press: async (user) => {
      await ask(user, "astrolabe");
      stream(decision(true, ["dispatch_issue_update"]), storedOnly, ended);
      await screen.findByRole("option", { name: /^Run issue update on LEGION-4/ });
      await submit(user);
    },
    row: /^Run issue update on LEGION-3 and LEGION-4/,
    step: "going-operation-picks",
  },
};

test.each(
  Object.entries(pressesOfAnEarlierRun)
)("every step of a press closes and shows nothing for another run once a chip change makes another run of the same words current: %s", async (_name, press) => {
  const projects = spyOnProjects();
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await press.press(user);
    await screen.findByTestId(press.step);

    await askOfAnotherProject(user, ...press.next);

    await screen.findByRole("option", { name: press.row });
    expect(screen.queryByTestId(press.step)).not.toBeInTheDocument();
    await tick(1000);
    expect(screen.queryByTestId("going-plan")).not.toBeInTheDocument();
    expect(plans.mock.calls.map(([run]) => run)).not.toContain("run-2");
    expect(approved).not.toHaveBeenCalled();
  } finally {
    bar.unmount();
    projects.mockRestore();
  }
});

// A submit inside the pause asks at once, before Dispatch has said the id of the run it asked: the
// press is of that run once its id arrives, and shows that run's plan when its proposal lands.
test("a submit inside the pause shows the plan of the run it asked, once that run's proposal lands", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await user.click(screen.getByRole("combobox", { name: "Search" }));
    await user.keyboard("astrolabe");
    await tick(KEYSTROKE_DEBOUNCE_MS);
    await waitFor(() => expect(screen.getAllByRole("option").length).toBeGreaterThan(0));
    expect(spies.createGoingRequest).not.toHaveBeenCalled();

    await submit(user);
    await waitFor(() => expect(streams).toHaveLength(1));
    await screen.findByTestId("going-operation-preparing");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);

    expect(await screen.findByTestId("going-plan")).toBeInTheDocument();
    expect(plans.mock.calls.map(([run]) => run)).toEqual(["run-1"]);
  } finally {
    bar.unmount();
  }
});
