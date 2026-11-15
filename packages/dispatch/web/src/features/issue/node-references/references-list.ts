import type { GraphEdge } from "../../../api/types";
import { firstLineOf } from "../issue-tree";

/**
 * What references one node, as Details draws it (dispatch://INFRA-1196 R14, R19): one row per
 * node that points at the focused one, in the order the graph read answers them, which is
 * newest first.
 *
 * The old page drew this per block of a document, as a disclosure on the Spec, Verification and
 * Plan tabs (`features/refs/ReferencedBy.tsx`'s `BlockReferences` at the merge base): what
 * serves the block, what needs it, and every mention of it. The new page draws `serves` as the
 * spine - a check and a plan task sit under the requirement they serve - and `needs` as a chip
 * (`tree-drafts.ts`'s `servePlacements`), so those two reach the reader from the outline; a
 * mention of a node reaches them nowhere else, which is what this list is for.
 *
 * Pure, so the rows are read without a browser: the component draws them and holds the read.
 */

/** The edge kind that is no tuple of the model: one node's text names another. */
const MENTIONS = "mentions";

/** A typed block's own opening line, `:::check{#c1 serves="…"}`, and its closing fence. */
const DIRECTIVE_OPENER = /^:{3,}[a-z_]/;
const DIRECTIVE_FENCE = /^:{3,}\s*$/;

/** One node that references the focused one. */
export interface ReferenceRow {
  /** What the citing node does for the focused one, as the graph names the edge's kind:
   *  `serves`, `needs`, `answers`, `mentions` and the rest. The component draws the page's
   *  word for it, never this token (R22). */
  readonly kind: string;
  /** The citing node's own first line, as every place that draws one line for a node cuts it
   *  (R1, `firstLineOf`); `""` where the read carries no words of it. */
  readonly line: string;
  /** What kind of node cites, as the graph names it: `block`, `comment`, `ask`, `message`,
   *  `artifact`, `issue`, `component`, `session`. The component draws the page's word for it
   *  where the edge's own kind has none. */
  readonly nodeKind: string;
  /** The citing node's own `dispatch://` address, which the row's link opens; `undefined` for
   *  a node the read names no address for, and such a row is no link. */
  readonly ref: string | undefined;
  /** The citing node itself, which is what makes it one row however many edges it holds. */
  readonly key: string;
}

/**
 * The words the read carries for the citing node: the excerpt the server sends for a node that
 * has text of its own, which for a block of a document is that block's stored excerpt
 * (`packages/envoy/internal/dispatch/refs/graph.go:250-258`), else the node's own text.
 *
 * A typed block's excerpt opens on the directive's line - its type and its attributes - and
 * ends on its closing fence. Both are markup and no word of the block, and the line holds a
 * block id and a slug, which the page draws nowhere a word belongs (R22), so both go. This is
 * `tree-drafts.ts`'s `blockWords`, which names a chip's target by the same rule and is private
 * to that module; the handoff asks integration to export it and to delete this copy.
 */
export function nodeWordsOf(edge: GraphEdge): string {
  const text = edge.excerpt?.text ?? edge.node.text ?? "";
  const lines = text.trimEnd().split("\n");
  if (!DIRECTIVE_OPENER.test(lines[0] ?? "")) return text;
  lines.shift();
  if (DIRECTIVE_FENCE.test(lines.at(-1) ?? "")) lines.pop();
  return lines.join("\n").trim();
}

/**
 * One row per citing node, in the read's own order.
 *
 * A node that cites the focused one twice - a comment that names it in two of its blocks, a
 * check that both serves it and names it in its words - is one row, because the row is a way
 * to that node and the node is one place. Which of its edges names the row is the one that
 * says the most: a tuple of the model over a mention, since "served by" tells the reader what
 * the node does and a mention tells them only that it was named. The row keeps the place of
 * the first edge either way, so the list stays in the order the read answered.
 */
export function referenceRows(edges: readonly GraphEdge[]): ReferenceRow[] {
  const rows = new Map<string, ReferenceRow>();
  for (const edge of edges) {
    const key = `${edge.node.kind}:${edge.node.id}`;
    const held = rows.get(key);
    if (held !== undefined && (held.kind !== MENTIONS || edge.kind === MENTIONS)) continue;
    const words = nodeWordsOf(edge);
    rows.set(key, {
      key,
      kind: edge.kind,
      line: words === "" ? "" : firstLineOf(words).line,
      nodeKind: edge.node.kind,
      ref: edge.node.ref,
    });
  }
  return [...rows.values()];
}
