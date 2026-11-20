import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useId,
  useLayoutEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
} from "react";

import type { DisplayTree } from "../../api/types";
import { type ReferenceNaming, useMarkdownHeadline } from "./markdown-engine";

/**
 * The names a page has already read for the references it draws, shared with every surface the
 * shell mounts beside it.
 *
 * A link to a block shows the block's own words, and reading them one link at a time is one
 * `GET /api/v1/references` per pointer - the read `GET /api/v1/reference-targets` makes once for
 * them all (its entry in `packages/envoy/internal/dispatch/api/routes_table.go`). A page that has
 * made that batched read publishes what it read here, and every surface under the shell names a
 * reference from it instead of asking for it again: the plain-text projection before it mounts a
 * read of its own (`plain-text.tsx`), and the new issue page's rows. A page that publishes
 * nothing leaves every surface exactly as it was, reading its own targets.
 *
 * A page publishes how each target is named - the reading its name is projected from - and the
 * store projects each one once, by the one reader every surface reads a stored text with
 * (`useMarkdownHeadline`), so no page writes a target's words by a reader of its own. A name is a
 * title drawn over one record: the references inside it read as their short forms, as a link's
 * title reads them (`ReferenceNaming.readsNone`), since a name inside a name resolves nothing.
 * The store holds each name whole, as a link to the target reads it (R8): a surface that draws a
 * shorter one - a chip - cuts it where it draws it.
 *
 * The store sits above the router, beside the margin's slot (`issue-page/margin-slot.tsx`),
 * because the margin is mounted beside the router and its cards draw the same references as the
 * page's own column: a context inside the page would not reach them. A page publishes in the
 * layout phase of the commit that draws what it read, so the page hands its own subtree the same
 * value in the render that draws it as well (`PublishedReferenceNames`).
 *
 * More than one surface can publish at once: the page's own column and a sheet that draws the
 * outline beside it each read the tree and publish what they read. The store keeps one value per
 * publisher and hands readers the union of every value still published (`merged`), so a surface
 * that unmounts takes only its own value with it, and every surface keeps the names the surfaces
 * still mounted read. What readers are handed changes only where what it holds does, so a
 * publisher that publishes the same references again draws no surface again.
 */

/** How a page names one target it read, which the store projects into the name readers see: the
 *  reading of the name's words - a block's own, or a title's as one text run, which nothing
 *  parses - with the page's own word for the target, which names it where those words project
 *  to none (`fallback`); or words that are no writer's text - the page's own word for a target -
 *  drawn as they stand. */
export type ReferenceNameReading =
  | { readonly tree: DisplayTree; readonly fallback?: string }
  | { readonly words: string };

/** What a page publishes from its one batched read, as one value per publisher, so its parts
 *  always come from one read and change together. */
export interface PublishedReferences {
  /** The references the read answered ambiguous: each writes short an id two or more targets
   *  carry, so it names none of them, and a surface draws it as its author wrote it. */
  readonly ambiguous: ReadonlySet<string>;
  /** Every reference the page asks the read for, answered or not yet: a surface waits on the
   *  page's answer for one of these rather than reading it itself. */
  readonly asked: ReadonlySet<string>;
  /** The references the read answered that nothing answers to now, or that the reader may not
   *  see: a surface draws each as its author wrote it and reads it no more (R8). */
  readonly gone: ReadonlySet<string>;
  /** How the read names each target that has a name, by its reference. */
  readonly readings: ReadonlyMap<string, ReferenceNameReading>;
  /** The references the read answered with no words to name them by - another issue's block it
   *  sent no reading of - which is no answer that the target has no name: a surface reads each
   *  one itself, as a link to it does (`RefLink`, R8). */
  readonly unnamed: ReadonlySet<string>;
}

/** What readers see: the published references, and the name each reading projected to. A
 *  reference whose reading has not projected yet - the reader's engine has not answered - has no
 *  name yet, and one whose reading projected to no words is among `unnamed`. */
export interface KnownReferences {
  readonly ambiguous: ReadonlySet<string>;
  readonly asked: ReadonlySet<string>;
  readonly gone: ReadonlySet<string>;
  readonly names: ReadonlyMap<string, string>;
  readonly unnamed: ReadonlySet<string>;
}

/** No reference at all: what a page that published nothing has read. */
const NO_REFERENCES: ReadonlySet<string> = new Set();
const NO_READINGS: ReadonlyMap<string, ReferenceNameReading> = new Map();
const NO_NAMES: ReadonlyMap<string, string> = new Map();

/** What the store holds while no page publishes. */
const NOTHING_PUBLISHED: PublishedReferences = {
  ambiguous: NO_REFERENCES,
  asked: NO_REFERENCES,
  gone: NO_REFERENCES,
  readings: NO_READINGS,
  unnamed: NO_REFERENCES,
};

/** What a reader sees while no page publishes, and outside the provider. */
const NOTHING_KNOWN: KnownReferences = {
  ambiguous: NO_REFERENCES,
  asked: NO_REFERENCES,
  gone: NO_REFERENCES,
  names: NO_NAMES,
  unnamed: NO_REFERENCES,
};

/** One publisher's change, handed to the store: what it publishes now, under its own key
 *  (`useId`), or `undefined` as it leaves. */
interface Publication {
  readonly published: PublishedReferences | undefined;
  readonly publisher: string;
}

/** What the store holds: every value still published, by its publisher's key, in the order the
 *  publishers last published, and the one value merged from them (`merged`). */
interface Store {
  readonly publications: ReadonlyMap<string, PublishedReferences>;
  readonly value: PublishedReferences;
}

/** No publisher at all: what the store holds before any page publishes and after the last one
 *  leaves. */
const NO_PUBLICATIONS: Store = { publications: new Map(), value: NOTHING_PUBLISHED };

const ReferenceNamesContext = createContext<KnownReferences>(NOTHING_KNOWN);

/** Where a page publishes what it read, and withdraws it again. A page outside the provider
 *  publishes to nobody. */
const ReferenceNamesRegistry = createContext<(publication: Publication) => void>(() => {});

/** `held` where it already holds every reference of `added`, so no copy is made, and the union
 *  of the two otherwise. */
function including(held: ReadonlySet<string>, added: ReadonlySet<string>): ReadonlySet<string> {
  if (held === added || added.size === 0) return held;
  if (held.size === 0) return added;
  return [...added].every((each) => held.has(each)) ? held : new Set([...held, ...added]);
}

/** The union of one part of every value still published, `kept` where it holds the same
 *  references, so a reader is handed the set it already holds: a publisher's own set where it
 *  alone holds any, so no copy is made, and a union of them all where two do. */
function union(parts: readonly ReadonlySet<string>[], kept: ReadonlySet<string>) {
  const held = parts.filter((part) => part.size > 0);
  const joined =
    held.length > 1 ? new Set(held.flatMap((part) => [...part])) : (held[0] ?? NO_REFERENCES);
  const same =
    joined === kept ||
    (joined.size === kept.size && [...joined].every((reference) => kept.has(reference)));
  return same ? kept : joined;
}

/** The readings of every value still published, `kept` where it holds the same readings. Where
 *  two publishers name one reference, the one that published last stands: a publisher publishes
 *  again each time its read answers anew, so the last to publish holds the newest answer. */
function readingsOf(
  published: readonly PublishedReferences[],
  kept: ReadonlyMap<string, ReferenceNameReading>
): ReadonlyMap<string, ReferenceNameReading> {
  const held = published.map((value) => value.readings).filter((map) => map.size > 0);
  const joined =
    held.length > 1 ? new Map(held.flatMap((map) => [...map])) : (held[0] ?? NO_READINGS);
  const same =
    joined === kept ||
    (joined.size === kept.size &&
      [...joined].every(([reference, reading]) => kept.get(reference) === reading));
  return same ? kept : joined;
}

/** Every value still published as the one value readers see, each part the one `previous`
 *  holds where it holds the same, and `previous` itself where every part does. */
function merged(
  publications: ReadonlyMap<string, PublishedReferences>,
  previous: PublishedReferences
): PublishedReferences {
  if (publications.size === 0) return NOTHING_PUBLISHED;
  const published = [...publications.values()];
  const next: PublishedReferences = {
    ambiguous: union(
      published.map((value) => value.ambiguous),
      previous.ambiguous
    ),
    asked: union(
      published.map((value) => value.asked),
      previous.asked
    ),
    gone: union(
      published.map((value) => value.gone),
      previous.gone
    ),
    readings: readingsOf(published, previous.readings),
    unnamed: union(
      published.map((value) => value.unnamed),
      previous.unnamed
    ),
  };
  const kept =
    next.ambiguous === previous.ambiguous &&
    next.asked === previous.asked &&
    next.gone === previous.gone &&
    next.readings === previous.readings &&
    next.unnamed === previous.unnamed;
  return kept ? previous : next;
}

/** The store after one publisher's change. A publisher's value is removed before it is set
 *  again, so one that publishes again moves to the end of the order `merged` reads them by; one
 *  that leaves takes its own value with it and no other. */
function withPublication(store: Store, { published, publisher }: Publication): Store {
  if (published === undefined && !store.publications.has(publisher)) return store;
  const publications = new Map(store.publications);
  publications.delete(publisher);
  if (published !== undefined) publications.set(publisher, published);
  return { publications, value: merged(publications, store.value) };
}

/** How a name is read: a title drawn over one record, inside which a reference reads as its
 *  short form and nothing is read (`ReferenceNaming.readsNone`). */
const NAME_NAMING: ReferenceNaming = { names: new Map(), readsNone: true };

/** How one projection tells the store what its reading projected to: its name, `""` for one that
 *  projected to no words, and `undefined` as it leaves. */
type NameReport = (reference: string, name: string | undefined) => void;

/** One target's name, projected by the shared reader from the reading the page published for
 *  it, whole; it draws nothing. */
function NameProjection({
  onName,
  reading,
  reference,
}: {
  onName: NameReport;
  reading: ReferenceNameReading;
  reference: string;
}): ReactNode {
  const headline = useMarkdownHeadline(
    "tree" in reading ? reading.tree : undefined,
    Number.POSITIVE_INFINITY,
    undefined,
    NAME_NAMING
  );
  const name =
    "words" in reading
      ? reading.words
      : headline === undefined
        ? undefined
        : headline.text || (reading.fallback ?? "");
  // In the layout phase, so a name lands in the commit its projection answered in, before the
  // page is painted with the reference still unnamed.
  useLayoutEffect(() => {
    onName(reference, name);
  }, [name, onName, reference]);
  useLayoutEffect(() => () => onName(reference, undefined), [onName, reference]);
  return null;
}

/** The names the projections reported, and the references whose reading projected to no words. */
interface Projected {
  readonly blank: ReadonlySet<string>;
  readonly names: ReadonlyMap<string, string>;
}

export function ReferenceNamesProvider({ children }: { children: ReactNode }): ReactNode {
  const [{ value }, publish] = useReducer(withPublication, NO_PUBLICATIONS);
  // Every projected name, kept in place as each projection reports, and handed to readers as
  // one map per change (`version`): a page's projections report in one commit, and a map copied
  // for each report would be copied once per target.
  const reported = useRef(new Map<string, string>());
  const [version, setVersion] = useState(0);
  const onName = useCallback<NameReport>((reference, name) => {
    const names = reported.current;
    if (names.get(reference) === name) return;
    if (name === undefined) names.delete(reference);
    else names.set(reference, name);
    setVersion((count) => count + 1);
  }, []);
  // biome-ignore lint/correctness/useExhaustiveDependencies: `version` counts each change of the map the ref holds.
  const projected = useMemo<Projected>(() => {
    const names = new Map<string, string>();
    const blank = new Set<string>();
    for (const [reference, name] of reported.current) {
      if (name === "") blank.add(reference);
      else names.set(reference, name);
    }
    return {
      blank: blank.size === 0 ? NO_REFERENCES : blank,
      names: names.size === 0 ? NO_NAMES : names,
    };
  }, [version]);
  const { ambiguous, asked, gone, unnamed } = value;
  const known = useMemo<KnownReferences>(
    () => ({
      ambiguous,
      asked,
      gone,
      names: projected.names,
      unnamed: including(unnamed, projected.blank),
    }),
    [ambiguous, asked, gone, projected, unnamed]
  );
  return (
    <ReferenceNamesRegistry.Provider value={publish}>
      <ReferenceNamesContext.Provider value={known}>
        {children}
        {[...value.readings].map(([reference, reading]) => (
          <NameProjection key={reference} onName={onName} reading={reading} reference={reference} />
        ))}
      </ReferenceNamesContext.Provider>
    </ReferenceNamesRegistry.Provider>
  );
}

/**
 * Publishes `published` for as long as the calling page is mounted, and withdraws it again when
 * the page leaves, leaving every other publisher's value as it was. It must be stable across
 * renders (`useMemo`): the store holds it in state, so a fresh value every render would register
 * on every commit. `undefined` publishes nothing. It publishes in the layout phase of the commit
 * that draws what the page read, so a surface beside the page drawn in that commit is named in
 * the commit after, before the page is painted; the page's own subtree reads the value in the
 * commit that draws it (`PublishedReferenceNames`).
 */
export function usePublishReferenceNames(published: PublishedReferences | undefined): void {
  const register = useContext(ReferenceNamesRegistry);
  // The key this publisher's value is held under, the same for as long as it is mounted, so its
  // cleanup withdraws its own value and no other.
  const publisher = useId();
  // A new value takes the place of the one before it, and the store keeps every part of it that
  // holds what it held (`merged`); a value withdrawn first would leave the store empty between
  // the two, and every part of the next would be new to it.
  useLayoutEffect(() => {
    register({ published, publisher });
  }, [published, publisher, register]);
  useLayoutEffect(() => () => register({ published: undefined, publisher }), [publisher, register]);
}

/**
 * Hands the subtree of a page that publishes `published` (`usePublishReferenceNames`) the page's
 * own value in the very render that draws it, over what the store holds: the store takes the
 * value in the layout phase of that commit, and a surface the subtree draws in it would read a
 * reference the page is asking for before the page's answer reached it. So a row drawn with the
 * page's read waits on that read for every reference the page asks it for, from its first
 * render, and mounts no read of its own for one (`plain-text.tsx`). A name is the store's alone,
 * since only its projection names one. What the subtree reads changes only where a part of it
 * does, so a page that publishes the same references again draws no row again.
 */
export function PublishedReferenceNames({
  children,
  published = NOTHING_PUBLISHED,
}: {
  children: ReactNode;
  published: PublishedReferences | undefined;
}): ReactNode {
  const known = useContext(ReferenceNamesContext);
  const { ambiguous, asked, gone, unnamed } = published;
  const own = useMemo<KnownReferences>(() => {
    const value = {
      ambiguous: including(known.ambiguous, ambiguous),
      asked: including(known.asked, asked),
      gone: including(known.gone, gone),
      names: known.names,
      unnamed: including(known.unnamed, unnamed),
    };
    const kept =
      value.ambiguous === known.ambiguous &&
      value.asked === known.asked &&
      value.gone === known.gone &&
      value.unnamed === known.unnamed;
    return kept ? known : value;
  }, [ambiguous, asked, gone, known, unnamed]);
  return <ReferenceNamesContext.Provider value={own}>{children}</ReferenceNamesContext.Provider>;
}

/**
 * `references` as the set it was the last time it held other references: the same set while what
 * it holds stays the same, for a page that reads its references anew on every read, so a value
 * it publishes from them changes only where they do. Read by their order, which a page reads
 * them in the same way each time.
 */
export function useSameReferences(references: ReadonlySet<string>): ReadonlySet<string> {
  const key = [...references].join("\n");
  // biome-ignore lint/correctness/useExhaustiveDependencies: the set is read anew only where `key`, what it holds, changes.
  return useMemo(() => references, [key]);
}

/** Everything the page published, with the names its readings projected to: what the
 *  plain-text projection reads before it mounts a read of its own (`plain-text.tsx`). */
export function useKnownReferences(): KnownReferences {
  return useContext(ReferenceNamesContext);
}

/** Every name the page read, by reference. Empty where the page published none. */
export function useKnownReferenceNames(): ReadonlyMap<string, string> {
  return useContext(ReferenceNamesContext).names;
}

/** Every reference the page read as ambiguous, empty where the page published none. Such a
 *  reference writes short an id two or more targets carry, so it names none of them, and a
 *  surface draws it as its author wrote it and links nothing. */
export function useKnownAmbiguousReferences(): ReadonlySet<string> {
  return useContext(ReferenceNamesContext).ambiguous;
}
