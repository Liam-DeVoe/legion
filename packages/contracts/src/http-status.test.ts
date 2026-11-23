import { expect, test } from "bun:test";
import {
  IDEMPOTENCY_KEY_COMMITTED,
  IDEMPOTENCY_KEY_IN_FLIGHT,
  IDEMPOTENCY_KEY_REUSED,
  retriesUnderKey,
} from "./dispatch-api-limits";
import { mayHaveReachedServer, outage } from "./http-status";

/** Every status from 100 to 599. */
const STATUSES = Array.from({ length: 500 }, (_, index) => 100 + index);

/** Every 5xx, written out rather than read through the rule under test. */
const SERVER_ERRORS = Array.from({ length: 100 }, (_, index) => 500 + index);

test("an outage is a 408, a 429 and every 5xx, and no other status from 100 to 599", () => {
  expect(STATUSES.filter(outage)).toEqual([408, 429, ...SERVER_ERRORS]);
});

// A hop's answer may have come after the request reached the server only for a 5xx: so of the
// statuses from 100 to 599 it holds of exactly the 5xx, each of them an outage, and the outages it
// does not hold of are the 408 and the 429 a hop sends before it forwards anything.
test("a request a hop answered may have reached the server after every 5xx, and after no other status from 100 to 599", () => {
  expect(STATUSES.filter(mayHaveReachedServer)).toEqual(SERVER_ERRORS);
  expect(STATUSES.filter((status) => mayHaveReachedServer(status) && !outage(status))).toEqual([]);
  expect(STATUSES.filter((status) => outage(status) && !mayHaveReachedServer(status))).toEqual([
    408, 429,
  ]);
});

// A keyed write asks again after exactly the outages, and after `409 KEY_IN_FLIGHT` besides,
// which is Dispatch's keyed-write layer rather than an outage: so that answer under that code is
// the one place, at any status and any code, where the two rules part.
test("retriesUnderKey reads every status from 100 to 599 as outage does, but a 409 KEY_IN_FLIGHT", () => {
  const codes = [
    undefined,
    IDEMPOTENCY_KEY_IN_FLIGHT,
    IDEMPOTENCY_KEY_COMMITTED,
    IDEMPOTENCY_KEY_REUSED,
    "UNAVAILABLE",
  ];
  const parts = STATUSES.flatMap((status) =>
    codes
      .filter((code) => retriesUnderKey(status, code) !== outage(status))
      .map((code) => `${status} ${code}`)
  );
  expect(parts).toEqual([`409 ${IDEMPOTENCY_KEY_IN_FLIGHT}`]);
});
