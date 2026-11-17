import { beforeAll, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, render, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import {
  ApiError,
  api,
  type IssueTreeAnswer,
  type WriteBounds,
  type WriteRequest,
} from "../../../api/client";
import { issueCoverageQuery } from "../../../api/queries";
import { warmMarkdownRenderer } from "../../refs/markdown-engine";
import type { TreeNode } from "../issue-tree";
import { NodeRow } from "../NodeRow";
import { NoTooltips } from "../NoTooltips";
import { CONTROL_LABELS, SETTLE_ANSWERS } from "../page-words";
import { issueTreeQuery, subscribeTreeFreshness, viewQuery } from "../tree-read/tree-read";

/**
 * Settle on a focused proposed row (dispatch://INFRA-4063 R4), drawn by G's `RowActs` in the row
 * as the page draws it, beside the reads the page holds while the row is drawn: the tree read,
 * the coverage read and the one view a slot has on (Verification), each under T's own query
 * options, with the Spec view's read held from the load and read by nobody now, and T's
 * subscription (`subscribeTreeFreshness`) keeping them fresh as the page does. Each read and
 * each write is answered here, so the test reads which went out.
 */

const KEY = "CORE-1";
const REFERENCE = `dispatch://${KEY}/artifact/spec-md#b-r7`;
/** The settle's path: the node's whole address percent-encoded as one segment. */
const SETTLE_PATH =
  "/api/v1/issues/CORE-1/nodes/dispatch%3A%2F%2FCORE-1%2Fartifact%2Fspec-md%23b-r7/settle";
/** The Spec view's read, held from the load and read by nobody now, and the Verification view's,
 *  which a slot has on: each under T's key (`viewKey`), as `viewQuery` gives it. */
const HELD_VIEW = viewQuery(KEY, "spec").queryKey;
const OPEN_VIEW = viewQuery(KEY, "verification").queryKey;

/** Requirement 7, which an agent proposed and nobody has settled. */
const PROPOSED: TreeNode = {
  alsoUnder: [],
  children: [],
  chips: [],
  facts: [],
  gaps: [],
  held: 0,
  id: "spec-md#b-r7",
  life: "proposed",
  question: false,
  reference: REFERENCE,
  replaced: false,
  text: "Requirement 7",
  transcluded: false,
  type: "requirement",
  waiting: false,
};

/** One page of the tree read or of a view, which is all of it. */
function treeAnswer(): IssueTreeAnswer {
  return { converted: true, cursor: null, nodes: [], tree: null, tuples: [], view_of: null };
}

/** Dispatch's 200 to a settle. */
function settled(): unknown {
  return {
    node: { address: REFERENCE, kind: "requirement", state: "settled", visibility: "project" },
    previous: "proposed",
  };
}

/** The reads the page holds while the row is drawn: the tree, the coverage, and the view a slot
 *  has on. */
function PageReads(): ReactNode {
  useQuery(issueTreeQuery(KEY));
  useQuery(issueCoverageQuery(KEY));
  useQuery(viewQuery(KEY, "verification"));
  return null;
}

interface DrawnRow {
  readonly client: QueryClient;
  /** Each read that went out, in order: `tree`, a view's name, or `coverage`. */
  readonly reads: string[];
  /** Whether the focus was on the row as each read of `reads` went out. */
  readonly onRowAtRead: boolean[];
  /** Each settle that went out, as it went, and the key it went under. */
  readonly sent: WriteRequest[];
  readonly keys: string[];
  readonly settle: () => HTMLButtonElement;
  readonly sentence: () => Element | null;
  readonly row: () => HTMLElement;
  /** A control outside the row, where a test puts the focus before a press. */
  readonly elsewhere: () => HTMLButtonElement;
  readonly unmount: () => void;
}

/** The focused proposed row, its settle answered by `answer` at each attempt. */
function drawRow(answer: (attempt: number) => Promise<unknown>): DrawnRow {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  client.setQueryData(issueTreeQuery(KEY).queryKey, treeAnswer());
  client.setQueryData(HELD_VIEW, treeAnswer());
  client.setQueryData(OPEN_VIEW, treeAnswer());
  client.setQueryData(issueCoverageQuery(KEY).queryKey, { requirements: [] } as never);
  const reads: string[] = [];
  const onRowAtRead: boolean[] = [];
  const read = (what: string) => {
    reads.push(what);
    onRowAtRead.push(document.activeElement?.hasAttribute("data-node-row") === true);
  };
  const tree = spyOn(api, "getIssueTree").mockImplementation(async (_key, query) => {
    read(query?.view ?? "tree");
    return treeAnswer();
  });
  const coverage = spyOn(api, "getIssueCoverage").mockImplementation(async () => {
    read("coverage");
    return { requirements: [] } as never;
  });
  const sent: WriteRequest[] = [];
  const keys: string[] = [];
  const send = spyOn(api, "send").mockImplementation(((
    request: WriteRequest,
    bounds: WriteBounds
  ) => {
    sent.push(request);
    keys.push(bounds.key);
    return answer(sent.length);
  }) as typeof api.send);
  const unsubscribe = subscribeTreeFreshness(client, KEY);
  const view = render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <NoTooltips>
          <PageReads />
          <button data-elsewhere="" type="button">
            Elsewhere
          </button>
          <NodeRow
            author={{ kind: "person", name: "bob", reader: false }}
            body={false}
            context={false}
            focused
            isClosed={false}
            level={1}
            node={PROPOSED}
            onFocus={() => {}}
            onPin={() => {}}
            onToggle={() => {}}
            ownAnswer={false}
            pinned={false}
            rowKey="row"
            tabStop
          />
        </NoTooltips>
      </MemoryRouter>
    </QueryClientProvider>
  );
  const found = <E extends Element>(selector: string): E => {
    const element = view.container.querySelector<E>(selector);
    if (element === null) throw new Error(`the page draws no ${selector}`);
    return element;
  };
  return {
    client,
    elsewhere: () => found<HTMLButtonElement>("[data-elsewhere]"),
    keys,
    onRowAtRead,
    reads,
    row: () => found<HTMLElement>("[data-node-row]"),
    sent,
    sentence: () => view.container.querySelector("[data-settle-answer]"),
    settle: () => found<HTMLButtonElement>('[data-row-act="settle"]'),
    unmount: () => {
      view.unmount();
      unsubscribe();
      send.mockRestore();
      coverage.mockRestore();
      tree.mockRestore();
    },
  };
}

beforeAll(async () => {
  await warmMarkdownRenderer();
});

// R4: Enter or a press sends one `POST /api/v1/issues/{key}/nodes/{id}/settle`, `{id}` the
// node's `dispatch://` address percent-encoded as one segment, and no body: a cookie proves a
// person, so the act names no actor.
test("Settle sends the address and no body", async () => {
  const user = userEvent.setup();
  const drawn = drawRow(async () => settled());
  try {
    expect(drawn.settle().textContent).toBe(CONTROL_LABELS.settle);
    expect(drawn.settle().tabIndex).toBe(-1);
    await user.click(drawn.settle());
    await waitFor(() => expect(drawn.sent).toHaveLength(1));
    // `toStrictEqual`: the request holds no `body` at all, not even an undefined one.
    expect(drawn.sent).toStrictEqual([{ method: "POST", path: SETTLE_PATH }]);
    await waitFor(() => expect(drawn.settle().textContent).toBe(CONTROL_LABELS.settle));
    expect(drawn.sent).toHaveLength(1);
  } finally {
    drawn.unmount();
  }
});

// R4: on 200 the tree read and the coverage read are read again, the view read held for the
// load is dropped, and the open view is read again; the focus is on the row before any of them.
test("a 200 drops the held views and reads the open one again", async () => {
  const user = userEvent.setup();
  const landing = Promise.withResolvers<unknown>();
  const drawn = drawRow(() => landing.promise);
  try {
    act(() => drawn.elsewhere().focus());
    await user.click(drawn.settle());
    await waitFor(() => expect(drawn.sent).toHaveLength(1));
    // The press leaves the focus where it was, and nothing is read while the write is out.
    expect(document.activeElement).toBe(drawn.elsewhere());
    expect(drawn.reads).toEqual([]);
    await act(async () => landing.resolve(settled()));
    await waitFor(() => expect(drawn.reads).toContain("verification"));
    expect(drawn.reads).toContain("tree");
    expect(drawn.reads).toContain("coverage");
    expect(drawn.reads, "the view nobody reads now was read again").not.toContain("spec");
    expect(drawn.client.getQueryData(HELD_VIEW), "the held view stayed").toBeUndefined();
    await waitFor(() =>
      expect(drawn.client.getQueryData<IssueTreeAnswer>(OPEN_VIEW)).toEqual(treeAnswer())
    );
    expect(drawn.onRowAtRead, "a read went out before the focus was on the row").not.toContain(
      false
    );
    expect(document.activeElement).toBe(drawn.row());
    await waitFor(() => expect(drawn.settle().textContent).toBe(CONTROL_LABELS.settle));
    expect(drawn.sentence()).toBeNull();
  } finally {
    drawn.unmount();
  }
});

// R4: a refusal sends no read, leaves the row as drawn, its mark included, and draws one
// sentence after the act with the role `status`, read from the refusal's status and code and
// never from its text: the 403 with no code says, in its text, what a `SETTLE_NOT_PERMITTED`
// would, and draws the sentence of every other refusal.
test("each refusal says its sentence and leaves the mark", async () => {
  const user = userEvent.setup();
  const refusals: readonly (readonly [ApiError, string])[] = [
    [
      new ApiError(403, { code: "SETTLE_NOT_PERMITTED", error: "settle not permitted" }),
      SETTLE_ANSWERS.not_permitted,
    ],
    [
      new ApiError(409, { code: "NODE_NOT_PROPOSED", error: "node not proposed" }),
      SETTLE_ANSWERS.not_proposed,
    ],
    [new ApiError(404, { code: "NOT_FOUND", error: "not found" }), SETTLE_ANSWERS.gone],
    [new ApiError(400, { code: "INVALID_JSON", error: "invalid json" }), SETTLE_ANSWERS.failed],
    [new ApiError(403, { error: SETTLE_ANSWERS.not_permitted }), SETTLE_ANSWERS.failed],
  ];
  const drawn = drawRow((attempt) => Promise.reject(refusals[attempt - 1]?.[0]));
  try {
    for (const [index, [refusal, words]] of refusals.entries()) {
      await user.click(drawn.settle());
      await waitFor(() => {
        expect(drawn.sent).toHaveLength(index + 1);
        expect(drawn.sentence()?.textContent, `${refusal.status} ${refusal.code}`).toBe(words);
      });
      const sentence = drawn.sentence();
      expect(sentence?.getAttribute("role")).toBe("status");
      expect(drawn.settle().nextElementSibling).toBe(sentence);
      expect(drawn.settle().textContent).toBe(CONTROL_LABELS.settle);
      expect(drawn.row().querySelector('[data-node-mark="proposed"]')).not.toBeNull();
      expect(drawn.reads, "a refusal sent a read").toEqual([]);
      expect(drawn.client.getQueryData<IssueTreeAnswer>(HELD_VIEW)).toEqual(treeAnswer());
    }
  } finally {
    drawn.unmount();
  }
});

// R4 and the plan's "Settle": `null` (`409 KEY_COMMITTED`) is a landing, as a 200 is: no
// sentence, the focus on the row, the same reads, and the held view dropped.
test("a 409 KEY_COMMITTED lands as a 200", async () => {
  const user = userEvent.setup();
  const drawn = drawRow(() =>
    Promise.reject(new ApiError(409, { code: "KEY_COMMITTED", error: "already made" }))
  );
  try {
    act(() => drawn.elsewhere().focus());
    await user.click(drawn.settle());
    await waitFor(() => expect(drawn.reads).toContain("verification"));
    expect(drawn.reads).toContain("tree");
    expect(drawn.reads).toContain("coverage");
    expect(drawn.reads).not.toContain("spec");
    expect(drawn.client.getQueryData(HELD_VIEW)).toBeUndefined();
    await waitFor(() => expect(drawn.settle().textContent).toBe(CONTROL_LABELS.settle));
    expect(document.activeElement).toBe(drawn.row());
    expect(drawn.sentence()).toBeNull();
    expect(drawn.sent).toHaveLength(1);
  } finally {
    drawn.unmount();
  }
});

// R4: while the write is out the act says "Settling…" and takes no press; a 5xx is made again
// under the write's key, so the act goes on saying it while it is tried again. Pressed from the
// keyboard - `f` leaves the focus on it, and Enter presses it - the act hands the focus to its
// row first, so its taking no press drops no focus out of the tree.
test("Settling… is said while the write is out", async () => {
  const user = userEvent.setup();
  const landing = Promise.withResolvers<unknown>();
  const drawn = drawRow((attempt) =>
    attempt === 1 ? Promise.reject(new ApiError(500, { error: "boom" })) : landing.promise
  );
  try {
    act(() => drawn.settle().focus());
    await user.keyboard("{Enter}");
    expect(document.activeElement).toBe(drawn.row());
    await waitFor(() => expect(drawn.sent).toHaveLength(1));
    expect(drawn.settle().textContent).toBe(SETTLE_ANSWERS.settling);
    expect(drawn.settle().disabled).toBe(true);
    await waitFor(() => expect(drawn.sent).toHaveLength(2));
    expect(drawn.keys[1]).toBe(drawn.keys[0] ?? "");
    expect(drawn.settle().textContent).toBe(SETTLE_ANSWERS.settling);
    expect(drawn.settle().disabled).toBe(true);
    expect(drawn.sentence()).toBeNull();
    await act(async () => landing.resolve(settled()));
    await waitFor(() => expect(drawn.settle().textContent).toBe(CONTROL_LABELS.settle));
    expect(drawn.settle().disabled).toBe(false);
    expect(drawn.sentence()).toBeNull();
  } finally {
    drawn.unmount();
  }
});

// R4: the sentence stands until the reader's next act - a key, a press, a wheel or a touch, the
// four of the tree's `READER_ACTS` - and a pointer passing over the page is none.
test("the refusal's sentence goes at the reader's next act", async () => {
  const user = userEvent.setup();
  const drawn = drawRow(() =>
    Promise.reject(new ApiError(409, { code: "NODE_NOT_PROPOSED", error: "node not proposed" }))
  );
  // user-event has no wheel and no touch, so those two are dispatched as the browser sends them.
  const acts: readonly (readonly [string, () => Promise<void>])[] = [
    ["keydown", () => user.keyboard("j")],
    ["pointerdown", () => user.pointer({ keys: "[MouseLeft]", target: document.body })],
    [
      "wheel",
      async () => {
        act(() => {
          document.body.dispatchEvent(new Event("wheel", { bubbles: true }));
        });
      },
    ],
    [
      "touchstart",
      async () => {
        act(() => {
          document.body.dispatchEvent(new Event("touchstart", { bubbles: true }));
        });
      },
    ],
  ];
  try {
    for (const [name, readerAct] of acts) {
      await user.click(drawn.settle());
      await waitFor(() => expect(drawn.sentence()?.textContent).toBe(SETTLE_ANSWERS.not_proposed));
      await user.hover(drawn.row());
      expect(drawn.sentence(), "a pointer passing over ended the sentence").not.toBeNull();
      await readerAct();
      await waitFor(() => expect(drawn.sentence(), `${name} left the sentence`).toBeNull());
    }
    expect(drawn.reads).toEqual([]);
  } finally {
    drawn.unmount();
  }
});
