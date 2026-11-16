import { createContext, type ReactNode, useContext, useEffect, useState } from "react";

/**
 * The names a page has already read for the references it draws, shared with every surface the
 * shell mounts beside it (dispatch://INFRA-1196 R20).
 *
 * A link to a block shows the block's own words, and reading them one link at a time is one
 * `GET /api/v1/references` per pointer - the read `GET /api/v1/reference-targets` exists to
 * replace ("the read a page that draws a chip per reference makes once instead of one
 * `GET /api/v1/references` per chip", `packages/contracts/AGENTS.md`). A page that has made that
 * batched read publishes its answer here, and every link under the shell reads its own name from
 * it instead of asking for it again. A page that publishes nothing leaves every link exactly as
 * it was, reading its own target.
 *
 * The store sits above the router, beside the margin's slot (`margin-slot.tsx`),
 * because the margin is mounted beside the router and its cards draw the same references as the
 * page's own column: a context inside the page would not reach them.
 */

/** What a page publishes from its one batched read: the name of each target it read, by
 *  reference, and the references it read as ambiguous (`useKnownAmbiguousReferences`). The store
 *  holds the two as one value, so they always come from one read and change together. */
interface PublishedReferences {
  readonly ambiguous: ReadonlySet<string>;
  readonly names: ReadonlyMap<string, string>;
}

/** No name at all: what a page that published nothing has read. */
const NO_NAMES: ReadonlyMap<string, string> = new Map();

/** No ambiguous reference: what a page that published none has read. */
const NO_AMBIGUOUS: ReadonlySet<string> = new Set();

/** What the store holds while no page publishes: no name and no ambiguous reference. */
const NOTHING_PUBLISHED: PublishedReferences = { ambiguous: NO_AMBIGUOUS, names: NO_NAMES };

const ReferenceNamesContext = createContext<PublishedReferences>(NOTHING_PUBLISHED);

/** Where a page publishes what it read. A page outside the provider publishes to nobody. */
const ReferenceNamesRegistry = createContext<(published: PublishedReferences) => void>(() => {});

export function ReferenceNamesProvider({ children }: { children: ReactNode }): ReactNode {
  const [published, setPublished] = useState(NOTHING_PUBLISHED);
  return (
    <ReferenceNamesRegistry.Provider value={setPublished}>
      <ReferenceNamesContext.Provider value={published}>{children}</ReferenceNamesContext.Provider>
    </ReferenceNamesRegistry.Provider>
  );
}

/**
 * Publishes `names`, and `ambiguous` beside them, for as long as the calling page is mounted,
 * and empties the store again when the reader leaves. Both must be stable across renders
 * (`useMemo`): the store holds them in state, so a fresh map or set every render would register
 * on every commit.
 */
export function usePublishReferenceNames(
  names: ReadonlyMap<string, string> | undefined,
  ambiguous: ReadonlySet<string> = NO_AMBIGUOUS
): void {
  const register = useContext(ReferenceNamesRegistry);
  useEffect(() => {
    register({ ambiguous, names: names ?? NO_NAMES });
    return () => register(NOTHING_PUBLISHED);
  }, [ambiguous, names, register]);
}

/** The name the page read for `reference`, or `undefined` where it read none and the caller
 *  must read its own. */
export function useKnownReferenceName(reference: string | undefined): string | undefined {
  const { names } = useContext(ReferenceNamesContext);
  return reference === undefined ? undefined : names.get(reference);
}

/** Every name the page read, by reference, for a surface that names what its own words cite
 *  from the page's one batched read rather than asking for each (the issue page's rows,
 *  `row-references.ts`). Empty where the page published none. */
export function useKnownReferenceNames(): ReadonlyMap<string, string> {
  return useContext(ReferenceNamesContext).names;
}

/** Every reference the page read as ambiguous, empty where the page published none. Such a
 *  reference writes short an id two or more targets carry, so it names none of them, and a
 *  surface draws it as its author wrote it and links nothing (dispatch://INFRA-1196 decision
 *  62). A reference with no name (`useKnownReferenceName`) and not in this set reads its own: a
 *  gone one, or one the page did not read. */
export function useKnownAmbiguousReferences(): ReadonlySet<string> {
  return useContext(ReferenceNamesContext).ambiguous;
}
