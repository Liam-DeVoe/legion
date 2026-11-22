import { afterAll, afterEach, beforeEach, describe, expect, test } from "bun:test";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import type { Server, Subprocess } from "bun";

import type { DispatchProcess } from "../harness-processes";
import type { ProxiedRequest, ProxiedSocket } from "../round-robin-proxy";
import { eventually, rowLimit, runBounded } from "./bounded-subprocess";

// The proxy runs as the harness runs it, a `bun` process of its own, and this file answers as the
// two Dispatch processes it proxies and opens its websockets with `ws`'s client. This lane loads no
// preload, so the global `Response` the upstreams answer with, a gzipped body included, is Bun's
// own.
const { WebSocket: WsClient } = require("ws") as {
  WebSocket: new (
    url: string,
    options: { readonly headers: Record<string, string> }
  ) => {
    close(code?: number): void;
    on(event: "close", listener: (code: number) => void): void;
    on(event: "message", listener: (data: Buffer) => void): void;
    send(data: string): void;
  };
};

const proxyScript = fileURLToPath(new URL("../round-robin-proxy.ts", import.meta.url));
const harnessPortsModule = fileURLToPath(new URL("../harness-ports.ts", import.meta.url));
const runServer = fileURLToPath(new URL("../run-server.sh", import.meta.url));
// run-server.sh runs the server from packages/envoy, without the trailing slash `pwd` drops.
const envoyDirectory = fileURLToPath(new URL("../../../envoy", import.meta.url));

/** What an upstream saw of one request. */
interface Seen {
  readonly body: string;
  readonly cookie: string | null;
  readonly host: string | null;
  readonly method: string;
  readonly origin: string | null;
  readonly path: string;
  readonly pin: string | null;
}

/** The comments `/compressed` answers: a kibibyte and more of JSON, as an issue's comment list
 *  is, so the answer is one Dispatch's own layer compresses. */
const compressedComments = Array.from({ length: 200 }, (_, index) => ({
  body: `comment ${index} says the deploy is green and the migration has landed.`,
}));

/** A Bun server standing in for one Dispatch process: it records every request it is sent and
 *  answers with its own name; a test hook answers the status the test sets, `/events` is an event
 *  stream the test writes to, and `/socket` a websocket that names the process, echoes, and
 *  closes with 1012 when asked to. */
function startUpstream(name: DispatchProcess) {
  const encoder = new TextEncoder();
  const upstream = {
    hookStatus: 200,
    seen: [] as Seen[],
    server: undefined as unknown as Server<undefined>,
    /** The open event stream's writer and its end, once a request opened it. */
    stream: undefined as { write: (text: string) => void; end: () => void } | undefined,
  };
  upstream.server = Bun.serve({
    hostname: "127.0.0.1",
    idleTimeout: 0,
    port: 0,
    async fetch(request, server) {
      const url = new URL(request.url);
      upstream.seen.push({
        body: await request.text(),
        cookie: request.headers.get("cookie"),
        host: request.headers.get("host"),
        method: request.method,
        origin: request.headers.get("origin"),
        path: `${url.pathname}${url.search}`,
        pin: request.headers.get("x-dispatch-e2e-process"),
      });
      if (url.pathname === "/socket") {
        return server.upgrade(request, { data: undefined })
          ? undefined
          : new Response("no upgrade", { status: 400 });
      }
      if (url.pathname.includes("/_test/")) {
        return new Response(`hook on ${name}`, { status: upstream.hookStatus });
      }
      if (url.pathname === "/compressed") {
        // Dispatch gzips an answer for a caller that takes gzip
        // (api/response_compression.go), so the proxy must forward one.
        const body = JSON.stringify({ comments: compressedComments, from: name });
        if (!(request.headers.get("accept-encoding") ?? "").includes("gzip")) {
          return new Response(body, { headers: { "Content-Type": "application/json" } });
        }
        return new Response(gzipSync(Buffer.from(body)), {
          headers: {
            "Content-Encoding": "gzip",
            "Content-Type": "application/json",
            Vary: "Accept-Encoding",
          },
        });
      }
      if (url.pathname === "/events") {
        const body = new ReadableStream<Uint8Array>({
          start(controller) {
            controller.enqueue(encoder.encode(`data: opened on ${name}\n\n`));
            upstream.stream = {
              end: () => controller.close(),
              write: (text) => controller.enqueue(encoder.encode(text)),
            };
          },
        });
        return new Response(body, { headers: { "Content-Type": "text/event-stream" } });
      }
      return new Response(name);
    },
    websocket: {
      message(ws, message) {
        if (message === "close 1012") ws.close(1012, "restarting");
        else ws.send(`${name} echoes ${String(message)}`);
      },
      open(ws) {
        ws.send(`opened on ${name}`);
      },
    },
  });
  return upstream;
}

const a = startUpstream("a");
const b = startUpstream("b");
/** The proxy of the row that runs, its port, and its origin, which `beforeEach` of the
 *  "round-robin proxy" block below sets. */
let proxy: Subprocess<"ignore", "pipe", "inherit"> | undefined;
let proxyPort = 0;
let origin = "";
/** A home for every child this file starts, so none of them reads the caller's. */
const childHome = temporaryDirectory("round-robin-proxy-home");

/** The proxy as the harness runs it, a `bun` process of its own, on a port it binds itself:
 *  DISPATCH_E2E_PORT unset is port 0, so the kernel chooses, and the proxy's one line names the
 *  origin it got. No port is chosen here and given back for the proxy to bind a moment later, a
 *  gap in which any other process could take it. The proxy comes back before that line is read,
 *  so its caller holds it to stop whether or not it ever listens. */
function startProxy(): {
  readonly proxy: Subprocess<"ignore", "pipe", "inherit">;
  readonly origin: Promise<string>;
} {
  const started = Bun.spawn(["bun", proxyScript], {
    env: {
      PATH: process.env.PATH ?? "",
      HOME: childHome,
      TMPDIR: tmpdir(),
      DISPATCH_E2E_PORT_A: String(a.server.port),
      DISPATCH_E2E_PORT_B: String(b.server.port),
      DISPATCH_E2E_PROCESSES: "2",
    },
    stderr: "inherit",
    stdout: "pipe",
  });
  return { proxy: started, origin: listensOn(started) };
}

/** The origin `started` names in the one line it logs once it listens:
 *  `round-robin proxy on <origin> for ...`. */
async function listensOn(started: Subprocess<"ignore", "pipe", "inherit">): Promise<string> {
  const listens = /round-robin proxy on (http:\/\/127\.0\.0\.1:[1-9][0-9]*) /;
  const decoder = new TextDecoder();
  const reader = started.stdout.getReader();
  let logged = "";
  let listening: RegExpExecArray | null = null;
  while (listening === null) {
    const { done, value } = await reader.read();
    if (done) throw new Error(`the proxy exited before it listened: ${logged}`);
    logged += decoder.decode(value);
    listening = listens.exec(logged);
  }
  reader.releaseLock();
  return listening[1] as string;
}

afterAll(() => {
  a.server.stop(true);
  b.server.stop(true);
});

const get = (path: string, headers: Record<string, string> = {}) =>
  Bun.fetch(`${origin}${path}`, { headers });
const answeredBy = async (path: string, headers: Record<string, string> = {}) =>
  (await get(path, headers)).text();
const proxyRequests = async () =>
  (await (await get("/_proxy/requests")).json()) as ProxiedRequest[];
const proxySockets = async () => (await (await get("/_proxy/sockets")).json()) as ProxiedSocket[];

/** A websocket to the proxy, the messages it has received, and its close code once it closes. */
function openSocket(headers: Record<string, string>) {
  const socket = new WsClient(`ws://127.0.0.1:${proxyPort}/socket`, { headers });
  const messages: string[] = [];
  const waiters: { count: number; resolve: () => void }[] = [];
  socket.on("message", (data) => {
    messages.push(String(data));
    for (const waiter of waiters) if (messages.length >= waiter.count) waiter.resolve();
  });
  const closed = new Promise<number>((resolve) => socket.on("close", resolve));
  const received = (count: number) =>
    new Promise<string[]>((resolve) => {
      if (messages.length >= count) resolve(messages);
      else waiters.push({ count, resolve: () => resolve(messages) });
    });
  return { closed, received, socket };
}

describe("round-robin proxy", () => {
  // Each row starts a proxy of its own and stops it after, because once a row passes its timeout
  // Bun's test runner sends SIGTERM to every process a row or hook of the file started that still
  // runs (https://github.com/oven-sh/bun/issues/46373), so a proxy the rows shared failed every
  // row after a slow one. A new proxy starts with empty logs and both alternations at A.
  beforeEach(async () => {
    a.seen.length = 0;
    b.seen.length = 0;
    a.hookStatus = 200;
    b.hookStatus = 200;
    const started = startProxy();
    proxy = started.proxy;
    origin = await started.origin;
    proxyPort = Number(new URL(origin).port);
  });

  // A proxy the runner killed already, as one whose row timed out, has exited, and its stop
  // returns at once.
  afterEach(async () => {
    proxy?.kill();
    await proxy?.exited;
    proxy = undefined;
  });

  test("alternates requests between A and B, forwarding Host and Origin unchanged", async () => {
    const answers = [];
    for (let index = 0; index < 4; index++) {
      answers.push(await answeredBy("/who?n=1", { Origin: origin }));
    }
    expect(answers).toEqual(["a", "b", "a", "b"]);
    expect(await proxyRequests()).toEqual([
      { method: "GET", path: "/who?n=1", upstream: "a" },
      { method: "GET", path: "/who?n=1", upstream: "b" },
      { method: "GET", path: "/who?n=1", upstream: "a" },
      { method: "GET", path: "/who?n=1", upstream: "b" },
    ]);
    for (const seen of [...a.seen, ...b.seen]) {
      expect([seen.host, seen.origin]).toEqual([`127.0.0.1:${proxyPort}`, origin]);
    }
  });

  test("an answer the upstream gzipped reaches the caller whole", async () => {
    // Dispatch compresses an answer for a caller that takes gzip, and `fetch` decodes that body
    // while leaving `content-encoding: gzip` and the compressed `content-length` on the Response
    // it answers with. A proxy that hands those headers on with the decoded body describes bytes
    // that are no longer the body, and the caller's own inflate fails.
    const read = await Bun.fetch(`${origin}/compressed`, {
      headers: { "Accept-Encoding": "gzip, deflate", "X-Dispatch-E2E-Process": "a" },
    });
    expect(read.status).toBe(200);
    expect(await read.json()).toEqual({ comments: compressedComments, from: "a" });
  });

  test("a pin header beats a pin cookie, a cookie pins on its own, and pins take no turn", async () => {
    expect(await answeredBy("/who", { Cookie: "x=1; dispatch-e2e-process=b" })).toBe("b");
    expect(await answeredBy("/who", { Cookie: "dispatch-e2e-process=b" })).toBe("b");
    expect(
      await answeredBy("/who", { Cookie: "dispatch-e2e-process=b", "X-Dispatch-E2E-Process": "a" })
    ).toBe("a");
    expect(await answeredBy("/who", { "X-Dispatch-E2E-Process": "b" })).toBe("b");
    // The alternation still starts at A: no pinned request took a turn.
    expect(await answeredBy("/who")).toBe("a");
    expect(await answeredBy("/who")).toBe("b");
    // The pin header is the proxy's and stops there; the cookie reaches the server as sent.
    expect([...a.seen, ...b.seen].map((seen) => seen.pin)).toEqual([
      null,
      null,
      null,
      null,
      null,
      null,
    ]);
    expect(b.seen[0]?.cookie).toBe("x=1; dispatch-e2e-process=b");
  });

  test("a pin that names no process is refused, never read as no pin", async () => {
    const refused: Record<string, string>[] = [
      { "X-Dispatch-E2E-Process": "c" },
      { Cookie: "dispatch-e2e-process=A" },
      { Cookie: "dispatch-e2e-process=b", "X-Dispatch-E2E-Process": "" },
      { Cookie: "dispatch-e2e-page=c" },
      // A pin of the other kind than the request's is refused too.
      { Cookie: "dispatch-e2e-api=b; dispatch-e2e-page=" },
    ];
    for (const headers of refused) {
      const response = await get("/who", headers);
      expect(response.status).toBe(400);
      expect(await response.json()).toMatchObject({
        error: expect.stringContaining('the processes are "a" and "b"'),
      });
    }
    expect([...a.seen, ...b.seen]).toEqual([]);
  });

  test("a page pin and an API pin send a context's page to one process and the rest to the other", async () => {
    const split = { Cookie: "dispatch-e2e-page=b; dispatch-e2e-api=a" };
    // The page: a navigation, index.html and every asset.
    expect(await answeredBy("/issues/CORE-1", { ...split, "Sec-Fetch-Mode": "navigate" })).toBe(
      "b"
    );
    expect(await answeredBy("/index.html", split)).toBe("b");
    expect(await answeredBy("/assets/index-abc.js", split)).toBe("b");
    // Everything else: the API, sign-in (a navigation included), a websocket.
    expect(await answeredBy("/api/v1/issues", split)).toBe("a");
    expect(
      await answeredBy("/auth/_dev/signin?login=alice", { ...split, "Sec-Fetch-Mode": "navigate" })
    ).toBe("a");
    expect(await answeredBy("/favicon.svg", split)).toBe("a");
    const socket = openSocket(split);
    expect(await socket.received(1)).toEqual(["opened on a"]);
    socket.socket.close();
    await socket.closed;
    // A pin of the request's kind beats the context pin, which still pins the other kind; the
    // header beats both.
    const both = "dispatch-e2e-process=a; dispatch-e2e-page=b";
    expect(await answeredBy("/assets/index-abc.js", { Cookie: both })).toBe("b");
    expect(await answeredBy("/api/v1/issues", { Cookie: both })).toBe("a");
    expect(await answeredBy("/api/v1/issues", { Cookie: "dispatch-e2e-api=b" })).toBe("b");
    expect(
      await answeredBy("/assets/index-abc.js", { ...split, "X-Dispatch-E2E-Process": "a" })
    ).toBe("a");
    // None of them took a turn.
    expect(await answeredBy("/who")).toBe("a");
    expect(await answeredBy("/who")).toBe("b");
  });

  test("a /_test/ request goes to both processes and answers A's, or a non-2xx of either", async () => {
    const hook = (headers: Record<string, string> = {}) =>
      Bun.fetch(`${origin}/api/v1/live/_test/disconnect`, {
        body: "payload",
        headers,
        method: "POST",
      });
    const pinned = await hook({ "X-Dispatch-E2E-Process": "b" });
    expect([pinned.status, await pinned.text()]).toEqual([200, "hook on a"]);
    for (const upstream of [a, b]) {
      expect(upstream.seen.map((seen) => [seen.method, seen.path, seen.body])).toEqual([
        ["POST", "/api/v1/live/_test/disconnect", "payload"],
      ]);
    }
    expect((await proxyRequests()).map((entry) => entry.upstream)).toEqual(["a", "b"]);

    b.hookStatus = 500;
    const bFailed = await hook();
    expect([bFailed.status, await bFailed.text()]).toEqual([500, "hook on b"]);
    a.hookStatus = 503;
    const bothFailed = await hook();
    expect([bothFailed.status, await bothFailed.text()]).toEqual([503, "hook on a"]);
    b.hookStatus = 200;
    a.hookStatus = 404;
    const aFailed = await hook();
    expect([aFailed.status, await aFailed.text()]).toEqual([404, "hook on a"]);
    // A fan-out took no turn either.
    expect(await answeredBy("/who")).toBe("a");
  });

  test("a /_proxy/ path is answered by the proxy and never forwarded", async () => {
    expect(await answeredBy("/who")).toBe("a");
    expect((await get("/_proxy/elsewhere")).status).toBe(404);
    expect((await get("/api/v1/_proxy/requests")).status).toBe(404);
    expect((await Bun.fetch(`${origin}/_proxy/requests`, { method: "DELETE" })).status).toBe(405);
    expect((await get("/_proxy/reset")).status).toBe(405);
    expect(await proxyRequests()).toEqual([{ method: "GET", path: "/who", upstream: "a" }]);
    expect([...a.seen, ...b.seen].map((seen) => seen.path)).toEqual(["/who"]);

    // The reset empties both logs and starts both alternations over at A: an unpinned socket took
    // the websocket turn before it, and the next one goes to A again.
    const before = openSocket({});
    expect(await before.received(1)).toEqual(["opened on a"]);
    before.socket.close();
    await before.closed;
    expect((await Bun.fetch(`${origin}/_proxy/reset`, { method: "POST" })).status).toBe(204);
    expect(await proxyRequests()).toEqual([]);
    expect(await proxySockets()).toEqual([]);
    expect(await answeredBy("/who")).toBe("a");
    const after = openSocket({});
    expect(await after.received(1)).toEqual(["opened on a"]);
    after.socket.close();
    await after.closed;
  });

  test(
    "relays a websocket to its pinned process both ways, and logs it",
    async () => {
      const pinned = openSocket({ Cookie: "dispatch-e2e-process=b" });
      expect(await pinned.received(1)).toEqual(["opened on b"]);
      pinned.socket.send("hello");
      expect(await pinned.received(2)).toEqual(["opened on b", "b echoes hello"]);
      const opened = await proxySockets();
      expect(opened).toEqual([
        {
          id: expect.any(Number),
          open: true,
          openedAt: expect.any(Number),
          path: "/socket",
          upstream: "b",
        },
      ]);
      expect(b.seen.at(-1)?.host).toBe(`127.0.0.1:${proxyPort}`);
      pinned.socket.close();
      await pinned.closed;
      // The proxy marks the row when its own side of the socket closes, of which this process
      // sees no event; its log is read until it says so.
      await eventually(
        `the proxy to log socket ${opened[0]?.id} (/socket, pinned to b) as closed`,
        async () => (await proxySockets())[0]?.open === false
      );

      // Unpinned upgrades alternate on a turn of their own, logged in open order, and a server's
      // close code reaches the browser.
      const first = openSocket({});
      expect(await first.received(1)).toEqual(["opened on a"]);
      const second = openSocket({ "X-Dispatch-E2E-Process": "a" });
      expect(await second.received(1)).toEqual(["opened on a"]);
      const third = openSocket({});
      expect(await third.received(1)).toEqual(["opened on b"]);
      const logged = await proxySockets();
      expect(logged.map((entry) => [entry.upstream, entry.open])).toEqual([
        ["b", false],
        ["a", true],
        ["a", true],
        ["b", true],
      ]);
      expect(logged.map((entry) => entry.id)).toEqual(
        logged.map((entry) => entry.id).sort((left, right) => left - right)
      );
      first.socket.send("close 1012");
      expect(await first.closed).toBe(1012);
      second.socket.close();
      third.socket.close();
      await Promise.all([second.closed, third.closed]);
    },
    // The one wait this row ends at a bound itself: the proxy's log marking the pinned socket
    // closed, which the short bound of `eventually` ends.
    rowLimit("short")
  );

  test("streams an event stream from its pinned process as it is written", async () => {
    const response = await get("/events", { "X-Dispatch-E2E-Process": "b" });
    expect(response.headers.get("content-type")).toBe("text/event-stream");
    const reader = (response.body as ReadableStream<Uint8Array>).getReader();
    const decoder = new TextDecoder();
    const next = async () => decoder.decode((await reader.read()).value);
    expect(await next()).toBe("data: opened on b\n\n");
    // The second event is written only once the first has come through the proxy, so a proxy
    // that buffered the stream would never answer this read.
    b.stream?.write("data: second\n\n");
    expect(await next()).toBe("data: second\n\n");
    b.stream?.end();
    expect((await reader.read()).done).toBe(true);
    expect(a.seen).toEqual([]);
  });
});

describe("DISPATCH_E2E_PROCESSES", () => {
  const run = (argv: string[], env: Record<string, string>) =>
    runBounded({
      argv,
      bound: "short",
      env: { PATH: process.env.PATH ?? "", HOME: childHome, TMPDIR: tmpdir(), ...env },
      from: import.meta.url,
    });
  // A process of its own per import: the refusal is the module's evaluation, which runs once.
  const importPorts = (env: Record<string, string>) =>
    run(["bun", "-e", `await import(${JSON.stringify(harnessPortsModule)})`], env);

  // Each value is one import of its own, so the row's limit is read from the two lists.
  const refusedProcesses = ["3", "two", " 2", "0"];
  const acceptedProcesses = ["", "1", "2"];
  test(
    "every harness importer refuses a value other than 1, 2 or unset, naming those",
    async () => {
      for (const value of refusedProcesses) {
        const { code, stderr } = await importPorts({ DISPATCH_E2E_PROCESSES: value });
        expect(code).not.toBe(0);
        expect(stderr).toContain(
          `DISPATCH_E2E_PROCESSES must be "1", "2" or unset, not ${JSON.stringify(value)}`
        );
      }
      for (const value of acceptedProcesses) {
        expect((await importPorts({ DISPATCH_E2E_PROCESSES: value })).code).toBe(0);
      }
    },
    rowLimit(...[...refusedProcesses, ...acceptedProcesses].map(() => "short" as const))
  );

  test(
    "run-server.sh refuses it too, and two processes without one cookie key",
    async () => {
      const database = { DATABASE_URL: "postgres://postgres@127.0.0.1:1/never-opened" };
      const unknown = await run(["bash", runServer], { ...database, DISPATCH_E2E_PROCESSES: "3" });
      expect(unknown.code).toBe(1);
      expect(unknown.stderr).toContain("DISPATCH_E2E_PROCESSES must be 1, 2 or unset, not '3'");
      const keyless = await run(["bash", runServer], { ...database, DISPATCH_E2E_PROCESSES: "2" });
      expect(keyless.code).not.toBe(0);
      expect(keyless.stderr).toContain(
        "DISPATCH_E2E_DEV_SIGNIN_SHARED_KEY must give both processes"
      );
    },
    rowLimit("short", "short")
  );

  test(
    "run-server.sh runs DISPATCH_E2E_SERVER_BIN, when set, as it runs the one it builds",
    async () => {
      const directory = mkdtempSync(join(tmpdir(), "run-server-binary-"));
      try {
        const binary = join(directory, "envoy-dispatch");
        // The binary reports the directory it runs in, the settings run-server.sh sweeps the
        // environment down to, and how it was told to listen: the socket it inherited, or the
        // port it was handed.
        writeFileSync(
          binary,
          [
            "#!/usr/bin/env bash",
            'fd="$DISPATCH_LISTEN_FD"',
            "socket=none",
            'if [ -n "$fd" ] && [ -S "/dev/fd/$fd" ]; then',
            "  socket=listening",
            "fi",
            '[ -n "$fd" ] || fd=none',
            'echo "given binary in $PWD with DISPATCH_TEST_HOOKS=$DISPATCH_TEST_HOOKS' +
              " DISPATCH_WEB_DIST=$DISPATCH_WEB_DIST DISPATCH_LISTEN_FD=$fd socket=$socket" +
              ' DISPATCH_PORT=$DISPATCH_PORT DISPATCH_SERVER_URL=$DISPATCH_SERVER_URL"',
            "",
          ].join("\n"),
          { mode: 0o755 }
        );
        // run-server.sh reads the fakes' ports, and binds the port it is given.
        const environment = {
          DATABASE_URL: "postgres://postgres@127.0.0.1:1/never-opened",
          DISPATCH_E2E_PORT: "8777",
          DISPATCH_E2E_SERVER_BIN: binary,
          DISPATCH_E2E_SERVER_WEB_DIST: "/given/web",
          FAKE_BROKER_PORT: "9024",
          FAKE_ENVOY_PORT: "9021",
          FAKE_GITHUB_PORT: "9022",
        };
        const { code, stdout } = await run(["bash", runServer], environment);
        expect(code).toBe(0);
        expect(stdout).toBe(
          `given binary in ${envoyDirectory} with DISPATCH_TEST_HOOKS=1 DISPATCH_WEB_DIST=/given/web ` +
            "DISPATCH_LISTEN_FD=none socket=none DISPATCH_PORT=8777 " +
            "DISPATCH_SERVER_URL=http://127.0.0.1:8777\n"
        );

        const missing = await run(["bash", runServer], {
          ...environment,
          DISPATCH_E2E_SERVER_BIN: join(directory, "absent"),
        });
        expect(missing.code).not.toBe(0);
        expect(missing.stderr).toContain(
          `DISPATCH_E2E_SERVER_BIN=${join(directory, "absent")} is not an executable file`
        );

        // With no port of its own the given binary serves on the socket held for it, as a build
        // from this tree does: held-port.py hands it the listening socket as DISPATCH_LISTEN_FD
        // and names its port. A previous release adopts it (legion-envoy-v20.1.0 and later), so a
        // two-build run holds B's port like A's and sets none.
        const { DISPATCH_E2E_PORT: _, ...portless } = environment;
        const held = await run(["bash", runServer], portless);
        expect(held.code).toBe(0);
        const port = /DISPATCH_PORT=([1-9][0-9]*) /.exec(held.stdout)?.[1];
        expect(port).toBeDefined();
        expect(held.stdout).toBe(
          `given binary in ${envoyDirectory} with DISPATCH_TEST_HOOKS=1 DISPATCH_WEB_DIST=/given/web ` +
            `DISPATCH_LISTEN_FD=3 socket=listening DISPATCH_PORT=${port} ` +
            `DISPATCH_SERVER_URL=http://127.0.0.1:${port}\n`
        );
      } finally {
        rmSync(directory, { force: true, recursive: true });
      }
    },
    rowLimit("short", "short", "short")
  );

  test(
    "A's and B's ports join the collision check in two-process mode only",
    async () => {
      // The ports resolve, and a collision is refused, when `harnessPorts` is called, not on import.
      const resolve = [
        "bun",
        "-e",
        `(await import(${JSON.stringify(harnessPortsModule)})).harnessPorts()`,
      ];
      const clash = { DISPATCH_E2E_PORT_A: "9021", FAKE_ENVOY_PORT: "9021" };
      const twoProcesses = await run(resolve, { ...clash, DISPATCH_E2E_PROCESSES: "2" });
      expect(twoProcesses.code).not.toBe(0);
      expect(twoProcesses.stderr).toContain(
        "DISPATCH_E2E_PORT_A and FAKE_ENVOY_PORT name port 9021"
      );
      expect((await run(resolve, clash)).code).toBe(0);
    },
    rowLimit("short", "short")
  );
});

/** Starts a process that binds `port` and answers "squatter" there, and waits until it answers
 *  there; returns the squatter's pid, or stops it and throws. The squatter tries the port for at
 *  most 10 s by the clock, and the wait reads it for at most 10 s by the clock, plus the 1 s of its
 *  last read, so a port that takes connections and never answers holds the wait for 11 s at most.
 *  It is synchronous because the server stop it follows is: the port is taken before that stop
 *  returns. */
function squat(port: number): number {
  const squatter = `const port = Number(process.argv[1]); const until = Date.now() + 10000; while (Date.now() < until) { try { Bun.serve({ hostname: "127.0.0.1", port, fetch: () => new Response("squatter") }); break; } catch { await Bun.sleep(25); } }`;
  const started = Bun.spawnSync([
    "bash",
    "-c",
    `nohup "$0" -e "$1" "$2" >/dev/null 2>&1 & echo $!; deadline=$((SECONDS + 10)); while [ "$SECONDS" -lt "$deadline" ]; do [ "$(curl -q -s --max-time 1 "http://127.0.0.1:$2/")" = squatter ] && exit 0; sleep 0.05; done; exit 1`,
    process.execPath,
    squatter,
    String(port),
  ]);
  const pid = Number(started.stdout.toString().trim().split("\n")[0]);
  if (!started.success) {
    try {
      process.kill(pid, "SIGKILL");
    } catch {}
    throw new Error(`the squatter did not take port ${port}`);
  }
  return pid;
}

/** Runs `body` with the next server this process starts and then stops taken over at once by
 *  another process (`squat`), which binds the stopped server's port and answers "squatter" there:
 *  the race a port chosen by starting and stopping a server, and bound by a child a moment later,
 *  is open to on a box where other runs start servers. Returns what `body` returned and the ports
 *  taken over; the squatters are stopped by their pids before it returns. */
async function withSquat<T>(body: () => Promise<T>): Promise<{ result: T; squatted: number[] }> {
  const serve = Bun.serve;
  const squatted: number[] = [];
  const squatters: number[] = [];
  let armed = true;
  Bun.serve = ((...args: Parameters<typeof Bun.serve>) => {
    const server = serve(...args);
    if (!armed) return server;
    armed = false;
    const port = Number(server.port);
    const stop = server.stop.bind(server);
    server.stop = (closeActiveConnections?: boolean) => {
      const stopped = stop(closeActiveConnections);
      squatters.push(squat(port));
      squatted.push(port);
      return stopped;
    };
    return server;
  }) as typeof Bun.serve;
  try {
    return { result: await body(), squatted };
  } finally {
    Bun.serve = serve;
    for (const pid of squatters) {
      try {
        process.kill(pid, "SIGKILL");
      } catch {}
    }
  }
}

/** Whether this host shows each process's environment at `/proc/<pid>/environ`, as Linux does.
 *  The port row reads the proxy's environment there, so on a host without it that row skips, and
 *  this says so once. */
const procEnviron = existsSync("/proc/self/environ");
if (!procEnviron) {
  console.warn(
    `Skipped the proxy's port row "a proxy started as this file starts it" in ${import.meta.path}. ` +
      "This host has no /proc/<pid>/environ, from which that row reads the proxy's environment."
  );
}

describe("the proxy's port", () => {
  test(
    "control: the squat takes a port a server of this process stopped, and answers there",
    async () => {
      const { result, squatted } = await withSquat(async () => {
        const server = Bun.serve({
          hostname: "127.0.0.1",
          port: 0,
          fetch: () => new Response("own"),
        });
        const port = Number(server.port);
        server.stop(true);
        return { port, answer: await (await Bun.fetch(`http://127.0.0.1:${port}/`)).text() };
      });
      expect({ squatted, answer: result.answer }).toEqual({
        squatted: [result.port],
        answer: "squatter",
      });
    },
    {
      // It drives one squat, a synchronous wait its own clock ends within 11 s, which Bun's 5 s
      // default of a plain `bun test` would cut short; 20 s leaves that wait room under load.
      timeout: 20_000,
    }
  );

  test(
    "the squat's wait is held by the clock: on a port that takes connections and never answers, it gives up within 15 s",
    () => {
      // The kernel takes each connection into this listener's queue while the wait blocks this
      // file's event loop, so every read connects and then hears nothing until its 1 s passes.
      const holder = Bun.listen({ hostname: "127.0.0.1", port: 0, socket: { data() {} } });
      const began = performance.now();
      let thrown = "";
      try {
        squat(holder.port);
      } catch (error) {
        thrown = String(error);
      } finally {
        holder.stop(true);
      }
      const seconds = (performance.now() - began) / 1000;
      expect({ thrown, withinTheCap: seconds < 15, seconds }).toEqual({
        thrown: `Error: the squatter did not take port ${holder.port}`,
        withinTheCap: true,
        seconds: expect.any(Number),
      });
    },
    {
      // It drives one squat against a port that never answers, which the squat's own clock ends
      // within 11 s by design; 20 s is past the 15 s it asserts, so the assertion fails first.
      timeout: 20_000,
    }
  );

  test.skipIf(!procEnviron)(
    "a proxy started as this file starts it answers as itself under the squat, and is given no port: it binds its own, so none is chosen here and given back",
    async () => {
      const { result, squatted } = await withSquat(async () => {
        const started = startProxy();
        try {
          const answer = await Bun.fetch(`${await started.origin}/_proxy/requests`);
          // The environment the proxy runs with, as the kernel holds it: a port handed to it by
          // any means a squat of this process's servers cannot see would show here.
          const handed = readFileSync(`/proc/${started.proxy.pid}/environ`, "utf8")
            .split("\0")
            .filter((entry) => entry.startsWith("DISPATCH_E2E_PORT="));
          return { status: answer.status, body: await answer.text(), handed };
        } catch (error) {
          return { thrown: String(error) };
        } finally {
          started.proxy.kill();
          await started.proxy.exited;
        }
      });
      expect({ ...result, squatted }).toEqual({
        status: 200,
        body: "[]",
        handed: [],
        squatted: [],
      });
    },
    {
      // It drives a proxy's start, a `bun` process that must log its line, and at most one squat,
      // which its own clock ends within 11 s; 30 s holds both on a loaded box.
      timeout: 30_000,
    }
  );
});
