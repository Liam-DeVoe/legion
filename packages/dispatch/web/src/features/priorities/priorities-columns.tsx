import type { ColumnDef } from "@tanstack/react-table";
import { createContext, type ReactNode, useContext } from "react";
import { Link } from "react-router-dom";
import { ChevronIcon } from "../../components/DisclosureToggle";
import {
  dragHandleBg,
  focusVisibleRing,
  linkHoverText,
  linkText,
  textMutedOnCanvas,
  textSecondaryOnCanvas,
} from "../../theme/classes";
import { Timestamp } from "../refs/Timestamp";
import { InPlaceField } from "./InPlaceField";
import { PriorityDescription } from "./PriorityDescription";
import {
  NEVER_MOVED,
  PRIORITY_COLUMNS,
  PRIORITY_COLUMNS_BY_ID,
  type PriorityItem,
} from "./priorities-model";
import { priorityPath } from "./priority-routes";
import { columnDefs } from "./priority-table-model";
import type { EditedText } from "./priority-writes";

/** Everything a row of the priorities table can do, which the page holds and the cells call. */
export interface PriorityRowActions {
  /** Whether this reader may write a priority: the add, the rename, the description and the
   *  drag are theirs alone, and every other reader sees the same rows without them. */
  readonly canWrite: boolean;
  /** Whether the order on screen is the order itself, which is the only view a drag can move:
   *  under another sort or a grouping, a drop would write a place the reader cannot see. */
  readonly canReorder: boolean;
  /** The priority whose row is open, which shows its whole description and its work. */
  readonly openId: string | undefined;
  readonly onOpen: (item: PriorityItem) => void;
  readonly onRename: (item: PriorityItem, title: string) => void;
  readonly onDescribe: (item: PriorityItem, description: string) => void;
  /** The reader's texts kept beside a priority's title or description while it shows another
   *  person's, and the change of them: held by the page (`usePriorityWrites`) rather than by
   *  the field, which a row scrolling away takes with it. */
  readonly kept: (id: string, attribute: EditedText) => readonly string[];
  readonly onKeep: (id: string, attribute: EditedText, texts: readonly string[]) => void;
  /** The row the reader is dragging, and the two ends of that drag. */
  readonly dragging: string | undefined;
  readonly onDragStart: (id: string) => void;
  readonly onDragEnd: () => void;
  /** One place up or down, which is the drag done from the keyboard. */
  readonly onMove: (item: PriorityItem, by: -1 | 1) => void;
}

/**
 * What the rows of the table can do, for its cells to read (`PrioritiesTable` provides it). It is
 * read here rather than built into the column declarations: new declarations bring new cell
 * renderers, which TanStack draws as components of their own (`flexRender`), so each change of
 * what a row can do - and each read of the priorities changes the order - would draw every cell
 * afresh, a field the reader is typing in among them, which then never sees another person's
 * text arrive under it.
 */
export const PriorityRowActionsContext = createContext<PriorityRowActions | null>(null);

function useRowActions(): PriorityRowActions {
  const actions = useContext(PriorityRowActionsContext);
  if (actions === null) throw new Error("a cell of the priorities table is drawn outside it");
  return actions;
}

function OrderCell({ item }: { item: PriorityItem }) {
  const actions = useRowActions();
  return (
    <span className="flex items-center gap-1">
      {actions.canWrite && actions.canReorder ? (
        <button
          aria-label={`Move ${item.order} ${item.priority.title}: drag it, or press the up and down arrows`}
          className={`cursor-grab rounded px-1 ${textMutedOnCanvas} ${focusVisibleRing}`}
          draggable
          onDragEnd={actions.onDragEnd}
          onDragStart={() => actions.onDragStart(item.priority.id)}
          onKeyDown={(event) => {
            if (event.key !== "ArrowUp" && event.key !== "ArrowDown") return;
            event.preventDefault();
            actions.onMove(item, event.key === "ArrowUp" ? -1 : 1);
          }}
          type="button"
        >
          <span aria-hidden className={`block h-3 w-3 rounded-sm ${dragHandleBg}`} />
        </button>
      ) : null}
      <Link
        className={`font-semibold tabular-nums ${linkText} ${linkHoverText}`}
        to={priorityPath(item.priority.id)}
      >
        {item.order}
      </Link>
    </span>
  );
}

function TitleCell({ item }: { item: PriorityItem }) {
  const actions = useRowActions();
  const open = actions.openId === item.priority.id;
  const { description, id, title } = item.priority;
  return (
    <div className="flex min-w-0 items-start gap-1">
      <button
        aria-expanded={open}
        aria-label={`${open ? "Close" : "Open"} ${item.order} ${title}`}
        className={`mt-1 rounded ${textMutedOnCanvas} ${focusVisibleRing}`}
        onClick={() => actions.onOpen(item)}
        type="button"
      >
        <ChevronIcon expanded={open} />
      </button>
      <div className="min-w-0 flex-1 space-y-1">
        {actions.canWrite ? (
          <InPlaceField
            kept={actions.kept(id, "title")}
            label={`Title of ${item.order}`}
            onKeptChange={(texts) => actions.onKeep(id, "title", texts)}
            onSave={(next) => actions.onRename(item, next)}
            required
            value={title}
          />
        ) : (
          <p className="font-medium">{title}</p>
        )}
        {open ? (
          <PriorityDescription
            canWrite={actions.canWrite}
            description={description}
            kept={actions.kept(id, "description")}
            label={`Description of ${item.order}`}
            onKeptChange={(texts) => actions.onKeep(id, "description", texts)}
            onSave={(next) => actions.onDescribe(item, next)}
          />
        ) : item.summary === "" ? null : (
          <p className={`truncate text-sm ${textSecondaryOnCanvas}`}>{item.summary}</p>
        )}
      </div>
    </div>
  );
}

const countCell = (value: number): ReactNode => (
  <span className={`tabular-nums ${value === 0 ? textMutedOnCanvas : ""}`}>{value}</span>
);

/** How each column of the priorities table draws. Everything else about a column — its width,
 *  its filter, what it groups by — is its declaration's (`PRIORITY_COLUMNS`). Built once: what a
 *  row can do is the cells' to read (`PriorityRowActionsContext`). */
export const PRIORITY_COLUMN_DEFS: ColumnDef<PriorityItem>[] = columnDefs<PriorityItem>(
  PRIORITY_COLUMNS,
  {
    order: {
      accessorFn: (item) => item.place,
      cell: ({ row }) => <OrderCell item={row.original} />,
    },
    title: {
      accessorFn: (item) => item.priority.title,
      cell: ({ row }) => <TitleCell item={row.original} />,
    },
    owner: { accessorFn: (item) => item.priority.owner },
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
);
