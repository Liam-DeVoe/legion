/** `e2e/acceptance/restricted-sweep.sh`, the driver of the check c-restricted-sweep: a read sweep of
 *  a deployment, as a restricted member, over every GET route of the route index and both sockets,
 *  for the objects of a project whose access list does not name the member.
 *
 *  The rows that start nothing hold the inputs it refuses, that it fills every wildcard of the
 *  route index with a kind of object, and which commit the control builds (`noReadCheckBase`, on
 *  repositories of the rows' own). The rest run it against stacks of the e2e harness
 *  (`restricted-sweep-stack.ts`, whose server `e2e/run-server.sh` starts), each the instrument's
 *  check of itself:
 *  - on this checkout's server, which holds a restricted member to their own projects, it passes,
 *    with rows that could have failed, every kind of object found and both sockets' controls held;
 *  - for a project the full member finds nothing of, it says UNKNOWN, never PASS;
 *  - on a build of the newest commit with no read check, it fails, and names the first route that
 *    answered a row, and both sockets;
 *  - the kind it fills each wildcard with is the kind the server's own route probes name.
 *
 *  The control's commit is read from git: the merge base of HEAD and DISPATCH_E2E_SWEEP_BASE_BRANCH
 *  (`origin/main` by default; the branch the read check's pull request targets while it is open),
 *  or the commit DISPATCH_E2E_SWEEP_BASE names.
 *
 *  WHY THE STACK ROWS ARE SKIPPED WITHOUT A POSTGRES PROVIDER. They start two whole stacks, each a
 *  Postgres, a Go server and the fakes, and build the control's server, which is minutes of work
 *  and needs `DISPATCH_E2E_PG_BIN` or `DISPATCH_E2E_PG_IMAGE` to make the databases at all
 *  (`e2e/harness-postgres.ts`), as `second-global-setup.test.ts`'s row does. Without one they
 *  would prove nothing, so they are skipped and say so. CI's `dispatch-project-access` job names
 *  one, and the branch the control's commit is read from
 *  (`.github/workflows/ci-job-dispatch-project-access.yaml`), so it runs them. */

import { describe, expect, test } from "bun:test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { hostTool } from "@legion/test-fixtures/host-tools";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";

import { rowLimit, runBounded } from "./bounded-subprocess";
import { noReadCheckBase, printedRuns, type StackRuns } from "./restricted-sweep-stack";

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
  .map(([, path]) => path || noPath());

/** Refuses a GET row of the route index with no path: read as "", it would match a listing's
 *  empty line and a call's ` GET ` alike, so no row below could fail on it. */
function noPath(): never {
  throw new Error(`${routeIndex} has a GET row with no path`);
}

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
      "neither a full member's token nor a fills file",
      { SWEEP_FULL_TOKEN: undefined },
      "UNKNOWN: SWEEP_FULL_TOKEN is required",
    ],
    [
      "a mode the sweep has none of",
      { SWEEP_MODE: "runner" },
      "UNKNOWN: SWEEP_MODE must be fills or writer",
    ],
    [
      "fills mode with no member named",
      { SWEEP_MODE: "fills", SWEEP_FILLS_FILE: "fills.tsv" },
      "UNKNOWN: SWEEP_MEMBER is required in fills mode",
    ],
    [
      "fills mode with no file to write",
      { SWEEP_MODE: "fills", SWEEP_MEMBER: "bob@outside.example" },
      "UNKNOWN: SWEEP_FILLS_FILE is required in fills mode",
    ],
    [
      "a cookie with no name",
      { SWEEP_RESTRICTED_COOKIE: "a-value" },
      "UNKNOWN: SWEEP_RESTRICTED_COOKIE must be <name>=<value>",
    ],
    [
      "a full member's cookie with no name",
      { SWEEP_FULL_COOKIE: "a-value" },
      "UNKNOWN: SWEEP_FULL_COOKIE must be <name>=<value>",
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

/** A runner's fills, every one read before the first request: those it cannot read are refused by
 *  name before it reaches the deployment, at a closed port here, and the last row, fills it reads,
 *  is their control: it reaches the port. */
describe("a runner's fills", () => {
  const directories = temporaryDirectories("restricted-sweep-runner-fills");
  const now = () => Math.floor(Date.now() / 1000);
  /** Fills of TGT for bob made at MADE (Unix seconds), each line of `more` after them. */
  const made = (madeAt: number, more: readonly string[] = []) =>
    [
      "restricted-sweep-fills\t1",
      "project\tTGT",
      `made_at\t${madeAt}`,
      "member\tbob@outside.example",
      "own\tOWN",
      ...more,
    ]
      .map((line) => `${line}\n`)
      .join("");
  /** A runner's inputs with TEXT as its fills file, and no full member's credential. */
  const runnerWith = (text: string | undefined) => {
    const file = join(directories(), "fills.tsv");
    if (text !== undefined) writeFileSync(file, text);
    return {
      SWEEP_ORIGIN: "http://127.0.0.1:9",
      SWEEP_PROJECT: "TGT",
      SWEEP_RESTRICTED_COOKIE: "dsession=a-restricted-members-cookie",
      SWEEP_FILLS_FILE: file,
    };
  };

  test.each([
    [
      "no file at the path",
      undefined,
      {},
      "UNKNOWN: SWEEP_FILLS_FILE names no file this run can read",
    ],
    ["an empty file", "", {}, "UNKNOWN: SWEEP_FILLS_FILE is empty"],
    [
      "fills of another version",
      "restricted-sweep-fills\t2\nproject\tTGT\n",
      {},
      "UNKNOWN: SWEEP_FILLS_FILE holds no fills of this sweep's version 1",
    ],
    [
      "fills of another project",
      "restricted-sweep-fills\t1\nproject\tOWN\n",
      {},
      "UNKNOWN: SWEEP_FILLS_FILE holds the fills of project OWN, not of TGT",
    ],
    [
      "fills older than 55 minutes",
      "stale",
      {},
      "UNKNOWN: SWEEP_FILLS_FILE was made 56 minutes ago, past 55",
    ],
    [
      "a fill the sweep does not read",
      "unread",
      {},
      "UNKNOWN: SWEEP_FILLS_FILE holds a fill this sweep does not read: bogus",
    ],
    [
      "a full member's token beside the fills",
      "fresh",
      { SWEEP_FULL_TOKEN: "a-full-members-token" },
      "UNKNOWN: a runner holds no full member's credential",
    ],
    [
      "a full member's cookie beside the fills",
      "fresh",
      { SWEEP_FULL_COOKIE: "dsession=a-full-members-cookie" },
      "UNKNOWN: a runner holds no full member's credential",
    ],
  ] as const)(
    "%s is UNKNOWN, exit 2, named",
    async (_what, text, change, message) => {
      const contents =
        text === "stale"
          ? made(now() - 56 * 60 - 30)
          : text === "unread"
            ? made(now(), ["bogus\tvalue"])
            : text === "fresh"
              ? made(now())
              : text;
      const { code, stdout } = await script({ ...runnerWith(contents), ...change });
      expect(stdout).toStartWith(message);
      expect(code).toBe(2);
    },
    rowLimit("short")
  );

  test.skipIf(["curl", "jq", "bun"].some((tool) => hostTool(tool) === null))(
    "fresh fills of the project are read, and the run goes on to the deployment",
    async () => {
      const { code, stdout } = await script(runnerWith(made(now())));
      expect(stdout).toContain("UNKNOWN: GET /api/v1/whoami as the member answered curl failed");
      expect(code).toBe(2);
    },
    rowLimit("short")
  );
});

/** The kinds the script fills each GET row's wildcards with (SWEEP_LIST_KINDS=1), by path. A pair
 *  with no `=` is refused, naming its line: read as a wildcard of no kind at all, it would pass the
 *  row below, which looks for an empty kind. */
async function listedKinds(): Promise<Map<string, Record<string, string>>> {
  const { code, stdout } = await script({ SWEEP_LIST_KINDS: "1" });
  expect(code).toBe(4);
  return new Map(
    stdout
      .trim()
      .split("\n")
      .map((line): [string, Record<string, string>] => {
        const [path = "", ...pairs] = line.split(" ");
        const kinds = pairs.map((pair): [string, string] => {
          const equals = pair.indexOf("=");
          if (equals < 0) throw new Error(`SWEEP_LIST_KINDS printed ${pair}, with no "=": ${line}`);
          return [pair.slice(0, equals), pair.slice(equals + 1)];
        });
        return [path, Object.fromEntries(kinds)];
      })
  );
}

test(
  "fills every wildcard of every GET row of the route index with a kind of object",
  async () => {
    const kinds = await listedKinds();
    expect([...kinds.keys()]).toEqual(getRows);
    // Each wildcard is read from the path itself, so a row the script lists with fewer pairs than
    // its path has wildcards fails here too.
    const unknown = [...kinds].flatMap(([path, pairs]) =>
      [...path.matchAll(/\{([^}]+)\}/g)]
        .map(([, wildcard = ""]) => wildcard)
        .filter((wildcard) => (pairs[wildcard] ?? "") === "")
        .map((wildcard) => `${path} {${wildcard}}`)
    );
    expect(unknown).toEqual([]);
  },
  rowLimit("short")
);

/** The GitHub proxy's `readable` expression (githubapi/proxy.go), the only reads it forwards. */
const proxySource = fileURLToPath(
  new URL("../../../envoy/internal/dispatch/githubapi/proxy.go", import.meta.url)
);

test(
  "reads every path the GitHub proxy forwards, each as its expression names it",
  async () => {
    const expression =
      /var readable = regexp\.MustCompile\(`\^repos\/[^`]*?\/\(\?:([^`]*)\)\$`\)/.exec(
        readFileSync(proxySource, "utf8")
      );
    expect(expression, `${proxySource} holds no readable expression this row reads`).not.toBeNull();
    // Each alternative, its number and its commit written as the script fills them.
    const forwarded = (expression?.[1] ?? "").split("|").map((alternative) =>
      alternative
        .replace(/^\((.*)\)$/, "$1")
        .replace("[0-9]+", "{number}")
        .replace(/\[0-9A-Fa-f\]\{\d+,\d+\}/, "{sha}")
    );
    const { code, stdout } = await script({ SWEEP_LIST_PROXY: "1" });
    expect(code).toBe(4);
    // The workflow-runs read carries the one commit its query must name.
    expect(stdout.trim().split("\n")).toEqual(
      forwarded.map((path) => (path === "actions/runs" ? "actions/runs?head_sha={sha}" : path))
    );
  },
  rowLimit("short")
);

const repositories = temporaryDirectories("restricted-sweep-base");
/** The files `noReadCheckBase` reads a tree by: the read check's own table, and a migration that
 *  gives people a member kind. */
const readCheck = "packages/envoy/internal/dispatch/api/read_classes.go";
const accessMigration = "packages/envoy/internal/dispatch/store/migrations/0001_people.up.sql";
const restrictedMembers = {
  [accessMigration]: "alter table people add column member_kind text;\n",
};
const theReadCheck = { [readCheck]: "package api\n" };

/** A repository of the row's own. Its git reads no configuration of this machine, and each commit
 *  is made with `commit-tree` and moved onto HEAD with `update-ref`, as
 *  `project-access-blob-base.test.ts` makes its own, so no hook a machine's git runs on a commit
 *  runs here. */
class Repository {
  readonly directory = repositories();

  constructor() {
    mkdirSync(this.directory, { recursive: true });
    this.git("init", "-q", "-b", "main");
  }

  git(...args: string[]): string {
    const run = Bun.spawnSync(["git", "-C", this.directory, ...args], {
      env: {
        GIT_AUTHOR_EMAIL: "test@example.com",
        GIT_AUTHOR_NAME: "test",
        GIT_COMMITTER_EMAIL: "test@example.com",
        GIT_COMMITTER_NAME: "test",
        GIT_CONFIG_GLOBAL: "/dev/null",
        GIT_CONFIG_NOSYSTEM: "1",
        PATH: process.env.PATH ?? "",
      },
      stderr: "pipe",
      stdout: "pipe",
    });
    if (run.exitCode !== 0) throw new Error(`git ${args.join(" ")}: ${run.stderr.toString()}`);
    return run.stdout.toString().trim();
  }

  /** A commit on top of HEAD that writes `files`, answered by its sha. */
  commit(message: string, files: Record<string, string>): string {
    for (const [path, text] of Object.entries(files)) {
      mkdirSync(dirname(join(this.directory, path)), { recursive: true });
      writeFileSync(join(this.directory, path), text);
    }
    this.git("add", "-A");
    const tree = this.git("write-tree");
    const parent = Bun.spawnSync([
      "git",
      "-C",
      this.directory,
      "rev-parse",
      "--verify",
      "--quiet",
      "HEAD",
    ])
      .stdout.toString()
      .trim();
    const commit = this.git(
      "commit-tree",
      tree,
      ...(parent === "" ? [] : ["-p", parent]),
      "-m",
      message
    );
    this.git("update-ref", "HEAD", commit);
    return commit;
  }
}

describe.skipIf(hostTool("git") === null)("the commit the control builds", () => {
  test("is the merge base of HEAD and the branch, which has restricted members and no read check", () => {
    const repository = new Repository();
    const base = repository.commit("restricted members", restrictedMembers);
    repository.git("branch", "below");
    repository.commit("the read check", theReadCheck);
    expect(noReadCheckBase(repository.directory, undefined, "below")).toBe(base);
  });

  test("is, once a squash merge put the read check on the branch, the commit before it", () => {
    const repository = new Repository();
    const base = repository.commit("restricted members", restrictedMembers);
    repository.commit("the read check, squashed", theReadCheck);
    repository.commit("later work", { "README.md": "later\n" });
    repository.git("branch", "below");
    repository.commit("this change", { "NOTES.md": "this change\n" });
    expect(noReadCheckBase(repository.directory, undefined, "below")).toBe(base);
  });

  test("is refused, naming both variables, where the merge base has no restricted members", () => {
    const repository = new Repository();
    const base = repository.commit("before restricted members", { "README.md": "before\n" });
    repository.git("branch", "below");
    repository.commit("restricted members and the read check", {
      ...restrictedMembers,
      ...theReadCheck,
    });
    expect(() => noReadCheckBase(repository.directory, undefined, "below")).toThrow(
      new RegExp(
        `${base}, holds no restricted members .*DISPATCH_E2E_SWEEP_BASE.*DISPATCH_E2E_SWEEP_BASE_BRANCH`
      )
    );
  });

  test("is what DISPATCH_E2E_SWEEP_BASE names, and refused where that holds the read check", () => {
    const repository = new Repository();
    const base = repository.commit("restricted members", restrictedMembers);
    const checked = repository.commit("the read check", theReadCheck);
    expect(noReadCheckBase(repository.directory, base, "origin/nowhere")).toBe(base);
    expect(() => noReadCheckBase(repository.directory, checked, "origin/nowhere")).toThrow(
      `${checked}, holds the read check`
    );
  });

  test("is refused where the branch names no commit this clone holds", () => {
    const repository = new Repository();
    repository.commit("restricted members", restrictedMembers);
    expect(() => noReadCheckBase(repository.directory, undefined, "origin/nowhere")).toThrow(
      "origin/nowhere names no commit this clone holds"
    );
  });
});

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
async function stack<Which extends keyof StackRuns>(which: Which): Promise<StackRuns[Which]> {
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
        [...pgProvider, "DISPATCH_E2E_SWEEP_BASE", "DISPATCH_E2E_SWEEP_BASE_BRANCH"]
          .filter((variable) => process.env[variable] !== undefined)
          .map((variable) => [variable, process.env[variable]])
      ),
    },
    from: import.meta.url,
  });
  if (code !== 0) throw new Error(`${named} exited ${code}:\n${stderr.slice(-6000)}`);
  const line = stdout.trim().split("\n").at(-1) ?? "";
  const runs = printedRuns(which, line);
  if (runs === undefined) {
    throw new Error(
      `${named} printed no runs of ${which} as its last line: ${line.slice(0, 2000)}`
    );
  }
  return runs;
}

/** The three stacks, started together the first time a row asks. */
let started: Promise<StackRuns> | undefined;
function stacks(): Promise<StackRuns> {
  started ??= Promise.all([stack("head"), stack("base"), stack("keyed")]).then(
    ([head, base, keyed]) => ({ base, head, keyed })
  );
  return started;
}

/** One row of the server's route probes (`RouteProbe` of `route_probes.go`), as the probe row
 *  reads it. */
interface RouteProbe {
  readonly method: string;
  readonly pattern: string;
  readonly probe: Readonly<Record<string, string>> | null;
}

/** Whether `value` is a route probe: a method, a pattern, and a probe that is null or names a
 *  kind for each wildcard. */
function isRouteProbe(value: unknown): value is RouteProbe {
  if (typeof value !== "object" || value === null) return false;
  if (!("method" in value && "pattern" in value && "probe" in value)) return false;
  const { method, pattern, probe } = value;
  return (
    typeof method === "string" &&
    typeof pattern === "string" &&
    (probe === null ||
      (typeof probe === "object" &&
        !Array.isArray(probe) &&
        Object.values(probe).every((kind) => typeof kind === "string")))
  );
}

/** The kind the server's route probes fill each wildcard of each GET pattern with, read from
 *  `printed`, what `routeprobes` printed. A row the tables give no probe reads `{}`, as a row that
 *  names no wildcard does. */
function serverKinds(printed: string): Map<string, Readonly<Record<string, string>>> {
  const read: unknown = JSON.parse(printed);
  if (!Array.isArray(read)) throw new Error(`the route probes are no JSON array: ${printed}`);
  const rows: readonly unknown[] = read;
  const kinds = new Map<string, Readonly<Record<string, string>>>();
  for (const row of rows) {
    if (!isRouteProbe(row))
      throw new Error(`a route probe of another shape: ${JSON.stringify(row)}`);
    if (row.method === "GET") kinds.set(row.pattern, row.probe ?? {});
  }
  return kinds;
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
      // The full member finds an object of every kind a route names.
      for (const kind of [
        "going-run",
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
      expect(stdout).toMatch(
        /^found {3}person \S+, session \S+, repository \S+, priority \S+, broadcast \S+, delivery run \d+$/m
      );
      expect(stdout).not.toMatch(/^found .* none(,|$)/m);
      // The worker session wrote only in TGT, so it marks a row of TGT; carol also works in OWN,
      // which bob sees, so she does not.
      expect(stdout).toMatch(/^found {3}actor ses-tgt-worker$/m);
      expect(stdout).not.toMatch(/^found {3}actor carol@example\.com$/m);
      expect(stdout).toMatch(/^found {3}conversation-file ses-tgt-worker\/\S+$/m);
      expect(stdout).toMatch(/^ok {6}GET \/api\/v1\/agents: 200, no row of TGT \(can fail\)$/m);
      // carol's direct message to the worker, by the session's list and by its own routes.
      expect(stdout).toMatch(/^found {3}direct-message [0-9a-f-]{36}$/m);
      expect(stdout).toMatch(
        /^ok {6}GET \/api\/v1\/agents\/\{session_id\}\/messages: .*\(can fail\)$/m
      );
      expect(stdout).toMatch(
        /^ok {6}GET \/api\/v1\/messages\/\{id\} \(a direct message\): .*\(can fail\)$/m
      );
      // Every read the GitHub proxy forwards, of the repository only TGT's issues resolve to.
      expect(stdout).toMatch(/^found {3}proxy-repository acme\/tgt-repo$/m);
      for (const shape of [
        "pulls/{number}",
        "issues/{number}",
        "commits/{sha}/check-runs",
        "actions/runs?head_sha={sha}",
      ]) {
        expect(stdout).toMatch(
          new RegExp(
            `^ok {6}GET /api/github/rest/repos/\\{owner\\}/\\{repo\\}/${shape.replace(/[{}?]/g, "\\$&")}: .*\\(can fail\\)$`,
            "m"
          )
        );
      }
      const swept =
        /^swept {3}(\d+) GET rows: (\d+) calls judged, (\d+) can fail, (\d+) blind, (\d+) not filled;/m.exec(
          stdout
        );
      expect(swept, stdout).not.toBeNull();
      expect(Number(swept?.[1])).toBe(getRows.length);
      expect(Number(swept?.[3])).toBeGreaterThan(0);
      // Every GET row of the index is judged, each under its own label, and none is left unfilled.
      expect(Number(swept?.[5])).toBe(0);
      expect(linesOf(stdout, "not filled")).toEqual([]);
      for (const path of getRows) {
        expect(stdout).toMatch(new RegExp(`^ok {6}GET ${path.replace(/[{}?]/g, "\\$&")}: `, "m"));
      }
      expect(stdout).toMatch(
        /^ok {6}\/ws\/doc\/\{room\}: http 404 .*\(control: admitted to their own document\)$/m
      );
      expect(stdout).toMatch(
        /^ok {6}\/ws\/live: the member is sent \d+ events in 5 s, none of TGT; the full member is sent the comment \(control\)$/m
      );
    },
    rowLimit({ atOnce: ["twoStackBuild", "twoStackBuild", "twoStackBuild"] })
  );

  // The route index's text names an example issue key (the search route's description): a target
  // keyed as that key's project reads the index, whose prose names no row of it, so the run passes.
  test(
    "for a target keyed as the route index's example issue key, the run passes",
    async () => {
      const { key, target } = (await stacks()).keyed;
      expect(key).toMatch(/^[A-Z][A-Z0-9]{1,9}$/);
      expect(target.stdout).toContain(`\nok      GET /api/v1: 200, no row of ${key} (`);
      expect(target.stdout).toContain(
        `\nPASS: no route and neither socket gave the member a row of ${key}\n`
      );
      expect(target.code, target.stdout).toBe(0);
    },
    rowLimit({ atOnce: ["twoStackBuild", "twoStackBuild", "twoStackBuild"] })
  );

  test(
    "for a project the full member finds nothing of, the run is UNKNOWN, never PASS",
    async () => {
      const { empty } = (await stacks()).head;
      expect(empty.stdout).toContain(
        "UNKNOWN: the full member finds no issue of EMPTY, so no route can be filled with its objects"
      );
      expect(empty.code).toBe(2);
    },
    rowLimit({ atOnce: ["twoStackBuild", "twoStackBuild", "twoStackBuild"] })
  );

  // The split, as the outside acceptance runs it: the fills written where the full member's token
  // is, a runner with the member's cookie alone, and a writer beside it, stopped by its pid once
  // the runner ends. The runner's live check holds only once a write the writer saw delivered
  // falls strictly inside the window the runner read, which this row joins as the acceptance's
  // tailnet leg does: as `YYYY-MM-DDTHH:MM:SSZ` strings.
  test(
    "split into fills and a runner with no full member's credential, the run passes, and a delivered write falls inside its window",
    async () => {
      const { fills, runner, writer } = (await stacks()).head.split;
      expect(fills.stdout.trimEnd().split("\n").at(-1), fills.stdout).toStartWith("FILLS: ");
      expect(fills.code).toBe(0);
      expect(runner, fills.stdout).toBeDefined();
      const runnerOut = runner?.stdout ?? "";
      expect(runnerOut.trimEnd().split("\n").at(-1), runnerOut).toStartWith(
        "PASS: no route and neither socket gave the member a row of TGT; the live check holds once a writer's delivered write falls inside the live window "
      );
      expect(runner?.code, runnerOut).toBe(0);
      expect(linesOf(runnerOut, "not filled")).toEqual([]);
      const window = /^live window (\S+) (\S+)$/m.exec(runnerOut);
      expect(window, runnerOut).not.toBeNull();
      const [w0 = "", w1 = ""] = [window?.[1], window?.[2]];
      const writes = (writer?.stdout ?? "").split("\n").filter((line) => line.startsWith("write "));
      expect(writes.length, writer?.stdout).toBeGreaterThan(0);
      expect(
        writes.filter((line) => !line.endsWith(" delivered")),
        writer?.stdout
      ).toEqual([]);
      const inside = writes
        .map((line) => line.split(" ")[1] ?? "")
        .filter((stamp) => stamp > w0 && stamp < w1);
      expect(inside.length, `${writer?.stdout}\nwindow ${w0} ${w1}`).toBeGreaterThan(0);
    },
    rowLimit({ atOnce: ["twoStackBuild", "twoStackBuild", "twoStackBuild"] })
  );

  test(
    "split into fills and a runner on a build with no read check, the runner fails and names the route and the live socket",
    async () => {
      const { fills, runner } = (await stacks()).base.split;
      expect(fills.code, fills.stdout).toBe(0);
      const runnerOut = runner?.stdout ?? "";
      expect(runnerOut.trimEnd().split("\n").at(-1), runnerOut).toMatch(
        /^FAIL: \d+ answers gave the member what TGT holds, the first: GET \/api\/v1\/\S+: the member is answered 200 /
      );
      expect(runner?.code, runnerOut).toBe(1);
      // The writer's comments reach the member's feed on a build with no read check.
      expect(runnerOut).toMatch(/^FAIL {4}\/ws\/live: the member is sent an event holding /m);
    },
    rowLimit({ atOnce: ["twoStackBuild", "twoStackBuild", "twoStackBuild"] })
  );

  test(
    "on a build with no read check, the run fails and names the route and both sockets",
    async () => {
      const { base, target } = (await stacks()).base;
      const { stdout } = target;
      expect(base, "the child names the commit it built").toMatch(/^[0-9a-f]{40}$/);
      expect(stdout).toMatch(
        /^FAIL: \d+ answers gave the member what TGT holds, the first: GET \/api\/v1\/\S+: the member is answered 200 /m
      );
      expect(target.code, stdout).toBe(1);
      // The verdict names the first of the FAIL lines, in the route index's order.
      const first = linesOf(stdout, "FAIL    ")[0]?.slice("FAIL    ".length);
      expect(first, stdout).toMatch(/^GET \/api\/v1\//);
      expect(stdout).toContain(`, the first: ${first}\n`);
      expect(stdout).toMatch(
        /^FAIL {4}GET \/api\/v1\/issues\/\{key\}: the member is answered 200 /m
      );
      expect(stdout).toMatch(
        /^FAIL {4}\/ws\/doc\/\{room\}: the member is admitted to the room of TGT's document/m
      );
      expect(stdout).toMatch(/^FAIL {4}\/ws\/live: the member is sent an event holding /m);
      // The agents list names the worker session, which wrote only in TGT: R4's clause for
      // sessions, which a run that marked no session could not fail.
      expect(stdout).toMatch(
        /^FAIL {4}GET \/api\/v1\/agents: the member is answered 200 holding ses-tgt-worker, a row of TGT: /m
      );
      // A direct message to a session of TGT alone, which the merge base lists to every caller.
      expect(stdout).toMatch(
        /^FAIL {4}GET \/api\/v1\/agents\/\{session_id\}\/messages: the member is answered 200 holding [0-9a-f-]{36}, a row of TGT: /m
      );
      // The GitHub proxy forwards the member's read of TGT's repository, a pull request it holds.
      expect(stdout).toMatch(
        /^FAIL {4}GET \/api\/github\/rest\/repos\/\{owner\}\/\{repo\}\/pulls\/\{number\}: the member is answered 200 for TGT's repository, where one the App is not installed on is answered /m
      );
    },
    rowLimit({ atOnce: ["twoStackBuild", "twoStackBuild", "twoStackBuild"] })
  );

  test(
    "fills each wildcard with the kind the server's own route probes name",
    async () => {
      const server = serverKinds((await stacks()).head.probes);
      const kinds = await listedKinds();
      expect(kinds.size).toBeGreaterThan(0);
      // Every row the script lists is a row of the server's table, so a row missing from the
      // probes fails, by name, rather than reading as one that names no wildcard.
      for (const [path, pairs] of kinds) {
        expect(server.has(path), `${path} is no GET row of the server's route probes`).toBe(true);
        expect([path, pairs]).toEqual([path, server.get(path) ?? {}]);
      }
    },
    rowLimit({ atOnce: ["twoStackBuild", "twoStackBuild", "twoStackBuild"] }, "short")
  );
});
