// packages/envoy/scripts/approve/session-scan-box.test.ts
//
// The scan of scripts/approve/session.ts (approve-check.sh step 1e, k31's step (e)) as a process,
// against a stand-in Dispatch that answers every diagnostic 401.
//
// On a box, k31 Part 1's form (APPROVE_CHECK_DISPATCH_LOCAL unset), the session's processes are not
// the scan's own, so the scan reads each process APPROVE_CHECK_SESSION_PIDS names. Named none, or
// none it can read, it prints UNKNOWN and exits 3, since it would otherwise call clean an
// environment it never read. In either form, so does a scan that read no file under a path it was
// named, or whose broker log names no minted token's hash or no key binding: an instrument that
// compared nothing has not passed.
//
// The control is a token of the shape the broker mints - two base64url parts of 43 characters
// joined by a `.`, with no prefix (authelia's HMAC strategy, `%s.%s`, built with an empty prefix:
// oauth/provider.go's compose.NewOAuth2HMACStrategy) - in a named process's environment, where a
// variable reads `NAME=value`, and in a file in the forms a log, a URL or an env file writes. The
// scan must find it in each, so the read it refuses to skip is a read that can find something.
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

/** A token of the shape the broker mints: two base64url parts of 43 characters (32 bytes each)
 *  joined by `.`, with no prefix. */
function brokerShapedToken(): string {
  return `${randomBytes(32).toString("base64url")}.${randomBytes(32).toString("base64url")}`;
}

function sha256(text: string): string {
  return createHash("sha256").update(text).digest("hex");
}

/** A broker log line for a minted token of hash `sum`, bound to the key `jkt` where it is given. */
function mintedLine(sum: string, jkt?: string): string {
  return `${JSON.stringify({ message: "broker: minted a grant's token", grant: "g", token_sha256: sum, ...(jkt === undefined ? {} : { jkt }) })}\n`;
}

type Form = "box" | "local";

/**
 * One scan in `form`: `log` as the broker log's text, `paths` (relative to the case's directory)
 * as the paths it is named, and `pids` as APPROVE_CHECK_SESSION_PIDS. The case's directory holds a
 * `box` directory with one file in it, which a path may name.
 */
async function scanCase(
  name: string,
  form: Form,
  options: { readonly log: string; readonly paths: readonly string[]; readonly pids: string }
) {
  const dir = join(work, `${form}-${name}`);
  const box = join(dir, "box");
  mkdirSync(box, { recursive: true });
  writeFileSync(join(box, "nothing.txt"), "nothing of the session's credentials\n");
  mkdirSync(join(dir, "empty"), { recursive: true });
  const log = join(dir, "broker.log");
  writeFileSync(log, options.log);
  const run = Bun.spawn(
    [process.execPath, SESSION, "scan", log, ...options.paths.map((path) => join(dir, path))],
    {
      env: {
        PATH: process.env["PATH"] ?? "",
        DISPATCH_URL: `http://127.0.0.1:${dispatch.port}`,
        APPROVE_CHECK_SESSION_PIDS: options.pids,
        ...(form === "local"
          ? { APPROVE_CHECK_DISPATCH_LOCAL: `http://127.0.0.1:${dispatch.port}` }
          : {}),
      },
      stdout: "pipe",
      stderr: "pipe",
    }
  );
  const [stdout, stderr, exitCode] = await Promise.all([
    new Response(run.stdout).text(),
    new Response(run.stderr).text(),
    run.exited,
  ]);
  return { answer: JSON.parse(stdout) as Record<string, unknown>, exitCode, stderr };
}

/** A log naming one minted token's hash and one binding, which every case below but the empty
 *  ones uses: a well-formed log, so only the case's own fault can make the scan UNKNOWN. */
const LOGGED = mintedLine(sha256(brokerShapedToken()), "jkt");

/** This test runner's own process, whose environment is readable, as the session's process. */
const READABLE = String(process.pid);

test("on a box, a scan naming no process of the session is UNKNOWN, exit 3", async () => {
  const { answer, exitCode } = await scanCase("no-pids", "box", {
    log: LOGGED,
    paths: ["box"],
    pids: "",
  });
  expect(exitCode).toBe(3);
  expect(answer["result"]).toBe("UNKNOWN");
});

test("on a box, a scan whose named processes it cannot read is UNKNOWN, exit 3", async () => {
  const { answer, exitCode } = await scanCase("gone", "box", {
    log: LOGGED,
    paths: ["box"],
    pids: "2147483646",
  });
  expect(exitCode).toBe(3);
  expect(answer["result"]).toBe("UNKNOWN");
});

for (const form of ["box", "local"] as const) {
  test(`${form}: a named path that does not exist reads nothing, so the scan is UNKNOWN, exit 3`, async () => {
    const { answer, exitCode } = await scanCase("missing", form, {
      log: LOGGED,
      paths: ["box", "no-such-path"],
      pids: READABLE,
    });
    expect(exitCode).toBe(3);
    expect(answer["result"]).toBe("UNKNOWN");
    expect(String(answer["files_skipped"])).toContain("no-such-path");
  });

  test(`${form}: a scan that read no file at all is UNKNOWN, exit 3`, async () => {
    const { answer, exitCode } = await scanCase("empty-dir", form, {
      log: LOGGED,
      paths: ["empty"],
      pids: READABLE,
    });
    expect(exitCode).toBe(3);
    expect(answer["result"]).toBe("UNKNOWN");
    expect(answer["files_read"]).toBe(0);
  });

  test(`${form}: an empty broker log names no minted token's hash, so the scan is UNKNOWN, exit 3`, async () => {
    const { answer, exitCode } = await scanCase("empty-log", form, {
      log: "",
      paths: ["box"],
      pids: READABLE,
    });
    expect(exitCode).toBe(3);
    expect(answer["result"]).toBe("UNKNOWN");
    expect(answer["logged_hashes"]).toBe(0);
  });

  test(`${form}: a broker log that names no key binding is UNKNOWN, exit 3`, async () => {
    const { answer, exitCode } = await scanCase("no-binding", form, {
      log: mintedLine(sha256(brokerShapedToken())),
      paths: ["box"],
      pids: READABLE,
    });
    expect(exitCode).toBe(3);
    expect(answer["result"]).toBe("UNKNOWN");
    expect(answer["logged_bindings"]).toBe(0);
  });

  test(`${form}: a well-formed scan that finds nothing is clean, exit 0`, async () => {
    const { answer, exitCode, stderr } = await scanCase("clean", form, {
      log: LOGGED,
      paths: ["box"],
      pids: READABLE,
    });
    expect(exitCode, stderr).toBe(0);
    expect(answer["result"]).toBe("clean");
    expect(answer["hash_matches"]).toEqual([]);
  });
}

test("control: on a box, a token of the broker's shape in a named process's environment, as NAME=value, is found", {
  timeout: 60_000,
}, async () => {
  const token = brokerShapedToken();
  const session = Bun.spawn(["sleep", "60"], {
    env: { PATH: process.env["PATH"] ?? "", PLANTED_TOKEN: token },
  });
  try {
    const { answer, exitCode, stderr } = await scanCase("planted", "box", {
      log: mintedLine(sha256(token), "jkt"),
      paths: ["box"],
      pids: String(session.pid),
    });
    expect(exitCode, stderr).toBe(0);
    expect((answer["hash_matches"] as { sha256: string }[]).map((match) => match.sha256)).toContain(
      sha256(token)
    );
  } finally {
    session.kill();
  }
});

test("control: a token of the broker's shape in a file as NAME=value, export NAME=value and ?access_token=value is found", {
  timeout: 60_000,
}, async () => {
  const tokens = [brokerShapedToken(), brokerShapedToken(), brokerShapedToken()];
  const dir = join(work, "box-file-forms");
  mkdirSync(join(dir, "box"), { recursive: true });
  writeFileSync(
    join(dir, "box", "session.env"),
    `DISPATCH_APPROVE_TOKEN=${tokens[0]}\nexport X=${tokens[1]}\nGET /api/v1/projects?access_token=${tokens[2]} HTTP/1.1\n`
  );
  const { answer, exitCode, stderr } = await scanCase("file-forms", "box", {
    log: tokens.map((token) => mintedLine(sha256(token), "jkt")).join(""),
    paths: ["box"],
    pids: READABLE,
  });
  expect(exitCode, stderr).toBe(0);
  const found = (answer["hash_matches"] as { sha256: string }[]).map((match) => match.sha256);
  for (const token of tokens) expect(found).toContain(sha256(token));
});

test("control: a token of the broker's shape after a / in a URL or a path, or percent-encoded after %20, is found", {
  timeout: 60_000,
}, async () => {
  const tokens = [brokerShapedToken(), brokerShapedToken(), brokerShapedToken()];
  const dir = join(work, "box-url-forms");
  mkdirSync(join(dir, "box"), { recursive: true });
  writeFileSync(
    join(dir, "box", "access.log"),
    `GET https://dispatch.example/v1/${tokens[0]} HTTP/1.1\n/run/user/1000/${tokens[1]}\nGET /x?h=Authorization%3A%20DPoP%20${tokens[2]} HTTP/1.1\n`
  );
  const { answer, exitCode, stderr } = await scanCase("url-forms", "box", {
    log: tokens.map((token) => mintedLine(sha256(token), "jkt")).join(""),
    paths: ["box"],
    pids: READABLE,
  });
  expect(exitCode, stderr).toBe(0);
  const found = (answer["hash_matches"] as { sha256: string }[]).map((match) => match.sha256);
  expect(tokens.map((token) => found.includes(sha256(token)))).toEqual([true, true, true]);
});

test("control: a token of the broker's shape joined to a run of base64url characters, a file name's or a cache key's, is found", {
  timeout: 60_000,
}, async () => {
  const tokens = [
    brokerShapedToken(),
    brokerShapedToken(),
    brokerShapedToken(),
    brokerShapedToken(),
    brokerShapedToken(),
  ];
  const dir = join(work, "box-joined-forms");
  mkdirSync(join(dir, "box"), { recursive: true });
  writeFileSync(
    join(dir, "box", "cache.txt"),
    [
      `cache_${tokens[0]}`,
      `approve-${tokens[1]}`,
      `approve_token_for_session_${tokens[2]}`,
      `550e8400-e29b-41d4-a716-446655440000.${tokens[3]}`,
      `${"a".repeat(60)}.${tokens[4]}`,
    ].join("\n")
  );
  const { answer, exitCode, stderr } = await scanCase("joined-forms", "box", {
    log: tokens.map((token) => mintedLine(sha256(token), "jkt")).join(""),
    paths: ["box"],
    pids: READABLE,
  });
  expect(exitCode, stderr).toBe(0);
  const found = (answer["hash_matches"] as { sha256: string }[]).map((match) => match.sha256);
  expect(tokens.map((token) => found.includes(sha256(token)))).toEqual([
    true,
    true,
    true,
    true,
    true,
  ]);
});
