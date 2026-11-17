import type { Table } from "@tanstack/react-table";
import type { ReactNode, RefObject } from "react";
import { borderDefault, surfaceBg, textSecondaryOnCanvas } from "../theme/classes";
import { CONTROL } from "./control-names";
import type { RowNoun } from "./DataTable";
import { StepButton } from "./StepButton";

/** What a pager takes from the page it belongs to beyond what its rows are called, which a
 *  table hands on unread. */
export interface PagerOptions {
  /** The page sizes the reader may choose, in the order they are offered. */
  readonly pageSizes: readonly number[];
  /** The page size that means every row, which reads as `All rows` rather than a count per
   *  page. Any size at least this big is that choice. */
  readonly allRows?: number;
  /** Whether the line counts the rows: `3 of 40 issues in 4 groups`, `Rows 1–25 of 40` and
   *  `All 40 rows`. True where unset. A page that counts its rows elsewhere passes false, and
   *  the line keeps the range (`Rows 1–25`, `All rows`), the page, its steps and the page size. */
  readonly totals?: boolean;
  readonly testId: string;
}

export interface PagerProps<TData> extends PagerOptions {
  /** What the table's rows are called, and the words its groups and its lines are counted in:
   *  `3 of 40 issues in 4 groups`, `Rows 1–25 of 40`. The table's own `noun`. */
  readonly noun: RowNoun;
  readonly table: Table<TData>;
  /** Measured by `useTableLayout`, which gives the box the room left under this line. */
  readonly pagerRef?: RefObject<HTMLDivElement | null>;
}

/** A caller's noun where a line starts with it: `rows` gives `Rows 1–25 of 40` and `Rows per
 *  page`, so the caller names its rows once rather than once per capitalisation. */
const sentence = (word: string) => `${word.charAt(0).toLocaleUpperCase()}${word.slice(1)}`;

/**
 * The one line under a table: how much of the listing it is showing, which page of it, the
 * steps between pages and how many rows a page holds. Every word it counts in is its caller's,
 * so the same pager serves a list of issues and a list of anything else.
 */
export function Pager<TData>({
  allRows = Number.MAX_SAFE_INTEGER,
  noun,
  pageSizes,
  pagerRef,
  table,
  testId,
  totals = true,
}: PagerProps<TData>): ReactNode {
  const { pageIndex, pageSize } = table.getState().pagination;
  const { grouping } = table.getState();
  // The rows every page holds together: the table's own count, which is the server's where the
  // server answered one page of them (`useDataTable`'s `serverPage`).
  const rowsOnPages = table.getRowCount();
  const leafCount = table.getFilteredRowModel().rows.length;
  const groupCount = table.getGroupedRowModel().rows.length;
  const total = table.options.data.length;
  const { group, line } = noun;
  const range = `${sentence(line.many)} ${rowsOnPages === 0 ? 0 : pageIndex * pageSize + 1}–${Math.min((pageIndex + 1) * pageSize, rowsOnPages)}`;
  return (
    <div
      ref={pagerRef}
      data-control={CONTROL.pager}
      data-testid={testId}
      className={`flex shrink-0 flex-wrap items-center gap-3 text-sm ${textSecondaryOnCanvas}`}
    >
      {totals ? (
        <span>
          {leafCount} of {total} {total === 1 ? noun.one : noun.many}
          {grouping.length > 0
            ? ` in ${groupCount} ${groupCount === 1 ? group.one : group.many}`
            : ""}
        </span>
      ) : null}
      <span className="ml-auto">
        {pageSize >= allRows
          ? totals
            ? `All ${rowsOnPages} ${rowsOnPages === 1 ? line.one : line.many}`
            : `All ${line.many}`
          : totals
            ? `${range} of ${rowsOnPages}`
            : range}
      </span>
      <StepButton ariaDisabled={!table.getCanPreviousPage()} onClick={() => table.previousPage()}>
        ‹ Previous
      </StepButton>
      <span>
        Page {pageIndex + 1} of {Math.max(table.getPageCount(), 1)}
      </span>
      <StepButton ariaDisabled={!table.getCanNextPage()} onClick={() => table.nextPage()}>
        Next ›
      </StepButton>
      <select
        aria-label={`${sentence(line.many)} per page`}
        className={`rounded-lg border px-2 py-1 ${borderDefault} ${surfaceBg}`}
        onChange={(event) => table.setPageSize(Number(event.target.value))}
        value={pageSize}
      >
        {pageSizes.map((size) => (
          <option key={size} value={size}>
            {size >= allRows ? `All ${line.many}` : `${size} per page`}
          </option>
        ))}
      </select>
    </div>
  );
}
