import { expect, test } from "bun:test";
import { createStopGates } from "./stop-gates";

const STOP_LINE = "the stop's own line";

test("before the stop every line is told and every write made; once it began, only the stop's own lines are told and nothing is written", () => {
  let stopping = false;
  const gates = createStopGates(() => stopping, new Set([STOP_LINE]));

  expect([gates.tells("envoy: a notice"), gates.tells(STOP_LINE), gates.writes()]).toEqual([
    true,
    true,
    true,
  ]);
  stopping = true;
  expect([gates.tells("envoy: a notice"), gates.tells(STOP_LINE), gates.writes()]).toEqual([
    false,
    true,
    false,
  ]);
});

test("a gated logger drops each line the tell gate drops, and leaves the logger's other members alone", () => {
  let stopping = false;
  const written: string[] = [];
  const logger = createStopGates(() => stopping, new Set([STOP_LINE])).gatedLogger({
    name: "host",
    warn: (line: string, fields?: Record<string, unknown>) =>
      written.push(`warn ${line}${fields === undefined ? "" : ` ${JSON.stringify(fields)}`}`),
    info: (line: string) => written.push(`info ${line}`),
  });

  logger.warn("envoy: before the stop", { sessionID: "ses_a" });
  stopping = true;
  logger.warn("envoy: after the stop");
  logger.info("envoy: after the stop");
  logger.warn(STOP_LINE);

  expect(written).toEqual([
    'warn envoy: before the stop {"sessionID":"ses_a"}',
    `warn ${STOP_LINE}`,
  ]);
  expect(logger.name).toBe("host");
});
