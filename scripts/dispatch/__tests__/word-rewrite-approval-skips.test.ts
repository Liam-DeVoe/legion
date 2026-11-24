import { describe, expect, test } from "bun:test";
import {
  APPROVAL_SKIP_REASONS,
  type ApprovalFacts,
  type ApprovalSkipReason,
  approvalSkip,
  isApprovalSkip,
  isOpenAsk,
} from "../word-rewrite/approval-skips.ts";
import { Approval, Artifact, type Block, Text } from "../word-rewrite/dispatch-api.ts";
import { itemPlace } from "../word-rewrite/places.ts";
import { fixture } from "./word-rewrite-fake.ts";

/**
 * The documents the tool leaves for a person's approval (spec version 18 and later): approved by a
 * person at the latest version, whatever the document, or approved by a person at an earlier
 * version and holding an ask block not yet answered. This file and `approval-skips.ts` hold every
 * approval reason, so that a change to them changes these two files alone.
 */

/** The blocks of `text-blocks.json`, which hold an ask block whose state is `open`. */
const BLOCKS_WITH_OPEN_ASK = Text.parse(fixture("text-blocks.json")).blocks ?? [];

function document(file: string, name: string, blocks: readonly Block[] = []): ApprovalFacts {
  const found = Artifact.array()
    .parse(fixture(file))
    .find((artifact) => artifact.name === name);
  if (found === undefined) throw new Error(`${file} lists no ${name}`);
  return { primary: found.primary === true, approval: found.approval, blocks };
}

function answered(blocks: readonly Block[]): Block[] {
  return blocks.map((block) =>
    block.type === "ask"
      ? {
          ...block,
          attributes: [
            ...(block.attributes ?? []).filter((attribute) => attribute.name !== "state"),
            { name: "state", value: "answered" },
          ],
        }
      : block
  );
}

const ROWS: Record<ApprovalSkipReason, () => ApprovalSkipReason | null> = {
  "approved-by-person": () => approvalSkip(document("issue-artifacts-approved.json", "spec.md")),
  "approved-earlier-open-ask": () =>
    approvalSkip(document("issue-artifacts-earlier.json", "earlier.md", BLOCKS_WITH_OPEN_ASK)),
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

  test("places.ts gives both reasons for a whole document", () => {
    const verification = document("issue-artifacts-approved.json", "verification.md");
    expect(itemPlace({ outOfScope: false, thisTree: false, closed: false, ...verification })).toBe(
      "approved-by-person"
    );
    const earlier = document("issue-artifacts-earlier.json", "earlier.md", BLOCKS_WITH_OPEN_ASK);
    expect(itemPlace({ outOfScope: false, thisTree: false, closed: false, ...earlier })).toBe(
      "approved-earlier-open-ask"
    );
  });

  test("a document beside the spec that a person approved at its latest version is left too", () => {
    const verification = document("issue-artifacts-approved.json", "verification.md");
    expect(verification.primary).toBe(false);
    expect(approvalSkip(verification)).toBe("approved-by-person");
  });

  test("approved at an earlier version with every ask answered is changed", () => {
    const earlier = document("issue-artifacts-earlier.json", "earlier.md");
    expect(approvalSkip({ ...earlier, blocks: answered(BLOCKS_WITH_OPEN_ASK) })).toBeNull();
    expect(approvalSkip(earlier)).toBeNull();
  });

  test("an ask block with no state attribute is open, as Dispatch's default is", () => {
    const bare = { id: "a", type: "ask", from: 0, to: 1, token: "t" };
    expect(isOpenAsk(bare)).toBe(true);
    expect(isOpenAsk({ ...bare, attributes: [{ name: "state", value: "resolved" }] })).toBe(false);
    expect(isOpenAsk({ ...bare, type: "check" })).toBe(false);
  });

  test("a plan review's approval, a request for changes and a draft leave nothing", () => {
    for (const approval of [
      Approval.parse({ state: "approved", kind: "plan_review", latest_version: 1 }),
      Approval.parse({ state: "changes_requested", kind: "person", latest_version: 1 }),
      Approval.parse({ state: "draft", latest_version: 1 }),
      undefined,
    ]) {
      expect(approvalSkip({ primary: true, approval, blocks: BLOCKS_WITH_OPEN_ASK })).toBeNull();
    }
  });
});
