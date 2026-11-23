// The kinds of declared chart the Evals tab draws (dispatch://INFRA-2918/artifact/plan-md, D10,
// T20), by kind: each its items views, its pure model function and its renderer (`ChartKind`,
// kind.ts). A declared chart of a kind this record lacks draws one line naming it
// (`DeclaredEval`). `kinds.test.ts` lists the 14 kinds the validator accepts against it.
import { bandKind } from "./band";
import { barKind } from "./bar";
import { intervalKind } from "./interval";
import type { ChartKind } from "./kind";
import { stackedBarKind } from "./stacked-bar";
import { xyKind } from "./xy";

export const chartKinds: Readonly<Record<string, ChartKind>> = {
  band: bandKind,
  bar: barKind,
  interval: intervalKind,
  "stacked-bar": stackedBarKind,
  xy: xyKind,
};
