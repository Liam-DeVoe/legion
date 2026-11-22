import { expect, test } from "bun:test";
import { ROW_MARGIN_MS } from "@legion/test-fixtures/row-clock";
import {
  JUDGE_DOCKER_CALL,
  JUDGE_OMP,
  JUDGE_OMP_ENVIRONMENT,
  JUDGE_PULLS,
  JUDGE_STANDIN_OMP,
  RIG_STEP,
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

// Catches: a judge-omp row whose wait is a bound measured on another step (it was RIG_STEP, the
// heads fixture's build, while what the row waits on is the Docker daemon and omp), and rows that
// hand rig.sh other bounds than the ones their wait counts.
test("a judge's wait is its host step, then each Docker call's bound and omp's, as rig.sh is given them", () => {
  expect(JUDGE_OMP.boundMs).toBe(
    RIG_STEP.boundMs +
      // The image's inspect, each pull and the pauses between them, and the run's daemon part.
      (1 + JUDGE_PULLS + 1) * JUDGE_DOCKER_CALL.boundMs +
      (JUDGE_PULLS - 1) * 2_000 +
      JUDGE_STANDIN_OMP.boundMs
  );
  expect({
    docker: Number(JUDGE_OMP_ENVIRONMENT.SKILL_SCENARIOS_DOCKER_TIMEOUT) * 1000,
    omp: Number(JUDGE_OMP_ENVIRONMENT.SKILL_SCENARIOS_TIMEOUT) * 1000,
  }).toEqual({ docker: JUDGE_DOCKER_CALL.boundMs, omp: JUDGE_STANDIN_OMP.boundMs });
  // A stall of the daemon as long as one Docker call's bound fits the wait beside the host step.
  expect(JUDGE_OMP.boundMs).toBeGreaterThan(RIG_STEP.boundMs + JUDGE_DOCKER_CALL.boundMs);
});
