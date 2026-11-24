import { describe, expect, mock, test } from "bun:test";
import type { DispatchPayloadReading } from "@legion/contracts";
import * as contracts from "@legion/contracts";

// renderInbound reads every Dispatch payload through readDispatchPayload, the one function the
// edge listener's verdict is held to (fixtures/dispatch-frame-verdicts.json and
// TestDispatchPayloadIsEventAgreesWithTheHostsReading). This file wraps that function, so a
// renderInbound that read the payload with a copy of its own - which the fixture would not hold -
// fails here: the wrapper sees no call, and the reading it is made to return does not decide the
// card.
const calls: unknown[] = [];
let forced: DispatchPayloadReading | undefined;
// A copy taken before the module is replaced, so the replacement calls the real reader rather than
// itself.
const real = { ...contracts };
mock.module("@legion/contracts", () => ({
  ...real,
  readDispatchPayload: (value: unknown): DispatchPayloadReading => {
    calls.push(value);
    return forced ?? real.readDispatchPayload(value);
  },
}));
// Imported after mock.module on purpose: a static import would bind delivery.ts to the real
// reader before the wrapper is in place, which is the module-loading boundary this test exercises.
const { renderInbound } = await import("../delivery");

const session = "ses_v";
const own = `notifications.agent.${session}`;
const event = {
  issue_key: "PRIV-1",
  project: "PRIV",
  type: "comment.created",
  actor: { kind: "user", id: "alice" },
  payload: { id: "c-1", body: "PRIV's comment" },
};
const frame = (payload: unknown) =>
  JSON.stringify({
    source: "dispatch",
    event_id: "e-1",
    topic: own,
    payload: JSON.stringify(payload),
  });

describe("renderInbound reads a Dispatch payload through readDispatchPayload", () => {
  test("it calls the reader once with the parsed payload, and shows the event it reads", () => {
    calls.length = 0;
    forced = undefined;
    const content = (renderInbound(frame(event), session, own) as { content: string }).content;
    expect(calls).toEqual([event]);
    expect(content).toContain("PRIV's comment");
    expect(content).not.toContain("unrecognised");
  });

  test("the reader's verdict decides the card: an event it reads as raw is shown as unrecognised", () => {
    calls.length = 0;
    forced = { kind: "raw", targeted: false };
    try {
      const content = (renderInbound(frame(event), session, own) as { content: string }).content;
      expect(calls).toHaveLength(1);
      expect(content).toContain("unrecognised");
      expect(content).not.toContain("PRIV's comment");
    } finally {
      forced = undefined;
    }
  });
});
