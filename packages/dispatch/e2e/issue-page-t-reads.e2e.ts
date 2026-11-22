import type { DisplayTree, IssueTreeNode } from "../web/src/api/types";
import { PRESET_NAMES } from "../web/src/features/issue-page/page-words";
import { readingOf, treeIdOf } from "../web/src/features/issue-page/tree-read/tree-nodes";
import { wordsOf } from "../web/src/features/refs/tree-fragment";
import { seedWholeIssue } from "./issue-layout-seed";
import { closeDetails, openDetails, phase } from "./issue-page";
import { nodeRow } from "./issue-page-g";
import { fixtureSeed, seedKeys } from "./issue-page-t";
import {
  checksOutOfPlace,
  firstLoad,
  idReadsOf,
  idSet,
  idsOfType,
  pastStaleTime,
  pressFilter,
  ROW_MS,
  releaseFilter,
  unexplainedDifferences,
  VIEW_FILTERS,
  VIEW_SEEDS,
} from "./issue-page-t-rows";
import {
  besideViewRead,
  blocksReads,
  decisionRowId,
  decisionsReadOf,
  decisionsViewGate,
  headerStatus,
  heldAnswer,
  linksTo,
  owedUnless,
  partUGate,
  R2_COUNTED_KINDS,
  type TreeAnswerWatch,
  treeAnswerOf,
  treeReadGate,
  treeReads,
  viewAnswerOf,
  viewCount,
  viewNodeIds,
} from "./issue-page-t-tree";
import { expect, test } from "./test";
import { expectLoadSettled, watchApiRequests } from "./traffic";

/**
 * Rows 2 and 3 of the rows that draw the new issue page from the tree read, each clause of theirs
 * a later gate holds a row of its own: what the first load and each filter send of the tree read,
 * and each filter's rows as its view read's. The head of `e2e/issue-page-t-rows.ts` says what the
 * rows read and what their gates are.
 */

// ---------------------------------------------------------------------------------------------
// Row 2: what the first load and each filter send.
// ---------------------------------------------------------------------------------------------

// c-tree-requests, with the clause that the page's reads are refreshed by events, never by age:
// a slot the stage mounts again past the app's 30 s `staleTime` sends no read of its own, shown
// on Plan, whose slot reads only through `useIssueTree`.
// SLOW(>=2s): the whole seeded issue, its load waited out to a 1.5 s quiet window, Details
// opened, and three filters each pressed on, off and on again with each press waited out.
test("the first load reads the tree once and a filter its view once", async ({ browser }) => {
  test.setTimeout(ROW_MS);
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    owedUnless(await treeReadGate(page, seeded.key));
    const views = {
      plan: viewAnswerOf(page, "plan"),
      spec: viewAnswerOf(page, "spec"),
      verification: viewAnswerOf(page, "verification"),
    } satisfies Record<(typeof VIEW_FILTERS)[number], TreeAnswerWatch>;
    const traffic = watchApiRequests(page);
    await page.clock.install();
    const load = await firstLoad(page, traffic, seeded.key);
    await expect(headerStatus(page), "the page draws no header").toBeVisible();
    expect(
      treeReads(load, seeded.key, null),
      "the first load sends one tree read with no view"
    ).toBe(1);
    expect(blocksReads(load), "the first load sends no blocks read").toEqual([]);
    expect(
      load.length,
      "the first load sends no more requests under /api/ than the reads did"
    ).toBeLessThanOrEqual(fixtureSeed("seedWholeIssue").loadRequests);
    await openDetails(page);
    await expectLoadSettled(traffic);
    expect(
      blocksReads(traffic.requests),
      "the first load, with the outline, Details and the header drawn, sends no blocks read"
    ).toEqual([]);
    await closeDetails(page);

    for (const filter of VIEW_FILTERS) {
      const name = PRESET_NAMES[filter];
      const late = filter === "plan";
      if (late) await pastStaleTime(page, traffic);
      const first = traffic.requests.length;
      await pressFilter(page, traffic, filter);
      await heldAnswer(views[filter], `the ${name} view read`);
      await expectLoadSettled(traffic);
      const pressed = traffic.requests.slice(first);
      expect(treeReads(pressed, seeded.key, filter), `${name} sends one view read`).toBe(1);
      expect(blocksReads(pressed), `${name} sends no blocks read`).toEqual([]);
      if (late) {
        expect(
          besideViewRead(pressed, seeded.key, filter),
          "a view pressed after 31 s sends no read"
        ).toEqual([]);
      }
      await releaseFilter(page, filter);
      if (late) await pastStaleTime(page, traffic);
      const again = traffic.requests.length;
      await pressFilter(page, traffic, filter);
      if (late) await expectLoadSettled(traffic);
      const repressed = traffic.requests.slice(again);
      expect(
        treeReads(repressed, seeded.key, filter),
        "a filter turned on again sends no view read"
      ).toBe(0);
      expect(blocksReads(repressed), `${name} turned on again sends no blocks read`).toEqual([]);
      if (late) expect(repressed, "a view pressed after 31 s sends no read").toEqual([]);
      await releaseFilter(page, filter);
    }
  });
});

// c-tree-requests' Decisions clause, owed on the model's decisions view of the tree read
// (`decisionsViewGate`): the decisions read moves onto the tree read's `view=decisions`, which the
// load reads once whoever asks, and the Decisions filter reads no blocks at all, since decisions
// are nodes of the spec.
// SLOW(>=2s): the whole seeded issue, its load waited out to a 1.5 s quiet window, and Decisions
// pressed on and off twice with each press waited out.
test("the first load reads the tree once and a filter its view once, and the load reads the Decisions view once @owed", async ({
  browser,
}) => {
  test.setTimeout(ROW_MS);
  await phase(browser, seedWholeIssue, async (page, seeded) => {
    owedUnless(await treeReadGate(page, seeded.key));
    const traffic = watchApiRequests(page);
    const load = await firstLoad(page, traffic, seeded.key);
    owedUnless(decisionsViewGate(load, seeded.key));
    expect(
      treeReads(load, seeded.key, "decisions"),
      "the load reads the Decisions view once, whoever asks"
    ).toBe(1);
    for (let press = 0; press < 2; press += 1) {
      const from = traffic.requests.length;
      await pressFilter(page, traffic, "decisions");
      const pressed = traffic.requests.slice(from);
      expect(
        treeReads(pressed, seeded.key, "decisions"),
        "Decisions turned on sends no view read beside the load's"
      ).toBe(0);
      const blocks = blocksReads(pressed);
      expect(blocks, "Decisions reads no blocks").toEqual([]);
      await releaseFilter(page, "decisions");
    }
  });
});

// ---------------------------------------------------------------------------------------------
// Row 3: each filter's rows are its view read's.
// ---------------------------------------------------------------------------------------------

// c-views.
// SLOW(>=2s): two seeded issues, each load waited out, and three filters each pressed, read
// until it holds still, and released.
test("each filter draws its view read's rows", async ({ browser }) => {
  test.setTimeout(ROW_MS);
  for (const [name, seed] of VIEW_SEEDS) {
    await phase(browser, seed, async (page, seeded) => {
      owedUnless(await treeReadGate(page, seeded.key));
      const views = {
        plan: viewAnswerOf(page, "plan"),
        spec: viewAnswerOf(page, "spec"),
        verification: viewAnswerOf(page, "verification"),
      } satisfies Record<(typeof VIEW_FILTERS)[number], TreeAnswerWatch>;
      const traffic = watchApiRequests(page);
      await firstLoad(page, traffic, seeded.key);
      const reads = await idReadsOf(seeded.key);
      const slugs = (reads.issue.artifacts ?? []).map((artifact) => artifact.slug);
      const keys = seedKeys(seeded, slugs);
      const recorded = fixtureSeed(name);
      for (const filter of VIEW_FILTERS) {
        const view = PRESET_NAMES[filter];
        const where = `${name} under ${view}`;
        const drawn = await pressFilter(page, traffic, filter);
        const answer = await heldAnswer(views[filter], `the ${view} view read`);
        if (filter === "spec") {
          expect(idsOfType(drawn.nodes, "decision"), "no decision row under Spec").toEqual([]);
          for (const kind of ["constraint", "ask", "comment", "message", "check", "plan_task"]) {
            expect(idsOfType(drawn.nodes, kind), `no ${kind} row under Spec`).toEqual([]);
          }
          expect(idsOfType(drawn.nodes, "callout"), "no memo row under Spec").toEqual([]);
        }
        const expected = viewNodeIds(answer, filter, reads);
        expect(
          idSet(drawn.nodes.map((node) => node.id)),
          `${where}, the rows are the view read's nodes of root R2's row kinds`
        ).toEqual(idSet(expected.map((node) => node.id)));
        expect(
          idSet(drawn.nodes.flatMap((node) => (node.context ? [node.id] : []))),
          `${where}, the rows drawn as context are the view read's context`
        ).toEqual(idSet(expected.flatMap((node) => (node.role === "context" ? [node.id] : []))));
        if (filter === "verification") {
          expect(
            checksOutOfPlace(drawn.rows, answer, reads),
            `${where}, each check stands under each statement it serves`
          ).toEqual([]);
        }
        expect(
          drawn.count?.trim(),
          `${where}, the control counts the view read's own ${R2_COUNTED_KINDS[filter].join(", ")}`
        ).toBe(String(viewCount(answer, filter)));
        expect(
          unexplainedDifferences(filter, drawn.rows, keys, recorded),
          `${where}, each difference from the fixture's rows is a kind root R2 leaves out`
        ).toEqual([]);
        await releaseFilter(page, filter);
      }
    });
  }
});

/** The first line of decision node `node`, the words the quote that heads a statement's line opens
 *  with (`spec-decided-by.ts`'s `decisionOf`): those of the first block of its reading that holds
 *  any (`wordsOf`), its heading where it opens with one, read inside the one `decision` block the
 *  read sends its reading as (`readingOf`). Where the read sent no reading it is the heading the
 *  model reads off that same first block (`Decision.heading`): the node's text is no help there,
 *  since its first line is the block's own `:::decision{…}` line. */
function decisionFirstLine(node: IssueTreeNode): string {
  const content = readingOf(node)?.content;
  const blocks = Array.isArray(content) ? (content as DisplayTree[]) : [];
  const words = blocks.map((block) => wordsOf(block).replace(/\s+/g, " ").trim());
  return words.find((one) => one !== "") ?? (node.decision?.heading ?? "").trim();
}

// c-views' decision lines, owed until the page draws the decisions from the tree read's nodes,
// once decisions are nodes of the tree read on this branch's base (`partUGate`): each decision
// whose `shaped` tie names a statement under Spec is named on that statement's line
// (`[data-spec-because]` in its row), never a row of its own, as the page draws that line
// (`spec-decided-by.ts`): the first decision by a quote of its first line, each other one after
// "informed by" by its name cut short, and each a link to its block. So a decision is on the line
// where the line quotes its first line or holds a link whose last path segment and fragment are
// its `<slug>#b-<block id>`. A decision the model marks replaced (its node's `decision.replaced`)
// draws no line at all (root R5), so the row counts none.
// SLOW(>=2s): two seeded issues, each load waited out, and Spec pressed and read until it holds
// still.
test("each filter draws its view read's rows, and each decision that shaped a statement is a line on its row @owed", async ({
  browser,
}) => {
  test.setTimeout(ROW_MS);
  for (const [, seed] of VIEW_SEEDS) {
    await phase(browser, seed, async (page, seeded) => {
      owedUnless(await treeReadGate(page, seeded.key));
      const tree = treeAnswerOf(page);
      const decisions = decisionsReadOf(page, seeded.key);
      const spec = viewAnswerOf(page, "spec");
      const traffic = watchApiRequests(page);
      await firstLoad(page, traffic, seeded.key);
      owedUnless(partUGate(await heldAnswer(tree, "the tree read"), decisions.read()));
      const reads = await idReadsOf(seeded.key);
      const drawn = await pressFilter(page, traffic, "spec");
      const answer = await heldAnswer(spec, "the Spec view read");
      const idOf = treeIdOf(answer, reads);
      const nodes = new Map((answer.nodes ?? []).map((node) => [node.id, node]));
      const statements = new Set(drawn.rows.flatMap((row) => (row.id === null ? [] : [row.id])));
      const lines = (answer.tuples ?? []).flatMap((tuple) => {
        const decision = nodes.get(tuple.from.id);
        const statement = idOf(tuple.to.id);
        if (tuple.kind !== "shaped" || decision?.supertag !== "decision") return [];
        if (decision.decision?.replaced === true) return [];
        if (!statements.has(statement)) return [];
        return [{ first: decisionFirstLine(decision), row: decisionRowId(decision), statement }];
      });
      expect(
        lines.length,
        "the Spec view read holds a decision, not replaced, that shaped a statement the Spec " +
          "filter draws"
      ).toBeGreaterThan(0);
      const missing: string[] = [];
      for (const { first, row, statement } of lines) {
        // The Spec rows held still across two readings in `pressFilter` above, with the page
        // quiet, so one reading of a drawn row's line is the line that row settled on.
        const [line] = await nodeRow(page, statement)
          .first()
          .locator("[data-spec-because]")
          .evaluateAll((found) =>
            found.map((one) => ({
              links: [...one.querySelectorAll("a[href]")].map(
                (link) => link.getAttribute("href") ?? ""
              ),
              text: one.textContent ?? "",
            }))
          );
        const text = (line?.text ?? "").replace(/\s+/g, " ").trim();
        const quoted = first !== "" && text.includes(first);
        const linked = (line?.links ?? []).some((href) => linksTo(href, [row]));
        if (!quoted && !linked) {
          missing.push(
            `${statement}'s line neither quotes "${first}" nor links to ${row}: ` +
              (line === undefined ? "its row draws no line" : `it reads "${text}"`)
          );
        }
      }
      expect(
        missing,
        "each decision, not replaced, that shaped a statement is named on that statement's line"
      ).toEqual([]);
      await releaseFilter(page, "spec");
    });
  }
});

// c-views for Decisions, owed on the model's decisions view of the tree read
// (`decisionsViewGate`): once the load reads the decisions as the tree read's `view=decisions`,
// the Decisions filter draws that view read's rows, as Spec, Verification and Plan draw theirs,
// which is how the root issue settled row 3's gap for Decisions.
// SLOW(>=2s): two seeded issues, each load waited out, and Decisions pressed, read until it holds
// still, and released.
test("each filter draws its view read's rows, and the Decisions filter draws its view read's rows @owed", async ({
  browser,
}) => {
  test.setTimeout(ROW_MS);
  for (const [name, seed] of VIEW_SEEDS) {
    await phase(browser, seed, async (page, seeded) => {
      owedUnless(await treeReadGate(page, seeded.key));
      const decisions = viewAnswerOf(page, "decisions");
      const traffic = watchApiRequests(page);
      const load = await firstLoad(page, traffic, seeded.key);
      owedUnless(decisionsViewGate(load, seeded.key));
      const reads = await idReadsOf(seeded.key);
      const view = PRESET_NAMES.decisions;
      const where = `${name} under ${view}`;
      const drawn = await pressFilter(page, traffic, "decisions");
      const answer = await heldAnswer(decisions, `the ${view} view read`);
      const expected = viewNodeIds(answer, "decisions", reads);
      expect(
        idSet(drawn.nodes.map((node) => node.id)),
        `${where}, the rows are the view read's nodes of root R2's row kinds`
      ).toEqual(idSet(expected.map((node) => node.id)));
      expect(
        idSet(drawn.nodes.flatMap((node) => (node.context ? [node.id] : []))),
        `${where}, the rows drawn as context are the view read's context`
      ).toEqual(idSet(expected.flatMap((node) => (node.role === "context" ? [node.id] : []))));
      expect(
        drawn.count?.trim(),
        `${where}, the control counts the view read's own ${R2_COUNTED_KINDS.decisions.join(", ")}`
      ).toBe(String(viewCount(answer, "decisions")));
      await releaseFilter(page, "decisions");
    });
  }
});
