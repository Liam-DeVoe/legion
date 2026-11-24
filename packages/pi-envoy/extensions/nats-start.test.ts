// The rows on a session's NATS connection at its start and after it: the first dial and its
// retries, the relay subject and the live view's control subject the session opens on it, the
// registration that names why a relay is missing, and the broker trade a dial presents. They have
// a file of their own so that `envoy.test.ts` stays below the size Biome reads a file up to (1 MiB
// by default). They keep the `describe` they had in that file, so their full names do not change.
// Both files share one harness (`../test/extension-harness`).
import { describe, expect, setSystemTime, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { deliverSubject } from "@legion/envoy-client/consumer-delivery";
import { natsFailureReason } from "@legion/envoy-client/errors";
import { INBOUND_FIRST_OPEN_BOUND_MS } from "@legion/envoy-client/inbound-outage";
import { NATS_DIAL_PLAN, NATS_DIAL_TOKEN_DROPPED } from "@legion/envoy-client/nats-connection";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { logger } from "@oh-my-pi/pi-utils";
import {
  type ConnectionOptions,
  ErrorCode,
  NatsError,
  nkeys,
  type connect as realConnect,
} from "nats";
import { version as packageVersion } from "../package.json" with { type: "json" };
import {
  configDirectory,
  createPi,
  forwardedRoleEnvelope,
  natsStandInConnect,
  natsState,
  responseWithRegistration,
  rowFetch,
  sessionContext,
  setNatsConnectForTests,
  settle,
  topLevelSession,
  until,
  useExtensionHarness,
  withEnvoyTimings,
} from "../test/extension-harness";
import type { SessionContext } from "../test/stand-in-host";

useExtensionHarness();

/** A pod's projected service-account token, which this machine trades at a stub broker. */
const MACHINE_IDENTITY_TOKEN = fixtureSecret("machine");

describe("envoy OMP extension", () => {
  test("retries NATS in the background when the initial connection fails", async () => {
    globalThis.fetch = async (input, init) => responseWithRegistration(input, init, {});
    natsState.failConnects = 1;
    const notifications: string[] = [];
    const intervals: { callback: () => void; intervalMs: number }[] = [];
    const context: SessionContext = {
      cwd: "/tmp/envoy-omp-test",
      sessionManager: { ...topLevelSession, getSessionId: () => "ses_retry" },
      setInterval: (callback, intervalMs) => intervals.push({ callback, intervalMs }),
      ui: { ...sessionContext().ui, notify: (message) => notifications.push(message) },
    };
    const { default: envoyExtension } = await import("./envoy.ts?retry");
    const fixture = createPi();

    envoyExtension(fixture.pi);
    await fixture.handlers.get("session_start")?.({}, context);

    expect(notifications.some((message) => message.includes("retrying in the background"))).toBe(
      true
    );
    // With no connection the session holds no relay subject, so the listener can give it no
    // consumer and its registration is refused until the retry connects. The relay may still
    // open: the start race is held for its bound (`INBOUND_FIRST_OPEN_BOUND_MS`), so the session
    // is not told that it receives nothing.
    expect(
      notifications.some((message) => message.includes("inbound messages are unavailable"))
    ).toBe(false);
    // The notice that the session is not registered names the dial's own failure, the cause a
    // person can act on (a hub with no auth service under NATS_AUTH=broker says so and what to
    // unset), not only the refusal it led to.
    expect(
      notifications.some((message) =>
        message.includes("because its relay subject could not be opened: CONNECTION_REFUSED")
      )
    ).toBe(true);
    expect(natsState.subscriptions.has("notifications.envoy.deliver.ses_retry")).toBe(false);
    const retry = intervals.at(-1);
    if (retry === undefined) throw new Error("retry interval was not registered");

    retry.callback();
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(natsState.subscriptions.has("notifications.envoy.deliver.ses_retry")).toBe(true);
    expect(notifications.some((message) => message.includes("this session is registered"))).toBe(
      true
    );
    // The relay opened inside the bound, so the session was told nothing, and it is told nothing
    // about receiving again either.
    expect(notifications.filter((message) => message.includes("inbound messages are"))).toEqual([]);
    const connectionsAfterRecovery = natsState.connectedNames.length;

    retry.callback();
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(natsState.connectedNames.length).toBe(connectionsAfterRecovery);
  });

  test("a NATS failure notice never names an address the log would not", async () => {
    // A throwaway nkey seed pasted as the NATS server: nats.js's resolver error names it as the
    // host it could not look up, lowercased, and the notice must carry neither form.
    const seed = new TextDecoder().decode(nkeys.createUser().getSeed());
    process.env.ENVOY_NATS_URL = seed;
    globalThis.fetch = async (input, init) => responseWithRegistration(input, init, {});
    natsState.failConnects = 1;
    natsState.failError = () =>
      Object.assign(new Error(`getaddrinfo EAI_AGAIN ${seed.toLowerCase()}`), {
        name: "DNSException",
        code: "EAI_AGAIN",
        syscall: "getaddrinfo",
      });
    const notifications: string[] = [];
    const context: SessionContext = {
      cwd: "/tmp/envoy-omp-test",
      sessionManager: { ...topLevelSession, getSessionId: () => "ses_seed_notice" },
      setInterval: () => {},
      ui: { ...sessionContext().ui, notify: (message) => notifications.push(message) },
    };
    const { default: envoyExtension } = await import("./envoy.ts?seed-notice");
    const fixture = createPi();
    envoyExtension(fixture.pi);
    await fixture.handlers.get("session_start")?.({}, context);
    expect(
      notifications.some((message) =>
        message.includes(
          "because its relay subject could not be opened: getaddrinfo EAI_AGAIN); retrying in the background"
        )
      )
    ).toBe(true);
    expect(notifications.join("\n").toLowerCase()).not.toContain(seed.toLowerCase());
  });

  // The hub closes a dial whose CONNECT came after its 2 s authentication timeout, which a busy
  // start of Oh My Pi causes, and answers the next one: the pass dials again rather than register
  // with no relay.
  test("a first NATS dial that fails once is dialed again, and the session_start pass registers with its relay", async () => {
    globalThis.fetch = async (input, init) => responseWithRegistration(input, init, {});
    natsState.failConnects = 1;
    natsState.failError = () =>
      new NatsError("'Authentication Timeout'", ErrorCode.AuthenticationTimeout);
    setNatsConnectForTests(natsStandInConnect, { firstDelayMs: 5, maxDelayMs: 5, budgetMs: 5_000 });
    try {
      const notifications: string[] = [];
      const context: SessionContext = {
        cwd: "/tmp/envoy-omp-test",
        sessionManager: { ...topLevelSession, getSessionId: () => "ses_redial" },
        setInterval: () => {},
        ui: { ...sessionContext().ui, notify: (message) => notifications.push(message) },
      };
      const { default: envoyExtension } = await import("./envoy.ts?redial-first-dial");
      const fixture = createPi();
      envoyExtension(fixture.pi);
      await fixture.handlers.get("session_start")?.({}, context);

      expect(natsState.failConnects).toBe(0);
      expect(natsState.subscriptions.has("notifications.envoy.deliver.ses_redial")).toBe(true);
      expect(
        notifications.filter((message) => message.includes("inbound messages are unavailable"))
      ).toEqual([]);
      expect(notifications.filter((message) => message.includes("not registered"))).toEqual([]);
    } finally {
      setNatsConnectForTests(natsStandInConnect);
    }
  });

  // A Legion wake after a pod release is a new pod that resumes the session's transcript: its
  // start blocks the event loop as a fresh one does, and its pass also reads the interests the
  // session registered before. The dial the hub closes is dialed again there too.
  test("a session resumed in a new pod, whose first dial the hub closes, is dialed again and registers with its relay in its session_start pass", async () => {
    const registrations: { readonly consumer: boolean }[] = [];
    globalThis.fetch = async (input, init) => {
      const url = new URL(input.toString());
      if (url.pathname === "/v1/interests/subscribe") {
        const body = JSON.parse(init?.body?.toString() ?? "{}") as {
          readonly delivery?: { readonly consumer?: boolean };
        };
        registrations.push({ consumer: body.delivery?.consumer === true });
      }
      return responseWithRegistration(input, init, {});
    };
    natsState.failConnects = 1;
    natsState.failError = () =>
      new NatsError("'Authentication Timeout'", ErrorCode.AuthenticationTimeout);
    setNatsConnectForTests(natsStandInConnect, { firstDelayMs: 5, maxDelayMs: 5, budgetMs: 5_000 });
    try {
      const notifications: string[] = [];
      const earlier = [
        { type: "message", message: { role: "user", content: "the task", timestamp: 1 } },
        { type: "message", message: { role: "assistant", content: "working on it", timestamp: 2 } },
      ];
      const context: SessionContext = {
        cwd: "/tmp/envoy-omp-test",
        sessionManager: {
          ...topLevelSession,
          getSessionId: () => "ses_woken",
          getBranch: () => earlier,
          getEntries: () => earlier,
        },
        setInterval: () => {},
        ui: { ...sessionContext().ui, notify: (message) => notifications.push(message) },
      };
      const { default: envoyExtension } = await import("./envoy.ts?redial-resumed-start");
      const fixture = createPi();
      envoyExtension(fixture.pi);
      await fixture.handlers.get("session_start")?.({}, context);

      expect(natsState.failConnects).toBe(0);
      expect(natsState.subscriptions.has("notifications.envoy.deliver.ses_woken")).toBe(true);
      expect(registrations.length).toBeGreaterThan(0);
      expect(registrations.every((one) => one.consumer)).toBe(true);
      expect(
        notifications.filter((message) => message.includes("inbound messages are unavailable"))
      ).toEqual([]);
      expect(notifications.filter((message) => message.includes("not registered"))).toEqual([]);
    } finally {
      setNatsConnectForTests(natsStandInConnect);
    }
  });

  // A pod whose first dial fails, and whose relay is still closed once the start race's bound
  // (`INBOUND_FIRST_OPEN_BOUND_MS`) has passed, is told that it receives nothing. A later pass
  // registers. Without a word that it receives again, the session reads the outage notice as its
  // state for its life.
  test("a session told it receives nothing is told once it receives again, in every part of the notice", async () => {
    globalThis.fetch = async (input, init) => responseWithRegistration(input, init, {});
    natsState.failConnects = 99;
    const infos: string[] = [];
    const stopSink = logger.registerLogSink((entry) => {
      if (entry.level === "info") infos.push(entry.message);
    });
    const start = Date.now();
    try {
      const notifications: string[] = [];
      const intervals: (() => void)[] = [];
      const context: SessionContext = {
        cwd: "/tmp/envoy-omp-test",
        sessionManager: { ...topLevelSession, getSessionId: () => "ses_restored" },
        setInterval: (callback) => intervals.push(callback),
        ui: { ...sessionContext().ui, notify: (message) => notifications.push(message) },
      };
      const { default: envoyExtension } = await import("./envoy.ts?inbound-restored");
      const fixture = createPi();
      envoyExtension(fixture.pi);
      await fixture.handlers.get("session_start")?.({}, context);
      const transcript = () =>
        fixture.entries
          .map((entry) => ("data" in entry ? entry.data : undefined))
          .map((data) =>
            typeof data === "object" &&
            data !== null &&
            "content" in data &&
            typeof data.content === "string"
              ? data.content
              : ""
          );
      expect(transcript().filter((content) => content.includes("Envoy inbound restored"))).toEqual(
        []
      );

      // Past the bound, the background retry's pass finds NATS still closed, and the session is
      // told that it receives nothing.
      setSystemTime(new Date(start + INBOUND_FIRST_OPEN_BOUND_MS + 1_000));
      await until(() => {
        intervals.at(-1)?.();
        return notifications.some((m) => m.includes("inbound messages are unavailable"));
      }, "the outage notice");

      // A later retry's pass dials, opens the relay and registers.
      natsState.failConnects = 0;
      await until(() => {
        intervals.at(-1)?.();
        return notifications.some((m) => m.includes("inbound messages are back"));
      }, "the restored notice");
      expect(infos.filter((m) => m.includes("this session receives again"))).toHaveLength(1);
      expect(
        transcript().filter((content) => content.includes("Envoy inbound restored"))
      ).toHaveLength(1);
    } finally {
      setSystemTime();
      stopSink();
    }
  });

  test("a relay lost after the start names its failure in the refused registration, and the next heartbeat opens it again and says so once", async () => {
    // The relay subject opened at the start. Later its connection closes for good, as nats.js
    // closes one on an authorization error it does not ignore, and the hub refuses every dial
    // after that. The relay's own resubscribe fails once and then waits NATS_RETRY_INTERVAL_MS,
    // and the agent stream's gives up, so here the heartbeat's pass is what opens them again.
    process.env.ENVOY_RESUBSCRIBE_DELAY_MS = "10";
    globalThis.fetch = async (input, init) => responseWithRegistration(input, init, []);
    const id = "ses_relay_lost";
    const relay = deliverSubject(id);
    const control = `agentstream.${id}.control`;
    const reason = "because its relay subject could not be opened: CONNECTION_REFUSED";
    const warnings: string[] = [];
    const infos: string[] = [];
    const stopSink = logger.registerLogSink((entry) => {
      if (entry.level === "warn") warnings.push(entry.message);
      if (entry.level === "info") infos.push(entry.message);
    });
    await withEnvoyTimings({ heartbeatMs: 45_000 }, async () => {
      const notifications: string[] = [];
      const intervals: { readonly callback: () => void; readonly intervalMs: number }[] = [];
      const context: SessionContext = {
        ...sessionContext(id),
        setInterval: (callback, intervalMs) => intervals.push({ callback, intervalMs }),
        ui: { ...sessionContext().ui, notify: (message) => notifications.push(message) },
      };
      const { default: envoyExtension } = await import("./envoy.ts?relay-lost-mid-session");
      const fixture = createPi();
      envoyExtension(fixture.pi);
      const heartbeat = () => {
        for (const { callback, intervalMs } of intervals) if (intervalMs === 45_000) callback();
      };
      const transcript = () =>
        fixture.entries
          .map((entry) => ("data" in entry ? entry.data : undefined))
          .map((data) =>
            typeof data === "object" &&
            data !== null &&
            "content" in data &&
            typeof data.content === "string"
              ? data.content
              : ""
          );
      try {
        await fixture.handlers.get("session_start")?.({}, context);
        expect(natsState.controls.get(relay)?.active()).toBe(true);
        expect(natsState.controls.get(control)?.active()).toBe(true);
        expect(natsState.connectedNames).toHaveLength(1);

        // The connection closes for good: nats.js ends every subscription on it, and the hub
        // refuses each dial from now on.
        natsState.failConnects = 99;
        await natsState.connections[0]?.close();
        natsState.controls.get(relay)?.end();
        natsState.controls.get(control)?.end();
        await until(() => natsState.failConnects < 99, "the relay's own redial");
        await settle();

        // A registration sent outside a pass while the subject is missing - here the one a new
        // follow sends - is refused naming why, and the session is told so.
        const told = () => ({
          logged: warnings.filter((m) => m.includes("this session receives nothing")),
          shown: notifications.filter((m) => m.includes("inbound messages are unavailable")),
          written: transcript().filter((content) => content.includes("Envoy inbound unavailable")),
        });
        const subscribe = fixture.tools.find((tool) => tool.name === "envoy_subscribe");
        if (subscribe === undefined) throw new Error("envoy_subscribe was not registered");
        const followed = await subscribe.execute("", { topics: ["team.relay-lost"] });
        expect(String(followed.details.registrationError)).toContain(reason);
        for (const part of Object.values(told())) {
          expect(part).toHaveLength(1);
          expect(part[0]).toContain(reason);
        }

        // The heartbeat's pass dials to open the relay again, is refused, and sends a
        // registration with no relay subject; that refusal tells the session nothing more.
        const beforeHeartbeat = natsState.failConnects;
        heartbeat();
        await until(
          () =>
            natsState.failConnects < beforeHeartbeat &&
            notifications.some((m) => m.includes("registry heartbeat failed")),
          "the heartbeat's dial and its failure"
        );
        await settle();
        for (const part of Object.values(told())) expect(part).toHaveLength(1);
        expect(
          transcript().filter((content) => content.includes("Envoy inbound restored"))
        ).toEqual([]);

        // The hub admits again. The next heartbeat dials a new connection, opens the relay and
        // the control subject on it, registers, and tells the session once that it receives
        // again.
        natsState.failConnects = 0;
        heartbeat();
        await until(
          () => notifications.some((m) => m.includes("inbound messages are back")),
          "the restored notice"
        );
        await settle();
        expect(natsState.connectedNames).toHaveLength(2);
        expect(natsState.controls.get(relay)?.active()).toBe(true);
        expect(natsState.controls.get(control)?.active()).toBe(true);
        expect(infos.filter((m) => m.includes("this session receives again"))).toHaveLength(1);
        expect(
          transcript().filter((content) => content.includes("Envoy inbound restored"))
        ).toHaveLength(1);

        natsState.controls
          .get(`notifications.agent.${id}`)
          ?.push(forwardedRoleEnvelope("legion-controller", "after the relay came back", "back"));
        await until(
          () => fixture.messages.some((m) => m.includes("after the relay came back")),
          "the message relayed on the new connection"
        );
      } finally {
        stopSink();
        await fixture.handlers.get("session_shutdown")?.({}, context);
      }
    });
  });

  test("a live view control subject whose own resubscribe failed is opened again by the next heartbeat", async () => {
    // The control subscription ends under a connection that stays up, and its one resubscribe,
    // one resubscribe delay later, is refused; nothing else would open it again.
    process.env.ENVOY_RESUBSCRIBE_DELAY_MS = "10";
    globalThis.fetch = async (input, init) => responseWithRegistration(input, init, []);
    const id = "ses_control_lost";
    const control = `agentstream.${id}.control`;
    await withEnvoyTimings({ heartbeatMs: 45_000 }, async () => {
      const intervals: { readonly callback: () => void; readonly intervalMs: number }[] = [];
      const context: SessionContext = {
        ...sessionContext(id),
        setInterval: (callback, intervalMs) => intervals.push({ callback, intervalMs }),
      };
      const { default: envoyExtension } = await import("./envoy.ts?control-lost-mid-session");
      const fixture = createPi();
      envoyExtension(fixture.pi);
      try {
        await fixture.handlers.get("session_start")?.({}, context);
        const first = natsState.controls.get(control);
        expect(first?.active()).toBe(true);

        natsState.failSubscribes = 1;
        first?.end();
        await until(() => natsState.failSubscribes === 0, "the control subject's own resubscribe");
        await settle();
        expect(natsState.controls.get(control)).toBe(first);

        for (const { callback, intervalMs } of intervals) if (intervalMs === 45_000) callback();
        await until(
          () => natsState.controls.get(control) !== first,
          "the heartbeat's open of the control subject"
        );
        expect(natsState.controls.get(control)?.active()).toBe(true);
        expect(natsState.connectedNames).toHaveLength(1);
      } finally {
        await fixture.handlers.get("session_shutdown")?.({}, context);
      }
    });
  });

  /** A session for the relay-loss rows: its notices in all three places, and its heartbeat. */
  async function relayLossSession(id: string, query: string) {
    const warnings: string[] = [];
    const infos: string[] = [];
    const stopSink = logger.registerLogSink((entry) => {
      if (entry.level === "warn") warnings.push(entry.message);
      if (entry.level === "info") infos.push(entry.message);
    });
    const notifications: string[] = [];
    const intervals: { readonly callback: () => void; readonly intervalMs: number }[] = [];
    const context: SessionContext = {
      ...sessionContext(id),
      setInterval: (callback, intervalMs) => intervals.push({ callback, intervalMs }),
      ui: { ...sessionContext().ui, notify: (message) => notifications.push(message) },
    };
    const { default: envoyExtension } = await import(`./envoy.ts?${query}`);
    const fixture = createPi();
    envoyExtension(fixture.pi);
    await fixture.handlers.get("session_start")?.({}, context);
    const written = (word: string) =>
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
        .filter((content) => content.includes(word));
    return {
      fixture,
      /** The outage as told in the log, the UI and the transcript. */
      outage: () => ({
        logged: warnings.filter((m) => m.includes("this session receives nothing")),
        shown: notifications.filter((m) => m.includes("inbound messages are unavailable")),
        written: written("Envoy inbound unavailable"),
      }),
      restored: () => ({
        logged: infos.filter((m) => m.includes("this session receives again")),
        shown: notifications.filter((m) => m.includes("inbound messages are back")),
        written: written("Envoy inbound restored"),
      }),
      heartbeat: () => {
        for (const { callback, intervalMs } of intervals) if (intervalMs === 45_000) callback();
      },
      follow: async (topic: string) => {
        const subscribe = fixture.tools.find((tool) => tool.name === "envoy_subscribe");
        if (subscribe === undefined) throw new Error("envoy_subscribe was not registered");
        return String((await subscribe.execute("", { topics: [topic] })).details.registrationError);
      },
      stop: async () => {
        stopSink();
        await fixture.handlers.get("session_shutdown")?.({}, context);
      },
    };
  }

  test("a registration sent right after the connection closed names the close", async () => {
    // nats.js ends each subscription of a connection it closes with no error of its own, and the
    // relay's own redial has not failed yet: the close is the only reason there is.
    process.env.ENVOY_RESUBSCRIBE_DELAY_MS = "10";
    globalThis.fetch = async (input, init) => responseWithRegistration(input, init, []);
    const id = "ses_relay_closed";
    await withEnvoyTimings({ heartbeatMs: 45_000 }, async () => {
      const session = await relayLossSession(id, "relay-closed-reason");
      const parked = Promise.withResolvers<void>();
      try {
        expect(natsState.controls.get(deliverSubject(id))?.active()).toBe(true);
        // Every dial from now on waits, so the relay's own redial never ends during the row.
        natsState.heldConnect = parked.promise;
        await natsState.connections[0]?.close();
        natsState.controls.get(deliverSubject(id))?.end();
        await until(() => natsState.connectedNames.length === 2, "the relay's own redial");
        const reason = "because its relay subject could not be opened: the NATS connection closed";
        expect(await session.follow("team.relay-closed")).toContain(reason);
        for (const part of Object.values(session.outage())) {
          expect(part).toHaveLength(1);
          expect(part[0]).toContain(reason);
        }
      } finally {
        natsState.heldConnect = undefined;
        parked.resolve();
        await session.stop();
      }
    });
  });

  test("a registration sent after a start whose dial failed names the dial's failure, though no relay exists yet", async () => {
    globalThis.fetch = async (input, init) => responseWithRegistration(input, init, []);
    natsState.failConnects = 99;
    const id = "ses_start_dial_failed";
    await withEnvoyTimings({ heartbeatMs: 45_000 }, async () => {
      const session = await relayLossSession(id, "start-dial-failed-reason");
      try {
        // The start's dial failed before a relay existed; a follow outside any pass names it.
        expect(await session.follow("team.start-dial-failed")).toContain(
          "because its relay subject could not be opened: CONNECTION_REFUSED"
        );
      } finally {
        await session.stop();
      }
    });
  });

  test("a registration sent once the connection closed with an error of its own names that error", async () => {
    // nats.js settles `closed()` with the close's own error, which replaces the plugin's own words
    // for the close once it settles.
    process.env.ENVOY_RESUBSCRIBE_DELAY_MS = "10";
    globalThis.fetch = async (input, init) => responseWithRegistration(input, init, []);
    const id = "ses_relay_closed_error";
    const closeError = NatsError.errorForCode(ErrorCode.AuthenticationExpired);
    await withEnvoyTimings({ heartbeatMs: 45_000 }, async () => {
      const session = await relayLossSession(id, "relay-closed-error");
      const parked = Promise.withResolvers<void>();
      try {
        natsState.heldConnect = parked.promise;
        natsState.closeError = closeError;
        await natsState.connections[0]?.close();
        natsState.controls.get(deliverSubject(id))?.end();
        await until(() => natsState.connectedNames.length === 2, "the relay's own redial");
        const reason = `because its relay subject could not be opened: ${natsFailureReason(closeError)}`;
        expect(natsFailureReason(closeError)).not.toBe("the NATS connection closed");
        expect(await session.follow("team.relay-closed-error")).toContain(reason);
      } finally {
        natsState.heldConnect = undefined;
        parked.resolve();
        await session.stop();
      }
    });
  });

  test("a relay subscription that ends with its own error names that error, and the next heartbeat opens it again", async () => {
    process.env.ENVOY_RESUBSCRIBE_DELAY_MS = "10";
    globalThis.fetch = async (input, init) => responseWithRegistration(input, init, []);
    const id = "ses_relay_errored";
    const relay = deliverSubject(id);
    await withEnvoyTimings({ heartbeatMs: 45_000 }, async () => {
      const session = await relayLossSession(id, "relay-own-error");
      try {
        const first = natsState.controls.get(relay);
        // The connection stays up; the relay's own resubscribe is refused, and waits 15 s.
        natsState.failSubscribes = 1;
        first?.fail(NatsError.errorForCode(ErrorCode.Disconnect));
        await until(() => natsState.failSubscribes === 0, "the relay's own resubscribe");
        const reason = "because its relay subject could not be opened: DISCONNECT";
        expect(await session.follow("team.relay-errored")).toContain(reason);
        for (const part of Object.values(session.outage())) {
          expect(part).toHaveLength(1);
          expect(part[0]).toContain(reason);
        }
        session.heartbeat();
        await until(
          () =>
            natsState.controls.get(relay) !== first &&
            natsState.controls.get(relay)?.active() === true,
          "the heartbeat's open of the relay subject"
        );
        await until(() => session.restored().written.length === 1, "the restored notice");
        for (const part of Object.values(session.restored())) expect(part).toHaveLength(1);
        expect(natsState.connectedNames).toHaveLength(1);
      } finally {
        await session.stop();
      }
    });
  });

  test("two opens of the live view's control subject that race each other take it once", async () => {
    // The control subject's own resubscribe and the heartbeat's pass both dial a closed
    // connection; whichever lands second takes nothing.
    process.env.ENVOY_RESUBSCRIBE_DELAY_MS = "10";
    globalThis.fetch = async (input, init) => responseWithRegistration(input, init, []);
    const id = "ses_control_race";
    const control = `agentstream.${id}.control`;
    await withEnvoyTimings({ heartbeatMs: 45_000 }, async () => {
      const session = await relayLossSession(id, "control-open-race");
      const parked = Promise.withResolvers<void>();
      try {
        natsState.heldConnect = parked.promise;
        await natsState.connections[0]?.close();
        natsState.controls.get(control)?.end();
        await until(() => natsState.connectedNames.length === 2, "the control subject's own dial");
        session.heartbeat();
        await until(() => natsState.connectedNames.length === 3, "the heartbeat's dial");
        natsState.heldConnect = undefined;
        parked.resolve();
        await until(
          () => (natsState.controlsByTopic.get(control) ?? []).some((c, i) => i > 0 && c.active()),
          "the control subject opened again"
        );
        await settle();
        // The first subscription ended with its connection; the stand-in still calls an ended
        // subscription active, so only those taken after it are counted.
        const live = (natsState.controlsByTopic.get(control) ?? [])
          .slice(1)
          .filter((c) => c.active());
        expect(live).toHaveLength(1);
      } finally {
        natsState.heldConnect = undefined;
        parked.resolve();
        await session.stop();
      }
    });
  });

  test("the budget of the pass: a pass whose deadline is 7.5 s dials once, bounded by that deadline", async () => {
    await withEnvoyTimings({ heartbeatMs: 5_000, passDeadlineMs: 7_500 }, async () => {
      globalThis.fetch = async (input, init) => responseWithRegistration(input, init, {});
      const bounds: number[] = [];
      setNatsConnectForTests(
        (async (options: ConnectionOptions) => {
          bounds.push(options.timeout ?? Number.NaN);
          throw NatsError.errorForCode(ErrorCode.ConnectionRefused);
        }) as unknown as typeof realConnect,
        NATS_DIAL_PLAN
      );
      try {
        const context: SessionContext = {
          cwd: "/tmp/envoy-omp-test",
          sessionManager: { ...topLevelSession, getSessionId: () => "ses_pass_budget" },
          setInterval: () => {},
          ui: sessionContext().ui,
        };
        const { default: envoyExtension } = await import("./envoy.ts?pass-budget");
        const fixture = createPi();
        envoyExtension(fixture.pi);
        await fixture.handlers.get("session_start")?.({}, context);
        // The rest of the pass needs 20.25 s, more than the 7.5 s it has, so no redial fits.
        expect(bounds).toHaveLength(1);
        expect(bounds[0]).toBeLessThanOrEqual(7_500);
        expect(bounds[0]).toBeGreaterThan(6_000);
      } finally {
        setNatsConnectForTests(natsStandInConnect);
      }
    });
  }, 30_000);

  // legacy-subjects-gate.sh reads each connection's build from the end of the name the hub's census
  // of connections lists: the image's build-commit stamp, else a packed build's release version,
  // else none.
  test("the connection name the server sees ends with the plugin's build", async () => {
    await withEnvoyTimings({ heartbeatMs: 5_000, passDeadlineMs: 7_500 }, async () => {
      globalThis.fetch = async (input, init) => responseWithRegistration(input, init, {});
      const names: string[] = [];
      setNatsConnectForTests(
        (async (options: ConnectionOptions) => {
          names.push(options.name ?? "");
          throw NatsError.errorForCode(ErrorCode.ConnectionRefused);
        }) as unknown as typeof realConnect,
        NATS_DIAL_PLAN
      );
      const stampDirectory = mkdtempSync(join(tmpdir(), "pi-envoy-build-"));
      try {
        // An instance of its own (the query), as each row of this file loads one: the build source
        // it is given is that instance's module state.
        const entry = await import("./envoy.ts?connection-build");
        const commit = "0123456789abcdef0123456789abcdef01234567";
        const rows = [
          { stamp: `${commit}\n`, packed: true, want: `omp-ses_build legion@${commit}` },
          {
            stamp: undefined,
            packed: true,
            want: `omp-ses_build @sjawhar/pi-envoy@${packageVersion}`,
          },
          { stamp: undefined, packed: false, want: "omp-ses_build" },
        ];
        for (const [index, row] of rows.entries()) {
          const stamp = join(stampDirectory, `build-commit-${index}`);
          if (row.stamp !== undefined) writeFileSync(stamp, row.stamp);
          entry.setBuildSourceForTests({ stamp, packed: row.packed });
          const context: SessionContext = {
            cwd: "/tmp/envoy-omp-test",
            sessionManager: { ...topLevelSession, getSessionId: () => "ses_build" },
            setInterval: () => {},
            ui: sessionContext().ui,
          };
          const fixture = createPi();
          entry.default(fixture.pi);
          names.length = 0;
          await fixture.handlers.get("session_start")?.({}, context);
          await fixture.handlers.get("session_shutdown")?.({}, context);
          expect(names[0]).toBe(row.want);
        }
      } finally {
        rmSync(stampDirectory, { recursive: true, force: true });
        setNatsConnectForTests(natsStandInConnect);
      }
    });
  }, 30_000);

  for (const { row, error, shown, withheld } of [
    {
      row: "names the NATS failure in the refused registration and in every part of the notice",
      error: () => new NatsError("'Authentication Timeout'", ErrorCode.AuthenticationTimeout),
      shown: "Authentication Timeout",
      withheld: [] as string[],
    },
    {
      row: "withholds the text of a NATS failure that carries no code, in every part of the notice",
      error: () => new Error("bad INFO from 10.9.8.7:4222 MARKER-7f3a"),
      shown: "because its relay subject could not be opened:",
      withheld: ["10.9.8.7", "MARKER-7f3a"],
    },
  ]) {
    test(`a session whose dial fails ${row}`, async () => {
      globalThis.fetch = async (input, init) => responseWithRegistration(input, init, {});
      natsState.failConnects = 99;
      natsState.failError = error;
      const warnings: string[] = [];
      const stopSink = logger.registerLogSink((entry) => {
        if (entry.level === "warn") warnings.push(entry.message);
      });
      const start = Date.now();
      try {
        const notifications: string[] = [];
        const intervals: (() => void)[] = [];
        const context: SessionContext = {
          cwd: "/tmp/envoy-omp-test",
          sessionManager: { ...topLevelSession, getSessionId: () => "ses_named_failure" },
          setInterval: (callback) => intervals.push(callback),
          ui: { ...sessionContext().ui, notify: (message) => notifications.push(message) },
        };
        const { default: envoyExtension } = await import(
          `./envoy.ts?named-failure-${withheld.length}`
        );
        const fixture = createPi();
        envoyExtension(fixture.pi);
        await fixture.handlers.get("session_start")?.({}, context);
        // The start race is held for its bound (`INBOUND_FIRST_OPEN_BOUND_MS`). Past it, the
        // background retry's pass, whose dial fails too, tells the session.
        setSystemTime(new Date(start + INBOUND_FIRST_OPEN_BOUND_MS + 1_000));
        await until(() => {
          intervals.at(-1)?.();
          return notifications.some((m) => m.includes("inbound messages are unavailable"));
        }, "the outage notice");

        const parts = {
          "the session_start notice": notifications.find((m) =>
            m.includes("this session is not registered")
          ),
          "the UI notice": notifications.find((m) =>
            m.includes("inbound messages are unavailable")
          ),
          // Held until the host is settled, and written to the transcript at once.
          "the transcript notice": fixture.entries
            .map((entry) => ("data" in entry ? entry.data : undefined))
            .map((data) =>
              typeof data === "object" &&
              data !== null &&
              "content" in data &&
              typeof data.content === "string"
                ? data.content
                : ""
            )
            .find((content) => content.includes("Envoy inbound unavailable")),
          "the log line": warnings.find((m) => m.includes("this session receives nothing")),
        };
        for (const [part, text] of Object.entries(parts)) {
          expect(text, `${part} was not given`).toBeDefined();
          expect(text, `${part} does not name the failure`).toContain(shown);
          // Named once: the pass's own failure text is not added to a refusal that names it.
          expect(
            text?.split("because its relay subject could not be opened").length,
            `${part} names the failure more than once`
          ).toBe(2);
          for (const secret of withheld)
            expect(text, `${part} printed ${secret}`).not.toContain(secret);
        }
      } finally {
        setSystemTime();
        stopSink();
      }
    });
  }

  // A failing row for #4140 at c8903b20b8, found by the acceptance (AcceptE2-2). It was put in
  // packages/pi-envoy/extensions/envoy.test.ts, inside `describe("envoy OMP extension", ...)`,
  // beside "natsCredential follows a session switch", and it moved with that row to this file:
  // run `bun test extensions/nats-start.test.ts -t 'a broker that answers no trade'` in
  // packages/pi-envoy.
  // With the broker trade one retry loop per session (71dc1b565f), a broker that answers 503 is
  // traded again at 15 s doubling to 5 minutes, and each caller waits on that loop within its own
  // deadline. A pass of the session's registration holds several such callers: the listener calls
  // (the pod's broker credential, since it holds no Envoy bearer) and the NATS dial's `ready` (20 s).
  // Together they outlast the pass's 30 s deadline, so the queue abandons the task, and the notice
  // says "a queued task did not settle within 30000ms and was abandoned", with no word of the
  // trade. At 4ec0330624 the same pass names the failed trade. On the real stack (the acceptance's
  // leak matrix) this was 12 rows whose trade cannot succeed: a broker refusing connections,
  // answering 500 or a malformed token, an unreadable machine token.
  test("a broker that answers no trade: the session's notice names the trade, and no pass is abandoned", async () => {
    // The pass deadline the story above names, 30 s, the deadline's floor (`MIN_PASS_DEADLINE_MS`),
    // with the heartbeat shortened to it, since a deadline is never shorter than the heartbeat. The
    // queue's own timer is then the row's bound, on the same loop as the pass: a pass that
    // outlasts it is abandoned, and the session is told so.
    const passMs = 30_000;
    await withEnvoyTimings({ heartbeatMs: passMs, passDeadlineMs: passMs }, async () => {
      const tokenFile = join(configDirectory, "projected-token");
      writeFileSync(tokenFile, `${MACHINE_IDENTITY_TOKEN}\n`);
      process.env.NATS_AUTH = "broker";
      process.env.AGENT_SECRETS_URL = "http://broker.test";
      process.env.AGENT_SECRETS_MACHINE_IDENTITY = "kubernetes";
      process.env.AGENT_SECRETS_MACHINE_TOKEN_FILE = tokenFile;
      globalThis.fetch = async (input, init) => {
        const request = new Request(input, init);
        if (new URL(request.url).host !== "broker.test")
          return responseWithRegistration(input, init, []);
        await request.text();
        return new Response("the broker is away", { status: 503 });
      };
      const notifications: string[] = [];
      const intervals: { callback: () => void; intervalMs: number }[] = [];
      const context: SessionContext = {
        cwd: "/tmp/envoy-omp-test",
        sessionManager: { ...topLevelSession, getSessionId: () => "ses_broker_away" },
        setInterval: (callback, intervalMs) => intervals.push({ callback, intervalMs }),
        // The one extra call a failed heartbeat arms is not run: the row reads the heartbeat.
        setTimeout: () => undefined,
        ui: { ...sessionContext().ui, notify: (message) => notifications.push(message) },
      };
      const { default: envoyExtension } = await import("./envoy.ts?broker-answers-no-trade");
      const fixture = createPi();
      envoyExtension(fixture.pi);
      await fixture.handlers.get("session_start")?.({}, context);
      expect(
        notifications.some((message) => message.includes("the trade for the session's token"))
      ).toBe(true);
      // The next heartbeat pass, while the broker is still away, as a pod's heartbeat runs it: the
      // heartbeat's own interval, whose tick says what its pass came to, a failure or a pass the
      // queue abandoned at its deadline. The 15 s retry the start's failure arms says neither, so a
      // row that ran it saw nothing of its pass.
      const heartbeat = intervals.find(({ intervalMs }) => intervalMs === passMs);
      if (heartbeat === undefined) throw new Error("the heartbeat interval was not registered");
      const before = notifications.length;
      heartbeat.callback();
      // Its pass ends inside the queue's 30 s deadline, the trade failing at 20 s, or is abandoned
      // there and says so: a heartbeat that says nothing a minute on has hung, and is named.
      await until(
        () => notifications.length > before,
        "the heartbeat's notice of what its pass came to",
        60_000
      );
      expect(notifications.slice(before)).toEqual([
        expect.stringContaining("envoy: registry heartbeat failed"),
      ]);
      expect(notifications.some((message) => message.includes("did not settle within"))).toBe(
        false
      );
      await fixture.handlers.get("session_shutdown")?.({}, context);
    });
  }, 90_000);

  /** A pod whose NATS connection presents its broker token, at a broker that answers each trade
   *  as `answer(n)` says for its n-th: a 503, or a token it mints for the trade's session. */
  function brokerPod(answer: (trade: number) => "503" | "grant"): { minted: Map<string, string> } {
    const tokenFile = join(configDirectory, "projected-token");
    writeFileSync(tokenFile, `${MACHINE_IDENTITY_TOKEN}\n`);
    process.env.NATS_AUTH = "broker";
    process.env.AGENT_SECRETS_URL = "http://broker.test";
    process.env.AGENT_SECRETS_MACHINE_IDENTITY = "kubernetes";
    process.env.AGENT_SECRETS_MACHINE_TOKEN_FILE = tokenFile;
    const minted = new Map<string, string>();
    let trades = 0;
    globalThis.fetch = async (input, init) => {
      const request = new Request(input, init);
      if (new URL(request.url).host !== "broker.test")
        return responseWithRegistration(input, init, []);
      const session = new URLSearchParams(await request.text()).get("session") ?? "";
      trades += 1;
      if (answer(trades) === "503") return new Response("the broker is away", { status: 503 });
      const token = fixtureSecret("nats");
      minted.set(token, session);
      return Response.json({
        access_token: token,
        issued_token_type: "urn:ietf:params:oauth:token-type:access_token",
        token_type: "DPoP",
        expires_in: 900,
      });
    };
    return { minted };
  }

  /** A session's context whose notices are kept with how long after `started` each was made: the
   *  start itself can hold the turn, so the time is taken as the notice is made. */
  function timedContext(sessionID: string, started: number) {
    const notifications: { readonly message: string; readonly at: number }[] = [];
    const context: SessionContext = {
      cwd: "/tmp/envoy-omp-test",
      sessionManager: { ...topLevelSession, getSessionId: () => sessionID },
      setInterval: () => {},
      ui: {
        ...sessionContext().ui,
        notify: (message) => notifications.push({ message, at: Date.now() - started }),
      },
    };
    return { context, notifications };
  }

  test("a broker that never grants: the start fails at its 20 s bound, naming the 503, inside its pass", async () => {
    // The start waits for ready within its bound (`TRADE_DEADLINE_MS`, 20 s), through the retry
    // loop's retries, and then fails naming what the last trade came to, before the pass's
    // deadline: a pass abandoned at that deadline names no cause at all. The deadline is its floor
    // here, 30 s (`MIN_PASS_DEADLINE_MS`, the heartbeat shortened to it), and the queue's own
    // timer is the row's upper bound, on the same loop as the start's: a start that outlasts it is
    // abandoned, and the session is told that instead of the trade.
    const passMs = 30_000;
    await withEnvoyTimings({ heartbeatMs: passMs, passDeadlineMs: passMs }, async () => {
      brokerPod(() => "503");
      const { default: envoyExtension } = await import("./envoy.ts?broker-answers-503");
      const fixture = createPi();
      envoyExtension(fixture.pi);
      // Timed from the start itself, not from before the module loads: a module slow to load on a
      // loaded machine is no slower start.
      const { context, notifications } = timedContext("ses_broker_503", Date.now());
      await fixture.handlers.get("session_start")?.({}, context);
      const notice = notifications.find(({ message }) =>
        message.includes("the trade for the session's token")
      );
      expect(
        notice,
        `no notice named the trade: ${notifications.map(({ message }) => message).join(" | ")}`
      ).toBeDefined();
      expect(notice?.message).toContain("HTTP 503");
      // A timer fires no sooner than it is due, so the start waited out its bound however loaded
      // the machine.
      expect(notice?.at).toBeGreaterThanOrEqual(19_000);
      expect(notifications.some(({ message }) => message.includes("did not settle within"))).toBe(
        false
      );
      await fixture.handlers.get("session_shutdown")?.({}, context);
    });
  }, 60_000);

  test("a first trade that answers 503 and then grants: the start presents its token in its first pass", async () => {
    // The start has nothing to do until it holds a token, so it waits for ready: the retry
    // loop's next trade, 15 s at most later, grants, and the session's first pass dials and
    // registers with it, rather than waiting for the next heartbeat, 90 s on.
    const { minted } = brokerPod((trade) => (trade === 1 ? "503" : "grant"));
    const started = Date.now();
    const { context, notifications } = timedContext("ses_broker_once_away", started);
    const { default: envoyExtension } = await import("./envoy.ts?broker-away-once");
    const fixture = createPi();
    envoyExtension(fixture.pi);
    await fixture.handlers.get("session_start")?.({}, context);
    expect(
      natsState.presented.map((token) => minted.get(token)),
      `the start presented no token: ${notifications.map(({ message }) => message).join(" | ")}`
    ).toEqual(["ses_broker_once_away"]);
    expect(natsState.controls.get("notifications.agent.ses_broker_once_away")?.active()).toBe(true);
    expect(
      notifications.some(({ message }) => message.includes("the trade for the session's token"))
    ).toBe(false);
    await fixture.handlers.get("session_shutdown")?.({}, context);
  }, 60_000);

  test("a first NATS dial whose broker token the hub refuses twice drops it, starts another trade, and says so", async () => {
    // The pass's dials share the keeper's count of the token's refusals (`connectNats`'
    // `hooks.credential`), so the dial drops a token the hub refused twice, as the keeper does on
    // an open connection. The waits between the dials are recorded, not slept.
    const { minted } = brokerPod(() => "grant");
    natsState.failConnects = 2;
    natsState.failError = () =>
      new NatsError("'Authorization Violation'", ErrorCode.AuthorizationViolation);
    setNatsConnectForTests(
      natsStandInConnect,
      { firstDelayMs: 5, maxDelayMs: 5, budgetMs: 30_000 },
      { delay: async () => {}, random: () => 0 }
    );
    const warnings: string[] = [];
    const stopSink = logger.registerLogSink((entry) => {
      if (entry.level === "warn") warnings.push(entry.message);
    });
    try {
      const { context, notifications } = timedContext("ses_first_dial_refused", Date.now());
      const { default: envoyExtension } = await import("./envoy.ts?first-dial-refused-twice");
      const fixture = createPi();
      envoyExtension(fixture.pi);
      await fixture.handlers.get("session_start")?.({}, context);
      expect(natsState.failConnects, notifications.map(({ message }) => message).join(" | ")).toBe(
        0
      );
      // The start's token, and the trade the drop started.
      await until(() => minted.size === 2, "the trade the drop started");
      expect([...minted.values()]).toEqual(["ses_first_dial_refused", "ses_first_dial_refused"]);
      expect(warnings.filter((message) => message.includes(NATS_DIAL_TOKEN_DROPPED))).toHaveLength(
        1
      );
      await fixture.handlers.get("session_shutdown")?.({}, context);
    } finally {
      stopSink();
      setNatsConnectForTests(natsStandInConnect);
    }
  }, 30_000);

  test("natsCredential follows a session switch", async () => {
    // A session that presents its broker token (NATS_AUTH=broker) is admitted to its own subjects
    // alone, so after a switch the connection presents the new session's token, and is back,
    // before anything of the new session's is subscribed.
    const tokenFile = join(configDirectory, "projected-token");
    writeFileSync(tokenFile, `${MACHINE_IDENTITY_TOKEN}\n`);
    process.env.NATS_AUTH = "broker";
    process.env.AGENT_SECRETS_URL = "http://broker.test";
    process.env.AGENT_SECRETS_MACHINE_IDENTITY = "kubernetes";
    process.env.AGENT_SECRETS_MACHINE_TOKEN_FILE = tokenFile;
    const traded: string[] = [];
    /** Each token the stub broker minted, to the session its trade wrote under. */
    const minted = new Map<string, string>();
    globalThis.fetch = async (input, init) => {
      const request = new Request(input, init);
      const url = new URL(request.url);
      if (url.host !== "broker.test") return responseWithRegistration(input, init, []);
      const session = new URLSearchParams(await request.text()).get("session") ?? "";
      traded.push(session);
      const token = fixtureSecret("nats");
      minted.set(token, session);
      return Response.json({
        access_token: token,
        issued_token_type: "urn:ietf:params:oauth:token-type:access_token",
        token_type: "DPoP",
        expires_in: 900,
      });
    };
    const { default: envoyExtension } = await import("./envoy.ts?nats-credential-switch");
    const fixture = createPi();
    envoyExtension(fixture.pi);
    await fixture.handlers.get("session_start")?.({}, sessionContext("ses_switch_from"));
    // Each CONNECT presented a token the broker minted for the session it names here.
    expect(natsState.presented.map((token) => minted.get(token))).toEqual(["ses_switch_from"]);
    expect(natsState.controls.get("notifications.agent.ses_switch_from")?.active()).toBe(true);

    natsState.timeline.length = 0;
    await fixture.handlers.get("session_switch")?.({}, sessionContext("ses_switch_to"));
    expect(natsState.presented.map((token) => minted.get(token))).toEqual([
      "ses_switch_from",
      "ses_switch_to",
    ]);
    // The reconnect comes before every subscribe of the new session's subjects.
    const moved = natsState.timeline.filter(
      (entry) => entry === "reconnect" || entry.includes("ses_switch_to")
    );
    expect(moved[0]).toBe("reconnect");
    expect(moved.length).toBeGreaterThan(1);
    expect(natsState.controls.get("notifications.agent.ses_switch_to")?.active()).toBe(true);
    expect(traded).toContain("ses_switch_to");
    await fixture.handlers.get("session_shutdown")?.({}, sessionContext("ses_switch_to"));
  });

  test("the keeper's warning of a refused token reaches the session as a notice, once", async () => {
    // A token the server refuses though it is live is a session that stops receiving, which the
    // session is told of as every other inbound outage is (the review of 2fa6fbf7cf, M2).
    const { minted } = brokerPod(() => "grant");
    const { context, notifications } = timedContext("ses_keeper_notice", Date.now());
    const { default: envoyExtension } = await import("./envoy.ts?keeper-notice");
    const fixture = createPi();
    envoyExtension(fixture.pi);
    await fixture.handlers.get("session_start")?.({}, context);
    expect(natsState.presented.map((token) => minted.get(token))).toEqual(["ses_keeper_notice"]);
    const refused = () =>
      notifications.filter(({ message }) =>
        message.includes("refused the connection's live broker token")
      );
    for (const read of natsState.statusReaders) {
      read({ type: "error", data: "AUTHORIZATION_VIOLATION" });
    }
    // The reader's warning is this process's own work, done within turns of the push; a bound of
    // two-thirds of the row's time names a hang without failing a machine that stalls.
    await until(() => refused().length > 0, "the keeper's notice", 20_000);
    expect(refused().map(({ message }) => message)).toEqual([
      "envoy: the NATS server refused the connection's live broker token (AUTHORIZATION_VIOLATION): the auth service is away, or the token was revoked; the connection keeps redialling, and at the second refusal the token is dropped and another traded",
    ]);
    await fixture.handlers.get("session_shutdown")?.({}, context);
  }, 30_000);

  test("a switch whose new token never comes fails inside a short pass, naming the trade", async () => {
    // ENVOY_HEARTBEAT_MS=10000 ENVOY_PASS_DEADLINE_MS=10000: the follow takes at most 8 s of the
    // pass (`followBoundMs`), its trade 4 s of that, so the pass names what the trade came to
    // rather than being abandoned at its deadline with no word of the cause (the review of
    // 2fa6fbf7cf, m3: unbounded, the trade alone waited 20 s).
    await withEnvoyTimings({ heartbeatMs: 10_000, passDeadlineMs: 10_000 }, async () => {
      brokerPod((trade) => (trade === 1 ? "grant" : "503"));
      const started = Date.now();
      // When the follow's trade for the new session reached the broker: the follow starts its
      // clock just before it, which the switch's own work before the pass can put well after the
      // row's switch. The pod's fetch is `rowFetch()`: `globalThis.fetch` reads the file's
      // recording wrapper, which calls whatever a row installed, so a wrapper that called it would
      // call itself forever.
      const pod = rowFetch();
      let followTradeAt: number | undefined;
      globalThis.fetch = async (input, init) => {
        const request = new Request(input, init);
        if (new URL(request.url).host === "broker.test") {
          const session = new URLSearchParams(await request.clone().text()).get("session");
          if (session === "ses_follow_to") followTradeAt ??= Date.now() - started;
        }
        return await pod(input, init);
      };
      const before = timedContext("ses_follow_from", started);
      const { default: envoyExtension } = await import("./envoy.ts?follow-bound");
      const fixture = createPi();
      envoyExtension(fixture.pi);
      await fixture.handlers.get("session_start")?.({}, before.context);
      const after = timedContext("ses_follow_to", started);
      const switchedAt = Date.now() - started;
      await fixture.handlers.get("session_switch")?.({}, after.context);
      const all = () => [...before.notifications, ...after.notifications];
      const named = () =>
        all().find(({ message }) => message.includes("could not take the new session's token"));
      await until(() => named() !== undefined, "a notice naming the follow's trade", 45_000);
      expect(named()?.message).toContain("HTTP 503");
      const at = named()?.at ?? Number.POSITIVE_INFINITY;
      // Not before the trade's 4 s share, which no timer ends early: counted from the switch,
      // before the follow started its clock.
      expect(at - switchedAt).toBeGreaterThanOrEqual(3_900);
      // Inside the follow's own 8 s bound (`followBoundMs`), counted from its trade: the 4 s share
      // and as much again for a loaded machine to say so, the share the follow would leave the
      // connection. The pass's own 10 s deadline is the bound the queue keeps: a follow that
      // outlasted it would be abandoned, which says so below.
      expect(followTradeAt).toBeDefined();
      expect(at - (followTradeAt ?? Number.NEGATIVE_INFINITY)).toBeLessThan(8_000);
      expect(all().some(({ message }) => message.includes("did not settle within"))).toBe(false);
      await fixture.handlers.get("session_shutdown")?.({}, after.context);
    });
  }, 60_000);

  test("after a switch whose move failed, the old session's viewer is replayed nothing of the new one", async () => {
    // The move to the new session's token fails (its trade is refused outright, a refusal no
    // trade cures, so the move fails at once rather than at the follow's deadline), so the new
    // session's agent-stream subject never opens. What the host records next is the new session's,
    // and the old session's control subject must not answer a replay with it.
    const tokenFile = join(configDirectory, "projected-token");
    writeFileSync(tokenFile, `${MACHINE_IDENTITY_TOKEN}\n`);
    process.env.NATS_AUTH = "broker";
    process.env.AGENT_SECRETS_URL = "http://broker.test";
    process.env.AGENT_SECRETS_MACHINE_IDENTITY = "kubernetes";
    process.env.AGENT_SECRETS_MACHINE_TOKEN_FILE = tokenFile;
    globalThis.fetch = async (input, init) => {
      const request = new Request(input, init);
      if (new URL(request.url).host !== "broker.test")
        return responseWithRegistration(input, init, []);
      const session = new URLSearchParams(await request.text()).get("session") ?? "";
      if (session !== "ses_failed_from") {
        return Response.json({ error: "invalid_client" }, { status: 401 });
      }
      return Response.json({
        access_token: fixtureSecret("nats"),
        issued_token_type: "urn:ietf:params:oauth:token-type:access_token",
        token_type: "DPoP",
        expires_in: 900,
      });
    };
    const { default: envoyExtension } = await import("./envoy.ts?nats-credential-failed-switch");
    const fixture = createPi();
    envoyExtension(fixture.pi);
    await fixture.handlers.get("session_start")?.({}, sessionContext("ses_failed_from"));
    const oldControl = "agentstream.ses_failed_from.control";
    expect(natsState.controls.get(oldControl)?.active()).toBe(true);

    const next = sessionContext("ses_failed_to");
    await fixture.handlers.get("session_switch")?.({}, next);
    expect(natsState.controls.has("agentstream.ses_failed_to.control")).toBe(false);
    const marker = "a turn of the session switched to";
    await fixture.handlers.get("message_end")?.(
      { message: { content: [{ text: marker, type: "text" }], role: "assistant", timestamp: 30 } },
      next
    );

    // A viewer of the old session asks for its history.
    const control = natsState.controls.get(oldControl);
    control?.push(JSON.stringify({ type: "watch", v: 1 }));
    control?.push(JSON.stringify({ type: "replay", v: 1 }), "inbox-old-viewer");
    for (let index = 0; index < 20; index += 1) await Promise.resolve();
    await Bun.sleep(50);
    const replayed = natsState.published
      .filter((each) => each.subject === "inbox-old-viewer")
      .map((each) => new TextDecoder().decode(each.data));
    expect(replayed.join("\n")).not.toContain(marker);
    expect(natsState.controls.get(oldControl)?.active()).toBe(false);
    await fixture.handlers.get("session_shutdown")?.({}, next);
  }, 30_000);

  test("after a rollback with no event, a viewer of the session left is replayed nothing of the one returned to", async () => {
    // Two sessions of one host: it switches from A to B, B's viewer attaches, and the host then
    // returns to A with no event. Until the next pass runs, A's control subject is closed and B's
    // is still open; what the host records then is A's, and B's viewer must be replayed only B's.
    globalThis.fetch = async (input, init) => responseWithRegistration(input, init, []);
    const { default: envoyExtension } = await import("./envoy.ts?agent-stream-ring-per-session");
    const fixture = createPi();
    envoyExtension(fixture.pi);
    const first = sessionContext("ses_ring_a");
    await fixture.handlers.get("session_start")?.({}, first);
    const second = sessionContext("ses_ring_b");
    await fixture.handlers.get("session_switch")?.({}, second);
    const control = natsState.controls.get("agentstream.ses_ring_b.control");
    expect(control?.active()).toBe(true);
    const say = (text: string, timestamp: number) => ({
      message: { content: [{ text, type: "text" }], role: "assistant", timestamp },
    });
    const ownTurn = "a turn of the session switched to";
    await fixture.handlers.get("message_end")?.(say(ownTurn, 40), second);
    // The host returns to A with no event, and records A's next turn.
    const returnedTurn = "a turn of the session returned to";
    await fixture.handlers.get("message_end")?.(say(returnedTurn, 50), first);

    control?.push(JSON.stringify({ type: "watch", v: 1 }));
    control?.push(JSON.stringify({ type: "replay", v: 1 }), "inbox-viewer-of-b");
    for (let index = 0; index < 20; index += 1) await Promise.resolve();
    await Bun.sleep(50);
    const replayed = natsState.published
      .filter((each) => each.subject === "inbox-viewer-of-b")
      .map((each) => new TextDecoder().decode(each.data))
      .join("\n");
    // The replay can carry a turn at all: B's own is in it.
    expect(replayed).toContain(ownTurn);
    expect(replayed).not.toContain(returnedTurn);
    await fixture.handlers.get("session_shutdown")?.({}, first);
  }, 30_000);

  test("natsCredential is kept renewed for the connection's life, and the keeper stops at shutdown", async () => {
    // Two-second tokens, so the keeper renews within the test: each renewal lands a new token,
    // and the connection reconnects to present it before the old one lapses. Shutdown stops it.
    const tokenFile = join(configDirectory, "projected-token");
    writeFileSync(tokenFile, `${MACHINE_IDENTITY_TOKEN}\n`);
    process.env.NATS_AUTH = "broker";
    process.env.AGENT_SECRETS_URL = "http://broker.test";
    process.env.AGENT_SECRETS_MACHINE_IDENTITY = "kubernetes";
    process.env.AGENT_SECRETS_MACHINE_TOKEN_FILE = tokenFile;
    const minted: string[] = [];
    globalThis.fetch = async (input, init) => {
      const request = new Request(input, init);
      if (new URL(request.url).host !== "broker.test")
        return responseWithRegistration(input, init, []);
      const token = fixtureSecret("nats");
      minted.push(token);
      return Response.json({
        access_token: token,
        issued_token_type: "urn:ietf:params:oauth:token-type:access_token",
        token_type: "DPoP",
        expires_in: 2,
      });
    };
    const { default: envoyExtension } = await import("./envoy.ts?nats-credential-keeper");
    const fixture = createPi();
    envoyExtension(fixture.pi);
    await fixture.handlers.get("session_start")?.({}, sessionContext("ses_kept"));
    const [first] = minted;
    expect(first).toBeString();
    expect(natsState.presented[0]).toBe(first);
    // What the start did is done; from here a reconnect is the keeper's.
    natsState.timeline.length = 0;
    const before = natsState.presented.length;
    const deadline = Date.now() + 5_000;
    while (!natsState.timeline.includes("reconnect") && Date.now() < deadline) await Bun.sleep(50);
    expect(natsState.timeline).toContain("reconnect");
    expect(natsState.presented.length).toBeGreaterThan(before);
    expect(natsState.presented.at(-1)).not.toBe(natsState.presented[before - 1]);
    await fixture.handlers.get("session_shutdown")?.({}, sessionContext("ses_kept"));
    // Stopped, it trades and reconnects no more.
    const reconnects = natsState.timeline.filter((entry) => entry === "reconnect").length;
    const traded = minted.length;
    await Bun.sleep(2_500);
    expect(natsState.timeline.filter((entry) => entry === "reconnect").length).toBe(reconnects);
    expect(minted).toHaveLength(traded);
  }, 15_000);
});
