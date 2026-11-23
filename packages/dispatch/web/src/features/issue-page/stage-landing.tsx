import { isIdPrefix, itemFromSearch } from "@legion/contracts";
import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useEffect, useLayoutEffect, useRef, useState } from "react";
import { Navigate, useLocation, useNavigate } from "react-router-dom";

import { api } from "../../api/client";
import { useAddressedItem } from "../artifacts/ArtifactDocument";
import { useWholeRouteRead } from "../refs/reference-target";
import { buildIssuePath, type IssueRoute } from "../refs/routes";
import { useKeymap } from "../shell/keymap";
import { useOpenedDocument, useOutlineAddress } from "./document-address";
import { issueAddress, presetPath } from "./issue-address";
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
 * It also binds the page's own Escape steps between a caret and the outline (R1): a caret is left
 * first (the outline's and a document's own), then an opened document is closed
 * (`OpenedDocument.tsx`), then the filter by words is emptied, then every view is turned off,
 * and then the outline is left (`useTreeKeymap.ts`). The words' two steps and the views' are
 * these. It keeps the outline's scroll place across a round trip through a view, puts the focus
 * on an approval request's entry in the header when the address names one, and opens Details at
 * the issue's steps on `/steps`.
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
  usePageEscape({
    documentOpen: opened !== undefined,
    filter: address.filter,
    issueKey: issue.key,
    viewOn: isFilter(address.preset),
  });
  useScrollPlace(opened === undefined ? address.preset : "document");
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
