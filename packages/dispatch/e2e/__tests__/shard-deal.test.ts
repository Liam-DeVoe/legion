import { describe, expect, test } from "bun:test";

import {
  deal,
  type Group,
  groupsOf,
  type Leg,
  recordedTimes,
  type Table,
  tableText,
  weigh,
} from "../tools/shard-deal";

/** A group of a made-up listing: `tests` tests of `file` in `project`. */
function group(file: string, tests: number, project = "chromium"): Group {
  return { line: `[${project}] › ${file}`, name: `[${project}] › e2e/${file}`, tests };
}

const table: Table = {
  groups: {
    "[chromium] › e2e/agent-view.e2e.ts": { seconds: 250, tests: 40 },
    "[chromium] › e2e/doc.e2e.ts": { seconds: 70, tests: 16 },
    "[chromium] › e2e/going-bar.e2e.ts": { seconds: 40, tests: 8 },
    "[chromium] › e2e/inbox.e2e.ts": { seconds: 180, tests: 50 },
    "[chromium] › e2e/issue-page-i.e2e.ts": { seconds: 420, tests: 45 },
    "[chromium] › e2e/issue-page-j.e2e.ts": { seconds: 300, tests: 30 },
    "[chromium] › e2e/owed.e2e.ts": { seconds: 0, tests: 3 },
    "[firefox-download-navigation] › e2e/shell.e2e.ts": { seconds: 6, tests: 1 },
    ...Object.fromEntries(
      [1, 2, 3, 4, 5, 6].map((n) => [
        `[chromium] › e2e/tie-${n}.e2e.ts`,
        { seconds: 30, tests: 10 },
      ])
    ),
  },
  medianTestSeconds: 3,
};

/** The listing: every group of the table but one, one held group listing half its tests, as a
 *  grep's subset does, and two files the table does not hold. */
const listed: Group[] = [
  group("agent-view.e2e.ts", 40),
  group("doc.e2e.ts", 16),
  group("going-bar.e2e.ts", 8),
  group("inbox.e2e.ts", 50),
  group("issue-page-i.e2e.ts", 45),
  group("issue-page-j.e2e.ts", 15),
  group("owed.e2e.ts", 3),
  group("shell.e2e.ts", 1, "firefox-download-navigation"),
  ...[1, 2, 3, 4, 5, 6].map((n) => group(`tie-${n}.e2e.ts`, 10)),
  group("new-file.e2e.ts", 10),
  group("new-file.e2e.ts", 2, "iphone"),
];
const weighed = listed.map((each) => weigh(each, table));
const heaviestGroup = Math.max(...weighed.map((each) => each.seconds));

/** Each leg's group names, in the order the leg holds them. */
function namesOf(legs: readonly Leg[]): string[][] {
  return legs.map((leg) => leg.groups.map((each) => each.name));
}

test("a group weighs its recorded seconds scaled to the tests listed, and an unrecorded one the median for each test", () => {
  const by = new Map(weighed.map((each) => [each.name, each]));
  expect(by.get("[chromium] › e2e/issue-page-i.e2e.ts")).toMatchObject({
    recorded: true,
    seconds: 420,
  });
  expect(by.get("[chromium] › e2e/issue-page-j.e2e.ts")).toMatchObject({
    recorded: true,
    seconds: 150,
  });
  expect(by.get("[chromium] › e2e/new-file.e2e.ts")).toMatchObject({
    recorded: false,
    seconds: 30,
  });
  expect(by.get("[iphone] › e2e/new-file.e2e.ts")).toMatchObject({ recorded: false, seconds: 6 });
});

describe.each([2, 4])("dealt to %d legs", (legs) => {
  const dealt = deal(weighed, legs);

  test("every group is in exactly one leg", () => {
    expect(dealt).toHaveLength(legs);
    const placed = dealt.flatMap((leg) => leg.groups.map((each) => each.name)).sort();
    expect(placed).toEqual(listed.map((each) => each.name).sort());
  });

  test("the deal is the same on every call, in whatever order the listing names the groups", () => {
    expect(namesOf(deal(weighed, legs))).toEqual(namesOf(dealt));
    expect(namesOf(deal([...weighed].reverse(), legs))).toEqual(namesOf(dealt));
    expect(namesOf(deal([...weighed.slice(7), ...weighed.slice(0, 7)], legs))).toEqual(
      namesOf(dealt)
    );
  });

  test("the heaviest leg is within the lightest leg plus the heaviest group", () => {
    for (const leg of dealt) {
      expect(leg.seconds).toBeCloseTo(
        leg.groups.reduce((sum, each) => sum + each.seconds, 0),
        6
      );
    }
    const seconds = dealt.map((leg) => leg.seconds);
    expect(Math.max(...seconds)).toBeLessThanOrEqual(Math.min(...seconds) + heaviestGroup);
  });
});

test("a listing's rows group by project and file, named as the table and as --test-list read them", () => {
  const rows = [
    { file: "doc.e2e.ts", project: "chromium" },
    { file: "doc.e2e.ts", project: "chromium" },
    { file: "doc.e2e.ts", project: "iphone" },
    { file: "stub/inbox.stub.e2e.ts", project: "stub" },
  ];
  expect(groupsOf(rows, "e2e")).toEqual([
    { line: "[chromium] › doc.e2e.ts", name: "[chromium] › e2e/doc.e2e.ts", tests: 2 },
    { line: "[iphone] › doc.e2e.ts", name: "[iphone] › e2e/doc.e2e.ts", tests: 1 },
    {
      line: "[stub] › stub/inbox.stub.e2e.ts",
      name: "[stub] › e2e/stub/inbox.stub.e2e.ts",
      tests: 1,
    },
  ]);
});

test("the record mode reads passed, failed, skipped and retried lines into seconds", () => {
  const legOne = [
    "Running 6 tests using 4 workers, shard 1 of 4",
    "✓ 3170 modules transformed.",
    "  ✓    1 [chromium] › e2e/a.e2e.ts:10:1 › opens the page @pr (5.5s)",
    "  ✓    2 [chromium] › e2e/a.e2e.ts:20:3 › a block › closes it (727ms)",
    "  ✘    3 [chromium] › e2e/b.e2e.ts:5:1 › fails, then passes (4.6s)",
    "  ✓    4 [chromium] › e2e/b.e2e.ts:5:1 › fails, then passes (retry #1) (4.7s)",
    "  -    5 [chromium] › e2e/b.e2e.ts:30:1 › is skipped @owed",
    "  ✓    6 [iphone] › e2e/a.e2e.ts:10:1 › opens the page @pr (1.3m)",
    "  1) [chromium] › e2e/b.e2e.ts:5:1 › fails, then passes ─────────────────",
    "  1 flaky",
    "  5 passed (8.9m)",
  ].join("\n");
  // A line of `gh run view --log`: the job, the step and a timestamp before the reporter's line.
  const legTwo =
    "full / shard (2)\tBrowser-test dispatch dashboard (shard 2 of 4)\t" +
    "2026-11-22T09:50:01.1234567Z   ✓    1 [chromium] › e2e/c.e2e.ts:8:1 › reads the list (2.0s)\n";
  const recorded = recordedTimes([legOne, legTwo]);
  expect(recorded).toEqual({
    groups: {
      "[chromium] › e2e/a.e2e.ts": { seconds: 6.2, tests: 2 },
      "[chromium] › e2e/b.e2e.ts": { seconds: 9.3, tests: 2 },
      "[chromium] › e2e/c.e2e.ts": { seconds: 2, tests: 1 },
      "[iphone] › e2e/a.e2e.ts": { seconds: 78, tests: 1 },
    },
    // The tests that ran - 0.727, 2.0, 5.5, 9.3 and 78 s - and not the skipped one.
    medianTestSeconds: 5.5,
  });
  expect(recordedTimes([legOne, legOne, legTwo])).toEqual(recorded);
  expect(JSON.parse(tableText(recorded))).toEqual(recorded);
  expect(() => recordedTimes(["no reporter line here"])).toThrow(
    "the logs hold no line of the list reporter"
  );
});
