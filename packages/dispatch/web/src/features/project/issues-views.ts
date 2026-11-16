import { copyAddress } from "../../lib/page-address";
import { openIssueStatuses } from "./board-model";
import type { ColumnId } from "./issues-table-model";

/**
 * The Issues tab's views. A view is one type: a name, the URL parameters it stands for, the
 * columns it opens with, and whether it draws the status bar. The built-in views are constants
 * of it, and a saved view is a name and a URL query. An address names its view with
 * `view=<id>`; every other parameter of the address replaces the view's value of that same
 * parameter, or narrows the view further, so a link carries only what differs from its view.
 */

export type BuiltInViewId = "open" | "done";

export interface BuiltInView {
  readonly id: BuiltInViewId;
  /** The tab's name: at most 14 characters. */
  readonly name: string;
  /** The view's own parameters, which an address's parameters replace one key at a time. */
  readonly params: () => readonly (readonly [string, string])[];
  /** The columns the view opens with, in the order they draw, the pinned ones first among
   *  them. Where the project has no Legion daemon its Legion columns are left out. */
  readonly cols: readonly ColumnId[];
  /** It draws the bar of its rows by status under the tabs. */
  readonly bar?: true;
}

const OPEN_STATUSES = openIssueStatuses.map((status) => ["status", status] as const);

/** Open: today's default list, and what an address that names no view, or a view this bundle
 *  does not know, opens. */
const OPEN: BuiltInView = {
  id: "open",
  name: "Open",
  params: () => [...OPEN_STATUSES, ["sort", "queue"]],
  // Main's default columns (every column but Updated), with Queue and Legion phase where a
  // daemon runs, and Created by and Open asks early, so the first screen at 1440 px beside the
  // sidebar holds them. What does not fit scrolls sideways under the pinned columns.
  cols: [
    "priority",
    "key",
    "title",
    "queue",
    "status",
    "author",
    "asks",
    "legion_phase",
    "assignee",
    "labels",
    "claim",
    "created",
  ],
  bar: true,
};

/** The built-in views, in their tabs' order. Any other list is a filter a reader saves as a
 *  view of their own. */
export const BUILT_IN_VIEWS: readonly BuiltInView[] = [
  OPEN,
  {
    id: "done",
    name: "Done",
    // Every done issue, newest first: the Closed column's date filter narrows it to a window.
    params: () => [
      ["status", "done"],
      ["sort", "-closed"],
    ],
    // Time per phase comes right after Closed, so it is on the first screen at 1440 px; the
    // columns after it scroll in sideways, as Open's do.
    cols: [
      "priority",
      "key",
      "title",
      "closed",
      "phase_hours",
      "work_hours",
      "assignee",
      "author",
      "labels",
    ],
  },
];

/** The view `id` names, or Open for an id this bundle does not know. */
export function viewFor(id: string | null): BuiltInView {
  return BUILT_IN_VIEWS.find((view) => view.id === id) ?? OPEN;
}

/** The ids older links name the Legion queue's tab by, which is gone: Open narrowed to the
 *  issues in the queue lists the same rows in the same order (`sort=queue` is Open's own). */
const QUEUE_VIEW_IDS = ["queue", "waiting"];

/** An older link's address in today's spelling, or undefined where it is today's already:
 *  `view=queue` and `view=waiting` become Open with `f.queue=Queued`. */
export function currentAddress(params: URLSearchParams): URLSearchParams | undefined {
  const id = params.get("view");
  if (id === null || !QUEUE_VIEW_IDS.includes(id)) return undefined;
  const next = copyAddress(params);
  next.delete("view");
  if (!next.has("f.queue")) next.set("f.queue", "Queued");
  return next;
}

/** What Open opens in for a reader who has picked no layout of the view yet, from the
 *  `project.issue-view` choice kept before the views: `board` is the Board, `legion` (the
 *  Legion view the Issues tab no longer has) the Board by Legion phase, anything else the
 *  table. */
export function legacyOpenLayout(stored: string): {
  readonly layout: "table" | "board";
  readonly group?: "legion_phase";
} {
  if (stored === "legion") return { layout: "board", group: "legion_phase" };
  return { layout: stored === "board" ? "board" : "table" };
}

/** The parameters that are no part of what a view lists: they mark no view as changed. */
const DISPLAY_KEYS = ["view", "page", "size", "cols", "layout"];

/** A view's own parameters as an address. */
const ownParams = (view: BuiltInView) =>
  copyAddress(view.params().map(([key, value]) => [key, value] as const));

/**
 * `own` with `params` written over it: each of `own`'s keys replaced by the address's value of
 * that key where it has one. A key the address gives as one empty value (`f.queue=`) takes the
 * value of `own` away, and `nofilter=1` lifts its Status filter.
 */
export function expandOver(own: URLSearchParams, params: URLSearchParams): URLSearchParams {
  const expanded = copyAddress(own);
  if (params.get("nofilter") === "1") expanded.delete("status");
  for (const key of new Set(params.keys())) {
    if (key === "view" || key === "nofilter") continue;
    const values = params.getAll(key);
    expanded.delete(key);
    if (values.length === 1 && values[0] === "") continue;
    for (const value of values) expanded.append(key, value);
  }
  return expanded;
}

/** The address with its view written out: the view's own parameters under the address's, an
 *  older link read in today's spelling (`currentAddress`). */
export function expandView(params: URLSearchParams): URLSearchParams {
  const current = currentAddress(params) ?? params;
  return expandOver(ownParams(viewFor(current.get("view"))), current);
}

/** Two spellings of one key's values that say the same: the sort in its order, every other key
 *  as a set. */
function sameValues(key: string, a: readonly string[], b: readonly string[]): boolean {
  if (key === "sort") return a.join(",") === b.join(",");
  return a.length === b.length && [...a].sort().join("\n") === [...b].sort().join("\n");
}

/** `full`, which spells a table's whole view, written as the difference from `own`: what `own`
 *  says already is left out, and what it says that `full` does not is taken away. */
export function compressAgainst(own: URLSearchParams, full: URLSearchParams): URLSearchParams {
  const next = copyAddress();
  for (const key of new Set([...full.keys(), ...own.keys()])) {
    if (key === "view") continue;
    const values = full.getAll(key);
    if (sameValues(key, values, own.getAll(key))) continue;
    if (values.length === 0) {
      if (key === "status") next.set("nofilter", "1");
      else next.set(key, "");
      continue;
    }
    for (const value of values) next.append(key, value);
  }
  return next;
}

/** `full`, which spells the table's whole view and names its view with `view=`, written as the
 *  difference from that view: what the view says already is left out. */
export function compressView(full: URLSearchParams): URLSearchParams {
  const view = viewFor(full.get("view"));
  const next = copyAddress();
  if (view.id !== OPEN.id) next.set("view", view.id);
  for (const [key, value] of compressAgainst(ownParams(view), full)) next.append(key, value);
  return next;
}

/**
 * What a table that draws no view opens with: Open's own parameters (the open statuses, sorted
 * by Queue), which is what the project List opened with before the views, and what a table
 * embedded in another page still opens with. Such a table writes no `view=`.
 */
export function embeddedDefaults(): URLSearchParams {
  return ownParams(OPEN);
}

/** A built-in view is changed when any parameter but the page, its size, the columns and the
 *  layout differs from the view's own; the address carries only what differs. */
export function isViewChanged(params: URLSearchParams): boolean {
  return [...params.keys()].some((key) => !DISPLAY_KEYS.includes(key));
}

/** What Clear filters leaves as it is: the view, its sort and grouping, and what is no part of
 *  what a view lists. Everything else is a narrowing: a filter, the search, a flag or a chip. */
const KEPT_BY_CLEAR = [...DISPLAY_KEYS, "sort", "group"];

/** The address's narrowings alone, spelled so that two addresses that narrow the same rows give
 *  the same text. */
export function narrowingsKey(params: URLSearchParams): string {
  return viewQueryKey(
    copyAddress([...params.entries()].filter(([key]) => !KEPT_BY_CLEAR.includes(key)))
  );
}

/**
 * The address with its narrowings taken back to those of `own`, the address the view opens at
 * (`view=<id>` alone for a built-in view, the saved query for a saved one): its filters, search,
 * flags and chips. The sort, the grouping, the columns, the layout and the page size stay as the
 * reader left them, as main's Clear filters keeps them; the page goes back to the first.
 */
export function clearNarrowings(params: URLSearchParams, own: URLSearchParams): URLSearchParams {
  const next = copyAddress(
    [...own.entries()].filter(([key]) => key === "view" || !KEPT_BY_CLEAR.includes(key))
  );
  for (const key of ["sort", "group", "cols", "layout", "size"])
    for (const value of params.getAll(key)) next.append(key, value);
  return next;
}

/** A Closed window: from (included) and to (excluded), in milliseconds, either end open. */
export interface ClosedWindow {
  readonly from: number | null;
  readonly to: number | null;
}

const DAY_MS = 86_400_000;

/** One end of a `<from>..<to>` window: a UTC date (`2026-11-03`), or an RFC 3339 time written in
 *  UTC with `Z`. */
function readEnd(text: string): number | null | undefined {
  if (text === "") return null;
  const at = /^\d{4}-\d{2}-\d{2}$/.test(text)
    ? Date.parse(`${text}T00:00:00Z`)
    : /Z$/.test(text)
      ? Date.parse(text)
      : Number.NaN;
  return Number.isNaN(at) ? undefined : at;
}

/** The window `f.closed` names: `<N>d` is the last N times 24 hours up to now, `<from>..<to>` a
 *  range, and `all` (or a value this cannot read) no window at all. */
export function readWindow(value: string, nowMs: number): ClosedWindow | undefined {
  const days = /^(\d+)d$/.exec(value);
  if (days !== null) return { from: nowMs - Number(days[1]) * DAY_MS, to: null };
  const range = /^(.*)\.\.(.*)$/.exec(value);
  if (range === null) return undefined;
  const from = readEnd(range[1] ?? "");
  const to = readEnd(range[2] ?? "");
  return from === undefined || to === undefined ? undefined : { from, to };
}

/** Whether a row closed at `closedAt` is in the window `value` names. With a window, a row that
 *  never closed is in none. */
export function inWindow(closedAt: string | null, value: string, nowMs: number): boolean {
  const window = readWindow(value, nowMs);
  if (window === undefined) return true;
  if (closedAt === null) return false;
  const at = Date.parse(closedAt);
  return (window.from === null || at >= window.from) && (window.to === null || at < window.to);
}

/** The read a window makes of the listing: `closed_since` and, for a window with an end,
 *  `closed_before`. A rolling window starts on the hour before, so the read's key moves once an
 *  hour rather than on every render, and the rows it reads are a superset of the window's. */
export function windowRead(
  value: string,
  nowMs: number
): { readonly closed_since?: string; readonly closed_before?: string } | undefined {
  const window = readWindow(value, nowMs);
  if (window === undefined) return undefined;
  const hour = 3_600_000;
  return {
    ...(window.from === null
      ? {}
      : { closed_since: new Date(Math.floor(window.from / hour) * hour).toISOString() }),
    ...(window.to === null ? {} : { closed_before: new Date(window.to).toISOString() }),
  };
}

/** A view saved by the reader: its name and the URL query that opens it. */
export interface SavedView {
  readonly name: string;
  readonly query: string;
}

/** What `project.saved-views` keeps: a version, and each project's saved views. */
export interface StoredViews {
  readonly version: 1;
  readonly projects: Readonly<Record<string, readonly SavedView[]>>;
}

export const NO_SAVED_VIEWS: StoredViews = { version: 1, projects: {} };

/** The longest name a saved view may have, which is the longest a view tab shows. */
export const VIEW_NAME_MAX = 14;

/** `project.saved-views` as stored, with every entry that does not parse dropped. */
export function readSavedViews(stored: string | null): StoredViews {
  if (stored === null) return NO_SAVED_VIEWS;
  let value: unknown;
  try {
    value = JSON.parse(stored);
  } catch {
    return NO_SAVED_VIEWS;
  }
  if (typeof value !== "object" || value === null || !("version" in value) || value.version !== 1)
    return NO_SAVED_VIEWS;
  if (!("projects" in value) || typeof value.projects !== "object" || value.projects === null)
    return NO_SAVED_VIEWS;
  const kept: Record<string, SavedView[]> = {};
  for (const [project, views] of Object.entries(value.projects)) {
    if (!Array.isArray(views)) continue;
    kept[project] = views.flatMap((view: unknown): SavedView[] =>
      typeof view === "object" &&
      view !== null &&
      "name" in view &&
      "query" in view &&
      typeof view.name === "string" &&
      view.name.trim() !== "" &&
      view.name.length <= VIEW_NAME_MAX &&
      typeof view.query === "string"
        ? [{ name: view.name, query: view.query }]
        : []
    );
  }
  return { version: 1, projects: kept };
}

/** Why `name` cannot name a saved view, or undefined where it can. A name already saved is not
 *  refused here: saving under it asks to replace it. */
export function viewNameProblem(name: string): string | undefined {
  const trimmed = name.trim();
  if (trimmed === "") return "Type a name";
  if (trimmed.length > VIEW_NAME_MAX) return `Use ${VIEW_NAME_MAX} characters or fewer`;
  if (BUILT_IN_VIEWS.some((view) => view.name.toLowerCase() === trimmed.toLowerCase()))
    return "A built-in view has this name";
  return undefined;
}

/** The address of a saved view's query, page and size left out: two addresses that list the
 *  same rows in the same columns and layout are one saved view. */
export function viewQueryKey(params: URLSearchParams): string {
  const kept = [...params.entries()].filter(([key]) => key !== "page" && key !== "size");
  kept.sort(([a, x], [b, y]) => (a === b ? x.localeCompare(y) : a.localeCompare(b)));
  return copyAddress(kept).toString();
}
