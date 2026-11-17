import type { Table } from "@tanstack/react-table";
import type { ReactNode } from "react";
import { ColumnsMenu, GroupBySelect, TableSearch } from "../project/issues-table-controls";

/** The search, the group-by and the column control every table on these pages carries, with
 *  whatever else its own page puts beside them: the issues table's own three controls. */
export function TableToolbar<Row>({
  children,
  searchLabel,
  searchPlaceholder,
  table,
}: {
  children?: ReactNode;
  searchLabel: string;
  searchPlaceholder: string;
  table: Table<Row>;
}): ReactNode {
  return (
    <div className="flex flex-wrap items-center gap-2">
      <TableSearch label={searchLabel} placeholder={searchPlaceholder} table={table} />
      <GroupBySelect table={table} />
      <ColumnsMenu columns={table.getAllLeafColumns()} />
      {children}
    </div>
  );
}
