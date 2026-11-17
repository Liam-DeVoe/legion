// The Legion parts of the Issues tab that hold no React and no cache: the board's phase order,
// the time formats a cell and a card write, where each listed row takes its Legion facts from
// (the board's `issues` map, else the items route of `legion-work-time`), the windows and pages
// that items read spans, the Closed window the Done reads take, and the rule that keeps an older
// server's Done answer off the page. The page reads what these name (`api/queries.ts`); the
// tests read the rules here rather than through the markup.
import type {
  EvalItemRow,
  EvalItemsResponse,
  IssueSummary,
  LegionBoardColumn,
  LegionBoardFacts,
  LegionBoardResponse,
} from "../../api/types";

/**
 * The Legion phases, in the board's order (`legionBoardColumnOrder` in
 * `packages/envoy/internal/dispatch/api/legion_board.go`): the ten columns of the board route,
 * each by its id and its title. The Legion phase column, its filter, the Board grouped by Legion
 * phase and Time per phase follow this order. An issue is in a phase only while the daemon holds
 * a card for it, and its Dispatch status is a column of its own.
 */
export const LEGION_PHASES = [
  { id: "queue", title: "Queue" },
  { id: "design", title: "Design" },
  { id: "planning", title: "Planning" },
  { id: "implementing", title: "Implementing" },
  { id: "testing", title: "Testing" },
  { id: "reviewing", title: "Reviewing" },
  { id: "accepting", title: "Accepting" },
  { id: "merging", title: "Merging" },
  { id: "awaiting_person", title: "Waiting on a person" },
  { id: "held", title: "Held" },
] as const satisfies readonly { readonly id: LegionBoardColumn["id"]; readonly title: string }[];

/** A board column's id: the address value of a Legion phase (`f.legion_phase=implementing`). */
export type LegionPhase = (typeof LEGION_PHASES)[number]["id"];

const phaseTitles: ReadonlyMap<string, string> = new Map(
  LEGION_PHASES.map((phase) => [phase.id, phase.title])
);

/** A phase's title, or the id as it is for a phase this build does not know. */
export function legionPhaseTitle(phase: string): string {
  return phaseTitles.get(phase) ?? phase;
}

/** How long the issue has been in its phase, from the moment the daemon's state first showed it
 *  there: minutes under an hour, hours and minutes under a day, then days and hours. An issue
 *  whose phase entry no read has recorded shows an em dash rather than a time nobody measured. */
export function formatPhaseTime(since: string | null, nowMs: number): string {
  if (since === null) return "—";
  const sinceMs = Date.parse(since);
  if (Number.isNaN(sinceMs)) return "—";
  return formatSpan(Math.floor(Math.max(0, nowMs - sinceMs) / 60_000));
}

/** The work time the server stored for the issue, in the same words as the time in its phase.
 *  The hours are the stored row's `work_hours`: the page computes none of them. */
export function formatWorkTime(hours: number): string {
  return formatSpan(Math.floor(Math.max(0, hours) * 60));
}

/** A span of whole minutes in the page's words: minutes under an hour, hours and minutes under a
 *  day, then days and hours. */
function formatSpan(minutes: number): string {
  if (minutes < 60) return `${minutes} m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} h ${minutes % 60} m`;
  return `${Math.floor(hours / 24)} d ${hours % 24} h`;
}

/** The facts a row's Legion cells draw: Work time, Time per phase (`hours_by_phase`), Review
 *  rounds and Legion tree (`root_key`, `tree_depth`), with the close and the first worker event
 *  they are counted between. The board's `issues` map holds them whole for the issues it carries,
 *  and an items row of `legion-work-time` carries the same facts as columns (`itemFacts`). */
export type LegionFacts = Pick<
  LegionBoardFacts,
  | "closed_at"
  | "hours_by_phase"
  | "review_rounds"
  | "root_key"
  | "tree_depth"
  | "work_first_at"
  | "work_hours"
>;

/** A listed row's facts: the board's `issues[key]` where the map holds the key, else its items
 *  row (`items`, by `row_key`), else none, the facts of an issue with no Legion work. A row both
 *  hold keeps the map's facts. Whether the items read is still out, or failed, is the caller's to
 *  tell apart from no Legion work: this reads only what has answered. */
export function legionFactsOf(
  key: string,
  board: LegionBoardResponse | undefined,
  items: Readonly<Record<string, LegionFacts>> | undefined
): LegionFacts | undefined {
  const held = board?.issues;
  if (held && Object.hasOwn(held, key)) return held[key];
  return items && Object.hasOwn(items, key) ? items[key] : undefined;
}

/** The prefix of an items row's column of one phase's hours (`hours_implementing`): the route
 *  writes each key of the stored `hours_by_phase` as a column of its own
 *  (`ColumnHoursPrefix` in `packages/envoy/internal/dispatch/legionfacts/items.go`). */
const HOURS_COLUMN_PREFIX = "hours_";

const numberIn = (cols: Readonly<Record<string, unknown>>, name: string): number | undefined => {
  const value = cols[name];
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
};

/** The facts an items row of `legion-work-time` carries, read from its columns as the route
 *  serves them (`itemColumns` in `legionfacts/items.go`): `work_hours`, `review_rounds`,
 *  `root_key`, `tree_depth`, `closed_at` and `work_first_at` under their own names, and the hours
 *  of each phase under `hours_<phase>`. A row without the four facts every issue row carries is
 *  no facts row, and reads as none. */
export function itemFacts(row: EvalItemRow): LegionFacts | undefined {
  const cols: Readonly<Record<string, unknown>> = row.cols ?? {};
  const workHours = numberIn(cols, "work_hours");
  const reviewRounds = numberIn(cols, "review_rounds");
  const treeDepth = numberIn(cols, "tree_depth");
  const { root_key: rootKey, closed_at: closedAt, work_first_at: workFirstAt } = cols;
  if (
    workHours === undefined ||
    reviewRounds === undefined ||
    treeDepth === undefined ||
    typeof rootKey !== "string"
  ) {
    return undefined;
  }
  const hoursByPhase: Record<string, number> = {};
  for (const [name, value] of Object.entries(cols)) {
    if (!name.startsWith(HOURS_COLUMN_PREFIX)) continue;
    if (typeof value === "number" && Number.isFinite(value)) {
      hoursByPhase[name.slice(HOURS_COLUMN_PREFIX.length)] = value;
    }
  }
  return {
    closed_at: typeof closedAt === "string" ? closedAt : null,
    hours_by_phase: hoursByPhase,
    review_rounds: reviewRounds,
    root_key: rootKey,
    tree_depth: treeDepth,
    work_first_at: typeof workFirstAt === "string" ? workFirstAt : null,
    work_hours: workHours,
  };
}

/** A Closed window (`f.closed`), as the Done reads take it: its first moment (`from`, included)
 *  and its end (`to`, left out), in milliseconds since the epoch, either one null where the
 *  window leaves it open. `rolling` is a window of the last N days (`f.closed=<N>d`), whose first
 *  moment moves with the clock; a read of every done row starts it on the hour
 *  (`projectDoneIssuesQuery`). The views (`issues-views.ts`) read the address into this. */
export interface ClosedWindow {
  readonly from: number | null;
  readonly to: number | null;
  readonly rolling: boolean;
}

/** The eval whose items carry a closed issue's Legion facts, one `issue` row per issue. */
export const LEGION_WORK_TIME = "legion-work-time";

/** The row kind of an issue's items row (`legionfacts.ItemRowKind`). */
const ISSUE_ROW_KIND = "issue";

/** The most rows one items read answers (`evalPageMax` in `api/evals.go`). */
export const ITEMS_PAGE_SIZE = 200;

/** The most days one items read spans (`maxDeliveryWindow` in `api/delivery.go`, which
 *  `parseDeliveryWindow` holds the raw `from`..`to` to). */
const ITEMS_WINDOW_DAYS = 84;

const DAY_MS = 86_400_000;

/** One items read's window, as the route takes it: `from` included and `to` left out, each a
 *  UTC midnight. */
export interface ItemsWindow {
  readonly from: string;
  readonly to: string;
}

const midnight = (ms: number): string => `${new Date(ms).toISOString().slice(0, 10)}T00:00:00Z`;

/**
 * The windows that cover the UTC days from the day of `firstMs` to the day of `lastMs`, both
 * included, oldest first. Each starts and ends at a UTC midnight and spans at most 84 days. The
 * route refuses a raw `from`..`to` over 84 days before it widens the window to whole days, so a
 * window that started off midnight would cover an 85th day, or be refused. `to` is left out, as
 * the route leaves it out (`parseDeliveryWindow`, then `evalDaysOf` in `api/evals.go`), so each
 * window ends at the midnight after its last day.
 */
export function closedDaysWindows(firstMs: number, lastMs: number): readonly ItemsWindow[] {
  const start = Math.floor(firstMs / DAY_MS) * DAY_MS;
  const end = (Math.floor(lastMs / DAY_MS) + 1) * DAY_MS;
  const windows: ItemsWindow[] = [];
  for (let from = start; from < end; from += ITEMS_WINDOW_DAYS * DAY_MS) {
    windows.push({
      from: midnight(from),
      to: midnight(Math.min(from + ITEMS_WINDOW_DAYS * DAY_MS, end)),
    });
  }
  return windows;
}

/**
 * The windows the items read of `rows` spans: the UTC days from the oldest close of a row that
 * the board's map does not hold to the newest, or to `nowMs` for a read of every done row. None
 * where every closed row is the map's, or no row has closed.
 */
export function itemsWindows(
  rows: readonly Pick<IssueSummary, "key" | "closed_at">[],
  board: LegionBoardResponse | undefined,
  nowMs?: number
): readonly ItemsWindow[] {
  const held = board?.issues;
  let first = Number.POSITIVE_INFINITY;
  let last = Number.NEGATIVE_INFINITY;
  for (const row of rows) {
    if (typeof row.closed_at !== "string" || (held && Object.hasOwn(held, row.key))) continue;
    const closed = Date.parse(row.closed_at);
    if (Number.isNaN(closed)) continue;
    first = Math.min(first, closed);
    last = Math.max(last, closed);
  }
  if (first === Number.POSITIVE_INFINITY) return [];
  return closedDaysWindows(first, Math.max(last, nowMs ?? last));
}

/**
 * Every issue row of `legion-work-time` closed in `windows`, as facts by issue key. Each window
 * is read page by page, `row_kind=issue` with `limit` 200 and `offset`, until `offset` and the
 * page's rows reach the answer's `total`. `list` reads one page (`api.listEvalItems` for the
 * project). A refusal it throws fails the whole read, `404 EVAL_NOT_FOUND` among them: a failed
 * read is never the empty facts of an issue with no Legion work.
 */
export async function readLegionItems(
  windows: readonly ItemsWindow[],
  list: (
    query: ItemsWindow & {
      readonly row_kind: string;
      readonly limit: number;
      readonly offset: number;
    }
  ) => Promise<Pick<EvalItemsResponse, "items" | "total">>
): Promise<Readonly<Record<string, LegionFacts>>> {
  const facts: Record<string, LegionFacts> = {};
  for (const window of windows) {
    let offset = 0;
    let total = 0;
    do {
      const page = await list({
        ...window,
        row_kind: ISSUE_ROW_KIND,
        limit: ITEMS_PAGE_SIZE,
        offset,
      });
      const rows = page.items ?? [];
      for (const row of rows) {
        const read = itemFacts(row);
        if (read !== undefined) facts[row.row_key] = read;
      }
      // A page with no rows ends the window, whatever `total` says: rows that left the window
      // between two pages would otherwise be asked for again and again.
      if (rows.length === 0) break;
      offset += rows.length;
      total = page.total;
    } while (offset < total);
  }
  return facts;
}

const NO_ROWS: readonly never[] = [];

/**
 * The rows a Done read lists. An older server, during a rolling deploy, writes no `closed_at`
 * and ignores `closed_since`, `closed_before` and `sort`. So a read with a window, or a page read,
 * none of whose rows carries `closed_at`, lists no row, rather than every done issue or a page of
 * them in the route's own order. The view lists rows again once a server that carries
 * `closed_at` answers.
 */
export function listedDoneRows<Row extends { readonly closed_at?: string | null }>(
  rows: readonly Row[],
  read: { readonly windowed: boolean; readonly paged: boolean }
): readonly Row[] {
  if (!read.windowed && !read.paged) return rows;
  return rows.some((row) => typeof row.closed_at === "string") ? rows : NO_ROWS;
}
