/** The outside driver, `e2e/acceptance/outside-edge.ts`: what works for a person from outside,
 *  through the edge that serves them. Its step 4's rule is `outside-edge-limits.test.ts`'s.
 *
 *  The rows that start nothing hold that a run with an input missing says UNKNOWN, naming the
 *  input, and never PASS. The rest read the runs of `outside-edge-stack.ts`: one stack of the e2e
 *  harness (whose server `e2e/run-server.sh` starts under `DISPATCH_E2E_SIGNIN=google`, with
 *  passkey sign-in on at the outside origin), the edge stand-in of `outside-edge-stand-in.ts`, and
 *  the driver run against them once per case, each case a person of its own:
 *  - a person on one project passes every step, approves the tailnet leg's code beside its own,
 *    and `approve` approves one more code;
 *  - a person on a second project fails step 1, naming it (the project-leak control);
 *  - a peer that never answers fails step 3 by its timeout, naming the peer;
 *  - a threshold of 1 fails step 4, naming the host;
 *  - `revoke` reads the person's own machine-logins list, and fails each credential it cannot
 *    end there.
 *
 *  WHY THE STACK ROWS ARE SKIPPED WITHOUT A POSTGRES PROVIDER. The stack is a Postgres, a Go build
 *  of the server and the fakes, and needs `DISPATCH_E2E_PG_BIN` or `DISPATCH_E2E_PG_IMAGE` to make
 *  its database (`e2e/harness-postgres.ts`); without one they prove nothing, so they are skipped
 *  and say so. They also need Chromium and the built dashboard (`bun run build:web`), which the
 *  child refuses to run without. CI runs them in `.github/workflows/dispatch-browser.yaml`'s
 *  two-process job, which holds all three. */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { existsSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { rowLimit, runBounded } from "./bounded-subprocess";
import { type DriverRun, peer, people, type StackRuns, silentPeer } from "./outside-edge-stack";

const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));
const driver = join(dispatchPackage, "e2e/acceptance/outside-edge.ts");
const stackProgram = fileURLToPath(new URL("./outside-edge-stack.ts", import.meta.url));

/** The TSV rows a run printed, by name: `row`, `want`, `got`, verdict. */
function rowsOf(run: DriverRun): Map<string, { want: string; got: string; verdict: string }> {
  const rows = new Map<string, { want: string; got: string; verdict: string }>();
  for (const line of run.stdout.split("\n")) {
    const [row, want, got, verdict, ...rest] = line.split("\t");
    if (row === undefined || verdict === undefined || rest.length > 0) continue;
    rows.set(row, { want: want ?? "", got: got ?? "", verdict });
  }
  return rows;
}

/** The verdict line: the run's last line. */
function verdictLine(run: DriverRun): string {
  return run.stdout.trimEnd().split("\n").at(-1) ?? "";
}

describe("the inputs", () => {
  const scratch = mkdtempSync(join(tmpdir(), "outside-edge-inputs-"));
  afterAll(() => rmSync(scratch, { force: true, recursive: true }));
  const limits = join(scratch, "limits.txt");
  writeFileSync(limits, "3010 dispatch.edge.example any - 1200 60\n");
  // In the order the driver reads them, so a run missing one is refused for that one first.
  const all = {
    OUTSIDE_INVITE_URL: "https://dispatch.edge.example/enroll#a-token",
    OUTSIDE_PROJECT: "OWN",
    OUTSIDE_PEER_SESSION: peer,
    OUTSIDE_BROKER_URL: "https://secrets.edge.example",
    OUTSIDE_LISTENER_URL: "https://envoy.edge.example",
    OUTSIDE_EDGE_LIMITS: limits,
    OUTSIDE_EDGE_STATE: join(scratch, "state"),
  } as const;
  const drive = (env: Record<string, string>) =>
    runBounded({
      argv: ["bun", driver],
      bound: "short",
      cwd: dispatchPackage,
      env: { PATH: process.env.PATH ?? "", HOME: process.env.HOME ?? "", ...env },
      from: import.meta.url,
    });

  for (const name of Object.keys(all) as (keyof typeof all)[]) {
    test(
      `a run without ${name} is UNKNOWN, naming it, and starts nothing`,
      async () => {
        const run = await drive(
          Object.fromEntries(Object.entries(all).filter(([key]) => key !== name))
        );
        expect(run.stdout).toBe(
          `inputs\tevery input of the driver\t${name} is not set\tUNKNOWN\nUNKNOWN: inputs\n`
        );
        expect(run.code).toBe(2);
      },
      rowLimit("short")
    );
  }

  test(
    "an http origin is refused without OUTSIDE_EDGE_ALLOW_HTTP, and an unreadable limits file is UNKNOWN",
    async () => {
      const http = await drive({ ...all, OUTSIDE_INVITE_URL: "http://127.0.0.1:9/enroll#a-token" });
      expect(http.stdout).toContain("\tOUTSIDE_INVITE_URL must be an https URL\tUNKNOWN\n");
      expect(http.code).toBe(2);
      const unread = await drive({ ...all, OUTSIDE_EDGE_LIMITS: join(scratch, "no-such-file") });
      expect(unread.stdout).toContain("\tOUTSIDE_EDGE_LIMITS could not be read: ");
      expect(unread.code).toBe(2);
    },
    rowLimit("short", "short")
  );
});

const pgProvider = process.env.DISPATCH_E2E_PG_BIN ?? process.env.DISPATCH_E2E_PG_IMAGE;
const stackRow = pgProvider === undefined ? test.skip : test;
if (pgProvider === undefined) {
  console.log(
    "outside-edge.test.ts: the stack rows are skipped: neither DISPATCH_E2E_PG_BIN nor DISPATCH_E2E_PG_IMAGE names a Postgres for the stack"
  );
}

describe("the driver against the local stack", () => {
  let runs: StackRuns;
  beforeAll(async () => {
    if (pgProvider === undefined) return;
    if (!existsSync(join(dispatchPackage, "web/dist/index.html"))) {
      throw new Error("the stack rows need the built dashboard: run `bun run build:web` first");
    }
    const child = await runBounded({
      argv: ["bun", stackProgram],
      bound: "outsideEdgeStack",
      cwd: dispatchPackage,
      env: { ...process.env, DISPATCH_E2E_SIGNIN: "google" },
      from: import.meta.url,
    });
    const last = child.stdout.trimEnd().split("\n").at(-1) ?? "";
    if (child.code !== 0 || !last.startsWith("{")) {
      throw new Error(
        `outside-edge-stack.ts exited ${child.code}:\n${child.stdout}\n${child.stderr}`
      );
    }
    runs = JSON.parse(last) as StackRuns;
  }, rowLimit("outsideEdgeStack"));

  stackRow(
    "a person on one project passes every step, and approves the tailnet leg's code beside its own",
    () => {
      const rows = rowsOf(runs.pass);
      expect(
        [...rows.entries()].filter(([, row]) => row.verdict !== "PASS"),
        runs.pass.stdout
      ).toEqual([]);
      for (const name of ["sign-in", "projects", "device-login", "tailnet-code", "relay"]) {
        expect(rows.has(name), `${name} in\n${runs.pass.stdout}`).toBe(true);
      }
      expect(rows.get("projects")?.got).toBe("[OWN]");
      expect(runs.pass.stdout).toContain(`\nAPPROVED ${runs.tailnetCode}\n`);
      expect(rows.get("relay")?.got).toMatch(
        new RegExp(`^outside-e2e pong [0-9a-f]{16} from ${peer} on the relay socket$`)
      );
      // Step 4 judged every threshold of the file, and counted requests on both hosts.
      expect([...rows.keys()].filter((name) => name.startsWith("limit-"))).toHaveLength(10);
      expect(runs.pass.stdout).toMatch(
        new RegExp(`\\noutside-edge: peak ${runs.outsideHost} [1-9][0-9]* requests in 60 s\\n`)
      );
      expect(runs.pass.stdout).toMatch(
        new RegExp(`\\noutside-edge: peak ${runs.edgeHost} [1-9][0-9]* requests in 60 s\\n`)
      );
      expect(verdictLine(runs.pass)).toBe("PASS");
      expect(runs.pass.code).toBe(0);
      // The output carries no link, token or cookie of the run.
      expect(runs.pass.stdout).not.toMatch(
        /enroll#|dsession=|edge-login-|edge-refresh-|relay-standin-/
      );
    }
  );

  stackRow("approve approves one more code in the kept browser state", () => {
    expect(runs.approve.stdout).toBe(`APPROVED ${runs.approveCode}\n`);
    expect(runs.approve.code).toBe(0);
  });

  stackRow("a person on a second project fails step 1, naming the extra project", () => {
    const rows = rowsOf(runs.secondProject);
    expect(rows.get("sign-in")?.verdict).toBe("PASS");
    expect(rows.get("projects")).toEqual({
      want: "GET /api/v1/projects lists exactly [OWN]",
      got: "[OTHER, OWN]; listed beside it: OTHER",
      verdict: "FAIL",
    });
    expect(verdictLine(runs.secondProject)).toBe("FAIL: projects");
    expect(runs.secondProject.code).toBe(1);
  });

  stackRow("a peer that never answers fails step 3 by its timeout, naming the peer", () => {
    const rows = rowsOf(runs.silentPeer);
    expect(rows.get("device-login")?.verdict).toBe("PASS");
    expect(rows.get("relay")?.got).toBe(`no reply from ${silentPeer} within 5 s`);
    expect(rows.get("relay")?.verdict).toBe("FAIL");
    expect(verdictLine(runs.silentPeer)).toBe("FAIL: relay");
    expect(runs.silentPeer.code).toBe(1);
  });

  stackRow("a threshold of 1 fails step 4, naming the host", () => {
    const rows = rowsOf(runs.oneRequest);
    expect(rows.get("relay")?.verdict).toBe("PASS");
    const row = rows.get("limit-2000-60s");
    expect(row?.verdict).toBe("FAIL");
    expect(row?.got).toMatch(new RegExp(`^peak [2-9][0-9]* in 60 s from ${runs.edgeHost}$`));
    expect(verdictLine(runs.oneRequest)).toBe("FAIL: limit-2000-60s");
    expect(runs.oneRequest.code).toBe(1);
  });

  stackRow("revoke ends every recorded login on the person's own list but the one it keeps", () => {
    const rows = rowsOf(runs.revoke);
    // The driver's own login and the tailnet leg's; approve's is OUTSIDE_EDGE_REVOKE_KEEP's.
    const revoked = [...rows.entries()].filter(([name]) => name.startsWith("revoke-"));
    expect(revoked, runs.revoke.stdout).toHaveLength(2);
    for (const [, row] of revoked)
      expect(row).toMatchObject({ got: "listed; the revoke answered 200", verdict: "PASS" });
    expect(rows.has(`revoke-${runs.keptCredential}`)).toBe(false);
    expect(runs.revoke.code).toBe(0);
    expect(runs.keptCredential).not.toBe("");
    expect(runs.listedAfterRevoke).toEqual([runs.keptCredential]);
  });

  stackRow("t5 while the person is still listed reads UNKNOWN at its deadline, never PASS", () => {
    const rows = rowsOf({ code: 0, stdout: runs.t5Listed.tsv, stderr: "" });
    expect(rows.get("T5-cookie")).toMatchObject({
      got: "still 200 at the deadline",
      verdict: "UNKNOWN",
    });
    expect(rows.get("T5-passkey")?.verdict).toBe("UNKNOWN");
    expect(runs.t5Listed.code).toBe(2);
  });

  stackRow(
    "t5 after the owner took the person off reads the cookie's 401 and the refused passkey",
    () => {
      const rows = rowsOf({ code: 0, stdout: runs.t5Delisted.tsv, stderr: "" });
      expect(rows.get("T5-cookie")?.got, runs.t5Delisted.stdout).toMatch(/^401 at=[0-9]{10}$/);
      expect(rows.get("T5-cookie")?.verdict).toBe("PASS");
      expect(rows.get("T5-passkey")?.got, runs.t5Delisted.stdout).toMatch(
        /^refused at=[0-9]{10}: /
      );
      expect(rows.get("T5-passkey")?.verdict).toBe("PASS");
      expect(runs.t5Delisted.code).toBe(0);
    }
  );

  test("the stack's people are four, one per case", () => {
    expect(new Set(Object.values(people)).size).toBe(4);
  });
});
