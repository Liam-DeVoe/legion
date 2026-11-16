import { type ReactNode, useRef } from "react";
import {
  issueStatusScale,
  surfaceMutedBg,
  surfaceMutedHoverBg,
  tableFilterActive,
  textSecondaryOnCanvas,
} from "../../theme/classes";
import { issueStatuses, statusText } from "./board-model";
import type { Item } from "./issues-table-model";

/** One segment of a bar: the value it counts, its label, its fill and how many rows it holds. */
export interface BarSegment {
  readonly value: string;
  readonly label: string;
  readonly fill: string;
  readonly count: number;
}

const STATUS_FILLS: Readonly<Record<string, string>> = issueStatusScale;

/** What the bar counts `items` by: their statuses in the status order, in the status scale,
 *  each status with no row left out. A status this bundle does not know counts last. */
export function barSegments(items: readonly Item[]): BarSegment[] {
  const counts = new Map<string, number>();
  for (const item of items) counts.set(item.issue.status, (counts.get(item.issue.status) ?? 0) + 1);
  const ordered: readonly { value: string; label: string; fill: string }[] = issueStatuses.map(
    (status) => ({ value: status, label: statusText(status), fill: STATUS_FILLS[status] ?? "" })
  );
  const known = new Set(ordered.map((entry) => entry.value));
  const others = [...counts.keys()].filter((value) => !known.has(value));
  return [
    ...ordered,
    ...others.map((value) => ({ value, label: statusText(value), fill: surfaceMutedBg })),
  ].flatMap((entry) => {
    const count = counts.get(entry.value) ?? 0;
    return count === 0 ? [] : [{ ...entry, count }];
  });
}

/**
 * The bar under the view tabs: the listed rows counted by status, from the left edge, each
 * segment as wide as its count's share. Each segment's legend entry is its press target: a
 * press narrows the view to that status, a second press restores the view. The legend wraps
 * onto as many lines as it needs. A view with no bar (Done) draws none, so its table starts
 * right under the tabs and the control row.
 */
export function ViewBar({
  items,
  narrowTo,
  picked,
}: {
  /** The rows the table lists. */
  items: readonly Item[];
  /** Narrows the view to the statuses `values`, or restores the view with `undefined`. */
  narrowTo: (values: readonly string[] | undefined) => void;
  /** The statuses the view is narrowed to by a press, if it is. */
  picked: readonly string[] | undefined;
}): ReactNode {
  // The pick a press made, so the second press restores exactly the view before it.
  const pressed = useRef<string | undefined>(undefined);
  const segments = barSegments(items);
  const isPicked = (value: string) =>
    picked !== undefined && picked.length === 1 && picked[0] === value;
  const press = (value: string) => {
    if (pressed.current === value && isPicked(value)) {
      pressed.current = undefined;
      narrowTo(undefined);
      return;
    }
    pressed.current = value;
    narrowTo([value]);
  };
  return (
    <section
      aria-label="Listed issues by status"
      className="flex min-h-[4.25rem] flex-col justify-center gap-1 md:min-h-12"
      data-view-bar="status"
    >
      <span aria-hidden className={`flex h-3 overflow-hidden rounded-sm ${surfaceMutedBg}`}>
        {segments.map((segment) => (
          <span
            className={`${segment.fill} min-w-px`}
            data-bar-segment={segment.value}
            key={segment.value}
            style={{ flexGrow: segment.count, flexBasis: 0 }}
          />
        ))}
      </span>
      <div className="flex min-w-0 flex-wrap gap-x-0.5">
        {segments.map((segment) => (
          <button
            aria-pressed={isPicked(segment.value)}
            className={`inline-flex min-h-9 shrink-0 items-center gap-1 rounded-md px-1 text-xs whitespace-nowrap md:min-h-7 ${
              isPicked(segment.value)
                ? tableFilterActive
                : `${textSecondaryOnCanvas} ${surfaceMutedHoverBg}`
            }`}
            data-bar-legend={segment.value}
            key={segment.value}
            onClick={() => press(segment.value)}
            type="button"
          >
            <span aria-hidden className={`size-2.5 rounded-sm ${segment.fill}`} />
            {segment.label}
            <span className="tabular-nums">{segment.count}</span>
          </button>
        ))}
      </div>
    </section>
  );
}
