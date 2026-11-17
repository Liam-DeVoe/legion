import { expect, type Mock, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";

import { api } from "../../api/client";
import type {
  Artifact,
  Comment,
  DisplayTree,
  IssueDetails,
  ReferenceTarget,
} from "../../api/types";
import type { TreeNode } from "./issue-tree";
import { NODE_TYPE_NAMES, PRESET_NAMES } from "./page-words";
import { presetView } from "./presets";
import {
  ReferenceNamesProvider,
  useKnownAmbiguousReferences,
  useKnownReferenceName,
} from "./reference-names";
import { nodesInOrder } from "./tree-keys";
import {
  issueTreeKey,
  subscribeTreeFreshness,
  type TreeAnswer,
  type TreeAnswerNode,
  type TreeTuple,
  viewKey,
} from "./tree-read/tree-read";
import { useIssueTree } from "./useIssueTree";

/**
 * The issue page's tree as the hook reads it (dispatch://INFRA-4062 R1, R4, R5): from the tree
 * read, with no document's blocks read, the filters' held view reads on its root, and the names
 * of what it holds read last and all at once. The batched name read is keyed by the set of
 * addresses the tree holds, so one comment, message, ask or block arriving is a key nobody has
 * an answer for: without the previous answer standing while the new one loads, every chip falls
 * back to its raw address, on a page that was already drawn.
 */

const KEY = "CORE-1";
const PLAN = "plan-md";
const TASK_REF = `dispatch://${KEY}/artifact/${PLAN}#b-t1`;
const TASK_WORDS = "Draw the tree's names from one read.";

const plan: Artifact = {
  created_at: "2026-11-05T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  id: "artifact-plan",
  issue_key: KEY,
  kind: "doc",
  name: "Plan",
  primary: false,
  project: "CORE",
  ref_key: "artifact:artifact-plan",
  role: "plan",
  session_id: null,
  slug: PLAN,
  versions: [],
};

/** One node of the tree read's answer, under the issue unless the case gives it a parent. */
function answerNode(
  id: string,
  supertag: string,
  extra: Partial<TreeAnswerNode> = {}
): TreeAnswerNode {
  return {
    attributes: {},
    author: null,
    id,
    made_at: "2026-11-05T00:00:00Z",
    parent: KEY,
    rank: null,
    state: "live",
    supertag,
    text: "",
    visibility: "project",
    ...extra,
  };
}

/** The plan's one task, as the tree read answers it: a typed block of the plan, with its words. */
const task = answerNode("t1", "plan_task", {
  document: { artifact_id: plan.id, position: 0, slug: PLAN },
  text: TASK_WORDS,
});

/** The tree read's answer for the issue: the plan's task, each of `comments` under the issue, as
 *  the read places a thread a person wrote in, and whatever more nodes and tuples a case adds. */
function treeAnswer(
  comments: readonly Comment[],
  more: { readonly nodes?: readonly TreeAnswerNode[]; readonly tuples?: readonly TreeTuple[] } = {}
): TreeAnswer {
  return {
    converted: false,
    cursor: null,
    nodes: [
      answerNode(KEY, "issue", { parent: null }),
      task,
      ...comments.map((one) =>
        answerNode(one.id, "comment", {
          author: one.author,
          made_at: one.created_at,
          text: one.body,
        })
      ),
      ...(more.nodes ?? []),
    ],
    tree: null,
    tuples: [...(more.tuples ?? [])],
    view_of: null,
  };
}

const issue: IssueDetails = {
  advances: [],
  advances_revision: 0,
  artifacts: [plan],
  assignee: null,
  blocked_by: [],
  children: [],
  claim: null,
  closed_at: null,
  components: { ids: [], inherited_from: null, mode: "inherit", reason: null, unknown: [] },
  created_at: "2026-11-05T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  decisions_artifact_id: "",
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
  plan_artifact_id: plan.id,
  primary_artifact_id: "",
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
};

const comment: Comment = {
  anchor: null,
  answers: null,
  artifact_id: null,
  ask_id: null,
  author: { id: "alice", kind: "user" },
  body: "One more turn of the conversation.",
  created_at: "2026-11-05T01:00:00Z",
  deliveries: [],
  edited_at: null,
  id: "comment-1",
  issue_key: KEY,
  mentions: [],
  reply_block: null,
  reply_to: null,
  resolved: false,
  resolved_at: null,
  resolved_by: null,
  suggestion: null,
  turn: null,
};

/** A read the tree is built from, by what it reads: each is one the batched name read waits on. */
type TreeRead = "asks" | "comments" | "coverage" | "login" | "messages" | "results" | "tree";

/** Every read the hook makes, answered empty but for the tree read (`tree`, by default the
 *  plan's one task and `comments`), the reader's login and `comments`. The `held` read stays out
 *  until its `until` settles, and then answers as it would have. No document's blocks are read:
 *  that read answers nothing, and a case reads that it was never sent. The decisions read is no
 *  read of the hook's: every decision is a node of the tree read, so the decisions read refuses. */
function stubTreeReads(
  answerNames: (refs: readonly string[]) => Promise<{
    targets: ReferenceTarget[];
  }>,
  {
    comments = [],
    held,
    tree = treeAnswer(comments),
  }: {
    readonly comments?: readonly Comment[];
    readonly held?: { readonly read: TreeRead; readonly until: Promise<void> };
    readonly tree?: TreeAnswer;
  } = {}
) {
  const answered = (read: TreeRead): Promise<void> =>
    read === held?.read ? held.until : Promise.resolve();
  return [
    spyOn(api, "getIssueCoverage").mockImplementation(async () => {
      await answered("coverage");
      return { gaps: [], has_plan: true, issue: KEY, requirements: [], spec_artifact_id: "" };
    }),
    spyOn(api, "getIssueDecisions").mockRejectedValue(new Error("the hook read the decisions")),
    spyOn(api, "getIssueResults").mockImplementation(async () => {
      await answered("results");
      return { checks: [], issue: KEY, verification_artifact_id: "" };
    }),
    spyOn(api, "getIssueReferences").mockResolvedValue({ members: [], truncated: false }),
    spyOn(api, "listIssueAsks").mockImplementation(async () => {
      await answered("asks");
      return [];
    }),
    spyOn(api, "listComments").mockImplementation(async () => {
      await answered("comments");
      return [...comments];
    }),
    spyOn(api, "listIssueMessages").mockImplementation(async () => {
      await answered("messages");
      return [];
    }),
    spyOn(api, "getIssueTree").mockImplementation(async () => {
      await answered("tree");
      return tree;
    }),
    spyOn(api, "getArtifactBlocks").mockResolvedValue([]),
    spyOn(api, "whoAmI").mockImplementation(async () => {
      await answered("login");
      return { event_head: "0", kind: "user", login: "alice" };
    }),
    spyOn(api, "getReferenceTargets").mockImplementation(answerNames),
  ];
}

/** The task row's own words, which the tree read answers. */
function taskWords(root: TreeNode): string | undefined {
  return nodesInOrder(root).find((node) => node.id === `${PLAN}#b-t1`)?.text;
}

/** The issue the plan task needs, which the tree read holds no node for, so the batched name
 *  read alone names the task's chip for it. */
const NEEDED = "dispatch://CORE-7";

/** The name the task's chip for the issue it needs draws. */
function neededName(root: TreeNode): string | undefined {
  return nodesInOrder(root)
    .find((node) => node.id === `${PLAN}#b-t1`)
    ?.chips.find((chip) => chip.ref === NEEDED)?.name;
}

test("the names already in hand stand while a tree that grew reads the new set", async () => {
  // Every read after the first is held open: that window, where the page is drawn and the
  // names of its new set of addresses are not in yet, is what this test is about.
  const held: (() => void)[] = [];
  let reads = 0;
  const needs = { tuples: [{ from: "t1", kind: "needs", to: NEEDED }] };
  const stubs = stubTreeReads(
    async (refs) => {
      reads += 1;
      if (reads > 1) {
        const gate = Promise.withResolvers<void>();
        held.push(gate.resolve);
        await gate.promise;
      }
      return {
        targets: [...refs].map(
          (ref): ReferenceTarget =>
            ref === NEEDED
              ? { gone: false, kind: "issue", name: "The needed issue", ref }
              : { gone: false, kind: "block", name: `words of ${ref}`, ref }
        ),
      };
    },
    { tree: treeAnswer([], needs) }
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  try {
    const view = renderHook(() => useIssueTree(issue).root, { wrapper });
    await waitFor(() => expect(neededName(view.result.current)).toBe("The needed issue"));

    // A comment arrives with the tree read's next answer: the tree holds one address more, so
    // the names read is a key nobody has answered, and it is still out when the page draws again.
    await act(async () => {
      client.setQueryData(issueTreeKey(KEY), treeAnswer([comment], needs));
      await Promise.resolve();
    });
    await waitFor(() =>
      expect(nodesInOrder(view.result.current).some((node) => node.id === comment.id)).toBe(true)
    );
    expect(reads).toBe(2);
    expect(neededName(view.result.current)).toBe("The needed issue");
    // Nothing is left mounted when the held read lands, so its answer updates no state.
    view.unmount();
  } finally {
    for (const release of held) release();
    for (const stub of stubs) stub.mockRestore();
  }
});

/** What the batched name read answers for `refs`: the plan task's words for its address, and
 *  words of their own for every other. */
function namedTargets(refs: readonly string[]): { targets: ReferenceTarget[] } {
  return {
    targets: refs.map(
      (ref): ReferenceTarget => ({
        gone: false,
        kind: "block",
        name: ref === TASK_REF ? TASK_WORDS : `words of ${ref}`,
        ref,
      })
    ),
  };
}

/** One document of the issue, with whatever the case gives it: a role, or being the spec. */
function issueDocument(id: string, slug: string, extra: Partial<Artifact> = {}): Artifact {
  return {
    created_at: "2026-11-05T00:00:00Z",
    created_by: { id: "alice", kind: "user" },
    id,
    issue_key: KEY,
    kind: "doc",
    name: slug,
    primary: false,
    project: "CORE",
    ref_key: `artifact:${id}`,
    session_id: null,
    slug,
    versions: [],
    ...extra,
  };
}

/** Every read the tree is built from, in the order the test holds each one open. */
const TREE_READS: readonly TreeRead[] = [
  "comments",
  "asks",
  "coverage",
  "messages",
  "results",
  "tree",
  "login",
];

// The tester's F17: the names come last and all at once. While any one read of the tree is still
// out - the comments, the asks, the coverage, the messages, the results, the tree read or the
// reader's login - the addresses known so far are not read; once it answers, every address the
// tree holds - a block's own, and one a comment's words cite - is read in one request, and the
// page publishes the answer to every surface beside it (`reference-names.tsx`). The decisions are
// nodes of the tree read, so no decisions read is made.
test("The names are read in one batched request, after every other read has answered", async () => {
  const cited = "dispatch://CORE-7";
  for (const read of TREE_READS) {
    const asked: string[][] = [];
    const out = Promise.withResolvers<void>();
    const stubs = stubTreeReads(
      async (refs) => {
        asked.push([...refs]);
        return namedTargets(refs);
      },
      {
        comments: [{ ...comment, body: `Read ${cited} first.` }],
        held: { read, until: out.promise },
      }
    );
    // A client and a page of the turn's own, so no answer of an earlier turn is in hand.
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>
        <ReferenceNamesProvider>{children}</ReferenceNamesProvider>
      </QueryClientProvider>
    );
    try {
      const view = renderHook(
        () => ({
          // How many reads were out when the hook last drew.
          fetching: client.isFetching(),
          name: useKnownReferenceName(TASK_REF),
          root: useIssueTree(issue).root,
        }),
        { wrapper }
      );
      // The hook has drawn with every other read answered, the held one the only read still
      // out: no name is read yet.
      await waitFor(() => expect(view.result.current.fetching).toBe(1));
      expect(asked, `no names read while the ${read} read is out`).toEqual([]);

      await act(async () => {
        out.resolve();
        await out.promise;
      });
      // The read sends no reading of the task, so the name the page publishes for it is the
      // page's word for its kind, as a chip naming it draws (`targetNames`).
      await waitFor(() => expect(view.result.current.name).toBe(NODE_TYPE_NAMES.plan_task));
      expect(asked, `one names read after the ${read} read answers`).toHaveLength(1);
      expect(asked[0]).toContain(TASK_REF);
      expect(asked[0]).toContain(cited);
      expect(api.getIssueDecisions).not.toHaveBeenCalled();
      view.unmount();
    } finally {
      out.resolve();
      for (const stub of stubs) stub.mockRestore();
    }
  }
});

// dispatch://INFRA-1196 R9 and decision 55: the batched read resolves an id a comment writes
// short in the owner the reference names, and answers under the reference as the request wrote
// it. So the names read is asked for the short address itself, and the page publishes its answer
// under that address, which is the one a row links it by.
test("a short address a comment cites is read as written and named from that answer", async () => {
  const cited = `dispatch://${KEY}/comment/c0b271c7`;
  const asked: string[][] = [];
  const stubs = stubTreeReads(
    async (refs) => {
      asked.push([...refs]);
      return {
        targets: namedTargets(refs).targets.map((target) =>
          target.ref === cited
            ? { ...target, kind: "comment", name: "The comment its short id names" }
            : target
        ),
      };
    },
    { comments: [{ ...comment, body: `Read ${cited} first.` }] }
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ReferenceNamesProvider>{children}</ReferenceNamesProvider>
    </QueryClientProvider>
  );
  try {
    const view = renderHook(
      () => {
        useIssueTree(issue);
        return useKnownReferenceName(cited);
      },
      { wrapper }
    );
    await waitFor(() => expect(view.result.current).toBe("The comment its short id names"));
    expect(asked[0]).toContain(cited);
    view.unmount();
  } finally {
    for (const stub of stubs) stub.mockRestore();
  }
});

// dispatch://INFRA-1196 decisions 62 and 89: the batched read answers a short reference whose id
// two or more targets carry gone and ambiguous, and one whose target is gone gone alone. Neither
// has a name, so the page publishes none for either; it publishes the ambiguous one among the
// references it read as ambiguous, which a link draws as written and links nothing, and leaves
// the gone one out of them, which a link reads its own.
test("the page publishes an ambiguous reference a comment cites as ambiguous, a gone one not", async () => {
  const ambiguous = `dispatch://${KEY}/comment/c0b271c7`;
  const gone = `dispatch://${KEY}/comment/c0ffee00`;
  const stubs = stubTreeReads(
    async (refs) => ({
      targets: namedTargets(refs).targets.map((target): ReferenceTarget => {
        if (target.ref === ambiguous) {
          return { ...target, ambiguous: true, gone: true, kind: "comment", name: "" };
        }
        return target.ref === gone ? { ...target, gone: true, kind: "comment", name: "" } : target;
      }),
    }),
    { comments: [{ ...comment, body: `Read ${ambiguous} and ${gone} first.` }] }
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ReferenceNamesProvider>{children}</ReferenceNamesProvider>
    </QueryClientProvider>
  );
  try {
    const view = renderHook(
      () => {
        useIssueTree(issue);
        return {
          ambiguous: useKnownAmbiguousReferences(),
          names: [useKnownReferenceName(ambiguous), useKnownReferenceName(gone)],
          task: useKnownReferenceName(TASK_REF),
        };
      },
      { wrapper }
    );
    await waitFor(() => expect([...view.result.current.ambiguous]).toEqual([ambiguous]));
    // The read has answered and its names are published with the set: the task's among them,
    // and none for the ambiguous reference or the gone one.
    expect(view.result.current.task).toBe(NODE_TYPE_NAMES.plan_task);
    expect(view.result.current.names).toEqual([undefined, undefined]);
    view.unmount();
  } finally {
    for (const stub of stubs) stub.mockRestore();
  }
});

// R21: the batched read names an ask by its question as stored, which is markdown, and every
// link that reads the page's published names draws the name as it is, so the page publishes the
// question's plain words, the name a chip naming the ask draws (`targetNames`).
test("a published name of an ask is its question's plain words, never its markdown", async () => {
  const cited = `dispatch://${KEY}/ask/ask-9`;
  const stubs = stubTreeReads(
    async (refs) => ({
      targets: namedTargets(refs).targets.map((target) =>
        target.ref === cited
          ? {
              ...target,
              kind: "ask",
              name: "**Hold** the `deploy` until [the plan](https://example.com/plan) lands",
            }
          : target
      ),
    }),
    { comments: [{ ...comment, body: `Read ${cited} first.` }] }
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ReferenceNamesProvider>{children}</ReferenceNamesProvider>
    </QueryClientProvider>
  );
  try {
    const view = renderHook(
      () => {
        useIssueTree(issue);
        return useKnownReferenceName(cited);
      },
      { wrapper }
    );
    await waitFor(() => expect(view.result.current).toBe("Hold the deploy until the plan lands"));
    view.unmount();
  } finally {
    for (const stub of stubs) stub.mockRestore();
  }
});

/** A document of the issue no word of the tree cites, which only the plan task's stored source
 *  does: the batched name read answers that source, so the address is known only once it has. */
const notes = issueDocument("artifact-notes", "notes-md");
const NOTES_REF = `dispatch://${KEY}/artifact/notes-md`;

/** The issue with its notes document, one value across renders as the page's issue read hands
 *  it: a fresh issue every render would build the tree and publish its names on every commit. */
const withNotes: IssueDetails = { ...issue, artifacts: [plan, notes] };

/** What the batched name read answers for `refs` where the plan task's stored source cites the
 *  notes document, which the read names as a document, gone where `gone` says so. */
function citingTargets(refs: readonly string[], gone = false): { targets: ReferenceTarget[] } {
  return {
    targets: namedTargets(refs).targets.map((target) => {
      if (target.ref === TASK_REF) {
        return { ...target, name: `:::plan_task{#t1}\nThe words are in ${NOTES_REF}.\n:::` };
      }
      return target.ref === NOTES_REF
        ? { ...target, gone, kind: "artifact", name: "notes.md" }
        : target;
    }),
  };
}

// A plan task's stored source is what the names read answers for it, so a whole document of the
// issue that source cites, which a chip on the task opens (`addDocumentChips`), is in no address
// that read was given. One more read names that address alone, once the first has answered, and
// the page publishes its answer too: the page's word for a document, never its file name.
test("what a plan task's words cite is named by one more read", async () => {
  const asked: string[][] = [];
  const stubs = stubTreeReads(async (refs) => {
    asked.push([...refs]);
    return citingTargets(refs);
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ReferenceNamesProvider>{children}</ReferenceNamesProvider>
    </QueryClientProvider>
  );
  try {
    const view = renderHook(
      () => {
        useIssueTree(withNotes);
        return useKnownReferenceName(NOTES_REF);
      },
      { wrapper }
    );
    await waitFor(() => expect(view.result.current).toBe(NODE_TYPE_NAMES.document));
    expect(asked).toHaveLength(2);
    expect(asked[0]).toContain(TASK_REF);
    expect(asked[1]).toEqual([NOTES_REF]);
    view.unmount();
  } finally {
    for (const stub of stubs) stub.mockRestore();
  }
});

// Once nothing is left for the second read to name - an address only a plan task's stored source
// cited is cited by a comment too, so the first read names it - the second read's query is
// disabled, and it would hold its last answer as placeholder for good. The name the page
// publishes for that address is the first read's newer one, never that older answer: here the
// document is gone by the time the first read names it again, so no name is published for it.
test("once the second read has nothing left to name, the first read's newer names stand", async () => {
  let gone = false;
  const asked: string[][] = [];
  const stubs = stubTreeReads(async (refs) => {
    asked.push([...refs]);
    return citingTargets(refs, gone);
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <ReferenceNamesProvider>{children}</ReferenceNamesProvider>
    </QueryClientProvider>
  );
  try {
    const view = renderHook(
      () => {
        useIssueTree(withNotes);
        return useKnownReferenceName(NOTES_REF);
      },
      { wrapper }
    );
    // The first round: only the plan task's source cites the notes, so the second read names it.
    await waitFor(() => expect(view.result.current).toBe(NODE_TYPE_NAMES.document));
    expect(asked[1]).toEqual([NOTES_REF]);

    // The notes are deleted, and a comment citing them arrives with the tree read's next answer:
    // the tree's own addresses hold them now, so the first read names them and nothing is left
    // for the second.
    gone = true;
    await act(async () => {
      client.setQueryData(
        issueTreeKey(KEY),
        treeAnswer([{ ...comment, body: `Read ${NOTES_REF} again.` }])
      );
      await Promise.resolve();
    });
    await waitFor(() => expect(view.result.current).toBeUndefined());
    expect(asked).toHaveLength(3);
    expect(asked[2]).toContain(NOTES_REF);
    view.unmount();
  } finally {
    for (const stub of stubs) stub.mockRestore();
  }
});

// The tester's F17 and dispatch://INFRA-4062 R5: one block read for every document of the issue
// was 25 of a first load's 47 requests, and F's four were still four. The tree read answers
// every typed block of every role document, so the outline costs that one request, with no
// page left to ask for, and no document's blocks are read. Every decision is a node of the tree
// read, so no decisions read is made either.
test("the tree is read in one request, and no document's blocks are read", async () => {
  const withDocuments: IssueDetails = {
    ...issue,
    artifacts: [
      issueDocument("artifact-spec", "spec", { primary: true }),
      plan,
      issueDocument("artifact-verification", "verification-md", { role: "verification" }),
      issueDocument("artifact-decisions", "decisions-md", { role: "decisions" }),
      notes,
    ],
    decisions_artifact_id: "artifact-decisions",
    plan_artifact_id: plan.id,
    primary_artifact_id: "artifact-spec",
    verification_artifact_id: "artifact-verification",
  };
  let named = 0;
  const stubs = stubTreeReads(async (refs) => {
    named += 1;
    return namedTargets(refs);
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  try {
    const view = renderHook(() => useIssueTree(withDocuments).root, { wrapper });
    // The names are read once every other read has answered, the tree read among them.
    await waitFor(() => expect(named).toBe(1));
    expect((api.getIssueTree as Mock<typeof api.getIssueTree>).mock.calls).toEqual([[KEY, {}]]);
    expect(api.getArtifactBlocks).not.toHaveBeenCalled();
    expect(api.getIssueDecisions).not.toHaveBeenCalled();
    view.unmount();
  } finally {
    for (const stub of stubs) stub.mockRestore();
  }
});

// The server gives a document its part by its role, or, for a row a rolling deploy left with no
// role, by the slug that part is made under, and it omits `role` on such a row. The issue read
// names the document all the same (`plan_artifact_id`), and the page names it by that id; its
// task is a row of the tree, which the tree read answers with no blocks read.
test("A plan with no role that the issue names by its id is named the plan, its task read from the tree", async () => {
  const roleless = issueDocument(plan.id, PLAN);
  const named: IssueDetails = { ...issue, artifacts: [roleless], plan_artifact_id: roleless.id };
  const cites = `dispatch://${KEY}/artifact/${PLAN}`;
  const stubs = stubTreeReads(async (refs) => namedTargets(refs), {
    comments: [{ ...comment, body: `The tasks are in ${cites}.` }],
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  try {
    const view = renderHook(() => useIssueTree(named).root, { wrapper });
    await waitFor(() => expect(taskWords(view.result.current)).toBe(TASK_WORDS));
    expect(api.getArtifactBlocks).not.toHaveBeenCalled();
    // A comment citing the whole plan carries a chip that names it as the plan.
    const chips = nodesInOrder(view.result.current).find((node) => node.id === comment.id)?.chips;
    expect(chips?.map(({ name, ref }) => ({ name, ref }))).toEqual([
      { name: PRESET_NAMES.plan, ref: cites },
    ]);
    view.unmount();
  } finally {
    for (const stub of stubs) stub.mockRestore();
  }
});

// The batched read names a typed block by its stored excerpt, which opens on the block's
// directive line. Every link that reads the page's published names draws the name as it is, so
// the page publishes the name a chip naming the block draws (`targetNames`): the first line of
// the server's reading where the read sends one, and the page's word for the block's kind where
// it sends none - never the directive line.
test("A published name of a typed block is never its directive line", async () => {
  const opener = `:::plan_task{#t1 serves="dispatch://${KEY}/spec#b-r1"}`;
  const reading: DisplayTree = {
    content: [
      {
        content: [{ marks: [{ type: "strong" }], text: TASK_WORDS, type: "text" }],
        type: "paragraph",
      },
    ],
    type: "doc",
  };
  for (const [sent, answer, published] of [
    ["a reading", { name: `${opener}\n**${TASK_WORDS}**\n:::`, text_tree: reading }, TASK_WORDS],
    ["no reading", { name: `${opener}\n${TASK_WORDS}\n:::` }, NODE_TYPE_NAMES.plan_task],
  ] as const) {
    const stubs = stubTreeReads(async (refs) => ({
      targets: namedTargets(refs).targets.map((target) =>
        target.ref === TASK_REF ? { ...target, ...answer } : target
      ),
    }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>
        <ReferenceNamesProvider>{children}</ReferenceNamesProvider>
      </QueryClientProvider>
    );
    try {
      const view = renderHook(
        () => {
          useIssueTree(issue);
          return useKnownReferenceName(TASK_REF);
        },
        { wrapper }
      );
      await waitFor(() => expect(view.result.current, `the read sent ${sent}`).toBe(published));
      view.unmount();
    } finally {
      for (const stub of stubs) stub.mockRestore();
    }
  }
});

// dispatch://INFRA-1196 decision 98: a refetch of the tree read that fails keeps the answer the
// first read gave - TanStack holds a query's data through a failed refetch, and the tree is still
// built from it - so a view that draws from the blocks it holds keeps drawing them
// (`blocksHeld`), while a view that needs every read to have answered stops waiting on one that
// failed (`blocksRead`). Before any answer the tree holds no blocks and has read none.
test("blocks a failed refetch leaves in hand stay held while they stop counting as read", async () => {
  const treeOut = Promise.withResolvers<void>();
  const stubs = stubTreeReads(async (refs) => namedTargets(refs), {
    held: { read: "tree", until: treeOut.promise },
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  try {
    const view = renderHook(() => useIssueTree(issue), { wrapper });
    expect(view.result.current.blocksHeld).toBe(false);
    expect(view.result.current.blocksRead).toBe(false);

    await act(async () => {
      treeOut.resolve();
      await treeOut.promise;
    });
    await waitFor(() => expect(view.result.current.blocksRead).toBe(true));
    expect(view.result.current.blocksHeld).toBe(true);

    // The tree is read again, and this time the read fails.
    (api.getIssueTree as Mock<typeof api.getIssueTree>).mockRejectedValue(
      new Error("the tree read failed")
    );
    await act(async () => {
      await client.refetchQueries({ exact: true, queryKey: issueTreeKey(KEY) });
    });
    await waitFor(() => expect(view.result.current.blocksRead).toBe(false));
    expect(view.result.current.blocksHeld).toBe(true);
    // The tree is still built from the answer the first read gave, and the plan's blocks too.
    expect(nodesInOrder(view.result.current.root).some((node) => node.id === `${PLAN}#b-t1`)).toBe(
      true
    );
    expect(view.result.current.blocksOf("plan").data?.map((block) => block.id)).toEqual(["t1"]);
    view.unmount();
  } finally {
    treeOut.resolve();
    for (const stub of stubs) stub.mockRestore();
  }
});

// dispatch://INFRA-4062 R6: a write that invalidates a query of the issue - here its comments,
// as a reply sent from the page does - invalidates the tree read too, through the one
// subscription the page holds (`subscribeTreeFreshness`, which `ViewReadProvider` mounts on the
// page and this case mounts itself), so the tree is read again and draws the new comment.
test("an invalidated comments query reads the tree again", async () => {
  const stubs = stubTreeReads(async (refs) => namedTargets(refs));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const unsubscribe = subscribeTreeFreshness(client, KEY);
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  try {
    const view = renderHook(() => useIssueTree(issue).root, { wrapper });
    await waitFor(() => expect(taskWords(view.result.current)).toBe(TASK_WORDS));
    expect(api.getIssueTree).toHaveBeenCalledTimes(1);

    (api.getIssueTree as Mock<typeof api.getIssueTree>).mockResolvedValue(treeAnswer([comment]));
    await act(async () => {
      await client.invalidateQueries({ queryKey: ["comments", KEY] });
    });
    await waitFor(() =>
      expect(nodesInOrder(view.result.current).some((node) => node.id === comment.id)).toBe(true)
    );
    expect(api.getIssueTree).toHaveBeenCalledTimes(2);
    view.unmount();
  } finally {
    unsubscribe();
    for (const stub of stubs) stub.mockRestore();
  }
});

// dispatch://INFRA-4062 R5 and decision 98: the tree read holds data once every page of it holds
// data - the pages are read in turn and answered merged - so neither the tree nor a role's blocks
// are drawn from the first page alone; and a refetch whose last page fails keeps every page held,
// and the plan's task on the second page with them.
test("blocksHeld holds while every page holds data", async () => {
  const secondOut = Promise.withResolvers<void>();
  const stubs = stubTreeReads(async (refs) => namedTargets(refs));
  // The tree read's two pages: the issue's own node, then the plan's task.
  const firstPage: TreeAnswer = {
    ...treeAnswer([]),
    cursor: "page-2",
    nodes: [answerNode(KEY, "issue", { parent: null })],
  };
  const secondPage: TreeAnswer = { ...treeAnswer([]), nodes: [task] };
  (api.getIssueTree as Mock<typeof api.getIssueTree>).mockImplementation(async (_key, query) => {
    if (query?.cursor === undefined) return firstPage;
    await secondOut.promise;
    return secondPage;
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  try {
    const view = renderHook(() => useIssueTree(issue), { wrapper });
    await waitFor(() => expect(api.getIssueTree).toHaveBeenCalledTimes(2));
    expect(view.result.current.blocksHeld).toBe(false);
    expect(view.result.current.blocksOf("plan").data).toBeUndefined();

    await act(async () => {
      secondOut.resolve();
      await secondOut.promise;
    });
    await waitFor(() => expect(view.result.current.blocksHeld).toBe(true));
    expect(view.result.current.blocksRead).toBe(true);
    expect(view.result.current.blocksOf("plan").data?.map((block) => block.id)).toEqual(["t1"]);

    // The tree is read again, and its second page fails.
    (api.getIssueTree as Mock<typeof api.getIssueTree>).mockImplementation(async (_key, query) => {
      if (query?.cursor === undefined) return firstPage;
      throw new Error("the second page failed");
    });
    await act(async () => {
      await client.refetchQueries({ exact: true, queryKey: issueTreeKey(KEY) });
    });
    await waitFor(() => expect(view.result.current.blocksRead).toBe(false));
    expect(view.result.current.blocksHeld).toBe(true);
    expect(view.result.current.blocksOf("plan").data?.map((block) => block.id)).toEqual(["t1"]);
    view.unmount();
  } finally {
    secondOut.resolve();
    for (const stub of stubs) stub.mockRestore();
  }
});

// dispatch://INFRA-4062 R4: a filter's view read is watched and never sent here: whatever
// `ViewReadProvider` holds for it reaches `presetView` through the tree's root (`root.views`),
// so the Plan filter draws the view's plan tasks - the one its query took, not the second the
// tree holds - and the root keeps its identity while neither the tree nor a held view changes.
test("a held view answer reaches presetView through root.views", async () => {
  const second = answerNode("t2", "plan_task", {
    document: { artifact_id: plan.id, position: 1, slug: PLAN },
    text: "A second step.",
  });
  const stubs = stubTreeReads(async (refs) => namedTargets(refs), {
    tree: treeAnswer([], { nodes: [second] }),
  });
  const planView: TreeAnswer = {
    ...treeAnswer([]),
    nodes: [{ ...task, mark: "query" }],
  };
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  try {
    const view = renderHook(() => useIssueTree(issue).root, { wrapper });
    await waitFor(() => expect(taskWords(view.result.current)).toBe(TASK_WORDS));
    expect(presetView(view.result.current, "plan", "").root.children.map((row) => row.id)).toEqual([
      `${PLAN}#b-t1`,
      `${PLAN}#b-t2`,
    ]);

    await act(async () => {
      client.setQueryData(viewKey(KEY, "plan"), planView);
      await Promise.resolve();
    });
    await waitFor(() => expect(view.result.current.views?.plan).toBe(planView));
    expect(presetView(view.result.current, "plan", "").root.children.map((row) => row.id)).toEqual([
      `${PLAN}#b-t1`,
    ]);
    const held = view.result.current;
    view.rerender();
    expect(view.result.current).toBe(held);
    // The hook sent no view read of its own.
    expect(
      (api.getIssueTree as Mock<typeof api.getIssueTree>).mock.calls.every(
        ([, query]) => query?.view === undefined
      )
    ).toBe(true);
    view.unmount();
  } finally {
    for (const stub of stubs) stub.mockRestore();
  }
});
