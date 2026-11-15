import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useMemo } from "react";
import { Link } from "react-router-dom";

import type { Actor, IssueDetails } from "../../../api/types";
import {
  pageDecisionText,
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
import { issueDecisionsQuery } from "../../doc/spec-marks";
import { useAgentText } from "../../refs/AgentName";
import { buildReferencePath, parseNodeReference } from "../../refs/routes";
import type { TreeNode } from "../issue-tree";
import { NodeGlyph } from "../NodeGlyph";
import { shortDay } from "../node-time";
import { authorText, freeze, pageChrome } from "../page-structure";
import {
  authorKind,
  CONTROL_LABELS,
  EMPTY_STATE_LINES,
  GAP_WORDS,
  GLYPH_TOKENS,
  NODE_TYPE_NAMES,
  nodeStateWord,
  sentenceCase,
} from "../page-words";
import { RowText } from "../RowText";
import { treeContext } from "../tree-drafts";
import {
  type SpecDecision,
  type SpecGroup,
  type SpecMark,
  type SpecParent,
  type SpecStatement,
  specDecidedBy,
} from "./spec-decided-by";

/**
 * The Spec filter's body (dispatch://INFRA-1196 R5, R17, R22, R35, the prototype's `.spec`).
 *
 * It is the approved picture `page-v3-spec-1440.png`: a legend of the four marks a statement
 * carries, then two columns, Decided by and Statement. In the left column stands, once per run
 * of statements one decision shaped, that decision in the words it came from, with a bracket
 * tying it to its statements; in the right column each statement's words in full, with one line
 * under them naming the decision that shaped them, and its mark at the right of the row. Below
 * 900 px it is one column and each decision stands above its statements, as
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
 * The body draws no summary of the issue. The picture heads the Spec filter with the issue's
 * three sentences under "In short"; the page draws them in the Contents pane from 1024 px up
 * and above the outline below that, with every filter on (R8, `IssueThreeThings.tsx`), and the
 * callout is drawn once (c-three-things).
 */
export function SpecFilterBody({
  context,
  issue,
  reader,
  root,
  view,
}: {
  /** The ids of the rows the filter draws only as context, drawn faint (R2, R32). */
  context: ReadonlySet<string>;
  issue: IssueDetails;
  /** Who is reading, so a decision of theirs reads as their own words (R34). */
  reader: string | undefined;
  /** The whole tree, which holds the asks, the decisions and each statement's checks. */
  root: TreeNode;
  /** The tree as the Spec filter leaves it (`presetView`). */
  view: TreeNode;
}): ReactNode {
  // The decisions the page already read for its tree, under the key `useIssueTree.ts` reads
  // them with, so the body costs no request of its own (R20).
  const decisions = useQuery(issueDecisionsQuery(issue.key, true)).data;
  // How this issue addresses a node, which is the one rule the tree was built with, so a
  // `shaped` tie resolves to the node it names (`tree-drafts.ts`).
  const nodeIdOf = useMemo(() => treeContext({ issue }).nodeIdOf, [issue]);
  const groups = useMemo(
    () => specDecidedBy({ decisions, nodeIdOf, reader, root, view }),
    [decisions, nodeIdOf, reader, root, view]
  );
  return (
    <div className="mt-3.5 mb-[120px] flex w-full min-w-0 flex-col" data-spec-body="">
      <Legend />
      <div
        className={`sticky top-[var(--issue-identity-height,0px)] z-[6] grid grid-cols-[280px_minmax(0,1fr)] gap-x-5 border-b pt-2 pb-1.5 max-[900px]:hidden ${pageLineBorder} ${pagePaperBg}`}
        data-spec-head=""
      >
        <span className={CAP} {...pageChrome}>
          {SPEC_WORDS.decided_by}
        </span>
        <span className={`pl-[26px] ${CAP}`} {...pageChrome}>
          {sentenceCase(NODE_TYPE_NAMES.requirement)}
        </span>
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
        role="tree"
      >
        {groups.map((group) => (
          <Group
            context={context}
            group={group}
            key={group.parent?.id ?? group.runs[0]?.statements[0]?.node.id ?? ""}
          />
        ))}
      </div>
      {groups.length === 0 ? (
        // What the page says where the filter's query matched nothing under the issue, in the
        // words the outline says it in (`IssueTree.tsx`).
        <p className={`py-1 text-sm ${pageMutedText}`} data-spec-empty="" {...pageChrome}>
          {EMPTY_STATE_LINES.spec}
        </p>
      ) : null}
    </div>
  );
}

/**
 * The words the Spec filter draws of its own. Nothing here says anything about the page or
 * about Dispatch (R22), and no mark reads proven (R17).
 */
export const SPEC_WORDS = freeze({
  /** Opens the line under a statement that names the decision that shaped it. */
  because: "because:",
  /** Before the options a person chose, where the ask offered them. */
  chose: "chose",
  /** The left column's heading: what decided the statements beside it. */
  decided_by: "Decided by",
  /** Before the other decisions that shaped a statement, beyond the one beside it (R5). */
  informed_by: "informed by",
  /** The four marks a statement carries, which the legend names and each shape is named by
   *  (R17): a statement every check of passed is covered and never proven, because a statement
   *  that has a check is not a statement that is true. */
  marks: {
    covered: "covered",
    failing: "failing",
    no_check: "no check",
    not_run: "not run",
  },
  /** What the left column says of several statements no decision shaped (R35). */
  nothing_many: "No decision shaped these statements.",
  /** What it says of one (R35). */
  nothing_one: "No decision shaped this statement.",
  /** The heading over an agent's decision. */
  said_agent: "Because an agent decided",
  /** The heading over the words of a person who is not the reader. */
  said_person: "Because a person said",
  /** The heading over the reader's own words, as the picture draws it. */
  said_you: "Because you said",
  /** Opens the whole of a person's answer, where the column draws its first sentence. */
  whole_answer: "whole answer",
  /** Opens the whole decision. */
  whole_decision: "whole decision",
} as const);

/** A column heading, in the prototype's `.cap`. */
const CAP = `text-xs leading-4 font-semibold tracking-[0.08em] uppercase ${pageQuietText}`;

/** The shape of each mark, one `path` in a 16 by 16 box, as `NodeGlyph.tsx` draws every other
 *  shape of the page: the tick and the cross the page already draws for a check that passed and
 *  one that failed, a ring for a statement whose checks have not run, and a bar over a dot for
 *  one no check serves. Integration moves them into `GLYPH_PATHS` under the tokens below. */
const MARK_PATHS: Readonly<Record<SpecMark, string>> = {
  covered: "M2 9 l4 4 l8 -10",
  failing: "M4 4 l8 8 M12 4 l-8 8",
  no_check: "M8 2 v7 M8 12.5 v1",
  not_run: "M8 2 a6 6 0 1 0 0.01 0 Z",
};

/** The colour each mark is drawn in, which repeats what its shape and the legend's word say
 *  (R6): a statement no check serves is drawn in the failed red, as the prototype draws it. */
const MARK_TONES: Readonly<Record<SpecMark, string>> = {
  covered: pageStateText.passed,
  failing: pageStateText.failed,
  no_check: pageStateText.failed,
  not_run: pageStateText.quiet,
};

/** The four marks in the order the legend draws them. */
const MARK_ORDER: readonly SpecMark[] = ["covered", "failing", "not_run", "no_check"];

/** One mark, by a shape and the accessible name its word gives it (R6, R17). */
function Mark({ className, mark }: { className: string; mark: SpecMark }): ReactNode {
  return (
    <svg
      aria-label={sentenceCase(SPEC_WORDS.marks[mark])}
      className={className}
      data-glyph={`statement-${mark}`}
      fill="none"
      role="img"
      stroke="currentColor"
      strokeWidth="1.5"
      viewBox="0 0 16 16"
    >
      <path d={MARK_PATHS[mark]} />
    </svg>
  );
}

/** What each mark means, above the columns: a shape and the word for it, since neither colour
 *  nor shape alone tells a reader what one says (R6). */
function Legend(): ReactNode {
  return (
    <p
      className={`mb-1 ml-[300px] flex flex-wrap gap-[14px] text-xs leading-5 max-[900px]:ml-0 ${pageQuietText}`}
      data-spec-legend=""
    >
      {MARK_ORDER.map((mark) => (
        <span
          className="inline-flex items-center gap-[5px]"
          data-spec-legend-mark={mark}
          key={mark}
        >
          <Mark className={`size-3 shrink-0 ${MARK_TONES[mark]}`} mark={mark} />
          <span {...pageChrome}>{SPEC_WORDS.marks[mark]}</span>
        </span>
      ))}
    </p>
  );
}

/** Who made something, by a name and a shape, as every other row of the page draws an author
 *  (R9): never an identifier, and the page's own words where the model holds none. */
function Author({ actor }: { actor: Actor | undefined }): ReactNode {
  const agentText = useAgentText();
  const kind = authorKind(actor);
  return (
    <span className="inline-flex items-center gap-1" data-spec-author="">
      <NodeGlyph className="size-3 shrink-0" token={GLYPH_TOKENS.authors[kind]} />
      <span {...(actor === undefined ? pageChrome : authorText)}>
        {actor === undefined ? CONTROL_LABELS.unknown_author : agentText.author(actor)}
      </span>
    </span>
  );
}

/** The statements under one parent: that parent's own line as the group's heading, then one row
 *  of the grid per run of statements one decision shaped. */
function Group({ context, group }: { context: ReadonlySet<string>; group: SpecGroup }): ReactNode {
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
          className="grid min-w-0 grid-cols-[280px_minmax(0,1fr)] gap-x-5 max-[900px]:grid-cols-[minmax(0,1fr)]"
          data-spec-run=""
          key={run.statements[0]?.node.id ?? ""}
          role="presentation"
        >
          <DecisionCell decision={run.decision} parent={group.parent} statements={run.statements} />
          <div className="flex min-w-0 flex-col" role="presentation">
            {run.statements.map((statement) => (
              <Statement
                context={context.has(statement.node.id)}
                decision={run.decision}
                key={statement.node.id}
                statement={statement}
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
      className="mb-1 ml-[300px] flex min-w-0 flex-col pl-[26px] max-[900px]:ml-0 max-[900px]:pl-0"
      data-spec-group-heading=""
      role="presentation"
    >
      <p className={`min-w-0 text-xs leading-[18px] [overflow-wrap:anywhere] ${pageQuietText}`}>
        <span {...pageChrome}>{NODE_TYPE_NAMES[parent.type]} · </span>
        <Author actor={parent.by} />
        {parent.at === undefined ? null : (
          <>
            <span {...pageChrome}> · </span>
            <span className="whitespace-nowrap" {...authorText}>
              {shortDay(parent.at)}
            </span>
          </>
        )}
      </p>
      <p
        className={`min-w-0 text-[15px] leading-6 font-semibold break-words ${pageInkText}`}
        {...authorText}
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
 * decided; a run no decision shaped says so in words (R35).
 */
function DecisionCell({
  decision,
  parent,
  statements,
}: {
  decision: SpecDecision | undefined;
  parent: SpecParent | undefined;
  statements: readonly SpecStatement[];
}): ReactNode {
  const rule = decision === undefined ? pageHairBorder : "border-page-decision";
  const cap = decision === undefined ? "bg-page-hair" : "bg-page-decision";
  // The question the words answer, unless the statements hang under it already: there the
  // group's heading is that question, and the two are one line (R5).
  const question =
    decision?.question === undefined || decision.id === parent?.id ? undefined : decision.question;
  return (
    <div
      aria-level={1}
      className={`relative self-stretch rounded-l-[4px] border-r-2 pt-1 pr-3.5 pb-2 pl-0.5 text-xs leading-[18px] max-[900px]:my-1.5 max-[900px]:rounded-none max-[900px]:border-r-0 max-[900px]:border-l-2 max-[900px]:py-0.5 max-[900px]:pr-0 max-[900px]:pl-2.5 ${
        decision?.said === "agent" ? "border-dashed" : ""
      } ${rule} ${pageMutedText}`}
      data-decision-id={decision?.id}
      data-said={decision?.said}
      data-spec-cell=""
      data-spec-statements={statements.map((statement) => statement.node.id).join(" ")}
      // An item of the outline, as the statements beside it are: it is where the focus goes
      // from a statement (R5's `w`), and the prototype's own cell takes the focus the same way.
      role="treeitem"
      tabIndex={-1}
    >
      <span
        aria-hidden="true"
        className={`absolute top-0 -right-0.5 h-0.5 w-2 max-[900px]:hidden ${cap}`}
      />
      <span
        aria-hidden="true"
        className={`absolute -right-0.5 bottom-0 h-0.5 w-2 max-[900px]:hidden ${cap}`}
      />
      <div className="sticky top-[calc(var(--issue-identity-height,0px)+44px)] flex min-w-0 flex-col gap-[3px] max-[900px]:static">
        {decision === undefined ? (
          <p data-spec-nothing="" {...pageChrome}>
            {statements.length === 1 ? SPEC_WORDS.nothing_one : SPEC_WORDS.nothing_many}
          </p>
        ) : (
          <>
            <p className={`font-semibold ${pageDecisionText}`} data-spec-said="" {...pageChrome}>
              {decision.said === "agent"
                ? SPEC_WORDS.said_agent
                : decision.said === "you"
                  ? SPEC_WORDS.said_you
                  : SPEC_WORDS.said_person}
            </p>
            <p
              className={`min-w-0 text-sm leading-[21px] [overflow-wrap:anywhere] ${pageInkText}`}
              data-spec-quote=""
            >
              <span {...pageChrome}>“</span>
              <span {...authorText}>{decision.quote}</span>
              <span {...pageChrome}>”</span>
            </p>
            <p className={`min-w-0 [overflow-wrap:anywhere] ${pageQuietText}`} data-spec-source="">
              {question === undefined ? null : (
                <>
                  <span {...authorText}>{question}</span>
                  <span {...pageChrome}> · </span>
                </>
              )}
              <Author actor={decision.by} />
              {decision.at === undefined ? null : (
                <>
                  <span {...pageChrome}> · </span>
                  <span className="whitespace-nowrap" {...authorText}>
                    {shortDay(decision.at)}
                  </span>
                </>
              )}
              {decision.chose.length === 0 ? null : (
                <>
                  <span {...pageChrome}> · {SPEC_WORDS.chose} </span>
                  <span {...authorText}>{decision.chose.join(", ")}</span>
                </>
              )}
              {decision.state === undefined ? null : (
                <>
                  <span {...pageChrome}> · </span>
                  <span data-node-state={decision.state} {...pageChrome}>
                    {nodeStateWord(decision.state)}
                  </span>
                </>
              )}
              {decision.unsure ? (
                <>
                  <span {...pageChrome}> · </span>
                  <span data-node-unsure="" {...pageChrome}>
                    {CONTROL_LABELS.unsure}
                  </span>
                </>
              ) : null}
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
  const route =
    decision.reference === undefined ? undefined : parseNodeReference(decision.reference);
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
 * One statement: its bullet, its words in full, the line naming the decision that shaped it,
 * and its mark at the right of the row (R5, R17, R34).
 */
function Statement({
  context,
  decision,
  statement,
}: {
  context: boolean;
  decision: SpecDecision | undefined;
  statement: SpecStatement;
}): ReactNode {
  const node = statement.node;
  return (
    <div
      aria-level={1}
      className="grid min-w-0 grid-cols-[18px_minmax(0,1fr)_26px] items-start gap-x-2 rounded-md py-[3px] pr-1.5 max-[760px]:grid-cols-[16px_minmax(0,1fr)_22px] max-[760px]:gap-x-1.5"
      data-node-context={context ? "" : undefined}
      data-node-id={node.id}
      // The attributes every row of the outline carries, so the page's own readers find a
      // statement of this body as they find a row of the tree (`e2e/issue-page.ts`'s
      // `drawnRows` and `nodeRow`). The keys that move between the rows are the outline's
      // (`useTreeKeymap.ts`), which this body stands in place of: see the handoff.
      data-node-row=""
      data-node-type={node.type}
      data-spec-mark={statement.mark}
      data-spec-statement=""
      role="treeitem"
      tabIndex={-1}
    >
      <span className="flex h-6 w-[18px] items-center justify-center max-[760px]:w-4">
        <i
          aria-hidden="true"
          className={`block size-[7px] rounded-full ${
            context ? pageKindBullet.context : pageKindBullet.statement
          }`}
        />
      </span>
      <div className="min-w-0">
        <div
          className={`text-[15px] leading-6 ${context ? pageQuietText : pageWordsText}`}
          data-node-words=""
        >
          <RowText markdown={node.text} source={node.source} tree={node.tree} />
        </div>
        <Because decision={decision} statement={statement} />
        {/* Every gap on the statement but the one its mark already says: "No check" is the
            mark at the right of the row and the word for it in the legend (R17), and a chip
            beside it would say it twice. The rest are drawn as the outline draws them (R10). */}
        <Gaps node={node} />
      </div>
      <Mark
        className={`mx-auto size-3.5 shrink-0 ${MARK_TONES[statement.mark]}`}
        mark={statement.mark}
      />
    </div>
  );
}

/** The gaps a statement carries, each in the few words the page knows it by, on one quiet line
 *  under its words (R10, `NodeRow.tsx`'s `NodeEdges`). The statement's own chips - the
 *  constraint a statement points at (R4) - are `NodeRow.tsx`'s `Chip`, which is private to that
 *  module: see the handoff. */
function Gaps({ node }: { node: TreeNode }): ReactNode {
  const gaps = node.gaps.filter((gap) => gap !== "no_check");
  if (gaps.length === 0) return null;
  return (
    <p
      className={`flex min-w-0 flex-wrap items-baseline gap-x-2 text-xs leading-5 ${pageQuietText}`}
      data-node-trail=""
    >
      {gaps.map((gap) => (
        <span data-gap="" data-gap-kind={gap} key={gap} {...pageChrome}>
          {GAP_WORDS[gap]}
        </span>
      ))}
    </p>
  );
}

/**
 * The one line under a statement that names the decision that shaped it (R5): the words it
 * came from, who decided and when, and then every other decision that informed it, each a link
 * to it. A statement no decision shaped draws no line here: its cell says so beside it (R35).
 */
function Because({
  decision,
  statement,
}: {
  decision: SpecDecision | undefined;
  statement: SpecStatement;
}): ReactNode {
  if (decision === undefined && statement.alsoShapedBy.length === 0) return null;
  return (
    <p
      className={`mb-0.5 min-w-0 text-xs leading-[18px] [overflow-wrap:anywhere] ${pageQuietText}`}
      data-spec-because=""
    >
      {decision === undefined ? null : (
        <>
          <span {...pageChrome}>{SPEC_WORDS.because} “</span>
          <span {...authorText}>{decision.quote}</span>
          <span {...pageChrome}>” — </span>
          <Author actor={decision.by} />
          {decision.at === undefined ? null : (
            <>
              <span {...pageChrome}> · </span>
              <span className="whitespace-nowrap" {...authorText}>
                {shortDay(decision.at)}
              </span>
            </>
          )}
        </>
      )}
      {statement.alsoShapedBy.length === 0 ? null : (
        <>
          <span {...pageChrome}>
            {decision === undefined ? "" : " · "}
            {SPEC_WORDS.informed_by}{" "}
          </span>
          {statement.alsoShapedBy.map((informed, place) => (
            <span key={informed.id}>
              {place === 0 ? null : <span {...pageChrome}>, </span>}
              <Informed decision={informed} />
            </span>
          ))}
        </>
      )}
    </p>
  );
}

/** One other decision that shaped the statement, named by who decided it and when, as a link to
 *  it: the model holds no short name for a decision, and a block id is an identifier (R22). */
function Informed({ decision }: { decision: SpecDecision }): ReactNode {
  const route =
    decision.reference === undefined ? undefined : parseNodeReference(decision.reference);
  const named = (
    <>
      <Author actor={decision.by} />
      {decision.at === undefined ? null : (
        <>
          <span {...pageChrome}> · </span>
          <span className="whitespace-nowrap" {...authorText}>
            {shortDay(decision.at)}
          </span>
        </>
      )}
    </>
  );
  if (route === undefined) {
    return <span data-spec-informed="">{named}</span>;
  }
  return (
    <Link
      className="dispatch-inline-link underline decoration-page-hair underline-offset-[3px]"
      data-spec-informed=""
      tabIndex={-1}
      to={buildReferencePath(route)}
    >
      {named}
    </Link>
  );
}
