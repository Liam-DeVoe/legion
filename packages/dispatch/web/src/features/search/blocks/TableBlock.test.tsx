import { afterEach, beforeEach, expect, spyOn, test } from "bun:test";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { renderWithQueries } from "../../../__tests__/query-provider";
import { api } from "../../../api/client";
import type { BlockOf } from "./block-cells";
import { TableBlock } from "./TableBlock";

// Each issue chip reads its issue's summary, the one read every reference to the issue shares.
let summaries: { mockRestore: () => void } | undefined;
beforeEach(() => {
  summaries = spyOn(api, "getIssueSummary").mockImplementation(async (key: string) => ({
    documents: [],
    key,
    project: "DEMO",
    status: "todo",
    title: `The issue ${key}`,
  }));
});
afterEach(() => summaries?.mockRestore());

/** The words of each body row's cells. */
function cellWords(): readonly (readonly (string | null)[])[] {
  return within(screen.getByTestId("going-block-table"))
    .getAllByRole("row")
    .slice(1)
    .map((row) =>
      within(row)
        .getAllByRole("cell")
        .map((cell) => cell.textContent)
    );
}

/** Waits until each of `count` issue chips shows its issue's title, read from its summary, so
 *  no read lands after the row has ended. */
async function chipsRead(count: number): Promise<void> {
  await waitFor(() => expect(screen.getAllByText(/^The issue DEMO-\d+$/u)).toHaveLength(count));
}

// A table the command bar's agent draws of issues counted by priority, as the server's chain
// answers it: each priority as the number the issue reads carry, and the issues with none in the
// group "none". The person reads each priority as Dispatch writes it everywhere else.
test("a table of issues by priority shows each priority as Dispatch writes it", () => {
  const block: BlockOf<"table"> = {
    kind: "table",
    title: "Open bugs by priority",
    spec: { source: "run", params: {}, table: "the chain's answer", columns: ["priority", "bugs"] },
    rows: [
      { priority: 1, bugs: 5 },
      { priority: 2, bugs: 2 },
      { priority: "none", bugs: 1 },
    ],
  };
  render(<TableBlock block={block} />);
  expect(cellWords()).toEqual([
    ["P1", "5"],
    ["P2", "2"],
    ["No priority", "1"],
  ]);
  // The columns are headed as the issue page names its fields, never by their stored names.
  expect(
    within(screen.getByTestId("going-block-table"))
      .getAllByRole("columnheader")
      .map((head) => head.textContent)
  ).toEqual(["Priority", "Bugs"]);
});

// Design v2, state 5: a status by its word, at most 8 rows with "Show all 42 ›" under them, and
// the first cell of a row that is an issue drawn as that issue's chip, a link to it.
const statuses = ["todo", "in_progress", "needs_review", "done"];
const issues: BlockOf<"table"> = {
  kind: "table",
  title: "DEMO issues",
  spec: { source: "run", params: {}, table: "issues", columns: ["key", "status"] },
  rows: Array.from({ length: 10 }, (_, index) => ({
    key: `DEMO-${index + 1}`,
    status: statuses[index % statuses.length],
  })),
};

test("a table shows 8 rows, its statuses in words and its issues as chips, then Show all", async () => {
  const user = userEvent.setup();
  renderWithQueries(<TableBlock block={issues} />);
  const rows = cellWords();
  expect(rows).toHaveLength(8);
  expect(rows.slice(0, 4).map((row) => row[1])).toEqual([
    "Todo",
    "In progress",
    "Needs review",
    "Done",
  ]);
  const chip = screen.getAllByTestId("going-block-item")[0] as HTMLElement;
  expect(chip).toHaveAttribute("href", "/issues/DEMO-1");
  expect(within(chip).getByTestId("item-chip")).toHaveTextContent("DEMO-1");
  // The header stays on top while the rows scroll under it.
  expect(screen.getByTestId("going-block-table").querySelector("thead")?.className).toContain(
    "sticky"
  );
  // With no conversation to open it over, Show all shows every row in place.
  const all = screen.getByTestId("going-block-show-all");
  expect(all).toHaveTextContent("Show all 10 ›");
  await user.click(all);
  expect(cellWords()).toHaveLength(10);
  expect(screen.queryByTestId("going-block-show-all")).toBeNull();
  await chipsRead(10);
});

test("Show all calls the conversation's own opener where one is given, and a whole table shows every row", async () => {
  const user = userEvent.setup();
  let opened = 0;
  const { unmount } = renderWithQueries(
    <TableBlock block={issues} onShowAll={() => (opened += 1)} />
  );
  await user.click(screen.getByTestId("going-block-show-all"));
  expect(opened).toBe(1);
  expect(cellWords()).toHaveLength(8);
  unmount();
  renderWithQueries(<TableBlock block={issues} whole />);
  expect(cellWords()).toHaveLength(10);
  expect(screen.queryByTestId("going-block-show-all")).toBeNull();
  await chipsRead(10);
});
