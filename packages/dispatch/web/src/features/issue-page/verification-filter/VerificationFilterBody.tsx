import {
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
  useCallback,
  useMemo,
  useRef,
  useState,
} from "react";

import type { Actor, CheckBlock } from "../../../api/types";
import {
  focusVisibleRing,
  pageFailedText,
  pageGuideBg,
  pageHairBorder,
  pageInkBg,
  pageInkBorder,
  pageInkText,
  pageKindBullet,
  pageKindTag,
  pageLineBorder,
  pageMutedText,
  pagePaneBg,
  pagePaperBg,
  pageQuietText,
  pageStateText,
  pageWordsText,
} from "../../../theme/classes";
import { useAgentText } from "../../refs/AgentName";
import { appKeymap } from "../../shell/keymap";
import { agentRoleWord } from "../agent-role";
import { CutWords } from "../CutWords";
import type { NodeChip, TreeNode } from "../issue-tree";
import { NodeFacts } from "../NodeFacts";
import { NodeGlyph } from "../NodeGlyph";
import { chipWords } from "../node-row-chip";
import { shortDay } from "../node-time";
import { authorText, pageChrome } from "../page-structure";
import {
  authorKind,
  type CheckState,
  CONTROL_LABELS,
  EMPTY_STATE_LINES,
  GAP_LINES,
  GLYPH_TOKENS,
  NODE_STATE_WORDS,
  NODE_TYPE_NAMES,
  type StatementState,
  sentenceCase,
  VERIFICATION_WORDS,
} from "../page-words";
import { RowText } from "../RowText";
import {
  type CheckReading,
  STATEMENT_STATES,
  type StatementReading,
  shortCommit,
  statementCounts,
  type VerificationReading,
  verificationReading,
} from "./statement-state";

/**
 * The Verification filter's body (dispatch://INFRA-1196 R17, R2, R10, R22, the approved picture
 * `page-v3-verification-1440.png` and the prototype's `.ver`).
 *
 * It is the picture: a summary of how the issue stands, then each statement with the checks
 * that serve it beside it and each check's latest result beside that. From 761 px up the three
 * stand in three columns, as the prototype's table draws them; at 760 px and below each
 * statement is a card and its checks stand under its words, as the prototype's `.vcards` draw
 * them (part V's R6). 760 px is the prototype's own break, so every break here is written from
 * 761 px up: Tailwind's `max-[760px]:` leaves 760 px itself out.
 *
 * All of it is one tree, named as the outline is (part V's R8). Its first row is the issue's
 * own, which holds the summary and the chips, so the page's `t k`, which focuses the tree's tab
 * stop, leaves the view's top on screen and the next Tab on the chip All; a focused tree element
 * would leave the top of a view taller than the screen above it. Then come each statement, with
 * its checks in one group under it, each check that serves no statement, and each other row.
 * One row holds the tree's tab stop (WAI-ARIA's roving tabindex): the issue's, until the stop
 * moves, and again when the row that held it is no longer drawn. The tree answers the outline's
 * keys (`j`, `k`, the arrows, Home, End, Enter, `o`) by a handler of its own, and the page's `?`
 * lists none of them: a binding of the page's keymap is a row of that list (part V's departure
 * 3).
 *
 * What it says of a statement and of a check is `statement-state.ts`, which is pure. Four
 * rules of R17 are drawn here:
 *
 * - **One commit, drawn once.** The summary names the commit the issue's results are read
 *   against; no result draws a commit of its own on its line, and a result recorded against
 *   another commit says so in its check's word, "Out of date".
 * - **Seven states, each a word and a shape.** A check's state is the page's own
 *   (`NODE_STATE_WORDS.check`), drawn with the shape its token carries (`NodeGlyph`), so no
 *   state is told from another by colour alone (R6).
 * - **Covered, never proven.** The summary counts the statements that carry a check as
 *   covered; no count, legend or heading of this body reads proven or met for it, because a
 *   statement that has a check is not a statement that is true.
 * - **Every value inside its card.** Each cell is a column of its own that may be zero wide
 *   and breaks inside a word where it has nothing else to break on, and a check's attributes
 *   and a result's are the two-column grid `NodeFacts.tsx` draws, which is what keeps a value
 *   from reaching past its card or over another at any width (the old tab's defect).
 *
 * A check's four attributes and what its result recorded are its node's facts
 * (`check-facts.ts`), drawn in the body that opens while the reader's focus is anywhere in the
 * check's row and while the reader has pressed it open (R6, part V's departure 5): a `run` as a
 * command with its own copy control, a screenshot as a picture, `when` as words. A statement no
 * check serves carries its gap in full, where the picture draws "No check proves this" (R10).
 */
export function VerificationFilterBody({
  answered = true,
  checks,
  context,
  emptyLine,
  view,
}: {
  /** Whether the coverage read and the results read have both answered. Until they have, the
   *  body draws its tree with no row and no line in it, so it never says that an issue has no
   *  checks before the reads that hold them are in (part V's R1); a failed read leaves it so,
   *  since the page has no word for one here. */
  answered?: boolean;
  /** The issue's checks with the results recorded against them, as the results read answers
   *  them (`GET /api/v1/issues/{key}/results`), which the page has already read for its tree. */
  checks: readonly CheckBlock[];
  /** The ids of the rows the filter draws as context, which under Verification is every
   *  statement (R2, `presets.ts`). */
  context: ReadonlySet<string>;
  /** What the body says where the filter's query left nothing to draw. */
  emptyLine: string;
  /** The tree as the Verification filter left it (`presetView`). */
  view: TreeNode;
  /** The Verification view with no words query: what the issue holds, whatever a query left of
   *  it. Where it is not given it is `view`, as it is where no query is typed. */
  whole?: TreeNode;
}): ReactNode {
  const read = useMemo(() => verificationReading({ checks, view }), [checks, view]);
  // Which state the reader narrowed the body to, as the picture's chips do; nothing is
  // narrowed until they press one.
  const [only, setOnly] = useState<StatementState | undefined>(undefined);
  const counts = statementCounts(read.statements);
  const shown =
    only === undefined
      ? read.statements
      : read.statements.filter((statement) => statement.state === only);
  const loose = only === undefined ? read.loose : [];
  const others = only === undefined ? read.others : [];
  // The issue's row is drawn where the view holds a row of any kind, so an issue with no
  // statement and no check draws no row at all, only the page's line for it (part V's R1).
  const holds = read.statements.length > 0 || read.loose.length > 0 || read.others.length > 0;
  const rows = answered && holds ? treeRows(view.id, shown, loose, others) : [];
  // The row that holds the tree's one tab stop, by its id in `rows`: the issue's at first. A
  // row the reader's narrowing took out hands it back to the issue's row, in this render rather
  // than in an effect after it, so the tree is never drawn without its stop (part V's R8).
  const [rovingId, setRovingId] = useState(view.id);
  if (rovingId !== view.id && !rows.includes(rovingId)) setRovingId(view.id);
  const tabIndexFor = (id: string): 0 | -1 => (id === rovingId ? 0 : -1);
  // Each drawn check's press, by its row's id in `rows`: true where the reader pressed its facts
  // open, false where they pressed them shut while the focus held them open, and no entry where
  // the focus alone decides (part V's departure 5). It is held here rather than in each check so
  // the tree's keys and a press on a statement's words press a check as its own words do. A row
  // that is no longer drawn forgets its press, as an unmounted row forgets its state.
  const [presses, setPresses] = useState<ReadonlyMap<string, boolean>>(() => new Map());
  if (presses.size > 0 && [...presses.keys()].some((id) => !rows.includes(id))) {
    setPresses(new Map([...presses].filter(([id]) => rows.includes(id))));
  }
  const press = useCallback((id: string, pressed: boolean | undefined) => {
    setPresses((held) => {
      if (held.get(id) === pressed) return held;
      const next = new Map(held);
      if (pressed === undefined) next.delete(id);
      else next.set(id, pressed);
      return next;
    });
  }, []);
  const tree = useRef<HTMLDivElement | null>(null);
  /** Each check row a press on `row` reaches, by its id in `rows`, and whether its facts are
   *  drawn, by the focus or by a press, as its row says (`aria-expanded`): a check's own row,
   *  or each check of a statement. */
  const checksOf = (row: Element): { drawn: boolean; id: string }[] => {
    // The tree draws its rows in the order `rows` names them, so the nth row element is the
    // nth id, as the outline's keys read theirs (`useTreeKeymap.ts`).
    const elements = [...(tree.current?.querySelectorAll(TREE_ROW) ?? [])];
    const reached = row.matches(CHECK_ROW)
      ? [row]
      : row.matches(STATEMENT_ROW)
        ? [...row.querySelectorAll(CHECK_ROW)]
        : [];
    return reached.flatMap((check) => {
      const id = rows[elements.indexOf(check)];
      return id === undefined
        ? []
        : [{ drawn: check.getAttribute("aria-expanded") === "true", id }];
    });
  };
  /** Enter on a check's row or a statement's, and a pointer's press on a statement's words (the
   *  tester's F21 and F22): each check's facts opened, or each shut where all of them are drawn.
   *  On a check's row that is the press its words take, the opposite of what is drawn. The
   *  answer is whether the row reached a check. */
  const pressChecks = (row: Element): boolean => {
    const reached = checksOf(row);
    const open = !reached.every((check) => check.drawn);
    for (const check of reached) press(check.id, open);
    return reached.length > 0;
  };
  /** `o`, on a row or on a control inside it: the facts of each check it reaches kept drawn once
   *  the focus leaves, or let go where the reader kept them, as the outline's `o` keeps a row's
   *  body open. The answer is whether the row reached a check. */
  const keepChecks = (row: Element): boolean => {
    const reached = checksOf(row);
    for (const check of reached) {
      press(check.id, presses.get(check.id) === true ? undefined : true);
    }
    return reached.length > 0;
  };
  // The tree's keys, which are the outline's (part V's R8 and departure 3). The page's keymap
  // listens on `window` and leaves a key a handler held back, so this handler holds back each
  // key it acts on, and only those: every other key reaches the keymap.
  const keyDown = (event: KeyboardEvent<HTMLElement>) => {
    // A key with Ctrl, Alt or Meta is the browser's or another binding's; a key that goes on
    // with a chord the reader began is the keymap's, so `t k` from inside the view turns it
    // off; and a key a control inside a row held back is that control's.
    if (event.altKey || event.ctrlKey || event.metaKey || event.defaultPrevented) return;
    if (appKeymap.pending() !== "") return;
    const target = event.target;
    if (!(target instanceof HTMLElement)) return;
    // A key typed into a field or into text the reader edits is typing, as the page's keymap
    // reads one (`shell/keymap.ts`). The view draws neither; a field a row draws keeps its keys.
    if (target.isContentEditable || target.matches("input, select, textarea")) return;
    const elements = [...event.currentTarget.querySelectorAll<HTMLElement>(TREE_ROW)];
    // The row the focus is on, or the one that holds the control it is on: the innermost, so a
    // check's words and the links in them are the check's, and a chip is the issue's.
    const row = target.closest<HTMLElement>(TREE_ROW);
    const at = row === null ? -1 : elements.indexOf(row);
    if (row === null || at === -1) return;
    const key = event.key;
    // Where a move goes: the next row or the one before, the first or the last, a statement's
    // first check, or the statement a check is drawn under. Right and Left go nowhere from any
    // other row.
    const to =
      key === "j" || key === "ArrowDown"
        ? elements[Math.min(at + 1, elements.length - 1)]
        : key === "k" || key === "ArrowUp"
          ? elements[Math.max(at - 1, 0)]
          : key === "Home"
            ? elements[0]
            : key === "End"
              ? elements.at(-1)
              : key === "ArrowRight" && row.matches(STATEMENT_ROW)
                ? row.querySelector<HTMLElement>(CHECK_ROW)
                : key === "ArrowLeft" && row.matches(CHECK_ROW)
                  ? row.parentElement?.closest<HTMLElement>(STATEMENT_ROW)
                  : undefined;
    if (to != null) {
      event.preventDefault();
      // The row the focus moves to takes the tree's one tab stop, so Tab back into the tree
      // lands on the row the reader left.
      const id = rows[elements.indexOf(to)];
      if (id !== undefined) setRovingId(id);
      to.focus();
      return;
    }
    // Enter is the row's only on the row itself: on a control inside it, a chip, a link or a
    // check's words, it is that control's own.
    const acted =
      key === "Enter" ? target === row && pressChecks(row) : key === "o" && keepChecks(row);
    if (acted) event.preventDefault();
  };
  return (
    <div className={BODY} data-verification-body="">
      {/* The page holds one tree on every address (R2), and it holds everything the view
          draws: the issue's row and the table, or in their place the one line that says the
          query left nothing. */}
      <div
        aria-label={CONTROL_LABELS.tree}
        className="flex min-w-0 flex-col"
        onKeyDown={keyDown}
        ref={tree}
        role="tree"
        tabIndex={-1}
      >
        {!answered ? null : !holds ? (
          <p className={`text-sm leading-5 ${pageQuietText}`} role="none" {...pageChrome}>
            {emptyLine}
          </p>
        ) : (
          <>
            <div
              aria-level={1}
              className="flex min-w-0 flex-col"
              data-node-id={view.id}
              data-node-row=""
              data-verification-issue=""
              role="treeitem"
              tabIndex={tabIndexFor(view.id)}
            >
              <Summary
                checks={read}
                commit={read.commit}
                counts={counts}
                covered={read.covered}
                total={read.statements.length}
              />
              <Chips
                counts={counts}
                onlyState={only}
                onPress={setOnly}
                total={read.statements.length}
              />
            </div>
            <div className={TABLE} data-verification-table="">
              <Headings />
              {shown.map((statement) => (
                <Statement
                  context={context.has(statement.node.id)}
                  key={statement.node.id}
                  onPress={press}
                  onPressChecks={pressChecks}
                  presses={presses}
                  statement={statement}
                  tabIndexFor={tabIndexFor}
                />
              ))}
              {/* What the table says where the state the reader narrowed to holds no statement,
                  rather than headings over nothing. */}
              {only !== undefined && shown.length === 0 ? (
                <p
                  className={`${CELL} text-sm leading-5 ${pageQuietText}`}
                  data-verification-none=""
                  role="none"
                  {...pageChrome}
                >
                  {EMPTY_STATE_LINES.verification_none}
                </p>
              ) : null}
              {loose.map((check) => (
                <Check
                  check={check}
                  key={check.node.id}
                  loose={true}
                  onPress={press}
                  pressed={presses.get(check.node.id)}
                  row={check.node.id}
                  tabIndex={tabIndexFor(check.node.id)}
                />
              ))}
              {others.map((node) => (
                <OtherRow key={node.id} node={node} tabIndex={tabIndexFor(node.id)} />
              ))}
            </div>
          </>
        )}
      </div>
    </div>
  );
}

/** The ids of the rows the tree draws, in the order they stand on the page: the issue's row,
 *  each statement followed by its checks, each check that serves no statement, and each other
 *  row. The tree's tab stop is one of them (part V's R8). */
function treeRows(
  issue: string,
  statements: readonly StatementReading[],
  loose: readonly CheckReading[],
  others: readonly TreeNode[]
): string[] {
  return [
    issue,
    ...statements.flatMap((statement) => [
      statement.node.id,
      ...statement.checks.map((check) => checkRowId(statement.node.id, check.node.id)),
    ]),
    ...loose.map((check) => check.node.id),
    ...others.map((node) => node.id),
  ];
}

/** The id of a check's row under a statement. A check that serves two statements is drawn
 *  under each, so its node's id names two rows, and the statement's id beside it names one. */
function checkRowId(statement: string, check: string): string {
  return `${statement} ${check}`;
}

/** The seven states a check can be in, in the order the page declares them. */
const CHECK_STATES = Object.keys(NODE_STATE_WORDS.check) as CheckState[];
/** A column heading, in the prototype's `.cap`. */
const CAP = `text-xs leading-4 font-semibold tracking-[0.08em] uppercase ${pageQuietText}`;

/** The body's measure (part V's R6, the prototype's `.ver`): at 760 px and below the width of
 *  the element around it less 20 px, 10 px under what stands above it; from 761 px up 1180 px
 *  wide, or that width less 48 px where that is less, 22 px under it. At every width it stands
 *  in the middle of that element with 120 px under it. */
const BODY =
  "mx-auto mt-2.5 mb-[120px] flex w-full min-w-0 max-w-[calc(100%-20px)] flex-col min-[761px]:mt-[22px] min-[761px]:w-[1180px] min-[761px]:max-w-[calc(100%-48px)]";

/** The table (the prototype's `.vt`): from 761 px up one frame, 1 px round it with 10 px
 *  corners, its rows bands inside it; at 760 px and below no frame, and each row a card of its
 *  own 8 px from the next (`.vcards`, part V's R6). */
const TABLE = `flex min-w-0 flex-col gap-2 ${pageLineBorder} min-[761px]:gap-0 min-[761px]:overflow-hidden min-[761px]:rounded-[10px] min-[761px]:border min-[761px]:bg-page-paper`;

/** One row of the table: from 761 px up a band of the frame with a rule under it but the last;
 *  at 760 px and below a card of its own, 1 px round it with 8 px corners (`.vcard`). */
const ROW = `rounded-[8px] border ${pageLineBorder} ${pagePaperBg} min-[761px]:rounded-none min-[761px]:border-x-0 min-[761px]:border-t-0 min-[761px]:last:border-b-0`;

/** The statement column and the rest, as the prototype's table divides them from 761 px up
 *  (38% and the rest); at 760 px and below one column, where each statement is a card and its
 *  checks stand under its words. It leaves out `grid` itself, since the headings are drawn
 *  only from 761 px up. */
const STATEMENT_COLUMNS =
  "min-w-0 grid-cols-[minmax(0,1fr)] min-[761px]:grid-cols-[minmax(0,38%)_minmax(0,62%)]";

/** The check column and its result, inside the rest (36% and 26% of the whole) from 761 px up;
 *  at 760 px and below the result stands under the check's words. */
const CHECK_COLUMNS =
  "grid min-w-0 grid-cols-[minmax(0,1fr)] min-[761px]:grid-cols-[minmax(0,58%)_minmax(0,42%)]";

/** One cell of the table, in the prototype's `.vt td`. */
const CELL = "min-w-0 px-3 py-[11px]";

/** What a pointer's press inside a check's words, its latest result's cell or a statement's
 *  words belongs to rather than to the row: a link or a button, whose press is its own (a link
 *  in the words, a statement's "more" control, the copy control, the line of earlier results),
 *  and the drawn facts, where a reader reads and selects as in the facts beside the words
 *  (part V's departure 5). */
const OWN_PRESS = "a, button, [data-node-body]";

/** Every row of the tree, which it draws in the order `treeRows` names them; a check's row, in
 *  its statement's group or of the table's own; and a statement's row. */
const TREE_ROW = "[data-node-row]";
const CHECK_ROW = "[data-verification-check]";
const STATEMENT_ROW = "[data-verification-card]";

/** The colour each state is drawn in, which repeats what its word and its shape already say
 *  (R6): met in the passed green, not met in the failed red, unknown quiet. */
const STATEMENT_TONES: Readonly<Record<StatementState, string>> = {
  met: pageStateText.passed,
  not_met: pageStateText.failed,
  unknown: pageStateText.quiet,
};

/** The fill each state takes in the summary's bar, which is decoration: the counts beside it
 *  say the same in words. */
const STATEMENT_FILLS: Readonly<Record<StatementState, string>> = {
  met: "bg-page-passed",
  not_met: "bg-page-failed",
  unknown: "bg-page-hair",
};

/** How a check's state is coloured: the passed green and the failed red the page already draws
 *  a check in, and the quiet grey for the five that are neither. */
function checkTone(state: CheckState): string {
  if (state === "passed") return pageStateText.passed;
  return state === "failed" ? pageStateText.failed : pageStateText.quiet;
}

/**
 * How the issue stands, in one card over the table (the prototype's `.vsum`): how many
 * statements carry a check, the bar of how they stand, the count in each state, the one commit
 * the page names for the issue's results, drawn here and nowhere else (R17), and how many of
 * the checks it draws stand in each of the seven states a check can be in, each by its word and
 * its shape, so the shapes the table draws are named once over it (R6).
 */
function Summary({
  checks,
  commit,
  counts,
  covered,
  total,
}: {
  /** The checks the body draws, each counted once however many statements it serves. */
  checks: Pick<VerificationReading, "loose" | "statements">;
  commit: string | undefined;
  counts: Readonly<Record<StatementState, number>>;
  covered: number;
  /** How many statements the filter drew, which the covered count is out of. */
  total: number;
}): ReactNode {
  const byState = new Map<string, CheckState | undefined>();
  for (const check of [
    ...checks.statements.flatMap((statement) => statement.checks),
    ...checks.loose,
  ]) {
    byState.set(check.node.id, check.state);
  }
  const inState = (state: CheckState): number =>
    [...byState.values()].filter((held) => held === state).length;
  return (
    <div
      className={`flex min-w-0 flex-wrap items-center gap-[18px] rounded-[10px] border px-[18px] py-3.5 ${pageLineBorder} ${pagePaperBg}`}
      data-verification-summary=""
    >
      <p className={`text-sm leading-5 font-semibold ${pageInkText}`} data-verification-covered="">
        <span {...authorText}>{covered}</span> <span {...pageChrome}>{VERIFICATION_WORDS.of}</span>{" "}
        <span {...authorText}>{total}</span>{" "}
        <span {...pageChrome}>{VERIFICATION_WORDS.covered}</span>
      </p>
      <span
        aria-hidden="true"
        className={`flex h-3 min-w-[220px] flex-1 overflow-hidden rounded-[6px] ${pageGuideBg}`}
        data-verification-bar=""
      >
        {STATEMENT_STATES.filter((state) => counts[state] > 0).map((state) => (
          <i
            className={`block h-full ${STATEMENT_FILLS[state]}`}
            key={state}
            style={{ width: `${(counts[state] / Math.max(total, 1)) * 100}%` }}
          />
        ))}
      </span>
      <p
        className={`flex min-w-0 flex-wrap gap-[14px] text-sm leading-5 ${pageMutedText}`}
        data-verification-legend=""
      >
        {STATEMENT_STATES.map((state) => (
          <span className="inline-flex items-center gap-[5px]" key={state}>
            <NodeGlyph
              className={`size-3 shrink-0 ${STATEMENT_TONES[state]}`}
              token={GLYPH_TOKENS.statements[state]}
            />
            <span className={pageInkText} {...authorText}>
              {counts[state]}
            </span>
            <span {...pageChrome}>{VERIFICATION_WORDS.states[state]}</span>
          </span>
        ))}
      </p>
      <p
        className={`flex min-w-0 basis-full flex-wrap gap-x-[14px] gap-y-1 text-xs leading-5 ${pageMutedText}`}
        data-verification-check-legend=""
      >
        {CHECK_STATES.map((state) => (
          <span
            className="inline-flex items-center gap-[5px]"
            data-check-legend-state={state}
            key={state}
          >
            <NodeGlyph
              className={`size-3 shrink-0 ${checkTone(state)}`}
              token={GLYPH_TOKENS.states.check[state]}
            />
            <span className={pageInkText} {...authorText}>
              {inState(state)}
            </span>
            <span {...pageChrome}>{NODE_STATE_WORDS.check[state]}</span>
          </span>
        ))}
      </p>
      {commit === undefined ? null : (
        <p className={`min-w-0 text-sm leading-5 ${pageMutedText}`} data-verification-commit="">
          <span {...pageChrome}>{VERIFICATION_WORDS.commit}</span>{" "}
          <code className={`font-mono break-all ${pageInkText}`} {...authorText}>
            {shortCommit(commit)}
          </code>
        </p>
      )}
    </div>
  );
}

/** The chips over the table (the prototype's `.vchips`): All, and one per state a statement can
 *  be in with how many stand in it. Pressing one draws those statements alone, pressing it
 *  again or pressing All draws them all, as the filter row's own controls work (R2). */
function Chips({
  counts,
  onlyState,
  onPress,
  total,
}: {
  counts: Readonly<Record<StatementState, number>>;
  onlyState: StatementState | undefined;
  onPress: (state: StatementState | undefined) => void;
  total: number;
}): ReactNode {
  const chip = (on: boolean): string =>
    `rounded-full border px-3 py-0.5 text-sm leading-5 font-semibold ${focusVisibleRing} ${
      on
        ? `${pageInkBg} ${pageInkBorder} text-page-paper`
        : `${pageHairBorder} ${pagePaperBg} ${pageMutedText}`
    }`;
  return (
    <div className="my-[14px] flex min-w-0 flex-wrap gap-1.5" data-verification-chips="">
      <button
        aria-pressed={onlyState === undefined}
        className={chip(onlyState === undefined)}
        data-verification-chip="all"
        onClick={() => onPress(undefined)}
        type="button"
        {...pageChrome}
      >
        {VERIFICATION_WORDS.all}{" "}
        <span className="text-xs font-medium" {...authorText}>
          {total}
        </span>
      </button>
      {STATEMENT_STATES.map((state) => (
        <button
          aria-pressed={onlyState === state}
          className={chip(onlyState === state)}
          data-verification-chip={state}
          // A chip with nothing in its state draws nothing, so it is no press to offer.
          disabled={counts[state] === 0 && onlyState !== state}
          key={state}
          onClick={() => onPress(onlyState === state ? undefined : state)}
          type="button"
          {...pageChrome}
        >
          {VERIFICATION_WORDS.states[state]}{" "}
          <span className="text-xs font-medium" {...authorText}>
            {counts[state]}
          </span>
        </button>
      ))}
    </div>
  );
}

/** The three column headings, in the prototype's `.vt th`, drawn from 761 px up only: at 760 px
 *  and below the columns stack and each statement's card carries its own words (part V's R6).
 *  They name the columns and are no row of the tree. */
function Headings(): ReactNode {
  return (
    <div
      className={`hidden border-b ${STATEMENT_COLUMNS} ${pageLineBorder} ${pagePaneBg} min-[761px]:grid`}
      data-verification-head=""
      role="none"
    >
      <span className={`${CELL} ${CAP}`} {...pageChrome}>
        {sentenceCase(NODE_TYPE_NAMES.requirement)}
      </span>
      <span className={CHECK_COLUMNS}>
        <span className={`${CELL} ${CAP}`} {...pageChrome}>
          {VERIFICATION_WORDS.checks}
        </span>
        <span className={`${CELL} ${CAP}`} {...pageChrome}>
          {VERIFICATION_WORDS.latest_result}
        </span>
      </span>
    </div>
  );
}

/**
 * One statement with the checks that serve it (the prototype's `.vt tr`, and its `.vcard` at
 * 760 px and below): how it stands at the commit the page names, its words, the node its words
 * came from as one line, and its gap where no check serves it (R1's ruling, R2, R10, R17). Its
 * words are cut as an agent's are on every row (ruling 6 of build-rulings-md: a twenty-line
 * statement opened this view on a wall of text).
 *
 * It is a row of the tree, and its checks are one group of rows under it, drawn open; a
 * statement no check of this issue serves holds no group and so says nothing of being open
 * (part V's R8). A pointer's press on its words cell does what Enter on its row does: it opens
 * the facts of each of its checks, or shuts them where all of them are drawn (the tester's
 * F21); a press on its "more" control or on a link in its words is that control's own. The
 * control and the links are tab stops, as every control of the view is (R8). As a card its left
 * edge is the statement's teal, or the failed red where it reads Not met or no check serves it,
 * so the card says so before its words are read (R6).
 */
function Statement({
  context,
  onPress,
  onPressChecks,
  presses,
  statement,
  tabIndexFor,
}: {
  context: boolean;
  /** Sets the press of the check row with this id (the body's `press`). */
  onPress: (row: string, pressed: boolean | undefined) => void;
  /** Presses the facts of the checks of this statement's row, as Enter on it does (the body's
   *  `pressChecks`). */
  onPressChecks: (row: Element) => void;
  /** Each drawn check's press, by its row's id. */
  presses: ReadonlyMap<string, boolean>;
  statement: StatementReading;
  /** The tab index of the row with this id, which is 0 for the tree's one tab stop. */
  tabIndexFor: (id: string) => 0 | -1;
}): ReactNode {
  const node = statement.node;
  const failing = statement.state === "not_met" || node.gaps.includes("no_check");
  return (
    <div
      aria-expanded={statement.checks.length === 0 ? undefined : true}
      aria-level={1}
      className={`grid ${STATEMENT_COLUMNS} ${ROW} border-l-4 ${failing ? "border-l-page-failed" : "border-l-page-statement"}`}
      data-node-context={context ? "" : undefined}
      data-node-id={node.id}
      data-node-row=""
      data-node-type={node.type}
      data-statement-state={statement.state}
      data-verification-card=""
      role="treeitem"
      tabIndex={tabIndexFor(node.id)}
    >
      {/* biome-ignore lint/a11y/noStaticElementInteractions lint/a11y/useKeyWithClickEvents: a
          pointer's way to what Enter on the statement's row does; the cell is no control of its
          own (part V's R8). */}
      <div
        className={`flex min-w-0 flex-col ${CELL}`}
        onClick={(event) => {
          const owner = event.target instanceof Element ? event.target.closest(OWN_PRESS) : null;
          if (owner !== null && event.currentTarget.contains(owner)) return;
          const row = event.currentTarget.closest(TREE_ROW);
          if (row !== null) onPressChecks(row);
        }}
      >
        <p
          className={`mb-0.5 flex items-center gap-1.5 text-xs leading-5 font-semibold ${STATEMENT_TONES[statement.state]}`}
        >
          <NodeGlyph
            className="size-3.5 shrink-0"
            token={GLYPH_TOKENS.statements[statement.state]}
          />
          <span data-statement-state={statement.state} {...pageChrome}>
            {VERIFICATION_WORDS.states[statement.state]}
          </span>
        </p>
        <CutWords
          className={`min-w-0 text-[15px] leading-6 ${pageWordsText}`}
          tabStops
          whole={false}
        >
          <RowText markdown={node.text} source={node.source} tabStops tree={node.tree} />
        </CutWords>
        <ParentLine node={node} />
        <Gaps node={node} />
      </div>
      <div className="flex min-w-0 flex-col">
        {statement.checks.length === 0 ? null : (
          // biome-ignore lint/a11y/useSemanticElements: a treeitem owns its child rows through a group, and a fieldset groups form fields.
          <div className="flex min-w-0 flex-col" role="group">
            {statement.checks.map((check) => {
              const row = checkRowId(node.id, check.node.id);
              return (
                <Check
                  check={check}
                  key={`${check.node.id}:${check.latest?.id ?? ""}`}
                  loose={false}
                  onPress={onPress}
                  pressed={presses.get(row)}
                  row={row}
                  tabIndex={tabIndexFor(row)}
                />
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}

/**
 * One check beside its latest result (the prototype's `.vc` and `.vres`): how it stands, by a
 * word and a shape, its own words, and the four attributes its body draws (R15, R17). Its
 * earlier results are one line with their count, which opens them in place (ruling 6 of
 * build-rulings-md: the approved picture draws one result for each check), and the result the
 * check's state stands on is marked wherever it is drawn.
 *
 * The facts of the check and of its results are drawn while the reader's focus is anywhere in
 * the row - its words, a link in them, its facts, its results, the line of earlier results or
 * the row itself - and while the reader has pressed them open, which is the rule the outline
 * draws a focused node's body by (R6, part V's departure 5). A pointer's press on the words or
 * on the latest result's cell, or Enter or Space on the words, opens them where they are shut
 * and shuts them where they are open, the focus inside or not, so the row says what it does
 * (`aria-expanded`) and does it; a press on a link or a button of the row is that control's
 * own. The words are a tab stop with no role: their links are tab stops too, and no control
 * holds another (part V's R8). So a reader reaches every fact of a check and of its results by
 * the keyboard alone, and the table stays the table the picture draws.
 *
 * It is a row of the tree, across its words and its latest result: a row of the group under
 * its statement, or, for a check that serves no statement, a row of the table of its own
 * (`loose`). The row says whether its facts are drawn (part V's R8), which is what the tree's
 * keys read: Enter on the row itself presses it as its words do, Enter on its statement's row
 * opens or shuts it with the statement's other checks, and `o` keeps it open or lets it go (the
 * body's `pressChecks` and `keepChecks`).
 */
function Check({
  check,
  loose,
  onPress,
  pressed,
  row,
  tabIndex,
}: {
  check: CheckReading;
  /** Whether the check serves no statement, and so stands as a row of the table itself. */
  loose: boolean;
  /** Sets this row's press (the body's `press`). */
  onPress: (row: string, pressed: boolean | undefined) => void;
  /** The reader's own press: open, shut, or neither, in which case the focus decides. */
  pressed: boolean | undefined;
  /** The row's id in the tree: a check drawn under two statements is two rows. */
  row: string;
  /** 0 where this row holds the tree's one tab stop, and -1 everywhere else. */
  tabIndex: 0 | -1;
}): ReactNode {
  const [focusInside, setFocusInside] = useState(false);
  const [earlierShown, setEarlierShown] = useState(false);
  // Whether the facts were drawn when the pointer went down: the focus its press puts in the row
  // draws them before the click, and the click must shut only what was drawn before it.
  const drawnAtPress = useRef<boolean | undefined>(undefined);
  const node = check.node;
  const surface = node.facts.find((fact) => fact.name === "surface")?.value ?? "";
  const opened = pressed ?? focusInside;
  const pointerPress = (event: MouseEvent<HTMLElement>) => {
    const drawn = drawnAtPress.current ?? opened;
    drawnAtPress.current = undefined;
    const owner = event.target instanceof Element ? event.target.closest(OWN_PRESS) : null;
    if (owner !== null && event.currentTarget.contains(owner)) return;
    onPress(row, !drawn);
  };
  return (
    <div
      aria-expanded={opened}
      aria-level={loose ? 1 : 2}
      className={`${CHECK_COLUMNS} ${loose ? ROW : `border-t first:border-t-0 ${pageLineBorder}`}`}
      data-node-id={node.id}
      data-node-row=""
      data-node-state={check.state}
      data-node-type={node.type}
      data-verification-check=""
      data-verification-loose={loose ? "" : undefined}
      onBlur={(event) => {
        const to = event.relatedTarget;
        if (to instanceof Node && event.currentTarget.contains(to)) return;
        setFocusInside(false);
        // A shut the reader pressed lasts while the focus stays; the next visit opens again.
        if (pressed === false) onPress(row, undefined);
      }}
      onFocus={() => setFocusInside(true)}
      role="treeitem"
      tabIndex={tabIndex}
    >
      <div className={CELL}>
        {/* biome-ignore lint/a11y/noStaticElementInteractions: the words hold links that are tab
            stops of their own, so no role may make them a control that holds controls; the row's
            `aria-expanded` says what a press does (part V's R8). */}
        <div
          className={`flex min-w-0 items-baseline gap-2 text-sm leading-5 ${pageWordsText} ${focusVisibleRing}`}
          data-verification-check-words=""
          onClick={pointerPress}
          onKeyDown={(event: KeyboardEvent<HTMLElement>) => {
            // A key on a link in the words is the link's own.
            if (event.target !== event.currentTarget) return;
            if (event.key !== "Enter" && event.key !== " ") return;
            if (event.altKey || event.ctrlKey || event.metaKey) return;
            // Space presses the words and does not scroll the page.
            event.preventDefault();
            onPress(row, !opened);
          }}
          onMouseDown={() => {
            drawnAtPress.current = opened;
          }}
          // biome-ignore lint/a11y/noNoninteractiveTabindex: a check's words are a tab stop of the view (part V's R8).
          tabIndex={0}
        >
          <span
            aria-hidden="true"
            className={`mt-1.5 block size-[7px] shrink-0 rounded-full ${pageKindBullet.check}`}
          />
          <div className="min-w-0 break-words" data-node-words="">
            <RowText markdown={node.text} source={node.source} tabStops tree={node.tree} />
          </div>
        </div>
        {surface === "" ? null : (
          <p
            className={`mt-0.5 text-xs leading-[18px] font-semibold ${pageKindTag.check}`}
            data-check-surface=""
            {...authorText}
          >
            {surface}
          </p>
        )}
        <AlsoUnder chips={node.alsoUnder} />
        <Gaps node={node} />
        {opened ? (
          <div className="mt-1 min-w-0" data-node-body="">
            <NodeFacts facts={node.facts} tabStops />
          </div>
        ) : null}
      </div>
      {/* biome-ignore lint/a11y/noStaticElementInteractions lint/a11y/useKeyWithClickEvents: a
          pointer's way to the press the check's words take by Enter or Space; the cell is no
          control of its own (part V's departure 5). */}
      <div
        className={CELL}
        onClick={pointerPress}
        onMouseDown={() => {
          drawnAtPress.current = opened;
        }}
      >
        {check.state === undefined ? null : (
          <p
            className={`flex items-center gap-1.5 text-xs leading-5 font-semibold ${checkTone(check.state)}`}
          >
            <NodeGlyph
              className="size-3.5 shrink-0"
              token={GLYPH_TOKENS.states.check[check.state]}
            />
            <span data-node-state={check.state} {...pageChrome}>
              {NODE_STATE_WORDS.check[check.state]}
            </span>
          </p>
        )}
        {check.latest === undefined ? null : (
          <LatestResult
            opened={opened}
            result={check.latest}
            stands={check.standsOn === check.latest.id}
          />
        )}
        {check.earlier.length === 0 ? null : (
          <button
            aria-expanded={earlierShown}
            className={`dispatch-inline-link mt-1 text-xs leading-5 font-medium underline decoration-page-hair underline-offset-[3px] ${pageQuietText}`}
            data-verification-earlier=""
            onClick={() => setEarlierShown(!earlierShown)}
            type="button"
            {...pageChrome}
          >
            <span {...authorText}>{check.earlier.length.toLocaleString()}</span>{" "}
            {check.earlier.length === 1
              ? VERIFICATION_WORDS.earlier_one
              : VERIFICATION_WORDS.earlier_many}
          </button>
        )}
        {earlierShown
          ? check.earlier.map((result) => (
              <LatestResult
                key={result.id}
                opened={opened}
                result={result}
                stands={check.standsOn === result.id}
              />
            ))
          : null}
      </div>
    </div>
  );
}

/** One result of a check: who recorded it and when, what it observed, whether the check's state
 *  stands on it, and what it holds that no line has room for, in the body the check's row opens
 *  (R6, R9, R15). What it observed is drawn as a node's own text is, through `RowText`, so no
 *  stored text of the view is drawn as raw characters (part V's departure 7). The commit it ran
 *  at is the one the summary names, drawn there and not here (R17). */
function LatestResult({
  opened,
  result,
  stands,
}: {
  opened: boolean;
  result: TreeNode;
  stands: boolean;
}): ReactNode {
  return (
    <div
      className="mt-1 min-w-0"
      data-node-id={result.id}
      data-node-type={result.type}
      data-result-stands={stands ? "" : undefined}
      data-verification-result=""
    >
      <p className={`min-w-0 text-xs leading-[18px] [overflow-wrap:anywhere] ${pageQuietText}`}>
        {stands ? (
          <>
            <span className={`font-semibold ${pageInkText}`} {...pageChrome}>
              {VERIFICATION_WORDS.stands}
            </span>
            <span {...pageChrome}> · </span>
          </>
        ) : null}
        <Author actor={result.author} />
        {result.at === undefined ? null : (
          <>
            <span {...pageChrome}> · </span>
            <span className="whitespace-nowrap" data-node-time="" {...authorText}>
              {shortDay(result.at)}
            </span>
          </>
        )}
      </p>
      {result.text === "" ? null : (
        <div
          className={`mt-0.5 min-w-0 text-sm leading-5 break-words ${pageWordsText}`}
          data-node-words=""
        >
          <RowText markdown={result.text} tabStops />
        </div>
      )}
      {opened ? (
        <div className="mt-1 min-w-0" data-node-body="">
          <NodeFacts facts={result.facts} tabStops />
        </div>
      ) : null}
    </div>
  );
}

/** Who made something, by a name and a shape, as every other row of the page draws an author
 *  (R9): a person by their login, an agent by its role's word as the outline's rows name one
 *  (`agent-role.ts`), never an identifier, and the page's own words where the model holds none.
 *  An agent's whole title is drawn nowhere in the view, which opens no body that would give it
 *  (part V's R7). */
function Author({ actor }: { actor: Actor | undefined }): ReactNode {
  const agentText = useAgentText();
  const kind = authorKind(actor);
  return (
    <span className="inline-flex min-w-0 items-center gap-1" data-node-author="">
      <NodeGlyph className="size-3 shrink-0" token={GLYPH_TOKENS.authors[kind]} />
      <span className="min-w-0 break-words" {...(actor === undefined ? pageChrome : authorText)}>
        {actor === undefined
          ? CONTROL_LABELS.unknown_author
          : kind === "agent"
            ? agentRoleWord(agentText.author(actor))
            : agentText.author(actor)}
      </span>
    </span>
  );
}

/** The other statements a check is drawn under, each named by its first words as every chip of
 *  the page names its target (R4, `node-row-chip.ts`): a check serves several statements and
 *  this body draws it under each. A target the reference read has not named draws nothing. */
function AlsoUnder({ chips }: { chips: readonly NodeChip[] }): ReactNode {
  const named = chips.filter((chip) => chip.name !== "");
  if (named.length === 0) return null;
  return (
    <p
      className={`mt-0.5 min-w-0 text-xs leading-[18px] [overflow-wrap:anywhere] ${pageQuietText}`}
      data-node-trail=""
    >
      <span {...pageChrome}>{CONTROL_LABELS.also_under} </span>
      {named.map((chip, place) => (
        <span data-node-chip="" key={chip.ref}>
          {place === 0 ? null : <span {...pageChrome}>, </span>}
          <span {...authorText}>{chipWords(chip.name)}</span>
        </span>
      ))}
    </p>
  );
}

/** Each gap the node carries, said in full where the picture draws "No check proves this"
 *  (R10): a statement no check serves, and a check whose `serves` names no statement. */
function Gaps({ node }: { node: TreeNode }): ReactNode {
  if (node.gaps.length === 0) return null;
  return (
    <>
      {node.gaps.map((gap) => (
        <p
          className={`mt-1 min-w-0 text-xs leading-[18px] font-semibold ${pageFailedText}`}
          data-gap=""
          data-gap-kind={gap}
          key={gap}
          {...pageChrome}
        >
          {GAP_LINES[gap]}
        </p>
      ))}
    </>
  );
}

/** The one line a row draws for the node its words came from, where the filter draws that node
 *  as no row (R2, R1's first-line rule): its kind and its first line. */
function ParentLine({ node }: { node: TreeNode }): ReactNode {
  if (node.parentLine === undefined) return null;
  return (
    <p
      className={`mt-0.5 flex min-w-0 items-baseline gap-1 text-xs leading-5 ${pageQuietText}`}
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
  );
}

/** A row the filter kept that is neither a statement nor a check: a node an address named,
 *  which outranks the filter (R20). It is drawn by its kind and its words, so nothing the
 *  filter drew is lost (R19). It is a row of the tree and of the table, as a check that serves
 *  no statement is. */
function OtherRow({ node, tabIndex }: { node: TreeNode; tabIndex: 0 | -1 }): ReactNode {
  return (
    <div
      aria-level={1}
      className={`${CELL} ${ROW}`}
      data-node-id={node.id}
      data-node-row=""
      data-node-type={node.type}
      data-verification-other=""
      role="treeitem"
      tabIndex={tabIndex}
    >
      <p className={`text-xs leading-5 ${pageQuietText}`} {...pageChrome}>
        {NODE_TYPE_NAMES[node.type]}
      </p>
      <div className={`min-w-0 text-[15px] leading-6 ${pageWordsText}`} data-node-words="">
        <RowText markdown={node.text} source={node.source} tree={node.tree} />
      </div>
      <ParentLine node={node} />
    </div>
  );
}
