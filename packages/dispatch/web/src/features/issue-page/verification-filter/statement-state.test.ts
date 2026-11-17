import { expect, test } from "bun:test";

import type { Artifact, ArtifactBlock, CheckBlock, IssueDetails } from "../../../api/types";
import { buildIssueTree, type TreeNode } from "../issue-tree";
import { NODE_STATE_WORDS } from "../page-words";
import { filterSignals, presetView } from "../presets";
import { blockNodeId } from "../tree-drafts";
import { treeRows } from "../tree-fold";
import {
  COMMIT_CHARACTERS,
  namedCommit,
  shortCommit,
  statementCounts,
  statementStandings,
  type VerificationReading,
  verificationReading,
} from "./statement-state";
import {
  COMMIT,
  check,
  checkNode,
  ISSUE_KEY,
  node,
  OTHER_COMMIT,
  result,
  SPEC_SLUG,
  statementNode,
  VERIFICATION_SLUG,
  verificationView,
} from "./verification-fixtures";

/**
 * How the Verification filter reads the issue's results (dispatch://INFRA-1196 R17): the one
 * commit the page names, how each statement stands at it, and how many statements carry a
 * check. Each row below is one of the ways a statement can stand, read over the tree the filter
 * leaves and the results read the page already made.
 *
 * What the body draws of this reading is `VerificationFilterBody.drawn.test.tsx`.
 */

/** The reading over the statements given, as the body is handed it. */
function reading(
  statements: readonly TreeNode[],
  checks: readonly CheckBlock[],
  ...loose: readonly TreeNode[]
): VerificationReading {
  return verificationReading({ checks, view: verificationView([...statements, ...loose]) });
}

/** A check that passed at the commit the page names. */
function passing(blockId: string): CheckBlock {
  return check({
    block_id: blockId,
    pass_stands: { answer: "yes" },
    results: [result({ check_block_id: blockId, id: `result-${blockId}` })],
  });
}

// R17: "a statement reads Met where every check of it passed at that commit".
test("a statement whose every check passed at the commit the page names reads Met", () => {
  const blocks = [passing("c-one"), passing("c-two")];
  const statement = statementNode("r1", "The page draws the checks.", blocks.map(checkNode));
  const read = reading([statement], blocks);

  expect(read.commit).toBe(COMMIT);
  expect(read.statements.map((held) => held.state)).toEqual(["met"]);
  expect(read.statements[0]?.checks.map((held) => held.state)).toEqual(["passed", "passed"]);
  expect(read.covered).toBe(1);
  expect(statementCounts(read.statements)).toEqual({ met: 1, not_met: 0, unknown: 0 });
});

// R17: "Not met where one failed there". One failure settles it, whatever the other checks say.
test("a statement one of whose checks failed at that commit reads Not met", () => {
  const failed = check({
    block_id: "c-two",
    pass_stands: { answer: "no" },
    results: [result({ check_block_id: "c-two", failed: 1, id: "result-two", verdict: "fail" })],
  });
  const blocks = [passing("c-one"), failed];
  const statement = statementNode("r1", "The page draws the checks.", blocks.map(checkNode));
  const read = reading([statement], blocks);

  expect(read.statements.map((held) => held.state)).toEqual(["not_met"]);
  expect(read.statements[0]?.checks.map((held) => held.state)).toEqual(["passed", "failed"]);
  expect(statementCounts(read.statements)).toEqual({ met: 0, not_met: 1, unknown: 0 });
});

// R17: "a result recorded against another commit reads Out of date and makes its statement
// Unknown". The server still answers that the pass stands - it compares a result with the basis
// its own check carries - and the page reads every result of the issue against the one commit
// it names.
test("a pass recorded against another commit reads Out of date and leaves its statement Unknown", () => {
  const elsewhere = check({
    block_id: "c-two",
    pass_stands: { answer: "yes" },
    results: [result({ check_block_id: "c-two", commit_sha: OTHER_COMMIT, id: "result-two" })],
  });
  const blocks = [passing("c-one"), elsewhere];
  const statement = statementNode("r1", "The page draws the checks.", blocks.map(checkNode));
  const read = reading([statement], blocks);

  expect(read.commit).toBe(COMMIT);
  expect(read.statements[0]?.checks.map((held) => held.state)).toEqual(["passed", "older"]);
  expect(read.statements[0]?.checks.map((held) => held.atNamedCommit)).toEqual([true, false]);
  expect(read.statements.map((held) => held.state)).toEqual(["unknown"]);
  expect(NODE_STATE_WORDS.check.older).toBe("Out of date");
});

// A statement whose only check passed at another commit has no result at the commit the page
// names at all, so the page names the commit of the result it does hold.
test("a pass at another commit alone names that commit and leaves the statement Unknown", () => {
  const elsewhere = check({
    block_id: "c-one",
    pass_stands: { answer: "yes" },
    results: [result({ commit_sha: OTHER_COMMIT, id: "result-one" })],
  });
  const read = reading(
    [statementNode("r1", "The page draws it.", [checkNode(elsewhere)])],
    [elsewhere]
  );

  expect(read.commit).toBe(OTHER_COMMIT);
  // The one result the issue holds is the commit the page names, so the pass is at it.
  expect(read.statements[0]?.checks.map((held) => held.state)).toEqual(["passed"]);
  expect(read.statements.map((held) => held.state)).toEqual(["met"]);
});

// A withdrawn result is no evidence (`results_comparison.go`, `tree-drafts.ts`), so it neither
// names the commit the rest of the issue is read against nor leaves its check reading Failed.
test("a check whose every result was withdrawn reads Retracted and names no commit", () => {
  const withdrawn = check({
    block_id: "c-two",
    pass_stands: { answer: "no" },
    results: [
      result({
        check_block_id: "c-two",
        commit_sha: OTHER_COMMIT,
        created_at: "2026-11-15T09:00:00Z",
        id: "result-withdrawn",
        retracted: {
          actor: { id: "tester", kind: "session" },
          created_at: "2026-11-15T10:00:00Z",
          reason: "Run was wrong.",
        },
      }),
    ],
  });
  const blocks = [passing("c-one"), withdrawn];
  const statement = statementNode("r1", "The page draws the checks.", blocks.map(checkNode));
  const read = reading([statement], blocks);

  // The newest result of the issue is the withdrawn one; the commit is the standing pass's.
  expect(read.commit).toBe(COMMIT);
  expect(read.statements[0]?.checks.map((held) => held.state)).toEqual(["passed", "retracted"]);
  expect(read.statements.map((held) => held.state)).toEqual(["unknown"]);
  expect(NODE_STATE_WORDS.check.retracted).toBe("Retracted");
  // The body still draws the withdrawn result: it is all the check has to show.
  expect(read.statements[0]?.checks[1]?.latest?.id).toBe("result-withdrawn");
});

// R17's "Unknown otherwise": a check with no result leaves its statement Unknown, and the
// statement is covered all the same, since a check serves it.
test("a check with no result reads No result, and its statement is covered and Unknown", () => {
  const unrun = check({ block_id: "c-one", pass_stands: { answer: "no" }, results: [] });
  const read = reading([statementNode("r1", "The page draws it.", [checkNode(unrun)])], [unrun]);

  expect(read.statements[0]?.checks.map((held) => held.state)).toEqual(["no-result"]);
  expect(read.statements[0]?.checks[0]?.latest).toBeUndefined();
  expect(read.statements.map((held) => held.state)).toEqual(["unknown"]);
  expect(read.covered).toBe(1);
  expect(read.commit).toBeUndefined();
  expect(NODE_STATE_WORDS.check["no-result"]).toBe("No result");
});

// R2: "each check under every requirement it serves as context". The tree holds one node per
// placement under the one id, and both statements are covered by it.
test("a check that serves two statements is read under both, and both are covered", () => {
  const shared = passing("c-one");
  const first = statementNode("r1", "The page draws the first.", [checkNode(shared)]);
  const second = statementNode("r2", "The page draws the second.", [
    { ...checkNode(shared), transcluded: true },
  ]);
  const read = reading([first, second], [shared]);

  expect(read.statements.map((held) => held.node.id)).toEqual([first.id, second.id]);
  expect(read.statements.map((held) => held.checks[0]?.node.id)).toEqual([
    checkNode(shared).id,
    checkNode(shared).id,
  ]);
  expect(read.statements.map((held) => held.state)).toEqual(["met", "met"]);
  expect(read.covered).toBe(2);
});

// A statement no check serves is Unknown and is in no covered count: "a statement that has a
// check is not a statement that is true" cuts both ways (R17).
test("a statement no check serves is Unknown and is not covered", () => {
  const served = passing("c-one");
  const read = reading(
    [
      statementNode("r1", "The page draws it.", [checkNode(served)]),
      statementNode("r2", "Nothing proves this.", []),
    ],
    [served]
  );

  expect(read.statements.map((held) => held.state)).toEqual(["met", "unknown"]);
  expect(read.covered).toBe(1);
  expect(statementCounts(read.statements)).toEqual({ met: 1, not_met: 0, unknown: 1 });
});

// The commit is the newest result's, read from the times the results carry and not from the
// order the read answered in.
test("the commit the page names is the newest standing result's, whatever order they arrive in", () => {
  const older = check({
    block_id: "c-one",
    results: [result({ commit_sha: OTHER_COMMIT, created_at: "2026-11-10T00:00:00Z", id: "old" })],
  });
  const newer = check({
    block_id: "c-two",
    results: [
      result({
        check_block_id: "c-two",
        commit_sha: COMMIT,
        created_at: "2026-11-14T00:58:00Z",
        id: "new",
      }),
    ],
  });

  expect(namedCommit([older, newer])).toBe(COMMIT);
  expect(namedCommit([newer, older])).toBe(COMMIT);
});

// The server writes its times to the microsecond, and `Date.parse` reads them to the
// millisecond: two results recorded inside one millisecond are still read newest first by the
// server's own time, and not left in the order the read happened to send them in.
test("of two results recorded in one millisecond, the later microsecond is the newest", () => {
  const rerun = check({
    block_id: "c-one",
    results: [
      result({
        commit_sha: OTHER_COMMIT,
        created_at: "2026-11-05T10:00:00.000100Z",
        failed: 1,
        id: "fail-first",
        verdict: "fail",
      }),
      result({ commit_sha: COMMIT, created_at: "2026-11-05T10:00:00.000900Z", id: "pass-later" }),
    ],
  });

  expect(namedCommit([rerun])).toBe(COMMIT);
});

// A result whose time the page cannot read is older than every result whose time it can, wherever
// it arrives, so it never names the commit over a result the page can place; and it throws nothing.
test("a result whose time is not a timestamp is the oldest, wherever it arrives", () => {
  const unread = result({ commit_sha: OTHER_COMMIT, created_at: "not a time", id: "unread" });
  const placed = result({ created_at: "2026-11-01T00:00:00Z", id: "placed" });

  expect(namedCommit([check({ block_id: "c-one", results: [unread, placed] })])).toBe(COMMIT);
  expect(namedCommit([check({ block_id: "c-one", results: [placed, unread] })])).toBe(COMMIT);
  expect(namedCommit([check({ block_id: "c-one", results: [unread] })])).toBe(OTHER_COMMIT);
});

// A payload with no answer over the check's results at all is the page's own blindness, and no
// verdict of the check's: it reads "Could not read" and proves nothing.
test("a check whose results the page cannot read draws Could not read and no met statement", () => {
  const unreadable = check({
    block_id: "c-one",
    pass_stands: undefined as unknown as never,
    results: [result({ id: "result-one" })],
  });
  const read = reading(
    [statementNode("r1", "The page draws it.", [checkNode(unreadable)])],
    [unreadable]
  );

  expect(read.statements[0]?.checks.map((held) => held.state)).toEqual(["unreadable"]);
  expect(read.statements.map((held) => held.state)).toEqual(["unknown"]);
  expect(NODE_STATE_WORDS.check.unreadable).toBe("Could not read");
});

// A commit Dispatch could not place: there is a result, and nothing can be said of how it
// stands, so neither Met nor Not met is true of its statement.
test("a check Dispatch cannot judge draws Cannot tell and leaves its statement Unknown", () => {
  const unmarked = check({
    block_id: "c-one",
    pass_stands: { answer: "unknown" },
    results: [result({ commit_stands: { answer: "unknown" }, id: "result-one" })],
  });
  const read = reading(
    [statementNode("r1", "The page draws it.", [checkNode(unmarked)])],
    [unmarked]
  );

  expect(read.statements[0]?.checks.map((held) => held.state)).toEqual(["unmarked"]);
  expect(read.statements.map((held) => held.state)).toEqual(["unknown"]);
  expect(NODE_STATE_WORDS.check.unmarked).toBe("Cannot tell");
});

// The latest result the body draws is the newest that stands, so a check whose newest result
// was withdrawn still shows the evidence it stands on.
test("the latest result a check draws is the newest that stands", () => {
  const taken = check({
    block_id: "c-one",
    pass_stands: { answer: "yes" },
    results: [
      result({
        created_at: "2026-11-15T09:00:00Z",
        id: "result-withdrawn",
        retracted: {
          actor: { id: "tester", kind: "session" },
          created_at: "2026-11-15T10:00:00Z",
          reason: "Run was wrong.",
        },
        verdict: "fail",
      }),
      result({ created_at: "2026-11-14T00:58:00Z", id: "result-standing" }),
    ],
  });
  const read = reading([statementNode("r1", "The page draws it.", [checkNode(taken)])], [taken]);

  expect(read.statements[0]?.checks[0]?.latest?.id).toBe("result-standing");
  expect(read.statements.map((held) => held.state)).toEqual(["met"]);
});

// R17: "Not met where one failed there". The server's `pass_stands` says yes for any pass at a
// basis, whatever later fails exist, so a check with an older pass at another commit and a fail
// at the commit the page names read "Out of date" and left its statement Unknown. The fail at
// the named commit is what the page reads.
const regressed = check({
  block_id: "c-one",
  pass_stands: { answer: "yes" },
  results: [
    result({ created_at: "2026-11-15T09:00:00Z", id: "fail-here", verdict: "fail" }),
    result({
      commit_sha: OTHER_COMMIT,
      created_at: "2026-11-13T09:00:00Z",
      id: "pass-elsewhere",
    }),
  ],
});

test("a fail at the commit the page names reads Failed whatever older pass still stands", () => {
  const read = reading(
    [statementNode("r1", "The page draws it.", [checkNode(regressed)])],
    [regressed]
  );

  expect(read.commit).toBe(COMMIT);
  expect(read.statements[0]?.checks[0]).toMatchObject({
    atNamedCommit: true,
    standsOn: "fail-here",
    state: "failed",
  });
  expect(read.statements.map((held) => held.state)).toEqual(["not_met"]);
});

/** The issue's tree as `buildIssueTree` draws it from its reads: one statement of the spec,
 *  which every check of `checks` serves, and the results read answering `checks`. */
function issueTree(checks: readonly CheckBlock[]): TreeNode {
  const document = (id: string, slug: string, name: string, primary: boolean): Artifact => ({
    created_at: "2026-11-05T00:00:00Z",
    created_by: { id: "alice", kind: "user" },
    id,
    issue_key: ISSUE_KEY,
    kind: "doc",
    name,
    primary,
    project: "CORE",
    ref_key: `artifact:${id}`,
    session_id: null,
    slug,
    versions: [],
  });
  const spec = document("artifact-spec", SPEC_SLUG, "spec.md", true);
  const verification = document(
    "artifact-verification",
    VERIFICATION_SLUG,
    "verification.md",
    false
  );
  const statement = `dispatch://${ISSUE_KEY}/${SPEC_SLUG}#b-r1`;
  const block = (id: string, type: string, serves?: string): ArtifactBlock => ({
    attributes: serves === undefined ? [] : [{ kind: "block[]", name: "serves", value: serves }],
    from: 0,
    id,
    references: { asks: 0, comments: 0 },
    to: 1,
    token: id,
    type,
  });
  const issue: IssueDetails = {
    advances: [],
    advances_revision: 0,
    artifacts: [spec, verification],
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
    key: ISSUE_KEY,
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
    verification_artifact_id: verification.id,
  };
  return buildIssueTree({
    coverage: {
      gaps: [],
      has_plan: false,
      issue: ISSUE_KEY,
      requirements: [
        {
          block_id: "r1",
          checks: checks.length,
          gaps: [],
          plan_tasks: 0,
          ref: statement,
          text: "The page draws it.",
        },
      ],
      spec_artifact_id: spec.id,
    },
    documents: [
      { artifactId: spec.id, blocks: [block("r1", "requirement")] },
      {
        artifactId: verification.id,
        blocks: checks.map((one) => block(one.block_id, "check", statement)),
      },
    ],
    issue,
    results: { checks: [...checks], issue: ISSUE_KEY, verification_artifact_id: verification.id },
  });
}

// The tree read a check's state from `checkState` alone while this body turned it over at the
// commit the page names, so one check read two states. The regressed check above read Passed on
// its row and was off the fold's frontier, so its statement arrived folded with the regression
// inside it, and the filter row counted nothing failing, while this body read it Failed; a fail
// that stands at another commit read Failed on the row and in the count, and Out of date here.
// The tree's node holds the one reading this body draws (`checkStanding`).
test("a check reads one state on its row, in the fold, in the filter row and in the body", () => {
  const statement = blockNodeId(SPEC_SLUG, "r1");
  const checkId = (blockId: string) => blockNodeId(VERIFICATION_SLUG, blockId);
  const placed = (root: TreeNode, blockId: string) =>
    root.children
      .find((child) => child.id === statement)
      ?.children.find((child) => child.id === checkId(blockId));
  const bodyStates = (root: TreeNode, checks: readonly CheckBlock[]) =>
    verificationReading({
      checks,
      view: presetView(root, "verification", "").root,
    }).statements[0]?.checks.map((held) => held.state);

  const tree = issueTree([regressed]);
  expect(placed(tree, "c-one")?.state).toBe("failed");
  expect(bodyStates(tree, [regressed])).toEqual(["failed"]);
  // The check is open and unanswered, so the fold opens its statement down to it.
  const rows = treeRows(tree);
  expect(rows.find((row) => row.kind === "node" && row.node.id === statement)).toMatchObject({
    expanded: true,
  });
  expect(rows.some((row) => row.kind === "node" && row.node.id === checkId("c-one"))).toBe(true);
  expect(filterSignals(tree, "").verification).toEqual({
    count: 1,
    flag: { count: 1, word: "failing" },
  });

  // The reverse: a fail that stands at the commit its own check compared with, which is not the
  // commit the page names, since another check's pass is newer.
  const passedHere = check({ block_id: "c-one", results: [result({ id: "pass-here" })] });
  const failedElsewhere = check({
    block_id: "c-two",
    compared_with: [{ commit: OTHER_COMMIT, kind: "named" }],
    pass_stands: { answer: "no" },
    results: [
      result({
        check_block_id: "c-two",
        commit_sha: OTHER_COMMIT,
        created_at: "2026-11-13T09:00:00Z",
        failed: 1,
        id: "fail-elsewhere",
        verdict: "fail",
      }),
    ],
  });
  const reverse = issueTree([passedHere, failedElsewhere]);
  expect(placed(reverse, "c-two")?.state).toBe("older");
  expect(bodyStates(reverse, [passedHere, failedElsewhere])).toEqual(["passed", "older"]);
  expect(filterSignals(reverse, "").verification).toEqual({ count: 2 });
});

// A check fails, its author fixes the check's words, and nobody has run it again. A fail
// recorded against words the check no longer has stands for nothing, as `checkState` and the
// server's `standingCommit` read it, so it is no fail at the commit the page names either: the
// check is Out of date and its statement Unknown, not Not met on evidence about words the check
// no longer has.
test("a fail at the commit the page names whose check's words moved reads Out of date", () => {
  const moved = check({
    block_id: "c-one",
    pass_stands: { answer: "no" },
    results: [result({ check_words_moved: true, failed: 1, id: "fail-moved", verdict: "fail" })],
  });
  const read = reading([statementNode("r1", "The page draws it.", [checkNode(moved)])], [moved]);

  expect(read.commit).toBe(COMMIT);
  expect(read.statements[0]?.checks[0]?.state).toBe("older");
  expect(NODE_STATE_WORDS.check.older).toBe("Out of date");
  expect(read.statements.map((held) => held.state)).toEqual(["unknown"]);
});

// Ruling 6: the body draws the newest result of each check and folds the rest, so the model
// hands it the rest apart, newest first, and names the result the state stands on.
test("a check's other results are handed apart from its latest, newest first", () => {
  const busy = check({
    block_id: "c-one",
    pass_stands: { answer: "yes" },
    results: [
      result({ created_at: "2026-11-12T09:00:00Z", id: "oldest" }),
      result({ created_at: "2026-11-14T09:00:00Z", id: "newest" }),
      result({ created_at: "2026-11-13T09:00:00Z", id: "middle" }),
    ],
  });
  const read = reading([statementNode("r1", "The page draws it.", [checkNode(busy)])], [busy]);
  const drawn = read.statements[0]?.checks[0];

  expect(drawn?.latest?.id).toBe("newest");
  expect(drawn?.earlier.map((held) => held.id)).toEqual(["middle", "oldest"]);
  expect(drawn?.standsOn).toBe("newest");
});

// R10: a check of a descendant issue counts. The coverage read counts it for the statement and
// this issue's tree holds no node for it, so the statement is covered and draws no check.
test("a statement a child issue's check serves is covered", () => {
  const served = {
    ...statementNode("r1", "The page draws it.", []),
    counts: { checks: 2, planTasks: 0 },
  };
  const unserved = {
    ...statementNode("r2", "The page draws that.", []),
    counts: { checks: 0, planTasks: 0 },
  };
  const read = reading([served, unserved], []);

  expect(read.covered).toBe(1);
  expect(read.statements.map((held) => held.state)).toEqual(["unknown", "unknown"]);
});

// The Spec filter's mark is this reading, over the whole tree, so one statement reads the same
// in both views.
test("every statement of the whole tree is read as the Verification filter reads it", () => {
  const passed = passing("c-one");
  const tree = node({
    children: [
      statementNode("r1", "The page draws it.", [checkNode(passed)]),
      statementNode("r2", "The page draws that.", []),
    ],
    id: "CORE-1",
    type: "issue",
  });
  const standings = statementStandings(tree, [passed]);

  expect([...standings]).toEqual([
    ["spec#b-r1", { covered: true, state: "met" }],
    ["spec#b-r2", { covered: false, state: "unknown" }],
  ]);
});

// While the results read is still out the page has read nothing of how the check stands, so it
// draws no state for it and no statement of it is Met.
test("a check the results read has not answered for draws no state", () => {
  const unread = passing("c-one");
  const read = reading([statementNode("r1", "The page draws it.", [checkNode(unread)])], []);

  expect(read.statements[0]?.checks.map((held) => held.state)).toEqual([undefined]);
  expect(read.statements.map((held) => held.state)).toEqual(["unknown"]);
  expect(read.covered).toBe(1);
});

// A check whose `serves` names no statement is a row of the filter all the same, and so is a
// node an address named: the body draws both rather than dropping what the filter kept.
test("the body is given the checks that serve no statement and every other row the filter kept", () => {
  const loose = passing("c-loose");
  const named = node({ id: "comment-1", text: "A comment an address named.", type: "comment" });
  const read = verificationReading({
    checks: [loose],
    view: verificationView(
      [statementNode("r1", "The page draws it.", []), checkNode(loose), named],
      [named.id]
    ),
  });

  expect(read.loose.map((held) => held.node.id)).toEqual([checkNode(loose).id]);
  expect(read.loose.map((held) => held.state)).toEqual(["passed"]);
  expect(read.others.map((held) => held.id)).toEqual(["comment-1"]);
  expect(read.covered).toBe(0);
});

// The page draws a commit as the picture draws it, in the short form git itself writes.
test("a commit is drawn in the short form the picture draws", () => {
  expect(shortCommit(COMMIT)).toBe(COMMIT.slice(0, COMMIT_CHARACTERS));
  expect(shortCommit(COMMIT)).toHaveLength(10);
  expect(shortCommit("abc")).toBe("abc");
});

/** What the model says of a node put forward and not yet settled (dispatch://INFRA-4063 R2). */
const PROPOSED = { life: "proposed" } as const;

/** What the model says of a node a later one took the place of (dispatch://INFRA-4063 R5); each
 *  node under it is `closed` too. */
const OVERTAKEN = { closed: true, life: "overtaken" } as const;

// dispatch://INFRA-4063 R3: a proposed check is only put forward, so it is no evidence for its
// statement or against it, and neither is a check an overtaken one replaced. Each statement
// stands on its other checks, and one whose only check is proposed is Unknown and not covered;
// every check is still read under its statement, as the body draws it.
test("a proposed check serves no statement", () => {
  const passed = passing("c-one");
  const failedAt = (blockId: string) =>
    check({
      block_id: blockId,
      pass_stands: { answer: "no" },
      results: [
        result({ check_block_id: blockId, failed: 1, id: `result-${blockId}`, verdict: "fail" }),
      ],
    });
  const proposedFail = failedAt("c-two");
  const overtakenFail = failedAt("c-three");
  const proposedPass = passing("c-four");
  const read = reading(
    [
      statementNode("r1", "The page draws the checks.", [
        checkNode(passed),
        { ...checkNode(proposedFail), ...PROPOSED },
      ]),
      statementNode("r2", "The page draws them again.", [
        { ...checkNode(passed), transcluded: true },
        { ...checkNode(overtakenFail), ...OVERTAKEN },
      ]),
      statementNode("r3", "The page may draw it.", [{ ...checkNode(proposedPass), ...PROPOSED }]),
    ],
    [passed, proposedFail, overtakenFail, proposedPass]
  );

  expect(read.statements.map((held) => held.checks.map((one) => one.state))).toEqual([
    ["passed", "failed"],
    ["passed", "failed"],
    ["passed"],
  ]);
  expect(read.statements.map((held) => held.state)).toEqual(["met", "met", "unknown"]);
  expect(read.covered).toBe(2);
  expect(statementCounts(read.statements)).toEqual({ met: 2, not_met: 0, unknown: 1 });
});

// dispatch://INFRA-4063 R3, c-unit: the Spec filter's mark is `statementStandings` over the whole
// tree and the Verification filter's is this reading over its view. On a statement whose only
// check is proposed both say it carries no check and stands on none: not covered, and Unknown.
// The same statement with that check live is covered and Met in both.
test("the Spec and Verification views agree on a statement whose only check is proposed", () => {
  const block = passing("c-one");
  const issueOf = (statement: TreeNode) =>
    node({ children: [statement], id: ISSUE_KEY, text: "An issue", type: "issue" });
  const proposed = statementNode("r1", "The page may draw it.", [
    { ...checkNode(block), ...PROPOSED },
  ]);
  const read = reading([proposed], [block]);

  expect([...statementStandings(issueOf(proposed), [block])]).toEqual([
    [proposed.id, { covered: false, state: "unknown" }],
  ]);
  expect(read.statements.map((held) => held.state)).toEqual(["unknown"]);
  expect(read.covered).toBe(0);

  const live = statementNode("r1", "The page may draw it.", [checkNode(block)]);
  expect([...statementStandings(issueOf(live), [block])]).toEqual([
    [live.id, { covered: true, state: "met" }],
  ]);
  expect(reading([live], [block]).statements.map((held) => held.state)).toEqual(["met"]);
  expect(reading([live], [block]).covered).toBe(1);
});

// dispatch://INFRA-4063 R3: a proposed statement is put forward and not yet settled, and a
// statement at or under an overtaken node was replaced, so none of them is in the covered count
// or in Met, Not met and Unknown, though the check under each passed; each one's standing says
// it is not covered.
test("a proposed statement is neither covered nor counted", () => {
  const passed = passing("c-one");
  // The passed check drawn again under another statement, with `extra` over it.
  const placed = (extra: Partial<TreeNode>) => ({
    ...checkNode(passed),
    transcluded: true,
    ...extra,
  });
  const proposed = {
    ...statementNode("r1", "The page may draw it.", [checkNode(passed)]),
    ...PROPOSED,
  };
  const overtaken = {
    ...statementNode("r2", "The page drew it.", [placed({ closed: true })]),
    ...OVERTAKEN,
  };
  const under = {
    ...statementNode("r3", "The page drew that.", [placed({ closed: true })]),
    closed: true as const,
  };
  const replaced = node({ children: [under], id: "ask-1", type: "ask", ...OVERTAKEN });
  const live = statementNode("r4", "The page draws it.", [placed({})]);
  const read = reading([proposed, overtaken, replaced, live], [passed]);

  expect(read.statements.map((held) => held.node.id)).toEqual([
    proposed.id,
    overtaken.id,
    under.id,
    live.id,
  ]);
  expect(read.covered).toBe(1);
  expect(statementCounts(read.statements)).toEqual({ met: 1, not_met: 0, unknown: 0 });
  const standings = statementStandings(
    node({ children: [proposed, overtaken, replaced, live], id: ISSUE_KEY, type: "issue" }),
    [passed]
  );
  expect([...standings].map(([id, standing]) => [id, standing.covered])).toEqual([
    [proposed.id, false],
    [overtaken.id, false],
    [under.id, false],
    [live.id, true],
  ]);
});

// dispatch://INFRA-4063 R3 leaves the rows alone: the Verification filter draws every row of its
// query, so the reading hands the body each statement, check and other row the filter kept,
// proposed and overtaken ones among them. Only the counts leave them out.
test("the reading keeps every row, proposed and overtaken ones included", () => {
  const proposedCheck = passing("c-one");
  const replacedCheck = passing("c-two");
  const proposedLoose = passing("c-loose");
  const overtakenLoose = passing("c-gone");
  const proposed = {
    ...statementNode("r1", "The page may draw it.", [{ ...checkNode(proposedCheck), ...PROPOSED }]),
    ...PROPOSED,
  };
  const overtaken = {
    ...statementNode("r2", "The page drew it.", [
      { ...checkNode(replacedCheck), closed: true as const },
    ]),
    ...OVERTAKEN,
  };
  const named = node({
    id: "comment-1",
    text: "A comment an address named.",
    type: "comment",
    ...OVERTAKEN,
  });
  const read = verificationReading({
    checks: [proposedCheck, replacedCheck, proposedLoose, overtakenLoose],
    view: verificationView(
      [
        proposed,
        overtaken,
        { ...checkNode(proposedLoose), ...PROPOSED },
        { ...checkNode(overtakenLoose), ...OVERTAKEN },
        named,
      ],
      [named.id]
    ),
  });

  expect(read.statements.map((held) => held.node.id)).toEqual([proposed.id, overtaken.id]);
  expect(read.statements.map((held) => held.checks.map((one) => one.node.id))).toEqual([
    [checkNode(proposedCheck).id],
    [checkNode(replacedCheck).id],
  ]);
  expect(read.loose.map((held) => held.node.id)).toEqual([
    checkNode(proposedLoose).id,
    checkNode(overtakenLoose).id,
  ]);
  expect(read.others.map((held) => held.id)).toEqual(["comment-1"]);
  expect(read.covered).toBe(0);
});
