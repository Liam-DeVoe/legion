/** The nightly's half of the quarantine, `e2e/tools/quarantine-check.ts`, driven through its pure
 *  part with the planted history of `fixtures/quarantine-history.fixture.ts`: what `decide`
 *  names, and the lines `report` gives for each. Nothing here reads GitHub or the clock; the
 *  moment of the reading is the fixture's. */
import { expect, test } from "bun:test";

import { entryLines } from "../quarantine";
import { decide, report } from "../tools/quarantine-check";
import {
  eighteenDays,
  fourteenDays,
  history,
  picturesEntry,
  readAt,
} from "./fixtures/quarantine-history.fixture";

test("a row flaky in two runs of the fourteen days is named, with its runs and the lines to add", () => {
  const decision = decide(history, [], readAt);
  expect(decision.toQuarantine.map(({ row, runs }) => [row, runs.map((run) => run.id)])).toEqual([
    [{ file: "pictures.e2e.ts", project: "chromium", title: "a picture uploads @pr" }, [101, 102]],
  ]);
  expect(decision.overdue).toEqual([]);
  const { annotations, failed, summary } = report(decision, readAt);
  expect(failed).toBe(true);
  expect(summary).toContain(
    "- `pictures.e2e.ts [chromium] a picture uploads @pr`, flaky in 2 runs: " +
      "[run 101](https://github.com/cybermonklabs/legion/actions/runs/101) of 2026-11-08 " +
      "(schedule), [run 102](https://github.com/cybermonklabs/legion/actions/runs/102) of " +
      "2026-11-12 (merge_group)."
  );
  const lines = summary.slice(summary.indexOf("  ```ts") + 1, summary.indexOf("  ```ts") + 8);
  expect(lines).toEqual([
    "    {",
    '      file: "pictures.e2e.ts",',
    '      project: "chromium",',
    '      title: "a picture uploads @pr",',
    '      issue: "dispatch://<KEY>",',
    '      added: "2026-11-19",',
    "    },",
  ]);
  expect(annotations).toEqual([
    "::error title=dispatch-browser quarantine::pictures.e2e.ts [chromium] a picture uploads " +
      "@pr was flaky in 2 runs of 14 days and is not quarantined: " +
      "https://github.com/cybermonklabs/legion/actions/runs/101 " +
      "https://github.com/cybermonklabs/legion/actions/runs/102",
  ]);
});

test("the same history names nothing once that row is quarantined", () => {
  const decision = decide(history, [picturesEntry], readAt);
  expect(decision.toQuarantine).toEqual([]);
  expect(decision.overdue).toEqual([]);
  const { annotations, failed, summary } = report(decision, readAt);
  expect(failed).toBe(false);
  expect(annotations).toEqual([]);
  expect(summary).toContain("No row was flaky in 2 runs that the quarantine does not hold.");
  expect(summary).toContain("No entry has been held longer than 14 days.");
});

// `keyed-writes.e2e.ts`'s row is named by two attempts of run 102 and by no other run, and
// `mermaid.e2e.ts`'s by run 103 and by a nightly 18 days before the reading: each is one run.
test("a row flaky in one run is not named, however many attempts of it named the row", () => {
  const named = decide(history, [picturesEntry], readAt).toQuarantine.map(({ row }) => row.file);
  expect(named).not.toContain("keyed-writes.e2e.ts");
  expect(named).not.toContain("mermaid.e2e.ts");
  expect(decide(history, [picturesEntry], readAt).counted.map((run) => run.id)).toEqual([
    101, 102, 103, 106,
  ]);
});

// `deep-links.e2e.ts`'s row is flaky in merge group 103 and in pull request 104. The control is
// the same history with 104 a merge group, where the row is named: the event is what kept it out.
test("a pull request's flaky row does not count", () => {
  expect(decide(history, [picturesEntry], readAt).toQuarantine).toEqual([]);
  const asMergeGroup = history.map((run) =>
    run.id === 104 ? { ...run, event: "merge_group" } : run
  );
  expect(
    decide(asMergeGroup, [picturesEntry], readAt).toQuarantine.map(({ row, runs }) => [
      `${row.file} ${row.title}`,
      runs.map((run) => run.id),
    ])
  ).toEqual([["deep-links.e2e.ts a held chunk lands @firefox", [103, 104]]]);
});

test("an entry held longer than fourteen days is named with its issue and the lines to remove", () => {
  const decision = decide(history, [picturesEntry, fourteenDays, eighteenDays], readAt);
  expect(decision.overdue).toEqual([{ days: 18, entry: eighteenDays }]);
  const { annotations, failed, summary } = report(decision, readAt);
  expect(failed).toBe(true);
  expect(summary).toContain(
    "- `offline-edits.e2e.ts [firefox] an edit is restored across a reload @firefox`, added " +
      "2026-11-01, 18 days ago, owned by dispatch://INFRA-2003. Once that issue's fix lands, " +
      "remove these lines from `packages/dispatch/e2e/quarantine.ts`:"
  );
  const lines = summary.slice(summary.indexOf("  ```ts") + 1, summary.indexOf("  ```ts") + 8);
  expect(lines).toEqual([
    "    {",
    '      file: "offline-edits.e2e.ts",',
    '      project: "firefox",',
    '      title: "an edit is restored across a reload @firefox",',
    '      issue: "dispatch://INFRA-2003",',
    '      added: "2026-11-01",',
    "    },",
  ]);
  expect(annotations).toEqual([
    "::error title=dispatch-browser quarantine::offline-edits.e2e.ts [firefox] an edit is " +
      "restored across a reload @firefox has been quarantined 18 days, more than 14; its issue " +
      "is dispatch://INFRA-2003",
  ]);
});

test("flaky rows a run counted and no row notice named are reported as unread", () => {
  const short = history.map((run) => (run.id === 106 ? { ...run, unread: 3 } : run));
  const { annotations, summary } = report(decide(short, [picturesEntry], readAt), readAt);
  expect(summary).toContain(
    "- [run 106](https://github.com/cybermonklabs/legion/actions/runs/106) of 2026-11-18 " +
      "(merge_group): its flake rate counts 3 more flaky rows than its row notices name, so " +
      "those rows are in its summary alone and are not counted here."
  );
  expect(annotations).toEqual([
    "::warning title=dispatch-browser quarantine::run 106 counts 3 flaky rows that no row " +
      "notice names, and they are not counted",
  ]);
});

// Biome breaks a property after its key where the line would pass 100 columns, so a long title
// is two lines of e2e/quarantine.ts. The lines `report` names to add or to remove are the lines
// the file holds, which `e2e/__tests__/quarantine.test.ts` checks of every real entry.
test("an entry's lines are the layout Biome keeps, a long title on the line after its key", () => {
  const title =
    "a person's Close withdraws the issue's asks, an earlier version keeps its open block, and " +
    "Reopen brings the asks back @pr";
  expect(entryLines({ ...picturesEntry, title })).toEqual([
    "  {",
    '    file: "pictures.e2e.ts",',
    '    project: "chromium",',
    "    title:",
    `      "${title}",`,
    '    issue: "dispatch://INFRA-2001",',
    '    added: "2026-11-12",',
    "  },",
  ]);
});
