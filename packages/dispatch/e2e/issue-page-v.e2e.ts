import AxeBuilder from "@axe-core/playwright";

import { presetPath } from "../web/src/features/issue-page/issue-address";
import {
  CHECK_WHEN_WORDS,
  type CheckState,
  CONTROL_LABELS,
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
  getIssueCoverage,
  getIssueResults,
  recordCheckResult,
  retractCheckResult,
} from "./api";
import { seedWholeIssue, type WholeIssueSeed } from "./issue-layout-seed";
import { HEAD_SHA, PROJECT } from "./issue-layout-seed-support";
import { requirementBlockId } from "./issue-layout-seed-text";
import { gotoIssue, issuePage, phase } from "./issue-page";
import {
  DESKTOP,
  expectIssuePageDrawn,
  focusThePage,
  nodeRows,
  PHONE,
  rowBody,
  rowMore,
  rowWords,
  tree,
} from "./issue-page-g";
import { activePreset, focusInside, identityBlockBottom, openPreset } from "./issue-page-i";
import {
  asRecorder,
  boxesPastTheirCard,
  checkLegendCounts,
  copyCommandControls,
  type DrawnCheck,
  glyphsWithNoBox,
  linkLook,
  namedCommit,
  nodeCommands,
  overlappingTexts,
  RUNBOOK,
  STATEMENT_B_WORDS,
  seedLinkedChecks,
  tabWalk,
  verificationBody,
  verificationCard,
  verificationCardBoxes,
  verificationCards,
  verificationCheck,
  verificationChip,
  verificationFrame,
  verificationIssueRow,
} from "./issue-page-v";
import { expect, test } from "./test";
import { expectLoadSettled, watchApiRequests } from "./traffic";

/**
 * Part V's rows of the new issue page (dispatch://INFRA-3936, part V of dispatch://INFRA-1196):
 * the Verification view, read on the real stack. Each row turns the new page on for its reader
 * with D's `phase`, seeds what it reads, and reads the view's cards through this part's
 * `e2e/issue-page-v.ts`, whose measures fail where the view draws no card.
 *
 * Rows 1 and 2 come from the checkpoint's `e2e/issue-layout.e2e.ts`, and rows 3 and 4 are new,
 * each under the title its check names (`verification.md` of dispatch://INFRA-3936, c-view).
 */

/** The issue's results read, which row 1 rewrites to draw the seventh state. */
const RESULTS_ROUTE = "**/api/v1/issues/*/results";

/** Records a pass of the whole issue's check 1 at the head commit, as the recorder's session,
 *  with what it observed and, where a row gives them, its screenshots. */
function recordPass(seeded: WholeIssueSeed, observed: string, screenshots?: string[]) {
  return recordCheckResult(
    seeded.key,
    {
      check: seeded.checks.passed,
      commit: HEAD_SHA,
      failed: 0,
      negative_control: "Removed the row's state and the run failed.",
      observed,
      role: "tester",
      run: "bun run e2e -- e2e/issue-page-v.e2e.ts",
      ...(screenshots === undefined ? {} : { screenshots }),
      surface: "browser",
      verdict: "pass",
    },
    asRecorder
  );
}

// c-view, row 1. The body the Verification view draws is `verification-filter/`: each statement
// with the checks that serve it, each check beside its newest result, and its earlier results
// folded to one line (ruling 6 of build-rulings-md, the tester's F15). Two of the seven states
// the seed cannot make, so this row makes them: `retracted` by withdrawing every result of one
// check, and `unreadable` by taking `pass_stands` out of the results answer in a route handler
// of its own, which is the one way a page meets a server that does not send it. The seeded
// Verification document holds no picture, so the row records a result carrying the issue's own
// uploaded shot. It reads too the view's measure at each break (part V's R6), the legend's
// counts, the facts opening on the focus and not on hover (R5), and the forced-colours marks.
// Each load's cards are counted before the page's tree is read, so at a base whose slot draws
// nothing the row fails on its own message.
// SLOW(>=2s): a whole seeded issue with one result recorded and another withdrawn, then seven
// loads of the view - six widths and a rewritten answer - each read clause by clause, with
// presses, focus moves and a forced-colours pass waited on at each width.
test("the Verification filter is readable @phone", async ({ browser }) => {
  test.setTimeout(420_000);
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    const specSlug = (await getArtifact(seeded.specArtifactId)).slug;
    const verificationSlug = (await getArtifact(seeded.verificationArtifactId)).slug;

    await recordPass(seeded, "1 passed, with a shot of the page", [
      `dispatch://${seeded.key}/artifact/${seeded.documents.image}`,
    ]);
    const recorded = await getIssueResults(seeded.key);
    const withdrawing = (recorded.checks ?? []).find(
      (check) => check.block_id === seeded.checks.uncontrolled
    );
    expect(withdrawing?.results ?? [], "the seed recorded no result to withdraw").not.toEqual([]);
    // The session that recorded a result is one of the three callers that may withdraw it, and
    // the agent bearer acts as no session unless it names one (`retractCheckResult`).
    for (const result of withdrawing?.results ?? []) {
      await retractCheckResult(result.id, { reason: "The run was wrong." }, asRecorder);
    }

    // What the issue's own reads say, which the page is read against.
    const statements = (await getIssueCoverage(seeded.key)).requirements ?? [];
    const covered = statements.filter((statement) => statement.checks > 0).length;
    const nodeOf = (index: number) => blockNodeId(specSlug, requirementBlockId(index));
    const twice = blockNodeId(verificationSlug, seeded.checks.passed);
    // The checks that serve each statement: check i serves statement i, for 1 to 6.
    const servedBy = new Map(
      seeded.checks.all.map((check, at) => [nodeOf(at + 1), [blockNodeId(verificationSlug, check)]])
    );
    // What the legend counts: each drawn check once, in the state it is drawn in, and 0 for a
    // state no drawn check is in.
    const legendOf = (checks: readonly DrawnCheck[]) => {
      const states = [...new Map(checks.map((check) => [check.id, check.state])).values()];
      return Object.fromEntries(
        Object.keys(NODE_STATE_WORDS.check).map((state) => [
          state,
          states.filter((held) => held === state).length,
        ])
      );
    };
    // Every check the view draws now, read afresh, under each card it is drawn on.
    const drawnChecksNow = async () =>
      (await verificationCards(page)).flatMap((card) => card.checks);

    for (const width of [1440, 1024, 800, 760, 390, 320]) {
      await page.setViewportSize({ height: width < 1024 ? PHONE.height : DESKTOP.height, width });
      await openPreset(page, seeded.key, "verification");
      expect(await activePreset(page), `the view is not named Verification at ${width} px`).toBe(
        PRESET_NAMES.verification
      );
      await expect
        .poll(() => verificationCardBoxes(page).count(), {
          message: `the Verification filter draws no statement at ${width} px`,
        })
        .toBeGreaterThan(0);
      await expectIssuePageDrawn(page);
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
      expect(
        Object.fromEntries(cards.map((card) => [card.id, card.checks.map((check) => check.id)])),
        `a card draws other checks than the ones that serve its statement at ${width} px`
      ).toEqual(Object.fromEntries(cards.map((card) => [card.id, servedBy.get(card.id) ?? []])));

      // No value reaches past either side of its card at any width, and no two values overlap:
      // the sixteen faults of the old tab this requirement is here for.
      expect(
        await boxesPastTheirCard(page),
        `an element reaches past its card at ${width} px`
      ).toEqual([]);
      expect(
        await overlappingTexts(page),
        `two pieces of text are drawn over each other at ${width} px`
      ).toEqual([]);

      // The view's measure (part V's R6). The document is no wider than the screen. From 761 px
      // up the body is 1180 px wide, or its column less 48 px where that is less, with 22 px
      // above it and 120 px under it, and the table's three columns stand at 38, 36 and 26 per
      // cent of its width; at 760 px and below the body is its column less 20 px with 10 px above
      // it, no heading is drawn, and each statement is a card with a 4 px left edge and 8 px
      // corners, 8 px from the next.
      const frame = await verificationFrame(page);
      expect(frame.page.scroll, `the page scrolls sideways at ${width} px`).toBeLessThanOrEqual(
        frame.page.viewport
      );
      if (width === 1440 || width === 1024) {
        const measure = width === 1440 ? 1180 : frame.body.parent - 48;
        expect(
          Math.abs(frame.body.width - measure),
          `the body is ${frame.body.width} px wide, not ${measure} px, at ${width} px`
        ).toBeLessThanOrEqual(1);
      }
      if (width > 760) {
        expect(
          [frame.body.top, frame.body.bottom],
          `the body's top and bottom margins are not 22 and 120 px at ${width} px`
        ).toEqual([22, 120]);
      } else {
        expect(frame.body.top, `the body's top margin is not 10 px at ${width} px`).toBe(10);
        expect(
          Math.abs(frame.body.width - (frame.body.parent - 20)),
          `the body is ${frame.body.width} px wide in a column of ${frame.body.parent} px at ${width} px`
        ).toBeLessThanOrEqual(1);
        await expect(
          page.locator("[data-verification-head]"),
          `a column heading is drawn at ${width} px`
        ).toBeHidden();
        expect(
          frame.firstCard,
          `the first statement is no card with a 4 px left edge and 8 px corners at ${width} px`
        ).toMatchObject({ borderLeft: "4px", radius: "8px" });
        expect(
          Math.abs((frame.firstCard.gap ?? Number.NaN) - 8),
          `the first two cards do not stand 8 px apart at ${width} px`
        ).toBeLessThanOrEqual(1);
      }
      if (width === 800) {
        expect(
          frame.columns.heads,
          "the table draws other than three headings at 800 px"
        ).toHaveLength(3);
        for (const [at, percent] of [38, 36, 26].entries()) {
          const head = frame.columns.heads[at] ?? 0;
          expect(
            Math.abs(head - (frame.columns.table * percent) / 100),
            `heading ${at + 1} is ${head} px of the table's ${frame.columns.table} px, not ${percent} per cent, at 800 px`
          ).toBeLessThanOrEqual(2);
        }
      }

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
        [...states],
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

      // The legend counts each drawn check once, in the state it is drawn in, the states no
      // check is in included (part V's R2).
      expect(
        await checkLegendCounts(page),
        `the legend does not count the drawn checks in their states at ${width} px`
      ).toEqual(legendOf(drawnChecks));

      // Ruling 6: the body draws the newest result of each check, and its earlier results are
      // one line with their count, which opens them in place. The check with two results draws
      // one, marked as the one its state stands on, says "1 earlier result", draws both once
      // that line is pressed, and one again at a second press.
      expect(
        drawnChecks.filter((check) => check.results > 1).map((check) => check.id),
        `a check draws more than its newest result at ${width} px`
      ).toEqual([]);
      expect(
        drawnChecks.find((check) => check.id === twice)?.earlier,
        `the check with two results does not say how many earlier ones it holds at ${width} px`
      ).toBe(`1 ${VERIFICATION_WORDS.earlier_one}`);
      const opened = verificationCheck(page, twice);
      const standing = opened.locator("[data-verification-result][data-result-stands]");
      await expect(
        standing,
        `check 1 marks no one result as the one it stands on at ${width} px`
      ).toHaveCount(1);
      expect(
        (await standing.locator("p").first().innerText()).slice(
          0,
          VERIFICATION_WORDS.stands.length
        ),
        `check 1's standing result's line does not begin with its mark at ${width} px`
      ).toBe(VERIFICATION_WORDS.stands);
      const folded = opened.locator("[data-verification-earlier]");
      await folded.click();
      await expect(folded, "the line does not say it opened").toHaveAttribute(
        "aria-expanded",
        "true"
      );
      expect(
        (await drawnChecksNow()).find((check) => check.id === twice)?.results,
        `the line does not open the earlier result in place at ${width} px`
      ).toBe(2);
      await folded.click();
      await expect
        .poll(async () => (await drawnChecksNow()).find((check) => check.id === twice)?.results, {
          message: `a second press on the line does not fold the earlier result at ${width} px`,
        })
        .toBe(1);

      // A command renders as code with a copy control, and `when` reads as words: both in the
      // check's own facts, which the reader's focus on its words opens, with no press (R5).
      await focusThePage(page);
      await expect(
        rowBody(opened),
        `check 1's facts are drawn with the focus off it at ${width} px`
      ).toHaveCount(0);
      await opened.locator("[data-verification-check-words]").focus();
      await expect(
        rowBody(opened).first(),
        `the focus on check 1's words does not open its facts at ${width} px`
      ).toBeVisible();
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
      const shot = rowBody(opened).locator("img").first();
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

      // Nothing opens on hover (R5): with the focus off the view, the pointer over check 2's
      // words draws no facts under it.
      const failing = verificationCheck(page, blockNodeId(verificationSlug, seeded.checks.failed));
      await focusThePage(page);
      await failing.locator("[data-verification-check-words]").hover();
      expect(
        await rowBody(failing).count(),
        `the pointer over check 2's words opened its facts at ${width} px`
      ).toBe(0);

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
      // Each shape keeps a box of its own, and the pressed chip keeps a mark of its own, its
      // outline, where the system draws every chip's fill and border alike (departure 4).
      const glyphs = await glyphsWithNoBox(page);
      expect(
        glyphs.drawn,
        `the view draws no shape once the colours are forced at ${width} px`
      ).toBeGreaterThan(0);
      expect(
        glyphs.empty,
        `a shape draws no box once the colours are forced at ${width} px`
      ).toEqual([]);
      const pressed = issuePage(page).locator('[data-verification-chip][aria-pressed="true"]');
      await expect(pressed, `no one chip is pressed at ${width} px`).toHaveCount(1);
      await expect(
        pressed,
        `the pressed chip draws no outline once the colours are forced at ${width} px`
      ).not.toHaveCSS("outline-style", "none");
      await page.emulateMedia({ forcedColors: "none" });
    }

    // The seventh state: a payload carrying no answer over a check's results at all, which is
    // an older build behind the same load balancer as the one this page came from. The page
    // says what is true of it - that it could not read how the check stands - and never one of
    // the other six.
    await page.route(RESULTS_ROUTE, async (route) => {
      const answer = await route.fetch();
      // The results answer as the server sends it, of which this handler reads two fields.
      const body: { checks?: { block_id: string; pass_stands?: unknown }[] } = await answer.json();
      for (const check of body.checks ?? []) {
        if (check.block_id === seeded.checks.passed) delete check.pass_stands;
      }
      await route.fulfill({ json: body, response: answer });
    });
    await page.setViewportSize(DESKTOP);
    await openPreset(page, seeded.key, "verification");
    await expect(
      verificationCardBoxes(page).first(),
      "the Verification view draws no statement on the rewritten answer"
    ).toBeVisible();
    await expectIssuePageDrawn(page);
    await expect
      .poll(
        async () =>
          (await drawnChecksNow())
            .filter((check) => check.state === "unreadable")
            .map((check) => [check.word, check.shape]),
        { message: "a check whose results the page cannot read draws no state of its own" }
      )
      .toEqual([[NODE_STATE_WORDS.check.unreadable, GLYPH_TOKENS.states.check.unreadable]]);
    expect(
      await checkLegendCounts(page),
      "the legend does not count the check whose results the page cannot read in its own state"
    ).toEqual(legendOf(await drawnChecksNow()));
    await page.unroute(RESULTS_ROUTE);
  });
});

// c-view, row 2. The reviewer's finding on `verification.e2e.ts`: an issue that holds no
// statement and no check says so in the Verification view's own column once the load has
// settled, in the page's one line for it, and the line is read inside the tree the view draws
// and not off the whole page, which says it nowhere else. The body is counted before the page's
// tree is read, so at a base whose slot draws nothing the row fails on its own message.
// SLOW(>=2s): a new issue's view loaded and waited out to a 1.5 s quiet window before it is read.
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
    const body = verificationBody(page);
    await expect(body, "the Verification filter draws no body of its own").toHaveCount(1);
    await expectIssuePageDrawn(page);
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

// c-view, row 3 (part V's R5, R7, R8 and R9). A check's words are drawn with their links through
// G's `RowText`: a link in the words' colour, underlined in the bullets' grey, and a
// `dispatch://` reference as the chip that names its target, never as its address. A result an
// agent recorded names it by its role's word. A check that serves two statements is drawn under
// each, names the other by its first words, and is counted once. A statement that only a child's
// check serves is covered, carries no gap, and says so in one line. A statement of 200 words is
// cut at six lines, and its "more" is a tab stop between the chips and the check's link.
// SLOW(>=2s): an issue, its child and their two documents seeded and waited for in the coverage
// read, then the view loaded and read at two widths, with a walk by Tab at the first.
test("the Verification view draws a check's links, its agent's role and a child's checks @phone", async ({
  browser,
}) => {
  test.setTimeout(180_000);
  await phase(browser, seedLinkedChecks, async (page, seeded) => {
    const { a, b, c, check } = seeded.nodes;
    const cut = new RegExp(
      `^${CONTROL_LABELS.read_more} · [\\d,]+ ${CONTROL_LABELS.read_more_words}$`,
      "u"
    );
    for (const width of [1440, 390]) {
      await page.setViewportSize({ height: width < 1024 ? PHONE.height : DESKTOP.height, width });
      await openPreset(page, seeded.key, "verification");
      const statementA = verificationCard(page, a);
      await expect(
        statementA,
        `the Verification view draws no card for statement A at ${width} px`
      ).toHaveCount(1);
      await expectIssuePageDrawn(page);

      // The check is drawn on A's card and on B's, and on no other, and the legend counts it
      // once, in its state.
      const cards = await verificationCards(page);
      expect(
        Object.fromEntries(cards.map((card) => [card.id, card.checks.map((one) => one.id)])),
        `the check is not drawn under each statement it serves and under no other at ${width} px`
      ).toEqual({ [a]: [check], [b]: [check], [c]: [] });
      const legend = await checkLegendCounts(page);
      const state = cards.find((card) => card.id === a)?.checks[0]?.state ?? "";
      expect(
        [Object.values(legend).reduce((sum, count) => sum + count, 0), legend[state]],
        `the legend does not count the check it draws twice once, in its state, at ${width} px`
      ).toEqual([1, 1]);

      // A is cut at six lines, and its "more" opens it whole: at 1440 px from the keyboard, Tab
      // from the chip All reaching it and then the link in the check's words (R8); at 390 px by a
      // press.
      const words = rowWords(statementA);
      const more = rowMore(statementA);
      await expect(more, `statement A draws no "more" at ${width} px`).toHaveText(cut);
      expect(
        Number(/[\d,]+/u.exec(await more.innerText())?.[0]?.replace(/,/g, "")),
        `statement A's cut holds back 20 words or fewer at ${width} px`
      ).toBeGreaterThan(20);
      expect(
        (await words.boundingBox())?.height ?? Number.POSITIVE_INFINITY,
        `statement A is not cut at six lines at ${width} px`
      ).toBeLessThanOrEqual(6 * 24 + 12);
      const link = statementA.locator(`[data-verification-check-words] a[href="${RUNBOOK}"]`);
      const tabTo = tabWalk(page, 20);
      if (width === 1440) {
        await verificationChip(page, "all").focus();
        await tabTo(
          more,
          `Tab from the chip All does not reach statement A's "more" at ${width} px`
        );
        await page.keyboard.press("Enter");
      } else {
        await more.click();
      }
      await expect(more, `"more" does not open statement A at ${width} px`).toHaveText(
        CONTROL_LABELS.read_less
      );
      expect(
        await words.evaluate((element) => element.scrollHeight - element.clientHeight),
        `statement A hides a word once "more" opened it at ${width} px`
      ).toBeLessThanOrEqual(1);
      if (width === 1440) {
        await tabTo(
          link,
          `Tab from statement A's "more" does not reach the link in the check's words at ${width} px`
        );
      }

      // The link is an `a` with its address, in the words' colour, underlined in #A8A29E (R5).
      await expect(
        link,
        `the check's words draw no link to the runbook at ${width} px`
      ).toHaveCount(1);
      const look = await linkLook(link);
      expect(look.color, `the link is not in the check's words' colour at ${width} px`).toBe(
        look.words
      );
      expect(look.line, `the link is not underlined at ${width} px`).toContain("underline");
      expect(
        look.underline,
        `the link is underlined in another colour than #A8A29E at ${width} px`
      ).toBe("rgb(168, 162, 158)");

      // No text reads an address: the check's reference to B is the chip that names B.
      await expect
        .poll(() => verificationBody(page).innerText(), {
          message: `the view draws a dispatch:// address as words at ${width} px`,
        })
        .not.toContain("dispatch://");

      // The result names the agent that recorded it by its role's word (R7).
      await expect(
        statementA.locator("[data-verification-result] [data-node-author]"),
        `the result does not name its agent by its role at ${width} px`
      ).toHaveText("Recorder");

      // "Also under" names B by its first words, cut at a word within 32 characters (R7).
      const alsoUnder = statementA.locator("[data-verification-check] [data-node-trail]");
      const chip = alsoUnder.locator("[data-node-chip]");
      await expect(
        chip,
        `the check under A names no other statement it serves at ${width} px`
      ).toHaveCount(1);
      const named = (await chip.innerText()).trim();
      await expect(
        alsoUnder,
        `the check under A does not say it is also under B at ${width} px`
      ).toHaveText(`${CONTROL_LABELS.also_under} ${named}`);
      expect(named, `"Also under" names B by no words at ${width} px`).not.toBe("");
      expect(
        named.length,
        `"Also under" names B by more than 32 characters at ${width} px: "${named}"`
      ).toBeLessThanOrEqual(32);
      expect(
        STATEMENT_B_WORDS.startsWith(named.replace(/…$/u, "")),
        `"Also under" names B by other than its first words at ${width} px: "${named}"`
      ).toBe(true);

      // C is covered by the child's check alone: it carries no gap, says so in one line, and the
      // summary counts it covered (R9).
      expect(
        cards.find((card) => card.id === c)?.gapKinds,
        `statement C carries a gap at ${width} px`
      ).toEqual([]);
      await expect(
        verificationCard(page, c).locator("[data-verification-elsewhere]"),
        `statement C does not say a check of the issue under this one serves it at ${width} px`
      ).toHaveText(`1 ${VERIFICATION_WORDS.elsewhere_one}`);
      await expect(
        page.locator("[data-verification-covered]"),
        `statement C is not counted covered at ${width} px`
      ).toHaveText(`3 ${VERIFICATION_WORDS.of} 3 ${VERIFICATION_WORDS.covered}`);
    }
  });
});

// c-view, row 4 (part V's R8, the tester's F21, F22 and F23). From the issue's own address `t k`
// turns the view on with the focus on the issue's row and the view's top on screen, and asks the
// server for nothing the first load did not; Tab alone then reaches the chips, a check's words,
// the copy control its facts draw and its line of earlier results, in reading order, and Enter
// works each one; the tree answers the outline's keys, and a press on a result's cell or on a
// statement's words opens a check's facts; axe finds no break of the tree's structure; `t k`
// from a row turns the view off; and the view's own address leaves the focus on the issue's row.
// SLOW(>=2s): a whole seeded issue loaded and waited out to a quiet window twice, a walk of up to
// 40 Tabs and some twenty keys and presses each waited on, an axe pass, and a third load.
test("the Verification view is worked by the keyboard alone", async ({ browser }) => {
  test.setTimeout(180_000);
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
    await recordPass(seeded, "1 passed, a second time");
    const [{ slug: specSlug }, { slug: verificationSlug }, coverage, results] = await Promise.all([
      getArtifact(seeded.specArtifactId),
      getArtifact(seeded.verificationArtifactId),
      getIssueCoverage(seeded.key),
      getIssueResults(seeded.key),
    ]);
    const nodeOf = (index: number) => blockNodeId(specSlug, requirementBlockId(index));
    const checkOf = (index: number) =>
      blockNodeId(verificationSlug, seeded.checks.all[index - 1] ?? "");
    const statements = coverage.requirements ?? [];
    const command =
      (results.checks ?? []).find((one) => one.block_id === seeded.checks.passed)?.run ?? "";
    expect(command, "check 1 holds no command").not.toBe("");

    // From the issue's own address, with the focus on the page, `t k` turns the view on, leaves
    // the focus on the issue's row inside it with the view's top on screen under the header's
    // band, and asks the server for nothing the first load did not (R1, R8).
    const traffic = watchApiRequests(page);
    await gotoIssue(page, presetPath(seeded.key, "conversation"));
    await expectLoadSettled(traffic);
    await expectIssuePageDrawn(page);
    const loaded = new Set(traffic.requests.map((request) => request.path));
    traffic.reset();
    await focusThePage(page);
    await page.keyboard.press("t");
    await page.keyboard.press("k");
    const body = verificationBody(page);
    await expect
      .poll(() => focusInside(body), {
        message: "t k left the focus outside the Verification view",
      })
      .toBe(true);
    await expect(page, "t k did not open the Verification view's address").toHaveURL(
      new RegExp(`${presetPath(seeded.key, "verification")}$`)
    );
    await expect(
      verificationIssueRow(page),
      "t k left the focus on another element of the view than the issue's row"
    ).toBeFocused();
    // The header's band, where it stays at the top of the screen; 0 where the page draws none
    // or it does not stay, as I's rows read it.
    const band = (await identityBlockBottom(page)) ?? 0;
    const top = await body.evaluate((element) => element.getBoundingClientRect().top);
    expect(
      top,
      `t k left the view's top under the header's band, which ends at ${band} px`
    ).toBeGreaterThanOrEqual(band);
    expect(top, "t k left the view's top below the screen").toBeLessThan(
      page.viewportSize()?.height ?? 0
    );
    await expectLoadSettled(traffic);
    expect(
      traffic.requests
        .filter((request) => !loaded.has(request.path))
        .map((request) => `${request.method} ${request.path}`),
      "t k sent a request under /api/ to a path the first load did not"
    ).toEqual([]);

    // Tab alone, at most 40 presses, reaches each control in reading order, and Enter works it
    // (R8).
    const tabTo = tabWalk(page, 40);
    expect(
      await tabTo(verificationChip(page, "all"), "Tab does not reach the chip All"),
      "the first Tab after t k reaches another control than the chip All"
    ).toBe(1);
    await tabTo(verificationChip(page, "not_met"), "Tab does not reach the chip Not met");
    await page.keyboard.press("Enter");
    await expect
      .poll(async () => (await verificationCards(page)).map((card) => card.id), {
        message: "Enter on the chip Not met does not draw statement 2 alone",
      })
      .toEqual([nodeOf(2)]);
    await page.keyboard.press("Enter");
    await expect
      .poll(async () => (await verificationCards(page)).length, {
        message: "a second Enter on the chip Not met does not draw every statement",
      })
      .toBe(statements.length);
    const check1 = verificationCheck(page, checkOf(1));
    await tabTo(
      check1.locator("[data-verification-check-words]"),
      "Tab does not reach check 1's words"
    );
    await expect(
      rowBody(check1).first(),
      "the focus on check 1's words does not open its facts"
    ).toBeVisible();
    await tabTo(
      rowBody(check1)
        .getByRole("button", { exact: true, name: CONTROL_LABELS.copy_command })
        .first(),
      "Tab does not reach the copy control of check 1's command"
    );
    await page.keyboard.press("Enter");
    await expect
      .poll(() => page.evaluate(() => navigator.clipboard.readText()), {
        message: "Enter on the copy control does not put check 1's whole command on the clipboard",
      })
      .toBe(command);
    await tabTo(
      check1.locator("[data-verification-earlier]"),
      "Tab does not reach check 1's line of earlier results"
    );
    await page.keyboard.press("Enter");
    await expect
      .poll(() => check1.locator("[data-verification-result]").count(), {
        message: "Enter on check 1's line of earlier results does not draw two results",
      })
      .toBe(2);

    // The tree's keys, counted from the row the focus is on or from the row that holds the
    // control it is on (R8).
    await page.keyboard.press("Home");
    await expect(verificationIssueRow(page), "Home does not focus the issue's row").toBeFocused();
    await page.keyboard.press("j");
    await expect(
      verificationCard(page, nodeOf(1)),
      "j does not move from the issue's row to statement 1's"
    ).toBeFocused();
    await page.keyboard.press("ArrowRight");
    await expect(check1, "ArrowRight does not move from statement 1 to its check").toBeFocused();
    await expect(
      rowBody(check1).first(),
      "the focus on check 1's row does not draw its facts"
    ).toBeVisible();
    await page.keyboard.press("Enter");
    await expect(check1, "Enter on check 1's row does not shut its facts").toHaveAttribute(
      "aria-expanded",
      "false"
    );
    await expect(rowBody(check1), "check 1 draws facts it shut").toHaveCount(0);
    await page.keyboard.press("Enter");
    await expect(check1, "a second Enter on check 1's row does not open its facts").toHaveAttribute(
      "aria-expanded",
      "true"
    );
    await page.keyboard.press("j");
    await expect(check1, "j does not move on from check 1's row").not.toBeFocused();
    await expect(check1, "j shut the facts that Enter opened on check 1").toHaveAttribute(
      "aria-expanded",
      "true"
    );
    await page.keyboard.press("End");
    await expect(
      body.locator("[data-node-row]").last(),
      "End does not reach the view's last row"
    ).toBeFocused();

    // A check's facts open from a press on its latest result's cell and on its statement's words
    // (the tester's F21).
    const check2 = verificationCheck(page, checkOf(2));
    await expect(check2, "check 2's facts are drawn before a press").toHaveAttribute(
      "aria-expanded",
      "false"
    );
    await check2.locator("[data-verification-result]").first().click();
    await expect(
      check2,
      "a click on check 2's latest result does not open its facts"
    ).toHaveAttribute("aria-expanded", "true");
    const check3 = verificationCheck(page, checkOf(3));
    await expect(check3, "check 3's facts are drawn before a press").toHaveAttribute(
      "aria-expanded",
      "false"
    );
    await rowWords(verificationCard(page, nodeOf(3))).click();
    await expect(
      check3,
      "a click on statement 3's words does not open check 3's facts"
    ).toHaveAttribute("aria-expanded", "true");

    // The tree's structure, by the rules that read it (part V's departure 8).
    const scan = await new AxeBuilder({ page })
      .include("[data-verification-body]")
      .withRules([
        "aria-required-children",
        "aria-required-parent",
        "nested-interactive",
        "aria-allowed-attr",
        "aria-valid-attr-value",
        "button-name",
        "link-name",
      ])
      .analyze();
    expect(
      scan.violations.flatMap((violation) =>
        violation.nodes.map(
          (node) =>
            `${violation.id} at ${node.target
              .map((part) => (Array.isArray(part) ? part.join(" ") : String(part)))
              .join(" ")}`
        )
      ),
      "axe finds the view's tree breaking a rule of its structure"
    ).toEqual([]);

    // `t` then `k` with the focus on a row of the view turns it off: the tree's keys do nothing
    // while a chord is pending, so the page's keymap takes the second key.
    await verificationCard(page, nodeOf(1)).focus();
    await page.keyboard.press("t");
    await page.keyboard.press("k");
    await expect(page, "t k from a row of the view does not turn it off").toHaveURL(
      new RegExp(`${presetPath(seeded.key, "conversation")}$`)
    );
    await expect(body, "t k from a row of the view leaves the view drawn").toHaveCount(0);

    // The view's own address, opened with nothing focused, leaves the focus on the issue's row
    // (the tester's F23).
    await gotoIssue(page, presetPath(seeded.key, "verification"));
    await expect(
      verificationIssueRow(page),
      "the view's own address leaves the focus elsewhere than on the issue's row"
    ).toBeFocused();
  });
});
