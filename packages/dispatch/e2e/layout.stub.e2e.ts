/** The Conversation's geometry at three widths, and the Details control a comment deep link
 *  leaves reachable. Both rows read what the page draws at a width - which controls are shown, how
 *  wide the Conversation is, which turn is current, and whether the control is hit-testable - and
 *  none of it is a claim about the server, so both run on the stubbed layer
 *  (packages/dispatch/AGENTS.md, "The stubbed layer"). On the issue page the navigation is an
 *  icon rail and the panel holding the Conversation is the Details drawer (dispatch://INFRA-1196
 *  R14). */
import { expect } from "@playwright/test";

import { createComment, createIssue, createProject } from "./api";
import {
  conversationStateControl,
  detailsControl,
  iconRail,
  marginShell,
  openDetails,
} from "./issue-page";
import { stubbed } from "./stub/stub-test";

const test = stubbed(async () => {
  await createProject({ key: "CORE", name: "Core" });
  const issue = await createIssue({
    project: "CORE",
    spec: "Review this tablet issue.",
    title: "Review at tablet width",
  });
  const comment = await createComment(issue.key, { body: "Open this review item." });
  return { comment: comment.id, issue: issue.key };
});

test("tablet keeps the Conversation readable and exposes the review sheet @pr", async ({
  page,
  scene,
}, testInfo) => {
  await page.setViewportSize({ height: 1024, width: 800 });
  await page.goto(`/issues/${scene.issue}/conversation`);

  await expect(page.getByRole("button", { name: "Open navigation" })).toBeVisible();
  await expect(detailsControl(page)).toBeVisible();
  // The conversation's own address opens Details, and the Conversation fills its width.
  await openDetails(page);
  const conversationBounds = await page.locator("#issue-conversation-panel").boundingBox();
  const drawerBounds = await marginShell(page).boundingBox();
  expect(conversationBounds?.width ?? 0).toBeGreaterThanOrEqual((drawerBounds?.width ?? 0) - 40);
  const tabletScreenshot = testInfo.outputPath("tablet-800.png");
  await page.screenshot({ path: tabletScreenshot, fullPage: true });
  await testInfo.attach("tablet layout (800px)", {
    contentType: "image/png",
    path: tabletScreenshot,
  });

  await page.setViewportSize({ height: 768, width: 1024 });
  await page.goto(`/issues/${scene.issue}/comments/${scene.comment}`);
  await openDetails(page);
  await expect(
    page
      .getByRole("list", { name: "Conversation turns" })
      .locator(`li[data-turn="comment:${scene.comment}"][aria-current="true"]`)
  ).toBeVisible();

  await page.setViewportSize({ height: 1024, width: 1280 });
  await expect(iconRail(page)).toBeVisible();
  await expect(page.getByRole("complementary", { name: "Details" })).toBeVisible();
  const desktopScreenshot = testInfo.outputPath("desktop-1280.png");
  await page.screenshot({ path: desktopScreenshot, fullPage: true });
  await testInfo.attach("desktop layout (1280px)", {
    contentType: "image/png",
    path: desktopScreenshot,
  });
});

// An unanchored comment deep link focuses its node in the outline and its Conversation turn,
// rather than opening a margin comment. The Details control must remain visibly openable and
// pointer-reachable on that route.
test("a comment deep link keeps the compact review-panel toggle hit-testable @pr", async ({
  page,
  scene,
}) => {
  await page.setViewportSize({ height: 1024, width: 800 });
  await page.goto(`/issues/${scene.issue}/conversation`);
  await expect(detailsControl(page)).toBeVisible();

  await page.setViewportSize({ height: 768, width: 1024 });
  await page.goto(`/issues/${scene.issue}/comments/${scene.comment}`);
  await expect(conversationStateControl(page, "words")).toHaveAttribute("aria-selected", "true");
  const toggle = detailsControl(page);
  await expect(toggle).toBeVisible();
  const hitTest = () =>
    toggle.evaluate((button) => {
      const rect = button.getBoundingClientRect();
      const elementAtCenter = document.elementFromPoint(
        rect.x + rect.width / 2,
        rect.y + rect.height / 2
      );
      return (
        elementAtCenter !== null && (elementAtCenter === button || button.contains(elementAtCenter))
      );
    });
  await expect.poll(hitTest).toBe(true);
});
