import { describe, expect, test } from "bun:test";
import { liveDispatch, Whoami } from "../word-rewrite/dispatch-api.ts";
import { runTool } from "../word-rewrite/main.ts";
import { OWNER_WINDOW_MS } from "../word-rewrite/pace.ts";
import { writerRefusal } from "../word-rewrite/write.ts";
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

const SUMMARY = "INFRA-2017: the word for a move out of the workflow is replaced by what happened";
const STATE = { project: "SEED", slug: "rewrite-state-md" } as const;
const SPEC = input("spec");

function writeOptions(overrides = {}) {
  return options({ mode: "write", summary: SUMMARY, stateDocument: STATE, ...overrides });
}

function seeded(title = "Waits on the release") {
  const dispatch = new FakeDispatch();
  const issue = dispatch.issue("SEED", title);
  const spec = dispatch.document({ issue: issue.key }, "spec.md", SPEC);
  return { dispatch, issue, spec };
}

function results(lines: readonly string[]): string[] {
  return lines
    .filter((line) => line.startsWith("write "))
    .map((line) => line.replace(/^.* result=/, ""));
}

describe("a write meets another writer between the tool's read and its write (L8b)", () => {
  test("nothing: one edit with every op, the token read and the summary", async () => {
    const { dispatch, spec } = seeded();
    const run = deps(dispatch, { model: rewritingModel() });
    expect(await runTool(writeOptions(), run)).toBe(0);
    expect(dispatch.writeCalls.filter((call) => call.startsWith("edit"))).toEqual([
      `edit ${spec.id} ops=1 summary=${SUMMARY}`,
    ]);
    expect(spec.markdown).toBe(
      "This issue is in backlog until the release lands.\n\nThe second paragraph names nothing.\n"
    );
    expect(spec.version).toBe(2);
    expect(results(run.lines)).toEqual(["written"]);
  });

  test("a session edits the document: precondition-failed, nothing more written, its edit stays", async () => {
    const { dispatch, spec } = seeded();
    dispatch.beforeWrite = (kind, id) => {
      if (kind === "edit" && id === spec.id) dispatch.editAsOther(id, "The second", "The other");
    };
    const run = deps(dispatch, { model: rewritingModel() });
    expect(await runTool(writeOptions(), run)).toBe(0);
    expect(results(run.lines)).toEqual(["precondition-failed"]);
    expect(dispatch.writeCalls.filter((call) => call.startsWith("edit"))).toHaveLength(1);
    expect(spec.markdown).toBe(input("spec-edited-by-another"));
  });

  test("a write Dispatch refuses is kept in the state and tried again by the next run, which reads the item first", async () => {
    const { dispatch, spec } = seeded();
    let raced = false;
    dispatch.beforeWrite = (kind, id) => {
      if (kind !== "edit" || id !== spec.id || raced) return;
      raced = true;
      dispatch.editAsOther(id, "The second", "The other");
    };
    const clock = new FakeClock();
    const model = rewritingModel();
    expect(await runTool(writeOptions(), deps(dispatch, { model, clock }))).toBe(0);
    const kept = (await dispatch.reads().stateText("SEED", "rewrite-state-md")) as {
      markdown: string;
    };
    expect(kept.markdown).toContain(`"id":"${spec.id}"`);

    clock.time += OWNER_WINDOW_MS;
    dispatch.calls.length = 0;
    const retry = deps(dispatch, { model, clock });
    expect(await runTool(writeOptions(), retry)).toBe(0);
    expect(dispatch.calls).toContain(`text ${spec.id}`);
    expect(results(retry.lines)).toEqual(["written"]);
    // The refused write's occurrence is judged again, and counted apart from the first asks.
    expect(retry.lines).toContain("model-calls first=0 second=0 again=1 judged-again=1");
    expect(spec.markdown).toBe(rewriteByRules(input("spec-edited-by-another")));
  });

  test("an event that leaves the title as it was is no reason to leave a judged title: it is read again and written", async () => {
    const dispatch = new FakeDispatch();
    const issue = dispatch.issue("SEED", input("title"));
    const model = new FakeModel((user) => {
      // A comment lands while the judge thinks: the issue's last event moves, its title does not.
      dispatch.touch(issue.key);
      return JSON.stringify({ rewrite: rewriteByRules(promptParagraph(user)) });
    });
    const run = deps(dispatch, { model });
    expect(await runTool(writeOptions(), run)).toBe(0);
    expect(results(run.lines)).toEqual(["written"]);
    expect(issue.title).toBe(rewriteByRules(input("title")));
  });

  test("the issue's last event moves before a title write: issue-changed", async () => {
    const dispatch = new FakeDispatch();
    const issue = dispatch.issue("SEED", input("title"));
    dispatch.beforeWrite = (kind, key) => {
      if (kind === "retitle") dispatch.touch(key);
    };
    const run = deps(dispatch, { model: rewritingModel() });
    expect(await runTool(writeOptions(), run)).toBe(0);
    expect(results(run.lines)).toEqual(["issue-changed"]);
    expect(issue.title).toBe(input("title"));
  });

  test("the issue closes before a title write: issue-closed", async () => {
    const dispatch = new FakeDispatch();
    const issue = dispatch.issue("SEED", input("title"));
    dispatch.beforeWrite = (kind, key) => {
      if (kind === "retitle") dispatch.close(key);
    };
    const run = deps(dispatch, { model: rewritingModel() });
    expect(await runTool(writeOptions(), run)).toBe(0);
    expect(results(run.lines)).toEqual(["issue-closed"]);
    expect(issue.title).toBe(input("title"));
  });

  for (const [moved, approval] of [
    ["approved-by-person", { state: "approved", kind: "person", latest_version: 1 }],
    ["approval-request", { state: "awaiting", waiting_on: "human", latest_version: 1 }],
  ] as const) {
    test(`a person's approval move before the write leaves the document: ${moved}`, async () => {
      const { dispatch, spec } = seeded();
      const model = new FakeModel((user) => {
        // The person acts while the judge thinks: after the read, before the write.
        spec.approval = approval;
        return JSON.stringify({ rewrite: rewriteByRules(promptParagraph(user)) });
      });
      const run = deps(dispatch, { model });
      expect(await runTool(writeOptions(), run)).toBe(0);
      expect(results(run.lines)).toEqual([moved]);
      expect(dispatch.writeCalls.filter((call) => call.startsWith("edit"))).toEqual([]);
      expect(spec.markdown).toBe(SPEC);
    });
  }

  test("the pace's bound holds: the write waits for a later run, unjudged, in the state", async () => {
    // The spec and the notes share their owner, the issue: one write to it in 15 minutes.
    const { dispatch, issue, spec } = seeded();
    const second = dispatch.document({ issue: issue.key }, "notes.md", input("notes"));
    const clock = new FakeClock();
    const model = rewritingModel();
    const first = deps(dispatch, { model, clock });
    expect(await runTool(writeOptions(), first)).toBe(0);
    expect(results(first.lines)).toEqual(["written", "held"]);
    expect(model.prompts).toHaveLength(1);
    expect(spec.version).toBe(2);
    expect(second.markdown).toBe(input("notes"));
    const state = dispatch.reads();
    const kept = (await state.stateText("SEED", "rewrite-state-md")) as { markdown: string };
    expect(kept.markdown).toContain(`"id":"${second.id}"`);

    clock.time += OWNER_WINDOW_MS;
    const later = deps(dispatch, { model, clock });
    expect(await runTool(writeOptions(), later)).toBe(0);
    expect(second.markdown).toBe("The notes say it was moved to backlog.\n");
    expect(model.prompts).toHaveLength(2);
  });
});

describe("who may write", () => {
  test("a person's credential is refused: whoami kind user", async () => {
    const { dispatch } = seeded();
    dispatch.whoami = { kind: "user", login: "alice@example.com", scopes: ["person", "approve"] };
    const run = deps(dispatch, { model: rewritingModel() });
    expect(await runTool(writeOptions(), run)).toBe(2);
    expect(run.lines).toEqual(["refused: whoami kind=user"]);
    expect(dispatch.writeCalls).toEqual([]);
  });

  test("a personal agent token is refused: whoami-personal.json, kind agent with an owner", async () => {
    const { dispatch } = seeded();
    dispatch.whoami = fixture("whoami-personal.json");
    const run = deps(dispatch, { model: rewritingModel() });
    expect(await runTool(writeOptions(), run)).toBe(2);
    expect(run.lines).toEqual(["refused: whoami kind=agent owner=set"]);
    expect(dispatch.writeCalls).toEqual([]);
  });

  test("the shared token may write: whoami-shared.json", () => {
    expect(writerRefusal(Whoami.parse(fixture("whoami-shared.json")))).toBeNull();
  });

  test("a token with a project set writes only that project, and lists the others", async () => {
    const { dispatch, spec } = seeded();
    const other = dispatch.document({ project: "OTHER" }, "notes.md", input("notes"));
    dispatch.whoami = {
      kind: "agent",
      owner: null,
      project: "SEED",
      scopes: [],
      service: null,
      session: null,
    };
    const run = deps(dispatch, { model: rewritingModel() });
    expect(await runTool(writeOptions(), run)).toBe(0);
    expect(spec.version).toBe(2);
    expect(other.markdown).toBe(input("notes"));
    expect(run.lines).toContain("reason credential-scope 1");
  });

  test("a second run over the first run's result writes nothing", async () => {
    // A title and a project document of two projects: two owners, two projects, two writes.
    const dispatch = new FakeDispatch();
    dispatch.issue("SEED", input("title"));
    dispatch.document({ project: "OTHER" }, "notes.md", input("notes"));
    const model = rewritingModel();
    expect(await runTool(writeOptions(), deps(dispatch, { model }))).toBe(0);
    const firstWrites = dispatch.writeCalls.filter(
      (call) => !call.startsWith("uploadState")
    ).length;
    expect(firstWrites).toBe(2);
    dispatch.writeCalls.length = 0;
    const prompts = model.prompts.length;
    expect(await runTool(writeOptions(), deps(dispatch, { model }))).toBe(0);
    expect(dispatch.writeCalls.filter((call) => !call.startsWith("uploadState"))).toEqual([]);
    expect(model.prompts.length).toBe(prompts);
  });
});

describe("Dispatch's answers to the writes, from the captured bodies", () => {
  /** The live writes over a fetch that answers one fixture with one status. */
  function liveWrites(status: number, body: unknown) {
    // Dispatch echoes each write's Idempotency-Key on its answer, as the client's write table reads.
    const answer = async (_input: Parameters<typeof fetch>[0], init?: RequestInit) => {
      const key = new Headers(init?.headers).get("Idempotency-Key") ?? "";
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json", "Idempotency-Key": key },
      });
    };
    const credential = { scheme: "Bearer", headers: () => ({}), secret: () => "" } as const;
    return liveDispatch(
      { url: "http://dispatch.test", credential },
      Object.assign(answer, { preconnect: fetch.preconnect })
    ).writes;
  }

  test("edit-ok.json is a written edit", async () => {
    expect(
      (await liveWrites(200, fixture("edit-ok.json")).edit("d", [], "sha256:0", SUMMARY)).ok
    ).toBe(true);
  });

  for (const [file, code] of [
    ["edit-precondition-failed.json", "PRECONDITION_FAILED"],
    ["title-issue-changed.json", "ISSUE_CHANGED"],
    ["title-issue-closed.json", "ISSUE_CLOSED"],
  ] as const) {
    test(`${file} is the refusal ${code}`, async () => {
      const writes = liveWrites(409, fixture(file));
      const outcome = file.startsWith("edit")
        ? await writes.edit("d", [], "sha256:0", SUMMARY)
        : await writes.retitle("SEED-9", "t", 4);
      expect(outcome).toEqual({ ok: false, status: 409, code });
    });
  }
});
