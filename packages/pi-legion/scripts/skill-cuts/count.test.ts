import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { SQL } from "bun";
import { z } from "zod";
import { loadConfig } from "./config";
import {
  countSql,
  fillTemplate,
  main,
  mtimeRange,
  pullSql,
  sessionsSql,
  sqlString,
  tasksSql,
} from "./countsql";
import { cutOffsets, lastTimestampMs, loadFixtures, storedPath, storeRefusal } from "./fixturedb";
import { psqlRows, pullText } from "./psql-rows";

// R1's fixture proof of count.sql (plan, "The SQL is committed and proven on fixtures"), and the
// pieces it stands on: countsql.ts's filling and fixturedb.ts's loading. The proof needs the pod's
// test server (LEGION_TEST_PG_DSN) and psql, and runs each query with the store command's own
// flags; without the server it is skipped with a printed line (the pi-legion CI job has none). It
// makes three databases of its own on that server and drops them at the end: F1's session, its
// subagent and F4 in the first; F1b in the second; F1's session in the third, cut into three parts
// inside its `alpha` read call line. Each query's rows must equal those of its F7 capture
// (psql-rows.test.ts gives the capture commands), so a change to the SQL that changes a result
// fails here until F7 is captured again.

const TESTDATA = path.join(import.meta.dir, "testdata");
const F1 = path.join(TESTDATA, "F1/2026-11-20T01-59-41-780Z_01a1f6ae-51d4-715f-a2a6-59f275bdff80");
const F1_SESSION = `${F1}.jsonl`;
const F1_SUBAGENT = `${F1}/ProductiveChicken.jsonl`;
const F4_SESSION = path.join(
  TESTDATA,
  "F4/projects/-legion-scratch-tmp-infra4676-t1-runs-f3-work/6441e7f4-71f3-49f1-a76f-0e0ffc82ea11.jsonl"
);
const F1B = path.join(TESTDATA, "F1b.jsonl");
const CONFIG = loadConfig(path.join(TESTDATA, "F7-skill-cuts.json"));
/** The text fixturedb cuts inside for the three-part file: F1's `alpha` read call line. */
const ALPHA_CALL = '"path":"skill://alpha"';

/** A file's hash as count.sql names it. */
function fileHash(stored: string): string {
  return createHash("md5").update(stored).digest("hex").slice(0, 16);
}

/** The fields of a count row the proof reads. */
const CountRow = z.object({
  row: z.literal("count"),
  skill: z.string(),
  kind: z.string(),
  copy: z.string(),
  files: z.number(),
  hashes: z.array(z.string()),
});

describe("countsql.ts fills the SQL", () => {
  test("count.sql for a day: every placeholder filled, the timeout first, the store a value", () => {
    const sql = countSql(CONFIG, "store_a", "2026-11-20");
    expect(sql.split("\n")[0]).toBe("SET LOCAL statement_timeout = '15min';");
    expect(sql).not.toContain("{{");
    expect(sql).toContain(
      `f.mtime_ms >= ${Date.parse("2026-11-20T00:00:00Z")} AND f.mtime_ms < ${Date.parse("2026-11-21T00:00:00Z")}`
    );
    expect(sql).toContain(
      "c.ts >= '2026-11-20T00:00:00.000Z'::timestamptz AND c.ts < '2026-11-22T00:00:00.000Z'::timestamptz"
    );
    expect(sql).toContain("'store', 'store_a'");
  });

  test("the hour form fills one hour of the day", () => {
    expect(mtimeRange("2026-11-20", 23)).toEqual({
      from: Date.parse("2026-11-20T23:00:00Z"),
      to: Date.parse("2026-11-21T00:00:00Z"),
    });
    expect(countSql(CONFIG, "store_a", "2026-11-20", 5)).toContain(
      `f.mtime_ms >= ${Date.parse("2026-11-20T05:00:00Z")} AND f.mtime_ms < ${Date.parse("2026-11-20T06:00:00Z")}`
    );
  });

  test.each([
    [
      "a store the configuration does not name",
      () => countSql(CONFIG, "store_b", "2026-11-20"),
      'names no store "store_b"',
    ],
    ["a day that is not a calendar day", () => mtimeRange("2026-11-31"), "is not a calendar day"],
    ["an hour out of range", () => mtimeRange("2026-11-20", 24), "is not an hour from 0 to 23"],
    [
      "a placeholder with no value",
      () => fillTemplate("SELECT {{a}}, {{b}}", { a: "1" }),
      "no value for the placeholder b",
    ],
    [
      "a value with no placeholder",
      () => fillTemplate("SELECT {{a}}", { a: "1", b: "2" }),
      "no placeholder for b",
    ],
    ["a pull offset below -2", () => pullSql("a.jsonl", -3), "is not an offset of -2 or more"],
    [
      "a hash that is not 16 hex characters",
      () => sessionsSql(["83af4b8e0c75a05"]),
      "is not a file hash",
    ],
    [
      "a task query with no term",
      () => tasksSql(mtimeRange("2026-11-20"), "2026-11-20T00:00:00Z", "2026-11-21T00:00:00Z", []),
      "at least one term",
    ],
  ])("%s is refused", (_name, call, fault) => {
    expect(call).toThrow(fault);
  });

  test("a string value is one literal, whatever it holds", () => {
    expect(sqlString("it's a path\\with'quotes")).toBe("'it''s a path\\with''quotes'");
    expect(pullSql("a'b.jsonl", -2)).toContain("WHERE f.path = 'a''b.jsonl'");
  });

  test("the command prints the query and refuses a store the configuration does not name", () => {
    const out: string[] = [];
    const err: string[] = [];
    const io = { out: (text: string) => out.push(text), err: (text: string) => err.push(text) };
    const config = path.join(TESTDATA, "F7-skill-cuts.json");
    expect(
      main(["--config", config, "--store", "store_a", "--day", "2026-11-20", "--hour", "3"], io)
    ).toBe(0);
    expect(out.join("")).toBe(countSql(CONFIG, "store_a", "2026-11-20", 3));
    expect(main(["--config", config, "--store", "store_c", "--day", "2026-11-20"], io)).toBe(2);
    expect(main(["--config", config, "--day", "2026-11-20"], io)).toBe(2);
    expect(
      main(["--config", config, "--store", "store_a", "--day", "2026-11-20", "--days", "2"], io)
    ).toBe(2);
    expect(err).toHaveLength(3);
    expect(err[0]).toContain('names no store "store_c"');
  });
});

describe("fixturedb.ts loads a fixture as the store keeps a file", () => {
  test("a file is cut inside the line that holds its middle byte, or the line --line names", () => {
    const bytes = new Uint8Array(readFileSync(F1_SESSION));
    for (const options of [{ parts: 2 }, { parts: 3, line: ALPHA_CALL }]) {
      const cuts = cutOffsets(bytes, options);
      expect(cuts).toHaveLength(options.parts - 1);
      const start = bytes.lastIndexOf(0x0a, (cuts[0] ?? 0) - 1) + 1;
      const end = bytes.indexOf(0x0a, cuts[0]);
      for (const cut of cuts) expect(cut > start && cut < end).toBe(true);
      if (options.line !== undefined) {
        expect(Buffer.from(bytes.subarray(start, end)).toString("utf8")).toContain(options.line);
      }
    }
  });

  test("a cut never splits a UTF-8 character, and a line too short to cut is refused", () => {
    const bytes = new TextEncoder().encode("{}\n\u00e9\u00e9\u00e9\u00e9\n{}\n");
    const [cut] = cutOffsets(bytes, { parts: 2 });
    expect(((bytes[cut ?? 0] ?? 0) & 0xc0) === 0x80).toBe(false);
    expect(() =>
      cutOffsets(new TextEncoder().encode("{}\nx\n{}\n"), { parts: 3, line: "x" })
    ).toThrow("too short");
  });

  test("mtime_ms is the timestamp of the file's last line that has one", () => {
    expect(lastTimestampMs(readFileSync(F1_SESSION, "utf8"))).toBe(
      Date.parse("2026-11-20T02:00:04.020Z")
    );
    expect(lastTimestampMs(readFileSync(F4_SESSION, "utf8"))).toBe(
      Date.parse("2026-11-20T02:15:25.948Z")
    );
    expect(lastTimestampMs(readFileSync(F1B, "utf8"))).toBe(Date.parse("2026-11-21T01:59:48.974Z"));
  });

  test("a fixture is stored by its path below testdata/", () => {
    expect(storedPath(F1_SUBAGENT)).toBe(
      "F1/2026-11-20T01-59-41-780Z_01a1f6ae-51d4-715f-a2a6-59f275bdff80/ProductiveChicken.jsonl"
    );
  });

  test("the session store's DSN is refused", () => {
    const dir = mkdtempSync(path.join(process.env.TMPDIR ?? tmpdir(), "skill-cuts-fixturedb-"));
    try {
      const file = path.join(dir, "OMP_SESSION_SQL_DSN");
      writeFileSync(file, "postgres://sessions@db.example:5432/sessions\n");
      const env = { OMP_SESSION_SQL_DSN_FILE: file };
      expect(storeRefusal("postgres://sessions@db.example:5432/sessions", env)).toContain(
        "session store"
      );
      expect(storeRefusal("postgres://legion_test@127.0.0.1:5433/fixtures", env)).toBeUndefined();
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});

const serverDsn = process.env.LEGION_TEST_PG_DSN;
if (serverDsn === undefined) {
  console.log(
    "count.test.ts: LEGION_TEST_PG_DSN is unset: count.sql's fixture proof on Postgres is skipped"
  );
}

describe.skipIf(serverDsn === undefined)("count.sql on the fixtures, in test databases", () => {
  const stamp = `${process.pid}_${Date.now()}`;
  const names = {
    first: `skill_cuts_count_${stamp}_first`,
    second: `skill_cuts_count_${stamp}_second`,
    third: `skill_cuts_count_${stamp}_third`,
  };
  /** The DSN of one of this run's databases on the server LEGION_TEST_PG_DSN names. */
  const dsnOf = (name: string) => {
    const url = new URL(serverDsn ?? "");
    url.pathname = `/${name}`;
    return url.toString();
  };
  let threeParts: readonly number[] = [];

  /** The rows of `sql` run in the database `name` with the store command's psql flags. */
  function run(name: string, sql: string): unknown[] {
    const psql = Bun.spawnSync(
      [
        "psql",
        "-X",
        "-q",
        "-w",
        "-v",
        "ON_ERROR_STOP=1",
        "--single-transaction",
        "-c",
        "SET TRANSACTION READ ONLY",
        "-f",
        "-",
        dsnOf(name),
      ],
      { stdin: new TextEncoder().encode(sql), stdout: "pipe", stderr: "pipe" }
    );
    if (psql.exitCode !== 0)
      throw new Error(`psql exited ${psql.exitCode}: ${psql.stderr.toString()}`);
    return psqlRows(psql.stdout.toString());
  }

  /** The rows of an F7 capture. */
  const captured = (name: string) => psqlRows(readFileSync(path.join(TESTDATA, name), "utf8"));

  beforeAll(async () => {
    const server = new SQL(serverDsn ?? "");
    try {
      for (const name of Object.values(names)) await server.unsafe(`CREATE DATABASE ${name}`);
    } finally {
      await server.close();
    }
    await loadFixtures(dsnOf(names.first), [F1_SESSION, F1_SUBAGENT, F4_SESSION], { parts: 2 });
    await loadFixtures(dsnOf(names.second), [F1B], { parts: 2 });
    const [loaded] = await loadFixtures(dsnOf(names.third), [F1_SESSION], {
      parts: 3,
      line: ALPHA_CALL,
    });
    threeParts = loaded?.offsets ?? [];
  }, 60_000);

  afterAll(async () => {
    const server = new SQL(serverDsn ?? "");
    try {
      for (const name of Object.values(names))
        await server.unsafe(`DROP DATABASE IF EXISTS ${name} WITH (FORCE)`);
    } finally {
      await server.close();
    }
  });

  test("the first database: alpha one read and one reference session, the missing skill none, beta one read in the subagent's file, F4's Skill call one read", () => {
    const rows = run(names.first, countSql(CONFIG, "store_a", "2026-11-20"));
    expect(rows).toEqual(captured("F7-count-first.txt"));
    const counts = rows.flatMap((row) => {
      const parsed = CountRow.safeParse(row);
      return parsed.success ? [parsed.data] : [];
    });
    const count = (skill: string, kind: string) =>
      counts.filter((row) => row.skill === skill && row.kind === kind);
    const alphaReads = count("alpha", "read");
    expect(
      alphaReads.map((row) => [
        row.copy.endsWith("/profiles/fixture/agent/skills/alpha/SKILL.md"),
        row.files,
      ])
    ).toEqual([
      [true, 1],
      [false, 1],
    ]);
    expect(count("alpha", "reference").map((row) => row.files)).toEqual([1]);
    expect(count("gamma", "read")).toEqual([]);
    expect(
      count("beta", "read").find((row) => row.copy.includes("/profiles/fixture/"))?.hashes
    ).toEqual([fileHash(storedPath(F1_SUBAGENT))]);
    expect(alphaReads[1]?.copy).toEndWith("/plugins/acme-tools/skills/alpha/SKILL.md");
    expect(alphaReads[1]?.hashes).toEqual([fileHash(storedPath(F4_SESSION))]);
  });

  test("the second database: F1b's alpha, read on two days, is one read session", () => {
    const rows = run(names.second, countSql(CONFIG, "store_a", "2026-11-21"));
    expect(rows).toEqual(captured("F7-count-second.txt"));
    expect(rows[0]).toMatchObject({
      row: "count",
      skill: "alpha",
      kind: "read",
      day: "2026-11-20",
      files: 1,
    });
  });

  test("a matched line that spans three parts is still counted", () => {
    const bytes = new Uint8Array(readFileSync(F1_SESSION));
    expect(threeParts).toHaveLength(3);
    const middle = Buffer.from(bytes.subarray(threeParts[1], threeParts[2]));
    expect(middle.includes(0x0a)).toBe(false);
    const rows = run(names.third, countSql(CONFIG, "store_a", "2026-11-20"));
    expect(rows.slice(0, 2)).toEqual(captured("F7-count-first.txt").slice(0, 2));
    expect(rows.at(-1)).toMatchObject({ row: "summary", files: 1, parts: 3, bad_lines: 0 });
  });

  test("the pull, task, sessions and empty queries give their F7 rows", () => {
    const pull = run(names.first, pullSql(storedPath(F1_SESSION), -2));
    expect(pull).toEqual(captured("F7-pull.txt"));
    expect(pullText(pull)).toBe(readFileSync(F1_SESSION, "utf8"));
    expect(run(names.first, pullSql("no/such/path.jsonl", -2))).toEqual([]);
    expect(
      run(
        names.first,
        tasksSql(mtimeRange("2026-11-20"), "2026-11-20T00:00:00Z", "2026-11-22T00:00:00Z", [
          "Fixture",
        ])
      )
    ).toEqual(captured("F7-tasks.txt"));
    const hashes = [F1_SESSION, F1_SUBAGENT, F4_SESSION].map((file) => fileHash(storedPath(file)));
    expect(run(names.first, sessionsSql([...hashes, "0000000000000000"]))).toEqual(
      captured("F7-sessions.txt")
    );
  });
});
