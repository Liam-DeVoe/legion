import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { SessionEntry, type SessionMessage, stripSkillHeader } from "../skill-scenarios/transcript";
import { type ArmName, buildArm } from "./arms";
import { capturePaths } from "./capture";
import { type Case, pathHash } from "./cases";
import {
  armCaptureDir,
  CaseResult,
  type CliIo,
  caseResultFile,
  falseLossChance,
  lossChance,
  main,
  ompBinary,
} from "./cli";
import { loadConfig } from "./config";
import { countSql } from "./countsql";
import { NOTHING_REPLY } from "./judge";
import { ATTACK_PLANTED_LINE, loadPrompt, READER_TRIES } from "./readers";
import {
  messageSse,
  type RecordedRequest,
  type Recorder,
  sseResponse,
  startRecorder,
} from "./recorder";
import { SCREEN_RUNS } from "./score";

// The runner's command line, end to end: each command runs as its own `bun cli.ts` process, with
// every GH_* and GITHUB_* variable taken out of its environment, against a stand-in gateway on
// 127.0.0.1 built on recorder.ts, which answers in the stream shape of the F5 replies
// (testdata/CAPTURE.md). The behaviour commands read their cases from a temporary directory, made
// from F1 (an Oh My Pi 18.18.6 session of a scratch profile with the made-up skills alpha and
// beta: 0 the user's prompt, 1 a read of skill://alpha, 2 its result, 3 a read of
// skill://alpha/references/x.md, 4 its result, 5 a read of the missing skill://gamma, 6 its
// result, 7 a `task` call), and replay bodies recorded from F2 (the request body Oh My Pi sends for
// a replayed history, `omp -p --mode json --no-session --hook <replay hook>` against the recorder),
// each with its arm's read result put in, saved where the capture step reads a capture back: no
// Oh My Pi runs here. No test here reaches a network. Every skill, repository and store is made up.

const CLI = path.join(import.meta.dir, "cli.ts");
const scratch = temporaryDirectory("skill-cuts-cli", { real: true });

/** The environment of each command: this one's, less every GH_* and GITHUB_* variable. */
const ENV = Object.fromEntries(
  Object.entries(process.env).filter(([name]) => !/^(GH|GITHUB)_/.test(name))
);

/** What one command did. */
interface Ran {
  readonly code: number;
  readonly stdout: string;
  readonly stderr: string;
}

/** Runs `bun cli.ts <args>` in `env`. */
async function cli(
  args: readonly string[],
  env: Readonly<Record<string, string | undefined>> = ENV
): Promise<Ran> {
  const child = Bun.spawn([process.execPath, CLI, ...args], {
    env,
    stdin: "ignore",
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ]);
  return { code, stdout, stderr };
}

/** The token counts of each stand-in reply. */
let usage = { input: 10, output: 3 };

/** The stand-in gateway: answers each POST to /v1/messages with what `answer` gives for its body:
 *  a response as it is, or a text as a streamed reply of the body's own model with `usage`. */
let answer: (body: { model: string; [field: string]: unknown }) => string | Response = () => "{}";
let recorder: Recorder;
beforeAll(() => {
  recorder = startRecorder({
    route: (request: RecordedRequest) => {
      if (request.method !== "POST" || request.path !== "/v1/messages") return undefined;
      const body = JSON.parse(request.body) as { model: string };
      const answered = answer(body);
      if (answered instanceof Response) return answered;
      return sseResponse(
        messageSse({
          id: `msg_standin_${request.n}`,
          model: body.model,
          content: [{ type: "text", text: answered }],
          stopReason: "end_turn",
          inputTokens: usage.input,
          outputTokens: usage.output,
        }),
        `req_standin_${request.n}`
      );
    },
  });
});
afterAll(async () => {
  await recorder.close();
});

/** The requests the stand-in took since `from`. */
function requestsSince(from: number): readonly RecordedRequest[] {
  return recorder.requests.slice(from);
}

/** A configuration whose gateway is the stand-in, and a key file. */
function setup(): { config: string; keyFile: string; key: string } {
  const config = path.join(scratch, "skill-cuts.json");
  const fixture = JSON.parse(
    readFileSync(path.join(import.meta.dir, "testdata", "F7-skill-cuts.json"), "utf8")
  );
  writeFileSync(config, JSON.stringify({ ...fixture, gatewayBaseUrl: recorder.url }));
  const key = fixtureSecret("gw");
  const keyFile = path.join(scratch, "key");
  writeFileSync(keyFile, key, { mode: 0o600 });
  return { config, keyFile, key };
}

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
  "",
].join("\n");

describe("read", () => {
  test("runs the reader through the gateway, writes its answer, and logs each request's settings and usage, never the key or a message's text", async () => {
    const { config, keyFile, key } = setup();
    const input = path.join(scratch, "attack-in.json");
    writeFileSync(
      input,
      JSON.stringify({ skill: "alpha", files: [{ path: "SKILL.md", text: SKILL_MD }] })
    );
    const finding = {
      kind: "vague",
      file: "SKILL.md",
      line: 9,
      quote: "Write each step's result on one line.",
      why: "It does not say what a result is.",
    };
    answer = () => JSON.stringify({ findings: [finding] });
    const out = path.join(scratch, "attack-out.json");
    const from = recorder.requests.length;
    const ran = await cli([
      "read",
      "--config",
      config,
      "--key-file",
      keyFile,
      "--prompt",
      "attack",
      "--in",
      input,
      "--out",
      out,
    ]);
    expect(ran.stderr).toBe("");
    expect(ran.code).toBe(0);
    const written = JSON.parse(readFileSync(out, "utf8"));
    expect(written.prompt).toBe("attack");
    expect(written.answer).toEqual({ findings: [finding] });
    expect(written.refusals).toEqual([]);
    expect(written.settings).toEqual(loadPrompt("attack").settings);
    expect(written.spent).toHaveLength(1);

    const requests = requestsSince(from);
    expect(requests).toHaveLength(1);
    expect(JSON.parse(requests[0]?.body ?? "{}").messages[0].content[0].text).toContain(
      "Write each step's result on one line."
    );

    const log = readFileSync(`${out}.requests.jsonl`, "utf8");
    const lines = log
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line));
    const settings = loadPrompt("attack").settings;
    expect(lines).toHaveLength(1);
    expect(lines[0]).toMatchObject({
      kind: "reader:attack",
      model: settings.model,
      thinking: "enabled",
      thinking_budget: settings.thinking_budget,
      max_tokens: settings.max_tokens,
      temperature: settings.temperature,
      effort: settings.effort,
      stream: true,
      answeredBy: settings.model,
      requestId: `req_standin_${requests[0]?.n}`,
      stopReason: "end_turn",
      usage: { input_tokens: 10, output_tokens: 3 },
    });
    expect(log).not.toContain(key);
    expect(log).not.toContain("alpha checklist");
    expect(log).not.toContain("one line");
  }, 30_000);

  test("refuses an unknown prompt and an input its reader's schema refuses, before any call", async () => {
    const { config, keyFile } = setup();
    const input = path.join(scratch, "bad-in.json");
    writeFileSync(input, JSON.stringify({ skill: "alpha", files: [] }));
    const out = path.join(scratch, "bad-out.json");
    const from = recorder.requests.length;
    const unknown = await cli([
      "read",
      "--config",
      config,
      "--key-file",
      keyFile,
      "--prompt",
      "poem",
      "--in",
      input,
      "--out",
      out,
    ]);
    expect(unknown.code).toBe(2);
    expect(unknown.stderr).toContain("--prompt poem is no reader");
    const refused = await cli([
      "read",
      "--config",
      config,
      "--key-file",
      keyFile,
      "--prompt",
      "attack",
      "--in",
      input,
      "--out",
      out,
    ]);
    expect(refused.code).toBe(2);
    expect(refused.stderr).toContain("the attack reader's input");
    const missing = await cli([
      "read",
      "--config",
      config,
      "--prompt",
      "attack",
      "--in",
      input,
      "--out",
      out,
    ]);
    expect(missing.code).toBe(2);
    expect(missing.stderr).toContain("--key-file required");
    expect(requestsSince(from)).toHaveLength(0);
  }, 30_000);

  test("writes a null answer with each refusal when every reply is refused, and exits 1", async () => {
    const { config, keyFile } = setup();
    const input = path.join(scratch, "attack-in.json");
    writeFileSync(
      input,
      JSON.stringify({ skill: "alpha", files: [{ path: "SKILL.md", text: SKILL_MD }] })
    );
    answer = () => "not JSON at all";
    const out = path.join(scratch, "refused-out.json");
    const from = recorder.requests.length;
    const ran = await cli([
      "read",
      "--config",
      config,
      "--key-file",
      keyFile,
      "--prompt",
      "attack",
      "--in",
      input,
      "--out",
      out,
    ]);
    expect(ran.code).toBe(1);
    const written = JSON.parse(readFileSync(out, "utf8"));
    expect(written.answer).toBeNull();
    expect(written.refusals).toHaveLength(READER_TRIES);
    expect(requestsSince(from)).toHaveLength(READER_TRIES);
  }, 30_000);
});

describe("control", () => {
  test("attack plants its line, runs the reader once, and holds when the answer lists that line", async () => {
    const { config, keyFile } = setup();
    const input = path.join(scratch, "attack-in.json");
    writeFileSync(
      input,
      JSON.stringify({ skill: "alpha", files: [{ path: "SKILL.md", text: SKILL_MD }] })
    );
    // The stand-in finds the planted line where the reader's numbered text shows it.
    answer = (body) => {
      const line = new RegExp(`(\\d+): ${ATTACK_PLANTED_LINE.replace(".", "\\.")}`).exec(
        JSON.stringify(body)
      )?.[1];
      return JSON.stringify({
        findings:
          line === undefined
            ? []
            : [
                {
                  kind: "vague",
                  file: "SKILL.md",
                  line: Number(line),
                  quote: ATTACK_PLANTED_LINE,
                  why: "Where is that?",
                },
              ],
      });
    };
    const out = path.join(scratch, "control-attack.json");
    const held = await cli([
      "control",
      "attack",
      "--config",
      config,
      "--key-file",
      keyFile,
      "--in",
      input,
      "--out",
      out,
    ]);
    expect(held.stderr).toBe("");
    expect(held.code).toBe(0);
    expect(JSON.parse(readFileSync(out, "utf8"))).toMatchObject({ control: "attack", held: true });

    answer = () => JSON.stringify({ findings: [] });
    const missed = await cli([
      "control",
      "attack",
      "--config",
      config,
      "--key-file",
      keyFile,
      "--in",
      input,
      "--out",
      out,
    ]);
    expect(missed.code).toBe(1);
    expect(JSON.parse(readFileSync(out, "utf8"))).toMatchObject({ control: "attack", held: false });
  }, 30_000);

  test("refuses a control it cannot plant, before any call", async () => {
    const { config, keyFile } = setup();
    const input = path.join(scratch, "map-in.json");
    writeFileSync(
      input,
      JSON.stringify({
        skill: "alpha",
        base: [{ path: "SKILL.md", text: SKILL_MD }],
        cut: [{ path: "SKILL.md", text: SKILL_MD }],
        audit: null,
      })
    );
    const out = path.join(scratch, "control-map.json");
    const from = recorder.requests.length;
    const noLine = await cli([
      "control",
      "map",
      "--config",
      config,
      "--key-file",
      keyFile,
      "--in",
      input,
      "--out",
      out,
    ]);
    expect(noLine.code).toBe(2);
    expect(noLine.stderr).toContain("--delete-line required");
    const absent = await cli([
      "control",
      "map",
      "--config",
      config,
      "--key-file",
      keyFile,
      "--in",
      input,
      "--out",
      out,
      "--delete-line",
      "No such line.",
    ]);
    expect(absent.code).toBe(2);
    expect(absent.stderr).toContain("no line of the cut text");
    const kind = await cli(["control", "poem", "--config", config, "--key-file", keyFile]);
    expect(kind.code).toBe(2);
    expect(requestsSince(from)).toHaveLength(0);
  }, 30_000);
});

describe("count-sql", () => {
  test("prints count.sql filled for the store and day, as countsql.ts does", async () => {
    const config = path.join(import.meta.dir, "testdata", "F7-skill-cuts.json");
    const ran = await cli([
      "count-sql",
      "--config",
      config,
      "--store",
      "store_a",
      "--day",
      "2026-11-20",
    ]);
    expect(ran.stderr).toBe("");
    expect(ran.code).toBe(0);
    expect(ran.stdout).toBe(countSql(loadConfig(config), "store_a", "2026-11-20"));
    const refused = await cli([
      "count-sql",
      "--config",
      config,
      "--store",
      "store_c",
      "--day",
      "2026-11-20",
    ]);
    expect(refused.code).toBe(2);
  }, 30_000);
});

// The behaviour test.

const TESTDATA = path.join(import.meta.dir, "testdata");
const F1 = path.join(
  TESTDATA,
  "F1",
  "2026-11-20T01-59-41-780Z_01a1f6ae-51d4-715f-a2a6-59f275bdff80.jsonl"
);
const F2 = path.join(TESTDATA, "F2-request.json");

/** F1's messages, in order. */
const messages: SessionMessage[] = readFileSync(F1, "utf8")
  .split("\n")
  .filter((line) => line.trim() !== "")
  .map((line) => SessionEntry.parse(JSON.parse(line)))
  .flatMap((entry) => (entry.type === "message" && entry.message ? [entry.message] : []));

/** The file F1's read result at `index` read, as it was on disk. */
function f1File(index: number): string {
  return `${stripSkillHeader(messages[index]?.content?.[0]?.text ?? "")}\n`;
}

/** The line of alpha's SKILL.md (line 9 of 10) each case exercises, and another line of it. */
const LINE = "2. Write each step's result on one line.";
const OTHER_LINE = "3. Stop after the third step.";
/** The steps the stand-in replays with: the one the line asks for, and one it does not. */
const PASS_STEP = "I write each step's result on one line.";
const FAIL_STEP = "I put the results wherever they fit.";
/** The anthropic-* headers Oh My Pi sent with F2's body. */
const F2_HEADERS: Record<string, string> = Object.fromEntries(
  Object.entries(
    JSON.parse(readFileSync(path.join(TESTDATA, "F2-request.headers.json"), "utf8"))
      .headers as Record<string, string>
  ).filter(([name]) => name.startsWith("anthropic-"))
);

/** How many replays of a case marked FLAKY the stand-in still refuses. */
let flakyLeft = 0;

/** Whether `body` is a judge's request: its system prompt is prompts/judge.md's. */
function isJudgement(body: { [field: string]: unknown }): boolean {
  const system = body.system as { text?: string }[] | undefined;
  return system?.[0]?.text === loadPrompt("judge").text;
}

/** The stand-in of the behaviour test. A judge passes a step that holds PASS_STEP and fails any
 *  other. A replay passes or fails by the arm text it receives: it answers PASS_STEP when its
 *  messages hold the case's line, and FAIL_STEP when they do not; it refuses (400) each body of a
 *  case marked BROKEN, and the first `flakyLeft` bodies of a case marked FLAKY. */
function behaviourStandIn(body: { [field: string]: unknown }): string | Response {
  if (isJudgement(body)) {
    const user = body.messages as { content: { text: string }[] }[];
    const step = user[0]?.content[1]?.text ?? "";
    return JSON.stringify({
      verdict: step.includes(PASS_STEP) ? "pass" : "fail",
      reason: "The stand-in judge read the step.",
    });
  }
  const text = JSON.stringify(body.messages);
  let refuse = text.includes("BROKEN");
  if (text.includes("FLAKY") && flakyLeft > 0) {
    flakyLeft -= 1;
    refuse = true;
  }
  if (refuse) {
    return Response.json(
      {
        type: "error",
        error: { type: "invalid_request_error", message: "the stand-in refuses this body" },
      },
      { status: 400 }
    );
  }
  return text.includes(LINE) ? PASS_STEP : FAIL_STEP;
}

/** A case of F1's step after alpha's reads, which LINE governs, whose real step is `realStep` and
 *  whose violating step is `violatingStep`. */
function makeCase(id: string, realStep = PASS_STEP, violatingStep = FAIL_STEP): Case {
  return {
    id,
    cut: "acme-skills-1",
    skill: "alpha",
    baseFile: "SKILL.md",
    line: { text: LINE, lineNumber: 9, lineCount: 10 },
    criterion: {
      pass: "The step writes each step's result on one line.",
      fail: "The step spreads a step's result over more than one line, or writes none.",
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
    realStep: { ...messages[7], content: [{ type: "text", text: realStep }] },
    violatingStep: { role: "assistant", content: [{ type: "text", text: violatingStep }] },
    source: { store: "store_a", pathHash: pathHash(`/sessions/${id}.jsonl`) },
    sessionStart: "2026-11-20T01:59:41.780Z",
    secretsRemoved: 0,
    sessionDigest: "46874d907e557998",
  };
}

/** One skill's texts, cases and output directory, under the scratch directory. */
interface Scenario {
  readonly dir: string;
  readonly old: string;
  readonly new: string;
  readonly casesFile: string;
  readonly out: string;
  readonly cases: readonly Case[];
  /** The word each case's recorded bodies carry: FLAKY, BROKEN or none. */
  readonly markers: ReadonlyMap<string, string>;
}

/** Writes a skill directory holding `files` (path inside it to text). */
function writeSkill(dir: string, files: Record<string, string>): void {
  for (const [file, text] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(dir, file)), { recursive: true });
    writeFileSync(path.join(dir, file), text);
  }
}

/** A scenario: alpha's old text as F1 read it, the cut's SKILL.md `newSkill`, and the cases. */
function scenario(
  name: string,
  newSkill: string,
  cases: readonly { id: string; realStep?: string; violatingStep?: string; marker?: string }[]
): Scenario {
  const dir = path.join(scratch, name);
  const old = path.join(dir, "old");
  const fresh = path.join(dir, "new");
  writeSkill(old, { "SKILL.md": f1File(2), "references/x.md": f1File(4) });
  writeSkill(fresh, { "SKILL.md": newSkill, "references/x.md": f1File(4) });
  const made = cases.map(({ id, realStep, violatingStep }) =>
    makeCase(id, realStep, violatingStep)
  );
  const casesFile = path.join(dir, "cases.jsonl");
  writeFileSync(casesFile, `${made.map((c) => JSON.stringify(c)).join("\n")}\n`);
  return {
    dir,
    old,
    new: fresh,
    casesFile,
    out: path.join(dir, "out"),
    cases: made,
    markers: new Map(cases.map(({ id, marker }) => [id, marker ?? ""])),
  };
}

/** Saves a body recorded from F2 for each case of `s` and each arm of `arms` (with `plantedLine`
 *  for minus), where the capture step under `out` reads it back: F2 with the case's id and marker
 *  in its user text, and the arm's read result of SKILL.md in place of F2's, or, for none, no read. */
function recordCaptures(
  s: Scenario,
  arms: readonly ArmName[],
  plantedLine?: string,
  out = s.out
): void {
  const f2 = readFileSync(F2, "utf8");
  const dirs = { old: s.old, new: s.new, ...(plantedLine === undefined ? {} : { plantedLine }) };
  for (const c of s.cases) {
    for (const name of arms) {
      const arm = buildArm(c, name, dirs, path.join(s.dir, "arms", c.id));
      const body = JSON.parse(f2);
      body.messages[0].content[1].text = `Case ${c.id} ${s.markers.get(c.id)}: write the checklist.`;
      if (name === "none") body.messages = [body.messages[0]];
      else body.messages[2].content[0].content[0].text = arm.messages[2]?.content?.[0]?.text;
      const files = capturePaths(armCaptureDir(out, arm), c.id, name);
      mkdirSync(path.dirname(files.body), { recursive: true });
      writeFileSync(files.body, JSON.stringify(body));
      writeFileSync(
        files.record,
        JSON.stringify({
          case: c.id,
          arm: name,
          requestHeaders: F2_HEADERS,
          captureDigest: "0123456789abcdef",
          sessionDigest: c.sessionDigest,
        })
      );
    }
  }
}

/** The arguments of `behaviour <command>` for `s`, and `extra`. */
function behaviour(command: string, s: Scenario, ...extra: string[]): string[] {
  const { config, keyFile } = setup();
  return [
    "behaviour",
    command,
    "--config",
    config,
    "--key-file",
    keyFile,
    "--out",
    s.out,
    "--old",
    s.old,
    "--new",
    s.new,
    "--cases",
    s.casesFile,
    "--scratch",
    path.join(s.dir, "scratch"),
    ...extra,
  ];
}

/** The arguments of `behaviour report` for `s`: no key file and no new text. */
function report(s: Scenario): string[] {
  const { config } = setup();
  return [
    "behaviour",
    "report",
    "--config",
    config,
    "--out",
    s.out,
    "--old",
    s.old,
    "--cases",
    s.casesFile,
  ];
}

/** Case `id`'s results under `out`. */
function result(out: string, id: string): CaseResult {
  return CaseResult.parse(JSON.parse(readFileSync(caseResultFile(out, id), "utf8")));
}

/** The passes of each arm in a stage of `r`. */
function passes(r: CaseResult, stage: "screen" | "confirm" | "control"): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const run of r.stages[stage]?.runs ?? []) {
    counts[run.arm] = (counts[run.arm] ?? 0) + (run.verdict === "pass" ? 1 : 0);
  }
  return counts;
}

/** The cut's SKILL.md with the case's line rewritten. */
const LOSS_SKILL = () => f1File(2).replace(LINE, "2. Put each step's result where it fits.");

describe("behaviour", () => {
  test("screen, confirm, control and report give each case's passes and the skill's verdict", async () => {
    const s = scenario("loss", LOSS_SKILL(), [
      { id: "c00", violatingStep: PASS_STEP },
      { id: "c01", realStep: FAIL_STEP },
      { id: "c02", marker: "FLAKY" },
      { id: "c03", marker: "BROKEN" },
      { id: "c04" },
      { id: "c05" },
      { id: "c06" },
      { id: "c07" },
      { id: "c08" },
      { id: "c09" },
    ]);
    recordCaptures(s, ["old", "new", "none", "minus"]);
    answer = behaviourStandIn;
    usage = { input: 10, output: 3 };
    flakyLeft = 3;
    const { key } = setup();
    const afterLog = path.join(s.dir, "after-case.log");

    let from = recorder.requests.length;
    const screen = await cli(behaviour("screen", s, "--after-case", `echo >> ${afterLog}`));
    expect(screen.stderr).toBe("");
    expect(screen.code).toBe(0);
    expect(screen.stdout).toContain(
      "screening stops before case c09: 6 informative and 6 screened cases"
    );
    // Three judge controls a case; then 30 replays and 30 judgements, c02's three refused replays
    // made again, and every one of c03's 15 replays of each arm refused.
    const screenRequests = requestsSince(from);
    expect(screenRequests).toHaveLength(3 + 3 + (3 + 33 + 30) + (3 + 45) + 5 * 63);
    // Each replay sends the anthropic-* headers its capture recorded.
    const replays = screenRequests.filter((request) => !isJudgement(JSON.parse(request.body)));
    expect(replays).toHaveLength(33 + 45 + 5 * 30);
    for (const request of replays) {
      expect(request.headers["anthropic-beta"]).toBe(F2_HEADERS["anthropic-beta"] ?? "");
    }

    // c00's violating step is judged pass, and c01's real step fail: each is dropped, its drop
    // naming the control it failed.
    const c00 = result(s.out, "c00");
    expect(c00.dropped).toBe(
      "the judge controls do not hold: the violating step is judged pass (fail wanted)"
    );
    expect(c00.controls).toMatchObject({
      realStep: { verdict: "pass" },
      violating: { verdict: "pass" },
      nothing: { verdict: "fail" },
      failed: ["violating"],
      hold: false,
    });
    const c01 = result(s.out, "c01");
    expect(c01.dropped).toBe(
      "the judge controls do not hold: the real step is judged fail (pass wanted)"
    );
    expect(c01.controls).toMatchObject({
      realStep: { verdict: "fail" },
      violating: { verdict: "fail" },
      nothing: { verdict: "fail" },
      failed: ["realStep"],
      hold: false,
    });
    expect(c00.stages.screen).toBeUndefined();
    expect(c01.stages.screen).toBeUndefined();
    const c02 = result(s.out, "c02");
    expect(c02.stages.screen?.state).toBe("counted");
    expect(passes(c02, "screen")).toEqual({ old: SCREEN_RUNS, new: 0, none: 0 });
    expect(c02.stages.screen?.runs.filter((run) => run.verdict === null)).toHaveLength(3);
    expect(c02.stages.screen?.seeds).toHaveLength(2);
    for (const run of c02.stages.screen?.runs ?? []) {
      if (run.verdict !== null) expect(run.id).toMatch(/^[0-9a-f]{12}$/);
    }
    expect(result(s.out, "c03").stages.screen?.state).toBe("unscreened");
    expect(existsSync(caseResultFile(s.out, "c09"))).toBe(false);
    expect(readFileSync(afterLog, "utf8").trim().split("\n")).toEqual(
      ["c00", "c01", "c02", "c03", "c04", "c05", "c06", "c07", "c08"].map((id) =>
        caseResultFile(s.out, id)
      )
    );

    from = recorder.requests.length;
    const confirm = await cli(behaviour("confirm", s));
    expect(confirm.stderr).toBe("");
    expect(confirm.code).toBe(0);
    expect(confirm.stdout).toContain("6 flagged cases: c02, c04, c05, c06, c07, c08");
    expect(requestsSince(from)).toHaveLength(6 * 160);
    expect(passes(result(s.out, "c04"), "confirm")).toEqual({ old: 40, new: 0 });

    from = recorder.requests.length;
    const control = await cli(behaviour("control", s));
    expect(control.stderr).toBe("");
    expect(control.code).toBe(0);
    expect(control.stdout).toContain("the control case is c02; minus leaves out the case's line");
    expect(requestsSince(from)).toHaveLength(160);
    expect(passes(result(s.out, "c02"), "control")).toEqual({ old: 40, minus: 0 });

    const reported = await cli(report(s));
    expect(reported.stderr).toBe("");
    expect(reported.code).toBe(0);
    const lines = reported.stdout.split("\n");
    expect(lines).toContain(
      "case c00: dropped: the judge controls do not hold: the violating step is judged pass (fail wanted)"
    );
    expect(lines).toContain(
      "case c01: dropped: the judge controls do not hold: the real step is judged fail (pass wanted)"
    );
    expect(lines).toContain(
      "case c02: screen: old 10/10, new 0/10, none 0/10: informative, flagged (3 runs with no verdict, run again); confirm: old 40/40, new 0/40: a loss; control: old 40/40, minus 0/40: a control loss"
    );
    expect(lines).toContain(
      "case c03: screen: unscreened: an arm lacks its 10 judged runs after 5 extra runs (45 runs with no verdict, run again)"
    );
    expect(lines).toContain(
      "case c04: screen: old 10/10, new 0/10, none 0/10: informative, flagged; confirm: old 40/40, new 0/40: a loss"
    );
    expect(lines).toContain("case c09: not run");
    expect(lines).toContain(
      "cases: 6 screened, 6 informative, 6 flagged, 6 confirmed losses; 2 dropped (2 by the judge controls, 0 for their model), 1 unscreened, 1 not run"
    );
    expect(lines).toContain(
      `judge controls failed, by control (a case can fail more than one): the real step not judged pass 1, the violating step not judged fail 1, the reply "${NOTHING_REPLY}" not judged fail 0`
    );
    expect(lines).toContain("control case: c02");
    expect(lines).toContain(
      "verdict: loss: in c02, c04, c05, c06, c07, c08 the cut's text passes at least 10 fewer of 40 fresh runs than the old text, and the planted-loss control (case c02) is a loss"
    );
    // Each informative case passed old 10, new 0 and none 0 of its screen runs.
    const each = falseLossChance({ old: 10, new: 0, none: 0 });
    const percent = (x: number) => `${(100 * x).toFixed(2)}%`;
    expect(each).toBeGreaterThan(0);
    expect(reported.stdout).toContain(
      `false-loss rate at the cases run (an estimate): ${percent(1 - (1 - each) ** 6)}, `
    );
    expect(reported.stdout).toContain(
      `(c02 ${percent(each)}, c04 ${percent(each)}, c05 ${percent(each)}, c06 ${percent(each)}, c07 ${percent(each)}, c08 ${percent(each)})`
    );
    expect(lines).toContain(
      `  old 0.9: ${[0.8, 0.7, 0.6, 0.5].map((cut) => `new ${cut} ${percent(lossChance({ old: 0.9, cut, none: 0.1 }))}`).join(", ")}`
    );
    expect(lines).toContain(
      "digests: 22 of 22 captures' system-prompt digests differ from their session's"
    );

    // The request log: one line per request with its settings and usage, never the key and never
    // a message's text.
    const log = readFileSync(path.join(s.out, "requests.jsonl"), "utf8");
    expect(log).not.toContain(key);
    expect(log).not.toContain(LINE);
    expect(log).not.toContain(PASS_STEP);
    const logged = log
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line));
    expect(logged).toHaveLength(435 + 6 * 160 + 160);
    expect(logged.filter((line) => line.status === 400)).toHaveLength(48);
    const judge = loadPrompt("judge").settings;
    expect(logged.find((line) => line.kind === "judge")).toMatchObject({
      model: judge.model,
      thinking: "enabled",
      thinking_budget: judge.thinking_budget,
      max_tokens: judge.max_tokens,
      temperature: judge.temperature,
      effort: judge.effort,
      usage: { input_tokens: 10, output_tokens: 3 },
    });
    expect(logged.find((line) => line.kind === "replay" && line.usage)).toMatchObject({
      model: "claude-sonnet-5-5",
      thinking: "between_tools",
      max_tokens: 128000,
    });
  }, 300_000);

  test("a run started again skips each complete case and sends no request for it", async () => {
    const s = scenario("resume", LOSS_SKILL(), [{ id: "c04" }, { id: "c05" }]);
    recordCaptures(s, ["old", "new", "none"]);
    answer = behaviourStandIn;
    usage = { input: 10, output: 3 };
    const early = await cli(behaviour("confirm", s));
    expect(early.code).toBe(2);
    expect(early.stderr).toContain("the screen is not finished: case c04 is not screened yet");

    let from = recorder.requests.length;
    expect((await cli(behaviour("screen", s))).code).toBe(0);
    expect(requestsSince(from)).toHaveLength(2 * 63);
    const c04 = readFileSync(caseResultFile(s.out, "c04"), "utf8");

    from = recorder.requests.length;
    const again = await cli(behaviour("screen", s));
    expect(again.code).toBe(0);
    expect(again.stdout).toContain("case c04: screened before, skipped");
    expect(again.stdout).toContain("case c05: screened before, skipped");
    expect(requestsSince(from)).toHaveLength(0);

    rmSync(caseResultFile(s.out, "c05"));
    from = recorder.requests.length;
    const rerun = await cli(behaviour("screen", s));
    expect(rerun.code).toBe(0);
    expect(requestsSince(from)).toHaveLength(63);
    expect(readFileSync(caseResultFile(s.out, "c04"), "utf8")).toBe(c04);
    expect(result(s.out, "c05").stages.screen?.state).toBe("counted");
  }, 120_000);

  test("the budget stops new calls, and a run started again goes on from the runs saved", async () => {
    const s = scenario("budget", LOSS_SKILL(), [{ id: "c04" }]);
    recordCaptures(s, ["old", "new", "none"]);
    answer = behaviourStandIn;
    // 100,000 prompt tokens a reply: $0.40 a judgement at claude-opus-5-5's list price, $0.20 a
    // replay at claude-sonnet-5-5's.
    usage = { input: 100_000, output: 3 };
    let from = recorder.requests.length;
    const stopped = await cli(behaviour("screen", s, "--budget-usd", "1", "--concurrency", "1"));
    expect(stopped.code).toBe(3);
    expect(stopped.stderr).toContain("the budget is spent");
    // The three judge controls (to $1.20), then no new call starts: no replay is made.
    expect(requestsSince(from)).toHaveLength(3);
    const logged = readFileSync(path.join(s.out, "requests.jsonl"), "utf8")
      .trim()
      .split("\n")
      .map((line) => JSON.parse(line));
    expect(logged.filter((line) => line.usage !== undefined)).toHaveLength(3);
    expect(
      logged
        .filter((line) => line.usage === undefined)
        .every((line) => line.error === "BudgetSpent")
    ).toBe(true);
    const saved = result(s.out, "c04");
    expect(saved.controls?.hold).toBe(true);
    expect(saved.stages.screen).toMatchObject({ state: "running", runs: [] });

    usage = { input: 10, output: 3 };
    from = recorder.requests.length;
    const resumed = await cli(behaviour("screen", s));
    expect(resumed.stderr).toBe("");
    expect(resumed.code).toBe(0);
    // The controls are not judged again: 30 replays and their 30 judgements.
    expect(requestsSince(from)).toHaveLength(60);
    expect(result(s.out, "c04").stages.screen?.state).toBe("counted");
  }, 120_000);

  test("a cut that keeps the line keeps it, and a control that leaves out a line the cases do not need gives no verdict", async () => {
    const s = scenario(
      "keeps",
      f1File(2),
      ["c04", "c05", "c06", "c07", "c08", "c09"].map((id) => ({ id }))
    );
    recordCaptures(s, ["old", "new", "none", "minus"]);
    answer = behaviourStandIn;
    usage = { input: 10, output: 3 };
    expect((await cli(behaviour("screen", s))).code).toBe(0);
    let from = recorder.requests.length;
    const confirm = await cli(behaviour("confirm", s));
    expect(confirm.code).toBe(0);
    expect(confirm.stdout).toContain("0 flagged cases");
    expect(requestsSince(from)).toHaveLength(0);

    const planted = { ...s, out: path.join(s.dir, "out-planted") };
    cpSync(s.out, planted.out, { recursive: true });
    recordCaptures(s, ["minus"], OTHER_LINE, planted.out);

    expect((await cli(behaviour("control", s))).code).toBe(0);
    const kept = (await cli(report(s))).stdout.split("\n");
    expect(kept).toContain(
      "case c04: screen: old 10/10, new 10/10, none 0/10: informative, not flagged; control: old 40/40, minus 0/40: a control loss"
    );
    expect(kept).toContain(
      "verdict: no loss detected by the registered rule: no confirmed case is a loss (0 flagged of 6 informative), and the planted-loss control (case c04) is a loss"
    );

    from = recorder.requests.length;
    const control = await cli(behaviour("control", planted, "--planted-line", OTHER_LINE));
    expect(control.code).toBe(0);
    expect(control.stdout).toContain(`minus leaves out the planted line "${OTHER_LINE}"`);
    expect(requestsSince(from)).toHaveLength(160);
    expect(passes(result(planted.out, "c04"), "control")).toEqual({ old: 40, minus: 40 });
    expect((await cli(report(planted))).stdout.split("\n")).toContain(
      "verdict: none: control not a loss"
    );
    // A control made with one planted line is not taken for one made with another.
    const other = await cli(behaviour("control", planted));
    expect(other.code).toBe(2);
    expect(other.stderr).toContain("were made with other skill texts");
  }, 300_000);

  test("stops above 1 GiB of peak resident memory, once the case is saved", async () => {
    const s = scenario("memory", LOSS_SKILL(), [{ id: "c04" }, { id: "c05" }]);
    recordCaptures(s, ["old", "new", "none"]);
    answer = behaviourStandIn;
    usage = { input: 10, output: 3 };
    const err: string[] = [];
    const io: CliIo = {
      out: () => {},
      err: (line) => err.push(line),
      peakRssBytes: () => 1024 ** 3 + 1,
    };
    expect(await main(behaviour("screen", s), io)).toBe(3);
    expect(err.join("\n")).toContain("is above 1 GiB, so the run stops after case c04");
    expect(result(s.out, "c04").peakRssBytes).toBe(1024 ** 3 + 1);
    expect(existsSync(caseResultFile(s.out, "c05"))).toBe(false);
  }, 120_000);

  test("Oh My Pi is --omp, LEGION_OMP_PATH or the omp on PATH, as an absolute path, and behaviour and load given --models refuse by name when none names one", async () => {
    const bin = path.join(scratch, "omp-bin");
    mkdirSync(bin, { recursive: true });
    const fake = path.join(bin, "omp");
    writeFileSync(fake, "#!/bin/sh\nexit 0\n", { mode: 0o755 });
    expect(ompBinary(undefined, { PATH: `/nowhere:${bin}` })).toBe(fake);
    expect(ompBinary(undefined, { PATH: "/nowhere", LEGION_OMP_PATH: fake })).toBe(fake);
    expect(ompBinary(path.relative(process.cwd(), fake), { PATH: "/nowhere" })).toBe(fake);
    expect(ompBinary(undefined, { PATH: "/nowhere" })).toBeUndefined();
    expect(ompBinary(path.join(bin, "missing"), { PATH: bin })).toBeUndefined();

    const none = { ...ENV, PATH: "/nowhere", LEGION_OMP_PATH: "" };
    const s = scenario("omp-missing", LOSS_SKILL(), [{ id: "c00" }]);
    const models = path.join(s.dir, "models.yml");
    writeFileSync(models, "providers: {}\n");
    const screen = await cli(behaviour("screen", s, "--models", models), none);
    expect(screen.code).toBe(2);
    expect(screen.stderr).toContain(
      "--models makes captures, which run Oh My Pi, and none is found: no --omp is given, LEGION_OMP_PATH is unset, and no omp is on PATH"
    );
    const { config, keyFile } = setup();
    const load = await cli(
      [
        "load",
        "--config",
        config,
        "--key-file",
        keyFile,
        "--skill",
        s.old,
        "--tasks",
        path.join(s.dir, "tasks.jsonl"),
        "--host",
        "omp",
        "--profile",
        path.join(s.dir, "profile.json"),
        "--out",
        path.join(s.dir, "load"),
        "--models",
        models,
        "--omp",
        path.join(bin, "missing"),
      ],
      none
    );
    expect(load.code).toBe(2);
    expect(load.stderr).toContain(`--omp ${path.join(bin, "missing")} names no file`);
  }, 60_000);

  test("the false-loss chance is the procedure's own, exact, and old at 10 of 10 no longer gives 0", () => {
    /** The chance of each count of `runs` runs that each pass with chance `p`. */
    const pmf = (runs: number, p: number) => {
      const choose = (k: number) => {
        let ways = 1;
        for (let i = 1; i <= k; i += 1) ways = (ways * (runs - k + i)) / i;
        return ways;
      };
      return Array.from({ length: runs + 1 }, (_, k) => choose(k) * p ** k * (1 - p) ** (runs - k));
    };
    /** The rules table's figures, every outcome counted: of the screen's 10 runs a case is
     *  informative at old − none ≥ 2 and flagged at old − new ≥ 2 too, and of the confirm's 40
     *  fresh runs a loss at old − new ≥ 10. */
    const bruteForce = (old: number, cut: number, none: number) => {
      const [o, n, z] = [pmf(10, old), pmf(10, cut), pmf(10, none)];
      let informative = 0;
      let flagged = 0;
      for (let a = 0; a <= 10; a += 1) {
        for (let b = 0; b <= 10; b += 1) {
          for (let c = 0; c <= 10; c += 1) {
            const chance = (o[a] ?? 0) * (n[b] ?? 0) * (z[c] ?? 0);
            if (a - c < 2) continue;
            informative += chance;
            if (a - b >= 2) flagged += chance;
          }
        }
      }
      const [O, N] = [pmf(40, old), pmf(40, cut)];
      let loss = 0;
      for (let a = 0; a <= 40; a += 1) {
        for (let b = 0; b <= a - 10; b += 1) loss += (O[a] ?? 0) * (N[b] ?? 0);
      }
      return (flagged / informative) * loss;
    };
    /** Whether `exact` is `counted` but for rounding: a relative difference below 1e-9. */
    const agrees = (exact: number, counted: number) =>
      counted > 0 && Math.abs(exact - counted) / counted < 1e-9;
    // Old and cut pooled with a Laplace term, none with its own: (o + n + 1)/22 and (z + 1)/12.
    for (const passes of [
      { old: 7, new: 6, none: 2 },
      { old: 9, new: 4, none: 0 },
    ]) {
      const p = (passes.old + passes.new + 1) / 22;
      const counted = bruteForce(p, p, (passes.none + 1) / 12);
      expect({ passes, agrees: agrees(falseLossChance(passes), counted) }).toEqual({
        passes,
        agrees: true,
      });
    }
    // The plug-in figure, the chance at the old text's own screen rate, gave 0 for a case whose old
    // text passed 10 of 10; this one does not.
    const full = falseLossChance({ old: 10, new: 10, none: 0 });
    expect(full).toBeGreaterThan(0);
    expect(agrees(full, bruteForce(21 / 22, 21 / 22, 1 / 12))).toBe(true);
    // A sensitivity point: the old text at 0.9, the cut's at 0.7, no skill at 0.1 (about 20%).
    const point = lossChance({ old: 0.9, cut: 0.7, none: 0.1 });
    expect(agrees(point, bruteForce(0.9, 0.7, 0.1))).toBe(true);
    expect(point).toBeGreaterThan(0.2);
    expect(point).toBeLessThan(0.21);
    expect(() => lossChance({ old: 0, cut: 0, none: 0.1 })).toThrow("no case is informative");
  });
});
