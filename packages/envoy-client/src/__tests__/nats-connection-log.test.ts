import { describe, expect, test } from "bun:test";
import { randomBytes } from "node:crypto";
import { connect, nkeys } from "nats";
import { messageFor } from "../errors";
import {
  NATS_ADDRESS_NOT_SHOWN,
  natsDialedHost,
  natsErrorForLog,
  natsServerForLog,
  replaceEachOnce,
  UNNAMED_NATS_SERVER,
} from "../nats-connection";

// What a log keeps of nats.js's own connect error (natsErrorForLog): every part a log does not name
// is replaced once, in the text as nats.js wrote it, and never in what an earlier replacement put
// there.
//
// Each URL's user info is joined here, so the source holds no credential-bearing URL for the push
// leak check to find.
const userinfo = (...parts: string[]) => `${parts.join(":")}@`;

describe("natsErrorForLog", () => {
  test("a credential repeated across servers is replaced once, and not inside its own stand-in", () => {
    // `a` is a letter of the stand-in itself: replaced pass by pass, each pass grew the text
    // again (82 characters for one server, 110,566 for six).
    const servers = Array.from({ length: 6 }, (_, at) => `nats://a:pw@host-${at}.example:4222`);
    expect(natsErrorForLog("a", servers)).toBe(NATS_ADDRESS_NOT_SHOWN);
  });

  test("no part is looked for in another part's stand-in", () => {
    // `shown` is a word of the stand-in that replaces `not`.
    const server = `nats://${userinfo("shown", "not")}host.example:4222`;
    expect(natsErrorForLog("refused: not", [server])).toBe(`refused: ${NATS_ADDRESS_NOT_SHOWN}`);
  });

  test("a part that holds another is replaced whole, whatever its case", () => {
    const server = `nats://${userinfo("abc", "abcdef")}host.example:4222`;
    expect(natsErrorForLog("user ABCDEF refused", [server])).toBe(
      `user ${NATS_ADDRESS_NOT_SHOWN} refused`
    );
  });

  test("a text with nothing to hide is kept as written", () => {
    expect(natsErrorForLog("CONNECTION_REFUSED", ["nats://127.0.0.1:4222"])).toBe(
      "CONNECTION_REFUSED"
    );
  });

  test("a server that does not parse leaves neither its credential nor its host in the text", () => {
    // A port out of range. nats.js's message for it is `Invalid URL`, and Bun keeps the address in
    // `error.input`, which only an uncaught error prints; were it ever quoted, nothing of it shows.
    const server = `nats://${userinfo("user", "canary-pw")}canary-host:99999`;
    const quoted = `"http://${userinfo("user", "canary-pw")}canary-host:99999" cannot be parsed as a URL`;
    expect(natsErrorForLog(quoted, [server]).toLowerCase()).not.toContain("canary");
  });

  test("a part written with a character whose lowercasing is not its case fold is still replaced", () => {
    // U+0130 lowercases to two characters and U+212A (the Kelvin sign) to `k`: a search built from
    // the lowercased part found neither as written.
    expect(replaceEachOnce("refused p\u0130ss here", [["p\u0130ss", "<X>"]])).toBe(
      "refused <X> here"
    );
    expect(replaceEachOnce("refused \u212Aey here", [["\u212Aey", "<X>"]])).toBe(
      "refused <X> here"
    );
  });
});

// A token or password may hold `/`, `?` or `#` unencoded (`openssl rand -base64 32` gives a `/` in
// about half its outputs): the URL's authority then ends there, its host is the credential's first
// segment, and nats.js dials that and names it in its resolver's error.
describe.each(["/", "?", "#"])("a credential holding an unencoded %s", (character) => {
  const token = `canarytok${character}def+gh==`;
  const password = `12${character}canarypw`;
  test("a token is named in no failure line", () => {
    const server = `nats://${userinfo(token)}127.0.0.1:4222`;
    expect(natsServerForLog(server)).toBe(UNNAMED_NATS_SERVER);
    expect(
      natsErrorForLog("getaddrinfo EAI_AGAIN canarytok", [server]).toLowerCase()
    ).not.toContain("canary");
  });
  test("a password is named in no failure line", () => {
    const server = `nats://${userinfo("canaryuser", password)}127.0.0.1:4222`;
    expect(natsServerForLog(server)).toBe(UNNAMED_NATS_SERVER);
    expect(
      natsErrorForLog("getaddrinfo EAI_AGAIN canaryuser", [server]).toLowerCase()
    ).not.toContain("canary");
  });
});

describe("natsServerForLog", () => {
  test("a label of 16 or more hexadecimal characters is read as a credential, not a host", () => {
    expect(natsServerForLog("nats://0123456789abcdef0123456789abcdef:4222")).toBe(
      UNNAMED_NATS_SERVER
    );
    expect(natsServerForLog("0123456789abcdef:4222")).toBe(UNNAMED_NATS_SERVER);
    // The control: a short hexadecimal label is a host name.
    expect(natsServerForLog("nats://deadbeef.example:4222")).toBe("nats://deadbeef.example:4222");
  });
});

/** What real nats.js says when it fails to connect to `server`: no server runs, so each of these
 *  fails at the resolver or the socket, and the text is nats.js's own. */
async function realFailure(server: string): Promise<string> {
  const outcome = await connect({ servers: [server], reconnect: false, timeout: 5_000 }).then(
    async (connection) => {
      await connection.close();
      return "connected";
    },
    (error: unknown) => messageFor(error)
  );
  if (outcome === "connected")
    throw new Error(`${server} connected, which this row needs it not to`);
  return outcome;
}

/** The failure line every caller writes: the server as a log names it, and nats.js's text. */
const failureLine = (server: string, text: string) =>
  `could not connect to NATS at ${natsServerForLog(server)}: ${natsErrorForLog(text, [server])}`;

const canary = () => `canary${randomBytes(4).toString("hex")}`;

// nats.js dials what follows the last `://` as an http: URL (servers.js `hostPort`), which ends the
// host at `\`, reads a comma list in one entry as one server, percent-decodes and writes a Unicode
// name in punycode. A nats: URL does none of these, so a log that parsed one named, or failed to
// hide, the host nats.js dialed: a credential's first part, or a whole seed. Each row runs real
// nats.js, holds that its own text carries the secret (the row can fail for the leak), and that
// the failure line carries none of it.
describe("a server nats.js dials apart from how it is written", () => {
  const seed = new TextDecoder().decode(nkeys.createUser().getSeed());
  const tokens = { backslash: canary(), comma: canary(), percent: canary(), unicode: canary() };
  const unicode = `nats://Grüße${tokens.unicode}Secret:4222`;
  const rows: [label: string, server: string, secrets: readonly string[]][] = [
    [
      "a backslash in the user info",
      `nats://${userinfo(`${tokens.backslash}\\en`)}127.0.0.1:4222`,
      [tokens.backslash],
    ],
    [
      "a comma list in one entry, a token in the second",
      `nats://a.example:4222,nats://${userinfo(`${tokens.comma}/x`)}127.0.0.1:4222`,
      [tokens.comma],
    ],
    [
      "a comma list in one entry, a seed in the second",
      `nats://a.example:4222,nats://${seed}:4222`,
      [seed],
    ],
    [
      "a percent escape in the user info",
      `nats://${userinfo(`${tokens.percent}%41/x`)}127.0.0.1:4222`,
      [tokens.percent],
    ],
    [
      "a Unicode name",
      unicode,
      [`grüße${tokens.unicode}secret`, new URL(`http://Grüße${tokens.unicode}Secret`).hostname],
    ],
  ];
  test.each(rows)(
    "%s: the failure line carries none of it",
    async (_, server, secrets) => {
      const text = await realFailure(server);
      const dialed = natsDialedHost(server) ?? "";
      expect(secrets.some((secret) => dialed.includes(secret.toLowerCase()))).toBe(true);
      expect(secrets.some((secret) => text.toLowerCase().includes(secret.toLowerCase()))).toBe(
        true
      );
      expect(natsServerForLog(server)).toBe(UNNAMED_NATS_SERVER);
      const line = failureLine(server, text).toLowerCase();
      for (const secret of secrets) expect(line).not.toContain(secret.toLowerCase());
    },
    30_000
  );
});

describe("a token pasted as the server", () => {
  test("a mixed-case alphanumeric label of 16 or more characters is named in no failure line", async () => {
    const token = `Xk3pQ9vT2mL8wR5n${randomBytes(3).toString("hex")}`;
    const server = `nats://${token}:4222`;
    const text = await realFailure(server);
    expect(text.toLowerCase()).toContain(token.toLowerCase());
    expect(natsServerForLog(server)).toBe(UNNAMED_NATS_SERVER);
    expect(failureLine(server, text).toLowerCase()).not.toContain(token.toLowerCase());
  }, 30_000);

  test("a host name written in one case is still named", () => {
    expect(natsServerForLog("nats://abcdefghij0123456789.example:4222")).toBe(
      "nats://abcdefghij0123456789.example:4222"
    );
    expect(natsServerForLog("NATS-HUB-0123456789AB.EXAMPLE:4222")).toBe(
      "NATS-HUB-0123456789AB.EXAMPLE:4222"
    );
  });
});

describe("natsDialedHost", () => {
  test("is nats.js's own host: the servers a log names keep their names", () => {
    expect(natsDialedHost(`nats://${userinfo("u", "p")}Host.Example:4222`)).toBe("host.example");
    expect(natsServerForLog(`nats://${userinfo("u", "p")}Host.Example:4222`)).toBe(
      "nats://Host.Example:4222"
    );
    expect(natsServerForLog("127.0.0.1:4223")).toBe("127.0.0.1:4223");
    expect(natsServerForLog("nats://[::1]:4222")).toBe("nats://[::1]:4222");
    expect(natsServerForLog("tls://nats.example:4443")).toBe("tls://nats.example:4443");
    expect(natsDialedHost("nats://host:99999")).toBeUndefined();
  });
});
