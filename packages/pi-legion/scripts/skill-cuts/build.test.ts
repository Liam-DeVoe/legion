import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { SessionEntry, type SessionMessage, stripSkillHeader } from "../skill-scenarios/transcript";
import {
  type BuildIo,
  currentBranch,
  DROP_REASONS,
  type DropReason,
  type Evidence,
  EXIT,
  fileHash,
  GithubReads,
  LISTING_MEASURE,
  ListingMeasure,
  main,
  messageOf,
  pullTranscript,
  RESTATE_WINDOWS,
  readTranscript,
  removeSecrets,
  SECRET_REMOVED,
  SPOOL_CHUNK,
  STOP_AFTER_SESSIONS,
} from "./build";
import { type Case, caseKind, loadCases, pathHash } from "./cases";
import { GhRetries } from "./common";
import {
  type GhResult,
  INSTALLERS_COLUMNS,
  LOADERS_COLUMNS,
  MANIFESTS_COLUMNS,
  PREFIX_TABLE_COLUMNS,
  REPOSITORIES_COLUMNS,
  SKILL_COPIES_COLUMNS,
  tsv,
} from "./copies";
import { mtimeRange } from "./countsql";
import { blindOrder, stepText } from "./judge";
import { loadProfile } from "./load";
import { psqlRows } from "./psql-rows";
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
// a made-up store file, each transcript stored as the store keeps a file it imported and then
// appended to: its first three quarters in the file row, in slices of PART_CHARS characters, and
// the rest in parts of PART_CHARS from the file row's byte length on, so a pull pages.
// The gateway is a stand-in built on recorder.ts; GitHub is a stand-in gh over made-up
// repositories (REPOSITORIES), each as it was at the one commit SHA. The copies output is of a
// later read: it lists a skill `newer` that no repository held at SHA, and not `older`, which
// acme/skills held then. The count is three made-up query outputs, one per store. Every skill,
// repository and store here is made up, every path is made up or F1's (F1_SKILLS, the skills
// directory of F1's scratch profile, testdata/CAPTURE.md), and nothing reaches a network.

const TESTDATA = path.join(import.meta.dir, "testdata");
const F1 = path.join(
  TESTDATA,
  "F1",
  "2026-11-20T01-59-41-780Z_01a1f6ae-51d4-715f-a2a6-59f275bdff80.jsonl"
);
/** The skills directory F1's session read its skills from: its scratch profile's. */
const F1_SKILLS = "/legion/scratch/tmp/acme-1/t1-runs/f1/home/.omp/profiles/fixture/agent/skills";
const PART_CHARS = 200;
const SHA = "0123456789abcdef0123456789abcdef01234567";
/** The commit acme/dots's installer pins acme/pack at (`ensure_vendor`). */
const PACK_PIN = "4444444444444444444444444444444444444444";
/** The commit of the made-up release v1.0.0 that acme/deploy's worker image takes memo from. */
const REL_PIN = "6666666666666666666666666666666666666666";
/** The directory of a made-up root session, whose subagent pinned-subagent is, and the times of
 *  its prompt builds: the last one before the subagent's start is ROOT_BUILD. */
const PINNED_ROOT = "/sessions/2026-11-20T00-00-00-000Z_0a1b2c3d-0000-4000-8000-000000000000";
const ROOT_BUILD = "2026-11-20T01:10:00.000Z";
const ROOT_DIGESTS = ["2026-11-20T00:10:00.000Z", ROOT_BUILD, "2026-11-20T02:30:00.000Z"];
/** acme/powers before power-two came (POWERS_AT): its commit, which GitHub gives at or before a
 *  time before POWERS_AT, and none before the repository was made (POWERS_MADE). */
const POWERS_OLD = "7".repeat(40);
const POWERS_AT = "2026-11-20T01:30:00.000Z";
const POWERS_MADE = "2026-11-20T00:30:00.000Z";
/** The commit of acme/kit's release tag kit-v1.0.0, whose packages/kit ships kit-one alone. */
const KIT_V1 = "8".repeat(40);
/** acme/dots before POWERS_AT: its plugin list pins @acme/kit 1.0.0, and it holds no pool yet. */
const DOTS_OLD = "9".repeat(40);
/** When acme/fresh and acme/newproj got their first commit: after every made-up session's start,
 *  so GitHub gives no commit of either at or before a session's start. */
const FRESH_MADE = "2026-11-20T03:00:00.000Z";
/** The release tags GitHub lists of each made-up repository. */
const TAGS: Readonly<Record<string, readonly string[]>> = {
  "acme/skills": ["pi-acme-v1.0.0"],
  "acme/kit": ["kit-v1.0.0", "kit-v2.0.0"],
};
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

/** A change of F1 that puts a read of `url` (`skill://<name>`) and its successful result, which
 *  starts `[Skill file: <header>]` and then holds `text`, after F1's prompt: the two lines' ids are
 *  `<id>1` and `<id>2`. */
function readFirst(id: string, url: string, header: string, text: string) {
  return (lines: Record<string, unknown>[]) => {
    const prompt = lines.filter((line) => line.type === "message")[0] ?? {};
    const callId = `toolu_${url.slice("skill://".length)}`;
    const call = {
      type: "message",
      id: `${id}1`,
      timestamp: "2026-11-20T01:59:45.000Z",
      message: {
        role: "assistant",
        content: [{ type: "toolCall", id: callId, name: "read", arguments: { path: url } }],
      },
    };
    const result = {
      type: "message",
      id: `${id}2`,
      timestamp: "2026-11-20T01:59:45.100Z",
      message: {
        role: "toolResult",
        toolCallId: callId,
        toolName: "read",
        isError: false,
        content: [{ type: "text", text: `[Skill file: ${header}]\n${text}` }],
      },
    };
    insertAfter(lines, prompt, call);
    insertAfter(lines, call, result);
  };
}

/** Where the renamed-plugin session reads zeta: Oh My Pi's cache of the plugin zeta-tools of the
 *  marketplace old-market (`plugins/cache/plugins/<marketplace>___<plugin>___<version>/`). */
const OLD_MARKET_ZETA =
  "/home/dev/.omp/plugins/cache/plugins/old-market___zeta-tools___0.1.0/skills/zeta/SKILL.md";

/** Where the pinned-units session reads power-one and pack-one: a dev box's Oh My Pi plugins
 *  directory, which holds acme/powers under the dependency's name acme-powers, and the clone of
 *  acme/pack in its dotfiles checkout's vendor/acme-pack. */
const POWERS_ONE =
  "/home/dev/.dotfiles/omp/plugins/node_modules/acme-powers/skills/power-one/SKILL.md";
const PACK_ONE = "/home/dev/.dotfiles/vendor/acme-pack/skills/pack-one/SKILL.md";

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
  // A read the prefix table places at the cut's copy, through a plugin whose manifest acme/skills
  // did not hold at the session's start: its profile ships no alpha.
  unshipped: { store: "store_a", change: readFrom("/opt/acme-tools/skills/alpha") },
  // Before its read of alpha, the session reads acme/other's delta, whose unit (acme/other's
  // user skills) ships acme/other's alpha too and comes first; the session read acme/skills's
  // alpha, which is the copy its host gave it.
  "read-wins": {
    store: "store_a",
    change: readFirst(
      "de17a00",
      "skill://delta",
      "/elsewhere/other-skills/delta/SKILL.md",
      "# delta\n"
    ),
  },
  // Before its read of alpha, the session reads zeta from Oh My Pi's cache of a plugin of
  // acme/market's marketplace under the name the marketplace had at the session's start
  // (old-market); the copies output's prefix table names the plugin under its later name
  // (new-market), whose unit ships nothing at that commit. acme/market holds Claude Code's
  // settings too.
  "renamed-plugin": {
    store: "store_a",
    change: readFirst("2e7a000", "skill://zeta", OLD_MARKET_ZETA, "# zeta\n"),
  },
  // A dev-box session that reads, before alpha, a skill of the package acme/dots's plugin list
  // installs from acme/powers as acme-powers (a `github:` dependency), and a skill of the pool
  // acme/dots's installer clones from acme/pack into vendor/acme-pack.
  "pinned-units": {
    store: "store_a",
    change: (lines) => {
      readFirst("90e0000", "skill://power-one", POWERS_ONE, "# power-one\n")(lines);
      readFirst("9ac0000", "skill://pack-one", PACK_ONE, "# pack-one\n")(lines);
    },
  },
  // pinned-units' reads in a subagent of the root PINNED_ROOT, which built its prompt last at
  // ROOT_BUILD before the subagent's start: acme/powers (a package) is read then, when it had
  // power-one alone (POWERS_OLD), and acme/dots and the pool it clones are read at the subagent's
  // start.
  "pinned-subagent": {
    store: "store_a",
    path: `${PINNED_ROOT}/pinned-subagent.jsonl`,
    change: (lines) => {
      readFirst("90e0000", "skill://power-one", POWERS_ONE, "# power-one\n")(lines);
      readFirst("9ac0000", "skill://pack-one", PACK_ONE, "# pack-one\n")(lines);
      insertAfter(lines, branchHead(lines), {
        type: "session_init",
        id: "5e551019",
        timestamp: "2026-11-20T01:59:43.500Z",
        agent: "task",
        resolvedModel: "anthropic/claude-sonnet-5-5",
        task: "Read the alpha steps.",
        tools: SUBAGENT_TOOLS,
        systemPrompt: SUBAGENT_PROMPT,
      });
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
  // A Legion pane in a workspace of acme/proj, whose own skills are in its `.claude/skills/` and
  // `.omp/skills/`; it reads alpha as the legion session does.
  "proj-legion": {
    store: "store_b",
    change: (lines) => {
      SESSIONS.legion?.change?.(lines);
      const header = lines.find((line) => line.type === "session");
      if (header !== undefined) header.cwd = "/legion/workspaces/acme/proj/K-1/";
    },
  },
  // A dev-box session in a checkout of acme/proj, the one repository of the copies output's list
  // named `proj`.
  "proj-devbox": { store: "store_a", change: workingDirectory("/home/dev/src/proj") },
  // A dev-box session under a directory named `skills`, the name of two repositories of the list.
  "two-repos": { store: "store_a", change: workingDirectory("/home/dev/src/skills/notes") },
  // A dev-box session in a directory below the root of a checkout of acme/proj.
  "proj-subdir": {
    store: "store_a",
    change: workingDirectory("/home/dev/src/proj/agents/agentvm"),
  },
  // A dev-box session in a checkout of acme/dual, whose root holds .claude/CLAUDE.md, AGENTS.md
  // and CLAUDE.md.
  "dual-root": { store: "store_a", change: workingDirectory("/home/dev/src/dual") },
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

/** A change of F1 that sets the working directory its header names. */
function workingDirectory(cwd: string) {
  return (lines: Record<string, unknown>[]) => {
    const header = lines.find((line) => line.type === "session");
    if (header !== undefined) header.cwd = cwd;
  };
}

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

/** The subagent session's transcript with a recorded prompt whose skill listing has no line of
 *  alpha, so a load test of alpha in its context tests nothing. */
const unlistedSubagentText = () =>
  sessionText("subagent").replace("- alpha: Use for the alpha steps.\\n", "");
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

/** The stand-in store command: it answers pull.sql, sessions.sql, tasks.sql, session-head.sql,
 *  session-paths.sql, subagents.sql and digest-times.sql from the store file `storeFile`, and
 *  appends each call to `logFile`. */
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
let after;
const place = /\\(pc\\.o, s\\.k\\) > \\((-?\\d+), (-?\\d+)\\)/.exec(query);
if (query.includes("omp_session_files_parts pt") && place) {
  kind = "pull";
  const filePath = literals(/WHERE f\\.path = ('(?:[^']|'')*')/.exec(query)[1])[0];
  after = [Number(place[1]), Number(place[2])];
  const limit = Number(/LIMIT (\\d+)/.exec(query)[1]);
  const text = store.files[filePath];
  const pieces = [];
  if (text !== undefined) {
    // Stored as a file the store imported and then appended to: the file row holds the first
    // three quarters, the parts the rest from the file row's byte length on. Slices and parts are
    // of PART_CHARS, and of more for a long file, so a file is never more than 100 rows.
    const size = Math.max(${PART_CHARS}, Math.ceil(text.length / 100));
    const row = text.slice(0, Math.floor((text.length * 3) / 4));
    for (let at = 0, k = 0; at < row.length || k === 0; at += size, k += 1) {
      pieces.push({ o: -1, k, c: row.slice(at, at + size) });
    }
    let offset = Buffer.byteLength(row, "utf8");
    for (let at = row.length; at < text.length; at += size) {
      const c = text.slice(at, at + size);
      pieces.push({ o: offset, k: 0, c });
      offset += Buffer.byteLength(c, "utf8");
    }
  }
  rows = pieces
    .filter((piece) => piece.o > after[0] || (piece.o === after[0] && piece.k > after[1]))
    .slice(0, limit);
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
} else if (query.includes("json_build_object('path', h.path")) {
  kind = "paths";
  // The path of each file of a path hash the query names, in path order.
  const hashes = new Set(literals(query.slice(query.indexOf("ANY"))));
  rows = Object.keys(store.files)
    .map((p) => ({ path: p, hash: createHash("sha256").update(p).digest("hex") }))
    .filter((row) => hashes.has(row.hash))
    .sort((a, b) => (a.path < b.path ? -1 : 1));
} else if (query.includes("'subagents', r.subagents")) {
  kind = "subagents";
  // For each session and times the query names, the files one level below its directory: the
  // earliest by header timestamp (then path) whose session_init line holds <skills> and whose
  // start is at or after \`from\` and before \`to\`, or none, and counts.
  const wanted = JSON.parse(literals(query.slice(query.indexOf("jsonb_array_elements(")))[0]);
  const sha = (p) => createHash("sha256").update(p).digest("hex");
  const seen = new Set();
  rows = [];
  for (const { hash, from, to } of wanted) {
    const key = [hash, from, to].join("\\u0000");
    if (seen.has(key)) continue;
    seen.add(key);
    const parent = Object.keys(store.files).find((p) => sha(p) === hash && p.endsWith(".jsonl"));
    if (parent === undefined) continue;
    const dir = parent.slice(0, -".jsonl".length) + "/";
    const children = Object.entries(store.files)
      .filter(([p]) => p.startsWith(dir) && !p.slice(dir.length).includes("/") && p.endsWith(".jsonl"))
      .map(([p, text]) => {
        const lines = text.split("\\n");
        const header = lines.find((line) => line.includes('"type":"session"'));
        const init = lines.find((line) => line.includes('"type":"session_init"') && line.includes("<skills>"));
        return { path: p, start: header === undefined ? null : JSON.parse(header).timestamp ?? null, init };
      });
    if (children.length === 0) continue;
    const recorded = children.filter((child) => child.init !== undefined);
    const at = (child) => Date.parse(child.start ?? "");
    const under = recorded
      .filter((child) => (from === null || at(child) >= Date.parse(from)) && (to === null || at(child) < Date.parse(to)))
      .sort((a, b) => ((a.start ?? "") + "\\u0000" + a.path < (b.start ?? "") + "\\u0000" + b.path ? -1 : 1));
    const first = under[0];
    rows.push({
      parent: hash,
      from,
      to,
      hash: first === undefined ? null : sha(first.path),
      start: first === undefined ? null : first.start,
      init: first === undefined ? null : first.init,
      subagents: children.length,
      recorded: recorded.length,
      under: under.length,
    });
  }
  const order = (row) => [row.parent, row.from ?? "", row.to ?? ""].join("\\u0000");
  rows.sort((a, b) => (order(a) < order(b) ? -1 : 1));
} else if (query.includes("AS digest_times")) {
  kind = "digests";
  // For each session the query names, its root (the topmost directory named as a top-level
  // session's, and .jsonl; or the file itself, below no such directory) and the time and digest
  // of each of the root's digest lines, earliest first.
  const hashes = new Set(literals(query.slice(query.indexOf("unnest("))));
  const sha = (p) => createHash("sha256").update(p).digest("hex");
  rows = [];
  for (const file of Object.keys(store.files).filter((p) => hashes.has(sha(p)))) {
    const dir = /^(.*?\\/[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9-]+Z_[0-9a-f-]+)\\//.exec(file)?.[1];
    const root = dir === undefined ? file : dir + ".jsonl";
    const text = store.files[root];
    const digests = [...(text ?? "").matchAll(/"customType":"system-prompt-digest","data":"([^"]*)","id":"[^"]*","parentId":(?:"[^"]*"|null),"timestamp":"([^"]+)"/g)]
      .map((m) => ({ at: m[2], digest: m[1] }))
      .sort((a, b) => Date.parse(a.at) - Date.parse(b.at) || (a.digest < b.digest ? -1 : 1));
    rows.push({ hash: sha(file), root: sha(root), rootHeld: text !== undefined, digests });
  }
  rows.sort((a, b) => (a.hash < b.hash ? -1 : 1));
} else if (query.includes("encode(sha256(convert_to(f.path")) {
  kind = "heads";
  // Each file of a path hash the query names: its header's cwd and timestamp and its first user
  // turn, from its whole text (a made-up file is shorter than the head session-head.sql reads).
  const hashes = new Set(literals(query.slice(query.indexOf("ANY"))));
  rows = Object.entries(store.files)
    .map(([p, text]) => ({ text, hash: createHash("sha256").update(p).digest("hex") }))
    .filter((file) => hashes.has(file.hash))
    .sort((a, b) => (a.hash < b.hash ? -1 : 1))
    .map(({ text, hash }) => {
      const lines = text.split("\\n").flatMap((line) => {
        try {
          return [JSON.parse(line)];
        } catch {
          return [];
        }
      });
      const header = lines.find((line) => line.type === "session");
      const user = lines.find((line) => line.type === "message" && line.message?.role === "user");
      return {
        hash,
        cwd: header?.cwd ?? null,
        start: header?.timestamp ?? null,
        first_user: user === undefined ? null : {
          timestamp: typeof user.message.timestamp === "number" ? user.message.timestamp : null,
          attribution: typeof user.message.attribution === "string" ? user.message.attribution : null,
        },
        reminders: [],
        bytes: Buffer.byteLength(text, "utf8"),
        bad_lines: 0,
      };
    });
} else {
  process.stderr.write("psql: error: not a query of the runner\\n");
  process.exit(3);
}
appendFileSync(${JSON.stringify(logFile)}, JSON.stringify({ db, kind, rows: rows.length, after, first: rows[0] && [rows[0].o, rows[0].k], limit64: kind !== "pull" || query.includes("LIMIT 64") }) + "\\n");
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

/** A need task's statement that the skill governs its first step. */
const GOVERNS = { firstStep: { governed: true, reason: "the checklist comes first" } };

/** A written task of the stand-in tasks reader. */
const written = (label: string, n: number) => ({
  label,
  from: "written",
  text: `A made-up ${label} task ${n}.`,
  why: "made up",
  ...(label === "need" ? GOVERNS : {}),
});

/** The text of a first request whose first step the stand-in tasks reader says the skill does
 *  not govern. */
const UNGOVERNED = "(open the notes first)";

/** The stand-in tasks reader's answer in its control mode: each message a need task, whose first
 *  step the skill governs unless the message holds UNGOVERNED. */
function controlAnswer(user: string): unknown {
  const tasks = [...user.matchAll(/<message id="(m\d+)">\n([\s\S]*?)\n<\/message>/g)].map(
    ([, id, text]) => ({
      label: "need",
      from: "session",
      messageId: id,
      why: "asks for the checklist",
      firstStep: (text ?? "").includes(UNGOVERNED)
        ? { governed: false, reason: "a file is opened first" }
        : GOVERNS.firstStep,
    })
  );
  return { terms: [], tasks };
}

/** The made-up sessions of the positive control's tasks (build-tasks --first-read), in store_a at
 *  `/sessions/<name>.jsonl`: each is F1 with its first request replaced; c-late's first reply
 *  reads a notes file instead of the skill, and c-same asks what c-one asks. */
const CONTROL_SESSIONS: Record<string, { request: string; firstRead: boolean }> = {
  "c-one": { request: "Give me the alpha checklist for the release.", firstRead: true },
  "c-same": { request: "Give me the alpha checklist for the release.", firstRead: true },
  "c-secret": {
    request: `Run the alpha checklist on the API.\n> Authorization: Bearer ${SECRET_TOKEN}`,
    firstRead: true,
  },
  "c-late": { request: "Write the alpha report.", firstRead: false },
  "c-ungoverned": { request: `Fix the alpha checklist ${UNGOVERNED}.`, firstRead: true },
  "c-two": { request: "Walk the alpha checklist for the fixture project.", firstRead: true },
};

/** The made-up path of control session `name`. */
const controlPath = (name: string) => `/sessions/${name}.jsonl`;

/** Control session `name`'s transcript text. */
function controlText(name: string): string {
  const lines = f1Lines();
  const session = CONTROL_SESSIONS[name];
  setText(lines, 0, () => session?.request ?? "");
  if (session?.firstRead === false) setReadTarget(lines, 1, "/tmp/notes.md");
  return lines.map((line) => `${JSON.stringify(line)}\n`).join("");
}

/** The transcript text of the control session c-other-model, which no row of CONTROL_SESSIONS
 *  holds: F1 with its own first request, run on openai/gpt-6.1-sol, whose first reply reads
 *  alpha. */
function otherModelControlText(): string {
  const lines = f1Lines();
  setText(lines, 0, () => "Run the alpha checklist on the other model.");
  setModel(lines, "openai/gpt-6.1-sol");
  return lines.map((line) => `${JSON.stringify(line)}\n`).join("");
}

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

/** The first words of the written-case prompt, by which the stand-in tells its requests. */
const WRITTEN_PROMPT = "You write tasks for a behaviour test of a coding agent's skill.";
/** A written task whose following step writes each step's result on a line of its own, LINE. */
const WRITTEN_TASK = {
  task: "Write the alpha report of the fixture project.",
  line: { file: "SKILL.md", number: 9, text: LINE },
  criterion: CRITERION,
  followingStep: {
    text: "",
    toolCalls: [{ name: "write", arguments: { path: "report.md", content: "1. done\n2. done" } }],
  },
  violatingStep: {
    text: "",
    toolCalls: [{ name: "write", arguments: { path: "report.md", content: "1. done, 2. done" } }],
  },
};
/** The stand-in written-case reader's tasks: one the build keeps; one whose task states the line
 *  in other words (RESTATED), which the stand-in restatement check finds; and one whose task names
 *  the audit-breaks session, so the stand-in audit says its following step breaks the line. */
const WRITTEN_TASKS = [
  WRITTEN_TASK,
  { ...WRITTEN_TASK, task: `Write the alpha report of the fixture project. ${RESTATED}` },
  { ...WRITTEN_TASK, task: "Write the alpha report of the fixture project (case audit-breaks)." },
];

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
  if (system.startsWith(WRITTEN_PROMPT)) {
    const count = Number(/<count>(\d+)<\/count>/.exec(user)?.[1] ?? "1");
    return JSON.stringify({ tasks: WRITTEN_TASKS.slice(0, count) });
  }
  if (system.startsWith("You read part of a coding agent's session")) {
    return JSON.stringify(caseAnswer(name, user));
  }
  if (system.startsWith("You pick the tasks of a load test")) {
    if (user.startsWith("<control>")) return JSON.stringify(controlAnswer(user));
    const fives = (label: string) => [1, 2, 3, 4, 5].map((n) => written(label, n));
    if (user.includes("None.")) {
      return JSON.stringify({ terms: ["alpha"], tasks: [...fives("need"), ...fives("near-miss")] });
    }
    // m1 is a need task, and m2, when it is offered, a near-miss one; else a fifth written one.
    const nearMiss = user.includes('<message id="m2">')
      ? [
          {
            label: "near-miss",
            from: "session",
            messageId: "m2",
            why: "a report, not the checklist",
          },
        ]
      : [written("near-miss", 5)];
    return JSON.stringify({
      terms: ["alpha"],
      tasks: [
        {
          label: "need",
          from: "session",
          messageId: "m1",
          why: "asks for the checklist",
          ...GOVERNS,
        },
        ...nearMiss,
        ...[1, 2, 3, 4].map((n) => written("need", n)),
        ...[1, 2, 3, 4].map((n) => written("near-miss", n)),
        // A need task whose first step the skill does not govern: the build leaves it out.
        {
          ...written("need", 5),
          firstStep: { governed: false, reason: "the notes are read first" },
        },
      ],
    });
  }
  return "{}";
}

let recorder: Recorder;

/** A made-up skill's SKILL.md. */
const skillMd = (name: string) =>
  `---\nname: ${name}\ndescription: Use when a task asks for ${name}.\n---\n\n# ${name}\n`;
/** A made-up project skill: one of a repository's own `.claude/skills/` or `.omp/skills/`. */
const projectSkillMd = (name: string) =>
  `---\nname: ${name}\ndescription: Use when the project asks for ${name}.\n---\n\n# ${name} of the project\n`;
/** The root CLAUDE.md of acme/proj: the project context file of a dev-box session in it. */
const PROJECT_RULES = "# Made-up project rules\n\nName each file you change.\n";

/** Claude Code's user memory in acme/skills's `.claude/`: the user-level context file of a devbox
 *  session whose units come from acme/skills. */
const USER_RULES = "# Made-up user rules\n\nAnswer each question in one line.\n";

/** How Oh My Pi picks and orders a session's project context files, as a profile's evidence says
 *  it. */
const SLOT_RULE =
  "Oh My Pi 18.18.6 keeps one project context file at each level from the working directory up to the checkout's root (capability/context-file.ts, lines 36-46: one for each depth), the first in its providers' order, highest priority first (capability/index.ts, lines 177-184 and 285-333), and joins the files it keeps the farthest level first (system-prompt.ts, lines 392-398)";
/** The made-up repositories as each was at the commit SHA: every file's path and text. acme/skills
 *  holds `older` and no `newer`; its installer links each skill of skills/ into Oh My Pi's user
 *  skills, and its prepack script stages alpha and beta for the package @acme/pi-acme; it holds
 *  Claude Code's settings and user memory in `.claude/`, as a dev box's config repository does.
 *  acme/other holds its own alpha and delta, which its installer links the same way. acme/market
 *  holds zeta and a marketplace that ships it in its plugin zeta-tools, under the marketplace's
 *  name at SHA (old-market; the copies output, read later, names it new-market), and Claude
 *  Code's settings. acme/widgets holds an AGENTS.md. acme/proj holds its own skills: kappa and
 *  beta in `.claude/skills/`, lambda and older in `.omp/skills/` (beta and older are names
 *  acme/skills's installer links too), kappa and mu in `agents/.claude/skills/`, nu in
 *  `agents/agentvm/.omp/skills/` and xi in `other/.claude/skills/`; a CLAUDE.md, and an AGENTS.md
 *  in agents/agentvm/. acme/dual holds .claude/CLAUDE.md, AGENTS.md and CLAUDE.md at its root.
 *  acme/big holds its own skill omega in `.claude/skills/` and an AGENTS.md, and the stand-in gh
 *  cuts its whole tree short (TRUNCATED), as GitHub cuts the tree of a repository of too many
 *  files. acme/dots is made in the shape of a dotfiles repository: its Oh My Pi plugins
 *  package.json installs acme/powers as a `github:` dependency named acme-powers, and its installer
 *  clones acme/pack into vendor/acme-pack at PACK_PIN, a pool its skills-sources.json feeds to Oh
 *  My Pi through its extension. acme/powers holds a package whose `pi` manifest declares its
 *  skills/ (power-one and power-two); acme/pack holds pack-one and pack-later at SHA, and pack-one
 *  and pack-two at PACK_PIN (PINNED). acme/fresh and acme/newproj got their first commit at
 *  FRESH_MADE: acme/fresh holds fresh-one, which a dev box's installer installs from then on, and
 *  acme/newproj holds its own skill nova in `.claude/skills/` and a CLAUDE.md. acme/alpha-set is a
 *  skill set's repository: a package named alpha-set whose `pi` manifest declares its skills/,
 *  which hold its own alpha beside acme/skills's (setTarget). */
const INSTALLER = [
  "#!/usr/bin/env bash",
  'for skill in "$REPO"/skills/*/; do',
  '  ln -sfn "$skill" "$HOME/.omp/agent/skills/$(basename "$skill")"',
  "done",
  "",
].join("\n");
const REPOSITORIES: Record<string, Record<string, string>> = {
  // acme/infra is the deployment of the suite's Legion project ACME: its project file alone, so
  // a pod installs the worker image's packages alone. acme/deploy is a deployment in full, which
  // the Legion install test names in place of acme/infra: its project file leaves beta-tools of
  // the catalog out, its role setup installs plugins of the catalog acme/catalog, its program
  // names every-tools as the plugin every role installs, and its worker image installs memo of
  // the release v1.0.0 of acme/rel as an Oh My Pi plugin under /opt/memo.
  "acme/infra": { "legion/projects/10-ACME.yaml": "key: ACME\ninstructions: acme.md\n" },
  "acme/deploy": {
    "legion/projects/10-ACME.yaml":
      "key: ACME\ninstructions: acme.md\nverificationSkillsLeaveOut: [beta-tools]\n",
    "legion/acme-setup.sh": [
      "#!/bin/sh",
      "repo=acme/catalog",
      'omp plugin marketplace add "$repo"',
      'omp plugin install "$p@$catalog" --force',
      "",
    ].join("\n"),
    "legion/main.go": 'package main\n\nvar everyRolePlugins = []string{"every-tools"}\n',
    "legion/worker/Dockerfile": [
      "FROM scratch",
      "ARG MEMO_RELEASE_URL=https://github.com/acme/rel/releases/download",
      "ARG MEMO_VERSION=1.0.0",
      'RUN release="${MEMO_RELEASE_URL}/v${MEMO_VERSION}"; \\',
      '    install -D -m 0644 "$t/skills/memo/SKILL.md" /opt/memo/skills/memo/SKILL.md; \\',
      '    printf \'{"name": "memo", "omp": {}}\\n\' > /opt/memo/package.json',
      "",
    ].join("\n"),
  },
  "acme/catalog": {
    ".claude-plugin/marketplace.json": JSON.stringify({
      name: "acme-catalog",
      plugins: ["every-tools", "alpha-tools", "beta-tools"].map((name) => ({
        name,
        source: `./plugins/${name}`,
      })),
    }),
    "plugins/every-tools/skills/every-one/SKILL.md": skillMd("every-one"),
    "plugins/alpha-tools/skills/alpha-one/SKILL.md": skillMd("alpha-one"),
    // A second alpha, whose text differs from acme/skills' alpha: Oh My Pi lists it as
    // alpha-tools/alpha beside the bare alpha the session read.
    "plugins/alpha-tools/skills/alpha/SKILL.md":
      "---\nname: alpha\ndescription: Use for the catalog's own alpha.\n---\n\n# alpha of the catalog\n",
    "plugins/beta-tools/skills/beta-one/SKILL.md": skillMd("beta-one"),
  },
  "acme/other": {
    "skills/alpha/SKILL.md": skillMd("alpha"),
    "skills/delta/SKILL.md": skillMd("delta"),
    "installers/skills.sh": INSTALLER,
  },
  // A dev box's own repository (it holds an installer and Claude Code's settings): its installer
  // links each skill of skills/ into Oh My Pi's user skills, its Oh My Pi config ignores dropped,
  // and quiet and shy hide themselves from the listing. No session reads any of them.
  "acme/home": {
    "installers/omp.sh": INSTALLER,
    // The dev box's plugin list installs @acme/pi-acme, a second install path of acme/skills'
    // alpha and beta; and acme/home's own alpha, of other text, is another source of the name.
    "omp/plugins/package.json": JSON.stringify({
      name: "omp-plugins",
      dependencies: { "@acme/pi-acme": "1.0.0" },
    }),
    "skills/alpha/SKILL.md":
      "---\nname: alpha\ndescription: Use for the home's own alpha.\n---\n\n# alpha of home\n",
    ".claude/settings.json": "{}\n",
    "omp/config.yml": "skills:\n  ignoredSkills:\n    - dropped\n",
    "skills/unread/SKILL.md": skillMd("unread"),
    "skills/dropped/SKILL.md": skillMd("dropped"),
    "skills/quiet/SKILL.md":
      "---\nname: quiet\ndescription: Use for quiet work.\ndisable-model-invocation: true\n---\n\n# quiet\n",
    "skills/shy/SKILL.md":
      "---\nname: shy\ndescription: Use for shy work.\nhide: true\n---\n\n# shy\n",
  },
  "acme/skills": {
    "skills/alpha/SKILL.md": skillMd("alpha"),
    "skills/alpha/references/x.md": "Step notes.\n",
    "skills/beta/SKILL.md": skillMd("beta"),
    "skills/older/SKILL.md": skillMd("older"),
    ".claude/settings.json": "{}\n",
    ".claude/CLAUDE.md": USER_RULES,
    "installers/skills.sh": INSTALLER,
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
  "acme/market": {
    "skills/zeta/SKILL.md": skillMd("zeta"),
    ".claude-plugin/marketplace.json": JSON.stringify({
      name: "old-market",
      owner: { name: "acme" },
      plugins: [{ name: "zeta-tools", source: "./" }],
    }),
    ".claude/settings.json": "{}\n",
  },
  "acme/widgets": { "AGENTS.md": "# Widgets\n" },
  "acme/proj": {
    ".claude/skills/kappa/SKILL.md": projectSkillMd("kappa"),
    ".claude/skills/beta/SKILL.md": projectSkillMd("beta"),
    ".omp/skills/lambda/SKILL.md": projectSkillMd("lambda"),
    ".omp/skills/older/SKILL.md": projectSkillMd("older"),
    "agents/.claude/skills/kappa/SKILL.md": projectSkillMd("kappa"),
    "agents/.claude/skills/mu/SKILL.md": projectSkillMd("mu"),
    "agents/agentvm/.omp/skills/nu/SKILL.md": projectSkillMd("nu"),
    "other/.claude/skills/xi/SKILL.md": projectSkillMd("xi"),
    "agents/agentvm/AGENTS.md": "# Made-up VM rules\n\nStart the VM before each test.\n",
    "CLAUDE.md": PROJECT_RULES,
  },
  "acme/dual": {
    ".claude/CLAUDE.md": "# Made-up rules for Claude\n\nAnswer in one line.\n",
    "AGENTS.md": "# Made-up agent rules\n\nName each file you read.\n",
    "CLAUDE.md": "# Made-up project rules\n\nRun the tests first.\n",
  },
  "acme/big": {
    ".claude/skills/omega/SKILL.md": projectSkillMd("omega"),
    ".claude/skills/omega/references/notes.md": "Omega notes.\n",
    "AGENTS.md": "# Made-up rules of a large repository\n\nRead the index first.\n",
    "data/runs/0001.json": "{}\n",
    "data/runs/0002.json": "{}\n",
  },
  "acme/powers": {
    "package.json": JSON.stringify({ name: "powers-of-acme", pi: { skills: ["./skills"] } }),
    "skills/power-one/SKILL.md": skillMd("power-one"),
    "skills/power-two/SKILL.md": skillMd("power-two"),
    // A pack-two of other text than acme/pack's: the pool's copy keeps the bare name, since Oh My
    // Pi admits a pool an extension names before an extension package's skill, though the session
    // read the package's power-one first.
    "skills/pack-two/SKILL.md":
      "---\nname: pack-two\ndescription: Use for the powers' own pack-two.\n---\n\n# pack-two of powers\n",
  },
  "acme/dots": {
    "omp/plugins/package.json": JSON.stringify({
      name: "omp-plugins",
      dependencies: {
        "acme-powers": "github:acme/powers#v1.2.0",
        // Packages the dev box pins by npm versions, and a clone that is one skill at its root.
        "@acme/kit": "2.0.0",
        "@acme/gone": "3.0.0",
        "acme-one": "github:acme/one-skill#v1",
      },
    }),
    "skills-sources.json": JSON.stringify({ sources: [{ root: "vendor/acme-pack" }] }),
    "omp/extensions/skill-pools.ts": [
      'import * as fs from "node:fs";',
      "export default function (pi) {",
      '  pi.on("resources_discover", async () => ({',
      '    skillPaths: JSON.parse(fs.readFileSync("skills-sources.json", "utf8")).sources.map(',
      '      (source) => source.root + "/skills"',
      "    ),",
      "  }));",
      "}",
      "",
    ].join("\n"),
    "installers/omp.sh": [
      "#!/usr/bin/env bash",
      'REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"',
      'ln -sfn "$REPO_DIR/omp/extensions/skill-pools.ts" "$HOME/.omp/agent/extensions/skill-pools.ts"',
      `ensure_vendor https://github.com/acme/pack.git acme-pack ${PACK_PIN}`,
      "",
    ].join("\n"),
  },
  "acme/pack": {
    "skills/pack-one/SKILL.md": skillMd("pack-one"),
    "skills/pack-later/SKILL.md": skillMd("pack-later"),
  },
  "acme/alpha-set": {
    "package.json": JSON.stringify({ name: "alpha-set", pi: { skills: ["./skills"] } }),
    "skills/alpha/SKILL.md": skillMd("alpha"),
    "skills/alpha/references/x.md": "Step notes.\n",
  },
  // Two packages: kit tags each release; gone tags none, and the registry's tarball of gone
  // 3.0.0 ships gone-one alone.
  "acme/kit": {
    "packages/kit/package.json": JSON.stringify({ name: "@acme/kit", omp: { skills: ["skills"] } }),
    "packages/kit/skills/kit-one/SKILL.md": skillMd("kit-one"),
    "packages/kit/skills/kit-two/SKILL.md": skillMd("kit-two"),
    "packages/gone/package.json": JSON.stringify({
      name: "@acme/gone",
      omp: { skills: ["skills"] },
    }),
    "packages/gone/skills/gone-one/SKILL.md": skillMd("gone-one"),
    "packages/gone/skills/gone-two/SKILL.md": skillMd("gone-two"),
  },
  // A repository that is one skill, at its root.
  "acme/one-skill": {
    "package.json": JSON.stringify({ name: "acme-one", pi: { skills: ["."] } }),
    "SKILL.md": skillMd("one-skill"),
  },
  "acme/fresh": { "skills/fresh-one/SKILL.md": skillMd("fresh-one") },
  "acme/newproj": {
    ".claude/skills/nova/SKILL.md": projectSkillMd("nova"),
    "CLAUDE.md": "# Made-up rules of a new project\n\nKeep each change small.\n",
  },
};

/** The ref the copies output read acme/pack at, later than the sessions (repositories.tsv). */
const PACK_LATER = "5".repeat(40);

/** The made-up repositories at a commit other than SHA, by `<repository>@<ref>`: the commit the
 *  stand-in gh gives for that ref, and the files there. acme/dots's installer pins acme/pack at
 *  PACK_PIN; the copies output read it later at another ref (repositories.tsv's branch), which
 *  the stand-in answers with SHA. */
const PINNED: Record<string, { sha: string; files: Record<string, string> }> = {
  [`acme/pack@${PACK_PIN}`]: {
    sha: PACK_PIN,
    files: {
      "skills/pack-one/SKILL.md": skillMd("pack-one"),
      "skills/pack-two/SKILL.md": skillMd("pack-two"),
    },
  },
  "acme/rel@v1.0.0": { sha: REL_PIN, files: { "skills/memo/SKILL.md": skillMd("memo") } },
  [`acme/powers@${POWERS_OLD}`]: {
    sha: POWERS_OLD,
    files: {
      "package.json": JSON.stringify({ name: "powers-of-acme", pi: { skills: ["./skills"] } }),
      "skills/power-one/SKILL.md": skillMd("power-one"),
    },
  },
  [`acme/kit@${KIT_V1}`]: {
    sha: KIT_V1,
    files: {
      "packages/kit/package.json": JSON.stringify({
        name: "@acme/kit",
        omp: { skills: ["skills"] },
      }),
      "packages/kit/skills/kit-one/SKILL.md": skillMd("kit-one"),
    },
  },
  "acme/kit@kit-v1.0.0": { sha: KIT_V1, files: {} },
  [`acme/dots@${DOTS_OLD}`]: {
    sha: DOTS_OLD,
    files: {
      ...Object.fromEntries(
        Object.entries(REPOSITORIES["acme/dots"] ?? {}).filter(
          ([file]) => file !== "skills-sources.json"
        )
      ),
      "omp/plugins/package.json": JSON.stringify({
        name: "omp-plugins",
        dependencies: {
          "acme-powers": "github:acme/powers#v1.2.0",
          "@acme/kit": "1.0.0",
          "@acme/gone": "3.0.0",
          "acme-one": "github:acme/one-skill#v1",
        },
      }),
    },
  },
};

/** A gzipped tar of `files`, each under `package/`, as npm packs a package. */
function tgz(files: Record<string, string>): Uint8Array {
  const encode = (text: string) => new TextEncoder().encode(text);
  const blocks: Uint8Array[] = [];
  for (const [name, text] of Object.entries(files)) {
    const body = encode(text);
    const header = new Uint8Array(512);
    header.set(encode(`package/${name}`), 0);
    header.set(encode(`${body.length.toString(8).padStart(11, "0")}\0`), 124);
    header.set(encode("0"), 156);
    blocks.push(header, body, new Uint8Array((512 - (body.length % 512)) % 512));
  }
  blocks.push(new Uint8Array(1024));
  return Bun.gzipSync(Buffer.concat(blocks));
}

/** The registry's tarballs of the made-up packages, by `<name>@<version>`: gone 3.0.0 ships
 *  gone-one alone. */
const TARBALLS: Readonly<Record<string, () => Uint8Array>> = {
  "@acme/gone@3.0.0": () =>
    tgz({
      "package.json": JSON.stringify({ name: "@acme/gone", omp: { skills: ["skills"] } }),
      "skills/gone-one/SKILL.md": skillMd("gone-one"),
    }),
};
/** The files of `repository` at the commit `id`: SHA's, or a PINNED commit's. */
const filesAt = (repository: string, id: string): Record<string, string> | undefined =>
  id === SHA
    ? REPOSITORIES[repository]
    : Object.entries(PINNED).find(
        ([key, at]) => key.startsWith(`${repository}@`) && at.sha === id
      )?.[1].files;

/** The blob id the stand-in gives a file's text. */
const blobId = (text: string) => createHash("sha1").update(text).digest("hex");
/** The one file a batch of blobs gives cut short, so it is read alone. */
const CUT_SHORT = REPOSITORIES["acme/skills"]?.["docker/omp-plugins/package.json"] ?? "";
/** The made-up repositories whose whole tree the stand-in gh gives cut short (`truncated`). */
const TRUNCATED: Readonly<Record<string, true>> = { "acme/big": true };

/** The id the stand-in gives the tree of the directory `dir` of `repo` at SHA: SHA for its root,
 *  since GitHub takes a commit's id for its tree's, else one made from the two. */
const treeId = (repo: string, dir: string) =>
  dir === "" ? SHA : createHash("sha1").update(`tree ${repo} ${dir}`).digest("hex");

/** The entries of the directory of `repo` whose tree id is `id`, one level, as GitHub's tree read
 *  without `recursive` gives them (a subdirectory as an entry of type `tree`); undefined when no
 *  directory of `repo` has that id. */
function treeLevel(repo: string, id: string): Record<string, unknown>[] | undefined {
  const files = Object.entries(REPOSITORIES[repo] ?? {});
  const dirs = new Set(files.length === 0 ? [] : [""]);
  for (const [file] of files) {
    const parts = file.split("/");
    for (let n = 1; n < parts.length; n += 1) dirs.add(parts.slice(0, n).join("/"));
  }
  const dir = [...dirs].find((each) => treeId(repo, each) === id);
  if (dir === undefined) return undefined;
  const prefix = dir === "" ? "" : `${dir}/`;
  const entries = new Map<string, Record<string, unknown>>();
  for (const [file, text] of files) {
    if (!file.startsWith(prefix)) continue;
    const [name = "", ...rest] = file.slice(prefix.length).split("/");
    entries.set(
      name,
      rest.length === 0
        ? { path: name, mode: "100644", type: "blob", sha: blobId(text), size: text.length }
        : { path: name, mode: "040000", type: "tree", sha: treeId(repo, `${prefix}${name}`) }
    );
  }
  return [...entries.values()].sort((a, b) => (String(a.path) < String(b.path) ? -1 : 1));
}

/** The gh calls the stand-in took. */
const ghCalls: string[][] = [];

/** The peak resident memory every build of the suite reads unless a test gives its own: under the
 *  1 GiB at which build-cases stops. */
const PEAK_BYTES = 200 * 1024 ** 2;
/** A peak resident memory above that 1 GiB. */
const OVER_LIMIT = 1024 ** 3 + 1;

/** The stand-in gh: GET reads of commits (SHA, or a PINNED ref's commit), default branches,
 *  acme/infra's legion/projects/10-ACME.yaml, each file of a made-up repository at SHA, the tree
 *  of each made-up repository at SHA or at a PINNED commit (whole, cut short for TRUNCATED's, or
 *  one directory a read by its tree id) and its blobs; and GraphQL reads of blobs in a batch. The
 *  clock is the real one, and the peak resident memory is PEAK_BYTES. */
const io = (lines: string[]): BuildIo => ({
  out: (text) => lines.push(text),
  err: (line) => lines.push(line),
  npm: async (name, version) => TARBALLS[`${name}@${version}`]?.(),
  gh: async (args) => {
    ghCalls.push([...args]);
    const route = args.find((arg) => arg.startsWith("repos/")) ?? "";
    const ok = (stdout: string) => ({ code: 0, stdout, stderr: "" });
    if (args[1] === "graphql") {
      const field = (name: string) =>
        args.find((arg) => arg.startsWith(`${name}=`))?.slice(name.length + 1) ?? "";
      const owner = `${field("owner")}/${field("name")}`;
      const texts = [
        ...Object.values(REPOSITORIES[owner] ?? {}),
        ...Object.entries(PINNED).flatMap(([key, at]) =>
          key.startsWith(`${owner}@`) ? Object.values(at.files) : []
        ),
      ];
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
    const tags = /^repos\/([^/]+\/[^/]+)\/git\/matching-refs\/tags\/(.+)$/.exec(route);
    if (tags !== null) {
      const start = decodeURIComponent(tags[2] ?? "");
      const listed = (TAGS[tags[1] ?? ""] ?? []).filter((tag) => tag.startsWith(start));
      return ok(listed.map((tag) => `refs/tags/${tag}\n`).join(""));
    }
    const commits = /^(repos\/[^/]+\/[^/]+)\/commits\?(.*)$/.exec(route);
    if (commits !== null) {
      const ref = new URLSearchParams(commits[2]).get("sha");
      const repo = (commits[1] ?? "").slice("repos/".length);
      // acme/pack's later ref, the one repositories.tsv names, was made after the sessions'
      // start: GitHub gives no commit of it at or before then, and `--jq .[0].sha` prints null.
      if (repo === "acme/pack" && ref === PACK_LATER) return ok("null\n");
      // acme/powers was made at POWERS_MADE and had power-one alone (POWERS_OLD) until POWERS_AT.
      const until = Date.parse(new URLSearchParams(commits[2]).get("until") ?? "");
      if (repo === "acme/powers" && until < Date.parse(POWERS_MADE)) return ok("null\n");
      if (repo === "acme/powers" && until < Date.parse(POWERS_AT)) return ok(`${POWERS_OLD}\n`);
      if (repo === "acme/dots" && until < Date.parse(POWERS_AT)) return ok(`${DOTS_OLD}\n`);
      // acme/fresh and acme/newproj have no commit before FRESH_MADE.
      const fresh = repo === "acme/fresh" || repo === "acme/newproj";
      if (fresh && until < Date.parse(FRESH_MADE)) return ok("null\n");
      return ok(`${PINNED[`${repo}@${ref}`]?.sha ?? SHA}\n`);
    }
    if (route === "repos/acme/infra/contents/legion/projects/10-ACME.yaml") {
      return ok("key: ACME\ninstructions: acme.md\n");
    }
    const [, owned = "", file = ""] = /^repos\/([^/]+\/[^/]+)\/contents\/(.+)$/.exec(route) ?? [];
    const text = REPOSITORIES[owned]?.[decodeURIComponent(file)];
    if (text !== undefined && args.includes(`ref=${SHA}`)) return ok(text);
    const [, repository = "", kind = "", id = ""] =
      /^repos\/([^/]+\/[^/]+)\/git\/(trees|blobs)\/([0-9a-f]{40})(?:\?recursive=1)?$/.exec(route) ??
      [];
    const files = Object.entries(filesAt(repository, id) ?? REPOSITORIES[repository] ?? {});
    if (kind === "trees" && !route.endsWith("?recursive=1")) {
      const level = treeLevel(repository, id);
      if (level !== undefined)
        return ok(JSON.stringify({ sha: id, truncated: false, tree: level }));
    }
    if (kind === "trees" && filesAt(repository, id) !== undefined && files.length > 0) {
      const cut = Object.hasOwn(TRUNCATED, repository);
      return ok(
        JSON.stringify({
          sha: id,
          truncated: cut,
          tree: files.slice(0, cut ? 1 : files.length).map(([file, text]) => ({
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
  now: () => performance.now(),
  peakRssBytes: () => PEAK_BYTES,
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
  // A subagent the good session started, one level below its directory, whose recorded prompt
  // lists alpha and beta: the good case's listing is measured against it.
  const goodStore = files[SESSIONS.good?.store ?? "store_a"] ?? {};
  goodStore[`${sessionPath("good").slice(0, -".jsonl".length)}/Helper.jsonl`] =
    sessionText("subagent");
  // pinned-subagent's root: its prompt builds' digest lines, the last before the subagent's start
  // at ROOT_BUILD.
  goodStore[`${PINNED_ROOT}.jsonl`] = ROOT_DIGESTS.map(
    (at, n) =>
      `${JSON.stringify({ type: "custom", customType: "system-prompt-digest", data: `d1${n}`, id: `d0${n}`, parentId: n === 0 ? null : `d0${n - 1}`, timestamp: at })}\n`
  ).join("");
  for (const name of Object.keys(CONTROL_SESSIONS)) {
    const store = files.store_a ?? {};
    store[controlPath(name)] = controlText(name);
  }
  // The sessions the tasks query's rows come from: task-1's is a subagent whose recorded listing
  // lacks alpha; task-3's is F1 as the openai session has it, on openai/gpt-6.1-sol; each other is
  // F1 as the good session has it, so its rebuilt profile is the good case's (and task-3's is
  // too: only its model differs). The control session c-unlisted is one like task-1's, and
  // c-other-model one whose first request ran on openai/gpt-6.1-sol.
  const storeA = files.store_a ?? {};
  const taskSessions: Record<string, string> = {
    "/sessions/task-1.jsonl": unlistedSubagentText(),
    "/sessions/task-3.jsonl": sessionText("openai"),
  };
  for (const row of TASK_ROWS) storeA[row.path] = taskSessions[row.path] ?? sessionText("good");
  storeA[controlPath("c-unlisted")] = unlistedSubagentText();
  storeA[controlPath("c-other-model")] = otherModelControlText();
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
  const market = "plugin:zeta-tools@new-market (acme/market:.claude-plugin/marketplace.json)";
  const powers = "package:acme-powers (acme/powers:package.json)";
  const pack = "pool:vendor/acme-pack (acme/dots:skills-sources.json)";
  writeFileSync(
    path.join(copiesDir, "prefix-table.tsv"),
    tsv(PREFIX_TABLE_COLUMNS, [
      ["**/agent/skills/alpha/", "acme/skills", "skills/alpha", "alpha", userSkills],
      ["**/agent/skills/beta/", "acme/skills", "skills/beta", "beta", userSkills],
      ["**/agent/skills/newer/", "acme/skills", "skills/newer", "newer", userSkills],
      ["/elsewhere/other-skills/alpha/", "acme/other", "skills/alpha", "alpha", otherSkills],
      ["/elsewhere/other-skills/delta/", "acme/other", "skills/delta", "delta", otherSkills],
      ["/opt/legion/pi-acme/dist/skills/alpha/", "acme/skills", "skills/alpha", "alpha", pkg],
      ["/opt/legion/pi-acme/dist/skills/beta/", "acme/skills", "skills/beta", "beta", pkg],
      ["/opt/legion/pi-acme/dist/skills/newer/", "acme/skills", "skills/newer", "newer", pkg],
      [
        "/opt/acme-tools/skills/alpha/",
        "acme/skills",
        "skills/alpha",
        "alpha",
        "plugin:acme-tools (acme/skills:plugins/acme-tools/.claude-plugin/plugin.json)",
      ],
      [
        "**/plugins/cache/plugins/new-market___zeta-tools___*/skills/zeta/",
        "acme/market",
        "skills/zeta",
        "zeta",
        market,
      ],
      // As copies.ts gives them: acme/powers' package under its dependency's name, and the pool
      // of acme/dots's clone of acme/pack at the later ref the copies output read it at.
      ...["power-one", "power-two"].map((name) => [
        `**/node_modules/acme-powers/skills/${name}/`,
        "acme/powers",
        `skills/${name}`,
        name,
        powers,
      ]),
      ...["pack-one", "pack-later"].map((name) => [
        `**/vendor/acme-pack/skills/${name}/`,
        "acme/pack",
        `skills/${name}`,
        name,
        pack,
      ]),
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
      installer(
        "acme/market",
        ".claude-plugin/marketplace.json",
        "claude-marketplace",
        "ships",
        "plugin:zeta-tools@new-market",
        "zeta",
        "skills/zeta"
      ),
      ...["power-one", "power-two"].map((name) =>
        installer(
          "acme/powers",
          "package.json",
          "omp-package",
          "ships",
          "package:acme-powers",
          name,
          `skills/${name}`
        )
      ),
      ...["pack-one", "pack-later"].map((name) => [
        "acme/dots",
        "skills-sources.json",
        "omp-skill-sources",
        "installs",
        "pool:vendor/acme-pack",
        "omp",
        name,
        "acme/pack",
        `skills/${name}`,
        "read by omp/extensions/skill-pools.ts",
      ]),
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
      ["acme/market", "main", SHA, "no", "no", "", 10, "recursive", 1, ""],
      ["acme/proj", "main", SHA, "no", "no", "", 10, "recursive", 8, ""],
      ["acme/dual", "main", SHA, "no", "no", "", 10, "recursive", 0, ""],
      ["acme/powers", "v1.2.0", SHA, "no", "no", "", 3, "whole", 2, ""],
      ["acme/dots", "main", SHA, "no", "no", "", 4, "whole", 0, ""],
      // Read later at another ref than PACK_PIN, made after the sessions' start.
      ["acme/pack", PACK_LATER, SHA, "no", "no", "", 2, "whole", 2, ""],
    ])
  );
  writeFileSync(
    path.join(copiesDir, "skill-copies.tsv"),
    tsv(SKILL_COPIES_COLUMNS, [
      ["acme/other", "skills/alpha", "alpha", 10, "no", "no", "", "", ""],
      ["acme/other", "skills/delta", "delta", 10, "no", "no", "", "", ""],
      ["acme/skills", "skills/alpha", "alpha", 10, "no", "no", "", "", ""],
      ["acme/skills", "skills/beta", "beta", 10, "no", "no", "", "", ""],
      ["acme/skills", "skills/newer", "newer", 10, "no", "no", "", "", ""],
      ["forker/skills", "skills/alpha", "alpha", 10, "yes", "no", "acme/skills", "", ""],
      ["acme/market", "skills/zeta", "zeta", 10, "no", "no", "", "", ""],
      ["acme/powers", "skills/power-one", "power-one", 10, "no", "no", "", "", ""],
      ["acme/powers", "skills/power-two", "power-two", 10, "no", "no", "", "", ""],
      ["acme/pack", "skills/pack-one", "pack-one", 10, "no", "no", "", "", ""],
      ["acme/pack", "skills/pack-later", "pack-later", 10, "no", "no", "", "", ""],
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
        calls: { "0123456789abcdef": ["0123456789abcdef"] },
        unpaired: 0,
        unpaired_hashes: [],
        unpaired_calls: {},
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
  stopped: unknown;
  peakRssBytes?: number;
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
    // pinned-units and pinned-subagent, whose packages and pools are held at other commits than
    // SHA, have builds of their own below.
    const own = ["pinned-units", "pinned-subagent"];
    const names = [...Object.keys(SESSIONS).filter((name) => !own.includes(name)), "missing"];
    const sessions = sessionsFile("all.jsonl", names, ["good", "missing"]);
    const from = recorder.requests.length;
    code = await main(buildArgs(sessions, out, "7"), io(printed));
    requests = recorder.requests.slice(from);
    cases = loadCases(out, old);
  }, 120_000);

  test("each case's evidence holds its listing measure: a subagent's against its own recorded listing, a top-level session's against a subagent's of it, else not measurable; the summary counts them", () => {
    const measureOf = (name: string) => {
      const items = byName(name)?.evidence?.filter((each) => each.item === LISTING_MEASURE) ?? [];
      expect(items).toHaveLength(1);
      return ListingMeasure.parse(JSON.parse(items[0]?.source ?? "{}"));
    };
    const listed = (name: string) => (byName(name)?.profile.skills ?? []).map((each) => each.name);
    // The subagent's recorded prompt lists alpha and beta; the measure sets its profile against it.
    const own = measureOf("subagent");
    const names = ["alpha", "beta"];
    expect(own).toMatchObject({
      kind: "session listing",
      recorded: 2,
      missing: names.filter((name) => !listed("subagent").includes(name)),
      added: listed("subagent")
        .filter((name) => !names.includes(name))
        .sort(),
      rebuiltDigest: null,
      subagent: null,
    });
    expect(own.missingShare).toBe(own.missing.length / 2);
    // The good session records only its digest; its subagent Helper's listing is used.
    const good = measureOf("good");
    expect(good).toMatchObject({
      kind: "subagent listing",
      recorded: 2,
      missing: names.filter((name) => !listed("good").includes(name)),
      sessionDigest: byName("good")?.sessionDigest ?? null,
      rebuiltDigest: null,
      subagent: {
        pathHash: pathHash(`${sessionPath("good").slice(0, -".jsonl".length)}/Helper.jsonl`),
        agent: "task",
        subagents: 1,
        recorded: 1,
      },
    });
    expect(good.why).toContain("the build makes no prompt");
    // A top-level session with no subagent in its store is not measurable.
    const checkout = measureOf("checkout");
    expect(checkout).toMatchObject({ kind: "not measurable", recorded: null, missing: [] });
    expect(checkout.why).toContain("the store holds no subagent of it");
    const summary = JSON.parse(readFileSync(`${out}.summary.json`, "utf8")) as {
      listing: Record<string, number>;
    };
    expect(summary.listing["session listing"]).toBeGreaterThan(0);
    expect(summary.listing["subagent listing"]).toBe(1);
    expect(summary.listing["not measurable"]).toBeGreaterThan(0);
    expect(summary.listing.none).toBe(0);
  });

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
        "read-wins",
        "renamed-plugin",
        "proj-legion",
        "proj-devbox",
        "two-repos",
        "proj-subdir",
        "dual-root",
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
    expect(c?.source).toEqual({
      store: "store_a",
      pathHash: pathHash(sessionPath("good")),
      kind: "session",
    });
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
      expect(check?.before).toBeNumber();
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
      contextFiles: [{ repo: "acme/skills", commit: SHA, path: ".claude/CLAUDE.md", user: true }],
    });
    const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
    expect(evidence["unit user-skills:omp (acme/skills:installers/skills.sh)"]).toContain(
      `message 2 reads [Skill file: ${F1_SKILLS}/alpha/SKILL.md]`
    );
    expect(evidence[`commit ${SHA} of acme/skills`]).toContain("at or before the session's start");
    expect(evidence[`the skills and manifests of acme/skills at ${SHA}`]).toContain(
      "its 3 SKILL.md files and 5 manifest files"
    );
    expect(evidence["skill alpha (acme/skills skills/alpha)"]).toBe(
      `acme/skills at ${SHA}: user-skills:omp links it (acme/skills:installers/skills.sh); message 2 reads this copy`
    );
    // The copies output lists `newer`, which acme/skills did not hold at the commit, and not
    // `older`, which it held.
    expect(evidence["left out: skill newer (acme/skills skills/newer)"]).toContain(
      "user-skills:omp does not ship it at the session's start"
    );
    expect(evidence["skill older (acme/skills skills/older)"]).toContain(
      "installers.tsv, which the copies output read later, does not list it"
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
    const commitCall = ghCalls.flat().find((arg) => arg.startsWith("repos/acme/skills/commits?"));
    expect(commitCall).toContain("until=2026-11-20T01%3A59%3A41.780Z");
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

  test("--reprofile builds each profile again from the case and its session's head: the new cases differ from the old ones only in profile and evidence, the store is asked for the session heads alone, one query a store, and no reader is called", async () => {
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
    const logBefore = psqlLogLength();
    expect(await main(args(staleFile, fresh), io(lines))).toBe(0);
    expect(lines.join("")).not.toContain("left out");
    expect(recorder.requests.length).toBe(requestsBefore);
    expect(
      psqlLines(logBefore)
        .map((line) => `${line.db} ${line.kind}`)
        .sort()
    ).toEqual([
      // The prompt builds of each case's root, a subagent's and a top-level session's own, one
      // query a store (digest-times.sql).
      "store_a digests",
      "store_a heads",
      "store_a subagents",
      "store_b digests",
      "store_b heads",
      "store_b subagents",
    ]);

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
      // The session's head gives the working directory the builder read from the transcript's
      // header, so every source, each that names that directory among them, is the builder's.
      const differing = (c.evidence ?? []).filter(
        (each, at) => each.source !== built.evidence?.[at]?.source
      );
      expect(differing.map((each) => each.item)).toEqual([]);
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

  test("--reprofile takes each case's working directory from its session's head, so a case built before the session repository was recorded gets its project skills and root context file; a case whose session the store no longer holds keeps the old rule, and its evidence says so", async () => {
    const devbox = byName("proj-devbox") as Case;
    const legion = byName("proj-legion") as Case;
    // Each as a builder wrote it before the session repository: no project skill, no context file
    // of acme/proj, no session repository or working directory in its profile, and no item naming
    // the session repository.
    const before = (c: Case): Case => ({
      ...c,
      profile: {
        ...c.profile,
        skills: c.profile.skills.filter((skill) => skill.repo !== "acme/proj"),
        contextFiles: c.profile.contextFiles.filter((file) => file.repo !== "acme/proj"),
        sessionRepository: undefined,
        workingDirectory: undefined,
      },
      evidence: (c.evidence ?? []).filter((each) => !each.item.startsWith("session repository ")),
    });
    // A case whose session the store holds no more, its old evidence naming acme/proj.
    const gone: Case = {
      ...legion,
      id: `${legion.id}-gone`,
      source: { ...legion.source, pathHash: pathHash("/sessions/gone.jsonl") },
    };
    const from = path.join(scratch, "heads", "old.jsonl");
    mkdirSync(path.dirname(from), { recursive: true });
    writeFileSync(
      from,
      [before(devbox), before(legion), gone].map((c) => `${JSON.stringify(c)}\n`).join("")
    );
    const fresh = path.join(scratch, "heads", "reprofiled.jsonl");
    const logBefore = psqlLogLength();
    const lines: string[] = [];
    expect(
      await main(
        [
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
          fresh,
        ],
        io(lines)
      )
    ).toBe(0);
    const [d, l, g] = loadCases(fresh, old);
    // The header's working directory gives each its session repository again.
    expect(d?.profile).toEqual(devbox.profile);
    expect(l?.profile).toEqual(legion.profile);
    for (const c of [d, l]) {
      expect(c?.evidence?.map((each) => each.item)).toContain("session repository acme/proj");
      expect(c?.evidence?.map((each) => each.item)).not.toContain("no session head");
    }
    // The one the store no longer holds keeps the old rule: the repository its old evidence names.
    expect(g?.profile).toEqual(legion.profile);
    expect(g?.evidence?.find((each) => each.item === "no session head")?.source).toBe(
      "the store store_b holds no file of the session now (session-head.sql), so its working directory is not known: the session repository is the one the old evidence names"
    );
    // One query a store for each of the heads, the prompt builds and the subagents; each head
    // saved beside the new case file.
    expect(
      psqlLines(logBefore)
        .map((line) => `${line.db} ${line.kind}`)
        .sort()
    ).toEqual([
      "store_a digests",
      "store_a heads",
      "store_a subagents",
      "store_b digests",
      "store_b heads",
      "store_b subagents",
    ]);
    const headFile = `${devbox.source.store}-${devbox.source.pathHash}.json`;
    expect(existsSync(path.join(`${fresh}.sessions`, headFile))).toBe(true);
    const summary = JSON.parse(readFileSync(`${fresh}.summary.json`, "utf8")) as {
      cases: { sessionHead: string }[];
    };
    expect(summary.cases.map((each) => each.sessionHead)).toEqual(["read", "read", "not held"]);
  }, 60_000);

  // Catches: a case whose session the store no longer holds rebuilt at its checkout's root, its
  // working directory lost while its evidence still names it.
  test("--reprofile of a case whose session the store no longer holds keeps the case's working directory below its checkout's root, and that level's project skills", async () => {
    const subdir = byName("proj-subdir") as Case;
    const gone: Case = {
      ...subdir,
      id: `${subdir.id}-gone`,
      source: { ...subdir.source, pathHash: pathHash("/sessions/gone-subdir.jsonl") },
    };
    const from = path.join(scratch, "heads-subdir", "old.jsonl");
    mkdirSync(path.dirname(from), { recursive: true });
    writeFileSync(from, `${JSON.stringify(gone)}\n`);
    const fresh = path.join(scratch, "heads-subdir", "reprofiled.jsonl");
    const args = ["build-cases", "--config", config, "--old", old, "--copies", copiesDir];
    const reprofile = ["--count", countDir, "--reprofile", from, "--out", fresh];
    expect(await main([...args, ...reprofile], io([]))).toBe(0);
    const [c] = loadCases(fresh, old);
    expect(c?.profile.workingDirectory).toBe("agents/agentvm");
    expect(c?.profile).toEqual(subdir.profile);
    expect(c?.evidence?.find((each) => each.item === "no session head")?.source).toContain(
      "the session repository is the one the old evidence names"
    );
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

  test("a top-level devbox session's profile holds Oh My Pi's user-level context file, the .claude/CLAUDE.md of the one repository of its units that holds Claude Code's settings, at the session's start; a subagent's holds none beside its recorded prompt", () => {
    const c = byName("good");
    expect(c?.context).toBe("devbox");
    expect(c?.profile.contextFiles).toEqual([
      { repo: "acme/skills", commit: SHA, path: ".claude/CLAUDE.md", user: true },
    ]);
    const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
    // Its working directory is no checkout of a repository of the copies output's list, so it has
    // no project context file (`no session repository`, below).
    expect(evidence["no session repository"]).toBeDefined();
    expect(evidence["context file acme/skills .claude/CLAUDE.md"]).toBe(
      `Oh My Pi's user-level context file is CLAUDE.md in Claude Code's config directory, on a dev box the .claude/ of the repository that installs its units: of the repositories of the profile's units (acme/skills), acme/skills alone holds Claude Code's settings .claude/settings.json at ${SHA}, and it has .claude/CLAUDE.md at ${SHA}`
    );
    // It is read at the commit of the session's start.
    expect(
      ghCalls.some(
        (args) =>
          args.includes("repos/acme/skills/contents/.claude/CLAUDE.md") &&
          args.includes(`ref=${SHA}`)
      )
    ).toBe(true);
    // A devbox session two of whose units' repositories hold Claude Code's settings has none, and
    // its evidence says why.
    const renamed = byName("renamed-plugin");
    expect(renamed?.profile.contextFiles).toEqual([]);
    expect(
      renamed?.evidence?.find(
        (each) => each.item === "left out: Oh My Pi's user-level context file"
      )?.source
    ).toBe(
      "2 repositories (acme/market, acme/skills) of the profile's units (acme/market, acme/skills) hold Claude Code's settings .claude/settings.json at the session's start, so which is the dev box's Claude Code config directory, whose CLAUDE.md Oh My Pi reads at user level, is not known"
    );
    // A subagent's recorded prompt holds the context files its session was given.
    const subagent = byName("subagent");
    expect(subagent?.profile.systemPrompt).toEqual(SUBAGENT_PROMPT);
    expect(subagent?.profile.contextFiles).toEqual([]);
    expect(subagent?.evidence?.map((each) => each.item)).not.toContain(
      "context file acme/skills .claude/CLAUDE.md"
    );
  });

  test("a Legion pane in a workspace lists the .claude/skills and .omp/skills of the workspace's repository at its commit, each as a project skill, and its profile names that repository at that commit", () => {
    const c = byName("proj-legion");
    expect(c?.context).toBe("legion:ACME:tester");
    // The working directory is the checkout's root, so no skill below it is listed.
    expect(c?.profile.skills).toEqual([
      { repo: "acme/skills", commit: SHA, path: "skills/alpha", name: "alpha" },
      { repo: "acme/skills", commit: SHA, path: "skills/beta", name: "beta" },
      // The project's beta differs from the package's, so Oh My Pi lists it under its provider.
      {
        repo: "acme/proj",
        commit: SHA,
        path: ".claude/skills/beta",
        name: "beta",
        namespace: "claude",
      },
      { repo: "acme/proj", commit: SHA, path: ".claude/skills/kappa", name: "kappa" },
      { repo: "acme/proj", commit: SHA, path: ".omp/skills/lambda", name: "lambda" },
      { repo: "acme/proj", commit: SHA, path: ".omp/skills/older", name: "older" },
    ]);
    expect(c?.profile.sessionRepository).toEqual({ repo: "acme/proj", commit: SHA });
    const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
    expect(evidence["session repository acme/proj"]).toBe(
      "the session's working directory /legion/workspaces/acme/proj/K-1/ is a Legion workspace of it: /legion/workspaces/<owner>/<name>/"
    );
    expect(evidence["skill kappa (acme/proj .claude/skills/kappa)"]).toBe(
      `project skill of acme/proj at ${SHA}: Oh My Pi lists each <name>/SKILL.md under .claude/skills/ of the working directory and of each parent up to the repository's root, the nearest first (discovery/claude.ts, loadSkills, lines 205-224)`
    );
    expect(evidence["skill lambda (acme/proj .omp/skills/lambda)"]).toBe(
      `project skill of acme/proj at ${SHA}: Oh My Pi lists each <name>/SKILL.md that has a description under .omp/skills/ of the working directory and of each parent up to the repository's root, the nearest first (discovery/builtin.ts, loadSkills, lines 314-327, over getProjectAncestorDirs, lines 81-109)`
    );
    // The repository is read at the session's commit, one directory a read where Oh My Pi reads a
    // project (from its root), and never whole: a session repository's whole tree can be more
    // than GitHub gives in one answer.
    expect(ghCalls.some((args) => args.includes(`repos/acme/proj/git/trees/${SHA}`))).toBe(true);
    expect(
      ghCalls.some((args) => args.includes(`repos/acme/proj/git/trees/${SHA}?recursive=1`))
    ).toBe(false);
  });

  test("a dev-box session in a checkout of the one listed repository of its directory's name lists that repository's project skills and gets its root CLAUDE.md beside the user-level file", () => {
    const c = byName("proj-devbox");
    expect(c?.context).toBe("devbox");
    expect(c?.profile.skills).toEqual([
      // The user's older and the project's beta differ from the copies that keep their names.
      { repo: "acme/skills", commit: SHA, path: "skills/older", name: "older", namespace: "agent" },
      { repo: "acme/skills", commit: SHA, path: "skills/alpha", name: "alpha" },
      { repo: "acme/skills", commit: SHA, path: "skills/beta", name: "beta" },
      {
        repo: "acme/proj",
        commit: SHA,
        path: ".claude/skills/beta",
        name: "beta",
        namespace: "claude",
      },
      { repo: "acme/proj", commit: SHA, path: ".claude/skills/kappa", name: "kappa" },
      { repo: "acme/proj", commit: SHA, path: ".omp/skills/lambda", name: "lambda" },
      { repo: "acme/proj", commit: SHA, path: ".omp/skills/older", name: "older" },
    ]);
    expect(c?.profile.contextFiles).toEqual([
      { repo: "acme/skills", commit: SHA, path: ".claude/CLAUDE.md", user: true },
      { repo: "acme/proj", commit: SHA, path: "CLAUDE.md" },
    ]);
    expect(c?.profile.sessionRepository).toEqual({ repo: "acme/proj", commit: SHA });
    const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
    expect(evidence["session repository acme/proj"]).toBe(
      "a dev-box session's repository is the one repository of the copies output's list (repositories.tsv) named as the last part of its working directory, or of the nearest of that directory's parents named as any listed repository. The working directory /home/dev/src/proj is named proj, which acme/proj alone of the list is named"
    );
    expect(evidence["context file acme/proj CLAUDE.md"]).toBe(
      `${SLOT_RULE}: at the checkout's root, depth 0, acme/proj (the session repository) has CLAUDE.md at ${SHA}, and Oh My Pi keeps CLAUDE.md, priority 10 (discovery/claude-md.ts, lines 20-30, over discovery/helpers.ts's loadStandaloneContextFiles (lines 844-900), registered after agents-md.ts (discovery/index.ts, lines 24-25))`
    );
    // The tree read at that commit gives it; no file of acme/proj is read alone.
    expect(
      ghCalls.some((args) => args.some((arg) => arg.startsWith("repos/acme/proj/contents/")))
    ).toBe(false);
  });

  test("a dev-box directory whose name two listed repositories have, or none has, gives no session repository, and the evidence says which matched", () => {
    for (const name of ["two-repos", "good"]) {
      const c = byName(name);
      expect(c?.profile.skills.map((skill) => skill.name)).toEqual(["alpha", "beta", "older"]);
      expect(c?.profile.skills.every((skill) => skill.repo === "acme/skills")).toBe(true);
      expect(c?.profile.contextFiles).toEqual([
        { repo: "acme/skills", commit: SHA, path: ".claude/CLAUDE.md", user: true },
      ]);
      expect(c?.profile.sessionRepository).toBeUndefined();
    }
    const why = (name: string) =>
      byName(name)?.evidence?.find((each) => each.item === "no session repository")?.source;
    const rule =
      "a dev-box session's repository is the one repository of the copies output's list (repositories.tsv) named as the last part of its working directory, or of the nearest of that directory's parents named as any listed repository.";
    expect(why("two-repos")).toBe(
      `${rule} The working directory /home/dev/src/skills/notes is under /home/dev/src/skills, named skills, which 2 repositories of the list are named (acme/skills, forker/skills), so which one it is a checkout of is not known`
    );
    expect(why("good")).toBe(
      `${rule} No part of the working directory /legion/scratch/tmp/acme-1/t1-runs/f1/work is named as a repository of the list`
    );
  });

  test("a project skill and a user or plugin skill of the same name follow Oh My Pi's order: the project's .omp/skills come before ~/.omp/agent/skills, and the project's .claude/skills after those and after extension packages", () => {
    /** Checks that `source` gives `start` and then Oh My Pi's order with its source. */
    const decided = (source: string | undefined, start: string) => {
      expect(source).toContain(
        `${start}: Oh My Pi 18.18.6 keeps under a name the first skill it admits, in its providers' order`
      );
      for (const cited of [
        "capability/index.ts, lines 177-184",
        "extensibility/skills.ts, resolveCollision, lines 141-223",
        "discovery/builtin.ts, lines 81-109 and 314-342",
        "discovery/omp-plugins.ts, lines 136-169",
        "discovery/claude.ts, lines 205-249",
      ]) {
        expect(source).toContain(cited);
      }
    };
    // On the dev box acme/skills's installer links beta and older into ~/.omp/agent/skills: the
    // user's beta comes before the project's .claude/skills/beta, and the project's
    // .omp/skills/older before the user's older. Each later copy differs, so Oh My Pi lists it
    // under a namespace: claude/beta (its provider's, for the dotted .claude/) and agent/older.
    const devbox = Object.fromEntries(
      byName("proj-devbox")?.evidence?.map((each) => [each.item, each.source]) ?? []
    );
    const claudeBeta = devbox["skill claude/beta (acme/proj .claude/skills/beta)"];
    decided(
      claudeBeta,
      "under the name beta Oh My Pi gives the session the copy of acme/skills skills/beta under that name (user-skills:omp links it (acme/skills:installers/skills.sh))"
    );
    expect(claudeBeta).toContain(
      "and this copy's text differs: Oh My Pi 18.18.6 keeps the bare name"
    );
    expect(devbox["skill older (acme/proj .omp/skills/older)"]).toStartWith(
      "project skill of acme/proj"
    );
    decided(
      devbox["skill agent/older (acme/skills skills/older)"],
      "under the name older Oh My Pi gives the session the copy of acme/proj .omp/skills/older under that name (project skill of acme/proj)"
    );
    // In the Legion pane the package @acme/pi-acme, an extension package, ships beta, and it
    // comes before the project's .claude/skills/beta.
    const legion = Object.fromEntries(
      byName("proj-legion")?.evidence?.map((each) => [each.item, each.source]) ?? []
    );
    const legionBeta = legion["skill claude/beta (acme/proj .claude/skills/beta)"];
    expect(legionBeta).toContain(
      "under the name beta Oh My Pi gives the session the copy of acme/skills skills/beta under that name (package:@acme/pi-acme "
    );
    expect(legionBeta).toContain("extension packages come next (90");
  });

  test("a dev-box working directory below its checkout's root lists the .omp/skills and .claude/skills of that directory and of each parent up to the root, the nearer first, and of no other directory", () => {
    const c = byName("proj-subdir");
    // other/ is no level of the working directory, so its xi is not listed.
    expect(c?.profile.skills).toEqual([
      { repo: "acme/skills", commit: SHA, path: "skills/older", name: "older", namespace: "agent" },
      { repo: "acme/skills", commit: SHA, path: "skills/alpha", name: "alpha" },
      { repo: "acme/skills", commit: SHA, path: "skills/beta", name: "beta" },
      {
        repo: "acme/proj",
        commit: SHA,
        path: ".claude/skills/beta",
        name: "beta",
        namespace: "claude",
      },
      { repo: "acme/proj", commit: SHA, path: "agents/.claude/skills/kappa", name: "kappa" },
      { repo: "acme/proj", commit: SHA, path: ".omp/skills/lambda", name: "lambda" },
      { repo: "acme/proj", commit: SHA, path: "agents/.claude/skills/mu", name: "mu" },
      { repo: "acme/proj", commit: SHA, path: "agents/agentvm/.omp/skills/nu", name: "nu" },
      { repo: "acme/proj", commit: SHA, path: ".omp/skills/older", name: "older" },
    ]);
    const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
    expect(evidence["session repository acme/proj"]).toBe(
      "a dev-box session's repository is the one repository of the copies output's list (repositories.tsv) named as the last part of its working directory, or of the nearest of that directory's parents named as any listed repository. The working directory /home/dev/src/proj/agents/agentvm is under /home/dev/src/proj, named proj, which acme/proj alone of the list is named; the working directory is agents/agentvm in the checkout"
    );
    // Of two kappa, the one of the nearer level (agents/) comes first.
    expect(evidence["left out: skill kappa from acme/proj .claude/skills/kappa"]).toStartWith(
      "project skill of acme/proj too; Oh My Pi gives the session the copy of acme/proj agents/.claude/skills/kappa under that name (project skill of acme/proj): Oh My Pi 18.18.6 keeps under a name the first skill it admits"
    );
    expect(c?.profile.workingDirectory).toBe("agents/agentvm");
    expect(c?.profile.sessionRepository).toEqual({ repo: "acme/proj", commit: SHA });
  });

  test("a working directory below its checkout's root gets the context file of each level up to the root, the farthest first, at its path in the repository", () => {
    const c = byName("proj-subdir");
    expect(c?.profile.contextFiles).toEqual([
      { repo: "acme/skills", commit: SHA, path: ".claude/CLAUDE.md", user: true },
      { repo: "acme/proj", commit: SHA, path: "CLAUDE.md" },
      { repo: "acme/proj", commit: SHA, path: "agents/agentvm/AGENTS.md" },
    ]);
    const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
    expect(evidence["context file acme/proj agents/agentvm/AGENTS.md"]).toBe(
      `${SLOT_RULE}: at agents/agentvm, depth 0, acme/proj (the session repository) has agents/agentvm/AGENTS.md at ${SHA}, and Oh My Pi keeps agents/agentvm/AGENTS.md, priority 10 (discovery/agents-md.ts, lines 20-30, over discovery/helpers.ts's loadStandaloneContextFiles (lines 844-900))`
    );
    expect(evidence["context file acme/proj CLAUDE.md"]).toStartWith(
      `${SLOT_RULE}: at the checkout's root, depth 2, acme/proj (the session repository) has CLAUDE.md`
    );
  });

  test("within one level's slot the context file Oh My Pi's ranking picks is kept: the working directory's .claude/CLAUDE.md (80) before its AGENTS.md and CLAUDE.md (10), and the user-level file stays marked apart", () => {
    const c = byName("dual-root");
    expect(c?.profile.contextFiles).toEqual([
      { repo: "acme/skills", commit: SHA, path: ".claude/CLAUDE.md", user: true },
      { repo: "acme/dual", commit: SHA, path: ".claude/CLAUDE.md" },
    ]);
    const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
    expect(evidence["context file acme/dual .claude/CLAUDE.md"]).toBe(
      `${SLOT_RULE}: at the checkout's root, depth 0, acme/dual (the session repository) has .claude/CLAUDE.md, AGENTS.md, CLAUDE.md at ${SHA}, and Oh My Pi keeps .claude/CLAUDE.md, priority 80 (discovery/claude.ts, line 45 and lines 72-77 and 180-192: at the working directory alone, an empty file too)`
    );
    expect(evidence["left out: acme/dual AGENTS.md"]).toBe(
      `${SLOT_RULE}: at the checkout's root, depth 0, .claude/CLAUDE.md (priority 80) comes before it (priority 10; discovery/agents-md.ts, lines 20-30, over discovery/helpers.ts's loadStandaloneContextFiles (lines 844-900))`
    );
    expect(evidence["left out: acme/dual CLAUDE.md"]).toStartWith(
      `${SLOT_RULE}: at the checkout's root, depth 0, .claude/CLAUDE.md (priority 80) comes before it (priority 10;`
    );
  });

  test("a Legion pane's root context file follows Oh My Pi's rule as a dev box's does: the workspace repository's AGENTS.md, else its CLAUDE.md", () => {
    // acme/proj holds no AGENTS.md, so its CLAUDE.md is the one Oh My Pi reads.
    expect(byName("proj-legion")?.profile.contextFiles).toEqual([
      { repo: "acme/proj", commit: SHA, path: "CLAUDE.md" },
      { repo: "acme/infra", commit: SHA, path: "legion/instructions/acme.md" },
    ]);
    const evidence = (name: string) =>
      Object.fromEntries(byName(name)?.evidence?.map((each) => [each.item, each.source]) ?? []);
    const rule = `${SLOT_RULE}: at the checkout's root, depth 0, `;
    expect(evidence("proj-legion")["context file acme/proj CLAUDE.md"]).toStartWith(
      `${rule}acme/proj (the session repository) has CLAUDE.md at ${SHA}, and Oh My Pi keeps CLAUDE.md, priority 10`
    );
    // acme/widgets holds an AGENTS.md, which comes first.
    expect(evidence("legion")["context file acme/widgets AGENTS.md"]).toStartWith(
      `${rule}acme/widgets (the session repository) has AGENTS.md at ${SHA}, and Oh My Pi keeps AGENTS.md, priority 10`
    );
  });

  test("a read whose unit the copies output names by a later name ships nothing at the session's start, so the prefix rows its manifest gives at that commit place it at the unit that installed it then", () => {
    const c = byName("renamed-plugin");
    expect(c?.profile.skills.find((skill) => skill.name === "zeta")).toEqual({
      repo: "acme/market",
      commit: SHA,
      path: "skills/zeta",
      name: "zeta",
    });
    const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
    const later = "plugin:zeta-tools@new-market (acme/market:.claude-plugin/marketplace.json)";
    const then = "plugin:zeta-tools@old-market (acme/market:.claude-plugin/marketplace.json)";
    expect(evidence[`unit ${later}`]).toContain(
      `message 2 reads [Skill file: ${OLD_MARKET_ZETA}], which attribute.ts's checkout rule places at acme/market skills/zeta`
    );
    expect(evidence[`left out: the skills of ${later}`]).toBe(
      `acme/market at ${SHA}: by copies.ts's rules .claude-plugin/marketplace.json ships no skill there`
    );
    expect(evidence[`unit ${then}`]).toBe(
      `message 2 reads [Skill file: ${OLD_MARKET_ZETA}], under the prefix row **/plugins/cache/plugins/old-market___zeta-tools___*/skills/zeta/ that the manifests of acme/market at ${SHA} give by copies.ts's rules; no unit installers.tsv names for the read ships zeta at that commit`
    );
    expect(evidence["skill zeta (acme/market skills/zeta)"]).toBe(
      `acme/market at ${SHA}: plugin:zeta-tools@old-market ships it (acme/market:.claude-plugin/marketplace.json); installers.tsv, which the copies output read later, does not list it; message 2 reads this copy`
    );
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
          const shown = /^(\d+): (.*)$/.exec(line);
          if (shown === null) throw new Error(`a line shown with no number: ${line}`);
          return { number: Number(shown[1]), text: shown[2] };
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

  test("a session the profile check drops makes no reader request: the profile at the session's start is checked before the first reader call", () => {
    const record = summaryOf(out).sessions.find(
      (each) => each.hash === fileHash(sessionPath("unshipped"))
    );
    expect(record).toMatchObject({
      reason: "not-cut-copy",
      detail: "the profile installs no alpha of acme/skills at the session's start",
      readerUsd: 0,
    });
    expect(record?.windows).toBeUndefined();
    expect(requests.some((each) => each.body.includes("(case unshipped)"))).toBe(false);
  });

  test("the copy of a skill the session read is the profile's copy of its name, ahead of another unit's copy that comes first", () => {
    const c = byName("read-wins");
    expect(c?.profile.skills).toEqual([
      { repo: "acme/skills", commit: SHA, path: "skills/alpha", name: "alpha" },
      { repo: "acme/skills", commit: SHA, path: "skills/beta", name: "beta" },
      { repo: "acme/other", commit: SHA, path: "skills/delta", name: "delta" },
      { repo: "acme/skills", commit: SHA, path: "skills/older", name: "older" },
    ]);
    const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
    // acme/other's unit comes first, from the read of delta at message 2.
    const units = (c?.evidence ?? []).filter((each) => each.item.startsWith("unit "));
    expect(units.map((each) => each.item)).toEqual([
      "unit user-skills:omp (acme/other:installers/skills.sh)",
      "unit user-skills:omp (acme/skills:installers/skills.sh)",
    ]);
    expect(evidence["left out: skill alpha from acme/other skills/alpha"]).toBe(
      "user-skills:omp links it (acme/other:installers/skills.sh) too; the session read the copy of acme/skills skills/alpha (message 4); its SKILL.md is the same, and Oh My Pi drops such a copy (NAMESPACE_RULE)"
    );
    expect(evidence["skill alpha (acme/skills skills/alpha)"]).toContain(
      "; message 4 reads this copy"
    );
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
      "not-cut-copy": 4,
      "no-role": 1,
      "bad-session-init": 1,
      unparseable: 1,
      "not-in-store": 1,
    });
    expect(Object.keys(summary.drops).every((reason) => reason in DROP_REASONS)).toBe(true);
    // Every session but pinned-units and pinned-subagent, and the missing one.
    expect(summary.sessionsTried).toBe(Object.keys(SESSIONS).length - 2 + 1);
    expect(summary.casesBuilt).toBe(21);
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
    expect(reasonOf("unshipped")).toBe("not-cut-copy");
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

  test("a pull pages at 64 rows a call, inside the file row's slices too, and a hash without a path is resolved with sessions.sql", () => {
    const log = readFileSync(psqlLog, "utf8")
      .split("\n")
      .filter((line) => line !== "")
      .map(
        (line) =>
          JSON.parse(line) as {
            kind: string;
            rows: number;
            after?: number[];
            first?: number[];
            limit64: boolean;
          }
      );
    expect(log.every((each) => each.limit64)).toBe(true);
    expect(log.filter((each) => each.kind === "sessions")).toHaveLength(1);
    expect(log.some((each) => each.kind === "pull" && each.rows === 64)).toBe(true);
    // A call that starts after a slice of the file row and returns the next one: a page that
    // ended inside the file row.
    expect(
      log.some((each) => each.kind === "pull" && each.after?.[0] === -1 && each.first?.[0] === -1)
    ).toBe(true);
    const good = summaryOf(out).sessions.find(
      (each) => each.hash === fileHash(sessionPath("good"))
    );
    expect(good?.pulls).toBeGreaterThan(1);
  });

  test("a build under the memory limit records its peak resident memory in the summary and prints it in its last line", () => {
    expect(summaryOf(out).peakRssBytes).toBe(PEAK_BYTES);
    expect(printed.at(-1)).toContain(
      `of readers at list price, peak memory 200 MiB; written to ${out} and ${out}.summary.json`
    );
  });
});

test("a dev-box session that read a skill of a github: dependency's package and one of a cloned pool gets a profile that holds each unit's skills, the pool's at the commit its installer pins at the session's start", async () => {
  const out = path.join(scratch, "pinned-units", "cases.jsonl");
  const lines: string[] = [];
  const callsBefore = ghCalls.length;
  const sessions = sessionsFile("pinned-units.jsonl", ["pinned-units"]);
  expect(await main(buildArgs(sessions, out, "1"), io(lines))).toBe(EXIT.ok);
  const [c] = loadCases(out, old);
  expect(c?.context).toBe("devbox");
  expect(
    c?.profile.skills
      .map((skill) => `${skill.name} ${skill.repo} ${skill.path} ${skill.commit}`)
      .sort()
  ).toEqual([
    `alpha acme/skills skills/alpha ${SHA}`,
    `beta acme/skills skills/beta ${SHA}`,
    `older acme/skills skills/older ${SHA}`,
    `pack-one acme/pack skills/pack-one ${PACK_PIN}`,
    `pack-two acme/pack skills/pack-two ${PACK_PIN}`,
    `pack-two acme/powers skills/pack-two ${SHA}`,
    `power-one acme/powers skills/power-one ${SHA}`,
    `power-two acme/powers skills/power-two ${SHA}`,
  ]);
  // Two pack-two of other texts: the pool's keeps the bare name, and the package's is listed
  // under the name of its directory in the plugins' node_modules.
  expect(c?.profile.skills).toContainEqual({
    repo: "acme/powers",
    commit: SHA,
    path: "skills/pack-two",
    name: "pack-two",
    namespace: "acme-powers",
  });
  // The package at its repository's root is named as Oh My Pi installs it.
  expect(c?.profile.plugins).toContainEqual({
    repo: "acme/powers",
    commit: SHA,
    path: ".",
    name: "acme-powers",
  });
  const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
  expect(evidence[`commit ${PACK_PIN} of acme/pack`]).toStartWith(
    `the last commit of ${PACK_PIN}, the ref acme/dots:installers/omp.sh pins at the session's start`
  );
  expect(evidence["left out: skill pack-later (acme/pack skills/pack-later)"]).toContain(
    "does not ship it at the session's start"
  );
  // acme/pack's later ref (repositories.tsv's) has no commit at the session's start, so its
  // default branch is read before its installer's pin takes its place.
  expect(
    ghCalls
      .slice(callsBefore)
      .flat()
      .some((arg) => arg.startsWith(`repos/acme/pack/commits?sha=${PACK_LATER}&`))
  ).toBe(true);
  expect(
    ghCalls
      .slice(callsBefore)
      .flat()
      .some((arg) => arg.startsWith("repos/acme/pack/commits?sha=main&"))
  ).toBe(true);
});

test("a subagent's profile reads its packages at its root's last prompt build before its start, and its pools at its own start, and the evidence names both times", async () => {
  const out = path.join(scratch, "pinned-subagent", "cases.jsonl");
  const lines: string[] = [];
  const sessions = sessionsFile("pinned-subagent.jsonl", ["pinned-subagent"]);
  expect(await main(buildArgs(sessions, out, "1"), io(lines))).toBe(EXIT.ok);
  const [c] = loadCases(out, old);
  // acme/powers, made after the root's start and before its last prompt build, is read at that
  // build, when it had power-one alone: power-two, added later but before the subagent's start,
  // is not installed. acme/pack, the pool's clone, is read at the subagent's start.
  expect(
    c?.profile.skills
      .map((skill) => `${skill.name} ${skill.repo} ${skill.commit}`)
      .filter((each) => !each.includes("acme/skills"))
      .sort()
  ).toEqual([
    `pack-one acme/pack ${PACK_PIN}`,
    `pack-two acme/pack ${PACK_PIN}`,
    `power-one acme/powers ${POWERS_OLD}`,
  ]);
  const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
  expect(evidence["read times"]).toStartWith(
    `the subagent started at 2026-11-20T01:59:41.780Z; its root (${pathHash(`${PINNED_ROOT}.jsonl`).slice(0, 12)}) built its prompt last at ${ROOT_BUILD} before it (2 builds), so each repository is read at ${ROOT_BUILD}, and a pool's at the subagent's start 2026-11-20T01:59:41.780Z: Oh My Pi 18.18.6 gives a subagent the skills its parent loaded`
  );
  expect(evidence[`commit ${POWERS_OLD} of acme/powers`]).toContain(
    `at or before the root's last prompt build ${ROOT_BUILD}`
  );
  expect(evidence[`commit ${PACK_PIN} of acme/pack`]).toContain(
    "at or before the session's start 2026-11-20T01:59:41.780Z"
  );
});

test("a subagent whose dev box pins packages by npm versions gets each package as the version its plugin list pinned at the root's last prompt build ships it, its pool as its own start has it, and a skill at a repository's root with the path .", async () => {
  // The suite's copies output, with acme/dots as the dev box's repository (it holds an installer
  // script and Claude Code's settings) and its installs of acme/kit and acme/one-skill.
  const copies = path.join(setupDir, "copies-dots");
  mkdirSync(copies, { recursive: true });
  for (const file of ["prefix-table.tsv", "loaders.tsv", "repositories.tsv"]) {
    writeFileSync(path.join(copies, file), readFileSync(path.join(copiesDir, file)));
  }
  const rows = (lines: (string | number)[][]) => lines.map((row) => `${row.join("\t")}\n`).join("");
  writeFileSync(
    path.join(copies, "manifests.tsv"),
    `${readFileSync(path.join(copiesDir, "manifests.tsv"), "utf8")}${rows([
      ["acme/dots", "installers/omp.sh", "installer-script", "tree", 1, "clones", ""],
      ["acme/dots", ".claude/settings.json", "claude-settings", "tree", 0, "none", ""],
    ])}`
  );
  // acme/dots's plugin list installs @acme/kit, whose repository the copies output read; the
  // pinned clone acme/one-skill it names no unit of, so the build reads it at its pin.
  const list = ["acme/dots", "omp/plugins/package.json", "omp-plugins", "installs"];
  writeFileSync(
    path.join(copies, "installers.tsv"),
    `${readFileSync(path.join(copiesDir, "installers.tsv"), "utf8")}${rows([
      [
        ...list,
        "package:@acme/kit",
        "omp",
        "kit-one",
        "acme/kit",
        "packages/kit/skills/kit-one",
        "",
      ],
    ])}`
  );
  writeFileSync(
    path.join(copies, "skill-copies.tsv"),
    `${readFileSync(path.join(copiesDir, "skill-copies.tsv"), "utf8")}${rows([
      ["acme/kit", "packages/kit/skills/kit-one", "kit-one", 10, "no", "no", "", "", ""],
    ])}`
  );
  const out = path.join(scratch, "npm-pins", "cases.jsonl");
  const sessions = sessionsFile("npm-pins.jsonl", ["pinned-subagent"]);
  const lines: string[] = [];
  const code = await main(buildArgs(sessions, out, "1", "--copies", copies), io(lines));
  expect({ code, said: code === EXIT.ok ? "" : lines.join("") }).toEqual({
    code: EXIT.ok,
    said: "",
  });
  const [c] = loadCases(out, old);
  const skills = (c?.profile.skills ?? []).map(
    (skill) => `${skill.name} ${skill.repo} ${skill.path} ${skill.commit}`
  );
  // The plugin list at the root's build (DOTS_OLD) pins @acme/kit 1.0.0, whose release tag ships
  // kit-one alone; the later list's 2.0.0 does not count. @acme/gone 3.0.0 has no tag, and its
  // tarball ships gone-one alone. acme/one-skill's skill is at its root.
  expect(skills).toEqual(
    expect.arrayContaining([
      `kit-one acme/kit packages/kit/skills/kit-one ${KIT_V1}`,
      `gone-one acme/kit packages/gone/skills/gone-one ${SHA}`,
      `one-skill acme/one-skill . ${SHA}`,
      `pack-one acme/pack skills/pack-one ${PACK_PIN}`,
    ])
  );
  expect(skills.filter((each) => /^(kit-two|gone-two) /.test(each))).toEqual([]);
  const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
  expect(evidence["read times of acme/dots"]).toStartWith(
    `its plugin lists, installers and settings at ${DOTS_OLD} (the root's last prompt build ${ROOT_BUILD}), its pool directories at ${SHA}`
  );
  // context.ts reads the commit of a subagent's pool directories from this item's source by this
  // phrase: the two must not drift.
  expect(
    /\bits pool directories at ([0-9a-f]{7,40})\b/.exec(
      evidence["read times of acme/dots"] ?? ""
    )?.[1]
  ).toBe(SHA);
  const versionOf = (name: string) =>
    Object.entries(evidence).find(([item]) => item.startsWith(`version of package:${name} `))?.[1];
  expect(versionOf("@acme/kit")).toStartWith(
    `acme/dots:omp/plugins/package.json at ${DOTS_OLD} pins @acme/kit 1.0.0; its release tag kit-v1.0.0 (gh api repos/acme/kit/git/matching-refs/tags/kit-v1.0.0) is acme/kit at ${KIT_V1}`
  );
  expect(versionOf("@acme/gone")).toContain(
    "GitHub lists no tag gone-v3.0.0 of acme/kit (gh api repos/acme/kit/git/matching-refs/tags/gone-v3.0.0), so the registry's tarball (https://registry.npmjs.org/@acme/gone/-/gone-3.0.0.tgz) names the skills it ships (gone-one)"
  );
  // The same session as a load task's: its profile is written with the root skill's path ".",
  // and load reads it.
  const from = path.join(scratch, "npm-pins", "tasks.jsonl");
  writeFileSync(
    from,
    `${JSON.stringify({ id: "r1", text: "Read the alpha steps.", label: "need", source: "session", store: "store_a", pathHash: pathHash(sessionPath("pinned-subagent")) })}\n`
  );
  writeFileSync(`${from}.summary.json`, JSON.stringify({ command: "build-tasks", skill: "alpha" }));
  const dir = path.join(scratch, "npm-pins", "profiles");
  const args = ["build-tasks", "--config", config, "--copies", copies, "--count", countDir];
  const said: string[] = [];
  const tasksCode = await main([...args, "--reprofile", from, "--profiles-out", dir], io(said));
  expect({ tasksCode, said: tasksCode === EXIT.ok ? "" : said.join("") }).toEqual({
    tasksCode: EXIT.ok,
    said: "",
  });
  const written = loadProfile(path.join(dir, "r1.json"), "omp", "alpha").profile;
  expect(written.skills).toContainEqual({
    repo: "acme/one-skill",
    commit: SHA,
    path: ".",
    name: "one-skill",
  });
});

test("a dev-box profile installs the units its dev box's own installers install though the session read none of them, leaves out a skill its Oh My Pi config ignores, and installs a skill that hides itself as not listed", async () => {
  // The copies output of the suite, with acme/home named as the dev box's repository: it holds an
  // installer script and Claude Code's settings.
  const copies = path.join(setupDir, "copies-home");
  mkdirSync(copies, { recursive: true });
  for (const file of [
    "prefix-table.tsv",
    "installers.tsv",
    "loaders.tsv",
    "skill-copies.tsv",
    "repositories.tsv",
  ]) {
    writeFileSync(path.join(copies, file), readFileSync(path.join(copiesDir, file)));
  }
  writeFileSync(
    path.join(copies, "manifests.tsv"),
    `${readFileSync(path.join(copiesDir, "manifests.tsv"), "utf8")}${[
      ["acme/home", "installers/omp.sh", "installer-script", "tree", 4, "links skills", ""],
      ["acme/home", ".claude/settings.json", "claude-settings", "tree", 0, "none", ""],
    ]
      .map((row) => `${row.join("\t")}\n`)
      .join("")}`
  );
  const out = path.join(scratch, "devbox-home", "cases.jsonl");
  const sessions = sessionsFile("devbox-home.jsonl", ["good"]);
  const lines: string[] = [];
  expect(await main(buildArgs(sessions, out, "1", "--copies", copies), io(lines))).toBe(EXIT.ok);
  const [c] = loadCases(out, old);
  const names = (c?.profile.skills ?? []).map((skill) => `${skill.name} ${skill.repo}`);
  // The session read none of acme/home's skills; the dev box installs them all but dropped.
  expect(names).toEqual(
    expect.arrayContaining(["unread acme/home", "quiet acme/home", "shy acme/home"])
  );
  expect(names).not.toContain("dropped acme/home");
  const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
  expect(evidence["unit user-skills:omp (acme/home:installers/omp.sh)"]).toStartWith(
    "a dev box installs every unit the installers of its own repository install into Oh My Pi at the session's start, whether or not the session read from it"
  );
  expect(evidence["left out: skill dropped (acme/home skills/dropped)"]).toContain(
    "acme/home:omp/config.yml at "
  );
  expect(evidence["left out: skill dropped (acme/home skills/dropped)"]).toContain(
    "names dropped in skills.ignoredSkills"
  );
  // quiet and shy are installed, so skill:// reads them, and not listed; unread is listed.
  expect(evidence["not listed: skill quiet"]).toContain("disable-model-invocation: true");
  expect(evidence["not listed: skill shy"]).toContain("hide: true");
  expect(evidence["not listed: skill unread"]).toBeUndefined();
  const measure = ListingMeasure.parse(JSON.parse(evidence[LISTING_MEASURE] ?? "{}"));
  expect(measure.added).toContain("unread");
  expect(measure.added).not.toContain("quiet");
  expect(measure.added).not.toContain("shy");
  // A load task's profile of the same session: every copy of the tested source (acme/skills'
  // alpha) is left out, the home's alpha stays under its namespace, and the evidence says the
  // package installs the tested source again (`held once`). One task from the good session's
  // twin /sessions/task-4.jsonl (F1 as the good session has it), rebuilt by --reprofile.
  const from = path.join(scratch, "devbox-home", "tasks.jsonl");
  writeFileSync(
    from,
    `${JSON.stringify({
      id: "t1",
      text: TASK_ROWS[0]?.text,
      label: "need",
      source: "session",
      store: "store_a",
      pathHash: pathHash("/sessions/task-4.jsonl"),
    })}\n`
  );
  writeFileSync(`${from}.summary.json`, JSON.stringify({ command: "build-tasks", skill: "alpha" }));
  const dir = path.join(scratch, "devbox-home", "profiles");
  const args = ["build-tasks", "--config", config, "--copies", copies, "--count", countDir];
  expect(await main([...args, "--reprofile", from, "--profiles-out", dir], io([]))).toBe(EXIT.ok);
  const [task] = reprofileSummary(dir).profiles.tasks;
  expect(task?.outcome).toBe("changed");
  const held = task?.differs.added.find((item) => item.item.startsWith("held once: "));
  expect(held?.item).toBe("held once: skill alpha (acme/skills skills/alpha)");
  expect(held?.source).toStartWith(
    "package:@acme/pi-acme ships it (acme/skills:packages/pi-acme/package.json) too, the same copy; a host that had it there at another commit than the first install's lists that one as pi-acme/alpha (NAMESPACE_RULE)"
  );
  const profile = JSON.parse(readFileSync(path.resolve(dir, `${task?.task}.json`), "utf8"));
  const alphas = profile.skills.filter((skill: { name: string }) => skill.name === "alpha");
  expect(alphas).toEqual([
    { repo: "acme/home", commit: SHA, path: "skills/alpha", name: "alpha", namespace: "agent" },
  ]);
});

test("a Legion profile installs what its deployment gives each role at its start: the every-role plugin and every other plugin of the catalog its role setup names but those the project leaves out, and the skill its worker image installs from a release", async () => {
  // The suite's copies output, with acme/deploy as the deployment that holds ACME's project file.
  const copies = path.join(setupDir, "copies-deploy");
  mkdirSync(copies, { recursive: true });
  for (const file of [
    "prefix-table.tsv",
    "installers.tsv",
    "loaders.tsv",
    "skill-copies.tsv",
    "repositories.tsv",
  ]) {
    writeFileSync(path.join(copies, file), readFileSync(path.join(copiesDir, file)));
  }
  writeFileSync(
    path.join(copies, "manifests.tsv"),
    readFileSync(path.join(copiesDir, "manifests.tsv"), "utf8").replace(
      "acme/infra\tlegion/projects/10-ACME.yaml",
      "acme/deploy\tlegion/projects/10-ACME.yaml"
    )
  );
  const out = path.join(scratch, "legion-deploy", "cases.jsonl");
  const sessions = sessionsFile("legion-deploy.jsonl", ["legion"]);
  const lines: string[] = [];
  expect(await main(buildArgs(sessions, out, "1", "--copies", copies), io(lines))).toBe(EXIT.ok);
  const [c] = loadCases(out, old);
  const skills = (c?.profile.skills ?? []).map(
    (skill) => `${skill.name} ${skill.repo} ${skill.path} ${skill.commit}`
  );
  expect(skills).toEqual(
    expect.arrayContaining([
      `every-one acme/catalog plugins/every-tools/skills/every-one ${SHA}`,
      `alpha-one acme/catalog plugins/alpha-tools/skills/alpha-one ${SHA}`,
      `memo acme/rel skills/memo ${REL_PIN}`,
    ])
  );
  expect(skills.some((skill) => skill.startsWith("beta-one "))).toBe(false);
  const evidence = Object.fromEntries(c?.evidence?.map((each) => [each.item, each.source]) ?? []);
  expect(
    evidence["unit plugin:alpha-tools@acme-catalog (acme/catalog:.claude-plugin/marketplace.json)"]
  ).toContain(
    "legion/projects/10-ACME.yaml sets verificationSkillsLeaveOut [beta-tools], so each role installs every plugin of the catalog but those, and the every-role plugins (every-tools)"
  );
  expect(evidence["unit image:memo (acme/deploy:legion/worker/Dockerfile)"]).toContain(
    "installs it in the worker image"
  );
  expect(evidence[`commit ${REL_PIN} of acme/rel`]).toStartWith(
    "the last commit of v1.0.0, the ref acme/deploy:legion/worker/Dockerfile pins"
  );
  // The catalog's alpha differs from the alpha the session read: it is installed too, under its
  // plugin's namespace.
  expect(c?.profile.skills).toContainEqual({
    repo: "acme/catalog",
    commit: SHA,
    path: "plugins/alpha-tools/skills/alpha",
    name: "alpha",
    namespace: "alpha-tools",
  });
  expect(
    evidence["skill alpha-tools/alpha (acme/catalog plugins/alpha-tools/skills/alpha)"]
  ).toContain("and this copy's text differs");
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
  // The two seeds give two orders, so the runs above can tell whether the seed is read.
  expect(expected("12")).not.toEqual(expected("11"));
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

test("build-cases refuses an --out inside a git or jj checkout, its own and --reprofile's, before any read", async () => {
  const from = recorder.requests.length;
  const logLength = () => (existsSync(psqlLog) ? readFileSync(psqlLog, "utf8").length : 0);
  const logged = logLength();
  const calls = ghCalls.length;
  const sessions = sessionsFile("checkout.jsonl", ["good"]);
  for (const marker of [".git", ".jj"]) {
    const checkout = path.join(scratch, `checkout${marker}`);
    mkdirSync(path.join(checkout, marker), { recursive: true });
    const out = path.join(checkout, "cases", "cases.jsonl");
    const reprofile = [
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
      sessions,
      "--out",
      out,
    ];
    for (const args of [buildArgs(sessions, out, "1"), reprofile]) {
      const lines: string[] = [];
      expect(await main(args, io(lines))).toBe(EXIT.refused);
      expect(lines.join("\n")).toContain(`is inside the checkout ${checkout}`);
      expect(existsSync(out)).toBe(false);
      expect(existsSync(`${out}.spool`)).toBe(false);
    }
  }
  expect(recorder.requests.length).toBe(from);
  expect(logLength()).toBe(logged);
  expect(ghCalls.length).toBe(calls);
});

/** The made-up session that reads alpha from the skill set alpha-set: F1 with its reads of alpha
 *  moved to a dev box's Oh My Pi plugins directory, which holds acme/alpha-set under its
 *  dependency's name. setTarget's store holds it; SESSIONS does not. */
const SET_SESSION = "set-read";
const SET_ALPHA_DIR = "/srv/dev/.dotfiles/omp/plugins/node_modules/alpha-set/skills/alpha";

/** The configuration and copies output of a build whose target is the skill set alpha-set
 *  (acme/alpha-set), made once: the configuration names the set and reads the suite's store with
 *  SET_SESSION added; the copies output is the suite's with the set's package added as copies.ts
 *  gives a `github:` dependency's (its prefix row under the dependency's name, its installers row
 *  and its copy, read at the tag a plugins list pins). */
let setFiles: { config: string; copies: string } | undefined;
function setTarget(): { config: string; copies: string } {
  if (setFiles !== undefined) return setFiles;
  const lines = f1Lines();
  setText(lines, 5, () => `Step 3 (case ${SET_SESSION}).`);
  readFrom(SET_ALPHA_DIR)(lines);
  const store = changedStore("set-target", (files) => {
    (files.store_a ?? {})[sessionPath(SET_SESSION)] = lines
      .map((line) => `${JSON.stringify(line)}\n`)
      .join("");
  });
  const setConfig = path.join(setupDir, "set-target", "skill-cuts-sets.json");
  writeFileSync(
    setConfig,
    JSON.stringify({
      ...JSON.parse(readFileSync(store, "utf8")),
      sets: [{ id: "alpha-set", repo: "acme/alpha-set" }],
      dotfilesRepo: "acme/dots",
    })
  );
  const copies = path.join(setupDir, "copies-set");
  mkdirSync(copies, { recursive: true });
  const added: Readonly<Record<string, readonly (readonly (string | number)[])[]>> = {
    "prefix-table.tsv": [
      [
        "**/node_modules/alpha-set/skills/alpha/",
        "acme/alpha-set",
        "skills/alpha",
        "alpha",
        "package:alpha-set (acme/alpha-set:package.json)",
      ],
    ],
    "installers.tsv": [
      [
        "acme/alpha-set",
        "package.json",
        "omp-package",
        "ships",
        "package:alpha-set",
        "omp",
        "alpha",
        "acme/alpha-set",
        "skills/alpha",
        "",
      ],
    ],
    "repositories.tsv": [["acme/alpha-set", "v1.0.0", SHA, "no", "no", "", 3, "whole", 1, ""]],
    "skill-copies.tsv": [["acme/alpha-set", "skills/alpha", "alpha", 10, "no", "no", "", "", ""]],
  };
  for (const file of [
    "installers.tsv",
    "loaders.tsv",
    "manifests.tsv",
    "prefix-table.tsv",
    "repositories.tsv",
    "skill-copies.tsv",
  ]) {
    const rows = (added[file] ?? []).map((row) => `${row.join("\t")}\n`).join("");
    writeFileSync(
      path.join(copies, file),
      `${readFileSync(path.join(copiesDir, file), "utf8")}${rows}`
    );
  }
  setFiles = { config: setConfig, copies };
  return setFiles;
}

/** The arguments of a build-cases run of `sessions` into `out` whose target is the set alpha-set
 *  (setTarget), given as `--set`. */
function setArgs(sessions: string, out: string): string[] {
  const target = setTarget();
  const swap: Readonly<Record<string, string>> = {
    "--cut": "--set",
    "skills-1": "alpha-set",
    [config]: target.config,
    [copiesDir]: target.copies,
  };
  return buildArgs(sessions, out, "1").map((arg) => swap[arg] ?? arg);
}

test("build-cases and --reprofile take a set where they take a cut", async () => {
  const sessions = sessionsFile("set-cases.jsonl", [SET_SESSION]);
  const out = path.join(scratch, "set-cases", "cases.jsonl");
  const built: string[] = [];
  expect(await main(setArgs(sessions, out), io(built))).toBe(EXIT.ok);
  const [c] = loadCases(out, old);
  // The case's cut field holds the set's id, and so do the summary and its inputs.
  expect(c?.cut).toBe("alpha-set");
  expect(c?.id).toStartWith("alpha-set-");
  const summary = JSON.parse(readFileSync(`${out}.summary.json`, "utf8"));
  expect([summary.cut, summary.inputs.cut]).toEqual(["alpha-set", "alpha-set"]);

  // --reprofile finds the case's set by its cut field, and builds its profile again.
  const target = setTarget();
  const reprofile = [
    "build-cases",
    "--config",
    target.config,
    "--old",
    old,
    "--copies",
    target.copies,
    "--count",
    countDir,
    "--reprofile",
    out,
  ];
  const fresh = path.join(scratch, "set-cases", "reprofiled.jsonl");
  const said: string[] = [];
  expect(await main([...reprofile, "--out", fresh], io(said))).toBe(EXIT.ok);
  expect(said.join("")).not.toContain("left out");
  const [again] = loadCases(fresh, old);
  expect(again?.cut).toBe("alpha-set");
  expect(again?.profile.skills).toEqual(c?.profile.skills ?? []);

  // One of --cut and --set is taken: both, neither, an id of the other kind or of no set, and
  // --set with --reprofile are refused before any read.
  const from = recorder.requests.length;
  const logged = psqlLogLength();
  const calls = ghCalls.length;
  const refused = path.join(scratch, "set-refused", "cases.jsonl");
  const swapped = (args: string[], was: string, now: string) =>
    args.map((arg) => (arg === was ? now : arg));
  const rows: [string[], string][] = [
    [
      [...setArgs(sessions, refused), "--cut", "skills-1"],
      "--cut skills-1 and --set alpha-set are both given: give one of the two",
    ],
    [
      setArgs(sessions, refused).filter(
        (arg, at, args) => arg !== "--set" && args[at - 1] !== "--set"
      ),
      "--cut or --set required",
    ],
    [
      swapped(setArgs(sessions, refused), "alpha-set", "skills-1"),
      "--set skills-1 is no set of the configuration (alpha-set)",
    ],
    [
      swapped(setArgs(sessions, refused), "--set", "--cut"),
      "--cut alpha-set is no cut of the configuration (skills-1)",
    ],
    [
      swapped(
        swapped(buildArgs(sessions, refused, "1"), "--cut", "--set"),
        "skills-1",
        "alpha-set"
      ),
      "--set alpha-set is no set of the configuration (none)",
    ],
    [[...reprofile, "--out", refused, "--set", "alpha-set"], "so it takes no --set"],
  ];
  for (const [args, why] of rows) {
    const lines: string[] = [];
    expect(await main(args, io(lines))).toBe(EXIT.refused);
    expect(lines.join("\n")).toContain(why);
  }
  expect(existsSync(refused)).toBe(false);
  expect(recorder.requests.length).toBe(from);
  expect(psqlLogLength()).toBe(logged);
  expect(ghCalls.length).toBe(calls);
}, 60_000);

test("a set target keeps a read placed at the set's copy and drops a Legion fork's read as not-cut-copy", async () => {
  const sessions = sessionsFile("set-target.jsonl", [SET_SESSION, "legion"]);
  const out = path.join(scratch, "set-target", "cases.jsonl");
  const lines: string[] = [];
  expect(await main(setArgs(sessions, out), io(lines))).toBe(EXIT.ok);
  const cases = loadCases(out, old);
  expect(cases.map((c) => c.source.pathHash)).toEqual([pathHash(sessionPath(SET_SESSION))]);
  // The profile installs the copy the session read: the set's alpha, not acme/skills's.
  expect(cases[0]?.profile.skills.filter((skill) => skill.name === "alpha")).toEqual([
    { repo: "acme/alpha-set", commit: SHA, path: "skills/alpha", name: "alpha" },
  ]);
  // The legion session read alpha from the package the Legion image carries: acme/skills's fork
  // of the skill, which the prefix table places at acme/skills's copy.
  const record = summaryOf(out).sessions.find(
    (each) => each.hash === fileHash(sessionPath("legion"))
  );
  expect(record).toMatchObject({
    reason: "not-cut-copy",
    detail:
      "the prefix table rule places /opt/legion/pi-acme/dist/skills/alpha/SKILL.md at acme/skills skills/alpha",
    readerUsd: 0,
  });
}, 60_000);

// build-cases --written: cases of a skill from written tasks, each in a recorded session's context.

/** The arguments of one `build-cases --written 3` run of alpha, and `extra`. */
function writtenArgs(out: string, ...extra: string[]): string[] {
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
    "--copies",
    copiesDir,
    "--count",
    countDir,
    "--written",
    "3",
    "--out",
    out,
    ...extra,
  ];
}

/** A cases file of one session case of alpha: F1's `task` step, which LINE governs. */
function sessionCasesFile(): string {
  const messages = f1Messages();
  const file = path.join(setupDir, "session-cases.jsonl");
  const c = {
    id: "skills-1-f1",
    cut: "skills-1",
    skill: "alpha",
    baseFile: "SKILL.md",
    line: { text: LINE, lineNumber: 9, lineCount: 10 },
    criterion: CRITERION,
    model: "anthropic/claude-sonnet-5-5",
    thinking: "off",
    context: "devbox",
    profile: {
      skills: [{ repo: "acme/skills", commit: SHA, path: "skills/alpha", name: "alpha" }],
      plugins: [],
      contextFiles: [],
    },
    messages: messages.slice(0, 7),
    skillReads: [2, 4],
    realStep: messages[7],
    violatingStep: { role: "assistant", content: [{ type: "text", text: "Step 4." }] },
    source: { store: "store_a", pathHash: pathHash("/sessions/f1.jsonl"), kind: "session" },
    sessionStart: "2026-11-20T01:59:41.780Z",
    secretsRemoved: 0,
    sessionDigest: null,
  };
  writeFileSync(file, `${JSON.stringify(c)}\n`);
  return file;
}

/** A written build's summary, as the tests read it. */
interface WrittenSummary {
  command: string;
  contextSession: {
    store: string;
    hash: string;
    pathHash: string;
    how: string;
    passed: { store: string; hash: string; why: string }[];
  };
  model: { model: string; how: string };
  casesBuilt: number;
  drops: Partial<Record<DropReason, number>>;
  tasks: { task: number; outcome: string; reason?: DropReason; detail?: string }[];
  stopped: string | null;
  peakRssBytes?: number;
}
const writtenSummaryOf = (out: string): WrittenSummary =>
  JSON.parse(readFileSync(`${out}.summary.json`, "utf8"));

/** The user text of each request whose system prompt starts `prompt`. */
const usersOf = (requests: readonly RecordedRequest[], prompt: string): string[] =>
  requests.flatMap((request) => {
    const body = JSON.parse(request.body) as {
      system: { text: string }[];
      messages: { content: { text: string }[] }[];
    };
    return body.system[0]?.text.startsWith(prompt) === true
      ? [body.messages[0]?.content.map((part) => part.text).join("\n") ?? ""]
      : [];
  });

describe("build-cases --written", () => {
  const out = path.join(scratch, "written", "cases.jsonl");
  const printed: string[] = [];
  const contextHash = fileHash(sessionPath("good"));
  const id = `skills-1-${pathHash(sessionPath("good")).slice(0, 12)}-w1`;
  let code = -1;
  let requests: RecordedRequest[] = [];
  beforeAll(async () => {
    const from = recorder.requests.length;
    code = await main(
      writtenArgs(
        out,
        "--context-session",
        `store_a:${contextHash}`,
        "--session-cases",
        sessionCasesFile()
      ),
      io(printed)
    );
    requests = recorder.requests.slice(from);
  }, 60_000);

  test("a written case has the task, the read and its result, and source.kind written", () => {
    expect({ code, failed: printed.join("").includes("failed") }).toEqual({
      code: 0,
      failed: false,
    });
    const messages = f1Messages();
    // The reader read the base text, the session case's step and criterion, the context session's
    // first user message and the count.
    const [asked] = usersOf(requests, WRITTEN_PROMPT);
    expect(usersOf(requests, WRITTEN_PROMPT)).toHaveLength(1);
    expect(asked).toContain(`9: ${LINE}`);
    expect(asked).toContain(
      `<session-cases>\n<case n="1">\n<step>\n${stepText(messages[7]?.content ?? [])}\n</step>\n<criterion>\nPass: ${CRITERION.pass}\nFail: ${CRITERION.fail}\n</criterion>`
    );
    expect(asked).toContain(
      `<first-message>\n${messages[0]?.content?.[0]?.text}\n</first-message>\n\n<count>3</count>`
    );

    const cases = loadCases(out, old);
    expect(cases.map((each) => each.id)).toEqual([id]);
    const [c] = cases;
    expect(c?.source).toEqual({
      store: "store_a",
      pathHash: pathHash(sessionPath("good")),
      kind: "written",
    });
    expect(c === undefined ? undefined : caseKind(c)).toBe("written");
    // The task as the user's message, a read of skill://alpha, and its result: the old text at the
    // path the context session's own read of alpha showed, as F1's read result holds it.
    expect(c?.messages).toHaveLength(3);
    expect(c?.messages[0]).toEqual({
      role: "user",
      content: [{ type: "text", text: WRITTEN_TASK.task }],
    });
    expect(c?.messages[1]).toMatchObject({
      role: "assistant",
      content: [
        {
          type: "toolCall",
          id: "written_read",
          name: "read",
          arguments: { path: "skill://alpha" },
        },
      ],
    });
    expect(c?.messages[1]?.content).toHaveLength(1);
    expect(c?.messages[2]?.content).toEqual(messages[2]?.content);
    expect(c?.messages[2]).toMatchObject({ role: "toolResult", toolCallId: "written_read" });
    expect(c?.skillReads).toEqual([2]);
    // The reader's following step is the real step; its violating step, the violating one.
    expect(c?.realStep).toEqual({
      role: "assistant",
      content: [
        { type: "toolCall", id: "following_1", ...WRITTEN_TASK.followingStep.toolCalls[0] },
      ],
    });
    expect(c?.violatingStep).toEqual({
      role: "assistant",
      content: [
        { type: "toolCall", id: "violating_1", ...WRITTEN_TASK.violatingStep.toolCalls[0] },
      ],
    });
    expect([c?.baseFile, c?.line, c?.criterion]).toEqual([
      "SKILL.md",
      { text: LINE, lineNumber: 9, lineCount: 10 },
      CRITERION,
    ]);
    // The context session's model, thinking level, context, start and digest at its read of the
    // skill, and its profile, as a session case of it has them.
    expect([c?.model, c?.thinking, c?.context, c?.sessionStart, c?.sessionDigest]).toEqual([
      "anthropic/claude-sonnet-5-5",
      "off",
      "devbox",
      "2026-11-20T01:59:41.780Z",
      "46874d907e557998",
    ]);
    expect(c?.profile.skills.filter((skill) => skill.name === "alpha")).toEqual([
      { repo: "acme/skills", commit: SHA, path: "skills/alpha", name: "alpha" },
    ]);
    const items = (c?.evidence ?? []).map((each) => each.item);
    expect(items).toContain(
      `context session read: message 2 reads [Skill file: ${F1_SKILLS}/alpha/SKILL.md]`
    );
    expect(items).toContain("audit of the real step: follows");
    expect(items).toContain(LISTING_MEASURE);
    expect(items).toContain(`written case: task 1 of the written-case reader`);
    expect(items).toContain(`written case: context session store_a:${contextHash}`);
    expect(items).toContain("written case: model anthropic/claude-sonnet-5-5");

    const summary = writtenSummaryOf(out);
    expect(summary).toMatchObject({
      command: "build-cases --written",
      contextSession: {
        store: "store_a",
        hash: contextHash,
        pathHash: pathHash(sessionPath("good")),
        how: "--context-session",
        passed: [],
      },
      model: { model: "anthropic/claude-sonnet-5-5", how: "the context session's own" },
      casesBuilt: 1,
    });
  });

  test("a written case whose task restates the line or whose following step the audit does not call follows is dropped", () => {
    const summary = writtenSummaryOf(out);
    expect(summary.tasks.map((each) => [each.task, each.outcome, each.reason ?? null])).toEqual([
      [1, `case ${id}`, null],
      [2, "dropped", "written-restates"],
      [3, "dropped", "written-not-followed"],
    ]);
    expect(summary.drops).toEqual({ "written-restates": 1, "written-not-followed": 1 });
    expect(summary.tasks[1]?.detail).toContain(RESTATED);
    expect(summary.tasks[2]?.detail).toBe("breaks: The step spreads the result over two lines.");
    // The restatement check read each task and its read call, never the skill's text.
    const restates = usersOf(requests, RESTATE_PROMPT);
    expect(restates).toHaveLength(3);
    for (const [n, user] of restates.entries()) {
      expect(user).toStartWith(
        `<line>\n${LINE}\n</line>\n\n<messages>\n<message index="0" role="user">\n[user]\n${WRITTEN_TASKS[n]?.task}\n</message>`
      );
      expect(user).not.toContain("3. Stop after the third step.");
    }
    // The audit read the line, the history as the judge reads it and the following step: only
    // for the tasks that do not restate the line.
    const audits = usersOf(requests, AUDIT_PROMPT);
    expect(audits).toHaveLength(2);
    expect(audits[0]).toBe(
      `<line>\n${LINE}\n</line>\n\n<before>\n[user]\n${WRITTEN_TASK.task}\n\n[assistant]\n[tool call: read] {"path":"skill://alpha"}\n\n[tool result: read]\n[the text of a file of the skill, left out]\n</before>\n\n<step>\n[tool call: write] ${JSON.stringify(WRITTEN_TASK.followingStep.toolCalls[0]?.arguments)}\n</step>`
    );
  });

  test("--reprofile keeps source.kind", async () => {
    const fresh = path.join(scratch, "written", "reprofiled.jsonl");
    const lines: string[] = [];
    const args = [
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
      out,
      "--out",
      fresh,
    ];
    expect(await main(args, io(lines))).toBe(EXIT.ok);
    const [before] = loadCases(out, old);
    const [after] = loadCases(fresh, old);
    expect(after?.source).toEqual(before?.source);
    expect(after === undefined ? undefined : caseKind(after)).toBe("written");
    // Its profile is built again from the context session's reads its evidence names, which its
    // messages do not hold, so it is the profile the build gave; the written case's evidence stays.
    expect(after?.profile).toEqual(before?.profile);
    const written = (c: Case | undefined) =>
      (c?.evidence ?? []).filter((each) =>
        /^(written case|context session read): /.test(each.item)
      );
    expect(written(after)).toEqual(written(before));
    expect(written(after).length).toBeGreaterThan(0);
  }, 60_000);

  test("a written build under the memory limit records its peak resident memory in the summary and prints it in its last line", () => {
    expect(writtenSummaryOf(out).stopped).toBeNull();
    expect(writtenSummaryOf(out).peakRssBytes).toBe(PEAK_BYTES);
    expect(printed.at(-1)).toContain(
      `of readers at list price, peak memory 200 MiB; written to ${out} and ${out}.summary.json`
    );
  });
});

test("the context session is the first served reading session whose rebuilt profile lists the skill", async () => {
  // not-served runs on a model no route serves; other-copy read acme/other's alpha, so its profile
  // installs no alpha of the cut's repository; good and openai are on served models, and list it.
  const names = ["not-served", "other-copy", "good", "openai"];
  const sessions = sessionsFile("written-candidates.jsonl", names);
  const candidates = names.map((name) => ({
    store: "store_a",
    hash: fileHash(sessionPath(name)),
    path: sessionPath(name),
  }));
  const orderOf = (seed: string) => blindOrder(candidates, seed).map(({ item }) => item.path);
  // A seed whose order puts not-served and other-copy first.
  const seed = Array.from({ length: 500 }, (_, n) => String(n)).find((each) => {
    const order = orderOf(each);
    return order[0] === sessionPath("not-served") && order[1] === sessionPath("other-copy");
  });
  expect(seed).toBeDefined();
  const chosen = orderOf(seed ?? "")[2] ?? "";
  const out = path.join(scratch, "written-seeded", "cases.jsonl");
  const lines: string[] = [];
  expect(
    await main(writtenArgs(out, "--sessions", sessions, "--seed", seed ?? ""), io(lines))
  ).toBe(EXIT.ok);
  const summary = writtenSummaryOf(out);
  expect(summary.contextSession).toMatchObject({
    store: "store_a",
    hash: fileHash(chosen),
    pathHash: pathHash(chosen),
  });
  expect(summary.contextSession.how).toContain("place 3 of 4");
  expect(summary.contextSession.passed.map((each) => [each.hash, each.why.split(":")[0]])).toEqual([
    [fileHash(sessionPath("not-served")), "model-not-served"],
    [fileHash(sessionPath("other-copy")), "not-cut-copy"],
  ]);
  const cases = loadCases(out, old);
  expect(cases.length).toBeGreaterThan(0);
  expect(cases.every((each) => each.source.pathHash === pathHash(chosen))).toBe(true);
  expect(summary.model.how).toBe("the context session's own");

  // With no served session whose profile lists the skill, the first one whose profile lists it,
  // on the served model with the most reading sessions of the skill (--reading-models).
  const unserved = sessionsFile("written-unserved.jsonl", ["other-copy", "not-served"]);
  const counted = (byModel: { model: string; served: boolean; sessions: number }[]) => ({
    sessions: byModel.reduce((sum, each) => sum + each.sessions, 0),
    byModel: byModel.map((each) => ({ ...each, anthropic: each.model.includes("claude-") })),
    noModel: 0,
    notQueried: 0,
    notAnthropic: 0,
    served: byModel.filter((each) => each.served).reduce((sum, each) => sum + each.sessions, 0),
    withModel: byModel.reduce((sum, each) => sum + each.sessions, 0),
  });
  const modelsFile = (name: string, byModel: Parameters<typeof counted>[0]) => {
    const file = path.join(setupDir, name);
    writeFileSync(file, JSON.stringify(counted(byModel)));
    return file;
  };
  const astra = { model: "openai-codex/gpt-6-astra", served: false, sessions: 4 };
  const skill = modelsFile("written-alpha-models.json", [
    astra,
    { model: "openai/gpt-6.1-sol", served: true, sessions: 2 },
    { model: "anthropic/claude-sonnet-5-5", served: true, sessions: 1 },
  ]);
  const fromSkill = path.join(scratch, "written-unserved", "cases.jsonl");
  const args = (to: string, ...extra: string[]) =>
    writtenArgs(to, "--sessions", unserved, "--seed", "1", ...extra);
  expect(await main(args(fromSkill, "--reading-models", skill), io(lines))).toBe(EXIT.ok);
  const [first] = loadCases(fromSkill, old);
  expect(first?.source.pathHash).toBe(pathHash(sessionPath("not-served")));
  expect(first?.model).toBe("openai/gpt-6.1-sol");
  expect(writtenSummaryOf(fromSkill).contextSession.how).toContain(
    "no reading session on a served model"
  );

  // With no served model in the skill's file, the served model with the most reading sessions over
  // the pool's files (the set's skills' models files).
  const none = modelsFile("written-none-models.json", [astra]);
  const poolA = modelsFile("written-pool-a.json", [
    { model: "openai/gpt-6.1-sol", served: true, sessions: 2 },
    { model: "anthropic/claude-opus-5-5", served: true, sessions: 2 },
  ]);
  const poolB = modelsFile("written-pool-b.json", [
    astra,
    { model: "anthropic/claude-opus-5-5", served: true, sessions: 1 },
  ]);
  const fromPool = path.join(scratch, "written-pool", "cases.jsonl");
  const pooled = args(
    fromPool,
    "--reading-models",
    none,
    "--model-pool",
    poolA,
    "--model-pool",
    poolB
  );
  expect(await main(pooled, io(lines))).toBe(EXIT.ok);
  expect(loadCases(fromPool, old)[0]?.model).toBe("anthropic/claude-opus-5-5");
  expect(writtenSummaryOf(fromPool).model.how).toContain("--model-pool");

  // With no served model anywhere, the build fails, naming why, and writes no case.
  const failed: string[] = [];
  const nowhere = path.join(scratch, "written-nowhere", "cases.jsonl");
  expect(await main(args(nowhere, "--reading-models", none), io(failed))).toBe(EXIT.failed);
  expect(failed.join("")).toContain("no served model for the written cases");
  expect(existsSync(nowhere)).toBe(false);
}, 120_000);

// Catches: a --context-session build given --sessions or --seed, which it never reads, that then
// records their digest and seed in its summary as if it had.
test("build-cases --written with --context-session refuses --sessions and --seed, before any read", async () => {
  const sessions = sessionsFile("written-both.jsonl", ["good"]);
  const context = ["--context-session", `store_a:${fileHash(sessionPath("good"))}`];
  const out = path.join(scratch, "written-both", "cases.jsonl");
  const from = recorder.requests.length;
  const logged = psqlLogLength();
  const calls = ghCalls.length;
  const rows: [string[], string][] = [
    [["--sessions", sessions], "--sessions"],
    [["--seed", "1"], "--seed"],
    [["--sessions", sessions, "--seed", "1"], "--sessions"],
  ];
  for (const [extra, flag] of rows) {
    const lines: string[] = [];
    expect(await main(writtenArgs(out, ...context, ...extra), io(lines))).toBe(EXIT.refused);
    expect(lines.join("\n")).toContain(`--written takes no ${flag} with --context-session`);
  }
  expect(existsSync(out)).toBe(false);
  expect(recorder.requests.length).toBe(from);
  expect(psqlLogLength()).toBe(logged);
  expect(ghCalls.length).toBe(calls);
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
  // The need task whose first step the skill does not govern is left out, with its reason.
  expect(summary.droppedNeed).toBe(1);
  expect(summary.dropped).toEqual([
    {
      source: "written",
      text: "A made-up need task 5.",
      why: "made up",
      reason: "the notes are read first",
    },
  ]);
  expect(summary.tasks[0].firstStep).toEqual(GOVERNS.firstStep);
}, 30_000);

test("build-tasks --model offers only the first user messages of sessions whose first request ran on that model, drops and counts each other session as other-model, and the summary names the model", async () => {
  const out = path.join(scratch, "tasks-model", "tasks.jsonl");
  const args = (model: string) => [
    "build-tasks",
    "--config",
    config,
    "--key-file",
    keyFile,
    "--skill",
    old,
    "--store",
    "store_a",
    "--out",
    out,
    "--model",
    model,
  ];
  const lines: string[] = [];
  expect(await main(args("openai/gpt-6.1-sol"), io(lines))).toBe(EXIT.ok);
  // task-3's session ran on openai/gpt-6.1-sol and is offered; task-1's and task-4's, of the other
  // text, ran on F1's model, and each is dropped.
  expect(tasksOf(out).filter((task) => task.source === "session")).toEqual([
    {
      id: "t1",
      text: "Write the Alpha report of step 2.",
      label: "need",
      source: "session",
      store: "store_a",
      pathHash: pathHash("/sessions/task-3.jsonl"),
    },
  ]);
  const summary = JSON.parse(readFileSync(`${out}.summary.json`, "utf8"));
  expect(summary.model).toBe("openai/gpt-6.1-sol");
  expect(summary.inputs.model).toBe("openai/gpt-6.1-sol");
  expect(summary.messagesOffered).toBe(1);
  expect(summary.candidateDrops.counts).toEqual({ "other-model": 2 });
  expect(summary.candidateDrops.sessions).toEqual(
    ["/sessions/task-1.jsonl", "/sessions/task-4.jsonl"].map((file) => ({
      store: "store_a",
      pathHash: pathHash(file),
      reason: "other-model",
      detail: "its first reply ran on anthropic/claude-sonnet-5-5, not openai/gpt-6.1-sol",
    }))
  );
  expect(lines.join("")).toContain(
    "2 candidate sessions dropped (2 other-model, not on openai/gpt-6.1-sol)"
  );
  expect(existsSync(`${out}.spool`)).toBe(false);
  // The model is one of the inputs: a run that goes on with another is refused.
  const other: string[] = [];
  expect(await main(args("anthropic/claude-sonnet-5-5"), io(other))).toBe(EXIT.refused);
  expect(other.join("\n")).toContain(
    "--model was openai/gpt-6.1-sol, now anthropic/claude-sonnet-5-5"
  );
}, 60_000);

/** The lines of the stand-in store's log since `from` characters, parsed. */
const psqlLines = (from: number) =>
  readFileSync(psqlLog, "utf8")
    .slice(from)
    .split("\n")
    .filter((line) => line !== "")
    .map(
      (line) => JSON.parse(line) as { db: string; kind: string; rows: number; after?: number[] }
    );

/** The characters of the stand-in store's log: none before its first call. */
const psqlLogLength = () => (existsSync(psqlLog) ? readFileSync(psqlLog, "utf8").length : 0);

/** The transcripts the stand-in store started to pull since `from` characters of its log: one
 *  pull call from the place before every row (PULL_START) for each. */
const transcriptPulls = (from: number) =>
  psqlLines(from).filter((line) => line.kind === "pull" && line.after?.[0] === -2).length;

/** A clock for `BuildIo.now` that moves one minute with each transcript the stand-in store starts
 *  to pull, so a search's own time is the same in every run. */
const pullClock = () => transcriptPulls(0) * 60_000;

/** `summary` with each dollar figure of its spend rounded, since a run that goes on adds its
 *  spend to the earlier run's, which can differ from one running sum in the last bit. */
function roundedSpend(summary: Record<string, unknown>): Record<string, unknown> {
  const spend = summary.readerSpend as { usd: number; models: Record<string, { usd: number }> };
  const round = (usd: number) => Number(usd.toFixed(9));
  return {
    ...summary,
    readerSpend: {
      usd: round(spend.usd),
      models: Object.fromEntries(
        Object.entries(spend.models).map(([model, each]) => [
          model,
          { ...each, usd: round(each.usd) },
        ])
      ),
    },
  };
}

/** An io whose `out` throws once `after` sessions are decided, as a kill after them would end the
 *  run: the next session's work is done, and its outcome is not written. */
function killedAfter(after: number, lines: string[]): BuildIo {
  let decided = 0;
  return {
    ...io(lines),
    out: (text) => {
      lines.push(text);
      if (/^build-cases: \d+\/\d+ /.test(text) && ++decided > after) throw new Error("killed");
    },
  };
}

describe("build-cases goes on from where a run stopped", () => {
  const names = [
    "good",
    "quoted",
    "secret",
    "legion",
    "no-step",
    "read-wins",
    "unshipped",
    "openai",
  ];
  const once = path.join(scratch, "resume", "once", "cases.jsonl");
  const killed = path.join(scratch, "resume", "killed", "cases.jsonl");
  let sessions = "";
  const lines: string[] = [];
  let resumedRequests: RecordedRequest[] = [];
  let resumedPulls: { kind: string; rows: number }[] = [];
  const codes: number[] = [];
  beforeAll(async () => {
    sessions = sessionsFile("resume.jsonl", names);
    const args = (out: string) => buildArgs(sessions, out, "5", "--concurrency", "1");
    codes.push(await main(args(once), io([])));
    codes.push(await main(args(killed), killedAfter(3, lines)));
    const from = recorder.requests.length;
    const logged = readFileSync(psqlLog, "utf8").length;
    codes.push(await main(args(killed), io(lines)));
    resumedRequests = recorder.requests.slice(from);
    resumedPulls = psqlLines(logged);
  }, 120_000);

  test("a build killed after 3 sessions and run again gives the cases and summary of one uninterrupted run, with no pull and no reader call for those 3", () => {
    expect(codes).toEqual([0, 1, 0]);
    expect(lines.join("")).toContain("going on from");
    expect(readFileSync(killed, "utf8")).toBe(readFileSync(once, "utf8"));
    const summary = (out: string) =>
      roundedSpend(
        JSON.parse(readFileSync(`${out}.summary.json`, "utf8")) as Record<string, unknown>
      );
    expect(summary(killed)).toEqual(summary(once));
    // The first 3 sessions of the seeded order were decided before the kill.
    const order = summaryOf(once).order.map(
      (each) => names.find((name) => fileHash(sessionPath(name)) === each.hash) ?? ""
    );
    for (const name of order.slice(0, 3)) {
      expect(resumedRequests.some((request) => request.body.includes(`(case ${name})`))).toBe(
        false
      );
    }
    expect(resumedRequests.some((request) => request.body.includes(`(case ${order[3]})`))).toBe(
      true
    );
    const later = summaryOf(once).sessions.slice(3);
    expect(resumedPulls.filter((each) => each.kind === "pull")).toHaveLength(
      later.reduce((total, each) => total + (each.pulls ?? 0), 0)
    );
  });

  test("a run that goes on from other inputs is refused, naming each difference, and leaves the files as they were", async () => {
    const before = readFileSync(`${killed}.summary.json`, "utf8");
    const seedRefused: string[] = [];
    expect(await main(buildArgs(sessions, killed, "6"), io(seedRefused))).toBe(2);
    expect(seedRefused.join("\n")).toContain("--seed was 5, now 6");
    const other = sessionsFile("resume-other.jsonl", names.slice(1));
    const sessionsRefused: string[] = [];
    expect(await main(buildArgs(other, killed, "5"), io(sessionsRefused))).toBe(2);
    expect(sessionsRefused.join("\n")).toContain(`--sessions was ${sessions}`);
    expect(sessionsRefused.join("\n")).toContain("holds a build of other inputs");
    expect(readFileSync(`${killed}.summary.json`, "utf8")).toBe(before);
  });
});

test("--concurrency 3 keeps the same cases, in the same order, as one session at a time", async () => {
  const names = [
    "good",
    "quoted",
    "secret",
    "legion",
    "no-step",
    "read-wins",
    "openai",
    "subagent",
  ];
  const sessions = sessionsFile("concurrency.jsonl", names);
  const outs = [1, 3].map((n) => path.join(scratch, `concurrency-${n}`, "cases.jsonl"));
  for (const [index, n] of [1, 3].entries()) {
    const out = outs[index] ?? "";
    expect(
      await main(buildArgs(sessions, out, "9", "--max", "3", "--concurrency", `${n}`), io([]))
    ).toBe(0);
  }
  const [one, three] = outs.map((out) => summaryOf(out));
  expect(readFileSync(outs[1] ?? "", "utf8")).toBe(readFileSync(outs[0] ?? "", "utf8"));
  expect(three?.order).toEqual(one?.order ?? []);
  expect(three?.casesBuilt).toBe(3);
  // The sessions decided are the same; a session still open when the third case was decided is
  // recorded as not needed, after them.
  const decided = (summary: Summary | undefined) =>
    (summary?.sessions ?? []).filter((each) => each.outcome !== "not-needed");
  expect(decided(three)).toEqual(decided(one));
  const notNeeded = (three?.sessions ?? []).slice(decided(three).length);
  expect(notNeeded.every((each) => each.outcome === "not-needed")).toBe(true);
  expect(notNeeded.length).toBeLessThanOrEqual(2);
}, 120_000);

test("build-tasks killed after its first query and run again gives the tasks and summary of one uninterrupted run, with no terms call and only the query left", async () => {
  const skillDir = path.join(setupDir, "old", "alpha");
  const args = (out: string) => [
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
  ];
  const once = path.join(scratch, "tasks-once", "tasks.jsonl");
  const killed = path.join(scratch, "tasks-killed", "tasks.jsonl");
  expect(await main(args(once), io([]))).toBe(0);
  const lines: string[] = [];
  const kill: BuildIo = {
    ...io(lines),
    out: (text) => {
      lines.push(text);
      if (/^build-tasks: store_a \d{4}-\d{2}-\d{2}: /.test(text)) throw new Error("killed");
    },
  };
  expect(await main(args(killed), kill)).toBe(1);
  const from = recorder.requests.length;
  const logged = readFileSync(psqlLog, "utf8").length;
  expect(await main(args(killed), io(lines))).toBe(0);
  // Only the tasks reader's pick of tasks: its terms were recorded.
  expect(recorder.requests.slice(from)).toHaveLength(1);
  expect(psqlLines(logged)).toHaveLength(1);
  expect(readFileSync(killed, "utf8")).toBe(readFileSync(once, "utf8"));
  const summary = (out: string) =>
    roundedSpend(
      JSON.parse(readFileSync(`${out}.summary.json`, "utf8")) as Record<string, unknown>
    );
  expect(summary(killed)).toEqual(summary(once));
  expect(existsSync(`${killed}.found.jsonl`)).toBe(false);
  // A finished build is left as it is.
  const again: string[] = [];
  expect(await main(args(killed), io(again))).toBe(0);
  expect(again.join("")).toContain("nothing to do");
}, 60_000);

/** A sessions file of the control sessions `names`, each with its path but `withoutPath`'s. */
function controlSessionsFile(file: string, names: readonly string[], withoutPath: string[] = []) {
  writeFileSync(
    path.join(setupDir, file),
    names
      .map((name) => {
        const line = { store: "store_a", hash: fileHash(controlPath(name)) };
        return withoutPath.includes(name) ? line : { ...line, path: controlPath(name) };
      })
      .map((line) => `${JSON.stringify(line)}\n`)
      .join("")
  );
  return path.join(setupDir, file);
}

/** The arguments of one build-tasks --first-read run. */
const controlArgs = (sessions: string, out: string, seed: string, ...extra: string[]) => [
  "build-tasks",
  "--first-read",
  "--config",
  config,
  "--key-file",
  keyFile,
  "--skill",
  old,
  "--sessions",
  sessions,
  "--seed",
  seed,
  ...extra,
  "--out",
  out,
];

/** The lines of a tasks file, parsed. */
const tasksOf = (file: string): Record<string, unknown>[] =>
  readFileSync(file, "utf8")
    .split("\n")
    .filter((line) => line !== "")
    .map((line) => JSON.parse(line));

/** The control session whose transcript path's hash is `hash`. */
const controlNamed = (hash: unknown) =>
  Object.keys(CONTROL_SESSIONS).find((name) => fileHash(controlPath(name)) === hash);

test("build-tasks --first-read takes the first requests of sessions whose first reply read the skill, once each, and leaves out one whose first step the skill does not govern", async () => {
  const names = Object.keys(CONTROL_SESSIONS);
  const sessions = controlSessionsFile("control.jsonl", names, ["c-two"]);
  const out = path.join(scratch, "control", "tasks.jsonl");
  const lines: string[] = [];
  const from = recorder.requests.length;
  expect(await main(controlArgs(sessions, out, "1", "--max", "10"), io(lines))).toBe(0);
  // One reader call judged every request found.
  expect(recorder.requests.slice(from)).toHaveLength(1);
  const order = blindOrder(names, "1").map(({ item }) => item);
  const firstOfSame = order.find((name) => name === "c-one" || name === "c-same");
  const kept = order.filter((name) => [firstOfSame, "c-secret", "c-two"].includes(name));
  const tasks = tasksOf(out);
  expect(tasks.map((task) => task.pathHash)).toEqual(
    kept.map((name) => pathHash(controlPath(name)))
  );
  expect(tasks.map((task) => task.id)).toEqual(["t1", "t2", "t3"]);
  expect(
    tasks.every(
      (task) => task.label === "need" && task.source === "session" && task.store === "store_a"
    )
  ).toBe(true);
  const secret = tasks.find((task) => task.pathHash === pathHash(controlPath("c-secret")));
  expect(secret?.text).toBe(
    `Run the alpha checklist on the API.\n> Authorization: Bearer ${SECRET_REMOVED}`
  );
  const summary = JSON.parse(readFileSync(`${out}.summary.json`, "utf8"));
  expect(summary.done).toBe(true);
  const outcomes = Object.fromEntries(
    (summary.sessions as { hash: string; outcome: string }[]).map((each) => [
      controlNamed(each.hash),
      each.outcome,
    ])
  );
  expect(outcomes).toEqual({
    "c-one": firstOfSame === "c-one" ? "first request" : "same request",
    "c-same": firstOfSame === "c-same" ? "first request" : "same request",
    "c-secret": "first request",
    "c-late": "not first",
    "c-ungoverned": "first request",
    "c-two": "first request",
  });
  expect(summary.droppedNeed).toBe(1);
  expect(summary.dropped).toEqual([
    {
      store: "store_a",
      pathHash: pathHash(controlPath("c-ungoverned")),
      reason: "a file is opened first",
    },
  ]);
  expect(summary.tasks.map((task: { secretsRemoved: number }) => task.secretsRemoved)).toEqual(
    kept.map((name) => (name === "c-secret" ? 1 : 0))
  );
  // c-two's path came from sessions.sql.
  expect(summary.sessionQueries).toBe(1);
  expect(lines.join("\n")).toContain("the sessions gave 3 control tasks, fewer than --max 10");
  expect(existsSync(`${out}.found.jsonl`)).toBe(false);
}, 60_000);

test("build-tasks --first-read stops once --max requests are kept, and killed after its first session and run again gives the tasks and summary of one uninterrupted run", async () => {
  const names = Object.keys(CONTROL_SESSIONS);
  const sessions = controlSessionsFile("control-max.jsonl", names);
  const once = path.join(scratch, "control-once", "tasks.jsonl");
  const killed = path.join(scratch, "control-killed", "tasks.jsonl");
  const fromOnce = recorder.requests.length;
  expect(await main(controlArgs(sessions, once, "2", "--max", "2"), io([]))).toBe(0);
  const onceCalls = recorder.requests.length - fromOnce;
  // The sessions tried: the order up to the second request the skill governs the first step of.
  const seen = new Set<string>();
  let governed = 0;
  const tried: string[] = [];
  for (const name of blindOrder(names, "2").map(({ item }) => item)) {
    if (governed === 2) break;
    tried.push(name);
    const session = CONTROL_SESSIONS[name];
    if (session === undefined || !session.firstRead || seen.has(session.request)) continue;
    seen.add(session.request);
    if (!session.request.includes(UNGOVERNED)) governed += 1;
  }
  const summary = (out: string) =>
    roundedSpend(
      JSON.parse(readFileSync(`${out}.summary.json`, "utf8")) as Record<string, unknown>
    );
  const onceSummary = summary(once) as { sessions: { hash: string }[] };
  expect(onceSummary.sessions.length).toBeLessThan(names.length);
  expect(onceSummary.sessions.map((each) => controlNamed(each.hash))).toEqual(tried);
  expect(tasksOf(once)).toHaveLength(2);

  const lines: string[] = [];
  const kill: BuildIo = {
    ...io(lines),
    out: (text) => {
      lines.push(text);
      if (/^build-tasks: 1\/\d+ /.test(text)) throw new Error("killed");
    },
  };
  expect(await main(controlArgs(sessions, killed, "2", "--max", "2"), kill)).toBe(1);
  const from = recorder.requests.length;
  const logged = readFileSync(psqlLog, "utf8").length;
  expect(await main(controlArgs(sessions, killed, "2", "--max", "2"), io(lines))).toBe(0);
  // The first session is not pulled again.
  const pulls = (out: string) =>
    (summary(out) as { sessions: { pulls?: number }[] }).sessions
      .slice(1)
      .reduce((total, each) => total + (each.pulls ?? 0), 0);
  expect(psqlLines(logged).filter((line) => line.kind === "pull")).toHaveLength(pulls(killed));
  // The killed run made no reader call, so the run that went on makes them all.
  expect(recorder.requests.length - from).toBe(onceCalls);
  expect(readFileSync(killed, "utf8")).toBe(readFileSync(once, "utf8"));
  expect(summary(killed)).toEqual(summary(once));
  expect(existsSync(`${killed}.found.jsonl`)).toBe(false);
  const again: string[] = [];
  expect(await main(controlArgs(sessions, killed, "2", "--max", "2"), io(again))).toBe(0);
  expect(again.join("")).toContain("nothing to do");
  // Another seed and --max at the same --out are refused, naming each.
  const other: string[] = [];
  expect(await main(controlArgs(sessions, killed, "3", "--max", "3"), io(other))).toBe(2);
  expect(other.join("\n")).toContain("--seed was 2, now 3; --max was 2, now 3");
}, 60_000);

test("build-tasks --first-read --model drops and counts each session whose first request ran on another model as other-model, keeps those on it, and reports how many on it read the skill in their first reply", async () => {
  const names = [...Object.keys(CONTROL_SESSIONS), "c-other-model"];
  const sessions = controlSessionsFile("control-model.jsonl", names);
  const named = (hash: unknown) => names.find((name) => fileHash(controlPath(name)) === hash);
  const run = async (model: string, dir: string) => {
    const out = path.join(scratch, dir, "tasks.jsonl");
    const lines: string[] = [];
    const args = controlArgs(sessions, out, "1", "--max", "10", "--model", model);
    expect(await main(args, io(lines))).toBe(EXIT.ok);
    const summary = JSON.parse(readFileSync(`${out}.summary.json`, "utf8"));
    const outcomes = Object.fromEntries(
      (summary.sessions as { hash: string; outcome: string; detail?: string }[]).map((each) => [
        named(each.hash),
        each.outcome === "other-model" ? each.detail : each.outcome,
      ])
    );
    return { tasks: tasksOf(out), summary, outcomes, printed: lines.join("\n") };
  };

  const openai = await run("openai/gpt-6.1-sol", "control-model-openai");
  expect(openai.tasks.map((task) => task.pathHash)).toEqual([
    pathHash(controlPath("c-other-model")),
  ]);
  expect(openai.outcomes).toEqual({
    ...Object.fromEntries(
      Object.keys(CONTROL_SESSIONS).map((name) => [
        name,
        "its first reply ran on anthropic/claude-sonnet-5-5, not openai/gpt-6.1-sol",
      ])
    ),
    "c-other-model": "first request",
  });
  expect(openai.summary.inputs.model).toBe("openai/gpt-6.1-sol");
  expect(openai.summary.model).toEqual({ name: "openai/gpt-6.1-sol", firstRead: 1, otherModel: 6 });
  expect(openai.printed).toContain(
    "build-tasks: 1 sessions tried on openai/gpt-6.1-sol read alpha in their first reply; 6 sessions on another model dropped (other-model)"
  );
  expect(openai.printed).toContain(
    "build-tasks: 1 sessions on openai/gpt-6.1-sol read alpha in their first reply, fewer than 5"
  );

  // On F1's model: c-one, c-same, c-secret, c-ungoverned and c-two read alpha in their first
  // reply (c-same asks what c-one asks), c-late does not, and c-other-model is dropped.
  const sonnet = await run("anthropic/claude-sonnet-5-5", "control-model-sonnet");
  const order = blindOrder(names, "1").map(({ item }) => item);
  const firstOfSame = order.find((name) => name === "c-one" || name === "c-same");
  expect(sonnet.tasks.map((task) => task.pathHash)).toEqual(
    order
      .filter((name) => [firstOfSame, "c-secret", "c-two"].includes(name))
      .map((name) => pathHash(controlPath(name)))
  );
  expect(sonnet.outcomes["c-other-model"]).toBe(
    "its first reply ran on openai/gpt-6.1-sol, not anthropic/claude-sonnet-5-5"
  );
  expect(sonnet.outcomes["c-late"]).toBe("not first");
  expect(sonnet.summary.model).toEqual({
    name: "anthropic/claude-sonnet-5-5",
    firstRead: 5,
    otherModel: 1,
  });
  expect(sonnet.printed).not.toContain("in their first reply, fewer than 5");
}, 60_000);

test("build-tasks refuses --sessions, --seed and --max without --first-read, and --store with it, before any read", async () => {
  const sessions = controlSessionsFile("control-refused.jsonl", ["c-one"]);
  const out = path.join(scratch, "control-refused", "tasks.jsonl");
  const from = recorder.requests.length;
  const logged = readFileSync(psqlLog, "utf8").length;
  const pick = ["build-tasks", "--config", config, "--key-file", keyFile, "--skill", old];
  for (const [flag, value] of [
    ["--sessions", sessions],
    ["--seed", "1"],
    ["--max", "5"],
  ]) {
    const lines: string[] = [];
    expect(await main([...pick, "--store", "store_a", flag, value, "--out", out], io(lines))).toBe(
      2
    );
    expect(lines.join("\n")).toContain(`${flag} goes with --first-read only`);
  }
  const lines: string[] = [];
  expect(await main([...controlArgs(sessions, out, "1"), "--store", "store_a"], io(lines))).toBe(2);
  expect(lines.join("\n")).toContain("--first-read takes its sessions from --sessions");
  expect(recorder.requests.length).toBe(from);
  expect(readFileSync(psqlLog, "utf8").length).toBe(logged);
  expect(existsSync(out)).toBe(false);
});

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

/** The entries of `text` and the messages of its branch, read from the whole text at once: what a
 *  transcript read from its spool, line by line, must give. */
function readWhole(text: string): { entries: SessionEntry[]; messages: SessionMessage[] } {
  const lines = text.split("\n");
  const entries: SessionEntry[] = [];
  for (const [index, line] of lines.entries()) {
    if (line.trim() === "") continue;
    let value: Record<string, unknown>;
    try {
      value = JSON.parse(line);
    } catch {
      if (index === lines.length - 1) break;
      throw new Error(`line ${index + 1} is not JSON`);
    }
    const message = value.message as { content?: unknown } | undefined;
    if (typeof message?.content === "string") {
      message.content = [{ type: "text", text: message.content }];
    }
    entries.push(SessionEntry.parse(value));
  }
  const messages = currentBranch(entries)
    .map(messageOf)
    .filter((message): message is SessionMessage => message !== undefined);
  return { entries, messages };
}

test("a transcript read from its spool, in chunks of any size, gives the entries, branch and messages its whole text gives, and holds no message's text", () => {
  const dir = path.join(scratch, "spools");
  mkdirSync(dir, { recursive: true });
  // Every made-up session, and one with a line of characters of 2 to 4 bytes, a line that ends
  // "\r\n", and a last line still being written. Each is read in chunks of 4096 bytes and of
  // SPOOL_CHUNK: a session longer than 4096 bytes puts a line across a 4096-byte edge. Only the
  // "written" text is read in chunks of 7 bytes too, which put its characters of 2 to 4 bytes
  // across chunk edges; the made-up sessions read so add nothing to that and take seconds.
  const texts = Object.keys(SESSIONS).map((name) => ({ name, text: sessionText(name) }));
  const note = { type: "custom", customType: "note", data: "é 日本 🎉 ".repeat(9) };
  texts.push({
    name: "written",
    text: `${sessionText("good")}${JSON.stringify(note)}\r\n{"type":"message","message":{"ro`,
  });
  // A line crosses a 4096-byte edge (the byte before it is no newline), and a character of more
  // than one byte crosses a 7-byte edge of the "written" text.
  expect(
    texts.some(({ text }) => {
      const bytes = Buffer.from(text);
      return bytes.length > 4096 && bytes[4095] !== 0x0a;
    })
  ).toBe(true);
  let offset = 0;
  let crossed = false;
  for (const char of texts.at(-1)?.text ?? "") {
    const size = Buffer.byteLength(char);
    crossed ||= size > 1 && Math.floor(offset / 7) !== Math.floor((offset + size - 1) / 7);
    offset += size;
  }
  expect(crossed).toBe(true);
  const refusedNames: string[] = [];
  for (const { name, text } of texts) {
    const chunks = name === "written" ? [7, 4096, SPOOL_CHUNK] : [4096, SPOOL_CHUNK];
    const file = path.join(dir, `${name}.jsonl`);
    writeFileSync(file, text);
    let whole: ReturnType<typeof readWhole>;
    try {
      whole = readWhole(text);
    } catch (error) {
      // The broken session: the same line is refused, whatever the chunk.
      for (const chunk of chunks) {
        expect(() => readTranscript(file, chunk)).toThrow((error as Error).message);
      }
      refusedNames.push(name);
      continue;
    }
    for (const chunk of chunks) {
      const t = readTranscript(file, chunk);
      expect({ name, chunk, messages: t.items.map((_, index) => t.message(index)) }).toEqual({
        name,
        chunk,
        messages: whole.messages,
      });
      expect(t.branch.map((entry) => entry.id)).toEqual(
        currentBranch(whole.entries).map((entry) => entry.id)
      );
      // Each entry of the branch reads back whole; the entries held hold no message's text.
      const branch = currentBranch(whole.entries);
      expect(branch.map((_, at) => t.entry(at))).toEqual(branch);
      expect(t.entries).toHaveLength(whole.entries.length);
      expect(JSON.stringify(t.entries)).not.toContain(LINE);
      expect(JSON.stringify(t.items)).not.toContain(LINE);
    }
  }
  expect(refusedNames).toEqual(["broken"]);
});

/** A sessions file of `count` sessions no store holds, each with no path. */
function absentSessions(file: string, count: number): string {
  const lines = Array.from({ length: count }, (_, n) => ({
    store: "store_a",
    hash: fileHash(`/sessions/absent-${n}.jsonl`),
  }));
  writeFileSync(
    path.join(setupDir, file),
    lines.map((line) => `${JSON.stringify(line)}\n`).join("")
  );
  return path.join(setupDir, file);
}

interface Stopped {
  rule: string;
  sessions: number;
  cases: number;
  spentUsd: number;
  projectedUsd: number | null;
}

const stoppedOf = (out: string): Stopped | null =>
  (JSON.parse(readFileSync(`${out}.summary.json`, "utf8")) as { stopped: Stopped | null }).stopped;

test("the stop rule stops a build with no case once --stop-after-sessions are decided (unbounded projection), and a run that goes on stops again at once, reading nothing", async () => {
  const sessions = absentSessions("absent.jsonl", STOP_AFTER_SESSIONS + 5);
  const out = path.join(scratch, "stop-sessions", "cases.jsonl");
  const lines: string[] = [];
  const args = buildArgs(sessions, out, "1", "--max", "15", "--concurrency", "1");
  expect(await main(args, io(lines))).toBe(EXIT.stoppedByCost);
  const stopped = {
    rule: "stop-projected-usd",
    sessions: STOP_AFTER_SESSIONS,
    cases: 0,
    spentUsd: 0,
    projectedUsd: null,
  };
  expect(stoppedOf(out)).toEqual(stopped);
  const summary = summaryOf(out);
  expect(summary.sessions).toHaveLength(STOP_AFTER_SESSIONS);
  expect(summary.drops).toEqual({ "not-in-store": STOP_AFTER_SESSIONS });
  expect(lines.join("\n")).toContain(
    `build-cases: stopped by --stop-projected-usd: ${STOP_AFTER_SESSIONS} sessions decided, 0 cases, $0.0000 of readers spent, projected unbounded to --max 15 cases`
  );
  expect(existsSync(`${out}.spool`)).toBe(false);

  const from = recorder.requests.length;
  const logged = readFileSync(psqlLog, "utf8").length;
  const again: string[] = [];
  expect(await main(args, io(again))).toBe(EXIT.stoppedByCost);
  expect(again.join("")).toContain(`${STOP_AFTER_SESSIONS} sessions decided there (0 cases)`);
  expect(recorder.requests.length).toBe(from);
  expect(readFileSync(psqlLog, "utf8").length).toBe(logged);
  expect(stoppedOf(out)).toEqual(stopped);
  expect(summaryOf(out).sessions).toHaveLength(STOP_AFTER_SESSIONS);
}, 60_000);

test("the stop rule stops a build with no case as soon as --stop-zero-case-usd is spent, before --stop-after-sessions are decided", async () => {
  const names = ["no-step", "audit-breaks", "audit-not-governed", "no-common", "not-served"];
  const sessions = sessionsFile("stop-usd.jsonl", names);
  const out = path.join(scratch, "stop-usd", "cases.jsonl");
  const lines: string[] = [];
  const args = buildArgs(
    sessions,
    out,
    "1",
    "--concurrency",
    "1",
    "--stop-zero-case-usd",
    "0.0001"
  );
  expect(await main(args, io(lines))).toBe(EXIT.stoppedByCost);
  const summary = summaryOf(out);
  const stopped = stoppedOf(out);
  // It stops after the first session that spent, a drop like those before it.
  const spentAt = summary.sessions.findIndex((each) => each.readerUsd > 0);
  expect(spentAt).toBe(summary.sessions.length - 1);
  expect(summary.sessions.length).toBeLessThan(names.length);
  expect(summary.sessions.every((each) => each.outcome === "dropped")).toBe(true);
  expect(stopped).toEqual({
    rule: "stop-zero-case-usd",
    sessions: summary.sessions.length,
    cases: 0,
    spentUsd: summary.readerSpend.usd,
    projectedUsd: null,
  });
  expect(stopped?.spentUsd).toBeGreaterThanOrEqual(0.0001);
}, 60_000);

test("the stop rule stops a build whose cost projected to --max cases passes --stop-projected-usd, and leaves one under the bound alone", async () => {
  const sessions = sessionsFile("stop-projected.jsonl", ["good", "secret", "skill-md-only"]);
  const bounds = (usd: string) => [
    "--max",
    "5",
    "--concurrency",
    "1",
    "--stop-after-sessions",
    "2",
    "--stop-projected-usd",
    usd,
  ];
  const over = path.join(scratch, "stop-over", "cases.jsonl");
  expect(await main(buildArgs(sessions, over, "1", ...bounds("0.0001")), io([]))).toBe(
    EXIT.stoppedByCost
  );
  const summary = summaryOf(over);
  const spentUsd = summary.readerSpend.usd;
  expect(stoppedOf(over)).toEqual({
    rule: "stop-projected-usd",
    sessions: 2,
    cases: 2,
    spentUsd,
    projectedUsd: (5 * spentUsd) / 2,
  });
  expect(summary.casesBuilt).toBe(2);
  expect(loadCases(over, old)).toHaveLength(2);

  const under = path.join(scratch, "stop-under", "cases.jsonl");
  expect(await main(buildArgs(sessions, under, "1", ...bounds("1000")), io([]))).toBe(EXIT.ok);
  expect(stoppedOf(under)).toBeNull();
  expect(summaryOf(under).casesBuilt).toBe(3);
}, 60_000);

test("build-cases refuses a stop bound that is no number, before any read", async () => {
  const sessions = sessionsFile("stop-refused.jsonl", ["good"]);
  const out = path.join(scratch, "stop-refused", "cases.jsonl");
  const from = recorder.requests.length;
  for (const [flag, value] of [
    ["--stop-projected-usd", "-1"],
    ["--stop-zero-case-usd", "x"],
    ["--stop-after-sessions", "2.5"],
  ]) {
    const lines: string[] = [];
    expect(await main(buildArgs(sessions, out, "1", `${flag}=${value}`), io(lines))).toBe(
      EXIT.refused
    );
    expect(lines.join("\n")).toContain(`${flag} ${value} is not`);
  }
  expect(recorder.requests.length).toBe(from);
  expect(existsSync(out)).toBe(false);
});

/** An io whose peak resident memory is PEAK_BYTES until it prints a line `decided` matches, and
 *  OVER_LIMIT from then on; what it writes to standard error goes to `errors`. */
function overLimitOnce(decided: RegExp, lines: string[], errors: string[]): BuildIo {
  let over = false;
  return {
    ...io(lines),
    out: (text) => {
      lines.push(text);
      if (decided.test(text)) over = true;
    },
    err: (line) => errors.push(line),
    peakRssBytes: () => (over ? OVER_LIMIT : PEAK_BYTES),
  };
}

test("build-cases stops above 1 GiB of peak resident memory once a session is decided: the sessions still open are stopped and not decided, the cases decided before are kept, exit 3; a run under the limit goes on and decides them", async () => {
  const sessions = sessionsFile("memory-stop.jsonl", ["good", "secret", "openai"]);
  const out = path.join(scratch, "memory-stop", "cases.jsonl");
  // All three sessions are open at once, so two are still open when the first is decided.
  const args = buildArgs(sessions, out, "1", "--concurrency", "3");
  const lines: string[] = [];
  const errors: string[] = [];
  const over = overLimitOnce(/^build-cases: \d+\/\d+ /, lines, errors);
  expect(await main(args, over)).toBe(EXIT.stopped);
  const reason =
    "the build's peak resident memory, 1024 MiB, is above 1 GiB, so no new session starts";
  expect(errors.join("\n")).toContain(`build-cases: stopped: ${reason}`);
  const stopped = summaryOf(out);
  expect(stopped.stopped).toBe(reason);
  expect(stopped.peakRssBytes).toBe(OVER_LIMIT);
  const [first, ...open] = stopped.sessions;
  expect(first?.outcome).toStartWith("case ");
  expect(open.map((each) => each.outcome)).toEqual([
    "stopped before its case was built: the peak memory passed 1 GiB",
    "stopped before its case was built: the peak memory passed 1 GiB",
  ]);
  expect(loadCases(out, old).map((c) => `case ${c.id}`)).toEqual([first?.outcome]);
  expect(lines.at(-1)).toContain(`, peak memory 1024 MiB; written to ${out}`);
  expect(existsSync(`${out}.spool`)).toBe(false);

  // A summary written before the peak was recorded holds no peakRssBytes; a run goes on from it.
  const summaryFile = `${out}.summary.json`;
  const older = JSON.parse(readFileSync(summaryFile, "utf8")) as Record<string, unknown>;
  writeFileSync(summaryFile, JSON.stringify({ ...older, peakRssBytes: undefined }));
  const again: string[] = [];
  expect(await main(args, io(again))).toBe(EXIT.ok);
  expect(again.join("")).toContain("1 sessions decided there (1 cases)");
  const done = summaryOf(out);
  expect(done.stopped).toBeNull();
  expect(done.peakRssBytes).toBe(PEAK_BYTES);
  expect(done.sessions.map((each) => each.hash)).toEqual(stopped.sessions.map((each) => each.hash));
  expect(done.sessions.every((each) => each.outcome.startsWith("case "))).toBe(true);
  expect(loadCases(out, old)).toHaveLength(3);
}, 120_000);

test("build-cases --written stops above 1 GiB of peak resident memory once a task is decided, with the cases built so far written, exit 3", async () => {
  const out = path.join(scratch, "written-memory", "cases.jsonl");
  const context = `store_a:${fileHash(sessionPath("good"))}`;
  const args = writtenArgs(
    out,
    "--context-session",
    context,
    "--session-cases",
    sessionCasesFile()
  );
  const lines: string[] = [];
  const errors: string[] = [];
  const over = overLimitOnce(/^build-cases: written task \d+ of /, lines, errors);
  expect(await main(args, over)).toBe(EXIT.stopped);
  const reason =
    "the build's peak resident memory, 1024 MiB, is above 1 GiB, so no new task starts";
  expect(errors.join("\n")).toContain(`build-cases: stopped: ${reason}`);
  const summary = writtenSummaryOf(out);
  expect(summary.stopped).toBe(reason);
  expect(summary.peakRssBytes).toBe(OVER_LIMIT);
  // Task 1 is a case, as in the written build above, and no task after it is read.
  const id = `skills-1-${pathHash(sessionPath("good")).slice(0, 12)}-w1`;
  expect(summary.tasks.map((each) => [each.task, each.outcome])).toEqual([[1, `case ${id}`]]);
  expect(loadCases(out, old).map((c) => c.id)).toEqual([id]);
  expect(lines.at(-1)).toContain(`, peak memory 1024 MiB; written to ${out}`);
}, 60_000);

test("the pull reads the store command's output row by row with psqlRows' checks and faults, and reports a failing command first", async () => {
  const dir = path.join(scratch, "pull-output");
  mkdirSync(dir, { recursive: true });
  const row = (o: number, c: string, k = 0) => ` ${JSON.stringify({ o, k, c })}`;
  const outputs = [
    ["part", "-----", row(-1, ""), row(0, "a\n"), "(2 rows)", ""],
    ["part", "-----", row(-1, ""), row(0, "a\n"), "(2 rows)"],
    ["no rule here", "(0 rows)"],
    ["-----", row(-1, ""), "(1 row)"],
    [" a | b", "---+---", row(-1, ""), "(1 row)"],
    ["part", "-----", row(-1, ""), "(2 rows)"],
    ["part", "-----", row(-1, ""), "(1 row)", "more"],
    ["part", "-----", " nope", "(1 row)"],
    ["part", "-----", "", row(-1, ""), "(1 row)"],
    ["part", "-----", row(-1, ""), "", ""],
  ].map((lines) => lines.join("\n"));
  /** A store command that prints `output`, named `name`. */
  const printing = (name: string, output: string) => {
    const file = path.join(dir, `${name}.txt`);
    writeFileSync(file, output);
    const command = path.join(dir, `${name}.sh`);
    writeFileSync(command, `#!/bin/sh\ncat '${file}'\n`);
    chmodSync(command, 0o755);
    return command;
  };
  const parsing: number[] = [];
  for (const [n, output] of outputs.entries()) {
    const command = printing(String(n), output);
    const spool = path.join(dir, `${n}.jsonl`);
    let fault: string | undefined;
    try {
      psqlRows(output);
    } catch (error) {
      fault = (error as Error).message;
    }
    if (fault === undefined) {
      parsing.push(n);
      expect(await pullTranscript(command, "store_a", "/sessions/x.jsonl", spool)).toEqual({
        bytes: 2,
        calls: 1,
      });
      expect(readFileSync(spool, "utf8")).toBe("a\n");
    } else {
      expect({
        n,
        error: await pullTranscript(command, "store_a", "/x", spool).catch(String),
      }).toEqual({ n, error: `Error: ${fault}` });
    }
  }
  // Only the two whole tables parse, with a last newline and without; every other output is a
  // fault, so the pull is checked on both.
  expect(parsing).toEqual([0, 1]);
  // An imported file appended to: the file row's two slices, then a part at the file row's byte
  // length, whose DEL psql prints as \x7F; and a part at 0 after them, which overlaps the file row.
  const head = ["part", "-----", row(-1, "ab"), row(-1, "c\n", 1)];
  const imported = printing(
    "imported",
    [...head, String.raw` {"o" : 4, "k" : 0, "c" : "d\x7F\n"}`, "(3 rows)", ""].join("\n")
  );
  const spool = path.join(dir, "imported.jsonl");
  expect(await pullTranscript(imported, "store_a", "/x", spool)).toEqual({ bytes: 7, calls: 1 });
  expect(readFileSync(spool, "utf8")).toBe("abc\nd\u007f\n");
  const overlap = printing("overlap", [...head, row(0, "d\n"), "(3 rows)", ""].join("\n"));
  expect(await pullTranscript(overlap, "store_a", "/x", spool).catch(String)).toBe(
    "Error: pull rows: the part at offset 0 does not start where the text before it ends (4)"
  );
  // A command that fails is reported as failing, whatever it printed.
  const failing = path.join(dir, "failing.sh");
  writeFileSync(failing, "#!/bin/sh\necho 'not a table'\necho 'psql: error: gone' >&2\nexit 3\n");
  chmodSync(failing, 0o755);
  expect(
    await pullTranscript(failing, "store_a", "/x", path.join(dir, "failing.jsonl")).catch(String)
  ).toBe("Error: the store command exited 3: psql: error: gone");
});

/** The profile load.ts's load test takes for a task from session `name`: build-cases' profile of
 *  that session's case, less the tested skill, with the case's context. */
function caseLoadProfile(cases: readonly Case[], name: string): Record<string, unknown> {
  const c = cases.find((each) => each.source.pathHash === pathHash(sessionPath(name)));
  if (c === undefined) throw new Error(`build-cases built no case of ${name}`);
  return {
    ...c.profile,
    skills: c.profile.skills.filter((each) => each.name !== "alpha"),
    context: c.context,
  };
}

/** The cases build-cases builds of the good and legion sessions, for the profile rows. */
async function profileCases(): Promise<Case[]> {
  const out = path.join(scratch, "profile-cases", "cases.jsonl");
  if (!existsSync(out)) {
    const sessions = sessionsFile("profile-cases.jsonl", ["good", "legion"]);
    expect(await main(buildArgs(sessions, out, "1"), io([]))).toBe(EXIT.ok);
  }
  return loadCases(out, old);
}

/** The profile files of the tasks of `out`, by task id. */
function profilesOf(out: string): Record<string, unknown> {
  return Object.fromEntries(
    tasksOf(out).map((task) => [
      String(task.id),
      JSON.parse(readFileSync(String(task.profile), "utf8")) as unknown,
    ])
  );
}

test("build-tasks --profiles-out gives each session task build-cases' profile of its session, less the tested skill, and each written task the --context-session's", async () => {
  const cases = await profileCases();
  const dir = path.join(scratch, "profiles-pick");
  const out = path.join(scratch, "profiles-pick-tasks", "tasks.jsonl");
  const legion = fileHash(sessionPath("legion"));
  const args = [
    "build-tasks",
    "--config",
    config,
    "--key-file",
    keyFile,
    "--skill",
    old,
    "--store",
    "store_a",
    "--out",
    out,
    "--profiles-out",
    dir,
    "--copies",
    copiesDir,
    "--count",
    countDir,
    "--context-session",
    `store_b:${legion}`,
  ];
  const lines: string[] = [];
  expect(await main(args, io(lines))).toBe(EXIT.ok);
  const tasks = tasksOf(out);
  expect(tasks.filter((task) => task.source === "session")).toHaveLength(2);
  expect(tasks.filter((task) => task.source === "written")).toHaveLength(8);
  // task-1's session, a subagent whose listing lacks alpha, is dropped and counted, and task-4's,
  // the next session with the same first message, is offered and taken in its place.
  expect(tasks[0]).toMatchObject({
    text: "Give me the alpha checklist for the fixture project.",
    source: "session",
    pathHash: pathHash("/sessions/task-4.jsonl"),
  });
  expect(lines.join("")).toContain("1 candidate sessions dropped (1 skill-not-listed)");
  for (const task of tasks) expect(task.profile).toBe(path.resolve(dir, `${task.id}.json`));
  const good = caseLoadProfile(cases, "good");
  const written = caseLoadProfile(cases, "legion");
  expect(good).not.toEqual(written);
  expect(profilesOf(out)).toEqual(
    Object.fromEntries(
      tasks.map((task) => [String(task.id), task.source === "session" ? good : written])
    )
  );
  const summary = JSON.parse(readFileSync(`${out}.summary.json`, "utf8"));
  expect(summary.profiles.contextSession).toMatchObject({
    store: "store_b",
    hash: legion,
    pathHash: pathHash(sessionPath("legion")),
    context: written.context,
  });
  expect(
    summary.profiles.tasks.map((each: { task: string; from: string }) => [each.task, each.from])
  ).toEqual(
    tasks.map((task) => [task.id, task.source === "session" ? "session" : "context-session"])
  );
  expect(summary.candidateDrops.counts).toEqual({ "skill-not-listed": 1 });
  expect(summary.candidateDrops.sessions).toEqual([
    {
      store: "store_a",
      pathHash: pathHash("/sessions/task-1.jsonl"),
      reason: "skill-not-listed",
      detail: "its context subagent:task does not list alpha",
      context: "subagent:task",
    },
  ]);
  expect(existsSync(`${out}.spool`)).toBe(false);
}, 120_000);

// Catches: a context session refused (exit 2) after the store read that found it, where exit 2 is
// a refusal before any store read.
test("build-tasks fails (exit 1) on a --context-session whose rebuilt context does not list the skill, naming why, before any reader call", async () => {
  const out = path.join(scratch, "profiles-unlisted-context", "tasks.jsonl");
  const unlisted = fileHash("/sessions/task-1.jsonl");
  const from = recorder.requests.length;
  const lines: string[] = [];
  const code = await main(
    [
      "build-tasks",
      "--config",
      config,
      "--key-file",
      keyFile,
      "--skill",
      old,
      "--store",
      "store_a",
      "--out",
      out,
      "--profiles-out",
      path.join(scratch, "profiles-unlisted-context"),
      "--copies",
      copiesDir,
      "--count",
      countDir,
      "--context-session",
      `store_a:${unlisted}`,
    ],
    io(lines)
  );
  expect(code).toBe(EXIT.failed);
  expect(lines.join("\n")).toContain(
    `--context-session store_a:${unlisted}: its rebuilt context (subagent:task) does not list alpha`
  );
  expect(recorder.requests.length).toBe(from);
  expect(existsSync(out)).toBe(false);
}, 60_000);

test("build-tasks --first-read --profiles-out gives each control task build-cases' profile of its session, less the tested skill", async () => {
  const cases = await profileCases();
  const names = [...Object.keys(CONTROL_SESSIONS), "c-unlisted"];
  const sessions = controlSessionsFile("profiles-control.jsonl", names, ["c-two"]);
  const dir = path.join(scratch, "profiles-control");
  const out = path.join(scratch, "profiles-control-tasks", "tasks.jsonl");
  const args = [
    ...controlArgs(sessions, out, "1", "--max", "10"),
    "--profiles-out",
    dir,
    "--copies",
    copiesDir,
    "--count",
    countDir,
  ];
  expect(await main(args, io([]))).toBe(EXIT.ok);
  const tasks = tasksOf(out);
  expect(tasks).toHaveLength(3);
  // c-unlisted's first reply reads alpha, but its recorded listing lacks it: dropped and counted.
  const summary = JSON.parse(readFileSync(`${out}.summary.json`, "utf8"));
  expect(summary.skillNotListed).toBe(1);
  expect(
    (summary.sessions as { hash: string; outcome: string; detail?: string }[]).find(
      (each) => each.hash === fileHash(controlPath("c-unlisted"))
    )
  ).toMatchObject({
    outcome: "skill-not-listed",
    detail: "its context subagent:task does not list alpha",
  });
  // Each control session is F1 with its first request replaced, so its profile is the good case's.
  const good = caseLoadProfile(cases, "good");
  expect(profilesOf(out)).toEqual(Object.fromEntries(tasks.map((task) => [String(task.id), good])));
  expect(summary.profiles.contextSession).toBeNull();
  expect(existsSync(`${out}.spool`)).toBe(false);
}, 120_000);

test("build-tasks refuses --profiles-out without --copies and --count, or without --context-session in the reader's mode, the profile flags without --profiles-out, and --context-session with --first-read, before any read", async () => {
  const out = path.join(scratch, "profiles-refused", "tasks.jsonl");
  const pick = ["build-tasks", "--config", config, "--key-file", keyFile, "--skill", old];
  const sessions = controlSessionsFile("profiles-refused.jsonl", ["c-one"]);
  const from = recorder.requests.length;
  const logged = readFileSync(psqlLog, "utf8").length;
  const rows: [string[], string][] = [
    [
      [...pick, "--store", "store_a", "--out", out, "--profiles-out", out],
      "--profiles-out needs --copies and --count",
    ],
    [
      [...pick, "--store", "store_a", "--out", out, "--profiles-out", out, "--copies", copiesDir],
      "--profiles-out needs --copies and --count",
    ],
    [
      [
        ...pick,
        "--store",
        "store_a",
        "--out",
        out,
        "--profiles-out",
        out,
        "--copies",
        copiesDir,
        "--count",
        countDir,
      ],
      "--profiles-out needs --context-session <store>:<hash>",
    ],
    [
      [...pick, "--store", "store_a", "--out", out, "--copies", copiesDir],
      "--copies goes with --profiles-out only",
    ],
    [
      [...pick, "--store", "store_a", "--out", out, "--count", countDir],
      "--count goes with --profiles-out only",
    ],
    [
      [
        ...pick,
        "--store",
        "store_a",
        "--out",
        out,
        "--context-session",
        "store_a:0123456789abcdef",
      ],
      "--context-session goes with --profiles-out only",
    ],
    [
      [
        ...pick,
        "--store",
        "store_a",
        "--out",
        out,
        "--profiles-out",
        out,
        "--copies",
        copiesDir,
        "--count",
        countDir,
        "--context-session",
        "store_b:nothex",
      ],
      "--context-session store_b:nothex is not <store>:<16 hex characters>",
    ],
    [
      [
        ...controlArgs(sessions, out, "1"),
        "--profiles-out",
        out,
        "--copies",
        copiesDir,
        "--count",
        countDir,
        "--context-session",
        `store_a:${fileHash(controlPath("c-one"))}`,
      ],
      "--first-read writes no written task, so it takes no --context-session",
    ],
  ];
  for (const [args, why] of rows) {
    const lines: string[] = [];
    expect({ why, code: await main(args, io(lines)) }).toEqual({ why, code: EXIT.refused });
    expect(lines.join("\n")).toContain(why);
  }
  expect(recorder.requests.length).toBe(from);
  expect(readFileSync(psqlLog, "utf8").length).toBe(logged);
  expect(existsSync(out)).toBe(false);
});

/** The arguments of a build-tasks run in the reader's mode on store_a, with `extra`. */
const tasksArgs = (out: string, ...extra: string[]) => [
  "build-tasks",
  "--config",
  config,
  "--key-file",
  keyFile,
  "--skill",
  old,
  "--store",
  "store_a",
  "--out",
  out,
  ...extra,
];

/** The flags that write each task's profile under `dir`, a written task taking the legion
 *  session's. */
const profileFlags = (dir: string) => [
  "--profiles-out",
  dir,
  "--copies",
  copiesDir,
  "--count",
  countDir,
  "--context-session",
  `store_b:${fileHash(sessionPath("legion"))}`,
];

/** An io that throws on the first printed text `pattern` matches, as a kill then would end the
 *  run: what was saved before the text was printed stays. */
const killedOn = (pattern: RegExp, lines: string[], now = () => performance.now()): BuildIo => ({
  ...io(lines),
  out: (text) => {
    lines.push(text);
    if (pattern.test(text)) throw new Error("killed");
  },
  now,
});

test("build-tasks --max-candidates ends the candidate search once that many candidates are decided; the reader gets the messages accepted by then and writes the rest of the tasks in the --context-session's context, and the summary says the bound ended the search", async () => {
  const dir = path.join(scratch, "bound-candidates");
  const out = path.join(dir, "tasks.jsonl");
  const lines: string[] = [];
  const logged = psqlLogLength();
  expect(
    await main(
      tasksArgs(out, ...profileFlags(path.join(dir, "profiles")), "--max-candidates", "2"),
      io(lines)
    )
  ).toBe(EXIT.ok);
  // task-1's session is dropped (skill-not-listed) and task-4's, of the same text, accepted: two
  // candidates decided, so task-3's, which a search with no bound takes too, is never pulled. The
  // context session and those two are.
  expect(transcriptPulls(logged)).toBe(3);
  const tasks = tasksOf(out);
  expect(tasks.filter((task) => task.source === "session")).toEqual([
    expect.objectContaining({
      text: "Give me the alpha checklist for the fixture project.",
      pathHash: pathHash("/sessions/task-4.jsonl"),
    }),
  ]);
  expect(tasks.filter((task) => task.source === "written")).toHaveLength(9);
  const summary = JSON.parse(readFileSync(`${out}.summary.json`, "utf8"));
  expect(summary.messagesOffered).toBe(1);
  expect(summary.search).toEqual({
    maxCandidates: 2,
    maxSearchMinutes: null,
    ended: "--max-candidates",
    decided: 2,
    accepted: 1,
    seconds: expect.any(Number),
  });
  expect(summary.candidateDrops.counts).toEqual({ "skill-not-listed": 1 });
  expect(
    summary.profiles.tasks.filter((each: { from: string }) => each.from === "context-session")
  ).toHaveLength(9);
  expect(lines.join("")).toContain(
    "build-tasks: the candidate search ended (--max-candidates): 2 candidates decided, 1 accepted, 1 dropped, "
  );
  expect(existsSync(`${out}.candidates.jsonl`)).toBe(false);
}, 120_000);

test("build-tasks --max-search-minutes ends the candidate search at the first candidate after the search's own time reaches it, and the summary says the bound ended the search", async () => {
  const out = path.join(scratch, "bound-minutes", "tasks.jsonl");
  const lines: string[] = [];
  // Each candidate's pull moves the clock a minute, so 1.5 minutes are reached after the second.
  expect(
    await main(tasksArgs(out, "--model", "openai/gpt-6.1-sol", "--max-search-minutes", "1.5"), {
      ...io(lines),
      now: pullClock,
    })
  ).toBe(EXIT.ok);
  const summary = JSON.parse(readFileSync(`${out}.summary.json`, "utf8"));
  expect(summary.search).toEqual({
    maxCandidates: null,
    maxSearchMinutes: 1.5,
    ended: "--max-search-minutes",
    decided: 2,
    accepted: 0,
    seconds: 120,
  });
  // task-1's and task-4's sessions ran on another model; task-3's, on the model, is not reached.
  expect(summary.candidateDrops.counts).toEqual({ "other-model": 2 });
  expect(summary.messagesOffered).toBe(0);
  expect(tasksOf(out).every((task) => task.source === "written")).toBe(true);
  expect(lines.join("")).toContain(
    "build-tasks: the candidate search ended (--max-search-minutes): 2 candidates decided, 0 accepted, 2 dropped, 120 s of search"
  );
}, 60_000);

test("build-tasks with bounds, killed mid-search and run again, pulls no decided candidate again and ends with the tasks and summary of one unkilled run, the search's seconds summed over the runs", async () => {
  const bounds = [
    "--model",
    "openai/gpt-6.1-sol",
    "--max-candidates",
    "60",
    "--max-search-minutes",
    "90",
  ];
  const once = path.join(scratch, "bound-once", "tasks.jsonl");
  const killed = path.join(scratch, "bound-killed", "tasks.jsonl");
  let logged = psqlLogLength();
  expect(await main(tasksArgs(once, ...bounds), { ...io([]), now: pullClock })).toBe(EXIT.ok);
  expect(transcriptPulls(logged)).toBe(3);
  const lines: string[] = [];
  expect(
    await main(
      tasksArgs(killed, ...bounds),
      killedOn(/^build-tasks: candidate .*: dropped /, lines, pullClock)
    )
  ).toBe(EXIT.failed);
  // The candidate decided before the kill is saved, after the bounds.
  expect(
    readFileSync(`${killed}.candidates.jsonl`, "utf8")
      .split("\n")
      .filter((line) => line !== "")
      .map((line) => JSON.parse(line))
  ).toEqual([
    { bounds: { maxCandidates: 60, maxSearchMinutes: 90 } },
    {
      dropped: {
        store: "store_a",
        pathHash: pathHash("/sessions/task-1.jsonl"),
        reason: "other-model",
        detail: "its first reply ran on anthropic/claude-sonnet-5-5, not openai/gpt-6.1-sol",
      },
      seconds: 60,
    },
  ]);
  logged = psqlLogLength();
  const from = recorder.requests.length;
  expect(await main(tasksArgs(killed, ...bounds), { ...io(lines), now: pullClock })).toBe(EXIT.ok);
  expect(lines.join("")).toContain("build-tasks: going on from 1 candidates decided in ");
  // task-1's session is not pulled again; task-4's and task-3's are.
  expect(transcriptPulls(logged)).toBe(2);
  // The terms and the queries were recorded: the one reader call is the pick of the tasks.
  expect(recorder.requests.length - from).toBe(1);
  expect(readFileSync(killed, "utf8")).toBe(readFileSync(once, "utf8"));
  const summary = (out: string) =>
    roundedSpend(
      JSON.parse(readFileSync(`${out}.summary.json`, "utf8")) as Record<string, unknown>
    );
  expect(summary(killed)).toEqual(summary(once));
  expect(summary(killed).search).toEqual({
    maxCandidates: 60,
    maxSearchMinutes: 90,
    ended: "candidates ran out",
    decided: 3,
    accepted: 1,
    seconds: 180,
  });
  expect(existsSync(`${killed}.candidates.jsonl`)).toBe(false);
}, 120_000);

test("build-tasks refuses (exit 2) to go on from a candidate search saved under other bounds, naming both values, and leaves its files as they were; an --out whose search decided nothing takes any bounds", async () => {
  const model = ["--model", "openai/gpt-6.1-sol"];
  const bounds = [...model, "--max-candidates", "60", "--max-search-minutes", "90"];
  const out = path.join(scratch, "bound-refused", "tasks.jsonl");
  expect(await main(tasksArgs(out, ...bounds), killedOn(/^build-tasks: candidate /, []))).toBe(
    EXIT.failed
  );
  const files = [`${out}.candidates.jsonl`, `${out}.summary.json`, `${out}.found.jsonl`];
  const before = files.map((file) => readFileSync(file, "utf8"));
  const from = recorder.requests.length;
  const logged = psqlLogLength();
  const rows: [string[], string][] = [
    [["--max-candidates", "30", "--max-search-minutes", "90"], "--max-candidates was 60, now 30"],
    [["--max-candidates", "60"], "--max-search-minutes was 90, now not given"],
    [[], "--max-candidates was 60, now not given; --max-search-minutes was 90, now not given"],
  ];
  for (const [extra, why] of rows) {
    const lines: string[] = [];
    expect({ why, code: await main(tasksArgs(out, ...model, ...extra), io(lines)) }).toEqual({
      why,
      code: EXIT.refused,
    });
    expect(lines.join("\n")).toContain(
      `${out}.candidates.jsonl holds a candidate search made under other bounds (1 candidates decided)`
    );
    expect(lines.join("\n")).toContain(why);
  }
  expect(files.map((file) => readFileSync(file, "utf8"))).toEqual(before);
  expect(recorder.requests.length).toBe(from);
  expect(psqlLogLength()).toBe(logged);

  // A build killed at its first query has no search file; this one holds only the bounds, as a
  // run killed after its search started and before its first decision leaves it. A run with
  // other bounds goes on from it.
  const queried = path.join(scratch, "bound-queried", "tasks.jsonl");
  expect(
    await main(
      tasksArgs(queried, ...bounds),
      killedOn(/^build-tasks: store_a \d{4}-\d{2}-\d{2}: /, [])
    )
  ).toBe(EXIT.failed);
  writeFileSync(
    `${queried}.candidates.jsonl`,
    `${JSON.stringify({ bounds: { maxCandidates: 60, maxSearchMinutes: 90 } })}\n`
  );
  expect(await main(tasksArgs(queried, ...model, "--max-candidates", "2"), io([]))).toBe(EXIT.ok);
  expect(JSON.parse(readFileSync(`${queried}.summary.json`, "utf8")).search).toMatchObject({
    maxCandidates: 2,
    maxSearchMinutes: null,
    ended: "--max-candidates",
    decided: 2,
  });
}, 120_000);

test("build-tasks refuses a bound that is no number, a bound with no candidate search to bound, and a bound with --first-read, before any read", async () => {
  const out = path.join(scratch, "bound-values", "tasks.jsonl");
  const sessions = controlSessionsFile("bound-values.jsonl", ["c-one"]);
  const model = ["--model", "openai/gpt-6.1-sol"];
  const from = recorder.requests.length;
  const logged = psqlLogLength();
  const rows: [string[], string][] = [
    [
      tasksArgs(out, ...model, "--max-candidates", "0"),
      "--max-candidates 0 is not a whole number of 1 or more",
    ],
    [
      tasksArgs(out, ...model, "--max-candidates", "2.5"),
      "--max-candidates 2.5 is not a whole number of 1 or more",
    ],
    [
      tasksArgs(out, ...model, "--max-search-minutes", "0"),
      "--max-search-minutes 0 is not a number of minutes above 0",
    ],
    [
      tasksArgs(out, ...model, "--max-search-minutes", "soon"),
      "--max-search-minutes soon is not a number of minutes above 0",
    ],
    [
      tasksArgs(out, "--max-candidates", "60"),
      "--max-candidates and --max-search-minutes bound the candidate search, which runs only with --model or --profiles-out",
    ],
    [
      [...controlArgs(sessions, out, "1"), "--max-search-minutes", "90"],
      "--first-read takes no --max-search-minutes: it stops once --max requests are kept",
    ],
  ];
  for (const [args, why] of rows) {
    const lines: string[] = [];
    expect({ why, code: await main(args, io(lines)) }).toEqual({ why, code: EXIT.refused });
    expect(lines.join("\n")).toContain(why);
  }
  expect(recorder.requests.length).toBe(from);
  expect(psqlLogLength()).toBe(logged);
  expect(existsSync(out)).toBe(false);
});

test("build-tasks with no bound writes the pinned bytes: the tasks, the summary, the profiles and the printed lines of a run with --model and of one with --profiles-out", async () => {
  /** The SHA-256 of `text`, the suite's scratch directory in it written `<scratch>`. */
  const digest = (text: string) =>
    createHash("sha256").update(text.replaceAll(scratch, "<scratch>")).digest("hex");
  const runs: Record<string, string[]> = {
    model: ["--model", "openai/gpt-6.1-sol"],
    profiles: profileFlags(path.join(scratch, "no-bound-profiles", "profiles")),
  };
  const digests: Record<string, string> = {};
  for (const [name, extra] of Object.entries(runs)) {
    const out = path.join(scratch, `no-bound-${name}`, "tasks.jsonl");
    const lines: string[] = [];
    expect(await main(tasksArgs(out, ...extra), io(lines))).toBe(EXIT.ok);
    expect(existsSync(`${out}.candidates.jsonl`)).toBe(false);
    digests[`${name} tasks`] = digest(readFileSync(out, "utf8"));
    digests[`${name} summary`] = digest(readFileSync(`${out}.summary.json`, "utf8"));
    digests[`${name} printed`] = digest(lines.join(""));
    const profiles = tasksOf(out).flatMap((task) =>
      task.profile === undefined ? [] : [readFileSync(String(task.profile), "utf8")]
    );
    if (profiles.length > 0) digests[`${name} profiles`] = digest(profiles.join(""));
  }
  // The digests of a run with no bound on this input; a change to what build-tasks writes
  // changes them.
  expect(digests).toEqual({
    "model tasks": "ffa3d7637184a555b27e1b14afee9b1dd58e697112f82f98d3819afda80652d9",
    "model summary": "fd2cf470c0eaa72f67f8a326ecfaeafd58a63ed82764f9aae296cfffc84bff8b",
    "model printed": "bf894d108dd96d68557fc836907c898f504618a57f906e073f786c51c1144dc3",
    "profiles tasks": "6978ce55a25eb77c1f126e1b0e35e0f4899d63975797aee26b9e32d53ff3dea0",
    "profiles summary": "d55948c92214318081f732b92f557f7118708818af63b01a92274f485d5bf000",
    "profiles printed": "6e4a4b3f3da89a2faa7e4e21ab71ad7d844a05da52f0aa836ebc41b6baee1532",
    "profiles profiles": "26bf1079e50926d2c9e1bdc9482bca041b0a71392fb091f4352c437ab81932c4",
  });
}, 120_000);

/** A stand-in store holding the suite's files (by store, then path) as `change` leaves them, and
 *  the path of a configuration whose store command reads it; its calls go to the suite's log. */
function changedStore(
  name: string,
  change: (files: Record<string, Record<string, string>>) => void
): string {
  const dir = path.join(setupDir, name);
  mkdirSync(dir, { recursive: true });
  const stores = JSON.parse(readFileSync(path.join(setupDir, "store.json"), "utf8")) as Record<
    string,
    { files: Record<string, string> }
  >;
  change(Object.fromEntries(Object.entries(stores).map(([db, store]) => [db, store.files])));
  writeFileSync(path.join(dir, "store.json"), JSON.stringify(stores));
  writeFileSync(path.join(dir, "psql.ts"), STANDIN_PSQL(path.join(dir, "store.json"), psqlLog));
  const psql = path.join(dir, "psql");
  writeFileSync(
    psql,
    `#!/bin/sh\nexec '${process.execPath}' '${path.join(dir, "psql.ts")}' "$@"\n`
  );
  chmodSync(psql, 0o755);
  const file = path.join(dir, "skill-cuts.json");
  writeFileSync(
    file,
    JSON.stringify({ ...JSON.parse(readFileSync(config, "utf8")), psqlCommand: psql })
  );
  return file;
}

/** `text`, a session's transcript, with its header's working directory `cwd`. */
const inDirectory = (text: string, cwd: string) =>
  text
    .split("\n")
    .map((line) => {
      if (line === "") return line;
      const entry = JSON.parse(line) as Record<string, unknown>;
      return entry.type === "session" ? JSON.stringify({ ...entry, cwd }) : line;
    })
    .join("\n");

test("a session repository whose whole tree GitHub cuts short is read one directory at a time, only where Oh My Pi reads a project, so build-cases --reprofile gives its case that repository's project skill and context file", async () => {
  const legion = (await profileCases()).find(
    (c) => c.source.pathHash === pathHash(sessionPath("legion"))
  ) as Case;
  // The legion session, in a workspace of acme/big, whose whole tree the stand-in cuts short.
  const moved = "/sessions/big-legion.jsonl";
  const big = changedStore("truncated-tree", (files) => {
    (files.store_b ?? {})[moved] = inDirectory(
      sessionText("legion"),
      "/legion/workspaces/acme/big/acme-12"
    );
  });
  const dir = path.join(scratch, "truncated-tree");
  const from = path.join(dir, "old.jsonl");
  mkdirSync(dir, { recursive: true });
  writeFileSync(
    from,
    `${JSON.stringify({ ...legion, source: { ...legion.source, pathHash: pathHash(moved) } })}\n`
  );
  const fresh = path.join(dir, "reprofiled.jsonl");
  const lines: string[] = [];
  const code = await main(
    [
      "build-cases",
      "--config",
      big,
      "--old",
      old,
      "--copies",
      copiesDir,
      "--count",
      countDir,
      "--reprofile",
      from,
      "--out",
      fresh,
    ],
    io(lines)
  );
  expect(lines.join("\n")).not.toContain("cuts the tree");
  expect(code).toBe(EXIT.ok);
  const [c] = loadCases(fresh, old);
  expect(c?.profile.skills).toEqual(
    [
      ...legion.profile.skills,
      { repo: "acme/big", commit: SHA, path: ".claude/skills/omega", name: "omega" },
    ].sort((a, b) => (a.name < b.name ? -1 : 1))
  );
  expect(c?.profile.contextFiles).toEqual([
    { repo: "acme/big", commit: SHA, path: "AGENTS.md" },
    ...legion.profile.contextFiles.filter((file) => file.repo !== "acme/widgets"),
  ]);
  // No whole tree of acme/big is asked for: its root, .claude/, .claude/skills/ and the skill's
  // directory, one level each, and not data/ or the skill's references/.
  expect(
    ghCalls
      .flatMap((args) => args.filter((arg) => arg.startsWith("repos/acme/big/git/trees/")))
      .sort()
  ).toEqual(
    ["", ".claude", ".claude/skills", ".claude/skills/omega"]
      .map((dir) => `repos/acme/big/git/trees/${treeId("acme/big", dir)}`)
      .sort()
  );
}, 120_000);

// Catches: one transport error on a GitHub read leaving a case out of a re-profile, as a commit
// read's TLS handshake timeout did on 2026-11-21, where copies.ts sends the read again; and a name
// that did not resolve, which context.ts's reads sent again before the rule was shared.
test.each([
  ["a TLS handshake timeout", "net/http: TLS handshake timeout", "TLS handshake timeout"],
  ["no such host", "dial tcp: lookup api.github.com: no such host", "no such host"],
])(
  "build-cases --reprofile whose commit read fails once with %s sends it again 5 s later and writes the case, and its summary counts the retry",
  async (_, error, reason) => {
    const [c] = await profileCases();
    const dir = path.join(scratch, `transient-commit-${reason.replaceAll(" ", "-")}`);
    const from = path.join(dir, "old.jsonl");
    mkdirSync(dir, { recursive: true });
    writeFileSync(from, `${JSON.stringify(c)}\n`);
    const fresh = path.join(dir, "reprofiled.jsonl");
    const lines: string[] = [];
    const waits: number[] = [];
    const standin = io(lines);
    let failed: string | undefined;
    const flaky: BuildIo = {
      ...standin,
      // The first commit read fails with `error`; every other call goes to the stand-in.
      gh: async (args) => {
        const route = args.find((arg) => arg.startsWith("repos/")) ?? "";
        if (failed !== undefined || !route.includes("/commits?")) return standin.gh(args);
        ghCalls.push([...args]);
        failed = `Get "https://api.github.com/${route}": ${error}`;
        return { code: 1, stdout: "", stderr: `${failed}\n` };
      },
      sleep: async (ms) => void waits.push(ms),
    };
    const code = await main(
      [
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
        fresh,
      ],
      flaky
    );
    expect(lines.join("")).not.toContain("left out");
    expect(code).toBe(EXIT.ok);
    expect(loadCases(fresh, old).map((each) => each.id)).toEqual([c?.id]);
    expect(waits).toEqual([5_000]);
    expect(lines).toContain(
      `build-cases: a gh call failed (${failed}); sending it again in 5 s, retry 1 of 4`
    );
    expect(JSON.parse(readFileSync(`${fresh}.summary.json`, "utf8"))).toMatchObject({
      casesWritten: 1,
      leftOut: [],
      githubRetries: { [reason]: 1 },
    });
  },
  120_000
);

/** The tasks file a build-tasks run with --profiles-out wrote on the suite's store (t1 from the
 *  session /sessions/task-4.jsonl, F1 as the good session has it, and t2 from
 *  /sessions/task-3.jsonl, F1 as the openai session has it; eight written tasks in the legion
 *  session's context), built once. */
async function profiledTasks(): Promise<string> {
  const out = path.join(scratch, "reprofile-from", "tasks.jsonl");
  if (!existsSync(out)) {
    const dir = path.join(scratch, "reprofile-from", "profiles");
    expect(await main(tasksArgs(out, ...profileFlags(dir)), io([]))).toBe(EXIT.ok);
  }
  return out;
}

/** The arguments of a build-tasks --reprofile run of `from` into `dir`, its configuration
 *  `configFile`. */
const reprofileArgs = (configFile: string, from: string, dir: string, ...extra: string[]) => [
  "build-tasks",
  "--config",
  configFile,
  "--copies",
  copiesDir,
  "--count",
  countDir,
  "--reprofile",
  from,
  "--profiles-out",
  dir,
  ...extra,
];

/** One evidence item. */
interface Item {
  item: string;
  source: string;
}

/** What build-tasks --reprofile records of one task. */
interface Reprofiled {
  task: string;
  from: string;
  was: string | null;
  profile: string | null;
  outcome: string;
  differs: { added: Item[]; removed: Item[] };
}

/** The summary build-tasks --reprofile wrote beside the tasks file of `dir`. */
const reprofileSummary = (dir: string) =>
  JSON.parse(readFileSync(path.join(dir, "tasks.jsonl.summary.json"), "utf8")) as {
    command: string;
    counts: Record<string, number>;
    profiles: { contextSession: Record<string, unknown> | null; tasks: Reprofiled[] };
  };

test("build-tasks --reprofile builds each task's profile again as --profiles-out builds it, with no reader call: on the store the tasks were built from, each is byte for byte the old one and says unchanged, and the new tasks file names the new profiles", async () => {
  const from = await profiledTasks();
  const dir = path.join(scratch, "reprofile-same", "profiles");
  const requests = recorder.requests.length;
  const logged = psqlLogLength();
  const lines: string[] = [];
  expect(await main(reprofileArgs(config, from, dir), io(lines))).toBe(EXIT.ok);
  expect(recorder.requests.length).toBe(requests);
  const tasks = tasksOf(from);
  expect(tasks.filter((task) => task.source === "session")).toHaveLength(2);
  expect(tasksOf(path.join(dir, "tasks.jsonl"))).toEqual(
    tasks.map((task) => ({ ...task, profile: path.resolve(dir, `${task.id}.json`) }))
  );
  for (const task of tasks) {
    expect(readFileSync(path.resolve(dir, `${task.id}.json`), "utf8")).toBe(
      readFileSync(String(task.profile), "utf8")
    );
  }
  const summary = reprofileSummary(dir);
  expect(summary.command).toBe("build-tasks --reprofile");
  expect(
    summary.profiles.tasks.map((each) => [each.task, each.from, each.outcome, each.differs])
  ).toEqual(
    tasks.map((task) => [
      String(task.id),
      task.source === "session" ? "session" : "context-session",
      "unchanged",
      { added: [], removed: [] },
    ])
  );
  expect(summary.counts).toEqual({ changed: 0, unchanged: tasks.length, kept: 0, leftOut: 0 });
  expect(summary.profiles.contextSession).toMatchObject({
    store: "store_b",
    hash: fileHash(sessionPath("legion")),
  });
  // The session tasks' paths by their path hashes, one query of their store; the context session
  // the old summary names by its file hash; and one pull of each of the three sessions.
  expect(
    psqlLines(logged)
      .filter((line) => line.kind !== "pull")
      .map((line) => `${line.db} ${line.kind}`)
      .sort()
  ).toEqual(["store_a paths", "store_a subagents", "store_b sessions", "store_b subagents"]);
  expect(transcriptPulls(logged)).toBe(3);
  expect(existsSync(`${dir}.spool`)).toBe(false);

  // Refused before any read: a flag of the search or the reader, a directory that holds files
  // already, and a tasks file with no summary.
  const other = path.join(scratch, "reprofile-same", "other");
  const bare = path.join(scratch, "reprofile-same", "bare.jsonl");
  writeFileSync(bare, readFileSync(from));
  const refusedAt = psqlLogLength();
  const rows: [string[], string][] = [
    [[...reprofileArgs(config, from, other), "--key-file", keyFile], "so it takes no --key-file"],
    [[...reprofileArgs(config, from, other), "--store", "store_a"], "so it takes no --store"],
    [reprofileArgs(config, from, dir), `--profiles-out ${dir} holds files already`],
    [reprofileArgs(config, bare, other), `${bare}.summary.json is not a file`],
  ];
  for (const [args, why] of rows) {
    const refused: string[] = [];
    expect({ why, code: await main(args, io(refused)) }).toEqual({ why, code: EXIT.refused });
    expect(refused.join("\n")).toContain(why);
  }
  expect(psqlLogLength()).toBe(refusedAt);
  expect(existsSync(other)).toBe(false);
}, 120_000);

test("build-tasks --reprofile of a task whose session is now in a checkout of acme/proj gives it a profile that names acme/proj at its commit and lists its project skills and root CLAUDE.md, says changed and lists the evidence items that differ; the other tasks are unchanged", async () => {
  const from = await profiledTasks();
  const task4 = "/sessions/task-4.jsonl";
  const moved = changedStore("reprofile-moved", (files) => {
    (files.store_a ?? {})[task4] = inDirectory(sessionText("good"), "/home/dev/src/proj");
  });
  const dir = path.join(scratch, "reprofile-moved", "profiles");
  const lines: string[] = [];
  expect(await main(reprofileArgs(moved, from, dir), io(lines))).toBe(EXIT.ok);
  const t1 = tasksOf(from).find((task) => task.id === "t1");
  expect(t1?.pathHash).toBe(pathHash(task4));
  const before = JSON.parse(readFileSync(String(t1?.profile), "utf8"));
  expect(JSON.parse(readFileSync(path.resolve(dir, "t1.json"), "utf8"))).toEqual({
    ...before,
    skills: [
      { repo: "acme/skills", commit: SHA, path: "skills/older", name: "older", namespace: "agent" },
      { repo: "acme/skills", commit: SHA, path: "skills/beta", name: "beta" },
      {
        repo: "acme/proj",
        commit: SHA,
        path: ".claude/skills/beta",
        name: "beta",
        namespace: "claude",
      },
      { repo: "acme/proj", commit: SHA, path: ".claude/skills/kappa", name: "kappa" },
      { repo: "acme/proj", commit: SHA, path: ".omp/skills/lambda", name: "lambda" },
      { repo: "acme/proj", commit: SHA, path: ".omp/skills/older", name: "older" },
    ],
    contextFiles: [
      { repo: "acme/skills", commit: SHA, path: ".claude/CLAUDE.md", user: true },
      { repo: "acme/proj", commit: SHA, path: "CLAUDE.md" },
    ],
    sessionRepository: { repo: "acme/proj", commit: SHA },
  });
  const summary = reprofileSummary(dir);
  const record = summary.profiles.tasks.find((each) => each.task === "t1");
  expect(record?.outcome).toBe("changed");
  expect(record?.differs.added.map((each) => each.item)).toEqual(
    expect.arrayContaining([
      "session repository acme/proj",
      "skill kappa (acme/proj .claude/skills/kappa)",
      "skill older (acme/proj .omp/skills/older)",
      "context file acme/proj CLAUDE.md",
    ])
  );
  expect(record?.differs.removed.map((each) => each.item)).toEqual(
    expect.arrayContaining(["no session repository", "skill older (acme/skills skills/older)"])
  );
  expect(
    summary.profiles.tasks.filter((each) => each.task !== "t1").map((each) => each.outcome)
  ).toEqual(Array(9).fill("unchanged"));
  expect(summary.counts).toMatchObject({ changed: 1, unchanged: 9 });
  expect(lines.join("")).toContain("build-tasks: t1: profile changed");
}, 120_000);

test("build-tasks --reprofile gives each written task the profile of the --context-session it is given, in place of the session the tasks file's summary names, and lists the evidence items that differ", async () => {
  const from = await profiledTasks();
  const dir = path.join(scratch, "reprofile-context", "profiles");
  const task4 = fileHash("/sessions/task-4.jsonl");
  expect(
    await main(reprofileArgs(config, from, dir, "--context-session", `store_a:${task4}`), io([]))
  ).toBe(EXIT.ok);
  const tasks = tasksOf(from);
  const written = tasks.filter((task) => task.source === "written");
  expect(written).toHaveLength(8);
  // task-4's session is F1 as the good session has it: its profile is t1's, not the legion one.
  const good = readFileSync(path.resolve(dir, "t1.json"), "utf8");
  const summary = reprofileSummary(dir);
  for (const task of written) {
    expect(readFileSync(String(task.profile), "utf8")).not.toBe(good);
    expect(readFileSync(path.resolve(dir, `${task.id}.json`), "utf8")).toBe(good);
    const record = summary.profiles.tasks.find((each) => each.task === task.id);
    expect(record?.from).toBe("context-session");
    expect(record?.outcome).toBe("changed");
    expect(record?.differs.added.map((each) => each.item)).toContain("no role");
    expect(record?.differs.removed.map((each) => each.item)).toContain("role tester");
  }
  expect(summary.profiles.contextSession).toMatchObject({
    store: "store_a",
    hash: task4,
    pathHash: pathHash("/sessions/task-4.jsonl"),
    context: "devbox",
  });
  expect(summary.counts).toMatchObject({ changed: 8, unchanged: 2 });
}, 120_000);

/** A copy of the suite's copies output in `<setupDir>/<name>`, with `rows` added at the end of
 *  each file they name. */
function copiesWith(
  name: string,
  rows: Readonly<Record<string, readonly (readonly (string | number)[])[]>>
): string {
  const dir = path.join(setupDir, name);
  mkdirSync(dir, { recursive: true });
  for (const file of [
    "prefix-table.tsv",
    "installers.tsv",
    "manifests.tsv",
    "loaders.tsv",
    "skill-copies.tsv",
    "repositories.tsv",
  ]) {
    const added = (rows[file] ?? []).map((row) => `${row.join("\t")}\n`).join("");
    writeFileSync(
      path.join(dir, file),
      `${readFileSync(path.join(copiesDir, file), "utf8")}${added}`
    );
  }
  return dir;
}

/** The start of F1, so of every session built from it. */
const F1_START = "2026-11-20T01:59:41.780Z";

/** The rows of a copies output where acme/home is the dev box's repository and, by the copies
 *  output's time, its installer installs fresh-one from acme/fresh, whose first commit
 *  (FRESH_MADE) came after every session's start. */
const FRESH_TARGET = {
  "manifests.tsv": [
    ["acme/home", "installers/omp.sh", "installer-script", "tree", 4, "links skills", ""],
    ["acme/home", ".claude/settings.json", "claude-settings", "tree", 0, "none", ""],
  ],
  "installers.tsv": [
    [
      "acme/home",
      "installers/omp.sh",
      "installer-script",
      "installs",
      "gh-skill:fresh-one",
      "omp",
      "fresh-one",
      "acme/fresh",
      "skills/fresh-one",
      "",
    ],
  ],
  "skill-copies.tsv": [["acme/fresh", "skills/fresh-one", "fresh-one", 1, "no", "no", "", "", ""]],
  "repositories.tsv": [["acme/fresh", "main", SHA, "no", "no", "", 1, "whole", 1, ""]],
};

/** The evidence item that leaves acme/fresh out of a profile of a session built from F1. */
const FRESH_LEFT_OUT = {
  item: `left out: acme/fresh, which has no commit at or before the session's start ${F1_START}`,
  source: `main, the ref repositories.tsv names, has no commit at or before the session's start; gh api repos/acme/fresh/commits?sha=main&until=${F1_START} gives no commit of main (from gh api repos/acme/fresh): GitHub holds no commit of acme/fresh then, so nothing of it was installed then, and the profile installs nothing of it`,
};

// Catches: a repository the dev box's installers install by the copies output's time, whose first
// commit came after the session's start, failing the whole build: GitHub gives no commit of it at
// or before that start.
test("a dev-box session that started before the first commit of a repository its dev box's installers install later gets its profile: that repository is left out with the evidence, and no profile lists a skill of it", async () => {
  const copies = copiesWith("copies-fresh", FRESH_TARGET);
  const from = await profiledTasks();
  const dir = path.join(scratch, "fresh-target", "profiles");
  const context = `store_a:${fileHash("/sessions/task-4.jsonl")}`;
  const said: string[] = [];
  const code = await main(
    [
      "build-tasks",
      "--config",
      config,
      "--copies",
      copies,
      "--count",
      countDir,
      "--reprofile",
      from,
      "--profiles-out",
      dir,
      "--context-session",
      context,
    ],
    io(said)
  );
  expect({ code, said: code === EXIT.ok ? "" : said.join("\n") }).toEqual({
    code: EXIT.ok,
    said: "",
  });
  const evidence = (reprofileSummary(dir).profiles.contextSession?.evidence ?? []) as Item[];
  expect(evidence).toContainEqual(FRESH_LEFT_OUT);
  expect(evidence.filter((each) => each.item.includes("acme/fresh"))).toEqual([FRESH_LEFT_OUT]);
  // Each session task's profile is built the same way.
  for (const record of reprofileSummary(dir).profiles.tasks) {
    if (record.from === "session") expect(record.differs.added).toContainEqual(FRESH_LEFT_OUT);
  }
  // No profile written holds a skill of acme/fresh; each holds acme/home's linked skills.
  for (const task of tasksOf(path.join(dir, "tasks.jsonl"))) {
    const profile = JSON.parse(readFileSync(String(task.profile), "utf8"));
    const repos = profile.skills.map((skill: { repo: string }) => skill.repo);
    expect(repos).not.toContain("acme/fresh");
    expect(repos).toContain("acme/home");
  }
}, 120_000);

// Catches: the same repository leaving each case of build-cases --reprofile out, as a GitHub read
// the profile needs that failed, where only that repository is left out of the profile.
test("build-cases --reprofile of a dev-box case whose session started before the first commit of a repository its dev box's installers install later writes the case: that repository is left out with the evidence, and the profile holds no skill of it", async () => {
  const good = (await profileCases()).find(
    (c) => c.source.pathHash === pathHash(sessionPath("good"))
  ) as Case;
  const dir = path.join(scratch, "fresh-target-cases");
  const from = path.join(dir, "old.jsonl");
  mkdirSync(dir, { recursive: true });
  writeFileSync(from, `${JSON.stringify(good)}\n`);
  const fresh = path.join(dir, "reprofiled.jsonl");
  const lines: string[] = [];
  const code = await main(
    [
      "build-cases",
      "--config",
      config,
      "--old",
      old,
      "--copies",
      copiesWith("copies-fresh", FRESH_TARGET),
      "--count",
      countDir,
      "--reprofile",
      from,
      "--out",
      fresh,
    ],
    io(lines)
  );
  expect({ code, said: code === EXIT.ok ? "" : lines.join("\n") }).toEqual({
    code: EXIT.ok,
    said: "",
  });
  const [c] = loadCases(fresh, old);
  expect(c?.id).toBe(good.id);
  expect(c?.evidence).toContainEqual(FRESH_LEFT_OUT);
  const repos = c?.profile.skills.map((skill) => skill.repo);
  expect(repos).not.toContain("acme/fresh");
  expect(repos).toContain("acme/home");
}, 120_000);

// Catches: a session repository with no commit at the session's start failing the whole build, or
// a later commit of it read in its place.
test("a session whose working directory is a checkout of a repository with no commit at its start gets its profile without that repository's project skills and context files; the evidence says why, and the listing measure counts the missing project skill", async () => {
  const copies = copiesWith("copies-newproj", {
    "repositories.tsv": [["acme/newproj", "main", SHA, "no", "no", "", 1, "recursive", 1, ""]],
  });
  // The good session in a checkout of acme/newproj, whose first commit (FRESH_MADE) came after
  // the session's start, and a subagent of it whose recorded listing holds acme/newproj's nova.
  const session = "/sessions/newproj.jsonl";
  const store = changedStore("newproj-store", (files) => {
    const storeA = files.store_a ?? {};
    storeA[session] = inDirectory(sessionText("good"), "/home/dev/src/newproj");
    storeA["/sessions/newproj/Helper.jsonl"] = sessionText("subagent").replace(
      "- beta: Use for the beta greeting.\\n",
      "- beta: Use for the beta greeting.\\n- nova: Use when the project asks for nova.\\n"
    );
  });
  const from = await profiledTasks();
  const dir = path.join(scratch, "newproj-context", "profiles");
  const callsBefore = ghCalls.length;
  const said: string[] = [];
  const code = await main(
    [
      "build-tasks",
      "--config",
      store,
      "--copies",
      copies,
      "--count",
      countDir,
      "--reprofile",
      from,
      "--profiles-out",
      dir,
      "--context-session",
      `store_a:${fileHash(session)}`,
    ],
    io(said)
  );
  expect({ code, said: code === EXIT.ok ? "" : said.join("\n") }).toEqual({
    code: EXIT.ok,
    said: "",
  });
  const evidence = (reprofileSummary(dir).profiles.contextSession?.evidence ?? []) as Item[];
  const items = evidence.map((each) => each.item);
  expect(items).toContain("session repository acme/newproj");
  expect(evidence).toContainEqual({
    item: `left out: acme/newproj, which has no commit at or before the session's start ${F1_START}`,
    source: `main, the ref repositories.tsv names, has no commit at or before the session's start; gh api repos/acme/newproj/commits?sha=main&until=${F1_START} gives no commit of main (from gh api repos/acme/newproj): GitHub holds no commit of acme/newproj then, so its project skills and context files are not read, and the profile names no session repository`,
  });
  const written = tasksOf(from).find((task) => task.source === "written");
  const profile = JSON.parse(readFileSync(path.resolve(dir, `${written?.id}.json`), "utf8"));
  expect(profile.sessionRepository).toBeUndefined();
  expect(profile.workingDirectory).toBeUndefined();
  expect(
    [...profile.skills, ...profile.contextFiles].filter(
      (each: { repo: string }) => each.repo === "acme/newproj"
    )
  ).toEqual([]);
  // No later commit of acme/newproj is read in its place: only its commit at the start is asked
  // for, and no tree or file of it.
  const asked = ghCalls
    .slice(callsBefore)
    .filter((args) =>
      args.some((arg) => arg.startsWith("repos/acme/newproj/") || arg === "name=newproj")
    );
  expect(asked.length).toBeGreaterThan(0);
  expect(
    asked.every((args) => args.some((arg) => arg.startsWith("repos/acme/newproj/commits?")))
  ).toBe(true);
  // The subagent's recorded listing holds nova, which the profile does not list.
  const measure = ListingMeasure.parse(
    JSON.parse(evidence.find((each) => each.item === LISTING_MEASURE)?.source ?? "{}")
  );
  expect(measure.kind).toBe("subagent listing");
  expect(measure.missing).toContain("nova");
}, 120_000);

test("build-tasks --reprofile keeps the old profile of a task whose session its store holds no longer, and of each written task when the context session is gone, each with an evidence item that says so; a store read that fails fails the run", async () => {
  const from = await profiledTasks();
  const task4 = "/sessions/task-4.jsonl";
  const gone = changedStore("reprofile-gone", (files) => {
    delete files.store_a?.[task4];
    delete files.store_b?.[sessionPath("legion")];
  });
  const dir = path.join(scratch, "reprofile-gone", "profiles");
  const lines: string[] = [];
  expect(await main(reprofileArgs(gone, from, dir), io(lines))).toBe(EXIT.ok);
  const tasks = tasksOf(from);
  for (const task of tasks) {
    expect(readFileSync(path.resolve(dir, `${task.id}.json`), "utf8")).toBe(
      readFileSync(String(task.profile), "utf8")
    );
  }
  const summary = reprofileSummary(dir);
  const byTask = new Map(summary.profiles.tasks.map((each) => [each.task, each]));
  // A kept task's evidence says why, and its listing measure is not measurable for that reason;
  // the measure its old build wrote is the one item it drops.
  const kept = (task: string, gone: Evidence) => {
    const note = byTask.get(task) as {
      outcome: string;
      differs: { added: Evidence[]; removed: Evidence[] };
    };
    expect(note.outcome).toBe("kept");
    expect(note.differs.added.map((each) => each.item)).toEqual([gone.item, LISTING_MEASURE]);
    expect(note.differs.added[0]).toEqual(gone);
    const measure = ListingMeasure.parse(JSON.parse(note.differs.added[1]?.source ?? "{}"));
    expect(measure.kind).toBe("not measurable");
    expect(measure.why).toEndWith(gone.source);
    expect(note.differs.removed.map((each) => each.item)).toEqual([LISTING_MEASURE]);
  };
  kept("t1", {
    item: "no session in the store",
    source: `the store store_a holds no file of the path hash ${pathHash(task4)} now (session-paths.sql), so the task keeps its old profile`,
  });
  expect(byTask.get("t2")?.outcome).toBe("unchanged");
  for (const task of tasks.filter((each) => each.source === "written")) {
    kept(String(task.id), {
      item: "no session in the store",
      source: `the store store_b holds no file of the hash ${fileHash(sessionPath("legion"))} now (sessions.sql), so each written task keeps its old profile`,
    });
  }
  expect(summary.counts).toEqual({ changed: 0, unchanged: 1, kept: 9, leftOut: 0 });

  // A store command that fails fails the run, and no tasks file is written.
  const failing = path.join(setupDir, "reprofile-failing");
  mkdirSync(failing, { recursive: true });
  const command = path.join(failing, "psql");
  writeFileSync(command, "#!/bin/sh\necho 'psql: error: gone' >&2\nexit 3\n");
  chmodSync(command, 0o755);
  const broken = path.join(failing, "skill-cuts.json");
  writeFileSync(
    broken,
    JSON.stringify({ ...JSON.parse(readFileSync(config, "utf8")), psqlCommand: command })
  );
  const failed = path.join(scratch, "reprofile-failing", "profiles");
  const said: string[] = [];
  expect(await main(reprofileArgs(broken, from, failed), io(said))).toBe(EXIT.failed);
  expect(said.join("\n")).toContain("the store command exited 3: psql: error: gone");
  expect(existsSync(path.join(failed, "tasks.jsonl"))).toBe(false);
}, 120_000);

// Catches: a context session whose rebuilt context does not list the skill refused (exit 2) after
// the store read that found it, where exit 2 is a refusal before any store read.
test("build-tasks --reprofile fails (exit 1) on a --context-session whose rebuilt context does not list the skill, naming why, and writes no tasks file", async () => {
  const from = await profiledTasks();
  const unlisted = fileHash("/sessions/task-1.jsonl");
  const dir = path.join(scratch, "reprofile-unlisted-context", "profiles");
  const said: string[] = [];
  const requests = recorder.requests.length;
  expect(
    await main(
      reprofileArgs(config, from, dir, "--context-session", `store_a:${unlisted}`),
      io(said)
    )
  ).toBe(EXIT.failed);
  expect(said.join("\n")).toContain(
    `the context session store_a:${unlisted}: its rebuilt context (subagent:task) does not list alpha`
  );
  expect(recorder.requests.length).toBe(requests);
  expect(existsSync(path.join(dir, "tasks.jsonl"))).toBe(false);
}, 120_000);

// Catches: a candidate's drop reason taken from the flags given, not from the step that failed.
test("build-tasks drops a candidate whose pull gives no row as pull-failed and one whose transcript does not parse as unparseable, with --model and with --profiles-out", async () => {
  const broken = changedStore("candidate-drops", (files) => {
    const store = files.store_a ?? {};
    store["/sessions/task-1.jsonl"] = `not one entry\n${sessionText("good")}`;
    store["/sessions/task-4.jsonl"] = "";
  });
  const args = (out: string, ...extra: string[]) => [
    "build-tasks",
    ...["--config", broken, "--key-file", keyFile, "--skill", old, "--store", "store_a"],
    ...["--out", out, ...extra],
  ];
  /** Each candidate drop the summary beside `out` records: its path hash and reason. */
  const drops = (out: string) => {
    // The summary build-tasks wrote, read as the file's other tests read one.
    const summary = JSON.parse(readFileSync(`${out}.summary.json`, "utf8")) as {
      candidateDrops: { sessions: { pathHash: string; reason: string }[] };
    };
    return summary.candidateDrops.sessions.map((each) => [each.pathHash, each.reason]);
  };
  const wanted = [
    [pathHash("/sessions/task-1.jsonl"), "unparseable"],
    [pathHash("/sessions/task-4.jsonl"), "pull-failed"],
  ];
  const model = path.join(scratch, "candidate-drops-model", "tasks.jsonl");
  expect(await main(args(model, "--model", "openai/gpt-6.1-sol"), io([]))).toBe(EXIT.ok);
  expect(drops(model)).toEqual(wanted);
  const profiled = path.join(scratch, "candidate-drops-profiles", "tasks.jsonl");
  const profiles = [
    ...["--profiles-out", path.join(scratch, "candidate-drops-profiles", "profiles")],
    ...["--copies", copiesDir, "--count", countDir],
    ...["--context-session", `store_b:${fileHash(sessionPath("legion"))}`],
  ];
  expect(await main(args(profiled, ...profiles), io([]))).toBe(EXIT.ok);
  expect(drops(profiled)).toEqual(wanted);
}, 120_000);

/** The start of the made-up long-lived sessions of the read-time rows: before acme/newproj's first
 *  commit (FRESH_MADE). */
const LONG_START = "2026-11-20T02:50:00.000Z";
/** Their prompt builds' system-prompt-digest lines: the first reply's, one after a reload past
 *  FRESH_MADE, and a later one. */
const LONG_BUILDS = [
  { at: "2026-11-20T02:50:30.000Z", digest: "5e1d000000000001" },
  { at: "2026-11-20T03:10:00.000Z", digest: "5e1d000000000002" },
  { at: "2026-11-20T04:00:00.000Z", digest: "5e1d000000000003" },
];
/** The path of the made-up long-lived session `id`, named as Oh My Pi names a top-level one. */
const longPath = (id: string) =>
  `/sessions/newproj/2026-11-20T02-50-00-000Z_0a1b2c3d-0000-4000-8000-0000000000${id}.jsonl`;

/** `text`, a session's transcript, with its header's working directory `cwd` and start `start`,
 *  and with `digests` as its system-prompt-digest lines in place of its own. */
function longSession(
  text: string,
  start: string,
  digests: readonly { at: string; digest: string }[]
): string {
  const lines = text
    .split("\n")
    .filter((line) => line !== "" && !line.includes('"customType":"system-prompt-digest"'))
    .map((line) => JSON.parse(line) as Record<string, unknown>);
  const header = lines.findIndex((line) => line.type === "session");
  lines[header] = { ...lines[header], timestamp: start, cwd: "/home/dev/src/newproj" };
  lines.splice(
    header + 1,
    0,
    ...digests.map(({ at, digest }, n) => ({
      type: "custom",
      customType: "system-prompt-digest",
      data: digest,
      id: `5e1d00${n}0`,
      parentId: null,
      timestamp: at,
    }))
  );
  return lines.map((line) => `${JSON.stringify(line)}\n`).join("");
}

/** A subagent's transcript that started at `start`, whose recorded listing holds alpha, beta and,
 *  with `nova`, acme/newproj's nova. */
const longSubagent = (start: string, nova: boolean) =>
  longSession(
    nova
      ? sessionText("subagent").replace(
          "- beta: Use for the beta greeting.\\n",
          "- beta: Use for the beta greeting.\\n- nova: Use when the project asks for nova.\\n"
        )
      : sessionText("subagent"),
    start,
    []
  );

/** The earliest subagent of the long-lived sessions, spawned under their first build, and one
 *  spawned under their second. */
const EARLY_SPAWN = "2026-11-20T02:55:00.000Z";
const LATER_SPAWN = "2026-11-20T03:15:00.000Z";

/** The stand-in store of the read-time rows: session a1 with a subagent under each of its first
 *  two builds, b2 with only the first, and c3 with only the second; each a checkout of
 *  acme/newproj. */
function longStore(): string {
  return changedStore("long-store", (files) => {
    const storeA = files.store_a ?? {};
    for (const id of ["a1", "b2", "c3"]) {
      const file = longPath(id);
      const dir = file.slice(0, -".jsonl".length);
      storeA[file] = longSession(sessionText("good"), LONG_START, LONG_BUILDS);
      if (id !== "c3") storeA[`${dir}/Early.jsonl`] = longSubagent(EARLY_SPAWN, false);
      if (id !== "b2") storeA[`${dir}/Later.jsonl`] = longSubagent(LATER_SPAWN, true);
    }
  });
}

/** The copies output of the read-time rows: the suite's, with acme/newproj listed. */
const longCopies = () =>
  copiesWith("copies-long", {
    "repositories.tsv": [["acme/newproj", "main", SHA, "no", "no", "", 1, "recursive", 1, ""]],
  });

/** The good session's case moved to the long-lived session `id`: its step's message at `step`,
 *  under the prompt whose digest is `digest`. */
async function longCase(id: string, session: string, step: string, digest: string | null) {
  const good = (await profileCases()).find(
    (c) => c.source.pathHash === pathHash(sessionPath("good"))
  ) as Case;
  return {
    ...good,
    id,
    source: { ...good.source, pathHash: pathHash(longPath(session)) },
    sessionStart: LONG_START,
    sessionDigest: digest,
    realStep: { ...good.realStep, timestamp: Date.parse(step) },
  };
}

/** build-cases --reprofile of the read-time rows' cases, run once: their rebuilt cases by id. */
let longCases: Promise<Map<string, Case>> | undefined;
function reprofiledLong(): Promise<Map<string, Case>> {
  longCases ??= (async () => {
    const cases = [
      // Its step after the second line: that build's.
      await longCase("long-second", "a1", "2026-11-20T03:20:00.000Z", LONG_BUILDS[1]?.digest ?? ""),
      // The first reply under the second build, whose line came after its message's time.
      await longCase("long-own", "a1", "2026-11-20T03:05:00.000Z", LONG_BUILDS[1]?.digest ?? ""),
      // Its step before every line.
      await longCase("long-before", "a1", "2026-11-20T02:50:10.000Z", null),
      // Its step after the second line, in a session with no subagent under that build.
      await longCase("long-none", "b2", "2026-11-20T03:20:00.000Z", LONG_BUILDS[1]?.digest ?? ""),
    ];
    const from = path.join(scratch, "long", "old.jsonl");
    mkdirSync(path.dirname(from), { recursive: true });
    writeFileSync(from, cases.map((c) => `${JSON.stringify(c)}\n`).join(""));
    const out = path.join(scratch, "long", "reprofiled.jsonl");
    const lines: string[] = [];
    const args = ["build-cases", "--config", longStore(), "--old", old, "--copies", longCopies()];
    const code = await main(
      [...args, "--count", countDir, "--reprofile", from, "--out", out],
      io(lines)
    );
    expect({ code, said: code === EXIT.ok ? "" : lines.join("") }).toEqual({
      code: EXIT.ok,
      said: "",
    });
    return new Map(loadCases(out, old).map((c) => [c.id, c]));
  })();
  return longCases;
}

/** Case `id`'s evidence, by item. */
const longEvidence = async (id: string) =>
  Object.fromEntries(
    ((await reprofiledLong()).get(id)?.evidence ?? []).map((each) => [each.item, each.source])
  );

/** Case `id`'s listing measure. */
const longMeasure = async (id: string) =>
  ListingMeasure.parse(JSON.parse((await longEvidence(id))[LISTING_MEASURE] ?? "{}"));

/** The names of the skills of case `id`'s profile, with their repositories. */
const longSkills = async (id: string) =>
  ((await reprofiledLong()).get(id)?.profile.skills ?? []).map(
    (each) => `${each.name} ${each.repo}`
  );

// Catches: a top-level case of a long-lived session read at the session's start, not at the prompt
// its step ran under, so its profile lacks what the box gained before that build.
test("a top-level case whose step comes after its session's second system-prompt-digest line is read at that line's time, and its listing is measured against the earliest subagent spawned under that build", async () => {
  const [, second, third] = LONG_BUILDS;
  // acme/newproj's first commit came after the session's start and before that build.
  expect(await longSkills("long-second")).toContain("nova acme/newproj");
  expect(await longMeasure("long-second")).toMatchObject({
    kind: "subagent listing",
    subagent: { pathHash: pathHash(`${longPath("a1").slice(0, -6)}/Later.jsonl`) },
  });
  const evidence = await longEvidence("long-second");
  expect(evidence["read times"]).toStartWith(
    `the step's message is at 2026-11-20T03:20:00.000Z, and the last system-prompt-digest line at or before it is at ${second?.at}; so it is the build of line 2 of the session's 3 system-prompt-digest lines, at ${second?.at}, digest ${second?.digest}, and each repository, a pool's too, is read at ${second?.at}: Oh My Pi 18.18.6 writes a system-prompt-digest line`
  );
  expect(evidence[`commit ${SHA} of acme/skills`]).toEndWith(
    `at or before the prompt build its step ran under ${second?.at}: gh api repos/acme/skills/commits?sha=main&until=${second?.at}`
  );
  expect((await longMeasure("long-second")).why).toContain(
    `spawned under the prompt build its profile is read at (from its line, at ${second?.at}, to the next line, at ${third?.at})`
  );
}, 120_000);

// Catches: the first reply under a new build read at the build before it: its message's time comes
// before the line Oh My Pi writes as that reply ends.
test("a top-level case whose step was the first reply under a build, its message before that build's line, is read at that line, which holds the case's digest", async () => {
  const [first, second] = LONG_BUILDS;
  expect(await longSkills("long-own")).toContain("nova acme/newproj");
  const evidence = await longEvidence("long-own");
  expect(evidence["read times"]).toStartWith(
    `the step's message is at 2026-11-20T03:05:00.000Z; the last system-prompt-digest line at or before it, at ${first?.at}, holds ${first?.digest}, not the case's digest ${second?.digest}, and the next, at ${second?.at}, holds it: the step was the first reply under that build; so it is the build of line 2 of the session's 3 system-prompt-digest lines, at ${second?.at}`
  );
}, 120_000);

// Catches: a case whose step precedes every line read at a later build, or measured against a
// subagent of a later build.
test("a top-level case whose step precedes every system-prompt-digest line of its session is read at the session's start, its first build, and measured against a subagent spawned under that build", async () => {
  const [first, second] = LONG_BUILDS;
  const evidence = await longEvidence("long-before");
  expect(evidence["read times"]).toStartWith(
    `the step's message is at 2026-11-20T02:50:10.000Z, before every system-prompt-digest line of the session; so it is the session's first build (its line is the first of the session's 3 system-prompt-digest lines, at ${first?.at}, digest ${first?.digest}), and each repository, a pool's too, is read at the session's start ${LONG_START}`
  );
  // acme/newproj had no commit at the session's start.
  expect(
    evidence[
      `left out: acme/newproj, which has no commit at or before the session's start ${LONG_START}`
    ]
  ).toBeDefined();
  expect(await longSkills("long-before")).not.toContain("nova acme/newproj");
  expect(await longMeasure("long-before")).toMatchObject({
    kind: "subagent listing",
    subagent: { pathHash: pathHash(`${longPath("a1").slice(0, -6)}/Early.jsonl`) },
  });
  expect((await longMeasure("long-before")).why).toContain(
    `spawned under the session's first prompt build, the one its profile is read at (before the second line, at ${second?.at})`
  );
}, 120_000);

// Catches: a top-level session measured against a subagent of another build when none was spawned
// under the build its profile is read at.
test("a top-level case whose session spawned no subagent with a recorded listing under the build its profile is read at is not measurable, and says why; a subagent of an earlier build is not used", async () => {
  const measure = await longMeasure("long-none");
  expect(measure).toMatchObject({ kind: "not measurable", recorded: null, subagent: null });
  expect(measure.why).toEndWith(
    `the store holds no subagent of it whose recorded prompt holds a skill listing spawned under the prompt build its profile is read at (from its line, at ${LONG_BUILDS[1]?.at}, to the next line, at ${LONG_BUILDS[2]?.at}) (subagents.sql: 1 of its 1 subagents record one, 0 of them spawned then), and the listing of a subagent spawned under another build is never used`
  );
}, 120_000);

// Catches: a context session read at its start while its listing measure takes a subagent spawned
// under a later build, so the measure compares two times.
test("a context session is read at the build its measured subagent was spawned under, the earliest whose recorded prompt holds a listing, and its measure compares that build with itself", async () => {
  const from = await profiledTasks();
  const dir = path.join(scratch, "long-context", "profiles");
  const said: string[] = [];
  const code = await main(
    [
      "build-tasks",
      "--config",
      longStore(),
      "--copies",
      longCopies(),
      "--count",
      countDir,
      "--reprofile",
      from,
      "--profiles-out",
      dir,
      "--context-session",
      `store_a:${fileHash(longPath("c3"))}`,
    ],
    io(said)
  );
  expect({ code, said: code === EXIT.ok ? "" : said.join("\n") }).toEqual({
    code: EXIT.ok,
    said: "",
  });
  const second = LONG_BUILDS[1];
  const later = pathHash(`${longPath("c3").slice(0, -6)}/Later.jsonl`);
  // acme/newproj, which had its first commit by then, gives the profile nova, which the subagent's
  // recorded listing holds: no name is missing.
  const written = tasksOf(from).find((task) => task.source === "written");
  const profile = JSON.parse(readFileSync(path.resolve(dir, `${written?.id}.json`), "utf8"));
  expect(profile.skills.map((each: { name: string }) => each.name)).toContain("nova");
  const evidence = (reprofileSummary(dir).profiles.contextSession?.evidence ?? []) as Item[];
  const sources = Object.fromEntries(evidence.map((each) => [each.item, each.source]));
  const measure = ListingMeasure.parse(JSON.parse(sources[LISTING_MEASURE] ?? "{}"));
  expect(measure).toMatchObject({ kind: "subagent listing", missing: [] });
  expect(measure.subagent?.pathHash).toBe(later);
  expect(sources["read times"]).toStartWith(
    `its subagent ${later}, the earliest whose recorded prompt holds a skill listing and the one its listing measure uses, started at ${LATER_SPAWN}, under the build of the last system-prompt-digest line before then (${second?.at}); so it is the build of line 2 of the session's 3 system-prompt-digest lines, at ${second?.at}, digest ${second?.digest}, and each repository, a pool's too, is read at ${second?.at}`
  );
}, 120_000);

// Catches: a task the reader picked from a session measured as if the store held no subagent of
// its session, when the store was never asked: its session's path was not known there.
test("build-tasks in the reader's mode measures a task from a session against the subagent of that session the store gives, and says the store holds none only after asking it", async () => {
  const store = changedStore("reader-subagent", (files) => {
    (files.store_a ?? {})["/sessions/task-4/Helper.jsonl"] = sessionText("subagent");
  });
  const out = path.join(scratch, "reader-subagent", "tasks.jsonl");
  const dir = path.join(scratch, "reader-subagent", "profiles");
  const said: string[] = [];
  const args = tasksArgs(out, ...profileFlags(dir)).map((arg) => (arg === config ? store : arg));
  expect(await main(args, io(said))).toBe(EXIT.ok);
  const summary = JSON.parse(readFileSync(`${out}.summary.json`, "utf8")) as {
    profiles: { tasks: { task: string; evidence?: Item[] }[] };
  };
  const measureOf = (session: string) => {
    const task = tasksOf(out).find((each) => each.pathHash === pathHash(session));
    const note = summary.profiles.tasks.find((each) => each.task === task?.id);
    const item = note?.evidence?.find((each) => each.item === LISTING_MEASURE);
    return ListingMeasure.parse(JSON.parse(item?.source ?? "{}"));
  };
  // task-4's session started Helper, whose recorded listing holds alpha and beta.
  expect(measureOf("/sessions/task-4.jsonl")).toMatchObject({
    kind: "subagent listing",
    recorded: 2,
    subagent: { pathHash: pathHash("/sessions/task-4/Helper.jsonl") },
  });
  // task-3's session started none, and the store, asked, says so.
  const none = measureOf("/sessions/task-3.jsonl");
  expect(none.kind).toBe("not measurable");
  expect(none.why).toEndWith(
    "the store holds no subagent of it (subagents.sql, asked for those spawned under the session's first prompt build, the one its profile is read at (no later line))"
  );
}, 120_000);

// Catches: a context session that no subagent places at a build said to be read at its first build
// because "a task runs as its session's first message": a context session has no step and is no
// task's session.
test("a context session no subagent of which records a skill listing is read at its first build, and its read times and listing measure say why: no subagent names the build, the store having been asked for those spawned at any time", async () => {
  const from = await profiledTasks();
  const summary = JSON.parse(readFileSync(`${from}.summary.json`, "utf8")) as {
    profiles: { contextSession: { evidence: Item[] } };
  };
  const sources = Object.fromEntries(
    summary.profiles.contextSession.evidence.map((each) => [each.item, each.source])
  );
  // The legion session: one system-prompt-digest line, and no file below its directory.
  const asked =
    "the store holds no subagent of it (subagents.sql, asked for those spawned at any time)";
  expect(sources["read times"]).toStartWith(
    `a context session has no step, and no subagent names the build it is read at: ${asked}; so it is the session's first build (its line is the first of the session's 1 system-prompt-digest line, at 2026-11-20T01:59:48.959Z, digest 46874d907e557998), and each repository, a pool's too, is read at the session's start ${F1_START}`
  );
  const measure = ListingMeasure.parse(JSON.parse(sources[LISTING_MEASURE] ?? "{}"));
  expect(measure.kind).toBe("not measurable");
  expect(measure.why).toEndWith(asked);
}, 120_000);

describe("GithubReads", () => {
  const COMMIT = "1111111111111111111111111111111111111111";
  const BLOB = "2222222222222222222222222222222222222222";
  const SKILL = "---\nname: alpha\ndescription: Use for the alpha list.\n---\n";
  const failed = (stderr: string): GhResult => ({ code: 1, stdout: "", stderr: `${stderr}\n` });
  /** A rule that sends a read again with no wait. */
  const noWait = () => new GhRetries({ sleep: async () => {}, err: () => {} }, "build-cases");
  /** A stand-in gh of `repo`, whose tree at COMMIT holds skills/alpha/SKILL.md, the blob BLOB:
   *  each call kept in `calls`, a GraphQL batch refused (so the blob is read alone), and the blob
   *  read answered by `blob`. */
  const standIn = (repo: string, blob: () => GhResult) => {
    const calls: string[] = [];
    const gh = async (args: readonly string[]): Promise<GhResult> => {
      const endpoint = args[1] ?? "";
      calls.push(endpoint);
      if (endpoint === `repos/${repo}/git/trees/${COMMIT}?recursive=1`) {
        const entry = { path: "skills/alpha/SKILL.md", mode: "100644", type: "blob", sha: BLOB };
        return { code: 0, stdout: JSON.stringify({ sha: COMMIT, tree: [entry] }), stderr: "" };
      }
      if (endpoint === `repos/${repo}/git/blobs/${BLOB}`) return blob();
      return failed("gh: Not Found (HTTP 404)");
    };
    return { gh, calls };
  };

  // Catches: a SKILL.md read that failed for a transient reason on every try kept, as a fault,
  // for every later case at that repository and commit.
  test("a SKILL.md read that fails for a transient reason on every try is not kept: the next read of that commit reads it again", async () => {
    let flaky = true;
    const { gh, calls } = standIn("acme/skills", () =>
      flaky
        ? failed("net/http: TLS handshake timeout")
        : {
            code: 0,
            stdout: JSON.stringify({
              content: Buffer.from(SKILL).toString("base64"),
              encoding: "base64",
            }),
            stderr: "",
          }
    );
    const reads = new GithubReads(gh, new Map(), noWait());
    const first = await reads.at("acme/skills", COMMIT, []).then(() => "read", String);
    expect(first).toContain("TLS handshake timeout");
    flaky = false;
    const again = await reads.at("acme/skills", COMMIT, []);
    expect(again.faults).toEqual([]);
    expect(again.copies.map((copy) => copy.description)).toEqual(["Use for the alpha list."]);
    // Five tries the first time, one the second.
    expect(calls.filter((call) => call.endsWith(`/git/blobs/${BLOB}`))).toHaveLength(6);
  });

  // Catches: a failure taken as transient for a word of the repository's name in its error, so a
  // read that failed for good is sent again for every later case.
  test("a read that fails for good is kept, though the repository's name holds the word timeout", async () => {
    const { gh, calls } = standIn("acme/other", () => failed("gh: Not Found (HTTP 404)"));
    const reads = new GithubReads(gh, new Map(), noWait());
    for (let read = 0; read < 2; read++) {
      const outcome = await reads.at("acme/request-timeout", COMMIT, []).then(() => "read", String);
      expect(outcome).toContain("gh: Not Found (HTTP 404)");
    }
    expect(calls).toEqual([`repos/acme/request-timeout/git/trees/${COMMIT}?recursive=1`]);
  });
});
