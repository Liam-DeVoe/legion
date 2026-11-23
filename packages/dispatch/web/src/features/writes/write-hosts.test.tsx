import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type RenderResult, render, waitFor } from "@testing-library/react";

import { api } from "../../api/client";
import { PendingWritesHost } from "./write-hosts";
import { kindsRegistered, plainWrite, writesOf } from "./write-store";

// The store's take-up of what a reload kept waits for every kind of write, a chunk of its own
// (`kinds.ts`), and its own ask for that chunk can fail with no reload to follow: the page being
// left, the reader staying at the leave prompt that the writes the reload kept
// (`WriteStorage.load`) or a write pressed meanwhile open, or the tab's recovery reloads spent.
// The writes host asks for the same chunk once the page has drawn, and its answer ends every
// take-up still waiting (`prefetchWriteKinds`, `kindsRegistered`), so the write pressed meanwhile
// goes out with no second press to ask again. The write is one the indicator does not show
// (`composer`): the host draws nothing, and the row reads the store alone.
//
// Each run holds nothing over into the next, which `bun test --rerun-each` runs in the same
// process with the same modules (the repeated unit lane, dispatch-unit-repeat.yaml): `kinds.ts`
// is evaluated once in that process, so only the first run of a file sees its evaluation end the
// take-up, and every later run sees the host's answered load end it, which is what the second row
// below holds on purpose. The press is handled from the start, so the store's clear between rows
// (`__tests__/setup.ts`) rejecting a press a failed run left out reaches no one as an unhandled
// rejection, and `kindsRegistered` in `finally` ends a take-up a failed run left waiting.
async function takeUpEndsWithTheHost(): Promise<void> {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  client.setQueryData(["whoami"], { event_head: "0", kind: "user", login: "alice" });
  const send = spyOn(api, "send").mockResolvedValue(null as never);
  const writes = writesOf(client);
  let asked = 0;
  writes.resume("alice", () => {
    asked += 1;
    return Promise.reject(new TypeError("Failed to fetch dynamically imported module"));
  });
  const pressed = writes.run({
    detail: null,
    kind: plainWrite,
    label: "A comment on CORE-1",
    presentation: "composer",
    request: {
      body: { body: "On it" },
      method: "POST",
      path: "/api/v1/issues/CORE-1/comments",
    },
  });
  pressed.catch(() => undefined);
  let view: RenderResult | undefined;
  try {
    for (let turn = 0; turn < 20; turn += 1) await Promise.resolve();
    expect(asked).toBe(1);
    expect(send, "the take-up whose ask failed still holds the write").not.toHaveBeenCalled();

    view = render(
      <QueryClientProvider client={client}>
        <PendingWritesHost />
      </QueryClientProvider>
    );
    await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
    expect(send.mock.calls[0]?.[0]).toMatchObject({ path: "/api/v1/issues/CORE-1/comments" });
    expect(await pressed).toBeNull();
    expect(asked, "the take-up's own load was not asked for again").toBe(1);
  } finally {
    view?.unmount();
    send.mockRestore();
    kindsRegistered();
  }
}

test("the writes host's fetch of the kinds of write ends a take-up whose own ask failed, and the write pressed meanwhile goes out", async () => {
  await takeUpEndsWithTheHost();
});

// Another load brought the kinds in before this take-up began - on the sign-in page, the
// kept-writes host's - so their evaluation, which ends the take-ups waiting at that moment, came
// before this one waited. Only the writes host's answered load can end it then. The import is a
// run-time one on purpose: a static import would evaluate `kinds.ts` as the file loads, before
// the first row, which could then no longer see an evaluation end its take-up.
test("a take-up whose own ask failed ends with the writes host's fetch also when another load brought the kinds in before it began", async () => {
  await import("./kinds");
  await takeUpEndsWithTheHost();
});
