import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { settle } from "../../__tests__/fake-clock";
import { answerPriorityPatches } from "../../__tests__/send-answers";
import { ApiError, api } from "../../api/client";
import { prioritiesQuery } from "../../api/queries";
import type { Priorities, Priority } from "../../api/types";
import {
  expectedText,
  pressedText,
  settledText,
  type TextChain,
  usePriorityWrites,
} from "./priority-writes";

/** The id the next chain a test starts is given. */
let fresh = 0;

/** A press as the hook makes it: what the write names as `expected`, and the chain after it. */
function press(chain: TextChain | undefined, text: string, held: string) {
  fresh += 1;
  return { chain: pressedText(chain, text, held, fresh), expected: expectedText(chain, held) };
}

/** The chain after one of its writes settled, which a test knows is still there. */
function settledChain(chain: TextChain, refused: boolean): TextChain {
  const next = settledText(chain, refused);
  if (next === undefined) throw new Error("the chain ended");
  return next;
}

test("the first write names the text the page reads", () => {
  expect(press(undefined, "One", "Old").expected).toBe("Old");
});

test("a write pressed while one is out names the text of the one out, which the server holds by its turn", () => {
  const first = press(undefined, "One", "Old");
  const second = press(first.chain, "Two", "Old");
  expect(second.expected).toBe("One");
  // And a third, the second still out after the first landed, names the second's.
  expect(press(settledChain(second.chain, false), "Three", "Old").expected).toBe("Two");
});

test("once the write lands and the page's read has not moved, the next names the text written", () => {
  const first = press(undefined, "One", "Old");
  expect(press(settledChain(first.chain, false), "Two", "Old").expected).toBe("One");
});

test("a read catching up with the writes part way names the last text, and one that caught up ends the chain", () => {
  const first = press(undefined, "One", "Old");
  const second = press(first.chain, "Two", "Old");
  const landed = settledChain(settledChain(second.chain, false), false);
  // The read shows the first write's answer, the second's not yet: the server holds the second.
  const partWay = press(landed, "Three", "One");
  expect(partWay.expected).toBe("Two");
  expect(partWay.chain.id).toBe(landed.id);
  // The read shows the second's answer: the next write starts a chain from it.
  const caughtUp = press(landed, "Three", "Two");
  expect(caughtUp.expected).toBe("Two");
  expect(caughtUp.chain.id).not.toBe(landed.id);
  expect(caughtUp.chain.known).toEqual(["Two", "Three"]);
});

test("once the read moves to another person's text, the next write names that text", () => {
  const first = press(undefined, "One", "Old");
  const landed = settledChain(first.chain, false);
  expect(press(landed, "Two", "Written elsewhere").expected).toBe("Written elsewhere");
});

test("a read showing another person's text while a write is out starts a chain from that text", () => {
  const first = press(undefined, "One", "Old");
  // Another person's text reached the page before "One" was answered: the server holds it, and
  // "One", which names "Old", is refused. A write naming "One" would be refused as well.
  const over = press(first.chain, "Mine", "Written elsewhere");
  expect(over.expected).toBe("Written elsewhere");
  expect(over.chain.id).not.toBe(first.chain.id);
  expect(over.chain.known).toEqual(["Written elsewhere", "Mine"]);
});

test("a read that caught up ends the chain: a text read before it is then another person's", () => {
  const first = press(undefined, "One", "Old");
  // The read caught up with the first write before the second was pressed.
  const second = press(settledChain(first.chain, false), "Two", "One");
  expect(second.expected).toBe("One");
  // Another person writes "Old" again once the second landed: the server holds it, and a write
  // naming "Two" would be refused each time it was pressed.
  expect(press(settledChain(second.chain, false), "Three", "Old").expected).toBe("Old");
});

test("a refused write breaks the chain: the next write names the text read, whether or not one is out", () => {
  const first = press(undefined, "One", "Old");
  const second = press(first.chain, "Two", "Old");
  // The first is refused while the second is out. The second names "One", which the server does
  // not hold, so it is refused too: the next write goes after it naming what the page reads.
  const refused = settledChain(second.chain, true);
  expect(press(refused, "Three", "Written elsewhere").expected).toBe("Written elsewhere");
  expect(press(refused, "Three", "Old").expected).toBe("Old");
  expect(press(refused, "Three", "Old").chain.id).not.toBe(refused.id);
  expect(settledText(refused, false)).toBeUndefined();
  expect(press(settledText(refused, false), "Three", "Written elsewhere").expected).toBe(
    "Written elsewhere"
  );
  // And one refused with nothing else out ends it at once.
  expect(settledText(first.chain, true)).toBeUndefined();
});

const BODY: Priorities = {
  priorities: [],
  work_under_no_priority: { by_project: null, count: 0, issues: null },
  priority_work_parked: { by_priority: null, count: 0, issues: null },
};

const SAVED: Priority = {
  id: "p1",
  title: "One",
  description: "",
  owner: "",
  rank: "n",
  state: "live",
  made_at: "2026-11-10T09:00:00Z",
  made_by: { kind: "user", id: "alice" },
};

const CHANGED = new ApiError(409, {
  code: "PRIORITY_CHANGED",
  error: "the title was written meanwhile",
});

const restores: (() => void)[] = [];
afterEach(() => {
  for (const restore of restores.splice(0)) restore();
  localStorage.clear();
});

/**
 * A page's writes of priority `p1`'s title, beside the page's read of the priorities: each write
 * and each read is answered only when the test says, in the order it went out. `sent` is what
 * each write that went out changed.
 */
function pageWrites() {
  const whoami = { event_head: "0", kind: "user" as const, login: "alice" };
  const viewer = spyOn(api, "whoAmI").mockResolvedValue(whoami);
  const reads: { resolve: (body: Priorities) => void }[] = [];
  const listing = spyOn(api, "listPriorities").mockImplementation(() => {
    const read = Promise.withResolvers<Priorities>();
    reads.push(read);
    return read.promise;
  });
  const answers: { reject: (refusal: Error) => void; resolve: (saved: Priority) => void }[] = [];
  const patches = answerPriorityPatches().mockImplementation(() => {
    const answer = Promise.withResolvers<Priority>();
    answers.push(answer);
    return answer.promise;
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["whoami"], whoami);
  const hook = renderHook(
    () => {
      useQuery(prioritiesQuery());
      return usePriorityWrites();
    },
    {
      wrapper: ({ children }: { children: ReactNode }) =>
        createElement(QueryClientProvider, { client }, children),
    }
  );
  restores.push(() => {
    hook.unmount();
    client.clear();
    for (const spy of [viewer, listing, patches]) spy.mockRestore();
  });
  const sent = () => patches.mock.calls.map(([, input]) => input);
  return {
    /** Lands or refuses the write that went out `index`th. */
    answer: async (index: number, refused: boolean) => {
      await waitFor(() => expect(answers.length).toBeGreaterThan(index));
      await act(async () => {
        if (refused) answers[index]?.reject(CHANGED);
        else answers[index]?.resolve(SAVED);
        await settle();
      });
    },
    /** Answers the page's `index`th read of the priorities, its first being the page's own. */
    read: async (index: number) => {
      await waitFor(() => expect(reads.length).toBeGreaterThan(index));
      await act(async () => {
        reads[index]?.resolve(BODY);
        await settle();
      });
    },
    /** The reader writes `title` where the page reads `held`. */
    rename: (title: string, held: string) =>
      act(() => hook.result.current.rename("p1", title, held, "Rename the priority")),
    /** What the `index`th write to go out changed, once it went. */
    sentAt: async (index: number) => {
      await waitFor(() => expect(sent().length).toBeGreaterThan(index));
      return sent()[index];
    },
  };
}

test("a write pressed over another person's text starts a chain of its own, which a write still out on the old one leaves alone", async () => {
  const { answer, read, rename, sentAt } = pageWrites();
  await read(0);
  rename("One", "Old");
  expect(await sentAt(0)).toEqual({ expected: { title: "Old" }, title: "One" });
  // Another person's "Bob" reaches the page before "One" is answered; the reader writes over it.
  rename("Mine", "Bob");
  // "One" is refused, since the server holds "Bob": that is what "Mine" names.
  await answer(0, true);
  expect(await sentAt(1)).toEqual({ expected: { title: "Bob" }, title: "Mine" });
  // The refusal was the old chain's: the next write goes after "Mine", and names it.
  rename("Mine, again", "Bob");
  await answer(1, false);
  expect(await sentAt(2)).toEqual({ expected: { title: "Mine" }, title: "Mine, again" });
});

test("a chain whose writes all landed ends once the page has read them: a text of theirs read again is another person's", async () => {
  const { answer, read, rename, sentAt } = pageWrites();
  await read(0);
  rename("One", "Old");
  await answer(0, false);
  // The refresh that landing started answers. Later another person writes "Old" back, and the
  // reader writes over it: the server holds "Old", and a write naming "One" would be refused.
  await read(1);
  rename("Two", "Old");
  expect(await sentAt(1)).toEqual({ expected: { title: "Old" }, title: "Two" });
});

test("a write pressed before the page read what landed goes on the chain, and that read leaves it there", async () => {
  const { answer, read, rename, sentAt } = pageWrites();
  await read(0);
  rename("One", "Old");
  await answer(0, false);
  // The page has not read "One" yet: the next write names it.
  rename("Two", "Old");
  expect(await sentAt(1)).toEqual({ expected: { title: "One" }, title: "Two" });
  // The refresh "One" started answers while "Two" is out: the chain is "Two"'s now, and goes on.
  await read(1);
  rename("Three", "Old");
  await answer(1, false);
  expect(await sentAt(2)).toEqual({ expected: { title: "Two" }, title: "Three" });
});
