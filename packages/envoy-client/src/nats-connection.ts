import type { ConnectionOptions } from "nats";
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

/** What stands in, in nats.js's own error text, for a server address a log will not name. */
export const NATS_ADDRESS_NOT_SHOWN = "<a server address not shown>";

/** One label of a host name a log names: letters, digits and inner hyphens, at most 32
 *  characters. A pasted nkey seed (58 characters) or a token's segment is a longer label. */
const HOST_LABEL = /^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$/i;
/** A label that is 16 or more hexadecimal characters alone: a token or key (a 128-bit hex token
 *  is 32), never a host name a log should print. */
const HEX_CREDENTIAL = /^[0-9a-f]{16,}$/i;

/** `server` parsed as nats.js dials it: a value written without a scheme is `nats://`. */
function parsedServer(server: string): URL | undefined {
  try {
    return new URL(server.includes("://") ? server : `nats://${server}`);
  } catch {
    return undefined;
  }
}

/** Whether `hostname` (a URL's, so an IPv6 literal keeps its brackets) reads as a host: an IP
 *  literal, or a name whose every label is a `HOST_LABEL` and no `HEX_CREDENTIAL`. */
function readsAsHost(hostname: string): boolean {
  if (/^\[[0-9a-f:.]+\]$/i.test(hostname) || /^\d{1,3}(?:\.\d{1,3}){3}$/.test(hostname))
    return true;
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
 * which is all nats.js dials: a token or password holding an unencoded `/`, `?` or `#` ends the
 * authority there, and the host is then the credential's first segment.
 */
export function natsServerForLog(server: string): string {
  const parsed = parsedServer(server);
  if (parsed === undefined || parsed.host === "" || /\s/.test(server)) return UNNAMED_NATS_SERVER;
  if (!["", "/"].includes(parsed.pathname) || parsed.search !== "" || parsed.hash !== "")
    return UNNAMED_NATS_SERVER;
  if (!readsAsHost(parsed.hostname)) return UNNAMED_NATS_SERVER;
  return server.includes("://") ? `${parsed.protocol}//${parsed.host}` : parsed.host;
}

/**
 * The user info `server` is written with: everything between its scheme and its last `@`,
 * whatever its characters, since a credential may hold `/`, `?` or `#` that ends a URL's
 * authority early; and the host after it as written.
 */
function writtenParts(server: string): { userInfo: string; hostname: string } {
  const rest = server.trim().replace(/^[a-z][a-z0-9+.-]*:\/\//i, "");
  const at = rest.lastIndexOf("@");
  const hostname = /^[^/?#:]*/.exec(at < 0 ? rest : rest.slice(at + 1))?.[0] ?? "";
  return { userInfo: at < 0 ? "" : rest.slice(0, at), hostname };
}

/** The user, password and host `server` holds: its user info as written (whole, and split at
 *  its first `:`), the URL's user and password where it parses, and the host nats.js dials, or
 *  the host as written for a server it cannot parse (a port out of range). */
function serverParts(server: string): { secrets: string[]; hostname: string } {
  const parsed = parsedServer(server);
  const { userInfo, hostname } = writtenParts(server);
  const colon = userInfo.indexOf(":");
  const secrets =
    colon < 0 ? [userInfo] : [userInfo, userInfo.slice(0, colon), userInfo.slice(colon + 1)];
  if (parsed !== undefined) secrets.push(parsed.username, parsed.password);
  return { secrets, hostname: parsed?.hostname ?? hostname };
}

/**
 * Every part of `servers` a log does not name: each address `natsServerForLog` does not name, as
 * written and as its host name, and every user, password or token a URL of them holds, as written
 * and decoded.
 */
export function natsUnnamedParts(servers: readonly string[]): string[] {
  const parts: string[] = [];
  for (const server of servers) {
    const { secrets, hostname } = serverParts(server);
    for (const secret of secrets) {
      parts.push(secret);
      try {
        parts.push(decodeURIComponent(secret));
      } catch {
        // A malformed escape: the part as written is what the text can carry.
      }
    }
    if (natsServerForLog(server) !== UNNAMED_NATS_SERVER) continue;
    parts.push(server, hostname);
  }
  return parts;
}

/**
 * `text` with each part of `replacements` replaced by its stand-in, whatever its case, in one pass
 * over `text` as written: a stand-in is never searched again, so a part that is a word of one (a
 * user `a`) does not grow the text, and no replacement can break a longer part's match for
 * another. Longest first: at any one place the longest part that matches is the one replaced, so a
 * part inside another leaves nothing of the longer behind. Each part is searched as written, with
 * Unicode case folding (a part whose lowercasing is not its fold, U+0130 or U+212A, is still
 * found); its lowercasing only chooses the stand-in, and of two entries for one part (whatever
 * its case) the later's stand-in is used.
 */
export function replaceEachOnce(
  text: string,
  replacements: Iterable<readonly [part: string, standIn: string]>
): string {
  const spellings = new Set<string>();
  const standIns = new Map<string, string>();
  for (const [part, standIn] of replacements) {
    if (part.trim() === "") continue;
    spellings.add(part);
    standIns.set(part.toLowerCase(), standIn);
  }
  if (spellings.size === 0) return text;
  const alternatives = [...spellings]
    .sort((a, b) => b.length - a.length)
    .map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  // A match whose lowercasing is no part's (a case the fold reaches and toLowerCase does not)
  // still gets a stand-in: the first, which hides it as well as its own would.
  const fallback = standIns.values().next().value as string;
  return text.replace(
    new RegExp(alternatives.join("|"), "giu"),
    (match) => standIns.get(match.toLowerCase()) ?? fallback
  );
}

/**
 * `text` (a NATS connect error's, which nats.js's resolver errors fill with the host they looked
 * up, lowercased) with every part of `servers` a log does not name (`natsUnnamedParts`) replaced
 * by `NATS_ADDRESS_NOT_SHOWN`, in one pass (`replaceEachOnce`).
 */
export function natsErrorForLog(text: string, servers: readonly string[]): string {
  return replaceEachOnce(
    text,
    natsUnnamedParts(servers).map((part) => [part, NATS_ADDRESS_NOT_SHOWN] as const)
  );
}
