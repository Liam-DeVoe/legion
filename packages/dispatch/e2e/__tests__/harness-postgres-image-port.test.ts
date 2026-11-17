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
 *  Skipped without DISPATCH_E2E_PG_IMAGE, as the other provider rows are; the `visual` job of
 *  .github/workflows/dispatch-browser.yaml runs it with that job's image. */

import { expect, test } from "bun:test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { stackPostgres } from "../harness-postgres";

const image = (process.env.DISPATCH_E2E_PG_IMAGE ?? "").trim();

test.skipIf(image === "")(
  "the image provider's Postgres serves though another program holds the ports just past Docker's last choice",
  async () => {
    const probe = `dispatch-e2e-pg-port-probe-${process.pid}`;
    execFileSync("docker", [
      "run",
      "--detach",
      "--name",
      probe,
      "--entrypoint",
      "sleep",
      "--publish",
      "127.0.0.1::5432",
      image,
      "60",
    ]);
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
        run: `port-${process.pid}`,
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
  300_000
);
