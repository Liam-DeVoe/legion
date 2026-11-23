import {
  type ColumnDef,
  type ColumnFiltersState,
  flexRender,
  type GroupingState,
  type PaginationState,
  type SortingState,
} from "@tanstack/react-table";
import { type ReactNode, useMemo, useState } from "react";

import { DataTable, type RowNoun, useDataTable } from "../../components/DataTable";
import { type LayoutColumns, useTableLayout } from "../project/issues-table-layout";
import { ALL_ROWS } from "../project/issues-table-model";

const NO_COUNTS: ReadonlyMap<string, number> = new Map();

/** The attribute each row of a table of the Evals tab carries its id in: never the issue tables'
 *  `data-issue-row`, since the tab lists no issues (spec R8). */
export const EVAL_ROW = "data-row";

/**
 * A table of the Evals tab, a chart's or a list's: the one table core (`components/DataTable.tsx`,
 * plan D5) for rows that are no issues. It draws every row, in a box as tall as its rows, on one
 * page with no pager, and binds none of the issue tables' row keys; each row carries `data-row`.
 * The reader sorts, filters and lays out its columns as in any table of the core.
 */
export function EvalTable<Row>({
  columns,
  empty,
  getRowId,
  label,
  layoutKey,
  noun,
  rows,
  scrollTestId,
}: {
  columns: ColumnDef<Row>[];
  /** What the table says with no row. */
  empty: string;
  getRowId: (row: Row) => string;
  /** The table's accessible name. */
  label: string;
  /** Where the reader's widths, order and visibility of its columns are kept. */
  layoutKey: string;
  noun: RowNoun;
  rows: Row[];
  scrollTestId: string;
}): ReactNode {
  const layoutColumns = useMemo(
    (): LayoutColumns => ({
      ids: columns.flatMap((column) => (column.id === undefined ? [] : [column.id])),
    }),
    [columns]
  );
  const [sorting, setSorting] = useState<SortingState>([]);
  const [grouping, setGrouping] = useState<GroupingState>([]);
  const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>([]);
  const [globalFilter, setGlobalFilter] = useState("");
  const [pagination, setPagination] = useState<PaginationState>({
    pageIndex: 0,
    pageSize: ALL_ROWS,
  });
  const [openFilter, setOpenFilter] = useState<string>();
  const layout = useTableLayout(layoutKey, layoutColumns);
  const state = { columnFilters, globalFilter, grouping, pagination, sorting };
  const table = useDataTable<Row>({
    columns,
    getRowId,
    layout,
    onColumnFiltersChange: setColumnFilters,
    onGlobalFilterChange: setGlobalFilter,
    onGroupingChange: setGrouping,
    onPaginationChange: setPagination,
    onSortingChange: setSorting,
    rowKeys: false,
    rows,
    state,
    viewKey: JSON.stringify(sorting),
    virtual: false,
  });
  return (
    <DataTable
      empty={empty}
      filterCounts={NO_COUNTS}
      label={label}
      model={table}
      noun={noun}
      onOpenFilterChange={setOpenFilter}
      openFilter={openFilter}
      pager={false}
      renderGroup={() => null}
      renderRow={({ index, measure, row }) => (
        <tr className="h-7" data-index={index} data-row={row.id} key={row.id} ref={measure}>
          {row.getVisibleCells().map((cell) => (
            <td className="px-2 text-right tabular-nums first:text-left" key={cell.id}>
              {flexRender(cell.column.columnDef.cell, cell.getContext())}
            </td>
          ))}
        </tr>
      )}
      rowAttribute={EVAL_ROW}
      scrollTestId={scrollTestId}
    />
  );
}
