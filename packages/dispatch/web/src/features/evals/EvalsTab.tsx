import { type ReactNode, useId } from "react";

import { textPrimaryOnCanvas } from "../../theme/classes";

/** The tab's sections, in the order dispatch://INFRA-2918/spec R2 sets: the target cards, the
 *  Legion measures, CI health, deploys and lead time, the merge timeline, and then every eval the
 *  project's agents and jobs log. */
export const EVALS_SECTIONS = [
  { id: "cards", title: "Target cards" },
  { id: "legion", title: "Legion measures" },
  { id: "ci", title: "CI health" },
  { id: "deploys", title: "Deploys and lead time" },
  { id: "timeline", title: "Merge timeline" },
  { id: "logged", title: "Logged" },
] as const;

/** One section of the tab, named by its heading. */
function EvalsSectionSlot({ id, title }: { id: string; title: string }): ReactNode {
  const headingId = useId();
  return (
    <section aria-labelledby={headingId} className="flex flex-col gap-2" data-section={id}>
      <h2 className={`text-[15px] font-semibold ${textPrimaryOnCanvas}`} id={headingId}>
        {title}
      </h2>
    </section>
  );
}

/** A project's Evals tab (`/projects/<KEY>/evals`, spec R1): its one page of numbers, every
 *  project's alike. */
export function EvalsTab({ project }: { project: string }): ReactNode {
  return (
    <div className="flex flex-col gap-6" data-project={project}>
      {EVALS_SECTIONS.map((section) => (
        <EvalsSectionSlot id={section.id} key={section.id} title={section.title} />
      ))}
    </div>
  );
}
