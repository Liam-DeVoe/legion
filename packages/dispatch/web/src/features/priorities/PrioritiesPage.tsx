import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { prioritiesQuery } from "../../api/queries";
import { LoadingSkeleton } from "../../components/LoadingSkeleton";
import { QueryError } from "../../components/QueryError";
import { textMutedOnCanvas, textPrimaryOnCanvas, textSecondaryOnCanvas } from "../../theme/classes";
import { IssuesTable } from "../project/IssuesTable";
import type { ShownColumnId } from "../project/issues-table-columns";
import type { IssueListing } from "../project/issues-table-data";
import { runningIssueStatuses } from "../project/issues-table-model";
import { NewPriority } from "./NewPriority";
import { PrioritiesTable } from "./PrioritiesTable";
import { PARKED_STATUSES } from "./priorities-model";

/** The two faults, as the issue listing answers each: the work being done under no priority,
 *  in the statuses an issue is worked in, and the work under a priority standing in backlog or
 *  icebox, with the move that put it there. */
const NO_PRIORITY: IssueListing = { advances: "none", statuses: runningIssueStatuses };
const PARKED: IssueListing = { advances: "any", statuses: PARKED_STATUSES, moved: true };

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

/** A section heading with its one count beside it, as a view tab carries its count. */
function CountedHeading({ count, title }: { count: number; title: string }): ReactNode {
  return (
    <div className="flex items-baseline gap-2">
      <h2 className={`text-lg font-semibold ${textPrimaryOnCanvas}`}>{title}</h2>
      <span className={`tabular-nums ${textMutedOnCanvas}`} data-fault-count>
        {count}
      </span>
    </div>
  );
}

/**
 * `/priorities`: the priorities themselves, in their one order, and under them the two faults.
 * A priority belongs to no project, so this page names none: every count, every row and both
 * fault lists are the whole deployment's. Each fault list is the one Issues table, over the
 * issue listing the server narrows for it, under its own prefix in the address.
 */
export function PrioritiesPage(): ReactNode {
  const priorities = useQuery(prioritiesQuery());
  // The titles are what the parked list's columns are declared over, so the map is built once
  // the read answers rather than once a render, which would rebuild that table's columns on
  // every refetch of anything.
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
        <IssuesTable
          label="Issues active under no priority"
          layout={NO_PRIORITY_LAYOUT}
          listing={NO_PRIORITY}
          stateKey="n"
        />
      </section>

      <section aria-label="Parked priority work" className="space-y-2">
        <CountedHeading count={body.priority_work_parked.count} title="Parked priority work" />
        <IssuesTable
          label="Parked priority issues"
          layout={PARKED_LAYOUT}
          listing={PARKED}
          priorityTitles={titles}
          stateKey="k"
        />
      </section>
    </div>
  );
}
