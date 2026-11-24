import { describe, expect, test } from "bun:test";
import { parseCommandLine, runTool } from "../word-rewrite/main.ts";
import { OWNER_WINDOW_MS } from "../word-rewrite/pace.ts";
import { runSettle, type SettleOptions } from "../word-rewrite/settle.ts";
import { verdictKey } from "../word-rewrite/verdicts.ts";
import {
  deps,
  FakeClock,
  FakeDispatch,
  FakeModel,
  input,
  keptState,
  options,
  promptParagraph,
  rewriteByRules,
  STATE_DOCUMENT,
  SUMMARY,
  TOOL_SESSION,
  writeOptions,
} from "./word-rewrite-fake.ts";

/**
 * The settle command (spec version 43): write mode with the tester's answers in place of the
 * model's, for the occurrences whose two answers failed.
 */

const SETTLE: SettleOptions = {
  mode: "settle",
  stateDocument: STATE_DOCUMENT,
  answers: "answers.json",
  summary: SUMMARY,
  mergeCommit: "abcdef1234567",
  extraMap: undefined,
  requestLog: undefined,
  stateAddress: new Map(),
  noDaemon: new Set(),
};

/** A model that gives back unchanged every paragraph that quotes the person, which fails the
 *  check twice, and rewrites every other paragraph. */
function quoteRefusingModel(): FakeModel {
  return new FakeModel((user) => {
    const paragraph = promptParagraph(user);
    return JSON.stringify({
      rewrite: paragraph.includes("Sami") ? paragraph : rewriteByRules(paragraph),
    });
  });
}

/**
 * An issue document whose first paragraph quotes the person beside a status use, and whose second
 * uses the word for a status alone, after a write run: the run rewrote the second paragraph and
 * kept both occurrences of the first as judge-refused.
 */
async function afterWriteRun(clock = new FakeClock(), dispatch = new FakeDispatch()) {
  const issue = dispatch.issue("SEED", "An issue");
  const mixed = input("quote-and-status").trimEnd();
  const document = dispatch.document(
    { issue: issue.key },
    "notes.md",
    `${input("quote-and-status")}\n${input("notes")}`
  );
  const run = deps(dispatch, { model: quoteRefusingModel(), clock });
  expect(await runTool(writeOptions(), run)).toBe(0);
  expect(document.version).toBe(2);
  const quoteKey = verdictKey(document.id, mixed, 0);
  const statusKey = verdictKey(document.id, mixed, 1);
  expect((await keptState(dispatch)).refused.map((entry) => entry.key)).toEqual([
    quoteKey,
    statusKey,
  ]);
  return { dispatch, issue, document, mixed, quoteKey, statusKey, clock };
}

/** The state document's markdown now. */
function stateText(dispatch: FakeDispatch): string {
  return (
    [...dispatch.documents.values()].find((one) => one.name === "rewrite-state.md")?.markdown ?? ""
  );
}

async function settle(
  dispatch: FakeDispatch,
  answers: unknown,
  clock = new FakeClock(),
  session = TOOL_SESSION.id
) {
  const run = deps(dispatch, { clock, session });
  const code = await runSettle(SETTLE, JSON.stringify(answers), run);
  return { code, lines: run.lines };
}

describe("the settle command", () => {
  test("its command line takes the state document, the answers file, the summary and the merge commit", () => {
    const parsed = parseCommandLine([
      "settle",
      "--state-document",
      "dispatch://SEED/artifact/rewrite-state-md",
      "--answers",
      "answers.json",
      "--summary",
      SUMMARY,
      "--merge-commit",
      "abcdef1",
    ]);
    expect(parsed).toMatchObject({ mode: "settle", answers: "answers.json", summary: SUMMARY });
    expect(() => parseCommandLine(["settle", "--answers", "a.json", "--tree", "SEED-1"])).toThrow(
      "flag --tree is not for settle"
    );
  });

  test("writes the tester's rewrite in one write, and saves the place its left names for the quote it keeps", async () => {
    const { dispatch, document, mixed, quoteKey, statusKey } = await afterWriteRun();
    const rewritten = rewriteByRules(mixed);
    const quoteAt = mixed.indexOf('"') + 1;
    const run = await settle(dispatch, {
      [statusKey]: { rewrite: rewritten, left: { [String(quoteAt)]: "quote" } },
    });
    expect(run.code).toBe(0);
    expect(run.lines).toContain(`settled item=${document.id} result=written`);
    expect(document.version).toBe(3);
    expect(document.markdown.startsWith(`${rewritten}\n`)).toBe(true);
    const state = await keptState(dispatch);
    expect(state.verdicts.get(verdictKey(document.id, rewritten, 0))).toBe("q");
    expect(state.verdicts.has(quoteKey)).toBe(false);
    expect(state.refused).toEqual([]);
  });

  test("saves the tester's allowed answer as the verdict, and writes nothing", async () => {
    const dispatch = new FakeDispatch();
    const document = dispatch.document({ project: "SEED" }, "quote.md", input("quote"));
    expect(await runTool(writeOptions(), deps(dispatch, { model: quoteRefusingModel() }))).toBe(0);
    const key = verdictKey(document.id, input("quote").trimEnd(), 0);
    expect((await keptState(dispatch)).verdicts.get(key)).toBe("j");
    const run = await settle(dispatch, { [key]: { allowed: "quote" } });
    expect(run.code).toBe(0);
    expect(run.lines).toContain(`settled item=${document.id} result=judged-left`);
    expect(document.version).toBe(1);
    const state = await keptState(dispatch);
    expect(state.verdicts.get(key)).toBe("q");
    expect(state.refused).toEqual([]);
  });

  test("refuses an answer that fails the check, and writes and saves nothing", async () => {
    // A rewrite that keeps the quote and names no place for it.
    const { dispatch, document, mixed, statusKey } = await afterWriteRun();
    const before = stateText(dispatch);
    const run = await settle(dispatch, { [statusKey]: { rewrite: rewriteByRules(mixed) } });
    expect(run.code).toBe(1);
    expect(
      run.lines.some((line) =>
        line.startsWith(
          `refused: answer for key ${[verdictKey(document.id, mixed, 0), statusKey].join(",")}: the rewrite still holds the word`
        )
      )
    ).toBe(true);
    expect(run.lines).toContain(`refused: item=${document.id} result=judge-refused`);
    expect(document.version).toBe(2);
    expect(stateText(dispatch)).toBe(before);
  });

  test("right after the run that wrote its item, the pace holds it back: it writes and saves nothing, and prints when it may write", async () => {
    const clock = new FakeClock();
    const dispatch = new FakeDispatch();
    dispatch.now = () => clock.now();
    const { document, mixed, statusKey } = await afterWriteRun(clock, dispatch);
    const wrote = clock.now();
    const before = stateText(dispatch);
    const quoteAt = mixed.indexOf('"') + 1;
    const answers = {
      [statusKey]: { rewrite: rewriteByRules(mixed), left: { [String(quoteAt)]: "quote" } },
    };
    const held = await settle(dispatch, answers, clock);
    expect(held.code).toBe(1);
    expect(held.lines).toContain(
      `held: item=${document.id}: the pace lets it be written at ${new Date(wrote + OWNER_WINDOW_MS).toISOString()}`
    );
    expect(document.version).toBe(2);
    expect(stateText(dispatch)).toBe(before);

    clock.time += OWNER_WINDOW_MS;
    expect((await settle(dispatch, answers, clock)).code).toBe(0);
    expect(document.version).toBe(3);
  });

  test("writes nothing to a document a person approved at its latest version just before the write, and the approval stays", async () => {
    const { dispatch, document, mixed, statusKey } = await afterWriteRun();
    const approval = { state: "approved", kind: "person", latest_version: 2 };
    const base = dispatch.reads();
    const run = deps(dispatch, {
      reads: {
        ...base,
        // The person approves after the settle read the item, before its write.
        artifact: async (id) => {
          document.approval = approval;
          return base.artifact(id);
        },
      },
    });
    const quoteAt = mixed.indexOf('"') + 1;
    const answers = {
      [statusKey]: { rewrite: rewriteByRules(mixed), left: { [String(quoteAt)]: "quote" } },
    };
    expect(await runSettle(SETTLE, JSON.stringify(answers), run)).toBe(1);
    expect(run.lines).toContain(`refused: item=${document.id} result=approved-by-person`);
    expect(document.version).toBe(2);
    expect(document.approval).toEqual(approval);
    expect(dispatch.writeCalls.filter((call) => call.startsWith("edit"))).toHaveLength(1);
  });

  test("a later report lists the settled occurrences with their place, and none as judge-refused, not judged or changed since", async () => {
    const { dispatch, document, mixed, statusKey } = await afterWriteRun();
    const quoteAt = mixed.indexOf('"') + 1;
    const answers = {
      [statusKey]: { rewrite: rewriteByRules(mixed), left: { [String(quoteAt)]: "quote" } },
    };
    expect((await settle(dispatch, answers)).code).toBe(0);
    const report = deps(dispatch);
    expect(await runTool(options({ stateDocument: STATE_DOCUMENT }), report)).toBe(0);
    expect(
      report.lines.some(
        (line) => line.includes(`document=notes-md`) && line.endsWith("verdict=quote")
      )
    ).toBe(true);
    expect(report.lines).toContain("judge-refused 0");
    expect(report.lines).toContain("not-judged 0");
    expect(report.lines).toContain("changed-since 0");
    expect(document.version).toBe(3);
  });

  test("saves its session's id before its first write, so a write whose later save fails is still its own edit", async () => {
    const { dispatch, document, mixed, statusKey } = await afterWriteRun();
    const writes = dispatch.writes();
    const order: string[] = [];
    let saves = 0;
    const run = deps(dispatch, {
      session: "settle-session",
      writes: {
        ...writes,
        edit: (...args) => {
          order.push("edit");
          return writes.edit(...args);
        },
        uploadState: (...args) => {
          order.push("uploadState");
          saves += 1;
          return saves === 1
            ? writes.uploadState(...args)
            : Promise.resolve({ ok: false, status: 409, code: "PRECONDITION_FAILED" });
        },
      },
    });
    const quoteAt = mixed.indexOf('"') + 1;
    const answers = {
      [statusKey]: { rewrite: rewriteByRules(mixed), left: { [String(quoteAt)]: "quote" } },
    };
    expect(await runSettle(SETTLE, JSON.stringify(answers), run)).toBe(1);
    expect(order).toEqual(["uploadState", "edit", "uploadState"]);
    expect(document.version).toBe(3);
    expect((await keptState(dispatch)).sessions).toContain("settle-session");
  });

  test("moves neither cursor, and reads only its answers' items (spec version 44)", async () => {
    const { dispatch, document, mixed, statusKey } = await afterWriteRun();
    const other = dispatch.document({ project: "SEED" }, "other.md", input("notes"));
    const before = await keptState(dispatch);
    dispatch.calls.length = 0;
    const quoteAt = mixed.indexOf('"') + 1;
    const answers = {
      [statusKey]: { rewrite: rewriteByRules(mixed), left: { [String(quoteAt)]: "quote" } },
    };
    expect((await settle(dispatch, answers)).code).toBe(0);
    const after = await keptState(dispatch);
    expect(after.cursor).toBe(before.cursor);
    expect(after.writeCursor).toBe(before.writeCursor);
    expect(dispatch.calls.filter((call) => call.startsWith("issues "))).toEqual([]);
    expect(dispatch.calls).toContain(`text ${document.id}`);
    expect(dispatch.calls).not.toContain(`text ${other.id}`);
  });

  test("a report prints the run time the pace gives for the settles it lists, one write per item", async () => {
    // Two items with judge-refused occurrences in one project: the project's minute between them.
    const dispatch = new FakeDispatch();
    for (const title of ["One", "Two"]) {
      const issue = dispatch.issue("SEED", title);
      dispatch.document({ issue: issue.key }, "notes.md", input("quote"));
    }
    expect(await runTool(writeOptions(), deps(dispatch, { model: quoteRefusingModel() }))).toBe(0);
    const report = deps(dispatch);
    expect(await runTool(options({ stateDocument: STATE_DOCUMENT, whole: true }), report)).toBe(0);
    expect(report.lines).toContain("judge-refused 2");
    expect(report.lines).toContain("settle-pace writes=2 run-time-s=60");
  });
});
