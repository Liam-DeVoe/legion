import type { ReactNode } from "react";
import { Link, useParams } from "react-router-dom";

import { pageMutedText } from "../../../theme/classes";
import { buildIssuePath } from "../../refs/routes";
import { NodeMarks, useNodeStatesByReference } from "../node-states/NodeMarks";
import { marksAt } from "../node-states/node-states";
import { authorText } from "../page-structure";
import { statesOfRead } from "../tree-read/tree-nodes";
import type { LensChip } from "./lens-drafts";

/**
 * One chip of a lens line: a lens, a check or a constraint, by its words (dispatch://INFRA-4065
 * R2, R3, R5). Where a step drafted the target, the tree read gives its id, and the words are the
 * link to its row on this issue's page (`?node=`, R7), drawn as G's `Chip` draws its link. The
 * issue's key is the route's own (`/issues/:key/*`), so the link works in the outline and in J's
 * Spec view alike. A target with no id, or a route with no key, draws the words alone.
 *
 * The link is out of the tab order, as every other control inside a row is, unless `tabStops`
 * holds: `f` steps to it and Enter follows it (`useTreeKeymap.ts`'s `rowStops`). The words are
 * one element right inside the chip, so part X's marks stand after them.
 */
export function NodeLink({ chip, tabStops }: { chip: LensChip; tabStops: boolean }): ReactNode {
  const { key = "" } = useParams<{ key: string }>();
  // The marks of the tree's nodes, by reference, for a chip that carries no state of its own: the
  // lens a cut-short chain stopped at is drawn from the lens facts' own map, which holds none.
  const states = useNodeStatesByReference();
  return (
    <span className="min-w-0 [overflow-wrap:anywhere]" data-node-chip="">
      {chip.id === undefined || key === "" ? (
        <span className="whitespace-nowrap" data-chip-name="" {...authorText}>
          {chip.words}
        </span>
      ) : (
        <Link
          className={`dispatch-inline-link font-medium whitespace-nowrap underline decoration-page-bullet underline-offset-[3px] ${pageMutedText}`}
          data-chip-name=""
          tabIndex={tabStops ? undefined : -1}
          to={`${buildIssuePath({ key, kind: "issue" })}?node=${encodeURIComponent(chip.id)}`}
          {...authorText}
        >
          {chip.words}
        </Link>
      )}
      {/* The marks of the node the chip names, from its state and its visibility as the tree
          read gives them, or, for a chip that carries neither, from the tree's node at its
          address; after its words in the chip's own size (dispatch://INFRA-4063 R1, R2, R7). */}
      <NodeMarks
        compact
        marks={
          chip.state === undefined && chip.visibility === undefined
            ? (marksAt(states, chip.address) ?? {})
            : statesOfRead(chip)
        }
      />
    </span>
  );
}
