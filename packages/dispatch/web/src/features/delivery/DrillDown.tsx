import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useRef } from "react";
import { Link } from "react-router-dom";

import { deliveryRunQuery } from "../../api/queries";
import type {
  DeliveryComponent,
  DeliveryPR,
  DeliveryRun,
  DeliveryRunJobDetail,
  DeliverySessionGroup,
} from "../../api/types";
import { Button } from "../../components/Button";
import {
  borderDefault,
  dangerText,
  inlineWarningText,
  linkChrome,
  surfaceBg,
  textMutedOnSurface,
  textPrimaryOnSurface,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { AgentName } from "../refs/AgentName";
import { buildIssuePath } from "../refs/routes";
import { useDialog } from "../shell/useDialog";
import { NO_COMPONENT, NO_ISSUE, NO_PRIORITY, NO_SESSION, PLACEHOLDER_LABELS } from "./lib/facets";
import { parentAgentLabel } from "./lib/parentAgent";
import { leadTimeMinutes } from "./lib/prList";
import { storedSessionTitle } from "./lib/sessionTitles";
import type { TimelineSelection } from "./Timeline";

const sectionLabel = `mb-1.5 text-xs font-medium ${textMutedOnSurface}`;
const chromeLink = `self-start text-sm ${linkChrome}`;

function fmt(iso: string | null): string {
  return iso === null ? "\u2014" : new Date(iso).toLocaleString();
}

function jobDurationMs(job: DeliveryRunJobDetail): number | null {
  if (job.started_at === null || job.completed_at === null) return null;
  return new Date(job.completed_at).getTime() - new Date(job.started_at).getTime();
}

function PRDetail({
  pr,
  components,
  sessionGroups,
  sessionTitles,
}: {
  pr: DeliveryPR;
  components: Readonly<Record<string, DeliveryComponent>>;
  sessionGroups: readonly DeliverySessionGroup[];
  sessionTitles: Readonly<Record<string, string>>;
}): ReactNode {
  const leadTime = leadTimeMinutes(pr);
  let componentText = PLACEHOLDER_LABELS[NO_ISSUE];
  if (pr.issue !== null) {
    componentText =
      (pr.components ?? []).length === 0
        ? PLACEHOLDER_LABELS[NO_COMPONENT]
        : (pr.components ?? []).map((id) => components[id]?.title ?? id).join(", ");
  }
  return (
    <div className="flex flex-col gap-3">
      <h3
        className={`pr-10 text-[15px] leading-6 font-semibold break-words ${textPrimaryOnSurface}`}
      >
        {pr.id}{" "}
        {pr.rework ? (
          <span className={`ml-2 align-middle text-xs ${inlineWarningText}`}>rework</span>
        ) : null}
      </h3>
      <p className={`-mt-2 text-sm ${textSecondaryOnSurface}`}>{pr.title}</p>
      <a className={chromeLink} href={pr.url} rel="noreferrer" target="_blank">
        Open on GitHub
      </a>
      <dl
        className={`grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 border-t pt-3 text-sm ${borderDefault} ${textPrimaryOnSurface}`}
      >
        <dt className={textMutedOnSurface}>Author</dt>
        <dd>{pr.author}</dd>
        <dt className={textMutedOnSurface}>Dispatch issue</dt>
        <dd>
          {pr.issue === null ? (
            "\u2014"
          ) : (
            <Link className={linkChrome} to={buildIssuePath({ key: pr.issue, kind: "issue" })}>
              {pr.issue}
              {pr.issue_title === null ? "" : ` \u2014 ${pr.issue_title}`}
            </Link>
          )}
        </dd>
        <dt className={textMutedOnSurface}>Priority</dt>
        <dd>
          {pr.issue === null
            ? PLACEHOLDER_LABELS[NO_ISSUE]
            : (pr.priority ?? PLACEHOLDER_LABELS[NO_PRIORITY])}
        </dd>
        <dt className={textMutedOnSurface}>Components</dt>
        <dd>{componentText}</dd>
        <dt className={textMutedOnSurface}>Parent agent(s)</dt>
        <dd>
          {pr.parent_agent === null
            ? PLACEHOLDER_LABELS[NO_SESSION]
            : parentAgentLabel(pr.parent_agent, sessionGroups)}
        </dd>
        <dt className={textMutedOnSurface}>Sessions</dt>
        {/* Each session wraps onto its own line rather than being cut, so every one stays
            reachable; the delivery read's titles hold a session's id when it has no title. */}
        <dd className="flex min-w-0 flex-col items-start break-words">
          {(pr.sessions ?? []).length === 0
            ? "\u2014"
            : (pr.sessions ?? []).map((session) => (
                <AgentName
                  className="max-w-full"
                  key={session}
                  sessionId={session}
                  storedTitle={storedSessionTitle(sessionTitles, session)}
                />
              ))}
        </dd>
        <dt className={textMutedOnSurface}>Merged</dt>
        <dd>{fmt(pr.merged_at)}</dd>
        <dt className={textMutedOnSurface}>Deployed</dt>
        <dd>{fmt(pr.deployed_at)}</dd>
        <dt className={textMutedOnSurface}>Lead time (merge→prod)</dt>
        <dd>{leadTime === undefined ? "not yet deployed" : `${Math.round(leadTime)} min`}</dd>
      </dl>
      {pr.partial ? (
        <p className={`text-sm ${textMutedOnSurface}`}>
          Still completing: some fields await the next reconcile pass.
        </p>
      ) : null}
      {pr.unfetchable_reason === null ? null : (
        <p className={`text-sm ${textMutedOnSurface}`}>
          Can no longer be fetched from GitHub: {pr.unfetchable_reason}
        </p>
      )}
    </div>
  );
}

/** Every job of the run, with each result, read once the drill-down opens. */
function RunJobs({ runId, durations }: { runId: number; durations: boolean }): ReactNode {
  const detail = useQuery(deliveryRunQuery(runId));
  if (detail.isPending) return <div className={`text-sm ${textMutedOnSurface}`}>Loading…</div>;
  if (detail.isError) {
    return <div className={`text-sm ${textMutedOnSurface}`}>The run's jobs could not be read.</div>;
  }
  return (detail.data.jobs ?? []).map((job) => {
    const durationMs = jobDurationMs(job);
    return (
      <div
        className={`flex justify-between gap-3 border-t py-1.5 text-sm first:border-t-0 ${borderDefault}`}
        key={job.name}
      >
        <span className={`min-w-0 break-words ${textPrimaryOnSurface}`}>{job.name}</span>
        <span
          className={`shrink-0 tabular-nums ${job.conclusion === "failure" ? dangerText : textSecondaryOnSurface}`}
        >
          {job.conclusion}
          {durations && durationMs !== null ? ` (${Math.round(durationMs / 1000)}s)` : ""}
        </span>
      </div>
    );
  });
}

function DeployDetail({ run }: { run: DeliveryRun }): ReactNode {
  return (
    <div className="flex flex-col gap-3">
      <h3 className={`pr-10 text-[15px] leading-6 font-semibold ${textPrimaryOnSurface}`}>
        Deploy: run #{run.id}
      </h3>
      <a className={`-mt-2 ${chromeLink}`} href={run.url} rel="noreferrer" target="_blank">
        Open run
      </a>
      <div>
        <div className={sectionLabel}>PRs shipped ({(run.prs ?? []).length})</div>
        {(run.prs ?? []).map((pr) => (
          <div className={`py-0.5 text-sm break-words ${textPrimaryOnSurface}`} key={pr.id}>
            {pr.id} {`\u2014 ${pr.title}`}
          </div>
        ))}
      </div>
      <div>
        <div className={sectionLabel}>Jobs</div>
        <RunJobs durations runId={run.id} />
      </div>
    </div>
  );
}

function FailureDetail({ run }: { run: DeliveryRun }): ReactNode {
  return (
    <div className="flex flex-col gap-3">
      <h3 className={`pr-10 text-[15px] leading-6 font-semibold ${dangerText}`}>
        Pipeline failure: run #{run.id}
      </h3>
      <a className={`-mt-2 ${chromeLink}`} href={run.url} rel="noreferrer" target="_blank">
        Open run
      </a>
      <div>
        <div className={sectionLabel}>Failed jobs</div>
        {(run.failed_jobs ?? []).map((job) => (
          <div className={`py-0.5 text-sm ${dangerText}`} key={job.name}>
            {job.name} at {fmt(job.completed_at)}
          </div>
        ))}
        {(run.failed_jobs ?? []).length === 0 && run.production?.conclusion === "failure" ? (
          <div className={`text-sm ${dangerText}`}>
            the production job at {fmt(run.production.completed_at)}
          </div>
        ) : null}
      </div>
      <div>
        <div className={sectionLabel}>All jobs</div>
        <RunJobs durations={false} runId={run.id} />
      </div>
    </div>
  );
}

/** The timeline and the list's shared drill-down: a PR, a deploy, or a pipeline failure, in a
 *  full-height panel at the right edge with a close button. It is a dialog (`useDialog`): focus
 *  starts on the close button and stays inside, Escape closes it, and closing hands focus back to
 *  whatever opened it. */
export function DrillDown({
  prs,
  runs,
  components,
  sessionGroups,
  sessionTitles,
  selection,
  onClose,
}: {
  prs: readonly DeliveryPR[];
  runs: readonly DeliveryRun[];
  components: Readonly<Record<string, DeliveryComponent>>;
  sessionGroups: readonly DeliverySessionGroup[];
  /** The delivery read's session titles: each session's stored title, or its id when none. */
  sessionTitles: Readonly<Record<string, string>>;
  selection: TimelineSelection | null;
  onClose: () => void;
}): ReactNode {
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const dialog = useDialog<HTMLElement>({
    initialFocusRef: closeButtonRef,
    onClose,
    open: selection !== null,
  });
  if (selection === null) return null;

  let body: ReactNode;
  if (selection.kind === "pr") {
    const pr = prs.find((candidate) => candidate.id === selection.id);
    body =
      pr === undefined ? (
        <div>PR {selection.id} not found.</div>
      ) : (
        <PRDetail
          components={components}
          pr={pr}
          sessionGroups={sessionGroups}
          sessionTitles={sessionTitles}
        />
      );
  } else {
    const run = runs.find((candidate) => candidate.id === selection.id);
    if (run === undefined) body = <div>Run {selection.id} not found.</div>;
    else if (selection.kind === "deploy") body = <DeployDetail run={run} />;
    else body = <FailureDetail run={run} />;
  }

  return (
    <aside
      aria-label="Details"
      aria-modal="true"
      className={`fixed top-0 right-0 z-30 h-full w-96 max-w-full overflow-y-auto border-l p-5 shadow-xl ${borderDefault} ${surfaceBg}`}
      ref={dialog.containerRef}
      role="dialog"
    >
      <Button
        aria-label="Close details"
        className="absolute top-3 right-3"
        iconOnly
        onClick={onClose}
        ref={closeButtonRef}
        variant="ghost-muted"
      >
        <svg aria-hidden="true" className="size-4" fill="none" viewBox="0 0 16 16">
          <path
            d="m4 4 8 8m0-8-8 8"
            stroke="currentColor"
            strokeLinecap="round"
            strokeWidth="1.5"
          />
        </svg>
      </Button>
      {body}
    </aside>
  );
}
