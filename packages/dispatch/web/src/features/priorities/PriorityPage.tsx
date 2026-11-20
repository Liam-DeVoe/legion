import { ISSUE_TITLE_MAX } from "@legion/contracts";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { advancesIssuesQuery, prioritiesQuery } from "../../api/queries";
import { DisclosureToggle } from "../../components/DisclosureToggle";
import { EmptyState } from "../../components/EmptyState";
import { LoadingSkeleton } from "../../components/LoadingSkeleton";
import { QueryError } from "../../components/QueryError";
import { issueProject } from "../../lib/issue-key";
import { card, linkHoverText, linkText, textSecondaryOnCanvas } from "../../theme/classes";
import { IssuesTable } from "../project/IssuesTable";
import { selectIssues } from "../project/issues-table-model";
import { useKeymapScope } from "../shell/keymap";
import { FIELD_CHIP, HEADING_BOX, InPlaceField } from "./InPlaceField";
import { PriorityDescription } from "./PriorityDescription";
import { PriorityMargin } from "./PriorityMargin";
import { buildPriorityItems, ownerOf } from "./priorities-model";
import { PRIORITIES_PATH } from "./priority-routes";
import { textWriteLabel, usePriorityWrites } from "./priority-writes";

/** The address prefix the work under the priority keeps its own view under. */
const WORK_PREFIX = "w";

/** The work table's own stored layout (`IssuesTable`): the issue, its title, its status and its
 *  project. Every other column is one tick away under Columns, who holds the issue and when it
 *  last changed among them; a project's own table keeps its own layout. The widths fit the box a
 *  1280 px window leaves beside the margin, 606 px, as a desktop's box (`useTableLayout`): the
 *  key, the project's key, and the status with its longest pill, "Needs review", whole inside
 *  the cell's padding. It draws no row's actions after them (`rowActions`), so no empty column
 *  stands at its right: the selection's bar keeps Start next. Title takes all the width they
 *  leave, and wraps in its two lines. It pins the checkbox, the key and Title (`pins`), so its
 *  pinned divider falls after Title as each fault list's does: the registry's pins count the
 *  Priority column this table opens hidden, which in that box left Title no room to stay
 *  pinned. Its key is its own, not the one a scoped table without a layout keeps
 *  (`IssuesTable`), whose stored layouts were written for a project's opening set and would
 *  show that set again here. */
const WORK_LAYOUT = {
  key: "priority-page-work",
  opens: ["key", "title", "status", "project"],
  pins: ["select", "key", "title"],
  rowActions: false,
  widths: {
    key: { min: 96, size: 96 },
    project: { min: 72, size: 72 },
    status: { min: 150, size: 150 },
  },
} as const;

/** The way back to the priorities page, above the page's header as above an empty page. */
function BackLink(): ReactNode {
  return (
    <Link className={`text-sm ${linkText} ${linkHoverText}`} to={PRIORITIES_PATH}>
      ‹ Priorities
    </Link>
  );
}

/**
 * `/priorities/<id>`: one priority, edited where it is read, in the shape an issue's page takes.
 * The header is a card, as an issue's is (`IssueHeader.tsx`): the title is its heading, in an
 * issue title's type, which a click opens into the box it is written in, and the owner is a chip
 * in the row under it, which a click opens into its field. The description is a card under the
 * header, with its disclosure, and the work under it after that. Each is saved as it is written,
 * because nothing else in Dispatch has an edit mode or a Save button. The order is not a control
 * here: it is the row's own place on the priorities page, which is where it is dragged.
 *
 * The description is an attribute of the priority node and not a Dispatch document, so this
 * page shows no versions, no comments on it and no anchors; there is no delete, since a
 * priority is retired by its state. What happened to it is beside the page, in the margin.
 */
export function PriorityPage(): ReactNode {
  const { id = "" } = useParams();
  // Keyed by the priority: going from one priority's page to another's draws the page afresh,
  // so each field starts from its own priority's text, and the writes the page held for the one
  // before go back to the shell's line (`usePriorityWrites`).
  return <PriorityView id={id} key={id} />;
}

/** The page of the priority `id`. */
function PriorityView({ id }: { id: string }): ReactNode {
  const priorities = useQuery(prioritiesQuery());
  const work = useQuery({ ...advancesIssuesQuery({ advances: id }, true), enabled: id !== "" });
  const [open, setOpen] = useState(true);
  // Where the owner's notice of the reader's kept texts is drawn: under the header's row of
  // chips, not inside it.
  const [ownerNoticeAt, setOwnerNoticeAt] = useState<HTMLElement | null>(null);
  // The one priority this page shows, whose conflicts it settles with the reader
  // (`usePriorityWrites`).
  const shown = useMemo(() => [id], [id]);
  const { canWrite, describe, keep, kept, patch, rename } = usePriorityWrites(shown);
  // The keys every table is walked with (`j`, `k`, `x`, `o`, Enter), which a table registers
  // in the `project` scope: the work table is the one table this page draws, so they are its.
  useKeymapScope("project");
  // The way out of this scope, in the issue table's own shape: the issues advancing this
  // priority, project by project, each as that project's table narrowed to them. The chip the
  // address raises there names this priority and gives the whole table back when it is removed.
  // Grouped once a read, not once a keystroke in the fields above.
  const byProject = useMemo(() => {
    const grouped = new Map<string, string[]>();
    for (const issue of work.data ?? []) {
      const project = issueProject(issue.key);
      const keys = grouped.get(project);
      if (keys === undefined) grouped.set(project, [issue.key]);
      else keys.push(issue.key);
    }
    return grouped;
  }, [work.data]);
  // Each priority's title by its id, which the work table's Advances column names every priority
  // a row advances by. Built once a read, so the table's columns are not rebuilt every render.
  const titles = useMemo(
    () => new Map((priorities.data?.priorities ?? []).map((row) => [row.id, row.title])),
    [priorities.data]
  );

  if (priorities.isPending) return <LoadingSkeleton label="Loading the priority" />;
  if (priorities.isError)
    return (
      <QueryError
        error={priorities.error}
        message="Could not load the priority."
        onRetry={() => void priorities.refetch()}
        retrying={priorities.isFetching}
      />
    );
  const items = buildPriorityItems(priorities.data.priorities ?? []);
  const item = items.find((candidate) => candidate.priority.id === id);
  if (item === undefined)
    return (
      <EmptyState
        action={<BackLink />}
        label="Priority empty state"
        message="No priority of that id is written."
      />
    );

  const { description, owner, title } = item.priority;
  const keptDescription = kept(id, "description", description);
  const owners = [...new Set(items.map(ownerOf))].sort((a, b) => a.localeCompare(b));

  return (
    <div className="flex flex-col gap-6 lg:flex-row">
      <div className="min-w-0 flex-1 space-y-3">
        <BackLink />
        <header className={`min-w-0 rounded-xl border p-3 ${card}`} data-testid="priority-header">
          {/* The title's notice of the reader's kept texts stands under the heading, outside it:
              drawn in it, it would be read as part of the heading's name. */}
          {canWrite ? (
            <InPlaceField
              closed="heading"
              kept={kept(id, "title", title)}
              label="Title"
              maxLength={ISSUE_TITLE_MAX}
              onKeptChange={(texts) => keep(id, "title", texts)}
              onSave={(next) => rename(id, next, title, textWriteLabel("title", title, next))}
              required
              value={title}
            />
          ) : (
            <h1 className={HEADING_BOX}>{title}</h1>
          )}
          <div className="mt-2 flex flex-wrap items-center gap-2">
            {canWrite ? (
              <InPlaceField
                closed="chip"
                label="Owner"
                noticeAt={ownerNoticeAt}
                onSave={(next) =>
                  patch.mutate({
                    id,
                    input: { owner: next },
                    label: `Owner of the priority “${title}”`,
                  })
                }
                options={owners}
                placeholder="Who holds it"
                value={owner}
              />
            ) : (
              <span className={FIELD_CHIP}>
                {owner.trim() === "" ? null : <span className="font-normal">Owner</span>}
                <span>{ownerOf(item)}</span>
              </span>
            )}
          </div>
          {/* The owner's notice, on a line of its own under the chips. Empty, it takes no room. */}
          <div className="empty:hidden" ref={setOwnerNoticeAt} />
        </header>

        {/* The disclosure's control is 44 px tall around its 20 px line, so the card's padding
            above and below it is that much less than at its sides: its words stand 16 px in from
            each edge, as the card's padding at its sides sets them. */}
        <section aria-label="Description" className={`rounded-xl border px-4 py-1 ${card}`}>
          <DisclosureToggle
            expanded={open}
            label={open ? "Description" : "Description, closed"}
            onToggle={() => setOpen((was) => !was)}
          />
          {/* Drawn while closed too where it keeps the reader's text beside it, so the notice of
              a write another person's change refused is on the page whether it is open or not.
              The card is the editor's box, so the editor draws none of its own. */}
          {open || keptDescription.length > 0 ? (
            <div className="space-y-2 pb-3">
              <PriorityDescription
                boxed={false}
                canWrite={canWrite}
                description={description}
                kept={keptDescription}
                label="Description"
                onKeptChange={(texts) => keep(id, "description", texts)}
                onSave={(next) =>
                  describe(id, next, description, textWriteLabel("description", title, next))
                }
              />
            </div>
          ) : null}
        </section>

        <section aria-label="Work advancing this priority" className="space-y-2">
          {byProject.size === 0 ? null : (
            <p className={`text-sm ${textSecondaryOnCanvas}`}>
              {[...byProject.entries()]
                .sort((a, b) => a[0].localeCompare(b[0]))
                .map(([project, keys]) => (
                  <Link
                    className={`mr-3 ${linkText} ${linkHoverText}`}
                    key={project}
                    to={`/projects/${project}/issues?${selectIssues(new URLSearchParams(), keys, title)}`}
                  >
                    Open in {project}
                  </Link>
                ))}
            </p>
          )}
          <IssuesTable
            layout={WORK_LAYOUT}
            listing={{ advances: id }}
            priorityTitles={titles}
            stateKey={WORK_PREFIX}
          />
        </section>
      </div>
      <PriorityMargin id={id} />
    </div>
  );
}
