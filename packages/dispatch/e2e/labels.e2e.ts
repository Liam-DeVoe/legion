import { createIssue, createProject, getIssue, patchIssue } from "./api";
import { filterPicker, pickFilterOption } from "./filters";
import { headerLabels, openDetails } from "./issue-page";
import { expect, type Locator, test } from "./test";
import { asUser } from "./users";

async function expectTouchTarget(locator: Locator): Promise<void> {
  const box = await locator.boundingBox();
  expect(box?.height ?? 0).toBeGreaterThanOrEqual(44);
}

// @pr: A keyed write a person makes: labels edited from the multi-select, and the project list filtered by every one picked.
test("edits issue labels from a searchable multi-select and filters project issues by every selected label @pr", async ({
  browser,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const edited = await createIssue({ project: "CORE", title: "Editable labels" });
  const matching = await createIssue({ project: "CORE", title: "Frontend documentation" });
  const other = await createIssue({ project: "CORE", title: "Backend documentation" });
  await patchIssue(edited.key, { labels: ["Frontend", "api"] });
  await patchIssue(matching.key, { labels: ["Frontend", "docs"] });
  await patchIssue(other.key, { labels: ["backend", "docs"] });

  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await page.goto(`/issues/${edited.key}`);
    // The labels' control is Details' Labels row (dispatch://INFRA-1196 R13).
    await openDetails(page);
    const editLabels = page.getByRole("button", { name: "Edit labels" });
    await editLabels.click();
    const labelSearch = page.getByRole("combobox", { name: "Search or create label" });
    await expect(page.getByRole("option", { name: "Frontend", exact: true })).toHaveAttribute(
      "aria-selected",
      "true"
    );
    await expect(page.getByRole("option", { name: "api", exact: true })).toHaveAttribute(
      "aria-selected",
      "true"
    );
    await expect(page.getByRole("option", { name: "backend", exact: true })).toHaveAttribute(
      "aria-selected",
      "false"
    );
    await labelSearch.fill("urgent");
    await expect(page.getByRole("option", { name: 'Create "urgent"' })).toBeVisible();
    await labelSearch.press("Enter");
    await expect(page.getByRole("option", { name: "urgent", exact: true })).toHaveAttribute(
      "aria-selected",
      "true"
    );

    const shots = process.env.DISPATCH_LABEL_SCREENSHOT_DIR;
    if (shots !== undefined) {
      await page.screenshot({
        fullPage: true,
        path: `${shots}/labels-${page.viewportSize()?.width}.png`,
      });
    }

    await editLabels.click();
    await expect
      .poll(() => getIssue(edited.key))
      .toMatchObject({
        labels: ["Frontend", "api", "urgent"],
      });

    await page.goto("/projects/CORE?label=docs");
    await expect(page).toHaveURL(/\/projects\/CORE\/issues\?label=docs$/);
    await filterPicker(page, "Labels").click();
    await expect(page.getByRole("option", { exact: true, name: "docs, 2 issues" })).toHaveAttribute(
      "aria-selected",
      "true"
    );
    await expect(page.getByText("Frontend documentation", { exact: true })).toBeVisible();
    await expect(page.getByText("Backend documentation", { exact: true })).toBeVisible();
    await expect(page.getByText("Editable labels", { exact: true })).toHaveCount(0);
    await pickFilterOption(page, "Labels", "docs");
    await expect(page).toHaveURL(
      (url) => url.pathname === "/projects/CORE/issues" && !url.searchParams.has("label")
    );
    await pickFilterOption(page, "Labels", "Frontend");
    await expect(page.getByText("Editable labels", { exact: true })).toBeVisible();
    await expect(page.getByText("Frontend documentation", { exact: true })).toBeVisible();
    await expect(page.getByText("Backend documentation", { exact: true })).toHaveCount(0);
    await pickFilterOption(page, "Labels", "docs");
    await expect(page.getByText("Frontend documentation", { exact: true })).toBeVisible();
    await expect(page.getByText("Editable labels", { exact: true })).toHaveCount(0);
    await expect(page.getByText("Backend documentation", { exact: true })).toHaveCount(0);
  } finally {
    await context.close();
  }
});

// The label controls' phone half: Details' Edit labels and its search box, and the project
// list's Labels picker with its options and Clear, are what a finger presses, so each holds the
// 44 px touch target and the filtered list fits the screen. What those controls do is the row
// above, which holds at any width.
test("the label controls keep touch-sized targets and the filtered list fits the screen @phone @phone-only", async ({
  browser,
}) => {
  await createProject({ key: "CORE", name: "Core" });
  const edited = await createIssue({ project: "CORE", title: "Editable labels" });
  const matching = await createIssue({ project: "CORE", title: "Frontend documentation" });
  await patchIssue(edited.key, { labels: ["Frontend", "api"] });
  await patchIssue(matching.key, { labels: ["Frontend", "docs"] });

  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    await page.goto(`/issues/${edited.key}`);
    await openDetails(page);
    const editLabels = page.getByRole("button", { name: "Edit labels" });
    await editLabels.click();
    const labelSearch = page.getByRole("combobox", { name: "Search or create label" });
    await expect(labelSearch).toBeVisible();
    await expectTouchTarget(editLabels);
    await expectTouchTarget(labelSearch);
    const shots = process.env.DISPATCH_LABEL_SCREENSHOT_DIR;
    if (shots !== undefined) {
      await page.screenshot({
        fullPage: true,
        path: `${shots}/labels-${page.viewportSize()?.width}.png`,
      });
    }
    await editLabels.click();

    await page.goto("/projects/CORE?label=docs");
    await expect(page.getByText("Frontend documentation", { exact: true })).toBeVisible();
    await filterPicker(page, "Labels").click();
    await expectTouchTarget(filterPicker(page, "Labels"));
    await expectTouchTarget(page.getByRole("option", { exact: true, name: "docs, 1 issue" }));
    await expectTouchTarget(page.getByRole("button", { name: "Clear labels" }));
    await expect(
      page.evaluate(
        () => document.documentElement.scrollWidth <= document.documentElement.clientWidth
      )
    ).resolves.toBe(true);
  } finally {
    await context.close();
  }
});

// The header draws the labels that fit on its properties line and a count of those left over,
// which opens Details at its Labels row, where all of them wrap inside the panel (the ruling of
// dispatch://INFRA-1196/comment/63b73642-8fc5-4cc4-8f5d-a3d8989d3bf4).
test("eight thirty-character labels end the header's line on a count and wrap in Details without widening the page", async ({
  browser,
}, testInfo) => {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({ project: "CORE", title: "Long labels" });
  const long = Array.from({ length: 8 }, (_, index) => `label-${index}-`.padEnd(30, "x"));
  const context = await asUser(browser, "alice");
  try {
    const page = await context.newPage();
    const header = page.getByTestId("issue-header");
    const title = header.getByRole("heading", { level: 1, name: "Long labels" });
    for (const [width, height] of [
      [1280, 900],
      [390, 844],
    ]) {
      await page.setViewportSize({ height, width });
      await patchIssue(issue.key, { labels: [] });
      await page.goto(`/issues/${issue.key}`);
      await expect(title).toBeVisible();
      const titleBefore = await title.boundingBox();

      await patchIssue(issue.key, { labels: long });
      await page.reload();
      const left = header.getByTestId("issue-labels-left");
      await expect(left).toBeVisible();
      // The title box is untouched, and the labels stand on one line of the header: those that
      // fit, then the count of the rest.
      expect(await title.boundingBox()).toEqual(titleBefore);
      const drawn = await headerLabels(page).count();
      await expect(left).toHaveText(`+${8 - drawn} ${8 - drawn === 1 ? "label" : "labels"}`);
      const line = await header.getByTestId("issue-header-labels").boundingBox();
      expect(line?.height ?? 0).toBeLessThanOrEqual(24);

      // The count opens Details at the Labels row, which wraps every label inside the panel.
      await left.click();
      const labels = page.getByTestId("issue-labels");
      await expect(page.getByText(long[7] ?? "", { exact: true }).last()).toBeVisible();
      const metrics = await labels.evaluate((element) => ({
        clientWidth: element.clientWidth,
        height: element.getBoundingClientRect().height,
        scrollWidth: element.scrollWidth,
      }));
      expect(metrics.scrollWidth).toBeLessThanOrEqual(metrics.clientWidth + 1);
      expect(metrics.height).toBeGreaterThan(44);
      const pageWidths = await page.evaluate(() => ({
        clientWidth: document.documentElement.clientWidth,
        scrollWidth: document.documentElement.scrollWidth,
      }));
      expect(pageWidths.scrollWidth).toBe(pageWidths.clientWidth);
      const shot = testInfo.outputPath(`labels-overflow-${width}.png`);
      await page.screenshot({ path: shot });
      await testInfo.attach(`eight long labels (${width}px)`, {
        contentType: "image/png",
        path: shot,
      });
    }
  } finally {
    await context.close();
  }
});
