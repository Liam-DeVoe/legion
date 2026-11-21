import type { GoingPlanAnswer, GoingPlanRow, GoingPlanTick } from "../../api/types";
import { actionLabel } from "./action-names";
import { objectOf, textOf } from "./fields";
import type { PlanDraft } from "./going-store";

/**
 * What one plan's rows say to the person, and the person's answer to them: each row's change as
 * the item, the value now and the new value; a document change as its sentences and its diff;
 * the ticks, where unticking a row unticks every row that needs it and ticking one ticks every
 * row it needs; and the approval those make. Where each row is drawn is `plan-surfaces.ts`'.
 */

type Arguments = Readonly<Record<string, unknown>>;

/** A row's arguments as an object: what its tool runs with. */
export function argumentsOf(row: GoingPlanRow): Arguments {
  return objectOf(row.arguments);
}

/** The tools that make a new issue, whose row shows on the list of its parent. */
export function makesIssue(row: GoingPlanRow): boolean {
  return row.tool === "dispatch_issue";
}

/** How a row names its change wherever it is drawn: its action and its item, and for a new issue,
 *  which has no item before it is made, its title; so each row of a plan is named apart. */
export function rowLabel(row: GoingPlanRow): string {
  return actionLabel(
    row.tool,
    row.item === "" && makesIssue(row) ? textOf(argumentsOf(row).title) : row.item
  );
}

/** The title of each new issue a plan's rows make, by the row's number, with the person's edits of
 *  it in `draft`: what a value naming that issue shows. */
export function newIssueTitles(
  rows: readonly GoingPlanRow[],
  draft: PlanDraft | undefined
): ReadonlyMap<number, string> {
  return new Map(
    rows
      .filter(makesIssue)
      .map((row) => [row.row, textOf({ ...argumentsOf(row), ...draft?.edits.get(row.row) }.title)])
  );
}

/** A value naming the issue a create of the same plan makes: "$N", N that create's number. */
const NEW_ISSUE = /^\$(\d+)$/u;

/** The arguments of each tool that may name a new issue of the plan as "$N": the ones whose tool
 *  takes an issue key there as a value the change writes, which the server reads "$N" in alone
 *  (going_plan.go `goingNewIssueArguments`). Every other value is text, shown as written. An
 *  update's before and after show in no field of their own: its read names them as the field
 *  rank, whose new value is the move they make (`moveText`), each issue as these show it. */
const NEW_ISSUE_ARGUMENTS: Readonly<Record<string, readonly string[]>> = {
  dispatch_issue: ["parent", "blocked_by"],
  dispatch_issue_update: ["parent", "blocked_by", "before", "after"],
};

/** A string as the row shows it: a value naming a new issue of the plan as its title and "(new)",
 *  any other as it is. */
function issueText(value: string, newIssues: ReadonlyMap<number, string>): string {
  const named = NEW_ISSUE.exec(value);
  const title = named === null ? undefined : newIssues.get(Number(named[1]));
  return title === undefined ? value : `${title} (new)`;
}

/** A value as one line of the row: a string as it is, anything else as its JSON. `newIssues` is
 *  given only for a value that may name a new issue of the plan (`RowChange.namesNewIssue`): there
 *  a string, or an item of a list, naming one ("$N", `newIssues`' row N) shows that issue's title
 *  and "(new)", as the issue has no key before the plan runs. Without it every string shows as
 *  written, so a title "$100" shows "$100". */
export function valueText(
  value: unknown,
  newIssues: ReadonlyMap<number, string> = new Map()
): string {
  if (value === undefined || value === null) {
    return "none";
  }
  if (typeof value === "string") {
    return issueText(value, newIssues);
  }
  if (Array.isArray(value)) {
    return JSON.stringify(
      value.map((item: unknown) => (typeof item === "string" ? issueText(item, newIssues) : item))
    );
  }
  return JSON.stringify(value);
}

/** One field a row writes: its name, its value now and the value the row writes, the person's
 *  edit where they made one, whether the person can edit it (a value of text), and whether its
 *  value may name a new issue of the plan as "$N" (`NEW_ISSUE_ARGUMENTS`). An update's rank is
 *  the move its before and after make, whose new value is that move's words (`moveText`), which
 *  the person cannot edit: rank is no argument the change takes. */
export interface RowChange {
  readonly field: string;
  readonly now: unknown;
  readonly next: unknown;
  readonly editable: boolean;
  readonly namesNewIssue: boolean;
}

/** The fields a new issue's row names, in the order the row shows them. */
const NEW_ISSUE_FIELDS = ["title", "project", "parent", "priority", "labels"] as const;

/** The arguments of an update that move its issue on its project's board, in the order a move
 *  reads: the update's read names them as the one field rank (going_observe.go
 *  `goingIssueFields`). */
const MOVE_ARGUMENTS = ["after", "before"] as const;

/** The move an update's arguments `args` make, as its rank's new value: "after <key>",
 *  "before <key>", or both, each key as its argument shows it (`valueText`), a new issue of the
 *  plan ("$N", `newIssues`' row N) as its title and "(new)"; undefined for a row that names
 *  neither. */
function moveText(args: Arguments, newIssues: ReadonlyMap<number, string>): string | undefined {
  const takes = NEW_ISSUE_ARGUMENTS.dispatch_issue_update ?? [];
  const places = MOVE_ARGUMENTS.filter((place) => args[place] !== undefined).map(
    (place) => `${place} ${valueText(args[place], takes.includes(place) ? newIssues : undefined)}`
  );
  return places.length === 0 ? undefined : places.join(", ");
}

/** What a row changes, field by field: the fields its item's read names, each with its value now
 *  and the new value, an update's rank the move its before and after make (`moveText`, which
 *  shows a new issue of the plan by its title in `newIssues`); a new issue's title, project,
 *  parent, priority and labels, none of which has a value now. A row that names no field shows
 *  its item alone. */
export function rowChanges(
  row: GoingPlanRow,
  edits: Arguments | undefined,
  newIssues: ReadonlyMap<number, string>
): RowChange[] {
  const args = { ...argumentsOf(row), ...edits };
  const fields = makesIssue(row)
    ? NEW_ISSUE_FIELDS.filter((field) => args[field] !== undefined)
    : (row.fields ?? []);
  const now = objectOf(row.now);
  return fields.map((field) => {
    const moves = row.tool === "dispatch_issue_update" && field === "rank";
    return {
      editable: !moves && typeof args[field] === "string",
      field,
      namesNewIssue: NEW_ISSUE_ARGUMENTS[row.tool]?.includes(field) ?? false,
      next: moves ? moveText(args, newIssues) : args[field],
      now: makesIssue(row)
        ? undefined
        : (now[field] ?? (fields.length === 1 ? row.now : undefined)),
    };
  });
}

/** One piece of a document change: the text it takes out and the text it puts in. */
export interface ChangePiece {
  readonly removed: string;
  readonly added: string;
}

/** A change of a document's text, as the plan shows it first: two or three sentences that say what
 *  it changes, and the pieces of its diff, which the plan keeps closed under them. */
export interface DocumentChange {
  readonly sentences: string;
  readonly pieces: readonly ChangePiece[];
}

/** The document change a row makes, or undefined for a row of any other kind: a suggestion
 *  (`dispatch_suggest`, its quote and its replacement), an edit (`dispatch_doc_edit`, each
 *  operation's text out and in) and an upload over a document (`dispatch_artifact`, the content now
 *  and the content it writes). Its sentences are the ones the model wrote for it - the
 *  suggestion's rationale, the version's summary - or else the plan's own summary. */
export function documentChange(row: GoingPlanRow, summary: string): DocumentChange | undefined {
  const args = argumentsOf(row);
  const said = (value: unknown) =>
    typeof value === "string" && value.trim() !== "" ? value : summary;
  switch (row.tool) {
    case "dispatch_suggest":
      return {
        pieces: [{ added: textOf(args.replace_with), removed: textOf(args.quote) }],
        sentences: said(args.body),
      };
    case "dispatch_doc_edit": {
      const ops = Array.isArray(args.ops) ? args.ops : [];
      return {
        pieces: ops.map((op: unknown) => {
          const fields = objectOf(op);
          return { added: textOf(fields.with ?? fields.markdown), removed: textOf(fields.find) };
        }),
        sentences: said(args.summary),
      };
    }
    case "dispatch_artifact":
      return typeof args.content === "string"
        ? {
            pieces: [{ added: args.content, removed: textOf(objectOf(row.now).content) }],
            sentences: said(args.summary),
          }
        : undefined;
    default:
      return undefined;
  }
}

/** The refusals of an upload whose document moved after the plan read its name: a document made
 *  under the name (`DOCUMENT_EXISTS`), text other than the text read (`DOCUMENT_CHANGED`), or a
 *  version written past the token read (`PRECONDITION_FAILED`). The route writes each for a caller
 *  holding the `dispatch` command, naming the command that reads a token and the field that sends
 *  it; the person who approved the row runs no command. */
const UPLOAD_MOVED_CODES: ReadonlySet<string> = new Set([
  "DOCUMENT_CHANGED",
  "DOCUMENT_EXISTS",
  "PRECONDITION_FAILED",
]);

/** What the row says of an upload its route refused because the document moved after the plan
 *  read it. */
export const UPLOAD_MOVED_REFUSAL =
  "The document under this name changed after the plan read it, so the upload wrote nothing. Read what it holds now before you ask for this upload again.";

/** What the row says of an edit of an approval request (`ASK_KIND_FIXED`), whose question and
 *  options are fixed: the route tells the agent that asked to hand the request back with its
 *  command, and what the person can do instead is change the document the request is about. */
export const ASK_KIND_FIXED_REFUSAL =
  "An approval request's question and options cannot be edited, so nothing was changed. Change the document it asks about instead.";

/** What the row says of an edit that writes a question into a decision whose question is empty
 *  and which holds a paragraph that is no text (`ASK_BLOCK_TEXT`, the document service's "has an
 *  empty question"): the route tells the agent to upload the document again with its command,
 *  and the person can write the question in the document editor. */
export const ASK_EMPTY_QUESTION_REFUSAL =
  "This decision has no question, and writing one here would remove what one of its paragraphs holds, so nothing was changed. Write the question beside that paragraph in the document editor.";

/** A refusal whose words name a command of the `dispatch` command-line tool - written for an
 *  agent that runs it - which the person who approved the row cannot run. */
const NAMES_A_COMMAND = /\bdispatch [a-z][a-z-]*/u;

/** What a row's result says to the person: the page's own words for an upload refused because
 *  its document moved (`UPLOAD_MOVED_REFUSAL`), an edit of an approval request
 *  (`ASK_KIND_FIXED_REFUSAL`) and a question written into a decision that cannot take one there
 *  (`ASK_EMPTY_QUESTION_REFUSAL`); any other refusal of a failed row whose words name a `dispatch`
 *  command, in whatever form the server writes it (backticked, followed by its flags, or in
 *  parentheses beside its arguments), in the page's general words, so no route's advice to run a
 *  command reaches the person; and every other result as Dispatch wrote it, a waiting or skipped
 *  row's words, which can name an item's title, among them. */
export function rowResultMessage(row: GoingPlanRow): string {
  const result = row.result;
  if (result === null) {
    return "";
  }
  if (
    row.tool === "dispatch_artifact" &&
    row.state === "failed" &&
    UPLOAD_MOVED_CODES.has(result.code)
  ) {
    return UPLOAD_MOVED_REFUSAL;
  }
  if (result.code === "ASK_KIND_FIXED") {
    return ASK_KIND_FIXED_REFUSAL;
  }
  if (
    result.code === "ASK_BLOCK_TEXT" &&
    result.message.includes("writing a question into it would delete")
  ) {
    return ASK_EMPTY_QUESTION_REFUSAL;
  }
  return row.state === "failed" && NAMES_A_COMMAND.test(result.message)
    ? "Dispatch refused this change with advice for its command-line tool, which this page does not run, so nothing was changed. Open the item to change it here."
    : result.message;
}

/** The rows that need `row`, and the rows that need those, by number. */
function rowsNeeding(rows: readonly GoingPlanRow[], row: number): Set<number> {
  const needing = new Set<number>();
  let grew = true;
  while (grew) {
    grew = false;
    for (const candidate of rows) {
      const needs = candidate.needs ?? [];
      if (!needing.has(candidate.row) && needs.some((need) => need === row || needing.has(need))) {
        needing.add(candidate.row);
        grew = true;
      }
    }
  }
  return needing;
}

/** The rows `row` needs, and the rows those need, by number. */
function rowsNeeded(rows: readonly GoingPlanRow[], row: number): Set<number> {
  const byNumber = new Map(rows.map((candidate) => [candidate.row, candidate]));
  const needed = new Set<number>();
  const visit = (at: number) => {
    for (const need of byNumber.get(at)?.needs ?? []) {
      if (!needed.has(need)) {
        needed.add(need);
        visit(need);
      }
    }
  };
  visit(row);
  return needed;
}

/** The draft with `row` ticked or unticked: unticking it unticks each row that needs it, and
 *  ticking it ticks each row it needs, so the draft never approves a row without what it needs. */
export function toggledDraft(
  draft: PlanDraft,
  rows: readonly GoingPlanRow[],
  row: number
): PlanDraft {
  const unticked = new Set(draft.unticked);
  if (unticked.has(row)) {
    unticked.delete(row);
    for (const need of rowsNeeded(rows, row)) unticked.delete(need);
  } else {
    unticked.add(row);
    for (const needing of rowsNeeding(rows, row)) unticked.add(needing);
  }
  return { ...draft, unticked };
}

/** The draft with the person's edit of `field` of `row`. */
export function editedDraft(
  draft: PlanDraft,
  row: number,
  field: string,
  value: string
): PlanDraft {
  const edits = new Map(draft.edits);
  edits.set(row, { ...edits.get(row), [field]: value });
  return { ...draft, edits };
}

/** Whether the row waits for the person: the rows a tick or an edit can change. */
export function rowWaits(row: GoingPlanRow): boolean {
  return row.state === "waiting";
}

/** The approval the draft makes of `plan` (`POST /api/v1/going/runs/{id}/plan/approve`): the
 *  version of the plan the person was shown, which Dispatch refuses the approval over once the
 *  plan has changed since, each waiting row's tick, and the values the person edited. */
export function approvalOf(
  plan: GoingPlanAnswer,
  draft: PlanDraft
): { plan: number; rows: GoingPlanTick[]; version: GoingPlanAnswer["version"] } {
  const rows: GoingPlanTick[] = (plan.rows ?? []).filter(rowWaits).map((row) => {
    const values = draft.edits.get(row.row);
    return {
      row: row.row,
      tick: !draft.unticked.has(row.row),
      ...(values === undefined ? {} : { values: { ...values } }),
    };
  });
  return { plan: plan.plan, rows, version: plan.version };
}
