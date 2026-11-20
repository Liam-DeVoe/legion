import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
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
  removeSecrets,
  SECRET_REMOVED,
} from "./build";
import { type Case, loadCases, pathHash } from "./cases";
import {
  INSTALLERS_COLUMNS,
  MANIFESTS_COLUMNS,
  PREFIX_TABLE_COLUMNS,
  REPOSITORIES_COLUMNS,
  tsv,
} from "./copies";
import { mtimeRange } from "./countsql";
import { blindOrder } from "./judge";
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
// The gateway is a stand-in built on recorder.ts; GitHub is a stand-in gh. Every skill,
// repository, store and path here is made up, and nothing reaches a network.

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

const SECRET_TOKEN = fixtureSecret("tok");
const PEM_BODY = fixtureSecret("pem");
const PEM = [
  `-----BEGIN RSA PRIVATE ${"KEY"}-----`,
  PEM_BODY,
  `-----END RSA PRIVATE ${"KEY"}-----`,
].join("\n");

/** Each made-up session: its store, and how it changes F1. */
const SESSIONS: Record<
  string,
  { store: string; change?: (lines: Record<string, unknown>[]) => void }
> = {
  good: { store: "store_a" },
  "not-in-base": { store: "store_a" },
  "read-lacks-line": {
    store: "store_a",
    change: (lines) => setText(lines, 2, (text) => text.replace(`${LINE}\n`, "")),
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
  openai: {
    store: "store_a",
    change: (lines) => {
      const change = lines.find((line) => line.type === "model_change");
      if (change !== undefined) change.model = "openai/gpt-6.1-sol";
    },
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
const sessionPath = (name: string) => `/sessions/${name}.jsonl`;

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
    for (let at = 0; at < text.length; at += ${PART_CHARS}) {
      const c = text.slice(at, at + ${PART_CHARS});
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

/** The proposal of the stand-in case reader for the session named in the transcript. */
function caseAnswer(name: string): unknown {
  const criterion = {
    pass: "The step writes each step's result on one line.",
    fail: "The step spreads a result over more than one line.",
  };
  if (name === "no-step")
    return { step: null, reason: "No step of the transcript is decided by the skill." };
  const text = name === "not-in-base" ? "2. Write each step's result on two lines." : LINE;
  return {
    step: { messageIndex: 7, what: "calls the task tool" },
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

/** The stand-in reader's answer to `body`. */
function readerAnswer(body: {
  system: { text: string }[];
  messages: { content: { text: string }[] }[];
}): string {
  const system = body.system[0]?.text ?? "";
  const user = body.messages[0]?.content.map((part) => part.text).join("\n") ?? "";
  if (system.startsWith("You read part of a coding agent's session")) {
    return JSON.stringify(caseAnswer(/\(case ([a-z-]+)\)/.exec(user)?.[1] ?? ""));
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
/** The gh calls the stand-in took. */
const ghCalls: string[][] = [];

/** The stand-in gh: GET reads of commits, default branches and two files. */
const io = (lines: string[]): BuildIo => ({
  out: (text) => lines.push(text),
  err: (line) => lines.push(line),
  gh: async (args) => {
    ghCalls.push([...args]);
    const route = args.find((arg) => arg.startsWith("repos/")) ?? "";
    const ok = (stdout: string) => ({ code: 0, stdout, stderr: "" });
    if (/^repos\/[^/]+\/[^/]+$/.test(route)) return ok("main\n");
    if (route.endsWith("/commits")) return ok(`${SHA}\n`);
    if (route === "repos/acme/widgets/contents/AGENTS.md") return ok("# Widgets\n");
    if (route === "repos/acme/infra/contents/legion/projects/10-ACME.yaml") {
      return ok("key: ACME\ninstructions: acme.md\n");
    }
    return { code: 1, stdout: "", stderr: "gh: Not Found (HTTP 404)\n" };
  },
});

const setupDir = path.join(scratch, "setup");
const config = path.join(setupDir, "skill-cuts.json");
const keyFile = path.join(setupDir, "key");
const old = path.join(setupDir, "old", "alpha");
const copiesDir = path.join(setupDir, "copies");
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

  const files: Record<string, Record<string, string>> = { store_a: {}, store_b: {} };
  for (const [name, session] of Object.entries(SESSIONS)) {
    const store = files[session.store] ?? {};
    store[sessionPath(name)] = sessionText(name);
  }
  writeFileSync(
    path.join(setupDir, "store.json"),
    JSON.stringify({
      store_a: { files: files.store_a, tasks: TASK_ROWS },
      store_b: { files: files.store_b, tasks: [] },
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
      stores: [...fixture.stores, { name: "store_b", context: "legion:ACME" }],
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
      ["/elsewhere/other-skills/alpha/", "acme/other", "skills/alpha", "alpha", otherSkills],
      ["/opt/legion/pi-acme/dist/skills/alpha/", "acme/skills", "skills/alpha", "alpha", pkg],
      ["/opt/legion/pi-acme/dist/skills/beta/", "acme/skills", "skills/beta", "beta", pkg],
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
      ["acme/skills", "main", SHA, "no", "no", "", 10, "recursive", 2, ""],
      ["acme/other", "main", SHA, "no", "no", "", 10, "recursive", 1, ""],
    ])
  );
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
    restatementCheck?: { read: number; before: number };
  }[];
  readerSpend: { usd: number; models: Record<string, { calls: number }> };
}

const summaryOf = (out: string): Summary => JSON.parse(readFileSync(`${out}.summary.json`, "utf8"));

describe("build-cases over every made-up session", () => {
  const out = path.join(scratch, "all", "cases.jsonl");
  const printed: string[] = [];
  let code = -1;
  let cases: Case[] = [];
  const byName = (name: string) =>
    cases.find((each) => each.source.pathHash === pathHash(sessionPath(name)));

  beforeAll(async () => {
    const names = [...Object.keys(SESSIONS), "missing"];
    const sessions = sessionsFile("all.jsonl", names, ["good", "missing"]);
    code = await main(buildArgs(sessions, out, "7"), io(printed));
    cases = loadCases(out, old);
  }, 60_000);

  test("exits 0 and writes cases that loadCases loads", () => {
    expect(printed.join("")).not.toContain("failed");
    expect(code).toBe(0);
    expect(cases.map((each) => each.source.pathHash).sort()).toEqual(
      ["good", "compacted-before-read", "secret", "legion"]
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
    // Of the 7 messages before the step, the reader read message 0 (before the read at 1) and
    // messages 1 to 6.
    expect(record("good")?.restatementCheck).toEqual({ read: 7, before: 7 });
    expect(record("restated")).toMatchObject({
      reason: "history-restates-line",
      detail:
        "the case reader finds the line's instruction stated in message 0 of the session, before the step at message 7",
      restatementCheck: { read: 7, before: 7 },
    });
    // The reader's request held message 0 with its index.
    const asked = recorder.requests.find((request) => request.body.includes("(case restated)"));
    expect(asked?.body).toContain('<message index=\\"0\\" role=\\"user\\">');
    expect(byName("restated")).toBeUndefined();
  });

  test("the profile is the skills the read's unit installs, at the commit of the session's start, each with its evidence", () => {
    const c = byName("good");
    expect(c?.profile).toEqual({
      skills: [
        { repo: "acme/skills", commit: SHA, path: "skills/alpha", name: "alpha" },
        { repo: "acme/skills", commit: SHA, path: "skills/beta", name: "beta" },
      ],
      plugins: [],
      contextFiles: [],
    });
    const items = c?.evidence?.map((each) => each.item) ?? [];
    expect(items).toContain("unit user-skills:omp (acme/skills:installers/skills.sh)");
    expect(items).toContain("skill alpha (acme/skills skills/alpha)");
    expect(items).toContain(`commit ${SHA} of acme/skills`);
    expect(c?.evidence?.find((each) => each.item.startsWith("unit "))?.source).toContain(
      `message 2 reads [Skill file: ${F1_SKILLS}/alpha/SKILL.md]`
    );
    const commitCall = ghCalls.find((args) => args.includes("repos/acme/skills/commits"));
    expect(commitCall).toContain("until=2026-11-20T01:59:41.780Z");
    expect(ghCalls.every((args) => args[0] === "api" && !args.includes("POST"))).toBe(true);
  });

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

  test("each drop is counted by its reason", () => {
    const summary = summaryOf(out);
    expect(summary.drops).toEqual({
      "line-not-in-base": 1,
      "line-not-in-read": 1,
      "read-before-compaction": 1,
      "line-quoted": 1,
      "history-restates-line": 1,
      "model-not-anthropic": 1,
      "read-by-path": 1,
      "no-read": 1,
      "no-step": 1,
      "not-cut-copy": 1,
      "no-role": 1,
      unparseable: 1,
      "not-in-store": 1,
    });
    expect(Object.keys(summary.drops).every((reason) => reason in DROP_REASONS)).toBe(true);
    expect(summary.sessionsTried).toBe(Object.keys(SESSIONS).length + 1);
    expect(summary.casesBuilt).toBe(4);
    const reasonOf = (name: string) =>
      summary.sessions.find((each) => each.hash === fileHash(sessionPath(name)))?.reason;
    expect(reasonOf("quoted")).toBe("line-quoted");
    expect(reasonOf("restated")).toBe("history-restates-line");
    expect(reasonOf("by-path")).toBe("read-by-path");
    expect(reasonOf("other-copy")).toBe("not-cut-copy");
    expect(reasonOf("compacted")).toBe("read-before-compaction");
    // Readers ran only where the session got as far as the reader; their spend is counted.
    expect(summary.readerSpend.models["claude-opus-5-5"]?.calls).toBe(
      Object.keys(SESSIONS).length - ["no-read", "other-copy", "no-role", "broken"].length
    );
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
