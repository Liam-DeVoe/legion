import { useQueries, useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { type EvalKind, evalsViewQuery, legionDaemonQuery } from "../../api/queries";
import type { EvalListResponse } from "../../api/types";
import { CiHealth } from "./CiHealth";
import { DeploysSection } from "./DeploysSection";
import { EvalsHeader } from "./EvalsHeader";
import { EvalsSection } from "./EvalsSection";
import { type EvalsUrlState, useEvalsUrlState } from "./evals-url";
import { LegionSection } from "./LegionSection";
import { TargetCards } from "./TargetCards";
import { useEvalsScope } from "./useEvalsScope";
import type { EvalsScope, EvalsViewState } from "./useEvalsViews";

/** The views each section reads, by kind: the target cards read the delivery and the Legion evals
 *  (plan D3), the Deploys and lead time section the same delivery view as the cards, and the
 *  merge timeline none, since it is closed on the first paint (spec R13). */
const CARDS: readonly EvalKind[] = ["delivery", "legion"];
const CARDS_WITHOUT_LEGION: readonly EvalKind[] = ["delivery"];
const LEGION: readonly EvalKind[] = ["legion"];
const CI: readonly EvalKind[] = ["ci"];
const DEPLOYS: readonly EvalKind[] = ["delivery"];
const TIMELINE: readonly EvalKind[] = [];
const LOGGED: readonly EvalKind[] = ["experiment"];

/** The tab's sections, in the order dispatch://INFRA-2918/spec R2 sets: the target cards, the
 *  Legion measures, CI health, deploys and lead time, the merge timeline, and then every eval the
 *  project's agents and jobs log. The Legion section and the Legion cards show only where the
 *  project has a Legion daemon (spec R5). */
function sectionsOf(legion: boolean) {
  return [
    { id: "cards", title: "Target cards", kinds: legion ? CARDS : CARDS_WITHOUT_LEGION },
    ...(legion ? [{ id: "legion", title: "Legion measures", kinds: LEGION }] : []),
    { id: "ci", title: "CI health", kinds: CI },
    { id: "deploys", title: "Deploys and lead time", kinds: DEPLOYS },
    { id: "timeline", title: "Merge timeline", kinds: TIMELINE },
    { id: "logged", title: "Logged", kinds: LOGGED },
  ];
}

/** The window and compare range of an answer, which are the same in every view of one scope. */
function daysOf(answer: EvalListResponse): Pick<EvalListResponse, "window" | "compare"> {
  return { window: answer.window, compare: answer.compare };
}

/** The header, naming the days of the first answer any of the tab's views gives. It reads only
 *  each view's window and compare range, so a delta that moves neither renders no header. */
function LiveHeader({
  kinds,
  onChange,
  scope,
  state,
}: {
  kinds: readonly EvalKind[];
  onChange: (next: EvalsUrlState) => void;
  scope: EvalsScope;
  state: EvalsUrlState;
}): ReactNode {
  const days = useQueries({
    queries: kinds.map((kind) => ({
      ...evalsViewQuery({ ...scope, kind, offset: 0 }),
      select: daysOf,
    })),
  });
  const first = days.find((view) => view.data !== undefined)?.data;
  return (
    <EvalsHeader
      compare={first?.compare}
      onChange={onChange}
      state={state}
      window={first?.window}
    />
  );
}

/** What a section draws of its views: its own charts and lists, or, for a section that has none
 *  of its own, `EvalsSection`'s list of its evals. */
function drawSection(
  id: string,
  project: string,
  scope: EvalsScope,
  delivery: boolean
): ((views: readonly EvalsViewState[]) => ReactNode) | undefined {
  switch (id) {
    case "cards":
      return (views) => <TargetCards delivery={delivery} project={project} views={views} />;
    case "legion":
      return (views) => <LegionSection project={project} repos={scope.repos} views={views} />;
    case "ci":
      return (views) => <CiHealth scope={scope} views={views} />;
    case "deploys":
      return (views) => <DeploysSection project={project} views={views} />;
    default:
      return undefined;
  }
}

/**
 * A project's Evals tab (`/projects/<KEY>/evals`, dispatch://INFRA-2918/spec R1, R2, R3, R5): its
 * one page of numbers, every project's alike, under one header. It is a subscriber: it opens one
 * evals view per kind it shows, all at once, and each section paints from its own as it lands,
 * never waiting for another's (plan D3). In parallel it reads the project's Legion daemon, which
 * decides whether the Legion section and cards show; until that read answers they show, so their
 * view is opened with the others rather than after it.
 *
 * The compare range a preset's default names moves at each UTC midnight the tab stays open past,
 * with the window the server moves (`useEvalsScope`).
 */
export function EvalsTab({ project }: { project: string }): ReactNode {
  const [state, setState] = useEvalsUrlState();
  const daemon = useQuery(legionDaemonQuery(project));
  const legion = daemon.data !== null;
  // Whether the project's delivery is on, which draws the delivery cards: every project's until
  // the tab reads the project's delivery settings (plan T24).
  const delivery = true;
  const scope = useEvalsScope(project, state);
  const sections = sectionsOf(legion);
  const kinds = [...new Set(sections.flatMap((section) => section.kinds))];
  return (
    <div className="flex flex-col gap-6">
      <LiveHeader kinds={kinds} onChange={setState} scope={scope} state={state} />
      {sections.map((section) => (
        <EvalsSection
          id={section.id}
          key={section.id}
          kinds={section.kinds}
          scope={scope}
          title={section.title}
        >
          {drawSection(section.id, project, scope, delivery)}
        </EvalsSection>
      ))}
    </div>
  );
}
