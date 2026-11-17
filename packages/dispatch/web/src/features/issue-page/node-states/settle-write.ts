import { useQueryClient } from "@tanstack/react-query";
import { useSearchParams } from "react-router-dom";

import { writeRequest } from "../../../api/client";
import { issueCoverageQuery } from "../../../api/queries";
import { parseNodeReference } from "../../refs/routes";
import { useKeyedWrite } from "../../writes/useKeyedWrite";
import { CONTROL_LABELS } from "../page-words";
import { issueTreeKey } from "../tree-read/tree-read";

/**
 * What the page writes and reads around a node's life (dispatch://INFRA-4063 R4, R6): the settle
 * of a proposed node, and the words filter's words, by which the outline opens each line of
 * overtaken nodes that holds a match.
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
 * the focus on its row before anything is read again, then reads the coverage and the tree again,
 * and the write stays out until both are answered. The coverage read is under `["issue", key]`,
 * so its invalidation is what T's `subscribeTreeFreshness` hears: it reads the tree again, drops
 * the view reads held for the load and reads the open view again. The tree read's own key is no
 * query of the issue, so invalidating it alone reads no view; it is invalidated here for a page
 * that reads the tree with no subscription. A refusal reads nothing again.
 */
export function useSettle(key: string, reference: string, landed: () => void) {
  const queryClient = useQueryClient();
  return useKeyedWrite({
    inline: true,
    label: () => CONTROL_LABELS.settle,
    onSuccess: () => {
      landed();
      return Promise.all([
        queryClient.invalidateQueries({ exact: true, queryKey: issueCoverageQuery(key).queryKey }),
        queryClient.invalidateQueries({ exact: true, queryKey: issueTreeKey(key) }),
      ]);
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
