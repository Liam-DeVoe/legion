import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { connect, Events, StringCodec } from "nats";
import {
  type EnvoyStack,
  envoyStackEnabled,
  startStoppableEnvoyStack,
  throwIfExited,
  useEnvoyStackDirectory,
} from "../../test/envoy-stack";
import { startStallProxy } from "../../test/stall-proxy";
import { RECOVERY_BOUND_MS } from "../../test/stall-scenario";
import { natsConnectionOptions } from "../nats-connection";

const codec = StringCodec();

// Reserved here, at the file's module scope: the file's own `afterAll` removes it once the
// `describe` below has stopped the stack writing into it.
const stackDirectory = useEnvoyStackDirectory();

describe.skipIf(!envoyStackEnabled)("a NATS connection whose path freezes", () => {
  let stack: EnvoyStack | undefined;
  let stopStack: (() => Promise<void>) | undefined;

  beforeAll(async () => {
    const start = startStoppableEnvoyStack({ work: stackDirectory });
    stopStack = start.stop;
    stack = await start.started;
    // A hang guard: what can hang is the stack's start, which builds natsd and the listener with
    // `go build` and waits for each to serve. It is the start the live describes of pi-envoy and
    // claude-envoy make. Worst measured cost of that start: 249 197.32 ms over 20 runs at the 96
    // rung in the Job infra-1580-krows-kfh-tgvmp of the cluster's `builds` namespace, two CPUs and
    // 8 GiB (218 busy loops, at which row A's runs took 33.5 s against the calibration's 31.2 s),
    // 2026-11-22T08:32Z to 09:41Z. The room one hang has in this file's step of the
    // `envoy-session-stall` job (`.github/workflows/ci-job-envoy-session-stall.yaml`) is 1 130 s:
    // the job's `timeout-minutes: 20`, less the 68 s from the job's start to this step's in the
    // slowest of its twelve green runs on this branch from d62604fd to a467c066 (the run at
    // b202d173), and less 2 s for the steps that run after a failed step (the ci-ok report, which
    // runs `if: always()`, and the post steps). The steps after this one are not counted: a step
    // that fails skips them. Six times the worst is 1 495 s, past that room, so the room decides:
    // 1 130 s less the 30 s kept for the `afterAll` is 1 100 s, and 1_100_000 is 4.4 times the
    // worst.
  }, 1_100_000);
  afterAll(async () => {
    await stopStack?.();
  }, 60_000);

  for (const { row, slug, stalledRedials, unackedBytes } of [
    {
      row: "A: with natsConnectionOptions it reconnects and receives again within 60 s of the freeze",
      slug: "a",
      stalledRedials: 0,
      unackedBytes: 0,
    },
    {
      row: "B: with natsConnectionOptions it reconnects and receives again within 60 s of the freeze when its first redial also hangs, and closes the hung redial's socket once it gives up on it",
      slug: "b",
      stalledRedials: 1,
      unackedBytes: 0,
    },
    {
      row: "C: with natsConnectionOptions it reconnects and receives again within 60 s of the freeze when it wrote data the frozen path never acknowledged",
      slug: "c",
      stalledRedials: 0,
      // Without patches/nats@2.29.3.patch's protocol.js hunk this row must fail: on Bun 1.4.4 a
      // 4 MiB backlog still drains into the kernel and the stale close's flush completes, 8 MiB
      // already hangs it, and 32 MiB leaves a margin.
      unackedBytes: 32 * 1024 * 1024,
    },
  ]) {
    test(row, async () => {
      if (stack === undefined) throw new Error("the Envoy stack did not start");
      const proxy = await startStallProxy(stack.natsHostPort);
      const publisher = await connect({ servers: [stack.natsUrl], name: `nats-stall-pub-${slug}` });
      const client = await connect(
        natsConnectionOptions({
          servers: [proxy.url],
          name: `nats-stall-${slug}`,
          credential: { kind: "none" },
        })
      );
      try {
        const subject = `nats-stall.${slug}.${crypto.randomUUID()}`;
        const before = Promise.withResolvers<void>();
        let arrivedAt: number | undefined;
        client.subscribe(subject, {
          callback: (error, message) => {
            if (error !== null) return;
            const body = codec.decode(message.data);
            if (body === "before") before.resolve();
            else arrivedAt ??= Date.now();
          },
        });
        await client.flush();
        publisher.publish(subject, codec.encode("before"));
        await publisher.flush();
        await before.promise;

        let reconnectedAt: number | undefined;
        void (async () => {
          for await (const status of client.status()) {
            if (status.type === Events.Reconnect) reconnectedAt ??= Date.now();
          }
        })();
        proxy.stallNext(stalledRedials);
        // The redials are the connections the proxy appended after this point. Taking them by an
        // ordinal rather than by acceptedAt >= frozenAt keeps a redial the proxy accepted inside
        // the freeze's own millisecond, which that comparison bins with the connections before it.
        const beforeFreeze = proxy.connections.length;
        const frozenAt = proxy.freeze();
        // The client's own writes after the freeze, in chunks below natsd's max_payload: more than
        // the frozen path's kernel buffers take, so they stay queued in its socket. Nothing awaits
        // a flush of them, which cannot complete.
        const chunk = new Uint8Array(64 * 1024);
        for (let sent = 0; sent < unackedBytes; sent += chunk.length) {
          client.publish(`${subject}.unacked`, chunk);
        }
        // What is measured is nats.js's own wall-clock ping and dial timers, so the wait is real
        // time. A publish reaches the client only once it has subscribed again on a path that
        // answers, and each one before that goes to the frozen connection and is lost, so one
        // goes out every quarter second until one arrives or the bound passes. A natsd or listener
        // that exits meanwhile fails the wait by name (`EnvoyStack.exited`).
        for (
          let attempt = 0;
          arrivedAt === undefined && Date.now() - frozenAt < RECOVERY_BOUND_MS;
          attempt += 1
        ) {
          throwIfExited(stack, "waiting for a publish to arrive");
          publisher.publish(subject, codec.encode(`after-${attempt}`));
          await publisher.flush();
          await Bun.sleep(250);
        }

        const reconnectMs = reconnectedAt === undefined ? undefined : reconnectedAt - frozenAt;
        const receiveMs = arrivedAt === undefined ? undefined : arrivedAt - frozenAt;
        console.log(
          `nats-stall row ${slug.toUpperCase()}: reconnect ${reconnectMs === undefined ? "none" : `${(reconnectMs / 1000).toFixed(1)} s`}, first publish received ${receiveMs === undefined ? "never" : `${(receiveMs / 1000).toFixed(1)} s`} after the freeze`
        );
        expect(reconnectMs).toBeDefined();
        expect(reconnectMs ?? Number.POSITIVE_INFINITY).toBeLessThan(RECOVERY_BOUND_MS);
        expect(receiveMs).toBeDefined();
        expect(receiveMs ?? Number.POSITIVE_INFINITY).toBeLessThan(RECOVERY_BOUND_MS);
        const redials = proxy.connections.slice(beforeFreeze);
        // The stalled redials never subscribed; the one after them did.
        expect(redials.length).toBeGreaterThan(stalledRedials);
        expect(
          redials.slice(0, stalledRedials).every((opened) => opened.subscribedAt === undefined)
        ).toBe(true);
        expect(redials[stalledRedials]?.subscribedAt).toBeDefined();
        // nats.js abandons a dial after its dial timeout; the socket it opened must go with it, so
        // the client holds no socket to a redial it gave up on. The proxy never closes a stalled
        // connection, so its close is the client's. Real nats.js dial timers: the wait polls the
        // real clock, and fails by name once natsd or the listener has exited. It has no deadline
        // of its own: if the client never closes the socket, the wait ends at the row's limit,
        // which names the row. The wait of a row its limit abandoned ends once the describe's
        // `afterAll` stops the stack: its next `throwIfExited` throws.
        for (const hung of redials.slice(0, stalledRedials)) {
          let closedAt = hung.closedAt;
          while (closedAt === undefined) {
            throwIfExited(stack, "waiting for the client to close a hung redial");
            await Bun.sleep(250);
            closedAt = hung.closedAt;
          }
          console.log(
            `nats-stall row ${slug.toUpperCase()} hung redial: accepted ${((hung.acceptedAt - frozenAt) / 1000).toFixed(1)} s after the freeze, closed ${((closedAt - hung.acceptedAt) / 1000).toFixed(1)} s after it was accepted`
          );
        }
      } finally {
        await client.close();
        await publisher.close();
        await proxy.close();
      }
      // A hang guard, not a bound on work: what can hang is a wait for the freeze's recovery or
      // for the client's close of a hung redial. Worst measured cost of each row, bun's time for
      // it, in the twelve green runs of the `envoy-session-stall` job on this branch from
      // d62604fd to a467c066, on the job's own GitHub Actions runner: A 30 428.16 ms,
      // B 40 463.93 ms and C 30 439.10 ms, all three in the run at f3ff8793. Six times the worst
      // is 242.8 s, and 250_000 is 6.2 times the worst. A row that hangs to this limit fits the
      // room the `beforeAll` above names: the stack's worst start (249 s), this row's 250 s, the
      // other two rows at their worst (30.4 s each) and the 30 s kept for the `afterAll` come to
      // 589.8 s, inside 1 130 s.
    }, 250_000);
  }
});
