import { SEARCH_QUERY_MAX } from "@legion/contracts/dispatch-tools";
import type { Table } from "@tanstack/react-table";
import { type ReactNode, useId } from "react";
import { Input, Select } from "../../components/Field";
import {
  textSecondaryOnCanvas,
  toolbarControlHeight,
  toolbarSelectHeight,
} from "../../theme/classes";
import { ColumnsMenu } from "../project/issues-table-controls";

/** The search, the group-by and the column control every table on these pages carries, with
 *  whatever else its own page puts beside them, each at a toolbar's one height from 1280 px up
 *  (`toolbarControlHeight`, 34 px). A phone's box
 *  (`compact`) draws no column control: its one cell names every field whatever the reader
 *  shows (`PRIORITY_PHONE_HIDDEN`), so a choice of columns there would change nothing the
 *  reader sees. */
export function TableToolbar<Row>({
  children,
  compact,
  searchLabel,
  searchPlaceholder,
  table,
}: {
  children?: ReactNode;
  compact: boolean;
  searchLabel: string;
  searchPlaceholder: string;
  table: Table<Row>;
}): ReactNode {
  const { globalFilter: search, grouping } = table.getState();
  const groupId = useId();
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Input
        aria-label={searchLabel}
        className={`w-full flex-none sm:w-auto sm:min-w-64 sm:flex-1 ${toolbarControlHeight}`}
        maxLength={SEARCH_QUERY_MAX}
        onChange={(event) => table.setGlobalFilter(event.target.value)}
        placeholder={searchPlaceholder}
        type="search"
        value={search}
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
            table.setGrouping(event.target.value ? [event.target.value] : []);
            table.setExpanded(true);
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
      {compact ? null : <ColumnsMenu columns={table.getAllLeafColumns()} />}
      {children}
    </div>
  );
}
