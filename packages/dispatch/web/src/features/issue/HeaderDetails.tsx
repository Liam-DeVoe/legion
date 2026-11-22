import { type ReactNode, useId } from "react";

import {
  borderDefault,
  secondaryButtonBorder,
  secondaryButtonHoverBorder,
  secondaryButtonText,
  textMutedOnSurface,
} from "../../theme/classes";
import { useUserPreference } from "../shell/userPreference";

/** One property of the issue in Details: its name and its value. */
export interface HeaderDetailsRow {
  /** Names the row for a test (`data-testid`), and keys it. */
  readonly id: string;
  readonly label: string;
  readonly value: ReactNode;
}

// Module-level, so the preference's write keeps one identity across renders: `useUserPreference`
// lists `write` among its callback's dependencies.
const readOpen = (stored: string | null) => stored === "open";
const writeOpen = (open: boolean) => (open ? "open" : "closed");

/**
 * The end of today's issue header: the row that holds `leading` (the header's labels), then
 * `trailing` (its actions) and the one disclosure, Details, at the row's right end, then the
 * panel. Open, the panel lists every property of the issue a reader does not act on in the header
 * itself, as a definition list of a name and a value per row (`rows`), and then `after`, the
 * panels a row's own control opens (who is subscribed, what references the issue).
 *
 * The groups of the row, in this order: Labels (`leading`), Actions (`trailing`) and Details.
 * When the row is too narrow, Actions and Details move to the next line together and whole, and
 * stay at its right end.
 *
 * The panel lays its rows out by its own width, not the viewport's: one row per line, and two per
 * line from 56 rem (about 900 px), since the header's width at one viewport depends on the
 * sidebar and the margin beside it. In a row the value sits beside its name while there is room
 * for 14 rem of it, and under it otherwise, and a value wraps inside its own cell.
 *
 * Whether Details is open is the reader's: closed until they open it, and then kept open for them
 * in this browser (`issue.details`), on every issue, until they close it.
 */
export function HeaderDetails({
  after,
  leading,
  rows,
  trailing,
}: {
  after?: ReactNode;
  leading: ReactNode;
  rows: readonly HeaderDetailsRow[];
  trailing?: ReactNode;
}): ReactNode {
  const [open, setOpen] = useUserPreference("issue.details", readOpen, writeOpen);
  const panelId = useId();
  return (
    <>
      <div className="flex min-w-0 flex-wrap items-end justify-between gap-x-4 gap-y-2">
        {leading}
        <div className="ml-auto flex shrink-0 items-center gap-2">
          {trailing}
          <button
            aria-controls={panelId}
            aria-expanded={open}
            className={`min-h-11 shrink-0 rounded-lg px-3 py-2 text-sm font-medium md:min-h-8 md:py-1 ${secondaryButtonBorder} ${secondaryButtonText} ${secondaryButtonHoverBorder}`}
            onClick={() => setOpen(!open)}
            type="button"
          >
            Details
          </button>
        </div>
      </div>
      {open ? (
        <div
          className={`@container mt-3 min-w-0 border-t pt-3 ${borderDefault}`}
          data-testid="issue-details"
          id={panelId}
        >
          <dl className="grid min-w-0 grid-cols-1 gap-x-6 gap-y-2 @min-[56rem]:grid-cols-2">
            {rows.map((row) => (
              <div
                className="flex min-w-0 flex-wrap items-baseline gap-x-3 gap-y-1"
                data-testid={`issue-details-${row.id}`}
                key={row.id}
              >
                <dt className={`w-32 shrink-0 text-sm font-medium ${textMutedOnSurface}`}>
                  {row.label}
                </dt>
                <dd className="min-w-0 flex-[1_1_14rem] text-sm [overflow-wrap:anywhere]">
                  {row.value}
                </dd>
              </div>
            ))}
          </dl>
          {after}
        </div>
      ) : null}
    </>
  );
}
