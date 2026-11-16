import { statusGlyph } from "../web/src/features/issue-page/issue-status";
import { CONTROL_LABELS } from "../web/src/features/issue-page/page-words";
import {
  createAsk,
  createIssue,
  createIssueArtifact,
  createProject,
  editArtifact,
  getArtifact,
  getArtifactVersion,
  getAsk,
  getIssue,
  patchIssue,
  requestApproval,
  reviewArtifact,
  setGatedSteps,
} from "./api";
import { closeDetails, gotoIssue, phase } from "./issue-page";
import { withIssuePage } from "./issue-page-option";
import {
  actionsMenuDrop,
  asAgent,
  badgeValue,
  DESKTOP,
  details,
  expectHeaderDrawn,
  expectHeaderWithinItsLimit,
  firstControlOfRowOne,
  HEADER_PROJECT,
  headerFamilies,
  headerLabels,
  headerParts,
  IPHONE_13,
  identityBand,
  issueDecisions,
  issueHeader,
  labelsCutAsTheLineNarrows,
  labelsLeft,
  lineOrder,
  MEASURED_TITLE,
  propertiesLine,
  seedHeader,
  shortControls,
  stickyReading,
  TWENTY_LABELS,
  TYPEFACE,
  timesDrawn,
  titleOfLines,
  typefaceAddress,
  typefaceStatuses,
  undersizedTargets,
  viewportAt,
  waitingCount,
  waitingPressInHistory,
} from "./issue-page-p";
import { leaveFocus, onBody } from "./keyboard-issue";
import { expect, type Locator, type Page, type Request, test } from "./test";
import { assertPageFits, assertWhole } from "./unclipped";
import { asUser } from "./users";

/**
 * The new issue page's header (dispatch://INFRA-3943, part P of dispatch://INFRA-1196): R13's
 * list and its limits, the approval control, the typeface's fallback, the count of what waits,
 * the title's editor, the keys, and what stays as the page scrolls. Each row turns the new page
 * on (`phase`), first waits for the header's properties line (`expectHeaderDrawn`), and reads
 * the header alone: no row waits for a row of the outline.
 */

/** The person the server resolves the ask `askId` to (`waiting_on_person`): whoever it was handed
 *  to, else the issue's assignee, else the project's default assignee. `GET /api/v1/asks/{id}`
 *  answers `AskRead`, whose `ask` carries `waiting_on_person`, resolved at read time: the stored
 *  row holds no such field (`packages/contracts/src/dispatch-api-asks.ts`). */
async function waitingOnPersonOf(askId: string): Promise<string | undefined> {
  return (await getAsk(askId)).ask.waiting_on_person;
}

/** The page's own address of the view a press of the count opens. */
function decisionsAddress(key: string): RegExp {
  return new RegExp(`/issues/${key}/decisions$`);
}

/** Whether `request` is a write of `key`'s own fields whose body holds `field`. */
function writesField(request: Request, key: string, field: string): boolean {
  if (request.method() !== "PATCH" || new URL(request.url()).pathname !== `/api/v1/issues/${key}`)
    return false;
  const body: unknown = request.postDataJSON();
  return typeof body === "object" && body !== null && field in body;
}

/** The review actions of an approval inside `scope`: Approve (`Approve v2` on a stale approval)
 *  and Request changes. The chip itself (`Approved v1`) is none of them. */
function reviewActions(scope: Locator): Locator {
  return scope.getByRole("button", { name: /^(Approve( v\d+)?|Request changes)$/ });
}

/** The words the labels' count reads with `left` labels it does not draw: `+3 labels`. */
function labelsLeftWords(left: number): string {
  return `+${left} ${left === 1 ? CONTROL_LABELS.labels_left_one : CONTROL_LABELS.labels_left_many}`;
}

/** How a row presses a control: the mouse's click, a tap of the phone's touch screen, or Enter
 *  with the focus on it. */
type Press = "click" | "Enter" | "tap";

/** Presses `control` as the row's project and width do it. */
async function press(control: Locator, how: Press): Promise<void> {
  if (how === "click") {
    await control.click();
    return;
  }
  if (how === "tap") {
    await control.tap();
    return;
  }
  await control.press("Enter");
}

/** A real press on `control` straight out of the title's edit, `what` naming it: on `iphone` a
 *  tap, whose mouse events follow its touch events, and on `chromium` the mouse's down and up at
 *  its middle. */
async function pressOutOfTheEdit(
  page: Page,
  control: Locator,
  what: string,
  tap: boolean
): Promise<void> {
  if (tap) {
    await control.tap();
    return;
  }
  const box = await control.boundingBox();
  if (box === null) throw new Error(`${what} is not drawn while the title is edited`);
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down();
  await page.mouse.up();
}

// c-header (1): the header's half of the checkpoint's "every property with a value is visible at
// every width @phone" (`e2e/issue-layout.e2e.ts:2988`), on the measured title and 20 labels.
// On the phone it reads what the 390 px viewport decides (R1 of "Phone acceptance" in
// `packages/dispatch/AGENTS.md`): R3's limit there, the 24 px targets, the page no wider than
// the window, the navigation's control first on row one and the dialog it opens, and the 44 px
// buttons and 16 px type of the dialog a header control opens.
test("the header holds R13's list and its limits at every width @phone", async ({ browser }) => {
  test.setTimeout(300_000);
  await phase(browser, seedHeader, async (page, seeded) => {
    await patchIssue(seeded.key, { labels: [...TWENTY_LABELS], title: MEASURED_TITLE });
    const desktopProject = test.info().project.name !== "iphone";

    // No approval request waits on the reader: the approval control reads Not requested and
    // carries no Approve, and the header holds its limit for that case.
    for (const width of desktopProject ? [DESKTOP.width, IPHONE_13.width] : [IPHONE_13.width]) {
      await page.setViewportSize(viewportAt(width));
      await gotoIssue(page, `/issues/${seeded.key}`);
      await expectHeaderDrawn(page);
      await expect(issueHeader(page)).toContainText(MEASURED_TITLE);
      await expect(issueHeader(page)).toContainText(CONTROL_LABELS.not_requested);
      await expect(issueHeader(page).getByRole("button", { name: "Approve" })).toHaveCount(0);
      await expectHeaderWithinItsLimit(page, width, false, TYPEFACE);
    }

    // An approval request that waits on the reader.
    await requestApproval(seeded.specArtifactId, { summary: "Read it back." });
    // R13's list, each part of the header by what it is: nothing else may be drawn there. Its one
    // link is the parent's, named exactly.
    const named = [
      new RegExp(`^a ${seeded.parentKey}$`),
      /^button Open navigation$/,
      /^button Copy issue key /,
      /^button Issue actions$/,
      /^button Details$/,
      /^h1 /,
      /^select Status$/,
      new RegExp(`^select Priority of ${seeded.key}$`),
      new RegExp(`^select Assignee of ${seeded.key}$`),
      /^issue-header$/,
      /^issue-details-control$/,
      /^issue-state-actions$/,
      /^issue-header-labels$/,
      /^issue-labels-left$/,
      /^issue-progress-children$/,
      /^issue-progress-subtree$/,
      /^issue-whose-turn$/,
      /^issue-approval-steps$/,
      /^button (Awaiting approval|Approve|Request changes)/,
      /^issue-route-unreachable$/,
    ];

    let drawnAtDesktop: number | undefined;
    for (const width of desktopProject ? [1440, 1280, 1024, 390] : [390]) {
      await page.setViewportSize(viewportAt(width));
      await gotoIssue(page, `/issues/${seeded.key}`);
      await expectHeaderDrawn(page);
      await expect(issueHeader(page)).toContainText(MEASURED_TITLE);
      await page.evaluate(() => window.scrollTo(0, 0));

      // The header's own values, each where it is drawn, with no control opened.
      const header = issueHeader(page);
      const inTheHeader: readonly (readonly [string, Locator, (string | RegExp)?])[] = [
        ["the parent", header.getByRole("link", { exact: true, name: seeded.parentKey })],
        ["the status", header.getByRole("combobox", { name: "Status" })],
        ["the priority", badgeValue(page, `Priority of ${seeded.key}`), "P1"],
        ["the assignee", badgeValue(page, `Assignee of ${seeded.key}`), "alice"],
        ["the first label", headerLabels(page).first(), TWENTY_LABELS[0]],
        ["the children's progress", header.getByTestId("issue-progress-children")],
        ["the whole tree's progress", header.getByTestId("issue-progress-subtree")],
        ["the count of what waits", waitingCount(page), /^Waiting on you \(\d+\)$/],
        ["Approve", header.getByRole("button", { exact: true, name: "Approve" })],
        ["Request changes", header.getByRole("button", { exact: true, name: "Request changes" })],
      ];
      for (const [name, at, words] of inTheHeader) {
        await expect(at, `${name} is not drawn in the header at ${width} px`).toBeVisible();
        if (words !== undefined) {
          await expect(at, `${name} does not read "${words}" at ${width} px`).toHaveText(words);
        }
        await expect(at, `${name} is outside the ${width} px viewport`).toBeInViewport();
        await assertWhole(at);
      }
      expect(
        await header.getByRole("combobox", { name: "Status" }).inputValue(),
        `the status reads otherwise at ${width} px`
      ).toBe("in_progress");
      // The status draws its mark beside its word.
      await expect(
        header.locator('label:has(> select[aria-label="Status"]) > [aria-hidden="true"]'),
        `the status draws another mark at ${width} px`
      ).toHaveText(statusGlyph("in_progress"));
      // Approve is filled with the page's ink, its words the paper's (row 17).
      expect(
        await header
          .getByRole("button", { exact: true, name: "Approve" })
          .evaluate((approve) => [
            getComputedStyle(approve).backgroundColor,
            getComputedStyle(approve).color,
          ]),
        `Approve is not drawn in the ink at ${width} px`
      ).toEqual(["rgb(28, 25, 23)", "rgb(255, 255, 255)"]);

      // The header draws R13's list and nothing else, in R1's order on its line.
      const parts = await headerParts(page);
      expect(
        parts.filter((part) => !named.some((pattern) => pattern.test(part))),
        `the header draws parts R13 does not name at ${width} px`
      ).toEqual([]);
      const order = [
        "select Status",
        `select Priority of ${seeded.key}`,
        `select Assignee of ${seeded.key}`,
        "issue-progress-children",
        "issue-whose-turn",
        "issue-header-labels",
      ];
      expect(
        parts.filter((part) => order.includes(part)),
        `the properties line is out of R1's order at ${width} px`
      ).toEqual(order);
      // The approval control stands after the count and before the labels.
      expect(
        await lineOrder(page, [
          { name: "the count", selector: '[data-testid="issue-whose-turn"]' },
          { name: "the approval control", selector: "[data-approval-artifact]" },
          { name: "the labels", selector: '[data-testid="issue-header-labels"]' },
        ]),
        `the approval control is not between the count and the labels at ${width} px`
      ).toEqual(["the count", "the approval control", "the labels"]);

      // The page draws the key and the title one time, the title as its one `h1`.
      await expect(
        page.locator("[data-issue-page] h1"),
        `the issue page does not hold one h1 at ${width} px`
      ).toHaveCount(1);
      expect(
        await timesDrawn(page.locator("[data-issue-page]"), MEASURED_TITLE),
        "the page draws the title more than once"
      ).toBe(1);
      expect(
        await timesDrawn(header, seeded.key),
        `the header does not draw the key once at ${width} px`
      ).toBe(1);

      // Where the shell draws no rail, row one starts with the navigation's control; beside the
      // rail the header draws none.
      if (width === IPHONE_13.width) {
        expect(
          await firstControlOfRowOne(page),
          "the navigation's control is not the first control of row one at 390 px"
        ).toBe("Open navigation");
      }
      if (width === DESKTOP.width) {
        await expect(
          header.getByRole("button", { exact: true, name: "Open navigation" }),
          "the navigation's control is drawn beside the rail at 1440 px"
        ).toHaveCount(0);
      }

      // The labels that fit, and a count equal to the labels the line does not draw.
      await expect(labelsLeft(page), `the labels end on no count at ${width} px`).toBeVisible();
      const drawn = await headerLabels(page).count();
      expect(drawn, `the header draws every label at ${width} px`).toBeLessThan(20);
      if (width === DESKTOP.width) drawnAtDesktop = drawn;
      await expect(labelsLeft(page)).toHaveText(labelsLeftWords(TWENTY_LABELS.length - drawn));

      // The frame after the line narrows, drawn before the labels are read again on the next
      // frame, draws each label whole or not at all: one that no longer fits wraps onto a line
      // the labels' box clips away. The line then takes its width back, and the labels are read
      // again before the next step.
      if (width === DESKTOP.width || width === IPHONE_13.width) {
        expect(
          await labelsCutAsTheLineNarrows(page, 60),
          `a label is cut in the frame after the line narrowed at ${width} px`
        ).toEqual([]);
        await propertiesLine(page).evaluate((line) => line.style.removeProperty("max-width"));
        let before = -1;
        await expect
          .poll(
            async () => {
              const now = await headerLabels(page).count();
              const words = ((await labelsLeft(page).textContent()) ?? "").replace(/\s+/g, " ");
              const settled =
                now === before && words.trim() === labelsLeftWords(TWENTY_LABELS.length - now);
              before = now;
              return settled;
            },
            { message: `the labels were not read again once the line widened at ${width} px` }
          )
          .toBe(true);
      }

      await expectHeaderWithinItsLimit(page, width, true, TYPEFACE);
      expect(
        await undersizedTargets(page),
        `a header control fails WCAG 2.5.8 at ${width} px`
      ).toEqual([]);
      expect(
        await shortControls(page, seeded.key),
        `a control R3 holds to 24 px is shorter at ${width} px`
      ).toEqual([]);
      await assertPageFits(page);
      if (width === IPHONE_13.width) {
        // A dialog a header control opens keeps the page's floors at 390 px: 16 px type in its
        // fields, so a phone does not zoom into them, and 44 px buttons (`web/src/styles.css`).
        await header.getByRole("button", { exact: true, name: "Request changes" }).click();
        const requestChanges = page.getByRole("dialog", { name: "Request changes" });
        await expect(requestChanges, "Request changes opened no dialog at 390 px").toBeVisible();
        expect(
          await requestChanges
            .locator("textarea")
            .evaluate((reason) => getComputedStyle(reason).fontSize),
          "the Request changes dialog's reason box is under 16 px at 390 px"
        ).toBe("16px");
        const heights = await requestChanges
          .locator("button")
          .evaluateAll((buttons) => buttons.map((button) => button.getBoundingClientRect().height));
        expect(
          heights.length,
          "the Request changes dialog draws no button at 390 px"
        ).toBeGreaterThan(0);
        expect(
          heights.filter((height) => height < 44),
          "a button of the Request changes dialog is under 44 px at 390 px"
        ).toEqual([]);
        await page.keyboard.press("Escape");
        await expect(requestChanges, "Escape left the Request changes dialog open").toHaveCount(0);
      }

      // The labels' count, Details' control and the menu's parent action - Edit parent, on an
      // issue with a parent - each open Details.
      await labelsLeft(page).click();
      await expect(details(page), "the labels' count opened no Details").toBeVisible();
      await closeDetails(page);
      await header.getByTestId("issue-details-control").click();
      await expect(details(page), "Details' control opened no Details").toBeVisible();
      await closeDetails(page);
      await header.getByRole("button", { exact: true, name: CONTROL_LABELS.issue_actions }).click();
      // The open menu hangs under its control, so a second press on the control reaches the
      // control, which closes the menu, rather than the menu's first action.
      expect(
        await actionsMenuDrop(page),
        `the actions menu opens over its own control at ${width} px`
      ).toBeGreaterThanOrEqual(-0.5);
      await header.getByRole("button", { exact: true, name: CONTROL_LABELS.edit_parent }).click();
      await expect(
        details(page),
        `the menu's parent action opened no Details at ${width} px`
      ).toBeVisible();
      await closeDetails(page);
      if (width === IPHONE_13.width) {
        await header.getByRole("button", { exact: true, name: "Open navigation" }).click();
        await expect(
          page.getByRole("dialog"),
          "the navigation's control opened no dialog at 390 px"
        ).toBeVisible();
        await page.keyboard.press("Escape");
      }
    }

    // At 1440 px, the labels the line drew there of the twenty and one more end on the count of
    // one label.
    if (drawnAtDesktop !== undefined) {
      await page.setViewportSize(viewportAt(DESKTOP.width));
      await patchIssue(seeded.key, { labels: TWENTY_LABELS.slice(0, drawnAtDesktop + 1) });
      await gotoIssue(page, `/issues/${seeded.key}`);
      await expectHeaderDrawn(page);
      await expect(
        labelsLeft(page),
        `${drawnAtDesktop + 1} labels at 1440 px do not end on "+1 ${CONTROL_LABELS.labels_left_one}"`
      ).toHaveText(`+1 ${CONTROL_LABELS.labels_left_one}`);
    }

    // An issue whose labels fit draws them all and no count.
    await page.setViewportSize(viewportAt(DESKTOP.width));
    await patchIssue(seeded.key, { labels: ["dispatch"] });
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectHeaderDrawn(page);
    await expect(headerLabels(page)).toHaveText(["dispatch"]);
    await expect(labelsLeft(page), "a label that fits ends on a count").toHaveCount(0);
    await expect(issueHeader(page).getByTestId("issue-route-unreachable")).toHaveCount(0);

    // A child of the issue, which has no children of its own, draws no children's progress.
    await gotoIssue(page, `/issues/${seeded.childKeys[0]}`);
    await expectHeaderDrawn(page);
    await expect(
      issueHeader(page).getByTestId("issue-progress-children"),
      "an issue with no children draws its progress"
    ).toHaveCount(0);

    // The parent, whose route reaches nobody, draws the header's one marker, after its approval
    // control and before its one label.
    await patchIssue(seeded.parentKey, { labels: ["dispatch"] });
    await gotoIssue(page, `/issues/${seeded.parentKey}`);
    await expectHeaderDrawn(page);
    await expect(issueHeader(page).getByTestId("issue-route-unreachable")).toHaveCount(1);
    await expect(issueHeader(page).getByRole("button", { name: "Approve" })).toHaveCount(0);
    await expect(issueHeader(page)).toContainText(CONTROL_LABELS.not_requested);
    await expect(headerLabels(page)).toHaveText(["dispatch"]);
    expect(
      await lineOrder(page, [
        { name: "the approval control", selector: "span", words: CONTROL_LABELS.not_requested },
        { name: "the route's marker", selector: '[data-testid="issue-route-unreachable"]' },
        { name: "the labels", selector: '[data-testid="issue-header-labels"]' },
      ]),
      `the route's marker is not between "${CONTROL_LABELS.not_requested}" and the labels`
    ).toEqual(["the approval control", "the route's marker", "the labels"]);
  });
});

// c-header (2): the checkpoint's "the approval entries stay in the identity block and read the
// node each step approves" (`e2e/issue-layout.e2e.ts:3641`).
test("the approval entries stay in the header and read the document each step approves", async ({
  browser,
}) => {
  test.setTimeout(150_000);
  await phase(browser, seedHeader, async (page) => {
    await createProject({ key: "GATED", name: "Gated" });
    await setGatedSteps("GATED", ["decisions", "criteria", "spec"]);
    await createProject({ key: "PLAIN", name: "Plain" });

    // A root of a project that gates steps: one entry per step, in the project's order, each
    // reading its own document.
    const root = await createIssue({ project: "GATED", spec: "The plan.", title: "Gated root" });
    const decisions = await createIssueArtifact(root.key, {
      content: "# Decisions\n\nHTTP it is.\n",
      name: "decisions.md",
    });
    await reviewArtifact(decisions.artifact.id, { state: "approved" });
    await requestApproval(root.primary_artifact_id ?? "", { summary: "Read it." });
    await gotoIssue(page, `/issues/${root.key}`);
    await expectHeaderDrawn(page);
    const steps = issueHeader(page).getByTestId("issue-approval-steps");
    await expect
      .poll(async () =>
        steps
          .locator("[data-approval-step]")
          .evaluateAll((entries) =>
            entries.map((entry) => entry.getAttribute("data-approval-step"))
          )
      )
      .toEqual(["decisions", "criteria", "spec"]);
    const entry = (step: string) => steps.locator(`[data-approval-step="${step}"]`);
    await expect(entry("decisions").getByRole("button", { name: "Approved v1" })).toBeVisible();
    await expect(entry("criteria")).toContainText(CONTROL_LABELS.not_requested);
    await expect(entry("spec").getByRole("button", { name: "Awaiting approval" })).toBeVisible();
    // The approval the request asks for pins a version of the spec, and that version is the
    // spec's own text.
    const awaiting = await getArtifact(root.primary_artifact_id ?? "");
    const pinned = awaiting.approval?.version ?? awaiting.approval?.latest_version ?? 0;
    expect(pinned, "the approval pins no version of the spec").toBeGreaterThan(0);
    expect(
      (await getArtifactVersion(root.primary_artifact_id ?? "", pinned)).markdown,
      "the document version the approval pins does not resolve to the spec's own text"
    ).toContain("The plan.");
    // The request waits on the reader, so the spec's entry carries Approve in the header (R31),
    // and the approval it records reads back.
    await entry("spec").getByRole("button", { exact: true, name: "Approve" }).click();
    await expect
      .poll(async () => (await getArtifact(root.primary_artifact_id ?? "")).approval?.state)
      .toBe("approved");
    await expect(entry("spec").getByRole("button", { exact: true, name: "Approve" })).toHaveCount(
      0
    );
    await expect(
      issueHeader(page).getByRole("button", { exact: true, name: "Request changes" }),
      "the header draws Request changes with no request waiting"
    ).toHaveCount(0);

    // A request of the Verification document waits on the reader: its own entry carries Approve
    // and Request changes, and the spec's, whose request was answered, carries neither
    // (decision 58). The count's press puts the focus on the entry of the document that waits,
    // and its Approve answers that document's request.
    const verification = await createIssueArtifact(root.key, {
      content: "# Verification\n\nOne check.\n",
      name: "verification.md",
    });
    await requestApproval(verification.artifact.id, { summary: "Read it." });
    await gotoIssue(page, `/issues/${root.key}`);
    await expectHeaderDrawn(page);
    for (const action of ["Approve", "Request changes"]) {
      await expect(
        entry("criteria").getByRole("button", { exact: true, name: action }),
        "the Verification document's entry carries no Approve while its request waits"
      ).toBeVisible();
    }
    await expect(
      reviewActions(entry("spec")),
      "the spec's entry carries actions though no request of it waits"
    ).toHaveCount(0);
    await expect(waitingCount(page)).toHaveText("Waiting on you (1)");
    await waitingCount(page).click();
    await expect
      .poll(() =>
        page.evaluate(() => document.activeElement?.getAttribute("data-approval-artifact"))
      )
      .toBe(verification.artifact.id);
    await entry("criteria").getByRole("button", { exact: true, name: "Approve" }).click();
    await expect
      .poll(async () => (await getArtifact(verification.artifact.id)).approval?.state)
      .toBe("approved");
    await expect(reviewActions(issueHeader(page))).toHaveCount(0);

    // A root of a project that gates nothing carries the one chip, "Not requested".
    const plain = await createIssue({ project: "PLAIN", spec: "The plan.", title: "Plain root" });
    await gotoIssue(page, `/issues/${plain.key}`);
    await expectHeaderDrawn(page);
    await expect(issueHeader(page).getByTestId("issue-approval-steps")).toHaveCount(0);
    await expect(issueHeader(page)).toContainText(CONTROL_LABELS.not_requested);

    // Every child carries the one chip, and no list of steps: the child of a parent not yet
    // approved, of a parent whose spec changed since its approval, and of an approved parent.
    const oneChip = async (key: string) => {
      await gotoIssue(page, `/issues/${key}`);
      await expectHeaderDrawn(page);
      await expect(issueHeader(page).getByTestId("issue-approval-steps")).toHaveCount(0);
      await expect(
        issueHeader(page).getByText(CONTROL_LABELS.not_requested, { exact: true }),
        `the child's header draws no "${CONTROL_LABELS.not_requested}"`
      ).toHaveCount(1);
    };
    const childOf = (parent: string, title: string) =>
      createIssue({ parent, project: "PLAIN", spec: "The child's plan.", title });
    const unapproved = await createIssue({
      project: "PLAIN",
      spec: "Not yet.",
      title: "Unapproved",
    });
    await oneChip((await childOf(unapproved.key, "Child of an unapproved parent")).key);
    const changed = await createIssue({
      project: "PLAIN",
      spec: "Before.",
      title: "Changed since",
    });
    await reviewArtifact(changed.primary_artifact_id ?? "", { state: "approved" });
    await editArtifact(changed.primary_artifact_id ?? "", {
      ops: [{ find: "Before.", op: "replace", with: "After." }],
    });
    expect((await getArtifact(changed.primary_artifact_id ?? "")).approval?.state).toBe("stale");
    await oneChip((await childOf(changed.key, "Child of a changed parent")).key);
    await requestApproval(plain.primary_artifact_id ?? "", { summary: "Read it." });
    await reviewArtifact(plain.primary_artifact_id ?? "", { state: "approved" });
    const child = await childOf(plain.key, "Child of an approved parent");
    await oneChip(child.key);

    // A child with an approval request open answers it from its chip in the header.
    await requestApproval(child.primary_artifact_id ?? "", { summary: "Read it." });
    await gotoIssue(page, `/issues/${child.key}`);
    await expectHeaderDrawn(page);
    await issueHeader(page).getByRole("button", { exact: true, name: "Approve" }).click();
    await expect
      .poll(async () => (await getArtifact(child.primary_artifact_id ?? "")).approval?.state)
      .toBe("approved");

    // The plain root's spec approved, written again, and sent back with no request open: nothing
    // waits on the reader, so the header draws neither action.
    await editArtifact(plain.primary_artifact_id ?? "", {
      ops: [{ find: "The plan.", op: "replace", with: "The plan, written again." }],
    });
    await reviewArtifact(plain.primary_artifact_id ?? "", {
      reason: "Name the rollback path.",
      state: "changes_requested",
    });
    await gotoIssue(page, `/issues/${plain.key}`);
    await expectHeaderDrawn(page);
    await expect(
      issueHeader(page).getByRole("button", { name: "Changes requested" }),
      "the plain root's spec does not read its changes request"
    ).toBeVisible();
    await expect(
      reviewActions(issueHeader(page)),
      "the header draws an action with no request waiting"
    ).toHaveCount(0);
  });
});

// c-header (3): the checkpoint's "the header holds its rule when the typeface does not load
// @phone" (`e2e/issue-layout.e2e.ts:5714`), less part B's readings of the file and its licence.
// On the phone it reads what the 390 px viewport decides in the fallback, as row (1) reads it in
// the face (R1 of "Phone acceptance" in `packages/dispatch/AGENTS.md`): R3's limit and the 24 px
// targets at 390 px, and the page no wider than the window at 390 and 320 px.
test("the header holds its limits when the typeface does not load @phone", async ({ browser }) => {
  test.setTimeout(240_000);
  await phase(browser, seedHeader, async (page, seeded) => {
    await patchIssue(seeded.key, { labels: [...TWENTY_LABELS], title: MEASURED_TITLE });

    // The page is drawn in the typeface.
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectHeaderDrawn(page);
    await expect
      .poll(() => typefaceStatuses(page), {
        message: `the document's font set holds no ${TYPEFACE} face that loaded`,
      })
      .toEqual(["loaded"]);
    expect(
      (await headerFamilies(page))[0],
      `the header's text is not drawn in ${TYPEFACE} first`
    ).toBe(TYPEFACE);
    const address = await typefaceAddress(page);
    if (address === null) {
      throw new Error(`the page's stylesheet declares no @font-face for ${TYPEFACE}`);
    }

    // With no request waiting and then one, the header holds its limits in the typeface at
    // 1440 px and 390 px; and with every request for its file refused, in the fallback, where
    // the document is also no wider than the window at four widths.
    const blocked = await asUser(browser, "alice");
    try {
      await withIssuePage(blocked, "alice");
      let refused = 0;
      await blocked.route(address, async (route) => {
        refused += 1;
        await route.abort();
      });
      const without = await blocked.newPage();
      for (const requestWaiting of [false, true]) {
        if (requestWaiting) {
          await requestApproval(seeded.specArtifactId, { summary: "Read it back." });
        }
        for (const width of [DESKTOP.width, IPHONE_13.width]) {
          await page.setViewportSize(viewportAt(width));
          await gotoIssue(page, `/issues/${seeded.key}`);
          await expectHeaderDrawn(page);
          await expect(issueHeader(page)).toContainText(MEASURED_TITLE);
          await expect
            .poll(() => typefaceStatuses(page), {
              message: `the ${TYPEFACE} face did not load at ${width} px`,
            })
            .toEqual(["loaded"]);
          await expectHeaderWithinItsLimit(page, width, requestWaiting, TYPEFACE);
        }
        for (const width of [DESKTOP.width, 1024, IPHONE_13.width, 320]) {
          await without.setViewportSize(viewportAt(width));
          await gotoIssue(without, `/issues/${seeded.key}`);
          await expectHeaderDrawn(without);
          await expect(issueHeader(without)).toContainText(MEASURED_TITLE);
          await expect
            .poll(() => typefaceStatuses(without), {
              message: `the ${TYPEFACE} face did not fail at ${width} px, so this is not the fallback`,
            })
            .toEqual(["error"]);
          await expectHeaderWithinItsLimit(without, width, requestWaiting, "the fallback");
          if (requestWaiting && (width === DESKTOP.width || width === IPHONE_13.width)) {
            expect(
              await undersizedTargets(without),
              `a header control fails WCAG 2.5.8 at ${width} px in the fallback`
            ).toEqual([]);
            expect(
              await shortControls(without, seeded.key),
              `a control R3 holds to 24 px is shorter at ${width} px in the fallback`
            ).toEqual([]);
          }
          const fit = await without.evaluate(() => ({
            clientWidth: document.documentElement.clientWidth,
            scrollWidth: document.documentElement.scrollWidth,
          }));
          expect
            .soft(
              fit.scrollWidth,
              `the page scrolls sideways at ${width} px without the typeface: ${fit.scrollWidth} px of page in ${fit.clientWidth} px`
            )
            .toBe(fit.clientWidth);
        }
      }
      expect(refused, "no request for the typeface reached the refusal").toBeGreaterThan(0);
    } finally {
      await blocked.close();
    }
  });
});

// c-header (4): the count's half of the checkpoint's "the count of what waits on the reader
// opens the first thing that waits @phone" (`e2e/issue-layout.e2e.ts:3275`). The node the press
// reaches is parts I's and Q's. On the phone every press of the count is a tap of the touch
// screen (R2 of "Phone acceptance" in `packages/dispatch/AGENTS.md`), and each opens what the
// mouse's click opens on `chromium`.
test("the count of what waits is a control that opens the Decisions address @phone", async ({
  browser,
}) => {
  test.setTimeout(150_000);
  await phase(browser, seedHeader, async (page, seeded) => {
    const desktopProject = test.info().project.name !== "iphone";
    // The pointer the project presses with: the mouse on `chromium`, the touch screen on `iphone`.
    const pointer: Press = desktopProject ? "click" : "tap";
    for (const [width, how] of (desktopProject
      ? [
          [DESKTOP.width, "click"],
          [IPHONE_13.width, "Enter"],
        ]
      : [[IPHONE_13.width, "tap"]]) as readonly (readonly [number, Press])[]) {
      await page.setViewportSize(viewportAt(width));
      await gotoIssue(page, `/issues/${seeded.key}`);
      await expectHeaderDrawn(page);
      // Two questions wait on alice, and the count is a button.
      await expect(waitingCount(page)).toHaveText("Waiting on you (2)");
      expect(await waitingCount(page).evaluate((count) => count.tagName)).toBe("BUTTON");

      await press(waitingCount(page), how);
      await expect(page).toHaveURL(decisionsAddress(seeded.key));
      const first = await waitingPressInHistory(page);
      expect(first, `the press left no mark in the history entry at ${width} px`).not.toBeNull();
      const length = await page.evaluate(() => history.length);

      // A press made at that address replaces its entry, with a new mark.
      await press(waitingCount(page), how);
      await expect
        .poll(() => waitingPressInHistory(page), {
          message: `a second press left the first press's mark at ${width} px`,
        })
        .not.toBe(first);
      expect(
        await page.evaluate(() => history.length),
        `a second press added a history entry at ${width} px`
      ).toBe(length);

      // Back leaves the address.
      await page.goBack();
      await expect(page).toHaveURL(new RegExp(`/issues/${seeded.key}$`));
    }

    // An approval request is all that waits: the count puts the focus on the approval control.
    const lone = await createIssue({
      force: true,
      project: HEADER_PROJECT,
      spec: "Plan.",
      title: "Lone",
    });
    await requestApproval(lone.primary_artifact_id ?? "", { summary: "Read it." });
    await page.setViewportSize(viewportAt(DESKTOP.width));
    await gotoIssue(page, `/issues/${lone.key}`);
    await expectHeaderDrawn(page);
    await expect(waitingCount(page)).toHaveText("Waiting on you (1)");
    await press(waitingCount(page), pointer);
    await expect
      .poll(() =>
        page.evaluate(() => document.activeElement?.hasAttribute("data-approval-artifact"))
      )
      .toBe(true);
    expect(
      await page.evaluate(() => document.activeElement?.closest("[data-issue-header]") !== null),
      "the focus is on an approval control outside the header"
    ).toBe(true);

    // The spec approved and the Verification document's request alone waiting: the header draws
    // the spec's chip and no chip of the Verification document, so the press puts the focus on
    // no chip rather than on the spec's (decision 58). The press's handler moves the focus as it
    // runs, so the focus is read once the press is done.
    const verifying = await createIssue({
      force: true,
      project: HEADER_PROJECT,
      spec: "Plan.",
      title: "Lone Verification document",
    });
    const spec = verifying.primary_artifact_id ?? "";
    await reviewArtifact(spec, { state: "approved" });
    const verification = await createIssueArtifact(verifying.key, {
      content: "# Verification\n\nOne check.\n",
      name: "verification.md",
    });
    await requestApproval(verification.artifact.id, { summary: "Read it." });
    await gotoIssue(page, `/issues/${verifying.key}`);
    await expectHeaderDrawn(page);
    await expect(waitingCount(page)).toHaveText("Waiting on you (1)");
    await expect(
      issueHeader(page).locator(`[data-approval-artifact="${spec}"]`),
      "the header draws no chip of the approved spec"
    ).toBeVisible();
    await press(waitingCount(page), pointer);
    expect(
      [null, verification.artifact.id],
      "the count's press focused another document's chip"
    ).toContain(
      await page.evaluate(
        () => document.activeElement?.getAttribute("data-approval-artifact") ?? null
      )
    );

    // A decision on the review list is all that waits: the issue's decisions document holds an
    // agent's decision that names the requirement its spec holds, which the server reads as
    // waiting for review, and no ask of the issue is open. The count counts it, and its press
    // opens the Decisions address.
    const decided = await createIssue({
      force: true,
      project: HEADER_PROJECT,
      spec: ":::requirement{#r1}\nThe header counts a decision that waits.\n:::\n",
      title: "Decided",
    });
    await createIssueArtifact(decided.key, {
      content: `## The count reads the decisions\n\nThe header counts what the Decisions filter lists for review. It shapes dispatch://${decided.key}/spec#b-r1.\n`,
      name: "decisions.md",
    });
    expect(
      ((await issueDecisions(decided.key)).decisions ?? []).map((decision) => ({
        names: (decision.links ?? []).map((link) => link.to.block_id),
        waiting: decision.waiting_review,
      })),
      "the server does not read the agent's decision as waiting for review"
    ).toEqual([{ names: ["r1"], waiting: true }]);
    expect((await getIssue(decided.key)).open_asks ?? [], "an ask of the issue is open").toEqual(
      []
    );
    await gotoIssue(page, `/issues/${decided.key}`);
    await expectHeaderDrawn(page);
    await expect(waitingCount(page), "a decision on the review list is not counted").toHaveText(
      "Waiting on you (1)"
    );
    await press(waitingCount(page), pointer);
    await expect(
      page,
      "the count's press with a decision waiting did not open the Decisions address"
    ).toHaveURL(decisionsAddress(decided.key));
  });
});

// c-header (5): the checkpoint's "the count of what waits holds only what waits on the reader"
// (`e2e/issue-layout.e2e.ts:3368`), finding F4 of
// dispatch://INFRA-1196/artifact/tester-findings-md. A question waits on the person the server
// resolves it to - whoever it was handed to, else the issue's assignee.
test("the count of what waits holds only what waits on the reader", async ({ browser }) => {
  test.setTimeout(120_000);
  await phase(browser, seedHeader, async (alice) => {
    const issue = await createIssue({ force: true, project: HEADER_PROJECT, title: "Whose turn" });
    const bobs = await asUser(browser, "bob");
    try {
      await withIssuePage(bobs, "bob");
      const bob = await bobs.newPage();
      await bob.setViewportSize(viewportAt(DESKTOP.width));
      await patchIssue(issue.key, { assignee: "alice" });
      const asked = await createAsk(issue.key, { question: "Which way does it go?" }, asAgent);
      expect(await waitingOnPersonOf(asked.id)).toBe("alice");

      // The agent's question waits on alice, who holds the issue: her count, not bob's. Each
      // absence is read once the header draws the assignee.
      await gotoIssue(alice, `/issues/${issue.key}`);
      await expectHeaderDrawn(alice);
      await expect(waitingCount(alice)).toHaveText("Waiting on you (1)");
      await gotoIssue(bob, `/issues/${issue.key}`);
      await expectHeaderDrawn(bob);
      await expect(badgeValue(bob, `Assignee of ${issue.key}`)).toHaveText("alice");
      await expect(waitingCount(bob), "a question waiting on alice counts for bob").toHaveCount(0);

      // Bob's own question waits on alice too: it raises her count and not his.
      const bobsQuestion = await createAsk(
        issue.key,
        { question: "And who decides it?" },
        { as: "user", login: "bob" }
      );
      expect(await waitingOnPersonOf(bobsQuestion.id)).toBe("alice");
      await gotoIssue(bob, `/issues/${issue.key}`);
      await expectHeaderDrawn(bob);
      await expect(badgeValue(bob, `Assignee of ${issue.key}`)).toHaveText("alice");
      await expect(waitingCount(bob), "bob's own question raised bob's count").toHaveCount(0);
      await gotoIssue(alice, `/issues/${issue.key}`);
      await expect(waitingCount(alice)).toHaveText("Waiting on you (2)");

      // The issue handed to bob: both questions wait on him now, and none on alice.
      await patchIssue(issue.key, { assignee: "bob" });
      expect(await waitingOnPersonOf(asked.id)).toBe("bob");
      await gotoIssue(bob, `/issues/${issue.key}`);
      await expect(waitingCount(bob)).toHaveText("Waiting on you (2)");
      await gotoIssue(alice, `/issues/${issue.key}`);
      await expectHeaderDrawn(alice);
      await expect(badgeValue(alice, `Assignee of ${issue.key}`)).toHaveText("bob");
      await expect(
        waitingCount(alice),
        "the count stayed with alice after the issue went to bob"
      ).toHaveCount(0);
    } finally {
      await bobs.close();
    }
  });
});

// c-header (6): the checkpoint's "a press on the status straight out of the title's editor lands
// on the status" (`e2e/issue-layout.e2e.ts:3416`), finding F5 of
// dispatch://INFRA-1196/artifact/tester-findings-md.
test("a press on the status straight out of the title's editor lands on the status", async ({
  browser,
}) => {
  await phase(browser, seedHeader, async (page, seeded) => {
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectHeaderDrawn(page);
    const title = issueHeader(page).getByRole("heading", { level: 1 });
    const editor = issueHeader(page).getByRole("textbox", { name: "Issue title" });
    const status = issueHeader(page).getByRole("combobox", { name: "Status" });

    // The control: with nothing edited, the press out of the editor focuses the status.
    await title.click();
    await expect(editor).toBeFocused();
    await status.click();
    await expect(status, "the status took no focus with nothing edited").toBeFocused();
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");

    // The same press with the title edited, and no Enter: the title saves and the press lands.
    await title.click();
    await expect(editor).toBeFocused();
    await editor.pressSequentially("edited ");
    await status.click();
    await expect(status, "the press out of an edited title landed on nothing").toBeFocused();
    await expect
      .poll(async () => (await getIssue(seeded.key)).title, {
        message: "the title the reader left was not saved",
      })
      .toContain("edited");
    await status.selectOption("todo");
    await expect
      .poll(async () => (await getIssue(seeded.key)).status, {
        message: "the status picked straight after the title was not written",
      })
      .toBe("todo");

    // A status picked while the title's write is still out waits behind it in the issue's line
    // (decision 47): the select draws the pick at once, and it is written once, after the title.
    // The issue is at `todo` already, so the pick is `testing`, which the select reads only if
    // the pick was drawn.
    let statusWrites = 0;
    const countStatusWrite = (request: Request) => {
      if (writesField(request, seeded.key, "status")) statusWrites += 1;
    };
    page.on("request", countStatusWrite);
    const held = `**/api/v1/issues/${seeded.key}`;
    const release = Promise.withResolvers<void>();
    let holding = false;
    await page.route(held, async (route) => {
      if (holding || !writesField(route.request(), seeded.key, "title")) {
        await route.fallback();
        return;
      }
      holding = true;
      await release.promise;
      await route.continue();
    });
    await title.click();
    await expect(editor).toBeFocused();
    await editor.pressSequentially("held ");
    await status.click();
    await expect.poll(() => holding, { message: "the title's write was not sent" }).toBe(true);
    await status.selectOption("testing");
    await expect(status, "the status picked while the title saved was not drawn").toHaveValue(
      "testing"
    );
    release.resolve();
    await expect
      .poll(async () => (await getIssue(seeded.key)).status, {
        message: "the status picked while the title saved was not written once",
      })
      .toBe("testing");
    expect(statusWrites, "the status picked while the title saved was not written once").toBe(1);
    await expect(status, "the status's write did not settle").toBeEnabled();
    // A second write of the pick would be sent as the first one settles, so 500 ms past that is
    // many frames past where a late duplicate would show.
    await page.waitForTimeout(500);
    expect(statusWrites, "the status picked while the title saved was not written once").toBe(1);
    page.off("request", countStatusWrite);
    await page.unroute(held);

    // Escape and Enter leave the title's editor with the focus on the heading it opened from,
    // which does not open the editor again; Enter saves what was typed.
    for (const leave of ["Escape", "Enter"] as const) {
      await page.keyboard.press("Escape");
      await title.click();
      await expect(editor).toBeFocused();
      if (leave === "Enter") await editor.pressSequentially("entered ");
      await page.keyboard.press(leave);
      await expect(editor, `${leave} left the title's editor open`).toHaveCount(0);
      await expect(title, `${leave} dropped the focus out of the header`).toBeFocused();
    }
    await expect
      .poll(async () => (await getIssue(seeded.key)).title, {
        message: "Enter did not save the title",
      })
      .toContain("entered");
  });
});

// c-header (7): `e2e/issue.e2e.ts:1016` at 750e2716, "issue header delivers the click that ends
// a title edit to the control under the pointer @phone": ending the edit of a title of two or
// three lines must not move the control under it between the mousedown and the mouseup. On the
// phone the press is a tap of the touch screen (R2 of "Phone acceptance" in
// `packages/dispatch/AGENTS.md`), whose mouse events come after its touch events: the order
// `IssueTitleRow.tsx` holds the title's height by until the press is delivered, at 390 px.
test("the press that ends a title edit reaches the control under it @phone", async ({
  browser,
}) => {
  test.setTimeout(150_000);
  await phase(browser, seedHeader, async (page, seeded) => {
    const tap = test.info().project.name === "iphone";
    const widths = tap ? [IPHONE_13.width] : [DESKTOP.width, IPHONE_13.width];
    for (const width of widths) {
      await page.setViewportSize(viewportAt(width));
      for (const lines of [2, 3]) {
        await gotoIssue(page, `/issues/${seeded.key}`);
        await expectHeaderDrawn(page);
        const title = await titleOfLines(page, lines);
        await patchIssue(seeded.key, { title });
        await gotoIssue(page, `/issues/${seeded.key}`);
        await expectHeaderDrawn(page);
        const heading = issueHeader(page).getByRole("heading", { level: 1 });
        await expect(heading).toHaveText(title);
        await heading.click();
        const editor = issueHeader(page).getByRole("textbox", { name: "Issue title" });
        await expect(editor).toBeFocused();
        // The editor draws the whole title, as many lines as it takes. Read softly, so a run
        // whose editor is one line still reads the press below.
        const drawnLines = await editor.evaluate((box) =>
          Math.round(
            (box.getBoundingClientRect().height - 2) /
              Number.parseFloat(getComputedStyle(box).lineHeight)
          )
        );
        expect
          .soft(drawnLines, `the editor draws the ${lines}-line title in another number of lines`)
          .toBe(lines);
        // A real press on the count under the title, straight out of the edit.
        await pressOutOfTheEdit(page, waitingCount(page), "the count", tap);
        await expect(
          page,
          `the press out of a ${lines}-line title's edit at ${width} px landed on nothing`
        ).toHaveURL(decisionsAddress(seeded.key));

        // The same press on the status, straight out of an edit of the same title, lands too,
        // and the status takes the focus.
        await gotoIssue(page, `/issues/${seeded.key}`);
        await expectHeaderDrawn(page);
        await heading.click();
        await expect(editor).toBeFocused();
        const status = issueHeader(page).getByRole("combobox", { name: "Status" });
        await pressOutOfTheEdit(page, status, "the status", tap);
        await expect(
          status,
          `the press out of a ${lines}-line title's edit at ${width} px did not reach the status`
        ).toBeFocused();
        await page.keyboard.press("Escape");
      }
    }
  });
});

// c-header (8): the header's steps of `e2e/keyboard-issue.e2e.ts`'s rows at lines 61, 258 and
// 306 at the checkpoint, and of `main`'s `e2e/keyboard-palette.e2e.ts`.
test("the header's keys and the palette reach their controls", async ({ browser }) => {
  test.setTimeout(150_000);
  await phase(browser, seedHeader, async (page, seeded) => {
    const key = seeded.key;
    await page.setViewportSize({ height: 900, width: 1280 });
    await gotoIssue(page, `/issues/${key}`);
    await expectHeaderDrawn(page);
    await leaveFocus(page);
    const header = issueHeader(page);
    const priority = header.getByRole("combobox", { name: `Priority of ${key}` });
    const status = header.getByRole("combobox", { name: "Status" });
    const patched: string[] = [];
    page.on("request", (request) => {
      if (request.method() === "PATCH") patched.push(new URL(request.url()).pathname);
    });

    // p and s reach their own control, and Escape gives the focus back to the page.
    await page.keyboard.press("p");
    await expect(priority, "p did not put the focus on the priority").toBeFocused();
    await page.keyboard.press("Escape");
    await expect.poll(() => onBody(page)).toBe(true);
    await page.keyboard.press("s");
    await expect(status, "s did not put the focus on the status").toBeFocused();
    await page.keyboard.press("Escape");
    await expect.poll(() => onBody(page)).toBe(true);
    expect(patched, "a key that focuses a control wrote the issue").toEqual([]);

    // A refused write: the digit and the picker are one write, so the refusal is said once, in
    // the header, with Retry, and the issue keeps its priority.
    const refused = `**/api/v1/issues/${key}`;
    await page.route(refused, async (route) => {
      if (route.request().method() !== "PATCH") {
        await route.continue();
        return;
      }
      await route.fulfill({
        body: JSON.stringify({ code: "ISSUE_INPUT", error: "Dispatch refused the priority" }),
        contentType: "application/json",
        status: 400,
      });
    });
    await page.keyboard.press("2");
    const failure = header
      .getByRole("alert")
      .filter({ hasText: `Could not update the priority of ${key}.` });
    await expect(failure).toHaveCount(1);
    await expect(failure.getByRole("button", { name: "Retry" })).toBeVisible();
    await priority.selectOption("3");
    await expect(failure).toHaveCount(1);
    await expect(failure.getByRole("button", { name: "Retry" })).toBeVisible();
    await expect.poll(() => getIssue(key)).toMatchObject({ priority: 1 });
    await page.unroute(refused);
    await gotoIssue(page, `/issues/${key}`);
    await expectHeaderDrawn(page);
    await leaveFocus(page);

    // A digit sets the priority through the picker's write, and Shift+P pins through the
    // header's own control; both hold after a new load.
    const write = page.waitForRequest(
      (request) =>
        request.method() === "PATCH" && new URL(request.url()).pathname === `/api/v1/issues/${key}`
    );
    await page.keyboard.press("2");
    expect((await write).postDataJSON()).toEqual({ priority: 2 });
    await expect(badgeValue(page, `Priority of ${key}`)).toHaveText("P2");
    await expect.poll(() => getIssue(key)).toMatchObject({ priority: 2 });
    await leaveFocus(page);
    const pinned = page.waitForResponse(
      (response) =>
        response.request().method() === "PUT" &&
        new URL(response.url()).pathname === `/api/v1/me/issues/${key}/state`
    );
    await page.keyboard.press("Shift+P");
    await expect(header.locator('[data-issue-action="pin"]')).toHaveAttribute(
      "aria-pressed",
      "true"
    );
    expect((await pinned).ok()).toBe(true);
    await gotoIssue(page, `/issues/${key}`);
    await expectHeaderDrawn(page);
    await expect(badgeValue(page, `Priority of ${key}`)).toHaveText("P2");
    await expect(header.locator('[data-issue-action="pin"]')).toHaveAttribute(
      "aria-pressed",
      "true"
    );
    await leaveFocus(page);

    // e puts the caret in the title, where every key is text: "sp0" is typed there, and Escape
    // keeps the title.
    await page.keyboard.press("e");
    const editor = header.getByRole("textbox", { name: "Issue title" });
    await expect(editor, "e did not put the caret in the title").toBeFocused();
    await page.keyboard.type("sp0");
    await expect(editor).toHaveValue(/sp0/);
    await page.keyboard.press("Escape");
    await expect(header.getByRole("heading", { level: 1 })).toHaveText("The header's issue");
    await expect
      .poll(() => getIssue(key))
      .toMatchObject({ priority: 2, title: "The header's issue" });
    await leaveFocus(page);

    // The palette offers Close issue and Set priority P2, and not Reopen issue.
    await page.keyboard.press("Control+k");
    const actions = page.getByRole("dialog", { name: "Search" }).getByRole("group", {
      name: "Actions",
    });
    await expect(actions.getByRole("option", { name: "Close issue" })).toBeVisible();
    await expect(actions.getByRole("option", { name: "Set priority P2" })).toBeVisible();
    await expect(actions.getByRole("option", { name: "Reopen issue" })).toHaveCount(0);
    await page.keyboard.press("Escape");
    const palette = page.getByRole("dialog", { name: "Search" });
    await expect(palette, "Escape did not close the palette").toHaveCount(0);

    // A status picked while its write is held in flight leaves the priority taking input, and
    // the palette offers Set priority with it, as the issue's line sends that write after the
    // status's. Escape closes the palette before the status's write is let go.
    const held = `**/api/v1/issues/${key}`;
    const release = Promise.withResolvers<void>();
    let holding = 0;
    await page.route(held, async (route) => {
      if (route.request().method() === "PATCH") {
        holding += 1;
        await release.promise;
      }
      await route.continue();
    });
    await status.selectOption("todo");
    await expect.poll(() => holding, { message: "the status's write was not sent" }).toBe(1);
    await expect(priority, "the priority refuses input while the status is saved").toBeEnabled();
    await leaveFocus(page);
    await page.keyboard.press("Control+k");
    await expect(
      actions.getByRole("option", { name: "Set priority P2" }),
      "the palette hides Set priority while the priority takes input"
    ).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(palette, "Escape did not close the palette").toHaveCount(0);
    release.resolve();
    await expect.poll(() => getIssue(key).then((read) => read.status)).toBe("todo");
    await expect(status, "the status's write did not settle").toBeEnabled();
    await page.unroute(held);

    // The palette's Close issue closes the issue, which then offers Reopen issue and neither
    // Close issue nor a priority, as the header offers Reopen and refuses a priority; Reopen
    // gives the issue back to the backlog. The rows are taken when the palette opens, so it
    // opens once the page has taken in the close and has no write in flight.
    await leaveFocus(page);
    await page.keyboard.press("Control+k");
    await actions.getByRole("option", { name: "Close issue" }).click();
    await expect(palette, "Close issue did not close the palette").toHaveCount(0);
    await expect.poll(() => getIssue(key).then((read) => read.status)).toBe("done");
    await expect(priority, "the closed issue's priority takes input").toBeDisabled();
    await expect(header.getByRole("status"), "the close is still being saved").toHaveCount(0);
    await leaveFocus(page);
    await page.keyboard.press("Control+k");
    const reopen = actions.getByRole("option", { name: "Reopen issue" });
    await expect(reopen, "the closed issue's palette offers no Reopen issue").toBeVisible();
    await expect(actions.getByRole("option", { name: "Close issue" })).toHaveCount(0);
    await expect(actions.getByRole("option", { name: "Set priority P2" })).toHaveCount(0);
    await reopen.click();
    await expect(palette, "Reopen issue did not close the palette").toHaveCount(0);
    await expect.poll(() => getIssue(key).then((read) => read.status)).toBe("backlog");
  });
});

// c-header (9): the header's half of the checkpoint's "the identity and the title stay when the
// page scrolls, and on a phone the filter control @phone" (`e2e/issue-layout.e2e.ts:3472`). On
// the phone it reads what the 390 px viewport decides (R1 of "Phone acceptance" in
// `packages/dispatch/AGENTS.md`): the title's 20 px type and its 362 px words with no clamp, and
// the whole header scrolling off the screen with the page, where the band stays from 761 px up.
test("the band stays when the page scrolls, and on a phone the header goes @phone", async ({
  browser,
}) => {
  test.setTimeout(150_000);
  await phase(browser, seedHeader, async (page, seeded) => {
    const widths =
      test.info().project.name === "iphone"
        ? [IPHONE_13.width]
        : [DESKTOP.width, 761, 760, IPHONE_13.width];
    for (const width of widths) {
      const wide = width > 760;
      // A short window, and a page made taller than it, so the page scrolls.
      await page.setViewportSize({ height: 420, width });
      await gotoIssue(page, `/issues/${seeded.key}`);
      await expectHeaderDrawn(page);
      await page.evaluate(() => {
        const page = document.querySelector("[data-issue-page]");
        if (page instanceof HTMLElement) page.style.minHeight = "2000px";
        window.scrollTo(0, 0);
      });

      // With the page at its top the band has no shadow, and the title is in its own type.
      const top = await stickyReading(page);
      if (top === null) throw new Error(`the page draws no header at ${width} px`);
      expect(top.condensed, `the band is stuck with the page at its top at ${width} px`).toBe(
        false
      );
      expect(top.shadow, `the band draws a shadow at the top at ${width} px`).toBe("none");
      const type = [top.title.weight, top.title.size, top.title.line, top.title.spacing];
      expect(type, `the title's type at ${width} px with the page at its top`).toEqual(
        wide
          ? ["600", "22px", "29px", `${(22 * -0.012).toFixed(3)}px`]
          : ["600", "20px", "26px", `${(20 * -0.012).toFixed(2)}px`]
      );
      if (width === DESKTOP.width) expect(top.title.clamp).toBe("2");
      if (width === IPHONE_13.width) {
        expect(top.title.clamp).toBe("none");
        expect(
          Math.abs(top.title.wordsWidth - 362),
          "the title's words are not 362 px wide"
        ).toBeLessThanOrEqual(1);
      }
      expect(
        top.properties.top,
        "the properties line is not under the title"
      ).toBeGreaterThanOrEqual(top.block.bottom - 1);

      await page.evaluate(() => window.scrollTo(0, 320));
      await expect
        .poll(async () => (await stickyReading(page))?.scrolled ?? 0, {
          message: `the page did not scroll at ${width} px`,
        })
        .toBeGreaterThan(300);
      await expect
        .poll(async () => (await stickyReading(page))?.condensed, {
          message: `the band's stuck state is wrong once scrolled at ${width} px`,
        })
        .toBe(wide);
      const scrolled = await stickyReading(page);
      if (scrolled === null) throw new Error("the header left the page as it scrolled");
      expect(
        [scrolled.title.weight, scrolled.title.size, scrolled.title.line, scrolled.title.spacing],
        `the title's type changed as the page scrolled at ${width} px`
      ).toEqual(type);
      if (wide) {
        expect(Math.abs(scrolled.block.top), "the band left the top").toBeLessThanOrEqual(1);
        expect(scrolled.block.height, "the band that stays is not 63 px").toBeGreaterThanOrEqual(
          62
        );
        expect(scrolled.block.height, "the band that stays is not 63 px").toBeLessThanOrEqual(64);
        expect(scrolled.shadow, "the stuck band draws no shadow").not.toBe("none");
        expect(
          scrolled.properties.bottom,
          "the properties line stayed on screen with the band"
        ).toBeLessThanOrEqual(scrolled.block.bottom);
        expect(scrolled.published, "the page publishes another height than the band's").toBe(
          Math.round(scrolled.block.height)
        );
        await expect(identityBand(page)).toBeInViewport();
      } else {
        expect(scrolled.block.bottom, "the header stayed on a phone").toBeLessThanOrEqual(0);
        expect(scrolled.properties.bottom).toBeLessThanOrEqual(0);
      }
    }
  });
});

/** The words `control` draws, read as a reader reads them off the screen: each glyph it draws,
 *  left to right, with a space wherever the glyphs stand more than 2 px apart and none wherever
 *  they touch. Its text content says nothing of that: a space the layout collapses is still in
 *  it, and a gap the layout opens is not. The control is one line. */
function drawnWords(control: Locator): Promise<string> {
  return control.evaluate((element) => {
    const glyphs: { char: string; left: number; right: number }[] = [];
    const range = document.createRange();
    const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
    for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
      const text = node.textContent ?? "";
      for (let at = 0; at < text.length; at += 1) {
        range.setStart(node, at);
        range.setEnd(node, at + 1);
        const box = range.getBoundingClientRect();
        if (box.width > 0) glyphs.push({ char: text[at] ?? "", left: box.left, right: box.right });
      }
    }
    glyphs.sort((a, b) => a.left - b.left);
    let words = "";
    let right = Number.NaN;
    for (const glyph of glyphs) {
      if (words !== "" && glyph.left - right > 2 && !words.endsWith(" ") && glyph.char !== " ") {
        words += " ";
      }
      words += glyph.char;
      right = glyph.right;
    }
    return words.replace(/\s+/g, " ").trim();
  });
}

// The tester's finding on the first round's head (dispatch://INFRA-3943, c-use): R2's count reads
// "+1 label" or "+N labels" and R7's reads "Waiting on you (N)", and a reader reads the words the
// screen draws, not the text the element holds. At 6164ec0f the labels' count drew "+1label"
// (its space is a flex container's collapsed item) and the count of what waits drew
// "Waiting on you ( 2 )" (its flex gap opens between the brackets and the number), while every
// row's `toHaveText` read the right words in the text content. On the phone it reads the same at
// 390 px.
test("the header draws the words of its two counts as a reader reads them @phone", async ({
  browser,
}) => {
  test.setTimeout(120_000);
  await phase(browser, seedHeader, async (page, seeded) => {
    await patchIssue(seeded.key, { labels: [...TWENTY_LABELS], title: MEASURED_TITLE });
    const widths =
      test.info().project.name !== "iphone" ? [DESKTOP.width, IPHONE_13.width] : [IPHONE_13.width];
    for (const width of widths) {
      await page.setViewportSize(viewportAt(width));
      await gotoIssue(page, `/issues/${seeded.key}`);
      await expectHeaderDrawn(page);
      await expect(waitingCount(page)).toHaveText(/^Waiting on you \(\d+\)$/);
      const count = ((await waitingCount(page).textContent()) ?? "").replace(/\s+/g, " ").trim();
      expect
        .soft(
          await drawnWords(waitingCount(page)),
          `the count of what waits draws other words at ${width} px`
        )
        .toBe(count);
      await expect(labelsLeft(page)).toHaveText(/^\+\d+ labels?$/);
      const left = ((await labelsLeft(page).textContent()) ?? "").replace(/\s+/g, " ").trim();
      expect
        .soft(
          await drawnWords(labelsLeft(page)),
          `the labels' count draws other words at ${width} px`
        )
        .toBe(left);
    }
  });
});

/** A reader whose login is as long as INFRA-1196's assignee's: 16 characters. */
const LONG_LOGIN = "dana@example.dev";

// The tester's finding on the first round's head (dispatch://INFRA-3943, c-use): R3 holds the
// header to its limits with 20 labels and the measured title, and the measured title is
// INFRA-1196's own. On a preview of 6164ec0f holding INFRA-1196 as copied from production -
// a root of a project that gates its spec, 24 children with 3 done, assigned to a 16-character
// login - with 20 labels and the spec's approval request waiting on the reader, the header took
// 4 lines and 127 px at 1440 px and 11 lines and 327 px at 390 px. Row (1)'s seed is a child
// with one chip and 3 children, so no row read this issue's shape. This row seeds that shape and
// reads R3's limits with no request waiting and with one; on the phone it reads 390 px.
test("the header holds its limits on a gated root shaped like INFRA-1196 @phone", async ({
  browser,
}) => {
  test.setTimeout(240_000);
  await phase(
    browser,
    async () => {
      await createProject({ key: "LIKE", name: "Like INFRA" });
      await setGatedSteps("LIKE", ["spec"]);
      const root = await createIssue({
        force: true,
        project: "LIKE",
        spec: "The plan.",
        title: MEASURED_TITLE,
      });
      for (let at = 0; at < 24; at += 1) {
        const child = await createIssue({
          force: true,
          parent: root.key,
          project: "LIKE",
          title: `Part ${at + 1} of the root`,
        });
        if (at < 3) await patchIssue(child.key, { status: "done" });
      }
      return root;
    },
    async (_alice, root) => {
      const context = await asUser(browser, LONG_LOGIN);
      try {
        await withIssuePage(context, LONG_LOGIN);
        const page = await context.newPage();
        await patchIssue(root.key, {
          assignee: LONG_LOGIN,
          labels: [...TWENTY_LABELS],
          priority: 1,
          status: "in_progress",
        });
        const widths =
          test.info().project.name !== "iphone"
            ? [DESKTOP.width, IPHONE_13.width]
            : [IPHONE_13.width];
        for (const waiting of [false, true]) {
          if (waiting) {
            await requestApproval(root.primary_artifact_id ?? "", { summary: "Read it." });
          }
          for (const width of widths) {
            await page.setViewportSize(viewportAt(width));
            await gotoIssue(page, `/issues/${root.key}`);
            await expectHeaderDrawn(page);
            await expect(issueHeader(page).getByTestId("issue-approval-steps")).toBeVisible();
            await expect(issueHeader(page).getByTestId("issue-progress-subtree")).toBeVisible();
            if (waiting) await expect(waitingCount(page)).toHaveText("Waiting on you (1)");
            await expectHeaderWithinItsLimit(page, width, waiting, TYPEFACE);
          }
        }
      } finally {
        await context.close();
      }
    }
  );
});
