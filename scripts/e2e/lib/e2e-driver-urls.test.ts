import { expect, test } from "bun:test";
import { join } from "node:path";
import { rowLimit, runBounded } from "../../../packages/dispatch/e2e/__tests__/bounded-subprocess";

// Two drivers of scripts/e2e take Dispatch's URL as their first argument, and it may carry a user
// and a password. One that is no http or https URL with a host is refused naming its length
// alone, and how to write a user part since it holds an `@`, before anything parses it: the URL
// parser's error for it, which the runtime prints on its `input:` line, holds the value whole.
// The password is made for this run.
const drivers = [
  {
    script: "dispatch-claimant-delivery.ts",
    env: { CLAIMANT_E2E_AGENT_TOKEN: "made-up-agent-token" },
  },
  {
    script: "verification-results.ts",
    env: {
      VERIFICATION_RESULTS_E2E_AGENT_TOKEN: "made-up-agent-token",
      VERIFICATION_RESULTS_E2E_DATABASE_URL: "postgres://127.0.0.1:1/never-opened",
    },
  },
] as const;

test(
  "a driver whose Dispatch URL is no http or https URL with a host names its length alone",
  async () => {
    const password = crypto.randomUUID();
    const dispatchUrl = `http://made-up-user:${password}/x@[bad`;
    const said: { readonly code: number; readonly output: string }[] = [];
    for (const { script, env } of drivers) {
      const { code, stdout, stderr } = await runBounded({
        argv: [
          process.execPath,
          join(import.meta.dir, "..", script),
          dispatchUrl,
          "nats://127.0.0.1:1",
          "http://127.0.0.1:1",
        ],
        bound: "short",
        env: { PATH: process.env.PATH ?? "", HOME: process.env.HOME ?? "", ...env },
        from: import.meta.url,
      });
      said.push({ code, output: stdout + stderr });
    }
    expect(said).toEqual(
      drivers.map(() => ({
        code: 2,
        output: `<dispatch-url> must be an http or https URL: its value is ${dispatchUrl.length} characters long; if it carries a user part, write each @, /, ?, # and \\ in that part percent-encoded (@ as %40, / as %2F), or remove the user part\n`,
      }))
    );
  },
  // One driver process for each script, one after the other.
  rowLimit("short", "short")
);
