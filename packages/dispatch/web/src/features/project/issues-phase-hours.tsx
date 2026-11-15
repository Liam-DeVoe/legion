import { createContext, type ReactNode, useContext, useId, useRef, useState } from "react";
import type { LegionBoardFacts } from "../../api/types";
import { AnchoredPanel } from "../../components/AnchoredPanel";
import {
  borderDefault,
  card,
  focusVisibleRing,
  legionPhaseFill,
  surfaceMutedBg,
  surfaceMutedStrongBg,
  textMutedOnCanvas,
  textSecondaryOnCanvas,
} from "../../theme/classes";
import { useUserPreference } from "../shell/userPreference";
import { phaseHours, type WorkPhase, workPhaseLabel } from "./issues-legion";

/** How Time per phase draws its bars: each phase's part of that row's own total (Share), or each
 *  bar against the longest total on the current page, with the total written beside it (Hours). */
export type PhaseHoursMode = "share" | "hours";

/** What every Time per phase cell of the page reads: the mode, and the longest total on the
 *  page, which Hours scales each bar against. Given once by the table rather than to each column
 *  definition, so a new page or a switch of mode draws no column anew. */
export const PhaseHoursContext = createContext<{
  readonly mode: PhaseHoursMode;
  readonly pageMax: number;
}>({ mode: "share", pageMax: 0 });

/** The reader's mode, kept in this browser for the signed-in login, so it survives a reload.
 *  Share until the reader picks Hours. */
export function usePhaseHoursMode(): [PhaseHoursMode, (next: PhaseHoursMode) => void] {
  return useUserPreference<PhaseHoursMode>(
    "project.phase-hours",
    (stored) => (stored === "hours" ? "hours" : "share"),
    (mode) => mode
  );
}

/** A phase's colour. */
const fillOf = (phase: WorkPhase) => legionPhaseFill[phase];

const hoursText = (hours: number) => `${hours < 10 ? hours.toFixed(1) : Math.round(hours)} h`;

const TOOLTIP_CLASS = `z-50 rounded-lg border p-2 text-xs font-normal shadow-lg ${card}`;

/** What the tooltip says: one line per phase, in the daemon's order with Other last, with its
 *  colour, its name, its hours and its share of the row's total. */
function PhaseHoursTooltip({
  id,
  phases,
  total,
}: {
  id: string;
  phases: readonly (readonly [WorkPhase, number])[];
  total: number;
}): ReactNode {
  return (
    <table className="border-separate border-spacing-x-2 border-spacing-y-0.5" id={id}>
      <tbody>
        {phases.map(([phase, hours]) => (
          <tr data-phase-hours-line={phase} key={phase}>
            <td>
              <span aria-hidden className={`block size-2.5 rounded-sm ${fillOf(phase)}`} />
            </td>
            <td className="whitespace-nowrap">{workPhaseLabel(phase)}</td>
            <td className="text-right tabular-nums whitespace-nowrap">{hoursText(hours)}</td>
            <td className={`text-right tabular-nums ${textMutedOnCanvas}`}>
              {Math.round((hours / total) * 100)}%
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** One row's Time per phase: a bar of its phases, each in its own colour. Hovering the cell, or
 *  focusing it from the keyboard, opens a tooltip naming each phase's hours and share. */
export function PhaseHoursCell({ facts }: { facts: LegionBoardFacts | undefined }): ReactNode {
  const { mode, pageMax } = useContext(PhaseHoursContext);
  const anchorRef = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const tooltipId = useId();
  const { phases, total } = phaseHours(facts);
  if (total === 0) return null;
  const width = mode === "share" || pageMax === 0 ? 100 : (total / pageMax) * 100;
  return (
    // In Hours the total is written first, at a fixed width so the bars still start together:
    // on a phone the column is wider than the room beside the pinned key, and a total at the
    // cell's right end would be off screen. The cell is a button so the keyboard reaches it and
    // a tap opens the tooltip where there is no hover.
    <button
      aria-describedby={open ? tooltipId : undefined}
      aria-label={`Time per phase, ${hoursText(total)}`}
      className={`flex w-full min-w-0 cursor-default items-center gap-2 rounded-sm outline-none focus-visible:ring-2 ${focusVisibleRing}`}
      data-phase-hours
      onBlur={() => setOpen(false)}
      onClick={() => setOpen(true)}
      onFocus={() => setOpen(true)}
      onMouseEnter={() => setOpen(true)}
      onMouseLeave={() => setOpen(false)}
      ref={anchorRef}
      type="button"
    >
      {mode === "hours" ? (
        <span className={`w-11 shrink-0 text-right text-xs tabular-nums ${textSecondaryOnCanvas}`}>
          {hoursText(total)}
        </span>
      ) : null}
      <span className="flex h-3 min-w-0 flex-1">
        <span
          className={`flex h-3 gap-px overflow-hidden rounded-sm ${surfaceMutedBg}`}
          data-phase-hours-bar
          style={{ width: `${width}%` }}
        >
          {/* Each segment's width is its share of the row's total, so the segments fill the
              bar whatever the total, under an hour included. */}
          {phases.map(([phase, hours]) => (
            <span
              className={fillOf(phase)}
              key={phase}
              style={{ flexBasis: 0, flexGrow: hours / total }}
            />
          ))}
        </span>
      </span>
      {open ? (
        <AnchoredPanel
          anchorRef={anchorRef}
          className={TOOLTIP_CLASS}
          onDismiss={() => setOpen(false)}
        >
          <PhaseHoursTooltip id={tooltipId} phases={phases} total={total} />
        </AnchoredPanel>
      ) : null}
    </button>
  );
}

const MODES: readonly { readonly value: PhaseHoursMode; readonly label: string }[] = [
  { value: "share", label: "Share" },
  { value: "hours", label: "Hours" },
];

/** The two-option control in the Time per phase header. */
export function PhaseHoursModeToggle({
  mode,
  onChange,
}: {
  mode: PhaseHoursMode;
  onChange: (mode: PhaseHoursMode) => void;
}): ReactNode {
  return (
    <fieldset
      aria-label="Time per phase as"
      className={`inline-flex shrink-0 rounded-md border p-0.5 ${borderDefault}`}
    >
      {MODES.map((option) => (
        <button
          aria-pressed={mode === option.value}
          className={`min-h-6 rounded px-1 text-[10px] font-medium ${
            mode === option.value
              ? `${surfaceMutedStrongBg} ${textSecondaryOnCanvas}`
              : textMutedOnCanvas
          }`}
          key={option.value}
          onClick={() => onChange(option.value)}
          type="button"
        >
          {option.label}
        </button>
      ))}
    </fieldset>
  );
}
