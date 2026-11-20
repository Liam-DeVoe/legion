import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { mkdirSync, readdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { z } from "zod";
import { skillHeaderPath } from "../skill-scenarios/transcript";
import { skillListing } from "./capture";
import { pathHash } from "./cases";
import { type CliIo, main } from "./cli";
import { ContentBlock, ReplyMessage, streamedMessage } from "./gateway";
import {
  claudeListing,
  firstMessageLoad,
  type Host,
  LoadRun,
  loadCapturePaths,
  loadRunFile,
  renamedSkill,
  skillOutcome,
  type TestedSkill,
  taskOutcome,
} from "./load";
import {
  messageSse,
  type RecordedRequest,
  type Recorder,
  type ReplyBlock,
  sseResponse,
  startRecorder,
} from "./recorder";
import type { TaskLabel } from "./score";

// The load test (plan, R5 `load.ts`). Its detection rows read the request bodies of the pinned
// hosts and the gateway's replies to them (testdata/CAPTURE.md): F2, the body Oh My Pi 18.18.6
// sends for a replayed history whose assistant message reads skill://alpha (`omp -p --mode json
// --no-session --hook <replay hook>` against the recorder); F5, the gateway's replies to bodies
// made from F2 (`bun send.ts <body.json>`); and F3, the bodies Claude Code 2.1.311 sent for a task
// that calls `Skill` on acme-tools:alpha and `Read` on beta's SKILL.md (`CLAUDE_CONFIG_DIR=<scratch>
// ANTHROPIC_BASE_URL=<recording proxy> claude -p --output-format stream-json --verbose
// "<prompt>"`). The command rows run `skill-cuts load` in this process against a stand-in gateway
// on 127.0.0.1 built on recorder.ts, with captures saved where the command reads them back, so no
// host runs and no row reaches a network. The rows that run a real host are skipped without
// LEGION_TEST_OMP (refused on GitHub Actions without it, where the pi-legion job installs the pin,
// as capture.test.ts does), and without LEGION_TEST_CLAUDE naming the Claude Code binary (which
// that job does not install). Every skill, repository and store here is made up.

const TESTDATA = path.join(import.meta.dir, "testdata");
const omp = process.env.LEGION_TEST_OMP;
const claude = process.env.LEGION_TEST_CLAUDE;
if (omp === undefined && process.env.GITHUB_ACTIONS === "true") {
  throw new Error("LEGION_TEST_OMP is unset on GitHub Actions");
}
/** A row's own timeout when it runs a real host: a few of its runs, about ten seconds each in a
 *  slow pod. */
const HOST_ROW_TIMEOUT_MS = 600_000;

/** A request body, as far as these rows read one. */
const Body = z.looseObject({
  messages: z.array(
    z.looseObject({ role: z.string(), content: z.union([z.string(), z.array(ContentBlock)]) })
  ),
});
type Body = z.infer<typeof Body>;

/** The request body in testdata file `name`. */
function fixture(name: string): Body {
  return Body.parse(JSON.parse(readFileSync(path.join(TESTDATA, name), "utf8")));
}
/** The content blocks of message `index` of `body`. */
function blocksOf(body: Body, index: number): ContentBlock[] {
  const content = body.messages[index]?.content;
  if (!Array.isArray(content)) throw new Error(`message ${index} holds no content blocks`);
  return content;
}
/** The content of the F5 reply `name`, read whole as gateway.ts reads a streamed reply. */
function f5(name: string): ContentBlock[] {
  const sse = readFileSync(path.join(TESTDATA, `F5-${name}.sse`), "utf8");
  return ReplyMessage.parse(streamedMessage(sse)).content;
}

const F2 = fixture("F2-request.json");
const F3 = fixture("F3-request-last.json");
/** F2's assistant message: one `read` of skill://alpha. */
const F2_READ = blocksOf(F2, 1);
/** The path of alpha's SKILL.md, as F2's read result names it in its `[Skill file: …]` line. */
const ALPHA_PATH =
  skillHeaderPath(
    z
      .looseObject({ content: z.array(z.looseObject({ text: z.string() })) })
      .parse(blocksOf(F2, 2)[0]).content[0]?.text ?? ""
  ) ?? "";
const F5_TOOLS = f5("skill-read-and-other");
/** F5's `bash` call, made in a message beside reads of skill://beta and of alpha's reference. */
const BASH = F5_TOOLS.filter((block) => block.name === "bash");
/** F3's assistant messages: a `Skill` call on acme-tools:alpha, and a `Read` of beta's SKILL.md. */
const F3_SKILL = blocksOf(F3, 2);
const F3_READ = blocksOf(F3, 5);
const F3_READ_PATH = z.object({ file_path: z.string() }).parse(F3_READ[0]?.input).file_path;
const F3_CALL = F3_SKILL[0] ?? { type: "tool_use" };
const CLAUDE_BASH: ContentBlock[] = [
  { type: "tool_use", id: "toolu_bash_1", name: "Bash", input: { command: "ls" } },
];

const alpha: TestedSkill = { name: "alpha" };
const beta: TestedSkill = { name: "beta" };
const pluginAlpha: TestedSkill = { name: "alpha", plugin: "acme-tools" };
const pluginBeta: TestedSkill = { name: "beta", plugin: "acme-tools" };
const none = (toolCalls: string[]) => ({ loaded: false, loadedBy: null, toolCalls });

describe("a run loads the skill when its first assistant message reads it", () => {
  const rows: [string, Host, readonly ContentBlock[], TestedSkill, unknown][] = [
    [
      "Oh My Pi: a read of skill://alpha loads alpha (F2)",
      "omp",
      F2_READ,
      alpha,
      { loaded: true, loadedBy: "read skill://alpha", toolCalls: ["read"] },
    ],
    [
      "Oh My Pi: a read of alpha's SKILL.md by its path loads alpha (the path of F2's read result)",
      "omp",
      [{ type: "tool_use", id: "toolu_path_1", name: "read", input: { path: ALPHA_PATH } }],
      alpha,
      { loaded: true, loadedBy: `read ${ALPHA_PATH}`, toolCalls: ["read"] },
    ],
    [
      "Oh My Pi: a read of skill://alpha with a read selector loads alpha",
      "omp",
      [
        {
          type: "tool_use",
          id: "toolu_sel_1",
          name: "read",
          input: { path: "skill://alpha:1-40" },
        },
      ],
      alpha,
      { loaded: true, loadedBy: "read skill://alpha:1-40", toolCalls: ["read"] },
    ],
    [
      "Oh My Pi: a read of another skill does not load beta (F2)",
      "omp",
      F2_READ,
      beta,
      none(["read"]),
    ],
    [
      "Oh My Pi: a read of alpha's reference does not load alpha (F5 skill-read)",
      "omp",
      f5("skill-read"),
      alpha,
      none(["read"]),
    ],
    [
      "Oh My Pi: reads of another skill and of alpha's reference beside a bash call do not load alpha (F5)",
      "omp",
      F5_TOOLS,
      alpha,
      none(["read", "bash", "read"]),
    ],
    [
      "Oh My Pi: the same message loads beta, by its read of skill://beta (F5)",
      "omp",
      F5_TOOLS,
      beta,
      { loaded: true, loadedBy: "read skill://beta", toolCalls: ["read", "bash", "read"] },
    ],
    [
      "Oh My Pi: another tool first, and no read, does not load alpha (F5's bash call)",
      "omp",
      BASH,
      alpha,
      none(["bash"]),
    ],
    [
      "Oh My Pi: another tool first, then a read of skill://alpha in the same message, loads alpha",
      "omp",
      [...BASH, ...F2_READ],
      alpha,
      { loaded: true, loadedBy: "read skill://alpha", toolCalls: ["bash", "read"] },
    ],
    ["Oh My Pi: text only does not load alpha (F5 text)", "omp", f5("text"), alpha, none([])],
    [
      "Claude Code: a Skill call naming acme-tools:alpha loads alpha of acme-tools (F3)",
      "claude",
      F3_SKILL,
      pluginAlpha,
      { loaded: true, loadedBy: "Skill acme-tools:alpha", toolCalls: ["Skill"] },
    ],
    [
      "Claude Code: a Skill call naming plain alpha loads alpha",
      "claude",
      [{ ...F3_CALL, input: { skill: "alpha" } }],
      pluginAlpha,
      { loaded: true, loadedBy: "Skill alpha", toolCalls: ["Skill"] },
    ],
    [
      "Claude Code: a Skill call naming another plugin's alpha does not load alpha of acme-tools",
      "claude",
      [{ ...F3_CALL, input: { skill: "other-tools:alpha" } }],
      pluginAlpha,
      none(["Skill"]),
    ],
    [
      "Claude Code: a Skill call on alpha does not load beta (F3)",
      "claude",
      F3_SKILL,
      pluginBeta,
      none(["Skill"]),
    ],
    [
      "Claude Code: a Read of beta's SKILL.md loads beta (F3)",
      "claude",
      F3_READ,
      pluginBeta,
      { loaded: true, loadedBy: `Read ${F3_READ_PATH}`, toolCalls: ["Read"] },
    ],
    [
      "Claude Code: a Read of beta's SKILL.md does not load alpha (F3)",
      "claude",
      F3_READ,
      pluginAlpha,
      none(["Read"]),
    ],
    [
      "Claude Code: another tool first, and no Skill call or Read of it, does not load alpha",
      "claude",
      CLAUDE_BASH,
      pluginAlpha,
      none(["Bash"]),
    ],
    [
      "Claude Code: another tool first, then a Skill call on alpha in the same message, loads alpha",
      "claude",
      [...CLAUDE_BASH, ...F3_SKILL],
      pluginAlpha,
      { loaded: true, loadedBy: "Skill acme-tools:alpha", toolCalls: ["Bash", "Skill"] },
    ],
    [
      "Claude Code: Oh My Pi's read of skill://alpha is no Claude Code load (F2)",
      "claude",
      F2_READ,
      pluginAlpha,
      none(["read"]),
    ],
  ];
  test.each(rows)("%s", (_name, host, content, skill, want) => {
    expect(ALPHA_PATH).toEndWith("/alpha/SKILL.md");
    expect(firstMessageLoad(host, content, skill)).toEqual(want as never);
  });

  test("Claude Code's listing gives each plugin skill as <plugin>:<name> with its description (F3)", () => {
    const listing = claudeListing(
      JSON.parse(readFileSync(path.join(TESTDATA, "F3-request-first.json"), "utf8"))
    );
    expect(listing.get("acme-tools:alpha")).toBe(
      "Use when a task asks for the alpha checklist of the made-up fixture project; it lists the three alpha steps."
    );
    expect(listing.get("acme-tools:beta")).toStartWith(
      "Use when a task asks for the beta greeting"
    );
    expect(listing.has("alpha")).toBe(false);
  });
});

/** The runs of one task, each loading the skill or not, as its run file keeps them. */
function runsOf(label: TaskLabel, loads: readonly boolean[]): LoadRun[] {
  return loads.map((loaded, index) =>
    LoadRun.parse({
      task: "t",
      run: index + 1,
      label,
      source: "written",
      host: "omp",
      name: "alpha",
      loaded,
      loadedBy: loaded ? "read skill://alpha" : null,
      toolCalls: loaded ? ["read"] : [],
      stopReason: loaded ? "tool_use" : "end_turn",
      requestId: null,
      usage: {
        model: "claude-sonnet-5-5",
        input: 1,
        output: 1,
        cacheRead: 0,
        cacheWrite: 0,
        usd: 0,
      },
      listed: "Use for the alpha checklist.",
    })
  );
}

describe("each task's loads feed score.ts's load rule", () => {
  const rows: [string, TaskLabel, boolean[], boolean | null][] = [
    ["a need task that loads in 1 of 3 runs is a fault", "need", [false, true, false], true],
    ["a need task that loads in 2 of 3 runs is no fault", "need", [true, false, true], false],
    [
      "a near-miss task that loads in 2 of 3 runs is a fault",
      "near-miss",
      [true, true, false],
      true,
    ],
    [
      "a near-miss task that loads in 1 of 3 runs is no fault",
      "near-miss",
      [false, false, true],
      false,
    ],
    ["a task with 2 of its 3 runs made has no verdict yet", "need", [true, true], null],
  ];
  test.each(rows)("%s", (_name, label, loads, fault) => {
    const outcome = taskOutcome(label, runsOf(label, loads), 3);
    expect(outcome.fault).toBe(fault);
    expect(outcome.loads).toBe(loads.filter(Boolean).length);
    expect(outcome.complete).toBe(loads.length === 3);
  });

  test("the skill's verdict is a description fault when one task is a fault, none when no task is, and waits for every task's runs", () => {
    const task = (id: string, label: TaskLabel, loads: boolean[], wanted = 3) => ({
      id,
      label,
      outcome: taskOutcome(label, runsOf(label, loads), wanted),
    });
    const good = [
      task("n1", "need", [true, true, false]),
      task("m1", "near-miss", [true, false, false]),
    ];
    expect(skillOutcome(good, 3)).toEqual({
      descriptionFault: false,
      reason: "no description fault in 2 tasks",
    });
    expect(skillOutcome([...good, task("n2", "need", [false, false, true])], 3)).toEqual({
      descriptionFault: true,
      reason: "a description fault: tasks n2",
    });
    expect(skillOutcome([...good, task("n3", "need", [true])], 3)).toEqual({
      descriptionFault: null,
      reason: "no verdict yet: tasks n3 lack runs",
    });
    expect(skillOutcome([task("n1", "need", [true], 1)], 1)).toEqual({
      descriptionFault: null,
      reason: "no verdict: R6's rule is of 3 runs a task, and this test runs 1",
    });
  });
});

const scratch = temporaryDirectory("skill-cuts-load", { real: true });

/** Writes `files` (path to text) under `root`. */
function writeTree(root: string, files: Record<string, string>): void {
  for (const [file, text] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(root, file)), { recursive: true });
    writeFileSync(path.join(root, file), text);
  }
}

/** Each file under `dir`, by its path inside it, with its text. */
function treeOf(dir: string): Record<string, string> {
  return Object.fromEntries(
    readdirSync(dir, { recursive: true, encoding: "utf8" })
      .filter((file) => statSync(path.join(dir, file)).isFile())
      .sort()
      .map((file) => [file, readFileSync(path.join(dir, file), "utf8")])
  );
}

const ALPHA_SKILL = [
  "---",
  "name: alpha",
  "description: Use when a task asks for the alpha checklist of the made-up project.",
  "---",
  "",
  "# Alpha",
  "",
  "Read skill://alpha/references/x.md before the first alpha step.",
  "",
].join("\n");
const ALPHA_X = "The alpha steps: one, two, three.\n";
const BETA_SKILL =
  "---\nname: beta\ndescription: Use when a task asks for the beta greeting of the made-up project.\n---\n\n# Beta\n\nGreet with one line.\n";

describe("--name tests the skill under another name", () => {
  test("the scratch copy changes only the frontmatter name and the directory's name", () => {
    const dir = path.join(scratch, "rename", "alpha");
    writeTree(dir, { "SKILL.md": ALPHA_SKILL, "references/x.md": ALPHA_X });
    const copy = renamedSkill(dir, "omega", path.join(scratch, "rename", "copies"));
    expect(path.basename(copy)).toBe("omega");
    expect(treeOf(copy)).toEqual({
      "SKILL.md": ALPHA_SKILL.replace("name: alpha", "name: omega"),
      "references/x.md": ALPHA_X,
    });
    // The skill's own text still names alpha: only the frontmatter's name line changed.
    expect(readFileSync(path.join(copy, "SKILL.md"), "utf8")).toContain(
      "skill://alpha/references/x.md"
    );
    expect(treeOf(dir)).toEqual({ "SKILL.md": ALPHA_SKILL, "references/x.md": ALPHA_X });
  });

  test("a frontmatter with no name gets one, and every other line stays", () => {
    const dir = path.join(scratch, "rename", "unnamed");
    const text = ALPHA_SKILL.replace("name: alpha\n", "");
    writeTree(dir, { "SKILL.md": text });
    const copy = renamedSkill(dir, "omega", path.join(scratch, "rename", "copies"));
    expect(readFileSync(path.join(copy, "SKILL.md"), "utf8")).toBe(
      text.replace("---\n", "---\nname: omega\n")
    );
  });
});

// The command, against the stand-in gateway.

/** What one command wrote and returned. */
interface Ran {
  readonly code: number;
  readonly stdout: string;
  readonly stderr: string;
}

/** Runs `skill-cuts <args>` in this process. */
async function cli(args: readonly string[]): Promise<Ran> {
  let stdout = "";
  let stderr = "";
  const io: CliIo = {
    out: (text) => {
      stdout += text;
    },
    err: (line) => {
      stderr += `${line}\n`;
    },
    peakRssBytes: () => 0,
  };
  const code = await main(args, io);
  return { code, stdout, stderr };
}

/** A request the stand-in takes, as far as it reads one. */
const Request = z.looseObject({
  model: z.string(),
  system: z.unknown().optional(),
  tools: z.unknown().optional(),
  messages: z.unknown(),
});
type Request = z.infer<typeof Request>;

/** How the stand-in answers a run's request. */
let answer: (body: Request) => ReplyBlock[] = () => [{ type: "text", text: "nothing" }];
let recorder: Recorder;
beforeAll(() => {
  recorder = startRecorder({
    route: (request: RecordedRequest) => {
      if (request.method !== "POST" || request.path !== "/v1/messages") return undefined;
      const body = Request.parse(JSON.parse(request.body));
      // Oh My Pi's hint request: no system prompt, no tools, `Skill: <name>` in its message.
      const hint = /\\nSkill: ([^\\]+)\\nDescription: /.exec(request.body)?.[1];
      const content: ReplyBlock[] =
        body.system === undefined && body.tools === undefined && hint !== undefined
          ? [{ type: "text", text: `Use for the ${hint} work, stand-in hint.` }]
          : answer(body);
      return sseResponse(
        messageSse({
          id: `msg_standin_${request.n}`,
          model: body.model,
          content,
          stopReason: content.some((block) => block.type === "tool_use") ? "tool_use" : "end_turn",
        }),
        `req_standin_${request.n}`
      );
    },
  });
});
afterAll(async () => {
  await recorder.close();
});

/** A configuration whose gateway is the stand-in, and a key file, under `dir`. */
function setup(dir: string): { config: string; keyFile: string; key: string } {
  mkdirSync(dir, { recursive: true });
  const config = path.join(dir, "skill-cuts.json");
  const made = JSON.parse(readFileSync(path.join(TESTDATA, "F7-skill-cuts.json"), "utf8"));
  writeFileSync(config, JSON.stringify({ ...made, gatewayBaseUrl: recorder.url }));
  const key = fixtureSecret("gw");
  const keyFile = path.join(dir, "key");
  writeFileSync(keyFile, key, { mode: 0o600 });
  return { config, keyFile, key };
}

/** Every string inside `value`. */
function texts(value: unknown): string[] {
  if (typeof value === "string") return [value];
  if (Array.isArray(value)) return value.flatMap(texts);
  if (typeof value === "object" && value !== null) return Object.values(value).flatMap(texts);
  return [];
}

/** The runs of task `id`'s run file under `out`. */
function savedRuns(out: string, id: string): LoadRun[] {
  return readFileSync(loadRunFile(out, id), "utf8")
    .trim()
    .split("\n")
    .map((line) => LoadRun.parse(JSON.parse(line)));
}

const READ_ALPHA: ReplyBlock[] = [
  { type: "tool_use", id: "toolu_alpha", name: "read", input: { path: "skill://alpha" } },
];
const TEXT: ReplyBlock[] = [{ type: "text", text: "Here is the answer." }];
const COMMIT = "0123456789abcdef0123456789abcdef01234567";
const NEED = "Write the alpha checklist of the made-up project.";
const NEAR = "Write the beta greeting of the made-up project.";
const BETA_PROFILE = {
  skills: [{ repo: "acme/skills", commit: COMMIT, path: "skills/beta", name: "beta" }],
  plugins: [],
  contextFiles: [],
};

describe("skill-cuts load with saved captures", () => {
  const dir = path.join(scratch, "saved");
  const out = path.join(dir, "out");
  const skill = path.join(dir, "alpha");
  const tasks = path.join(dir, "tasks.jsonl");
  const profile = path.join(dir, "profile.json");
  let args: string[] = [];
  let key = "";
  /** Of each task's text, the runs the stand-in answers with a read of skill://alpha. */
  const reads = new Map([
    [NEED, 2],
    [NEAR, 2],
  ]);
  const answered = new Map<string, number>();

  beforeAll(() => {
    const made = setup(dir);
    key = made.key;
    writeTree(skill, { "SKILL.md": ALPHA_SKILL, "references/x.md": ALPHA_X });
    const lines = [
      {
        id: "need-1",
        text: NEED,
        label: "need",
        source: "session",
        store: "store_a",
        pathHash: pathHash("/sessions/a.jsonl"),
      },
      { id: "near-1", text: NEAR, label: "near-miss", source: "written" },
    ];
    writeFileSync(tasks, lines.map((line) => `${JSON.stringify(line)}\n`).join(""));
    writeFileSync(profile, JSON.stringify(BETA_PROFILE));
    // Each task's capture, saved where the command reads it back: F2's body with the task's text
    // as its one message, and the fields of its record that the load test reads.
    for (const { id, text } of lines) {
      const files = loadCapturePaths(out, "omp", id);
      writeTree(path.dirname(files.body), {
        [path.basename(files.body)]: JSON.stringify({
          ...F2,
          messages: [{ role: "user", content: [{ type: "text", text }] }],
        }),
        [path.basename(files.record)]: JSON.stringify({
          listing: [
            { name: "alpha", hint: "Use for the alpha checklist." },
            { name: "beta", hint: "Use for the beta greeting." },
          ],
          requestHeaders: { "anthropic-version": "2023-06-01" },
          hintCalls: [],
        }),
      });
    }
    answer = (body) => {
      const text = texts(body.messages).find((each) => reads.has(each)) ?? "";
      const n = (answered.get(text) ?? 0) + 1;
      answered.set(text, n);
      return n <= (reads.get(text) ?? 0) ? READ_ALPHA : TEXT;
    };
    args = [
      "load",
      "--config",
      made.config,
      "--key-file",
      made.keyFile,
      "--skill",
      skill,
      "--tasks",
      tasks,
      "--host",
      "omp",
      "--profile",
      profile,
      "--out",
      out,
      "--omp",
      path.join(dir, "no-omp-runs-here"),
      "--scratch",
      path.join(dir, "scratch"),
      "--cache-dir",
      path.join(dir, "cache"),
    ];
  });

  test("sends each task's body 3 times, prints each task's loads and verdict and the skill's, and writes a run file per task and the summary", async () => {
    const before = recorder.requests.length;
    const ran = await cli(args);
    expect({ code: ran.code, stderr: ran.stderr }).toEqual({ code: 0, stderr: "" });
    expect(recorder.requests.length - before).toBe(6);
    expect(ran.stdout).toContain(
      'load: Oh My Pi lists alpha under the hint "Use for the alpha checklist."'
    );
    expect(ran.stdout).toMatch(
      /task need-1 \(need, from a session\): loaded in 2 of 3 runs \(.*\): no fault/
    );
    expect(ran.stdout).toMatch(
      /task near-1 \(near-miss, written\): loaded in 2 of 3 runs \(.*\): a fault: a near-miss task loads it in more than 1 of 3 runs/
    );
    expect(ran.stdout).toContain(
      "the skill alpha: a description fault: tasks near-1 (R6's test has at least 5 need and 5 near-miss tasks; this one has 1 and 1)"
    );
    for (const id of ["need-1", "near-1"]) {
      const runs = savedRuns(out, id);
      expect(runs.map((run) => run.run)).toEqual([1, 2, 3]);
      expect(runs.filter((run) => run.loaded).map((run) => run.loadedBy)).toEqual([
        "read skill://alpha",
        "read skill://alpha",
      ]);
      expect(runs.every((run) => run.listed === "Use for the alpha checklist.")).toBe(true);
      expect(runs.every((run) => run.usage.model === "claude-sonnet-5-5")).toBe(true);
    }
    const summary = z
      .looseObject({
        verdict: z.unknown(),
        tested: z.unknown(),
        tasks: z.array(z.looseObject({ id: z.string(), loads: z.number(), fault: z.unknown() })),
      })
      .parse(JSON.parse(readFileSync(path.join(out, "summary.json"), "utf8")));
    expect(summary.verdict).toEqual({
      descriptionFault: true,
      reason: "a description fault: tasks near-1",
    });
    expect(summary.tasks.map((task) => [task.id, task.loads, task.fault])).toEqual([
      ["need-1", 2, false],
      ["near-1", 2, true],
    ]);
    expect(summary.tested).toEqual({ name: "alpha", renamedFrom: null, changed: "nothing" });
    // Each run went with the header the host sent, and no output holds the key.
    const sent = recorder.requests.slice(before);
    expect(sent.every((request) => request.headers["anthropic-version"] === "2023-06-01")).toBe(
      true
    );
    const log = readFileSync(path.join(out, "requests.jsonl"), "utf8");
    expect(
      log
        .trim()
        .split("\n")
        .map((line) => JSON.parse(line).kind)
    ).toEqual(Array(6).fill("load"));
    for (const text of [ran.stdout, log, JSON.stringify(summary)]) expect(text).not.toContain(key);
  });

  test("a command started again skips each task whose run file is complete, and makes only the runs a task lacks", async () => {
    let before = recorder.requests.length;
    const again = await cli(args);
    expect(again.code).toBe(0);
    expect(recorder.requests.length).toBe(before);
    expect(again.stdout).toContain(
      "task need-1 (need, from a session): its run file is complete, skipped"
    );
    const file = loadRunFile(out, "near-1");
    const lines = readFileSync(file, "utf8").trim().split("\n");
    writeFileSync(file, `${lines.slice(0, 2).join("\n")}\n`);
    before = recorder.requests.length;
    const resumed = await cli(args);
    expect(resumed.code).toBe(0);
    expect(recorder.requests.length - before).toBe(1);
    expect(savedRuns(out, "near-1").map((run) => run.run)).toEqual([1, 2, 3]);
  });

  test("an --out of another test's settings, a task id given twice and a profile that installs the tested name are refused before any model call", async () => {
    const before = recorder.requests.length;
    const renamed = await cli([...args, "--name", "omega"]);
    expect(renamed.code).toBe(2);
    expect(renamed.stderr).toContain("holds a load test of other settings");
    const twice = path.join(dir, "twice.jsonl");
    writeFileSync(twice, `${readFileSync(tasks, "utf8")}${readFileSync(tasks, "utf8")}`);
    const dup = await cli(args.map((arg) => (arg === tasks ? twice : arg)));
    expect(dup.code).toBe(2);
    expect(dup.stderr).toContain("the task id need-1 is given twice");
    const own = path.join(dir, "own-profile.json");
    writeFileSync(
      own,
      JSON.stringify({
        ...BETA_PROFILE,
        skills: [{ repo: "acme/skills", commit: COMMIT, path: "skills/alpha", name: "alpha" }],
      })
    );
    const mine = await cli(args.map((arg) => (arg === profile ? own : arg)));
    expect(mine.code).toBe(2);
    expect(mine.stderr).toContain(
      "it installs a skill named alpha, the name the skill is tested under"
    );
    expect(recorder.requests.length).toBe(before);
  });
});

/** Made-up repositories, the tested skill and one need task for a real host's run, under `dir`,
 *  with the stand-in's configuration. */
function hostInputs(dir: string) {
  const made = setup(dir);
  const repos = path.join(dir, "repos");
  writeTree(path.join(repos, "acme/skills", COMMIT), { "skills/beta/SKILL.md": BETA_SKILL });
  const skill = path.join(dir, "alpha");
  writeTree(skill, { "SKILL.md": ALPHA_SKILL, "references/x.md": ALPHA_X });
  const tasks = path.join(dir, "tasks.jsonl");
  writeFileSync(
    tasks,
    `${JSON.stringify({ id: "need-1", text: NEED, label: "need", source: "written" })}\n`
  );
  const out = path.join(dir, "out");
  const common = [
    "--config",
    made.config,
    "--key-file",
    made.keyFile,
    "--skill",
    skill,
    "--tasks",
    tasks,
    "--out",
    out,
    "--runs",
    "1",
    "--repos",
    repos,
    "--scratch",
    path.join(dir, "scratch"),
    "--cache-dir",
    path.join(dir, "cache"),
  ];
  return { out, common };
}

describe.skipIf(omp === undefined)("skill-cuts load on the pinned Oh My Pi", () => {
  test(
    "captures the first request of a one-message history, lists the renamed skill under its hint, and counts a read of skill://<name> as a load",
    async () => {
      const dir = path.join(scratch, "omp");
      const { out, common } = hostInputs(dir);
      const models = path.join(dir, "models.yml");
      writeFileSync(
        models,
        "providers:\n  anthropic:\n    baseUrl: http://127.0.0.1:9\n    api: anthropic-messages\n    compat: {replayUnsignedThinking: false, supportsLongCacheRetention: true}\n    models:\n      - {id: claude-sonnet-5-5, name: Claude Sonnet 5.5, reasoning: true, input: [text, image], contextWindow: 1000000, maxTokens: 128000, cost: {input: 2, output: 10, cacheRead: 0.1, cacheWrite: 2.5}}\n"
      );
      const profile = path.join(dir, "profile.json");
      writeFileSync(profile, JSON.stringify(BETA_PROFILE));
      answer = () => [
        { type: "tool_use", id: "toolu_omega", name: "read", input: { path: "skill://omega" } },
      ];
      const ran = await cli([
        "load",
        ...common,
        "--name",
        "omega",
        "--host",
        "omp",
        "--profile",
        profile,
        "--omp",
        String(omp),
        "--models",
        models,
      ]);
      expect({ code: ran.code, stderr: ran.stderr }).toEqual({ code: 0, stderr: "" });
      expect(ran.stdout).toContain(
        "alpha is tested under the name omega: its frontmatter name and its directory's name are changed"
      );
      const body = JSON.parse(readFileSync(loadCapturePaths(out, "omp", "need-1").body, "utf8"));
      const listing = skillListing(body);
      expect([...listing.keys()].sort()).toEqual(["beta", "omega"]);
      expect(listing.get("omega")).toBe("Use for the omega work, stand-in hint.");
      expect(texts(body.messages).some((text) => text.includes(NEED))).toBe(true);
      expect(texts(body).some((text) => text.includes("placeholder prompt"))).toBe(false);
      expect(savedRuns(out, "need-1")).toMatchObject([
        {
          loaded: true,
          loadedBy: "read skill://omega",
          name: "omega",
          listed: "Use for the omega work, stand-in hint.",
        },
      ]);
      expect(ran.stdout).toContain(
        "no verdict: R6's rule is of 3 runs a task, and this test runs 1"
      );
    },
    HOST_ROW_TIMEOUT_MS
  );
});

describe.skipIf(claude === undefined)("skill-cuts load on Claude Code", () => {
  test(
    "installs the profile and the tested skill as local plugins, captures the first request with its listing and headers, and counts a Skill call as a load",
    async () => {
      const dir = path.join(scratch, "claude");
      const { out, common } = hostInputs(dir);
      const profile = path.join(dir, "profile.json");
      writeFileSync(
        profile,
        JSON.stringify({
          ...BETA_PROFILE,
          claude: {
            marketplace: "acme-market",
            plugins: [{ name: "acme-tools", skills: ["beta"] }],
            skillPlugin: "acme-tools",
          },
        })
      );
      answer = () => [
        {
          type: "tool_use",
          id: "toolu_skill",
          name: "Skill",
          input: { skill: "acme-tools:alpha" },
        },
      ];
      const ran = await cli([
        "load",
        ...common,
        "--host",
        "claude",
        "--profile",
        profile,
        "--thinking",
        "off",
        "--claude",
        String(claude),
      ]);
      expect({ code: ran.code, stderr: ran.stderr }).toEqual({ code: 0, stderr: "" });
      const files = loadCapturePaths(out, "claude", "need-1");
      const body = JSON.parse(readFileSync(files.body, "utf8"));
      const listing = claudeListing(body);
      expect(listing.get("acme-tools:alpha")).toBe(
        "Use when a task asks for the alpha checklist of the made-up project."
      );
      expect(listing.has("acme-tools:beta")).toBe(true);
      expect(texts(body.messages).some((text) => text.includes(NEED))).toBe(true);
      // Thinking off: the body asks for none.
      expect(body.thinking).toBeUndefined();
      const record = z
        .looseObject({ requestHeaders: z.record(z.string(), z.string()) })
        .parse(JSON.parse(readFileSync(files.record, "utf8")));
      expect(record.requestHeaders["anthropic-beta"]).toBeString();
      expect(
        Object.keys(record.requestHeaders).every((name) => name.startsWith("anthropic-"))
      ).toBe(true);
      expect(savedRuns(out, "need-1")).toMatchObject([
        { loaded: true, loadedBy: "Skill acme-tools:alpha", toolCalls: ["Skill"] },
      ]);
      expect(ran.stdout).toContain(
        'Claude Code lists acme-tools:alpha as "Use when a task asks for the alpha checklist of the made-up project."'
      );
    },
    HOST_ROW_TIMEOUT_MS
  );
});
