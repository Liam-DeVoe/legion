import { afterEach, expect, jest, mock, test } from "bun:test";
import { readFileSync } from "node:fs";
import path from "node:path";
import { createLegionDaemonClient } from "@legion/pi-shared/daemon-client";
import { resetEnvoyPluginInterfaceForTests } from "@legion/pi-shared/interface";
import type { PiApi, SessionContext } from "@legion/pi-shared/pi-types";
import { legionRoleClaimBridge, RoleClaimStoppingError } from "@legion/pi-shared/role-claim-bridge";
import { hostAgentRegistryMock } from "@legion/pi-shared/test/host-registry";
import { restoreEnvironment } from "@legion/test-fixtures/environment";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { logger } from "@oh-my-pi/pi-utils";
import { HOST_STOP_GRACE_MS } from "./host-stop";

// The module reaches the Oh My Pi host through `@legion/pi-shared/subagent-session`, which only
// the binary resolves; the import below must come after the mock.
mock.module("@oh-my-pi/pi-coding-agent", () => ({
  copyToClipboard: async () => undefined,
  ...hostAgentRegistryMock,
}));

const { createControllerSession } = await import("./controller-session");

restoreEnvironment([
  "LEGION_CONTROLLER",
  "LEGION_TREE",
  "LEGION_BOOT_TOKEN",
  "LEGION_BOOT_TOKEN_FILE",
  "LEGION_PROJECT",
  "LEGION_SLACK_CHANNELS",
  "LEGION_DAEMON_URL",
  "LEGION_CONTROLLER_START_MESSAGE",
]);
afterEach(() => resetEnvoyPluginInterfaceForTests());

const CONTROLLER_SESSION = "ses_controller_stopping";
const TRANSCRIPT = "/sessions/-legion-default/2026-08-30T03-47-40-329Z_controller.jsonl";

/** The daemon's own answers (`packages/daemon/internal/api`'s golden test): its project is
 * `legion`, and it registers this controller as `legion-legion-controller`. */
function daemonFixture(name: string): string {
  return readFileSync(
    path.resolve(import.meta.dir, "../../contracts/fixtures/daemon-api", name),
    "utf8"
  );
}

/** The listener's 503 to a role claim, read by name and status (`listenerAskedToClaimAgain`), as
 * pi-envoy's own copy of envoy-client throws it behind the bridge. */
class ListenerAskedToClaimAgain extends Error {
  override readonly name = "EnvoyApiError";
  readonly details = { status: 503 };
}

/** What Envoy's one bound instance does with each role claim and notice subscription the
 * controller asks for, in order. */
type EnvoyStep = () => Promise<void>;

/**
 * The controller the daemon launched (`LEGION_CONTROLLER=1` and a boot token), against a daemon
 * that answers its health, its registration and its ready, the last with `answers.ready` (a 204
 * by default). Envoy's one bound instance answers each role claim with the next of `claims` and
 * each notice subscription with the next of `subscriptions` (both default to landing), recording
 * each. The host's stop is `answers.hostStop`; by default none has begun. Every exit is recorded,
 * in `exits` and as `exit <code>` on `answers.steps`, and `exited` settles at the first. Every
 * line the Oh My Pi log takes is recorded too.
 */
function daemonLaunchedController(answers: {
  readonly claims?: readonly EnvoyStep[];
  readonly subscriptions?: readonly EnvoyStep[];
  readonly ready?: () => Response;
  readonly hostStop?: () => Promise<void> | undefined;
  readonly steps?: string[];
}) {
  process.env.LEGION_CONTROLLER = "1";
  delete process.env.LEGION_TREE;
  delete process.env.LEGION_BOOT_TOKEN_FILE;
  delete process.env.LEGION_CONTROLLER_START_MESSAGE;
  process.env.LEGION_BOOT_TOKEN = fixtureSecret("boot");
  process.env.LEGION_PROJECT = "legion";
  process.env.LEGION_SLACK_CHANNELS = "";
  process.env.LEGION_DAEMON_URL = "http://daemon.test";
  const posted: string[] = [];
  const daemon = createLegionDaemonClient("http://daemon.test", {
    pluginContract: 60,
    fetch: (async (input) => {
      const route = new URL(input.toString()).pathname;
      posted.push(route);
      if (route === "/healthz") {
        return new Response(daemonFixture("health.json"), {
          headers: { "content-type": "application/json" },
        });
      }
      if (route === "/legion/v1/claims/register") {
        return new Response(daemonFixture("register-controller.json"), {
          headers: { "content-type": "application/json" },
        });
      }
      if (route === "/legion/v1/claims/ready" && answers.ready !== undefined)
        return answers.ready();
      return new Response(null, { status: 204 });
    }) as typeof fetch,
  });
  const claims: string[] = [];
  const subscriptions: string[] = [];
  legionRoleClaimBridge().instances.push({
    claim: async (_sessionID, role) => {
      claims.push(role);
      await (answers.claims?.[claims.length - 1] ?? (async () => undefined))();
    },
    subscribe: async (_sessionID, topic) => {
      subscriptions.push(topic);
      await (answers.subscriptions?.[subscriptions.length - 1] ?? (async () => undefined))();
    },
    sessionID: () => CONTROLLER_SESSION,
  });
  const exits: number[] = [];
  const exited = Promise.withResolvers<void>();
  const controller = createControllerSession({
    daemon: () => daemon,
    persistedTranscript: async () => ({ sessionFile: TRANSCRIPT, agentId: CONTROLLER_SESSION }),
    pi: {} as PiApi,
    exitProcess: (code) => {
      exits.push(code);
      answers.steps?.push(`exit ${code}`);
      exited.resolve();
      return undefined as never;
    },
    hostStop: answers.hostStop ?? (() => undefined),
  });
  const context = {
    sessionManager: { getSessionId: () => CONTROLLER_SESSION },
    ui: { notify: () => undefined, setWidget: () => undefined },
  } as unknown as SessionContext;
  const lines: { readonly level: string; readonly message: string }[] = [];
  const stopSink = logger.registerLogSink((entry) => {
    lines.push({ level: entry.level, message: entry.message });
  });
  return {
    controller,
    context,
    claims,
    subscriptions,
    exits,
    exited: exited.promise,
    lines,
    posted,
    stopSink,
  };
}

/** Lets every callback already queued run. */
function settled(): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  setImmediate(resolve);
  return promise;
}

const answered503: EnvoyStep = async () => {
  throw new ListenerAskedToClaimAgain("envoy: the role's claim met a conflict at every try");
};
const refusedForStop: EnvoyStep = async () => {
  throw new RoleClaimStoppingError("legion-legion-controller");
};

// What the stop rows below hold the controller to: any other refusal of its claim still exits Oh
// My Pi, so the daemon launches it again, and says why in the log.
test("a daemon-launched controller whose role claim Envoy refuses for another reason exits, saying why", async () => {
  const run = daemonLaunchedController({
    claims: [
      async () => {
        throw new Error("Envoy role legion-legion-controller is held by live session ses_other");
      },
    ],
  });
  try {
    await run.controller.handleSessionStart(run.context);

    expect(run.exits).toEqual([1]);
    expect(run.lines.filter(({ level }) => level === "error")).toHaveLength(1);
  } finally {
    run.stopSink();
  }
});

// The daemon-launched controller runs in RPC mode, where a SIGTERM stops Envoy's session with no
// `session_shutdown` (R24 of dispatch://INFRA-3589). Its role claim is answered 503, and the stop
// begins while it waits to claim again: the next claim is refused naming the stop
// (`RoleClaimStoppingError`). The controller exits nothing and writes no line from then on, so the
// stop's role release and DELETE run to their end.
test("a daemon-launched controller whose claim, answered 503, is refused naming the stop exits nothing and writes no line", async () => {
  const run = daemonLaunchedController({ claims: [answered503, refusedForStop] });
  try {
    await run.controller.handleSessionStart(run.context);
    await settled();

    expect(run.claims).toEqual(["legion-legion-controller", "legion-legion-controller"]);
    expect(run.exits).toEqual([]);
    // The one line is the wait after the 503, written before the stop began.
    expect(run.lines.map(({ message }) => message)).toEqual([
      expect.stringContaining("was answered 503 at attempt 1"),
    ]);
    expect(run.posted).not.toContain("/legion/v1/claims/ready");
  } finally {
    run.stopSink();
  }
});

// The same wait, and the session's `session_shutdown` stops the controller session
// (`ControllerSession.stop`): the wait ends at once, no claim is made after it, and nothing exits
// or is written.
test("a daemon-launched controller whose session stops while its claim waits after a 503 claims no more, exits nothing and writes no line", async () => {
  const run = daemonLaunchedController({ claims: [answered503] });
  try {
    const claiming = run.controller.handleSessionStart(run.context);
    while (run.lines.length === 0) await settled();
    run.controller.stop();
    await claiming;
    await settled();

    expect(run.claims).toEqual(["legion-legion-controller"]);
    expect(run.exits).toEqual([]);
    expect(run.lines.map(({ message }) => message)).toEqual([
      expect.stringContaining("was answered 503 at attempt 1"),
    ]);
  } finally {
    run.stopSink();
  }
});

// The role claim landed, and the stop begins before the controller topic's subscription: Envoy
// refuses that subscription naming the stop. The controller exits nothing, writes no line, and
// reports no ready.
test("a daemon-launched controller whose controller-topic subscription is refused naming the stop exits nothing, writes no line and reports no ready", async () => {
  const run = daemonLaunchedController({ subscriptions: [refusedForStop] });
  try {
    await run.controller.handleSessionStart(run.context);
    await settled();

    expect(run.claims).toEqual(["legion-legion-controller"]);
    expect(run.subscriptions).toEqual(["notifications.legion.legion.controller"]);
    expect(run.exits).toEqual([]);
    expect(run.lines).toEqual([]);
    expect(run.posted).not.toContain("/legion/v1/claims/ready");
  } finally {
    run.stopSink();
  }
});

/** The daemon refuses the controller's ready: its registration deadline retired the controller's
 * process and stopped it (`registrationDeadline`, `packages/daemon/internal/supervise/table.go`).
 * A 4xx is a refusal that no retry changes. */
const readyRefused = (): Response =>
  Response.json({ error: "this claim's process was retired" }, { status: 409 });

/** The host's stop, begun before the daemon's refusal comes: pi-envoy's role release, and then,
 * when the row calls `end`, its DELETE, which ends the stop. Each step is recorded on `steps`.
 * `asked` settles when the controller's exit asks for the stop, which is when its wait begins. */
function hostStopRunning(steps: string[]) {
  steps.push("release");
  const ended = Promise.withResolvers<void>();
  const asked = Promise.withResolvers<void>();
  return {
    hostStop: () => {
      asked.resolve();
      return ended.promise;
    },
    asked: asked.promise,
    end: () => {
      steps.push("delete");
      ended.resolve();
    },
  };
}

// R16 of dispatch://INFRA-3589: the daemon's registration deadline retired the daemon-launched
// controller and stopped its process. In RPC mode the SIGTERM runs pi-envoy's stop (its role
// release, then its DELETE) and emits no `session_shutdown`, so the controller has not heard of the
// stop when the daemon refuses its `claims/ready`. The exit that the refusal makes waits for the
// host's stop to end, at most its bound, and keeps its status. With no stop begun, it exits at
// once, as before.
test("a daemon-launched controller whose ready the daemon refuses while the host's stop runs exits only after the stop's DELETE, with status 1", async () => {
  const steps: string[] = [];
  const stop = hostStopRunning(steps);
  const run = daemonLaunchedController({ ready: readyRefused, hostStop: stop.hostStop, steps });
  try {
    const claiming = run.controller.handleSessionStart(run.context);
    await Promise.race([stop.asked, run.exited]);
    stop.end();
    await claiming;
    await run.exited;

    expect(steps).toEqual(["release", "delete", "exit 1"]);
    expect(run.lines.filter(({ level }) => level === "error")).toHaveLength(1);
  } finally {
    run.stopSink();
  }
});

test("a daemon-launched controller whose ready the daemon refuses with no stop begun exits at once", async () => {
  const steps: string[] = [];
  const run = daemonLaunchedController({ ready: readyRefused, steps });
  try {
    await run.controller.handleSessionStart(run.context);

    expect(steps).toEqual(["exit 1"]);
    expect(run.lines.filter(({ level }) => level === "error")).toHaveLength(1);
  } finally {
    run.stopSink();
  }
});

test("a daemon-launched controller whose ready the daemon refuses while the host's stop runs exits at the bound where the stop does not end", async () => {
  const steps: string[] = [];
  const stop = hostStopRunning(steps);
  const run = daemonLaunchedController({ ready: readyRefused, hostStop: stop.hostStop, steps });
  jest.useFakeTimers();
  try {
    const claiming = run.controller.handleSessionStart(run.context);
    await Promise.race([stop.asked, run.exited]);
    jest.advanceTimersByTime(HOST_STOP_GRACE_MS - 1);
    await Promise.resolve();
    expect(steps).toEqual(["release"]);
    jest.advanceTimersByTime(1);
    await claiming;

    expect(steps).toEqual(["release", "exit 1"]);
  } finally {
    jest.useRealTimers();
    run.stopSink();
  }
});
