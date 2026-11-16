import { ISSUE_TAB_LABELS } from "../web/src/features/issue/issue-tab-list";
import { presetPath } from "../web/src/features/issue-page/issue-address";
import { PRESET_NAMES, PRESET_ORDER, type Preset } from "../web/src/features/issue-page/page-words";
import { createIssue, createIssueArtifact, createProject, createProjectDocument } from "./api";
import {
  closeDetails,
  contentsPane,
  detailsClose,
  filterRow,
  gotoIssue,
  iconRail,
  issuePage,
  marginResizeHandle,
  marginShell,
  openDetails,
  openMenu,
  outlineControl,
  outlineSheet,
  phase,
  presetChooser,
  presetCount,
  presetMenuItem,
  presetToggle,
  presetToggles,
  presetToolbar,
} from "./issue-page";
import { withIssuePage } from "./issue-page-option";
import { expect, type Locator, type Page, test } from "./test";
import { assertPageFits, assertWhole } from "./unclipped";
import { asUser } from "./users";

/**
 * The new issue page's frame (dispatch://INFRA-3946, part D of dispatch://INFRA-1196): the icon
 * rail, Details over the page, the Contents pane and the Outline sheet, the view row and its
 * chords, the frame's skeleton, and the guards that today's shell is what every other reader
 * and every other page still gets. Every row but the preference-off guard turns the new page on
 * (`phase`, `withIssuePage`).
 */

/** Two statements, so Spec counts two, and one check, so Verification counts one. */
const SPEC = `:::requirement{#r1}
The frame keeps the page's width.
:::

:::requirement{#r2}
The view row is one line at every width.
:::
`;

/** What the seed below gives each control of the view row to count: no question and no thread
 *  of the reader's, two statements, one check, no plan and no decision. */
const SEEDED_COUNTS: Readonly<Record<Preset, number>> = {
  conversation: 0,
  decisions: 0,
  plan: 0,
  spec: 2,
  verification: 1,
};

/** One issue with a spec of two statements and a Verification document of one check. */
async function seedFrame(): Promise<{ key: string }> {
  await createProject({ key: "FRM", name: "Frame" });
  const issue = await createIssue({ project: "FRM", spec: SPEC, title: "The new page's frame" });
  await createIssueArtifact(issue.key, {
    content: `:::check{#c1 serves="dispatch://${issue.key}/spec#b-r1" surface="browser" run="bun run e2e -- e2e/issue-page-d.e2e.ts"}
The frame keeps the page's width.
:::
`,
    name: "verification.md",
  });
  return { key: issue.key };
}

/** The widths R10 of the part holds the frame to. */
const WIDTHS = [320, 390, 768, 1024, 1280, 1440] as const;

/** A viewport of `width`: the desktop height from 768 px up, a phone's below. */
function viewportAt(width: number): { height: number; width: number } {
  return { height: width >= 768 ? 900 : 844, width };
}

/** The column that holds the stage and the foot, inside its insets. */
async function stageWidth(page: Page): Promise<number> {
  return page.getByTestId("issue-column").evaluate((column) => {
    const style = getComputedStyle(column);
    return (
      column.clientWidth -
      Number.parseFloat(style.paddingLeft) -
      Number.parseFloat(style.paddingRight)
    );
  });
}

/** Leaves the focus on no control, so a key reaches the page's own bindings. */
async function focusNothing(page: Page): Promise<void> {
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
}

/** `locator`'s box, failing the row where it draws none. */
async function boxOf(
  locator: Locator,
  what: string
): Promise<{
  height: number;
  width: number;
  x: number;
  y: number;
}> {
  const box = await locator.boundingBox();
  if (box === null) throw new Error(`${what} draws no box`);
  return box;
}

// c-frame (1): the frame's half of the checkpoint's "one panel of properties holds no
// conversation list @phone".
test("the frame draws an icon rail, Details over the page and the Contents pane @phone", async ({
  browser,
}) => {
  test.setTimeout(180_000);
  await phase(browser, seedFrame, async (page, { key }) => {
    await gotoIssue(page, `/issues/${key}`);

    // The rail: 56 px at the left edge, the window's height, R2's links in order, no tooltip, and
    // no sidebar beside it.
    await expect(iconRail(page), "the new page draws no icon rail").toHaveCount(1);
    const rail = await boxOf(iconRail(page), "the rail");
    expect(rail.x, "the rail is not at the left edge").toBe(0);
    expect(rail.width, "the rail is not 56 px wide").toBe(56);
    expect(rail.height, "the rail is not the window's height").toBe(900);
    const links = await iconRail(page)
      .getByRole("link")
      .evaluateAll((all) => all.map((link) => link.getAttribute("aria-label") ?? ""));
    expect(links[1], "the rail's second link is not the Inbox").toMatch(/^Inbox( \d+)?$/);
    expect(
      [links[0], ...links.slice(2)],
      "the rail's links are not Dispatch, Inbox, Agents, Priorities, Delivery, Pipeline, the project and Settings"
    ).toEqual(["Dispatch", "Agents", "Priorities", "Delivery", "Pipeline", "FRM", "Settings"]);
    await expect(iconRail(page).getByRole("link", { exact: true, name: "FRM" })).toHaveAttribute(
      "aria-current",
      "true"
    );
    expect(await iconRail(page).locator("[title]").count(), "a rail entry carries a title").toBe(0);
    await expect(
      page.getByRole("complementary", { exact: true, name: "Navigation" }),
      "the new page draws the sidebar beside its rail"
    ).toHaveCount(0);
    // Its foot opens the whole navigation over the page, and Escape closes it.
    await iconRail(page).getByRole("button", { exact: true, name: "Open navigation" }).click();
    const navigation = page.getByRole("dialog", { exact: true, name: "Navigation" });
    await expect(navigation, "the rail's foot opens no navigation").toBeVisible();
    await page.keyboard.press("Escape");
    await expect(navigation).toBeHidden();

    // The page's column: the window less the rail.
    const column = await boxOf(issuePage(page), "the page");
    expect(column.x, "the page does not start at the rail's edge").toBe(56);
    expect(column.width, "the page's column is not 1384 px wide").toBe(1384);

    // Details: hidden at the load; `Shift+M` opens it over the right of the page, the focus on
    // its Close, with the prototype's edge shadow, no tab and no shade, the page as wide as it was.
    await expect(marginShell(page), "Details is open on a fresh load").toBeHidden();
    await presetToggle(page, "conversation").focus();
    await page.keyboard.press("Shift+M");
    await expect(marginShell(page), "Shift+M did not open Details").toBeVisible();
    const drawer = await boxOf(marginShell(page), "Details");
    expect(Math.round(drawer.x), "Details does not open against the right edge").toBe(1056);
    expect(Math.round(drawer.width), "Details does not open 384 px wide").toBe(384);
    await expect(detailsClose(page), "opening Details left the focus off its Close").toBeFocused();
    expect(
      await marginShell(page).evaluate((element) => getComputedStyle(element).boxShadow),
      "Details casts no edge shadow"
    ).toContain("rgba(28, 25, 23, 0.14) -12px 0px 30px");
    await expect(marginShell(page).getByRole("tab"), "Details draws a tab").toHaveCount(0);
    await expect(
      page.getByTestId("details-shade"),
      "Details shades the page at 1440 px"
    ).toBeHidden();
    expect((await boxOf(issuePage(page), "the page")).width, "Details took a column").toBe(1384);
    // Escape closes it and gives the focus back to where it was taken from.
    await page.keyboard.press("Escape");
    await expect(marginShell(page), "Escape did not close Details").toBeHidden();
    await expect(presetToggle(page, "conversation")).toBeFocused();
    // With the list of keys open over it, Escape closes the list alone.
    await openDetails(page);
    await page.keyboard.press("?");
    const keys = page.getByRole("dialog", { name: "Keyboard shortcuts" });
    await expect(keys).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(keys).toBeHidden();
    await expect(marginShell(page), "Escape closed Details under the list of keys").toBeVisible();

    // Its handle widens it, and a reload keeps the width.
    const handle = await boxOf(marginResizeHandle(page), "Details' handle");
    await page.mouse.move(handle.x + 2, handle.y + 2);
    await page.mouse.down();
    await page.mouse.move(handle.x - 120, handle.y + 2);
    await page.mouse.up();
    const resized = (await boxOf(marginShell(page), "Details")).width;
    expect(resized, "dragging the handle did not widen Details").toBeGreaterThan(384 + 50);
    await page.reload();
    await expect(issuePage(page)).toHaveCount(1);
    await expect(marginShell(page), "a load opened Details").toBeHidden();
    await openDetails(page);
    expect((await boxOf(marginShell(page), "Details")).width, "the width was not kept").toBeCloseTo(
      resized,
      0
    );

    // Leaving the issue and coming back finds it closed.
    await iconRail(page)
      .getByRole("link", { name: /^Inbox/ })
      .click();
    await expect(page).toHaveURL(/\/$/);
    await page.goBack();
    await expect(issuePage(page)).toHaveCount(1);
    await expect(marginShell(page), "coming back to the issue found Details open").toBeHidden();

    // The pane: 260 px against the rail with no view on; under a view, no pane and the stage
    // has the column's 1328 px.
    const pane = await boxOf(contentsPane(page), "the Contents pane");
    expect(pane.x, "the pane is not against the rail").toBe(56);
    expect(pane.width, "the pane is not 260 px wide").toBe(260);
    expect(await stageWidth(page), "the stage beside the pane is not 1068 px").toBe(1068);
    await gotoIssue(page, presetPath(key, "spec"));
    await expect(contentsPane(page), "a view draws the pane").toHaveCount(0);
    expect(await stageWidth(page), "the stage under a view is not 1328 px").toBe(1328);

    // The rail follows `md`: drawn at 768 px, not at 767 px.
    await page.setViewportSize(viewportAt(768));
    await expect(iconRail(page), "no rail at 768 px").toHaveCount(1);
    await page.setViewportSize(viewportAt(767));
    await expect(iconRail(page), "a rail at 767 px").toHaveCount(0);

    // At 390 px: no rail; Details a dialog the window's width that holds the focus, over the
    // shade; the Outline control opens the Contents sheet 56 px from the top.
    await page.setViewportSize(viewportAt(390));
    await gotoIssue(page, `/issues/${key}`);
    await expect(iconRail(page), "the phone draws the rail").toHaveCount(0);
    await focusNothing(page);
    await page.keyboard.press("Shift+M");
    const phoneDetails = page.getByRole("dialog", { exact: true, name: "Details" });
    await expect(phoneDetails, "Details is no dialog on the phone").toBeVisible();
    expect(Math.round((await boxOf(phoneDetails, "Details")).width)).toBe(390);
    await expect(detailsClose(page)).toBeFocused();
    const shade = page.getByTestId("details-shade");
    await expect(shade, "Details draws no shade on the phone").toBeVisible();
    expect(await shade.evaluate((element) => getComputedStyle(element).backgroundColor)).toBe(
      "rgba(28, 25, 23, 0.25)"
    );
    await page.keyboard.press("Escape");
    await expect(phoneDetails).toBeHidden();
    await outlineControl(page).click();
    const sheet = page.getByRole("dialog", { exact: true, name: "Contents" });
    await expect(sheet).toBeVisible();
    await expect(outlineSheet(page)).toHaveCount(1);
    expect(Math.round((await boxOf(sheet, "the Outline sheet")).y)).toBe(56);
    await sheet.getByRole("button", { exact: true, name: "Close" }).click();
    await expect(sheet).toBeHidden();
    await outlineControl(page).click();
    await expect(sheet).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(sheet).toBeHidden();

    // At each of R10's widths, Details closed and open, nothing is wider than the window and
    // nothing of the frame scrolls sideways.
    for (const width of WIDTHS) {
      await page.setViewportSize(viewportAt(width));
      await gotoIssue(page, `/issues/${key}`);
      for (const open of [false, true]) {
        if (open) await openDetails(page);
        await assertPageFits(page);
        await assertWhole(filterRow(page));
        if ((await iconRail(page).count()) > 0) await assertWhole(iconRail(page));
        if ((await contentsPane(page).count()) > 0) await assertWhole(contentsPane(page));
        if (open) {
          await assertWhole(marginShell(page));
          await closeDetails(page);
        }
      }
      if ((await outlineControl(page).count()) > 0) {
        await outlineControl(page).click();
        await assertWhole(outlineSheet(page));
        await page.keyboard.press("Escape");
        await expect(outlineSheet(page)).toHaveCount(0);
      }
    }
  });
});

/** The address `view` has on `key`, at the end of the page's URL. */
function addressOf(key: string, view: Preset): RegExp {
  return new RegExp(`${presetPath(key, view)}$`);
}

/** The view row's height and each control in it, with its box and the words it draws. */
async function rowControls(
  page: Page
): Promise<{ height: number; controls: { height: number; text: string; width: number }[] }> {
  const row = await boxOf(filterRow(page), "the view row");
  const controls = await filterRow(page)
    .locator("button, a, input, select")
    .evaluateAll((all) =>
      all
        .filter((control) => control.closest('[role="menu"], [role="dialog"]') === null)
        .map((control) => {
          const box = control.getBoundingClientRect();
          return {
            height: box.height,
            text: (control as HTMLElement).innerText.trim(),
            top: box.top,
            width: box.width,
          };
        })
    );
  return { controls, height: row.height };
}

// c-frame (2): the toggle half of the checkpoint's "the five controls are toggles that draw their
// query's answer @phone", its "the filter row is one line at every width @phone" less the words
// box, and five chords of `e2e/keyboard-issue.e2e.ts`'s "every chord the page bound before the
// redesign brings a target on screen with the focus in it".
test("the five controls are toggles with an address each, one line at every width, and the t chords reach them @phone", async ({
  browser,
}) => {
  test.setTimeout(180_000);
  await phase(browser, seedFrame, async (page, { key }) => {
    await gotoIssue(page, `/issues/${key}`);

    // The toolbar Filters: five toggles in R7's order, each named by its view's word alone and
    // described by its signal, whose count is the seed's.
    await expect(presetToolbar(page), "no toolbar named Filters holds the five").toHaveCount(1);
    await expect(presetToggles(page)).toHaveCount(5);
    expect(
      await presetToggles(page).evaluateAll((buttons) =>
        buttons.map((button) => button.querySelector("[data-preset-name]")?.textContent ?? "")
      ),
      "the row's controls are not Conversation, Spec, Verification, Plan and Decisions"
    ).toEqual(PRESET_ORDER.map((preset) => PRESET_NAMES[preset]));
    for (const preset of PRESET_ORDER) {
      const toggle = presetToggle(page, preset);
      await expect(toggle, `no toggle named ${PRESET_NAMES[preset]}`).toHaveCount(1);
      await expect(toggle).toHaveAttribute(
        "aria-pressed",
        preset === "conversation" ? "true" : "false"
      );
      await expect(presetCount(page, preset)).toHaveText(String(SEEDED_COUNTS[preset]));
      await expect(
        toggle,
        `${PRESET_NAMES[preset]} is not described by its count`
      ).toHaveAccessibleDescription(new RegExp(`^${SEEDED_COUNTS[preset]}\\b`));
    }

    // A press turns a view on and the others off, a second press turns it off, and each state
    // is its own address.
    for (const view of ["spec", "verification", "plan", "decisions"] as const) {
      await presetToggle(page, view).click();
      await expect(page).toHaveURL(addressOf(key, view));
      for (const other of PRESET_ORDER) {
        await expect(presetToggle(page, other)).toHaveAttribute(
          "aria-pressed",
          other === view ? "true" : "false"
        );
      }
      await presetToggle(page, view).click();
      await expect(page).toHaveURL(addressOf(key, "conversation"));
      await expect(presetToggle(page, "conversation")).toHaveAttribute("aria-pressed", "true");
    }
    for (const view of PRESET_ORDER) {
      await gotoIssue(page, presetPath(key, view));
      await expect(presetToggle(page, view)).toHaveAttribute("aria-pressed", "true");
    }
    // Conversation turns every view off from an address of the issue that is no view's.
    await gotoIssue(page, `/issues/${key}/collaborators`);
    await presetToggle(page, "conversation").click();
    await expect(page).toHaveURL(addressOf(key, "conversation"));

    // One tab stop, moved by the arrows, Home and End.
    expect(
      await presetToggles(page).evaluateAll(
        (buttons) => buttons.filter((button) => (button as HTMLElement).tabIndex === 0).length
      ),
      "the toolbar is not one tab stop"
    ).toBe(1);
    await presetToggle(page, "conversation").focus();
    await page.keyboard.press("ArrowRight");
    await expect(presetToggle(page, "spec")).toBeFocused();
    await page.keyboard.press("End");
    await expect(presetToggle(page, "decisions")).toBeFocused();
    await page.keyboard.press("Home");
    await expect(presetToggle(page, "conversation")).toBeFocused();
    await page.keyboard.press("ArrowLeft");
    await expect(presetToggle(page, "decisions")).toBeFocused();

    // Keys opens the list of keys, which names the five chords.
    await filterRow(page).getByRole("button", { exact: true, name: "Keys" }).click();
    const keys = page.getByRole("dialog", { name: "Keyboard shortcuts" });
    await expect(keys).toBeVisible();
    for (const view of ["spec", "verification", "plan", "decisions"] as const) {
      await expect(keys).toContainText(`Turn the ${PRESET_NAMES[view]} filter on or off`);
    }
    await expect(keys).toContainText(`${PRESET_NAMES.conversation}: turn every filter off`);
    await page.keyboard.press("Escape");
    await expect(keys).toBeHidden();

    // The chords, each with its pressed control and its address.
    for (const [second, view] of [
      ["s", "spec"],
      ["k", "verification"],
      ["p", "plan"],
      ["d", "decisions"],
      ["c", "conversation"],
    ] as const) {
      await focusNothing(page);
      await page.keyboard.press("t");
      await page.keyboard.press(second);
      await expect(page, `t ${second} did not open ${PRESET_NAMES[view]}`).toHaveURL(
        addressOf(key, view)
      );
      await expect(presetToggle(page, view)).toHaveAttribute("aria-pressed", "true");
    }

    // Below 1280 px the one control opens a menu of five, the one on checked; below 1024 px the
    // Outline control stands beside it.
    for (const width of [1279, 390]) {
      await page.setViewportSize(viewportAt(width));
      await gotoIssue(page, presetPath(key, "spec"));
      await presetChooser(page).click();
      await expect(openMenu(page)).toHaveCount(1);
      for (const preset of PRESET_ORDER) {
        await expect(presetMenuItem(page, preset)).toHaveAttribute(
          "aria-checked",
          preset === "spec" ? "true" : "false"
        );
      }
      await page.keyboard.press("Escape");
      await expect(openMenu(page)).toHaveCount(0);
    }
    for (const width of [1023, 390]) {
      await page.setViewportSize(viewportAt(width));
      await gotoIssue(page, `/issues/${key}`);
      await expect(outlineControl(page), `no Outline control at ${width} px`).toHaveCount(1);
    }

    // At each of R10's widths the row is one line, at most 35 px tall from 1280 px and 51 px
    // below, no control in it under 24 px, and each control's words inside its name.
    for (const width of WIDTHS) {
      await page.setViewportSize(viewportAt(width));
      await gotoIssue(page, `/issues/${key}`);
      await assertPageFits(page);
      const limit = width >= 1280 ? 35 : 51;
      await expect
        .poll(async () => (await rowControls(page)).height, {
          message: `the view row is over ${limit} px tall at ${width} px`,
        })
        .toBeLessThanOrEqual(limit);
      const { controls } = await rowControls(page);
      for (const control of controls) {
        expect(
          control.height,
          `"${control.text}" is under 24 px tall at ${width} px`
        ).toBeGreaterThanOrEqual(24);
        expect(
          control.width,
          `"${control.text}" is under 24 px wide at ${width} px`
        ).toBeGreaterThanOrEqual(24);
      }
      const named = filterRow(page).locator("button:visible");
      for (let at = 0; at < (await named.count()); at += 1) {
        const words = (await named.nth(at).innerText()).replace(/\s+/g, " ").trim();
        if (words === "") continue;
        await expect(
          named.nth(at),
          `"${words}" is not inside its control's name`
        ).toHaveAccessibleName(new RegExp(words.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
      }
    }

    // At 390 px a scrolled page keeps the row at the top; at 1440 px it scrolls away.
    for (const [width, stays] of [
      [390, true],
      [1440, false],
    ] as const) {
      await page.setViewportSize(viewportAt(width));
      await gotoIssue(page, `/issues/${key}`);
      await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
      const row = await boxOf(filterRow(page), "the view row");
      if (stays) expect(Math.round(row.y), "the row left the top at 390 px").toBe(0);
      else expect(row.y, "the row stayed at the top at 1440 px").toBeLessThan(0);
    }
  });
});

/** An issue whose spec links another issue, so today's page draws a reference to hover. */
async function seedTodaysPage(): Promise<{ key: string }> {
  await createProject({ key: "OFF", name: "Today" });
  const target = await createIssue({ project: "OFF", title: "The issue the spec links" });
  const issue = await createIssue({
    project: "OFF",
    spec: `See [the other issue](dispatch://${target.key}) for context.\n`,
    title: "A reader with the preference off",
  });
  return { key: issue.key };
}

// c-frame (3): with the preference off, today's shell on the issue page.
test("a reader with the preference off keeps today's shell on the issue page", async ({
  browser,
}) => {
  const { key } = await seedTodaysPage();
  const alice = await asUser(browser, "alice");
  try {
    const page = await alice.newPage();
    await page.setViewportSize({ height: 900, width: 1440 });
    await page.goto(`/issues/${key}/spec`);
    await expect(page.getByTestId("main-content")).toBeVisible();
    await expect(iconRail(page), "today's issue page draws the rail").toHaveCount(0);
    await expect(
      page.getByRole("complementary", { exact: true, name: "Navigation" }),
      "today's issue page lost its sidebar"
    ).toBeVisible();
    await expect(marginShell(page), "today's issue page lost its margin column").toBeVisible();
    await expect(marginShell(page).getByRole("tab", { name: "Comments" })).toBeVisible();
    await expect(marginResizeHandle(page)).toHaveCount(1);
    // `Shift+M` folds the margin to its rail, and back.
    await focusNothing(page);
    await page.keyboard.press("Shift+M");
    await expect(page.getByTestId("margin-rail"), "Shift+M did not fold the margin").toBeVisible();
    await page.keyboard.press("Shift+M");
    await expect(page.getByTestId("margin-rail")).toHaveCount(0);
    // A resting pointer on a reference opens its card.
    const reference = page.locator("[data-dispatch-href]").first();
    await expect(reference).toBeVisible();
    await reference.hover();
    await expect(page.getByRole("tooltip"), "a hover opened no card on today's page").toBeVisible();

    // While the issue page's chunk loads, today's skeleton names its nine tabs.
    const held = Promise.withResolvers<void>();
    await page.route("**/assets/IssuePage-*.js", async (route) => {
      await held.promise;
      await route.continue();
    });
    const arrival = page.goto(`/issues/${key}`).catch(() => undefined);
    const tabs = page.locator('[aria-busy="true"] [role="tablist"]');
    await expect(tabs).toBeVisible();
    expect(
      (await tabs.innerText())
        .split("\n")
        .map((word) => word.trim())
        .filter((word) => word !== ""),
      "today's skeleton does not name its nine tabs"
    ).toEqual([...ISSUE_TAB_LABELS]);
    held.resolve();
    await arrival;
  } finally {
    await alice.close();
  }
});

/**
 * The new page fills a slot in the shell's margin and draws its own frame (dispatch://INFRA-1196
 * R12, R14), and the margin is mounted once for every route. These three rows say the frame
 * changed no other page for a reader who turned the new page on: each draws its margin as it did,
 * no rail, and none of the new page's panel.
 */
const ISSUE_PANEL = '[data-testid="issue-margin"]';

test("a project document page draws its margin, and none of the issue page's panel", async ({
  browser,
}) => {
  await createProject({ key: "DOC", name: "Documents" });
  const document = await createProjectDocument("DOC", {
    content: "# The handbook\n\nIts margin is the margin every document page has.\n",
    name: "handbook.md",
  });
  const alice = await asUser(browser, "alice");
  try {
    await withIssuePage(alice, "alice");
    const page = await alice.newPage();
    await page.setViewportSize({ height: 900, width: 1440 });
    await page.goto(`/projects/DOC/documents/${document.artifact.slug}`);
    await expect(page.getByRole("heading", { level: 1, name: "The handbook" })).toBeVisible();
    await expect(iconRail(page), "a project document page draws the rail").toHaveCount(0);
    await expect(page.getByTestId("desktop-margin-shell")).toHaveCount(1);
    await expect(page.getByTestId("margin-sheet")).toBeVisible();
    await expect(page.getByRole("tab", { name: "Comments" })).toBeVisible();
    await expect(page.getByLabel("Resize margin")).toHaveCount(1);
    await expect(page.locator(ISSUE_PANEL)).toHaveCount(0);
  } finally {
    await alice.close();
  }
});

test("the Inbox keeps its margin-less layout", async ({ browser }) => {
  await createProject({ key: "INB", name: "Inbox" });
  await createIssue({ project: "INB", spec: SPEC, title: "An issue to list" });
  const alice = await asUser(browser, "alice");
  try {
    await withIssuePage(alice, "alice");
    const page = await alice.newPage();
    await page.setViewportSize({ height: 900, width: 1440 });
    await page.goto("/");
    await expect(page.getByTestId("main-content")).toBeVisible();
    await expect(iconRail(page), "the Inbox draws the rail").toHaveCount(0);
    // A route with no margin spends no width on one, which is what it did before the slot.
    await expect(page.getByTestId("desktop-margin-shell")).toHaveCount(0);
    await expect(page.locator(ISSUE_PANEL)).toHaveCount(0);
  } finally {
    await alice.close();
  }
});

test("an Agents page keeps its margin-less layout", async ({ browser }) => {
  const alice = await asUser(browser, "alice");
  try {
    await withIssuePage(alice, "alice");
    const page = await alice.newPage();
    await page.setViewportSize({ height: 900, width: 1440 });
    await page.goto("/agents");
    await expect(page.getByTestId("main-content")).toBeVisible();
    await expect(iconRail(page), "the Agents page draws the rail").toHaveCount(0);
    await expect(page.getByTestId("desktop-margin-shell")).toHaveCount(0);
    await expect(page.locator(ISSUE_PANEL)).toHaveCount(0);
  } finally {
    await alice.close();
  }
});

// c-frame (7): the checkpoint's "the issue page's skeleton fits a phone and names its presets
// @phone" (`e2e/phone.e2e.ts`), with the new page on: the frame's skeleton names nothing.
test("the issue page's skeleton fits a phone and names no tab @phone", async ({ browser }) => {
  await phase(browser, seedFrame, async (page, { key }) => {
    await page.setViewportSize({ height: 844, width: 390 });
    // The new page's own chunk, held until the skeleton has been read: it is the only thing
    // between the address and the page, so holding it is what makes the skeleton readable at
    // all. The navigation is not awaited while it is held, and is let go at the end.
    const held = Promise.withResolvers<void>();
    await page.route("**/assets/IssuePage-*.js", async (route) => {
      await held.promise;
      await route.continue();
    });
    const arrival = page.goto(`/issues/${key}`).catch(() => undefined);
    const skeleton = page.locator('[aria-busy="true"]');
    await expect(skeleton.locator('[role="toolbar"]')).toBeVisible();
    expect((await skeleton.innerText()).trim(), "the frame's skeleton names a tab").toBe("");
    await expect(skeleton.locator('[role="toolbar"] > *')).toHaveCount(5);
    const layout = await page.evaluate(() => ({
      client: document.documentElement.clientWidth,
      scroll: document.documentElement.scrollWidth,
    }));
    expect(
      layout.scroll,
      `the skeleton made the page ${layout.scroll}px wide in a ${layout.client}px viewport`
    ).toBeLessThanOrEqual(layout.client);
    held.resolve();
    await arrival;
  });
});
