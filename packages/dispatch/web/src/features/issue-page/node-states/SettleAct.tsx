import { matchQuery, type QueryClient, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useRef, useState } from "react";

import { ApiError } from "../../../api/client";
import { issueTreeKey } from "../../../api/queries";
import { pageFailedText, pageRowAct } from "../../../theme/classes";
import { writesOf } from "../../writes/write-store";
import type { TreeNode } from "../issue-tree";
import { pageChrome } from "../page-structure";
import { CONTROL_LABELS, SETTLE_ANSWERS } from "../page-words";
import { READER_ACTS } from "../useNodeEditing";
import { settleKeyOf, useSettle, useSettleIssue } from "./settle-write";

/** The sentence a refused settle draws after the act, read from the refusal's status and
 *  code, never from its text: every refusal the page names no sentence for - a 403 with no code,
 *  a 400 `INVALID_JSON`, a page's own error - is the one that says it could not. */
function refusalWords(refusal: Error): string {
  if (refusal instanceof ApiError) {
    if (refusal.status === 403 && refusal.code === "SETTLE_NOT_PERMITTED") {
      return SETTLE_ANSWERS.not_permitted;
    }
    if (refusal.status === 409 && refusal.code === "NODE_NOT_PROPOSED") {
      return SETTLE_ANSWERS.not_proposed;
    }
    if (refusal.status === 404) return SETTLE_ANSWERS.gone;
  }
  return SETTLE_ANSWERS.failed;
}

/** Puts the focus on the row `control` is drawn in (`NodeRow`'s `data-node-row`), with no
 *  scroll: the reader is already looking at it. */
function focusRowOf(control: HTMLElement | null): void {
  control?.closest<HTMLElement>("[data-node-row]")?.focus({ preventScroll: true });
}

/** The read of the tree a press of Settle waits for once its write has landed
 *  (`watchLandingRead`). */
interface LandingRead {
  /** Whether the act waits for that read: the cache holds the tree, and the read has not ended. */
  readonly waits: () => boolean;
  readonly stop: () => void;
}

/**
 * Watches, from a press of Settle, for the read of the tree of issue `issue` that tells of the
 * press's landing: the first read of it to begin with no write of that issue out, so after
 * Dispatch answered the press's own write, which is one of that issue's (`useSettle`). A read
 * already out at the press, or one begun while a write of the issue was out - a first load, the
 * reader's Retry - may read the tree from before the settle, and is passed over. `ended` is told
 * once that read has ended, answered or refused, or once the cache lets the tree go, which no
 * read then answers, and the watch ends there.
 */
function watchLandingRead(client: QueryClient, issue: string, ended: () => void): LandingRead {
  const cache = client.getQueryCache();
  const writes = writesOf(client);
  const tree = { exact: true, queryKey: issueTreeKey(issue) };
  // Whether the read out began with no write of the issue out.
  let tells = false;
  let done = false;
  const stop = cache.subscribe((event) => {
    if (!matchQuery(tree, event.query)) return;
    if (event.type === "updated" && event.action.type === "fetch") {
      tells = writes.writesOutOf(issue).length === 0;
    } else if (event.type === "removed" || (tells && event.query.state.fetchStatus === "idle")) {
      done = true;
      stop();
      ended();
    }
  });
  return { stop, waits: () => !done && cache.find(tree) !== undefined };
}

/**
 * Settle, among the acts of a focused proposed row (`NodeRow.tsx`'s `RowActs`). No key of its own
 * presses it: it carries `data-row-act`, which `useTreeKeymap.ts`'s `KEYED_CONTROLS` names only
 * for Reply and Answer, so `f` reaches it as it reaches each control no key presses, and Enter or
 * a press sends the settle (`settle-write.ts`). It needs no writer: a cookie proves a person, so
 * every reader of the page gets it. It is drawn for a node whose own address names its issue, and
 * for one whose address names none - a lens's, a risk's, a constraint's, a finding's - under the
 * issue the tree is drawn for (`settle-write.ts`'s `SettleIssueProvider`); with neither, nothing.
 *
 * A pointer's press leaves the focus where it is, as the row's other acts do; a press of the act
 * holding the focus (Enter after `f`) moves it to the row first, since the act takes no press
 * while the write is out, and says so. On a landing the focus goes to the row before the reads, so
 * `j` goes on from there; the reads then draw the row with no mark and no act. Until the tree's
 * read answers, the act goes on saying "Settling…" and takes no press, though the write settled
 * once the coverage's read answered: a press then would settle a node Dispatch holds settled, and
 * be refused (`watchLandingRead`). Where that read draws the node still proposed, the act takes a
 * press again. A refusal reads nothing and leaves the row as drawn, its mark included, and draws
 * one sentence after the act with the role `status`, until the reader's next act.
 */
export function SettleAct({
  node,
  tabStops = false,
}: {
  node: TreeNode;
  /** Whether the act keeps its place in the tab order (`NodeRow`'s `tabStops`). */
  tabStops?: boolean;
}): ReactNode {
  const treeIssue = useSettleIssue();
  const reference = node.reference;
  const key = reference === undefined ? undefined : (settleKeyOf(reference) ?? treeIssue);
  if (reference === undefined || key === undefined) return null;
  return <Settle issueKey={key} reference={reference} tabStops={tabStops} />;
}

function Settle({
  issueKey,
  reference,
  tabStops,
}: {
  issueKey: string;
  reference: string;
  tabStops: boolean;
}): ReactNode {
  const control = useRef<HTMLButtonElement>(null);
  const client = useQueryClient();
  // The issue whose tree the landing reads again, as `useSettle` reads it.
  const treeIssue = useSettleIssue() ?? issueKey;
  // The read of the tree the newest press waits for once it lands, and whether the act waits.
  const landingRead = useRef<LandingRead | undefined>(undefined);
  const [reading, setReading] = useState(false);
  useEffect(() => () => landingRead.current?.stop(), []);
  const settle = useSettle(issueKey, reference, () => {
    focusRowOf(control.current);
    setReading(landingRead.current?.waits() === true);
  });
  const { error, reset } = settle;
  const out = settle.isPending || reading;
  // The sentence goes at the reader's first act after it was drawn, read in the capture phase so
  // a handler that answers the act and stops it still ends it; the listeners go with it, or with
  // the act.
  useEffect(() => {
    if (error === null) return;
    const acted = new AbortController();
    const end = () => {
      acted.abort();
      reset();
    };
    for (const type of READER_ACTS) {
      document.addEventListener(type, end, { capture: true, signal: acted.signal });
    }
    return () => acted.abort();
  }, [error, reset]);
  return (
    <>
      <button
        className={`dispatch-inline-link ${pageRowAct}`}
        data-row-act="settle"
        disabled={out}
        onClick={(event) => {
          // A focused control that is disabled drops the focus to the page, and the tree then lets
          // go of the row, its acts with it; on the row the focus stays in the tree.
          if (event.currentTarget.contains(document.activeElement)) {
            focusRowOf(event.currentTarget);
          }
          landingRead.current?.stop();
          landingRead.current = watchLandingRead(client, treeIssue, () => setReading(false));
          settle.mutate();
        }}
        onMouseDown={(event) => event.preventDefault()}
        ref={control}
        tabIndex={tabStops ? undefined : -1}
        type="button"
        {...pageChrome}
      >
        {out ? SETTLE_ANSWERS.settling : CONTROL_LABELS.settle}
      </button>
      {error === null ? null : (
        <span className={pageFailedText} data-settle-answer="" role="status" {...pageChrome}>
          {refusalWords(error)}
        </span>
      )}
    </>
  );
}
