import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { dispatchPayloadKind } from "@legion/contracts";
import { renderInbound } from "../delivery";
import { parseFrameJSON } from "../frame-json";
import {
  DISPATCH_PROPERTY_FRAMES,
  dispatchFramesDigest,
  generatedDispatchFrames,
} from "./dispatch-frame-generator";

// The plugin's side of #5192's property: of the generated Dispatch frames
// (dispatch-frame-generator.ts), the edge listener passes for a token of PRIV the ones
// packages/contracts/fixtures/dispatch-frame-property.json records
// (TestEdgeFrameVerdictOnGeneratedDispatchFrames writes them from the edge's own verdicts), and
// none of those may reach the session as a card that shows another project's event.
//
// The card is read as INFRA-764's review read it (Job legion-review5192-fz5wn): the frame
// rendered by renderInbound on the session's own agent subject, the card showing PUB when it holds
// "PUB" or "pub-1". The plugin before #5192 showed the whole payload of a Dispatch frame it read
// no event of (parseDispatchFrame's raw value, read by its own keys, parseFrameJSON); it now shows
// none of it, so the oracle adds that value back for such a frame, read the same way, and judges
// each frame as that plugin would have shown it. The edge's rule is then held to the bar alone,
// without the plugin's change behind it: the frames it passes show PUB in no card even to the
// earlier plugin.
//
// The oracle can fail: over the frames #5192's head 297f438ae2 passed (passed_at_297f438ae2, the
// same Go test run at that commit), it finds cards that show PUB, every one a payload the plugin
// read no event of - the 1,254 INFRA-764's review counted with #5199's reader, which leaves a
// "__proto__" key out, and 1,329 with the reader before it. The test asserts that there is at
// least one and prints how many, not either number, which moves with the plugin's code.

type PropertyFixture = {
  readonly seed: readonly [number, number];
  readonly frames: number;
  readonly sha256: string;
  readonly passed: readonly number[];
  readonly passed_at_297f438ae2: readonly number[];
};

const fixture = JSON.parse(
  readFileSync(
    new URL("../../../contracts/fixtures/dispatch-frame-property.json", import.meta.url),
    "utf8"
  )
) as PropertyFixture;

const session = "ses_v";
const own = `notifications.agent.${session}`;

/** The payload value the plugin before #5192 showed whole for a frame it read no event of, or
 *  undefined for a frame whose payload it read as an event. */
function rawShownPayload(frame: string): unknown {
  const envelope = JSON.parse(frame) as { readonly payload?: unknown };
  if (typeof envelope.payload !== "string") return undefined;
  let value: unknown;
  try {
    value = parseFrameJSON(envelope.payload);
  } catch {
    return envelope.payload;
  }
  return dispatchPayloadKind(value) === "event" ? undefined : value;
}

/** Whether the earlier plugin's card of frame shows another project's event. */
function cardShowsPub(frame: string): boolean {
  let content: string;
  try {
    content = (renderInbound(frame, session, own) as { content: string }).content;
  } catch {
    return false;
  }
  const raw = rawShownPayload(frame);
  if (raw !== undefined) content += `\n${JSON.stringify(raw)}`;
  return /PUB/.test(content) || /pub-1/.test(content);
}

describe("the Dispatch frames the edge passes", () => {
  const frames = generatedDispatchFrames();

  test("are the frames the edge's side judged", () => {
    expect(fixture.seed).toEqual([5192, 11]);
    expect(frames.length).toBe(DISPATCH_PROPERTY_FRAMES);
    expect(fixture.frames).toBe(DISPATCH_PROPERTY_FRAMES);
    expect(dispatchFramesDigest(frames)).toBe(fixture.sha256);
  });

  test("control: over the frames 297f438ae2 passed, the oracle finds cards that show PUB", () => {
    const showing = fixture.passed_at_297f438ae2.filter((index) =>
      cardShowsPub(frames[index] as string)
    );
    console.log(
      `dispatch-frame property: 297f438ae2 passed ${fixture.passed_at_297f438ae2.length} frames, ${showing.length} of whose cards show PUB`
    );
    expect(showing.length).toBeGreaterThan(0);
    expect(showing.every((index) => rawShownPayload(frames[index] as string) !== undefined)).toBe(
      true
    );
  }, 120_000);

  test("control: the token's project beside another project's nested event shows PUB in the card", () => {
    // The edge's side withholds this frame (TestEdgeFrameVerdictPassesADispatchFrameOnlyOfAListedProject,
    // "the token's project beside another project's nested event, which the plugin reads as no event").
    const pubEvent = JSON.stringify({
      issue_key: "PUB-1",
      project: "PUB",
      type: "comment.created",
      actor: { kind: "user", id: "alice" },
      payload: { id: "c-1", body: "PUB's comment" },
    });
    const frame = JSON.stringify({
      source: "dispatch",
      event_id: "e-1",
      topic: own,
      payload: `{"event":${pubEvent},"project":"PRIV","issue_key":"PRIV-1"}`,
    });
    expect(cardShowsPub(frame)).toBe(true);
  });

  test("show another project's event in no card", () => {
    const showing = fixture.passed.filter((index) => cardShowsPub(frames[index] as string));
    console.log(
      `dispatch-frame property: the edge passes ${fixture.passed.length} frames, ${showing.length} of whose cards show PUB`
    );
    expect(fixture.passed.length).toBeGreaterThan(0);
    expect(showing.map((index) => frames[index])).toEqual([]);
  }, 120_000);
});
