import { beforeAll, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { api } from "../../api/client";
import type { IssueReferenceSummary, PersonNode } from "../../api/types";
import { warmMarkdownRenderer } from "../refs/markdown-engine";
import { ReferenceNamesProvider } from "../refs/reference-names";
import { PersonPage } from "./PersonPage";

// The line draws through the shared renderer, whose Markdown engine loads lazily, and the rows
// run in random order, so the file loads the engine before its first row.
beforeAll(async () => {
  await warmMarkdownRenderer();
});

/** The issue the node's line names, as its summary read answers it. */
const named: IssueReferenceSummary = {
  documents: [],
  key: "CORE-9",
  project: "CORE",
  status: "in_progress",
  title: "Warm the cache",
};

/** One comment alice wrote, as the person nodes read sends it: the first line of its text, which
 *  holds a mark and a reference, and no tree of that line. */
const node: PersonNode = {
  created_at: "2026-11-20T09:00:00Z",
  href: "/issues/CORE-1/comments/comment-1",
  id: "comment-1",
  kind: "comment",
  relations: ["authored_by"],
  title: "Ask **dispatch://CORE-9** first.",
};

// The reading list's `person-node-title`: the line is drawn by the renderer, never as the
// characters its author typed. Its mark is drawn as a mark, so no `**` shows, and its reference
// reads as its target's key and title, a link to that issue.
test("a node's line is drawn through the renderer, its marks drawn and a reference read as its target", async () => {
  const listPersonNodes = spyOn(api, "listPersonNodes").mockResolvedValue({
    limit: 50,
    offset: 0,
    person: "alice",
    rows: [node],
    total: 1,
  });
  const getIssueSummary = spyOn(api, "getIssueSummary").mockResolvedValue(named);
  const view = render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <MemoryRouter initialEntries={["/people/alice"]}>
        <ReferenceNamesProvider>
          <Routes>
            <Route element={<PersonPage />} path="/people/:login" />
          </Routes>
        </ReferenceNamesProvider>
      </MemoryRouter>
    </QueryClientProvider>
  );
  try {
    const list = await view.findByRole("list", { name: "Person nodes" });
    const [row] = within(list).getAllByRole("listitem");
    if (row === undefined) throw new Error("the page drew no row for the node");
    await waitFor(() => expect(row.textContent).toContain("Ask CORE-9 Warm the cache first."));
    expect(row.textContent).not.toContain("**");
    expect(row.textContent).not.toContain("dispatch://");
    const reference = row.querySelector('a[href="/issues/CORE-9"]');
    expect(reference?.textContent).toBe("CORE-9 Warm the cache");
    expect(row.querySelector("strong")?.textContent).toBe("CORE-9 Warm the cache");
  } finally {
    view.unmount();
    getIssueSummary.mockRestore();
    listPersonNodes.mockRestore();
  }
});

// The line is read with its own issue as the text's source: a role reference reads as the role
// the issue its comment is on records, read by that issue.
test("a role the line's issue records reads as that role, read by the issue its comment is on", async () => {
  const listPersonNodes = spyOn(api, "listPersonNodes").mockResolvedValue({
    limit: 50,
    offset: 0,
    person: "alice",
    rows: [{ ...node, title: "Ask dispatch://role/legion-p-core-1-reviewer first." }],
    total: 1,
  });
  const collaborators = spyOn(api, "getIssueCollaborators").mockResolvedValue({
    agents: [
      {
        kind: "session",
        last_at: null,
        legion: {
          replaced_at: null,
          replaced_by: null,
          role: "reviewer",
          role_token: "legion-p-core-1-reviewer",
          started_at: "2026-10-26T02:00:00Z",
        },
        reasons: [],
        session_id: "a-reviewer",
        title: "A reviewer",
      },
    ],
    issue_key: "CORE-1",
    legion_registry: "ok",
    people: [],
  } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["agents"], []);
  const view = render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/people/alice"]}>
        <ReferenceNamesProvider>
          <Routes>
            <Route element={<PersonPage />} path="/people/:login" />
          </Routes>
        </ReferenceNamesProvider>
      </MemoryRouter>
    </QueryClientProvider>
  );
  try {
    const list = await view.findByRole("list", { name: "Person nodes" });
    const [row] = within(list).getAllByRole("listitem");
    if (row === undefined) throw new Error("the page drew no row for the node");
    await waitFor(() => expect(row.textContent).toContain("Ask legion-p-core-1-reviewer first."));
    expect(collaborators.mock.calls.map((call) => call[0])).toContain("CORE-1");
  } finally {
    view.unmount();
    collaborators.mockRestore();
    listPersonNodes.mockRestore();
  }
});
