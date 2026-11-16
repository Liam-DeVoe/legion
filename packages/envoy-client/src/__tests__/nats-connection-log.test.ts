import { describe, expect, test } from "bun:test";
import {
  NATS_ADDRESS_NOT_SHOWN,
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
