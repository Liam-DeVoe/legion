import { afterAll, beforeEach, expect, test } from "bun:test";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { PRIORITY_ID_MAX } from "../packages/contracts/src/dispatch-api-limits";
import { bearerCredential } from "../packages/envoy-client/src/bearer";
import { createHeadlessProof } from "../packages/proof-editor/src/lib-headless";
import { holdDeadlines } from "./dispatch/__tests__/held-deadlines";
import { priorityDescription, rowMarker, seedPriorities } from "./priorities-seed";
import { priorityId } from "./priorities-seed-ids";

/** One priority as the fake Dispatch below holds it. A gone one is a priority somebody deleted:
 *  the listing leaves it out, and its id is still held. */
interface HeldPriority {
  id: string;
  title: string;
  description: string;
  owner: string;
  rank: string;
  state: "live" | "gone";
}

/** One case of the shared list of titles and the id or the refusal each gives. */
type IdCase =
  | { readonly title: string; readonly id: string }
  | { readonly title: string; readonly refusal: "no-slug" | "none" | "any" };

/** The cases of the rule by which a priority's id is made from its title, which the server's
 *  tests read as well (its `description` names them), read by path rather than imported. The
 *  file is this repository's own, and the test `priority id: every case of the shared list gives
 *  its id or its refusal` fails on a case of another shape. */
const idFixture: { readonly cases: readonly IdCase[] } = JSON.parse(
  readFileSync(`${import.meta.dir}/../packages/contracts/fixtures/priority-ids.json`, "utf8")
);
const ID_CASES = idFixture.cases;

/** One issue as the fake Dispatch below holds it: its status and its own `advances` relations,
 *  with the revision of the newest write to them. */
interface HeldIssue {
  status: string;
  advances: { id: string; inherited: boolean }[];
  revision: number;
}

interface Recorded {
  readonly method: string;
  readonly path: string;
  readonly body: unknown;
}

const DOCUMENT = "dispatch://TBL-1/artifact/priorities-md";

/** The route the seed reads that document's table through, as Dispatch writes it. */
const DOCUMENT_PATH = "/api/v1/issues/TBL-1/artifacts/priorities-md/text";

/** The route the check of R10 reads that document at one version through: the version the
 *  board's markers name. */
const VERSIONS_PATH = "/api/v1/issues/TBL-1/artifacts/priorities-md/versions/";

/**
 * The fixture table. It holds a row whose `Where` cell begins with `DONE`, a row naming a key
 * that is `done`, a row naming a key in `icebox`, a row naming no key at all, a row whose owner
 * cell names several owners, a row carrying a bare key and a `dispatch://` reference, a key no
 * issue carries, and two rows naming keys of two projects.
 */
const TABLE = [
  "# The table a person keeps by hand",
  "",
  "| \\# | Priority | Owner | Where |",
  "| --- | --- | --- | --- |",
  "| 1 | Legion works reliably first | INFRA controller | TEST-1 is implementing |",
  "| 2 | Uninstall the old tracker | CHIEF | DONE: TEST-9 removed it from mise |",
  "| 3 | Ship the gateway | OSSV controller | TEST-2 finished it |",
  "| 4 | Nothing a person asked for is parked | INFRA controller (enforcement), BET controller " +
    "(its own list) | TEST-3 sits in icebox |",
  "| 5 | Hourly self-check against this list | CHIEF | nothing to point at yet |",
  "| 6 | One cross-project view of priorities | INFRA fan | TEST-4, and the spec at " +
    "dispatch://TEST-5/artifact/spec-md |",
  "| 7 | Chase the applications | OSSV controller | TEST-404 names no issue |",
  "| 8 | Rocq and Lean speed work restarted | LINUX controller | OTHER-7 and OTHER-8 |",
  "",
  "INFRA holds every slot, so starting new work means ranking it above something.",
  "",
].join("\n");

/** The same rows, moved: row 8 first and rows 4 and 5 swapped. */
const MOVED_TABLE = (() => {
  const lines = TABLE.split("\n");
  const [one, two, three, four, five, six, seven, eight] = lines.slice(4, 12);
  return [
    ...lines.slice(0, 4),
    eight,
    one,
    two,
    three,
    five,
    four,
    six,
    seven,
    ...lines.slice(12),
  ].join("\n");
})();

const priorities: HeldPriority[] = [];
const issues = new Map<string, HeldIssue>();
const requests: Recorded[] = [];
let document = { markdown: TABLE, version: 1 };
/** Whether the fake refuses a write whose body names no session, as the real server refuses
 *  one from a bearer that proves no session of its own (server.go, bearerSessionActor: 400
 *  ACTOR_KIND "bearer callers require actor.kind session"). The shared token every pod carries
 *  is such a bearer. */
let requireActor = false;

/** The id the server of 2026-11-15 made from a title when a create named none: the title as a
 *  slug, with no cut, which it then refused past `PRIORITY_ID_MAX` characters. That refusal
 *  stopped the seed on its first long title while the seed named no id. */
function uncutSlug(title: string): string {
  return title
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "");
}

function issueBody(key: string, issue: HeldIssue): object {
  return {
    key,
    status: issue.status,
    advances: issue.advances,
    advances_revision: issue.revision,
  };
}

/**
 * A Dispatch holding one document, the issues above and the priorities a run writes. It answers
 * the routes the seed and its check call and refuses what the server of 2026-11-15 refused: a
 * priority id no row carries, a whole-set `advances` write whose revision is behind, and a create
 * of an id it holds, a gone one included (409 PRIORITY_EXISTS). A create takes the id its body
 * names, which it refuses past `PRIORITY_ID_MAX` characters, and where the body names none, the
 * title's slug with no cut, which it refuses past the same bound. It refuses the ids `none` and
 * `any`, and a title with no slug where it has to make the id. The listing answers live
 * priorities only, a priority's history answers 200 for any id it holds and 404 for any other,
 * and the document answers its one version by its number.
 */
const server = Bun.serve({
  port: 0,
  async fetch(request) {
    const { pathname } = new URL(request.url);
    const text = await request.text();
    const body = text === "" ? undefined : JSON.parse(text);
    requests.push({ method: request.method, path: pathname, body });
    const actor = (body as { actor?: { kind?: string; id?: string } } | undefined)?.actor;
    if (requireActor && request.method !== "GET" && (actor?.kind !== "session" || !actor.id)) {
      return Response.json(
        { code: "ACTOR_KIND", error: "bearer callers require actor.kind session" },
        { status: 400 }
      );
    }
    if (request.method === "GET" && pathname === DOCUMENT_PATH) {
      return Response.json({ ...document, token: "sha256:document" });
    }
    if (request.method === "GET" && pathname === `${VERSIONS_PATH}${document.version}`) {
      return Response.json({ markdown: document.markdown, number: document.version });
    }
    const key = /^\/api\/v1\/issues\/([A-Z][A-Z0-9]*-[0-9]+)$/.exec(pathname)?.[1];
    if (key !== undefined) {
      const issue = issues.get(key);
      if (issue === undefined) {
        return Response.json(
          { code: "ISSUE_NOT_FOUND", error: `no issue carries the key ${key}` },
          { status: 404 }
        );
      }
      if (request.method === "GET") return Response.json(issueBody(key, issue));
      if (request.method === "PATCH") {
        const write = body as { advances: string[]; advances_revision?: number };
        for (const id of write.advances) {
          if (!priorities.some((priority) => priority.id === id)) {
            return Response.json(
              { code: "ADVANCES_INPUT", error: `no priority carries the id ${id}` },
              { status: 400 }
            );
          }
        }
        if (write.advances_revision !== undefined && write.advances_revision < issue.revision) {
          return Response.json(
            { code: "ADVANCES_REVISION", error: `the revision is ${issue.revision}` },
            { status: 409 }
          );
        }
        const held = issue.advances.filter((entry) => entry.inherited);
        const changes = Math.abs(issue.advances.length - held.length - write.advances.length);
        issue.advances = [...held, ...write.advances.map((id) => ({ id, inherited: false }))];
        issue.revision += Math.max(changes, 1);
        return Response.json(issueBody(key, issue));
      }
    }
    const history = /^\/api\/v1\/priorities\/([^/]+)\/history$/.exec(pathname)?.[1];
    if (history !== undefined && request.method === "GET") {
      const id = decodeURIComponent(history);
      if (priorities.some((priority) => priority.id === id)) return Response.json([]);
      return Response.json(
        { code: "PRIORITY_NOT_FOUND", error: `no priority of the id "${id}"` },
        { status: 404 }
      );
    }
    if (pathname === "/api/v1/priorities") {
      if (request.method === "GET") {
        return Response.json({ priorities: priorities.filter((one) => one.state === "live") });
      }
      if (request.method === "POST") {
        const write = body as { id?: string; title: string; description: string; owner: string };
        const refuse = (error: string): Response =>
          Response.json({ code: "PRIORITY_INPUT", error }, { status: 400 });
        let id = write.id ?? "";
        if (id !== "" && id.length > PRIORITY_ID_MAX) {
          return refuse(
            `an id is at most ${PRIORITY_ID_MAX} characters, and this one is ${id.length}`
          );
        }
        if (id === "") {
          if (!/[A-Za-z0-9]/.test(write.title)) {
            return refuse("this title holds no letter or digit an id can be written from");
          }
          id = uncutSlug(write.title);
          if (id.length > PRIORITY_ID_MAX) {
            return refuse(
              "the id this title derives cannot be used, because an id is at most " +
                `${PRIORITY_ID_MAX} characters, and this one is ${id.length}: name the id in id`
            );
          }
        }
        if (id === "none" || id === "any") return refuse(`"${id}" is no priority id`);
        if (priorities.some((priority) => priority.id === id)) {
          return Response.json(
            { code: "PRIORITY_EXISTS", error: `a priority with the id ${id} is already written` },
            { status: 409 }
          );
        }
        const priority: HeldPriority = {
          id,
          title: write.title,
          description: write.description,
          owner: write.owner,
          rank: String(priorities.length + 1).padStart(4, "0"),
          state: "live",
        };
        priorities.push(priority);
        return Response.json(priority, { status: 201 });
      }
    }
    return Response.json({ code: "NOT_FOUND", error: pathname }, { status: 404 });
  },
});

afterAll(() => server.stop(true));

beforeEach(() => {
  priorities.length = 0;
  requests.length = 0;
  document = { markdown: TABLE, version: 1 };
  requireActor = false;
  issues.clear();
  for (const [key, status] of [
    ["TEST-1", "in_progress"],
    ["TEST-2", "done"],
    ["TEST-3", "icebox"],
    ["TEST-4", "backlog"],
    ["TEST-5", "todo"],
    ["TEST-9", "done"],
    ["OTHER-7", "in_progress"],
    ["OTHER-8", "needs_review"],
  ]) {
    issues.set(key ?? "", { status: status ?? "", advances: [], revision: 0 });
  }
});

const config = {
  url: `http://127.0.0.1:${server.port}`,
  credential: bearerCredential("token"),
};

/** The priorities each issue advances by a relation of its own, in the order they were written. */
function advanced(key: string): string[] {
  return (issues.get(key)?.advances ?? [])
    .filter((entry) => !entry.inherited)
    .map((entry) => entry.id);
}

/** Every write the fake Dispatch was asked for. */
function writes(): Recorded[] {
  return requests.filter((recorded) => recorded.method !== "GET");
}

/** A table of the four columns the seed reads, one row for each `[#, Priority, Owner, Where]`. */
function table(...rows: readonly (readonly [string, string, string, string])[]): string {
  return [
    "| \\# | Priority | Owner | Where |",
    "| --- | --- | --- | --- |",
    ...rows.map((cells) => `| ${cells.join(" | ")} |`),
    "",
  ].join("\n");
}

/** A priority on the board before the run: by default one a person wrote by hand, which carries
 *  no record of any table. */
function hold(
  id: string,
  state: HeldPriority["state"],
  description = "Written by hand on the priorities page."
): HeldPriority {
  const priority: HeldPriority = {
    id,
    title: id,
    description,
    owner: "CHIEF",
    rank: String(priorities.length + 1).padStart(4, "0"),
    state,
  };
  priorities.push(priority);
  return { ...priority };
}

/** The seed run as the command it is, against the fake Dispatch, with the shared bearer every
 *  pod carries and the session it names on each write. */
async function runCommand(
  session: string
): Promise<{ readonly code: number; readonly stdout: string; readonly stderr: string }> {
  const env: Record<string, string | undefined> = {
    ...process.env,
    DISPATCH_URL: config.url,
    DISPATCH_TOKEN: "token",
    DISPATCH_SESSION_ID: session,
  };
  delete env.DISPATCH_TOKEN_FILE;
  delete env.ENVOY_CONFIG_FILE;
  const child = Bun.spawn(["bun", `${import.meta.dir}/priorities-seed.ts`, DOCUMENT], {
    env,
    stderr: "pipe",
    stdout: "pipe",
  });
  const [code, stdout, stderr] = await Promise.all([
    child.exited,
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
  ]);
  return { code, stdout, stderr };
}

/** The check of R10 (`priorities-seed-check.sh`) run against the fake Dispatch, its bearer in a
 *  file of a scratch directory of its own, which goes with the run. */
async function runCheck(): Promise<{
  readonly code: number;
  readonly stdout: string;
  readonly stderr: string;
}> {
  const scratch = mkdtempSync(join(tmpdir(), "priorities-seed-check."));
  try {
    const tokenFile = join(scratch, "token");
    writeFileSync(tokenFile, "token", { mode: 0o600 });
    const child = Bun.spawn(
      ["bash", `${import.meta.dir}/priorities-seed-check.sh`, "--artifact", DOCUMENT],
      {
        env: { ...process.env, DISPATCH_TOKEN_FILE: tokenFile, DISPATCH_URL: config.url },
        stderr: "pipe",
        stdout: "pipe",
      }
    );
    const [code, stdout, stderr] = await Promise.all([
      child.exited,
      new Response(child.stdout).text(),
      new Response(child.stderr).text(),
    ]);
    return { code, stdout, stderr };
  } finally {
    rmSync(scratch, { recursive: true });
  }
}

/** A case of the shared list that gives an id rather than a refusal. */
function givesId(one: IdCase): one is Extract<IdCase, { readonly id: string }> {
  return "id" in one;
}

/** The case of the shared list with the longest title that gives an id. */
function longestCase(): Extract<IdCase, { readonly id: string }> {
  return ID_CASES.filter(givesId).reduce((longest, one) =>
    one.title.length > longest.title.length ? one : longest
  );
}

test("writes one priority for each row but the DONE one, in the table's order", async () => {
  const report = await seedPriorities(config, DOCUMENT);

  expect(priorities.map((priority) => priority.title)).toEqual([
    "Legion works reliably first",
    "Ship the gateway",
    "Nothing a person asked for is parked",
    "Hourly self-check against this list",
    "One cross-project view of priorities",
    "Chase the applications",
    "Rocq and Lean speed work restarted",
  ]);
  expect(report.rows.map((row) => row.row)).toEqual(["1", "3", "4", "5", "6", "7", "8"]);
  expect(report.passed).toEqual(["2"]);
  expect(report.version).toBe(1);
  expect(report.document).toBe(DOCUMENT);
  // The owner cell verbatim, several owners included.
  expect(priorities[2]?.owner).toBe(
    "INFRA controller (enforcement), BET controller (its own list)"
  );
  expect(priorities[0]?.owner).toBe("INFRA controller");
  // Nothing of the DONE row is read: no priority, and no read of the key it names.
  expect(requests.some((recorded) => recorded.path.endsWith("/TEST-9"))).toBe(false);
});

test("relates every key that is not done, the icebox one included", async () => {
  await seedPriorities(config, DOCUMENT);

  expect(advanced("TEST-1")).toEqual(["legion-works-reliably-first"]);
  expect(advanced("TEST-3")).toEqual(["nothing-a-person-asked-for-is-parked"]);
  expect(advanced("TEST-4")).toEqual(["one-cross-project-view-of-priorities"]);
  // A key inside a `dispatch://` reference is a key the row names.
  expect(advanced("TEST-5")).toEqual(["one-cross-project-view-of-priorities"]);
  // Two projects: the eighth row's keys are another project's.
  expect(advanced("OTHER-7")).toEqual(["rocq-and-lean-speed-work-restarted"]);
  expect(advanced("OTHER-8")).toEqual(["rocq-and-lean-speed-work-restarted"]);
  // The `done` key is related by nothing, and the row naming no key relates nothing.
  expect(advanced("TEST-2")).toEqual([]);
  expect(writes().filter((write) => write.path === "/api/v1/issues/TEST-2")).toEqual([]);
});

test("records the key it did not relate with its status, and the key no issue carries", async () => {
  const report = await seedPriorities(config, DOCUMENT);

  const done = report.rows.find((row) => row.row === "3");
  expect(done?.related).toEqual([]);
  expect(done?.excluded).toEqual([{ key: "TEST-2", reason: "done" }]);
  expect(priorities[1]?.description).toContain("Not advanced: TEST-2 (done).");
  const unknown = report.rows.find((row) => row.row === "7");
  expect(unknown?.related).toEqual([]);
  expect(unknown?.excluded).toEqual([{ key: "TEST-404", reason: "no issue carries this key" }]);
  expect(priorities[5]?.description).toContain(
    "Not advanced: TEST-404 (no issue carries this key)."
  );
  const none = report.rows.find((row) => row.row === "5");
  expect(none?.related).toEqual([]);
  expect(none?.excluded).toEqual([]);
  expect(priorities[3]?.description).toContain("Advances: none.\n\nNot advanced: none.");
});

test("writes the row's own text, its number and the version it read as the description", async () => {
  await seedPriorities(config, DOCUMENT);

  expect(priorities[4]?.description).toBe(
    [
      "One cross-project view of priorities",
      "",
      "TEST-4, and the spec at dispatch://TEST-5/artifact/spec-md",
      "",
      "---",
      "",
      `Table row 6 of ${DOCUMENT}, read at version 1.`,
      "",
      "Advances: TEST-4, TEST-5.",
      "",
      "Not advanced: none.",
    ].join("\n")
  );
  expect(rowMarker(priorities[4]?.description ?? "")).toEqual({ row: "6", document: DOCUMENT });
  expect(
    priorityDescription(
      { number: "6", title: "One cross-project view of priorities", owner: "INFRA fan", where: "" },
      DOCUMENT,
      null,
      [],
      []
    )
  ).toContain(`Table row 6 of ${DOCUMENT}, read at version none.`);
});

// A priority's page edits its description in the documents' editor, which reads the lines of one
// paragraph as one line joined by spaces and writes the text back in its own form. Each line of
// the record is a paragraph of its own, so after such an edit the marker is still a line of its
// own: a second run keeps every row by it, and the check of R10 finds every row and its
// relations by it. The editor's own parse and serialize stand in for the edit here
// (`createHeadlessProof`: the soft-break rule the editor's `setMarkdown` applies, and the
// serializer its `getMarkdown` writes with), since the browser editor needs a page to run in.
test("a description edited in the documents' editor keeps its row by its marker, for a second run and the check", async () => {
  await seedPriorities(config, DOCUMENT);
  const proof = await createHeadlessProof();
  for (const priority of priorities) {
    priority.description = proof.serializeMarkdown(proof.parseMarkdown(priority.description));
  }
  // The editor writes the text in its own form: its rule is `***`.
  expect(priorities[4]?.description).toContain("\n\n***\n\n");
  const edited = priorities.map((priority) => ({ ...priority }));
  requests.length = 0;

  const second = await seedPriorities(config, DOCUMENT);

  expect(writes()).toEqual([]);
  expect(priorities).toEqual(edited);
  expect(second.rows.map((row) => [row.row, row.kept])).toEqual(
    ["1", "3", "4", "5", "6", "7", "8"].map((row) => [row, "marker"])
  );
  const check = await runCheck();
  expect(check.stderr).toBe("");
  expect(check.stdout).toContain(
    `${DOCUMENT}, read at version 1: 7 rows stand on the board in the table's order, 0 rows ` +
      "stand on it by their id, 1 rows passed over (DONE), 6 relations read back"
  );
  expect(check.code).toBe(0);
});

test("a second run writes no second priority and no second relation", async () => {
  await seedPriorities(config, DOCUMENT);
  const written = priorities.map((priority) => ({ ...priority }));
  requests.length = 0;

  const second = await seedPriorities(config, DOCUMENT);

  expect(writes()).toEqual([]);
  expect(priorities).toEqual(written);
  expect(second.rows.every((row) => row.kept === "marker")).toBe(true);
  expect(second.rows.flatMap((row) => row.relations)).toEqual([]);
  // The rows the second run read are the same rows, with the relations they already carry.
  expect(second.rows.map((row) => row.related)).toEqual([
    ["TEST-1"],
    [],
    ["TEST-3"],
    [],
    ["TEST-4", "TEST-5"],
    [],
    ["OTHER-7", "OTHER-8"],
  ]);
});

test("a table whose rows moved keeps the priorities it wrote and their order", async () => {
  await seedPriorities(config, DOCUMENT);
  const order = priorities.map((priority) => priority.id);
  requests.length = 0;
  document = { markdown: MOVED_TABLE, version: 2 };

  const moved = await seedPriorities(config, DOCUMENT);

  expect(writes()).toEqual([]);
  expect(priorities.map((priority) => priority.id)).toEqual(order);
  expect(moved.rows.map((row) => row.row)).toEqual(["8", "1", "3", "5", "4", "6", "7"]);
  expect(moved.rows.every((row) => row.kept === "marker")).toBe(true);
});

// A row's priority is found by the row number its record names before anything else, so a row
// given a new title under its `#` cell keeps its priority. Without that, its new title's id is one
// no priority holds, and a second priority would be written for the row.
test("a second run keeps the priority of a row given a new title under its # cell", async () => {
  await seedPriorities(config, DOCUMENT);
  const written = priorities.map((priority) => ({ ...priority }));
  requests.length = 0;
  document = {
    markdown: TABLE.replace("| 3 | Ship the gateway |", "| 3 | Ship the gateway to every region |"),
    version: 2,
  };

  const second = await seedPriorities(config, DOCUMENT);

  expect(writes()).toEqual([]);
  expect(priorities).toEqual(written);
  const retitled = second.rows.find((row) => row.row === "3");
  expect(retitled?.title).toBe("Ship the gateway to every region");
  expect(retitled?.priority).toBe("ship-the-gateway");
  expect(retitled?.kept).toBe("marker");
});

test("priority id: every case of the shared list gives its id or its refusal", () => {
  expect(ID_CASES.length).toBeGreaterThan(0);
  // The seed refuses a row whose title gives no id or a keyword id (placeRows).
  const gives = (title: string): IdCase => {
    const id = priorityId(title);
    if (id === "") return { title, refusal: "no-slug" };
    if (id === "none" || id === "any") return { title, refusal: id };
    return { title, id };
  };
  // Every case the seed gives otherwise, each beside its title.
  const wrong = ID_CASES.flatMap((one) => {
    const seed = gives(one.title);
    return Bun.deepEquals(seed, one) ? [] : [{ case: one, seed }];
  });
  expect(wrong).toEqual([]);
});

// The defect of 2026-11-15: the seed named no id, the server made the slug of the title with no
// cut and refused it past 200 characters, so the seed stopped on its first row. A seed that names
// no id, or names the slug with no cut, fails here.
test("priority id: a title whose slug is longer than 200 writes under its cut id", async () => {
  const longest = longestCase();
  expect(uncutSlug(longest.title).length).toBeGreaterThan(PRIORITY_ID_MAX);
  document = {
    markdown: table(["1", longest.title, "CHIEF", "TEST-1 is implementing"]),
    version: 4,
  };

  const report = await seedPriorities(config, DOCUMENT);

  expect(priorities.map((priority) => [priority.id, priority.title])).toEqual([
    [longest.id, longest.title],
  ]);
  const creates = writes().filter((write) => write.path === "/api/v1/priorities");
  expect(creates.map((write) => write.body)).toMatchObject([{ id: longest.id }]);
  expect(report.rows.map((row) => row.priority)).toEqual([longest.id]);
  expect(advanced("TEST-1")).toEqual([longest.id]);
});

// Two rows that give one id would have the second create refused 409 PRIORITY_EXISTS, with the
// rows above it already written. The run is refused before the first write instead, naming both
// rows, and a DONE row counts, as a row a later run writes once its Where cell moves.
test("priority id: two rows that one id after the cut names are refused before any write", async () => {
  document = {
    markdown: table(
      ["1", "Ship the gateway", "CHIEF", "TEST-1 is implementing"],
      ["2", "Ship the Gateway!", "OSSV controller", "nothing to point at yet"]
    ),
    version: 3,
  };

  await expect(seedPriorities(config, DOCUMENT)).rejects.toThrow(
    "rows 1 and 2 give the one id ship-the-gateway after the cut: " +
      '"Ship the gateway" and "Ship the Gateway!" differ only where an id does not'
  );
  expect(writes()).toEqual([]);
  expect(priorities).toEqual([]);

  // Two long titles that part only past where the id is cut.
  const longest = longestCase();
  const parted = `${longest.title.slice(0, 300)} and then something else`;
  expect(uncutSlug(parted)).not.toBe(uncutSlug(longest.title));
  document = {
    markdown: table(
      ["1", "Hold the line", "CHIEF", "TEST-1 is implementing"],
      ["2", longest.title, "CHIEF", "TEST-4"],
      ["3", parted, "OSSV controller", "DONE last week"]
    ),
    version: 5,
  };

  await expect(seedPriorities(config, DOCUMENT)).rejects.toThrow(
    `rows 2 and 3 give the one id ${longest.id} after the cut`
  );
  expect(writes()).toEqual([]);
  expect(priorities).toEqual([]);
});

test("a row that gives no id, a title over 1,000, none, any or another row's id is refused before any write", async () => {
  const tooLong = `Long title ${"x".repeat(990)}`;
  expect(tooLong.length).toBe(1001);
  // A live priority the seed wrote from row 7 of this table, which a row of another title holds
  // now: the row that gives its id would be refused 409 PRIORITY_EXISTS.
  const recorded = priorityDescription(
    { number: "7", title: "Ship the gateway", owner: "CHIEF", where: "" },
    DOCUMENT,
    1,
    [],
    []
  );
  const refusals: readonly (readonly [string, string])[] = [
    ["... ?! -- ...", 'row 2: the title "... ?! -- ..." holds no ASCII letter or digit'],
    [tooLong, "row 2: the title is 1001 characters"],
    ["None", 'row 2: the title "None" gives the id none'],
    ["Any", 'row 2: the title "Any" gives the id any'],
    [
      "Ship the gateway",
      "rows 2 and 7: the id ship-the-gateway that row 2 gives is held by a live priority that " +
        "carries the record of row 7 of this table",
    ],
  ];
  for (const [title, refusal] of refusals) {
    priorities.length = 0;
    requests.length = 0;
    const before = hold("ship-the-gateway", "live", recorded);
    document = {
      markdown: table(
        ["1", "Hold the line", "CHIEF", "TEST-1 is implementing"],
        ["2", title, "OSSV controller", "TEST-4"]
      ),
      version: 6,
    };

    await expect(seedPriorities(config, DOCUMENT), title).rejects.toThrow(refusal);
    expect(writes(), title).toEqual([]);
    expect(priorities, title).toEqual([before]);
  }

  // Every row it refuses is named, not only the first.
  priorities.length = 0;
  requests.length = 0;
  hold("ship-the-gateway", "live", recorded);
  hold("keep-the-lights-on", "gone");
  document = {
    markdown: table(
      ["1", "Hold the line", "CHIEF", "TEST-1 is implementing"],
      ["2", "... ?! -- ...", "CHIEF", "TEST-4"],
      ["3", tooLong, "CHIEF", "TEST-4"],
      ["4", "None", "CHIEF", "TEST-4"],
      ["5", "Any", "CHIEF", "TEST-4"],
      ["6", "Ship the gateway", "CHIEF", "TEST-4"],
      ["8", "Keep the lights on", "CHIEF", "TEST-4"],
      ["9", "Cut the backlog", "CHIEF", "TEST-4"],
      ["10", "Cut the backlog!", "CHIEF", "TEST-4"]
    ),
    version: 7,
  };

  const refusal = await seedPriorities(config, DOCUMENT).then(
    () => "",
    (error: Error) => error.message
  );

  for (const named of [
    "row 2: the title",
    "row 3: the title is 1001 characters",
    "row 4: the title",
    "row 5: the title",
    "rows 6 and 7:",
    "row 8: the id keep-the-lights-on is a gone priority's",
    "rows 9 and 10 give the one id cut-the-backlog after the cut",
  ]) {
    expect(refusal).toContain(named);
  }
  expect(refusal).not.toContain("row 1:");
  expect(writes()).toEqual([]);
});

test("DONE rows are passed over whatever their title gives, and the other rows are written", async () => {
  document = {
    markdown: table(
      ["1", "Hold the line", "CHIEF", "TEST-1 is implementing"],
      ["2", "... ?! -- ...", "CHIEF", "DONE: nothing to name"],
      ["3", `Long title ${"x".repeat(990)}`, "CHIEF", "DONE in October"],
      ["4", "Any", "CHIEF", "DONE before the list"],
      ["5", "Keep the lights on", "OSSV controller", "TEST-4"]
    ),
    version: 8,
  };

  const report = await seedPriorities(config, DOCUMENT);

  expect(priorities.map((priority) => priority.id)).toEqual([
    "hold-the-line",
    "keep-the-lights-on",
  ]);
  expect(report.passed).toEqual(["2", "3", "4"]);
});

test("a row whose id a live priority without this table's record holds is kept by its id", async () => {
  const byHand = hold("ship-the-gateway", "live");
  // The priority already advances one of the row's two keys.
  issues.set("TEST-4", {
    status: "backlog",
    advances: [{ id: "ship-the-gateway", inherited: false }],
    revision: 1,
  });
  document = {
    markdown: table(
      ["1", "Ship the gateway", "OSSV controller", "TEST-1 and TEST-4"],
      ["2", "Hold the line", "CHIEF", "TEST-3 sits in icebox"]
    ),
    version: 9,
  };

  const run = await runCommand("019a1c01-0d7b-7a2a-8e93-4a5b6c7d8e02");

  expect(run.code, run.stderr).toBe(0);
  expect(run.stdout).toContain(
    "row 1: kept ship-the-gateway (by its id); related TEST-1 of TEST-1, TEST-4; not related none"
  );
  expect(run.stdout).toContain("row 2: wrote hold-the-line;");
  // No copy, and nothing written to the kept priority's title, description or owner.
  expect(priorities[0]).toEqual(byHand);
  const creates = writes().filter((write) => write.path.startsWith("/api/v1/priorities"));
  expect(creates.map((write) => write.body)).toMatchObject([{ id: "hold-the-line" }]);
  expect(advanced("TEST-1")).toEqual(["ship-the-gateway"]);
  expect(advanced("TEST-4")).toEqual(["ship-the-gateway"]);
  expect(advanced("TEST-3")).toEqual(["hold-the-line"]);
});

// The listing leaves a deleted priority out, and Dispatch refuses a create of its id. Without the
// read of each new id's history, the first row would be written and the second refused 409.
test("a row whose id a gone priority holds is refused before any write", async () => {
  const gone = hold("ship-the-gateway", "gone");
  document = {
    markdown: table(
      ["1", "Hold the line", "CHIEF", "TEST-1 is implementing"],
      ["2", "Ship the gateway", "OSSV controller", "TEST-4"]
    ),
    version: 10,
  };

  await expect(seedPriorities(config, DOCUMENT)).rejects.toThrow(
    "row 2: the id ship-the-gateway is a gone priority's"
  );
  expect(writes()).toEqual([]);
  expect(priorities).toEqual([gone]);
});

test("run as a command with the shared bearer, the seed names its session on every write", async () => {
  // The seed is run once, from a session whose Dispatch credential is the shared bearer every
  // pod carries. That bearer proves no session, so Dispatch refuses each write whose body does
  // not name one, and the seed must name the session it runs in (DISPATCH_SESSION_ID).
  requireActor = true;
  const session = "019a1c01-0d7b-7a2a-8e93-4a5b6c7d8e01";
  const { code, stderr } = await runCommand(session);
  expect(stderr).not.toContain("ACTOR_KIND");
  expect(code).toBe(0);
  expect(priorities).toHaveLength(7);
  expect(writes().length).toBeGreaterThan(7);
  for (const write of writes()) {
    expect(write.body).toMatchObject({ actor: { kind: "session", id: session } });
  }
});

test("every request of the seed hands its credential a deadline", async () => {
  const held = holdDeadlines();
  try {
    await seedPriorities({ ...config, credential: held.credential }, DOCUMENT);
    held.expectEachRequestBoundByItsDeadline("the seed");
  } finally {
    held.restore();
  }
});
