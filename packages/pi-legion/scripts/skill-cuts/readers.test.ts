import { expect, test } from "bun:test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { messageFor } from "@legion/envoy-client/errors";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { z } from "zod";
import { parseCraftList } from "./craftlist";
import { createGateway, type MessagesBody } from "./gateway";
import {
  ATTACK_PLANTED_LINE,
  AttackInput,
  CraftInput,
  craftInput,
  loadPrompt,
  MapInput,
  NAME_ONLY_REFUSAL,
  nameOnlyReason,
  PROMPTS_DIR,
  parsedAnswer,
  parsePrompt,
  plantControl,
  READER_NAMES,
  READER_TRIES,
  READERS,
  type Reader,
  ReaderFailed,
  type ReaderName,
  readerBody,
  runControl,
  runReader,
  SameJobInput,
  SentenceInput,
  skillFiles,
  TASKS_PER_LABEL,
} from "./readers";
import { styleRules } from "./style";

/** The one user message of a reader's request body, its blocks' text. */
const UserMessage = z.tuple([
  z.object({
    role: z.literal("user"),
    content: z.array(z.object({ type: z.literal("text"), text: z.string() })),
  }),
]);

// The readers' schemas and their one call each, with a gateway whose fake fetch answers each body
// with a synthetic answer in the stream shape the gateway reads (testdata/F5): no test here
// reaches a network. Every skill, repository and message is made up.

const scratch = temporaryDirectory("skill-cuts-readers", { real: true });
/** The text of the fixture `name` (testdata/CAPTURE.md). */
const fixture = (name: string) =>
  readFileSync(path.join(import.meta.dir, "testdata", name), "utf8");

const SKILL_MD = [
  "---",
  "name: alpha",
  "description: Use when a task asks for the alpha checklist of the made-up fixture project.",
  "---",
  "",
  "# Alpha",
  "",
  "1. Read `references/x.md` before the first step.",
  "2. Write each step's result on one line.",
  "3. Run the tests where needed.",
  "",
].join("\n");
const CUT_MD = SKILL_MD.replace("2. Write each step's result on one line.\n", "");

/** A streamed reply whose one text block is `text`, in the shape the gateway streams (F5). */
function sse(text: string): Response {
  const events = [
    {
      type: "message_start",
      message: {
        model: "claude-opus-5-5",
        id: "msg_test",
        type: "message",
        role: "assistant",
        content: [],
        stop_reason: null,
        usage: { input_tokens: 100, output_tokens: 1 },
      },
    },
    { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } },
    { type: "content_block_delta", index: 0, delta: { type: "text_delta", text } },
    { type: "content_block_stop", index: 0 },
    { type: "message_delta", delta: { stop_reason: "end_turn" }, usage: { output_tokens: 20 } },
    { type: "message_stop" },
  ];
  return new Response(
    events.map((event) => `event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`).join(""),
    {
      status: 200,
      headers: { "content-type": "text/event-stream" },
    }
  );
}

/** A gateway whose fake fetch answers the n-th body (from 1) with the n-th of `answers` as JSON,
 *  the last again once they run out, and the bodies sent. */
function world(...answers: unknown[]) {
  const bodies: MessagesBody[] = [];
  const keyFile = path.join(scratch, `key-${Math.random()}`);
  writeFileSync(keyFile, fixtureSecret("gw"), { mode: 0o600 });
  const gateway = createGateway({
    baseUrl: "http://gateway.test",
    keyFile,
    concurrency: 1,
    timeoutMs: 5_000,
    fetch: async (_url, init) => {
      bodies.push(JSON.parse(String(init.body)) as MessagesBody);
      const answer = answers[Math.min(bodies.length, answers.length) - 1];
      return sse(typeof answer === "string" ? answer : JSON.stringify(answer));
    },
    sleep: async () => {},
  });
  return { gateway, bodies };
}

/** Why the schema of reader `name` refuses `answer` to `input`, as runReader would say it, or
 *  undefined when it parses. */
function refusal(name: ReaderName, input: unknown, answer: unknown): string | undefined {
  const reader: Reader<unknown, unknown> = READERS[name];
  try {
    parsedAnswer(reader.answer(reader.input.parse(input)), answer);
    return undefined;
  } catch (error) {
    return messageFor(error);
  }
}

/** One reader's input, a valid answer, and bad answers each with a piece of why it is refused. */
interface Row {
  readonly input: unknown;
  readonly valid: readonly unknown[];
  readonly bad: readonly { readonly answer: unknown; readonly why: string }[];
}

const CRITERION = {
  pass: "The step writes each alpha step's result on a line of its own.",
  fail: "The step puts two results on one line.",
};
const caseInput = {
  skill: "alpha",
  // The lines of SKILL.md the session's read held, each with its number in the file.
  files: [
    {
      path: "SKILL.md",
      lines: [
        { number: 6, text: "# Alpha" },
        { number: 8, text: "1. Read `references/x.md` before the first step." },
        { number: 9, text: "2. Write each step's result on one line." },
      ],
    },
  ],
  messages: [
    {
      index: 2,
      role: "user",
      text: '[user]\nWrite the alpha checklist, and put every result on "one line".',
    },
    {
      index: 3,
      role: "assistant",
      text: '[assistant]\n[tool call: read] {"path":"skill://alpha"}',
    },
    {
      index: 4,
      role: "toolResult",
      text: "[tool result: read]\n[the text of a file of the skill, left out]",
    },
    {
      index: 5,
      role: "assistant",
      text: '[assistant]\n[tool call: todo] {"plan":"Each result goes on\\n\\"one line\\"."}',
    },
    { index: 6, role: "assistant", text: "[assistant]\n1. ok\n2. ok" },
  ],
};
const caseStep = {
  step: { messageIndex: 6, what: "writes the results" },
  line: { file: "SKILL.md", text: "2. Write each step's result on one line." },
  criterion: CRITERION,
  violatingStep: { text: "1. ok, 2. ok", toolCalls: [] },
  restatements: [],
};
/** The same case, with each message before the step that states the line, its words quoted:
 *  white space, case and a JSON string's escapes aside. */
const restated = {
  ...caseStep,
  violatingStep: {
    text: "",
    toolCalls: [{ name: "write", arguments: { path: "checklist.md", content: "1. ok, 2. ok" } }],
  },
  restatements: [
    { messageIndex: 2, quote: 'Put every result on "one line".' },
    { messageIndex: 5, quote: 'each result goes on "one   line".' },
  ],
};

/** A window of the restatement check: message 5 comes in two parts. */
const restateInput = {
  line: "2. Write each step's result on one line.",
  messages: [
    {
      index: 2,
      role: "user",
      text: '[user]\nWrite the alpha checklist, and put every result on "one line".',
    },
    { index: 5, role: "toolResult", text: "[part 1 of 2 of this message]\nThe notes say:" },
    {
      index: 5,
      role: "toolResult",
      text: "[part 2 of 2 of this message]\nKeep each result to a single line.",
    },
  ],
};
const auditInput = {
  line: "2. Write each step's result on one line.",
  before: "[user]\nWrite the alpha checklist.",
  step: "[assistant]\n1. ok\n2. ok",
};

const mapInput = {
  skill: "alpha",
  base: [{ path: "SKILL.md", text: SKILL_MD }],
  cut: [{ path: "SKILL.md", text: CUT_MD }],
  audit: null,
};
const kept = {
  file: "SKILL.md",
  line: 8,
  rule: "read x first",
  status: "kept",
  cut: { file: "SKILL.md", line: 8 },
  reason: "same words",
  audit: null,
};
const lost = {
  file: "SKILL.md",
  line: 9,
  rule: "one line per result",
  status: "lost",
  cut: null,
  reason: "left out",
  audit: null,
};

const skillsInput = {
  skills: [
    { id: "s1", repo: "acme/skills", name: "alpha", description: "Use for the alpha checklist." },
    {
      id: "s2",
      repo: "acme/tools",
      name: "alpha-list",
      description: "Use to write the alpha checklist.",
    },
    { id: "s3", repo: "acme/tools", name: "beta", description: "Use to greet." },
  ],
};

const namesInput = {
  skills: [
    {
      id: "s1",
      repo: "acme/skills",
      name: "alpha",
      description: "Use for the alpha checklist.",
      descriptionFault: null,
    },
    {
      id: "s2",
      repo: "acme/tools",
      name: "beta",
      description: "Use to write the greeting card of the fixture project.",
      descriptionFault: "a need task loaded it on 0 of 3 runs",
    },
  ],
  namesTaken: ["alpha", "beta", "gamma"],
};
const notNominated = { id: "s1", nominated: false };
const nominated = {
  id: "s2",
  nominated: true,
  risk: "missed",
  task: "writing the greeting card of the fixture project",
  reason:
    "Beta names a letter, not greetings: an agent asked for a greeting card looks for a greeting skill.",
  proposed: ["greeting-card"],
};

const tasksInput = {
  skill: { name: "alpha", description: "Use when a task asks for the alpha checklist." },
  messages: [
    { id: "m1", text: "Write the alpha checklist for the fixture project." },
    { id: "m2", text: "Write the beta greeting for the fixture project." },
  ],
};
/** A need task's statement that the skill governs its first step. */
const governs = { firstStep: { governed: true, reason: "reading the checklist comes first" } };
const written = (label: string, n: number) => ({
  label,
  from: "written",
  text: `task ${label} ${n}`,
  why: "the description",
  ...(label === "need" ? governs : {}),
});
const fourEach = ["need", "near-miss"].flatMap((label) =>
  [1, 2, 3, 4].map((n) => written(label, n))
);
const tasksAnswer = {
  terms: ["alpha checklist"],
  tasks: [
    {
      label: "need",
      from: "session",
      messageId: "m1",
      why: "it asks for the checklist",
      ...governs,
    },
    {
      label: "near-miss",
      from: "session",
      messageId: "m2",
      why: "a fixture task without the checklist",
    },
    ...fourEach,
  ],
};
/** The first need task, saying nothing of its first step. */
const unstated = Object.fromEntries(
  Object.entries(tasksAnswer.tasks[0] ?? {}).filter(([key]) => key !== "firstStep")
);

/** The craft reader's input: alpha's two files (SKILL.md has 10 lines, references/x.md 3), two
 *  style rules, one reader property, and one nearest skill of theirs whose SKILL.md has 8 lines. */
const craftReaderInput = {
  skill: "alpha",
  files: [
    { path: "SKILL.md", text: SKILL_MD },
    { path: "references/x.md", text: "# X\n\nRead me first.\n" },
  ],
  style: [
    {
      id: "S1",
      line: 9,
      part: "order",
      text: "| `**Done when:**` | The check that ends the skill |",
    },
    {
      id: "S2",
      line: 19,
      part: "rules",
      text: "- **One rule per bullet or numbered line.** Start it with the rule in bold.",
    },
  ],
  properties: [
    {
      id: "C1",
      property: "The description says when to use the skill, and never sums up its steps.",
      readerLooksFor: "A Use when clause in the description, and no list of steps.",
    },
  ],
  nearest: [
    {
      name: "beta-skill-one",
      files: [
        {
          path: "SKILL.md",
          text: "---\nname: beta-skill-one\ndescription: Use for the beta checklist.\n---\n\n1. Read the notes first.\n2. Write each result on its own line.\n3. Run the named tests before you report.\n",
        },
      ],
    },
  ],
};
/** A gap of the craft answer. */
const craftGap = (gap: Record<string, unknown>) => ({
  file: "SKILL.md",
  line: 9,
  rule: "S2",
  kind: "present",
  why: "The item holds two rules.",
  ...gap,
});
/** The craft answer that judges S1 met, S2 gap and C1 `c1`, with `gaps`. */
const craftAnswer = (gaps: readonly unknown[], c1 = "met") => ({
  judged: [
    { rule: "S1", verdict: "met" },
    { rule: "S2", verdict: "gap" },
    { rule: "C1", verdict: c1 },
  ],
  gaps,
});
const allMet = {
  judged: [
    { rule: "S1", verdict: "met" },
    { rule: "S2", verdict: "not-applicable" },
    { rule: "C1", verdict: "met" },
  ],
  gaps: [],
};

const nearestInput = {
  skill: {
    name: "alpha",
    description: "Use when a task asks for the alpha checklist of the made-up fixture project.",
    body: "# Alpha\n\n1. Read `references/x.md` before the first step.\n",
  },
  candidates: [
    { name: "alpha-list", source: "acme/tools", description: "Use to write the alpha checklist." },
    { name: "alpha-notes", source: "acme/tools", description: "Use to take notes on alpha." },
    { name: "alpha-list", source: "acme/skills", description: "Use to print the alpha list." },
  ],
};
/** A nearest entry naming the candidate `name` of `source`. */
const nearestEntry = (name: string, source: string, verdict: string) => ({
  name,
  source,
  verdict,
  missing: verdict === "equivalent" ? "none" : "the one-line results",
  reason: `${name} writes the alpha checklist.`,
});
const noNearest = { verdict: "none", nearest: [], reason: "No candidate writes the checklist." };

const claimInput = {
  claim: "the skill has no check that runs the tests before the summary",
  gap: craftGap({ line: 10, why: "Nothing says which tests to run before the summary." }),
  files: [{ path: "SKILL.md", text: SKILL_MD }],
};

// The proposal readers' inputs: a made-up diff of a closed pull request (a changed file, a renamed
// one and a deleted one), and the target texts of one proposal.
const LEGION = "acme/skills at 1a2b3c4d";
const DOTFILES = "acme/dotfiles at 5e6f7a8b";
const PROPOSAL_DIFF = [
  "diff --git a/.claude/CLAUDE.md b/.claude/CLAUDE.md",
  "index 1111111..2222222 100644",
  "--- a/.claude/CLAUDE.md",
  "+++ b/.claude/CLAUDE.md",
  "@@ -1,3 +1,3 @@",
  " # Notes",
  "-- Do not turn a correction into a rule all agents.",
  "+- Do not turn a correction into a rule for all agents.",
  " - Check findings first.",
  "@@ -9,2 +9,3 @@ # Notes",
  " - Ask for a reason.",
  "+- Say what the reason is.",
  " - Report the evidence.",
  "diff --git a/skills/alpha/notes.md b/skills/alpha/references/notes.md",
  "similarity index 90%",
  "rename from skills/alpha/notes.md",
  "rename to skills/alpha/references/notes.md",
  "index 3333333..4444444 100644",
  "--- a/skills/alpha/notes.md",
  "+++ b/skills/alpha/references/notes.md",
  "@@ -1,2 +1,2 @@",
  " # Notes",
  "-Old line.",
  "+New line.",
  "diff --git a/omp/old.md b/omp/old.md",
  "deleted file mode 100644",
  "index 5555555..0000000",
  "--- a/omp/old.md",
  "+++ /dev/null",
  "@@ -1,2 +0,0 @@",
  "-# Old",
  "-Gone.",
].join("\n");
const proposalTargetInput = {
  diff: {
    pr: "41",
    text: PROPOSAL_DIFF,
    files: [
      {
        before: ".claude/CLAUDE.md",
        after: ".claude/CLAUDE.md",
        hunks: ["@@ -1,3 +1,3 @@", "@@ -9,2 +9,3 @@ # Notes"],
      },
      {
        before: "skills/alpha/notes.md",
        after: "skills/alpha/references/notes.md",
        hunks: ["@@ -1,2 +1,2 @@"],
      },
      { before: "omp/old.md", after: null, hunks: ["@@ -1,2 +0,0 @@"] },
    ],
  },
  item: {
    id: "41-10.5",
    state: "open",
    text: 'a missing word in `CLAUDE.md` ("into a rule all agents" becomes "into a rule for all agents").',
  },
  repository: DOTFILES,
  skills: [
    { repo: LEGION, name: "alpha" },
    { repo: DOTFILES, name: "alpha" },
    { repo: DOTFILES, name: "beta-notes" },
  ],
};
const dotfilesTarget = (path: string, why = "The hunk changes it.") => ({
  kind: "dotfiles",
  path,
  why,
});
const skillTarget = (repo: string, name: string) => ({
  kind: "skill",
  skill: { repo, name },
  why: `The change is for ${name}.`,
});
const noTarget = {
  kind: "none",
  why: "The proposal's file was deleted, and nothing took its line.",
};

const proposalInput = {
  item: {
    id: "41-1.1",
    state: "open",
    text: "before it deletes a rule, an agent finds the rule's reason.",
  },
  hunks: [
    {
      file: "skills/alpha/SKILL.md",
      hunk: 0,
      header: "@@ -8,3 +8,4 @@ # Alpha",
      lines: [
        " 1. Read `references/x.md` before the first step.",
        "+2. Before you delete a rule, find its reason.",
        " 2. Write each step's result on one line.",
        " 3. Run the tests where needed.",
      ],
    },
  ],
  targets: [
    {
      kind: "skill",
      repo: LEGION,
      name: "alpha",
      files: craftReaderInput.files,
    },
    {
      kind: "dotfiles",
      repo: DOTFILES,
      path: ".claude/CLAUDE.md",
      text: "# Notes\n\n- Check findings first.\n",
    },
    { kind: "dotfiles", repo: DOTFILES, path: "omp/rules/new.md", text: null },
  ],
  findings: [
    {
      repo: LEGION,
      name: "alpha",
      gaps: [
        craftGap({}),
        craftGap({ line: 0, rule: "C1", kind: "missing", why: "No Use when clause." }),
      ],
    },
  ],
  style: craftReaderInput.style,
  properties: craftReaderInput.properties,
};
const fix = (fields: Record<string, unknown>) => ({
  why: "The change adds the reason.",
  ...fields,
});
const make = (fields: Record<string, unknown>) => ({
  rule: "S2",
  why: "The new item holds two rules.",
  ...fields,
});
const SUMMARY = "The change adds a step that finds a rule's reason before its deletion.";

/** The written-case reader's input: alpha's base text, one session case, the context session's
 *  first user message, and at most two tasks. */
const writtenCaseInput = {
  skill: "alpha",
  files: [{ path: "SKILL.md", text: SKILL_MD }],
  sessionCases: [
    { step: '[tool call: write] {"path":"checklist.md","content":"1. ok"}', criterion: CRITERION },
  ],
  firstMessage: "Make the alpha checklist of the fixture project for the release.",
  count: 2,
};
/** A written task whose line, the base text's line 9, decides its first step. */
const writtenTask = {
  task: "Draft the alpha checklist of the fixture project for the next build.",
  line: { file: "SKILL.md", number: 9, text: "2. Write each step's result on one line." },
  criterion: CRITERION,
  followingStep: {
    text: "",
    toolCalls: [{ name: "write", arguments: { path: "checklist.md", content: "1. ok\n2. ok" } }],
  },
  violatingStep: {
    text: "",
    toolCalls: [{ name: "write", arguments: { path: "checklist.md", content: "1. ok, 2. ok" } }],
  },
};
/** `writtenTask` with `fields` in its place. */
const task = (fields: Record<string, unknown>) => ({ ...writtenTask, ...fields });

const ROWS: Record<ReaderName, Row> = {
  restate: {
    input: restateInput,
    valid: [
      { restatements: [] },
      {
        restatements: [
          { messageIndex: 2, quote: 'put every result on "one line"' },
          { messageIndex: 5, quote: "Keep each result to a single line." },
        ],
      },
    ],
    bad: [
      {
        answer: { restatements: [{ messageIndex: 3, quote: "x" }] },
        why: "restatements.0.messageIndex: is no message index of the window",
      },
      {
        answer: { restatements: [{ messageIndex: 2, quote: "Keep each result" }] },
        why: "restatements.0.quote: is not words of message 2",
      },
      { answer: {}, why: "restatements" },
    ],
  },
  audit: {
    input: auditInput,
    valid: [
      { verdict: "follows", reason: "It writes each result on its own line." },
      { verdict: "breaks", reason: "It puts two results on one line." },
      { verdict: "not-governed", reason: "The step writes no result." },
    ],
    bad: [
      { answer: { verdict: "pass", reason: "x" }, why: "verdict" },
      { answer: { verdict: "follows" }, why: "reason" },
      { answer: { verdict: "follows", reason: "x", criterion: "y" }, why: "criterion" },
    ],
  },
  case: {
    input: caseInput,
    valid: [
      caseStep,
      restated,
      { step: null, reason: "No step after the read follows a line of the skill." },
    ],
    bad: [
      {
        answer: { ...caseStep, step: { messageIndex: 4, what: "x" } },
        why: "is a toolResult message, not an assistant message",
      },
      {
        answer: { ...caseStep, step: { messageIndex: 9, what: "x" } },
        why: "is no message index of the transcript",
      },
      {
        answer: { ...caseStep, line: { file: "README.md", text: "a line" } },
        why: "line.file: is no file of the skill",
      },
      {
        answer: { ...caseStep, line: { file: "SKILL.md", text: "two\nlines" } },
        why: "is not one non-blank line",
      },
      { answer: { ...caseStep, criterion: { pass: "x" } }, why: "criterion.fail" },
      { answer: { step: null }, why: "reason" },
      {
        answer: { ...caseStep, violatingStep: { text: " ", toolCalls: [] } },
        why: "violatingStep: holds no text and no tool call",
      },
      {
        answer: {
          ...caseStep,
          violatingStep: { text: "", toolCalls: [{ name: "", arguments: {} }] },
        },
        why: "violatingStep.toolCalls.0.name",
      },
      {
        answer: { ...caseStep, violatingStep: undefined },
        why: "violatingStep: Invalid input: expected object, received undefined",
      },
      {
        answer: { ...caseStep, restatements: undefined },
        why: "restatements: Invalid input: expected array, received undefined",
      },
      {
        answer: { ...caseStep, restatements: [{ messageIndex: 6, quote: "1. ok" }] },
        why: "restatements.0.messageIndex: is not before the step, message 6",
      },
      {
        answer: { ...caseStep, restatements: [{ messageIndex: 7, quote: "x" }] },
        why: "restatements.0.messageIndex: is no message index of the transcript",
      },
      {
        answer: {
          ...caseStep,
          restatements: [{ messageIndex: 2, quote: "Write each result on one line." }],
        },
        why: "restatements.0.quote: is not words of message 2",
      },
    ],
  },
  map: {
    input: mapInput,
    valid: [{ rules: [kept, lost] }],
    bad: [
      {
        answer: { rules: [{ ...lost, cut: { file: "SKILL.md", line: 9 } }] },
        why: "a lost rule has no place in the cut text",
      },
      {
        answer: { rules: [{ ...kept, cut: null }] },
        why: "a lost rule has no place in the cut text",
      },
      {
        answer: { rules: [{ ...kept, line: 12 }] },
        why: "is past the last line, 10, of SKILL.md in the base text",
      },
      {
        answer: { rules: [{ ...kept, cut: { file: "SKILL.md", line: 10 } }] },
        why: "is past the last line, 9, of SKILL.md in the cut text",
      },
      {
        answer: { rules: [{ ...kept, audit: "the audit's words" }] },
        why: "cites an audit, and none was given",
      },
      { answer: { rules: [{ ...kept, status: "moved" }] }, why: "status" },
      { answer: { rules: [] }, why: "rules" },
    ],
  },
  attack: {
    input: { skill: "alpha", files: [{ path: "SKILL.md", text: SKILL_MD }] },
    valid: [
      {
        findings: [
          {
            kind: "vague",
            file: "SKILL.md",
            line: 10,
            quote: "where needed",
            why: "which tests, and when?",
          },
        ],
      },
      { findings: [] },
    ],
    bad: [
      {
        answer: {
          findings: [{ kind: "unclear", file: "SKILL.md", line: 10, quote: "q", why: "w" }],
        },
        why: "kind",
      },
      {
        answer: { findings: [{ kind: "vague", file: "SKILL.md", line: 11, quote: "q", why: "w" }] },
        why: "is past the last line, 10",
      },
      {
        answer: { findings: [{ kind: "vague", file: "x.md", line: 1, quote: "q", why: "w" }] },
        why: "is no file of the skill",
      },
    ],
  },
  "same-job": {
    input: skillsInput,
    valid: [
      {
        pairs: [{ a: "s1", b: "s2", task: "the alpha checklist", why: "both say alpha checklist" }],
      },
      { pairs: [] },
    ],
    bad: [
      {
        answer: { pairs: [{ a: "s1", b: "s9", task: "t", why: "w" }] },
        why: "pairs.0.b: is no id of the skills",
      },
      {
        answer: { pairs: [{ a: "s1", b: "s1", task: "t", why: "w" }] },
        why: "pairs a skill with itself",
      },
    ],
  },
  sentence: {
    input: {
      skills: [
        {
          id: "s1",
          repo: "acme/skills",
          path: "skills/alpha/SKILL.md",
          text: "# Alpha\n\nWhere this text and the program differ, trust the program.\n",
        },
        { id: "s2", repo: "acme/skills", path: "skills/beta/SKILL.md", text: "# Beta\n" },
      ],
    },
    valid: [
      {
        found: [
          {
            id: "s1",
            line: 3,
            quote: "Where this text and the program differ, trust the program.",
          },
        ],
      },
      { found: [] },
    ],
    bad: [
      {
        answer: { found: [{ id: "s1", line: 4, quote: "q" }] },
        why: "is past the last line, 3, of skill s1",
      },
      { answer: { found: [{ id: "s7", line: 1, quote: "q" }] }, why: "is no id of the skills" },
    ],
  },
  names: {
    input: namesInput,
    valid: [{ skills: [notNominated, nominated] }],
    bad: [
      {
        answer: {
          skills: [
            notNominated,
            { ...nominated, reason: "The job is not clear from the name alone." },
          ],
        },
        why: `skills.1.reason: ${NAME_ONLY_REFUSAL}`,
      },
      {
        answer: {
          skills: [
            notNominated,
            { ...nominated, reason: "The name beta alone does not say what the skill does." },
          ],
        },
        why: `skills.1.reason: ${NAME_ONLY_REFUSAL}`,
      },
      {
        answer: { skills: [notNominated, { ...nominated, task: "unclear from the name" }] },
        why: `skills.1.task: ${NAME_ONLY_REFUSAL}`,
      },
      {
        answer: { skills: [notNominated, { ...nominated, proposed: ["gamma"] }] },
        why: "is a name in use",
      },
      {
        answer: { skills: [notNominated, { ...nominated, proposed: ["beta"] }] },
        why: "is the skill's own name",
      },
      {
        answer: { skills: [notNominated, { ...nominated, proposed: ["Greeting_Card"] }] },
        why: "is not lowercase words joined by -",
      },
      {
        answer: {
          skills: [notNominated, { ...nominated, proposed: ["a-1", "a-2", "a-3", "a-4"] }],
        },
        why: "proposed",
      },
      { answer: { skills: [notNominated, { ...nominated, proposed: [] }] }, why: "proposed" },
      {
        answer: { skills: [notNominated, { id: "s2", nominated: false }] },
        why: "is false for a skill whose description has a fault",
      },
      { answer: { skills: [nominated] }, why: "gives no entry for s1" },
      { answer: { skills: [notNominated, notNominated, nominated] }, why: "answers s1 twice" },
    ],
  },
  tasks: {
    input: tasksInput,
    valid: [
      tasksAnswer,
      { terms: ["alpha"], tasks: [...fourEach, written("need", 5), written("near-miss", 5)] },
    ],
    bad: [
      {
        answer: { ...tasksAnswer, tasks: tasksAnswer.tasks.slice(1) },
        why: `holds 4 need tasks whose first step the skill governs, fewer than ${TASKS_PER_LABEL}`,
      },
      {
        answer: { ...tasksAnswer, tasks: [unstated, ...tasksAnswer.tasks.slice(1)] },
        why: "tasks.0.firstStep: is missing: a need task says whether the skill governs its first step",
      },
      {
        // A need task whose first step the skill does not govern is not one of the five.
        answer: {
          ...tasksAnswer,
          tasks: [
            { ...unstated, firstStep: { governed: false, reason: "the file is read first" } },
            ...tasksAnswer.tasks.slice(1),
          ],
        },
        why: `holds 4 need tasks whose first step the skill governs, fewer than ${TASKS_PER_LABEL}`,
      },
      {
        answer: {
          ...tasksAnswer,
          tasks: [{ ...tasksAnswer.tasks[0], messageId: "m9" }, ...tasksAnswer.tasks.slice(1)],
        },
        why: "is no id of the messages",
      },
      {
        answer: {
          ...tasksAnswer,
          tasks: [
            ...tasksAnswer.tasks,
            { label: "need", from: "session", messageId: "m1", why: "again" },
          ],
        },
        why: "takes a message a task already took",
      },
      {
        answer: {
          ...tasksAnswer,
          tasks: [{ ...tasksAnswer.tasks[0], text: "changed" }, ...tasksAnswer.tasks.slice(1)],
        },
        why: "text",
      },
      { answer: { ...tasksAnswer, terms: [] }, why: "terms" },
    ],
  },
  craft: {
    input: craftReaderInput,
    valid: [
      craftAnswer([craftGap({})]),
      allMet,
      // A missing gap names the line it belongs after, 0 before line 1; a nearest gap is judged
      // with no style rule or property.
      craftAnswer(
        [
          craftGap({}),
          craftGap({ line: 0, rule: "C1", kind: "missing", why: "No Use when clause." }),
          craftGap({ file: "references/x.md", line: 3, rule: "C1", kind: "missing", why: "w" }),
          craftGap({ line: 10, rule: "nearest:beta-skill-one:SKILL.md:8", why: "Which tests?" }),
        ],
        "gap"
      ),
    ],
    bad: [
      {
        answer: craftAnswer([craftGap({}), craftGap({ file: "README.md" })]),
        why: "gaps.1.file: is no file of the skill",
      },
      {
        answer: craftAnswer([craftGap({ line: 11 })]),
        why: "gaps.0.line: is past the last line, 10, of SKILL.md",
      },
      {
        answer: craftAnswer([craftGap({ line: 11, kind: "missing" })]),
        why: "gaps.0.line: is past the last line, 10, of SKILL.md",
      },
      {
        answer: craftAnswer([craftGap({ line: 0 })]),
        why: "gaps.0.line: is 0, and a present gap names a line from 1",
      },
      { answer: craftAnswer([craftGap({ rule: "" })]), why: "gaps.0.rule" },
      {
        answer: craftAnswer([{ file: "SKILL.md", line: 9, kind: "present", why: "w" }]),
        why: "gaps.0.rule",
      },
      {
        answer: craftAnswer([craftGap({}), craftGap({ rule: "the paragraph rule" })]),
        why: "gaps.1.rule: is no style rule, reader property or nearest skill's line of the input",
      },
      {
        // C2 is a property a behaviour test checks: no reader property of the input.
        answer: craftAnswer([craftGap({}), craftGap({ rule: "C2" })]),
        why: "gaps.1.rule: is no style rule, reader property or nearest skill's line of the input",
      },
      {
        answer: craftAnswer([craftGap({}), craftGap({ rule: "nearest:gamma:SKILL.md:1" })]),
        why: "gaps.1.rule: names gamma, no nearest skill of the input",
      },
      {
        answer: craftAnswer([craftGap({}), craftGap({ rule: "nearest:beta-skill-one:x.md:1" })]),
        why: "gaps.1.rule: names x.md, no file of the nearest skill beta-skill-one",
      },
      {
        answer: craftAnswer([
          craftGap({}),
          craftGap({ rule: "nearest:beta-skill-one:SKILL.md:9" }),
        ]),
        why: "gaps.1.rule: names line 9, past the last line, 8, of SKILL.md of beta-skill-one",
      },
      { answer: craftAnswer([craftGap({ kind: "absent" })]), why: "gaps.0.kind" },
      {
        answer: { ...allMet, judged: allMet.judged.slice(1) },
        why: "judged: judges no S1",
      },
      {
        answer: { ...allMet, judged: [...allMet.judged, { rule: "S2", verdict: "met" }] },
        why: "judged.3.rule: judges S2 twice",
      },
      {
        answer: { ...allMet, judged: [...allMet.judged, { rule: "S9", verdict: "met" }] },
        why: "judged.3.rule: is no style rule or reader property of the input",
      },
      {
        answer: { ...allMet, judged: [{ rule: "S1", verdict: "fail" }, ...allMet.judged.slice(1)] },
        why: "judged.0.verdict",
      },
      {
        answer: craftAnswer([craftGap({})], "gap"),
        why: "judged.2.verdict: judges C1 gap, and no gap names it",
      },
      {
        answer: { ...allMet, gaps: [craftGap({})] },
        why: "gaps.0.rule: is S2, which the answer does not judge gap",
      },
    ],
  },
  nearest: {
    input: nearestInput,
    valid: [
      noNearest,
      {
        verdict: "equivalent",
        nearest: [nearestEntry("alpha-list", "acme/tools", "equivalent")],
        reason: "alpha-list does the job.",
      },
      {
        verdict: "equivalent",
        nearest: [
          nearestEntry("alpha-notes", "acme/tools", "partial"),
          nearestEntry("alpha-list", "acme/skills", "equivalent"),
        ],
        reason: "The best does the whole job.",
      },
    ],
    bad: [
      {
        answer: {
          ...noNearest,
          verdict: "partial",
          nearest: [nearestEntry("gamma", "acme/tools", "partial")],
        },
        why: "nearest.0: gamma of acme/tools is no candidate",
      },
      {
        answer: {
          ...noNearest,
          verdict: "partial",
          nearest: [nearestEntry("alpha-notes", "acme/skills", "partial")],
        },
        why: "nearest.0: alpha-notes of acme/skills is no candidate",
      },
      {
        answer: {
          verdict: "partial",
          nearest: [
            nearestEntry("alpha-list", "acme/tools", "partial"),
            nearestEntry("alpha-notes", "acme/tools", "partial"),
            nearestEntry("alpha-list", "acme/skills", "partial"),
          ],
          reason: "r",
        },
        why: "nearest",
      },
      {
        answer: { ...noNearest, nearest: [nearestEntry("alpha-list", "acme/tools", "partial")] },
        why: "verdict: is none, and the answer names a candidate",
      },
      {
        answer: { ...noNearest, verdict: "partial" },
        why: "verdict: is not none, and the answer names no candidate",
      },
      {
        answer: {
          verdict: "partial",
          nearest: [
            nearestEntry("alpha-notes", "acme/tools", "partial"),
            nearestEntry("alpha-list", "acme/tools", "equivalent"),
          ],
          reason: "r",
        },
        why: "verdict: is partial, not the best entry's verdict, equivalent",
      },
      {
        answer: {
          verdict: "partial",
          nearest: [
            nearestEntry("alpha-list", "acme/tools", "partial"),
            nearestEntry("alpha-list", "acme/tools", "partial"),
          ],
          reason: "r",
        },
        why: "nearest.1: names alpha-list of acme/tools twice",
      },
      {
        answer: {
          ...noNearest,
          verdict: "none",
          nearest: [nearestEntry("alpha-list", "acme/tools", "none")],
        },
        why: "nearest.0.verdict",
      },
      {
        answer: {
          verdict: "partial",
          nearest: [{ ...nearestEntry("alpha-list", "acme/tools", "partial"), reason: "" }],
          reason: "r",
        },
        why: "nearest.0.reason",
      },
    ],
  },
  claim: {
    input: claimInput,
    valid: [
      { states: true, reason: "The gap says no tests run before the summary." },
      { states: false, reason: "The gap is about which tests, not whether any run." },
    ],
    bad: [
      { answer: { states: "yes", reason: "r" }, why: "states" },
      { answer: { states: true }, why: "reason" },
      { answer: { states: false, reason: "" }, why: "reason" },
    ],
  },
  "proposal-target": {
    input: proposalTargetInput,
    valid: [
      {
        hunks: [{ file: ".claude/CLAUDE.md", hunk: 1 }],
        targets: [dotfilesTarget(".claude/CLAUDE.md")],
      },
      // A renamed file named by its path before the change; a skill of the list beside a none.
      {
        hunks: [
          { file: "skills/alpha/notes.md", hunk: 0 },
          { file: ".claude/CLAUDE.md", hunk: 0 },
        ],
        targets: [skillTarget(DOTFILES, "alpha"), noTarget],
      },
      // A deleted file by its path before; a file the change would add is a dotfiles target.
      {
        hunks: [{ file: "omp/old.md", hunk: 0 }],
        targets: [dotfilesTarget("omp/rules/new.md", "The line would go to a new rule file.")],
      },
      { hunks: [{ file: "omp/old.md", hunk: 0 }], targets: [noTarget] },
    ],
    bad: [
      {
        answer: { hunks: [{ file: "README.md", hunk: 0 }], targets: [noTarget] },
        why: "hunks.0.file: is no file of the diff",
      },
      {
        answer: { hunks: [{ file: ".claude/CLAUDE.md", hunk: 2 }], targets: [noTarget] },
        why: "hunks.0.hunk: is past the last hunk, 1, of .claude/CLAUDE.md",
      },
      {
        answer: {
          hunks: [
            { file: "skills/alpha/notes.md", hunk: 0 },
            { file: "skills/alpha/references/notes.md", hunk: 0 },
          ],
          targets: [noTarget],
        },
        why: "hunks.1: names hunk 0 of skills/alpha/references/notes.md twice",
      },
      { answer: { hunks: [], targets: [noTarget] }, why: "hunks" },
      {
        answer: {
          hunks: [{ file: "omp/old.md", hunk: 0 }],
          targets: [skillTarget(LEGION, "gamma")],
        },
        why: "targets.0.skill: gamma of acme/skills at 1a2b3c4d is no skill of the list",
      },
      {
        answer: {
          hunks: [{ file: "omp/old.md", hunk: 0 }],
          targets: [skillTarget(LEGION, "beta-notes")],
        },
        why: "targets.0.skill: beta-notes of acme/skills at 1a2b3c4d is no skill of the list",
      },
      {
        answer: { hunks: [{ file: "omp/old.md", hunk: 0 }], targets: [{ kind: "none" }] },
        why: "targets.0.why",
      },
      {
        answer: { hunks: [{ file: "omp/old.md", hunk: 0 }], targets: [{ kind: "none", why: " " }] },
        why: "targets.0.why: is blank",
      },
      {
        answer: {
          hunks: [{ file: "omp/old.md", hunk: 0 }],
          targets: [
            { ...dotfilesTarget(".claude/CLAUDE.md"), skill: { repo: LEGION, name: "alpha" } },
          ],
        },
        why: "targets.0",
      },
      {
        answer: {
          hunks: [{ file: "omp/old.md", hunk: 0 }],
          targets: [dotfilesTarget("../outside/CLAUDE.md")],
        },
        why: "targets.0.path: is not a path inside the repository",
      },
      {
        answer: {
          hunks: [{ file: "omp/old.md", hunk: 0 }],
          targets: [dotfilesTarget(".claude/CLAUDE.md"), dotfilesTarget(".claude/CLAUDE.md")],
        },
        why: "targets.1: names the target .claude/CLAUDE.md twice",
      },
      { answer: { hunks: [{ file: "omp/old.md", hunk: 0 }], targets: [] }, why: "targets" },
    ],
  },
  proposal: {
    input: proposalInput,
    valid: [
      {
        fixes: [fix({ finding: 1 })],
        makes: [make({ file: "alpha/SKILL.md", line: 9 })],
        summary: SUMMARY,
      },
      {
        fixes: [fix({ rule: "C1", file: ".claude/CLAUDE.md", line: 3 })],
        makes: [],
        summary: SUMMARY,
      },
      // A make in a file the change adds (no text today), one in a hunk's file, and one with no
      // file.
      {
        fixes: [],
        makes: [
          make({ rule: "S1", file: "omp/rules/new.md" }),
          make({ file: "skills/alpha/SKILL.md" }),
          make({}),
        ],
        summary: SUMMARY,
      },
    ],
    bad: [
      {
        answer: { fixes: [fix({ finding: 0, rule: "S2" })], makes: [], summary: SUMMARY },
        why: "fixes.0: gives both a finding and a rule",
      },
      {
        answer: { fixes: [fix({})], makes: [], summary: SUMMARY },
        why: "fixes.0: gives neither a finding nor a rule",
      },
      {
        answer: { fixes: [fix({ finding: 2 })], makes: [], summary: SUMMARY },
        why: "fixes.0.finding: is no finding of the input, which holds 2",
      },
      {
        answer: { fixes: [fix({ rule: "S9" })], makes: [], summary: SUMMARY },
        why: "fixes.0.rule: is no style rule or reader property of the input",
      },
      {
        // C2 is a property a behaviour test checks: no reader property of the input.
        answer: { fixes: [], makes: [make({ rule: "C2" })], summary: SUMMARY },
        why: "makes.0.rule: is no style rule or reader property of the input",
      },
      {
        answer: { fixes: [], makes: [make({ rule: "nearest:beta:SKILL.md:1" })], summary: SUMMARY },
        why: "makes.0.rule: is not S<n> or C<n>",
      },
      {
        answer: { fixes: [], makes: [make({ file: "SKILL.md", line: 2 })], summary: SUMMARY },
        why: "makes.0.file: is no file of the targets or the hunks",
      },
      {
        answer: {
          fixes: [fix({ rule: "S1", file: ".claude/CLAUDE.md", line: 4 })],
          makes: [],
          summary: SUMMARY,
        },
        why: "fixes.0.line: is past the last line, 3, of .claude/CLAUDE.md",
      },
      {
        answer: { fixes: [], makes: [make({ line: 2 })], summary: SUMMARY },
        why: "makes.0.line: names a line and no file",
      },
      {
        answer: { fixes: [], makes: [], summary: "One line.\nAnother line." },
        why: "summary: is not one non-blank line",
      },
      { answer: { fixes: [], makes: [] }, why: "summary" },
    ],
  },
  "written-case": {
    input: writtenCaseInput,
    valid: [
      { tasks: [writtenTask] },
      {
        tasks: [
          writtenTask,
          task({
            line: {
              file: "SKILL.md",
              number: 8,
              text: "1. Read `references/x.md` before the first step.",
            },
            followingStep: { text: "I read the reference first.", toolCalls: [] },
          }),
        ],
      },
    ],
    bad: [
      {
        // A line the base text does not hold, at the number of one it holds.
        answer: {
          tasks: [task({ line: { ...writtenTask.line, text: "2. Write each result twice." } })],
        },
        why: "tasks.0.line.text: is not line 9 of SKILL.md",
      },
      {
        answer: { tasks: [task({ line: { ...writtenTask.line, number: 11 } })] },
        why: "tasks.0.line.number: is past the last line, 10, of SKILL.md",
      },
      {
        answer: { tasks: [task({ line: { ...writtenTask.line, file: "README.md" } })] },
        why: "tasks.0.line.file: is no file of the base text",
      },
      {
        answer: { tasks: [writtenTask, writtenTask, writtenTask] },
        why: "tasks: holds 3 tasks, more than the 2 asked for",
      },
      { answer: { tasks: [] }, why: "tasks" },
      { answer: { tasks: [task({ task: " " })] }, why: "tasks.0.task: is blank" },
      {
        answer: { tasks: [task({ followingStep: { text: " ", toolCalls: [] } })] },
        why: "tasks.0.followingStep: holds no text and no tool call",
      },
      { answer: { tasks: [task({ violatingStep: undefined })] }, why: "tasks.0.violatingStep" },
      { answer: { tasks: [task({ criterion: { pass: "x" } })] }, why: "tasks.0.criterion.fail" },
    ],
  },
};

test("each reader's schema refuses a bad answer and a valid one parses", () => {
  expect(Object.keys(ROWS).sort()).toEqual([...READER_NAMES].sort());
  for (const name of READER_NAMES) {
    const row = ROWS[name];
    for (const answer of row.valid)
      expect({ name, refused: refusal(name, row.input, answer) }).toEqual({
        name,
        refused: undefined,
      });
    for (const { answer, why } of row.bad) {
      const refused = refusal(name, row.input, answer);
      expect({ name, refused: refused?.includes(why) ? why : refused }).toEqual({
        name,
        refused: why,
      });
    }
  }
});

test("the names reader refuses a reason that is only that the job is not clear from the name alone", () => {
  for (const reason of [
    "The job is not clear from the name alone.",
    "The name alone doesn't say what the skill does.",
    "Without reading the description, an agent cannot know what alpha is for.",
    "The name is too generic.",
  ]) {
    expect({ reason, nameOnly: nameOnlyReason(reason, "alpha") }).toEqual({
      reason,
      nameOnly: true,
    });
  }
  for (const reason of [
    "The name suggests running tests, and the skill sets up experiments.",
    "An agent asked to write a greeting card would look for a greeting skill, not alpha.",
  ]) {
    expect({ reason, nameOnly: nameOnlyReason(reason, "alpha") }).toEqual({
      reason,
      nameOnly: false,
    });
  }
});

test("a reader makes one call per batch, and asks again while the answer is refused", async () => {
  // A valid answer: one call, its answer, no refusal.
  const one = world(ROWS.map.valid[0]);
  const result = await runReader("map", mapInput, one.gateway);
  expect(one.bodies).toHaveLength(1);
  expect(result.answer).toEqual(ROWS.map.valid[0]);
  expect(result.refusals).toEqual([]);
  expect(result.spent).toHaveLength(1);
  // The request: the base and cut texts with their lines numbered, and the audit or None.
  const [message] = UserMessage.parse(one.bodies[0]?.messages);
  const text = message.content[0]?.text ?? "";
  expect(text).toContain('<base>\n<file path="SKILL.md">\n1: ---\n2: name: alpha');
  expect(text).toContain(
    "9: 2. Write each step's result on one line.\n10: 3. Run the tests where needed.\n</file>\n</base>"
  );
  expect(text).toContain("9: 3. Run the tests where needed.\n</file>\n</cut>");
  expect(one.bodies[0]).toEqual(readerBody("map", mapInput));
  expect(text.endsWith("<audit>\nNone.\n</audit>")).toBe(true);

  // A name-only reason, then text that is no JSON, then a valid fenced answer: three calls of the
  // same body, two refusals kept, the third answer taken.
  const nameOnly = {
    skills: [notNominated, { ...nominated, reason: "The job is not clear from the name alone." }],
  };
  const retried = world(
    nameOnly,
    "I nominate beta.",
    `\`\`\`json\n${JSON.stringify(ROWS.names.valid[0])}\n\`\`\``
  );
  const names = await runReader("names", namesInput, retried.gateway);
  expect(retried.bodies).toHaveLength(3);
  expect(retried.bodies[1]).toEqual(retried.bodies[0]);
  expect(names.answer).toEqual(ROWS.names.valid[0]);
  expect(names.refusals).toHaveLength(2);
  expect(names.refusals[0]).toContain(NAME_ONLY_REFUSAL);
  expect(names.refusals[1]).toContain("the answer is not one JSON value");

  // Refused on every try: ReaderFailed with each refusal, after READER_TRIES calls.
  const never = world(nameOnly);
  const failed = await runReader("names", namesInput, never.gateway).catch(
    (error: unknown) => error
  );
  expect(never.bodies).toHaveLength(READER_TRIES);
  if (!(failed instanceof ReaderFailed))
    throw new Error(`expected ReaderFailed, got ${messageFor(failed)}`);
  expect(failed.refusals).toHaveLength(READER_TRIES);
  expect(failed.refusals.every((each) => each.includes(NAME_ONLY_REFUSAL))).toBe(true);

  // An input the reader's schema refuses makes no call.
  const none = world(ROWS.map.valid[0]);
  await expect(runReader("map", { ...mapInput, base: [] }, none.gateway)).rejects.toThrow(
    "the map reader's input is refused: base"
  );
  await expect(
    runReader("same-job", { skills: [skillsInput.skills[0], skillsInput.skills[0]] }, none.gateway)
  ).rejects.toThrow("gives the id s1 twice");
  expect(none.bodies).toHaveLength(0);
});

test("the case reader's request shows only the offered lines, each with its number in the file, and says a line not shown cannot be a case's line; its input refuses a file with no line and a blank line", () => {
  const [user] = UserMessage.parse(readerBody("case", caseInput).messages);
  const text = user.content.map((part) => part.text).join("\n\n");
  expect(text).toStartWith('<skill name="alpha">\nOnly the lines a case may name are shown');
  expect(text).toContain("A line not shown cannot be a case's line");
  expect(text).toContain(
    '<file path="SKILL.md">\n6: # Alpha\n8: 1. Read `references/x.md` before the first step.\n9: 2. Write each step\'s result on one line.\n</file>\n</skill>'
  );
  expect(text).not.toContain("3. Run the tests where needed.");
  const file = caseInput.files[0];
  expect(READERS.case.input.safeParse({ ...caseInput, files: [] }).success).toBe(false);
  expect(
    READERS.case.input.safeParse({ ...caseInput, files: [{ ...file, lines: [] }] }).success
  ).toBe(false);
  expect(
    READERS.case.input.safeParse({
      ...caseInput,
      files: [{ ...file, lines: [{ number: 7, text: " " }] }],
    }).success
  ).toBe(false);
});

test("in control mode the tasks reader takes every message, once, as a need task saying whether the skill governs its first step, and its request says so", () => {
  const input = { ...tasksInput, mode: "control" };
  const take = (id: string, governed: boolean) => ({
    label: "need",
    from: "session",
    messageId: id,
    why: "it asks for the checklist",
    firstStep: { governed, reason: governed ? "the checklist comes first" : "a file comes first" },
  });
  expect(refusal("tasks", input, { terms: [], tasks: [take("m1", true), take("m2", false)] })).toBe(
    undefined
  );
  expect(refusal("tasks", input, { terms: [], tasks: [take("m1", true)] })).toContain(
    "leaves out the message m2"
  );
  expect(
    refusal("tasks", input, {
      terms: [],
      tasks: [take("m1", true), take("m2", true), written("near-miss", 1)],
    })
  ).toContain("is not a need task of a message");
  const bare = Object.fromEntries(
    Object.entries(take("m2", true)).filter(([key]) => key !== "firstStep")
  );
  expect(refusal("tasks", input, { terms: [], tasks: [take("m1", true), bare] })).toContain(
    "a need task says whether the skill governs its first step"
  );
  const [user] = UserMessage.parse(readerBody("tasks", input).messages);
  expect(user.content[0]?.text).toStartWith("<control>\nEach message is the first request");
  const [picking] = UserMessage.parse(readerBody("tasks", tasksInput).messages);
  expect(picking.content[0]?.text).toStartWith('<skill name="alpha">');
});

test("the audit's request holds the line, the messages before the step and the step, and its input refuses a criterion; the restatement check's holds the line and its window", () => {
  const [audit] = UserMessage.parse(readerBody("audit", auditInput).messages);
  expect(audit.content.map((part) => part.text)).toEqual([
    `<line>\n${auditInput.line}\n</line>\n\n<before>\n${auditInput.before}\n</before>\n\n<step>\n${auditInput.step}\n</step>`,
  ]);
  expect(READERS.audit.input.safeParse({ ...auditInput, criterion: CRITERION }).success).toBe(
    false
  );
  const [restate] = UserMessage.parse(readerBody("restate", restateInput).messages);
  const text = restate.content[0]?.text ?? "";
  expect(text.startsWith(`<line>\n${restateInput.line}\n</line>\n\n<messages>\n`)).toBe(true);
  expect(text).toContain(
    '<message index="5" role="toolResult">\n[part 2 of 2 of this message]\nKeep each result to a single line.\n</message>'
  );
});

// Catches: a written-case request that hides the base text's line numbers, the session cases or
// the context session's first message, or a prompt that lets a task restate its line.
test("the written-case reader's request numbers the base text and gives the session cases, the context session's first message and the count", () => {
  const [user] = UserMessage.parse(readerBody("written-case", writtenCaseInput).messages);
  const text = user.content.map((part) => part.text).join("\n\n");
  expect(text).toStartWith(
    '<skill name="alpha">\n<file path="SKILL.md">\n1: ---\n2: name: alpha\n'
  );
  expect(text).toContain(
    "9: 2. Write each step's result on one line.\n10: 3. Run the tests where needed.\n</file>\n</skill>"
  );
  expect(text).toContain(
    `<session-cases>\n<case n="1">\n<step>\n${writtenCaseInput.sessionCases[0]?.step}\n</step>\n<criterion>\nPass: ${CRITERION.pass}\nFail: ${CRITERION.fail}\n</criterion>\n</case>\n</session-cases>`
  );
  expect(text).toContain(`<first-message>\n${writtenCaseInput.firstMessage}\n</first-message>`);
  expect(text).toEndWith("<count>2</count>");
  // A skill with no session case says so.
  const [none] = UserMessage.parse(
    readerBody("written-case", { ...writtenCaseInput, sessionCases: [] }).messages
  );
  expect(none.content[0]?.text).toContain("<session-cases>\nNone.\n</session-cases>");
  // The prompt asks for a task of the context session's own work, one the skill applies to, that
  // states nothing of its line.
  const prompt = loadPrompt("written-case").text;
  for (const words of [
    "must not state, restate or hint at what the line says",
    "reads like the real work of the session `<first-message>` comes from",
    "The skill applies to the task",
  ]) {
    expect({ words, said: prompt.includes(words) }).toEqual({ words, said: true });
  }
});

// The craft reader, and the style text's rule ids it judges (style.ts).

// Catches: a gap the reader places in no file of the skill, or past its end, or against no rule
// the input gave, kept as a finding; or a missing element whose place is lost.
test("a craft gap outside the file, or with no rule id, is refused; a missing gap carries the line it belongs after", () => {
  // Each answer holds S2's gap, which its judgement needs, and the gap under test second.
  const one = (gap: Record<string, unknown>) =>
    refusal("craft", craftReaderInput, craftAnswer([craftGap({}), craftGap(gap)], "gap"));
  expect(one({ file: "README.md", rule: "C1" })).toContain("gaps.1.file: is no file of the skill");
  expect(one({ line: 11 })).toContain("gaps.1.line: is past the last line, 10, of SKILL.md");
  expect(one({ file: "references/x.md", line: 4, rule: "C1" })).toContain(
    "gaps.1.line: is past the last line, 3, of references/x.md"
  );
  // Each rule id no input gives is refused at the gap's rule.
  for (const rule of ["", "S", "the order table", "C2", "S3", "nearest:beta-skill-one:SKILL.md"]) {
    expect({ rule, refused: one({ rule })?.includes("gaps.1.rule") }).toEqual({
      rule,
      refused: true,
    });
  }
  const { rule: _, ...noRule } = craftGap({});
  expect(refusal("craft", craftReaderInput, craftAnswer([craftGap({}), noRule], "met"))).toContain(
    "gaps.1.rule"
  );
  // A missing gap names the line after which the missing element belongs: 0 before line 1, and
  // at most the file's last line.
  for (const line of [0, 4, 10]) {
    expect({ line, refused: one({ line, kind: "missing", rule: "C1" }) }).toEqual({
      line,
      refused: undefined,
    });
  }
  expect(one({ line: 11, kind: "missing", rule: "C1" })).toContain(
    "gaps.1.line: is past the last line, 10, of SKILL.md"
  );
  expect(one({ line: 0, rule: "C1" })).toContain(
    "gaps.1.line: is 0, and a present gap names a line from 1"
  );
  // A gap against a nearest skill names a line of a file of it, and no judgement.
  expect(one({ rule: "nearest:beta-skill-one:SKILL.md:6" })).toContain("judges C1 gap");
  expect(
    refusal(
      "craft",
      craftReaderInput,
      craftAnswer([craftGap({}), craftGap({ rule: "nearest:beta-skill-one:SKILL.md:6" })])
    )
  ).toBeUndefined();
});

// Catches: a rule judged twice or not at all, a rule judged gap with no gap, or a gap against a
// rule the answer judges met.
test("the craft answer judges each style rule and reader property once, and its gaps agree with its judgements", () => {
  const judged = (list: readonly unknown[]) =>
    refusal("craft", craftReaderInput, { judged: list, gaps: [] });
  expect(judged(allMet.judged.filter((each) => each.rule !== "C1"))).toContain(
    "judged: judges no C1"
  );
  expect(judged([...allMet.judged, allMet.judged[0]])).toContain("judges S1 twice");
  expect(judged([...allMet.judged, { rule: "C2", verdict: "met" }])).toContain(
    "is no style rule or reader property of the input"
  );
  expect(refusal("craft", craftReaderInput, craftAnswer([]))).toContain(
    "judged.1.verdict: judges S2 gap, and no gap names it"
  );
  expect(
    refusal("craft", craftReaderInput, { ...allMet, gaps: [craftGap({ rule: "C1" })] })
  ).toContain("gaps.0.rule: is C1, which the answer does not judge gap");
});

// Catches: a property a behaviour test checks reaching the reader, which would judge it on a
// skill's text.
test("the craft reader's input holds only reader properties", () => {
  const list = parseCraftList(fixture("F12-craft-list.json"));
  const style = styleRules(fixture("style-check.md"));
  const input = CraftInput.parse(
    craftInput({
      skill: "alpha",
      files: craftReaderInput.files,
      style,
      list,
      nearest: craftReaderInput.nearest,
    })
  );
  expect(input.properties).toEqual([
    {
      id: "C1",
      property: "The description says when to use the skill, and never sums up its steps.",
      readerLooksFor:
        'In the frontmatter description: a "Use when" clause naming the situations that call for the skill; no list of its steps.',
    },
    {
      id: "C3",
      property: "The skill's name uses only letters, numbers and hyphens.",
      readerLooksFor: "The frontmatter name holds only letters, digits and hyphens.",
    },
  ]);
  expect(input.style.map((rule) => rule.id)).toEqual(style.map((rule) => rule.id));
  const [user] = UserMessage.parse(readerBody("craft", input).messages);
  const text = user.content.map((part) => part.text).join("\n\n");
  expect(text).not.toContain("C2");
  expect(text).not.toContain("an arm that runs with no skill");
  expect(text).toContain(
    '<property id="C3">\n<states>The skill\'s name uses only letters, numbers and hyphens.</states>\n<looks-for>The frontmatter name holds only letters, digits and hyphens.</looks-for>\n</property>'
  );
  // The input refuses a property that carries how it is checked.
  expect(
    READERS.craft.input.safeParse({
      ...craftReaderInput,
      properties: [{ ...craftReaderInput.properties[0], checkedBy: "behaviour" }],
    }).success
  ).toBe(false);
});

test("the craft reader's request numbers each file's lines, and gives each style rule with its id and each nearest skill's files", () => {
  const [user] = UserMessage.parse(readerBody("craft", craftReaderInput).messages);
  const text = user.content.map((part) => part.text).join("\n\n");
  expect(text).toStartWith('<skill name="alpha">\n<file path="SKILL.md">\n1: ---\n2: name: alpha');
  expect(text).toContain(
    '<file path="references/x.md">\n1: # X\n2: \n3: Read me first.\n</file>\n</skill>'
  );
  expect(text).toContain(
    '<style>\n<rule id="S1" line="9" part="the order table">\n| `**Done when:**` | The check that ends the skill |\n</rule>\n<rule id="S2" line="19" part="the rules">'
  );
  expect(text).toContain(
    '<nearest>\n<skill name="beta-skill-one">\n<file path="SKILL.md">\n1: ---\n2: name: beta-skill-one'
  );
  expect(text).toContain(
    "8: 3. Run the named tests before you report.\n</file>\n</skill>\n</nearest>"
  );
  const [alone] = UserMessage.parse(
    readerBody("craft", { ...craftReaderInput, nearest: [] }).messages
  );
  expect(alone.content[0]?.text).toEndWith(
    "<nearest>\nNone: no nearest skill of theirs was given.\n</nearest>"
  );
});

// The style texts are made up in the shapes of the two the reader reads (testdata/CAPTURE.md, "The
// style texts").
// Catches: a style text whose rules get no ids, ids out of the text's order, an example or a
// source of a rule counted as a rule of its own, or a check item left out.
test("a style text's rules get ids S1, S2, ... in its order: each order-table row, each item under Rules, each under Check before you commit", () => {
  // The numbered shape: 5 table rows and 10 numbered rules, each with its nested example and
  // source.
  const numbered = styleRules(fixture("style-numbered.md"));
  expect(numbered.map((rule) => rule.id)).toEqual(
    Array.from({ length: 15 }, (_, n) => `S${n + 1}`)
  );
  expect(numbered.map((rule): string => rule.part)).toEqual([
    ...Array<string>(5).fill("order"),
    ...Array<string>(10).fill("rules"),
  ]);
  expect(numbered[0]).toEqual({
    id: "S1",
    line: 15,
    part: "order",
    text: "| `# Title`, then one or two sentences | The goal: what is true when the skill has worked |",
  });
  expect(numbered[5]?.line).toBe(23);
  expect(numbered[5]?.text.split("\n")).toEqual([
    "1. **Open with the goal in one or two sentences, then a `**Done when:**` line.**",
    "   - Example: `Rotate the build key with no outage.` then `**Done when:** the old key is revoked and each job runs with the new one.`",
    '   - Source: as `alpha-skill-one/SKILL.md:10` ("**Core rule:** evidence before claims."); bs `beta-skill-one/SKILL.md:11` ("**Done when:** the review is complete").',
  ]);
  expect(numbered[14]?.line).toBe(50);
  expect(numbered[14]?.text).toStartWith("10. **Name each reference at the step that needs it");
  expect(numbered[14]?.text.split("\n")).toHaveLength(3);
  // The check shape: 7 table rows, 10 items under Rules and 3 under Check before you commit; the
  // paragraph rule is a check item with its own id.
  const check = styleRules(fixture("style-check.md"));
  expect(check.map((rule) => [rule.id, rule.line, rule.part])).toEqual([
    ...[9, 10, 11, 12, 13, 14, 15].map((line, n) => [`S${n + 1}`, line, "order"]),
    ...[19, 20, 21, 22, 23, 24, 25, 26, 27, 28].map((line, n) => [`S${n + 8}`, line, "rules"]),
    ...[32, 33, 34].map((line, n) => [`S${n + 18}`, line, "check"]),
  ]);
  expect(check[18]).toEqual({
    id: "S19",
    line: 33,
    part: "check",
    text: "- No paragraph holds more than one rule.",
  });
  // A text with no table row and no item under Rules or Check is refused.
  expect(() =>
    styleRules(
      "# Notes\n\n- An item under no Rules heading.\n\n## Rules\n\nThe rules are elsewhere.\n"
    )
  ).toThrow("the style text holds no rule");
  expect(styleRules("## Rules\n\nSee below.\n\n- **Plain words.**\n")).toEqual([
    { id: "S1", line: 5, part: "rules", text: "- **Plain words.**" },
  ]);
});

// The nearest and claim readers.

// Catches: an answer naming a skill no candidate is, more than two, or a verdict its entries do
// not give.
test("nearest answers none or one or two candidates, each with a verdict and a reason", () => {
  const entry = nearestEntry("alpha-list", "acme/tools", "partial");
  const answer = (nearest: unknown[], verdict = "partial") => ({ verdict, nearest, reason: "r" });
  expect(refusal("nearest", nearestInput, answer([{ ...entry, name: "alpha-lists" }]))).toContain(
    "alpha-lists of acme/tools is no candidate"
  );
  expect(refusal("nearest", nearestInput, answer([entry], "none"))).toContain(
    "verdict: is none, and the answer names a candidate"
  );
  expect(refusal("nearest", nearestInput, answer([], "equivalent"))).toContain(
    "verdict: is not none, and the answer names no candidate"
  );
  expect(refusal("nearest", nearestInput, answer([entry], "equivalent"))).toContain(
    "is equivalent, not the best entry's verdict, partial"
  );
  expect(refusal("nearest", nearestInput, answer([{ ...entry, verdict: undefined }]))).toContain(
    "nearest.0.verdict"
  );
  // The request quotes the skill's whole description and body, and each candidate's whole
  // description with its source.
  const [user] = UserMessage.parse(readerBody("nearest", nearestInput).messages);
  const text = user.content[0]?.text ?? "";
  expect(text).toStartWith(
    `<skill name="alpha">\n<description>\n${nearestInput.skill.description}\n</description>\n<body>\n${nearestInput.skill.body}\n</body>\n</skill>`
  );
  expect(text).toContain(
    '<candidate name="alpha-list" source="acme/skills">\nUse to print the alpha list.\n</candidate>'
  );
  expect(
    READERS.nearest.input.safeParse({
      ...nearestInput,
      candidates: [nearestInput.candidates[0], nearestInput.candidates[0]],
    }).success
  ).toBe(false);
});

// Catches: a claim answer that is not a plain yes or no, or that gives no reason.
test("the claim answer is yes or no with a reason", () => {
  for (const answer of [
    { states: "yes", reason: "r" },
    { states: 1, reason: "r" },
    { states: true },
    { states: false, reason: " " },
    { states: true, reason: "r", line: 10 },
  ]) {
    expect({ answer, refused: refusal("claim", claimInput, answer) !== undefined }).toEqual({
      answer,
      refused: true,
    });
  }
  const [user] = UserMessage.parse(readerBody("claim", claimInput).messages);
  expect(user.content[0]?.text).toStartWith(
    `<claim>\n${claimInput.claim}\n</claim>\n\n<gap file="SKILL.md" line="10" rule="S2" kind="present">\nNothing says which tests to run before the summary.\n</gap>\n\n<skill>\n<file path="SKILL.md">\n1: ---`
  );
  // Its input refuses a gap in no file of the skill, and a claim of two lines.
  expect(
    READERS.claim.input.safeParse({ ...claimInput, gap: { ...claimInput.gap, file: "x.md" } })
      .success
  ).toBe(false);
  expect(READERS.claim.input.safeParse({ ...claimInput, claim: "one\ntwo" }).success).toBe(false);
});

// The proposal readers.

// Catches: a hunk of a file the diff does not hold, or past its file's last hunk, or a skill the
// reader does not read, passed on to the proposal reader as the item's change or its target.
test("proposal-target names only files of the diff or skills of the list", () => {
  const refused = (answer: unknown) => refusal("proposal-target", proposalTargetInput, answer);
  // The item's text names CLAUDE.md, and a skill's file the diff does not hold is no file of it.
  expect(
    refused({ hunks: [{ file: "skills/alpha/SKILL.md", hunk: 0 }], targets: [noTarget] })
  ).toContain("hunks.0.file: is no file of the diff");
  // A renamed file has one hunk, by either of its paths.
  expect(
    refused({ hunks: [{ file: "skills/alpha/notes.md", hunk: 1 }], targets: [noTarget] })
  ).toContain("hunks.0.hunk: is past the last hunk, 0, of skills/alpha/references/notes.md");
  // A path that names two files of the diff, a new file and a file renamed away from it, is the
  // new file's: the one the diff shows at that path (proposals.ts's item takes the same).
  const moved = {
    ...proposalTargetInput,
    diff: {
      ...proposalTargetInput.diff,
      files: [
        { before: null, after: "rules.md", hunks: ["@@ -0,0 +1,2 @@"] },
        {
          before: "rules.md",
          after: "rules-old.md",
          hunks: ["@@ -1,2 +1,2 @@", "@@ -10,2 +10,2 @@"],
        },
      ],
    },
  };
  expect(
    refusal("proposal-target", moved, {
      hunks: [{ file: "rules.md", hunk: 1 }],
      targets: [noTarget],
    })
  ).toContain("hunks.0.hunk: is past the last hunk, 0, of rules.md");
  // A skill is named by its repository and its name, as the list gives both.
  expect(
    refused({
      hunks: [{ file: "omp/old.md", hunk: 0 }],
      targets: [skillTarget("acme/tools", "alpha")],
    })
  ).toContain("targets.0.skill: alpha of acme/tools is no skill of the list");
  expect(
    refused({
      hunks: [{ file: "omp/old.md", hunk: 0 }],
      targets: [{ kind: "skill", skill: { name: "alpha" }, why: "w" }],
    })
  ).toContain("targets.0.skill.repo");
  // The input refuses a skill listed twice, and a file of the diff with no path.
  const input = READERS["proposal-target"].input;
  expect(
    input.safeParse({
      ...proposalTargetInput,
      skills: [proposalTargetInput.skills[0], proposalTargetInput.skills[0]],
    }).success
  ).toBe(false);
  expect(
    input.safeParse({
      ...proposalTargetInput,
      diff: { ...proposalTargetInput.diff, files: [{ before: null, after: null, hunks: [] }] },
    }).success
  ).toBe(false);
});

// Catches: the diff sent after the item, so that no item of a pull request reads it from the
// prompt cache, a request with no cache marker, or a marker on the part that differs per item.
test("proposal-target's body puts the diff first with a cache marker", async () => {
  const body = readerBody("proposal-target", proposalTargetInput);
  const [user] = body.messages as { role: string; content: Record<string, unknown>[] }[];
  expect(body.messages).toHaveLength(1);
  expect(user?.content).toEqual([
    {
      type: "text",
      text: [
        `<diff pr="41">\n${PROPOSAL_DIFF}\n</diff>`,
        "",
        "<hunks>",
        '<file path=".claude/CLAUDE.md">',
        "hunk 0: @@ -1,3 +1,3 @@",
        "hunk 1: @@ -9,2 +9,3 @@ # Notes",
        "</file>",
        '<file path="skills/alpha/references/notes.md" before="skills/alpha/notes.md">',
        "hunk 0: @@ -1,2 +1,2 @@",
        "</file>",
        '<file path="omp/old.md" deleted="yes">',
        "hunk 0: @@ -1,2 +0,0 @@",
        "</file>",
        "</hunks>",
      ].join("\n"),
      cache_control: { type: "ephemeral" },
    },
    {
      type: "text",
      text: [
        "<repository>\nacme/dotfiles at 5e6f7a8b\n</repository>",
        `<item id="41-10.5" state="open">\n${proposalTargetInput.item.text}\n</item>`,
        [
          "<skills>",
          '<skill repo="acme/skills at 1a2b3c4d" name="alpha"/>',
          '<skill repo="acme/dotfiles at 5e6f7a8b" name="alpha"/>',
          '<skill repo="acme/dotfiles at 5e6f7a8b" name="beta-notes"/>',
          "</skills>",
        ].join("\n"),
      ].join("\n\n"),
    },
  ]);
  // Another item of the same pull request: the first part is the same, byte for byte.
  const other = readerBody("proposal-target", {
    ...proposalTargetInput,
    item: {
      id: "41-1.1",
      state: "partly on main",
      text: "before it deletes a rule, it reads why.",
    },
  });
  const [otherUser] = other.messages as { content: Record<string, unknown>[] }[];
  expect(JSON.stringify(otherUser?.content[0])).toBe(JSON.stringify(user?.content[0]));
  expect(otherUser?.content[1]).not.toEqual(user?.content[1]);
  // runReader sends that body.
  const one = world(ROWS["proposal-target"].valid[0]);
  await runReader("proposal-target", proposalTargetInput, one.gateway);
  expect(one.bodies).toEqual([body]);
});

test("the proposal reader's request gives the hunks, each target's numbered text, the findings with their indexes, the style rules and the reader properties", () => {
  const [user] = UserMessage.parse(readerBody("proposal", proposalInput).messages);
  expect(user.content).toHaveLength(1);
  const text = user.content[0]?.text ?? "";
  expect(text).toStartWith(
    `<item id="41-1.1" state="open">\n${proposalInput.item.text}\n</item>\n\n<hunks>\n<hunk file="skills/alpha/SKILL.md" index="0">\n@@ -8,3 +8,4 @@ # Alpha\n 1. Read \`references/x.md\` before the first step.\n+2. Before you delete a rule, find its reason.`
  );
  expect(text).toContain(
    '<targets>\n<target kind="skill" repo="acme/skills at 1a2b3c4d" name="alpha">\n<file path="alpha/SKILL.md">\n1: ---\n2: name: alpha'
  );
  expect(text).toContain(
    '<file path="alpha/references/x.md">\n1: # X\n2: \n3: Read me first.\n</file>\n</target>'
  );
  expect(text).toContain(
    '<target kind="dotfiles" repo="acme/dotfiles at 5e6f7a8b">\n<file path=".claude/CLAUDE.md">\n1: # Notes\n2: \n3: - Check findings first.\n</file>\n</target>'
  );
  expect(text).toContain(
    '<target kind="dotfiles" repo="acme/dotfiles at 5e6f7a8b">\n<file path="omp/rules/new.md">\nNone: the repository holds no such file at this commit.\n</file>\n</target>\n</targets>'
  );
  expect(text).toContain(
    '<findings>\n<finding index="0" skill="alpha" repo="acme/skills at 1a2b3c4d" file="alpha/SKILL.md" line="9" rule="S2" kind="present">\nThe item holds two rules.\n</finding>\n<finding index="1" skill="alpha" repo="acme/skills at 1a2b3c4d" file="alpha/SKILL.md" line="0" rule="C1" kind="missing">\nNo Use when clause.\n</finding>\n</findings>'
  );
  expect(text).toContain(
    '<style>\n<rule id="S1" line="9" part="the order table">\n| `**Done when:**` | The check that ends the skill |\n</rule>'
  );
  expect(text).toEndWith(
    '<craft-properties>\n<property id="C1">\n<states>The description says when to use the skill, and never sums up its steps.</states>\n<looks-for>A Use when clause in the description, and no list of steps.</looks-for>\n</property>\n</craft-properties>'
  );
  const [none] = UserMessage.parse(
    readerBody("proposal", { ...proposalInput, findings: [] }).messages
  );
  expect(none.content[0]?.text).toContain(
    "<findings>\nNone: no target skill has a findings entry with an answer.\n</findings>"
  );
  // Its input refuses the findings of a skill that is no target, and a target with no text and no
  // files.
  const input = READERS.proposal.input;
  expect(
    input.safeParse({
      ...proposalInput,
      findings: [{ ...proposalInput.findings[0], name: "beta" }],
    }).success
  ).toBe(false);
  expect(
    input.safeParse({
      ...proposalInput,
      targets: [{ kind: "skill", repo: LEGION, name: "alpha", files: [] }],
    }).success
  ).toBe(false);
});

test("each prompt file pins its settings, and they reach every request made with it", async () => {
  for (const name of ["judge", ...READER_NAMES]) {
    const settings = parsePrompt(
      name,
      readFileSync(path.join(PROMPTS_DIR, `${name}.md`), "utf8")
    ).settings;
    expect({
      name,
      model: settings.model,
      temperature: settings.temperature,
      effort: settings.effort,
    }).toEqual({
      name,
      model: "claude-opus-5-5",
      temperature: 1,
      effort: "high",
    });
  }
  for (const name of READER_NAMES) {
    const settings = parsePrompt(
      name,
      readFileSync(path.join(PROMPTS_DIR, `${name}.md`), "utf8")
    ).settings;
    const row = ROWS[name];
    // A refused answer, then a valid one: both requests carry the settings.
    const { gateway, bodies } = world({ refused: true }, row.valid[0]);
    await runReader(name, row.input, gateway);
    expect(bodies).toHaveLength(2);
    for (const body of bodies) {
      expect({
        name,
        model: body.model,
        max_tokens: body.max_tokens,
        temperature: body.temperature,
        thinking: body.thinking,
        output_config: body.output_config,
        stream: body.stream,
        system: body.system,
      }).toEqual({
        name,
        model: settings.model,
        max_tokens: settings.max_tokens,
        temperature: settings.temperature,
        thinking: { type: "enabled", budget_tokens: settings.thinking_budget },
        output_config: { effort: settings.effort },
        stream: true,
        system: [
          {
            type: "text",
            text: readFileSync(path.join(PROMPTS_DIR, `${name}.md`), "utf8")
              .replace(/^---\n[\s\S]*?\n---\n/, "")
              .trim(),
          },
        ],
      });
    }
  }
});

test("a prompt header that does not pin every setting, and only those, is refused", () => {
  const good =
    "---\nmodel: claude-opus-5-5\nthinking_budget: 4000\nmax_tokens: 8000\ntemperature: 1\neffort: high\n---\nJudge.\n";
  expect(parsePrompt("p", good)).toEqual({
    name: "p",
    settings: {
      model: "claude-opus-5-5",
      thinking_budget: 4000,
      max_tokens: 8000,
      temperature: 1,
      effort: "high",
    },
    text: "Judge.",
  });
  const refusals: [string, string][] = [
    ["Judge.\n", "prompts/p.md has no header"],
    [good.replace("effort: high\n", ""), "effort"],
    [good.replace("effort: high\n", "effort: high\ntop_k: 5\n"), "top_k"],
    [good.replace("temperature: 1", "temperature: 0.5"), "temperature"],
    [
      good.replace("thinking_budget: 4000", "thinking_budget: 8000"),
      "thinking_budget: is not below max_tokens",
    ],
    [good.replace("thinking_budget: 4000", "thinking_budget: 512"), "thinking_budget"],
    [
      good.replace("max_tokens: 8000", "max_tokens: 8000\nmax_tokens: 9000"),
      "the header gives max_tokens twice",
    ],
    [
      good.replace("model: claude-opus-5-5", "model claude-opus-5-5"),
      'a header line is not "<key>: <value>"',
    ],
    [good.replace("Judge.\n", "\n"), "holds no prompt text"],
  ];
  for (const [file, why] of refusals) expect(() => parsePrompt("p", file)).toThrow(why);
});

test("each control plants its finding and holds only when the answer names it", async () => {
  // map: a rule the cut kept word for word (line 8 of both texts) deleted from the cut text.
  const keptLine = "1. Read `references/x.md` before the first step.";
  const map = plantControl({ reader: "map", input: mapInput, deleteLine: keptLine });
  expect(MapInput.parse(map.input).cut).toEqual([
    { path: "SKILL.md", text: CUT_MD.replace(`${keptLine}\n`, "") },
  ]);
  expect(
    map.held({ rules: [{ ...kept, status: "lost", cut: null, reason: "deleted" }] }).held
  ).toBe(true);
  expect(map.held({ rules: [kept] })).toEqual({
    held: false,
    why: "the rule at SKILL.md:8 of the base text is kept: same words",
  });
  expect(map.held({ rules: [lost] })).toEqual({
    held: false,
    why: "the map gives no rule at SKILL.md:8",
  });
  expect(() =>
    plantControl({
      reader: "map",
      input: mapInput,
      deleteLine: "2. Write each step's result on one line.",
    })
  ).toThrow("the map control's line is no line of the cut text");
  const ran = await runControl(
    { reader: "map", input: mapInput, deleteLine: keptLine },
    world({ rules: [{ ...kept, status: "lost", cut: null, reason: "deleted" }] }).gateway
  );
  expect([ran.held, ran.result.refusals]).toEqual([true, []]);

  // attack: the planted line added at the end of SKILL.md, as a paragraph of its own (line 11).
  const attack = plantControl({
    reader: "attack",
    input: { skill: "alpha", files: [{ path: "SKILL.md", text: CUT_MD }] },
    plantLine: ATTACK_PLANTED_LINE,
  });
  expect(AttackInput.parse(attack.input).files[0]?.text).toBe(
    `${CUT_MD}\n${ATTACK_PLANTED_LINE}\n`
  );
  const finding = (line: number) => ({
    findings: [{ kind: "vague", file: "SKILL.md", line, quote: "where needed", why: "which?" }],
  });
  expect(attack.held(finding(11)).held).toBe(true);
  expect(attack.held(finding(9))).toEqual({
    held: false,
    why: "no finding at SKILL.md:11, the added line",
  });

  // same-job: a rewording of s1's description under a made-up name, paired with s1 only.
  const planted = {
    id: "s4",
    repo: "acme/more",
    name: "checklist-writer",
    description: "Use to produce the list of alpha steps.",
  };
  const sameJob = plantControl({ reader: "same-job", input: skillsInput, planted, copyOf: "s1" });
  expect(SameJobInput.parse(sameJob.input).skills).toEqual([...skillsInput.skills, planted]);
  const pair = (a: string, b: string) => ({
    pairs: [{ a, b, task: "the alpha checklist", why: "w" }],
  });
  expect(sameJob.held(pair("s1", "s4")).held).toBe(true);
  expect(sameJob.held(pair("s1", "s2"))).toEqual({ held: false, why: "no pair of s4 and s1" });
  expect(() =>
    plantControl({
      reader: "same-job",
      input: skillsInput,
      planted: { ...planted, id: "s1" },
      copyOf: "s2",
    })
  ).toThrow("is an id of the skills already");

  // sentence: a rewording added at the end of s2 (line 3).
  const reworded = "Should the program and these steps differ, the program is right.";
  const sentence = plantControl({
    reader: "sentence",
    input: SentenceInput.parse(ROWS.sentence.input),
    into: "s2",
    sentence: reworded,
  });
  expect(SentenceInput.parse(sentence.input).skills[1]?.text).toBe(`# Beta\n\n${reworded}\n`);
  expect(sentence.held({ found: [{ id: "s2", line: 3, quote: reworded }] }).held).toBe(true);
  expect(sentence.held({ found: [{ id: "s1", line: 3, quote: "q" }] }).held).toBe(false);
});

test("a skill's files: its .md files, SKILL.md first and the rest by path", () => {
  const dir = path.join(scratch, "skill-files");
  for (const [file, text] of [
    ["references/b.md", "b\n"],
    ["SKILL.md", "s\n"],
    ["LICENSE", "l\n"],
    ["references/a.md", "a\n"],
  ] as const) {
    mkdirSync(path.dirname(path.join(dir, file)), { recursive: true });
    writeFileSync(path.join(dir, file), text);
  }
  expect(skillFiles(dir)).toEqual([
    { path: "SKILL.md", text: "s\n" },
    { path: "references/a.md", text: "a\n" },
    { path: "references/b.md", text: "b\n" },
  ]);
});
