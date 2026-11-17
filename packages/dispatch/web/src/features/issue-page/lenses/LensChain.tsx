import type { LensChain as ChainRead } from "@legion/contracts";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";

import { ApiError } from "../../../api/client";
import { pageFailedText, pageQuietText } from "../../../theme/classes";
import type { TreeNode } from "../issue-tree";
import { chipWords } from "../node-row-chip";
import { NodeMarks, useNodeStatesByReference } from "../node-states/NodeMarks";
import { marksAt } from "../node-states/node-states";
import { authorText, pageChrome } from "../page-structure";
import { LENS_ANSWERS, LENS_WORDS, NODE_TYPE_NAMES } from "../page-words";
import { kindWords, type LensChip, type LensFacts } from "./lens-drafts";
import { lensChainQuery } from "./lens-reads";
import { NodeLink } from "./NodeLink";

/**
 * The chain a focused lens stands in (dispatch://INFRA-4065 R2, R6): the lenses that triggered
 * it and the lenses it triggered, from the lens read at the lens's own address. It is drawn only
 * for a lens that has a trigger chip, and the read is sent only once the reader presses
 * `LENS_WORDS.show_chain`: the focus alone sends nothing, because `j` passes every row. The read
 * is held once in a load (`lensChainQuery`), so a later press draws the held answer and sends
 * nothing.
 *
 * Of the answer's chains it draws the one that holds this lens, in the answer's order. The
 * others are the chains of the lenses invoked on this lens, which are its rows. Each entry names
 * its lens by its kind's words as the `lens_kind` node holds them, and by the first line of the
 * row it stands under where the tree drew that row. A chain the read cut short ends with
 * `LENS_WORDS.more_lenses` and the chip of the lens it stopped at.
 *
 * The control is out of the tab order, as every other control inside a row is, unless `tabStops`
 * holds: `f` steps to it (`useTreeKeymap.ts`'s `rowStops`).
 */
export function LensChain({ node, tabStops }: { node: TreeNode; tabStops: boolean }): ReactNode {
  const lens = node.lens;
  const address = node.reference;
  if (node.type !== "lens" || lens === undefined || address === undefined) return null;
  if (lens.triggeredBy.length === 0 && lens.triggered.length === 0) return null;
  return <Chain address={address} lens={lens} tabStops={tabStops} />;
}

/** The control and, once it is pressed, what the lens read of `address` says. */
function Chain({
  address,
  lens,
  tabStops,
}: {
  address: string;
  lens: LensFacts;
  tabStops: boolean;
}): ReactNode {
  const [pressed, setPressed] = useState(false);
  const read = useQuery(lensChainQuery(address, pressed));
  return (
    <div
      className={`mt-1 flex min-w-0 flex-col gap-1 text-xs leading-5 ${pageQuietText}`}
      data-lens-chain=""
    >
      <p className="flex min-w-0 flex-wrap items-baseline">
        {/* A press leaves the focus on the row, as the row's other controls do. */}
        <button
          className={`dispatch-inline-link text-xs leading-[22px] font-medium underline decoration-page-hair underline-offset-[3px] ${pageQuietText}`}
          data-lens-show-chain=""
          onClick={() => setPressed(true)}
          onMouseDown={(event) => event.preventDefault()}
          tabIndex={tabStops ? undefined : -1}
          type="button"
          {...pageChrome}
        >
          {LENS_WORDS.show_chain}
        </button>
      </p>
      {/* An answer once held stays drawn through a later read that fails - a refresh of every
          read the page holds sends this one again - since TanStack keeps the answer and sets the
          error beside it (dispatch://INFRA-1196 decision 98). The failure is said only where no
          answer is held and no read is out, as the menu says its catalogue's. */}
      {!pressed ? null : read.data !== undefined ? (
        <ChainEntries
          chain={read.data.chains?.find((each) =>
            (each.lenses ?? []).some((entry) => entry.address === address)
          )}
          lens={lens}
          tabStops={tabStops}
        />
      ) : read.isError && !read.isFetching ? (
        <p className={pageFailedText} role="status" {...pageChrome}>
          {read.error instanceof ApiError && read.error.status === 404
            ? LENS_ANSWERS.gone
            : LENS_ANSWERS.chain_failed}
        </p>
      ) : (
        <p role="status" {...pageChrome}>
          {LENS_ANSWERS.loading_chain}
        </p>
      )}
    </div>
  );
}

/** The one chain that holds the focused lens, entry by entry, and, where the read cut it short,
 *  the lens it stopped at. */
function ChainEntries({
  chain,
  lens,
  tabStops,
}: {
  chain: ChainRead | undefined;
  lens: LensFacts;
  tabStops: boolean;
}): ReactNode {
  // The marks of the tree's nodes, by reference: an entry carries those of the tree's node at its
  // lens's address, never the lens read's own state and visibility.
  const states = useNodeStatesByReference();
  if (chain === undefined) return null;
  const stoppedAt = chain.stopped_at;
  return (
    <>
      <ol className="flex min-w-0 flex-col">
        {(chain.lenses ?? []).map((entry) => {
          const place = lens.lenses?.get(entry.address);
          const placed = place?.kindWords;
          const held = filled(placed) ? placed : entry.kind?.words;
          const onLine = place?.onLine;
          return (
            <li
              className="flex min-w-0 flex-wrap items-baseline gap-x-2"
              data-lens-address={entry.address}
              data-lens-chain-entry=""
              key={entry.address}
            >
              {filled(held) ? (
                <span
                  className="min-w-0 break-words font-medium"
                  data-lens-chain-kind=""
                  {...authorText}
                >
                  {held}
                </span>
              ) : (
                <span className="font-medium" data-lens-chain-kind="" {...pageChrome}>
                  {NODE_TYPE_NAMES.lens}
                </span>
              )}
              {/* After the lens's name, in the chain's own size; none where the tree holds no
                  node at that address. */}
              <NodeMarks compact marks={marksAt(states, entry.address) ?? {}} />
              {filled(onLine) ? (
                <span className="min-w-0 break-words" data-lens-chain-line="" {...authorText}>
                  {onLine}
                </span>
              ) : null}
            </li>
          );
        })}
      </ol>
      {stoppedAt === null || stoppedAt === "" ? null : (
        <p className="flex min-w-0 flex-wrap items-baseline gap-x-2" data-lens-chain-more="">
          <span className="shrink-0" {...pageChrome}>
            {LENS_WORDS.more_lenses}
          </span>
          <NodeLink chip={stoppedChip(stoppedAt, lens)} tabStops={tabStops} />
        </p>
      )}
    </>
  );
}

/** The chip of the lens a chain stopped at, named as a chip names any lens - by its kind's words,
 *  capitalised and cut as the tag is - and linked to its row where the tree drew one. */
function stoppedChip(address: string, lens: LensFacts): LensChip {
  const place = lens.lenses?.get(address);
  const held = place?.kindWords;
  return {
    address,
    id: place?.id,
    words: chipWords(kindWords(filled(held) ? held : NODE_TYPE_NAMES.lens)),
  };
}

/** Whether a stored string has words to draw. */
function filled(text: string | undefined): text is string {
  return text !== undefined && text !== "";
}
