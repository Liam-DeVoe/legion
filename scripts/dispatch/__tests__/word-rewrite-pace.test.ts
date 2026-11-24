import { describe, expect, test } from "bun:test";
import { runTool } from "../word-rewrite/main.ts";
import {
  OWNER_WINDOW_MS,
  Pace,
  type PacedWrite,
  PROJECT_WINDOW_MS,
  runTimeMs,
  WRITE_GAP_MS,
} from "../word-rewrite/pace.ts";
import { readStateDocument } from "../word-rewrite/state.ts";
import {
  deps,
  FakeClock,
  FakeDispatch,
  input,
  options,
  rewritingModel,
} from "./word-rewrite-fake.ts";

const write = (owner: string, project: string): PacedWrite => ({ owner, project });

describe("the pace of write mode", () => {
  test("no write inside 10 seconds: the next waits out the gap", async () => {
    const clock = new FakeClock();
    const pace = new Pace(clock);
    const started = clock.now();
    await pace.gap();
    expect(clock.now()).toBe(started);
    pace.wrote(write("A-1", "A"));
    clock.time += 3_000;
    await pace.gap();
    expect(clock.now() - started).toBe(WRITE_GAP_MS);
    pace.wrote(write("B-1", "B"));
    await pace.gap();
    expect(clock.now() - started).toBe(2 * WRITE_GAP_MS);
  });

  test("no second write to one owner inside 15 minutes", () => {
    const clock = new FakeClock();
    const pace = new Pace(clock);
    pace.wrote(write("SEED-3", "SEED"));
    clock.time += OWNER_WINDOW_MS - 1;
    expect(pace.admits(write("SEED-3", "SEED"))).toBe(false);
    expect(pace.admits(write("SEED-4", "SEED"))).toBe(true);
    clock.time += 1;
    expect(pace.admits(write("SEED-3", "SEED"))).toBe(true);
  });

  test("no second write in one project inside one minute", () => {
    const clock = new FakeClock();
    const pace = new Pace(clock);
    pace.wrote(write("SEED-3", "SEED"));
    clock.time += PROJECT_WINDOW_MS - 1;
    expect(pace.admits(write("SEED-4", "SEED"))).toBe(false);
    expect(pace.admits(write("OTHER-1", "OTHER"))).toBe(true);
    clock.time += 1;
    expect(pace.admits(write("SEED-4", "SEED"))).toBe(true);
  });

  test("a write the project's bound holds is kept for the next run, which writes it", async () => {
    const dispatch = new FakeDispatch();
    const first = dispatch.issue("SEED", input("title"));
    const second = dispatch.issue("SEED", input("title"));
    const clock = new FakeClock();
    const model = rewritingModel();
    const stateDocument = { project: "SEED", slug: "rewrite-state-md" } as const;
    const run = options({ mode: "write", summary: "INFRA-2017: a test", stateDocument });
    expect(await runTool(run, deps(dispatch, { model, clock }))).toBe(0);
    const written = [first, second].filter((issue) => issue.title !== input("title"));
    expect(written).toHaveLength(1);
    const kept = await readStateDocument(dispatch.reads(), stateDocument);
    expect(kept.state.held.map((held) => held.id)).toEqual(
      [first, second].filter((issue) => issue.title === input("title")).map((issue) => issue.key)
    );

    clock.time += PROJECT_WINDOW_MS;
    expect(await runTool(run, deps(dispatch, { model, clock }))).toBe(0);
    expect([first, second].every((issue) => issue.title !== input("title"))).toBe(true);
    expect((await readStateDocument(dispatch.reads(), stateDocument)).state.held).toEqual([]);
  });

  test("the run time the three bounds give, over a mix of owners and projects", () => {
    expect(runTimeMs([])).toBe(0);
    // Three owners in three projects: the 10-second gap alone.
    expect(runTimeMs([write("A-1", "A"), write("B-1", "B"), write("C-1", "C")])).toBe(
      2 * WRITE_GAP_MS
    );
    // Three owners in one project: one write a minute.
    expect(runTimeMs([write("A-1", "A"), write("A-2", "A"), write("A-3", "A")])).toBe(
      2 * PROJECT_WINDOW_MS
    );
    // One owner twice: its second write waits 15 minutes; the others fit in between.
    expect(
      runTimeMs([write("A-1", "A"), write("A-1", "A"), write("A-2", "A"), write("B-1", "B")])
    ).toBe(OWNER_WINDOW_MS);
    // A project document is an owner of its own beside its project's issues.
    expect(runTimeMs([write("A/notes-md", "A"), write("A-1", "A")])).toBe(PROJECT_WINDOW_MS);
  });
});
