import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";

import { api } from "../../../api/client";
import type { Actor, IssueDecisions } from "../../../api/types";
import type { TreeNode } from "../issue-tree";
import { presetView } from "../presets";
import { useDecisionsEntries } from "./decisions-entries";
import {
  answeredAsk,
  closing,
  DECISIONS_DOCUMENT,
  decision,
  decisionNode,
  decisionsRead,
  issueDetails,
  issueTree,
  KEY,
  openAsk,
  READER,
  shaped,
  specRef,
  statement,
} from "./decisions-fixtures";
import type { DecisionsAsk, DecisionsParts } from "./decisions-parts";

/**
 * The seam between today's reads and the Decisions view (dispatch://INFRA-3937 R8). Each case
 * puts the three reads and the decisions document's blocks in the cache under the keys the
 * page's tree reads them by, as the cache holds them once the tree is drawn, and no query reads
 * again on mount, so any request the seam made would be one of its own. The view is the one the
 * slot passes: the tree as the Decisions view leaves it (`presetView`).
 */

const BOB: Actor = { id: "bob", kind: "user" };

/** The seam's parts for the issue whose tree holds `children`, with `decisions` and `asks` as
 *  the cache holds them, no comment and no block of the decisions document. */
async function entriesOf(
  children: readonly TreeNode[],
  decisions: IssueDecisions,
  asks: readonly DecisionsAsk[] = []
): Promise<DecisionsParts> {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  client.setQueryData(["decisions", KEY], decisions);
  client.setQueryData(["asks", KEY], asks);
  client.setQueryData(["comments", KEY], []);
  // The issue names its decisions document by its role (`issueDetails`), so the seam reads that
  // document's blocks, as the tree does.
  client.setQueryData(["artifact", DECISIONS_DOCUMENT.id, "blocks"], []);
  const issue = issueDetails();
  const root = issueTree(children);
  const view = presetView(root, "decisions", "").root;
  const wrapper = ({ children: inside }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{inside}</QueryClientProvider>
  );
  const hook = renderHook(() => useDecisionsEntries({ issue, reader: READER, root, view }), {
    wrapper,
  });
  // A read the mount started has had its turn before the parts are read.
  await act(() => Promise.resolve());
  const parts = hook.result.current;
  hook.unmount();
  return parts;
}

/** The node ids of each part, in the order the part draws them. */
function idsOf(parts: DecisionsParts): Record<keyof DecisionsParts, string[]> {
  const ids = (entries: readonly { readonly node: TreeNode }[]) =>
    entries.map((entry) => entry.node.id);
  return {
    agents: ids(parts.agents),
    named: ids(parts.named),
    open_questions: ids(parts.open_questions),
    others: ids(parts.others),
    review: ids(parts.review),
    yours: ids(parts.yours),
  };
}

const D = (block: string) => decisionNode(block).id;

// R8: the seam gives the view's four parts from the reads the page's tree already made. Bob's
// answers stand under the reader's decisions, newest first among the reader's own. Two answers
// of one moment stand in the order their nodes first stand in the tree, read depth first: Bob's
// is under the statement at the tree's top and the reader's is the issue's last ask, so Bob's
// stands first, which neither the reader's entries first nor a walk level by level gives.
test("the seam gives four parts from today's reads and makes no request", async () => {
  const tie = "2026-11-11T09:00:00Z";
  const children = [
    statement("s1", {
      children: [answeredAsk("a-tie-bob", { at: tie, by: BOB, text: "Bob's, at that moment." })],
    }),
    openAsk("q-open"),
    answeredAsk("a-early", { at: "2026-11-12T09:00:00Z", text: "My first." }),
    answeredAsk("a-bob", { at: "2026-11-13T09:00:00Z", by: BOB, text: "Bob's." }),
    answeredAsk("a-late", { at: "2026-11-14T09:00:00Z", text: "My second." }),
    answeredAsk("a-tie-mine", { at: tie, text: "Mine, at that moment." }),
    decisionNode("review"),
    decisionNode("minor"),
  ];
  const made = [decision("review", { links: [shaped(specRef("s1"))] }), decision("minor")];
  const asks = [
    closing("a-tie-bob", { actor: BOB, at: tie, kind: "answered" }),
    closing("a-early", { at: "2026-11-12T09:00:00Z", kind: "answered" }),
    closing("a-bob", { actor: BOB, at: "2026-11-13T09:00:00Z", kind: "answered" }),
    closing("a-late", { at: "2026-11-14T09:00:00Z", kind: "answered" }),
    closing("a-tie-mine", { at: tie, kind: "answered" }),
  ];
  const getIssueDecisions = spyOn(api, "getIssueDecisions");
  const listIssueAsks = spyOn(api, "listIssueAsks");
  const listComments = spyOn(api, "listComments");
  // Any request at all, past the reads the page's tree makes.
  const request = spyOn(globalThis, "fetch");
  try {
    const ids = idsOf(await entriesOf(children, decisionsRead(made), asks));
    expect(ids.yours).toEqual(["a-late", "a-bob", "a-early", "a-tie-bob", "a-tie-mine"]);
    expect(ids.others).toEqual([]);
    expect(ids.open_questions).toEqual(["q-open"]);
    expect(ids.review).toEqual([D("review")]);
    expect(ids.agents).toEqual([D("minor")]);
    expect(ids.named).toEqual([]);

    expect(getIssueDecisions).not.toHaveBeenCalled();
    expect(listIssueAsks).not.toHaveBeenCalled();
    expect(listComments).not.toHaveBeenCalled();
    expect(request).not.toHaveBeenCalled();
  } finally {
    getIssueDecisions.mockRestore();
    listIssueAsks.mockRestore();
    listComments.mockRestore();
    request.mockRestore();
  }
});

// R8: a decisions read that holds no decision, or none of its `decisions` member, gives no
// decision and no error, and the view draws the questions alone. The tree is the one the page
// builds from that same read, which holds no decision node (`tree-drafts.ts`'s `addDecisions`).
test("an empty decisions member gives the questions alone and no error", async () => {
  const children = [statement("s1"), openAsk("q-open")];
  // A read with no `decisions` member, which its type does not allow and an answer can still be.
  const { document, open_questions } = decisionsRead([]);
  const missing = { document, open_questions } as unknown as IssueDecisions;
  for (const [read, decisions] of [
    ["an empty member", decisionsRead([])],
    ["no member", missing],
  ] as const) {
    const drawn = entriesOf(children, decisions);
    // The hook throws nothing on this read: the promise of its parts settles with them.
    await expect(drawn).resolves.toBeDefined();
    expect({ parts: idsOf(await drawn), read }).toEqual({
      parts: {
        agents: [],
        named: [],
        open_questions: ["q-open"],
        others: [],
        review: [],
        yours: [],
      },
      read,
    });
  }
});
