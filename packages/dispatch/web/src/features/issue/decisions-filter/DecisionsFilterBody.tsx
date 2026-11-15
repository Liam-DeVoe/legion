import { useQuery } from "@tanstack/react-query";
import {
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

import { api } from "../../../api/client";
import type { Actor, Artifact, IssueDetails } from "../../../api/types";
import {
  pageInkText,
  pageMutedText,
  pageQuietText,
  textMutedOnCanvas,
} from "../../../theme/classes";
import { issueDecisionsQuery } from "../../doc/spec-marks";
import { DETAILS_ATTRIBUTE } from "../../margin/margin-slot";
import { useAgentText } from "../../refs/AgentName";
import { useSetFocusedNode } from "../focused-node";
import type { OutlineRequest } from "../IssueTree";
import { documentOfNode } from "../issue-artifacts";
import { firstLineOf, nodeWords, type TreeNode } from "../issue-tree";
import { threadMarks } from "../outline-thread-read";
import { conversationItem, useOutlineWriter } from "../outline-writer";
import { authorText, pageChrome } from "../page-structure";
import { authorKind, CONTROL_LABELS, nodeStateWord } from "../page-words";
import { treeContext } from "../tree-drafts";
import type { DrawnRow } from "../tree-fold";
import { useTreeKeymap } from "../useTreeKeymap";
import { DecisionsRow, type RowPerson } from "./DecisionsRow";
import {
  conversationPath,
  DECISIONS_WORDS,
  type DecisionsEntry,
  type DecisionsPart,
  decisionsParts,
  firstWaiting,
} from "./decisions-parts";

/** The three parts drawn under a heading of their own, in the filter's order; the fourth, the
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
 * reader's own decisions, newest first; and, folded at the foot, the agents' decisions that no
 * statement rests on. Which entry stands in which part is `decisions-parts.ts`, which is pure;
 * this draws it. A part that holds nothing is not drawn, and no sentence explains the parts:
 * their headings are the whole of it (R22). It is drawn the same at every width: the rows are
 * the outline's own (`DecisionsRow.tsx`), which keep their words and their margin from 320 px.
 *
 * The list answers the WAI-ARIA tree pattern as the outline does, under the outline's own name,
 * so a reader and the browser suite find one tree on the page whatever filter is on: each part
 * is a group named by its heading, each entry a row of the tree, and the foot is a row that
 * holds its entries one level down and says whether they are drawn. The focused row is the
 * list's one tab stop and nothing inside a row takes the tab order (R9); the keys are the
 * outline's own (`useTreeKeymap.ts`): the arrows and `j`/`k` move, `Shift+K` and `Shift+C`
 * press Keep and Change, `a` and `r` answer and reply, `f` steps through a row's links, Enter
 * goes to the entry's node in the conversation, and the key that cycles how much is drawn
 * unfolds the foot.
 *
 * The reads are the ones the page's tree is built from, under the keys it reads them by
 * (`useIssueTree.ts`), so the body costs no request of its own (R20).
 */
export function DecisionsFilterBody({
  artifacts,
  emptyLine,
  holdsEscape,
  isClosed,
  issue,
  onReport,
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
   *  clear or a filter to turn off comes first in the key table's order (`IssueBody.tsx`). */
  holdsEscape: boolean;
  /** Whether the issue is closed, which Keep and Change are refused on. */
  isClosed: boolean;
  issue: IssueDetails;
  /** What a key reported when it had nowhere to go, which the page draws in its own status. */
  onReport: (report: string) => void;
  /** Who is reading, whose own answer carries the ink rule (R34). */
  reader: string | undefined;
  /** What the page last asked of the outline: go to a node, which the Contents pane's list
   *  asks (`IssueBody.tsx`). Each ask is acted on once. */
  request?: OutlineRequest;
  /** The node the address names, which takes the focus once per address (R19). */
  reveal?: string;
  /** The whole tree, which holds each ask and each decision and each statement a tie names. */
  root: TreeNode;
  /** The tree as the Decisions filter leaves it (`presetView`). */
  view: TreeNode;
  /** The press of the header's count of what waits on the reader that turned this filter on
   *  (`waiting.ts`'s `waitingPressOf`): the first thing that waits, in the filter's order,
   *  takes the focus, once per press (R13). */
  waitingPress?: number;
}): ReactNode {
  const navigate = useNavigate();
  const decisions = useQuery(issueDecisionsQuery(issue.key, true)).data;
  const asks = useQuery({
    queryFn: () => api.listIssueAsks(issue.key, "all"),
    queryKey: ["asks", issue.key],
  }).data;
  // How this issue addresses a node, which is the one rule the tree was built with, so a
  // `shaped` tie resolves to the node it names (`tree-drafts.ts`).
  const nodeIdOf = useMemo(() => treeContext({ issue }).nodeIdOf, [issue]);
  const parts = useMemo(
    () => decisionsParts({ asks, decisions, nodeIdOf, root, view }),
    [asks, decisions, nodeIdOf, root, view]
  );
  // Each row's people by the page's one rule for naming one (`AgentText.author`), read once for
  // every row: a name and a shape, never an id (R9, R22).
  const agentText = useAgentText();
  const person = (actor: Actor | undefined): RowPerson => ({
    kind: authorKind(actor),
    name: agentText.author(actor),
    reader: isReader(actor, reader),
  });
  // Where each thread of the issue is marked read in the reader's Inbox: its newest write's row.
  const threads = useMemo(() => threadMarks(root), [root]);
  const setFocusedNode = useSetFocusedNode();
  const writer = useOutlineWriter();
  const body = useRef<HTMLDivElement | null>(null);
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

  const drawn = useMemo(
    () => [
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

  const takeFocus = useCallback(
    (entry: DecisionsEntry, state: string | undefined) => {
      const node = entry.node;
      setFocused(node.id);
      setStop(node.id);
      const item = conversationItem(node);
      setFocusedNode({
        ...(node.author === undefined ? {} : { author: node.author }),
        id: node.id,
        ...(item === undefined ? {} : { item }),
        ...(state === undefined ? {} : { state }),
        title: node.text === "" ? node.id : firstLineOf(nodeWords(node)).line,
        type: node.type,
      });
    },
    [setFocusedNode]
  );
  // The focus leaving the list closes what it opened, and the panel goes back to the issue's
  // own facts, unless it went to Details, where a reader reads the node's (`IssueTree.tsx`).
  const releaseFocus = useCallback(
    (event: { currentTarget: HTMLElement; relatedTarget: EventTarget | null }) => {
      const to = event.relatedTarget;
      if (to instanceof Node && event.currentTarget.contains(to)) return;
      setFocused(undefined);
      if (to instanceof Element && to.closest(`[${DETAILS_ATTRIBUTE}]`) !== null) return;
      setFocusedNode(undefined);
    },
    [setFocusedNode]
  );
  const pin = useCallback((id: string) => {
    setPinned((held) => {
      const next = new Set(held);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  }, []);

  // Takes the reader to one entry: the foot is unfolded where the entry is one of its own, and
  // the row takes the focus once this render has drawn it.
  const footed = useMemo(() => new Set(parts.agents.map((entry) => entry.node.id)), [parts.agents]);
  const goTo = useCallback(
    (id: string) => {
      if (footed.has(id)) setFootShown(true);
      setWanted(id);
    },
    [footed]
  );
  useEffect(() => {
    if (wanted === undefined) return;
    setWanted(undefined);
    rowOf(body.current, wanted)?.focus();
  }, [wanted]);

  // What the page asked for, each ask acted on once.
  const handled = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (request === undefined || request.seq === handled.current) return;
    handled.current = request.seq;
    if (request.id !== undefined) goTo(request.id);
  }, [goTo, request]);

  // The header's count of what waits on the reader puts the focus on the first thing that
  // waits, in this filter's order: an open question whose turn is the reader's, else a decision
  // of the review list (R13). The reads that hold it may still be out when the press lands, so
  // the press is taken once the list holds something that waits.
  const pressed = useRef<number | undefined>(undefined);
  const waiting = firstWaiting(parts);
  useEffect(() => {
    if (waitingPress === undefined || waitingPress === pressed.current) return;
    if (waiting === undefined) return;
    pressed.current = waitingPress;
    goTo(waiting);
  }, [goTo, waiting, waitingPress]);

  // An address that names an entry puts the focus on it, once per address (R19): the row is
  // not on the page until the reads that hold it have answered, so this looks again on every
  // list the body draws, and unfolds the foot where the entry is one of its own.
  const taken = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (reveal === undefined || taken.current === reveal) return;
    if (!drawn.some((entry) => entry.node.id === reveal)) {
      if (footed.has(reveal)) setFootShown(true);
      return;
    }
    const row = rowOf(body.current, reveal);
    if (row === null) return;
    taken.current = reveal;
    row.focus();
  }, [drawn, footed, reveal]);

  // A decision the reader kept leaves the review list for their own decisions, and its row
  // with it, which drops the focus to the page. The reader is working down the list, so the
  // focus goes to the decision that stood after it, else the one before it (the prototype's
  // `keepDecision`), else to the kept decision where it now stands. A reader who has moved
  // somewhere else on the page keeps their place.
  const reviewing = useMemo(() => parts.review.map((entry) => entry.node.id), [parts.review]);
  const reviewed = useRef(reviewing);
  useLayoutEffect(() => {
    const before = reviewed.current;
    reviewed.current = reviewing;
    if (focused === undefined || reviewing.includes(focused)) return;
    const at = before.indexOf(focused);
    if (at === -1) return;
    const active = document.activeElement;
    if (active !== null && active !== document.body && active.isConnected) return;
    const next =
      before.slice(at + 1).find((id) => reviewing.includes(id)) ??
      before
        .slice(0, at)
        .reverse()
        .find((id) => reviewing.includes(id)) ??
      focused;
    rowOf(body.current, next)?.focus();
  }, [focused, reviewing]);

  useTreeKeymap({
    container: body,
    holdsEscape,
    onCycle: () => {
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
    onReport,
    onReveal: goTo,
    onZoom: NOTHING,
    oneNodeAtATime: false,
    rows,
    root,
    writer,
    zoom: undefined,
  });

  const first = drawn[0]?.node.id;
  const tabStop =
    stop !== undefined && drawn.some((entry) => entry.node.id === stop) ? stop : first;
  const rowFor = (entry: DecisionsEntry, part: DecisionsPart): ReactNode => {
    const node = entry.node;
    const id = node.id;
    const place = documentOfNode(id, artifacts);
    const mark = threads.get(id);
    const keeper =
      entry.kind === "decision" && entry.keptBy !== undefined
        ? agentText.author(entry.keptBy)
        : undefined;
    // How the node stands, as the panel's facts of the focused node say it.
    const state =
      entry.kind === "ask"
        ? node.state === undefined
          ? undefined
          : nodeStateWord(node.state)
        : entry.standing === undefined
          ? undefined
          : `${DECISIONS_WORDS.states[entry.standing]}${keeper === undefined ? "" : ` ${keeper}`}`;
    return (
      <DecisionsRow
        {...(node.answer === undefined ? {} : { answerer: agentText.author(node.answer.by) })}
        author={person(node.author)}
        body={focused === id || pinned.has(id) || everything}
        {...(entry.kind === "ask" && entry.declined !== undefined
          ? { decliner: person(entry.declined.by) }
          : {})}
        {...(place === undefined ? {} : { document: place.artifact })}
        entry={entry}
        focused={focused === id}
        isClosed={isClosed}
        {...(keeper === undefined ? {} : { keeper })}
        key={id}
        level={part === "agents" ? 2 : 1}
        onFocus={() => takeFocus(entry, state)}
        onPin={() => pin(id)}
        ownAnswer={isReader(node.answer?.by, reader)}
        pinned={pinned.has(id)}
        review={part === "review"}
        tabStop={tabStop === id}
        {...(mark === undefined ? {} : { threadRead: mark })}
      />
    );
  };
  const foot = `${headings}-agents`;
  return (
    <div
      className="mx-auto mt-3.5 mb-[120px] flex w-full max-w-[960px] min-w-0 flex-col max-[760px]:mt-2.5"
      data-decisions-body=""
    >
      <div
        aria-label={CONTROL_LABELS.tree}
        className="flex min-w-0 flex-col"
        onBlur={releaseFocus}
        ref={body}
        role="tree"
      >
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
                className="mt-[22px] mb-1 flex min-w-0 items-baseline gap-2.5 max-[760px]:mt-[18px]"
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
        {parts.agents.length === 0 ? null : (
          // biome-ignore lint/a11y/useSemanticElements: a tree owns only its rows and groups of rows; a fieldset is neither.
          <div
            aria-labelledby={foot}
            className="flex min-w-0 flex-col"
            data-decisions-part="agents"
            role="group"
          >
            {/* The foot (the prototype's `.minor-t`): one row that says how many decisions it
                holds and whether they are drawn, and whose control unfolds them. Out of the
                tab order, as every control inside the tree is (R9): the key that cycles how
                much is drawn unfolds it. */}
            <div
              aria-expanded={footShown}
              aria-level={1}
              className="mt-[22px] mb-1 flex min-w-0 max-[760px]:mt-[18px]"
              data-decisions-foot=""
              role="treeitem"
              tabIndex={-1}
            >
              <button
                aria-expanded={footShown}
                className={`dispatch-inline-link py-1 text-left text-sm leading-5 font-medium hover:text-page-ink ${pageMutedText}`}
                data-decisions-heading=""
                id={foot}
                onClick={() => setFootShown(!footShown)}
                tabIndex={-1}
                type="button"
                {...pageChrome}
              >
                <span aria-hidden="true">{footShown ? "▾" : "▸"}</span>{" "}
                <span data-decisions-count="" {...authorText}>
                  {parts.agents.length}
                </span>{" "}
                {parts.agents.length === 1
                  ? DECISIONS_WORDS.agents_one
                  : DECISIONS_WORDS.agents_many}
              </button>
            </div>
            {footShown ? parts.agents.map((entry) => rowFor(entry, "agents")) : null}
          </div>
        )}
      </div>
      {drawn.length === 0 && parts.agents.length === 0 ? (
        <p className={`py-1 text-sm ${textMutedOnCanvas}`} {...pageChrome}>
          {emptyLine}
        </p>
      ) : null}
    </div>
  );
}

/** The row of node `id` inside `container`, or null where it draws none. */
function rowOf(container: HTMLElement | null, id: string): HTMLElement | null {
  return (
    container?.querySelector<HTMLElement>(`[data-node-row][data-node-id="${CSS.escape(id)}"]`) ??
    null
  );
}

/** Whether `actor` is the reader writing as themselves: a person signed in as `reader`, and not
 *  an agent acting for them, whose words are the agent's (R9). It is `TreeRows.tsx`'s rule,
 *  which is not exported there; integration gives the two one copy. */
function isReader(actor: Actor | undefined, reader: string | undefined): boolean {
  return (
    reader !== undefined &&
    actor?.kind === "user" &&
    actor.id === reader &&
    (actor.act?.session ?? "") === ""
  );
}
