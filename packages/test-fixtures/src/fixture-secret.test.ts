import { describe, expect, test } from "bun:test";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fixtureSecret, wordBuiltFixtureSecrets } from "./fixture-secret";

describe("fixtureSecret", () => {
  test("is its prefix, a dash and 24 random upper-case letters, new at every call", () => {
    const one = fixtureSecret("boot");
    expect(one).toMatch(/^boot-[A-Z]{24}$/);
    expect(fixtureSecret("boot")).not.toBe(one);
  });

  // Every 10-character window holds a random upper-case letter, so no window reads as a word:
  // with the longest prefix the first window holds one random character, so 200 secrets test it.
  test("leaves no 10-character window without an upper-case letter", () => {
    for (let made = 0; made < 200; made += 1) {
      const secret = fixtureSecret("abcdefgh");
      for (let at = 0; at + 10 <= secret.length; at += 1) {
        expect(secret.slice(at, at + 10)).toMatch(/[A-Z]/);
      }
    }
  });

  test("refuses a prefix that could hold a word of 10 characters", () => {
    expect(() => fixtureSecret("controller")).toThrow("1 to 8 lower-case letters or digits");
    expect(() => fixtureSecret("")).toThrow();
  });
});

describe("wordBuiltFixtureSecrets", () => {
  const packageWith = (source: string): string => {
    const root = mkdtempSync(path.join(tmpdir(), "fixture-secret-"));
    writeFileSync(path.join(root, "probe.test.ts"), source);
    return root;
  };

  // A check that read nothing has not passed: a root with no test source is an error, not [].
  test("refuses a root that holds no test source", () => {
    const root = mkdtempSync(path.join(tmpdir(), "fixture-secret-"));
    writeFileSync(
      path.join(root, "index.ts"),
      'export const greeting = "a file the check does not read";\n'
    );
    expect(() => wordBuiltFixtureSecrets(root)).toThrow(`no test source under ${root}`);
  });

  // The control: the fixture that hid the operator's claim refusal (#4140) is found.
  test.each([
    [
      "a word-built capability in a 0600 file",
      'await writeFile(secretFile, "controller-start-capability\\n", { mode: 0o600 });',
    ],
    [
      "a word-built token in an environment variable",
      'env: { DISPATCH_TOKEN: "environment-token" },',
    ],
    // biome-ignore lint/suspicious/noTemplateCurlyInString: source text the scanner reads
    ["a secret built around a name", "const BOOT = { LEGION_BOOT_TOKEN: `${name}-boot` };"],
    ["a camel-case token", 'const bootToken = "launch-boot-token";'],
    ["a token file written with no mode", 'writeFileSync(tokenFile, "pane-listener-token");'],
    ["a plain bearer", 'bearerCredential("listener-token")'],
    ["a stand-in's registration secret", 'Response.json({ secret: "registration-secret" })'],
  ])("finds %s", (_, source) => {
    expect(wordBuiltFixtureSecrets(packageWith(source))).toHaveLength(1);
  });

  test.each([
    ["the helper", 'const SECRET = fixtureSecret("ctl");'],
    ["a short value, never held", 'process.env.LEGION_BOOT_TOKEN = "decoy";'],
    // biome-ignore lint/suspicious/noTemplateCurlyInString: source text the scanner reads
    ["the helper's value in a file", "await writeFile(file, `${SECRET}\\n`, { mode: 0o600 });"],
    // biome-ignore lint/suspicious/noTemplateCurlyInString: source text the scanner reads
    ["a random call", "const token = `${crypto.randomUUID()}`;"],
    // biome-ignore lint/suspicious/noTemplateCurlyInString: source text the scanner reads
    ["a claim token, which names a role", "const claimToken = `legion-${name}-implementer`;"],
    [
      "a refused value, exempted",
      'ENVOY_TOKEN: "S3CRET\\nnotes", // fixture-secret: refused for its newline, never held',
    ],
  ])("leaves %s", (_, source) => {
    expect(wordBuiltFixtureSecrets(packageWith(source))).toEqual([]);
  });
});
