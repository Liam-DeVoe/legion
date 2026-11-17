import { expect, test } from "bun:test";
import { fileURLToPath } from "node:url";
import {
  REDIRECT_CODES,
  type RedirectServers,
  redirectCase,
  redirectServers,
  UNNAMED_TARGETS,
} from "@legion/test-fixtures/redirect-servers";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";

// The e2e client's `request` (e2e/api.ts) as a row or a script calls it: in a `bun` process of its
// own, since the module reads its server, `PLAYWRIGHT_BASE_URL` here, and its bearer when it loads.
// Each call carries a credential: the agent bearer and a handoff whose body holds a made-up key, or
// the session cookie of a dev sign-in and a comment, the sign-in answered by the first server
// itself.
const api = fileURLToPath(new URL("../api.ts", import.meta.url));
const scratch = temporaryDirectory("e2e-api-redirects");

/** The two calls of the client, each with the words its refusal names its credential by. */
const CALLS = [
  {
    carries: "the agent bearer",
    route: "/api/v1/issues/ACME-1/handoffs/implement",
    call: 'createHandoff("ACME-1", "implement", { key: "made-up-handoff-key" })',
  },
  {
    carries: "the session cookie",
    route: "/api/v1/issues/ACME-1/comments",
    call: 'createComment("ACME-1", { body: "A made-up comment." }, { login: "alice" })',
  },
] as const;

/** The two servers: the first answers the dev sign-in itself and redirects every API call. */
function dispatchRedirects(): RedirectServers {
  return redirectServers({
    redirects: (pathname) => pathname.startsWith("/api/"),
    direct: () =>
      new Response(null, {
        status: 302,
        headers: { Location: "/", "Set-Cookie": "dsession=2.made-up.0.0.made-up-mac; Path=/" },
      }),
    atElsewhere: () => Response.json({ id: "h-elsewhere", version: 1 }, { status: 201 }),
  });
}

/** The message of the error that `call` of the client, against the first of `servers`, ended its
 * `bun` process with, as Bun prints it (the whole stderr when it printed none), and the exit. */
async function run(
  servers: RedirectServers,
  call: string
): Promise<{ exit: number; died: string }> {
  const child = Bun.spawn(
    ["bun", "-e", `import * as api from ${JSON.stringify(api)};\nawait api.${call};`],
    {
      cwd: scratch,
      env: {
        PATH: process.env.PATH ?? "",
        HOME: scratch,
        PLAYWRIGHT_BASE_URL: servers.origin,
        E2E_AGENT_TOKEN: "made-up-agent-token",
      },
      stdout: "pipe",
      stderr: "pipe",
    }
  );
  const [exit, stderr] = await Promise.all([child.exited, new Response(child.stderr).text()]);
  const line = stderr.split("\n").find((one) => one.startsWith("error: "));
  return { exit, died: line === undefined ? stderr : line.slice("error: ".length) };
}

/** The line `request` refuses a redirect of `route` with. */
function refusal(route: string, status: number, where: string, carries: string): string {
  return `POST ${route} failed: ${status}, ${where}, and the e2e client follows no redirect, since it carries ${carries}: PLAYWRIGHT_BASE_URL, or DISPATCH_E2E_PORT where it is unset, must name Dispatch's own address`;
}

// fetch would send a 307's or 308's body on to the origin a redirect names, a key in a handoff
// included, and take that origin's answer as Dispatch's (Bun and Node drop the bearer and the
// cookie alone). So no call follows a redirect: the 3xx is the answer, and the call fails naming
// it. One case for each code, so a client that follows again fails at each.
for (const code of REDIRECT_CODES) {
  test(
    redirectCase(code),
    async () => {
      const servers = dispatchRedirects();
      servers.code = code;
      try {
        for (const { carries, route, call } of CALLS) {
          const outcome = await run(servers, call);
          expect(servers.received, `the other origin after a ${code} of ${route}`).toEqual([]);
          expect(outcome).toEqual({
            exit: 1,
            died: refusal(route, code, `redirecting to ${servers.elsewhere}`, carries),
          });
        }
      } finally {
        servers.stop();
      }
    },
    {
      // A hang guard over the two `bun` client processes this case starts and waits on itself,
      // which no bound of bounded-subprocess.ts covers.
      timeout: 30_000,
    }
  );
}

// A call that read a redirect's body before its status would wait on a body that never ends, and
// refuse nothing.
test(
  "a redirect whose body never ends is refused naming its status, before any of its body is read",
  async () => {
    const servers = dispatchRedirects();
    servers.endlessBody = true;
    try {
      const [{ carries, route, call }] = CALLS;
      expect(await run(servers, call)).toEqual({
        exit: 1,
        died: refusal(route, 307, `redirecting to ${servers.elsewhere}`, carries),
      });
    } finally {
      servers.stop();
    }
  },
  {
    // The bound of the wait on a body that never ends: a client that read the body before the
    // status would hang until this stops it, and one that refuses ends at once.
    timeout: 10_000,
  }
);

// A redirect within Dispatch's own address, or one that names nowhere, is not named as the
// address the client already has, and a host longer than any host name is never printed. The
// client's paths are its own whatever the URL's, so each says to name Dispatch's own address.
test(
  "a redirect to another path of the same address, to no target, or to an overlong host is refused saying so",
  async () => {
    const servers = dispatchRedirects();
    try {
      const [{ carries, route, call }] = CALLS;
      const said: string[] = [];
      for (const { location } of UNNAMED_TARGETS) {
        servers.location = location;
        said.push((await run(servers, call)).died);
      }
      expect(said).toEqual(UNNAMED_TARGETS.map(({ where }) => refusal(route, 307, where, carries)));
    } finally {
      servers.stop();
    }
  },
  {
    // A hang guard over the `bun` client processes this row starts one after another, one for
    // each unnamed target, which no bound of bounded-subprocess.ts covers.
    timeout: 30_000,
  }
);
