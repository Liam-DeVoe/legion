/** The judging rules of `e2e/acceptance/restricted-sweep.sh`, each held alone: the script runs
 *  against a fake Dispatch of this file's own that keeps every route from the restricted member
 *  but the one a row plants a leak in, so the leak that row plants is the only reason the run can
 *  fail. A run against a real stack (`restricted-sweep.test.ts`) fails on many routes at once, and
 *  a rule that stopped working there would hide behind the others.
 *
 *  The fake answers the full member's lookup with one object of each kind the sweep fills a
 *  wildcard with, all of the project TGT. To the full member, any other path or query that names
 *  one of those objects answers a row of TGT, a bare list answers one too, and a path naming an
 *  object that does not exist answers 404. To the restricted member, every path that names an
 *  object of TGT or a missing one answers 404 and a bare list answers `[]`, as a server holding
 *  them to their own projects does. Its sockets refuse every upgrade, so the socket section never
 *  proves anything here and a run with no leak is UNKNOWN, never PASS.
 *
 *  It also records every path the restricted member is sent, so a row can hold the sweep to
 *  calling every GET row of the route index, read from that record rather than from the script's
 *  own lines. */

import { afterAll, describe, expect, test } from "bun:test";
import { readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { skipWithout } from "@legion/test-fixtures/host-tools";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import type { ServerWebSocket } from "bun";
import { createEncoder, toUint8Array, writeVarString, writeVarUint } from "lib0/encoding";

import { rowLimit, runBounded } from "./bounded-subprocess";

const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));
const sweepScript = fileURLToPath(new URL("../acceptance/restricted-sweep.sh", import.meta.url));
const routeIndex = fileURLToPath(
  new URL("../../../envoy/internal/dispatch/api/testdata/routes.txt", import.meta.url)
);
/** The pattern of each GET row of the route index, in its order. */
const getRows = readFileSync(routeIndex, "utf8")
  .split("\n")
  .map((line) => line.split(" "))
  .filter(([method]) => method === "GET")
  .map(([, path]) => path ?? "");

const fullToken = "the-full-members-token";
const memberCookie = "dsession=the-restricted-members-cookie";
const fullCookie = "dsession=the-full-members-cookie";

/** TGT's objects, as the fake's lookup answers them, and the reference scheme it writes: the sweep
 *  reads the scheme from the server's own answer rather than spelling one. */
const tgt = {
  issue: "TGT-1",
  title: "Chart the target reef",
  document: "11111111-1111-4111-8111-111111111111",
  block: "b-1111111111",
  comment: "22222222-2222-4222-8222-222222222222",
  ask: "33333333-3333-4333-8333-333333333333",
  message: "44444444-4444-4444-8444-444444444444",
  handoff: "55555555-5555-4555-8555-555555555555",
  projectDocument: "66666666-6666-4666-8666-666666666666",
  nearCopy: "77777777-7777-4777-8777-777777777777",
  goingRun: "88888888-8888-4888-8888-888888888888",
  session: "ses-tgt-worker",
  conversationFile: "shot-png",
  directMessage: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
  repository: "acme/tgt-repo",
};
const scheme = "fake-ref://";
const tgtValues = Object.values(tgt);
/** The values the sweep fills a path with on its missing side, which name nothing. */
const missingValues = [
  "ZZNONE",
  "00000000-0000-4000-8000-",
  "no-such-",
  "nobody@example.com",
  "ses-nosuchsession",
  "b-nosuchblock",
  "999999999999",
  "zz no such words zz",
];

/** What a planted leak answers the restricted member, for the requests it matches. */
interface Leak {
  readonly matches: (target: string) => boolean;
  readonly status: number;
  readonly body: unknown;
}

/** What a fake changes of its answers. `omit` leaves a kind out of the lookup; `issues` is how many
 *  issues TGT lists (1 by default), and `handoffOn` the one whose handoff the lookup finds;
 *  `conversationGone` answers the conversation file as missing to the full member too;
 *  `proxyReadsCookies` makes the GitHub proxy read as the real one does, as the browser's person
 *  alone, so the full member's token is answered there as a missing repository is. */
interface FakeShape {
  readonly omit?: ReadonlySet<string>;
  readonly issues?: number;
  readonly handoffOn?: string;
  readonly conversationGone?: boolean;
  readonly proxyReadsCookies?: boolean;
  readonly feed?: FeedShape;
}

/** The lookup the full member is answered, by exact path and query: TGT's objects, and OWN's, the
 *  member's own project, whose issue names carol and no session. */
function lookup(target: string, fake: FakeShape): unknown {
  const author = { kind: "user", id: "carol@example.com" };
  const agent = { kind: "session", id: tgt.session };
  const handoffOn = fake.handoffOn ?? tgt.issue;
  const issues = Array.from({ length: fake.issues ?? 1 }, (_, index) => ({
    key: `TGT-${index + 1}`,
    title: index === 0 ? tgt.title : `Issue ${index + 1} of the target reef`,
    repo: "acme/tgt-repo",
  }));
  const answers: Record<string, unknown> = {
    "/api/v1/whoami": { kind: "agent", owner: "carol@example.com" },
    "/api/v1/projects": [{ key: "OWN" }, { key: "TGT" }],
    "/api/v1/projects/TGT/access": { project: "TGT", people: [] },
    "/api/v1/issues?project=TGT&limit=50": { issues },
    "/api/v1/issues?project=OWN&limit=50": {
      issues: [{ key: "OWN-1", created_by: author, assignee: "carol@example.com" }],
    },
    "/api/v1/issues/OWN-1/comments": [],
    "/api/v1/issues/OWN-1/asks": [],
    "/api/v1/issues/OWN-1/messages": [],
    "/api/v1/issues/OWN-1/handoffs": [],
    [`/api/v1/issues/${tgt.issue}/summary`]: {
      documents: [{ id: tgt.document, slug: "spec", primary: true }],
    },
    [`/api/v1/artifacts/${tgt.document}/blocks`]: [{ id: tgt.block }],
    [`/api/v1/issues/${tgt.issue}/comments`]: [{ id: tgt.comment, author }],
    [`/api/v1/comments/${tgt.comment}/blocks`]: {
      item: { issue_key: tgt.issue, ref: `${scheme}${tgt.issue}/comment/${tgt.comment}` },
    },
    [`/api/v1/issues/${tgt.issue}/asks`]: [{ id: tgt.ask, author: agent }],
    [`/api/v1/issues/${tgt.issue}/messages`]: [{ message: { id: tgt.message, author } }],
    [`/api/v1/agents/${tgt.session}/messages`]: [
      { message: { id: tgt.directMessage, author, body: "A direct message to the worker." } },
    ],
    [`/api/v1/issues/${tgt.issue}/handoffs`]: [],
    [`/api/v1/issues/${handoffOn}/handoffs`]: fake.omit?.has("handoff")
      ? []
      : [{ id: tgt.handoff, phase: "plan", author: agent }],
    "/api/v1/projects/TGT/artifacts": [
      { id: tgt.projectDocument, issue_key: null, slug: "playbook-md" },
    ],
    "/api/v1/near-copy-checks?project=TGT": { checks: [{ id: tgt.nearCopy }] },
    "/api/v1/priorities": { priorities: [{ id: "tgt-work" }] },
    "/api/v1/broadcasts": [{ id: "99999999-9999-4999-8999-999999999999" }],
    "/api/v1/delivery/timeline": { runs: [{ id: 900 }] },
  };
  if (fake.conversationGone === true) {
    answers[`/api/v1/agents/${tgt.session}/artifacts/${tgt.conversationFile}`] = null;
  }
  return answers[target];
}

/** Whether `target`, decoded, names an object of TGT: one of its ids or keys, or the project key
 *  standing as a path segment, a query value or a reference's project. */
function namesTgt(target: string): boolean {
  return (
    tgtValues.some((value) => target.includes(value)) ||
    /(^|[/=])TGT([-/&?]|$)/.test(target) ||
    target.includes(`${scheme}TGT`)
  );
}

/** How a fake serves its sockets. The live feed: not at all (every upgrade refused 404), or, with
 *  `live`, a feed that starts for both callers and sends the full member each comment it is
 *  written. `closes` closes the member's feed when the comment is written, which the sweep writes
 *  only once both feeds have started, so it closes long before any window could end.
 *  `memberRefusal` refuses the member's live upgrade 404 with that body. `noise` sends the full
 *  member, in place of the comment, an event of TGT whose first field holds that text. The
 *  document socket: not at all, or, with `doc`, a room for the member's own document that admits
 *  them, as Hocuspocus answers a token frame, and a 404 for TGT's, whose body is `docRefusal`
 *  when a row sets it. */
interface FeedShape {
  readonly live?: boolean;
  readonly closes?: boolean;
  readonly memberRefusal?: unknown;
  readonly noise?: string;
  readonly doc?: boolean;
  readonly docRefusal?: unknown;
}

/** The member's own document, in OWN, which the document socket's control opens. */
const ownDocument = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";

/** Hocuspocus's answer to a token frame that admits the caller: the room, the authentication
 *  message (2), its authenticated subtype (2) and the scope (socket-access.ts reads it). */
function admitted(room: string): Uint8Array {
  const frame = createEncoder();
  writeVarString(frame, room);
  writeVarUint(frame, 2);
  writeVarUint(frame, 2);
  writeVarString(frame, "read-write");
  return toUint8Array(frame);
}

/** What a socket of the fake is: the member's or the full member's, and, for the document
 *  socket, the room. */
interface SocketData {
  readonly member: boolean;
  readonly room?: string;
}

/** A fake Dispatch on a port of its own, with `leaks` planted for the restricted member. */
function fakeDispatch(leaks: readonly Leak[], fake: FakeShape) {
  const feed = fake.feed ?? {};
  const memberPaths: string[] = [];
  const fullFeeds: ServerWebSocket<SocketData>[] = [];
  const memberFeeds: ServerWebSocket<SocketData>[] = [];
  let events = 0;
  const server = Bun.serve<SocketData>({
    hostname: "127.0.0.1",
    port: 0,
    async fetch(request, served) {
      const url = new URL(request.url);
      const target = decodeURIComponent(url.pathname + url.search);
      const member = request.headers.get("cookie") === memberCookie;
      const fullBrowser = request.headers.get("cookie") === fullCookie;
      const full = fullBrowser || request.headers.get("authorization") === `Bearer ${fullToken}`;
      if (request.headers.get("upgrade") !== null) {
        if (member && feed.memberRefusal !== undefined && url.pathname === "/ws/live") {
          return Response.json(feed.memberRefusal, { status: 404 });
        }
        if (feed.live === true && url.pathname === "/ws/live" && (member || full)) {
          if (served.upgrade(request, { data: { member } })) return undefined;
        }
        if (feed.doc === true && member && url.pathname === `/ws/doc/${ownDocument}`) {
          if (served.upgrade(request, { data: { member, room: ownDocument } })) return undefined;
        }
        if (feed.doc === true && member && url.pathname === `/ws/doc/${tgt.document}`) {
          return feed.docRefusal === undefined
            ? new Response("document not found", { status: 404 })
            : Response.json(feed.docRefusal, { status: 404 });
        }
        return new Response("no socket here", { status: 404 });
      }
      if (!member && !full) return Response.json({ code: "NO_IDENTITY" }, { status: 401 });
      if (member) {
        memberPaths.push(url.pathname);
        if (target === "/api/v1/whoami") {
          return Response.json({
            kind: "user",
            login: "bob@outside.example",
            member_kind: "restricted",
          });
        }
        if (target === "/api/v1/projects") return Response.json([{ key: "OWN" }]);
        if (feed.doc === true && target === "/api/v1/schema/blocks") {
          return Response.json({ version: 1 });
        }
        if (feed.doc === true && target === "/api/v1/projects/OWN/artifacts") {
          return Response.json([{ id: ownDocument, issue_key: null, slug: "guide-md" }]);
        }
        const leak = leaks.find((planted) => planted.matches(target));
        if (leak !== undefined) return Response.json(leak.body, { status: leak.status });
        if (namesTgt(target) || missingValues.some((value) => target.includes(value))) {
          return Response.json({ code: "NOT_FOUND", error: "not found" }, { status: 404 });
        }
        return Response.json([]);
      }
      if (request.method === "POST" && target === "/api/v1/going/requests") {
        return Response.json({ id: tgt.goingRun, project: "TGT" }, { status: 201 });
      }
      if (request.method === "POST" && target === `/api/v1/issues/${tgt.issue}/comments`) {
        events += 1;
        const id = crypto.randomUUID();
        // The noise leads its event, so a line that quotes the feed's first bytes quotes it.
        const event =
          feed.noise === undefined
            ? { id: events, issue_key: tgt.issue, project: "TGT", payload: { id } }
            : { body: feed.noise, id: events, issue_key: tgt.issue, project: "TGT" };
        for (const socket of fullFeeds) {
          socket.send(JSON.stringify({ type: "event", sub: "e", id: String(events), event }));
        }
        if (feed.closes === true) {
          for (const socket of memberFeeds) socket.close(1011, "the feed went away");
        }
        return Response.json({ id }, { status: 201 });
      }
      if (
        fake.proxyReadsCookies === true &&
        !fullBrowser &&
        target.startsWith("/api/github/rest/")
      ) {
        return Response.json({ code: "NOT_FOUND", error: "not found" }, { status: 404 });
      }
      const answer = lookup(target, fake);
      if (answer === null) return Response.json({ code: "NOT_FOUND" }, { status: 404 });
      if (answer !== undefined) return Response.json(answer);
      if (missingValues.some((value) => target.includes(value))) {
        return Response.json({ code: "NOT_FOUND", error: "not found" }, { status: 404 });
      }
      return Response.json([{ project: "TGT", issue_key: tgt.issue, path: target }]);
    },
    websocket: {
      message(socket, raw) {
        if (socket.data.room !== undefined) {
          // The one frame the probe sends is its token frame; a fake admits the member to it.
          socket.send(admitted(socket.data.room));
          return;
        }
        const frame = JSON.parse(String(raw)) as { type?: string; sub?: string };
        if (frame.type !== "subscribe") return;
        socket.send(
          JSON.stringify({ type: "events_start", sub: frame.sub, since: String(events) })
        );
        (socket.data.member ? memberFeeds : fullFeeds).push(socket);
      },
    },
  });
  return { memberPaths, origin: `http://127.0.0.1:${server.port}`, server };
}

const servers: { stop: (closeActiveConnections?: boolean) => unknown }[] = [];
afterAll(() => {
  for (const server of servers) server.stop(true);
});

const indexes = temporaryDirectories("restricted-sweep-index");

/** What a row changes of a run: the fake's answers (FakeShape), inputs (a variable set undefined
 *  is left out), and `rows`, the GET patterns of the route index the run reads in place of the
 *  whole of it. A sweep of the whole index makes about 400 calls; a row that holds one rule reads
 *  only the rows that rule needs. */
interface RunShape extends FakeShape {
  readonly env?: Readonly<Record<string, string | undefined>>;
  readonly rows?: readonly string[];
}

/** What one run of the sweep answered, and the paths of every request the member sent. */
interface SweptRun {
  readonly code: number;
  readonly stdout: string;
  readonly memberPaths: readonly string[];
}

/** One run of the sweep against a fake with `leaks` planted. */
async function sweep(leaks: readonly Leak[], shape: RunShape = {}): Promise<SweptRun> {
  const fake = fakeDispatch(leaks, shape);
  servers.push(fake.server);
  let routes: string | undefined;
  if (shape.rows !== undefined) {
    routes = join(indexes(), "routes.txt");
    writeFileSync(routes, shape.rows.map((pattern) => `GET ${pattern} any\n`).join(""));
  }
  const inputs: Record<string, string | undefined> = {
    PATH: process.env.PATH ?? "",
    TMPDIR: tmpdir(),
    SWEEP_ORIGIN: fake.origin,
    SWEEP_PROJECT: "TGT",
    SWEEP_WRITE_ISSUE: tgt.issue,
    SWEEP_FULL_TOKEN: fullToken,
    SWEEP_RESTRICTED_COOKIE: memberCookie,
    SWEEP_LIVE_SECONDS: "1",
    SWEEP_CONVERSATION_FILE: `${tgt.session}/${tgt.conversationFile}`,
    SWEEP_PACE_SECONDS: "0",
    SWEEP_ROUTES: routes,
    ...shape.env,
  };
  const run = await runBounded({
    argv: ["bash", sweepScript],
    bound: "short",
    cwd: dispatchPackage,
    env: Object.fromEntries(
      Object.entries(inputs).filter((entry): entry is [string, string] => entry[1] !== undefined)
    ),
    from: import.meta.url,
  });
  fake.server.stop(true);
  return { code: run.code, memberPaths: fake.memberPaths, stdout: run.stdout };
}

/** The `FAIL    ` lines of a run, without that prefix. */
function failures(stdout: string): string[] {
  return stdout
    .split("\n")
    .filter((line) => line.startsWith("FAIL    "))
    .map((line) => line.slice("FAIL    ".length));
}

/** A leak at the one request whose decoded path and query is `target`. */
function at(target: string, status: number, body: unknown): Leak {
  return { matches: (seen) => seen === target, status, body };
}

const withoutTools = skipWithout("bash", "curl", "jq", "bun");

describe.skipIf(withoutTools)("each judging rule, alone", () => {
  test(
    "a 200 for the project's object where a missing one is refused is a FAIL naming that route",
    async () => {
      const { code, stdout } = await sweep(
        [at(`/api/v1/artifacts/${tgt.projectDocument}/asks`, 200, [])],
        { rows: ["/api/v1/artifacts/{id}/asks"] }
      );
      expect(failures(stdout)).toEqual([
        "GET /api/v1/artifacts/{id}/asks: the member is answered 200 for TGT's object, where a missing one is refused 404: []",
      ]);
      expect(stdout).toContain(
        "\nFAIL: 1 answers gave the member what TGT holds, the first: GET /api/v1/artifacts/{id}/asks: "
      );
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  test(
    "a 200 holding a row of the project, where a missing one is answered 200 too, is a FAIL",
    async () => {
      const { code, stdout } = await sweep(
        [
          at(`/api/v1/issues/${tgt.issue}/comments`, 200, [{ id: tgt.comment }]),
          at("/api/v1/issues/ZZNONE-1/comments", 200, []),
        ],
        { rows: ["/api/v1/issues/{key}/comments"] }
      );
      expect(failures(stdout)).toEqual([
        `GET /api/v1/issues/{key}/comments: the member is answered 200 holding ${tgt.comment}, a row of TGT: [{"id":"${tgt.comment}"}]`,
      ]);
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  test(
    "a refusal other than 403 or 404 for the project's object, where a missing one is refused 404, is a FAIL",
    async () => {
      // As the server writes it (api/owner.go): the issue in prose, and the route it names. The
      // prose names no row; the refusal itself says the document exists, which a missing one's
      // 404 does not.
      const refusal = {
        code: "ARTIFACT_LINKED",
        error: `artifact belongs to issue ${tgt.issue}; use /api/v1/issues/${tgt.issue}/...`,
      };
      const { code, stdout } = await sweep(
        [at(`/api/v1/artifacts/${tgt.projectDocument}/comments`, 400, refusal)],
        { rows: ["/api/v1/artifacts/{id}/comments"] }
      );
      expect(failures(stdout)).toEqual([
        `GET /api/v1/artifacts/{id}/comments: the member is answered 400 for TGT's object, otherwise than a missing one answered 404: ${JSON.stringify(refusal)}`,
      ]);
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  test(
    "a list holding a row of the project is a FAIL naming the list",
    async () => {
      const { code, stdout } = await sweep(
        [at("/api/v1/inbox", 200, [{ id: tgt.ask, issue_key: tgt.issue }])],
        { rows: ["/api/v1/inbox"] }
      );
      expect(failures(stdout)).toEqual([
        `GET /api/v1/inbox: the member is answered 200 holding ${tgt.ask}, a row of TGT: [{"id":"${tgt.ask}","issue_key":"${tgt.issue}"}]`,
      ]);
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  // The route index's text names example issue keys of any project, in prose (the search route's
  // description names one): an issue key inside prose is no row of the project. The list row
  // above is its control, an issue key the server wrote as a whole value.
  test(
    "a route index whose prose names an issue key of the project is no FAIL",
    async () => {
      const index = {
        routes: [
          {
            path: "/api/v1/search",
            description:
              "A q that is one issue key in any form a person writes it - TGT-1042, tgt-1042, TGT 1042.",
          },
        ],
      };
      const { stdout } = await sweep([at("/api/v1", 200, index)], { rows: ["/api/v1"] });
      expect(failures(stdout)).toEqual([]);
      expect(stdout).toContain("\nok      GET /api/v1: 200, no row of TGT (can fail)\n");
    },
    rowLimit("short")
  );

  // Every form a person or an agent cites the project's items by, each in the member's own row
  // (an OWN item of their inbox). Where a person wrote it - inside prose of a `body`, or as the
  // whole `body` and `title` of an OWN comment whose whole text, or first line, is the link - it
  // names no row of the project and the run is no FAIL; only the plain key, which is itself a
  // marker, counts there, and only whole. As the whole value of a member the server writes
  // (`member`: a `ref`, an `href`, a `url`) each form is a row, which names its key. The rows of
  // the two kinds are each other's controls.
  const forms: { form: string; text: string; marker: string; member: string }[] = [
    { form: "a plain issue key", text: tgt.issue, marker: tgt.issue, member: "ref" },
    { form: "a reference", text: `${scheme}${tgt.issue}`, marker: tgt.issue, member: "ref" },
    { form: "an issue path", text: `/issues/${tgt.issue}`, marker: tgt.issue, member: "href" },
    { form: "a project path", text: "/projects/TGT", marker: "TGT", member: "href" },
    {
      form: "a dashboard URL",
      text: `https://dispatch.example/issues/${tgt.issue}`,
      marker: tgt.issue,
      member: "url",
    },
  ];
  /** The member's own inbox item, OWN's, with `fields` beside its id and issue key. */
  const ownItem = (fields: Record<string, string>) => [
    { id: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", issue_key: "OWN-1", ...fields },
  ];

  test.each(forms)(
    "$form inside prose of the member's own row is no FAIL",
    async ({ text }) => {
      const body = `This follows ${text}, the target plan.`;
      const { stdout } = await sweep([at("/api/v1/inbox", 200, ownItem({ body }))], {
        rows: ["/api/v1/inbox"],
      });
      expect(failures(stdout)).toEqual([]);
      expect(stdout).toContain("\nok      GET /api/v1/inbox: 200, no row of TGT (can fail)\n");
    },
    rowLimit("short")
  );

  test.each(forms.filter(({ form }) => form !== "a plain issue key"))(
    "$form as the whole body and title a person wrote is no FAIL",
    async ({ text }) => {
      const { stdout } = await sweep(
        [at("/api/v1/inbox", 200, ownItem({ title: text, body: text }))],
        { rows: ["/api/v1/inbox"] }
      );
      expect(failures(stdout)).toEqual([]);
      expect(stdout).toContain("\nok      GET /api/v1/inbox: 200, no row of TGT (can fail)\n");
    },
    rowLimit("short")
  );

  // The plain key is a marker itself, so the whole body a person wrote counts when it is the key.
  test(
    "a plain issue key as the whole body a person wrote is a FAIL naming it",
    async () => {
      const item = ownItem({ body: tgt.issue });
      const { code, stdout } = await sweep([at("/api/v1/inbox", 200, item)], {
        rows: ["/api/v1/inbox"],
      });
      expect(failures(stdout)).toEqual([
        `GET /api/v1/inbox: the member is answered 200 holding ${tgt.issue}, a row of TGT: ${JSON.stringify(item)}`,
      ]);
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  test.each(forms)(
    "$form as the whole value of a member the server writes is a FAIL naming it",
    async ({ text, marker, member }) => {
      const item = ownItem({ [member]: text });
      const { code, stdout } = await sweep([at("/api/v1/inbox", 200, item)], {
        rows: ["/api/v1/inbox"],
      });
      expect(failures(stdout)).toEqual([
        `GET /api/v1/inbox: the member is answered 200 holding ${marker}, a row of TGT: ${JSON.stringify(item)}`,
      ]);
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  // A list filtered by the project's key answers the member's own row, whose prose names that key:
  // the same answer for the project and for a missing one, which the comparison must read alike,
  // though the key is a fill on one side only. The search route takes the project as a filter.
  test(
    "a filter by the project that answers the member's own prose naming its key, as a missing one is answered, is no FAIL",
    async () => {
      const own = [{ issue_key: "OWN-1", snippet: "See /projects/TGT for the plan." }];
      const searches: Leak = {
        matches: (seen) => seen.startsWith("/api/v1/search?"),
        status: 200,
        body: own,
      };
      const { stdout } = await sweep([searches], { rows: ["/api/v1/search"] });
      expect(failures(stdout)).toEqual([]);
      expect(stdout).toContain(
        "\nok      GET /api/v1/search?q=<title>&project=: 200, as a missing one is answered (can fail)\n"
      );
    },
    rowLimit("short")
  );

  test(
    "of two leaks, the verdict names the one whose row comes first in the route index",
    async () => {
      const { code, stdout } = await sweep(
        [
          at("/api/v1/inbox", 200, [{ issue_key: tgt.issue }]),
          at(`/api/v1/artifacts/${tgt.projectDocument}/asks`, 200, []),
        ],
        { rows: ["/api/v1/artifacts/{id}/asks", "/api/v1/inbox"] }
      );
      const found = failures(stdout);
      expect(found.map((line) => line.split(":")[0])).toEqual([
        "GET /api/v1/artifacts/{id}/asks",
        "GET /api/v1/inbox",
      ]);
      expect(stdout).toContain(
        `\nFAIL: 2 answers gave the member what TGT holds, the first: ${found[0]}\n`
      );
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  test(
    "a session that worked in the project alone, in the member's agents list, is a FAIL",
    async () => {
      const { code, stdout } = await sweep(
        [at("/api/v1/agents", 200, [{ session_id: tgt.session, title: "Works on TGT" }])],
        { rows: ["/api/v1/agents"] }
      );
      expect(failures(stdout)).toEqual([
        `GET /api/v1/agents: the member is answered 200 holding ${tgt.session}, a row of TGT: [{"session_id":"${tgt.session}","title":"Works on TGT"}]`,
      ]);
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  // The control of the row above: the same list with no session of the project's is no FAIL.
  test(
    "the member's agents list with no session of the project is no FAIL",
    async () => {
      const { stdout } = await sweep([], { rows: ["/api/v1/agents"] });
      expect(failures(stdout)).toEqual([]);
      expect(stdout).toMatch(/^ok {6}GET \/api\/v1\/agents: 200, no row of TGT \(can fail\)$/m);
    },
    rowLimit("short")
  );

  test(
    "a row of the project in an answer that also measures its own time is a FAIL",
    async () => {
      const { code, stdout } = await sweep(
        [
          at(`/api/v1/search?q=${tgt.title}`, 200, {
            took_ms: 3,
            results: [{ kind: "document", project: "TGT" }],
          }),
        ],
        { rows: ["/api/v1/search"] }
      );
      expect(failures(stdout)).toEqual([
        `GET /api/v1/search?q=: the member is answered 200 holding TGT, a row of TGT: {"took_ms":3,"results":[{"kind":"document","project":"TGT"}]}`,
      ]);
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  test(
    "a row of the project in the body of the member's refused live upgrade is a FAIL",
    async () => {
      const { code, stdout } = await sweep([], {
        feed: { memberRefusal: { error: "refused", project: "TGT" } },
        rows: ["/api/v1/whoami"],
      });
      expect(failures(stdout)).toEqual([
        `/ws/live: the member's refused upgrade holds TGT, a row of TGT: http 404 {"error":"refused","project":"TGT"}`,
      ]);
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  // The control of the row above: the same refusal, its body naming nothing of TGT, is a refusal.
  test(
    "the member's refused live upgrade whose body names nothing of the project is a refusal",
    async () => {
      const { stdout } = await sweep([], {
        feed: { live: true, memberRefusal: { error: "refused" } },
        rows: ["/api/v1/whoami"],
      });
      expect(failures(stdout)).toEqual([]);
      expect(stdout).toContain(
        `\nok      /ws/live: the member's upgrade is refused: http 404 {"error":"refused"}\n`
      );
    },
    rowLimit("short")
  );

  test(
    "a row of the project in the body of the member's refused document upgrade is a FAIL",
    async () => {
      const { code, stdout } = await sweep([], {
        feed: { doc: true, docRefusal: { error: "refused", project: "TGT" } },
        rows: ["/api/v1/whoami"],
      });
      expect(failures(stdout)).toEqual([
        `/ws/doc/{room}: the refusal holds TGT, a row of TGT: http 404 {"error":"refused","project":"TGT"}`,
      ]);
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  // The control of the row above: the member is admitted to their own document, and refused
  // TGT's with a body that names nothing of it.
  test(
    "the member's refused document upgrade whose body names nothing of the project is a refusal",
    async () => {
      const { stdout } = await sweep([], { feed: { doc: true }, rows: ["/api/v1/whoami"] });
      expect(failures(stdout)).toEqual([]);
      expect(stdout).toContain(
        "\nok      /ws/doc/{room}: http 404 document not found (control: admitted to their own document)\n"
      );
    },
    rowLimit("short")
  );

  test(
    "a direct message to the project's session, in the member's list of that session's messages, is a FAIL",
    async () => {
      const listed = [
        { message: { id: tgt.directMessage, body: "A direct message to the worker." } },
      ];
      const { code, stdout } = await sweep(
        [at(`/api/v1/agents/${tgt.session}/messages`, 200, listed)],
        { rows: ["/api/v1/agents/{session_id}/messages"] }
      );
      expect(failures(stdout)).toEqual([
        `GET /api/v1/agents/{session_id}/messages: the member is answered 200 holding ${tgt.directMessage}, a row of TGT: ${JSON.stringify(listed)}`,
      ]);
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  // The control of the row above: the same list refused, and the direct message by its own
  // routes refused too, which a run reads whatever routes it is given.
  test(
    "the member refused the project's session's messages and its direct message is no FAIL",
    async () => {
      const { stdout } = await sweep([], { rows: ["/api/v1/agents/{session_id}/messages"] });
      expect(failures(stdout)).toEqual([]);
      expect(stdout).toContain(
        "\nok      GET /api/v1/agents/{session_id}/messages: 404 (can fail)\n"
      );
      expect(stdout).toContain(
        "\nok      GET /api/v1/messages/{id} (a direct message): 404 (can fail)\n"
      );
      expect(stdout).toContain(
        "\nok      GET /api/v1/messages/{id}/blocks (a direct message): 404 (can fail)\n"
      );
    },
    rowLimit("short")
  );

  test(
    "a GitHub proxy read of the project's repository that the member is answered is a FAIL",
    async () => {
      const pull = { number: 1, title: "The change of the target reef" };
      const { code, stdout } = await sweep(
        [at(`/api/github/rest/repos/${tgt.repository}/pulls/1`, 200, pull)],
        { rows: ["/api/v1/whoami"] }
      );
      expect(failures(stdout)).toEqual([
        `GET /api/github/rest/repos/{owner}/{repo}/pulls/{number}: the member is answered 200 for TGT's repository, where one the App is not installed on is answered 404: ${JSON.stringify(pull)}`,
      ]);
      expect(code).toBe(1);
    },
    rowLimit("short")
  );

  // The control of the row above: each proxy read of the project's repository answered as one
  // the App is not installed on.
  test(
    "each GitHub proxy read answered as a repository the App is not installed on is no FAIL",
    async () => {
      const { stdout } = await sweep([], { rows: ["/api/v1/whoami"] });
      expect(failures(stdout)).toEqual([]);
      for (const shape of [
        "pulls/{number}",
        "issues/{number}",
        "commits/{sha}/check-runs",
        "actions/runs?head_sha={sha}",
      ]) {
        expect(stdout).toContain(
          `\nok      GET /api/github/rest/repos/{owner}/{repo}/${shape}: 404, as a repository the App is not installed on (can fail)\n`
        );
      }
    },
    rowLimit("short")
  );

  // A proxy that reads only a browser's person, as the real one does: with the full member's
  // cookie the reads can fail, and with their token alone they are blind, never a pass of a read
  // that could fail.
  test(
    "with the full member's cookie, a proxy that reads no token is read as the full member",
    async () => {
      const { stdout } = await sweep([], {
        env: { SWEEP_FULL_COOKIE: fullCookie },
        proxyReadsCookies: true,
        rows: ["/api/v1/whoami"],
      });
      expect(stdout).toContain(
        "\nok      GET /api/github/rest/repos/{owner}/{repo}/pulls/{number}: 404, as a repository the App is not installed on (can fail)\n"
      );
      expect(stdout).not.toContain("the-full-members-cookie");
    },
    rowLimit("short")
  );

  test(
    "without the full member's cookie, a proxy that reads no token makes its reads blind",
    async () => {
      const { stdout } = await sweep([], { proxyReadsCookies: true, rows: ["/api/v1/whoami"] });
      expect(stdout).toContain(
        "\nok      GET /api/github/rest/repos/{owner}/{repo}/pulls/{number}: 404, as a repository the App is not installed on (blind)\n"
      );
    },
    rowLimit("short")
  );
});

/** The one run of this file over the whole route index, with nothing planted, which two rows read:
 *  it makes about 400 calls. */
let quiet: Promise<SweptRun> | undefined;
function quietRun(): Promise<SweptRun> {
  quiet ??= sweep([]);
  return quiet;
}

describe.skipIf(withoutTools)("a run that proves nothing", () => {
  test(
    "with every route refused but neither socket proved, is UNKNOWN, never PASS",
    async () => {
      const { code, stdout } = await quietRun();
      expect(failures(stdout)).toEqual([]);
      expect(stdout).toContain(
        "\nUNKNOWN: nothing reached the member, but this run did not prove it:\n"
      );
      expect(stdout).toMatch(/^UNKNOWN \/ws\/doc\/\{room\}: /m);
      expect(stdout).toContain(
        "\nUNKNOWN /ws/live: the full member's feed did not stay subscribed: it was sent http 404\n"
      );
      expect(stdout).not.toContain("PASS");
      expect(code).toBe(2);
    },
    rowLimit("short")
  );

  test(
    "where the lookup finds no object of one kind, is UNKNOWN and names the kind",
    async () => {
      const { code, stdout } = await sweep([], {
        omit: new Set(["handoff"]),
        rows: ["/api/v1/whoami"],
      });
      expect(stdout).toMatch(/^missing handoff: no issue of the first 1 holds a handoff$/m);
      expect(stdout).toContain("\n  handoff: no issue of the first 1 holds a handoff\n");
      expect(code).toBe(2);
    },
    rowLimit("short")
  );

  test(
    "where no conversation file is named, is UNKNOWN and names both rows it could not fill",
    async () => {
      const { code, stdout } = await sweep([], {
        env: { SWEEP_CONVERSATION_FILE: undefined },
        rows: [
          "/api/v1/agents/{session_id}/artifacts/{slug}",
          "/api/v1/agents/{session_id}/artifacts/{slug}/versions/{number}",
        ],
      });
      for (const pattern of [
        "/api/v1/agents/{session_id}/artifacts/{slug}",
        "/api/v1/agents/{session_id}/artifacts/{slug}/versions/{number}",
      ]) {
        expect(stdout).toContain(`\n  GET ${pattern} is not filled: {session_id}: `);
      }
      expect(code).toBe(2);
    },
    rowLimit("short")
  );

  test(
    "where the member's live feed closes before its window ends, is UNKNOWN",
    async () => {
      const { code, stdout } = await sweep([], {
        feed: { live: true, closes: true },
        rows: ["/api/v1/whoami"],
      });
      expect(stdout).toContain(
        "\nUNKNOWN /ws/live: a subscribed feed ended before the observation window finished\n"
      );
      expect(stdout).not.toContain("PASS");
      expect(code).toBe(2);
    },
    rowLimit("short")
  );

  // The control of the row above: the same feed, held open for its window, passes the live check,
  // so what that row reads is the close and not a feed the sweep cannot read at all.
  test(
    "where the member's live feed is held for its window, the live check holds",
    async () => {
      const { stdout } = await sweep([], { feed: { live: true }, rows: ["/api/v1/whoami"] });
      expect(stdout).toMatch(
        /^ok {6}\/ws\/live: the member is sent 0 events in 1 s, none of TGT; the full member is sent the comment \(control\)$/m
      );
    },
    rowLimit("short")
  );

  test(
    "where the full member's live feed misses the comment, is UNKNOWN and quotes no event's text",
    async () => {
      const words = "words only a member of TGT may read";
      const { code, stdout } = await sweep([], {
        feed: { live: true, noise: words },
        rows: ["/api/v1/whoami"],
      });
      expect(stdout).toMatch(/^UNKNOWN \/ws\/live: the control did not hold: /m);
      expect(stdout).not.toContain(words);
      expect(code).toBe(2);
    },
    rowLimit("short")
  );

  test(
    "where the named conversation file is missing to the full member too, is UNKNOWN and says so",
    async () => {
      const { code, stdout } = await sweep([], {
        conversationGone: true,
        rows: ["/api/v1/agents/{session_id}/artifacts/{slug}"],
      });
      expect(stdout).toContain(
        `\n  GET /api/v1/agents/{session_id}/artifacts/{slug} is not filled: {session_id}: the full member is answered 404 for SWEEP_CONVERSATION_FILE's ${tgt.session}/${tgt.conversationFile}\n`
      );
      expect(code).toBe(2);
    },
    rowLimit("short")
  );
});

describe.skipIf(withoutTools)("the lookup", () => {
  test(
    "finds an object on any of the 50 issues it lists, the 30th included",
    async () => {
      const { stdout } = await sweep([], {
        issues: 30,
        handoffOn: "TGT-30",
        rows: ["/api/v1/whoami"],
      });
      expect(stdout).toMatch(new RegExp(`^found {3}handoff ${tgt.handoff}$`, "m"));
      expect(stdout).not.toMatch(/^missing handoff/m);
    },
    rowLimit("short")
  );
});

describe.skipIf(withoutTools)("the routes it calls", () => {
  test(
    "the member is sent every GET row of the route index",
    async () => {
      const { memberPaths, stdout } = await quietRun();
      expect(stdout).not.toMatch(/^not filled /m);
      expect(stdout).toMatch(/, 0 not filled;/);
      // Each GET pattern as the paths it fills: a wildcard is one path segment.
      const unsent = getRows.filter((pattern) => {
        const shape = new RegExp(`^${pattern.replace(/\{[^}]+\}/g, "[^/]+")}$`);
        return !memberPaths.some((path) => shape.test(decodeURIComponent(path)));
      });
      expect(unsent).toEqual([]);
    },
    rowLimit("short")
  );
});
