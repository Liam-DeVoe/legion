// packages/envoy-client/src/__tests__/dispatch-proxy.test.ts
//
// `dispatch proxy --socket <path> -- <command>` (dispatch://INFRA-4799/spec, R1-R7), run in this
// test's own process through `runDispatchCli(["proxy", …])`, in a pod's environment under
// `DISPATCH_TOKEN_BROKER=1`, against a stand-in secrets broker and a stand-in Dispatch. A row
// starts the proxy with a command that waits for a file the row makes (`DONE_LOOP`), makes its
// requests over the socket, then makes that file and reads the proxy's exit status and its
// standard error. A request `fetch` sends as it stands goes through `fetch(url, {unix})`; one
// `fetch` would rewrite (a target in absolute form or one that starts with `//`, a dot segment, a
// backslash, the asterisk form, `CONNECT` and `Upgrade`) is written by hand over `node:net`.
// `dispatch-proxy-process.test.ts` runs the real entry as its own process for what a call in this
// process cannot hold: its standard streams, a signal, and its sockets.
//
// Each wait is bounded and says what it waited for, so a proxy that never makes its socket fails
// its row in a few seconds and leaves nothing behind.

import { afterEach, beforeEach, expect, test } from "bun:test";
import { createHash, randomBytes, randomUUID } from "node:crypto";
import { existsSync, lstatSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { connect } from "node:net";
import { dirname, join } from "node:path";
import { dispatchToolSpecs } from "@legion/contracts";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import { runDispatchCli } from "../dispatch-cli";
import { commandName } from "../dispatch-command";
import { brokerStandIns } from "./broker-stand-ins";

/** One proxy run of this file, in this process: what it printed so far, and its exit. */
interface ProxyRun {
  readonly socket: string;
  /** The file whose making ends the run's `DONE_LOOP` command; "" where its command is another. */
  readonly done: string;
  readonly exited: Promise<number>;
  readonly printed: { stdout: string; stderr: string };
}

/** Every run a row started, ended by the hook below whatever the row's outcome. */
const running = new Set<ProxyRun>();

// Registered before the directories' removal and the stand-ins' cleanup, so it runs first: each
// run's command ends while the file it waits for can still be made, and the proxy exits before
// the stand-ins it talks to stop.
afterEach(async () => {
  const runs = [...running];
  running.clear();
  for (const run of runs) {
    if (run.done !== "") writeFileSync(run.done, "");
  }
  await within(
    Promise.allSettled(runs.map((run) => run.exited)),
    4_000,
    "every proxy run this row started exited"
  ).catch(() => undefined);
});

/** Each directory is `mkdtemp` under `os.tmpdir()`: a socket at `<it>/d.sock` is short of the
 *  107 bytes a unix socket's path may hold. */
const makeDirectory = temporaryDirectories("dp");

const { podEnvironment, servers, stubBroker, projectedToken } = brokerStandIns("dispatch-proxy");

/** The session every row's proxy acts as (`DISPATCH_SESSION_ID`). */
const SESSION = "ses-1";

/** The command a row's proxy runs: it waits for the file its first argument names. */
const DONE_LOOP = 'while [ ! -e "$0" ]; do sleep 0.02; done';

/** What Dispatch answers a token its broker no longer holds live (`brokerTokenActor`). */
const DEAD = {
  code: "BROKER_TOKEN_NOT_ACTIVE",
  error: "the secrets broker does not hold this access token as live",
};

/** The broker's final refusal, a text `KNOWN_BROKER_REFUSALS` lists verbatim. */
const BROKER_REFUSAL = "no entry lists the account this token names";

/**
 * Every refusal the proxy makes of its own (R5): 405 for a method or path R2 does not send, 400
 * for a target that names another host, and 502 for a trade the broker refused and for a
 * Dispatch that does not answer. The proxy never answers 403 or 404 itself, since a tool reads
 * those as Dispatch's, and no code Dispatch answers starts with `PROXY_`.
 */
const PROXY_ANSWERS: readonly { readonly status: number; readonly code: string }[] = [
  { status: 405, code: "PROXY_METHOD_REFUSED" },
  { status: 400, code: "PROXY_TARGET_REFUSED" },
  { status: 502, code: "PROXY_TRADE_REFUSED" },
  { status: 502, code: "PROXY_DISPATCH_UNREACHABLE" },
];

/** Every command the `dispatch` command line has for a Dispatch tool. */
const COMMANDS = new Set(dispatchToolSpecs.map((spec) => commandName(spec.name)));

const COMMENTS = "/api/v1/issues/DSP-1/comments";
const BLOB_PATH = "/api/v1/artifacts/a-1/versions/1";
const DEAD_FIRST = "/api/v1/dead-first";
/** 1 MiB and 7 bytes, so a body cut at a power of two fails the length. */
const BLOB_BYTES = 1024 * 1024 + 7;

/** The proxy's exit line, in its fixed form (R4). */
function exitLine(requests: number, trades: number, renewed: number): string {
  return `dispatch proxy: ${requests} requests, ${trades} trades, ${renewed} after BROKER_TOKEN_NOT_ACTIVE`;
}

/** Any exit line, wherever it stands in `text`. */
const ANY_EXIT_LINE =
  /^dispatch proxy: \d+ requests, \d+ trades, \d+ after BROKER_TOKEN_NOT_ACTIVE$/m;

function lastLine(text: string): string {
  return text.trimEnd().split("\n").at(-1) ?? "";
}

let stateDir = "";
beforeEach(() => {
  stateDir = makeDirectory("state");
});

type Environment = Record<string, string | undefined>;

/** A pod's environment under `DISPATCH_TOKEN_BROKER=1`, as a role's `dispatch` command reads it:
 *  the host and session the plugin sets, the broker to trade at with the pod's projected token,
 *  the Dispatch server, and this row's own state directory. */
function podProxyEnvironment(brokerUrl: string, dispatchUrl: string): Environment {
  return {
    PATH: process.env.PATH,
    ...podEnvironment(brokerUrl, dispatchUrl),
    DISPATCH_HOST: "omp",
    DISPATCH_SESSION_ID: SESSION,
    DISPATCH_TOKEN_BROKER: "1",
    DISPATCH_STATE_DIR: stateDir,
  };
}

/** `promise`, or a failure naming `what` once `ms` have passed. */
async function within<T>(promise: Promise<T>, ms: number, what: string): Promise<T> {
  const late = Promise.withResolvers<never>();
  const timer = setTimeout(() => late.reject(new Error(`not done within ${ms} ms: ${what}`)), ms);
  try {
    return await Promise.race([promise, late.promise]);
  } finally {
    clearTimeout(timer);
  }
}

/** A fresh socket path: `d.sock` in a new directory. */
function socketPath(): string {
  return join(makeDirectory(), "d.sock");
}

/** Runs `dispatch <argv>` in this process as the entry would, and keeps it for the cleanup. */
function launch(argv: readonly string[], env: Environment, socket = "", done = ""): ProxyRun {
  const printed = { stdout: "", stderr: "" };
  const exited = runDispatchCli(argv, env, {
    stdout: (text) => {
      printed.stdout += text;
    },
    stderr: (text) => {
      printed.stderr += text;
    },
    readText: (path) => readFileSync(path, "utf-8"),
    cwd: stateDir,
  });
  // A run that throws fails the row that awaits it; one no row awaits is still handled.
  exited.catch(() => undefined);
  const run = { socket, done, exited, printed };
  running.add(run);
  return run;
}

/** `dispatch <argv>` to its end: its exit status and what it printed. */
async function ran(
  argv: readonly string[],
  env: Environment
): Promise<{ code: number; stdout: string; stderr: string }> {
  const run = launch(argv, env);
  const code = await within(run.exited, 10_000, `dispatch ${argv.join(" ")} exited`);
  return { code, ...run.printed };
}

/** Whether something accepts a connection at the unix socket `path` now. */
function accepts(path: string): Promise<boolean> {
  const { promise, resolve } = Promise.withResolvers<boolean>();
  const probe = connect(path);
  probe.once("connect", () => {
    probe.destroy();
    resolve(true);
  });
  probe.once("error", () => {
    probe.destroy();
    resolve(false);
  });
  return promise;
}

/** Waits, at most `ms`, until the run's socket accepts a connection; fails at once, naming what
 *  it printed, where the run ends first. */
async function untilAccepting(run: ProxyRun, ms = 5_000): Promise<void> {
  let ended: number | undefined;
  run.exited.then(
    (code) => {
      ended = code;
    },
    () => {
      ended = -1;
    }
  );
  const deadline = Date.now() + ms;
  while (true) {
    if (ended !== undefined) {
      throw new Error(
        `dispatch proxy exited ${ended} before its socket ${run.socket} accepted a connection; standard output: ${JSON.stringify(run.printed.stdout)}; standard error: ${JSON.stringify(run.printed.stderr)}`
      );
    }
    if (await accepts(run.socket)) return;
    if (Date.now() > deadline) {
      throw new Error(`the proxy's socket ${run.socket} accepted no connection within ${ms} ms`);
    }
    await Bun.sleep(20);
  }
}

/** A proxy whose command waits for its done file, once its socket accepts. */
async function startedProxy(env: Environment): Promise<ProxyRun> {
  const socket = socketPath();
  const done = join(makeDirectory(), "done");
  const run = launch(
    ["proxy", "--socket", socket, "--", "bash", "-c", DONE_LOOP, done],
    env,
    socket,
    done
  );
  await untilAccepting(run);
  return run;
}

/** Ends the run's command, and answers the proxy's exit status and what it printed. */
async function finish(run: ProxyRun): Promise<{ code: number; stdout: string; stderr: string }> {
  writeFileSync(run.done, "");
  const code = await within(run.exited, 5_000, "dispatch proxy exited once its command ended");
  return { code, ...run.printed };
}

/** One answer as the tool reads it. */
interface Answer {
  readonly status: number;
  readonly headers: Readonly<Record<string, string>>;
  readonly body: Buffer;
}

/** A request through the proxy's socket as `fetch` sends it; a redirect is not followed. */
async function viaSocket(socket: string, path: string, init: RequestInit = {}): Promise<Answer> {
  const response = await fetch(`http://dispatch${path}`, {
    ...init,
    unix: socket,
    redirect: "manual",
    signal: AbortSignal.timeout(10_000),
  });
  return {
    status: response.status,
    headers: Object.fromEntries(response.headers.entries()),
    body: Buffer.from(await response.arrayBuffer()),
  };
}

/** How long a request written by hand waits for its whole answer. */
const RAW_ANSWER_MS = 3_000;

/** The answer `bytes` holds once it is whole (or once the connection `ended`), else undefined.
 *  `bodiless` is a HEAD's answer, which carries no body whatever its headers say. */
function wholeAnswer(bytes: Buffer, ended: boolean, bodiless: boolean): Answer | undefined {
  const end = bytes.indexOf("\r\n\r\n");
  if (end < 0) return undefined;
  const [statusLine = "", ...lines] = bytes.subarray(0, end).toString("latin1").split("\r\n");
  const status = Number(statusLine.split(" ")[1]);
  const headers: Record<string, string> = {};
  for (const line of lines) {
    const colon = line.indexOf(":");
    const name = line.slice(0, colon).trim().toLowerCase();
    const value = line.slice(colon + 1).trim();
    headers[name] = headers[name] === undefined ? value : `${headers[name]}, ${value}`;
  }
  const rest = bytes.subarray(end + 4);
  if (bodiless || status === 204 || status === 304) {
    return { status, headers, body: Buffer.alloc(0) };
  }
  if ((headers["transfer-encoding"] ?? "").toLowerCase().includes("chunked")) {
    const chunks: Buffer[] = [];
    let at = 0;
    while (true) {
      const sizeEnd = rest.indexOf("\r\n", at);
      if (sizeEnd < 0) break;
      const size = Number.parseInt(rest.subarray(at, sizeEnd).toString("latin1"), 16);
      if (size === 0) return { status, headers, body: Buffer.concat(chunks) };
      if (rest.length < sizeEnd + 2 + size + 2) break;
      chunks.push(rest.subarray(sizeEnd + 2, sizeEnd + 2 + size));
      at = sizeEnd + 2 + size + 2;
    }
    return ended ? { status, headers, body: Buffer.concat(chunks) } : undefined;
  }
  const length = headers["content-length"];
  if (length !== undefined && rest.length >= Number(length)) {
    return { status, headers, body: rest.subarray(0, Number(length)) };
  }
  return ended ? { status, headers, body: rest } : undefined;
}

/** `request`, written as it stands over a connection to the proxy's socket, and its answer. */
function rawRequest(socket: string, request: string, bodiless = false): Promise<Answer> {
  const { promise, resolve, reject } = Promise.withResolvers<Answer>();
  const firstLine = request.slice(0, request.indexOf("\r\n"));
  const connection = connect(socket);
  let bytes = Buffer.alloc(0);
  let settled = false;
  const settle = (outcome: Answer | Error): void => {
    if (settled) return;
    settled = true;
    clearTimeout(timer);
    connection.destroy();
    if (outcome instanceof Error) reject(outcome);
    else resolve(outcome);
  };
  const timer = setTimeout(() => {
    settle(
      new Error(
        `no whole answer to ${JSON.stringify(firstLine)} within ${RAW_ANSWER_MS} ms; ${bytes.length} bytes came: ${JSON.stringify(bytes.toString("latin1").slice(0, 300))}`
      )
    );
  }, RAW_ANSWER_MS);
  connection.on("data", (chunk: Buffer) => {
    bytes = Buffer.concat([bytes, chunk]);
    const answer = wholeAnswer(bytes, false, bodiless);
    if (answer !== undefined) settle(answer);
  });
  connection.on("end", () => {
    settle(
      wholeAnswer(bytes, true, bodiless) ??
        new Error(`the connection ended with no answer to ${JSON.stringify(firstLine)}`)
    );
  });
  connection.on("error", (error) => {
    settle(new Error(`${JSON.stringify(firstLine)}: ${error.message}`));
  });
  connection.write(request);
  return promise;
}

/** A GET written by hand for `target`, which asks the proxy to close the connection after it. */
function rawGet(target: string, extra = ""): string {
  return `GET ${target} HTTP/1.1\r\nHost: dispatch\r\n${extra}Connection: close\r\n\r\n`;
}

/** The `{code, error}` body of an answer, as Dispatch's refusals and the proxy's own are shaped. */
function refusalOf(answer: Answer): { readonly code: string; readonly error: string } {
  const text = answer.body.toString("utf8");
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    throw new Error(
      `the ${answer.status} answer is not JSON: ${JSON.stringify(text.slice(0, 300))}`
    );
  }
  if (
    parsed === null ||
    typeof parsed !== "object" ||
    !("code" in parsed) ||
    !("error" in parsed)
  ) {
    throw new Error(`the ${answer.status} answer holds no {code, error}: ${text.slice(0, 300)}`);
  }
  return { code: String(parsed.code), error: String(parsed.error) };
}

/** The `dispatch` commands `text` names, among those the command line has. */
function namedCommands(text: string): string[] {
  return [...text.matchAll(/\bdispatch ([a-z][a-z-]*)/g)]
    .map((match) => match[1] ?? "")
    .filter((word) => COMMANDS.has(word));
}

/** A JSON object of a JWT's part `index` (0, the header; 1, the claims). */
function jwtPart(compact: string, index: number): Record<string, unknown> {
  const part = compact.split(".")[index] ?? "";
  const parsed: unknown = JSON.parse(Buffer.from(part, "base64url").toString("utf8") || "{}");
  return parsed !== null && typeof parsed === "object" ? { ...parsed } : {};
}

/** `ath` of a proof presented beside `token`. */
function accessTokenHash(token: string): string {
  return createHash("sha256").update(token).digest("base64url");
}

/** What the stand-in broker answers its next trade. */
type BrokerAnswer =
  | { readonly kind: "token"; readonly seconds: number }
  | { readonly kind: "unavailable" }
  | { readonly kind: "refused" };

/** One trade the stand-in broker was asked, and what it answered. */
interface BrokerTrade {
  readonly form: URLSearchParams;
  /** The public key of the trade's proof, which the token it answers is bound to. */
  readonly jwk: unknown;
  readonly status: number;
  /** The access token it answered; undefined for a 503 or a refusal. */
  readonly token: string | undefined;
  /** When it answered. */
  readonly at: number;
}

/**
 * A secrets broker whose `answer` a row may change between trades: a random token good for a
 * given number of seconds, a 503, or the final refusal of "Fixtures from the real system". Each
 * answer waits `delayMs` first.
 */
function standInBroker(first: BrokerAnswer, delayMs = 0) {
  const broker = {
    url: "",
    answer: first,
    trades: [] as BrokerTrade[],
    /** The access tokens it handed out, in order. */
    tokens: (): string[] =>
      broker.trades.flatMap((trade) => (trade.token === undefined ? [] : [trade.token])),
  };
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: async (request) => {
      const form = new URLSearchParams(await request.text());
      const jwk = jwtPart(request.headers.get("DPoP") ?? "", 0).jwk;
      if (delayMs > 0) await Bun.sleep(delayMs);
      const answer = broker.answer;
      if (answer.kind === "unavailable") {
        broker.trades.push({ form, jwk, status: 503, token: undefined, at: Date.now() });
        return Response.json({ error: "temporarily_unavailable" }, { status: 503 });
      }
      if (answer.kind === "refused") {
        broker.trades.push({ form, jwk, status: 400, token: undefined, at: Date.now() });
        return Response.json(
          { error: "invalid_grant", error_description: BROKER_REFUSAL },
          { status: 400 }
        );
      }
      const minted = fixtureSecret("access");
      broker.trades.push({ form, jwk, status: 200, token: minted, at: Date.now() });
      return Response.json({
        access_token: minted,
        issued_token_type: "urn:ietf:params:oauth:token-type:access_token",
        token_type: "DPoP",
        expires_in: answer.seconds,
        credential_id: "cred-1",
      });
    },
  });
  servers.push(server);
  broker.url = `http://127.0.0.1:${server.port}`;
  return broker;
}

/** One request the stand-in Dispatch was sent. */
interface Received {
  readonly method: string;
  readonly path: string;
  readonly query: string;
  /** By lower-case name, as `Headers` keeps them. */
  readonly headers: Readonly<Record<string, string>>;
  readonly body: Buffer;
}

/** One answer the stand-in Dispatch gave: the path it answered and the headers it sent. */
interface Answered {
  readonly path: string;
  /** By lower-case name, as `Headers` keeps them. */
  readonly headers: Readonly<Record<string, string>>;
}

/** A JSON answer with the headers the stand-in sends beside each, its `Content-Length` among
 *  them. */
function jsonAnswer(value: unknown, status = 200, headers: Record<string, string> = {}): Response {
  const text = JSON.stringify(value);
  return new Response(text, {
    status,
    headers: {
      "Content-Type": "application/json",
      "Content-Length": String(Buffer.byteLength(text)),
      "X-Dispatch-Test": "kept",
      ...headers,
    },
  });
}

/** The issue list the stand-in answers. */
const ISSUES = [{ key: "DSP-1", title: "Native workspace", status: "in_progress" }];
/** Dispatch's own refusals of a read, with its own codes. */
const FORBIDDEN = {
  code: "PROJECT_SCOPE",
  error: "this request's credential may not read issue DSP-403",
};
const NOT_FOUND = { code: "NOT_FOUND", error: "issue DSP-404 not found" };

/**
 * A Dispatch that records each request (method, path, query, headers and body bytes) and each
 * answer it gives (the path and the headers it sent), and answers by path: the issue list, a
 * binary document version of 1 MiB and 7 bytes, a 302 to a host that does not resolve and one to
 * its own issue list, its own 403 and 404, a 201 for the comment POST, and on `DEAD_FIRST` a 401
 * `BROKER_TOKEN_NOT_ACTIVE` for the first request and for any request that presents that
 * request's token again (`dispatch-dead-token.test.ts`'s `deadFirst`).
 */
function standInDispatch() {
  const received: Received[] = [];
  const answered: Answered[] = [];
  const blob = randomBytes(BLOB_BYTES);
  const deadPresented: string[] = [];
  let url = "";
  const answerTo = (method: string, path: string, headers: Record<string, string>): Response => {
    if (path === DEAD_FIRST) {
      const token = (headers.authorization ?? "").replace(/^DPoP /, "");
      deadPresented.push(token);
      if (deadPresented.length === 1 || token === deadPresented[0]) {
        return Response.json(DEAD, { status: 401 });
      }
      return jsonAnswer({ served: true });
    }
    if (method === "POST" && path === COMMENTS) {
      return jsonAnswer({ comment: { id: "c-1", issue_key: "DSP-1" } }, 201, {
        "Idempotency-Key": headers["idempotency-key"] ?? "",
      });
    }
    if (path === "/api/v1/issues") return jsonAnswer(ISSUES);
    if (path === BLOB_PATH) {
      return new Response(blob, {
        headers: {
          "Content-Type": "application/octet-stream",
          "Content-Length": String(blob.length),
          "Content-Disposition": 'attachment; filename="blob.bin"',
          ETag: '"v1"',
          "X-Dispatch-Test": "kept",
        },
      });
    }
    if (path === "/api/v1/moved") {
      return new Response(null, {
        status: 302,
        headers: { Location: "http://elsewhere.invalid/x" },
      });
    }
    if (path === "/api/v1/moved-here") {
      return new Response(null, { status: 302, headers: { Location: `${url}/api/v1/issues` } });
    }
    if (path === "/api/v1/issues/DSP-403") return jsonAnswer(FORBIDDEN, 403);
    if (path === "/api/v1/issues/DSP-404") return jsonAnswer(NOT_FOUND, 404);
    return jsonAnswer({ code: "NOT_FOUND", error: `no route for ${method} ${path}` }, 404);
  };
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: async (request) => {
      const { pathname, search } = new URL(request.url);
      const headers = Object.fromEntries(request.headers.entries());
      received.push({
        method: request.method,
        path: pathname,
        query: search,
        headers,
        body: Buffer.from(await request.arrayBuffer()),
      });
      const answer = answerTo(request.method, pathname, headers);
      answered.push({ path: pathname, headers: Object.fromEntries(answer.headers.entries()) });
      return answer;
    },
  });
  servers.push(server);
  url = `http://127.0.0.1:${server.port}`;
  return { url, port: server.port, received, answered, blob };
}

/** A Dispatch URL nothing listens at: a server's port, once that server has stopped. */
function unreachableUrl(): string {
  const gone = Bun.serve({ port: 0, hostname: "127.0.0.1", fetch: () => new Response("") });
  const url = `http://127.0.0.1:${gone.port}`;
  gone.stop(true);
  return url;
}

/** Field 22 of /proc/<pid>/stat: the process's start time, in clock ticks since boot. */
function startOf(pid: number): string {
  const stat = readFileSync(`/proc/${pid}/stat`, "utf-8");
  return stat.slice(stat.lastIndexOf(")") + 2).split(" ")[19] ?? "";
}

/**
 * The record a live AskGate gate writes for session `id` (`dispatch-askgate.test.ts`'s
 * `writeRecord`): this test process stands in for Oh My Pi, and `mode` is the check mode the
 * owner file carries.
 */
function writeRecord(
  id = SESSION,
  owner: { pid: number; start: string | null; store: string } = {
    pid: process.pid,
    start: startOf(process.pid),
    store: join(stateDir, "askgate", String(process.pid)),
  },
  mode: "warn" | "block" | null = "block"
): void {
  const name = owner.start === null ? String(owner.pid) : `${owner.pid}-${owner.start}`;
  const file = join(stateDir, "sessions", id, "askgate", `${name}.json`);
  mkdirSync(dirname(file), { recursive: true });
  writeFileSync(file, JSON.stringify({ v: 2, ...owner, ...(mode === null ? {} : { mode }) }));
  mkdirSync(join(owner.store, "markers"), { recursive: true });
}

/** Leaves a socket at `path` with nothing listening on it: a process that listened there and was
 *  killed before it could remove it, as a killed proxy leaves its socket. */
function leftBehindSocket(path: string): void {
  Bun.spawnSync(
    [
      process.execPath,
      "-e",
      'require("node:net").createServer().listen(process.argv[1], () => process.kill(process.pid, "SIGKILL"))',
      path,
    ],
    { timeout: 10_000 }
  );
  if (!lstatSync(path).isSocket()) throw new Error(`no socket was left at ${path}`);
}

// The hang guards below are limits on a row's waits, not budgets: each row fails at once where
// the proxy never makes its socket.

test("makes its socket at the path it is given, with mode 0600", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  const run = await startedProxy(podProxyEnvironment(broker.url, dispatch.url));

  const made = lstatSync(run.socket);
  expect(made.isSocket()).toBe(true);
  expect((made.mode & 0o777).toString(8)).toBe("600");

  const { code } = await finish(run);
  expect(code).toBe(0);
  expect(existsSync(run.socket)).toBe(false);
}, 15_000);

test("refuses a socket path that exists, a regular file and a socket a killed proxy left, with exit 2, and runs no command", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  const env = podProxyEnvironment(broker.url, dispatch.url);
  const file = socketPath();
  writeFileSync(file, "kept\n");
  const left = socketPath();
  leftBehindSocket(left);

  for (const [what, path] of [
    ["a regular file", file],
    ["a socket a killed proxy left", left],
  ] as const) {
    const marker = join(makeDirectory(), "ran");
    const { code, stdout, stderr } = await ran(
      ["proxy", "--socket", path, "--", "touch", marker],
      env
    );
    expect({ what, code, stdout }).toEqual({ what, code: 2, stdout: "" });
    expect(existsSync(marker), what).toBe(false);
    expect(stderr, what).toContain(path);
  }
  // What stood at each path is left as it was.
  expect(readFileSync(file, "utf-8")).toBe("kept\n");
  expect(lstatSync(left).isSocket()).toBe(true);
  expect(dispatch.received).toEqual([]);
}, 15_000);

test("exits 2, runs no command and names the cause", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const refusing = standInBroker({ kind: "refused" });
  const dispatch = standInDispatch();
  const pod = podProxyEnvironment(broker.url, dispatch.url);
  const tokenFile = join(makeDirectory(), "dispatch-token");
  writeFileSync(tokenFile, `${fixtureSecret("dsp")}\n`, { mode: 0o600 });
  const causes: readonly (readonly [string, Environment, string])[] = [
    [
      "no DISPATCH_TOKEN_BROKER",
      { ...pod, DISPATCH_TOKEN_BROKER: undefined },
      "DISPATCH_TOKEN_BROKER",
    ],
    ["DISPATCH_TOKEN_BROKER empty", { ...pod, DISPATCH_TOKEN_BROKER: "" }, "DISPATCH_TOKEN_BROKER"],
    [
      "a DISPATCH_TOKEN_FILE and no DISPATCH_TOKEN_BROKER",
      { ...pod, DISPATCH_TOKEN_BROKER: undefined, DISPATCH_TOKEN_FILE: tokenFile },
      "DISPATCH_TOKEN_BROKER",
    ],
    ["no DISPATCH_HOST", { ...pod, DISPATCH_HOST: undefined }, "DISPATCH_HOST"],
    ["no DISPATCH_SESSION_ID", { ...pod, DISPATCH_SESSION_ID: undefined }, "DISPATCH_SESSION_ID"],
    [
      "no AGENT_SECRETS_URL, so Dispatch is off",
      { ...pod, AGENT_SECRETS_URL: undefined },
      "AGENT_SECRETS_URL",
    ],
    [
      "a first trade the broker refuses for good",
      podProxyEnvironment(refusing.url, dispatch.url),
      BROKER_REFUSAL,
    ],
  ];

  for (const [what, env, cause] of causes) {
    const socket = socketPath();
    const marker = join(makeDirectory(), "ran");
    const { code, stdout, stderr } = await ran(
      ["proxy", "--socket", socket, "--", "touch", marker],
      env
    );
    expect({ what, code, stdout }).toEqual({ what, code: 2, stdout: "" });
    expect(stderr, what).toContain(cause);
    expect(existsSync(marker), what).toBe(false);
    expect(existsSync(socket), what).toBe(false);
  }
  expect(broker.trades).toEqual([]);
  expect(refusing.trades.length).toBeGreaterThan(0);
  expect(dispatch.received).toEqual([]);
}, 20_000);

test("trades once for 500 requests under a token that lives an hour, for the session DISPATCH_SESSION_ID names, and counts them in its exit line", async () => {
  const broker = stubBroker(3600);
  const dispatch = standInDispatch();
  const run = await startedProxy(podProxyEnvironment(broker.url, dispatch.url));

  const statuses: number[] = [];
  for (let batch = 0; batch < 20; batch += 1) {
    const answers = await Promise.all(
      Array.from({ length: 25 }, (_, index) =>
        viaSocket(run.socket, `/api/v1/issues?page=${batch * 25 + index}`)
      )
    );
    statuses.push(...answers.map((answer) => answer.status));
  }

  expect(statuses).toEqual(Array.from({ length: 500 }, () => 200));
  expect(broker.trades).toHaveLength(1);
  expect(broker.trades[0]?.form.get("session")).toBe(SESSION);
  expect(dispatch.received).toHaveLength(500);
  const token = broker.trades[0]?.token ?? "no trade";
  expect(new Set(dispatch.received.map((asked) => asked.headers.authorization))).toEqual(
    new Set([`DPoP ${token}`])
  );
  // Each request carries a proof of its own.
  const jtis = dispatch.received.map((asked) => jwtPart(asked.headers.dpop ?? "", 1).jti);
  expect(new Set(jtis).size).toBe(500);

  const { code, stderr } = await finish(run);
  expect(code).toBe(0);
  expect(lastLine(stderr)).toBe(exitLine(500, 1, 0));
}, 30_000);

test("sends a GET under /api/ with its method, path, query and end-to-end headers, the broker token and a new proof in place of the tool's, no Host or hop-by-hop header, and Accept-Encoding: identity", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  const run = await startedProxy(podProxyEnvironment(broker.url, dispatch.url));
  const toolBearer = fixtureSecret("tool");
  const toolProof = fixtureSecret("proof");
  const proxyBasic = fixtureSecret("basic");

  // Written by hand, since `fetch` sets its own Host, Connection and Accept-Encoding.
  const answer = await rawRequest(
    run.socket,
    [
      "GET /api/v1/issues?project=DSP&limit=2 HTTP/1.1",
      "Host: dispatch.example",
      "Connection: close, X-Hop",
      "X-Hop: dropped",
      "Keep-Alive: timeout=5",
      "Proxy-Connection: keep-alive",
      `Proxy-Authorization: Basic ${proxyBasic}`,
      "TE: trailers",
      "Trailer: X-Trail",
      `Authorization: Bearer ${toolBearer}`,
      `DPoP: ${toolProof}`,
      "Accept-Encoding: gzip, br",
      "Accept: application/json",
      "User-Agent: work-cost/1",
      "X-Tool-Header: kept",
      'If-None-Match: "v0"',
      "",
      "",
    ].join("\r\n")
  );
  // A second read, so two requests carry two proofs.
  const again = await viaSocket(run.socket, "/api/v1/issues?project=DSP");

  expect(answer.status).toBe(200);
  expect(JSON.parse(answer.body.toString("utf8"))).toEqual(ISSUES);
  expect(answer.headers["x-dispatch-test"]).toBe("kept");
  expect(again.status).toBe(200);
  expect(dispatch.received).toHaveLength(2);
  const [asked, second] = dispatch.received;
  const token = broker.tokens()[0] ?? "no trade";
  expect(asked?.method).toBe("GET");
  expect(asked?.path).toBe("/api/v1/issues");
  expect(asked?.query).toBe("?project=DSP&limit=2");
  const headers = asked?.headers ?? {};
  // The end-to-end headers, as the tool sent them.
  expect(headers.accept).toBe("application/json");
  expect(headers["user-agent"]).toBe("work-cost/1");
  expect(headers["x-tool-header"]).toBe("kept");
  expect(headers["if-none-match"]).toBe('"v0"');
  expect(headers["accept-encoding"]).toBe("identity");
  // The broker token in place of the tool's, with a proof of its own bound to the trade's key.
  expect(headers.authorization).toBe(`DPoP ${token}`);
  const proof = headers.dpop ?? "";
  expect(proof).not.toBe(toolProof);
  expect(jwtPart(proof, 0).jwk).toEqual(broker.trades[0]?.jwk);
  const claims = jwtPart(proof, 1);
  expect(claims.htm).toBe("GET");
  expect(claims.htu).toBe(`${dispatch.url}/api/v1/issues`);
  expect(claims.ath).toBe(accessTokenHash(token));
  expect(typeof claims.jti).toBe("string");
  expect(jwtPart(second?.headers.dpop ?? "", 1).jti).not.toBe(claims.jti);
  // No Host of the tool's, and no hop-by-hop header.
  expect(headers.host).toBe(`127.0.0.1:${dispatch.port}`);
  for (const name of [
    "x-hop",
    "keep-alive",
    "proxy-connection",
    "proxy-authorization",
    "te",
    "trailer",
  ]) {
    expect(headers[name], name).toBeUndefined();
  }
  expect((headers.connection ?? "").toLowerCase()).not.toContain("x-hop");
  const sent = JSON.stringify(dispatch.received.map((request) => request.headers));
  for (const secret of [toolBearer, toolProof, proxyBasic]) expect(sent).not.toContain(secret);
  expect(broker.trades[0]?.form.get("session")).toBe(SESSION);

  expect((await finish(run)).code).toBe(0);
}, 15_000);

test("sends a comment POST that carries an Idempotency-Key, with its body and that header", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  const run = await startedProxy(podProxyEnvironment(broker.url, dispatch.url));
  const keys = [randomUUID(), randomUUID()];
  const body = JSON.stringify({
    body: "Shipped, café 😀.",
    actor: { kind: "session", id: SESSION },
  });

  const answers: Answer[] = [];
  for (const key of keys) {
    answers.push(
      await viaSocket(run.socket, COMMENTS, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": key,
          Authorization: `Bearer ${fixtureSecret("tool")}`,
        },
        body,
      })
    );
  }

  expect(answers.map((answer) => answer.status)).toEqual([201, 201]);
  expect(answers.map((answer) => answer.headers["idempotency-key"])).toEqual(keys);
  expect(JSON.parse(answers[0]?.body.toString("utf8") ?? "")).toEqual({
    comment: { id: "c-1", issue_key: "DSP-1" },
  });
  const token = broker.tokens()[0] ?? "no trade";
  expect(dispatch.received.map((asked) => [asked.method, asked.path])).toEqual([
    ["POST", COMMENTS],
    ["POST", COMMENTS],
  ]);
  for (const [index, asked] of dispatch.received.entries()) {
    expect(asked.body.equals(Buffer.from(body))).toBe(true);
    expect(asked.headers["idempotency-key"]).toBe(keys[index] ?? "");
    expect(asked.headers["content-type"]).toBe("application/json");
    expect(asked.headers.authorization).toBe(`DPoP ${token}`);
    const claims = jwtPart(asked.headers.dpop ?? "", 1);
    expect(claims.htm).toBe("POST");
    expect(claims.htu).toBe(`${dispatch.url}${COMMENTS}`);
    expect(claims.ath).toBe(accessTokenHash(token));
  }
  const jtis = dispatch.received.map((asked) => jwtPart(asked.headers.dpop ?? "", 1).jti);
  expect(new Set(jtis).size).toBe(2);

  expect((await finish(run)).code).toBe(0);
}, 15_000);

test("sends the comment POST with no AskGate marker while the session holds a live AskGate record in block mode", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  // A live gate of this session, in block mode, that minted no marker for anything.
  writeRecord(SESSION);
  const run = await startedProxy(podProxyEnvironment(broker.url, dispatch.url));
  const body = JSON.stringify({ body: "Reviewed nowhere, and posted." });

  const answer = await viaSocket(run.socket, COMMENTS, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": randomUUID() },
    body,
  });

  expect(answer.status).toBe(201);
  expect(dispatch.received.map((asked) => [asked.method, asked.path])).toEqual([
    ["POST", COMMENTS],
  ]);
  expect(dispatch.received[0]?.body.toString("utf8")).toBe(body);
  expect((await finish(run)).code).toBe(0);
}, 15_000);

test("refuses a target in absolute form and one that starts with // with 400 PROXY_TARGET_REFUSED, and sends nothing", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  // A host a credential carried there would reach.
  const elsewhere = standInDispatch();
  const run = await startedProxy(podProxyEnvironment(broker.url, dispatch.url));
  const body = JSON.stringify({ body: "To the wrong host." });
  const elsewhereHost = `127.0.0.1:${elsewhere.port}`;

  const targets = [
    `GET ${dispatch.url}/api/v1/issues HTTP/1.1\r\nHost: 127.0.0.1:${dispatch.port}\r\nConnection: close\r\n\r\n`,
    `GET ${elsewhere.url}/api/v1/issues HTTP/1.1\r\nHost: ${elsewhereHost}\r\nConnection: close\r\n\r\n`,
    rawGet(`//${elsewhereHost}/api/v1/issues`),
    rawGet("//evil.example/api/v1/issues"),
    [
      `POST ${elsewhere.url}${COMMENTS} HTTP/1.1`,
      `Host: ${elsewhereHost}`,
      "Content-Type: application/json",
      `Idempotency-Key: ${randomUUID()}`,
      `Content-Length: ${Buffer.byteLength(body)}`,
      "Connection: close",
      "",
      body,
    ].join("\r\n"),
    [
      `POST //${elsewhereHost}${COMMENTS} HTTP/1.1`,
      "Host: dispatch",
      "Content-Type: application/json",
      `Idempotency-Key: ${randomUUID()}`,
      `Content-Length: ${Buffer.byteLength(body)}`,
      "Connection: close",
      "",
      body,
    ].join("\r\n"),
  ];
  for (const request of targets) {
    const line = request.slice(0, request.indexOf("\r\n"));
    const answer = await rawRequest(run.socket, request);
    expect({ line, status: answer.status }).toEqual({ line, status: 400 });
    expect(answer.headers["content-type"] ?? "", line).toStartWith("application/json");
    const { code, error } = refusalOf(answer);
    expect({ line, code }).toEqual({ line, code: "PROXY_TARGET_REFUSED" });
    expect(error.length, line).toBeGreaterThan(0);
  }

  expect(dispatch.received).toEqual([]);
  expect(elsewhere.received).toEqual([]);
  expect((await finish(run)).code).toBe(0);
}, 15_000);

test("gives back a 302 as it is and follows no redirect", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  const run = await startedProxy(podProxyEnvironment(broker.url, dispatch.url));

  const away = await viaSocket(run.socket, "/api/v1/moved");
  const here = await viaSocket(run.socket, "/api/v1/moved-here");

  expect(away.status).toBe(302);
  expect(away.headers.location).toBe("http://elsewhere.invalid/x");
  expect(here.status).toBe(302);
  expect(here.headers.location).toBe(`${dispatch.url}/api/v1/issues`);
  // Each was asked once, and the Location of neither was asked after it.
  expect(dispatch.received.map((asked) => asked.path)).toEqual([
    "/api/v1/moved",
    "/api/v1/moved-here",
  ]);
  expect((await finish(run)).code).toBe(0);
}, 15_000);

test("gives back the status, the end-to-end headers and a binary body of 1 MiB and more with the same bytes", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  const run = await startedProxy(podProxyEnvironment(broker.url, dispatch.url));

  const blob = await viaSocket(run.socket, BLOB_PATH);
  expect(blob.status).toBe(200);
  expect(blob.headers["content-type"]).toBe("application/octet-stream");
  expect(blob.headers["content-disposition"]).toBe('attachment; filename="blob.bin"');
  expect(blob.headers.etag).toBe('"v1"');
  expect(blob.headers["x-dispatch-test"]).toBe("kept");
  expect(blob.body.length).toBe(BLOB_BYTES);
  expect(blob.body.equals(dispatch.blob)).toBe(true);

  // Dispatch's own refusals come back with its own status and code.
  const forbidden = await viaSocket(run.socket, "/api/v1/issues/DSP-403");
  expect(forbidden.status).toBe(403);
  expect(JSON.parse(forbidden.body.toString("utf8"))).toEqual(FORBIDDEN);
  expect(forbidden.headers["x-dispatch-test"]).toBe("kept");
  const missing = await viaSocket(run.socket, "/api/v1/issues/DSP-404");
  expect(missing.status).toBe(404);
  expect(JSON.parse(missing.body.toString("utf8"))).toEqual(NOT_FOUND);
  expect(dispatch.received.map((asked) => asked.path)).toEqual([
    BLOB_PATH,
    "/api/v1/issues/DSP-403",
    "/api/v1/issues/DSP-404",
  ]);
  expect((await finish(run)).code).toBe(0);
}, 15_000);

test("gives back Dispatch's Content-Length with the body, unchunked, when the answer carries no Content-Encoding but identity", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  const run = await startedProxy(podProxyEnvironment(broker.url, dispatch.url));

  // Written by hand, so the row reads the proxy's header lines and its body's framing as they
  // came over the socket.
  const paths = [BLOB_PATH, "/api/v1/issues/DSP-403", "/api/v1/issues/DSP-404"];
  const answers = new Map<string, Answer>();
  for (const path of paths) answers.set(path, await rawRequest(run.socket, rawGet(path)));

  expect(dispatch.answered.map((answered) => answered.path)).toEqual(paths);
  for (const { path, headers: sent } of dispatch.answered) {
    const answer = answers.get(path);
    // Dispatch sent a length, and no content encoding.
    expect(sent["content-length"], path).toBeDefined();
    expect(sent["content-encoding"], path).toBeUndefined();
    expect(answer?.headers["content-length"], path).toBe(sent["content-length"]);
    expect(answer?.headers["transfer-encoding"], path).toBeUndefined();
    expect(answer?.body.length, path).toBe(Number(sent["content-length"]));
  }
  expect(answers.get(BLOB_PATH)?.body.equals(dispatch.blob)).toBe(true);
  expect(JSON.parse(answers.get("/api/v1/issues/DSP-403")?.body.toString("utf8") ?? "")).toEqual(
    FORBIDDEN
  );
  expect(JSON.parse(answers.get("/api/v1/issues/DSP-404")?.body.toString("utf8") ?? "")).toEqual(
    NOT_FOUND
  );
  expect((await finish(run)).code).toBe(0);
}, 15_000);

test("drops a token Dispatch answers 401 BROKER_TOKEN_NOT_ACTIVE for, trades once more, sends the request once more, and counts r = 1", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  const run = await startedProxy(podProxyEnvironment(broker.url, dispatch.url));

  const answer = await viaSocket(run.socket, DEAD_FIRST);

  expect(answer.status).toBe(200);
  expect(JSON.parse(answer.body.toString("utf8"))).toEqual({ served: true });
  const tokens = broker.tokens();
  expect(tokens).toHaveLength(2);
  expect(dispatch.received.map((asked) => [asked.path, asked.headers.authorization])).toEqual([
    [DEAD_FIRST, `DPoP ${tokens[0]}`],
    [DEAD_FIRST, `DPoP ${tokens[1]}`],
  ]);
  const { code, stderr } = await finish(run);
  expect(code).toBe(0);
  expect(lastLine(stderr)).toBe(exitLine(2, 2, 1));
}, 15_000);

test("starts its command only once the socket accepts and its first trade is made, with DISPATCH_PROXY_SOCKET set", async () => {
  // The trade takes a while, so a command started before it lands starts before its answer.
  const broker = standInBroker({ kind: "token", seconds: 3600 }, 300);
  const dispatch = standInDispatch();
  const dir = makeDirectory("command");
  const script = join(dir, "first-request.mjs");
  const report = join(dir, "report.json");
  writeFileSync(
    script,
    [
      'import { writeFileSync } from "node:fs";',
      'const socket = process.env.DISPATCH_PROXY_SOCKET ?? "";',
      "const started = Date.now();",
      'let status = "";',
      "try {",
      '  status = String((await fetch("http://dispatch/api/v1/issues", { unix: socket })).status);',
      "} catch (error) {",
      '  status = "no answer: " + String(error);',
      "}",
      "writeFileSync(process.argv[2], JSON.stringify({ socket, started, status }));",
      "",
    ].join("\n")
  );
  const socket = socketPath();
  const run = launch(
    ["proxy", "--socket", socket, "--", process.execPath, script, report],
    podProxyEnvironment(broker.url, dispatch.url),
    socket
  );

  const code = await within(run.exited, 10_000, "dispatch proxy exited once its command ended");
  expect({ code, stderr: run.printed.stderr }).toMatchObject({ code: 0 });
  const seen: unknown = JSON.parse(readFileSync(report, "utf-8"));
  // The command's one connection, made as it started, was accepted and answered.
  expect(seen).toMatchObject({ socket, status: "200" });
  const started = seen !== null && typeof seen === "object" && "started" in seen ? seen.started : 0;
  expect(broker.tokens()).toHaveLength(1);
  expect(Number(started)).toBeGreaterThanOrEqual(broker.trades[0]?.at ?? Number.POSITIVE_INFINITY);
  expect(lastLine(run.printed.stderr)).toBe(exitLine(1, 1, 0));
  expect(existsSync(socket)).toBe(false);
}, 15_000);

test("writes nothing to the session's records", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  const run = await startedProxy(podProxyEnvironment(broker.url, dispatch.url));

  expect((await viaSocket(run.socket, "/api/v1/issues")).status).toBe(200);
  const posted = await viaSocket(run.socket, COMMENTS, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": randomUUID() },
    body: JSON.stringify({ body: "Shipped." }),
  });
  expect(posted.status).toBe(201);
  expect((await viaSocket(run.socket, "/api/v1/issues/DSP-1", { method: "DELETE" })).status).toBe(
    405
  );
  expect((await rawRequest(run.socket, rawGet("//evil.example/api/v1/issues"))).status).toBe(400);
  expect((await finish(run)).code).toBe(0);

  const session = join(stateDir, "sessions", SESSION);
  expect(existsSync(join(session, "results.jsonl"))).toBe(false);
  expect(existsSync(join(session, "state.json"))).toBe(false);
}, 15_000);

test("answers 502 PROXY_TRADE_REFUSED in the broker's words and sends nothing when the token has ended and the broker refuses a new one", async () => {
  const broker = standInBroker({ kind: "token", seconds: 2 });
  const dispatch = standInDispatch();
  const run = await startedProxy(podProxyEnvironment(broker.url, dispatch.url));
  broker.answer = { kind: "refused" };
  // A real wait: the token's life is the broker's `expires_in`, counted by the one client's
  // clock, so the row waits it out rather than move a clock the client does not share.
  await Bun.sleep(Math.max(0, (broker.trades[0]?.at ?? Date.now()) + 2_500 - Date.now()));

  const read = await viaSocket(run.socket, "/api/v1/issues");
  const write = await viaSocket(run.socket, COMMENTS, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": randomUUID() },
    body: JSON.stringify({ body: "Shipped." }),
  });

  for (const [what, answer] of [
    ["the read", read],
    ["the comment POST", write],
  ] as const) {
    expect({ what, status: answer.status }).toEqual({ what, status: 502 });
    const { code, error } = refusalOf(answer);
    expect({ what, code }).toEqual({ what, code: "PROXY_TRADE_REFUSED" });
    expect(error, what).toContain(BROKER_REFUSAL);
  }
  expect(dispatch.received).toEqual([]);
  expect(broker.tokens()).toHaveLength(1);
  const { code, stderr } = await finish(run);
  expect(code).toBe(0);
  expect(lastLine(stderr)).toBe(exitLine(0, 1, 0));
}, 20_000);

test("goes on with the live token when its renewal fails", async () => {
  // 200 s is inside the 300 s renewal window from the trade on, so every request asks for a
  // renewal, and the broker answers each 503.
  const broker = standInBroker({ kind: "token", seconds: 200 });
  const dispatch = standInDispatch();
  const run = await startedProxy(podProxyEnvironment(broker.url, dispatch.url));
  broker.answer = { kind: "unavailable" };

  const statuses: number[] = [];
  for (let page = 0; page < 5; page += 1) {
    statuses.push((await viaSocket(run.socket, `/api/v1/issues?page=${page}`)).status);
  }

  expect(statuses).toEqual([200, 200, 200, 200, 200]);
  const tokens = broker.tokens();
  expect(tokens).toHaveLength(1);
  expect(dispatch.received.map((asked) => asked.headers.authorization)).toEqual(
    Array.from({ length: 5 }, () => `DPoP ${tokens[0]}`)
  );
  const { code, stderr } = await finish(run);
  expect(code).toBe(0);
  expect(lastLine(stderr)).toBe(exitLine(5, 1, 0));
}, 15_000);

test("answers 502 PROXY_DISPATCH_UNREACHABLE when Dispatch does not answer", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const run = await startedProxy(podProxyEnvironment(broker.url, unreachableUrl()));

  const read = await viaSocket(run.socket, "/api/v1/issues");
  const write = await viaSocket(run.socket, COMMENTS, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": randomUUID() },
    body: JSON.stringify({ body: "Shipped." }),
  });

  for (const [what, answer] of [
    ["the read", read],
    ["the comment POST", write],
  ] as const) {
    expect({ what, status: answer.status }).toEqual({ what, status: 502 });
    expect({ what, code: refusalOf(answer).code }).toEqual({
      what,
      code: "PROXY_DISPATCH_UNREACHABLE",
    });
  }
  expect((await finish(run)).code).toBe(0);
}, 15_000);

/** A request the proxy refuses with 405, how it is sent, and the command its answer names where
 *  the plan names one. */
interface RefusedRequest {
  readonly what: string;
  readonly send: (socket: string) => Promise<Answer>;
  readonly names?: string;
  /** A HEAD's answer, which carries no body. */
  readonly bodiless?: boolean;
}

/** The methods and paths R2 does not send, each one of the 405 table's rows. */
function refusedRequests(): readonly RefusedRequest[] {
  const json = { "Content-Type": "application/json" };
  const body = JSON.stringify({ body: "Shipped." });
  const keyed = () => ({ ...json, "Idempotency-Key": randomUUID() });
  const sent =
    (method: string, path: string, headers: Record<string, string> = {}, sentBody?: string) =>
    (socket: string) =>
      viaSocket(socket, path, {
        method,
        headers,
        ...(sentBody === undefined ? {} : { body: sentBody }),
      });
  const written = (request: string) => (socket: string) => rawRequest(socket, request);
  return [
    {
      what: "POST /api/v1/issues/DSP-1/comments with no Idempotency-Key",
      send: sent("POST", COMMENTS, json, body),
      names: "comment",
    },
    {
      what: "POST /api/v1/issues/DSP-1/messages with an Idempotency-Key",
      send: sent("POST", "/api/v1/issues/DSP-1/messages", keyed(), body),
      names: "message",
    },
    {
      what: "POST /api/v1/issues/DSP-1/comments/x with an Idempotency-Key",
      send: sent("POST", `${COMMENTS}/x`, keyed(), body),
    },
    { what: "GET /healthz", send: sent("GET", "/healthz"), names: "api" },
    { what: "GET /", send: sent("GET", "/"), names: "api" },
    { what: "GET /apix", send: sent("GET", "/apix"), names: "api" },
    { what: "HEAD /api/v1/issues", send: sent("HEAD", "/api/v1/issues"), bodiless: true },
    { what: "PUT /api/v1/issues/DSP-1", send: sent("PUT", "/api/v1/issues/DSP-1", json, body) },
    {
      what: "PATCH /api/v1/issues/DSP-1",
      send: sent("PATCH", "/api/v1/issues/DSP-1", json, JSON.stringify({ title: "Renamed" })),
    },
    { what: "DELETE /api/v1/issues/DSP-1", send: sent("DELETE", "/api/v1/issues/DSP-1") },
    { what: "OPTIONS /api/v1/issues", send: sent("OPTIONS", "/api/v1/issues") },
    {
      what: "CONNECT dispatch:443",
      send: written("CONNECT dispatch:443 HTTP/1.1\r\nHost: dispatch:443\r\n\r\n"),
    },
    {
      what: "a GET with Upgrade: websocket",
      send: written(
        [
          "GET /api/v1/issues HTTP/1.1",
          "Host: dispatch",
          "Connection: Upgrade",
          "Upgrade: websocket",
          "Sec-WebSocket-Version: 13",
          `Sec-WebSocket-Key: ${randomBytes(16).toString("base64")}`,
          "",
          "",
        ].join("\r\n")
      ),
    },
    {
      what: "the asterisk form OPTIONS *",
      send: written("OPTIONS * HTTP/1.1\r\nHost: dispatch\r\nConnection: close\r\n\r\n"),
    },
    { what: "GET /api/../healthz", send: written(rawGet("/api/../healthz")) },
    { what: "GET /api/%2e%2e/healthz", send: written(rawGet("/api/%2e%2e/healthz")) },
    { what: "GET /api/v1/./issues", send: written(rawGet("/api/v1/./issues")) },
    { what: "GET /api\\v1\\issues", send: written(rawGet("/api\\v1\\issues")) },
  ];
}

test("refuses each other method and path with 405 PROXY_METHOD_REFUSED, names the dispatch command to use, and sends nothing", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  const run = await startedProxy(podProxyEnvironment(broker.url, dispatch.url));

  for (const { what, send, names, bodiless } of refusedRequests()) {
    const answer = await send(run.socket);
    expect({ what, status: answer.status }).toEqual({ what, status: 405 });
    if (bodiless === true) continue;
    expect(answer.headers["content-type"] ?? "", what).toStartWith("application/json");
    const { code, error } = refusalOf(answer);
    expect({ what, code }).toEqual({ what, code: "PROXY_METHOD_REFUSED" });
    const commands = namedCommands(error);
    expect({ what, error, names: commands.length > 0 }).toEqual({ what, error, names: true });
    if (names !== undefined) expect(commands, `${what}: ${error}`).toContain(names);
  }

  expect(dispatch.received).toEqual([]);
  expect((await finish(run)).code).toBe(0);
}, 20_000);

test("answers each refusal of its own with 400, 405 or 502 and a code that starts with PROXY_", async () => {
  // Dispatch does not answer, and the token lives 2 s: one run meets every refusal the proxy makes.
  const broker = standInBroker({ kind: "token", seconds: 2 });
  const run = await startedProxy(podProxyEnvironment(broker.url, unreachableUrl()));
  const answers: { readonly what: string; readonly status: number; readonly code: string }[] = [];
  const record = (what: string, answer: Answer): void => {
    answers.push({ what, status: answer.status, code: refusalOf(answer).code });
  };

  record("a read Dispatch does not answer", await viaSocket(run.socket, "/api/v1/issues"));
  record(
    "a method R2 does not send",
    await viaSocket(run.socket, "/api/v1/issues/DSP-1", { method: "DELETE" })
  );
  record(
    "a target in absolute form",
    await rawRequest(
      run.socket,
      "GET http://evil.example/api/v1/issues HTTP/1.1\r\nHost: evil.example\r\nConnection: close\r\n\r\n"
    )
  );
  broker.answer = { kind: "refused" };
  // A real wait, as the trade-refused row's: the last token traded lapses 2 s after its trade.
  await Bun.sleep(Math.max(0, (broker.trades.at(-1)?.at ?? Date.now()) + 2_500 - Date.now()));
  record(
    "a read once the token has ended and the broker refuses a new one",
    await viaSocket(run.socket, "/api/v1/issues")
  );

  for (const answer of answers) {
    expect([400, 405, 502], answer.what).toContain(answer.status);
    expect(answer.code, answer.what).toStartWith("PROXY_");
    // An answer the list does not hold fails here.
    expect(PROXY_ANSWERS, answer.what).toContainEqual({ status: answer.status, code: answer.code });
  }
  // An entry of the list no case produced fails here.
  for (const listed of PROXY_ANSWERS) {
    expect(
      answers.map(({ status, code }) => ({ status, code })),
      `${listed.status} ${listed.code}`
    ).toContainEqual(listed);
  }
  expect((await finish(run)).code).toBe(0);
}, 20_000);

test("dispatch --help lists proxy, and the refusal of an unknown command names it", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  const env = podProxyEnvironment(broker.url, dispatch.url);

  const help = await ran(["--help"], env);
  expect(help.code).toBe(0);
  expect(help.stdout.split("\n")[0]).toBe("Usage: dispatch <command> [flags]");
  expect(help.stdout).toMatch(/^ {2}proxy +\S/m);

  const unknown = await ran(["frobnicate"], env);
  expect(unknown.code).toBe(2);
  const listed = /the commands are: ([^\n]*)/.exec(`${unknown.stdout}${unknown.stderr}`)?.[1] ?? "";
  expect(listed.split(/,\s*/)).toContain("proxy");
});

test("takes its own arguments: --socket, --dry-run, --help, and the command after --", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  const env = podProxyEnvironment(broker.url, dispatch.url);
  const socket = socketPath();
  const other = socketPath();
  const marker = join(makeDirectory(), "ran");
  const long = join(makeDirectory(), "s".repeat(108));
  expect(Buffer.byteLength(long)).toBeGreaterThan(107);

  const refusals: readonly (readonly [string, readonly string[]])[] = [
    ["no --socket", ["proxy", "--", "touch", marker]],
    ["a second --socket", ["proxy", "--socket", socket, "--socket", other, "--", "touch", marker]],
    ["a socket path over 107 bytes", ["proxy", "--socket", long, "--", "touch", marker]],
    [
      "a word before -- that is no flag of the proxy",
      ["proxy", "--socket", socket, "--verbose", "--", "touch", marker],
    ],
    ["a command with no -- before it", ["proxy", "--socket", socket, "touch", marker]],
    ["no command after --", ["proxy", "--socket", socket, "--"]],
    ["no -- and no command", ["proxy", "--socket", socket]],
  ];
  for (const [what, argv] of refusals) {
    const { code, stdout, stderr } = await ran(argv, env);
    expect({ what, code, stdout }).toEqual({ what, code: 2, stdout: "" });
    expect(stderr.trim().length, what).toBeGreaterThan(0);
    expect(stderr, what).not.toMatch(ANY_EXIT_LINE);
  }

  const help = await ran(["proxy", "--help"], env);
  expect(help.code).toBe(0);
  expect(help.stdout.length).toBeGreaterThan(0);
  expect(help.stderr).not.toMatch(ANY_EXIT_LINE);

  // `--socket=<path>` is the same flag, and every word after `--` is the command's, flags or not.
  const dry = await ran(
    ["proxy", "--dry-run", `--socket=${socket}`, "--", "bash", "--socket", other, "--help"],
    env
  );
  expect(dry.code).toBe(0);
  expect(dry.stdout.split("\n")).toContain(`Socket: ${socket}`);
  const after = await ran(["proxy", "--socket", socket, "--dry-run", "--", "touch", marker], env);
  expect(after.code).toBe(0);
  expect(after.stdout.split("\n")).toContain(`Socket: ${socket}`);

  for (const path of [socket, other, long, marker]) expect(existsSync(path), path).toBe(false);
  expect(broker.trades).toEqual([]);
  expect(dispatch.received).toEqual([]);
});

test("dispatch proxy --help says what the proxy does and how a tool calls it", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();

  const { code, stdout, stderr } = await ran(
    ["proxy", "--help"],
    podProxyEnvironment(broker.url, dispatch.url)
  );

  expect(code).toBe(0);
  for (const word of [
    "--socket",
    "--dry-run",
    "DISPATCH_PROXY_SOCKET",
    "Idempotency-Key",
    "AskGate",
    "--unix-socket",
    "AF_UNIX",
  ]) {
    expect(stdout, word).toContain(word);
  }
  expect(stderr).not.toMatch(ANY_EXIT_LINE);
  expect(broker.trades).toEqual([]);
});

test("--dry-run prints the Dispatch server, the session and the socket, trades nothing, makes no socket and runs no command", async () => {
  const broker = standInBroker({ kind: "token", seconds: 3600 });
  const dispatch = standInDispatch();
  const pod = podProxyEnvironment(broker.url, dispatch.url);
  const socket = socketPath();
  const marker = join(makeDirectory(), "ran");

  const dry = await ran(["proxy", "--dry-run", `--socket=${socket}`, "--", "touch", marker], pod);

  expect(dry.code).toBe(0);
  const lines = dry.stdout.split("\n");
  expect(lines).toContain(`Dispatch server: ${dispatch.url}`);
  expect(lines).toContain(`Session: ${SESSION}`);
  expect(lines).toContain(`Socket: ${socket}`);
  expect(dry.stderr).not.toMatch(ANY_EXIT_LINE);
  expect(existsSync(socket)).toBe(false);
  expect(existsSync(marker)).toBe(false);

  // Its refusals: the checks of the run itself, each exit 2, with nothing started.
  const taken = socketPath();
  writeFileSync(taken, "kept\n");
  const refusals: readonly (readonly [string, string, Environment, string])[] = [
    ["a socket path that exists", taken, pod, taken],
    [
      "no DISPATCH_TOKEN_BROKER",
      socket,
      { ...pod, DISPATCH_TOKEN_BROKER: undefined },
      "DISPATCH_TOKEN_BROKER",
    ],
    [
      "no DISPATCH_SESSION_ID",
      socket,
      { ...pod, DISPATCH_SESSION_ID: undefined },
      "DISPATCH_SESSION_ID",
    ],
    ["no AGENT_SECRETS_URL", socket, { ...pod, AGENT_SECRETS_URL: undefined }, "AGENT_SECRETS_URL"],
  ];
  for (const [what, path, env, cause] of refusals) {
    const refused = await ran(["proxy", "--dry-run", "--socket", path, "--", "touch", marker], env);
    expect({ what, code: refused.code }).toEqual({ what, code: 2 });
    expect(refused.stderr, what).toContain(cause);
    expect(refused.stderr, what).not.toMatch(ANY_EXIT_LINE);
    expect(refused.stdout, what).not.toContain("Socket: ");
    expect(existsSync(marker), what).toBe(false);
  }
  expect(readFileSync(taken, "utf-8")).toBe("kept\n");
  expect(existsSync(socket)).toBe(false);
  expect(broker.trades).toEqual([]);
  expect(dispatch.received).toEqual([]);
  // The projected token is the pod's identity: a dry run reads nothing a trade would send.
  expect(`${dry.stdout}${dry.stderr}`).not.toContain(projectedToken);
});
