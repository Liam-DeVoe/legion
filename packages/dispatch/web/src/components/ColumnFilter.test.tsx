import { afterEach, expect, test } from "bun:test";
import type { ColumnDef, ColumnFiltersState, FilterFn } from "@tanstack/react-table";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useMemo, useState } from "react";
import {
  DataTable,
  DataTableCells,
  DataTableGroupRow,
  type DataTableNames,
  type RowNoun,
  useDataTable,
} from "./DataTable";
import { fold } from "./MultiSelect";
import { type LayoutColumns, useTableLayout } from "./table-layout";

/** A row that is not an issue, with a status the column knows and labels its rows name. */
interface Ticket {
  id: string;
  status: string;
  labels: string[];
}

const STATUSES = ["todo", "done"];
const STATUS_WORDS: Readonly<Record<string, string>> = { todo: "Todo", done: "Done" };
const TICKETS: Ticket[] = [
  { id: "t1", status: "todo", labels: ["legion"] },
  { id: "t2", status: "done", labels: ["legion", "web"] },
  { id: "t3", status: "done", labels: [] },
];

const valuesOf = (ticket: Ticket, id: string) =>
  id === "status" ? [ticket.status] : ticket.labels;
/** A pick matches a value by its `fold`, as the filter ticks it. */
const picked: FilterFn<Ticket> = (row, id, value: string[]) =>
  value.some((pick) => valuesOf(row.original, id).some((entry) => fold(entry) === fold(pick)));

const COLUMNS: ColumnDef<Ticket>[] = [
  { accessorFn: (row) => row.id, enableColumnFilter: false, header: "Ticket", id: "ticket" },
  {
    accessorFn: (row) => row.status,
    filterFn: picked,
    header: "Status",
    id: "status",
    meta: {
      always: STATUSES,
      order: STATUSES,
      show: (value: string) => STATUS_WORDS[value] ?? value,
    },
  },
  {
    accessorFn: (row) => row.labels.join(", "),
    filterFn: picked,
    header: "Labels",
    id: "labels",
  },
];
const LAYOUT: LayoutColumns = {
  ids: ["ticket", "status", "labels"],
  rowAttribute: "data-ticket-row",
};
const NAMES: DataTableNames = {
  groupAttribute: "data-ticket-group",
  keymap: {
    next: { id: "tickets-next", keys: "j", label: "Next ticket" },
    open: { id: "tickets-open", keys: "o", label: "Open ticket" },
    openOnEnter: { id: "tickets-open-enter", keys: "Enter", label: "Open the focused ticket" },
    previous: { id: "tickets-previous", keys: "k", label: "Previous ticket" },
    scope: "agents",
    select: { id: "tickets-select", keys: "x", label: "Select or deselect the focused ticket" },
  },
  linkAttribute: "data-ticket-link",
  rowAttribute: "data-ticket-row",
};
const NOUN: RowNoun = {
  group: { one: "group", many: "groups" },
  line: { one: "row", many: "rows" },
  many: "tickets",
  one: "ticket",
};
/** The address parameter each filter is read from. */
const PARAMS: Readonly<Record<string, string>> = { status: "status", labels: "label" };

/**
 * A table that reads its picks from `address` as the address spells them, with the shared
 * header filter (`ColumnFilter`) on Status and Labels. The picks it holds now are written out
 * under it, so a test reads what a press left.
 */
function Tickets({ address }: { address: string }): ReactNode {
  const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>(() => {
    const params = new URLSearchParams(address);
    return Object.entries(PARAMS).flatMap(([id, param]) => {
      const value = params.getAll(param);
      return value.length === 0 ? [] : [{ id, value }];
    });
  });
  const [openFilter, setOpenFilter] = useState<string>();
  const layout = useTableLayout("tickets", LAYOUT, []);
  const model = useDataTable<Ticket>({
    columns: COLUMNS,
    getRowId: (row) => row.id,
    layout,
    names: NAMES,
    onColumnFiltersChange: setColumnFilters,
    onGlobalFilterChange: () => undefined,
    onGroupingChange: () => undefined,
    onPaginationChange: () => undefined,
    onSortingChange: () => undefined,
    rows: TICKETS,
    state: {
      columnFilters,
      globalFilter: "",
      grouping: [],
      pagination: { pageIndex: 0, pageSize: 25 },
      sorting: [],
    },
    viewKey: "tickets",
  });
  // What each value of the open filter's column would list.
  const counts = useMemo(() => {
    const counted = new Map<string, number>();
    if (openFilter !== undefined)
      for (const ticket of TICKETS)
        for (const value of valuesOf(ticket, openFilter))
          counted.set(value, (counted.get(value) ?? 0) + 1);
    return counted;
  }, [openFilter]);
  return (
    <>
      <DataTable
        empty="No tickets match."
        filterCounts={counts}
        label="Tickets"
        model={model}
        noun={NOUN}
        onOpenFilterChange={setOpenFilter}
        openFilter={openFilter}
        renderGroup={({ entry: { row }, index, measure }) => (
          <DataTableGroupRow
            columns={COLUMNS.length}
            expanded={row.getIsExpanded()}
            heading={String(row.groupingValue)}
            index={index}
            label={String(row.groupingValue)}
            measure={measure}
            onToggle={row.getToggleExpandedHandler()}
            width={300}
          />
        )}
        renderRow={({ row, index, measure }) => (
          <tr data-index={index} data-ticket-row={row.id} ref={measure} tabIndex={-1}>
            <DataTableCells row={row} />
          </tr>
        )}
        scrollTestId="tickets-scroll"
      />
      <output data-testid="picks">{JSON.stringify(columnFilters)}</output>
    </>
  );
}

const undraws: (() => void)[] = [];
afterEach(() => {
  for (const undraw of undraws.splice(0)) undraw();
  localStorage.clear();
});

// k18 Four (R14): a picked value outside a column's own options, in another case than an
// option's own, is that option: offered once, ticked, and one press unpicks it, which leaves the
// filter with no pick, as unpicking a filter's last value does.
// Each option is matched by its name in any case, so a second option of another case is found.
test.each([
  ["?status=Done", "Status", /^done,/i],
  ["?label=Legion", "Labels", /^legion,/i],
])("%s is offered once in its filter, ticked, and one press unpicks it", async (address, column, option) => {
  const user = userEvent.setup();
  const view = render(<Tickets address={address} />);
  undraws.push(() => view.unmount());
  await screen.findByRole("table", { name: "Tickets" });

  await user.click(screen.getByRole("button", { name: `Filter ${column}: 1 picked` }));
  const list = screen.getByRole("listbox", { name: `${column} options` });
  const offered = within(list).getAllByRole("option", { name: option });
  expect(offered).toHaveLength(1);
  expect(offered[0]).toHaveAttribute("aria-selected", "true");

  await user.click(offered[0] as HTMLElement);
  expect(within(list).getByRole("option", { name: option })).toHaveAttribute(
    "aria-selected",
    "false"
  );
  expect(screen.getByRole("button", { name: `Filter ${column}` })).toBeInTheDocument();
  expect(screen.getByTestId("picks").textContent).toBe("[]");
});
