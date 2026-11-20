import { Database } from "bun:sqlite";
import { describe, expect, test } from "bun:test";
import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { linkOmpNatives } from "@legion/pi-shared/test/omp-natives";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { SessionEntry, type SessionMessage, stripSkillHeader } from "../skill-scenarios/transcript";
import { type Arm, type ArmName, buildArm } from "./arms";
import {
  armPromptBlocks,
  type Capture,
  type CaptureOptions,
  captureBody,
  type HintWriter,
  promptDigest,
  skillListing,
} from "./capture";
import { type Case, pathHash } from "./cases";
import { localSource, skillFrontmatter } from "./context";
import { END_TURN_REPLY, messageSse, sseResponse, startRecorder } from "./recorder";
import { BLOCK_REASON, MESSAGES_VARIABLE } from "./replay-hook";

// The capture on the pinned Oh My Pi (plan, R5 `capture.ts`), against the recorder, with made-up
// skills and repositories: `alpha` and `beta` of a made-up `acme/skills`, read from local
// directories in place of `gh`, and the messages of F1 (testdata/F1, an Oh My Pi 18.18.6 session
// that reads skill://alpha and skill://alpha/references/x.md, captured by plan task t1 with
// `HOME=<scratch> omp -p --session-dir <scratch>/sessions --model anthropic/claude-sonnet-5-5
// "<prompt>"`, testdata/CAPTURE.md). Hint requests go to a stand-in that writes a new hint on each
// call, so a row can tell a hint the cache gave from one written again. LEGION_TEST_OMP names the
// binary, as in this package's other real-binary tests: a run without it skips, except on GitHub
// Actions, where the pi-legion job installs the pin (GITHUB_ACTIONS, not CI: agent harnesses on
// the devbox export CI=true).
const omp = process.env.LEGION_TEST_OMP;
const onActions = process.env.GITHUB_ACTIONS === "true";
const skip = omp === undefined && !onActions;
if (skip) {
  console.log(
    "capture.test.ts: LEGION_TEST_OMP is unset, so its rows are skipped; set it to the pinned Oh My Pi binary to run them"
  );
}

/** The binary the rows run; throws on GitHub Actions without it. */
function binary(): string {
  if (omp === undefined) throw new Error("LEGION_TEST_OMP is unset on GitHub Actions");
  return omp;
}

/** A row's own timeout: up to seven Oh My Pi runs of about ten seconds each in a slow pod. */
const ROW_TIMEOUT_MS = 600_000;

const F1 = path.join(
  import.meta.dir,
  "testdata",
  "F1",
  "2026-11-20T01-59-41-780Z_01a1f6ae-51d4-715f-a2a6-59f275bdff80.jsonl"
);
const messages: SessionMessage[] = readFileSync(F1, "utf8")
  .split("\n")
  .filter((line) => line.trim() !== "")
  .map((line) => SessionEntry.parse(JSON.parse(line)))
  .flatMap((entry) => (entry.type === "message" && entry.message ? [entry.message] : []));
/** The session_init line of F1's subagent ProductiveChicken: the system prompt its session
 *  recorded (its skill listing names alpha and beta) and its tool names. */
const SUBAGENT_INIT = readFileSync(
  path.join(F1.replace(/\.jsonl$/, ""), "ProductiveChicken.jsonl"),
  "utf8"
)
  .split("\n")
  .filter((line) => line.trim() !== "")
  .map((line) => JSON.parse(line) as { type: string; systemPrompt?: string[]; tools?: string[] })
  .find((entry) => entry.type === "session_init");
const SUBAGENT_PROMPT = SUBAGENT_INIT?.systemPrompt ?? [];
const SUBAGENT_TOOLS = SUBAGENT_INIT?.tools ?? [];
/** The file F1's read result at `index` read, as it was on disk. */
function f1File(index: number): string {
  return `${stripSkillHeader(messages[index]?.content?.[0]?.text ?? "")}\n`;
}
const OLD_SKILL = f1File(2);
const X = f1File(4);
/** The new text: alpha's description and its last line, each with words added. */
const NEW_SKILL = OLD_SKILL.replace(
  /^description: (.*)$/m,
  "description: $1 It names the new text."
)
  .trimEnd()
  .concat(" Then stop.\n");
const BETA_SKILL =
  "---\nname: beta\ndescription: Use when a task asks for the beta greeting of the made-up project.\n---\n\n# Beta\n\nGreet with one line.\n";
/** A skill Oh My Pi installs and never lists, as two of cybermonklabs/legion's skills are. */
const DELTA_SKILL =
  "---\nname: delta\ndescription: Use when asked for the delta audit.\ndisable-model-invocation: true\n---\n\n# Delta\n";
const COMMIT = "0123456789abcdef0123456789abcdef01234567";
const MODELS = `providers:
  anthropic:
    baseUrl: http://127.0.0.1:9
    api: anthropic-messages
    compat: {replayUnsignedThinking: false, supportsLongCacheRetention: true}
    models:
      - {id: claude-sonnet-5-5, name: Claude Sonnet 5.5, reasoning: true, input: [text, image], contextWindow: 1000000, maxTokens: 128000, cost: {input: 2, output: 10, cacheRead: 0.1, cacheWrite: 2.5}}
`;

const dir = temporaryDirectory("skill-cuts-capture", { real: true });

/** Writes `files` (path to text) under `root`. */
function writeTree(root: string, files: Record<string, string>): void {
  for (const [file, text] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(root, file)), { recursive: true });
    writeFileSync(path.join(root, file), text);
  }
}

/** F1's `task` step as a case of alpha, with id `id`. */
function f1Case(id: string): Case {
  const lines = OLD_SKILL.trimEnd().split("\n");
  return {
    id,
    cut: "acme-skills-1",
    skill: "alpha",
    baseFile: "SKILL.md",
    line: { text: lines.at(-1) ?? "", lineNumber: lines.length, lineCount: lines.length },
    criterion: { pass: "The reply does the step.", fail: "The reply does not do the step." },
    model: "anthropic/claude-sonnet-5-5",
    thinking: "off",
    context: "devbox",
    profile: {
      skills: [
        { repo: "acme/skills", commit: COMMIT, path: "skills/alpha", name: "alpha" },
        { repo: "acme/skills", commit: COMMIT, path: "skills/beta", name: "beta" },
        { repo: "acme/skills", commit: COMMIT, path: "skills/delta", name: "delta" },
      ],
      plugins: [],
      contextFiles: [{ repo: "acme/project", commit: COMMIT, path: "AGENTS.md" }],
    },
    messages: messages.slice(0, 7),
    skillReads: [2, 4],
    realStep: messages[7] ?? {},
    violatingStep: { role: "assistant", content: [{ type: "text", text: "Step 4 is done." }] },
    source: { store: "store_a", pathHash: pathHash("/sessions/alpha.jsonl") },
    sessionStart: "2026-11-20T01:59:41.780Z",
    secretsRemoved: 0,
    sessionDigest: "46874d907e557998",
  };
}

/** The skill each hint request names, in the order the stand-in got them. */
const hintRequests: string[] = [];
/** The `anthropic-version` header each hint request came with. */
const hintVersions: (string | undefined)[] = [];
/** A stand-in hint writer: a new hint on each call, naming the skill and the call. */
const hints: HintWriter = async (body, headers) => {
  const skill = /Skill: ([^\\]+)\\nDescription: /.exec(JSON.stringify(body.messages))?.[1] ?? "";
  hintRequests.push(skill);
  hintVersions.push(headers["anthropic-version"]);
  return {
    text: `Use for the ${skill} work, stand-in hint ${hintRequests.length}.`,
    model: body.model,
  };
};

/** Every string inside `value`. */
function texts(value: unknown): string[] {
  if (typeof value === "string") return [value];
  if (Array.isArray(value)) return value.flatMap(texts);
  if (typeof value === "object" && value !== null) return Object.values(value).flatMap(texts);
  return [];
}

/** Every `tool_use` block inside `value`. */
function toolUses(value: unknown): unknown[] {
  if (Array.isArray(value)) return value.flatMap(toolUses);
  if (typeof value !== "object" || value === null) return [];
  if (Reflect.get(value, "type") === "tool_use") return [value];
  return Object.values(value).flatMap(toolUses);
}

/** The hints a capture's scratch profile holds, by Oh My Pi's key, read before the next capture
 *  of the case writes that profile again. */
function profileHints(capture: Capture): Map<string, string> {
  const file = path.join(
    capture.record.scratch,
    "home/.omp/profiles/capture/agent/skill-descriptions.db"
  );
  const db = new Database(file);
  try {
    const rows = db.query("SELECT key, description FROM skill_descriptions").all() as {
      key: string;
      description: string;
    }[];
    return new Map(rows.map((row) => [row.key, row.description]));
  } finally {
    db.close();
  }
}

/** A capture and the hints its scratch profile held once it was made. */
interface Captured {
  readonly capture: Capture;
  readonly profile: Map<string, string>;
}

/** The hint `captured`'s profile held for `skill`. */
function profileHint({ capture, profile }: Captured, skill: string): string | undefined {
  return profile.get(capture.record.hints.find((hint) => hint.skill === skill)?.ompKey ?? "");
}

interface Captures {
  readonly arms: Record<"old" | "new" | "none", Arm>;
  readonly old: Captured;
  readonly fresh: Captured;
  readonly none: Captured;
  readonly again: Captured;
  readonly hintsAfterFirst: number;
  readonly hintsBeforeAgain: number;
}
let captured: Promise<Captures> | undefined;

/** The captures the rows read, made once: the old, new and none arms of case c1, then the old arm
 *  of case c2, which has the same texts. */
function captures(): Promise<Captures> {
  captured ??= (async () => {
    const repos = path.join(dir, "repos");
    writeTree(path.join(repos, "acme/skills", COMMIT), {
      "skills/alpha/SKILL.md": OLD_SKILL,
      "skills/alpha/references/x.md": X,
      "skills/beta/SKILL.md": BETA_SKILL,
      "skills/delta/SKILL.md": DELTA_SKILL,
    });
    writeTree(path.join(repos, "acme/project", COMMIT), {
      "AGENTS.md": "# Made-up project\n\nKeep each answer short.\n",
    });
    writeTree(path.join(dir, "old"), { "SKILL.md": OLD_SKILL, "references/x.md": X });
    writeTree(path.join(dir, "new"), { "SKILL.md": NEW_SKILL, "references/x.md": X });
    writeFileSync(path.join(dir, "models.yml"), MODELS);
    const options: CaptureOptions = {
      omp: binary(),
      outDir: path.join(dir, "out"),
      scratchDir: path.join(dir, "scratch"),
      cacheDir: path.join(dir, "cache"),
      source: localSource(repos),
      models: path.join(dir, "models.yml"),
      hints,
    };
    const dirs = { old: path.join(dir, "old"), new: path.join(dir, "new") };
    const armOf = (c: Case, name: ArmName) => buildArm(c, name, dirs, path.join(dir, "arms"));
    const capture = async (c: Case, arm: Arm): Promise<Captured> => {
      const made = await captureBody(c, arm, options);
      return { capture: made, profile: profileHints(made) };
    };
    const c1 = f1Case("c1");
    const arms = { old: armOf(c1, "old"), new: armOf(c1, "new"), none: armOf(c1, "none") };
    const old = await capture(c1, arms.old);
    const hintsAfterFirst = hintRequests.length;
    const fresh = await capture(c1, arms.new);
    const none = await capture(c1, arms.none);
    const hintsBeforeAgain = hintRequests.length;
    const again = await capture(f1Case("c2"), armOf(f1Case("c2"), "old"));
    return { arms, old, fresh, none, again, hintsAfterFirst, hintsBeforeAgain };
  })();
  return captured;
}

/** `blocks` less every line of the skill listing that names `skill`. */
function withoutListingLine(blocks: readonly string[], skill: string): string[] {
  return blocks.map((block) =>
    block
      .split("\n")
      .filter((line) => !line.startsWith(`- ${skill}: `))
      .join("\n")
  );
}

test("an arm's recorded prompt is the recording with only the skill's listing line changed: under the arm's hint, or left out", () => {
  expect(SUBAGENT_PROMPT.length).toBeGreaterThan(1);
  const recordedLine = skillListing({
    system: SUBAGENT_PROMPT.map((text) => ({ type: "text", text })),
  }).get("alpha");
  expect(recordedLine).toBeDefined();
  const replaced = armPromptBlocks(SUBAGENT_PROMPT, "alpha", "Use for the arm's alpha.");
  expect(replaced.line).toBe("replaced");
  expect(skillListing({ system: replaced.blocks.map((text) => ({ type: "text", text })) })).toEqual(
    new Map([
      ["alpha", "Use for the arm's alpha."],
      [
        "beta",
        skillListing({ system: SUBAGENT_PROMPT.map((text) => ({ type: "text", text })) }).get(
          "beta"
        ) ?? "",
      ],
    ])
  );
  const removed = armPromptBlocks(SUBAGENT_PROMPT, "alpha", undefined);
  expect(removed.line).toBe("removed");
  for (const each of [replaced, removed]) {
    expect(withoutListingLine(each.blocks, "alpha")).toEqual(
      withoutListingLine(SUBAGENT_PROMPT, "alpha")
    );
  }
  expect(removed.blocks.join("").length).toBe(
    SUBAGENT_PROMPT.join("").length - `- alpha: ${recordedLine}\n`.length
  );
  expect(armPromptBlocks(SUBAGENT_PROMPT, "gamma", "Use for gamma.")).toEqual({
    blocks: SUBAGENT_PROMPT,
    line: "not listed",
  });
});

/** The texts of a body's `system` blocks, in order. */
function systemBlocks(body: { readonly [field: string]: unknown }): string[] {
  return Array.isArray(body.system)
    ? body.system.map((block: unknown) =>
        typeof block === "object" && block !== null ? String(Reflect.get(block, "text")) : ""
      )
    : [];
}

describe.skipIf(skip)("capture on the pinned Oh My Pi", () => {
  test(
    "the recorded body lists each installed skill under the hint the profile's cache holds, never its description",
    async () => {
      const { old, fresh } = await captures();
      for (const each of [old, fresh]) {
        const listing = skillListing(each.capture.body);
        expect([...listing.keys()].sort()).toEqual(["alpha", "beta"]);
        for (const skill of ["alpha", "beta"]) {
          const hint = profileHint(each, skill);
          expect(hint).toStartWith(`Use for the ${skill} work, stand-in hint `);
          expect(listing.get(skill)).toBe(hint as string);
        }
        expect(each.capture.record.listing.map((entry) => entry.hint)).toEqual([
          ...listing.values(),
        ]);
      }
      const oldText = skillFrontmatter(path.join(dir, "old")).description;
      const newText = skillFrontmatter(path.join(dir, "new")).description;
      expect(newText).not.toBe(oldText);
      const oldBody = old.capture.body;
      const freshBody = fresh.capture.body;
      expect(skillListing(oldBody).get("alpha")).not.toContain(oldText.slice(0, 30));
      // The new text gets a hint of its own, written for it; beta's comes from the cache.
      expect(skillListing(freshBody).get("alpha")).not.toBe(skillListing(oldBody).get("alpha"));
      expect(skillListing(freshBody).get("beta")).toBe(skillListing(oldBody).get("beta"));
      expect(old.capture.record.hintCalls.map((call) => call.skill).sort()).toEqual([
        "alpha",
        "beta",
      ]);
      expect(old.capture.record.runs).toBe(2);
      // delta is installed and hidden, so it is neither listed nor given a hint.
      expect(old.capture.record.context.skills.map((skill) => skill.name)).toEqual([
        "alpha",
        "beta",
        "delta",
      ]);
      expect(old.capture.record.hints.map((hint) => hint.skill)).toEqual(["alpha", "beta"]);
      expect(fresh.capture.record.hintCalls.map((call) => call.skill)).toEqual(["alpha"]);
    },
    ROW_TIMEOUT_MS
  );

  test(
    "the none arm's body lists none of the skill and lists exactly the profile's other skills",
    async () => {
      const { none, arms } = await captures();
      const body = none.capture.body;
      expect([...skillListing(body).keys()]).toEqual(["beta"]);
      expect(arms.none.profileSkills.map((skill) => skill.name)).toEqual(["beta", "delta"]);
      // No call reads the skill: the user's prompt still names it, as the session's did.
      const calls = toolUses(body.messages);
      expect(calls.length).toBeGreaterThan(0);
      expect(calls.filter((call) => JSON.stringify(call).includes("skill://alpha"))).toEqual([]);
      expect(none.capture.record.hintCalls).toEqual([]);
      expect(none.capture.record.runs).toBe(1);
    },
    ROW_TIMEOUT_MS
  );

  test(
    "the body's messages are the arm's, and the placeholder prompt is in none of it",
    async () => {
      const { old, fresh, arms } = await captures();
      for (const [each, arm] of [
        [old, arms.old],
        [fresh, arms.new],
      ] as const) {
        const sent = texts(each.capture.body.messages);
        for (const index of [0, 2, 4]) {
          const text = arm.messages[index]?.content?.[0]?.text ?? "";
          expect(text).not.toBe("");
          expect(sent.some((one) => one.includes(text))).toBe(true);
        }
        expect(texts(each.capture.body).some((text) => text.includes("placeholder prompt"))).toBe(
          false
        );
      }
      const named = (each: Captured) =>
        texts(each.capture.body.messages).some((text) => text.includes("It names the new text."));
      expect(named(fresh)).toBe(true);
      expect(named(old)).toBe(false);
      // The context file reached the system prompt, and both digests are recorded.
      const body = old.capture.body;
      expect(texts(body.system).some((text) => text.includes("Keep each answer short."))).toBe(
        true
      );
      expect(old.capture.record.sessionDigest).toBe("46874d907e557998");
      expect(old.capture.record.captureDigest).toBe(promptDigest(body));
      // The turn's and the hints' `anthropic-*` headers go with them, for a replay to send.
      expect(old.capture.record.requestHeaders["anthropic-version"]).toBe("2023-06-01");
      expect(
        Object.keys(old.capture.record.requestHeaders).every((name) =>
          name.startsWith("anthropic-")
        )
      ).toBe(true);
      expect(hintVersions.length).toBeGreaterThan(0);
      expect(hintVersions.every((version) => version === "2023-06-01")).toBe(true);
    },
    ROW_TIMEOUT_MS
  );

  test(
    "another capture of one arm text writes no hint and lists the same one, and a saved capture is read back",
    async () => {
      const { old, again, hintsAfterFirst, hintsBeforeAgain } = await captures();
      expect(hintsAfterFirst).toBe(2);
      expect(hintRequests.length).toBe(hintsBeforeAgain);
      expect(again.capture.record.runs).toBe(1);
      expect(skillListing(again.capture.body)).toEqual(skillListing(old.capture.body));
      expect(again.capture.record.hints.every((hint) => !hint.written)).toBe(true);
      expect(profileHint(again, "alpha")).toBe(profileHint(old, "alpha") as string);
      const c1 = f1Case("c1");
      const read = await captureBody(
        c1,
        buildArm(c1, "old", { old: path.join(dir, "old"), new: path.join(dir, "new") }, dir),
        {
          omp: binary(),
          outDir: path.join(dir, "out"),
          scratchDir: path.join(dir, "scratch"),
          cacheDir: path.join(dir, "cache"),
          source: localSource(path.join(dir, "repos")),
          models: path.join(dir, "models.yml"),
          hints,
        }
      );
      expect(read.record).toEqual(old.capture.record);
      expect(hintRequests.length).toBe(hintsBeforeAgain);
    },
    ROW_TIMEOUT_MS
  );

  test(
    "the hook gives the model the arm's messages and blocks every tool call, and the capture's digest is the one Oh My Pi records",
    async () => {
      const root = path.join(dir, "hook");
      const home = path.join(root, "home");
      const agentDir = path.join(home, ".omp/profiles/hook/agent");
      const sessions = path.join(root, "sessions");
      const marker = path.join(root, "marker-the-block-must-stop");
      for (const each of [agentDir, sessions, path.join(root, "work")]) {
        mkdirSync(each, { recursive: true });
      }
      await linkOmpNatives(binary(), home);
      const bodies: string[] = [];
      const recorder = startRecorder({
        route: (request) => {
          if (request.method !== "POST" || request.path !== "/v1/messages") return undefined;
          bodies.push(request.body);
          if (bodies.length > 1) return sseResponse(END_TURN_REPLY);
          return sseResponse(
            messageSse({
              id: "msg_hook_1",
              model: "claude-sonnet-5-5",
              content: [
                {
                  type: "tool_use",
                  id: "toolu_hook_1",
                  name: "bash",
                  input: { command: `touch ${marker}` },
                },
              ],
              stopReason: "tool_use",
            })
          );
        },
      });
      try {
        // The scratch profile's provider, as a capture writes it: the recorder, a placeholder key.
        writeFileSync(
          path.join(agentDir, "models.yml"),
          MODELS.replace("http://127.0.0.1:9", recorder.url).replace(
            "    api: anthropic-messages\n",
            "    api: anthropic-messages\n    auth: apiKey\n    apiKey: placeholder-the-recorder-holds-no-key\n"
          )
        );
        writeFileSync(
          path.join(agentDir, "config.yml"),
          "modelRoles:\n  default: anthropic/claude-sonnet-5-5\n  smol: anthropic/claude-sonnet-5-5\n"
        );
        const messagesFile = path.join(root, "messages.json");
        writeFileSync(messagesFile, JSON.stringify(messages.slice(0, 3)));
        const child = Bun.spawn(
          [
            binary(),
            "--profile",
            "hook",
            "-p",
            "--mode",
            "json",
            "--session-dir",
            sessions,
            "--model",
            "anthropic/claude-sonnet-5-5",
            "--thinking",
            "off",
            "--hook",
            path.join(import.meta.dir, "replay-hook.ts"),
            "placeholder prompt",
          ],
          {
            cwd: path.join(root, "work"),
            env: {
              HOME: home,
              PATH: "/usr/local/bin:/usr/bin:/bin",
              TMPDIR: root,
              PI_NO_TITLE: "1",
              [MESSAGES_VARIABLE]: messagesFile,
            },
            stdout: "pipe",
            stderr: "pipe",
          }
        );
        const [stdout, stderr, code] = await Promise.all([
          new Response(child.stdout).text(),
          new Response(child.stderr).text(),
          child.exited,
        ]);
        expect({ code, stderr: code === 0 ? "" : stderr }).toEqual({ code: 0, stderr: "" });
        // The blocked call's result, as Oh My Pi's own event stream records it.
        expect(stdout).toContain(BLOCK_REASON);
      } finally {
        await recorder.close();
      }
      expect(existsSync(marker)).toBe(false);
      expect(bodies.length).toBe(2);
      const [first, second] = bodies.map((body) => JSON.parse(body));
      // The hook gives each request exactly the arm's messages, the run's own dropped.
      expect(second.messages).toEqual(first.messages);
      const sent = texts(first.messages);
      for (const index of [0, 2]) {
        const text = messages[index]?.content?.[0]?.text ?? "";
        expect(sent.some((each) => each.includes(text))).toBe(true);
      }
      expect(texts(second.messages).some((text) => text.includes(BLOCK_REASON))).toBe(false);
      const transcript = readdirSync(sessions, { recursive: true })
        .map(String)
        .filter((file) => file.endsWith(".jsonl"));
      expect(transcript.length).toBe(1);
      const digests = readFileSync(path.join(sessions, transcript[0] as string), "utf8")
        .split("\n")
        .filter((line) => line.trim() !== "")
        .map((line) => JSON.parse(line))
        .filter((entry) => entry.type === "custom" && entry.customType === "system-prompt-digest")
        .map((entry) => entry.data);
      expect(digests.length).toBeGreaterThan(0);
      expect(digests[0]).toBe(promptDigest(first));
    },
    ROW_TIMEOUT_MS
  );

  test(
    "a Legion-context capture defines the Legion and Envoy plugins' tools, and every endpoint it reached was a listener of its own on 127.0.0.1",
    async () => {
      // The two plugins as the worker image ships them: each one's bundle, built from this
      // checkout into a made-up repository at a made-up commit, its manifest naming the bundle.
      const repo = path.join(dir, "repos/acme/legion", COMMIT);
      const packages = path.resolve(import.meta.dir, "../../..");
      const plugins = [
        {
          name: "pi-envoy",
          entry: "envoy",
          externals: ["pi-ai", "pi-coding-agent", "pi-tui", "pi-utils"],
        },
        {
          name: "pi-legion",
          entry: "legion",
          externals: ["pi-coding-agent", "pi-tui", "pi-utils"],
        },
      ];
      for (const plugin of plugins) {
        const dist = path.join(repo, "packages", plugin.name, "dist");
        const built = Bun.spawnSync(
          [
            "bun",
            "build",
            `extensions/${plugin.entry}.ts`,
            "--outdir",
            dist,
            "--target",
            "bun",
            "--format",
            "esm",
            ...plugin.externals.flatMap((name) => ["--external", `@oh-my-pi/${name}`]),
          ],
          { cwd: path.join(packages, plugin.name), stderr: "pipe" }
        );
        expect({
          code: built.exitCode,
          stderr: built.exitCode === 0 ? "" : built.stderr.toString(),
        }).toEqual({ code: 0, stderr: "" });
        writeTree(path.join(repo, "packages", plugin.name), {
          "package.json": JSON.stringify({
            name: `@acme/${plugin.name}`,
            files: ["dist"],
            omp: { extensions: [`dist/${plugin.entry}.js`], skills: ["dist/skills"] },
          }),
        });
      }
      // The Envoy plugin reads the dispatch-first skill from its skills at load.
      writeTree(path.join(dir, "repos/acme/skills", COMMIT), {
        "skills/dispatch-first/SKILL.md": readFileSync(
          path.resolve(packages, "../skills/dispatch-first/SKILL.md"),
          "utf8"
        ),
      });
      await captures();
      const base = f1Case("legion-1");
      const c: Case = {
        ...base,
        context: "legion:acme:implementer",
        profile: {
          ...base.profile,
          skills: [
            ...base.profile.skills,
            {
              repo: "acme/skills",
              commit: COMMIT,
              path: "skills/dispatch-first",
              name: "dispatch-first",
            },
          ],
          plugins: plugins.map((plugin) => ({
            repo: "acme/legion",
            commit: COMMIT,
            path: `packages/${plugin.name}`,
            name: plugin.name,
          })),
          appendSystemPrompt: "You are a made-up role of a made-up project.",
        },
      };
      const capture = await captureBody(
        c,
        buildArm(c, "old", { old: path.join(dir, "old"), new: path.join(dir, "new") }, dir),
        {
          omp: binary(),
          outDir: path.join(dir, "out"),
          scratchDir: path.join(dir, "scratch"),
          cacheDir: path.join(dir, "cache"),
          source: localSource(path.join(dir, "repos")),
          models: path.join(dir, "models.yml"),
          hints,
        }
      );
      const { record, body } = capture;
      expect(record.context.plugins.map((plugin) => plugin.as)).toEqual(["extension", "extension"]);
      expect(record.pluginTools.tools).toContain("legion");
      expect(record.pluginTools.legion).toBe(true);
      expect(record.pluginTools.envoy).toContain("envoy_send");
      expect(record.pluginTools.devices).toEqual(expect.arrayContaining(record.pluginTools.envoy));
      expect(record.pluginTools.leftOut).toEqual([]);
      // Every endpoint the pane was given is a listener of the capture's own on 127.0.0.1, and
      // the stand-in daemon took the registration the tool waits for.
      expect(record.legion?.role).toBe("implementer");
      for (const endpoint of Object.values(record.legion?.endpoints ?? {})) {
        expect(endpoint).toMatch(/^(http|nats):\/\/127\.0\.0\.1:\d+(\/|$)/);
      }
      expect(record.connections.http).toContain("POST /legion/v1/claims/register");
      expect(record.connections.http).toContain("POST /v1/messages");
      // Nothing the run added reaches the model: no endpoint's failure, no placeholder.
      expect(record.hookDropped[0]).toBeGreaterThan(0);
      expect(texts(body.messages).some((text) => text.includes("127.0.0.1"))).toBe(false);
      expect(texts(body).some((text) => text.includes("placeholder prompt"))).toBe(false);
      expect(record.exit.code === 0 || record.exit.reason !== "").toBe(true);
    },
    ROW_TIMEOUT_MS
  );

  test(
    "a subagent case's capture sends its recorded system prompt byte for byte but for the skill's listing line, per arm, and only its recorded tools",
    async () => {
      await captures();
      const base = f1Case("subagent-1");
      const c: Case = {
        ...base,
        context: "subagent:task",
        profile: {
          ...base.profile,
          contextFiles: [],
          systemPrompt: SUBAGENT_PROMPT,
          tools: SUBAGENT_TOOLS,
        },
      };
      const dirs = { old: path.join(dir, "old"), new: path.join(dir, "new") };
      for (const name of ["old", "new", "none"] as const) {
        const { body, record } = await captureBody(
          c,
          buildArm(c, name, dirs, path.join(dir, "arms-subagent")),
          {
            omp: binary(),
            outDir: path.join(dir, "out"),
            scratchDir: path.join(dir, "scratch"),
            cacheDir: path.join(dir, "cache"),
            source: localSource(path.join(dir, "repos")),
            models: path.join(dir, "models.yml"),
            hints,
          }
        );
        const sent = systemBlocks(body);
        expect(sent).toHaveLength(SUBAGENT_PROMPT.length);
        // Every byte the recording holds but the skill's line of its listing.
        expect(withoutListingLine(sent, "alpha")).toEqual(
          withoutListingLine(SUBAGENT_PROMPT, "alpha")
        );
        const hint = record.hints.find((each) => each.skill === "alpha")?.hint;
        const listing = skillListing(body);
        if (name === "none") {
          expect(listing.has("alpha")).toBe(false);
          expect(record.recordedPrompt?.line).toBe("removed");
        } else {
          expect(hint).toStartWith("Use for the alpha work, stand-in hint ");
          expect(listing.get("alpha")).toBe(hint as string);
          expect(record.recordedPrompt?.line).toBe("replaced");
        }
        expect(sent).toEqual(
          armPromptBlocks(SUBAGENT_PROMPT, "alpha", name === "none" ? undefined : hint).blocks
        );
        expect(record.recordedPrompt?.blocks).toBe(SUBAGENT_PROMPT.length);
        expect(record.tools).toEqual({
          recorded: SUBAGENT_TOOLS,
          sent: SUBAGENT_TOOLS,
          missing: [],
          extra: [],
        });
      }
    },
    ROW_TIMEOUT_MS
  );
});
