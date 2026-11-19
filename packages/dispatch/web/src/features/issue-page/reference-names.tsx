import { createContext, type ReactNode, useContext, useEffect, useId, useReducer } from "react";

/**
 * The names a page has already read for the references it draws, shared with every surface the
 * shell mounts beside it.
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
 *
 * More than one surface can publish at once: the page's own column and a sheet that draws the
 * outline beside it each read the tree and publish what they read. The store keeps one value per
 * publisher and hands readers the union of every value still published (`merged`), so a surface
 * that unmounts takes only its own value with it, and every link keeps the names the surfaces
 * still mounted read.
 */

/** What a page publishes from its one batched read: the name of each target it read, by
 *  reference, the references it read as ambiguous (`useKnownAmbiguousReferences`), and the
 *  references among the names whose post its author edited (`useKnownEditedReferences`). The
 *  store holds the three as one value per publisher, so a publisher's three always come from one
 *  read and change together. */
interface PublishedReferences {
  readonly ambiguous: ReadonlySet<string>;
  readonly edited: ReadonlySet<string>;
  readonly names: ReadonlyMap<string, string>;
}

/** No name at all: what a page that published nothing has read. */
const NO_NAMES: ReadonlyMap<string, string> = new Map();

/** No ambiguous reference: what a page that published none has read. */
const NO_AMBIGUOUS: ReadonlySet<string> = new Set();

/** No edited reference: what a page that published none has read. */
const NO_EDITED: ReadonlySet<string> = new Set();

/** What a reader sees while no page publishes, and outside the provider: no name, no ambiguous
 *  reference and no edited one. */
const NOTHING_PUBLISHED: PublishedReferences = {
  ambiguous: NO_AMBIGUOUS,
  edited: NO_EDITED,
  names: NO_NAMES,
};

/** One publisher's change, handed to the store: what it publishes now, under its own key
 *  (`useId`), or `undefined` as it leaves. */
interface Publication {
  readonly published: PublishedReferences | undefined;
  readonly publisher: string;
}

/** What the store holds: every value still published, by its publisher's key, in the order the
 *  publishers last published, and the one value readers see, merged from them (`merged`). The
 *  merged value is made anew only when a publisher publishes or leaves, so a reader is handed
 *  the same map and set on every render in between, and re-renders only then. */
interface Store {
  readonly publications: ReadonlyMap<string, PublishedReferences>;
  readonly value: PublishedReferences;
}

/** No publisher at all: what the store holds before any page publishes and after the last one
 *  leaves. */
const NO_PUBLICATIONS: Store = { publications: new Map(), value: NOTHING_PUBLISHED };

const ReferenceNamesContext = createContext<PublishedReferences>(NOTHING_PUBLISHED);

/** Where a page publishes what it read, and withdraws it again. A page outside the provider
 *  publishes to nobody. */
const ReferenceNamesRegistry = createContext<(publication: Publication) => void>(() => {});

/**
 * Every value still published as the one value readers see. The names are merged, and where two
 * publishers name one reference, the name of the one that published last stands: a publisher
 * publishes again each time its read answers anew, so the last to publish holds the newest
 * answer. Whether a named reference is edited is the answer of that same publisher, so a name
 * and its mark always come from one read: a newer answer that names a post by its deleted line
 * is never marked by an older one that read it edited. The ambiguous references are the union
 * of every publisher's. A publisher whose map or set is empty adds nothing to it, so where one
 * publisher alone has names, or ambiguous references, readers get its own map or set as it
 * published it, and no copy is made.
 */
function merged(publications: ReadonlyMap<string, PublishedReferences>): PublishedReferences {
  if (publications.size === 0) return NOTHING_PUBLISHED;
  const published = [...publications.values()];
  const naming = published.filter((value) => value.names.size > 0);
  const ambiguous = published.map((value) => value.ambiguous).filter((set) => set.size > 0);
  return {
    ambiguous:
      ambiguous.length > 1
        ? new Set(ambiguous.flatMap((set) => [...set]))
        : (ambiguous[0] ?? NO_AMBIGUOUS),
    edited: naming.length > 1 ? editedOfLast(naming) : (naming[0]?.edited ?? NO_EDITED),
    names:
      naming.length > 1
        ? new Map(naming.flatMap((value) => [...value.names]))
        : (naming[0]?.names ?? NO_NAMES),
  };
}

/** The edited references of several publishers' names, each as the last publisher to name it
 *  answers it (`merged`). */
function editedOfLast(naming: readonly PublishedReferences[]): ReadonlySet<string> {
  const edited = new Set<string>();
  for (const { edited: marked, names } of naming) {
    for (const reference of names.keys()) {
      if (marked.has(reference)) edited.add(reference);
      else edited.delete(reference);
    }
  }
  return edited;
}

/** The store after one publisher's change. A publisher's value is removed before it is set
 *  again, so one that publishes again moves to the end of the order `merged` reads them by; one
 *  that leaves takes its own value with it and no other. */
function withPublication(store: Store, { published, publisher }: Publication): Store {
  if (published === undefined && !store.publications.has(publisher)) return store;
  const publications = new Map(store.publications);
  publications.delete(publisher);
  if (published !== undefined) publications.set(publisher, published);
  return { publications, value: merged(publications) };
}

export function ReferenceNamesProvider({ children }: { children: ReactNode }): ReactNode {
  const [{ value }, publish] = useReducer(withPublication, NO_PUBLICATIONS);
  return (
    <ReferenceNamesRegistry.Provider value={publish}>
      <ReferenceNamesContext.Provider value={value}>{children}</ReferenceNamesContext.Provider>
    </ReferenceNamesRegistry.Provider>
  );
}

/**
 * Publishes `names`, with `ambiguous` and `edited` beside them, for as long as the calling page
 * is mounted, and withdraws them again when the reader leaves, leaving every other publisher's
 * value as it was. All three must be stable across renders (`useMemo`): the store holds them in
 * state, so a fresh map or set every render would register on every commit.
 */
export function usePublishReferenceNames(
  names: ReadonlyMap<string, string> | undefined,
  ambiguous: ReadonlySet<string> = NO_AMBIGUOUS,
  edited: ReadonlySet<string> = NO_EDITED
): void {
  const register = useContext(ReferenceNamesRegistry);
  // The key this publisher's value is held under, the same for as long as it is mounted, so its
  // cleanup withdraws its own value and no other.
  const publisher = useId();
  useEffect(() => {
    register({ published: { ambiguous, edited, names: names ?? NO_NAMES }, publisher });
    return () => register({ published: undefined, publisher });
  }, [ambiguous, edited, names, publisher, register]);
}

/** The name the page read for `reference`, the words alone, or `undefined` where it read none
 *  and the caller must read its own. A reference in `useKnownEditedReferences` is drawn with
 *  ` · edited` after what the caller's cut leaves of it (`editedName`). */
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
 *  surface draws it as its author wrote it and links nothing. A reference with no name
 *  (`useKnownReferenceName`) and not in this set reads its own: a gone one, or one the page did
 *  not read. */
export function useKnownAmbiguousReferences(): ReadonlySet<string> {
  return useContext(ReferenceNamesContext).ambiguous;
}

/** Every reference among the page's names whose post its author edited, a comment, a message or
 *  a block of either, empty where the page published none. A surface that draws such a
 *  reference's name cuts the name to its own length and then marks it ` · edited`
 *  (`editedName`), so no cut takes the mark (`row-references.ts`'s `rowReferenceWords`). */
export function useKnownEditedReferences(): ReadonlySet<string> {
  return useContext(ReferenceNamesContext).edited;
}
