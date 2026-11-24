import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type RenderResult, render, waitFor } from "@testing-library/react";

import { keepWrite, keyMinted } from "../../__tests__/kept-sends-harness";
import { api } from "../../api/client";
import { PendingWritesHost } from "./write-hosts";
import { kindsRegistered, plainWrite, writesOf } from "./write-store";

// The store's take-up of what a reload kept waits for every kind of write, a chunk of its own
// (`kinds.ts`), and its own ask for that chunk can fail with no reload to follow: the page being
// left, the reader staying at the leave prompt that the writes the reload kept
// (`WriteStorage.load`) or a write pressed meanwhile open, or the tab's recovery reloads spent.
// The writes host asks for the same chunk once the page has drawn, and the evaluation of
// `kinds.ts` that its load brings ends every take-up still waiting (`kindsRegistered`), so the
// write pressed meanwhile goes out with no second press to ask again. The write is one the
// indicator does not show (`composer`): the host draws nothing, and the row reads the store alone.
// The tab keeps a write of alice's from before the reload, one the indicator does not show either,
// since a take-up with nothing kept for the person ends at once and waits for nothing: it goes
// out first, and the press behind it.
//
// Each run holds nothing over into the next, which `bun test --rerun-each` runs in the same
// process with the same modules (the repeated unit lane, dispatch-unit-repeat.yaml): `kinds.ts`
// is evaluated once in that process, so only the first run of a file sees its evaluation end the
// take-up. Every later run, and the second row below, finds `kinds.ts` evaluated before its
// take-up began, while the take-up's own ask is the row's and fails. The app has no such take-up:
// its ask is the host's own load (`loadWriteKinds`), answered from what is in once the kinds are,
// so in the app a take-up that begins after them ends through that ask. In these runs only the
// host's answered load ends it (`prefetchWriteKinds`), which keeps the row's end from turning on
// which load evaluated the module. The press is handled from the start, so the store's clear
// between rows (`__tests__/setup.ts`) rejecting a press a failed run left out reaches no one as an
// unhandled rejection. `kindsRegistered` in `finally` ends a take-up a failed run left waiting,
// and the send is restored only some turns after it, so that a write the take-up lets go meets
// the stub rather than the network guard of a later row or run.
async function takeUpEndsWithTheHost(): Promise<void> {
  const kept = keyMinted(Date.now());
  keepWrite(window.sessionStorage, kept, { presentation: "composer" });
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
    await waitFor(() => expect(send).toHaveBeenCalledTimes(2));
    expect(send.mock.calls.map(([request]) => request.path)).toEqual([
      "/api/v1/issues/CORE-1",
      "/api/v1/issues/CORE-1/comments",
    ]);
    expect(await pressed).toBeNull();
    expect(asked, "the take-up's own load was not asked for again").toBe(1);
  } finally {
    view?.unmount();
    kindsRegistered();
    for (let turn = 0; turn < 20; turn += 1) await Promise.resolve();
    send.mockRestore();
  }
}

test("the writes host's fetch of the kinds of write ends a take-up whose own ask failed, and the write pressed meanwhile goes out", async () => {
  await takeUpEndsWithTheHost();
});

// `kinds.ts` was evaluated before this take-up began - by the first row's host load when the
// file runs in order, or by this import when this row runs first or alone - so its evaluation
// ended the take-ups waiting at that moment, before this one waited. This take-up's own ask is the
// test's loader, which fails although the kinds are in, so only the writes host's answered load
// can end it.
// The import is a run-time one on purpose: a static import would evaluate `kinds.ts` as the file
// loads, before the first row, which could then no longer see an evaluation end its take-up.
test("a take-up whose own ask failed ends with the writes host's fetch also when another load brought the kinds in before it began", async () => {
  await import("./kinds");
  await takeUpEndsWithTheHost();
});
