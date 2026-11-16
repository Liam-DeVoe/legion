import { expect, test } from "bun:test";
import { SEARCH_QUERY_MAX } from "@legion/contracts/dispatch-tools";
import { act } from "@testing-library/react";

import { renderAddress } from "../../__tests__/render-address";
import { useAgentFilters } from "./agent-search";

// The setter takes the fields that changed and writes those alone, so a caller hands it the one
// field its control moves rather than the whole narrowing read back with one field replaced:
// the old setter worked out what had changed by comparing each array's identity, which a caller
// that rebuilt a field it did not change (the group multi-select already does) would defeat, and
// a write of all four cuts `q`, `machine` and `role` at `SEARCH_QUERY_MAX`.
test("the setter writes the field it is handed and leaves every other whole", () => {
  const machines = Array.from({ length: 120 }, (_, index) => `host-${index}`).join(",");
  const query = "p".repeat(SEARCH_QUERY_MAX + 200);
  const hook = renderAddress(() => useAgentFilters(), [`/agents?machine=${machines}&q=${query}`]);
  try {
    const address = () => new URLSearchParams(hook.result.current.location.search);
    expect(hook.result.current.value[0].machines).toHaveLength(120);
    expect(hook.result.current.value[0].search).toBe(query);

    act(() => hook.result.current.value[1]({ roles: ["planner"] }));
    expect(address().get("role")).toBe("planner");
    expect(address().get("machine")).toBe(machines);
    expect(address().get("q")).toBe(query);

    act(() => hook.result.current.value[1]({ groups: ["active"] }));
    expect(address().get("group")).toBe("active");
    expect(address().get("role")).toBe("planner");
    expect(address().get("machine")).toBe(machines);
    expect(address().get("q")).toBe(query);
  } finally {
    hook.unmount();
  }
});

// The reader's own edit of a field is what cuts it, and an empty value takes its parameter out.
test("the field the reader edits is cut at the cap, and emptying it leaves the address", () => {
  const hook = renderAddress(() => useAgentFilters(), ["/agents?group=active"]);
  try {
    const address = () => new URLSearchParams(hook.result.current.location.search);
    act(() => hook.result.current.value[1]({ search: "x".repeat(SEARCH_QUERY_MAX + 5) }));
    expect(address().get("q")).toBe("x".repeat(SEARCH_QUERY_MAX));

    act(() => hook.result.current.value[1]({ groups: [] }));
    expect(address().has("group")).toBe(false);
    expect(address().get("q")).toBe("x".repeat(SEARCH_QUERY_MAX));
  } finally {
    hook.unmount();
  }
});
