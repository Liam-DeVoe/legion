import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { evalDetailQuery } from "../../api/queries";
import type { EvalDetailResponse, EvalListItem } from "../../api/types";
import { textMutedOnSurface, textPrimaryOnSurface } from "../../theme/classes";
import { evalName } from "./chart-model";
import { EvalTitle } from "./EvalPanel";
import type { EvalsScope } from "./useEvalsViews";

/** One chart an eval declares, as its detail answers it: the fields every kind carries. */
interface DeclaredChart {
  readonly id?: unknown;
  readonly kind?: unknown;
  readonly title?: unknown;
}

/** The charts an eval's newest version declares, as its detail read answers them (plan D10): none
 *  until the detail has answered, or where the eval declares none. */
export function declaredCharts(detail: EvalDetailResponse | undefined): readonly DeclaredChart[] {
  const charts = detail?.items?.[0]?.charts;
  return Array.isArray(charts) ? charts : [];
}

/**
 * An eval that declares its own charts, drawn in place of the per-day line (dispatch://INFRA-2918/
 * artifact/plan-md, D7, D10): its answer above its charts, from its detail read at the version the
 * evals view answers, so a register write that moves either reads it again (D11a). Each chart is
 * named here by its title and kind; plan T20 draws each declared kind in this place.
 */
export function DeclaredEval({
  item,
  scope,
}: {
  item: EvalListItem;
  scope: EvalsScope;
}): ReactNode {
  const { slug, version } = item.definition;
  const detail = useQuery(evalDetailQuery(scope.project, slug, version));
  const answer = detail.data?.items?.[0]?.definition.answer ?? item.definition.answer;
  return (
    <div className="flex flex-col gap-1" data-declared={slug}>
      <span className={`text-xs ${textMutedOnSurface}`}>
        <EvalTitle slug={slug} title={evalName(item)} />
      </span>
      {answer === "" ? null : (
        <p className={`text-sm ${textPrimaryOnSurface}`} data-answer>
          {answer}
        </p>
      )}
      <ul aria-label={`Charts of ${evalName(item)}`} className="flex flex-col gap-0.5 text-xs">
        {declaredCharts(detail.data).map((chart, index) => {
          const name =
            typeof chart.title === "string" ? chart.title : String(chart.id ?? index + 1);
          return (
            <li className={textMutedOnSurface} key={String(chart.id ?? index)}>
              {`${name} (${String(chart.kind)})`}
            </li>
          );
        })}
      </ul>
    </div>
  );
}
