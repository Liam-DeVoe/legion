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

/** A build's schema numbers: V, the schema version it reads, and R, the highest `since` among the
 *  elements it may create, both of the manifest it was built with (`pmdoc/schema/blocks.json`).
 *  A page presents its build's as `schema_version` and `schema_creatable` on a document socket
 *  (web/src/features/doc/connection.ts), and a process registers its build's in its
 *  `server_processes` row. */
interface SchemaNumbers {
  readonly version: number;
  readonly creatable: number;
}

/** The numbers a page presented on the document socket `url` names, read as a server reads them
 *  (`schemaPageOf` in packages/envoy/internal/dispatch/docs): a page that sends no
 *  `schema_creatable` is of a build before the manifest, whose R is 1. */
function presentedNumbers(url: string): SchemaNumbers {
  const query = new URL(url).searchParams;
  const version = query.get("schema_version");
  const creatable = query.get("schema_creatable") ?? "1";
  if (version === null || !/^\d+$/.test(version) || !/^\d+$/.test(creatable)) {
    throw new Error(`the page's document socket ${url} presents no schema numbers`);
  }
  return { version: Number(version), creatable: Number(creatable) };
}

/** One live process: the entry chunk of the dashboard build it registered, which names its build
 *  (`entryChunkOf`), and the schema numbers it registered. */
interface FleetProcess extends SchemaNumbers {
  readonly entry: string | null;
}

/** How long after its last heartbeat a registry row counts as a live process: `LiveWindow` of
 *  packages/envoy/internal/dispatch/processes, by which a server reads its fleet. */
const liveWindowSeconds = 15;

/** The fleet as a server reads it to admit a document socket (`docs.readSchemaFleet`): every
 *  `server_processes` row whose heartbeat is inside the live window, with its numbers. */
async function liveFleet(): Promise<FleetProcess[]> {
  const answer = await sql(
    `select coalesce(json_agg(json_build_object('entry', b.entry, 'version', p.schema_version,
       'creatable', p.schema_creatable) order by p.started_at), '[]')
     from server_processes p left join dashboard_builds b on b.build = p.dashboard_build
     where p.heartbeat_at > now() - make_interval(secs => ${liveWindowSeconds})`
  );
  return JSON.parse(answer) as FleetProcess[];
}

/** `numbers` as a message names them. */
const named = (numbers: SchemaNumbers): string => `V ${numbers.version}, R ${numbers.creatable}`;

/**
 * Refuses, naming the fleet's numbers and the page's, unless `fleet` holds a live process of each
 * build that registered its numbers. A fleet missing either build is not two builds at once, and a
 * process registering version 0 is of a build before #2461, whose server holds a page by its own
 * version alone (`schemaReadOnly` in that build) rather than by the rule `pageWritable` states: in
 * either case the row cannot say what its page is admitted as.
 */
function requireBothBuildsLive(fleet: readonly FleetProcess[], page: SchemaNumbers): void {
  const numbers =
    `live processes: ${fleet.map((process) => `${process.entry ?? "no dashboard"} (${named(process)})`).join("; ") || "none"}; ` +
    `the page presents ${named(page)}`;
  for (const process of dispatchProcesses) {
    const entry = entryChunkOf(webDistOf(process));
    const own = fleet.filter((live) => live.entry === entry);
    if (own.length === 0) {
      throw new Error(
        `the fleet holds no live process of ${builds[process]} (dashboard entry ${entry}), so it is not two builds at once; ${numbers}`
      );
    }
    if (own.some((live) => live.version === 0)) {
      throw new Error(
        `a live process of ${builds[process]} registers schema version 0, so it is of a build before #2461, whose server admits a page by its own version and not by the fleet's numbers; ${numbers}`
      );
    }
  }
}

/** The highest `since` among the elements the row's document holds: a heading and paragraphs,
 *  which every manifest reads from version 1. */
const documentSince = 1;

/**
 * Whether a page presenting `page` may write the row's document while `fleet` is live.
 *
 * This is the page admission both builds' servers run (`docs.schemaFleet.admits`, stated once in
 * packages/envoy/cmd/dispatch/README.md, "Two builds at once"): a page is writable when every
 * live process reads what it creates (R_page <= min V), some live process creates what it creates
 * (R_page <= max R), and it reads what every live process creates and what the document holds
 * (V_page >= max(max R, D)); otherwise it is read-only. Both servers read the one fleet, so the
 * answer is the same whichever build's server the page talks to.
 *
 * So the rule holds a page read-only only while a live process reads below the page's numbers or
 * creates above them. Every release from #2461 on registers its numbers, and at this writing this
 * tree's manifest and the previous build's (legion-envoy-v30.21.1) are both version 4 with R 4,
 * and each process registers 4 and 4: no page creates above what a live process reads, so the
 * rule admits both rows' pages writable. A release that raises the schema above the previous
 * build's makes both rows read-only by the same rule, and the row takes that branch then.
 */
function pageWritable(page: SchemaNumbers, fleet: readonly SchemaNumbers[]): boolean {
  const minVersion = Math.min(...fleet.map((process) => process.version));
  const maxCreatable = Math.max(...fleet.map((process) => process.creatable));
  return (
    page.creatable <= Math.min(minVersion, maxCreatable) &&
    page.version >= Math.max(maxCreatable, documentSince)
  );
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

      // The issue's document, admitted by the numbers the page presents on its document socket and
      // the fleet's, read here as the other build's server reads them (pageWritable). Where they
      // admit this page writable, an edit typed here is saved by that server, then read from the
      // store by this page's own server, and shown below in a context of the other build's page.
      // Where they hold it read-only - a live process reads below what the page creates, or
      // creates above what it reads - the page says so, takes no edit, and the stored document is
      // untouched.
      const documentSocket = page.waitForEvent("websocket", {
        predicate: (socket) => new URL(socket.url()).pathname.startsWith("/ws/doc/"),
      });
      await page.goto(`/issues/${issue.key}/spec`);
      await expect(documentEditor(page)).toContainText("Written before the edit.");
      await expect(connectedDot(page)).toHaveText("connected");
      const numbers = presentedNumbers((await documentSocket).url());
      const fleet = await liveFleet();
      requireBothBuildsLive(fleet, numbers);
      const writable = pageWritable(numbers, fleet);
      test.info().annotations.push({
        type: "schema admission",
        description: `${builds[pageProcess]}'s page (${named(numbers)}) is ${writable ? "writable" : "read-only"} beside ${fleet.map(named).join("; ")}`,
      });
      const edit = `Typed on ${builds[pageProcess]}'s page.`;
      if (writable) {
        await typeAtEnd(page, edit);
      } else {
        await expect(page.locator('.dispatch-doc[data-read-only="true"]')).toBeVisible();
      }
      for (const reader of [server, pageProcess]) {
        await expect
          .poll(
            async () =>
              (await getIssueArtifactText(issue.key, "spec", { process: reader })).markdown,
            { timeout: 30_000 }
          )
          .toContain(writable ? edit : "Written before the edit.");
        if (!writable) {
          const { markdown } = await getIssueArtifactText(issue.key, "spec", { process: reader });
          expect(markdown).not.toContain(edit);
        }
      }

      // The Delivery page on the fixture's day: the page's live socket subscribes to the timeline
      // view, and the other build's server answers that subscription with a snapshot, whatever
      // other view of the page it refuses on that socket: a server of a build before INFRA-1077
      // answers `INVALID_FRAME` to the Pipeline section's view of this tree's page, which names a
      // window (web/src/api/live-socket.ts).
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
