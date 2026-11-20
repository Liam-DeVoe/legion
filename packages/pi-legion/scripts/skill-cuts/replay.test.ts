import { expect, test } from "bun:test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import type { Arm } from "./arms";
import { createGateway, type Gateway, type MessagesBody, type Reply } from "./gateway";
import {
  MAX_BREAKPOINTS,
  MAX_SKILL_ROUNDS,
  replay,
  replayOnce,
  withCacheBreakpoint,
} from "./replay";

// The replay of an arm, on F5 (testdata/): the model gateway's replies to request bodies made from
// F2, captured by plan task t1 (testdata/CAPTURE.md, "F5") with
// `bun $TMPDIR/infra4676/send.ts <body.json> > F5-<shape>.sse`. F5-skill-read's only tool call
// reads skill://alpha/references/x.md; F5-skill-read-and-other holds a read of skill://beta, a
// `bash` call and a read of skill://alpha/references/x.md; F5-text holds text only. The gateway's
// fetch is a fake that answers with them: no test reaches a network.

const TESTDATA = path.join(import.meta.dir, "testdata");
const scratch = temporaryDirectory("skill-cuts-replay", { real: true });

/** The body the arm's capture recorded: F2, a replayed history Oh My Pi 18.18.6 sent. */
const BODY = JSON.parse(
  readFileSync(path.join(TESTDATA, "F2-request.json"), "utf8")
) as MessagesBody;

const SKILL_MD =
  "---\nname: alpha\ndescription: Use when a task asks for the alpha checklist of the made-up fixture project; it lists the three alpha steps.\n---\n\n# Alpha\n\n1. Read `references/x.md` before the first step.\n2. Write each step's result on one line.\n3. Stop after the third step.\n";
const X_MD = "# X\n\nEach alpha step's result is one short line.\n";

const PROFILE_SKILLS = ["alpha", "beta"].map((name) => ({
  repo: "acme/skills",
  commit: "0123456789abcdef0123456789abcdef01234567",
  path: `skills/${name}`,
  name,
}));

/** The skill's directory as the model sees it, the same in every arm. */
const INSTALLED = "/home/agent/.omp/agent/skills/alpha";

/** An arm of skill alpha whose directory holds `files`. */
function armWith(name: "old" | "new" | "minus", files: Record<string, string>): Arm {
  const skillDir = path.join(scratch, `${name}-${Object.keys(files).length}-${Math.random()}`);
  for (const [file, text] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(skillDir, file)), { recursive: true });
    writeFileSync(path.join(skillDir, file), text);
  }
  return {
    name,
    skill: "alpha",
    skillDir,
    installedDir: INSTALLED,
    messages: [],
    profileSkills: PROFILE_SKILLS,
  };
}

/** F5's reply `shape` as the gateway sent it. */
function f5(shape: string): Response {
  const kept = JSON.parse(readFileSync(path.join(TESTDATA, `F5-${shape}.headers.json`), "utf8"));
  return new Response(readFileSync(path.join(TESTDATA, `F5-${shape}.sse`), "utf8"), {
    status: kept.status,
    headers: kept.headers,
  });
}

/** A gateway whose fake fetch answers the n-th call (from 1) with `answer`, and the bodies and
 *  headers sent. */
function world(answer: (n: number) => Response) {
  const bodies: MessagesBody[] = [];
  const headers: Headers[] = [];
  const keyFile = path.join(scratch, `key-${Math.random()}`);
  writeFileSync(keyFile, fixtureSecret("gw"), { mode: 0o600 });
  const gateway = createGateway({
    baseUrl: "http://gateway.test",
    keyFile,
    concurrency: 2,
    timeoutMs: 5_000,
    fetch: async (_url, init) => {
      bodies.push(JSON.parse(String(init.body)));
      headers.push(new Headers(init.headers));
      return answer(bodies.length);
    },
    sleep: async () => {},
  });
  return { gateway, bodies, headers };
}

/** The `anthropic-*` headers Oh My Pi 18.18.6 sent beside a body at `--thinking low` (F2), as a
 *  capture keeps them. */
const CAPTURED_HEADERS: Record<string, string> = Object.fromEntries(
  Object.entries(
    JSON.parse(readFileSync(path.join(TESTDATA, "F2-request-thinking-low.headers.json"), "utf8"))
      .headers
  ).filter((entry): entry is [string, string] => entry[0].startsWith("anthropic-"))
);

/** F5-skill-read's one tool call. */
const SKILL_READ_CALL = {
  type: "tool_use",
  id: "toolu_01WDxLVjvqwiht3W6X8n4wAn",
  name: "read",
  input: { path: "skill://alpha/references/x.md", i: "Reading alpha reference" },
  caller: { type: "direct" },
};

/** Where `value` holds cache breakpoints: `tools.10`, `system.0`, `messages.2.content.0`. */
function breakpointPaths(value: unknown, at = ""): string[] {
  const under = (key: string | number) => (at === "" ? `${key}` : `${at}.${key}`);
  if (Array.isArray(value))
    return value.flatMap((item, index) => breakpointPaths(item, under(index)));
  if (typeof value !== "object" || value === null) return [];
  return [
    ...("cache_control" in value ? [at] : []),
    ...Object.entries(value).flatMap(([key, field]) =>
      key === "cache_control" ? [] : breakpointPaths(field, under(key))
    ),
  ];
}

/** A copy of `body` to change in a test. */
function copyOf(body: MessagesBody) {
  return JSON.parse(JSON.stringify(body));
}

/** `body` with each breakpoint of F2's (5 minutes) made an hour long, as the pilot's bodies, which
 *  Oh My Pi built for claude-opus-5-5, hold them. */
function hourLong(body: MessagesBody): MessagesBody {
  return JSON.parse(
    JSON.stringify(body).replaceAll(
      '"cache_control":{"type":"ephemeral"}',
      '"cache_control":{"type":"ephemeral","ttl":"1h"}'
    )
  );
}

/** The last block of `body`'s last message. */
function lastBlock(body: MessagesBody): Record<string, unknown> {
  const content = Object(body.messages.at(-1)).content;
  const block = Array.isArray(content) ? content.at(-1) : undefined;
  if (typeof block !== "object" || block === null) throw new Error("the last message has no block");
  return block;
}

test("answers skill reads and stops at the first other call", async () => {
  const arm = armWith("old", { "SKILL.md": SKILL_MD, "references/x.md": X_MD });
  const { gateway, bodies } = world((n) => f5(n === 1 ? "skill-read" : "text"));
  const replayed = await replayOnce(BODY, arm, gateway);

  expect(bodies).toHaveLength(2);
  // Each send is the body with a cache breakpoint on its last block (withCacheBreakpoint).
  const cached = withCacheBreakpoint(BODY);
  expect(bodies[0]).toEqual(cached);
  // The second send is the same body with the reply and the arm's answer to its read added.
  const { messages: sentMessages, ...sentFields } = bodies[1] ?? BODY;
  const { messages: bodyMessages, ...bodyFields } = cached;
  expect(sentFields).toEqual(bodyFields);
  expect(sentMessages).toEqual([
    ...bodyMessages,
    { role: "assistant", content: [SKILL_READ_CALL] },
    {
      role: "user",
      content: [
        {
          type: "tool_result",
          tool_use_id: SKILL_READ_CALL.id,
          content: [
            {
              type: "text",
              text: `[Skill file: ${INSTALLED}/references/x.md]\n${X_MD.replace(/\n$/, "")}`,
            },
          ],
          is_error: false,
        },
      ],
    },
  ]);
  // The answer shows the installed path, the same in every arm: the arm's own directory is in no
  // body sent.
  expect(JSON.stringify(bodies)).not.toContain(arm.skillDir ?? "no directory");

  expect(replayed.rounds).toBe(1);
  expect(replayed.step.message.stop_reason).toBe("end_turn");
  expect(replayed.step.message.content.map((block) => block.type)).toEqual(["text"]);
  expect(replayed.usage.map((spent) => [spent.model, spent.output])).toEqual([
    ["claude-sonnet-5-5", 84],
    ["claude-sonnet-5-5", 96],
  ]);
});

test("a reply with a skill read and another tool call is the judged step at once", async () => {
  const arm = armWith("old", { "SKILL.md": SKILL_MD, "references/x.md": X_MD });
  const { gateway, bodies } = world(() => f5("skill-read-and-other"));
  const replayed = await replayOnce(BODY, arm, gateway);
  expect(bodies).toHaveLength(1);
  expect(replayed.rounds).toBe(0);
  expect(replayed.step.message.content.map((block) => block.name ?? block.type)).toEqual([
    "text",
    "read",
    "bash",
    "read",
  ]);
  expect(replayed.usage).toHaveLength(1);
});

test("a read of a directory of the skill is the judged step: the arm answers files only", async () => {
  const arm = armWith("old", { "SKILL.md": SKILL_MD, "references/x.md": X_MD });
  // A body that does not stream gets one JSON message.
  const reply = {
    id: "msg_made_up",
    type: "message",
    role: "assistant",
    model: "claude-sonnet-5-5",
    content: [
      {
        type: "tool_use",
        id: "toolu_dir",
        name: "read",
        input: { path: "skill://alpha/references" },
      },
    ],
    stop_reason: "tool_use",
    usage: { input_tokens: 10, output_tokens: 5 },
  };
  const { gateway, bodies } = world(() => Response.json(reply));
  const replayed = await replayOnce({ ...BODY, stream: false }, arm, gateway);
  expect(bodies).toHaveLength(1);
  expect(replayed.rounds).toBe(0);
  expect(replayed.step.message.content).toEqual(reply.content);
});

test("answers at most three rounds of skill reads; the reply after the last is the step", async () => {
  const arm = armWith("old", { "SKILL.md": SKILL_MD, "references/x.md": X_MD });
  const { gateway, bodies } = world(() => f5("skill-read"));
  const replayed = await replayOnce(BODY, arm, gateway);
  expect(MAX_SKILL_ROUNDS).toBe(3);
  expect(bodies).toHaveLength(4);
  expect(bodies.map((body) => body.messages.length)).toEqual([3, 5, 7, 9]);
  // Every round keeps the one breakpoint on the body's own last block: the prefix all runs share.
  for (const sent of bodies) {
    expect(breakpointPaths(sent).sort()).toEqual(["messages.2.content.0", "system.0", "tools.10"]);
  }
  expect(replayed.rounds).toBe(3);
  expect(replayed.step.message.stop_reason).toBe("tool_use");
  expect(replayed.usage).toHaveLength(4);

  const once = world(() => f5("skill-read"));
  expect((await replayOnce(BODY, arm, once.gateway, { maxSkillRounds: 1 })).rounds).toBe(1);
  expect(once.bodies).toHaveLength(2);
});

test("sends the captured anthropic-* headers with the body on every round", async () => {
  const arm = armWith("old", { "SKILL.md": SKILL_MD, "references/x.md": X_MD });
  expect(CAPTURED_HEADERS["anthropic-beta"]).toContain("context-management-2025-06-27");
  const { gateway, headers } = world(() => f5("skill-read"));
  const replayed = await replayOnce(BODY, arm, gateway, { headers: CAPTURED_HEADERS });
  expect(replayed.rounds).toBe(3);
  expect(headers).toHaveLength(4);
  for (const sent of headers) {
    for (const [name, value] of Object.entries(CAPTURED_HEADERS)) {
      expect(sent.get(name)).toBe(value);
    }
  }

  // `replay` gives each of its runs the same headers.
  const runs = world(() => f5("text"));
  const settled = await replay(BODY, arm, runs.gateway, 2, { headers: CAPTURED_HEADERS });
  expect(settled.map((run) => run.status)).toEqual(["fulfilled", "fulfilled"]);
  expect(runs.headers.map((sent) => sent.get("anthropic-beta"))).toEqual([
    CAPTURED_HEADERS["anthropic-beta"],
    CAPTURED_HEADERS["anthropic-beta"],
  ]);
});

test("a read of a file the arm lacks is answered with the not-found result", async () => {
  const lacking = armWith("new", { "SKILL.md": SKILL_MD });
  const { gateway, bodies } = world((n) => f5(n === 1 ? "skill-read" : "text"));
  await replayOnce(BODY, lacking, gateway);
  expect(bodies[1]?.messages.at(-1)).toEqual({
    role: "user",
    content: [
      {
        type: "tool_result",
        tool_use_id: SKILL_READ_CALL.id,
        content: [
          {
            type: "text",
            text: `File not found: ${INSTALLED}/references/x.md`,
          },
        ],
        is_error: true,
      },
    ],
  });

  // The `none` arm has no directory of the skill: Oh My Pi's answer to a skill it lacks.
  const none: Arm = {
    name: "none",
    skill: "alpha",
    skillDir: undefined,
    installedDir: INSTALLED,
    messages: [],
    profileSkills: PROFILE_SKILLS.filter((skill) => skill.name !== "alpha"),
  };
  const noSkill = world((n) => f5(n === 1 ? "skill-read" : "text"));
  await replayOnce(BODY, none, noSkill.gateway);
  expect(noSkill.bodies[1]?.messages.at(-1)).toMatchObject({
    content: [
      {
        tool_use_id: SKILL_READ_CALL.id,
        content: [{ type: "text", text: "Unknown skill: alpha\nAvailable: beta" }],
        is_error: true,
      },
    ],
  });
});

test("replay runs n replays, the first alone, and keeps each one's outcome", async () => {
  const arm = armWith("old", { "SKILL.md": SKILL_MD, "references/x.md": X_MD });
  const events: string[] = [];
  const text = await world(() => f5("text")).gateway.send(BODY);
  let sends = 0;
  // A gateway whose third send fails: the other runs keep their steps.
  const gateway: Gateway = {
    async send(): Promise<Reply> {
      sends += 1;
      const n = sends;
      events.push(`start ${n}`);
      await Promise.resolve();
      events.push(`end ${n}`);
      if (n === 3) throw new Error("the model call failed: made up");
      return text;
    },
    spend: () => ({ models: {}, usd: 0 }),
  };
  const runs = await replay(BODY, arm, gateway, 4);
  expect(events.slice(0, 2)).toEqual(["start 1", "end 1"]);
  expect(events).toHaveLength(8);
  expect(runs.map((run) => run.status)).toEqual([
    "fulfilled",
    "fulfilled",
    "rejected",
    "fulfilled",
  ]);
  expect(runs[0]).toMatchObject({ status: "fulfilled", value: { rounds: 0, step: text } });
  const failed = runs[2];
  expect(failed?.status === "rejected" && String(failed.reason)).toBe(
    "Error: the model call failed: made up"
  );
  await expect(replay(BODY, arm, gateway, 0)).rejects.toThrow("1 or more, not 0");
});

test("withCacheBreakpoint puts one breakpoint on the last block and changes nothing else", () => {
  const before = JSON.stringify(BODY);
  // F2 holds two breakpoints, on its tools and its system prompt, and none in its messages.
  expect(breakpointPaths(BODY).sort()).toEqual(["system.0", "tools.10"]);
  const long = hourLong(BODY);
  const cached = withCacheBreakpoint(long);
  expect(breakpointPaths(cached).sort()).toEqual(["messages.2.content.0", "system.0", "tools.10"]);
  expect(lastBlock(cached)).toMatchObject({ type: "tool_result" });
  expect(lastBlock(cached).cache_control).toEqual({ type: "ephemeral", ttl: "1h" });
  // Byte for byte, the body less the breakpoint added is the body given, which is left as it was.
  const back = copyOf(cached);
  delete lastBlock(back).cache_control;
  expect(JSON.stringify(back)).toBe(JSON.stringify(long));
  expect(JSON.stringify(BODY)).toBe(before);
  // After 5-minute breakpoints, as F2's own are, the one added is 5-minute too: a request may not
  // hold an hour-long breakpoint after a shorter one.
  expect(lastBlock(withCacheBreakpoint(BODY)).cache_control).toEqual({ type: "ephemeral" });
});

test("withCacheBreakpoint keeps at most four, dropping the earliest message ones first", () => {
  const crowded = copyOf(hourLong(BODY));
  crowded.messages[0].content[0].cache_control = { type: "ephemeral", ttl: "1h" };
  crowded.messages[0].content[1].cache_control = { type: "ephemeral", ttl: "1h" };
  crowded.messages[1].content[0].cache_control = { type: "ephemeral", ttl: "1h" };
  const cached = withCacheBreakpoint(crowded);
  expect(MAX_BREAKPOINTS).toBe(4);
  expect(breakpointPaths(cached).sort()).toEqual([
    "messages.1.content.0",
    "messages.2.content.0",
    "system.0",
    "tools.10",
  ]);
  expect(breakpointPaths(crowded)).toHaveLength(5);

  // Tools and a system prompt that hold four already leave no room: the body comes back as it is.
  const full = copyOf(BODY);
  for (const tool of full.tools.slice(0, 3)) tool.cache_control = { type: "ephemeral" };
  expect(breakpointPaths(full)).toHaveLength(5);
  expect(withCacheBreakpoint(full)).toBe(full);
});

test("withCacheBreakpoint does not add a second breakpoint to a last block that holds one", () => {
  const marked = copyOf(BODY);
  lastBlock(marked).cache_control = { type: "ephemeral" };
  expect(withCacheBreakpoint(marked)).toBe(marked);
  expect(breakpointPaths(withCacheBreakpoint(marked))).toHaveLength(3);

  // With more than four, the earliest message breakpoint goes and the last block's own stays.
  marked.messages[0].content[0].cache_control = { type: "ephemeral" };
  marked.messages[0].content[1].cache_control = { type: "ephemeral" };
  const cached = withCacheBreakpoint(marked);
  expect(breakpointPaths(cached).sort()).toEqual([
    "messages.0.content.1",
    "messages.2.content.0",
    "system.0",
    "tools.10",
  ]);
  expect(lastBlock(cached).cache_control).toEqual({ type: "ephemeral" });
});
