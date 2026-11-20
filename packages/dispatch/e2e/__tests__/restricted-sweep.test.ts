/** `e2e/acceptance/restricted-sweep.sh`, the driver of the check c-restricted-sweep: a read sweep of
 *  a deployment, as a restricted member, over every GET route of the route index and both sockets,
 *  for the objects of a project whose access list does not name the member.
 *
 *  The rows that start nothing hold the inputs it refuses, and that it fills every wildcard of the
 *  route index with a kind of object. The rest run it against stacks of the e2e harness
 *  (`restricted-sweep-stack.ts`, whose server `e2e/run-server.sh` starts), each the instrument's
 *  check of itself:
 *  - on this checkout's server, which holds a restricted member to their own projects, it passes,
 *    with rows that could have failed, every kind of object found and both sockets' controls held;
 *  - for a project the full member finds nothing of, it says UNKNOWN, never PASS;
 *  - on the build of #4177's merge base, which has no read check, it fails, and names the first
 *    route that answered a row, and both sockets;
 *  - the kind it fills each wildcard with is the kind the server's own route probes name.
 *
 *  WHY THE STACK ROWS ARE SKIPPED WITHOUT A POSTGRES PROVIDER. They start two whole stacks, each a
 *  Postgres, a Go server and the fakes, and build the merge base's server, which is minutes of
 *  work and needs `DISPATCH_E2E_PG_BIN` or `DISPATCH_E2E_PG_IMAGE` to make the databases at all
 *  (`e2e/harness-postgres.ts`), as `second-global-setup.test.ts`'s row does. Without one they
 *  would prove nothing, so they are skipped and say so. */

import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { hostTool } from "@legion/test-fixtures/host-tools";

import { rowLimit, runBounded } from "./bounded-subprocess";
import type { StackRuns } from "./restricted-sweep-stack";

const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));
const sweepScript = fileURLToPath(new URL("../acceptance/restricted-sweep.sh", import.meta.url));
const routeIndex = fileURLToPath(
  new URL("../../../envoy/internal/dispatch/api/testdata/routes.txt", import.meta.url)
);
/** The path of each GET row of the route index, in its order. */
const getRows = readFileSync(routeIndex, "utf8")
  .split("\n")
  .map((line) => line.split(" "))
  .filter(([method]) => method === "GET")
  .map(([, path]) => path ?? "");

/** The script run alone, with these variables and PATH and TMPDIR, as a child that starts nothing. */
function script(env: Readonly<Record<string, string | undefined>>) {
  return runBounded({
    argv: ["bash", sweepScript],
    bound: "short",
    cwd: dispatchPackage,
    env: {
      PATH: process.env.PATH ?? "",
      TMPDIR: tmpdir(),
      ...Object.fromEntries(Object.entries(env).filter(([, value]) => value !== undefined)),
    },
    from: import.meta.url,
  });
}

/** Inputs the script takes, which each refusal row changes one of. Port 9 is closed. */
const inputs = {
  SWEEP_ORIGIN: "http://127.0.0.1:9",
  SWEEP_PROJECT: "TGT",
  SWEEP_FULL_TOKEN: "a-full-members-token",
  SWEEP_RESTRICTED_COOKIE: "dsession=a-restricted-members-cookie",
  SWEEP_WRITE_ISSUE: "TGT-1",
};

describe("the inputs", () => {
  test.each([
    ["no origin", { SWEEP_ORIGIN: undefined }, "UNKNOWN: SWEEP_ORIGIN is required"],
    [
      "plain http to a host off this machine",
      { SWEEP_ORIGIN: "http://dispatch.example" },
      "UNKNOWN: SWEEP_ORIGIN must be https://",
    ],
    [
      "an origin with a path",
      { SWEEP_ORIGIN: "https://dispatch.example/api" },
      "UNKNOWN: SWEEP_ORIGIN must be an origin alone",
    ],
    [
      "a project key Dispatch refuses",
      { SWEEP_PROJECT: "tgt" },
      "UNKNOWN: SWEEP_PROJECT is no project key: tgt",
    ],
    [
      "no full member's token",
      { SWEEP_FULL_TOKEN: undefined },
      "UNKNOWN: SWEEP_FULL_TOKEN is required",
    ],
    [
      "a cookie with no name",
      { SWEEP_RESTRICTED_COOKIE: "a-value" },
      "UNKNOWN: SWEEP_RESTRICTED_COOKIE must be <name>=<value>",
    ],
    [
      "an issue of another project to write on",
      { SWEEP_WRITE_ISSUE: "OWN-1" },
      "UNKNOWN: SWEEP_WRITE_ISSUE is no issue of TGT: OWN-1",
    ],
    [
      "a window of no seconds",
      { SWEEP_LIVE_SECONDS: "0" },
      "UNKNOWN: SWEEP_LIVE_SECONDS must be a whole number of seconds",
    ],
  ] as const)(
    "%s is UNKNOWN, exit 2, named",
    async (_what, change, message) => {
      const { code, stdout } = await script({ ...inputs, ...change });
      expect(stdout).toStartWith(message);
      expect(code).toBe(2);
    },
    rowLimit("short")
  );

  test.skipIf(["curl", "jq", "bun"].some((tool) => hostTool(tool) === null))(
    "an origin that answers nothing is UNKNOWN, not PASS",
    async () => {
      const { code, stdout } = await script(inputs);
      expect(stdout).toContain("UNKNOWN: GET /api/v1/whoami as the member answered curl failed");
      expect(code).toBe(2);
    },
    rowLimit("short")
  );
});

/** The kinds the script fills each GET row's wildcards with (SWEEP_LIST_KINDS=1), by path. */
async function listedKinds(): Promise<Map<string, Record<string, string>>> {
  const { code, stdout } = await script({ SWEEP_LIST_KINDS: "1" });
  expect(code).toBe(4);
  return new Map(
    stdout
      .trim()
      .split("\n")
      .map((line) => {
        const [path = "", ...pairs] = line.split(" ");
        return [path, Object.fromEntries(pairs.map((pair) => pair.split("=")))];
      })
  );
}

test(
  "fills every wildcard of every GET row of the route index with a kind of object",
  async () => {
    const kinds = await listedKinds();
    expect([...kinds.keys()]).toEqual(getRows);
    const unknown = [...kinds].flatMap(([path, pairs]) =>
      Object.entries(pairs)
        .filter(([, kind]) => kind === "")
        .map(([wildcard]) => `${path} {${wildcard}}`)
    );
    expect(unknown).toEqual([]);
  },
  rowLimit("short")
);

const pgProvider = ["DISPATCH_E2E_PG_BIN", "DISPATCH_E2E_PG_IMAGE"].filter(
  (variable) => (process.env[variable] ?? "").trim() !== ""
);
const missing = [
  ...(pgProvider.length === 0
    ? ["DISPATCH_E2E_PG_BIN or DISPATCH_E2E_PG_IMAGE, naming a Postgres for the stacks"]
    : []),
  ...["go", "psql", "git", "tar", "curl", "jq", "bun"]
    .filter((tool) => hostTool(tool) === null)
    .map((tool) => `${tool} on PATH`),
];
const withoutStacks = missing.length > 0;
if (withoutStacks) {
  console.warn(
    `Skipped the rows of ${import.meta.path} that run the sweep against a stack. This run does ` +
      `not have: ${missing.join("; ")}.`
  );
}

/** One child of `restricted-sweep-stack.ts`, and what it printed. */
async function stack(which: "head" | "base"): Promise<StackRuns> {
  const { code, named, stderr, stdout } = await runBounded({
    argv: ["bun", "e2e/__tests__/restricted-sweep-stack.ts", which],
    bound: "twoStackBuild",
    cwd: dispatchPackage,
    env: {
      PATH: process.env.PATH ?? "",
      // The caller's own HOME, as `second-global-setup.test.ts` gives its child: the stack builds
      // the Go server, and a home of its own would be a cold build cache.
      HOME: process.env.HOME ?? "",
      TMPDIR: tmpdir(),
      ...Object.fromEntries(
        [...pgProvider, "DISPATCH_E2E_SWEEP_BASE"]
          .filter((variable) => process.env[variable] !== undefined)
          .map((variable) => [variable, process.env[variable]])
      ),
    },
    from: import.meta.url,
  });
  if (code !== 0) throw new Error(`${named} exited ${code}:\n${stderr.slice(-6000)}`);
  return JSON.parse(stdout.trim().split("\n").at(-1) ?? "") as StackRuns;
}

/** Both stacks, started together the first time a row asks. */
let started: Promise<{ readonly head: StackRuns; readonly base: StackRuns }> | undefined;
function stacks() {
  started ??= Promise.all([stack("head"), stack("base")]).then(([head, base]) => ({ base, head }));
  return started;
}

/** The lines of a run that judged something, by their first word. */
function linesOf(stdout: string, first: string): string[] {
  return stdout.split("\n").filter((line) => line.startsWith(first));
}

describe.skipIf(withoutStacks)("against the local stack", () => {
  test(
    "on this checkout's server the member reads nothing of TGT: PASS, with rows that could fail",
    async () => {
      const { target } = (await stacks()).head;
      const { stdout } = target;
      expect(stdout).toContain(
        "\nPASS: no route and neither socket gave the member a row of TGT\n"
      );
      expect(target.code, stdout).toBe(0);
      expect(linesOf(stdout, "FAIL"), stdout).toEqual([]);
      expect(linesOf(stdout, "UNKNOWN"), stdout).toEqual([]);
      expect(linesOf(stdout, "missing"), stdout).toEqual([]);
      // The full member finds an object of every kind the project owns.
      for (const kind of [
        "issue",
        "document",
        "block",
        "comment",
        "ask",
        "message",
        "handoff",
        "project-document",
        "near-copy-check",
      ]) {
        expect(stdout).toMatch(new RegExp(`^found   ${kind} \\S+$`, "m"));
      }
      const swept =
        /^swept {3}(\d+) GET rows: (\d+) calls judged, (\d+) can fail, (\d+) blind, (\d+) not filled;/m.exec(
          stdout
        );
      expect(swept, stdout).not.toBeNull();
      expect(Number(swept?.[1])).toBe(getRows.length);
      expect(Number(swept?.[3])).toBeGreaterThan(0);
      // Every GET row of the index is called, or named as not filled, and only the four whose
      // object no route lists, or this stack never writes, are not filled.
      for (const path of getRows) expect(stdout).toContain(` GET ${path}`);
      expect(linesOf(stdout, "not filled").map((line) => line.split(":")[0])).toEqual([
        "not filled GET /api/v1/agents/{session_id}/artifacts/{slug}",
        "not filled GET /api/v1/agents/{session_id}/artifacts/{slug}/versions/{number}",
        "not filled GET /api/v1/delivery/runs/{id}",
        "not filled GET /api/v1/going/requests/{id}/stream",
      ]);
      expect(stdout).toMatch(
        /^ok {6}\/ws\/doc\/\{room\}: http 404 .*\(control: admitted to their own document\)$/m
      );
      expect(stdout).toMatch(
        /^ok {6}\/ws\/live: the member is sent \d+ events in 5 s, none of TGT; the full member is sent the comment \(control\)$/m
      );
    },
    rowLimit({ atOnce: ["twoStackBuild", "twoStackBuild"] })
  );

  test(
    "for a project the full member finds nothing of, the run is UNKNOWN, never PASS",
    async () => {
      const { empty } = (await stacks()).head;
      expect(empty?.stdout).toContain(
        "UNKNOWN: the full member finds no issue of EMPTY, so no route can be filled with its objects"
      );
      expect(empty?.code).toBe(2);
    },
    rowLimit({ atOnce: ["twoStackBuild", "twoStackBuild"] })
  );

  test(
    "on #4177's merge base, which has no read check, the run fails and names the route and both sockets",
    async () => {
      const { target } = (await stacks()).base;
      const { stdout } = target;
      expect(stdout).toMatch(
        /^FAIL: \d+ answers gave the member what TGT holds, the first: GET \/api\/v1\/\S+: the member is answered 200 /m
      );
      expect(target.code, stdout).toBe(1);
      expect(stdout).toMatch(
        /^FAIL {4}GET \/api\/v1\/issues\/\{key\}: the member is answered 200 /m
      );
      expect(stdout).toMatch(
        /^FAIL {4}\/ws\/doc\/\{room\}: the member is admitted to the room of TGT's document/m
      );
      expect(stdout).toMatch(/^FAIL {4}\/ws\/live: the member is sent an event holding /m);
    },
    rowLimit({ atOnce: ["twoStackBuild", "twoStackBuild"] })
  );

  test(
    "fills each wildcard with the kind the server's own route probes name",
    async () => {
      const { probes } = (await stacks()).head;
      const read = JSON.parse(probes ?? "[]") as {
        method: string;
        pattern: string;
        probe: Record<string, string> | null;
      }[];
      const server = new Map(
        read
          .filter((entry) => entry.method === "GET")
          .map((entry) => [entry.pattern, entry.probe ?? {}])
      );
      const kinds = await listedKinds();
      expect(kinds.size).toBeGreaterThan(0);
      for (const [path, pairs] of kinds)
        expect([path, pairs]).toEqual([path, server.get(path) ?? {}]);
    },
    rowLimit({ atOnce: ["twoStackBuild", "twoStackBuild"] }, "short")
  );
});
