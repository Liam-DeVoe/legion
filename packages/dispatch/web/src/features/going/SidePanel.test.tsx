import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { api } from "../../api/client";
import type { GoingRun } from "../../api/types";
import { resetGoingStore, setPlace } from "./going-store";
import { SidePanel } from "./SidePanel";

// The conversation kept at the side of the page (spec R1, R11): a click on the page names the item
// the next request is about, in place of the page's own issue and project, and History, read again
// once a run starts there, lists that run. Its box is the bar's (design v2, state 2): what Enter
// does now, the chip, Stop while the run works, and words sent during the run held until it ends.

const PAGE = "/issues/CORE-1";

const started: GoingRun = {
  context: null,
  continues: null,
  conversation: "c-9",
  created_at: new Date().toISOString(),
  finished_at: null,
  id: "r-9",
  issues: [],
  level: "standard",
  mode: "ask",
  state: "queued",
  stop_reason: null,
  used: { calls: 0, tokens: 0, working_ms: 0 },
  words: "what does the runbook say",
};

/** The panel at the side of `PAGE`, beside `page`, alice signed in, on a client that keeps a read
 *  fresh for 30 s as the app's does (`main.tsx`). */
function renderPanel(page: ReactNode = null): void {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: 30_000 } },
  });
  queryClient.setQueryData(["whoami"], { event_head: "0", kind: "user", login: "alice" });
  window.history.pushState({}, "", PAGE);
  setPlace("side");
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter
        future={{ v7_relativeSplatPath: true, v7_startTransition: true }}
        initialEntries={[PAGE]}
      >
        {page}
        <SidePanel />
      </MemoryRouter>
    </QueryClientProvider>
  );
}

/** Starts are answered with `started`, whose stream stays open. */
function answerStarts() {
  const start = spyOn(api, "createGoingRun").mockResolvedValue(started);
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation(
    () => Promise.withResolvers<void>().promise
  );
  return {
    restore: () => {
      start.mockRestore();
      stream.mockRestore();
    },
    start,
  };
}

afterEach(() => {
  cleanup();
  resetGoingStore();
  window.history.pushState({}, "", "/");
});

test("a click on a project's document is the next request's context, and the page's issue is not", async () => {
  const user = userEvent.setup();
  const starts = answerStarts();
  try {
    renderPanel(<span data-dispatch-ref="/projects/OPS/documents/runbook">The runbook</span>);
    await user.click(screen.getByText("The runbook"));
    await user.type(
      screen.getByRole("textbox", { name: "Ask the command bar" }),
      `${started.words}{Enter}`
    );

    await waitFor(() => expect(starts.start).toHaveBeenCalledTimes(1));
    expect(starts.start.mock.calls[0]?.[0].context).toEqual({
      block: "",
      issue: "",
      path: PAGE,
      project: "OPS",
    });
  } finally {
    starts.restore();
  }
});

test("History, opened again after a run starts at the side, lists that run", async () => {
  const user = userEvent.setup();
  const starts = answerStarts();
  let listed = false;
  const history = spyOn(api, "myGoingRuns").mockImplementation(async () => ({
    limit: 50,
    offset: 0,
    rows: listed
      ? [{ id: started.conversation, newest_at: started.created_at, runs: [started] }]
      : [],
    total: listed ? 1 : 0,
  }));
  try {
    renderPanel();
    const toggle = screen.getByTestId("going-history-button");
    await user.click(toggle);
    expect(await screen.findByText("No conversation in the last 7 days.")).toBeInTheDocument();
    await user.click(toggle);

    listed = true;
    await user.type(
      screen.getByRole("textbox", { name: "Ask the command bar" }),
      `${started.words}{Enter}`
    );
    await screen.findByTestId("going-run");
    await user.click(toggle);

    const shown = await screen.findByTestId("going-history");
    expect(await within(shown).findByText(started.words)).toBeInTheDocument();
  } finally {
    history.mockRestore();
    starts.restore();
  }
});

test("the panel's box says what Enter does, and words sent while its run works go as the next turn once it ends", async () => {
  const user = userEvent.setup();
  const next: GoingRun = {
    ...started,
    continues: started.id,
    id: "r-10",
    words: "and the appendix",
  };
  const start = spyOn(api, "createGoingRun")
    .mockResolvedValueOnce(started)
    .mockResolvedValueOnce(next);
  let send: ((name: string, data: unknown) => void) | undefined;
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((id, onFrame) => {
    if (id === started.id) send = onFrame;
    return Promise.withResolvers<void>().promise;
  });
  try {
    renderPanel();
    const box = screen.getByRole("textbox", { name: "Ask the command bar" });
    expect(box).toHaveAttribute("placeholder", "Ask…");
    expect(screen.getByTestId("going-box-chip")).toHaveTextContent("CORE-1");
    await user.type(box, `${started.words}{Enter}`);
    await screen.findByTestId("going-run");
    expect(box).toHaveAttribute("placeholder", "Ask a follow-up…");
    expect(screen.getByTestId("going-stop")).toBeInTheDocument();

    await user.type(box, "and the appendix{Enter}");
    expect(start).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("going-queued")).toHaveTextContent("and the appendix");

    act(() => {
      send?.("reply", { labels: [], text: "It says to page the on-call.", tools: [] });
      send?.("end", { offers: [], reason: "", state: "done" });
    });
    await waitFor(() => expect(start).toHaveBeenCalledTimes(2));
    expect(start.mock.calls[1]?.[0]).toMatchObject({
      continues: started.id,
      words: "and the appendix",
    });
    await waitFor(() => expect(screen.getAllByTestId("going-run")).toHaveLength(2));
    expect(screen.queryByTestId("going-queued")).toBeNull();
  } finally {
    start.mockRestore();
    stream.mockRestore();
  }
});
