import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";

import { answerRoute } from "../../__tests__/send-answers";
import { api } from "../../api/client";
import type { GoingUndoAnswer } from "../../api/types";
import { Writes } from "../writes/write-store";
import { forgetTurns, undoGoingTurn, undoNewestGoingTurn, useLastUndo } from "./going-turns";

// The bar's turns as the store holds them: the undo key, which undoes the newest undoable run
// Dispatch holds, and which follows a turn's undo already out rather than sending a second.

afterEach(forgetTurns);

function signedIn(): QueryClient {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  client.setQueryData(["whoami"], { event_head: "0", kind: "user", login: "alice" });
  return client;
}

const RUN = "8b0c8f4e-5a7e-4f43-9a5c-1a2b3c4d5e6f";

test("the undo key undoes the newest undoable run Dispatch holds, where this page has no write of the bar out", async () => {
  const writes = new Writes(signedIn());
  const runs = spyOn(api, "myGoingRuns").mockResolvedValue({
    limit: 1,
    offset: 0,
    rows: [
      {
        author_login: "alice",
        context: null,
        created_at: "2026-11-12T10:00:00Z",
        decision: null,
        finished: true,
        finished_at: "2026-11-12T10:00:02Z",
        hits: [],
        id: RUN,
        issues: ["CORE-1"],
        proposal: null,
        structured_output: null,
        undos: [],
        words: "start CORE-1",
        writes: [],
      },
    ],
    total: 1,
  });
  const undo = answerRoute<(run: string, input: unknown) => Promise<GoingUndoAnswer>>(
    "POST",
    "/api/v1/going/runs/*/undo"
  );
  undo.mockResolvedValue({
    lines: [
      {
        action: "dispatch_issue_update",
        fields: [{ field: "status", message: "status is in_review now", ok: false }],
        item: "CORE-1",
        now: { status: "in_review" },
        outcome: {
          code: "GOING_UNDO_CHANGED",
          message: "another writer changed it",
          ok: false,
          status: 409,
        },
        undo: "write_back",
        write_id: "write-0",
      },
    ],
  });
  try {
    await undoNewestGoingTurn(writes);

    expect(runs).toHaveBeenCalledWith({ limit: 1, undoable: true });
    expect(undo).toHaveBeenCalledWith(
      RUN,
      { reason: "Undone from the command bar: “start CORE-1”" },
      expect.anything()
    );
    // What the shell's corner reads, through the hook it reads it with.
    const hook = renderHook(() => useLastUndo());
    const said = hook.result.current;
    hook.unmount();
    expect(said?.state).toBe("undone");
    expect(said?.words).toBe("start CORE-1");
    expect(said?.lines[0]?.fields?.[0]?.message).toBe("status is in_review now");
  } finally {
    runs.mockRestore();
  }
});

test("the undo key pressed while a turn's Undo is out follows that undo, sends no second one, and works again once it settles", async () => {
  const writes = new Writes(signedIn());
  const held = Promise.withResolvers<GoingUndoAnswer>();
  const undo = answerRoute<(run: string, input: unknown) => Promise<GoingUndoAnswer>>(
    "POST",
    "/api/v1/going/runs/*/undo"
  );
  undo.mockImplementation(() => held.promise);
  const runs = spyOn(api, "myGoingRuns").mockResolvedValue({
    limit: 1,
    offset: 0,
    rows: [],
    total: 0,
  });
  const said = () => {
    const hook = renderHook(() => useLastUndo());
    const last = hook.result.current;
    hook.unmount();
    return last;
  };
  try {
    // The turn's own Undo, out and not yet answered.
    const button = undoGoingTurn(writes, RUN, "start the three CORE issues");
    await waitFor(() => expect(undo).toHaveBeenCalledTimes(1));

    // The key, pressed meanwhile: the shell follows the undo that is out, and nothing is sent.
    await undoNewestGoingTurn(writes);
    expect(undo).toHaveBeenCalledTimes(1);
    expect(said()).toMatchObject({ runId: RUN, state: "undoing" });

    held.resolve({ lines: [] });
    await button;
    expect(said()).toMatchObject({ runId: RUN, state: "undone" });

    // Settled, the key works again: it asks Dispatch for the newest undoable run.
    await undoNewestGoingTurn(writes);
    expect(runs).toHaveBeenCalledWith({ limit: 1, undoable: true });
    expect(said()).toMatchObject({ runId: "", state: "undone" });
    expect(undo).toHaveBeenCalledTimes(1);
  } finally {
    runs.mockRestore();
  }
});
