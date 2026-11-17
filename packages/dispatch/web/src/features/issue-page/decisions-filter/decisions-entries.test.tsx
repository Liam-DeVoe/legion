import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";

import { commentDeliveryFields } from "../../../__tests__/comment-fixture";
import { api } from "../../../api/client";
import type { Actor, ArtifactBlock, Comment, Decision, IssueDecisions } from "../../../api/types";
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
  READER_ACTOR,
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

/** The seam's parts for the issue whose tree holds `children`, with `decisions`, `asks`,
 *  `comments` and the decisions document's `blocks` as the cache holds them. */
async function entriesOf(
  children: readonly TreeNode[],
  decisions: IssueDecisions,
  asks: readonly DecisionsAsk[] = [],
  comments: readonly Comment[] = [],
  blocks: readonly ArtifactBlock[] = []
): Promise<DecisionsParts> {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  client.setQueryData(["decisions", KEY], decisions);
  client.setQueryData(["asks", KEY], asks);
  client.setQueryData(["comments", KEY], comments);
  // The issue names its decisions document by its role (`issueDetails`), so the seam reads that
  // document's blocks, as the tree does.
  client.setQueryData(["artifact", DECISIONS_DOCUMENT.id, "blocks"], blocks);
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

/** The decisions document's blocks as the blocks read answers them, in document order: each
 *  decision's heading, whose block is the decision's own (`Decision.block_id`), then one block
 *  for each paragraph of its words, each at the place it holds in the document's markdown. */
function documentBlocks(made: readonly Decision[]): ArtifactBlock[] {
  const blocks: ArtifactBlock[] = [];
  let at = 0;
  const add = (id: string, type: string, markdown: string) => {
    blocks.push({
      attributes: [],
      from: at,
      id,
      references: { asks: 0, comments: 0 },
      to: at + markdown.length,
      token: id,
      type,
    });
    // A blank line stands between two blocks of the markdown.
    at += markdown.length + 2;
  };
  for (const one of made) {
    add(one.block_id, "heading", `## ${one.heading}`);
    one.text.split("\n\n").forEach((paragraph, index) => {
      add(`${one.block_id}-${index + 1}`, "paragraph", paragraph);
    });
  }
  return blocks;
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
// stands first, which neither the reader's entries first nor a walk level by level gives. The
// decisions document's blocks place a Change (decision 110 (1) of dispatch://INFRA-1196): two
// decisions that wait open with the same words, and the reader's Change was written in the
// second one's section, so the second leaves the review list for her decisions and the first
// still waits. The quote alone would put the Change on the first.
test("the seam gives four parts from today's reads and makes no request", async () => {
  const tie = "2026-11-11T09:00:00Z";
  // Two decisions that wait, each opening with the same paragraph.
  const opening = "The rail stays 56 px wide.";
  const reviewWords = `${opening}\n\nThe outline needs the room.`;
  const askedWords = `${opening}\n\nA phone draws it over the body.`;
  const children = [
    statement("s1", {
      children: [answeredAsk("a-tie-bob", { at: tie, by: BOB, text: "Bob's, at that moment." })],
    }),
    openAsk("q-open"),
    answeredAsk("a-early", { at: "2026-11-12T09:00:00Z", text: "My first." }),
    answeredAsk("a-bob", { at: "2026-11-13T09:00:00Z", by: BOB, text: "Bob's." }),
    answeredAsk("a-late", { at: "2026-11-14T09:00:00Z", text: "My second." }),
    answeredAsk("a-tie-mine", { at: tie, text: "Mine, at that moment." }),
    decisionNode("review", { text: reviewWords }),
    decisionNode("asked", { text: askedWords }),
    decisionNode("minor"),
  ];
  const made = [
    decision("review", { links: [shaped(specRef("s1"))], text: reviewWords }),
    decision("asked", { links: [shaped(specRef("s1"))], text: askedWords }),
    decision("minor"),
  ];
  // The reader's Change on the second decision, made after both last changed: its anchor
  // quotes the decision's first paragraph, as the page's Change does, which the first decision
  // holds too, and names that paragraph's block.
  const change: Comment = {
    ...commentDeliveryFields(),
    anchor: {
      artifact_id: DECISIONS_DOCUMENT.id,
      block_id: "asked-1",
      mark_id: "mark-asked",
      orphaned: false,
      quote: opening,
      version: 1,
    },
    ask_id: null,
    author: READER_ACTOR,
    body: "Draw it under the body on a phone.",
    created_at: "2026-11-15T09:00:00Z",
    edited_at: null,
    id: "change-asked",
    issue_key: KEY,
    reply_to: null,
    resolved: false,
    resolved_at: null,
    resolved_by: null,
    suggestion: null,
    turn: null,
  };
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
    const parts = await entriesOf(
      children,
      decisionsRead(made),
      asks,
      [change],
      documentBlocks(made)
    );
    const ids = idsOf(parts);
    expect(ids.yours).toEqual([
      D("asked"),
      "a-late",
      "a-bob",
      "a-early",
      "a-tie-bob",
      "a-tie-mine",
    ]);
    expect(ids.others).toEqual([]);
    expect(ids.open_questions).toEqual(["q-open"]);
    expect(ids.review).toEqual([D("review")]);
    expect(ids.agents).toEqual([D("minor")]);
    expect(ids.named).toEqual([]);
    // The second decision carries the reader's Change, and the first, whose words hold the same
    // quote, carries none.
    expect(parts.yours[0]).toMatchObject({
      change: { id: change.id },
      kind: "decision",
      standing: "change_requested",
    });
    expect(parts.review[0]).not.toHaveProperty("change");

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
