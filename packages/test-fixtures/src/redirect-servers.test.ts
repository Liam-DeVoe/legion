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

test("a redirect names the Location a test sets, or none, as it is written", async () => {
  const servers = redirectServers();
  started.push(servers);
  for (const [location, named] of [
    ["/another-path?q=1", "/another-path?q=1"],
    ["", ""],
    ["http://[not a host", "http://[not a host"],
    [null, null],
  ] as const) {
    servers.location = location;
    const answer = await fetch(`${servers.origin}/asked`, { redirect: "manual" });
    expect(answer.headers.get("Location"), String(location)).toBe(named);
  }
  servers.location = undefined;
  const answer = await fetch(`${servers.origin}/asked`, { redirect: "manual" });
  expect(answer.headers.get("Location")).toBe(`${servers.elsewhere}/asked`);
});

/** What `pending` settled with by the next turn of the event loop, or "pending". */
async function byTheNextTurn(pending: Promise<unknown>): Promise<unknown> {
  const bound = Promise.withResolvers<"pending">();
  setImmediate(() => bound.resolve("pending"));
  return await Promise.race([pending, bound.promise]);
}

// A client that reads a redirect's body before its status waits on this body for good, so a row
// that gives it one shows whether the client refuses before it reads.
test("an endless body sends its first chunk and then nothing, until the server stops", async () => {
  const servers = redirectServers();
  started.push(servers);
  servers.endlessBody = true;
  const answer = await fetch(`${servers.origin}/asked`, { redirect: "manual" });
  expect(answer.status).toBe(307);
  const reader = answer.body?.getReader();
  if (reader === undefined) throw new Error("the redirect had no body");
  const first = await reader.read();
  expect(new TextDecoder().decode(first.value)).toBe("the first chunk of a body that ");
  const next = reader.read().then(
    (read) => (read.done ? "ended" : "more"),
    () => "failed"
  );
  expect(await byTheNextTurn(next)).toBe("pending");
  servers.stop();
  expect(await next).not.toBe("more");
  // The control: with no endless body, the redirect's body is empty and ends at once.
  const plain = redirectServers();
  started.push(plain);
  expect(await (await fetch(`${plain.origin}/asked`, { redirect: "manual" })).text()).toBe("");
});
