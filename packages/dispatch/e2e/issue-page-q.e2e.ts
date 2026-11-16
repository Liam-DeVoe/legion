import {
  CONTROL_LABELS,
  DECISIONS_WORDS,
  NODE_STATE_WORDS,
} from "../web/src/features/issue-page/page-words";
import { blockNodeId } from "../web/src/features/issue-page/tree-drafts";
import { getArtifact, getAsk, listComments } from "./api";
import { seedDecisionsFilter, seedReviewList } from "./issue-layout-seed";
import { OPEN_QUESTION_BLOCK_ID } from "./issue-layout-seed-text";
import { gotoIssue, phase } from "./issue-page";
import {
  DESKTOP,
  drawnRows,
  expectIssuePageDrawn,
  focusRow,
  nodeRow,
  PHONE,
  parentOfRow,
  rowOf,
  unfoldEveryBranch,
} from "./issue-page-g";
import { openPreset } from "./issue-page-i";
import { decisionsFoot, decisionsPartsDrawn, primarySlug, reviewFlag } from "./issue-page-q";
import { expect, test } from "./test";

/**
 * Part Q's rows of the new issue page (dispatch://INFRA-3937, part Q of dispatch://INFRA-1196):
 * the Decisions view - the open questions, the decisions that wait for the reader's review with
 * Keep and Change, the reader's own decisions, and the agents' other decisions folded at the
 * foot - read on the real stack. Each row turns the new page on for its reader through D's
 * `phase`, seeds what it reads with the seeds of `e2e/issue-layout-seed.ts`, and reads the page
 * through D's `e2e/issue-page.ts`, G's `e2e/issue-page-g.ts`, I's `e2e/issue-page-i.ts` and this
 * part's `e2e/issue-page-q.ts`.
 *
 * The rows come from the checkpoint's `e2e/issue-layout.e2e.ts`, each under the title its check
 * names (`verification.md` of dispatch://INFRA-3937, c-groups).
 */

// c-decisions. Clause by clause, at 1440 px and at 390 px. What waits on dispatch://INFRA-1150
// and is not read here: an agent decision's author (`decided_by`), its level of uncertainty and
// its reason, the reading under an agent's decision, and the `shaped` tuple itself (today's tie
// is a decision block's link to a spec block). What waits on dispatch://INFRA-1253: the
// unconverted issue, `?view=decisions`, and "the page makes no request to /decisions".
test("the Decisions filter draws its four parts and quotes the person's answer", async ({
  browser,
}) => {
  test.setTimeout(240_000);
  await phase(browser, seedDecisionsFilter, async (page, seeded) => {
    const specSlug = await primarySlug(seeded.key);
    const decisionsSlug = (await getArtifact(seeded.decisionsArtifactId)).slug;
    const D = (index: number) => blockNodeId(decisionsSlug, seeded.decisions[index] ?? "");
    const open = blockNodeId(specSlug, OPEN_QUESTION_BLOCK_ID);
    const stored = (await getAsk(seeded.optionedAskId)).ask.answer;
    expect(stored?.text ?? "", "the seed's answered ask holds no answer").not.toBe("");
    // A heading is its words and its count; the foot's is its control's, which says whether the
    // rows under it are drawn. White space between the parts of one is the browser's to lay out.
    const squeezed = (text: string) => text.replace(/\s+/gu, "");
    const headings = {
      agents: squeezed(`▸ 3 ${DECISIONS_WORDS.agents_many}`),
      open_questions: squeezed(`${DECISIONS_WORDS.parts.open_questions} 1`),
      review: squeezed(`${DECISIONS_WORDS.parts.review} 2`),
      yours: squeezed(`${DECISIONS_WORDS.parts.yours} 4`),
    };
    const parts = async () =>
      (await decisionsPartsDrawn(page)).map((part) => ({
        ...part,
        heading: squeezed(part.heading),
      }));
    for (const size of [DESKTOP, PHONE]) {
      const at = `at ${size.width} px`;
      await page.setViewportSize(size);
      await openPreset(page, seeded.key, "decisions");
      await expectIssuePageDrawn(page);
      // Four parts under their own headings, in this order: the open question first, the two
      // decisions waiting for review newest first, the reader's own decisions newest first -
      // the one kept, the question declined, the two answered - and the foot, folded.
      await expect.poll(parts, { message: `the four parts ${at}` }).toEqual([
        { count: "1", heading: headings.open_questions, part: "open_questions", rows: [open] },
        { count: "2", heading: headings.review, part: "review", rows: [D(5), D(0)] },
        {
          count: "4",
          heading: headings.yours,
          part: "yours",
          rows: [D(4), seeded.declinedAskId, seeded.optionedAskId, seeded.answeredAskId],
        },
        { count: "3", heading: headings.agents, part: "agents", rows: [] },
      ]);
      // No other sentence stands over them or between them.
      expect(
        await page.evaluate(() => {
          const body = document.querySelector("[data-decisions-body]")?.cloneNode(true);
          if (!(body instanceof Element)) return null;
          for (const row of body.querySelectorAll("[data-node-row]")) row.remove();
          return {
            paragraphs: body.querySelectorAll("p").length,
            text: (body.textContent ?? "").replace(/\s+/gu, ""),
          };
        }),
        `words outside the rows ${at}`
      ).toEqual({
        paragraphs: 0,
        text: `${headings.open_questions}${headings.review}${headings.yours}${headings.agents}`,
      });

      // A declined ask reads as a question the person declined, never as an answer.
      const declined = nodeRow(page, seeded.declinedAskId).first();
      await expect(declined.locator("[data-ask-declined] p").first()).toHaveText(
        `alice ${DECISIONS_WORDS.declined}`
      );
      await expect(declined.locator("[data-declined-note]")).toHaveText(
        "The release owner decides this one."
      );
      expect(rowOf(await drawnRows(page), seeded.declinedAskId)).toMatchObject({
        answerLine: null,
        answerText: null,
        state: "declined",
        stateWord: NODE_STATE_WORDS.ask.declined,
      });

      // The answered ask draws its question and options; with the focus on it, the person's
      // answer character for character as the store holds it, by who answered, with no reading
      // under it: the section of decisions.md that restates it is no row of the filter.
      const asked = nodeRow(page, seeded.optionedAskId).first();
      await asked.focus();
      await expect
        .poll(async () => rowOf(await drawnRows(page), seeded.optionedAskId)?.answerText ?? null, {
          message: `the answered ask does not draw the person's answer as the store holds it ${at}`,
        })
        .toBe(stored?.text ?? "");
      await expect(asked.locator('[data-node-option="chosen"]')).toHaveCount(1);
      await expect(asked.locator('[data-node-option="chosen"]')).toContainText("Rail");
      await expect(asked.locator("[data-node-option]")).toHaveCount(2);
      expect(
        rowOf(await drawnRows(page), seeded.optionedAskId)?.answerAuthor,
        `the answered ask does not name who answered ${at}`
      ).toBe(stored?.user);
      expect(await nodeRow(page, D(1)).count(), `the reading is a row of the filter ${at}`).toBe(0);

      // The three states, as words: waiting for review, kept with the keeper's name, replaced.
      await decisionsFoot(page).click();
      await expect
        .poll(async () => (await parts()).find((part) => part.part === "agents")?.rows.length, {
          message: `the foot did not unfold ${at}`,
        })
        .toBe(3);
      const rows = await drawnRows(page);
      expect(rowOf(rows, D(5))).toMatchObject({
        state: "waiting_review",
        stateWord: NODE_STATE_WORDS.decision.waiting_review,
      });
      expect(rowOf(rows, D(4))).toMatchObject({
        state: "kept",
        stateWord: `${NODE_STATE_WORDS.decision.kept} alice`,
      });
      expect(rowOf(rows, D(6))).toMatchObject({
        state: "replaced",
        stateWord: NODE_STATE_WORDS.decision.replaced,
      });
      expect(rowOf(rows, D(2))?.unsure, `the unsure decision does not say so ${at}`).toBe(
        CONTROL_LABELS.unsure
      );

      // The person's decision that shaped three statements is one entry with one node id - the
      // answer itself - and it reaches each of the three; the agent's reaches its two.
      expect(await nodeRow(page, seeded.answeredAskId).count()).toBe(1);
      await expect(
        nodeRow(page, seeded.answeredAskId).locator("[data-decision-statement] a"),
        `the person's decision does not reach the three statements it shaped ${at}`
      ).toHaveCount(3);
      await expect(nodeRow(page, D(0)).locator("[data-decision-statement] a")).toHaveCount(2);

      // Each entry links to the node that holds it, in the conversation.
      expect(
        await page.evaluate(() =>
          [...document.querySelectorAll("[data-decisions-body] [data-node-row]")]
            .filter((row) => row.querySelectorAll("a[data-entry-link]").length !== 1)
            .map((row) => row.getAttribute("data-node-id"))
        ),
        `entries with no link of their own ${at}`
      ).toEqual([]);
      await expect(
        nodeRow(page, seeded.optionedAskId).locator("a[data-entry-link]")
      ).toHaveAttribute("href", `/issues/${seeded.key}/asks/${seeded.optionedAskId}`);
    }

    // The same three words wherever a decision is drawn: with no filter on too, where the
    // section that restates the person's answer hangs under the ask it answers.
    await page.setViewportSize(DESKTOP);
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    await unfoldEveryBranch(page);
    const outline = await drawnRows(page);
    expect(rowOf(outline, D(5))?.stateWord).toBe(NODE_STATE_WORDS.decision.waiting_review);
    expect(rowOf(outline, D(4))?.stateWord).toBe(`${NODE_STATE_WORDS.decision.kept} alice`);
    expect(rowOf(outline, D(6))?.stateWord).toBe(NODE_STATE_WORDS.decision.replaced);
    expect(
      await parentOfRow(page, D(1)),
      "the decision that answers the ask is not drawn under it"
    ).toBe(seeded.answeredAskId);
  });
});

// c-review-list (R35). What waits on dispatch://INFRA-1150: the `shaped` tuple itself; today's
// tie is a decision block's link to a block of the spec.
test("only a decision that changed a statement waits for review @phone", async ({ browser }) => {
  test.setTimeout(150_000);
  await phase(browser, seedReviewList, async (page, seeded) => {
    const id = (block: string) => blockNodeId(seeded.decisionsSlug, block);
    const partRows = async (part: string) =>
      (await decisionsPartsDrawn(page)).find((one) => one.part === part)?.rows ?? [];
    for (const size of [DESKTOP, PHONE]) {
      const at = `at ${size.width} px`;
      await page.setViewportSize(size);
      await openPreset(page, seeded.key, "decisions");
      await expectIssuePageDrawn(page);
      // The review list holds the two a tie names a statement for, newest first.
      await expect
        .poll(() => partRows("review"), { message: `the review list ${at}` })
        .toEqual([id(seeded.tiedNewer), id(seeded.tiedOlder)]);
      // Each with its Keep and Change controls, with no focus on it.
      for (const block of [seeded.tiedNewer, seeded.tiedOlder]) {
        await expect(
          nodeRow(page, id(block)).locator("button[data-row-action]"),
          `${block} draws no Keep and Change ${at}`
        ).toHaveText([CONTROL_LABELS.keep_decision, CONTROL_LABELS.change_decision]);
      }
      // The Decisions control's flag holds those two and neither other decision, where the
      // controls stand apart (part D's view row, from 1280 px up).
      if (size === DESKTOP) await expect(reviewFlag(page), `the flag ${at}`).toHaveText("2");
      // The other two are in the filter's foot, with no Keep and no Change.
      expect(await partRows("agents"), `the foot draws its rows unasked ${at}`).toEqual([]);
      await decisionsFoot(page).click();
      await expect
        .poll(async () => [...(await partRows("agents"))].sort(), {
          message: `the foot's rows ${at}`,
        })
        .toEqual([id(seeded.tiedToNote), id(seeded.untied)].sort());
      for (const block of [seeded.tiedToNote, seeded.untied]) {
        await expect(
          nodeRow(page, id(block)).locator("button[data-row-action]"),
          `${block} draws a Keep or a Change ${at}`
        ).toHaveCount(0);
      }
    }
    // The other two are drawn in the outline under the issue, with no Keep or Change control,
    // the focus on them included.
    await page.setViewportSize(DESKTOP);
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    await unfoldEveryBranch(page);
    for (const block of [seeded.tiedToNote, seeded.untied]) {
      const row = await focusRow(page, id(block));
      expect(await parentOfRow(page, id(block)), `${block} hangs under another row`).toBeNull();
      await expect(
        row.locator("button[data-row-action]"),
        `${block} draws a Keep or a Change in the outline`
      ).toHaveCount(0);
    }
    await expect(reviewFlag(page)).toHaveText("2");
  });
});

// The tester's F6, under R7 and R35: a Change sent on a decision is a comment anchored to the
// decision's words. Once it is sent the decision says a change was asked for, leaves the review
// list for the reader's own decisions, takes no Keep and no Change, and draws the change under
// its words with a link to it; the Decisions control's flag drops by one; and with no filter on
// the decision says the same and the change hangs under it.
test("a change sent on a decision is drawn on it, and the decision leaves the review list", async ({
  browser,
}) => {
  test.setTimeout(150_000);
  await phase(browser, seedReviewList, async (page, seeded) => {
    const id = (block: string) => blockNodeId(seeded.decisionsSlug, block);
    const changed = id(seeded.tiedNewer);
    const words = "Tie it to the first statement instead.";
    const partRows = async (part: string) =>
      (await decisionsPartsDrawn(page)).find((one) => one.part === part)?.rows ?? [];

    await openPreset(page, seeded.key, "decisions");
    await expectIssuePageDrawn(page);
    await expect.poll(() => partRows("review")).toEqual([changed, id(seeded.tiedOlder)]);
    await expect(reviewFlag(page)).toHaveText("2");

    const row = nodeRow(page, changed).first();
    await row.locator('button[data-row-action="change"]').click();
    await row.getByLabel(CONTROL_LABELS.your_change).fill(words);
    await row.getByRole("button", { exact: true, name: CONTROL_LABELS.send_change }).click();

    // The decision leaves the review list and stands with the reader's own decisions.
    await expect
      .poll(() => partRows("review"), { message: "the changed decision still waits for review" })
      .toEqual([id(seeded.tiedOlder)]);
    expect(await partRows("yours"), "the changed decision is not among the reader's own").toEqual([
      changed,
    ]);
    // It says a change was asked for, by a word and a shape, and takes no Keep and no Change.
    expect(rowOf(await drawnRows(page), changed)).toMatchObject({
      state: "change_requested",
      stateWord: NODE_STATE_WORDS.decision.change_requested,
    });
    await expect(row.locator("button[data-row-action]")).toHaveCount(0);
    // The change is drawn under the decision's words, word for word, by who asked, with a link
    // to it in the conversation.
    const comment = (await listComments(seeded.key)).find((one) => one.body === words);
    expect(comment, "the server holds no comment for the change").toBeDefined();
    await expect(row.locator("[data-decision-change] [data-change-words]")).toHaveText(words);
    await expect(row.locator("[data-decision-change] [data-change-by]")).toHaveText("alice");
    await expect(row.locator("[data-decision-change] [data-change-link]")).toHaveAttribute(
      "href",
      `/issues/${seeded.key}/comments/${comment?.id}`
    );
    // The flag moves: one decision fewer waits for the reader's review.
    await expect(reviewFlag(page), "the flag did not move").toHaveText("1");

    // With no filter on, the decision says the same and the change hangs under it.
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    await unfoldEveryBranch(page);
    expect(rowOf(await drawnRows(page), changed)).toMatchObject({
      state: "change_requested",
      stateWord: NODE_STATE_WORDS.decision.change_requested,
    });
    expect(
      await parentOfRow(page, comment?.id ?? ""),
      "the change is not drawn under the decision it changes"
    ).toBe(changed);
    const outlined = await focusRow(page, changed);
    await expect(outlined.locator("button[data-row-action]")).toHaveCount(0);
    await expect(reviewFlag(page)).toHaveText("1");
  });
});
