import { type Mock, spyOn } from "bun:test";

import { api } from "../../api/client";

/** Pinned's reads of the issue's questions, comments and messages (`emptyDetailsReads`), which a
 *  case asserts on or answers with the items its pins name. */
export interface PinReads {
  readonly asks: Mock<typeof api.listIssueAsks>;
  readonly comments: Mock<typeof api.listComments>;
  readonly messages: Mock<typeof api.listIssueMessages>;
}

/** The spies `emptyDetailsReads` sets, and the one call that puts every one of them back. */
export interface DetailsReads {
  readonly pinReads: PinReads;
  readonly restore: () => void;
}

/**
 * For unit tests only: answers every read open Details makes of the fixture's issue CORE-1 with
 * the API's empty shape, all but the issue's own read and the reader's state, which each caller
 * answers with what its cases hold. A read that reached the network would fail the case
 * (`__tests__/network.ts`). The caller puts every read back with `restore`.
 */
export function emptyDetailsReads(): DetailsReads {
  const reads = [
    spyOn(api, "whoAmI").mockResolvedValue({ event_head: "0", kind: "user", login: "alice" }),
    spyOn(api, "listProjects").mockResolvedValue([]),
    spyOn(api, "listAgents").mockResolvedValue([]),
    spyOn(api, "listProjectArchitectureSources").mockResolvedValue([]),
    spyOn(api, "getReferenceTargets").mockResolvedValue({ targets: [] }),
    // Details' People rows count the issue's subscribed agents on every load (`details-more.tsx`).
    spyOn(api, "getIssueSubscribers").mockResolvedValue([]),
    // Details' history reads the issue's log while Details is open (`IssueHistory.tsx`).
    spyOn(api, "getIssueEvents").mockResolvedValue([]),
    // Details' Steps reads the issue's step records while Details is open (`DetailsSteps.tsx`).
    spyOn(api, "listSteps").mockResolvedValue({ issue: "CORE-1", tasks: [] }),
  ];
  const pinReads: PinReads = {
    asks: spyOn(api, "listIssueAsks").mockResolvedValue([]),
    comments: spyOn(api, "listComments").mockResolvedValue([]),
    messages: spyOn(api, "listIssueMessages").mockResolvedValue([]),
  };
  return {
    pinReads,
    restore: () => {
      for (const spy of reads) spy.mockRestore();
      for (const spy of Object.values(pinReads)) spy.mockRestore();
    },
  };
}
