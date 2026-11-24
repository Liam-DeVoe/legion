import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { chmodSync, existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { fileHash } from "./build";
import { type Case, loadCasesAt, pathHash } from "./cases";
import { composedAt, DAEMON_IMAGE_ITEM, localSource, rolePromptParts } from "./context";
import { loadPrompt } from "./readers";
import {
  messageSse,
  type RecordedRequest,
  type Recorder,
  sseResponse,
  startRecorder,
} from "./recorder";
import { main, type RoleCasesIo } from "./rolecases";
import {
  BASE_COMMIT,
  HEAD_COMMIT,
  ROLE_LINE,
  ROLE_PART,
  ROLE_REPO,
  SKILLS_ONLY_PLUGIN,
  writeRolePromptRepository,
} from "./testdata/roleprompt";

// The role-prompt arm: build-role-cases (rolecases.ts) builds cases of a line of a role prompt from
// sessions of that role. Its sessions here are two made-up sessions of a Legion implementer in the
// store's form (testdata/R8-implementer.jsonl, and the same session with its task named otherwise),
// each stored as the store keeps a file and read through a stand-in store command, as
// build.test.ts's store is; the repository is testdata/roleprompt.ts's, read through --repos; the
// case reader is a stand-in gateway on 127.0.0.1 built on recorder.ts. build.ts's profile step
// (`build-cases --reprofile`, which build.test.ts tests) stands in here as `io.profile`: it gives
// each session the made-up plugin, and the daemon image's commit by the session, BASE_COMMIT for
// one and HEAD_COMMIT, whose prompt lacks the line, for the other. Composing runs `go test` in a
// copy of the repository; a row that composes skips without Go, except on GitHub Actions, where the
// pi-legion job sets Go up.

const goMissing = Bun.which("go") === null;
const onActions = process.env.GITHUB_ACTIONS === "true";
if (goMissing && !onActions) {
  console.log("rolecases.test.ts: no `go` on PATH, so its rows that compose are skipped");
}

const scratch = temporaryDirectory("skill-cuts-rolecases", { real: true });
const TESTDATA = path.join(import.meta.dir, "testdata");
/** The made-up session, and the same session with its task named otherwise. */
const SESSION = readFileSync(path.join(TESTDATA, "R8-implementer.jsonl"), "utf8");
const KEPT_TASK = "Make the widget list.";
const ABSENT_TASK = "Make the gadget list.";
/** Where the store keeps each session, under a Legion workspace's session directory. */
const KEPT =
  "/sessions/--legion-workspaces-acme-widgets-acme-1--/2026-11-20T02-00-00-000Z_kept.jsonl";
const ABSENT =
  "/sessions/--legion-workspaces-acme-widgets-acme-1--/2026-11-20T02-00-00-000Z_absent.jsonl";
/** Where the store keeps the kept session with its session line's timestamp not ISO 8601 (one
 *  `Date.parse` reads all the same). */
const NOT_ISO =
  "/sessions/--legion-workspaces-acme-widgets-acme-1--/2026-11-20T02-00-00-000Z_not-iso.jsonl";
/** The daemon image's commit of each session, as the stand-in profile step names it. */
const DAEMON_AT: Readonly<Record<string, string>> = {
  [pathHash(KEPT)]: BASE_COMMIT,
  [pathHash(ABSENT)]: HEAD_COMMIT,
};
/** The made-up skill the implementer reads first. */
const ALPHA =
  "---\nname: alpha\ndescription: Use when a task asks for the alpha checklist of the made-up fixture project.\n---\n\n# Alpha\n\n1. Name each file you change.\n";

/** The stand-in store command: it answers pull.sql from the store file `storeFile`, each file in
 *  slices of 200 characters of its file row, as build.test.ts's stand-in stores a file. */
const STANDIN_PSQL = (storeFile: string) => `
import { readFileSync } from "node:fs";
const db = process.argv[2] ?? "";
const query = await Bun.stdin.text();
const store = JSON.parse(readFileSync(${JSON.stringify(storeFile)}, "utf8"))[db];
if (store === undefined || !query.includes("omp_session_files_parts pt")) {
  process.stderr.write("psql: error: not a pull of a store of the stand-in\\n");
  process.exit(2);
}
const literal = /WHERE f\\.path = '((?:[^']|'')*)'/.exec(query)[1].replaceAll("''", "'");
const place = /\\(pc\\.o, s\\.k\\) > \\((-?\\d+), (-?\\d+)\\)/.exec(query);
const after = [Number(place[1]), Number(place[2])];
const text = store.files[literal] ?? "";
const pieces = [];
for (let at = 0, k = 0; at < text.length; at += 200, k += 1) pieces.push({ o: -1, k, c: text.slice(at, at + 200) });
const rows = pieces.filter((p) => p.o > after[0] || (p.o === after[0] && p.k > after[1])).slice(0, 64);
process.stdout.write(" value \\n-------\\n" + rows.map((row) => " " + JSON.stringify(row) + "\\n").join("") + "(" + rows.length + (rows.length === 1 ? " row" : " rows") + ")\\n\\n");
`;

/** The stand-in case reader's answer: the session's last message is the step ROLE_LINE decides. */
const PROPOSAL = {
  step: { messageIndex: 3, what: "reports the finished list" },
  line: { file: ROLE_PART, text: ROLE_LINE },
  criterion: {
    pass: "The reply reports the finished work in one line.",
    fail: "The reply reports the finished work over more than one line.",
  },
  violatingStep: {
    text: "The widget list is made.\nIt is in widgets.txt.\nEach widget has its own line.",
    toolCalls: [],
  },
  restatements: [],
};

let recorder: Recorder;
beforeAll(() => {
  recorder = startRecorder({
    route: (request: RecordedRequest) => {
      if (request.method !== "POST" || request.path !== "/v1/messages") return undefined;
      const body = JSON.parse(request.body) as { model: string };
      return sseResponse(
        messageSse({
          id: `msg_standin_${request.n}`,
          model: body.model,
          content: [{ type: "text", text: JSON.stringify(PROPOSAL) }],
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

/** The inputs of a build under `dir`: the configuration, the key file, the skill's directory,
 *  the sessions file and the store, the copies and count directories, and the repositories. */
function inputs(dir: string) {
  mkdirSync(dir, { recursive: true });
  const storeFile = path.join(dir, "store.json");
  writeFileSync(
    storeFile,
    JSON.stringify({
      store_acme: {
        files: {
          [KEPT]: SESSION,
          [ABSENT]: SESSION.replace(KEPT_TASK, ABSENT_TASK),
          [NOT_ISO]: SESSION.replace(
            '"timestamp":"2026-11-20T02:00:00.000Z"',
            '"timestamp":"2026-11-20 02:00:00"'
          ),
        },
      },
    })
  );
  const psql = path.join(dir, "psql.ts");
  writeFileSync(psql, `#!${process.execPath}\n${STANDIN_PSQL(storeFile)}`);
  chmodSync(psql, 0o755);
  const config = path.join(dir, "skill-cuts.json");
  const fixture = JSON.parse(readFileSync(path.join(TESTDATA, "F7-skill-cuts.json"), "utf8"));
  writeFileSync(
    config,
    JSON.stringify({
      ...fixture,
      stores: [{ name: "store_acme", context: "legion:ACME" }],
      psqlCommand: psql,
      gatewayBaseUrl: recorder.url,
      cuts: [{ id: "acme-skills-1", repo: "acme/skills", pr: 1 }],
    })
  );
  const keyFile = path.join(dir, "key");
  writeFileSync(keyFile, fixtureSecret("gw"), { mode: 0o600 });
  const old = path.join(dir, "alpha");
  mkdirSync(old, { recursive: true });
  writeFileSync(path.join(old, "SKILL.md"), ALPHA);
  const sessions = path.join(dir, "sessions.jsonl");
  writeFileSync(
    sessions,
    [KEPT, ABSENT]
      .map(
        (each) => `${JSON.stringify({ store: "store_acme", hash: fileHash(each), path: each })}\n`
      )
      .join("")
  );
  for (const each of ["copies", "count"]) mkdirSync(path.join(dir, each), { recursive: true });
  const repos = path.join(dir, "repos");
  writeRolePromptRepository(repos);
  return { config, keyFile, old, sessions, repos };
}

/** The stand-in of build.ts's profile step: alpha and the made-up plugin, and the daemon image's
 *  commit DAEMON_AT names for the draft's session. */
const profile: NonNullable<RoleCasesIo["profile"]> = async (draft) => ({
  profile: {
    skills: [{ repo: "acme/skills", commit: "a".repeat(40), path: "skills/alpha", name: "alpha" }],
    plugins: [SKILLS_ONLY_PLUGIN],
    contextFiles: [],
  },
  evidence: [
    {
      item: `${DAEMON_IMAGE_ITEM}${ROLE_REPO}`,
      source: `a made-up deployment names the daemon image, ${composedAt(ROLE_REPO, DAEMON_AT[draft.source.pathHash] ?? "")}: the Legion daemon of a pod composes its role prompt as it was at the commit its daemon image was built from`,
    },
  ],
});

describe.skipIf(goMissing && !onActions)("build-role-cases", () => {
  test("keeps a session whose role prompt composed at its daemon image's commit holds the line, and drops one whose prompt lacks it as line-absent, before any reader call; its case loads at the old arm's commit", async () => {
    const dir = path.join(scratch, "build");
    const given = inputs(dir);
    const out = path.join(dir, "out", "cases.jsonl");
    const lines: string[] = [];
    const io: RoleCasesIo = {
      out: (text) => lines.push(text),
      err: (line) => lines.push(line),
      gh: async () => ({ code: 1, stdout: "", stderr: "gh: no GitHub read is made here\n" }),
      now: () => performance.now(),
      profile,
    };
    const from = recorder.requests.length;
    const code = await main(
      [
        ...["--config", given.config, "--key-file", given.keyFile, "--cut", "acme-skills-1"],
        ...["--skill", "alpha", "--old", given.old, "--role", "implementer", "--line", ROLE_LINE],
        ...["--old-role-prompt-at", BASE_COMMIT, "--sessions", given.sessions, "--seed", "7"],
        ...["--copies", path.join(dir, "copies"), "--count", path.join(dir, "count")],
        ...["--out", out, "--repos", given.repos, "--cache-dir", path.join(dir, "cache")],
      ],
      io
    );
    expect({ code, said: code === 0 ? "" : lines.join("\n") }).toEqual({ code: 0, said: "" });

    // The case reader read the kept session alone.
    const read = recorder.requests.slice(from).map((request) => request.body);
    expect(read.length).toBeGreaterThan(0);
    expect(read.every((body) => body.includes(KEPT_TASK) && !body.includes(ABSENT_TASK))).toBe(
      true
    );
    expect(read.every((body) => body.includes(loadPrompt("case").text.slice(0, 60)))).toBe(true);

    const summary = JSON.parse(readFileSync(`${out}.summary.json`, "utf8"));
    expect(summary.drops).toEqual({ "line-absent": 1 });
    const absent = summary.sessions.find(
      (each: { pathHash?: string }) => each.pathHash === pathHash(ABSENT)
    );
    expect(absent).toMatchObject({ outcome: "dropped", reason: "line-absent" });
    expect(absent.detail).toContain(HEAD_COMMIT);
    expect(absent.readerUsd).toBe(0);

    const cases: Case[] = await loadCasesAt(
      out,
      given.old,
      rolePromptParts(BASE_COMMIT, localSource(given.repos))
    );
    expect(cases).toHaveLength(1);
    const [c] = cases;
    expect(c).toMatchObject({
      skill: "alpha",
      baseFile: ROLE_PART,
      line: { text: ROLE_LINE, lineNumber: 2, lineCount: 3 },
      rolePromptLine: { role: "implementer", part: ROLE_PART },
      context: "legion:ACME:implementer",
      skillReads: [2],
      source: { store: "store_acme", pathHash: pathHash(KEPT) },
      criterion: PROPOSAL.criterion,
      model: "anthropic/claude-sonnet-5-5",
      thinking: "off",
      sessionDigest: "0a1b2c3d4e5f6071",
    });
    expect(c?.messages).toHaveLength(3);
    expect(c?.realStep.content?.[0]?.text).toBe("The widget list is made, in widgets.txt.");
    expect(c?.evidence?.map((each) => each.item)).toContain("role implementer");
    expect(existsSync(`${out}.spool`)).toBe(false);
  }, 300_000);
});

// A session line whose timestamp is not ISO 8601 gives no session start, as build-cases reads one
// (build.ts's `sessionHeader`), though `Date.parse` reads it: the session is dropped as no-header
// before its profile, any compose or any reader call, so this row needs no Go.
test("drops a session whose session line's timestamp is not ISO 8601 as no-header, before its profile or any reader call", async () => {
  const dir = path.join(scratch, "not-iso");
  const given = inputs(dir);
  writeFileSync(
    given.sessions,
    `${JSON.stringify({ store: "store_acme", hash: fileHash(NOT_ISO), path: NOT_ISO })}\n`
  );
  const out = path.join(dir, "out", "cases.jsonl");
  const lines: string[] = [];
  let profiled = 0;
  const io: RoleCasesIo = {
    out: (text) => lines.push(text),
    err: (line) => lines.push(line),
    gh: async () => ({ code: 1, stdout: "", stderr: "gh: no GitHub read is made here\n" }),
    now: () => performance.now(),
    profile: (draft, profileInputs) => {
      profiled += 1;
      return profile(draft, profileInputs);
    },
  };
  const from = recorder.requests.length;
  const code = await main(
    [
      ...["--config", given.config, "--key-file", given.keyFile, "--cut", "acme-skills-1"],
      ...["--skill", "alpha", "--old", given.old, "--role", "implementer", "--line", ROLE_LINE],
      ...["--old-role-prompt-at", BASE_COMMIT, "--sessions", given.sessions, "--seed", "7"],
      ...["--copies", path.join(dir, "copies"), "--count", path.join(dir, "count")],
      ...["--out", out, "--repos", given.repos, "--cache-dir", path.join(dir, "cache")],
    ],
    io
  );
  expect({ code, said: code === 0 ? "" : lines.join("\n") }).toEqual({ code: 0, said: "" });
  expect({ profiled, readerCalls: recorder.requests.length - from }).toEqual({
    profiled: 0,
    readerCalls: 0,
  });
  const summary = JSON.parse(readFileSync(`${out}.summary.json`, "utf8"));
  expect(summary).toMatchObject({ cases: 0, drops: { "no-header": 1 } });
  expect(summary.sessions).toEqual([
    expect.objectContaining({
      pathHash: pathHash(NOT_ISO),
      outcome: "dropped",
      reason: "no-header",
      detail: "no session line with an ISO timestamp",
    }),
  ]);
}, 60_000);
