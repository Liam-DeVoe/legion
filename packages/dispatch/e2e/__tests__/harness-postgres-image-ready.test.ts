/** The image provider's start returns only once the container's final server serves, not the
 *  server the image's own initialisation runs first (pull request #4236).
 *
 *  The image's entrypoint (docker-library/postgres's docker-entrypoint.sh) makes a fresh cluster
 *  with a temporary server, which listens on the same unix socket as the final one. That server
 *  makes the stack's database, runs the scripts of /docker-entrypoint-initdb.d, and stops, and
 *  only then does the final server start. A start that took the first `select 1` that answered
 *  could take it from the temporary server, and hand its stack a socket that went away a moment
 *  later: `harness-postgres-image-mount.test.ts` failed one run in four so, at `create extension`,
 *  with "No such file or directory" on that socket.
 *
 *  The row widens that moment to seconds: it starts the provider from an image of its own, made
 *  from the one the run names with one init script that sleeps 5 s, and then reads, for the
 *  next 8 s, the start time of the server that answers. Every read must answer, and every
 *  answer must name one server. The image is made with `docker commit` rather than a build, so
 *  it needs no registry and no builder. Skipped without DISPATCH_E2E_PG_IMAGE outside CI, and
 *  failed without it in CI (`./postgres-provider`). */

import { expect, test } from "bun:test";
import { spawnSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { pgConnectionsVariable, pgImageVariable, stackPostgres } from "../harness-postgres";
import { requirePostgres, skipsWithoutPostgres } from "./postgres-provider";

const skipped = skipsWithoutPostgres("e2e/__tests__/harness-postgres-image-ready.test.ts", [
  pgImageVariable,
]);

/** How long the init script of the row's image sleeps after the temporary server made the
 *  stack's database, in seconds. */
const initSleep = 5;
/** How long the row reads the server after the start returned: past the init script's sleep,
 *  and past the moment the temporary server stops and the final one starts. */
const watchMs = 8_000;

/** `docker` with `args`, which throws where it fails. */
function docker(...args: string[]): string {
  const run = spawnSync("docker", args, { encoding: "utf8", timeout: 120_000 });
  if (run.status !== 0) throw new Error(`docker ${args.join(" ")}: ${run.stderr}`);
  return run.stdout;
}

test.skipIf(skipped)(
  "image provider: a stack's Postgres answers from the moment its start returns, though the image's own init runs a script after it made the database",
  async () => {
    const { value: image } = requirePostgres([pgImageVariable]);
    const slow = `dispatch-e2e-pg-slow-init-${process.pid}`;
    const builder = `${slow}-build`;
    const dir = mkdtempSync(join(tmpdir(), "pg-image-ready-"));
    let made = false;
    try {
      const script = join(dir, "slow.sh");
      writeFileSync(script, `sleep ${initSleep}\n`);
      docker("create", "--name", builder, image);
      try {
        docker("cp", script, `${builder}:/docker-entrypoint-initdb.d/slow.sh`);
        docker("commit", builder, slow);
        made = true;
      } finally {
        docker("rm", "--force", builder);
      }

      const provider = stackPostgres({
        dir,
        run: `slow-init-${process.pid}`,
        env: {
          ...process.env,
          [pgImageVariable]: slow,
          [pgConnectionsVariable]: process.env[pgConnectionsVariable] ?? "100",
        },
      });
      if (provider === undefined) throw new Error(`${pgImageVariable} named no provider`);
      const database = await provider.start(0);
      try {
        // Each read names the server that answered by its start time, so a start that returned
        // with the temporary server shows as two servers even where no read fell in the moment
        // between the two, and a read that fell there shows as a refusal.
        const refused: string[] = [];
        const servers = new Set<string>();
        const until = Date.now() + watchMs;
        while (Date.now() < until) {
          const psql = spawnSync(
            "psql",
            [database.url, "-XAtc", "select pg_postmaster_start_time()"],
            { encoding: "utf8", timeout: 30_000 }
          );
          if (psql.status === 0) servers.add(psql.stdout.trim());
          else refused.push(`${new Date().toISOString()} ${psql.stderr.trim()}`);
        }
        expect({ refused, servers: servers.size }).toEqual({ refused: [], servers: 1 });
      } finally {
        await database.stop();
      }
    } finally {
      if (made) docker("rmi", slow);
      rmSync(dir, { recursive: true, force: true });
    }
  },
  // The row drives a `docker create`, `cp` and `commit` of the image, one start of the provider,
  // whose waits give up at 120 s, 8 s of reads and the stop. It read 12.3 to 22.8 s on a shared
  // Linux machine under a load of about 270 to 300, on 2026-11-17; 300 s leaves the 120 s wait
  // and a slow Docker daemon room.
  300_000
);
