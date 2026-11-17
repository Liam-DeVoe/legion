/** The image provider's Postgres serves though another program holds the host ports Docker's
 *  allocator would choose next (pull request #4236, acceptance finding D2).
 *
 *  Requirement R2 of that pull request: "For a program that cannot take a socket, the program
 *  binds a port that the kernel chooses. The stack reads the port from the program: from its
 *  output, or from the kernel's list of the sockets that the process holds."
 *
 *  Before that pull request, the image provider published `127.0.0.1::5432` and let Docker choose
 *  the host port. Docker does not ask the kernel: it hands out host ports in order after the last
 *  one it chose, tries ten of them, and then fails the `docker run` with `failed to bind host port
 *  127.0.0.1:<n>/tcp: address already in use` (dockerd 29.9.2 logs `Failed to allocate port`
 *  attempt=1..10). So another program that held the ports just past Docker's last choice failed
 *  the stack's Postgres. The provider now publishes no port: its server listens on no TCP port
 *  and serves on a unix socket in a bind mount (e2e/harness-postgres.ts).
 *
 *  The row finds Docker's last choice with a probe container, holds the next 64 loopback ports
 *  as any other program could, and starts one stack's Postgres from the image. It must serve.
 *  Where another program holds those ports already, as a second run of this row does, the probe
 *  cannot start, Docker's next ports are taken without the row, and the stack's Postgres must
 *  serve all the same. The probe is removed by the id its `docker create` printed, whatever
 *  happens.
 *  A developer's run without DISPATCH_E2E_PG_IMAGE skips it and says so on one line; a CI run
 *  never skips it, and fails naming the variable where the job sets none
 *  (`e2e/__tests__/postgres-provider.ts`). The `visual` job of
 *  .github/workflows/dispatch-browser.yaml runs it with that job's image, and the `dispatch-ui`
 *  job, which runs this directory with no provider, leaves it out (`postgres-provider.test.ts`). */

import { expect, test } from "bun:test";
import { execFileSync, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { pgImageVariable, stackPostgres } from "../harness-postgres";
import { requirePostgres, skipsWithoutPostgres } from "./postgres-provider";

test.skipIf(
  skipsWithoutPostgres("e2e/__tests__/harness-postgres-image-port.test.ts", [pgImageVariable])
)(
  "the image provider's Postgres serves though another program holds the ports just past Docker's last choice",
  async () => {
    const { value: image } = requirePostgres([pgImageVariable]);
    // The probe has no name of the row's: Docker names it, and the row removes it by the id its
    // `docker create` printed, which no other run's container can have. It is created first and
    // started after, so its id is the row's before its start can fail.
    const probe = execFileSync(
      "docker",
      ["create", "--entrypoint", "sleep", "--publish", "127.0.0.1::5432", image, "60"],
      { encoding: "utf8" }
    ).trim();
    let last: number | undefined;
    try {
      const start = spawnSync("docker", ["start", probe], { encoding: "utf8" });
      if (start.status === 0) {
        const published = execFileSync("docker", ["port", probe, "5432/tcp"], {
          encoding: "utf8",
        });
        last = Number(/^127\.0\.0\.1:(\d+)$/m.exec(published)?.[1]);
        expect(Number.isInteger(last) && last > 0).toBe(true);
      } else if (!/address already in use/.test(start.stderr)) {
        // Docker binds the ports past its last choice and fails the start when they are taken,
        // which is the case below; any other failure is the row's.
        throw new Error(`docker start ${probe}: ${start.stderr || start.error?.message}`);
      }
    } finally {
      execFileSync("docker", ["rm", "--force", "--volumes", probe]);
    }

    // Another program on the machine: it listens on each of the next 64 ports it can bind. Where
    // the probe could not start, another program holds them already.
    const held: { stop(closeActiveConnections?: boolean): void }[] = [];
    if (last !== undefined) {
      for (let port = last + 1; port <= last + 64 && port <= 65535; port++) {
        try {
          held.push(Bun.listen({ hostname: "127.0.0.1", port, socket: { data() {} } }));
        } catch {
          // Taken already by someone else, which Docker cannot bind either.
        }
      }
      expect(held.length).toBeGreaterThanOrEqual(10);
    }

    const dir = mkdtempSync(join(tmpdir(), "pg-image-port-"));
    try {
      const provider = stackPostgres({
        dir,
        run: `port-${randomBytes(8).toString("hex")}`,
        env: {
          ...process.env,
          DISPATCH_E2E_PG_CONNECTIONS: process.env.DISPATCH_E2E_PG_CONNECTIONS ?? "100",
        },
      });
      if (provider === undefined) throw new Error("DISPATCH_E2E_PG_IMAGE named no provider");
      const database = await provider.start(0);
      try {
        const answer = execFileSync("psql", [database.url, "-XAtc", "select 1"], {
          encoding: "utf8",
        });
        expect(answer.trim()).toBe("1");
      } finally {
        await database.stop();
      }
    } finally {
      for (const listener of held) listener.stop(true);
      rmSync(dir, { recursive: true, force: true });
    }
  },
  // The row drives the probe's `docker run` and the provider's container start, the provider's
  // readiness wait, one psql and the stop. That wait gives up at 120 s (`readyMs`,
  // e2e/harness-postgres.ts), and the probe on a runner pulls the image too, which the `visual`
  // job's runs have not timed apart; 300 s holds both with room to spare. The row read 5.6 s with
  // the image present, on a shared Linux machine under a load of about 200, on 2026-11-17.
  300_000
);
