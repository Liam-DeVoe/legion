import { useQueries } from "@tanstack/react-query";
import { type ReactNode, useEffect, useMemo, useRef } from "react";
import { Link, Navigate, useLocation } from "react-router-dom";

import { api } from "../../../api/client";
import { primarySpec } from "../../../api/issue-cache";
import { issueCoverageQuery, issueResultsQuery } from "../../../api/queries";
import { QueryError } from "../../../components/QueryError";
import {
  primaryButtonBg,
  primaryButtonHoverBg,
  textPrimaryOnCanvas,
  textSecondaryOnCanvas,
} from "../../../theme/classes";
import { issueDecisionsQuery } from "../../doc/spec-marks";
import { ItemMatchList } from "../../issue/ItemMatchList";
import type { ItemLanding, ItemRoute } from "../../issue/useItemLanding";
import { refetchFailedOnFocus } from "../../refs/reference-target";
import { buildIssuePath } from "../../refs/routes";
import { type AddressFallback, useAddressFallback, useAddressTarget } from "../address-target";
import { useOutlineEditing } from "../IssuePresetPanels";
import { IssueTree } from "../IssueTree";
import { issueAddress, landingPath } from "../issue-address";
import { artifactsBySlug } from "../issue-artifacts";
import { NoTooltips } from "../NoTooltips";
import { useOutlineRequest } from "../outline-request";
import { KEY_REPORTS } from "../page-structure";
import { EMPTY_STATE_LINES } from "../page-words";
import { presetView } from "../presets";
import type { SlotProps } from "../slot-props";
import { specSummary } from "../spec-summary";
import { useIssueTree } from "../useIssueTree";

/** What each read the outline waits on takes: a copy of the tree's own rule for its reads,
 *  `useIssueTree.ts`'s `EVENT_FRESH`, which this file is to import once that file exports it.
 *  Events keep a read fresh and its age never does, so the slot mounted again on a press of a
 *  view more than 30 s after the load reads none of them again, and window focus fetches again
 *  only a read in error. */
const EVENT_FRESH = {
  refetchOnWindowFocus: refetchFailedOnFocus,
  staleTime: Number.POSITIVE_INFINITY,
} as const;

/** Filled by INFRA-1196 part G, "A node's row", then part I, "The outline". */
export function BodySlot({ issue, route }: SlotProps): ReactNode {
  // The outline: the issue's one tree as the Conversation view leaves it, folded to the
  // frontier, in place of the rows drawn with every branch open before it. The spec's summary
  // callout is no row of it: the Spec view draws it as the issue's three sentences.
  const { hash, key: locationKey, search } = useLocation();
  const specSlug = primarySpec(issue)?.slug;
  const address = issueAddress(route, search, hash, specSlug);
  const { blocksHeld, root } = useIssueTree(issue);
  // Whether every read the tree's nodes come from holds its answer: the issue's children come
  // with the issue, the documents' blocks are `blocksHeld`, and every other node comes from one
  // of these, read under the keys `useIssueTree` reads them by, so they cost no request of their
  // own. Until then a tree with no node is one whose nodes have not arrived, and the outline's
  // line that the issue holds nothing would be false on every cold load, so the outline draws
  // nothing in its place; so would `q` and `w` saying there is no question or decision to go to,
  // so they say nothing. A read that failed holds nothing either: the page cannot say what it
  // would have held. The conversation's reads are kept too, which tell a turn the outline leaves
  // out (`useAddressFallback`).
  const key = issue.key;
  const nodeReads = useQueries({
    combine: (answers) => {
      const [, , , asks, comments, messages] = answers;
      return {
        asks: asks.data,
        comments: comments.data,
        held: answers.every((answer) => answer.data !== undefined),
        messages: messages.data,
      };
    },
    queries: [
      { ...issueCoverageQuery(key), ...EVENT_FRESH },
      { ...issueDecisionsQuery(key, true), ...EVENT_FRESH },
      { ...issueResultsQuery(key), ...EVENT_FRESH },
      { queryFn: () => api.listIssueAsks(key, "all"), queryKey: ["asks", key], ...EVENT_FRESH },
      { queryFn: () => api.listComments(key), queryKey: ["comments", key], ...EVENT_FRESH },
      {
        queryFn: () => api.listIssueMessages(key),
        queryKey: ["issue-messages", key],
        ...EVENT_FRESH,
      },
    ],
  });
  const readsHeld = nodeReads.held && blocksHeld;
  // The address's own node is resolved against the whole tree and then kept by the view, so an
  // address that names a node the words filter would not have matched still draws it. An item
  // the tree holds no node of falls back to the item's own page once the reads are held
  // (`useAddressFallback`); a turn the outline leaves out lands on its nearest drawn ancestor,
  // which the view keeps as it keeps the address's own node.
  const addressed = useAddressTarget(root, address);
  const fallback = useAddressFallback({
    address,
    issue,
    reads: nodeReads,
    readsHeld,
    reveal: addressed.reveal,
    root,
    route,
    search,
  });
  const ancestor = fallback?.kind === "agents-only" ? fallback.ancestor : undefined;
  const ancestorNamed = useMemo(
    () => (ancestor === undefined ? undefined : new Set(ancestor.slice(-1).map((one) => one.id))),
    [ancestor]
  );
  const named = ancestorNamed ?? addressed.named;
  const memo = useMemo(() => specSummary(root, specSlug ?? "")?.id, [root, specSlug]);
  const view = useMemo(
    () => presetView(root, "conversation", address.filter, named, memo),
    [address.filter, memo, named, root]
  );
  const artifacts = useMemo(() => artifactsBySlug(issue), [issue]);
  const request = useOutlineRequest();
  // The document a row of the outline is being edited in, and that document's one editor, which
  // the body draws for the row to take onto its own stage (`useOutlineEditing`).
  const { onEditing, panels } = useOutlineEditing(issue);
  const isClosed = issue.closed_at !== null;
  return (
    <NoTooltips>
      {/* The outline's measure, the prototype's `#pane`: at most 1000 px, in the middle of the
          column. On a phone 4 px from the left edge and 10 px from the right, as the
          prototype's `#reading` is there, so the bullets keep the picture's place. */}
      <div className="mx-auto flex w-full max-w-[1000px] min-w-0 flex-col max-[761px]:-mr-1 max-[761px]:-ml-2.5 max-[761px]:w-auto">
        {fallback?.kind === "landing" ? (
          // Keyed on the following, so a link to the same item followed again is drawn anew and
          // takes the focus again.
          <ItemFallback fallback={fallback} key={locationKey} />
        ) : (
          <IssueTree
            addressKey={locationKey}
            artifacts={artifacts}
            context={view.context}
            emptyLine={
              address.filter === "" ? EMPTY_STATE_LINES.conversation : EMPTY_STATE_LINES.no_match
            }
            filtering={view.filtering}
            focusReveal
            // The words filter's Escape comes before the outline is left.
            holdsEscape={address.filter !== ""}
            isClosed={isClosed}
            {...(ancestor === undefined ? {} : { landingReport: KEY_REPORTS.agents_thread })}
            onEditing={onEditing}
            readsHeld={readsHeld}
            {...(request === undefined ? {} : { request })}
            reveal={ancestor ?? addressed.reveal}
            root={view.root}
            rootRow={view.rootRow}
          />
        )}
      </div>
      {panels}
    </NoTooltips>
  );
}

/**
 * The address's item's own page, drawn in the outline's place where the tree holds no node of
 * the item and the old page's landing has read it (`useAddressFallback`), as today's page draws
 * it (`issue/IssuePage.tsx`, `IssueDetail`): an item of another issue, or one beside a document,
 * is gone to, and so is the whole id an id written short names; the several items a short id
 * names are listed to choose from; and an item the issue holds none of, or whose read failed,
 * is said to be so. The list and the failure take the focus as they are drawn, so it is on the
 * line that names the item and never left on the page's body.
 *
 * Going to a document goes to the address that opens it with the item marked (`landingPath`),
 * never to the one the old landing writes (`?comment=`), which this page reads as the item's
 * node again and would go to the same document again and again.
 */
function ItemFallback({
  fallback,
}: {
  fallback: Extract<AddressFallback, { kind: "landing" }>;
}): ReactNode {
  const { landing, route } = fallback;
  const list = useFocusOnDraw<HTMLDivElement>();
  // The address is replaced rather than added to the reader's history, so Back still leaves the
  // page they came from.
  if (landing.kind === "redirect" || landing.kind === "rewrite") {
    return <Navigate replace to={landingPath(landing.to)} />;
  }
  if (landing.kind === "ambiguous") {
    return (
      <div ref={list} tabIndex={-1}>
        <ItemMatchList candidates={landing.candidates} route={route} />
      </div>
    );
  }
  return <ItemLandingFailure landing={landing} route={route} />;
}

/** The element its ref is given to takes the focus once it is drawn. */
function useFocusOnDraw<T extends HTMLElement>() {
  const element = useRef<T>(null);
  useEffect(() => element.current?.focus(), []);
  return element;
}

/** What each kind of item is called: a copy of `issue/IssuePage.tsx`'s `itemNoun`. */
const ITEM_NOUNS: Readonly<Record<ItemRoute["kind"], string>> = {
  ask: "Ask",
  comment: "Comment",
  message: "Message",
};

/**
 * An address's item this issue holds none of (`missing`), or whose read failed
 * (`unavailable`), named in the page, where the outline would be, with the focus on its line.
 *
 * A copy of `issue/IssuePage.tsx`'s `ItemLandingFailure`, which that file keeps to itself, with
 * its words, as `IssueOutlinePage.tsx` copies today's failed states: today's page is changed by
 * additions alone, and the copy goes when that page is removed. Its missing line is drawn as the
 * shell's `NotFoundPage` draws it, but here: that page names the whole window "Not found", where
 * the issue's page stands around it. Each heading is the page's second level, under the issue's.
 */
function ItemLandingFailure({
  landing,
  route,
}: {
  landing: Extract<ItemLanding, { kind: "missing" | "unavailable" }>;
  route: ItemRoute;
}): ReactNode {
  const heading = useFocusOnDraw<HTMLHeadingElement>();
  const noun = ITEM_NOUNS[route.kind];
  if (landing.kind === "missing") {
    return (
      <section>
        <h2 className="mb-2 text-2xl font-semibold" ref={heading} tabIndex={-1}>
          {`${noun} not found`}
        </h2>
        <p className={textSecondaryOnCanvas}>
          {`That link doesn't name ${route.kind === "ask" ? "an" : "a"} ${noun.toLowerCase()} on ${route.key} — it may have been deleted.`}
        </p>
        <Link
          className={`mt-4 inline-block rounded-lg px-3 py-2 text-sm font-medium ${primaryButtonBg} ${primaryButtonHoverBg}`}
          to={buildIssuePath({ key: route.key, kind: "issue" })}
        >
          {`Back to ${route.key}`}
        </Link>
      </section>
    );
  }
  return (
    <section>
      <h2 className={`text-xl font-semibold ${textPrimaryOnCanvas}`} ref={heading} tabIndex={-1}>
        Couldn&apos;t load this {noun.toLowerCase()}
      </h2>
      <div className="mt-2">
        <QueryError message={`Couldn't load this ${noun.toLowerCase()}.`} onRetry={landing.retry} />
      </div>
    </section>
  );
}
