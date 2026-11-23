// packages/envoy/scripts/approve/session-scan-box.test.ts
//
// The scan of scripts/approve/session.ts run on a box, k31 Part 1's form: outside the local stack
// (APPROVE_CHECK_DISPATCH_LOCAL unset) the session's processes are not the scan's own, so the scan
// reads each process APPROVE_CHECK_SESSION_PIDS names. Named none, or none it can read, it prints
// UNKNOWN and exits 3, since it would otherwise call clean an environment it never read. Named a
// live process whose environment holds a token the broker logged, it finds that token: the control
// that the read it refuses to skip is a read that can find something.
import { afterAll, beforeAll, expect, test } from "bun:test";
import { createHash, randomBytes } from "node:crypto";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const SESSION = join(import.meta.dir, "session.ts");

let dispatch: { readonly port: number | undefined; stop(force?: boolean): void };
let work: string;

beforeAll(() => {
  dispatch = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: () => Response.json({ code: "BROKER_TOKEN_NOT_ACTIVE" }, { status: 401 }),
  });
  work = mkdtempSync(join(tmpdir(), "session-scan-box-"));
});

afterAll(() => {
  dispatch.stop(true);
  rmSync(work, { recursive: true, force: true });
});

/** The scan run as on a box, with `pids` named, against a broker log naming `token`'s hash. */
async function scanOnABox(name: string, token: string, pids: string) {
  const dir = join(work, name);
  const box = join(dir, "box");
  mkdirSync(box, { recursive: true });
  writeFileSync(join(box, "nothing.txt"), "nothing of the session's credentials\n");
  const log = join(dir, "broker.log");
  const sum = createHash("sha256").update(token).digest("hex");
  writeFileSync(
    log,
    `${JSON.stringify({ message: "broker: minted a grant's token", grant: "g", token_sha256: sum, jkt: "jkt" })}\n`
  );
  const run = Bun.spawn([process.execPath, SESSION, "scan", log, box], {
    env: {
      PATH: process.env["PATH"] ?? "",
      DISPATCH_URL: `http://127.0.0.1:${dispatch.port}`,
      APPROVE_CHECK_SESSION_PIDS: pids,
    },
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(run.stdout).text(),
    new Response(run.stderr).text(),
    run.exited,
  ]);
  return { answer: JSON.parse(stdout) as Record<string, unknown>, exitCode, stderr, sum };
}

test("on a box, a scan naming no process of the session is UNKNOWN, exit 3", async () => {
  const { answer, exitCode } = await scanOnABox(
    "no-pids",
    randomBytes(24).toString("base64url"),
    ""
  );
  expect(exitCode).toBe(3);
  expect(answer["result"]).toBe("UNKNOWN");
});

test("on a box, a scan whose named processes it cannot read is UNKNOWN, exit 3", async () => {
  const { answer, exitCode } = await scanOnABox(
    "gone",
    randomBytes(24).toString("base64url"),
    "2147483646"
  );
  expect(exitCode).toBe(3);
  expect(answer["result"]).toBe("UNKNOWN");
});

test("control: on a box, a token in a named process's environment is found", {
  timeout: 60_000,
}, async () => {
  const token = randomBytes(24).toString("base64url");
  const session = Bun.spawn(["sleep", "60"], {
    env: { PATH: process.env["PATH"] ?? "", PLANTED_TOKEN: token },
  });
  try {
    const { answer, exitCode, stderr, sum } = await scanOnABox(
      "planted",
      token,
      String(session.pid)
    );
    expect(exitCode, stderr).toBe(0);
    expect((answer["hash_matches"] as { sha256: string }[]).map((match) => match.sha256)).toContain(
      sum
    );
  } finally {
    session.kill();
  }
});
