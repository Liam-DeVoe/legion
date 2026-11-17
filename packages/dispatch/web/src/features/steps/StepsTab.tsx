import { useQuery } from "@tanstack/react-query";
import { Fragment, type ReactNode } from "react";

import { issueStepsQuery } from "../../api/queries";
import { EmptyState } from "../../components/EmptyState";
import { QueryError } from "../../components/QueryError";
import {
  badgeMed,
  borderDefault,
  card,
  dangerText,
  successText,
  surfaceMutedBg,
  textMutedOnCanvas,
  textMutedOnSurface,
  textPrimaryOnSurface,
  textSecondaryOnCanvas,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { type StepRow, stepTasks } from "./steps-model";

const COLUMNS = [
  "Step",
  "Role",
  "Type",
  "Agent",
  "Model",
  "Attempt",
  "Started",
  "Ended",
  "Result",
] as const;

/** A field the record does not hold: a merge GitHub made has no agent, a run that has not
 *  answered yet has no model, a run that started before the release that records steps has no
 *  start. */
const NONE = "—";

function Cell({ children, className = "" }: { children: ReactNode; className?: string }) {
  return <td className={`px-1.5 py-1.5 align-top ${className}`}>{children}</td>;
}

/** A run's start and end are read against each other, down the table, so each is the instant
 *  itself (`Nov 16 8:00 AM`) rather than a distance from now, which would make a forty-minute run
 *  and the one after it read alike. The date and the time each stay whole, so a narrow column
 *  puts the time under the date and never splits either; the year shows when it is not this one,
 *  and the whole date and time is the title. */
const day = new Intl.DateTimeFormat(undefined, { day: "numeric", month: "short" });
const dayOfAnotherYear = new Intl.DateTimeFormat(undefined, {
  day: "numeric",
  month: "short",
  year: "numeric",
});
const timeOfDay = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" });

function Time({ at }: { at: string | null }): ReactNode {
  if (at === null) return NONE;
  const date = new Date(at);
  if (Number.isNaN(date.getTime())) return <time>{at}</time>;
  const dayFormat = date.getFullYear() === new Date().getFullYear() ? day : dayOfAnotherYear;
  return (
    <time dateTime={at} title={date.toLocaleString()}>
      <span className="whitespace-nowrap">{dayFormat.format(date)}</span>{" "}
      <span className="whitespace-nowrap">{timeOfDay.format(date)}</span>
    </time>
  );
}

/** An agent token or a model id, or the dash for one the record does not hold. A token breaks at
 *  its hyphens on its own; a model id (`anthropic/claude-opus-5`) is also offered a break after
 *  each slash, so neither forces its column wider than its longest part. */
function Identifier({ value }: { value: string }): ReactNode {
  if (value === "") return NONE;
  const parts = value.split("/");
  // Each part is keyed by the text up to and including it, which no other part of the id shares.
  return parts.map((part, index) => {
    const through = parts.slice(0, index + 1).join("/");
    return index === 0 ? (
      <Fragment key={through}>{part}</Fragment>
    ) : (
      <Fragment key={through}>
        <wbr />/{part}
      </Fragment>
    );
  });
}

/** How the run ended, or that it runs: a failed or stopped run that sent its task back names the
 *  step it sent it to, and a result this build has no word for shows the server's own. */
function Result({ row }: { row: StepRow }): ReactNode {
  switch (row.state) {
    case "running":
      return (
        <span
          className={`rounded-sm px-1.5 py-0.5 text-xs font-medium ${badgeMed.bg} ${badgeMed.text}`}
        >
          running
        </span>
      );
    case "passed":
      return <span className={`font-medium ${successText}`}>passed</span>;
    case "other":
      return row.result;
    case "failed":
    case "stopped":
      return (
        <>
          <span className={`font-medium ${row.state === "failed" ? dangerText : ""}`}>
            {row.state}
          </span>
          {row.returnedTo === null ? null : (
            <span className={textMutedOnSurface}>, returned to step {row.returnedTo}</span>
          )}
        </>
      );
  }
}

/** One task's runs, one row each, in the order the server read them. */
function TaskTable({ rows, task }: { rows: readonly StepRow[]; task: string }): ReactNode {
  return (
    <section className="min-w-0 space-y-2">
      <h2 className={`text-sm font-semibold ${textSecondaryOnCanvas}`}>Task {task}</h2>
      {/* `relative` keeps the scrolled table's cells inside this box, as the Settings tables do
          (`features/settings/classes.ts`); the table scrolls sideways where the page is narrower
          than its columns. */}
      <div className={`relative overflow-x-auto rounded-xl border ${card}`}>
        <table aria-label={`Steps of task ${task}`} className="w-full text-left text-xs">
          <thead
            className={`border-b ${surfaceMutedBg} ${borderDefault} ${textSecondaryOnSurface}`}
          >
            <tr>
              {COLUMNS.map((column) => (
                <th
                  className="px-1.5 py-1.5 font-semibold whitespace-nowrap"
                  key={column}
                  scope="col"
                >
                  {column}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr className={`border-b last:border-0 ${borderDefault}`} key={row.id}>
                <Cell className={`font-medium whitespace-nowrap ${textPrimaryOnSurface}`}>
                  {row.label}
                </Cell>
                <Cell className={`whitespace-nowrap ${textPrimaryOnSurface}`}>{row.role}</Cell>
                <Cell className={`whitespace-nowrap ${textSecondaryOnSurface}`}>
                  {row.type === "" ? NONE : row.type}
                </Cell>
                <Cell className={`font-mono break-words ${textSecondaryOnSurface}`}>
                  <Identifier value={row.agent} />
                </Cell>
                <Cell className={`font-mono break-words ${textSecondaryOnSurface}`}>
                  <Identifier value={row.model} />
                </Cell>
                <Cell className={`tabular-nums ${textSecondaryOnSurface}`}>{row.attempt}</Cell>
                <Cell className={textMutedOnSurface}>
                  <Time at={row.started} />
                </Cell>
                <Cell className={textMutedOnSurface}>
                  <Time at={row.ended} />
                </Cell>
                <Cell className={textPrimaryOnSurface}>
                  <Result row={row} />
                </Cell>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

/**
 * The steps Legion ran on the issue (`GET /issues/{key}/steps`): one table per task, in the order
 * of each task's first step, and one row per run of a step - its role, type, agent, model,
 * attempt, start, end and result. The records are the Legion daemon's; this tab only reads them.
 */
export function StepsTab({ issueKey }: { issueKey: string }): ReactNode {
  const query = useQuery(issueStepsQuery(issueKey));
  if (query.isPending) {
    return <p className={textMutedOnCanvas}>Loading steps…</p>;
  }
  if (query.isError) {
    return (
      <QueryError
        message={`Couldn't load the steps of ${issueKey}: ${query.error.message}`}
        onRetry={() => void query.refetch()}
        retrying={query.isFetching}
      />
    );
  }
  const tasks = stepTasks(query.data);
  if (tasks.length === 0) {
    return (
      <EmptyState label="Steps empty state" message="No step of this issue has run in Legion." />
    );
  }
  return (
    <div className="min-w-0 space-y-6">
      {tasks.map(({ rows, task }) => (
        <TaskTable key={task} rows={rows} task={task} />
      ))}
    </div>
  );
}
