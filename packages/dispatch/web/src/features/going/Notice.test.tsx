import { expect, jest, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter, useLocation } from "react-router-dom";

import { realClock, tick } from "../../__tests__/fake-clock";
import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { answerRoute } from "../../__tests__/send-answers";
import { userOnFakeClock } from "../../__tests__/user";
import type { WriteBounds } from "../../api/client";
import type { GoingUndoAnswer } from "../../api/types";
import { Notice, showNotice } from "./Notice";

// The notice at the bottom right of the page (design v3, states 11 and 13): one line for a few
// seconds, its actions, and Undo through the run's own undo (R6). The shell's status corner draws
// it; the setup's `afterEach` lets any notice a row left go.

function CurrentRoute(): ReactNode {
  return <output data-testid="current-route">{useLocation().pathname}</output>;
}

function renderNotice(): void {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  render(
    <MemoryRouter>
      <QueryClientProvider client={queryClient}>
        <CurrentRoute />
        <Notice />
      </QueryClientProvider>
    </MemoryRouter>
  );
}

test("a notice shows its line and actions, and goes once its time is up", async () => {
  jest.useFakeTimers();
  try {
    renderNotice();
    expect(screen.queryByTestId("going-notice")).toBeNull();
    act(() =>
      showNotice({
        actions: [{ label: "Open", to: "/issues/CORE-1" }],
        ms: 6_000,
        text: "Done: Watch the comet",
      })
    );
    const notice = screen.getByTestId("going-notice");
    expect(notice).toHaveAttribute("role", "status");
    expect(notice).toHaveTextContent("Done: Watch the comet · Open");

    await tick(5_900);
    expect(screen.getByTestId("going-notice")).toBeInTheDocument();
    await tick(200);
    expect(screen.queryByTestId("going-notice")).toBeNull();
  } finally {
    realClock();
  }
});

test("a newer notice takes the place of the one shown, with a time of its own", async () => {
  jest.useFakeTimers();
  try {
    renderNotice();
    act(() => showNotice({ ms: 6_000, text: "Done: Watch the comet" }));
    await tick(5_000);
    act(() => showNotice({ ms: 6_000, text: "Failed: Count the moons" }));
    expect(screen.getAllByTestId("going-notice")).toHaveLength(1);
    expect(screen.getByTestId("going-notice")).toHaveTextContent("Failed: Count the moons");
    // The first notice's time is up, and the newer one stays for its own.
    await tick(2_000);
    expect(screen.getByTestId("going-notice")).toHaveTextContent("Failed: Count the moons");
    await tick(4_000);
    expect(screen.queryByTestId("going-notice")).toBeNull();
  } finally {
    realClock();
  }
});

test("an action opens its page or runs its press, and the notice goes", async () => {
  jest.useFakeTimers();
  const user = userOnFakeClock();
  try {
    renderNotice();
    act(() =>
      showNotice({
        actions: [{ label: "Open", to: "/issues/CORE-46" }],
        ms: 10_000,
        text: "Filed CORE-46",
      })
    );
    await user.click(screen.getByRole("link", { name: "Open" }));
    expect(screen.getByTestId("current-route")).toHaveTextContent("/issues/CORE-46");
    expect(screen.queryByTestId("going-notice")).toBeNull();

    const pressed: string[] = [];
    act(() =>
      showNotice({
        actions: [{ label: "Review", press: () => pressed.push("review") }],
        ms: 6_000,
        text: "Needs you: Change CORE-10",
      })
    );
    await user.click(screen.getByRole("button", { name: "Review" }));
    expect(pressed).toEqual(["review"]);
    expect(screen.queryByTestId("going-notice")).toBeNull();
  } finally {
    realClock();
  }
});

test("Undo reverses the run through its own undo, and the notice says what was undone or why not", async () => {
  const user = userEvent.setup();
  const undo = answerRoute<
    (run: string, body: undefined, bounds: WriteBounds) => Promise<GoingUndoAnswer>
  >("POST", "/api/v1/going/runs/*/undo");
  undo.mockImplementationOnce(async () => ({
    lines: [
      {
        fields: [],
        item: "CORE-46",
        outcome: { code: "", message: "", ok: true, status: 200 },
        plan: 1,
        row: 0,
        tool: "dispatch_issue",
        undo: "close_created",
      },
    ],
  }));
  renderNotice();
  act(() =>
    showNotice({
      ms: 10_000,
      text: "Filed CORE-46",
      undo: { run: "r-file", undone: "Undone · CORE-46 closed" },
    })
  );
  const notice = screen.getByTestId("going-notice");
  expect(notice).toHaveTextContent("Filed CORE-46 · Undo");
  await user.click(within(notice).getByRole("button", { name: "Undo" }));
  await waitFor(() => expect(undo).toHaveBeenCalledWith("r-file", undefined, keyedAttempt));
  await waitFor(() =>
    expect(screen.getByTestId("going-notice")).toHaveTextContent("Undone · CORE-46 closed")
  );
  expect(within(screen.getByTestId("going-notice")).queryByRole("button")).toBeNull();

  // A change another writer made since stays, and the notice says so in its words.
  undo.mockImplementationOnce(async () => ({
    lines: [
      {
        fields: [],
        item: "CORE-47",
        outcome: { code: "", message: "bob reopened CORE-47 since", ok: false, status: 409 },
        plan: 1,
        row: 0,
        tool: "dispatch_issue",
        undo: "close_created",
      },
    ],
  }));
  act(() =>
    showNotice({
      ms: 10_000,
      text: "Filed CORE-47",
      undo: { run: "r-file-2", undone: "Undone · CORE-47 closed" },
    })
  );
  await user.click(screen.getByRole("button", { name: "Undo" }));
  await waitFor(() =>
    expect(screen.getByTestId("going-notice")).toHaveTextContent(
      "Not undone: bob reopened CORE-47 since"
    )
  );
});
