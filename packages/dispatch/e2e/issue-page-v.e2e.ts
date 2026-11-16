import {
  CHECK_WHEN_WORDS,
  type CheckState,
  EMPTY_STATE_LINES,
  GAP_LINES,
  GLYPH_TOKENS,
  NODE_STATE_WORDS,
  PRESET_NAMES,
  VERIFICATION_WORDS,
} from "../web/src/features/issue-page/page-words";
import { blockNodeId } from "../web/src/features/issue-page/tree-drafts";
import {
  createIssue,
  createProject,
  getArtifact,
  getIssue,
  getIssueCoverage,
  getIssueResults,
  recordCheckResult,
  retractCheckResult,
} from "./api";
import { seedWholeIssue } from "./issue-layout-seed";
import { HEAD_SHA, PROJECT, recorderActor } from "./issue-layout-seed-support";
import { requirementBlockId } from "./issue-layout-seed-text";
import { issuePage, phase } from "./issue-page";
import { DESKTOP, expectIssuePageDrawn, nodeRows, PHONE, tree } from "./issue-page-g";
import { activePreset, openPreset } from "./issue-page-i";
import {
  boxesPastTheirCard,
  copyCommandControls,
  namedCommit,
  nodeCommands,
  overlappingTexts,
  verificationCardBoxes,
  verificationCards,
} from "./issue-page-v";
import { expect, test } from "./test";
import { expectLoadSettled, watchApiRequests } from "./traffic";

/**
 * Part V's rows of the new issue page (dispatch://INFRA-3936, part V of dispatch://INFRA-1196):
 * the Verification view, read on the real stack. Each row turns the new page on for its reader
 * with D's `phase`, seeds what it reads, and reads the view's cards through this part's
 * `e2e/issue-page-v.ts`, whose measures fail where the view draws no card.
 *
 * The two rows come from the checkpoint's `e2e/issue-layout.e2e.ts`, each under the title its
 * check names (`verification.md` of dispatch://INFRA-3936, c-view rows 1 and 2).
 */

/** The slug of the issue's own spec, which is how its blocks are addressed. */
async function primarySlug(key: string): Promise<string> {
  const primary = ((await getIssue(key)).artifacts ?? []).find((artifact) => artifact.primary);
  expect(primary, `${key} carries no primary document`).toBeDefined();
  return primary?.slug ?? "";
}

// c-view, row 1. The body the Verification view draws is `verification-filter/`: each statement
// with the checks that serve it, each check beside its newest result, and its earlier results
// folded to one line (ruling 6 of build-rulings-md, the tester's F15). Two of the seven states
// the seed cannot make, so this row makes them: `retracted` by withdrawing every result of one
// check, and `unreadable` by taking `pass_stands` out of the results answer in a route handler
// of its own, which is the one way a page meets a server that does not send it. The seeded
// Verification document holds no picture, so the row records a result carrying the issue's own
// uploaded shot.
test("the Verification filter is readable @phone", async ({ browser }) => {
  // Four loads of the view - three widths and the rewritten answer - each read clause by clause.
  test.setTimeout(240_000);
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    const specSlug = await primarySlug(seeded.key);
    const verificationSlug = (await getArtifact(seeded.verificationArtifactId)).slug;

    await recordCheckResult(
      seeded.key,
      {
        check: seeded.checks.passed,
        commit: HEAD_SHA,
        failed: 0,
        negative_control: "Removed the row's state and the run failed.",
        observed: "1 passed, with a shot of the page",
        role: "tester",
        run: "bun run e2e -- e2e/issue-page-v.e2e.ts",
        screenshots: [`dispatch://${seeded.key}/artifact/${seeded.documents.image}`],
        surface: "browser",
        verdict: "pass",
      },
      { actor: recorderActor, as: "agent" }
    );
    const recorded = await getIssueResults(seeded.key);
    const withdrawing = (recorded.checks ?? []).find(
      (check) => check.block_id === seeded.checks.uncontrolled
    );
    expect(withdrawing?.results ?? [], "the seed recorded no result to withdraw").not.toEqual([]);
    for (const result of withdrawing?.results ?? []) {
      await retractCheckResult(result.id, { reason: "The run was wrong." });
    }

    // What the issue's own reads say, which the page is read against.
    const statements = (await getIssueCoverage(seeded.key)).requirements ?? [];
    const covered = statements.filter((statement) => statement.checks > 0).length;
    const nodeOf = (index: number) => blockNodeId(specSlug, requirementBlockId(index));
    const twice = blockNodeId(verificationSlug, seeded.checks.passed);

    for (const width of [1440, 1024, 390]) {
      await page.setViewportSize({ height: width < 1024 ? PHONE.height : DESKTOP.height, width });
      await openPreset(page, seeded.key, "verification");
      expect(await activePreset(page), `the view is not named Verification at ${width} px`).toBe(
        PRESET_NAMES.verification
      );
      await expectIssuePageDrawn(page);
      await expect
        .poll(() => verificationCardBoxes(page).count(), {
          message: `the Verification filter draws no statement at ${width} px`,
        })
        .toBeGreaterThan(0);
      const cards = await verificationCards(page);

      // Each statement of the issue is drawn, with the checks that serve it under it.
      expect(
        cards.map((card) => card.id).sort(),
        `the Verification view draws other statements than the issue's at ${width} px`
      ).toEqual(statements.map((statement) => blockNodeId(specSlug, statement.block_id)).sort());
      expect(
        new Set(cards.flatMap((card) => card.checks.map((check) => check.id))).size,
        `the Verification view draws no six checks at ${width} px`
      ).toBe(6);

      // No value renders outside its card at any width, and no two values overlap: the sixteen
      // faults of the old tab this requirement is here for.
      expect(
        await boxesPastTheirCard(page),
        `an element reaches past its card at ${width} px`
      ).toEqual([]);
      expect(
        await overlappingTexts(page),
        `two pieces of text are drawn over each other at ${width} px`
      ).toEqual([]);

      // The page names one commit for the issue's results, the newest standing result's, drawn
      // once, in the ten characters the prototype draws a commit in, and on no result's own
      // line.
      expect(
        await namedCommit(page),
        `the page names no one commit for the issue's results at ${width} px`
      ).toBe(HEAD_SHA.slice(0, 10));
      expect(
        (await page.locator("[data-verification-result]").allInnerTexts()).filter((drawn) =>
          drawn.includes(HEAD_SHA.slice(0, 10))
        ),
        `a result draws a commit of its own at ${width} px`
      ).toEqual([]);

      // How many statements carry a check is drawn as covered, and no count, legend or heading
      // reads proven or met for it.
      expect(
        (await page.locator("[data-verification-covered]").innerText()).replace(/\s+/g, " "),
        `the count of the statements that carry a check does not read covered at ${width} px`
      ).toBe(
        `${covered} ${VERIFICATION_WORDS.of} ${statements.length} ${VERIFICATION_WORDS.covered}`
      );
      for (const part of [
        "[data-verification-covered]",
        "[data-verification-legend]",
        "[data-verification-head]",
      ]) {
        const read = (await page.locator(part).allInnerTexts()).join(" ").toLowerCase();
        expect(read, `${part} reads proven at ${width} px`).not.toContain("proven");
      }
      expect(
        (await page.locator("[data-verification-covered]").innerText()).toLowerCase(),
        `the coverage count reads met at ${width} px`
      ).not.toContain("met");

      // A statement reads Met where every check of it passed at that commit, Not met where one
      // failed there, and Unknown otherwise - here no result, an out-of-date one, a commit
      // Dispatch cannot place, and a withdrawn one.
      const stateOf = new Map(cards.map((card) => [card.id, card.state]));
      expect(
        [1, 2, 3, 4, 5, 6].map((index) => stateOf.get(nodeOf(index))),
        `a statement does not read by how its checks stand at ${width} px`
      ).toEqual(["met", "not_met", "unknown", "unknown", "unknown", "unknown"]);
      expect(
        cards.find((card) => card.id === nodeOf(1))?.stateWord,
        `the statement every check of which passed does not read Met at ${width} px`
      ).toBe(VERIFICATION_WORDS.states.met);
      expect(
        cards.find((card) => card.id === nodeOf(2))?.stateWord,
        `the statement one check of which failed does not read Not met at ${width} px`
      ).toBe(VERIFICATION_WORDS.states.not_met);

      // A statement no check serves carries its gap.
      expect(
        cards.find((card) => card.id === nodeOf(7))?.gapKinds,
        `a statement no check serves carries no gap at ${width} px`
      ).toEqual(["no_check"]);
      expect(
        (await page.locator('[data-gap-kind="no_check"]').first().innerText()).trim(),
        `the gap is not said in the page's own words at ${width} px`
      ).toBe(GAP_LINES.no_check);

      // Each of the seven states a check can be in is told apart by a word and a shape. Six of
      // them stand on the seeded checks; the seventh is below, on the rewritten answer.
      const drawnChecks = cards.flatMap((card) => card.checks);
      const states = new Map(
        drawnChecks.map((check) => [check.state ?? "", `${check.word} ${check.shape}`])
      );
      expect(
        [...states.keys()].sort(),
        `the Verification view draws not every seeded state at ${width} px`
      ).toEqual(["failed", "no-result", "older", "passed", "retracted", "unmarked"]);
      expect(
        [...states].map(([state, drawn]) => [state, drawn]),
        `a state is drawn in other words or another shape than the page declares at ${width} px`
      ).toEqual(
        [...states.keys()].map((state) => [
          state,
          `${NODE_STATE_WORDS.check[state as CheckState]} ${GLYPH_TOKENS.states.check[state as CheckState]}`,
        ])
      );
      expect(
        new Set([...states.values()]).size,
        `two of the seeded states are drawn the same way at ${width} px: ${JSON.stringify([...states])}`
      ).toBe(states.size);

      // Ruling 6: the body draws the newest result of each check, and its earlier results are
      // one line with their count, which opens them in place. The check with two results draws
      // one, says "1 earlier result", and draws both once that line is pressed.
      expect(
        drawnChecks.filter((check) => check.results > 1).map((check) => check.id),
        `a check draws more than its newest result at ${width} px`
      ).toEqual([]);
      expect(
        drawnChecks.find((check) => check.id === twice)?.earlier,
        `the check with two results does not say how many earlier ones it holds at ${width} px`
      ).toBe(`1 ${VERIFICATION_WORDS.earlier_one}`);
      const folded = page.locator(
        `[data-verification-check][data-node-id="${twice}"] [data-verification-earlier]`
      );
      await folded.click();
      await expect(folded, "the line does not say it opened").toHaveAttribute(
        "aria-expanded",
        "true"
      );
      expect(
        (await verificationCards(page))
          .flatMap((card) => card.checks)
          .find((check) => check.id === twice)?.results,
        `the line does not open the earlier result in place at ${width} px`
      ).toBe(2);
      await folded.click();

      // A command renders as code with a copy control, and `when` reads as words: both in the
      // check's own facts, which the reader's press on its words opens.
      const opened = page.locator(`[data-verification-check][data-node-id="${twice}"]`);
      await opened.locator("[data-verification-check-words]").click();
      expect(
        await nodeCommands(page).count(),
        `the check's command is not drawn as code at ${width} px`
      ).toBeGreaterThan(0);
      expect(
        (
          await nodeCommands(page)
            .first()
            .evaluate((element) => getComputedStyle(element).fontFamily)
        ).toLowerCase(),
        `the command is not set in a monospace face at ${width} px`
      ).toMatch(/mono/);
      expect(
        await copyCommandControls(page).count(),
        `a command has no copy control at ${width} px`
      ).toBe(await nodeCommands(page).count());
      expect(
        (await opened.innerText()).replace(/\s+/g, " "),
        `the check draws its when attribute as a schema token at ${width} px`
      ).toContain(CHECK_WHEN_WORDS.before_merge);
      expect(
        await opened.innerText(),
        `the check draws the schema's own token at ${width} px`
      ).not.toContain("before_merge");

      // The screenshot the result recorded renders as a picture.
      const shot = opened.locator("[data-node-body] img").first();
      await expect(
        shot,
        `the seeded screenshot is drawn as no picture at ${width} px`
      ).toBeVisible();
      await expect
        .poll(() => shot.evaluate((element: HTMLImageElement) => element.naturalWidth), {
          message: `the seeded screenshot draws no image at ${width} px`,
        })
        .toBeGreaterThan(0);
      // The opened facts stay inside their card too.
      expect(
        await boxesPastTheirCard(page),
        `an element of the opened check reaches past its card at ${width} px`
      ).toEqual([]);

      // Nothing the body draws is told apart by colour alone.
      await page.emulateMedia({ forcedColors: "active" });
      const forced = await verificationCards(page);
      expect(
        forced.flatMap((card) => card.checks.map((check) => `${check.word} ${check.shape}`)),
        `the states are told apart differently once the colours are forced at ${width} px`
      ).toEqual(drawnChecks.map((check) => `${check.word} ${check.shape}`));
      expect(
        forced.map((card) => card.stateWord),
        `the statements are told apart differently once the colours are forced at ${width} px`
      ).toEqual(cards.map((card) => card.stateWord));
      await page.emulateMedia({ forcedColors: "none" });
    }

    // The seventh state: a payload carrying no answer over a check's results at all, which is
    // an older build behind the same load balancer as the one this page came from. The page
    // says what is true of it - that it could not read how the check stands - and never one of
    // the other six.
    await page.route("**/api/v1/issues/*/results", async (route) => {
      const answer = await route.fetch();
      // The results answer as the server sends it, of which this handler reads two fields.
      const body: { checks?: { block_id: string; pass_stands?: unknown }[] } = await answer.json();
      for (const check of body.checks ?? []) {
        if (check.block_id === seeded.checks.passed) delete check.pass_stands;
      }
      await route.fulfill({ json: body, response: answer });
    });
    await page.setViewportSize({ height: DESKTOP.height, width: DESKTOP.width });
    await openPreset(page, seeded.key, "verification");
    await expectIssuePageDrawn(page);
    await expect(
      verificationCardBoxes(page).first(),
      "the Verification view draws no statement on the rewritten answer"
    ).toBeVisible();
    await expect
      .poll(
        async () =>
          (await verificationCards(page))
            .flatMap((card) => card.checks)
            .filter((check) => check.state === "unreadable")
            .map((check) => [check.word, check.shape]),
        { message: "a check whose results the page cannot read draws no state of its own" }
      )
      .toEqual([[NODE_STATE_WORDS.check.unreadable, GLYPH_TOKENS.states.check.unreadable]]);
    await page.unroute("**/api/v1/issues/*/results");
  });
});

// c-view, row 2. The reviewer's finding on verification.e2e.ts:440: an issue that holds no
// statement and no check says so in the Verification view's own column once the load has
// settled, in the page's one line for it, and the line is read inside the tree the view draws
// and not off the whole page, which says it nowhere else.
test("the Verification filter says an issue holds no checks, in its own column", async ({
  browser,
}) => {
  const bare = async () => {
    await createProject({ key: PROJECT, name: "Issue layout" });
    return await createIssue({ force: true, project: PROJECT, title: "Nothing to verify yet" });
  };
  await phase(browser, bare, async (page, seeded) => {
    const settling = watchApiRequests(page);
    await openPreset(page, seeded.key, "verification");
    await expectLoadSettled(settling);
    await expectIssuePageDrawn(page);
    const body = issuePage(page).locator("[data-verification-body]");
    await expect(body, "the Verification filter draws no body of its own").toHaveCount(1);
    await expect(
      tree(page).getByText(EMPTY_STATE_LINES.verification, { exact: true }),
      "the view's tree does not say the issue holds no checks"
    ).toHaveCount(1);
    await expect(
      page.getByText(EMPTY_STATE_LINES.verification, { exact: true }),
      "the line is drawn outside the Verification view's tree as well"
    ).toHaveCount(1);
    await expect(verificationCardBoxes(page), "an issue with no statement draws one").toHaveCount(
      0
    );
    expect(await nodeRows(page).count(), "an issue with no check draws a row").toBe(0);
  });
});
