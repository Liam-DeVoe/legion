import {
  Fragment,
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { Link, useInRouterContext } from "react-router-dom";

import type { GoingPlanAnswer, GoingPlanRow } from "../../api/types";
import { Pill, pillShapeClassName } from "../../components/Pill";
import { QueryError } from "../../components/QueryError";
import { issueProject } from "../../lib/issue-key";
import {
  borderDefault,
  calloutDangerBorder,
  calloutWarningBorder,
  dangerText,
  diffAddedBg,
  diffAddedText,
  diffRemovedBg,
  diffRemovedText,
  focusVisibleRing,
  inlineWarningText,
  inputClasses,
  linkHoverText,
  linkText,
  primaryButton,
  quoteAccentBorder,
  quoteBodyText,
  secondaryButtonCompact,
  textMutedOnSurface,
  textPrimaryOnSurface,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { changedWords } from "../doc/change-set";
import { IssueLabels } from "../issue/IssueLabels";
import { issuePriority } from "../issue/issue-priority";
import { PriorityEditor } from "../issue/PriorityControl";
import { AgentName, useAgentText } from "../refs/AgentName";
import { objectOf, textOf } from "./fields";
import { draftOf, type PlanDraft, setDraft, useGoing, useGoingValue } from "./going-store";
import {
  argumentsOf,
  broadcastSessions,
  type ChangePiece,
  type DocumentChange,
  deliveryWords,
  documentChange,
  editedDraft,
  fieldWords,
  type LabelMark,
  labelMarks,
  MODE_WORDS,
  needsUnticked,
  newIssueTitles,
  planTitle,
  type RowChange,
  reviewWords,
  rowChanges,
  rowKind,
  rowLabel,
  rowRemoves,
  rowResultMessage,
  rowText,
  rowWaits,
  specSentences,
  tickedCount,
  toggledDraft,
  valuesNowWords,
  valueWords,
  withChangesUnticked,
} from "./plan-rows";
import type { PlanAnswering } from "./usePlan";

/** How a row stands once its plan no longer waits, in the person's words. */
const ROW_STATES: Readonly<Record<GoingPlanRow["state"], string>> = {
  approved: "Approved",
  done: "Done",
  failed: "Failed",
  running: "Running",
  skipped: "Skipped: a change it needs did not run",
  unticked: "Not run",
  waiting: "Waits for you",
};

/** Where the person sets which changes of a plan wait for them: the Command bar section of
 *  Settings (`CommandBarSection.tsx`). */
const MODE_SETTING_PATH = "/settings#command-bar-heading";

/** A row of a plan card or of the page's planned changes. */
const ROW = '[data-testid="going-plan-row"]';

/** How the card takes the focus from the box while it shows a plan that waits (`holdCardFocus`),
 *  and the box it hands the focus back to (`usePlanCardKeys`). */
let cardFocus: (() => boolean) | undefined;
let cardBox: HTMLElement | undefined;

/** Holds `take` as the way the box's ↑ moves the focus into the card, until the release it
 *  answers. */
function holdCardFocus(take: () => boolean): () => void {
  cardFocus = take;
  return () => {
    if (cardFocus === take) cardFocus = undefined;
  };
}

/**
 * What the box does with a key while a plan waits (design v3, Keys, "Bar, a plan waits"): ↑ in
 * the empty box moves the focus from the box into the plan card, onto its row nearest the box.
 * There ↑ ↓ move between rows, Space ticks or unticks a row, and E opens the editor of its new
 * value; Esc, or any letter, puts the focus back in this box, the letter typed into it. The
 * handler answers whether the key was the card's, which the box then does nothing more with.
 */
export function usePlanCardKeys(): (
  event: KeyboardEvent<HTMLInputElement | HTMLTextAreaElement>
) => boolean {
  return useCallback((event) => {
    const box = event.currentTarget;
    if (
      event.key !== "ArrowUp" ||
      event.altKey ||
      event.ctrlKey ||
      event.metaKey ||
      event.shiftKey ||
      box.value !== ""
    ) {
      return false;
    }
    cardBox = box;
    if (cardFocus === undefined || !cardFocus()) return false;
    event.preventDefault();
    return true;
  }, []);
}

/** Opens the editor of a row's first new value the person can edit: the issue page's priority
 *  menu, its label picker, or a box of text. */
function openEditor(row: HTMLElement): void {
  const editor = row.querySelector<HTMLElement>("[data-plan-edit]");
  const select = editor?.querySelector("select");
  if (select !== null && select !== undefined) {
    select.focus();
    // A browser that opens no menu from a key leaves the menu focused, where Space opens it.
    if ("showPicker" in select && typeof select.showPicker === "function") {
      try {
        select.showPicker();
      } catch {
        // The menu stays focused.
      }
    }
    return;
  }
  editor?.click();
}

/** The card's keys on its rows (`usePlanCardKeys`). A key pressed in a control of a row - an edit
 *  box, the priority menu, the label picker - is that control's. */
function onRowKey(event: KeyboardEvent<HTMLUListElement>): void {
  const row = event.target;
  if (
    !(row instanceof HTMLElement) ||
    !row.matches(ROW) ||
    event.altKey ||
    event.ctrlKey ||
    event.metaKey
  ) {
    return;
  }
  const box = cardBox?.isConnected === true ? cardBox : undefined;
  switch (event.key) {
    case "ArrowDown":
    case "ArrowUp": {
      event.preventDefault();
      const rows = [...event.currentTarget.querySelectorAll<HTMLElement>(ROW)];
      const at = rows.indexOf(row);
      rows[
        event.key === "ArrowDown" ? Math.min(at + 1, rows.length - 1) : Math.max(at - 1, 0)
      ]?.focus();
      return;
    }
    case " ":
      event.preventDefault();
      row.querySelector<HTMLInputElement>('input[type="checkbox"]')?.click();
      return;
    case "e":
    case "E":
      event.preventDefault();
      openEditor(row);
      return;
    case "Escape":
      if (box !== undefined) {
        event.preventDefault();
        event.stopPropagation();
        box.focus();
      }
      return;
    default:
      // The letter is not taken: it goes on to the box the focus is now in.
      if (box !== undefined && /^\p{L}$/u.test(event.key)) box.focus();
  }
}

/**
 * The person's answer to `plan` as the store holds it, with each row whose item changed after the
 * plan showed unticked once for that change (`withChangesUnticked`), so it waits to be ticked
 * again (R4). Undefined while no plan is read.
 */
export function usePlanDraft(plan: GoingPlanAnswer | undefined): PlanDraft | undefined {
  const held = useGoingValue((of) => of.draft);
  const draft = plan === undefined ? undefined : draftOf(plan.run, plan.plan, held);
  useEffect(() => {
    if (plan === undefined || draft === undefined) return;
    const unticked = withChangesUnticked(draft, plan);
    if (unticked !== draft) setDraft(unticked);
  }, [plan, draft]);
  return draft;
}

/** One piece of a document change, its words out and in marked as a diff marks them. A whole
 *  document's pieces are diffed within the bound `changedWords` keeps, and once for each pair. */
function PieceDiff({ piece }: { readonly piece: ChangePiece }): ReactNode {
  const { added, removed } = piece;
  const parts = useMemo(() => changedWords(removed, added), [removed, added]);
  return (
    <p
      className="whitespace-pre-wrap break-words font-mono text-xs"
      data-testid="going-plan-diff-piece"
    >
      {parts.map((part, index) => {
        const Mark = part.kind === "added" ? "ins" : part.kind === "removed" ? "del" : "span";
        const marked =
          part.kind === "added"
            ? `${diffAddedBg} ${diffAddedText} no-underline`
            : part.kind === "removed"
              ? `${diffRemovedBg} ${diffRemovedText}`
              : undefined;
        return (
          // biome-ignore lint/suspicious/noArrayIndexKey: a part is its place in the diff, which never changes for one piece
          <Mark className={marked} key={index}>
            {part.value}
          </Mark>
        );
      })}
    </p>
  );
}

/** The item a row changes, by its key: the place an item's chip, with its status and title, takes
 *  (design v3, state 4). */
function ItemSlot({ item }: { readonly item: string }): ReactNode {
  return (
    <span
      className={`font-medium whitespace-nowrap ${textPrimaryOnSurface}`}
      data-item={item}
      data-testid="going-plan-item"
    >
      {item}
    </span>
  );
}

/** Labels as the issue page's chips: a label the change keeps, one it puts in, in green, and one
 *  it takes out, struck through; "None" for no label. */
function LabelChips({ marks }: { readonly marks: readonly LabelMark[] }): ReactNode {
  if (marks.length === 0) return <>{valueWords("labels", [])}</>;
  return (
    <span className="inline-flex flex-wrap items-center gap-1">
      {marks.map(({ label, mark }) =>
        mark === "added" ? (
          <ins
            className={`${pillShapeClassName} ${diffAddedBg} ${diffAddedText} no-underline`}
            data-mark="added"
            key={label}
          >
            <span className="sr-only">added </span>
            {label}
          </ins>
        ) : mark === "removed" ? (
          <del
            className={`${pillShapeClassName} ${diffRemovedBg} ${diffRemovedText}`}
            data-mark="removed"
            key={label}
          >
            <span className="sr-only">removed </span>
            {label}
          </del>
        ) : (
          <Pill key={label}>{label}</Pill>
        )
      )}
    </span>
  );
}

/** A value as the issue page shows it: labels as its chips, marked against the labels `against`
 *  where given; a session holding a claim by its name; anything else in words (`valueWords`). */
function FieldValue({
  against,
  field,
  newIssues,
  value,
}: {
  readonly against?: unknown;
  readonly field: string;
  readonly newIssues: ReadonlyMap<number, string> | undefined;
  readonly value: unknown;
}): ReactNode {
  if (field === "labels" && Array.isArray(value)) {
    return <LabelChips marks={labelMarks(against ?? value, value)} />;
  }
  const holder = objectOf(value);
  if (field === "claim" && holder.kind === "session") {
    return <AgentName kind="static" sessionId={textOf(holder.id)} storedTitle={undefined} />;
  }
  return <>{valueWords(field, value, newIssues)}</>;
}

/** The issue page's label picker over the labels a row writes, opened at once; the labels it
 *  closes on are the person's edit. */
function LabelsEditor({
  labels,
  onEdit,
  project,
}: {
  readonly labels: readonly string[];
  readonly onEdit: (labels: string[]) => void;
  readonly project: string;
}): ReactNode {
  const holder = useRef<HTMLSpanElement>(null);
  useEffect(() => {
    holder.current?.querySelector<HTMLButtonElement>('button[aria-haspopup="listbox"]')?.click();
  }, []);
  return (
    <span data-testid="going-plan-labels-editor" ref={holder}>
      <IssueLabels
        disabled={false}
        labels={labels}
        onSave={async (next) => onEdit(next)}
        project={project}
        saving={false}
        variant="rail"
      />
    </span>
  );
}

/** A new value the person can press to edit, as the issue page lets them press a value. */
const VALUE_BUTTON = `rounded text-left underline decoration-dotted underline-offset-2 outline-none focus-visible:ring-2 ${focusVisibleRing} ${textPrimaryOnSurface}`;

/**
 * One field a row writes, in the issue page's words (design v3, rule 6): as a line, its name, the
 * value now and the new value ("Priority: No priority → P2"); as a new issue's title, the value
 * alone; as a part of a new issue's line, the value with the word that says what it is ("under
 * CORE-1"). While the row waits, a press on the new value, or its Edit, opens the field's own
 * control from the issue page: the priority menu, the label picker, or a box of text. A value of
 * a field that may name a new issue of the plan (`RowChange.namesNewIssue`) naming one shows that
 * issue's title and "(new)" (`newIssues`), and its edit box holds the value itself, which the
 * person may replace with an issue's key. The edit box starts from the new value of the plan it
 * is drawn for, and its caller keys it to that plan, so a revised plan or a new run's never opens
 * with an earlier plan's value or the person's unsaved typing. An edited value says "Edited".
 */
function FieldChange({
  change,
  editable,
  issueKey,
  newIssues,
  onEdit,
  project,
  shape,
}: {
  readonly change: RowChange;
  readonly editable: boolean;
  readonly issueKey: string;
  readonly newIssues: ReadonlyMap<number, string>;
  readonly onEdit: (value: unknown) => void;
  readonly project: string;
  readonly shape: "line" | "title" | "part";
}): ReactNode {
  const { field, next, now } = change;
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(typeof next === "string" ? next : "");
  const box = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (editing) box.current?.focus();
  }, [editing]);
  const named = fieldWords(field).toLowerCase();
  const shown = change.namesNewIssue ? newIssues : undefined;
  const edit = editable ? change.edit : undefined;
  const nextValue = <FieldValue against={now} field={field} newIssues={shown} value={next} />;
  let value: ReactNode;
  if (edit === "priority" && (editing || next !== null)) {
    value = (
      <span data-plan-edit="" data-testid="going-plan-next">
        <PriorityEditor
          issueKey={issueKey}
          write={{
            failed: false,
            pending: false,
            retry: undefined,
            shown: issuePriority(typeof next === "number" ? next : null),
            stopped: undefined,
            submit: onEdit,
          }}
        />
      </span>
    );
  } else if (edit === "labels" && editing) {
    value = (
      <LabelsEditor
        labels={Array.isArray(next) ? next.filter((label) => typeof label === "string") : []}
        onEdit={(labels) => {
          onEdit(labels);
          setEditing(false);
        }}
        project={project}
      />
    );
  } else if (edit === "text" && editing) {
    value = (
      <form
        className="flex items-center gap-1"
        onSubmit={(event) => {
          event.preventDefault();
          onEdit(text);
          setEditing(false);
        }}
      >
        <input
          aria-label={`New ${named}`}
          className={`rounded border px-1 py-0.5 text-xs ${inputClasses(true)}`}
          onChange={(event) => setText(event.target.value)}
          onKeyDown={(event) => {
            // Enter keeps the edit, and the plan is approved from the box alone.
            event.stopPropagation();
            if (event.key === "Escape") setEditing(false);
          }}
          ref={box}
          value={text}
        />
        <button className={secondaryButtonCompact} type="submit">
          Keep
        </button>
      </form>
    );
  } else if (edit !== undefined) {
    // A part of a new issue's line is pressed to edit it; a line and a title also say Edit.
    value = (
      <>
        <button
          aria-label={
            shape === "part" ? `Edit ${named}: ${valueWords(field, next, shown)}` : undefined
          }
          className={VALUE_BUTTON}
          data-testid="going-plan-next"
          onClick={() => setEditing(true)}
          type="button"
        >
          {nextValue}
        </button>
        {shape === "part" ? null : (
          <>
            {" "}
            <button
              aria-label={`Edit ${named}`}
              className={secondaryButtonCompact}
              data-plan-edit=""
              onClick={() => setEditing(true)}
              type="button"
            >
              Edit
            </button>
          </>
        )}
      </>
    );
  } else {
    value = (
      <span className={textPrimaryOnSurface} data-testid="going-plan-next">
        {nextValue}
      </span>
    );
  }
  return (
    <div
      className={`flex flex-wrap items-baseline gap-x-2 gap-y-1 ${shape === "title" ? "text-sm font-medium" : "text-xs"}`}
      data-field={field}
      data-testid="going-plan-field"
    >
      {shape === "line" ? (
        <>
          <span className={`font-medium ${textSecondaryOnSurface}`}>{fieldWords(field)}:</span>{" "}
          <span className={textMutedOnSurface} data-testid="going-plan-now">
            <FieldValue field={field} newIssues={undefined} value={now} />
          </span>{" "}
          <span aria-hidden="true">→</span>{" "}
        </>
      ) : null}
      {shape === "part" && field === "parent" ? (
        <>
          <span className={textMutedOnSurface}>under</span>{" "}
        </>
      ) : null}
      {value}
      {change.edited ? (
        <>
          {" "}
          <span className={`font-medium ${textMutedOnSurface}`} data-testid="going-plan-edited">
            Edited
          </span>
        </>
      ) : null}
    </div>
  );
}

/** The words a row posts or sends, whole, as a quote. */
function Quote({ text }: { readonly text: string }): ReactNode {
  return (
    <blockquote
      className={`border-l-2 pl-2 text-sm whitespace-pre-wrap break-words ${quoteAccentBorder} ${quoteBodyText}`}
      data-testid="going-plan-quote"
    >
      {text}
    </blockquote>
  );
}

/**
 * A message row (R7): whom it reaches and how - "Message to ‹session› on DEMO-15 · as a BTW", a
 * broadcast's "Message to 4 sessions · as an aside" with the sessions under it, a message on an
 * issue's "Message on DEMO-15" - and the whole text as a quote. While it waits it says that a
 * message cannot be undone once sent (R6).
 */
function MessageBody({
  row,
  waiting,
}: {
  readonly row: GoingPlanRow;
  readonly waiting: boolean;
}): ReactNode {
  const args = argumentsOf(row);
  const delivery = deliveryWords(row);
  const sessions = broadcastSessions(row);
  const onIssue = row.item || row.issue || textOf(args.issue);
  let to: ReactNode;
  if (row.tool === "dispatch_broadcast") {
    to = `Message to ${sessions.length} ${sessions.length === 1 ? "session" : "sessions"}`;
  } else if (row.tool === "dispatch_send_to_agent") {
    to = (
      <>
        Message to{" "}
        <AgentName kind="static" sessionId={textOf(args.session)} storedTitle={undefined} />
        {row.issue === "" ? null : (
          <>
            {" "}
            on <ItemSlot item={row.issue} />
          </>
        )}
      </>
    );
  } else {
    to =
      onIssue === "" ? (
        "Message in its conversation"
      ) : (
        <>
          Message on <ItemSlot item={onIssue} />
        </>
      );
  }
  return (
    <div className="space-y-1">
      <p className={textPrimaryOnSurface} data-testid="going-plan-message">
        {to}
        {delivery === "" ? null : ` · ${delivery}`}
      </p>
      {sessions.length === 0 ? null : (
        <ul className={`text-xs ${textSecondaryOnSurface}`} data-testid="going-plan-sessions">
          {sessions.map((session) => (
            <li key={session}>
              <AgentName kind="static" sessionId={session} storedTitle={undefined} />
            </li>
          ))}
        </ul>
      )}
      <Quote text={rowText(row)} />
      {waiting ? (
        <p className={`text-xs ${textMutedOnSurface}`} data-testid="going-plan-final">
          Cannot be undone once sent
        </p>
      ) : null}
    </div>
  );
}

/** A change of a document's text (R4): what the row does, two or three sentences on what it
 *  changes, and its diff under them, closed. */
function DocumentBody({
  document,
  label,
}: {
  readonly document: DocumentChange;
  readonly label: string;
}): ReactNode {
  return (
    <div className="space-y-1">
      <p className={textPrimaryOnSurface}>{label}</p>
      <p className={textSecondaryOnSurface} data-testid="going-plan-sentences">
        {document.sentences}
      </p>
      <details data-testid="going-plan-diff">
        <summary className={`cursor-pointer text-xs ${textMutedOnSurface}`}>The diff</summary>
        {document.pieces.map((piece, index) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: a piece is its place in the change, which never changes for one row
          <PieceDiff key={index} piece={piece} />
        ))}
      </details>
    </div>
  );
}

/**
 * A new issue's row: a "New" chip and its title, then its fields on one line, "DEMO · under
 * DEMO-12 · P2", with its labels as chips; its short spec as two or three sentences, with the
 * whole text under them, closed. The title, the project and the parent are the person's to edit.
 */
function NewIssueBody({
  changes,
  field,
  spec,
}: {
  readonly changes: readonly RowChange[];
  readonly field: (change: RowChange, shape: "title" | "part") => ReactNode;
  readonly spec: string;
}): ReactNode {
  const title = changes.find((change) => change.field === "title");
  const parts = changes.filter((change) => change.field !== "title");
  return (
    <div className="space-y-1">
      <div className="flex flex-wrap items-baseline gap-2">
        <Pill>New</Pill>
        {title === undefined ? null : field(title, "title")}
      </div>
      {parts.length === 0 ? null : (
        <div
          className={`flex flex-wrap items-baseline gap-x-1 gap-y-1 text-xs ${textSecondaryOnSurface}`}
          data-testid="going-plan-new-issue-fields"
        >
          {parts.map((change, index) => (
            <Fragment key={change.field}>
              {index === 0 ? null : <span aria-hidden="true"> · </span>}
              {field(change, "part")}
            </Fragment>
          ))}
        </div>
      )}
      {spec.trim() === "" ? null : (
        <div className="space-y-1">
          <p className={textSecondaryOnSurface} data-testid="going-plan-spec-sentences">
            {specSentences(spec)}
          </p>
          <details data-testid="going-plan-spec">
            <summary className={`cursor-pointer text-xs ${textMutedOnSurface}`}>
              The whole spec
            </summary>
            <p className={`text-xs whitespace-pre-wrap break-words ${textSecondaryOnSurface}`}>
              {spec}
            </p>
          </details>
        </div>
      )}
    </div>
  );
}

/**
 * One row of a plan, wherever it is drawn (design v3, state 6): its tick while it waits, then the
 * change as its kind shows it - a change of an issue's fields as the item and each field in the
 * issue page's words with the value now and the new value, a new issue as its title and fields, a
 * message as whom it reaches with its text, a change of a document's text as its sentences and
 * its diff, and any other change as what it does. A row that closes, deletes or removes something
 * is marked in red; a row whose item changed after the person saw it is marked in amber, says its
 * value now, and waits to be ticked again; a row unticked because a row it needs is says so. In
 * Auto-review it says it passed review, or why it waits; once its plan no longer waits, how it
 * stands; and what its write answered.
 */
export function PlanRowView({
  draft,
  plan,
  row,
}: {
  readonly draft: PlanDraft;
  readonly plan: GoingPlanAnswer;
  readonly row: GoingPlanRow;
}): ReactNode {
  const agents = useAgentText();
  const waiting = rowWaits(row) && plan.state === "waiting";
  const ticked = !draft.unticked.has(row.row);
  const edits = draft.edits.get(row.row);
  const newIssues = newIssueTitles(plan.rows ?? [], draft);
  const args = argumentsOf(row);
  const kind = rowKind(row);
  const changes = rowChanges(row, edits, newIssues);
  const removes = rowRemoves(row, changes);
  const changed = row.changed && waiting;
  const session = textOf(args.session);
  const label = rowLabel(
    row,
    session === "" ? edits : { ...edits, session: agents.sessionText(session, undefined) },
    newIssues
  );
  const stands = reviewWords(plan, row) ?? (waiting ? undefined : ROW_STATES[row.state]);
  const resultMessage = rowResultMessage(row);
  const item = row.item || row.issue;
  const project = textOf(args.project) || issueProject(item);
  const field = (change: RowChange, shape: "line" | "title" | "part") => (
    <FieldChange
      change={change}
      editable={waiting}
      issueKey={kind === "new_issue" ? (newIssues.get(row.row) ?? "") : item}
      key={`${plan.run}:${plan.plan}:${change.field}`}
      newIssues={newIssues}
      onEdit={(value) => setDraft(editedDraft(draft, row.row, change.field, value))}
      project={project}
      shape={shape}
    />
  );
  const document = documentChange(row, plan.summary);
  let body: ReactNode;
  if (kind === "new_issue") {
    body = <NewIssueBody changes={changes} field={field} spec={textOf(args.spec)} />;
  } else if (kind === "message") {
    body = <MessageBody row={row} waiting={waiting} />;
  } else if (kind === "document" && document !== undefined) {
    body = <DocumentBody document={document} label={label} />;
  } else if (kind === "change" || kind === "claim") {
    body = (
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <ItemSlot item={item} />{" "}
        <div className="min-w-0 space-y-1">{changes.map((change) => field(change, "line"))}</div>
      </div>
    );
  } else {
    const posts = rowText(row);
    body = (
      <div className="space-y-1">
        <p className={textPrimaryOnSurface}>{label}</p>
        {changes.map((change) => field(change, "line"))}
        {posts === "" ? null : <Quote text={posts} />}
      </div>
    );
  }
  const marked = removes
    ? `border-l-4 pl-2 ${calloutDangerBorder}`
    : changed
      ? `border-l-4 pl-2 ${calloutWarningBorder}`
      : "";
  return (
    <li
      className={`border-t py-2 outline-none first:border-t-0 focus-visible:ring-2 ${focusVisibleRing} ${borderDefault}`}
      data-changed={changed ? "true" : undefined}
      data-kind={kind}
      data-removes={removes ? "true" : undefined}
      data-row={row.row}
      data-state={row.state}
      data-testid="going-plan-row"
      data-ticked={waiting ? String(ticked) : undefined}
      tabIndex={-1}
    >
      <div className={`flex items-start gap-2 ${marked}`}>
        {waiting ? (
          <input
            aria-label={label}
            checked={ticked}
            className="mt-0.5 size-4 shrink-0 cursor-pointer"
            onChange={() => setDraft(toggledDraft(draft, plan.rows ?? [], row.row))}
            type="checkbox"
          />
        ) : null}
        <div className="min-w-0 flex-1 space-y-1 text-sm">
          {body}
          {removes ? (
            <p className={`text-xs font-medium ${dangerText}`} data-testid="going-plan-removes">
              {args.status === "done" ? "Closes" : "Removes"}
            </p>
          ) : null}
          {changed ? (
            <p className={`text-xs ${inlineWarningText}`} data-testid="going-plan-changed">
              Changed since the plan: now {valuesNowWords(changes)}
            </p>
          ) : null}
          {waiting && needsUnticked(draft, row) ? (
            <p className={`text-xs ${textMutedOnSurface}`} data-testid="going-plan-needs">
              Needs the change above
            </p>
          ) : null}
          {stands === undefined ? null : (
            <p className={`text-xs ${textMutedOnSurface}`} data-testid="going-plan-row-state">
              {stands}
            </p>
          )}
          {resultMessage === "" ? null : (
            <p className={`text-xs ${row.state === "failed" ? dangerText : textMutedOnSurface}`}>
              {resultMessage}
            </p>
          )}
        </div>
      </div>
    </li>
  );
}

/**
 * The plan's one primary button, "Approve 3 changes", which counts the rows that wait and are
 * ticked - those the page draws too - with ↵ beside it where Enter in the box approves; Discard,
 * which rejects the plan; and why Dispatch refused the last answer.
 */
export function PlanControls({
  answering,
  draft,
  enterApproves,
}: {
  readonly answering: PlanAnswering;
  readonly draft: PlanDraft;
  readonly enterApproves: boolean;
}): ReactNode {
  const plan = answering.plan;
  if (plan === undefined) {
    return null;
  }
  const count = tickedCount(plan, draft);
  return (
    <div className="space-y-1">
      {plan.state === "waiting" ? (
        <div className="flex flex-wrap items-center gap-2">
          <button
            className={primaryButton}
            data-testid="going-plan-approve"
            disabled={answering.pending || count === 0}
            onClick={answering.approve}
            type="button"
          >
            {count === 1 ? "Approve 1 change" : `Approve ${count} changes`}
            {enterApproves ? (
              <span aria-hidden="true" className="ml-2 font-normal">
                ↵
              </span>
            ) : null}
          </button>
          <button
            className={secondaryButtonCompact}
            data-testid="going-plan-reject"
            disabled={answering.pending}
            onClick={answering.reject}
            type="button"
          >
            Discard
          </button>
        </div>
      ) : null}
      {answering.refusal === undefined ? null : (
        <p className={`text-xs ${dangerText}`} data-testid="going-plan-refused" role="alert">
          {answering.refusal}
        </p>
      )}
    </div>
  );
}

/** The approval mode the plan's run is in, "Ask" or "Auto-review", as a link to where the person
 *  sets it. */
function ModeLink({ mode }: { readonly mode: GoingPlanAnswer["mode"] }): ReactNode {
  const routed = useInRouterContext();
  const className = `text-xs underline ${linkText} ${linkHoverText}`;
  const title = "Set which changes wait for you, in Settings";
  return routed ? (
    <Link className={className} data-testid="going-plan-mode" title={title} to={MODE_SETTING_PATH}>
      {MODE_WORDS[mode]}
    </Link>
  ) : (
    <a className={className} data-testid="going-plan-mode" href={MODE_SETTING_PATH} title={title}>
      {MODE_WORDS[mode]}
    </a>
  );
}

/**
 * The plan card of the bar and the side panel (design v3, state 6): a title made from the plan's
 * rows and its approval mode, linked to its setting; the rows no page on the screen draws, with
 * "2 changes show on this page" for the rest; and the plan's Approve and Discard. A row the page
 * draws (`PlannedChanges`) shows there and not here, so the rows of one plan can show in both
 * places, each once, and Approve approves the ticked rows of both. While the plan waits, the box's
 * ↑ moves the focus onto the card's rows (`usePlanCardKeys`). A plan that could not be read says
 * so, with Retry.
 */
export function PlanCard({ answering }: { readonly answering: PlanAnswering }): ReactNode {
  const going = useGoing();
  const plan = answering.plan;
  const draft = usePlanDraft(plan);
  const list = useRef<HTMLUListElement>(null);
  const waiting = plan?.state === "waiting";
  useEffect(() => {
    if (!waiting) return undefined;
    return holdCardFocus(() => {
      const rows = list.current?.querySelectorAll<HTMLElement>(ROW);
      const nearest = rows?.[rows.length - 1];
      nearest?.focus();
      return nearest !== undefined;
    });
  }, [waiting]);
  if (plan === undefined || draft === undefined) {
    const unreadable = answering.unreadable;
    return unreadable === undefined ? null : (
      <section
        aria-label="Plan"
        className={`rounded-lg border p-3 ${borderDefault}`}
        data-testid="going-plan-unreadable"
      >
        <QueryError
          message={unreadable.message}
          onRetry={unreadable.retry}
          retrying={unreadable.retrying}
        />
      </section>
    );
  }
  const onPage = new Set([...going.onPage.values()].flat());
  const rows = (plan.rows ?? []).filter((row) => !onPage.has(row.row));
  return (
    <section
      aria-label="Plan"
      className={`space-y-2 rounded-lg border p-3 ${borderDefault}`}
      data-plan={plan.plan}
      data-state={plan.state}
      data-testid="going-plan"
    >
      <header className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
        <h3
          className={`text-sm font-semibold ${textPrimaryOnSurface}`}
          data-testid="going-plan-title"
        >
          {planTitle(plan.rows ?? [])}
        </h3>
        <ModeLink mode={plan.mode} />
      </header>
      {plan.summary === "" ? null : (
        <p className={`text-sm ${textSecondaryOnSurface}`}>{plan.summary}</p>
      )}
      {rows.length === 0 ? null : (
        // The rows take the card's keys (`onRowKey`): the focus reaches them from the box.
        <ul data-testid="going-plan-card-rows" onKeyDown={onRowKey} ref={list}>
          {rows.map((row) => (
            <PlanRowView draft={draft} key={row.row} plan={plan} row={row} />
          ))}
        </ul>
      )}
      {onPage.size > 0 ? (
        <p className={`text-xs ${textMutedOnSurface}`} data-testid="going-plan-on-page-note">
          {onPage.size === 1 ? "1 change shows" : `${onPage.size} changes show`} on this page
        </p>
      ) : null}
      <PlanControls answering={answering} draft={draft} enterApproves />
    </section>
  );
}
