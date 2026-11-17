import { afterEach, beforeEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, type RenderResult, render, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { api } from "../../../api/client";
import { MarginProvider } from "../../margin/margin-context";
import { parseIssuePath } from "../../refs/routes";
import { KeymapProvider } from "../../shell/KeymapProvider";
import { OutlineRequestProvider } from "../outline-request";
import { EMPTY_STATE_LINES } from "../page-words";
import { ViewDecisionsSlot } from "../slots/view-decisions";
import { issueDetails, KEY } from "./decisions-fixtures";

/**
 * The line the Decisions view's slot hands its body for an issue that holds no question and no
 * decision it draws: the empty issue's own line where no words are typed, and the line that says
 * nothing matched where they are. Words of spaces alone filter nothing (the view trims them), so
 * they draw the empty issue's line too.
 */

/** Every read the page's tree is built from, answered with nothing, and who is reading. */
function stubReads(): { mockRestore(): void }[] {
  return [
    spyOn(api, "getIssueCoverage").mockResolvedValue({
      gaps: [],
      has_plan: false,
      issue: KEY,
      requirements: [],
      spec_artifact_id: "",
    }),
    spyOn(api, "getIssueDecisions").mockResolvedValue({
      decisions: [],
      document: null,
      open_questions: [],
    }),
    spyOn(api, "getIssueResults").mockResolvedValue({
      checks: [],
      issue: KEY,
      verification_artifact_id: "",
    }),
    spyOn(api, "getIssueReferences").mockResolvedValue({ members: [], truncated: false }),
    spyOn(api, "listIssueAsks").mockResolvedValue([]),
    spyOn(api, "listComments").mockResolvedValue([]),
    spyOn(api, "listIssueMessages").mockResolvedValue([]),
    spyOn(api, "getArtifactBlocks").mockResolvedValue([]),
    spyOn(api, "whoAmI").mockResolvedValue({ event_head: "0", kind: "user", login: "alice" }),
    spyOn(api, "getReferenceTargets").mockResolvedValue({ targets: [] }),
  ];
}

let stubs: { mockRestore(): void }[] = [];
beforeEach(() => {
  stubs = stubReads();
});
afterEach(() => {
  cleanup();
  for (const stub of stubs) stub.mockRestore();
});

/** The slot on the issue's Decisions address with `search` after it, under the page's own
 *  providers. */
function drawSlot(search: string): RenderResult {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const route = parseIssuePath(`/issues/${KEY}/decisions`);
  if (route === undefined) throw new Error("the Decisions address names no route");
  return render(
    <MemoryRouter initialEntries={[`/issues/${KEY}/decisions${search}`]}>
      <QueryClientProvider client={client}>
        <KeymapProvider>
          <MarginProvider>
            <OutlineRequestProvider>
              <ViewDecisionsSlot issue={issueDetails()} route={route} />
            </OutlineRequestProvider>
          </MarginProvider>
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
}

/** The line the body draws once the reads that say which entries there are have answered. */
async function emptyLineOf(view: RenderResult): Promise<string | null | undefined> {
  await waitFor(() =>
    expect(view.container.querySelector("[data-decisions-body] p")).not.toBeNull()
  );
  return view.container.querySelector("[data-decisions-body] p")?.textContent;
}

test("no words draw the empty issue's line, and words that match nothing the no-match line", async () => {
  expect(await emptyLineOf(drawSlot(""))).toBe(EMPTY_STATE_LINES.decisions);
  cleanup();
  expect(await emptyLineOf(drawSlot("?q=zzz"))).toBe(EMPTY_STATE_LINES.no_match);
});

test("words of spaces alone draw the empty issue's line, as no words do", async () => {
  expect(await emptyLineOf(drawSlot("?q=%20%20"))).toBe(EMPTY_STATE_LINES.decisions);
});
