import { afterEach, expect, test } from "bun:test";
import { createConnection, createServer, type Server, type Socket } from "node:net";

import {
  browserLauncher,
  browserNetwork,
  browserNetworkVariable,
  chromiumExecutable,
  chromiumLaunchOptions,
  loopbackAddress,
  loopbackSocksServer,
  resolverMaps,
} from "../browser-namespace";
import { rowLimit, runBounded } from "./bounded-subprocess";

// A local run's Chromium gets a network namespace of its own and reaches the host's loopback
// through a SOCKS5 proxy (e2e/browser-namespace.ts). These rows hold the choice of network, the
// projects that launch through it, and the proxy's host half: what it opens, what it refuses,
// and the bytes it carries. Chromium in the namespace itself needs an unprivileged user namespace,
// which a CI runner refuses, so the forcing that shows the namespace keeping a request is run by
// hand (the pull request that brought this file names it).

const opened: Server[] = [];
afterEach(() => {
  for (const server of opened.splice(0)) server.close();
});

async function listening(server: Server): Promise<number> {
  opened.push(server);
  const { promise, resolve } = Promise.withResolvers<void>();
  server.listen(0, "127.0.0.1", resolve);
  await promise;
  const address = server.address();
  if (address === null || typeof address === "string") throw new Error("no TCP port");
  return address.port;
}

/** An upstream on the host's loopback that answers each line it reads with `echo <line>`. */
function echoServer(): Promise<number> {
  return listening(
    createServer((socket) => {
      socket.setEncoding("utf8");
      socket.on("data", (chunk: string) => socket.write(`echo ${chunk}`));
    })
  );
}

/** Reads from `socket` until `bytes` have come, or it ends. */
async function readBytes(socket: Socket, bytes: number): Promise<Buffer> {
  let read = Buffer.alloc(0);
  const { promise, resolve } = Promise.withResolvers<Buffer>();
  const take = (chunk: Buffer) => {
    read = Buffer.concat([read, chunk]);
    if (read.length >= bytes) resolve(read);
  };
  socket.on("data", take);
  socket.once("end", () => resolve(read));
  socket.once("close", () => resolve(read));
  const done = await promise;
  socket.off("data", take);
  return done;
}

/** A SOCKS5 CONNECT through the proxy on `proxy` to `host` (an IPv4 address or a name) and
 *  `port`: the reply's code, and the socket, joined to the upstream on success. */
async function connectThrough(
  proxy: number,
  host: string,
  port: number
): Promise<{ code: number | undefined; socket: Socket }> {
  const socket = createConnection({ host: "127.0.0.1", port: proxy });
  const { promise, resolve, reject } = Promise.withResolvers<void>();
  socket.once("connect", resolve);
  socket.once("error", reject);
  await promise;
  socket.write(Buffer.from([5, 1, 0]));
  expect([...(await readBytes(socket, 2))]).toEqual([5, 0]);
  const address = /^\d+\.\d+\.\d+\.\d+$/.test(host)
    ? Buffer.from([1, ...host.split(".").map(Number)])
    : Buffer.concat([Buffer.from([3, host.length]), Buffer.from(host, "latin1")]);
  const portBytes = Buffer.alloc(2);
  portBytes.writeUInt16BE(port);
  socket.write(Buffer.concat([Buffer.from([5, 1, 0]), address, portBytes]));
  const answer = await readBytes(socket, 10);
  return { code: answer[1], socket };
}

test("the browser's network is its own unless CI or the variable says otherwise, and no other value is read", () => {
  expect(browserNetwork({})).toBe("own");
  expect(browserNetwork({ CI: "true" })).toBe("host");
  expect(browserNetwork({ CI: "true", [browserNetworkVariable]: "own" })).toBe("own");
  expect(browserNetwork({ [browserNetworkVariable]: "host" })).toBe("host");
  expect(() => browserNetwork({ [browserNetworkVariable]: "none" })).toThrow(
    `${browserNetworkVariable} is "none"`
  );
});

test("a Chromium launch names the launcher where the network is its own, and keeps its arguments", () => {
  const args = ["--enable-precise-memory-info"];
  expect(chromiumLaunchOptions({ args }, {})).toEqual({ args, executablePath: browserLauncher });
  expect(chromiumLaunchOptions({ args }, { CI: "true" })).toEqual({ args });
});

// Every Chromium project the config runs on this machine's network launches through the
// launcher, and none else does: the visual projects run in a container with no network of its
// own, and the launcher starts Chromium alone. The config is read in a child, as Playwright reads
// it, with the variable unset and with CI set.
test(
  "the config launches every Chromium project but the visual ones through the launcher, and none on CI",
  async () => {
    const read = async (extra: Record<string, string>) => {
      const run = await runBounded({
        argv: [
          process.execPath,
          "-e",
          'const c = (await import("./e2e/playwright.config.ts")).default; console.log(JSON.stringify(c.projects.map((p) => [p.name, p.use?.browserName ?? p.use?.defaultBrowserType, p.use?.launchOptions?.executablePath ?? null])));',
        ],
        bound: "short",
        cwd: new URL("../..", import.meta.url).pathname,
        env: { HOME: process.env.HOME, PATH: process.env.PATH, ...extra },
        from: import.meta.url,
      });
      expect(run.code).toBe(0);
      return JSON.parse(run.stdout) as [string, string, string | null][];
    };
    const own = await read({});
    const launched = own.filter(([, , path]) => path === browserLauncher).map(([name]) => name);
    expect(launched.sort()).toEqual(
      own
        .filter(([name, engine]) => engine === "chromium" && !name.startsWith("visual"))
        .map(([name]) => name)
        .sort()
    );
    expect(launched).toContain("chromium");
    expect(launched).toContain("stub");
    expect((await read({ CI: "true" })).filter(([, , path]) => path !== null)).toEqual([]);
  },
  rowLimit("short", "short")
);

test("the proxy resolves the names Chromium's own resolver rules map, and loopback alone", () => {
  const names = resolverMaps([
    "--headless",
    "--host-resolver-rules=MAP dispatch-e2e.test 127.0.0.1, MAP *.wild 127.0.0.1",
  ]);
  expect([...names]).toEqual([["dispatch-e2e.test", "127.0.0.1"]]);
  expect(loopbackAddress("127.0.0.1", names)).toBe("127.0.0.1");
  expect(loopbackAddress("127.12.0.9", names)).toBe("127.12.0.9");
  expect(loopbackAddress("Dispatch-E2E.test", names)).toBe("127.0.0.1");
  expect(loopbackAddress("localhost", names)).toBe("127.0.0.1");
  expect(loopbackAddress("0:0:0:0:0:0:0:1", names)).toBe("::1");
  expect(loopbackAddress("10.0.0.1", names)).toBeUndefined();
  expect(loopbackAddress("example.com", names)).toBeUndefined();
  expect(loopbackAddress("0:0:0:0:0:0:0:2", names)).toBeUndefined();
});

test(
  "the proxy carries a connection to a loopback port both ways, by address and by mapped name",
  async () => {
    const proxy = await listening(
      loopbackSocksServer(new Map([["dispatch-e2e.test", "127.0.0.1"]]))
    );
    const echo = await echoServer();
    for (const host of ["127.0.0.1", "dispatch-e2e.test"]) {
      const { code, socket } = await connectThrough(proxy, host, echo);
      expect(code).toBe(0);
      socket.write("ping");
      expect((await readBytes(socket, "echo ping".length)).toString()).toBe("echo ping");
      socket.destroy();
    }
  },
  rowLimit("short")
);

test(
  "the proxy refuses an address off loopback, a name nothing maps, and says a closed port refused",
  async () => {
    const proxy = await listening(loopbackSocksServer(new Map()));
    const closed = await listening(createServer());
    opened.pop()?.close();
    for (const [host, port, code] of [
      ["10.0.0.1", 80, 2],
      ["example.com", 80, 2],
      ["127.0.0.1", closed, 5],
    ] as const) {
      const { code: answered, socket } = await connectThrough(proxy, host, port);
      expect({ host, code: answered }).toEqual({ host, code });
      socket.destroy();
    }
  },
  rowLimit("short")
);

test("the launcher starts the headless shell for a headless launch and Chromium itself for a headed one", () => {
  expect(chromiumExecutable(["--headless", "--no-sandbox"])).toMatch(/chrome-headless-shell$/);
  expect(chromiumExecutable(["--no-sandbox"])).toMatch(/\/chrome$/);
});
