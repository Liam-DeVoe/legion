/** A document table whose cell names an issue with a long title, read for its width: the
 *  reference is drawn as the issue's title, and a table cell sizes its column to what it holds,
 *  so a reference cap that the cell cannot resolve would size the column to the whole title and
 *  scroll the table sideways. The layout rows let a table scroll inside its own box, so this row
 *  holds the table itself. It reads only what the page draws from its answers and its document
 *  socket, so it runs against its recording (packages/dispatch/README.md, "The stubbed layer");
 *  re-record after a change to the seed below or to what the page reads:
 *  `bun run e2e:stub:record -- document-table-reference.stub`. */
import { expect } from "@playwright/test";

import { createIssue, createProject } from "./api";
import { pinFontFaces } from "./layout-check";
import { routeLayoutSockets } from "./layout-sockets";
import { stubbed } from "./stub/stub-test";

const LONG_TITLE =
  "Local testing workflow for Dispatch, with every header property production holds, measured on production at every width from 360 to 1920 pixels";

const test = stubbed(async () => {
  await createProject({ key: "CORE", name: "Core" });
  const named = await createIssue({ project: "CORE", title: LONG_TITLE });
  const document = await createIssue({
    project: "CORE",
    spec: [
      "The steps of the plan, one per row.",
      "",
      "| Step | Issue | Note |",
      "| --- | --- | --- |",
      `| one | ${named.key} | short |`,
      "| two | none | short |",
    ].join("\n"),
    title: "A plan whose table names an issue",
  });
  return { document: document.key, named: named.key };
});

test.use({ locale: "en-US", timezoneId: "UTC" });

test("a document table whose cell names an issue with a long title is no wider than its column @pr", async ({
  page,
  scene,
}, testInfo) => {
  const keep = await routeLayoutSockets(page, testInfo);
  try {
    // A 900 px document column, beside the sidebar and the comment margin.
    await page.setViewportSize({ height: 900, width: 1652 });
    await page.goto(`/issues/${scene.document}/spec`);
    await pinFontFaces(page);
    const table = page.locator(".dispatch-doc .milkdown table").first();
    await expect(table).toBeVisible();
    const reference = table.locator(".dispatch-reference").first();
    await expect(reference).toContainText(LONG_TITLE.slice(0, 40));
    const measured = await table.evaluate((element) => {
      const column = element.parentElement?.getBoundingClientRect().width ?? 0;
      const title = element.querySelector(".dispatch-reference")?.getBoundingClientRect().width;
      return {
        clientWidth: element.clientWidth,
        column: Math.round(column),
        scrollWidth: element.scrollWidth,
        title: Math.round(title ?? 0),
      };
    });
    console.log(`TABLE ${JSON.stringify(measured)}`);
    expect(measured.scrollWidth, "the table's width against its column").toBeLessThanOrEqual(
      measured.clientWidth + 1
    );
  } finally {
    keep();
  }
});
