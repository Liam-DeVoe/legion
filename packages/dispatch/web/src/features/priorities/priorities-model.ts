import type { PriorityRow } from "../../api/types";
import { questionLine } from "../inbox/question-line";
import type { IssueStatus } from "../project/board-model";
import { ageOf, AGES as ISSUE_AGES, runningIssueStatuses } from "../project/issues-table-model";
import { columnsById, type TableColumn, tableLayout } from "./priority-table-model";

/** The statuses each count column adds up. Active is the statuses an issue is being worked in
 *  (`runningIssueStatuses`), which no status bar counts as one: Open's `IssueStatusBar` on the
 *  Issues tab and `IssueTableStatus` on the priority pages' embedded tables count each status on
 *  its own. Triage, Todo is work admitted and not started; Backlog, icebox is work that stands in
 *  the backlog or the icebox. The three together are every open status, so no open issue under a
 *  priority is uncounted. */
const WAITING_STATUSES: readonly IssueStatus[] = ["todo", "triage"];
/** The one list of the parked statuses: the parked count here and the parked fault list of the
 *  priorities page (`PrioritiesPage.tsx`) both read it. */
export const PARKED_STATUSES: readonly IssueStatus[] = ["backlog", "icebox"];

/** What the newest-event column files a row under when nothing under the priority has moved. */
export const NEVER_MOVED = "Never";

/** One row of the priorities table: the priority as the server answers it, its place in the one
 *  order, and the four counts the row shows, added up once rather than per cell. */
export interface PriorityItem {
  readonly priority: PriorityRow;
  /** The priority's place in the order, from 1: what `P7` reads from. */
  readonly place: number;
  readonly order: string;
  /** The first line of the description, in plain words, which the row shows while it is
   *  closed: a description is markdown, so the markers it opens with are not read out. */
  readonly summary: string;
  readonly inFlight: number;
  readonly waiting: number;
  readonly parked: number;
  readonly closed: number;
  readonly newest: string | null;
}

/** What a priority's place in the one order is called, from the index the read answers it at:
 *  the row's `P7`, and the same name the issue header's Advances line writes
 *  (`IssuePriorityLine`), so one rule says what `P7` means. */
export const orderLabel = (index: number): string => `P${index + 1}`;

const countOf = (by: PriorityRow["work"]["by_status"], statuses: readonly string[]): number =>
  statuses.reduce((total, status) => total + (by?.[status] ?? 0), 0);

/** The rows in the order the server answers them, which is rank order: the place a row shows
 *  is its place in that answer, so the order a reader drags and the order they read are one. */
export function buildPriorityItems(rows: readonly PriorityRow[]): PriorityItem[] {
  return rows.map((priority, index) => ({
    priority,
    place: index + 1,
    order: orderLabel(index),
    summary: questionLine(priority.description),
    inFlight: countOf(priority.work.by_status, runningIssueStatuses),
    waiting: countOf(priority.work.by_status, WAITING_STATUSES),
    parked: countOf(priority.work.by_status, PARKED_STATUSES),
    closed: priority.work.closed_last_7_days,
    newest: priority.work.newest_event_at,
  }));
}

/** What a drop writes: the priority the dragged one lands above (`before`), or the one it lands
 *  below (`after`), which is the shape `PATCH /api/v1/priorities/{id}` takes. A drop on the
 *  dragged row itself, or on a row that is not in the order, moves nothing. */
export function rankMove(
  order: readonly string[],
  moved: string,
  onto: string
): { before?: string; after?: string } | undefined {
  const from = order.indexOf(moved);
  const to = order.indexOf(onto);
  if (from === -1 || to === -1 || from === to) return undefined;
  return from > to ? { before: onto } : { after: onto };
}

/** The age bucket the newest event under a priority files under, which its filter picks by. */
const newestBucket = (at: string | null): string => (at === null ? NEVER_MOVED : ageOf(at));

/** The ages the issues table files a time under, with the words for a priority nothing has
 *  happened under at all. */
const AGES = [...ISSUE_AGES, NEVER_MOVED];

/** What a search over the priorities table matches: the title and the owner, as the box says. */
export function matchesPrioritySearch(item: PriorityItem, search: string): boolean {
  const words = search.trim().toLowerCase();
  if (words === "") return true;
  return `${item.priority.title} ${item.priority.owner}`.toLowerCase().includes(words);
}

/** The owner a row files under, which is the owner's own text or the words for none. */
export const ownerOf = (item: PriorityItem): string =>
  item.priority.owner.trim() === "" ? "No owner" : item.priority.owner.trim();

/**
 * Every column of the priorities table, left to right: the order and the title stay pinned at
 * the left edge while the counts scroll. A column keeps the id the address and a stored layout
 * have always named it by, whatever its header reads. The four count columns are alike: a
 * number, filtered by picking the numbers the rows hold, which is how a reader asks for the
 * priorities with nothing active.
 *
 * Each column is at least as wide as its header whole: its label and its filter button, at their
 * widest below 1280 px, where each is a 44 px touch target, with the gap between them and the
 * cell's padding, 64 px over the label. Measured in Chromium (builds Job
 * infra-3221-t16c-measure-rp2nn): Order's label is 43 px in a 44 px button, Triage, Todo's 86,
 * Closed, 7 days' 102 and Newest event's 97. While the table is sorted by a column, the layout
 * adds the sort mark (`SORT_MARK_WIDTH`), except where `markInMin` says the minimum holds it.
 */
export const PRIORITY_COLUMNS: readonly TableColumn<PriorityItem>[] = [
  { id: "order", header: "Order", size: 108, min: 108, pinned: true, text: (item) => item.order },
  {
    id: "title",
    header: "Priority",
    size: 420,
    min: 180,
    markInMin: true,
    pinned: true,
    text: (item) => `${item.priority.title} ${item.priority.description}`,
  },
  {
    id: "owner",
    header: "Owner",
    size: 200,
    min: 160,
    markInMin: true,
    values: (item) => [ownerOf(item)],
    group: ownerOf,
  },
  {
    id: "in-flight",
    header: "Active",
    size: 132,
    min: 132,
    markInMin: true,
    values: (i) => [String(i.inFlight)],
  },
  {
    id: "waiting",
    header: "Triage, Todo",
    size: 152,
    min: 152,
    values: (i) => [String(i.waiting)],
  },
  {
    id: "parked",
    header: "Backlog, icebox",
    size: 180,
    min: 180,
    values: (item) => [String(item.parked)],
  },
  {
    id: "closed",
    header: "Closed, 7 days",
    size: 168,
    min: 168,
    values: (i) => [String(i.closed)],
  },
  {
    id: "newest",
    header: "Newest event",
    size: 164,
    min: 164,
    values: (item) => [newestBucket(item.newest)],
    group: (item) => newestBucket(item.newest),
    order: AGES,
    always: AGES,
  },
];

export const PRIORITY_COLUMNS_BY_ID = columnsById(PRIORITY_COLUMNS);

/** The priorities table's own words for its rows, which the table core reads and never
 *  invents: the attribute a row carries holding its priority's id, the one naming the group it
 *  sits in, and the one on the link opening the row follows, its order's link to the priority's
 *  page. A priority is no issue, so none of them is the issue table's. */
export const PRIORITY_ROW_ATTRIBUTE = "data-priority-row";
export const PRIORITY_GROUP_ATTRIBUTE = "data-priority-group";
export const PRIORITY_LINK_ATTRIBUTE = "data-priority-link";

export const PRIORITY_LAYOUT = tableLayout(PRIORITY_COLUMNS, "title", PRIORITY_ROW_ATTRIBUTE);
/** The table opens in the one order, which is what the page is about. */
export const PRIORITY_SORTING = [{ id: "order", desc: false }];
