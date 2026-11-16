import { useQuery } from "@tanstack/react-query";
import type { Column, FilterFn, RowSelectionState, VisibilityState } from "@tanstack/react-table";
import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { apiErrorMessage } from "../../api/client";
import {
  legionBoardQuery,
  legionSettingsQuery,
  projectDonePageQuery,
  projectOpenCountQuery,
  userStateQuery,
  whoAmIQuery,
} from "../../api/queries";
import { ColumnFilter } from "../../components/ColumnFilter";
import { ColumnsMenu } from "../../components/ColumnsMenu";
import { DataTable, type DataTableNames, useDataTable } from "../../components/DataTable";
import { LoadingSkeleton } from "../../components/LoadingSkeleton";
import { Pager, type PagerOptions } from "../../components/Pager";
import { QueryError } from "../../components/QueryError";
import { useTableLayout } from "../../components/table-layout";
import { readStore, writeStore } from "../../components/table-storage";
import { copyAddress, useAddressParams, useAddressUpdate } from "../../lib/page-address";
import {
  borderDefault,
  dangerText,
  focusVisibleRing,
  textMutedOnCanvas,
  textSecondaryOnCanvas,
} from "../../theme/classes";
import { useAgentRegistry } from "../refs/agent-registry";
import { setSelectedIssues } from "../search/bar-scope";
import { useKeymap } from "../shell/keymap";
import { COMPACT_VIEWPORT_QUERY, useMediaQuery } from "../shell/useDialog";
import { useUserPreference } from "../shell/userPreference";
import { ConfirmDialog } from "./ConfirmDialog";
import { type BoardBy, IssueBoard } from "./IssueBoard";
import {
  type ActiveView,
  IssueViewTabs,
  LayoutSwitch,
  type SettingsFailure,
  ToggleButton,
  ViewControls,
  type ViewLayout,
} from "./IssueViewTabs";
import { useIssueFilters } from "./issue-filters";
import { phaseHours } from "./issues-legion";
import { PhaseHoursContext, PhaseHoursModeToggle, usePhaseHoursMode } from "./issues-phase-hours";
import { useIssueTableActions } from "./issues-table-actions";
import { ViewBar } from "./issues-table-bar";
import {
  issueTableLayout,
  type ListingColumnId,
  listingColumns,
  type ShownColumnId,
  useIssueTableColumns,
} from "./issues-table-columns";
import {
  type AdvancesListing,
  answeredWhole,
  type DonePage,
  type DoneRead,
  type IssueListing,
  useIssueTableItems,
} from "./issues-table-data";
import {
  ALL_ROWS,
  COLUMNS,
  type ColumnId,
  columnMinimums,
  compareGroups,
  defaultView,
  facetCounts,
  groupValue,
  ISSUE_GROUP_ATTRIBUTE,
  ISSUE_LINK_ATTRIBUTE,
  ISSUE_ROW_ATTRIBUTE,
  type Item,
  isBoardColumn,
  matchesFilters,
  matchesSearch,
  PAGE_SIZES,
  readUrl,
  tableStorageKey,
  UNCLAIMED_FACET,
  type View,
  viewLayoutColumns,
} from "./issues-table-model";
import {
  type ExtraChip,
  type GroupByChoice,
  IssueTableSelection,
  IssueTableStatus,
  IssueTableToolbar,
} from "./issues-table-page";
import { GroupRow, IssueRow } from "./issues-table-rows";
import { useTableView } from "./issues-table-view";
import {
  BUILT_IN_VIEWS,
  type BuiltInView,
  type BuiltInViewId,
  clearNarrowings,
  currentAddress,
  isViewChanged,
  legacyOpenLayout,
  narrowingsKey,
  readSavedViews,
  type SavedView,
  type StoredViews,
  viewFor,
  viewQueryKey,
  windowRead,
} from "./issues-views";
import { RangeFilter } from "./RangeFilter";
import { issueIsUnread } from "./UnreadDot";

const EMPTY_FACETS: ReadonlyMap<string, number> = new Map();
/** Status offers Done before the closed issues it would count are read. */
const DONE_UNLOADED: ReadonlyMap<string, readonly string[]> = new Map([["status", ["done"]]]);
const NONE: readonly string[] = [];
const NO_VIEWS: readonly BuiltInView[] = [];
const NO_SAVED: readonly SavedView[] = [];
/** An embedded table's own parameters that Clear filters leaves as they are (`<stateKey>.`). */
const EMBEDDED_KEPT = ["sort", "group", "size"];
/** The page's flags Clear filters takes off an embedded table as well, as main's does. */
const EMBEDDED_CLEARED = ["needs-you", "unread"];

const fuzzy: FilterFn<Item> = (row, _id, query: string) => matchesSearch(row.original, query);

const ACTION_WIDTH = 104;

/** The checkbox a row draws for its selection (`issues-table-columns.tsx`'s select column), the
 *  one checkbox every row has. */
const ROW_CHECKBOX = 'input[type="checkbox"]';

/** The List's own words, which the table core reads and never invents: the attributes its rows
 *  and groups carry, the link opening a row follows, what its filters count, and the keys that
 *  walk it, under the ids and labels the palette and the shortcut help have always shown. */
export const ISSUE_TABLE_NAMES: DataTableNames = {
  filterCountNoun: { one: "issue", many: "issues" },
  groupAttribute: ISSUE_GROUP_ATTRIBUTE,
  keymap: {
    next: { id: "list-next", keys: "j", label: "Next issue" },
    open: { id: "list-open", keys: "o", label: "Open issue" },
    openOnEnter: { id: "list-open-enter", keys: "Enter", label: "Open the focused issue" },
    previous: { id: "list-previous", keys: "k", label: "Previous issue" },
    scope: "project",
    select: { id: "list-select", keys: "x", label: "Select or deselect the focused issue" },
  },
  linkAttribute: ISSUE_LINK_ATTRIBUTE,
  rowAttribute: ISSUE_ROW_ATTRIBUTE,
};

/** The line under the table: the range of rows, the page, the steps and the page size, with no
 *  totals. The Issues tab's open view counts its rows on its tab; a priority page's tables
 *  count theirs in their headings and strips. */
const ISSUE_TABLE_PAGER: PagerOptions = {
  allRows: ALL_ROWS,
  nouns: {
    group: { one: "group", many: "groups" },
    item: { one: "issue", many: "issues" },
    row: { one: "row", many: "rows" },
  },
  pageSizes: PAGE_SIZES,
  testId: "issues-table-pager",
  totals: false,
};

/** Where one view's columns, widths and layout are kept in this browser, per project. Open
 *  keeps the key the table has always used, so today's stored layout becomes Open's. */
const viewStorageKey = (project: string, view: BuiltInViewId) =>
  view === "open" ? tableStorageKey(project) : `${tableStorageKey(project)}.view.${view}`;

/** Whether a view lists closed issues: its Status filter picks Done, or the Status filter is
 *  lifted (`lifted`, `nofilter=1`). */
function filtersListClosed(filters: View["filters"], lifted: boolean): boolean {
  const status = filters.find((filter) => filter.id === "status")?.value;
  return Array.isArray(status) ? status.map(String).includes("done") : lifted;
}

/** Whether an address of the Issues tab lists closed issues (`filtersListClosed`). */
function listsClosed(params: URLSearchParams): boolean {
  return filtersListClosed(readUrl(params).filters, params.get("nofilter") === "1");
}

/** Which done issues a view reads: none, the window its Closed filter names, or every done
 *  issue where it names none. */
function doneRead(filters: View["filters"], lifted: boolean, nowMs: number): DoneRead {
  if (!filtersListClosed(filters, lifted)) return undefined;
  const closed = filters.find((filter) => filter.id === "closed");
  return (typeof closed?.value === "string" ? windowRead(closed.value, nowMs) : undefined) ?? {};
}

/** The one layout key of every table scoped to a priority: the work advancing a priority is
 *  one table, drawn under an opened row of the priorities page and on the priority's own page,
 *  so the columns a reader drags there are dragged once rather than once per priority. */
const SCOPED_LAYOUT_KEY = "priority-work";

/**
 * Where the table stands. On a project's Issues tab it is the project's one list of issues, in
 * views, in the table or the Board. Another page (the priority pages) draws it embedded, over a
 * listing of its own, as main draws it - no view tabs, no saved views, no Board, no Legion
 * columns, and every parameter of its own under `stateKey`.
 */
type TablePlace =
  | {
      readonly kind: "project";
      readonly project: string;
      readonly hasLegion: boolean;
      readonly settingsFailure: SettingsFailure | undefined;
    }
  | {
      readonly kind: "embedded";
      readonly listing: AdvancesListing;
      readonly label: string;
      readonly layout:
        | { readonly key: string; readonly shown: readonly ShownColumnId[] }
        | undefined;
      readonly priorityTitles: ReadonlyMap<string, string> | undefined;
      readonly stateKey: string;
    };

/** The Issues tab's table, or an issue table another page draws over its own listing. */
export function IssuesTable({
  label,
  layout,
  listing,
  priorityTitles,
  stateKey,
}: {
  /** What this list is called, for a reader who cannot see where it sits; by its listing when
   *  unset: a project's issues, or the issues advancing one priority. */
  label?: string;
  /** The layout this table keeps, under its own key, where it is not its listing's, and the
   *  columns it opens with shown (`issueTableLayout`). */
  layout?: { readonly key: string; readonly shown: readonly ShownColumnId[] };
  /** What this table lists: one project's issues, with the project's views, or the issues
   *  advancing one priority, which belong to no project (`issues-table-data.ts`). */
  listing: IssueListing;
  /** Each priority's title by its id. Given, the table has the Advances column. */
  priorityTitles?: ReadonlyMap<string, string>;
  /** The prefix a table another page draws writes its own view under: `<stateKey>.sort` and
   *  the rest. */
  stateKey?: string;
}): ReactNode {
  if ("project" in listing) return <ProjectIssuesTable project={listing.project} />;
  return (
    <ViewsTable
      place={{
        kind: "embedded",
        label: label ?? "Priority issues",
        layout,
        listing,
        priorityTitles,
        stateKey: stateKey ?? "issues",
      }}
    />
  );
}

/** The Issues tab: the project's one list of issues, in views, in the table or the Board. */
function ProjectIssuesTable({ project }: { project: string }): ReactNode {
  const settings = useQuery(legionSettingsQuery());
  if (settings.isPending) return <LoadingSkeleton label="Loading issues" />;
  // Until a read of the Legion setting answers, the project is drawn as one with no daemon, and
  // a failed read says so beside the tabs with its error and a Retry.
  const hasLegion = (settings.data ?? []).some((setting) => setting.project === project);
  const settingsFailure: SettingsFailure | undefined =
    settings.isError && settings.data === undefined
      ? {
          message: apiErrorMessage(settings.error, "Dispatch did not answer"),
          retry: () => void settings.refetch(),
          retrying: settings.isFetching,
        }
      : undefined;
  return (
    <ViewsTable key={project} place={{ kind: "project", hasLegion, project, settingsFailure }} />
  );
}

function ViewsTable({ place }: { place: TablePlace }): ReactNode {
  const embedded = place.kind === "embedded";
  const project = place.kind === "project" ? place.project : undefined;
  const hasLegion = place.kind === "project" && place.hasLegion;
  const settingsFailure = place.kind === "project" ? place.settingsFailure : undefined;
  const listing: IssueListing =
    place.kind === "project" ? { project: place.project } : place.listing;
  // Whether the listing's one open read is every row it holds, so the table reads no closed
  // issue beside it (`answeredWhole`).
  const whole = answeredWhole(listing);
  // A parameter of this table's own view: bare on the Issues tab, under the prefix elsewhere.
  const ownParam = (name: string) =>
    place.kind === "embedded" ? `${place.stateKey}.${name}` : name;
  const params = useAddressParams();
  const update = useAddressUpdate();
  const { view, setFilters, setGrouping, setPagination, setSearch, setSorting } = useTableView(
    place.kind === "embedded" ? place.stateKey : undefined
  );
  const { sorting, grouping, filters, search } = view;
  const pagination = { pageIndex: view.page, pageSize: view.pageSize };
  const views = embedded ? NO_VIEWS : BUILT_IN_VIEWS;
  // An embedded table draws no view: it opens as Open does.
  const builtIn = viewFor(embedded ? null : params.get("view"));
  // An older link to the Legion queue's tab is written as Open with its Queue filter, which
  // lists the same rows, so the tab marks the change and Clear filters takes it away.
  const formerView = embedded ? null : params.get("view");
  useEffect(() => {
    if (formerView === null) return;
    update((current) => currentAddress(current) ?? current);
  }, [formerView, update]);
  const viewer = useQuery(whoAmIQuery()).data?.login;
  const board = useQuery({
    ...legionBoardQuery({ kind: "legion", project: project ?? "" }),
    enabled: hasLegion,
  });
  const flags = useIssueFilters();
  const userState = useQuery(userStateQuery());
  const [selection, setSelection] = useState<RowSelectionState>({});
  const lastMarked = useRef<string | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const recordMark = useCallback((key: string) => {
    lastMarked.current = key;
  }, []);
  const [openFilter, setOpenFilter] = useState<string>();
  const [phaseMode, setPhaseMode] = usePhaseHoursMode();

  // Saved views: this browser's, for the signed-in login, per project.
  const [stored, setStored] = useUserPreference<StoredViews>(
    "project.saved-views",
    readSavedViews,
    (value) => JSON.stringify(value)
  );
  const saved = project === undefined ? NO_SAVED : (stored.projects[project] ?? NO_SAVED);
  const [chosenSaved, setChosenSaved] = useState<string>();
  const queryKey = viewQueryKey(params);
  const matchedSaved = saved.find(
    (entry) => viewQueryKey(copyAddress(entry.query)) === queryKey
  )?.name;
  const savedName =
    chosenSaved !== undefined && saved.some((entry) => entry.name === chosenSaved)
      ? chosenSaved
      : matchedSaved;
  const savedView = saved.find((entry) => entry.name === savedName);
  const active: ActiveView =
    savedView === undefined
      ? { kind: "built-in", id: builtIn.id }
      : { kind: "saved", name: savedView.name };
  const changed =
    savedView === undefined
      ? isViewChanged(params)
      : viewQueryKey(copyAddress(savedView.query)) !== queryKey;
  // The address the view opens at, and whether the reader's narrowings differ from its own: the
  // tab's mark shows any change, Clear filters only a change of what the rows are narrowed by.
  // An embedded table offers Clear filters by main's rule: while it has a filter (its Status
  // filter included), a search, or the Needs you or Unread flag.
  const ownAddress =
    savedView === undefined
      ? copyAddress(builtIn.id === "open" ? "" : `view=${builtIn.id}`)
      : copyAddress(savedView.query);
  const narrowed = embedded
    ? filters.length > 0 || search !== "" || flags.needsYou || flags.unread
    : narrowingsKey(params) !== narrowingsKey(ownAddress);

  // The layout: the address's for this load, else the one kept for the view, else today's
  // `project.issue-view` choice (Open's). An embedded table keeps its layout under its own key,
  // as one table wherever it is drawn, and has no Board.
  const storeKey =
    place.kind === "embedded"
      ? tableStorageKey(place.layout?.key ?? SCOPED_LAYOUT_KEY)
      : viewStorageKey(place.project, builtIn.id);
  const [legacyView] = useUserPreference(
    "project.issue-view",
    (value) => value ?? "list",
    (value) => value
  );
  const [keptLayouts, setKeptLayouts] = useState<Record<string, ViewLayout>>({});
  const keptLayout =
    keptLayouts[storeKey] ??
    (readStore(storeKey).layout === "board"
      ? "board"
      : readStore(storeKey).layout === "table"
        ? "table"
        : undefined);
  const urlLayout = embedded ? null : params.get("layout");
  // Before the reader picks a layout of Open, today's `project.issue-view` choice picks it:
  // `board` its Board, `legion` (the old Legion view) its Board by Legion phase.
  const legacy = legacyOpenLayout(legacyView);
  const layoutChoice: ViewLayout = embedded
    ? "table"
    : urlLayout === "board" || urlLayout === "table"
      ? urlLayout
      : (keptLayout ?? (builtIn.id === "open" ? legacy.layout : "table"));
  // The Board's columns are the address's `group=`, so the Legion phase is written there once,
  // when Open first opens on that choice with no grouping of its own; a reader who then picks
  // Status keeps it for the load.
  const legacyGrouped = useRef(false);
  const legacyGroup =
    !embedded &&
    hasLegion &&
    builtIn.id === "open" &&
    urlLayout === null &&
    keptLayout === undefined
      ? legacy.group
      : undefined;
  useEffect(() => {
    if (legacyGroup === undefined || legacyGrouped.current) return;
    legacyGrouped.current = true;
    update((current) => {
      if (!current.has("group")) current.set("group", legacyGroup);
      return current;
    });
  }, [legacyGroup, update]);
  const setLayout = (next: ViewLayout) => {
    if (params.has("layout"))
      update((current) => {
        current.set("layout", next);
        return current;
      });
    else {
      writeStore(storeKey, { layout: next });
      setKeptLayouts((current) => ({ ...current, [storeKey]: next }));
    }
  };
  useKeymap(
    "project",
    embedded
      ? []
      : [
          {
            id: "toggle-view",
            keys: "v",
            label: "Switch the table and the Board",
            run: () => setLayout(layoutChoice === "board" ? "table" : "board"),
          },
        ]
  );
  const [showIcebox, setShowIcebox] = useUserPreference(
    "project.board-edges",
    (value) => value === "shown",
    (shown) => (shown ? "shown" : "hidden")
  );
  const [lanes, setLanes] = useUserPreference(
    "project.board-lanes",
    (value) => value === "shown",
    (shown) => (shown ? "shown" : "hidden")
  );

  // The columns: the view's own, the address's `cols=` for this load. Where the project has no
  // Legion daemon the board's columns are not drawn, and Queue (Start next's order there) is in
  // the Columns menu rather than the defaults. An embedded table opens with the columns the
  // List had before the views, and the listing columns its listing gives a use
  // (`listingColumns`).
  const viewCols = useMemo(
    () =>
      embedded
        ? []
        : hasLegion
          ? builtIn.cols
          : builtIn.cols.filter((id) => !isBoardColumn(id) && id !== "queue"),
    [builtIn, embedded, hasLegion]
  );
  const listingColsText =
    place.kind === "embedded"
      ? listingColumns(place.listing, place.priorityTitles !== undefined).join(",")
      : "";
  const listingCols = useMemo(
    () => (listingColsText === "" ? [] : (listingColsText.split(",") as ListingColumnId[])),
    [listingColsText]
  );
  // Below 1280 px every button is a 44 px touch target, and each column's minimum holds its
  // header with that filter button; from 1280 px with the 32 px one.
  const touch = useMediaQuery(COMPACT_VIEWPORT_QUERY);
  const storedLayout = useTableLayout(
    storeKey,
    place.kind === "embedded"
      ? issueTableLayout(listingCols, place.layout?.shown)
      : viewLayoutColumns(viewCols, touch)
  );
  const colsParam = embedded ? null : params.get("cols");
  // Start next is offered only on the view that lists Todo issues (Open); elsewhere its column
  // is not drawn, so the view's own columns get its width. An embedded table offers it as the
  // List did.
  const trailingWidth = embedded || builtIn.id === "open" ? ACTION_WIDTH : 0;
  const layout = useMemo(() => {
    // An embedded table is laid out by main's rules alone (`issueTableLayout`): its columns at
    // main's widths, scrolling sideways where they do not fit, in the order the reader left them.
    if (embedded) return storedLayout;
    const kept =
      storedLayout.order.length > 0
        ? storedLayout.order
        : [
            "select",
            ...viewCols,
            ...COLUMNS.map((column) => column.id).filter(
              (id) => id !== "select" && !viewCols.includes(id)
            ),
          ];
    // On a phone Select and Key are the pinned columns, and Title, which fills the rest of the
    // box, comes straight after them, so a row at rest reads its key and its title.
    const phone = storedLayout.boxWidth < 640;
    const phoneOrder = (ids: readonly string[]) => [
      "select",
      "key",
      "title",
      ...ids.filter((id) => id !== "select" && id !== "key" && id !== "title"),
    ];
    const order = phone ? phoneOrder(kept) : kept;
    // A header drag on a phone keeps that rule: a column dropped on Title lands right after it
    // (the core puts it before Title, and Title is put back after Key), a column dropped on
    // another moves among the columns after Title, and Title dragged anywhere stays where it
    // is. What is stored is the order drawn, so it survives a reload.
    const setOrder: typeof storedLayout.setOrder = phone
      ? (next) =>
          storedLayout.setOrder((current) =>
            phoneOrder(typeof next === "function" ? next(current) : next)
          )
      : storedLayout.setOrder;
    const hidden: VisibilityState = hasLegion
      ? {}
      : Object.fromEntries(COLUMNS.filter((c) => c.board).map((c) => [c.id, false]));
    const shown = colsParam === null ? undefined : new Set(["select", ...colsParam.split(",")]);
    const visibility: VisibilityState =
      shown === undefined
        ? { ...storedLayout.visibility, ...hidden }
        : {
            ...Object.fromEntries(COLUMNS.map((column) => [column.id, shown.has(column.id)])),
            ...hidden,
          };
    // Until the reader drags a width, a view's columns are fitted to the box: Title gives first,
    // down to its minimum (240 px, a width its two lines read at), then every other column
    // toward its minimum. What still does not fit scrolls sideways under the pinned columns.
    const minimums = columnMinimums(touch);
    const responsive = (() => {
      const { boxWidth: box, responsive: drawn, sizing } = storedLayout;
      if (Object.keys(sizing).length > 0 || box < 640) return drawn;
      const ids = order.filter((id) => visibility[id] !== false);
      const widths = { ...drawn.sizing };
      let over = ids.reduce((sum, id) => sum + (widths[id] ?? 0), 0) + trailingWidth - box + 2;
      if (over <= 0) return drawn;
      const title = widths.title;
      if (title !== undefined && ids.includes("title")) {
        const cut = Math.min(over, Math.max(0, title - (minimums.title ?? 0)));
        widths.title = title - cut;
        over -= cut;
      }
      const slack = ids
        .filter((id) => id !== "select" && id !== "title")
        .map((id) => [id, Math.max(0, (widths[id] ?? 0) - (minimums[id] ?? 0))] as const);
      const total = slack.reduce((sum, [, room]) => sum + room, 0);
      if (over > 0 && total > 0) {
        const given = Math.min(over, total);
        for (const [id, room] of slack) widths[id] = (widths[id] ?? 0) - (given * room) / total;
      }
      return { ...drawn, sizing: widths };
    })();
    if (shown === undefined) return { ...storedLayout, order, responsive, setOrder, visibility };
    return {
      ...storedLayout,
      order,
      setOrder,
      responsive,
      visibility,
      // `cols=` holds for this load and writes nothing: a change of columns rewrites it.
      setVisibility: (next: VisibilityState | ((current: VisibilityState) => VisibilityState)) => {
        const resolved = typeof next === "function" ? next(visibility) : next;
        const ids = COLUMNS.map((column) => column.id).filter(
          (id) => id !== "select" && resolved[id] !== false
        );
        update((current) => {
          current.set("cols", ids.join(","));
          return current;
        });
      },
    };
  }, [colsParam, embedded, hasLegion, storedLayout, touch, trailingWidth, update, viewCols]);
  const { attachAbove, boxWidth } = layout;

  // The reads: the open listing, the done issues the view lists, and the board.
  const nowMs = Date.now();
  // A view whose Status filter picks Done alone holds only the done rows it read, so its footer
  // counts its rows against those rather than against the open listing beside them.
  const statusValue = filters.find((filter) => filter.id === "status")?.value;
  const doneOnly =
    Array.isArray(statusValue) && statusValue.length === 1 && statusValue[0] === "done";
  // Done reads only the page on screen from the server while it lists every done issue (in its
  // Closed window, where it has one) in the order they closed: sorted by Closed alone, with no
  // search, flag, chip, grouping or other column filter, and a page size the server serves.
  // Anything else asks every row the window holds, and the table does the rest.
  const closeSort =
    sorting.length === 1 && sorting[0]?.id === "closed"
      ? sorting[0].desc
        ? ("-closed_at" as const)
        : ("closed_at" as const)
      : undefined;
  const pagedByServer =
    place.kind === "project" &&
    doneOnly &&
    closeSort !== undefined &&
    search === "" &&
    grouping.length === 0 &&
    pagination.pageSize !== ALL_ROWS &&
    !flags.needsYou &&
    !flags.unread &&
    filters.every((filter) => filter.id === "status" || filter.id === "closed") &&
    !params.has("exclude_tree") &&
    !params.has("only");
  const doneWindow = whole
    ? undefined
    : doneRead(filters, params.get(ownParam("nofilter")) === "1", nowMs);
  const done: DoneRead =
    doneWindow === undefined || !pagedByServer || closeSort === undefined
      ? doneWindow
      : {
          ...doneWindow,
          page: {
            sort: closeSort,
            limit: pagination.pageSize,
            offset: pagination.pageIndex * pagination.pageSize,
          },
        };
  // A project's view that lists only done issues (Done) reads no open listing: its rows are
  // the done reads, and the other tabs count from the server's total and the board.
  const readsOpen = !(place.kind === "project" && doneOnly);
  const claimPicked = (filters.find((filter) => filter.id === "claim")?.value ?? []) as string[];
  const wantsFree = claimPicked.includes(UNCLAIMED_FACET);
  const registry = useAgentRegistry(wantsFree || openFilter === "claim");
  const { all, allItems, doneIssues, doneLoaded, doneTotal, issues, listingError, openLoaded } =
    useIssueTableItems(listing, {
      done,
      readsOpen,
      registry,
      board: board.data,
      hasLegion,
      viewer,
    });

  // The narrowings that are no column: the flags, `exclude_tree` and `only`.
  const excludedText = params.getAll("exclude_tree").join(",");
  const excluded = useMemo(
    () => (excludedText === "" ? [] : excludedText.split(",")),
    [excludedText]
  );
  const onlyParam = params.get("only");
  const only = useMemo(
    () => (onlyParam === null ? undefined : new Set(onlyParam.split(",").filter(Boolean))),
    [onlyParam]
  );
  const items = useMemo(
    () =>
      allItems.filter(
        ({ issue, facts }) =>
          (!doneOnly || issue.status === "done") &&
          (!flags.needsYou || issue.open_asks > 0) &&
          (!flags.unread ||
            issueIsUnread(issue, userState.data?.[issue.key]?.last_read_seq ?? 0)) &&
          !excluded.some((tree) => tree === issue.key || tree === facts?.root_key) &&
          (only === undefined || only.has(issue.key))
      ),
    [allItems, doneOnly, flags.needsYou, flags.unread, userState.data, only, excluded]
  );

  const columns = useIssueTableColumns(
    userState.data,
    boxWidth < 640,
    recordMark,
    listingCols,
    place.kind === "embedded" ? place.priorityTitles : undefined,
    embedded,
    touch
  );

  const model = useDataTable<Item>({
    rows: items,
    rowCount: doneTotal,
    columns,
    getRowId: (item) => item.issue.key,
    layout,
    names: ISSUE_TABLE_NAMES,
    state: { sorting, grouping, columnFilters: filters, globalFilter: search, pagination },
    onSortingChange: setSorting,
    onGroupingChange: setGrouping,
    onColumnFiltersChange: setFilters,
    onGlobalFilterChange: setSearch,
    onPaginationChange: setPagination,
    viewKey: params.toString(),
    selection: { state: selection, onChange: setSelection, onMark: recordMark },
    search: { column: "title", filterFn: fuzzy },
    groupOrder: { compare: compareGroups, heading: groupValue },
  });
  const { table } = model;
  const returnFocus = () => {
    const leaves = model.displayRows.flatMap(({ row }) => (row.getIsGrouped() ? [] : [row.id]));
    const last = lastMarked.current;
    const key = last !== null && leaves.includes(last) ? last : leaves[0];
    return key === undefined ? searchRef.current : model.focusRow(key, ROW_CHECKBOX);
  };

  const listedRows = table.getFilteredRowModel().rows;
  const listedKeys = !openLoaded ? undefined : listedRows.map((row) => row.id).join(" ");
  useEffect(() => {
    if (listedKeys === undefined) return;
    const listed = new Set(listedKeys === "" ? [] : listedKeys.split(" "));
    setSelection((current) =>
      Object.keys(current).every((key) => listed.has(key))
        ? current
        : Object.fromEntries(Object.entries(current).filter(([key]) => listed.has(key)))
    );
  }, [listedKeys]);

  const selectedSignature = listedRows
    .filter((row) => row.getIsSelected())
    .map((row) => row.id)
    .join(" ");
  useEffect(() => {
    setSelectedIssues(selectedSignature === "" ? [] : selectedSignature.split(" "));
    return () => setSelectedIssues([]);
  }, [selectedSignature]);

  const {
    busy,
    notice,
    moved,
    startNext,
    confirming,
    cancelConfirming,
    confirmed,
    interrupted,
    interruptedDescription,
    finish,
    dismiss,
  } = useIssueTableActions({
    listing,
    issues: all,
    items: allItems,
    clearSelection: () => setSelection({}),
  });

  const pageCount = table.getPageCount();
  useEffect(() => {
    if (
      (done === undefined || doneIssues.data !== undefined) &&
      openLoaded &&
      pagination.pageIndex >= Math.max(pageCount, 1)
    )
      table.setPageIndex(Math.max(pageCount - 1, 0));
  }, [done, doneIssues.data, openLoaded, pagination.pageIndex, pageCount, table]);
  const openFacets = useMemo(
    () => (openFilter ? facetCounts(items, openFilter, filters, search) : EMPTY_FACETS),
    [items, openFilter, filters, search]
  );

  // Each tab's count, from the data the page holds: the open listing (and the board) for every
  // view but Done, whose count is the server's total of its own page read - the last page this
  // page read of every done issue, with no Closed window, which every page of answers with the
  // same total. Kept once Done has read one, so the tab still counts while another view is open,
  // and the same query as Done's own, so no second read is made. A view that reads no open
  // listing (Done) counts Open by the server's total of a one-row open page, and the Legion
  // queue by the board alone.
  const ownDonePage =
    done?.page !== undefined && done.closed_since === undefined && done.closed_before === undefined
      ? done.page
      : undefined;
  const [donePageRead, setDonePageRead] = useState<DonePage | undefined>(ownDonePage);
  if (
    ownDonePage !== undefined &&
    (donePageRead?.sort !== ownDonePage.sort ||
      donePageRead.limit !== ownDonePage.limit ||
      donePageRead.offset !== ownDonePage.offset)
  )
    setDonePageRead(ownDonePage);
  const doneCount = useQuery({
    ...projectDonePageQuery(
      project ?? "",
      {},
      donePageRead ?? { sort: "-closed_at", limit: defaultView.pageSize, offset: 0 }
    ),
    enabled: donePageRead !== undefined && project !== undefined,
  });
  const openCount = useQuery({
    ...projectOpenCountQuery(project ?? ""),
    enabled: !readsOpen && project !== undefined,
  });
  const counts = useMemo(() => {
    const result = new Map<string, number | undefined>();
    for (const entry of views) {
      if (entry.id === "done") {
        result.set(entry.id, doneCount.data?.total);
        continue;
      }
      if (!readsOpen) {
        result.set(entry.id, openCount.data);
        continue;
      }
      // Before the open listing answers the tab says no count, not 0.
      if (!openLoaded) {
        result.set(entry.id, undefined);
        continue;
      }
      const own = readUrl(copyAddress(`view=${entry.id}`)).filters;
      result.set(entry.id, allItems.filter((item) => matchesFilters(item, own)).length);
    }
    return result;
  }, [views, allItems, doneCount.data, openCount.data, openLoaded, readsOpen]);
  // A saved view counts from the open listing, so it says no count on a view that reads none;
  // one that lists closed issues counts only while it is open (`tabSavedCounts`): the page
  // holds its closed rows then alone.
  const savedCounts = useMemo(() => {
    const result = new Map<string, number | undefined>();
    for (const entry of saved) {
      const query = copyAddress(entry.query);
      if (!readsOpen || listsClosed(query)) {
        result.set(entry.name, undefined);
        continue;
      }
      const own = readUrl(query).filters;
      result.set(entry.name, allItems.filter((item) => matchesFilters(item, own)).length);
    }
    return result;
  }, [saved, allItems, readsOpen]);

  const selectView = (id: string) => {
    setChosenSaved(undefined);
    const next = viewFor(id);
    update(() => {
      const fresh = copyAddress();
      if (next.id !== "open") fresh.set("view", next.id);
      return fresh;
    });
  };
  const selectSaved = (name: string) => {
    const entry = saved.find((candidate) => candidate.name === name);
    if (entry === undefined) return;
    setChosenSaved(name);
    update(() => copyAddress(entry.query));
  };
  const writeSaved = (next: readonly { name: string; query: string }[]) => {
    if (project !== undefined)
      setStored({ version: 1, projects: { ...stored.projects, [project]: [...next] } });
  };
  const saveView = (name: string) => {
    const query = copyAddress(params);
    query.delete("page");
    if (!query.has("cols"))
      query.set(
        "cols",
        table
          .getVisibleLeafColumns()
          .map((column) => column.id)
          .filter((id) => id !== "select")
          .join(",")
      );
    query.set("layout", layoutChoice);
    const entry = { name, query: query.toString() };
    writeSaved([...saved.filter((candidate) => candidate.name !== name), entry]);
    setChosenSaved(name);
    update(() => copyAddress(entry.query));
  };
  const deleteSaved = (name: string) => {
    writeSaved(saved.filter((candidate) => candidate.name !== name));
    setChosenSaved(undefined);
  };
  // Clear filters: the narrowings back to the view's own, the sort and grouping kept. An
  // embedded table clears as main's does: its filters back to the open statuses, its search and
  // its page, and the Needs you and Unread flags; its sort, grouping and page size stay.
  const clearFilters = () =>
    update((current) => {
      if (place.kind === "project") return clearNarrowings(current, ownAddress);
      const mark = `${place.stateKey}.`;
      for (const key of [...current.keys()])
        if (
          (key.startsWith(mark) && !EMBEDDED_KEPT.includes(key.slice(mark.length))) ||
          EMBEDDED_CLEARED.includes(key)
        )
          current.delete(key);
      return current;
    });

  // The bar: the listed rows by status, on a view that draws one (Open). An embedded table
  // draws main's strip instead (`IssueTableStatus`). A press narrows the Status filter, and a
  // second press takes the address's `status` away, which gives the view's own back.
  const barPicks =
    params.has("status") && Array.isArray(statusValue) ? statusValue.map(String) : undefined;
  const narrowTo = (values: readonly string[] | undefined) => {
    if (values === undefined)
      update((current) => {
        current.delete("status");
        current.delete("page");
        return current;
      });
    else table.getColumn("status")?.setFilterValue([...values]);
  };

  // The chips of the narrowings an address carries that are no column's filter.
  const extraChips: ExtraChip[] = [
    ...excluded.map((tree) => ({
      label: `Not tree ${tree}`,
      remove: () =>
        update((current) => {
          const rest = current.getAll("exclude_tree").filter((value) => value !== tree);
          current.delete("exclude_tree");
          for (const value of rest) current.append("exclude_tree", value);
          return current;
        }),
    })),
    ...(only === undefined
      ? []
      : [
          {
            label: params.get("onlyLabel") ?? `${only.size} issues`,
            remove: () =>
              update((current) => {
                current.delete("only");
                current.delete("onlyLabel");
                return current;
              }),
          },
        ]),
  ];
  // The Columns menu lists the Legion columns first, where a reader looks for them, then the
  // issue's own columns. An embedded table lists its columns as main does, in their order.
  const legionFirst = (id: string) =>
    isBoardColumn(id) || id === "queue" || id === "closed" ? 0 : 1;
  const offeredColumns: Column<Item>[] = embedded
    ? table.getAllLeafColumns()
    : table
        .getAllLeafColumns()
        .filter((column) => hasLegion || !isBoardColumn(column.id))
        .sort((a, b) => legionFirst(a.id) - legionFirst(b.id));

  // The Group by menu. In the table it bands the rows by one of the columns that group, in the
  // registry's order (main's columns, and the Legion phase where a daemon runs); on the Board it
  // picks the Board's columns, Status or the Legion phase, kept in the same `group=` the table
  // bands by. A Board with no Legion daemon has nothing to pick and draws no menu.
  const boardBy: BoardBy = hasLegion && grouping[0] === "legion_phase" ? "legion_phase" : "status";
  const registryPlace = (id: string) => COLUMNS.findIndex((column) => column.id === id);
  const groupBy: GroupByChoice | undefined =
    layoutChoice === "board" && !embedded
      ? hasLegion
        ? {
            value: boardBy,
            options: [
              { label: "Status", value: "status" },
              { label: "Legion phase", value: "legion_phase" },
            ],
            onChange: (next) => table.setGrouping(next === "legion_phase" ? [next] : []),
          }
        : undefined
      : {
          value: grouping[0] ?? "",
          options: [
            { label: "Nothing", value: "" },
            ...offeredColumns
              .filter((column) => column.getCanGroup())
              .sort((a, b) => registryPlace(a.id) - registryPlace(b.id))
              .map((column) => ({ label: String(column.columnDef.header), value: column.id })),
          ],
          onChange: (next) => {
            table.setGrouping(next ? [next] : []);
            table.setExpanded(true);
          },
        };

  // A view with a Legion filter waits for the board: it never lists unfiltered rows under the
  // view's name.
  const legionFiltered =
    hasLegion && filters.some((filter) => isBoardColumn(filter.id) || filter.id === "queue");
  const pageRows = table.getRowModel().rows;
  const pageMax = Math.max(0, ...pageRows.map((row) => phaseHours(row.original.facts).total));
  // TanStack hands back the same sorted model until the rows, the filters or the sort change.
  // Where the server pages the view, the rows it lists are the page on screen alone.
  const sortedModel = table.getSortedRowModel();
  const boardRows = useMemo(
    () => sortedModel.flatRows.filter((row) => !row.getIsGrouped()).map((row) => row.original),
    [sortedModel]
  );
  // The statuses the view's Status filter picks, which the status Board draws as columns.
  const statusPicks = Array.isArray(statusValue) ? statusValue.map(String) : undefined;
  // The Board reorders a column only while the view shows it in Queue order, first first, which
  // is the order a rank write moves (GitHub Projects' rule: a sorted board takes no reorder
  // within a column). Under any other sort a drop into another column writes the status alone.
  const firstSort = sorting[0];
  const reorderRefusal =
    firstSort?.id === "queue" && !firstSort.desc
      ? undefined
      : firstSort === undefined
        ? "Not sorted by Queue: sort by Queue to reorder"
        : `Sorted by ${String(table.getColumn(firstSort.id)?.columnDef.header ?? firstSort.id)}${
            firstSort.id === "queue" ? ", last first" : ""
          }: sort by Queue to reorder`;
  // The labels the Board's Labels picker offers: every label of the project's loaded issues.
  const labelOptions = useMemo(
    () =>
      [...new Set(allItems.flatMap((item) => item.issue.labels ?? []))].sort((a, b) =>
        a.localeCompare(b)
      ),
    [allItems]
  );

  const loading =
    (readsOpen && issues.isPending) ||
    userState.isPending ||
    (done !== undefined && doneIssues.isPending) ||
    (legionFiltered && board.isPending);
  // The active tab counts the rows the table lists now, the reader's own changes (filters,
  // search, flags, chips) included, as a search narrows GitHub's Open count; a view the server
  // pages counts by the server's total. Every other tab keeps its own view's count.
  const listedCount = loading ? undefined : (doneTotal ?? listedRows.length);
  const tabCounts =
    active.kind === "built-in" ? new Map(counts).set(active.id, listedCount) : counts;
  const tabSavedCounts =
    active.kind === "saved" ? new Map(savedCounts).set(active.name, listedCount) : savedCounts;

  const tabs = embedded ? null : (
    <IssueViewTabs
      active={active}
      boardHeader={board.data?.header}
      changed={changed}
      counts={tabCounts}
      hasLegion={hasLegion}
      onDelete={deleteSaved}
      onSelect={selectView}
      onSelectSaved={selectSaved}
      saved={saved}
      savedCounts={tabSavedCounts}
      settingsFailure={settingsFailure}
      views={views}
    />
  );
  const failed = (readsOpen && issues.isError && issues.data === undefined) || userState.isError;
  const boardFailed = legionFiltered && board.isError;

  const toolbarTrailing = embedded ? (
    <ColumnsMenu columns={offeredColumns} size="compact" />
  ) : (
    <>
      <LayoutSwitch layout={layoutChoice} onLayout={setLayout} />
      {layoutChoice === "board" ? (
        <>
          {boardBy === "status" ? (
            <ToggleButton onChange={setShowIcebox} pressed={showIcebox}>
              Show Icebox
            </ToggleButton>
          ) : null}
          <ToggleButton onChange={setLanes} pressed={lanes}>
            Lanes
          </ToggleButton>
        </>
      ) : (
        <ColumnsMenu columns={offeredColumns} size="compact" />
      )}
      <ViewControls
        active={active}
        boardHeader={board.data?.header}
        boardPending={board.isPending}
        changed={changed}
        hasLegion={hasLegion}
        onSave={saveView}
        saved={saved}
      />
    </>
  );

  return (
    <section
      aria-label={place.kind === "embedded" ? place.label : "Project issues"}
      className={`${embedded ? "space-y-3" : "space-y-2"} sm:pb-24`}
      data-view={embedded ? undefined : builtIn.id}
    >
      <div className={embedded ? "space-y-3" : "space-y-2"} ref={attachAbove}>
        {tabs}
        {embedded ? (
          // Issues by status, as main draws them: a click shows only that status, a second
          // click every status.
          <IssueTableStatus items={allItems} table={table} />
        ) : layoutChoice === "board" || builtIn.bar !== true ? null : (
          // The table's bar; the Board draws none, since its columns carry their counts.
          <ViewBar
            items={loading ? [] : listedRows.map((row) => row.original)}
            narrowTo={narrowTo}
            picked={barPicks}
          />
        )}
        <IssueTableToolbar
          embedded={embedded}
          extraChips={extraChips}
          groupBy={groupBy}
          labelOptions={labelOptions}
          layout={layoutChoice}
          // Only while a filter, the search, a flag or a chip differs from the view's own; the
          // sort and the grouping it leaves as they are.
          onClear={narrowed ? clearFilters : undefined}
          searchRef={searchRef}
          table={table}
          trailing={toolbarTrailing}
          unclaimedUnchecked={
            wantsFree && registry.isError
              ? registry.refilling
                ? "refilling"
                : "unreachable"
              : undefined
          }
        />
        {listingError ? (
          <p className={dangerText} role="alert">
            Could not load {done !== undefined ? "closed " : ""}issues.{" "}
            <button
              aria-label={
                done !== undefined ? "Retry loading closed issues" : "Retry loading issues"
              }
              onClick={() => void listingError.refetch()}
              type="button"
            >
              Retry
            </button>
          </p>
        ) : null}
        {notice ? (
          <p
            aria-live="polite"
            className={`text-sm ${notice.kind === "error" ? dangerText : textSecondaryOnCanvas}`}
            role={notice.kind === "error" ? "alert" : "status"}
          >
            {notice.text}
          </p>
        ) : null}
        {interrupted ? (
          <section aria-label="Interrupted table action" className={textSecondaryOnCanvas}>
            <p>{interruptedDescription}</p>
            <button
              className={`min-h-11 rounded-lg border px-3 text-sm ${borderDefault} ${focusVisibleRing}`}
              onClick={finish}
              type="button"
            >
              Finish
            </button>{" "}
            <button
              className={`min-h-11 rounded-lg border px-3 text-sm ${borderDefault} ${focusVisibleRing}`}
              onClick={dismiss}
              type="button"
            >
              Dismiss
            </button>
          </section>
        ) : null}
      </div>

      {failed ? (
        <p className={dangerText} role="alert">
          Could not load issues.{" "}
          <button
            onClick={() => {
              void issues.refetch();
              void userState.refetch();
            }}
            type="button"
          >
            Retry loading issues
          </button>
        </p>
      ) : boardFailed ? (
        <QueryError
          message="Could not read the Legion board, which this view needs."
          onRetry={() => void board.refetch()}
          retrying={board.isFetching}
        />
      ) : loading ? (
        <LoadingSkeleton label="Loading issues" />
      ) : layoutChoice === "board" && project !== undefined ? (
        <>
          <IssueBoard
            by={boardBy}
            hasLegion={hasLegion}
            lanes={lanes}
            listsDone={done !== undefined}
            project={project}
            reorderRefusal={reorderRefusal}
            rows={boardRows}
            showIcebox={showIcebox}
            statusPicks={statusPicks}
            userState={userState.data}
          />
          {/* The Board of a view the server pages draws the page on screen, and the steps to
              the others. */}
          {doneTotal === undefined ? null : <Pager {...ISSUE_TABLE_PAGER} table={table} />}
        </>
      ) : (
        <PhaseHoursContext value={{ mode: phaseMode, pageMax }}>
          <DataTable
            empty={
              embedded ? (
                "No issues match these filters."
              ) : (
                <span className={textMutedOnCanvas}>No issues match these filters</span>
              )
            }
            filterCounts={openFacets}
            filterUnloaded={done !== undefined && !doneLoaded ? DONE_UNLOADED : undefined}
            label={`${place.kind === "embedded" ? place.label : "Project issues"} table`}
            model={model}
            onOpenFilterChange={setOpenFilter}
            openFilter={openFilter}
            pager={ISSUE_TABLE_PAGER}
            renderFilter={({ column, open, onOpenChange }) =>
              column.columnDef.meta?.range ? (
                <RangeFilter column={column} onOpenChange={onOpenChange} open={open} />
              ) : (
                <span className="flex items-center gap-1">
                  {column.id === ("phase_hours" satisfies ColumnId) ? (
                    <PhaseHoursModeToggle mode={phaseMode} onChange={setPhaseMode} />
                  ) : null}
                  <ColumnFilter
                    column={column}
                    countNoun={ISSUE_TABLE_NAMES.filterCountNoun}
                    counts={column.id === openFilter ? openFacets : EMPTY_FACETS}
                    onOpenChange={onOpenChange}
                    open={open}
                    unloaded={
                      done === undefined || doneLoaded
                        ? NONE
                        : (DONE_UNLOADED.get(column.id) ?? NONE)
                    }
                  />
                </span>
              )
            }
            renderGroup={({ entry, index, measure }) => (
              <GroupRow
                columns={table.getVisibleLeafColumns().length + 1}
                continued={entry.continued}
                grouping={grouping[0] ?? ""}
                index={index}
                legionQueue={!embedded}
                measure={measure}
                row={entry.row}
                shown={entry.shown}
                viewportWidth={boxWidth}
              />
            )}
            renderRow={({ row, index, measure }) => (
              <IssueRow
                busy={busy}
                index={index}
                measure={measure}
                moved={moved}
                onStartNext={trailingWidth === 0 ? undefined : (key) => startNext([key])}
                row={row}
              />
            )}
            scrollTestId="issues-table-scroll"
            // An embedded table stacks its Title header's sort and filter on a phone, as main's
            // does; the Issues tab pins Title beside Key and stacks Time per phase instead, so
            // its name, its Share/Hours control and its filter all show in a phone's column.
            stackColumn={embedded ? "title" : ("phase_hours" satisfies ColumnId)}
            trailingWidth={trailingWidth}
          />
        </PhaseHoursContext>
      )}

      <IssueTableSelection
        busy={busy}
        returnFocus={returnFocus}
        startNext={startNext}
        table={table}
      />
      {confirming === undefined ? null : (
        <ConfirmDialog
          body={confirming.body}
          confirmLabel={confirming.confirmLabel}
          key={confirming.title}
          onCancel={cancelConfirming}
          onConfirm={confirmed}
          title={confirming.title}
        />
      )}
    </section>
  );
}
