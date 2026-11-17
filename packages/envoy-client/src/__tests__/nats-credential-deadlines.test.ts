// packages/envoy-client/src/__tests__/nats-credential-deadlines.test.ts
//
// How long a session's broker NATS credential waits, and on what: `ready` fails at its deadline,
// named, for a broker that never answers and for a pod session with no registration yet; `renew`
// takes the new session's token across a switch; a CONNECT with no live token trades on its own
// and keeps the broker's back-off while the registration is missing; and `followNatsCredential`
// moves a connection to the new session's token after a switch, ending at its own deadline when
// the connection closes, never comes back, or the new session has no registration yet.

import { describe, expect, test } from "bun:test";
import {
  type BrokerNatsCredential,
  brokerNatsCredential,
  followNatsCredential,
} from "../nats-credential";
import { natsBrokerStandIns } from "./broker-stand-ins";

const { podEnvironment, servers, stubBroker } = natsBrokerStandIns("nats-credential-deadlines");

describe("ready", () => {
  test("a broker that never answers the trade fails it, named, at the deadline", async () => {
    // A broker that takes the request and never answers: with no deadline a start waited on it in
    // silence (64 s and counting, at 579d9b45f6), where a hub that never answers fails at nats.js's
    // own dial timeout, naming it.
    const server = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch: () => new Promise<Response>(() => undefined),
    });
    servers.push(server);
    const credential = brokerNatsCredential(
      podEnvironment(`http://127.0.0.1:${server.port}`),
      "ses_hung",
      "NATS_AUTH",
      300
    );
    const started = Date.now();
    const outcome = await credential.ready().then(
      () => "ready",
      (error: unknown) => (error instanceof Error ? error.message : String(error))
    );
    expect(outcome).toBe(
      "NATS_AUTH is broker, but the secrets broker did not answer the trade for the session's token within 0.3 s"
    );
    expect(Date.now() - started).toBeLessThan(5_000);
  }, 15_000);

  test("a startup's bound shorter than the trade deadline ends its wait first, naming the broker's silence", async () => {
    // A start whose pass deadline is shorter than the default bounds its wait by that deadline
    // less what the rest of its pass needs (`ReadyOptions.boundMs`), so its pass is not abandoned
    // with no cause named.
    const server = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch: () => new Promise<Response>(() => undefined),
    });
    servers.push(server);
    const credential = brokerNatsCredential(
      podEnvironment(`http://127.0.0.1:${server.port}`),
      "ses_short_pass",
      "NATS_AUTH",
      10_000
    );
    const started = Date.now();
    const outcome = await credential.ready({ waitForReady: true, boundMs: 400 }).then(
      () => "ready",
      (error: unknown) => (error instanceof Error ? error.message : String(error))
    );
    expect(outcome).toBe(
      "NATS_AUTH is broker, but the secrets broker did not answer the trade for the session's token within 0.4 s"
    );
    expect(Date.now() - started).toBeLessThan(3_000);
  }, 20_000);

  test("a pod session the broker holds no registration for fails it at the deadline, naming the session", async () => {
    // The token client waits out a missing registration for as long as its caller waits (#4153),
    // so a start that handed it no deadline waited until the registration landed, and then said
    // the broker had not answered, which it had.
    const broker = stubBroker(600);
    broker.state.unregistered = true;
    const credential = brokerNatsCredential(
      podEnvironment(broker.url),
      "ses_pending",
      "NATS_AUTH",
      300
    );
    const started = Date.now();
    const outcome = await credential.ready().then(
      () => "ready",
      (error: unknown) => (error instanceof Error ? error.message : String(error))
    );
    expect(outcome).toBe(
      "NATS_AUTH is broker, and the session's token waits on its registration: the secrets broker has no registration for this pod's session ses_pending yet, and the trade is being retried in the background until it lands (pod_session_not_registered: No registration says this pod may write under the session name 'ses_pending'. A pod writes under a session name its deployment registered for it.)"
    );
    expect(Date.now() - started).toBeLessThan(5_000);
  }, 15_000);
});

describe("renew", () => {
  test("a switch while a renewal is in flight takes the new session's token", async () => {
    // A trade reads the session's name as it starts, so the keeper's renewal of the session
    // before can be in flight when the host switches. The switch's renewal must not be handed
    // that one: the connection would come back as the session before, and the new session's
    // subscribes be refused. The switch ends the wait for the session before, which a missing
    // registration could hold for minutes. Two-second tokens, so each renewal is a real trade.
    const broker = stubBroker(2);
    let session = "ses_before";
    const credential = brokerNatsCredential(podEnvironment(broker.url), () => session);
    await credential.ready();
    credential.authenticator("nonce-1");
    const keeperRenewal = credential.renew().then(
      () => "renewed",
      (error: unknown) => (error instanceof Error ? error.message : String(error))
    );
    session = "ses_after";
    await credential.renew();
    expect(await keeperRenewal).toBe(
      "the host moved from the session ses_before to ses_after while ses_before's token was traded"
    );
    expect(credential.needsReconnect()).toBe(true);
    const auth = credential.authenticator("nonce-2") as { auth_token: string };
    const presented = broker.state.minted.find((one) => one.value === auth.auth_token);
    expect(presented?.session).toBe("ses_after");
  });

  test("CONNECTs with no live token while the registration is missing keep the broker's back-off", async () => {
    // The token client's retry loop trades for a pod session with no registration on its own
    // schedule, 2 s doubling to 30 s, each wait at least half its length, and no caller cuts a
    // wait short. nats.js asks the authenticator at every attempt, every 2 s for a connection the
    // server refuses, so an authenticator that traded at each one would trade at that pace. Inside
    // the schedule's first slot, a CONNECT every 50 ms makes no trade of its own.
    const broker = stubBroker(600);
    broker.state.unregistered = true;
    const credential = brokerNatsCredential(podEnvironment(broker.url), "ses_unregistered");
    const deadline = Date.now() + 900;
    let connects = 0;
    while (Date.now() < deadline) {
      expect(credential.authenticator("nonce")).toBeUndefined();
      connects += 1;
      await Bun.sleep(50);
    }
    expect(connects).toBeGreaterThan(10);
    expect(broker.state.trades).toBe(1);
  }, 15_000);

  test("a CONNECT with no live token presents nothing, trades, and presents the token next", async () => {
    // No ready(), renew() or keeper: the authenticator's own trade is what the next reconnect
    // presents, so a connection whose token lapsed comes back on its own.
    const broker = stubBroker(600);
    const credential = brokerNatsCredential(podEnvironment(broker.url), "ses_self");
    expect(credential.authenticator("nonce-1")).toBeUndefined();
    const traded = () => broker.state.minted.length > 0;
    const deadline = Date.now() + 5_000;
    while (!traded() && Date.now() < deadline) await Bun.sleep(20);
    // The trade has landed once the authenticator presents its token.
    let auth: { auth_token?: string } | undefined;
    while (auth === undefined && Date.now() < deadline) {
      auth = credential.authenticator("nonce-2") as { auth_token?: string } | undefined;
      if (auth === undefined) await Bun.sleep(20);
    }
    expect(auth?.auth_token).toBe(broker.state.minted[0]?.value ?? "no token minted");
    expect(broker.state.minted.map((one) => one.session)).toEqual(["ses_self"]);
  });
});

describe("followNatsCredential", () => {
  /** A connection whose reconnect asks the authenticator again and reports the statuses the
   *  caller pushes, and which the caller closes. */
  function controlledConnection(credential: BrokerNatsCredential) {
    const pushed: { type: string }[] = [];
    let wake = () => {};
    let stopped = false;
    const ended = Promise.withResolvers<void>();
    const statuses = {
      stop: () => {
        stopped = true;
        wake();
      },
      async *[Symbol.asyncIterator]() {
        while (!stopped) {
          const next = pushed.shift();
          if (next !== undefined) {
            yield next;
            continue;
          }
          await new Promise<void>((resolve) => {
            wake = resolve;
          });
        }
      },
    };
    return {
      push: (type: string) => {
        pushed.push({ type });
        wake();
      },
      close: () => ended.resolve(),
      stoppedReader: () => stopped,
      connection: {
        reconnect: async () => {
          credential.authenticator("nonce-again");
        },
        status: () => statuses,
        closed: () => ended.promise,
      } as unknown as Parameters<typeof followNatsCredential>[0],
    };
  }

  async function switched(): Promise<BrokerNatsCredential> {
    const broker = stubBroker(600);
    let session = "ses_from";
    const credential = brokerNatsCredential(podEnvironment(broker.url), () => session);
    await credential.ready();
    credential.authenticator("nonce-1");
    session = "ses_to";
    return credential;
  }

  test("a connection that closes before it is back rejects at once, and stops its reader", async () => {
    const credential = await switched();
    const rig = controlledConnection(credential);
    const following = followNatsCredential(rig.connection, credential);
    // A status that is not the reconnect ends nothing.
    rig.push("disconnect");
    await Bun.sleep(50);
    rig.close();
    await expect(following).rejects.toThrow(
      "the NATS connection closed before it came back as the new session"
    );
    expect(rig.stoppedReader()).toBe(true);
  });

  test("a connection that never comes back rejects 8 s after the token lands, and stops its reader", async () => {
    const credential = await switched();
    const rig = controlledConnection(credential);
    await expect(followNatsCredential(rig.connection, credential)).rejects.toThrow(
      "the NATS connection did not come back as the new session within 8 s"
    );
    expect(rig.stoppedReader()).toBe(true);
  }, 30_000);

  test("a switch to a session with no registration yet stops waiting for its token at 20 s, naming why", async () => {
    // The case the token client's wait exists for: a pod's host moves to a session whose
    // registration has not landed. The follow must end at its own bound, naming why, so the pass
    // behind it (a registration, a role claim) runs again rather than stalling.
    const broker = stubBroker(600);
    let session = "ses_from";
    const credential = brokerNatsCredential(podEnvironment(broker.url), () => session);
    await credential.ready();
    credential.authenticator("nonce-1");
    broker.state.unregistered = true;
    session = "ses_to";
    const rig = controlledConnection(credential);
    const started = Date.now();
    await expect(followNatsCredential(rig.connection, credential)).rejects.toThrow(
      "the secrets broker has no registration for this pod's session ses_to yet"
    );
    expect(Date.now() - started).toBeLessThan(25_000);
  }, 40_000);

  test("a switch whose first trade answers 503 waits for ready and takes the token the next trade grants", async () => {
    // A switch has nothing to do until it holds the new session's token, so its trade waits for
    // ready within its bound: the retry loop's next trade, 15 s at most later, grants, and the
    // follow goes on to its reconnect. Failing fast, the switch would end at the 503 and the new
    // session would wait for the next heartbeat.
    const broker = stubBroker(600);
    let session = "ses_from";
    const credential = brokerNatsCredential(podEnvironment(broker.url), () => session);
    await credential.ready();
    credential.authenticator("nonce-1");
    broker.state.down = true;
    session = "ses_to";
    const rig = controlledConnection(credential);
    const before = broker.state.trades;
    const following = followNatsCredential(rig.connection, credential);
    while (broker.state.trades === before) await Bun.sleep(20);
    // The first trade for the new session was answered 503; the broker is back for the next.
    broker.state.down = false;
    while (broker.state.minted.length < 2) await Bun.sleep(50);
    await Bun.sleep(50);
    rig.push("reconnect");
    await following;
    expect(credential.needsReconnect()).toBe(false);
    expect(broker.state.minted.at(-1)?.session).toBe("ses_to");
  }, 40_000);

  test("a reconnect returns once its CONNECT presented the new session's token", async () => {
    const credential = await switched();
    const rig = controlledConnection(credential);
    const following = followNatsCredential(rig.connection, credential);
    await Bun.sleep(50);
    rig.push("reconnect");
    await following;
    expect(credential.needsReconnect()).toBe(false);
    expect(rig.stoppedReader()).toBe(true);
  });

  test("a redial already in flight with the old token is followed by another reconnect", async () => {
    // The connection is redialling when the switch comes: nats.js's reconnect() does nothing
    // while it is not connected, and the redial's CONNECT presented the old token, so its
    // reconnect leaves the move undone. The follow asks again, and the next CONNECT presents the
    // new session's token.
    const credential = await switched();
    const rig = controlledConnection(credential);
    const asked: string[] = [];
    (rig.connection as { reconnect: () => Promise<void> }).reconnect = async () => {
      if (asked.push("reconnect") === 1) return;
      credential.authenticator("nonce-again");
      rig.push("reconnect");
    };
    const following = followNatsCredential(rig.connection, credential);
    await Bun.sleep(50);
    // The in-flight redial comes back, admitted as the old session.
    rig.push("reconnect");
    const started = Date.now();
    await following;
    expect(Date.now() - started).toBeLessThan(2_000);
    expect(asked).toEqual(["reconnect", "reconnect"]);
    expect(credential.needsReconnect()).toBe(false);
    expect(rig.stoppedReader()).toBe(true);
  }, 30_000);
});
