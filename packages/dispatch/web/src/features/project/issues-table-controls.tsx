import { SEARCH_QUERY_MAX } from "@legion/contracts/dispatch-tools";
import type { Column, Table } from "@tanstack/react-table";
import {
  type ReactNode,
  type RefObject,
  useCallback,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { flushSync } from "react-dom";
import { useSearchParams } from "react-router-dom";
import type { IssueSummary } from "../../api/types";
import { AnchoredPanel } from "../../components/AnchoredPanel";
import { Button, buttonClassName } from "../../components/Button";
import type { RowNoun } from "../../components/DataTable";
import { Input, Select } from "../../components/Field";
import { MultiSelect } from "../../components/MultiSelect";
import { useFootInset } from "../../hooks/useFootInset";
import {
  borderDefault,
  checkboxAccent,
  issueStatusScale,
  popoverPanel,
  rowHoverBg,
  surfaceBg,
  surfaceMutedBg,
  surfaceMutedHoverBg,
  tableFilterActive,
  textMutedOnCanvas,
  textPrimaryOnCanvas,
  textSecondaryOnCanvas,
  toolbarControlHeight,
  toolbarSelectHeight,
} from "../../theme/classes";
import { useMediaQuery } from "../shell/useDialog";
import { issueStatuses, statusLabel } from "./board-model";
import { IssueFlagChips, UnclaimedUnchecked } from "./IssueFlags";
import { useIssueFilters } from "./issue-filters";
import {
  ALL_ROWS,
  defaultView,
  type Item,
  PAGE_SIZES,
  readUrl,
  runningIssueStatuses,
  writeUrl,
} from "./issues-table-model";

function FunnelIcon(): ReactNode {
  return (
    <svg aria-hidden="true" className="size-3.5" fill="currentColor" viewBox="0 0 16 16">
      <title>Filter</title>
      <path d="M1.5 2h13l-5 6.2V13l-3 1.5V8.2z" />
    </svg>
  );
}

const filterTrigger = (active: boolean) =>
  `inline-flex size-7 items-center justify-center rounded-md ${active ? tableFilterActive : `${textMutedOnCanvas} ${surfaceMutedHoverBg}`}`;

export function ColumnFilter<TData>({
  column,
  counts,
  noun,
  open,
  onOpenChange,
  unloaded,
}: {
  column: Column<TData, unknown>;
  counts: ReadonlyMap<string, number>;
  /** What one row of this table is, as `IssueTablePager` takes it: this menu says how many
   *  rows each value holds, and a table of priorities holds priorities. */
  noun: RowNoun;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The values whose rows are not loaded yet: each says `load` rather than count them as
   *  none, as Status does for Done before the closed issues are read. */
  unloaded: readonly string[];
}): ReactNode {
  const meta = column.columnDef.meta;
  const name = String(column.columnDef.header);
  if (meta?.text) {
    const value = (column.getFilterValue() as string | undefined) ?? "";
    return (
      <Popover
        align="right"
        label={<FunnelIcon />}
        labelClassName={filterTrigger(value !== "")}
        open={open}
        onOpenChange={onOpenChange}
        title={`Filter ${name}`}
      >
        {() => (
          <div className="p-1">
            <Input
              aria-label={`Filter ${name}`}
              autoFocus
              onChange={(event) => column.setFilterValue(event.target.value || undefined)}
              placeholder={`${name} contains…`}
              surface="recessed"
              value={value}
            />
          </div>
        )}
      </Popover>
    );
  }
  const picks = (column.getFilterValue() as string[] | undefined) ?? [];
  const order = meta?.order;
  const options = [...new Set([...counts.keys(), ...picks, ...(meta?.always ?? [])])].sort(
    (a, b) =>
      order === undefined
        ? (counts.get(b) ?? 0) - (counts.get(a) ?? 0)
        : order.indexOf(a) - order.indexOf(b)
  );
  const show = meta?.show ?? ((value: string) => value);
  return (
    <MultiSelect
      emptyMessage="No matching value."
      label={name}
      onChange={(next) => column.setFilterValue(next.length === 0 ? undefined : next)}
      onOpenChange={onOpenChange}
      open={open}
      // An option whose rows are not read yet says so rather than claim a count of zero.
      optionDetail={(value) => (unloaded.includes(value) ? "load" : String(counts.get(value) ?? 0))}
      optionDetailLabel={(value) =>
        unloaded.includes(value)
          ? "not loaded"
          : `${counts.get(value) ?? 0} ${(counts.get(value) ?? 0) === 1 ? noun.one : noun.many}`
      }
      optionLabel={show}
      options={options}
      searchLabel={`Search ${name}`}
      selected={picks}
      triggerAriaLabel={
        picks.length === 0 ? `Filter ${name}` : `Filter ${name}: ${picks.length} picked`
      }
      triggerClassName={filterTrigger(picks.length > 0)}
    >
      <FunnelIcon />
    </MultiSelect>
  );
}

/** How a popover's button is drawn where its host names no look of its own. */
const POPOVER_TRIGGER = buttonClassName({ size: "sm" });

/** A button and the panel it opens: the table's own popovers, on the shared `AnchoredPanel`. */
export function Popover({
  label,
  labelClassName,
  title,
  children,
  align = "left",
  open: controlledOpen,
  onOpenChange,
}: {
  label: ReactNode;
  labelClassName?: string;
  title?: string;
  children: (close: () => void) => ReactNode;
  align?: "left" | "right";
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}): ReactNode {
  const [localOpen, setLocalOpen] = useState(false);
  const open = controlledOpen ?? localOpen;
  const setOpen = useCallback(
    (value: boolean) => {
      setLocalOpen(value);
      onOpenChange?.(value);
    },
    [onOpenChange]
  );
  const trigger = useRef<HTMLButtonElement>(null);

  const close = () => {
    setOpen(false);
    trigger.current?.focus();
  };
  return (
    <>
      <button
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-label={title}
        className={labelClassName ?? POPOVER_TRIGGER}
        onClick={() => setOpen(!open)}
        ref={trigger}
        title={title}
        type="button"
      >
        {label}
      </button>
      {open ? (
        <AnchoredPanel
          align={align}
          anchorRef={trigger}
          className={`z-50 flex max-h-[min(20rem,calc(100dvh-16px))] w-64 max-w-[calc(100vw-16px)] flex-col overflow-y-auto p-1 text-sm font-normal ${popoverPanel}`}
          label={title ?? (typeof label === "string" ? label : "Table actions")}
          onDismiss={() => setOpen(false)}
          role="dialog"
        >
          {children(close)}
        </AnchoredPanel>
      ) : null}
    </>
  );
}

/** The column control of a table of any row type: this table's rows are issues, and the
 *  priorities pages hold priorities and fault rows (`priorities/priority-table-controls.tsx`).
 *  It stands in a toolbar, so from 1280 px up it is as tall as the controls beside it. */
export function ColumnsMenu<Row>({ columns }: { columns: Column<Row, unknown>[] }): ReactNode {
  return (
    <Popover
      align="right"
      label="Columns"
      labelClassName={`${buttonClassName()} ${toolbarControlHeight}`}
    >
      {() =>
        columns
          .filter((column) => column.getCanHide())
          .map((column) => (
            <label
              className={`flex items-center gap-2 rounded px-2 py-1 ${rowHoverBg}`}
              key={column.id}
            >
              <input
                checked={column.getIsVisible()}
                className={checkboxAccent}
                onChange={column.getToggleVisibilityHandler()}
                type="checkbox"
              />
              {String(column.columnDef.header)}
            </label>
          ))
      }
    </Popover>
  );
}

export function Action({
  children,
  disabled,
  onClick,
}: {
  children: ReactNode;
  disabled?: boolean;
  onClick: () => void;
}): ReactNode {
  return (
    <Button disabled={disabled} onClick={onClick} size="sm">
      {children}
    </Button>
  );
}

/** The type of a status segment, with its name or with its count alone. */
const SEGMENT_TYPE = "text-xs font-medium whitespace-nowrap";

/** What sets a status segment's width with its name, which the bar's hidden copy of its segments
 *  draws them with too (`IssueTableStatus`), so the copy is as wide as the segments it stands
 *  for. */
const SEGMENT = `px-2 ${SEGMENT_TYPE}`;

/** The padding and type of a segment that draws its count alone (`IssueTableStatus`'s
 *  `legend`): a named segment's. */
const COUNT_SEGMENT = SEGMENT;

/** How narrow a segment that draws its count alone may be: 24 px, WCAG 2.5.8's least target
 *  width, in place of the 44 px floor `styles.css` gives every button below 1280 px, so the eight
 *  segments of a priority's work fit its 316 px bar at 390 px and none scrolls past its edge. Its
 *  height keeps that floor. Set as an inline style, since the floor stands in no cascade layer
 *  and outranks every utility class; from `md` up each count with its padding is wider anyway. */
const COUNT_SEGMENT_MIN = 24;

export function IssueTableStatus({
  table,
  items,
  epics,
  legend = false,
  notice,
}: {
  table: Table<Item>;
  items: readonly Item[];
  epics: readonly IssueSummary[];
  /** Whether the bar draws each segment's count alone where the segments with their names are
   *  wider than it, and the names in a legend under it (`IssuesTable`'s `statusLegend`). Unset,
   *  each segment draws its name and its count, and the bar scrolls sideways where they are
   *  wider than it, as a project's does on a phone. */
  legend?: boolean;
  /** The line that says what the last table action did, which the strip places so that its
   *  arrival moves nothing on the page. */
  notice?: ReactNode;
}): ReactNode {
  const statusColumn = table.getColumn("status");
  const statusPicks = (statusColumn?.getFilterValue() as string[] | undefined) ?? [];
  const statusCounts = new Map<string, number>();
  let queuedCount = 0;
  let runningCount = 0;
  for (const item of items) {
    statusCounts.set(item.issue.status, (statusCounts.get(item.issue.status) ?? 0) + 1);
    if (item.queue !== undefined) queuedCount += 1;
    if ((runningIssueStatuses as readonly string[]).includes(item.issue.status)) runningCount += 1;
  }
  const shown = issueStatuses.filter((status) => (statusCounts.get(status) ?? 0) > 0);
  const barRef = useRef<HTMLFieldSetElement>(null);
  const namesRef = useRef<HTMLDivElement>(null);
  const [namesOverflow, setNamesOverflow] = useState(false);
  // Whether the segments with their names are wider than the bar, read off a copy of them that
  // is never drawn rather than off the bar itself: the copy is the same whichever way the bar
  // draws its segments, so what the switch does to the bar cannot switch it back. Read before
  // the first paint, and again in the frame the bar's width or the copy's (a count that
  // changed) does, before that frame is painted.
  useLayoutEffect(() => {
    const bar = barRef.current;
    const names = namesRef.current;
    if (!legend || bar === null || names === null) return;
    const measure = () =>
      setNamesOverflow(names.getBoundingClientRect().width > bar.getBoundingClientRect().width);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const sizes = new ResizeObserver(() => flushSync(measure));
    sizes.observe(bar);
    sizes.observe(names);
    return () => sizes.disconnect();
  }, [legend]);
  const countsOnly = legend && namesOverflow;
  return (
    <div className={`relative rounded-lg border p-3 ${borderDefault} ${surfaceBg}`}>
      <div
        className={`mb-2 flex flex-wrap items-baseline gap-x-4 gap-y-1 text-sm ${textSecondaryOnCanvas}`}
      >
        <span>
          <b className={`font-semibold tabular-nums ${textPrimaryOnCanvas}`}>{queuedCount}</b> in
          Legion's queue
        </span>
        <span>
          <b className={`font-semibold tabular-nums ${textPrimaryOnCanvas}`}>{runningCount}</b>{" "}
          running
        </span>
        <span>
          <b className={`font-semibold tabular-nums ${textPrimaryOnCanvas}`}>{epics.length}</b>{" "}
          {epics.length === 1 ? "epic" : "epics"}
        </span>
      </div>
      {/* Absolute from `sm` up, at the right of the counts; on a phone the line under them. It
          stands before the bar, so the bar's next sibling is its legend alone. */}
      {notice}
      {legend ? (
        // The copy the bar is measured against: the segments with their names, each at its
        // natural width, in a strip that takes no height and clips them, so nothing of it is
        // drawn and the page grows no wider. It stands before the bar, so nothing a reader
        // reads under the bar holds it.
        <div aria-hidden className="h-0 overflow-hidden">
          <div className="invisible flex w-max" ref={namesRef}>
            {shown.map((status) => (
              <span className={SEGMENT} key={status}>
                {statusLabel(status)} {statusCounts.get(status)}
              </span>
            ))}
          </div>
        </div>
      ) : null}
      <fieldset
        aria-label="Issues by status"
        className={`flex min-w-0 overflow-x-auto rounded border-0 p-0 ${surfaceMutedBg}`}
        ref={barRef}
      >
        {shown.map((status) => {
          const count = statusCounts.get(status) ?? 0;
          const on = statusPicks.length === 0 || statusPicks.includes(status);
          return (
            <button
              // A count alone is named by its status, as its segment is with the name drawn.
              aria-label={countsOnly ? `${statusLabel(status)} ${count}` : undefined}
              className={`h-6 shrink-0 text-left ${countsOnly ? COUNT_SEGMENT : SEGMENT} ${issueStatusScale[status]} ${on ? "" : "opacity-30"}`}
              key={status}
              onClick={() =>
                statusColumn?.setFilterValue(
                  statusPicks.length === 1 && statusPicks[0] === status ? undefined : [status]
                )
              }
              title={`${statusLabel(status)}: ${count}`}
              // Each status takes width in proportion to its count, so the bar spans the box
              // and reads as shares; a phone keeps 44 px targets and scrolls sideways instead,
              // unless the table draws the counts alone with their names in a legend (`legend`),
              // each segment as narrow as its count and 24 px (`COUNT_SEGMENT_MIN`).
              style={{ flexGrow: count, minWidth: countsOnly ? COUNT_SEGMENT_MIN : undefined }}
              type="button"
            >
              {countsOnly ? (
                count
              ) : (
                <>
                  {statusLabel(status)} {count}
                </>
              )}
            </button>
          );
        })}
      </fieldset>
      {countsOnly ? (
        // The names the segments no longer draw, in the bar's order, each beside a swatch of its
        // status's colour, wrapping onto as many lines as the box needs. Each segment keeps its
        // name as its accessible name and its tooltip, so the legend is for the eye alone.
        <ul
          aria-hidden
          className={`mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs ${textSecondaryOnCanvas}`}
        >
          {shown.map((status) => (
            <li className="flex items-center gap-1.5" key={status}>
              <span
                className={`size-3 shrink-0 rounded-sm border ${borderDefault} ${issueStatusScale[status]}`}
              />
              {statusLabel(status)}
            </li>
          ))}
        </ul>
      ) : null}
    </div>
  );
}

export function IssueTableToolbar({
  table,
  unclaimedUnchecked,
}: {
  table: Table<Item>;
  /** Why the Claimed by filter cannot tell which claims have lapsed, while it is asked to. */
  unclaimedUnchecked: "refilling" | "unreachable" | undefined;
}): ReactNode {
  const flags = useIssueFilters();
  const groupId = useId();
  const [, setParams] = useSearchParams();
  const { globalFilter: search, columnFilters: filters, grouping } = table.getState();
  const setSearch = table.setGlobalFilter;
  const setGrouping = table.setGrouping;
  const setExpanded = table.setExpanded;
  return (
    <>
      {/* The toolbar is one row where the search box, its whole placeholder (about 406 px) and
          the five controls after it (about 557 px with their gaps) fit; in a narrower box, as
          beside a priority's margin, the search box takes a row of its own and the controls the
          row under it, rather than cut the placeholder and leave the last control alone. Every
          control stands at a toolbar's one height from 1280 px up (`toolbarControlHeight`, 34 px);
          below 1280 px each takes the 44 px floor `styles.css` gives a control. */}
      <div className="@container flex flex-wrap items-center gap-2">
        <Input
          aria-label="Search issues"
          className={`w-full flex-none sm:w-auto sm:min-w-64 sm:flex-1 @max-[61rem]:basis-full ${toolbarControlHeight}`}
          onChange={(event) => {
            setSearch(event.target.value);
          }}
          placeholder="Search issues: key, title, epic, assignee, author, labels"
          type="search"
          value={search}
          maxLength={SEARCH_QUERY_MAX}
        />
        <label
          className={`flex items-center gap-2 text-sm ${textSecondaryOnCanvas}`}
          htmlFor={groupId}
        >
          Group by
          <Select
            className={`w-36 ${toolbarSelectHeight}`}
            id={groupId}
            onChange={(event) => {
              setGrouping(event.target.value ? [event.target.value] : []);
              setExpanded(true);
            }}
            value={grouping[0] ?? ""}
          >
            <option value="">Nothing</option>
            {table
              .getAllLeafColumns()
              .filter((column) => column.getCanGroup())
              .map((column) => (
                <option key={column.id} value={column.id}>
                  {String(column.columnDef.header)}
                </option>
              ))}
          </Select>
        </label>
        <ColumnsMenu columns={table.getAllLeafColumns()} />
        <IssueFlagChips inToolbar />
        {filters.length > 0 || search !== "" || flags.needsYou || flags.unread ? (
          <Button
            onClick={() =>
              setParams(
                (current) => {
                  const next = writeUrl(current, {
                    ...readUrl(current),
                    // Back to the view the table opens with, not to no filter at all: an empty
                    // status pick asks the server for every closed issue in the project.
                    filters: defaultView.filters,
                    search: "",
                    page: 0,
                  });
                  for (const flag of ["needs-you", "unread"]) next.delete(flag);
                  return next;
                },
                { replace: true }
              )
            }
            className={toolbarControlHeight}
            variant="ghost"
          >
            Clear filters
          </Button>
        ) : null}
      </div>
      <UnclaimedUnchecked reason={unclaimedUnchecked} />
    </>
  );
}

export function IssueTablePager<TData>({
  noun,
  table,
  pagerRef,
}: {
  /** What one row of this table is, singular and plural, for the count this pager prints: a
   *  table of priorities counts priorities, and a reader told "22 of 22 issues" under a list
   *  of priorities is told the wrong thing about what they are reading. */
  noun: RowNoun;
  table: Table<TData>;
  pagerRef: RefObject<HTMLDivElement | null>;
}): ReactNode {
  const { pageIndex, pageSize } = table.getState().pagination;
  const { grouping } = table.getState();
  const rowsOnPages = table.getPrePaginationRowModel().rows.length;
  const leafCount = table.getFilteredRowModel().rows.length;
  const groupCount = table.getGroupedRowModel().rows.length;
  const total = table.options.data.length;
  return (
    <div
      ref={pagerRef}
      data-testid="issues-table-pager"
      className={`flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 text-sm ${textSecondaryOnCanvas}`}
    >
      {/* The count takes the room the controls leave. On a phone it shares the first line with
          the page size, and Previous, the page and Next keep the second line together. */}
      <span className="min-w-max flex-1">
        {leafCount} of {total} {total === 1 ? noun.one : noun.many}
        {grouping.length > 0 ? ` in ${groupCount} ${groupCount === 1 ? "group" : "groups"}` : ""}
      </span>
      {/* One cluster from `sm`, so in a narrow box the three wrap to the next line together,
          at the right, and no control is left alone on a line of its own. */}
      <div className="ml-auto flex items-center gap-x-3 max-sm:contents">
        <span className={`max-sm:hidden ${textMutedOnCanvas}`}>
          {pageSize >= ALL_ROWS
            ? `All ${rowsOnPages} ${rowsOnPages === 1 ? "row" : "rows"}`
            : `Rows ${rowsOnPages === 0 ? 0 : pageIndex * pageSize + 1}–${Math.min((pageIndex + 1) * pageSize, rowsOnPages)} of ${rowsOnPages}`}
        </span>
        <div className="flex items-center gap-2 max-sm:order-last max-sm:basis-full max-sm:justify-between">
          <Action disabled={!table.getCanPreviousPage()} onClick={() => table.previousPage()}>
            ‹ Previous
          </Action>
          <span className="tabular-nums whitespace-nowrap">
            Page {pageIndex + 1} of {Math.max(table.getPageCount(), 1)}
          </span>
          <Action disabled={!table.getCanNextPage()} onClick={() => table.nextPage()}>
            Next ›
          </Action>
        </div>
        <Select
          aria-label="Rows per page"
          className="w-32"
          size="sm"
          onChange={(event) => table.setPageSize(Number(event.target.value))}
          value={pageSize}
        >
          {PAGE_SIZES.map((size) => (
            <option key={size} value={size}>
              {size >= ALL_ROWS ? "All rows" : `${size} per page`}
            </option>
          ))}
        </Select>
      </div>
    </div>
  );
}

/** Tailwind's `sm` itself: true exactly when the selection bar's `sm:` classes put it at the foot
 *  of the screen rather than under the header. In `rem`, for `MD_VIEWPORT_QUERY`'s reason
 *  (`features/shell/useDialog.ts`): Tailwind declares `sm` as `40rem`, so only a `rem` query
 *  switches with those classes at any default font size. */
const SM_VIEWPORT_QUERY = "(min-width: 40rem)";

export function IssueTableSelection({
  table,
  epics,
  busy,
  unansweredEpic,
  startNext,
  moveTo,
  newEpic,
}: {
  table: Table<Item>;
  epics: readonly IssueSummary[];
  busy: boolean;
  unansweredEpic: string | undefined;
  startNext: (keys: readonly string[], confirmFirst?: boolean) => void;
  moveTo: (keys: readonly string[], epic: string | null) => void;
  /** How an epic is made of the selected issues; absent on a table that stands on no project,
   *  since an epic is created in one and a cross-project selection names none. */
  newEpic?: (keys: readonly string[]) => void;
}): ReactNode {
  const selectedKeys = table.getSelectedRowModel().flatRows.map((row) => row.original.issue.key);
  const leafCount = table.getFilteredRowModel().rows.length;
  const setSelection = table.setRowSelection;
  /** The epic the reader has picked to move the selection into; no write has been sent yet. */
  const [epic, setEpic] = useState("");
  // From `sm` up the bar sits at the foot of the screen, in the foot's page layer
  // (`hooks/useFootInset.ts` lists the foot's layers and the elements in each), so the hook holds
  // its band, its box on the screen, and the floats keep clear of its controls. Below `sm` the
  // bar sits under the header, at the top of the screen and not at its foot, so it counts nothing
  // there.
  const barRef = useRef<HTMLDivElement>(null);
  const atFoot = useMediaQuery(SM_VIEWPORT_QUERY);
  useFootInset(barRef, selectedKeys.length > 0 && atFoot, "page");
  return (
    <>
      {/* Over the page, not in it: selecting a row moves nothing else. */}
      {selectedKeys.length === 0 ? null : (
        <div
          className={`fixed inset-x-2 top-14 z-30 mx-auto flex w-fit max-w-[calc(100vw-1rem)] flex-nowrap items-center gap-2 overflow-x-auto whitespace-nowrap px-3 py-2 text-sm sm:top-auto sm:bottom-4 [&>*]:shrink-0 ${popoverPanel} ${textPrimaryOnCanvas}`}
          ref={barRef}
          role="toolbar"
        >
          <span className="mr-1 font-semibold">{selectedKeys.length} selected</span>
          {selectedKeys.length < leafCount ? (
            <Action disabled={busy} onClick={() => table.toggleAllRowsSelected(true)}>
              Select all {leafCount} matching
            </Action>
          ) : null}
          {/* A selection the reader cannot see all of — "Select all N matching" — is asked
              about first: Start next writes once per issue. */}
          <Action
            disabled={busy}
            onClick={() =>
              startNext(selectedKeys, selectedKeys.length > table.getRowModel().rows.length)
            }
          >
            Start next
          </Action>
          {/* Two steps: the select stages an epic and Move commits it. A closed `<select>`
              commits on an arrow key in Chrome on Linux and Windows, and this one reparents
              every selected issue. */}
          <Select
            aria-label="Move to epic"
            className="w-40"
            size="sm"
            surface="recessed"
            disabled={busy}
            onChange={(event) => setEpic(event.target.value)}
            value={epic}
          >
            <option value="">Move to epic…</option>
            {epics.map((option) => (
              <option key={option.key} value={option.key}>
                {option.title}
              </option>
            ))}
          </Select>
          <Action
            disabled={busy || epic === ""}
            onClick={() => {
              const into = epic;
              setEpic("");
              void moveTo(selectedKeys, into);
            }}
          >
            Move
          </Action>
          <Action disabled={busy} onClick={() => void moveTo(selectedKeys, null)}>
            Take out of epic
          </Action>
          {newEpic === undefined ? null : (
            <Action
              disabled={busy || unansweredEpic !== undefined}
              onClick={() => void newEpic(selectedKeys)}
            >
              New epic from these
            </Action>
          )}
          <Action disabled={busy} onClick={() => setSelection({})}>
            Clear selection
          </Action>
        </div>
      )}
    </>
  );
}
