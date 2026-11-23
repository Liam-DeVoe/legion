import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";

import { api } from "../../api/client";
import { issueQuery } from "../../api/queries";
import type { Artifact, IssueDetails, Version } from "../../api/types";
import type { DocumentToolbar } from "../doc/ProofDocument";
import { emulateChromiumFocusFixup } from "./chromium-focus-fixup";
import { issueDetails } from "./decisions-filter/decisions-fixtures";
import { NoTooltips } from "./NoTooltips";
import { VersionControl } from "./VersionControl";

const spec = {
  approval: { latest_version: 1, state: "requested" },
  created_at: "2026-11-01T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  id: "artifact-spec",
  issue_key: "CORE-1",
  kind: "doc",
  name: "spec.md",
  primary: true,
  project: "CORE",
  ref_key: "CORE-1",
  session_id: null,
  slug: "spec-md",
  versions: [],
} as unknown as Artifact;

function renderControl(copied: boolean) {
  const toolbar: DocumentToolbar = {
    connection: "connected",
    copyBlockLink: async () => copied,
    isNamingVersion: false,
    pending: undefined,
    requestNamedVersion: () => {},
    versions: [],
  };
  return render(
    <VersionControl
      artifact={spec}
      isClosed={false}
      onShowDiffChange={() => {}}
      onVersionChange={() => {}}
      showDiff={false}
      toolbar={toolbar}
      version={undefined}
    />
  );
}

test("the version control reports whether copying a document link succeeded", async () => {
  const user = userEvent.setup();
  for (const [copied, message] of [
    [true, "Copied"],
    [false, "Copy failed - select the text"],
  ] as const) {
    const view = renderControl(copied);
    try {
      await user.click(screen.getByRole("button", { name: "Copy link to block" }));
      expect((await screen.findByText(message)).textContent).toBe(message);
    } finally {
      view.unmount();
    }
  }
});

// No approval beside the filters: the approval entries stay in the identity block
// (dispatch://INFRA-1196 R31).
test("the version control draws no approval", () => {
  const view = renderControl(true);
  try {
    expect(screen.queryByRole("button", { name: /^Approve/ })).toBeNull();
    expect(view.container.textContent ?? "").not.toMatch(/approv/i);
  } finally {
    view.unmount();
  }
});

/** The issue's own read, as the page holds it while the control is drawn: the read Name version
 *  waits for once its write lands. */
function IssueRead(): ReactNode {
  useQuery(issueQuery("CORE-1"));
  return null;
}

// ST-6 for the control's own naming, with no editor of the document on the page: inside
// `NoTooltips` the dialog stays open and busy after the write lands, so Enter sends nothing more,
// until the issue's read, whose documents' versions the picker lists, is in again. Then it closes
// and the focus goes once to Name version, which stayed focusable meanwhile: refused with
// `aria-disabled`, never `disabled`, which would leave the dialog's close nothing to give it to.
test("with no editor on the page, Name version's dialog waits for the issue's read and gives the focus back once", async () => {
  const user = userEvent.setup();
  const unemulate = emulateChromiumFocusFixup();
  const write = Promise.withResolvers<Version>();
  const reread = Promise.withResolvers<IssueDetails>();
  const createArtifactVersion = spyOn(api, "createArtifactVersion").mockReturnValue(write.promise);
  const getIssue = spyOn(api, "getIssue")
    .mockResolvedValueOnce(issueDetails())
    .mockReturnValueOnce(reread.promise);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(
    <QueryClientProvider client={client}>
      <NoTooltips>
        <IssueRead />
        <VersionControl
          artifact={spec}
          isClosed={false}
          onShowDiffChange={() => {}}
          onVersionChange={() => {}}
          showDiff={false}
          toolbar={undefined}
          version={undefined}
        />
      </NoTooltips>
    </QueryClientProvider>
  );
  const moves: EventTarget[] = [];
  const noteMove = (event: FocusEvent) => {
    if (event.target !== null) moves.push(event.target);
  };
  try {
    await waitFor(() => expect(client.getQueryState(["issue", "CORE-1"])?.status).toBe("success"));
    const opener = screen.getByRole("button", { name: "Name version" });
    await user.click(opener);
    const dialog = await screen.findByRole("dialog", { name: "Name this version" });
    const box = within(dialog).getByLabelText("What changed?");
    await waitFor(() => expect(box).toHaveFocus());
    await user.keyboard("Settled the rollout{Enter}");
    await waitFor(() => expect(createArtifactVersion).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(opener).toHaveAttribute("aria-disabled", "true"));
    expect(opener).not.toBeDisabled();

    await act(async () =>
      write.resolve({
        authors: [{ id: "alice", kind: "user" }],
        created_at: "2026-11-02T00:00:00Z",
        named: true,
        number: 1,
        summary: "Settled the rollout",
      })
    );
    await waitFor(() => expect(getIssue).toHaveBeenCalledTimes(2));
    await user.keyboard("{Enter}");
    expect(createArtifactVersion).toHaveBeenCalledTimes(1);
    expect(dialog).toBeInTheDocument();
    expect(box).toHaveFocus();
    expect(box).toHaveAttribute("aria-busy", "true");
    expect(box).not.toBeDisabled();

    document.addEventListener("focusin", noteMove, true);
    await act(async () => reread.resolve(issueDetails()));
    await waitFor(() => expect(dialog).not.toBeInTheDocument());
    expect(opener).toHaveFocus();
    expect(moves).toEqual([opener]);
    expect(createArtifactVersion).toHaveBeenCalledTimes(1);
  } finally {
    document.removeEventListener("focusin", noteMove, true);
    view.unmount();
    unemulate();
    getIssue.mockRestore();
    createArtifactVersion.mockRestore();
  }
});
