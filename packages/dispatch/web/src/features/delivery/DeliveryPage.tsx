import { useQuery } from "@tanstack/react-query";
import {
  type CSSProperties,
  type ReactNode,
  type RefObject,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useSearchParams } from "react-router-dom";

import { isDeliveryNotConfigured } from "../../api/client";
import { deliveryMeasuresQuery, deliveryTimelineQuery } from "../../api/queries";
import type { DeliverySessionGroup } from "../../api/types";
import { Button, segmentedClassName } from "../../components/Button";
import { Select } from "../../components/Field";
import { PageHeader, SectionHeader } from "../../components/Layout";
import { MultiSelect } from "../../components/MultiSelect";
import { QueryError } from "../../components/QueryError";
import { useSettled } from "../../hooks/useSettled";
import { useRepeatableSearchParams, useSearchParamsUpdate } from "../../lib/url-array-params";
import {
  linkInline,
  switchOffBg,
  switchOnBg,
  switchThumbBg,
  textMutedOnCanvas,
  textSecondaryOnCanvas,
} from "../../theme/classes";
import { useMediaQuery } from "../shell/useDialog";
import { useDocumentTitle } from "../shell/useDocumentTitle";
import { DeliverySettingsForm } from "./DeliverySettingsForm";
import { DrillDown } from "./DrillDown";
import { FacetPanel } from "./FacetPanel";
import {
  buildColorScale,
  COLOR_COUNT_KEYS,
  COLOR_FACET_OPTIONS,
  COLOR_PALETTE,
  COLOR_PALETTE_LIGHT,
  type ColorFacet,
} from "./lib/colorScale";
import {
  type DeployedFacet,
  FACET_KEYS,
  FACET_PARAMS,
  type Filters,
  PLACEHOLDER_LABELS,
  type ReworkFacet,
} from "./lib/facets";
import { parentAgentLabel } from "./lib/parentAgent";
import {
  DEFAULT_WINDOW_PRESET,
  hasMalformedWindowParams,
  liveWindow,
  readWindow,
  type WindowChoice,
} from "./lib/window";
import { MeasuresPanel } from "./MeasuresPanel";
import { PRList } from "./PRList";
import { SourceFreshness } from "./SourceFreshness";
import { Timeline, type TimelineSelection } from "./Timeline";
import { WindowControl } from "./WindowControl";

/** How long the search waits after the last keystroke before the server is asked again. */
const SEARCH_DEBOUNCE_MS = 250;

const COLOR_FACETS = COLOR_FACET_OPTIONS.map((option) => option.value);

/** The colour-by facet the page uses when its URL names none, as the prototype does. */
const DEFAULT_COLOR_BY: ColorFacet = "repo";

/** No Parent agent value chosen: one array, so a render that chooses none hands the colour scale
 *  the same dependency. */
const NO_AGENTS: readonly string[] = [];

type Mode = "timeline" | "list";

/** How the filtered PRs are shown: dots on the timeline or a sortable list, the facet the merges
 *  are coloured by, whether the timeline splits them into one column per value, and, coloured by
 *  Parent agent, the values chosen to have their own colour and lane (`agents`). */
interface DeliveryView {
  colorBy: ColorFacet;
  lanes: boolean;
  mode: Mode;
  agents: string[];
}

/** The repeatable URL parameter that names the Parent agent values given their own colour and
 *  lane, kept apart from the facet filters, which narrow the population. */
const AGENTS_PARAM = ["agents"] as const;

/** A sub-window of the read window a brush drag set (`ws`/`we`): it narrows what the chart, the
 *  list and the count show without a new read, so the facets keep counting the whole window. */
export interface BrushWindow {
  start: string;
  end: string;
}

interface DeliveryUrlState {
  /** The window the URL names: a rolling preset, whose end Dispatch keeps at the present, or a
   *  custom range. The range a preset resolved to is the answer's `window`. */
  window: WindowChoice;
  filters: Filters;
  view: DeliveryView;
  brush: BrushWindow | null;
}

/** The delivery page's full state, read from and written to the URL — the same pattern
 *  `features/project/issue-filters.ts`'s `useIssueFilters` uses: repeatable facet params through
 *  the shared `useRepeatableSearchParams`, one `update` (`useSearchParamsUpdate`) funnelling every
 *  write through `setSearchParams(..., {replace: true})`. `window` and `filters` are memoized on the
 *  URL's own identity, so a render with an unchanged URL builds the same live view. */
function useDeliveryUrlState(): [DeliveryUrlState, (next: DeliveryUrlState) => void] {
  const [searchParams] = useSearchParams();
  const update = useSearchParamsUpdate();
  const malformedWindow = hasMalformedWindowParams(searchParams);
  const windowChoice = useMemo(() => readWindow(searchParams), [searchParams]);
  useEffect(() => {
    if (!malformedWindow) return;
    update((params) => {
      params.delete("from");
      params.delete("to");
      params.delete("window");
      params.delete("ws");
      params.delete("we");
    });
  }, [malformedWindow, update]);
  const search = searchParams.get("q") ?? "";
  const colorByParam = searchParams.get("colorBy");
  const colorBy = COLOR_FACETS.find((facet) => facet === colorByParam) ?? DEFAULT_COLOR_BY;
  const lanes = searchParams.get("lanes") === "1";
  const mode: Mode = searchParams.get("mode") === "list" ? "list" : "timeline";
  const { agents: agentParams } = useRepeatableSearchParams(AGENTS_PARAM);
  const agents = useMemo(() => [...new Set(agentParams)], [agentParams]);
  const view = useMemo(() => ({ colorBy, lanes, mode, agents }), [colorBy, lanes, mode, agents]);
  const ws = searchParams.get("ws");
  const we = searchParams.get("we");
  const brush = useMemo(
    () => (ws === null || we === null ? null : { start: ws, end: we }),
    [ws, we]
  );
  const arrayParams = useRepeatableSearchParams(FACET_KEYS);
  const filters: Filters = useMemo(
    () => ({
      ...arrayParams,
      rework: arrayParams.rework as ReworkFacet[],
      deployed: arrayParams.deployed as DeployedFacet[],
      search,
    }),
    [arrayParams, search]
  );

  const setState = useCallback(
    (next: DeliveryUrlState) => {
      update((params) => {
        if (next.window.kind === "custom") {
          params.set("from", next.window.from);
          params.set("to", next.window.to);
          params.delete("window");
        } else {
          params.delete("from");
          params.delete("to");
          if (next.window.preset === DEFAULT_WINDOW_PRESET) params.delete("window");
          else params.set("window", next.window.preset);
        }
        for (const key of FACET_KEYS) {
          params.delete(key);
          for (const value of next.filters[key]) params.append(key, value);
        }
        if (next.filters.search.trim() === "") params.delete("q");
        else params.set("q", next.filters.search);
        if (next.view.colorBy === DEFAULT_COLOR_BY) params.delete("colorBy");
        else params.set("colorBy", next.view.colorBy);
        if (next.view.lanes) params.set("lanes", "1");
        else params.delete("lanes");
        if (next.view.mode === "list") params.set("mode", "list");
        else params.delete("mode");
        params.delete("agents");
        for (const agent of next.view.agents) params.append("agents", agent);
        if (next.brush === null) {
          params.delete("ws");
          params.delete("we");
        } else {
          params.set("ws", next.brush.start);
          params.set("we", next.brush.end);
        }
      });
    },
    [update]
  );

  return [{ window: windowChoice, filters, view, brush }, setState];
}

/** The Timeline | List segmented toggle. */
function ModeToggle({ mode, onChange }: { mode: Mode; onChange: (mode: Mode) => void }): ReactNode {
  const item = (value: Mode, label: string) => (
    <Button aria-pressed={mode === value} onClick={() => onChange(value)} size="sm" variant="ghost">
      {label}
    </Button>
  );
  return (
    <fieldset aria-label="View" className={segmentedClassName}>
      {item("timeline", "Timeline")}
      {item("list", "List")}
    </fieldset>
  );
}

/** The Swimlanes switch. */
function LanesSwitch({
  checked,
  onChange,
}: {
  checked: boolean;
  onChange: (checked: boolean) => void;
}): ReactNode {
  return (
    <label className="flex cursor-pointer items-center gap-2">
      {/* The button is the touch target (44px below `xl`, by the app's floor); the track inside
          it keeps the switch's own size, so the floor never swells the track into a disc. */}
      <button
        aria-checked={checked}
        className="inline-flex shrink-0 items-center justify-center rounded-full"
        onClick={() => onChange(!checked)}
        role="switch"
        type="button"
      >
        <span
          className={`inline-flex h-5 w-9 items-center rounded-full transition-colors ${checked ? switchOnBg : switchOffBg}`}
        >
          <span
            className={`block h-4 w-4 rounded-full shadow-sm transition-transform ${switchThumbBg} ${checked ? "translate-x-4.5" : "translate-x-0.5"}`}
          />
        </span>
      </button>
      <span className={textSecondaryOnCanvas}>Swimlanes</span>
    </label>
  );
}

/** Coloured by Parent agent: which values get their own colour and lane, busiest first. The rest
 *  share one Other colour and lane; with none chosen, the colours and lanes are the facet's usual. */
function OwnColourPicker({
  counts,
  groups,
  selected,
  onChange,
}: {
  counts: Readonly<Record<string, number>>;
  groups: readonly DeliverySessionGroup[];
  selected: readonly string[];
  onChange: (next: string[]) => void;
}): ReactNode {
  const [open, setOpen] = useState(false);
  const options = Object.keys(counts)
    .filter((value) => !(value in PLACEHOLDER_LABELS))
    .sort((a, b) => (counts[b] ?? 0) - (counts[a] ?? 0));
  const state = selected.length === 0 ? "every value" : `${selected.length} chosen`;
  const countOf = (value: string) => counts[value] ?? 0;
  return (
    <MultiSelect
      emptyMessage="No matches."
      label="Own colour and lane"
      onChange={onChange}
      onOpenChange={setOpen}
      open={open}
      optionDetail={(value) => String(countOf(value))}
      optionDetailLabel={(value) => `${countOf(value)} PR${countOf(value) === 1 ? "" : "s"}`}
      optionLabel={(value) => parentAgentLabel(value, groups)}
      options={[...new Set([...selected, ...options])]}
      searchLabel="Search parent agents…"
      selected={selected}
      triggerAriaLabel={`Own colour and lane: ${state}`}
    >
      <span className="truncate">Own colour: {state}</span>
    </MultiSelect>
  );
}

/** `/delivery`: successful production deploys, pipeline failures, and merged PRs labelled by
 *  agent session, Dispatch issue, priority, and architectural component, as the prototype's
 *  timeline view shows them. Until the timeline's configuration is set, Dispatch refuses the
 *  page's live view with `DELIVERY_NOT_CONFIGURED` and the page shows the settings form in the
 *  timeline's place; once a save configures it, Dispatch answers the same subscription with the
 *  timeline. */
export function DeliveryPage(): ReactNode {
  useDocumentTitle("Delivery · Dispatch");
  const [state, setState] = useDeliveryUrlState();
  const [sectionRef, gutterStyle] = useMainGutter();
  const [selection, setSelection] = useState<TimelineSelection | null>(null);
  const search = useSettled(state.filters.search.trim(), SEARCH_DEBOUNCE_MS);

  // Every active facet and the search, as the query string the GET route would take without its
  // window: Dispatch applies them, so `query.data.prs` is already the filtered list.
  const liveQuery = useMemo(() => {
    const params = new URLSearchParams();
    if (search !== "") params.set("q", search);
    for (const key of FACET_KEYS) {
      for (const value of state.filters[key]) params.append(FACET_PARAMS[key], value);
    }
    return params.toString();
  }, [state.filters, search]);
  const query = useQuery(
    deliveryTimelineQuery({ kind: "timeline", window: liveWindow(state.window), query: liveQuery })
  );
  const data = query.data;
  // The measures follow a brush drag as the prototype's panel does (its App.tsx: computeDora over
  // the brush window), so they read the brush where the timeline keeps reading the whole window.
  // With no brush the timeline's own answer carries them (`measures`), so the page subscribes its
  // population once; only a brush subscribes a measures view of its own, for its window.
  const brushed = useQuery({
    ...deliveryMeasuresQuery({
      kind: "measures",
      window:
        state.brush === null
          ? liveWindow(state.window)
          : { from: state.brush.start, to: state.brush.end },
      query: liveQuery,
    }),
    enabled: state.brush !== null,
  });
  const measures = state.brush === null ? data?.measures : brushed.data;
  // When the answer on screen last changed (its snapshot or its latest delta). While a facet change
  // waits for its snapshot, the previous answer stays on screen (`keepPreviousData`) and the new
  // query reports `dataUpdatedAt` 0, so this keeps the last time an answer arrived instead of
  // reading that as the epoch.
  const answeredAtRef = useRef(0);
  if (query.dataUpdatedAt > 0) answeredAtRef.current = query.dataUpdatedAt;
  const answeredAt = answeredAtRef.current;

  const activeWindow = useMemo(
    () =>
      state.brush ?? (data === undefined ? null : { start: data.window.from, end: data.window.to }),
    [state.brush, data]
  );
  const shownPRs = useMemo(() => {
    const prs = data?.prs ?? [];
    if (state.brush === null) return prs;
    const startMs = Date.parse(state.brush.start);
    const endMs = Date.parse(state.brush.end);
    return prs.filter((pr) => {
      if (pr.merged_at === null) return false;
      const mergedMs = Date.parse(pr.merged_at);
      return mergedMs >= startMs && mergedMs <= endMs;
    });
  }, [data, state.brush]);
  // The waiting line's population, whatever the brush: the pull requests still waiting at the
  // read window's start, and every tracked merge of the read window. The brush narrows the line's
  // time range, never what it counts, so a merge before the brush that has not shipped holds the
  // line up across it.
  const waitingMerges = useMemo(
    () => [
      ...(data?.waiting ?? []),
      ...(data?.prs ?? []).filter((pr) => pr.deployed_status !== "not_tracked"),
    ],
    [data]
  );
  // The merge colours are stepped for the surface they are drawn on.
  const dark = useMediaQuery("(prefers-color-scheme: dark)");
  // Coloured by Parent agent, the chosen values take the palette and the rest share Other.
  const chosenAgents = state.view.colorBy === "parentAgent" ? state.view.agents : NO_AGENTS;
  const colorScale = useMemo(
    () =>
      buildColorScale(
        data?.color_counts?.[COLOR_COUNT_KEYS[state.view.colorBy]] ?? {},
        chosenAgents,
        dark ? COLOR_PALETTE : COLOR_PALETTE_LIGHT
      ),
    [data, state.view.colorBy, chosenAgents, dark]
  );
  const allRepos = useMemo(() => Object.keys(data?.color_counts?.repo ?? {}), [data]);

  const drillDownSelection = useMemo((): TimelineSelection | null => {
    if (selection === null) return null;
    if (selection.kind !== "pr") return selection;
    return (data?.prs ?? []).some((pr) => pr.id === selection.id) ? selection : null;
  }, [selection, data]);

  const setView = (view: DeliveryView) => setState({ ...state, view });

  // Whether the page shows the setup form: the last settled timeline read's answer, held while a
  // read is in flight. A refetch with nothing cached clears `query.error` the moment it starts
  // (the window regaining focus or a live-socket gap refresh), so a body read live from the query
  // would unmount the form, and the draft typed into it, on every tab switch. A read that settles
  // on anything but `DELIVERY_NOT_CONFIGURED` shows that instead: the timeline a save brings, or a
  // failure with Retry. Set during render (React's pattern for state derived from props), so the
  // render that settles already shows the new body.
  const [notConfigured, setNotConfigured] = useState(false);
  if (query.fetchStatus === "idle" && isDeliveryNotConfigured(query.error) !== notConfigured) {
    setNotConfigured(!notConfigured);
  }
  return (
    // From `xl` the page is the window's height and the chart takes what its header leaves, never
    // less than its own `min-h-[28rem]`. Where the window is too short for both - 1280x800 and
    // 1280x720 - the page scrolls inside this box, rather than spill past the shell, which made
    // the document scroll on past it and took the shell's Navigation off the top of the screen.
    // The height is the window's less `<main>`'s own top and bottom gutter, read from `<main>`
    // (`useMainGutter`) so the page follows the shell's padding instead of repeating it.
    <section
      className="flex flex-col xl:h-[calc(100dvh-var(--main-gutter,3rem))] xl:overflow-y-auto"
      ref={sectionRef}
      style={gutterStyle}
    >
      <PageHeader
        actions={
          notConfigured ? undefined : (
            <WindowControl
              key={
                state.window.kind === "custom"
                  ? `${state.window.from}|${state.window.to}`
                  : state.window.preset
              }
              onChange={(window) => setState({ ...state, window, brush: null })}
              value={state.window}
            />
          )
        }
        description={
          // When this answer was read and the window it covers stay inside the page's `header`,
          // where the visual review masks them (e2e/visual.e2e.ts): they differ from run to run.
          notConfigured || data === undefined ? undefined : (
            <span className="text-xs">
              Generated {new Date(answeredAt).toLocaleString()} · window{" "}
              {new Date(data.window.from).toLocaleDateString()}–
              {new Date(data.window.to).toLocaleDateString()}
              {state.brush === null ? null : (
                <button
                  className={`ml-3 ${linkInline}`}
                  onClick={() => setState({ ...state, brush: null })}
                  type="button"
                >
                  clear brush window
                </button>
              )}
            </span>
          )
        }
        title="Delivery timeline"
      />
      <div className="flex min-h-0 flex-1 flex-col gap-4">
        {notConfigured ? (
          <section aria-labelledby="delivery-setup-heading">
            <SectionHeader
              description="Once set, this page shows the merged pull requests of the authors you name, the production deploys that shipped them, and the pipeline failures in between."
              id="delivery-setup-heading"
              title="Set up the delivery timeline"
            />
            <DeliverySettingsForm initial={null} />
          </section>
        ) : (
          <>
            {data === undefined ? null : (
              // The sources, on one quiet line under the title.
              <div className="-mt-4">
                <SourceFreshness freshness={data.freshness} readAtMs={answeredAt} />
              </div>
            )}

            {query.isPending ? (
              <p className={textMutedOnCanvas}>Loading delivery timeline…</p>
            ) : null}
            {query.isError ? (
              <QueryError
                message="Couldn't load the delivery timeline."
                onRetry={() => void query.refetch()}
              />
            ) : null}

            {data === undefined ? null : state.brush !== null && brushed.isError ? (
              <QueryError
                message="Couldn't load the delivery measures."
                onRetry={() => void brushed.refetch()}
              />
            ) : (
              <MeasuresPanel data={measures} />
            )}

            {data === undefined || activeWindow === null ? null : (
              <>
                <div className="mt-2 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm">
                  <ModeToggle
                    mode={state.view.mode}
                    onChange={(mode) => setView({ ...state.view, mode })}
                  />
                  <span className={`flex items-center gap-2 ${textSecondaryOnCanvas}`}>
                    <label htmlFor="delivery-color-by">Color merges by</label>
                    <Select
                      className="w-40"
                      id="delivery-color-by"
                      onChange={(event) => {
                        const colorBy = COLOR_FACETS.find((facet) => facet === event.target.value);
                        if (colorBy !== undefined) setView({ ...state.view, colorBy });
                      }}
                      value={state.view.colorBy}
                    >
                      {COLOR_FACET_OPTIONS.map((option) => (
                        <option key={option.value} value={option.value}>
                          {option.label}
                        </option>
                      ))}
                    </Select>
                  </span>
                  {state.view.colorBy === "parentAgent" ? (
                    <OwnColourPicker
                      counts={data.color_counts?.parent_agent ?? {}}
                      groups={data.session_groups ?? []}
                      onChange={(agents) => setView({ ...state.view, agents })}
                      selected={state.view.agents}
                    />
                  ) : null}
                  {state.view.mode === "timeline" ? (
                    <LanesSwitch
                      checked={state.view.lanes}
                      onChange={(lanes) => setView({ ...state.view, lanes })}
                    />
                  ) : null}
                  <span className={`text-xs xl:ml-auto ${textMutedOnCanvas}`}>
                    {shownPRs.length} PRs in current filter/window
                  </span>
                </div>

                <div className="flex min-h-0 flex-1 flex-col gap-4 xl:flex-row">
                  <div className="shrink-0 xl:w-60 xl:overflow-y-auto">
                    <FacetPanel
                      data={data}
                      filters={state.filters}
                      onChange={(filters) => setState({ ...state, filters })}
                    />
                  </div>
                  <div className="flex h-[75dvh] min-h-[28rem] min-w-0 flex-col xl:h-auto xl:flex-1">
                    {state.view.mode === "timeline" ? (
                      <Timeline
                        chosen={chosenAgents}
                        colorBy={state.view.colorBy}
                        colorScale={colorScale}
                        components={data.components ?? {}}
                        filters={state.filters}
                        lanes={state.view.lanes}
                        onBrush={(brush) => setState({ ...state, brush })}
                        onSelect={setSelection}
                        prs={shownPRs}
                        runs={data.runs ?? []}
                        sessionGroups={data.session_groups ?? []}
                        waiting={waitingMerges}
                        window={activeWindow}
                      />
                    ) : (
                      <PRList
                        allRepos={allRepos}
                        colorBy={state.view.colorBy}
                        colorScale={colorScale}
                        onSelect={(id) => setSelection({ kind: "pr", id })}
                        onSelectRun={(id) => setSelection({ kind: "deploy", id })}
                        prs={shownPRs}
                        selectedId={selection?.kind === "pr" ? selection.id : undefined}
                        sessionGroups={data.session_groups ?? []}
                      />
                    )}
                  </div>
                  <DrillDown
                    components={data.components ?? {}}
                    onClose={() => setSelection(null)}
                    prs={data.prs ?? []}
                    runs={data.runs ?? []}
                    selection={drillDownSelection}
                    sessionGroups={data.session_groups ?? []}
                    sessionTitles={data.session_titles ?? {}}
                  />
                </div>
              </>
            )}
          </>
        )}
      </div>
    </section>
  );
}

/** `<main>`'s own top and bottom padding as a `--main-gutter` length on the page, kept current
 *  as the window crosses a breakpoint that changes it. Until it is read the page's CSS falls
 *  back to the desktop gutter. */
function useMainGutter(): [RefObject<HTMLElement | null>, CSSProperties | undefined] {
  const ref = useRef<HTMLElement | null>(null);
  const [gutter, setGutter] = useState<number | undefined>(undefined);
  useLayoutEffect(() => {
    const main = ref.current?.closest("main");
    if (main === null || main === undefined) return;
    const read = () => {
      const style = getComputedStyle(main);
      const total = Number.parseFloat(style.paddingTop) + Number.parseFloat(style.paddingBottom);
      if (Number.isFinite(total)) setGutter(total);
    };
    read();
    window.addEventListener("resize", read);
    return () => window.removeEventListener("resize", read);
  }, []);
  return [
    ref,
    gutter === undefined ? undefined : ({ "--main-gutter": `${gutter}px` } as CSSProperties),
  ];
}
