import { expect, test } from "bun:test";
import { LISTENER_CALL_RETRY_DELAY_MS } from "@legion/contracts";
import { projectRefusal } from "@legion/envoy-client/test/subscribe-answer";
import { onEnvoyRoleRegained } from "@legion/pi-shared/role-claim-bridge";
import {
  createPi,
  response,
  responseWithRegistration,
  sessionContext,
  settle,
  until,
  useExtensionHarness,
  withEnvoyTimings,
} from "../test/extension-harness";
import type { SessionContext } from "../test/stand-in-host";

// A stopping session registers nothing and is told nothing (R24 of dispatch://INFRA-3589), also
// where a registration it sent before its stop is answered only after the stop began
// (`registerWithRelay`'s `stopping`), and it makes no role move once its stop began: a move that
// runs or waits its turn when the stop begins claims nothing after that and tells nothing, and
// `envoy_role_set` is refused. The rows drive the extension through the harness that
// `envoy.test.ts` uses. They live in a file of their own, since `envoy.test.ts` is close to the
// file size above which Biome checks nothing in it.

useExtensionHarness();

/** The role the rows below hold. */
const ROLE = "legion-controller";

/** The listener's grant of a claim of `ROLE` to `session`. */
function claimGranted(session: string): Response {
  return response({
    session_id: session,
    machine_id: "test",
    dir: "/tmp/envoy-omp-test",
    topics: [`notifications.role.${ROLE}`],
  });
}

/** The listener's answer to a read of `ROLE` that finds no live holder. */
function noHolder(): Response {
  return Response.json({ error: `no holder for role ${ROLE}` }, { status: 404 });
}

/** Whether a request to `path` with `init` is the stop's hand-back of `ROLE`: the removal of the
 *  role's topic from the session's interests. */
function handsBackRole(path: string, init: RequestInit | undefined): boolean {
  if (path !== "/v1/interests/unsubscribe") return false;
  const body = JSON.parse(init?.body?.toString() ?? "{}") as { readonly topics?: unknown };
  return Array.isArray(body.topics) && body.topics.includes(`notifications.role.${ROLE}`);
}

/** Whether a request to `path` with `init` is the stop's deregistration of `session`. */
function deregisters(session: string, path: string, init: RequestInit | undefined): boolean {
  return init?.method === "DELETE" && path === `/v1/sessions/${session}`;
}

/** Holds a request's answer on the wire until the row calls what this adds to `held`. */
async function heldOn(held: (() => void)[]): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  held.push(resolve);
  await promise;
}

/** A host for `sessionID` whose heartbeat ticks only when the row runs a tick of `intervals`,
 *  and whose pane notices go to `notifications`. */
function stoppingHost(sessionID: string) {
  const fixture = createPi();
  const intervals: (() => void)[] = [];
  const notifications: string[] = [];
  const base = sessionContext(sessionID);
  const context: SessionContext = {
    ...base,
    setInterval: (callback) => intervals.push(callback),
    ui: { ...base.ui, notify: (message) => notifications.push(message) },
  };
  return { fixture, intervals, notifications, context };
}

// A heartbeat's registration is on the wire when the session's stop begins, and only then does
// an edge listener answer it a project refusal: the rest is not registered again, and the
// session is told nothing of the refused topic, in its transcript or in its pane.
test("a project refusal answered after the stop began registers nothing again and tells nothing", async () => {
  const refusedRoot = "notifications.github.acme.priv";
  let holding = false;
  const held: (() => void)[] = [];
  const registrations: string[][] = [];
  globalThis.fetch = async (input, init) => {
    const url = new URL(input.toString());
    const body = init?.body === undefined ? undefined : JSON.parse(init.body.toString());
    if (url.pathname === "/v1/interests/subscribe") {
      const topics: string[] = Array.isArray(body?.topics) ? body.topics : [];
      registrations.push(topics);
      const refused = topics.filter((topic) => topic.startsWith(refusedRoot));
      if (holding && refused.length > 0) {
        // Answered only once the test lets it go, after the stop began.
        const { promise, resolve } = Promise.withResolvers<void>();
        held.push(resolve);
        await promise;
        return Response.json(projectRefusal(refused), { status: 403 });
      }
    }
    return responseWithRegistration(input, init, {});
  };
  // A fresh instance of the entry, as each row of `envoy.test.ts` loads: the query makes it a
  // module of its own, with state no other row shares, which a static import cannot give.
  const { default: envoyExtension } = await import("./envoy.ts?refused-while-stopping");
  const fixture = createPi();
  const intervals: (() => void)[] = [];
  const notifications: string[] = [];
  const base = sessionContext("ses_refused_while_stopping");
  const context: SessionContext = {
    ...base,
    setInterval: (callback) => intervals.push(callback),
    ui: { ...base.ui, notify: (message) => notifications.push(message) },
  };
  envoyExtension(fixture.pi);
  await fixture.handlers.get("session_start")?.({}, context);
  const subscribeTool = fixture.tools.find((tool) => tool.name === "envoy_subscribe");
  if (subscribeTool === undefined) throw new Error("envoy_subscribe was not registered");

  try {
    const subscribed = await subscribeTool.execute("", {
      topics: [`${refusedRoot}.>`, "notifications.dispatch.issue.PRIV-1.>"],
    });
    expect(subscribed.isError).toBeFalsy();
    holding = true;
    intervals[0]?.();
    await until(() => held.length === 1, "the heartbeat's registration on the wire", 10_000);
    const heldAt = registrations.length;

    const stopping = fixture.handlers.get("session_shutdown")?.({}, context);
    for (const answer of held.splice(0)) answer();
    await stopping;
    await settle();

    expect(registrations.slice(heldAt)).toEqual([]);
    expect(
      fixture.entries
        .map((entry) => ("data" in entry ? entry.data : undefined))
        .map((data) =>
          typeof data === "object" &&
          data !== null &&
          "content" in data &&
          typeof data.content === "string"
            ? data.content
            : ""
        )
        .filter((content) => content.includes(refusedRoot))
    ).toEqual([]);
    expect(notifications.filter((message) => message.includes(refusedRoot))).toEqual([]);
  } finally {
    for (const answer of held.splice(0)) answer();
    await fixture.handlers.get("session_shutdown")?.({}, context);
  }
});

// A session that holds a role: a heartbeat's registration is on the wire when the session's stop
// begins, and the listener accepts it only once the stop has handed the role back. The pass ends
// there. It reads and claims no role, so it takes back nothing the stop gave up, Legion's regain
// hook does not fire, and the pane hears nothing.
test("a heartbeat's registration accepted after the stop began makes no role move and tells nothing", async () => {
  const session = "ses_accepted_while_stopping";
  let holding = false;
  let stopBegan = false;
  let handedBack = false;
  const held: (() => void)[] = [];
  const afterStop: string[] = [];
  globalThis.fetch = async (input, init) => {
    const path = new URL(input.toString()).pathname;
    if (stopBegan) afterStop.push(`${init?.method ?? "GET"} ${path}`);
    // The listener names this session the role's holder until the stop hands the role back.
    if (path === `/v1/roles/${ROLE}`) {
      return handedBack ? noHolder() : response({ role: ROLE, holder: session, last_seen: 1 });
    }
    if (path === "/v1/roles/set") return claimGranted(session);
    if (handsBackRole(path, init)) handedBack = true;
    if (path === "/v1/interests/subscribe" && holding) {
      // Accepted only once the test lets it go, after the stop handed the role back.
      holding = false;
      const { promise, resolve } = Promise.withResolvers<void>();
      held.push(resolve);
      await promise;
    }
    return responseWithRegistration(input, init, {});
  };
  // A fresh instance of the entry, as the first row loads: no static import gives one.
  const { default: envoyExtension } = await import("./envoy.ts?accepted-while-stopping");
  const regained: string[] = [];
  onEnvoyRoleRegained(async (role, reason) => {
    regained.push(`${role}:${reason}`);
  });
  const { fixture, intervals, notifications, context } = stoppingHost(session);
  envoyExtension(fixture.pi);
  await fixture.handlers.get("session_start")?.({}, context);
  const roleTool = fixture.tools.find((tool) => tool.name === "envoy_role_set");
  if (roleTool === undefined) throw new Error("envoy_role_set was not registered");

  try {
    expect((await roleTool.execute("", { role: ROLE })).isError).toBeFalsy();
    holding = true;
    intervals[0]?.();
    await until(() => held.length === 1, "the heartbeat's registration on the wire", 10_000);
    const notified = notifications.length;

    stopBegan = true;
    const stopping = fixture.handlers.get("session_shutdown")?.({}, context);
    await until(() => handedBack, "the stop's hand-back of the role");
    for (const answer of held.splice(0)) answer();
    await stopping;
    await settle();

    expect(afterStop.filter((request) => request.includes("/v1/roles/"))).toEqual([]);
    expect(regained).toEqual([]);
    expect(notifications.slice(notified)).toEqual([]);
  } finally {
    for (const answer of held.splice(0)) answer();
    await fixture.handlers.get("session_shutdown")?.({}, context);
  }
});

// A session that holds a role: a heartbeat's pass finds that the listener lost the claim, and its
// claim of the role again is on the wire when the session's stop begins. The listener answers
// that claim a 500 only once the stop is done, and the client's stop gate refuses the retry the
// 500 asks for, so the pass fails. That failure is no outage to report: no heartbeat follows it,
// so the pane hears nothing and is promised no retry.
test("a heartbeat's role claim that fails after the stop began tells the pane nothing", async () => {
  const session = "ses_claim_failed_while_stopping";
  let listenerHoldsClaim = true;
  let failClaims = false;
  let failedClaims = 0;
  const held: (() => void)[] = [];
  globalThis.fetch = async (input, init) => {
    const path = new URL(input.toString()).pathname;
    if (path === `/v1/roles/${ROLE}`) {
      return listenerHoldsClaim
        ? response({ role: ROLE, holder: session, last_seen: 1 })
        : noHolder();
    }
    if (path === "/v1/roles/set" && failClaims) {
      failedClaims += 1;
      if (failedClaims === 1) {
        // Answered only once the test lets it go, after the stop. The stop gate sends no retry.
        const { promise, resolve } = Promise.withResolvers<void>();
        held.push(resolve);
        await promise;
      }
      return Response.json({ error: "the listener failed" }, { status: 500 });
    }
    if (path === "/v1/roles/set") return claimGranted(session);
    return responseWithRegistration(input, init, {});
  };
  // A fresh instance of the entry, as the first row loads: no static import gives one.
  const { default: envoyExtension } = await import("./envoy.ts?claim-failed-while-stopping");
  const { fixture, intervals, notifications, context } = stoppingHost(session);
  envoyExtension(fixture.pi);
  await fixture.handlers.get("session_start")?.({}, context);
  const roleTool = fixture.tools.find((tool) => tool.name === "envoy_role_set");
  if (roleTool === undefined) throw new Error("envoy_role_set was not registered");

  try {
    expect((await roleTool.execute("", { role: ROLE })).isError).toBeFalsy();
    // The listener drops the claim, so the heartbeat's pass claims the role again.
    listenerHoldsClaim = false;
    failClaims = true;
    intervals[0]?.();
    await until(() => held.length === 1, "the heartbeat's role claim on the wire", 10_000);
    const notified = notifications.length;

    await fixture.handlers.get("session_shutdown")?.({}, context);
    for (const answer of held.splice(0)) answer();
    // The client waits its retry delay out before the stop gate refuses the retry, and nothing
    // marks that refusal, so the row waits the delay out on the real clock too.
    await Bun.sleep(LISTENER_CALL_RETRY_DELAY_MS * 2);
    await settle();

    expect(failedClaims).toBe(1);
    expect(notifications.slice(notified)).toEqual([]);
  } finally {
    for (const answer of held.splice(0)) answer();
    await fixture.handlers.get("session_shutdown")?.({}, context);
  }
});

// A heartbeat's registration is on the wire when the session's stop begins, and the listener
// answers it only after the pass's deadline. The pass is abandoned at that deadline, after the
// stop began. That is no stall that recovers, since the DELETE follows it, so the pane hears
// nothing. The wait is real: the queue abandons its task on a real timer, read at the task's
// start. The deadline is never shorter than the refresh interval, so the interval names 25 ms for
// a 1 s deadline to stand; every tick here is hand-driven, so none of them fires on it.
test("a heartbeat's pass abandoned at its deadline after the stop began tells the pane nothing", async () => {
  await withEnvoyTimings({ heartbeatMs: 25, passDeadlineMs: 1_000 }, async () => {
    const session = "ses_abandoned_while_stopping";
    let holding = false;
    const held: (() => void)[] = [];
    globalThis.fetch = async (input, init) => {
      if (new URL(input.toString()).pathname === "/v1/interests/subscribe" && holding) {
        holding = false;
        const { promise, resolve } = Promise.withResolvers<void>();
        held.push(resolve);
        await promise;
      }
      return responseWithRegistration(input, init, {});
    };
    // A fresh instance of the entry, as the first row loads: no static import gives one.
    const { default: envoyExtension } = await import("./envoy.ts?abandoned-while-stopping");
    const { fixture, intervals, notifications, context } = stoppingHost(session);
    envoyExtension(fixture.pi);
    await fixture.handlers.get("session_start")?.({}, context);

    try {
      holding = true;
      const ticked = performance.now();
      intervals[0]?.();
      await until(() => held.length === 1, "the heartbeat's registration on the wire");
      // The pass is still inside its deadline when the stop begins.
      expect(notifications.filter((message) => message.includes("has stalled"))).toEqual([]);
      const notified = notifications.length;

      await fixture.handlers.get("session_shutdown")?.({}, context);
      // Past the pass's deadline, with room for its timer to run late.
      await Bun.sleep(Math.max(0, ticked + 1_500 - performance.now()));
      await settle();

      expect(notifications.slice(notified)).toEqual([]);
    } finally {
      for (const answer of held.splice(0)) answer();
      await fixture.handlers.get("session_shutdown")?.({}, context);
    }
  });
});

// A session that holds a role: a heartbeat's pass finds that the listener lost the claim, and its
// claim of the role again is on the wire when the session's stop begins. The listener grants that
// claim while the stop is still running. The move ends there: Legion's regain hook does not fire,
// and the pane hears nothing.
test("a heartbeat's role claim granted after the stop began fires no regain hook and tells nothing", async () => {
  const session = "ses_reclaimed_while_stopping";
  let listenerHoldsClaim = true;
  let holdClaim = false;
  const heldClaims: (() => void)[] = [];
  const heldDeletes: (() => void)[] = [];
  globalThis.fetch = async (input, init) => {
    const path = new URL(input.toString()).pathname;
    if (path === `/v1/roles/${ROLE}`) {
      return listenerHoldsClaim
        ? response({ role: ROLE, holder: session, last_seen: 1 })
        : noHolder();
    }
    if (path === "/v1/roles/set") {
      if (holdClaim) {
        // Answered only once the test lets it go, after the stop began.
        holdClaim = false;
        await heldOn(heldClaims);
      }
      return claimGranted(session);
    }
    // The stop's last call, held so that the stop still runs when the claim is answered.
    if (deregisters(session, path, init)) await heldOn(heldDeletes);
    return responseWithRegistration(input, init, {});
  };
  // A fresh instance of the entry, as the first row loads: no static import gives one.
  const { default: envoyExtension } = await import("./envoy.ts?reclaimed-while-stopping");
  const regained: string[] = [];
  onEnvoyRoleRegained(async (role, reason) => {
    regained.push(`${role}:${reason}`);
  });
  const { fixture, intervals, notifications, context } = stoppingHost(session);
  envoyExtension(fixture.pi);
  await fixture.handlers.get("session_start")?.({}, context);
  const roleTool = fixture.tools.find((tool) => tool.name === "envoy_role_set");
  if (roleTool === undefined) throw new Error("envoy_role_set was not registered");

  try {
    expect((await roleTool.execute("", { role: ROLE })).isError).toBeFalsy();
    // The listener drops the claim, so the heartbeat's pass claims the role again.
    listenerHoldsClaim = false;
    holdClaim = true;
    intervals[0]?.();
    await until(() => heldClaims.length === 1, "the heartbeat's role claim on the wire", 10_000);
    const notified = notifications.length;

    const stopping = fixture.handlers.get("session_shutdown")?.({}, context);
    await until(() => heldDeletes.length === 1, "the stop's deregistration on the wire");
    for (const answer of heldClaims.splice(0)) answer();
    await settle();

    expect(regained).toEqual([]);
    expect(notifications.slice(notified)).toEqual([]);
    for (const answer of heldDeletes.splice(0)) answer();
    await stopping;
  } finally {
    for (const answer of [...heldClaims.splice(0), ...heldDeletes.splice(0)]) answer();
    await fixture.handlers.get("session_shutdown")?.({}, context);
  }
});

// A session that holds a role: the model claims it again, and that claim is on the wire. A
// heartbeat's pass registers, and its role move waits its turn behind the claim. The session's
// stop begins and hands the role back, and only then is the claim answered. The waiting move
// claims nothing after the stop began, so it takes back nothing the stop gave up, Legion's regain
// hook does not fire, and the pane hears nothing. The tool says the session is stopping.
test("a role move that waits its turn when the stop begins claims nothing and tells nothing", async () => {
  const session = "ses_queued_while_stopping";
  let stopBegan = false;
  let handedBack = false;
  let holdClaim = false;
  let registrations = 0;
  const heldClaims: (() => void)[] = [];
  const heldDeletes: (() => void)[] = [];
  const claimsAfterStop: string[] = [];
  globalThis.fetch = async (input, init) => {
    const path = new URL(input.toString()).pathname;
    // The listener names this session the role's holder until the stop hands the role back.
    if (path === `/v1/roles/${ROLE}`) {
      return handedBack ? noHolder() : response({ role: ROLE, holder: session, last_seen: 1 });
    }
    if (path === "/v1/roles/set") {
      if (stopBegan) claimsAfterStop.push(String(init?.body));
      if (holdClaim) {
        // Answered only once the test lets it go, after the stop handed the role back.
        holdClaim = false;
        await heldOn(heldClaims);
      }
      return claimGranted(session);
    }
    if (handsBackRole(path, init)) handedBack = true;
    // The stop's last call, held so that the stop still runs when the claim is answered.
    if (deregisters(session, path, init)) await heldOn(heldDeletes);
    if (path === "/v1/interests/subscribe") registrations += 1;
    return responseWithRegistration(input, init, {});
  };
  // A fresh instance of the entry, as the first row loads: no static import gives one.
  const { default: envoyExtension } = await import("./envoy.ts?queued-while-stopping");
  const regained: string[] = [];
  onEnvoyRoleRegained(async (role, reason) => {
    regained.push(`${role}:${reason}`);
  });
  const { fixture, intervals, notifications, context } = stoppingHost(session);
  envoyExtension(fixture.pi);
  await fixture.handlers.get("session_start")?.({}, context);
  const roleTool = fixture.tools.find((tool) => tool.name === "envoy_role_set");
  if (roleTool === undefined) throw new Error("envoy_role_set was not registered");

  try {
    expect((await roleTool.execute("", { role: ROLE })).isError).toBeFalsy();
    holdClaim = true;
    const claiming = roleTool.execute("", { role: ROLE });
    await until(() => heldClaims.length === 1, "the tool's claim on the wire");
    const registered = registrations;
    intervals[0]?.();
    await until(() => registrations > registered, "the heartbeat's registration", 10_000);
    // The pass's role move is queued a few turns after its registration is answered.
    await settle();
    const notified = notifications.length;

    stopBegan = true;
    const stopping = fixture.handlers.get("session_shutdown")?.({}, context);
    await until(() => handedBack, "the stop's hand-back of the role");
    await until(() => heldDeletes.length === 1, "the stop's deregistration on the wire");
    for (const answer of heldClaims.splice(0)) answer();
    const claimed = await claiming;
    await settle();

    expect(claimsAfterStop).toEqual([]);
    expect(regained).toEqual([]);
    expect(notifications.slice(notified)).toEqual([]);
    expect(claimed).toMatchObject({
      isError: true,
      content: [
        {
          type: "text",
          text: `this session is stopping, so it claims no role: role ${ROLE} is not held here`,
        },
      ],
    });
    for (const answer of heldDeletes.splice(0)) answer();
    await stopping;
  } finally {
    for (const answer of [...heldClaims.splice(0), ...heldDeletes.splice(0)]) answer();
    await fixture.handlers.get("session_shutdown")?.({}, context);
  }
});

// The model calls `envoy_role_set` while the session's stop runs. The tool refuses, naming the
// stop, and sends the listener no role call at all.
test("envoy_role_set called once the stop has begun is refused naming the stop and sends nothing", async () => {
  const session = "ses_role_set_while_stopping";
  let stopBegan = false;
  const heldDeletes: (() => void)[] = [];
  const roleCallsAfterStop: string[] = [];
  globalThis.fetch = async (input, init) => {
    const path = new URL(input.toString()).pathname;
    if (stopBegan && path.startsWith("/v1/roles/")) roleCallsAfterStop.push(path);
    if (path === "/v1/roles/set") return claimGranted(session);
    // The stop's last call, held so that the stop still runs when the tool is called.
    if (deregisters(session, path, init)) await heldOn(heldDeletes);
    return responseWithRegistration(input, init, {});
  };
  // A fresh instance of the entry, as the first row loads: no static import gives one.
  const { default: envoyExtension } = await import("./envoy.ts?role-set-while-stopping");
  const { fixture, notifications, context } = stoppingHost(session);
  envoyExtension(fixture.pi);
  await fixture.handlers.get("session_start")?.({}, context);
  const roleTool = fixture.tools.find((tool) => tool.name === "envoy_role_set");
  if (roleTool === undefined) throw new Error("envoy_role_set was not registered");

  try {
    const notified = notifications.length;
    stopBegan = true;
    const stopping = fixture.handlers.get("session_shutdown")?.({}, context);
    await until(() => heldDeletes.length === 1, "the stop's deregistration on the wire");

    const refused = await roleTool.execute("", { role: ROLE });
    await settle();

    expect(roleCallsAfterStop).toEqual([]);
    expect(refused).toMatchObject({
      isError: true,
      content: [
        {
          type: "text",
          text: `this session is stopping, so it claims no role: role ${ROLE} is not held here`,
        },
      ],
    });
    expect(notifications.slice(notified)).toEqual([]);
    for (const answer of heldDeletes.splice(0)) answer();
    await stopping;
  } finally {
    for (const answer of heldDeletes.splice(0)) answer();
    await fixture.handlers.get("session_shutdown")?.({}, context);
  }
});
