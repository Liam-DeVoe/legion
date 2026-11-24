import { describe, expect, test } from "bun:test";
import { Text } from "../word-rewrite/dispatch-api.ts";
import { countWord, indexOfByte, matches } from "../word-rewrite/pattern.ts";
import { fixture } from "./word-rewrite-fake.ts";

/** The pattern's finds and misses, as the measurement of dispatch://INFRA-2017/comment/6ef37a16
 *  names them. This file is one of the places the word may stand (R1, kind 4). */
describe("the word's pattern", () => {
  for (const found of ["park", "Parked", "PARK_CHILD", "isParked", "releasesParkedClaims"]) {
    test(`finds ${found}`, () => {
      expect(countWord(found)).toBe(1);
    });
  }

  for (const missed of ["spark", "Spark", "SPARK", "SPARK_HOME", "Genspark", "sparkline"]) {
    test(`does not find ${missed}`, () => {
      expect(countWord(missed)).toBe(0);
    });
  }

  test("finds each occurrence once, in order, inside names and across cases", () => {
    const text = "park_child parked; Spark is not; PARKING lot";
    expect(matches(text).map((match) => text.slice(match.start, match.end))).toEqual([
      "park",
      "park",
      "PARK",
    ]);
  });

  test("gives UTF-8 byte offsets after multi-byte text", () => {
    const text = "Café ✓ — the parked tree";
    const [match] = matches(text);
    expect(match?.start).toBe(13);
    expect(match?.byteStart).toBe(Buffer.byteLength("Café ✓ — the ", "utf8"));
    expect(match?.byteEnd).toBe((match?.byteStart ?? 0) + 4);
    expect(indexOfByte(text, match?.byteStart ?? 0)).toBe(match?.start);
  });

  test("its byte offsets fall inside the blocks' from and to of text-blocks.json", () => {
    const text = Text.parse(fixture("text-blocks.json"));
    const bytes = Buffer.from(text.markdown, "utf8");
    const blocks = text.blocks ?? [];
    const found = matches(text.markdown);
    expect(found.length).toBeGreaterThan(0);
    for (const match of found) {
      // The bytes Dispatch's range names are the bytes of the match.
      expect(bytes.subarray(match.byteStart, match.byteEnd).toString("utf8").toLowerCase()).toBe(
        "park"
      );
      const holding = blocks.filter(
        (block) => block.from <= match.byteStart && match.byteEnd <= block.to
      );
      expect(holding.length).toBeGreaterThan(0);
    }
    // The first occurrence follows multi-byte characters, and its block starts at byte 0.
    const first = found[0];
    expect(first?.byteStart).toBeGreaterThan(first?.start ?? 0);
    expect(blocks.find((block) => block.from === 0)?.type).toBe("paragraph");
  });
});
