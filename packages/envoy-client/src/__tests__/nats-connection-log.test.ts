import { afterAll, describe, expect, test } from "bun:test";
import { randomBytes } from "node:crypto";
import { connect, ErrorCode, NatsError, nkeys } from "nats";
import { LoggableError } from "../errors";
import {
  natsDialedAddress,
  natsDialFailure,
  natsFailureReason,
  natsServerForLog,
  UNNAMED_NATS_SERVER,
} from "../nats-connection";

// What a log prints of a failed NATS dial: the server as a log names it (natsServerForLog) and why
// it failed (natsFailureReason), an allowlist: nats.js's code, the server's own `-ERR` text, or a
// message this package composed, never the text nats.js or the runtime derived from what the
// servers were set to.
//
// Each URL's user info is joined here, so the source holds no credential-bearing URL for the push
// leak check to find.
const userinfo = (...parts: string[]) => `${parts.join(":")}@`;
const canary = () => `canary${randomBytes(4).toString("hex")}`;
const sixDigits = () => String(100000 + (randomBytes(4).readUInt32BE() % 900000));

/** What real nats.js throws when it fails to connect to `server`: no server runs, so each fails
 *  at the URL parser, the resolver or the socket, and the error is nats.js's own. */
async function realFailure(server: string, timeout = 5_000): Promise<unknown> {
  try {
    const connection = await connect({ servers: [server], reconnect: false, timeout });
    await connection.close();
  } catch (error) {
    return error;
  }
  throw new Error(`${server} connected, which this row needs it not to`);
}

const textOf = (error: unknown) => (error instanceof Error ? error.message : String(error));

// Every spelling a red team found where nats.js dials, and its error then names, something other
// than what a nats: URL parse reads (four rounds of them). Each row runs real nats.js, holds first
// that nats.js's own text carries the secret (so the row can fail for the leak), and then that the
// failure line carries none of it and names no server.
describe("a failed dial's line carries nothing nats.js derived from the server", () => {
  const seed = new TextDecoder().decode(nkeys.createUser().getSeed());
  const tokens = {
    backslash: canary(),
    comma: canary(),
    percent: canary(),
    unicode: canary(),
    slash: canary(),
  };
  const digits = {
    ipv4: sixDigits(),
    loopback: sixDigits(),
    second: sixDigits(),
    ipv6: sixDigits(),
  };
  const mixed = `Xk3pQ9vT2mL8wR5n${randomBytes(3).toString("hex")}`;
  const hyphenated = `Xk3pQ9vT-2mL8wR5n${randomBytes(3).toString("hex")}`;
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
      `nats://Grüße${tokens.unicode}Secret:4222`,
      [`grüße${tokens.unicode}secret`, new URL(`http://Grüße${tokens.unicode}Secret`).hostname],
    ],
    [
      "a token holding an unencoded /",
      `nats://${userinfo(`${tokens.slash}/def+gh==`)}127.0.0.1:4222`,
      [tokens.slash],
    ],
    ["an nkey seed pasted as the server", `nats://${seed}:4222`, [seed]],
    ["a mixed-case token pasted as the server", `nats://${mixed}:4222`, [mixed]],
    // P2: a hyphen is a host label's character, so the label read as a host name.
    [
      "a hyphenated mixed-case token pasted as the server",
      `nats://${hyphenated}:4222`,
      [hyphenated],
    ],
    // P1: nats.js takes the port from the first `:<digits>`, a password's included, and Bun's
    // refusal of a port of 65536 or more prints the number.
    [
      "a password's six digits on an IP host",
      `nats://${userinfo("user", `${digits.ipv4}abc`)}10.0.0.5:4222`,
      [digits.ipv4],
    ],
    [
      "a password's six digits on the loopback",
      `nats://${userinfo("user", `${digits.loopback}abc`)}127.0.0.1:4222`,
      [digits.loopback],
    ],
    [
      "a password's six digits after its second `:`",
      `nats://${userinfo("user", "abc", `${digits.second}x`)}10.0.0.5:4222`,
      [digits.second],
    ],
    [
      "a password's six digits after a `]:`, before an IPv6 host",
      `nats://${userinfo("user", `pw]:${digits.ipv6}`)}[2001:db8::1]:4222`,
      [digits.ipv6],
    ],
  ];
  test.each(rows)(
    "%s",
    async (_, server, secrets) => {
      const error = await realFailure(server);
      const text = textOf(error).toLowerCase();
      expect(secrets.some((secret) => text.includes(secret.toLowerCase()))).toBe(true);
      expect(natsServerForLog(server)).toBe(UNNAMED_NATS_SERVER);
      const line = natsDialFailure(natsServerForLog(server), error, "no credential").toLowerCase();
      for (const secret of secrets) expect(line).not.toContain(secret.toLowerCase());
    },
    30_000
  );
});

// A server that takes the connection and never sends its INFO, for a dial that times out.
const mute = Bun.listen({ hostname: "127.0.0.1", port: 0, socket: { data() {} } });
afterAll(() => mute.stop(true));

// What an operator still reads: each error nats.js and the runtime give a failed dial, as the line
// prints it.
describe("a failed dial's line says why it failed", () => {
  test("a server that refuses the connection: CONNECTION_REFUSED", async () => {
    const error = await realFailure("nats://127.0.0.1:1");
    expect(natsDialFailure(natsServerForLog("nats://127.0.0.1:1"), error)).toBe(
      "could not connect to NATS at nats://127.0.0.1:1: CONNECTION_REFUSED"
    );
  }, 30_000);

  test("a server that never answers: TIMEOUT", async () => {
    const server = `nats://127.0.0.1:${mute.port}`;
    const error = await realFailure(server, 500);
    expect(natsDialFailure(natsServerForLog(server), error)).toBe(
      `could not connect to NATS at ${server}: TIMEOUT`
    );
  }, 30_000);

  test("a server that does not parse: ERR_INVALID_URL, and its URL is not quoted", async () => {
    const server = `nats://${userinfo("user", "canary-pw")}canary-host:99999`;
    const error = await realFailure(server);
    // Bun keeps the URL it could not parse in the error's `input`, which an uncaught error prints.
    expect(String((error as { input?: unknown }).input)).toContain("canary-pw");
    expect(natsFailureReason(error)).toBe("ERR_INVALID_URL");
    expect(natsDialFailure(natsServerForLog(server), error)).toBe(
      `could not connect to NATS at ${UNNAMED_NATS_SERVER}: ERR_INVALID_URL`
    );
  }, 30_000);

  test("a port the socket refuses: ERR_SOCKET_BAD_PORT, and not its number", async () => {
    const digits = sixDigits();
    const error = await realFailure(`nats://${userinfo("user", digits)}127.0.0.1:4222`);
    expect(natsFailureReason(error)).toMatch(/^(?:[a-z]+ )?ERR_SOCKET_BAD_PORT$/);
  }, 30_000);

  test("a name the resolver cannot find: getaddrinfo and its code, and not the name", async () => {
    const name = `${canary()}.invalid`;
    const error = await realFailure(`nats://${name}:4222`);
    expect(textOf(error).toLowerCase()).toContain(name);
    expect(natsFailureReason(error)).toMatch(/^getaddrinfo (?:ENOTFOUND|EAI_AGAIN|EAI_NONAME)$/);
  }, 30_000);

  test("the server's own refusal: its text, which names nothing the client was set with", () => {
    // As nats.js builds it from the server's `-ERR` (protocol.js `toError`).
    const refused = new NatsError("'Authorization Violation'", ErrorCode.AuthorizationViolation);
    expect(natsFailureReason(refused)).toBe("'Authorization Violation'");
    const denied = new NatsError(
      `Permissions Violation for Subscription to "notifications.envoy.deliver.ses_other"`,
      ErrorCode.PermissionsViolation
    );
    expect(natsFailureReason(denied)).toBe(
      `Permissions Violation for Subscription to "notifications.envoy.deliver.ses_other"`
    );
  });

  test("any other nats.js error: its code alone, whatever its text", () => {
    // nats.js's TLS errors carry the runtime's text (node_transport.js), a certificate's names in it.
    const tls = new NatsError(`certificate names ${canary()}.example`, ErrorCode.Tls);
    expect(natsFailureReason(tls)).toBe("TLS");
  });

  test("this package's own reason: its message whole", () => {
    const own = new LoggableError("NATS_AUTH is broker, but the NATS server sent no nonce to sign");
    expect(natsFailureReason(own)).toBe(
      "NATS_AUTH is broker, but the NATS server sent no nonce to sign"
    );
  });

  test("an error of no kind it knows: its type, and its message withheld", () => {
    expect(natsFailureReason(new Error(`refused ${canary()}`))).toBe("Error (message withheld)");
    expect(natsFailureReason(new TypeError(`bad ${canary()}`))).toBe(
      "TypeError (message withheld)"
    );
    expect(natsFailureReason(`a thrown ${canary()}`)).toBe("a thrown string (message withheld)");
  });
});

describe("natsServerForLog", () => {
  test("names a server where nats.js dials the host and port written", () => {
    expect(natsServerForLog(`nats://${userinfo("u", "p")}nats.example:4222`)).toBe(
      "nats://nats.example:4222"
    );
    expect(natsServerForLog("nats://nats.example")).toBe("nats://nats.example");
    expect(natsServerForLog("127.0.0.1:4223")).toBe("127.0.0.1:4223");
    expect(natsServerForLog("nats://[::1]:4222")).toBe("nats://[::1]:4222");
    expect(natsServerForLog("tls://nats.example:4443")).toBe("tls://nats.example:4443");
    expect(natsServerForLog("nats://nats-hub-0123456789ab.example:4222")).toBe(
      "nats://nats-hub-0123456789ab.example:4222"
    );
    expect(natsServerForLog("NATS-HUB-0123456789AB.EXAMPLE:4222")).toBe(
      "NATS-HUB-0123456789AB.EXAMPLE:4222"
    );
    expect(natsServerForLog("nats://deadbeef.example:4222")).toBe("nats://deadbeef.example:4222");
  });

  test("names none where the port nats.js dials is a password's digits", () => {
    // Five digits, below 65536: the socket dials them, and a log naming 4222 would name a server
    // the dial never reached.
    expect(natsServerForLog(`nats://${userinfo("u", "12345")}nats.example:4222`)).toBe(
      UNNAMED_NATS_SERVER
    );
    expect(natsDialedAddress(`nats://${userinfo("u", "12345")}nats.example:4222`)).toEqual({
      host: "nats.example",
      port: 12345,
    });
  });

  test("names none whose host mixes cases, or is a long hexadecimal label", () => {
    expect(natsServerForLog(`nats://${userinfo("u", "p")}Host.Example:4222`)).toBe(
      UNNAMED_NATS_SERVER
    );
    expect(natsServerForLog("nats://0123456789abcdef0123456789abcdef:4222")).toBe(
      UNNAMED_NATS_SERVER
    );
    expect(natsServerForLog("0123456789abcdef:4222")).toBe(UNNAMED_NATS_SERVER);
  });

  test("names none nats.js cannot read", () => {
    expect(natsDialedAddress("nats://host:99999")).toBeUndefined();
    expect(natsServerForLog("nats://host:99999")).toBe(UNNAMED_NATS_SERVER);
  });
});
