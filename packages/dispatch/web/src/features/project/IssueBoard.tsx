import {
  type CollisionDetection,
  closestCorners,
  DndContext,
  type DragEndEvent,
  MouseSensor,
  pointerWithin,
  TouchSensor,
  useDraggable,
  useDroppable,
  useSensor,
  useSensors,
} from "@dnd-kit/core";
import { useQueryClient } from "@tanstack/react-query";
import { type CSSProperties, type ReactNode, useCallback, useMemo, useState } from "react";
import { Link } from "react-router-dom";

import { ApiError, issuePatchRequest } from "../../api/client";
import { boardMoveMutationKey } from "../../api/queries";
import type { IssueSummary, UpdateIssueInput, UserState } from "../../api/types";
import { AttentionBadge } from "../../components/Badge";
import { LabelPill, Pill, StatusPill } from "../../components/Pill";
import {
  borderDefault,
  borderStrong,
  card,
  cardHoverBorder,
  dangerText,
  focusVisibleRing,
  linkHoverText,
  linkText,
  selectedCardBorder,
  surfaceMutedBg,
  textMutedOnCanvas,
  textMutedOnSurface,
  textPrimaryOnSurface,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { ClaimChip } from "../issue/ClaimChip";
import { issuePriority } from "../issue/issue-priority";
import { PriorityControl } from "../issue/PriorityControl";
import { ProgressChips } from "../issue/ProgressChips";
import { UnreachableRouteMarker } from "../issue/RouteReach";
import { formatPhaseTime, formatWorkTime } from "../legion/lib/format";
import { closeRefPreview, referenceTriggerProps } from "../refs/RefPreview";
import { buildIssuePath } from "../refs/routes";
import { useKeymap, useKeymapScope } from "../shell/keymap";
import { closestMatching } from "../shell/roving";
import { useKeyedWrite } from "../writes/useKeyedWrite";
import type { CardPlace } from "./board-hold";
import {
  type IssueStatus,
  isIssueStatus,
  issueStatuses,
  laneLabel,
  type PriorityLane,
  priorityLanes,
  statusLabel,
} from "./board-model";
import { projectIssuesQueryKey } from "./issue-filters";
import { LEGION_PHASES, phaseLabel } from "./issues-legion";
import { type Item, workerName, workHoursOf } from "./issues-table-model";
import { issueIsUnread, UnreadDot } from "./UnreadDot";

const boardCollisionDetection: CollisionDetection = (args) => {
  const pointerCollisions = pointerWithin(args);
  return pointerCollisions.length > 0 ? pointerCollisions : closestCorners(args);
};

const boardFocusRing = `outline-none focus-visible:ring-2 ${focusVisibleRing}`;

/** The card `node` sits in, or is. */
const cardAround = (node: Element | null): HTMLElement | null =>
  closestMatching(node, "[data-board-card]");

/** One fact of a card with Legion work: its name, then its value. */
function Fact({ hook, label, value }: { hook: string; label: string; value: string }): ReactNode {
  return (
    <span className={`inline-flex gap-1 ${textSecondaryOnSurface}`} data-legion-fact={hook}>
      <span className={textMutedOnSurface}>{label}</span>
      <span>{value}</span>
    </span>
  );
}

/** What a card with a card on the Legion board shows beside today's card: every field the
 *  Legion view's card showed, from the board route. */
function LegionFacts({ item, nowMs }: { item: Item; nowMs: number }): ReactNode {
  const legion = item.card?.card;
  if (legion === undefined) return null;
  const roles = (legion.roles ?? []).map(workerName);
  const blockedBy = legion.blocked_by ?? legion.waiting?.blocked_by ?? [];
  const workHours = workHoursOf(item);
  // A measure the server has no value for is left out, never drawn as 0 or a dash.
  return (
    <>
      <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
        {legion.phase_since === null ? null : (
          <Fact
            hook="phase-time"
            label="Time in Legion phase"
            value={formatPhaseTime(legion.phase_since, nowMs)}
          />
        )}
        {workHours === undefined ? null : (
          <Fact hook="work-time" label="Work time" value={formatWorkTime(workHours)} />
        )}
        {/* A card whose issue has no worker running leaves the fact out rather than say None:
            every queued card would carry it. */}
        {roles.length === 0 ? null : <Fact hook="roles" label="Workers" value={roles.join(", ")} />}
      </div>
      {blockedBy.length === 0 ? null : (
        <p
          className={`mt-2 border-t pt-2 text-xs ${borderDefault} ${textSecondaryOnSurface}`}
          data-legion-fact="blocked-by"
        >
          Blocked by {blockedBy.join(", ")}
        </p>
      )}
      {legion.waiting_on === null ? null : (
        <p
          className={`mt-2 border-t pt-2 text-xs ${borderDefault} ${textSecondaryOnSurface}`}
          data-legion-fact="waiting-on"
        >
          Waiting on {legion.waiting_on.kind}
          {legion.waiting_on.since === null
            ? ""
            : ` since ${new Date(legion.waiting_on.since).toLocaleString()}`}
          {legion.waiting_on.answered_at === null ? "" : ", answered"}
        </p>
      )}
      {legion.children === 0 ? null : (
        <p className={`mt-2 text-xs ${textMutedOnSurface}`} data-legion-fact="children">
          {legion.children} {legion.children === 1 ? "issue" : "issues"} of this tree not started
        </p>
      )}
    </>
  );
}

/** One card: today's card (key, title, priority, claim, progress, labels, open asks, unread),
 *  and for an issue with a card on the Legion board its Legion fields. The whole card is the
 *  drag handle: an 8 px mouse move or a 150 ms touch hold lifts it. */
function BoardCard({
  item,
  nowMs,
  unread,
}: {
  item: Item;
  nowMs: number;
  unread: boolean;
}): ReactNode {
  const { issue } = item;
  const { attributes, isDragging, listeners, setNodeRef, transform } = useDraggable({
    attributes: { role: "article", roleDescription: "card", tabIndex: -1 },
    id: issue.key,
  });
  const drop = useDroppable({ id: `card:${issue.key}` });
  const setRefs = useCallback(
    (node: HTMLElement | null) => {
      setNodeRef(node);
      drop.setNodeRef(node);
    },
    [setNodeRef, drop.setNodeRef]
  );
  const style: CSSProperties = {
    transform:
      transform === null ? undefined : `translate3d(${transform.x}px, ${transform.y}px, 0)`,
  };
  return (
    <article
      aria-label={`${issue.key} ${issue.title}`}
      className={`cursor-grab rounded-xl border p-3 ${card} ${cardHoverBorder} ${boardFocusRing} ${
        isDragging ? "relative z-50 opacity-70" : ""
      } ${drop.isOver && !isDragging ? selectedCardBorder : ""}`}
      data-board-card={issue.key}
      ref={setRefs}
      style={style}
      {...attributes}
      {...listeners}
    >
      <Link
        className={`flex flex-col text-sm ${linkText} ${linkHoverText}`}
        data-issue-link
        to={buildIssuePath({ key: issue.key, kind: "issue" })}
        {...referenceTriggerProps({ key: issue.key, kind: "issue" })}
      >
        {/* The unread dot sits beside the key, as it does in the table's Key cell, so a card
            with no chips below shows no lone dot. */}
        <span className="inline-flex items-center gap-1.5 self-start font-semibold">
          {issue.key}
          {unread ? <UnreadDot /> : null}
          {item.place === undefined ? null : (
            <span className={`font-normal ${textMutedOnSurface}`}>#{item.place}</span>
          )}
        </span>
        <span className={`mt-1 self-start ${textPrimaryOnSurface}`}>{issue.title}</span>
      </Link>
      <div className="mt-3 flex flex-wrap items-center gap-2">
        {/* Every card edits its priority in place, the unset one from the "Priority" tag, and
            the Board's `p` key focuses this control. */}
        <PriorityControl
          disabled={issue.status === "done"}
          issueKey={issue.key}
          priority={issuePriority(issue.priority)}
        />
        <ClaimChip claim={issue.claim} stacked />
        <ProgressChips progress={issue.progress} />
        <UnreachableRouteMarker issue={issue} />
        {(issue.labels ?? []).map((label) => (
          <LabelPill key={label}>{label}</LabelPill>
        ))}
        {issue.open_asks === 0 ? null : (
          <span title={`${issue.open_asks} open asks`}>
            <AttentionBadge count={issue.open_asks} />
          </span>
        )}
      </div>
      <LegionFacts item={item} nowMs={nowMs} />
    </article>
  );
}

/** What the Board's columns are: the issues' statuses, or their Legion phases. */
export type BoardBy = "status" | "legion_phase";

/** A column's id, which is a status or a Legion phase, and `none` for the rail of the rows the
 *  daemon holds no card for. */
const NONE = "none";

/** The column a row is in on a Board of `by`. */
export const columnOf = (item: Item, by: BoardBy): string =>
  by === "status" ? item.issue.status : (item.card?.column ?? NONE);

/** A column's name: a status's own, or a phase's. */
const columnName = (id: string, by: BoardBy) =>
  by === "status"
    ? isIssueStatus(id)
      ? statusLabel(id)
      : id
    : id === NONE
      ? "None"
      : phaseLabel(id);

/** One column: its title, its count, and its cards; the whole column is its drop target. The
 *  count is the column's rows as they stand now, which a Board holding the reader's place draws
 *  at once while it holds the cards back. */
function BoardColumn({
  by,
  count,
  dropId,
  id,
  items,
  nowMs,
  userState,
}: {
  by: BoardBy;
  count: number;
  dropId: string;
  id: string;
  items: readonly Item[];
  nowMs: number;
  userState: UserState | undefined;
}): ReactNode {
  const { isOver, setNodeRef } = useDroppable({ id: dropId });
  return (
    <section
      aria-label={columnName(id, by)}
      className={`w-72 shrink-0 snap-start ${boardFocusRing}`}
      data-board-column={id}
      ref={setNodeRef}
      tabIndex={-1}
    >
      <header
        className={`min-h-[44px] rounded-xl border px-3 py-2 ${borderDefault} ${surfaceMutedBg} ${
          isOver ? selectedCardBorder : ""
        }`}
        data-testid="board-column-header"
      >
        <div
          className={`flex items-center justify-between gap-2 text-sm font-semibold ${textPrimaryOnSurface}`}
        >
          {by === "status" ? <StatusPill>{columnName(id, by)}</StatusPill> : columnName(id, by)}
          <Pill>{count}</Pill>
        </div>
      </header>
      {items.length === 0 ? (
        <div
          className={`mt-3 min-h-32 rounded-xl border border-dashed px-3 py-6 text-center text-xs ${textMutedOnCanvas} ${
            isOver ? selectedCardBorder : borderStrong
          }`}
        >
          Nothing here
        </div>
      ) : (
        <div className="mt-3 space-y-3">
          {items.map((item) => (
            <BoardCard
              item={item}
              key={item.issue.key}
              nowMs={nowMs}
              unread={issueIsUnread(item.issue, userState?.[item.issue.key]?.last_read_seq ?? 0)}
            />
          ))}
        </div>
      )}
    </section>
  );
}

/** A collapsed rail: Icebox while the reader has not turned it on, Done while the view lists no
 *  done issue, and None on a Board by Legion phase. It is a drop target like a column: a card
 *  dropped on the Done rail closes the issue. */
function Rail({
  by,
  count,
  dropId,
  id,
}: {
  by: BoardBy;
  count: number | undefined;
  dropId: string;
  id: string;
}): ReactNode {
  const { isOver, setNodeRef } = useDroppable({ id: dropId });
  return (
    <section
      aria-label={`${columnName(id, by)} (collapsed)`}
      className={`w-10 shrink-0 snap-start ${boardFocusRing}`}
      data-board-column={id}
      ref={setNodeRef}
      tabIndex={-1}
    >
      <div
        className={`flex h-full min-h-[52px] flex-col items-center gap-3 rounded-xl border px-1 py-3 ${borderDefault} ${surfaceMutedBg} ${
          isOver ? selectedCardBorder : ""
        }`}
        data-testid="board-column-rail"
      >
        {count === undefined ? null : <Pill>{count}</Pill>}
        <span
          className={`text-sm font-semibold tracking-wide ${textPrimaryOnSurface}`}
          style={{ writingMode: "vertical-rl" }}
        >
          {columnName(id, by)}
        </span>
      </div>
    </section>
  );
}

/**
 * The status Board's columns, in the status order: each status the view's Status filter picks
 * and every other status one of its rows is in, Done aside (it is the rail). Icebox is a column
 * only when the reader turns it on, and a rail otherwise.
 */
export function boardStatuses({
  rows,
  showIcebox,
  statusPicks,
}: {
  rows: readonly Item[];
  showIcebox: boolean;
  statusPicks: readonly string[] | undefined;
}): IssueStatus[] {
  const named = new Set<string>(statusPicks ?? []);
  for (const row of rows) named.add(row.issue.status);
  return issueStatuses.filter(
    (status) => named.has(status) && status !== "done" && (status !== "icebox" || showIcebox)
  );
}

/** A column's drop id: its id, inside a lane where the board has lanes. */
const dropIdOf = (column: string, lane: string | undefined) =>
  lane === undefined ? `column:${column}` : `lane:${lane}|column:${column}`;

/** Where a drop lands: a column by its id (and lane), a card by its key, or nothing the board
 *  knows. */
function readDrop(
  overId: string,
  rows: readonly Item[],
  placeOf: (item: Item) => { column: string; lane: PriorityLane }
): { column: string; key?: string; lane?: PriorityLane } | undefined {
  if (overId.startsWith("card:")) {
    const key = overId.slice("card:".length);
    const row = rows.find((item) => item.issue.key === key);
    return row === undefined ? undefined : { ...placeOf(row), key };
  }
  const match = /^(?:lane:([^|]+)\|)?column:(.+)$/.exec(overId);
  if (match === null || match[2] === undefined) return undefined;
  const lane = priorityLanes.find((candidate) => String(candidate) === match[1]);
  return { column: match[2], ...(match[1] === undefined ? {} : { lane: lane ?? null }) };
}

/**
 * The Board: a layout of the current view. It draws every row the view lists, the cards in each
 * column in the view's sort, in columns by status (main's status Board) or, in a project with a
 * Legion daemon, by Legion phase, as the Group by menu picks.
 *
 * On the status Board a drop on a column, or on the Done rail, sets that status. A sorted board
 * takes no reorder within a column (GitHub Projects' rule), because the sort puts the card back:
 * a drop within a column writes the rank only where the rank decides where the card lands, which
 * is while the view sorts by Queue, first first, and among cards that all have a Queue number or
 * all have none (the sort puts every card with one ahead of those without). Under any other sort
 * a reorder is refused with `reorderRefusal`, and a drop into another column writes the status
 * alone, the card landing where the sort puts it.
 *
 * On the Legion phase Board the daemon owns every column: a move within Queue writes the rank
 * under the same rule, and every other drop is refused.
 */
export function IssueBoard({
  by,
  counted,
  hasLegion,
  lanes,
  listsDone,
  placeOf,
  project,
  reorderRefusal,
  rows,
  showIcebox,
  statusPicks,
  userState,
}: {
  by: BoardBy;
  /** The rows the columns and lanes count: the view's rows as they stand now, where a Board
   *  holding the reader's place draws other rows (`useHeldBoard`); `rows` where unset. */
  counted?: readonly Item[];
  hasLegion: boolean;
  lanes: boolean;
  /** The view lists done issues: the Done rail opens into a column with their cards. */
  listsDone: boolean;
  /** Where a drawn row stands while the Board holds the reader's place; where its fields put it
   *  where unset. */
  placeOf?: (item: Item) => CardPlace;
  project: string;
  /** Why a card cannot be reordered within its column under the view's sort; undefined while
   *  the view sorts by Queue, first first. */
  reorderRefusal: string | undefined;
  /** The rows the Board draws, in the view's sort. */
  rows: readonly Item[];
  showIcebox: boolean;
  statusPicks: readonly string[] | undefined;
  userState: UserState | undefined;
}): ReactNode {
  const queryClient = useQueryClient();
  const [notice, setNotice] = useState<string>();
  const nowMs = Date.now();
  const countedRows = counted ?? rows;
  // Where a drawn card stands: its held place, else where its fields put it.
  const placeAt = (item: Item): { column: string; lane: PriorityLane } => {
    const held = placeOf?.(item);
    return {
      column: held?.column ?? columnOf(item, by),
      lane:
        held === undefined || held.lane === undefined
          ? issuePriority(item.issue.priority)
          : held.lane,
    };
  };
  const columnAt = (item: Item) => placeAt(item).column;
  const laneAt = (item: Item) => placeAt(item).lane;
  // The status columns: those the counted rows and the picked statuses name, and those a held
  // card still stands in.
  const statuses = useMemo(() => {
    const named = boardStatuses({ rows: countedRows, showIcebox, statusPicks });
    if (placeOf === undefined) return named;
    const held = new Set(rows.map((item) => placeOf(item).column));
    return issueStatuses.filter(
      (status) =>
        named.includes(status) ||
        (held.has(status) && status !== "done" && (status !== "icebox" || showIcebox))
    );
  }, [countedRows, placeOf, rows, showIcebox, statusPicks]);
  const patch = useKeyedWrite({
    inline: true,
    label: ({ key }: { input: UpdateIssueInput; key: string }) => `${key} moved on the board`,
    target: ({ key }) => `issue:${key}`,
    request: ({ input, key }) => issuePatchRequest(key, input),
    mutationKey: boardMoveMutationKey(project),
    scope: { id: `board:${project}` },
  });
  const sensors = useSensors(
    useSensor(MouseSensor, { activationConstraint: { distance: 8 } }),
    useSensor(TouchSensor, { activationConstraint: { delay: 150, tolerance: 5 } })
  );
  const openKey = [...projectIssuesQueryKey(project, []), "open"];

  const move = async (key: string, input: UpdateIssueInput) => {
    const previous = queryClient.getQueryData<IssueSummary[]>(openKey);
    if (previous !== undefined && input.status !== undefined) {
      void queryClient.cancelQueries({ queryKey: openKey, exact: true });
      queryClient.setQueryData(
        openKey,
        previous.map((issue) =>
          issue.key === key ? { ...issue, status: input.status ?? issue.status } : issue
        )
      );
    }
    try {
      await patch.mutateAsync({ input, key });
      setNotice(undefined);
    } catch (cause) {
      if (previous !== undefined) queryClient.setQueryData(openKey, previous);
      setNotice(cause instanceof ApiError ? cause.message : "Could not move the card.");
    } finally {
      await queryClient.invalidateQueries({ queryKey: projectIssuesQueryKey(project, []) });
    }
  };

  const onDragEnd = ({ active, over }: DragEndEvent) => {
    if (over === null) return;
    const key = String(active.id);
    const row = rows.find((item) => item.issue.key === key);
    const target = readDrop(String(over.id), rows, placeAt);
    if (row === undefined || target === undefined || target.key === key) return;
    const from = columnAt(row);
    // A drop into another lane takes that lane's priority, as today's lanes do.
    const lane = lanes ? target.lane : undefined;
    const priority =
      lane === undefined || lane === issuePriority(row.issue.priority) ? {} : { priority: lane };
    // Within its own column, and its own lane, a drop can only reorder.
    const reorders = target.column === from && Object.keys(priority).length === 0;
    const sameLane = (item: Item) => !lanes || laneAt(item) === laneAt(row);
    // The card a reorder lands beside: the one it is dropped on, else the column's last.
    const beside =
      target.key !== undefined
        ? rows.find((item) => item.issue.key === target.key)
        : rows
            .filter((item) => columnAt(item) === from && item.issue.key !== key && sameLane(item))
            .at(-1);
    const refusal = (): string | undefined =>
      reorderRefusal ??
      (beside === undefined || (beside.place === undefined) === (row.place === undefined)
        ? undefined
        : hasLegion
          ? "Legion orders its queue."
          : "Queued cards stay first.");
    if (by === "legion_phase") {
      if (target.column === "queue" && from !== "queue") {
        setNotice("Only Legion moves cards into Queue.");
        return;
      }
      if (target.column !== "queue" || !reorders) {
        setNotice("Only Legion moves cards between phases.");
        return;
      }
      const why = refusal();
      if (why !== undefined) {
        setNotice(why);
        return;
      }
      const column = rows.filter((item) => columnAt(item) === "queue" && sameLane(item));
      const neighbour = target.key ?? column.at(-1)?.issue.key;
      if (neighbour === undefined || neighbour === key) return;
      const movingUp =
        target.key !== undefined &&
        column.findIndex((item) => item.issue.key === key) >
          column.findIndex((item) => item.issue.key === target.key);
      void move(key, { rank: movingUp ? { before: neighbour } : { after: neighbour } });
      return;
    }
    if (!isIssueStatus(target.column)) return;
    if (reorders) {
      const why = refusal();
      if (why !== undefined) {
        setNotice(why);
        return;
      }
    }
    const input: UpdateIssueInput = {
      ...priority,
      ...(row.issue.status === target.column ? {} : { status: target.column }),
      ...(target.key === undefined || reorderRefusal !== undefined
        ? {}
        : { rank: { before: target.key } }),
    };
    if (Object.keys(input).length === 0) return;
    void move(key, input);
  };

  // Main's card keys for the priority: `p` on a focused card reaches its priority select, and
  // Escape goes back to the card, then out of the board.
  useKeymapScope("board");
  useKeymap("board", [
    {
      id: "priority",
      keys: "p",
      label: "Focus the card's priority",
      run: () => cardAround(document.activeElement)?.querySelector("select")?.focus(),
      when: () => cardAround(document.activeElement) !== null,
    },
    {
      id: "back",
      inEditable: true,
      keys: "Escape",
      label: "Back to the card, then out",
      run: () => {
        const active = document.activeElement;
        const around = cardAround(active);
        if (around === active) (active as HTMLElement).blur();
        else around?.focus();
      },
      when: () => cardAround(document.activeElement) !== null,
    },
  ]);

  // Icebox, while the reader has not turned it on, is a rail in its own place in the status
  // order (after Triage, before Backlog), as the Board on main draws it: for a view that lists
  // Icebox issues or picks the Icebox status. One rule for the whole board, so every lane draws
  // it.
  const iceboxRail =
    by === "status" &&
    !statuses.includes("icebox") &&
    (countedRows.some((item) => item.issue.status === "icebox") ||
      (statusPicks ?? []).includes("icebox"));
  const order: readonly string[] =
    by === "legion_phase"
      ? LEGION_PHASES.map((phase) => phase.id)
      : iceboxRail
        ? [...statuses, "icebox" as const].sort(
            (a, b) => issueStatuses.indexOf(a) - issueStatuses.indexOf(b)
          )
        : statuses;
  // The columns' counts are the counted rows', as they stand now; the cards are the drawn rows',
  // where the Board places them.
  const drawRow = (from: readonly Item[], counting: readonly Item[], lane: string | undefined) => {
    const columns = new Map<string, Item[]>();
    for (const item of from) {
      const id = columnAt(item);
      const list = columns.get(id) ?? [];
      list.push(item);
      columns.set(id, list);
    }
    const counts = new Map<string, number>();
    for (const item of counting) {
      const id = columnOf(item, by);
      counts.set(id, (counts.get(id) ?? 0) + 1);
    }
    return (
      <div className="flex w-max items-stretch gap-4">
        {order.map((id) =>
          id === "icebox" && iceboxRail ? (
            <Rail
              by={by}
              count={counts.get("icebox") ?? 0}
              dropId={dropIdOf("icebox", lane)}
              id="icebox"
              key={id}
            />
          ) : (
            <BoardColumn
              by={by}
              count={counts.get(id) ?? 0}
              dropId={dropIdOf(id, lane)}
              id={id}
              items={columns.get(id) ?? []}
              key={id}
              nowMs={nowMs}
              userState={userState}
            />
          )
        )}
        {by === "legion_phase" ? (
          <Rail by={by} count={counts.get(NONE) ?? 0} dropId={dropIdOf(NONE, lane)} id={NONE} />
        ) : listsDone ? (
          <BoardColumn
            by={by}
            count={counts.get("done") ?? 0}
            dropId={dropIdOf("done", lane)}
            id="done"
            items={columns.get("done") ?? []}
            nowMs={nowMs}
            userState={userState}
          />
        ) : (
          <Rail by={by} count={undefined} dropId={dropIdOf("done", lane)} id="done" />
        )}
      </div>
    );
  };

  return (
    <section aria-label="Project board" className="min-w-0" data-board-by={by}>
      {notice === undefined ? null : (
        <div
          aria-live="assertive"
          className={`mb-3 rounded-xl border px-4 py-2 text-sm ${card} ${dangerText}`}
          role="alert"
        >
          {notice}
        </div>
      )}
      <DndContext
        collisionDetection={boardCollisionDetection}
        onDragEnd={onDragEnd}
        onDragStart={closeRefPreview}
        sensors={sensors}
      >
        {lanes ? (
          <div className="flex flex-col gap-6" data-testid="board-lanes-container">
            {priorityLanes.map((lane) => {
              const inLane = rows.filter((item) => laneAt(item) === lane);
              const countedInLane = countedRows.filter(
                (item) => issuePriority(item.issue.priority) === lane
              );
              return (
                <section aria-label={`${laneLabel(lane)} lane`} key={String(lane)}>
                  <h2
                    className={`mb-2 flex items-center gap-2 text-xs font-semibold tracking-wide uppercase ${textSecondaryOnSurface}`}
                  >
                    {laneLabel(lane)}
                    <Pill>{countedInLane.length}</Pill>
                  </h2>
                  <div className="relative overflow-x-auto pb-3">
                    {drawRow(inLane, countedInLane, String(lane))}
                  </div>
                </section>
              );
            })}
          </div>
        ) : (
          // `relative` is load-bearing: a card's `sr-only` text is absolutely placed, and with no
          // placed ancestor inside the scroll box it widens the whole page past the window.
          <div className="relative overflow-x-auto pb-3" data-testid="board-scroll-container">
            {drawRow(rows, countedRows, undefined)}
          </div>
        )}
      </DndContext>
    </section>
  );
}
