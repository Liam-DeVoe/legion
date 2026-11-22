import { afterEach, expect, spyOn, test } from "bun:test";
import { waitFor } from "@testing-library/react";

import { api } from "../../api/client";
import type { GoingRun } from "../../api/types";
import {
  asksPerson,
  continueRun,
  conversationTitle,
  dropQueued,
  goingState,
  newConversation,
  openConversation,
  queuedReady,
  resetGoingStore,
  setLevel,
  setPlace,
  startRun,
} from "./going-store";

// The bar's store (design v2, states 2 and 3 and the Keys table): Ctrl-K opens the bar on the
// conversation while its run works or its plan waits, else on the search; words sent while the
// run works start no second conversation beside it, and wait for it; the title is the first
// question; an answer asks the person a question where the frame that ended it says so; and a
// run whose stream dropped is reconnecting until the stream comes back, ends or is given up.

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

// Design v2, state 9: Continue on a run a bound stopped is a follow-up at that run's own level,
// whatever the bar is set to now, and "Continue in Deep" names Deep; it stamps when the run ended.
test("Continue follows a bound-stopped run up at its own level, and Continue in Deep at Deep", async () => {
  const runs = answerRuns(
    record("r-1", "c-1", "split CORE-1"),
    record("r-2", "c-1", "Continue"),
    record("r-3", "c-1", "Continue")
  );
  const BOUND = { offers: ["continue"], reason: "TIME_BUDGET: …", state: "stopped" };
  try {
    await startRun("split CORE-1", CONTEXT, "alice");
    runs.send("r-1", "end", BOUND);
    expect(goingState().runs.get("r-1")?.endedAt).toBeNumber();
    setLevel("fast");
    expect(await continueRun("r-1", CONTEXT, "alice")).toBe(true);
    expect(runs.start.mock.calls[1]?.[0]).toEqual(
      expect.objectContaining({ continues: "r-1", level: "standard", words: "Continue" })
    );
    runs.send("r-2", "end", BOUND);
    expect(await continueRun("r-2", CONTEXT, "alice", "deep")).toBe(true);
    expect(runs.start.mock.calls[2]?.[0]).toEqual(
      expect.objectContaining({ continues: "r-2", level: "deep" })
    );
    // A run that a bound did not stop offers no Continue.
    runs.send("r-3", "end", ENDED_DONE);
    expect(await continueRun("r-3", CONTEXT, "alice")).toBe(false);
  } finally {
    runs.restore();
  }
});

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

test("the title is the first question, from the moment of Enter, and an answer asks the person only where its frame says it waits for them", async () => {
  const { promise: answered, resolve: answer } = Promise.withResolvers<GoingRun>();
  const start = spyOn(api, "createGoingRun").mockReturnValue(answered);
  const frames = new Map<string, (name: string, data: unknown) => void>();
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation((id, onFrame) => {
    frames.set(id, onFrame);
    return Promise.withResolvers<void>().promise;
  });
  const send = (id: string, name: string, data: unknown) => frames.get(id)?.(name, data);
  try {
    const started = startRun("Hand CORE-1 to Legion", CONTEXT, "alice");
    expect(goingState().starting?.words).toBe("Hand CORE-1 to Legion");
    expect(conversationTitle()).toBe("Hand CORE-1 to Legion");
    answer(record("r-1", "c-1", "Hand CORE-1 to Legion"));
    await started;
    expect(goingState().starting).toBeUndefined();
    expect(conversationTitle()).toBe("Hand CORE-1 to Legion");

    // An answer that ends in a question mark is no question to the person by itself.
    send("r-1", "reply", { labels: [], text: "Did you mean CORE-1 itself?", tools: [] });
    send("r-1", "end", ENDED_DONE);
    expect(asksPerson()).toBe(false);

    // The reply that ends the turn says the answer waits for the person.
    start.mockResolvedValueOnce(record("r-2", "c-1", "yes"));
    await startRun("yes", CONTEXT, "alice");
    send("r-2", "reply", {
      labels: [],
      text: "Which changes hand it over: a label or a status",
      tools: [],
      waits_for_person: true,
    });
    expect(asksPerson()).toBe(false);
    send("r-2", "end", ENDED_DONE);
    expect(asksPerson()).toBe(true);

    // So may the end frame.
    start.mockResolvedValueOnce(record("r-3", "c-1", "both"));
    await startRun("both", CONTEXT, "alice");
    send("r-3", "reply", { labels: [], text: "Both, or only the label", tools: [] });
    send("r-3", "end", { ...ENDED_DONE, waits_for_person: true });
    expect(asksPerson()).toBe(true);
    expect(goingState().conversation).toEqual(["r-1", "r-2", "r-3"]);
  } finally {
    start.mockRestore();
    stream.mockRestore();
  }
});

test("a run whose stream drops is reconnecting until the stream is answered again, a frame comes, or the stream ends or is given up", async () => {
  const start = spyOn(api, "createGoingRun")
    .mockResolvedValueOnce(record("r-1", "c-1", "watch the comet"))
    .mockResolvedValueOnce(record("r-2", "c-2", "watch the moon"));
  const streams = new Map<
    string,
    {
      readonly dropped: (dropped: boolean) => void;
      readonly frame: (name: string, data: unknown) => void;
      readonly ended: {
        readonly resolve: (value: undefined) => void;
        readonly reject: (error: Error) => void;
      };
    }
  >();
  const stream = spyOn(api, "streamGoingRunEvents").mockImplementation(
    (id, onFrame, _signal, onDropped) => {
      const ended = Promise.withResolvers<void>();
      streams.set(id, { dropped: (dropped) => onDropped?.(dropped), ended, frame: onFrame });
      return ended.promise;
    }
  );
  const reconnecting = (id: string) => goingState().runs.get(id)?.reconnecting;
  try {
    await startRun("watch the comet", CONTEXT, "alice");
    const comet = streams.get("r-1");
    expect(reconnecting("r-1")).toBe(false);

    comet?.dropped(true);
    expect(reconnecting("r-1")).toBe(true);
    comet?.dropped(false);
    expect(reconnecting("r-1")).toBe(false);

    comet?.dropped(true);
    comet?.frame("call", { kind: "read", label: "Reading CORE-1", step: 2, tool: "read" });
    expect(reconnecting("r-1")).toBe(false);

    // Given up: the run says its steps could not be read, and no longer that it reconnects.
    comet?.dropped(true);
    comet?.ended.reject(new Error("the run's stream kept ending before its run did"));
    await waitFor(() => expect(goingState().runs.get("r-1")?.unreadable).toBeDefined());
    expect(reconnecting("r-1")).toBe(false);

    // Ended while it waited to open the stream again.
    newConversation();
    await startRun("watch the moon", CONTEXT, "alice");
    const moon = streams.get("r-2");
    moon?.dropped(true);
    expect(reconnecting("r-2")).toBe(true);
    moon?.ended.resolve(undefined);
    await waitFor(() => expect(reconnecting("r-2")).toBe(false));
    expect(goingState().runs.get("r-2")?.unreadable).toBeUndefined();
  } finally {
    start.mockRestore();
    stream.mockRestore();
  }
});
