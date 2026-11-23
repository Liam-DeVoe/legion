import { type ReactNode, useId } from "react";

import type { EvalKind } from "../../api/queries";
import { QueryError } from "../../components/QueryError";
import { textMutedOnCanvas, textPrimaryOnCanvas } from "../../theme/classes";
import { type EvalsScope, type EvalsViewStatus, useEvalsViews } from "./useEvalsViews";

/** A section's own state: `failed` while any of its views is refused, else `subscribed` while any
 *  has not answered, else `painted`. A section with no view (the merge timeline, closed) is
 *  `closed`. */
type SectionState = EvalsViewStatus | "closed";

function sectionState(statuses: readonly EvalsViewStatus[]): SectionState {
  if (statuses.length === 0) return "closed";
  if (statuses.includes("failed")) return "failed";
  return statuses.includes("subscribed") ? "subscribed" : "painted";
}

/**
 * One section of the Evals tab (dispatch://INFRA-2918/spec R2, R3, R5): its heading, and what its
 * own views answer, painted as each lands, whatever another section's views do. Each section reads
 * its views itself, so a delta of one view renders only the sections that read it. While a view
 * has not answered the section says so on a line of its own; a refused view shows its error with
 * Retry; a section whose views answered no eval reads "no data". What the section draws of its
 * evals is the task of the section's own change: this lists them by name.
 */
export function EvalsSection({
  id,
  kinds,
  scope,
  title,
}: {
  id: string;
  /** The kinds of eval the section reads, each its own view. */
  kinds: readonly EvalKind[];
  scope: EvalsScope;
  title: string;
}): ReactNode {
  const headingId = useId();
  const views = useEvalsViews(scope, kinds);
  const state = sectionState(views.map((view) => view.status));
  const items = views.flatMap((view) => view.items);
  return (
    <section
      aria-labelledby={headingId}
      className="flex flex-col gap-2"
      data-section={id}
      data-state={state}
    >
      <h2 className={`text-[15px] font-semibold ${textPrimaryOnCanvas}`} id={headingId}>
        {title}
      </h2>
      {state === "failed" ? (
        <QueryError
          message={`${title}: could not load.`}
          onRetry={() => {
            for (const view of views) if (view.status === "failed") view.retry();
          }}
        />
      ) : null}
      {state === "subscribed" ? (
        <p className={`text-sm ${textMutedOnCanvas}`} role="status">
          {title}: loading…
        </p>
      ) : null}
      {state === "painted" && items.length === 0 ? (
        <p className={`text-sm ${textMutedOnCanvas}`}>no data</p>
      ) : null}
      {items.length === 0 ? null : (
        <ul className="flex flex-col gap-1 text-sm">
          {items.map((item) => (
            <li data-eval={item.definition.slug} key={item.id}>
              {item.definition.name}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
