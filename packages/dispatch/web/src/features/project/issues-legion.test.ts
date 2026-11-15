import { expect, test } from "bun:test";
import type { LegionBoardFacts } from "../../api/types";
import { phaseHours, workPhaseIds, workPhaseLabel } from "./issues-legion";

const facts = (hours_by_phase: Record<string, number>): LegionBoardFacts =>
  ({ hours_by_phase }) as unknown as LegionBoardFacts;

test("Time per phase draws the work phases in the daemon's order and Other last", () => {
  expect(workPhaseIds).toEqual([
    "design",
    "planning",
    "implementing",
    "testing",
    "reviewing",
    "accepting",
    "merging",
    "awaiting_person",
    "held",
    "other",
  ]);
  expect(workPhaseLabel("other")).toBe("Other");
  const { phases, total } = phaseHours(
    facts({ other: 0.5, merging: 1, design: 2, implementing: 0 })
  );
  expect(phases).toEqual([
    ["design", 2],
    ["merging", 1],
    ["other", 0.5],
  ]);
  expect(total).toBe(3.5);
});

test("hours under Queue or a key this bundle does not know count in Other", () => {
  const { phases, total } = phaseHours(facts({ queue: 0.25, other: 0.5, rebasing: 1, testing: 2 }));
  expect(phases).toEqual([
    ["testing", 2],
    ["other", 1.75],
  ]);
  expect(total).toBe(3.75);
});
