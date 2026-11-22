/** A local run's Chromium in a network namespace of its own.
 *
 *  Chromium fails every request that is waiting for a connection with `net::ERR_NETWORK_CHANGED`
 *  when the network namespace it runs in gains or loses an address. A host that starts and stops
 *  docker networks does that all day - a dev box shared by many runs did it 44 times in one minute
 *  - and a request waits for a connection whenever the page has six open to one origin already,
 *  as it does while it loads. A lazy chunk that fails that way makes the app reload the page
 *  (`DeploymentResilience.tsx`'s `vite:preloadError`), and the row reading it fails on a page it
 *  did not load: "Execution context was destroyed".
 *
 *  So a local run starts Chromium through `e2e/browser-namespace.sh` (`launchOptions
 *  .executablePath`, `chromiumLaunchOptions` below), which runs this file's `launch`:
 *
 *    - a new user namespace and network namespace (`unshare`), with its loopback device up and
 *      one dummy link that is up and holds a documentation address, 192.0.2.1: Chromium counts a
 *      namespace with no link but loopback as offline (`navigator.onLine` false), which the app
 *      reads, and nothing the host does reaches either link;
 *    - the browser mapped back to this user's own uid and gid, as it runs on the host;
 *    - a SOCKS5 proxy for every connection Chromium makes, loopback included (`--proxy-server`,
 *      `--proxy-bypass-list=<-loopback>`): inside the namespace it listens on 127.0.0.1:1080 and
 *      carries each connection through a Unix socket to its other half on the host, which opens
 *      it to the address asked for on the host's loopback - any port, so the stack's listeners
 *      and a server a row starts for itself are reached alike - and to nothing else. A page's
 *      origin is still the URL it opened, so `http://127.0.0.1:<port>` stays the secure context
 *      the app needs; a name a project maps with `--host-resolver-rules` (`MAP <name> <ip>`, the
 *      plain-HTTP projects') is mapped the same way here.
 *
 *  `DISPATCH_E2E_BROWSER_NETWORK` chooses: `own` for this namespace, `host` for the host's
 *  network. Unset, it is `host` on CI, where no job asked for this namespace and a job's browser
 *  runs in a container of its own, and elsewhere `own` where this machine lets this user make it,
 *  else `host`, said once on the run's output with the reason (an issue pod's runtime may refuse
 *  an unprivileged user namespace). Firefox and WebKit, and the visual review's Chromium, which
 *  runs in a container with no network of its own (`e2e/visual-run.sh`), are started as before. */
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { createRequire } from "node:module";
import { createConnection, createServer, isIPv4, type Server } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import type { LaunchOptions } from "@playwright/test";

import { pipeBoth, relaySocketsToPorts } from "./visual-relay";

export const browserNetworkVariable = "DISPATCH_E2E_BROWSER_NETWORK";

export type BrowserNetwork = "own" | "host";

/** The namespace's own setup, run as its root: the loopback device and the dummy link up, then
 *  the browser's command under a user namespace that maps this user's uid and gid back. Its
 *  arguments are the uid, the gid and the command. */
const namespaceSetup = [
  "set -e",
  "ip link set lo up",
  "ip link add dispatch0 type dummy",
  "ip address add 192.0.2.1/32 dev dispatch0",
  "ip link set dispatch0 up",
  "uid=$1 gid=$2",
  "shift 2",
  'exec unshare --user --map-user="$uid" --map-group="$gid" -- "$@"',
].join("\n");

/** The `unshare` command that makes the namespace and runs `command` in it as this user. */
function inNamespace(command: readonly string[]): string[] {
  return [
    "unshare",
    "--user",
    "--map-root-user",
    "--net",
    "--",
    "sh",
    "-c",
    namespaceSetup,
    "browser-namespace",
    String(process.getuid?.() ?? 0),
    String(process.getgid?.() ?? 0),
    ...command,
  ];
}

let probed: { readonly refusal: string | undefined } | undefined;

/** Why this machine refuses this user the namespace, read by making one and running `true` in
 *  it exactly as the launcher runs the browser, once per process; undefined where it makes one. */
export function namespaceRefusal(): string | undefined {
  if (probed === undefined) {
    const [program, ...args] = inNamespace(["true"]);
    const made = spawnSync(program ?? "", args, { encoding: "utf8", timeout: 10_000 });
    probed = {
      refusal:
        made.status === 0
          ? undefined
          : (made.error?.message ?? (made.stderr.trim() || `exit ${made.status ?? made.signal}`)),
    };
  }
  return probed.refusal;
}

let toldHost = false;

/** The network a run's Chromium gets: what `DISPATCH_E2E_BROWSER_NETWORK` names; unset, `host`
 *  on CI, else `own` unless `refusal` says why this machine makes no namespace, which the run's
 *  own process (not a worker) says once. Any other value is refused rather than read as either. */
export function browserNetwork(
  env: NodeJS.ProcessEnv = process.env,
  refusal: () => string | undefined = namespaceRefusal
): BrowserNetwork {
  const named = env[browserNetworkVariable];
  if (named === "own" || named === "host") return named;
  if (named !== undefined && named !== "") {
    throw new Error(
      `${browserNetworkVariable} is ${JSON.stringify(named)}: it is "own" (Chromium in a network ` +
        `namespace of its own) or "host" (Chromium on the host's network), or unset.`
    );
  }
  if (env.CI) return "host";
  const refused = refusal();
  if (refused === undefined) return "own";
  if (!toldHost && env.TEST_WORKER_INDEX === undefined) {
    toldHost = true;
    console.log(
      `e2e: Chromium runs on the host's network, where a network the host adds or removes fails ` +
        `the requests waiting for a connection: this machine made no network namespace for it ` +
        `(${refused}). ${browserNetworkVariable}=own makes that an error.`
    );
  }
  return "host";
}

/** The executable Playwright starts in place of Chromium for a namespace of its own. */
export const browserLauncher = fileURLToPath(new URL("./browser-namespace.sh", import.meta.url));

/** `options` as a Chromium project or row launches with them: started through the launcher
 *  where the browser gets a namespace of its own. Every Chromium launch of a local run goes
 *  through here, `args` and all, since a row's own `launchOptions` replace a project's whole. */
export function chromiumLaunchOptions(
  options: LaunchOptions = {},
  env: NodeJS.ProcessEnv = process.env
): LaunchOptions {
  return browserNetwork(env) === "own" ? { ...options, executablePath: browserLauncher } : options;
}

/** Where Chromium reaches its proxy inside its namespace. The namespace is the browser's alone,
 *  so no other listener can hold the port. */
export const proxyPort = 1080;

/** A SOCKS5 reply with no bound address, as Chromium reads one: `code` 0 is success. */
function reply(code: number): Buffer {
  return Buffer.from([5, code, 0, 1, 0, 0, 0, 0, 0, 0]);
}

/** SOCKS5 reply codes (RFC 1928, 6). */
const socks = {
  succeeded: 0,
  generalFailure: 1,
  notAllowed: 2,
  connectionRefused: 5,
  commandNotSupported: 7,
  addressNotSupported: 8,
} as const;

/** A CONNECT request read out of `buffered`: the length it took, its command, the host it names
 *  (an address, or a name) and the port; undefined while the request is incomplete; null for an
 *  address type SOCKS5 does not define. */
function readRequest(
  buffered: Buffer
): { length: number; command: number; host: string; port: number } | null | undefined {
  if (buffered.length < 5) return undefined;
  const type = buffered[3];
  let at = 4;
  let host: string;
  if (type === 1) {
    if (buffered.length < at + 4 + 2) return undefined;
    host = [...buffered.subarray(at, at + 4)].join(".");
    at += 4;
  } else if (type === 3) {
    const length = buffered[at] ?? 0;
    if (buffered.length < at + 1 + length + 2) return undefined;
    host = buffered.subarray(at + 1, at + 1 + length).toString("latin1");
    at += 1 + length;
  } else if (type === 4) {
    if (buffered.length < at + 16 + 2) return undefined;
    const words: string[] = [];
    for (let word = 0; word < 8; word += 1) {
      words.push(buffered.readUInt16BE(at + word * 2).toString(16));
    }
    host = words.join(":");
    at += 16;
  } else {
    return null;
  }
  return { command: buffered[1] ?? 0, host, length: at + 2, port: buffered.readUInt16BE(at) };
}

/** The host's loopback address `host` stands for, or undefined where it names anything else:
 *  an IPv4 address in 127.0.0.0/8, IPv6's ::1, `localhost`, or a name `names` maps to one. */
export function loopbackAddress(
  host: string,
  names: ReadonlyMap<string, string>
): string | undefined {
  const mapped =
    names.get(host.toLowerCase()) ?? (host.toLowerCase() === "localhost" ? "127.0.0.1" : host);
  if (isIPv4(mapped)) return mapped.startsWith("127.") ? mapped : undefined;
  return mapped === "0:0:0:0:0:0:0:1" || mapped === "::1" ? "::1" : undefined;
}

/** The names `--host-resolver-rules` maps to an address in Chromium's own arguments, so the
 *  proxy resolves them as Chromium would: each rule `MAP <name> <address>` with no wildcard. */
export function resolverMaps(args: readonly string[]): Map<string, string> {
  const names = new Map<string, string>();
  for (const arg of args) {
    if (!arg.startsWith("--host-resolver-rules=")) continue;
    for (const rule of arg.slice("--host-resolver-rules=".length).split(",")) {
      const [verb, name, address] = rule.trim().split(/\s+/);
      if (verb === "MAP" && name !== undefined && address !== undefined && !name.includes("*")) {
        names.set(name.toLowerCase(), address);
      }
    }
  }
  return names;
}

/** The host half of the proxy: a SOCKS5 server (no authentication, CONNECT alone) that opens
 *  each connection asked of it on the host's loopback (`loopbackAddress`) and refuses the rest. */
export function loopbackSocksServer(names: ReadonlyMap<string, string>): Server {
  return createServer({ allowHalfOpen: true }, (client) => {
    let buffered = Buffer.alloc(0);
    let greeted = false;
    const refuse = (code: number) => {
      client.end(reply(code));
    };
    const read = (chunk: Buffer) => {
      buffered = Buffer.concat([buffered, chunk]);
      if (!greeted) {
        if (buffered.length < 2) return;
        if (buffered[0] !== 5) {
          client.destroy();
          return;
        }
        const count = buffered[1] ?? 0;
        if (buffered.length < 2 + count) return;
        const methods = buffered.subarray(2, 2 + count);
        buffered = buffered.subarray(2 + count);
        if (!methods.includes(0)) {
          client.end(Buffer.from([5, 0xff]));
          return;
        }
        greeted = true;
        client.write(Buffer.from([5, 0]));
      }
      const request = readRequest(buffered);
      if (request === undefined) return;
      client.off("data", read);
      client.pause();
      if (request === null) {
        refuse(socks.addressNotSupported);
        return;
      }
      if (request.command !== 1) {
        refuse(socks.commandNotSupported);
        return;
      }
      const address = loopbackAddress(request.host, names);
      if (address === undefined) {
        refuse(socks.notAllowed);
        return;
      }
      const early = buffered.subarray(request.length);
      const upstream = createConnection({ allowHalfOpen: true, host: address, port: request.port });
      const failed = (error: NodeJS.ErrnoException) => {
        refuse(error.code === "ECONNREFUSED" ? socks.connectionRefused : socks.generalFailure);
      };
      upstream.once("error", failed);
      upstream.once("connect", () => {
        upstream.off("error", failed);
        client.write(reply(socks.succeeded));
        if (early.length > 0) upstream.write(early);
        pipeBoth(client, upstream);
        client.resume();
      });
    };
    client.on("data", read);
    client.on("error", () => client.destroy());
  });
}

/** The Chromium executable Playwright would have started for these arguments: the headless shell
 *  for a headless launch, as Playwright picks it, else Chromium itself. Read from Playwright's own
 *  registry of the browsers it installed, through `@playwright/test`'s own dependencies. */
export function chromiumExecutable(args: readonly string[]): string {
  const name = args.includes("--headless") ? "chromium-headless-shell" : "chromium";
  const fromHere = createRequire(import.meta.url);
  const fromTest = createRequire(fromHere.resolve("@playwright/test/package.json"));
  const fromPlaywright = createRequire(fromTest.resolve("playwright/package.json"));
  const bundle: unknown = fromPlaywright("playwright-core/lib/coreBundle");
  const path = registryExecutable(bundle, name);
  if (path === undefined) {
    throw new Error(
      `e2e/browser-namespace.ts: Playwright's registry names no executable for ${name}; ` +
        `run Chromium on the host's network (${browserNetworkVariable}=host) until it is read again.`
    );
  }
  return path;
}

/** `playwright-core/lib/coreBundle`'s `registry.registry.findExecutable(name).executablePath()`,
 *  each step checked, since the bundle is Playwright's own and not a typed API. */
function registryExecutable(bundle: unknown, name: string): string | undefined {
  const step = (value: unknown, key: string): unknown =>
    typeof value === "object" && value !== null && key in value
      ? Reflect.get(value, key)
      : undefined;
  const registry = step(step(bundle, "registry"), "registry");
  const find = step(registry, "findExecutable");
  if (typeof find !== "function") return undefined;
  const executable: unknown = Reflect.apply(find, registry, [name]);
  const path = step(executable, "executablePath");
  if (typeof path !== "function") return undefined;
  const value: unknown = Reflect.apply(path, executable, []);
  return typeof value === "string" ? value : undefined;
}

/** Runs `command` with this process's standard streams and Chromium's two pipes (descriptors 3
 *  and 4, `--remote-debugging-pipe`), passes it the signals this process gets, and exits as it
 *  does. */
function runAsChild(command: readonly string[], then: () => void): void {
  const [program, ...args] = command;
  const child = spawn(program ?? "", args, { stdio: ["inherit", "inherit", "inherit", 3, 4] });
  for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"] as const) {
    process.on(signal, () => child.kill(signal));
  }
  child.on("error", (error) => {
    console.error(`e2e/browser-namespace.ts: ${program}: ${error.message}`);
    then();
    process.exit(127);
  });
  child.on("exit", (code, signal) => {
    then();
    if (signal !== null) process.kill(process.pid, signal);
    process.exit(code ?? 1);
  });
}

/** `launch <Chromium's arguments>`: on the host, the proxy's host half on a Unix socket, and the
 *  namespace with the browser in it. */
async function launch(args: readonly string[]): Promise<void> {
  const executable = chromiumExecutable(args);
  const directory = mkdtempSync(join(tmpdir(), "dispatch-browser-net-"));
  const server = loopbackSocksServer(resolverMaps(args));
  const { promise, resolve, reject } = Promise.withResolvers<void>();
  server.once("error", reject);
  server.listen(join(directory, String(proxyPort)), resolve);
  await promise;
  runAsChild(
    inNamespace([
      process.execPath,
      fileURLToPath(import.meta.url),
      "inside",
      directory,
      "--",
      executable,
      ...args,
      `--proxy-server=socks5://127.0.0.1:${proxyPort}`,
      "--proxy-bypass-list=<-loopback>",
    ]),
    () => {
      server.close();
      rmSync(directory, { force: true, recursive: true });
    }
  );
}

/** `inside <directory> -- <command>`: in the namespace, 127.0.0.1:1080 carried to the host half's
 *  socket under `directory`, then the browser. */
async function inside(argv: readonly string[]): Promise<void> {
  const [directory, separator, ...command] = argv;
  if (directory === undefined || separator !== "--" || command.length === 0) {
    throw new Error("usage: browser-namespace.ts inside <directory> -- <command>...");
  }
  await relaySocketsToPorts(directory, [{ listenPort: proxyPort, socketPort: proxyPort }]);
  runAsChild(command, () => {});
}

if (import.meta.main) {
  const [mode, ...rest] = process.argv.slice(2);
  const run = mode === "launch" ? launch(rest) : mode === "inside" ? inside(rest) : undefined;
  if (run === undefined) {
    console.error(
      "usage: browser-namespace.ts launch <Chromium's arguments>... | inside <directory> -- <command>..."
    );
    process.exit(2);
  }
  run.catch((error: unknown) => {
    console.error(
      `e2e/browser-namespace.ts: ${error instanceof Error ? error.message : String(error)}`
    );
    process.exit(70);
  });
}
