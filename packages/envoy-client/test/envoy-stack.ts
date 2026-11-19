import { closeSync, mkdirSync, openSync, readFileSync } from "node:fs";
import { createServer, type Server } from "node:net";
import * as path from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import type { Subprocess } from "bun";
import { z } from "zod";

// Destructured: claude-envoy's typecheck, which compiles this file, forbids `process.env.X`.
const { LEGION_E2E_ENVOY_STACK } = process.env;

/**
 * Whether the tests that run a real natsd and Envoy listener run: they build both with Go, which
 * a Bun-only job lacks, so they run only where `LEGION_E2E_ENVOY_STACK=1` says Go is there. With
 * it set, a missing `go` or a failed build fails the test instead of skipping it.
 */
export const envoyStackEnabled = LEGION_E2E_ENVOY_STACK === "1";

/** A natsd with JetStream and an Envoy listener on it, both on loopback ports of their own. */
export interface EnvoyStack {
  /** The listener's base URL, `http://127.0.0.1:<port>`. */
  readonly envoyUrl: string;
  /** natsd's client URL, `nats://127.0.0.1:<port>`. */
  readonly natsUrl: string;
  /** natsd's client address, `127.0.0.1:<port>`, for a proxy to front. */
  readonly natsHostPort: string;
  /** natsd's monitoring base URL, `http://127.0.0.1:<port>` (`/connz`, `/jsz`). */
  readonly monitorUrl: string;
  /**
   * Starts one more listener on the same natsd, on a free loopback port, under the first
   * listener's environment (the caller's `listenerEnv` included) and a machine id of its own, and
   * resolves once its /healthz answers healthy. The stack stops it with the rest, and the caller
   * may stop it sooner.
   */
  startListener(): Promise<StartedListener>;
  /**
   * Stops natsd, leaves the bus down for `downMs`, and starts it again on the same port and the
   * same store, resolving once it holds the `ENVOY_NOTIFICATIONS` stream again: an outage of the
   * bus under every client of the stack.
   */
  restartNats(downMs: number): Promise<void>;
  /** Stops the listeners and natsd. Their scratch directory is the caller's, and goes with it. */
  stop(): Promise<void>;
}

/** A listener `startListener` added to the stack. */
export interface StartedListener {
  /** Its base URL, `http://127.0.0.1:<port>`. */
  readonly url: string;
  /** Stops it. */
  stop(): Promise<void>;
}

/**
 * Reserves the directory one stack works in — its Go binaries, natsd's store, both homes and
 * both logs — and returns its path.
 *
 * Every test file that starts a stack calls this once per stack, at its own module scope, as
 * `const stackDirectory = useEnvoyStackDirectory()`, and hands the path to `startEnvoyStack`.
 * Two calls reserve two directories, which is what a file that starts two stacks needs.
 *
 * The call cannot live in this module's body: `temporaryDirectory` registers the `beforeAll`
 * that makes the directory and the `afterAll` that removes it where it is called, and Bun
 * evaluates an imported module once per process, so a hook here would take the scope of
 * whichever test file imported this one first.
 *
 * Module scope rather than the `describe` body that starts the stack, because Bun runs a
 * scope's `afterAll` hooks in the order they were registered: a directory reserved beside the
 * stack would be removed before the `afterAll` that stops the processes writing into it, where
 * a file's own `afterAll` runs after every `describe` of the file has finished.
 */
export function useEnvoyStackDirectory(): string {
  return temporaryDirectory("envoy-stack");
}

/** What a caller may change about the stack it starts. */
export interface EnvoyStackOptions {
  /**
   * The directory the stack works in, which the caller reserved with
   * {@link useEnvoyStackDirectory} and the fixture removes again. It exists and is empty when
   * `startEnvoyStack` is called, and one stack has it to itself.
   */
  readonly work: string;
  /**
   * Environment for the listener process, merged over the stack's own, so a caller can add a
   * setting or replace one of these. The session-stall rig (test/session-stall.ts) shortens the
   * session windows (`ENVOY_SESSION_LIVE_WINDOW`, `ENVOY_SESSION_GONE_HORIZON`) with it, which
   * is the only way to cross them in a test's seconds rather than the deployment's hour.
   */
  readonly listenerEnv?: Readonly<Record<string, string>>;
}

/** The Go module both rigs build their binaries from. */
export const ENVOY_MODULE = path.resolve(import.meta.dir, "../../envoy");
const READY_TIMEOUT_MS = 60_000;
const TAIL_LINES = 10;
const STOP_GRACE_MS = 10_000;

/** What a rig builds, and how it names itself when the build cannot run. */
export interface GoBuild {
  /** The directory the binaries go in, one named after each package's own directory. */
  readonly bin: string;
  /** The main packages of {@link ENVOY_MODULE} to build. */
  readonly packages: readonly string[];
  /** The rig, as "the <stack> stack" in the error a missing `go` raises. */
  readonly stack: string;
  /** What that same error says the rig would have built ("natsd and the listener"). */
  readonly binaries: string;
}

/** Builds `build.packages` from this checkout into `build.bin`, a binary each. */
export async function buildGoBinaries(build: GoBuild): Promise<void> {
  if (Bun.which("go") === null) {
    throw new Error(`the ${build.stack} stack needs go on PATH to build ${build.binaries}`);
  }
  // An -o ending in a separator names a directory: each main package's binary goes in it, named
  // after the package's directory.
  const spawned = Bun.spawn(["go", "build", "-o", `${build.bin}${path.sep}`, ...build.packages], {
    cwd: ENVOY_MODULE,
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stderr, code] = await Promise.all([new Response(spawned.stderr).text(), spawned.exited]);
  if (code !== 0) {
    throw new Error(
      `go build ${build.packages.join(" ")} in ${ENVOY_MODULE} exited ${code}: ${stderr}`
    );
  }
}

/**
 * `count` distinct loopback ports nothing listens on now. Every probe socket stays open until all
 * of them are bound, so the kernel cannot hand one ephemeral port out twice; one at a time, each
 * closed before the next opened, it can, and two processes of one stack then race for one port.
 */
export async function freePorts(count: number): Promise<number[]> {
  const servers: Server[] = [];
  try {
    const ports: number[] = [];
    for (let i = 0; i < count; i++) {
      const server = createServer();
      servers.push(server);
      const listening = Promise.withResolvers<void>();
      server.once("error", listening.reject);
      server.listen(0, "127.0.0.1", () => listening.resolve());
      await listening.promise;
      const address = server.address();
      if (address === null || typeof address === "string") throw new Error("no free loopback port");
      ports.push(address.port);
    }
    return ports;
  } finally {
    await Promise.all(
      servers.map((server) => {
        const closed = Promise.withResolvers<void>();
        server.close(() => closed.resolve());
        return closed.promise;
      })
    );
  }
}

/** The last `lines` lines of a log file, for an error that names why a process failed. */
export function tail(file: string, lines: number): string {
  try {
    return readFileSync(file, "utf8").trimEnd().split("\n").slice(-lines).join("\n");
  } catch {
    return "(no log)";
  }
}

/** How long a rig gives a process to come up, and how much of its log an error quotes. */
export interface ReadyBudget {
  readonly timeoutMs: number;
  readonly tailLines: number;
}

const ENVOY_READY: ReadyBudget = { timeoutMs: READY_TIMEOUT_MS, tailLines: TAIL_LINES };

/** Polls `ready` until it holds, throwing when `child` exits first or the budget passes. */
export async function awaitReady(
  what: string,
  child: Subprocess,
  log: string,
  budget: ReadyBudget,
  ready: () => Promise<boolean>
): Promise<void> {
  const deadline = Date.now() + budget.timeoutMs;
  for (;;) {
    if (child.exitCode !== null || child.signalCode !== null) {
      throw new Error(
        `${what} exited (${child.exitCode ?? child.signalCode}): ${tail(log, budget.tailLines)}`
      );
    }
    if (await ready().catch(() => false)) return;
    if (Date.now() > deadline) {
      throw new Error(
        `${what} was not ready within ${budget.timeoutMs} ms: ${tail(log, budget.tailLines)}`
      );
    }
    await Bun.sleep(100);
  }
}

const StatusSchema = z.object({ status: z.string() });
const JszSchema = z.object({
  account_details: z
    .array(z.object({ stream_detail: z.array(z.object({ name: z.string() })).optional() }))
    .optional(),
});

/** `url`'s JSON answer read with `schema`; undefined when it answers no 2xx or another shape. */
async function getJson<T>(url: string, schema: z.ZodType<T>): Promise<T | undefined> {
  const response = await fetch(url, { signal: AbortSignal.timeout(2_000) });
  if (!response.ok) return undefined;
  const parsed = schema.safeParse(await response.json());
  return parsed.success ? parsed.data : undefined;
}

/**
 * Asks `child` to stop with `signal` — the one that process takes as a stop request — and kills
 * it once the grace has passed.
 */
export async function stopProcess(child: Subprocess, signal: "SIGTERM" | "SIGINT"): Promise<void> {
  child.kill(signal);
  const timer = setTimeout(() => child.kill("SIGKILL"), STOP_GRACE_MS);
  await child.exited;
  clearTimeout(timer);
}

/**
 * Starts natsd (JetStream on) and the listener on free loopback ports, and resolves once the
 * listener's /healthz answers healthy and natsd holds the `ENVOY_NOTIFICATIONS` stream. Throws,
 * naming why, when `go` is missing, a build fails or either process does not come up. The NATS
 * server runs with no authorization at all, so every client may do anything, and the listener
 * holds the grant this checkout's `infra/core.ListenerPermissions` renders, which answers every
 * registration `consumer`.
 *
 * `options.listenerEnv` is merged over the listener's own environment, which is how a caller
 * shortens the session windows the session states are judged by.
 *
 * The stack works in `options.work`, which the caller reserved with
 * {@link useEnvoyStackDirectory}: `stop` leaves it, and the fixture removes it when the file
 * that reserved it is done, whether or not the stack ever reached `stop`.
 */
export async function startEnvoyStack(options: EnvoyStackOptions): Promise<EnvoyStack> {
  const { PATH = "" } = process.env;
  const work = options.work;
  const processes: Subprocess[] = [];
  // natsd is stopped last, after every listener: a restart leaves the newest natsd at the end of
  // `processes`, behind the listeners started before it.
  const natsProcesses = new Set<Subprocess>();
  const stop = async () => {
    const order = [...processes].reverse();
    for (const child of order.filter((one) => !natsProcesses.has(one))) {
      await stopProcess(child, "SIGTERM");
    }
    for (const child of order.filter((one) => natsProcesses.has(one))) {
      await stopProcess(child, "SIGTERM");
    }
  };
  try {
    const bin = path.join(work, "bin");
    await buildGoBinaries({
      bin,
      packages: ["./internal/testnats/cmd/natsd", "./cmd/listener"],
      stack: "Envoy",
      binaries: "natsd and the listener",
    });
    const [natsPort, monitorPort, listenerPort] = await freePorts(3);
    const natsHome = path.join(work, "nats-home");
    mkdirSync(natsHome);
    const machineID = `envoy-stack-${process.pid}`;
    const natsArgs = [
      "-p",
      String(natsPort),
      "-m",
      String(monitorPort),
      "-store_dir",
      path.join(work, "nats-store"),
    ];
    const natsLog = path.join(work, "natsd.log");
    const monitor = `http://127.0.0.1:${monitorPort}`;
    /** natsd on its one port and store, ready once it holds the notifications stream when the
     *  listener has made it (`withStream`) or once its /healthz answers ok before that. */
    const startNats = async (withStream: boolean): Promise<Subprocess> => {
      const natsOut = openSync(natsLog, "a");
      const natsd = Bun.spawn([path.join(bin, "natsd"), ...natsArgs], {
        env: { PATH, HOME: natsHome },
        stdout: natsOut,
        stderr: natsOut,
      });
      closeSync(natsOut);
      processes.push(natsd);
      natsProcesses.add(natsd);
      await awaitReady(
        "natsd",
        natsd,
        natsLog,
        ENVOY_READY,
        async () => (await getJson(`${monitor}/healthz`, StatusSchema))?.status === "ok"
      );
      if (withStream) await awaitStream(natsd);
      return natsd;
    };
    const awaitStream = (natsd: Subprocess) =>
      awaitReady("natsd's ENVOY_NOTIFICATIONS stream", natsd, natsLog, ENVOY_READY, async () => {
        const jsz = await getJson(`${monitor}/jsz?streams=true`, JszSchema);
        return (jsz?.account_details ?? []).some((account) =>
          (account.stream_detail ?? []).some((stream) => stream.name === "ENVOY_NOTIFICATIONS")
        );
      });
    let natsd = await startNats(false);

    const natsHostPort = `127.0.0.1:${natsPort}`;
    const natsUrl = `nats://${natsHostPort}`;
    let listeners = 0;
    /** One listener on `port`, under the stack's environment and a machine id and home of its own.
     *  The port is `freePorts`' own element, which a caller compiling with
     *  noUncheckedIndexedAccess reads as possibly undefined. */
    const spawnListener = async (
      port: number | undefined
    ): Promise<{ url: string; child: Subprocess }> => {
      listeners += 1;
      const name = listeners === 1 ? "listener" : `listener-${listeners}`;
      const home = path.join(work, `${name}-home`);
      mkdirSync(home);
      const url = `http://127.0.0.1:${port}`;
      const log = path.join(work, `${name}.log`);
      const out = openSync(log, "a");
      const child = Bun.spawn([path.join(bin, "listener")], {
        // The caller's environment last, so a test may replace a setting of the stack's own.
        env: {
          PATH,
          HOME: home,
          PORT: String(port),
          ENVOY_LISTEN_HOST: "127.0.0.1",
          ENVOY_MACHINE_ID: listeners === 1 ? machineID : `${machineID}-${listeners}`,
          NATS_URLS: natsUrl,
          ...options.listenerEnv,
        },
        stdout: out,
        stderr: out,
      });
      closeSync(out);
      processes.push(child);
      await awaitReady(
        `the ${name}`,
        child,
        log,
        ENVOY_READY,
        async () => (await getJson(`${url}/healthz`, StatusSchema))?.status === "healthy"
      );
      return { url, child };
    };
    const { url: envoyUrl } = await spawnListener(listenerPort);
    await awaitStream(natsd);
    const startListener = async (): Promise<StartedListener> => {
      const [port] = await freePorts(1);
      const { url, child } = await spawnListener(port);
      return { url, stop: () => stopProcess(child, "SIGTERM") };
    };
    const restartNats = async (downMs: number): Promise<void> => {
      await stopProcess(natsd, "SIGTERM");
      await Bun.sleep(downMs);
      natsd = await startNats(true);
    };
    return {
      envoyUrl,
      natsUrl,
      natsHostPort,
      monitorUrl: monitor,
      startListener,
      restartNats,
      stop,
    };
  } catch (error) {
    await stop();
    throw error;
  }
}
