// packages/cipher-client/src/__tests__/renewal-cycle.test.ts
//
// How often a client trades, and a device login refreshes, across several lives of the broker's
// access token, whatever life the broker gives it (`BROKER_ACCESS_TOKEN_SECONDS`, S here) and
// whatever its subject has left. Each row moves the process's clock (`setSystemTime`) a fixed step
// at a time, calls `token()` at each step, and fails at the first call after which the client has
// traded, or the login has refreshed, more often in the last life than the rules allow: a token is
// held to the moment the renewal corpus names (`testdata/renewal.json`, never before half its
// life), and a device login's read trade asks for no more than half the life a refresh gives (the
// cap, `DeviceLoginIdentity.spend`). The clients are a device login's read client and its plain
// client, and a person's read client whose sign-in credential nears its end, which a read trade
// does not move.
//
// The clock stands still between steps, so a row's counts are those of a stepped clock: a trade
// made at exactly the moment a rule names is made there, where real time would have moved on a few
// milliseconds. This file takes from the package's source only what a build before these rules
// had too, so it runs against such a build as a control.

import { afterAll, afterEach, beforeAll, expect, setSystemTime, test } from "bun:test";
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import { stubDeviceLoginBroker, writeDeviceLoginFile } from "../../test/device-logins";
import { signedIn, stubIssuer, writeSignIn } from "../../test/sign-ins";
import { DEVICE_LOGIN_FILE_VAR } from "../device-login-file";
import {
  BROKER_URL_VAR,
  type BrokerEnvironment,
  brokerTokenClient,
  IDENTITY_DEVICE_LOGIN,
  IDENTITY_VAR,
  MACHINE_AUDIENCE_VAR,
  SESSION_VAR,
} from "../environment";
import { METADATA_HOST_VAR } from "../metadata-identity";
import {
  SIGNIN_CLIENT_ID_VAR,
  SIGNIN_CLIENT_SECRET_FILE_VAR,
  SIGNIN_DIR_VAR,
  SIGNIN_ISSUER_VAR,
} from "../signins";
import {
  type BrokerTokenClient,
  DEFAULT_RENEW_BEFORE_MS,
  EXPIRES_IN_STEP_MS,
  TRADE_ATTEMPT_MS,
} from "../token-client";
import { FORM_SCOPE, SCOPE_READ } from "../wire";
import { stubBroker } from "./stub";

const makeDirectory = temporaryDirectories("renewal-cycle");

const PERSON = "ada@example.com";
const CLIENT_ID = "example-device-client";

/** How far the clock moves between two calls. */
const STEP_SECONDS = 10;

/** How many lives of the access token a row runs across. */
const LIVES = 6;

/** The client's renewal window, in seconds. */
const WINDOW_SECONDS = DEFAULT_RENEW_BEFORE_MS / 1000;

/** The minute every trade of a device login keeps (`ACCESS_MARGIN_MS`, two trade attempts). */
const MARGIN_SECONDS = (2 * TRADE_ATTEMPT_MS) / 1000;

/** What a read trade asks a device login for: the window and a second (`#trade`). */
const READ_ASK_SECONDS = (DEFAULT_RENEW_BEFORE_MS + EXPIRES_IN_STEP_MS) / 1000;

/** The renewal corpus both clients replay, read by relative path, never a copy. */
interface RenewalCorpus {
  readonly rows: readonly {
    readonly life_seconds: number;
    readonly renew_before_seconds: number;
    readonly renews_after_seconds: number;
  }[];
}
const renewalCorpus = (await Bun.file(
  new URL("../../../envoy/internal/cipherclient/testdata/renewal.json", import.meta.url)
).json()) as RenewalCorpus;

/** How long after it lands the corpus has a token of `life` seconds traded again, under the
 *  client's window; a life the corpus has no row for is refused, so no bound rests on a guess. */
function moment(life: number): number {
  const row = renewalCorpus.rows.find(
    (one) => one.life_seconds === life && one.renew_before_seconds === WINDOW_SECONDS
  );
  if (row === undefined) throw new Error(`the renewal corpus has no row for a life of ${life} s`);
  return row.renews_after_seconds;
}

/** The least life a device login's read trade spends an access token with, in seconds: the ask,
 *  capped at half the life S a refresh gave, never under the minute. */
function readCap(life: number): number {
  return Math.max(MARGIN_SECONDS, Math.min(READ_ASK_SECONDS, life / 2));
}

/** Trades in one life of a device login's plain client: every plain token lives S, and is held to
 *  the corpus's moment for S. */
function plainTradesPerLife(life: number): number {
  return Math.ceil(life / moment(life));
}

/**
 * Trades in one life of a device login's read client that spends an access token with at least
 * `cap` left: a read token traded from a fresh access token lives S and is held to the corpus's
 * moment for S; one traded with exactly the cap left, which `spend`'s strict `<` spends without a
 * refresh, lives the cap and brings one more trade before the next refresh. Where the cap is S or
 * more, every trade refreshes and there is no such trade. The one extra holds where the cap is at
 * least half of what that first renewal leaves, so the next trade after it refreshes.
 */
function readTradesPerLife(life: number, cap: number): number {
  if (cap >= life) return Math.ceil(life / moment(life));
  if (life - moment(life) >= 2 * cap) {
    throw new Error(`a read token at ${life} s halves more than once before a refresh`);
  }
  return Math.ceil(life / moment(life)) + 1;
}

/** Trades in one life of a person's read client: full tokens as a plain client's, and as the
 *  credential nears its end, each read token lives what the credential has left and is held to
 *  at least half of it, so calls a step apart see at most log2(S / step) + 1 of those. */
function personTradesPerLife(life: number): number {
  return Math.ceil(life / moment(life)) + Math.floor(Math.log2(life / STEP_SECONDS)) + 1;
}

/** Refreshes in one life of a device login whose trades spend an access token with at least
 *  `cap` left: a refresh leaves S, and the next comes only once less than the cap is left, so
 *  refreshes come more than S − cap apart (the cap is at most half of S, so at most two a life);
 *  where the cap is S or more, every trade refreshes. */
function refreshesPerLife(life: number, cap: number, trades: number): number {
  return cap >= life ? trades : Math.ceil(life / (life - cap));
}

/** One row's client and what it is held to. */
interface Cycle {
  readonly name: string;
  readonly life: number;
  readonly client: BrokerTokenClient;
  readonly trades: () => number;
  readonly refreshes: () => number;
  readonly tradesPerLife: number;
  readonly refreshesPerLife: number;
  /** Refreshes come more than this many seconds apart. */
  readonly refreshGap: number;
  /** Run after each call: a row whose file an older build rewrote takes out what it keeps. */
  readonly afterCall?: () => Promise<void>;
}

/** What a row's client did over its span. */
interface Counted {
  readonly trades: number;
  readonly refreshes: number;
}

/**
 * Calls `cycle.client.token()` every {@link STEP_SECONDS} across {@link LIVES} lives from
 * `start`, and after each call holds the trades and the refreshes of the last life, and the time
 * since the last refresh, to the row's bounds, naming the call that broke one.
 */
async function run(cycle: Cycle, start: number): Promise<Counted> {
  const life = cycle.life * 1000;
  const traded: number[] = [];
  const refreshed: number[] = [];
  for (let at = 0; at < LIVES * life; at += STEP_SECONDS * 1000) {
    setSystemTime(new Date(start + at));
    await cycle.client.token();
    await cycle.afterCall?.();
    while (traded.length < cycle.trades()) traded.push(at);
    while (refreshed.length < cycle.refreshes()) {
      const last = refreshed.at(-1);
      if (last !== undefined) {
        expect(
          (at - last) / 1000,
          `${cycle.name}: a refresh at ${at / 1000} s comes ${(at - last) / 1000} s after the last`
        ).toBeGreaterThan(cycle.refreshGap);
      }
      refreshed.push(at);
    }
    const inLife = (times: readonly number[]): number =>
      times.filter((time) => time > at - life).length;
    expect(
      inLife(traded),
      `${cycle.name}: the trades in the life up to the call at ${at / 1000} s`
    ).toBeLessThanOrEqual(cycle.tradesPerLife);
    expect(
      inLife(refreshed),
      `${cycle.name}: the refreshes in the life up to the call at ${at / 1000} s`
    ).toBeLessThanOrEqual(cycle.refreshesPerLife);
  }
  cycle.client.close();
  return { trades: traded.length, refreshes: refreshed.length };
}

const stops: (() => void)[] = [];
afterEach(() => {
  setSystemTime();
  for (const stop of stops.splice(0)) stop();
});

/** The metadata server a person's machine asks for its own Google identity token, the actor of a
 *  person's trade; the metadata identity reads `GCE_METADATA_HOST` from the process. */
let stopMetadata = () => {};
let previousMetadataHost: string | undefined;
beforeAll(() => {
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: () => new Response(`${fixtureSecret("machine")}\n`),
  });
  stopMetadata = () => server.stop(true);
  previousMetadataHost = process.env[METADATA_HOST_VAR];
  process.env[METADATA_HOST_VAR] = `127.0.0.1:${server.port}`;
});
afterAll(() => {
  stopMetadata();
  if (previousMetadataHost === undefined) delete process.env[METADATA_HOST_VAR];
  else process.env[METADATA_HOST_VAR] = previousMetadataHost;
});

/**
 * A device login whose broker mints access tokens of `life` seconds, as a laptop's first
 * `dispatch` command meets it (no access token kept yet), and its read or plain client. Where
 * `olderBuild` is set, an older build rewrites the login's file after every call, leaving out the
 * life a refresh gave the access token, as that build's refreshes and saves do.
 */
async function deviceLoginCycle(life: number, read: boolean, olderBuild = false): Promise<Cycle> {
  const stub = stubDeviceLoginBroker({ accessSeconds: life });
  stops.push(stub.stop);
  const path = join(makeDirectory(), "legion", "device-login.json");
  await writeDeviceLoginFile(path, stub.url);
  const env: BrokerEnvironment = {
    [BROKER_URL_VAR]: stub.url,
    [IDENTITY_VAR]: IDENTITY_DEVICE_LOGIN,
    [DEVICE_LOGIN_FILE_VAR]: path,
  };
  const made = await brokerTokenClient(env, "ses-laptop-1", read ? {} : undefined);
  if (made === undefined) throw new Error("the factory made no client for a device login");
  // With the life kept, a read trade spends a token with the cap left; with none, the minute.
  const cap = read && !olderBuild ? readCap(life) : MARGIN_SECONDS;
  const trades = read ? readTradesPerLife(life, cap) : plainTradesPerLife(life);
  const afterCall = async (): Promise<void> => {
    const json = JSON.parse(await readFile(path, "utf8")) as Record<string, unknown>;
    if (!("access_life_seconds" in json)) return;
    delete json["access_life_seconds"];
    await writeFile(path, JSON.stringify(json), { mode: 0o600 });
  };
  return {
    name: `a device login's ${read ? "read" : "plain"} client at S = ${life} s${olderBuild ? " whose file an older build rewrote" : ""}`,
    life,
    client: made.client,
    trades: () => stub.trades.length,
    refreshes: () => stub.refreshes.length,
    tradesPerLife: trades,
    refreshesPerLife: refreshesPerLife(life, cap, trades),
    refreshGap: life - cap,
    ...(olderBuild ? { afterCall } : {}),
  };
}

/**
 * A person's read client on a machine whose broker mints tokens of `life` seconds, and whose
 * sign-in credential lapses at `start` and {@link LIVES} lives: the broker answers a read trade
 * with the credential's remaining life where that is shorter, in whole seconds, truncated, as
 * `clampNarrowed` bounds it, and a read trade moves the credential not at all, as
 * `signInCredential` takes it. The row's last call comes a step before the credential lapses.
 */
async function personCycle(life: number, start: number): Promise<Cycle> {
  const ends = start + LIVES * life * 1000;
  const broker = stubBroker((trade) => {
    const left = Math.floor((ends - Date.now()) / 1000);
    if (trade.form.get(FORM_SCOPE) !== SCOPE_READ || left <= 0) {
      return { status: 400, body: { error: "invalid_grant" } };
    }
    return {
      body: {
        access_token: fixtureSecret("read"),
        issued_token_type: "urn:ietf:params:oauth:token-type:access_token",
        token_type: "DPoP",
        expires_in: Math.min(life, left),
        scope: SCOPE_READ,
      },
    };
  });
  const issuer = await stubIssuer({ clientId: CLIENT_ID, person: PERSON });
  stops.push(broker.stop, issuer.stop);
  const dir = makeDirectory();
  await writeSignIn(join(dir, "signins"), await signedIn(PERSON, { client_id: CLIENT_ID }));
  const secret = join(dir, "client-secret");
  await writeFile(secret, `${fixtureSecret("client")}\n`, { mode: 0o600 });
  const made = await brokerTokenClient(
    {
      [BROKER_URL_VAR]: broker.url,
      [IDENTITY_VAR]: "metadata",
      [MACHINE_AUDIENCE_VAR]: "example-machine-client-id",
      [SESSION_VAR]: "legion-demo-controller",
      [SIGNIN_DIR_VAR]: join(dir, "signins"),
      [SIGNIN_ISSUER_VAR]: issuer.url,
      [SIGNIN_CLIENT_ID_VAR]: CLIENT_ID,
      [SIGNIN_CLIENT_SECRET_FILE_VAR]: secret,
    },
    "ses_cycle",
    {}
  );
  if (made === undefined) throw new Error("the factory made no client for a person");
  return {
    name: `a person's read client at S = ${life} s`,
    life,
    client: made.client,
    trades: () => broker.trades.length,
    refreshes: () => 0,
    tradesPerLife: personTradesPerLife(life),
    refreshesPerLife: 0,
    refreshGap: 0,
  };
}

/** How long a row may take: every call is a turn of the event loop, and a trade a request. */
const ROW_MS = 120_000;

// Fifteen rows: each access-token life against each of the three clients.
test.each(
  [120, 300, 302, 600, 900].flatMap((life) =>
    (
      [
        ["a device login's read client", "read"],
        ["a device login's plain client", "plain"],
        ["a person's read client whose sign-in credential nears its end", "person"],
      ] as const
    ).map(([name, client]) => [name, life, client] as const)
  )
)(
  "%s trades and refreshes within its bounds at an access-token life of %i s",
  async (_name, life, client) => {
    const start = Date.now();
    const cycle =
      client === "person"
        ? await personCycle(life, start)
        : await deviceLoginCycle(life, client === "read");
    await run(cycle, start);
  },
  ROW_MS
);

// Under a minute of life every trade refreshes, plain or read, by the minute every trade keeps
// alone: the read trade's ask may add no refresh to the plain client's.
test(
  "at an access-token life of 30 s a device login's read client refreshes no more than its plain client",
  async () => {
    const start = Date.now();
    const plain = await run(await deviceLoginCycle(30, false), start);
    const read = await run(await deviceLoginCycle(30, true), start);
    expect(read.refreshes).toBeLessThanOrEqual(plain.refreshes);
  },
  ROW_MS
);

// A file an older build rewrote keeps no life, so a read trade spends a token with the minute
// left, as a plain one does, until a refresh of this build keeps a life again. At 120 s the read
// client may still refresh more often than the plain client: a read token is clamped to the
// login's access token, so it renews at its half life with less of that token left, while a plain
// token is not clamped. Both are held to the bounds of one life.
test(
  "at an access-token life of 120 s a device login whose file an older build rewrote trades and refreshes within its bounds",
  async () => {
    const start = Date.now();
    await run(await deviceLoginCycle(120, true, true), start);
    await run(await deviceLoginCycle(120, false, true), start);
  },
  ROW_MS
);
