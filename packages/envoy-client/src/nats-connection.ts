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

/** `server` parsed as nats.js dials it: a value written without a scheme is `nats://`. */
function parsedServer(server: string): URL | undefined {
  try {
    return new URL(server.includes("://") ? server : `nats://${server}`);
  } catch {
    return undefined;
  }
}

/** Whether `hostname` (a URL's, so an IPv6 literal keeps its brackets) reads as a host: an IP
 *  literal, or a name whose every label is a `HOST_LABEL`. */
function readsAsHost(hostname: string): boolean {
  if (/^\[[0-9a-f:.]+\]$/i.test(hostname) || /^\d{1,3}(?:\.\d{1,3}){3}$/.test(hostname))
    return true;
  return hostname.split(".").every((label) => HOST_LABEL.test(label));
}

/**
 * A NATS server as a log names it: scheme, host and port alone, and host and port alone for one
 * written without a scheme. A URL may carry a token or a user and password
 * (`nats://token@host:port`), and a value that does not read as a host may be a credential pasted
 * into the wrong setting (an nkey seed parses as a host name), so neither reaches a log: such an
 * address is `UNNAMED_NATS_SERVER`.
 */
export function natsServerForLog(server: string): string {
  const parsed = parsedServer(server);
  if (parsed === undefined || parsed.host === "" || /\s/.test(server)) return UNNAMED_NATS_SERVER;
  if (!readsAsHost(parsed.hostname)) return UNNAMED_NATS_SERVER;
  return server.includes("://") ? `${parsed.protocol}//${parsed.host}` : parsed.host;
}

/** A server written as `[scheme://][userinfo@]host…`, read without parsing it as a URL. */
const WRITTEN_SERVER = /^(?:[a-z][a-z0-9+.-]*:\/\/)?(?:([^/?#@]*)@)?([^/?#:]*)/i;

/** The user, password and host `server` holds: its URL's, or, for one nats.js cannot parse
 *  either (a port out of range), the user info and host as written, which its error quotes. */
function serverParts(server: string): { secrets: string[]; hostname: string } {
  const parsed = parsedServer(server);
  if (parsed !== undefined)
    return { secrets: [parsed.username, parsed.password], hostname: parsed.hostname };
  const [, userInfo = "", hostname = ""] = WRITTEN_SERVER.exec(server.trim()) ?? [];
  const colon = userInfo.indexOf(":");
  const secrets =
    colon < 0 ? [userInfo] : [userInfo, userInfo.slice(0, colon), userInfo.slice(colon + 1)];
  return { secrets, hostname };
}

/**
 * `text` (a NATS connect error's, which nats.js's resolver errors fill with the host they looked
 * up, lowercased) with every address of `servers` a log does not name (`natsServerForLog`), as
 * written and as its host name, and every user, password or token a URL of them holds, replaced
 * by `NATS_ADDRESS_NOT_SHOWN` whatever its case. One pass over `text` as nats.js wrote it: a
 * stand-in is never searched again, so a part that is a word of the stand-in (a user `a`) does
 * not grow the text, however many servers repeat it.
 */
export function natsErrorForLog(text: string, servers: readonly string[]): string {
  // Keyed lowercased: the search ignores case, so one spelling of a part is enough.
  const parts = new Map<string, string>();
  const add = (part: string): void => {
    if (part.trim() !== "") parts.set(part.toLowerCase(), part);
  };
  for (const server of servers) {
    const { secrets, hostname } = serverParts(server);
    for (const secret of secrets) {
      add(secret);
      try {
        add(decodeURIComponent(secret));
      } catch {
        // A malformed escape: the part as written is what the text can carry.
      }
    }
    if (natsServerForLog(server) !== UNNAMED_NATS_SERVER) continue;
    add(server);
    add(hostname);
  }
  if (parts.size === 0) return text;
  // Longest first: at any one place the longest part that matches is the one replaced, so a part
  // inside another leaves nothing of the longer behind.
  const alternatives = [...parts.values()]
    .sort((a, b) => b.length - a.length)
    .map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  return text.replace(new RegExp(alternatives.join("|"), "gi"), NATS_ADDRESS_NOT_SHOWN);
}
