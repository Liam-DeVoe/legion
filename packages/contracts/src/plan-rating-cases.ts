import { z } from "zod";

/**
 * `fixtures/daemon-api/plan-rating-cases.json`, the daemon's verdict on each rating of a plan:
 * a rating and a reason a retro's completion carries, either left out where the case leaves it
 * out, and the daemon's verdict on it, `accepted` or its refusal code. internal/api's
 * TestPlanRatingCasesGolden writes the file. Both readers of it parse it with this schema: the
 * contract's request schema (`./legion-api.test.ts`) and the `legion` tool
 * (`packages/pi-shared/src/legion-phase-ops.test.ts`), so a case of another shape, a member of
 * another type or a member the schema does not name fails both, rather than one test reading it
 * through a cast.
 */
export const PlanRatingCases = z.strictObject({
  cases: z
    .array(
      z.strictObject({
        name: z.string().min(1),
        planRating: z.number().int().optional(),
        planReason: z.string().optional(),
        verdict: z.union([z.literal("accepted"), z.string().startsWith("PLAN_RATING_")]),
      })
    )
    .min(1),
});
