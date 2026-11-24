import { describe, expect, test } from "bun:test";
import type { DedupeIdentity } from "@legion/envoy-client/delivery";
import { heldStoredCards, STORED_CARD_ENTRY, storedCardRecord } from "./missed-messages";

/** A Dispatch frame: its dedupe key names its event, so the ledger knows it by both. */
const dispatchFrame = (n: number): DedupeIdentity => ({
  event_id: `event-${n}`,
  dedupe_key: `dispatch-${n}`,
  source: "dispatch",
});

/** The transcript entry that records a card held for a settled host, with `data` as written. */
const heldEntry = (data: unknown) => ({ type: "custom", customType: STORED_CARD_ENTRY, data });

// A held card's record says how the card is handed (`UnwrittenFrames`): a `low` card as a stored
// card, a late BTW as an inert card, a steer that starts no turn. A restore hands each as its
// record says, so a late BTW never goes into the next turn's context as a stored card.
describe("the record of a card held for a settled host", () => {
  test("says how the card is handed, and a restore reads it back so", () => {
    const [low, late] = [dispatchFrame(1), dispatchFrame(2)];
    const stored = { identity: low, content: "low", dispatchMessageId: undefined };
    const inert = {
      identity: late,
      content: [{ type: "text" as const, text: "late BTW" }],
      dispatchMessageId: "message-2",
    };

    expect(storedCardRecord({ ...stored, deliverAs: "stored" })).toEqual({
      identity: low,
      content: "low",
      deliver_as: "stored",
    });
    expect(storedCardRecord({ ...inert, deliverAs: "inert" })).toEqual({
      identity: late,
      content: [{ type: "text", text: "late BTW" }],
      dispatch_message_id: "message-2",
      deliver_as: "inert",
    });
    expect(
      heldStoredCards([
        heldEntry(storedCardRecord({ ...stored, deliverAs: "stored" })),
        heldEntry(storedCardRecord({ ...inert, deliverAs: "inert" })),
      ])
    ).toEqual([
      { ...stored, deliverAs: "stored" },
      { ...inert, deliverAs: "inert" },
    ]);
  });

  // A build before the inert record held only stored cards, and its record names no hand-over.
  test("that an earlier build wrote, with no deliver_as, reads as a stored card", () => {
    const low = dispatchFrame(1);

    expect(heldStoredCards([heldEntry({ identity: low, content: "low" })])).toEqual([
      { identity: low, content: "low", dispatchMessageId: undefined, deliverAs: "stored" },
    ]);
  });

  test("holds nothing again once the host wrote the inert card it records", () => {
    const late = dispatchFrame(1);

    expect(
      heldStoredCards([
        heldEntry(
          storedCardRecord({
            identity: late,
            content: "late BTW",
            dispatchMessageId: undefined,
            deliverAs: "inert",
          })
        ),
        {
          type: "custom_message",
          customType: "envoy-message",
          content: "late BTW",
          details: { event_id: "event-1", handOver: "inert" },
        },
      ])
    ).toEqual([]);
  });
});
