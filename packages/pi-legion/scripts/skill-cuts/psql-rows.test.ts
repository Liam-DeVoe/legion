import { expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import path from "node:path";
import { z } from "zod";
import { PullRow, psqlRows, pullText } from "./psql-rows";

// psql-rows.ts on F7: psql's own output under the store command's flags, captured from the two
// test databases of R1's fixture proof (count.test.ts makes the same two). Capture, from
// packages/pi-legion/scripts/skill-cuts, with the pod's test server (LEGION_TEST_PG_DSN):
//
//   createdb -h 127.0.0.1 -p 5433 -U legion_test "skillcuts_first_$(date +%s)"   (and _second_)
//   F1=testdata/F1/2026-11-20T01-59-41-780Z_01a1f6ae-51d4-715f-a2a6-59f275bdff80
//   bun fixturedb.ts --dsn "$FIRST" $F1.jsonl $F1/ProductiveChicken.jsonl \
//     testdata/F4/projects/-legion-scratch-tmp-infra4676-t1-runs-f3-work/6441e7f4-71f3-49f1-a76f-0e0ffc82ea11.jsonl
//   bun fixturedb.ts --dsn "$SECOND" testdata/F1b.jsonl
//   PSQL='psql -X -q -w -v ON_ERROR_STOP=1 --single-transaction -c "SET TRANSACTION READ ONLY" -f -'
//   bun countsql.ts --config testdata/F7-skill-cuts.json --store store_a --day 2026-11-20 \
//     | $PSQL "$FIRST" > testdata/F7-count-first.txt
//   bun countsql.ts --config testdata/F7-skill-cuts.json --store store_a --day 2026-11-21 \
//     | $PSQL "$SECOND" > testdata/F7-count-second.txt
//   bun -e 'import { pullSql } from "./countsql"; process.stdout.write(pullSql(
//     "F1/2026-11-20T01-59-41-780Z_01a1f6ae-51d4-715f-a2a6-59f275bdff80.jsonl", -2))' \
//     | $PSQL "$FIRST" > testdata/F7-pull.txt
//   bun -e 'import { pullSql } from "./countsql"; process.stdout.write(pullSql("no/such/path.jsonl", -2))' \
//     | $PSQL "$FIRST" > testdata/F7-empty.txt
//   bun -e 'import { tasksSql, mtimeRange } from "./countsql"; process.stdout.write(tasksSql(
//     mtimeRange("2026-11-20"), "2026-11-20T00:00:00Z", "2026-11-22T00:00:00Z", ["Fixture"]))' \
//     | $PSQL "$FIRST" > testdata/F7-tasks.txt
//   bun -e 'import { sessionsSql } from "./countsql"; process.stdout.write(sessionsSql(["83af4b8e0c75a050",
//     "0da7427e81182998", "5ebda8a6e0707d07", "0000000000000000"]))' | $PSQL "$FIRST" > testdata/F7-sessions.txt
//   echo "SELECT json_build_object('o', -1, 'c', E'first line | one\\nsecond line | two')::text AS part;" \
//     | $PSQL "$FIRST" > testdata/F7-newline-pipe.txt
//
// fixturedb.ts splits each file into two parts in the middle of a line, so F7-pull.txt holds F1's
// session as the file row (offset -1, empty) and two parts.

const TESTDATA = path.join(import.meta.dir, "testdata");
const F1_SESSION = "F1/2026-11-20T01-59-41-780Z_01a1f6ae-51d4-715f-a2a6-59f275bdff80.jsonl";
const F1_SUBAGENT =
  "F1/2026-11-20T01-59-41-780Z_01a1f6ae-51d4-715f-a2a6-59f275bdff80/ProductiveChicken.jsonl";
const F4_SESSION =
  "F4/projects/-legion-scratch-tmp-infra4676-t1-runs-f3-work/6441e7f4-71f3-49f1-a76f-0e0ffc82ea11.jsonl";
const F1_PROFILE =
  "/legion/scratch/tmp/infra4676/t1-runs/f1/home/.omp/profiles/fixture/agent/skills";

function f7(name: string): string {
  return readFileSync(path.join(TESTDATA, name), "utf8");
}

/** A file's hash as count.sql names it: md5 of its path, cut to 16 hex characters. */
function fileHash(filePath: string): string {
  return createHash("md5").update(filePath).digest("hex").slice(0, 16);
}

/** The fields of a count row this file checks; the rest (store, unpaired_hashes) are dropped. */
const CountRow = z.object({
  row: z.literal("count"),
  skill: z.string(),
  kind: z.string(),
  copy: z.string(),
  day: z.string(),
  files: z.number(),
  hashes: z.array(z.string()),
  unpaired: z.number(),
});

/** The count rows of a count result, without the summary and without their `row` field. */
function counts(rows: unknown[]) {
  return rows.flatMap((row) => {
    const parsed = CountRow.safeParse(row);
    if (!parsed.success) return [];
    const { row: _row, ...count } = parsed.data;
    return [count];
  });
}

test("the first database's count: alpha read and referenced once, the missing skill not at all, beta read in the subagent's file, F4's Skill call counted", () => {
  const rows = psqlRows(f7("F7-count-first.txt"));
  expect(counts(rows)).toEqual([
    {
      skill: "alpha",
      kind: "read",
      copy: `${F1_PROFILE}/alpha/SKILL.md`,
      day: "2026-11-20",
      files: 1,
      hashes: [fileHash(F1_SESSION)],
      unpaired: 0,
    },
    {
      skill: "alpha",
      kind: "reference",
      copy: `${F1_PROFILE}/alpha/references/x.md`,
      day: "2026-11-20",
      files: 1,
      hashes: [fileHash(F1_SESSION)],
      unpaired: 0,
    },
    {
      skill: "alpha",
      kind: "read",
      copy: "/legion/scratch/tmp/infra4676/t1-runs/f3/marketplace/plugins/acme-tools/skills/alpha/SKILL.md",
      day: "2026-11-20",
      files: 1,
      hashes: [fileHash(F4_SESSION)],
      unpaired: 0,
    },
    {
      skill: "beta",
      kind: "read",
      copy: `${F1_PROFILE}/beta/SKILL.md`,
      day: "2026-11-20",
      files: 1,
      hashes: [fileHash(F1_SUBAGENT)],
      unpaired: 0,
    },
    {
      skill: "beta",
      kind: "read",
      copy: "/legion/scratch/tmp/infra4676/t1-runs/f3/config/plugins/cache/acme-market/acme-tools/0.1.0/skills/beta/SKILL.md",
      day: "2026-11-20",
      files: 1,
      hashes: [fileHash(F4_SESSION)],
      unpaired: 0,
    },
  ]);
  expect(rows.at(-1)).toEqual({
    row: "summary",
    store: "store_a",
    mtime_from: Date.parse("2026-11-20T00:00:00Z"),
    mtime_to: Date.parse("2026-11-21T00:00:00Z"),
    files: 3,
    parts: 6,
    bytes: 185931,
    lines_parsed: 15,
    bad_lines: 0,
    shared_call_ids: 0,
  });
});

test("the second database's count: F1b's alpha, read on two days, is one session on its first day", () => {
  const rows = psqlRows(f7("F7-count-second.txt"));
  const alpha = counts(rows).filter((row) => row.skill === "alpha" && row.kind === "read");
  expect(alpha).toEqual([
    {
      skill: "alpha",
      kind: "read",
      copy: `${F1_PROFILE}/alpha/SKILL.md`,
      day: "2026-11-20",
      files: 1,
      hashes: [fileHash("F1b.jsonl")],
      unpaired: 0,
    },
  ]);
  expect(rows.at(-1)).toMatchObject({
    row: "summary",
    files: 1,
    mtime_from: Date.parse("2026-11-21T00:00:00Z"),
  });
});

test("a transcript pull's rows join into the file's text, the file row first", () => {
  const rows = psqlRows(f7("F7-pull.txt"));
  expect(rows.map((row) => PullRow.parse(row).o)).toEqual([-1, 0, 8479]);
  expect(pullText(rows)).toBe(readFileSync(path.join(TESTDATA, F1_SESSION), "utf8"));
  expect(pullText([...rows].reverse())).toBe(readFileSync(path.join(TESTDATA, F1_SESSION), "utf8"));
});

test("the task query's rows are each a path and a first user message", () => {
  const rows = z
    .array(z.strictObject({ path: z.string(), text: z.string() }))
    .parse(psqlRows(f7("F7-tasks.txt")));
  expect(rows.map((row) => row.path)).toEqual([F1_SESSION, F4_SESSION]);
  expect(rows[0]?.text).toStartWith("This is a test of skill reads in a made-up fixture project.");
  expect(rows[1]?.text).toStartWith(
    "This is a test of skill loading in a made-up fixture project."
  );
});

test("the sessions query names each hashed file the store holds", () => {
  expect(psqlRows(f7("F7-sessions.txt"))).toEqual(
    [F1_SESSION, F1_SUBAGENT, F4_SESSION].map((filePath) => ({
      path: filePath,
      hash: fileHash(filePath),
    }))
  );
});

test("an empty result is no row", () => {
  expect(psqlRows(f7("F7-empty.txt"))).toEqual([]);
});

test("a value that holds a newline and a | stays one row", () => {
  expect(psqlRows(f7("F7-newline-pipe.txt"))).toEqual([
    { o: -1, c: "first line | one\nsecond line | two" },
  ]);
});

test("a row line that is not one JSON value is refused, naming its line", () => {
  const lines = f7("F7-count-second.txt").split("\n");
  lines[3] = `${lines[3]?.slice(0, 40)}`;
  expect(() => psqlRows(lines.join("\n"))).toThrow("psql output line 4: not one JSON value");
});

test.each([
  [
    "a footer that gives another count",
    (text: string) => text.replace("(3 rows)", "(4 rows)"),
    "line 6: the footer gives 4 rows, the table holds 3",
  ],
  ["no footer", (text: string) => text.replace("(3 rows)", ""), "no footer (N rows)"],
  ["no header rule", (text: string) => text.replace(/^-+$/m, ""), "no header rule"],
  ["empty output", () => "", "no header rule"],
  [
    "text after the footer",
    (text: string) => `${text}SET\n`,
    "line 8: text after the table's footer",
  ],
  [
    "a second column",
    (text: string) => text.replace(/^(-+)$/m, "$1+-----"),
    "line 2: the table has more than one column",
  ],
])("%s is refused", (_name, change, fault) => {
  expect(() => psqlRows(change(f7("F7-count-second.txt")))).toThrow(fault);
});

test("a pull with a part left out, an offset twice, or a row of another shape is refused", () => {
  const rows = psqlRows(f7("F7-pull.txt"));
  expect(() => pullText([rows[0], rows[2]])).toThrow(
    "the part at offset 8479 does not start where the text before it ends (0)"
  );
  expect(() => pullText([...rows, rows[1]])).toThrow("pull rows give offset 0 twice");
  expect(() => pullText([{ o: 0, content: "x" }])).toThrow(
    'pull row 1 is not {"o": <offset>, "c": <text>}'
  );
});
