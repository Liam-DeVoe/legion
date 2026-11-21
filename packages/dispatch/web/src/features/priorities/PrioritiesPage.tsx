import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { useSearchParams } from "react-router-dom";
import { prioritiesQuery } from "../../api/queries";
import { EmptyState } from "../../components/EmptyState";
import { LoadingSkeleton } from "../../components/LoadingSkeleton";
import { QueryError } from "../../components/QueryError";
import { Tabs, tabButtonId } from "../../components/Tabs";
import { useIsHuman } from "../../hooks/useIsHuman";
import { textPrimaryOnCanvas } from "../../theme/classes";
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

/** How each fault list sizes the columns it opens with, in place of the Issues table's own
 *  widths. Every column but Title and Claimed by is as wide as what it holds (`fit`): its header
 *  - its words with its sort and filter controls - or its widest cell on the page, whichever is
 *  wider, with the cell's padding, measured in the browser as the table draws them, so none is
 *  wider than it needs and none cuts a key, a pill or a time however wide production's are
 *  (`useTableLayout`'s `fits`). Measured in Chromium at 1280 px on the review corpus, Key's
 *  widest cell needs 87 to 96 px, its key and the unread dot after it, where production's
 *  `INFRA-2947` needs 121; Project's header 65; and Updated's header 114, its words and its two
 *  controls, where its widest time needs 96. Title, which takes the rest, is the column a reader
 *  reads a row by. Neither list holds an issue in Legion's queue, so neither draws the row's
 *  actions after its columns (`listsQueued`). */
const FAULT_WIDTHS = {
  key: { fit: true },
  project: { fit: true },
  status: { fit: true },
  updated: { fit: true },
} as const;

/** Each fault list's own stored layout and the columns it opens with (`IssuesTable`). The parked
 *  list names besides the priority each issue is parked under, and when the work was moved
 *  there and by whom; it names no reason, since nothing records one until
 *  dispatch://INFRA-2934 does. Each draws its titles wrapped (`wrapTitles`), in up to eight
 *  lines: a fault row is read by its title, production's mostly run to 194 characters, which two
 *  lines of Title cut in most, and a few to 1,000, which eight lines end in '…' with the whole
 *  title in its tooltip. Each draws its status bar's counts alone, with the names in a legend
 *  under it, where the segments with their names are wider than the bar (`statusLegend`): on a
 *  phone the in-flight statuses' names are, and a bar that scrolled them sideways cut "In
 *  progress" and "Retro".
 *
 *  In a 1280 px window each list's box is 910 px inside, the row's checkbox column 44 of it.
 *  Claimed by has a set width, in which a holder's chip wraps whole however long the session's
 *  name is (`wrapTitles` draws the claims so, `ClaimChip`'s `wraps`), and no narrower than its
 *  header, 131 px with its two controls: one line of a held chip needs 532 px for a session
 *  titled as production's are, 550 for one titled with an emoji. A column that gives Title its
 *  floor (`leaves`) gives up width where the others would leave Title less, so Title keeps it
 *  whatever keys and pills production's rows hold. */
const NO_PRIORITY_LAYOUT = {
  key: "priorities-no-priority",
  opens: FAULT_OPENS,
  // Its statuses are the in-flight ones, the widest of whose pills, "Needs review", needs 150 px
  // with the cell's padding. Title keeps 260 px: Claimed by gives up width for it from its 208
  // px down to its header's, and stands at 190 with the review corpus's keys and at 156 with
  // keys as wide as production's.
  widths: {
    ...FAULT_WIDTHS,
    claim: { leaves: 260, min: 132, size: 208 },
  },
  wrapTitles: true,
  statusLegend: true,
} as const;
const PARKED_LAYOUT = {
  key: "priorities-parked",
  // When the work was moved there is how long it has stood as it is, which is what Updated says
  // on the other list, so Updated opens hidden here, one tick away under Columns, and the room
  // it leaves is Title's.
  opens: [...FAULT_OPENS.filter((id) => id !== "updated"), "advances", "moved"],
  // Its statuses are backlog and icebox alone, whose pills need 110 px. A move is a time and a
  // name, which wrap onto two lines, the time over the name: Moved there is no wider than 96 px
  // where its cells wrap, and its header, 102 px, is wider, so it stands at that. The Advances
  // column holds each priority's chip and its title after it, which wraps whole, at no more
  // than 160 px. Claimed by stands at its header's width. With the review corpus's keys that
  // leaves Title 201 px; with keys as wide as production's, Advances gives up width for Title's
  // 200, down to the narrowest its chips need, 134 px on the corpus's.
  widths: {
    ...FAULT_WIDTHS,
    advances: { fit: true, leaves: 200, most: 160 },
    claim: { min: 132, size: 132 },
    moved: { fit: true, most: 96 },
  },
  wrapTitles: true,
  statusLegend: true,
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
  // by, with its place in the order: the map holds them in the order the read answers them,
  // which is the one order (`useIssueTableColumns`). Built once that read answers rather than
  // once a render, since the parked list's columns are declared over it, and a map built afresh
  // would rebuild them on every refetch. A title is what a person wrote; the id is a slug of it
  // and no name at all to a reader.
  const titles = useMemo(
    () => new Map((priorities.data?.priorities ?? []).map((row) => [row.id, row.title])),
    [priorities.data]
  );
  // Whether this reader may write the first priority, which the empty tab then offers.
  const canWrite = useIsHuman();
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
      className="mt-3"
      hidden={tab !== id}
      id={`${TABS_ID}-${id}-panel`}
      role="tabpanel"
    >
      {tab === id ? content : null}
    </div>
  );
  return (
    <section>
      {/* One strip at `md` and above, as the project page draws its header: the heading, and
          the tabs beside it. Narrower, the tabs take a line of their own, and scroll sideways
          inside it where they are wider than the window. */}
      <header className="flex flex-wrap items-center gap-2 md:min-h-12 md:flex-nowrap">
        <h1 className={`shrink-0 text-[22px] font-semibold tracking-tight ${textPrimaryOnCanvas}`}>
          Priorities
        </h1>
        <div className="min-w-0 max-w-full">
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
        </div>
      </header>
      {panel(
        "order",
        rows.length === 0 ? (
          // The page with no priority is where the first is written.
          <EmptyState
            action={
              canWrite ? (
                <div className="flex flex-wrap items-center justify-center gap-2">
                  <NewPriority />
                </div>
              ) : undefined
            }
            label="Priorities empty state"
            message={body.empty ?? "No priority has been written yet."}
          />
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
    </section>
  );
}
