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
import { SIGNIN_COMMAND } from "@legion/broker-client/signins";
import {
  BrokerRefusedError,
  BrokerTradeFailedError,
  RegistrationPendingError,
  SignInEndedError,
} from "@legion/broker-client/token-client";
import { scrub } from "@legion/contracts/held-secrets";
// Types alone: `envoySettings` imports this module, and every bundle of it — the `dispatch`
// command's, each hook's — would otherwise carry nats.js whole.
import type { Auth, Events, NatsConnection, Status } from "nats";
import {
  type BrokerSessionName,
  brokerTokenFor,
  sessionName,
  type TradedToken,
} from "./broker-credential";
import type { NatsSeed } from "./envoy-settings";
import { LoggableError, natsFailureReason } from "./errors";

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
 * where the session's listener and Dispatch calls present a broker token too, it is this one
 * (`brokerTokenFor`, `brokerCredential`), so a session holds one token, not two; a bearer those
 * calls are given instead (`ENVOY_TOKEN`, a file's `token`) is theirs alone.
 */
export interface BrokerNatsCredential {
  /**
   * Takes the token to present, trading for one where the token in hand is not live: a connection
   * is dialled only once this has answered, since the CONNECT is signed at once. A broker that
   * gives no answer within the trade's deadline fails it. A pod session the broker holds no
   * registration for yet is no failure: the trade is retried for as long as the session runs
   * (#4153), so where `life` is given, the wait goes on until the registration lands or `life`
   * ends, the token client saying so once and every 30 minutes. A caller that cannot wait that
   * long gives no `life`, and is told at the deadline that the registration is pending, by session.
   */
  ready(life?: AbortSignal): Promise<void>;
  /**
   * The CONNECT's credential for the server's `nonce`: the token in hand as `auth_token`, and as
   * `sig` the proof of its key over that nonce, base64url-encoded, as the auth service reads a
   * signature. One snapshot of the token per CONNECT, so the proof always names the token sent
   * beside it. It presents nothing — and the server refuses the connection — where it holds no
   * live token, and then starts a trade, so the next reconnect presents a live token where the
   * broker answers one; that case never throws, since nats.js closes a connection for good when
   * its authenticator throws. A server that sent no nonce is the one case that does throw
   * ({@link noAuthService}): nats-server sends one to every client where an auth service runs,
   * so such a hub has nothing that checks a broker token, and a CONNECT with no credential would
   * be admitted there while the session believed it presented its own.
   */
  authenticator(nonce?: string): Auth;
  /** When the token in hand lapses; the epoch before any is taken. */
  expiresAt(): Date;
  /** Takes the session's token again: a renewed one inside the renewal window, and the token of
   *  the session the name now reads, after a switch, even one made while a renewal was in flight.
   *  It waits no longer than `signal`: the token client waits out a pod session's missing
   *  registration for as long as its caller does, so every caller in this package names its
   *  deadline, and a switch to another session ends the waits for the one before. */
  renew(signal?: AbortSignal): Promise<void>;
  /** Whether the last CONNECT presented a token other than the one in hand, so the connection
   *  should reconnect to present it. */
  needsReconnect(): boolean;
}

/** Why a CONNECT presenting the broker token fails at a hub that sent no nonce: it runs no auth
 *  service (a hub that does sends every client one), and the remedy, which undoes the setting
 *  `source` names: `NATS_AUTH`, or a file's `envoy.natsAuth in <file>`. */
export function noAuthService(source: string): string {
  const remedy =
    source === NATS_AUTH_VAR ? `unset ${NATS_AUTH_VAR}` : "take natsAuth out of that file";
  return `${source} is broker, but the NATS server sent no nonce to sign: it runs no auth service, so ${remedy} for this hub`;
}

/** How long {@link BrokerNatsCredential.ready} waits for a trade: nats.js's own dial timeout, so a
 *  secrets broker that never answers fails a start as a hub that never answers does, named,
 *  rather than holding it in silence. */
export const TRADE_DEADLINE_MS = 20_000;

/** Why `ready` failed: the broker has not answered the trade the setting `source` chose. */
function tradeUnanswered(source: string, deadlineMs: number): string {
  return `${source} is broker, but the secrets broker did not answer the trade for the session's token within ${deadlineMs / 1000} s`;
}

/**
 * Why a trade for the session's token failed, as a log may print it. A refusal is the broker's
 * HTTP status, OAuth code and what it said where that is one of its known refusals, else "(its
 * text is withheld)" (`BrokerRefusedError.said`: a peer at AGENT_SECRETS_URL can put the subject
 * token it was sent in its text); a pod session the broker holds no registration for yet is
 * broker-client's own line, naming the session and the refusal as it prints
 * (`RegistrationPendingError`); a trade that got no answer, or an answer that is not the
 * grant's, is broker-client's own text, which names the broker as `urlForText` does and the
 * failure by its class (`BrokerTradeFailedError`); an ended sign-in is its remedy; and any other
 * failure goes by `natsFailureReason`'s allowlist, since its text is the runtime's or the
 * transport's, which quotes what it was handed.
 */
export function tradeFailureReason(error: unknown): string {
  if (error instanceof BrokerRefusedError) {
    const code = error.shownCode === "" ? "" : ` ${error.shownCode}`;
    const said = error.said === "" ? "" : `: ${scrub(error.said)}`;
    return `the secrets broker refused the identity traded: HTTP ${error.status}${code}${said}`;
  }
  if (error instanceof RegistrationPendingError) return scrub(error.message);
  if (error instanceof SessionMovedError) return error.message;
  if (error instanceof BrokerTradeFailedError) return scrub(error.message);
  if (error instanceof SignInEndedError)
    return `the issuer no longer honours the sign-in on this machine; run: ${SIGNIN_COMMAND}`;
  return natsFailureReason(error);
}

/** Whether `error` is the reason an `AbortSignal.timeout` ends with: the caller's deadline. */
function isDeadline(error: unknown): boolean {
  return error instanceof DOMException && error.name === "TimeoutError";
}

/**
 * `promise`, or `signal`'s reason once it ends, so a caller waits no longer than its own deadline
 * even for a trade another caller started. The reason comes one turn after the end: the token
 * client ends a wait for a trade this caller started in its own words at the same moment
 * (`RegistrationPendingError`, naming the session), and those are what the caller is told.
 */
function within<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const stop = () => {
      setTimeout(() => reject(signal.reason), 0);
    };
    if (signal.aborted) stop();
    else signal.addEventListener("abort", stop, { once: true });
    promise.then(
      (value) => {
        signal.removeEventListener("abort", stop);
        resolve(value);
      },
      (error: unknown) => {
        signal.removeEventListener("abort", stop);
        reject(error);
      }
    );
  });
}

/**
 * A wait for one session's token that a switch to another session ended: the host has moved on,
 * and the renewal for the session it is on now is the one that counts. The trade itself goes on
 * in the token client, for that session's next caller.
 */
export class SessionMovedError extends LoggableError {
  constructor(from: string, to: string) {
    super(`the host moved from the session ${from} to ${to} while ${from}'s token was traded`);
    this.name = "SessionMovedError";
  }
}

/**
 * The broker token of `env`'s machine, or of a person signed in on it, for the session `session`
 * names, as a NATS connection presents it, chosen by the setting `source` names (`NATS_AUTH` by
 * default, or a file's `envoy.natsAuth in <file>`), which a refusal at a hub with no auth service
 * names. Nothing is traded until {@link BrokerNatsCredential.ready} or
 * {@link BrokerNatsCredential.renew}; `ready` waits `tradeDeadlineMs` for it.
 *
 * Every wait for a trade ends at its caller's deadline: the token client waits out a pod
 * session's missing registration for as long as its caller waits (`BrokerTokenClient.token`), so a
 * wait with none would last as long as the registration is missing. Each caller trades through
 * the token client, which makes one trade per session and lets each caller stop waiting for it on
 * its own signal, so no caller waits behind another caller's trade, and one for another session
 * least of all. A renewal for a session other than the last renewal's is a switch, and it ends
 * every wait for the session before ({@link SessionMovedError}). A token is held only where it
 * was traded for the session the name reads when it lands, so a trade for the session before a
 * switch never takes the place of the new session's token.
 */
export function brokerNatsCredential(
  env: BrokerEnvironment,
  session: BrokerSessionName | undefined,
  source: string = NATS_AUTH_VAR,
  tradeDeadlineMs: number = TRADE_DEADLINE_MS
): BrokerNatsCredential {
  const take = brokerTokenFor(env, session);
  let held: TradedToken | undefined;
  let presented: string | undefined;
  // The session the last renewal was for, and what ends every wait for it at a switch.
  let waitingFor: string | undefined;
  let switched = new AbortController();
  const live = (): boolean => held !== undefined && held.token.expiresAt.getTime() > Date.now();
  const renew = (signal?: AbortSignal): Promise<void> => {
    const name = sessionName(session);
    if (waitingFor !== undefined && name !== waitingFor) {
      switched.abort(new SessionMovedError(waitingFor, name));
      switched = new AbortController();
    }
    waitingFor = name;
    const moved = switched.signal;
    const limit = signal === undefined ? moved : AbortSignal.any([signal, moved]);
    return within(take(limit), limit).then(
      (taken) => {
        if (taken.session === sessionName(session)) held = taken;
      },
      (error: unknown) => {
        // A switch ended this wait: the token client's words for it name the session before,
        // which is not what the caller needs to know.
        if (moved.aborted && signal?.aborted !== true) throw moved.reason;
        throw error;
      }
    );
  };
  return {
    // The trade itself goes on past the deadline, for the keeper and the next `ready`: only this
    // wait for it ends. Its failure is this package's own line, naming the setting that chose the
    // broker token and why as a log may print it (`tradeFailureReason`).
    ready: async (life?: AbortSignal) => {
      if (live()) return;
      let pending: RegistrationPendingError;
      try {
        await renew(AbortSignal.timeout(tradeDeadlineMs));
        return;
      } catch (error) {
        if (isDeadline(error)) throw new LoggableError(tradeUnanswered(source, tradeDeadlineMs));
        if (!(error instanceof RegistrationPendingError)) {
          throw new LoggableError(
            `${source} is broker, but the trade for the session's token failed: ${tradeFailureReason(error)}`
          );
        }
        pending = error;
      }
      if (life === undefined) {
        throw new LoggableError(
          `${source} is broker, and the session's token waits on its registration: ${tradeFailureReason(pending)}`
        );
      }
      try {
        await renew(life);
      } catch (error) {
        if (life.aborted) throw life.reason;
        throw new LoggableError(
          `${source} is broker, but the trade for the session's token failed: ${tradeFailureReason(error)}`
        );
      }
    },
    renew,
    authenticator(nonce?: string): Auth {
      if (nonce === undefined || nonce === "") throw new LoggableError(noAuthService(source));
      const snapshot = held;
      if (snapshot === undefined || !live()) {
        // Nothing live to present: the server refuses this CONNECT, and the trade started here
        // lands a token for the next one, within `ready`'s deadline. Its failure is the keeper's
        // to report.
        renew(AbortSignal.timeout(tradeDeadlineMs)).catch(() => {});
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
/** The least the keeper waits between two renewals, where the token in hand lives long enough for
 *  it: the wait is never longer than half the token's remaining life. */
const RENEW_FLOOR_MS = 1_000;
/** The keeper's retry after a renewal that landed no new token: doubling from the first to the
 *  last. */
const RETRY_FIRST_MS = 2_000;
const RETRY_LAST_MS = 60_000;
/** How often the keeper repeats its warning while renewals keep failing. */
const WARN_AGAIN_MS = 30 * 60_000;

/** How long after `now` the keeper renews a token that lapses at `expiry`: the token client's
 *  window ahead of it, or halfway there for a token that lives less than twice that window; at
 *  least `RENEW_FLOOR_MS`, unless that is past half the token's remaining life (the broker allows
 *  one-second tokens), and at once for a token already lapsed. */
function renewDelay(expiry: number, now: number): number {
  const remaining = expiry - now;
  if (remaining <= 0) return 0;
  const ahead = remaining - Math.min(RENEW_AHEAD_MS, remaining / 2);
  return Math.max(Math.min(RENEW_FLOOR_MS, remaining / 2), ahead);
}

/**
 * Holds `connection` to a token the auth service still admits: ahead of each token's expiry it
 * renews `credential` and, when that lands a new token, reconnects, so the CONNECT presents the
 * new token before the server drops the user the old one was admitted as. A renewal that fails, or
 * lands no new token (the token client keeps the token in hand through a transient exchange
 * failure), is retried at 2 s doubling to a minute, and `warn` is told once, naming why, and again
 * every 30 minutes while it lasts; the token is never named. The connection keeps reconnecting on
 * its own meanwhile (`ignoreAuthErrorAbort`), and the authenticator starts a trade of its own at
 * each CONNECT it cannot sign, so the first live token admits it again.
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
  /** Tells `warn` of `why`, once, and again every 30 minutes while renewals keep failing. */
  const warnOnce = (why: string) => {
    const now = Date.now();
    if (warnedAt !== undefined && now - warnedAt < WARN_AGAIN_MS) return;
    warnedAt = now;
    warn(
      `the NATS connection's broker token ${why}; it is tried again, and the connection reconnects once a token is live`
    );
  };
  const tick = async () => {
    timer = undefined;
    if (stopped || connection.isClosed()) return;
    const before = credential.expiresAt().getTime();
    try {
      // Bounded as `ready` is: the token client waits out a missing registration for as long as
      // its caller does, and the keeper's next tick is its own retry.
      await credential.renew(AbortSignal.timeout(TRADE_DEADLINE_MS));
      if (stopped) return;
      if (credential.needsReconnect()) await connection.reconnect();
    } catch (error) {
      // The host moved to another session meanwhile: that session's renewal is the one that
      // counts, so the keeper renews again at once, for it, and has nothing to warn of.
      if (error instanceof SessionMovedError) {
        schedule(0);
        return;
      }
      warnOnce(`could not be renewed (${tradeFailureReason(error)})`);
      retry();
      return;
    }
    if (credential.expiresAt().getTime() === before) {
      // The token client answered the token in hand: its trade failed in a way it keeps the
      // token through (a broker that is away, a busy one), so try again before it lapses.
      warnOnce(
        `renewal landed no new token, so the one in hand lapses at ${new Date(before).toISOString()}`
      );
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

/** How long a session switch waits for its connection to come back as the new session. */
const FOLLOW_TIMEOUT_MS = 15_000;

/** nats.js's status of a connection that is back (`Events.Reconnect`), written out so this module
 *  imports nats.js's types alone; the type holds it to the enum's own value. */
const RECONNECTED: `${Events.Reconnect}` = "reconnect";

/**
 * Moves `connection` to the token of the session `credential`'s name reads now — the session a
 * host switched to — and returns once the connection has come back admitted with it. A caller
 * subscribes the new session's subjects only after this returns: until then the connection is
 * still the user the old session's token was admitted as, the auth service granted that user none
 * of the new session's subjects, and the server refuses a subscribe to one, which nats.js answers
 * by closing the subscription. Nothing happens where the token in hand is the one the connection
 * presented. A reconnect counts only once the CONNECT it made presented the token in hand, so one
 * that began before the move does not end it; and since nats.js's `reconnect()` does nothing while
 * the connection is redialling, a redial that comes back with the old token is followed by another
 * reconnect, which presents the new one. Throws where the new session's token or the connection
 * does not come back within one 15 s deadline, which the renewal is given as its signal, or where
 * the connection closes first. The status reader it opens is stopped on every way out: nats.js
 * keeps a reader registered until it is stopped, whatever becomes of the connection.
 */
export async function followNatsCredential(
  connection: Pick<NatsConnection, "reconnect" | "status" | "closed">,
  credential: BrokerNatsCredential
): Promise<void> {
  const deadline = AbortSignal.timeout(FOLLOW_TIMEOUT_MS);
  try {
    await credential.renew(deadline);
  } catch (error) {
    throw new LoggableError(
      isDeadline(error)
        ? `the NATS connection could not take the new session's token within ${FOLLOW_TIMEOUT_MS / 1000} s`
        : `the NATS connection could not take the new session's token: ${tradeFailureReason(error)}`
    );
  }
  if (!credential.needsReconnect()) return;
  // nats.js's status() is a QueuedIterator, which has a stop() its declared type leaves out.
  const statuses: AsyncIterable<Status> & { stop?: () => void } = connection.status();
  const back = (async () => {
    for await (const status of statuses) {
      if (status.type !== RECONNECTED) continue;
      if (!credential.needsReconnect()) return;
      // Back as the old session: a redial that was in flight when the move asked for one.
      connection.reconnect().catch(() => {});
    }
    throw new LoggableError(
      "the NATS connection's status ended before it came back as the new session"
    );
  })();
  const closed = connection.closed().then(() => {
    throw new LoggableError("the NATS connection closed before it came back as the new session");
  });
  const late = Promise.withResolvers<never>();
  const expire = () =>
    late.reject(
      new LoggableError(
        `the NATS connection did not come back as the new session within ${FOLLOW_TIMEOUT_MS / 1000} s`
      )
    );
  if (deadline.aborted) expire();
  else deadline.addEventListener("abort", expire, { once: true });
  try {
    await connection.reconnect();
    await Promise.race([back, closed, late.promise]);
  } finally {
    deadline.removeEventListener("abort", expire);
    statuses.stop?.();
    back.catch(() => {});
    closed.catch(() => {});
    late.promise.catch(() => {});
  }
}
