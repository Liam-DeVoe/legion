import { afterEach, beforeAll, expect, test } from "bun:test";
import { cleanup, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { displayTree } from "../../__tests__/display-trees";
import { renderWithQueries as render } from "../../__tests__/query-provider";
import type { AskAnswerWithTrees } from "../../api/types";
import { warmMarkdownRenderer } from "../refs/markdown-engine";
import { AskAnswerHistory } from "./AskCompletionCard";

beforeAll(async () => {
  await warmMarkdownRenderer();
});

afterEach(cleanup);

// A chosen option of an earlier answer is one of the ask's options, so its label is drawn from
// that option's tree and never parsed in the page: the tree here reads `soon` where the label's
// markdown says `now`. An option the ask no longer holds has no tree anywhere and is shown as
// written (`earlier-chosen-option`).
test("an earlier answer's chosen options are drawn from their options' trees", async () => {
  const answers = [
    {
      at: "2026-10-08T01:00:00Z",
      selected: ["Ship **now**", "**Gone**"],
      text: null,
      user: "alice",
    },
    { at: "2026-10-08T02:00:00Z", selected: ["Hold"], text: null, user: "alice" },
  ] as unknown as AskAnswerWithTrees[];
  const view = render(
    <MemoryRouter>
      <AskAnswerHistory
        answers={answers}
        askId="ask-1"
        issue="CORE-1"
        options={[
          { label: "Ship **now**", label_tree: displayTree("Ship **soon**") },
          { label: "Hold", label_tree: displayTree("Hold") },
        ]}
      />
    </MemoryRouter>
  );
  try {
    await waitFor(() => expect(view.container.querySelector("strong")?.textContent).toBe("soon"));
    expect(view.container.textContent).toContain("Ship soon, **Gone**");
  } finally {
    view.unmount();
  }
});
