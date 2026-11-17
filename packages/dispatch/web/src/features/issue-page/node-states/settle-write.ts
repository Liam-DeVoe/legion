import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router-dom";

import { writeRequest } from "../../../api/client";
import { primarySpec } from "../../../api/issue-cache";
import { issueCoverageQuery, issueQuery } from "../../../api/queries";
import type { IssueDetails } from "../../../api/types";
import { issueDecisionsQuery } from "../../doc/spec-marks";
import { type IssueRoute, parseNodeReference } from "../../refs/routes";
import { useKeyedWrite } from "../../writes/useKeyedWrite";
import { issueAddress, pathToTarget, presetPath } from "../issue-address";
import { buildIssueTree, type TreeNode } from "../issue-tree";
import { CONTROL_LABELS } from "../page-words";
import { issueTreeQuery } from "../tree-read/tree-read";
import { overtakenRefs } from "./node-states";

/**
 * What the page writes and reads around a node's life (dispatch://INFRA-4063 R3, R4, R6): the
 * settle of a proposed node; the words filter's words, by which the outline opens each line of
 * overtaken nodes that holds a match; and what the tree read the page already holds says of the
 * overtaken nodes, for the header's count and for an address the Decisions view draws nowhere.
 */

/** `POST /api/v1/issues/{key}/nodes/{id}/settle`: a person settles the proposed node `reference`
 *  names, under the issue `key` (R4). `{id}` is the node's whole `dispatch://` address, which
 *  `routeUrl` percent-encodes as one path segment. It carries no body: a cookie proves a person,
 *  so the settle names no actor. */
export function settleRequest(key: string, reference: string) {
  return writeRequest("post", "/api/v1/issues/{key}/nodes/{id}/settle", { id: reference, key });
}

/** The issue a node's own `dispatch://` address names, which its settle goes under; undefined for
 *  an address that names none. The address says it with no context of the page's: Settle needs
 *  no writer, so it is drawn where no `OutlineWriterProvider` holds the issue's key. */
export function settleKeyOf(reference: string): string | undefined {
  const route = parseNodeReference(reference);
  return route !== undefined && "key" in route ? route.key : undefined;
}

/**
 * The settle of the proposed node `reference` names, under the issue `key` (R4): one keyed write
 * per press, which the act that makes it shows refused itself (`inline`), so the shell's
 * indicator names it only while it is out, and the settles of one node go in press order. A 5xx
 * is made again under its key, so the write stays out while it is tried again.
 *
 * A landing - a 200, or `null` for `409 KEY_COMMITTED` - tells `landed` first, so the act puts
 * the focus on its row before anything is read again, then reads the coverage again, and the
 * write stays out until it is answered. The coverage read is under `["issue", key]`, so its
 * invalidation is what T's `subscribeTreeFreshness` hears, which the page's `ViewReadProvider`
 * holds: it reads the tree again, drops the view reads held for the load and reads the open view
 * again. The tree is not invalidated here as well, since that would read it twice. A refusal
 * reads nothing again.
 */
export function useSettle(key: string, reference: string, landed: () => void) {
  const queryClient = useQueryClient();
  return useKeyedWrite({
    inline: true,
    label: () => CONTROL_LABELS.settle,
    onSuccess: () => {
      landed();
      return queryClient.invalidateQueries({
        exact: true,
        queryKey: issueCoverageQuery(key).queryKey,
      });
    },
    request: () => settleRequest(key, reference),
    target: () => `settle:${reference}`,
  });
}

/** The words the words filter holds, as the page's address carries them (`?q=`): the same value
 *  `issue-address.ts`'s `issueAddress` reads into `IssueAddress.filter` from the location's
 *  search, which `slots/body.tsx` narrows the outline's view by (`presets.ts`'s `presetView`,
 *  which trims them). `""` for an address that narrows nothing. */
export function useFilterWords(): string {
  const [search] = useSearchParams();
  return search.get("q") ?? "";
}

/** No reference at all: what the count leaves out until the tree read has answered. */
const NO_REFERENCES: ReadonlySet<string> = new Set();

/**
 * The outline's tree as the reads the page already holds build it (`buildIssueTree`): T's tree
 * read (`issueTreeQuery`) of the issue `key`, with `issue` and the decisions read, so each node
 * carries its state and its reference as the outline's does. Each read is watched by an observer
 * that is never enabled, so it sends no request and holds what the page read (R1). Undefined
 * until the issue and the tree read are held, and while the caller hands no `issue`, which
 * builds nothing.
 */
function useHeldTree(key: string, issue: IssueDetails | undefined): TreeNode | undefined {
  const tree = useQuery({ ...issueTreeQuery(key), enabled: false }).data;
  const decisions = useQuery(issueDecisionsQuery(key, false)).data;
  return useMemo(
    () =>
      issue === undefined || tree === undefined
        ? undefined
        : buildIssueTree({ decisions, issue, tree }),
    [decisions, issue, tree]
  );
}

/**
 * What the header's count of what waits on the reader leaves out (R3, `overtakenRefs`): the
 * reference of each node at or under an overtaken one, and the id of each such ask, in the tree
 * the page's held reads of the issue `key` build (`useHeldTree`), so it costs no request. Empty
 * until the issue and the tree read are held.
 */
export function useOvertakenRefs(key: string): ReadonlySet<string> {
  const issue = useQuery({ ...issueQuery(key), enabled: false }).data;
  const root = useHeldTree(key, issue);
  return useMemo(() => (root === undefined ? NO_REFERENCES : overtakenRefs(root)), [root]);
}

/**
 * The address of an overtaken node, or of a node under one, under the Decisions view, which draws
 * such a node nowhere (dispatch://INFRA-1196 decisions 115 and 118): it is replaced by the same
 * address under no filter, its search and its fragment kept, so the outline draws it, opens the
 * line that holds it and focuses it, and the stage, the filter row and the outline agree (R6).
 * The address's target is read as the outline reads it (`issueAddress`, `pathToTarget`), in the
 * tree the page's held reads build (`useHeldTree`), so it costs no request; that tree is built
 * only for an address under Decisions that names a node, and nothing is replaced until the tree
 * read has answered. Every other address, under every other view, stays as it is.
 */
export function useOvertakenTarget(issue: IssueDetails, route: IssueRoute): void {
  const { hash, search } = useLocation();
  const navigate = useNavigate();
  const address = issueAddress(route, search, hash, primarySpec(issue)?.slug);
  const targeted = address.preset === "decisions" && address.target !== undefined;
  const root = useHeldTree(issue.key, targeted ? issue : undefined);
  const path = root === undefined ? undefined : pathToTarget(root, address.target);
  const closed = path?.[path.length - 1]?.closed === true;
  useEffect(() => {
    if (!closed) return;
    navigate({ hash, pathname: presetPath(issue.key, "conversation"), search }, { replace: true });
  }, [closed, hash, issue.key, navigate, search]);
}
