import { afterEach, beforeAll, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { displayTree } from "../../__tests__/display-trees";
import { graphNodeFields } from "../../__tests__/graph-fixture";
import { api } from "../../api/client";
import type { IssueReferenceSummary } from "../../api/types";
import { MarkdownBody } from "./MarkdownBody";
import { warmMarkdownRenderer } from "./markdown-engine";
import { PlainText } from "./plain-text";
import {
  type PublishedReferences,
  ReferenceNamesProvider,
  usePublishReferenceNames,
} from "./reference-names";

// The rows draw through the shared renderer, whose Markdown engine loads lazily, and they run in
// random order, so the file loads the engine before its first row, whichever row that is.
beforeAll(async () => {
  await warmMarkdownRenderer();
});

/**
 * The plain-text projection (`plain-text.tsx`) under a page that publishes its batched read
 * (`reference-names.tsx`): how often a line is drawn, and what it reads a reference as by R8's
 * one rule, beside the painted link that reads the same target.
 */

/** The spies a row put on the API client, taken off again once it ends. */
const spies: { mockRestore(): void }[] = [];

afterEach(() => {
  for (const spy of spies.splice(0)) {
    spy.mockRestore();
  }
});

/** Publishes `published` for as long as it is drawn, as the page publishes its batched read. */
function Publisher({ published }: { published: PublishedReferences }): ReactNode {
  usePublishReferenceNames(published);
  return null;
}

/** What a page publishes of its batched read: made anew on every call, as the page's own read
 *  makes its sets anew each time any of its reads answers. */
function publication({
  names = new Map(),
  unnamed = [],
}: {
  names?: ReadonlyMap<string, string>;
  unnamed?: readonly string[];
}): PublishedReferences {
  return {
    ambiguous: new Set(),
    asked: new Set([...names.keys(), ...unnamed]),
    gone: new Set(),
    readings: new Map([...names].map(([reference, words]) => [reference, { words }])),
    unnamed: new Set(unnamed),
  };
}

/** Draws `children` under the names store with `published` published, as the shell and a page
 *  that has read its references draw a surface. */
function page(client: QueryClient, published: PublishedReferences, children: ReactNode) {
  return (
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <ReferenceNamesProvider>
          <Publisher published={published} />
          {children}
        </ReferenceNamesProvider>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

function client(): QueryClient {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

// The architect's ruling on the CI row `margin-thread-reply`, which lost its click to a margin
// that changed shape between the press's aim and the press: a line sets nothing while it is
// drawn, so it is drawn once for each change of what it reads and never twice for one - the
// projection's answer, the store's name landing, the projection read again with it - and a page
// that publishes the same references again, in sets made anew, draws no line again.
test("a plain line is drawn once for each change of what it reads, and never for the same names published anew", async () => {
  const reference = "dispatch://CORE-1";
  const names = new Map([[reference, "Fix the thing"]]);
  const drawn: (string | undefined)[] = [];
  // One element for every draw of the page, so the line draws again only for what it reads.
  const line = (
    <PlainText markdown={`Tied to ${reference} here.`}>
      {(text) => {
        drawn.push(text);
        return <span data-testid="line">{text ?? ""}</span>;
      }}
    </PlainText>
  );
  const queries = client();
  const view = render(page(queries, publication({ names }), line));
  try {
    await waitFor(() =>
      expect(view.getByTestId("line").textContent).toBe("Tied to CORE-1 Fix the thing here.")
    );
    expect(drawn, "the line was drawn again for nothing it reads").toEqual([
      undefined,
      `Tied to ${reference} here.`,
      `Tied to ${reference} here.`,
      "Tied to CORE-1 Fix the thing here.",
    ]);
    const settled = drawn.length;
    view.rerender(page(queries, publication({ names }), line));
    expect(drawn.length - settled, "the same names published anew drew the line again").toBe(0);
  } finally {
    view.unmount();
  }
});

/** CORE-2, as its summary answers it. */
const CORE_2: IssueReferenceSummary = {
  documents: [],
  key: "CORE-2",
  project: "CORE",
  status: "in_progress",
  title: "Drain the queue",
};

// The architect's ruling on `publishedAnswer`, by R8: a batched read that answered for a target
// and sent no words for it - another issue's block - has not answered that the target has no
// name. A plain line reads the target itself, as the painted link to it does, by the same query,
// so the two read one target by one request and name it in the same words.
test("a plain line and a link name another issue's block the batch answered with no words alike, by one read", async () => {
  const block = "dispatch://CORE-2/spec#b-r1";
  const text = "Keep the queue drained.";
  const getReferences = spyOn(api, "getReferences").mockResolvedValue({
    edges: [],
    node: {
      ...graphNodeFields(),
      block_id: "r1",
      document: "dispatch://CORE-2/spec",
      id: "CORE-2/spec#r1",
      issue_key: "CORE-2",
      kind: "block",
      project: "CORE",
      ref: block,
      text,
      text_tree: displayTree(text),
    },
  });
  spies.push(getReferences, spyOn(api, "getIssueSummary").mockResolvedValue(CORE_2));
  const markdown = `See ${block} first`;
  const view = render(
    page(
      client(),
      publication({ unnamed: [block] }),
      <>
        <PlainText markdown={markdown}>
          {(line) => <span data-testid="line">{line ?? ""}</span>}
        </PlainText>
        <MarkdownBody markdown={markdown} />
      </>
    )
  );
  try {
    await within(view.container).findByRole("link", { name: text });
    await waitFor(() => expect(view.getByTestId("line").textContent).toBe(`See ${text} first`));
    expect(getReferences, "the line and the link read the block twice").toHaveBeenCalledTimes(1);
  } finally {
    view.unmount();
  }
});

// Decisions 66 and 88 over a nested list: an item of an ordered list opens on its number, and
// where its first block is a list of its own, the number stands before the first words the item
// holds, as a rendered body draws it, never on a line of its own.
test("an ordered item that opens a list of its own reads its number before its first words", async () => {
  const markdown = "1. - a\n   - b\n2. c";
  const view = render(
    page(
      client(),
      publication({}),
      <>
        <PlainText markdown={markdown}>
          {(line) => <span data-testid="whole">{line ?? ""}</span>}
        </PlainText>
        <PlainText firstLine markdown={markdown}>
          {(line) => <span data-testid="first">{line ?? ""}</span>}
        </PlainText>
        <PlainText markdown={markdown} tree={displayTree(markdown)}>
          {(line) => <span data-testid="read">{line ?? ""}</span>}
        </PlainText>
      </>
    )
  );
  try {
    await waitFor(() => expect(view.getByTestId("whole").textContent).toBe("1. a b 2. c"));
    expect(view.getByTestId("first").textContent).toBe("1. a");
    expect(view.getByTestId("read").textContent).toBe("1. a b 2. c");
  } finally {
    view.unmount();
  }
});
