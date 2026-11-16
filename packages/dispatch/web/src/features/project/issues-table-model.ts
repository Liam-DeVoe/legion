import { LEGION_ROLES } from "@legion/contracts";
import { rankItem, rankings } from "@tanstack/match-sorter-utils";
import type { ColumnFiltersState } from "@tanstack/react-table";
import type {
  Actor,
  IssueSummary,
  LegionBoardCard,
  LegionBoardColumnID,
  LegionBoardFacts,
  LegionBoardResponse,
  UpdateIssueInput,
} from "../../api/types";
import type { LayoutColumns } from "../../components/table-layout";
import { capSearchQuery, copyAddress, type TableAddressView } from "../../lib/page-address";
import { compareTimestamps } from "../../lib/timestamps";
import { type IssueStatus, issueStatuses, openIssueStatuses, statusText } from "./board-model";
import {
  ACTIVITIES,
  activityLabel,
  legionPhaseIds,
  phaseHours,
  phaseLabel,
  WAITING_REASONS,
  waitingLabel,
  workPhaseIds,
  workPhaseLabel,
} from "./issues-legion";
import { compressView, expandView, inWindow } from "./issues-views";

/** An issue's card on the Legion board, with the column it sits in. */
export interface BoardCardRow {
  readonly card: LegionBoardCard;
  readonly column: LegionBoardColumnID;
}

export interface Item {
  readonly issue: IssueSummary;
  /** Its place in Start next's queue: the Todo issues labelled `legion`, in rank order. Start
   *  next keeps this rule of its own, apart from the Queue column's number. */
  readonly queue?: number;
  readonly author: string;
  readonly claimedBy: string;
  /** Whether an agent could pick this issue up now: nobody holds it, or the server would hand
   *  its claim to the next agent that asks. The Claimed by filter's "Nobody or lapsed". */
  readonly claimFree: boolean;
  readonly search: string;
  /** Its card on the board route, where the daemon gives it one: the card's column is the
   *  issue's Legion phase. */
  readonly card?: BoardCardRow;
  /** Its stored per-issue row of the board route (`issues[KEY]`), where it has Legion work. */
  readonly facts?: LegionBoardFacts;
  /** The Queue column's number: the board's `waiting_place` in a project with a Legion daemon,
   *  else the Start next number. */
  readonly place?: number;
  /** In the Legion queue: it has a place, or its card is in the Queue column. */
  readonly queued: boolean;
  /** Whether the reader created it, or an agent session the reader owns did (`f.author=@me`). */
  readonly mine: boolean;
}

/** The attributes the List's own rows carry, which the table core is told to read: its keys
 *  walk `data-issue-row`, its anchor holds a row's place by it and its groups by
 *  `data-issue-group`, and opening a row follows the link marked `data-issue-link`. */
export const ISSUE_ROW_ATTRIBUTE = "data-issue-row";
export const ISSUE_GROUP_ATTRIBUTE = "data-issue-group";
export const ISSUE_LINK_ATTRIBUTE = "data-issue-link";

/** The one key the List keeps a project's table under in the browser: the reader's layout, and
 *  the record of an action a reload interrupted. */
export const tableStorageKey = (project: string) => `dispatch.project-table.${project}`;

export const AGES = ["Today", "This week", "This month", "Older"] as const;
export const ALL_ROWS = Number.MAX_SAFE_INTEGER;
export const PAGE_SIZES = [25, 50, 100, 250, ALL_ROWS];
/** The statuses an issue is being worked in, which the priority pages count as running. */
export const runningIssueStatuses: readonly IssueStatus[] = [
  "in_progress",
  "testing",
  "needs_review",
  "retro",
];
/** The Created by value that names the reader who opens the page. */
export const ME = "@me";
/** The Claimed by value that picks the issues nobody is working: no claim at all, or a claim
 *  the server would hand over (`claimIsTakeable`). The `?unclaimed=1` link sets it. */
export const UNCLAIMED_FACET = "Nobody or lapsed";

/** How many of one action's writes are in flight at once, whether it is sending them for the
 *  first time or Finish is retrying what a refusal left behind. A selection is as large as the
 *  project, and "Select all N matching" is two clicks away, so the table sends a bounded
 *  stream rather than every request at once; each write is still independent in the store. */
export const WRITES_IN_FLIGHT = 8;

/** How a column filters, and everything a filter of its kind needs: a column that filters by
 *  picking values cannot exist without the values a row files under, nor one that filters by
 *  typed text without the text it reads. Matching, the URL parameter and the options a filter
 *  always offers live here too, so a column is one entry rather than a branch in three files. */
export type ColumnFilterSpec =
  | {
      readonly kind: "picks";
      /** Every value this row files under here: its filter options and its facet counts. */
      readonly values: (item: Item) => string[];
      /** The heading this row groups under; absent when the column cannot group. */
      readonly group?: (item: Item) => string;
      /** The order the filter lists its values in; by count when absent. */
      readonly order?: readonly string[];
      /** A value as the reader sees it, in an option and in a group's heading. */
      readonly show?: (value: string) => string;
      /** A row must carry every picked value, not just one of them, compared case-insensitively. */
      readonly match?: "all";
      /** The URL parameter the picks travel in; `f.<id>` when absent. */
      readonly param?: string;
      /** Values the filter offers whatever the loaded rows hold. */
      readonly always?: readonly string[];
      /** A value out of the URL as this column keeps it. */
      readonly normalize?: (value: string) => string;
    }
  | { readonly kind: "text"; readonly value: (item: Item) => string }
  /** A window of time: `<N>d`, `<from>..<to>` or `all` (`issues-views.ts`'s `readWindow`). */
  | { readonly kind: "range"; readonly value: (item: Item) => string | null };

/** Every column the table has. Narrow enough that a rendering, a width or a minimum keyed by
 *  an id no column carries fails to compile. */
export type ColumnId =
  | "select"
  | "priority"
  | "queue"
  | "key"
  | "title"
  | "status"
  | "legion_phase"
  | "assignee"
  | "author"
  | "labels"
  | "claim"
  | "asks"
  | "created"
  | "updated"
  | "closed"
  | "roles"
  | "activity"
  | "waited"
  | "waiting_reason"
  | "time_in_phase"
  | "work_hours"
  | "review_rounds"
  | "legion"
  | "phase_hours";

/** One column's identity: everything about it that is not how its cell renders. The column
 *  definitions, `columnIds`, `groupable`, `facets`, the default widths and the minimums are all
 *  derived from this list, so a new column is one entry here plus its cell. */
export interface ColumnSpec {
  readonly id: ColumnId;
  /** Its width before the reader drags it. */
  readonly size: number;
  /** How narrow a drag or a narrow window may take it below 1280 px, where every button is a
   *  44 px touch target: the whole header label, its sort mark and its 44 px filter button with
   *  the cell's padding, about the label's width plus 78 px. From 1280 px the filter button is
   *  32 px, and a filterable column's minimum is 12 px less (`columnMinimums`), unless its cell
   *  sets it (`cellMin`). A table whose minimums do not fit scrolls sideways. */
  readonly min: number;
  /** Its minimum is what its cell holds, not its header, so it is the same at any width: a
   *  ten-character key with its unread dot, a title, a whole claim chip. */
  readonly cellMin?: true;
  /** How narrow it may go on a phone, where the key and the title are the row. */
  readonly compactMin?: number;
  /** Stays in view while the table scrolls sideways. */
  readonly pinned?: boolean;
  /** Headers sort unless this says otherwise. */
  readonly sortable?: boolean;
  readonly filter?: ColumnFilterSpec;
  /** Its source is the board route: it does not show in a project with no Legion daemon. */
  readonly board?: boolean;
}

const one = (
  value: (item: Item) => string,
  rest: { order?: readonly string[]; show?: (value: string) => string; groups?: boolean } = {}
): ColumnFilterSpec => ({
  kind: "picks",
  values: (item) => [value(item)],
  ...(rest.groups === false ? {} : { group: value }),
  ...(rest.order === undefined ? {} : { order: rest.order }),
  ...(rest.show === undefined ? {} : { show: rest.show }),
});

export const priorityName = (value: string) => (value === "" ? "No priority" : `P${value}`);
export function ageOf(iso: string): string {
  const days = (Date.now() - new Date(iso).getTime()) / 86_400_000;
  return days < 1 ? "Today" : days < 7 ? "This week" : days < 30 ? "This month" : "Older";
}

/** How long ago `since` was, in milliseconds; undefined with no time. */
export function ageMs(since: string | null | undefined): number | undefined {
  if (since === null || since === undefined) return undefined;
  const at = Date.parse(since);
  return Number.isNaN(at) ? undefined : Date.now() - at;
}

/** The issue's work time in hours, or undefined where none was measured: an issue with no
 *  Legion facts, or one whose facts hold no measured work (`work_first_at` null, which the
 *  server answers with `work_hours` 0). A missing work time draws an empty cell, never 0. */
export function workHoursOf(item: Pick<Item, "facts">): number | undefined {
  const facts = item.facts;
  if (facts === undefined) return undefined;
  return facts.work_first_at == null && !(facts.work_hours > 0) ? undefined : facts.work_hours;
}

/**
 * How each measure is computed, the one sentence its header's tooltip, a Board card's fact and
 * the Columns menu say: the server's rule, read from the code that computes it. Work time, Time
 * per phase and Review rounds are the per-issue facts (`legion/metrics`: `computeWorkTime` with
 * `issueEnd`, `parkedSpans` and `queuedSpans`; `issueParts` with `waitingOnPersonSpans` and
 * `waitingOnChildrenSpans`; `reviewRounds`). Time waiting and Time in Legion phase run from the
 * board's `waiting.since` (`legion.CardWaiting`) and `phase_since` (`api/legion_board.go`) to now.
 * A measure the server has no value for is an empty cell, never 0.
 */
export const MEASURE_METHODS: Partial<Record<ColumnId, string>> = {
  work_hours:
    "Dispatch counts the hours from the first Legion worker event until the issue closes, or until Dispatch last read it. It does not count Backlog or Icebox hours, or hours that the issue waits to start with no free capacity.",
  phase_hours:
    "Work time goes to Waiting on a person while the issue's ask or root waits, else to its phase or Other. A root does not count the hours it waits on its children.",
  review_rounds:
    "Dispatch counts each start of the Reviewing phase, or each move to Needs review status for an issue with no recorded Legion phases.",
  waited:
    "Dispatch shows the time since Legion asked a person, when the issue waits on a person, or else since its Legion phase started.",
  time_in_phase:
    "Dispatch shows the time since Legion first recorded the issue in its current Legion phase.",
};

const HOUR_MS = 3_600_000;
/** What a column that holds a duration filters by: four lengths, in their order, and None for
 *  a row with no duration. The address keeps the ids (`f.waited=under-1h`). */
export const DURATION_BUCKETS = ["under-1h", "1h-1d", "1d-7d", "over-7d", "none"] as const;
const DURATION_NAMES: Readonly<Record<string, string>> = {
  "under-1h": "Under 1 hour",
  "1h-1d": "1 hour to 1 day",
  "1d-7d": "1 to 7 days",
  "over-7d": "Over 7 days",
  none: "None",
};
/** The bucket a duration falls in: each length takes its lower bound and not its upper. */
export function durationBucket(ms: number | undefined): (typeof DURATION_BUCKETS)[number] {
  if (ms === undefined) return "none";
  if (ms < HOUR_MS) return "under-1h";
  if (ms < 24 * HOUR_MS) return "1h-1d";
  return ms < 7 * 24 * HOUR_MS ? "1d-7d" : "over-7d";
}
/** The filter of a column that holds a duration, `ms` of a row or undefined with none. */
const duration = (ms: (item: Item) => number | undefined): ColumnFilterSpec => ({
  kind: "picks",
  values: (item) => [durationBucket(ms(item))],
  order: DURATION_BUCKETS,
  show: (value) => DURATION_NAMES[value] ?? value,
  always: DURATION_BUCKETS,
});

/** Review rounds' filter: none, one, two, three or more, or None for an issue with no Legion
 *  work. The address keeps the ids (`f.review_rounds=3-or-more`). */
const ROUNDS = ["0", "1", "2", "3-or-more", "none"] as const;
const roundsOf = (item: Item): (typeof ROUNDS)[number] => {
  const rounds = item.facts?.review_rounds;
  if (rounds === undefined) return "none";
  return rounds >= 3 ? "3-or-more" : (String(rounds) as "0" | "1" | "2");
};

/** A Legion role as the Workers column names it: `implementer` is Implementer. */
export const workerName = (role: string) =>
  role === "" ? role : `${role[0]?.toUpperCase()}${role.slice(1)}`;

/** The pick of a row with no value in a column, kept in the address as `none`: an empty value
 *  there takes the view's own value away rather than picking anything. */
const NO_VALUE = "none";

export const COLUMNS: readonly ColumnSpec[] = [
  { id: "select", size: 44, min: 44, pinned: true, sortable: false },
  {
    id: "priority",
    size: 132,
    min: 132,
    pinned: true,
    filter: one((item) => String(item.issue.priority ?? ""), {
      order: ["0", "1", "2", "3", ""],
      show: priorityName,
    }),
  },
  {
    id: "queue",
    size: 124,
    min: 124,
    // The address keeps the values `Queued` and `Not queued` (`f.queue=Queued`); the reader
    // sees the set named by the Legion queue.
    filter: one((item) => (item.queued ? "Queued" : "Not queued"), {
      order: ["Queued", "Not queued"],
      show: (value) => (value === "Queued" ? "In Legion queue" : "Not in Legion queue"),
      groups: false,
    }),
  },
  {
    id: "key",
    size: 124,
    min: 124,
    compactMin: 124,
    cellMin: true,
    pinned: true,
    filter: { kind: "text", value: (item) => item.issue.key },
  },
  {
    // Two lines of the title at 240 px hold about 60 characters, half of a median INFRA title
    // (115), so its subject reads; and with Select, Priority and Key the pinned part is 540 px
    // at most, under the pinned share of a 974 px table (1024 px), so it stays pinned there.
    id: "title",
    size: 380,
    min: 240,
    compactMin: 120,
    cellMin: true,
    pinned: true,
    filter: { kind: "text", value: (item) => item.issue.title },
  },
  {
    // The widest status chip whole ("NEEDS REVIEW", 134 px) with the cell's padding: the cell,
    // not the header, sets this minimum.
    id: "status",
    size: 150,
    min: 150,
    cellMin: true,
    filter: {
      kind: "picks",
      values: (item) => [item.issue.status],
      group: (item) => item.issue.status,
      order: issueStatuses,
      // A `?status=` this bundle does not know stays under its own text: the filter applies
      // and matches nothing, which is what the Board has always done (`statusText`).
      show: statusText,
      // Shared with the Board, and every status is offered even before its rows are loaded.
      param: "status",
      always: issueStatuses,
    },
  },
  {
    // The column of the board the issue's card is in: its Legion phase, beside its status and
    // never merged with it. An issue the daemon holds no card for has none (`none`).
    id: "legion_phase",
    size: 168,
    min: 168,
    board: true,
    filter: one((item) => item.card?.column ?? NO_VALUE, {
      order: [...legionPhaseIds, NO_VALUE],
      show: (value) => (value === NO_VALUE ? "None" : phaseLabel(value)),
    }),
  },
  {
    id: "assignee",
    size: 150,
    min: 140,
    filter: one((item) => item.issue.assignee ?? "Unassigned"),
  },
  {
    id: "author",
    size: 180,
    min: 156,
    filter: {
      kind: "picks",
      values: (item) => (item.mine ? [item.author, ME] : [item.author]),
      group: (item) => item.author,
      show: (value) => (value === ME ? "Me" : value),
    },
  },
  {
    id: "labels",
    size: 180,
    min: 124,
    filter: {
      kind: "picks",
      // The project's own spelling of each label, which is the Board's too: one `?label=` is
      // one cache key and one ticked option in both views. Matching is case-insensitive, so a
      // link in another casing still picks the same rows.
      values: (item) => item.issue.labels ?? [],
      match: "all",
      param: "label",
    },
  },
  {
    // The claim chip whole: "Claimed by", the holder's name (which truncates, down to 48 px),
    // the time and "not running" - 305 px for the widest INFRA and GTM chips - and the padding.
    id: "claim",
    size: 324,
    min: 324,
    cellMin: true,
    filter: {
      kind: "picks",
      values: (item) => (item.claimFree ? [item.claimedBy, UNCLAIMED_FACET] : [item.claimedBy]),
      group: (item) => item.claimedBy,
    },
  },
  {
    // Whether the issue has an open ask, not how many: the address keeps `any` and `none`. A
    // link written when the filter picked a count (`f.asks=0`, `f.asks=2`) keeps its rows: 0
    // reads as `none`, and any other count as `any`.
    id: "asks",
    size: 152,
    // Its header and its filter button measure 126 px at 1280 px and wider, 138 px below it,
    // which is what Open's first screen at 1440 px has room for.
    min: 140,
    filter: {
      kind: "picks",
      values: (item) => [item.issue.open_asks > 0 ? "any" : "none"],
      order: ["any", "none"],
      show: (value) =>
        value === "any" ? "With open asks" : value === "none" ? "No open asks" : value,
      always: ["any", "none"],
      normalize: (value) => (/^\d+$/.test(value) ? (Number(value) > 0 ? "any" : "none") : value),
    },
  },
  {
    id: "created",
    size: 132,
    min: 132,
    filter: one((item) => ageOf(item.issue.created_at), { order: AGES }),
  },
  {
    id: "updated",
    size: 136,
    min: 136,
    filter: one((item) => ageOf(item.issue.updated_at), { order: AGES }),
  },
  {
    id: "closed",
    size: 124,
    min: 124,
    filter: { kind: "range", value: (item) => item.issue.closed_at },
  },
  {
    // The roles of the workers the daemon runs on the issue, or None.
    id: "roles",
    size: 136,
    min: 136,
    board: true,
    filter: {
      kind: "picks",
      values: (item) => {
        const roles = item.card?.card.roles ?? [];
        return roles.length === 0 ? ["none"] : [...roles];
      },
      order: [...LEGION_ROLES, "none"],
      show: (value) => (value === "none" ? "None" : workerName(value)),
    },
  },
  {
    // What an issue the daemon holds capacity for is doing (the board's `slot_use`).
    id: "activity",
    size: 168,
    min: 168,
    board: true,
    filter: one((item) => item.card?.card.slot_use ?? NO_VALUE, {
      order: [...ACTIVITIES, NO_VALUE],
      show: (value) => (value === NO_VALUE ? "None" : activityLabel(value)),
      groups: false,
    }),
  },
  {
    id: "waited",
    size: 176,
    min: 176,
    board: true,
    filter: duration((item) => ageMs(item.card?.card.waiting?.since)),
  },
  {
    // Why the card waits (the board's `waiting.reason`): set for a card in the queue, and for
    // a root that waits on a person outside it.
    id: "waiting_reason",
    size: 164,
    min: 164,
    board: true,
    filter: one((item) => item.card?.card.waiting?.reason ?? NO_VALUE, {
      order: [...WAITING_REASONS, NO_VALUE],
      show: (value) => (value === NO_VALUE ? "None" : waitingLabel(value)),
      groups: false,
    }),
  },
  {
    id: "time_in_phase",
    size: 224,
    min: 224,
    board: true,
    filter: duration((item) => ageMs(item.card?.card.phase_since)),
  },
  {
    id: "work_hours",
    size: 148,
    min: 148,
    board: true,
    filter: duration((item) => {
      const hours = workHoursOf(item);
      return hours === undefined ? undefined : hours * HOUR_MS;
    }),
  },
  {
    id: "review_rounds",
    size: 180,
    min: 180,
    board: true,
    filter: {
      kind: "picks",
      values: (item) => [roundsOf(item)],
      order: ROUNDS,
      show: (value) => (value === "3-or-more" ? "3 or more" : value === "none" ? "None" : value),
      always: ROUNDS,
    },
  },
  {
    // The issue's place in its Legion tree: the root, a child under it, or None for an issue
    // with no Legion work. The address keeps `root`, `child` and `none`.
    id: "legion",
    size: 156,
    min: 156,
    board: true,
    filter: {
      kind: "picks",
      values: (item) =>
        item.facts === undefined ? ["none"] : [item.facts.tree_depth === 0 ? "root" : "child"],
      order: ["root", "child", "none"],
      show: (value) => ({ root: "Root", child: "Child", none: "None" })[value] ?? value,
      always: ["root", "child", "none"],
    },
  },
  {
    id: "phase_hours",
    size: 284,
    min: 284,
    // On a phone it holds its stacked header (its name over its Share/Hours control and its
    // filter) and its bar, and fits in the room beside the pinned key at 390 px (172 px).
    compactMin: 156,
    board: true,
    filter: {
      kind: "picks",
      values: (item) => phaseHours(item.facts).phases.map(([phase]) => phase),
      order: workPhaseIds,
      show: workPhaseLabel,
    },
  },
];
const columnSpec: ReadonlyMap<string, ColumnSpec> = new Map(
  COLUMNS.map((column) => [column.id, column])
);
export const columnFilterSpec = (id: string): ColumnFilterSpec | undefined =>
  columnSpec.get(id)?.filter;
/** The columns a URL may name in `sort=` or a filter parameter: every one a reader controls. */
export const columnIds: readonly string[] = COLUMNS.filter(
  (column) => column.sortable !== false || column.filter !== undefined
).map((column) => column.id);
const groupable: readonly string[] = COLUMNS.filter(
  (column) => column.filter?.kind === "picks" && column.filter.group !== undefined
).map((column) => column.id);
export const canGroupBy = (id: string) => groupable.includes(id);
/** How much wider a button is below 1280 px, where every button is a 44 px touch target
 *  (`styles.css`), than a filter button's 32 px from 1280 px. */
const TOUCH_TARGET_GAIN = 44 - 32;
const minimumsFor = (touch: boolean): Readonly<Partial<Record<string, number>>> =>
  Object.fromEntries(
    COLUMNS.map((column) => [
      column.id,
      touch || column.filter === undefined || column.cellMin
        ? column.min
        : column.min - TOUCH_TARGET_GAIN,
    ])
  );
const TOUCH_MINIMUMS = minimumsFor(true);
const WIDE_MINIMUMS = minimumsFor(false);
/** Each registry column's minimum by id, with the buttons in use: 44 px touch targets below
 *  1280 px (`touch`), 32 px filter buttons from 1280 px. Looked up by a column id, so a lookup
 *  can miss: a column the registry gives no such width. */
export const columnMinimums = (touch: boolean) => (touch ? TOUCH_MINIMUMS : WIDE_MINIMUMS);
export const COMPACT_COLUMN_MIN: Readonly<Partial<Record<string, number>>> = Object.fromEntries(
  COLUMNS.flatMap((column) =>
    column.compactMin === undefined ? [] : [[column.id, column.compactMin]]
  )
);
/** A column's narrowest on a phone, which has touch targets: its phone minimum where the
 *  registry gives one. */
const phoneMin = (id: ColumnId): number => COMPACT_COLUMN_MIN[id] ?? TOUCH_MINIMUMS[id] ?? 0;

/** The layout rules every view's table shares, for `useTableLayout`: every registry column, at
 *  the registry's starting width and never below its minimum. Select, Priority, Key and Title
 *  stay pinned within 60% of a box 640 px wide or wider, Title narrowing first. On a phone,
 *  where the key and the title are what identify a row, Select and Key stay pinned and Title,
 *  the first column after them, takes the rest of the box down to its phone minimum: a row at
 *  rest reads its key and its title whole, with no column cut at the right edge, and the
 *  columns after Title scroll in under the key. */
function layoutColumns(shown: readonly ColumnId[] | undefined, touch: boolean): LayoutColumns {
  const visible = shown === undefined ? undefined : new Set<ColumnId>(["select", ...shown]);
  const pinned = COLUMNS.filter(
    (column) => column.pinned && (visible === undefined || visible.has(column.id))
  ).map(({ id }) => id);
  return {
    ids: COLUMNS.map((column) => column.id),
    rowAttribute: ISSUE_ROW_ATTRIBUTE,
    hiddenByDefault:
      visible === undefined
        ? { updated: false }
        : Object.fromEntries(COLUMNS.flatMap(({ id }) => (visible.has(id) ? [] : [[id, false]]))),
    sizes: Object.fromEntries(COLUMNS.map((column) => [column.id, column.size])),
    minimums: columnMinimums(touch),
    pins: {
      wide: { columns: pinned, share: 0.6 },
      phone: {
        columns: (["select", "key"] satisfies ColumnId[]).filter(
          (id) => visible === undefined || visible.has(id)
        ),
        // Just under the whole box: its two border pixels are not the rows'.
        share: 0.99,
        widths: { key: phoneMin("key"), phase_hours: phoneMin("phase_hours") },
        flexibleMin: phoneMin("title"),
      },
      flexible: "title" satisfies ColumnId,
    },
  };
}

/** The table's columns with Updated hidden until a reader shows it: the layout every saved
 *  `dispatch.project-table.<project>` record was written for, at the touch minimums. */
export const ISSUE_TABLE_COLUMNS: LayoutColumns = layoutColumns(undefined, true);

const viewLayouts = new Map<string, LayoutColumns>();
/** One view's layout rules: the view's default columns shown, every other column hidden until
 *  the reader shows it, the pinned columns the view shows, and the minimums of the buttons in
 *  use (`touch`). Held per set of columns and button size, since the stored layout is read
 *  again whenever the rules' identity changes. */
export function viewLayoutColumns(shown: readonly ColumnId[], touch: boolean): LayoutColumns {
  const key = `${shown.join(",")}|${touch}`;
  const known = viewLayouts.get(key);
  if (known !== undefined) return known;
  const made = layoutColumns(shown, touch);
  viewLayouts.set(key, made);
  return made;
}

/** Whether a column's source is the board route. */
export const isBoardColumn = (id: string) => columnSpec.get(id)?.board === true;

/** What this table holds in the address, in the shape every table holds it
 *  (`lib/page-address.ts`); `readUrl` and `writeUrl` below are this table's own spelling of it. */
export type View = TableAddressView;
/** The Open view, which is what an address that names no view opens. */
export const defaultView: View = {
  sorting: [{ id: "queue", desc: false }],
  grouping: [],
  filters: [{ id: "status", value: [...openIssueStatuses] }],
  search: "",
  page: 0,
  pageSize: 50,
};
/** The server matches a label on `lower(label)`, so the table does too: a Todo issue labelled
 *  `Legion` is in Legion's queue. */
export const isQueued = (issue: Pick<IssueSummary, "status" | "labels">) =>
  issue.status === "todo" && (issue.labels ?? []).some((value) => value.toLowerCase() === "legion");

/** What the rows read beyond the listing: the board route's answer, whether the project has a
 *  Legion daemon at all, and who the reader is. */
export interface ItemContext {
  readonly board?: LegionBoardResponse;
  readonly hasLegion?: boolean;
  readonly viewer?: string;
}

/** Every card of the board by its issue, with the column it sits in. A card whose issue is not
 *  in the listing names no row. */
export function cardsByKey(board: LegionBoardResponse | undefined): Map<string, BoardCardRow> {
  const cards = new Map<string, BoardCardRow>();
  for (const column of board?.columns ?? [])
    for (const card of column.cards ?? []) cards.set(card.key, { card, column: column.id });
  return cards;
}

/** Whether a card stands in Legion's queue: in the queue column, or given a place in it. */
const cardQueued = ({ card, column }: BoardCardRow) =>
  (card.waiting_place ?? undefined) !== undefined || column === "queue";

export function buildItems(
  issues: readonly IssueSummary[],
  label: (actor: Actor) => string,
  isFree: (issue: IssueSummary) => boolean = (issue) => issue.claim === null,
  { board, hasLegion = false, viewer }: ItemContext = {}
): Item[] {
  // Rank's alphabet is byte-ordered, not locale-ordered.
  const queue = issues
    .filter(isQueued)
    .sort((a, b) =>
      a.rank < b.rank
        ? -1
        : a.rank > b.rank
          ? 1
          : compareTimestamps(a.created_at, b.created_at) || a.key.localeCompare(b.key)
    );
  const positions = new Map(queue.map((issue, index) => [issue.key, index + 1]));
  const cards = hasLegion ? cardsByKey(board) : new Map<string, BoardCardRow>();
  const facts = hasLegion ? (board?.issues ?? {}) : {};
  const isMine = (actor: Actor | null) =>
    viewer !== undefined && actor !== null && (actor.id === viewer || actor.owner === viewer);
  return issues.map((issue) => {
    const author = issue.created_by ? label(issue.created_by) : "Unknown";
    const claimedBy = issue.claim ? label(issue.claim.actor) : "Nobody";
    // A done issue keeps no card: the daemon gives cards to open issues.
    const card = issue.status === "done" ? undefined : cards.get(issue.key);
    const place = hasLegion ? (card?.card.waiting_place ?? undefined) : positions.get(issue.key);
    return {
      issue,
      queue: positions.get(issue.key),
      author,
      claimedBy,
      claimFree: isFree(issue),
      search: [
        issue.key,
        issue.title,
        issue.assignee ?? "",
        author,
        claimedBy,
        ...(issue.labels ?? []),
      ].join(" "),
      ...(card === undefined ? {} : { card }),
      ...(facts[issue.key] === undefined ? {} : { facts: facts[issue.key] }),
      place,
      queued: card === undefined ? place !== undefined : cardQueued(card),
      mine: isMine(issue.created_by),
    };
  });
}
function withinOneEdit(a: string, b: string): boolean {
  if (Math.abs(a.length - b.length) > 1) return false;
  let i = 0,
    j = 0,
    edits = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      i++;
      j++;
      continue;
    }
    if (++edits > 1) return false;
    if (a.length > b.length) i++;
    else if (b.length > a.length) j++;
    else {
      i++;
      j++;
    }
  }
  return edits + a.length - i + b.length - j <= 1;
}
export function matchesSearch(item: Item, query: string): boolean {
  // Nothing typed matches everything, and the hot caller — the facet counts — asks on every
  // render, so this answers before any work is done.
  if (query.trim() === "") return true;
  let words: string[] | undefined;
  const searchWords = () => {
    if (words === undefined) words = item.search.toLowerCase().split(/[^\p{L}\p{N}]+/u);
    return words;
  };
  return query
    .trim()
    .toLowerCase()
    .split(/\s+/)
    .every(
      (word) =>
        rankItem(item.search, word, { threshold: rankings.CONTAINS }).passed ||
        (word.length >= 5 && searchWords().some((candidate) => withinOneEdit(candidate, word)))
    );
}
export function facets(item: Item, id: string): string[] {
  const filter = columnFilterSpec(id);
  return filter?.kind === "picks" ? filter.values(item) : [];
}
export function matchesFilters(
  item: Item,
  filters: ColumnFiltersState,
  except?: string,
  nowMs = Date.now()
): boolean {
  return filters.every(({ id, value }) => {
    if (id === except) return true;
    const filter = columnFilterSpec(id);
    if (filter?.kind === "text")
      return filter.value(item).toLowerCase().includes(String(value).toLowerCase());
    if (filter?.kind === "range") return inWindow(filter.value(item), String(value), nowMs);
    if (!Array.isArray(value) || value.length === 0) return true;
    return filter?.kind === "picks" && filter.match === "all"
      ? value.every((picked: string) =>
          facets(item, id).some((entry) => entry.toLowerCase() === picked.toLowerCase())
        )
      : facets(item, id).some((entry) => value.includes(entry));
  });
}
/** The headings that stand for "none of them", wherever a column has one: they read last, after
 *  every named group. */
const EMPTY_GROUPS: readonly string[] = ["Nobody", "Unassigned"];
/** Which of two of column `id`'s groups comes first: the order its filter lists its values in,
 *  and by name where the registry gives none — numbers inside a name counted as numbers, so
 *  `CORE-2` reads before `CORE-10`, and the empty group last. A group's place is its column's,
 *  not the row sort's, so the headings read P0, P1, P2, P3 however the rows are sorted. */
export function compareGroups(id: string, a: string, b: string): number {
  const filter = columnFilterSpec(id);
  if (filter?.kind !== "picks" || filter.group === undefined) return 0;
  const order = filter.order;
  if (order === undefined) {
    const empty = Number(EMPTY_GROUPS.includes(a)) - Number(EMPTY_GROUPS.includes(b));
    return empty !== 0 ? empty : a.localeCompare(b, undefined, { numeric: true });
  }
  const place = (value: string) => {
    const index = order.indexOf(value);
    return index === -1 ? order.length : index;
  };
  return place(a) - place(b);
}
/** The heading `item` groups under in column `id`, which is what `compareGroups` orders. */
export function groupValue(id: string, item: Item): string {
  const filter = columnFilterSpec(id);
  return filter?.kind === "picks" && filter.group !== undefined ? filter.group(item) : "";
}
export function facetCounts(
  items: readonly Item[],
  id: string,
  filters: ColumnFiltersState,
  search: string
): Map<string, number> {
  const counts = new Map<string, number>();
  for (const item of items)
    if (matchesFilters(item, filters, id) && matchesSearch(item, search)) {
      for (const value of new Set(facets(item, id)))
        counts.set(value, (counts.get(value) ?? 0) + 1);
    }
  return counts;
}
const paramFor = (id: string) => {
  const filter = columnFilterSpec(id);
  return filter?.kind === "picks" && filter.param !== undefined ? filter.param : `f.${id}`;
};
/** The column a filter parameter in a URL belongs to. */
const columnForParam: ReadonlyMap<string, string> = new Map(
  COLUMNS.map((column) => [paramFor(column.id), column.id])
);
/** The parameters this table's view is spelled in, which a write rewrites whole. */
const TABLE_KEYS = ["sort", "group", "q", "page", "size", "nofilter", "unclaimed"];

/**
 * The table's view out of an address that spells every part of it: no default is put back, so
 * an address with no `status` lists every status and one with no `sort` sorts by nothing. The
 * views (`issues-views.ts`'s `expandView`) write out a view's own parameters before this reads
 * them.
 */
export function readTableAddress(params: URLSearchParams): View {
  const sorting = (params.get("sort") ?? "")
    .split(",")
    .map((part) => ({ id: part.replace(/^-/, ""), desc: part.startsWith("-") }))
    .filter(({ id }) => columnIds.includes(id));
  const grouping = params.get("group") ?? "";
  const filters: ColumnFiltersState = [...new Set(params.keys())].flatMap((key) => {
    const id = columnForParam.get(key) ?? "";
    const filter = columnFilterSpec(id);
    return columnIds.includes(id)
      ? [
          {
            id,
            value:
              filter?.kind === "text" || filter?.kind === "range"
                ? params.get(key)
                : params
                    .getAll(key)
                    .map((value) =>
                      filter?.kind === "picks" && filter.normalize ? filter.normalize(value) : value
                    ),
          },
        ]
      : [];
  });
  // `?unclaimed=1` is the Board strip's own parameter and what shared List links carry: it picks
  // the Claimed by filter's "Nobody or lapsed", so both layouts read one URL.
  if (params.get("unclaimed") === "1") {
    const claim = filters.find((filter) => filter.id === "claim");
    if (claim === undefined) filters.push({ id: "claim", value: [UNCLAIMED_FACET] });
    else
      claim.value = [
        ...new Set([...(Array.isArray(claim.value) ? claim.value : []), UNCLAIMED_FACET]),
      ];
  }
  const page = Number(params.get("page"));
  const size = Number(params.get("size"));
  return {
    sorting,
    grouping: canGroupBy(grouping) ? [grouping] : [],
    filters,
    search: capSearchQuery(params.get("q") ?? ""),
    page: Number.isSafeInteger(page) && page > 0 ? page - 1 : 0,
    pageSize: PAGE_SIZES.includes(size) ? size : defaultView.pageSize,
  };
}

/** `state` spelled out whole over `params`: every filter, the status pick included, and the
 *  sort as it stands. `compressView` then leaves out what the view already says. */
export function writeTableAddress(params: URLSearchParams, state: View): URLSearchParams {
  const next = copyAddress(params);
  for (const key of [...next.keys()])
    if (key.startsWith("f.") || columnForParam.has(key) || TABLE_KEYS.includes(key))
      next.delete(key);
  const sort = state.sorting.map(({ id, desc }) => `${desc ? "-" : ""}${id}`).join(",");
  if (sort !== "") next.set("sort", sort);
  if (state.grouping[0]) next.set("group", state.grouping[0]);
  for (const filter of state.filters) {
    const values = Array.isArray(filter.value) ? filter.value : [filter.value];
    for (const value of values) {
      if (filter.id === "claim" && value === UNCLAIMED_FACET) next.set("unclaimed", "1");
      else next.append(paramFor(filter.id), String(value));
    }
  }
  if (state.search) next.set("q", state.search);
  if (state.page) next.set("page", String(state.page + 1));
  if (state.pageSize !== defaultView.pageSize) next.set("size", String(state.pageSize));
  return next;
}

/** The table's view as the address names it: the view `view=` names (Open where it names
 *  none), with every parameter of the address over the view's own. */
export function readUrl(params: URLSearchParams): View {
  return readTableAddress(expandView(params));
}

/** `state` written over `params` as the difference from the view `params` names, so a link
 *  carries only what the reader changed and a view left as it is carries `view=` alone. */
export function writeUrl(params: URLSearchParams, state: View): URLSearchParams {
  return compressView(writeTableAddress(params, state));
}

/** What a narrowing that names itself in no other way is called. */
const DEFAULT_SELECTION_LABEL = "Selected issues";

/** The issues behind a number the reader clicked elsewhere, as the URL holds them: the keys,
 *  and the words naming them. It is the address's own narrowing, not one table's control, so it
 *  takes no prefix: one page shows one selection however many tables it draws. */
export interface IssueSelection {
  readonly keys: ReadonlySet<string>;
  readonly label: string;
}
export function readSelection(params: URLSearchParams): IssueSelection | null {
  const only = params.get("only");
  if (only === null) return null;
  return {
    keys: new Set(only.split(",").filter(Boolean)),
    label: params.get("onlyLabel") ?? DEFAULT_SELECTION_LABEL,
  };
}
/** Narrows the table to these issues. They are mostly finished ones, so the Status filter is
 *  lifted (closed issues load) and the page goes back to the first; other filters stay. */
export function selectIssues(
  params: URLSearchParams,
  keys: readonly string[],
  label: string
): URLSearchParams {
  const next = copyAddress(params);
  next.set("only", keys.join(","));
  next.set("onlyLabel", label);
  next.delete("status");
  next.set("nofilter", "1");
  next.delete("page");
  return next;
}
/** Drops that narrowing, leaving every other control as it is. */
export function clearPicked(params: URLSearchParams): URLSearchParams {
  const next = copyAddress(params);
  next.delete("only");
  next.delete("onlyLabel");
  next.delete("page");
  return next;
}

/** The selected issues that are in Legion's queue, in the order the queue holds them. */
export function queuedInOrder(items: readonly Item[], keys: readonly string[]): string[] {
  return items
    .filter((item): item is Item & { queue: number } => item.queue !== undefined)
    .sort((a, b) => a.queue - b.queue)
    .filter((item) => keys.includes(item.issue.key))
    .map((item) => item.issue.key);
}

/**
 * The rank writes that move `keys` to the head of Legion's queue, each one placing its issue
 * before the one the previous write placed. Rows already ahead of everything unselected are
 * left alone, so a finished action writes nothing, and a resumed one writes only what is left:
 * the same function serves the first attempt and the one that picks it up again.
 */
export function startNextWrites(
  items: readonly Item[],
  keys: readonly string[]
): { key: string; input: UpdateIssueInput }[] {
  const queue = items
    .filter((item) => item.queue !== undefined)
    .sort((a, b) => (a.queue ?? 0) - (b.queue ?? 0))
    .map((item) => item.issue.key);
  const queued = new Set(queue);
  const selected = keys.filter((key) => queued.has(key));
  const selectedKeys = new Set(selected);
  let before = queue.find((key) => !selectedKeys.has(key));
  const operations: { key: string; input: UpdateIssueInput }[] = [];
  for (const key of selected.reverse()) {
    if (before && queue.indexOf(key) > queue.indexOf(before)) {
      operations.push({ key, input: { rank: { before } } });
      queue.splice(queue.indexOf(key), 1);
      queue.splice(queue.indexOf(before), 0, key);
    }
    before = key;
  }
  return operations;
}
