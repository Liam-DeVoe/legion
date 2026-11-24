/** The priorities page over one recorded answer of the priorities read: the section of priority
 *  work in backlog or icebox with the row of an issue that a live priority covers and that stands
 *  in backlog, the priorities table sorted by the column an older link names, and the layouts a
 *  reader's browser kept under the names the page used before. Each reads what the browser draws
 *  from that fixed answer and what it keeps in its own storage, and none of it is a claim about
 *  what the server decides, so the row runs against a recording rather than against a harness
 *  (packages/dispatch/README.md, "The stubbed layer"). The browser's storage is seeded before the
 *  page loads, as the release before left it: the page moves those layouts on its first render
 *  (`moveTableStore` and `renameStoredColumns`, `web/src/features/project/issues-table-storage.ts`),
 *  and it reads the address's column id the same way (`PRIORITY_FORMER_IDS`). */
import { expect } from "@playwright/test";

import { createIssue, createPriority, createProject, patchIssue } from "./api";
import { stubbed } from "./stub/stub-test";

/** The id the priorities table's backlog column had in the release before, which a reader's
 *  stored layout and an older link still name. */
const FORMER_COLUMN_ID = "parked";

/** The browser keys the page keeps its layouts under (`tableStorageKey`): the list in backlog or
 *  icebox now and in the release before, and the priorities table, whose key did not change. */
const BACKLOG_KEY = "dispatch.project-table.priorities-backlog";
const FORMER_BACKLOG_KEY = "dispatch.project-table.priorities-parked";
const PRIORITIES_KEY = "dispatch.project-table.priorities";

/** The list's layout as a reader of the release before stored it: a width, a place and a column
 *  hidden. That list has no column of the former id. */
const formerBacklogLayout = {
  sizing: { status: 222 },
  order: ["status", "key"],
  visibility: { assignee: false },
};

/** The priorities table's layout as that reader stored it: the backlog column, under its former
 *  id, wider than it opens, moved before In flight, and shown. */
const formerPrioritiesLayout = {
  sizing: { [FORMER_COLUMN_ID]: 240 },
  order: ["owner", FORMER_COLUMN_ID, "in-flight", "waiting"],
  visibility: { [FORMER_COLUMN_ID]: true },
};

const test = stubbed(async () => {
  await createProject({ key: "CORE", name: "Core" });
  const priority = await createPriority({ owner: "alice", title: "Ship the daemon everywhere" });
  const issue = await createIssue({ project: "CORE", title: "Daemon rollout to the fleet" });
  await patchIssue(issue.key, { advances: [priority.id], status: "backlog" });
  return { issue: issue.key, priority: priority.title, title: issue.title };
});

test("the priorities page lists priority work in backlog or icebox and moves the layouts a reader kept @pr", async ({
  page,
  scene,
}) => {
  // Before the page's own scripts run, so its first render meets the storage the release before
  // left. The page loads once, so the seed is written once.
  await page.addInitScript(
    (layouts) => {
      for (const [key, layout] of Object.entries(layouts)) {
        localStorage.setItem(key, JSON.stringify(layout));
      }
    },
    { [FORMER_BACKLOG_KEY]: formerBacklogLayout, [PRIORITIES_KEY]: formerPrioritiesLayout }
  );
  await page.goto(`/priorities?p.sort=${FORMER_COLUMN_ID}`);

  // The section of priority work in backlog or icebox, and the row of the issue that stands in
  // backlog under the live priority.
  const section = page.getByRole("region", { name: "Priority work in backlog or icebox" });
  await expect(
    section.getByRole("heading", { exact: true, name: "Priority work in backlog or icebox" })
  ).toBeVisible();
  await expect(
    section.getByText(
      "Work that advances a priority and stands in the backlog or the icebox, with the time it " +
        "was moved there and whoever moved it. No reason is recorded for a move yet."
    )
  ).toBeVisible();
  await expect(section.getByText(/^1 issue · /)).toBeVisible();
  const table = section.getByRole("table", { name: "Priority issues in backlog or icebox table" });
  const row = table.getByRole("row", { exact: true, name: `${scene.issue} ${scene.title}` });
  await expect(row).toBeVisible();
  await expect(row.getByRole("link", { name: scene.priority })).toBeVisible();
  await expect(row.getByText("Backlog", { exact: true })).toBeVisible();

  // The priorities table is sorted by the backlog column, which the address names by its
  // former id, and it draws that column where the reader put it: before In flight.
  const priorities = page.getByRole("table", { name: "Priorities table" });
  const backlogHeader = priorities.getByRole("columnheader", { name: /^In backlog or icebox/ });
  await expect(backlogHeader).toHaveAttribute("aria-sort", "ascending");
  const headers = await priorities.getByRole("columnheader").allTextContents();
  const at = (start: string) => headers.findIndex((text) => text.trim().startsWith(start));
  expect(at("In backlog or icebox")).toBeGreaterThan(-1);
  expect(at("In backlog or icebox")).toBeLessThan(at("In flight"));

  // The stored layouts moved on that first render: the width, place and visibility of the
  // backlog column are under its id now, and the list's layout is under its key now, with the
  // former key gone.
  const stored = async (key: string) =>
    page.evaluate((name) => JSON.parse(localStorage.getItem(name) ?? "null"), key);
  await expect.poll(() => stored(FORMER_BACKLOG_KEY)).toBeNull();
  const backlog = await stored(BACKLOG_KEY);
  expect(backlog.sizing).toEqual(formerBacklogLayout.sizing);
  expect(backlog.order).toEqual(formerBacklogLayout.order);
  expect(backlog.visibility.assignee).toBe(false);
  const prioritiesLayout = await stored(PRIORITIES_KEY);
  expect(prioritiesLayout).toEqual({
    sizing: { backlog: 240 },
    order: ["owner", "backlog", "in-flight", "waiting"],
    visibility: { backlog: true },
  });
});
