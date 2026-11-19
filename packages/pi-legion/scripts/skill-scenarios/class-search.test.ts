import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import {
  type BuiltWorld,
  CHECKS,
  type Check,
  DATA,
  judged,
  pinnedRoutes,
  pinSection,
  RIG,
  readData,
  readsHeld,
  report,
  servedReads,
  Tree,
  Worlds,
  worldOfDir,
} from "./class-search";
import { pinnedReads } from "./production-shapes";
import { spawnChecked } from "./test-programs";

// The class search (class-search.ts, `rig.sh class-search`): every class of defect INFRA-764's
// review rounds found, each a named check whose rule judges each hit, the residuals declared
// apart (the architect's ruling of 2026-11-18T08:40Z). These rows hold that it finds no instance
// outside its residuals at this checkout, and that an instance planted of each class fails the
// check that searches for it, naming the instance.

const CHECKOUT = path.resolve(import.meta.dir, "../../../..");
const tree = new Tree(CHECKOUT);
/** The check of the search named `name`. */
function checkNamed(name: string): Check {
  const found = CHECKS.find((check) => check.name === name);
  if (found === undefined) throw new Error(`the class search has no check named ${name}`);
  return found;
}
/** The tree with `line` added after the last line of `file`, and where it then is. */
function planting(file: string, line: string): { tree: Tree; at: string } {
  const text = existing(file);
  const body = text === "" || text.endsWith("\n") ? text : `${text}\n`;
  return {
    tree: new Tree(CHECKOUT, new Map([[file, `${body}${line}\n`]])),
    at: `${file}:${body.split("\n").length}`,
  };
}
function existing(file: string): string {
  try {
    return readFileSync(path.join(CHECKOUT, file), "utf8");
  } catch {
    return "";
  }
}
/** Where each instance of `hits` is. */
const instances = (hits: readonly { at: string; verdict: string }[]) =>
  hits.filter((hit) => hit.verdict === "instance").map((hit) => hit.at);

// Catches: a search that fails at the head it is run at, which is the zero the push waits for, or
// that prints no line for one of its checks or loses a residual it declares.
test("the class search finds no instance outside its residuals at this checkout, and each residual", () => {
  const done = spawnChecked(["bash", path.join(import.meta.dir, "rig.sh"), "class-search"], {
    env: process.env,
  });
  const stdout = done.stdout.toString();
  expect({ exitCode: done.exitCode, stderr: done.stderr.toString() }).toEqual({
    exitCode: 0,
    stderr: "",
  });
  expect(stdout).toContain(
    "result: 0 findings outside the residuals, 5 residuals found, 0 stale\n"
  );
  for (const check of CHECKS) expect(stdout).toContain(`== ${check.name}\n`);
}, 900_000);

// Catches: a data file whose hand search drops an entry of the 46, maps one to no check and no
// row, or names a check the search has not got; and a residual with no ruling.
test("the data file maps each of the hand search's 46 entries, and gives each residual its ruling", () => {
  const data = readData(readFileSync(DATA, "utf8"));
  expect(data.searches.map(({ entry }) => entry)).toEqual(
    Array.from({ length: 46 }, (_, index) => index + 1)
  );
  expect(data.residuals.map(({ ruled, words }) => ruled !== "" && words !== "")).toEqual(
    data.residuals.map(() => true)
  );
  expect(() =>
    readData(JSON.stringify({ residuals: [], searches: [{ entry: 1, finding: "x" }] }))
  ).toThrow("hand search entry 1 names neither a check or a row that holds it");
  expect(() =>
    readData(
      JSON.stringify({ residuals: [], searches: [{ entry: 1, finding: "x", check: ["none"] }] })
    )
  ).toThrow("hand search entry 1 names no check: none");
});

// Catches: a residual whose instance is gone left declared, which would read as a ruling on a
// finding the search no longer makes; and an instance a residual does not cover counted as one.
test("a residual no instance is of is stale, and an instance no residual covers is a finding", () => {
  const data = readData(readFileSync(DATA, "utf8"));
  const [first] = data.residuals;
  if (first === undefined) throw new Error("the data file declares no residual");
  const check = checkNamed(first.check);
  const none = judged([{ check, hits: [] }], data.residuals);
  expect(none.stale.map(({ name }) => name)).toEqual(data.residuals.map(({ name }) => name));
  expect(report(none, data, "head")).toContain(
    `== stale residuals (declared, and no instance found): ${data.residuals.length}\n`
  );
  const other = judged(
    [
      {
        check,
        hits: [{ at: "planted.ts:1", text: "planted", verdict: "instance", why: "planted" }],
      },
    ],
    data.residuals
  );
  expect(other.findings.map(({ hit }) => hit.at)).toEqual(["planted.ts:1"]);
});

describe("the writes and the reads of the world", () => {
  let worlds: Worlds;
  let approved: BuiltWorld;
  beforeAll(() => {
    worlds = new Worlds(tree);
    const found = worlds.all().find((world) => world.name === "threads-closed-text-fixes");
    if (found === undefined) throw new Error("no world of threads-closed's text-fixes was built");
    approved = found;
  }, 300_000);
  afterAll(() => worlds?.done());
  /** The approved world with `routes` ahead of its own, under a directory of its own. */
  function planted(routes: readonly unknown[]): Worlds {
    const dir = worlds.copy(approved);
    const file = path.join(dir, "fixtures.json");
    const fixtures = JSON.parse(readFileSync(file, "utf8"));
    fixtures.routes = [...routes, ...fixtures.routes];
    writeFileSync(file, JSON.stringify(fixtures));
    return new Worlds(tree, [worldOfDir("planted", dir)]);
  }

  // Catches: the route every write once fell to, `{id:6001}` and exit 0 for any POST, PATCH or
  // PUT no route above it took, which changed nothing (INFRA-764 round 25's PUT of a review).
  test.each([
    "writes: every write route of every world is modelled or refused",
    "writes: no route of any world is a catch-all",
  ])("a catch-all write route fails %s, naming it", async (name) => {
    const on = planted([{ match: "^gh api", stdout: { id: 6001 } }]);
    try {
      expect(instances(await checkNamed(name).run(tree, on))).toContain(
        "planted: fixtures.json routes[0]"
      );
    } finally {
      on.done();
    }
  }, 600_000);

  // Catches: a gh command gh's source reads as a write answered by a route with a success the
  // world does not keep, as `gh pr (edit|comment|review|ready)` once answered any of them.
  test("a gh write command a route answers with a success that changes nothing fails, naming it", async () => {
    const on = planted([{ match: "^gh pr merge( |$)", stdout: "merged" }]);
    try {
      const hits = await checkNamed(
        "writes: every gh write command the world serves is modelled"
      ).run(tree, on);
      expect(instances(hits)).toEqual(["gh-writes.json: pr merge"]);
    } finally {
      on.done();
    }
  }, 600_000);

  // Catches: a read production refuses (the 403 a role App gets for a branch's protection) that
  // the world answers, which tells an agent what production would not.
  test("a read production refuses that the world answers fails, naming it", async () => {
    const on = planted([
      { match: "^gh api .*/branches/main/protection", methods: ["GET"], stdout: {} },
    ]);
    try {
      const hits = await checkNamed(
        "reads: production's pinned answers the world does not give"
      ).run(tree, on);
      expect(instances(hits)).toContain("production-shapes.json: branches-main-protection");
    } finally {
      on.done();
    }
  }, 600_000);

  // Catches: a check that drops a spelling of the token's print, or reads one that prints none.
  test("each gh auth call production prints the token for is found, and no other", async () => {
    const hits = await checkNamed(
      "reads: a credential production prints that the world holds none of"
    ).run(tree, worlds);
    expect(hits.map((hit) => [hit.at, hit.verdict])).toEqual([
      ["gh auth status -t", "instance"],
      ["gh auth status --show-token", "instance"],
      ["gh auth token", "instance"],
    ]);
  }, 300_000);
});

// Catches: a GET the world serves with no pin and no word of the README's pin section, which the
// world answers as it likes and no row holds to production.
test("an unpinned GET the README does not name fails the reads check, naming it", () => {
  const route = "GET repos/{owner}/{repo}/hooks";
  const hits = readsHeld(
    [...servedReads(tree), { route, by: "planted" }],
    pinnedRoutes(pinnedReads()),
    pinSection(readFileSync(path.join(CHECKOUT, "packages/pi-envoy/scripts/README.md"), "utf8"))
  );
  expect(instances(hits)).toEqual([route]);
});

/** Each check that greps the tree, and a line of its class planted in a file it reads. */
const PLANTED: readonly [check: string, file: string, line: string][] = [
  [
    "reads: a remote helper key on the world's clone",
    `${RIG}/rig.sh`,
    "  git config remote.upstream.vcs other",
  ],
  [
    "an agent's world: a record of the rig's that names the rig",
    `${RIG}/rig.sh`,
    `  cp "$(label_provenance "\${P##*/}")" "$R/provenance"`,
  ],
  [
    "readers: a reader reads the run's archive and its recorded provenance alone",
    `${RIG}/health.ts`,
    'export const PLANTED_SCRATCH = process.env.TMPDIR ?? "";',
  ],
  [
    "readers: no reader decides a world fact from an agent's text",
    `${RIG}/score.ts`,
    "export const plantedResolved = (call: Call) => /PRRT_[\\w-]+/.test(callText(call));",
  ],
  [
    "hand search 1: a branch on a label's name",
    `${RIG}/score.ts`,
    'export const plantedBase = (label: string) => label === "base";',
  ],
  [
    "hand search 3: a gate on a tell main's engine makes unreachable",
    "skills/planted.md",
    "Move on once the issue is `awaiting_merge`.",
  ],
  [
    "hand search 4 and 6: one fact defined in two places",
    `${RIG}/worker-pane.ts`,
    "export function reviewOf(): void {}",
  ],
  [
    "hand search 6: a reader of world.json of its own",
    `${RIG}/health.ts`,
    'const plantedWorld = readFileSync(path.join(".", "world.json"), "utf8");',
  ],
  [
    "hand search 5: a citation of a test by its line",
    "docs/planted.md",
    "The row at rig.test.ts:120 holds it.",
  ],
  [
    "hand search 7: a verb read by a reader of its own",
    `${RIG}/score.ts`,
    'const plantedVerb = (argv: string[]) => argv.includes("-X");',
  ],
  [
    "hand search 8: a prompt restating a skill's rule",
    "packages/daemon/internal/prompts/roles/planted.md",
    "Resolve each thread as review-threads says.",
  ],
  [
    "hand search 9: a reference to a deleted symbol",
    "packages/pi-legion/src/planted.ts",
    'export const planted = "READY_NOT_APPROVED";',
  ],
  [
    "hand search 11: a bound a slow host fails",
    `${RIG}/planted.test.ts`,
    "expect(Date.now() - started).toBeLessThan(5_000);",
  ],
  [
    "hand search 15 and 31: a reader of another program's source as text",
    `${RIG}/daemon-standin.ts`,
    'const plantedHolds = ENGINE_SOURCE.includes("func planted(");',
  ],
  [
    "hand search 30: a reader of the boot record of its own",
    `${RIG}/score.ts`,
    'const plantedBoots = readFileSync(path.join(dir, BOOTED), "utf8");',
  ],
  [
    "hand search 20 and 35: a hand-kept list of the rig's programs",
    `${RIG}/rig.sh`,
    "for program in gh-standin.ts daemon-standin.ts; do :; done",
  ],
  [
    "hand search 37: a reader of the system prompt's argument as its text",
    `${RIG}/score.ts`,
    'const plantedPrompt = readFileSync(path.join(dir, "system-args"), "utf8");',
  ],
  [
    "hand search 40: an identity a commit sets that the environment beats",
    `${RIG}/rig.sh`,
    "  git -c user.name=planted commit -qm planted",
  ],
  [
    "hand search 41: a database a shared server keeps",
    `${RIG}/planted.test.ts`,
    'await sql("CREATE DATABASE planted");',
  ],
];

// Catches, for each check that greps: a pattern that misses its class, and a rule that calls an
// instance of it true. The planted line is the one instance the check finds that it does not
// find at this checkout. A check that walks the whole tree (the citations, the deleted symbols)
// reads every text file of packages/ twice.
test.each(PLANTED)(
  "a planted instance fails %s, naming it",
  async (name, file, line) => {
    const { tree: plantedTree, at } = planting(file, line);
    const check = checkNamed(name);
    const worlds = new Worlds(tree, []);
    try {
      const before = new Set(instances(await check.run(tree, worlds)));
      const found = instances(await check.run(plantedTree, worlds)).filter(
        (one) => !before.has(one)
      );
      expect(found).toEqual([at]);
    } finally {
      worlds.done();
    }
  },
  60_000
);
