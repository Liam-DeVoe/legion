import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { useSearchParams } from "react-router-dom";
import { prioritiesQuery } from "../../api/queries";
import { LoadingSkeleton } from "../../components/LoadingSkeleton";
import { QueryError } from "../../components/QueryError";
import { Tabs, tabButtonId } from "../../components/Tabs";
import { textPrimaryOnCanvas, textSecondaryOnCanvas } from "../../theme/classes";
import { IssuesTable } from "../project/IssuesTable";
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
 *  in the statuses an issue is worked in, and the work whose own row names a priority standing
 *  in backlog or icebox, with the move that put it there - the read that answers the parked
 *  count (`advances=any&own=true&status=backlog&status=icebox&moved=true`). */
const NO_PRIORITY: IssueListing = { advances: "none", statuses: runningIssueStatuses };
const PARKED: IssueListing = {
  advances: "any",
  own: true,
  statuses: PARKED_STATUSES,
  moved: true,
};

/** The columns R6 asks every fault row to name, which each fault list opens with and no more:
 *  the issue (its key and title), the row's project, since neither list stands on one, its
 *  status, who holds it, and when it last changed, which is how long it has stood as it is. The
 *  row's checkbox stands beside them, as on every issues table. Every other column is one tick
 *  away under Columns. */
const FAULT_OPENS = ["key", "title", "status", "claim", "updated", "project"] as const;

/** Each fault list's own stored layout and the columns it opens with (`IssuesTable`). The parked
 *  list names besides the priority each issue is parked under, and when the work was moved
 *  there and by whom; it names no reason, since nothing records one until
 *  dispatch://INFRA-2934 does. */
const NO_PRIORITY_LAYOUT = { key: "priorities-no-priority", opens: FAULT_OPENS };
const PARKED_LAYOUT = {
  key: "priorities-parked",
  opens: [...FAULT_OPENS, "advances", "moved"],
  // Narrower than the Issues table's own, so the parked list's nine columns and the row's
  // actions fit the 1070 px box a 1440 px window gives it, with Title taking the rest above its
  // own minimum. A cell narrower than its words cuts them, the whole of them in its tooltip.
  widths: {
    claim: { min: 120, size: 120 },
    updated: { min: 88, size: 88 },
    project: { min: 72, size: 72 },
    advances: { min: 128, size: 128 },
    moved: { min: 128, size: 128 },
  },
} as const;

/** The page's three tabs, by the words each reads before its one count (INFRA-3221, R13). */
const TAB_LABELS: Record<PrioritiesTab, string> = {
  order: "Priorities",
  none: "Active under no priority",
  parked: "Parked priority work",
};

/**
 * `/priorities`: the priorities themselves, in their one order, and the two faults, each on a
 * tab of its own that reads its name and its one count, so whichever table is chosen is the one
 * table on the page, with nothing between the tab and it, and fills the window. A priority
 * belongs to no project, so this page names none: every count, every row and both fault lists
 * are the whole deployment's. Each fault list is the one Issues table, over the issue listing
 * the server narrows for it, under its own prefix in the address.
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
  // Each priority's title by its id, which the parked list's Advances column names a priority
  // by: built once that read answers rather than once a render, since the parked list's columns
  // are declared over it, and a map built afresh would rebuild them on every refetch. A title is
  // what a person wrote; the id is a slug of it and no name at all to a reader.
  const titles = useMemo(
    () => new Map((priorities.data?.priorities ?? []).map((row) => [row.id, row.title])),
    [priorities.data]
  );
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
  const rows = body.priorities ?? [];
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
            { id: "order", label: TAB_LABELS.order, count: rows.length },
            { id: "none", label: TAB_LABELS.none, count: body.work_under_no_priority.count },
            { id: "parked", label: TAB_LABELS.parked, count: body.priority_work_parked.count },
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
          <IssuesTable
            label={TAB_LABELS.none}
            layout={NO_PRIORITY_LAYOUT}
            listing={NO_PRIORITY}
            stateKey="n"
          />
        )}
        {panel(
          "parked",
          <IssuesTable
            label={TAB_LABELS.parked}
            layout={PARKED_LAYOUT}
            listing={PARKED}
            priorityTitles={titles}
            stateKey="k"
          />
        )}
      </div>
    </div>
  );
}
