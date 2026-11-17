import { afterEach, expect, spyOn, test } from "bun:test";
import {
  GOING_ERROR_ANSWER_DID_NOT_ARRIVE,
  GOING_ERROR_ANSWER_FAILED,
  GOING_ERROR_NO_MODEL_CONFIGURED,
  GOING_EVENT_CHANGED,
  GOING_EVENT_CUT,
  GOING_EVENT_END,
  GOING_EVENT_ERROR,
  GOING_EVENT_ITEM,
  GOING_EVENT_NONE,
  GOING_EVENT_TEXT,
} from "@legion/contracts";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { graphNode } from "../../../__tests__/graph-fixture";
import { ApiError, api } from "../../../api/client";
import type { GraphEdge, GraphReferences } from "../../../api/types";
import { RUN_PATH } from "../../refs/routes";
import { NotFoundPage } from "../../shell/NotFoundPage";
import {
  ANSWER_LINES,
  CONTROL_LABELS,
  NODE_REFERENCES_WORDS,
  RUN_PAGE_WORDS,
  STATE_LINES,
  sentenceCase,
  wordsIn,
} from "../page-words";
import { RunPage } from "./RunPage";
import type { RunFrame } from "./run-page";

const RUN = "3f2b8c1e-5d4a-4b6f-9e2d-1a7c0b9e8f43";
const ASKED_AT = "2026-11-17T09:00:00Z";
const RUN_WORD = sentenceCase(NODE_REFERENCES_WORDS.kinds.interaction);

/** The run's read: its node, carrying `frames` as INFRA-1950's answer does, and `edges`. */
function answerOf(
  frames: readonly RunFrame[],
  edges: readonly GraphEdge[] = [],
  question = "When does the release ship?"
): GraphReferences {
  return {
    edges: [...edges],
    node: Object.assign(
      graphNode({
        id: RUN,
        // Not in this stack's `GraphNode["kind"]` until INFRA-1950's tip that adds it, as
        // `RunFrame` says; that merge drops the cast.
        kind: "interaction" as GraphReferences["node"]["kind"],
        made_at: ASKED_AT,
        ref: `dispatch://interaction/${RUN}`,
        text: question,
      }),
      { frames }
    ),
  };
}

/** A frame of the answer. */
function frame(sequence: number, name: string, data: RunFrame["data"] = null): RunFrame {
  return { data, name, sequence };
}

let restore: (() => void) | undefined;
afterEach(() => {
  restore?.();
  restore = undefined;
});

/** The page at the run `id`'s address, its one read answered by `answer`, in a client of its own
 *  that waits for no retry. */
function draw(answer: () => Promise<GraphReferences>, id = RUN) {
  const getReferences = spyOn(api, "getReferences").mockImplementation(answer);
  const client = new QueryClient({ defaultOptions: { queries: { retryDelay: 0 } } });
  const view = render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[`/runs/${id}`]}>
        <Routes>
          <Route element={<RunPage />} path={RUN_PATH} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
  restore = () => {
    getReferences.mockRestore();
    view.unmount();
    client.clear();
  };
  return { getReferences, view };
}

/** `NotFoundPage` drawn on its own: its markup, and the title it gives the tab. */
function notFoundAlone(): { readonly markup: string; readonly title: string } {
  const alone = render(
    <MemoryRouter>
      <NotFoundPage />
    </MemoryRouter>
  );
  const drawn = { markup: alone.container.innerHTML, title: document.title };
  alone.unmount();
  return drawn;
}

/** Every word the run's page says of its own, none of which the not-found page may say. */
const RUN_PAGE_PHRASES = [
  RUN_WORD,
  RUN_PAGE_WORDS.question,
  RUN_PAGE_WORDS.what_changed,
  ANSWER_LINES.loading_older,
  ANSWER_LINES.run_unreadable,
];

test("a 404 draws NotFoundPage", async () => {
  const alone = notFoundAlone();
  for (const refusal of [new ApiError(404, { code: "NOT_FOUND" }), new ApiError(400, {})]) {
    const { getReferences, view } = draw(() => Promise.reject(refusal));
    await waitFor(() => expect(view.container.innerHTML).toBe(alone.markup));
    expect(document.title).toBe(alone.title);
    for (const phrase of RUN_PAGE_PHRASES) {
      expect(view.container.textContent).not.toContain(phrase);
    }
    // A run that is missing is asked for once: a retry finds it no more than the read did.
    expect(getReferences).toHaveBeenCalledTimes(1);
    restore?.();
    restore = undefined;
  }
  // An id that is no lowercase uuid names no run, and nothing is asked for it.
  for (const id of ["not-a-run", RUN.toUpperCase()]) {
    const { getReferences, view } = draw(() => Promise.resolve(answerOf([])), id);
    expect(view.container.innerHTML).toBe(alone.markup);
    expect(getReferences).toHaveBeenCalledTimes(0);
    restore?.();
    restore = undefined;
  }
});

test("no frame draws its event name or error code", async () => {
  const codes = [
    GOING_ERROR_NO_MODEL_CONFIGURED,
    GOING_ERROR_ANSWER_DID_NOT_ARRIVE,
    GOING_ERROR_ANSWER_FAILED,
    "SOMETHING_ELSE",
  ];
  const frames = [
    frame(1, GOING_EVENT_TEXT, { text: "The release ships on Friday\\." }),
    frame(2, GOING_EVENT_ITEM, {
      href: "/issues/CORE-9",
      id: "CORE-9",
      kind: "issue",
      title: "The release plan",
    }),
    frame(3, GOING_EVENT_CUT, { words: 120 }),
    frame(4, GOING_EVENT_CHANGED, { dropped: 2 }),
    frame(5, GOING_EVENT_NONE),
    ...codes.map((code, index) => frame(6 + index, GOING_EVENT_ERROR, { code })),
    frame(10, GOING_EVENT_END, { items: 1, words: 120 }),
  ];
  const { getReferences, view } = draw(() => Promise.resolve(answerOf(frames)));
  await screen.findByRole("heading", { level: 1, name: RUN_WORD });
  expect(getReferences.mock.calls).toEqual([
    [`dispatch://interaction/${RUN}`, { kind: "changed_by_run" }],
  ]);
  // The item is its title, linked, and neither its kind nor its id.
  expect(screen.getByRole("link", { name: "The release plan" })).toHaveAttribute(
    "href",
    "/issues/CORE-9"
  );

  const text = view.container.textContent ?? "";
  expect(wordsIn(text)).not.toContain("issue");
  for (const token of ["CORE-9", ...codes, "changed_by_run", "interaction", RUN, "dispatch://"]) {
    expect(text).not.toContain(token);
  }
  // Each frame by its sentence: `ANSWER_FAILED` and the code the page does not know both say
  // the answer failed.
  for (const line of [
    `${STATE_LINES.run_cut.before}120${STATE_LINES.run_cut.after}`,
    `${STATE_LINES.run_changed.before}2${STATE_LINES.run_changed.after}`,
    STATE_LINES.run_no_model,
    STATE_LINES.run_did_not_arrive,
  ]) {
    expect(screen.getByText(line)).toBeInTheDocument();
  }
  expect(screen.getAllByText(STATE_LINES.run_failed)).toHaveLength(2);
});

test("a failed read other than 404 draws its one sentence with Retry", async () => {
  const user = userEvent.setup();
  const { getReferences } = draw(() => Promise.reject(new ApiError(500, {})));
  expect(await screen.findByRole("alert")).toHaveTextContent(ANSWER_LINES.run_unreadable);
  expect(screen.queryByRole("heading", { name: RUN_WORD })).toBeNull();
  const asked = getReferences.mock.calls.length;
  await user.click(screen.getByRole("button", { name: CONTROL_LABELS.retry }));
  await waitFor(() => expect(getReferences.mock.calls.length).toBeGreaterThan(asked));
});

test("the answered page draws the question as typed, the time and what it changed", async () => {
  const question = "**not bold** [x](y)";
  const changed: GraphEdge = {
    created_at: "2026-11-17T09:00:05Z",
    direction: "in",
    excerpt: { text: "Ship the **release** on Friday. Then rest." },
    kind: "changed_by_run",
    node: graphNode({ id: "CORE-1", issue_key: "CORE-1", kind: "issue", ref: "dispatch://CORE-1" }),
    source_seq: null,
  };
  const frames = [
    frame(1, GOING_EVENT_TEXT, { text: "Done\\." }),
    frame(2, GOING_EVENT_END, { items: 0, words: 1 }),
  ];
  draw(() => Promise.resolve(answerOf(frames, [changed], question)));
  await screen.findByRole("heading", { level: 1, name: RUN_WORD });
  expect(document.title).toBe(`${RUN_WORD} · Dispatch`);

  // The question, character for character: no strong text and no link made of it.
  const asked = screen.getByRole("region", { name: RUN_PAGE_WORDS.question });
  expect(within(asked).getByText(question).textContent).toBe(question);
  expect(asked.querySelector("strong")).toBeNull();
  expect(within(asked).queryByRole("link")).toBeNull();

  // When it was asked.
  expect(document.querySelector(`time[datetime="${ASKED_AT}"]`)).not.toBeNull();

  // The issue it changed, by its kind's word and its first line, which links to it.
  const what = screen.getByRole("region", { name: RUN_PAGE_WORDS.what_changed });
  expect(within(what).getByRole("listitem")).toHaveTextContent(
    sentenceCase(NODE_REFERENCES_WORDS.kinds.issue)
  );
  expect(within(what).getByRole("link", { name: "Ship the release on Friday." })).toHaveAttribute(
    "href",
    "/issues/CORE-1"
  );
});
