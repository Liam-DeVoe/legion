// packages/envoy-client/src/__tests__/token-file-rereads.test.ts
//
// A `DISPATCH_TOKEN_FILE` or `ENVOY_TOKEN_FILE` can name a projected Kubernetes service-account
// token, which the kubelet rewrites in place before it expires (an hour by default). A long-lived
// client — the Oh My Pi and Claude Code plugins, the MCP server, a session's own DispatchClient —
// resolves its settings once, so the credential it holds must read the file for every request:
// one that kept the value it resolved with would present a dead token an hour later.

import { afterEach, expect, test } from "bun:test";
import { writeFileSync } from "node:fs";
import * as path from "node:path";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import { bearerCredential } from "../bearer";
import { DispatchClient } from "../dispatch-http";
import { sessionSettings } from "../session-settings";

const tempDir = temporaryDirectories("token-file-rereads");

const servers: { stop: (closeActive?: boolean) => void }[] = [];
afterEach(() => {
  for (const server of servers.splice(0)) server.stop(true);
});

/** A Dispatch that answers every request with an empty issue list, keeping the Authorization
 *  each carried. */
function stubDispatch(): { url: string; asked: string[] } {
  const asked: string[] = [];
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: (request) => {
      asked.push(request.headers.get("Authorization") ?? "");
      return Response.json([]);
    },
  });
  servers.push(server);
  return { url: `http://127.0.0.1:${server.port}`, asked };
}

/** A file holding `token` as the kubelet writes a projected token: the token and a newline. */
function tokenFile(token: string): string {
  const file = path.join(tempDir(), "token");
  writeFileSync(file, `${token}\n`, { mode: 0o600 });
  return file;
}

/** The settings a session resolves once, at start, from `env` and no envoy.json. */
function resolvedOnce(env: Record<string, string>) {
  return sessionSettings(
    { path: path.join(tempDir(), "envoy.json"), error: null },
    { HOME: tempDir(), ...env },
    { cwd: tempDir(), nats: false }
  );
}

// A DispatchClient made once from settings resolved once presents the token the file holds at
// each request: the first call the first token, the second, after the file was rewritten, the
// second. Control: the same client over a credential holding the value read at resolution (what
// the settings gave before) presents the first token twice, so this test sees a cached read.
test("a DispatchClient made once presents what DISPATCH_TOKEN_FILE holds at each request", async () => {
  const dispatch = stubDispatch();
  const file = tokenFile("first-projected-not-a-secret");
  const settings = resolvedOnce({ DISPATCH_URL: dispatch.url, DISPATCH_TOKEN_FILE: file });
  if (!settings.dispatch.enabled) throw new Error(`Dispatch is off: ${settings.dispatch.error}`);
  const client = new DispatchClient(settings.dispatch.url, settings.dispatch.credential);
  const cached = new DispatchClient(
    settings.dispatch.url,
    bearerCredential(settings.dispatch.credential.secret())
  );

  await client.listAllIssues({});
  writeFileSync(file, "second-projected-not-a-secret\n");
  await client.listAllIssues({});
  expect(dispatch.asked).toEqual(["Bearer first-projected-not-a-secret", "Bearer second-projected-not-a-secret"]);
  expect(client.token, "the secret an excerpt redacts is the one presented last").toBe(
    "second-projected-not-a-secret"
  );

  dispatch.asked.length = 0;
  await cached.listAllIssues({});
  expect(dispatch.asked, "control: a credential that kept its first read").toEqual([
    "Bearer first-projected-not-a-secret",
  ]);
});

// Envoy's listener bearer is read the same way: settings resolved once give a credential whose
// headers carry what ENVOY_TOKEN_FILE holds at each request. Control: the value read at
// resolution, kept, is the first token after the file changed.
test("the Envoy credential presents what ENVOY_TOKEN_FILE holds at each request", async () => {
  const file = tokenFile("first-listener-not-a-secret");
  const settings = resolvedOnce({ ENVOY_URL: "http://127.0.0.1:9", ENVOY_TOKEN_FILE: file });
  if (!settings.envoy.enabled) throw new Error(`Envoy is off: ${settings.envoy.error}`);
  const credential = settings.envoy.credential;
  if (credential === undefined) throw new Error("Envoy resolved no credential");
  const kept = credential.secret();
  expect(await credential.headers("GET", "http://127.0.0.1:9/v1/health")).toEqual({
    Authorization: "Bearer first-listener-not-a-secret",
  });
  writeFileSync(file, "second-listener-not-a-secret\n");
  expect(await credential.headers("GET", "http://127.0.0.1:9/v1/health")).toEqual({
    Authorization: "Bearer second-listener-not-a-secret",
  });
  expect(kept, "control: the value read at resolution").toBe("first-listener-not-a-secret");
});

// A token file that is gone or blank at a request refuses that request, naming the variable and
// the path and never a token, rather than sending the token read before or none: the request is
// not sent. The next request after the file is back presents what it then holds.
test("a DISPATCH_TOKEN_FILE that is gone or blank at a request sends nothing", async () => {
  const dispatch = stubDispatch();
  const file = tokenFile("first-projected-not-a-secret");
  const settings = resolvedOnce({ DISPATCH_URL: dispatch.url, DISPATCH_TOKEN_FILE: file });
  if (!settings.dispatch.enabled) throw new Error(`Dispatch is off: ${settings.dispatch.error}`);
  const client = new DispatchClient(settings.dispatch.url, settings.dispatch.credential);

  writeFileSync(file, "  \n");
  const blank = await client.listAllIssues({}).then(
    () => null,
    (error: unknown) => String(error)
  );
  expect(blank).toContain(`DISPATCH_TOKEN_FILE names ${file}, which is empty`);
  expect(blank).not.toContain("first-projected-not-a-secret");
  expect(dispatch.asked).toEqual([]);

  writeFileSync(file, "third-projected-not-a-secret\n");
  await client.listAllIssues({});
  expect(dispatch.asked).toEqual(["Bearer third-projected-not-a-secret"]);
});
