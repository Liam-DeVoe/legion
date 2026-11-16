import { BLOCK_ATTRIBUTE_LABELS, PLAN_WORDS } from "../web/src/features/issue-page/page-words";
import { blockNodeId } from "../web/src/features/issue-page/tree-drafts";
import { getArtifact, getIssue } from "./api";
import { seedPlanOrder } from "./issue-layout-seed";
import { PLAN_ORDER_TASKS, requirementBlockId } from "./issue-layout-seed-text";
import { issuePage, phase } from "./issue-page";
import { drawnRows, expectIssuePageDrawn, nodeRow, PHONE, rowOf } from "./issue-page-g";
import { focusedNodeId, idsOf, openPreset } from "./issue-page-i";
import { planChartRows, planView } from "./issue-page-k";
import { expect, test } from "./test";
import { assertPageFits } from "./unclipped";

/**
 * Part K's rows of the new issue page (dispatch://INFRA-3938, part K of dispatch://INFRA-1196):
 * the Plan view, read on the real stack. Each row turns the new page on for its reader with D's
 * `phase`, seeds the plan it reads with the seeds of `e2e/issue-layout-seed.ts`, and reads the
 * page through D's `e2e/issue-page.ts`, G's `e2e/issue-page-g.ts`, I's `e2e/issue-page-i.ts`
 * and this part's `e2e/issue-page-k.ts`.
 *
 * The row comes from the checkpoint's `e2e/issue-layout.e2e.ts`, under the title its check
 * names (`verification.md` of dispatch://INFRA-3938, c-rows).
 */

/** The slug of the issue's own spec, which is how its blocks are addressed. */
async function primarySlug(key: string): Promise<string> {
  const primary = ((await getIssue(key)).artifacts ?? []).find((artifact) => artifact.primary);
  expect(primary, `${key} carries no primary document`).toBeDefined();
  return primary?.slug ?? "";
}

// c-rows (1), as the architect's ruling 5 reads R26 (build-rulings-md): the Plan view opens on
// the chart of time, and one control switches to the ordered steps. The order and each bar's
// place are the page's own, worked out from the `needs` the plan document holds
// (`web/src/features/issue-page/plan-filter/plan-order.ts`), because today's reads give no plan
// read. What waits on dispatch://INFRA-1253, and is not read here: the position and the size
// the plan read gives each task - so every bar is one unit long and its row says "No size" -
// and a parent's Plan view drawing its children's tasks from one request. The overtaken task
// waits on dispatch://INFRA-1950 R5, which is the state the model holds none of today. The
// tester's F19 is read here too: each task is drawn once, and after every task it needs.
test("the Plan filter draws the tasks as ordered steps @phone", async ({ browser }) => {
  test.setTimeout(150_000);
  await phase(browser, seedPlanOrder, async (page, seeded) => {
    const planSlug = (await getArtifact(seeded.planArtifactId)).slug;
    const specSlug = await primarySlug(seeded.key);
    const step = (blockId: string) => blockNodeId(planSlug, blockId);
    const T = PLAN_ORDER_TASKS;
    // Each task after every task in its needs, which is not the order the plan is written in:
    // the document holds t1, t2, t3, and t1 needs t2 and t2 needs t3. The two that need each
    // other stand where the order puts them; the task whose need the plan no longer holds
    // waits on nothing.
    const bars = [step(T.third), step(T.second), step(T.first), step(T.loose)];
    const charted = [step(T.third), step(T.second), step(T.first), "cycle", step(T.loose)];
    const stepped = [
      step(T.third),
      step(T.second),
      step(T.first),
      step(T.knotA),
      step(T.knotB),
      step(T.loose),
    ];
    const chartOrder = async () =>
      (await planChartRows(page)).map((row) => (row.kind === "cycle" ? "cycle" : row.id));

    await openPreset(page, seeded.key, "plan");
    await expectIssuePageDrawn(page);

    // The view opens on the chart, and its one control says so.
    await expect(planView(page, "chart"), "Plan does not open on the chart").toHaveAttribute(
      "aria-pressed",
      "true"
    );
    await expect(planView(page, "steps")).toHaveAttribute("aria-pressed", "false");
    await expect
      .poll(chartOrder, { message: "the chart does not draw each step after the steps it needs" })
      .toEqual(charted);
    const chart = await planChartRows(page);
    const bar = (id: string) => chart.find((row) => row.id === id);

    // One bar per step, each drawn once, starting where every task it needs has ended.
    expect(
      new Set(chart.filter((row) => row.kind === "bar").map((row) => row.id)).size,
      "a step is drawn more than once"
    ).toBe(bars.length);
    expect(
      bars.map((id) => bar(id)?.start),
      "a bar does not start after the tasks it needs"
    ).toEqual([0, 1, 2, 0]);
    for (const [task, needed] of [
      [T.first, T.second],
      [T.second, T.third],
    ] as const) {
      const mine = bar(step(task))?.bar;
      const theirs = bar(step(needed))?.bar;
      expect(
        mine?.left ?? -1,
        `the bar of ${task} starts before the bar of ${needed}, which it needs, has ended`
      ).toBeGreaterThanOrEqual((theirs?.left ?? 0) + (theirs?.width ?? 1e6) - 1);
    }
    // Today's model holds no size, so each bar is one unit long and its row says so (R26).
    const widths = bars.map((id) => Math.round(bar(id)?.bar?.width ?? 0));
    expect(new Set(widths).size, `the bars are not one unit long each: ${widths}`).toBe(1);
    expect(widths[0] ?? 0, "a bar draws no box").toBeGreaterThan(20);
    expect(
      bars.map((id) => bar(id)?.size),
      "a bar whose task holds no size does not say so"
    ).toEqual(bars.map(() => PLAN_WORDS.no_size));

    // Each step shows what it needs and the statements it serves, each statement a link to it;
    // the step that serves two statements is one row naming both.
    expect(bar(step(T.first))?.needs, `${T.first} does not name the step it needs`).toEqual([
      step(T.second),
    ]);
    expect(bar(step(T.third))?.needs, `${T.third} names a step it does not need`).toEqual([]);
    expect(bar(step(T.loose))?.needs, `${T.loose} names a step the plan does not hold`).toEqual([]);
    expect(
      bar(step(T.first))?.serves,
      `${T.first} names other than the one statement it serves`
    ).toHaveLength(1);
    expect(
      bar(step(T.third))?.serves,
      `${T.third} does not name both statements it serves`
    ).toHaveLength(2);
    await expect(
      nodeRow(page, step(T.first)).locator(
        `a[data-plan-serve][href$="#b-${requirementBlockId(1)}"]`
      ),
      `${T.first} does not link the statement it serves`
    ).toHaveCount(1);
    // The lines between a step and the steps it needs: two on this plan.
    await expect(
      issuePage(page).locator("[data-plan-link]"),
      "the chart does not draw one line for each need"
    ).toHaveCount(2);

    // The two tasks that need each other are one line that lists both, each a link to its own
    // node, and neither is a bar: no order can put either after every task it needs.
    const cycle = issuePage(page).locator("[data-plan-chart] [data-plan-cycle]");
    await expect(cycle, "the cycle is not drawn as one line").toHaveCount(1);
    await expect(cycle).toContainText(PLAN_WORDS.cycle);
    for (const knot of [T.knotA, T.knotB]) {
      await expect(cycle, `the cycle's line does not list ${knot}`).toContainText(`Task ${knot}`);
      await expect(
        cycle.locator(`a[href$="#b-${knot}"]`),
        `the cycle's line does not link ${knot} to its own node`
      ).toHaveCount(1);
      expect(bar(step(knot)), `${knot} is drawn as a bar as well as in the cycle`).toBeUndefined();
    }

    // The keys reach the bars in order.
    await nodeRow(page, step(T.third)).first().focus();
    for (const next of bars.slice(1)) {
      await page.keyboard.press("ArrowDown");
      await expect
        .poll(() => focusedNodeId(page), { message: `the arrow did not reach ${next}` })
        .toBe(next);
    }

    // The control switches to the ordered steps, which are the outline's own rows in the same
    // order, with the cycle's tasks as rows where the order puts the cycle.
    await planView(page, "steps").click();
    await expect(planView(page, "steps")).toHaveAttribute("aria-pressed", "true");
    await expectIssuePageDrawn(page);
    await expect
      .poll(async () => idsOf(await drawnRows(page)), {
        message: "the steps are not drawn after the steps they need",
      })
      .toEqual(stepped);
    const rows = await drawnRows(page);
    const first = rowOf(rows, step(T.first));
    expect(first?.chips.join(" "), `${T.first} does not say what it needs`).toContain(
      BLOCK_ATTRIBUTE_LABELS.needs
    );
    expect(first?.chips.join(" "), `${T.first} does not name the task it needs`).toContain(
      `Task ${T.second}`
    );
    expect(first?.parent?.id, `${T.first} does not name the statement it serves`).toBe(
      blockNodeId(specSlug, requirementBlockId(1))
    );
    expect(first?.parent?.type).toBe("requirement");
    await expect(
      issuePage(page).locator("[data-plan-cycle]"),
      "the steps do not draw the cycle's one line"
    ).toHaveCount(1);

    // The same order on a phone, in both views, where the page fits in one direction (R18).
    await page.setViewportSize({ height: PHONE.height, width: PHONE.width });
    await expectIssuePageDrawn(page);
    await expect
      .poll(async () => idsOf(await drawnRows(page)), {
        message: "the steps' order is not the same at 390 px",
      })
      .toEqual(stepped);
    await assertPageFits(page);
    await planView(page, "chart").click();
    await expect
      .poll(chartOrder, { message: "the chart's order is not the same at 390 px" })
      .toEqual(charted);
    expect(
      (await planChartRows(page)).filter((row) => row.kind === "bar").map((row) => row.size),
      "a bar does not say it holds no size at 390 px"
    ).toEqual(bars.map(() => PLAN_WORDS.no_size));
    await assertPageFits(page);
  });
});
