import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";

import { api, apiErrorMessage } from "../../api/client";
import { MultiSelect } from "../../components/MultiSelect";
import { QueryError } from "../../components/QueryError";
import { badgeLow, badgePrimary, textSecondaryOnSurface } from "../../theme/classes";
import { badgeSelectBadge, badgeSelectRailTrigger, badgeSelectRailWrapper } from "./badge-select";
import { useIssueRepo } from "./useIssueRepo";

/** The option that clears the issue's own repository, so it resolves to its nearest ancestor's,
 *  else to the project's default, again. */
const INHERIT = "";

/**
 * The issue's repository in the details rail: `Repository:` and a badge naming the repository the
 * issue resolves to (its own, else its nearest ancestor's, else the project's default, muted when
 * not its own; "No repository" when none resolves), which is the trigger of the shared list's
 * single pick (`MultiSelect`), laid over the badge as `AssigneeControl`'s is, whose text box
 * narrows the repositories as the reader types. The list holds Inherit first, then the
 * repositories mapped to the issue's project in Settings; Inherit clears the issue's own
 * repository. A pick writes at once, and a pick of the value the issue holds writes nothing.
 * Labelled `Repository of <KEY>`. While the issue resolves to a repository the mappings are read
 * only once the reader reaches for the control (pointer over it, a touch on it, or focus), and
 * until they answer the list holds Inherit and the issue's own repository, saying the
 * repositories are loading where a typed word leaves nothing. While it resolves to none, they are
 * read at once, because the item shows only when the project maps a repository: until the read
 * answers nothing shows, a project that maps none has nothing to pick and gets no item, and a
 * read that fails shows `Repository:` with the failure and a Retry, never a hidden item. A failed
 * read beside a resolved repository shows the same failure next to the picker, and in its list.
 * The badge is hidden from assistive technology; the trigger's description names what it shows:
 * the repository, `, inherited or the project's default` when it is not the issue's own, or
 * `No repository`.
 */
export function RepoControl({
  disabled = false,
  issueKey,
  project,
  repo,
  resolvedRepo,
}: {
  disabled?: boolean;
  issueKey: string;
  project: string;
  repo: string | null;
  resolvedRepo: string | null;
}): ReactNode {
  const [wanted, setWanted] = useState(false);
  const mappings = useQuery({
    enabled: wanted || resolvedRepo === null,
    queryKey: ["repo-projects"],
    queryFn: () => api.listRepoProjects(),
  });
  const want = () => setWanted(true);
  const [open, setOpen] = useState(false);
  const write = useIssueRepo(issueKey);
  const mapped = (mappings.data ?? [])
    .filter((mapping) => mapping.project === project)
    .map((mapping) => mapping.repo)
    .sort();
  const failure = mappings.isError
    ? apiErrorMessage(mappings.error, `Could not load the repositories mapped to ${project}.`)
    : null;
  const loadFailure =
    failure === null ? null : (
      <QueryError
        message={failure}
        onRetry={() => void mappings.refetch()}
        retrying={mappings.isFetching}
      />
    );
  if (resolvedRepo === null && !mappings.isError && (mappings.isPending || mapped.length === 0)) {
    return null;
  }
  // The issue's own repository stays selectable before the list loads, so the control never
  // shows a value its options lack.
  const options = repo === null || mapped.includes(repo) ? mapped : [repo, ...mapped];
  const value = repo ?? INHERIT;
  const tone = repo === null ? badgeLow : badgePrimary;
  const description =
    resolvedRepo === null
      ? "No repository"
      : repo === null
        ? `${resolvedRepo}, inherited or the project's default`
        : resolvedRepo;
  // Nothing resolved and no list to pick from: the failure is all there is to show.
  const picker =
    resolvedRepo === null && mappings.isError ? null : (
      // biome-ignore lint/a11y/noStaticElementInteractions: nothing here is made interactive - the handlers only keep a press on the trigger, or in the list it opens (portalled, but a React child of this one), out of the header under it, and read the mappings as the reader reaches for either, so the wrapper needs no role of its own. Every open panel reads a press in the capture phase (`AnchoredPanel`), so stopping it here hides it from none of them.
      <span
        className={`${badgeSelectRailWrapper} ${disabled ? "opacity-50" : ""}`}
        onFocus={want}
        onMouseDown={(event) => event.stopPropagation()}
        onPointerDown={(event) => event.stopPropagation()}
        onPointerEnter={want}
        onTouchStart={(event) => {
          event.stopPropagation();
          want();
        }}
        title={
          repo === null && resolvedRepo !== null ? "Inherited or the project's default" : undefined
        }
      >
        <MultiSelect
          disabled={disabled}
          emptyMessage={
            mappings.isPending ? "Loading repositories…" : "No repository matches that."
          }
          error={failure}
          label={`Repository of ${issueKey}`}
          mode="single"
          onOpenChange={setOpen}
          onPick={(picked) => {
            if (picked !== value) write.submit(picked === INHERIT ? null : picked);
          }}
          open={open}
          optionLabel={(candidate) => (candidate === INHERIT ? "Inherit" : candidate)}
          options={[INHERIT, ...options]}
          searchLabel="Search repositories"
          triggerClassName={badgeSelectRailTrigger}
          value={value}
        >
          {/* A single pick's trigger reads what it holds as its description, and the badge is
              hidden from it: a screen reader hears the words beside it, the target as well as
              Inherit. */}
          <span
            aria-hidden="true"
            className={`${badgeSelectBadge} font-mono ${tone.bg} ${tone.text}`}
          >
            {resolvedRepo ?? "No repository"}
          </span>
          <span className="sr-only">{description}</span>
        </MultiSelect>
      </span>
    );
  return (
    <div className={`flex shrink-0 items-center gap-2 text-sm ${textSecondaryOnSurface}`}>
      <span className="font-medium">Repository:</span>
      {picker}
      {loadFailure}
      {write.failed ? (
        <QueryError
          message={write.error ?? `Could not update the repository of ${issueKey}.`}
          onRetry={write.retry}
          retrying={write.pending}
        />
      ) : null}
    </div>
  );
}
