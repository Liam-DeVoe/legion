import { expect, test } from "bun:test";

/** Every kind of declared chart the validator accepts (`internal/dispatch/evals/charts`, a port of
 *  the eval platform's `validate_charts`): its 15 kinds but `module`, which Dispatch refuses
 *  (dispatch://INFRA-2918/artifact/plan-md, D10 and decision 5a). */
const ACCEPTED_KINDS = [
  "xy",
  "band",
  "bar",
  "interval",
  "stacked-bar",
  "grid",
  "lanes",
  "network",
  "architecture",
  "number",
  "pending",
  "runs",
  "text",
  "prose",
] as const;

/** The registry the tab draws a declared chart through: each kind's pure model function (the spec
 *  and its readings to the chart's model) and its renderer, by kind. Read by a path held in a
 *  string, so this file compiles and runs before the registry exists: until it does, the import
 *  fails and so does the row, which `test.failing` keeps green; once it exists, the row passes and
 *  `test.failing` turns it red, the sign to drop the marker (plan T1, T20). */
const REGISTRY_PATH: string = "./registry";

test.failing("every accepted kind has a model and a renderer", async () => {
  const { chartKinds } = (await import(REGISTRY_PATH)) as {
    chartKinds: Readonly<Record<string, { model: unknown; renderer: unknown }>>;
  };
  expect(ACCEPTED_KINDS).toHaveLength(14);
  expect(Object.keys(chartKinds).sort()).toEqual([...ACCEPTED_KINDS].sort());
  for (const kind of ACCEPTED_KINDS) {
    expect([kind, typeof chartKinds[kind]?.model]).toEqual([kind, "function"]);
    expect([kind, typeof chartKinds[kind]?.renderer]).toEqual([kind, "function"]);
  }
  expect(Object.hasOwn(chartKinds, "module")).toBe(false);
});
