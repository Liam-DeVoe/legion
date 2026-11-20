import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { SessionEntry, type SessionMessage, stripSkillHeader } from "../skill-scenarios/transcript";
import {
  type BuildIo,
  DROP_REASONS,
  type DropReason,
  fileHash,
  main,
  RESTATE_WINDOWS,
  removeSecrets,
  SECRET_REMOVED,
} from "./build";
import { type Case, loadCases, pathHash } from "./cases";
import {
  INSTALLERS_COLUMNS,
  LOADERS_COLUMNS,
  MANIFESTS_COLUMNS,
  PREFIX_TABLE_COLUMNS,
  REPOSITORIES_COLUMNS,
  SKILL_COPIES_COLUMNS,
  tsv,
} from "./copies";
import { mtimeRange } from "./countsql";
import { blindOrder } from "./judge";
import { READER_TRIES } from "./readers";
import {
  messageSse,
  type RecordedRequest,
  type Recorder,
  sseResponse,
  startRecorder,
} from "./recorder";

// The case and task builders on made-up sessions. Every session is F1 (testdata/F1, an Oh My Pi
// 18.18.6 session of a scratch profile with the made-up skills alpha and beta; CAPTURE.md, "F1"),
// changed in one way per session. F1's messages: 0 the user's prompt, 1 a read of skill://alpha,
// 2 its result, 3 a read of skill://alpha/references/x.md, 4 its result, 5 a read of the missing
// skill://gamma, 6 its result, 7 a `task` call (the step the stand-in reader proposes), 8-11 the
// rest. Message 5's text names the session (`Step 3 (case <name>).`), so the stand-in reader
// knows which session it reads.
//
// The store is a stand-in for the configuration's psqlCommand: a program that reads the query on
// its standard input and prints psql's aligned table of one JSON value per row (F7's shape) from
// a made-up store file, each transcript stored in parts of PART_CHARS characters, so a pull pages.
// The gateway is a stand-in built on recorder.ts; GitHub is a stand-in gh over made-up
// repositories (REPOSITORIES), each as it was at the one commit SHA. The copies output (R4's) is
// of a later read: it lists a skill `newer` that no repository held at SHA, and not `older`,
// which acme/skills held then. The count is three made-up query outputs, one per store. Every
// skill, repository, store and path here is made up, and nothing reaches a network.

const TESTDATA = path.join(import.meta.dir, "testdata");
const F1 = path.join(
  TESTDATA,
  "F1",
  "2026-11-20T01-59-41-780Z_01a1f6ae-51d4-715f-a2a6-59f275bdff80.jsonl"
);
const F1_SKILLS =
  "/legion/scratch/tmp/infra4676/t1-runs/f1/home/.omp/profiles/fixture/agent/skills";
const PART_CHARS = 200;
const SHA = "0123456789abcdef0123456789abcdef01234567";
const LINE = "2. Write each step's result on one line.";
const scratch = temporaryDirectory("skill-cuts-build", { real: true });

/** F1's lines, parsed, as a fresh copy. */
function f1Lines(): Record<string, unknown>[] {
  return readFileSync(F1, "utf8")
    .split("\n")
    .filter((line) => line.trim() !== "")
    .map((line) => JSON.parse(line) as Record<string, unknown>);
}

/** The `message` entries of `lines`, in order. */
function messageLines(lines: Record<string, unknown>[]): { message: SessionMessage }[] {
  return lines.filter((line) => line.type === "message") as unknown as {
    message: SessionMessage;
  }[];
}

/** F1's messages, in order. */
function f1Messages(): SessionMessage[] {
  return f1Lines().flatMap((line) => {
    const entry = SessionEntry.parse(line);
    return entry.type === "message" && entry.message ? [entry.message] : [];
  });
}

/** Puts `entry` into the branch after `after`, its child now `entry`'s. */
function insertAfter(
  lines: Record<string, unknown>[],
  after: Record<string, unknown>,
  entry: Record<string, unknown>
) {
  const child = lines.find((line) => line.parentId === after.id);
  entry.parentId = after.id;
  if (child !== undefined) child.parentId = entry.id;
  lines.splice(lines.indexOf(after) + 1, 0, entry);
}

/** Puts `count` assistant messages, each of `text(n)`, between F1's message 6 and its step. */
function addFillers(lines: Record<string, unknown>[], count: number, text: (n: number) => string) {
  let previous = lines.filter((line) => line.type === "message")[6] ?? {};
  for (let n = 0; n < count; n += 1) {
    const filler = {
      type: "message",
      id: `f111${String(n).padStart(4, "0")}`,
      timestamp: "2026-11-20T01:59:53.300Z",
      message: { role: "assistant", content: [{ type: "text", text: text(n) }] },
    };
    insertAfter(lines, previous, filler);
    previous = filler;
  }
}

/** Replaces the text of the first block of message `index`. */
function setText(lines: Record<string, unknown>[], index: number, text: (old: string) => string) {
  const block = messageLines(lines)[index]?.message.content?.[0];
  if (block === undefined) throw new Error(`F1 has no message ${index}`);
  block.text = text(block.text ?? "");
}

/** The target of the read call of message `index`. */
function setReadTarget(lines: Record<string, unknown>[], index: number, target: string) {
  const call = messageLines(lines)[index]?.message.content?.find(
    (block) => block.type === "toolCall"
  );
  if (call === undefined) throw new Error(`F1's message ${index} holds no call`);
  call.arguments = { ...(call.arguments as Record<string, unknown>), path: target };
}

/** Sets the model F1's `model_change` line names. */
function setModel(lines: Record<string, unknown>[], model: string) {
  const change = lines.find((line) => line.type === "model_change");
  if (change === undefined) throw new Error("F1 has no model_change line");
  change.model = model;
}

/** Moves F1's reads of alpha to the skill directory `dir`: the `[Skill file: …]` line of each of
 *  its two read results. */
function readFrom(dir: string) {
  return (lines: Record<string, unknown>[]) => {
    for (const index of [2, 4]) {
      setText(lines, index, (text) => text.replace(`${F1_SKILLS}/alpha`, dir));
    }
  };
}

const SECRET_TOKEN = fixtureSecret("tok");
const PEM_BODY = fixtureSecret("pem");
const PEM = [
  `-----BEGIN RSA PRIVATE ${"KEY"}-----`,
  PEM_BODY,
  `-----END RSA PRIVATE ${"KEY"}-----`,
].join("\n");

/** Each made-up session: its store, how it changes F1, and its path when not `/sessions/<name>`. */
const SESSIONS: Record<
  string,
  { store: string; change?: (lines: Record<string, unknown>[]) => void; path?: string }
> = {
  good: { store: "store_a" },
  "not-in-base": { store: "store_a" },
  "read-lacks-line": {
    store: "store_a",
    change: (lines) => setText(lines, 2, (text) => text.replace(`${LINE}\n`, "")),
  },
  // The x.md read reads another path, so the session reads only SKILL.md of the skill.
  "skill-md-only": {
    store: "store_a",
    change: (lines) => setReadTarget(lines, 3, "/tmp/notes.md"),
  },
  // The skill's text changed after the session read it: no line of its reads is in the base.
  "no-common": {
    store: "store_a",
    change: (lines) => {
      for (const index of [2, 4]) {
        setText(lines, index, (text) => `${text.split("\n")[0]}\nAn older text of the file.`);
      }
    },
  },
  compacted: {
    store: "store_a",
    change: (lines) => {
      const messages = lines.filter((line) => line.type === "message");
      const kept = messages[5] ?? {};
      insertAfter(lines, messages[6] ?? {}, compaction(kept.id));
    },
  },
  "compacted-before-read": {
    store: "store_a",
    change: (lines) => {
      const first = lines.filter((line) => line.type === "message")[0] ?? {};
      insertAfter(lines, first, compaction(first.id));
    },
  },
  quoted: {
    store: "store_a",
    change: (lines) => setText(lines, 3, () => "Step 2. Write each step's result on one line."),
  },
  // The user's prompt, before the skill's read, states the line in other words.
  restated: {
    store: "store_a",
    change: (lines) => setText(lines, 0, (text) => `${text} ${RESTATED}`),
  },
  // A model whose id the gateway serves, under another provider than Anthropic.
  openai: {
    store: "store_a",
    change: (lines) => setModel(lines, "openai/gpt-6.1-sol"),
  },
  // An Anthropic model behind a gateway's prefix: served, since its id is.
  "gateway-model": {
    store: "store_a",
    change: (lines) => setModel(lines, "ossv-gateway/claude-opus-5-5"),
  },
  // A model no route serves: dropped before any reader reads the session.
  "not-served": {
    store: "store_a",
    change: (lines) => setModel(lines, "openai-codex/gpt-6-astra"),
  },
  // A served model at the skill's read, then one no route serves, before the step.
  "switched-model": {
    store: "store_a",
    change: (lines) =>
      insertAfter(lines, lines.filter((line) => line.type === "message")[6] ?? {}, {
        type: "model_change",
        id: "c0de0001",
        timestamp: "2026-11-20T01:59:53.300Z",
        model: "openai-codex/gpt-6-astra",
      }),
  },
  "by-path": {
    store: "store_a",
    change: (lines) => setReadTarget(lines, 5, `${F1_SKILLS}/alpha/SKILL.md`),
  },
  "no-read": {
    store: "store_a",
    change: (lines) => {
      setReadTarget(lines, 1, "skill://beta");
      setReadTarget(lines, 3, "skill://beta/references/x.md");
    },
  },
  "no-step": { store: "store_a" },
  "other-copy": {
    store: "store_a",
    change: (lines) => {
      for (const index of [2, 4])
        setText(lines, index, (text) =>
          text.replace(`${F1_SKILLS}/alpha`, "/elsewhere/other-skills/alpha")
        );
    },
  },
  // A read in a checkout of the cut's repository: no prefix row maps it, and the path names
  // acme/skills before the skill's directory.
  checkout: { store: "store_a", change: readFrom("/home/dev/acme/skills/skills/alpha") },
  // The same skill directory in a checkout of a fork: the path names forker/skills.
  "checkout-fork": { store: "store_a", change: readFrom("/home/dev/forker/skills/skills/alpha") },
  // A path no prefix row maps and no copy's directory ends: the store's context places it. The
  // count's reads of store_a come from a unit of acme/skills, those of store_c from acme/other's.
  context: { store: "store_a", change: readFrom("/mnt/loaded/alpha") },
  "context-other": { store: "store_c", change: readFrom("/mnt/loaded/alpha") },
  secret: {
    store: "store_a",
    change: (lines) =>
      setText(
        lines,
        0,
        (text) => `${text} Sign in with Bearer ${SECRET_TOKEN} and this key:\n${PEM}`
      ),
  },
  legion: {
    store: "store_b",
    change: (lines) => {
      const header = lines.find((line) => line.type === "session");
      if (header !== undefined) header.cwd = "/legion/workspaces/acme/widgets/acme-12";
      setText(
        lines,
        0,
        () => "Continue Make the widget. Issue: ACME-12. Phase: testing. Handoff: none."
      );
      for (const index of [2, 4])
        setText(lines, index, (text) =>
          text.replace(`${F1_SKILLS}/alpha`, "/opt/legion/pi-acme/dist/skills/alpha")
        );
    },
  },
  "no-role": { store: "store_b" },
  // A Legion root architect, which the daemon starts with "<KEY> is yours and …".
  "root-architect": {
    store: "store_b",
    change: (lines) =>
      setText(
        lines,
        0,
        () =>
          "ACME-12 is yours and its tree is open at generation 1, holding an admission slot while nothing in it runs. Take it up in this turn, as your role says."
      ),
  },
  // A Legion architect of a child issue, whose first message is the daemon's catch-up notice.
  "child-architect": {
    store: "store_b",
    change: (lines) => insertAfter(lines, branchHead(lines), catchUpNotice()),
  },
  // A Legion reviewer an architect's Envoy message woke: its role is the claim it holds, beside
  // the workspace its path names.
  "claimed-reviewer": {
    store: "store_b",
    path: "/sessions/--legion-workspaces-acme-widgets-acme-12--/claimed.jsonl",
    change: (lines) =>
      insertAfter(lines, branchHead(lines), {
        type: "custom",
        customType: "envoy-role-claim",
        id: "c1a1d001",
        timestamp: "2026-11-20T01:59:43.600Z",
        data: { role: "legion-acme-acme-12-reviewer", session_id: "made-up" },
      }),
  },
  // A subagent's session, whose session_init line records its system prompt and tools.
  subagent: {
    store: "store_a",
    change: (lines) =>
      insertAfter(lines, branchHead(lines), {
        type: "session_init",
        id: "5e551017",
        timestamp: "2026-11-20T01:59:43.500Z",
        agent: "task",
        resolvedModel: "anthropic/claude-sonnet-5-5",
        task: "Read the alpha steps.",
        tools: SUBAGENT_TOOLS,
        systemPrompt: SUBAGENT_PROMPT,
      }),
  },
  "bad-init": {
    store: "store_a",
    change: (lines) =>
      insertAfter(lines, branchHead(lines), {
        type: "session_init",
        id: "5e551018",
        timestamp: "2026-11-20T01:59:43.500Z",
        agent: "task",
        tools: SUBAGENT_TOOLS,
      }),
  },
  // FILLERS messages between the skill's read and the step, so the step is in the third window.
  "third-window": {
    store: "store_a",
    change: (lines) => addFillers(lines, FILLERS, (n) => `Still working (${n}).`),
  },
  // The user's prompt states the line, and the step comes late: the case reader, reading the
  // window of the step, does not see the prompt; the restatement check's first window does.
  "late-restated": {
    store: "store_a",
    change: (lines) => {
      setText(lines, 0, (text) => `${text} ${RESTATED}`);
      addFillers(lines, FILLERS, (n) => `Late filler ${n}: ${"x".repeat(4_000)}`);
    },
  },
  // Messages before the step that need more windows of the restatement check than it reads.
  "too-long": {
    store: "store_a",
    change: (lines) => addFillers(lines, 12, (n) => `Long filler ${n}: ${"y".repeat(100_000)}`),
  },
  // The restatement check's reader answers nothing it can read.
  "restate-fails": { store: "store_a" },
  // The audit finds the real step breaks the line, or that the line does not govern it.
  "audit-breaks": { store: "store_a" },
  "audit-not-governed": { store: "store_a" },
  broken: {
    store: "store_a",
    change: (lines) => lines.splice(6, 0, { notJson: true }),
  },
};

/** A compaction entry that keeps the entries from `kept` on. */
function compaction(kept: unknown): Record<string, unknown> {
  return {
    type: "compaction",
    id: "c0ffee01",
    timestamp: "2026-11-20T01:59:45.000Z",
    summary: "The user asked for the alpha steps of the made-up project.",
    firstKeptEntryId: kept,
    tokensBefore: 1200,
  };
}

/** The made-up path of session `name`. */
const sessionPath = (name: string) => SESSIONS[name]?.path ?? `/sessions/${name}.jsonl`;

/** The made-up session case `c` comes from. */
const nameOf = (c: Case) =>
  Object.keys(SESSIONS).find((name) => pathHash(sessionPath(name)) === c.source.pathHash) ?? "";

/** F1's `thinking_level_change` line, after which an added line opens the session's branch. */
function branchHead(lines: Record<string, unknown>[]): Record<string, unknown> {
  const head = lines.find((line) => line.type === "thinking_level_change");
  if (head === undefined) throw new Error("F1 has no thinking_level_change line");
  return head;
}

/** The daemon's catch-up notice to the architect of a child issue, as an Envoy message line. */
function catchUpNotice(): Record<string, unknown> {
  return {
    type: "custom_message",
    customType: "envoy-message",
    id: "e0e0e001",
    timestamp: "2026-11-20T01:59:43.600Z",
    display: true,
    content: [
      "envoy:",
      "  from: agent",
      '  at: "2026-11-20T01:59:43Z"',
      "  summary: catch-up on ACME-12",
      "  message:",
      "    kind: catch-up",
      "    role: architect",
      '    reason: "the issues under ACME-12, which you own, at generation 1 as the daemon records them now; start or resume your issue as your role says"',
      "",
    ].join("\n"),
  };
}

/** The subagent session's recorded system prompt and tools. */
const SUBAGENT_PROMPT = [
  "You are a made-up subagent.\n<skills>\n- alpha: Use for the alpha steps.\n- beta: Use for the beta greeting.\n</skills>\n",
  "<project-context>\nA made-up project.\n</project-context>",
];
const SUBAGENT_TOOLS = ["read", "bash", "task"];
/** The messages the third-window session adds between the skill's read and its step. */
const FILLERS = 90;

/** Session `name`'s transcript text. */
function sessionText(name: string): string {
  const lines = f1Lines();
  setText(lines, 5, () => `Step 3 (case ${name}).`);
  SESSIONS[name]?.change?.(lines);
  return lines
    .map((line) => (line.notJson === true ? "{not json" : JSON.stringify(line)))
    .map((line) => `${line}\n`)
    .join("");
}

/** The tasks rows of the stand-in store: a first user message, the day its file is of. */
const TASK_ROWS = [
  {
    dayStart: mtimeRange("2026-11-20").from,
    path: "/sessions/task-1.jsonl",
    text: "Give me the alpha checklist for the fixture project.",
  },
  {
    dayStart: mtimeRange("2026-11-20").from,
    path: "/sessions/task-2.jsonl",
    text: "Fix the beta greeting.",
  },
  {
    dayStart: mtimeRange("2026-11-21").from,
    path: "/sessions/task-3.jsonl",
    text: "Write the Alpha report of step 2.",
  },
  {
    dayStart: mtimeRange("2026-11-21").from,
    path: "/sessions/task-4.jsonl",
    text: "Give me the alpha checklist for the fixture project.",
  },
];

/** The stand-in store command: it answers pull.sql, sessions.sql and tasks.sql from STORE. */
const STANDIN_PSQL = (storeFile: string, logFile: string) => `
import { appendFileSync, readFileSync } from "node:fs";
import { createHash } from "node:crypto";
const db = process.argv[2] ?? "";
const query = await Bun.stdin.text();
const store = JSON.parse(readFileSync(${JSON.stringify(storeFile)}, "utf8"))[db];
if (store === undefined) {
  process.stderr.write("psql: error: database " + db + " does not exist\\n");
  process.exit(2);
}
const literals = (text) => [...text.matchAll(/'((?:[^']|'')*)'/g)].map((m) => m[1].replaceAll("''", "'"));
let kind;
let rows;
if (query.includes("omp_session_files_parts pt") && /p\\.o > (-?\\d+)/.test(query)) {
  kind = "pull";
  const filePath = literals(/WHERE f\\.path = ('(?:[^']|'')*')/.exec(query)[1])[0];
  const after = Number(/WHERE p\\.o > (-?\\d+)/.exec(query)[1]);
  const text = store.files[filePath];
  const pieces = [];
  if (text !== undefined) {
    pieces.push({ o: -1, c: "" });
    let offset = 0;
    // Parts of PART_CHARS, and of more for a long file, so it is never more than 100 parts.
    const size = Math.max(${PART_CHARS}, Math.ceil(text.length / 100));
    for (let at = 0; at < text.length; at += size) {
      const c = text.slice(at, at + size);
      pieces.push({ o: offset, c });
      offset += Buffer.byteLength(c, "utf8");
    }
  }
  rows = pieces.filter((piece) => piece.o > after).slice(0, 64);
} else if (query.includes("left(md5(f.path), 16) = ANY")) {
  kind = "sessions";
  const hashes = new Set(literals(query.slice(query.indexOf("ANY"))));
  rows = Object.keys(store.files)
    .map((p) => ({ path: p, hash: createHash("md5").update(p).digest("hex").slice(0, 16) }))
    .filter((row) => hashes.has(row.hash));
} else if (query.includes("first user message")) {
  kind = "tasks";
  const dayStart = Number(/f\\.mtime_ms >= (\\d+)/.exec(query)[1]);
  // The terms of the ARRAY after unnest(, less the '' the query compares each term to.
  const terms = literals(query.slice(query.indexOf("unnest("))).filter((t) => t !== "");
  rows = store.tasks
    .filter((row) => row.dayStart === dayStart && terms.some((t) => row.text.toLowerCase().includes(t.toLowerCase())))
    .map((row) => ({ path: row.path, text: row.text }));
} else {
  process.stderr.write("psql: error: not a query of the runner\\n");
  process.exit(3);
}
appendFileSync(${JSON.stringify(logFile)}, JSON.stringify({ db, kind, rows: rows.length, limit64: !query.includes("omp_session_files_parts pt") || query.includes("LIMIT 64") }) + "\\n");
process.stdout.write(" value \\n-------\\n" + rows.map((row) => " " + JSON.stringify(row) + "\\n").join("") + "(" + rows.length + (rows.length === 1 ? " row" : " rows") + ")\\n\\n");
`;

/** The words the `restated` session's prompt adds, which state LINE in other words. */
const RESTATED = "Put the result of each step on a single line.";

/** The criterion the stand-in case reader writes. */
const CRITERION = {
  pass: "The step writes each step's result on one line.",
  fail: "The step spreads a result over more than one line.",
};

/** The proposal of the stand-in case reader for the session named in the transcript: the step is
 *  the message of the window that calls the `task` tool (F1's message 7), and a window that holds
 *  none gives no step. */
function caseAnswer(name: string, transcript: string): unknown {
  const criterion = CRITERION;
  const step = transcript
    .split('<message index="')
    .find((message) => message.includes("[tool call: task]"))
    ?.split('"')[0];
  if (name === "no-step" || step === undefined)
    return { step: null, reason: "No step of the transcript is decided by the skill." };
  const text = name === "not-in-base" ? "2. Write each step's result on two lines." : LINE;
  return {
    step: { messageIndex: Number(step), what: "calls the task tool" },
    line: { file: "SKILL.md", text },
    criterion,
    violatingStep: { text: "Step 4.", toolCalls: [VIOLATING_CALL] },
    // Message 0, the user's prompt, comes before the skill's read: the reader sees it only in
    // the messages before the read.
    restatements: name === "restated" ? [{ messageIndex: 0, quote: RESTATED }] : [],
  };
}

/** The tool call of the stand-in reader's violating step. */
const VIOLATING_CALL = { name: "read", arguments: { path: "skill://beta" } };

/** A written task of the stand-in tasks reader. */
const written = (label: string, n: number) => ({
  label,
  from: "written",
  text: `A made-up ${label} task ${n}.`,
  why: "made up",
});

/** The first words of the restate and audit prompts, by which the stand-in tells their requests. */
const RESTATE_PROMPT = "You read part of a coding agent's session and find each message";
const AUDIT_PROMPT = "You decide whether one step of a coding agent's session follows";

/** The stand-in restatement check: each message of the window that holds RESTATED, quoted; and,
 *  for the restate-fails session, no JSON at all. */
function restateAnswer(name: string, window: string): string {
  if (name === "restate-fails") return "I could not read the window.";
  const restatements = window
    .split('<message index="')
    .slice(1)
    .filter((message) => message.includes(RESTATED))
    .map((message) => ({ messageIndex: Number(message.split('"')[0]), quote: RESTATED }));
  return JSON.stringify({ restatements });
}

/** The stand-in audit: the step follows the line, but in the audit-<verdict> sessions. */
function auditAnswer(name: string): unknown {
  if (name === "audit-breaks") {
    return { verdict: "breaks", reason: "The step spreads the result over two lines." };
  }
  if (name === "audit-not-governed") {
    return { verdict: "not-governed", reason: "The step writes no result." };
  }
  return { verdict: "follows", reason: "The step writes the result on one line." };
}

/** The stand-in reader's answer to `body`. */
function readerAnswer(body: {
  system: { text: string }[];
  messages: { content: { text: string }[] }[];
}): string {
  const system = body.system[0]?.text ?? "";
  const user = body.messages[0]?.content.map((part) => part.text).join("\n") ?? "";
  const name = /\(case ([a-z-]+)\)/.exec(user)?.[1] ?? "";
  if (system.startsWith(RESTATE_PROMPT)) return restateAnswer(name, user);
  if (system.startsWith(AUDIT_PROMPT)) return JSON.stringify(auditAnswer(name));
  if (system.startsWith("You read part of a coding agent's session")) {
    return JSON.stringify(caseAnswer(name, user));
  }
  if (system.startsWith("You pick the tasks of a load test")) {
    const fives = (label: string) => [1, 2, 3, 4, 5].map((n) => written(label, n));
    if (user.includes("None.")) {
      return JSON.stringify({ terms: ["alpha"], tasks: [...fives("need"), ...fives("near-miss")] });
    }
    return JSON.stringify({
      terms: ["alpha"],
      tasks: [
        { label: "need", from: "session", messageId: "m1", why: "asks for the checklist" },
        {
          label: "near-miss",
          from: "session",
          messageId: "m2",
          why: "a report, not the checklist",
        },
        ...[1, 2, 3, 4].map((n) => written("need", n)),
        ...[1, 2, 3, 4].map((n) => written("near-miss", n)),
      ],
    });
  }
  return "{}";
}

let recorder: Recorder;

/** A made-up skill's SKILL.md. */
const skillMd = (name: string) =>
  `---\nname: ${name}\ndescription: Use when a task asks for ${name}.\n---\n\n# ${name}\n`;

/** The made-up repositories as each was at the commit SHA: every file's path and text. acme/skills
 *  holds `older` and no `newer`; its installer links each skill of skills/ into Oh My Pi's user
 *  skills, and its prepack script stages alpha and beta for the package @acme/pi-acme. */
const REPOSITORIES: Record<string, Record<string, string>> = {
  "acme/skills": {
    "skills/alpha/SKILL.md": skillMd("alpha"),
    "skills/alpha/references/x.md": "Step notes.\n",
    "skills/beta/SKILL.md": skillMd("beta"),
    "skills/older/SKILL.md": skillMd("older"),
    "installers/skills.sh": [
      "#!/usr/bin/env bash",
      'for skill in "$REPO"/skills/*/; do',
      '  ln -sfn "$skill" "$HOME/.omp/agent/skills/$(basename "$skill")"',
      "done",
      "",
    ].join("\n"),
    "packages/pi-acme/package.json": JSON.stringify({
      name: "@acme/pi-acme",
      omp: { skills: ["dist/skills"] },
    }),
    "scripts/pi-plugin-prepack.sh": [
      "#!/usr/bin/env bash",
      "plugin() {",
      '  case "$1" in',
      "  @acme/pi-acme)",
      "    skills=( alpha beta )",
      "    ;;",
      "  esac",
      "}",
      'cp -R "$root/skills/$skill" "$dest/$skill"',
      "stage_skills dist/skills",
      "",
    ].join("\n"),
    "docker/omp-plugins/package.json": JSON.stringify({
      name: "omp-plugins",
      dependencies: { "@acme/codegraph": "1.0.0" },
    }),
  },
};

/** The blob id the stand-in gives a file's text. */
const blobId = (text: string) => createHash("sha1").update(text).digest("hex");
/** The one file a batch of blobs gives cut short, so it is read alone. */
const CUT_SHORT = REPOSITORIES["acme/skills"]?.["docker/omp-plugins/package.json"] ?? "";

/** The gh calls the stand-in took. */
const ghCalls: string[][] = [];

/** The stand-in gh: GET reads of commits, default branches, two files, and the tree and the
 *  blobs of each made-up repository at SHA; and GraphQL reads of blobs in a batch. */
const io = (lines: string[]): BuildIo => ({
  out: (text) => lines.push(text),
  err: (line) => lines.push(line),
  gh: async (args) => {
    ghCalls.push([...args]);
    const route = args.find((arg) => arg.startsWith("repos/")) ?? "";
    const ok = (stdout: string) => ({ code: 0, stdout, stderr: "" });
    if (args[1] === "graphql") {
      const field = (name: string) =>
        args.find((arg) => arg.startsWith(`${name}=`))?.slice(name.length + 1) ?? "";
      const texts = Object.values(REPOSITORIES[`${field("owner")}/${field("name")}`] ?? {});
      const blobs = [...field("query").matchAll(/(b\d+): object\(oid: "([0-9a-f]{40})"\)/g)].map(
        ([, alias, id]) => {
          const text = texts.find((each) => blobId(each) === id);
          if (text === undefined) return [alias, null];
          const cut = text === CUT_SHORT;
          return [alias, { isBinary: false, isTruncated: cut, text: cut ? null : text }];
        }
      );
      return ok(JSON.stringify({ data: { repository: Object.fromEntries(blobs) } }));
    }
    if (/^repos\/[^/]+\/[^/]+$/.test(route)) return ok("main\n");
    if (route.endsWith("/commits")) return ok(`${SHA}\n`);
    if (route === "repos/acme/widgets/contents/AGENTS.md") return ok("# Widgets\n");
    if (route === "repos/acme/infra/contents/legion/projects/10-ACME.yaml") {
      return ok("key: ACME\ninstructions: acme.md\n");
    }
    const [, repository = "", kind = "", id = ""] =
      /^repos\/([^/]+\/[^/]+)\/git\/(trees|blobs)\/([0-9a-f]{40})(?:\?recursive=1)?$/.exec(route) ??
      [];
    const files = Object.entries(REPOSITORIES[repository] ?? {});
    if (kind === "trees" && id === SHA && files.length > 0) {
      return ok(
        JSON.stringify({
          sha: SHA,
          truncated: false,
          tree: files.map(([file, text]) => ({
            path: file,
            mode: "100644",
            type: "blob",
            sha: blobId(text),
            size: text.length,
          })),
        })
      );
    }
    const blob = files.find(([, text]) => blobId(text) === id);
    if (kind === "blobs" && blob !== undefined) {
      return ok(
        JSON.stringify({ encoding: "base64", content: Buffer.from(blob[1]).toString("base64") })
      );
    }
    return { code: 1, stdout: "", stderr: "gh: Not Found (HTTP 404)\n" };
  },
});

const setupDir = path.join(scratch, "setup");
const config = path.join(setupDir, "skill-cuts.json");
const keyFile = path.join(setupDir, "key");
const old = path.join(setupDir, "old", "alpha");
const copiesDir = path.join(setupDir, "copies");
const countDir = path.join(setupDir, "count");
const psqlLog = path.join(setupDir, "psql.log");

beforeAll(() => {
  recorder = startRecorder({
    route: (request: RecordedRequest) => {
      if (request.method !== "POST" || request.path !== "/v1/messages") return undefined;
      const body = JSON.parse(request.body);
      return sseResponse(
        messageSse({
          id: `msg_standin_${request.n}`,
          model: body.model,
          content: [{ type: "text", text: readerAnswer(body) }],
          stopReason: "end_turn",
          inputTokens: 100,
          outputTokens: 20,
        }),
        `req_standin_${request.n}`
      );
    },
  });
  mkdirSync(path.join(old, "references"), { recursive: true });
  const messages = f1Messages();
  writeFileSync(
    path.join(old, "SKILL.md"),
    `${stripSkillHeader(messages[2]?.content?.[0]?.text ?? "")}\n`
  );
  writeFileSync(
    path.join(old, "references", "x.md"),
    `${stripSkillHeader(messages[4]?.content?.[0]?.text ?? "")}\n`
  );

  const files: Record<string, Record<string, string>> = { store_a: {}, store_b: {}, store_c: {} };
  for (const [name, session] of Object.entries(SESSIONS)) {
    const store = files[session.store] ?? {};
    store[sessionPath(name)] = sessionText(name);
  }
  writeFileSync(
    path.join(setupDir, "store.json"),
    JSON.stringify({
      store_a: { files: files.store_a, tasks: TASK_ROWS },
      store_b: { files: files.store_b, tasks: [] },
      store_c: { files: files.store_c, tasks: [] },
    })
  );
  writeFileSync(
    path.join(setupDir, "psql.ts"),
    STANDIN_PSQL(path.join(setupDir, "store.json"), psqlLog)
  );
  const psql = path.join(setupDir, "psql");
  writeFileSync(
    psql,
    `#!/bin/sh\nexec '${process.execPath}' '${path.join(setupDir, "psql.ts")}' "$@"\n`
  );
  chmodSync(psql, 0o755);

  const fixture = JSON.parse(readFileSync(path.join(TESTDATA, "F7-skill-cuts.json"), "utf8"));
  writeFileSync(
    config,
    JSON.stringify({
      ...fixture,
      stores: [
        ...fixture.stores,
        { name: "store_b", context: "legion:ACME" },
        { name: "store_c", context: "devbox" },
      ],
      servedModels: [...fixture.servedModels, "anthropic/claude-opus-5-5"],
      psqlCommand: psql,
      gatewayBaseUrl: recorder.url,
    })
  );
  writeFileSync(keyFile, fixtureSecret("gw"), { mode: 0o600 });

  mkdirSync(copiesDir, { recursive: true });
  const userSkills = "user-skills:omp (acme/skills:installers/skills.sh)";
  const otherSkills = "user-skills:omp (acme/other:installers/skills.sh)";
  const pkg = "package:@acme/pi-acme (acme/skills:packages/pi-acme/package.json)";
  writeFileSync(
    path.join(copiesDir, "prefix-table.tsv"),
    tsv(PREFIX_TABLE_COLUMNS, [
      ["**/agent/skills/alpha/", "acme/skills", "skills/alpha", "alpha", userSkills],
      ["**/agent/skills/beta/", "acme/skills", "skills/beta", "beta", userSkills],
      ["**/agent/skills/newer/", "acme/skills", "skills/newer", "newer", userSkills],
      ["/elsewhere/other-skills/alpha/", "acme/other", "skills/alpha", "alpha", otherSkills],
      ["/opt/legion/pi-acme/dist/skills/alpha/", "acme/skills", "skills/alpha", "alpha", pkg],
      ["/opt/legion/pi-acme/dist/skills/beta/", "acme/skills", "skills/beta", "beta", pkg],
      ["/opt/legion/pi-acme/dist/skills/newer/", "acme/skills", "skills/newer", "newer", pkg],
    ])
  );
  const installer = (
    repo: string,
    manifest: string,
    kind: string,
    action: string,
    unit: string,
    skill: string,
    copy: string,
    note = ""
  ) => [repo, manifest, kind, action, unit, "omp", skill, skill === "" ? "" : repo, copy, note];
  writeFileSync(
    path.join(copiesDir, "installers.tsv"),
    tsv(INSTALLERS_COLUMNS, [
      installer(
        "acme/skills",
        "installers/skills.sh",
        "installer-script",
        "links",
        "user-skills:omp",
        "alpha",
        "skills/alpha"
      ),
      installer(
        "acme/skills",
        "installers/skills.sh",
        "installer-script",
        "links",
        "user-skills:omp",
        "beta",
        "skills/beta"
      ),
      installer(
        "acme/skills",
        "installers/skills.sh",
        "installer-script",
        "links",
        "user-skills:omp",
        "newer",
        "skills/newer"
      ),
      installer(
        "acme/skills",
        "packages/pi-acme/package.json",
        "omp-package",
        "ships",
        "package:@acme/pi-acme",
        "newer",
        "skills/newer"
      ),
      installer(
        "acme/other",
        "installers/skills.sh",
        "installer-script",
        "links",
        "user-skills:omp",
        "alpha",
        "skills/alpha"
      ),
      installer(
        "acme/skills",
        "packages/pi-acme/package.json",
        "omp-package",
        "ships",
        "package:@acme/pi-acme",
        "alpha",
        "skills/alpha"
      ),
      installer(
        "acme/skills",
        "packages/pi-acme/package.json",
        "omp-package",
        "ships",
        "package:@acme/pi-acme",
        "beta",
        "skills/beta"
      ),
      installer(
        "acme/skills",
        "docker/omp-plugins/package.json",
        "omp-plugins",
        "installs",
        "package:@acme/codegraph",
        "",
        "",
        "no package @acme/codegraph with skills was read"
      ),
    ])
  );
  writeFileSync(
    path.join(copiesDir, "manifests.tsv"),
    tsv(MANIFESTS_COLUMNS, [
      [
        "acme/infra",
        "legion/projects/10-ACME.yaml",
        "legion-project",
        "tree",
        0,
        "project ACME: verificationSkillsLeaveOut none",
        "",
      ],
    ])
  );
  writeFileSync(
    path.join(copiesDir, "repositories.tsv"),
    tsv(REPOSITORIES_COLUMNS, [
      ["acme/skills", "main", SHA, "no", "no", "", 10, "recursive", 3, ""],
      ["acme/other", "main", SHA, "no", "no", "", 10, "recursive", 1, ""],
      ["forker/skills", "main", SHA, "yes", "no", "acme/skills", 10, "recursive", 1, ""],
    ])
  );
  writeFileSync(
    path.join(copiesDir, "skill-copies.tsv"),
    tsv(SKILL_COPIES_COLUMNS, [
      ["acme/other", "skills/alpha", "alpha", 10, "no", "no", "", "", ""],
      ["acme/skills", "skills/alpha", "alpha", 10, "no", "no", "", "", ""],
      ["acme/skills", "skills/beta", "beta", 10, "no", "no", "", "", ""],
      ["acme/skills", "skills/newer", "newer", 10, "no", "no", "", "", ""],
      ["forker/skills", "skills/alpha", "alpha", 10, "yes", "no", "acme/skills", "", ""],
    ])
  );
  writeFileSync(path.join(copiesDir, "loaders.tsv"), tsv(LOADERS_COLUMNS, []));

  // The count: each store's reads of alpha, by the copy path the count gives them. store_a's come
  // from acme/skills's user skills, store_b's from the package the image carries, store_c's from
  // acme/other's user skills; so the context of store_a and store_b installs acme/skills's alpha,
  // and that of store_c acme/other's.
  for (const [store, copy] of [
    ["store_a", `${F1_SKILLS}/alpha/SKILL.md`],
    ["store_b", "/opt/legion/pi-acme/dist/skills/alpha/SKILL.md"],
    ["store_c", "/elsewhere/other-skills/alpha/SKILL.md"],
  ] as const) {
    const rows = [
      {
        row: "count",
        store,
        day: "2026-11-20",
        skill: "alpha",
        copy,
        kind: "read",
        files: 1,
        hashes: ["0123456789abcdef"],
        unpaired: 0,
        unpaired_hashes: [],
      },
      {
        row: "summary",
        store,
        mtime_from: 0,
        mtime_to: 0,
        files: 1,
        parts: 1,
        bytes: 1,
        lines_parsed: 1,
        bad_lines: 0,
        shared_call_ids: 0,
      },
    ];
    mkdirSync(path.join(countDir, store), { recursive: true });
    writeFileSync(
      path.join(countDir, store, "2026-11-20.txt"),
      [
        " total",
        "-".repeat(40),
        ...rows.map((row) => ` ${JSON.stringify(row)}`),
        `(${rows.length} rows)`,
        "",
      ].join("\n")
    );
  }
});
afterAll(async () => {
  await recorder.close();
});

/** A sessions file of `names` (each with its path but `withoutPath`'s) and `extra` lines. */
function sessionsFile(
  file: string,
  names: readonly string[],
  withoutPath: readonly string[] = [],
  extra: unknown[] = []
) {
  const lines = names.map((name) => {
    const store = SESSIONS[name]?.store ?? "store_a";
    const hash = fileHash(sessionPath(name));
    return withoutPath.includes(name) ? { store, hash } : { store, hash, path: sessionPath(name) };
  });
  writeFileSync(
    path.join(setupDir, file),
    [...lines, ...extra].map((line) => `${JSON.stringify(line)}\n`).join("")
  );
  return path.join(setupDir, file);
}

/** The arguments of one build-cases run. */
function buildArgs(sessions: string, out: string, seed: string, ...extra: string[]): string[] {
  return [
    "build-cases",
    "--config",
    config,
    "--key-file",
    keyFile,
    "--cut",
    "skills-1",
    "--skill",
    "alpha",
    "--old",
    old,
    "--sessions",
    sessions,
    "--seed",
    seed,
    "--copies",
    copiesDir,
    "--count",
    countDir,
    "--out",
    out,
    ...extra,
  ];
}

interface Summary {
  seed: string;
  order: { store: string; hash: string }[];
  sessionsTried: number;
  casesBuilt: number;
  drops: Partial<Record<DropReason, number>>;
  sessions: {
    hash: string;
    outcome: string;
    reason?: DropReason;
    detail?: string;
    pulls?: number;
    context?: string;
    model?: string;
    readerUsd: number;
    windows?: number;
    restatementCheck?: { read: number; before: number; windows: number };
    audit?: { verdict: string; reason: string };
  }[];
  readerSpend: { usd: number; models: Record<string, { calls: number }> };
  byModel: Record<string, { tried: number; drops: Partial<Record<DropReason, number>> }>;
}

const summaryOf = (out: string): Summary => JSON.parse(readFileSync(`${out}.summary.json`, "utf8"));

describe("build-cases over every made-up session", () => {
  const out = path.join(scratch, "all", "cases.jsonl");
  const printed: string[] = [];
  let code = -1;
  let cases: Case[] = [];
  const byName = (name: string) =>
    cases.find((each) => each.source.pathHash === pathHash(sessionPath(name)));

  /** The requests the stand-in gateway took for this build. */
  let requests: RecordedRequest[] = [];
  beforeAll(async () => {
    const names = [...Object.keys(SESSIONS), "missing"];
    const sessions = sessionsFile("all.jsonl", names, ["good", "missing"]);
    const from = recorder.requests.length;
    code = await main(buildArgs(sessions, out, "7"), io(printed));
    requests = recorder.requests.slice(from);
    cases = loadCases(out, old);
  }, 120_000);

  test("exits 0 and writes cases that loadCases loads", () => {
    expect(printed.join("")).not.toContain("failed");
    expect(code).toBe(0);
    expect(cases.map((each) => each.source.pathHash).sort()).toEqual(
      [
        "good",
        "compacted-before-read",
        "secret",
        "legion",
        "root-architect",
        "child-architect",
        "claimed-reviewer",
        "subagent",
        "third-window",
        "openai",
        "gateway-model",
        "skill-md-only",
        "checkout",
        "context",
      ]
        .map((name) => pathHash(sessionPath(name)))
        .sort()
    );
  });

  test("a case of F1 holds the messages before the step, the step, the skill's reads, the model and thinking", () => {
    const c = byName("good");
    const messages = f1Messages();
    expect(c?.messages).toHaveLength(7);
    expect(c?.messages[0]).toEqual(messages[0] as SessionMessage);
    expect(c?.messages[2]).toEqual(messages[2] as SessionMessage);
    expect(c?.realStep).toEqual(messages[7] as SessionMessage);
    // The reader's violating step, as an assistant message: its text, then its tool call.
    expect(c?.violatingStep).toEqual({
      role: "assistant",
      content: [
        { type: "text", text: "Step 4." },
        { type: "toolCall", id: "violating_1", ...VIOLATING_CALL },
      ],
    });
    expect(c?.skillReads).toEqual([2, 4]);
    expect(c?.model).toBe("anthropic/claude-sonnet-5-5");
    expect(c?.thinking).toBe("off");
    expect(c?.line).toEqual({ text: LINE, lineNumber: 9, lineCount: 10 });
    expect(c?.baseFile).toBe("SKILL.md");
    expect(c?.context).toBe("devbox");
    expect(c?.sessionStart).toBe("2026-11-20T01:59:41.780Z");
    expect(c?.sessionDigest).toBe("46874d907e557998");
    expect(c?.source).toEqual({ store: "store_a", pathHash: pathHash(sessionPath("good")) });
    expect(c?.secretsRemoved).toBe(0);
    expect(c?.id).toBe(`skills-1-${pathHash(sessionPath("good")).slice(0, 12)}`);
    // No path text in the case beyond the transcript's own messages.
    expect(JSON.stringify({ ...c, messages: [], realStep: {} })).not.toContain(sessionPath("good"));
  });

  test("the case reader reads the messages before the skill's read too, and a restatement there drops the case", () => {
    const summary = summaryOf(out);
    const record = (name: string) =>
      summary.sessions.find((each) => each.hash === fileHash(sessionPath(name)));
    expect(record("restated")).toMatchObject({
      reason: "history-restates-line",
      detail:
        "the case reader finds the line's instruction stated in message 0 of the session, before the step at message 7",
    });
    // Its own finding drops the case before the restatement check runs.
    expect(record("restated")?.restatementCheck).toBeUndefined();
    // The reader's request held message 0 with its index.
    const asked = recorder.requests.find((request) => request.body.includes("(case restated)"));
    expect(asked?.body).toContain('<message index=\\"0\\" role=\\"user\\">');
    expect(byName("restated")).toBeUndefined();
  });

  test("the restatement check reads every message the case keeps before the step, less the skill's reads, and a restatement in its first window drops a late step's case", () => {
    const summary = summaryOf(out);
    const record = (name: string) =>
      summary.sessions.find((each) => each.hash === fileHash(sessionPath(name)));
    // F1's 7 messages before the step, less the skill's two read results, in one window.
    expect(record("good")?.restatementCheck).toEqual({ read: 5, before: 5, windows: 1 });
    expect(record("third-window")?.restatementCheck).toEqual({
      read: 5 + FILLERS,
      before: 5 + FILLERS,
      windows: 1,
    });
    // The late step's case reader window held no message 0; the check's first window did.
    const late = record("late-restated");
    expect(late).toMatchObject({ reason: "history-restates-line" });
    expect(late?.detail).toStartWith(
      "the restatement check finds the line's instruction stated in message 0 of the case, before the step (window 1 of "
    );
    expect(late?.detail).toEndWith(`: 0: ${JSON.stringify(RESTATED)}`);
    expect(late?.restatementCheck?.windows).toBeGreaterThan(1);
    expect(late?.restatementCheck?.read).toBeLessThan(late?.restatementCheck?.before ?? 0);
    const restating = requests.filter(
      (request) => request.body.includes(RESTATE_PROMPT) && request.body.includes("Late filler")
    );
    expect(restating).toHaveLength(1);
    // Every kept case's check read every message it must.
    for (const c of cases) {
      const check = summary.sessions.find(
        (each) => each.hash === fileHash(sessionPath(nameOf(c)))
      )?.restatementCheck;
      expect(check?.read).toBe(check?.before as number);
    }
  });

  test("a check that cannot read every message before the step drops the case, whether its messages need too many windows or a window's reader fails", () => {
    const summary = summaryOf(out);
    const record = (name: string) =>
      summary.sessions.find((each) => each.hash === fileHash(sessionPath(name)));
    const tooLong = record("too-long");
    expect(tooLong).toMatchObject({ reason: "restatement-check-incomplete" });
    expect(tooLong?.detail).toContain(`more than ${RESTATE_WINDOWS}`);
    expect(tooLong?.restatementCheck?.windows).toBeGreaterThan(RESTATE_WINDOWS);
    expect(tooLong?.restatementCheck?.read).toBe(0);
    // No restatement call was made for it: its windows were counted first.
    expect(
      requests.filter(
        (request) => request.body.includes(RESTATE_PROMPT) && request.body.includes("Long filler")
      )
    ).toHaveLength(0);
    const fails = record("restate-fails");
    expect(fails).toMatchObject({ reason: "restatement-check-incomplete" });
    expect(fails?.detail).toStartWith("window 1 of 1 of the restatement check was not read");
    expect(fails?.restatementCheck).toEqual({ read: 0, before: 5, windows: 1 });
    expect(
      requests.filter(
        (request) =>
          request.body.includes(RESTATE_PROMPT) && request.body.includes("(case restate-fails)")
      )
    ).toHaveLength(READER_TRIES);
  });

  test("the audit reads the line, the messages before the step and the real step, and no criterion; only a step that follows the line is kept", () => {
    const summary = summaryOf(out);
    const record = (name: string) =>
      summary.sessions.find((each) => each.hash === fileHash(sessionPath(name)));
    expect(record("audit-breaks")).toMatchObject({
      reason: "audit-breaks",
      detail: "The step spreads the result over two lines.",
      audit: { verdict: "breaks", reason: "The step spreads the result over two lines." },
    });
    expect(record("audit-not-governed")).toMatchObject({ reason: "audit-not-governed" });
    const evidence = byName("good")?.evidence?.find((each) => each.item.startsWith("audit "));
    expect(evidence).toEqual({
      item: "audit of the real step: follows",
      source:
        "the audit reader, given the line, the messages before the step as the judge reads them and the real step, and no criterion: The step writes the result on one line.",
    });
    const audits = requests.filter((request) => request.body.includes(AUDIT_PROMPT));
    // One audit for each kept case and each case the audit dropped.
    expect(audits).toHaveLength(cases.length + 2);
    for (const request of audits) {
      expect(request.body).toContain(LINE);
      expect(request.body).not.toContain(CRITERION.pass);
      expect(request.body).not.toContain(CRITERION.fail);
    }
  });

  test("the profile is what the read's unit shipped at the commit of the session's start, not what the later copies output lists, each skill with its evidence", () => {
    const c = byName("good");
    expect(c?.profile).toEqual({
      skills: [
        { repo: "acme/skills", commit: SHA, path: "skills/alpha", name: "alpha" },
        { repo: "acme/skills", commit: SHA, path: "skills/beta", name: "beta" },
        { repo: "acme/skills", commit: SHA, path: "skills/older", name: "older" },
      ],
      plugins: [],
      contextFiles: [],
    });
    const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
    expect(evidence["unit user-skills:omp (acme/skills:installers/skills.sh)"]).toContain(
      `message 2 reads [Skill file: ${F1_SKILLS}/alpha/SKILL.md]`
    );
    expect(evidence[`commit ${SHA} of acme/skills`]).toContain("at or before the session's start");
    expect(evidence[`the skills and manifests of acme/skills at ${SHA}`]).toContain(
      "its 3 SKILL.md files and 4 manifest files"
    );
    expect(evidence["skill alpha (acme/skills skills/alpha)"]).toBe(
      `acme/skills at ${SHA}: user-skills:omp links it (acme/skills:installers/skills.sh)`
    );
    // The copies output lists `newer`, which acme/skills did not hold at the commit, and not
    // `older`, which it held.
    expect(evidence["left out: skill newer (acme/skills skills/newer)"]).toContain(
      "user-skills:omp does not ship it at the session's start"
    );
    expect(evidence["skill older (acme/skills skills/older)"]).toContain(
      "installers.tsv, which R4 read later, does not list it"
    );
    // A package ships what its prepack script lists at the commit.
    expect(byName("legion")?.profile.skills.map((skill) => skill.name)).toEqual(["alpha", "beta"]);
    // Every skill of every profile is in its repository at the profile's commit.
    for (const each of cases) {
      for (const skill of each.profile.skills) {
        expect(skill.commit).toBe(SHA);
        expect(REPOSITORIES[skill.repo]?.[`${skill.path}/SKILL.md`]).toBeDefined();
      }
    }
    const commitCall = ghCalls.find((args) => args.includes("repos/acme/skills/commits"));
    expect(commitCall).toContain("until=2026-11-20T01:59:41.780Z");
    expect(ghCalls.every((args) => args[0] === "api" && !args.includes("POST"))).toBe(true);
    // A repository's files are read in one batch; the one the batch gives cut short is read alone.
    expect(ghCalls.some((args) => args[1] === "graphql")).toBe(true);
    const alone = ghCalls.flatMap((args) => args.filter((arg) => arg.includes("/git/blobs/")));
    expect([...new Set(alone)]).toEqual([`repos/acme/skills/git/blobs/${blobId(CUT_SHORT)}`]);
  });

  test("a read is placed as attribute.ts places it: a read in a checkout of the cut's repository and one the store's context puts at its copy are kept; a checkout of a fork and a read the store's context puts at another copy drop, with the rule named", () => {
    const record = (name: string) =>
      summaryOf(out).sessions.find((each) => each.hash === fileHash(sessionPath(name)));
    const unit = (name: string) =>
      byName(name)?.evidence?.find(
        (each) => each.item === "unit user-skills:omp (acme/skills:installers/skills.sh)"
      )?.source;
    const checkout = (repository: string) =>
      `checkout: to ${repository} skills/alpha: the path ends with skills/alpha/, the skill directory of 3 repositories, and the path names ${repository}`;
    const context = (repository: string) =>
      `context: to ${repository} skills/alpha, the one copy of alpha its context installs`;
    expect(unit("checkout")).toContain(
      `message 2 reads [Skill file: /home/dev/acme/skills/skills/alpha/SKILL.md], which attribute.ts's checkout rule places at acme/skills skills/alpha (${checkout("acme/skills")})`
    );
    expect(unit("context")).toContain(
      `message 2 reads [Skill file: /mnt/loaded/alpha/SKILL.md], which attribute.ts's context rule places at acme/skills skills/alpha (${context("acme/skills")})`
    );
    // The profile of a read no prefix row maps is what the units that ship its copy shipped.
    for (const name of ["checkout", "context"]) {
      expect(byName(name)?.profile.skills.map((skill) => skill.name)).toEqual([
        "alpha",
        "beta",
        "older",
      ]);
    }
    expect(record("checkout-fork")).toMatchObject({
      reason: "not-cut-copy",
      detail: `the checkout rule places /home/dev/forker/skills/skills/alpha/SKILL.md at forker/skills skills/alpha (${checkout("forker/skills")})`,
    });
    expect(record("context-other")).toMatchObject({
      reason: "not-cut-copy",
      detail: `the context rule places /mnt/loaded/alpha/SKILL.md at acme/other skills/alpha (${context("acme/other")})`,
    });
    expect(record("other-copy")).toMatchObject({
      reason: "not-cut-copy",
      detail:
        "the prefix table rule places /elsewhere/other-skills/alpha/SKILL.md at acme/other skills/alpha",
    });
    for (const name of ["checkout-fork", "context-other", "other-copy"]) {
      expect(record(name)?.windows).toBeUndefined();
    }
  });

  test("--reprofile builds each profile again from the case alone: the new cases differ from the old ones only in profile and evidence, and no session is read and no reader called", async () => {
    // The case file as a builder that took the copies output's list wrote it: each profile holds
    // `newer`, which acme/skills did not hold at the commit, and not `older`.
    const stale = cases.map((c) => ({
      ...c,
      profile: {
        ...c.profile,
        skills: [
          ...c.profile.skills.filter((skill) => skill.name !== "older"),
          { repo: "acme/skills", commit: SHA, path: "skills/newer", name: "newer" },
        ],
      },
      evidence: (c.evidence ?? []).map((each) =>
        each.item === "skill older (acme/skills skills/older)"
          ? { item: "skill newer (acme/skills skills/newer)", source: "installers.tsv lists it" }
          : each
      ),
    }));
    const staleFile = path.join(scratch, "all", "stale.jsonl");
    writeFileSync(staleFile, stale.map((c) => `${JSON.stringify(c)}\n`).join(""));
    const fresh = path.join(scratch, "all", "reprofiled.jsonl");
    const args = (from: string, to: string) => [
      "build-cases",
      "--config",
      config,
      "--old",
      old,
      "--copies",
      copiesDir,
      "--count",
      countDir,
      "--reprofile",
      from,
      "--out",
      to,
    ];
    const lines: string[] = [];
    const requestsBefore = recorder.requests.length;
    const logBefore = statSync(psqlLog).size;
    expect(await main(args(staleFile, fresh), io(lines))).toBe(0);
    expect(lines.join("")).not.toContain("left out");
    expect(recorder.requests.length).toBe(requestsBefore);
    expect(statSync(psqlLog).size).toBe(logBefore);

    const rebuilt = loadCases(fresh, old);
    expect(rebuilt.map((c) => c.id)).toEqual(cases.map((c) => c.id));
    const rest = (c: Case) => ({ ...c, profile: undefined, evidence: undefined });
    for (const [index, c] of rebuilt.entries()) {
      const built = cases[index] as Case;
      // Its messages, line, steps and every other field are the old file's.
      expect(rest(c)).toEqual(rest(stale[index] as Case));
      // Its profile is the one the builder gives the session: no `newer`.
      expect(c.profile).toEqual(built.profile);
      expect(c.evidence?.map((each) => each.item)).toEqual(
        built.evidence?.map((each) => each.item)
      );
      // The working directory is not in a case, so only a workspace's AGENTS.md is said otherwise.
      const differing = (c.evidence ?? []).filter(
        (each, at) => each.source !== built.evidence?.[at]?.source
      );
      expect(differing.map((each) => each.item)).toEqual(
        c.profile.contextFiles.some((file) => file.path === "AGENTS.md")
          ? ["context file acme/widgets AGENTS.md"]
          : []
      );
    }
    const summary = JSON.parse(readFileSync(`${fresh}.summary.json`, "utf8")) as {
      casesRead: number;
      casesWritten: number;
      leftOut: unknown[];
      cases: { id: string; added: string[]; removed: string[] }[];
    };
    expect(summary).toMatchObject({
      casesRead: cases.length,
      casesWritten: cases.length,
      leftOut: [],
    });
    expect(summary.cases.every((each) => each.removed.join() === "newer")).toBe(true);
    expect(summary.cases.find((each) => each.id === byName("good")?.id)?.added).toEqual(["older"]);

    // It takes no session and no reader, and writes a new file.
    expect(await main([...args(staleFile, fresh), "--sessions", staleFile], io(lines))).toBe(2);
    expect(lines.join("\n")).toContain("so it takes no --sessions");
    expect(await main(args(staleFile, staleFile), io(lines))).toBe(2);
    expect(lines.join("\n")).toContain("is the file --reprofile reads");
  }, 60_000);

  test("a Legion session's role comes from its first user message, and its profile adds the image's plugin and the context files", () => {
    const c = byName("legion");
    expect(c?.context).toBe("legion:ACME:tester");
    expect(c?.profile.plugins).toEqual([
      { repo: "acme/skills", commit: SHA, path: "packages/pi-acme", name: "pi-acme" },
    ]);
    expect(c?.profile.contextFiles).toEqual([
      { repo: "acme/widgets", commit: SHA, path: "AGENTS.md" },
      { repo: "acme/infra", commit: SHA, path: "legion/instructions/acme.md" },
    ]);
    const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
    expect(evidence["role tester"]).toContain("first user message");
    expect(evidence["left out: the plugin package:@acme/codegraph"]).toContain("installs it");
  });

  test("a root architect's role comes from the daemon's start, a child's architect's from its catch-up notice, and a reviewer's from its claim beside its workspace; a session of no form is dropped with its first words", () => {
    const evidence = (name: string) =>
      Object.fromEntries(byName(name)?.evidence?.map((each) => [each.item, each.source]) ?? []);
    expect(byName("root-architect")?.context).toBe("legion:ACME:architect-root");
    expect(evidence("root-architect")["role architect-root"]).toContain('"ACME-12 is yours and …"');
    expect(byName("child-architect")?.context).toBe("legion:ACME:architect");
    expect(evidence("child-architect")["role architect"]).toContain(
      '"the issues under ACME-12, which you own …"'
    );
    // The catch-up notice is the first message the child's architect read.
    expect(byName("child-architect")?.messages[0]?.role).toBe("custom");
    expect(byName("claimed-reviewer")?.context).toBe("legion:ACME:reviewer");
    expect(evidence("claimed-reviewer")["role reviewer"]).toContain("legion-acme-acme-12-reviewer");
    const noRole = summaryOf(out).sessions.find(
      (each) => each.hash === fileHash(sessionPath("no-role"))
    );
    const words = (f1Messages()[0]?.content?.[0]?.text ?? "").replace(/\s+/g, " ").slice(0, 80);
    expect(words).toHaveLength(80);
    expect(noRole).toMatchObject({
      reason: "no-role",
      detail: `no form names its role; its first user message starts: ${JSON.stringify(words)}`,
    });
  });

  test("a subagent's case carries its recorded system prompt and tools, and no context file", () => {
    const c = byName("subagent");
    expect(c?.context).toBe("subagent:task");
    expect(c?.profile.systemPrompt).toEqual(SUBAGENT_PROMPT);
    expect(c?.profile.tools).toEqual(SUBAGENT_TOOLS);
    expect(c?.profile.contextFiles).toEqual([]);
    expect(c?.profile.skills.map((skill) => skill.name)).toEqual(["alpha", "beta", "older"]);
    const items = c?.evidence?.map((each) => each.item) ?? [];
    expect(items).toContain("subagent task");
    expect(items).toContain("the recorded system prompt and tools");
    expect(items).toContain("no context files");
    // The session_init line is no message: the case's messages are F1's first seven.
    const messages = f1Messages();
    expect(c?.messages.map((message) => message.role)).toEqual(
      messages.slice(0, 7).map((message) => message.role)
    );
    expect(c?.messages[2]).toEqual(messages[2] as SessionMessage);
  });

  test("a step in the third window is found, and the restatement check reads every message before it", () => {
    const c = byName("third-window");
    const messages = f1Messages();
    expect(c?.realStep).toEqual(messages[7] as SessionMessage);
    expect(c?.messages).toHaveLength(7 + FILLERS);
    const record = summaryOf(out).sessions.find(
      (each) => each.hash === fileHash(sessionPath("third-window"))
    );
    // Windows of 40 from the read's call (message 1): 1-40, 41-80, then 81 on.
    expect(record?.windows).toBe(3);
    expect(record?.restatementCheck).toEqual({
      read: 5 + FILLERS,
      before: 5 + FILLERS,
      windows: 1,
    });
    // One case reader call a window, each holding some of the session's added messages.
    expect(
      requests.filter(
        (request) =>
          request.body.includes("propose one case") && request.body.includes("Still working (")
      )
    ).toHaveLength(3);
  });

  test("a compaction before the skill's read leaves the summary, the entries it kept, and those after it", () => {
    const c = byName("compacted-before-read");
    const messages = f1Messages();
    expect(c?.messages.map((message) => message.role)).toEqual([
      "compactionSummary",
      "user",
      "assistant",
      "toolResult",
      "assistant",
      "toolResult",
      "assistant",
      "toolResult",
    ]);
    expect(c?.messages[0]?.summary).toBe(
      "The user asked for the alpha steps of the made-up project."
    );
    expect(c?.messages[1]).toEqual(messages[0] as SessionMessage);
    expect(c?.skillReads).toEqual([3, 5]);
  });

  test("a secret in a message is taken out of the case and counted", () => {
    const c = byName("secret");
    const text = readFileSync(out, "utf8");
    expect(text).not.toContain(SECRET_TOKEN);
    expect(text).not.toContain(PEM_BODY);
    expect(c?.secretsRemoved).toBe(2);
    expect(c?.messages[0]?.content?.[0]?.text).toContain(`Bearer ${SECRET_REMOVED}`);
  });

  test("the case reader is offered only the base text's lines the session's own reads hold, each with its number in the file, and no file the session did not read", () => {
    /** The `<skill>` part of the case reader's first request for session `name`. */
    const offered = (name: string) => {
      const request = requests.find(
        (each) => each.body.includes("propose one case") && each.body.includes(`(case ${name})`)
      );
      const body = JSON.parse(request?.body ?? "{}") as {
        messages?: { content?: { text?: string }[] }[];
      };
      const text = (body.messages?.[0]?.content ?? []).map((part) => part.text ?? "").join("\n");
      return /<skill name="alpha">[\s\S]*?<\/skill>/.exec(text)?.[0] ?? "";
    };
    const base = {
      "SKILL.md": readFileSync(path.join(old, "SKILL.md"), "utf8").split("\n"),
      "references/x.md": readFileSync(path.join(old, "references", "x.md"), "utf8").split("\n"),
    };
    /** Each `<number>: <line>` of a `<skill>` part's files, by file. */
    const shownLines = (skill: string) =>
      [...skill.matchAll(/<file path="([^"]+)">\n([\s\S]*?)\n<\/file>/g)].map((match) => ({
        file: match[1] ?? "",
        lines: (match[2] ?? "").split("\n").map((line) => {
          const [, number, text] = /^(\d+): (.*)$/.exec(line) ?? [];
          return { number: Number(number), text };
        }),
      }));
    const lineNumber = base["SKILL.md"].indexOf(LINE) + 1;

    const good = offered("good");
    expect(good).toContain(`\n${lineNumber}: ${LINE}\n`);
    expect(shownLines(good).map((each) => each.file)).toEqual(["SKILL.md", "references/x.md"]);
    // Every line shown is the base file's line at its number.
    for (const name of ["good", "read-lacks-line", "skill-md-only"]) {
      for (const { file, lines } of shownLines(offered(name))) {
        for (const { number, text } of lines) {
          expect(base[file as keyof typeof base][number - 1]).toBe(text);
        }
      }
    }
    // The read lacks the line, so it is not offered; the reader's proposal of it still drops.
    const lacking = offered("read-lacks-line");
    expect(lacking).not.toBe("");
    expect(lacking).not.toContain(LINE);
    expect(shownLines(offered("skill-md-only")).map((each) => each.file)).toEqual(["SKILL.md"]);
  });

  test("a session whose reads hold no line of the base text is dropped as no-common-line, with no reader call", () => {
    const record = summaryOf(out).sessions.find(
      (each) => each.hash === fileHash(sessionPath("no-common"))
    );
    expect(record).toMatchObject({
      reason: "no-common-line",
      detail:
        "the session's successful reads of SKILL.md, references/x.md hold no whole line of the base text",
      readerUsd: 0,
    });
    expect(record?.windows).toBeUndefined();
    expect(requests.some((each) => each.body.includes("(case no-common)"))).toBe(false);
  });

  test("a session whose model id the gateway serves is kept on its own model, whatever its prefix, and one on a model no route serves is dropped, before any reader when none of its models is served", () => {
    expect(byName("openai")?.model).toBe("openai/gpt-6.1-sol");
    expect(byName("gateway-model")?.model).toBe("ossv-gateway/claude-opus-5-5");
    const summary = summaryOf(out);
    const record = (name: string) =>
      summary.sessions.find((each) => each.hash === fileHash(sessionPath(name)));
    expect(record("not-served")).toMatchObject({
      reason: "model-not-served",
      model: "openai-codex/gpt-6-astra",
      detail:
        "openai-codex/gpt-6-astra (id gpt-6-astra): no route of the gateway serves it; no servedModels entry has its id",
    });
    expect(record("not-served")?.windows).toBeUndefined();
    expect(record("not-served")?.readerUsd).toBe(0);
    // Served at the read, so the reader reads it; the step's model is not served.
    expect(record("switched-model")).toMatchObject({
      reason: "model-not-served",
      model: "openai-codex/gpt-6-astra",
      windows: 1,
    });
    expect(summary.byModel["openai-codex/gpt-6-astra"]).toEqual({
      tried: 2,
      drops: { "model-not-served": 2 },
    });
    expect(summary.byModel["openai/gpt-6.1-sol"]).toEqual({ tried: 1, drops: {} });
    expect(summary.byModel["ossv-gateway/claude-opus-5-5"]).toEqual({ tried: 1, drops: {} });
    // Each session tried and each drop is under one model.
    const rows = Object.values(summary.byModel);
    expect(rows.reduce((total, row) => total + row.tried, 0)).toBe(summary.sessionsTried);
    for (const [reason, count] of Object.entries(summary.drops)) {
      const counted = rows.reduce(
        (total, row) => total + (row.drops[reason as DropReason] ?? 0),
        0
      );
      expect(counted).toBe(count);
    }
    expect(summary.byModel.none?.tried).toBeGreaterThan(0);
  });

  test("each drop is counted by its reason", () => {
    const summary = summaryOf(out);
    expect(summary.drops).toEqual({
      "line-not-in-base": 1,
      "line-not-in-read": 1,
      "read-before-compaction": 1,
      "line-quoted": 1,
      "history-restates-line": 2,
      "restatement-check-incomplete": 2,
      "audit-breaks": 1,
      "audit-not-governed": 1,
      "model-not-served": 2,
      "read-by-path": 1,
      "no-read": 1,
      "no-step": 1,
      "no-common-line": 1,
      "not-cut-copy": 3,
      "no-role": 1,
      "bad-session-init": 1,
      unparseable: 1,
      "not-in-store": 1,
    });
    expect(Object.keys(summary.drops).every((reason) => reason in DROP_REASONS)).toBe(true);
    expect(summary.sessionsTried).toBe(Object.keys(SESSIONS).length + 1);
    expect(summary.casesBuilt).toBe(14);
    const reasonOf = (name: string) =>
      summary.sessions.find((each) => each.hash === fileHash(sessionPath(name)))?.reason;
    expect(reasonOf("quoted")).toBe("line-quoted");
    expect(reasonOf("restated")).toBe("history-restates-line");
    expect(reasonOf("late-restated")).toBe("history-restates-line");
    expect(reasonOf("too-long")).toBe("restatement-check-incomplete");
    expect(reasonOf("restate-fails")).toBe("restatement-check-incomplete");
    expect(reasonOf("by-path")).toBe("read-by-path");
    expect(reasonOf("other-copy")).toBe("not-cut-copy");
    expect(reasonOf("compacted")).toBe("read-before-compaction");
    expect(reasonOf("bad-init")).toBe("bad-session-init");
    expect(reasonOf("not-served")).toBe("model-not-served");
    expect(reasonOf("switched-model")).toBe("model-not-served");
    expect(reasonOf("read-lacks-line")).toBe("line-not-in-read");
    expect(reasonOf("no-common")).toBe("no-common-line");
    // Every reader call is counted: one case reader call a window of each session the reader
    // reached, one restatement call a window of each case checked (READER_TRIES for the window
    // whose answer was refused, none for the case with too many windows, up to the window of a
    // restatement), and one audit of each case checked whole.
    expect(summary.readerSpend.models["claude-opus-5-5"]?.calls).toBe(requests.length);
    const count = (prompt: string) =>
      requests.filter((request) => request.body.includes(prompt)).length;
    const sum = (values: number[]) => values.reduce((total, each) => total + each, 0);
    expect(count("propose one case")).toBe(sum(summary.sessions.map((each) => each.windows ?? 0)));
    const restateCalls = summary.sessions.map((each) => {
      if (each.restatementCheck === undefined) return 0;
      if (each.hash === fileHash(sessionPath("restate-fails"))) return READER_TRIES;
      if (each.hash === fileHash(sessionPath("too-long"))) return 0;
      if (each.hash === fileHash(sessionPath("late-restated"))) return 1;
      return each.restatementCheck.windows;
    });
    expect(count(RESTATE_PROMPT)).toBe(sum(restateCalls));
    expect(count(AUDIT_PROMPT)).toBe(summary.sessions.filter((each) => each.audit).length);
    expect(summary.readerSpend.usd).toBeGreaterThan(0);
  });

  test("a pull pages at 64 parts a call, and a hash without a path is resolved with sessions.sql", () => {
    const log = readFileSync(psqlLog, "utf8")
      .split("\n")
      .filter((line) => line !== "")
      .map((line) => JSON.parse(line) as { kind: string; rows: number; limit64: boolean });
    expect(log.every((each) => each.limit64)).toBe(true);
    expect(log.filter((each) => each.kind === "sessions")).toHaveLength(1);
    expect(log.some((each) => each.kind === "pull" && each.rows === 64)).toBe(true);
    const good = summaryOf(out).sessions.find(
      (each) => each.hash === fileHash(sessionPath("good"))
    );
    expect(good?.pulls).toBeGreaterThan(1);
  });
});

test("the seed fixes the order the sessions are taken in, and --max stops at that many cases", async () => {
  const names = ["good", "secret", "legion", "no-step"];
  const sessions = sessionsFile("seeded.jsonl", names);
  const runs = [];
  for (const [run, seed] of [
    ["a", "11"],
    ["b", "11"],
    ["c", "12"],
  ] as const) {
    const out = path.join(scratch, `seeded-${run}`, "cases.jsonl");
    expect(await main(buildArgs(sessions, out, seed, "--max", "1"), io([]))).toBe(0);
    runs.push({ summary: summaryOf(out), cases: loadCases(out, old) });
  }
  const candidates = names.map((name) => ({
    store: SESSIONS[name]?.store ?? "",
    hash: fileHash(sessionPath(name)),
  }));
  const expected = (seed: string) => blindOrder(candidates, seed).map(({ item }) => item);
  expect(runs[0]?.summary.order).toEqual(expected("11"));
  expect(runs[1]?.summary.order).toEqual(runs[0]?.summary.order ?? []);
  expect(runs[2]?.summary.order).toEqual(expected("12"));
  for (const run of runs) {
    expect(run.cases).toHaveLength(1);
    const first = run.summary.sessions.find((each) => each.outcome.startsWith("case "));
    expect(run.cases[0]?.source.pathHash).toBe(
      pathHash(sessionPath(names.find((name) => fileHash(sessionPath(name)) === first?.hash) ?? ""))
    );
  }
}, 60_000);

test("build-cases refuses an unknown cut and a sessions line whose path is not its hash, before any read", async () => {
  const from = recorder.requests.length;
  const logLength = () => (existsSync(psqlLog) ? readFileSync(psqlLog, "utf8").length : 0);
  const logged = logLength();
  const lines: string[] = [];
  const out = path.join(scratch, "refused", "cases.jsonl");
  const sessions = sessionsFile("good.jsonl", ["good"]);
  const unknownCut = buildArgs(sessions, out, "1").map((arg) =>
    arg === "skills-1" ? "skills-9" : arg
  );
  expect(await main(unknownCut, io(lines))).toBe(2);
  expect(lines.join("\n")).toContain("--cut skills-9 is no cut of the configuration");
  const noCount = buildArgs(sessions, out, "1").filter(
    (arg, at, args) => arg !== "--count" && args[at - 1] !== "--count"
  );
  expect(await main(noCount, io(lines))).toBe(2);
  expect(lines.join("\n")).toContain("--count required");
  const emptyCount = path.join(scratch, "refused", "count");
  mkdirSync(emptyCount, { recursive: true });
  const noOutputs = buildArgs(sessions, out, "1").map((arg, at, args) =>
    args[at - 1] === "--count" ? emptyCount : arg
  );
  expect(await main(noOutputs, io(lines))).toBe(2);
  expect(lines.join("\n")).toContain("holds no query output of a store of the configuration");
  const wrongHash = sessionsFile(
    "wrong.jsonl",
    [],
    [],
    [{ store: "store_a", hash: "0000000000000000", path: sessionPath("good") }]
  );
  expect(await main(buildArgs(wrongHash, out, "1"), io(lines))).toBe(2);
  expect(lines.join("\n")).toContain(
    `its path's hash is ${fileHash(sessionPath("good"))}, not 0000000000000000`
  );
  expect(recorder.requests.length).toBe(from);
  expect(logLength()).toBe(logged);
});

test("build-tasks proposes terms, reads first user messages per store and day, and writes labelled tasks", async () => {
  const skillDir = path.join(setupDir, "old", "alpha");
  const out = path.join(scratch, "tasks", "tasks.jsonl");
  const lines: string[] = [];
  const code = await main(
    [
      "build-tasks",
      "--config",
      config,
      "--key-file",
      keyFile,
      "--skill",
      skillDir,
      "--store",
      "store_a",
      "--out",
      out,
    ],
    io(lines)
  );
  expect(lines.join("")).toContain("build-tasks: 10 tasks (2 from sessions)");
  expect(code).toBe(0);
  const tasks = readFileSync(out, "utf8")
    .split("\n")
    .filter((line) => line !== "")
    .map((line) => JSON.parse(line));
  expect(tasks[0]).toEqual({
    id: "t1",
    text: "Give me the alpha checklist for the fixture project.",
    label: "need",
    source: "session",
    store: "store_a",
    pathHash: pathHash("/sessions/task-1.jsonl"),
  });
  expect(tasks[1]).toEqual({
    id: "t2",
    text: "Write the Alpha report of step 2.",
    label: "near-miss",
    source: "session",
    store: "store_a",
    pathHash: pathHash("/sessions/task-3.jsonl"),
  });
  expect(tasks[2]).toEqual({
    id: "t3",
    text: "A made-up need task 1.",
    label: "need",
    source: "written",
  });
  expect(tasks.filter((task) => task.label === "need")).toHaveLength(5);
  expect(tasks.filter((task) => task.label === "near-miss")).toHaveLength(5);
  const summary = JSON.parse(readFileSync(`${out}.summary.json`, "utf8"));
  expect(summary.terms).toEqual(["alpha"]);
  expect(summary.queries).toEqual([
    { store: "store_a", day: "2026-11-20", rows: 1 },
    { store: "store_a", day: "2026-11-21", rows: 2 },
  ]);
  // The same first message on two sessions is offered once.
  expect(summary.messagesFound).toBe(3);
  expect(summary.messagesOffered).toBe(2);
}, 30_000);

test("removeSecrets takes out each secret shape and counts it, and leaves plain text alone", () => {
  const token = fixtureSecret("tok");
  const cases: [string, string, number][] = [
    // A request header as `curl -v` prints it in a tool result: no command line gives it.
    [
      `> Authorization: Bearer ${token}\n> Accept: */*`,
      `> Authorization: Bearer ${SECRET_REMOVED}\n> Accept: */*`,
      1,
    ],
    [PEM, SECRET_REMOVED, 1],
    [
      `postgres://reader:${token}@db.example/x`,
      `postgres://reader:${SECRET_REMOVED}@db.example/x`,
      1,
    ],
    [`export ACME_API_KEY=${token}9`, `export ACME_API_KEY=${SECRET_REMOVED}`, 1],
    [`"password": "${token}7"`, `"password": "${SECRET_REMOVED}"`, 1],
    ["max_tokens: 16000 and token=short1", "max_tokens: 16000 and token=short1", 0],
    ["const token = createToken(session);", "const token = createToken(session);", 0],
  ];
  for (const [text, expected, removed] of cases) {
    expect(removeSecrets(text)).toEqual({ text: expected, removed });
  }
});
