import { useQueries, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useState } from "react";

import type { IssueDetails } from "../../../api/types";
import type { Preset } from "../page-words";
import { subscribeTreeFreshness, type TreeView, viewKey, viewQuery } from "./tree-read";

/** The views of the tree the filters read, one for each filter: Spec's, Verification's and
 *  Plan's role documents, and the issue's decisions wherever they are written
 *  (dispatch://INFRA-4097's plan, choice 6). */
const VIEW_FILTERS: readonly TreeView[] = ["spec", "verification", "plan", "decisions"];

/**
 * The filters' view reads of one load of the issue (dispatch://INFRA-4062 R4, R6), around the
 * stage. Each filter's view is read the first time its filter is on in a load, and then held for
 * the rest of it, never stale and kept by this provider's observers, which stay mounted for the
 * load, so a filter turned off and on again finds its answer held and sends nothing; once the
 * stage goes, they are collected as any query nothing observes is. `useIssueTree` watches the
 * four reads without ever sending one, and sets what they hold on the tree's root
 * (`TreeNode.views`), which is what each filter draws from.
 *
 * The page mounts the stage once for each issue it opens (`IssueOutlinePage`'s `key`), so a mount
 * here is a load: it drops whatever view a past load of the issue held, so no load draws a view
 * older than its own tree. It holds the page's one subscription that keeps the tree and its views
 * fresh (`subscribeTreeFreshness`), whose reset of the views reads again only the one that is on.
 */
export function ViewReadProvider({
  children,
  issue,
  preset,
}: {
  children: ReactNode;
  issue: IssueDetails;
  preset: Preset;
}): ReactNode {
  const queryClient = useQueryClient();
  const key = issue.key;
  // Whether the views of a past load are dropped. No view is read before then: a view read as the
  // stage mounts would be dropped by that reset and read a second time.
  const [dropped, setDropped] = useState(false);
  useEffect(() => {
    for (const filter of VIEW_FILTERS) {
      void queryClient.resetQueries({ exact: true, queryKey: viewKey(key, filter) });
    }
    setDropped(true);
  }, [key, queryClient]);
  useEffect(() => subscribeTreeFreshness(queryClient, key), [key, queryClient]);
  useQueries({
    queries: VIEW_FILTERS.map((filter) => ({
      ...viewQuery(key, filter),
      enabled: dropped && preset === filter,
      staleTime: Number.POSITIVE_INFINITY,
    })),
  });
  return children;
}
