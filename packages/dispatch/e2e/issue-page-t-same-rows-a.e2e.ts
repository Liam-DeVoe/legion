import { phase } from "./issue-page";
import { fixtureSeed } from "./issue-page-t";
import {
  DECISION_SEEDS,
  differences,
  expectDecisionsFromNodes,
  firstRowOutOfPlace,
  readOutline,
  SAME_ROWS_A_SEEDS,
  SAME_ROWS_MS,
} from "./issue-page-t-rows";
import {
  convertedGate,
  decisionNodesGate,
  decisionsAreNodes,
  owedUnless,
  treeReadGate,
} from "./issue-page-t-tree";
import { expect, test } from "./test";

/**
 * Row 1 of the rows that draw the new issue page from the tree read, "the outline drawn from the
 * tree read holds the rows the reads drew", for the first nine of `SEEDS` in their order
 * (`SAME_ROWS_A_SEEDS`), each seed's decision clauses a row of its own; the last five are in
 * `e2e/issue-page-t-same-rows-b.e2e.ts`. The head of `e2e/issue-page-t-rows.ts` says what the
 * rows read, what their gates are and why the seeds split there.
 */

for (const [name, seed] of SAME_ROWS_A_SEEDS) {
  const title = `the outline drawn from the tree read holds the rows the reads drew: ${name}`;
  // c-same-rows on one seed.
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
      const recorded = fixtureSeed(name);
      const nodes = decisionsAreNodes(outline.answer, outline.decisions);
      expect(
        [
          ...differences("as the page opens", outline.opened, recorded.opened, outline, nodes),
          ...differences("with every branch open", outline.rows, recorded.rows, outline, nodes),
        ],
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
  }
}
