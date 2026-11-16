import { expect, test } from "bun:test";
import { screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { renderWithQueries } from "../../__tests__/query-provider";
import type { Artifact, ArtifactApproval, Ask } from "../../api/types";
import { ApprovalSteps } from "../issue/ApprovalSteps";
import type { StepEntry } from "../issue/gated-steps";
import { NoTooltips } from "./NoTooltips";

const SPEC_ID = "artifact-spec";
const DECISIONS_ID = "artifact-decisions";

function doc(id: string, name: string, approval?: ArtifactApproval): Artifact {
  return {
    created_at: "2026-09-09T00:00:00Z",
    created_by: { id: "alice", kind: "user" },
    id,
    issue_key: "CORE-1",
    kind: "doc",
    name,
    primary: id === SPEC_ID,
    project: "CORE",
    ref_key: `CORE-1/${name}`,
    session_id: null,
    slug: name.replace(".", "-"),
    versions: [],
    ...(approval === undefined ? {} : { approval }),
  };
}

function specEntry(approval?: ArtifactApproval): StepEntry {
  return { document: doc(SPEC_ID, "spec.md", approval), label: "Spec", step: "spec" };
}

function decisionsEntry(approval?: ArtifactApproval): StepEntry {
  return {
    document: doc(DECISIONS_ID, "decisions.md", approval),
    label: "Decisions",
    step: "decisions",
  };
}

// dispatch://INFRA-1196 R19: the link to the version an approval request asks about, which the
// panel's old card drew, is Details' approval entry's own: inside `NoTooltips` each entry links
// its document at the version the request names - not the document's latest - or the version a
// review approved, opened in place of the outline. The header's copy holds R13's list and
// nothing else, so it links nothing.
test("an approval entry links its document at the version the request asks about", () => {
  const request = {
    approval: { artifact_id: SPEC_ID, name: "spec.md", requested_version: 1, version: 1 },
    id: "ask-1",
    kind: "approval",
    state: "open",
  } as unknown as Ask;
  const entries = [
    specEntry({ latest_version: 2, state: "awaiting", waiting_on: "human" }),
    decisionsEntry({ latest_version: 3, state: "stale", version: 2 }),
  ];
  const details = renderWithQueries(
    <MemoryRouter>
      <NoTooltips>
        <ApprovalSteps
          entries={entries}
          primaryArtifactId={SPEC_ID}
          versions={{ asks: [request], issueKey: "CORE-1" }}
        />
      </NoTooltips>
    </MemoryRouter>
  );
  try {
    expect(screen.getByRole("link", { name: "spec.md, version 1" }).getAttribute("href")).toBe(
      "/issues/CORE-1/artifacts/spec-md?v=1"
    );
    expect(screen.getByRole("link", { name: "decisions.md, version 2" }).getAttribute("href")).toBe(
      "/issues/CORE-1/artifacts/decisions-md?v=2"
    );
  } finally {
    details.unmount();
  }

  const header = renderWithQueries(
    <MemoryRouter>
      <NoTooltips>
        <ApprovalSteps
          entries={entries}
          primaryArtifactId={SPEC_ID}
          reasons={false}
          size="compact"
        />
      </NoTooltips>
    </MemoryRouter>
  );
  try {
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  } finally {
    header.unmount();
  }
});
