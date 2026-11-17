import type { Table } from "@tanstack/react-table";
import { type ReactNode, type RefObject, useState } from "react";
import { Chip } from "../../components/Chip";
import { ColumnsMenu } from "../../components/ColumnsMenu";
import { ControlRow } from "../../components/ControlRow";
import { DisplayMenu } from "../../components/DisplayMenu";
import {
  columnFilterChip,
  columnFilterPicks,
  type FilterChip,
  filterChips,
} from "../../components/filter-chips";
import { PanelTrigger } from "../../components/PanelTrigger";
import { SearchBox } from "../../components/SearchBox";
import { SelectionBar } from "../../components/SelectionBar";
import { StepButton } from "../../components/StepButton";
import { useAddressUpdate } from "../../lib/page-address";
import { borderDefault, issueStatusScale, surfaceMutedBg } from "../../theme/classes";
import { issueStatuses, statusLabel } from "./board-model";
import { IssueFlagChips, UnclaimedUnchecked } from "./IssueFlags";
import { useIssueFilters } from "./issue-filters";
import {
  COLUMNS,
  defaultView,
  type Item,
  isDefaultStatus,
  readUrl,
  UNCLAIMED_FACET,
  writeUrl,
} from "./issues-table-model";

/**
 * A priority page's status strip: every status of the rows the table holds, each as wide as its
 * count, a press showing only that status and a second press every status. It draws no other
 * count (R13).
 */
export function IssueTableStatus({
  table,
  items,
}: {
  table: Table<Item>;
  items: readonly Item[];
}): ReactNode {
  const statusColumn = table.getColumn("status");
  const statusPicks = (statusColumn?.getFilterValue() as string[] | undefined) ?? [];
  const statusCounts = new Map<string, number>();
  for (const item of items)
    statusCounts.set(item.issue.status, (statusCounts.get(item.issue.status) ?? 0) + 1);
  return (
    <div className={`rounded-xl border p-3 ${borderDefault}`}>
      <fieldset
        aria-label="Issues by status"
        className={`flex min-h-11 min-w-0 overflow-x-auto rounded-md border-0 p-0 md:min-h-7 ${surfaceMutedBg}`}
      >
        {issueStatuses.map((status) => {
          const count = statusCounts.get(status) ?? 0;
          if (count === 0) return null;
          const on = statusPicks.length === 0 || statusPicks.includes(status);
          return (
            <button
              className={`min-h-11 shrink-0 px-3 py-2 text-left text-xs font-medium whitespace-nowrap md:min-h-7 md:py-1 ${issueStatusScale[status]} ${on ? "" : "opacity-30"}`}
              key={status}
              onClick={() =>
                statusColumn?.setFilterValue(
                  statusPicks.length === 1 && statusPicks[0] === status ? undefined : [status]
                )
              }
              title={`${statusLabel(status)}: ${count}`}
              // Each status takes width in proportion to its count, so the bar spans the box
              // and reads as shares; a phone keeps 44 px targets and scrolls sideways instead.
              style={{ flexGrow: count }}
              type="button"
            >
              {statusLabel(status)} {count}
            </button>
          );
        })}
      </fieldset>
    </div>
  );
}

/** Which of the row's own panels stands open, so the List keeps one at a time rather than a
 *  row of them: the filter panel behind the trigger, or the display menu at the right end. */
type ToolbarPanel = "flags" | "view";

/**
 * The one row the List's controls sit in: the search box, the trigger over the three flags that
 * narrow no column of the table, the chips of every narrowing that is on - the issues a link
 * names (`only=`), the flags, and every column's filter - **Clear filters** (which the row
 * draws), the page's own controls (the columns menu where the page gives none), and the display
 * menu carrying the grouping (INFRA-3135, R1 and R3). A column's filter is set on that column's
 * header, where `ColumnFilter` draws it, and its chip is drawn here as well, so a column the
 * reader hid keeps its narrowing in sight.
 */
export function IssueTableToolbar({
  clear,
  controls,
  displayLabel = "View",
  ownStatuses,
  searchRef,
  selection,
  stateKey,
  table,
  unclaimedUnchecked,
}: {
  /** The Issues tab's **Clear filters**: shown while `on`, the view's own narrowings back.
   *  Unset, the row clears by the table's own rule: back to the open statuses, with no search
   *  and no flag. */
  clear?: { readonly on: boolean; readonly run: () => void };
  /** The page's own controls, in the place of the columns menu the row draws by itself. */
  controls?: ReactNode;
  /** The display menu's name: Group by on the Issues tab. */
  displayLabel?: string;
  /** The statuses the view lists by itself, which draw no chip and which taking the Status
   *  chip off goes back to: the open statuses where unset. */
  ownStatuses?: readonly string[];
  /** The search box, which the selection bar hands the focus to when no row is left for it. */
  searchRef: RefObject<HTMLInputElement | null>;
  /** The chip of the issues a link names, which the row draws first among its chips. */
  selection?: FilterChip;
  /** The prefix this table's view is written under (`useTableView`), which **Clear filters**
   *  rewrites; unset is the bare one the project page publishes. */
  stateKey?: string;
  table: Table<Item>;
  /** Why the Claimed by filter cannot tell which claims have lapsed, while it is asked to. */
  unclaimedUnchecked: "refilling" | "unreachable" | undefined;
}): ReactNode {
  const flags = useIssueFilters();
  const update = useAddressUpdate();
  const [openPanel, setOpenPanel] = useState<ToolbarPanel | undefined>(undefined);
  const { globalFilter: search, columnFilters: filters, grouping } = table.getState();
  const setSearch = table.setGlobalFilter;
  const setGrouping = table.setGrouping;
  const setExpanded = table.setExpanded;
  // Unclaimed is the Claimed by column's "Nobody or lapsed", which this table writes as
  // `?unclaimed=1` under its own prefix (`issues-table-model.ts`) - the parameter the Board's
  // control row writes too - so the flag is read from and written to that column, and a table drawn
  // under a prefix narrows itself rather than the page's bare address.
  const claim = table.getColumn("claim");
  const claimPicks = (claim?.getFilterValue() as string[] | undefined) ?? [];
  const unclaimed = claimPicks.includes(UNCLAIMED_FACET);
  const setUnclaimed = (on: boolean) => {
    const rest = claimPicks.filter((value) => value !== UNCLAIMED_FACET);
    const next = on ? [...rest, UNCLAIMED_FACET] : rest;
    claim?.setFilterValue(next.length === 0 ? undefined : next);
  };
  // The three narrowings that are no column of this table, and so the ones the row's own
  // trigger holds.
  const flagged = [
    { label: "Needs you", on: flags.needsYou, remove: () => flags.setNeedsYou(false) },
    { label: "Unread", on: flags.unread, remove: () => flags.setUnread(false) },
    { label: "Unclaimed", on: unclaimed, remove: () => setUnclaimed(false) },
  ].filter(({ on }) => on);
  // Every column filter that is on, a chip of the row whether its column is shown or hidden:
  // a column the reader hid with the columns menu keeps its filter, and its chip is then the one
  // place it shows. Three columns are the List's own. Status opens on the open statuses, which a
  // link leaves out and Clear filters goes back to, so that pick is no chip, and taking a Status
  // chip off goes back to it rather than asking for every closed issue in the project. Claimed
  // by's "Nobody or lapsed" is the Unclaimed flag's chip already, so the column's chip names its
  // other picks, and taking it off leaves that one on. Labels is the project's own `?label=`,
  // which the Board reads too (`useIssueFilters`), so one of its values is one chip under the
  // name both views give it, `Label: <value>`, rather than a second chip in other words for the
  // narrowing the Board already names.
  const columned = table.getAllLeafColumns().flatMap((column) => {
    const picks = columnFilterPicks(column.getFilterValue());
    if (column.id === "status") {
      const own = ownStatuses === undefined ? defaultView.filters[0]?.value : [...ownStatuses];
      const isOwn =
        ownStatuses === undefined
          ? isDefaultStatus(picks)
          : picks.length === ownStatuses.length &&
            ownStatuses.every((status) => picks.includes(status));
      return picks.length === 0 || isOwn
        ? []
        : [columnFilterChip(column, picks, () => column.setFilterValue(own))];
    }
    if (column.id === "claim") {
      const named = picks.filter((value) => value !== UNCLAIMED_FACET);
      return named.length === 0
        ? []
        : [
            columnFilterChip(column, named, () =>
              column.setFilterValue(unclaimed ? [UNCLAIMED_FACET] : undefined)
            ),
          ];
    }
    if (column.id === "labels") {
      return picks.map((value) => ({
        key: `label:${value}`,
        label: `Label: ${value}`,
        remove: () => {
          const kept = picks.filter((one) => one !== value);
          column.setFilterValue(kept.length === 0 ? undefined : kept);
        },
      }));
    }
    return picks.length === 0 ? [] : [columnFilterChip(column, picks)];
  });
  // Exactly the scope it has always had: the table's own filters and search, and the two flags
  // the write below takes out of the address.
  const narrowed = filters.length > 0 || search !== "" || flags.needsYou || flags.unread;
  const chips = filterChips(
    [...(selection === undefined ? [] : [selection]), ...flagged, ...columned],
    "compact"
  );
  const clearFilters = () =>
    update((current) => {
      const next = writeUrl(
        current,
        {
          ...readUrl(current, stateKey),
          // Back to the view the table opens with, not to no filter at all: an empty status pick
          // asks the server for every closed issue in the project.
          filters: defaultView.filters,
          search: "",
          page: 0,
        },
        stateKey
      );
      for (const flag of ["needs-you", "unread"]) next.delete(flag);
      return next;
    });
  return (
    <>
      <ControlRow
        chips={chips}
        display={
          <DisplayMenu
            label={displayLabel}
            onOpenChange={(open) => setOpenPanel(open ? "view" : undefined)}
            open={openPanel === "view"}
            sections={[
              {
                id: "group",
                label: "Group by",
                onChange: (next) => {
                  setGrouping(next ? [next] : []);
                  setExpanded(true);
                },
                options: [
                  { label: "Nothing", value: "" },
                  // In the registry's order, whatever order the columns are drawn in.
                  ...table
                    .getAllLeafColumns()
                    .filter((column) => column.getCanGroup())
                    .map((column) => ({
                      label: String(column.columnDef.header),
                      place: COLUMNS.findIndex(({ id }) => id === column.id),
                      value: column.id,
                    }))
                    .sort((a, b) => a.place - b.place)
                    .map(({ label, value }) => ({ label, value })),
                ],
                value: grouping[0] ?? "",
              },
            ]}
            size="compact"
          />
        }
        onClear={
          clear === undefined
            ? narrowed
              ? clearFilters
              : undefined
            : clear.on
              ? clear.run
              : undefined
        }
        search={
          <SearchBox
            className="w-full"
            label="Search issues"
            onChange={(value) => setSearch(value)}
            placeholder="Search issues: key, title, assignee, author, labels"
            ref={searchRef}
            size="compact"
            value={search}
          />
        }
        size="compact"
        trigger={
          <PanelTrigger
            count={flagged.length + columned.length}
            label="Filters"
            onOpenChange={(open) => setOpenPanel(open ? "flags" : undefined)}
            open={openPanel === "flags"}
            size="compact"
            stacked
          >
            {() => (
              <>
                <IssueFlagChips />
                <Chip onClick={() => setUnclaimed(!unclaimed)} selected={unclaimed}>
                  Unclaimed
                </Chip>
              </>
            )}
          </PanelTrigger>
        }
      >
        {controls ?? <ColumnsMenu columns={table.getAllLeafColumns()} size="compact" />}
      </ControlRow>
      <UnclaimedUnchecked reason={unclaimedUnchecked} />
    </>
  );
}

export function IssueTableSelection({
  returnFocus,
  table,
  busy,
  startNext,
}: {
  /** Where the focus goes when the bar goes with it (`SelectionBar`), which the List names. */
  returnFocus: () => HTMLElement | null;
  table: Table<Item>;
  busy: boolean;
  startNext: (keys: readonly string[], confirmFirst?: boolean) => void;
}): ReactNode {
  const selectedKeys = table.getSelectedRowModel().flatRows.map((row) => row.original.issue.key);
  const leafCount = table.getFilteredRowModel().rows.length;
  const setSelection = table.setRowSelection;
  return (
    // Over the list, not in it: selecting a row moves no row (INFRA-3135, R4).
    <SelectionBar count={selectedKeys.length} label="Selected issues" returnFocus={returnFocus}>
      {selectedKeys.length < leafCount ? (
        <StepButton ariaDisabled={busy} onClick={() => table.toggleAllRowsSelected(true)}>
          Select all {leafCount} matching
        </StepButton>
      ) : null}
      {/* A selection the reader cannot see all of — "Select all N matching" — is asked
          about first: Start next writes once per issue. */}
      <StepButton
        ariaDisabled={busy}
        onClick={() =>
          startNext(selectedKeys, selectedKeys.length > table.getRowModel().rows.length)
        }
      >
        Start next
      </StepButton>
      <StepButton ariaDisabled={busy} onClick={() => setSelection({})}>
        Clear selection
      </StepButton>
    </SelectionBar>
  );
}
