import { expect, test } from "bun:test";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { filterChips } from "./filter-chips";

test("each narrowing that is on is one chip named Remove <its text> filter, whose press takes that one off", async () => {
  const user = userEvent.setup();
  const removed: string[] = [];
  const view = render(
    <div>
      {filterChips([
        { key: "label:docs", label: "Label: docs", remove: () => removed.push("docs") },
        { label: "Author alice", remove: () => removed.push("alice") },
        { label: "Needs you", remove: () => removed.push("needs you") },
      ])}
    </div>
  );
  try {
    const chips = screen.getAllByRole("button");
    expect(chips.map((chip) => chip.getAttribute("aria-label"))).toEqual([
      "Remove Label: docs filter",
      "Remove Author alice filter",
      "Remove Needs you filter",
    ]);
    const author = screen.getByRole("button", { name: "Remove Author alice filter" });
    expect(author).toHaveAttribute("data-control", "chip");
    // A narrowing that is on: the selected fill, and its whole text where it is cut.
    expect(author).toHaveAttribute("aria-pressed", "true");
    expect(author.textContent).toContain("Author alice");
    await user.click(author);
    expect(removed).toEqual(["alice"]);
  } finally {
    view.unmount();
  }
});

test("no narrowing on is no chips at all, which the row reads as nothing to draw", () => {
  expect(filterChips([])).toBeUndefined();
});
