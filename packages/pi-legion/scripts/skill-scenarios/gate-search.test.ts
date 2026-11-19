// The control of gate-search.ts. The checkout as it is, its generated plugin included, flags no
// statement of the merge condition. Each planted sentence below states the condition with fewer
// facts than R14 to R16 and is flagged for the facts it lacks, naming its text: the reviewer's gate
// with one role, in a text the reviewer reads; the architect's acceptance with no word of a pass and
// a fail, in a text the architect reads; the reviewer's gate in the generated copy of a skill; and
// merge-gate.md's gate with its two-roles words cut, which is that text before they were added. The
// same sentence in a text that no reader reaches is no flag, and a checkout with no texts is
// UNKNOWN, never clean.
//
//   bun test gate-search.test.ts
//
// GATE_SEARCH_CHECKOUT names the checkout (default: the repository this file is in). Each plant
// works in a copy of the checkout's prompts and skills, or of its built plugin, under its own
// mktemp directory.

import { afterAll, describe, expect, test } from "bun:test";
import { appendFileSync, cpSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { buildGenerated, GENERATED, gateSearch } from "./gate-search";
import { roleTexts } from "./role-texts";

const checkout = process.env.GATE_SEARCH_CHECKOUT ?? path.resolve(import.meta.dir, "../../../..");
const scratch = mkdtempSync(path.join(tmpdir(), "gate-search-test-"));
afterAll(() => rmSync(scratch, { recursive: true, force: true }));

let copies = 0;
function copy(): string {
  const dir = path.join(scratch, String(copies++));
  for (const part of ["skills", "packages/daemon/internal/prompts"])
    cpSync(path.join(checkout, part), path.join(dir, part), { recursive: true });
  return dir;
}

const MERGE_GATE = "skills/legion-worker/references/merge-gate.md";
// The words that make merge-gate.md's gate name a pass of each role at the reviewed head.
const TWO_ROLES = [
  " from the implementer and from the tester both (from CI, for a check carrying `ci_check`)",
  ": a line that names one of the two roles alone is no pass for the approval",
];
const ONE_ROLE =
  "Approve only when each `before_merge` check reads yes on the line of the head you review, from the implementer.";
const ACCEPT =
  "Accept only when a pass stands for every `before_merge` check at the approved head.";
// A prompt part of another role: prompts.Compose gives it to no reader here, and no link names it.
const UNREAD = "packages/daemon/internal/prompts/roles/implementer.md";

describe("gate-search", () => {
  test("the checkout as it is, with its generated plugin, flags nothing", () => {
    const outcome = gateSearch(checkout, undefined);
    console.log(outcome.lines.filter((line) => line.includes(" GATE ")).join("\n"));
    expect(outcome.faults).toEqual([]);
    expect(outcome.generated).toBeGreaterThan(0);
    const gate = outcome.lines.find((line) => line.startsWith(`${MERGE_GATE}:`));
    expect(gate).toContain("GATE reviewer");
    expect(gate).toContain("states line, other head, roles, ci, not known, refusal, no wait\n");
  });

  test(`${MERGE_GATE}'s gate with its two-roles words cut is flagged for the roles`, () => {
    const dir = copy();
    const file = path.join(dir, MERGE_GATE);
    let text = readFileSync(file, "utf8");
    for (const words of TWO_ROLES) {
      expect(text).toContain(words);
      text = text.replace(words, "");
    }
    writeFileSync(file, text);
    const faults = gateSearch(dir, null).faults;
    expect(faults).toHaveLength(1);
    expect(faults[0]).toStartWith(`${MERGE_GATE}:`);
    expect(faults[0]).toContain("R14: a pass from the implementer and one from the tester");
  });

  for (const [plant, reader, file, lacks] of [
    [
      ONE_ROLE,
      "reviewer",
      "skills/legion-worker/references/review-threads.md",
      "R14: a pass from the implementer and one from the tester",
    ],
    [
      ACCEPT,
      "architect",
      "skills/legion-architect/references/wake-routing.md",
      "R15 and R16: a pass and a fail at one head leave it not known",
    ],
  ] as const) {
    test(`"${plant}" in ${file}, which the ${reader} reads, is flagged naming that text`, () => {
      const dir = copy();
      expect(roleTexts(dir, reader).files).toContain(file);
      appendFileSync(path.join(dir, file), `\n${plant}\n`);
      const faults = gateSearch(dir, null).faults;
      console.log(`planted in ${file}: ${faults.join(" | ")}`);
      expect(faults).toHaveLength(1);
      expect(faults[0]).toStartWith(`${file}:`);
      expect(faults[0]).toContain(lacks);
    });

    test(`"${plant}" in ${UNREAD}, which no reader reaches, is no flag`, () => {
      const dir = copy();
      for (const reader of ["reviewer", "architect", "sub-architect"] as const)
        expect(roleTexts(dir, reader).files).not.toContain(UNREAD);
      appendFileSync(path.join(dir, UNREAD), `\n${plant}\n`);
      expect(gateSearch(dir, null).faults).toEqual([]);
    });
  }

  test("the one-role gate in the generated copy of merge-gate.md is flagged naming the copy", () => {
    const build = buildGenerated(checkout);
    try {
      expect(build.fault).toBeUndefined();
      const out = build.out as string;
      appendFileSync(path.join(out, "legion", MERGE_GATE), `\n${ONE_ROLE}\n`);
      const faults = gateSearch(checkout, out).faults;
      console.log(`planted in the generated copy: ${faults.join(" | ")}`);
      expect(faults).toHaveLength(1);
      expect(faults[0]).toStartWith(`${GENERATED}/${MERGE_GATE}:`);
      expect(faults[0]).toContain("R14: a pass from the implementer and one from the tester");
    } finally {
      if (build.out !== undefined)
        rmSync(path.dirname(build.out), { recursive: true, force: true });
    }
  });

  test("a checkout with no texts is UNKNOWN, never clean", () => {
    const empty = path.join(scratch, "empty");
    cpSync(path.join(checkout, "skills/legion-worker/SKILL.md"), path.join(empty, "README.md"));
    const faults = gateSearch(empty, undefined).faults;
    expect(faults.some((fault) => fault.includes("UNKNOWN: read no text"))).toBe(true);
    expect(
      faults.some((fault) => fault.includes("UNKNOWN: the generated plugin is not read"))
    ).toBe(true);
    expect(faults.some((fault) => fault.includes("is not in the checkout"))).toBe(true);
  });
});
