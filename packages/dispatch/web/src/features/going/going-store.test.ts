import { afterEach, expect, spyOn, test } from "bun:test";

import { api } from "../../api/client";
import type { GoingRun } from "../../api/types";
import {
  asksPerson,
  conversationTitle,
  dropQueued,
  goingState,
  newConversation,
  openConversation,
  queuedReady,
  resetGoingStore,
  setPlace,
  startRun,
} from "./going-store";

// The bar's store (design v2, state 2 and the Keys table): Ctrl-K opens the bar on the
// conversation while its run works or its plan waits, else on the search; words sent while the
// run works start no second conversation beside it, and wait for it; the title is the first
// question; and an answer that asks the person a question reads as one.

const CONTEXT = { path: "/projects/CORE/issues" };

function record(id: string, conversation: string, words: string): GoingRun {
  return {
    context: null,
    continues: null,
    conversation,
    created_at: new Date().toISOString(),
    finished_at: null,
    id,
    issues: [],
    level: "standard",
    mode: "ask",
    state: "queued",
    stop_reason: null,
    used: { calls: 0, tokens: 0, working_ms: 0 },
    words,
  };
}

/** Answers each start with the next of `records`, and holds each run's stream open, handing the
 *  test the way to send it a frame. */
function answerRuns(...records: GoingRun[]) {
  const start = spyOn(api, "createGoingRun");
  for (const made of records) start.mockResolvedValueOnce(made);
  const frames = new Map<string, (name: string, data: unknown) => void>();
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((id, onFrame) => {
    frames.set(id, onFrame);
    return Promise.withResolvers<void>().promise;
  });
  return {
    restore: () => {
      start.mockRestore();
      stream.mockRestore();
    },
    send: (id: string, name: string, data: unknown) => frames.get(id)?.(name, data),
    start,
  };
}

const ENDED_DONE = { offers: [], reason: "", state: "done" };

afterEach(resetGoingStore);

test("Ctrl-K opens the bar on the conversation while its run works or its plan waits, and on the search once it ended", async () => {
  const runs = answerRuns(record("r-1", "c-1", "split CORE-1"));
  try {
    await startRun("split CORE-1", CONTEXT, "alice");
    setPlace("over");
    expect(goingState().conversation).toEqual(["r-1"]);
    setPlace("closed");

    runs.send("r-1", "plan", { plan: 1, state: "waiting" });
    runs.send("r-1", "end", { offers: [], reason: "", state: "waiting" });
    setPlace("over");
    expect(goingState().conversation).toEqual(["r-1"]);
    setPlace("closed");

    runs.send("r-1", "end", ENDED_DONE);
    setPlace("over");
    expect(goingState().conversation).toBeUndefined();
    expect(goingState().place).toBe("over");
  } finally {
    runs.restore();
  }
});

test("Ctrl-K opens the conversation of this page's run that works when the open one has ended", async () => {
  const runs = answerRuns(record("r-1", "c-1", "count the moons"));
  try {
    await startRun("count the moons", CONTEXT, "alice");
    // History opens another conversation, which ended, over the closed bar's place.
    const old: GoingRun = { ...record("r-0", "c-0", "chart the comets"), state: "done" };
    openConversation({ id: "c-0", newest_at: old.created_at, runs: [old] });
    runs.send("r-0", "end", ENDED_DONE);
    setPlace("closed");

    setPlace("over");
    expect(goingState().conversation).toEqual(["r-1"]);
  } finally {
    runs.restore();
  }
});

test("words sent while the run works start no run beside it: they wait for it, after any that wait already, until × or the run's end", async () => {
  const runs = answerRuns(record("r-1", "c-1", "watch the comet"));
  try {
    await startRun("watch the comet", CONTEXT, "alice");
    expect(await startRun("and the moon", CONTEXT, "alice")).toBe(false);
    expect(await startRun("then Mars", CONTEXT, "alice")).toBe(false);
    expect(runs.start).toHaveBeenCalledTimes(1);
    expect(goingState().conversation).toEqual(["r-1"]);
    expect(goingState().queued).toEqual({ run: "r-1", words: "and the moon\nthen Mars" });
    // They go only once the run no longer works.
    expect(queuedReady()).toBeUndefined();
    runs.send("r-1", "end", ENDED_DONE);
    expect(queuedReady()).toEqual({ run: "r-1", words: "and the moon\nthen Mars" });

    dropQueued();
    expect(goingState().queued).toBeUndefined();
  } finally {
    runs.restore();
  }
});

test("New lets the words that wait go with the conversation", async () => {
  const runs = answerRuns(record("r-1", "c-1", "watch the comet"));
  try {
    await startRun("watch the comet", CONTEXT, "alice");
    await startRun("and the moon", CONTEXT, "alice");
    newConversation();
    expect(goingState().queued).toBeUndefined();
  } finally {
    runs.restore();
  }
});

test("the title is the first question, from the moment of Enter, and an answer that ends in a question asks the person", async () => {
  const { promise: answered, resolve: answer } = Promise.withResolvers<GoingRun>();
  const start = spyOn(api, "createGoingRun").mockReturnValue(answered);
  let frames: ((name: string, data: unknown) => void) | undefined;
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((_id, onFrame) => {
    frames = onFrame;
    return Promise.withResolvers<void>().promise;
  });
  try {
    const started = startRun("Hand CORE-1 to Legion", CONTEXT, "alice");
    expect(goingState().starting?.words).toBe("Hand CORE-1 to Legion");
    expect(conversationTitle()).toBe("Hand CORE-1 to Legion");
    answer(record("r-1", "c-1", "Hand CORE-1 to Legion"));
    await started;
    expect(goingState().starting).toBeUndefined();
    expect(conversationTitle()).toBe("Hand CORE-1 to Legion");

    frames?.("reply", {
      labels: [],
      text: "Which changes hand it over: a label, a status?",
      tools: [],
    });
    expect(asksPerson()).toBe(false);
    frames?.("end", ENDED_DONE);
    expect(asksPerson()).toBe(true);
  } finally {
    start.mockRestore();
    stream.mockRestore();
  }
});
