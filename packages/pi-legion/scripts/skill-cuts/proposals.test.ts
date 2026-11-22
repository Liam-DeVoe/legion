import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { main } from "./cli";
import { ItemFaults, parseProposalItems, splitDiff } from "./proposals";
import { loadPrompt, READER_TRIES } from "./readers";
import { messageSse, type Recorder, sseResponse, startRecorder } from "./recorder";

// The `proposals` command (proposals.ts) on F11, a pull request body cut to the section of its
// open proposals (testdata/CAPTURE.md "F11"), and F15, a closed pull request's diff cut to the
// hunks of two files and a renamed file (CAPTURE.md "F15"), which stands for the diff of both
// pull requests here. The skills roots and the repository tree are made up and written into a
// temporary directory; the readers' answers are canned (CAPTURE.md "The canned reader answers"),
// given by a stand-in gateway on 127.0.0.1.

const TESTDATA = path.join(import.meta.dir, "testdata");
const F11 = path.join(TESTDATA, "F11-body.md");
const F15 = path.join(TESTDATA, "F15-pr.diff");
const SECTION = "Open proposals of #41 and #75 (R5)";
const CARRY = "Lines this pull request can carry";
const GONE = "Lines whose target is gone";
const THIRD = "Lines this pull request carries through another requirement";
const BROKEN = "Lines with an item in a broken form";
const LEGION = "acme/skills at 1a2b3c4d";
const DOTFILES = "acme/dotfiles at 5e6f7a8b";
const SKILL_PATH = "plugins/acme/skills/writing-agent-skills/SKILL.md";

const scratch = temporaryDirectory("skill-cuts-proposals", { real: true });
const body = () => readFileSync(F11, "utf8");
const sha256 = (text: string) => createHash("sha256").update(text).digest("hex");

/** `cli.ts`'s exit status and what it wrote, run in this process. */
async function run(...args: string[]) {
  const out: string[] = [];
  const err: string[] = [];
  const code = await main(args, {
    out: (text) => out.push(text),
    err: (line) => err.push(line),
    peakRssBytes: () => 0,
  });
  return { code, out: out.join(""), err: err.join("\n") };
}

/** The faults ItemFaults names when `parse` throws it, or the error it throws instead. */
function faultsOf(parse: () => unknown): unknown {
  try {
    parse();
  } catch (error) {
    return error instanceof ItemFaults ? error.faults : error;
  }
  return [];
}

describe("the items of a pull request's body", () => {
  // Catches: an item dropped or read twice, a state form the parser does not take, or an item's
  // text cut short.
  test("parses one item per id of each named list, one of each state form (F11)", async () => {
    const items = parseProposalItems(body(), SECTION, [CARRY, GONE]);
    expect(items.map((item) => [item.id, item.state, item.list, item.line])).toEqual([
      ["41-1.1", "open", CARRY, 9],
      ["41-3.2", "partly on main", CARRY, 10],
      ["41-10.5", "open", CARRY, 11],
      ["75-1", "open; no ruling found", CARRY, 12],
      ["41-8.3", "target gone", GONE, 16],
      ["41-9.12", "target gone", GONE, 17],
      ["41-9.14", "done in its new home", GONE, 18],
    ]);
    expect(items[0]?.text).toBe(
      "before it deletes a rule, an agent finds the rule's reason (the clause beside it, else the commit that added it, with `git log -S`) and deletes the rule only if that reason no longer holds. Recommend keep: without it an agent deletes a rule whose reason still holds. Not carried: write `keep 41-1.1` to carry it."
    );
    // The dry run prints them with each list's count, and reads nothing else.
    const dry = await run(
      ...["proposals", "--dry-run", "--body", F11, "--section", SECTION],
      ...["--list", CARRY, "--list", GONE]
    );
    expect(dry).toEqual({
      code: 0,
      err: "",
      out: [
        `proposals: ${F11}: 7 items: 4 in "${CARRY}", 3 in "${GONE}"`,
        "41-1.1 (open): line 9",
        "41-3.2 (partly on main): line 10",
        "41-10.5 (open): line 11",
        "75-1 (open; no ruling found): line 12",
        "41-8.3 (target gone): line 16",
        "41-9.12 (target gone): line 17",
        "41-9.14 (done in its new home): line 18",
        "",
      ].join("\n"),
    });
  });

  // Catches: an item line that is not of the form read as an item, or passed over in silence.
  test("exits 1 naming an item line of a named list it cannot parse", async () => {
    expect(faultsOf(() => parseProposalItems(body(), SECTION, [CARRY, BROKEN]))).toEqual([
      `line 27, in the list "${BROKEN}", is not "- \`<id>\` (<state>): <text>": - \`41-0.1\` open: the state of this item is not in parentheses.`,
    ]);
    const ran = await run(
      ...["proposals", "--dry-run", "--body", F11, "--section", SECTION],
      ...["--list", CARRY, "--list", BROKEN]
    );
    expect(ran.code).toBe(1);
    expect(ran.out).toBe("");
    expect(ran.err).toContain(`proposals: ${F11}: 1 item line of a named list cannot be read:\n`);
    expect(ran.err).toContain("line 27,");
    // An id given again, an id an entry's file cannot be named by, and a line that follows an
    // item, is not indented and is no item.
    const made = [
      "### Made up",
      "",
      "Lines:",
      "",
      "- `a-1` (open): one",
      "- `a-1` (open): again",
      "- `a/2` (open): a slash",
      "  goes on",
      "not an item",
      "",
      "After the list.",
    ].join("\n");
    expect(faultsOf(() => parseProposalItems(made, "Made up", ["Lines"]))).toEqual([
      'line 6, in the list "Lines", gives the id a-1 of line 5 again',
      'line 7, in the list "Lines", gives the id "a/2", which holds a character other than a letter, a digit, ".", "_" and "-"',
      'line 9, in the list "Lines", follows an item, is not indented and is no item: not an item',
    ]);
  });

  // Catches: a list the run was not given read for its items, or a list's line outside the
  // section, or past a heading, taken as the list's.
  test("does not read a list it was not given", () => {
    const items = parseProposalItems(body(), SECTION, [GONE]);
    expect(items.map((item) => item.id)).toEqual(["41-8.3", "41-9.12", "41-9.14"]);
    // The third list's lines are not of the form: read, they fail.
    expect(faultsOf(() => parseProposalItems(body(), SECTION, [THIRD]))).toEqual([
      `line 22, in the list "${THIRD}", is not "- \`<id>\` (<state>): <text>": - \`41-8.2\`: the new line after line 62 of \`CLAUDE.md\` (R4).`,
      `line 23, in the list "${THIRD}", is not "- \`<id>\` (<state>): <text>": - \`41-13.2\`: the \`ignoredSkills\` comment in \`omp/config.yml\` changes with R2's hide.`,
    ]);
    const made = [
      "## Before",
      "",
      "Lines:",
      "",
      "- `x-1` (open): in another section",
      "",
      "## The section",
      "",
      "Lines:",
      "",
      "- `s-1` (open): the first item",
      "  and its second line",
      "#### A deeper heading",
      "- `s-2` (open): after the heading",
      "",
      "## After",
      "",
      "Lines:",
      "",
      "- `y-1` (open): in a later section",
    ].join("\n");
    expect(parseProposalItems(made, "The section", ["Lines"])).toEqual([
      {
        id: "s-1",
        state: "open",
        text: "the first item and its second line",
        list: "Lines",
        line: 11,
      },
    ]);
    expect(() => parseProposalItems(made, "Elsewhere", ["Lines"])).toThrow(
      '--section "Elsewhere" names no heading of the body'
    );
    expect(() => parseProposalItems(made, "The section", ["Gone"])).toThrow(
      '--list "Gone" starts no line of the section "The section"'
    );
  });
});

describe("a pull request's diff", () => {
  // Catches: a hunk cut at a blank context line or run into the next file, a renamed file named
  // by one path only, or a file with no hunk left out.
  test("splits a gh pr diff into files and hunks (F15)", () => {
    const text = readFileSync(F15, "utf8");
    const files = splitDiff(text);
    expect(
      files.map(({ before, after, status, hunks }) => ({
        before,
        after,
        status,
        hunks: hunks.map((hunk) => [hunk.header, hunk.lines.length]),
      }))
    ).toEqual([
      {
        before: ".claude/CLAUDE.md",
        after: ".claude/CLAUDE.md",
        status: "modified",
        hunks: [
          [
            "@@ -56,7 +56,8 @@ Before such an action, say what changes, what could break, and the safest altern",
            9,
          ],
          [
            "@@ -72,13 +73,15 @@ Before such an action, say what changes, what could break, and the safest altern",
            21,
          ],
        ],
      },
      {
        before: "plugins/acme/skills/product-demos/pipeline.md",
        after: "plugins/acme/skills/product-demos/references/pipeline.md",
        status: "renamed",
        hunks: [["@@ -4,7 +4,8 @@", 9]],
      },
      {
        before: SKILL_PATH,
        after: SKILL_PATH,
        status: "modified",
        hunks: [
          [
            '@@ -5,14 +5,12 @@ description: "Use when you create, edit, rewrite, shorten, test or delete an age',
            17,
          ],
          [
            "@@ -26,7 +24,7 @@ If this skill disagrees with the code, a command's output or its `--help`, follo",
            8,
          ],
        ],
      },
    ]);
    expect(files[0]?.hunks[0]?.lines.slice(0, 4)).toEqual([
      " ",
      " ## Asking me",
      " ",
      "-- Ask me only for my authority, my taste or my risk appetite. Decide the rest, do it, and tell me.",
    ]);
    expect(files[1]?.hunks[0]?.lines.at(-1)).toBe(" ```");
    // Every line of the diff is a header line of its file or a line of a hunk.
    const counted = files.reduce(
      (sum, file) => sum + file.hunks.reduce((each, hunk) => each + 1 + hunk.lines.length, 0),
      0
    );
    expect(counted).toBe(text.split("\n").length - 1 - (4 + 7 + 4));

    // A file the change adds, one it deletes, a rename with no change of text, a binary file, and
    // a last line with no newline.
    const made = [
      "diff --git a/new.md b/new.md",
      "new file mode 100644",
      "index 0000000..1111111",
      "--- /dev/null",
      "+++ b/new.md",
      "@@ -0,0 +1,2 @@",
      "+# New",
      "+Last line.",
      "\\ No newline at end of file",
      "diff --git a/gone.md b/gone.md",
      "deleted file mode 100644",
      "index 2222222..0000000",
      "--- a/gone.md",
      "+++ /dev/null",
      "@@ -1 +0,0 @@",
      "-Gone.",
      "diff --git a/old name.md b/new name.md",
      "similarity index 100%",
      "rename from old name.md",
      "rename to new name.md",
      "diff --git a/logo.png b/logo.png",
      "index 3333333..4444444 100644",
      "Binary files a/logo.png and b/logo.png differ",
      "",
    ].join("\n");
    expect(splitDiff(made)).toEqual([
      {
        before: null,
        after: "new.md",
        status: "added",
        hunks: [
          {
            header: "@@ -0,0 +1,2 @@",
            lines: ["+# New", "+Last line.", "\\ No newline at end of file"],
          },
        ],
      },
      {
        before: "gone.md",
        after: null,
        status: "deleted",
        hunks: [{ header: "@@ -1 +0,0 @@", lines: ["-Gone."] }],
      },
      { before: "old name.md", after: "new name.md", status: "renamed", hunks: [] },
      { before: "logo.png", after: "logo.png", status: "modified", hunks: [] },
    ]);
    // A hunk shorter than its @@ line counts, and text before the first file, are refused.
    expect(() => splitDiff(text.replace(" ## Asking me\n", ""))).toThrow(
      "in the hunk at line 5, starts with none of"
    );
    expect(() => splitDiff(`From the pull request:\n${text}`)).toThrow(
      "line 1 of the diff starts no file"
    );
  });
});

// The command, against the stand-in gateway.

/** A made-up skills root of the Legion repository. */
const LEGION_TREE: Readonly<Record<string, string>> = {
  "dispatch-writing-specs/SKILL.md":
    "---\nname: dispatch-writing-specs\ndescription: Use when you write a spec.\n---\n\n# Writing specs\n\n1. Record each answer with its date and a link.\n",
  "dispatch-writing-specs/references/answers.md": "# Answers\n\nGive each answer's reason.\n",
  "plain-notes/SKILL.md": "---\nname: plain-notes\ndescription: Use for notes.\n---\n\n# Notes\n",
};
/** A made-up dotfiles repository tree, whose skills root is `plugins/acme/skills`; it holds a
 *  skill of the Legion root's name too. */
const DOTFILES_TREE: Readonly<Record<string, string>> = {
  ".claude/CLAUDE.md":
    "# Agents\n\n- Do not turn a correction about one action into a rule all agents.\n- Check reviewer findings before you act on them.\n",
  [SKILL_PATH]:
    "---\nname: writing-agent-skills\ndescription: Use when you write a skill.\n---\n\n# Writing agent skills\n\n- Count the failure before you write for it.\n- Delete a rule when its reason no longer holds.\n",
  "plugins/acme/skills/dispatch-writing-specs/SKILL.md":
    "---\nname: dispatch-writing-specs\ndescription: Use when you write a spec.\n---\n\n# The dotfiles copy of the spec skill\n",
};

/** The proposal-target answer for each item of F11, and for the made-up item 41-20.1 of a diff
 *  that renames a file away from the path a new file takes (the test "a hunk's file is …"). */
const TARGET_ANSWERS: Readonly<Record<string, unknown>> = {
  "41-1.1": {
    hunks: [{ file: SKILL_PATH, hunk: 0 }],
    targets: [
      {
        kind: "skill",
        skill: { repo: DOTFILES, name: "writing-agent-skills" },
        why: "The hunk changes that skill's section on rules.",
      },
    ],
  },
  "41-3.2": {
    hunks: [{ file: SKILL_PATH, hunk: 1 }],
    targets: [
      {
        kind: "skill",
        skill: { repo: DOTFILES, name: "writing-agent-skills" },
        why: "The review of stale skills is that skill's.",
      },
    ],
  },
  "41-10.5": {
    hunks: [{ file: ".claude/CLAUDE.md", hunk: 1 }],
    targets: [{ kind: "dotfiles", path: ".claude/CLAUDE.md", why: "The missing word is in it." }],
  },
  "75-1": {
    hunks: [{ file: ".claude/CLAUDE.md", hunk: 0 }],
    targets: [
      { kind: "dotfiles", path: ".claude/CLAUDE.md", why: "The asking rules are in it." },
      {
        kind: "dotfiles",
        path: "omp/rules/design-and-asks.md",
        why: "The change adds this rule file.",
      },
    ],
  },
  "41-8.3": {
    hunks: [{ file: ".claude/CLAUDE.md", hunk: 0 }],
    targets: [{ kind: "none", why: "AskGate's rules are in a repository no listed skill is in." }],
  },
  "41-9.12": {
    hunks: [{ file: "plugins/acme/skills/product-demos/pipeline.md", hunk: 0 }],
    targets: [
      {
        kind: "skill",
        skill: { repo: LEGION, name: "dispatch-writing-specs" },
        why: "The line left the repository for that skill.",
      },
    ],
  },
  "41-9.14": {
    hunks: [{ file: ".claude/CLAUDE.md", hunk: 0 }],
    targets: [{ kind: "none", why: "Its new home holds it already." }],
  },
  "41-20.1": {
    hunks: [{ file: "rules.md", hunk: 0 }],
    targets: [{ kind: "dotfiles", path: "rules.md", why: "The change adds the rules file." }],
  },
};

/** The proposal answer for item `id`. */
function proposalAnswer(id: string): unknown {
  if (id !== "41-1.1") return { fixes: [], makes: [], summary: `The change of ${id} adds a line.` };
  return {
    fixes: [{ finding: 0, why: "The new line states one rule." }],
    makes: [
      {
        rule: "S8",
        file: "writing-agent-skills/SKILL.md",
        line: 9,
        why: "The changed line holds two rules.",
      },
    ],
    summary: "The change makes an agent find a rule's reason before it deletes the rule.",
  };
}

/** A request body's model, its reader (by its system prompt), its item's id and its user
 *  message's blocks. */
function requestOf(body: string) {
  const sent = JSON.parse(body) as {
    model: string;
    system: { text: string }[];
    messages: { content: { text: string; cache_control?: unknown }[] }[];
  };
  const system = sent.system[0]?.text;
  const blocks = sent.messages[0]?.content ?? [];
  return {
    model: sent.model,
    reader: ["proposal-target", "proposal"].find((name) => loadPrompt(name).text === system),
    id: /<item id="([^"]+)"/.exec(blocks.map((block) => block.text).join("\n\n"))?.[1],
    blocks,
  };
}

/** The items whose proposal-target replies the stand-in gives as text that is no JSON. */
let refusedTargets = new Set<string>();
let recorder: Recorder;
beforeAll(() => {
  recorder = startRecorder({
    route: (request) => {
      if (request.method !== "POST" || request.path !== "/v1/messages") return undefined;
      const { model, reader, id = "" } = requestOf(request.body);
      const reply =
        reader === "proposal-target"
          ? refusedTargets.has(id)
            ? "The hunks are those of the skill."
            : JSON.stringify(TARGET_ANSWERS[id])
          : reader === "proposal"
            ? JSON.stringify(proposalAnswer(id))
            : "{}";
      return sseResponse(
        messageSse({
          id: `msg_standin_${request.n}`,
          model,
          content: [{ type: "text", text: reply }],
          stopReason: "end_turn",
          inputTokens: 10,
          outputTokens: 3,
        }),
        `req_standin_${request.n}`
      );
    },
  });
});
afterAll(async () => {
  await recorder.close();
});

/** Each request since `from`: its model, its reader, its item's id and its user message's
 *  blocks. */
function requestsSince(from: number) {
  return recorder.requests.slice(from).map((request) => requestOf(request.body));
}

/** Writes `files` (a path inside `dir` to its text) under `dir`. */
function writeTree(dir: string, files: Readonly<Record<string, string>>): void {
  for (const [file, text] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(dir, file)), { recursive: true });
    writeFileSync(path.join(dir, file), text);
  }
}

/** A run of `proposals` in its own directory: F11, F15 as the diff of both pull requests, the two
 *  skills roots, the dotfiles tree, a findings file and a configuration whose gateway is the
 *  stand-in. The findings file holds, in order, a craft entry of writing-agent-skills of another
 *  commit, one of the dotfiles commit, a proposal's entry, and one of the Legion
 *  dispatch-writing-specs whose every reply was refused. */
function scenario(name: string) {
  const dir = path.join(scratch, name);
  const legion = path.join(dir, "legion", "skills");
  const dotfiles = path.join(dir, "dotfiles");
  const dotfilesSkills = path.join(dotfiles, "plugins", "acme", "skills");
  writeTree(legion, LEGION_TREE);
  writeTree(dotfiles, DOTFILES_TREE);
  const gap = (line: number, why: string) => ({
    file: "SKILL.md",
    line,
    rule: "S8",
    kind: "present",
    why,
  });
  const craftEntry = (skillDir: string, source: string, answer: unknown) => ({
    entry: "craft",
    skill: { name: path.basename(skillDir), dir: skillDir, source, files: [{ path: "SKILL.md" }] },
    answer,
  });
  const findings = path.join(dir, "reader-findings.jsonl");
  writeFileSync(
    findings,
    `${[
      craftEntry(path.join(dotfilesSkills, "writing-agent-skills"), "acme/dotfiles at 00000000", {
        judged: [{ rule: "S8", verdict: "gap" }],
        gaps: [gap(8, "A gap of an older commit's read.")],
      }),
      craftEntry(path.join(dotfilesSkills, "writing-agent-skills"), DOTFILES, {
        judged: [{ rule: "S8", verdict: "gap" }],
        gaps: [gap(9, "The line holds two rules.")],
      }),
      { entry: "proposal", id: "41-1.1", state: "open", answer: { fixes: [], makes: [] } },
      craftEntry(path.join(legion, "dispatch-writing-specs"), LEGION, null),
    ]
      .map((line) => JSON.stringify(line))
      .join("\n")}\n`
  );
  const config = path.join(dir, "skill-cuts.json");
  const fixture = JSON.parse(readFileSync(path.join(TESTDATA, "F7-skill-cuts.json"), "utf8"));
  writeFileSync(config, JSON.stringify({ ...fixture, gatewayBaseUrl: recorder.url }));
  const keyFile = path.join(dir, "key");
  writeFileSync(keyFile, fixtureSecret("gw"), { mode: 0o600 });
  const out = path.join(dir, "out");
  const args = [
    ...["proposals", "--config", config, "--key-file", keyFile],
    ...["--body", F11, "--body-source", "acme/dotfiles#105 at ac1b11e8", "--section", SECTION],
    ...["--list", CARRY, "--list", GONE, "--pr-diff", `41=${F15}`, "--pr-diff", `75=${F15}`],
    ...["--skills-root", `${LEGION}=${legion}`, "--skills-root", `${DOTFILES}=${dotfilesSkills}`],
    ...["--repo-root", `${DOTFILES}=${dotfiles}`],
    ...["--style", path.join(TESTDATA, "style-check.md"), "--style-source", "style notes v1"],
    ...["--craft", path.join(TESTDATA, "F12-craft-list.json"), "--craft-source", "craft list v2"],
    ...["--findings", findings, "--out", out],
  ];
  /** Item `id`'s entry. */
  const entry = (id: string) => JSON.parse(readFileSync(path.join(out, `${id}.json`), "utf8"));
  return { legion, dotfiles, dotfilesSkills, out, args, entry };
}

describe("proposals", () => {
  /** The scenario run once with every reply answered, for the three tests that read that run: the
   *  scenario, what the run wrote, and the requests it sent. */
  let full: {
    s: ReturnType<typeof scenario>;
    ran: Awaited<ReturnType<typeof run>>;
    sent: ReturnType<typeof requestsSince>;
  };
  beforeAll(async () => {
    const s = scenario("full");
    const from = recorder.requests.length;
    const ran = await run(...s.args);
    full = { s, ran, sent: requestsSince(from) };
  }, 30_000);

  // Catches: a run that ends 0 with an item unread, or that reads again an item whose entry is
  // written.
  test("exits 1 when an item has no entry", async () => {
    const s = scenario("no-entry");
    refusedTargets = new Set(["41-3.2"]);
    const from = recorder.requests.length;
    const first = await run(...s.args);
    refusedTargets = new Set();
    expect(first.code).toBe(1);
    expect(first.err).toContain(
      "proposals: 41-3.2: the proposal-target reader's answer was refused"
    );
    expect(first.err).toContain("proposals: 1 item of 7 has no entry: 41-3.2");
    expect(existsSync(path.join(s.out, "41-3.2.json"))).toBe(false);
    const refused = JSON.parse(readFileSync(path.join(s.out, "41-3.2.refused.json"), "utf8"));
    expect([refused.id, refused.reader, refused.refusals.length]).toEqual([
      "41-3.2",
      "proposal-target",
      READER_TRIES,
    ]);
    const sent = requestsSince(from);
    expect(
      sent.filter((request) => request.id === "41-3.2").map((request) => request.reader)
    ).toEqual(Array(READER_TRIES).fill("proposal-target"));
    // The other six: four read by both readers, and two with no target by one.
    expect(sent).toHaveLength(READER_TRIES + 4 * 2 + 2);
    for (const id of ["41-1.1", "41-10.5", "75-1", "41-8.3", "41-9.12", "41-9.14"]) {
      expect(s.entry(id).id).toBe(id);
    }
    // Run again: only the item with no entry is read, and the run ends 0.
    const again = recorder.requests.length;
    const second = await run(...s.args);
    expect({ code: second.code, err: second.err }).toEqual({ code: 0, err: "" });
    expect(second.out).toContain("proposals: 7 items, 1 to read;");
    expect(requestsSince(again).map((request) => [request.reader, request.id])).toEqual([
      ["proposal-target", "41-3.2"],
      ["proposal", "41-3.2"],
    ]);
    expect(s.entry("41-3.2").answer).toEqual(proposalAnswer("41-3.2"));
    // An entry of another text of its item than the body's is refused before any call.
    const changed = s.entry("41-3.2");
    writeFileSync(path.join(s.out, "41-3.2.json"), JSON.stringify({ ...changed, text: "older" }));
    const stale = recorder.requests.length;
    const third = await run(...s.args);
    expect(third.code).toBe(2);
    expect(third.err).toContain("is of another state or text of item 41-3.2 than the body's");
    expect(recorder.requests.length).toBe(stale);
  }, 30_000);

  // Catches: a skill target read from another root (the dotfiles copy of a Legion skill's name), a
  // file target read from a skills root, or a target skill given another commit's findings.
  test("gives a Legion skill target its text at the Legion commit and a dotfiles target its text at the dotfiles commit", () => {
    const { s, ran, sent } = full;
    expect({ code: ran.code, err: ran.err }).toEqual({ code: 0, err: "" });
    const proposalText = (id: string) =>
      sent.find((request) => request.reader === "proposal" && request.id === id)?.blocks[0]?.text ??
      "";

    // The Legion skill: its files from the Legion root, never the dotfiles copy of its name.
    const legionSkill = path.join(s.legion, "dispatch-writing-specs");
    const specs = s.entry("41-9.12");
    expect(specs.targets).toEqual([
      {
        kind: "skill",
        repo: LEGION,
        name: "dispatch-writing-specs",
        dir: legionSkill,
        files: [
          {
            path: "SKILL.md",
            sha256: sha256(LEGION_TREE["dispatch-writing-specs/SKILL.md"] ?? ""),
          },
          {
            path: "references/answers.md",
            sha256: sha256(LEGION_TREE["dispatch-writing-specs/references/answers.md"] ?? ""),
          },
        ],
        // The Legion skill's entry has no answer: every reply was refused.
        findings: { line: 4, gaps: null },
        why: "The line left the repository for that skill.",
      },
    ]);
    expect(specs.hunks).toEqual([
      {
        file: "plugins/acme/skills/product-demos/references/pipeline.md",
        hunk: 0,
        header: "@@ -4,7 +4,8 @@",
      },
    ]);
    const specsText = proposalText("41-9.12");
    expect(specsText).toContain(
      `<target kind="skill" repo="${LEGION}" name="dispatch-writing-specs">\n<file path="dispatch-writing-specs/SKILL.md">\n1: ---`
    );
    expect(specsText).toContain("8: 1. Record each answer with its date and a link.");
    expect(specsText).toContain('<file path="dispatch-writing-specs/references/answers.md">');
    expect(specsText).not.toContain("The dotfiles copy of the spec skill");
    expect(specsText).toContain(
      "<findings>\nNone: no target skill has a findings entry with an answer.\n</findings>"
    );

    // The dotfiles file: its text from the repository tree at the dotfiles commit.
    const claude = s.entry("41-10.5");
    expect(claude.targets).toEqual([
      {
        kind: "dotfiles",
        repo: DOTFILES,
        path: ".claude/CLAUDE.md",
        file: path.join(s.dotfiles, ".claude/CLAUDE.md"),
        sha256: sha256(DOTFILES_TREE[".claude/CLAUDE.md"] ?? ""),
        why: "The missing word is in it.",
      },
    ]);
    expect(proposalText("41-10.5")).toContain(
      `<target kind="dotfiles" repo="${DOTFILES}">\n<file path=".claude/CLAUDE.md">\n1: # Agents\n2: \n3: - Do not turn a correction about one action into a rule all agents.`
    );
    // A file the change would add: no text at the dotfiles commit.
    expect(s.entry("75-1").targets[1]).toEqual({
      kind: "dotfiles",
      repo: DOTFILES,
      path: "omp/rules/design-and-asks.md",
      file: path.join(s.dotfiles, "omp/rules/design-and-asks.md"),
      sha256: null,
      why: "The change adds this rule file.",
    });
    expect(proposalText("75-1")).toContain(
      '<file path="omp/rules/design-and-asks.md">\nNone: the repository holds no such file at this commit.\n</file>'
    );

    // A dotfiles skill: its files from the dotfiles skills root, and the findings entry of its
    // source, the dotfiles commit, not an older read's.
    const skill = s.entry("41-1.1");
    expect(skill.targets[0].dir).toBe(path.join(s.dotfilesSkills, "writing-agent-skills"));
    expect(skill.targets[0].findings).toEqual({ line: 2, gaps: 1 });
    const skillText = proposalText("41-1.1");
    expect(skillText).toContain(
      `<finding index="0" skill="writing-agent-skills" repo="${DOTFILES}" file="writing-agent-skills/SKILL.md" line="9" rule="S8" kind="present">\nThe line holds two rules.\n</finding>`
    );
    expect(skillText).not.toContain("A gap of an older commit's read.");
    expect(skillText).toContain(
      `<hunks>\n<hunk file="${SKILL_PATH}" index="0">\n@@ -5,14 +5,12 @@ description:`
    );
    expect(skill.answer).toEqual(proposalAnswer("41-1.1"));
    expect(skill.body).toEqual({
      file: F11,
      source: "acme/dotfiles#105 at ac1b11e8",
      sha256: sha256(body()),
    });
    expect(skill.diff).toEqual({ pr: "41", file: F15, sha256: sha256(readFileSync(F15, "utf8")) });
    expect([skill.state, skill.list, skill.line, skill.noTarget]).toEqual(["open", CARRY, 9, null]);
    expect(skill.reader).toEqual({ prompt: "proposal", settings: loadPrompt("proposal").settings });
    expect(skill.target.reader).toEqual({
      prompt: "proposal-target",
      settings: loadPrompt("proposal-target").settings,
    });
    expect(skill.usd).toBeCloseTo(
      skill.target.usd +
        skill.spent.reduce((sum: number, each: { usd: number }) => sum + each.usd, 0)
    );
    // The user messages sent, both, in the item's input file.
    const input = readFileSync(path.join(s.out, "41-1.1.input.md"), "utf8");
    const targetRequest = sent.find(
      (request) => request.reader === "proposal-target" && request.id === "41-1.1"
    );
    expect(input).toBe(
      `<!-- the user message of the proposal-target request -->\n${targetRequest?.blocks.map((block) => block.text).join("\n\n")}\n\n<!-- the user message of the proposal request -->\n${skillText}\n`
    );
    // Each item's diff is its prefix's: 75-1's request gives the diff of 75, first, cached.
    const seventyFive = sent.find(
      (request) => request.reader === "proposal-target" && request.id === "75-1"
    );
    expect(seventyFive?.blocks[0]?.text).toStartWith(
      `<diff pr="75">\n${readFileSync(F15, "utf8")}`
    );
    expect(seventyFive?.blocks[0]?.cache_control).toEqual({ type: "ephemeral" });
    expect(seventyFive?.blocks[1]?.text).toContain(
      `<skill repo="${LEGION}" name="dispatch-writing-specs"/>\n<skill repo="${LEGION}" name="plain-notes"/>\n<skill repo="${DOTFILES}" name="dispatch-writing-specs"/>\n<skill repo="${DOTFILES}" name="writing-agent-skills"/>`
    );
  });

  // Catches: an item with no target read by the proposal reader anyway, or an entry that cannot
  // say which words of the body the item had.
  test("an item with no target found gets an entry with the body's target words and no target text", () => {
    const { s, ran } = full;
    expect({ code: ran.code, err: ran.err }).toEqual({ code: 0, err: "" });
    const askGate = s.entry("41-8.3");
    expect(askGate.noTarget).toEqual({
      bodyWords:
        "AskGate would let an agent ask you for the purpose of an instruction. AskGate is now in acme/skills `packages/pi-envoy/extensions/askgate/rules.md`. Recommend drop: ACME-11's architect reads no conflict between AskGate and a question for a reason. Not carried.",
      why: ["AskGate's rules are in a repository no listed skill is in."],
    });
    expect([askGate.targets, askGate.reader, askGate.answer, askGate.spent]).toEqual([
      [],
      null,
      null,
      [],
    ]);
    expect(askGate.target.answer).toEqual(TARGET_ANSWERS["41-8.3"]);
    expect(askGate.usd).toBe(askGate.target.usd);
    expect(ran.out).toContain("proposals: 41-8.3: no target found;");
    const sent = full.sent.filter((request) => request.id === "41-8.3");
    expect(sent.map((request) => request.reader)).toEqual(["proposal-target"]);
    const input = readFileSync(path.join(s.out, "41-8.3.input.md"), "utf8");
    expect(input).toStartWith("<!-- the user message of the proposal-target request -->\n");
    expect(input).not.toContain("<!-- the user message of the proposal request -->");
    expect(s.entry("41-9.14").noTarget.why).toEqual(["Its new home holds it already."]);
  });

  // Catches: every item of a pull request sent at once, so that none reads the diff from the
  // prompt cache that the first one's request writes.
  test("the first item of each pull request is read alone, before the others of it", () => {
    expect(full.ran.code).toBe(0);
    const sent = full.sent.map((request) => `${request.reader} ${request.id}`);
    expect(sent.slice(0, 4).sort()).toEqual([
      "proposal 41-1.1",
      "proposal 75-1",
      "proposal-target 41-1.1",
      "proposal-target 75-1",
    ]);
    expect(sent).toHaveLength(7 * 2 - 2);
  });

  // Catches: the proposal-target answer checked against one file of the diff and the entry's hunk
  // taken from another, where one path names two files: a file renamed away from it, and a new
  // file at it.
  test("a hunk's file is the diff file the answer check took, where a renamed file's old path is a new file's path", async () => {
    const s = scenario("hunk-file");
    const dir = path.join(scratch, "hunk-file");
    const body = path.join(dir, "made-up-body.md");
    writeFileSync(
      body,
      "### Made-up proposals\n\nLines:\n\n- `41-20.1` (open): the new rules file.\n"
    );
    // rules.md renamed to rules-old.md with two hunks, then a new rules.md with one.
    const diff = path.join(dir, "made-up.diff");
    writeFileSync(
      diff,
      [
        "diff --git a/rules.md b/rules-old.md",
        "similarity index 80%",
        "rename from rules.md",
        "rename to rules-old.md",
        "index 1111111..2222222 100644",
        "--- a/rules.md",
        "+++ b/rules-old.md",
        "@@ -1,2 +1,2 @@",
        " # Rules",
        "-Old rule.",
        "+Old rule, kept.",
        "@@ -10,2 +10,2 @@",
        " Tail.",
        "-Old tail.",
        "+New tail.",
        "diff --git a/rules.md b/rules.md",
        "new file mode 100644",
        "index 0000000..3333333",
        "--- /dev/null",
        "+++ b/rules.md",
        "@@ -0,0 +1,2 @@",
        "+# Rules",
        "+The new rule.",
        "",
      ].join("\n")
    );
    const swap: Readonly<Record<string, string>> = {
      [F11]: body,
      [SECTION]: "Made-up proposals",
      [CARRY]: "Lines",
      [`41=${F15}`]: `41=${diff}`,
    };
    const args = s.args
      .filter((arg, at, all) => arg !== GONE && all[at + 1] !== GONE)
      .map((arg) => swap[arg] ?? arg);
    const from = recorder.requests.length;
    const ran = await run(...args);
    expect({ code: ran.code, err: ran.err }).toEqual({ code: 0, err: "" });
    // The answer names rules.md: the new file, the one the diff shows at that path.
    expect(s.entry("41-20.1").hunks).toEqual([
      { file: "rules.md", hunk: 0, header: "@@ -0,0 +1,2 @@" },
    ]);
    const proposal = requestsSince(from).find((request) => request.reader === "proposal");
    expect(proposal?.blocks[0]?.text).toContain(
      '<hunk file="rules.md" index="0">\n@@ -0,0 +1,2 @@\n+# Rules\n+The new rule.'
    );
  }, 30_000);

  test("refuses, before any call, an item whose prefix no --pr-diff gives, a --pr-diff that is no diff, and a skills root with no skill", async () => {
    const s = scenario("refusals");
    const from = recorder.requests.length;
    const without = s.args.filter(
      (arg, at, all) => arg !== `75=${F15}` && all[at + 1] !== `75=${F15}`
    );
    const noDiff = await run(...without);
    expect(noDiff.code).toBe(2);
    expect(noDiff.err).toContain("no --pr-diff gives the diff of 75 (75-1)");
    const notDiff = await run(...s.args.map((arg) => (arg === `75=${F15}` ? `75=${F11}` : arg)));
    expect(notDiff.code).toBe(2);
    expect(notDiff.err).toContain(
      `the diff ${F11} of --pr-diff 75 is refused: line 1 of the diff starts no file`
    );
    const empty = path.join(scratch, "refusals", "empty-root");
    mkdirSync(empty, { recursive: true });
    const noSkill = await run(
      ...s.args.map((arg) => (arg === `${LEGION}=${s.legion}` ? `${LEGION}=${empty}` : arg))
    );
    expect(noSkill.code).toBe(2);
    expect(noSkill.err).toContain("holds no <name>/SKILL.md");
    expect(recorder.requests.length).toBe(from);
    expect(existsSync(s.out)).toBe(false);
  }, 30_000);
});
