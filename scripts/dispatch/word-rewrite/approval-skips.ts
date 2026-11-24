import type { Approval, Block } from "./dispatch-api.ts";

/**
 * The documents the tool leaves whole because of a person's approval (R6 at spec version 18 and
 * later): a document a person approved at its latest version, whatever document it is, and a
 * document a person approved at an earlier version that holds an ask block not yet answered. A new
 * version of the first takes the person's approval away, and for a Legion root a spec or a gated
 * Verification document then holds the tree's work until the person approves again; a person's
 * answer to the second carries the approval to its next version only when that answer is the
 * whole change since the approved one.
 *
 * Dispatch tells both from the document's `approval` (`applyApprovals` in
 * `packages/envoy/internal/dispatch/api/reviews.go`): `state` is `approved` when the latest review
 * approves the latest version, and `stale` when it approves an earlier one; `kind` is `person` for
 * a person's review. An ask block's `state` attribute is `open`, `answered` or `resolved`, and
 * `open` when the block carries none (`schema/blocks.json`'s default; `setAskServerAttributes` in
 * `docs/ask_blocks.go`).
 *
 * This file and its own test hold every approval reason, so that a change to which approvals the
 * tool keeps changes these two files alone.
 */

export const APPROVAL_SKIP_REASONS = ["approved-by-person", "approved-earlier-open-ask"] as const;

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

/** Whether a block is an ask that no answer or resolution has closed. */
export function isOpenAsk(block: Block): boolean {
  if (block.type !== "ask") return false;
  const state = block.attributes?.find((attribute) => attribute.name === "state")?.value;
  return state === undefined || state === "open";
}

/** The approval reason that leaves a whole document, or null. */
export function approvalSkip(document: ApprovalFacts): ApprovalSkipReason | null {
  const approval = document.approval;
  if (approval?.kind !== "person") return null;
  if (approval.state === "approved") return "approved-by-person";
  if (approval.state === "stale" && document.blocks.some(isOpenAsk)) {
    return "approved-earlier-open-ask";
  }
  return null;
}
