import { expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { copyFileSync, existsSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { main } from "./cli";
import type { CraftAnswer, CraftGap } from "./readers";

// `craft-merge`, the merge of fresh `craft` readings of one skill's same inputs. Every entry is
// made up here, in the shape `craft` writes (cli.ts's `craftRead`): the skill alpha-one of
// acme/skills (SKILL.md and references/steps.md), a style text and a craft list of made-up
// sources, and the nearest skill beta-one of acme/dotfiles. The command reads only the entries,
// so no skill, style text or craft list is written.

const scratch = temporaryDirectory("skill-cuts-craft-merge");
const at = (...parts: string[]) => path.join(scratch, ...parts);
const sha256 = (text: string | Buffer) => createHash("sha256").update(text).digest("hex");

type Verdict = CraftAnswer["judged"][number]["verdict"];

/** The rules every reading judges: the style text's S1, S2 and S10, and the craft list's reader
 *  properties C1 and C3, in the order the reader is given them. */
const RULES = ["S1", "S2", "S10", "C1", "C3"] as const;

/** The inputs of a craft entry of alpha-one, as `craft` writes them. */
const INPUTS = {
  skill: {
    name: "alpha-one",
    dir: "/srv/acme/skills/alpha-one",
    source: "acme/skills at 1a2b3c4d",
    files: [
      { path: "SKILL.md", sha256: sha256("alpha-one SKILL.md") },
      { path: "references/steps.md", sha256: sha256("alpha-one references/steps.md") },
    ],
  },
  style: {
    file: "/srv/acme/style.md",
    source: "acme/dotfiles at 5e6f7a8b style.md",
    sha256: sha256("the style text"),
    rules: 10,
  },
  craft: {
    file: "/srv/acme/craft-list.json",
    source: "the craft list, version 1",
    sha256: sha256("the craft list"),
    readerProperties: ["C1", "C3"],
  },
  nearest: [
    {
      name: "beta-one",
      dir: "/srv/acme/theirs/beta-one",
      files: [{ path: "SKILL.md", sha256: sha256("beta-one SKILL.md") }],
    },
  ],
  reader: {
    prompt: "craft",
    settings: { model: "claude-opus-5-5", thinking_budget: 16000, max_tokens: 64000 },
  },
};
type Inputs = typeof INPUTS;

/** The judged list of an answer: every rule of RULES, `met` unless `verdicts` says otherwise. */
function judged(
  verdicts: Partial<Record<(typeof RULES)[number], Verdict>> = {}
): CraftAnswer["judged"] {
  return RULES.map((rule) => ({ rule, verdict: verdicts[rule] ?? "met" }));
}

/** A craft entry of alpha-one, as `craft` writes it, with `answer` (null: every reply refused)
 *  and `usd`, its inputs INPUTS with `change` applied to a copy of them. */
function craftEntry(
  answer: CraftAnswer | null,
  usd: number,
  change: (inputs: Inputs) => void = () => {}
): Record<string, unknown> {
  const inputs = structuredClone(INPUTS);
  change(inputs);
  const tokens = { input: 7000, output: 4000, cacheRead: 0, cacheWrite: 0 };
  const spent = [{ model: "claude-opus-5-5", ...tokens, usd }];
  return {
    entry: "craft",
    ...inputs,
    input: "/srv/acme/out/alpha-one.input.md",
    answer,
    refusals: answer === null ? ["gaps.0.line: is past the end of SKILL.md"] : [],
    spent,
    usd,
  };
}

/** Writes `value` as the entry file `name` under the scratch directory, and gives its path. */
function entryFile(name: string, value: unknown): string {
  writeFileSync(at(name), `${JSON.stringify(value, null, 2)}\n`);
  return at(name);
}

/** `craft-merge`'s exit status and what it wrote, run in this process. */
async function craftMerge(...args: string[]) {
  const out: string[] = [];
  const err: string[] = [];
  const code = await main(["craft-merge", ...args], {
    out: (text) => out.push(text),
    err: (line) => err.push(line),
    peakRssBytes: () => 0,
  });
  return { code, out: out.join(""), err: err.join("\n") };
}

/** The `--entry` flags of `files`. */
const entryFlags = (...files: string[]) => files.flatMap((file) => ["--entry", file]);

/** The merged entry in `file`. */
const readMerged = (file: string) => JSON.parse(readFileSync(file, "utf8"));

/** A gap of a craft answer. */
const gap = (
  file: string,
  line: number,
  rule: string,
  kind: CraftGap["kind"],
  why: string
): CraftGap => ({ file, line, rule, kind, why });

// Catches: a gap counted once per listing rather than once per reading, gaps merged by fewer than
// their file, line and rule, a line sorted as text, a judged rule sorted as text (S10 before S2),
// a why without its reading, or the first entry's inputs not kept.
test("merges three readings by file, line and rule, with each gap's count of readings", async () => {
  const nearestRule = "nearest:beta-one:SKILL.md:4";
  const files = [
    entryFile(
      "three-1.json",
      craftEntry(
        {
          judged: judged({ S2: "gap", S10: "gap", C1: "gap", C3: "gap" }),
          gaps: [
            gap("SKILL.md", 3, "S2", "missing", "No line says when the skill is done."),
            gap("SKILL.md", 3, "C1", "missing", "No opening line says what the skill is for."),
            gap("SKILL.md", 10, "C3", "present", "The step holds two rules."),
            gap("references/steps.md", 2, "S10", "present", "The example sits in a paragraph."),
          ],
        },
        0.25
      )
    ),
    entryFile(
      "three-2.json",
      craftEntry(
        {
          judged: judged({ S2: "gap", C3: "gap" }),
          gaps: [
            gap("SKILL.md", 10, "C3", "present", "Two rules share one step."),
            gap("SKILL.md", 3, "S2", "missing", "Nothing names the check that ends it."),
            gap("SKILL.md", 12, nearestRule, "missing", "beta-one names its stop rule here."),
          ],
        },
        0.125
      )
    ),
    entryFile(
      "three-3.json",
      craftEntry(
        {
          judged: judged({ S1: "not-applicable", S2: "gap", S10: "gap" }),
          gaps: [
            gap("SKILL.md", 3, "S2", "missing", "It never says when to stop."),
            gap("references/steps.md", 2, "S10", "present", "The example is in a paragraph."),
          ],
        },
        0.5
      )
    ),
  ];
  const out = at("three-merged.json");
  const ran = await craftMerge(...entryFlags(...files), "--out", out);
  expect({ code: ran.code, err: ran.err }).toEqual({ code: 0, err: "" });
  expect(ran.out).toBe(
    `craft-merge: alpha-one: 5 gaps in 3 readings (in 3 of 3: 1; 2 of 3: 2; 1 of 3: 2), $0.8750 at list price; written to ${out}\n`
  );
  const merged = readMerged(out);
  expect(Object.keys(merged)).toEqual([
    ...["entry", "skill", "style", "craft", "nearest", "reader", "readings", "entries"],
    ...["gaps", "judged", "noGap", "usd"],
  ]);
  expect(merged).toMatchObject({ entry: "craft-merged", ...INPUTS, readings: 3, noGap: false });
  expect(merged.usd).toBe(0.875);
  expect(merged.entries).toEqual(
    files.map((file) => ({ path: file, sha256: sha256(readFileSync(file)) }))
  );
  expect(merged.gaps).toEqual([
    {
      ...{ file: "SKILL.md", line: 3, rule: "C1", readings: 1, of: 3, kinds: ["missing"] },
      why: ["reading 1: No opening line says what the skill is for."],
    },
    {
      ...{ file: "SKILL.md", line: 3, rule: "S2", readings: 3, of: 3 },
      kinds: ["missing", "missing", "missing"],
      why: [
        "reading 1: No line says when the skill is done.",
        "reading 2: Nothing names the check that ends it.",
        "reading 3: It never says when to stop.",
      ],
    },
    {
      ...{ file: "SKILL.md", line: 10, rule: "C3", readings: 2, of: 3 },
      kinds: ["present", "present"],
      why: ["reading 1: The step holds two rules.", "reading 2: Two rules share one step."],
    },
    {
      ...{ file: "SKILL.md", line: 12, rule: nearestRule, readings: 1, of: 3, kinds: ["missing"] },
      why: ["reading 2: beta-one names its stop rule here."],
    },
    {
      ...{ file: "references/steps.md", line: 2, rule: "S10", readings: 2, of: 3 },
      kinds: ["present", "present"],
      why: [
        "reading 1: The example sits in a paragraph.",
        "reading 3: The example is in a paragraph.",
      ],
    },
  ]);
  expect(merged.judged).toEqual([
    { rule: "C1", gap: 1, met: 2, notApplicable: 0 },
    { rule: "C3", gap: 2, met: 1, notApplicable: 0 },
    { rule: "S1", gap: 0, met: 2, notApplicable: 1 },
    { rule: "S2", gap: 3, met: 0, notApplicable: 0 },
    { rule: "S10", gap: 2, met: 1, notApplicable: 0 },
  ]);
});

// Catches: a gap split by its kind, a reading's kind dropped, or a reading that lists one gap
// twice counted as two readings.
test("a gap with two kinds in two readings is one gap with both kinds", async () => {
  const files = [
    entryFile(
      "kinds-1.json",
      craftEntry(
        {
          judged: judged({ S1: "gap" }),
          gaps: [gap("SKILL.md", 5, "S1", "present", "The goal line names a tool.")],
        },
        0.25
      )
    ),
    entryFile(
      "kinds-2.json",
      craftEntry(
        {
          judged: judged({ S1: "gap" }),
          gaps: [
            gap("SKILL.md", 5, "S1", "missing", "No goal line."),
            gap("SKILL.md", 5, "S1", "present", "The title stands in for the goal."),
          ],
        },
        0.25
      )
    ),
  ];
  const out = at("kinds-merged.json");
  const ran = await craftMerge(...entryFlags(...files), "--out", out);
  expect({ code: ran.code, err: ran.err }).toEqual({ code: 0, err: "" });
  expect(ran.out).toBe(
    `craft-merge: alpha-one: 1 gap in 2 readings (in 2 of 2: 1; 1 of 2: 0), $0.5000 at list price; written to ${out}\n`
  );
  const merged = readMerged(out);
  expect(merged.readings).toBe(2);
  expect(merged.gaps).toEqual([
    {
      ...{ file: "SKILL.md", line: 5, rule: "S1", readings: 2, of: 2 },
      kinds: ["present", "missing", "present"],
      why: [
        "reading 1: The goal line names a tool.",
        "reading 2: No goal line.",
        "reading 2: The title stands in for the goal.",
      ],
    },
  ]);
  expect(merged.judged).toContainEqual({ rule: "S1", gap: 2, met: 0, notApplicable: 0 });
});

// Catches: readings of another skill, another text of a skill file, another style text, another
// craft list or other nearest skills merged as one skill's, wherever the entry stands among the
// entries, or a refusal that does not say what differs.
test("refuses entries of different inputs (a file sha256, the style, the craft list, the nearest skills)", async () => {
  const answer = { judged: judged(), gaps: [] };
  const first = entryFile("inputs-first.json", craftEntry(answer, 0.25));
  const same = entryFile("inputs-same.json", craftEntry(answer, 0.125));
  const other = sha256("another text");
  const cases: [string, (inputs: Inputs) => void, string][] = [
    [
      "file-sha",
      (inputs) => {
        inputs.skill.files[0] = { path: "SKILL.md", sha256: other };
      },
      `its skill file SKILL.md has sha256 ${other}, not ${INPUTS.skill.files[0]?.sha256}`,
    ],
    [
      "file-set",
      (inputs) => {
        inputs.skill.files.pop();
      },
      "it has no skill file references/steps.md",
    ],
    [
      "dir",
      (inputs) => {
        inputs.skill.dir = "/srv/acme/skills/alpha-two";
      },
      "its skill directory is alpha-two, not alpha-one",
    ],
    [
      "style",
      (inputs) => {
        inputs.style.sha256 = other;
      },
      `its style text has sha256 ${other}, not ${INPUTS.style.sha256}`,
    ],
    [
      "craft",
      (inputs) => {
        inputs.craft.sha256 = other;
      },
      `its craft list has sha256 ${other}, not ${INPUTS.craft.sha256}`,
    ],
    [
      "nearest-none",
      (inputs) => {
        inputs.nearest = [];
      },
      "it does not read the nearest skill beta-one",
    ],
    [
      "nearest-other",
      (inputs) => {
        inputs.nearest.push({ name: "gamma-one", dir: "/srv/acme/theirs/gamma-one", files: [] });
      },
      "it reads the nearest skill gamma-one, which the first does not",
    ],
    [
      "nearest-file",
      (inputs) => {
        inputs.nearest[0] = { ...INPUTS.nearest[0], files: [{ path: "SKILL.md", sha256: other }] };
      },
      `its nearest skill beta-one's file SKILL.md has sha256 ${other}, not ${INPUTS.nearest[0]?.files[0]?.sha256}`,
    ],
  ];
  for (const [name, change, words] of cases) {
    const differs = entryFile(`inputs-${name}.json`, craftEntry(answer, 0.25, change));
    const out = at(`inputs-${name}-merged.json`);
    const ran = await craftMerge(...entryFlags(first, same, differs), "--out", out);
    expect({ name, code: ran.code }).toEqual({ name, code: 2 });
    expect(ran.err).toContain(`the entry ${differs} reads other inputs than the first, ${first}: `);
    expect(ran.err).toContain(words);
    expect(existsSync(out)).toBe(false);
  }
  // Two faults at once: each is named.
  const both = entryFile(
    "inputs-both.json",
    craftEntry(answer, 0.25, (inputs) => {
      inputs.style.sha256 = other;
      inputs.craft.sha256 = other;
    })
  );
  const ran = await craftMerge(...entryFlags(first, both), "--out", at("inputs-both-merged.json"));
  expect(ran.code).toBe(2);
  expect(ran.err).toContain(
    `its style text has sha256 ${other}, not ${INPUTS.style.sha256}; its craft list has sha256 ${other}, not ${INPUTS.craft.sha256}`
  );
});

// Catches: a skill with no gap in any reading written with no word of it, or its judged rules
// lost.
test("a skill no reading finds a gap in gets noGap", async () => {
  // Three fresh readings: each spent its own dollars.
  const files = [0.25, 0.125, 0.5].map((usd, index) =>
    entryFile(
      `none-${index + 1}.json`,
      craftEntry({ judged: judged({ C3: "not-applicable" }), gaps: [] }, usd)
    )
  );
  const out = at("none-merged.json");
  const ran = await craftMerge(...entryFlags(...files), "--out", out);
  expect({ code: ran.code, err: ran.err }).toEqual({ code: 0, err: "" });
  expect(ran.out).toBe(
    `craft-merge: alpha-one: no gap in 3 readings, $0.8750 at list price; written to ${out}\n`
  );
  const merged = readMerged(out);
  expect(merged).toMatchObject({ entry: "craft-merged", readings: 3, gaps: [], noGap: true });
  expect(merged.judged).toEqual([
    { rule: "C1", gap: 0, met: 3, notApplicable: 0 },
    { rule: "C3", gap: 0, met: 0, notApplicable: 3 },
    { rule: "S1", gap: 0, met: 3, notApplicable: 0 },
    { rule: "S2", gap: 0, met: 3, notApplicable: 0 },
    { rule: "S10", gap: 0, met: 3, notApplicable: 0 },
  ]);
});

// Catches: a reading whose every reply was refused merged as a reading with no gap.
test("refuses an entry with no answer", async () => {
  const answer = { judged: judged(), gaps: [] };
  const first = entryFile("refused-1.json", craftEntry(answer, 0.25));
  const refused = entryFile("refused-2.json", craftEntry(null, 0.25));
  const third = entryFile("refused-3.json", craftEntry(answer, 0.5));
  const out = at("refused-merged.json");
  const ran = await craftMerge(...entryFlags(first, refused, third), "--out", out);
  expect(ran.code).toBe(2);
  expect(ran.err).toContain(`the entry ${refused} has no answer: every reply was refused`);
  expect(existsSync(out)).toBe(false);
});

// Catches: one reading given twice, by its path (as given, or another path to the same file) or as
// a copy of its file, counted as two readings, which makes a gap look found by more readings.
test("refuses one reading given twice, by path or by content", async () => {
  const answer = {
    judged: judged({ S2: "gap" }),
    gaps: [gap("SKILL.md", 3, "S2", "missing", "No line says when the skill is done.")],
  };
  const first = entryFile("twice-1.json", craftEntry(answer, 0.25));
  const second = entryFile("twice-2.json", craftEntry(answer, 0.125));
  const copy = at("twice-copy.json");
  copyFileSync(first, copy);
  const out = at("twice-merged.json");
  const given = (other: string) => `the entries ${first} and ${other} are one reading given twice`;

  const samePath = await craftMerge(...entryFlags(first, second, first), "--out", out);
  expect(samePath.code).toBe(2);
  expect(samePath.err).toContain(`${given(first)}: the same file`);
  // Another path to the same file.
  const relative = path.relative(process.cwd(), first);
  const otherPath = await craftMerge(...entryFlags(first, second, relative), "--out", out);
  expect(otherPath.code).toBe(2);
  expect(otherPath.err).toContain(`${given(relative)}: the same file`);
  const sameContent = await craftMerge(...entryFlags(first, second, copy), "--out", out);
  expect(sameContent.code).toBe(2);
  expect(sameContent.err).toContain(
    `${given(copy)}: the same content (sha256 ${sha256(readFileSync(first))})`
  );
  expect(existsSync(out)).toBe(false);

  // The two that differ merge.
  const merged = await craftMerge(...entryFlags(first, second), "--out", out);
  expect({ code: merged.code, err: merged.err }).toEqual({ code: 0, err: "" });
});

// Catches: one reading taken for a merge, or a file that is no craft entry read as one.
test("refuses fewer than two entries, and a file that is no craft entry", async () => {
  const answer = { judged: judged(), gaps: [] };
  const first = entryFile("shape-1.json", craftEntry(answer, 0.25));
  const one = await craftMerge(...entryFlags(first), "--out", at("shape-merged.json"));
  expect(one.code).toBe(2);
  expect(one.err).toContain("craft-merge takes two --entry or more, not 1");
  const noOut = await craftMerge(...entryFlags(first, first));
  expect(noOut.code).toBe(2);
  expect(noOut.err).toContain("--out required");
  writeFileSync(at("shape-not-json.json"), "{ not json");
  const notJson = await craftMerge(
    ...entryFlags(first, at("shape-not-json.json")),
    "--out",
    at("shape-merged.json")
  );
  expect(notJson.code).toBe(2);
  expect(notJson.err).toContain(`the entry ${at("shape-not-json.json")} is not JSON`);
  const row = entryFile("shape-row.json", { row: "nearest", skill: { name: "alpha-one" } });
  const notEntry = await craftMerge(...entryFlags(first, row), "--out", at("shape-merged.json"));
  expect(notEntry.code).toBe(2);
  expect(notEntry.err).toContain(`the entry ${row} is no craft entry: `);
  expect(existsSync(at("shape-merged.json"))).toBe(false);
});
