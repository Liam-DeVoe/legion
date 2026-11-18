import { MAX_ISSUE_ADVANCES } from "@legion/contracts";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useMemo, useState } from "react";
import { Link } from "react-router-dom";

import { apiErrorMessage, issuePatchRequest } from "../../api/client";
import { prioritiesQuery } from "../../api/queries";
import type { Issue, PriorityRow } from "../../api/types";
import { MultiSelect } from "../../components/MultiSelect";
import { usePageWords, useTooltips } from "../../components/tooltips";
import {
  dangerText,
  linkHoverText,
  linkText,
  pageFailedText,
  pageInkText,
  pageMutedText,
  pagePaneBg,
  pageQuietText,
  surfaceMutedStrongBg,
  textMutedOnSurface,
  textSecondaryHoverToPrimary,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { useKeyedWrite } from "../writes/useKeyedWrite";
import { orderLabel } from "./priorities-model";
import { priorityPath } from "./priority-routes";

/** What the line calls each priority the read answered, by its id: its order and its title, as
 *  the priorities page names it ("P7 Rocq and Lean speed work restarted"). The read answers the
 *  priorities in rank order, so the order is the row's place in that one list, named by the one
 *  rule the table names it by (`orderLabel`). A priority this map does not hold is named by its
 *  own id, so a line never waits on a second read to say which priority it means. */
function priorityLabels(rows: readonly PriorityRow[]): ReadonlyMap<string, string> {
  return new Map(rows.map((row, index) => [row.id, `${orderLabel(index)} ${row.title}`]));
}

/** The issue's own set while the picker is open, and the read it was drawn from: the set the
 *  close compares it with, and the revision the close's write carries. */
interface Draft {
  readonly ids: string[];
  readonly from: readonly string[];
  readonly revision: number;
}

/**
 * The `Advances` item of the issue header's metadata rail (INFRA-2947, R16 and R3): each
 * priority this issue advances, named by its order and title and linking to that priority's own
 * page, and the shared `MultiSelect` that adds and removes the issue's own, as the labels and
 * components beside it are edited. Like those it stages a draft while open and saves the final
 * draft once, when it closes.
 *
 * An issue that advances none carries no label and no empty line — only the picker's trigger,
 * which places it under a priority, and which is how a person acts on a row of the page's first
 * fault list. A priority inherited from an issue above (R4) is named on the line and says so,
 * and the picker does not offer it: that relation is the ancestor's, a relation on a child never
 * places the issues above it, and every `MultiSelect` row is a toggle, so a row for it would
 * offer a retraction this issue cannot make.
 *
 * Each close that changed the set writes the whole of the issue's own set —
 * `PATCH /api/v1/issues/{key}` — with the `advances_revision` of the read the draft was drawn
 * from when the picker opened. The server reads the act from the difference between the set and
 * what it holds now, so without the revision a set another writer moved in between would read as
 * this write's act and retract a relation the person never saw; with it, that write is refused
 * 409 ADVANCES_REVISION (`api/issue_advances.go`, `refuseAdvancesRevision`), the line shows the
 * server's reason as it shows every refusal, and the issue is read again so the next write
 * carries the revision it holds now. The list of priorities to choose from is read only once the
 * line has an entry to name or the picker is open, as the components line reads the
 * architecture tree only while its picker is open.
 *
 * Inside `NoTooltips` (`components/tooltips.tsx`) it says the new issue page's words with their
 * marks and carries no `title`; there `inRow` draws it as the value of a row of Details, which
 * draws the label: no `Advances` of its own, a line that wraps and cuts no word, and "Add a
 * priority" on the picker's trigger while the issue advances none.
 */
export function IssuePriorityLine({
  disabled,
  inRow = false,
  issue,
}: {
  disabled: boolean;
  /** Whether the line is the value of a row of the new issue page's Details. It acts inside
   *  `NoTooltips` alone. */
  inRow?: boolean;
  issue: Pick<Issue, "advances" | "advances_revision" | "key">;
}): ReactNode {
  const tooltips = useTooltips();
  const page = usePageWords();
  const inDetails = page !== null && inRow;
  const advances = issue.advances ?? [];
  const own = advances.filter((entry) => !entry.inherited).map((entry) => entry.id);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [inputError, setInputError] = useState<string | null>(null);
  const open = draft !== null;
  const queryClient = useQueryClient();
  const priorities = useQuery({
    ...prioritiesQuery(),
    enabled: open || advances.length > 0,
  });
  const rows = useMemo(() => priorities.data?.priorities ?? [], [priorities.data]);
  const labels = useMemo(() => priorityLabels(rows), [rows]);
  const write = useKeyedWrite({
    // The line says why a write was refused, beside the control it came from.
    inline: true,
    label: () => `Advances of ${issue.key}`,
    // One write to the issue, in press order with every other (`issue:<KEY>`).
    target: () => `issue:${issue.key}`,
    // The set and the revision of the read it was drawn from, both taken when the picker
    // opened: a retry sends the same pair, never a revision read after the set was chosen.
    request: (input: { advances: string[]; advances_revision: number }) =>
      issuePatchRequest(issue.key, input),
    onSettled: () => {
      // Every open issue, this one included: a descendant inherits the change with no event of
      // its own, and a refused write leaves this issue's read stale, so the next write must
      // carry the revision it holds now. The priorities body and the rail's two counts move with
      // the relation as well.
      const rereads = [
        queryClient.invalidateQueries({ queryKey: ["issue"] }),
        queryClient.invalidateQueries({ queryKey: ["priorities"] }),
      ];
      // Outside `NoTooltips` the write stops pending as it lands and these reads go on after it,
      // as on `main`. Inside, `onSettled` returns them, which TanStack Query awaits before the
      // write stops pending: the trigger stays `aria-disabled`, and refuses to open, until the
      // issue it draws is the one read after the write.
      if (page === null) return;
      return Promise.all(rereads);
    },
  });
  const saving = write.isPending;

  // What the picker offers: the live priorities in rank order, less those the issue inherits,
  // which are no choice here; and each of the issue's own, even one the read does not hold, so a
  // relation the line names can always be taken off.
  const live = rows
    .map((row) => row.id)
    .filter((id) => !advances.some((entry) => entry.inherited && entry.id === id));
  const options = [...live, ...own.filter((id) => !live.includes(id))];

  const setOpen = (next: boolean) => {
    if (next) {
      if (disabled || saving) return;
      setDraft({ from: own, ids: [...own], revision: issue.advances_revision });
      setInputError(null);
      return;
    }
    if (draft === null) return;
    setDraft(null);
    setInputError(null);
    // A close that changed nothing writes nothing; ids are unique, so equal lengths and every id
    // of the draft in the set it was drawn from is the same set in another order.
    if (
      draft.ids.length !== draft.from.length ||
      draft.ids.some((id) => !draft.from.includes(id))
    ) {
      write.mutate({ advances: draft.ids, advances_revision: draft.revision });
    }
  };
  const changeDraft = (next: string[]) => {
    if (next.length > MAX_ISSUE_ADVANCES) {
      setInputError(
        page?.STATE_LINES.advances_at_cap ??
          `An issue advances at most ${MAX_ISSUE_ADVANCES} priorities.`
      );
      return;
    }
    setDraft((current) => (current === null ? current : { ...current, ids: next }));
    setInputError(null);
  };

  // Inside `NoTooltips` each colour is one of the new issue page's: the line is the ink in a row
  // of Details (the prototype's `.frow .fv`) and the muted grey elsewhere (its `#hdr-props`); a
  // priority is a link in the ink; what qualifies it is quiet words, a refusal the failed red;
  // the trigger is muted words on the page's pane that turn ink under the pointer.
  const quiet = page === null ? textMutedOnSurface : pageQuietText;
  const failed = page === null ? dangerText : pageFailedText;
  // A failed save shows the server's reason, else the page's own line; each carries the mark of
  // whose words it is.
  const notSaved =
    page?.ANSWER_LINES.priorities_not_saved ?? "Could not save what this issue advances.";
  const saveFailure = apiErrorMessage(write.error, notSaved);
  // Inside `NoTooltips` nothing of the line keeps its width, in a row of Details and in the
  // header alike: a priority's title and a refusal's reason are values, which break anywhere and
  // stay within their line - the title with `min-w-0`, a flex item that shrinks beside the mark
  // after it, the reason with `max-w-full` - rather than push the line past its row. Outside,
  // each part stays whole, as on `main`.
  const wraps = page !== null;
  // In a row of Details, with no priority to stand beside, the trigger says what it does, as the
  // labels' says "Add a label", because that panel raises no tooltip that could say it instead.
  // Its name then begins with the words it draws and ends with the joining word and the issue's
  // key ("Add a priority to CORE-1"), so a voice user says what they read (WCAG 2.5.3 Label in
  // Name). Everywhere else it draws "+" and is named "Edit priorities", as the labels' trigger is
  // named "Edit labels": inside `NoTooltips` by the page's word (`edit_priorities`), and outside
  // in the same words as a literal, since no control imports the page's word list
  // (`components/tooltips.tsx`).
  const addWords =
    page !== null && inRow && advances.length === 0 ? page.CONTROL_LABELS.add_priority : null;
  const triggerName =
    page === null
      ? "Edit priorities"
      : addWords === null
        ? page.CONTROL_LABELS.edit_priorities
        : `${addWords} ${page.CONTROL_LABELS.control_name_to} ${issue.key}`;

  return (
    <div
      className={`flex ${wraps ? "min-w-0 flex-wrap" : "shrink-0"} items-center gap-2 text-sm ${page === null ? textSecondaryOnSurface : inDetails ? pageInkText : pageMutedText}`}
      data-testid={inDetails ? undefined : "issue-advances"}
    >
      {advances.length === 0 || inDetails ? null : (
        <span className="font-medium" {...page?.pageChrome}>
          {page?.PROPERTY_LABELS.advances ?? "Advances"}
        </span>
      )}
      {advances.map((entry) => {
        const label = labels.get(entry.id) ?? entry.id;
        return (
          <span
            className={`flex ${wraps ? "min-w-0" : "shrink-0"} items-center gap-1`}
            key={entry.id}
          >
            {/* Inside `NoTooltips` the priority's title wraps inside its line rather than
                widening it; its words are the person's who wrote it. */}
            <Link
              className={`dispatch-inline-link ${wraps ? "min-w-0 [overflow-wrap:anywhere]" : "shrink-0 whitespace-nowrap"} underline ${page === null ? `${linkText} ${linkHoverText}` : pageInkText}`}
              title={tooltips ? label : undefined}
              to={priorityPath(entry.id)}
              {...page?.authorText}
            >
              {label}
            </Link>
            {entry.inherited ? (
              <span className={`shrink-0 text-xs ${quiet}`} {...page?.pageChrome}>
                {page?.CONTROL_LABELS.advances_inherited ?? "inherited"}
              </span>
            ) : null}
          </span>
        );
      })}
      <MultiSelect
        // Not disabled while it saves what the close committed: a disabled button cannot hold
        // focus, so Escape's return to the trigger would be undone by the save's own render.
        // `setOpen` is what refuses to reopen until the save has landed, and `aria-disabled` is
        // how the reader is told it is busy without focus being taken away.
        ariaDisabled={saving}
        disabled={disabled}
        emptyMessage={
          priorities.isPending
            ? (page?.ANSWER_LINES.loading_priorities ?? "Loading priorities…")
            : "No priority to choose."
        }
        error={inputError}
        label="Priorities"
        onChange={changeDraft}
        onOpenChange={setOpen}
        open={open}
        optionLabel={(id) => labels.get(id) ?? id}
        options={options}
        searchLabel={page?.CONTROL_LABELS.priority_to_advance ?? "Priority to advance"}
        selected={draft?.ids ?? own}
        // One character and its name, as the labels' trigger beside it is: an issue that
        // advances nothing still carries this control, and a word here puts the issue header's
        // metadata rail over its width on a desktop, which would leave every bare issue's rail
        // scrolling and faded (e2e/issue.e2e.ts, "the details rail fades its trailing edge only
        // while it has somewhere to scroll"). A control whose whole label is an icon carries no
        // visible text for WCAG 2.5.3 to match. The character is a plain "+" (U+002B): the
        // fullwidth "＋" this line held draws as an empty box where no CJK font is installed,
        // which is this pod's Chromium and some readers' browsers.
        triggerAriaLabel={triggerName}
        // Inside `NoTooltips` it is 28 px tall from `md`, as the controls beside it in Details
        // are (the repository's box, `main`'s input in `RepoControl.tsx`, with its `md:min-h-7`;
        // the labels' and the components' triggers' `sm:min-h-7`); below 1280 px `styles.css`
        // gives each of them a 44 px floor all the same. Outside it the class is `main`'s.
        triggerClassName={`inline-flex min-h-11 shrink-0 items-center rounded-full px-2 py-2 text-sm whitespace-nowrap outline-none ${page === null ? `${surfaceMutedStrongBg} ${textSecondaryOnSurface} ${textSecondaryHoverToPrimary}` : `${pagePaneBg} ${pageMutedText} hover:text-page-ink md:min-h-7 md:py-1`}`}
      >
        {page === null ? "+" : <span {...page.pageChrome}>{addWords ?? "+"}</span>}
      </MultiSelect>
      {priorities.isError ? (
        <span
          className={`${wraps ? "" : "shrink-0 "}text-xs ${failed}`}
          role="alert"
          {...page?.pageChrome}
        >
          {page?.ANSWER_LINES.priorities_unreadable ?? "Could not load the priorities."}
        </span>
      ) : null}
      {write.error === null ? null : (
        <span
          className={`${wraps ? "max-w-full [overflow-wrap:anywhere] " : "shrink-0 "}text-xs ${failed}`}
          role="alert"
          {...(saveFailure === notSaved ? page?.pageChrome : page?.authorText)}
        >
          {saveFailure}
        </span>
      )}
    </div>
  );
}
