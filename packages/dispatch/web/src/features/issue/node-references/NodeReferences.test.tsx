import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

import { graphNodeFields } from "../../../__tests__/graph-fixture";
import { api } from "../../../api/client";
import type { GraphReferences } from "../../../api/types";
import { MarginSlotProvider } from "../../margin/margin-slot";
import { NODE_REFERENCES_WORDS, NodeReferences } from "./NodeReferences";

const STATEMENT = "dispatch://LAY-2/spec#b-r1";

const ANSWER: GraphReferences = {
  edges: [
    {
      created_at: "2026-11-15T10:00:00Z",
      direction: "in",
      excerpt: { text: "Thread 1 opens here. Its second paragraph is a block of its own." },
      kind: "mentions",
      node: {
        ...graphNodeFields(),
        id: "11111111-1111-4111-8111-111111111111",
        issue_key: "LAY-2",
        kind: "comment",
        ref: "dispatch://LAY-2/comment/11111111-1111-4111-8111-111111111111",
      },
      source_seq: null,
    },
    {
      created_at: "2026-11-15T09:00:00Z",
      direction: "in",
      excerpt: {
        block_id: "c1",
        text:
          ':::check{#c1 serves="dispatch://LAY-2/spec#b-r1"}\n' +
          "Check 1 of the redesign holds its own row and its own state.\n" +
          ":::",
      },
      kind: "serves",
      node: {
        ...graphNodeFields(),
        block_id: "c1",
        document: "dispatch://LAY-2/artifact/verification-md",
        id: "verification-md#c1",
        issue_key: "LAY-2",
        kind: "block",
        ref: "dispatch://LAY-2/artifact/verification-md#b-c1",
      },
      source_seq: null,
    },
  ],
  node: {
    ...graphNodeFields(),
    block_id: "r1",
    id: "spec#r1",
    issue_key: "LAY-2",
    kind: "block",
    ref: STATEMENT,
  },
};

let restore: (() => void) | undefined;
afterEach(() => {
  restore?.();
  restore = undefined;
});

/** The props of one drawing: the row the reader is on and its own address. */
interface Drawn {
  readonly node: { readonly id: string; readonly type: "issue" | "requirement" };
  readonly reference: string | undefined;
}

const STATEMENT_ROW: Drawn = {
  node: { id: "spec#b-r1", type: "requirement" },
  reference: STATEMENT,
};

/** The component inside the shell it is drawn in: Details is the margin's drawer, open or
 *  shut, and the panel is inside it (`features/margin/margin-slot.tsx`). */
function draw(detailsOpen: boolean, drawn: Drawn = STATEMENT_ROW) {
  const getReferences = spyOn(api, "getReferences").mockResolvedValue(ANSWER);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={client}>
        <MarginSlotProvider drawer={{ open: detailsOpen, setOpen: () => {} }}>
          <NodeReferences node={drawn.node} reference={drawn.reference} />
        </MarginSlotProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  restore = () => {
    getReferences.mockRestore();
    view.unmount();
    client.clear();
  };
  return { getReferences, view };
}

test("the first drawing of a focused row reads nothing", async () => {
  const { getReferences } = draw(true);
  expect(
    await screen.findByRole("button", { name: NODE_REFERENCES_WORDS.what_references })
  ).toBeDefined();
  expect(getReferences.mock.calls).toHaveLength(0);
});

test("the reader opening the list reads it once, and every node that references the row is a link to it", async () => {
  const { getReferences } = draw(true);
  const control = await screen.findByRole("button", {
    name: NODE_REFERENCES_WORDS.what_references,
  });
  await userEvent.click(control);
  expect(getReferences.mock.calls).toEqual([[STATEMENT]]);
  expect(control.getAttribute("aria-expanded")).toBe("true");

  // The mention, which the outline draws nowhere, and the check, by the words of its block
  // with its directive line and its fence left out.
  const mention = await screen.findByRole("link", {
    name: "Thread 1 opens here.",
  });
  expect(mention.getAttribute("href")).toBe(
    "/issues/LAY-2/comments/11111111-1111-4111-8111-111111111111"
  );
  const check = screen.getByRole("link", {
    name: "Check 1 of the redesign holds its own row and its own state.",
  });
  expect(check.getAttribute("href")).toBe("/issues/LAY-2/artifacts/verification-md#b-c1");
  // What each edge is, in the page's own words: a tuple of the model by its own word, a
  // mention by the kind of node that holds it.
  const list = screen.getByRole("list");
  expect(list.textContent).toContain(NODE_REFERENCES_WORDS.edges.serves);
  expect(list.textContent).toContain(NODE_REFERENCES_WORDS.kinds.comment);
});

test("a list the reader opens while Details is shut reads nothing", async () => {
  const { getReferences } = draw(false);
  await userEvent.click(
    await screen.findByRole("button", { name: NODE_REFERENCES_WORDS.what_references })
  );
  expect(getReferences.mock.calls).toHaveLength(0);
  expect(screen.queryByRole("list")).toBeNull();
});

test("the focus moving to another row closes the list, so the next row costs no read", async () => {
  const { getReferences, view } = draw(true);
  await userEvent.click(
    await screen.findByRole("button", { name: NODE_REFERENCES_WORDS.what_references })
  );
  expect(getReferences.mock.calls).toHaveLength(1);
  view.rerender(
    <MemoryRouter>
      <QueryClientProvider
        client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
      >
        <MarginSlotProvider drawer={{ open: true, setOpen: () => {} }}>
          <NodeReferences
            node={{ id: "spec#b-r2", type: "requirement" }}
            reference="dispatch://LAY-2/spec#b-r2"
          />
        </MarginSlotProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  const control = await screen.findByRole("button", {
    name: NODE_REFERENCES_WORDS.what_references,
  });
  expect(control.getAttribute("aria-expanded")).toBe("false");
  expect(getReferences.mock.calls).toHaveLength(1);
});

test("the issue itself draws no list here, because Details' References row holds it", () => {
  const { getReferences } = draw(true, {
    node: { id: "LAY-2", type: "issue" },
    reference: "dispatch://LAY-2",
  });
  expect(screen.queryByRole("button", { name: NODE_REFERENCES_WORDS.what_references })).toBeNull();
  expect(getReferences.mock.calls).toHaveLength(0);
});

test("a row the model gives no address draws no list", () => {
  const { getReferences } = draw(true, {
    node: { id: "spec#b-r1", type: "requirement" },
    reference: undefined,
  });
  expect(screen.queryByRole("button", { name: NODE_REFERENCES_WORDS.what_references })).toBeNull();
  expect(getReferences.mock.calls).toHaveLength(0);
});
