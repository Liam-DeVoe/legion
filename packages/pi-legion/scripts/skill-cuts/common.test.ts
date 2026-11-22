import { describe, expect, test } from "bun:test";
import { MAX_RATE_WAIT_MS, type ResetIo, untilInstallationReset } from "./common";
import { graphqlArgs } from "./copies";

// Catches: a wait for a primary rate limit's reset that read `gh api rate_limit`, which names no
// owner, so the pod's gh answers it from its default owner's installation and not from the one
// that refused the call. Where the default owner's reset comes sooner, each of the waits ends
// before the refusing installation's reset, and the read fails after its last try.
describe("untilInstallationReset", () => {
  const NOW = Date.parse("2026-11-20T00:00:00Z");
  const COMMIT = "1111111111111111111111111111111111111111";
  const seconds = (ms: number) => Math.floor(ms / 1000);
  const iso = (ms: number) => new Date(ms).toISOString();
  type Answer = { readonly code: number; readonly stdout: string };
  /** A `gh api -i` answer: its status line, an X-RateLimit-Reset header when `reset` (seconds
   *  since the epoch) is given, and `body`. */
  const included = (status: string, reset: number | undefined, body: unknown) =>
    [
      `HTTP/2.0 ${status}`,
      "Content-Type: application/json; charset=utf-8",
      ...(reset === undefined ? [] : [`X-Ratelimit-Reset: ${reset}`]),
      "",
      JSON.stringify(body),
    ].join("\r\n");
  /** `gh api rate_limit`'s answer: another installation's resets, a minute and half a minute
   *  away, sooner than the refusing installation's. */
  const rateLimit: Answer = {
    code: 0,
    stdout: JSON.stringify({
      resources: { core: { reset: seconds(NOW) + 60 }, graphql: { reset: seconds(NOW) + 30 } },
    }),
  };
  /** A stand-in gh at NOW: `answer` gives its answer to each call but `rate_limit`, which gets
   *  `rateLimit`; each call kept in `calls`, each line said in `lines`. */
  const standIn = (answer: (args: readonly string[]) => Answer | undefined) => {
    const calls: string[][] = [];
    const lines: string[] = [];
    const io: ResetIo = {
      gh: async (args) => {
        calls.push([...args]);
        if (args.includes("rate_limit")) return rateLimit;
        return answer(args) ?? { code: 1, stdout: "" };
      },
      say: (line) => void lines.push(line),
      now: () => NOW,
    };
    return { io, calls, lines };
  };

  test.each([
    ["build.ts's tree read", ["api", `repos/acme/x/git/trees/${COMMIT}?recursive=1`]],
    [
      "build.ts's file read",
      ["api", "-X", "GET", "repos/acme/x/contents/a.md", "-f", `ref=${COMMIT}`],
    ],
    [
      "context.ts's file read",
      [
        ...["api", "--method", "GET", "-H", "Accept: application/vnd.github.raw"],
        `repos/acme/x/contents/a.md?ref=${COMMIT}`,
      ],
    ],
    ["copies.ts's commit read", ["api", "repos/acme/x/commits?sha=main&per_page=1"]],
  ])("%s, refused for the REST limit, waits until the reset of acme's installation, from the X-RateLimit-Reset header of gh api -i repos/acme/x, which gh prints on a refused answer too", async (_, args) => {
    const reset = seconds(NOW) + 600;
    const { io, calls, lines } = standIn((asked) =>
      asked.join(" ") === "api -i --method GET repos/acme/x"
        ? {
            code: 1,
            stdout: included("403 Forbidden", reset, {
              message: "API rate limit exceeded for installation ID 1.",
            }),
          }
        : undefined
    );
    expect(await untilInstallationReset(args, io)).toBe(601_000);
    expect(calls).toEqual([["api", "-i", "--method", "GET", "repos/acme/x"]]);
    expect(lines).toEqual([
      `acme's REST rate limit resets at ${iso(reset * 1000)}; waiting 601 s, until ${iso(NOW + 601_000)}`,
    ]);
  });

  test("a call that names an owner alone (users/acme) reads the reset of acme's first repository's listing", async () => {
    const reset = seconds(NOW) + 120;
    const { io, calls } = standIn((asked) =>
      asked.join(" ") === "api -i --method GET users/acme/repos?per_page=1"
        ? { code: 0, stdout: included("200 OK", reset, [{ full_name: "acme/x" }]) }
        : undefined
    );
    expect(await untilInstallationReset(["api", "users/acme"], io)).toBe(121_000);
    expect(calls).toEqual([["api", "-i", "--method", "GET", "users/acme/repos?per_page=1"]]);
  });

  test("a GraphQL call refused for its limit reads the rateLimit.resetAt of the installation its owner field names, with its owner and name fields", async () => {
    const resetAt = "2026-11-20T00:05:00Z";
    const { io, calls, lines } = standIn((asked) =>
      asked[2] === "graphql" && asked.includes("owner=acme") && asked.includes("name=x")
        ? {
            code: 0,
            stdout: included("200 OK", seconds(NOW) + 1, { data: { rateLimit: { resetAt } } }),
          }
        : undefined
    );
    const query =
      "query($owner: String!, $name: String!) { repository(owner: $owner, name: $name) { id } }";
    expect(await untilInstallationReset(graphqlArgs("acme/x", query), io)).toBe(301_000);
    expect(calls.map((call) => call.slice(0, 7))).toEqual([
      ["api", "-i", "graphql", "-f", "owner=acme", "-f", "name=x"],
    ]);
    expect(calls[0]?.[8]).toContain("rateLimit { resetAt }");
    expect(lines).toEqual([
      `acme's GraphQL rate limit resets at ${iso(Date.parse(resetAt))}; waiting 301 s, until ${iso(NOW + 301_000)}`,
    ]);
  });

  test("a GraphQL read of a reset that GitHub refuses too, its limit spent, gives the reset in its X-RateLimit-Reset header", async () => {
    const reset = seconds(NOW) + 240;
    const { io } = standIn((asked) =>
      asked[2] === "graphql"
        ? {
            code: 1,
            stdout: included("200 OK", reset, {
              errors: [{ type: "RATE_LIMITED", message: "API rate limit exceeded" }],
            }),
          }
        : undefined
    );
    expect(await untilInstallationReset(graphqlArgs("acme/x", "query { x }"), io)).toBe(241_000);
  });

  test("gh repo list acme, which reads the GraphQL API, reads acme's GraphQL reset with its owner field alone", async () => {
    const resetAt = "2026-11-20T00:02:00Z";
    const { io, calls } = standIn((asked) =>
      asked[2] === "graphql" &&
      asked.includes("owner=acme") &&
      !asked.some((arg) => arg.startsWith("name="))
        ? { code: 0, stdout: included("200 OK", undefined, { data: { rateLimit: { resetAt } } }) }
        : undefined
    );
    const listing = ["repo", "list", "acme", "--limit", "2000", "--json", "nameWithOwner"];
    expect(await untilInstallationReset(listing, io)).toBe(121_000);
    expect(calls.map((call) => call.slice(0, 5))).toEqual([
      ["api", "-i", "graphql", "-f", "owner=acme"],
    ]);
    expect(calls[0]?.[6]).toContain("repositoryOwner(login: $owner)");
  });

  test.each([
    [
      "a code search",
      ["api", "-i", "-X", "GET", "search/code", "-f", "q=x org:acme", "-f", "page=1"],
    ],
    ["gh api rate_limit", ["api", "rate_limit"]],
    [
      "a GraphQL call with no owner field",
      ["api", "graphql", "-f", "query=query { viewer { login } }"],
    ],
  ])("%s names no owner: it waits the 15-minute cap, with no read", async (_, args) => {
    const { io, calls, lines } = standIn(() => undefined);
    expect(await untilInstallationReset(args, io)).toBe(MAX_RATE_WAIT_MS);
    expect(MAX_RATE_WAIT_MS).toBe(900_000);
    expect(calls).toEqual([]);
    expect(lines).toEqual([
      `the refused call names no owner in its endpoint or its owner field, so no installation's reset is read; waiting 900 s, the longest rate wait, until ${iso(NOW + 900_000)}`,
    ]);
  });

  test.each([
    [
      "a read that fails with no answer",
      { code: 1, stdout: "" },
      "could not be read (gh exited 1, giving none)",
    ],
    [
      "an answer with no reset",
      { code: 0, stdout: included("200 OK", undefined, {}) },
      "could not be read (its answer gave none)",
    ],
    [
      "a reset already past",
      { code: 0, stdout: included("200 OK", seconds(NOW) - 5, {}) },
      `resets at ${iso((seconds(NOW) - 5) * 1000)}, a time already past`,
    ],
  ])("%s waits the 15-minute cap, never a minute", async (_, answer, why) => {
    const { io, lines } = standIn(() => answer);
    expect(await untilInstallationReset(["api", "repos/acme/x/git/blobs/abc"], io)).toBe(900_000);
    expect(lines).toHaveLength(1);
    expect(lines[0]).toContain(why);
    expect(lines[0]).toEndWith(
      `; waiting 900 s, the longest rate wait, until ${iso(NOW + 900_000)}`
    );
  });

  test("a reset more than 15 minutes away is waited for 15 minutes", async () => {
    const { io } = standIn(() => ({
      code: 0,
      stdout: included("200 OK", seconds(NOW) + 3_600, {}),
    }));
    expect(await untilInstallationReset(["api", "repos/acme/x"], io)).toBe(900_000);
  });
});
