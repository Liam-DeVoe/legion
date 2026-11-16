import { expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useEffect } from "react";
import { MemoryRouter } from "react-router-dom";

import type { Comment, IssueDetails } from "../../api/types";
import { DetailsFocusedNode } from "./details-more";
import { type FocusedNode, FocusedNodeProvider, useSetFocusedNode } from "./focused-node";
import { type OutlineWriter, OutlineWriterProvider, useOutlineWriter } from "./outline-writer";
import { PROPERTY_LABELS } from "./page-structure";
import {
  CONTROL_LABELS,
  issueStatusWord,
  NODE_REFERENCES_WORDS,
  NODE_TYPE_NAMES,
  sentenceCase,
} from "./page-words";

const KEY = "CORE-1";

const issue = {
  artifacts: [],
  key: KEY,
  status: "in_progress",
  title: "Review the spec",
} as unknown as IssueDetails;

/** The reader's own comment, which heads a thread and was delivered to nobody. */
const OWN_COMMENT = {
  ask_id: null,
  author: { id: "alice", kind: "user" },
  body: "The reader's own words.",
  created_at: "2026-11-15T10:00:00Z",
  deliveries: [],
  id: "comment-1",
  reply_to: null,
  resolved: false,
  suggestion: null,
} as unknown as Comment;

/** A client holding every read the facts and the writer make, so the drawing sends none. */
function readsHeld(): QueryClient {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  client.setQueryData(["agents"], []);
  client.setQueryData(["asks", KEY], []);
  client.setQueryData(["comments", KEY], [OWN_COMMENT]);
  client.setQueryData(["issue-messages", KEY], []);
  client.setQueryData(["whoami"], { login: "alice" });
  return client;
}

/** Gives `node` the reader's focus, as the outline does when a row takes it. */
function Focus({ node }: { node: FocusedNode }): ReactNode {
  const setFocused = useSetFocusedNode();
  useEffect(() => setFocused(node), [node, setFocused]);
  return null;
}

// R6 of dispatch://INFRA-3948: until the outline gives a row the focus, and whenever the reader
// is on no row, the facts are the issue's own - its kind, its title and its status - and the
// issue draws no list of what references it, because Details' "Referenced by" row holds it.
test("with no node focused, the facts are the issue's", () => {
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={readsHeld()}>
        <FocusedNodeProvider>
          <DetailsFocusedNode issue={issue} state={undefined} />
        </FocusedNodeProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  try {
    const facts = screen.getByTestId("focused-node-facts");
    expect(facts.getAttribute("data-focused-node")).toBe(KEY);
    expect(
      within(facts).getByRole("heading", { name: PROPERTY_LABELS.focused_node })
    ).toBeDefined();
    const text = facts.textContent ?? "";
    expect(text).toContain(`${sentenceCase(NODE_TYPE_NAMES.issue)}:`);
    expect(text).toContain(issue.title);
    expect(text).toContain(issueStatusWord("in_progress"));
    expect(
      within(facts).queryByRole("button", { name: NODE_REFERENCES_WORDS.what_references })
    ).toBeNull();
  } finally {
    view.unmount();
  }
});

// R7 of dispatch://INFRA-3948: Edit on the reader's own comment is drawn only under the page's
// writer, and sets it to edit that comment and its words; the box that saves it is part L's.
test("Edit on the reader's own comment sets the writer to edit it", async () => {
  let writer: OutlineWriter | undefined;
  function Probe(): ReactNode {
    writer = useOutlineWriter();
    return null;
  }
  const comment: FocusedNode = {
    author: OWN_COMMENT.author,
    id: "comment-1",
    item: { id: "comment-1", kind: "comment" },
    reference: `dispatch://${KEY}/comment/comment-1`,
    title: OWN_COMMENT.body,
    type: "comment",
  };
  const facts = (
    <FocusedNodeProvider>
      <Focus node={comment} />
      <DetailsFocusedNode issue={issue} state={undefined} />
    </FocusedNodeProvider>
  );
  // With no writer above them, the facts draw no Edit.
  const alone = render(
    <MemoryRouter>
      <QueryClientProvider client={readsHeld()}>{facts}</QueryClientProvider>
    </MemoryRouter>
  );
  await waitFor(() =>
    expect(screen.getByTestId("focused-node-facts").getAttribute("data-focused-node")).toBe(
      "comment-1"
    )
  );
  expect(screen.queryByRole("button", { name: CONTROL_LABELS.edit })).toBeNull();
  alone.unmount();

  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={readsHeld()}>
        <OutlineWriterProvider issueKey={KEY}>
          <Probe />
          {facts}
        </OutlineWriterProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  try {
    const edit = await screen.findByRole("button", { name: CONTROL_LABELS.edit });
    expect(writer?.editing, "the writer edited something before Edit").toBeUndefined();

    await userEvent.click(edit);

    await waitFor(() =>
      expect(writer?.editing).toEqual({ body: OWN_COMMENT.body, id: "comment-1" })
    );
    expect(writer?.reply).toBeNull();
  } finally {
    view.unmount();
  }
});
