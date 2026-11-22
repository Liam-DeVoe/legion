import { expect, test } from "bun:test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { SessionEntry, type SessionMessage, stripSkillHeader } from "../skill-scenarios/transcript";
import {
  type Case,
  caseKind,
  loadCases,
  pathHash,
  pathReadFile,
  resolvedReadPath,
  skillReadFile,
} from "./cases";

// The loader of the case file, on F1 (testdata/F1): an Oh My Pi 18.18.6 session of a scratch
// profile with the made-up skills alpha and beta, captured with
// `HOME=<scratch> omp -p --session-dir <scratch>/sessions --model anthropic/claude-sonnet-5-5
// "<prompt>"` (testdata/CAPTURE.md). Its messages are: 0 the user's prompt, 1 a read of
// skill://alpha, 2 its result, 3 a read of skill://alpha/references/x.md, 4 its result, 5 a read
// of the missing skill://gamma, 6 its not-found result, 7 a `task` call (the step here), and the
// rest of the session.

const F1 = path.join(
  import.meta.dir,
  "testdata",
  "F1",
  "2026-11-20T01-59-41-780Z_01a1f6ae-51d4-715f-a2a6-59f275bdff80.jsonl"
);
const scratch = temporaryDirectory("skill-cuts-cases", { real: true });

/** F1's messages, in order. */
function f1Messages(): SessionMessage[] {
  return readFileSync(F1, "utf8")
    .split("\n")
    .filter((line) => line.trim() !== "")
    .map((line) => SessionEntry.parse(JSON.parse(line)))
    .flatMap((entry) => (entry.type === "message" && entry.message ? [entry.message] : []));
}

/** The text of F1's read result at `index`, less its header, with the newline the read drops. */
function readText(messages: SessionMessage[], index: number): string {
  return `${stripSkillHeader(messages[index]?.content?.[0]?.text ?? "")}\n`;
}

/** A skill directory under the scratch directory holding the alpha files F1 read. */
function alphaDir(name: string): string {
  const messages = f1Messages();
  const dir = path.join(scratch, name);
  mkdirSync(path.join(dir, "references"), { recursive: true });
  writeFileSync(path.join(dir, "SKILL.md"), readText(messages, 2));
  writeFileSync(path.join(dir, "references", "x.md"), readText(messages, 4));
  return dir;
}

/** A case of F1's `task` step, which line 9 of alpha's SKILL.md governs. */
function f1Case(id = "c1"): Case {
  const messages = f1Messages();
  return {
    id,
    cut: "acme-skills-1",
    skill: "alpha",
    baseFile: "SKILL.md",
    line: { text: "2. Write each step's result on one line.", lineNumber: 9, lineCount: 10 },
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
      contextFiles: [{ repo: "acme/project", commit: "fedcba9876543210", path: "AGENTS.md" }],
    },
    messages: messages.slice(0, 7),
    skillReads: [2, 4],
    realStep: messages[7] ?? {},
    violatingStep: {
      role: "assistant",
      content: [
        { type: "text", text: "Step 4." },
        { type: "toolCall", id: "violating_1", name: "read", arguments: { path: "skill://beta" } },
      ],
    },
    source: { store: "store_a", pathHash: pathHash("/sessions/alpha.jsonl") },
    sessionStart: "2026-11-20T01:59:41.780Z",
    secretsRemoved: 0,
    sessionDigest: "46874d907e557998",
  };
}

/** A case file under the scratch directory holding `rows`, one JSON line each. */
function caseFile(name: string, rows: unknown[]): string {
  const file = path.join(scratch, `${name}.jsonl`);
  writeFileSync(file, rows.map((row) => `${JSON.stringify(row)}\n`).join(""));
  return file;
}

test("a case of F1 loads", () => {
  const base = alphaDir("base-loads");
  const cases = loadCases(caseFile("loads", [f1Case()]), base);
  expect(cases).toEqual([f1Case()]);
  expect(cases[0]?.messages[2]?.content?.[0]?.text).toStartWith("[Skill file: /");
});

test("a case file is read line by line: a case longer than one read, and the line numbers after it", () => {
  const base = alphaDir("base-long");
  // A user message of more than 2 MiB, of characters of 2, 3 and 4 bytes, so a line spans reads.
  const long = f1Case("c-long");
  const words = "é ✓ 𝄞 ".repeat(Math.ceil((5 * 2 ** 19) / 14));
  long.messages = [{ role: "user", content: [{ type: "text", text: words }] }, ...long.messages];
  long.skillReads = long.skillReads.map((index) => index + 1);
  const file = path.join(scratch, "long.jsonl");
  writeFileSync(file, `${JSON.stringify(long)}\n\nnot json\n${JSON.stringify(f1Case("c2"))}`);
  expect(() => loadCases(file, base)).toThrow(`refused cases:\n${file}:3 is not JSON: `);
  writeFileSync(file, `${JSON.stringify(long)}\r\n\n${JSON.stringify(f1Case("c2"))}`);
  const cases = loadCases(file, base);
  expect(cases.map((c) => c.id)).toEqual(["c-long", "c2"]);
  expect(cases[0]?.messages[0]?.content?.[0]?.text).toBe(words);
});

test("a case with the builder's evidence loads, and an evidence entry with no source is refused", () => {
  const base = alphaDir("base-evidence");
  const evidence = [
    { item: "role", source: "no role: the context is devbox" },
    { item: "skill alpha", source: "[Skill file: …/skills/alpha/SKILL.md] at message 2" },
  ];
  const withEvidence = { ...f1Case("c-evidence"), evidence };
  expect(loadCases(caseFile("evidence", [withEvidence]), base)).toEqual([withEvidence]);
  const blank = { ...f1Case("c-blank"), evidence: [{ item: "role", source: "" }] };
  const file = caseFile("evidence-blank", [blank]);
  expect(() => loadCases(file, base)).toThrow(
    `case c-blank (${file}:1) is not a case: evidence.0.source`
  );
});

// Catches: a case file posted before cases had a kind refused, or its cases read as written.
test("a case with no source.kind loads as a session case", () => {
  const base = alphaDir("base-kind");
  const source = f1Case().source;
  const unmarked = f1Case("c-unmarked");
  const session = { ...f1Case("c-session"), source: { ...source, kind: "session" } };
  const written = { ...f1Case("c-written"), source: { ...source, kind: "written" } };
  const cases = loadCases(caseFile("kinds", [unmarked, session, written]), base);
  // The case with no kind loads as it is, and reads as a session case.
  expect(cases[0]).toEqual(unmarked);
  expect(cases.map((c) => [c.id, caseKind(c)])).toEqual([
    ["c-unmarked", "session"],
    ["c-session", "session"],
    ["c-written", "written"],
  ]);
  const other = { ...f1Case("c-other"), source: { ...source, kind: "replayed" } };
  const file = caseFile("kind-other", [other]);
  expect(() => loadCases(file, base)).toThrow(
    `case c-other (${file}:1) is not a case: source.kind`
  );
});

test("a subagent's case, with its recorded system prompt and tools, loads; a context of another shape is refused", () => {
  const base = alphaDir("base-subagent");
  const c1 = f1Case("c-subagent");
  const subagent = {
    ...c1,
    context: "subagent:task",
    profile: {
      ...c1.profile,
      contextFiles: [],
      systemPrompt: ["A made-up prompt.\n<skills>\n- alpha: Use for alpha.\n</skills>\n"],
      tools: ["read", "bash"],
    },
  };
  expect(loadCases(caseFile("subagent", [subagent]), base)).toEqual([subagent]);
  const odd = { ...f1Case("c-odd"), context: "subagent:" };
  const noTools = { ...subagent, id: "c-no-tools", profile: { ...subagent.profile, tools: [] } };
  const file = caseFile("subagent-odd", [odd, noTools]);
  expect(() => loadCases(file, base)).toThrow(`case c-odd (${file}:1) is not a case: context`);
  expect(() => loadCases(file, base)).toThrow(
    `case c-no-tools (${file}:2) is not a case: profile.tools`
  );
});

test("a profile names the session's working directory inside its repository's checkout, and marks a dev box's user-level context file; a directory outside the checkout and another marker are refused", () => {
  const base = alphaDir("base-workdir");
  const c1 = f1Case("c-workdir");
  const placed = {
    ...c1,
    profile: {
      ...c1.profile,
      contextFiles: [
        {
          repo: "acme/skills",
          commit: "0123456789abcdef",
          path: ".claude/CLAUDE.md",
          user: true as const,
        },
        { repo: "acme/project", commit: "fedcba9876543210", path: "agents/AGENTS.md" },
      ],
      workingDirectory: "agents/agentvm",
    },
  };
  expect(loadCases(caseFile("workdir", [placed]), base)).toEqual([placed]);
  const rows = ["../agents", "/agents", "agents/", "agents/./vm", ""].map((dir, n) => ({
    ...placed,
    id: `c-dir-${n}`,
    profile: { ...placed.profile, workingDirectory: dir },
  }));
  const notUser = {
    ...placed,
    id: "c-not-user",
    profile: {
      ...placed.profile,
      contextFiles: [{ ...placed.profile.contextFiles[0], user: false }],
    },
  };
  const file = caseFile("workdir-bad", [...rows, notUser]);
  for (const n of [0, 1, 2, 3, 4]) {
    expect(() => loadCases(file, base)).toThrow(
      `case c-dir-${n} (${file}:${n + 1}) is not a case: profile.workingDirectory`
    );
  }
  expect(() => loadCases(file, base)).toThrow(
    `case c-not-user (${file}:6) is not a case: profile.contextFiles.0.user`
  );
});

test("a profile names its session repository and the commit it was read at; one with no commit, another key, or a repository that is no owner/name is refused", () => {
  const base = alphaDir("base-session-repo");
  const c1 = f1Case("c-repo");
  const named = {
    ...c1,
    profile: {
      ...c1.profile,
      sessionRepository: { repo: "acme/project", commit: "fedcba9876543210" },
      workingDirectory: "agents/agentvm",
    },
  };
  expect(loadCases(caseFile("session-repo", [named]), base)).toEqual([named]);
  const rows = [
    { repo: "acme/project" },
    { repo: "acme/project", commit: "fedcba9876543210", path: "agents" },
    { repo: "project", commit: "fedcba9876543210" },
  ].map((sessionRepository, n) => ({
    ...named,
    id: `c-repo-${n}`,
    profile: { ...named.profile, sessionRepository },
  }));
  const file = caseFile("session-repo-bad", rows);
  for (const n of [0, 1, 2]) {
    expect(() => loadCases(file, base)).toThrow(
      `case c-repo-${n} (${file}:${n + 1}) is not a case: profile.sessionRepository`
    );
  }
});

test("refuses a line that is not a whole line of the base text", () => {
  const base = alphaDir("base-line");
  const part = {
    ...f1Case("c-part"),
    line: { ...f1Case().line, text: "Write each step's result" },
  };
  const elsewhere = { ...f1Case("c-elsewhere"), line: { ...f1Case().line, lineNumber: 8 } };
  // The whole line with its spaces trimmed is the line.
  const padded = {
    ...f1Case("c-padded"),
    line: { ...f1Case().line, text: "  2. Write each step's result on one line. " },
  };
  const file = caseFile("line", [part, elsewhere]);
  expect(() => loadCases(file, base)).toThrow(
    `case c-part (${file}:1): its line is not a whole line of ${path.join(base, "SKILL.md")}: "Write each step's result"`
  );
  expect(() => loadCases(file, base)).toThrow(
    `case c-elsewhere (${file}:2): line 8 of ${path.join(base, "SKILL.md")} is not its line`
  );
  expect(loadCases(caseFile("padded", [padded]), base)).toHaveLength(1);
});

test("refuses a case with no realStep, naming it", () => {
  const base = alphaDir("base-real-step");
  const { realStep: _, ...noStep } = f1Case("c-no-step");
  const file = caseFile("no-step", [noStep]);
  expect(() => loadCases(file, base)).toThrow(
    `case c-no-step (${file}:1) has no realStep (the real agent's next assistant message)`
  );
  const userStep = { ...f1Case("c-user-step"), realStep: f1Messages()[0] };
  const userFile = caseFile("user-step", [userStep]);
  expect(() => loadCases(userFile, base)).toThrow(
    `case c-user-step (${userFile}:1) is not a case: realStep: is not an assistant message`
  );
});

test("refuses a case with no violatingStep, and one that is no assistant step or holds nothing", () => {
  const base = alphaDir("base-violating-step");
  const { violatingStep: _, ...noViolating } = f1Case("c-no-violating");
  const file = caseFile("no-violating", [noViolating]);
  expect(() => loadCases(file, base)).toThrow(
    `case c-no-violating (${file}:1) has no violatingStep (a step that breaks the line, written before any run): build its case again`
  );
  const userStep = { ...f1Case("c-user-violating"), violatingStep: f1Messages()[0] };
  const empty = {
    ...f1Case("c-empty-violating"),
    violatingStep: { role: "assistant", content: [{ type: "text", text: "  " }] },
  };
  const bad = caseFile("bad-violating", [userStep, empty]);
  expect(() => loadCases(bad, base)).toThrow(
    `case c-user-violating (${bad}:1) is not a case: violatingStep: is not an assistant message`
  );
  expect(() => loadCases(bad, base)).toThrow(
    `case c-empty-violating (${bad}:2) is not a case: violatingStep: holds no text and no tool call`
  );
});

test("refuses a skillReads index that is not a read result of the skill's files", () => {
  const base = alphaDir("base-reads");
  const user = { ...f1Case("c-user"), skillReads: [0, 2, 4] };
  const outside = { ...f1Case("c-outside"), skillReads: [2, 4, 40] };
  const gamma = { ...f1Case("c-gamma"), skillReads: [2, 4, 6] };
  const missing = { ...f1Case("c-missing"), skillReads: [2] };
  const file = caseFile("reads", [user, outside, gamma, missing]);
  expect(() => loadCases(file, base)).toThrow(
    [
      "refused cases:",
      `case c-user (${file}:1): skillReads index 0 is not a read result`,
      `case c-outside (${file}:2): skillReads index 40 is not a read result`,
      `case c-gamma (${file}:3): skillReads index 6 answers a read of skill://gamma, not a read of a skill://alpha file`,
      `case c-missing (${file}:4): the read result at index 4 (skill://alpha/references/x.md) is not in skillReads`,
    ].join("\n")
  );
});

test("refuses a file of more than one skill, and an id given twice", () => {
  const base = alphaDir("base-skills");
  const beta = { ...f1Case("c-beta"), skill: "beta", skillReads: [] };
  const file = caseFile("skills", [f1Case("c1"), f1Case("c1")]);
  expect(() => loadCases(file, base)).toThrow(`case c1 (${file}:2): its id is given twice`);
  // beta has no SKILL.md line 9 of alpha's text; only the skill names matter here.
  const mixed = caseFile("mixed", [f1Case("c1"), { ...beta, baseFile: "SKILL.md" }]);
  expect(() => loadCases(mixed, base)).toThrow(
    `${mixed} holds cases of more than one skill: alpha and beta (one skill per case file)`
  );
});

test("skillReadFile reads skill:// URLs of the skill only", () => {
  expect(skillReadFile("alpha", "skill://alpha")).toBe("SKILL.md");
  expect(skillReadFile("alpha", "skill://alpha/")).toBe("SKILL.md");
  expect(skillReadFile("alpha", "skill://alpha/references/x.md")).toBe("references/x.md");
  expect(skillReadFile("alpha", "skill://alpha/references/x%20y.md")).toBe("references/x y.md");
  expect(skillReadFile("alpha", "skill://alphabet")).toBeUndefined();
  expect(skillReadFile("alpha", "skill://beta")).toBeUndefined();
  expect(skillReadFile("alpha", "skill://alpha/../beta/SKILL.md")).toBeUndefined();
  expect(skillReadFile("alpha", "skill://alpha/SKILL.md:1-5")).toBeUndefined();
  expect(skillReadFile("alpha", "/profiles/p/agent/skills/alpha/SKILL.md")).toBeUndefined();
});

/** The made-up copy of alpha a dev box's session read by path, and another copy of it. */
const COPY = "/home/agent/src/acme-skills/skills/alpha";
const OTHER_COPY = "/tmp/eval/acme-skills/skills/alpha";

test("pathReadFile reads whole reads by path of the case's own copy only", () => {
  const c = { pathCopy: { dir: COPY } };
  expect(pathReadFile(c, `${COPY}/SKILL.md`)).toBe("SKILL.md");
  expect(pathReadFile(c, `${COPY}/references/x.md`)).toBe("references/x.md");
  // A read selector: a `:` after the last `/`.
  expect(pathReadFile(c, `${COPY}/SKILL.md:1-180`)).toBeUndefined();
  expect(pathReadFile(c, `${COPY}/SKILL.md:raw`)).toBeUndefined();
  expect(pathReadFile(c, `${COPY}/references/x.md:raw:1-5`)).toBeUndefined();
  // A `:` before the last `/` is the path's own.
  expect(pathReadFile({ pathCopy: { dir: "/srv/a:b/alpha" } }, "/srv/a:b/alpha/SKILL.md")).toBe(
    "SKILL.md"
  );
  // Another copy, a raw URL of the repository at another commit, the directory itself, a
  // directory under it, a name that only starts like it, and a path that leaves it.
  expect(pathReadFile(c, `${OTHER_COPY}/SKILL.md`)).toBeUndefined();
  expect(
    pathReadFile(
      c,
      "https://raw.githubusercontent.com/acme/skills/0123456789abcdef/skills/alpha/SKILL.md"
    )
  ).toBeUndefined();
  expect(pathReadFile(c, COPY)).toBeUndefined();
  expect(pathReadFile(c, `${COPY}/`)).toBeUndefined();
  expect(pathReadFile(c, `${COPY}/references/`)).toBeUndefined();
  expect(pathReadFile(c, `${COPY}bet/SKILL.md`)).toBeUndefined();
  expect(pathReadFile(c, `${COPY}/../beta/SKILL.md`)).toBeUndefined();
  expect(pathReadFile(c, "skill://alpha")).toBeUndefined();
  // A case that names no copy has no read by path of it.
  expect(pathReadFile({}, `${COPY}/SKILL.md`)).toBeUndefined();
});

// Catches: a read of the copy by a relative or `~` path, as a session in a checkout of the cut's
// repository makes one, that the case leaves out of skillReads, so every arm sends the base text
// there; or one resolved otherwise than Oh My Pi's read resolves it.
test("pathReadFile resolves a relative read against the copy's cwd and a ~ read against its home, as Oh My Pi's read does, and reads none whose directory the copy does not name", () => {
  const cwd = "/home/agent/src/acme-skills";
  const home = "/home/agent";
  const c = { pathCopy: { dir: COPY, cwd, home } };
  expect(pathReadFile(c, "skills/alpha/SKILL.md")).toBe("SKILL.md");
  expect(pathReadFile(c, "./skills/alpha/references/x.md")).toBe("references/x.md");
  expect(pathReadFile(c, "docs/../skills/alpha/SKILL.md")).toBe("SKILL.md");
  expect(pathReadFile(c, "~/src/acme-skills/skills/alpha/SKILL.md")).toBe("SKILL.md");
  // Oh My Pi takes a stray `:` or `@` off the start of a path before it resolves it.
  expect(pathReadFile(c, ":~/src/acme-skills/skills/alpha/SKILL.md")).toBe("SKILL.md");
  expect(pathReadFile(c, "@~/src/acme-skills/skills/alpha/SKILL.md")).toBe("SKILL.md");
  // A selector, a directory, another copy, and a path that leaves the copy are still none.
  expect(pathReadFile(c, "skills/alpha/SKILL.md:1-5")).toBeUndefined();
  expect(pathReadFile(c, "skills/alpha/references/")).toBeUndefined();
  expect(pathReadFile(c, "../eval/skills/alpha/SKILL.md")).toBeUndefined();
  expect(pathReadFile(c, "skills/alpha/../beta/SKILL.md")).toBeUndefined();
  // With no cwd, a relative read is none; with no home, a `~` read is none.
  expect(pathReadFile({ pathCopy: { dir: COPY, home } }, "skills/alpha/SKILL.md")).toBeUndefined();
  expect(
    pathReadFile({ pathCopy: { dir: COPY, cwd } }, "~/src/acme-skills/skills/alpha/SKILL.md")
  ).toBeUndefined();
  // expandTilde's forms: `~` alone, `~/<rest>` kept as written, `~<rest>` joined (normalized).
  expect(resolvedReadPath("~", { home })).toBe(home);
  expect(resolvedReadPath("~/a//b", { home })).toBe("/home/agent/a//b");
  expect(resolvedReadPath("~a/../b", { home })).toBe("/home/agent/b");
  // An absolute path is kept as written; a URL is no path.
  expect(resolvedReadPath("/srv/a/../b", { cwd })).toBe("/srv/a/../b");
  expect(resolvedReadPath("https://example.com/skills/alpha/SKILL.md", { cwd })).toBeUndefined();
});

/** F1's case whose session also read alpha's SKILL.md by path in COPY (8), its first five lines
 *  there (10), and OTHER_COPY's SKILL.md (12): the results as Oh My Pi shows them, cut short. */
function pathCase(id: string): Case {
  const c = f1Case(id);
  const read = (callId: string, target: string, text: string): SessionMessage[] => [
    {
      role: "assistant",
      content: [{ type: "toolCall", id: callId, name: "read", arguments: { path: target } }],
    },
    {
      role: "toolResult",
      toolCallId: callId,
      toolName: "read",
      content: [{ type: "text", text }],
      isError: false,
    },
  ];
  return {
    ...c,
    messages: [
      ...c.messages,
      ...read("path_skill", `${COPY}/SKILL.md`, "[~/src/acme-skills/skills/alpha/SKILL.md#BF93]"),
      ...read(
        "path_range",
        `${COPY}/SKILL.md:1-5`,
        "[~/src/acme-skills/skills/alpha/SKILL.md#BF93]"
      ),
      ...read("path_other", `${OTHER_COPY}/SKILL.md`, `[${OTHER_COPY}/SKILL.md#BF93]`),
    ],
    skillReads: [2, 4, 8],
    pathCopy: { dir: COPY },
  };
}

test("a case whose session read its own copy by path loads with that read in skillReads; a read with a selector or of another copy needs none", () => {
  const base = alphaDir("base-path");
  const cases = loadCases(caseFile("path", [pathCase("c-path")]), base);
  expect(cases).toEqual([pathCase("c-path")]);
  expect(cases[0]?.pathCopy).toEqual({ dir: COPY });
});

test("a case whose copy names its cwd and home loads with a relative and a ~ read of it in skillReads, refuses either left out, and refuses a cwd or home that is no absolute directory", () => {
  const base = alphaDir("base-path-resolved");
  // The read of SKILL.md relative to the working directory, and a whole read of references/x.md
  // under `~` in the place of the read with a selector.
  const targets: Record<string, string> = {
    path_skill: "skills/alpha/SKILL.md",
    path_range: "~/src/acme-skills/skills/alpha/references/x.md",
  };
  const relative = pathCase("c-resolved");
  relative.messages = relative.messages.map((message) => {
    const call = message.content?.[0];
    const target = targets[call?.id ?? ""];
    if (message.role !== "assistant" || call === undefined || target === undefined) return message;
    return { ...message, content: [{ ...call, arguments: { path: target } }] };
  });
  const resolved = {
    ...relative,
    skillReads: [2, 4, 8, 10],
    pathCopy: { dir: COPY, cwd: "/home/agent/src/acme-skills", home: "/home/agent" },
  };
  expect(loadCases(caseFile("path-resolved", [resolved]), base)).toEqual([resolved]);
  const left = { ...resolved, id: "c-resolved-left", skillReads: [2, 4, 8] };
  const leftFile = caseFile("path-resolved-left", [left]);
  expect(() => loadCases(leftFile, base)).toThrow(
    `case c-resolved-left (${leftFile}:1): the read result at index 10 (~/src/acme-skills/skills/alpha/references/x.md) is not in skillReads`
  );
  for (const where of [{ cwd: "src/acme-skills" }, { home: "/home/agent/" }]) {
    const bad = caseFile("path-where", [{ ...resolved, pathCopy: { dir: COPY, ...where } }]);
    expect(() => loadCases(bad, base)).toThrow(
      `case c-resolved (${bad}:1) is not a case: pathCopy.${Object.keys(where)[0]}: is not an absolute directory with no / at its end`
    );
  }
});

test("refuses a whole read by path of the copy that skillReads leaves out, a path read in skillReads it does not answer, and a copy that is no absolute directory", () => {
  const base = alphaDir("base-path-refused");
  const left = { ...pathCase("c-left"), skillReads: [2, 4] };
  const range = { ...pathCase("c-range"), skillReads: [2, 4, 8, 10] };
  const { pathCopy: _, ...noCopy } = pathCase("c-no-copy");
  const file = caseFile("path-refused", [left, range, noCopy]);
  expect(() => loadCases(file, base)).toThrow(
    [
      "refused cases:",
      `case c-left (${file}:1): the read result at index 8 (${COPY}/SKILL.md) is not in skillReads`,
      `case c-range (${file}:2): skillReads index 10 answers a read of ${COPY}/SKILL.md:1-5, not a read of a skill://alpha file or a whole read of a file of ${COPY}`,
      `case c-no-copy (${file}:3): skillReads index 8 answers a read of ${COPY}/SKILL.md, not a read of a skill://alpha file`,
    ].join("\n")
  );
  for (const dir of ["skills/alpha", `${COPY}/`, "/", "/home/agent/../alpha"]) {
    const bad = caseFile("path-dir", [{ ...pathCase("c-dir"), pathCopy: { dir } }]);
    expect(() => loadCases(bad, base)).toThrow(
      `case c-dir (${bad}:1) is not a case: pathCopy.dir: is not an absolute directory with no / at its end`
    );
  }
});
