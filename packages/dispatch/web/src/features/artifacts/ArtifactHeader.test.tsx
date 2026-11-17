import { expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import type { ArtifactV2, Version } from "../../api/types";
import { ArtifactHeader } from "./ArtifactHeader";

/** A plan uploaded as a file before a Markdown upload made it a document: version 1 holds the
 *  file and carries its bytes' SHA-256, and version 2 is the document's text, which carries none. */
const versions: Version[] = [
  {
    authors: [{ id: "alice", kind: "user" }],
    created_at: "2026-11-10T00:00:00Z",
    mime: "text/plain",
    named: false,
    number: 1,
    sha256: "plan-file-sha",
    size: 13,
    summary: null,
  },
  {
    authors: [{ id: "alice", kind: "user" }],
    created_at: "2026-11-11T00:00:00Z",
    named: false,
    number: 2,
    size: 40,
    summary: null,
  },
];

const plan: ArtifactV2 = {
  created_at: "2026-11-10T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  id: "artifact-plan",
  issue_key: "CORE-1",
  kind: "doc",
  latest_version: 2,
  latest_version_at: "2026-11-11T00:00:00Z",
  latest_version_size: 40,
  name: "plan.md",
  primary: false,
  project: "CORE",
  ref_key: "CORE-1/plan-md",
  role: "plan",
  session_id: null,
  slug: "plan-md",
  unsettled: null,
  version_count: 2,
};

/** The header of the plan's page showing `version`, with the live document's toolbar, which
 *  carries the plan's versions. */
function renderHeader(version: number) {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  return render(
    <MemoryRouter>
      <QueryClientProvider client={queryClient}>
        <ArtifactHeader
          artifact={plan}
          highlight={false}
          onShowDiffChange={() => {}}
          showVersionPicker={false}
          toolbar={{
            connection: "connected",
            copyBlockLink: () => Promise.resolve(true),
            isNamingVersion: false,
            pending: undefined,
            requestNamedVersion: () => {},
            versions,
          }}
          version={version}
        >
          {null}
        </ArtifactHeader>
      </QueryClientProvider>
    </MemoryRouter>
  );
}

// A version stored as a file has no text, so a diff of it against the current text would show
// nothing: the page draws the file in its place. The header offers the diff on a version of the
// document's text alone.
test("ArtifactHeader offers no diff against the current text on a version that holds a file", () => {
  const file = renderHeader(1);
  try {
    const header = within(file.getByTestId("artifact-header"));
    expect(header.getByRole("button", { name: "Name version" })).toBeTruthy();
    expect(header.queryByRole("button", { name: "Diff vs current" })).toBeNull();
  } finally {
    file.unmount();
  }

  const text = renderHeader(2);
  try {
    const header = within(text.getByTestId("artifact-header"));
    expect(header.getByRole("button", { name: "Diff vs current" })).toBeTruthy();
  } finally {
    text.unmount();
  }
});
