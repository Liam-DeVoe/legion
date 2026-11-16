// packages/envoy-client/src/nats-credential.ts
//
// The credential a session's NATS connection presents. It is one of three: the nkey user a seed
// names, as every session presented before the hub ran an auth service; the session's broker
// token, which the auth service of a hub that runs one admits as that session alone
// (`packages/envoy/internal/broker/natscallout`); or none. Which one is the deployment's to say
// (`envoySettings`): a machine that can trade for a broker token says nothing about the hub, so
// the broker token is presented only where `NATS_AUTH` or `envoy.natsAuth` says `broker`.
//
// A broker token goes on the CONNECT as its token, with a proof of the key it is bound to over the
// nonce the server sent that one connection (`DpopKey.connectionProof`) as its signature, which
// is the claim set the Go client signs (`packages/envoy/natscred`, `Session`). The auth service
// answers with a NATS user that expires with the token, so the connection moves to the next token
// before then (`keepNatsCredential`).

import type { BrokerEnvironment } from "@legion/broker-client/environment";
import type { Auth, NatsConnection } from "nats";
import { type BrokerSessionName, brokerTokenFor, type TradedToken } from "./broker-credential";
import type { NatsSeed } from "./envoy-settings";
import { messageFor } from "./errors";

/** The variable that names which credential a session's NATS connection presents: `broker` or
 *  `none`. `envoy.natsAuth` names it in the file, and the variable overrides that field. */
export const NATS_AUTH_VAR = "NATS_AUTH";

/** The credential a session's NATS connection presents. */
export type NatsCredential =
  | { readonly kind: "seed"; readonly seed: NatsSeed }
  | { readonly kind: "broker"; readonly broker: BrokerNatsCredential }
  | { readonly kind: "none" };

/**
 * A session's broker token as its NATS connection presents it. One per process and session name:
 * the token is the one the session's listener and Dispatch calls present
 * (`brokerTokenFor`, `brokerCredential`), so a session holds one token, not two.
 */
export interface BrokerNatsCredential {
  /** Takes the token to present, trading for one where the token in hand is not live: a
   *  connection is dialled only once this has answered, since the CONNECT is signed at once. */
  ready(): Promise<void>;
  /**
   * The CONNECT's credential for the server's `nonce`: the token in hand as `auth_token`, and as
   * `sig` the proof of its key over that nonce, base64url-encoded, as the auth service reads a
   * signature. One snapshot of the token per CONNECT, so the proof always names the token sent
   * beside it. It presents nothing — and the server refuses the connection — where it holds no
   * live token or the server sent no nonce, and then starts a trade, so the next reconnect
   * presents a live token where the broker answers one. It never throws: nats.js closes a
   * connection for good when its authenticator throws.
   */
  authenticator(nonce?: string): Auth;
  /** When the token in hand lapses; the epoch before any is taken. */
  expiresAt(): Date;
  /** Takes the session's token again: a renewed one inside the renewal window, and the token of
   *  the session the name now reads, after a switch. */
  renew(): Promise<void>;
  /** Whether the last CONNECT presented a token other than the one in hand, so the connection
   *  should reconnect to present it. */
  needsReconnect(): boolean;
}

/**
 * The broker token of `env`'s machine, or of a person signed in on it, for the session `session`
 * names, as a NATS connection presents it. Nothing is traded until {@link BrokerNatsCredential.ready}
 * or {@link BrokerNatsCredential.renew}.
 */
export function brokerNatsCredential(
  env: BrokerEnvironment,
  session: BrokerSessionName | undefined
): BrokerNatsCredential {
  const take = brokerTokenFor(env, session);
  let held: TradedToken | undefined;
  let presented: string | undefined;
  let taking: Promise<void> | undefined;
  const live = (): boolean => held !== undefined && held.token.expiresAt.getTime() > Date.now();
  const renew = (): Promise<void> => {
    if (taking === undefined) {
      taking = take()
        .then((taken) => {
          held = taken;
        })
        .finally(() => {
          taking = undefined;
        });
    }
    return taking;
  };
  return {
    ready: () => (live() ? Promise.resolve() : renew()),
    renew,
    authenticator(nonce?: string): Auth {
      const snapshot = held;
      if (snapshot === undefined || !live() || nonce === undefined || nonce === "") {
        // Nothing live to present: the server refuses this CONNECT, and the trade started here
        // lands a token for the next one. Its failure is the keeper's to report.
        if (!live()) renew().catch(() => {});
        return undefined;
      }
      const accessToken = snapshot.token.accessToken;
      let sig: string;
      try {
        // base64url, as nats.go sends a signature and the auth service reads one.
        const proof = snapshot.key.connectionProof({ accessToken, nonce });
        sig = Buffer.from(proof, "utf8").toString("base64url");
      } catch {
        return undefined;
      }
      presented = accessToken;
      return { auth_token: accessToken, sig };
    },
    expiresAt: () => held?.token.expiresAt ?? new Date(0),
    needsReconnect: () =>
      presented !== undefined && held !== undefined && presented !== held.token.accessToken,
  };
}

/** The most the keeper renews ahead of a token's expiry: the token client's own window
 *  (`DEFAULT_RENEW_BEFORE_MS`, `@legion/broker-client/token-client`), inside which it trades. */
const RENEW_AHEAD_MS = 5 * 60_000;
/** The least the keeper waits between two renewals. */
const RENEW_FLOOR_MS = 1_000;
/** The keeper's retry after a renewal that landed no new token: doubling from the first to the
 *  last. */
const RETRY_FIRST_MS = 2_000;
const RETRY_LAST_MS = 60_000;
/** How often the keeper repeats its warning while renewals keep failing. */
const WARN_AGAIN_MS = 30 * 60_000;

/** How long after `now` the keeper renews a token that lapses at `expiry`: the token client's
 *  window ahead of it, or halfway there for a token that lives less than twice that window. */
function renewDelay(expiry: number, now: number): number {
  const remaining = expiry - now;
  return Math.max(RENEW_FLOOR_MS, remaining - Math.min(RENEW_AHEAD_MS, remaining / 2));
}

/**
 * Holds `connection` to a token the auth service still admits: ahead of each token's expiry it
 * renews `credential` and, when that lands a new token, reconnects, so the CONNECT presents the
 * new token before the server drops the user the old one was admitted as. A renewal that fails, or
 * lands no new token, is retried at 2 s doubling to a minute, and `warn` is told once, naming why,
 * and again every 30 minutes while it lasts; the token is never named. The connection keeps
 * reconnecting on its own meanwhile (`ignoreAuthErrorAbort`), and the authenticator starts a trade
 * of its own at each CONNECT it cannot sign, so the first live token admits it again.
 *
 * Returns the function that stops it; the connection closing stops it too.
 */
export function keepNatsCredential(
  connection: Pick<NatsConnection, "reconnect" | "isClosed" | "closed">,
  credential: BrokerNatsCredential,
  warn: (text: string) => void
): () => void {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let retries = 0;
  let warnedAt: number | undefined;
  const stop = () => {
    stopped = true;
    clearTimeout(timer);
    timer = undefined;
  };
  const schedule = (delay: number) => {
    if (stopped || connection.isClosed()) return;
    timer = setTimeout(() => void tick(), delay);
    timer.unref?.();
  };
  const retry = () => {
    retries += 1;
    schedule(Math.min(RETRY_FIRST_MS * 2 ** (retries - 1), RETRY_LAST_MS));
  };
  const tick = async () => {
    timer = undefined;
    if (stopped || connection.isClosed()) return;
    const before = credential.expiresAt().getTime();
    try {
      await credential.renew();
      if (stopped) return;
      if (credential.needsReconnect()) await connection.reconnect();
    } catch (error) {
      const now = Date.now();
      if (warnedAt === undefined || now - warnedAt >= WARN_AGAIN_MS) {
        warnedAt = now;
        warn(
          `the NATS connection's broker token could not be renewed (${messageFor(error)}); it is tried again, and the connection reconnects once a token is live`
        );
      }
      retry();
      return;
    }
    if (credential.expiresAt().getTime() === before) {
      // The token client answered the token in hand: its trade failed in a way it keeps the
      // token through (a broker that is away, a busy one), so try again before it lapses.
      retry();
      return;
    }
    retries = 0;
    warnedAt = undefined;
    schedule(renewDelay(credential.expiresAt().getTime(), Date.now()));
  };
  connection.closed().then(stop, stop);
  schedule(renewDelay(credential.expiresAt().getTime(), Date.now()));
  return stop;
}
