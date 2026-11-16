import { describe, expect, test } from "bun:test";
import { join } from "node:path";

// The operator's NATS helpers given a server URL that holds a credential, and a connect that
// fails: each names the failure and exits 1, and neither the credential nor nats.js's own error
// (which quotes the URL it could not parse, `input` and all) reaches its output. No server runs:
// port 99999 does not parse, and nothing listens on 127.0.0.1:1.
const CANARY = "canary-pw-7f3a";
const cases = [
  [
    "a URL that does not parse",
    `nats://user:${CANARY}@127.0.0.1:99999`,
    "a server address that does not read as a host",
  ],
  ["a server that refuses", `nats://user:${CANARY}@127.0.0.1:1`, "nats://127.0.0.1:1"],
] as const;

function run(script: string, ...args: string[]) {
  const result = Bun.spawnSync(["bun", join(import.meta.dir, script), ...args], {
    stdout: "pipe",
    stderr: "pipe",
    timeout: 30_000,
  });
  return {
    exitCode: result.exitCode,
    output: `${result.stdout.toString()}${result.stderr.toString()}`,
  };
}

describe.each(cases)("a failed NATS connect at %s", (_, url, named) => {
  test("nats-stream names it and never prints the credential", () => {
    const result = run("nats-stream.ts", "last", url, "STREAM", "subject", "1");
    expect(result.exitCode).toBe(1);
    expect(result.output).toContain(`nats-stream: could not connect to NATS at ${named}: `);
    expect(result.output.toLowerCase()).not.toContain(CANARY);
  });

  for (const [command, ...rest] of [
    ["publish", "subject", "data"],
    ["envelope", "session", "message-id", "1"],
  ]) {
    test(`dispatch-user-turns ${command} names it and never prints the credential`, () => {
      const [subjectOrSession, ...more] = rest;
      const result = run("dispatch-user-turns.ts", command, url, subjectOrSession, ...more);
      expect(result.exitCode).toBe(1);
      expect(result.output).toContain(
        `dispatch-user-turns: could not connect to NATS at ${named}: `
      );
      expect(result.output.toLowerCase()).not.toContain(CANARY);
    });
  }
});
