import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import type { Artifact, ArtifactBlock, Decision, IssueDetails } from "../../api/types";
import { MarginProvider } from "../margin/margin-context";
import { buildDispatchReference } from "../refs/routes";
import { KeymapProvider } from "../shell/KeymapProvider";
import { useKeymapScope } from "../shell/keymap";
import { IssueThreeThings } from "./IssueThreeThings";
import { IssueTree } from "./IssueTree";
import { buildIssueTree, type TreeNode } from "./issue-tree";
import { EMPTY_STATE_LINES } from "./page-words";
import { specSummary } from "./spec-summary";
import { blockNodeId } from "./tree-drafts";

/**
 * The head of the page draws the issue in plain language: at most three bullets, each one spoken
 * sentence of the spec's summary (dispatch://INFRA-1196 R5), out of the summary node's own text,
 * which the tree the page already holds carries - the head reads no document of its own (R20).
 *
 * Every row below renders the head with no `QueryClientProvider` around it, which is the whole
 * proof of R20 here: a component that read anything would throw for want of a client, so these
 * rows pass only while the head makes no read at all.
 */

/** The spec's summary node, as the tree holds it: a `callout` whose text is the block's source,
 *  fence and all. `cut` leaves the closing fence off, which is how a summary longer than the
 *  server's excerpt arrives: cut at `BlockExcerptRunes` wherever that falls. */
function summaryNode(text: string, cut = false): TreeNode {
  return {
    alsoUnder: [],
    children: [],
    chips: [],
    facts: [],
    gaps: [],
    held: 0,
    id: "spec#b-summary",
    question: false,
    replaced: false,
    text: `:::callout{#summary title="What this issue is"}\n${text}${cut ? "" : "\n:::"}`,
    transcluded: false,
    type: "callout",
    waiting: false,
  };
}

/** The sentences the head draws, in order, and the label before each. `reading` is whether the
 *  reads that carry the summary are still out. */
function bullets(
  summary: TreeNode | undefined,
  reading = false,
  look: "contents" | "spec" = "contents"
): { labels: string[]; lines: string[]; unmount: () => void } {
  const view = render(
    <MemoryRouter initialEntries={["/issues/CORE-1"]}>
      <IssueThreeThings look={look} reading={reading} summary={summary} />
    </MemoryRouter>
  );
  const items = within(screen.getByTestId("issue-three-things")).queryAllByRole("listitem");
  return {
    labels: items.map((item) => item.querySelector("[data-page-chrome]")?.textContent ?? ""),
    lines: items.map((item) => item.querySelector("[data-author-text]")?.textContent ?? ""),
    unmount: view.unmount,
  };
}

// The architect's ruling 5: each sentence stands after its label, in the order the summary
// holds them, in the Contents slot (the pane and the Outline sheet) and the Spec view's first
// group alike; and the two rows the spec named under them are drawn nowhere.
test("each sentence stands after its label, and the two rows are not drawn", () => {
  for (const look of ["contents", "spec"] as const) {
    const { labels, lines, unmount } = bullets(
      summaryNode("One body. Every property shows. The reasoning is one press away."),
      false,
      look
    );
    try {
      expect(labels).toEqual(["What it is:", "Why it matters:", "Done when:"]);
      expect(lines).toEqual([
        "One body.",
        "Every property shows.",
        "The reasoning is one press away.",
      ]);
      const head = screen.getByTestId("issue-three-things");
      expect(head.textContent).not.toContain("How it gets done");
      expect(head.textContent).not.toContain("What you will be shown");
      expect(head.querySelector("a")).toBeNull();
    } finally {
      unmount();
    }
  }
});

test("a summary of four sentences draws the first three of them", () => {
  const { lines, unmount } = bullets(
    summaryNode(
      "The issue page draws one ordered body. Every property is visible at every width. " +
        "The reasoning behind each requirement is one press away. Nothing the page drew is lost."
    )
  );
  try {
    expect(lines).toEqual([
      "The issue page draws one ordered body.",
      "Every property is visible at every width.",
      "The reasoning behind each requirement is one press away.",
    ]);
  } finally {
    unmount();
  }
});

// An author who wrote two sentences gets two: no third bullet, and nothing drawn in its place.
test("a summary of two sentences draws two bullets and says nothing about the third", () => {
  const { lines, unmount } = bullets(
    summaryNode("The page is one outline. It opens folded to the work in front of the reader.")
  );
  try {
    expect(lines).toEqual([
      "The page is one outline.",
      "It opens folded to the work in front of the reader.",
    ]);
    expect(screen.getByTestId("issue-three-things").textContent).not.toContain(
      EMPTY_STATE_LINES.spec_summary
    );
  } finally {
    unmount();
  }
});

// The node's text is the server's excerpt, cut at 480 runes, so a longer summary arrives ending
// mid-sentence. That tail is words nobody spoke as a sentence, so it is not drawn.
test("a summary cut mid-sentence draws its whole sentences and not the fragment", () => {
  const { lines, unmount } = bullets(
    summaryNode("The page is one outline. The reasoning behind each requirement is reach")
  );
  try {
    expect(lines).toEqual(["The page is one outline."]);
  } finally {
    unmount();
  }
});

// A summary cut inside its first sentence holds words and no whole sentence of them. That is a
// spec with a summary, so the head says why it draws none rather than that there is none; and a
// whole summary whose words end in no sentence end says that instead, since it is not too long.
test("a summary with no whole sentence says why it draws none, not that there is none", () => {
  for (const [summary, line] of [
    [
      summaryNode("The reasoning behind each requirement is reached from the node it", true),
      EMPTY_STATE_LINES.spec_summary_cut,
    ],
    [summaryNode("The page is one outline"), EMPTY_STATE_LINES.spec_summary_unended],
  ] as const) {
    const { lines, unmount } = bullets(summary);
    try {
      expect(lines).toEqual([]);
      const head = screen.getByTestId("issue-three-things").textContent;
      expect(head).not.toContain("This spec has no summary.");
      expect(head).toContain(line);
    } finally {
      unmount();
    }
  }
  expect(EMPTY_STATE_LINES.spec_summary_cut).toBe("The summary is too long to show here.");
});

// A summary may say one sentence twice. Each is a bullet of its own, and React is told the two
// apart by their place: keyed by its words alone, the two bullets shared one key.
test("a summary that says one sentence twice draws both, each under a key of its own", () => {
  const errors = spyOn(console, "error").mockImplementation(() => {});
  try {
    const { lines, unmount } = bullets(
      summaryNode("The page is one outline. The page is one outline. It fits a phone.")
    );
    try {
      expect(lines).toEqual([
        "The page is one outline.",
        "The page is one outline.",
        "It fits a phone.",
      ]);
      expect(errors.mock.calls.filter((call) => String(call[0]).includes("same key"))).toEqual([]);
    } finally {
      unmount();
    }
  } finally {
    errors.mockRestore();
  }
});

// A spec that carries no summary node at all still draws its row in that same place, saying so.
test("a spec with no summary draws the page's own line and no bullet", () => {
  const { lines, unmount } = bullets(undefined);
  try {
    expect(lines).toEqual([]);
    expect(screen.getByTestId("issue-three-things").textContent).toContain(
      EMPTY_STATE_LINES.spec_summary
    );
  } finally {
    unmount();
  }
});

// The page's first load draws the tree before the spec's blocks and their words arrive, so the
// summary node is missing, or there with no words yet. That is no spec without a summary, and
// the head says nothing about one until the reads that carry it have answered.
test("while the reads that carry the summary are out, the head says nothing about it", () => {
  for (const summary of [undefined, { ...summaryNode(""), text: "" }]) {
    const { lines, unmount } = bullets(summary, true);
    try {
      expect(lines).toEqual([]);
      expect(screen.getByTestId("issue-three-things").textContent).not.toContain(
        EMPTY_STATE_LINES.spec_summary
      );
    } finally {
      unmount();
    }
  }
});

// A summary written as bullets is read as the sentences its items hold, each item one bullet
// of the head whether or not its author ended it with a full stop: the item's own end is where
// it stops, and the marker in front of it is the list's syntax, not the author's words.
test("a summary written as bullets draws each item as a sentence", () => {
  const { lines, unmount } = bullets(
    summaryNode(
      "- What: the page is one outline\n- Why: a reader finds the work\n- Done: it fits a phone"
    )
  );
  try {
    expect(lines).toEqual([
      "What: the page is one outline",
      "Why: a reader finds the work",
      "Done: it fits a phone",
    ]);
  } finally {
    unmount();
  }
});

// A sentence in bold still ends at its full stop: the asterisks after it are the emphasis, and
// they are neither the sentence's end nor words of it.
test("a summary in bold, or ending in a bold sentence, draws that sentence", () => {
  const whole = bullets(summaryNode("**The page is one outline.**"));
  try {
    expect(whole.lines).toEqual(["The page is one outline."]);
  } finally {
    whole.unmount();
  }
  const last = bullets(
    summaryNode("The page is one outline. Nothing is lost. **It fits a phone.**")
  );
  try {
    expect(last.lines).toEqual([
      "The page is one outline.",
      "Nothing is lost.",
      "It fits a phone.",
    ]);
  } finally {
    last.unmount();
  }
});

// The excerpt is cut wherever `BlockExcerptRunes` falls, which can be inside the last item of
// a list as well as inside a sentence; the words after the cut are no item anybody wrote.
test("a bulleted summary cut inside its last item draws the whole items and not the fragment", () => {
  const { lines, unmount } = bullets(
    summaryNode(
      "- What: the page is one outline\n- Why: a reader finds the work\n- Done: it fi",
      true
    )
  );
  try {
    expect(lines).toEqual(["What: the page is one outline", "Why: a reader finds the work"]);
  } finally {
    unmount();
  }
});

const KEY = "CORE-1";

const decisionsDocument: Artifact = {
  created_at: "2026-11-05T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  id: "artifact-decisions",
  issue_key: KEY,
  kind: "doc",
  name: "Decisions",
  primary: false,
  project: "CORE",
  ref_key: "artifact:artifact-decisions",
  session_id: null,
  slug: "decisions",
  versions: [],
};

const decision: Decision = {
  asks: [],
  block_id: "d1",
  changed_at: null,
  changed_version: 1,
  heading: "Ship behind a flag",
  kept: null,
  kind: "agent",
  links: [],
  ref: `dispatch://${KEY}/artifact/decisions#b-d1`,
  replaced: false,
  text: "## Ship behind a flag\n\nWe ship it behind a flag.",
  text_sha256: "sha256:one",
  unsure: false,
  waiting_review: false,
};

/** The issue the drawn-gap case reads: a spec with a summary and one requirement no check
 *  serves, and a decisions document with one decision, as the page's reads answer them. */
function fullyReadIssue(): TreeNode {
  const spec: Artifact = {
    ...decisionsDocument,
    id: "artifact-spec",
    name: "spec.md",
    primary: true,
    slug: "spec",
  };
  const details = {
    advances: [],
    advances_revision: 0,
    artifacts: [spec, decisionsDocument],
    assignee: null,
    blocked_by: [],
    children: [],
    claim: null,
    closed_at: null,
    components: { ids: [], inherited_from: null, mode: "inherit", reason: null, unknown: [] },
    created_at: "2026-11-05T00:00:00Z",
    created_by: { id: "alice", kind: "user" },
    decisions_artifact_id: decisionsDocument.id,
    design_gates: [],
    external_links: [],
    holder_state: null,
    key: KEY,
    labels: [],
    last_seen: null,
    last_seq: 1,
    number: 1,
    open_asks: [],
    parent: null,
    plan_artifact_id: "",
    primary_artifact_id: spec.id,
    priority: null,
    progress: { children: null, tasks: null },
    project: "CORE",
    rank: "U",
    referenced_by_count: 0,
    repo: null,
    resolved_repo: null,
    route: null,
    route_holder: null,
    route_status: null,
    route_status_reason: null,
    status: "todo",
    takeable_at: null,
    title: "The issue page draws one ordered body",
    updated_at: "2026-11-05T00:00:00Z",
    verification_artifact_id: "",
  } satisfies IssueDetails;
  const block = (id: string, type: string): ArtifactBlock => ({
    attributes: [],
    from: 0,
    id,
    references: { asks: 0, comments: 0 },
    to: 1,
    token: id,
    type,
  });
  return buildIssueTree({
    coverage: {
      gaps: ["r1 has no check"],
      has_plan: false,
      issue: KEY,
      requirements: [
        {
          block_id: "r1",
          checks: 0,
          gaps: ["r1 has no check"],
          plan_tasks: 0,
          ref: `dispatch://${KEY}/spec#b-r1`,
          text: "The page draws the whole issue in one reading.",
        },
      ],
      spec_artifact_id: spec.id,
    },
    decisions: {
      decisions: [decision],
      document: decisionsDocument.id,
      open_questions: [],
    },
    documents: [
      { artifactId: spec.id, blocks: [block("summary", "callout"), block("r1", "requirement")] },
    ],
    issue: details,
    targets: [
      {
        gone: false,
        kind: "block",
        name: ":::callout{#summary}\nWhat this is. Why it matters. What is different when done.\n:::",
        ref: buildDispatchReference({
          block: { id: "summary" },
          key: KEY,
          kind: "artifact",
          slug: "spec",
        }),
      },
    ],
  });
}

/** happy-dom answers every media query against its own 1024 px window. The case below reads
 *  the outline at 1440 px: a `max-width` in px, a `min-width` in rem, and `not all and` either. */
function setViewport(width: number): void {
  window.matchMedia = ((query: string) => {
    const max = /max-width:\s*([\d.]+)px/.exec(query)?.[1];
    const min = /min-width:\s*([\d.]+)rem/.exec(query)?.[1];
    const matched =
      max !== undefined ? width <= Number(max) : min !== undefined && width >= Number(min) * 16;
    return {
      addEventListener: () => {},
      addListener: () => {},
      dispatchEvent: () => true,
      matches: query.startsWith("not all and ") ? !matched : matched,
      media: query,
      onchange: null,
      removeEventListener: () => {},
      removeListener: () => {},
    } as MediaQueryList;
  }) as typeof window.matchMedia;
}

const originalMatchMedia = window.matchMedia;
afterEach(() => {
  window.matchMedia = originalMatchMedia;
});

/** The page's own `issue` scope, which every one of the outline's keys is registered in. */
function IssueScope(): ReactNode {
  useKeymapScope("issue");
  return null;
}

/** Every row the outline drew for `nodeId`, in the order it drew them. */
function rowsFor(container: HTMLElement, nodeId: string): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>(`[data-node-row][data-node-id="${nodeId}"]`)];
}

// R10, R22 and the person's ruling on helper text: a gap is drawn only where it tells a node
// apart from its siblings and names work somebody owes. Under today's model every requirement
// and every decision lacks an origin, and no issue records who wrote or confirmed its summary,
// so on a fully read issue the requirement, the decision and the head draw none of those three -
// while the requirement still draws the gap that does tell it apart: no check serves it. The
// head is drawn with no `QueryClientProvider` around it, as every case above draws it (R20).
test("a requirement, a decision and the head of a fully read issue draw no gap their whole kind shares", async () => {
  setViewport(1440);
  const root = fullyReadIssue();
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <KeymapProvider>
          <MarginProvider>
            <IssueScope />
            <IssueTree
              artifacts={new Map([[decisionsDocument.slug, decisionsDocument]])}
              context={new Set()}
              emptyLine="Nothing here."
              filtering={false}
              focusReveal={false}
              holdsEscape={false}
              isClosed={false}
              onEditing={() => {}}
              reveal={undefined}
              root={root}
              rootRow
            />
          </MarginProvider>
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  const head = render(
    <MemoryRouter>
      <IssueThreeThings look="contents" reading={false} summary={specSummary(root, "spec")} />
    </MemoryRouter>
  );
  try {
    const requirement = blockNodeId("spec", "r1");
    const settled = blockNodeId("decisions", "d1");
    await waitFor(() => expect(rowsFor(view.container, requirement).length).toBe(1));
    expect(rowsFor(view.container, settled).length).toBe(1);
    const kinds = (element: Element | undefined) =>
      [...(element?.querySelectorAll("[data-gap]") ?? [])].map((gap) =>
        gap.getAttribute("data-gap-kind")
      );
    expect(kinds(rowsFor(view.container, requirement)[0])).toEqual(["no_check"]);
    expect(kinds(rowsFor(view.container, settled)[0])).toEqual([]);
    expect(head.container.querySelector("li [data-author-text]")?.textContent).toBe(
      "What this is."
    );
    expect(kinds(head.container)).toEqual([]);
    for (const words of ["No source given", "Not confirmed in Dispatch", "No author in Dispatch"]) {
      expect(view.container.textContent).not.toContain(words);
      expect(head.container.textContent).not.toContain(words);
    }
  } finally {
    head.unmount();
    view.unmount();
  }
});
