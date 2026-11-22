import { expect, test } from "bun:test";
import { REPLAY_STATE_ENTRY } from "../src/missed-messages";
import {
  createPi,
  responseWithRegistration,
  sessionContext,
  settle,
  until,
  useExtensionHarness,
} from "../test/extension-harness";
import type { SessionContext } from "../test/stand-in-host";

// A session whose stop has begun registers, unsubscribes and claims nothing, writes no ledger
// entry and tells its pane nothing (R24 of dispatch://INFRA-3589), whichever path is still
// running when the stop begins: the stop gates of `../src/stop-gates` and the client's, and the
// guards of the paths no gate covers. The rows drive the extension through the harness that
// `envoy.test.ts` uses, in a file of their own, since `envoy.test.ts` is close to the file size
// above which Biome checks nothing in it.

useExtensionHarness();

/** A branch entry no reader of the extension acts on: a session with one has history, so its
 *  pass reads the listener's rows for its role (`desiredRole`'s `readRows`). */
const HISTORY = { type: "custom", customType: "an-unrelated-plugin", data: {} };

/** The listener's refusal of a registration, which leaves the pass to throw once its role move
 *  is done. */
function refusedRegistration(): Response {
  return Response.json({ error: "the listener refused this registration" }, { status: 400 });
}

/** A host for `sessionID` with `branch`, whose intervals go to `intervals` and whose pane
 *  notices go to `notifications`. */
function host(
  sessionID: string,
  branch: readonly unknown[],
  intervals: (() => void)[],
  notifications: string[]
): SessionContext {
  const base = sessionContext(sessionID);
  return {
    ...base,
    sessionManager: { ...base.sessionManager, getBranch: () => branch },
    setInterval: (callback) => intervals.push(callback),
    ui: { ...base.ui, notify: (message) => notifications.push(message) },
  };
}

/** Holds a request's answer on the wire until the row calls what this adds to `held`. */
async function heldOn(held: (() => void)[]): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  held.push(resolve);
  await promise;
}

// A session with history starts, and the listener refuses its first registration. The pass goes
// on to its role move, which reads the listener's rows for the session's role. The session's stop
// begins while that read is on the wire, and the read is answered only once the stop is done. The
// pass then throws the refusal: that is no outage to report, since the stop deleted the record,
// so the pane is told nothing and no retry interval is armed.
test("a session_start registration that fails after the stop began tells the pane nothing and arms no retry", async () => {
  const session = "ses_start_failed_while_stopping";
  const heldReads: (() => void)[] = [];
  let reads = 0;
  globalThis.fetch = async (input, init) => {
    const path = new URL(input.toString()).pathname;
    if (path === "/v1/interests/subscribe") return refusedRegistration();
    if (path === `/v1/interests/${session}`) {
      reads += 1;
      // The second read is the role move's (the first is the inbound's recovery of the
      // session's interests).
      if (reads === 2) await heldOn(heldReads);
      return Response.json({ error: "no interest" }, { status: 404 });
    }
    return responseWithRegistration(input, init, {});
  };
  // A fresh instance of the entry, as each row of `envoy.test.ts` loads: the query makes it a
  // module of its own, with state no other row shares, which a static import cannot give.
  const { default: envoyExtension } = await import("./envoy.ts?start-failed-while-stopping");
  const fixture = createPi();
  const intervals: (() => void)[] = [];
  const notifications: string[] = [];
  const context = host(session, [HISTORY], intervals, notifications);
  envoyExtension(fixture.pi);

  const starting = fixture.handlers.get("session_start")?.({}, context);
  try {
    await until(() => heldReads.length === 1, "the role move's read on the wire", 10_000);
    const notified = notifications.length;
    const armed = intervals.length;

    await fixture.handlers.get("session_shutdown")?.({}, context);
    for (const answer of heldReads.splice(0)) answer();
    await starting;
    await settle();

    expect(notifications.slice(notified)).toEqual([]);
    expect(intervals.length).toBe(armed);
  } finally {
    for (const answer of heldReads.splice(0)) answer();
    await starting;
    await fixture.handlers.get("session_shutdown")?.({}, context);
  }
});

// A session follows a topic, and its stop begins: the stop's DELETE is still on the wire when the
// model calls `envoy_unsubscribe`. The tool is refused naming the stop. It sends the listener no
// removal and writes no ledger entry.
test("envoy_unsubscribe after the stop began sends nothing, writes no ledger entry, and is refused naming the stop", async () => {
  const session = "ses_unsubscribe_while_stopping";
  const topic = "notifications.dispatch.issue.DSP-9.>";
  let stopBegan = false;
  const heldDeletes: (() => void)[] = [];
  const afterStop: string[] = [];
  globalThis.fetch = async (input, init) => {
    const path = new URL(input.toString()).pathname;
    if (stopBegan) afterStop.push(`${init?.method ?? "GET"} ${path}`);
    // The stop's last call, held so that the stop still runs when the tool is called.
    if (init?.method === "DELETE" && path === `/v1/sessions/${session}`) {
      await heldOn(heldDeletes);
    }
    return responseWithRegistration(input, init, {});
  };
  // A fresh instance of the entry, as the first row loads: no static import gives one.
  const { default: envoyExtension } = await import("./envoy.ts?unsubscribe-while-stopping");
  const fixture = createPi();
  const notifications: string[] = [];
  const context = host(session, [], [], notifications);
  envoyExtension(fixture.pi);
  await fixture.handlers.get("session_start")?.({}, context);
  const tool = (name: string) => {
    const found = fixture.tools.find((candidate) => candidate.name === name);
    if (found === undefined) throw new Error(`${name} was not registered`);
    return found;
  };
  const ledgerEntries = () =>
    fixture.entries.filter(
      (entry) => "customType" in entry && entry.customType === REPLAY_STATE_ENTRY
    ).length;

  try {
    expect((await tool("envoy_subscribe").execute("", { topics: [topic] })).isError).toBeFalsy();
    stopBegan = true;
    const stopping = fixture.handlers.get("session_shutdown")?.({}, context);
    await until(() => heldDeletes.length === 1, "the stop's deregistration on the wire");
    const ledgerWritten = ledgerEntries();
    const notified = notifications.length;

    const refused = await tool("envoy_unsubscribe").execute("", { topics: [topic] });
    await settle();

    expect(refused).toMatchObject({
      isError: true,
      content: [{ type: "text", text: "this session is stopping, so it unsubscribes nothing" }],
    });
    expect(afterStop).toEqual([`DELETE /v1/sessions/${session}`]);
    expect(ledgerEntries()).toBe(ledgerWritten);
    expect(notifications.slice(notified)).toEqual([]);
    for (const answer of heldDeletes.splice(0)) answer();
    await stopping;
  } finally {
    for (const answer of heldDeletes.splice(0)) answer();
    await fixture.handlers.get("session_shutdown")?.({}, context);
  }
});

// A session switches to a session with history, and the listener refuses that session's first
// registration. The switch's pass goes on to its role move, and the session's stop begins while
// the move's read of the listener's rows is on the wire. The pass then throws the refusal into
// the switch, which reports a failure to the pane at any other time: the tell gate drops it.
test("a notice a switch raises after the stop began is dropped by the tell gate", async () => {
  const first = "ses_before_switch";
  const switched = "ses_switched_while_stopping";
  const heldReads: (() => void)[] = [];
  let reads = 0;
  globalThis.fetch = async (input, init) => {
    const path = new URL(input.toString()).pathname;
    const body = init?.body === undefined ? undefined : JSON.parse(init.body.toString());
    if (path === "/v1/interests/subscribe" && body?.session_id === switched) {
      return refusedRegistration();
    }
    if (path === `/v1/interests/${switched}`) {
      reads += 1;
      // The second read is the role move's (the first is the inbound's recovery of the
      // session's interests).
      if (reads === 2) await heldOn(heldReads);
      return Response.json({ error: "no interest" }, { status: 404 });
    }
    return responseWithRegistration(input, init, {});
  };
  // A fresh instance of the entry, as the first row loads: no static import gives one.
  const { default: envoyExtension } = await import("./envoy.ts?switched-while-stopping");
  const fixture = createPi();
  const notifications: string[] = [];
  const intervals: (() => void)[] = [];
  const before = host(first, [], intervals, notifications);
  const after = host(switched, [HISTORY], intervals, notifications);
  envoyExtension(fixture.pi);
  await fixture.handlers.get("session_start")?.({}, before);

  const switching = fixture.handlers.get("session_switch")?.({ reason: "new" }, after);
  try {
    await until(() => heldReads.length === 1, "the switch's role move read on the wire", 10_000);
    const notified = notifications.length;

    await fixture.handlers.get("session_shutdown")?.({}, after);
    for (const answer of heldReads.splice(0)) answer();
    await switching;
    await settle();

    expect(notifications.slice(notified)).toEqual([]);
  } finally {
    for (const answer of heldReads.splice(0)) answer();
    await switching;
    await fixture.handlers.get("session_shutdown")?.({}, after);
  }
});

// A session whose subagent check could not publish its transcript at the start asks again at the
// next session event. Once the stop has begun, a session event does nothing: the check does not
// run, so it publishes no transcript and logs no failed publish outside the tell gate.
test("a session event after the stop began runs no subagent check", async () => {
  const session = "ses_event_after_stop";
  globalThis.fetch = async (input, init) => responseWithRegistration(input, init, {});
  // A fresh instance of the entry, as the first row loads: no static import gives one.
  const { default: envoyExtension } = await import("./envoy.ts?event-after-stop");
  const fixture = createPi();
  let publishes = 0;
  const base = host(session, [], [], []);
  // The transcript's publish fails, so the check's answer is not kept: each event asks again.
  const context: SessionContext = {
    ...base,
    sessionManager: {
      ...base.sessionManager,
      ensureOnDisk: async () => {
        publishes += 1;
        throw new Error("another writer holds the transcript's publish lock");
      },
    },
  };
  envoyExtension(fixture.pi);
  await fixture.handlers.get("session_start")?.({}, context);
  await fixture.handlers.get("session_shutdown")?.({}, context);
  const before = publishes;

  await fixture.handlers.get("session_switch")?.({ reason: "new" }, context);
  await fixture.handlers.get("session_branch")?.({}, context);
  await fixture.handlers.get("session_tree")?.({}, context);
  await settle();

  expect(publishes).toBe(before);
});
