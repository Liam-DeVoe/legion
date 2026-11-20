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
import { projectNow, type ReferenceNaming, useMarkdownHeadline } from "./markdown-engine";

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
 *
 * The store sits above the router, beside the margin's slot (`issue-page/margin-slot.tsx`),
 * because the margin is mounted beside the router and its cards draw the same references as the
 * page's own column: a context inside the page would not reach them.
 *
 * More than one surface can publish at once: the page's own column and a sheet that draws the
 * outline beside it each read the tree and publish what they read. The store keeps one value per
 * publisher and hands readers the union of every value still published (`merged`), so a surface
 * that unmounts takes only its own value with it, and every surface keeps the names the surfaces
 * still mounted read.
 */

/** How a page names one target it read, which the store projects into the name readers see. */
export interface ReferenceNameReading {
  /** The reading of the name's words: a block's own, or a title's as one text run, which nothing
   *  parses. */
  readonly tree?: DisplayTree;
  /** The name as the read stored it, where it sent no reading of it: parsed by the reader. */
  readonly markdown?: string;
  /** Words that are no writer's text - the page's own word for a target - drawn as they stand. */
  readonly words?: string;
  /** How the page cuts the words its name projects to, where it names a target shorter than its
   *  whole words; the words whole where it is left out. A name it cuts to `""` is no name. */
  readonly cut?: (words: string) => string;
}

/** What a page publishes from its one batched read, as one value per publisher, so its parts
 *  always come from one read and change together. */
export interface PublishedReferences {
  /** The references the read answered ambiguous: each writes short an id two or more targets
   *  carry, so it names none of them, and a surface draws it as its author wrote it. */
  readonly ambiguous: ReadonlySet<string>;
  /** Every reference the page asks the read for, answered or not yet: a surface waits on the
   *  page's answer for one of these rather than reading it itself. */
  readonly asked: ReadonlySet<string>;
  /** The references the read answered that nothing answers to now. */
  readonly gone: ReadonlySet<string>;
  /** How the read names each target that has a name, by its reference. */
  readonly readings: ReadonlyMap<string, ReferenceNameReading>;
}

/** What readers see: the published references, and the name each reading projected to. A
 *  reference whose reading has not projected yet - the reader's engine has not answered - has no
 *  name yet. */
export interface KnownReferences {
  readonly ambiguous: ReadonlySet<string>;
  readonly asked: ReadonlySet<string>;
  readonly gone: ReadonlySet<string>;
  readonly names: ReadonlyMap<string, string>;
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
};

/** What a reader sees while no page publishes, and outside the provider. */
const NOTHING_KNOWN: KnownReferences = {
  ambiguous: NO_REFERENCES,
  asked: NO_REFERENCES,
  gone: NO_REFERENCES,
  names: NO_NAMES,
};

/** One publisher's change, handed to the store: what it publishes now, under its own key
 *  (`useId`), or `undefined` as it leaves. */
interface Publication {
  readonly published: PublishedReferences | undefined;
  readonly publisher: string;
}

/** What the store holds: every value still published, by its publisher's key, in the order the
 *  publishers last published, and the one value merged from them (`merged`). The merged value is
 *  made anew only when a publisher publishes or leaves. */
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

/** The union of one part of every value still published: a publisher's own set or map where it
 *  alone holds any, so no copy is made, and a union of them all where two do. In a map, where two
 *  publishers name one reference, the one that published last stands: a publisher publishes again
 *  each time its read answers anew, so the last to publish holds the newest answer. */
function union<Value>(parts: readonly ReadonlySet<Value>[], none: ReadonlySet<Value>) {
  const held = parts.filter((part) => part.size > 0);
  return held.length > 1 ? new Set(held.flatMap((part) => [...part])) : (held[0] ?? none);
}

function merged(publications: ReadonlyMap<string, PublishedReferences>): PublishedReferences {
  if (publications.size === 0) return NOTHING_PUBLISHED;
  const published = [...publications.values()];
  const readings = published.map((value) => value.readings).filter((map) => map.size > 0);
  return {
    ambiguous: union(
      published.map((value) => value.ambiguous),
      NO_REFERENCES
    ),
    asked: union(
      published.map((value) => value.asked),
      NO_REFERENCES
    ),
    gone: union(
      published.map((value) => value.gone),
      NO_REFERENCES
    ),
    readings:
      readings.length > 1
        ? new Map(readings.flatMap((map) => [...map]))
        : (readings[0] ?? NO_READINGS),
  };
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

/** How a name is read: a title drawn over one record, inside which a reference reads as its
 *  short form and nothing is read (`ReferenceNaming.readsNone`). */
const NAME_NAMING: ReferenceNaming = { names: new Map(), readsNone: true };

/** The name `reading` projects to, cut as it asks, made in this task with the engine a load has
 *  already given (`projectNow`): the name the store holds for the reading once its projection
 *  answers, for a caller that must answer now. `undefined` until the engine has loaded, and for
 *  a reading that names nothing (`""`). */
export function projectedName(reading: ReferenceNameReading): string | undefined {
  const { cut, markdown, tree, words } = reading;
  const projected =
    words ?? projectNow(tree, Number.POSITIVE_INFINITY, markdown, NAME_NAMING)?.text;
  const name = projected === undefined ? undefined : (cut?.(projected) ?? projected);
  return name === "" ? undefined : name;
}

/** How one projection tells the store what its reading projected to. */
type NameReport = (reference: string, name: string | undefined) => void;

/** One target's name, projected by the shared reader from the reading the page published for
 *  it; it draws nothing. */
function NameProjection({
  onName,
  reading,
  reference,
}: {
  onName: NameReport;
  reading: ReferenceNameReading;
  reference: string;
}): ReactNode {
  const { cut, markdown, tree, words } = reading;
  const headline = useMarkdownHeadline(
    words === undefined ? tree : undefined,
    Number.POSITIVE_INFINITY,
    words === undefined ? markdown : undefined,
    NAME_NAMING
  );
  const projected = words ?? headline?.text;
  // Cut once for each projection, never again on a draw that changes nothing of it: the store
  // draws every projection again whenever one of them reports (decision 102).
  const name = useMemo(
    () => (projected === undefined ? undefined : (cut?.(projected) ?? projected)),
    [cut, projected]
  );
  // In the layout phase, so a name lands in the commit its projection answered in, before the
  // page is painted with the reference still unnamed.
  useLayoutEffect(() => {
    onName(reference, name === "" ? undefined : name);
  }, [name, onName, reference]);
  useLayoutEffect(() => () => onName(reference, undefined), [onName, reference]);
  return null;
}

export function ReferenceNamesProvider({ children }: { children: ReactNode }): ReactNode {
  const [{ value }, publish] = useReducer(withPublication, NO_PUBLICATIONS);
  // Every projected name, kept in place as each projection reports, and handed to readers as
  // one map per change (`version`): a page's projections report in one commit, and a map copied
  // for each report would be copied once per target.
  const projected = useRef(new Map<string, string>());
  const [version, setVersion] = useState(0);
  const onName = useCallback<NameReport>((reference, name) => {
    const names = projected.current;
    if (names.get(reference) === name) return;
    if (name === undefined) names.delete(reference);
    else names.set(reference, name);
    setVersion((count) => count + 1);
  }, []);
  // biome-ignore lint/correctness/useExhaustiveDependencies: `version` counts each change of the map the ref holds.
  const names = useMemo(
    () => (projected.current.size === 0 ? NO_NAMES : new Map(projected.current)),
    [version]
  );
  const known = useMemo<KnownReferences>(
    () =>
      value === NOTHING_PUBLISHED
        ? NOTHING_KNOWN
        : { ambiguous: value.ambiguous, asked: value.asked, gone: value.gone, names },
    [names, value]
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
 * that draws what the page read, so a surface drawn in that commit is named in the commit after,
 * before the page is painted, and a read of its own mounts only after that (`plain-text.tsx`'s
 * `ReferenceNameRead`), so it never asks for a reference the page is already asking for.
 */
export function usePublishReferenceNames(published: PublishedReferences | undefined): void {
  const register = useContext(ReferenceNamesRegistry);
  // The key this publisher's value is held under, the same for as long as it is mounted, so its
  // cleanup withdraws its own value and no other.
  const publisher = useId();
  useLayoutEffect(() => {
    register({ published, publisher });
    return () => register({ published: undefined, publisher });
  }, [published, publisher, register]);
}

/** Everything the page published, with the names its readings projected to: what the
 *  plain-text projection reads before it mounts a read of its own (`plain-text.tsx`). */
export function useKnownReferences(): KnownReferences {
  return useContext(ReferenceNamesContext);
}

/** The name the page read for `reference`, or `undefined` where it read none and the caller
 *  must read its own. */
export function useKnownReferenceName(reference: string | undefined): string | undefined {
  const { names } = useContext(ReferenceNamesContext);
  return reference === undefined ? undefined : names.get(reference);
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
