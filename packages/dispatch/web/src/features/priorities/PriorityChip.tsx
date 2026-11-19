import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { usePageWords, useTooltips } from "../../components/tooltips";
import {
  badgePrimary,
  borderStrong,
  borderTransparent,
  textMutedOnCanvas,
} from "../../theme/classes";
import { orderLabel } from "./priorities-model";
import { priorityPath } from "./priority-routes";

/**
 * One priority an issue advances, drawn as its place in the one order (`#3`): a compact chip that
 * links to the priority's own page. The whole title is in the chip's accessible name ("#3 Ship
 * it") and, where the surface has tooltips, in its `title`, so a row stays one line tall however
 * long the title is. A priority the issue inherits from an issue above takes the muted, dashed
 * style, and its name and tooltip say it is inherited.
 *
 * `place` and `title` are the priorities read's: a priority that read does not hold, one deleted
 * since, is named by its id.
 *
 * The chip is the link's inner box, so the link can grow to the 44 px target narrow windows give
 * every link (`styles.css`) while the chip keeps its size.
 */
export function PriorityChip({
  id,
  inherited,
  place,
  title,
}: {
  id: string;
  inherited: boolean;
  /** The priority's place in the order, from 1: what `#3` reads from. */
  place: number | undefined;
  title: string | undefined;
}): ReactNode {
  const tooltips = useTooltips();
  const page = usePageWords();
  const label = place === undefined ? id : orderLabel(place - 1);
  const named = title === undefined ? label : `${label} ${title}`;
  const whole = title ?? id;
  return (
    <Link
      aria-label={
        inherited ? `${named}, ${page?.CONTROL_LABELS.advances_inherited ?? "inherited"}` : named
      }
      className="group inline-flex shrink-0 items-center"
      title={tooltips ? (inherited ? `${whole}, inherited from an issue above` : whole) : undefined}
      to={priorityPath(id)}
    >
      <span
        className={`rounded-sm border px-1.5 py-0.5 text-xs font-semibold tabular-nums whitespace-nowrap group-hover:underline ${
          inherited
            ? `border-dashed ${borderStrong} ${textMutedOnCanvas}`
            : `${borderTransparent} ${badgePrimary.bg} ${badgePrimary.text}`
        }`}
      >
        {label}
      </span>
    </Link>
  );
}
