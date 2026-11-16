// The Legion view on the project Issues page, its settings control, the Settings page's own
// Legion daemon field, the live push, `/pipeline` and the Delivery page's chart controls
// (dispatch://INFRA-1077, acceptance A1). Every spec is tagged `@legion`, so `--grep @legion`
// runs this file's work and nothing else.
//
// The board reads only what Dispatch recorded of the project's daemon, so the fixture writes those
// rows (`legion-seed.ts`); the settings control is the one place that does call the daemon, through
// Dispatch's relay, so that spec stands a fake daemon up and reads what reached it.
import { baseUrl, userHeaders } from "./api";
import { PIPELINE_SECTION_URL, seedPipelineFixture } from "./delivery-seed";
import {
  LEGION_PROJECT,
  pushLegionObservation,
  seedLegionFixture,
  seedLegionProject,
  startFakeLegionDaemon,
} from "./legion-seed";
import { expect, test } from "./test";
import { asUser } from "./users";

/** The ten columns, in the order the server sends them and the view draws them. */
const COLUMNS = [
  "queue",
  "design",
  "planning",
  "implementing",
  "testing",
  "reviewing",
  "accepting",
  "merging",
  "awaiting_person",
  "held",
];

test("the Legion view shows the daemon's columns and its cards @legion", async ({ browser }) => {
  const daemon = await startFakeLegionDaemon({});
  await seedLegionFixture(daemon.url);
  const context = await asUser(browser, "alice");
  const page = await context.newPage();

  try {
    await page.goto(`/projects/${LEGION_PROJECT}`);
    await page.getByRole("button", { name: "Legion" }).click();

    const view = page.getByRole("region", { name: "Legion" });
    await expect(view).toBeVisible();
    // Every column, in the server's order, each with the label the server gave it.
    await expect(view.locator("[data-legion-column]")).toHaveCount(COLUMNS.length);
    expect(
      await view
        .locator("[data-legion-column]")
        .evaluateAll((columns) =>
          columns.map((column) => column.getAttribute("data-legion-column"))
        )
    ).toEqual(COLUMNS);
    await expect(page.getByRole("region", { name: "Waiting on a person" })).toBeVisible();

    // The header: two of the four slots held, and the daemon's waiting line.
    await expect(view.locator("[data-legion-slots]")).toHaveText("2 of 4 slots in use");
    await expect(view.locator("[data-legion-queue-length]")).toHaveText("3 waiting in the queue");

    // The implementing card, with every fact the spec names on it.
    const implementing = page.getByRole("region", { name: "Implementing" });
    const card = implementing.locator('[data-legion-card="ACME-1"]');
    await expect(card).toBeVisible();
    await expect(card).toContainText("Ship the widgets");
    await expect(card).toContainText("P0");
    await expect(card.locator('[data-legion-fact="phase-time"]')).toContainText("2 h");
    await expect(card.locator('[data-legion-fact="slot"]')).toContainText("0");
    await expect(card.locator('[data-legion-fact="roles"]')).toContainText("implementer");
    await expect(card.locator('[data-legion-fact="checks"]')).toContainText("failing");

    // The queue keeps the daemon's own order: ACME-4 before ACME-3, then the blocked root, which
    // names what holds it. Neither key order nor priority order would give this.
    const queue = page.getByRole("region", { name: "Queue" });
    expect(
      await queue
        .locator("[data-legion-card]")
        .evaluateAll((cards) => cards.map((card) => card.getAttribute("data-legion-card")))
    ).toEqual(["ACME-4", "ACME-3", "ACME-5"]);
    await expect(
      queue.locator('[data-legion-card="ACME-5"] [data-legion-fact="blocked-by"]')
    ).toContainText("Blocked by ACME-1");

    // What a root waits on from a person, on its own card.
    await expect(
      page
        .getByRole("region", { name: "Waiting on a person" })
        .locator('[data-legion-card="ACME-2"] [data-legion-fact="waiting-on"]')
    ).toContainText("Waiting on ask");
  } finally {
    await context.close();
    await daemon.close();
  }
});

test("the settings control changes the admission cap of a daemon @legion", async ({ browser }) => {
  const daemon = await startFakeLegionDaemon({ admission_cap: { applied: "at-once" } });
  await seedLegionFixture(daemon.url);
  const context = await asUser(browser, "alice");
  const page = await context.newPage();

  try {
    await page.goto(`/projects/${LEGION_PROJECT}`);
    await page.getByRole("button", { name: "Legion" }).click();

    // Each setting with its value and where it came from, as the daemon reports them.
    const cap = page.locator('[data-legion-setting="admission_cap"]');
    await expect(cap.locator("[data-legion-setting-value]")).toHaveText("4");
    await expect(cap.locator('[data-legion-setting-source="file"]')).toContainText(
      "configuration file"
    );
    await expect(
      page.locator('[data-legion-setting="review_round_cap"] [data-legion-setting-source="route"]')
    ).toContainText("Changed here by bob");

    await page.getByLabel("New value for admission_cap").fill("6");
    await page.getByRole("button", { name: "Change admission_cap" }).click();

    // What the daemon said about the key, and the new value with the route as its source and the
    // person who changed it.
    await expect(cap.locator("[data-legion-setting-result]")).toHaveText("Applied at once");
    await expect(cap.locator("[data-legion-setting-value]")).toHaveText("6");
    await expect(cap.locator('[data-legion-setting-source="route"]')).toContainText(
      "Changed here by alice"
    );

    // Dispatch relayed the change itself, naming the person who asked for it.
    expect(daemon.changes).toEqual([{ by: "alice", changes: { admission_cap: 6 } }]);
  } finally {
    await context.close();
    await daemon.close();
  }
});

// The daemon's third verdict, the one a person must not read as a success: a key it took into
// its own settings and could not carry into the thing that holds a copy of the value
// (`failed`, the daemon's internal/api/settings_route.go). The row's answer is announced
// (`role="alert"`, on the span RowAnswer renders), reads the holder's own error, and the value
// stays the new one, because the daemon's settings carry it and only a client, listener or
// connection kept the old.
//
// It drives `admission_cap`: the fixture seeds that key and `review_round_cap` and no other
// (`legion-seed.ts`), and the panel reads the verdict alone — `describeVerdict` keys on nothing
// about the path. The verdict is the shape a real failure has, the daemon's own
// `worker_rpc_timeout_seconds`: `holder` is the row's named holder, while the holder that kept
// the old value is named inside `failed`, which the daemon writes as `<holder>: <error>`
// (internal/daemon/rebuilder.go's observe). So the row proves the other half too — the text
// names what the daemon's text names, never the row's holder.
test("the panel shows a failed verdict as a failure and keeps the new value @legion", async ({
  browser,
}) => {
  const daemon = await startFakeLegionDaemon({
    admission_cap: {
      failed: "nats: reopen the intake consumers: nats: no servers available for connection",
      holder: "worker-stream",
      old: "4",
      new: "6",
    },
  });
  await seedLegionFixture(daemon.url);
  const context = await asUser(browser, "alice");
  const page = await context.newPage();

  try {
    await page.goto(`/projects/${LEGION_PROJECT}`);
    await page.getByRole("button", { name: "Legion" }).click();
    const cap = page.locator('[data-legion-setting="admission_cap"]');
    await expect(cap.locator("[data-legion-setting-value]")).toHaveText("4");

    await page.getByLabel("New value for admission_cap").fill("6");
    await page.getByRole("button", { name: "Change admission_cap" }).click();

    const answer = cap.locator("[data-legion-setting-result]");
    const failed =
      "The daemon took the new value, and what holds it kept the old one: " +
      "nats: reopen the intake consumers: nats: no servers available for connection";
    await expect(answer).toHaveText(failed);
    // Announced as a failure, and naming no holder the daemon's own text did not name.
    await expect(answer.locator('[role="alert"]')).toHaveText(failed);
    await expect(answer).not.toContainText("worker-stream");
    // The daemon took the value, so the row shows the new one rather than rolling back to 4.
    await expect(cap.locator("[data-legion-setting-value]")).toHaveText("6");
  } finally {
    await context.close();
    await daemon.close();
  }
});

// A1: "a daemon URL set on the Settings page that brings the view", and A4's "A project with no
// Legion setting shows no Legion view". The project is seeded with its issues and its daemon's
// observations but no `legion_settings` row, so the only thing that can bring the view is the
// person naming the daemon on the Settings page, beside the Delivery settings, as the Design's
// "Where the data comes from" says.
test("a project with no Legion setting has no Legion view until the Settings page names its daemon @legion", async ({
  browser,
}) => {
  const daemon = await startFakeLegionDaemon({});
  await seedLegionProject();
  const context = await asUser(browser, "alice");
  const page = await context.newPage();

  try {
    await page.goto(`/projects/${LEGION_PROJECT}`);
    await expect(page.getByRole("button", { name: "Board" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Legion" })).toHaveCount(0);

    // The Settings page, beside the Delivery settings.
    await page.goto("/settings");
    await expect(page.getByRole("heading", { name: "Legion" })).toBeVisible();
    await page.getByLabel("Legion daemon state URL").fill(daemon.url);
    await page
      .getByRole("combobox", { exact: true, name: "Legion project" })
      .selectOption(LEGION_PROJECT);
    const saved = page.waitForResponse(
      (response) =>
        response.request().method() === "PUT" &&
        response.url().endsWith(`/api/v1/settings/legion/${LEGION_PROJECT}`)
    );
    await page.getByRole("button", { name: "Save Legion daemon" }).click();
    expect((await saved).status()).toBe(200);

    // The view the setting brought.
    await page.goto(`/projects/${LEGION_PROJECT}`);
    await page.getByRole("button", { name: "Legion" }).click();
    await expect(page.getByRole("region", { name: "Legion" })).toBeVisible();
    await expect(
      page.getByRole("region", { name: "Implementing" }).locator('[data-legion-card="ACME-1"]')
    ).toBeVisible();
  } finally {
    await context.close();
    await daemon.close();
  }
});

// The other half of the Settings control: the URL it refuses, and the Remove that takes the
// view away again. A state URL that is not an absolute http(s) one with a host and no path is
// refused, and the page's own wording must be the server's own, since the page carries a second
// copy of the rule (`legionStateUrl` in LegionDaemonsSection.tsx) and only the server's copy
// decides what is stored.
test("the Settings page refuses a state URL that is not a daemon's, and its Remove takes the view away @legion", async ({
  browser,
}) => {
  const daemon = await startFakeLegionDaemon({});
  await seedLegionFixture(daemon.url);
  const context = await asUser(browser, "alice");
  const page = await context.newPage();
  const bad = `${daemon.url}/legion/v1/state`;

  try {
    // What the server says about this URL, so the page can be held to its wording.
    const refusal = await fetch(new URL(`/api/v1/settings/legion/${LEGION_PROJECT}`, baseUrl), {
      body: JSON.stringify({ state_url: bad }),
      headers: { "Content-Type": "application/json", ...(await userHeaders("alice")) },
      method: "PUT",
    });
    expect(refusal.status).toBe(400);
    const refused = (await refusal.json()) as { code: string; error: string };
    expect(refused.code).toBe("LEGION_SETTING_INPUT");

    await page.goto("/settings");
    const section = page.locator("[data-legion-daemons]");
    await expect(section).toContainText(daemon.url);

    // The page refuses the same URL, in the server's own words, and sends no write at all.
    let wrote = false;
    page.on("request", (request) => {
      if (
        request.method() === "PUT" &&
        request.url().endsWith(`/api/v1/settings/legion/${LEGION_PROJECT}`)
      ) {
        wrote = true;
      }
    });
    await page.getByLabel("Legion daemon state URL").fill(bad);
    await page
      .getByRole("combobox", { exact: true, name: "Legion project" })
      .selectOption(LEGION_PROJECT);
    await page.getByRole("button", { name: "Save Legion daemon" }).click();
    await expect(page.getByRole("alert")).toHaveText(refused.error);
    expect(wrote).toBe(false);
    // The refusal changed nothing: the project still reads the daemon it did.
    await expect(section).toContainText(daemon.url);

    // Remove stops the reading, and the Issues page loses its Legion view.
    const removed = page.waitForResponse(
      (response) =>
        response.request().method() === "DELETE" &&
        response.url().endsWith(`/api/v1/settings/legion/${LEGION_PROJECT}`)
    );
    await page
      .getByRole("button", { name: `Remove the Legion daemon of ${LEGION_PROJECT}` })
      .click();
    expect((await removed).status()).toBeLessThan(300);

    await page.goto(`/projects/${LEGION_PROJECT}`);
    await expect(page.getByRole("button", { name: "Board" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Legion" })).toHaveCount(0);
  } finally {
    await context.close();
    await daemon.close();
  }
});

// A1: "a `/ws/live` push that moves a card with no reload". A read of the daemon writes the
// observations it found and announces itself on `legion.StateChannel`; the open board recomputes
// and the card moves with no navigation of any kind.
test("a live push moves a card between columns with no reload @legion", async ({ browser }) => {
  const daemon = await startFakeLegionDaemon({});
  await seedLegionFixture(daemon.url);
  const context = await asUser(browser, "alice");
  const page = await context.newPage();

  try {
    await page.goto(`/projects/${LEGION_PROJECT}`);
    await page.getByRole("button", { name: "Legion" }).click();
    const implementing = page.getByRole("region", { name: "Implementing" });
    const testing = page.getByRole("region", { name: "Testing" });
    await expect(implementing.locator('[data-legion-card="ACME-1"]')).toBeVisible();
    await expect(testing.locator('[data-legion-card="ACME-1"]')).toHaveCount(0);

    // What a read of the daemon a minute later would have left, and the announcement it makes.
    await pushLegionObservation("ACME-1", "phase", '"testing"');

    // No reload: the card moves under the open page.
    await expect(testing.locator('[data-legion-card="ACME-1"]')).toBeVisible({ timeout: 15000 });
    await expect(implementing.locator('[data-legion-card="ACME-1"]')).toHaveCount(0);
  } finally {
    await context.close();
    await daemon.close();
  }
});

// A1: "`/pipeline` reaching the Pipeline section".
test("/pipeline reaches the Delivery page's Pipeline section @legion", async ({ browser }) => {
  await seedPipelineFixture();
  const context = await asUser(browser, "alice");
  const page = await context.newPage();

  try {
    await page.goto("/pipeline");
    await expect(page).toHaveURL(/\/delivery#pipeline$/);
    const pipeline = page.locator("#pipeline");
    await expect(pipeline).toBeVisible();
    await expect(page.getByRole("heading", { name: /Pipeline/ })).toBeVisible();
  } finally {
    await context.close();
  }
});

// A1: "every chart on the Delivery page switching to its table, back to its graph, and to a
// compare range". Every chart the page draws is read off the page itself, so a chart added later
// is covered without this spec being edited.
test("every chart on the Delivery page shows its table, its graph again and a compare range @legion", async ({
  browser,
}) => {
  await seedPipelineFixture();
  const context = await asUser(browser, "alice");
  const page = await context.newPage();
  try {
    await page.goto(PIPELINE_SECTION_URL);
    await expect(page.getByRole("heading", { name: "Delivery timeline" })).toBeVisible();
    const labels = await page
      .locator("[data-chart]")
      .evaluateAll((charts) => charts.map((chart) => chart.getAttribute("data-chart") ?? ""));
    expect(labels.length).toBeGreaterThan(0);

    for (const label of labels) {
      const chart = page.locator(`[data-chart="${label}"]`);
      const view = chart.getByRole("group", { name: `${label} view` });
      await view.getByRole("button", { name: "Table" }).click();
      await expect(chart.locator(`[data-chart-table="${label}"]`)).toBeVisible();
      await view.getByRole("button", { name: "Graph" }).click();
      await expect(chart.locator(`[data-chart-table="${label}"]`)).toHaveCount(0);
      await expect(chart.locator('[data-chart-range="window"]').first()).toBeVisible();

      // The compare control's default second range: the range just before the window.
      await chart.locator(`select[data-chart-compare="${label}"]`).selectOption("preceding");
      await expect(chart.locator('[data-chart-range="compare"]').first()).toBeVisible();
      await view.getByRole("button", { name: "Table" }).click();
      // The compare column's heading names what a row of every chart on this page is: a UTC day.
      await expect(
        chart.locator(`[data-chart-table="${label}"] th`, { hasText: /^Compare / }).first()
      ).toBeVisible();
      await view.getByRole("button", { name: "Graph" }).click();
    }
  } finally {
    await context.close();
  }
});
