import {
  createContext,
  createElement,
  type ReactNode,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
} from "react";

import type { DocumentToolbar } from "../doc/ProofDocument";

export interface DocumentToolbars {
  /** Which documents are drawn against their previous version. */
  readonly diffs: ReadonlySet<string>;
  /** Turns a document's diff on or off. */
  readonly showDiff: (artifactId: string, next: boolean) => void;
  /** The one callback a document reports its toolbar through, by artifact id. */
  readonly toolbarSink: (artifactId: string) => (next: DocumentToolbar | undefined) => void;
  /** The toolbar each open document published. */
  readonly toolbars: ReadonlyMap<string, DocumentToolbar>;
}

/** Outside a `DocumentToolbarsProvider` a document's toolbar is held nowhere and no diff is on,
 *  which is what a unit test that mounts the body alone gets. */
const NO_TOOLBARS: DocumentToolbars = {
  diffs: new Set(),
  showDiff: () => {},
  toolbarSink: () => () => {},
  toolbars: new Map(),
};

const DocumentToolbarsContext = createContext<DocumentToolbars>(NO_TOOLBARS);

/**
 * Holds one toolbar and one diff per document, by its artifact id, for one issue page
 * (dispatch://INFRA-1196 R16): every editor on the page reports its own toolbar through a
 * callback of its own, and the page's version control draws the toolbar of whichever document is
 * open. It stands in `slots/providers.tsx`, above the body, whose row editor reports here, and
 * the view row, whose version control reads it.
 */
export function DocumentToolbarsProvider({ children }: { children: ReactNode }): ReactNode {
  return createElement(DocumentToolbarsContext.Provider, { value: useHeldToolbars() }, children);
}

/** The page's toolbars and diffs, held by the nearest `DocumentToolbarsProvider`. */
export function useDocumentToolbars(): DocumentToolbars {
  return useContext(DocumentToolbarsContext);
}

/**
 * The toolbars and diffs the provider holds.
 *
 * Each callback is held across renders, because an editor publishes its toolbar from an effect:
 * a fresh function every render would have it publish again on every commit.
 */
function useHeldToolbars(): DocumentToolbars {
  const [toolbars, setToolbars] = useState<ReadonlyMap<string, DocumentToolbar>>(() => new Map());
  const [diffs, setDiffs] = useState<ReadonlySet<string>>(() => new Set());
  const sinks = useRef(new Map<string, (next: DocumentToolbar | undefined) => void>());
  const showDiff = useCallback(
    (artifactId: string, next: boolean) =>
      setDiffs((held) => {
        const made = new Set(held);
        if (next) made.add(artifactId);
        else made.delete(artifactId);
        return made;
      }),
    []
  );
  const toolbarSink = useCallback(
    (artifactId: string) => {
      const held = sinks.current.get(artifactId);
      if (held !== undefined) return held;
      const made = (next: DocumentToolbar | undefined) => {
        setToolbars((held) => {
          const replaced = new Map(held);
          if (next === undefined) replaced.delete(artifactId);
          else replaced.set(artifactId, next);
          return replaced;
        });
        // A document that has closed shows no diff: the control that turned one on has gone
        // with it.
        if (next === undefined) showDiff(artifactId, false);
      };
      sinks.current.set(artifactId, made);
      return made;
    },
    [showDiff]
  );
  return useMemo(
    () => ({ diffs, showDiff, toolbarSink, toolbars }),
    [diffs, showDiff, toolbarSink, toolbars]
  );
}
