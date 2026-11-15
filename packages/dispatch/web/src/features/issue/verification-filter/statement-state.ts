import type { CheckBlock, CheckResult } from "../../../api/types";
import type { TreeNode } from "../issue-tree";
import type { CheckState } from "../page-words";
import { checkState } from "../tree-drafts";

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
 * How one check stands is `checkState`'s, the page's one reading of it, so the body, the head's
 * checks line and the outline's rows all say the same word. This module turns one of its
 * answers over: a pass or a fail recorded against another commit than the one the page names
 * reads "Out of date" here, because `checkState` compares a result with the basis its own check
 * carries (`compared_with`), and R17 reads every result of the issue against the one commit the
 * page names.
 *
 * The model holds no React and makes no request: the body draws it, and the unit rows read the
 * same functions.
 */

/** How a statement stands at the commit the page names (R17). */
export type StatementState = "met" | "not_met" | "unknown";

/** The three, in the order the head counts them. */
export const STATEMENT_STATES: readonly StatementState[] = ["met", "not_met", "unknown"];

/** One check of a statement, as the body draws it. */
export interface CheckReading {
  /** Whether the result its state stands on ran at the commit the page names, which is what
   *  "passed at that commit" and "failed there" are read from (R17). */
  readonly atNamedCommit: boolean;
  /** The result the body draws under the check: the newest that still stands, or, where every
   *  result of it was withdrawn, the newest withdrawn one, since that is what the reader is
   *  owed. Absent where the check has no result at all, and where the results read has not
   *  answered for the check. */
  readonly latest?: TreeNode;
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
 *  picture draws ("a2ea3fd69"). */
export const COMMIT_CHARACTERS = 9;

/** A commit as the page draws it: its first nine characters, and the whole of a shorter one. */
export function shortCommit(sha: string): string {
  return sha.slice(0, COMMIT_CHARACTERS);
}

/**
 * The one commit the page names for the issue's results: the commit of the newest result
 * recorded against any check of the issue that still stands and names one. A result nobody
 * withdrew is the evidence the page reads, so a withdrawn result names no commit here, and a
 * result recorded with no commit at all names none either.
 */
export function namedCommit(checks: readonly CheckBlock[]): string | undefined {
  const named = checks.flatMap((check) =>
    standingResults(check).filter((result) => result.commit_sha !== "")
  );
  return newestFirst(named)[0]?.commit_sha;
}

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
  const blocks = new Map(
    checks.filter((check) => check.ref !== "").map((check) => [check.ref, check])
  );
  const commit = namedCommit(checks);
  const read = (node: TreeNode): CheckReading =>
    checkReading(node, blocks.get(node.reference ?? ""), commit);
  const statements = view.children
    .filter((node) => node.type === "requirement")
    .map((node): StatementReading => {
      const readings = node.children.filter((child) => child.type === "check").map(read);
      return { checks: readings, node, state: statementState(readings) };
    });
  return {
    ...(commit === undefined ? {} : { commit }),
    covered: statements.filter((statement) => statement.checks.length > 0).length,
    loose: view.children.filter((node) => node.type === "check").map(read),
    others: view.children.filter((node) => node.type !== "requirement" && node.type !== "check"),
    statements,
  };
}

/**
 * How a statement stands at the commit the page names: Met where every check of it passed
 * there, Not met where one failed there, and Unknown otherwise, which is where it carries no
 * check, where a check's evidence was recorded against another commit, and where a check
 * stands in any of the other five states (R17).
 */
export function statementState(checks: readonly CheckReading[]): StatementState {
  if (checks.length === 0) return "unknown";
  if (checks.every((check) => check.state === "passed" && check.atNamedCommit)) return "met";
  if (checks.some((check) => check.state === "failed" && check.atNamedCommit)) return "not_met";
  return "unknown";
}

/** How many statements stand in each state, which the head draws as its counts and its bar. */
export function statementCounts(
  statements: readonly StatementReading[]
): Readonly<Record<StatementState, number>> {
  const counts: Record<StatementState, number> = { met: 0, not_met: 0, unknown: 0 };
  for (const statement of statements) counts[statement.state] += 1;
  return counts;
}

/**
 * How one check stands at the commit the page names, and the result the body draws under it.
 *
 * The state is `checkState`'s, with one turn of its own: a pass or a fail whose result was
 * recorded against another commit than the one the page names reads "Out of date", because
 * that result no longer speaks for the work the rest of the page is read against, and R17 says
 * such a result makes its statement Unknown. A result that names no commit at all turns
 * nothing over: the page has no ground to call it older, and it is no evidence at the named
 * commit either.
 *
 * Which result the state stands on is the state's own: a check that passed stands on the
 * newest pass that still stands, since a pass recorded before a later fail is what the
 * server's `pass_stands` answered over (`tree-drafts.ts`); every other check stands on the
 * newest result that stands at all.
 */
function checkReading(
  node: TreeNode,
  block: CheckBlock | undefined,
  commit: string | undefined
): CheckReading {
  if (block === undefined) return { atNamedCommit: false, node };
  const standing = newestFirst(standingResults(block));
  const state = checkState(block);
  const stands =
    state === "passed" ? standing.find(({ verdict }) => verdict === "pass") : standing[0];
  const elsewhere =
    commit !== undefined &&
    stands !== undefined &&
    stands.commit_sha !== "" &&
    stands.commit_sha !== commit;
  const drawn = elsewhere && (state === "passed" || state === "failed") ? "older" : state;
  // What the body draws under the check: the newest result that stands, and where a retraction
  // left none standing the newest withdrawn one, which is what a retracted check has to show.
  const latest = standing[0] ?? newestFirst(block.results ?? [])[0];
  const drawnResult =
    latest === undefined ? undefined : node.children.find((child) => child.id === latest.id);
  return {
    atNamedCommit: stands !== undefined && commit !== undefined && stands.commit_sha === commit,
    ...(drawnResult === undefined ? {} : { latest: drawnResult }),
    node,
    state: drawn,
  };
}

/** Every result of `check` nobody withdrew, which is every result that stands as evidence. */
function standingResults(check: CheckBlock): readonly CheckResult[] {
  return (check.results ?? []).filter((result) => result.retracted === undefined);
}

/** `results` newest first, by the time each was recorded. The server already answers newest
 *  first; this reads the times themselves, so the order is the model's own and no caller
 *  depends on the order a read happened to arrive in. */
function newestFirst(results: readonly CheckResult[]): readonly CheckResult[] {
  return [...results].sort((first, second) => recordedAt(second) - recordedAt(first));
}

/** When a result was recorded, and the oldest possible time for one whose time cannot be read,
 *  so a result with no readable time never outranks one that has one. */
function recordedAt(result: CheckResult): number {
  const time = Date.parse(result.created_at);
  return Number.isNaN(time) ? Number.NEGATIVE_INFINITY : time;
}
