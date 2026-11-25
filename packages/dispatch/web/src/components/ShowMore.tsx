import type { ReactNode } from "react";

import { Button } from "./Button";

/** The button at the foot of an offset-paged list: it reads the next page and says so while that
 *  read is in flight. A list with no next page renders none, so a caller hands it the infinite
 *  query's own two flags and its fetcher and writes no condition of its own. */
export function ShowMore({
  fetching,
  hasMore,
  onFetch,
}: {
  fetching: boolean;
  hasMore: boolean;
  onFetch: () => void;
}): ReactNode {
  if (!hasMore) {
    return null;
  }
  return (
    <Button className="mt-4" disabled={fetching} onClick={onFetch}>
      {fetching ? "Loading…" : "Show more"}
    </Button>
  );
}
