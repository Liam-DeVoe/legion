/** `bun e2e/tools/shard-deal.ts <leg> <legs> <test-list> <config> [<selection>...]`: the tests one
 *  leg of the shard job runs, dealt to the legs by the time they took rather than by their count.
 *
 *  It lists the tests `<config>` selects under `<selection>`, the `--project` flags and the grep
 *  the leg's own run names, with `playwright test --list`, which loads the spec files and runs no
 *  `globalSetup`, so no stack starts. It groups those tests by project and file, weighs each
 *  group, deals the groups to `<legs>` legs and writes the groups of leg `<leg>` to `<test-list>`,
 *  in the form the run's `--test-list` reads: one `[project] › <file>` line per group, the file
 *  named from the config's root directory, which selects every test of that file in that project.
 *  It prints every leg's planned seconds and this leg's groups, for the log.
 *
 *  A group is a whole file of one project, so each test belongs to one group and each group to
 *  one leg, and a file's hooks and serial blocks stay on one machine. A group no test of this run
 *  falls in, a file the grep filters out, is not listed, so it is in no leg.
 *
 *  The deal: the heaviest group first, each onto the leg with the fewest planned seconds, a tie
 *  between groups broken by name and between legs by number. Every leg lists the same checkout
 *  and so computes the same deal, and the heaviest leg ends within the heaviest group of the
 *  lightest.
 *
 *  The weights are `e2e/shard-times.json`: for each `[project] › e2e/<file>`, the seconds its
 *  tests took, every attempt counted, and how many tests that was. A group the table holds weighs
 *  those seconds scaled to the tests this run lists of it, so a grep's subset of a file and a file
 *  that gained tests weigh what they hold. A group the table does not hold weighs its test count
 *  times the table's median seconds per test.
 *
 *  `bun e2e/tools/shard-deal.ts --record <log>... > e2e/shard-times.json` writes that table from
 *  the list reporter's lines (`✓`, `✘` and `-`, a retry being one more attempt of its test) in
 *  the logs of a full-lane run's shard legs. To refresh it from run `<run>`, from this package:
 *
 *    gh run view <run> --json jobs --jq '.jobs[] | select(.name | contains("shard")) | .databaseId'
 *    gh run view --job <job> --log > shard-<n>.log      (once for each of those jobs)
 *    bun e2e/tools/shard-deal.ts --record shard-*.log > e2e/shard-times.json
 *
 *  A stale table only makes the deal less even. The tests come from the listing and never from
 *  the table, so a test the table does not know still runs, in exactly one leg.
 */
import { readFileSync, writeFileSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

import { listing } from "../__tests__/playwright-listing";
import { type Listing, rowsOfListing } from "../configurations";

const usage =
  "usage: bun e2e/tools/shard-deal.ts <leg> <legs> <test-list> <config> [<selection>...]\n" +
  "       bun e2e/tools/shard-deal.ts --record <log>... > e2e/shard-times.json";

/** The package every path here is read from, which is the directory the shard job runs in. */
const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));
const tablePath = fileURLToPath(new URL("../shard-times.json", import.meta.url));

/** What the table holds of one group: the seconds of every attempt of its tests, and how many
 *  distinct tests those attempts were. */
export type Recorded = { seconds: number; tests: number };

/** `e2e/shard-times.json`. `medianTestSeconds` is the median of the recorded tests that ran,
 *  each test's attempts summed: what a group the table does not hold weighs for each test. */
export type Table = { groups: Record<string, Recorded>; medianTestSeconds: number };

/** One project's tests of one file, as this run lists them. */
export type Group = {
  /** The `--test-list` line: `[project] › <file>`, the file relative to the config's root
   *  directory, which is how Playwright's `loadTestList` compares it. */
  line: string;
  /** `[project] › e2e/<file>`, as the list reporter prints it from this package: the table's
   *  key. */
  name: string;
  /** How many tests of the group this run lists. */
  tests: number;
};

/** A group and what it weighs; `recorded` is whether the table held it. */
export type Weighed = Group & { recorded: boolean; seconds: number };

/** One leg of the deal: its groups and the seconds they weigh together. */
export type Leg = { groups: Weighed[]; seconds: number };

/** Plain code-unit order, which no locale moves, so every machine sorts names alike. */
function byName(one: string, other: string): number {
  return one < other ? -1 : one > other ? 1 : 0;
}

/** The groups of a listing's rows. `directory` is the config's root directory as seen from this
 *  package, `e2e`, which the table's names carry and the `--test-list` lines do not. */
export function groupsOf(
  rows: readonly { file: string; project: string }[],
  directory: string
): Group[] {
  const groups = new Map<string, Group>();
  for (const row of rows) {
    const line = `[${row.project}] › ${row.file}`;
    const group = groups.get(line) ?? {
      line,
      name: `[${row.project}] › ${join(directory, row.file)}`,
      tests: 0,
    };
    group.tests += 1;
    groups.set(line, group);
  }
  return [...groups.values()];
}

/** What `group` weighs by `table`, as the header says. */
export function weigh(group: Group, table: Table): Weighed {
  const held = table.groups[group.name];
  return held === undefined || held.tests === 0
    ? { ...group, recorded: false, seconds: group.tests * table.medianTestSeconds }
    : { ...group, recorded: true, seconds: (held.seconds * group.tests) / held.tests };
}

/** `groups` dealt to `legs` legs, as the header says. The answer does not depend on the order
 *  of `groups`. */
export function deal(groups: readonly Weighed[], legs: number): Leg[] {
  const dealt: Leg[] = Array.from({ length: legs }, () => ({ groups: [], seconds: 0 }));
  const heaviestFirst = [...groups].sort(
    (one, other) => other.seconds - one.seconds || byName(one.name, other.name)
  );
  for (const group of heaviestFirst) {
    let lightest = dealt[0];
    for (const leg of dealt) if (leg.seconds < lightest.seconds) lightest = leg;
    lightest.groups.push(group);
    lightest.seconds += group.seconds;
  }
  return dealt;
}

/** A list reporter's line for one attempt of one test, wherever it stands in a log line: a
 *  `gh run view --log` line starts with the job, the step and a timestamp. The mark, the
 *  attempt's number, the project, the file with the test's line and column, and the title, which
 *  ends in the attempt's duration when it ran and in ` (retry #N)` before that on a retry. */
const attemptLine = /(?:^|\s)([✓✘-])\s+\d+\s+\[([^\]]+)\] › (\S+?:\d+:\d+) › (.+)$/u;
const durationEnd = / \((\d+(?:\.\d+)?)(ms|s|m|h)\)$/u;
const retryEnd = / \(retry #(\d+)\)$/u;
const unitSeconds: Record<string, number> = { h: 3_600, m: 60, ms: 0.001, s: 1 };

/** The table the list reporter's lines in `logs`, the texts of a run's shard legs' logs, make.
 *  A skipped test counts as a test of no seconds; a retry adds its seconds to its test. An
 *  attempt printed twice, by a log given twice, counts once. Seconds are kept to the tenth. */
export function recordedTimes(logs: readonly string[]): Table {
  const attempts = new Set<string>();
  const tests = new Map<string, { group: string; ran: boolean; seconds: number }>();
  for (const text of logs) {
    for (const logLine of text.split("\n")) {
      const match = attemptLine.exec(logLine.trimEnd());
      if (match === null) continue;
      const [, mark, project, location, ending] = match;
      const timed = durationEnd.exec(ending);
      let title = timed === null ? ending : ending.slice(0, timed.index);
      const retried = retryEnd.exec(title);
      if (retried !== null) title = title.slice(0, retried.index);
      const file = location.replace(/:\d+:\d+$/u, "");
      const test = `[${project}] › ${location} › ${title}`;
      const attempt = `${test} #${retried?.[1] ?? 0}`;
      const seconds =
        mark === "-" || timed === null ? 0 : Number(timed[1]) * (unitSeconds[timed[2]] ?? 1);
      if (attempts.has(attempt)) continue;
      attempts.add(attempt);
      const found = tests.get(test) ?? { group: `[${project}] › ${file}`, ran: false, seconds: 0 };
      found.seconds += seconds;
      found.ran ||= timed !== null && mark !== "-";
      tests.set(test, found);
    }
  }
  if (tests.size === 0) throw new Error("the logs hold no line of the list reporter");
  const groups: Record<string, Recorded> = {};
  for (const test of tests.values()) {
    const group = groups[test.group] ?? { seconds: 0, tests: 0 };
    group.seconds += test.seconds;
    group.tests += 1;
    groups[test.group] = group;
  }
  for (const group of Object.values(groups)) group.seconds = Math.round(group.seconds * 10) / 10;
  const ran = [...tests.values()]
    .filter((test) => test.ran)
    .map((test) => test.seconds)
    .sort((one, other) => one - other);
  const middle = Math.floor(ran.length / 2);
  const median =
    ran.length === 0 ? 0 : ran.length % 2 === 1 ? ran[middle] : (ran[middle - 1] + ran[middle]) / 2;
  return { groups, medianTestSeconds: Math.round(median * 10) / 10 };
}

/** `table` as `e2e/shard-times.json` holds it: one group to a line, by name, in the layout the
 *  formatter keeps. */
export function tableText(table: Table): string {
  const groups = Object.keys(table.groups)
    .sort(byName)
    .map((name) => {
      const { seconds, tests } = table.groups[name];
      return `    ${JSON.stringify(name)}: { "seconds": ${seconds}, "tests": ${tests} }`;
    });
  const median = `  "medianTestSeconds": ${table.medianTestSeconds}`;
  return `{\n  "groups": {\n${groups.join(",\n")}\n  },\n${median}\n}\n`;
}

/** The listing of the leg's own selection, under the leg's own environment, so it selects what
 *  the run selects and fills the transform cache under the same TMPDIR the run reads. */
async function listed(config: string, selection: readonly string[]): Promise<Weighed[]> {
  const environment: Record<string, string> = {};
  for (const [name, value] of Object.entries(process.env)) {
    if (value !== undefined) environment[name] = value;
  }
  const answer = (await listing(import.meta.url, config, environment, selection)) as Listing & {
    config?: { rootDir?: string };
  };
  const rootDir = answer.config?.rootDir;
  if (rootDir === undefined) throw new Error(`the listing of ${config} names no root directory`);
  const table = JSON.parse(readFileSync(tablePath, "utf8")) as Table;
  const directory = relative(dispatchPackage, rootDir);
  return groupsOf(rowsOfListing(answer), directory).map((group) => weigh(group, table));
}

if (import.meta.main) {
  const argv = process.argv.slice(2);
  if (argv[0] === "--record") {
    if (argv.length < 2) {
      console.error(usage);
      process.exit(2);
    }
    const logs = argv.slice(1).map((path) => readFileSync(path, "utf8"));
    process.stdout.write(tableText(recordedTimes(logs)));
  } else {
    const [legText = "", legsText = "", out = "", config = "", ...selection] = argv;
    const leg = Number(legText);
    const legs = Number(legsText);
    if (!Number.isInteger(legs) || legs < 1 || !Number.isInteger(leg) || leg < 1 || leg > legs) {
      console.error(`${usage}\n<leg> must be a whole number from 1 to <legs>: ${argv.join(" ")}`);
      process.exit(2);
    }
    if (out === "" || config === "") {
      console.error(usage);
      process.exit(2);
    }
    const dealt = deal(await listed(config, selection), legs);
    const mine = [...(dealt[leg - 1]?.groups ?? [])].sort((one, other) =>
      byName(one.name, other.name)
    );
    writeFileSync(out, mine.map((group) => `${group.line}\n`).join(""));
    const testsIn = dealt.map((each) => each.groups.reduce((sum, group) => sum + group.tests, 0));
    const groups = dealt.flatMap((each) => each.groups);
    console.log(
      `shard-deal: ${groups.length} groups of ${testsIn.reduce((sum, tests) => sum + tests, 0)} ` +
        `tests dealt to ${legs} legs by e2e/shard-times.json, ` +
        `${groups.filter((group) => !group.recorded).length} of the groups not in it`
    );
    dealt.forEach((each, index) => {
      console.log(
        `  leg ${index + 1} of ${legs}: ${Math.round(each.seconds)} s planned, ` +
          `${each.groups.length} groups, ${testsIn[index]} tests`
      );
    });
    console.log(`leg ${leg} runs these groups, written to ${out}:`);
    for (const group of mine) {
      const weight = group.recorded ? "" : ", not in the table";
      console.log(`  ${group.name}: ${Math.round(group.seconds)} s, ${group.tests} tests${weight}`);
    }
  }
}
