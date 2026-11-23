// The Evals tab's header state, read from and written to the URL, so a shared link opens the same
// window, compare range and repositories (dispatch://INFRA-2918/artifact/plan-md, D2). The window
// is the Delivery page's (`window`, or `from` and `to`), so an old `/delivery` link keeps it
// through the redirect; the compare range is `compare=off` or `compare_from` and `compare_to`, and
// names nothing for the default, the days before the window; each repository is a `repo`.
import { useCallback, useEffect, useMemo } from "react";
import { useSearchParams } from "react-router-dom";

import { useRepeatableSearchParams, useSearchParamsUpdate } from "../../lib/url-array-params";
import {
  type CompareChoice,
  DEFAULT_WINDOW_PRESET,
  hasMalformedWindowParams,
  readCompare,
  readWindow,
  type WindowChoice,
} from "../delivery/lib/window";

export interface EvalsUrlState {
  readonly window: WindowChoice;
  readonly compare: CompareChoice;
  /** The repositories whose day rows every number folds in place of the project's own; none,
   *  the project's. */
  readonly repos: readonly string[];
}

const REPO_PARAM = ["repo"] as const;

/** The header's state and its one writer, which replaces the URL's entry rather than pushing one.
 *  Each part is memoized on the URL's own identity, so a render with an unchanged URL hands the
 *  tab's views the same state and subscribes nothing again. A window or a compare range the URL
 *  names malformed is dropped from it, as the Delivery page dropped a malformed window. */
export function useEvalsUrlState(): [EvalsUrlState, (next: EvalsUrlState) => void] {
  const [searchParams] = useSearchParams();
  const update = useSearchParamsUpdate();
  const malformedWindow = hasMalformedWindowParams(searchParams);
  const compare = useMemo(() => readCompare(searchParams), [searchParams]);
  const malformedCompare =
    (searchParams.has("compare_from") || searchParams.has("compare_to")) &&
    compare.kind !== "custom";
  useEffect(() => {
    if (!malformedWindow && !malformedCompare) return;
    update((params) => {
      if (malformedWindow) {
        params.delete("from");
        params.delete("to");
        params.delete("window");
      }
      if (malformedCompare) {
        params.delete("compare_from");
        params.delete("compare_to");
      }
    });
  }, [malformedWindow, malformedCompare, update]);
  const windowChoice = useMemo(() => readWindow(searchParams), [searchParams]);
  const { repo } = useRepeatableSearchParams(REPO_PARAM);
  const repos = useMemo(() => [...new Set(repo.filter((name) => name !== ""))], [repo]);
  const state = useMemo(
    () => ({ window: windowChoice, compare, repos }),
    [windowChoice, compare, repos]
  );

  const setState = useCallback(
    (next: EvalsUrlState) => {
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
        params.delete("compare");
        params.delete("compare_from");
        params.delete("compare_to");
        if (next.compare.kind === "off") params.set("compare", "off");
        if (next.compare.kind === "custom") {
          params.set("compare_from", next.compare.from);
          params.set("compare_to", next.compare.to);
        }
        params.delete("repo");
        for (const name of next.repos) params.append("repo", name);
      });
    },
    [update]
  );
  return [state, setState];
}
