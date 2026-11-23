import { useQueries, useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";

import { type EvalKind, evalsViewQuery, legionDaemonQuery } from "../../api/queries";
import type { EvalListResponse } from "../../api/types";
import { CiHealth } from "./CiHealth";
import { DeploysSection } from "./DeploysSection";
import { EvalPanelHost } from "./EvalPanel";
import { EvalsHeader } from "./EvalsHeader";
import { EvalsSection } from "./EvalsSection";
import { keepLogged, LoggedEvals, UnnamedEvals } from "./EveryEval";
import { type EvalsUrlState, useEvalsUrlState } from "./evals-url";
import { LegionSection } from "./LegionSection";
import { TargetCards } from "./TargetCards";
import { TimelineSection } from "./TimelineSection";
import { brushDays, useCardsScope, useEvalsScope } from "./useEvalsScope";
import type { EvalsScope, EvalsViewSelect, EvalsViewState } from "./useEvalsViews";

/** The views each section reads, by kind: the target cards read the delivery and the Legion evals
 *  (plan D3), the Deploys and lead time section the same delivery view as the cards, the merge
 *  timeline none, since it reads a view of its own once it is opened (spec R13), and the Logged
 *  section every view the tab reads, since a logged eval may be of any kind (plan D7). */
const CARDS: readonly EvalKind[] = ["delivery", "legion"];
const CARDS_WITHOUT_LEGION: readonly EvalKind[] = ["delivery"];
const LEGION: readonly EvalKind[] = ["legion"];
const CI: readonly EvalKind[] = ["ci"];
const DEPLOYS: readonly EvalKind[] = ["delivery"];
const TIMELINE: readonly EvalKind[] = [];
const EVERY_KIND: readonly EvalKind[] = ["delivery", "legion", "ci", "experiment"];
const EVERY_KIND_BUT_LEGION: readonly EvalKind[] = ["delivery", "ci", "experiment"];

/** One section of the tab: what it is, its heading, the kinds of eval it reads, and what it keeps
 *  of each page of their views (all of it where absent). */
interface Section {
  readonly id: "cards" | "legion" | "ci" | "deploys" | "timeline" | "logged";
  readonly title: string;
  readonly kinds: readonly EvalKind[];
  readonly select?: EvalsViewSelect;
}

/** The tab's sections, in the order dispatch://INFRA-2918/spec R2 sets: the target cards, the
 *  Legion measures, CI health, deploys and lead time, the merge timeline, and then every eval the
 *  project's agents and jobs log, of every view the tab reads. The Legion section, the Legion
 *  cards and the Legion view show only where the project has a Legion daemon (spec R5). */
function sectionsOf(legion: boolean): Section[] {
  return [
    { id: "cards", title: "Target cards", kinds: legion ? CARDS : CARDS_WITHOUT_LEGION },
    ...(legion ? [{ id: "legion", title: "Legion measures", kinds: LEGION } as const] : []),
    { id: "ci", title: "CI health", kinds: CI },
    { id: "deploys", title: "Deploys and lead time", kinds: DEPLOYS },
    { id: "timeline", title: "Merge timeline", kinds: TIMELINE },
    {
      id: "logged",
      title: "Logged",
      kinds: legion ? EVERY_KIND : EVERY_KIND_BUT_LEGION,
      select: keepLogged,
    },
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

/** What a section draws of its views: its own charts and lists, then, for a kind's section, the
 *  computed evals of that kind it does not draw by name; the Logged section, every logged eval.
 *  The target cards name the brushed days they read, while the merge timeline's brush is set
 *  (`days`). */
function drawSection(
  id: Exclude<Section["id"], "timeline">,
  project: string,
  scope: EvalsScope,
  delivery: boolean,
  days: { from: string; to: string } | null
): (views: readonly EvalsViewState[]) => ReactNode {
  switch (id) {
    case "cards":
      return (views) => (
        <TargetCards days={days} delivery={delivery} project={project} views={views} />
      );
    case "legion":
      return (views) => (
        <>
          <LegionSection project={project} repos={scope.repos} views={views} />
          <UnnamedEvals kind="legion" scope={scope} views={views} />
        </>
      );
    case "ci":
      return (views) => (
        <>
          <CiHealth scope={scope} views={views} />
          <UnnamedEvals kind="ci" scope={scope} views={views} />
        </>
      );
    case "deploys":
      return (views) => (
        <>
          <DeploysSection project={project} views={views} />
          <UnnamedEvals kind="delivery" scope={scope} views={views} />
        </>
      );
    case "logged":
      return (views) => <LoggedEvals scope={scope} views={views} />;
  }
}

/**
 * A project's Evals tab (`/projects/<KEY>/evals`, dispatch://INFRA-2918/spec R1, R2, R3, R5): its
 * one page of numbers, every project's alike, under one header. It is a subscriber: it opens one
 * evals view per kind it shows, all at once, and each section paints from its own as it lands,
 * never waiting for another's (plan D3). In parallel it reads the project's Legion daemon, which
 * decides whether the Legion section and cards show; until that read answers they show, so their
 * view is opened with the others rather than after it. The merge timeline opens no view until the
 * reader opens it (spec R13); its brush sets the target cards' days and its Repository facet the
 * repository every number reads (decision 3). A card's or a chart's title opens the number's
 * panel (plan D12).
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
  const cardsScope = useCardsScope(scope, state);
  const days = useMemo(() => brushDays(state.brush), [state.brush]);
  const sections = sectionsOf(legion);
  const kinds = [...new Set(sections.flatMap((section) => section.kinds))];
  return (
    <div className="flex flex-col gap-6">
      <LiveHeader kinds={kinds} onChange={setState} scope={scope} state={state} />
      <EvalPanelHost kinds={kinds} scope={scope}>
        {sections.map((section) =>
          section.id === "timeline" ? (
            <TimelineSection
              brush={state.brush}
              key={section.id}
              onBrush={(brush) => setState({ ...state, brush })}
              project={project}
              scope={scope}
            />
          ) : (
            <EvalsSection
              id={section.id}
              key={section.id}
              kinds={section.kinds}
              scope={section.id === "cards" ? cardsScope : scope}
              select={section.select}
              title={section.title}
            >
              {drawSection(section.id, project, scope, delivery, days)}
            </EvalsSection>
          )
        )}
      </EvalPanelHost>
    </div>
  );
}
