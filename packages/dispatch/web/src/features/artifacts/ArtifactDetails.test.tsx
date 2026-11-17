import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
// biome-ignore lint/style/noRestrictedImports: the two compare selects below are set to the version each already holds, which user-event's selectOptions cannot deliver
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { graphNode } from "../../__tests__/graph-fixture";
import { api, type WholeList } from "../../api/client";
import { ArtifactDetails } from "./ArtifactDetails";
import { type DocumentFixture, documentRead, serveVersions } from "./artifact-fixture";

/** The graph's answer for a node: the edges at it, every page, with the node beside them. */
type References = WholeList<"/api/v2/references">;

/** The answer `GET /api/v2/references` gives with `edges` at `node`, one page holding them all. */
function referencesOf(edges: References["items"], node: References["node"]): References {
  return { items: edges, limit: 50, node, offset: 0, total: edges.length };
}

const artifact = {
  created_at: "2026-09-09T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  id: "artifact-1",
  issue_key: "CORE-1",
  project: "CORE",
  kind: "image",
  name: "diagram.png",
  primary: false,
  ref_key: "CORE-1/diagram-png",
  session_id: null,
  slug: "diagram-png",
  unsettled: null,
  versions: [
    {
      authors: [{ id: "alice", kind: "user" }],
      created_at: "2026-09-09T00:00:00Z",
      mime: "image/png",
      named: false,
      number: 1,
      sha256: "first-sha",
      size: 10,
      summary: null,
    },
    {
      authors: [{ id: "alice", kind: "user" }],
      created_at: "2026-09-09T01:00:00Z",
      mime: "image/png",
      named: false,
      number: 2,
      sha256: "second-sha",
      size: 20,
      summary: null,
    },
  ],
  // `satisfies`, not an annotation: the tests below map over `artifact.versions`, which the
  // route table's `Version[] | null` would not let them.
} satisfies DocumentFixture;

function renderDetails(target: DocumentFixture) {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  return render(
    <MemoryRouter>
      <QueryClientProvider client={queryClient}>
        <ArtifactDetails artifact={documentRead(target)} />
      </QueryClientProvider>
    </MemoryRouter>
  );
}

/** The graph's answer for an artifact nothing cites: the details panel reads this on every
 *  render, so a test that is not about references answers it with no edges. */
function noReferences(target: DocumentFixture) {
  // A project document has no issue, and its reference names its project instead.
  const owner = target.issue_key ?? target.project;
  return spyOn(api, "getReferences").mockResolvedValue(
    referencesOf(
      [],
      graphNode({
        id: target.id,
        issue_key: target.issue_key ?? undefined,
        kind: "artifact",
        project: target.project,
        ref: `dispatch://${owner}/artifact/${target.slug}`,
      })
    )
  );
}

test("ArtifactDetails compares selected blob versions side by side", async () => {
  const user = userEvent.setup();
  const getArtifact = spyOn(api, "getArtifact").mockResolvedValue(documentRead(artifact));
  const getReferences = noReferences(artifact);
  const listArtifactVersions = serveVersions([artifact]);

  try {
    const view = renderDetails(artifact);

    // The comparison is a deliberate action: nothing to pick from until it is opened.
    expect(screen.queryByLabelText("Compare diagram.png from")).not.toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Show comparison" }));
    // Version 1 is the From select's own default: a selection a control already holds is one
    // user-event's selectOptions cannot deliver.
    fireEvent.change(screen.getByLabelText("Compare diagram.png from"), {
      target: { value: "1" },
    });
    // Version 2 is likewise the To select's default.
    fireEvent.change(screen.getByLabelText("Compare diagram.png to"), {
      target: { value: "2" },
    });

    const comparison = await screen.findByLabelText("Blob version comparison");
    expect(comparison.textContent).toContain("Version 1");
    expect(comparison.textContent).toContain("10 B");
    expect(comparison.textContent).toContain("first-sha");
    expect(comparison.textContent).toContain("Version 2");
    expect(comparison.textContent).toContain("20 B");
    expect(comparison.textContent).toContain("second-sha");
    await user.selectOptions(screen.getByLabelText("Compare diagram.png to"), "1");
    expect(await screen.findByText("Identical blobs.")).not.toBeNull();
    view.unmount();
  } finally {
    getArtifact.mockRestore();
    getReferences.mockRestore();
    listArtifactVersions.mockRestore();
  }
});

// A document version's line names its stored markdown's size, as a file version's names its bytes;
// a document carries no SHA-256, so its line names none.
test("ArtifactDetails names each document version's stored size", async () => {
  const user = userEvent.setup();
  const doc: DocumentFixture = {
    ...artifact,
    id: "artifact-doc",
    kind: "doc",
    name: "spec.md",
    slug: "spec-md",
    versions: artifact.versions.map(({ mime: _mime, sha256: _sha256, ...version }) => ({
      ...version,
      size: version.number === 1 ? 512 : 1_048_475,
    })),
  };

  const getReferences = noReferences(doc);
  const listArtifactVersions = serveVersions([doc]);
  const view = renderDetails(doc);
  try {
    await user.click(await screen.findByRole("button", { name: "Show all versions" }));
    const versions = screen.getByRole("region", { name: "Versions for spec.md" });
    expect(versions.textContent).toContain("512 B");
    expect(versions.textContent).toContain("1023.9 KB");
    expect(versions.textContent).not.toContain("SHA-256");
  } finally {
    view.unmount();
    getReferences.mockRestore();
    listArtifactVersions.mockRestore();
  }
});

/** A plan uploaded as a file before a Markdown upload made it a document: version 1 holds the
 *  file, with its bytes' type and SHA-256, and each version after it is the document's text, which
 *  carries neither. */
function convertedPlan(textVersions: number): DocumentFixture {
  return {
    ...artifact,
    id: "artifact-plan",
    kind: "doc",
    name: "plan.md",
    slug: "plan-md",
    versions: [
      {
        authors: [{ id: "alice", kind: "user" }],
        created_at: "2026-09-09T00:00:00Z",
        mime: "text/plain",
        named: false,
        number: 1,
        sha256: "plan-file-sha",
        size: 12,
        summary: null,
      },
      ...Array.from({ length: textVersions }, (_, index) => ({
        authors: [{ id: "alice", kind: "user" as const }],
        created_at: `2026-09-1${index}T00:00:00Z`,
        named: false,
        number: index + 2,
        size: 40,
        summary: null,
      })),
    ],
  };
}

// The file version reads as a file: its type, its size and its SHA-256 beside its download link.
// The compare reads text, so it offers the text versions alone: a file version answers its bytes,
// and a compare of it waited for ever on text it never got.
test("ArtifactDetails shows a document's file version as a file and compares its text versions alone", async () => {
  const user = userEvent.setup();
  const plan = convertedPlan(2);
  const getReferences = noReferences(plan);
  const listArtifactVersions = serveVersions([plan]);
  const getArtifactVersion = spyOn(api, "getArtifactVersion").mockImplementation(
    async (_id, version) => ({
      authors: [],
      created_at: "2026-09-14T00:00:00Z",
      markdown: version === 2 ? "Before\n" : "After\n",
      named: false,
      number: version,
      summary: null,
    })
  );
  const view = renderDetails(plan);
  try {
    await user.click(await screen.findByRole("button", { name: "Show all versions" }));
    const download = screen.getByRole("link", { name: "Download version 1" });
    expect(download.getAttribute("href")).toBe(
      "/api/v2/artifacts/artifact-plan/versions/1?download=true"
    );
    const fileLine = download.closest("li")?.textContent;
    expect(fileLine).toContain("text/plain");
    expect(fileLine).toContain("12 B");
    expect(fileLine).toContain("SHA-256 plan-file-sha");
    const textLine = screen.getByRole("link", { name: "Download version 2" }).closest("li");
    expect(textLine?.textContent).toContain("40 B");
    expect(textLine?.textContent).not.toContain("SHA-256");

    await user.click(screen.getByRole("button", { name: "Show comparison" }));
    for (const side of ["from", "to"]) {
      const picker = screen.getByLabelText(`Compare plan.md ${side}`);
      expect(
        within(picker)
          .getAllByRole("option")
          .map((option) => option.textContent)
      ).toEqual(["Version 3", "Version 2"]);
    }
    await screen.findByText("After", { exact: false });
    expect(getArtifactVersion.mock.calls.map(([, version]) => version).sort()).toEqual([2, 3]);
  } finally {
    view.unmount();
    getArtifactVersion.mockRestore();
    getReferences.mockRestore();
    listArtifactVersions.mockRestore();
  }
});

// With one text version beside the file there is nothing to compare: the compare it offered
// before read the file version and stood at "Loading versions to compare…" for ever.
test("ArtifactDetails offers no compare for a document whose only other version is a file", async () => {
  const plan = convertedPlan(1);
  const getReferences = noReferences(plan);
  const listArtifactVersions = serveVersions([plan]);
  const view = renderDetails(plan);
  try {
    await screen.findByRole("button", { name: "Show all versions" });
    expect(screen.queryByRole("button", { name: "Show comparison" })).not.toBeInTheDocument();
  } finally {
    view.unmount();
    getReferences.mockRestore();
    listArtifactVersions.mockRestore();
  }
});

test("ArtifactDetails links each inbound source to the record that cites it", async () => {
  const listArtifactVersions = serveVersions([artifact]);
  const getReferences = spyOn(api, "getReferences").mockResolvedValue(
    referencesOf(
      [
        {
          created_at: "2026-09-09T01:00:00Z",
          direction: "in",
          excerpt: { text: "See dispatch://CORE-1/artifact/diagram-png before deciding." },
          kind: "mentions",
          node: graphNode({
            id: "comment-1",
            issue_key: "CORE-1",
            kind: "comment",
            project: "CORE",
            ref: "dispatch://CORE-1/comment/comment-1",
          }),
          source_seq: 1,
        },
        {
          created_at: "2026-09-09T00:30:00Z",
          direction: "in",
          excerpt: { text: "Source artifact" },
          kind: "mentions",
          node: graphNode({
            id: "artifact-source",
            issue_key: "CORE-1",
            kind: "artifact",
            project: "CORE",
            ref: "dispatch://CORE-1/artifact/source-doc",
          }),
          source_seq: 2,
        },
      ],
      graphNode({
        id: "artifact-1",
        issue_key: "CORE-1",
        kind: "artifact",
        project: "CORE",
        ref: "dispatch://CORE-1/artifact/diagram-png",
      })
    )
  );

  try {
    const view = renderDetails(artifact);

    expect(
      (await screen.findByRole("link", { name: "Comment · CORE-1" })).getAttribute("href")
    ).toBe("/issues/CORE-1/comments/comment-1");
    // A citing document navigates to that document, never to its owning issue.
    expect(screen.getByRole("link", { name: "Artifact · CORE-1" }).getAttribute("href")).toBe(
      "/issues/CORE-1/artifacts/source-doc"
    );
    view.unmount();
  } finally {
    getReferences.mockRestore();
    listArtifactVersions.mockRestore();
  }
});

test("ArtifactDetails groups a source's edges into one card and expands its clamped excerpt", async () => {
  const user = userEvent.setup();
  const long = `${"A cited passage that runs well past the clamp. ".repeat(8)}End of block.`;
  const listArtifactVersions = serveVersions([artifact]);
  const getReferences = spyOn(api, "getReferences").mockResolvedValue(
    referencesOf(
      [
        {
          created_at: "2026-09-09T01:00:00Z",
          direction: "in",
          excerpt: { block_id: "b-1", text: long },
          kind: "mentions",
          node: graphNode({
            id: "artifact-source",
            issue_key: "CORE-1",
            kind: "artifact",
            project: "CORE",
            ref: "dispatch://CORE-1/artifact/source-doc",
          }),
          source_seq: 3,
        },
        {
          created_at: "2026-09-09T00:30:00Z",
          direction: "in",
          excerpt: { text: "spec.md" },
          kind: "anchored_to",
          node: graphNode({
            id: "artifact-source",
            issue_key: "CORE-1",
            kind: "artifact",
            project: "CORE",
            ref: "dispatch://CORE-1/artifact/source-doc",
          }),
          source_seq: null,
        },
      ],
      graphNode({
        id: "artifact-1",
        issue_key: "CORE-1",
        kind: "artifact",
        project: "CORE",
        ref: "dispatch://CORE-1/artifact/diagram-png",
      })
    )
  );

  try {
    const view = renderDetails(artifact);

    const references = await screen.findByRole("region", { name: "Referenced by" });
    // One source, two edges: one card carrying both, not two cards repeating the source.
    expect(within(references).getAllByRole("link", { name: "Artifact · CORE-1" })).toHaveLength(1);
    expect(within(references).getByText("Anchored item")).toBeTruthy();

    const excerpt = within(references).getByText(long);
    expect(excerpt.className).toContain("line-clamp-3");
    await user.click(within(references).getByRole("button", { name: "Show more" }));
    expect(within(references).getByText(long).className).not.toContain("line-clamp-3");
    expect(within(references).getByRole("button", { name: "Show less" })).toBeTruthy();
    view.unmount();
  } finally {
    getReferences.mockRestore();
    listArtifactVersions.mockRestore();
  }
});

test("ArtifactDetails fetches and renders a document diff only after the reader opens the comparison", async () => {
  const user = userEvent.setup();
  const doc: DocumentFixture = {
    ...artifact,
    id: "artifact-doc",
    kind: "doc",
    name: "spec.md",
    slug: "spec-md",
    versions: artifact.versions.map(
      ({ mime: _mime, sha256: _sha256, size: _size, ...version }) => version
    ),
  };
  const getArtifact = spyOn(api, "getArtifact").mockResolvedValue(documentRead(doc));
  const getReferences = noReferences(doc);
  const listArtifactVersions = serveVersions([doc]);
  const getArtifactVersion = spyOn(api, "getArtifactVersion").mockImplementation(
    async (_id, version) => ({
      authors: [],
      created_at: "2026-09-14T00:00:00Z",
      markdown: version === 1 ? "Before\n" : "After\n",
      named: false,
      number: version,
      summary: null,
    })
  );

  try {
    const view = renderDetails(doc);
    await screen.findByRole("button", { name: "Show comparison" });
    expect(getArtifactVersion).not.toHaveBeenCalled();
    expect(screen.queryByLabelText("Compare spec.md from")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Show comparison" }));
    expect(await screen.findByLabelText("Compare spec.md from")).not.toBeNull();
    await screen.findByText("After", { exact: false });
    expect(getArtifactVersion).toHaveBeenCalledTimes(2);

    await user.click(screen.getByRole("button", { name: "Hide comparison" }));
    expect(screen.queryByLabelText("Compare spec.md from")).not.toBeInTheDocument();
    view.unmount();
  } finally {
    getArtifact.mockRestore();
    getReferences.mockRestore();
    getArtifactVersion.mockRestore();
    listArtifactVersions.mockRestore();
  }
});

// A block id is a uuid only by default: a markdown author sets their own with `:::ask{#id}`, so
// one carrying a space or a percent reaches the deep link. Every other `#b-` producer encodes it
// and `ProofDocument` decodes it, so an unencoded fragment misses the block or throws there.
test("ArtifactDetails encodes an author-set block id in the source deep link", async () => {
  const listArtifactVersions = serveVersions([artifact]);
  const getReferences = spyOn(api, "getReferences").mockResolvedValue(
    referencesOf(
      [
        {
          created_at: "2026-09-09T01:00:00Z",
          direction: "in",
          excerpt: { block_id: "two words%", text: "A decision block cites this." },
          kind: "mentions",
          node: graphNode({
            id: "artifact-source",
            issue_key: "CORE-1",
            kind: "artifact",
            project: "CORE",
            ref: "dispatch://CORE-1/artifact/source-doc",
          }),
          source_seq: 3,
        },
      ],
      graphNode({
        id: "artifact-1",
        issue_key: "CORE-1",
        kind: "artifact",
        project: "CORE",
        ref: "dispatch://CORE-1/artifact/diagram-png",
      })
    )
  );

  try {
    const view = renderDetails(artifact);
    const references = await screen.findByRole("region", { name: "Referenced by" });
    const source = within(references).getByRole("link", { name: "Artifact · CORE-1" });
    expect(source.getAttribute("href")).toContain("#b-two%20words%25");
    view.unmount();
  } finally {
    getReferences.mockRestore();
    listArtifactVersions.mockRestore();
  }
});
