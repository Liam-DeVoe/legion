import type { IssueDecisions, IssueDetails } from "../../api/types";
import { waitingOnHuman } from "../inbox/BlockedOnYou";
import type { TreeNode } from "./issue-tree";

/**
 * What waits on the reader, as the header's count counts it (dispatch://INFRA-1196 R13): each
 * open ask of the issue whose turn is a person's, each decision in the Decisions filter's review
 * list, and each open approval request whose turn is a person's - three things and no other.
 * Whose turn an ask is, is the rule every count of the page's work already reads
 * (`waitingOnHuman`): the ask waits on a person, and on the issue's page that person is the
 * reader the indicator has always said "Waiting on you" to.
 */
export interface WaitingOnReader {
  /** The open asks other than an approval request whose turn is the reader's. */
  readonly asks: number;
  /** The decisions waiting for the reader's review. */
  readonly decisions: number;
  /** The documents an open approval request whose turn is the reader's covers, by artifact id:
   *  the header's approval control for each is where the count puts the focus when nothing
   *  else waits. */
  readonly approvals: readonly string[];
  /** All three, which is the number the count draws. */
  readonly total: number;
}

export function waitingOnReader(
  issue: Pick<IssueDetails, "artifacts" | "open_asks" | "primary_artifact_id">,
  decisions: IssueDecisions | undefined
): WaitingOnReader {
  const open = waitingOnHuman((issue.open_asks ?? []).filter((ask) => ask.state === "open"));
  const approvals = open
    .filter((ask) => ask.kind === "approval")
    .map(
      (ask) =>
        ask.artifact_id ??
        issue.artifacts?.find((artifact) => artifact.slug === ask.approval_artifact?.slug)?.id ??
        issue.primary_artifact_id
    );
  const asks = open.length - approvals.length;
  const reviewing = (decisions?.decisions ?? []).filter(
    (decision) => decision.waiting_review
  ).length;
  return { approvals, asks, decisions: reviewing, total: asks + reviewing + approvals.length };
}

/**
 * The first node of `root` that waits on the reader, in the order the outline draws it: an open
 * ask whose turn is the reader's, or a decision waiting for their review (`TreeNode.waiting`).
 * The Decisions filter's view is the root the count hands this, so it is the filter's order.
 */
export function firstWaitingNode(root: TreeNode): string | undefined {
  for (const child of root.children) {
    if (child.waiting && (child.type === "ask" || child.type === "decision")) return child.id;
    const below = firstWaitingNode(child);
    if (below !== undefined) return below;
  }
  return undefined;
}

/** What the count leaves in the address's history entry, which the outline reads to put the
 *  focus on what waits once it has drawn it: one press, told apart from the next by `press`. */
export interface WaitingPress {
  readonly waitingPress: number;
}

/** The press `state` carries, where the history entry is the count's own. */
export function waitingPressOf(state: unknown): number | undefined {
  if (typeof state !== "object" || state === null || !("waitingPress" in state)) return undefined;
  const press = state.waitingPress;
  return typeof press === "number" ? press : undefined;
}
