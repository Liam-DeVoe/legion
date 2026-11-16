import { describe, expect, test } from "bun:test";
import { NATS_ADDRESS_NOT_SHOWN, natsErrorForLog } from "../nats-connection";

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
    // A port out of range: nats.js quotes the address it could not parse, with its scheme flipped.
    const server = `nats://${userinfo("user", "canary-pw")}canary-host:99999`;
    const quoted = `"http://${userinfo("user", "canary-pw")}canary-host:99999" cannot be parsed as a URL`;
    expect(natsErrorForLog(quoted, [server]).toLowerCase()).not.toContain("canary");
  });
});
