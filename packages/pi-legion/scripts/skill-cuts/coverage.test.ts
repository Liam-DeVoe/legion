import { beforeAll, expect, test } from "bun:test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { main } from "./cli";

// `craft-coverage`, the inventory of the craft reader's entries, on F13 (testdata/CAPTURE.md
// "F13"): a made-up `skills/` tree (alpha-one, alpha-two, the vendored vendored-tool, a skill
// nested inside alpha-one, a directory with no SKILL.md and a README) and a made-up
// `plugins/x/skills` tree (beta-one, beta-two, vendored-tool), written from F13-tree.json; and
// F13-entries.jsonl, the craft entries of alpha-one and alpha-two (source "acme/skills at
// 1a2b3c4d"), of beta-one (source "acme/dotfiles at 5e6f7a8b", every reply refused) and of
// beta-two (source "acme/skills at 1a2b3c4d"), with a proposal's entry among them.

const TESTDATA = path.join(import.meta.dir, "testdata");
const ENTRIES = path.join(TESTDATA, "F13-entries.jsonl");
const scratch = temporaryDirectory("skill-cuts-coverage");
const root = (...parts: string[]) => path.join(scratch, "roots", ...parts);

beforeAll(() => {
  const tree: Record<string, string> = JSON.parse(
    readFileSync(path.join(TESTDATA, "F13-tree.json"), "utf8")
  );
  for (const [file, text] of Object.entries(tree)) {
    mkdirSync(path.dirname(root(file)), { recursive: true });
    writeFileSync(root(file), text);
  }
});

/** `craft-coverage`'s exit status and what it wrote, run in this process. */
async function coverage(...args: string[]) {
  const out: string[] = [];
  const err: string[] = [];
  const code = await main(["craft-coverage", ...args], {
    out: (text) => out.push(text),
    err: (line) => err.push(line),
    peakRssBytes: () => 0,
  });
  return { code, out: out.join(""), err: err.join("\n") };
}

const OURS = "acme/skills at 1a2b3c4d";
const THEIRS = "acme/dotfiles at 5e6f7a8b";

// Catches: a skill of a root the reader never read passing as covered, a vendored skill counted
// as ours, a skill nested deeper than <name>/SKILL.md or a directory with no SKILL.md listed, or
// an entry of another source or a proposal's entry taken as a skill's.
test("lists each `<name>/SKILL.md` under the root less the skipped names, and exits 1 for one with no entry", async () => {
  const skills = await coverage(
    ...["--root", root("skills"), "--source", OURS, "--skip", "vendored-tool"],
    ...["--entries", ENTRIES]
  );
  expect({ code: skills.code, err: skills.err }).toEqual({ code: 0, err: "" });
  expect(skills.out).toBe(
    [
      `craft-coverage: ${root("skills")}, source ${OURS}: 2 skills (skipped: vendored-tool), 2 with an entry`,
      "alpha-one: entry: 2 gaps",
      "alpha-two: entry: no gap",
      "",
    ].join("\n")
  );

  const plugins = await coverage(
    ...["--root", root("plugins", "x", "skills"), "--source", THEIRS, "--skip", "vendored-tool"],
    ...["--entries", ENTRIES]
  );
  expect(plugins.code).toBe(1);
  expect(plugins.out).toBe(
    [
      `craft-coverage: ${root("plugins", "x", "skills")}, source ${THEIRS}: 2 skills (skipped: vendored-tool), 1 with an entry`,
      "beta-one: entry: every reply refused",
      "beta-two: none",
      "",
    ].join("\n")
  );
  expect(plugins.err).toBe(
    `craft-coverage: 1 skill under ${root("plugins", "x", "skills")} has no entry: beta-two`
  );

  // With no --skip, the vendored skill is listed, and it has no entry.
  const unskipped = await coverage(
    "--root",
    root("skills"),
    "--source",
    OURS,
    "--entries",
    ENTRIES
  );
  expect(unskipped.code).toBe(1);
  expect(unskipped.out).toContain("vendored-tool: none\n");
  expect(unskipped.err).toBe(
    `craft-coverage: 1 skill under ${root("skills")} has no entry: vendored-tool`
  );

  // With no --source, an entry of any source matches by its directory's name.
  const anySource = await coverage(
    ...["--root", root("plugins", "x", "skills"), "--skip", "vendored-tool"],
    ...["--entries", ENTRIES]
  );
  expect({ code: anySource.code, err: anySource.err }).toEqual({ code: 0, err: "" });
  expect(anySource.out).toContain("beta-two: entry: no gap\n");
});

test("a root or an entries file it cannot read, or an entries line that is no JSON, is refused (exit 2)", async () => {
  const noRoot = await coverage("--root", root("no-such-root"), "--entries", ENTRIES);
  expect(noRoot.code).toBe(2);
  expect(noRoot.err).toContain(`cannot read the root ${root("no-such-root")}`);
  const noEntries = await coverage("--root", root("skills"), "--entries", root("none.jsonl"));
  expect(noEntries.code).toBe(2);
  expect(noEntries.err).toContain("cannot read the entries");
  const broken = path.join(scratch, "broken.jsonl");
  writeFileSync(broken, `${readFileSync(ENTRIES, "utf8")}{"entry": "craft", "skill":\n`);
  const notJson = await coverage("--root", root("skills"), "--entries", broken);
  expect(notJson.code).toBe(2);
  expect(notJson.err).toContain(`line 6 of the entries ${broken} is not JSON`);
  const missing = await coverage("--root", root("skills"));
  expect(missing.code).toBe(2);
  expect(missing.err).toContain("--entries required");
});
