import type { Locator } from "@playwright/test";

import { presetPath } from "../web/src/features/issue-page/issue-address";
import { firstLineOf } from "../web/src/features/issue-page/issue-tree";
import {
  CONTROL_LABELS,
  EMPTY_STATE_LINES,
  PRESET_NAMES,
  PRESET_ORDER,
  SPEC_WORDS,
} from "../web/src/features/issue-page/page-words";
import { blockNodeId } from "../web/src/features/issue-page/tree-drafts";
import {
  createAsk,
  getArtifact,
  getAsk,
  getIssueDecisions,
  listComments,
  listIssueAsks,
} from "./api";
import {
  seedNoChildren,
  seedNoSummary,
  seedUnbrokenWords,
  seedWholeIssue,
  UNBROKEN_PATH,
} from "./issue-layout-seed";
import { requirementBlockId, SUMMARY_SENTENCES } from "./issue-layout-seed-text";
import {
  contentsPane,
  gotoIssue,
  issuePage,
  outlineControl,
  outlineSheet,
  phase,
} from "./issue-page";
import {
  DESKTOP,
  drawnRows,
  expectIssuePageDrawn,
  focusThePage,
  nodeRow,
  nodeRows,
  PHONE,
  rowOf,
  tree,
} from "./issue-page-g";
import { focusedNodeId, openPreset, rowGaps } from "./issue-page-i";
import {
  becauseLines,
  contentsEntries,
  contentsLineFaults,
  decidedByCells,
  focusBranchControl,
  IPHONE_13,
  rowAuthor,
  TYPEFACE,
  threeThings,
  typefaceAddress,
  typefaceStatuses,
} from "./issue-page-j";
import { withIssuePage } from "./issue-page-option";
import { expect, test } from "./test";
import { expectLoadSettled, watchApiRequests } from "./traffic";
import { assertPageFits } from "./unclipped";
import { asUser } from "./users";

/**
 * Part J's rows of the new issue page (dispatch://INFRA-3939, part J of dispatch://INFRA-1196):
 * the issue's three sentences, the Contents list and the Spec view, read on the real stack,
 * because they read the server's stored decisions, asks, coverage and results. Each row turns
 * the new page on for alice with `withIssuePage` through D's `phase`, seeds what it reads with
 * the seeds of `e2e/issue-layout-seed.ts`, and reads the page through D's `e2e/issue-page.ts`,
 * G's `e2e/issue-page-g.ts`, I's `e2e/issue-page-i.ts` and this part's `e2e/issue-page-j.ts`.
 *
 * The rows come from the checkpoint's `e2e/issue-layout.e2e.ts`, each under the title its
 * check names (`verification.md` of dispatch://INFRA-3939, c-rows).
 */

// c-three-things, as the architect's ruling 5 reads R8 (build-rulings-md): the three sentences
// stand each after its label; with no view on they are in the Contents pane at 1440 px and in
// the Outline sheet at 390 px, never above the outline; under Spec they are the view's first
// group; under Verification, Plan and Decisions they are not drawn; no Contents pane is drawn
// under a view; and the two rows "How it gets done" and "What you will be shown" are drawn
// nowhere. Partial on today's data: who wrote the sentences and when they were last read back
// are dispatch://INFRA-1278, so the head draws neither and this test never asserts an author.
test("the three sentences stand with their labels in the Contents pane, the Spec filter and the Outline sheet @phone", async ({
  browser,
}) => {
  // Twelve loads of the page, each read for its head and the page's own draw.
  test.setTimeout(180_000);
  const labels = [
    CONTROL_LABELS.in_short_what,
    CONTROL_LABELS.in_short_why,
    CONTROL_LABELS.in_short_done,
  ];
  const spoken = async (head: Locator): Promise<string[]> =>
    (await head.locator("li [data-author-text]").allInnerTexts()).map((sentence) =>
      sentence.replace(/\s+/g, " ").trim()
    );
  const labelled = async (head: Locator): Promise<string[]> =>
    (await head.locator("li [data-page-chrome]").allInnerTexts()).map((label) => label.trim());
  /** The head `head` draws: the spec's own three sentences, each after its label, and neither
   *  of the two rows. The sentences are the summary node's words, which the batched reference
   *  read names last of the page's reads, so the reading waits for them. */
  const expectTheThree = async (head: Locator, where: string): Promise<void> => {
    await expect(head, `the three sentences are not drawn ${where}`).toHaveCount(1);
    await expect
      .poll(() => spoken(head), {
        message: `the spec's own three sentences are not drawn ${where}`,
      })
      .toEqual([...SUMMARY_SENTENCES]);
    expect(await labelled(head), `the three sentences carry no labels ${where}`).toEqual(labels);
    const words = (await head.innerText()).replace(/\s+/g, " ");
    for (const row of ["How it gets done", "What you will be shown"]) {
      expect(words, `"${row}" is drawn ${where}`).not.toContain(row);
    }
    expect(await head.getByRole("link").count(), `the head holds a link ${where}`).toBe(0);
  };

  await phase(browser, seedWholeIssue, async (page, seeded) => {
    // 1440 px, no view: in the Contents pane, at the left of the outline.
    await page.setViewportSize({ height: DESKTOP.height, width: DESKTOP.width });
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    await expect(contentsPane(page), "no Contents pane is drawn with no view on").toHaveCount(1);
    await expectTheThree(
      contentsPane(page).getByTestId("issue-three-things"),
      "in the Contents pane at 1440 px"
    );
    expect(await threeThings(page).count(), "the three sentences are drawn twice").toBe(1);
    const pane = await contentsPane(page).boundingBox();
    const outline = await tree(page).boundingBox();
    expect(pane, "the Contents pane has no box").not.toBeNull();
    expect(outline, "the outline has no box").not.toBeNull();
    expect(
      (pane?.x ?? 0) + (pane?.width ?? 0),
      "the Contents pane is not at the left of the outline"
    ).toBeLessThanOrEqual((outline?.x ?? 0) + 1);

    // Who wrote the sentences and when they were last read back wait on dispatch://INFRA-1278,
    // and no issue holds either, so the head draws no author and no gap for them.
    expect(
      await rowAuthor(threeThings(page)).count(),
      "the head names an author, which the model does not hold"
    ).toBe(0);
    expect(
      await rowGaps(threeThings(page)).count(),
      "the head draws a gap that every issue has, which tells the reader nothing about this one"
    ).toBe(0);

    // Under a view, at both widths: no Contents pane; the Spec view draws the three as its
    // first group, above its first statement; the other three draw none.
    for (const size of [DESKTOP, PHONE]) {
      await page.setViewportSize({ height: size.height, width: size.width });
      for (const preset of PRESET_ORDER) {
        if (preset === "conversation") continue;
        const where = `with the ${PRESET_NAMES[preset]} view at ${size.width} px`;
        // A view's body draws rows of its own shape - a table under Verification, a chart under
        // Plan - so the page is waited for until it has stopped reading, which is what says it
        // has drawn everything it is going to. A view whose part has not filled its slot draws
        // no tree, so only the Spec view is read for one.
        const traffic = watchApiRequests(page);
        await gotoIssue(page, presetPath(seeded.key, preset));
        if (preset === "spec") await expectIssuePageDrawn(page);
        await expectLoadSettled(traffic);
        expect(await contentsPane(page).count(), `a Contents pane is drawn ${where}`).toBe(0);
        if (preset !== "spec") {
          expect(await threeThings(page).count(), `the three sentences are drawn ${where}`).toBe(0);
          continue;
        }
        await expectTheThree(threeThings(page), where);
        const head = await threeThings(page).boundingBox();
        const firstRow = await nodeRows(page).first().boundingBox();
        expect(
          head?.y ?? Number.POSITIVE_INFINITY,
          `the three sentences are not the Spec view's first group ${where}`
        ).toBeLessThan(firstRow?.y ?? 0);
      }
    }

    // 390 px, no view: not above the outline, and in the sheet the Outline control opens.
    await page.setViewportSize({ height: PHONE.height, width: PHONE.width });
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    await expect
      .poll(() => nodeRows(page).count(), { message: "no row is drawn at 390 px" })
      .toBeGreaterThan(0);
    expect(await contentsPane(page).count(), "a Contents pane is drawn at 390 px").toBe(0);
    expect(
      await threeThings(page).count(),
      "the three sentences are drawn above the outline at 390 px"
    ).toBe(0);
    await outlineControl(page).click();
    await expectTheThree(
      outlineSheet(page).getByTestId("issue-three-things"),
      "in the Outline sheet at 390 px"
    );
  });

  await phase(browser, seedNoSummary, async (page, seeded) => {
    await page.setViewportSize({ height: DESKTOP.height, width: DESKTOP.width });
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    // The line waits on the spec's blocks read, which says whether the spec holds a summary
    // node at all: until it answers the head says nothing about one
    // (`web/src/features/issue-page/IssueThreeThings.tsx`), so the reading waits for the line.
    await expect(
      threeThings(page),
      "the head says something other than the page's own line for a spec with no summary"
    ).toContainText(EMPTY_STATE_LINES.spec_summary);
    expect(
      await threeThings(page).locator("li").count(),
      "a spec with no summary draws sentences all the same"
    ).toBe(0);
  });
});

// c-tree, the places that draw one line for a node (the architect's ruling 1): a Contents entry
// is the node's first line by R1's rule, cut at a word, never cut again mid-word by its box, and
// a line that is a whole sentence carries no ellipsis after its own full stop. The issue itself
// is no entry of the list (ruling 5). The crumb half of the checkpoint's row is part I's.
test("a Contents entry draws a node's first line, cut at a word", async ({ browser }) => {
  test.setTimeout(120_000);
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    const words = new Map<string, string>([
      ...(await listComments(seeded.key)).map((one) => [one.id, one.body] as const),
      ...(await listIssueAsks(seeded.key)).map((one) => [one.id, one.question] as const),
    ]);
    await page.setViewportSize({ height: DESKTOP.height, width: DESKTOP.width });
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    const entries = contentsEntries(contentsPane(page));
    await expect
      .poll(() => entries.count(), { message: "the list holds no entry" })
      .toBeGreaterThan(0);
    const drawn = await entries.evaluateAll((rows) =>
      rows.map((row) => ({
        id: row.getAttribute("data-contents-entry") ?? "",
        line: (row.querySelector("[data-contents-line]")?.textContent ?? "").trim(),
      }))
    );
    // A line that is a whole sentence carries no ellipsis after its own full stop: "Thread 1
    // opens here.…" read as four dots.
    expect(
      drawn.filter((entry) => /[.!?]…$/.test(entry.line)).map((entry) => entry.line),
      "an entry ends a sentence and then an ellipsis"
    ).toEqual([]);
    expect(
      drawn.map((entry) => entry.id),
      "the issue itself is an entry of the list, which repeats the header"
    ).not.toContain(seeded.key);
    const thread = drawn.find((entry) => entry.id === seeded.comments.threads[0]);
    expect(thread, "the reader's own thread is no entry of the list").toBeDefined();
    for (const entry of drawn) {
      const whole = words.get(entry.id);
      if (whole === undefined) continue;
      const first = firstLineOf(whole).line;
      const cut = entry.line.endsWith("…") ? entry.line.slice(0, -1) : entry.line;
      expect(first.startsWith(cut), `the entry ${entry.id} draws other than its first line`).toBe(
        true
      );
      expect(
        cut.length === first.length || first.charAt(cut.length) === " ",
        `the entry ${entry.id} is cut inside a word: "${entry.line}"`
      ).toBe(true);
    }
  });
});

// c-typeface, the typeface arriving late. The Contents list cuts each entry's first line at a word
// to the room its box leaves it, measuring the words on a canvas in the face the list is drawn in
// (`one-line.ts`), and `font-display: swap` (`styles.css`) draws the page's first frames in the
// fallback while the typeface's file is on its way. This question is made of words the two faces
// draw at very different widths - at 14 px a "1" is 5 px in Inter and 8 px in the DejaVu fallback
// the browser suite's machines draw - so at the pane's width and at the sheet's the fallback's cut
// holds two words or more fewer than the typeface's: an entry not cut again once the file arrives
// leaves out words its box has room for.
const FACE_MOVING_QUESTION =
  "Roll 1.1.11, 1.11.1, 11.1.1, 1.1.1, 11.11, 1.111, 111.1, 11.1.11 and 1.11.11 to the hosts first?";

test("a Contents entry is cut again in the typeface once its file arrives @phone", async ({
  browser,
}) => {
  await phase(browser, seedNoChildren, async (page, seeded) => {
    const question = await createAsk(seeded.key, { question: FACE_MOVING_QUESTION });
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    const address = await typefaceAddress(page);
    if (address === null) {
      throw new Error(`the page's stylesheet declares no @font-face for ${TYPEFACE}`);
    }

    // A reader whose request for the file is held until the list is drawn in the fallback: the
    // pane at 1440 px, and on a phone the Outline sheet, opened before the file arrives.
    const phone = test.info().project.name === "iphone";
    const reader = await asUser(browser, "alice");
    const arrives = Promise.withResolvers<void>();
    try {
      await withIssuePage(reader, "alice");
      let held = 0;
      await reader.route(address, async (route) => {
        held += 1;
        await arrives.promise;
        await route.continue();
      });
      const late = await reader.newPage();
      await late.setViewportSize(phone ? IPHONE_13 : DESKTOP);
      // `commit`, because the page's load event waits on the held file.
      await late.goto(`/issues/${seeded.key}`, { waitUntil: "commit" });
      await expectIssuePageDrawn(late);
      if (phone) await outlineControl(late).click();
      const entry = (phone ? outlineSheet(late) : contentsPane(late)).locator(
        `[data-contents-entry="${question.id}"]`
      );
      await expect(entry, "the Contents list does not list the question").toBeVisible();
      await expect
        .poll(() => typefaceStatuses(late), { message: `the ${TYPEFACE} file is not held` })
        .toEqual(["loading"]);
      expect(held, "no request for the typeface reached the hold").toBeGreaterThan(0);
      await expect
        .poll(() => contentsLineFaults(entry, FACE_MOVING_QUESTION), {
          message: "the entry's line is not cut to its box in the fallback",
        })
        .toEqual([]);
      const inFallback = await entry.locator("[data-contents-line]").textContent();

      arrives.resolve();
      await expect
        .poll(() => typefaceStatuses(late), {
          message: `the ${TYPEFACE} face did not load once its file was let through`,
        })
        .toEqual(["loaded"]);
      await expect
        .poll(() => contentsLineFaults(entry, FACE_MOVING_QUESTION), {
          message: `the entry's line is not cut again in ${TYPEFACE} once its file arrived`,
        })
        .toEqual([]);
      expect(
        await entry.locator("[data-contents-line]").textContent(),
        "the typeface moved no word of the cut, so this row read nothing"
      ).not.toBe(inFallback);
    } finally {
      arrives.resolve();
      await reader.close();
    }
  });
});

// R2 (part J), from the Contents clause of the checkpoint's `one panel of properties holds no
// conversation list @phone`: each question and each of the reader's own threads is an entry, a
// press or Enter goes to its node in the outline, the entry the reader is in is marked, and Focus
// branch draws that branch alone, then the whole outline again. Below 1024 px the list is in the
// Outline sheet, which a choice closes.
test("a Contents entry goes to its node, and Focus branch draws that branch alone @phone", async ({
  browser,
}) => {
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    const specSlug = (await getArtifact(seeded.specArtifactId)).slug;
    const question = blockNodeId(specSlug, seeded.openQuestion.blockId);

    await page.setViewportSize({ height: DESKTOP.height, width: DESKTOP.width });
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    await expect.poll(() => nodeRows(page).count()).toBeGreaterThan(0);
    const firstRow = (await drawnRows(page)).find((row) => row.visible)?.id;
    expect(firstRow, "the outline draws no first row").toBeDefined();
    const entry = contentsPane(page).locator(`[data-contents-entry="${question}"]`);
    await expect(entry, "the Contents pane does not list the open question").toHaveCount(1);
    await nodeRows(page).first().focus();
    await entry.focus();
    await page.keyboard.press("Enter");
    await expect
      .poll(() => focusedNodeId(page), { message: "Enter on an entry went to no node" })
      .toBe(question);
    await expect(entry, "the entry the reader is in is not marked").toHaveAttribute(
      "aria-selected",
      "true"
    );
    await nodeRows(page).first().focus();
    await entry.click();
    await expect
      .poll(() => focusedNodeId(page), { message: "a press on an entry went to no node" })
      .toBe(question);
    const focusBranch = focusBranchControl(contentsPane(page));
    await focusBranch.click();
    await expect(focusBranch).toHaveAttribute("aria-pressed", "true");
    await expect
      .poll(async () => (await drawnRows(page)).find((row) => row.visible)?.id, {
        message: "Focus branch did not draw the entry's branch alone",
      })
      .toBe(question);
    await focusBranch.click();
    await expect(focusBranch).toHaveAttribute("aria-pressed", "false");
    await expect
      .poll(async () => (await drawnRows(page)).find((row) => row.visible)?.id, {
        message: "Focus branch turned off did not draw the whole outline again",
      })
      .toBe(firstRow);

    // 390 px: the list is in the Outline sheet, and a choice there goes to the node and closes
    // the sheet (`useContentsChosen`).
    await page.setViewportSize({ height: PHONE.height, width: PHONE.width });
    await gotoIssue(page, `/issues/${seeded.key}`);
    await expectIssuePageDrawn(page);
    await outlineControl(page).click();
    const chosen = outlineSheet(page).locator(`[data-contents-entry="${question}"]`);
    await expect(chosen, "the Outline sheet does not list the open question").toHaveCount(1);
    await chosen.click();
    await expect(outlineSheet(page), "a choice in the Outline sheet left it open").toHaveCount(0);
    await expect
      .poll(() => focusedNodeId(page), {
        message: "a choice in the Outline sheet went to no node",
      })
      .toBe(question);
  });
});

// c-home. The Spec view's body: the decision beside the statements it shaped, in the words it
// came from, and one line on each statement's own row (R5), drawn with the focus elsewhere and
// the pointer off the page; the tester's F7 is read here too - the line is a link to the
// decision, and `w` moves the focus to it - and F20: no text of the view is a raw address or a
// fence. Partial on today's data: a statement carries no `from` (dispatch://INFRA-1098 item 21),
// so no statement hangs under an answered ask and the body draws one group with no heading; a
// criterion is dispatch://INFRA-1253's; an agent decision's level of uncertainty and its reason
// are dispatch://INFRA-1150's; and the versions of a view's query are dispatch://INFRA-1253 R8,
// so this row reads the live Spec view.
test("a spec node shows the decisions that shaped it at every version", async ({ browser }) => {
  test.setTimeout(150_000);
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    const specSlug = (await getArtifact(seeded.specArtifactId)).slug;
    const statement = (index: number) => blockNodeId(specSlug, requirementBlockId(index));
    const decisionsSlug = (await getArtifact(seeded.decisionsArtifactId)).slug;
    const decisionNode = (index: number) =>
      blockNodeId(decisionsSlug, seeded.decisions[index] ?? "");
    const stored = (await getIssueDecisions(seeded.key)).decisions ?? [];
    expect(
      stored.some((one) => one.kind === "person"),
      "the seed holds no decision the server reads as a person's"
    ).toBe(true);
    const answered = await getAsk(seeded.answeredAskId);
    const answer = answered.ask.answer?.text ?? "";
    expect(answer, "the seeded ask holds no answer to quote").not.toBe("");
    const quote = firstLineOf(answer).line;

    await openPreset(page, seeded.key, "spec");
    await expectIssuePageDrawn(page);
    await expect
      .poll(async () => (await decidedByCells(page)).filter((cell) => cell.said !== null).length, {
        message: "the Spec view draws no decision beside its statements",
      })
      .toBeGreaterThan(0);
    // Nothing is focused and nothing is hovered: the lines are drawn by the page itself.
    await focusThePage(page);

    // The person's decision draws the three statements its ties point to, once, under one
    // cell, in the words it came from: the person's own answer to its first sentence end, the
    // question it answers, who answered, and a link to the whole answer.
    const cells = await decidedByCells(page);
    const mine = cells.filter((cell) => cell.said === "you");
    expect(mine, "the person's decision is not drawn once").toHaveLength(1);
    expect(
      mine[0]?.ids,
      "the person's decision does not stand beside the three statements it shaped"
    ).toEqual([statement(3), statement(4), statement(5)]);
    expect(mine[0]?.text).toContain(SPEC_WORDS.said_you);
    expect(mine[0]?.text).toContain(`“${quote}”`);
    expect(mine[0]?.text).toContain(answered.ask.question);
    expect(mine[0]?.text).toContain("alice");
    await expect(
      issuePage(page).locator('[data-spec-cell][data-said="you"] [data-spec-whole]'),
      "the cell carries no link to the whole answer"
    ).toHaveAttribute("href", `/issues/${seeded.key}/asks/${seeded.answeredAskId}`);

    // The agent's decision stands beside the two it shaped, marked as an agent's, in its own
    // words.
    const agents = cells.filter(
      (cell) => cell.said === "agent" && cell.text.includes("Chosen so the page makes no request")
    );
    expect(
      agents.flatMap((cell) => cell.ids).sort(),
      "the agent's decision does not stand beside the two statements it shaped"
    ).toEqual([statement(1), statement(2)].sort());
    expect(agents[0]?.text).toContain(SPEC_WORDS.said_agent);

    // One line on each statement's own row, with the same words, who decided and when; a
    // statement a second decision shaped names it after the first.
    const lines = await becauseLines(page);
    for (const index of [3, 4, 5]) {
      expect(
        lines[statement(index)],
        `statement ${index} carries no line for its decision`
      ).toContain(`${SPEC_WORDS.because} “${quote}”`);
      expect(lines[statement(index)]).toContain("alice");
    }
    expect(lines[statement(4)], "the second decision that shaped it is not named").toContain(
      SPEC_WORDS.informed_by
    );
    expect(lines[statement(1)], "the agent's line is drawn on the wrong statement").toContain(
      "Chosen so the page makes no request per node."
    );
    // A replaced decision draws no line: the statement it shaped stands with none, and its
    // cell says so, as does the cell of a statement no decision shaped (R35).
    expect(lines[statement(6)] ?? "", "the replaced decision draws a line").toBe("");
    expect(lines[statement(7)] ?? "", "a statement no decision shaped carries a line").toBe("");
    const unshaped = cells.find((cell) => cell.ids.includes(statement(6)));
    expect(
      unshaped?.said,
      "a statement only a replaced decision shaped names a decision"
    ).toBeNull();
    expect([
      EMPTY_STATE_LINES.spec_no_decision_one,
      EMPTY_STATE_LINES.spec_no_decision_many,
    ]).toContain(unshaped?.text);

    // Every statement of the query is a row of this body, and no decision and no issue is.
    const rows = await drawnRows(page);
    for (const requirement of seeded.requirements) {
      expect(
        rowOf(rows, blockNodeId(specSlug, requirement)),
        `the Spec view does not draw ${requirement}`
      ).toBeDefined();
    }
    expect(rowOf(rows, decisionNode(0)), "the Spec view draws a decision as a row").toBeUndefined();
    expect(rowOf(rows, seeded.key), "the Spec view draws the issue as a row").toBeUndefined();

    // Nothing opens on hover: the pointer crosses every cell and the body's text is the same.
    const body = issuePage(page).locator("[data-spec-body]");
    const before = await body.innerText();
    for (const cell of await issuePage(page).locator("[data-spec-cell]").all()) await cell.hover();
    expect(await body.innerText(), "the body changed under the pointer").toBe(before);
    // F20: a reference is drawn by its name, never as its `dispatch://` address or a `:::`
    // fence of the stored source.
    expect(/dispatch:\/\/|:::/.test(before), "the Spec view draws a raw reference").toBe(false);

    // F7: with the focus on a statement, its line is the link to the decision that shaped it,
    // and `w` moves the focus to that decision, which then draws the whole answer.
    const shaped = nodeRow(page, statement(3)).first();
    await shaped.focus();
    await expect(
      shaped.locator("[data-spec-because] a[data-spec-because-link]"),
      "the focused statement does not link the decision that shaped it"
    ).toHaveAttribute("href", `/issues/${seeded.key}/asks/${seeded.answeredAskId}`);
    await page.keyboard.press("w");
    await expect
      .poll(
        () =>
          page.evaluate(() => ({
            cell: document.activeElement?.hasAttribute("data-spec-cell") ?? false,
            said: document.activeElement?.getAttribute("data-said") ?? null,
          })),
        { message: "w did not move the focus to the decision behind the statement" }
      )
      .toEqual({ cell: true, said: "you" });
    await expect(
      issuePage(page).locator('[data-spec-cell][data-said="you"] [data-spec-whole-answer]'),
      "the focused decision does not draw the person's whole answer"
    ).toHaveText(answer, { useInnerText: true });
  });
});

// c-reflow, on words the seeded issue does not carry. R17 binds the page a reader opens, not one
// seed's text: a requirement whose words name a file path holds a run of 118 characters with no
// space in it, which is longer than the card that draws it at every width this reads. Found by
// the tester on 2026-11-09 at 39649c0f269d, where the Spec preset's requirement list draws that
// run in a span that neither wraps it nor gives it its own line, and the whole page scrolls
// sideways: 884 px of page in a 390 px viewport, and 3,644 px in a 1,440 px one for a 300
// character run.
test("a requirement whose words hold an unbroken run keeps the page inside its viewport @phone", async ({
  browser,
}) => {
  await phase(browser, seedUnbrokenWords, async (page, seeded) => {
    for (const width of [320, 390, 1440]) {
      await page.setViewportSize({ height: width < 1024 ? PHONE.height : DESKTOP.height, width });
      await gotoIssue(page, presetPath(seeded.key, "spec"));
      await expectIssuePageDrawn(page);
      // The words are on the page before it is measured, so a fit read before the list draws
      // cannot pass for the fit of a page that holds them.
      await expect(
        page.getByText(UNBROKEN_PATH, { exact: false }).first(),
        `the Spec view draws none of the requirement's words at ${width} px`
      ).toBeVisible();
      await assertPageFits(page);
    }
  });
});
