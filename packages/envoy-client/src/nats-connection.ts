import type { ConnectionOptions } from "nats";
// Not part of nats.js's public surface; nats 2.29.x has it where its server list calls it. A
// move would fail the typecheck here, and the rows that dial real nats.js (nats-connection-log
// test) would fail on a change of what it returns.
import { hostPort } from "nats/lib/nats-base-client/servers";
import { LoggableError } from "./errors";
import { natsAuthOptions } from "./nats-auth";
import type { NatsCredential } from "./nats-credential";

/**
 * How often a session pings its NATS server, and how many pings may go unanswered. nats.js 2.29.3
 * sends one ping per tick and drops the connection on the tick that finds `maxPingOut` pings still
 * unanswered, so a path that freezes (packets stop, neither side closes) is left within
 * 3 x 10 s = 30 s and redialed at once. A redial that also hangs costs nats.js's 20 s dial timeout
 * and the 2 s reconnect wait: 30 + 20 + 2 = 52 s, under a minute. nats.js's default 120 s interval
 * left a frozen path for up to 6 minutes.
 */
export const NATS_PING_INTERVAL_MS = 10_000;
export const NATS_MAX_PINGS_OUT = 2;

/** The `connect` options every session plugin dials NATS with: its credential (`natsAuthOptions`:
 * a seed's nkey user, the session's broker token, or none), reconnect attempts without limit 2 s
 * apart, the ping check above, and, for the broker token alone, never giving up over a refused
 * credential. A seed or no credential that the server refuses twice in a row still closes the
 * connection for good, as below; a broker token presented to a server that sends no nonce fails
 * the CONNECT (`noAuthService`, `./nats-credential`).
 *
 * That last one matters on a hub whose connections an auth service admits
 * (packages/envoy/internal/broker/natscallout): while that service is away, which a restart of the
 * hub itself causes, or while the session's token lapses with the broker away, the server refuses
 * the session. nats.js 2.29.3 closes a connection for good on a repeated authorization error unless
 * `ignoreAuthErrorAbort` is set (`protocol.js:657-661`; the default is false, `options.js:50`), and
 * it clears `lastError` after a successful reconnect only for an expired authentication
 * (`protocol.js:397-399`), so a session refused once in any earlier outage would abort on its first
 * refusal in the next one. A seed or no credential keeps nats.js's default, as before the auth
 * service (R12, `__tests__/nats-connection-r12.test.ts`): no auth service decides those. */
export function natsConnectionOptions(input: {
  readonly servers: readonly string[];
  readonly name: string;
  readonly credential: NatsCredential;
}): ConnectionOptions {
  return {
    servers: [...input.servers],
    name: input.name,
    ...natsAuthOptions(input.credential),
    reconnect: true,
    maxReconnectAttempts: -1,
    reconnectTimeWait: 2_000,
    pingInterval: NATS_PING_INTERVAL_MS,
    maxPingOut: NATS_MAX_PINGS_OUT,
    ...(input.credential.kind === "broker" ? { ignoreAuthErrorAbort: true } : {}),
  };
}

/** What a log says in place of a NATS server address it will not name. */
export const UNNAMED_NATS_SERVER = "a server address that does not read as a host";

/** The port nats.js dials where a server names none (servers.js `hostPort`, `DEFAULT_PORT`). */
const NATS_DEFAULT_PORT = 4222;

/** One label of a host name a log names: letters, digits and inner hyphens, at most 32
 *  characters. A pasted nkey seed (58 characters) or a token's segment is a longer label. */
const HOST_LABEL = /^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$/i;
/** A label that is 16 or more hexadecimal characters alone: a token or key (a 128-bit hex token
 *  is 32), never a host name a log should print. */
const HEX_CREDENTIAL = /^[0-9a-f]{16,}$/i;

/**
 * The host and port nats.js dials for `server`, as nats.js itself finds them: its own `hostPort`
 * (servers.js), the function its server list calls for every entry. It takes what follows the
 * last `://`, takes the port from the first `:` followed by digits anywhere in it (a password's
 * included), and reads the rest as an http: URL, which ends the host at `\`, takes the last of a
 * comma list written in one entry, percent-decodes, writes a Unicode name in punycode and
 * lowercases. So the address a nats: URL parse names can differ from the one dialed. Undefined
 * where nats.js cannot read the server either, which it refuses before it dials.
 */
export function natsDialedAddress(server: string): { host: string; port: number } | undefined {
  try {
    const { hostname, port } = hostPort(server);
    return { host: hostname.toLowerCase(), port };
  } catch {
    return undefined;
  }
}

/** `server` parsed as written: a value written without a scheme is `nats://`. */
function parsedServer(server: string): URL | undefined {
  try {
    return new URL(server.includes("://") ? server : `nats://${server}`);
  } catch {
    return undefined;
  }
}

/**
 * Whether `hostname` (a URL's, so an IPv6 literal keeps its brackets, and a nats: URL keeps the
 * case it was written in) is one a log names: an IP literal, or a name written in one case whose
 * every label is a `HOST_LABEL` and no `HEX_CREDENTIAL`. A host name is written in one case, and a
 * pasted token, base64url or not, mixes them; a name the rule refuses is left unnamed, which costs
 * the log a name and never a secret.
 */
function readsAsHost(hostname: string): boolean {
  if (/^\[[0-9a-f:.]+\]$/i.test(hostname) || /^\d{1,3}(?:\.\d{1,3}){3}$/.test(hostname))
    return true;
  if (hostname !== hostname.toLowerCase() && hostname !== hostname.toUpperCase()) return false;
  return hostname
    .split(".")
    .every((label) => HOST_LABEL.test(label) && !HEX_CREDENTIAL.test(label));
}

/**
 * A NATS server as a log names it: scheme, host and port alone, and host and port alone for one
 * written without a scheme. A URL may carry a token or a user and password
 * (`nats://token@host:port`), and a value that does not read as a host may be a credential pasted
 * into the wrong setting (an nkey seed parses as a host name), so neither reaches a log: such an
 * address is `UNNAMED_NATS_SERVER`. So is one whose URL holds more than scheme, host and port,
 * and one where the host or the port nats.js dials (`natsDialedAddress`) is not the one written:
 * there the two reads part (a `\`, a comma list, a percent escape, a Unicode name, a password's
 * digits read as the port), and the dialed address is a credential's part.
 */
export function natsServerForLog(server: string): string {
  const parsed = parsedServer(server);
  if (parsed === undefined || parsed.host === "" || /\s/.test(server)) return UNNAMED_NATS_SERVER;
  if (!["", "/"].includes(parsed.pathname) || parsed.search !== "" || parsed.hash !== "")
    return UNNAMED_NATS_SERVER;
  if (!readsAsHost(parsed.hostname)) return UNNAMED_NATS_SERVER;
  const dialed = natsDialedAddress(server);
  const port = parsed.port === "" ? NATS_DEFAULT_PORT : Number(parsed.port);
  if (
    dialed === undefined ||
    dialed.host !== parsed.hostname.replace(/^\[|\]$/g, "").toLowerCase() ||
    dialed.port !== port
  )
    return UNNAMED_NATS_SERVER;
  return server.includes("://") ? `${parsed.protocol}//${parsed.host}` : parsed.host;
}

/** The codes nats.js gives an `-ERR` the server itself sent (core.js `ErrorCode`, protocol.js
 *  `toError`): its message is the server's own text, which names nothing the client was set with. */
const SERVER_ERROR_CODES: Record<string, true> = {
  AUTHORIZATION_VIOLATION: true,
  AUTHENTICATION_EXPIRED: true,
  AUTHENTICATION_TIMEOUT: true,
  ACCOUNT_EXPIRED: true,
  PERMISSIONS_VIOLATION: true,
  NATS_PROTOCOL_ERR: true,
};
/** A code or a syscall as a runtime or a service sets them: one word (`EAI_AGAIN`,
 *  `ERR_SOCKET_BAD_PORT`, Bun's `ConnectionRefused`), never text taken from a setting. */
const ERROR_CODE = /^[A-Za-z][A-Za-z0-9_]{0,63}$/;
const SYSCALL = /^[a-z]+$/;
const ERROR_NAME = /^[A-Za-z]+$/;

/**
 * Why a NATS call failed, as a log may print it. An allowlist: nothing nats.js or the runtime
 * derives from the configured servers reaches it, whatever their spelling. nats.js passes the
 * runtime's own errors on unchanged, and those quote what they were handed (a resolver's error
 * names the host it looked up, a socket's the port, a URL parser's the whole URL), so their text
 * is never printed, only their code:
 *  - a `LoggableError` (this package's or a caller's own message): its message;
 *  - nats.js's error for the server's own `-ERR` (`'Authorization Violation'`, `Permissions
 *    Violation for …`): its message, the server's text;
 *  - any other nats.js error: its code (`CONNECTION_REFUSED`, `TIMEOUT`, `TLS`, …);
 *  - a runtime error: its syscall and code (`getaddrinfo EAI_AGAIN`, `ERR_SOCKET_BAD_PORT`);
 *  - anything else: its type, `(message withheld)`.
 */
export function natsFailureReason(error: unknown): string {
  if (error instanceof LoggableError) return error.message;
  if (!(error instanceof Error)) return `a thrown ${typeof error} (message withheld)`;
  const { code, syscall } = error as { code?: unknown; syscall?: unknown };
  if (error.name === "NatsError" && typeof code === "string" && code !== "")
    return SERVER_ERROR_CODES[code] === true ? error.message : code;
  if (typeof code === "string" && ERROR_CODE.test(code))
    return typeof syscall === "string" && SYSCALL.test(syscall) ? `${syscall} ${code}` : code;
  return `${ERROR_NAME.test(error.name) ? error.name : "Error"} (message withheld)`;
}

/**
 * The line a failed dial is logged with: `could not connect to NATS at <named>[ presenting
 * <credential>]: <reason>`, `named` being the servers as a log names them (`natsServersForLog`,
 * or a caller's own name for a server it never names) and the reason `natsFailureReason`'s.
 */
export function natsDialFailure(named: string, error: unknown, presenting?: string): string {
  const credential = presenting === undefined ? "" : ` presenting ${presenting}`;
  return `could not connect to NATS at ${named}${credential}: ${natsFailureReason(error)}`;
}

/** `servers` as a log names them, one by one (`natsServerForLog`), joined by commas. */
export function natsServersForLog(servers: readonly string[]): string {
  return servers.map(natsServerForLog).join(", ");
}
