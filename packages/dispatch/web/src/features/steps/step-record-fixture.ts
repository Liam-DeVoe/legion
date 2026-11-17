import type { StepRecord } from "../../api/types";

/** One step record as the server reads it back, which `steps-model.test.ts` and
 *  `StepsTab.test.tsx` both build on: a passed implementation run of step 2 of the task `default`,
 *  with every field a fixture does not set itself. */
export function stepRecord(fields: Partial<StepRecord> & Pick<StepRecord, "id">): StepRecord {
  return {
    agent: "legion-leg-leg-1-implementer",
    attempt: 1,
    ended_at: "2026-11-16T09:40:00Z",
    generation: 1,
    issue: "CORE-1",
    kind: "step",
    model: "anthropic/claude-opus-5",
    rating: null,
    rating_reason: null,
    report: null,
    result: "passed",
    returned_to: null,
    role: "implementer",
    session: "",
    started_at: "2026-11-16T09:00:00Z",
    step: 2,
    task: "default",
    type: "implementation",
    ...fields,
  };
}
