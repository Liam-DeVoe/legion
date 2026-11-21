import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";

import { useMarkdownHeadline } from "../refs/markdown-engine";
import {
  type KnownReferences,
  type PublishedReferences,
  type ReferenceNameReading,
  ReferenceNamesProvider,
  useKnownReferences,
  usePublishReferenceNames,
} from "../refs/reference-names";
import { parseNodeReference } from "../refs/routes";
import {
  chipName,
  type IssueTreeReads,
  lineReading,
  type NodeChip,
  targetNaming,
} from "./issue-tree";

/**
 * What the shared reader makes of what the tree holds, read through the code the page's
 * surfaces draw with, for a test of the tree that holds a node or a chip rather than a row drawn
 * of it: a node's one line, by the projection every one line of a node is drawn with
 * (`useMarkdownHeadline`, `lineReading`); the names the store projects from what a page publishes
 * (`ReferenceNamesProvider`); and a chip's name, cut as a chip cuts it (`chipName`). The engine
 * must have loaded (`warmMarkdownRenderer`), so each is answered in the render that draws it.
 */

/** The words a node's one line is cut from (`NodeRow.tsx`'s `useNodeLine`): the reading
 *  `lineReading` names, projected by the shared reader with no reference named, so each reads as
 *  the words its author wrote, and nothing for a reading that holds none. */
export function lineWordsOf(node: Parameters<typeof lineReading>[0]): string {
  const reading = lineReading(node);
  if ("none" in reading) return "";
  const parsed = "markdown" in reading;
  const view = renderHook(() =>
    useMarkdownHeadline(
      "tree" in reading ? reading.tree : undefined,
      Number.POSITIVE_INFINITY,
      parsed ? reading.markdown : undefined,
      undefined,
      parsed
    )
  );
  const words = view.result.current?.text ?? "";
  view.unmount();
  return words;
}

/** Publishes `published` for as long as it is drawn, as a page publishes its batched read. */
export function Publisher({ published }: { published: PublishedReferences }): ReactNode {
  usePublishReferenceNames(published);
  return null;
}

/** What a page publishes where its batched read named each of `names`' references by its name,
 *  as words the store draws as they stand, and answered nothing else: every one of those is a
 *  reference it asks for, and so is each of `asked`, which it has named nothing for yet. */
export function publishedNames(
  names: ReadonlyMap<string, string>,
  { asked = [] }: { asked?: readonly string[] } = {}
): PublishedReferences {
  return {
    ambiguous: new Set(),
    asked: new Set([...names.keys(), ...asked]),
    gone: new Set(),
    readings: new Map([...names].map(([reference, words]) => [reference, { words }])),
    unnamed: new Set(),
  };
}

/** A wrapper that draws what it is handed under the names store, with `names` published as a
 *  page publishes its batched read (`publishedNames`), and a query client that holds nothing:
 *  what a surface of the page reads a reference by. */
export function publishingNames(
  names: ReadonlyMap<string, string>
): (props: { children: ReactNode }) => ReactNode {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const published = publishedNames(names);
  return ({ children }) => (
    <QueryClientProvider client={client}>
      <ReferenceNamesProvider>
        <Publisher published={published} />
        {children}
      </ReferenceNamesProvider>
    </QueryClientProvider>
  );
}

/** The names the store projects from `readings`, by reference, as every surface under the shell
 *  reads them: each whole, and none for a reading that projects to no words. */
export function projectedNames(
  readings: ReadonlyMap<string, ReferenceNameReading>
): ReadonlyMap<string, string> {
  const published: PublishedReferences = {
    ambiguous: new Set(),
    asked: new Set(readings.keys()),
    gone: new Set(),
    readings,
    unnamed: new Set(),
  };
  let known: KnownReferences | undefined;
  const Reader = (): ReactNode => {
    known = useKnownReferences();
    return null;
  };
  const view = render(
    <ReferenceNamesProvider>
      <Publisher published={published} />
      <Reader />
    </ReferenceNamesProvider>
  );
  const names = known?.names ?? new Map<string, string>();
  view.unmount();
  return names;
}

/** The names the page publishes from `reads` (`targetNaming`), as the store projects them. */
export function namesOf(reads: IssueTreeReads): ReadonlyMap<string, string> {
  return projectedNames(targetNaming(reads).readings);
}

/** The name a chip draws for its target: the name the store projects from the chip's reading,
 *  cut as a chip cuts it (`chipName`), and `""` where it has none. */
export function nameOf(chip: NodeChip): string {
  if (chip.reading === undefined) return "";
  const name = projectedNames(new Map([[chip.ref, chip.reading]])).get(chip.ref) ?? "";
  return chipName(parseNodeReference(chip.ref), name);
}
