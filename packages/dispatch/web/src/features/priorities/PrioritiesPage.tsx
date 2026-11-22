import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { prioritiesQuery } from "../../api/queries";
import { LoadingSkeleton } from "../../components/LoadingSkeleton";
import { QueryError } from "../../components/QueryError";
import { textMutedOnCanvas, textPrimaryOnCanvas, textSecondaryOnCanvas } from "../../theme/classes";
import type { ShownColumnId } from "../project/issues-table-columns";
import type { AdvancesListing } from "../project/issues-table-data";
import { runningIssueStatuses } from "../project/issues-table-model";
import { ListingTable } from "../project/ListingTable";
import { useKeymapScope } from "../shell/keymap";
import { NewPriority } from "./NewPriority";
import { PrioritiesTable } from "./PrioritiesTable";
import { PARKED_STATUSES } from "./priorities-model";

/** The two faults, as the issue listing answers each: the work being done under no priority,
 *  in the statuses an issue is worked in, and the work under a priority standing in backlog or
 *  icebox, with the move that put it there. */
const NO_PRIORITY: AdvancesListing = { advances: "none", statuses: runningIssueStatuses };
const PARKED: AdvancesListing = { advances: "any", statuses: PARKED_STATUSES, moved: true };

/** Each fault list's own stored layout and the columns it opens with shown. Both name each row's
 *  project, since neither stands on one, and when the issue last changed, which is how long it
 *  has stood as it is (R6); the parked list names what the work advances, when it was moved
 *  there and why besides, the why being, on every row, that nothing records it yet. The list
 *  under no priority has no Advances column at all: every row of it advances nothing. */
const NO_PRIORITY_LAYOUT = {
  key: "priorities-no-priority",
  shown: ["project", "updated"] satisfies ShownColumnId[],
};
const PARKED_LAYOUT = {
  key: "priorities-parked",
  shown: ["project", "advances", "moved", "reason", "updated"] satisfies ShownColumnId[],
};

/** A section heading: its name, and beside it the one count of the issues its table lists. */
function CountedHeading({ count, title }: { count: number; title: string }): ReactNode {
  return (
    <h2 className={`flex items-baseline gap-2 text-lg font-semibold ${textPrimaryOnCanvas}`}>
      {title} <span className={`font-normal tabular-nums ${textMutedOnCanvas}`}>{count}</span>
    </h2>
  );
}

/**
 * `/priorities`: the priorities themselves, in their one order, and under them the two faults.
 * A priority belongs to no project, so this page names none: every count, every row and both
 * fault lists are the whole deployment's. Each fault list is the one Issues table, over the
 * issue listing the server narrows for it, under its own prefix in the address. The List's
 * `needs-you` and `unread` flags stay bare, so they narrow every issue table of the page at once,
 * while each table's Unclaimed is its own Claimed by pick (`<prefix>.unclaimed`).
 *
 * Every table of the page registers its keys in the `project` scope, which the page pushes. The
 * priorities table is the page's main one, whose `j` and `k` walk it from anywhere the focus is
 * in no other table; each issue table beside it - both fault lists, and the work under an opened
 * row - walks its own rows from one of them (`moveFrom="row"`). The keymap answers a key with the
 * first registration whose `when` holds, so the tables would otherwise all take `j` and `k` from
 * the page, and the first to register would answer for every one.
 */
export function PrioritiesPage(): ReactNode {
  useKeymapScope("project");
  const priorities = useQuery(prioritiesQuery());
  // The titles, built once a read answers rather than once a render: the parked list's columns
  // are declared over them, so a map built afresh would rebuild that table's columns on every
  // refetch of anything. Every column of this page names a priority by the title a person
  // wrote, never by the id, which is a slug of that title and no name at all to a reader.
  const { rows, titles } = useMemo(() => {
    const priorityRows = priorities.data?.priorities ?? [];
    return {
      rows: priorityRows,
      titles: new Map(priorityRows.map((row) => [row.id, row.title])),
    };
  }, [priorities.data]);
  if (priorities.isPending) return <LoadingSkeleton label="Loading priorities" />;
  if (priorities.isError)
    return (
      <QueryError
        error={priorities.error}
        message="Could not load the priorities."
        onRetry={() => void priorities.refetch()}
        retrying={priorities.isFetching}
      />
    );
  const body = priorities.data;
  return (
    <div className="space-y-8">
      <h1 className={`text-2xl font-semibold ${textPrimaryOnCanvas}`}>Priorities</h1>

      {rows.length === 0 ? (
        <div className="space-y-3">
          <p className={textSecondaryOnCanvas}>
            {body.empty ?? "No priority has been written yet."}
          </p>
          {/* The page with no priority is where the first is written. */}
          <div className="flex flex-wrap items-center gap-2">
            <NewPriority />
          </div>
        </div>
      ) : (
        <PrioritiesTable rows={rows} titles={titles} />
      )}

      <section aria-label="Active under no priority" className="space-y-2">
        <CountedHeading
          count={body.work_under_no_priority.count}
          title="Active under no priority"
        />
        <ListingTable
          label="Issues active under no priority"
          layout={NO_PRIORITY_LAYOUT}
          listing={NO_PRIORITY}
          moveFrom="row"
          stateKey="n"
        />
      </section>

      <section aria-label="Priority work in backlog or icebox" className="space-y-2">
        <CountedHeading
          count={body.priority_work_parked.count}
          title="Priority work in backlog or icebox"
        />
        <ListingTable
          label="Priority issues in backlog or icebox"
          layout={PARKED_LAYOUT}
          listing={PARKED}
          moveFrom="row"
          priorityTitles={titles}
          stateKey="k"
        />
      </section>
    </div>
  );
}
