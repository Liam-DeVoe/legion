import { matchQuery, type QueryClient, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  createElement,
  type ReactNode,
  useContext,
  useEffect,
  useMemo,
  useSyncExternalStore,
} from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router-dom";

import { writeRequest } from "../../../api/client";
import { primarySpec } from "../../../api/issue-cache";
import { issueCoverageQuery, issueTreeKey } from "../../../api/queries";
import { type RefreshTarget, refreshQueries } from "../../../api/query-refresh";
import type { IssueDetails } from "../../../api/types";
import { type IssueRoute, parseIssuePath, parseNodeReference } from "../../refs/routes";
import { useKeyedWrite } from "../../writes/useKeyedWrite";
import { writesOf } from "../../writes/write-store";
import { type AddressTarget, issueAddress, pathToTarget, presetPath } from "../issue-address";
import { buildIssueTree, type TreeNode } from "../issue-tree";
import { CONTROL_LABELS } from "../page-words";
import { dropViews, issueTreeQuery } from "../tree-read/tree-read";
import { withLine } from "./node-states";

/**
 * What the page writes and reads around a node's life: the settle of a proposed node; the words
 * filter's words, by which the outline opens each line of overtaken nodes that holds a match; the
 * lines of overtaken nodes the page's address opens in a view; and what the tree read the page
 * already holds says of the overtaken nodes, for an address the Decisions view draws nowhere.
 */

/** `POST /api/v1/issues/{key}/nodes/{id}/settle`: a person settles the proposed node `reference`
 *  names, under the issue `key`. `{id}` is the node's whole address, scheme and all, which
 *  `routeUrl` percent-encodes as one path segment. It carries no body: a cookie proves a person,
 *  so the settle names no actor. */
function settleRequest(key: string, reference: string) {
  return writeRequest("post", "/api/v1/issues/{key}/nodes/{id}/settle", { id: reference, key });
}

/** The issue a node's own address names, which its settle goes under; undefined for
 *  an address that names none. The address says it with no context of the page's: Settle needs
 *  no writer, so it is drawn where no `OutlineWriterProvider` holds the issue's key. */
export function settleKeyOf(reference: string): string | undefined {
  const route = parseNodeReference(reference);
  return route !== undefined && "key" in route ? route.key : undefined;
}

/** The key of the issue the tree is drawn for, which the settle of a node whose own address names
 *  no issue goes under: the address of a lens, a risk, a constraint or a finding
 *  (`dispatch://lens/<uuid>`, `dispatch://lens/<uuid>#b-<block>`) names none, and the server
 *  settles such a node under the issue it stands under. Undefined outside a
 *  `SettleIssueProvider`. */
const SettleIssue = createContext<string | undefined>(undefined);

/**
 * Hands the key of the issue the tree is drawn for to each Settle under it (`SettleAct.tsx`), for
 * a node whose own address names no issue - a lens's, a risk's, a constraint's, a finding's -
 * which settles under the issue the tree is drawn for. `IssueTree` wraps its rows in it, with the
 * key of the issue its root is.
 */
export function SettleIssueProvider({
  children,
  value,
}: {
  children?: ReactNode;
  /** The key of the issue the tree is drawn for; undefined for a tree whose root is no issue. */
  value: string | undefined;
}): ReactNode {
  return createElement(SettleIssue.Provider, { value }, children);
}

/** The key of the issue the tree is drawn for, under which a node whose own address names no
 *  issue settles; undefined outside a `SettleIssueProvider`. */
export function useSettleIssue(): string | undefined {
  return useContext(SettleIssue);
}

/** One press's watch of the reads of the tree, for the read that tells of its landing
 *  (`SettleLandings.watch`): the node's act takes no press while it runs. */
interface LandingWatch {
  /** The tree's query, whose reads the watch reads. */
  readonly tree: RefreshTarget;
  readonly stop: () => void;
}

/**
 * The settles of one client's page that the act of their node waits on (`useSettle`), by the
 * node's reference, as the write's target `settle:<reference>` names the node: each from its
 * press until the read of the tree that tells of its landing has ended, or its refusal. One for
 * each node, whichever act made the press, since `NodeRow` draws a row's acts on the focused row
 * alone, and a reader who moves the focus off the row and back meanwhile finds a new act, which
 * reads the settle already waited on (`useSettleWaits`).
 */
class SettleLandings {
  readonly #client: QueryClient;
  /** The newest press's watch of each node, by the node's reference. */
  readonly #watches = new Map<string, LandingWatch>();
  readonly #listeners = new Set<() => void>();

  constructor(client: QueryClient) {
    this.#client = client;
  }

  readonly subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  };

  /** Whether a settle of the node `reference` names is waited on: its watch runs. */
  waits(reference: string): boolean {
    return this.#watches.has(reference);
  }

  /**
   * Watches, from a press of the settle of the node `reference` names, in place of an earlier
   * press's watch of that node, for the read of the tree of issue `issue` that tells of the
   * press's landing: the first read of it to begin with no write of that issue out, so after
   * Dispatch answered the press's own write, which is one of that issue's. A read out at the
   * press, or one begun while a write of the issue was out - a first load, the reader's Retry -
   * may read the tree from before the settle, and is passed over. The watch ends once that read
   * has ended, answered or refused, or once the cache lets the tree go, which no read then
   * answers.
   */
  watch(reference: string, issue: string): LandingWatch {
    this.end(reference, this.#watches.get(reference));
    const cache = this.#client.getQueryCache();
    const writes = writesOf(this.#client);
    const tree = { exact: true, queryKey: issueTreeKey(issue) };
    // Whether the read out began with no write of the issue out.
    let tells = false;
    const watch: LandingWatch = {
      stop: cache.subscribe((event) => {
        if (!matchQuery(tree, event.query)) return;
        if (event.type === "updated" && event.action.type === "fetch") {
          tells = writes.writesOutOf(issue).length === 0;
        } else if (
          event.type === "removed" ||
          (tells && event.query.state.fetchStatus === "idle")
        ) {
          this.end(reference, watch);
        }
      }),
      tree,
    };
    this.#watches.set(reference, watch);
    this.#publish();
    return watch;
  }

  /** The press `watch` is of has landed: where the cache holds no tree for a read to tell of the
   *  landing, the watch ends there; else it runs on to that read's end. */
  land(reference: string, watch: LandingWatch | undefined): void {
    if (watch !== undefined && this.#client.getQueryCache().find(watch.tree) === undefined) {
      this.end(reference, watch);
    }
  }

  /** Ends `watch`, where it is still the watch of the node `reference` names. */
  end(reference: string, watch: LandingWatch | undefined): void {
    if (watch === undefined || this.#watches.get(reference) !== watch) return;
    watch.stop();
    this.#watches.delete(reference);
    this.#publish();
  }

  #publish(): void {
    for (const listener of this.#listeners) listener();
  }
}

/** Each client's settles that wait (`SettleLandings`), as `writesOf` keeps its store: one for
 *  each app, as there is one client. */
const settleLandings = new WeakMap<QueryClient, SettleLandings>();

function landingsOf(client: QueryClient): SettleLandings {
  let landings = settleLandings.get(client);
  if (landings === undefined) {
    landings = new SettleLandings(client);
    settleLandings.set(client, landings);
  }
  return landings;
}

/** Whether the act of the node `reference` names waits on a settle of it (`useSettle`): from the
 *  press until the read of the tree that tells of its landing has ended, or its refusal. Read from
 *  the client's settles, not the act's own press, so an act drawn again for the row meanwhile -
 *  the reader moved the focus off it and back - reads a settle already pressed. */
export function useSettleWaits(reference: string): boolean {
  const landings = landingsOf(useQueryClient());
  return useSyncExternalStore(landings.subscribe, () => landings.waits(reference));
}

/**
 * The settle of the proposed node `reference` names, under the issue `key`: one keyed write
 * per press, which the act that makes it shows refused itself (`inline`), so the shell's
 * indicator names it only while it is out, and the settles of one node go in press order. A 5xx
 * is made again under its key, so the write stays out while it is tried again.
 *
 * From the press until the read of the tree that tells of its landing has ended, or its refusal,
 * the node's settle is waited on (`SettleLandings`): `useSettleWaits` answers true for the
 * node, so its act, and an act drawn again for its row meanwhile, says it is settling and takes
 * no press. A press then would go after this one, or settle a node Dispatch holds settled, and be
 * refused.
 *
 * A landing - a 200, or `null` for `409 KEY_COMMITTED` - tells `landed` first, so the act puts
 * the focus on its row before anything is read again. It then reads the coverage again, reads
 * the tree again as the page does on an event of the issue (`refreshQueries`, which waits for a
 * read already out rather than cutting it off), and drops the view reads held for the load
 * (`dropViews`), so the open view is read again; the write stays out until the coverage's read
 * is answered. The tree and the views are those of the issue the tree is drawn for
 * (`SettleIssueProvider`), else of `key`: a node whose own address names another issue settles
 * under that issue, whose coverage is the one read again. The coverage is invalidated first and
 * the tree asked for after it, so the coverage's read begins first. The tree is asked for here,
 * since the tree read's subscription hears no read but the tree's and its views' own
 * (`subscribeTreeFreshness` in `tree-read/tree-read.ts`), and another issue's coverage is no
 * query of the page's. The write is one of the issue the tree is drawn for (`issue`), whatever
 * issue its path names, since the subscription holds for the writes of its own issue alone;
 * `subscribeTreeFreshness` says what it holds while the write is out.
 * A refusal reads nothing again.
 *
 * The read that tells of the landing is the first read of the tree of `drawn` to begin with no
 * write of `drawn` out; `subscribeTreeFreshness` (in `tree-read/tree-read.ts`) says when the reads
 * go. That read may go out before this callback runs, so the watch of the tree's reads begins at
 * the press (`onMutate`); where the cache holds no tree at the landing, the wait ends there.
 */
export function useSettle(key: string, reference: string, landed: () => void) {
  const queryClient = useQueryClient();
  const drawn = useSettleIssue() ?? key;
  const landings = landingsOf(queryClient);
  return useKeyedWrite({
    inline: true,
    issue: drawn,
    label: () => CONTROL_LABELS.settle,
    onError: (_refusal, _variables, watch) => landings.end(reference, watch),
    onMutate: () => landings.watch(reference, drawn),
    onSuccess: (_answer, _variables, watch) => {
      landed();
      landings.land(reference, watch);
      const coverage = queryClient.invalidateQueries({
        exact: true,
        queryKey: issueCoverageQuery(key).queryKey,
      });
      refreshQueries(queryClient, [{ exact: true, queryKey: issueTreeKey(drawn) }]);
      dropViews(queryClient, drawn);
      return coverage;
    },
    request: () => settleRequest(key, reference),
    target: () => `settle:${reference}`,
  });
}

/** Whether the words filter holds words, as the page's address carries them (`?q=`): the same
 *  value `issue-address.ts`'s `issueAddress` reads into `IssueAddress.filter` from the location's
 *  search, which `slots/body.tsx` narrows the outline's view by (`presets.ts`'s `presetView`),
 *  trimmed as that view trims it. False for an address that narrows nothing. */
export function useHoldsWords(): boolean {
  const [search] = useSearchParams();
  return (search.get("q") ?? "").trim() !== "";
}

/**
 * The lines of overtaken nodes the page's address opens in `root`, a view's own tree: the id of
 * each node whose line of overtaken children the path to the node the address names runs
 * through (`pathToTarget`, `withLine`), so that node is drawn as a row, as the outline opens its
 * own. The first node of the path at or under an overtaken one (`closed`) is drawn in the line
 * of the node above it, though in a view's tree it need not be overtaken itself - a row drawn
 * under the line of a parent the view draws no row for - so that line opens too. The address is
 * read as the slots read it (`issueAddress`); an address that opens a view opens no document, so
 * no spec slug is needed to read it. `target` says which node the address names, so a view opens
 * them once for each; `lines` is undefined for an address that names no node and until `root`
 * holds the one it names, and empty where the path runs through no closed node.
 */
export function useAddressLines(root: TreeNode): {
  readonly lines: ReadonlySet<string> | undefined;
  readonly target: string | undefined;
} {
  const { hash, pathname, search } = useLocation();
  return useMemo(() => {
    const route = parseIssuePath(pathname, search);
    const target =
      route === undefined ? undefined : issueAddress(route, search, hash, undefined).target;
    const path = pathToTarget(root, target);
    const lines = path === undefined ? undefined : new Set(withLine(new Set<string>(), path));
    const closed = path?.findIndex((node) => node.closed === true) ?? -1;
    const above = closed > 0 ? path?.[closed - 1] : undefined;
    if (above !== undefined) lines?.add(above.id);
    return {
      lines,
      target: target === undefined ? undefined : JSON.stringify(target),
    };
  }, [hash, pathname, root, search]);
}

/**
 * The outline's tree as the reads the page already holds build it (`buildIssueTree`): the tree
 * read (`issueTreeQuery`) of the issue `key`, with `issue`, so each node carries its state and
 * its reference as the outline's does. The read is watched by an observer that is never enabled,
 * so it sends no request and holds what the page read. Undefined until the tree read is
 * held, and while the caller hands no `issue`, which builds nothing.
 */
function useHeldTree(key: string, issue: IssueDetails | undefined): TreeNode | undefined {
  const tree = useQuery({ ...issueTreeQuery(key), enabled: false }).data;
  return useMemo(
    () => (issue === undefined || tree === undefined ? undefined : buildIssueTree({ issue, tree })),
    [issue, tree]
  );
}

/**
 * The address of an overtaken node, or of a node under one, under the Decisions view, which draws
 * such a node nowhere: it is replaced by the same address under no filter, its search and its
 * fragment kept, so the outline draws it, opens the line that holds it and focuses it, and the
 * stage, the filter row and the outline agree.
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
  // The target by its own fields, so a render that changes neither them nor the tree walks none.
  const target = address.target;
  const kind = target?.kind;
  const id = target?.id;
  const slug = target?.kind === "block" ? target.document : undefined;
  const closed = useMemo(() => {
    if (root === undefined || kind === undefined || id === undefined) return false;
    const named: AddressTarget = kind === "block" ? { document: slug, id, kind } : { id, kind };
    return pathToTarget(root, named)?.at(-1)?.closed === true;
  }, [id, kind, root, slug]);
  useEffect(() => {
    if (!closed) return;
    navigate({ hash, pathname: presetPath(issue.key, "conversation"), search }, { replace: true });
  }, [closed, hash, issue.key, navigate, search]);
}
