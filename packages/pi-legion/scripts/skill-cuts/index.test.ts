import { beforeAll, expect, test } from "bun:test";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { main } from "./cli";
import { NO_CONTROL_LOSS } from "./score";

// `results-index`, the inventory of the sets' results: each skill set-count lists at 20 or more
// sessions (its summary.json's `atLeast20`), beside its nearest row (`nearest`'s row JSON), its
// cases file and its result (the report `behaviour report` printed, kept as `report.txt` in the
// result's directory or tarball). Every file is made up here, in the shapes those commands write:
// the sets alpha-set and beta-set, four skills at 20 or more and one below.

const scratch = temporaryDirectory("skill-cuts-index", { real: true });
const at = (...parts: string[]) => path.join(scratch, ...parts);

/** Writes `files` (path to text or bytes) under `root`. */
function writeTree(root: string, files: Record<string, string | Uint8Array>): void {
  for (const [file, text] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(root, file)), { recursive: true });
    writeFileSync(path.join(root, file), text);
  }
}

/** A nearest row of `skill`, as `nearest` writes it: its answer, or null when every reply was
 *  refused. */
function nearestRow(skill: string, answered: boolean): string {
  const entry = {
    name: "our-checklist",
    source: "acme/skills",
    verdict: "partial",
    missing: "It does not say when to stop.",
    reason: "Both list steps.",
  };
  return JSON.stringify({
    row: "nearest",
    skill: { name: skill, description: `Use for ${skill}.`, source: null, dir: `/sets/${skill}` },
    candidates: [{ source: "acme/skills", root: "/ours", skills: 3 }],
    answer: answered ? { verdict: "partial", nearest: [entry], reason: "One is near." } : null,
    nearest: answered ? [{ ...entry, description: "Use for our checklist." }] : [],
    refusals: [],
    spent: [],
    usd: 0,
  });
}

/** A report as `behaviour report` prints it, with the verdict line `verdict`. */
function report(skill: string, verdict: string): string {
  return [
    `skill ${skill}: 6 cases in cases-${skill}.jsonl`,
    "case c04: screen: old 10/10, none 0/10: informative, flagged",
    `verdict: ${verdict}`,
    "peak resident memory: 210 MiB, the most at the end of a case",
    "",
  ].join("\n");
}

beforeAll(() => {
  writeTree(at("count"), {
    "summary.json": JSON.stringify({
      window: { start: "2026-10-23T00:00:00Z", end: "2026-11-21T00:00:00Z" },
      skills: [
        { set: "alpha-set", skill: "alpha-one", own: 40 },
        { set: "alpha-set", skill: "alpha-two", own: 25 },
        { set: "alpha-set", skill: "alpha-three", own: 19 },
        { set: "beta-set", skill: "beta-one", own: 22 },
        { set: "beta-set", skill: "beta-two", own: 20 },
      ],
      atLeast20: [
        { set: "alpha-set", skill: "alpha-one", own: 40 },
        { set: "alpha-set", skill: "alpha-two", own: 25 },
        { set: "beta-set", skill: "beta-one", own: 22 },
        { set: "beta-set", skill: "beta-two", own: 20 },
      ],
    }),
  });
  writeTree(at("nearest"), {
    "alpha-set/alpha-one.json": nearestRow("alpha-one", true),
    // The request log and the user message beside a row are no rows.
    "alpha-set/alpha-one.json.requests.jsonl": "{}\n",
    "alpha-set/alpha-one.input.md": "# alpha-one\n",
    "alpha-set/alpha-three.json": nearestRow("alpha-three", true),
    // alpha-two's row: every reply refused, so it names no nearest skill.
    "alpha-set/alpha-two.json": nearestRow("alpha-two", false),
    "beta-set/beta-one.json": nearestRow("beta-one", true),
    "beta-set/beta-two.json": nearestRow("beta-two", true),
  });
  writeTree(at("cases"), {
    "cases-alpha-one.jsonl": "{}\n",
    "cases-alpha-two.jsonl.gz": Bun.gzipSync(new TextEncoder().encode("{}\n")),
    "cases-beta-two.jsonl": "{}\n",
  });
  writeTree(at("results"), {
    "results-alpha-one/report.txt": report("alpha-one", "none: fewer than 6 informative cases: 2"),
    "results-beta-one/report.txt": report(
      "beta-one",
      "no loss of no skill detected by the registered rule"
    ),
  });
  // alpha-two's result as a tarball of its directory.
  writeTree(at("tar"), {
    "results-alpha-two/report.txt": report(
      "alpha-two",
      "loss: no skill loses against their skill on 1 case (c04)"
    ),
    "results-alpha-two/cases/c04.json": "{}\n",
  });
  const tar = Bun.spawnSync([
    "tar",
    "-czf",
    at("results", "results-alpha-two.tar.gz"),
    "-C",
    at("tar"),
    "results-alpha-two",
  ]);
  if (tar.exitCode !== 0) throw new Error(`tar failed: ${tar.stderr.toString()}`);
});

/** `results-index`'s exit status and what it wrote, run in this process. */
async function index(...args: string[]) {
  const out: string[] = [];
  const err: string[] = [];
  const code = await main(["results-index", ...args], {
    out: (text) => out.push(text),
    err: (line) => err.push(line),
    peakRssBytes: () => 0,
  });
  return { code, out: out.join(""), err: err.join("\n") };
}

const ARGS = [
  ...["--count", at("count"), "--nearest", at("nearest")],
  ...["--cases", at("cases"), "--results", at("results")],
];

// Catches: a skill at 20 or more sessions left off the index, one below 20 listed, a refused
// nearest row, a missing cases file or a missing result passing as there, or a result tarball's
// report not read.
test("lists each skill at 20 or more in set-count's output and exits 1 for one with no nearest row, no cases file or no result", async () => {
  const ran = await index(...ARGS);
  expect(ran.code).toBe(1);
  expect(ran.out).toBe(
    [
      `results-index: 4 skills at 20 or more sessions in ${at("count", "summary.json")}; 1 with a nearest row, a cases file and a result`,
      `alpha-set/alpha-one (40 sessions): nearest: partial: our-checklist (acme/skills) (${at("nearest", "alpha-set", "alpha-one.json")}); cases: ${at("cases", "cases-alpha-one.jsonl")}; result: ${at("results", "results-alpha-one")}: verdict: none: fewer than 6 informative cases: 2`,
      `alpha-set/alpha-two (25 sessions): nearest: every reply refused (${at("nearest", "alpha-set", "alpha-two.json")}); cases: ${at("cases", "cases-alpha-two.jsonl.gz")}; result: ${at("results", "results-alpha-two.tar.gz")}: verdict: loss: no skill loses against their skill on 1 case (c04)`,
      `beta-set/beta-one (22 sessions): nearest: partial: our-checklist (acme/skills) (${at("nearest", "beta-set", "beta-one.json")}); cases: none; result: ${at("results", "results-beta-one")}: verdict: no loss of no skill detected by the registered rule`,
      `beta-set/beta-two (20 sessions): nearest: partial: our-checklist (acme/skills) (${at("nearest", "beta-set", "beta-two.json")}); cases: ${at("cases", "cases-beta-two.jsonl")}; result: none`,
      "",
    ].join("\n")
  );
  expect(ran.err).toBe(
    "results-index: 3 of 4 skills lack a nearest row, a cases file or a result: alpha-set/alpha-two (no nearest row), beta-set/beta-one (no cases file), beta-set/beta-two (no result)"
  );

  // Each one given what it lacks: the index is whole.
  writeTree(at("nearest"), { "alpha-set/alpha-two.json": nearestRow("alpha-two", true) });
  writeTree(at("cases"), {
    "cases-beta-one.jsonl.gz": Bun.gzipSync(new TextEncoder().encode("{}\n")),
  });
  writeTree(at("results"), {
    "results-beta-two/report.txt": report("beta-two", `none: ${NO_CONTROL_LOSS}`),
  });
  const whole = await index(...ARGS);
  expect({ code: whole.code, err: whole.err }).toEqual({ code: 0, err: "" });
  expect(whole.out).toContain("; 4 with a nearest row, a cases file and a result\n");

  // A result whose report has no verdict line is no result.
  writeTree(at("results"), { "results-beta-two/report.txt": "skill beta-two: 6 cases\n" });
  const unfinished = await index(...ARGS);
  expect(unfinished.code).toBe(1);
  expect(unfinished.out).toContain(
    `result: ${at("results", "results-beta-two")}: its report.txt has no verdict line`
  );
  expect(unfinished.err).toContain("beta-set/beta-two (no result)");
});

test("a summary that is not set-count's, or a nearest row that is no JSON, is refused (exit 2)", async () => {
  writeTree(at("bad-count"), { "summary.json": JSON.stringify({ skills: [] }) });
  const notCount = await index(...ARGS.map((arg) => (arg === at("count") ? at("bad-count") : arg)));
  expect(notCount.code).toBe(2);
  expect(notCount.err).toContain(`${at("bad-count", "summary.json")} is not set-count's summary`);
  writeTree(at("bad-nearest"), { "alpha-set/alpha-one.json": "{ not json" });
  const notJson = await index(
    ...ARGS.map((arg) => (arg === at("nearest") ? at("bad-nearest") : arg))
  );
  expect(notJson.code).toBe(2);
  expect(notJson.err).toContain(`${at("bad-nearest", "alpha-set", "alpha-one.json")} is not JSON`);
  const missing = await index("--count", at("count"));
  expect(missing.code).toBe(2);
  expect(missing.err).toContain("--nearest, --cases, --results required");
});
