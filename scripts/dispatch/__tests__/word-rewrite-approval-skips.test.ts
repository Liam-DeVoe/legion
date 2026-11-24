import { describe, expect, test } from "bun:test";
import {
  APPROVAL_SKIP_REASONS,
  type ApprovalFacts,
  type ApprovalSkipReason,
  approvalSkip,
  isApprovalSkip,
} from "../word-rewrite/approval-skips.ts";
import { Approval, Artifact } from "../word-rewrite/dispatch-api.ts";
import { itemPlace } from "../word-rewrite/places.ts";
import { fixture } from "./word-rewrite-fake.ts";

/**
 * The documents the tool leaves for a person's approval, as the approved spec (version 14) says:
 * the issue's spec that a person approved at its latest version. This file and
 * `approval-skips.ts` hold every approval reason, so that a change to them changes these two
 * files alone.
 */

function document(file: string, name: string): ApprovalFacts {
  const found = Artifact.array()
    .parse(fixture(file))
    .find((artifact) => artifact.name === name);
  if (found === undefined) throw new Error(`${file} lists no ${name}`);
  return { primary: found.primary === true, approval: found.approval, blocks: [] };
}

const ROWS: Record<ApprovalSkipReason, () => ApprovalSkipReason | null> = {
  "approved-by-person": () => approvalSkip(document("issue-artifacts-approved.json", "spec.md")),
};

describe("the documents a person's approval leaves", () => {
  test("every reason approval-skips.ts lists has a row here, and no row names another", () => {
    expect(Object.keys(ROWS).sort()).toEqual([...APPROVAL_SKIP_REASONS].sort());
    expect(APPROVAL_SKIP_REASONS.every((reason) => isApprovalSkip(reason))).toBe(true);
    expect(isApprovalSkip("approval-request")).toBe(false);
  });

  for (const reason of APPROVAL_SKIP_REASONS) {
    test(`a document the tool leaves with ${reason}`, () => {
      expect(ROWS[reason]()).toBe(reason);
    });
  }

  test("places.ts gives the reason for the whole spec", () => {
    const spec = document("issue-artifacts-approved.json", "spec.md");
    expect(itemPlace({ outOfScope: false, thisTree: false, closed: false, ...spec })).toBe(
      "approved-by-person"
    );
  });

  test("a document beside the spec that a person approved is changed: the rule is the spec's", () => {
    const verification = document("issue-artifacts-approved.json", "verification.md");
    expect(verification.primary).toBe(false);
    expect(approvalSkip(verification)).toBeNull();
  });

  test("a spec approved at an earlier version is changed", () => {
    const earlier = document("issue-artifacts-earlier.json", "earlier.md");
    expect(approvalSkip({ ...earlier, primary: true })).toBeNull();
  });

  test("a plan review's approval, a request for changes and a draft leave nothing", () => {
    for (const approval of [
      Approval.parse({ state: "approved", kind: "plan_review", latest_version: 1 }),
      Approval.parse({ state: "changes_requested", kind: "person", latest_version: 1 }),
      Approval.parse({ state: "draft", latest_version: 1 }),
      undefined,
    ]) {
      expect(approvalSkip({ primary: true, approval, blocks: [] })).toBeNull();
    }
  });
});
