import { describe, expect, test } from "bun:test";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { Event, liveDispatch } from "../word-rewrite/dispatch-api.ts";
import { parseArguments, runTool, SAVE_INTERVAL_MS } from "../word-rewrite/main.ts";
import { builtInMap } from "../word-rewrite/name-map.ts";
import { holdsWord } from "../word-rewrite/pattern.ts";
import {
  emptyState,
  type HeldWrite,
  newestEventId,
  parseState,
  type RunState,
  readStateDocument,
  STATE_BOUND_BYTES,
  StateTooLarge,
  serializeState,
  stateBytes,
} from "../word-rewrite/state.ts";
import { verdictKey } from "../word-rewrite/verdicts.ts";
import {
  deps,
  FakeClock,
  FakeDispatch,
  FakeModel,
  fixture,
  input,
  options,
  promptParagraph,
  rewriteByRules,
  rewritingModel,
} from "./word-rewrite-fake.ts";

const SUMMARY = "INFRA-2017: the word is replaced by what happened";
const STATE = { project: "SEED", slug: "rewrite-state-md" } as const;

const writeOptions = (overrides = {}) =>
  options({ mode: "write", summary: SUMMARY, stateDocument: STATE, ...overrides });

async function keptState(dispatch: FakeDispatch): Promise<RunState> {
  return (await readStateDocument(dispatch.reads(), STATE)).state;
}

describe("the state between runs", () => {
  test("a run that ends after its first project leaves a state the next goes on from, judging no item twice", async () => {
    const dispatch = new FakeDispatch();
    dispatch.project("SEED").project("ALPHA").project("BETA");
    dispatch.document({ project: "ALPHA" }, "notes.md", input("kernel"));
    const beta = dispatch.document({ project: "BETA" }, "notes.md", input("beta"));
    const model = new FakeModel((user) =>
      promptParagraph(user).includes("kernel")
        ? JSON.stringify({ allowed: "other-team-technical" })
        : JSON.stringify({ rewrite: rewriteByRules(promptParagraph(user)) })
    );
    dispatch.failing.add("BETA");
    const first = deps(dispatch, { model });
    expect(await runTool(writeOptions({ projects: ["SEED", "ALPHA", "BETA"] }), first)).toBe(2);
    expect(first.lines.at(-1)).toBe("unavailable: GET /api/v1/issues transport");
    const afterFirst = await keptState(dispatch);
    expect(afterFirst.finished).toEqual(["SEED", "ALPHA"]);
    expect(afterFirst.whole).toBe(false);
    expect([...afterFirst.verdicts.values()]).toEqual(["t"]);
    expect(model.prompts).toHaveLength(1);

    dispatch.failing.clear();
    const second = deps(dispatch, { model });
    expect(await runTool(writeOptions({ projects: ["SEED", "ALPHA", "BETA"] }), second)).toBe(0);
    expect(dispatch.calls.filter((call) => call === "issues ALPHA")).toHaveLength(1);
    expect(model.prompts).toHaveLength(2);
    expect(beta.markdown).toBe("The beta issue was moved to backlog.\n");
    const afterSecond = await keptState(dispatch);
    expect(afterSecond.whole).toBe(true);
    expect(afterSecond.finished).toEqual(["SEED", "ALPHA", "BETA"]);
  });

  test("after the whole read, an issue's event names its title and every document of it, a project document's event that document, and nothing else is read", async () => {
    // event-log.json: three events of one issue, none with a top-level artifact id, as Dispatch
    // writes them; then one event of a project document.
    const log = Event.array().parse(fixture("event-log.json"));
    expect(log.every((event) => event.issue_key === null || event.artifact_id === null)).toBe(true);
    const dispatch = new FakeDispatch();
    const quiet = dispatch.issue("SEED", input("quiet-title"));
    const untouched = dispatch.document({ issue: quiet.key }, "other.md", input("untouched"));
    for (let number = 2; number < 10; number += 1) dispatch.issue("SEED", `Issue ${number}`);
    const logIssue = dispatch.issue("SEED", "Log steps, renamed");
    const created = dispatch.document({ issue: logIssue.key }, "log-b.md", "Uploaded after it.\n");
    const edited = dispatch.document({ issue: logIssue.key }, "log-a.md", "First.\n\nSecond.\n");
    const sibling = dispatch.document({ issue: logIssue.key }, "log-c.md", "A sibling.\n");
    const projectDocument = dispatch.document({ project: "SEED" }, "notes.md", "Project notes.\n");
    const otherProjectDocument = dispatch.document({ project: "SEED" }, "quiet.md", "Quiet.\n");
    expect(logIssue.key).toBe(log[2]?.issue_key ?? "");
    const kept = dispatch.document({ project: "SEED" }, "rewrite-state.md", "");
    const cursor = dispatch.log.length;
    for (const event of log) dispatch.log.push({ ...event, id: dispatch.log.length + 1 });
    dispatch.log.push({
      id: dispatch.log.length + 1,
      issue_key: null,
      artifact_id: projectDocument.id,
      project: "SEED",
      seq: 0,
      type: "artifact.version",
      actor: { kind: "session", id: "another" },
      created_at: "2026-11-24T10:50:00Z",
      payload: {},
    });
    kept.markdown = serializeState({ ...emptyState(), cursor, whole: true, finished: ["SEED"] });
    dispatch.calls.length = 0;
    const run = deps(dispatch);
    expect(await runTool(options({ stateDocument: STATE }), run)).toBe(0);
    const textReads = dispatch.calls.filter((call) => call.startsWith("text "));
    expect(textReads.sort()).toEqual(
      [created.id, edited.id, sibling.id, projectDocument.id].map((id) => `text ${id}`).sort()
    );
    expect(dispatch.calls).toContain(`issue ${logIssue.key}`);
    expect(dispatch.calls).not.toContain(`issue ${quiet.key}`);
    expect(dispatch.calls).not.toContain(`text ${untouched.id}`);
    expect(dispatch.calls).not.toContain(`text ${otherProjectDocument.id}`);
    expect(dispatch.calls.filter((call) => call.startsWith("issues "))).toEqual([]);
    // The issue's title and three documents, and the project document.
    expect(run.lines).toContain("items-read 5");
  });

  test("a write the pace held back is in the state, and the next run writes it though no event names it", async () => {
    const dispatch = new FakeDispatch();
    const issue = dispatch.issue("SEED", "Waits on the release");
    const held = dispatch.document({ issue: issue.key }, "notes.md", input("notes"));
    const write: HeldWrite = {
      kind: "i",
      project: "SEED",
      issue: issue.key,
      id: held.id,
      keys: [verdictKey(held.id, "x", 0)],
      retry: false,
    };
    const state: RunState = {
      ...emptyState(),
      cursor: dispatch.log.length,
      whole: true,
      finished: ["SEED"],
      held: [write],
    };
    dispatch.document({ project: "SEED" }, "rewrite-state.md", serializeState(state));
    const cursorAfter = dispatch.log.length;
    const run = deps(dispatch, { model: rewritingModel() });
    expect(await runTool(writeOptions({ projects: ["SEED"] }), run)).toBe(0);
    expect(
      dispatch.log
        .slice(0, cursorAfter)
        .some((event) => event.artifact_id === held.id && event.id > (state.cursor ?? 0))
    ).toBe(false);
    expect(held.markdown).toBe("The notes say it was moved to backlog.\n");
    expect((await keptState(dispatch)).held).toEqual([]);
  });

  test("report mode reads the state document and writes only its local --state copy", async () => {
    const dispatch = new FakeDispatch();
    dispatch.document(
      { project: "SEED" },
      "rewrite-state.md",
      serializeState({ ...emptyState(), cursor: 0 })
    );
    const directory = mkdtempSync(join(process.env.TMPDIR ?? tmpdir(), "word-rewrite-state-"));
    try {
      const file = join(directory, "state.md");
      const run = deps(dispatch);
      expect(await runTool(options({ stateDocument: STATE, stateFile: file }), run)).toBe(0);
      expect(dispatch.calls).toContain("stateText SEED rewrite-state-md");
      expect(dispatch.writeCalls).toEqual([]);
      expect(existsSync(file)).toBe(true);
      expect(parseState(readFileSync(file, "utf8")).whole).toBe(true);
    } finally {
      rmSync(directory, { recursive: true, force: true });
    }
  });

  test("write mode saves the state at most once a minute while it holds something unsaved, and once at its end", async () => {
    // Six project documents the judge leaves, each judgement taking 25 seconds.
    const dispatch = new FakeDispatch();
    for (let index = 0; index < 6; index += 1) {
      dispatch.document({ project: "SEED" }, `notes-${index}.md`, input("kernel"));
    }
    const clock = new FakeClock();
    const started = clock.now();
    const model = new FakeModel(() => {
      clock.time += 25_000;
      return JSON.stringify({ allowed: "other-team-technical" });
    });
    const writes = dispatch.writes();
    const savedAt: number[] = [];
    const run = deps(dispatch, {
      model,
      clock,
      writes: {
        ...writes,
        uploadState: (project, name, content, token) => {
          savedAt.push(clock.now() - started);
          return writes.uploadState(project, name, content, token);
        },
      },
    });
    expect(await runTool(writeOptions(), run)).toBe(0);
    // Saved after the third judgement (75 s), the sixth (150 s), and at the end.
    expect(savedAt).toEqual([75_000, 150_000, 150_000]);
    expect(savedAt[1] - savedAt[0]).toBeGreaterThanOrEqual(SAVE_INTERVAL_MS);
    const kept = [...dispatch.documents.values()].find(
      (document) => document.name === "rewrite-state.md"
    );
    expect(kept?.issue).toBeNull();
    expect(kept?.project).toBe("SEED");
    expect((await keptState(dispatch)).verdicts.size).toBe(6);
  });

  test("a run with nothing new to keep writes no state at all", async () => {
    const dispatch = new FakeDispatch();
    const kept = dispatch.document({ project: "SEED" }, "rewrite-state.md", "");
    kept.markdown = serializeState({
      ...emptyState(),
      cursor: dispatch.log.length,
      whole: true,
      finished: ["SEED"],
    });
    const run = deps(dispatch, { model: rewritingModel() });
    expect(await runTool(writeOptions({ projects: ["SEED"] }), run)).toBe(0);
    expect(dispatch.writeCalls.filter((call) => call.startsWith("uploadState"))).toEqual([]);
  });

  test("the state is uploaded with no summary, so Dispatch keeps its version unnamed", async () => {
    const bodies: unknown[] = [];
    const answer = async (_input: Parameters<typeof fetch>[0], init?: RequestInit) => {
      bodies.push(JSON.parse(String(init?.body)));
      const key = new Headers(init?.headers).get("Idempotency-Key") ?? "";
      return new Response(JSON.stringify({ artifact: {}, version: {} }), {
        status: 201,
        headers: { "Content-Type": "application/json", "Idempotency-Key": key },
      });
    };
    const credential = { scheme: "Bearer", headers: () => ({}), secret: () => "" } as const;
    const { writes } = liveDispatch(
      { url: "http://dispatch.test", credential },
      Object.assign(answer, { preconnect: fetch.preconnect })
    );
    const content = serializeState(emptyState());
    expect((await writes.uploadState("SEED", "rewrite-state.md", content, null)).ok).toBe(true);
    expect((await writes.uploadState("SEED", "rewrite-state.md", content, "sha256:1")).ok).toBe(
      true
    );
    expect(bodies).toHaveLength(2);
    for (const body of bodies) {
      expect(typeof body === "object" && body !== null && "summary" in body).toBe(false);
      expect(body).toMatchObject({ name: "rewrite-state.md", content });
    }
    expect(bodies[1]).toMatchObject({ precondition: { document: "sha256:1" } });
  });

  test("the state holds hashes, counts, ids and one-word codes, and no text of any item", async () => {
    const dispatch = new FakeDispatch();
    dispatch.document({ project: "SEED" }, "notes.md", input("quote"));
    const model = new FakeModel(() => JSON.stringify({ allowed: "quote" }));
    expect(await runTool(writeOptions(), deps(dispatch, { model }))).toBe(0);
    const kept = (await dispatch.reads().stateText("SEED", "rewrite-state-md")) as {
      markdown: string;
    };
    expect(holdsWord(kept.markdown)).toBe(false);
    expect(kept.markdown).not.toContain("Sami");
    const state = parseState(kept.markdown);
    expect(
      [...state.verdicts.entries()].every(
        ([key, code]) => /^[0-9a-f]{32}$/.test(key) && code === "q"
      )
    ).toBe(true);
  });

  test("state-document.json, the captured state, reads back", () => {
    const captured = fixture("state-document.json") as { markdown: string };
    const state = parseState(captured.markdown);
    expect(state.whole).toBe(true);
    expect(serializeState(state)).toBe(captured.markdown);
  });

  test("19,000 verdicts, 200 held writes and 22 projects serialize under 900 KiB; one past it is refused", () => {
    const state = emptyState();
    for (let index = 0; index < 19_000; index += 1)
      state.verdicts.set(verdictKey("item", String(index), 0), "t");
    state.held = Array.from({ length: 200 }, (_, index) => ({
      kind: "i" as const,
      project: "SEED",
      issue: `SEED-${index + 1}`,
      id: `00000000-0000-4000-8000-${String(index).padStart(12, "0")}`,
      keys: [verdictKey("held", String(index), 0), verdictKey("held", String(index), 1)],
      retry: true,
    }));
    state.finished = Array.from({ length: 22 }, (_, index) => `PROJECT${index}`);
    expect(Buffer.byteLength(serializeState(state))).toBeLessThan(STATE_BOUND_BYTES);
    for (let index = 0; index < 6_000; index += 1)
      state.verdicts.set(verdictKey("more", String(index), 0), "q");
    expect(() => serializeState(state)).toThrow(StateTooLarge);
    try {
      serializeState(state);
    } catch (error) {
      expect(String(error)).toContain("state-too-large");
      expect(String(error)).toContain("verdicts=25000 held=200 waiting=0 refused=0 projects=22");
    }
  });

  test("the items kept for their gate count in the 900 KiB bound", () => {
    const state = emptyState();
    for (let index = 0; index < 19_000; index += 1)
      state.verdicts.set(verdictKey("item", String(index), 0), "t");
    const waiting = (count: number) =>
      Array.from({ length: count }, (_, index) => ({
        project: "SEED",
        issue: `SEED-${index + 1}`,
        id: `00000000-0000-4000-8000-${String(index).padStart(12, "0")}`,
        gate: "c241",
      }));
    state.waiting = waiting(1_000);
    expect(stateBytes(state)).toBeLessThan(STATE_BOUND_BYTES);
    state.waiting = waiting(3_000);
    expect(stateBytes(state)).toBeGreaterThan(STATE_BOUND_BYTES);
    expect(() => serializeState(state)).toThrow("waiting=3000");
  });
});

/** A model that leaves the kernel's own term and rewrites every other paragraph by the rules of
 *  `inputs.json`, each answer taking 30 seconds of the fake clock. */
function timedModel(clock: FakeClock): FakeModel {
  return new FakeModel((user) => {
    clock.time += 30_000;
    const paragraph = promptParagraph(user);
    return paragraph.includes("kernel")
      ? JSON.stringify({ allowed: "other-team-technical" })
      : JSON.stringify({ rewrite: rewriteByRules(paragraph) });
  });
}

describe("the stop time", () => {
  test("--stop-at takes an RFC 3339 time and --stop-after a duration; anything else is refused", () => {
    const base = ["report", "--tree", "SEED-1", "--merge-commit", "abcdef1"];
    expect(parseArguments([...base, "--stop-at", "2026-11-24T13:30:00Z"]).stopAt).toBe(
      Date.parse("2026-11-24T13:30:00Z")
    );
    expect(parseArguments([...base, "--stop-after", "90m"]).stopAfterMs).toBe(90 * 60_000);
    expect(parseArguments([...base, "--stop-after", "1h30m"]).stopAfterMs).toBe(90 * 60_000);
    expect(parseArguments(base).stopAt).toBeUndefined();
    expect(() => parseArguments([...base, "--stop-at", "tomorrow"])).toThrow("--stop-at");
    expect(() => parseArguments([...base, "--stop-after", "90"])).toThrow("--stop-after");
    expect(() => parseArguments([...base, "--stop-after", "0m"])).toThrow("--stop-after");
  });

  test("a run stopped mid-project saves its verdicts and held-back writes, and the next goes on judging nothing twice", async () => {
    const dispatch = new FakeDispatch();
    const kernel = dispatch.document({ project: "SEED" }, "kernel.md", input("kernel"));
    const first = dispatch.document({ project: "SEED" }, "notes.md", input("notes"));
    const held = dispatch.document({ project: "SEED" }, "beta.md", input("beta"));
    const other = dispatch.document({ project: "OTHER" }, "notes.md", input("project-notes"));
    const cut = dispatch.document({ project: "OTHER" }, "requested.md", input("requested"));
    const clock = new FakeClock();
    const model = timedModel(clock);
    const projects = ["SEED", "OTHER"];

    // 30 s: the kernel's term is left. 60 s: the notes are written, which holds the project for a
    // minute, so beta.md is held back unjudged. 90 s: OTHER's notes are written. Then the stop
    // time (75 s) has passed, and requested.md is neither read for its turn nor judged.
    const stopping = deps(dispatch, { model, clock });
    const run = writeOptions({ projects, stopAfterMs: 75_000 });
    expect(await runTool(run, stopping)).toBe(0);
    expect(stopping.lines.at(-1)).toBe(
      `result stopped at its stop time ${new Date(Date.parse("2026-11-24T12:00:00Z") + 75_000).toISOString()}`
    );
    expect(stopping.lines).toContain("model-calls first=3 second=0 again=0 judged-again=0");
    expect(first.version).toBe(2);
    expect(other.version).toBe(2);
    expect(cut.version).toBe(1);
    const saved = await keptState(dispatch);
    expect(saved.verdicts.size).toBe(1);
    expect(saved.held.map((write) => write.id)).toEqual([held.id]);
    expect(saved.finished).toEqual(["SEED"]);
    expect(saved.whole).toBe(false);
    expect(kernel.version).toBe(1);

    clock.time += 15 * 60_000;
    const next = deps(dispatch, { model, clock });
    expect(await runTool(writeOptions({ projects }), next)).toBe(0);
    expect(next.lines).toContain("model-calls first=2 second=0 again=0 judged-again=0");
    expect(next.lines.at(-1)).toBe("result done");
    expect(held.version).toBe(2);
    expect(cut.version).toBe(2);
    const judged = model.prompts.map(promptParagraph);
    expect(judged).toHaveLength(5);
    expect(new Set(judged).size).toBe(5);
    expect((await keptState(dispatch)).whole).toBe(true);
  });

  test("a run with no stop time runs to its end", async () => {
    const dispatch = new FakeDispatch();
    dispatch.document({ project: "SEED" }, "notes.md", input("notes"));
    const run = deps(dispatch, { model: rewritingModel() });
    expect(await runTool(writeOptions(), run)).toBe(0);
    expect(run.lines.at(-1)).toBe("result done");
  });
});

describe("the items kept until their gate", () => {
  /** The old name of the map entry whose new name is `renamed`. */
  function oldName(renamed: string): string {
    const entry = builtInMap(241).find((candidate) => candidate.new === renamed);
    if (entry === undefined) throw new Error(`the map renames nothing to ${renamed}`);
    return entry.old;
  }

  /** A model that writes each name's new name. */
  function renamingModel(old: string, renamed: string): FakeModel {
    return new FakeModel((user) =>
      JSON.stringify({ rewrite: promptParagraph(user).split(old).join(renamed) })
    );
  }

  test("a kept daemon name is read again once the project's daemon reads at the new contract, with no event naming it", async () => {
    const old = oldName("kept-decision");
    const dispatch = new FakeDispatch();
    dispatch.daemonSettings.set("SEED", "http://daemon.test");
    const notice = dispatch.document(
      { project: "SEED" },
      "notice.md",
      `The controller reads the ${old} notice at its next wake.\n`
    );
    let daemon = 240;
    const daemonStates = { read: async () => ({ contract: { daemon, caller: daemon } }) };
    const model = renamingModel(old, "kept-decision");
    const clock = new FakeClock();
    const runWith = () => deps(dispatch, { model, clock, daemonStates, contract: 241 });

    expect(await runTool(writeOptions(), runWith())).toBe(0);
    const below = await keptState(dispatch);
    expect(below.whole).toBe(true);
    expect(below.waiting).toEqual([{ project: "SEED", issue: null, id: notice.id, gate: "c241" }]);
    expect(model.prompts).toHaveLength(0);

    clock.time += 15 * 60_000;
    dispatch.calls.length = 0;
    expect(await runTool(writeOptions(), runWith())).toBe(0);
    expect(dispatch.calls).not.toContain(`text ${notice.id}`);

    daemon = 241;
    clock.time += 15 * 60_000;
    dispatch.calls.length = 0;
    const crossed = runWith();
    expect(await runTool(writeOptions(), crossed)).toBe(0);
    expect(dispatch.calls).toContain(`text ${notice.id}`);
    expect(crossed.lines).toContain("model-calls first=1 second=0 again=0 judged-again=0");
    expect(notice.markdown).toBe(
      "The controller reads the kept-decision notice at its next wake.\n"
    );
    expect((await keptState(dispatch)).waiting).toEqual([]);
  });

  test("a kept Dispatch API name is read again once /healthz's commit holds the merge, with no event naming it", async () => {
    const old = oldName("priority_work_in_backlog_or_icebox");
    const dispatch = new FakeDispatch();
    const rail = dispatch.document(
      { project: "SEED" },
      "rail.md",
      `The rail reads ${old} from the priorities answer.\n`
    );
    let holds = false;
    const ancestry = { holds: async () => holds };
    const model = renamingModel(old, "priority_work_in_backlog_or_icebox");
    const clock = new FakeClock();
    const runWith = () => deps(dispatch, { model, clock, ancestry, contract: 241 });

    expect(await runTool(writeOptions(), runWith())).toBe(0);
    expect((await keptState(dispatch)).waiting).toEqual([
      { project: "SEED", issue: null, id: rail.id, gate: "m" },
    ]);

    holds = true;
    clock.time += 15 * 60_000;
    dispatch.calls.length = 0;
    const crossed = runWith();
    expect(await runTool(writeOptions(), crossed)).toBe(0);
    expect(crossed.lines).toContain(
      "dispatch commit=1111111111111111111111111111111111111111 holds-merge=yes"
    );
    expect(dispatch.calls).toContain(`text ${rail.id}`);
    expect(rail.markdown).toBe(
      "The rail reads priority_work_in_backlog_or_icebox from the priorities answer.\n"
    );
    expect((await keptState(dispatch)).waiting).toEqual([]);
  });
});

describe("the verdicts' keys", () => {
  test("the tool's own write of one paragraph leaves the verdicts of the others standing", async () => {
    const dispatch = new FakeDispatch();
    const document = dispatch.document(
      { project: "SEED" },
      "mixed.md",
      `${input("kernel")}\n${input("notes")}`
    );
    const clock = new FakeClock();
    const model = timedModel(clock);
    const first = deps(dispatch, { model, clock });
    expect(await runTool(writeOptions(), first)).toBe(0);
    expect(first.lines).toContain("model-calls first=2 second=0 again=0 judged-again=0");
    expect(document.version).toBe(2);

    // The write's own event names the document, so the next run reads it again.
    clock.time += 15 * 60_000;
    dispatch.calls.length = 0;
    const next = deps(dispatch, { model, clock });
    expect(await runTool(writeOptions(), next)).toBe(0);
    expect(dispatch.calls).toContain(`text ${document.id}`);
    expect(next.lines).toContain("model-calls first=0 second=0 again=0 judged-again=0");
  });
});

describe("report mode's counts", () => {
  test("prints the held-back writes the state holds, and calls no model", async () => {
    const dispatch = new FakeDispatch();
    const issue = dispatch.issue("SEED", input("title"));
    const held: HeldWrite = {
      kind: "t",
      project: "SEED",
      issue: issue.key,
      id: issue.key,
      keys: [],
      retry: false,
    };
    const kept = dispatch.document({ project: "SEED" }, "rewrite-state.md", "");
    kept.markdown = serializeState({
      ...emptyState(),
      cursor: dispatch.log.length,
      whole: true,
      finished: ["SEED"],
      held: [held],
    });
    const run = deps(dispatch);
    expect(await runTool(options({ stateDocument: STATE }), run)).toBe(0);
    expect(run.lines.some((line) => line.startsWith("state verdicts=0 held=1 "))).toBe(true);
    expect(run.lines).toContain("model-calls first=0 second=0 again=0 judged-again=0");
    expect(run.lines.some((line) => line.startsWith("occurrence kind=title"))).toBe(true);
  });
  test("--whole reads every project again from the start, with the state's verdicts, and writes no state", async () => {
    // A fully read state: one verdict on the kernel's term, and no event after the cursor.
    const dispatch = new FakeDispatch();
    const kernel = dispatch.document({ project: "SEED" }, "kernel.md", input("kernel"));
    const notes = dispatch.document({ project: "SEED" }, "notes.md", input("notes"));
    const kept = dispatch.document({ project: "SEED" }, "rewrite-state.md", "");
    const verdicts = new Map([[verdictKey(kernel.id, input("kernel").trim(), 0), "t" as const]]);
    kept.markdown = serializeState({
      ...emptyState(),
      cursor: dispatch.log.length,
      whole: true,
      finished: ["SEED"],
      verdicts,
    });
    const before = kept.markdown;

    dispatch.calls.length = 0;
    const evented = deps(dispatch);
    expect(await runTool(options({ stateDocument: STATE }), evented)).toBe(0);
    expect(dispatch.calls).not.toContain(`text ${notes.id}`);

    dispatch.calls.length = 0;
    const whole = deps(dispatch);
    expect(await runTool(options({ stateDocument: STATE, whole: true }), whole)).toBe(0);
    expect(dispatch.calls).toContain(`text ${notes.id}`);
    expect(dispatch.calls).toContain(`text ${kernel.id}`);
    expect(whole.lines.some((line) => line.endsWith("verdict=other-team-technical"))).toBe(true);
    expect(whole.lines.some((line) => line.endsWith("not-judged"))).toBe(true);
    expect(dispatch.writeCalls).toEqual([]);
    expect(kept.markdown).toBe(before);
    expect(() =>
      parseArguments([
        "report",
        "--tree",
        "SEED-1",
        "--merge-commit",
        "abcdef1",
        "--whole",
        "--state",
        "x",
      ])
    ).toThrow("--whole writes no state");
  });

  test("write mode's --whole reads every project again too, with the state's verdicts, and saves its state", async () => {
    const dispatch = new FakeDispatch();
    const kernel = dispatch.document({ project: "SEED" }, "kernel.md", input("kernel"));
    const notes = dispatch.document({ project: "SEED" }, "notes.md", input("notes"));
    const kept = dispatch.document({ project: "SEED" }, "rewrite-state.md", "");
    const cursor = dispatch.log.length;
    const verdicts = new Map([[verdictKey(kernel.id, input("kernel").trim(), 0), "t" as const]]);
    kept.markdown = serializeState({
      ...emptyState(),
      cursor,
      whole: true,
      finished: ["SEED"],
      verdicts,
    });
    const model = rewritingModel();
    const run = deps(dispatch, { model });
    expect(await runTool(writeOptions({ whole: true }), run)).toBe(0);
    // The kernel's term keeps its verdict and is not asked about; the notes are judged and written.
    expect(model.prompts.map(promptParagraph)).toEqual([input("notes").trim()]);
    expect(notes.version).toBe(2);
    const saved = await keptState(dispatch);
    expect(saved.cursor).toBe(cursor);
    expect(saved.verdicts.size).toBe(1);
  });
});

describe("an occurrence whose two answers fail", () => {
  test("is kept in the state, never asked about again, and listed by every report until its text changes", async () => {
    const dispatch = new FakeDispatch();
    const notes = dispatch.document({ project: "SEED" }, "notes.md", input("notes"));
    // The paragraph back as it was: it still holds the word, so both answers fail.
    const model = new FakeModel((user) => JSON.stringify({ rewrite: promptParagraph(user) }));
    const clock = new FakeClock();
    const first = deps(dispatch, { model, clock });
    expect(await runTool(writeOptions(), first)).toBe(0);
    expect(model.prompts).toHaveLength(2);
    expect(first.lines).toContain("model-calls first=1 second=1 again=0 judged-again=0");
    expect(first.lines.some((line) => line.endsWith(" judge-refused"))).toBe(true);
    expect(first.lines).toContain("judge-refused 1");
    const saved = await keptState(dispatch);
    expect([...saved.verdicts.values()]).toEqual(["j"]);
    expect(saved.refused).toEqual([
      { project: "SEED", issue: null, id: notes.id, key: [...saved.verdicts.keys()][0] },
    ]);
    expect(saved.held).toEqual([]);
    expect(notes.version).toBe(1);

    // A later report that does not read the item lists it from the state.
    clock.time += 15 * 60_000;
    const report = deps(dispatch, { clock });
    expect(await runTool(options({ stateDocument: STATE }), report)).toBe(0);
    expect(report.lines).toContain(
      `occurrence project=SEED issue=- item=${notes.id} key=${saved.refused[0]?.key} judge-refused from=state`
    );
    expect(report.lines).toContain("judge-refused 1");

    // A later write run that reads it asks nothing about it.
    const whole = deps(dispatch, { model, clock });
    expect(await runTool(writeOptions({ whole: true }), whole)).toBe(0);
    expect(model.prompts).toHaveLength(2);
    expect(whole.lines).toContain("judge-refused 1");

    // Once its text changes, it leaves the state and the new text is judged.
    dispatch.editAsOther(notes.id, "The notes", "The new notes");
    const changed = deps(dispatch, { model: rewritingModel(), clock });
    clock.time += 15 * 60_000;
    expect(await runTool(writeOptions(), changed)).toBe(0);
    const after = await keptState(dispatch);
    expect(after.refused).toEqual([]);
    expect([...after.verdicts.values()]).not.toContain("j");
    expect(notes.version).toBe(3);
  });
});

describe("the state's contents", () => {
  test("hold only hashes, numbers, true or false, item, issue and event ids, project keys and one-word codes", async () => {
    // A state with every part filled by a real run. SEED, read first: a verdict on the kernel's
    // term, a refused occurrence (beta), a kept name (the notice, its daemon below the contract).
    // OTHER: the title is written, so its notes, which share its owner, are held back.
    const dispatch = new FakeDispatch();
    dispatch.daemonSettings.set("SEED", "http://daemon.test");
    dispatch.document({ project: "SEED" }, "kernel.md", input("kernel"));
    dispatch.document({ project: "SEED" }, "beta.md", input("beta"));
    const notice = builtInMap(241).find((entry) => entry.new === "kept-decision")?.old ?? "";
    dispatch.document({ project: "SEED" }, "notice.md", `It reads the ${notice} notice.\n`);
    const issue = dispatch.issue("OTHER", input("title"));
    dispatch.document({ issue: issue.key }, "notes.md", input("notes"));
    const model = new FakeModel((user) => {
      const paragraph = promptParagraph(user);
      if (paragraph.includes("kernel")) return JSON.stringify({ allowed: "other-team-technical" });
      if (paragraph.includes("beta")) return JSON.stringify({ rewrite: paragraph });
      return JSON.stringify({ rewrite: rewriteByRules(paragraph) });
    });
    const daemonStates = { read: async () => ({ contract: { daemon: 240, caller: 240 } }) };
    expect(
      await runTool(writeOptions(), deps(dispatch, { model, daemonStates, contract: 241 }))
    ).toBe(0);
    const kept = (await dispatch.reads().stateText("SEED", "rewrite-state-md")) as {
      markdown: string;
    };
    const stored: unknown = JSON.parse(kept.markdown.split("\n")[1] ?? "");
    const state = parseState(kept.markdown);
    expect(state.verdicts.size).toBeGreaterThan(0);
    expect(state.held.length).toBeGreaterThan(0);
    expect(state.waiting.length).toBe(1);
    expect(state.refused.length).toBe(1);

    const FIELDS = ["v", "cursor", "whole", "finished", "verdicts", "held", "waiting", "refused"];
    const ITEM_FIELDS = ["k", "p", "i", "id", "keys", "r", "g"];
    const VALUE =
      /^([0-9a-f]{32}|[A-Z][A-Z0-9]*|[A-Z][A-Z0-9]*-[1-9][0-9]*|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}|[a-z]|c[1-9][0-9]*)$/;
    const leaves: unknown[] = [];
    const walk = (value: unknown, names: readonly string[]) => {
      if (Array.isArray(value)) {
        for (const entry of value) walk(entry, ITEM_FIELDS);
      } else if (typeof value === "object" && value !== null) {
        for (const [name, entry] of Object.entries(value)) {
          expect(names.includes(name) || /^[0-9a-f]{32}$/.test(name)).toBe(true);
          walk(entry, ITEM_FIELDS);
        }
      } else {
        leaves.push(value);
      }
    };
    walk(stored, FIELDS);
    for (const leaf of leaves) {
      const kind = typeof leaf;
      expect(
        leaf === null ||
          kind === "number" ||
          kind === "boolean" ||
          (kind === "string" && VALUE.test(String(leaf)))
      ).toBe(true);
    }
    expect(holdsWord(kept.markdown)).toBe(false);
  });
});

describe("the newest id of the event log", () => {
  /** A log of the ids 1 to `newest`, answering as `GET /api/v1/events/log` does: the events after
   *  `after`, oldest first, at most `limit`. */
  function fakeLog(newest: number) {
    const log = {
      reads: 0,
      eventLog: async (after: number, limit: number) => {
        log.reads += 1;
        const ids = Array.from(
          { length: Math.max(0, Math.min(limit, newest - after)) },
          (_, index) => after + 1 + index
        );
        return ids.map((id) => ({
          id,
          issue_key: null,
          artifact_id: null,
          project: "SEED",
          seq: 0,
          type: "issue.updated",
          actor: { kind: "session", id: "s" },
          created_at: "2026-11-24T10:00:00Z",
          payload: {},
        }));
      },
    };
    return log;
  }

  test("over a log of 1,000,000 ids it finds the last id in at most 42 reads", async () => {
    const log = fakeLog(1_000_000);
    expect(await newestEventId(log)).toBe(1_000_000);
    expect(log.reads).toBeLessThanOrEqual(42);
  });

  test("over an empty log it answers 0", async () => {
    expect(await newestEventId({ eventLog: async () => [] })).toBe(0);
  });

  test("a later run starts with no state: the cursor is the newest id at the start of the first whole read", async () => {
    const dispatch = new FakeDispatch();
    dispatch.issue("SEED", "One");
    dispatch.issue("SEED", "Two");
    const newest = dispatch.log.length;
    const run = deps(dispatch, { clock: new FakeClock(), model: rewritingModel() });
    expect(await runTool(writeOptions(), run)).toBe(0);
    expect((await keptState(dispatch)).cursor).toBe(newest);
  });
});
