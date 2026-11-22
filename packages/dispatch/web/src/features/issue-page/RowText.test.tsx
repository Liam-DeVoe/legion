import { beforeAll, expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, waitFor } from "@testing-library/react";
import { type ReactNode, useMemo } from "react";
import { MemoryRouter } from "react-router-dom";

import { displayTree } from "../../__tests__/display-trees";
import type { DisplayTree } from "../../api/types";
import { warmMarkdownRenderer } from "../refs/markdown-engine";
import { NoTooltips } from "./NoTooltips";
import { RowText } from "./RowText";
import { ReferenceNamesProvider, usePublishReferenceNames } from "./reference-names";

/**
 * A row's words keep the link the reader is on through a read of the tree again (decision 385).
 * A later read hands a row the same words in objects of its own, which paint nothing again;
 * where the words do change, the new link of the same address takes the focus back; and a reader
 * who put the focus on nothing is given none.
 */

beforeAll(async () => {
  await warmMarkdownRenderer();
});

/** The names the page's one batched read brought, published as the page publishes them. */
function Named({ children }: { children: ReactNode }): ReactNode {
  const names = useMemo(() => new Map([["dispatch://CORE-2", "Draw the children"]]), []);
  usePublishReferenceNames(names);
  return children;
}

/** The row's words `markdown`, drawn from the reading `tree` of them. */
function row(markdown: string, tree: DisplayTree | undefined): ReactNode {
  return (
    <ReferenceNamesProvider>
      <Named>
        <RowText markdown={markdown} tree={tree} />
      </Named>
    </ReferenceNamesProvider>
  );
}

/** Draws `children` as the page's slots draw a row's parts, inside `NoTooltips`; `redraw` draws
 *  them again in the same tree, as a read of the tree again does. */
function draw(children: ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  const page = (inside: ReactNode) => (
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <NoTooltips>{inside}</NoTooltips>
      </MemoryRouter>
    </QueryClientProvider>
  );
  const view = render(page(children));
  return { ...view, redraw: (inside: ReactNode) => view.rerender(page(inside)) };
}

const CORE_2 = 'a[data-row-reference="dispatch://CORE-2"]';

/** The row's own link to CORE-2 in `container`, once the row's link pass has made it, with the
 *  focus put on it as the row's `f` puts it there. */
async function focusedLink(container: HTMLElement): Promise<HTMLAnchorElement> {
  const link = await waitFor(() => {
    const found = container.querySelector<HTMLAnchorElement>(CORE_2);
    expect(found).not.toBeNull();
    return found as HTMLAnchorElement;
  });
  act(() => link.focus());
  expect(document.activeElement).toBe(link);
  return link;
}

/** Lets a task go by, in which anything a draw started late would land. */
async function aTask(): Promise<void> {
  await act(async () => {
    const { promise, resolve } = Promise.withResolvers<void>();
    setTimeout(resolve, 0);
    await promise;
  });
}

test("a row's words drawn again from the same words in new objects keep the focused link", async () => {
  const text = "Draw the outline as CORE-2 says.";
  const reading = displayTree(text);
  const view = draw(row(text, reading));
  try {
    const link = await focusedLink(view.container);
    // A later read of the tree answers the same reading in objects of its own. Each reading the
    // test's engine makes gives its blocks ids of their own, so the copy is of the one reading.
    view.redraw(row(text, structuredClone(reading)));
    await aTask();
    expect(link.isConnected, "the same words drawn again replaced the link").toBe(true);
    expect(document.activeElement, "the same words drawn again took the focus").toBe(link);
  } finally {
    view.unmount();
  }
});

test("a row whose words change keeps the focus on the link of the same address", async () => {
  const text = "Draw the outline as CORE-2 says.";
  const changed = `${text} Then draw the margin.`;
  const view = draw(row(text, displayTree(text)));
  try {
    await focusedLink(view.container);
    view.redraw(row(changed, displayTree(changed)));
    await waitFor(() => expect(view.container.textContent).toContain("Then draw the margin."));
    await aTask();
    const focused = document.activeElement;
    expect(focused?.isConnected, "the focus is on a link the paint removed").toBe(true);
    expect(view.container.contains(focused), "the focus left the row's words").toBe(true);
    expect(focused?.matches(CORE_2), "the focus is not on the link to CORE-2").toBe(true);
  } finally {
    view.unmount();
  }
});

test("a reader who put the focus on nothing is given none by a paint of other words", async () => {
  const text = "Draw the outline as CORE-2 says.";
  const changed = `${text} Then draw the margin.`;
  const view = draw(row(text, displayTree(text)));
  try {
    const link = await focusedLink(view.container);
    // The reader leaves the link for nothing, as a press on the page's blank space does.
    act(() => link.blur());
    await aTask();
    expect(document.activeElement, "the link kept the focus the reader took off it").toBe(
      document.body
    );
    view.redraw(row(changed, displayTree(changed)));
    await waitFor(() => expect(view.container.textContent).toContain("Then draw the margin."));
    await aTask();
    expect(document.activeElement, "a paint of other words gave the focus back").toBe(
      document.body
    );
  } finally {
    view.unmount();
  }
});
