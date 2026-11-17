import { useQueries, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useState } from "react";

import type { IssueDetails } from "../../../api/types";
import { QueryError } from "../../../components/QueryError";
import { ANSWER_LINES, type Preset } from "../page-words";
import { dropViews, subscribeTreeFreshness, TREE_VIEWS, useTreeRead, viewQuery } from "./tree-read";

/**
 * The filters' view reads of one load of the issue, around the stage. Each filter's view is read
 * the first time its filter is on in a load, and then held for the rest of it, never stale by age
 * and kept by this provider's observers, which stay mounted for the load, so a filter turned off
 * and on again finds its answer held and sends nothing; once the stage goes, they are collected
 * as any query nothing observes is. `useIssueTree` watches the four reads without ever sending
 * one, and sets what they hold on the tree's root (`TreeNode.views`), which is what each filter
 * draws from.
 *
 * The page mounts the stage once for each issue it opens (`IssueOutlinePage`'s `key`), so a mount
 * here is a load: it drops whatever view a past load of the issue held, so no load draws a view
 * older than its own tree. It holds the subscription that keeps the tree and its views fresh
 * (`subscribeTreeFreshness`): a write marks every held view stale, so the open one is read again
 * and draws what it held until its new answer lands, and another is read again when its filter
 * is next on.
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
    dropViews(queryClient, key);
    setDropped(true);
  }, [key, queryClient]);
  useEffect(() => subscribeTreeFreshness(queryClient, key), [key, queryClient]);
  useQueries({
    queries: TREE_VIEWS.map((view) => ({
      ...viewQuery(key, view),
      enabled: dropped && preset === view,
      staleTime: Number.POSITIVE_INFINITY,
    })),
  });
  return children;
}

/**
 * The outline's line for a tree read refused before it first answered, its retries spent: until
 * the read holds an answer the tree is the issue alone and the outline draws no row, so the line
 * says the read failed, with a Retry that reads the tree again, as each view's line says of a
 * read of its own. Nothing while the read is out, and nothing once it holds an answer, which a
 * failed refetch keeps.
 */
export function TreeReadFailed({ issue }: { issue: IssueDetails }): ReactNode {
  const { failed, refetch } = useTreeRead(issue);
  return failed ? (
    <div data-tree-unreadable="">
      <QueryError message={ANSWER_LINES.outline_unreadable} onRetry={() => void refetch()} />
    </div>
  ) : null;
}
