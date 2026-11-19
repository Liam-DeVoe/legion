import { type ReactNode, useEffect } from "react";

import { chunk, chunkComponent } from "../shell/chunk-component";
import { prefetchChunk } from "../shell/chunk-loads";
import { useContentDrawn } from "../shell/content-drawn";
import { indicatorWrites, keptWrites } from "./shown-writes";
import { useWriteSelection } from "./write-store";

/**
 * Where the shell places its two lists of writes, and where their code is fetched: the
 * pending-writes indicator in the status corner of every signed-in page
 * (`PendingWritesIndicator.tsx`), and what a lapsed session keeps, on the sign-in page
 * (`KeptWrites.tsx`).
 *
 * Each list draws nothing while it holds no write, which is how almost every page load finds
 * it, and the two carried 3,516 bytes of the entry chunk (2,588 and 928) that every page parsed
 * before it drew anything. So each is a chunk of its own, and its host here reads the same
 * selection of the one write store the list reads (`shown-writes.ts`): no row, no list and no
 * download; a row, the list's chunk.
 *
 * The lists are drawn through `chunkComponent` (`features/shell/chunk-component.tsx`), the path
 * every chunk of this dashboard a reader is in front of takes: a chunk that is in draws in the
 * commit of the row that shows it, and a download that fails goes through `renderChunk`, so a
 * list that cannot load after a deploy spends one of the tab's recovery reloads
 * (`DeploymentResilience.tsx`). Both chunks are started once the page has drawn, so a reader who
 * presses a write, or whose session lapses, usually finds its list in already.
 */
const pendingWritesChunk = chunk<object>(() =>
  import("./PendingWritesIndicator").then((module) => module.PendingWritesIndicator)
);
const keptWritesChunk = chunk<object>(() =>
  import("./KeptWrites").then((module) => module.KeptWrites)
);

// While a chunk is still on its way its list draws nothing, as it would with no write to show.
const PendingWritesIndicator = chunkComponent(pendingWritesChunk, null);
const KeptWrites = chunkComponent(keptWritesChunk, null);

/** The pending-writes indicator, for the shell's status corner. */
export function PendingWritesHost(): ReactNode {
  const rows = useWriteSelection("indicator", indicatorWrites);
  // Both chunks are started here, after the page has drawn (`features/shell/content-drawn.ts`):
  // an effect runs after the commit it belongs to, so a landing asks for neither before its
  // first paint, and a landing that draws a live document asks only once its first block is
  // in. Nobody is waiting on either yet, which is what `prefetchChunk` tells the chunk-failure
  // recovery. The kept writes' chunk is started here too, because the sign-in page that shows
  // them follows this shell when the session lapses, and with the chunk in, the list draws with
  // that page rather than a moment after it.
  const drawn = useContentDrawn();
  useEffect(() => {
    if (!drawn) return;
    prefetchChunk(pendingWritesChunk.load);
    prefetchChunk(keptWritesChunk.load);
  }, [drawn]);
  return rows.length === 0 ? null : <PendingWritesIndicator />;
}

/** What a lapsed session keeps, for the sign-in page. */
export function KeptWritesHost(): ReactNode {
  const rows = useWriteSelection("kept-while-signed-out", keptWrites);
  return rows.length === 0 ? null : <KeptWrites />;
}
