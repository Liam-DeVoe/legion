import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";

import { primarySpec } from "../../api/issue-cache";
import type { Artifact, IssueDetails } from "../../api/types";
import { textMutedOnCanvas } from "../../theme/classes";
import { PHONE_VIEWPORT_QUERY, useMediaQuery } from "../shell/useDialog";
import { IssueChildren } from "./IssueChildren";
import { ContentsList, IssueContentsPane, OutlineSheet } from "./IssueContents";
import { IssuePresets } from "./IssuePresets";
import { IssueThreeThings, nodesOfType, specSummary } from "./IssueThreeThings";
import { IssueTree, type OutlineRequest } from "./IssueTree";
import type { AddressTarget, IssueAddress } from "./issue-address";
import { pathToTarget } from "./issue-address";
import { artifactsBySlug } from "./issue-artifacts";
import type { TreeNode } from "./issue-tree";
import { pageChrome } from "./page-structure";
import { EMPTY_STATE_LINES, PRESET_ORDER, type Preset } from "./page-words";
import { presetCounts, presetView } from "./presets";
import { useIssueTree } from "./useIssueTree";
import { firstWaitingNode, waitingPressOf } from "./waiting";

/** The width the Contents pane stands beside the outline from (R8, R14): 1024 px, in `rem` so
 *  it switches with Tailwind's `lg:` at any default font size. */
export const CONTENTS_PANE_QUERY = "(min-width: 64rem)";

/** The page's inset from the edges of its column, the prototype's: 28 px from the phone's width
 *  up, 14 px below it. The Contents pane takes none, and stands against the rail. */
export const PAGE_INSET = "px-3.5 md:px-7";

/**
 * The issue's body on every address: its three sentences, the preset row, the children section
 * and the one tree the preset is a query over (dispatch://INFRA-1196 R1, R3, R5, R10) - or, where
 * the address opens a document, that document in place of the children section and the tree.
 *
 * It is one component because all four read the same tree: the head reads the spec's summary
 * node and the issue's checks, the row counts what each preset's query holds, and the tree
 * draws what the open preset left. Building that tree is this page's one model read
 * (`useIssueTree.ts`), so it happens here, once, below the guards `IssuePage` draws for an
 * issue it could not load.
 *
 * Under the preset row the body is two columns from 1024 px up: the Contents pane against the
 * rail, with the three sentences and the list of questions and the reader's own threads, and
 * the reading column with the outline (R8, R14). Below 1024 px the pane is not drawn: the three
 * sentences head the reading column, above the outline, and the list is behind the Outline
 * control beside the presets.
 */
export function IssueBody({
  address,
  below,
  openedDocument,
  isClosed,
  issue,
  onEditing,
  onLeave,
  plan,
  toolbar,
}: {
  address: IssueAddress;
  /** What the page draws under the outline, in the reading column (`IssuePresetPanels`). */
  below?: ReactNode;
  /** The document the address opens, drawn in place of the children section and the tree
   *  (R3), with its version control beside the filters; undefined where the outline is drawn. */
  openedDocument?: { readonly panel: ReactNode; readonly toolbar: ReactNode };
  /** Whether the issue is closed, which the node menu's upload reads. */
  isClosed: boolean;
  issue: IssueDetails;
  /** The document a row of the tree is being edited in, so the page draws that document's one
   *  editor for the row to take; `undefined` while the reader is editing nothing. */
  onEditing: (artifactId: string | undefined) => void;
  /** Called before a preset's own control leaves this address, so the page can record where
   *  the reader was before the move. */
  onLeave: () => void;
  /** The issue's plan document, whose state the head's first row draws. */
  plan: Artifact | undefined;
  /** The version control beside the filters while the outline is drawn. */
  toolbar?: ReactNode;
}): ReactNode {
  const navigate = useNavigate();
  const { hash, pathname, search, state: locationState } = useLocation();
  const { blocksRead, root } = useIssueTree(issue);
  const counts = useMemo(
    () => presetCounts(root, address.filter, PRESET_ORDER),
    [address.filter, root]
  );
  // The address's own node is resolved against the whole tree and then kept by the preset's
  // query, so an address that names a node the preset would not have matched still draws it.
  // The target is read by what it says rather than by the value `issueAddress` built, which is
  // a new one on every render: the tree is walked and pruned again only when the address or
  // the tree itself changes, not on every commit the page makes.
  const targetKind = address.target?.kind;
  const targetId = address.target?.id;
  // Which document a block target belongs to, where its address names one: a block id alone
  // names a block of any document of the issue (`pathToTarget`).
  const targetDocument = address.target?.kind === "block" ? address.target.document : undefined;
  const target = useMemo<AddressTarget | undefined>(() => {
    if (targetKind === undefined || targetId === undefined) return undefined;
    if (targetKind === "block") return { document: targetDocument, id: targetId, kind: "block" };
    // These fields are the whole of a target, and this is the one place that says so, so a
    // target holds its value across a render rather than being built afresh.
    return { id: targetId, kind: targetKind } as AddressTarget;
  }, [targetDocument, targetId, targetKind]);
  const reveal = useMemo(() => pathToTarget(root, target), [root, target]);
  // The node the address named when the page was first drawn here: a fresh load's own, which
  // keeps the rule below, where a document the preset draws lands on its own node. A node named
  // after that was named by a link or a chip the reader followed on this page, and a keyboard
  // reader reaches the chain only if the focus goes where that link went (R10): its row takes
  // the focus whatever preset draws it.
  const landedOn = useRef(target);
  const followed = target !== undefined && target !== landedOn.current;
  // The node the preset draws the body of without being asked: the decision waiting for the
  // reader's review, whose body holds Keep and Change, so a reader who opens the preset is in
  // front of what waits for them (R7). It opens the body and never takes the focus, which is
  // the reader's.
  const opened = useMemo(
    () => (target === undefined ? opensOn(root, address.preset) : undefined),
    [address.preset, root, target]
  );
  const named = useMemo(() => {
    const node = reveal?.at(-1);
    return new Set(node === undefined ? [] : [node.id]);
  }, [reveal]);
  const view = useMemo(
    () => presetView(root, address.preset, address.filter, named),
    [address.filter, address.preset, named, root]
  );
  const artifacts = useMemo(() => artifactsBySlug(issue), [issue]);
  const drawsDocument = openedDocument !== undefined;
  const shownToolbar = openedDocument === undefined ? toolbar : openedDocument.toolbar;
  const specSlug = primarySpec(issue)?.slug ?? "";
  // What a key reported when it had nowhere to go, raised by the tree (`IssueTree`'s
  // `onReport`) and drawn here as the page's own status.
  const [report, setReport] = useState("");
  // Where the version control goes. From `md` it sits beside the preset row, as the design
  // draws it (R2). Below `md` it is 96 px of a 844 px screen - the version picker, the
  // name-a-version control and the link, two lines of 44 px controls - and the identity block,
  // the head, the preset row and the children section already take 700 px of it, so it is
  // drawn under the tree instead: the same move the identity block's fourth row makes at that
  // width (`IssueHeader.tsx`), and for the same reason - the first open question has to be on a
  // phone's first screen (R6). A document opened in place of the tree has no tree to go under,
  // so there it is drawn above the document it reads. Nothing is behind a control either way,
  // so every one of its controls is still drawn with nothing opened (R18).
  const isPhoneViewport = useMediaQuery(PHONE_VIEWPORT_QUERY);
  const phoneToolbar =
    isPhoneViewport && shownToolbar !== undefined ? (
      <div className="mt-1 flex min-w-0 flex-wrap items-center gap-2">{shownToolbar}</div>
    ) : null;
  // Where the three sentences and the list are drawn: in the Contents pane from 1024 px up, and
  // below that the sentences over the outline and the list behind the Outline control (R8).
  const paned = useMediaQuery(CONTENTS_PANE_QUERY);
  // What the list last asked of the outline, and whether the outline draws one branch at a
  // time: the pane and the sheet share them, so a width that swaps one for the other keeps both.
  const [request, setRequest] = useState<OutlineRequest | undefined>(undefined);
  const [focusBranch, setFocusBranch] = useState(false);
  const ask = (id: string | undefined, branch: boolean) =>
    setRequest((held) => ({ focusBranch: branch, id, seq: (held?.seq ?? 0) + 1 }));
  // The header's count of what waits on the reader turns the Decisions filter on and leaves its
  // press in the history entry (`IssuePropertiesRow.tsx`): once the filter has drawn the first
  // node that waits - an open question whose turn is the reader's, or a decision waiting for
  // their review, in the filter's order - the outline goes to it as the Contents pane's list
  // goes to a node, once per press (R13, dispatch://INFRA-3678).
  const waitingPress = waitingPressOf(locationState);
  const waitingNode = useMemo(
    () =>
      waitingPress === undefined || address.preset !== "decisions"
        ? undefined
        : firstWaitingNode(view.root),
    [address.preset, view.root, waitingPress]
  );
  const tookPress = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (waitingPress === undefined || waitingNode === undefined || drawsDocument) return;
    if (tookPress.current === waitingPress) return;
    tookPress.current = waitingPress;
    setRequest((held) => ({ focusBranch, id: waitingNode, seq: (held?.seq ?? 0) + 1 }));
  }, [drawsDocument, focusBranch, waitingNode, waitingPress]);
  const list = (close?: () => void) =>
    drawsDocument ? null : (
      <ContentsList
        focusBranch={focusBranch}
        onFocusBranch={(current) => {
          setFocusBranch(!focusBranch);
          ask(current, !focusBranch);
        }}
        onGo={(id) => {
          close?.();
          ask(id, focusBranch);
        }}
        root={view.root}
      />
    );
  const head = (
    <IssueThreeThings
      checks={nodesOfType(root, "check")}
      inPane={paned}
      issueKey={issue.key}
      plan={plan}
      planTasks={nodesOfType(root, "plan_task").length}
      reading={!blocksRead}
      summary={specSummary(root, specSlug)}
    />
  );

  return (
    <>
      {/* What a key reported when it had nowhere to go: the page's one `role="status"`, which
          is empty while every key the reader pressed went somewhere (R9, R21). It is drawn
          here, above everything, so the page's own report is the first status a reader meets -
          ahead of the live-connection status a document's toolbar carries. */}
      <p className={`text-xs ${PAGE_INSET} ${textMutedOnCanvas}`} role="status" {...pageChrome}>
        {report}
      </p>
      <div className={PAGE_INSET}>
        <IssuePresets
          active={address.preset}
          artifacts={issue.artifacts ?? []}
          counts={counts}
          filter={address.filter}
          isClosed={isClosed}
          issueKey={issue.key}
          nodeMenu={address.nodeMenu}
          onLeave={onLeave}
          onFilterChange={(next) => {
            const query = new URLSearchParams(search);
            if (next === "") query.delete("q");
            else query.set("q", next);
            const rest = query.toString();
            navigate(`${pathname}${rest === "" ? "" : `?${rest}`}${hash}`, { replace: true });
          }}
          outline={paned || drawsDocument ? undefined : <OutlineSheet list={list} />}
          toolbar={isPhoneViewport ? undefined : shownToolbar}
        />
      </div>
      <div className="flex min-w-0 items-start">
        {paned ? <IssueContentsPane brief={head} list={list()} /> : null}
        <div className={`flex min-w-0 flex-1 flex-col pt-2.5 ${PAGE_INSET}`}>
          {paned ? null : head}
          {drawsDocument ? (
            <>
              {phoneToolbar}
              {openedDocument?.panel}
            </>
          ) : (
            <>
              <IssueChildren issue={issue} />
              <IssueTree
                artifacts={artifacts}
                context={view.context}
                emptyLine={EMPTY_STATE_LINES[address.preset]}
                filtering={view.filtering}
                focusReveal={followed || address.preset !== "spec" || targetKind === "item"}
                isClosed={isClosed}
                onEditing={onEditing}
                onReport={setReport}
                openBody={opened}
                request={request}
                reveal={reveal}
                root={view.root}
              />
              {phoneToolbar}
            </>
          )}
          {below}
        </div>
      </div>
    </>
  );
}

/**
 * The chain down to the node a preset opens on when the address names none: on the Decisions
 * preset the first decision waiting for the reader's review, so the reader lands on what waits
 * for them with Keep and Change in its body (R6, R7). Every other preset opens on no node.
 */
function opensOn(root: TreeNode, preset: Preset): string | undefined {
  if (preset !== "decisions") return undefined;
  return nodesOfType(root, "decision").find((node) => node.waiting)?.id;
}
