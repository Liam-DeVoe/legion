import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";

import { api, apiErrorMessage } from "../../api/client";
import { MultiSelect } from "../../components/MultiSelect";
import { QueryError } from "../../components/QueryError";
import { badgeLow, badgePrimary, textSecondaryOnSurface } from "../../theme/classes";
import { badgeListTrigger, badgeSelectBadge, badgeSelectRailWrapper } from "./badge-select";
import { useIssueRepo } from "./useIssueRepo";

/**
 * The issue's repository in the details rail: `Repository:` and a badge naming the repository the
 * issue resolves to (its own, else its nearest ancestor's, else the project's default, muted when
 * not its own; "No repository" when none resolves), which is the trigger of the shared list's
 * single pick (`MultiSelect`), as `AssigneeControl` is. The list holds Inherit first, then the
 * repositories mapped to the issue's project in Settings, and its text box narrows them; Inherit
 * clears the issue's own repository. Its closed trigger takes the list's keys (`keysOnTrigger`),
 * so an arrow, Home, End, Page Up, Page Down or a letter only moves the list's highlight, and only
 * Enter or a click writes; a pick of the value the issue already holds writes nothing. Labelled
 * `Repository of <KEY>`. While the issue resolves to a repository the mappings are read only once
 * the reader reaches for the control (pointer over it, a touch on it, or focus). While it resolves
 * to none, they are read at once, because the item shows only when the project maps a repository:
 * until the read answers nothing shows, a project that maps none has nothing to pick and gets no
 * item, and a read that fails shows `Repository:` with the failure and a Retry, never a hidden
 * item. A failed read beside a resolved repository shows the same failure next to the picker. The
 * badge is hidden from assistive technology; the trigger's description names what it shows: the
 * repository, `, inherited or the project's default` when it is not the issue's own, or
 * `No repository`. `held` is the host's own write out (the issue page's other field writes): the
 * trigger keeps the focus and its Tab stop (`aria-disabled`), and a pick made then is dropped.
 */
export function RepoControl({
  disabled = false,
  held = false,
  issueKey,
  project,
  repo,
  resolvedRepo,
}: {
  disabled?: boolean;
  held?: boolean;
  issueKey: string;
  project: string;
  repo: string | null;
  resolvedRepo: string | null;
}): ReactNode {
  const [wanted, setWanted] = useState(false);
  const [open, setOpen] = useState(false);
  const mappings = useQuery({
    enabled: wanted || resolvedRepo === null,
    queryKey: ["repo-projects"],
    queryFn: () => api.listRepoProjects(),
  });
  const want = () => setWanted(true);
  const write = useIssueRepo(issueKey);
  const mapped = (mappings.data ?? [])
    .filter((mapping) => mapping.project === project)
    .map((mapping) => mapping.repo)
    .sort();
  const loadFailure = mappings.isError ? (
    <QueryError
      message={apiErrorMessage(
        mappings.error,
        `Could not load the repositories mapped to ${project}.`
      )}
      onRetry={() => void mappings.refetch()}
      retrying={mappings.isFetching}
    />
  ) : null;
  if (resolvedRepo === null && !mappings.isError && (mappings.isPending || mapped.length === 0)) {
    return null;
  }
  // The issue's own repository stays in the list before the list loads, so the list always holds
  // the value it marks. `""` is Inherit.
  const options = repo === null || mapped.includes(repo) ? mapped : [repo, ...mapped];
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
      // biome-ignore lint/a11y/noStaticElementInteractions: nothing here is made interactive - the handlers only keep a press on the trigger, or in the list it opens (portalled, but a React child of this one), out of the header under it, and read the mappings as the reader reaches for either, so the wrapper needs no role of its own.
      <span
        className={`${badgeSelectRailWrapper} ${disabled || held ? "opacity-50" : ""}`}
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
          ariaDisabled={held}
          disabled={disabled}
          emptyMessage={
            mappings.isPending ? "Loading repositories…" : "No repository matches that."
          }
          keysOnTrigger
          label={`Repository of ${issueKey}`}
          mode="single"
          onOpenChange={setOpen}
          onPick={(candidate) => {
            const picked = candidate === "" ? null : candidate;
            if (held || picked === repo) return;
            write.submit(picked);
          }}
          open={open}
          optionLabel={(candidate) => (candidate === "" ? "Inherit" : candidate)}
          options={["", ...options]}
          searchLabel="Search repositories"
          triggerClassName={badgeListTrigger}
          value={repo ?? ""}
        >
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
