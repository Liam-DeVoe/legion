import { afterEach, expect, test } from "bun:test";
import type {
  ColumnDef,
  ColumnFiltersState,
  GroupingState,
  PaginationState,
  SortingState,
} from "@tanstack/react-table";
import { render, screen } from "@testing-library/react";
import { type ReactNode, useState } from "react";

import { MOUSE_CSS, SIZE_CSS, sizeUnder } from "../__tests__/control-size";
import { ColumnFilter } from "./ColumnFilter";
import type { ControlSize } from "./control-names";
import { type DataTableNames, type RowNoun, useDataTable } from "./DataTable";
import { type LayoutColumns, useTableLayout } from "./table-layout";

/** A row that is not an issue. */
interface Deploy {
  id: string;
  region: string;
  service: string;
}

/** A column of free text, whose funnel opens a text box (`PanelTrigger`), and a column of
 *  values, whose funnel opens a list of them (`MultiSelect`). */
const COLUMNS: ColumnDef<Deploy>[] = [
  {
    accessorFn: (row) => row.service,
    filterFn: "includesString",
    header: "Service",
    id: "service",
    meta: { text: true },
  },
  { accessorFn: (row) => row.region, filterFn: "arrIncludesSome", header: "Region", id: "region" },
];

const LAYOUT_COLUMNS: LayoutColumns = {
  ids: ["service", "region"],
  rowAttribute: "data-deploy-row",
};

const NAMES: DataTableNames = {
  groupAttribute: "data-deploy-group",
  keymap: {
    next: { id: "deploys-next", keys: "j", label: "Next deploy" },
    open: { id: "deploys-open", keys: "o", label: "Open deploy" },
    openOnEnter: { id: "deploys-open-enter", keys: "Enter", label: "Open the focused deploy" },
    previous: { id: "deploys-previous", keys: "k", label: "Previous deploy" },
    scope: "agents",
    select: { id: "deploys-select", keys: "x", label: "Select or deselect the focused deploy" },
  },
  linkAttribute: "data-deploy-link",
  rowAttribute: "data-deploy-row",
};

const NOUN: RowNoun = {
  group: { many: "groups", one: "group" },
  line: { many: "rows", one: "row" },
  many: "deploys",
  one: "deploy",
};

const DEPLOYS: Deploy[] = [
  { id: "d1", region: "eu", service: "billing" },
  { id: "d2", region: "us", service: "search" },
];

const COUNTS: ReadonlyMap<string, number> = new Map([
  ["eu", 1],
  ["us", 1],
]);

/** Each column's funnel as a table's header draws it, at `size` where one is given. */
function Funnels({ size }: { size?: ControlSize }): ReactNode {
  const [sorting, setSorting] = useState<SortingState>([]);
  const [grouping, setGrouping] = useState<GroupingState>([]);
  const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>([]);
  const [globalFilter, setGlobalFilter] = useState("");
  const [pagination, setPagination] = useState<PaginationState>({ pageIndex: 0, pageSize: 25 });
  const layout = useTableLayout("funnels", LAYOUT_COLUMNS);
  const state = { columnFilters, globalFilter, grouping, pagination, sorting };
  const { table } = useDataTable<Deploy>({
    columns: COLUMNS,
    getRowId: (row) => row.id,
    layout,
    names: NAMES,
    onColumnFiltersChange: setColumnFilters,
    onGlobalFilterChange: setGlobalFilter,
    onGroupingChange: setGrouping,
    onPaginationChange: setPagination,
    onSortingChange: setSorting,
    rows: DEPLOYS,
    state,
    viewKey: JSON.stringify(state),
  });
  return table
    .getAllLeafColumns()
    .map((column) => (
      <ColumnFilter
        column={column}
        counts={COUNTS}
        key={column.id}
        noun={NOUN}
        onOpenChange={() => undefined}
        open={false}
        size={size}
        unloaded={[]}
      />
    ));
}

afterEach(() => {
  localStorage.clear();
});

test("the funnel keeps the 44 px target at every width at the comfortable size, and gives it up from md only at the compact size", () => {
  const view = render(<Funnels />);
  // What each funnel draws under `css`: a window below `md` (`SIZE_CSS` alone), or from it.
  const drawn = (css: string) =>
    [
      screen.getByRole("button", { name: "Filter Service" }),
      screen.getByRole("button", { name: "Filter Region" }),
    ].map((funnel) => sizeUnder(funnel, css));
  try {
    const target = { minHeight: "44px", minWidth: "44px", padding: "4px 4px 4px 4px" };
    expect(drawn(SIZE_CSS)).toEqual([target, target]);
    expect(drawn(`${SIZE_CSS}${MOUSE_CSS}`)).toEqual([target, target]);
    view.rerender(<Funnels size="comfortable" />);
    expect(drawn(`${SIZE_CSS}${MOUSE_CSS}`)).toEqual([target, target]);
    view.rerender(<Funnels size="compact" />);
    expect(drawn(SIZE_CSS)).toEqual([target, target]);
    const mouse = { minHeight: "32px", minWidth: "32px", padding: "4px 4px 4px 4px" };
    expect(drawn(`${SIZE_CSS}${MOUSE_CSS}`)).toEqual([mouse, mouse]);
  } finally {
    view.unmount();
  }
});
