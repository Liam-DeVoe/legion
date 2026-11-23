import { isIdPrefix, itemFromSearch } from "@legion/contracts";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useEffect, useLayoutEffect, useRef } from "react";
import { Navigate, useLocation, useNavigate } from "react-router-dom";

import { api } from "../../api/client";
import { QueryError } from "../../components/QueryError";
import { textPrimaryOnCanvas } from "../../theme/classes";
import { useAddressedItem } from "../artifacts/ArtifactDocument";
import { ItemMatchList } from "../issue/ItemMatchList";
import { buildIssuePath } from "../refs/routes";
import { useKeymap } from "../shell/keymap";
import { NotFoundPage } from "../shell/NotFoundPage";
import { useOpenedDocument, useOutlineAddress } from "./document-address";
import { issueAddress, presetPath } from "./issue-address";
import { OpenedDocument } from "./OpenedDocument";
import { useAskOutline, useFocusBranch } from "./outline-request";
import { NODE_TYPE_NAMES, sentenceCase } from "./page-words";
import { isFilter } from "./presets";
import type { SlotProps } from "./slot-props";
import { useDocumentToolbars } from "./useDocumentToolbars";
import { useIssueDetail } from "./useIssueDetail";
import { type ItemLanding, type ItemRoute, isItemRoute, useItemLanding } from "./useItemLanding";

/**
 * What the stage draws before the outline or a view (dispatch://INFRA-1196 part N's R1, R6):
 * `instead`, what an item's address lands on where it is not a node of the outline - another
 * address it moves to, the items an id written short names, or the page's words for one it does
 * not name - and `document`, the document or file the address opens in place of the outline and
 * of every view (`OpenedDocument.tsx`). Neither, and the stage draws the outline or the view.
 *
 * It also binds the page's own Escape steps between a caret and the outline (R1): a caret is left
 * first (the outline's and a document's own), then an opened document is closed
 * (`OpenedDocument.tsx`), then the filter by words is emptied, then every view is turned off,
 * and then the outline is left (`useTreeKeymap.ts`). The words' two steps and the views' are
 * these. It keeps the outline's scroll place across a round trip through a view, and puts the
 * focus on an approval request's entry in the header when the address names one.
 */
export function useStageLanding({ issue, route }: SlotProps): {
  readonly document?: ReactNode;
  readonly instead?: ReactNode;
} {
  const { hash, search } = useLocation();
  const { highlightTerm, primaryArtifact } = useIssueDetail(issue, route);
  const address = issueAddress(route, search, hash, primaryArtifact?.slug);
  const landing = useItemLanding(route);
  const opened = useOpenedDocument(issue, route);
  const outline = useOutlineAddress(issue.key);
  const navigate = useNavigate();
  const { diffs, showDiff, toolbarSink } = useDocumentToolbars();
  // The ask or comment an address's `?ask=` or `?comment=` names for the outline to open on. An
  // id written short is resolved as a document's address resolves one, and the address is
  // replaced with the whole id, which names the node (`ArtifactDocument`'s `useAddressedItem`); a
  // whole id reads nothing more.
  const namesItem = address.document === undefined && address.target?.kind === "item";
  const queryItem =
    namesItem && !isItemRoute(route) ? (itemFromSearch(search) ?? undefined) : undefined;
  const owner = { key: issue.key, kind: "issue" } as const;
  const queryLanding = useAddressedItem(owner, queryItem);
  // `?mark=` names an ask or a comment by its id alone: a comment when the issue's comments hold
  // that id - or, for an id written short, one starting with it - and an ask otherwise. Only such
  // an address reads them here, under the key the outline reads them by, so it costs no request
  // of its own. A short id is resolved here and written whole under `?mark=`, so the document
  // lands on the item under its whole id.
  const listed = address.documentItem;
  const marked = listed?.kind === "mark" ? listed.id : undefined;
  const comments = useQuery({
    enabled: marked !== undefined,
    queryFn: () => api.listComments(issue.key),
    queryKey: ["comments", issue.key],
  });
  const markedItem =
    marked === undefined || comments.data === undefined
      ? undefined
      : {
          id: marked,
          kind: comments.data.some(
            (comment) =>
              comment.id === marked || (isIdPrefix(marked) && comment.id.startsWith(marked))
          )
            ? ("comment" as const)
            : ("ask" as const),
        };
  const markLanding = useAddressedItem(owner, markedItem, "mark");
  const documentItem =
    listed?.kind !== "mark"
      ? listed
      : markedItem === undefined || isIdPrefix(markedItem.id)
        ? undefined
        : markedItem;
  useApprovalFocus(landing);
  usePageEscape({
    documentOpen: opened !== undefined,
    filter: address.filter,
    issueKey: issue.key,
    viewOn: isFilter(address.preset),
  });
  useScrollPlace(opened === undefined ? address.preset : "document");

  // The rewrite replaces the address with the whole id rather than adding to the reader's
  // history, so Back still leaves the page they came from.
  if (landing?.kind === "redirect" || landing?.kind === "rewrite") {
    return { instead: <Navigate replace to={landing.to} /> };
  }
  if (queryLanding.rewrite !== undefined) {
    return { instead: <Navigate replace to={queryLanding.rewrite} /> };
  }
  if (markLanding.rewrite !== undefined) {
    return { instead: <Navigate replace to={markLanding.rewrite} /> };
  }
  if (isItemRoute(route) && landing?.kind === "ambiguous") {
    return { instead: <ItemMatchList candidates={landing.candidates} route={route} /> };
  }
  if (queryItem !== undefined && queryLanding.candidates !== undefined) {
    return {
      instead: (
        <ItemMatchList
          candidates={queryLanding.candidates}
          route={{ id: queryItem.id, key: route.key, kind: queryItem.kind }}
        />
      ),
    };
  }
  if (isItemRoute(route) && (landing?.kind === "missing" || landing?.kind === "unavailable")) {
    return { instead: <ItemLandingFailure landing={landing} route={route} /> };
  }
  // Until the item's own read answers, the stage draws nothing rather than an outline the
  // landing is about to leave.
  if (landing?.kind === "pending") return { instead: null };
  if (opened === undefined) return {};
  const openedId = opened.artifact?.id ?? "";
  return {
    document: (
      <OpenedDocument
        artifact={opened.artifact}
        artifactRoute={opened.route}
        highlightTerm={highlightTerm}
        isClosed={issue.closed_at !== null}
        issueKey={issue.key}
        item={documentItem}
        onShowDiff={(next) => showDiff(openedId, next)}
        onToolbarChange={toolbarSink(openedId)}
        // A version picked in the document's own editor opens it at that version, keeping the
        // outline it was opened from to go back to.
        onVersionChange={(version) =>
          navigate(
            buildIssuePath({
              key: issue.key,
              kind: "artifact",
              slug: opened.route.slug,
              ...(version === null ? {} : { version }),
            }),
            { state: { outline } }
          )
        }
        outline={outline}
        showDiff={diffs.has(openedId)}
      />
    ),
  };
}

/** An item deep link the page could not resolve: the id names nothing on this issue, or the
 *  lookup failed. Either way the reader is told which item, inside the issue page. The item's
 *  name is the page's own word for that kind of node (`page-words.ts`): "Question not found" for
 *  an ask (part N's R6). */
function ItemLandingFailure({
  landing,
  route,
}: {
  landing: Extract<ItemLanding, { kind: "missing" | "unavailable" }>;
  route: ItemRoute;
}): ReactNode {
  const noun = NODE_TYPE_NAMES[route.kind];
  // The article follows the word the page draws, which is "question" for an ask (R22).
  const article = /^[aeiou]/iu.test(noun) ? "an" : "a";
  if (landing.kind === "missing") {
    return (
      <NotFoundPage
        backLabel={`Back to ${route.key}`}
        backTo={`/issues/${route.key}`}
        detail={`That link doesn't name ${article} ${noun} on ${route.key} — it may have been deleted.`}
        title={`${sentenceCase(noun)} not found`}
      />
    );
  }
  return (
    <section>
      <h1 className={`text-xl font-semibold ${textPrimaryOnCanvas}`}>
        Couldn&apos;t load this {noun}
      </h1>
      <div className="mt-2">
        <QueryError message={`Couldn't load this ${noun}.`} onRetry={landing.retry} />
      </div>
    </section>
  );
}

/** An approval request's own address puts the focus on the approval the header draws for the
 *  document it reviews, since the outline draws no node for it (`useItemLanding`'s `approval`,
 *  R20). It runs once the request is resolved, and again only for another landing: after that
 *  the focus is the reader's. */
function useApprovalFocus(landing: ItemLanding | undefined): void {
  const approvalOf = landing?.kind === "approval" ? landing.artifactId : undefined;
  useEffect(() => {
    if (approvalOf === undefined) return;
    document
      .querySelector<HTMLElement>(`[data-approval-artifact="${CSS.escape(approvalOf)}"]`)
      ?.focus();
  }, [approvalOf]);
}

/**
 * The page's own Escape steps between a caret and the outline (R1): the box the filter by words
 * is typed in empties, then leaves; the filter's words are cleared; every view is turned off,
 * the reader kept on the node they were on. A document open over the page closes first
 * (`OpenedDocument.tsx`), a menu open over it answers its own press (`IssuePresets.tsx`), and a
 * control inside a row of the outline gives the row back first (`useTreeKeymap.ts`).
 */
function usePageEscape({
  documentOpen,
  filter,
  issueKey,
  viewOn,
}: {
  documentOpen: boolean;
  filter: string;
  issueKey: string;
  viewOn: boolean;
}): void {
  const navigate = useNavigate();
  const { hash, pathname, search } = useLocation();
  const ask = useAskOutline();
  const [focusBranch] = useFocusBranch();
  // What the filter by words holds is `?q=`, which the box writes in place, keeping the rest of
  // the address.
  const setWords = (next: string) => {
    const query = new URLSearchParams(search);
    if (next === "") query.delete("q");
    else query.set("q", next);
    const rest = query.toString();
    navigate(`${pathname}${rest === "" ? "" : `?${rest}`}${hash}`, { replace: true });
  };
  // Whether the page's own Escape is the next step: no document is open, whose close comes
  // first, no menu is open, whose press it is, and the focus is in no control inside a row of
  // the outline, which Escape gives the row back from first (`useTreeKeymap.ts`).
  const escapesHere = () => {
    if (documentOpen || document.querySelector('[role="menu"]') !== null) return false;
    const active = document.activeElement;
    const row = active?.closest("[data-node-row]");
    return row === null || row === undefined || row === active;
  };
  useKeymap("issue", [
    {
      // The box the filter by words is typed in: Escape empties it, and leaves it once it is
      // empty, as Escape leaves a caret first.
      id: "words-leave",
      inEditable: true,
      keys: "Escape",
      label: "Clear the filter by words, then leave it",
      palette: false,
      run: () => {
        if (filter !== "") setWords("");
        else (document.activeElement as HTMLElement | null)?.blur();
      },
      when: () => document.activeElement?.matches('[data-testid="issue-filter"]') === true,
    },
    {
      id: "words-clear",
      keys: "Escape",
      label: "Clear the filter by words",
      palette: false,
      run: () => setWords(""),
      when: () => filter !== "" && escapesHere(),
    },
    {
      // Every view off, which is Conversation's address. A reader on a row of the view stays on
      // that node in the outline it opens on, so the next Escape leaves the outline.
      id: "filters-off",
      keys: "Escape",
      label: "Turn every view off",
      palette: false,
      run: () => {
        const on = document.activeElement?.closest<HTMLElement>("[data-node-row]")?.dataset.nodeId;
        navigate(presetPath(issueKey, "conversation"));
        if (on !== undefined) ask(on, focusBranch);
      },
      when: () => viewOn && escapesHere(),
    },
  ]);
}

/**
 * Keeps the reader's place on each view and on an opened document across a round trip (R1): a
 * press or a chord that turns a view on and off again leaves the outline where the reader left
 * it. The place is recorded as the reader scrolls, under the view on, and put back as a view is
 * drawn again, before the browser paints it. A scroll the browser makes as a shorter view clamps
 * the window lands after the view has changed, so it is recorded under the view it belongs to.
 */
function useScrollPlace(place: string): void {
  const places = useRef(new Map<string, number>());
  const current = useRef(place);
  useEffect(() => {
    const record = () => places.current.set(current.current, window.scrollY);
    window.addEventListener("scroll", record, { passive: true });
    return () => window.removeEventListener("scroll", record);
  }, []);
  useLayoutEffect(() => {
    current.current = place;
    const top = places.current.get(place);
    if (top !== undefined) window.scrollTo({ top });
  }, [place]);
}
