/** The image provider's start in a run directory whose path holds a character that a
 *  `docker run` argument or a Postgres connection string reads as syntax.
 *
 *  The provider bind-mounts a socket directory under the run directory into its container
 *  (e2e/harness-postgres.ts). A `--volume <path>:/var/run/postgresql` splits at each `:`, so a
 *  `TMPDIR` that holds one failed every start with `docker: Error response from daemon: invalid
 *  mode: /var/run/postgresql`. `--mount` reads its value as one CSV record, so its `source=`
 *  field is quoted, which is what lets a `"` stand in the path. A `,` cannot stand in any socket
 *  directory a stack names: psql (libpq) and the Dispatch server (pgx) both read the `host` of a
 *  `DATABASE_URL` as a list split at each `,`. So a run directory that holds one puts the socket
 *  in /tmp, as a run directory too long for a socket path does.
 *
 *  Each row makes its run directory under /tmp, not under `TMPDIR`: the socket must stay in the
 *  directory that holds the character, and a long `TMPDIR` would move it to /tmp and leave the
 *  row proving nothing. So the row reads where the socket went before it says the stack served.
 *  Skipped without DISPATCH_E2E_PG_IMAGE, as the other provider rows are; the `visual` job of
 *  .github/workflows/dispatch-browser.yaml runs it with that job's image. */

import { expect, test } from "bun:test";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { join } from "node:path";

import { stackPostgres } from "../harness-postgres";

const image = (process.env.DISPATCH_E2E_PG_IMAGE ?? "").trim();

const rows = [
  { component: "c:olon", socketInRunDirectory: true },
  { component: 'q"uote', socketInRunDirectory: true },
  { component: "c,omma", socketInRunDirectory: false },
] as const;

for (const [index, row] of rows.entries()) {
  test.skipIf(image === "")(
    `the image provider's Postgres serves from a run directory that holds ${row.component}`,
    async () => {
      const root = mkdtempSync(join("/tmp", "pg-mount-"));
      const dir = join(root, row.component);
      mkdirSync(dir);
      try {
        const provider = stackPostgres({
          dir,
          run: `mount-${process.pid}-${index}`,
          env: {
            ...process.env,
            DISPATCH_E2E_PG_CONNECTIONS: process.env.DISPATCH_E2E_PG_CONNECTIONS ?? "100",
          },
        });
        if (provider === undefined) throw new Error("DISPATCH_E2E_PG_IMAGE named no provider");
        const database = await provider.start(0);
        try {
          expect(database.address.startsWith(`${dir}/`)).toBe(row.socketInRunDirectory);
          expect(database.address).not.toContain(",");
          const answer = execFileSync("psql", [database.url, "-XAtc", "select 1"], {
            encoding: "utf8",
          });
          expect(answer.trim()).toBe("1");
        } finally {
          await database.stop();
        }
      } finally {
        rmSync(root, { recursive: true, force: true });
      }
    },
    300_000
  );
}
