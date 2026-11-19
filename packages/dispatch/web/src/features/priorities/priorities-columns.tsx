import type { ColumnDef } from "@tanstack/react-table";
import { type ReactNode, useMemo } from "react";
import { Link } from "react-router-dom";
import { ChevronIcon } from "../../components/DisclosureToggle";
import {
  focusVisibleRing,
  linkHoverText,
  linkText,
  textMutedOnCanvas,
  textPrimaryOnCanvas,
  textSecondaryOnCanvas,
} from "../../theme/classes";
import { Timestamp } from "../refs/Timestamp";
import { PriorityDescription } from "./PriorityDescription";
import {
  NEVER_MOVED,
  PRIORITY_COLUMNS,
  PRIORITY_COLUMNS_BY_ID,
  type PriorityItem,
} from "./priorities-model";
import { priorityPath } from "./priority-routes";
import { columnDefs } from "./priority-table-model";

/** Everything a row of the priorities table can do, which the page holds and the cells call. */
export interface PriorityRowActions {
  /** Whether this reader may write a priority: the add, the description and the drag are
   *  theirs alone, and every other reader sees the same rows without them. A title is renamed
   *  on the priority's own page, which the title links to. */
  readonly canWrite: boolean;
  /** Whether the order on screen is the order itself, which is the only view a drag can move:
   *  under another sort or a grouping, a drop would write a place the reader cannot see. */
  readonly canReorder: boolean;
  /** The priority whose row is open, which shows its whole description and its work. */
  readonly openId: string | undefined;
  readonly onOpen: (item: PriorityItem) => void;
  readonly onDescribe: (item: PriorityItem, description: string) => void;
  /** The row the reader is dragging, and the two ends of that drag. */
  readonly dragging: string | undefined;
  readonly onDragStart: (id: string) => void;
  readonly onDragEnd: () => void;
  /** One place up or down, which is the drag done from the keyboard. */
  readonly onMove: (item: PriorityItem, by: -1 | 1) => void;
}

/** The grip a row is dragged by: two columns of three dots. */
function GripIcon(): ReactNode {
  return (
    <svg aria-hidden="true" className="h-4 w-4" fill="currentColor" viewBox="0 0 16 16">
      <circle cx="5.5" cy="3.5" r="1.5" />
      <circle cx="10.5" cy="3.5" r="1.5" />
      <circle cx="5.5" cy="8" r="1.5" />
      <circle cx="10.5" cy="8" r="1.5" />
      <circle cx="5.5" cy="12.5" r="1.5" />
      <circle cx="10.5" cy="12.5" r="1.5" />
    </svg>
  );
}

function OrderCell({ actions, item }: { actions: PriorityRowActions; item: PriorityItem }) {
  return (
    <span className="flex items-center gap-1">
      {actions.canWrite && actions.canReorder ? (
        <button
          aria-label={`Move ${item.order} ${item.priority.title}: drag it, or press the up and down arrows`}
          className={`-ml-1 cursor-grab rounded p-0.5 active:cursor-grabbing ${textMutedOnCanvas} ${focusVisibleRing}`}
          draggable
          onDragEnd={actions.onDragEnd}
          onDragStart={(event) => {
            // Firefox starts no drag that carries no data, so the drag carries the id it moves.
            event.dataTransfer.setData("text/plain", item.priority.id);
            event.dataTransfer.effectAllowed = "move";
            actions.onDragStart(item.priority.id);
          }}
          onKeyDown={(event) => {
            if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
            event.preventDefault();
            actions.onMove(item, event.key === "ArrowUp" ? -1 : 1);
          }}
          type="button"
        >
          <GripIcon />
        </button>
      ) : null}
      <span className={`font-semibold tabular-nums ${textSecondaryOnCanvas}`}>{item.order}</span>
    </span>
  );
}

function TitleCell({ actions, item }: { actions: PriorityRowActions; item: PriorityItem }) {
  const open = actions.openId === item.priority.id;
  const { description, title } = item.priority;
  return (
    <div className="flex min-w-0 items-start gap-1">
      <button
        aria-expanded={open}
        aria-label={`${open ? "Close" : "Open"} ${item.order} ${title}`}
        className={`mt-0.5 rounded ${textMutedOnCanvas} ${focusVisibleRing}`}
        onClick={() => actions.onOpen(item)}
        type="button"
      >
        <ChevronIcon expanded={open} />
      </button>
      <div className="min-w-0 flex-1 space-y-1">
        {/* The title is read here and renamed on the priority's own page, which it opens: a
            click, or `o` and Enter on the focused row, which open the row's `data-issue-link`. */}
        <Link
          className={`flex w-full min-w-0 font-medium ${linkText} ${linkHoverText}`}
          data-issue-link
          to={priorityPath(item.priority.id)}
        >
          <span
            className={`line-clamp-2 min-w-0 flex-1 break-words hover:underline ${textPrimaryOnCanvas}`}
          >
            {title}
          </span>
        </Link>
        {open ? (
          <PriorityDescription
            canWrite={actions.canWrite}
            description={description}
            label={`Description of ${item.order}`}
            onSave={(next) => actions.onDescribe(item, next)}
            rows={6}
          />
        ) : item.summary === "" ? null : (
          <p className={`truncate text-sm ${textSecondaryOnCanvas}`}>{item.summary}</p>
        )}
      </div>
    </div>
  );
}

/** The owner in at most two lines, the whole of it in the tooltip: an owner is free text, and a
 *  long one would make its row as tall as the list. */
function OwnerCell({ owner }: { owner: string }) {
  return (
    <span className="line-clamp-2 break-words" title={owner === "" ? undefined : owner}>
      {owner}
    </span>
  );
}

const countCell = (value: number): ReactNode => (
  <span className={`tabular-nums ${value === 0 ? textMutedOnCanvas : ""}`}>{value}</span>
);

/** How each column of the priorities table draws. Everything else about a column — its width,
 *  its filter, what it groups by — is its declaration's (`PRIORITY_COLUMNS`). */
export function usePriorityColumns(actions: PriorityRowActions): ColumnDef<PriorityItem>[] {
  return useMemo(
    () =>
      columnDefs<PriorityItem>(
        PRIORITY_COLUMNS,
        {
          order: {
            accessorFn: (item) => item.place,
            cell: ({ row }) => <OrderCell actions={actions} item={row.original} />,
          },
          title: {
            accessorFn: (item) => item.priority.title,
            cell: ({ row }) => <TitleCell actions={actions} item={row.original} />,
          },
          owner: {
            accessorFn: (item) => item.priority.owner,
            cell: ({ row }) => <OwnerCell owner={row.original.priority.owner} />,
          },
          "in-flight": {
            accessorFn: (item) => item.inFlight,
            cell: ({ row }) => countCell(row.original.inFlight),
          },
          waiting: {
            accessorFn: (item) => item.waiting,
            cell: ({ row }) => countCell(row.original.waiting),
          },
          parked: {
            accessorFn: (item) => item.parked,
            cell: ({ row }) => countCell(row.original.parked),
          },
          closed: {
            accessorFn: (item) => item.closed,
            cell: ({ row }) => countCell(row.original.closed),
          },
          newest: {
            accessorFn: (item) => item.newest ?? "",
            cell: ({ row }) =>
              row.original.newest === null ? (
                <span className={textMutedOnCanvas}>{NEVER_MOVED}</span>
              ) : (
                <Timestamp at={row.original.newest} className={textSecondaryOnCanvas} />
              ),
          },
        },
        PRIORITY_COLUMNS_BY_ID
      ),
    [actions]
  );
}
