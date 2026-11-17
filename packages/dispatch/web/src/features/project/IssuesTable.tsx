import { useQuery } from "@tanstack/react-query";
import type { FilterFn, RowSelectionState, VisibilityState } from "@tanstack/react-table";
import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { apiErrorMessage } from "../../api/client";
import {
  legionDaemonQuery,
  projectDonePageQuery,
  projectOpenCountQuery,
  userStateQuery,
  whoAmIQuery,
} from "../../api/queries";
import type { UserState } from "../../api/types";
import { ColumnsMenu } from "../../components/ColumnsMenu";
import {
  DataTable,
  type DataTableNames,
  type RowNoun,
  useDataTable,
} from "../../components/DataTable";
import type { FilterChip } from "../../components/filter-chips";
import { useFocusPlace } from "../../components/focus-place";
import { LoadingSkeleton } from "../../components/LoadingSkeleton";
import type { PagerOptions } from "../../components/Pager";
import { QueryError } from "../../components/QueryError";
import {
  type LayoutColumns,
  type TableLayoutModel,
  useTableLayout,
} from "../../components/table-layout";
import {
  addressPath,
  copyAddress,
  type TableAddress,
  useAddressNavigate,
  useAddressParams,
  useAddressUpdate,
  usePageAddress,
  useTableAddress,
} from "../../lib/page-address";
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
import { IssueBoard } from "./IssueBoard";
import {
  IssueViewTabs,
  LayoutSwitch,
  type ReadFailure,
  ToggleButton,
  ViewControls,
} from "./IssueViewTabs";
import { useIssueFilters } from "./issue-filters";
import { useIssueTableActions } from "./issues-table-actions";
import {
  issueTableLayout,
  listingColumns,
  type ShownColumnId,
  useIssueTableColumns,
} from "./issues-table-columns";
import {
  answeredWhole,
  type DoneRead,
  type IssueListing,
  useIssueTableItems,
} from "./issues-table-data";
import {
  ALL_ROWS,
  clearPicked,
  columnMinimums,
  compareGroups,
  facetCounts,
  fitViewWidths,
  groupValue,
  ISSUE_GROUP_ATTRIBUTE,
  ISSUE_LINK_ATTRIBUTE,
  ISSUE_ROW_ATTRIBUTE,
  type Item,
  matchesFilters,
  matchesSearch,
  PAGE_SIZES,
  readUrl,
  tableStorageKey,
  UNCLAIMED_FACET,
  type View,
  viewLayoutColumns,
  writeUrl,
} from "./issues-table-model";
import { IssueTableSelection, IssueTableStatus, IssueTableToolbar } from "./issues-table-page";
import { GroupRow, IssueRow } from "./issues-table-rows";
import { useTableView } from "./issues-table-view";
import {
  type ActiveView,
  BUILT_IN_VIEWS,
  type BuiltInView,
  builtInAddress,
  clearNarrowings,
  currentAddress,
  expandView,
  isBoardFedColumn,
  isSavedViewChanged,
  isViewChanged,
  matchSavedView,
  narrowingsDiffer,
  readCols,
  readWindow,
  type SavedView,
  type SavedViews,
  savedViewQuery,
  shownColumns,
  useSavedViews,
  useViewLayout,
  type ViewLayout,
  viewColumns,
  viewFor,
  viewLayoutKey,
  viewsGrammar,
} from "./issues-views";
import { issueIsUnread } from "./UnreadDot";

const EMPTY_FACETS: ReadonlyMap<string, number> = new Map();
/** Status offers Done before the closed issues it would count are read. */
const DONE_UNLOADED: ReadonlyMap<string, readonly string[]> = new Map([["status", ["done"]]]);
/** The one layout key of every table scoped to a priority: the work advancing a priority is
 *  one table, drawn under an opened row of the priorities page and on the priority's own page,
 *  so the columns a reader drags there are dragged once rather than once per priority. */
const SCOPED_LAYOUT_KEY = "priority-work";

const fuzzy: FilterFn<Item> = (row, _id, query: string) => matchesSearch(row.original, query);

const ACTION_WIDTH = 104;

/** The checkbox a row draws for its selection (`issues-table-columns.tsx`'s select column), the
 *  one checkbox every row has. */
const ROW_CHECKBOX = 'input[type="checkbox"]';

/** What an answer the server gave no words for is called in an alert. */
const NO_ANSWER = "Dispatch did not answer.";

/** The List's own words, which the table core reads and never invents: the attributes its rows
 *  and groups carry, the link opening a row follows, and the keys that walk it, under the ids
 *  and labels the palette and the shortcut help have always shown. */
const ISSUE_TABLE_NAMES: DataTableNames = {
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

/** The same words for an issue table a page draws beside its main table over the same keys - the
 *  priorities page's fault lists, and the work under an opened priority - whose `j` and `k` walk
 *  it only from one of its own rows (`moveFrom: "row"`), so they never take those keys from the
 *  main table. */
const ISSUE_TABLE_BESIDE_NAMES: DataTableNames = {
  ...ISSUE_TABLE_NAMES,
  keymap: { ...ISSUE_TABLE_NAMES.keymap, moveFrom: "row" },
};

/** What the List's rows are, which its header filters count in and its pager counts: issues, in
 *  groups, over rows. */
const ISSUE_TABLE_NOUN: RowNoun = {
  group: { one: "group", many: "groups" },
  line: { one: "row", many: "rows" },
  many: "issues",
  one: "issue",
};

/** The line under the List: its page sizes, the one that means every row, and its test id. */
const ISSUE_TABLE_PAGER: PagerOptions = {
  allRows: ALL_ROWS,
  pageSizes: PAGE_SIZES,
  testId: "issues-table-pager",
};

/** The Issues tab's table in the address: the project table's own spelling, with the view it
 *  names between that spelling and the address (`viewsGrammar`). A module constant, since
 *  `useTableAddress` memoizes what it reads on the grammar's identity. */
const PROJECT_GRAMMAR = viewsGrammar<View>({
  read: (params) => readUrl(params),
  write: (params, view) => writeUrl(params, view),
});

/** The columns whose cells read the Legion facts (`LegionFacts`): a filter, a sort or a grouping
 *  of one of them waits for the read of those facts. */
const FACTS_COLUMNS: readonly string[] = ["work_hours", "phase_hours", "review_rounds", "legion"];

/** What Open's own address asks of its rows, which Open's tab counts by on another view. */
const OPEN_ADDRESS = builtInAddress("open");

/** The rows of `items` that the view at `query` lists, as the view's tab counts them: its filters,
 *  its search, the issues a link names, and the Needs you and Unread flags. */
function countOf(
  items: readonly Item[],
  query: URLSearchParams,
  userState: UserState | undefined
): number {
  const own = readUrl(expandView(query));
  const needsYou = query.get("needs-you") === "1";
  const unread = query.get("unread") === "1";
  return items.filter(
    (item) =>
      matchesFilters(item, own.filters) &&
      matchesSearch(item, own.search) &&
      (own.selection === null || own.selection.keys.has(item.issue.key)) &&
      (!needsYou || item.issue.open_asks > 0) &&
      (!unread || issueIsUnread(item.issue, userState?.[item.issue.key]?.last_read_seq ?? 0))
  ).length;
}

/** Whether the view at `query` lists closed issues: its Status filter picks Done, or it picks no
 *  status (`nofilter=1`). The open listing cannot count such a view. */
function listsClosed(query: URLSearchParams): boolean {
  const expanded = expandView(query);
  const statuses = expanded.getAll("status");
  return statuses.includes("done") || (statuses.length === 0 && expanded.get("nofilter") === "1");
}

/**
 * What the Issues tab gives its table beyond a listing: the project's daemon, the view the tabs
 * have open and the controls that change it. Another page's table (the priority pages') has
 * none of it: no view tabs, no Legion column, no Board.
 */
interface IssuesTabPlace {
  readonly project: string;
  /** The project names a Legion daemon, as its daemon setting last answered. */
  readonly hasLegion: boolean;
  /** The daemon setting's read failed with no answer: the project is drawn with no daemon. */
  readonly daemonFailure: ReadFailure | undefined;
  /** The built-in view the address opens, which a saved view narrows. */
  readonly base: BuiltInView;
  readonly active: ActiveView;
  readonly changed: boolean;
  /** The address the open view opens at: its built-in address, or its saved query. */
  readonly ownAddress: URLSearchParams;
  readonly saved: SavedViews;
  /** Marks a saved view as the one the reader opened, or none. */
  readonly choose: (name: string | undefined) => void;
  readonly layout: ViewLayout;
  readonly setLayout: (layout: ViewLayout) => void;
  readonly showIcebox: boolean;
  readonly setShowIcebox: (shown: boolean) => void;
  readonly lanes: boolean;
  readonly setLanes: (shown: boolean) => void;
  /** The page of Done's own read whose `total` counts Done's tab, once Done has been opened. */
  readonly donePage: DonePageRead | undefined;
  readonly rememberDonePage: (page: DonePageRead) => void;
}

/** One page of Done read with no window: what Done's tab counts by. */
interface DonePageRead {
  readonly offset: number;
  readonly size: number;
}

export function IssuesTable({
  label,
  layout,
  listing,
  moveFrom = "page",
  priorityTitles,
  stateKey,
}: {
  /** What this list is called, for a reader who cannot see where it sits; by its listing when
   *  unset: a project's issues, or the issues advancing one priority. */
  label?: string;
  /** The layout this table keeps, under its own key, where it is not its listing's, and the
   *  columns it opens with shown: each fault list of the priorities page shows its listing
   *  columns and Updated, which a table opens with hidden (`issueTableLayout`). */
  layout?: { readonly key: string; readonly shown: readonly ShownColumnId[] };
  /** What this table lists: one project's issues, or the issues advancing one priority, which
   *  belong to no project (`issues-table-data.ts`). */
  listing: IssueListing;
  /** Where its `j` and `k` walk its rows from (`RowKeymap.moveFrom`): anywhere on the page the
   *  focus is in no other table, for the page's one table or its main one (the default); or only
   *  from one of its own rows, for a table drawn beside the page's main one. */
  moveFrom?: "page" | "row";
  /** Each priority's title by its id. Given, the table has the Advances column, which names each
   *  priority a row advances by its title; unset, it has none (`listingColumns`). */
  priorityTitles?: ReadonlyMap<string, string>;
  /** The prefix this table's own view takes in the address: `<stateKey>.sort` and the rest.
   *  Unset on a project's listing, the table is the project's Issues tab, with its views. */
  stateKey?: string;
}): ReactNode {
  if ("project" in listing && stateKey === undefined)
    return <IssuesTab key={listing.project} label={label} project={listing.project} />;
  return (
    <ListingTable
      label={label}
      layout={layout}
      listing={listing}
      moveFrom={moveFrom}
      priorityTitles={priorityTitles}
      stateKey={stateKey}
    />
  );
}

/** An issue table another page draws over a listing of its own, as the List was drawn before the
 *  views: today's columns at today's widths, its view under its own prefix. */
function ListingTable({
  label,
  layout: ownLayout,
  listing,
  moveFrom,
  priorityTitles,
  stateKey,
}: {
  label: string | undefined;
  layout: { readonly key: string; readonly shown: readonly ShownColumnId[] } | undefined;
  listing: IssueListing;
  moveFrom: "page" | "row";
  priorityTitles: ReadonlyMap<string, string> | undefined;
  stateKey: string | undefined;
}): ReactNode {
  const address = useTableView(stateKey);
  // The listing columns this table has: the ones its listing gives a use (`listingColumns`).
  const columnsLayout = issueTableLayout(
    listingColumns(listing, priorityTitles !== undefined),
    ownLayout?.shown
  );
  // One stored layout per project, and one for every scoped table: the work advancing a
  // priority is one table drawn in two places, so a reader who drags a column there drags it
  // for the priorities page and the priority's own page alike, and not once per priority.
  const layout = useTableLayout(
    tableStorageKey(ownLayout?.key ?? ("project" in listing ? listing.project : SCOPED_LAYOUT_KEY)),
    columnsLayout
  );
  return (
    <IssueTableBody
      address={address}
      columnsLayout={columnsLayout}
      layout={layout}
      listing={listing}
      moveFrom={moveFrom}
      name={label ?? ("project" in listing ? "Project issues" : "Priority issues")}
      priorityTitles={priorityTitles}
      stateKey={stateKey}
    />
  );
}

/**
 * The project's Issues tab: its one list of issues, in views (Open, Done and the reader's saved
 * ones), in the table or the Board. It reads whether the project names a Legion daemon first,
 * and shows the loading state until that read answers; a failed read draws the project as one
 * with no daemon, with the error under the tabs, and a later failed read keeps the answer the
 * page drew.
 */
function IssuesTab({ label, project }: { label: string | undefined; project: string }): ReactNode {
  const daemon = useQuery(legionDaemonQuery(project));
  const params = useAddressParams();
  const update = useAddressUpdate();
  // An older address in today's spelling, written over the address the page opened on, so the
  // tab shows its mark and Clear filters takes what it added away.
  const rewritten = currentAddress(params)?.toString();
  useEffect(() => {
    if (rewritten !== undefined) update(() => copyAddress(rewritten));
  }, [rewritten, update]);
  const saved = useSavedViews(project);
  // The saved view the reader opened by its tab, which stays the open tab while they change it.
  const [chosen, setChosen] = useState<string | undefined>(undefined);
  const savedView =
    saved.views.find((view) => view.name === chosen) ?? matchSavedView(saved.views, params);
  const base = viewFor(expandView(params).get("view"));
  const active: ActiveView =
    savedView === undefined
      ? { kind: "built-in", id: base.id }
      : { kind: "saved", name: savedView.name };
  const tableKey = tableStorageKey(project);
  const [layout, setLayout] = useViewLayout(tableKey, active);
  const [showIcebox, setShowIcebox] = useUserPreference(
    "project.board-edges",
    (stored) => stored === "shown",
    (shown) => (shown ? "shown" : "hidden")
  );
  const [lanes, setLanes] = useUserPreference(
    "project.board-lanes",
    (stored) => stored === "shown",
    (shown) => (shown ? "shown" : "hidden")
  );
  useKeymap("project", [
    {
      id: "toggle-view",
      keys: "v",
      label: "Toggle Table / Board",
      run: () => setLayout(layout === "board" ? "table" : "board"),
    },
  ]);
  const [donePage, setDonePage] = useState<DonePageRead | undefined>(undefined);
  const rememberDonePage = useCallback(
    (page: DonePageRead) =>
      setDonePage((held) =>
        held?.offset === page.offset && held.size === page.size ? held : page
      ),
    []
  );
  if (daemon.isPending) return <LoadingSkeleton label="Loading issues" />;
  const hasLegion = daemon.data !== undefined && daemon.data !== null;
  const daemonFailure: ReadFailure | undefined =
    daemon.isError && daemon.data === undefined
      ? {
          message: apiErrorMessage(daemon.error, NO_ANSWER),
          retry: () => void daemon.refetch(),
          retrying: daemon.isFetching,
        }
      : undefined;
  const tab: IssuesTabPlace = {
    project,
    hasLegion,
    daemonFailure,
    base,
    active,
    changed:
      savedView === undefined ? isViewChanged(params) : isSavedViewChanged(params, savedView),
    ownAddress: savedView === undefined ? builtInAddress(base.id) : copyAddress(savedView.query),
    saved,
    choose: setChosen,
    layout,
    setLayout,
    showIcebox,
    setShowIcebox,
    lanes,
    setLanes,
    donePage,
    rememberDonePage,
  };
  // One table per view's stored layout, and per daemon answer: the columns a view opens with,
  // and the key its widths and order are kept under, are read once as the table mounts.
  return (
    <IssuesTabTable
      key={`${viewLayoutKey(tableKey, base.id)}|${hasLegion}`}
      name={label ?? "Project issues"}
      tab={tab}
    />
  );
}

/**
 * The table of one view of the Issues tab, laid out by the view's columns: the address's `cols=`
 * for this load, else the view's own, with no board-fed column in a project with no daemon.
 * Its widths and order are kept under the view's key (`viewLayoutKey`); its column visibility
 * too, except while `cols=` is in the address, which holds for that load and writes nothing: a
 * change of columns then writes `cols=`. Until the reader drags a width, the columns are fitted
 * to the box (`fitViewWidths`); on a phone Title follows Select and Key.
 */
function IssuesTabTable({ name, tab }: { name: string; tab: IssuesTabPlace }): ReactNode {
  const address = useTableAddress(PROJECT_GRAMMAR);
  const update = useAddressUpdate();
  const { params } = address;
  const { base, hasLegion } = tab;
  // Below 1280 px every button is a 44 px touch target, and each column's minimum holds its
  // header with that filter button.
  const touch = useMediaQuery(COMPACT_VIEWPORT_QUERY);
  const cols = readCols(params);
  const rules = viewLayoutColumns(shownColumns(params, base, hasLegion), touch);
  // What the stored layout keeps hidden until the reader shows it, whatever the address and the
  // daemon setting say now: the view's own columns in a project with a daemon. So a load with
  // `cols=`, or one drawn with no daemon while the setting's read is out, writes back none of the
  // columns it hid for that load alone.
  const ownRules = viewLayoutColumns(viewColumns(base, true), touch);
  const storedRules = useMemo<LayoutColumns>(
    () => ({ ...rules, hiddenByDefault: ownRules.hiddenByDefault }),
    [rules, ownRules]
  );
  const stored = useTableLayout(viewLayoutKey(tableStorageKey(tab.project), base.id), storedRules);
  const shown = cols === undefined ? undefined : new Set(["select", ...cols]);
  // No board-fed column shows in a project with no daemon, whatever a reader stored while one
  // ran, and Queue only once the reader shows it.
  const unfed: VisibilityState = hasLegion
    ? {}
    : {
        queue: shown?.has("queue") ?? stored.visibility.queue ?? false,
        ...Object.fromEntries(rules.ids.filter(isBoardFedColumn).map((id) => [id, false])),
      };
  const visibility: VisibilityState =
    shown === undefined
      ? { ...stored.visibility, ...unfed }
      : { ...Object.fromEntries(rules.ids.map((id) => [id, shown.has(id)])), ...unfed };
  const drawn = stored.order.length > 0 ? stored.order : rules.ids;
  const phone = stored.boxWidth < 640;
  const pinnedOnPhone = ["select", "key", "title"];
  const order = phone
    ? [...pinnedOnPhone, ...drawn.filter((id) => !pinnedOnPhone.includes(id))]
    : stored.order;
  const writeCols: TableLayoutModel["setVisibility"] = (next) => {
    const resolved = typeof next === "function" ? next(visibility) : next;
    const ids = rules.ids.filter((id) => id !== "select" && resolved[id] !== false);
    update((current) => {
      current.set("cols", ids.join(","));
      return current;
    });
  };
  const layout: TableLayoutModel = {
    ...stored,
    order,
    responsive:
      Object.keys(stored.sizing).length > 0 || phone
        ? stored.responsive
        : {
            ...stored.responsive,
            sizing: fitViewWidths(
              drawn.filter((id) => visibility[id] !== false),
              stored.responsive.sizing,
              columnMinimums(touch),
              stored.boxWidth,
              ACTION_WIDTH
            ),
          },
    visibility,
    ...(shown === undefined ? {} : { setVisibility: writeCols }),
  };
  return (
    <IssueTableBody
      address={address}
      columnsLayout={rules}
      layout={layout}
      listing={{ project: tab.project }}
      moveFrom="page"
      name={name}
      priorityTitles={undefined}
      stateKey={undefined}
      tab={tab}
    />
  );
}

function IssueTableBody({
  address,
  columnsLayout,
  layout,
  listing,
  moveFrom,
  name,
  priorityTitles,
  stateKey,
  tab,
}: {
  address: TableAddress<View>;
  /** The columns the table has, their widths and their minimums. */
  columnsLayout: LayoutColumns;
  layout: TableLayoutModel;
  listing: IssueListing;
  moveFrom: "page" | "row";
  /** What this list is called, for a reader who cannot see where it sits. */
  name: string;
  priorityTitles: ReadonlyMap<string, string> | undefined;
  stateKey: string | undefined;
  /** The Issues tab's own parts; unset for a table another page draws. */
  tab?: IssuesTabPlace;
}): ReactNode {
  const project = "project" in listing ? listing.project : undefined;
  // Whether the listing's one open read is every row it holds, so the table reads no closed
  // issue beside it (`answeredWhole`).
  const whole = answeredWhole(listing);
  const { params, view, setFilters, setGrouping, setPagination, setSearch, setSorting } = address;
  const pageAddress = usePageAddress();
  const navigate = useAddressNavigate();
  const update = useAddressUpdate();
  const { sorting, grouping, filters, search } = view;
  const pagination = { pageIndex: view.page, pageSize: view.pageSize };
  const { attachAbove, boxWidth } = layout;
  const flags = useIssueFilters();
  const userState = useQuery(userStateQuery());
  const viewer = useQuery(whoAmIQuery()).data?.login;
  const [selection, setSelection] = useState<RowSelectionState>({});
  // The issue the reader marked last - by its own checkbox, or `x` on its row - and the List's
  // search box: where the focus goes when the bar goes with it (`returnFocus`, below). A
  // select-all marks no row of the reader's own, so it records none.
  const lastMarked = useRef<string | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const recordMark = useCallback((key: string) => {
    lastMarked.current = key;
  }, []);
  const [openFilter, setOpenFilter] = useState<string>();

  // What the view reads (the spec's Loading section). A project's view reads the open listing
  // unless its Status filter picks Done alone, and the done issues where it picks Done or no
  // status: one page of them while it is sorted by Closed, newest first, and narrowed by nothing
  // but a Closed window, and every done issue the window holds otherwise. A listing that
  // advances a priority reads its whole listing once closed issues are let through.
  const statusValue = filters.find((filter) => filter.id === "status")?.value;
  const statusPicks = Array.isArray(statusValue) ? statusValue.map(String) : [];
  const listsDone = !whole && (statusPicks.length === 0 || statusPicks.includes("done"));
  const readsOpen =
    project === undefined || statusPicks.length === 0 || statusPicks.some((s) => s !== "done");
  const closedValue = filters.find((filter) => filter.id === "closed")?.value;
  const closed = typeof closedValue === "string" ? closedValue : undefined;
  // The clock a window is read against, held while the Closed filter stays, so a rolling
  // window's page read keeps its key until the reader changes the window.
  const [clock, setClock] = useState(() => ({ closed, now: Date.now() }));
  if (clock.closed !== closed) setClock({ closed, now: Date.now() });
  const nowMs = clock.closed === closed ? clock.now : Date.now();
  const closedWindow = project === undefined ? undefined : readWindow(closed, nowMs);
  const firstSort = sorting[0];
  const pageRead =
    tab !== undefined &&
    tab.layout === "table" &&
    statusPicks.length === 1 &&
    statusPicks[0] === "done" &&
    sorting.length === 1 &&
    firstSort?.id === "closed" &&
    firstSort.desc &&
    search === "" &&
    grouping.length === 0 &&
    pagination.pageSize !== ALL_ROWS &&
    !flags.needsYou &&
    !flags.unread &&
    view.selection === null &&
    filters.every((filter) => filter.id === "status" || filter.id === "closed");
  const windowFrom = closedWindow?.from;
  const windowTo = closedWindow?.to;
  const windowRolling = closedWindow?.rolling;
  // The page on screen, for a view whose done rows are read a page at a time.
  const offset = pageRead ? pagination.pageIndex * pagination.pageSize : 0;
  const size = pageRead ? pagination.pageSize : 0;
  const done = useMemo<DoneRead | undefined>(() => {
    if (!listsDone) return undefined;
    const held =
      windowFrom === undefined || windowTo === undefined || windowRolling === undefined
        ? undefined
        : { from: windowFrom, to: windowTo, rolling: windowRolling };
    return pageRead
      ? { kind: "page", window: held, offset, size }
      : { kind: "all", window: held, nowMs: clock.now };
  }, [listsDone, pageRead, windowFrom, windowTo, windowRolling, offset, size, clock.now]);
  const claimPicked = (filters.find((filter) => filter.id === "claim")?.value ?? []) as string[];
  // Which claims have lapsed is the agent registry's answer, read only while the Claimed by
  // filter asks for it: because it is picked, or because its panel is open and counting.
  const wantsFree = claimPicked.includes(UNCLAIMED_FACET);
  const registry = useAgentRegistry(wantsFree || openFilter === "claim");
  const hasLegion = tab?.hasLegion ?? false;
  const {
    all,
    allItems,
    board,
    doneLoaded,
    doneRead,
    doneTotal,
    issues,
    items: factsRead,
    itemsStatus,
    listingError,
  } = useIssueTableItems(listing, {
    reads: { open: readsOpen, done, legion: hasLegion },
    registry,
    viewer,
  });
  // A view with a Legion filter, sort or grouping waits for the board, and one whose filter,
  // sort or grouping reads facts waits for the facts of its done rows too: it never lists rows,
  // or a count, from a read that has not answered.
  const reading = [...filters.map(({ id }) => id), ...sorting.map(({ id }) => id), ...grouping];
  const boardWaits =
    hasLegion &&
    (filters.some(({ id }) => isBoardFedColumn(id) || id === "queue") ||
      reading.some(isBoardFedColumn));
  const factsWait =
    hasLegion && done !== undefined && reading.some((id) => FACTS_COLUMNS.includes(id));
  const openPending = readsOpen && issues.isPending;
  const loading =
    openPending ||
    userState.isPending ||
    (!readsOpen && done !== undefined && doneRead.isPending) ||
    (boardWaits && board.isPending) ||
    (factsWait && itemsStatus === "pending");
  const failed =
    !loading &&
    ((readsOpen && issues.isError && issues.data === undefined) ||
      (!readsOpen && done !== undefined && doneRead.isError && doneRead.data === undefined) ||
      userState.isError);
  const legionFailed =
    !loading &&
    !failed &&
    ((boardWaits && board.isError && board.data === undefined) ||
      (factsWait && itemsStatus === "error"));
  const loaded = !loading && !failed && !legionFailed;
  // The issues an address names with `only=`, which the table shows alone until its chip is
  // removed. The narrowing is the reader's, so it outranks nothing: every other filter still
  // applies to what is left.
  const picked = view.selection;
  const items = useMemo(
    () =>
      allItems.filter(
        ({ issue }) =>
          (picked === null || picked.keys.has(issue.key)) &&
          (!flags.needsYou || issue.open_asks > 0) &&
          (!flags.unread || issueIsUnread(issue, userState.data?.[issue.key]?.last_read_seq ?? 0))
      ),
    [allItems, flags.needsYou, flags.unread, picked, userState.data]
  );
  // What the table is narrowed to, and the way back to every row: removing the chip clears
  // `only=` and leaves every other control where the reader set it. It is a chip of the control
  // row, beside the flags' and the columns', drawn by the same `filterChips` and named as they
  // are, `Remove Showing: <title> filter`, and its removal leaves a history entry, so Back gives
  // the narrowing back (`useAddressNavigate`'s `replace: false`). The toolbar is drawn in every
  // state, so the chip counts what it shows only once the rows the table waits on are in: not
  // beside the loading skeleton, not after a failed read, and not while the closed issues it
  // asked for are still out, where it would count rows the table has not read. Until then it
  // names its narrowing alone.
  const counted = loaded && listingError === undefined && (done === undefined || doneLoaded);
  const pickedChip: FilterChip | undefined =
    picked === null
      ? undefined
      : {
          key: "only",
          label: `Showing: ${picked.label}`,
          text: counted
            ? `Showing: ${picked.label} · ${items.length} of ${picked.keys.size} ${picked.keys.size === 1 ? "issue" : "issues"}`
            : undefined,
          remove: () =>
            navigate(
              addressPath(
                pageAddress.pathname,
                clearPicked(copyAddress(pageAddress.search)),
                pageAddress.hash
              ),
              { replace: false }
            ),
        };

  const columns = useIssueTableColumns(
    userState.data,
    boxWidth < 640,
    recordMark,
    columnsLayout,
    priorityTitles,
    tab === undefined ? undefined : { hasLegion }
  );

  const model = useDataTable<Item>({
    rows: items,
    columns,
    getRowId: (item) => item.issue.key,
    layout,
    names: moveFrom === "row" ? ISSUE_TABLE_BESIDE_NAMES : ISSUE_TABLE_NAMES,
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
    // Done's page is the server's: the table pages by the count of every row its window holds.
    ...(doneTotal === undefined ? {} : { serverPage: { rowCount: doneTotal } }),
  });
  const { table } = model;
  // The bar gone with the focus (`SelectionBar`): the checkbox of the issue the reader marked
  // last, else the first row's, else the search box once no row is listed. A row the window has
  // not drawn is scrolled to and takes the focus as it lands (`focusRow`).
  const returnFocus = () => {
    const leaves = model.displayRows.flatMap(({ row }) => (row.getIsGrouped() ? [] : [row.id]));
    const last = lastMarked.current;
    const key = last !== null && leaves.includes(last) ? last : leaves[0];
    return key === undefined ? searchRef.current : model.focusRow(key, ROW_CHECKBOX);
  };
  // A control of the table's head - a column filter, or the list it opened in a portal of its
  // own - goes with the table when a press in it sends the List back to its skeleton, and the
  // focus it held goes to the search box rather than the page's body. A notice's button under
  // the toolbar goes with the notice it answers - the interrupted action's Finish and Dismiss -
  // and its focus goes to the control now at its place among the notices, else the one before
  // it, else the search box. Only the head and the notices are counted: the selection bar, the
  // rows and the dialogs each hand the focus on themselves. The handler sits on the List's
  // section, which stays in every branch.
  const sectionRef = useRef<HTMLElement>(null);
  const noticesRef = useRef<HTMLDivElement>(null);
  const noteFocus = useFocusPlace(
    (element) => {
      const section = sectionRef.current;
      if (section === null) return null;
      // A portal's element is counted only where a control of the head was before it.
      if (!section.contains(element)) return section.querySelector("thead");
      const notices = noticesRef.current;
      return notices?.contains(element) ? notices : element.closest("thead");
    },
    () => searchRef.current
  );

  // Whether every listing the view reads has answered.
  const listingIn =
    (!readsOpen || issues.data !== undefined) &&
    (done === undefined || doneRead.data !== undefined);
  // The keys the table lists under the filters and the search it is showing, in row order. A key
  // it no longer lists drops out of the selection - another filter, another project - so a row
  // that is listed again comes back unselected; this is the rule `hooks/useMarkedKeys.ts` holds
  // the Inbox's marks to, written against the table's own `rowSelection` rather than a second
  // selection beside it. Joined into a string, so the pruning runs when the keys change rather
  // than on every render that rebuilds the rows, and `undefined` while the listing is still out,
  // which prunes nothing.
  const listedRows = table.getFilteredRowModel().rows;
  const listedKeys = !listingIn ? undefined : listedRows.map((row) => row.id).join(" ");
  useEffect(() => {
    if (listedKeys === undefined) return;
    const listed = new Set(listedKeys === "" ? [] : listedKeys.split(" "));
    setSelection((current) =>
      Object.keys(current).every((key) => listed.has(key))
        ? current
        : Object.fromEntries(Object.entries(current).filter(([key]) => listed.has(key)))
    );
  }, [listedKeys]);

  // What the bar says it will act on when it opens over this table: the issues the reader has
  // selected, in the order the table lists them. It belongs to the table while it is mounted,
  // so leaving the page - the Board, another project, anywhere - clears it again.
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
    if (listingIn && pagination.pageIndex >= Math.max(pageCount, 1))
      table.setPageIndex(Math.max(pageCount - 1, 0));
  }, [listingIn, pagination.pageIndex, pageCount, table]);
  const openFacets = useMemo(
    () => (openFilter ? facetCounts(items, openFilter, filters, search) : EMPTY_FACETS),
    [items, openFilter, filters, search]
  );

  const visibleColumns = table.getVisibleLeafColumns().length + 1;
  // A column filter's panel hangs off its header and goes with the table: it is closed then, as
  // a panel whose anchor has left the page closes (`AnchoredPanel`), so the table coming back
  // does not open it again and take the focus from wherever it went meanwhile.
  useEffect(() => {
    if (!loaded) setOpenFilter(undefined);
  }, [loaded]);

  // The tab counts. Open counts from the open listing, and on a view that reads none (Done) from
  // the `total` of a one-row page of it. Done counts by the `total` of its own page read, once
  // Done has been opened in the page: the same read, kept while another view is open. A saved
  // view counts from the open listing, and one that lists closed issues only while it is open.
  // The open view's tab counts the rows the table lists now, the reader's changes included.
  const active = tab?.active;
  const onDone = active?.kind === "built-in" && active.id === "done";
  const openCount = useQuery({
    ...projectOpenCountQuery(project ?? ""),
    enabled: tab !== undefined && !readsOpen,
  });
  const rememberDonePage = tab?.rememberDonePage;
  useEffect(() => {
    if (onDone && done?.kind === "page" && done.window === undefined)
      rememberDonePage?.({ offset: done.offset, size: done.size });
  }, [onDone, done, rememberDonePage]);
  const donePage = tab?.donePage;
  const doneCount = useQuery({
    ...projectDonePageQuery(project ?? "", {
      offset: donePage?.offset ?? 0,
      size: donePage?.size ?? 0,
    }),
    // Done's own read, held: leaving Done reads it no more until a live event does.
    refetchOnMount: false,
    enabled: tab !== undefined && donePage !== undefined && !onDone,
  });
  const openIn = readsOpen && issues.data !== undefined;
  const savedViews = tab?.saved.views;
  const counts = useMemo(() => {
    const built = new Map<string, number | undefined>([
      ["open", openIn ? countOf(allItems, OPEN_ADDRESS, userState.data) : openCount.data],
      ["done", doneCount.data?.total],
    ]);
    const savedCounts = new Map<string, number | undefined>(
      (savedViews ?? []).map((saved: SavedView) => {
        const query = copyAddress(saved.query);
        return [
          saved.name,
          openIn && !listsClosed(query) ? countOf(allItems, query, userState.data) : undefined,
        ];
      })
    );
    return { built, savedCounts };
  }, [allItems, doneCount.data, openCount.data, openIn, savedViews, userState.data]);
  const listedCount = counted ? (doneTotal ?? listedRows.length) : undefined;

  const header = board.data?.header;
  const tabs =
    tab === undefined ? null : (
      <IssueViewTabs
        active={tab.active}
        boardFailure={
          hasLegion && board.isError
            ? {
                message: apiErrorMessage(board.error, NO_ANSWER),
                retry: () => void board.refetch(),
                retrying: board.isFetching,
              }
            : undefined
        }
        changed={tab.changed}
        counts={
          active?.kind === "built-in"
            ? new Map(counts.built).set(active.id, listedCount)
            : counts.built
        }
        daemonFailure={tab.daemonFailure}
        daemonReadError={
          hasLegion && header !== undefined && header.read_at !== null && !header.read_ok
            ? (header.read_error ?? "")
            : undefined
        }
        factsFailure={
          hasLegion && factsRead.isError
            ? {
                message: apiErrorMessage(factsRead.error, NO_ANSWER),
                retry: () => void factsRead.refetch(),
                retrying: factsRead.isFetching,
              }
            : undefined
        }
        onDelete={(saved) => {
          tab.saved.remove(saved);
          tab.choose(undefined);
        }}
        onSelect={(id) => {
          tab.choose(undefined);
          update(() => builtInAddress(id));
        }}
        onSelectSaved={(saved) => {
          const entry = tab.saved.views.find((one) => one.name === saved);
          if (entry === undefined) return;
          tab.choose(entry.name);
          update(() => copyAddress(entry.query));
        }}
        saved={tab.saved.views}
        savedCounts={
          active?.kind === "saved"
            ? new Map(counts.savedCounts).set(active.name, listedCount)
            : counts.savedCounts
        }
        views={BUILT_IN_VIEWS}
      />
    );
  // Save view, in the control row after the Columns menu: the view as the address has it, with
  // the columns it shows and its layout, under a name the reader gives. The (i) hint follows it.
  const viewControls =
    tab === undefined ? null : (
      <ViewControls
        changed={tab.changed}
        check={tab.saved.check}
        initialName={active?.kind === "saved" ? active.name : ""}
        onSave={(saving) => {
          const query = savedViewQuery(params, {
            cols: table.getVisibleLeafColumns().map((column) => column.id),
            layout: tab.layout,
          });
          const answer = tab.saved.save(saving, query);
          if (answer.kind === "problem") return;
          tab.choose(answer.name);
          update(() => copyAddress(query));
        }}
      />
    );
  const layoutSwitch =
    tab === undefined ? null : <LayoutSwitch layout={tab.layout} onLayout={tab.setLayout} />;

  if (tab !== undefined && tab.layout === "board")
    // The Board of the view, as the project's Board draws it, with the view's controls in its
    // control row.
    return (
      <section aria-label={name} className="space-y-3 sm:pb-24">
        {tabs}
        <IssueBoard
          controls={
            <>
              {layoutSwitch}
              <ToggleButton onChange={tab.setShowIcebox} pressed={tab.showIcebox}>
                Show Icebox
              </ToggleButton>
              <ToggleButton onChange={tab.setLanes} pressed={tab.lanes}>
                Lanes
              </ToggleButton>
              {viewControls}
            </>
          }
          lanes={tab.lanes}
          project={tab.project}
          showEdges={tab.showIcebox}
        />
      </section>
    );

  // The toolbar is drawn in every branch, at the one place in the tree, as the Board draws its
  // `controls`: a filter press that sends the List off to read issues it has not loaded - Clear
  // filters back to the open issues, which a List opened on the closed ones has never read -
  // puts the skeleton in place of the table alone, and the control pressed, with the keyboard
  // focus on it, stays where it was. Everything else here needs the listing and waits for it.
  return (
    <section aria-label={name} className="space-y-3 sm:pb-24" onFocus={noteFocus} ref={sectionRef}>
      <div className="space-y-3" ref={attachAbove}>
        {tabs}
        {loaded ? (
          <>
            {listingError ? (
              <p className={dangerText} role="alert">
                Could not load {done !== undefined ? "closed " : ""}issues.{" "}
                <button
                  type="button"
                  aria-label={
                    done !== undefined ? "Retry loading closed issues" : "Retry loading issues"
                  }
                  onClick={() => void listingError.refetch()}
                >
                  Retry
                </button>
              </p>
            ) : null}
            {done !== undefined && readsOpen && doneRead.isPending ? (
              <p aria-live="polite" className={textMutedOnCanvas}>
                Loading closed issues…
              </p>
            ) : null}
            {/* Issues by status: a click shows only that status, a second click every status. */}
            <IssueTableStatus table={table} items={allItems} />
          </>
        ) : null}

        <IssueTableToolbar
          clear={
            tab === undefined
              ? undefined
              : {
                  on: narrowingsDiffer(params, tab.ownAddress),
                  run: () => update((current) => clearNarrowings(current, tab.ownAddress)),
                }
          }
          controls={
            tab === undefined ? undefined : (
              <>
                {layoutSwitch}
                <ColumnsMenu columns={table.getAllLeafColumns()} size="compact" />
                {viewControls}
              </>
            )
          }
          displayLabel={tab === undefined ? undefined : "Group by"}
          ownStatuses={tab?.base.statuses}
          searchRef={searchRef}
          selection={pickedChip}
          stateKey={stateKey}
          table={table}
          unclaimedUnchecked={
            wantsFree && registry.isError
              ? registry.refilling
                ? "refilling"
                : "unreachable"
              : undefined
          }
        />

        {loaded ? (
          <div className="space-y-3" ref={noticesRef}>
            <p
              aria-live="polite"
              role={notice ? (notice.kind === "error" ? "alert" : "status") : undefined}
              className={`min-h-5 text-sm ${notice?.kind === "error" ? dangerText : textSecondaryOnCanvas}`}
            >
              {notice?.text}
            </p>
            {interrupted ? (
              <section aria-label="Interrupted table action" className={textSecondaryOnCanvas}>
                <p>{interruptedDescription}</p>
                <button
                  type="button"
                  onClick={finish}
                  className={`min-h-11 rounded-lg border px-3 text-sm ${borderDefault} ${focusVisibleRing}`}
                >
                  Finish
                </button>{" "}
                <button
                  type="button"
                  onClick={dismiss}
                  className={`min-h-11 rounded-lg border px-3 text-sm ${borderDefault} ${focusVisibleRing}`}
                >
                  Dismiss
                </button>
              </section>
            ) : null}
          </div>
        ) : null}
      </div>

      {loading ? <LoadingSkeleton label="Loading issues" /> : null}
      {failed ? (
        <p className={dangerText} role="alert">
          Could not load issues.{" "}
          <button
            type="button"
            onClick={() => {
              if (readsOpen) void issues.refetch();
              else void doneRead.refetch();
              void userState.refetch();
            }}
          >
            Retry loading issues
          </button>
        </p>
      ) : null}
      {legionFailed ? (
        boardWaits && board.isError ? (
          <QueryError
            message="Could not read the Legion board, which this view needs."
            onRetry={() => void board.refetch()}
            retrying={board.isFetching}
          />
        ) : (
          <QueryError
            message="Could not read the Legion facts, which this view needs."
            onRetry={() => void factsRead.refetch()}
            retrying={factsRead.isFetching}
          />
        )
      ) : null}
      {loaded ? (
        <>
          <DataTable
            empty="No issues match these filters."
            filterCounts={openFacets}
            filterUnloaded={doneLoaded || whole ? undefined : DONE_UNLOADED}
            label={`${name} table`}
            model={model}
            noun={ISSUE_TABLE_NOUN}
            onOpenFilterChange={setOpenFilter}
            openFilter={openFilter}
            pager={ISSUE_TABLE_PAGER}
            renderGroup={({ entry, index, measure }) => (
              <GroupRow
                columns={visibleColumns}
                viewportWidth={boxWidth}
                continued={entry.continued}
                grouping={grouping[0] ?? ""}
                index={index}
                measure={measure}
                shown={entry.shown}
                row={entry.row}
              />
            )}
            renderRow={({ row, index, measure }) => (
              <IssueRow
                busy={busy}
                index={index}
                measure={measure}
                moved={moved}
                onStartNext={(key) => startNext([key])}
                row={row}
              />
            )}
            scrollTestId="issues-table-scroll"
            stackColumn="title"
            trailingWidth={ACTION_WIDTH}
          />

          <IssueTableSelection
            returnFocus={returnFocus}
            table={table}
            busy={busy}
            startNext={startNext}
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
        </>
      ) : null}
    </section>
  );
}
