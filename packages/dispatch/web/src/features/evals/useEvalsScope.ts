import { useEffect, useMemo, useState } from "react";

import { compareRange, DAY_MS, liveWindow } from "../delivery/lib/window";
import type { EvalsUrlState } from "./evals-url";
import type { EvalsScope } from "./useEvalsViews";

/**
 * The present the tab reads its default compare range at: the moment it mounted, and again just
 * after each UTC midnight it stays open past. A preset window is subscribed by its days, so the
 * server moves it to the next day at midnight; this moves the range the tab compares it with to
 * the days before it on the same tick. The timer goes when the tab does.
 */
function useUtcDayClock(): number {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    // A millisecond past the midnight that ends the day `windowDays` reads `now` in: at midnight
    // itself a preset's days still end the day before. A timer that fires early still reads the
    // new day.
    const next = (Math.floor((now - 1) / DAY_MS) + 1) * DAY_MS + 1;
    const timer = setTimeout(
      () => setNow(Math.max(Date.now(), next)),
      Math.max(next - Date.now(), 0)
    );
    return () => clearTimeout(timer);
  }, [now]);
  return now;
}

/** What every evals view of the tab shares (plan D2, D3): the project, the header's window as the
 *  live socket names it, its compare range, read at the present `useUtcDayClock` keeps, and the
 *  repositories. It is the same object while none of those changes, so a render subscribes nothing
 *  again. */
export function useEvalsScope(project: string, state: EvalsUrlState): EvalsScope {
  const now = useUtcDayClock();
  return useMemo(
    () => ({
      project,
      window: liveWindow(state.window),
      compare: compareRange(state.compare, state.window, now),
      repos: state.repos,
    }),
    [project, state, now]
  );
}
