// The control of gate-search.ts. The checkout as it is, its generated plugin included, flags no
// statement of the merge condition, and reads each of the reviewer's role prompts as a gate that
// states every fact its kind needs. Each planted sentence below is flagged naming its text, for the
// facts of R14 to R16 it lacks or for the pass it asks of named roles, which R14 rules out: R14's
// two-role rule put back in the reviewer's role prompts in the words that rule wrote there, one place
// at a time; a gate in each form of that rule, in a text the reviewer reads and in the generated
// copy of a skill; and the architect's acceptance with no word of a pass and a fail, in a text the
// architect reads. The same sentence in a text that no reader reaches is no flag, and a checkout
// with no texts is UNKNOWN, never clean.
//
//   bun test gate-search.test.ts
//
// GATE_SEARCH_CHECKOUT names the checkout (default: the repository this file is in). Each plant
// works in a copy of the checkout's prompts and skills, or of its built plugin, under its own
// mktemp directory.

import { afterAll, describe, expect, setDefaultTimeout, test } from "bun:test";
import { appendFileSync, cpSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { buildGenerated, type Entry, GENERATED, gateSearch } from "./gate-search";
import { roleTexts } from "./role-texts";
import { fileRowLimit, SCRIPT_RUN } from "./test-limit";

// Each row copies the checkout's prompts and skills, or builds its plugin, and the hook removes the
// copies: each test and hook gets the limit test-limit.ts computes from the bounds of what they
// wait for, set before the hook below.
setDefaultTimeout(fileRowLimit({ ...SCRIPT_RUN, part: "the plugin's build" }));

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

const CORE_REVIEWER = "packages/daemon/internal/prompts/roles/core/reviewer.md";
const REVIEWER = "packages/daemon/internal/prompts/roles/reviewer.md";
// A gate that asks a pass from the implementer and from the tester, and one that takes a line of
// one role alone for no pass: each holds one form of TWO_ROLE_FORMS (gate-search.ts) and no other.
const TWO_ROLES_GATE =
  "Approve only when each `before_merge` check reads yes on the line of the head you review, from the implementer and from the tester both.";
const ONE_ROLE_ALONE_GATE =
  "Approve only when each `before_merge` check reads yes on the line of the head you review: a line that names one of the two roles alone is no pass for your approval.";
// The fact a gate that asks a pass of named roles goes against.
const STATE_DECIDES = "R14: the state at the head decides, whoever recorded the pass";
// R14's two-role rule in the reviewer's role prompts: in each row, words of the state-alone gate
// and the words the two-role rule held in their place. The last row is a form of that rule that
// asks the tester's pass only where a tester worked the change. Rows two and four each hold one
// form of TWO_ROLE_FORMS alone.
const PUT_BACK: readonly (readonly [string, string, string])[] = [
  [
    CORE_REVIEWER,
    "and every `before_merge` check of the issue's Verification document reading yes on that head's line in `dispatch results --issue <issue>`, whoever recorded the pass: Dispatch's state decides your approval, and no role's pass is needed beside it.",
    "and a pass standing for every `before_merge` check of the issue's Verification document at that head, from the implementer and from the tester both, which `dispatch results --issue <issue>` prints on that head's line under each check: a line that names one of the two roles alone is no pass for your approval.",
  ],
  [
    CORE_REVIEWER,
    "and its state comes from its results from CI:",
    "and its results from CI stand for both roles:",
  ],
  [
    REVIEWER,
    "refuse it while any such check does not read yes on the line of that head under the check in `dispatch results --issue <issue>`, naming each by its block id. That state decides, whoever recorded the pass: for a check carrying `ci_check` it comes from its results from CI, and a pass at another commit is none.",
    "refuse it while any such check has no pass standing at that head from the implementer and from the tester both (from CI, for a check carrying `ci_check`), naming each by its block id, and read `dispatch results --issue <issue>` for it, on the line of that head under the check, which names its state and its roles: a line that names one of the two roles alone is no pass for your approval, and a pass at another commit is none.",
  ],
  [
    CORE_REVIEWER,
    "reading yes on that head's line",
    "reading yes on that head's line, from the implementer and, where a tester worked the change, from the tester,",
  ],
];
const ACCEPT =
  "Accept only when a pass stands for every `before_merge` check at the approved head.";
// A prompt part of another role: prompts.Compose gives it to no reader here, and no link names it.
const UNREAD = "packages/daemon/internal/prompts/roles/implementer.md";

// The coordinator's ruling keeps these six main-text sentences outside this pull request. A
// guide pull request removes each entry in the same commit that changes its sentence.
const MAIN_GUIDE_EXEMPTIONS: readonly Entry[] = [
  {
    file: "skills/legion-architect/SKILL.md",
    has: "Accept only when a pass stands for every",
    keep: "Owned by the guide pull request.",
  },
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "`dispatch results --issue <issue>`: whether a pass stands for each `before_merge` check at the head the step before your acceptance passed",
    keep: "Owned by the guide pull request.",
  },
  {
    file: "skills/legion-architect/references/acceptance.md",
    has: "Each check needs a pass at that head",
    keep: "Owned by the guide pull request.",
  },
  {
    file: "skills/legion-worker/SKILL.md",
    has: "They override this skill, except",
    keep: "Owned by the guide pull request.",
  },
  {
    file: "skills/legion-worker/references/merge-gate.md",
    has: "Approval is gated by the issue's own `before_merge` checks, as your role prompt says",
    keep: "Owned by the guide pull request.",
  },
  {
    file: "skills/legion-worker/references/results.md",
    has: "A `before_merge` result stands while its commit is the head of an open pull request",
    keep: "Owned by the guide pull request.",
  },
];

describe("gate-search", () => {
  test("the checkout as it is, with its generated plugin, flags nothing", () => {
    const outcome = gateSearch(checkout, undefined, MAIN_GUIDE_EXEMPTIONS);
    console.log(outcome.lines.filter((line) => line.includes(" GATE ")).join("\n"));
    expect(outcome.faults).toEqual([]);
    expect(outcome.generated).toBeGreaterThan(0);
    const gate = outcome.lines.find((line) => line.startsWith(`${MERGE_GATE}:`));
    expect(gate).toContain("KEEP");
    expect(gate).toContain("Owned by the guide pull request.");
    // The state-alone gate of each reviewer's role prompt is read, and states every fact.
    for (const file of [CORE_REVIEWER, REVIEWER])
      expect(
        outcome.lines.find((line) => line.startsWith(`${file}:`) && line.includes(" GATE "))
      ).toContain(
        " GATE reviewer (by its words): states line, other head, ci, not known, refusal, no wait\n"
      );
  });

  test("a role prompt that asks for a pass from two named roles fails", () => {
    for (const [file, gate, rule] of PUT_BACK) {
      const dir = copy();
      const text = readFileSync(path.join(dir, file), "utf8");
      expect({ file, gate: text.includes(gate) }).toEqual({ file, gate: true });
      writeFileSync(path.join(dir, file), text.replace(gate, rule));
      const faults = gateSearch(dir, null, MAIN_GUIDE_EXEMPTIONS).faults;
      console.log(`${file} with "${rule.slice(0, 80)}...": ${faults.join(" | ")}`);
      expect({ rule, faults: faults.length }).toEqual({ rule, faults: 1 });
      expect(faults[0]).toStartWith(`${file}:`);
      expect(faults[0]).toContain(`goes against ${STATE_DECIDES}`);
    }
  });

  test("a stale main-guide exemption fails by name", () => {
    const stale: Entry = {
      file: "skills/legion-retro/SKILL.md",
      has: "A pass must stand at the approved head for every `before_merge` check (stale)",
      keep: "Stale control.",
    };
    const faults = gateSearch(checkout, undefined, [...MAIN_GUIDE_EXEMPTIONS, stale]).faults;
    expect(
      faults.some(
        (fault) =>
          fault.includes(stale.file) && fault.includes(stale.has) && fault.includes("matches 0")
      )
    ).toBe(true);
  });

  for (const [plant, reader, file, flag] of [
    [
      TWO_ROLES_GATE,
      "reviewer",
      "skills/legion-worker/references/review-threads.md",
      `goes against ${STATE_DECIDES}`,
    ],
    [
      ONE_ROLE_ALONE_GATE,
      "reviewer",
      "skills/legion-worker/references/review-threads.md",
      `goes against ${STATE_DECIDES}`,
    ],
    [
      ACCEPT,
      "architect",
      "skills/legion-architect/references/wake-routing.md",
      "lacks R15 and R16: a pass and a fail at one head leave it not known",
    ],
  ] as const) {
    test(`"${plant}" in ${file}, which the ${reader} reads, is flagged naming that text`, () => {
      const dir = copy();
      expect(roleTexts(dir, reader).files).toContain(file);
      appendFileSync(path.join(dir, file), `\n${plant}\n`);
      const faults = gateSearch(dir, null, MAIN_GUIDE_EXEMPTIONS).faults;
      console.log(`planted in ${file}: ${faults.join(" | ")}`);
      expect(faults).toHaveLength(1);
      expect(faults[0]).toStartWith(`${file}:`);
      expect(faults[0]).toContain(flag);
    });

    test(`"${plant}" in ${UNREAD}, which no reader reaches, is no flag`, () => {
      const dir = copy();
      for (const reader of ["reviewer", "architect", "sub-architect"] as const)
        expect(roleTexts(dir, reader).files).not.toContain(UNREAD);
      appendFileSync(path.join(dir, UNREAD), `\n${plant}\n`);
      expect(gateSearch(dir, null, MAIN_GUIDE_EXEMPTIONS).faults).toEqual([]);
    });
  }

  test("a two-role gate in the generated copy of merge-gate.md is flagged naming the copy", () => {
    const build = buildGenerated(checkout);
    try {
      expect(build.fault).toBeUndefined();
      const out = build.out as string;
      appendFileSync(path.join(out, "legion", MERGE_GATE), `\n${TWO_ROLES_GATE}\n`);
      const faults = gateSearch(checkout, out, MAIN_GUIDE_EXEMPTIONS).faults;
      console.log(`planted in the generated copy: ${faults.join(" | ")}`);
      expect(faults).toHaveLength(1);
      expect(faults[0]).toStartWith(`${GENERATED}/${MERGE_GATE}:`);
      expect(faults[0]).toContain(`goes against ${STATE_DECIDES}`);
    } finally {
      if (build.out !== undefined)
        rmSync(path.dirname(build.out), { recursive: true, force: true });
    }
  });

  test("a checkout with no texts is UNKNOWN, never clean", () => {
    const empty = path.join(scratch, "empty");
    cpSync(path.join(checkout, "skills/legion-worker/SKILL.md"), path.join(empty, "README.md"));
    const faults = gateSearch(empty, undefined, MAIN_GUIDE_EXEMPTIONS).faults;
    expect(faults.some((fault) => fault.includes("UNKNOWN: read no text"))).toBe(true);
    expect(
      faults.some((fault) => fault.includes("UNKNOWN: the generated plugin is not read"))
    ).toBe(true);
    expect(faults.some((fault) => fault.includes("is not in the checkout"))).toBe(true);
  });
});
