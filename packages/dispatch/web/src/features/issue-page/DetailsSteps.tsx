import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useRef } from "react";

import { issueStepsQuery } from "../../api/queries";
import {
  pageInkText,
  pageLineBorder,
  pageMutedText,
  pagePaperBg,
  pageQuietText,
  pageStateText,
  pageStatusProgressText,
} from "../../theme/classes";
import { standsFailed } from "../refs/reference-target";
import { Identifier, Time, useCatchUp } from "../steps/StepsTab";
import { type StepRow, stepTasks } from "../steps/steps-model";
import { DetailsErrorLine, retryRead, useRetryHandOff } from "./details-error";
import { authorText, pageChrome } from "./page-structure";
import { ANSWER_LINES, CONTROL_LABELS } from "./page-words";

/** The label of each field of a run, and how a run ended, in the page's words. */
const FIELDS = CONTROL_LABELS.step_fields;
const RESULTS = CONTROL_LABELS.step_results;

/** A field the record does not hold - a merge GitHub made names no agent, a run that has not
 *  answered names no model, a run that has not ended no end - read as every row of Details reads
 *  a property with no value. */
function None(): ReactNode {
  return (
    <span className={pageQuietText} {...pageChrome}>
      {CONTROL_LABELS.none}
    </span>
  );
}

/** The colour each result Details has a word for is drawn in, as Details' other states are
 *  drawn: a run that runs in the in-progress blue, one that passed in the passed green, one that
 *  failed in the failed red, and one that stopped in the ink every value of a run is drawn in
 *  (`Field`). Each is a state of `StepEnd` but `other`, so a state `StepEnd` adds fails to
 *  compile until it has a colour here. */
const RESULT_COLOURS: Record<"running" | "passed" | "failed" | "stopped", string> = {
  failed: pageStateText.failed,
  passed: pageStateText.passed,
  running: pageStatusProgressText,
  stopped: "",
};

/** How the run ended, or that it runs, in its colour (`RESULT_COLOURS`). A result this build has
 *  no word for shows the server's own. */
function Result({ row }: { row: StepRow }): ReactNode {
  if (row.state === "other") return <span {...authorText}>{row.result}</span>;
  return (
    <span className={`font-medium ${RESULT_COLOURS[row.state]}`} {...pageChrome}>
      {RESULTS[row.state]}
    </span>
  );
}

/** One labelled value of a run: its label in the quiet grey, as Details labels its rows, and the
 *  value in the ink beside it, given the width Details has left so it wraps rather than runs
 *  past it. `field` names it for a reader of the page's markup. */
function Field({
  children,
  field,
  label,
}: {
  children: ReactNode;
  field: string;
  label: string;
}): ReactNode {
  return (
    <div className="grid min-w-0 grid-cols-[88px_minmax(0,1fr)] gap-2" data-step-field={field}>
      <dt className={pageQuietText} {...pageChrome}>
        {label}
      </dt>
      <dd className={`min-w-0 break-words ${pageInkText}`}>{children}</dd>
    </div>
  );
}

/** One run of one step, as a list of labelled values rather than a row of a table, so each
 *  value has the width of Details to wrap in. Its agent, model, start and end are drawn as the
 *  Steps tab draws them (`Identifier`, `Time`), as text the page did not author, and one the
 *  record does not hold reads None. A failed or stopped run that sent its task back names where
 *  it sent it (`StepEnd.returnedTo`). */
function Run({ row }: { row: StepRow }): ReactNode {
  return (
    <li
      className={`min-w-0 rounded-lg border px-3 py-2 ${pageLineBorder} ${pagePaperBg}`}
      data-step-run={row.id}
    >
      <dl className="flex min-w-0 flex-col gap-0.5 text-xs leading-[18px]">
        <Field field="step" label={FIELDS.step}>
          <span className="font-semibold" {...authorText}>
            {row.label}
          </span>
        </Field>
        <Field field="role" label={FIELDS.role}>
          {row.role === "" ? <None /> : <span {...authorText}>{row.role}</span>}
        </Field>
        <Field field="type" label={FIELDS.type}>
          {row.type === "" ? <None /> : <span {...authorText}>{row.type}</span>}
        </Field>
        <Field field="agent" label={FIELDS.agent}>
          {row.agent === "" ? (
            <None />
          ) : (
            <span className="font-mono" {...authorText}>
              <Identifier value={row.agent} />
            </span>
          )}
        </Field>
        <Field field="model" label={FIELDS.model}>
          {row.model === "" ? (
            <None />
          ) : (
            <span className="font-mono" {...authorText}>
              <Identifier value={row.model} />
            </span>
          )}
        </Field>
        <Field field="attempt" label={FIELDS.attempt}>
          <span className="tabular-nums" {...authorText}>
            {row.attempt}
          </span>
        </Field>
        <Field field="started" label={FIELDS.started}>
          {row.started === null ? (
            <None />
          ) : (
            <span {...authorText}>
              <Time at={row.started} />
            </span>
          )}
        </Field>
        <Field field="ended" label={FIELDS.ended}>
          {row.ended === null ? (
            <None />
          ) : (
            <span {...authorText}>
              <Time at={row.ended} />
            </span>
          )}
        </Field>
        <Field field="result" label={FIELDS.result}>
          <Result row={row} />
        </Field>
        {(row.state === "failed" || row.state === "stopped") && row.returnedTo !== null ? (
          <Field field="returned_to" label={FIELDS.returned_to}>
            <span className="whitespace-nowrap" {...authorText}>
              {row.returnedTo}
            </span>
          </Field>
        ) : null}
      </dl>
    </li>
  );
}

/**
 * The steps Legion ran on the issue (`GET /issues/{key}/steps`), under the history in Details,
 * where today's page has a Steps tab: headed by how many runs it lists, then one group per task
 * in the order of each task's first step (`stepTasks`), each run of the task in the order the
 * server read them, with its step, role, type, agent, model, attempt, start, end and result.
 *
 * It reads through the Steps tab's own catch-up (`useCatchUp` in `features/steps/StepsTab.tsx`)
 * while Details is open (`open`), and through its Retry, and in no other way, so a load that
 * leaves Details shut costs no request for it. A step record leaves the daemon on a lane of its
 * own, so the event that comes with it can reach the page before the record reaches the server:
 * the catch-up reads at once and then a few times more, and starts again at each refresh the
 * event feed sends the query (`changesSteps` in `api/event-refresh.ts`), which reaches it through
 * the catch-up's hold on the query (`ownQuery` in `api/query-refresh.ts`). The query itself is
 * never enabled - it would read on its own as Details opens, in the commit the catch-up reads in,
 * and the scheduler, which takes only a read begun after the one asked for as its answer, would
 * send the catch-up's as a second request - and has no retry policy: the catch-up's next read
 * asks again for a read that failed, where the app's retries would go on after Details shuts.
 * What a read answered stays drawn when a later read fails, and no failure is said over it; the
 * failure and its Retry are drawn only while no read has answered. Retry joins a read already out
 * (`retryRead`) and, once a read lands and takes the line with it, hands the focus it held to the
 * heading rather than to the page.
 */
export function DetailsSteps({ issueKey, open }: { issueKey: string; open: boolean }): ReactNode {
  const query = useQuery({ ...issueStepsQuery(issueKey), enabled: false, retry: false });
  useCatchUp(issueKey, open);
  const heading = useRef<HTMLHeadingElement>(null);
  const retry = useRetryHandOff(() => heading.current);
  // The line stands while the read stands failed (`standsFailed`): it holds no answer, and a
  // fetch of it has failed. A refresh that fails over runs the read holds draws no line, and the
  // runs stay drawn. The line stays while the read its Retry sent is out, though that read puts
  // the query back to pending as it starts, so its Retry keeps the focus.
  const failed = standsFailed(query);
  const tasks = query.data === undefined ? undefined : stepTasks(query.data);
  const runs = tasks?.reduce((count, task) => count + task.rows.length, 0);
  return (
    <section
      aria-label={CONTROL_LABELS.steps}
      className="flex min-w-0 flex-col gap-1.5"
      data-testid="issue-steps"
    >
      <h2
        className={`text-sm font-semibold outline-none ${pageInkText}`}
        ref={heading}
        tabIndex={-1}
        {...pageChrome}
      >
        {CONTROL_LABELS.steps}
        {runs === undefined ? null : (
          <>
            {" ("}
            <span {...authorText}>{runs}</span>
            {")"}
          </>
        )}
      </h2>
      {failed ? (
        <DetailsErrorLine
          error={query.error}
          failure={query.errorUpdateCount}
          message={ANSWER_LINES.steps_unreadable}
          onRetry={retryRead(query)}
          {...retry}
          retrying={query.isFetching}
        />
      ) : null}
      {tasks === undefined ? (
        failed ? null : (
          <p className={`text-xs ${pageQuietText}`} {...pageChrome}>
            {ANSWER_LINES.loading_steps}
          </p>
        )
      ) : runs === 0 ? (
        <p className="text-sm">
          <None />
        </p>
      ) : (
        tasks.map(({ rows, task }) => (
          <section
            aria-label={`${CONTROL_LABELS.steps_task} ${task}`}
            className="flex min-w-0 flex-col gap-1.5"
            data-step-task={task}
            key={task}
          >
            <h3 className={`text-xs leading-[18px] font-semibold ${pageMutedText}`} {...pageChrome}>
              {CONTROL_LABELS.steps_task} <span {...authorText}>{task}</span>
            </h3>
            <ol className="flex min-w-0 flex-col gap-1.5">
              {rows.map((row) => (
                <Run key={row.id} row={row} />
              ))}
            </ol>
          </section>
        ))
      )}
    </section>
  );
}
