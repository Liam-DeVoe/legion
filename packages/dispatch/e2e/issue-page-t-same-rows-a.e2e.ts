import { phase } from "./issue-page";
import { fixtureSeed } from "./issue-page-t";
import {
  BESIDE_STAMPED_TITLE,
  DECISION_SEEDS,
  expectDecisionsFromNodes,
  firstRowOutOfPlace,
  outlineDifferences,
  readOutline,
  SAME_ROWS_A_SEEDS,
  SAME_ROWS_MS,
  STAMPED_TITLE,
} from "./issue-page-t-rows";
import { convertedGate, decisionNodesGate, owedUnless, treeReadGate } from "./issue-page-t-tree";
import { sessionTitleGate } from "./issue-page-u";
import { expect, test } from "./test";

/**
 * Row 1 of the rows that draw the new issue page from the tree read, "the outline drawn from the
 * tree read holds the rows the reads drew", for the first nine of `SEEDS` in their order
 * (`SAME_ROWS_A_SEEDS`), each seed's decision clauses and its author of an agent's decision rows
 * of their own; the last five are in `e2e/issue-page-t-same-rows-b.e2e.ts`. The head of
 * `e2e/issue-page-t-rows.ts` says what the rows read, what their gates are and why the seeds
 * split there.
 */

for (const [name, seed] of SAME_ROWS_A_SEEDS) {
  const title = `the outline drawn from the tree read holds the rows the reads drew: ${name}`;
  // c-same-rows on one seed, but the author of an agent's decision, which is the `@owed` row's
  // below (`ReadingsPart`).
  // SLOW(>=2s): a whole seeded issue, read as it opens and again with every branch opened and
  // every row's body read in turn, each reading taken twice until it holds still.
  test(title, async ({ browser }) => {
    test.setTimeout(SAME_ROWS_MS);
    await phase(browser, seed, async (page, seeded) => {
      owedUnless(await treeReadGate(page, seeded.key));
      owedUnless(await convertedGate(page, seeded.key));
      const outline = await readOutline(page, seeded);
      expect(
        firstRowOutOfPlace("as the page opens", outline.opened, outline) ??
          firstRowOutOfPlace("with every branch open", outline.rows, outline),
        "each row stands under the parent the tree read answers"
      ).toBeUndefined();
      expect(
        outlineDifferences(outline, name, BESIDE_STAMPED_TITLE).found,
        "each row reads as the reads drew it"
      ).toEqual([]);
    });
  });

  if (DECISION_SEEDS.has(name)) {
    // c-same-rows' decision clauses on one seed, owed on `decisionNodesGate`.
    // SLOW(>=2s): a whole seeded issue, read as it opens and again with every branch opened,
    // each reading taken twice until it holds still.
    test(`${title}, its decisions as nodes of the tree read @owed`, async ({ browser }) => {
      test.setTimeout(SAME_ROWS_MS);
      await phase(browser, seed, async (page, seeded) => {
        owedUnless(await treeReadGate(page, seeded.key));
        owedUnless(await convertedGate(page, seeded.key));
        const outline = await readOutline(page, seeded, { bodies: false });
        owedUnless(decisionNodesGate(outline.answer, outline.decisions));
        expectDecisionsFromNodes(outline, fixtureSeed(name));
      });
    });

    // c-same-rows' author of an agent's decision on one seed, owed on `sessionTitleGate`: the
    // title its session stamped, on its row's line and in its body, as the fixture recorded it.
    // SLOW(>=2s): a whole seeded issue, read as it opens and again with every branch opened and
    // every row's body read in turn, each reading taken twice until it holds still.
    test(`${title}, an agent's decision by the title its session stamped @owed`, async ({
      browser,
    }) => {
      test.setTimeout(SAME_ROWS_MS);
      await phase(browser, seed, async (page, seeded) => {
        owedUnless(await treeReadGate(page, seeded.key));
        owedUnless(await convertedGate(page, seeded.key));
        owedUnless(await sessionTitleGate(seeded.key));
        const outline = await readOutline(page, seeded);
        const { compared, found } = outlineDifferences(outline, name, STAMPED_TITLE);
        expect(
          compared,
          `no row the page draws of ${name} is an agent's decision the fixture holds, so the ` +
            "row would compare nothing"
        ).toBeGreaterThan(0);
        expect(found, "each agent's decision draws the title its session stamped").toEqual([]);
      });
    });
  }
}
