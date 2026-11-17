import { afterEach, describe, expect, test } from "bun:test";
import { randomBytes } from "node:crypto";
import {
  forgetHeldSecrets,
  holdSecret,
  holdUrlUserinfo,
  scrub,
  scrubError,
  WITHHELD,
} from "./held-secrets";

afterEach(forgetHeldSecrets);

const secret = () => `dsp_${randomBytes(24).toString("base64url")}`;

describe("scrub", () => {
  test("a line with no held secret comes back as itself, with or without secrets held", () => {
    const line = "Dispatch at https://dispatch.example is unreachable: ECONNREFUSED";
    expect(scrub(line)).toBe(line);
    holdSecret(secret());
    expect(scrub(line)).toBe(line);
  });

  test("a held secret is taken out whole, wherever it stands", () => {
    const held = holdSecret(secret());
    expect(scrub(`refused ${held} twice: ${held}.`)).toBe(
      `refused ${WITHHELD} twice: ${WITHHELD}.`
    );
  });

  test("a run of 10 or more of its characters is taken out, and a shorter one is not", () => {
    const held = holdSecret(secret());
    const run = held.slice(5, 17);
    expect(scrub(`echo [${run}]`)).toBe(`echo [${WITHHELD}]`);
    expect(scrub(`echo [${held.slice(3, 13)}]`)).toBe(`echo [${WITHHELD}]`);
    expect(scrub(`echo [${held.slice(3, 12)}]`)).toBe(`echo [${held.slice(3, 12)}]`);
  });

  test("two held secrets side by side are one stretch withheld", () => {
    const one = holdSecret(secret());
    const two = holdSecret(secret());
    expect(scrub(`${one}${two}`)).toBe(WITHHELD);
  });

  test("a value shorter than 10 characters is not held", () => {
    holdSecret("token");
    expect(scrub("a token in the line")).toBe("a token in the line");
  });

  test("an error's message, stack and cause are scrubbed", () => {
    const held = holdSecret(secret());
    const error = new Error(`outer ${held}`, { cause: new Error(`inner ${held.slice(2, 20)}`) });
    scrubError(error);
    expect(error.message).toBe(`outer ${WITHHELD}`);
    expect(String(error.stack)).not.toContain(held);
    expect((error.cause as Error).message).toBe(`inner ${WITHHELD}`);
  });
});

// A URL's user info is sent by Bun's fetch as `Authorization: Basic <base64 of user:password>`,
// decoded, so a server that echoes the header or the URL prints neither once the URL is held.
describe("holdUrlUserinfo", () => {
  test("holds the password, the decoded pair and the Basic value", () => {
    const password = `pw-${randomBytes(9).toString("hex")}`;
    holdUrlUserinfo(
      `https://operator-7f2a:${encodeURIComponent(`${password}/x`)}@listener.example`
    );
    const basic = btoa(`operator-7f2a:${password}/x`);
    expect(scrub(`you sent Basic ${basic}`)).toBe(`you sent Basic ${WITHHELD}`);
    expect(scrub(`password ${password}/x`)).toBe(`password ${WITHHELD}`);
    expect(scrub(`escaped ${encodeURIComponent(`${password}/x`)}`)).toBe(`escaped ${WITHHELD}`);
  });

  test("a URL with no user info, or no URL, holds nothing", () => {
    holdUrlUserinfo("https://listener.example/v1");
    holdUrlUserinfo("not a url at all");
    expect(scrub("https://listener.example/v1 and not a url at all")).toBe(
      "https://listener.example/v1 and not a url at all"
    );
  });
});
