import type { PriorityRow } from "../../api/types";
import { questionLine } from "../inbox/question-line";
import type { IssueStatus } from "../project/board-model";
import { ageOf, AGES as ISSUE_AGES, runningIssueStatuses } from "../project/issues-table-model";
import { columnsById, type TableColumn, tableLayout } from "./priority-table-model";

/** The statuses each count column adds up. In flight is the statuses an issue is being worked
 *  in, which the issues table's own status bar counts (`runningIssueStatuses`); waiting is work
 *  admitted and not started; parked is work that stands in the backlog or the icebox. The three
 *  together are every open status, so no open issue under a priority is uncounted. */
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
  /** The priority's place in the order, from 1: what `#7` reads from. */
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
 *  the row's `#7`, and the same name the issue header's Advances line writes
 *  (`IssuePriorityLine`), so one rule says what `#7` means. It is not `P7`: P0 to P3 are an
 *  issue's own priority, a different thing. */
export const orderLabel = (index: number): string => `#${index + 1}`;

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

/** The count columns are alike: a number, filtered by picking the numbers the rows hold, which
 *  is how a reader asks for the priorities with nothing active. Each is as wide as its header
 *  needs beside its filter control, and no wider, so the table fits a desktop window. */
const COUNT_WIDTH = { size: 112, min: 112 };

/** Every column of the priorities table, left to right: the order and the title stay pinned at
 *  the left edge while the counts scroll. Each header reads in 16 characters or fewer, in the
 *  words a status is named by (INFRA-3221, R13). What closed in the last 7 days opens hidden, one
 *  tick away under Columns, so the table fits a 1440 px window beside the sidebar with the work
 *  as it stands now - who owns each priority, what is active, in triage or todo, and in backlog
 *  or icebox - and when anything under it last moved. */
export const PRIORITY_COLUMNS: readonly TableColumn<PriorityItem>[] = [
  { id: "order", header: "#", size: 76, min: 76, pinned: true, text: (item) => item.order },
  {
    id: "title",
    header: "Priority",
    size: 420,
    min: 180,
    pinned: true,
    text: (item) => `${item.priority.title} ${item.priority.description}`,
  },
  {
    id: "owner",
    header: "Owner",
    size: 160,
    min: 120,
    values: (item) => [ownerOf(item)],
    group: ownerOf,
  },
  { id: "in-flight", header: "Active", ...COUNT_WIDTH, values: (i) => [String(i.inFlight)] },
  {
    id: "waiting",
    header: "Triage, Todo",
    size: 132,
    min: 132,
    values: (i) => [String(i.waiting)],
  },
  {
    id: "parked",
    header: "Backlog, icebox",
    size: 148,
    min: 148,
    values: (item) => [String(item.parked)],
  },
  {
    id: "closed",
    header: "Closed, 7 days",
    size: 156,
    min: 156,
    hidden: true,
    values: (i) => [String(i.closed)],
  },
  {
    id: "newest",
    header: "Newest event",
    size: 156,
    min: 156,
    values: (item) => [newestBucket(item.newest)],
    group: (item) => newestBucket(item.newest),
    order: AGES,
    always: AGES,
  },
];

export const PRIORITY_COLUMNS_BY_ID = columnsById(PRIORITY_COLUMNS);
/** The priorities table's layout: the order and the title pinned, and the title filling a
 *  desktop box with what the other shown columns leave (`fills`), so nothing scrolls sideways. */
export const PRIORITY_LAYOUT = { ...tableLayout(PRIORITY_COLUMNS, "title"), fills: "title" };
/** The table opens in the one order, which is what the page is about. */
export const PRIORITY_SORTING = [{ id: "order", desc: false }];
