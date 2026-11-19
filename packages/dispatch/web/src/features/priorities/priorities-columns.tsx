import type { ColumnDef } from "@tanstack/react-table";
import { createContext, type ReactNode, useContext } from "react";
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
  /** The priority whose row is open, which shows its whole description. */
  readonly openId: string | undefined;
  readonly onOpen: (item: PriorityItem) => void;
  readonly onDescribe: (item: PriorityItem, description: string) => void;
  /** The reader's texts kept beside a priority's description while it shows another person's,
   *  where the page reads `held`, and the change of them: held in the write store
   *  (`usePriorityWrites`) rather than by the editor, which a row scrolling away takes with it.
   *  A row holds no title's texts: the title is written on the priority's own page. */
  readonly kept: (id: string, held: string) => readonly string[];
  readonly onKeep: (id: string, texts: readonly string[]) => void;
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
 * afresh, an editor the reader is typing in among them, which then never sees another person's
 * text arrive under it.
 */
export const PriorityRowActionsContext = createContext<PriorityRowActions | null>(null);

function useRowActions(): PriorityRowActions {
  const actions = useContext(PriorityRowActionsContext);
  if (actions === null) throw new Error("a cell of the priorities table is drawn outside it");
  return actions;
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

function OrderCell({ item }: { item: PriorityItem }) {
  const actions = useRowActions();
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

function TitleCell({ item }: { item: PriorityItem }) {
  const actions = useRowActions();
  const open = actions.openId === item.priority.id;
  const { description, id, title } = item.priority;
  const keptDescription = actions.kept(id, description);
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
            click, or `o` and Enter on the focused row, which open the row's `data-issue-link`.
            It wraps whole, however long, in the type of the Issues table's titles. */}
        <Link
          className={`flex w-full min-w-0 ${linkText} ${linkHoverText}`}
          data-issue-link
          to={priorityPath(item.priority.id)}
        >
          <span className={`min-w-0 flex-1 break-words ${textPrimaryOnCanvas}`}>{title}</span>
        </Link>
        {/* An open row draws its whole description, and so does a closed one that keeps the
            reader's text beside it: the notice of a write another person's change refused is on
            the row whether it is open or not. */}
        {open || keptDescription.length > 0 ? (
          <PriorityDescription
            canWrite={actions.canWrite}
            description={description}
            kept={keptDescription}
            label={`Description of ${item.order}`}
            onKeptChange={(texts) => actions.onKeep(id, texts)}
            onSave={(next) => actions.onDescribe(item, next)}
          />
        ) : item.summary === "" ? null : (
          // A closed row shows the description's first line, muted, on one line, cut where
          // the column ends; the whole line is its tooltip, and the open row shows it all.
          <p className={`truncate text-xs ${textMutedOnCanvas}`} title={item.summary}>
            {item.summary}
          </p>
        )}
      </div>
    </div>
  );
}

/** The owner on one line, the whole of it in the tooltip: an owner is free text, and a long one
 *  would make its row as tall as the list. */
function OwnerCell({ owner }: { owner: string }) {
  return (
    <span
      className={`block truncate ${textSecondaryOnCanvas}`}
      title={owner === "" ? undefined : owner}
    >
      {owner}
    </span>
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
          <span className={`text-xs ${textMutedOnCanvas}`}>{NEVER_MOVED}</span>
        ) : (
          <Timestamp
            at={row.original.newest}
            className={`whitespace-nowrap text-xs ${textMutedOnCanvas}`}
          />
        ),
    },
  },
  PRIORITY_COLUMNS_BY_ID
);
