import type { ReactNode } from "react";

import { Button } from "../../components/Button";
import { DisclosureToggle } from "../../components/DisclosureToggle";
import { textMutedOnCanvas } from "../../theme/classes";
import type { InboxGroup } from "./grouping";
import type { InboxListRow } from "./list-row";

/** How many rows a band draws at once, and how many more each **Show more** adds. A band of a
 *  thousand threads is a scroll nobody asked for, and the rows behind the first page are one
 *  press away. */
export const GROUP_PAGE = 50;

/**
 * One band as items of the Inbox's single list: its heading, the rows it is drawing, and
 * **Show more** while it is holding any back. A plain function rather than a component, so
 * every row stays a direct child of the one keyed `<ul>` and a row that changes band moves
 * there instead of remounting under another parent.
 *
 * The caller decides which rows are drawn - a folded band draws almost none, and a long one
 * draws its first page - because the same rule decides what the selection steps through and
 * what the marks count.
 */
export function groupItems({
  folded,
  group,
  held,
  onShowMore,
  onToggleFold,
  renderRow,
  rows,
}: {
  /** Whether the reader has folded this band shut. */
  folded: boolean;
  group: InboxGroup;
  /** How many of the band's rows are behind **Show more**; none draws no button. */
  held: number;
  onShowMore: (key: string) => void;
  onToggleFold: (key: string) => void;
  renderRow: (row: InboxListRow) => ReactNode;
  /** The rows to draw, in order. */
  rows: readonly InboxListRow[];
}): ReactNode[] {
  const heading =
    group.label === undefined ? null : (
      // A plain list item: `role="presentation"` here took the item out of the list while
      // leaving it a child of the `<ul>`, which is a list element with a child that is no list
      // item (axe's `list`, serious). The band divider is an item of the list it divides.
      <li className="pt-4 first:pt-0" key={`heading-${group.key}`}>
        {/* A band divider, not a heading that competes with the rows under it. */}
        <h2 className={textMutedOnCanvas}>
          <DisclosureToggle
            expanded={!folded}
            label={`${group.label} (${group.rows.length})`}
            onToggle={() => onToggleFold(group.key)}
            textClassName={textMutedOnCanvas}
          />
        </h2>
      </li>
    );
  return [
    heading,
    ...rows.map((row) => renderRow(row)),
    held > 0 && !folded ? (
      <li className="mt-2" key={`more-${group.key}`}>
        <Button data-inbox-show-more={group.key} onClick={() => onShowMore(group.key)} size="sm">
          Show {Math.min(held, GROUP_PAGE)} more
          {group.label === undefined ? "" : ` in ${group.label}`}
        </Button>
      </li>
    ) : null,
  ];
}
