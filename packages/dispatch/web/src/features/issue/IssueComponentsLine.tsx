import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { Link } from "react-router-dom";

import { api } from "../../api/client";
import { projectArchitectureSourcesQuery } from "../../api/queries";
import type { Issue } from "../../api/types";
import { usePageWords, useTooltips } from "../../components/tooltips";
import {
  badgeHigh,
  badgePrimary,
  dangerText,
  linkHoverText,
  linkText,
  pageFailedText,
  pageInkText,
  pageLabelChip,
  pageMutedText,
  pageQuietText,
  pageWaitingChip,
  textMutedOnSurface,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { ComponentPicker } from "../architecture/ComponentPicker";
import { buildIssuePath } from "../refs/routes";
import { useIssueComponents } from "./useIssueComponents";

/** The name the components picker's trigger answers to, in its words on every page: what it
 *  opens and the issue it is on, so a voice user saying what they read presses it (WCAG
 *  2.5.3). The new issue page's add menu reaches this trigger by this name rather than drawing
 *  a second picker, so a word changed here changes both sides at once. */
export function componentsControlName(issueKey: string): string {
  return `Edit components of ${issueKey}`;
}

/**
 * The `Components:` line of the issue header's metadata rail: the issue's effective
 * attachment as chips. An explicit set lists its live component ids and marks each id a
 * re-import retired; `none` shows the reason; an issue no ancestor chain attached reads
 * `Not attached`. When the attachment is an ancestor's, that ancestor is named and linked.
 * The line is editable only when the project has an architecture source — the small
 * `["architecture-sources", project]` list the project page shares, whose `[]` means
 * read-only — through the same `ComponentPicker` the Architecture pane uses, offering every
 * source's components: a picked set saves `explicit`, clearing every pick saves `inherit`
 * (back to the ancestors'). Ids are project-wide, so a chip is the bare id whichever source
 * supplies it. The component list itself (the whole tree, refetched on every issue event in
 * the project) is fetched only while the picker is open.
 *
 * Inside `NoTooltips` (`components/tooltips.tsx`, the new issue page) it says that page's
 * words with their marks and carries no `title`. There `inRow` draws it as the value of a row
 * of Details, which draws the label (dispatch://INFRA-1196 R13, R19): no `Components:` of its
 * own, a line that wraps, and each component named by its title from the architecture read,
 * which it then makes as soon as the issue names a component.
 */
export function IssueComponentsLine({
  inRow = false,
  issue,
}: {
  /** Whether the line is the value of a row of the new issue page's Details. It acts inside
   *  `NoTooltips` alone. */
  inRow?: boolean;
  issue: Pick<Issue, "components" | "key" | "project" | "status" | "title">;
}): ReactNode {
  const tooltips = useTooltips();
  const page = usePageWords();
  const inDetails = page !== null && inRow;
  const { components } = issue;
  const sources = useQuery(projectArchitectureSourcesQuery(issue.project));
  const [pickerOpen, setPickerOpen] = useState(false);
  const namesComponents = components.mode === "explicit" && (components.ids ?? []).length > 0;
  const tree = useQuery({
    queryKey: ["architecture", issue.project],
    queryFn: () => api.getArchitecture(issue.project),
    enabled: pickerOpen || (inDetails && namesComponents),
  });
  const componentName = (id: string) =>
    inDetails
      ? ((tree.data?.components ?? []).find((component) => component.id === id)?.title ?? id)
      : id;
  // In a row of Details nothing of the line stops its wrap (dispatch://INFRA-3935 R4): the
  // words break between themselves, and a long name breaks inside its chip only where no break
  // between words is left. Everywhere else each part stays whole, as on `main`.
  const part = inDetails ? "min-w-0 [overflow-wrap:anywhere]" : "shrink-0";
  // Inside `NoTooltips` each colour is one of the new issue page's (dispatch://INFRA-3935 R6,
  // decision 56 of dispatch://INFRA-1196/artifact/part-decisions-md): the line is the ink in a row
  // of Details (the prototype's `.frow .fv`) and the muted grey elsewhere (its `#hdr-props`); a
  // component is the page's label chip, a retired one its waiting chip, the one warning here;
  // what qualifies the set is quiet words, the issue it is inherited from a link in the ink, and
  // a refused save the failed red.
  const quiet = page === null ? textMutedOnSurface : pageQuietText;
  const write = useIssueComponents(issue);
  const inheritedFrom =
    components.inherited_from === null ? null : (
      <span className={`${part} text-xs ${quiet}`} {...page?.pageChrome}>
        {page?.CONTROL_LABELS.components_inherited_from ?? "inherited from"}{" "}
        <Link
          className={`dispatch-inline-link underline ${page === null ? `${linkText} ${linkHoverText}` : pageInkText}`}
          to={buildIssuePath({ key: components.inherited_from, kind: "issue" })}
          {...page?.authorText}
        >
          {components.inherited_from}
        </Link>
      </span>
    );
  return (
    <div
      className={`flex ${inDetails ? "min-w-0 flex-wrap" : "shrink-0"} items-center gap-2 text-sm ${page === null ? textSecondaryOnSurface : inDetails ? pageInkText : pageMutedText}`}
      data-testid="issue-components"
    >
      {inDetails ? null : (
        <span className="font-medium" {...page?.pageChrome}>
          {page === null ? "Components:" : `${page.PROPERTY_LABELS.components}:`}
        </span>
      )}
      {components.mode === "inherit" ? (
        <span className={quiet} {...page?.pageChrome}>
          {page?.CONTROL_LABELS.components_not_attached ?? "Not attached"}
        </span>
      ) : components.mode === "none" ? (
        <span className={quiet} {...page?.pageChrome}>
          {page === null ? (
            <>None — {components.reason}</>
          ) : (
            <>
              {page.CONTROL_LABELS.components_none} —{" "}
              <span {...page.authorText}>{components.reason}</span>
            </>
          )}
        </span>
      ) : (
        <>
          {(components.ids ?? []).map((id) => (
            <span
              className={`inline-flex ${part} items-center rounded-full px-2 py-0.5 text-xs font-medium ${page === null ? `${badgePrimary.bg} ${badgePrimary.text}` : pageLabelChip}`}
              key={id}
              {...page?.authorText}
            >
              {componentName(id)}
            </span>
          ))}
          {(components.unknown ?? []).map((id) => (
            <span
              className={`inline-flex ${part} items-center rounded-full px-2 py-0.5 text-xs font-medium ${page === null ? `${badgeHigh.bg} ${badgeHigh.text}` : pageWaitingChip}`}
              key={id}
              title={
                tooltips ? `${id} is no longer in the project's architecture model` : undefined
              }
              {...page?.pageChrome}
            >
              {page === null ? (
                <>retired: {id}</>
              ) : (
                <>
                  {page.CONTROL_LABELS.components_retired}: <span {...page.authorText}>{id}</span>
                </>
              )}
            </span>
          ))}
        </>
      )}
      {inheritedFrom}
      {(sources.data?.length ?? 0) === 0 ? null : (
        <ComponentPicker
          components={tree.data?.components ?? []}
          error={tree.error === null ? null : tree.error.message}
          loading={tree.data === undefined && tree.error === null}
          onOpenChange={setPickerOpen}
          onSave={(ids) =>
            void write.submit(ids.length === 0 ? { mode: "inherit" } : { ids, mode: "explicit" })
          }
          saving={write.pending}
          selected={
            components.mode === "explicit" && components.inherited_from === null
              ? (components.ids ?? [])
              : []
          }
          triggerAriaLabel={componentsControlName(issue.key)}
        >
          {page === null ? (
            components.mode === "explicit" && components.inherited_from === null ? (
              "+"
            ) : (
              "Set components"
            )
          ) : (
            <span {...page.pageChrome}>
              {components.mode === "explicit" && components.inherited_from === null
                ? "+"
                : page.CONTROL_LABELS.components_set}
            </span>
          )}
        </ComponentPicker>
      )}
      {write.error === null ? null : (
        <span
          className={`shrink-0 text-xs ${page === null ? dangerText : pageFailedText}`}
          role="alert"
          {...page?.authorText}
        >
          {write.error}
        </span>
      )}
    </div>
  );
}
