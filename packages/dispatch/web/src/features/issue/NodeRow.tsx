import {
  type CSSProperties,
  type ReactNode,
  type RefObject,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { Link } from "react-router-dom";

import type { Artifact } from "../../api/types";
import {
  pageFocusedRow,
  pageFoldedHalo,
  pageGuideLineBg,
  pageInkText,
  pageKindBullet,
  pageKindTag,
  pageMutedText,
  pageOwnWordsRule,
  pageQuietText,
  pageRowAct,
  pageRowActs,
  pageRowOption,
  pageStateText,
  pageWordsText,
} from "../../theme/classes";
import { buildReferencePath, parseNodeReference, shortForm } from "../refs/routes";
import { DecisionActions } from "./DecisionActions";
import { STAGE_IN_ROW, useDocumentStage } from "./document-stage";
import { bringOnScreen } from "./identity-block";
import { firstLineOf, type NodeChip, nodeWords, type TreeNode } from "./issue-tree";
import { NodeFacts } from "./NodeFacts";
import { NodeGlyph } from "./NodeGlyph";
import { shortDay } from "./node-time";
import { type ThreadMark, ThreadReadMark } from "./outline-thread-read";
import { type OutlineWriter, replyItemOf, takesAnswer, useOutlineWriter } from "./outline-writer";
import { authorText, pageChrome } from "./page-structure";
import {
  type AuthorKind,
  CONTROL_LABELS,
  GAP_LINES,
  GAP_WORDS,
  GLYPH_TOKENS,
  NODE_TYPE_NAMES,
  type NodeType,
  nodeStateWord,
} from "./page-words";
import { RowText } from "./RowText";

/**
 * One row of the issue's outline, and the body it opens on focus (dispatch://INFRA-1196 R1,
 * R6, R9, R34, and the architect's ruling 1 on R1 in
 * dispatch://INFRA-1196/artifact/build-rulings-md).
 *
 * A row reads from the left as the prototype's `.card` does: its bullet in its kind's colour,
 * which is also its fold control where it holds nodes, then its words. At the right end of its
 * first line stand the node's kind as one tag where it has a typed kind, then its author and
 * its time in grey, and under them its state where the model holds one. The words of every
 * node start at one left edge, so nothing stands between a bullet and its words; the reader's
 * own words carry an ink rule and its indent, which is the outline's second left edge and its
 * last (R34). From 761 px up the tag, the author and the time are a column of their own at the
 * right; on a phone they stand at the right end of the words' first line, and the words wrap
 * around them.
 *
 * A drawn row draws its node's words whole (ruling 1). An agent's words and an answered question
 * are cut at six lines where more than 20 words would be hidden, and "more · N words" opens the
 * rest in place; a row of pictures the sixth line crosses is drawn whole, and the reader's own
 * words and a question that waits on the reader are never cut. A folded branch is the one place
 * a row draws a single line (R1): its first line, cut at a word by `firstLineOf`, and how many
 * nodes it holds; its focus draws its words whole.
 *
 * The body is drawn for the focused row, for a row pinned open, and while the cycle key opens
 * every body: it adds what the row does not carry - the rest of a cut text, an option's own
 * words, the facts, what the coverage read counts, the gaps in full, Keep and Change - and the
 * control that keeps it open (R6). The focus opens it, never the pointer, and no element of the
 * row carries a `title`.
 *
 * The row is a `treeitem` of the tree's one tab stop: the focused row is the only row that takes
 * the focus, and nothing inside a row takes any, so Tab from outside enters the tree once and
 * Tab from the row leaves it again (R9). What a reader does inside a row they do with a key: the
 * fold and the pin have the arrow keys and `o`, and Keep, Change and a chip's link have keys of
 * their own (`useTreeKeymap.ts`).
 */
export function NodeRow({
  answerer,
  author,
  body,
  context,
  document,
  editing,
  expanded,
  focused,
  folded,
  isClosed,
  level,
  node,
  onFocus,
  onPin,
  onToggle,
  ownAnswer,
  pinned,
  rowKey,
  tabStop,
  threadRead,
}: {
  /** The name of the person whose answer this ask carries (`AgentText.author`), where it
   *  carries one. */
  answerer?: string;
  /** Who made the node: the name the page draws an author with (`AgentText.author`), never an
   *  id (R9, R22), the kind of author, which picks its shape, and whether it is the reader, whose
   *  name is drawn in the ink and whose words carry the ink rule (R34). */
  author: { readonly kind: AuthorKind; readonly name: string; readonly reader: boolean };
  /** Whether this row draws its body: it has the focus, the reader pinned it open, or the cycle
   *  key opens every body. */
  body: boolean;
  /** Whether the view draws this row only as the ancestor of a match: the row is faint and
   *  marked, and the arrow keys reach it as they reach any other row (R2). */
  context: boolean;
  /** The document this row's node is a block of, which its body writes against: a decision's
   *  Keep and Change are writes on the decisions document (R7). Undefined for a node that is
   *  no block of a document of this issue. */
  document?: Artifact;
  /** The document this row's own text lives in, while the reader is editing it here: its
   *  editor is drawn in place of the row's words and the caret is in this node's block. */
  editing?: string;
  /** `true` when its children are drawn, `false` when it holds children and they are not, and
   *  undefined for a node that holds none. */
  expanded?: boolean;
  focused: boolean;
  /** How many nodes it holds while it is folded. */
  folded?: number;
  /** Whether the issue is closed, which the writes a body offers are refused on. */
  isClosed: boolean;
  /** How deep the row sits, the tree's first row at 1 (`aria-level`). */
  level: number;
  node: TreeNode;
  onFocus: () => void;
  onPin: () => void;
  onToggle: () => void;
  /** Whether the answer this ask carries is the reader's own, which carries the ink rule. */
  ownAnswer: boolean;
  pinned: boolean;
  /** What tells this row from every other, including another placement of the same node
   *  (`rowKeyOf`): the row the caret was taken from is found by it (`useNodeEditing.ts`). */
  rowKey: string;
  /** Whether this row is the tree's one tab stop: the focused row, or the tree's first row
   *  before the reader has touched it. */
  tabStop: boolean;
  /** The thread whose newest write this row draws, which a dwell on the row marks read in the
   *  reader's Inbox (`outline-thread-read.tsx`). */
  threadRead?: ThreadMark;
}): ReactNode {
  const row = useRef<HTMLDivElement | null>(null);
  const words = useRef<HTMLDivElement | null>(null);
  const stage = useDocumentStage(editing, STAGE_IN_ROW);
  // Where the reader writes: a row's Reply and an open question's Answer set it (R16).
  const writer = useOutlineWriter();
  // The reader's own words: a turn a person wrote who is the reader (R34). An ask, a block or a
  // decision is not a turn, so its words are never the reader's here, whoever asked it.
  const own = author.reader && (node.type === "comment" || node.type === "message");
  const tone = context ? "context" : toneOf(node.type, author.kind);
  // A folded branch draws one line and its count (R1); its focus draws its words whole.
  const collapsed = expanded === false;
  const oneLine = collapsed && !body;
  // The prototype's `CUT_LINES`: an agent's words and an answered question may be cut, the
  // reader's own words and a question waiting on the reader never are (ruling 1).
  const cuttable =
    !oneLine &&
    editing === undefined &&
    (((node.type === "comment" || node.type === "message") && author.kind === "agent") ||
      (node.type === "ask" && node.state === "answered" && !node.waiting));
  const clamp = useClamp(words, cuttable);
  const [opened, setOpened] = useState(false);
  const clamped = clamp !== undefined && !opened && !body;

  // The focused row is brought inside the viewport and clear of the sticky identity block
  // after every move, by the one rule the whole page places a target with (R9,
  // `identity-block.ts`), and again whenever the row grows under it: a body draws more once it
  // is open - the editor a reader puts the caret in arrives on the row's stage after the row
  // has the focus (`document-stage.tsx`) - and the text would otherwise push its foot off the
  // screen.
  //
  // Except while a press inside the row is held. A press on an unfocused row focuses it on
  // mousedown, before the press ends, and a scroll then moves what the pointer went down on:
  // the press ends on something else and the control it was on never hears it. Measured at
  // 1440 px on the Decisions preset, a row whose top sat 24 px under the identity block
  // scrolled 32 px between mousedown and mouseup, and the reader's Keep was never sent. The
  // reader pressed what they could see, so there is nothing to bring on screen.
  const pressed = useRef(false);
  const press = () => {
    pressed.current = true;
    const ended = new AbortController();
    const release = () => {
      pressed.current = false;
      ended.abort();
    };
    window.addEventListener("pointerup", release, { capture: true, signal: ended.signal });
    window.addEventListener("pointercancel", release, { capture: true, signal: ended.signal });
  };
  useLayoutEffect(() => {
    const element = row.current;
    if (!focused || element === null) return;
    const place = () => {
      if (!pressed.current) bringOnScreen(element);
    };
    place();
    if (typeof ResizeObserver === "undefined") return;
    const grown = new ResizeObserver(place);
    grown.observe(element);
    return () => grown.disconnect();
  }, [focused]);

  const depth = level - 1;
  const first = firstLineOf(nodeWords(node));
  const foldedWords = folded === 1 ? CONTROL_LABELS.folded_row : CONTROL_LABELS.folded_rows;
  const edges = node.alsoUnder.length > 0 || node.chips.length > 0 || node.gaps.length > 0;
  return (
    <div
      aria-expanded={expanded}
      aria-level={level}
      className="relative min-w-0 pl-[calc(var(--row-depth)*23px)] outline-none max-[760px]:pl-[calc(var(--row-depth)*17px)]"
      data-folded-count={folded}
      data-node-context={context ? "" : undefined}
      data-node-id={node.id}
      data-node-row=""
      data-node-state={node.state}
      data-node-type={node.type}
      data-row-key={rowKey}
      onFocus={onFocus}
      onPointerDown={press}
      ref={row}
      role="treeitem"
      style={{ "--row-depth": depth } as CSSProperties}
      tabIndex={tabStop ? 0 : -1}
    >
      {/* One faint guide per level above the row, each at the middle of that level's bullet
          (the prototype's `.tn > .kids`, 9 px into a level 23 px wide, and 8 px into one 17 px
          wide on a phone). The rows stand one under another, so a level's guides join into one
          line down its whole branch. */}
      {Array.from({ length: depth }, (_unused, guide) => (
        <span
          aria-hidden="true"
          className={`absolute inset-y-0 w-px left-[calc(var(--guide)*23px+9px)] max-[760px]:left-[calc(var(--guide)*17px+8px)] ${pageGuideLineBg}`}
          // biome-ignore lint/suspicious/noArrayIndexKey: a guide is its level and nothing else.
          key={guide}
          style={{ "--guide": guide } as CSSProperties}
        />
      ))}
      <div
        className={`relative grid min-w-0 grid-cols-[18px_minmax(0,1fr)_170px] gap-x-2 rounded-md py-0.5 pr-1.5 max-[1240px]:grid-cols-[18px_minmax(0,1fr)_160px] max-[760px]:flow-root max-[760px]:pr-1 max-[760px]:pl-[22px] ${
          focused ? pageFocusedRow : ""
        }`}
        data-node-card=""
      >
        <Bullet collapsed={collapsed} expanded={expanded} onToggle={onToggle} tone={tone} />
        <RowMargin author={author} context={context} node={node} tone={tone} />
        <div
          className={`col-start-2 row-start-1 min-w-0 ${own ? pageOwnWordsRule : ""}`}
          data-node-content=""
          data-node-own={own ? "" : undefined}
        >
          {oneLine ? (
            <p
              className={`text-[15px] leading-6 break-words ${context ? pageQuietText : pageWordsText}`}
            >
              <span data-node-first-line="" {...authorText}>
                {first.cut ? `${first.line}…` : first.line}
              </span>
              {folded === undefined ? null : (
                <span
                  className={`ml-1.5 text-xs whitespace-nowrap ${pageQuietText}`}
                  data-node-count=""
                  {...pageChrome}
                >
                  <span {...authorText}>{folded}</span> {foldedWords}
                </span>
              )}
            </p>
          ) : (
            <div
              className={`relative text-[15px] leading-6 ${
                context ? pageQuietText : own ? pageInkText : pageWordsText
              } ${clamped ? "overflow-clip" : ""}`}
              data-node-words=""
              ref={words}
              style={clamped ? { maxHeight: clamp.height } : undefined}
            >
              {editing === undefined ? (
                <RowText markdown={node.text} source={node.source} tree={node.tree} />
              ) : (
                <div className="flex min-w-0 flex-col" ref={stage} {...authorText} />
              )}
              {clamped && !clamp.pictures ? (
                <span
                  aria-hidden="true"
                  className="pointer-events-none absolute inset-x-0 bottom-0 h-8 bg-[linear-gradient(transparent,var(--color-page-paper))]"
                />
              ) : null}
            </div>
          )}
          {/* A press on it leaves the focus where it is: focusing the row would draw the words
              whole under the pointer and move the control before the press ends, so the press
              would land on nothing. It opens the rest in place, and closes it again. */}
          {clamp === undefined || oneLine ? null : (
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
                  {CONTROL_LABELS.read_more} · <span {...authorText}>{clamp.hidden}</span>{" "}
                  {CONTROL_LABELS.read_more_words}
                </>
              )}
            </button>
          )}
          {node.options === undefined || oneLine ? null : (
            <NodeOptions body={body} node={node} writer={writer} />
          )}
          {node.answer === undefined || oneLine ? null : (
            <AskAnswer answerer={answerer ?? ""} node={node} own={ownAnswer} />
          )}
          {/* Under a filter, the node's parent where the filter draws it as no row: one line,
              its kind's word and its first line (R2, R1's first-line rule). */}
          {node.parentLine === undefined ? null : (
            <p
              className={`flex min-w-0 items-baseline gap-1 text-xs leading-5 ${pageQuietText}`}
              data-node-parent-line=""
              data-parent-id={node.parentLine.id}
              data-parent-type={node.parentLine.type}
            >
              <span className="shrink-0" {...pageChrome}>
                {NODE_TYPE_NAMES[node.parentLine.type]}
              </span>
              <span className="min-w-0 break-words" {...authorText}>
                {node.parentLine.line}
              </span>
            </p>
          )}
          {edges && !oneLine ? (
            <p
              className={`flex min-w-0 flex-wrap items-baseline gap-x-2 text-xs leading-5 ${pageQuietText}`}
              data-node-trail=""
            >
              <NodeEdges node={node} />
            </p>
          ) : null}
          {body ? (
            <NodeBody
              document={document}
              isClosed={isClosed}
              node={node}
              onPin={onPin}
              pinned={pinned}
            />
          ) : null}
          {focused && writer !== undefined ? (
            <RowActs node={node} ownAnswer={ownAnswer} writer={writer} />
          ) : null}
        </div>
        {threadRead === undefined || oneLine ? null : <ThreadReadMark mark={threadRead} />}
      </div>
    </div>
  );
}

/** The kinds of node the page draws no tag for: what somebody said, which is the conversation's
 *  own words rather than a kind it revealed (R34: "as one tag where the node has a typed
 *  kind"). Every other kind is a typed node, and its tag names it. */
const UNTYPED: Readonly<Partial<Record<NodeType, true>>> = { comment: true, message: true };

/** A node's colour, as `pageKindBullet` and `pageKindTag` name them, by what it is and, for a
 *  turn, by who wrote it: a person's words are the ink, an agent's the quiet grey. */
type Tone = keyof typeof pageKindBullet;

function toneOf(type: NodeType, author: AuthorKind): Tone {
  switch (type) {
    case "ask":
      return "question";
    case "check":
    case "result":
      return "check";
    case "child_issue":
      return "child";
    case "comment":
    case "message":
      return author === "person" ? "person" : "agent";
    case "decision":
      return "decision";
    case "plan_task":
      return "step";
    case "requirement":
      return "statement";
    case "callout":
    case "document":
    case "handoff":
    case "image":
    case "issue":
      return "issue";
  }
}

/** The colour a state is drawn in, beside its word and its shape (R6). */
function stateTone(node: TreeNode): keyof typeof pageStateText {
  if (node.waiting) return node.type === "decision" ? "decision" : "waiting";
  if (node.type === "check" && node.state === "passed") return "passed";
  if (node.type === "check" && node.state === "failed") return "failed";
  return "quiet";
}

/**
 * The bullet: a 7 px dot in the node's colour in an 18 px column (the prototype's `.bul`), and
 * the node's fold control where it holds nodes, named by what pressing it does. A folded
 * branch's dot keeps a halo, as the prototype's `.card.collapsed` does, beside the count of what
 * it holds. Out of the tab order, as every control inside a row is (R9).
 */
function Bullet({
  collapsed,
  expanded,
  onToggle,
  tone,
}: {
  collapsed: boolean;
  expanded?: boolean;
  onToggle: () => void;
  tone: Tone;
}): ReactNode {
  const dot = (
    <i
      aria-hidden="true"
      className={`block size-[7px] rounded-full ${pageKindBullet[tone]} ${collapsed ? pageFoldedHalo : ""}`}
    />
  );
  const place =
    "col-start-1 row-start-1 flex h-6 w-[18px] items-center justify-center self-start max-[760px]:absolute max-[760px]:top-0.5 max-[760px]:left-0 max-[760px]:w-4";
  if (expanded === undefined) {
    return (
      <span className={place} data-node-bullet="">
        {dot}
      </span>
    );
  }
  return (
    <button
      aria-expanded={expanded}
      aria-label={expanded ? CONTROL_LABELS.fold : CONTROL_LABELS.unfold}
      className={`${place} cursor-pointer`}
      data-node-bullet=""
      onClick={onToggle}
      tabIndex={-1}
      type="button"
    >
      {dot}
    </button>
  );
}

/**
 * What stands at the right end of the row's first line (R9, R34, the prototype's `.mg`): the
 * node's kind as one tag - its word and its shape, in its colour - where it has a typed kind,
 * then who made it, by a name and a shape, and when, in the page's short form; and under them
 * its state, by a word and a shape, with the one word a decision the model marks unsure says of
 * itself. All of it is grey but the tag, the reader's own name and a state that asks something
 * of the reader. From 761 px up it is a column of its own; on a phone it floats at the right of
 * the words' first line.
 */
function RowMargin({
  author,
  context,
  node,
  tone,
}: {
  author: { readonly kind: AuthorKind; readonly name: string; readonly reader: boolean };
  context: boolean;
  node: TreeNode;
  tone: Tone;
}): ReactNode {
  const stateGlyphs: Readonly<Record<string, Readonly<Record<string, string>>>> =
    GLYPH_TOKENS.states;
  const stateGlyph = node.state === undefined ? undefined : stateGlyphs[node.type]?.[node.state];
  const time = node.at === undefined ? "" : shortDay(node.at);
  return (
    <div
      className={`col-start-3 row-start-1 min-w-0 self-start text-right text-xs leading-6 max-[760px]:float-right max-[760px]:ml-2 max-[760px]:max-w-[60%] ${pageQuietText}`}
      data-node-margin=""
    >
      <div className="flex flex-wrap items-baseline justify-end gap-x-[7px]">
        {UNTYPED[node.type] === true ? null : (
          <span
            className={`inline-flex items-center gap-1 font-semibold whitespace-nowrap ${pageKindTag[tone]}`}
            data-node-tag=""
          >
            <NodeGlyph className="size-3 shrink-0" token={GLYPH_TOKENS.types[node.type]} />
            <span {...pageChrome}>{NODE_TYPE_NAMES[node.type]}</span>
          </span>
        )}
        <span className="min-w-0 [overflow-wrap:anywhere]" data-node-byline="">
          {/* Who made it, by a word and a shape: one shape per kind of author, so a person, an
              agent and an author nobody recorded never share one (R9). The name is the page's
              one name for an author (`AgentText.author`), never an id (R22); where nobody was
              recorded the words are the page's own. */}
          <span
            className="inline-flex items-center gap-1"
            data-author-kind={author.kind}
            data-node-author=""
          >
            <NodeGlyph className="size-3 shrink-0" token={GLYPH_TOKENS.authors[author.kind]} />
            <span
              className={author.reader && !context ? `font-semibold ${pageInkText}` : ""}
              {...(author.kind === "unknown" ? pageChrome : authorText)}
            >
              {author.name}
            </span>
          </span>
          {time === "" ? null : (
            <>
              <span {...pageChrome}> · </span>
              <span className="whitespace-nowrap" data-node-time="" {...authorText}>
                {time}
              </span>
            </>
          )}
        </span>
      </div>
      {node.state === undefined && node.unsure !== true ? null : (
        <div className="flex flex-wrap items-center justify-end gap-x-1.5 leading-4 font-semibold">
          {node.state === undefined ? null : (
            <span
              className={`inline-flex items-center gap-1 ${context ? "" : pageStateText[stateTone(node)]}`}
            >
              {stateGlyph === undefined ? null : (
                <NodeGlyph className="size-3 shrink-0" token={stateGlyph} />
              )}
              <span data-node-state={node.state} {...pageChrome}>
                {nodeStateWord(node.state)}
              </span>
            </span>
          )}
          {node.unsure === true ? (
            <span data-node-unsure="" {...pageChrome}>
              {CONTROL_LABELS.unsure}
            </span>
          ) : null}
        </div>
      )}
    </div>
  );
}

/**
 * The choices an ask offers, under its question (the prototype's `.opts`): each by its own
 * words, the one the person chose filled and in the ink, the others hollow and grey, and while
 * the question waits on the reader every one in the waiting colour. The words an option carries
 * beside its name are drawn in the focused node's body (R6).
 *
 * An open question's options are the choices of its answer (R16, the prototype's `button.opt`):
 * a press picks one, or lets it go, and sets the line at the foot of the outline to answer the
 * question, where the keyboard picks them too; the one picked is drawn on the waiting tint.
 */
function NodeOptions({
  body,
  node,
  writer,
}: {
  body: boolean;
  node: TreeNode;
  writer: OutlineWriter | undefined;
}): ReactNode {
  const chosen = new Set(node.answer?.selected ?? []);
  const answering = writer !== undefined && takesAnswer(node);
  const picks =
    writer?.answer !== undefined && writer.answer.nodeId === node.id ? writer.answer.selected : [];
  return (
    <ul className="my-0.5 text-sm leading-[21px]" data-node-options="">
      {(node.options ?? []).map((option) => {
        const picked = chosen.has(option.label);
        const words = (
          <>
            <span aria-hidden="true" className="w-3 shrink-0 text-center text-xs" {...pageChrome}>
              {picked || picks.includes(option.label) ? "●" : "○"}
            </span>
            <span className="min-w-0 break-words">
              <span {...authorText}>{option.label}</span>
              {body && option.description !== undefined ? (
                <span className={`font-normal ${pageQuietText}`}>
                  <span {...pageChrome}> — </span>
                  <span {...authorText}>{option.description}</span>
                </span>
              ) : null}
            </span>
          </>
        );
        return (
          <li
            className={
              answering
                ? ""
                : `flex items-baseline gap-2 ${
                    picked
                      ? `font-semibold ${pageInkText}`
                      : node.waiting
                        ? pageStateText.waiting
                        : pageQuietText
                  }`
            }
            data-node-option={picked ? "chosen" : ""}
            key={option.label}
          >
            {answering ? (
              // Out of the tab order, as every control inside a row is: the line the press sets
              // draws the same options as its own chips, which the keyboard reaches (R9).
              <button
                aria-pressed={picks.includes(option.label)}
                className={`dispatch-inline-link ${pageRowOption}`}
                data-pick-option={option.label}
                onClick={() => writer.pick(node, option.label)}
                tabIndex={-1}
                type="button"
              >
                {words}
              </button>
            ) : (
              words
            )}
          </li>
        );
      })}
    </ul>
  );
}

/**
 * What the reader can do with the focused row, on one quiet line under it (the prototype's
 * `.acts`, drawn on the focused row alone): Reply on every row that takes a reply - a comment, a
 * message, an ask whatever its state, and the issue itself, whose reply is a new comment on it
 * (R16, dispatch://INFRA-3677) - Answer on an open question, and Change answer on a question the
 * reader answered. Each sets the line at the foot of the outline and puts the caret in it; `r`
 * and `a` press the first two from the keyboard (`useTreeKeymap.ts`). A typed node - a
 * statement, a check, a decision - takes no reply in today's model, which holds no reply to a
 * block (dispatch://INFRA-1253), so it draws none.
 *
 * A press leaves the focus where it is until the action moves it to the line, as the row's other
 * controls do, so the press never lands on a row that moved under it.
 */
function RowActs({
  node,
  ownAnswer,
  writer,
}: {
  node: TreeNode;
  ownAnswer: boolean;
  writer: OutlineWriter;
}): ReactNode {
  const acts: { readonly act: string; readonly label: string; readonly run: () => void }[] = [];
  if (takesAnswer(node)) {
    acts.push({ act: "answer", label: CONTROL_LABELS.answer, run: () => writer.answerNode(node) });
  }
  if (ownAnswer && node.type === "ask" && node.state === "answered") {
    acts.push({
      act: "change-answer",
      label: CONTROL_LABELS.change_answer,
      run: () => writer.changeAnswer(node),
    });
  }
  const item = replyItemOf(node);
  if (item !== undefined || node.type === "issue") {
    acts.push({
      act: "reply",
      label: CONTROL_LABELS.reply,
      run: () => (item === undefined ? writer.writeOnIssue() : writer.replyTo(item)),
    });
  }
  if (acts.length === 0) return null;
  return (
    <p className={pageRowActs} data-row-acts="">
      {acts.map(({ act, label, run }) => (
        <button
          className={`dispatch-inline-link ${pageRowAct}`}
          data-row-act={act}
          key={act}
          onClick={run}
          onMouseDown={(event) => event.preventDefault()}
          tabIndex={-1}
          type="button"
          {...pageChrome}
        >
          {label}
        </button>
      ))}
    </p>
  );
}

/**
 * A person's answer to an ask, which is part of the ask and no node of its own
 * (dispatch://INFRA-1150 R3, dispatch://INFRA-1196 R1): the page's word for it, who answered by
 * a name and a shape as every author is drawn (R9), what they chose where the ask offers no list
 * to mark it in, and what they wrote, whole and character for character, keeping the line breaks
 * they typed (R9, ruling 1). The reader's own answer carries the ink rule their own words carry
 * (R34).
 */
function AskAnswer({
  answerer,
  node,
  own,
}: {
  answerer: string;
  node: TreeNode;
  own: boolean;
}): ReactNode {
  const answer = node.answer;
  if (answer === undefined) return null;
  const kind = answer.by.kind === "user" ? "person" : "agent";
  return (
    <div
      className={`mt-1 flex min-w-0 flex-col ${own ? pageOwnWordsRule : ""}`}
      data-node-answer=""
    >
      <p
        className={`flex min-w-0 flex-wrap items-center gap-x-1.5 text-xs leading-5 ${pageQuietText}`}
      >
        <span {...pageChrome}>{CONTROL_LABELS.answer}</span>
        <span className="inline-flex items-center gap-1" data-answer-author="">
          <NodeGlyph className="size-3 shrink-0" token={GLYPH_TOKENS.authors[kind]} />
          <span className={own ? `font-semibold ${pageInkText}` : ""} {...authorText}>
            {answerer}
          </span>
        </span>
      </p>
      {node.options !== undefined || answer.selected.length === 0 ? null : (
        <ul className={`flex min-w-0 flex-col text-sm leading-[21px] ${pageInkText}`}>
          {answer.selected.map((option) => (
            <li
              className="break-words font-semibold"
              data-answer-choice=""
              key={option}
              {...authorText}
            >
              {option}
            </li>
          ))}
        </ul>
      )}
      {answer.text === "" ? null : (
        <p
          className={`text-[15px] leading-6 whitespace-pre-wrap break-words ${own ? pageInkText : pageWordsText}`}
          data-answer-text=""
          {...authorText}
        >
          {answer.text}
        </p>
      )}
    </div>
  );
}

/** Every edge of the node that is not the spine, and every gap on it, on a quiet line under its
 *  words (the prototype's `.trail`): the chips naming what it also sits under and what it points
 *  at, and the one word each gap is known by, which its body says in full (R4, R10). */
function NodeEdges({ node }: { node: TreeNode }): ReactNode {
  return (
    <>
      {node.alsoUnder.map((chip) => (
        <Chip chip={chip} key={`also:${chip.ref}`} label={CONTROL_LABELS.also_under} />
      ))}
      {node.chips.map((chip) => (
        <Chip chip={chip} key={chip.ref} />
      ))}
      {node.gaps.map((gap) => (
        <span data-gap="" data-gap-kind={gap} key={gap} {...pageChrome}>
          {GAP_WORDS[gap]}
        </span>
      ))}
    </>
  );
}

/**
 * One edge of the node that is not the spine, naming its target: the page's word for the edge
 * where it has one, the target's own name from the batched reference read, and the target's
 * address, which is how a reader cites it and the only thing that tells two blocks of one
 * document apart. A target nothing answers to now says so instead of naming nothing.
 *
 * The address is drawn in its short form - `CORE-1 spec block`, `refs/routes.ts`'s `shortForm`,
 * the form the old Plan tab drew for the same pointer - from the moment the reference read has
 * answered for it. The raw `dispatch://CORE-1/spec#b-<block id>` is a name nobody chose: a
 * block id is a uuid, which R21 and c-plain-words refuse where a name belongs, and a chip whose
 * target the graph answered with no text of its own drew nothing else. A chip still waiting for
 * that read keeps the raw address, which is the deliberate transient `useIssueTree.ts` records
 * ("the names come last and all at once"): until the read answers there is nothing else to say,
 * and the reference is still the reference.
 */
function Chip({ chip, label }: { chip: NodeChip; label?: string }): ReactNode {
  const target = parseNodeReference(chip.ref);
  const route = chip.gone ? undefined : target;
  // The target's own words, as the link a reader follows to it: a chip names a node the page
  // may not be drawing, and the old page's decision cards reached each one by its words (R18,
  // c-nothing-lost). A target nothing answers to now, or a reference the grammar cannot read,
  // is text: there is nowhere to go.
  const named =
    chip.name === "" ? null : route === undefined ? (
      <span {...authorText}>{`${chip.name} `}</span>
    ) : (
      <>
        {/* Out of the tab order, as every other control inside a row is: the tree is one tab
            stop and the row's own keys reach what it holds: `f` steps to a chip, and Enter on
            it follows the link (R9, WCAG 2.1.1, `useTreeKeymap.ts`). */}
        <Link
          className={`dispatch-inline-link underline decoration-page-hair underline-offset-[3px] ${pageMutedText}`}
          tabIndex={-1}
          to={buildReferencePath(route)}
          {...authorText}
        >
          {chip.name}
        </Link>{" "}
      </>
    );
  // A blocking child issue is named by its key and addressed by the same key, and a chip saying
  // "CORE-2 CORE-2" says one of them twice.
  const address = chip.answered && target !== undefined ? shortForm(target) : chip.ref;
  return (
    <span className="min-w-0 break-words" data-node-chip="">
      {label === undefined ? null : <span {...pageChrome}>{label} </span>}
      {/* A sentence about the target, so a colon sets it apart from the target's own words. */}
      {chip.stale === true ? <span {...pageChrome}>{CONTROL_LABELS.target_stale}: </span> : null}
      {chip.gone ? <span {...pageChrome}>{CONTROL_LABELS.target_gone} </span> : named}
      {address === chip.name ? null : (
        <span className="break-all" {...authorText}>
          {address}
        </span>
      )}
    </span>
  );
}

/**
 * What the focused node's body adds to its row (R6): the facts of a check and of a result
 * (`NodeFacts.tsx`), the gaps said in full, the two counts the coverage read carries for a
 * requirement, what a reader does with a decision (`DecisionActions.tsx`), and the control that
 * keeps the body open once the focus leaves it. The node's words are the row's own, drawn whole
 * there with the rest of a cut text once the body is open.
 */
function NodeBody({
  document,
  isClosed,
  node,
  onPin,
  pinned,
}: {
  document?: Artifact;
  isClosed: boolean;
  node: TreeNode;
  onPin: () => void;
  pinned: boolean;
}): ReactNode {
  return (
    <div
      className="mt-1 flex min-w-0 flex-col gap-1 text-sm"
      data-node-body=""
      data-pinned={pinned ? "" : undefined}
    >
      <NodeFacts facts={node.facts} />
      {node.gaps.map((gap) => (
        <p className={`text-xs ${pageQuietText}`} key={gap} {...pageChrome}>
          {GAP_LINES[gap]}
        </p>
      ))}
      {node.counts === undefined ? null : (
        <p className={`text-xs ${pageQuietText}`} {...pageChrome}>
          <span>{CONTROL_LABELS.serving_checks}: </span>
          <span {...authorText}>{node.counts.checks}</span>
          <span>, {CONTROL_LABELS.serving_plan_tasks}: </span>
          <span {...authorText}>{node.counts.planTasks}</span>
        </p>
      )}
      {node.type === "decision" ? (
        <DecisionActions document={document} isClosed={isClosed} nodeReference={node.reference} />
      ) : null}
      <p>
        <button
          className={`dispatch-inline-link text-xs leading-6 font-medium ${pageQuietText}`}
          onClick={onPin}
          tabIndex={-1}
          type="button"
          {...pageChrome}
        >
          {pinned ? CONTROL_LABELS.let_close : CONTROL_LABELS.keep_open}
        </button>
      </p>
    </div>
  );
}

/** The prototype's `CUT_LINES`: how many lines of an agent's words or an answered question a row
 *  draws before it cuts them. */
const CUT_LINES = 6;

/** How many words a cut must hide before it is made: fewer are drawn whole. */
const CUT_WORDS = 20;

/** How far past six lines a text may run and still be drawn whole, as the prototype allows. */
const CUT_SLACK = 12;

/** What a row draws of words it cuts: how tall it draws them, how many words it holds back,
 *  and whether the cut was moved to the foot of a row of pictures the sixth line crossed. */
interface Clamp {
  readonly height: number;
  readonly hidden: number;
  readonly pictures: boolean;
}

/**
 * Where a row cuts its words, read from the words as the browser laid them out, which is the
 * prototype's own reading (`measureClamps`): one layout read per row that may be cut - an
 * agent's words and an answered question - and again when its width or its words change, never
 * one for any other row. Undefined where the words are drawn whole.
 */
function useClamp(words: RefObject<HTMLElement | null>, cuttable: boolean): Clamp | undefined {
  const [clamp, setClamp] = useState<Clamp | undefined>(undefined);
  useLayoutEffect(() => {
    const element = words.current;
    if (!cuttable || element === null) {
      setClamp(undefined);
      return;
    }
    const measure = () => {
      const next = measureClamp(element);
      setClamp((held) =>
        held?.height === next?.height &&
        held?.hidden === next?.hidden &&
        held?.pictures === next?.pictures
          ? held
          : next
      );
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    // The words' own box is held to the cut while it is drawn, so their growing - a reading
    // that paints after the row, a picture that loads - is seen on what they are drawn in.
    for (const child of element.children) observer.observe(child);
    return () => observer.disconnect();
  }, [cuttable, words]);
  return cuttable ? clamp : undefined;
}

/** The cut of the words `element` holds, as the prototype's `measureClamps` reads it. */
function measureClamp(element: HTMLElement): Clamp | undefined {
  const pitch = Number.parseFloat(getComputedStyle(element).lineHeight);
  const limit = CUT_LINES * (Number.isFinite(pitch) ? pitch : 24);
  if (element.scrollHeight <= limit + CUT_SLACK) return undefined;
  const top = element.getBoundingClientRect().top;
  // The words a cut would hide, block by block: a block below the line whole, and of a block the
  // line crosses the share of its words below it.
  let hidden = 0;
  for (const block of blocksOf(element)) {
    const box = block.getBoundingClientRect();
    const from = box.top - top;
    const to = box.bottom - top;
    const count = (block.textContent ?? "").split(/\s+/u).filter(Boolean).length;
    if (from >= limit) hidden += count;
    else if (to > limit && box.height > 0) hidden += count * ((to - limit) / box.height);
  }
  const held = Math.round(hidden);
  if (held <= CUT_WORDS) return undefined;
  // A row of pictures the line crosses is drawn whole, and the cut moves to its foot.
  let height = limit;
  let pictures = false;
  for (const picture of element.querySelectorAll("img")) {
    const box = picture.getBoundingClientRect();
    if (box.top - top < limit && box.bottom - top > limit) {
      height = Math.max(height, box.bottom - top + 6);
      pictures = true;
    }
  }
  return { height, hidden: held, pictures };
}

/** The blocks of the words `element` holds: the paragraphs, lists and pictures of a text drawn
 *  from its reading, and the one paragraph of a text drawn as stored. */
function blocksOf(element: HTMLElement): Element[] {
  const drawn = element.querySelector(".dispatch-markdown");
  if (drawn !== null && drawn.children.length > 0) return [...drawn.children];
  const first = element.firstElementChild;
  return first === null ? [] : [first];
}
