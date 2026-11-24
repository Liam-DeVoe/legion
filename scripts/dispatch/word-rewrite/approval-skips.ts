import type { Approval, Block } from "./dispatch-api.ts";

/**
 * The documents the tool leaves whole because of a person's approval: the spec of an issue (its
 * primary document) that a person approved at its latest version, as the approved spec says (R6
 * at version 14). A new version of that spec takes the person's approval away, and for a Legion
 * root the daemon then holds the tree's work until the person approves again.
 *
 * Dispatch tells it from the document's `approval` (`applyApprovals` in
 * `packages/envoy/internal/dispatch/api/reviews.go`): `state` is `approved` when the latest review
 * approves the latest version, and `kind` is `person` for a person's review.
 *
 * This file and its own test hold every approval reason, so that a change to which approvals the
 * tool keeps changes these two files alone.
 */

export const APPROVAL_SKIP_REASONS = ["approved-by-person"] as const;

export type ApprovalSkipReason = (typeof APPROVAL_SKIP_REASONS)[number];

/** What a document's approval reason is read from: whether it is its issue's primary document,
 *  its approval, and its blocks as its text was read. */
export interface ApprovalFacts {
  readonly primary: boolean;
  readonly approval?: Approval | undefined;
  readonly blocks: readonly Block[];
}

/** Whether a reason is one of this file's. */
export function isApprovalSkip(reason: string): reason is ApprovalSkipReason {
  return APPROVAL_SKIP_REASONS.some((skip) => skip === reason);
}

/** The approval reason that leaves a whole document, or null. */
export function approvalSkip(document: ApprovalFacts): ApprovalSkipReason | null {
  const approval = document.approval;
  if (!document.primary || approval?.kind !== "person") return null;
  return approval.state === "approved" ? "approved-by-person" : null;
}
