import { type ReactNode, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";

import {
  borderDefault,
  popoverPanel,
  surfaceMutedBg,
  textMutedOnSurface,
  textPrimaryOnSurface,
} from "../../theme/classes";
import { formatDue } from "./lib/format";

/** The widest a bar is drawn, in CSS pixels: past it the slot's leftover is air, so seven days
 *  in a wide card read as seven bars and not as seven blocks. */
const MAX_BAR_PX = 24;
/** The air between two neighbouring bars, in CSS pixels. */
const GAP_PX = 2;

/** Where a slot's bar sits along a chart whose `count` slots are each `slot` units of the
 *  viewBox wide and which is drawn `widthPx` wide: its width, and its offset inside the slot, in
 *  viewBox units. Unmeasured (a DOM with no layout), the bar leaves one unit each side. */
export function barGeometry(
  slot: number,
  count: number,
  widthPx: number
): { width: number; inset: number } {
  if (widthPx <= 0 || count === 0) return { width: slot - 2, inset: 1 };
  const slotPx = widthPx / count;
  const barPx = Math.max(1, Math.min(MAX_BAR_PX, slotPx - GAP_PX));
  const width = (slot * barPx) / slotPx;
  return { width, inset: (slot - width) / 2 };
}

/** A day as a chart's tooltip names it: "Sep 27"; anything that is not `YYYY-MM-DD` as it is. */
export function tooltipDay(day: string): string {
  return /^\d{4}-\d{2}-\d{2}$/.test(day) ? formatDue(day) : day;
}

/** The body of a chart tooltip: the day, a note beside it, and its values, labels muted and
 *  figures in the primary ink, aligned. */
export function BarTooltip({
  day,
  note,
  rows,
}: {
  day: string;
  note?: string;
  rows: readonly { label?: string; value: string }[];
}): ReactNode {
  return (
    <>
      <div className={textMutedOnSurface}>
        {tooltipDay(day)}
        {note === undefined ? "" : ` · ${note}`}
      </div>
      <dl className="mt-0.5 grid grid-cols-[auto_auto] gap-x-3 tabular-nums">
        {rows.map((row) =>
          row.label === undefined ? (
            <dd className={`col-span-2 font-medium ${textPrimaryOnSurface}`} key={row.value}>
              {row.value}
            </dd>
          ) : (
            <div className="contents" key={row.label}>
              <dt className={textMutedOnSurface}>{row.label}</dt>
              <dd className={`text-right font-medium ${textPrimaryOnSurface}`}>{row.value}</dd>
            </div>
          )
        )}
      </dl>
    </>
  );
}

/** The frame of a per-day bar chart: it measures the chart, so the bars can be capped in pixels,
 *  tints the slot under the pointer and shows that day's tooltip above it. The chart itself takes
 *  no pointer events, so its `<title>`s stay what a screen reader reads without a second, native
 *  tooltip over this one. `children` draws the chart for a measured width (0 before layout). */
export function BarSlots({
  count,
  className,
  tooltip,
  children,
}: {
  count: number;
  /** The frame's height and width, as the chart's own classes were. */
  className: string;
  tooltip: (index: number) => ReactNode;
  children: (widthPx: number) => ReactNode;
}): ReactNode {
  const ref = useRef<HTMLDivElement | null>(null);
  const [widthPx, setWidthPx] = useState(0);
  const [hover, setHover] = useState<{ index: number; x: number; top: number } | null>(null);

  useLayoutEffect(() => {
    const element = ref.current;
    if (element === null) return;
    const measure = () => setWidthPx(element.clientWidth);
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, []);

  const track = (clientX: number) => {
    const element = ref.current;
    if (element === null || count === 0) return;
    const rect = element.getBoundingClientRect();
    if (rect.width <= 0) return;
    const index = Math.min(
      count - 1,
      Math.max(0, Math.floor(((clientX - rect.left) / rect.width) * count))
    );
    setHover({ index, x: rect.left + ((index + 0.5) / count) * rect.width, top: rect.top });
  };

  return (
    <div
      className={`relative border-b ${borderDefault} ${className}`}
      onPointerCancel={() => setHover(null)}
      onPointerDown={(event) => track(event.clientX)}
      onPointerLeave={() => setHover(null)}
      onPointerMove={(event) => track(event.clientX)}
      ref={ref}
    >
      {hover === null ? null : (
        <div
          aria-hidden="true"
          className={`absolute inset-y-0 rounded-t-sm ${surfaceMutedBg}`}
          data-testid="bar-hover-band"
          style={{ left: `${(hover.index / count) * 100}%`, width: `${100 / count}%` }}
        />
      )}
      <div className="pointer-events-none relative h-full w-full">{children(widthPx)}</div>
      {hover === null
        ? null
        : createPortal(
            // Fixed and in the body: a table's sideways scroller, which holds the Trend column,
            // would clip a tooltip drawn inside it.
            <div
              className={`pointer-events-none fixed z-50 px-2 py-1.5 text-xs whitespace-nowrap ${popoverPanel}`}
              role="tooltip"
              style={{
                left: Math.min(Math.max(hover.x, 72), window.innerWidth - 72),
                top: hover.top - 6,
                transform: "translate(-50%, -100%)",
              }}
            >
              {tooltip(hover.index)}
            </div>,
            document.body
          )}
    </div>
  );
}
