import { afterEach, expect, test } from "bun:test";
import { REDIRECT_CODES, type RedirectServers, redirectServers } from "./redirect-servers";

const started: RedirectServers[] = [];
afterEach(() => {
  for (const servers of started.splice(0)) servers.stop();
});

// The instrument of every redirect row: a client that follows the redirect must show up in
// `received`, or an empty `received` proves nothing.
test("a client that follows each redirect reaches the other server, which records what it sent", async () => {
  const servers = redirectServers();
  started.push(servers);
  for (const code of REDIRECT_CODES) {
    servers.code = code;
    const answer = await fetch(`${servers.origin}/v1/send?to=b`, {
      method: "POST",
      headers: { "x-api-key": `made-up-key-${code}` },
      body: "made-up body",
    });
    expect(answer.status).toBe(200);
  }
  expect(servers.elsewhere).not.toBe(servers.origin);
  expect(servers.received.map(({ method, path }) => `${method} ${path}`)).toEqual([
    "GET /v1/send?to=b",
    "GET /v1/send?to=b",
    "GET /v1/send?to=b",
    "POST /v1/send?to=b",
    "POST /v1/send?to=b",
  ]);
  expect(servers.received.map(({ headers }) => headers["x-api-key"])).toEqual(
    REDIRECT_CODES.map((code) => `made-up-key-${code}`)
  );
  expect(servers.received.at(-1)?.body).toBe("made-up body");
});

test("a client that follows no redirect gets the status and a Location on the other server, which receives nothing", async () => {
  const servers = redirectServers({
    direct: () => new Response("direct"),
    redirects: (path) => path === "/moved",
  });
  started.push(servers);
  for (const code of REDIRECT_CODES) {
    servers.code = code;
    const answer = await fetch(`${servers.origin}/moved?q=1`, { redirect: "manual" });
    expect(answer.status).toBe(code);
    expect(answer.headers.get("Location")).toBe(`${servers.elsewhere}/moved?q=1`);
  }
  expect(await (await fetch(`${servers.origin}/kept`)).text()).toBe("direct");
  expect(servers.received).toEqual([]);
});
