import AxeBuilder from "@axe-core/playwright";
import { CONTROL_LABELS } from "../web/src/features/issue-page/page-words";
import { buildDispatchReference, buildIssuePath } from "../web/src/features/refs/routes";
import { getIssue } from "./api";
import { closeDetails, gotoIssue, marginShell, openDetails, openMenu, phase } from "./issue-page";
import {
  addControl,
  CHANGES_REASON,
  COMPONENT_TITLE,
  detailsRow,
  ELEVEN,
  marginSection,
  marginSectionDisclosure,
  marginSlot,
  OTHER_LINK,
  PINNED_FIRST_LINE,
  PLANNER_TITLE,
  PROJECT,
  PULL_NUMBER,
  pinsList,
  QUESTION,
  REPOSITORY,
  seedDetailsIssue,
  TWENTY_LABELS,
  UNPINNED_COMMENT,
} from "./issue-page-h";
import { expect, type Locator, type Page, type Request, test } from "./test";
import { assertWhole } from "./unclipped";

/**
 * Details' properties and pins (dispatch://INFRA-3945, part H of dispatch://INFRA-1196): the
 * rows of the three sections and their controls, the one Add control, the key `l`, the reader's
 * pins, and no comment, question or message of the issue. Every row reads `seedDetailsIssue`
 * as alice with the new page on (`phase`), and writes through Dispatch.
 */

/** Development's rows, in the order R2 of the part gives them, as ONE holds them: ONE's spec
 *  holds no task list, so it draws no Tasks row. */
const ONE_DEVELOPMENT_ROWS = [
  "Status",
  "Priority",
  "Assignee",
  "Labels",
  "Advances",
  "Parent",
  "Blocked by",
  "Blocks",
  "Repository",
  "Pull request",
  "Links",
  "Claimed by",
  "Messages go to",
  "Components",
];

/** The tags `e2e/accessibility.e2e.ts` holds the dashboard to. */
const AXE_TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"];

/** The page's ink and the grey of a link's underline, as the prototype's `.lnk` draws them. */
const INK = "rgb(28, 25, 23)";
const UNDERLINE = "rgb(168, 162, 158)";

/** A viewport of `width`: the desktop height from 768 px up, a phone's below. */
function viewportAt(width: number): { height: number; width: number } {
  return { height: width >= 768 ? 900 : 844, width };
}

/** The page's reads of Dispatch from now on: every URL it asked for, decoded, and whether none
 *  is still out. Only `fetch` and `xhr` count, so the live socket never holds a read open. */
function watchRequests(page: Page): { readonly settled: () => boolean; readonly urls: string[] } {
  const urls: string[] = [];
  let out = 0;
  const counted = (request: Request) => ["fetch", "xhr"].includes(request.resourceType());
  const ended = (request: Request) => {
    if (counted(request)) out -= 1;
  };
  page.on("request", (request) => {
    if (!counted(request)) return;
    urls.push(decodeURIComponent(request.url()));
    out += 1;
  });
  page.on("requestfailed", ended);
  page.on("requestfinished", ended);
  return { settled: () => out === 0, urls };
}

/** The labels of a section's rows, in the order they stand (`IssueMargin.tsx`'s `Row`). */
function rowLabels(section: Locator): Promise<string[]> {
  return section
    .locator(":scope > div > div > div:first-child > span[data-page-chrome]")
    .allTextContents();
}

/** Whether `locator`'s box lies inside the viewport sideways, so it is read with no sideways
 *  scroll. */
async function expectInsideSideways(page: Page, locator: Locator, what: string): Promise<void> {
  const box = await locator.boundingBox();
  const width = page.viewportSize()?.width ?? 0;
  expect(box, `${what} has no box`).not.toBeNull();
  expect(box?.x ?? -1, `${what} starts left of the viewport`).toBeGreaterThanOrEqual(0);
  expect(
    (box?.x ?? 0) + (box?.width ?? 0),
    `${what} ends right of the viewport`
  ).toBeLessThanOrEqual(width + 1);
}

/** The first element with a box that Details draws under its head, the element that holds
 *  Details' Close: it is the section Development, or inside it, while no document is open. */
function firstUnderTheHead(page: Page): Promise<string> {
  return marginShell(page).evaluate((drawer) => {
    const close = drawer.querySelector('[data-testid="details-close"]');
    const head = [...drawer.children].find((child) => close !== null && child.contains(close));
    const development = drawer.querySelector('[data-margin-section="development"]');
    if (head === undefined || development === null) return "no head or no Development";
    const walker = document.createTreeWalker(drawer, NodeFilter.SHOW_ELEMENT);
    for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
      if (!(node instanceof HTMLElement) || head.contains(node) || node === head) continue;
      if (
        !(head.compareDocumentPosition(node) & Node.DOCUMENT_POSITION_FOLLOWING) ||
        node.contains(development)
      ) {
        continue;
      }
      if (node.getBoundingClientRect().height === 0) continue;
      return development === node || development.contains(node)
        ? "development"
        : node.outerHTML.slice(0, 120);
    }
    return "nothing";
  });
}

test("Details opens on the properties and holds no conversation @phone", async ({ browser }) => {
  test.setTimeout(180_000);
  await phase(browser, seedDetailsIssue, async (page, seeded) => {
    const { one, three } = seeded.keys;
    const desktopProject = test.info().project.name !== "iphone";
    if (desktopProject) {
      // (1) Closed, Details reads neither the blockers' titles nor what the issue blocks.
      await page.setViewportSize(viewportAt(1440));
      const traffic = watchRequests(page);
      await gotoIssue(page, `/issues/${one}`);
      await expect(marginSlot(page)).toHaveCount(1);
      await expect
        .poll(() => traffic.settled(), { message: "the load's reads never ended" })
        .toBe(true);
      const urls = traffic.urls;
      const threeRef = `ref=${buildDispatchReference({ key: three, kind: "issue" })}`;
      const blocksRead = (url: string) =>
        url.includes("/api/v1/references?") && url.includes("kind=blocked_by");
      const titlesRead = (url: string) =>
        url.includes("/api/v1/reference-targets?") && url.includes(threeRef);
      expect(urls.filter(blocksRead), "closed Details read what ONE blocks").toEqual([]);
      expect(urls.filter(titlesRead), "closed Details read its blockers' titles").toEqual([]);
      await openDetails(page);
      await expect
        .poll(() => urls.some(blocksRead), { message: "Details read no Blocks" })
        .toBe(true);
      await expect
        .poll(() => urls.some(titlesRead), { message: "Details read no blocker's title" })
        .toBe(true);

      // (2) Development first under the head, each section drawn once, through a close and an
      // open, and nothing of the conversation.
      expect(await firstUnderTheHead(page), "Details does not open on Development").toBe(
        "development"
      );
      for (let round = 0; round < 2; round += 1) {
        for (const section of ["development", "people", "references"] as const) {
          await expect(
            page.locator(`[data-margin-section="${section}"]`),
            `the page draws ${section} other than once`
          ).toHaveCount(1);
        }
        await closeDetails(page);
        await openDetails(page);
      }
      await expect(
        marginShell(page).getByRole("tab", { name: /^(Comments|Pinned)\b/ })
      ).toHaveCount(0);
      await expect(
        marginShell(page).getByText(UNPINNED_COMMENT, { exact: false }),
        "Details draws a comment of the issue"
      ).toHaveCount(0);
      await expect(
        marginShell(page).getByText(QUESTION, { exact: false }),
        "Details draws a question of the issue"
      ).toHaveCount(0);
      await expect(
        marginShell(page).locator("[title]"),
        "an element of Details carries a title"
      ).toHaveCount(0);
      const editLabels = detailsRow(page, "Labels").getByRole("button", { name: "Edit labels" });
      await editLabels.click();
      await expect(page.getByRole("combobox", { name: "Search or create label" })).toBeVisible();
      await expect(
        marginShell(page).locator("[title]"),
        "an element of Details carries a title with the labels' popover open"
      ).toHaveCount(0);
      await page.keyboard.press("Escape");

      // (3) The two pins, each a link to its own address with its Unpin.
      const commentPin = pinsList(page).getByRole("link", { exact: true, name: PINNED_FIRST_LINE });
      await expect(commentPin).toHaveAttribute(
        "href",
        buildIssuePath({ id: seeded.pinnedCommentId, key: one, kind: "comment" })
      );
      const historyPin = pinsList(page).getByRole("link", {
        exact: true,
        name: CONTROL_LABELS.history,
      });
      await expect(historyPin).toHaveAttribute(
        "href",
        buildIssuePath({ key: one, kind: "activity" })
      );
      await expect(pinsList(page).getByRole("button", { name: CONTROL_LABELS.unpin })).toHaveCount(
        2
      );
      await closeDetails(page);
    }

    // (4) On a phone: the three sections inside Details' width, and a pin's link that closes it.
    await page.setViewportSize(viewportAt(390));
    await gotoIssue(page, `/issues/${one}`);
    await openDetails(page);
    for (const section of ["development", "people", "references"] as const) {
      await expect(marginSection(page, section)).toBeVisible();
    }
    expect(
      await marginShell(page).evaluate((drawer) => drawer.scrollWidth <= drawer.clientWidth),
      "Details scrolls sideways at 390 px"
    ).toBe(true);
    await pinsList(page).getByRole("link", { exact: true, name: PINNED_FIRST_LINE }).click();
    await expect(marginShell(page), "a pin's link left Details open on a phone").toBeHidden();
    await expect(page).toHaveURL(
      buildIssuePath({ id: seeded.pinnedCommentId, key: one, kind: "comment" })
    );
    await openDetails(page);
    for (let left = 2; left > 0; left -= 1) {
      const unpin = pinsList(page).getByRole("button", { name: CONTROL_LABELS.unpin });
      await expect(unpin).toHaveCount(left);
      await unpin.first().click();
    }
    await expect(pinsList(page)).toContainText(CONTROL_LABELS.none);
  });
});

test("every property with a value is visible in Details at every width @phone", async ({
  browser,
}) => {
  test.setTimeout(240_000);
  await phase(browser, seedDetailsIssue, async (page, seeded) => {
    const { blocks, one, parent, three } = seeded.keys;
    const desktopProject = test.info().project.name !== "iphone";

    // (5) Every width: R2's order, the twenty labels whole, and each value in its row.
    for (const width of desktopProject ? [1440, 1280, 1024, 390] : [390]) {
      await page.setViewportSize(viewportAt(width));
      await gotoIssue(page, `/issues/${one}`);
      await openDetails(page);
      expect(
        await rowLabels(marginSection(page, "development")),
        `Development's rows are out of order at ${width} px`
      ).toEqual(ONE_DEVELOPMENT_ROWS);
      const labels = detailsRow(page, "Labels").locator("[data-author-text]");
      await expect
        .poll(async () =>
          (await labels.allTextContents()).filter((label) => TWENTY_LABELS.includes(label))
        )
        .toEqual(TWENTY_LABELS);
      for (const label of TWENTY_LABELS) {
        await assertWhole(detailsRow(page, "Labels").getByText(label, { exact: true }));
      }
      const values: readonly (readonly [string, Locator])[] = [
        ["the repository", detailsRow(page, "Repository").getByText(REPOSITORY).first()],
        [
          "the pull request",
          detailsRow(page, "Pull request").getByRole("link", {
            name: new RegExp(`#${PULL_NUMBER}\\b`),
          }),
        ],
        ["the other link", detailsRow(page, "Links").locator(`a[href="${OTHER_LINK}"]`)],
        ["the component", detailsRow(page, "Components").getByText(COMPONENT_TITLE)],
        [
          "the claim's holder",
          detailsRow(page, "Claimed by").getByRole("link", { exact: true, name: PLANNER_TITLE }),
        ],
        [
          "the release",
          detailsRow(page, "Claimed by").getByRole("button", {
            name: `Release the claim on ${one}`,
          }),
        ],
        [
          "the route's control",
          detailsRow(page, "Messages go to").getByRole("button", {
            name: "Messages default to role:architect",
          }),
        ],
        ["the priority's link", detailsRow(page, "Advances").getByRole("link").first()],
        ["who opened it", detailsRow(page, "Opened by")],
        ["the approval step", detailsRow(page, "Approval")],
        [
          "Referenced by's count",
          marginSection(page, "references").getByRole("button", { name: /Referenced by/ }),
        ],
        [
          "a document's link",
          marginSection(page, "references").getByRole("link", { name: /notes-01\.md/ }),
        ],
      ];
      for (const [what, at] of values) {
        await expect(at, `${what} is not drawn in Details at ${width} px`).toBeVisible();
        await expectInsideSideways(page, at, `${what} at ${width} px`);
      }
      await expect(detailsRow(page, "Blocked by")).toContainText(three);
      await expect(detailsRow(page, "Blocked by")).toContainText(seeded.titles.three);
      for (const label of [...ONE_DEVELOPMENT_ROWS, "Opened by", "Approval"]) {
        await expect(
          detailsRow(page, label).getByText(label, { exact: true }),
          `the ${label} row draws its name other than once at ${width} px`
        ).toHaveCount(1);
      }
      await closeDetails(page);
    }

    // (6) Blocks, and Files and documents, fold after ten behind Show all with the count.
    await gotoIssue(page, `/issues/${one}`);
    await openDetails(page);
    const blocking = detailsRow(page, "Blocks").getByRole("link");
    await expect(blocking).toHaveCount(10);
    await detailsRow(page, "Blocks")
      .getByRole("button", { name: `${CONTROL_LABELS.show_all} ${ELEVEN}` })
      .click();
    await expect(blocking).toHaveCount(ELEVEN);
    for (const key of blocks) {
      const link = detailsRow(page, "Blocks").getByRole("link", { exact: true, name: key });
      expect(
        Number(await link.evaluate((element) => getComputedStyle(element).fontWeight)),
        `${key} is not bold in Blocks`
      ).toBeGreaterThanOrEqual(600);
    }
    await expect(detailsRow(page, "Blocks")).toContainText(`Waits on ONE (${ELEVEN})`);
    const documents = marginSection(page, "references").locator('a[href*="/artifacts/"]');
    await expect(documents).toHaveCount(10);
    await marginSection(page, "references")
      .getByRole("button", { name: `${CONTROL_LABELS.show_all} ${ELEVEN}` })
      .click();
    await expect(documents).toHaveCount(ELEVEN);

    if (!desktopProject) return;
    // (7) At 1440 px, the prototype's sizes and the page's own colours.
    await page.setViewportSize(viewportAt(1440));
    await gotoIssue(page, `/issues/${one}`);
    await openDetails(page);
    const row = await detailsRow(page, "Parent").evaluate((element) => {
      const style = getComputedStyle(element);
      const name = element.firstElementChild?.getBoundingClientRect().width ?? 0;
      return {
        fontSize: style.fontSize,
        lineHeight: style.lineHeight,
        name: Math.round(name),
        paddingBottom: style.paddingBottom,
        paddingTop: style.paddingTop,
      };
    });
    expect(row).toEqual({
      fontSize: "14px",
      lineHeight: "20px",
      name: 112,
      paddingBottom: "8px",
      paddingTop: "8px",
    });
    const chip = await detailsRow(page, "Labels")
      .getByText(TWENTY_LABELS[0] ?? "", { exact: true })
      .evaluate((element) => {
        const style = getComputedStyle(element);
        return {
          fontSize: style.fontSize,
          fontWeight: style.fontWeight,
          lineHeight: style.lineHeight,
          radius: style.borderTopLeftRadius,
        };
      });
    expect(chip).toEqual({
      fontSize: "12px",
      fontWeight: "500",
      lineHeight: "18px",
      radius: "6px",
    });
    const links: readonly (readonly [string, Locator])[] = [
      ["the parent", detailsRow(page, "Parent").getByRole("link", { exact: true, name: parent })],
      [
        "a blocker's key",
        detailsRow(page, "Blocked by").getByRole("link", { exact: true, name: three }),
      ],
      ["a key of Blocks", blocking.first()],
      ["a pin", pinsList(page).getByRole("link").first()],
    ];
    for (const [what, link] of links) {
      const colours = await link.evaluate((element) => {
        const style = getComputedStyle(element);
        return { colour: style.color, underline: style.textDecorationColor };
      });
      expect(colours, `${what} is not the ink, underlined in the grey`).toEqual({
        colour: INK,
        underline: UNDERLINE,
      });
    }
    expect(
      await documents.first().evaluate((element) => getComputedStyle(element).color),
      "a document's entry in Details is not the ink"
    ).toBe(INK);
    const approve = detailsRow(page, "Approval").getByRole("button", {
      exact: true,
      name: "Approve",
    });
    expect(
      await approve.evaluate((element) => {
        const style = getComputedStyle(element);
        return { background: style.backgroundColor, colour: style.color };
      })
    ).toEqual({ background: INK, colour: "rgb(255, 255, 255)" });
    const axe = await new AxeBuilder({ page })
      .withTags(AXE_TAGS)
      .include("aside[data-details]")
      .analyze();
    expect(axe.violations.map((violation) => violation.id)).toEqual([]);
  });
});

test("each control in Details saves and reads back", async ({ browser }) => {
  test.setTimeout(300_000);
  await phase(browser, seedDetailsIssue, async (page, seeded) => {
    const { one, three, two } = seeded.keys;
    const reopen = async (key: string) => {
      await gotoIssue(page, `/issues/${key}`);
      await openDetails(page);
    };

    // (8) The labels: one removed and one added, then `l` from a closed Details.
    await reopen(one);
    const editLabels = detailsRow(page, "Labels").getByRole("button", { name: "Edit labels" });
    await editLabels.click();
    await page.getByRole("option", { exact: true, name: "label-20" }).click();
    const search = page.getByRole("combobox", { name: "Search or create label" });
    await search.fill("label-new");
    await expect(page.getByRole("option", { name: 'Create "label-new"' })).toBeVisible();
    await search.press("Enter");
    await editLabels.click();
    const relabelled = [...TWENTY_LABELS.slice(0, 19), "label-new"];
    await expect
      .poll(async () => [...((await getIssue(one)).labels ?? [])].sort())
      .toEqual([...relabelled].sort());
    await reopen(one);
    await expect
      .poll(async () =>
        (await detailsRow(page, "Labels").locator("[data-author-text]").allTextContents()).filter(
          (label) => label.startsWith("label-")
        )
      )
      .toEqual(relabelled);
    await closeDetails(page);
    await page.keyboard.press("l");
    await expect(marginShell(page), "l did not open Details").toBeVisible();
    await expect(
      detailsRow(page, "Labels").getByRole("button", { name: "Edit labels" }),
      "l did not open Details at Edit labels"
    ).toHaveAttribute("aria-expanded", "true");
    await page.keyboard.press("Escape");

    // (9) TWO's parent, from Add, then the priority it inherits.
    await reopen(two);
    await expect(detailsRow(page, "Parent")).toContainText(CONTROL_LABELS.none);
    await expect(detailsRow(page, "Parent").getByRole("button").first()).toHaveText(
      CONTROL_LABELS.set_parent
    );
    await addControl(page).click();
    const entries = await openMenu(page).getByRole("menuitem").allTextContents();
    expect(entries.slice(0, 4), "the Add menu lists TWO's empty fields out of order").toEqual([
      "Parent",
      "Labels",
      "Messages go to",
      "Blocked by",
    ]);
    expect(
      entries.slice(4).filter((entry) => !["Repository", "Components", "Advances"].includes(entry)),
      "the Add menu lists a field R5 does not name"
    ).toEqual([]);
    expect(entries, "the Add menu lists the header's Assignee").not.toContain("Assignee");
    await openMenu(page).getByRole("menuitem", { exact: true, name: "Parent" }).click();
    await detailsRow(page, "Parent").getByLabel("Parent", { exact: true }).fill(one);
    await detailsRow(page, "Parent").getByRole("button", { name: "Save parent" }).click();
    await expect.poll(async () => (await getIssue(two)).parent).toBe(one);
    await reopen(two);
    const parentRow = detailsRow(page, "Parent");
    await expect(parentRow.getByRole("button").first()).toHaveText(CONTROL_LABELS.edit_parent);
    await expect(parentRow.getByRole("link", { exact: true, name: one })).toBeVisible();
    await expect(detailsRow(page, "Advances")).toContainText(CONTROL_LABELS.advances_inherited);

    // (10) TWO's route, and whether it reaches anyone.
    await detailsRow(page, "Messages go to")
      .getByRole("button", { name: "Messages default to no route" })
      .click();
    await detailsRow(page, "Messages go to")
      .getByLabel("Messages go to", { exact: true })
      .fill("role:implementer");
    await detailsRow(page, "Messages go to").getByRole("button", { name: "Save route" }).click();
    await expect.poll(async () => (await getIssue(two)).route).toBe("role:implementer");
    await reopen(two);
    await expect(
      detailsRow(page, "Messages go to").getByRole("button", {
        name: "Messages default to role:implementer",
      })
    ).toBeVisible();
    const reach = (await getIssue(two)).route_status;
    await expect(marginSlot(page).getByTestId("issue-details-route-unreachable")).toHaveCount(
      reach === "no_holder" ? 1 : 0
    );

    // (11) TWO's blockers: a refused key keeps the editor open, then THREE.
    const blockedBy = detailsRow(page, "Blocked by");
    await blockedBy.getByRole("button", { name: `Add issues ${two} waits on` }).click();
    const keys = blockedBy.getByLabel(`Issues ${two} waits on`, { exact: true });
    await keys.fill(`${PROJECT}-9999`);
    await blockedBy.getByRole("button", { name: CONTROL_LABELS.save_blocked_by }).click();
    await expect(blockedBy.getByRole("alert")).toHaveText(`There is no issue ${PROJECT}-9999.`);
    await expect(keys, "the refused list closed the editor").toBeVisible();
    await keys.fill(three);
    await blockedBy.getByRole("button", { name: CONTROL_LABELS.save_blocked_by }).click();
    await expect.poll(async () => (await getIssue(two)).blocked_by).toEqual([three]);
    await reopen(two);
    const blocker = detailsRow(page, "Blocked by").getByRole("link", { exact: true, name: three });
    await expect(blocker).toBeVisible();
    expect(
      Number(await blocker.evaluate((element) => getComputedStyle(element).fontWeight))
    ).toBeGreaterThanOrEqual(600);
    await expect(detailsRow(page, "Blocked by")).toContainText(seeded.titles.three);

    // (12) THREE's priority, added and removed.
    await reopen(three);
    await expect(detailsRow(page, "Advances")).toHaveCount(0);
    await marginSlot(page)
      .getByRole("button", { name: `${CONTROL_LABELS.add_priority} to ${three}` })
      .click();
    await marginSlot(page)
      .getByRole("combobox", { name: CONTROL_LABELS.priority_to_advance })
      .selectOption(seeded.priorityId);
    await marginSlot(page)
      .getByRole("button", { exact: true, name: CONTROL_LABELS.add })
      .last()
      .click();
    await expect.poll(async () => (await getIssue(three)).advances?.length ?? 0).toBe(1);
    await reopen(three);
    const added = detailsRow(page, "Advances").getByRole("link");
    await expect(added).toHaveCount(1);
    await added.click();
    await expect(page).toHaveURL(`/priorities/${seeded.priorityId}`);
    await reopen(three);
    await detailsRow(page, "Advances")
      .getByRole("button", { name: /^Remove / })
      .click();
    await expect.poll(async () => (await getIssue(three)).advances?.length ?? 0).toBe(0);
    await reopen(three);
    await expect(detailsRow(page, "Advances")).toHaveCount(0);

    // (13) Approval: its two controls where no request waits on alice, and bob's reason.
    await expect(detailsRow(page, "Approval")).toContainText(CHANGES_REASON);
    await reopen(one);
    const approval = detailsRow(page, "Approval");
    await expect(approval.getByRole("button", { exact: true, name: "Approve" })).toBeVisible();
    await approval.getByRole("button", { exact: true, name: "Request changes" }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await reopen(two);
    await expect(
      detailsRow(page, "Approval").getByRole("button", { name: /^(Approve|Request changes)$/ })
    ).toHaveCount(0);

    // (14) The claim, released.
    await reopen(one);
    await detailsRow(page, "Claimed by")
      .getByRole("button", { name: `Release the claim on ${one}` })
      .click();
    await expect.poll(async () => (await getIssue(one)).claim).toBeNull();
    await reopen(one);
    await expect(detailsRow(page, "Claimed by")).toContainText(CONTROL_LABELS.none);

    // (15) Every section opened on each load so far, and one closed is kept for the next issue.
    await reopen(two);
    await expect(marginSectionDisclosure(page, "development")).toHaveAttribute(
      "aria-expanded",
      "true"
    );
    await marginSectionDisclosure(page, "development").click();
    await expect(detailsRow(page, "Repository")).toHaveCount(0);
    expect(
      await page.evaluate(() => localStorage.getItem("dispatch.issue.margin-sections:alice"))
    ).toBe("development");
    await reopen(one);
    await expect(marginSectionDisclosure(page, "development")).toHaveAttribute(
      "aria-expanded",
      "false"
    );
  });
});
