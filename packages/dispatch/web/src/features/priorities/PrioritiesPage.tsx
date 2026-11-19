import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { useSearchParams } from "react-router-dom";
import { prioritiesQuery } from "../../api/queries";
import { LoadingSkeleton } from "../../components/LoadingSkeleton";
import { QueryError } from "../../components/QueryError";
import { Tabs, tabButtonId } from "../../components/Tabs";
import { textPrimaryOnCanvas, textSecondaryOnCanvas } from "../../theme/classes";
import { IssuesTable } from "../project/IssuesTable";
import type { ShownColumnId } from "../project/issues-table-columns";
import type { IssueListing } from "../project/issues-table-data";
import { runningIssueStatuses } from "../project/issues-table-model";
import { useKeymapScope } from "../shell/keymap";
import { NewPriority } from "./NewPriority";
import { PrioritiesTable } from "./PrioritiesTable";
import { PARKED_STATUSES } from "./priorities-model";

/** The page's three tabs, as the address names each (`?tab=`): the order itself, which is the
 *  tab an address naming none opens, and the two faults. */
type PrioritiesTab = "order" | "none" | "parked";
const TAB_PARAM = "tab";
const TABS_ID = "priorities";

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

/** The per-project or per-priority counts of a fault, in plain words: `CORE 12 · OPS 7`,
 *  greatest first, which is what says where the fault is. */
function countsLine(by: Readonly<Record<string, number>> | null): string {
  return Object.entries(by ?? {})
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([name, count]) => `${name} ${count}`)
    .join(" · ");
}

/** One fault's tab: what the fault is and how many issues it holds and where, on one line from
 *  a tablet's width up, so the Issues table over them, the last thing on the page, has the rest
 *  of the window. A narrower screen wraps the sentence and gives the counts a line of their own,
 *  cut where it ends: thirteen priorities' counts wrapped whole pushed the table off a phone's
 *  first screen. */
function FaultPanel({
  by,
  children,
  count,
  label,
  line,
}: {
  by: Readonly<Record<string, number>> | null;
  children: ReactNode;
  count: number;
  label: string;
  line: string;
}): ReactNode {
  const counts = countsLine(by);
  const counted = `${count} ${count === 1 ? "issue" : "issues"}${counts === "" ? "" : ` · ${counts}`}`;
  return (
    <section aria-label={label} className="space-y-2">
      <p
        className={`flex flex-wrap gap-x-4 gap-y-1 text-sm sm:flex-nowrap ${textSecondaryOnCanvas}`}
      >
        <span className="min-w-0 sm:shrink-0">{line}</span>
        <span className="min-w-0 basis-full truncate sm:basis-auto" title={counted}>
          {counted}
        </span>
      </p>
      {children}
    </section>
  );
}

/**
 * `/priorities`: the priorities themselves, in their one order, and the two faults, each on a
 * tab of its own, so whichever table is chosen is the one table on the page and fills the
 * window. A priority belongs to no project, so this page names none: every count, every row and
 * both fault lists are the whole deployment's. Each fault list is the one Issues table, over the
 * issue listing the server narrows for it, under its own prefix in the address.
 */
export function PrioritiesPage(): ReactNode {
  const priorities = useQuery(prioritiesQuery());
  const [params, setParams] = useSearchParams();
  const asked = params.get(TAB_PARAM);
  const tab: PrioritiesTab = asked === "none" || asked === "parked" ? asked : "order";
  // The keys every table of the application is walked with (`j`, `k`, `x`, `o`, Enter), which
  // each table registers in the `project` scope. The registry holds one table's per page
  // (`features/project/issues-table-keys.ts`), and each tab draws exactly one table, so the keys
  // are that table's: the priorities, or a fault list. The work under a priority is on its own
  // page, which turns the scope on for that table alone.
  useKeymapScope("project");
  // What the page draws of one read, built once that read answers rather than once a render:
  // the titles are what the parked list's columns are declared over, so a map built afresh
  // would rebuild that table's columns on every refetch of anything.
  //
  // Every column and every count line of this page names a priority by the title a person
  // wrote, never by the id, which is a slug of that title and no name at all to a reader.
  const { parkedByPriority, rows, titles } = useMemo(() => {
    const read = priorities.data;
    const priorityRows = read?.priorities ?? [];
    const byId = new Map(priorityRows.map((row) => [row.id, row.title]));
    return {
      rows: priorityRows,
      titles: byId,
      parkedByPriority: Object.fromEntries(
        Object.entries(read?.priority_work_parked.by_priority ?? {}).map(([id, count]) => [
          byId.get(id) ?? id,
          count,
        ])
      ),
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
  const unpriorited = body.work_under_no_priority;
  const parked = body.priority_work_parked;
  // Each tab's panel is in the page whichever is chosen, as the tab list's `aria-controls`
  // names it, and only the chosen one draws its table.
  const panel = (id: PrioritiesTab, content: ReactNode) => (
    <div
      aria-labelledby={tabButtonId(TABS_ID, id)}
      className="mt-2"
      hidden={tab !== id}
      id={`${TABS_ID}-${id}-panel`}
      role="tabpanel"
    >
      {tab === id ? content : null}
    </div>
  );
  return (
    <div className="space-y-2">
      <h1 className={`text-2xl font-semibold ${textPrimaryOnCanvas}`}>Priorities</h1>
      <div>
        <Tabs
          activeTab={tab}
          ariaLabel="Priorities page"
          compact
          idPrefix={TABS_ID}
          onSelect={(next) =>
            setParams(
              (current) => {
                const changed = new URLSearchParams(current);
                changed.set(TAB_PARAM, next);
                return changed;
              },
              { replace: true }
            )
          }
          tabs={[
            { id: "order", label: "Priorities", count: rows.length },
            { id: "none", label: "In flight under no priority", count: unpriorited.count },
            { id: "parked", label: "In backlog or icebox", count: parked.count },
          ]}
        />
        {panel(
          "order",
          rows.length === 0 ? (
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
            <PrioritiesTable rows={rows} />
          )
        )}
        {panel(
          "none",
          <FaultPanel
            by={unpriorited.by_project}
            count={unpriorited.count}
            label="In flight under no priority"
            line="Issues being worked that advance no priority."
          >
            <IssuesTable
              label="Issues in flight under no priority"
              layout={NO_PRIORITY_LAYOUT}
              listing={NO_PRIORITY}
              stateKey="n"
            />
          </FaultPanel>
        )}
        {panel(
          "parked",
          <FaultPanel
            by={parkedByPriority}
            count={parked.count}
            label="In backlog or icebox"
            line="Issues under a priority that stand in the backlog or the icebox."
          >
            <IssuesTable
              label="Priority issues in backlog or icebox"
              layout={PARKED_LAYOUT}
              listing={PARKED}
              priorityTitles={titles}
              stateKey="k"
            />
          </FaultPanel>
        )}
      </div>
    </div>
  );
}
