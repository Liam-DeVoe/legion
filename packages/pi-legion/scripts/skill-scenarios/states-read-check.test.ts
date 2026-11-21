// states-read-check.sh's judgement of two scores, on score tables written here in score.ts's
// shape. It judges a row only when the row counts all five runs of the batch scored and no rig
// error. A row short of that reads UNKNOWN and the judgement exits 3, whatever its rate: four rig
// errors beside one scored run must not stand in for a batch of five. A complete row that does not
// hold reads FAIL and the judgement exits 1: head below 5/5 on no-verdict-fail or no-verdict-pass,
// or ablate-states-read not below head on no-verdict-fail.
//
//   bun test states-read-check.test.ts

import { expect, setDefaultTimeout, test } from "bun:test";
import { writeFileSync } from "node:fs";
import path from "node:path";
import { spawnWithin, type Wait } from "@legion/test-fixtures/row-clock";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import { fileRowLimit } from "./test-limit";

/** One run of `states-read-check.sh judge`: bash and awk over two small files. */
const JUDGE: Wait = { part: "states-read-check.sh judge", boundMs: 60_000 };

setDefaultTimeout(fileRowLimit(JUDGE));

const SCRIPT = path.join(import.meta.dir, "states-read-check.sh");
const scratch = temporaryDirectories("states-read-check");

/** A row of a score's table of counts: the pass column and the rig errors. */
interface Counts {
  readonly pass: string;
  readonly rig: number;
}

const complete = (passed: number): Counts => ({ pass: `${passed}/5`, rig: 0 });

/** Scores whose every row the judgement reads is complete and holds. */
const REVIEWER: Record<string, Counts> = {
  "reviewer-own-head:no-implementer\tablate-states-read": complete(1),
  "reviewer-own-head:no-implementer\thead": complete(5),
  "reviewer-own-head:no-verdict-fail\tablate-states-read": complete(0),
  "reviewer-own-head:no-verdict-fail\thead": complete(5),
  "reviewer-own-head:no-verdict-pass\tablate-states-read": complete(4),
  "reviewer-own-head:no-verdict-pass\thead": complete(5),
};
const ARCHITECT: Record<string, Counts> = {
  "architect-pass-and-fail\tablate-states-read": complete(4),
  "architect-pass-and-fail\thead": complete(5),
  "architect-pass-and-fail:no-verdict-fail\tablate-states-read": complete(3),
  "architect-pass-and-fail:no-verdict-fail\thead": complete(5),
  "architect-pass-and-fail:no-verdict-pass\tablate-states-read": complete(5),
  "architect-pass-and-fail:no-verdict-pass\thead": complete(5),
};

/** A score as score.ts prints it: a run line, the table of counts, a blank line, the legend.
 * A row whose counts are `undefined` is left out of the table. */
function score(rows: Record<string, Counts | undefined>): string {
  const table = Object.entries(rows)
    .filter((entry): entry is [string, Counts] => entry[1] !== undefined)
    .map(([row, counts]) => {
      const scored = counts.pass.split("/")[1];
      return `${row}\t${counts.pass}\t0/${scored}\t${counts.rig}\t0`;
    });
  return [
    "some-scenario-head-1\tpass=true\tref=false\texit=0",
    "",
    "scenario\tlabel\tpass\tpass+ref\trig errors\tflagged",
    ...table,
    "",
    "pass       the scorer's rule for each scenario",
    "",
  ].join("\n");
}

/** The judgement of the two scores, each the complete one above with `reviewer` and `architect`
 * written over its rows, and what it printed on both streams. */
function judge(
  reviewer: Record<string, Counts | undefined> = {},
  architect: Record<string, Counts | undefined> = {}
) {
  const dir = scratch();
  const files = [path.join(dir, "reviewer.txt"), path.join(dir, "architect.txt")];
  writeFileSync(files[0] as string, score({ ...REVIEWER, ...reviewer }));
  writeFileSync(files[1] as string, score({ ...ARCHITECT, ...architect }));
  const run = spawnWithin(JUDGE, ["bash", SCRIPT, "judge", ...files]);
  return { code: run.exitCode, out: `${run.stdout.toString()}${run.stderr.toString()}` };
}

test("complete scores whose rows hold pass the judgement", () => {
  const { code, out } = judge();
  expect(out).toContain(
    "ok reviewer-own-head:no-verdict-fail ablate-states-read 0/5, below head's 5/5"
  );
  expect(out).toContain(
    "ok architect-pass-and-fail:no-verdict-fail ablate-states-read 3/5, below head's 5/5"
  );
  expect(out).toContain("ok judgement");
  expect(code).toBe(0);
});

// Catches: an ablated row compared at any rate, so one scored run and four rig errors approve the
// control.
test("an ablated row of the reviewer short of five scored runs reads UNKNOWN", () => {
  const { code, out } = judge({
    "reviewer-own-head:no-verdict-fail\tablate-states-read": { pass: "0/1", rig: 4 },
  });
  expect(out).toContain(
    "UNKNOWN reviewer-own-head:no-verdict-fail ablate-states-read: 0/1 with 4 rig errors"
  );
  expect(out).not.toContain("ok reviewer-own-head:no-verdict-fail ablate-states-read");
  expect(out).toContain("UNKNOWN judgement");
  expect(out).not.toContain("ok judgement");
  expect(code).toBe(3);
});

test("an ablated row of the architect short of five scored runs reads UNKNOWN", () => {
  const { code, out } = judge(
    {},
    { "architect-pass-and-fail:no-verdict-fail\tablate-states-read": { pass: "0/1", rig: 4 } }
  );
  expect(out).toContain(
    "UNKNOWN architect-pass-and-fail:no-verdict-fail ablate-states-read: 0/1 with 4 rig errors"
  );
  expect(out).not.toContain("ok architect-pass-and-fail:no-verdict-fail ablate-states-read");
  expect(out).toContain("UNKNOWN judgement");
  expect(code).toBe(3);
});

// Catches: a count of four, with the fifth run a rig error, read as a failed control rather than
// an unmeasured one (the final batches' architect row).
test("an ablated row of four scored runs and a rig error reads UNKNOWN, not FAIL", () => {
  const { code, out } = judge(
    {},
    { "architect-pass-and-fail:no-verdict-fail\tablate-states-read": { pass: "4/4", rig: 1 } }
  );
  expect(out).toContain(
    "UNKNOWN architect-pass-and-fail:no-verdict-fail ablate-states-read: 4/4 with 1 rig error"
  );
  expect(out).not.toContain("FAIL");
  expect(code).toBe(3);
});

// Catches: a head row judged on fewer than five scored runs, and a control compared with it.
test("a head row short of five scored runs reads UNKNOWN, and so does its case's control", () => {
  const { code, out } = judge({
    "reviewer-own-head:no-verdict-fail\thead": { pass: "4/4", rig: 1 },
  });
  expect(out).toContain("UNKNOWN reviewer-own-head:no-verdict-fail head: 4/4 with 1 rig error");
  expect(out).toContain(
    "UNKNOWN reviewer-own-head:no-verdict-fail ablate-states-read: 0/5, but head's row is not complete"
  );
  expect(out).not.toContain("FAIL");
  expect(code).toBe(3);
});

// Catches: a rig error beside five passes, which no batch of five makes, read as head's 5/5.
test("a head row with a rig error beside five scored runs reads UNKNOWN", () => {
  const { code, out } = judge(
    {},
    { "architect-pass-and-fail:no-verdict-pass\thead": { pass: "5/5", rig: 1 } }
  );
  expect(out).toContain(
    "UNKNOWN architect-pass-and-fail:no-verdict-pass head: 5/5 with 1 rig error"
  );
  expect(out).not.toContain("ok architect-pass-and-fail:no-verdict-pass head");
  expect(code).toBe(3);
});

test("a row the score does not hold reads UNKNOWN", () => {
  const { code, out } = judge({ "reviewer-own-head:no-verdict-pass\thead": undefined });
  expect(out).toContain(
    "UNKNOWN reviewer-own-head:no-verdict-pass head: the score holds no such row"
  );
  expect(code).toBe(3);
});

test("a complete head row below five passes fails", () => {
  const { code, out } = judge({ "reviewer-own-head:no-verdict-fail\thead": complete(4) });
  expect(out).toContain("FAIL reviewer-own-head:no-verdict-fail head: 4/5, want 5/5");
  expect(out).toContain("FAIL judgement");
  expect(code).toBe(1);
});

test("a complete ablated row equal to head fails", () => {
  const { code, out } = judge(
    {},
    { "architect-pass-and-fail:no-verdict-fail\tablate-states-read": complete(5) }
  );
  expect(out).toContain(
    "FAIL architect-pass-and-fail:no-verdict-fail ablate-states-read: 5/5, not below head's 5/5"
  );
  expect(code).toBe(1);
});

// A row that does not hold decides the judgement even when another row is short.
test("a failed row beside a short one fails", () => {
  const { code, out } = judge(
    { "reviewer-own-head:no-verdict-pass\thead": complete(3) },
    { "architect-pass-and-fail:no-verdict-fail\tablate-states-read": { pass: "0/1", rig: 4 } }
  );
  expect(out).toContain("FAIL reviewer-own-head:no-verdict-pass head: 3/5, want 5/5");
  expect(out).toContain("UNKNOWN architect-pass-and-fail:no-verdict-fail ablate-states-read");
  expect(out).toContain("FAIL judgement");
  expect(code).toBe(1);
});
