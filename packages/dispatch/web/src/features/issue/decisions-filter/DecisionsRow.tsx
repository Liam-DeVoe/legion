import { type ReactNode, useRef, useState } from "react";
import { Link } from "react-router-dom";

import type { Artifact } from "../../../api/types";
import {
  pageFocusedRow,
  pageInkText,
  pageKindBullet,
  pageKindTag,
  pageMutedText,
  pageOwnWordsRule,
  pageQuietText,
  pageStateText,
  pageWordsText,
} from "../../../theme/classes";
import { DecisionActions } from "../DecisionActions";
import { NodeGlyph } from "../NodeGlyph";
import { shortDay } from "../node-time";
import { type ThreadMark, ThreadReadMark } from "../outline-thread-read";
import { useOutlineWriter } from "../outline-writer";
import { authorText, pageChrome } from "../page-structure";
import {
  type AuthorKind,
  CONTROL_LABELS,
  GLYPH_TOKENS,
  NODE_TYPE_NAMES,
  nodeStateWord,
} from "../page-words";
import { RowText } from "../RowText";
import { DECISIONS_WORDS, type DecisionStanding, type DecisionsEntry } from "./decisions-parts";
import { AskAnswer, NodeOptions, RowActs, useClamp, useRowOnScreen } from "./node-row-parts";

/** Who the page names: by a name and never an id, with the kind that picks their shape and
 *  whether they are the reader, whose name is drawn in the ink (R9, R34). */
export interface RowPerson {
  readonly kind: AuthorKind;
  readonly name: string;
  readonly reader: boolean;
}

/**
 * One row of the Decisions filter (dispatch://INFRA-1196 R2, R9, R34, R35, the prototype's
 * `.dcard`: "outline lines, the same as the conversation's").
 *
 * It is a row of the outline, drawn as `NodeRow.tsx` draws one and read by the same marks
 * (`[data-node-row]`, `[data-node-content]`, `[data-node-margin]`): its bullet in its kind's
 * colour, its words first and in full, and at the right end of its first line its kind as one
 * tag, who made it and when, and under them how it stands. From 761 px up the tag, the author
 * and the time are a column of their own; on a phone they stand at the right of the words'
 * first line.
 *
 * An ask draws what its row in the conversation draws, by the same parts: its question, cut at
 * six lines once it is answered, its options, which are the choices of its answer while it is
 * open, and the person's answer character for character as the store holds it, with no
 * sentence of the page's beside it. A declined ask says who declined it, and draws their note
 * where they wrote one: a question the person declined, never an answer.
 *
 * A decision draws its words in full, how it stands in the three states R2 names - "Waiting
 * for review", "Kept by" with the person's name, "Replaced" - and the statements its `shaped`
 * ties name, each a link to that statement. A decision of the review list draws Keep and
 * Change with no focus, as the picture does; no other row draws either (R35). The two controls
 * are `DecisionActions.tsx`'s own, so the writes are the ones every other Keep and Change of
 * the page makes.
 *
 * Every entry ends on a link to the node that holds it, in the conversation. Nothing inside a
 * row takes the tab order (R9): the row's own keys reach what it holds (`useTreeKeymap.ts`).
 */
export function DecisionsRow({
  answerer,
  author,
  body,
  decliner,
  document,
  entry,
  focused,
  isClosed,
  keeper,
  level,
  onFocus,
  onPin,
  ownAnswer,
  pinned,
  review,
  tabStop,
  threadRead,
}: {
  /** The name of the person whose answer this ask carries, where it carries one. */
  answerer?: string;
  /** Who made the node. */
  author: RowPerson;
  /** Whether this row draws what its focus adds: it has the focus, the reader pinned it open,
   *  or the cycle key opens every body. */
  body: boolean;
  /** The person who declined this ask, where it was declined. */
  decliner?: RowPerson;
  /** The decisions document a decision is a block of, which its Keep writes against. */
  document?: Artifact;
  entry: DecisionsEntry;
  focused: boolean;
  /** Whether the issue is closed, which Keep and Change are refused on. */
  isClosed: boolean;
  /** The name of the person who kept this decision, where it stands kept. */
  keeper?: string;
  /** How deep the row sits: 1, or 2 under the foot's own row (`aria-level`). */
  level: number;
  onFocus: () => void;
  onPin: () => void;
  /** Whether the answer this ask carries is the reader's own, which carries the ink rule. */
  ownAnswer: boolean;
  pinned: boolean;
  /** Whether this decision is an entry of the review list, which draws Keep and Change. */
  review: boolean;
  /** Whether this row is the list's one tab stop. */
  tabStop: boolean;
  /** The thread whose newest write this row draws, which a dwell on the row marks read. */
  threadRead?: ThreadMark;
}): ReactNode {
  const node = entry.node;
  const row = useRef<HTMLDivElement | null>(null);
  const words = useRef<HTMLDivElement | null>(null);
  // Where the reader writes: an open question's options and Answer set it (R16).
  const writer = useOutlineWriter();
  const press = useRowOnScreen(row, focused);
  // An answered question is cut at six lines; a question that waits on the reader and a
  // decision are drawn whole, as the conversation draws them (ruling 1).
  const cuttable = node.type === "ask" && node.state === "answered" && !node.waiting;
  const clamp = useClamp(words, cuttable);
  const [opened, setOpened] = useState(false);
  const clamped = clamp !== undefined && !opened && !body;
  const standing = entry.kind === "decision" ? entry.standing : undefined;
  const tone = entry.kind === "ask" ? "question" : "decision";
  return (
    <div
      aria-level={level}
      className="relative my-0.5 min-w-0 outline-none"
      data-node-id={node.id}
      data-node-row=""
      data-node-state={entry.kind === "ask" ? node.state : standing}
      data-node-type={node.type}
      data-row-key={node.id}
      onFocus={onFocus}
      onPointerDown={press}
      ref={row}
      role="treeitem"
      tabIndex={tabStop ? 0 : -1}
    >
      <div
        className={`relative grid min-w-0 grid-cols-[18px_minmax(0,1fr)_170px] gap-x-2 rounded-md py-0.5 pr-1.5 max-[1240px]:grid-cols-[18px_minmax(0,1fr)_160px] max-[760px]:flow-root max-[760px]:pr-1 max-[760px]:pl-[22px] ${
          focused ? pageFocusedRow : ""
        }`}
        data-node-card=""
      >
        <span
          className="col-start-1 row-start-1 flex h-6 w-[18px] items-center justify-center self-start max-[760px]:absolute max-[760px]:top-0.5 max-[760px]:left-0 max-[760px]:w-4"
          data-node-bullet=""
        >
          <i
            aria-hidden="true"
            className={`block size-[7px] rounded-full ${pageKindBullet[tone]}`}
          />
        </span>
        <RowMargin author={author} entry={entry} keeper={keeper} review={review} />
        <div className="col-start-2 row-start-1 min-w-0" data-node-content="">
          {/* A replaced decision's words are struck through and grey, beside the word that
              says so (the prototype's `.replaced`). */}
          <div
            className={`relative text-[15px] leading-6 ${
              standing === "replaced" ? `line-through ${pageQuietText}` : pageWordsText
            } ${clamped ? "overflow-clip" : ""}`}
            data-node-words=""
            ref={words}
            style={clamped ? { maxHeight: clamp.height } : undefined}
          >
            <RowText markdown={node.text} source={node.source} tree={node.tree} />
            {clamped && !clamp.pictures ? (
              <span
                aria-hidden="true"
                className="pointer-events-none absolute inset-x-0 bottom-0 h-8 bg-[linear-gradient(transparent,var(--color-page-paper))]"
              />
            ) : null}
          </div>
          {/* A press on it leaves the focus where it is, so the control does not move under
              the pointer before the press ends. It opens the rest in place, and closes it. */}
          {clamp === undefined ? null : (
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
          {node.options === undefined ? null : (
            <NodeOptions body={body} node={node} writer={writer} />
          )}
          {node.answer === undefined ? null : (
            <AskAnswer answerer={answerer ?? ""} node={node} own={ownAnswer} />
          )}
          {entry.kind !== "ask" || entry.declined === undefined || decliner === undefined ? null : (
            <Declined by={decliner} note={entry.declined.note} />
          )}
          {/* The node's parent in the conversation, as one line: its kind's word and its first
              line (R2, R1's first-line rule). */}
          {entry.parent === undefined ? null : (
            <p
              className={`flex min-w-0 items-baseline gap-1 text-xs leading-5 ${pageQuietText}`}
              data-node-parent-line=""
              data-parent-id={entry.parent.id}
              data-parent-type={entry.parent.type}
            >
              <span className="shrink-0" {...pageChrome}>
                {NODE_TYPE_NAMES[entry.parent.type]}
              </span>
              <span className="min-w-0 break-words" {...authorText}>
                {entry.parent.line}
              </span>
            </p>
          )}
          {entry.kind !== "decision" || entry.shapes.length === 0 ? null : (
            <p
              className={`flex min-w-0 flex-wrap items-baseline gap-x-2 text-xs leading-5 ${pageQuietText}`}
              data-decision-informs=""
            >
              <span className="shrink-0" {...pageChrome}>
                {DECISIONS_WORDS.informs}
              </span>
              {entry.shapes.map((shape) => (
                <span
                  className="min-w-0 break-words"
                  data-decision-statement={shape.id}
                  key={shape.id}
                >
                  {/* A sentence about the statement, so a colon sets it apart from the
                      statement's own words. */}
                  {shape.stale ? (
                    <span {...pageChrome}>{CONTROL_LABELS.target_stale}: </span>
                  ) : null}
                  {shape.path === undefined ? (
                    <span {...authorText}>{shape.line}</span>
                  ) : (
                    <Link
                      className={`dispatch-inline-link font-semibold hover:underline ${pageKindTag.statement}`}
                      tabIndex={-1}
                      to={shape.path}
                      {...authorText}
                    >
                      {shape.line}
                    </Link>
                  )}
                </span>
              ))}
            </p>
          )}
          <div
            className={`mt-1 mb-0.5 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2 ${DECISION_CONTROLS}`}
            data-entry-actions=""
          >
            {review ? (
              <DecisionActions
                document={document}
                isClosed={isClosed}
                nodeReference={node.reference}
              />
            ) : null}
            {entry.path === undefined ? null : (
              <Link
                className={`dispatch-inline-link text-xs leading-5 font-medium underline decoration-page-hair underline-offset-[3px] ${pageMutedText}`}
                data-entry-link=""
                tabIndex={-1}
                to={entry.path}
                {...pageChrome}
              >
                {DECISIONS_WORDS.in_conversation} <span aria-hidden="true">›</span>
              </Link>
            )}
          </div>
          {/* What the focus adds to an ask: what each option says beside its name and the rest
              of a cut question, with the control that keeps them open (R6). A decision's row
              holds nothing back, so it draws no body. */}
          {body && entry.kind === "ask" ? (
            <div
              className="mt-1 flex min-w-0 flex-col gap-1 text-sm"
              data-node-body=""
              data-pinned={pinned ? "" : undefined}
            >
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
          ) : null}
          {focused && writer !== undefined ? (
            <RowActs node={node} ownAnswer={ownAnswer} writer={writer} />
          ) : null}
        </div>
        {threadRead === undefined ? null : <ThreadReadMark mark={threadRead} />}
      </div>
    </div>
  );
}

/**
 * Keep and Change as the picture draws them (the prototype's `.dbtn`): Keep filled in the
 * decisions' violet, Change outlined in it, 24 px tall, on one line with the entry's link.
 * `DecisionActions.tsx` draws both controls in the outline's own look and is not edited by this
 * part, so the look is given from here, on the marks the controls carry (`ROW_ACTION_MARKS`);
 * the form Change opens, and what a refused write says, take a line of their own under them.
 * Integration moves the look into `DecisionActions.tsx` and drops these.
 */
const DECISION_CONTROLS = [
  "[&>p]:gap-x-3",
  "[&_button[data-row-action]]:rounded-[7px]",
  "[&_button[data-row-action]]:border-[1.5px]",
  "[&_button[data-row-action]]:border-page-decision",
  "[&_button[data-row-action]]:px-3",
  "[&_button[data-row-action]]:py-0.5",
  "[&_button[data-row-action]]:leading-5",
  "[&_button[data-row-action]]:font-semibold",
  "[&_button[data-row-action=keep]]:bg-page-decision",
  "[&_button[data-row-action=keep]]:text-page-paper",
  "[&_button[data-row-action=change]]:bg-page-paper",
  "[&_button[data-row-action=change]]:text-page-decision",
  "[&>form]:order-last",
  "[&>form]:basis-full",
  "[&>[role=alert]]:order-last",
  "[&>[role=alert]]:basis-full",
].join(" ");

/** The shape of each state of a decision, one `path` in a 16 by 16 box, as `NodeGlyph.tsx`
 *  draws every other shape of the page; a kept decision carries the shape of a person, who kept
 *  it. Integration moves them into `GLYPH_PATHS` under these tokens. */
const STANDING_SHAPES: Readonly<
  Record<DecisionStanding, { readonly path: string; readonly token: string }>
> = {
  kept: { path: "M8 2 a3 3 0 1 0 0.01 0 Z M2 15 a6 6 0 0 1 12 0", token: "decision-kept" },
  replaced: { path: "M2 11 h12 M4 4 l8 4 l-8 4 Z", token: "decision-replaced" },
  waiting_review: {
    path: "M1 8 a7 4 0 0 0 14 0 a7 4 0 0 0 -14 0 Z M8 6 v4",
    token: "decision-waiting-review",
  },
};

/**
 * What stands at the right end of the row's first line (R9, R34, the prototype's `.mg`): the
 * node's kind as one tag, who made it by a name and a shape, and when; and under them how it
 * stands, by a word and a shape (R6). An ask reads the word the conversation draws for its
 * state. A decision reads one of R2's three: "Waiting for review", "Kept by" with the name of
 * the person who kept it, or "Replaced". The state is drawn wherever the decision is, and in
 * the decisions' violet where it asks something of the reader or says what they did: on a
 * decision of the review list, and on one they kept (R35: "one rule decides the list and a
 * second rule decides the mark on a row").
 */
function RowMargin({
  author,
  entry,
  keeper,
  review,
}: {
  author: RowPerson;
  entry: DecisionsEntry;
  keeper: string | undefined;
  review: boolean;
}): ReactNode {
  const node = entry.node;
  const time = node.at === undefined ? "" : shortDay(node.at);
  const standing = entry.kind === "decision" ? entry.standing : undefined;
  const asked = entry.kind === "ask" ? node.state : undefined;
  const unsure = entry.kind === "decision" && node.unsure === true;
  const askGlyphs: Readonly<Record<string, string>> = GLYPH_TOKENS.states.ask;
  const askGlyph = asked === undefined ? undefined : askGlyphs[asked];
  return (
    <div
      className={`col-start-3 row-start-1 min-w-0 self-start text-right text-xs leading-6 max-[760px]:float-right max-[760px]:ml-2 max-[760px]:max-w-[60%] ${pageQuietText}`}
      data-node-margin=""
    >
      <div className="flex flex-wrap items-baseline justify-end gap-x-[7px]">
        <span
          className={`inline-flex items-center gap-1 font-semibold whitespace-nowrap ${
            pageKindTag[entry.kind === "ask" ? "question" : "decision"]
          }`}
          data-node-tag=""
        >
          <NodeGlyph className="size-3 shrink-0" token={GLYPH_TOKENS.types[node.type]} />
          <span {...pageChrome}>{NODE_TYPE_NAMES[node.type]}</span>
        </span>
        <span className="min-w-0 [overflow-wrap:anywhere]" data-node-byline="">
          <span
            className="inline-flex items-center gap-1"
            data-author-kind={author.kind}
            data-node-author=""
          >
            <NodeGlyph className="size-3 shrink-0" token={GLYPH_TOKENS.authors[author.kind]} />
            <span
              className={author.reader ? `font-semibold ${pageInkText}` : ""}
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
      {asked === undefined && standing === undefined && !unsure ? null : (
        <div className="flex flex-wrap items-center justify-end gap-x-1.5 leading-4 font-semibold">
          {asked === undefined ? null : (
            <span
              className={`inline-flex items-center gap-1 ${
                node.waiting ? pageStateText.waiting : pageStateText.quiet
              }`}
            >
              {askGlyph === undefined ? null : (
                <NodeGlyph className="size-3 shrink-0" token={askGlyph} />
              )}
              <span data-node-state={asked} {...pageChrome}>
                {nodeStateWord(asked)}
              </span>
            </span>
          )}
          {standing === undefined ? null : (
            <span
              className={`inline-flex items-center gap-1 ${
                review || standing === "kept" ? pageStateText.decision : pageStateText.quiet
              }`}
            >
              <svg
                aria-label={DECISIONS_WORDS.shapes[standing]}
                className="size-3 shrink-0"
                data-glyph={STANDING_SHAPES[standing].token}
                fill="none"
                role="img"
                stroke="currentColor"
                strokeWidth="1.5"
                viewBox="0 0 16 16"
              >
                <path d={STANDING_SHAPES[standing].path} />
              </svg>
              <span data-node-state={standing} {...pageChrome}>
                {DECISIONS_WORDS.states[standing]}
                {standing === "kept" && keeper !== undefined ? (
                  <>
                    {" "}
                    <span data-kept-by="" {...authorText}>
                      {keeper}
                    </span>
                  </>
                ) : null}
              </span>
            </span>
          )}
          {unsure ? (
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
 * That a person declined this question, under it (R2: "a declined ask reads as a question the
 * person declined and never as an answer"): who declined, by a name and a shape as every
 * author is drawn (R9), and the note they wrote where they wrote one, word for word. The
 * reader's own note carries the ink rule their own words carry (R34).
 */
function Declined({ by, note }: { by: RowPerson; note: string | undefined }): ReactNode {
  const own = by.reader && note !== undefined;
  return (
    <div
      className={`mt-1 flex min-w-0 flex-col ${own ? pageOwnWordsRule : ""}`}
      data-ask-declined=""
    >
      <p
        className={`flex min-w-0 flex-wrap items-center gap-x-1.5 text-xs leading-5 ${pageQuietText}`}
      >
        <span className="inline-flex items-center gap-1" data-declined-by="">
          <NodeGlyph className="size-3 shrink-0" token={GLYPH_TOKENS.authors[by.kind]} />
          <span className={by.reader ? `font-semibold ${pageInkText}` : ""} {...authorText}>
            {by.name}
          </span>
        </span>
        {/* A space between the two, so the line reads as one sentence to a reader who hears it
            or copies it: the gap the line draws is no character. */}{" "}
        <span {...pageChrome}>{DECISIONS_WORDS.declined}</span>
      </p>
      {note === undefined ? null : (
        <p
          className={`text-[15px] leading-6 whitespace-pre-wrap break-words ${own ? pageInkText : pageWordsText}`}
          data-declined-note=""
          {...authorText}
        >
          {note}
        </p>
      )}
    </div>
  );
}
