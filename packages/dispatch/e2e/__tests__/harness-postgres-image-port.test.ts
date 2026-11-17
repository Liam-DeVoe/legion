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
 *  A developer's run without DISPATCH_E2E_PG_IMAGE skips it and says so on one line; a CI run
 *  never skips it, and fails naming the variable where the job sets none
 *  (`e2e/__tests__/postgres-provider.ts`). The `visual` job of
 *  .github/workflows/dispatch-browser.yaml runs it with that job's image, and the `dispatch-ui`
 *  job, which runs this directory with no provider, leaves it out (`postgres-provider.test.ts`). */

import { expect, test } from "bun:test";
import { execFileSync } from "node:child_process";
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
    // `docker run` printed, which no other run's container can have.
    const probe = execFileSync(
      "docker",
      ["run", "--detach", "--entrypoint", "sleep", "--publish", "127.0.0.1::5432", image, "60"],
      { encoding: "utf8" }
    ).trim();
    let last: number;
    try {
      const published = execFileSync("docker", ["port", probe, "5432/tcp"], { encoding: "utf8" });
      last = Number(/^127\.0\.0\.1:(\d+)$/m.exec(published)?.[1]);
    } finally {
      execFileSync("docker", ["rm", "--force", probe]);
    }
    expect(Number.isInteger(last) && last > 0).toBe(true);

    // Another program on the machine: it listens on each of the next 64 ports it can bind.
    const held: { stop(closeActiveConnections?: boolean): void }[] = [];
    for (let port = last + 1; port <= last + 64 && port <= 65535; port++) {
      try {
        held.push(Bun.listen({ hostname: "127.0.0.1", port, socket: { data() {} } }));
      } catch {
        // Taken already by someone else, which Docker cannot bind either.
      }
    }
    expect(held.length).toBeGreaterThanOrEqual(10);

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
  // The row drives two `docker run`s, the probe's and the provider's, the provider's readiness
  // wait, one psql and the stop. That wait gives up at 120 s (`readyMs`,
  // e2e/harness-postgres.ts), and the probe on a runner pulls the image too, which the `visual`
  // job's runs have not timed apart; 300 s holds both with room to spare. The row read 5.6 s with
  // the image present, on a shared Linux machine under a load of about 200, on 2026-11-17.
  300_000
);
