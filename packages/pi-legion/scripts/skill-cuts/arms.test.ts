import { beforeAll, expect, test } from "bun:test";
import { lstatSync, mkdirSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import path from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import {
  SessionEntry,
  type SessionMessage,
  skillHeaderPath,
  stripSkillHeader,
} from "../skill-scenarios/transcript";
import { type Arm, type ArmName, buildArm, skillFile, unknownSkill } from "./arms";
import { type Case, pathHash } from "./cases";

// The arms of a case, on F1 (testdata/F1): an Oh My Pi 18.18.6 session of a scratch profile with
// the made-up skills alpha and beta, captured with `HOME=<scratch> omp -p
// --session-dir <scratch>/sessions --model anthropic/claude-sonnet-5-5 "<prompt>"`
// (testdata/CAPTURE.md). Its messages are: 0 the user's prompt, 1 a read of skill://alpha, 2 its
// result, 3 a read of skill://alpha/references/x.md, 4 its result, 5 a read of the missing
// skill://gamma, 6 its not-found result, 7 a `task` call (the step here), and the rest.

const F1 = path.join(
  import.meta.dir,
  "testdata",
  "F1",
  "2026-11-20T01-59-41-780Z_01a1f6ae-51d4-715f-a2a6-59f275bdff80.jsonl"
);
const scratch = temporaryDirectory("skill-cuts-arms", { real: true });
const LINE = "2. Write each step's result on one line.";
const PLANTED = "Run the tests where needed.";

/** F1's messages, in order. */
function f1Messages(): SessionMessage[] {
  return readFileSync(F1, "utf8")
    .split("\n")
    .filter((line) => line.trim() !== "")
    .map((line) => SessionEntry.parse(JSON.parse(line)))
    .flatMap((entry) => (entry.type === "message" && entry.message ? [entry.message] : []));
}
const messages = f1Messages();

/** The one text of a message. */
function textOf(message: SessionMessage | undefined): string {
  return message?.content?.[0]?.text ?? "";
}

/** A message's `details`, the tool's own record, as an object. */
function detailsOf(message: SessionMessage | undefined): Record<string, unknown> {
  const details = message?.details;
  return typeof details === "object" && details !== null ? { ...details } : {};
}

/** The file F1's read result at `index` read, as it was on disk: its text less the header, with
 * the newline the read drops. */
function f1File(index: number): string {
  return `${stripSkillHeader(textOf(messages[index]))}\n`;
}

/** A skill directory under the scratch directory holding `files` (path inside it to text). */
function skillDir(name: string, files: Record<string, string>): string {
  const dir = path.join(scratch, name);
  for (const [file, text] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(dir, file)), { recursive: true });
    writeFileSync(path.join(dir, file), text);
  }
  return dir;
}

/** The old text: alpha's files as F1 read them. The new text: a SKILL.md with the line
 * rewritten, and no references/x.md; the whole new text also rewrites references/x.md. Written
 * once the scratch directory is made. */
const oldDir = path.join(scratch, "old");
const newSkill = f1File(2).replace(LINE, "2. Put each step's result on a line of its own.");
const newX = f1File(4).replace("counted from one", "numbered from one");
const newDir = path.join(scratch, "new");
const wholeNewDir = path.join(scratch, "whole-new");
beforeAll(() => {
  skillDir("old", { "SKILL.md": f1File(2), "references/x.md": f1File(4) });
  skillDir("new", { "SKILL.md": newSkill });
  skillDir("whole-new", { "SKILL.md": newSkill, "references/x.md": newX });
});
/** Where F1's profile installed alpha, as its first `[Skill file: …]` line shows it. */
const INSTALLED = path.dirname(skillHeaderPath(textOf(messages[2])) ?? "");

/** The case of F1's `task` step, which line 9 of alpha's SKILL.md governs. */
const f1Case: Case = {
  id: "c1",
  cut: "acme-skills-1",
  skill: "alpha",
  baseFile: "SKILL.md",
  line: { text: LINE, lineNumber: 9, lineCount: 10 },
  criterion: {
    pass: "The reply writes the result of step 4 on one line.",
    fail: "The reply spreads a step's result over more than one line.",
  },
  model: "anthropic/claude-sonnet-5-5",
  thinking: "off",
  context: "devbox",
  profile: {
    skills: [
      { repo: "acme/skills", commit: "0123456789abcdef", path: "skills/alpha", name: "alpha" },
      { repo: "acme/skills", commit: "0123456789abcdef", path: "skills/beta", name: "beta" },
    ],
    plugins: [],
    contextFiles: [],
  },
  messages: messages.slice(0, 7),
  skillReads: [2, 4],
  realStep: messages[7] ?? {},
  violatingStep: { role: "assistant", content: [{ type: "text", text: "Step 4." }] },
  source: { store: "store_a", pathHash: pathHash("/sessions/alpha.jsonl") },
  sessionStart: "2026-11-20T01:59:41.780Z",
  secretsRemoved: 0,
  sessionDigest: "46874d907e557998",
};
const original = structuredClone(f1Case);
/** The ids of the case's reads of alpha's files. */
const readIds = f1Case.skillReads.map((index) => messages[index]?.toolCallId);

function arm(name: ArmName, plantedLine?: string): Arm {
  const dirs = { old: oldDir, new: newDir, ...(plantedLine === undefined ? {} : { plantedLine }) };
  return buildArm(f1Case, name, dirs, path.join(scratch, `scratch-${name}-${plantedLine ?? ""}`));
}

/** A read result of the skill as F1 holds one, at F1's installed path: the header, then the
 * file's text less its newline. */
function readResult(file: string, text: string): string {
  return `[Skill file: ${path.join(INSTALLED, file)}]\n${text.replace(/\n$/, "")}`;
}

test("every read result of the skill's files is replaced by the arm's file with its header", () => {
  const old = arm("old");
  expect(old.skillDir).toBe(oldDir);
  expect(old.installedDir).toBe(INSTALLED);
  // Built from the files F1 read, the old arm gives F1's own messages, byte for byte.
  expect(JSON.stringify(old.messages)).toBe(JSON.stringify(f1Case.messages));
  const fresh = arm("new");
  expect(textOf(fresh.messages[2])).toBe(readResult("SKILL.md", newSkill));
  expect(fresh.messages[2]?.details).toEqual({
    ...detailsOf(messages[2]),
    displayContent: {
      text: newSkill.replace(/\n$/, ""),
      startLine: 1,
      lineNumbers: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10],
    },
  });
  // A new read, as replay asks one, is answered at the same installed path.
  expect(skillFile(fresh, "SKILL.md")).toEqual({
    text: readResult("SKILL.md", newSkill),
    isError: false,
  });
});

test("a case whose reads show no installed path gets the same neutral one in every arm", () => {
  const bare = structuredClone(f1Case);
  for (const index of bare.skillReads) {
    const part = bare.messages[index]?.content?.[0];
    if (part) part.text = stripSkillHeader(part.text ?? "");
  }
  for (const name of ["old", "new", "none", "minus"] as const) {
    const each = buildArm(bare, name, { old: oldDir, new: newDir }, path.join(scratch, "bare"));
    expect(each.installedDir).toBe("/skills/alpha");
  }
  const old = buildArm(bare, "old", { old: oldDir, new: newDir }, path.join(scratch, "bare"));
  expect(textOf(old.messages[2])).toBe(`[Skill file: /skills/alpha/SKILL.md]\n${f1File(2).trim()}`);
  expect(skillFile(old, "references/x.md").text).toStartWith(
    "[Skill file: /skills/alpha/references/x.md]\n"
  );
});

test("a file the arm lacks gets Oh My Pi's not-found result, in F1's shape", () => {
  const fresh = arm("new");
  const gamma = messages[6];
  expect(textOf(gamma)).toBe("Unknown skill: gamma\nAvailable: alpha, beta");
  const notFound = `File not found: ${path.join(INSTALLED, "references/x.md")}`;
  expect(fresh.messages[4]).toEqual({
    ...gamma,
    toolCallId: messages[4]?.toolCallId,
    timestamp: messages[4]?.timestamp,
    content: [{ type: "text", text: notFound }],
  });
  expect(skillFile(fresh, "references/x.md")).toEqual({ text: notFound, isError: true });
  // An arm without the skill's directory answers as F1's read of the missing skill gamma did.
  const noGamma: Arm = { ...arm("none"), skill: "gamma", profileSkills: f1Case.profile.skills };
  expect(skillFile(noGamma, "SKILL.md")).toEqual({ text: textOf(gamma), isError: true });
  expect(skillFile(arm("none"), "SKILL.md")).toEqual({
    text: "Unknown skill: alpha\nAvailable: beta",
    isError: true,
  });
});

test("none removes each read call of the skill and its result, and leaves the skill out of its profile", () => {
  const none = arm("none");
  expect(none.skillDir).toBeUndefined();
  expect(none.profileSkills.map((skill) => skill.name)).toEqual(["beta"]);
  const parts = none.messages.flatMap((message) => message.content ?? []);
  expect(parts.filter((part) => readIds.includes(part.id))).toEqual([]);
  expect(none.messages.filter((message) => readIds.includes(message.toolCallId))).toEqual([]);
  // The assistant's own words stay; the gamma read, which is no read of alpha, stays whole.
  expect(none.messages).toEqual([
    messages[0],
    { ...messages[1], content: [{ type: "text", text: "Step 1." }] },
    { ...messages[3], content: [{ type: "text", text: "Step 2." }] },
    messages[5],
    messages[6],
  ]);
});

test("none leaves out every copy of the skill's source, a namespaced one too, and keeps a copy of another source that shares the name; each other arm keeps them all", () => {
  const twice = structuredClone(f1Case);
  twice.profile.skills.push(
    // acme/skills' alpha again at another commit, which Oh My Pi lists as pack/alpha.
    {
      repo: "acme/skills",
      commit: "fedcba9876543210",
      path: "skills/alpha",
      name: "alpha",
      namespace: "pack",
    },
    // Another repository's alpha, listed as tools/alpha.
    {
      repo: "acme/tools",
      commit: "0123456789abcdef",
      path: "skills/alpha",
      name: "alpha",
      namespace: "tools",
    }
  );
  const dirs = { old: oldDir, new: newDir };
  const none = buildArm(twice, "none", dirs, path.join(scratch, "twice"));
  expect(
    none.profileSkills.map((skill) => `${skill.repo} ${skill.namespace ?? ""}/${skill.name}`)
  ).toEqual(["acme/skills /beta", "acme/tools tools/alpha"]);
  // Its not-found answer lists the names Oh My Pi lists.
  expect(skillFile(none, "SKILL.md").text).toBe(
    "Unknown skill: alpha\nAvailable: beta, tools/alpha"
  );
  for (const name of ["old", "new", "minus", "twin"] as const) {
    expect(buildArm(twice, name, dirs, path.join(scratch, "twice")).profileSkills).toEqual(
      twice.profile.skills
    );
  }
});

test("minus removes the case's line, or the planted line when one is given", () => {
  const minus = arm("minus");
  expect(minus.skillDir).toBe(path.join(scratch, "scratch-minus-", "minus"));
  const less = f1File(2).replace(`${LINE}\n`, "");
  expect(readFileSync(path.join(minus.skillDir ?? "", "SKILL.md"), "utf8")).toBe(less);
  expect(textOf(minus.messages[2])).toBe(readResult("SKILL.md", less));
  expect(textOf(minus.messages[4])).toBe(readResult("references/x.md", f1File(4)));
  // The old directory keeps its text.
  expect(readFileSync(path.join(oldDir, "SKILL.md"), "utf8")).toBe(f1File(2));

  const plantedOld = skillDir("old-planted", {
    "SKILL.md": f1File(2),
    "references/x.md": `${f1File(4)}${PLANTED}\n`,
  });
  const planted = buildArm(
    f1Case,
    "minus",
    { old: plantedOld, new: newDir, plantedLine: PLANTED },
    path.join(scratch, "scratch-planted")
  );
  const dir = planted.skillDir ?? "";
  expect(readFileSync(path.join(dir, "SKILL.md"), "utf8")).toBe(f1File(2));
  expect(readFileSync(path.join(dir, "references/x.md"), "utf8")).toBe(f1File(4));
  expect(() => arm("minus", "A line no file holds.")).toThrow(
    `case c1: the minus arm would equal old: the planted line is in no file of ${oldDir}`
  );
});

test("minus copies each link of the old directory as the file it names, so its edit never writes through a link, and leaves out every copy of the line", () => {
  // The old SKILL.md is a link to a file outside the directory, its reference a relative link.
  const shared = skillDir("linked-shared", { "alpha.md": f1File(2), "x.md": f1File(4) });
  const linked = path.join(scratch, "linked-old");
  mkdirSync(path.join(linked, "references"), { recursive: true });
  symlinkSync(path.join(shared, "alpha.md"), path.join(linked, "SKILL.md"));
  symlinkSync("../../linked-shared/x.md", path.join(linked, "references", "x.md"));
  const dirs = { old: linked, new: newDir };
  const minus = buildArm(f1Case, "minus", dirs, path.join(scratch, "scratch-linked"));
  const dir = minus.skillDir ?? "";
  expect(lstatSync(path.join(dir, "SKILL.md")).isSymbolicLink()).toBe(false);
  expect(readFileSync(path.join(dir, "SKILL.md"), "utf8")).toBe(f1File(2).replace(`${LINE}\n`, ""));
  expect(readFileSync(path.join(dir, "references/x.md"), "utf8")).toBe(f1File(4));
  // The files the links name keep their text.
  expect(readFileSync(path.join(shared, "alpha.md"), "utf8")).toBe(f1File(2));
  const planted = buildArm(
    f1Case,
    "minus",
    { ...dirs, plantedLine: f1File(4).trimEnd().split("\n").at(-1) ?? "" },
    path.join(scratch, "scratch-linked-planted")
  );
  expect(readFileSync(path.join(planted.skillDir ?? "", "references/x.md"), "utf8")).not.toBe(
    f1File(4)
  );
  expect(readFileSync(path.join(shared, "x.md"), "utf8")).toBe(f1File(4));

  // A rule written twice is still the rule: minus leaves out each line that is the case's line.
  const twice = skillDir("twice-old", {
    "SKILL.md": f1File(2).replace(`${LINE}\n`, `${LINE}\n${LINE}\n`),
    "references/x.md": f1File(4),
  });
  const less = buildArm(f1Case, "minus", { old: twice, new: newDir }, path.join(scratch, "twice"));
  expect(readFileSync(path.join(less.skillDir ?? "", "SKILL.md"), "utf8")).toBe(
    f1File(2).replace(`${LINE}\n`, "")
  );
});

// Catches: a link of the old directory that names nothing failing the minus arm with a raw error,
// where old answers a read of it as not found.
test("minus answers a read of a link of the old directory that names nothing as not found, as old does", () => {
  const dangling = skillDir("dangling-old", {
    "SKILL.md": f1File(2),
    "references/x.md": f1File(4),
  });
  symlinkSync("nowhere.md", path.join(dangling, "references", "gone.md"));
  const dirs = { old: dangling, new: newDir };
  const old = buildArm(f1Case, "old", dirs, path.join(scratch, "scratch-dangling"));
  const minus = buildArm(f1Case, "minus", dirs, path.join(scratch, "scratch-dangling"));
  expect(skillFile(minus, "references/gone.md")).toEqual(skillFile(old, "references/gone.md"));
  expect(skillFile(minus, "references/gone.md").isError).toBe(true);
  expect(readFileSync(path.join(minus.skillDir ?? "", "SKILL.md"), "utf8")).toBe(
    f1File(2).replace(`${LINE}\n`, "")
  );
});

// Catches: a link to a directory that holds the old directory copied into itself without end.
test("minus refuses an old directory with a link to a directory that holds it, naming the link", () => {
  const looped = skillDir("looped-old", { "SKILL.md": f1File(2) });
  symlinkSync("..", path.join(looped, "up"));
  expect(() =>
    buildArm(f1Case, "minus", { old: looped, new: newDir }, path.join(scratch, "scratch-looped"))
  ).toThrow(`${path.join(looped, "up")} names a directory that holds ${looped}`);
});

/** An arm's messages as JSON with the text of the skill's files taken out: each read result of
 * them keeps its header line, and the text after it and the tool's record of that text go. */
function outsideFileTexts(each: Arm): string {
  return JSON.stringify(
    each.messages.map((message) => {
      if (!readIds.includes(message.toolCallId)) return message;
      const header = textOf(message).split("\n")[0];
      const details = { ...detailsOf(message), totalLines: 0, displayContent: {} };
      return { ...message, content: [{ type: "text", text: header }], details };
    })
  );
}

test("only the skill's directory differs between arms", () => {
  const dirs = { old: oldDir, new: wholeNewDir };
  const build = (name: ArmName) => buildArm(f1Case, name, dirs, path.join(scratch, "whole"));
  const [old, fresh, minus, none] = [build("old"), build("new"), build("minus"), build("none")];
  // Outside the skill's file texts, headers included, the arms with the skill are byte-identical.
  expect(outsideFileTexts(fresh)).toBe(outsideFileTexts(old));
  expect(outsideFileTexts(minus)).toBe(outsideFileTexts(old));
  for (const index of f1Case.skillReads) {
    for (const each of [old, fresh, minus]) {
      expect(skillHeaderPath(textOf(each.messages[index]))).toBe(
        skillHeaderPath(textOf(messages[index]))
      );
    }
  }
  expect(textOf(fresh.messages[2])).not.toBe(textOf(old.messages[2]));
  expect(textOf(fresh.messages[4])).toBe(readResult("references/x.md", newX));
  // None is the others less the skill's read calls and their results.
  const withoutReads = old.messages.flatMap((message) => {
    if (readIds.includes(message.toolCallId)) return [];
    const kept = (message.content ?? []).filter((part) => !readIds.includes(part.id));
    return [{ ...message, content: kept }];
  });
  expect(JSON.stringify(none.messages)).toBe(JSON.stringify(withoutReads));
  for (const each of [old, fresh, minus, none]) {
    expect(each.installedDir).toBe(INSTALLED);
    expect(each.skill).toBe("alpha");
    // No arm's directory, nor the scratch directory they are under, reaches its messages.
    expect(JSON.stringify(each.messages)).not.toContain(scratch);
  }
  for (const each of [old, fresh, minus]) expect(each.profileSkills).toEqual(f1Case.profile.skills);
  // No arm changes the case it was built from.
  expect(f1Case).toEqual(original);
});

// Catches: a false-loss control whose second arm is not the old text, or is a copy the model can
// tell from it.
test("twin is the old arm again: the old directory, the same messages and profile, its own name", () => {
  const old = arm("old");
  const twin = arm("twin");
  expect(twin.name).toBe("twin");
  expect(twin.skillDir).toBe(old.skillDir ?? "");
  expect(twin.skillDir).toBe(path.resolve(oldDir));
  expect(JSON.stringify(twin.messages)).toBe(JSON.stringify(old.messages));
  expect(twin.profileSkills).toEqual(old.profileSkills);
  expect(twin.installedDir).toBe(old.installedDir);
  expect(f1Case).toEqual(original);
});

// The arm of ours (`--ours`): a skill whose name may differ from the case's skill. `alpha-ours`
// is alpha's directory renamed (its frontmatter name and nothing else changed); `alpha-short`
// holds a SKILL.md of its own and no references/x.md.
const OURS_INSTALLED = path.join(path.dirname(INSTALLED), "alpha-ours");
const SHORT_SKILL =
  "---\nname: alpha-short\ndescription: Use for the alpha checklist, in one line.\n---\n\n# Alpha, short\n\n2. Write each step's result on one line.\n";
const renamedDir = path.join(scratch, "alpha-ours");
const shortDir = path.join(scratch, "alpha-short");
beforeAll(() => {
  skillDir("alpha-ours", {
    "SKILL.md": f1File(2).replace("name: alpha\n", "name: alpha-ours\n"),
    "references/x.md": f1File(4),
  });
  skillDir("alpha-short", { "SKILL.md": SHORT_SKILL });
});

/** The arm `name` of `c` with `ours` (its directory and its name) as the arm of ours. */
function withOurs(c: Case, name: ArmName, ours: { dir: string; name: string }): Arm {
  const dirs = { old: oldDir, new: newDir, ours };
  return buildArm(c, name, dirs, path.join(scratch, `scratch-ours-${ours.name}`));
}
const RENAMED = { dir: renamedDir, name: "alpha-ours" };
const SHORT = { dir: shortDir, name: "alpha-short" };

test("old holds the profile's skills; ours holds them with ours in place of theirs, a skill of our name replaced, not doubled; none holds them less theirs", () => {
  const [alpha, beta] = f1Case.profile.skills;
  expect(withOurs(f1Case, "old", RENAMED).profileSkills).toEqual(f1Case.profile.skills);
  const ours = withOurs(f1Case, "ours", RENAMED);
  expect(ours.name).toBe("ours");
  expect(ours.skill).toBe("alpha-ours");
  expect(ours.skillDir).toBe(path.resolve(renamedDir));
  expect(ours.installedDir).toBe(OURS_INSTALLED);
  expect(ours.profileSkills).toEqual([{ ...alpha, name: "alpha-ours" }, beta]);
  expect(ours.ours).toEqual({ theirs: "alpha", replacedOwn: false, removedReads: [] });
  expect(withOurs(f1Case, "none", RENAMED).profileSkills).toEqual([beta]);

  // A profile that holds a skill of our name already: the --ours text replaces that entry, so
  // the name is installed once, and the arm records that it replaced it.
  const incumbent = {
    repo: "acme/dotfiles",
    commit: "fedcba9876543210",
    path: "skills/alpha-ours",
    name: "alpha-ours",
  };
  const holding: Case = {
    ...f1Case,
    profile: { ...f1Case.profile, skills: [...f1Case.profile.skills, incumbent] },
  };
  const bundled = withOurs(holding, "ours", RENAMED);
  expect(bundled.profileSkills).toEqual([beta, incumbent]);
  expect(bundled.ours?.replacedOwn).toBe(true);
  expect(withOurs(holding, "old", RENAMED).profileSkills).toEqual(holding.profile.skills);
  expect(withOurs(holding, "none", RENAMED).profileSkills).toEqual([beta, incumbent]);

  // Ours under their skill's own name stands in their skill's place, as the cut's text does.
  const same = withOurs(f1Case, "ours", { dir: renamedDir, name: "alpha" });
  expect(same.profileSkills).toEqual(f1Case.profile.skills);
  expect(same.ours?.replacedOwn).toBe(false);
  expect(f1Case).toEqual(original);
});

// Catches: a namespaced copy of their skill's source kept in the arm of ours, where its old text
// stays readable at another path, or ours listed twice.
test("the ours arm leaves out every copy of their skill's source, a namespaced one too, and lists ours once under its own name", () => {
  const [alpha, beta] = f1Case.profile.skills;
  // Another repository's alpha, listed as tools/alpha: it stays as the profile has it.
  const tools = {
    repo: "acme/tools",
    commit: "0123456789abcdef",
    path: "skills/alpha",
    name: "alpha",
    namespace: "tools",
  };
  const copies = structuredClone(f1Case);
  copies.profile.skills.push(
    // acme/skills' alpha again at another commit, which Oh My Pi lists as pack/alpha.
    {
      repo: "acme/skills",
      commit: "fedcba9876543210",
      path: "skills/alpha",
      name: "alpha",
      namespace: "pack",
    },
    tools
  );
  const ours = withOurs(copies, "ours", RENAMED);
  expect(ours.profileSkills).toEqual([{ ...alpha, name: "alpha-ours" }, beta, tools]);
  expect(ours.ours?.replacedOwn).toBe(false);
  // A read of theirs is answered with the names Oh My Pi lists, ours once under its own name.
  expect(unknownSkill(ours, "alpha").text).toBe(
    "Unknown skill: alpha\nAvailable: alpha-ours, beta, tools/alpha"
  );

  // A profile that lists a skill of another source under our name keeps it, the --ours text in
  // its place, and no copy of theirs.
  const incumbent = {
    repo: "acme/dotfiles",
    commit: "fedcba9876543210",
    path: "skills/alpha-ours",
    name: "alpha-ours",
  };
  copies.profile.skills.push(incumbent);
  const bundled = withOurs(copies, "ours", RENAMED);
  expect(bundled.profileSkills).toEqual([beta, tools, incumbent]);
  expect(bundled.ours?.replacedOwn).toBe(true);
  expect(f1Case).toEqual(original);
});

test("each history read mapped by its path inside the skill, or removed when ours lacks the file", () => {
  const short = withOurs(f1Case, "ours", SHORT);
  const shortInstalled = path.join(path.dirname(INSTALLED), "alpha-short");
  const [call] = (messages[1]?.content ?? []).filter((part) => part.type === "toolCall");
  const read = messages[2];
  const shown = path.join(shortInstalled, "SKILL.md");
  const body = SHORT_SKILL.replace(/\n$/, "");
  expect(short.messages).toEqual([
    messages[0],
    {
      ...messages[1],
      content: [
        { type: "text", text: "Step 1." },
        { ...call, arguments: { ...Object(call?.arguments), path: "skill://alpha-short" } },
      ],
    },
    {
      ...read,
      content: [{ type: "text", text: `[Skill file: ${shown}]\n${body}` }],
      details: {
        ...detailsOf(read),
        resolvedPath: shown,
        totalLines: 8,
        displayContent: { text: body, startLine: 1, lineNumbers: [1, 2, 3, 4, 5, 6, 7, 8] },
        meta: { source: { type: "internal", value: "skill://alpha-short" } },
      },
    },
    // references/x.md: ours has no such file, so its read and result go, as none removes them.
    { ...messages[3], content: [{ type: "text", text: "Step 2." }] },
    messages[5],
    messages[6],
  ]);
  // A new read, as replay asks one, is answered from our directory at our installed path.
  expect(skillFile(short, "SKILL.md")).toEqual({
    text: `[Skill file: ${shown}]\n${body}`,
    isError: false,
  });
  expect(JSON.stringify(short.messages)).not.toContain(scratch);
});

test("a renamed unchanged copy gives old's texts", () => {
  const old = withOurs(f1Case, "old", RENAMED);
  const ours = withOurs(f1Case, "ours", RENAMED);
  // But for its name (in the read's URL, the installed path and the frontmatter), every text and
  // record of the history is old's, byte for byte, and so is the profile.
  expect(JSON.stringify(ours.messages)).not.toBe(JSON.stringify(old.messages));
  expect(JSON.stringify(ours.messages).replaceAll("alpha-ours", "alpha")).toBe(
    JSON.stringify(old.messages)
  );
  expect(JSON.stringify(ours.profileSkills).replaceAll("alpha-ours", "alpha")).toBe(
    JSON.stringify(old.profileSkills)
  );
  expect(skillFile(ours, "references/x.md").text.replaceAll("alpha-ours", "alpha")).toBe(
    skillFile(old, "references/x.md").text
  );
});

test("the ours arm records each history read it removed for want of our file at that path", () => {
  expect(withOurs(f1Case, "ours", SHORT).ours?.removedReads).toEqual(["references/x.md"]);
  expect(withOurs(f1Case, "ours", RENAMED).ours?.removedReads).toEqual([]);
  // No other arm carries the record.
  for (const name of ["old", "new", "none", "minus", "twin"] as const) {
    expect(withOurs(f1Case, name, SHORT).ours).toBeUndefined();
  }
  expect(() => buildArm(f1Case, "ours", { old: oldDir }, path.join(scratch, "no-ours"))).toThrow(
    "case c1: the ours arm needs --ours"
  );
});
