import { afterEach, beforeAll, describe, expect, it, mock, spyOn } from "bun:test";
import { readFileSync, symlinkSync } from "node:fs";
import path from "node:path";
import { createLegionDaemonClient } from "@legion/pi-shared/daemon-client";
import { resetEnvoyPluginInterfaceForTests } from "@legion/pi-shared/interface";
import type { SessionContext } from "@legion/pi-shared/pi-types";
import { legionRoleClaimBridge, RoleClaimStoppingError } from "@legion/pi-shared/role-claim-bridge";
import { hostAgentRegistryMock } from "@legion/pi-shared/test/host-registry";
import { restoreEnvironment } from "@legion/test-fixtures/environment";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";

// The module reaches the Oh My Pi host through `@legion/pi-shared/subagent-session`, which only
// the binary resolves; the import below must come after the mock.
mock.module("@oh-my-pi/pi-coding-agent", () => ({
  copyToClipboard: async () => undefined,
  ...hostAgentRegistryMock,
}));

const { attributionInputs, createClaimSession } = await import("./claim-session");

restoreEnvironment(["LEGION_BOOT_TOKEN", "LEGION_STATE_DIR", "LEGION_CLAIM_FIRST_TURN_BOUND_MS"]);
afterEach(() => resetEnvoyPluginInterfaceForTests());

const ROOT_TRANSCRIPT =
  "/sessions/-legion-default/2026-08-30T03-47-40-329Z_01a050c7-b4a9-7000-a539-240e20271441.jsonl";
const ROOT_ID = "01a050c7-b4a9-7000-a539-240e20271441";

// The pane's workspace reaches `attributionInputs` as the daemon set it; jj matches a scope
// against the root it resolved, so a link's own path must never reach the overlay. The path is
// reserved here and the directory made in the `beforeAll` the fixture registers, so a `bun test
// -t <pattern>` that selects no row of this file makes nothing and leaves nothing behind; the
// link is made in a hook of this file's own, which Bun runs after the fixture's.
const REAL_WORKSPACE = temporaryDirectory("claim-session-ws", { real: true });
const WORKSPACE = path.join(REAL_WORKSPACE, "link");
beforeAll(() => {
  symlinkSync(REAL_WORKSPACE, WORKSPACE);
});

describe("attributionInputs", () => {
  it("reads the session id, the state directory, the trailer key and the workspace", () => {
    expect(
      attributionInputs(ROOT_TRANSCRIPT, {
        LEGION_STATE_DIR: "/state",
        LEGION_ISSUE_TRAILER: "ACME-7",
        LEGION_WORKSPACE: WORKSPACE,
      })
    ).toEqual({
      sessionId: ROOT_ID,
      stateDir: "/state",
      issueKey: "ACME-7",
      workspace: REAL_WORKSPACE,
    });
  });

  // jj matches a `[[--scope]]` against the workspace root it resolved, and a path that does not
  // resolve matches nothing and says nothing: the trailer is then the session line alone.
  it.each([
    ["no LEGION_WORKSPACE", undefined],
    ["an empty LEGION_WORKSPACE", ""],
    ["a workspace that does not exist", "/no/such/workspace"],
  ])("takes no workspace from %s", (_name, workspace) => {
    expect(
      attributionInputs(ROOT_TRANSCRIPT, {
        LEGION_STATE_DIR: "/state",
        LEGION_ISSUE_TRAILER: "ACME-7",
        ...(workspace === undefined ? {} : { LEGION_WORKSPACE: workspace }),
      }).workspace
    ).toBeUndefined();
  });

  // The daemon sets LEGION_ISSUE for every pane; it sets LEGION_ISSUE_TRAILER only for an issue
  // whose repository the deployment names. A caller reading LEGION_ISSUE would stamp a Dispatch
  // key into the commits of a repository it never named (dispatch://INFRA-472).
  it("takes no key from LEGION_ISSUE", () => {
    expect(
      attributionInputs(ROOT_TRANSCRIPT, { LEGION_STATE_DIR: "/state", LEGION_ISSUE: "ACME-7" })
        .issueKey
    ).toBeUndefined();
  });

  it("takes no key from an empty LEGION_ISSUE_TRAILER", () => {
    expect(
      attributionInputs(ROOT_TRANSCRIPT, { LEGION_STATE_DIR: "/state", LEGION_ISSUE_TRAILER: "" })
        .issueKey
    ).toBeUndefined();
  });

  it("yields no session id for a subagent transcript", () => {
    expect(
      attributionInputs("/sessions/-x/2026-08-30T03-47-40-329Z_abc/Worker.jsonl", {
        LEGION_STATE_DIR: "/state",
        LEGION_ISSUE_TRAILER: "ACME-7",
      }).sessionId
    ).toBeUndefined();
  });

  it("fails the pane that has no state directory", () => {
    expect(() => attributionInputs(ROOT_TRANSCRIPT, {})).toThrow(
      "LEGION_STATE_DIR is required for Legion"
    );
  });
});

const PANE_SESSION = "ses_pane_stopping";
/** A transcript no session id is read from: the boot writes no jj attribution. */
const SUBAGENT_TRANSCRIPT = "/sessions/-x/2026-08-30T03-47-40-329Z_abc/Worker.jsonl";
const STATE_DIR = temporaryDirectory("claim-session-state");

/** Lets every callback already queued run: the claim step's failure path is microtasks alone. */
function settled(): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  setImmediate(resolve);
  return promise;
}

/** The pane's claim, booted against a daemon that registers and readies it, with a first-turn
 * bound of 0 and every exit it makes recorded. Envoy's one bound instance refuses each claim
 * naming the session's stop (`RoleClaimStoppingError`), as pi-envoy's instance does once its
 * stop began; `reached` settles once a claim reached it. */
function pane() {
  process.env.LEGION_BOOT_TOKEN = fixtureSecret("boot");
  process.env.LEGION_STATE_DIR = STATE_DIR;
  process.env.LEGION_CLAIM_FIRST_TURN_BOUND_MS = "0";
  const registered = readFileSync(
    path.resolve(import.meta.dir, "../../contracts/fixtures/daemon-api/register.json"),
    "utf8"
  );
  const daemon = createLegionDaemonClient("http://daemon.test", {
    pluginContract: 60,
    fetch: (async (input) =>
      new URL(input.toString()).pathname.endsWith("/claims/register")
        ? new Response(registered, { headers: { "content-type": "application/json" } })
        : new Response(null, { status: 204 })) as typeof fetch,
  });
  const claims: string[] = [];
  const reached = Promise.withResolvers<void>();
  legionRoleClaimBridge().instances.push({
    claim: async (_sessionID, role) => {
      claims.push(role);
      reached.resolve();
      throw new RoleClaimStoppingError(role);
    },
    subscribe: async () => undefined,
    sessionID: () => PANE_SESSION,
  });
  const exits: number[] = [];
  const claim = createClaimSession({
    daemon: () => daemon,
    persistedTranscript: async () => ({ sessionFile: SUBAGENT_TRANSCRIPT, agentId: PANE_SESSION }),
    exitProcess: (code) => {
      exits.push(code);
      return undefined as never;
    },
  });
  const context = {
    sessionManager: { getSessionId: () => PANE_SESSION },
  } as unknown as SessionContext;
  return { claim, context, claims, exits, reached: reached.promise };
}

// The pane's stop has begun when its first-turn bound comes (R24 of dispatch://INFRA-3589): the
// claim it would make exits nothing and writes nothing, so the stop's role release and DELETE
// run to their end. Either the session's `session_shutdown` told the claim session so, or, in
// RPC mode where a SIGTERM emits none, Envoy refused the claim naming the stop.
describe("a claim whose session's stop has begun", () => {
  it("is not made once the session's shutdown stopped the claim session, and exits nothing", async () => {
    const { claim, context, claims, exits } = pane();
    const lines = spyOn(console, "error").mockImplementation(() => undefined);
    try {
      await claim.bootstrap(context);
      // The bound is armed and has not run: the stop begins first.
      claim.stop();
      await settled();
      await settled();

      expect(claims).toEqual([]);
      expect(exits).toEqual([]);
      expect(lines).not.toHaveBeenCalled();
    } finally {
      lines.mockRestore();
    }
  });

  it("that Envoy refuses naming the stop, at the bound, says nothing and exits nothing", async () => {
    const { claim, context, claims, exits, reached } = pane();
    const lines = spyOn(console, "error").mockImplementation(() => undefined);
    try {
      await claim.bootstrap(context);
      await reached;
      await settled();

      expect(claims).toHaveLength(1);
      expect(exits).toEqual([]);
      expect(lines).not.toHaveBeenCalled();
    } finally {
      lines.mockRestore();
    }
  });
});
