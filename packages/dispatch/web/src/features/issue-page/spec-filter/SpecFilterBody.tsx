import { useQuery } from "@tanstack/react-query";
import {
  Fragment,
  type KeyboardEvent,
  type ReactNode,
  type RefObject,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Link, useNavigate } from "react-router-dom";

import { api } from "../../../api/client";
import { primarySpec, roleDocument } from "../../../api/issue-cache";
import { issueCoverageQuery, issueResultsQuery } from "../../../api/queries";
import type { Actor, IssueDetails } from "../../../api/types";
import { QueryError } from "../../../components/QueryError";
import {
  pageDecisionText,
  pageFocusedRow,
  pageHairBg,
  pageHairBorder,
  pageInkText,
  pageKindBullet,
  pageLineBorder,
  pageMutedText,
  pagePaperBg,
  pageQuietText,
  pageStateText,
  pageWordsText,
} from "../../../theme/classes";
import { useAgentText } from "../../refs/AgentName";
import {
  buildReferencePath,
  type DispatchReferenceRoute,
  parseNodeReference,
} from "../../refs/routes";
import { appKeymap } from "../../shell/keymap";
import { agentRoleWord } from "../agent-role";
import { CutWords } from "../CutWords";
import { decisionNodes } from "../decision-nodes/decision-nodes";
import { conversationPath } from "../decisions-filter/decisions-parts";
import { useSetFocusedNode } from "../focused-node";
import { IssueThreeThings } from "../IssueThreeThings";
import { focusedNodeOf, type OutlineRequest, useReleaseFocus } from "../IssueTree";
import type { TreeNode } from "../issue-tree";
import { NodeGlyph } from "../NodeGlyph";
import { NodeEdges, useClamp, useRowOnScreen } from "../NodeRow";
import { chipWords } from "../node-row-chip";
import { shortDay } from "../node-time";
import { useReferenceNames } from "../one-line";
import { useTakeOutlineRequest } from "../outline-request";
import { useOutlineWriter } from "../outline-writer";
import { authorText, KEY_REPORTS, pageChrome } from "../page-structure";
import {
  ANSWER_LINES,
  authorKind,
  CONTROL_LABELS,
  EMPTY_STATE_LINES,
  GLYPH_TOKENS,
  NODE_TYPE_NAMES,
  nodeStateWord,
  SPEC_WORDS,
  sentenceCase,
  VERIFICATION_WORDS,
} from "../page-words";
import { RowText, useOutOfTabOrder } from "../RowText";
import { specSummary } from "../spec-summary";
import { treeContext } from "../tree-drafts";
import type { DrawnRow } from "../tree-fold";
import { nodesInOrder } from "../tree-keys";
import { useTreeRead, viewQuery } from "../tree-read/tree-read";
import { useTreeKeymap } from "../useTreeKeymap";
import {
  type SpecDecision,
  type SpecGroup,
  type SpecMark,
  type SpecParent,
  type SpecSaid,
  type SpecStatement,
  specDecidedBy,
} from "./spec-decided-by";

/** What a key that folds, zooms, cycles or opens a branch does here, where the columns hold
 *  none: every statement is drawn whole (R2: "a filter draws every branch it kept open"). */
const NOTHING = (): void => {};

/**
 * The Spec filter's body (dispatch://INFRA-1196 R5, R17, R22, R35, the prototype's `.spec`).
 *
 * It is the approved picture `page-v3-spec-1440.png`: a legend of the four marks a statement
 * carries, then two columns, Decided by and Statement. In the left column stands, once per run
 * of statements one decision shaped, that decision in the words it came from, with a bracket
 * tying it to its statements; in the right column each statement's words in full, with one line
 * under them naming the decision that shaped them, and its mark at the right of the row. At
 * 900 px and below it is one column and each decision stands above its statements, as
 * `page-v3-spec-390.png` draws it.
 *
 * Which decision shaped which statement is `spec-decided-by.ts`, which is pure; this module
 * draws it and holds two rules of its own:
 *
 * - **The question is drawn once.** Where the decision's own question is the node the
 *   statements hang under, the group's heading is that question and the cell does not say it
 *   again: "where the answered ask is the node's own parent, its parent line and its decision
 *   line are one line" (R5).
 * - **A statement's own words carry its number.** The picture draws a tag, "R1", at the right
 *   of each statement; the model holds no short name for a statement, and a block id is an
 *   identifier, which R22 refuses where a name belongs. The spec's own words open "**R5.**"
 *   where its author wrote a number, and the row draws them in full, so the number a reader
 *   cites is the author's own.
 *
 * After its column heads the body draws the issue's three sentences under their labels, as the
 * picture's first group (ruling 5 of build-rulings-md: "Under Spec: the first group"), in the
 * statements' column, as the prototype's `renderSpec` puts them.
 *
 * It is the outline under the Spec filter, so it is one `role="tree"` named as the outline is,
 * with the outline's keys (`useTreeKeymap.ts`): the statements are its rows, the focused one is
 * its one tab stop, the arrows and `j`/`k` move between them, `w` moves to the decision beside
 * a statement, the arrows and `j`/`k` move on from that decision to the statement after or
 * before its run, and Enter goes to a statement in the conversation, where it is edited. `r`
 * replies to a statement once part L makes a statement a target of the outline's writer, which
 * the body already passes to the keymap. The focus on a statement puts its facts in Details, as
 * a row of the outline does, and the focus on a decision puts that decision's; an address
 * naming a statement (`/spec#b-<block>`) puts the focus on it (R10, R19).
 */
export function SpecFilterBody({
  context,
  emptyLine,
  holdsEscape,
  issue,
  reader,
  reading,
  request,
  reveal,
  root,
  view,
}: {
  /** The ids of the rows the filter draws only as context, drawn faint (R2, R32). */
  context: ReadonlySet<string>;
  /** What the body says where the filter's query left no statement. */
  emptyLine: string;
  /** Whether the page takes Escape from a row before the body is left: a filter by words to
   *  clear or the filter to turn off comes first in the key table's order
   *  (`slots/view-spec.tsx`). */
  holdsEscape: boolean;
  issue: IssueDetails;
  /** Who is reading, so a decision of theirs reads as their own words (R34). */
  reader: string | undefined;
  /** Whether the spec's blocks are still being read, which says whether it holds a summary. */
  reading: boolean;
  /** What the page last asked of the outline: go to a node. The page acts on each ask once,
   *  whichever outline or body took it (`useTakeOutlineRequest`). */
  request?: OutlineRequest;
  /** The node the address names, which takes the focus once per address (R19). */
  reveal?: string;
  /** The whole tree, which holds the asks, the decisions and each statement's checks, and on
   *  the issue's node the Spec view read the page holds (`TreeNode.views`), whose decisions the
   *  column draws. */
  root: TreeNode;
  /** The tree as the Spec filter leaves it (`presetView`). */
  view: TreeNode;
}): ReactNode {
  const navigate = useNavigate();
  // Every read below is judged by the answer it holds, never by its status: a read the page asks
  // again - on every event that names its key (`event-refresh.ts`) - keeps its answer while it is
  // out and when it fails, and what the body drew from that answer stands. Only a read that
  // failed with no answer held says so, with a way to read again (`failedWithNothing`, the
  // architect's decision 98 on dispatch://INFRA-1196).
  // The decisions are the decision nodes of the Spec view read (`?view=spec`), which the page
  // holds on the issue's node (`TreeNode.views`), so the body costs no request of its own
  // to draw them: each decision the view holds, and each question a person answered that carries
  // `shaped` ties, which the asks read says is answered (`decisionNodes`). The view read and the
  // asks read are watched by observers that are never enabled, under the keys the page reads
  // them by (`ViewReadProvider`, `useIssueTree.ts`). A change of the issue marks the view stale
  // and reads it again with its answer kept (`subscribeTreeFreshness`), and only a new load drops
  // it (`dropViews`), so the column draws the answer it last held until the next one lands, and
  // after a read again that fails, which leaves the body an answer to draw. Until the view and
  // the asks read have both answered the column says nothing of a statement no decision shaped,
  // since it does not know yet: a person's decision is a question they answered, which only the
  // asks read says. Where the asks read failed with no answer held, the column says the open
  // questions could not be read, on a line of its own after the view read's, with a Retry that
  // reads the asks alone again.
  const specRead = useQuery({ ...viewQuery(issue.key, "spec"), enabled: false });
  const asksRead = useQuery({
    enabled: false,
    queryFn: () => api.listIssueAsks(issue.key, "all"),
    queryKey: ["asks", issue.key],
  });
  const asks = asksRead.data;
  const answered = root.views?.spec;
  const [last, setLast] = useState(answered);
  useEffect(() => {
    if (answered !== undefined) setLast(answered);
  }, [answered]);
  const held = answered ?? last;
  const decisions = useMemo(
    () => (held === undefined ? [] : decisionNodes(held, { asks, issue })),
    [asks, held, issue]
  );
  const decisionsFailed = failedWithNothing({ data: held, isError: specRead.isError });
  const asksFailed = failedWithNothing(asksRead);
  // The two reads the statements come from, the coverage and the spec's blocks, under the keys
  // the tree reads them with (no request of their own): until both hold an answer the body
  // cannot say the issue holds no statement. Both documents' blocks, the spec's here and the
  // Verification document's below, are the tree read's (`useTreeRead`).
  const spec = primarySpec(issue);
  const coverageRead = useQuery(issueCoverageQuery(issue.key));
  const { blocksOf } = useTreeRead(issue);
  const specBlocksRead = blocksOf("spec");
  const statementsRead =
    coverageRead.data !== undefined && (spec === undefined || specBlocksRead.data !== undefined);
  const statementsFailed = failedWithNothing(coverageRead) || failedWithNothing(specBlocksRead);
  // Each statement's mark is how it stands at the commit the page names, as the Verification
  // filter reads it (`statement-state.ts`), from three reads under the keys the tree reads them
  // with (no request of their own): the issue's results, the coverage, and the blocks of its
  // Verification document, which put each check under the statement it serves, the document
  // found as the tree read finds it (`tree-nodes.ts`'s `blocksOfRole`). Before all three hold an
  // answer a mark would say Unknown or no check of a statement that is Met, so none is drawn
  // until they do; a failed read of the results or of those blocks is one line, since either
  // leaves every statement unmarked.
  const resultsRead = useQuery(issueResultsQuery(issue.key));
  const results = resultsRead.data;
  const verification = roleDocument(issue, "verification");
  const checksRead = blocksOf("verification");
  const marked =
    results !== undefined &&
    coverageRead.data !== undefined &&
    (verification === undefined || checksRead.data !== undefined);
  const resultsFailed = failedWithNothing(resultsRead) || failedWithNothing(checksRead);
  // How this issue addresses a node, which is the one rule the tree was built with, so a
  // `shaped` tie resolves to the node it names (`tree-drafts.ts`).
  const nodeIdOf = useMemo(() => treeContext({ issue }).nodeIdOf, [issue]);
  // How a reference a plain line cites is named - in the three sentences, a quote, a question,
  // a group's heading - read once over the whole tree (`referenceStandIns`).
  const nameOf = useReferenceNames(root, nodeIdOf);
  const { groups, others } = useMemo(
    () =>
      specDecidedBy({
        checks: results?.checks ?? [],
        decisions,
        nameOf,
        nodeIdOf,
        reader,
        root,
        view,
      }),
    [decisions, nameOf, nodeIdOf, reader, results, root, view]
  );
  const specSlug = spec?.slug ?? "";
  const summary = useMemo(() => specSummary(root, specSlug), [root, specSlug]);
  const setFocusedNode = useSetFocusedNode();
  const writer = useOutlineWriter();
  const tree = useRef<HTMLDivElement | null>(null);
  const [focused, setFocused] = useState<string | undefined>(undefined);
  // The run whose cell holds the focus itself, which draws its decision whole (R5), named by
  // the run's first statement; undefined while the focus is on a statement or on a control
  // inside a cell. The body holds it, as it holds the focused statement, so a press defers both
  // alike (`focusItem`).
  const [openCell, setOpenCell] = useState<string | undefined>(undefined);
  // Where the tree's one tab stop rests, which outlives the focus leaving it (R9).
  const [stop, setStop] = useState<string | undefined>(undefined);
  // What a key reported when it had nowhere to go, and the statement it was pressed on, which
  // draws it under its words until the reader moves on (the tester's F16, `NodeRow`'s `report`).
  const [report, setReport] = useState<{ readonly line: string; readonly row: string }>();
  const reportHere = useCallback((line: string) => {
    const row = document.activeElement?.closest<HTMLElement>("[data-node-row]")?.dataset.nodeId;
    setReport(line === "" || row === undefined ? undefined : { line, row });
  }, []);
  // The statements in the order they are drawn, which is the order the keys move through them,
  // and after them every other row the filter kept.
  const statements = useMemo(
    () => [
      ...groups.flatMap((group) =>
        group.runs.flatMap((run) => run.statements.map((statement) => statement.node))
      ),
      ...others,
    ],
    [groups, others]
  );
  const rows = useMemo<DrawnRow[]>(
    () => statements.map((node) => ({ kind: "node", level: 1, node, trail: [] })),
    [statements]
  );
  const takeFocus = useCallback(
    (node: TreeNode) => {
      setFocused(node.id);
      setOpenCell(undefined);
      setStop(node.id);
      // A move to a statement is the reader going on from what a key reported.
      setReport((held) => (held?.row === node.id ? held : undefined));
      setFocusedNode(focusedNodeOf(node));
    },
    [setFocusedNode]
  );
  // The focus on a decision's cell is the reader reading that decision: no statement draws the
  // focused look any longer, and Details draws the node the cell is drawn as, or the issue's own
  // facts beside a run no decision shaped. The tab stop stays on the last statement (R9).
  // `open` names the run whose cell draws its words whole, where the cell itself has the focus.
  const takeCell = useCallback(
    (decision: SpecDecision | undefined, open: string | undefined) => {
      setFocused(undefined);
      setOpenCell(open);
      setReport(undefined);
      const id = decision?.node ?? decision?.id;
      const node = id === undefined ? undefined : nodesInOrder(root).find((one) => one.id === id);
      setFocusedNode(node === undefined ? undefined : focusedNodeOf(node));
    },
    [root, setFocusedNode]
  );
  // R7 under a press, as the outline holds it (`IssueTree.tsx`'s `focusRow`). A press focuses
  // what it lands on as it goes down, before it ends, so a statement taking the focus for a link
  // inside it would draw its words whole - the cut, "more" and the chips beside it gone - and the
  // statement or cell the focus left would draw its own cut again, while the press is held: a
  // link inside or under a cut statement moved off the pointer, and the press ended on something
  // else, so it followed no link (review 5928280634 on pull request 4234). So the focus a press
  // gives a statement or a cell - any focus inside it, a link's included - is taken once the
  // press is over, after its mouseup and the click that follows it, in a task of its own, and
  // only while that statement or cell still holds the focus; a key's move takes it at once. This
  // is a copy of the outline's deferral, which this part imports in place of it, with
  // `PRESS_ENDS`, once part I exports one (decision 94).
  const press = useRef<{
    ending?: number;
    focus?: { readonly item: HTMLElement; readonly take: () => void };
    held: boolean;
  }>({ held: false });
  const focusItem = useCallback((item: HTMLElement, take: () => void) => {
    if (press.current.held) press.current.focus = { item, take };
    else take();
  }, []);
  useEffect(() => {
    const held = press.current;
    const end = () => {
      held.held = false;
      const pending = held.focus;
      held.focus = undefined;
      if (pending?.item.contains(document.activeElement) === true) pending.take();
    };
    const ended = () => {
      if (!held.held) return;
      window.clearTimeout(held.ending);
      held.ending = window.setTimeout(end, 0);
    };
    const listening = new AbortController();
    for (const type of PRESS_ENDS) {
      window.addEventListener(type, ended, { capture: true, signal: listening.signal });
    }
    return () => {
      listening.abort();
      window.clearTimeout(held.ending);
    };
  }, []);
  const focusStatement = useCallback(
    (item: HTMLElement, node: TreeNode) => focusItem(item, () => takeFocus(node)),
    [focusItem, takeFocus]
  );
  const focusCell = useCallback(
    (item: HTMLElement, decision: SpecDecision | undefined, open: string | undefined) =>
      focusItem(item, () => takeCell(decision, open)),
    [focusItem, takeCell]
  );
  // The focus leaving the body gives the panel back to the issue's own facts, unless it went
  // to Details, where a reader reads the node's: the outline's own rule (`useReleaseFocus`).
  // This body edits nothing, so no row holds the focus through an edit.
  const leave = useCallback(() => {
    setFocused(undefined);
    setOpenCell(undefined);
  }, []);
  const releaseFocus = useReleaseFocus({ editing: false, onLeave: leave });
  // Puts the focus on the statement or the decision cell that draws node `id`.
  const goTo = useCallback((id: string): boolean => {
    const target = tree.current?.querySelector<HTMLElement>(
      `[data-node-id="${CSS.escape(id)}"][role="treeitem"]`
    );
    target?.focus();
    return target != null;
  }, []);
  // `w` on a statement, or on a control inside one: the focus goes to the cell beside it, which
  // draws the decision its run is headed by, whichever decision the tree's order names first
  // (`decisionBehind` can name one that only informed it, or one that was replaced); a run no
  // decision shaped says so at the statement (R5, R35). False where the focus is on no
  // statement, which leaves the key to the keymap.
  const toOwnCell = useCallback((): boolean => {
    const container = tree.current;
    const statement = document.activeElement?.closest<HTMLElement>("[data-spec-statement]");
    const id = statement?.dataset.nodeId;
    if (container === null || statement == null || id === undefined) return false;
    if (!container.contains(statement)) return false;
    const cell = container.querySelector<HTMLElement>(
      `[data-spec-cell][data-spec-statements~="${CSS.escape(id)}"]`
    );
    if (cell?.hasAttribute("data-decision-id") === true) cell.focus();
    else reportHere(KEY_REPORTS.no_decision);
    return true;
  }, [reportHere]);
  // `j`, `k` and the arrows from a cell, which is no row of the keymap's (it finds none there
  // and would start again from the top): the statement after the cell's run, or before it,
  // and at either end of the tree the run's own last or first statement, as the keymap stops
  // at its ends.
  const leaveCell = useCallback(
    (cell: HTMLElement, step: 1 | -1): void => {
      const rows = [...(tree.current?.querySelectorAll<HTMLElement>("[data-node-row]") ?? [])];
      const ids = (cell.dataset.specStatements ?? "").split(" ");
      const first = rows.findIndex((row) => row.dataset.nodeId === ids[0]);
      const last = rows.findIndex((row) => row.dataset.nodeId === ids.at(-1));
      reportHere("");
      (step > 0 ? (rows[last + 1] ?? rows[last]) : (rows[first - 1] ?? rows[first]))?.focus();
    },
    [reportHere]
  );
  // What the page asked for, each ask taken once by whichever outline or body the page drew when
  // it came (`useTakeOutlineRequest`), so a body drawn again - the view turned off and on - does
  // not move the reader for an ask already acted on. An ask goes only to a statement's row,
  // never to a cell: a cell can carry the answered ask's id, which a Contents entry names.
  const takeRequest = useTakeOutlineRequest();
  useEffect(() => {
    if (request === undefined || !takeRequest(request) || request.id === undefined) return;
    tree.current
      ?.querySelector<HTMLElement>(`[data-node-row][data-node-id="${CSS.escape(request.id)}"]`)
      ?.focus();
  }, [request, takeRequest]);
  // An address that names a statement puts the focus on it, once per address (R19): the row is
  // not on the page until the reads that hold it have answered, so this looks again on every
  // drawing of the columns.
  const taken = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (reveal === undefined || taken.current === reveal || statements.length === 0) return;
    if (goTo(reveal)) taken.current = reveal;
  }, [goTo, reveal, statements]);

  useTreeKeymap({
    container: tree,
    holdsEscape,
    onCycle: NOTHING,
    // Enter on a statement goes to it in the conversation, where it is edited: this filter
    // draws each statement and edits none.
    onEdit: (_row, node) => {
      const path = conversationPath(node);
      if (path !== undefined) navigate(path);
    },
    onEditEnd: NOTHING,
    onFold: NOTHING,
    onOpenDeeper: NOTHING,
    onPin: NOTHING,
    onReport: reportHere,
    // `w` and `q` can name a node this filter draws no row or cell for - a decision that shaped
    // no statement of the view, an open question - and the row says the key went nowhere, in
    // the words the outline says it in. A decision named from a statement is that statement's
    // own cell's to answer, as the tree's key handler answers a `w` it takes first.
    onReveal: (id) => {
      const decided = decisions.some((one) => nodeIdOf(one.ref) === id);
      if (decided && toOwnCell()) return;
      if (goTo(id)) return;
      reportHere(decided ? KEY_REPORTS.no_decision : KEY_REPORTS.no_next_question);
    },
    onZoom: NOTHING,
    oneNodeAtATime: false,
    rows,
    // The whole tree, which `q` walks for the next open question, and `w` on a row that is no
    // statement for the decision behind it (`tree-keys.ts`'s `decisionBehind`).
    root,
    writer,
    zoom: undefined,
  });

  const tabStop =
    stop !== undefined && statements.some((node) => node.id === stop) ? stop : statements[0]?.id;
  return (
    <div className="mt-3.5 mb-[120px] flex w-full min-w-0 flex-col" data-spec-body="">
      {statementsFailed ? (
        <QueryError
          message={ANSWER_LINES.statements_unreadable}
          onRetry={() => {
            if (failedWithNothing(coverageRead)) void coverageRead.refetch();
            if (failedWithNothing(specBlocksRead)) void specBlocksRead.refetch();
          }}
        />
      ) : null}
      {decisionsFailed ? (
        <QueryError
          message={ANSWER_LINES.decisions_unreadable}
          onRetry={() => void specRead.refetch()}
        />
      ) : null}
      {/* The asks read failed with no answer held: a person's decisions are answered questions,
          which only it says, so the column says the open questions could not be read, on a line
          of its own after the view read's. It is a copy of the shape of the page's shared
          failed-read line (`slots/failed-read-line.tsx`'s `FailedReadLine`), which it takes
          once this branch holds it. */}
      {asksFailed ? (
        <QueryError
          message={ANSWER_LINES.asks_unreadable}
          onRetry={() => void asksRead.refetch()}
        />
      ) : null}
      {resultsFailed ? (
        <QueryError
          message={ANSWER_LINES.results_unreadable}
          onRetry={() => {
            if (failedWithNothing(resultsRead)) void resultsRead.refetch();
            if (failedWithNothing(checksRead)) void checksRead.refetch();
          }}
        />
      ) : null}
      <Legend />
      <div
        className={`sticky top-[var(--issue-identity-height,0px)] z-[6] grid grid-cols-[280px_minmax(0,1fr)] gap-x-5 border-b pt-2 pb-1.5 max-[901px]:hidden ${pageLineBorder} ${pagePaperBg}`}
        data-spec-head=""
      >
        <span className={CAP} {...pageChrome}>
          {SPEC_WORDS.decided_by}
        </span>
        <span className={`pl-[26px] ${CAP}`} {...pageChrome}>
          {sentenceCase(NODE_TYPE_NAMES.requirement)}
        </span>
      </div>
      {/* The issue's three sentences, the first group, in the statements' column, as the
          prototype's `renderSpec` puts them after the column heads (ruling 5). */}
      <div className="mt-[18px] ml-[300px] min-w-0 max-[901px]:ml-0">
        <IssueThreeThings look="spec" nameOf={nameOf} reading={reading} summary={summary} />
      </div>
      {/* The outline the page draws under the Spec filter, which is this body's two columns:
          one tree, named as the tree every other filter draws, so a reader meets one outline
          on every address (R2, `IssueTree.tsx`). Its items are each statement and each
          decision beside them, which is what the focus moves between (R5's `w`); the layout
          between them carries no role of its own. */}
      <div
        aria-label={CONTROL_LABELS.tree}
        className="flex min-w-0 flex-col"
        data-spec-tree=""
        onBlur={releaseFocus}
        // A focus on the tree itself - part D's `reachPart` after `t s` lands here - goes on to
        // the tab-stop row; where the view draws no row it stays here.
        onFocus={(event) => {
          if (event.target !== event.currentTarget) return;
          event.currentTarget.querySelector<HTMLElement>('[data-node-row][tabindex="0"]')?.focus();
        }}
        // The keys a statement and a cell answer before the keymap does (`useTreeKeymap.ts`
        // listens on the window, after this): `w` on a statement, and `j`, `k` and the arrows
        // on a cell, which the keymap finds no row on.
        onKeyDown={(event) => {
          const key = bareKey(event);
          if (key === "w") {
            if (toOwnCell()) event.preventDefault();
            return;
          }
          const step = key === undefined ? undefined : CELL_STEPS[key];
          const cell =
            event.target instanceof Element
              ? event.target.closest<HTMLElement>("[data-spec-cell]")
              : null;
          if (step === undefined || cell === null) return;
          event.preventDefault();
          leaveCell(cell, step);
        }}
        // A press in the tree, whose focus a statement or a cell takes once the press is over
        // (`focusItem`). A press on what is no item - a group's heading, the space between runs
        // and cells - leaves the focus where it was: the tree would take it as their nearest
        // focusable box and hand it on to the tab-stop row, moving the reader and the page to a
        // statement they did not press.
        onMouseDown={(event) => {
          window.clearTimeout(press.current.ending);
          press.current.held = true;
          if (
            event.target instanceof Element &&
            event.target.closest('[role="treeitem"]') === null
          ) {
            event.preventDefault();
          }
        }}
        ref={tree}
        role="tree"
        tabIndex={-1}
      >
        {groups.map((group) => (
          <Group
            context={context}
            focused={focused}
            group={group}
            key={group.parent?.id ?? group.runs[0]?.statements[0]?.node.id ?? ""}
            known={held !== undefined && asks !== undefined}
            marked={marked}
            onCellFocus={focusCell}
            onFocus={focusStatement}
            openCell={openCell}
            {...(report === undefined ? {} : { report })}
            tabStop={tabStop}
          />
        ))}
        {others.map((node) => (
          <OtherRow
            focused={focused === node.id}
            key={node.id}
            node={node}
            onFocus={(row) => focusStatement(row, node)}
            tabStop={tabStop === node.id}
          />
        ))}
      </div>
      {statementsRead && statements.length === 0 ? (
        // What the page says where the filter's query matched nothing under the issue, in the
        // words the outline says it in (`IssueTree.tsx`).
        <p className={`py-1 text-sm ${pageMutedText}`} data-spec-empty="" {...pageChrome}>
          {emptyLine}
        </p>
      ) : null}
    </div>
  );
}

/** A column heading, in the prototype's `.cap`. */
const CAP = `text-xs leading-4 font-semibold tracking-[0.08em] uppercase ${pageQuietText}`;

/** A link inside a line of words, which the hair-coloured underline marks as one. */
const INLINE_LINK = "dispatch-inline-link underline decoration-page-hair underline-offset-[3px]";

/** Which way each key the keymap moves between rows with moves from a cell: on to the
 *  statement after its run, or back to the one before it. */
const CELL_STEPS: Readonly<Partial<Record<string, 1 | -1>>> = {
  ArrowDown: 1,
  ArrowUp: -1,
  j: 1,
  k: -1,
};

/** What ends a press in the tree, as the outline reads it (`IssueTree.tsx`'s `PRESS_ENDS`): its
 *  mouseup, which the click follows in the same task; the drag a press on a link starts instead,
 *  which sends no mouseup; a pointer the browser cancelled; and a key, since a press released
 *  outside the window sends no mouseup, and a key's move must never wait on a press that is
 *  over. */
const PRESS_ENDS = ["mouseup", "dragend", "pointercancel", "keydown"] as const;

/** Whether `read` failed with no answer held: a read asked again that fails keeps the answer it
 *  had, which the body still draws from, so only a read that left nothing to draw says it
 *  failed (the architect's decision 98 on dispatch://INFRA-1196). A read that is off - no
 *  document plays its part - has not failed. */
function failedWithNothing(read: { readonly data: unknown; readonly isError: boolean }): boolean {
  return read.isError && read.data === undefined;
}

/** How far below the view's column heads a statement or cell is put where it has to be scrolled
 *  to: the clearance `bringOnScreen` leaves below the band (`identity-block.ts`'s private
 *  `CLEARANCE`), which this part imports once that module exports it (decision 94). */
const HEADS_CLEARANCE = 8;

/**
 * Places the focused statement or cell on screen by the page's one rule (`useRowOnScreen`,
 * `bringOnScreen`), and then below the view's own column heads, which stay at the top under the
 * band and which that rule does not know of (R4; R11 of dispatch://INFRA-1196: the focused row
 * is never under a sticky header). The rule clears the band alone, so on the tester's build a
 * statement the arrows moved up to stood with its top 27 px down, under heads 31 px tall. Where
 * the item stands under the heads it is scrolled down by the rest, by the rule's own clearance,
 * again whenever it grows - after the rule has placed it, since its observer is made first - and
 * never while a press inside it is held, as the rule holds its own placing. At 901 px and below
 * the heads are not drawn, and the rule alone places it.
 *
 * Answers the item's own `onPointerDown`, as `useRowOnScreen` does.
 */
function useOnScreenUnderHeads(item: RefObject<HTMLElement | null>, focused: boolean): () => void {
  const placeByRule = useRowOnScreen(item, focused);
  const pressed = useRef(false);
  useLayoutEffect(() => {
    const element = item.current;
    if (!focused || element === null) return;
    const place = () => {
      if (!pressed.current) clearOfHeads(element);
    };
    place();
    if (typeof ResizeObserver === "undefined") return;
    const grown = new ResizeObserver(place);
    grown.observe(element);
    return () => grown.disconnect();
  }, [focused, item]);
  return () => {
    placeByRule();
    pressed.current = true;
    const ended = new AbortController();
    const release = () => {
      pressed.current = false;
      ended.abort();
    };
    window.addEventListener("pointerup", release, { capture: true, signal: ended.signal });
    window.addEventListener("pointercancel", release, { capture: true, signal: ended.signal });
  };
}

/** Scrolls the page until `element` stands below its view's column heads, where its top is
 *  under them; nothing where the heads are not drawn. */
function clearOfHeads(element: HTMLElement): void {
  const heads = element
    .closest("[data-spec-body]")
    ?.querySelector("[data-spec-head]")
    ?.getBoundingClientRect();
  if (heads === undefined || heads.height === 0) return;
  const top = element.getBoundingClientRect().top;
  if (top < heads.bottom) window.scrollBy({ top: top - heads.bottom - HEADS_CLEARANCE });
}

/** The key `event` is as one of the keymap's single-key bindings reads it - a letter in lower
 *  case - or undefined where the keymap would read it otherwise: with a modifier held, in an
 *  input method's composition, or as the second key of a chord it is reading (`t k`). */
function bareKey(event: KeyboardEvent): string | undefined {
  if (
    event.defaultPrevented ||
    event.nativeEvent.isComposing ||
    event.altKey ||
    event.ctrlKey ||
    event.metaKey ||
    event.shiftKey ||
    appKeymap.pending() !== ""
  ) {
    return undefined;
  }
  return event.key.length === 1 ? event.key.toLowerCase() : event.key;
}

/** The colour each mark is drawn in, which repeats what its shape and the legend's word say
 *  (R6): met in the passed green, not met and no check in the failed red, unknown quiet. */
const MARK_TONES: Readonly<Record<SpecMark, string>> = {
  met: pageStateText.passed,
  no_check: pageStateText.failed,
  not_met: pageStateText.failed,
  unknown: pageStateText.quiet,
};

/** The four marks in the order the legend draws them. */
const MARK_ORDER: readonly SpecMark[] = ["met", "not_met", "unknown", "no_check"];

/** The cell's first line, which says who decided: an agent, a person, or the reader (R34). */
const SAID_WORDS: Readonly<Record<SpecSaid, string>> = {
  agent: SPEC_WORDS.said_agent,
  person: SPEC_WORDS.said_person,
  you: SPEC_WORDS.said_you,
};

/** The shape of a mark: how a statement stands, or that no check serves it. */
function markToken(mark: SpecMark): string {
  return mark === "no_check" ? GLYPH_TOKENS.no_check : GLYPH_TOKENS.statements[mark];
}

/** The word of a mark, in the Verification filter's own words for how a statement stands. */
function markWord(mark: SpecMark): string {
  return mark === "no_check" ? SPEC_WORDS.no_check : VERIFICATION_WORDS.states[mark];
}

/** What each mark means, above the columns: a shape and the word for it, since neither colour
 *  nor shape alone tells a reader what one says (R6). */
function Legend(): ReactNode {
  return (
    <p
      className={`mb-1 ml-[300px] flex flex-wrap gap-[14px] text-xs leading-5 max-[901px]:ml-0 ${pageQuietText}`}
      data-spec-legend=""
    >
      {MARK_ORDER.map((mark) => (
        <span
          className="inline-flex items-center gap-[5px]"
          data-spec-legend-mark={mark}
          key={mark}
        >
          <NodeGlyph className={`size-3 shrink-0 ${MARK_TONES[mark]}`} token={markToken(mark)} />
          <span {...pageChrome}>{markWord(mark)}</span>
        </span>
      ))}
    </p>
  );
}

/** Who made something, by a name and a shape, as every other row of the page draws an author
 *  (R9): never an identifier, and an agent by its role's word, as a row names one (ruling 6,
 *  `NodeRow.tsx`). Where the model holds no author it draws nothing: only the focused cell says
 *  so (`UnknownAuthor`, reading (c) of dispatch://INFRA-3939's plan). */
function Author({ actor }: { actor: Actor | undefined }): ReactNode {
  const agentText = useAgentText();
  const kind = authorKind(actor);
  if (kind === "unknown") return null;
  const name = agentText.author(actor);
  return (
    <span className="inline-flex items-center gap-1" data-spec-author="">
      <NodeGlyph className="size-3 shrink-0" token={GLYPH_TOKENS.authors[kind]} />
      <span {...authorText}>{kind === "agent" ? agentRoleWord(name) : name}</span>
    </span>
  );
}

/** What the focused cell says where the model holds no author for its decision (R5), in the
 *  page's own words and with the shape of an author nobody recorded, as a row's opened body says
 *  it (`NodeRow.tsx`). */
function UnknownAuthor(): ReactNode {
  return (
    <span className="inline-flex items-center gap-1" data-spec-author="">
      <NodeGlyph className="size-3 shrink-0" token={GLYPH_TOKENS.authors.unknown} />
      <span {...pageChrome}>{CONTROL_LABELS.unknown_author}</span>
    </span>
  );
}

/** When something was made, by its day. */
function Day({ at }: { at: string }): ReactNode {
  return (
    <span className="whitespace-nowrap" {...authorText}>
      {shortDay(at)}
    </span>
  );
}

/** Words quoted from a person or an agent, between the page's own curly quotes. */
function Quote({ words }: { words: string }): ReactNode {
  return (
    <>
      <span {...pageChrome}>“</span>
      <span {...authorText}>{words}</span>
      <span {...pageChrome}>”</span>
    </>
  );
}

/** Where the link to a decision goes: its node's own address, where the model holds one. */
function routeOf(decision: SpecDecision | undefined): DispatchReferenceRoute | undefined {
  return decision?.reference === undefined ? undefined : parseNodeReference(decision.reference);
}

/** One part of a line `dotted` draws. */
interface LinePart {
  /** Whether it draws anything: false for an `Author` of nobody, which draws nothing. */
  readonly drawn?: boolean;
  readonly key: string;
  /** What it draws; `null` where the model holds nothing for it. */
  readonly part: ReactNode;
}

/** The parts of one line with a middle dot before each drawn part that follows another, so a
 *  part that draws nothing leaves no dot behind. */
function dotted(parts: readonly LinePart[]): ReactNode {
  const drawn = parts.map(({ drawn: shown, part }) => shown ?? part !== null);
  return parts.map(({ key, part }, place) => (
    <Fragment key={key}>
      {drawn[place] === true && drawn.slice(0, place).includes(true) ? (
        <span {...pageChrome}> · </span>
      ) : null}
      {part}
    </Fragment>
  ));
}

/** The statements under one parent: that parent's own line as the group's heading, then one row
 *  of the grid per run of statements one decision shaped. */
function Group({
  context,
  focused,
  group,
  known,
  marked,
  onCellFocus,
  onFocus,
  openCell,
  report,
  tabStop,
}: {
  context: ReadonlySet<string>;
  focused: string | undefined;
  group: SpecGroup;
  /** Whether the Spec view read the decisions come from and the asks read have both answered,
   *  which is when a run with no decision can say that none shaped it. */
  known: boolean;
  /** Whether the reads a mark comes from hold an answer, which is when a statement can carry
   *  how it stands. */
  marked: boolean;
  /** Takes a focus inside a run's cell, with the decision the cell draws, and the run whose
   *  cell draws its words whole where the cell itself has the focus. */
  onCellFocus: (
    cell: HTMLElement,
    decision: SpecDecision | undefined,
    open: string | undefined
  ) => void;
  /** Takes a focus inside a statement's row. */
  onFocus: (row: HTMLElement, node: TreeNode) => void;
  /** The run whose cell draws its decision whole, by its first statement. */
  openCell: string | undefined;
  /** What a key reported, and the statement it was pressed on. */
  report?: { readonly line: string; readonly row: string };
  tabStop: string | undefined;
}): ReactNode {
  return (
    <section
      className="mt-[18px] min-w-0"
      data-parent-id={group.parent?.id}
      data-spec-group=""
      role="presentation"
    >
      {group.parent === undefined ? null : <GroupHeading parent={group.parent} />}
      {group.runs.map((run) => (
        <div
          className="grid min-w-0 grid-cols-[280px_minmax(0,1fr)] gap-x-5 max-[901px]:grid-cols-[minmax(0,1fr)]"
          data-spec-run=""
          key={run.statements[0]?.node.id ?? ""}
          role="presentation"
        >
          <DecisionCell
            decision={run.decision}
            focused={openCell !== undefined && openCell === run.statements[0]?.node.id}
            known={known}
            onFocus={(cell, itself) =>
              onCellFocus(cell, run.decision, itself ? run.statements[0]?.node.id : undefined)
            }
            parent={group.parent}
            statements={run.statements}
          />
          <div className="flex min-w-0 flex-col" role="presentation">
            {run.statements.map((statement) => (
              <Statement
                context={context.has(statement.node.id)}
                decision={run.decision}
                focused={focused === statement.node.id}
                key={statement.node.id}
                marked={marked}
                onFocus={(row) => onFocus(row, statement.node)}
                {...(report?.row === statement.node.id ? { report: report.line } : {})}
                statement={statement}
                tabStop={tabStop === statement.node.id}
              />
            ))}
          </div>
        </div>
      ))}
    </section>
  );
}

/** The node a group of statements hangs under, which the filter draws as no row (R2): its kind,
 *  who made it and when, and its first line. */
function GroupHeading({ parent }: { parent: SpecParent }): ReactNode {
  return (
    <div
      className="mb-1 ml-[300px] flex min-w-0 flex-col pl-[26px] max-[901px]:ml-0 max-[901px]:pl-0"
      data-spec-group-heading=""
      role="presentation"
    >
      <p className={`min-w-0 text-xs leading-[18px] [overflow-wrap:anywhere] ${pageQuietText}`}>
        {dotted([
          { key: "kind", part: <span {...pageChrome}>{NODE_TYPE_NAMES[parent.type]}</span> },
          { drawn: parent.by !== undefined, key: "author", part: <Author actor={parent.by} /> },
          { key: "day", part: parent.at === undefined ? null : <Day at={parent.at} /> },
        ])}
      </p>
      <p
        className={`min-w-0 text-[15px] leading-6 font-semibold break-words ${pageInkText}`}
        // A parent with no words is named by its kind's word, which is the page's own.
        {...(parent.line === NODE_TYPE_NAMES[parent.type] ? pageChrome : authorText)}
      >
        {parent.line}
      </p>
    </div>
  );
}

/**
 * The decision that shaped a run of statements, in the left column: the words it came from,
 * where they came from and when, and a link to the whole of it (R5). The bracket at its
 * trailing edge spans the statements it shaped, in the decisions' violet, dashed where an agent
 * decided; a run no decision shaped says so in words, once the Spec view read the
 * decisions come from has answered, and says nothing before that or where that read failed
 * with none, since it does not know.
 *
 * A person's words are quoted, their first sentence in plain words; an agent's are drawn whole
 * as a row draws a node's words (`RowText`), each reference by its name, and cut as an agent's
 * are at six lines (ruling 1). The focus on the cell draws a person's whole answer character
 * for character under its first sentence (R5: "The focus on the line draws the whole answer word
 * for word"), an agent's words whole, and "Author unknown" where the model holds no author;
 * `w` on a statement is how a keyboard reader puts it there. The focused cell's words are placed
 * on screen and below the column heads (`useOnScreenUnderHeads`): they are what it draws, and
 * they stay at the top of a run taller than the screen while the cell's own box runs past it.
 */
function DecisionCell({
  decision,
  focused,
  known,
  onFocus,
  parent,
  statements,
}: {
  decision: SpecDecision | undefined;
  /** Whether the cell itself has the focus, which draws its words whole (`SpecFilterBody`'s
   *  `openCell`). */
  focused: boolean;
  known: boolean;
  /** Takes a focus anywhere in the cell, which no statement holds any longer: the cell, and
   *  whether the focus is on the cell itself. */
  onFocus: (cell: HTMLElement, itself: boolean) => void;
  parent: SpecParent | undefined;
  statements: readonly SpecStatement[];
}): ReactNode {
  const words = useRef<HTMLDivElement | null>(null);
  const press = useOnScreenUnderHeads(words, focused);
  const rule = decision === undefined ? pageHairBorder : "border-page-decision";
  const cap = decision === undefined ? pageHairBg : pageKindBullet.decision;
  // The question the words answer, unless the statements hang under it already: there the
  // group's heading is that question, and the two are one line (R5).
  const question =
    decision?.question === undefined || decision.id === parent?.id ? undefined : decision.question;
  return (
    <div
      aria-level={1}
      className={`relative self-stretch rounded-l-[4px] border-r-2 pt-1 pr-3.5 pb-2 pl-0.5 text-xs leading-[18px] max-[901px]:my-1.5 max-[901px]:rounded-none max-[901px]:border-r-0 max-[901px]:border-l-2 max-[901px]:py-0.5 max-[901px]:pr-0 max-[901px]:pl-2.5 ${
        decision?.said === "agent" ? "border-dashed" : ""
      } ${rule} ${pageMutedText}`}
      data-decision-id={decision?.id}
      // The node the cell is drawn as, which Details draws while the cell has the focus: for a
      // person's decision, drawn by their answer, the section of `decisions.md` that names it, or
      // the question itself where its `shaped` ties shaped the run.
      data-node-id={decision?.node ?? decision?.id}
      data-said={decision?.said}
      data-spec-cell=""
      data-spec-statements={statements.map((statement) => statement.node.id).join(" ")}
      // An item of the outline, as the statements beside it are: it is where the focus goes
      // from a statement (R5's `w`), and the prototype's own cell takes the focus the same way.
      // Its words are drawn whole only while the cell itself has the focus, never a control
      // inside it: `f` and a press put the focus on its link, and words growing around the link
      // would move it from where the reader found it.
      onFocus={(event) => onFocus(event.currentTarget, event.target === event.currentTarget)}
      onPointerDown={press}
      role="treeitem"
      tabIndex={-1}
    >
      <span
        aria-hidden="true"
        className={`absolute top-0 -right-0.5 h-0.5 w-2 max-[901px]:hidden ${cap}`}
      />
      <span
        aria-hidden="true"
        className={`absolute -right-0.5 bottom-0 h-0.5 w-2 max-[901px]:hidden ${cap}`}
      />
      <div
        className="sticky top-[calc(var(--issue-identity-height,0px)+44px)] flex min-w-0 flex-col gap-[3px] max-[901px]:static"
        ref={words}
      >
        {decision === undefined ? (
          known ? (
            <p data-spec-nothing="" {...pageChrome}>
              {statements.length === 1
                ? EMPTY_STATE_LINES.spec_no_decision_one
                : EMPTY_STATE_LINES.spec_no_decision_many}
            </p>
          ) : null
        ) : (
          <>
            <p className={`font-semibold ${pageDecisionText}`} data-spec-said="" {...pageChrome}>
              {SAID_WORDS[decision.said]}
            </p>
            {decision.words === undefined ? (
              <p
                className={`min-w-0 text-sm leading-[21px] [overflow-wrap:anywhere] ${pageInkText}`}
                data-spec-quote=""
              >
                <Quote words={decision.quote} />
              </p>
            ) : (
              // A block, since a text drawn from its reading holds paragraphs, lists and links.
              <div className="flex min-w-0 flex-col [overflow-wrap:anywhere]" data-spec-quote="">
                <CutWords className={`text-sm leading-[21px] ${pageInkText}`} whole={focused}>
                  <RowText
                    markdown={decision.words.markdown}
                    source={decision.words.source}
                    tree={decision.words.tree}
                  />
                </CutWords>
              </div>
            )}
            {focused && decision.whole !== undefined ? (
              <p
                className={`min-w-0 text-sm leading-[21px] whitespace-pre-wrap [overflow-wrap:anywhere] ${pageInkText}`}
                data-spec-whole-answer=""
                {...authorText}
              >
                {decision.whole}
              </p>
            ) : null}
            <p className={`min-w-0 [overflow-wrap:anywhere] ${pageQuietText}`} data-spec-source="">
              {dotted([
                {
                  key: "question",
                  part: question === undefined ? null : <span {...authorText}>{question}</span>,
                },
                {
                  drawn: decision.by !== undefined || focused,
                  key: "author",
                  part:
                    decision.by === undefined && focused ? (
                      <UnknownAuthor />
                    ) : (
                      <Author actor={decision.by} />
                    ),
                },
                { key: "day", part: decision.at === undefined ? null : <Day at={decision.at} /> },
                {
                  key: "chose",
                  part:
                    decision.chose.length === 0 ? null : (
                      <>
                        <span {...pageChrome}>{SPEC_WORDS.chose} </span>
                        <span {...authorText}>{decision.chose.join(", ")}</span>
                      </>
                    ),
                },
                {
                  key: "state",
                  part:
                    decision.state === undefined ? null : (
                      <span data-node-state={decision.state} {...pageChrome}>
                        {nodeStateWord(decision.state)}
                      </span>
                    ),
                },
                {
                  key: "unsure",
                  part: decision.unsure ? (
                    <span data-node-unsure="" {...pageChrome}>
                      {CONTROL_LABELS.unsure}
                    </span>
                  ) : null,
                },
              ])}
            </p>
            <WholeOf decision={decision} />
          </>
        )}
      </div>
    </div>
  );
}

/** The link to the whole of a decision: the answer a person wrote, or the decision itself. It
 *  goes to the node's own address, which is where every other link of this page goes (`Chip`),
 *  and is out of the tab order as every control inside a row is (R9). */
function WholeOf({ decision }: { decision: SpecDecision }): ReactNode {
  const route = routeOf(decision);
  if (route === undefined) return null;
  return (
    <Link
      className={`dispatch-inline-link self-start text-left font-semibold underline decoration-page-hair underline-offset-[3px] ${pageInkText}`}
      data-spec-whole=""
      tabIndex={-1}
      to={buildReferencePath(route)}
      {...pageChrome}
    >
      {decision.type === "ask" ? SPEC_WORDS.whole_answer : SPEC_WORDS.whole_decision}{" "}
      <span aria-hidden="true">›</span>
    </Link>
  );
}

/**
 * One statement: its bullet, its words with the chips it carries after the last of them, the
 * line naming the decision that shaped it, and its mark at the right of the row (R5, R6, R17,
 * R34). Its words are cut as an agent's are on every row of the page, at six lines where more
 * than 20 words would be hidden, with "more · N words" opening the rest in place (ruling 1 and
 * ruling 6 of build-rulings-md: a statement of twenty lines opened every view on a wall of text);
 * the focused statement draws them whole. The row draws its own box rather than `CutWords`, so
 * its chips stand after its last word while the words are whole, and beside "more" while they
 * are cut, as a row of the outline draws them (departure 3 of dispatch://INFRA-3939's plan).
 */
function Statement({
  context,
  decision,
  focused,
  marked,
  onFocus,
  report,
  statement,
  tabStop,
}: {
  context: boolean;
  decision: SpecDecision | undefined;
  focused: boolean;
  /** Whether the reads its mark comes from hold an answer. */
  marked: boolean;
  /** Takes a focus anywhere in the row, once a press that gave it is over. */
  onFocus: (row: HTMLElement) => void;
  /** What a key pressed on this statement reported when it had nowhere to go. */
  report?: string;
  statement: SpecStatement;
  tabStop: boolean;
}): ReactNode {
  const node = statement.node;
  const row = useRef<HTMLDivElement | null>(null);
  const words = useRef<HTMLDivElement | null>(null);
  const press = useOnScreenUnderHeads(row, focused);
  const clamp = useClamp(words, true);
  useOutOfTabOrder(words, true);
  const [opened, setOpened] = useState(false);
  const clamped = clamp !== undefined && !opened && !focused;
  // "more · N words" opens the rest of cut words and closes it again; the focused statement
  // draws its words whole, so it draws no such control.
  const more = clamp !== undefined && !focused;
  // Every gap and chip on the statement but the gap its mark already says: "No check" is the
  // mark at the right of the row and the word for it in the legend (R17), and a chip beside it
  // would say it twice. The rest are drawn as the outline draws them (R4, R10).
  const edged = { ...node, gaps: node.gaps.filter((gap) => gap !== "no_check") };
  const edges = edged.alsoUnder.length > 0 || edged.chips.length > 0 || edged.gaps.length > 0;
  // The chips at the end of the words, on their line, as a row of the outline draws its trail
  // (`NodeRow.tsx`, ruling 5); where the words are cut they stand beside "more" instead, so a
  // cut never hides one.
  const trail = edges ? (
    <span
      className={`ml-1.5 inline-flex max-w-full flex-wrap items-baseline gap-x-2 align-baseline text-xs leading-6 ${pageQuietText}`}
      data-node-trail=""
    >
      <NodeEdges node={edged} />
    </span>
  ) : null;
  return (
    <div
      aria-level={1}
      className={`grid min-w-0 grid-cols-[18px_minmax(0,1fr)_26px] items-start gap-x-2 rounded-md py-[3px] pr-1.5 outline-none max-[761px]:grid-cols-[16px_minmax(0,1fr)_22px] max-[761px]:gap-x-1.5 ${
        focused ? pageFocusedRow : ""
      }`}
      data-node-context={context ? "" : undefined}
      data-node-id={node.id}
      // The attributes every row of the outline carries, so the page's own readers find a
      // statement of this body as they find a row of the tree (`e2e/issue-page.ts`'s
      // `drawnRows` and `nodeRow`), and the outline's keys walk it (`useTreeKeymap.ts`).
      data-node-row=""
      data-node-type={node.type}
      data-spec-mark={marked ? statement.mark : undefined}
      data-spec-statement=""
      // Any focus inside the row is the reader on this statement - a chip or a link of its
      // words as much as the row itself - as a row of the outline takes it (`NodeRow.tsx`), and
      // only once a press that gave it is over (`focusItem`).
      onFocus={(event) => onFocus(event.currentTarget)}
      onPointerDown={press}
      ref={row}
      role="treeitem"
      tabIndex={tabStop ? 0 : -1}
    >
      <span className="flex h-6 w-[18px] items-center justify-center max-[761px]:w-4">
        <i
          aria-hidden="true"
          className={`block size-[7px] rounded-full ${
            context ? pageKindBullet.context : pageKindBullet.statement
          }`}
        />
      </span>
      <div className="flex min-w-0 flex-col">
        <div
          className={`relative text-[15px] leading-6 ${context ? pageQuietText : pageWordsText} ${
            clamped ? "overflow-clip" : ""
          }`}
          data-node-words=""
          ref={words}
          style={clamped ? { maxHeight: clamp.height } : undefined}
        >
          <RowText markdown={node.text} source={node.source} tree={node.tree} />
          {/* A direct child of the words' box, which `styles.css` reads to draw the words
              inline, so the trail follows the last word. */}
          {clamped ? null : trail}
          {clamped && !clamp.pictures ? (
            <span
              aria-hidden="true"
              className="pointer-events-none absolute inset-x-0 bottom-0 h-8 bg-[linear-gradient(transparent,var(--color-page-paper))]"
            />
          ) : null}
        </div>
        {more ? (
          <p className="flex min-w-0 flex-wrap items-baseline">
            {/* A press on it leaves the focus where it is: focusing the statement would draw the
                words whole under the pointer and move the control before the press ends. */}
            <button
              className={`dispatch-inline-link text-xs leading-[22px] font-medium underline decoration-page-hair underline-offset-[3px] ${pageQuietText}`}
              data-node-more=""
              onClick={() => setOpened((held) => !held)}
              onMouseDown={(event) => event.preventDefault()}
              tabIndex={-1}
              type="button"
              {...pageChrome}
            >
              {opened ? (
                CONTROL_LABELS.read_less
              ) : (
                <>
                  {CONTROL_LABELS.read_more} ·{" "}
                  <span {...authorText}>{clamp.hidden.toLocaleString()}</span>{" "}
                  {CONTROL_LABELS.read_more_words}
                </>
              )}
            </button>
            {clamped ? trail : null}
          </p>
        ) : null}
        <Because decision={decision} statement={statement} />
        {report === undefined ? null : (
          <p
            className={`text-xs leading-5 ${pageMutedText}`}
            data-row-report=""
            role="status"
            {...pageChrome}
          >
            {report}
          </p>
        )}
      </div>
      {/* No mark until its reads hold an answer; the row's third column keeps its width, so
          the row does not move when the mark comes. */}
      {marked ? (
        <NodeGlyph
          className={`mx-auto mt-[5px] size-3.5 shrink-0 ${MARK_TONES[statement.mark]}`}
          token={markToken(statement.mark)}
        />
      ) : null}
    </div>
  );
}

/** A row the filter kept that is no statement: a node an address named, which outranks the
 *  filter (R20). It is drawn by its kind and its words in the statements' column, with no mark
 *  and no decision beside it, so nothing the filter kept is lost (R19). */
function OtherRow({
  focused,
  node,
  onFocus,
  tabStop,
}: {
  focused: boolean;
  node: TreeNode;
  /** Takes a focus anywhere in the row, once a press that gave it is over. */
  onFocus: (row: HTMLElement) => void;
  tabStop: boolean;
}): ReactNode {
  const row = useRef<HTMLDivElement | null>(null);
  const press = useOnScreenUnderHeads(row, focused);
  return (
    <div
      aria-level={1}
      className={`mt-[18px] ml-[300px] min-w-0 rounded-md py-[3px] pr-1.5 pl-[26px] outline-none max-[901px]:ml-0 max-[901px]:pl-0 ${
        focused ? pageFocusedRow : ""
      }`}
      data-node-id={node.id}
      data-node-row=""
      data-node-type={node.type}
      data-spec-other=""
      // Any focus inside the row is the reader on it, as on a statement.
      onFocus={(event) => onFocus(event.currentTarget)}
      onPointerDown={press}
      ref={row}
      role="treeitem"
      tabIndex={tabStop ? 0 : -1}
    >
      <p className={`text-xs leading-5 ${pageQuietText}`} {...pageChrome}>
        {NODE_TYPE_NAMES[node.type]}
      </p>
      <CutWords className={`text-[15px] leading-6 ${pageWordsText}`} whole={focused}>
        <RowText markdown={node.text} source={node.source} tree={node.tree} />
      </CutWords>
    </div>
  );
}

/**
 * The one line under a statement that names the decision that shaped it (R5): the words it
 * came from, as the link to the whole of that decision, who decided and when, and then every
 * other decision that informed it, each a link to it. A statement no decision shaped draws no
 * line here: its cell says so beside it (R35). The quote is plain words, an agent's its first
 * line: the link is one line, and a text drawn with its own links stands in none (departure 2
 * of dispatch://INFRA-3939's plan).
 */
function Because({
  decision,
  statement,
}: {
  decision: SpecDecision | undefined;
  statement: SpecStatement;
}): ReactNode {
  if (decision === undefined && statement.alsoShapedBy.length === 0) return null;
  const route = routeOf(decision);
  const quoted = decision === undefined ? null : <Quote words={decision.quote} />;
  return (
    <p
      className={`mb-0.5 min-w-0 text-xs leading-[18px] [overflow-wrap:anywhere] ${pageQuietText}`}
      data-spec-because=""
    >
      {decision === undefined ? null : (
        <>
          <span {...pageChrome}>{SPEC_WORDS.because} </span>
          {route === undefined ? (
            quoted
          ) : (
            // Out of the tab order, as every control inside a row is (R9): `f` steps to it.
            <Link
              className={INLINE_LINK}
              data-spec-because-link=""
              tabIndex={-1}
              to={buildReferencePath(route)}
            >
              {quoted}
            </Link>
          )}
          {decision.by === undefined && decision.at === undefined ? null : (
            <span {...pageChrome}> — </span>
          )}
          {dotted([
            {
              drawn: decision.by !== undefined,
              key: "author",
              part: <Author actor={decision.by} />,
            },
            { key: "day", part: decision.at === undefined ? null : <Day at={decision.at} /> },
          ])}
        </>
      )}
      {statement.alsoShapedBy.length === 0 ? null : (
        <>
          <span {...pageChrome}>
            {decision === undefined ? "" : " · "}
            {SPEC_WORDS.informed_by}{" "}
          </span>
          {statement.alsoShapedBy.map((informed, place) => (
            <Fragment key={informed.id}>
              {place === 0 ? null : <span {...pageChrome}>, </span>}
              <Informed decision={informed} />
            </Fragment>
          ))}
        </>
      )}
    </p>
  );
}

/** One other decision that shaped the statement, named by its own words as a chip names its
 *  target (`chipWords`), as a link to it, then when it was made (reading (c) of
 *  dispatch://INFRA-3939's plan): the model holds no short name for a decision, and a block id
 *  is an identifier (R22). A decision with no words is named by its kind's word, so no link is
 *  empty (decision 66). */
function Informed({ decision }: { decision: SpecDecision }): ReactNode {
  const route = routeOf(decision);
  const words = chipWords(decision.quote);
  const named =
    words === "" ? (
      <span {...pageChrome}>{NODE_TYPE_NAMES[decision.type]}</span>
    ) : (
      <span {...authorText}>{words}</span>
    );
  return (
    <>
      {route === undefined ? (
        <span data-spec-informed="">{named}</span>
      ) : (
        <Link
          className={INLINE_LINK}
          data-spec-informed=""
          tabIndex={-1}
          to={buildReferencePath(route)}
        >
          {named}
        </Link>
      )}
      {decision.at === undefined ? null : (
        <>
          <span {...pageChrome}> · </span>
          <Day at={decision.at} />
        </>
      )}
    </>
  );
}
