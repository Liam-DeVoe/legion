import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import type { LiveClientFrame, LiveServerFrame } from "../web/src/api/types";
import {
  baseUrl,
  createAsk,
  createIssue,
  createProject,
  e2eAgentToken,
  getAsk,
  getIssueArtifactText,
  listComments,
} from "./api";
import { seedDeliveryFixture } from "./delivery-seed";
import { connectedDot, documentEditor, typeAtEnd } from "./editor";
import {
  apiPinCookie,
  binaryB,
  type DispatchProcess,
  dispatchProcesses,
  entryChunkOf,
  pagePinCookie,
  pinnedTo,
  webDistOf,
} from "./harness-processes";
import { sql } from "./psql";
import type { ProxiedRequest, ProxiedSocket } from "./round-robin-proxy";
import { type Browser, type BrowserContext, expect, type Page, test, type WebSocket } from "./test";
import { signIn } from "./users";

/**
 * A rolling release runs two builds of Dispatch at once behind one load balancer, so a page one
 * build served talks to the other build's server. Here a browser context's page requests (its
 * navigations, `index.html`, `/assets/*`) are pinned to one process and its API, event stream,
 * websockets and `/auth` to the other (the proxy's `dispatch-e2e-page` and `dispatch-e2e-api`
 * cookies, e2e/round-robin-proxy.ts). With `DISPATCH_E2E_BINARY_B` naming a previous release's
 * envoy-dispatch and `DISPATCH_E2E_WEB_DIST_B` its web dist, Dispatch B is that release, so each
 * direction runs one build's page against the other build's server; without it both processes run
 * this tree. In each direction the page signs in, opens a project's issue list and an issue, posts
 * a comment, answers an ask, opens the issue's document - where that server admits the page
 * writable it types and sees the edit saved and shown in a context of the other build's page, and
 * where it admits the page read-only the page says so and the stored document is untouched
 * (`pageWritable`) - and opens the Delivery page, whose live socket subscribes to the timeline and
 * is answered a snapshot. Every selector is one both builds' own browser suites use. Each
 * direction then reads the entry chunk its page loaded and holds it to the one that build's own
 * `index.html` names (`entryChunkOf`), so a direction whose server is the previous release but
 * whose page this tree served is a failure here rather than a row that passes unnoticed.
 *
 * Each seed call names the process it runs on (`process` in e2e/api.ts, which pins its sign-in
 * there too), so the build that wrote a row and the build that reads it back are both known.
 */

/** What a message calls each process's build. */
const builds: Record<DispatchProcess, string> = {
  a: "this tree's build (Dispatch A)",
  b: binaryB === undefined ? "this tree's build (Dispatch B)" : "the previous build (Dispatch B)",
};

const seedAgent = { kind: "session", id: "e2e-cross-build" } as const;

/** packages/envoy, and the directory build-dispatch-build.sh wrote the previous build into: the
 *  binary's own, which also holds that build's `pmdoc/schema/blocks.json`. Its `web/` dist is
 *  `webDistOf`'s to name, which this spec and e2e/two-builds.multiprocess.e2e.ts share. */
const envoyDir = fileURLToPath(new URL("../../envoy", import.meta.url));
const buildB = binaryB === undefined ? undefined : dirname(binaryB);

/** A build's schema manifest (`pmdoc/schema/blocks.json`): the version its server reads, and
 *  whether it names its nodes. A build names them from the one that admits a page over the fleet
 *  on (#2461, `legion-envoy-v30.21.0`), and a build before that names none (the two-build Go
 *  harness's `admittedScope`, packages/envoy/cmd/dispatch/twobuild_documents_test.go, tells the
 *  two apart the same way). */
interface BuildManifest {
  readonly namesNodes: boolean;
  readonly version: number;
}

function manifestOf(process: DispatchProcess): BuildManifest {
  const path =
    process === "b" && buildB !== undefined
      ? resolve(buildB, "pmdoc", "schema", "blocks.json")
      : resolve(envoyDir, "internal", "dispatch", "pmdoc", "schema", "blocks.json");
  const manifest: unknown = JSON.parse(readFileSync(path, "utf8"));
  if (
    manifest === null ||
    typeof manifest !== "object" ||
    !("version" in manifest) ||
    typeof manifest.version !== "number"
  ) {
    throw new Error(`${path} declares no schema version`);
  }
  const namesNodes =
    "nodes" in manifest && Array.isArray(manifest.nodes) && manifest.nodes.length > 0;
  return { namesNodes, version: manifest.version };
}

/** The schema a page presented on a document socket: the `schema_version` and `schema_creatable`
 *  its provider puts in the socket's query (web/src/features/doc/connection.ts). A page of a
 *  build before `schema_creatable` sends `schema_version` alone, which is the version of the
 *  server it talks to when its page carries no manifest of its own, and creates up to 1: every
 *  element of a manifest before it has since 1. */
interface PresentedSchema {
  readonly creatable: number;
  readonly version: number;
}

/** The address of each document socket the page opens, in the order the page opens them. */
function watchDocumentSockets(page: Page): URL[] {
  const opened: URL[] = [];
  page.on("websocket", (socket: WebSocket) => {
    const url = new URL(socket.url());
    if (url.pathname.startsWith("/ws/doc/")) opened.push(url);
  });
  return opened;
}

/** The schema a document socket's address presents. */
function presentedBy(socket: URL): PresentedSchema {
  const version = Number(socket.searchParams.get("schema_version") ?? Number.NaN);
  const creatable = Number(socket.searchParams.get("schema_creatable") ?? 1);
  if (!Number.isInteger(version) || !Number.isInteger(creatable)) {
    throw new Error(`the page opened a document socket presenting no schema numbers: ${socket}`);
  }
  return { creatable, version };
}

/** What the document socket's admission reads off the registry (`docs.readSchemaFleet`): the
 *  lowest `schema_version` and the highest `schema_creatable` among the live rows of
 *  `server_processes`, those whose heartbeat is inside the registry's live window (15 seconds,
 *  `processes.LiveWindow`). A process of a build before those columns registers neither, and the
 *  zeros its row holds are what it counts as. */
interface FleetNumbers {
  readonly maxCreatable: number;
  readonly minVersion: number;
}

async function liveFleet(): Promise<FleetNumbers> {
  const read = await sql(
    `SELECT count(*) || ' ' || coalesce(min(schema_version), 0) || ' ' || coalesce(max(schema_creatable), 0)
       FROM server_processes WHERE heartbeat_at > now() - interval '15 seconds'`
  );
  const [live, minVersion, maxCreatable] = read.split(" ").map(Number);
  if (live === undefined || minVersion === undefined || maxCreatable === undefined) {
    throw new Error(`the registry's live rows read as ${JSON.stringify(read)}`);
  }
  // Both servers of this run register when they start, so an empty registry is a broken run, and
  // numbers read off it would hold every page read-only for a reason no build has.
  if (live === 0) throw new Error("the registry holds no live server process");
  return { maxCreatable, minVersion };
}

/**
 * Whether `server` admits a page that presented `presented` writable, and the numbers that say
 * so. The rule is stated here, from the numbers the registry and the page hold, and not read
 * from either server, so this spec cannot inherit a fault in the rule it checks.
 *
 * A server of a build that admits over the fleet (its manifest names its nodes: this tree's, and
 * every release from #2461 on) admits the page writable when the page creates no more than every
 * live process reads and no more than the highest any of them creates, and reads at least what
 * they create and what the document holds: a heading and a paragraph, since 1 in every manifest.
 * While a process of a build before the fleet columns is live, the lowest version is 0, which no
 * page creates at or under, so such a server admits every page read-only; a previous build that
 * registers numbers the page is inside leaves it writable.
 *
 * A server of a build before that knows no fleet: it compares the page's `schema_version` with its
 * own manifest version exactly (`schemaReadOnly` in that build), so it admits a page of its own
 * version, and holds a page of any other version read-only.
 *
 * With no previous build - both processes run this tree - the fleet is two processes of one
 * build, and a page of that build is writable on either.
 */
async function pageWritable(
  server: DispatchProcess,
  presented: PresentedSchema
): Promise<{ readonly why: string; readonly writable: boolean }> {
  const page = `schema_version ${presented.version}, schema_creatable ${presented.creatable}`;
  const manifest = manifestOf(server);
  if (!manifest.namesNodes) {
    const writable = presented.version === manifest.version;
    return {
      why: `${builds[server]} knows no fleet and reads schema version ${manifest.version}, so it admits the page (${page}) ${writable ? "writable" : "read-only"}`,
      writable,
    };
  }
  const fleet = await liveFleet();
  const writable =
    presented.creatable <= Math.min(fleet.minVersion, fleet.maxCreatable) &&
    presented.version >= Math.max(fleet.maxCreatable, 1);
  return {
    why: `the registry's live rows read lowest schema_version ${fleet.minVersion} and highest schema_creatable ${fleet.maxCreatable}, so ${builds[server]} admits the page (${page}) ${writable ? "writable" : "read-only"}`,
    writable,
  };
}

/** The day `seedDeliveryFixture` writes its timeline on. */
const deliveryWindow = "from=2024-06-01T00%3A00%3A00Z&to=2024-06-02T00%3A00%3A00Z";

// The database reset is the `test` this file imports (e2e/test.ts); the round-robin proxy's log
// is not in the database, so it is cleared here.
test.beforeEach(async () => {
  expect((await fetch(new URL("/_proxy/reset", baseUrl), { method: "POST" })).status).toBe(204);
});

/** A signed-in context whose page comes from `page` and whose every other request goes to `api`. */
async function splitContext(
  browser: Browser,
  page: DispatchProcess,
  api: DispatchProcess
): Promise<BrowserContext> {
  const context = await browser.newContext();
  await context.addCookies([
    { name: pagePinCookie, url: baseUrl, value: page },
    { name: apiPinCookie, url: baseUrl, value: api },
  ]);
  await signIn(context, "alice");
  return context;
}

/** The frames the page's live socket sends and receives, from the moment it opens. Both builds
 *  run every live subscription of a page over one `/ws/live` socket, with the same frames
 *  (web/src/api/live-socket.ts). */
function watchLiveSocket(page: Page): { sent: LiveClientFrame[]; received: LiveServerFrame[] } {
  const frames = { received: [] as LiveServerFrame[], sent: [] as LiveClientFrame[] };
  page.on("websocket", (socket: WebSocket) => {
    if (new URL(socket.url()).pathname !== "/ws/live") return;
    socket.on("framesent", ({ payload }) => {
      frames.sent.push(JSON.parse(String(payload)) as LiveClientFrame);
    });
    socket.on("framereceived", ({ payload }) => {
      frames.received.push(JSON.parse(String(payload)) as LiveServerFrame);
    });
  });
  return frames;
}

for (const pageProcess of dispatchProcesses) {
  const server: DispatchProcess = pageProcess === "a" ? "b" : "a";
  test(`a page of ${builds[pageProcess]} works against the server of ${builds[server]}`, async ({
    browser,
  }) => {
    test.setTimeout(120_000);
    const on = { process: server };
    await createProject({ key: "XB", name: "Cross build" }, on);
    const issue = await createIssue(
      { project: "XB", spec: "# Cross build\n\nWritten before the edit.\n", title: "Two builds" },
      on
    );
    const ask = await createAsk(
      issue.key,
      { options: [{ label: "Ship" }, { label: "Hold" }], question: "Ship it?" },
      { ...on, actor: seedAgent, as: "agent" }
    );
    // The Delivery page's timeline, written in Postgres: the live socket answers its timeline
    // subscription with a snapshot only once delivery is configured.
    await seedDeliveryFixture();

    const context = await splitContext(browser, pageProcess, server);
    try {
      const page = await context.newPage();
      const live = watchLiveSocket(page);
      const documentSockets = watchDocumentSockets(page);

      // The project's issue list, and the issue from it. Each build's list renders exactly one
      // link carrying `data-issue-link` per issue, and that is what this names: a list row of a
      // build before #2988 is itself that link, and the table of a build from #2988 on gives it
      // the title cell beside a second link holding the key alone, which a name read of the key
      // would match as well.
      await page.goto("/projects/XB/issues");
      await page.locator(`a[data-issue-link][href="/issues/${issue.key}"]`).click();
      await expect(documentEditor(page)).toContainText("Written before the edit.");

      // A comment, which the other build's server lists.
      await page.goto(`/issues/${issue.key}/conversation`);
      const comment = `A comment from ${builds[pageProcess]}'s page`;
      const message = page
        .getByRole("form", { name: "Comment composer" })
        .getByRole("textbox", { name: "Comment" });
      await message.fill(comment);
      await message.press("Control+Enter");
      await expect(
        page.getByRole("list", { name: "Conversation turns" }).getByText(comment)
      ).toBeVisible();
      await expect
        .poll(async () =>
          (await listComments(issue.key, undefined, { process: pageProcess })).map(
            (written) => written.body
          )
        )
        .toContain(comment);

      // The ask, answered in place, which the other build's server reads as answered.
      const card = page.getByRole("region", { name: "Conversation" }).getByTestId(`ask-${ask.id}`);
      await card.getByRole("radio", { name: "Ship" }).check();
      await card.getByRole("button", { exact: true, name: "Answer" }).click();
      await expect(
        card.getByRole("list", { name: "Options" }).locator('li[data-selected="true"]')
      ).toContainText("Ship");
      await expect
        .poll(async () => ((await getAsk(ask.id, { process: pageProcess })).answers ?? []).length)
        .toBe(1);

      // The issue's document. Where the other build's server admits this page writable
      // (pageWritable, from the numbers the page presented and the registry holds), an edit typed
      // here is saved by that server, then read from the store by this page's own server, and
      // shown below in a context of the other build's page. Where it admits the page read-only -
      // a page whose version a server that knows no fleet does not read as its own, or a page
      // outside the numbers the live processes register, as every page is on a server of this
      // tree while a process of a build before the fleet columns is live - the page says so,
      // takes no edit, and the stored document is untouched.
      await page.goto(`/issues/${issue.key}/spec`);
      await expect(documentEditor(page)).toContainText("Written before the edit.");
      await expect(connectedDot(page)).toHaveText("connected");
      const documentSocket = documentSockets.at(-1);
      if (documentSocket === undefined) throw new Error("the page opened no document socket");
      const { why, writable } = await pageWritable(server, presentedBy(documentSocket));
      console.log(`CROSS-BUILD ${builds[pageProcess]}'s page: ${why}`);
      const edit = `Typed on ${builds[pageProcess]}'s page.`;
      if (writable) {
        await typeAtEnd(page, edit);
      } else {
        await expect(page.locator('.dispatch-doc[data-read-only="true"]'), why).toBeVisible();
      }
      for (const reader of [server, pageProcess]) {
        await expect
          .poll(
            async () =>
              (await getIssueArtifactText(issue.key, "spec", { process: reader })).markdown,
            { message: why, timeout: 30_000 }
          )
          .toContain(writable ? edit : "Written before the edit.");
        if (!writable) {
          const { markdown } = await getIssueArtifactText(issue.key, "spec", { process: reader });
          expect(markdown, why).not.toContain(edit);
        }
      }

      // The Delivery page on the fixture's day: the page's live socket subscribes to the timeline
      // view, and the other build's server answers that subscription with a snapshot.
      await page.goto(`/delivery?${deliveryWindow}`);
      await expect(page.getByRole("heading", { name: "Delivery timeline" })).toBeVisible();
      await expect
        .poll(() =>
          live.sent.flatMap((frame) => (frame.type === "subscribe" ? [frame.view.kind] : []))
        )
        .toContain("timeline");
      await expect
        .poll(() => {
          const timeline = live.sent.flatMap((frame) =>
            frame.type === "subscribe" && frame.view.kind === "timeline" ? [frame.sub] : []
          );
          return live.received.some(
            (frame) => frame.type === "snapshot" && timeline.includes(frame.sub)
          );
        })
        .toBe(true);

      // The split held: every asset came from the page's process, and the document and live
      // sockets went to the other. Read before the other build's context opens a page of its own.
      const assets = (
        (await (await fetch(new URL("/_proxy/requests", baseUrl))).json()) as ProxiedRequest[]
      ).filter((request) => request.path.startsWith("/assets/"));
      expect(assets.length).toBeGreaterThan(0);
      expect(assets.filter((request) => request.upstream !== pageProcess)).toEqual([]);
      const sockets = (await (
        await fetch(new URL("/_proxy/sockets", baseUrl))
      ).json()) as ProxiedSocket[];
      for (const path of ["/ws/doc/", "/ws/live"]) {
        const opened = sockets.filter((socket) => socket.path.startsWith(path));
        expect(opened.length, `a ${path} socket opened`).toBeGreaterThan(0);
        expect(opened.filter((socket) => socket.upstream !== server)).toEqual([]);
      }

      // And the page this whole row ran on is that process's own build's: the entry chunk the
      // document it loaded names is the one `index.html` of that build names. Nothing above can
      // tell one build's page from the other's - a page of this tree drives every row here
      // against the previous build's server just as B's own page does - so this is what holds
      // B's page to B's binary, which is read from the `web/` beside that binary and not from
      // the `DISPATCH_E2E_WEB_DIST_B` the harness was handed: a run that served B this tree's
      // page while running the previous release's server fails here, as a dropped default for
      // that variable in packages/dispatch/package.json made it do. Where there is no previous
      // build, B's dist is whatever the harness serves it (`webDistOf`, which `bun run
      // e2e:multiprocess` points at this tree's own `web/dist-b`, a second build of this source
      // under another build id, and which is A's dist when nothing names one): the assertion then
      // says the page came from the process it was pinned to and nothing about a second build.
      const loaded = await page
        .locator('head > script[type="module"][src^="/assets/"]')
        .getAttribute("src");
      expect(loaded, `the page was served by ${builds[pageProcess]}`).toBe(
        entryChunkOf(webDistOf(pageProcess))
      );

      // The document, in a context of the other build's page against this page's own server: the
      // edit where this page could make one, and the text it opened with where it could not.
      const otherBuild = await splitContext(browser, server, pageProcess);
      try {
        const reading = await otherBuild.newPage();
        await reading.goto(`/issues/${issue.key}/spec`);
        await expect(documentEditor(reading)).toContainText(
          writable ? edit : "Written before the edit."
        );
      } finally {
        await otherBuild.close();
      }
    } finally {
      await context.close();
    }
  });
}

// A newer build writes an element this one does not know into a document both serve. Each page
// opening it says so, and offers no upload to replace it: a replacement deletes that element.
test("a document holding newer content shows that state and no replace drop zone", async ({
  browser,
}) => {
  test.skip(
    binaryB !== undefined,
    "the newer-content hook is this tree's; Dispatch B runs the previous build (DISPATCH_E2E_BINARY_B)"
  );
  await createProject({ key: "NEW", name: "Newer content" }, { process: "a" });
  const issue = await createIssue(
    { project: "NEW", spec: "Written before the newer element.\n", title: "Newer content" },
    { process: "a" }
  );
  // A /_test/ route goes to both processes (e2e/round-robin-proxy.ts).
  const injected = await fetch(
    new URL(`/api/v1/artifacts/${issue.primary_artifact_id}/_test/newer-content`, baseUrl),
    { headers: { Authorization: `Bearer ${e2eAgentToken}` }, method: "POST" }
  );
  expect(injected.status, await injected.text()).toBe(204);
  for (const target of dispatchProcesses) {
    const text = await fetch(
      new URL(`/api/v1/artifacts/${issue.primary_artifact_id}/text`, baseUrl),
      { headers: { Authorization: `Bearer ${e2eAgentToken}`, ...pinnedTo(target) } }
    );
    expect(text.status).toBe(409);
    expect(await text.text()).toContain('"code":"DOC_NEWER_CONTENT"');
  }

  for (const target of dispatchProcesses) {
    const context = await splitContext(browser, target, target);
    try {
      const page = await context.newPage();
      await page.goto(`/issues/${issue.key}/spec`);
      const editor = page.getByRole("region", { name: "Document editor" });
      await expect(
        editor.getByRole("status").filter({
          hasText: "This document holds content this version of Dispatch cannot show yet.",
        })
      ).toBeVisible();
      await expect(
        editor.getByText("Upload markdown to replace and repair this document.")
      ).toHaveCount(0);
      await expect(editor.getByLabel("Upload artifact")).toHaveCount(0);
      await expect(documentEditor(page)).toBeHidden();
    } finally {
      await context.close();
    }
  }
});
