// packages/broker-client/src/__tests__/sources.test.ts
//
// Two of the identities a TypeScript caller holds, the two this file tests: a pod's projected
// service-account token, read from the file its runtime mounts, and a Compute Engine machine's
// own Google identity token, asked of its metadata server with format=full and fetched through
// no proxy. A pool machine's identity and a person's sign-in have tests of their own
// (pool-identity.test.ts, person-identity.test.ts).

import { afterAll, expect, test } from "bun:test";
import { writeFile } from "node:fs/promises";
import { join } from "node:path";
import { inspect } from "node:util";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import { METADATA_HOST_VAR, metadataIdentity } from "../metadata-identity";
import { projectedToken } from "../projected-token";
import { SUBJECT_TOKEN_TYPE_GOOGLE, SUBJECT_TOKEN_TYPE_KUBERNETES } from "../wire";

const makeDirectory = temporaryDirectories("broker-client");

const servers: { stop: (closeActive?: boolean) => void }[] = [];
afterAll(() => {
  for (const server of servers) server.stop(true);
});

/** A file in a directory of this test's own, holding `contents`. */
async function tokenFile(contents: string): Promise<string> {
  const path = join(makeDirectory(), "token");
  await writeFile(path, contents, { mode: 0o600 });
  return path;
}

/** One request the stub metadata server was asked, as it saw it. */
interface MetadataRequest {
  readonly path: string;
  readonly query: string;
  readonly flavor: string | null;
}

/** A stand-in metadata server answering `token`, and what it was asked. */
function stubMetadata(token: string): { host: string; asked: MetadataRequest[] } {
  const asked: MetadataRequest[] = [];
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: (request) => {
      const url = new URL(request.url);
      asked.push({
        path: url.pathname,
        query: url.search,
        flavor: request.headers.get("Metadata-Flavor"),
      });
      return new Response(`${token}\n`);
    },
  });
  servers.push(server);
  return { host: `127.0.0.1:${server.port}`, asked };
}

test("a projected token is read from its file at every read, trimmed", async () => {
  const path = await tokenFile("first.projected.token\n");
  const source = projectedToken(path);
  expect(await source.read()).toEqual({
    token: "first.projected.token",
    tokenType: SUBJECT_TOKEN_TYPE_KUBERNETES,
  });
  // The kubelet rewrites the file in place, so a renewal must see the new token.
  await writeFile(path, "second.projected.token\n");
  expect((await source.read()).token).toBe("second.projected.token");
  expect(source.describe()).toBe(`the projected service-account token ${path}`);
});

test("a projected token file that is missing or empty is an error naming it, never an empty token", async () => {
  const missing = join(makeDirectory(), "absent");
  expect(projectedToken(missing).read()).rejects.toThrow(missing);
  const blank = await tokenFile("  \n");
  expect(projectedToken(blank).read()).rejects.toThrow(`${blank} is empty`);
});

test("a machine's identity token is asked for with format=full, the audience and the header", async () => {
  const metadata = stubMetadata("machine.identity.token");
  const source = metadataIdentity("example-audience", {
    [METADATA_HOST_VAR]: metadata.host,
  } as NodeJS.ProcessEnv);
  expect(await source.read()).toEqual({
    token: "machine.identity.token",
    tokenType: SUBJECT_TOKEN_TYPE_GOOGLE,
  });
  expect(metadata.asked).toEqual([
    {
      path: "/computeMetadata/v1/instance/service-accounts/default/identity",
      query: "?audience=example-audience&format=full",
      flavor: "Google",
    },
  ]);
});

test("a metadata server that refuses is an error naming the URL it was asked", async () => {
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: () => new Response("no identity here", { status: 403 }),
  });
  servers.push(server);
  const source = metadataIdentity("example-audience", {
    [METADATA_HOST_VAR]: `127.0.0.1:${server.port}`,
  } as NodeJS.ProcessEnv);
  expect(source.read()).rejects.toThrow(`127.0.0.1:${server.port}`);
  expect(source.read()).rejects.toThrow("HTTP 403");
});

// GCE_METADATA_HOST names a host: a host name or an IP address, with an optional port, and nothing
// else. Any other value is refused naming its length alone, with nothing asked: a user part, a
// URL written in its place (the source builds `http://<host>/computeMetadata/…`, and the URL
// parser would read `http://http://user:password@host/…` with the host `http` and the password in
// the path), a path, or a space. No member of the error, nor of a cause under it, holds the
// password. The password is made for this run.
test("a metadata host is a host and a port alone, and any other value is refused by its length", async () => {
  const password = crypto.randomUUID();
  let asked = 0;
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: () => {
      asked += 1;
      return new Response("no identity here", { status: 403 });
    },
  });
  servers.push(server);
  const refused = [
    `made-up-user:${password}@127.0.0.1:${server.port}`,
    `http://made-up-user:${password}@127.0.0.1:${server.port}`,
    `http://127.0.0.1:${server.port}`,
    `127.0.0.1:${server.port}/elsewhere`,
    `made-up-user:${password}@bad host`,
    `127.0.0.1:${server.port}?x=${password}`,
    "127.0.0.1:99999",
  ];
  const said: { readonly message: string; readonly holds: boolean }[] = [];
  for (const host of [`127.0.0.1:${server.port}`, `LOCALHOST:${server.port}`, ...refused]) {
    const error = await metadataIdentity("example-audience", {
      [METADATA_HOST_VAR]: host,
    } as NodeJS.ProcessEnv)
      .read()
      .then(
        () => undefined,
        (thrown: unknown) => thrown
      );
    said.push({
      message: error instanceof Error ? error.message : String(error),
      holds: inspect(error, { depth: Number.POSITIVE_INFINITY, showHidden: true }).includes(
        password
      ),
    });
  }
  expect({ said, asked }).toEqual({
    said: [
      {
        message: `the metadata server at http://127.0.0.1:${server.port}/computeMetadata/v1/instance/service-accounts/default/identity?audience=example-audience&format=full answered HTTP 403`,
        holds: false,
      },
      {
        message: `the metadata server at http://LOCALHOST:${server.port}/computeMetadata/v1/instance/service-accounts/default/identity?audience=example-audience&format=full answered HTTP 403`,
        holds: false,
      },
      ...refused.map((host) => ({
        message: `${METADATA_HOST_VAR} must name the metadata server's host, with an optional port and nothing else: its value is ${host.length} characters long`,
        holds: false,
      })),
    ],
    // The two hosts were asked; no refused value was.
    asked: 2,
  });
});

test("no proxy variable can take the identity token", async () => {
  // A proxy that would take the token if anything here honoured one. It answers every request
  // with a token of its own, so a reader that went through it would succeed with the wrong
  // token rather than fail, which is the failure this guards against.
  const taken: string[] = [];
  const proxy = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: (request) => {
      taken.push(request.url);
      return new Response("the.proxy.took.it");
    },
  });
  servers.push(proxy);
  const metadata = stubMetadata("machine.identity.token");
  const proxyUrl = `http://127.0.0.1:${proxy.port}`;
  const source = metadataIdentity("example-audience", {
    [METADATA_HOST_VAR]: metadata.host,
    HTTP_PROXY: proxyUrl,
    http_proxy: proxyUrl,
    HTTPS_PROXY: proxyUrl,
    ALL_PROXY: proxyUrl,
    NODE_USE_ENV_PROXY: "1",
  } as NodeJS.ProcessEnv);

  // The reader takes its environment from the caller, so set the process's variables too: a
  // runtime that reads them reads them from there.
  const before = { ...process.env };
  process.env.HTTP_PROXY = proxyUrl;
  process.env.http_proxy = proxyUrl;
  process.env.HTTPS_PROXY = proxyUrl;
  process.env.ALL_PROXY = proxyUrl;
  process.env.NODE_USE_ENV_PROXY = "1";
  try {
    expect((await source.read()).token).toBe("machine.identity.token");
  } finally {
    for (const name of [
      "HTTP_PROXY",
      "http_proxy",
      "HTTPS_PROXY",
      "ALL_PROXY",
      "NODE_USE_ENV_PROXY",
    ]) {
      if (before[name] === undefined) delete process.env[name];
      else process.env[name] = before[name];
    }
  }
  expect(taken).toEqual([]);
  expect(metadata.asked.length).toBe(1);
});
