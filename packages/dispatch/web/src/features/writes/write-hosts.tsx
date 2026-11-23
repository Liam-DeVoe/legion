import { type ReactNode, useEffect, useMemo, useState } from "react";

import { ChunkBoundary } from "../shell/ChunkBoundary";
import { chunk, chunkComponent } from "../shell/chunk-component";
import {
  importWhenOnline,
  keptUntilRejected,
  prefetchChunk,
  renderChunk,
} from "../shell/chunk-loads";
import { useContentDrawn } from "../shell/content-drawn";
import { indicatorWrites, keptWrites } from "./shown-writes";
import { kindsRegistered, useWriteSelection, type Write } from "./write-store";

/**
 * Where the shell places its two lists of writes, and where their code is fetched: the
 * pending-writes indicator in the status corner of every signed-in page
 * (`PendingWritesIndicator.tsx`), and what a lapsed session keeps, on the sign-in page
 * (`KeptWrites.tsx`). Where every kind of write the page makes is fetched too (`kinds.ts`).
 *
 * Each list draws nothing while it holds no write, which is how almost every page load finds
 * it, and the two carried 3,516 bytes of the entry chunk (2,588 and 928) that every page parsed
 * before it drew anything. So each is a chunk of its own, and its host here reads the same
 * selection of the one write store the list reads (`shown-writes.ts`): no list before the first
 * row. The indicator stays mounted from then on and draws nothing once none is left
 * (`PendingWritesHost` says why); the kept list is drawn only while it holds a row.
 *
 * The lists are drawn through `chunkComponent` (`features/shell/chunk-component.tsx`), as the
 * issue page's tab panels, a document's decision cards and the reference-preview layer are; a
 * route goes through `lazyChunk`, and a surface a key opens through `askedSurface`. A chunk that
 * is in draws in the commit of the row that shows it, and a download that fails goes through
 * `renderChunk`, so a list that cannot load after a deploy spends one of the tab's recovery
 * reloads (`DeploymentResilience.tsx`). Both hosts sit outside the page's error boundary - the
 * status corner, and the sign-in page - so each list has a boundary of its own (`ChunkBoundary`):
 * one that fails online draws nothing, as it would with no write to show, and the rest of the
 * page stays drawn and signed in. A change of which writes a list shows, or of where one stands -
 * a write pressed or gone, one refused or late - draws the list afresh (`reset`,
 * `shownWritesKey`), which asks for its chunk again. A boundary that never reset would leave the
 * reader on a page no recovery reload follows - the boundary says when none does - with no Retry,
 * Discard or Stop trying for the life of the page.
 * On the sign-in page the reader changes none of those writes, so the kept list asks again as
 * well when the reader comes back to it (`KeptWritesHost`). Both chunks are started once the page
 * has drawn, so a reader who presses a write, or whose session lapses, usually finds its list in
 * already.
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

/**
 * Every kind of write the page makes (`kinds.ts`), which only the store's take-up of what a
 * reload kept reads (`Writes.resume`): a write the page makes itself registers its own kind as
 * the module its component imports for it is evaluated. So the kinds are a chunk of their own,
 * about 22,000 bytes no first paint draws taken out of the entry (dispatch://INFRA-1109, R13).
 */
const writeKinds = keptUntilRejected(() => import("./kinds"));

/**
 * Loads every kind of write, for the store's take-up (`app.tsx`, `Writes.resume`), which every
 * write the reader presses meanwhile waits behind. Under `renderChunk`, since a reader waits on
 * it: a download that fails spends one of the tab's recovery reloads (`DeploymentResilience.tsx`),
 * and the reloaded page takes up every write this one held, kept in the tab from its press.
 * Under `importWhenOnline`, so a download that fails offline is asked for again once the network
 * is back, rather than reloading into the browser's offline page.
 */
export function loadWriteKinds(): Promise<unknown> {
  return renderChunk(() => importWhenOnline(writeKinds));
}

/**
 * Starts the kinds of write for a host, nobody waiting on them yet (`prefetchChunk`), and once
 * the load answers ends every take-up still waiting for them (`kindsRegistered`). In the app that
 * end is never the one that counts: every load of the kinds goes through `writeKinds`, which
 * keeps the module once it has come in, so the load that brings them in evaluates `kinds.ts`,
 * whose evaluation ends every take-up waiting then, and a take-up that begins after that ends
 * through its own ask, answered from what is in (`Writes.resume`). It counts only where a
 * take-up's own ask is not `writeKinds` and fails after the kinds are in - the loader a test
 * hands `Writes.resume` (`write-hosts.test.tsx`) - and there it keeps the take-up's end from
 * turning on which load evaluated the module.
 */
function prefetchWriteKinds(): void {
  prefetchChunk(() => writeKinds().then(kindsRegistered));
}

/** What a list's boundary draws afresh on (`ChunkBoundary`'s `reset`): which writes it shows, and
 *  where each stands. A change of either may be the reader's way to the list's controls - a write
 *  pressed or gone, one refused with Retry and Discard, one late with Stop trying - while an
 *  upload's progress, which changes a row at every percent, changes neither: a boundary whose
 *  chunk keeps failing would otherwise ask for it again at every percent of an upload. */
function shownWritesKey(rows: readonly Write[]): string {
  return rows.map((row) => `${row.id}:${row.status}`).join(",");
}

/** The pending-writes indicator, for the shell's status corner. */
export function PendingWritesHost(): ReactNode {
  const rows = useWriteSelection("indicator", indicatorWrites);
  // Mounted from the first row on, and never unmounted for want of one: the indicator draws
  // nothing itself while nothing is pending, and the focus it hands on as its panel goes - after
  // a Discard or a Stop trying that takes the last row away - is a layout effect of its own
  // (`usePanelFocusReturn`), which runs only in a commit it is still mounted in. Unmounted in the
  // commit its last row goes, it would leave the focus on the page's body. Set in the render that
  // first sees a row, so that render draws it already.
  const [shown, setShown] = useState(false);
  if (rows.length > 0 && !shown) setShown(true);
  // Both chunks are started here, after the page has drawn (`features/shell/content-drawn.ts`):
  // an effect runs after the commit it belongs to, so a landing asks for neither before its
  // first paint, and a landing that draws a live document asks only once its first block is
  // in. Nobody is waiting on either yet, which is what `prefetchChunk` tells the chunk-failure
  // recovery. The kept writes' chunk is started here too, because the sign-in page that shows
  // them follows this shell when the session lapses, and with the chunk in, the list draws with
  // that page rather than a moment after it. So are the kinds of write: the take-up asked for
  // them as the person signed in, and this finds that download in or on its way. One the take-up
  // lost before the page drew, with no reload to follow - the page was being left, the reader
  // chose to stay at the leave prompt that the writes the reload kept (`WriteStorage.load`) or a
  // write pressed meanwhile open, or the tab's recovery reloads are spent - is asked for again
  // here, and its evaluation ends the take-up (`kindsRegistered`) with no press to ask for them
  // (`Writes.run`).
  const drawn = useContentDrawn();
  useEffect(() => {
    if (!drawn) return;
    prefetchChunk(pendingWritesChunk.load);
    prefetchChunk(keptWritesChunk.load);
    prefetchWriteKinds();
  }, [drawn]);
  const reset = useMemo(() => shownWritesKey(rows), [rows]);
  return rows.length > 0 || shown ? (
    <ChunkBoundary reset={reset}>
      <PendingWritesIndicator />
    </ChunkBoundary>
  ) : null;
}

/** What a lapsed session keeps, for the sign-in page. */
export function KeptWritesHost(): ReactNode {
  const rows = useWriteSelection("kept-while-signed-out", keptWrites);
  // The kinds of write, for a take-up of what a reload kept that still waits for them as the
  // session lapses (`Writes.resume`): it takes them up all the same, for the sign-in that follows,
  // and only then are they rows of the store this list shows. The shell that asked for them is
  // gone, so if its download failed with no reload to follow, nothing else on this page would ask
  // again and the list would never show those writes. Nobody waits on them here, which is what
  // `prefetchChunk` tells the chunk-failure recovery. A load that fails here too is asked for
  // again as the reader comes back to the page (below).
  useEffect(prefetchWriteKinds, []);
  // A list whose chunk failed draws again on a change of the writes it shows, and on the sign-in
  // page none may come: the reader presses no write there, and one out may never be answered. So
  // it draws again too as the reader comes back to the page - the window gets the focus back, or
  // the browser comes back online - which is what a reader who finds the list missing does
  // before they reload by hand. A list that has not failed draws nothing anew for either. The
  // kinds are asked for again then as well: a take-up still waiting for them holds back every
  // write it would put in this list, so a list waiting on them draws nothing whatever else asks.
  const [returns, setReturns] = useState(0);
  useEffect(() => {
    const returned = () => {
      prefetchWriteKinds();
      setReturns((count) => count + 1);
    };
    window.addEventListener("focus", returned);
    window.addEventListener("online", returned);
    return () => {
      window.removeEventListener("focus", returned);
      window.removeEventListener("online", returned);
    };
  }, []);
  const reset = useMemo(() => `${shownWritesKey(rows)};${returns}`, [rows, returns]);
  return rows.length === 0 ? null : (
    <ChunkBoundary reset={reset}>
      <KeptWrites />
    </ChunkBoundary>
  );
}
