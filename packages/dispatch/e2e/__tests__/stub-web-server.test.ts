/** Which runs of this config get the stubbed layer's static file server, and that the server
 *  starts whatever program holds the port this suite used to fix.
 *
 *  `globalSetup` starts `bun e2e/stub/static-server.ts` for a run that holds a stubbed row
 *  (e2e/stub/server.ts). Two jobs of `.github/workflows/dispatch-browser.yaml` run this config
 *  without a stubbed row, and one of them cannot start that command at all: `visual` runs
 *  Playwright inside the pinned container through `e2e/visual-run.sh`, and that image carries the
 *  three browser engines and node, not bun. A run that started the server there would die before
 *  its first row, and the pull-request lane skips `visual`, so no run on a pull request would say
 *  so. So the first row holds the config to its rule: a run that selects no stub project starts no
 *  server.
 *
 *  The server binds port 0 and the run reads its port from the server's own line. Before, it was
 *  Playwright's `webServer` on the fixed port 4319: Playwright tested the URL, then started the
 *  server, which bound the port later. So a second program on 4319 failed the run, either by
 *  answering the test (`http://127.0.0.1:4319/index.html is already used`) or by holding the port
 *  without listening, so the test found it free and the bind then failed (`Failed to start
 *  server. Is port 4319 in use?`). The second and third rows hold 4319 in those two ways.
 *
 *  Each row runs the real Playwright runner over this config with a filter that matches no row and
 *  `--pass-with-no-tests`: the hooks still run, so the server starts and stops, and no browser
 *  starts. The server serves a directory of this test's own (`DISPATCH_E2E_STUB_DIST`), so the
 *  rows need no build of `web/dist`. */

import { afterAll, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import type { Server } from "bun";

import { stubListening } from "../stub/origin";

const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));

/** The port the stubbed layer was served on before its server bound port 0. */
const oldStubPort = 4319;

/** One run of the Playwright runner, its hooks and nothing else: about three seconds. */
const runTimeout = 60_000;

const dist = mkdtempSync(join(tmpdir(), "stub-web-server-dist-"));
writeFileSync(join(dist, "index.html"), "<!doctype html><title>stub-web-server.test.ts</title>\n");
afterAll(() => rmSync(dist, { force: true, recursive: true }));

/** The line the static file server prints once it serves. */
const listening = new RegExp(`${stubListening} http://127\\.0\\.0\\.1:(\\d+)/ over (.+)`, "g");

interface Run {
  readonly code: number;
  readonly output: string;
  /** The port on each listening line of the static file server, in order. */
  readonly served: readonly { readonly port: number; readonly dist: string }[];
}

/** A run of the config with `projects` that runs no row. `env` is all the environment it gets on
 *  top of `HOME` and `PATH`, so a variable of the machine's cannot change what it does. */
async function runOf(projects: readonly string[], env: Record<string, string> = {}): Promise<Run> {
  const child = Bun.spawn(
    [
      "bunx",
      "playwright",
      "test",
      "--config",
      "e2e/playwright.config.ts",
      ...projects.map((project) => `--project=${project}`),
      "--grep",
      "a title no row of this suite holds, 5d4c0a",
      "--pass-with-no-tests",
    ],
    {
      cwd: dispatchPackage,
      env: {
        DISPATCH_E2E_STUB_DIST: dist,
        HOME: process.env.HOME ?? "",
        PATH: process.env.PATH ?? "",
        ...env,
      },
      stderr: "pipe",
      stdout: "pipe",
    }
  );
  const [code, stderr, stdout] = await Promise.all([
    child.exited,
    new Response(child.stderr).text(),
    new Response(child.stdout).text(),
  ]);
  const output = `${stdout}${stderr}`;
  const served = [...output.matchAll(listening)].map((match) => ({
    dist: match[2].trim(),
    port: Number(match[1]),
  }));
  return { code, output, served };
}

/** Asserts that a run of the stubbed layer passed with one static file server, which served this
 *  test's directory on a port other than `held`. */
function expectServedBeside(run: Run, held: number): void {
  expect(run.code, `the run of the stubbed layer exited ${run.code}:\n${run.output}`).toBe(0);
  expect(
    run.served,
    `the run printed no single listening line of the server:\n${run.output}`
  ).toHaveLength(1);
  expect(run.served[0].dist).toBe(dist);
  expect(run.served[0].port).not.toBe(held);
}

test(
  "a run that selects no stub project starts no static file server",
  async () => {
    // A run against servers already up, as `visual` is (e2e/visual-run.sh), so the hook starts no
    // stack either and the row needs no Postgres.
    const run = await runOf(["visual", "visual-phone"], { DISPATCH_E2E_REUSE_SERVERS: "1" });
    expect(run.code, `the run exited ${run.code}:\n${run.output}`).toBe(0);
    expect(
      run.served,
      "The containerised `visual` job runs this config with no bun on its PATH, so a run that " +
        "holds no stubbed row must start no static file server."
    ).toEqual([]);
  },
  runTimeout
);

test(
  "the stubbed layer is served while another program serves on the port it used to fix",
  async () => {
    let rival: Server<undefined> | undefined;
    try {
      rival = Bun.serve({
        fetch: () => new Response("another checkout's page\n"),
        hostname: "127.0.0.1",
        port: oldStubPort,
      });
    } catch (error) {
      // Something on this machine already holds the port, which is the case this row is about.
      if (!(error instanceof Error && "code" in error && error.code === "EADDRINUSE")) throw error;
    }
    try {
      expectServedBeside(await runOf(["stub", "stub-phone"]), oldStubPort);
    } finally {
      rival?.stop(true);
    }
  },
  runTimeout
);

test(
  "the stubbed layer is served while another program holds, without listening, the port it used to fix",
  async () => {
    // A socket bound and never listening: a test of the port finds nothing there, and a bind of
    // it fails. That is a port taken between a test and a bind. Python binds without listening;
    // Bun and node bind only to listen.
    const rival = Bun.spawn(
      [
        "python3",
        "-c",
        "import socket, sys\n" +
          "s = socket.socket()\n" +
          "try:\n" +
          `    s.bind(("127.0.0.1", ${oldStubPort}))\n` +
          "    print('held', flush=True)\n" +
          "except OSError as error:\n" +
          "    print(f'taken {error}', flush=True)\n" +
          "sys.stdin.read()\n",
      ],
      { stdin: "pipe", stdout: "pipe" }
    );
    try {
      const reader = rival.stdout.getReader();
      const first = new TextDecoder().decode((await reader.read()).value);
      reader.releaseLock();
      // `taken`: another program already holds the port, which is the case this row is about.
      expect(first).toMatch(/^(held|taken .*Address already in use)/);
      expectServedBeside(await runOf(["stub", "stub-phone"]), oldStubPort);
    } finally {
      rival.kill();
      await rival.exited;
    }
  },
  runTimeout
);
