import {
  CONTROL_LABELS,
  issueStatusWord,
  NODE_STATE_WORDS,
  NODE_TYPE_NAMES,
} from "../web/src/features/issue-page/page-words";
import { getAsk, listComments } from "./api";
import {
  DESKTOP,
  drawnRows,
  expectRowGroups,
  expectRowLayouts,
  focusRow,
  focusThePage,
  nodeRow,
  nodeRows,
  openIssue,
  PHONE,
  PLANNER_TITLE,
  parentOfRow,
  phase,
  READER,
  type RowSeed,
  rowAct,
  rowBody,
  rowGroups,
  rowLayouts,
  rowMore,
  rowOf,
  rowWords,
  seedRows,
  unfoldEveryBranch,
} from "./issue-page-g";
import { expect, test } from "./test";
import { expectLoadSettled, watchApiRequests } from "./traffic";

/**
 * Part G of dispatch://INFRA-1196, "A node's row" (dispatch://INFRA-3947): one node of an issue
 * drawn as one row of the approved design, in the new page's body, which draws the
 * conversation's rows with every branch unfolded (`ConversationRows.tsx`) until part I's outline
 * replaces it. Each row is one `check` block of dispatch://INFRA-3947/artifact/verification-md,
 * named by its `-g` string, and reads the page through `e2e/issue-page-g.ts`; every row unfolds
 * each folded branch before it reads, so it holds on part I's outline too.
 *
 * The rows are on the real stack: the page draws what the server stored and indexed - a
 * document's blocks, the decisions read, the batched names - and the new page is the reader's
 * own preference (`withIssuePage`).
 */

/** An address, the short form of one, or a slug, which no chip, author or tag draws (R4). */
function addressesOf(seeded: RowSeed): RegExp {
  return new RegExp(`dispatch://|${seeded.key} \\S+( block)?$|-md\\b|session:`, "u");
}

// c-row-layout: every row at both widths, read off the page's own layout (R1, R4, R5, R8).
test("a node draws its kind at the right of its row and every node starts at one left edge @phone", async ({
  browser,
}) => {
  test.setTimeout(180_000);
  await phase(browser, seedRows, async (page, seeded) => {
    for (const size of [DESKTOP, PHONE]) {
      await page.setViewportSize(size);
      const settling = watchApiRequests(page);
      await openIssue(page, seeded.key);
      await expectLoadSettled(settling);
      const where = `the conversation at ${size.width} px`;
      if (size.width === PHONE.width) {
        // The load takes no focus, so it scrolls nothing (R8).
        expect(await page.evaluate(() => window.scrollY), `${where} scrolled at load`).toBe(0);
      }
      await unfoldEveryBranch(page);
      await expect
        .poll(() => nodeRows(page).count(), { message: `${where} draws no row` })
        .toBeGreaterThan(0);
      expectRowLayouts(await rowLayouts(page), where, true, size.width);
      expectRowGroups(await rowGroups(page), where, size.width, addressesOf(seeded));
      // R17 of the root, F17: the rows name what their words cite from the one batched read.
      expect(
        settling.requests
          .map((request) => request.path)
          .filter(
            (path) =>
              path.includes(`/issues/${seeded.child}/summary`) || path.includes("/issues/UTF-16")
          ),
        `${where}: a row reads a target of its own for a reference its words cite`
      ).toEqual([]);
    }

    await page.setViewportSize(DESKTOP);
    await openIssue(page, seeded.key);
    await unfoldEveryBranch(page);
    const rows = await drawnRows(page);
    // No row for the issue, which the header names (R8).
    expect(
      rows.filter((row) => row.type === "issue"),
      "the body draws the issue's own row"
    ).toEqual([]);
    // A one-line statement with no state is 29 px tall at 1440 px (R1).
    const oneLine = (await rowLayouts(page)).find(
      (row) => row.id === `${seeded.slugs.spec}#b-r-one`
    );
    expect(
      oneLine?.height ?? 1e6,
      "the one-line statement is taller than 29 px"
    ).toBeLessThanOrEqual(29);

    // A child: its status, what blocks it and who holds it, and no author (R5).
    const child = nodeRow(page, seeded.child).first();
    const facts = child.locator("[data-child-facts]");
    await expect(facts).toContainText(issueStatusWord("todo"));
    await expect(facts).toContainText(seeded.blocker);
    await expect(
      facts.locator("[data-child-holder]"),
      "the child's holder is drawn in a link"
    ).not.toContainText("session:");
    await expect(
      facts.locator("[data-child-holder] a"),
      "the child's holder is a link"
    ).toHaveCount(0);
    await expect(child.locator("[data-node-margin] [data-node-author]")).toHaveCount(0);

    // `r-cites`: the document it cites by the page's word, the child it cites by its key, among
    // its bold words and its list (R4, F14).
    const cites = nodeRow(page, `${seeded.slugs.spec}#b-r-cites`).first();
    await expect(cites.locator("a[data-row-chip]").first()).toHaveText(NODE_TYPE_NAMES.document);
    await expect(rowWords(cites)).not.toContainText("notes.md");
    await expect(cites.locator(`a[data-row-reference="dispatch://${seeded.child}"]`)).toHaveText(
      new RegExp(`^${seeded.child}\\b`, "u")
    );
    await expect(rowWords(cites).locator("strong")).toHaveCount(1);
    await expect(rowWords(cites).locator("li")).toHaveCount(2);

    // The needs chip names its target by its first words, with no mark of markdown (F18).
    const needs = rowOf(rows, `${seeded.slugs.plan}#b-t2`);
    expect(needs?.chips.join(" ") ?? "", "the needs chip draws a mark of markdown").not.toMatch(
      /[*\\]/u
    );

    // The decision that quotes a typed block draws none of its source, and its reference is a
    // chip in place (F20).
    const raw = rows.find((row) => row.type === "decision" && row.text.includes("raw source"));
    expect(raw?.text ?? ":::", "the decision draws a typed block's source").not.toContain(":::");
    expect(raw?.text ?? "dispatch://", "the decision draws an address").not.toContain(
      "dispatch://"
    );

    // An agent's link and a word that only looks like a key (R4).
    const linked = nodeRow(page, seeded.linked).first();
    await expect(linked.locator("a", { hasText: "UTF-16" })).toHaveCount(0);
    await expect(rowWords(linked)).toContainText("in UTF-16.");

    // A picture is 84 px tall, and a press opens it whole (R4).
    const picture = nodeRow(page, seeded.pictured).first().locator("[data-node-words] img");
    await expect(picture).toBeVisible();
    expect(
      Math.round((await picture.boundingBox())?.height ?? 0),
      "the picture is not 84 px tall"
    ).toBe(84);
    await picture.click();
    await expect(
      page.locator("dialog[open] img"),
      "a press does not open the picture whole"
    ).toBeVisible();
    await page.keyboard.press("Escape");

    // A fold control folds its branch to one line with its count, and a second press unfolds it.
    const parent = nodeRows(page)
      .filter({ has: page.locator(':scope[aria-expanded="true"]') })
      .first();
    await parent.locator("button[data-node-bullet]").click();
    await expect(parent).toHaveAttribute("aria-expanded", "false");
    await expect(parent.locator("[data-node-count]")).toHaveCount(1);
    await parent.locator("button[data-node-bullet]").click();
    await expect(parent).toHaveAttribute("aria-expanded", "true");

    // At 390 px a focused comment's Reply is under 44 px (R1).
    await page.setViewportSize(PHONE);
    const comment = await focusRow(page, seeded.comment);
    expect((await rowAct(comment, "reply").boundingBox())?.height ?? 1e6).toBeLessThan(44);
  });
});

// c-cut: ruling 1 on R2 at 1440 px.
test("an agent message and an answered question are cut at six lines, and the words of the reader and a waiting question are whole", async ({
  browser,
}) => {
  test.setTimeout(120_000);
  await phase(browser, seedRows, async (page, seeded) => {
    await openIssue(page, seeded.key);
    await unfoldEveryBranch(page);
    const agent = nodeRow(page, seeded.longMessage).first();
    const cut = (await rowLayouts(page)).find((row) => row.id === seeded.longMessage);
    expect(cut?.more ?? "", "the agent's long message draws no more").toMatch(
      /^more · [\d,]+ words$/u
    );
    expect(
      Number(/([\d,]+)/u.exec(cut?.more ?? "")?.[1]?.replace(",", "")),
      "the cut holds back 20 words or fewer"
    ).toBeGreaterThan(20);
    expect((await rowWords(agent).boundingBox())?.height ?? 1e6).toBeLessThanOrEqual(6 * 24 + 12);
    await rowMore(agent).click();
    await focusThePage(page);
    await expect
      .poll(async () => (await rowLayouts(page)).find((row) => row.id === seeded.longMessage))
      .toMatchObject({ more: CONTROL_LABELS.read_less, wordsHidden: 0 });

    const layouts = await rowLayouts(page);
    expect(
      layouts.find((row) => row.id === seeded.longComment),
      "the reader's own long comment is cut"
    ).toMatchObject({
      more: null,
      own: true,
      wordsHidden: 0,
    });
    await expect(nodeRow(page, seeded.answered).first().locator("[data-answer-text]")).toHaveText(
      seeded.answerWords,
      { useInnerText: true }
    );
    expect((await getAsk(seeded.waitingLong)).ask.waiting_on_person).toBe(READER);
    expect(
      layouts.find((row) => row.id === seeded.waitingLong),
      "the waiting question is cut"
    ).toMatchObject({
      more: null,
      wordsHidden: 0,
    });
    expect(layouts.find((row) => row.id === seeded.answeredLong)?.more ?? "").toMatch(/^more · /u);

    // Focused, the answered question is whole, with no more, on the focused card.
    const answered = await focusRow(page, seeded.answeredLong);
    await expect(rowMore(answered)).toHaveCount(0);
    await expect(answered.locator("[data-node-card]").first()).toHaveCSS(
      "background-color",
      "rgb(250, 250, 249)"
    );

    // A statement with no author says so in its body, not on its row (R3).
    const statement = await focusRow(page, `${seeded.slugs.spec}#b-r-one`);
    await expect(rowBody(statement).locator("[data-node-author]")).toHaveText(
      CONTROL_LABELS.unknown_author
    );
    await expect(statement.locator("[data-node-margin] [data-node-author]")).toHaveCount(0);

    // Keep open keeps a body when the focus moves on; a second press lets it close (R6).
    await rowBody(statement).getByRole("button", { name: CONTROL_LABELS.keep_open }).click();
    await focusRow(page, seeded.comment);
    await expect(rowBody(statement)).toHaveCount(1);
    await focusRow(page, `${seeded.slugs.spec}#b-r-one`);
    await rowBody(statement).getByRole("button", { name: CONTROL_LABELS.let_close }).click();
    await focusRow(page, seeded.comment);
    await expect(rowBody(statement)).toHaveCount(0);

    // No element of a row carries a `title` (R6).
    expect(await nodeRows(page).locator("[title]").count()).toBe(0);
  });
});

// c-author: R3 at 1440 px with every branch unfolded, and at 390 px.
test("an author is drawn by name and shape, and an agent's decision by its uncertainty @phone", async ({
  browser,
}) => {
  test.setTimeout(120_000);
  await phase(browser, seedRows, async (page, seeded) => {
    for (const size of [DESKTOP, PHONE]) {
      await page.setViewportSize(size);
      await openIssue(page, seeded.key);
      await unfoldEveryBranch(page);
      const rows = await drawnRows(page);
      const where = `at ${size.width} px`;
      // A person by their login, an agent by its role's word, and neither by an id.
      expect(rowOf(rows, seeded.comment)?.author, `the reader's comment ${where}`).toBe(READER);
      expect(rowOf(rows, seeded.linked)?.author, `the planner's message ${where}`).toBe("Planner");
      for (const row of rows) {
        expect(row.author ?? "", `the ${row.type} ${row.id} ${where} draws an id`).not.toMatch(
          /session:|019a1c0a/u
        );
      }
      const shape = (id: string) => rowOf(rows, id)?.authorShape ?? null;
      expect(shape(seeded.comment), "people and agents share a shape").not.toBe(
        shape(seeded.linked)
      );
      // The answer is word for word, with no "Unsure"; only the seeded decision says it.
      expect(rowOf(rows, seeded.answered)?.unsure ?? null).toBeNull();
      expect(rows.filter((row) => row.unsure !== null).map((row) => row.id)).toEqual([
        seeded.unsure,
      ]);
      expect(rowOf(rows, seeded.unsure)?.unsure).toBe(CONTROL_LABELS.unsure);
    }
    await page.setViewportSize(DESKTOP);
    // The planner's open row gives its whole title; a node with no author says so in a third
    // shape.
    const planner = await focusRow(page, seeded.linked);
    await expect(rowBody(planner).locator("[data-node-author-title]")).toHaveText(PLANNER_TITLE);
    const statement = await focusRow(page, `${seeded.slugs.spec}#b-r-one`);
    const unknown = await rowBody(statement)
      .locator("[data-node-author] svg[data-glyph]")
      .getAttribute("data-glyph");
    const rows = await drawnRows(page);
    expect(
      new Set([
        rowOf(rows, seeded.comment)?.authorShape,
        rowOf(rows, seeded.linked)?.authorShape,
        unknown,
      ]).size,
      "a person, an agent and an author nobody recorded do not each have a shape of their own"
    ).toBe(3);
  });
});

// c-writer: R7 and R10 at 1440 px.
test("a row's Reply and Answer set the page's writer, and a decision asked to change draws it", async ({
  browser,
}) => {
  test.setTimeout(120_000);
  await phase(browser, seedRows, async (page, seeded) => {
    await openIssue(page, seeded.key);
    await unfoldEveryBranch(page);
    const writes = watchApiRequests(page);

    const comment = await focusRow(page, seeded.comment);
    await expect(rowAct(comment, "reply")).toHaveCount(1);
    await expect(rowAct(comment, "answer")).toHaveCount(0);
    await rowAct(comment, "reply").click();
    await expect(comment).toBeFocused();

    const question = await focusRow(page, seeded.openQuestion);
    await expect(rowAct(question, "answer")).toHaveCount(1);
    await expect(rowAct(question, "reply")).toHaveCount(1);
    const option = question.locator('[data-pick-option="1024 px"]');
    await option.click();
    await expect(option).toHaveAttribute("aria-pressed", "true");
    await expect(option).toContainText("●");
    await expect(question.locator('[data-pick-option="1280 px"]')).toContainText("○");
    await rowAct(question, "answer").click();
    await expect(question).toBeFocused();
    await expect(page.locator('[data-testid="issue-composer"]')).toHaveCount(0);

    // A statement and a check take no reply in today's model, so they draw no act.
    for (const id of [`${seeded.slugs.spec}#b-r-one`, `${seeded.slugs.verification}#b-c1`]) {
      const typed = await focusRow(page, id);
      await expect(typed.locator("[data-row-act]")).toHaveCount(0);
    }

    // The decision that shaped a statement waits for review: Keep and Change.
    const shaping = await focusRow(page, seeded.shaping);
    await expect(shaping.locator("button[data-row-action]")).toHaveCount(2);

    // The decision a person asked to change says so, holds the change under it, and draws no
    // Keep and no Change (R10, F6).
    const changed = rowOf(await drawnRows(page), seeded.changed);
    expect(changed).toMatchObject({
      state: "change_requested",
      stateWord: NODE_STATE_WORDS.decision.change_requested,
    });
    expect(
      (await listComments(seeded.key)).some((one) => one.id === seeded.change),
      "the server holds no comment for the change"
    ).toBe(true);
    expect(
      await parentOfRow(page, seeded.change),
      "the change is not drawn under its decision"
    ).toBe(seeded.changed);
    const outlined = await focusRow(page, seeded.changed);
    await expect(outlined.locator("button[data-row-action]")).toHaveCount(0);

    // Nothing was sent: no box exists until part L draws it. A thread's read mark is the page's
    // own record of what the reader read (`outline-thread-read.tsx`), and no write of theirs.
    expect(
      writes.requests.filter(
        (request) =>
          ["PATCH", "POST", "PUT"].includes(request.method) &&
          !request.path.includes("/me/threads/")
      ),
      "a press on a row's act wrote something"
    ).toEqual([]);
  });
});
