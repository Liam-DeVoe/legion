/** the row writes a version of a grandchild's Verification document through the server and reads
 *  its own write back on the root's open page with no reload: the counts it reads are the
 *  server's coverage over every issue under the root, and what reaches the page is the event the
 *  server sends over the live socket, which names the grandchild's ancestors */
import { VERIFICATION_WORDS } from "../web/src/features/issue-page/page-words";
import { blockNodeId } from "../web/src/features/issue-page/tree-drafts";
import {
  createIssue,
  createIssueArtifact,
  createProject,
  documentBase,
  getArtifact,
  getIssueCoverage,
} from "./api";
import { specBlockRef } from "./issue-layout-seed-text";
import { phase } from "./issue-page";
import { expectOpeningLanded } from "./issue-page-g";
import { openPreset } from "./issue-page-i";
import { verificationCard } from "./issue-page-v";
import { expect, test } from "./test";

/**
 * Part S of dispatch://INFRA-1196 (dispatch://INFRA-5411): what a document event of an issue
 * reaches on the new page of an issue above it, read on the real stack. An issue's document
 * events (`artifact.created`, `artifact.version`, `artifact.removed`) name the issue's ancestors,
 * and an ancestor's open page marks that ancestor's tree read, each view of it, and on an upload
 * or a version its coverage read, from them (`web/src/api/event-refresh.ts`). Each row turns the
 * new page on for alice with `phase` (`e2e/issue-page.ts`, through `withIssuePage`), seeds what
 * it reads through `e2e/api.ts`, opens the page with `openPreset` (`e2e/issue-page-i.ts`), and
 * reads the Verification view through `verificationCard` (`e2e/issue-page-v.ts`).
 *
 * The rows are the browser checks of the Verification document, by their titles:
 *
 * 1. `c-ancestor-coverage`: "a grandchild document version moves the root coverage".
 */

/** The project every row files its issues in. */
const PROJECT = "ANC";

/** The block id of the root's one requirement, which the grandchild's first check serves. */
const REQUIREMENT = "r-drawn";

/** What `seedThreeGenerations` wrote. */
interface ThreeGenerations {
  readonly root: string;
  readonly grandchild: string;
  /** The grandchild's Verification document, which holds no check yet. */
  readonly grandchildVerification: string;
  /** The node id the root's Verification view draws the requirement's card under. */
  readonly requirement: string;
}

/** The checks the root's coverage read counts for its requirement, its own and those of every
 *  issue under it; undefined where the read does not hold the requirement yet. */
async function rootChecks(root: string): Promise<number | undefined> {
  const coverage = await getIssueCoverage(root);
  return (coverage.requirements ?? []).find((row) => row.block_id === REQUIREMENT)?.checks;
}

/**
 * A root with one requirement, a child under it, and a grandchild under the child whose
 * Verification document holds no check yet, so nothing serves the requirement. The seed waits
 * until the root's coverage read holds the requirement, served by no check, so the row reads the
 * view and not the indexing.
 */
async function seedThreeGenerations(): Promise<ThreeGenerations> {
  await createProject({ key: PROJECT, name: "Ancestors" });
  const root = await createIssue({
    force: true,
    project: PROJECT,
    spec: `:::requirement{#${REQUIREMENT}}\nEach issue under the root is drawn on its page.\n:::\n`,
    title: "The root whose requirement a grandchild serves",
  });
  const child = await createIssue({
    force: true,
    parent: root.key,
    project: PROJECT,
    title: "The child between the root and the grandchild",
  });
  const grandchild = await createIssue({
    force: true,
    parent: child.key,
    project: PROJECT,
    title: "The grandchild whose check serves the root",
  });
  const verification = await createIssueArtifact(grandchild.key, {
    content: "How the grandchild is proved.\n",
    name: "verification.md",
  });
  await expect
    .poll(() => rootChecks(root.key), {
      message: `${root.key}'s coverage read does not hold its requirement, served by no check`,
      timeout: 20_000,
    })
    .toBe(0);
  const specSlug = (await getArtifact(root.primary_artifact_id ?? "")).slug;
  return {
    grandchild: grandchild.key,
    grandchildVerification: verification.artifact.id,
    requirement: blockNodeId(specSlug, REQUIREMENT),
    root: root.key,
  };
}

// Row 1, `c-ancestor-coverage`. The root's new page is open on Verification, and nothing serves
// its requirement: the summary counts it uncovered, its card carries the gap of a statement no
// check serves, and no line counts checks elsewhere. A session writes the grandchild's
// Verification document again with its first check, which serves the root's requirement. The
// version's event names the grandchild's ancestors, so the root's page marks the root's coverage
// read, which counts every issue under the root, and with no reload the card counts one check
// elsewhere, carries no gap, and the summary counts the requirement covered (decision 402's page
// case). A server whose document events name no ancestors leaves the root's page unable to place
// the grandchild, and the counts stay as they were until a reload.
// SLOW(>=2s): three issues seeded and waited for in the coverage read, the root's page loaded,
// then a version written under it and the page's coverage read waited for, which the scheduler
// starts at most once a second (MIN_REFETCH_INTERVAL_MS, web/src/api/query-refresh.ts).
test("a grandchild document version moves the root coverage", async ({ browser }) => {
  test.setTimeout(120_000);
  await phase(browser, seedThreeGenerations, async (page, seeded) => {
    await openPreset(page, seeded.root, "verification");
    await expectOpeningLanded(page);
    const card = verificationCard(page, seeded.requirement);
    await expect(
      card,
      "the Verification view draws no card for the root's requirement"
    ).toHaveCount(1);
    const covered = page.locator("[data-verification-covered]");
    const elsewhere = card.locator("[data-verification-elsewhere]");
    const coveredOf = (count: number) =>
      `${count} ${VERIFICATION_WORDS.of} 1 ${VERIFICATION_WORDS.covered}`;

    // Before the version: no check serves the requirement, the root's own or one under it.
    await expect(covered, "the summary does not count the requirement uncovered").toHaveText(
      coveredOf(0)
    );
    await expect(
      card.locator('[data-gap-kind="no_check"]'),
      "the requirement's card does not say no check serves it"
    ).toHaveCount(1);
    await expect(elsewhere, "the requirement's card counts checks elsewhere").toHaveCount(0);

    // A session writes the grandchild's Verification document again, its first check serving
    // the root's requirement, and the server counts it in the root's coverage read.
    await createIssueArtifact(
      seeded.grandchild,
      {
        content:
          "How the grandchild is proved.\n\n" +
          `:::check{#c-grandchild serves="${specBlockRef(seeded.root, REQUIREMENT)}"` +
          ' surface="browser" run="bun run e2e -- e2e/issue-page-s.e2e.ts" when="before_merge"}\n' +
          "The grandchild's first check serves the root's requirement.\n:::\n",
        name: "verification.md",
        precondition: await documentBase(seeded.grandchildVerification),
      },
      { as: "agent" }
    );
    await expect
      .poll(() => rootChecks(seeded.root), {
        message: `${seeded.root}'s coverage read does not count the grandchild's check`,
        timeout: 20_000,
      })
      .toBe(1);

    // With no reload, the root's page draws the counts its coverage read now answers.
    await expect(
      elsewhere,
      "the requirement's card does not count the grandchild's check elsewhere with no reload"
    ).toHaveText(`1 ${VERIFICATION_WORDS.elsewhere_one}`);
    await expect(
      covered,
      "the summary does not count the requirement covered with no reload"
    ).toHaveText(coveredOf(1));
    await expect(
      card.locator("[data-gap]"),
      "the requirement's card still carries a gap with no reload"
    ).toHaveCount(0);
  });
});
