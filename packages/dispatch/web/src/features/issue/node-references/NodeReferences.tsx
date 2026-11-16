import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useId, useState } from "react";
import { Link } from "react-router-dom";

import { linkHoverText, linkText, pageHeaderControl, pageQuietText } from "../../../theme/classes";
import { useMarginDrawer } from "../../margin/margin-slot";
import { drawsEdge } from "../../refs/ReferencedBy";
import { referencesQuery } from "../../refs/reference-target";
import { buildReferencePath, parseNodeReference, shortForm } from "../../refs/routes";
import { PHONE_VIEWPORT_QUERY, useMediaQuery } from "../../shell/useDialog";
import type { FocusedNode } from "../focused-node";
import { authorText, freeze, pageChrome } from "../page-structure";
import { CONTROL_LABELS } from "../page-words";
import { referenceRows } from "./references-list";

/**
 * Every word this part draws. Integration moves them into `page-words.ts`: the control and the
 * two tables into `CONTROL_LABELS`' neighbours, and the two sentences into `ANSWER_LINES`,
 * which is where what the page says in answer to the reader's own act is declared (R22).
 */
export const NODE_REFERENCES_WORDS = freeze({
  /**
   * The relation each kind of tuple puts the citing node in with the row the reader is on.
   * They are the words the old page's Referenced-by panel drew for the same edges
   * (`features/refs/ReferencedBy.tsx`'s `STRUCTURAL_LABELS`), lowercase because the row draws
   * them beside the node's own words, where a capital reads as a proper noun (R19, R22).
   */
  edges: {
    anchored_to: "anchored item",
    answers: "answer",
    attached_to: "attached document",
    blocked_by: "blocked issue",
    child_of: "child issue",
    followed_by: "follower",
    needs: "needed by",
    owned_by: "owned item",
    replies_to: "reply",
    serves: "served by",
  },
  /**
   * The page's word for each kind of node the graph answers with, which names a row whose edge
   * is a mention. `artifact` and `block` are both a document to a reader, and a session is the
   * conversation of one agent (R22: no schema token where a word belongs).
   */
  kinds: {
    artifact: "document",
    ask: "question",
    block: "document",
    comment: "comment",
    component: "component",
    issue: "issue",
    message: "message",
    session: "agent",
  },
  /** What the list says while its read is out, and when the read failed. */
  loading: "Loading references…",
  unreadable: "Could not load the references.",
  /** The control in Details that lists what references the row the reader is on (R14, R19).
   *  It names the row, because Details' References section holds the issue's own Referenced by
   *  beside it and two lists cannot share one name. */
  what_references: "What references this row",
} as const);

const EDGE_WORDS: Readonly<Record<string, string>> = NODE_REFERENCES_WORDS.edges;
const KIND_WORDS: Readonly<Record<string, string>> = NODE_REFERENCES_WORDS.kinds;

/**
 * What references the row the reader is on, in Details (dispatch://INFRA-1196 R14: the panel
 * holds "what references the issue or the focused node"; R19's table: "A block's 'Referenced
 * by' list" goes to "The focused node's facts in the panel").
 *
 * One link per node that points at the focused one, mentions included, each landing on that
 * node: a node of this issue opens focused in the outline, and a node of another issue or of a
 * project document opens where that address opens (R20, `buildReferencePath`). The outline
 * already carries two of the three edges the old disclosure listed - what serves a node is its
 * spine and what needs it is a chip - and nothing on the page points back from a node to what
 * mentions it, so this is where a reader reads that.
 *
 * It costs no request on the first load and none per node (R21). Two gates hold that: the
 * reader opens the list, and Details is open around it, which on the issue page it is not when
 * the page loads. The list closes again when the focus moves to another row, so a reader
 * walking the outline with Details open reads one node's references and not every node's. The
 * read is `referencesQuery`, under the key every other Referenced-by panel reads
 * (`["references", "to", <address>]`), so a node read here and opened elsewhere is read once
 * and an event that changes it refreshes both (`api/event-refresh.ts`).
 */
export function NodeReferences({
  node,
  reference,
}: {
  /** The row the reader is on. The issue's own references are Details' References row, so the
   *  issue draws no list of its own here and nothing is drawn twice (R14, c-one-panel). */
  node: Pick<FocusedNode, "id" | "type">;
  /** The focused node's own `dispatch://` address (`TreeNode.reference`); `undefined` for a
   *  node the model gives no address, which nothing can reference. */
  reference: string | undefined;
}): ReactNode {
  const drawer = useMarginDrawer();
  const phone = useMediaQuery(PHONE_VIEWPORT_QUERY);
  const panelId = useId();
  // Which row the reader opened the list on, so the focus moving to another row closes it:
  // the state is the row's own id and not a flag.
  const [openedOn, setOpenedOn] = useState<string | undefined>(undefined);
  const addressed = node.type === "issue" ? undefined : reference;
  const open = addressed !== undefined && openedOn === node.id;
  // Outside the shell no drawer holds the panel and it is always open; on the issue page it is
  // the Details drawer, which opens shut.
  const reading = open && (drawer === undefined || drawer.open);
  const query = useQuery({ ...referencesQuery(addressed), enabled: reading });
  // An edge a newer build answers with, of a kind or to a node this page does not know, is left
  // out, as every list of a node's references leaves it (`drawsEdge`): its kind's word would be
  // the node kind's, which says it was only named.
  const rows = referenceRows((query.data?.edges ?? []).filter(drawsEdge));
  if (addressed === undefined) {
    return null;
  }
  return (
    <section
      aria-label={NODE_REFERENCES_WORDS.what_references}
      className="mt-2"
      data-node-references={node.id}
    >
      <button
        aria-controls={panelId}
        aria-expanded={open}
        className={pageHeaderControl}
        onClick={() => setOpenedOn(open ? undefined : node.id)}
        type="button"
        {...pageChrome}
      >
        {NODE_REFERENCES_WORDS.what_references}
      </button>
      {open ? (
        <div id={panelId}>
          {reading && query.isPending ? (
            <p className={`mt-1 text-xs ${pageQuietText}`} {...pageChrome}>
              {NODE_REFERENCES_WORDS.loading}
            </p>
          ) : null}
          {query.isError ? (
            <p className={`mt-1 flex flex-wrap items-baseline gap-2 text-xs ${pageQuietText}`}>
              <span {...pageChrome}>{NODE_REFERENCES_WORDS.unreadable}</span>
              <button
                className={`font-medium underline ${linkText} ${linkHoverText}`}
                disabled={query.isFetching}
                onClick={() => void query.refetch()}
                type="button"
                {...pageChrome}
              >
                {CONTROL_LABELS.retry}
              </button>
            </p>
          ) : null}
          {query.isSuccess && rows.length === 0 ? (
            <p className={`mt-1 text-xs ${pageQuietText}`} {...pageChrome}>
              {CONTROL_LABELS.none}
            </p>
          ) : null}
          {rows.length === 0 ? null : (
            <ul className="mt-1 flex min-w-0 flex-col gap-0.5">
              {rows.map((row) => {
                const route = row.ref === undefined ? undefined : parseNodeReference(row.ref);
                // The edge's own word where the model gives the tuple one, and the kind of
                // node where it does not, which is every mention: the old panel's own rule.
                const word = EDGE_WORDS[row.kind] ?? KIND_WORDS[row.nodeKind];
                // A node the read carries no words of is named by its address's short form,
                // "CORE-1 spec block", as a chip names such a target (`NodeRow.tsx`'s `Chip`).
                // A node with neither is drawn as its word alone: its id is no name (R22).
                const line = row.line === "" && route !== undefined ? shortForm(route) : row.line;
                return (
                  <li className="flex min-w-0 items-baseline gap-2 text-sm" key={row.key}>
                    {word === undefined ? null : (
                      <span className={`shrink-0 text-xs ${pageQuietText}`} {...pageChrome}>
                        {word}
                      </span>
                    )}
                    {line === "" ? null : route === undefined ? (
                      <span className="min-w-0 truncate" {...authorText}>
                        {line}
                      </span>
                    ) : (
                      <Link
                        className={`dispatch-inline-link min-w-0 flex-1 truncate underline ${linkText} ${linkHoverText}`}
                        onClick={() => {
                          // On a phone Details covers the outline the link focuses a row of.
                          if (phone) drawer?.setOpen(false);
                        }}
                        to={buildReferencePath(route)}
                        {...authorText}
                      >
                        {line}
                      </Link>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      ) : null}
    </section>
  );
}
