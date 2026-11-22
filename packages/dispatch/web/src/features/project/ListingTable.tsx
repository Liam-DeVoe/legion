import type { ReactNode } from "react";
import type { FilterChip } from "../../components/filter-chips";
import { useTableLayout } from "../../components/table-layout";
import { useIssueTableActions } from "./issues-table-actions";
import { LISTING_OWN } from "./issues-table-chips";
import { issueTableLayout, listingColumns, type ShownColumnId } from "./issues-table-columns";
import {
  type AdvancesListing,
  answeredWhole,
  listingsOf,
  useAdvancesTableItems,
} from "./issues-table-data";
import { IssueTableFrame, ListingNotices, ReadStates } from "./issues-table-frame";
import { tableStorageKey } from "./issues-table-model";
import { IssueTableStatus } from "./issues-table-page";
import { picksOf, useIssueTable, useIssueTableReads } from "./issues-table-state";
import { useTableView } from "./issues-table-view";

/** The one layout key of every table scoped to a priority: the work advancing a priority is
 *  one table, drawn under an opened row of the priorities page and on the priority's own page,
 *  so the columns a reader drags there are dragged once rather than once per priority. */
const SCOPED_LAYOUT_KEY = "priority-work";

/** A listing table leaves out no tree: `exclude_tree` is a narrowing of the Issues tab alone. */
const NO_TREES: readonly string[] = [];
const NO_CHIPS: readonly FilterChip[] = [];

/**
 * The issue table a priority page draws over a listing of the issues that advance a priority,
 * which belong to no project (`AdvancesListing`), as the List was drawn before the views: today's
 * columns at today's widths, its view under its own prefix in the address (`stateKey`), its
 * status strip over it, and no view tabs, no Legion column and no Board. It reads its open
 * issues, and its whole listing once the reader lets closed issues through.
 */
export function ListingTable({
  label,
  layout: ownLayout,
  listing,
  moveFrom = "page",
  priorityTitles,
  stateKey,
}: {
  /** What this list is called, for a reader who cannot see where it sits; "Priority issues"
   *  where unset. */
  label?: string;
  /** The layout this table keeps, under its own key, where it is not every priority's one, and
   *  the columns it opens with shown: each fault list of the priorities page shows its listing
   *  columns and Updated, which a table opens with hidden (`issueTableLayout`). */
  layout?: { readonly key: string; readonly shown: readonly ShownColumnId[] };
  listing: AdvancesListing;
  /** Where its `j` and `k` walk its rows from (`RowKeymap.moveFrom`): anywhere on the page the
   *  focus is in no other table, for the page's one table or its main one (the default); or only
   *  from one of its own rows, for a table drawn beside the page's main one. */
  moveFrom?: "page" | "row";
  /** Each priority's title by its id. Given, the table has the Advances column, which names each
   *  priority a row advances by its title; unset, it has none (`listingColumns`). */
  priorityTitles?: ReadonlyMap<string, string>;
  /** The prefix this table's own view takes in the address: `<stateKey>.sort` and the rest. */
  stateKey: string;
}): ReactNode {
  const address = useTableView(stateKey);
  const { params, view } = address;
  // The listing columns this table has: the ones its listing gives a use (`listingColumns`).
  const columnsLayout = issueTableLayout(
    listingColumns(listing, priorityTitles !== undefined),
    ownLayout?.shown
  );
  // One stored layout for every scoped table: the work advancing a priority is one table drawn
  // in two places, so a reader who drags a column there drags it for the priorities page and the
  // priority's own page alike, and not once per priority.
  const layout = useTableLayout(
    tableStorageKey(ownLayout?.key ?? SCOPED_LAYOUT_KEY),
    columnsLayout,
    view.sorting
  );
  const { flags, panel, userState, viewer } = useIssueTableReads(view.filters);
  // A listing its open read answers whole reads no closed issue, even where the Status filter
  // picks Done; any other reads its whole listing once the Status filter lets Done through.
  const whole = answeredWhole(listing);
  const closed = !whole && listingsOf(picksOf(view.filters, "status")).done;
  const rows = useAdvancesTableItems(listing, { closed, registry: panel.registry, viewer });
  const { issues, listingError, wholeIn } = rows;
  const actions = useIssueTableActions(listing);
  const loading = issues.isPending || userState.isPending;
  const failed = !loading && ((issues.isError && issues.data === undefined) || userState.isError);
  const loaded = !loading && !failed;
  const closedIn = !closed || wholeIn;
  const state = useIssueTable({
    actions,
    address,
    allItems: rows.allItems,
    columnsLayout,
    counted: loaded && listingError === undefined && closedIn,
    excluded: NO_TREES,
    flags,
    layout,
    listingIn: issues.data !== undefined && closedIn,
    loaded,
    // A listing table draws today's cells at today's widths (`useIssueTableColumns`).
    mode: { kind: "embedded", priorityTitles },
    moveFrom,
    onBoard: false,
    panel,
    serverRowCount: undefined,
    userState: userState.data,
    viewKey: params.toString(),
  });
  return (
    <IssueTableFrame
      actions={actions}
      closedUnloaded={!whole && !wholeIn}
      legionQueue={false}
      loaded={loaded}
      name={label ?? "Priority issues"}
      narrowings={NO_CHIPS}
      notices={
        loaded ? (
          <ListingNotices
            closed={closed}
            closedPending={closed && rows.whole.isPending}
            error={listingError}
          />
        ) : null
      }
      own={LISTING_OWN}
      readStates={
        <ReadStates
          failure={
            failed
              ? {
                  text: "Could not load issues.",
                  retry: () => {
                    void issues.refetch();
                    void userState.refetch();
                  },
                }
              : undefined
          }
          loading={loading}
        />
      }
      state={state}
      stateKey={stateKey}
      // Its strip of every status the table holds, a press showing only that status and a second
      // press every status.
      strip={loaded ? <IssueTableStatus items={rows.allItems} table={state.table} /> : null}
    />
  );
}
