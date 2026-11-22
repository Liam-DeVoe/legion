import {
  type ComponentType,
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { flushSync } from "react-dom";

import { inputClasses } from "../../theme/classes";
import { ChunkBoundary } from "./ChunkBoundary";
import type { Chunk } from "./chunk-component";
import { importWhenOnline, renderChunk } from "./chunk-loads";
import { DIALOG_SCOPE, useKeymapScope } from "./keymap";
import { useCloseOnNavigation } from "./useDialog";

/** What a surface drawn through `askedSurface` takes, beside its own props. */
export interface AskedSurfaceProps {
  /** Closes the surface: what its host does when the reader closes it, and what this does when
   *  the reader cancels the open before the surface's chunk is in, or the chunk cannot load. */
  readonly onClose: () => void;
  /** Whether the reader has the surface open. A host that draws its surface only while it is
   *  open - the Create issue dialog, the shortcut sheet - leaves it unset, and the surface is
   *  open while it is drawn. A host that keeps its surface mounted while closed - the search bar,
   *  which runs a chosen row once it has closed, and the Inbox peek, which keeps its filters for
   *  the next open - passes it. */
  readonly open?: boolean;
  /** What the reader typed into the placeholder while the surface's chunk was on its way, which
   *  the surface puts in its own text box as it mounts, so those keys arrive late rather than
   *  being lost. Empty when the chunk was in at the press. */
  readonly typed?: string;
}

/**
 * A surface the reader opens with a key or a press - the Create issue dialog
 * (`features/issue/CreateIssueDialogHost.tsx`), the shortcut sheet (`ShortcutHelpHost.tsx`),
 * the search bar (`features/search/SearchPaletteHost.tsx`) and the Inbox peek
 * (`features/inbox/InboxHost.tsx`) - whose code is a chunk of its own.
 *
 * WHY NOT `lazy` AND `Suspense`, OR `chunkComponent`. Each of these opens on a key, and the keys
 * a reader types straight after it are meant for the surface. Until a surface has mounted, no
 * `dialog` scope is on the keymap's stack (`useDialog.ts`), so the page's own shortcuts are live
 * and take those keys: on an issue page a digit writes the issue's priority and `e` puts what
 * follows into its title (`features/issue/IssuePage.tsx`). Behind `lazy` that window is at
 * least 300 ms on every first open, also with the chunk in (`chunk-component.tsx` says why), and
 * a chunk that is not in adds its download. Here a chunk that is in draws its surface in the
 * commit of the press, as the entry's own dialogs do; `chunkComponent` would do that too, but it
 * asks for its chunk as it mounts and draws a fixed fallback, and these need what follows.
 *
 * WHILE THE CHUNK IS NOT IN, A PLACEHOLDER HOLDS THE KEYS. The press draws a small box in the
 * entry's code, which takes the focus in the commit of the press and pushes the `dialog` scope,
 * so no key reaches the page's shortcuts or a field of the page. What the reader types into it
 * is handed to the surface as `typed` when the chunk lands, and the surface then takes the box's
 * place and the focus. The reader can call the open off: Escape in the box, a press or a Tab that
 * takes the focus out of it, or a move to another address each close it, and a chunk that lands
 * after that opens nothing and takes no focus - a dialog must not open on its own against a page
 * the reader has left or a field they went on to, which a download held by an outage would
 * otherwise do minutes later (`importWhenOnline`). A window that loses the focus to another
 * application cancels nothing: the box gets the focus back with the window.
 *
 * WHEN THE CHUNK IS ASKED FOR. On every open while it is not in, under `renderChunk` and
 * `importWhenOnline`: the reader is waiting on it, so a download that fails online spends one of
 * the tab's recovery reloads (`DeploymentResilience.tsx`), and one that fails offline is asked
 * for again once the network is back. A download that fails online closes the open, so the next
 * press asks again (`keptUntilRejected` keeps no failure) rather than finding the surface open
 * and drawing nothing for good. A host that wants its chunk in before the press, as most of these
 * do, starts it itself with `prefetchChunk`.
 *
 * Each surface draws under a `ChunkBoundary`, since every one of these hosts sits outside the
 * page's error boundary (`app.tsx`): a render that throws under it - a chunk the surface draws
 * from failing online, the decision card of a body in the peek among them
 * (`features/doc/TextAskCardHost.tsx`) - closes the surface and leaves the page drawn and
 * signed in, rather than reaching the app's top boundary (`main.tsx`).
 */
export function askedSurface<P extends AskedSurfaceProps>(source: Chunk<P>): ComponentType<P> {
  function AskedSurface(props: P): ReactNode {
    const { onClose } = props;
    const open = props.open !== false;
    const Component = source.loaded();
    // Set once the chunk lands, only to draw again: `source.loaded()` is what this reads.
    const [, landed] = useState<object>();
    // What the placeholder holds for the surface. A closed surface has nothing typed, so the next
    // open starts empty.
    const held = useRef<Held>({ opener: null, text: "" }).current;
    if (!open) held.text = "";
    // biome-ignore lint/correctness/useExhaustiveDependencies: `onClose` is a new function at every render of the host and closes the same surface; with it here every render would start another wait on the one download.
    useEffect(() => {
      if (!open || Component !== undefined) {
        return;
      }
      let live = true;
      void renderChunk(() => importWhenOnline(source.load)).then(
        () => {
          if (!live) return;
          // The page as the press found it, and then the surface, in one step: the focus goes
          // back to the opener, and the surface draws and commits before the browser handles
          // another event, so no key lands in the opener between the two. The surface's first
          // render then reads the page the reader opened it over - the bar takes its rows from
          // what holds the focus (`appKeymap.actions()` in `features/search/SearchPalette.tsx`),
          // and with the box focused it would withdraw every row bound to the card or the page
          // the reader was on - and its `useDialog` records the opener as what the focus goes
          // back to when it closes. The box's blur on the way cancels nothing: its opener is
          // taken first.
          const { opener } = held;
          held.opener = null;
          if (opener instanceof HTMLElement) opener.focus();
          flushSync(() => landed({}));
        },
        () => {
          // The recovery has the tab's reload; if none follows, the open is over.
          if (live) onClose();
        }
      );
      return () => {
        live = false;
      };
    }, [open, Component]);
    return (
      <ChunkBoundary onError={onClose}>
        {Component !== undefined ? (
          <Component {...props} typed={held.text} />
        ) : open ? (
          <Placeholder held={held} onCancel={onClose} />
        ) : null}
      </ChunkBoundary>
    );
  }
  return AskedSurface;
}

/** What the placeholder holds for the surface that takes its place: what held the focus at the
 *  press, until the surface lands, and what the reader typed into the box. */
interface Held {
  opener: Element | null;
  text: string;
}

/**
 * The box that holds the keys while an asked-for surface's chunk is on its way: the focus from
 * the commit of the press, and the `dialog` scope, which masks every shortcut beneath it
 * (`keymap.ts`). Its Escape is its own and goes no further, so a dialog the box sits over is not
 * closed with it.
 *
 * Called off while it holds the focus - an Escape, a move to another address - it gives the focus
 * back to where the press found it as it goes, as a closing dialog does (`useDialog`). That runs
 * in the commit that removes the box, and React fires no `onBlur` for a focus moved in a commit,
 * so that move cancels nothing more. A box the focus has already left gives nothing back: the
 * reader is where they went.
 */
function Placeholder({ held, onCancel }: { held: Held; onCancel: () => void }): ReactNode {
  const box = useRef<HTMLInputElement>(null);
  useKeymapScope(DIALOG_SCOPE);
  useCloseOnNavigation(true, onCancel);
  useLayoutEffect(() => {
    const input = box.current;
    held.opener = document.activeElement;
    input?.focus();
    return () => {
      const { opener } = held;
      if (document.activeElement === input && opener instanceof HTMLElement) opener.focus();
    };
  }, [held]);
  return (
    <input
      aria-label="Loading"
      className={`fixed top-20 left-1/2 z-50 w-72 max-w-[calc(100vw-2rem)] -translate-x-1/2 rounded-lg border px-3 py-2 text-sm shadow-2xl ${inputClasses(false)}`}
      onBlur={() => {
        if (held.opener !== null && document.hasFocus()) onCancel();
      }}
      onChange={(event) => {
        held.text = event.target.value;
      }}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.stopPropagation();
          onCancel();
        }
      }}
      placeholder="Loading…"
      ref={box}
    />
  );
}
