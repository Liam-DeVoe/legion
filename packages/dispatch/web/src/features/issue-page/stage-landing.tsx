import { isIdPrefix, itemFromSearch } from "@legion/contracts";
import { useQuery } from "@tanstack/react-query";
import {
  type ReactNode,
  type RefObject,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { Navigate, useLocation, useNavigate, useNavigationType } from "react-router-dom";

import { api } from "../../api/client";
import { useAddressedItem } from "../artifacts/ArtifactDocument";
import { useWholeRouteRead } from "../refs/reference-target";
import { buildIssuePath, type IssueRoute } from "../refs/routes";
import { useOpenedDocument, useOutlineAddress } from "./document-address";
import { useEscapeOrder } from "./escape-order";
import { useFocusedNode } from "./focused-node";
import { issueAddress } from "./issue-address";
import { DETAILS_ATTRIBUTE, useMarginDrawer } from "./margin-slot";
import { OpenedDocument } from "./OpenedDocument";
import { useAskOutline, useFocusBranch } from "./outline-request";
import { isFilter } from "./presets";
import type { SlotProps } from "./slot-props";
import { useFollowing } from "./slots/following";
import { useDocumentToolbars } from "./useDocumentToolbars";
import { useIssueDetail } from "./useIssueDetail";
import { type ItemLanding, isItemRoute, useItemLanding } from "./useItemLanding";

/**
 * What the stage draws before the outline or a view (dispatch://INFRA-1196 part N's R1, R6), for
 * what `main`'s outline does not land itself: `instead`, the address replaced where it writes an
 * id short beside a view's address (`?ask=`, `?comment=`) or under `?mark=`, with the whole id
 * under the same parameter, and nothing while that id or an ask the address names is still read;
 * `document`, the document or file the address opens in place of the outline and of every view
 * (`OpenedDocument.tsx`); and `outline`, the route the outline is drawn at in place of the
 * address's, which is the issue's own for an approval request's address, whose entry the header
 * draws, so the outline lands on no item of it. None, and the stage draws the outline or the
 * view. Every other item an address names - on screen in the outline, an item of another issue,
 * an id written short on an item's own address, several ids, none, a failed read - is the
 * outline's own landing (`slots/body.tsx`), which this does not do again.
 *
 * It also binds the page's own Escape steps between a caret and the outline (R1,
 * `escape-order.ts`), keeps the outline's scroll place across a round trip through a view or a
 * document, puts the focus on an approval request's entry in the header when the address names
 * one, and opens Details at the issue's steps on `/steps`.
 */
export function useStageLanding({ issue, route }: SlotProps): {
  readonly document?: ReactNode;
  readonly instead?: ReactNode;
  readonly outline?: IssueRoute;
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
  // replaced with the whole id under the same parameter, which names the node
  // (`ArtifactDocument`'s `useAddressedItem`); a whole id reads nothing more. Several items that
  // one id names are the outline's to list. Until the short id's read first answers the outline
  // is not drawn: its own landing, which watches that read's state (`useAddressFallback`), can
  // see the answer a render before this does and replace the address with the item's own
  // (`/comments/<id>`). `queryRead` is that read, and sends nothing of its own.
  const namesItem = address.document === undefined && address.target?.kind === "item";
  const queryItem =
    namesItem && !isItemRoute(route) ? (itemFromSearch(search) ?? undefined) : undefined;
  const owner = { key: issue.key, kind: "issue" } as const;
  const queryLanding = useAddressedItem(owner, queryItem);
  const queryRead = useWholeRouteRead(
    queryItem === undefined ? undefined : { id: queryItem.id, key: issue.key, kind: queryItem.kind }
  );
  // `?mark=` names an ask or a comment by its id alone: a comment when the issue's comments hold
  // that id - or, for an id written short, one starting with it - and an ask otherwise. Only an
  // address that opens its document reads them here (a view's address draws the view), under the
  // key the outline reads them by, so it costs no request of its own. A short id is resolved here
  // and written whole under `?mark=`, so the document lands on the item under its whole id.
  const listed = opened === undefined ? undefined : address.documentItem;
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
  useStepsAddress(route);
  useEscapeOrder({
    documentOpen: opened !== undefined,
    filter: address.filter,
    issueKey: issue.key,
    viewOn: isFilter(address.preset),
  });
  useOutlinePlace(opened === undefined && !isFilter(address.preset), address.target !== undefined);
  const queryAnswered = useAnsweredOnce(
    queryRead.prefix === undefined || !queryRead.resolution.isPending
  );
  const askAnswered = useAnsweredOnce(route.kind !== "ask" || landing?.kind !== "pending");

  // The rewrite replaces the address with the whole id rather than adding to the reader's
  // history, so Back still leaves the page they came from.
  if (queryLanding.rewrite !== undefined) {
    return { instead: <Navigate replace to={queryLanding.rewrite} /> };
  }
  if (!queryAnswered) return { instead: null };
  if (markLanding.rewrite !== undefined) {
    return { instead: <Navigate replace to={markLanding.rewrite} /> };
  }
  // An approval request is no node of the outline, whose own landing would take its address for
  // the document it reviews and replace it there: the address stays the request's, and the
  // outline is drawn at the issue's own address under the header that holds its entry. Until an
  // ask's own read has said whether it is one, the outline is not drawn, so its landing takes
  // none.
  if (landing?.kind === "approval") return { outline: { key: issue.key, kind: "issue" } };
  if (!askAnswered) return { instead: null };
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

/**
 * Keeps the node the reader was last on in a view while the stage turns the view off - by
 * Escape, `t c`, Conversation or Back - and then asks the outline for it, which draws it focused
 * (R14). Each view's tree sets that node as its rows take the focus (`focused-node.tsx`); the
 * focus leaving a view gives Details the issue back, by the outline's rule (`useReleaseFocus`) or
 * the Decisions view's own, so what is kept is the last node a view set, not what F holds as the
 * view goes. A node F held before the view was drawn is no node of the view's. Where the address
 * names a node of its own to land on, the outline lands there instead, and nothing is asked.
 *
 * It is an element of its own, so a move of the focus draws it alone and not the stage.
 */
export function ViewNodeKeeper({
  lands,
  viewOn,
}: {
  /** Whether the address names a node beside the view for the outline to land on. */
  readonly lands: boolean;
  /** Whether the stage draws a view in the outline's place. */
  readonly viewOn: boolean;
}): null {
  const focused = useFocusedNode();
  const ask = useAskOutline();
  const [focusBranch] = useFocusBranch();
  const kept = useRef<string | undefined>(undefined);
  const seen = useRef(focused);
  const wasOn = useRef(viewOn);
  // In the commit that draws the outline, before its own effects take the request.
  useLayoutEffect(() => {
    const from = wasOn.current;
    wasOn.current = viewOn;
    if (from === viewOn) return;
    const node = kept.current;
    kept.current = undefined;
    seen.current = focused;
    if (from && node !== undefined && !lands) ask(node, focusBranch);
  }, [ask, focusBranch, focused, lands, viewOn]);
  useLayoutEffect(() => {
    if (focused === seen.current) return;
    seen.current = focused;
    if (viewOn && focused !== undefined) kept.current = focused.id;
  }, [focused, viewOn]);
  return null;
}

/** What the stage keeps of the first thing a view told it for a following of an address that
 *  names a node (`ViewSlotProps.onAddressDrawn`): that following, and whether the reader had
 *  acted since it began (`ReaderActsWatch`) by then. */
export interface ViewTold {
  readonly following: string;
  readonly reader: boolean;
}

/** The mark the stage sets on the box it draws a view in (`useAddressLanding`), which the box's
 *  take of the focus reads (`ViewStage`), with the values it takes (`LANDING_MARKS`). */
export const ADDRESS_LANDING = "data-address-landing";

/** The values of `ADDRESS_LANDING`: the view has not told the stage yet whether it draws the
 *  node the address names (`undecided`), or told it after the reader's first act since the
 *  address was followed (`acted`). */
export const LANDING_MARKS = { acted: "acted", undecided: "undecided" } as const;

/**
 * An address that names a node moves the focus once, onto where it lands (dispatch://INFRA-1196
 * decision 413): the row the view draws for the node, the outline's row drawn in the view's place,
 * the line there that the issue holds none of it, or a failed read's Retry. Where the view takes
 * the focus on its tab stop as it draws its rows (`ViewStage`), before every read it needs has
 * settled and it can say whether it draws the node, the focus moved first to the view's first
 * row, then to the outline drawn in its place, then to the line drawn in the outline's place. So
 * from the following's start (`following`, undefined where the address names no such node) until
 * the view has told the stage what it draws (`told`), the stage marks the box it draws the view in
 * (`box`) undecided, and the box's take of the focus gives none to a row meanwhile. Told after
 * the reader's first act since the address was followed, it marks the box `acted`: the landing
 * moves nothing then, as the view's own landing on its row does nothing after such an act. The
 * mark is set in the commit that draws the view, before the box's take runs, and read again as
 * it changes. An address that names no node leaves the box unmarked, so the view's tab stop takes
 * the focus as its rows are drawn.
 */
export function useAddressLanding(
  box: RefObject<HTMLElement | null>,
  following: string | undefined,
  told: ViewTold | undefined
): void {
  const mark =
    following === undefined
      ? undefined
      : told?.following !== following
        ? LANDING_MARKS.undecided
        : told.reader
          ? LANDING_MARKS.acted
          : undefined;
  // At every commit of the stage, so a box drawn anew for another view takes the mark too.
  useLayoutEffect(() => {
    const element = box.current;
    if (element === null) return;
    if (mark === undefined) element.removeAttribute(ADDRESS_LANDING);
    else element.setAttribute(ADDRESS_LANDING, mark);
  });
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
 * Whether `answered` has held at any render since the address was followed (`useFollowing`): a
 * read the stage waits for has answered once. A later run of that read - a Retry, or a failed
 * read asked again as the outline mounts, which sets it back to pending - holds nothing back,
 * so what the outline drew for its answer stays drawn while it runs.
 */
function useAnsweredOnce(answered: boolean): boolean {
  const following = useFollowing();
  const [answeredIn, setAnsweredIn] = useState<string | undefined>(undefined);
  if (answered && answeredIn !== following) setAnsweredIn(following);
  return answered || answeredIn === following;
}

/** How many frames `/steps` waits for Details to draw the steps and the history above them once
 *  it has opened Details: about two seconds, the history's first read on a cold load. */
const STEPS_FRAMES = 120;

/** The heading of the steps inside Details (`DetailsSteps.tsx`), which takes the focus. */
const STEPS_HEADING = `aside[${DETAILS_ATTRIBUTE}] [data-testid="issue-steps"] h2`;

/** The history above the steps in Details, drawn: its list, or the line that it failed
 *  (`IssueHistory.tsx`). Its entries arriving after the steps took the focus would push them out
 *  of Details' view. */
const HISTORY_DRAWN = `aside[${DETAILS_ATTRIBUTE}] [data-testid="issue-history"] :is(ol, [role="alert"])`;

/** Puts the focus on the steps' heading inside Details and brings it to the top of Details' view,
 *  once Details has drawn it and the history above it; `left` frames are waited for that, and
 *  then the heading is reached as it stands. A page that never draws it leaves the focus where it
 *  was. */
function reachSteps(left: number): void {
  const heading = document.querySelector<HTMLElement>(STEPS_HEADING);
  const drawn = heading !== null && heading.closest("[hidden]") === null;
  if (!(drawn && document.querySelector(HISTORY_DRAWN) !== null) && left > 0) {
    requestAnimationFrame(() => reachSteps(left - 1));
    return;
  }
  if (!drawn) return;
  heading.focus({ preventScroll: true });
  heading.scrollIntoView({ block: "start" });
}

/**
 * `/steps`, the address of today's Steps tab, opens Details at the issue's steps, which part M
 * draws under the history there (part N's R6): Details opens, as `/activity` opens it at the
 * history, and the steps' heading takes the focus once Details draws it, as `/collaborators`
 * puts it on that row's control. It opens once per arrival at the address, which is one following
 * of it (`useFollowing`), so the reader's Close stands until they arrive again - by `t e`, which
 * opens that address (`useIssueKeymap.ts`), or by Back to it from another address, as
 * `/activity` opens again (`details-more.tsx`'s `DetailsHistory`). The search waits two frames
 * first, as `useOpenDetailsAt` does, for the drawer gives the focus to its own Close as it opens.
 */
function useStepsAddress(route: IssueRoute): void {
  const following = useFollowing();
  const openDetails = useMarginDrawer()?.setOpen;
  const steps = route.kind === "steps";
  const openedFor = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!steps) {
      if (openedFor.current !== following) openedFor.current = undefined;
      return;
    }
    if (openedFor.current === following) return;
    openedFor.current = following;
    openDetails?.(true);
    requestAnimationFrame(() => requestAnimationFrame(() => reachSteps(STEPS_FRAMES)));
  }, [following, openDetails, steps]);
}

/**
 * Keeps the outline's scroll place across a round trip through a view or a document (R1, the
 * plan's ST-5). The place is recorded on each scroll while the outline is drawn, for the
 * following it is drawn in (`useFollowing`), and kept while a view or a document is drawn in its
 * place. When the outline is drawn again it is put back before the browser paints: by Back, the
 * place of the following Back returns to; by Conversation, `t c` or Escape off a view - each an
 * address of the outline that names no node - or by a document's way back to the outline address
 * it was opened from, the outline's last place. A new address that names a node, a link to it,
 * puts nothing back: it lands by its own rules (`slots/body.tsx`). A scroll the browser makes as
 * a shorter view clamps the page lands after that view is drawn, so it is never the outline's.
 */
function useOutlinePlace(drawn: boolean, namesNode: boolean): void {
  const { pathname, search } = useLocation();
  const following = useFollowing();
  const move = useNavigationType();
  // The address as a document's way back writes it (`NodeMenu.tsx`'s `outline`).
  const address = `${pathname}${search}`;
  const places = useRef(new Map<string, number>());
  const last = useRef<{ readonly address: string; readonly top: number } | undefined>(undefined);
  const now = useRef({ address, drawn, following });
  useEffect(() => {
    const record = () => {
      const { address, drawn, following } = now.current;
      if (!drawn) return;
      places.current.set(following, window.scrollY);
      last.current = { address, top: window.scrollY };
    };
    window.addEventListener("scroll", record, { passive: true });
    return () => window.removeEventListener("scroll", record);
  }, []);
  useLayoutEffect(() => {
    const wasDrawn = now.current.drawn;
    now.current = { address, drawn, following };
    if (wasDrawn || !drawn) return;
    const returned = move === "POP" ? places.current.get(following) : undefined;
    const outline =
      last.current !== undefined && (!namesNode || last.current.address === address)
        ? last.current.top
        : undefined;
    const top = returned ?? outline;
    if (top !== undefined) window.scrollTo({ top });
  }, [address, drawn, following, move, namesNode]);
}
