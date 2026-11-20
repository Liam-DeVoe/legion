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

/** The lookup the full member is answered, by exact path and query. `omit` leaves a kind out. */
function lookup(target: string, omit: ReadonlySet<string>): unknown {
  const author = { kind: "user", id: "carol@example.com" };
  const agent = { kind: "session", id: tgt.session };
  const answers: Record<string, unknown> = {
    "/api/v1/whoami": { kind: "agent", owner: "carol@example.com" },
    "/api/v1/projects": [{ key: "OWN" }, { key: "TGT" }],
    "/api/v1/projects/TGT/access": { project: "TGT", people: [] },
    "/api/v1/issues?project=TGT&limit=50": {
      issues: [{ key: tgt.issue, title: tgt.title, repo: "acme/tgt-repo" }],
    },
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
    [`/api/v1/issues/${tgt.issue}/handoffs`]: omit.has("handoff")
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
  return answers[target];
}

/** Whether `target`, decoded, names an object of TGT: one of its ids or keys, or the project key
 *  standing as a path segment, a query value or a reference's project. */
function namesTgt(target: string): boolean {
  return (
    tgtValues.some((value) => target.includes(value)) ||
    /(^|[/=])TGT([/&?]|$)/.test(target) ||
    target.includes(`${scheme}TGT`)
  );
}

/** How a fake serves its live feed: not at all (every upgrade refused 404), or, with `live`, a feed
 *  that starts for both callers and sends the full member each comment it is written. `closes`
 *  closes the member's feed when the comment is written, which the sweep writes only once both
 *  feeds have started, so it closes long before any window could end. */
interface FeedShape {
  readonly live?: boolean;
  readonly closes?: boolean;
}

/** A fake Dispatch on a port of its own, with `leaks` planted for the restricted member. */
function fakeDispatch(leaks: readonly Leak[], omit: ReadonlySet<string>, feed: FeedShape) {
  const memberPaths: string[] = [];
  const fullFeeds: ServerWebSocket<{ member: boolean }>[] = [];
  const memberFeeds: ServerWebSocket<{ member: boolean }>[] = [];
  let events = 0;
  const server = Bun.serve<{ member: boolean }>({
    hostname: "127.0.0.1",
    port: 0,
    async fetch(request, served) {
      const url = new URL(request.url);
      const target = decodeURIComponent(url.pathname + url.search);
      const member = request.headers.get("cookie") === memberCookie;
      const full = request.headers.get("authorization") === `Bearer ${fullToken}`;
      if (request.headers.get("upgrade") !== null) {
        if (feed.live === true && url.pathname === "/ws/live" && (member || full)) {
          if (served.upgrade(request, { data: { member } })) return undefined;
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
        const event = { id: events, issue_key: tgt.issue, project: "TGT", payload: { id } };
        for (const socket of fullFeeds) {
          socket.send(JSON.stringify({ type: "event", sub: "e", id: String(events), event }));
        }
        if (feed.closes === true) {
          for (const socket of memberFeeds) socket.close(1011, "the feed went away");
        }
        return Response.json({ id }, { status: 201 });
      }
      const answer = lookup(target, omit);
      if (answer !== undefined) return Response.json(answer);
      if (missingValues.some((value) => target.includes(value))) {
        return Response.json({ code: "NOT_FOUND", error: "not found" }, { status: 404 });
      }
      return Response.json([{ project: "TGT", issue_key: tgt.issue, path: target }]);
    },
    websocket: {
      message(socket, raw) {
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

/** What a row changes of a run: kinds the lookup leaves out, inputs (a variable set undefined is
 *  left out), the live feed, and `rows`, the GET patterns of the route index the run reads in
 *  place of the whole of it. A sweep of the whole index makes about 400 calls; a row that holds
 *  one rule reads only the rows that rule needs. */
interface RunShape {
  readonly omit?: ReadonlySet<string>;
  readonly env?: Readonly<Record<string, string | undefined>>;
  readonly feed?: FeedShape;
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
  const fake = fakeDispatch(leaks, shape.omit ?? new Set(), shape.feed ?? {});
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
    "a refusal other than 403 or 404 that names the project's object is a FAIL",
    async () => {
      const refusal = { code: "ARTIFACT_LINKED", error: `artifact belongs to issue ${tgt.issue}` };
      const { code, stdout } = await sweep(
        [at(`/api/v1/artifacts/${tgt.projectDocument}/comments`, 400, refusal)],
        { rows: ["/api/v1/artifacts/{id}/comments"] }
      );
      expect(failures(stdout)).toEqual([
        `GET /api/v1/artifacts/{id}/comments: the member is answered 400 holding ${tgt.issue}, a row of TGT: ${JSON.stringify(refusal)}`,
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
        "\nUNKNOWN /ws/live: the full member's feed did not stay subscribed: http 404 no socket here\n"
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
