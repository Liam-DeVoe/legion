import { afterEach, beforeEach, describe, expect, jest, type Mock, spyOn, test } from "bun:test";
import { act, screen, waitFor, within } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";

import { realClock, tick } from "../../__tests__/fake-clock";
import { userOnFakeClock } from "../../__tests__/user";
import { api } from "../../api/client";
import type { GoingEvent, GoingLine, GoingRun } from "../../api/types";
import { resetGoingStore } from "../going/going-store";
import { FILED_NOTICE_MS } from "./filing";
import {
  CONFIGURED_CAPABILITY,
  type GoingBarSpies,
  type OpenedStream,
  renderBar,
  spyOnGoingBar,
} from "./going-bar-fixture";
import { forgetAnsweredRun } from "./useGoingAnswer";
import { LINE_WAIT_MS, type LineEvent, type LineState, lineNext, lineShown } from "./useGoingBar";
import { KEYSTROKE_DEBOUNCE_MS, MEANING_PAUSE_MS } from "./useTieredSearch";

// The line on top of the command bar (spec R2, design v3, state 1): the hits show at once, and the
// line reads Ask, with a faint pulsing dot, until the decision model Jev, asked on the pause,
// names another line; an answer that comes 500 ms after the pause or later is dropped, so the
// line never changes under the person's Enter, and Enter on File as a new issue files the words.

const states: Readonly<Record<string, LineState>> = {
  typing: { kind: "typing" },
  "hits shown": { kind: "hits" },
  "Jev pending": { kind: "pending" },
  "Jev answered Do": { kind: "answered", line: "do" },
  "500 ms passed": { kind: "ask" },
  "late answer dropped": { kind: "dropped" },
};

const events: Readonly<Record<string, LineEvent>> = {
  "a keystroke": { kind: "keystroke" },
  "the hits arriving": { kind: "hits" },
  "the pause": { kind: "pause" },
  "Jev answering File": { kind: "jev", line: "file" },
  "500 ms": { kind: "wait" },
  Enter: { kind: "enter" },
};

const answeredFile: LineState = { kind: "answered", line: "file" };

type Row = readonly [keyof typeof states, keyof typeof events, LineState, GoingLine];

/** Each state under each event: the state the line moves to, and what it then reads, which is
 *  what Enter on it runs. */
const rows: readonly Row[] = [
  ["typing", "a keystroke", { kind: "typing" }, "ask"],
  ["typing", "the hits arriving", { kind: "hits" }, "ask"],
  ["typing", "the pause", { kind: "pending" }, "ask"],
  ["typing", "Jev answering File", { kind: "typing" }, "ask"],
  ["typing", "500 ms", { kind: "typing" }, "ask"],
  ["typing", "Enter", { kind: "ask" }, "ask"],
  ["hits shown", "a keystroke", { kind: "typing" }, "ask"],
  ["hits shown", "the hits arriving", { kind: "hits" }, "ask"],
  ["hits shown", "the pause", { kind: "pending" }, "ask"],
  ["hits shown", "Jev answering File", { kind: "hits" }, "ask"],
  ["hits shown", "500 ms", { kind: "hits" }, "ask"],
  ["hits shown", "Enter", { kind: "ask" }, "ask"],
  ["Jev pending", "a keystroke", { kind: "typing" }, "ask"],
  ["Jev pending", "the hits arriving", { kind: "pending" }, "ask"],
  ["Jev pending", "the pause", { kind: "pending" }, "ask"],
  ["Jev pending", "Jev answering File", answeredFile, "file"],
  ["Jev pending", "500 ms", { kind: "ask" }, "ask"],
  ["Jev pending", "Enter", { kind: "ask" }, "ask"],
  ["Jev answered Do", "a keystroke", { kind: "typing" }, "ask"],
  ["Jev answered Do", "the hits arriving", { kind: "answered", line: "do" }, "do"],
  ["Jev answered Do", "the pause", { kind: "answered", line: "do" }, "do"],
  ["Jev answered Do", "Jev answering File", { kind: "answered", line: "do" }, "do"],
  ["Jev answered Do", "500 ms", { kind: "answered", line: "do" }, "do"],
  ["Jev answered Do", "Enter", { kind: "answered", line: "do" }, "do"],
  ["500 ms passed", "a keystroke", { kind: "typing" }, "ask"],
  ["500 ms passed", "the hits arriving", { kind: "ask" }, "ask"],
  ["500 ms passed", "the pause", { kind: "ask" }, "ask"],
  ["500 ms passed", "Jev answering File", { kind: "dropped" }, "ask"],
  ["500 ms passed", "500 ms", { kind: "ask" }, "ask"],
  ["500 ms passed", "Enter", { kind: "ask" }, "ask"],
  ["late answer dropped", "a keystroke", { kind: "typing" }, "ask"],
  ["late answer dropped", "the hits arriving", { kind: "dropped" }, "ask"],
  ["late answer dropped", "the pause", { kind: "dropped" }, "ask"],
  ["late answer dropped", "Jev answering File", { kind: "dropped" }, "ask"],
  ["late answer dropped", "500 ms", { kind: "dropped" }, "ask"],
  ["late answer dropped", "Enter", { kind: "dropped" }, "ask"],
];

describe("theLineOnTopStates", () => {
  for (const [state, event, next, reads] of rows) {
    test(`the line ${state}, on ${event}, moves to ${next.kind} and reads ${reads}`, () => {
      const moved = lineNext(states[state] as LineState, events[event] as LineEvent);
      expect(moved).toEqual(next);
      expect(lineShown(moved)).toBe(reads);
    });
  }

  test("Enter runs what the line reads before it: Ask while Jev is pending, Do once he answered", () => {
    expect(lineShown(states["Jev pending"] as LineState)).toBe("ask");
    expect(lineShown(states["Jev answered Do"] as LineState)).toBe("do");
  });
});

// The same rules through the bar itself, its clock held, as AnswerPanel.test.tsx holds it: the
// pause asks the run of the words, whose decision frame a row sends down its stream.

let spies: GoingBarSpies;
let streams: OpenedStream[];
let createGoingRun: Mock<typeof api.createGoingRun>;

beforeEach(() => {
  jest.useFakeTimers();
  spies = spyOnGoingBar();
  streams = spies.streams;
  createGoingRun = spyOn(api, "createGoingRun").mockRejectedValue(new Error("no run in this test"));
});

afterEach(() => {
  spies.restore();
  createGoingRun.mockRestore();
  forgetAnsweredRun();
  resetGoingStore();
  realClock();
});

/** Types `words`, waits for the hits, and moves the clock through the pause that asks Jev. */
async function typeAndPause(user: UserEvent, words: string): Promise<void> {
  const input = screen.getByRole<HTMLInputElement>("combobox", { name: "Search" });
  await user.click(input);
  await user.keyboard(words);
  await tick(KEYSTROKE_DEBOUNCE_MS);
  await waitFor(() => expect(screen.getAllByRole("option").length).toBeGreaterThan(1));
  expect(lineWords()).toHaveTextContent(/^Ask/);
  await tick(MEANING_PAUSE_MS - KEYSTROKE_DEBOUNCE_MS);
  await waitFor(() => expect(streams).toHaveLength(1));
}

/** A row of the line on top: its first by default, or its second, Ask. */
function lineRow(id = "search-option-line"): HTMLElement {
  const line = document.getElementById(id);
  if (line === null) {
    throw new Error(`the bar lists no ${id}`);
  }
  return line;
}

/** What a row of the line on top says: its label and the words. */
function lineWords(id?: string): HTMLElement {
  return within(lineRow(id)).getByTestId("search-line-words");
}

/** The keys the foot names for the highlighted row. */
function footHints(): HTMLElement {
  return screen.getByTestId("search-keyboard-hints");
}

/** Hands the run's stream Jev's decision of `action`, which gives `line`. */
function decide(line: GoingLine, action: string): void {
  const frame: GoingEvent = { data: { action, context: "named", line }, event: "decision" };
  act(() => streams[0]?.send(frame));
}

test("while Jev may still decide, the line reads Ask with a faint pulsing dot in a place the row keeps", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await typeAndPause(user, "who looks after the astrolabe");
    const row = lineRow();
    const parts = row.children.length;
    expect(within(row).getByTestId("search-line-deciding")).toBeInTheDocument();
    expect(lineWords()).toHaveTextContent("Ask who looks after the astrolabe");
    expect(row).toHaveTextContent("↵");
    expect(footHints()).toHaveTextContent("↵ Ask · Esc Close");

    // Past the wait the line reads Ask for good: the dot goes, and its place stays.
    await tick(LINE_WAIT_MS);
    expect(within(row).queryByTestId("search-line-deciding")).toBeNull();
    expect(row.children).toHaveLength(parts);
    expect(lineWords()).toHaveTextContent("Ask who looks after the astrolabe");
  } finally {
    bar.unmount();
  }
});

test("Jev answering within 500 ms of the pause makes the line Do, it stays Do, and Ask is the second row", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await typeAndPause(user, "mark the astrolabe started");
    decide("do", "dispatch_issue_update");
    await waitFor(() => expect(lineWords()).toHaveTextContent(/^Do/));
    await tick(LINE_WAIT_MS);
    expect(lineWords()).toHaveTextContent("Do mark the astrolabe started");
    expect(lineRow()).toHaveAttribute("data-line", "do");
    expect(lineWords("search-option-line-ask")).toHaveTextContent("Ask mark the astrolabe started");
    expect(screen.queryByTestId("search-line-deciding")).toBeNull();
    expect(footHints()).toHaveTextContent("↵ Do · Esc Close");
  } finally {
    bar.unmount();
  }
});

test("Jev answering after 500 ms is dropped, and the line reads Ask", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await typeAndPause(user, "mark the astrolabe started");
    await tick(LINE_WAIT_MS);
    decide("do", "dispatch_issue_update");
    await tick(0);
    expect(lineWords()).toHaveTextContent(/^Ask/);
    expect(document.getElementById("search-option-line-ask")).toBeNull();
  } finally {
    bar.unmount();
  }
});

test("with no deciding model the line reads Ask, no dot pulses, and nothing says why", async () => {
  const user = userOnFakeClock();
  spies.goingCapability.mockResolvedValue({
    ...CONFIGURED_CAPABILITY,
    decision: { configured: false, model: "", reachable: false },
  });
  const bar = renderBar();
  try {
    await typeAndPause(user, "mark the astrolabe started");
    expect(lineWords()).toHaveTextContent("Ask mark the astrolabe started");
    expect(screen.queryByTestId("search-line-deciding")).toBeNull();
    expect(screen.queryByText(/deciding model/)).toBeNull();
  } finally {
    bar.unmount();
  }
});

test("Enter before Jev answers runs Ask: a run of the words of no kind", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await typeAndPause(user, "who looks after the astrolabe");
    await user.keyboard("{Enter}");
    await waitFor(() => expect(createGoingRun).toHaveBeenCalledTimes(1));
    const [input] = createGoingRun.mock.calls[0] ?? [];
    expect(input).toMatchObject({ level: "standard", words: "who looks after the astrolabe" });
    expect(input).not.toHaveProperty("kind");
  } finally {
    bar.unmount();
  }
});

// Enter on File as a new issue (R9, design v3, state 13): the words are filed at once by a
// new-issue run, while the line's first row reads "Filing in LEGION…" with a spinner and nothing
// else changes; once the run's end names the issue, the bar closes and the notice reads "Filed
// LEGION-46 · Open · Undo" for 10 s; a filing that files nothing stays in the bar, in words, with
// Try again.

const FILE_WORDS = "we need a dome for the astrolabe";

/** The record `POST /api/v1/going/runs` answers for the new-issue run `id`. */
function newIssueRun(id: string): GoingRun {
  return {
    context: null,
    continues: null,
    conversation: id,
    created_at: new Date().toISOString(),
    finished_at: null,
    id,
    issues: [],
    level: "fast",
    mode: "ask",
    state: "queued",
    stop_reason: null,
    used: { calls: 0, tokens: 0, working_ms: 0 },
    words: FILE_WORDS,
  };
}

/** Each new-issue run's stream, held open until a row ends it with its end frame. */
function holdRunStreams() {
  const ends: ((end: Record<string, unknown>) => void)[] = [];
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((_id, onFrame) => {
    const { promise, resolve } = Promise.withResolvers<void>();
    ends.push((end) => {
      onFrame("end", end);
      resolve();
    });
    return promise;
  });
  return { ends, stream };
}

/** Types the words, has Jev answer File, and presses Enter on the line. */
async function enterOnFile(user: UserEvent): Promise<void> {
  await typeAndPause(user, FILE_WORDS);
  decide("file", "dispatch_issue");
  await waitFor(() => expect(lineWords()).toHaveTextContent(/^File as a new issue in LEGION/));
  expect(lineWords()).toHaveTextContent(`File as a new issue in LEGION: ${FILE_WORDS}`);
  expect(lineWords("search-option-line-ask")).toHaveTextContent(`Ask ${FILE_WORDS}`);
  expect(footHints()).toHaveTextContent("↵ File · Esc Close");
  await user.keyboard("{Enter}");
}

test("Enter on File as a new issue files the words: the line reads Filing, then the bar closes and the notice offers Open and Undo for 10 s", async () => {
  const user = userOnFakeClock();
  createGoingRun.mockResolvedValue(newIssueRun("run-filing"));
  const { ends, stream } = holdRunStreams();
  let closed = 0;
  const bar = renderBar(undefined, () => {
    closed += 1;
  });
  try {
    await enterOnFile(user);
    await waitFor(() => expect(createGoingRun).toHaveBeenCalledTimes(1));
    const [input] = createGoingRun.mock.calls[0] ?? [];
    expect(input).toEqual({
      context: { block: "", issue: "", path: "/projects/LEGION/issues", project: "LEGION" },
      kind: "new_issue",
      words: FILE_WORDS,
    });

    // The first row turns into the filing, and nothing else changes: the second row, the words
    // in the box and the bar itself stay as they were.
    await waitFor(() => expect(lineRow()).toHaveAttribute("data-line", "filing"));
    expect(lineWords()).toHaveTextContent("Filing in LEGION…");
    expect(lineRow()).not.toHaveTextContent("↵");
    expect(lineWords("search-option-line-ask")).toHaveTextContent(`Ask ${FILE_WORDS}`);
    expect(screen.getByRole("combobox", { name: "Search" })).toHaveValue(FILE_WORDS);
    expect(screen.getByRole("dialog", { name: "Search" })).toHaveAttribute("data-state", "search");
    expect(closed).toBe(0);

    await waitFor(() => expect(ends).toHaveLength(1));
    await act(async () => {
      ends[0]?.({ issue: "LEGION-46", offers: ["undo"], reason: "", state: "done" });
    });
    await waitFor(() => expect(closed).toBe(1));
    const notice = screen.getByTestId("going-notice");
    expect(notice).toHaveTextContent("Filed LEGION-46 · Open · Undo");
    expect(within(notice).getByRole("link", { name: "Open" })).toHaveAttribute(
      "href",
      "/issues/LEGION-46"
    );
    expect(within(notice).getByRole("button", { name: "Undo" })).toBeInTheDocument();

    await tick(FILED_NOTICE_MS);
    expect(screen.queryByTestId("going-notice")).toBeNull();
  } finally {
    stream.mockRestore();
    bar.unmount();
  }
});

test("a filing that files nothing stays in the bar in words, with Try again, which files the words again", async () => {
  const user = userOnFakeClock();
  createGoingRun.mockResolvedValue(newIssueRun("run-refused"));
  const { ends, stream } = holdRunStreams();
  let closed = 0;
  const bar = renderBar(undefined, () => {
    closed += 1;
  });
  try {
    await enterOnFile(user);
    await waitFor(() => expect(ends).toHaveLength(1));
    await act(async () => {
      ends[0]?.({
        offers: [],
        reason: "NEAR_COPY: a near copy is filed already, LEGION-7",
        state: "failed",
      });
    });
    await waitFor(() => expect(lineRow()).toHaveAttribute("data-line", "failed"));
    // The reason in words, never its code, and one next step.
    expect(lineWords()).toHaveTextContent("Not filed: a near copy is filed already, LEGION-7");
    expect(lineRow()).toHaveTextContent("Try again");
    expect(footHints()).toHaveTextContent("↵ Try again · Esc Close");
    expect(screen.queryByTestId("going-notice")).toBeNull();
    expect(closed).toBe(0);

    await user.keyboard("{Enter}");
    await waitFor(() => expect(createGoingRun).toHaveBeenCalledTimes(2));
    expect(createGoingRun.mock.calls[1]?.[0]).toMatchObject({
      kind: "new_issue",
      words: FILE_WORDS,
    });
  } finally {
    stream.mockRestore();
    bar.unmount();
  }
});

test("a filing Dispatch refuses to start says so in the bar, and a word typed after lets it go", async () => {
  const user = userOnFakeClock();
  const bar = renderBar();
  try {
    await enterOnFile(user);
    await waitFor(() => expect(lineRow()).toHaveAttribute("data-line", "failed"));
    expect(lineWords()).toHaveTextContent("Not filed: Dispatch could not be reached.");

    await user.keyboard("s");
    await waitFor(() => expect(lineRow()).not.toHaveAttribute("data-line", "failed"));
  } finally {
    bar.unmount();
  }
});
