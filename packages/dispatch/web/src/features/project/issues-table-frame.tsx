import { type ReactNode, useRef } from "react";
import { DataTable, type RowNoun } from "../../components/DataTable";
import type { FilterChip } from "../../components/filter-chips";
import { useFocusPlace } from "../../components/focus-place";
import { LoadingSkeleton } from "../../components/LoadingSkeleton";
import type { PagerOptions } from "../../components/Pager";
import { TABLE_FRAME_MARK } from "../../components/table-keys";
import {
  borderDefault,
  dangerText,
  focusVisibleRing,
  textMutedOnCanvas,
  textSecondaryOnCanvas,
} from "../../theme/classes";
import { ConfirmDialog } from "./ConfirmDialog";
import type { IssueTableActions } from "./issues-table-actions";
import type { OwnNarrowings } from "./issues-table-chips";
import { ALL_ROWS, PAGE_SIZES } from "./issues-table-model";
import { IssueTableSelection, IssueTableToolbar } from "./issues-table-page";
import { GroupRow, IssueRow } from "./issues-table-rows";
import type { IssueTableState } from "./issues-table-state";

/** The width of the column of each row's Start next, after the table's own columns. */
export const ACTION_WIDTH = 104;

/** The checkbox a row draws for its selection (`issues-table-columns.tsx`'s select column), the
 *  one checkbox every row has. */
const ROW_CHECKBOX = 'input[type="checkbox"]';

/** Status offers Done before the closed issues it would count are read. */
const DONE_UNLOADED: ReadonlyMap<string, readonly string[]> = new Map([["status", ["done"]]]);

/** What the List's rows are, which its header filters count in and its pager counts: issues, in
 *  groups, over rows. */
const ISSUE_TABLE_NOUN: RowNoun = {
  group: { one: "group", many: "groups" },
  line: { one: "row", many: "rows" },
  many: "issues",
  one: "issue",
};

/** The line under the List: its page sizes, the one that means every row, and its test id. It
 *  counts no total: the Issues tab's count of the open view is on its tab, and the priority pages'
 *  tables draw none (R13). */
const ISSUE_TABLE_PAGER: PagerOptions = {
  allRows: ALL_ROWS,
  pageSizes: PAGE_SIZES,
  testId: "issues-table-pager",
  totals: false,
};

/** The headers that stack in a box under 640 px (`DataTable`'s `stackColumns`): Title's sort over
 *  its filter, and Time per phase's name over its Share and Hours switch and its filter, which
 *  side by side pass a phone's window. A table another page draws has no Time per phase. */
const STACKED_HEADERS: readonly string[] = ["title", "phase_hours"];

/** A read that failed and can be made again, as a notice names it. */
interface Refetchable {
  readonly refetch: () => Promise<unknown>;
}

/**
 * What the listing's reads say over the rows once the rows are in, in the table and on the Board:
 * a failed read of the listing with its Retry (`error`), in `errorText` where the caller names
 * the failure itself; and the closed issues still being read beside the open ones
 * (`closedPending`). `closed` says the view reads closed issues, which both words name.
 */
export function ListingNotices({
  closed,
  closedPending,
  error,
  errorText,
}: {
  closed: boolean;
  closedPending: boolean;
  error: Refetchable | undefined;
  errorText?: string;
}): ReactNode {
  return (
    <>
      {error ? (
        <p className={dangerText} role="alert">
          {errorText ?? `Could not load ${closed ? "closed " : ""}issues.`}{" "}
          <button
            type="button"
            aria-label={closed ? "Retry loading closed issues" : "Retry loading issues"}
            onClick={() => void error.refetch()}
          >
            Retry
          </button>
        </p>
      ) : null}
      {closedPending ? (
        <p aria-live="polite" className={textMutedOnCanvas}>
          Loading closed issues…
        </p>
      ) : null}
    </>
  );
}

/**
 * What stands in place of the rows while the reads they wait on are out (`loading`) or have
 * failed (`failure`, its words and its Retry). `children` is what else a page says there: the
 * Issues tab's failed read of the Legion facts a view needs.
 */
export function ReadStates({
  children,
  failure,
  loading,
}: {
  children?: ReactNode;
  failure: { readonly text: string; readonly retry: () => void } | undefined;
  loading: boolean;
}): ReactNode {
  return (
    <>
      {loading ? <LoadingSkeleton label="Loading issues" /> : null}
      {failure ? (
        <p className={dangerText} role="alert">
          {failure.text}{" "}
          <button type="button" onClick={failure.retry}>
            Retry loading issues
          </button>
        </p>
      ) : null}
      {children}
    </>
  );
}

/**
 * An issue table as every page draws it: `header` (the Issues tab's view tabs), the listing's
 * `notices`, the `strip` of its rows by status, the toolbar, what the table's bulk actions say,
 * `readStates` in place of the rows while they are out, and once they are in the rows, the
 * selection bar and the confirm dialog.
 *
 * The toolbar is drawn in every state, at the one place in the tree, as the Board draws its
 * `controls`: a filter press that sends the List off to read issues it has not loaded - Clear
 * filters back to the open issues, which a List opened on the closed ones has never read - puts
 * the skeleton in place of the table alone, and the control pressed, with the keyboard focus on
 * it, stays where it was. Everything else here needs the listing and waits for it (`loaded`).
 * The section is the table's frame: its toolbar, box and pager, which another table's page-wide
 * keys leave to it (`TABLE_FRAME_ATTRIBUTE`).
 */
export function IssueTableFrame({
  actions,
  clear,
  closedUnloaded,
  controls,
  header,
  legionQueue,
  loaded,
  name,
  narrowings,
  notices,
  own,
  readStates,
  state,
  stateKey,
  strip,
}: {
  actions: IssueTableActions;
  /** The Issues tab's **Clear filters** (`IssueTableToolbar`). */
  clear?: { readonly on: boolean; readonly run: () => void };
  /** The closed issues are not read yet, so the Status filter does not count Done. */
  closedUnloaded: boolean;
  /** The page's own controls in the toolbar, in the place of the columns menu. */
  controls?: ReactNode;
  header?: ReactNode;
  /** A group heading names the next issue in the Legion queue (`GroupRow`). */
  legionQueue: boolean;
  loaded: boolean;
  /** What this list is called, for a reader who cannot see where it sits. */
  name: string;
  /** The narrowings that are no column and no flag (`exclude_tree`), with their removes. */
  narrowings: readonly FilterChip[];
  notices: ReactNode;
  own: OwnNarrowings;
  readStates: ReactNode;
  state: IssueTableState;
  /** The prefix the table's view is written under (`useTableView`), unset on the Issues tab. */
  stateKey?: string;
  strip: ReactNode;
}): ReactNode {
  const { lastMarked, model, panel, table } = state;
  const { attachAbove, boxWidth } = model.layout;
  const grouping = table.getState().grouping[0] ?? "";
  const searchRef = useRef<HTMLInputElement>(null);
  // The bar gone with the focus (`SelectionBar`): the checkbox of the issue the reader marked
  // last, else the first row's, else the search box once no row is listed. A row the window has
  // not drawn is scrolled to and takes the focus as it lands (`focusRow`).
  const returnFocus = () => {
    const leaves = model.displayRows.flatMap(({ row }) => (row.getIsGrouped() ? [] : [row.id]));
    const last = lastMarked.current;
    const key = last !== null && leaves.includes(last) ? last : leaves[0];
    return key === undefined ? searchRef.current : model.focusRow(key, ROW_CHECKBOX);
  };
  // A control of the table's head - a column filter, or the list it opened in a portal of its
  // own - goes with the table when a press in it sends the List back to its skeleton, and the
  // focus it held goes to the search box rather than the page's body. A notice's button under
  // the toolbar goes with the notice it answers - the interrupted action's Finish and Dismiss -
  // and its focus goes to the control now at its place among the notices, else the one before
  // it, else the search box. Only the head and the notices are counted: the selection bar, the
  // rows and the dialogs each hand the focus on themselves. The handler sits on the List's
  // section, which stays in every branch.
  const sectionRef = useRef<HTMLElement>(null);
  const noticesRef = useRef<HTMLDivElement>(null);
  const noteFocus = useFocusPlace(
    (element) => {
      const section = sectionRef.current;
      if (section === null) return null;
      // A portal's element is counted only where a control of the head was before it.
      if (!section.contains(element)) return section.querySelector("thead");
      const notices = noticesRef.current;
      return notices?.contains(element) ? notices : element.closest("thead");
    },
    () => searchRef.current
  );
  const { busy, notice, moved, interrupted, interruptedDescription, confirming } = actions;
  const visibleColumns = table.getVisibleLeafColumns().length + 1;
  return (
    <section
      aria-label={name}
      className="space-y-3 sm:pb-24"
      {...TABLE_FRAME_MARK}
      onFocus={noteFocus}
      ref={sectionRef}
    >
      <div className="space-y-3" ref={attachAbove}>
        {header}
        {notices}
        {strip}

        <IssueTableToolbar
          clear={clear}
          controls={controls}
          narrowings={narrowings}
          own={own}
          searchRef={searchRef}
          selection={state.pickedChip}
          stateKey={stateKey}
          table={table}
          unclaimedUnchecked={panel.unclaimedUnchecked}
        />

        {loaded ? (
          <div className="space-y-3" ref={noticesRef}>
            <p
              aria-live="polite"
              role={notice ? (notice.kind === "error" ? "alert" : "status") : undefined}
              className={`min-h-5 text-sm ${notice?.kind === "error" ? dangerText : textSecondaryOnCanvas}`}
            >
              {notice?.text}
            </p>
            {interrupted ? (
              <section aria-label="Interrupted table action" className={textSecondaryOnCanvas}>
                <p>{interruptedDescription}</p>
                <button
                  type="button"
                  onClick={state.finish}
                  className={`min-h-11 rounded-lg border px-3 text-sm ${borderDefault} ${focusVisibleRing}`}
                >
                  Finish
                </button>{" "}
                <button
                  type="button"
                  onClick={actions.dismiss}
                  className={`min-h-11 rounded-lg border px-3 text-sm ${borderDefault} ${focusVisibleRing}`}
                >
                  Dismiss
                </button>
              </section>
            ) : null}
          </div>
        ) : null}
      </div>

      {readStates}
      {loaded ? (
        <>
          <DataTable
            empty="No issues match these filters."
            filterCounts={state.openFacets}
            filterUnloaded={closedUnloaded ? DONE_UNLOADED : undefined}
            // A reader at the top keeps the first row on screen too, on the Issues tab and on a
            // priority's page (R11).
            holdAtTop
            label={`${name} table`}
            model={model}
            noun={ISSUE_TABLE_NOUN}
            onOpenFilterChange={panel.setOpenFilter}
            openFilter={panel.openFilter}
            pager={ISSUE_TABLE_PAGER}
            renderGroup={({ entry, index, measure }) => (
              <GroupRow
                columns={visibleColumns}
                viewportWidth={boxWidth}
                continued={entry.continued}
                index={index}
                legionQueue={legionQueue}
                measure={measure}
                show={table.getColumn(grouping)?.columnDef.meta?.show}
                shown={entry.shown}
                row={entry.row}
              />
            )}
            renderRow={({ row, index, measure }) => (
              <IssueRow
                busy={busy}
                index={index}
                measure={measure}
                moved={moved}
                onStartNext={(key) => state.startNext([key])}
                row={row}
              />
            )}
            scrollTestId="issues-table-scroll"
            stackColumns={STACKED_HEADERS}
            trailingWidth={ACTION_WIDTH}
          />

          <IssueTableSelection
            returnFocus={returnFocus}
            table={table}
            busy={busy}
            startNext={state.startNext}
          />
          {confirming === undefined ? null : (
            <ConfirmDialog
              body={confirming.body}
              confirmLabel={confirming.confirmLabel}
              key={confirming.title}
              onCancel={actions.cancelConfirming}
              onConfirm={actions.confirmed}
              title={confirming.title}
            />
          )}
        </>
      ) : null}
    </section>
  );
}
