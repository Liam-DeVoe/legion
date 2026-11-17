import {
  type FocusEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useNavigate } from "react-router-dom";

import type { Artifact, IssueDetails } from "../../../api/types";
import { QueryError } from "../../../components/QueryError";
import {
  focusVisibleRing,
  pageInkText,
  pageMutedText,
  pageQuietText,
  textMutedOnCanvas,
} from "../../../theme/classes";
import { useAgentText } from "../../refs/AgentName";
import { useSetFocusedNode } from "../focused-node";
import { focusedNodeOf, type OutlineRequest, useReleaseFocus } from "../IssueTree";
import { documentOfNode } from "../issue-artifacts";
import { lineWords, type TreeNode } from "../issue-tree";
import { chipWords } from "../node-row-chip";
import { useTakeOutlineRequest } from "../outline-request";
import { threadMarks } from "../outline-thread-read";
import { useOutlineWriter } from "../outline-writer";
import { authorText, pageChrome } from "../page-structure";
import {
  ANSWER_LINES,
  CONTROL_LABELS,
  DECISIONS_WORDS,
  isReader,
  PRESET_NAMES,
} from "../page-words";
import type { DrawnRow } from "../tree-fold";
import { nodesInOrder } from "../tree-keys";
import { useTreeKeymap } from "../useTreeKeymap";
import { DecisionsRow } from "./DecisionsRow";
import { useDecisionsEntries } from "./decisions-entries";
import {
  conversationPath,
  DECISIONS_PART_ORDER,
  type DecisionsEntry,
  type DecisionsPart,
  type NamedEntry,
  waitingOrder,
} from "./decisions-parts";
import { entryStanding } from "./entry-standing";

/** The three parts drawn under a heading of their own, in the view's order; the fourth, the
 *  foot, is headed by the control that unfolds it. */
const HEADED = ["open_questions", "review", "yours"] as const satisfies readonly DecisionsPart[];

/** What a key that folds, zooms or opens a branch does here, where the list holds none. */
const NOTHING = (): void => {};

/**
 * The Decisions filter's body (dispatch://INFRA-1196 R2, R9, R22, R35, the picture
 * `page-v3-decisions-1440.png`, the prototype's `.decs`).
 *
 * It is one list in four parts, each under its own heading and in this order: the open
 * questions; the decisions that wait for the reader's review, each with Keep and Change; the
 * reader's decisions, newest first, where another person's entry stands too, with that person's
 * name on its row; and, folded at the foot, the agents' decisions that no statement rests on.
 * Which entry stands in which part is `decisions-parts.ts`, which is pure, read through the seam
 * (`decisions-entries.ts`); this draws it. A part that holds nothing is not drawn, and no
 * sentence explains the parts: their headings are the whole of it (R22). Before the parts
 * stands the node an address names where it is neither a question nor a decision, a comment
 * say, as a row with no heading and no count: the address outranks the view (`presets.ts`),
 * and the four parts stay all the list says. It is drawn the same at every width: the rows are
 * the outline's own (`DecisionsRow.tsx`).
 *
 * The list is one landmark named by the filter, and inside it answers the WAI-ARIA tree pattern
 * as the outline does, under the outline's own name, so a reader and the browser suite find one
 * tree on the page whatever filter is on: each part is a group named by its heading and each
 * entry a row of the tree. The focused row is the list's one tab stop and nothing inside a row
 * takes the tab order (R9); the keys are the outline's own (`useTreeKeymap.ts`): the arrows and
 * `j`/`k` move, `Shift+K` and `Shift+C` press Keep and Change, `a` and `r` answer and reply,
 * `f` steps through a row's links, Enter goes to the entry's node in the conversation, and the
 * key that cycles how much is drawn unfolds the foot. The foot's own control is a plain button
 * in the tab order, as the prototype's is, so Tab reaches it where the foot is all the list
 * holds. The tree element keeps no focus while the list draws a row or the foot: a focus on it
 * goes on to the tab stop, or to the foot's control where the list draws no row, which is how
 * `t d` and a page opened here reach a list that holds only the foot. On an issue the view
 * draws nothing for, with no row and no foot, there is nowhere to pass it, and the tree keeps
 * it. A key with nowhere to go says so at the row it was pressed on, or at the list's tab stop
 * where it was pressed on no row, as the outline's keys do (the tester's F16).
 *
 * The reads are the seam's: the ones the page's tree is built from, under the keys it reads them
 * by (`useIssueTree.ts`), so the body costs no request of its own (R20).
 * The seam says how they stand too: until they answer the body says nothing of the issue's
 * decisions, and where they failed with nothing held it says the decisions could not be read,
 * with a Retry, above the parts.
 */
export function DecisionsFilterBody({
  artifacts,
  emptyLine,
  holdsEscape,
  isClosed,
  issue,
  reader,
  request,
  reveal,
  root,
  view,
  waitingPress,
}: {
  /** Each artifact of the issue by its slug, which is how a decision names the document a Keep
   *  is written against (`issue-artifacts.ts`). */
  artifacts: ReadonlyMap<string, Artifact>;
  /** What the page says where the issue holds no ask and no decision the filter draws. */
  emptyLine: string;
  /** Whether the page takes Escape from a row before the list is left: a filter by words to
   *  clear or a view to turn off comes first in the key table's order
   *  (`slots/view-decisions.tsx`). */
  holdsEscape: boolean;
  /** Whether the issue is closed, which Keep and Change are refused on. */
  isClosed: boolean;
  issue: IssueDetails;
  /** Who is reading, whose own answer carries the ink rule (R34). Another person's decision
   *  stands under "Your decisions" too, with that person's name on its row. */
  reader: string | undefined;
  /** What the page last asked of the outline: go to a node, which the Contents pane's list
   *  asks (`outline-request.tsx`). Each ask is acted on once. */
  request?: OutlineRequest;
  /** The node the address names, which takes the focus once per address (R19). */
  reveal?: string;
  /** The whole tree, which holds each ask and each decision and each statement a tie names. */
  root: TreeNode;
  /** The tree as the Decisions filter leaves it (`presetView`). */
  view: TreeNode;
  /** The press of the header's count of what waits on the reader that turned this filter on
   *  (`waiting.ts`'s `waitingPressOf`): each press puts the focus on the next thing that waits,
   *  in the filter's order (R13, the tester's F12). */
  waitingPress?: number;
}): ReactNode {
  const navigate = useNavigate();
  const parts = useDecisionsEntries({ issue, reader, root, view });
  // The page's one rule for naming a person (`AgentText.author`), which the panel's facts of the
  // focused row name its keeper by: a name, never an id (R9, R22).
  const agentText = useAgentText();
  // Where each thread of the issue is marked read in the reader's Inbox: its newest write's row.
  const threads = useMemo(() => threadMarks(root), [root]);
  // The name each statement takes on a decision's "informs" line: the words its one line is cut
  // from (`lineWords`), its first block's, cut by G's `chipWords` (decision 83), which each row
  // draws as it is, so a statement reads here as the outline's chip for it reads. Where the
  // coverage read sent the server's reading of a statement they are that reading's words, so
  // they draw none of the marks its author typed; and the tree already holds them, so a
  // statement is named with no read of names to wait for. A statement drawn under two parents
  // is one node, so its first placement in the page's reading order names it.
  const statementNames = useMemo<ReadonlyMap<string, string>>(() => {
    const names = new Map<string, string>();
    for (const node of nodesInOrder(root)) {
      if (node.type === "requirement" && !names.has(node.id))
        names.set(node.id, chipWords(lineWords(node)));
    }
    return names;
  }, [root]);
  const setFocusedNode = useSetFocusedNode();
  const writer = useOutlineWriter();
  const body = useRef<HTMLElement | null>(null);
  const tree = useRef<HTMLDivElement | null>(null);
  const footControl = useRef<HTMLButtonElement | null>(null);
  const headings = useId();

  const [focused, setFocused] = useState<string | undefined>(undefined);
  // Where the list's one tab stop rests, which outlives the focus leaving it (R9).
  const [stop, setStop] = useState<string | undefined>(undefined);
  const [pinned, setPinned] = useState<ReadonlySet<string>>(() => new Set());
  const [footShown, setFootShown] = useState(false);
  // The key that cycles how much is drawn: the list as it opens, then everything - the foot
  // unfolded and every body open - then the list as it opens again.
  const [everything, setEverything] = useState(false);
  // The node a key or the page is taking the reader to, once its row is drawn.
  const [wanted, setWanted] = useState<string | undefined>(undefined);
  // What a key reported when it had nowhere to go, and the row it was pressed on, which draws it
  // until the reader moves on (`NodeRow`'s `report`). `row` is unset where the key was pressed on
  // no row - on the foot's control, or with the focus on the page - and the list's tab stop
  // draws it then, as the outline places one (`IssueTree.tsx`); a list with no tab stop draws
  // none. The control's own focus clears a report, so one made while the focus is already on it
  // stays.
  const [report, setReport] = useState<{ readonly line: string; readonly row?: string }>();
  const reportHere = useCallback((line: string) => {
    const row = document.activeElement?.closest<HTMLElement>("[data-node-row]")?.dataset.nodeId;
    setReport(line === "" ? undefined : { line, ...(row === undefined ? {} : { row }) });
  }, []);

  // The rows in the order the page draws them, which is the order the outline's keys walk: the
  // node the address names first, then the parts, then the foot's rows while it is unfolded.
  const drawn = useMemo<readonly (DecisionsEntry | NamedEntry)[]>(
    () => [
      ...parts.named,
      ...parts.open_questions,
      ...parts.review,
      ...parts.yours,
      ...(footShown ? parts.agents : []),
    ],
    [footShown, parts]
  );
  // The rows as the outline's keys read them: the nth is the nth row drawn.
  const rows = useMemo<DrawnRow[]>(
    () => drawn.map((entry) => ({ kind: "node", level: 1, node: entry.node, trail: [] })),
    [drawn]
  );

  // The decision the reader last pressed Keep on, or sent a Change on, on its row here. It
  // holds only while the reader stays on that row: a move to another row and the focus leaving
  // the list each let it go, so it never moves the focus on later. The press is no review of
  // its own: the decision leaving the review list moves the focus on only where it then stands
  // kept by the reader or with their change, so a write the server refused moves nothing.
  const ownReview = useRef<string | undefined>(undefined);
  const takeFocus = useCallback(
    (entry: DecisionsEntry | NamedEntry) => {
      const node = entry.node;
      setFocused(node.id);
      setStop(node.id);
      if (ownReview.current !== node.id) ownReview.current = undefined;
      // A move to a row is the reader going on from what a key reported.
      setReport((held) => (held?.row === node.id ? held : undefined));
      // The panel's facts of the focused node are the outline's, but for how the node stands,
      // which they say as the row reads it, so the two never differ: an entry the row reads no
      // standing for says none, whatever state its node holds.
      const { state: _nodeState, ...facts } = focusedNodeOf(node);
      const state = entryStanding(entry, agentText.author)?.words;
      setFocusedNode(state === undefined ? facts : { ...facts, state });
    },
    [agentText, setFocusedNode]
  );
  // R7 under a press. A press focuses what it lands on as it goes down, before it ends, so the
  // row taking the focus would open its body and draw its acts, and the row the focus left would
  // close its own, while the press is held: every row under the one that closed would move up,
  // a cut answer or decision of the pressed row would be drawn whole and push down what stands
  // under its words, and the click that ends the press would land on something else, so a link
  // or a control of a row the reader was not on would never fire. So the focus a press gives a
  // row is taken once the press is over - after its mouseup and the click that follows it, in a
  // task of its own - and only while that row still holds the focus, as the outline takes it
  // (`IssueTree.tsx`'s `focusRow`); a key's move takes it at once. The press is marked on the
  // whole list, since the foot's rows stand outside the tree element. The latest `takeFocus`
  // takes it, so a render that makes a new one while a press is held does not drop its end.
  // This is a copy of part I's press logic in `IssueTree.tsx`, with that file's private
  // `PRESS_ENDS` (below), which decisions 94 and 141 allow only until part I exports the hook
  // for a press's focus and its `PRESS_ENDS`: once both are under this part's base, this file
  // imports them and deletes its copy.
  const press = useRef<{
    ending?: number;
    focus: DecisionsEntry | NamedEntry | undefined;
    held: boolean;
  }>({ focus: undefined, held: false });
  const takePressed = useRef(takeFocus);
  takePressed.current = takeFocus;
  const focusRow = useCallback(
    (entry: DecisionsEntry | NamedEntry) => {
      if (press.current.held) press.current.focus = entry;
      else takeFocus(entry);
    },
    [takeFocus]
  );
  useEffect(() => {
    const held = press.current;
    const end = () => {
      held.held = false;
      const pending = held.focus;
      held.focus = undefined;
      if (pending === undefined) return;
      if (rowOf(body.current, pending.node.id)?.contains(document.activeElement) === true)
        takePressed.current(pending);
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
  // The focus leaving the list with no press under way, by Tab or a script, lets go at once of
  // what the row opened, what a key reported and the reader's own review. A press on another
  // control of the page keeps them until that press has ended and its click has landed, or until
  // Escape, so nothing moves under the pointer (`useReleaseFocus`, decision 105 as amended). The
  // panel goes back to the issue's own facts as the focus leaves, unless it went to Details,
  // where a reader reads the node's. No row of the list is edited in place.
  const leave = useCallback(() => {
    setFocused(undefined);
    setReport(undefined);
    ownReview.current = undefined;
  }, []);
  const releaseFocus = useReleaseFocus({ editing: false, onLeave: leave });
  // The foot's control is inside the list, so the focus going to it from a row, by Tab or a
  // press, never leaves the list and `releaseFocus` does not act. The focus is off every row
  // all the same, so it does what the list's leave does, and the panel goes back to the issue's
  // own facts.
  const footFocused = useCallback(() => {
    leave();
    setFocusedNode(undefined);
  }, [leave, setFocusedNode]);
  const pin = useCallback((id: string) => {
    setPinned((held) => {
      const next = new Set(held);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  }, []);

  // The part each entry stands in, read over the whole list, the foot's whether it is unfolded
  // or not, in the order the page draws it.
  const partOf = useMemo(() => {
    const parted = new Map<string, DecisionsPart>();
    for (const part of DECISIONS_PART_ORDER) {
      for (const entry of parts[part]) parted.set(entry.node.id, part);
    }
    return parted;
  }, [parts]);
  // Takes the reader to one entry: the foot is unfolded where the entry is one of its own, and
  // the row takes the focus once this render has drawn it.
  const goTo = useCallback(
    (id: string) => {
      if (partOf.get(id) === "agents") setFootShown(true);
      setWanted(id);
    },
    [partOf]
  );
  useEffect(() => {
    if (wanted === undefined) return;
    setWanted(undefined);
    rowOf(body.current, wanted)?.focus();
  }, [wanted]);

  // What the page asked for, each ask acted on once. The page holds what an outline or a view
  // took, so a view drawn again does not act on the last ask twice (`outline-request.tsx`).
  const takeRequest = useTakeOutlineRequest();
  useEffect(() => {
    if (request === undefined || !takeRequest(request)) return;
    if (request.id !== undefined) goTo(request.id);
  }, [goTo, request, takeRequest]);

  // The header's count of what waits on the reader puts the focus on a thing that waits, in
  // this filter's order: each open question whose turn is the reader's, then each decision of
  // the review list (R13). Each press goes to the next of them after the one the last press
  // went to, and round to the first again, so a reader works down what waits with the one
  // control (the tester's F12). The reads that hold it may still be out when the press lands,
  // so the press is taken once the list holds something that waits.
  const pressed = useRef<number | undefined>(undefined);
  const went = useRef<string | undefined>(undefined);
  const waiting = useMemo(() => waitingOrder(parts), [parts]);
  useEffect(() => {
    if (waitingPress === undefined || waitingPress === pressed.current) return;
    if (waiting.length === 0) return;
    pressed.current = waitingPress;
    const next = waiting[(waiting.indexOf(went.current ?? "") + 1) % waiting.length];
    if (next === undefined) return;
    went.current = next;
    goTo(next);
  }, [goTo, waiting, waitingPress]);

  // An address that names an entry puts the focus on it, once per address (R19), and one that
  // names a node of another kind, a comment say, on that node's row, drawn first: the row is
  // not on the page until the reads that hold it have answered, so this looks again on every
  // list the body draws, and unfolds the foot where the entry is one of its own.
  const taken = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (reveal === undefined || taken.current === reveal) return;
    if (!drawn.some((entry) => entry.node.id === reveal)) {
      if (partOf.get(reveal) === "agents") setFootShown(true);
      return;
    }
    const row = rowOf(body.current, reveal);
    if (row === null) return;
    taken.current = reveal;
    row.focus();
  }, [drawn, partOf, reveal]);

  // The tree element takes the focus only to pass it on: to the list's tab stop, else, where
  // the list draws no row, to the foot's control. `t d` focuses the tree where it finds no tab
  // stop in it (`useIssueKeymap.ts`), and a list that holds only the foot has none. On an issue
  // the view draws nothing for there is neither, and the tree keeps the focus, which leaves it
  // in the list. A focus that came up from a row is the row's own. The tab stop is looked for
  // in the whole list, since the foot's rows stand after the tree element and are its rows all
  // the same (`aria-owns`).
  const passFocus = useCallback((event: FocusEvent<HTMLDivElement>) => {
    if (event.target !== event.currentTarget) return;
    const stopRow = body.current?.querySelector<HTMLElement>('[data-node-row][tabindex="0"]');
    (stopRow ?? footControl.current)?.focus();
  }, []);
  // A page opened on a list that holds only the foot has no row for the stage to focus
  // (`slots/stage.tsx`), so the tree takes the focus once, on the stage's own terms: no address
  // names an entry and nothing holds the focus. It goes through the tree, so `t d` and a page
  // opened here reach the foot's control by one path. The reads may answer after the body
  // mounts, so this looks again on every render until it has acted.
  const footReached = useRef(false);
  // Whether the focus on the foot's control is the one this body put there on its own, which
  // the reader has not yet pressed, typed on or left.
  const footPlaced = useRef(false);
  useEffect(() => {
    if (footReached.current || reveal !== undefined) return;
    if (drawn.length > 0 || parts.agents.length === 0) return;
    const active = document.activeElement;
    if (active !== null && active !== document.body) return;
    footReached.current = true;
    tree.current?.focus();
    footPlaced.current = document.activeElement === footControl.current;
  });
  // The tree is built read by read (`useIssueTree.ts`), so a list can hold only the foot until
  // the asks read answers, and then draw its questions. The focus the body put on the foot's
  // control was for a list with no row: once one is drawn it goes to the tab stop, where the
  // stage would have put it, unless the reader has taken the control as their own.
  useEffect(() => {
    if (!footPlaced.current || drawn.length === 0) return;
    footPlaced.current = false;
    if (document.activeElement === footControl.current) tree.current?.focus();
  });
  // Pressing the control, typing on it or leaving it makes the focus the reader's own.
  const footTaken = (): void => {
    footPlaced.current = false;
  };

  // An entry the reader is on can move to another part of the list, and its row is drawn
  // again there, which drops the focus to the page: a decision they kept or asked to change
  // leaves the review list for their own decisions, a question another person answered goes
  // under Your decisions, a decision of the foot that a read ties to a statement comes up to
  // the review list. A reader who kept or asked to change a decision on its row here is working
  // down the review list, so the focus goes to the decision that stood after it, else the one
  // before it (the prototype's `keepDecision`), else to the decision where it now stands. That
  // it was their review is read from the decision the server wrote back - kept by them, or
  // with their change - after their press on its row, so a Keep the server refused and a
  // Change sent empty, which write nothing, are none. An entry that moves by no act of the
  // reader here - a read that says another person kept a decision or answered a question, that
  // a decision was replaced or ties to a statement, or a Change sent from another page - keeps
  // the focus where it now stands: the reader asked to go nowhere, and the focus stays on the
  // entry they were on (R6), the foot unfolded for it where it is one of the foot's own. An
  // entry the reader is on that is drawn nowhere now - another person deleted it, or the words
  // filter took it out of the view - takes its row and the focus away: the focus goes to the
  // entry that stood after it in its part, else the one before it, else the view's first row,
  // else, where the list draws no row, the tree, which hands it to the foot's control or keeps
  // it, so it is never left on the page (decision 110). A reader who has moved somewhere else
  // on the page keeps their place.
  const stood = useRef(partOf);
  useLayoutEffect(() => {
    const before = stood.current;
    stood.current = partOf;
    if (focused === undefined) return;
    const was = before.get(focused);
    const now = partOf.get(focused);
    if (was === undefined || was === now) return;
    const active = document.activeElement;
    if (active !== null && active !== document.body && active.isConnected) return;
    // The press is spent once the decision has left the review list, whatever it wrote.
    const pressedHere = was === "review" && ownReview.current === focused;
    if (pressedHere) ownReview.current = undefined;
    const standing: readonly (DecisionsEntry | NamedEntry)[] = now === undefined ? [] : parts[now];
    const entry = standing.find((held) => held.node.id === focused);
    const reviewed =
      pressedHere &&
      entry?.kind === "decision" &&
      (isReader(entry.keptBy, reader) || isReader(entry.change?.by, reader));
    if (now !== undefined && !reviewed) {
      goTo(focused);
      return;
    }
    const next =
      neighbourOf(focused, was, before, partOf) ?? (now === undefined ? undefined : focused);
    const row =
      next === undefined
        ? body.current?.querySelector<HTMLElement>("[data-node-row]")
        : rowOf(body.current, next);
    if (row !== null && row !== undefined) {
      row.focus();
      return;
    }
    // No row is left to take it: the focus is off every row, so what a row's focus opened goes,
    // as when the foot's control takes it, and the tree holds it or hands it to that control.
    footFocused();
    tree.current?.focus();
  }, [focused, footFocused, goTo, partOf, parts, reader]);

  useTreeKeymap({
    container: body,
    holdsEscape,
    onCycle: () => {
      // Folding the foot takes its rows away, and a focus on one of them would drop to the
      // page: it goes to the foot's control, which stays where the rows were.
      if (everything && focused !== undefined && partOf.get(focused) === "agents")
        footControl.current?.focus({ preventScroll: true });
      setEverything(!everything);
      setFootShown(!everything);
    },
    // Enter on a row goes to its node in the conversation, where the node is edited: this
    // filter draws each entry and edits none (the prototype's `openDecItem`).
    onEdit: (_row, node) => {
      const path = conversationPath(node);
      if (path !== undefined) navigate(path);
    },
    onEditEnd: NOTHING,
    onFold: NOTHING,
    onOpenDeeper: NOTHING,
    onPin: pin,
    onReport: reportHere,
    onReveal: goTo,
    onZoom: NOTHING,
    oneNodeAtATime: false,
    rows,
    // The tree the view left, as the outline's keys read it (`IssueTree.tsx`): `q` and `w` look
    // past the drawn rows, and a node the words filter took out of the view draws no row to go to.
    root: view,
    writer,
    zoom: undefined,
  });

  const first = drawn[0]?.node.id;
  const tabStop =
    stop !== undefined && drawn.some((entry) => entry.node.id === stop) ? stop : first;
  const rowFor = (entry: DecisionsEntry | NamedEntry, part: DecisionsPart): ReactNode => {
    const node = entry.node;
    const id = node.id;
    const place = documentOfNode(id, artifacts);
    const mark = threads.get(id);
    return (
      <DecisionsRow
        body={focused === id || pinned.has(id) || everything}
        {...(place === undefined ? {} : { document: place.artifact })}
        entry={entry}
        focused={focused === id}
        isClosed={isClosed}
        key={id}
        level={part === "agents" ? 2 : 1}
        onFocus={() => focusRow(entry)}
        onPin={() => pin(id)}
        // A Keep pressed or a Change sent is the press over: its click has landed, so the row
        // takes the focus at once rather than once the press's end comes, which the write's
        // answer can come before, and the decision would leave the review list from a row the
        // list did not yet hold the reader on.
        onReviewed={() => {
          ownReview.current = id;
          takeFocus(entry);
        }}
        pinned={pinned.has(id)}
        reader={reader}
        {...(report !== undefined && (report.row ?? tabStop) === id ? { report: report.line } : {})}
        review={part === "review"}
        statementNames={statementNames}
        tabStop={tabStop === id}
        {...(mark === undefined ? {} : { threadRead: mark })}
      />
    );
  };
  const foot = `${headings}-agents`;
  const footRows = `${headings}-agents-rows`;
  return (
    // One landmark named by the filter, which is where a reader who knew the old tab's four
    // sections finds them (R19). It holds the tree's rows and the foot's control, so the keys
    // and the focus are read over the whole of it.
    <section
      aria-label={PRESET_NAMES.decisions}
      className="mx-auto mt-3.5 mb-[120px] flex w-full max-w-[960px] min-w-0 flex-col max-[761px]:mt-2.5"
      data-decisions-body=""
      onBlur={releaseFocus}
      // A press in the list, whose focus a row takes once the press is over (`focusRow`).
      onMouseDown={() => {
        window.clearTimeout(press.current.ending);
        press.current.held = true;
      }}
      ref={body}
    >
      {/* The decisions could not be read: the view holds no answer of the reads they come from,
          and one of them failed (`useDecisionsEntries`' `failed`). It says so as the Spec view
          says it of the same reads, with a Retry, and never that the issue has none. */}
      {parts.failed ? (
        <QueryError message={ANSWER_LINES.decisions_unreadable} onRetry={parts.retry} />
      ) : null}
      {/* The foot's rows stand after its control, outside this element, and are this tree's
          all the same (`aria-owns`), so the page holds one tree. The element itself takes the
          focus only to pass it on (`passFocus`), and only from a script: a press on it outside
          a row, on a part's heading, its count or the gap between two rows, would otherwise
          focus it, as the nearest element that takes the focus, and the focus would go on to
          the tab stop, scroll the page to it and open its body. The outline's tree takes no
          focus from a press either (`IssueTree.tsx`). */}
      <div
        aria-label={CONTROL_LABELS.tree}
        aria-owns={footShown && parts.agents.length > 0 ? footRows : undefined}
        className="flex min-w-0 flex-col"
        onFocus={passFocus}
        onMouseDown={(event) => {
          const target = event.target;
          if (!(target instanceof Element) || target.closest("[data-node-row]") === null)
            event.preventDefault();
        }}
        ref={tree}
        role="tree"
        tabIndex={-1}
      >
        {/* The node the address names where it is neither a question nor a decision stands
            first, at the tree's first level, under no heading and in no part: the address
            outranks the view, and the parts stay four (R1, R6). */}
        {parts.named.map((entry) => rowFor(entry, "named"))}
        {HEADED.map((part) => {
          const entries: readonly DecisionsEntry[] = parts[part];
          if (entries.length === 0) return null;
          return (
            // biome-ignore lint/a11y/useSemanticElements: a tree owns only its rows and groups of rows; a fieldset is neither.
            <div
              aria-labelledby={`${headings}-${part}`}
              className="flex min-w-0 flex-col"
              data-decisions-part={part}
              key={part}
              role="group"
            >
              {/* The part's heading (the prototype's `.dgh`): its name and how many entries
                  it holds. Its name is the group's; a tree holds rows and groups alone, so it
                  is no element with a role of its own. */}
              <div
                className="mt-[22px] mb-1 flex min-w-0 items-baseline gap-2.5 max-[761px]:mt-[18px]"
                data-decisions-heading=""
              >
                <span
                  className={`text-xs leading-[18px] font-semibold tracking-[0.08em] uppercase ${pageInkText}`}
                  id={`${headings}-${part}`}
                  {...pageChrome}
                >
                  {DECISIONS_WORDS.parts[part]}
                </span>
                <span
                  className={`text-xs leading-[18px] ${pageQuietText}`}
                  data-decisions-count=""
                  {...authorText}
                >
                  {entries.length}
                </span>
              </div>
              {entries.map((entry) => rowFor(entry, part))}
            </div>
          );
        })}
      </div>
      {parts.agents.length === 0 ? null : (
        <div className="flex min-w-0 flex-col" data-decisions-part="agents">
          {/* The foot (the prototype's `.minor-t`): one control that says how many decisions
              it holds and whether they are drawn, and unfolds them. It is a plain button in
              the tab order, as the prototype's is, so Tab reaches it where the foot is all the
              list holds; the key that cycles how much is drawn unfolds it too. A press of it
              moves no focus until the click lands: it is no row, so the list does not hold its
              focus until the press is over (`focusRow`), and the focus coming here closes the
              open body of the row that held it, so the control would move up from under the
              pointer before the release, and the click would be lost. */}
          <div className="mt-[22px] mb-1 flex min-w-0 max-[761px]:mt-[18px]" data-decisions-foot="">
            <button
              aria-controls={footShown ? footRows : undefined}
              aria-expanded={footShown}
              className={`dispatch-inline-link rounded py-1 text-left text-sm leading-5 font-medium hover:text-page-ink ${pageMutedText} ${focusVisibleRing}`}
              data-decisions-heading=""
              id={foot}
              onBlur={footTaken}
              onClick={(event) => {
                setFootShown(!footShown);
                event.currentTarget.focus({ preventScroll: true });
              }}
              onFocus={footFocused}
              onKeyDown={footTaken}
              onMouseDown={(event) => event.preventDefault()}
              onPointerDown={footTaken}
              ref={footControl}
              type="button"
              {...pageChrome}
            >
              <span aria-hidden="true">{footShown ? "▾" : "▸"}</span>{" "}
              <span data-decisions-count="" {...authorText}>
                {parts.agents.length}
              </span>{" "}
              {parts.agents.length === 1 ? DECISIONS_WORDS.agents_one : DECISIONS_WORDS.agents_many}
            </button>
          </div>
          {footShown ? (
            // biome-ignore lint/a11y/useSemanticElements: a tree owns only its rows and groups of rows; a fieldset is neither.
            <div
              aria-labelledby={foot}
              className="flex min-w-0 flex-col"
              id={footRows}
              role="group"
            >
              {parts.agents.map((entry) => rowFor(entry, "agents"))}
            </div>
          ) : null}
        </div>
      )}
      {/* The issue holds no question and no decision the view draws. The node an address
          names is no entry of the list, so its row and this line are drawn together. It is
          drawn only once the view knows what the issue holds (`known`): never before its reads
          answer, and never where they failed, which leaves it nothing known. */}
      {parts.known &&
      parts.open_questions.length === 0 &&
      parts.review.length === 0 &&
      parts.yours.length === 0 &&
      parts.agents.length === 0 ? (
        <p className={`py-1 text-sm ${textMutedOnCanvas}`} {...pageChrome}>
          {emptyLine}
        </p>
      ) : null}
    </section>
  );
}

/** The row of node `id` inside `container`, or null where it draws none. */
function rowOf(container: HTMLElement | null, id: string): HTMLElement | null {
  return (
    container?.querySelector<HTMLElement>(`[data-node-row][data-node-id="${CSS.escape(id)}"]`) ??
    null
  );
}

/** What ends a press in the list, as it ends one in the outline: a copy of `IssueTree.tsx`'s
 *  own `PRESS_ENDS`, which that file keeps to itself until part I exports it (decisions 94 and
 *  141). Its mouseup, which the click follows in the same task; the drag a press on a picture
 *  or a link starts instead, which sends no mouseup; a pointer the browser cancelled; and a
 *  key, since a press released outside the window sends no mouseup, and a key's move must
 *  never wait on a press that is over. */
const PRESS_ENDS = ["mouseup", "dragend", "pointercancel", "keydown"] as const;

/** The entry that stood after `id` in `part` as `before` drew the list, else the one before
 *  it, of those that stand in `part` still (`now`); undefined where none does. Each map holds
 *  the entries in the order the page draws them. */
function neighbourOf(
  id: string,
  part: DecisionsPart,
  before: ReadonlyMap<string, DecisionsPart>,
  now: ReadonlyMap<string, DecisionsPart>
): string | undefined {
  const stood = [...before].flatMap(([entry, held]) => (held === part ? [entry] : []));
  const at = stood.indexOf(id);
  const stays = (entry: string) => now.get(entry) === part;
  return stood.slice(at + 1).find(stays) ?? stood.slice(0, at).reverse().find(stays);
}
