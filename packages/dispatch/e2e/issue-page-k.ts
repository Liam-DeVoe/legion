import type { Locator, Page } from "@playwright/test";

import { issuePage } from "./issue-page";

/**
 * What part K's rows read of the Plan view (dispatch://INFRA-3938, part K of
 * dispatch://INFRA-1196): its chart and the one control that switches it to the ordered steps
 * (`web/src/features/issue-page/plan-filter/PlanFilterBody.tsx`). This is part K's file, and no
 * other part changes it (decision 2 of the parts). The steps are the outline's own rows, which
 * part G's `e2e/issue-page-g.ts` reads.
 *
 * - **The chart** `[data-plan-chart]`: a step's row is `[data-plan-bar-row]` with `data-node-id`
 *   and `data-plan-start` (the unit its bar starts at), holding its bar `[data-plan-bar]`, the
 *   mark that it holds no size `[data-plan-no-size]`, each task it needs `[data-plan-need]` (by
 *   that task's node id) and each statement it serves `[data-plan-serve]`; a cycle's row is its
 *   one line `[data-plan-cycle]`.
 * - **The control** `[data-plan-switch]`: the two buttons `[data-plan-view="chart"]` and
 *   `[data-plan-view="steps"]`, the one that is on carrying `aria-pressed="true"`.
 */

/** One row of the Plan view's chart: a step with where its bar starts, what it says of its
 *  size, the steps it needs and the statements it serves; or a cycle's one line. */
export interface ChartRow {
  readonly id: string;
  readonly kind: "bar" | "cycle";
  readonly needs: readonly string[];
  readonly serves: readonly string[];
  readonly size: string;
  /** The bar's left edge and its width, in px, or null for a cycle's line. */
  readonly bar: { readonly left: number; readonly width: number } | null;
  readonly start: number;
  readonly text: string;
}

/** Every row of the Plan view's chart, in the order it is drawn. */
export function planChartRows(page: Page): Promise<ChartRow[]> {
  return page.evaluate(() => {
    const text = (element: Element | null) =>
      (element?.textContent ?? "").replace(/\s+/g, " ").trim();
    return [
      ...document.querySelectorAll("[data-plan-chart] :is([data-plan-bar-row], [data-plan-cycle])"),
    ].map((row) => {
      const bar = row.querySelector("[data-plan-bar]")?.getBoundingClientRect();
      return {
        bar: bar === undefined ? null : { left: bar.left, width: bar.width },
        id: row.getAttribute("data-node-id") ?? "",
        kind: row.hasAttribute("data-plan-cycle") ? ("cycle" as const) : ("bar" as const),
        needs: [...row.querySelectorAll("[data-plan-need]")].map(
          (need) => need.getAttribute("data-plan-need") ?? ""
        ),
        serves: [...row.querySelectorAll("[data-plan-serve]")].map(text),
        size: text(row.querySelector("[data-plan-no-size]")),
        start: Number(row.getAttribute("data-plan-start") ?? "-1"),
        text: text(row),
      };
    });
  });
}

/** One of the Plan view's two controls, the chart or the ordered steps (R6). */
export function planView(page: Page, view: "chart" | "steps"): Locator {
  return issuePage(page).locator(`[data-plan-view="${view}"]`);
}
