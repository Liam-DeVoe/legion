/** The outside driver's `hold` and `release` (`e2e/acceptance/outside-edge.ts`): the session `run`
 *  kept, registered at the listener again and held live while a check acts as the person's
 *  session, then ended. The rows run them against a stand-in of the edge: envoy-client's relay
 *  socket stand-in (`@legion/envoy-client/test/relay-socket-stand-in`), its broker's trade and its
 *  socket, with the device login's grants, the registration and its end served through its
 *  `routes`. Each row first makes a device login with the real broker client and keeps it, and a
 *  session, where `run` keeps them, then runs the driver with the variables a check gives it and
 *  no other: `hold` with OUTSIDE_PROJECT, OUTSIDE_BROKER_URL, OUTSIDE_LISTENER_URL and
 *  OUTSIDE_EDGE_STATE (and OUTSIDE_EDGE_ALLOW_HTTP, for the stand-in's http), `release` with
 *  OUTSIDE_EDGE_STATE alone. Each way a hold ends is a row, and so is each refusal: a hold time
 *  outside 60 to 3600 s, a listener that refuses the registration, a listener that closes the held
 *  socket, an unregister that fails, no hold to release, a holder that ignores SIGTERM, and a held
 *  pid whose process started at another time, which release does not signal. The rows start no
 *  browser and no stack, so they run wherever `e2e/__tests__` runs; they read `/proc`, as `hold`
 *  and `release` do. */

import { describe, expect, test } from "bun:test";
import { randomBytes } from "node:crypto";
import { existsSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  deviceLoginHost,
  finishDeviceLogin,
  startDeviceLogin,
} from "@legion/cipher-client/device-login";
import { legacyAgentSubject } from "@legion/contracts";
import {
  type RelaySocketStandIn,
  standInDeliverSubject,
  startRelaySocketStandIn,
} from "@legion/envoy-client/test/relay-socket-stand-in";
import { consumerDelivery } from "@legion/envoy-client/test/subscribe-answer";
import {
  type BoundedRun,
  eventually,
  rowLimit,
  runBounded,
  watch,
  whether,
} from "./bounded-subprocess";

const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));
const driver = join(dispatchPackage, "e2e/acceptance/outside-edge.ts");

/** The edge stand-in of these rows, and what it was asked. */
interface HoldEdge {
  readonly relay: RelaySocketStandIn;
  /** The session each registration named, and each unregister, in order. */
  readonly registrations: string[];
  readonly unregisters: string[];
}

/** Starts the edge: the relay socket stand-in, which trades and serves the socket, with the
 *  device login's two grants, approved at once, the registration and its end. `registration`
 *  and `unregister` are the statuses those two answer, 200 where unset. */
function startHoldEdge(answers: { registration?: number; unregister?: number } = {}): HoldEdge {
  const registrations: string[] = [];
  const unregisters: string[] = [];
  const pair = () =>
    Response.json({
      access_token: `hold-login-${randomBytes(16).toString("hex")}`,
      token_type: "DPoP",
      expires_in: 900,
      refresh_token: `hold-refresh-${randomBytes(16).toString("hex")}`,
      scope: "machine",
      credential_id: "hold-credential",
      credential_expires_at: new Date(Date.now() + 86_400_000)
        .toISOString()
        .replace(/\.\d{3}Z$/, "Z"),
    });
  const relay = startRelaySocketStandIn({
    routes: async (request, standIn) => {
      const { pathname } = new URL(request.url);
      if (request.method === "POST" && pathname === "/v1/oauth/device_authorization") {
        return Response.json({
          device_code: `hold-device-${randomBytes(8).toString("hex")}`,
          user_code: "HOLD-CODE",
          verification_uri: `${standIn.url}/credentials/machine`,
          expires_in: 600,
          interval: 1,
        });
      }
      if (request.method === "POST" && pathname === "/v1/oauth/token") {
        const grant = new URLSearchParams(await request.text()).get("grant_type");
        if (grant === "urn:ietf:params:oauth:grant-type:device_code" || grant === "refresh_token")
          return pair();
        // The exchange: the stand-in's own trade mints the session's token.
        return undefined;
      }
      if (request.method === "POST" && pathname === "/v1/interests/subscribe") {
        const body: unknown = await request.json();
        const session =
          typeof body === "object" && body !== null ? String(Reflect.get(body, "session_id")) : "";
        registrations.push(session);
        const status = answers.registration ?? 200;
        if (status !== 200) {
          return Response.json(
            { error: "the stand-in refuses this registration", code: "registration_refused" },
            { status }
          );
        }
        return Response.json({
          session_id: session,
          machine_id: "outside-edge-hold-stand-in",
          dir: "/outside-edge-e2e",
          topics: [legacyAgentSubject(session)],
          delivery: consumerDelivery(session),
        });
      }
      const ended = /^\/v1\/sessions\/([^/]+)$/.exec(pathname);
      if (request.method === "DELETE" && ended !== null) {
        unregisters.push(decodeURIComponent(ended[1] ?? ""));
        const status = answers.unregister ?? 200;
        if (status !== 200) {
          return Response.json({ error: "the stand-in refuses this unregister" }, { status });
        }
        return Response.json({ deleted: true });
      }
      return undefined;
    },
  });
  return { relay, registrations, unregisters };
}

/** A state directory as `run` leaves it for `hold`: a device login made with the real broker
 *  client at `edge`, and a session. */
async function keptState(edge: HoldEdge): Promise<{ state: string; session: string }> {
  const state = mkdtempSync(join(tmpdir(), "outside-edge-hold-"));
  const started = await startDeviceLogin({
    brokerUrl: edge.relay.url,
    person: "erin@outside.example",
    host: deviceLoginHost("outside-edge-e2e"),
  });
  await finishDeviceLogin(started, {
    path: join(state, "device-login.json"),
    sleep: async () => undefined,
  });
  const session = `outside-e2e-${randomBytes(6).toString("hex")}`;
  writeFileSync(join(state, "session"), session, { mode: 0o600 });
  return { state, session };
}

/** `hold`, with the variables a check gives it and these beside them. */
function hold(edge: HoldEdge, state: string, extra: Record<string, string> = {}) {
  return runBounded({
    argv: ["bun", driver, "hold"],
    bound: "outsideEdgeHold",
    cwd: dispatchPackage,
    env: {
      PATH: process.env.PATH ?? "",
      HOME: process.env.HOME ?? "",
      OUTSIDE_EDGE_ALLOW_HTTP: "1",
      OUTSIDE_PROJECT: "OWN",
      OUTSIDE_BROKER_URL: edge.relay.url,
      OUTSIDE_LISTENER_URL: edge.relay.url,
      OUTSIDE_EDGE_STATE: state,
      ...extra,
    },
    from: import.meta.url,
  });
}

/** `release`, with OUTSIDE_EDGE_STATE alone. */
function release(state: string) {
  return runBounded({
    argv: ["bun", driver, "release"],
    bound: "short",
    cwd: dispatchPackage,
    env: { PATH: process.env.PATH ?? "", HOME: process.env.HOME ?? "", OUTSIDE_EDGE_STATE: state },
    from: import.meta.url,
  });
}

/** Waits for the hold's `held`, as a check polls for it; where the hold ends first, or the short
 *  bound passes, throws with what the hold printed. */
async function heldBy(state: string, held: Promise<BoundedRun>): Promise<void> {
  const ended = watch(held);
  if (await whether(() => existsSync(join(state, "held")), ended)) return;
  const ran = ended() ? await held : undefined;
  throw new Error(
    ran === undefined
      ? "the hold wrote no held within the short bound"
      : `the hold ended, exit ${ran.code}, and wrote no held:\n${ran.stdout}${ran.stderr}`
  );
}

/** The mode bits of a file. */
const modeOf = (path: string) => statSync(path).mode & 0o777;

/** The start of process `pid`, in clock ticks since boot: field 22 of `/proc/<pid>/stat`, the
 *  fields after the name counted from field 3, since the name may hold spaces. */
function startOf(pid: number): number {
  const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
  return Number(stat.slice(stat.lastIndexOf(")") + 2).split(" ")[19]);
}

/** The process a file names as `held` does, `<pid> <start>`; undefined until it is written. */
function namedBy(path: string): { readonly pid: number; readonly start: number } | undefined {
  const named = existsSync(path)
    ? /^([1-9][0-9]*) ([0-9]+)$/.exec(readFileSync(path, "utf8"))
    : null;
  return named === null ? undefined : { pid: Number(named[1]), start: Number(named[2]) };
}

/** A holder that takes SIGTERM and goes on, printing that it took it. Once its handler is in
 *  place it writes `<pid> <start>` to `at`, as `hold` names itself in `held`, so a SIGTERM sent
 *  after that reaches the handler. */
function stubbornHolder(at: string): Promise<BoundedRun> {
  const holder = runBounded({
    argv: [
      "bun",
      "--eval",
      `process.on("SIGTERM", () => console.log("took SIGTERM"));
const fs = require("node:fs");
const stat = fs.readFileSync("/proc/self/stat", "utf8");
const start = stat.slice(stat.lastIndexOf(")") + 2).split(" ")[19];
fs.writeFileSync(${JSON.stringify(at)}, \`\${process.pid} \${start}\`);
setInterval(() => undefined, 1_000);`,
    ],
    bound: "short",
    env: { PATH: process.env.PATH ?? "", HOME: process.env.HOME ?? "" },
    from: import.meta.url,
  });
  // A row that fails before it awaits the holder leaves it to end at its bound, and that end is
  // this row's own failure, not an error between rows: a row that awaits it still sees it.
  holder.catch(() => undefined);
  return holder;
}

describe.concurrent("hold and release, against an edge stand-in", () => {
  test(
    "hold registers the kept session and holds its socket until release ends it, unregistered",
    async () => {
      const edge = startHoldEdge();
      const { state, session } = await keptState(edge);
      try {
        const held = hold(edge, state);
        await heldBy(state, held);
        // While held: the session registered once, its socket open, its relay subject taken.
        expect(edge.registrations).toEqual([session]);
        expect(edge.relay.sockets(session)).toBe(1);
        expect(edge.relay.subs).toContainEqual({
          session,
          subject: standInDeliverSubject(session),
        });
        const { pid, start } = namedBy(join(state, "held")) ?? { pid: 0, start: 0 };
        expect(pid).toBeGreaterThan(0);
        expect(start).toBe(startOf(pid));
        expect(modeOf(join(state, "held"))).toBe(0o600);
        expect(edge.unregisters).toEqual([]);
        const released = await release(state);
        expect(released.stdout).toBe(`RELEASED ${session}\n`);
        expect(released.code).toBe(0);
        const ran = await held;
        expect(ran.stdout).toBe(`HELD ${session}\nRELEASED ${session}\n`);
        expect(ran.code).toBe(0);
        expect(ran.stderr).toContain(`the hold of ${session} ends: SIGTERM`);
        expect(readFileSync(join(state, "released"), "utf8")).toBe("unregistered");
        expect(modeOf(join(state, "released"))).toBe(0o600);
        expect(edge.unregisters).toEqual([session]);
        expect(edge.registrations).toEqual([session]);
        await eventually("the held socket's close", () => edge.relay.sockets(session) === 0);
      } finally {
        edge.relay.stop();
        rmSync(state, { force: true, recursive: true });
      }
    },
    rowLimit("outsideEdgeHold", "short", "short")
  );

  test(
    "hold ends itself once OUTSIDE_EDGE_HOLD_SECONDS have passed, the least of them, 60",
    async () => {
      const edge = startHoldEdge();
      const { state, session } = await keptState(edge);
      try {
        // The hold is another process on the real clock, and 60 s is the least it takes, so this
        // row spends that minute; the rows run at once, so the file costs it once.
        const started = performance.now();
        const ran = await hold(edge, state, { OUTSIDE_EDGE_HOLD_SECONDS: "60" });
        expect(performance.now() - started).toBeGreaterThanOrEqual(60_000);
        expect(ran.stdout).toBe(`HELD ${session}\nRELEASED ${session}\n`);
        expect(ran.code).toBe(0);
        expect(ran.stderr).toContain(`the hold of ${session} ends: 60 s passed`);
        expect(edge.unregisters).toEqual([session]);
        // The hold ended on its own: release reads what it wrote and signals no process.
        const released = await release(state);
        expect(released.stdout).toBe(`RELEASED ${session}\n`);
        expect(released.code).toBe(0);
      } finally {
        edge.relay.stop();
        rmSync(state, { force: true, recursive: true });
      }
    },
    rowLimit("outsideEdgeHold", "short")
  );

  test(
    "a hold time outside 60 to 3600 s is refused, naming the variable, and registers nothing",
    async () => {
      const edge = startHoldEdge();
      const { state } = await keptState(edge);
      try {
        const runs = await Promise.all(
          ["59", "3601", "1e3", "-60"].map((seconds) =>
            hold(edge, state, { OUTSIDE_EDGE_HOLD_SECONDS: seconds })
          )
        );
        for (const ran of runs) {
          expect(ran.stdout).toBe(
            "UNKNOWN: no session held: OUTSIDE_EDGE_HOLD_SECONDS must be a whole number of seconds from 60 to 3600\n"
          );
          expect(ran.code).toBe(2);
        }
        expect(existsSync(join(state, "held"))).toBe(false);
        expect(edge.registrations).toEqual([]);
        expect(edge.relay.upgrades).toEqual([]);
      } finally {
        edge.relay.stop();
        rmSync(state, { force: true, recursive: true });
      }
    },
    rowLimit({ atOnce: ["outsideEdgeHold", "outsideEdgeHold", "outsideEdgeHold"] })
  );

  test(
    "a listener that refuses the registration: UNKNOWN naming it, no held, and nothing to release",
    async () => {
      const edge = startHoldEdge({ registration: 403 });
      const { state, session } = await keptState(edge);
      try {
        const ran = await hold(edge, state);
        expect(ran.stdout).toStartWith(`UNKNOWN: the listener did not register ${session}: `);
        expect(ran.stdout).toContain("the stand-in refuses this registration");
        expect(ran.stdout).not.toContain("HELD");
        expect(ran.code).toBe(2);
        expect(edge.registrations).toEqual([session]);
        expect(existsSync(join(state, "held"))).toBe(false);
        const released = await release(state);
        expect(released.stdout).toBe("UNKNOWN: no session is held\n");
        expect(released.code).toBe(2);
      } finally {
        edge.relay.stop();
        rmSync(state, { force: true, recursive: true });
      }
    },
    rowLimit("outsideEdgeHold", "short")
  );

  test(
    "a listener that closes the held socket: the close and its code printed, no second registration, exit 2",
    async () => {
      const edge = startHoldEdge();
      const { state, session } = await keptState(edge);
      try {
        const held = hold(edge, state);
        await heldBy(state, held);
        edge.relay.close(1011, "the stand-in ends the held socket", session);
        const ran = await held;
        const closed = "1011 the stand-in ends the held socket";
        expect(ran.stdout).toBe(
          `HELD ${session}\nUNKNOWN: the listener closed the held socket of ${session} (${closed}); it was not registered again\n`
        );
        expect(ran.code).toBe(2);
        expect(edge.registrations).toEqual([session]);
        expect(edge.unregisters).toEqual([session]);
        expect(readFileSync(join(state, "released"), "utf8")).toBe(
          `the listener closed the held socket (${closed}); unregistered`
        );
        const released = await release(state);
        expect(released.stdout).toBe(
          `UNKNOWN: ${session} was not released: the listener closed the held socket (${closed}); unregistered\n`
        );
        expect(released.code).toBe(2);
      } finally {
        edge.relay.stop();
        rmSync(state, { force: true, recursive: true });
      }
    },
    rowLimit("outsideEdgeHold", "short", "short")
  );

  test(
    "an unregister that fails: RELEASED and exit 2, released naming the failure, and release UNKNOWN",
    async () => {
      const edge = startHoldEdge({ unregister: 500 });
      const { state, session } = await keptState(edge);
      try {
        const held = hold(edge, state);
        await heldBy(state, held);
        const released = await release(state);
        expect(released.stdout).toStartWith(
          `UNKNOWN: ${session} was not released: the unregister failed: `
        );
        expect(released.code).toBe(2);
        const ran = await held;
        expect(ran.stdout).toBe(`HELD ${session}\nRELEASED ${session}\n`);
        expect(ran.code).toBe(2);
        expect(edge.unregisters).toEqual([session]);
        expect(readFileSync(join(state, "released"), "utf8")).toStartWith(
          "the unregister failed: "
        );
      } finally {
        edge.relay.stop();
        rmSync(state, { force: true, recursive: true });
      }
    },
    rowLimit("outsideEdgeHold", "short", "short")
  );

  test(
    "release with no hold: UNKNOWN: no session is held",
    async () => {
      const state = mkdtempSync(join(tmpdir(), "outside-edge-hold-"));
      try {
        const released = await release(state);
        expect(released.stdout).toBe("UNKNOWN: no session is held\n");
        expect(released.code).toBe(2);
      } finally {
        rmSync(state, { force: true, recursive: true });
      }
    },
    rowLimit("short")
  );

  test(
    "release sends SIGKILL to a holder still running 15 s after its SIGTERM, and exits 2",
    async () => {
      const state = mkdtempSync(join(tmpdir(), "outside-edge-hold-"));
      const session = `outside-e2e-${randomBytes(6).toString("hex")}`;
      writeFileSync(join(state, "session"), session, { mode: 0o600 });
      try {
        const holder = stubbornHolder(join(state, "held"));
        await eventually(
          "the holder's held file",
          () => namedBy(join(state, "held")) !== undefined
        );
        const { pid } = namedBy(join(state, "held")) ?? { pid: 0 };
        expect(pid).toBeGreaterThan(0);
        const started = performance.now();
        const released = await release(state);
        expect(performance.now() - started).toBeGreaterThanOrEqual(15_000);
        expect(released.stdout).toBe(
          `UNKNOWN: the holder ${pid} of ${session} still ran 15 s after its SIGTERM, so it was sent SIGKILL; ${session} may still be registered\n`
        );
        expect(released.code).toBe(2);
        const ran = await holder;
        expect(ran.stdout).toBe("took SIGTERM\n");
        expect(ran.code).not.toBe(0);
        expect(() => process.kill(pid, 0)).toThrow();
      } finally {
        rmSync(state, { force: true, recursive: true });
      }
    },
    rowLimit("short", "short", "short")
  );

  test(
    "release signals no process at the held pid that started at another time: the held process is gone",
    async () => {
      // The row above is this one's control: the same holder, named by its own start, takes the
      // SIGTERM. Here `held` names its pid with another start, as a pid a later process took.
      const state = mkdtempSync(join(tmpdir(), "outside-edge-hold-"));
      const session = `outside-e2e-${randomBytes(6).toString("hex")}`;
      writeFileSync(join(state, "session"), session, { mode: 0o600 });
      const ready = join(state, "ready");
      const holder = stubbornHolder(ready);
      let alive = 0;
      try {
        await eventually("the holder's ready file", () => namedBy(ready) !== undefined);
        const { pid, start } = namedBy(ready) ?? { pid: 0, start: 0 };
        expect(pid).toBeGreaterThan(0);
        alive = pid;
        writeFileSync(join(state, "held"), `${pid} ${start + 1}`, { mode: 0o600 });
        const released = await release(state);
        expect(() => process.kill(pid, 0)).not.toThrow();
        process.kill(pid, "SIGKILL");
        alive = 0;
        const ran = await holder;
        expect(released.stdout).toBe("UNKNOWN: the held process is gone\n");
        expect(released.code).toBe(2);
        // It took no SIGTERM, which it would have printed.
        expect(ran.stdout).toBe("");
      } finally {
        // A row that failed before the holder was ended ends it here, rather than at its bound;
        // one that ended already throws, and is left so.
        try {
          if (alive > 0) process.kill(alive, "SIGKILL");
        } catch {}
        rmSync(state, { force: true, recursive: true });
      }
    },
    rowLimit("short", "short", "short")
  );
});
