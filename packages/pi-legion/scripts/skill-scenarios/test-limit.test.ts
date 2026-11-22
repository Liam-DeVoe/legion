import { expect, test } from "bun:test";
import { ROW_MARGIN_MS } from "@legion/test-fixtures/row-clock";
import {
  DOCKER_BOUND_MARGIN,
  JUDGE_DOCKER_CALL,
  JUDGE_OMP,
  JUDGE_OMP_ENVIRONMENT,
  JUDGE_ROW_WORK,
  JUDGE_STANDIN_OMP,
  ROW_WORK_MS,
  rowLimit,
} from "./test-limit";

test("a row's limit is the bounds of what it waits for, its own work and the margin", () => {
  const parts = [
    { part: "one", boundMs: 1_000 },
    { part: "two", boundMs: 2_000 },
  ];
  expect(rowLimit(...parts)).toBe(3_000 + ROW_WORK_MS + ROW_MARGIN_MS);
  expect(rowLimit()).toBe(ROW_WORK_MS + ROW_MARGIN_MS);
});

// Catches: a Docker call's bound that is not the slowest measured call with the margin of the
// repository's other Docker bounds, or not whole seconds, which rig.sh refuses.
test("a Docker call's bound is the slowest measured call with the margin, in whole seconds", () => {
  const measured = DOCKER_BOUND_MARGIN * JUDGE_ROW_WORK.boundMs;
  expect(JUDGE_DOCKER_CALL.boundMs).toBeGreaterThanOrEqual(measured);
  expect(JUDGE_DOCKER_CALL.boundMs).toBeLessThan(measured + 1_000);
  expect(JUDGE_DOCKER_CALL.boundMs % 1_000).toBe(0);
});

// Catches: a judge-omp row whose wait adds up every Docker call's bound (with every call of a
// daemon that answers none, the rows then outlast the pi-legion job's timeout rather than fail by
// the rig's refusal), and rows that hand rig.sh other bounds than the ones their wait counts.
test("a judge row's wait is its normal work and one Docker call's bound, the run's, as rig.sh is given them", () => {
  expect(JUDGE_OMP.boundMs).toBe(
    JUDGE_ROW_WORK.boundMs +
      // The largest one call's bound: the run's, the daemon's part and omp's own.
      JUDGE_DOCKER_CALL.boundMs +
      JUDGE_STANDIN_OMP.boundMs
  );
  expect({
    docker: Number(JUDGE_OMP_ENVIRONMENT.SKILL_SCENARIOS_DOCKER_TIMEOUT) * 1000,
    omp: Number(JUDGE_OMP_ENVIRONMENT.SKILL_SCENARIOS_TIMEOUT) * 1000,
  }).toEqual({ docker: JUDGE_DOCKER_CALL.boundMs, omp: JUDGE_STANDIN_OMP.boundMs });
});
