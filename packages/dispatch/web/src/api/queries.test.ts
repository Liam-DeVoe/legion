import { expect, test } from "bun:test";

import { EVALS_VIEW_LIMIT, type EvalKind, type EvalsViewParams, evalsViewQuery } from "./queries";

const KINDS: EvalKind[] = ["delivery", "legion", "ci", "experiment"];

const COMPARE = { from: "2026-09-27T00:00:00.000Z", to: "2026-10-26T00:00:00.000Z" };

/** The live view `params` subscribe: the key's last part. */
function viewOf(params: EvalsViewParams) {
  const [scope, name, view] = evalsViewQuery(params).queryKey;
  if (typeof view === "string") throw new Error("the key names no view");
  expect([scope, name]).toEqual(["evals", "view"]);
  return view;
}

// The query is the one GET /api/v1/projects/{key}/evals takes without its window, which the view
// names apart (packages/contracts DeliveryLiveView), so the hub reads it as the route reads it.
test.each(
  KINDS
)("the %s view carries the project, the window, the compare range, the repositories and its kind", (kind: EvalKind) => {
  const view = viewOf({
    compare: COMPARE,
    kind,
    offset: 0,
    project: "ACME",
    repos: ["acme/widgets", "acme/gadgets"],
    window: { preset: 28 },
  });
  expect(view).toEqual({
    kind: "evals",
    project: "ACME",
    query: expect.any(String),
    window: { preset: 28 },
  });
  const query = new URLSearchParams(view.query);
  expect([...query.keys()].sort()).toEqual([
    "compare_from",
    "compare_to",
    "kind",
    "limit",
    "repo",
    "repo",
  ]);
  expect(query.get("compare_from")).toBe(COMPARE.from);
  expect(query.get("compare_to")).toBe(COMPARE.to);
  expect(query.getAll("repo")).toEqual(["acme/widgets", "acme/gadgets"]);
  expect(query.get("kind")).toBe(kind);
  expect(query.get("limit")).toBe(String(EVALS_VIEW_LIMIT));
});

test("a view with no compare range names none, one of the project's own rows no repository, and a later page its offset", () => {
  const first = viewOf({
    compare: null,
    kind: "ci",
    offset: 0,
    project: "ACME",
    repos: [],
    window: { from: "2026-01-01T00:00:00Z", to: "2026-01-29T00:00:00Z" },
  });
  expect(first.window).toEqual({ from: "2026-01-01T00:00:00Z", to: "2026-01-29T00:00:00Z" });
  expect(first.query).toBe("kind=ci&limit=200");

  const second = viewOf({
    compare: null,
    kind: "ci",
    offset: EVALS_VIEW_LIMIT,
    project: "ACME",
    repos: [],
    window: { preset: 7 },
  });
  expect(second.query).toBe("kind=ci&limit=200&offset=200");
});
