import { afterEach, beforeEach, expect, jest, type Mock, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { realClock, tick } from "../../__tests__/fake-clock";
import { answerRoute } from "../../__tests__/send-answers";
import { userOnFakeClock } from "../../__tests__/user";
import { ApiError, api, type WriteBounds } from "../../api/client";
import type {
  ArtifactUploadResponse,
  GoingDecisionEvent,
  GoingEvent,
  GoingProposalEvent,
  GoingUndoAnswer,
  GoingWriteInput,
  GoingWriteLine,
  IssueDetails,
} from "../../api/types";
import { writesOf } from "../writes/write-store";
import {
  type GoingBarSpies,
  BAR_HIT as hit,
  type OpenedStream,
  renderBar,
  spyOnGoingBar,
} from "./going-bar-fixture";
import { forgetTurns } from "./going-turns";
import { OPERATION_OPTION_ID, offerOptionId, useGoingOperations } from "./OperationRows";
import { useSendToAgentOffer } from "./SendToAgentOffer";
import { searchAnswer } from "./search-fixture";
import { optionId } from "./search-model";
import type { SendToAgentOutcome } from "./send-to-agent";
import { forgetAnsweredRun, type GoingAnswer } from "./useGoingAnswer";
import { KEYSTROKE_DEBOUNCE_MS, MEANING_PAUSE_MS } from "./useTieredSearch";

// The bar's operations as the person meets them: the operation the decision acts on is a row of
// its own above the hits, which the highlight never moves to by itself, and the bar's submit runs
// it; one the person typed past goes with its run, though the panel keeps that run's answer; a
// submit before the proposal prepares and runs as it lands; below the threshold the two best are
// offered and only a pick runs one; a missing argument is asked for; and with the person's
// setting on every write asks first. The clock is held, as in AnswerPanel.test.tsx.

let streams: OpenedStream[];
let settings: Mock<typeof api.myGoingSettings>;
let spies: GoingBarSpies;
type WriteAnswer = (run: string, input: GoingWriteInput, bounds: WriteBounds) => Promise<unknown>;
let written: Mock<WriteAnswer>;

/** Dispatch's line for a write of `input` that landed: a status change of the item it names. */
function landedLine(input: GoingWriteInput): GoingWriteLine {
  return {
    action: input.offer,
    before: { status: "todo" },
    fields: ["status"],
    item: `LEGION-${3 + (input.invocation ?? 0)}`,
    now: { status: "in_progress" },
    outcome: { code: "", message: "", ok: true, status: 200 },
    write_id: `write-${input.invocation ?? 0}`,
  };
}

beforeEach(() => {
  jest.useFakeTimers();
  spies = spyOnGoingBar();
  streams = spies.streams;
  settings = spyOn(api, "myGoingSettings").mockResolvedValue({ confirm_writes: false });
  written = answerRoute<WriteAnswer>("POST", "/api/v1/going/runs/*/writes");
  written.mockImplementation(async (_run, input) => landedLine(input));
});

afterEach(() => {
  spies.restore();
  settings.mockRestore();
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

const ended: GoingEvent = { data: { items: 0, words: 3 }, event: "end" };

/** What each write sent: the offer, the invocation and how it came to run. */
function sent(): unknown[] {
  return written.mock.calls.map(([, input]) => input);
}

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

test("at or above the threshold the operation is a row above the hits the highlight never takes, and the submit runs each invocation at once", async () => {
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
    expect(written).not.toHaveBeenCalled();

    await submit(user);

    await waitFor(() =>
      expect(sent()).toEqual([
        { approved: "auto", invocation: 0, offer: "dispatch_issue_update" },
        { approved: "auto", invocation: 1, offer: "dispatch_issue_update" },
      ])
    );
    const lines = await screen.findAllByTestId("going-write-line");
    await waitFor(() =>
      expect(lines.map((line) => line.getAttribute("data-state"))).toEqual(["done", "done"])
    );
    expect(lines[0]).toHaveTextContent("LEGION-3 · status: todo → in_progress");
    expect(lines[1]).toHaveTextContent("LEGION-4 · status: todo → in_progress");
    // The operation ran, so its row is gone and no second submit runs it again.
    expect(screen.queryByRole("option", { name: /^Run issue update/ })).not.toBeInTheDocument();
  } finally {
    bar.unmount();
  }
});

test("an operation the person typed past goes with its run while the panel keeps its answer, and the submit runs nothing of it", async () => {
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
    expect(written).not.toHaveBeenCalled();
  } finally {
    bar.unmount();
  }
});

test("once a turn's writes land the bar reads its hits again, so a hit shows what they left, and an undo that lands does the same", async () => {
  const user = userOnFakeClock();
  const undo = answerRoute<(run: string, input: unknown) => Promise<GoingUndoAnswer>>(
    "POST",
    "/api/v1/going/runs/*/undo"
  );
  undo.mockResolvedValue({
    lines: ["LEGION-3", "LEGION-4"].map((item, invocation) => ({
      action: "dispatch_issue_update",
      fields: [{ field: "status", message: "", ok: true }],
      item,
      now: { status: "todo" },
      outcome: { code: "", message: "", ok: true, status: 200 },
      undo: "write_back",
      write_id: `write-${invocation}`,
    })),
  });
  const hitHeader = () => document.getElementById(`search-group-issue:${hit.owner.key}`);
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
    await screen.findByRole("option", { name: /^Run issue update/ });
    expect(hitHeader()).toHaveAttribute("data-status", "todo");

    // Dispatch's search now reads the status the writes leave.
    spies.search.mockResolvedValue(
      searchAnswer([{ ...hit, owner: { ...hit.owner, status: "in_progress" } }])
    );
    await submit(user);
    await waitFor(() =>
      expect(
        screen.getAllByTestId("going-write-line").map((line) => line.getAttribute("data-state"))
      ).toEqual(["done", "done"])
    );
    await waitFor(() => expect(hitHeader()).toHaveAttribute("data-status", "in_progress"));

    // And the status the undo leaves, once it lands.
    spies.search.mockResolvedValue(searchAnswer([hit]));
    await user.click(screen.getByTestId("going-undo"));
    await waitFor(() => expect(undo).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(hitHeader()).toHaveAttribute("data-status", "todo"));
  } finally {
    bar.unmount();
  }
});

test("a submit pressed again while the turn's writes are out, and again once they landed, writes each invocation once", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
    await screen.findByRole("option", { name: /^Run issue update/ });
    const first = Promise.withResolvers<GoingWriteLine>();
    written.mockImplementation((_run, input) =>
      input.invocation === 0 ? first.promise : Promise.resolve(landedLine(input))
    );

    await submit(user);
    // The first invocation is out, the second waits behind it on the run's line.
    await waitFor(() => expect(written).toHaveBeenCalledTimes(1));
    await submit(user);
    first.resolve(landedLine({ approved: "auto", invocation: 0, offer: "dispatch_issue_update" }));
    const lines = await screen.findAllByTestId("going-write-line");
    await waitFor(() =>
      expect(lines.map((line) => line.getAttribute("data-state"))).toEqual(["done", "done"])
    );
    await submit(user);
    await tick(1000);

    expect(sent()).toEqual([
      { approved: "auto", invocation: 0, offer: "dispatch_issue_update" },
      { approved: "auto", invocation: 1, offer: "dispatch_issue_update" },
    ]);
    expect(screen.getAllByTestId("going-write-line")).toHaveLength(2);
  } finally {
    bar.unmount();
  }
});

test("a submit pressed again after Dispatch refused one invocation makes that one again, and only it", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
    await screen.findByRole("option", { name: /^Run issue update/ });
    let refuse = true;
    written.mockImplementation(async (_run, input) => {
      if (input.invocation === 1 && refuse) {
        refuse = false;
        throw new ApiError(409, { code: "ISSUE_CLAIMED", error: "LEGION-4 is claimed" });
      }
      return landedLine(input);
    });

    await submit(user);
    await waitFor(() =>
      expect(
        screen.getAllByTestId("going-write-line").map((line) => line.getAttribute("data-state"))
      ).toEqual(["done", "refused"])
    );
    await submit(user);

    await waitFor(() =>
      expect(
        screen.getAllByTestId("going-write-line").map((line) => line.getAttribute("data-state"))
      ).toEqual(["done", "done"])
    );
    await tick(1000);
    expect(sent()).toEqual([
      { approved: "auto", invocation: 0, offer: "dispatch_issue_update" },
      { approved: "auto", invocation: 1, offer: "dispatch_issue_update" },
      { approved: "auto", invocation: 1, offer: "dispatch_issue_update" },
    ]);
  } finally {
    bar.unmount();
  }
});

test("a submit before the proposal says it is preparing and runs once the proposal lands", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]));
    await screen.findByRole("option", { name: /^Run issue update/ });

    await submit(user);

    expect(await screen.findByTestId("going-operation-preparing")).toBeInTheDocument();
    expect(written).not.toHaveBeenCalled();
    stream(proposal);
    await waitFor(() => expect(written).toHaveBeenCalledTimes(2));
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
    expect(written).not.toHaveBeenCalled();
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

    await waitFor(() =>
      expect(sent()).toEqual([{ approved: "auto", invocation: 0, offer: "dispatch_artifact" }])
    );
    await screen.findByTestId("going-write-line");
    // The upload that ran has its line; the one withheld still says why it is not among them.
    expect(screen.getByTestId("going-operation-withheld")).toHaveTextContent(reason);
  } finally {
    bar.unmount();
  }
});

test("below the threshold the two best are offered, a submit runs neither, and a pick runs that one as the person's", async () => {
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
    expect(written).not.toHaveBeenCalled();

    await user.click(comment);
    await waitFor(() =>
      expect(sent()).toEqual([{ approved: "person", invocation: 0, offer: "dispatch_comment" }])
    );
  } finally {
    bar.unmount();
  }
});

test("with the person's setting on, the operation asks first and is sent confirmed once they confirm", async () => {
  settings.mockResolvedValue({ confirm_writes: true });
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
    await screen.findByRole("option", { name: /^Run issue update/ });

    await submit(user);

    const step = await screen.findByTestId("going-confirm-step");
    expect(
      within(step)
        .getAllByTestId("going-write-line")
        .map((line) => line.textContent)
    ).toEqual(["issue update LEGION-3: asks first", "issue update LEGION-4: asks first"]);
    await tick(1000);
    expect(written).not.toHaveBeenCalled();

    await user.click(within(step).getByRole("button", { name: "Confirm" }));
    await waitFor(() =>
      expect(sent()).toEqual([
        { approved: "confirmed", invocation: 0, offer: "dispatch_issue_update" },
        { approved: "confirmed", invocation: 1, offer: "dispatch_issue_update" },
      ])
    );
  } finally {
    bar.unmount();
  }
});

test("with the person's setting on, a submit pressed again while the confirmed writes are out, and again once they landed, asks nothing and writes none of them again", async () => {
  settings.mockResolvedValue({ confirm_writes: true });
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
    await screen.findByRole("option", { name: /^Run issue update/ });
    const first = Promise.withResolvers<GoingWriteLine>();
    written.mockImplementation((_run, input) =>
      input.invocation === 0 ? first.promise : Promise.resolve(landedLine(input))
    );
    await submit(user);
    await user.click(
      within(await screen.findByTestId("going-confirm-step")).getByRole("button", {
        name: "Confirm",
      })
    );
    await waitFor(() => expect(written).toHaveBeenCalledTimes(1));

    // Both writes are made, one out and one waiting its turn: nothing is left to confirm.
    await submit(user);
    await tick(1000);
    expect(screen.queryByTestId("going-confirm-step")).not.toBeInTheDocument();
    first.resolve(
      landedLine({ approved: "confirmed", invocation: 0, offer: "dispatch_issue_update" })
    );
    await waitFor(() =>
      expect(
        screen.getAllByTestId("going-write-line").map((line) => line.getAttribute("data-state"))
      ).toEqual(["done", "done"])
    );

    // Both landed: still nothing to confirm.
    await submit(user);
    await tick(1000);
    expect(screen.queryByTestId("going-confirm-step")).not.toBeInTheDocument();
    expect(sent()).toEqual([
      { approved: "confirmed", invocation: 0, offer: "dispatch_issue_update" },
      { approved: "confirmed", invocation: 1, offer: "dispatch_issue_update" },
    ]);
  } finally {
    bar.unmount();
  }
});

test("a write Dispatch refused because the person's setting asks first makes the next press ask first", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
    await screen.findByRole("option", { name: /^Run issue update/ });
    await waitFor(() => expect(settings).toHaveBeenCalledTimes(1));
    // The setting was turned on in another tab after this page read it.
    settings.mockResolvedValue({ confirm_writes: true });
    written.mockImplementation(async (_run, input) => {
      if (input.approved === "auto") {
        throw new ApiError(409, {
          code: "GOING_CONFIRMATION_REQUIRED",
          error: "your setting asks you to confirm every write of the bar",
        });
      }
      return landedLine(input);
    });

    await submit(user);
    await waitFor(() =>
      expect(
        screen.getAllByTestId("going-write-line").map((line) => line.getAttribute("data-code"))
      ).toEqual(["GOING_CONFIRMATION_REQUIRED", "GOING_CONFIRMATION_REQUIRED"])
    );
    await submit(user);

    const step = await screen.findByTestId("going-confirm-step");
    await user.click(within(step).getByRole("button", { name: "Confirm" }));
    await waitFor(() =>
      expect(sent().slice(2)).toEqual([
        { approved: "confirmed", invocation: 0, offer: "dispatch_issue_update" },
        { approved: "confirmed", invocation: 1, offer: "dispatch_issue_update" },
      ])
    );
  } finally {
    bar.unmount();
  }
});

test("a write Dispatch refused because the person's setting asks first keeps its Run row and the run hint, and a press of Run asks first before the setting is read again", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  // The read of the setting the refusal asks for, held until the end: nothing but the refusal
  // itself can tell the page the setting is on before the next press.
  const reread = Promise.withResolvers<{ confirm_writes: boolean }>();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
    await screen.findByRole("option", { name: /^Run issue update/ });
    await waitFor(() => expect(settings).toHaveBeenCalledTimes(1));
    // The setting was turned on in another tab after this page read it.
    settings.mockImplementation(() => reread.promise);
    written.mockImplementation(async (_run, input) => {
      if (input.approved === "auto") {
        throw new ApiError(409, {
          code: "GOING_CONFIRMATION_REQUIRED",
          error: "your setting asks you to confirm every write of the bar",
        });
      }
      return landedLine(input);
    });

    await submit(user);
    await waitFor(() =>
      expect(
        screen.getAllByTestId("going-write-line").map((line) => line.getAttribute("data-code"))
      ).toEqual(["GOING_CONFIRMATION_REQUIRED", "GOING_CONFIRMATION_REQUIRED"])
    );

    const row = await screen.findByRole("option", {
      name: /^Run issue update on LEGION-3 and LEGION-4/,
    });
    expect(screen.getByText("to run")).toBeInTheDocument();
    await user.click(within(row).getByRole("button", { name: "Run" }));

    const step = await screen.findByTestId("going-confirm-step");
    await user.click(within(step).getByRole("button", { name: "Confirm" }));
    await waitFor(() =>
      expect(sent().slice(2)).toEqual([
        { approved: "confirmed", invocation: 0, offer: "dispatch_issue_update" },
        { approved: "confirmed", invocation: 1, offer: "dispatch_issue_update" },
      ])
    );
  } finally {
    reread.resolve({ confirm_writes: true });
    bar.unmount();
  }
});

test("a turn whose first write landed and whose second was refused for the setting confirms the second alone", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
    await screen.findByRole("option", { name: /^Run issue update/ });
    await waitFor(() => expect(settings).toHaveBeenCalledTimes(1));
    // The setting was turned on in another tab between the turn's two requests.
    settings.mockResolvedValue({ confirm_writes: true });
    written.mockImplementation(async (_run, input) => {
      if (input.approved === "auto" && input.invocation === 1) {
        throw new ApiError(409, {
          code: "GOING_CONFIRMATION_REQUIRED",
          error: "your setting asks you to confirm every write of the bar",
        });
      }
      return landedLine(input);
    });

    await submit(user);
    await waitFor(() =>
      expect(
        screen.getAllByTestId("going-write-line").map((line) => line.getAttribute("data-code"))
      ).toEqual([null, "GOING_CONFIRMATION_REQUIRED"])
    );
    const row = await screen.findByRole("option", { name: /^Run issue update on LEGION-4/ });
    await user.click(within(row).getByRole("button", { name: "Run" }));

    const step = await screen.findByTestId("going-confirm-step");
    const listed = within(step).getAllByTestId("going-write-line");
    expect(listed).toHaveLength(1);
    expect(listed[0]).toHaveTextContent("LEGION-4");
    await user.click(within(step).getByRole("button", { name: "Confirm" }));
    await waitFor(() =>
      expect(sent().slice(2)).toEqual([
        { approved: "confirmed", invocation: 1, offer: "dispatch_issue_update" },
      ])
    );
  } finally {
    bar.unmount();
  }
});

test("a read of the setting that fails after the refusal still asks first, since the refusal showed the setting on", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
    await screen.findByRole("option", { name: /^Run issue update/ });
    await waitFor(() => expect(settings).toHaveBeenCalledTimes(1));
    settings.mockRejectedValue(new ApiError(503, { code: "UNAVAILABLE", error: "down" }));
    written.mockImplementation(async (_run, input) => {
      if (input.approved === "auto") {
        throw new ApiError(409, {
          code: "GOING_CONFIRMATION_REQUIRED",
          error: "your setting asks you to confirm every write of the bar",
        });
      }
      return landedLine(input);
    });

    await submit(user);
    await waitFor(() =>
      expect(
        screen.getAllByTestId("going-write-line").map((line) => line.getAttribute("data-code"))
      ).toEqual(["GOING_CONFIRMATION_REQUIRED", "GOING_CONFIRMATION_REQUIRED"])
    );
    // The page has read the setting again and been refused.
    await waitFor(() => expect(settings.mock.calls.length).toBeGreaterThan(1));
    const row = await screen.findByRole("option", {
      name: /^Run issue update on LEGION-3 and LEGION-4/,
    });
    await user.click(within(row).getByRole("button", { name: "Run" }));

    await screen.findByTestId("going-confirm-step");
    expect(sent().slice(2)).toEqual([]);
  } finally {
    bar.unmount();
  }
});

test("with the setting on, a write Dispatch refused is listed by the next confirm step, which sends it alone", async () => {
  const user = userOnFakeClock();
  settings.mockResolvedValue({ confirm_writes: true });
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
    await screen.findByRole("option", { name: /^Run issue update/ });
    written.mockImplementation(async (_run, input) => {
      if (input.invocation === 0 && sent().length <= 2) {
        throw new ApiError(409, { code: "ISSUE_CLOSED", error: "the issue is closed" });
      }
      return landedLine(input);
    });

    await submit(user);
    const first = await screen.findByTestId("going-confirm-step");
    expect(within(first).getAllByTestId("going-write-line")).toHaveLength(2);
    await user.click(within(first).getByRole("button", { name: "Confirm" }));
    await waitFor(() =>
      expect(
        screen.getAllByTestId("going-write-line").map((line) => line.getAttribute("data-code"))
      ).toEqual(["ISSUE_CLOSED", null])
    );

    await submit(user);
    const second = await screen.findByTestId("going-confirm-step");
    const listed = within(second).getAllByTestId("going-write-line");
    expect(listed).toHaveLength(1);
    expect(listed[0]).toHaveTextContent("LEGION-3");
    await user.click(within(second).getByRole("button", { name: "Confirm" }));
    await waitFor(() =>
      expect(sent().slice(2)).toEqual([
        { approved: "confirmed", invocation: 0, offer: "dispatch_issue_update" },
      ])
    );
  } finally {
    bar.unmount();
  }
});

test("a confirm step after a refusal of the route and a refusal for the setting lists both writes it sends", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
    await screen.findByRole("option", { name: /^Run issue update/ });
    await waitFor(() => expect(settings).toHaveBeenCalledTimes(1));
    settings.mockResolvedValue({ confirm_writes: true });
    written.mockImplementation(async (_run, input) => {
      if (input.approved === "auto" && input.invocation === 0) {
        throw new ApiError(404, { code: "ISSUE_NOT_FOUND", error: "no such issue" });
      }
      if (input.approved === "auto") {
        throw new ApiError(409, {
          code: "GOING_CONFIRMATION_REQUIRED",
          error: "your setting asks you to confirm every write of the bar",
        });
      }
      return landedLine(input);
    });

    await submit(user);
    await waitFor(() =>
      expect(
        screen.getAllByTestId("going-write-line").map((line) => line.getAttribute("data-code"))
      ).toEqual(["ISSUE_NOT_FOUND", "GOING_CONFIRMATION_REQUIRED"])
    );
    // The row names what its press sends, as the confirm step it opens lists it.
    await screen.findByRole("option", { name: /^Run issue update on LEGION-3 and LEGION-4/ });
    await submit(user);
    const step = await screen.findByTestId("going-confirm-step");
    expect(
      within(step)
        .getAllByTestId("going-write-line")
        .map((line) => line.textContent)
    ).toEqual([expect.stringContaining("LEGION-3"), expect.stringContaining("LEGION-4")]);
    await user.click(within(step).getByRole("button", { name: "Confirm" }));
    await waitFor(() =>
      expect(sent().slice(2)).toEqual([
        { approved: "confirmed", invocation: 0, offer: "dispatch_issue_update" },
        { approved: "confirmed", invocation: 1, offer: "dispatch_issue_update" },
      ])
    );
  } finally {
    bar.unmount();
  }
});

/** The decision's update of LEGION-3, which the words named, beside one of LEGION-4 that only an
 *  item's stored text asked for: the proposal marks it not automatic. */
const injected: GoingProposalEvent = {
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

test("a submit runs only the invocations the proposal marked automatic, and the rest is a row the person picks", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), injected, ended);
    expect(
      await screen.findByRole("option", { name: /^Run issue update on LEGION-3(?! and)/ })
    ).toHaveAttribute("id", OPERATION_OPTION_ID);
    const offered = screen.getByRole("option", { name: /^Run issue update on LEGION-4/ });
    expect(offered).toHaveAttribute("id", offerOptionId("dispatch_issue_update"));

    await submit(user);
    await waitFor(() => expect(screen.getAllByTestId("going-write-line")).toHaveLength(1));
    await tick(1000);
    expect(sent()).toEqual([{ approved: "auto", invocation: 0, offer: "dispatch_issue_update" }]);

    // The person picks the offered write: it runs as theirs, and the one already made is not made
    // again.
    await user.click(screen.getByRole("option", { name: /^Run issue update on LEGION-4/ }));
    await waitFor(() =>
      expect(sent()).toEqual([
        { approved: "auto", invocation: 0, offer: "dispatch_issue_update" },
        { approved: "person", invocation: 1, offer: "dispatch_issue_update" },
      ])
    );
  } finally {
    bar.unmount();
  }
});

test("a pick of the offered row before any submit runs only the invocations the row names", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), injected, ended);
    const offered = await screen.findByRole("option", { name: /^Run issue update on LEGION-4/ });

    await user.click(offered);
    await waitFor(() => expect(screen.getAllByTestId("going-write-line")).toHaveLength(1));
    await tick(1000);
    expect(sent()).toEqual([{ approved: "person", invocation: 1, offer: "dispatch_issue_update" }]);
  } finally {
    bar.unmount();
  }
});

test("after the pick row runs, the operation row stays for the automatic invocations still to run, and the submit runs them", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), injected, ended);
    await user.click(await screen.findByRole("option", { name: /^Run issue update on LEGION-4/ }));
    await waitFor(() => expect(screen.getAllByTestId("going-write-line")).toHaveLength(1));
    await tick(1000);

    expect(
      screen.getByRole("option", { name: /^Run issue update on LEGION-3(?! and)/ })
    ).toHaveAttribute("id", OPERATION_OPTION_ID);
    await submit(user);
    await waitFor(() =>
      expect(sent()).toEqual([
        { approved: "person", invocation: 1, offer: "dispatch_issue_update" },
        { approved: "auto", invocation: 0, offer: "dispatch_issue_update" },
      ])
    );
    await waitFor(() =>
      expect(screen.queryByRole("option", { name: /^Run issue update/ })).toBeNull()
    );
  } finally {
    bar.unmount();
  }
});

test("with the person's setting on, the confirm step lists only the automatic invocations and confirms only those", async () => {
  settings.mockResolvedValue({ confirm_writes: true });
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), injected, ended);
    await screen.findByRole("option", { name: /^Run issue update on LEGION-3(?! and)/ });

    await submit(user);
    const step = await screen.findByTestId("going-confirm-step");
    expect(
      within(step)
        .getAllByTestId("going-write-line")
        .map((line) => line.textContent)
    ).toEqual(["issue update LEGION-3: asks first"]);
    await user.click(within(step).getByRole("button", { name: "Confirm" }));
    await waitFor(() =>
      expect(sent()).toEqual([
        { approved: "confirmed", invocation: 0, offer: "dispatch_issue_update" },
      ])
    );
  } finally {
    bar.unmount();
  }
});

test("an invocation missing one argument asks for it, and the write carries the value as its fill", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(
      decision(true, ["dispatch_follow"]),
      {
        data: {
          offers: [
            {
              action: "dispatch_follow",
              invocations: [
                {
                  arguments: { action: "follow", ask: "ask-1" },
                  automatic: true,
                  missing: { field: "@session", kind: "text" },
                  target_argument: "ask",
                },
              ],
              offered: true,
            },
          ],
        },
        event: "proposal",
      } satisfies GoingProposalEvent,
      ended
    );
    await screen.findByRole("option", { name: /^Run follow on ask-1/ });

    await submit(user);

    const field = await screen.findByRole("textbox", { name: "Fill the session for ask-1" });
    await waitFor(() => expect(field).toHaveFocus());
    expect(written).not.toHaveBeenCalled();
    await user.keyboard("Planner{Enter}");
    await waitFor(() =>
      expect(sent()).toEqual([
        { approved: "auto", fill: "Planner", invocation: 0, offer: "dispatch_follow" },
      ])
    );
  } finally {
    bar.unmount();
  }
});

/** A follow of ask-1 and of ask-2, each missing the session that follows it. */
const follows: GoingProposalEvent = {
  data: {
    offers: [
      {
        action: "dispatch_follow",
        invocations: ["ask-1", "ask-2"].map((id) => ({
          arguments: { action: "follow", ask: id },
          automatic: true,
          missing: { field: "@session", kind: "text" },
          target_argument: "ask",
        })),
        offered: true,
      },
    ],
  },
  event: "proposal",
};

test("after one invocation landed and another was refused for the setting, the next press asks only the refused one's missing argument, and sends that one alone", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_follow"]), follows, ended);
    await screen.findByRole("option", { name: /^Run follow on ask-1 and ask-2/ });
    await waitFor(() => expect(settings).toHaveBeenCalledTimes(1));
    // The setting was turned on in another tab between the turn's two requests.
    settings.mockResolvedValue({ confirm_writes: true });
    written.mockImplementation(async (_run, input) => {
      if (input.approved === "auto" && input.invocation === 1) {
        throw new ApiError(409, {
          code: "GOING_CONFIRMATION_REQUIRED",
          error: "your setting asks you to confirm every write of the bar",
        });
      }
      return landedLine(input);
    });

    await submit(user);
    await user.type(
      await screen.findByRole("textbox", { name: "Fill the session for ask-1" }),
      "Planner"
    );
    await user.type(screen.getByRole("textbox", { name: "Fill the session for ask-2" }), "Tester");
    await user.keyboard("{Enter}");
    await waitFor(() =>
      expect(
        screen.getAllByTestId("going-write-line").map((line) => line.getAttribute("data-code"))
      ).toEqual([null, "GOING_CONFIRMATION_REQUIRED"])
    );

    const row = await screen.findByRole("option", { name: /^Run follow on ask-2/ });
    await user.click(within(row).getByRole("button", { name: "Run" }));

    // ask-1's follow landed: the press asks for ask-2's session alone.
    const field = await screen.findByRole("textbox", { name: "Fill the session for ask-2" });
    expect(screen.queryByRole("textbox", { name: "Fill the session for ask-1" })).toBeNull();
    await user.type(field, "Reviewer");
    await user.keyboard("{Enter}");
    const step = await screen.findByTestId("going-confirm-step");
    const listed = within(step).getAllByTestId("going-write-line");
    expect(listed).toHaveLength(1);
    expect(listed[0]).toHaveTextContent("ask-2");
    await user.click(within(step).getByRole("button", { name: "Confirm" }));
    await waitFor(() =>
      expect(sent().slice(2)).toEqual([
        { approved: "confirmed", fill: "Reviewer", invocation: 1, offer: "dispatch_follow" },
      ])
    );
  } finally {
    bar.unmount();
  }
});

/** Picks DEMO on the bar's project chip, which asks the same words of that project: another
 *  run, whose stream is handed `frames` once it opens. */
async function askOfAnotherProject(user: UserEvent, ...frames: GoingEvent[]): Promise<void> {
  await user.click(screen.getByRole("button", { name: "Project" }));
  await user.click(await screen.findByRole("button", { name: "DEMO" }));
  await tick(MEANING_PAUSE_MS);
  await waitFor(() => expect(streams).toHaveLength(2));
  act(() => {
    for (const frame of frames) streams[1]?.send(frame);
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

// A fill step and a confirm step ask about the writes of the run they opened for. A chip change
// with the same words makes another run current, so the step closes, and the next run's proposal
// opens none: a Confirm or a Run would send that run's writes, with the fills typed for this one.
test("a confirm step closes and sends nothing once a chip change makes another run of the same words current", async () => {
  settings.mockResolvedValue({ confirm_writes: true });
  const projects = spyOnProjects();
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_issue_update"]), proposal, ended);
    await screen.findByRole("option", { name: /^Run issue update/ });
    await submit(user);
    await screen.findByTestId("going-confirm-step");

    await askOfAnotherProject(user, decision(true, ["dispatch_issue_update"]), proposal, ended);

    await screen.findByRole("option", { name: /^Run issue update/ });
    expect(screen.queryByTestId("going-confirm-step")).not.toBeInTheDocument();
    await tick(1000);
    expect(written).not.toHaveBeenCalled();
  } finally {
    bar.unmount();
    projects.mockRestore();
  }
});

test("a fill step closes and sends nothing once a chip change makes another run of the same words current", async () => {
  const projects = spyOnProjects();
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(decision(true, ["dispatch_follow"]), follows, ended);
    await screen.findByRole("option", { name: /^Run follow on ask-1 and ask-2/ });
    await submit(user);
    await user.type(
      await screen.findByRole("textbox", { name: "Fill the session for ask-1" }),
      "Planner"
    );

    await askOfAnotherProject(user, decision(true, ["dispatch_follow"]), follows, ended);

    await screen.findByRole("option", { name: /^Run follow on ask-1 and ask-2/ });
    expect(screen.queryByTestId("going-operation-fill")).not.toBeInTheDocument();
    await tick(1000);
    expect(written).not.toHaveBeenCalled();
  } finally {
    bar.unmount();
    projects.mockRestore();
  }
});

test("a new issue the run made is the issue its write names, with no read of it that could fail", async () => {
  const newIssueSettings = spyOn(api, "getNewIssueSettings").mockResolvedValue({
    label: "",
    session: "",
  });
  const uploadArtifact = spyOn(api, "uploadArtifact").mockResolvedValue(
    {} as ArtifactUploadResponse
  );
  // A read of the issue after its create would fail here, as a timed-out read does.
  const getIssue = spyOn(api, "getIssue").mockRejectedValue(new Error("the read timed out"));
  written.mockImplementation(
    async (_run, input) =>
      ({
        action: input.offer,
        before: null,
        fields: null,
        item: "LEGION-9",
        now: { key: "LEGION-9" },
        outcome: { code: "", message: "", ok: true, status: 201 },
        write_id: "write-issue",
      }) satisfies GoingWriteLine
  );
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await ask(user, "astrolabe");
    stream(
      decision(false, ["dispatch_issue"]),
      {
        data: {
          offers: [
            {
              action: "dispatch_issue",
              invocations: [
                {
                  arguments: { project: "LEGION", title: "astrolabe" },
                  automatic: true,
                  missing: null,
                  target_argument: "",
                },
              ],
              offered: true,
            },
          ],
        },
        event: "proposal",
      } satisfies GoingProposalEvent,
      ended
    );

    await user.click(await screen.findByRole("option", { name: /^Make a new issue from/ }));

    await waitFor(() =>
      expect(screen.getByTestId("search-new-issue-said")).toHaveTextContent("LEGION-9 is made.")
    );
    expect(sent()).toEqual([
      {
        approved: "person",
        arguments: { project: "LEGION", title: "astrolabe" },
        invocation: 0,
        offer: "dispatch_issue",
      },
    ]);
    expect(uploadArtifact.mock.calls.map(([owner]) => owner)).toEqual([{ issue: "LEGION-9" }]);
    expect(getIssue).not.toHaveBeenCalled();
    // The issue is made, so its row opens it and nothing offers to make it again.
    expect(screen.getByRole("option", { name: /^Open LEGION-9/ })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /^Make a new issue/ })).not.toBeInTheDocument();
  } finally {
    bar.unmount();
    for (const spy of [newIssueSettings, uploadArtifact, getIssue]) spy.mockRestore();
  }
});

/** An ended run of `words` whose proposal offers one send to an agent, at once. */
function sendAnswer(runId: string, words: string): GoingAnswer {
  return {
    block: undefined,
    changed: undefined,
    cut: undefined,
    decision: decision(false, ["dispatch_send_to_agent"]).data,
    end: { items: 0, words: 3 },
    error: undefined,
    items: [],
    none: false,
    proposal: {
      offers: [
        {
          action: "dispatch_send_to_agent",
          invocations: [
            {
              arguments: { session: "session-1" },
              automatic: true,
              missing: null,
              target_argument: "session",
            },
          ],
          offered: true,
        },
      ],
    },
    replayed: false,
    roster: undefined,
    runId,
    state: "ended",
    text: "",
    unreachable: undefined,
    words,
  };
}

/** Dispatch's line for a send to an agent that landed: the route answers it 201 whatever became
 *  of its delivery, and the line records that delivery. */
function sentLine(
  input: GoingWriteInput,
  delivery: GoingWriteLine["delivery"],
  id: string
): GoingWriteLine {
  return {
    action: input.offer,
    before: null,
    delivery,
    fields: ["message"],
    item: id,
    now: { id },
    outcome: { code: "", message: "", ok: true, status: 201 },
    write_id: "write-send",
  };
}

// The send through the run says what the direct send says of the same delivery, but for a failed
// one: the run recorded the write, the words stored under the route's 201, so its failure is said
// with its reason and is the send's last word (`failed`), as every write the run recorded is. One
// the listener stored for a session that has stopped is queued, and only one that reached the
// session is sent. A write that landed with no delivery Dispatch could read, or with its attempt
// still pending, stored the words and no more is known: it is never sent.
test("a send through the run answers what became of its delivery, as the direct send does", async () => {
  const words = "tell the agent";
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  // Each send is its own run's: a run writes each of its invocations once.
  const hook = renderHook(
    ({ runId }: { readonly runId: string }) =>
      useGoingOperations({
        answer: sendAnswer(runId, words),
        askNow: () => false,
        hits: [],
        words,
      }),
    {
      initialProps: { runId: "run-1" },
      wrapper: ({ children }: { readonly children: ReactNode }) => (
        <QueryClientProvider client={client}>{children}</QueryClientProvider>
      ),
    }
  );
  const delivered = (delivery: GoingWriteLine["delivery"], id: string) =>
    written.mockImplementation(async (_run, input) => sentLine(input, delivery, id));
  const send = async (runId: string) => {
    hook.rerender({ runId });
    const sendToAgent = hook.result.current.sendToAgent;
    expect(sendToAgent).toBeDefined();
    let said: SendToAgentOutcome | undefined;
    await act(async () => {
      said = await sendToAgent?.(writesOf(client), {
        block: undefined,
        issue: "LEGION-3",
        session: "session-1",
        words,
      });
    });
    return said;
  };
  try {
    delivered({ error: "the session takes no aside", state: "failed" }, "m-1");
    expect(await send("run-1")).toEqual({
      kind: "failed",
      refusal: "the session takes no aside",
    });
    delivered({ error: null, state: "failed" }, "m-2");
    expect(await send("run-2")).toEqual({ kind: "failed", refusal: "Dispatch gave no reason." });
    delivered({ error: null, state: "queued" }, "m-3");
    expect(await send("run-3")).toEqual({ kind: "queued" });
    delivered({ error: null, state: "sent" }, "m-4");
    expect(await send("run-4")).toEqual({ kind: "sent" });
    delivered(undefined, "m-5");
    expect(await send("run-5")).toEqual({ kind: "stored" });
    delivered({ error: null, state: "pending" }, "m-6");
    expect(await send("run-6")).toEqual({ kind: "stored" });
    // A landing Dispatch kept no line for.
    written.mockImplementation(async () => null);
    expect(await send("run-7")).toEqual({ kind: "stored" });
  } finally {
    hook.unmount();
    client.clear();
  }
});

/** LEGION-3 as its read answers it, its claim held by `session-1`, a live agent. */
function issueHeldByALiveAgent(): IssueDetails {
  return {
    advances: [],
    advances_revision: 0,
    artifacts: [],
    assignee: null,
    blocked_by: [],
    children: [],
    claim: {
      actor: { id: "session-1", kind: "session", origin: { session_title: "Implementer" } },
      at: "2026-11-07T06:00:00Z",
    },
    closed_at: null,
    components: { ids: [], inherited_from: null, mode: "inherit", reason: null, unknown: [] },
    created_at: "2026-11-07T05:00:00Z",
    created_by: { id: "alice", kind: "user" },
    decisions_artifact_id: "",
    design_gates: [],
    external_links: [],
    holder_state: "live",
    key: "LEGION-3",
    labels: [],
    last_seen: null,
    last_seq: 1,
    number: 3,
    open_asks: [],
    parent: null,
    plan_artifact_id: "",
    primary_artifact_id: "artifact-1",
    priority: null,
    progress: { children: null, tasks: null },
    project: "LEGION",
    rank: "U",
    referenced_by_count: 0,
    repo: null,
    resolved_repo: null,
    route: null,
    route_holder: null,
    route_status: null,
    route_status_reason: null,
    status: "in_progress",
    takeable_at: null,
    title: "Instruments",
    updated_at: "2026-11-07T06:00:00Z",
    verification_artifact_id: "",
  };
}

/** The bar's offer to send the words to the agent on LEGION-3, sending through the run's write as
 *  the bar wires the two together where the run's proposal offers the send. */
function RunSendOffer({ words }: { readonly words: string }): ReactNode {
  const operations = useGoingOperations({
    answer: sendAnswer("run-1", words),
    askNow: () => false,
    hits: [],
    words,
  });
  const offer = useSendToAgentOffer({
    block: undefined,
    issue: "LEGION-3",
    send: operations.sendToAgent,
    words,
  });
  return (
    <>
      {offer?.row === undefined ? null : (
        <button onClick={offer.row.select} type="button">
          {offer.row.label}
        </button>
      )}
      {offer?.said}
    </>
  );
}

// A send through the run whose delivery failed was still recorded by the run - the route stored
// the words under its 201 - and the run writes each of its invocations once, so a second press
// would be answered that same line and send nothing. The bar says the failure with its reason and,
// as for every write the run recorded, offers no second press.
test("a send through the run whose delivery failed says why, and offers no press that would send nothing", async () => {
  const words = "tell the agent";
  const getIssue = spyOn(api, "getIssue").mockResolvedValue(issueHeldByALiveAgent());
  const listAgents = spyOn(api, "listAgents").mockRejectedValue(new Error("no registry"));
  written.mockImplementation(async (_run, input) =>
    sentLine(input, { error: "the session takes no aside", state: "failed" }, "m-1")
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const user = userOnFakeClock();
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <RunSendOffer words={words} />
      </QueryClientProvider>
    </MemoryRouter>
  );
  const label = "Send this to the agent on LEGION-3";
  try {
    await user.click(await screen.findByRole("button", { name: label }));
    const said = await screen.findByTestId("search-send-to-agent-said");
    expect(said).toHaveTextContent("the session takes no aside");
    expect(said).toHaveAttribute("role", "alert");
    expect(screen.queryByRole("button", { name: label })).not.toBeInTheDocument();
    expect(sent()).toHaveLength(1);
  } finally {
    view.unmount();
    client.clear();
    getIssue.mockRestore();
    listAgents.mockRestore();
  }
});
