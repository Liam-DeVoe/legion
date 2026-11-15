import { type ComponentType, type LazyExoticComponent, lazy } from "react";

// Chunk downloads nobody is waiting for, and chunk downloads a reader is waiting for: what the
// chunk-failure recovery (`DeploymentResilience.tsx`, `reloadForChunkFailure`) reads to tell the
// failure of a prefetch, which reloads nothing, from the failure of a chunk a reader is in front
// of, which reloads from the tab's budget.
//
// WHAT THAT RULE RESTS ON. The recovery asks "is a reader waiting on any chunk?", and it can only
// answer from the chunks started through the functions of this module. A chunk a reader waits
// for and that was started some other way is invisible to it: it fails, `renderedChunksOut` is
// still zero, a prefetch of some other chunk happens to be in flight, and the page reloads
// nothing although the reader is in front of a page that cannot draw. That was the state of it
// until dispatch://INFRA-1109: the `lazy` routes of `app.tsx`, the machine sign-in route, the
// live view, the margin, the editor library and Yjs transport a mounting document awaits, the
// Yjs a past version awaits, the headless Markdown engine a drawn body awaits and the delivery
// timeline's charting library were all started bare. The shell starts prefetches at a page's
// first paint - the search bar's, the Markdown engine's warm and, on an address the landing read
// takes for the Inbox, the Inbox peek's - so a failure of any of those bare chunks in that window
// reloaded nothing.
//
// So: a chunk a render waits on goes through `renderChunk`, a chunk nothing waits on through
// `prefetchChunk`, and nothing imports a chunk through neither. A component a `Suspense` waits
// on goes through `lazyChunk`, which is `renderChunk` under React's `lazy`, and that one is held
// by construction: `lazy` itself is refused everywhere in `web/src` but this module, by the
// `noRestrictedImports` rule of the repository's `biome.json`. Where one import is both - the
// entry prefetches a route's chunk that the router then renders, the shell warms the Markdown
// engine that a body then awaits - the two are told apart at each call, not in the import,
// because the download is shared.
//
// The functions live here, apart from the recovery, so a module the recovery's own component
// imports - the Markdown engine, which it warms - can take `renderChunk` without importing that
// component back, which would be an import cycle.
//
// `vite:preloadError` is dispatched from the rejection itself, before any `then` of the promise
// these hold runs, so a render's failure always finds its own count still up.
let prefetchedChunksOut = 0;
let renderedChunksOut = 0;

/** Starts a chunk nothing is waiting for yet, and swallows its failure: the same import runs
 *  again under `renderChunk` when the router renders the route, and that is where a reader meets
 *  a chunk that will not download. */
export function prefetchChunk(load: () => Promise<unknown>): void {
  prefetchedChunksOut += 1;
  void load()
    .catch(() => undefined)
    .finally(() => {
      prefetchedChunksOut -= 1;
    });
}

/** Starts a chunk a reader is waiting on: a route the router is rendering, or a library a mounted
 *  component awaits before it can draw. While one of these is out, a chunk failure reloads from
 *  the tab's budget however many prefetches are in flight beside it. */
export function renderChunk<T>(load: () => Promise<T>): Promise<T> {
  renderedChunksOut += 1;
  const chunk = load();
  void chunk
    .catch(() => undefined)
    .finally(() => {
      renderedChunksOut -= 1;
    });
  return chunk;
}

/**
 * React's `lazy`, with the download under `renderChunk`: how every component of this app whose
 * chunk a `Suspense` waits on is written - a route, a host's dialog or sheet, a page behind a
 * gate. React asks for the chunk the first time it renders the component, so a reader is in
 * front of the boundary that waits on it, and a download that fails must spend one of the tab's
 * recovery reloads however many prefetches are in flight beside it.
 *
 * Typed on the component, as `lazy` is, so the component keeps its own props.
 */
// biome-ignore lint/suspicious/noExplicitAny: this is `lazy`'s own bound on the component it loads, passed through to it unchanged; `ComponentType<never>` is not assignable to it, through the class half of `ComponentType`.
export function lazyChunk<T extends ComponentType<any>>(
  load: () => Promise<{ default: T }>
): LazyExoticComponent<T> {
  return lazy(() => renderChunk(load));
}

/** Whether the chunks out are prefetches alone: at least one is, and no render waits on any. A
 *  chunk failure then is a prefetch's, and the recovery reloads nothing for it. */
export function onlyPrefetchesOut(): boolean {
  return prefetchedChunksOut > 0 && renderedChunksOut === 0;
}
