import type { CheckBlock } from "../../../api/types";
import type { TreeNode } from "../issue-tree";
import type { CheckState, StatementState } from "../page-words";
import { checkStanding, namedCommit, newestFirst } from "../tree-drafts";
import { outOfCounts } from "../tree-fold";

/**
 * How each statement of the issue stands, and the one commit the page names for its results
 * (dispatch://INFRA-1196 R17, R2's Verification clauses).
 *
 * The Verification filter draws each statement with the checks that serve it and their latest
 * results. R17 adds three readings over that, and all three are here, pure, so the body draws
 * them and reads nothing of its own:
 *
 * - **One commit for the issue's results.** The page names it once, where the results are read,
 *   rather than on every result (`VerificationFilterBody.tsx`). It is the commit of the newest
 *   result recorded against any check of the issue that still stands: a withdrawn result is no
 *   evidence, as the server counts it (`results_comparison.go`'s `passStandsOf` skips it, and
 *   `tree-drafts.ts`'s `checkState` reads the newest result a retraction left standing), so a
 *   withdrawal may not move the commit every other result is read against and leave a page of
 *   results that each read "Out of date".
 * - **Met, Not met, Unknown.** A statement reads Met where every check of it passed at that
 *   commit, Not met where one failed there, and Unknown otherwise - no check at all, a result
 *   recorded against another commit, a withdrawn one, or a check Dispatch could not read. The
 *   checks' own states say which of those it is, in the words `NODE_STATE_WORDS.check` declares.
 * - **Covered.** How many statements carry a check. It is covered and never proven or met: a
 *   statement that has a check is not a statement that is true (R17, R22).
 *
 * How one check stands is `tree-drafts.ts`'s `checkStanding`, the page's one reading of it:
 * `checkState`'s answer over the basis the check itself carries (`compared_with`), turned over
 * at the one commit the page names, because R17 reads every result of the issue against that
 * commit - a fail there reads Failed whatever older pass the server still counts, and a pass or
 * a fail recorded against another commit reads "Out of date". The tree's check node holds the
 * same reading (`addChecks`), so this body, the outline's row, the fold's frontier and the
 * filter row's failing count say one word for one check.
 *
 * The model holds no React and makes no request: the body draws it, and the unit rows read the
 * same functions.
 */

/** The three, in the order the head counts them. */
export const STATEMENT_STATES: readonly StatementState[] = ["met", "not_met", "unknown"];

/** One check of a statement, as the body draws it. */
export interface CheckReading {
  /** Whether the result its state stands on ran at the commit the page names, which is what
   *  "passed at that commit" and "failed there" are read from (R17). */
  readonly atNamedCommit: boolean;
  /** Every other result of the check, newest first, which the body folds into one line with
   *  their count under the latest (ruling 6 of build-rulings-md: a check with 431 results drew
   *  a page 149,936 px tall). */
  readonly earlier: readonly TreeNode[];
  /** The result the body draws under the check: the newest that still stands, or, where every
   *  result of it was withdrawn, the newest withdrawn one, since that is what the reader is
   *  owed. Absent where the check has no result at all, and where the results read has not
   *  answered for the check. */
  readonly latest?: TreeNode;
  /** The id of the result the check's state stands on - the newest pass that stands for a
   *  check that passed, and the newest result that stands for every other - which the body
   *  marks wherever it draws it; absent where none stands. */
  readonly standsOn?: string;
  /** The check's own node, with its words and the four attributes its body draws. */
  readonly node: TreeNode;
  /** How the check stands, in the seven states the page has a word for, or `undefined` while
   *  the results read has not answered for it: a check the page has not read how it stands
   *  draws no state, because none of the seven is true of it. */
  readonly state?: CheckState;
}

/** One statement of the issue, with the checks that serve it. */
export interface StatementReading {
  readonly checks: readonly CheckReading[];
  readonly node: TreeNode;
  readonly state: StatementState;
}

/** What the Verification filter's body draws. */
export interface VerificationReading {
  /** The one commit the page names for the issue's results, or `undefined` where no result
   *  that stands names one. */
  readonly commit?: string;
  /** How many of the statements carry a check (R17's covered count). */
  readonly covered: number;
  /** The checks the filter draws that serve no statement of the issue: a `serves` naming
   *  nothing places the check under the issue (`tree-drafts.ts`), and the filter draws it as a
   *  row of its own, so the body draws it too rather than dropping a node the filter kept. */
  readonly loose: readonly CheckReading[];
  /** Every other row the filter drew: a node an address named, which outranks the filter
   *  (`presets.ts`), and which the body draws by its words so nothing the filter kept is lost
   *  (R19, R20). */
  readonly others: readonly TreeNode[];
  readonly statements: readonly StatementReading[];
}

/** How many characters of a commit the page draws, which is the short form the approved
 *  prototype draws ("a2ea3fd269"). */
export const COMMIT_CHARACTERS = 10;

/** A commit as the page draws it: its first ten characters, and the whole of a shorter one. */
export function shortCommit(sha: string): string {
  return sha.slice(0, COMMIT_CHARACTERS);
}

/** The one commit the page names for the issue's results (R17): the commit of the newest result
 *  that still stands and names one. The rule is `tree-drafts.ts`'s, beside `checkStanding`,
 *  since the tree reads every check's state at this commit too and cannot import this module. */
export { namedCommit };

/**
 * The reading the body draws: the one commit, each statement with its checks and their latest
 * results, how each statement stands at that commit, and how many carry a check.
 *
 * `view` is the tree the Verification filter left (`presets.ts`'s `presetView`): its top rows
 * are the statements the filter draws as context, each with the checks that serve it under it
 * and each check's results under that. `checks` is the issue's results read, which the page has
 * already made for its tree (`useIssueTree.ts`), and which is what holds a result's commit, its
 * verdict and its withdrawal; a check node is tied to its block by its own address, which the
 * tree carries (`tree-drafts.ts` writes `check.ref` into the node's `reference`).
 */
export function verificationReading({
  checks,
  view,
}: {
  readonly checks: readonly CheckBlock[];
  readonly view: TreeNode;
}): VerificationReading {
  const { commit, read } = checkReader(checks);
  const statements = view.children
    .filter((node) => node.type === "requirement")
    .map((node): StatementReading => {
      const readings = node.children.filter((child) => child.type === "check").map(read);
      return { checks: readings, node, state: statementState(readings) };
    });
  return {
    ...(commit === undefined ? {} : { commit }),
    covered: statements.filter((statement) => carriesCheck(statement.node, statement.checks))
      .length,
    loose: view.children.filter((node) => node.type === "check").map(read),
    others: view.children.filter((node) => node.type !== "requirement" && node.type !== "check"),
    statements,
  };
}

/**
 * Whether a statement carries a check (R17's covered): a check of this issue drawn under it,
 * or a check the coverage read counts for it that this issue's tree does not hold - a
 * descendant issue's, which R10 counts and this issue's results read does not answer for
 * (`coverage.go`, `TreeNode.counts`).
 */
function carriesCheck(node: TreeNode, checks: readonly CheckReading[]): boolean {
  // A proposed statement carries no check, and a proposed check, or one at or under an
  // overtaken node, covers no statement. The coverage read's count below still counts a proposed
  // check: leaving it out there is the server's.
  if (outOfCounts(node)) return false;
  checks = inCounts(checks);
  return checks.length > 0 || (node.counts?.checks ?? 0) > 0;
}

/** How one statement stands, for a view that draws no check under it. */
export interface StatementStanding {
  /** Whether it carries a check (R17's covered). */
  readonly covered: boolean;
  readonly state: StatementState;
}

/**
 * How every statement of the whole tree stands at the commit the page names, by its node id:
 * the reading the Verification filter draws beside each statement, for the Spec filter's mark,
 * so one statement never reads Met in one view and something else in the other (R17). `root`
 * is the whole tree, where each check sits under every statement it serves.
 */
export function statementStandings(
  root: TreeNode,
  checks: readonly CheckBlock[]
): ReadonlyMap<string, StatementStanding> {
  const { read } = checkReader(checks);
  const standings = new Map<string, StatementStanding>();
  const walk = (node: TreeNode): void => {
    if (node.type === "requirement" && !standings.has(node.id)) {
      const readings = node.children.filter((child) => child.type === "check").map(read);
      standings.set(node.id, {
        covered: carriesCheck(node, readings),
        state: statementState(readings),
      });
    }
    for (const child of node.children) walk(child);
  };
  walk(root);
  return standings;
}

/**
 * How a statement stands at the commit the page names: Met where every check of it passed
 * there, Not met where one failed there, and Unknown otherwise, which is where it carries no
 * check, where a check's evidence was recorded against another commit, and where a check
 * stands in any of the other five states (R17).
 */
export function statementState(checks: readonly CheckReading[]): StatementState {
  // A proposed check, and one at or under an overtaken node, serves no statement: it is neither
  // evidence for it nor against it.
  checks = inCounts(checks);
  if (checks.length === 0) return "unknown";
  if (checks.every((check) => check.state === "passed" && check.atNamedCommit)) return "met";
  if (checks.some((check) => check.state === "failed" && check.atNamedCommit)) return "not_met";
  return "unknown";
}

/** How many statements stand in each state, which the head draws as its counts and its bar. */
export function statementCounts(
  statements: readonly StatementReading[]
): Readonly<Record<StatementState, number>> {
  // A proposed statement, and one at or under an overtaken node, is in none of the three.
  statements = inCounts(statements);
  const counts: Record<StatementState, number> = { met: 0, not_met: 0, unknown: 0 };
  for (const statement of statements) counts[statement.state] += 1;
  return counts;
}

/** What a count reads of `readings`: each one but those whose node is a proposed statement or
 *  check or sits at or under an overtaken node (`outOfCounts`). */
export function inCounts<Reading extends { readonly node: TreeNode }>(
  readings: readonly Reading[]
): readonly Reading[] {
  return readings.filter((reading) => !outOfCounts(reading.node));
}

/** The commit the page names for `checks`' results, and how one check node of the tree reads at
 *  it (`checkReading`): the node is tied to its block by its own address, which the tree carries
 *  (`tree-drafts.ts` writes `check.ref` into the node's `reference`). */
function checkReader(checks: readonly CheckBlock[]): {
  readonly commit: string | undefined;
  readonly read: (node: TreeNode) => CheckReading;
} {
  const blocks = new Map(
    checks.filter((check) => check.ref !== "").map((check) => [check.ref, check])
  );
  const commit = namedCommit(checks);
  return {
    commit,
    read: (node) => checkReading(node, blocks.get(node.reference ?? ""), commit),
  };
}

/**
 * How one check stands at the commit the page names, and the result the body draws under it.
 *
 * The state, and the result it stands on, are `checkStanding`'s (`tree-drafts.ts`): the one
 * reading of a check the tree's node holds too, which is `checkState`'s answer turned over at
 * the named commit (R17). What this adds is the body's own: whether that result ran at the
 * named commit, the latest result the body draws under the check, and every other result, which
 * the body folds under it.
 */
function checkReading(
  node: TreeNode,
  block: CheckBlock | undefined,
  commit: string | undefined
): CheckReading {
  if (block === undefined) return { atNamedCommit: false, earlier: [], node };
  const { state, stands } = checkStanding(block, commit);
  const results = newestFirst(block.results ?? []);
  // What the body draws under the check: the newest result that stands, and where a retraction
  // left none standing the newest withdrawn one, which is what a retracted check has to show.
  const latest = results.find((result) => result.retracted === undefined) ?? results[0];
  const resultNodes = new Map(
    node.children.filter((child) => child.type === "result").map((child) => [child.id, child])
  );
  const drawnResult = latest === undefined ? undefined : resultNodes.get(latest.id);
  return {
    atNamedCommit: stands !== undefined && commit !== undefined && stands.commit_sha === commit,
    earlier: results.flatMap((result) => {
      const held = resultNodes.get(result.id);
      return held === undefined || result.id === latest?.id ? [] : [held];
    }),
    ...(drawnResult === undefined ? {} : { latest: drawnResult }),
    node,
    ...(stands === undefined ? {} : { standsOn: stands.id }),
    state,
  };
}
