/** The issue landing at both widths: Spec primary with the open asks in Needs you at a desktop
 *  width, and the review panel toggle the phone layout draws instead. Each reads only what the
 *  page draws, so each runs against a recording rather than against a harness
 *  (packages/dispatch/AGENTS.md, "The stubbed layer"). The phone row runs under `stub-phone`,
 *  which is the one project that takes the ` @phone-only` tag it carries. */
import { expect } from "@playwright/test";

import { createAsk, createIssue, createProject } from "./api";
import { needsYouCards } from "./editor";
import { detailsControl, expectActivePreset, openDetails } from "./issue-page";
import { stubbed } from "./stub/stub-test";

const session = {
  actor: { id: "e2e-spec-primary", kind: "session" as const },
  as: "agent" as const,
};

const test = stubbed(async () => {
  await createProject({ key: "SPEC", name: "Spec primary" });
  const issue = await createIssue({
    project: "SPEC",
    spec: "Primary document content.",
    title: "Keep the document central",
  });
  await createAsk(issue.key, { question: "Approve the document-first layout?" }, session);
  await createAsk(
    issue.key,
    {
      anchor: { artifact: "spec", quote: "Primary" },
      question: "Is this opening clear?",
    },
    session
  );
  return { issue: issue.key };
});

test("issue landing keeps Spec primary and puts open asks in Needs you @pr", async ({
  page,
  scene,
}, testInfo) => {
  await page.setViewportSize({ height: 900, width: 1440 });
  await page.goto(`/issues/${scene.issue}`);

  // The issue's own address is the Tree preset's now (dispatch://INFRA-1196 R19); at a phone
  // width the row of five is one control naming it.
  await expectActivePreset(page, "Tree");
  // Needs you is the margin's own cards, in Details, which the header's control opens (R14).
  await openDetails(page);

  await expect(
    needsYouCards(page).filter({ hasText: "Approve the document-first layout?" })
  ).toBeVisible();
  await expect(needsYouCards(page).filter({ hasText: "Is this opening clear?" })).toBeVisible();
  const desktopScreenshot = testInfo.outputPath("spec-primary-desktop-1440.png");
  await page.screenshot({ path: desktopScreenshot, fullPage: true });
  await testInfo.attach("spec primary desktop 1440", {
    contentType: "image/png",
    path: desktopScreenshot,
  });
});

// Below 768 px the panel is not beside the document at all: it is Details, a sheet over the whole
// screen behind the header's control (dispatch://INFRA-1196 R14), and opening it has to bring the
// open asks up without pushing the page sideways. The screen is the project's own, so the claim
// is read at the width a phone actually gives it.
test("on a phone the issue's open asks come up behind the review panel toggle @phone @phone-only @pr", async ({
  page,
  scene,
}, testInfo) => {
  await page.goto(`/issues/${scene.issue}`);
  const toggle = detailsControl(page);
  await expect(toggle).toBeVisible();
  const toggleScreenshot = testInfo.outputPath("spec-primary-phone-toggle.png");
  await page.screenshot({ path: toggleScreenshot, fullPage: true });
  await testInfo.attach("spec primary phone toggle", {
    contentType: "image/png",
    path: toggleScreenshot,
  });
  await openDetails(page);
  await expect(
    needsYouCards(page).filter({ hasText: "Approve the document-first layout?" })
  ).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true
  );
  const needsYouScreenshot = testInfo.outputPath("spec-primary-phone-needs-you.png");
  await page.screenshot({ path: needsYouScreenshot, fullPage: true });
  await testInfo.attach("spec primary phone Needs you", {
    contentType: "image/png",
    path: needsYouScreenshot,
  });
});
