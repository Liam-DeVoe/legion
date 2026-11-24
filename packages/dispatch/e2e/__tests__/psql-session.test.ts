import { expect, test } from "bun:test";
import { captureEnvironment } from "@legion/test-fixtures/environment";

import { resetSession, sql } from "../psql";

/** The row of psql's session that needs no PostgreSQL: the refusal of a run whose environment
 *  names no database. The rows that read psql's answers to real statements are
 *  psql-session-postgres.test.ts's, which runs where a server is (`bun run test:postgres`). */

// `sql` resolves the database when it starts its psql, and once an earlier case has started one,
// no call reaches that refusal again; so the case ends that psql first. It is the refusal of this
// process's own `sql` that it proves, so the case runs here rather than in a child, and it puts
// the two names back rather than replacing `process.env`: a replaced object is no longer the
// runtime's environment, so every later `Bun.spawn` of the run would inherit the copy's values
// and a later write would reach neither the real environment nor `Bun.env`.
test("refuses when neither PLAYWRIGHT_DATABASE_URL nor DATABASE_URL names a database", async () => {
  resetSession();
  const restore = captureEnvironment(["DATABASE_URL", "PLAYWRIGHT_DATABASE_URL"]);
  delete process.env.DATABASE_URL;
  delete process.env.PLAYWRIGHT_DATABASE_URL;
  try {
    await expect(sql("SELECT 1")).rejects.toThrow(
      "PLAYWRIGHT_DATABASE_URL or DATABASE_URL must name the database for this e2e run"
    );
  } finally {
    restore();
  }
});
