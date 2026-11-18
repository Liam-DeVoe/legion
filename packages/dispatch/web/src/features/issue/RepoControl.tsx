import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";

import { api, apiErrorMessage } from "../../api/client";
import { MultiSelect } from "../../components/MultiSelect";
import { QueryError } from "../../components/QueryError";
import { badgeLow, badgePrimary, textSecondaryOnSurface } from "../../theme/classes";
import {
  badgeReachHandlers,
  badgeSelectBadge,
  badgeSelectRailTrigger,
  badgeSelectRailWrapper,
} from "./badge-select";
import { useIssueRepo } from "./useIssueRepo";

/** The option that clears the issue's own repository, so it resolves to its nearest ancestor's,
 *  else to the project's default, again. */
const INHERIT = "";

/** The grammar a repository GitHub could hold is written in, which is the grammar the server
 *  holds an issue's repository to (`githubCouldHoldRepo`, `issue_repo.go`, itself copied from the
 *  daemon's `ghrepo`): an owner is a letter or digit and then up to 38 letters, digits, hyphens
 *  and underscores - as GitHub serves logins, which is looser than its sign-up form, since
 *  `hello--world`, `artofcode-` and a managed user's `mona-cat_octo` are all served - and a
 *  repository name is at most 100 letters, digits, `.`, `-` and `_`, and is neither `.` nor
 *  `..`. It is written here rather than taken from `ownerRepoPattern`, the looser `owner/name`
 *  shape the settings forms share, because those forms write a repository mapping, which the
 *  server holds to that looser shape alone. This control tests a typed repository against it
 *  before it writes, so a typo shows in the list rather than coming back as the server's 400 a
 *  round trip later. */
const ownerRepo = /^[A-Za-z0-9][A-Za-z0-9_-]{0,38}\/(?!\.{1,2}$)[A-Za-z0-9._-]{1,100}$/;

/** What the list says about a typed value GitHub could hold no repository at. The server says the
 *  same thing in its own words for anything that reaches it. */
const NOT_OWNER_NAME = "Write the repository as owner/name.";

/**
 * The issue's repository in the details rail: `Repository:` and a badge naming the repository the
 * issue resolves to (its own, else its nearest ancestor's, else the project's default, muted when
 * not its own; "No repository" when none resolves), which is the trigger of the shared list's
 * single pick (`MultiSelect`), laid over the badge as `AssigneeControl`'s is, and ends in the
 * shared chevron, since the control it replaced was a visible text box. Labelled
 * `Repository of <KEY>`. The badge is hidden from assistive technology; the trigger's name goes on
 * from its label to the words that read what it shows: the repository,
 * `, inherited or the project's default` when it is not the issue's own, or `No repository`.
 *
 * The list holds Inherit first, which clears the issue's own repository, then the repositories
 * mapped to the issue's project in Settings, and the issue's own repository when no mapping lists
 * it; its text box narrows them as the reader types. The mappings are suggestions rather than the
 * set of values: any `owner/name` may be given, one this project maps, one another project maps,
 * or one no project maps, which is what the server takes. So a typed text that matches no option
 * gets a row of its own, `Use "<typed>"`. A pick writes at once through `useIssueRepo`, and a pick
 * of the value the issue holds writes nothing. A typed text GitHub could hold no repository at
 * (`ownerRepo` above) writes nothing: the list stays open with the text in its box and says so
 * under the options until the text changes. A typed text that passes is written as typed, less
 * the spaces at its ends, and the server's own refusal shows beside the trigger with the write
 * rolled back (`useIssueFieldWrite`). Leaving the list picks nothing; a typed repository is given
 * by its row or Enter.
 *
 * The item renders whenever the issue can carry a repository, which is always: a project that
 * maps none has nothing to suggest, and its issues still take any repository. The mappings are
 * read once the reader reaches for the control (pointer over it, a touch on it, or focus), and at
 * once while the issue resolves to no repository at all, which is the issue most likely to be
 * given one. Until they answer, the list says the repositories are loading where a typed word
 * matches no option. A read that fails costs the suggestions and nothing else: the failure shows
 * beside the trigger with a Retry, and in the list, which still takes a typed repository.
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
  const [open, setOpen] = useState(false);
  /** Whether the typed repository was refused here, for not being `owner/name`. */
  const [refused, setRefused] = useState(false);
  const write = useIssueRepo(issueKey);
  const mapped = (mappings.data ?? [])
    .filter((mapping) => mapping.project === project)
    .map((mapping) => mapping.repo)
    .sort();
  const failure = mappings.isError
    ? apiErrorMessage(mappings.error, `Could not load the repositories mapped to ${project}.`)
    : null;
  // The issue's own repository stays in the list whether or not its project maps it, so the list
  // never lacks the value it marks picked.
  const options = repo === null || mapped.includes(repo) ? mapped : [repo, ...mapped];
  const value = repo ?? INHERIT;
  const tone = repo === null ? badgeLow : badgePrimary;
  const spoken =
    resolvedRepo === null
      ? "No repository"
      : repo === null
        ? `${resolvedRepo}, inherited or the project's default`
        : resolvedRepo;
  // What is read is what the server reads: it drops a `.git` suffix before it judges the name
  // (canonicalRepo), so a clone URL's name is no longer to the list than to the server. The list
  // offers no row for a text that names an option, so the typed text is never the value the
  // issue holds.
  const give = (typed: string): boolean => {
    if (!ownerRepo.test(typed.replace(/\.git$/i, ""))) {
      setRefused(true);
      return false;
    }
    write.submit(typed);
    return true;
  };
  return (
    <div className={`flex shrink-0 items-center gap-2 text-sm ${textSecondaryOnSurface}`}>
      <span className="font-medium">Repository:</span>
      <span
        className={`${badgeSelectRailWrapper} ${disabled ? "opacity-50" : ""}`}
        {...badgeReachHandlers(() => setWanted(true))}
        title={
          repo === null && resolvedRepo !== null ? "Inherited or the project's default" : undefined
        }
      >
        <MultiSelect
          createLabel={(typed) => `Use "${typed}"`}
          disabled={disabled}
          emptyMessage={
            mappings.isPending ? "Loading repositories…" : "No repository matches that."
          }
          error={refused ? NOT_OWNER_NAME : failure}
          label={`Repository of ${issueKey}`}
          mode="single"
          onCreate={give}
          onOpenChange={(next) => {
            setOpen(next);
            setRefused(false);
          }}
          onPick={(picked) => {
            if (picked !== value) write.submit(picked === INHERIT ? null : picked);
          }}
          onQueryChange={() => setRefused(false)}
          open={open}
          optionLabel={(candidate) => (candidate === INHERIT ? "Inherit" : candidate)}
          options={[INHERIT, ...options]}
          searchLabel="Search repositories"
          triggerClassName={badgeSelectRailTrigger}
          value={value}
        >
          {/* A single pick's trigger is named for its list and then for what it shows, and the
              badge is hidden from that name: a screen reader hears the words beside it, the
              target as well as Inherit. */}
          <span
            aria-hidden="true"
            className={`${badgeSelectBadge} font-mono ${tone.bg} ${tone.text}`}
          >
            {resolvedRepo ?? "No repository"}
          </span>
          <span className="sr-only">{spoken}</span>
        </MultiSelect>
      </span>
      {failure === null ? null : (
        <QueryError
          message={failure}
          onRetry={() => void mappings.refetch()}
          retrying={mappings.isFetching}
        />
      )}
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
