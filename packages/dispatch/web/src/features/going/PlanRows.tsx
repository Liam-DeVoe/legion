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

import type { GoingPlanAnswer, GoingPlanRow, GoingUndoLine } from "../../api/types";
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
  pageQuietText,
  primaryButton,
  quoteAccentBorder,
  quoteBodyText,
  secondaryButtonCompact,
  successText,
  textMutedOnSurface,
  textPrimaryOnSurface,
  textSecondaryOnSurface,
} from "../../theme/classes";
import { changedWords } from "../doc/change-set";
import { AssigneeEditor } from "../issue/AssigneeControl";
import { IssueLabels } from "../issue/IssueLabels";
import { issuePriority } from "../issue/issue-priority";
import { PriorityEditor } from "../issue/PriorityControl";
import { StatusMenu } from "../issue-page/StatusMenu";
import { issueStatuses } from "../project/board-model";
import { AgentName, useAgentText } from "../refs/AgentName";
import { ItemChip } from "../refs/ItemChip";
import { objectOf, textOf } from "./fields";
import { draftOf, type PlanDraft, setDraft, useGoing, useGoingValue } from "./going-store";
import {
  argumentsOf,
  broadcastSessions,
  type ChangePiece,
  cardTitle,
  type DocumentChange,
  deliveryWords,
  documentChange,
  editedDraft,
  fieldWords,
  type LabelMark,
  labelMarks,
  MARK_WORDS,
  MODE_WORDS,
  needsUnticked,
  newIssueTitles,
  ROW_MARKS,
  type RowChange,
  type RowMark,
  removalWords,
  reviewWords,
  rowChanges,
  rowKind,
  rowLabel,
  rowOutcome,
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
import { focusConversationBox } from "./RunControls";
import { UndoResult, useRunUndo } from "./RunUndo";
import type { PlanAnswering } from "./usePlan";

/** Each mark of a row as it draws (design v2, state 7): a ring while it waits its turn, a
 *  spinner while it runs, a check once it ran, a cross where it failed, and a dash where it did
 *  not run. Motion stops where the person asked for less. */
const MARK_GLYPHS: Readonly<Record<Exclude<RowMark, "waits">, ReactNode>> = {
  done: <span className={successText}>✓</span>,
  failed: <span className={dangerText}>✕</span>,
  not_run: <span className={textMutedOnSurface}>–</span>,
  running: (
    <span className="inline-block size-3 rounded-full border-2 border-current border-t-transparent motion-safe:animate-spin" />
  ),
  turn: <span className={textMutedOnSurface}>○</span>,
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

/** The issue a row changes, as an answer names an item (design v3, state 6): its chip, with its
 *  key, a dot in its status's colour and its title (`ItemChip`), the title cut where the slot ends
 *  so the fields keep their place beside it. */
function ItemSlot({ item }: { readonly item: string }): ReactNode {
  return (
    <span className="min-w-0 max-w-64" data-item={item} data-testid="going-plan-item">
      {item === "" ? null : <ItemChip issue={item} />}
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
 * value now in the page's grey, which no reader takes for a link, and the new value ("Priority: No
 * priority → P2"); as a new issue's title, the value alone; as a part of a new issue's line, the
 * value with the word that says what it is ("under CORE-1"). While the row waits, a press on the
 * new value, or its Edit, opens the field's own control from the issue page: the priority menu,
 * the label picker, the status menu, the assignee picker, or a box of text; the status menu and the
 * assignee picker stand as the new value itself, as the priority menu does. A value of a field
 * that may name a new issue of the plan (`RowChange.namesNewIssue`) naming one shows that issue's
 * title and "(new)" (`newIssues`), and its edit box holds the value itself, which the person may
 * replace with an issue's key. The edit box starts from the new value of the plan it is drawn
 * for, and its caller keys it to that plan, so a revised plan or a new run's never opens with an
 * earlier plan's value or the person's unsaved typing. An edited value says "Edited".
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
  } else if (edit === "status") {
    value = (
      <span data-plan-edit="" data-testid="going-plan-next">
        <StatusMenu
          label={`Status of ${issueKey}`}
          onPick={onEdit}
          status={typeof next === "string" ? next : ""}
          statuses={issueStatuses}
        />
      </span>
    );
  } else if (edit === "assignee") {
    value = (
      <span data-plan-edit="" data-testid="going-plan-next">
        <AssigneeEditor
          issueKey={issueKey}
          write={{
            error: undefined,
            failed: false,
            pending: false,
            retry: undefined,
            shown: typeof next === "string" && next !== "" ? next : null,
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
          <span className={pageQuietText} data-testid="going-plan-now">
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

/** An issue key in a row's words, as a word of its own (`going/agent/labels.go` writes keys so),
 *  captured so that splitting the words keeps it. */
const KEY_IN_WORDS = /(?<![\w-])([A-Z][A-Z0-9]{1,9}-[1-9][0-9]*)(?![\w-])/u;

/** A change of a document's text (R4): what the row does, naming the issue whose document it
 *  changes (`issue`, "" for a project's document) by its chip - in place of its key where the
 *  row's words write it ("Upload notes.md to DEMO-15"), else after them ("Suggest a change to
 *  spec on DEMO-15") - then two or three sentences on what it changes, and its diff under them,
 *  closed. */
function DocumentBody({
  document,
  issue,
  label,
}: {
  readonly document: DocumentChange;
  readonly issue: string;
  readonly label: string;
}): ReactNode {
  // The words between the keys at the even places, each key at the odd place after them.
  const parts = label.split(KEY_IN_WORDS);
  const named = parts.some((part, index) => index % 2 === 1 && part === issue);
  return (
    <div className="space-y-1">
      <p className={textPrimaryOnSurface} data-testid="going-plan-document">
        {parts.map((part, index) =>
          index % 2 === 1 && part === issue ? (
            // biome-ignore lint/suspicious/noArrayIndexKey: a part is its place in the row's words, which never change for one row
            <ItemSlot item={issue} key={index} />
          ) : (
            part
          )
        )}
        {issue === "" || named ? null : (
          <>
            {" on "}
            <ItemSlot item={issue} />
          </>
        )}
      </p>
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
 * Auto-review it says it passed review, or why it waits. Once its plan no longer waits (design
 * v2, state 7) it ticks in place - a ring while it waits its turn, a spinner while it runs, a
 * check once it ran - and says how it stands (`rowOutcome`): a message's result, a failure's
 * reason with "Say what to do instead" (`onFix`, where given), "Not run: it needs the change
 * above". Once its run was undone, it says what became of its change (`undo`).
 */
export function PlanRowView({
  draft,
  onFix,
  plan,
  row,
  undo,
}: {
  readonly draft: PlanDraft;
  readonly onFix?: () => void;
  readonly plan: GoingPlanAnswer;
  readonly row: GoingPlanRow;
  readonly undo?: GoingUndoLine;
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
  const review = reviewWords(plan, row);
  // A row says how it stands once its plan no longer waits, and where it failed while the plan
  // waits (a row Auto-review ran); a discarded plan's rows ran nothing and say nothing of it.
  const outcome =
    review === undefined &&
    plan.state !== "rejected" &&
    (plan.state !== "waiting" || row.state === "failed")
      ? rowOutcome(row)
      : undefined;
  const resultMessage = outcome === undefined ? rowResultMessage(row) : "";
  const mark: RowMark = plan.state === "rejected" ? "not_run" : ROW_MARKS[row.state];
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
    body = (
      <DocumentBody document={document} issue={row.issue || textOf(args.issue)} label={label} />
    );
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
        ) : mark === "waits" ? null : (
          <span
            className="mt-0.5 inline-flex size-4 shrink-0 items-center justify-center text-sm"
            data-mark={mark}
            data-testid="going-plan-row-mark"
          >
            <span aria-hidden="true">{MARK_GLYPHS[mark]}</span>
            <span className="sr-only">{MARK_WORDS[mark]}</span>
          </span>
        )}
        <div className="min-w-0 flex-1 space-y-1 text-sm">
          {body}
          {removes ? (
            <p className={`text-xs font-medium ${dangerText}`} data-testid="going-plan-removes">
              {removalWords(row, changes)}
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
          {review === undefined ? null : (
            <p className={`text-xs ${textMutedOnSurface}`} data-testid="going-plan-row-state">
              {review}
            </p>
          )}
          {outcome === undefined ? null : (
            <p
              className={`text-xs ${outcome.failed ? dangerText : textMutedOnSurface}`}
              data-testid="going-plan-row-state"
            >
              {outcome.text}
              {outcome.failed && onFix !== undefined ? (
                <>
                  {" "}
                  <button
                    className={`underline ${linkText} ${linkHoverText}`}
                    data-testid="going-plan-fix"
                    onClick={onFix}
                    type="button"
                  >
                    Say what to do instead
                  </button>
                </>
              ) : null}
            </p>
          )}
          {resultMessage === "" ? null : (
            <p className={`text-xs ${row.state === "failed" ? dangerText : textMutedOnSurface}`}>
              {resultMessage}
            </p>
          )}
          {undo === undefined ? null : (
            <p className={`text-xs ${textSecondaryOnSurface}`} data-testid="going-undo-line">
              <UndoResult line={undo} />
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
 * When this card first drew `plan` with the rows sent to run all come to an end (its state
 * "done"), in this page's clock: the time its last row ran, as this page saw it. The run may work
 * on after that, to write its answer, so its end is later. Undefined until then.
 */
function useRanAt(plan: GoingPlanAnswer | undefined): number | undefined {
  const ran = plan?.state === "done" ? `${plan.run}:${plan.plan}` : undefined;
  const [seen, setSeen] = useState<{ readonly plan: string; readonly at: number }>();
  if (ran !== undefined && seen?.plan !== ran) {
    setSeen({ at: Date.now(), plan: ran });
  }
  return seen !== undefined && seen.plan === ran ? seen.at : undefined;
}

/**
 * The plan card of the bar and the side panel. While its plan waits (design v3, state 6): a
 * title made from the plan's rows and its approval mode, linked to its setting; the rows no page
 * on the screen draws, with "2 changes show on this page" for the rest; and the plan's Approve and
 * Discard. A row the page draws (`PlannedChanges`) shows there and not here, so the rows of one
 * plan can show in both places, each once, and Approve approves the ticked rows of both. While the
 * plan waits, the box's ↑ moves the focus onto the card's rows (`usePlanCardKeys`).
 *
 * Once approved (design v2, state 7), its title follows its rows, "Applying 1 of 3…" then
 * "Applied 3 changes · 16:42", the time its last row ran (`useRanAt`) or its run ended
 * (`appliedAt`), whichever this page knows first, each row ticks in place, and a
 * failed row's "Say what to do instead" puts the cursor in the box. From the first row that ran,
 * the card keeps one Undo (R6); once pressed, its title reads "Undone · 3 changes reverted" and
 * each row says what became of its change. An old turn's card (`folded`, design v2, state 8)
 * draws a plan that no longer waits as its one line, "Applied 3 changes · Undo", which opens it.
 * A plan that could not be read says so, with Retry.
 */
export function PlanCard({
  answering,
  appliedAt,
  folded = false,
}: {
  readonly answering: PlanAnswering;
  readonly appliedAt?: number;
  readonly folded?: boolean;
}): ReactNode {
  const going = useGoing();
  const plan = answering.plan;
  const draft = usePlanDraft(plan);
  const undoing = useRunUndo(plan?.run ?? "");
  const ranAt = useRanAt(plan);
  const [open, setOpen] = useState(false);
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
  const lines = undoing.lines;
  const shut = folded && !waiting && !open;
  // The time its last row ran, or its run ended, whichever this page knows to be earlier.
  const appliedTime =
    appliedAt === undefined || ranAt === undefined
      ? (appliedAt ?? ranAt)
      : Math.min(appliedAt, ranAt);
  const title = cardTitle(plan, lines, folded ? undefined : appliedTime);
  // The rows the page draws are the open plan's, which an old turn's card is not.
  const onPage = folded ? new Set<number>() : new Set([...going.onPage.values()].flat());
  const rows = (plan.rows ?? []).filter((row) => !onPage.has(row.row));
  // A line of a change no row drawn here names - one the page drew - still says what became of it.
  const unmatched = (lines ?? []).filter(
    (line) => line.plan !== plan.plan || !rows.some((row) => row.row === line.row)
  );
  const undo =
    lines === undefined && (plan.rows ?? []).some((row) => row.state === "done") ? (
      <button
        className={secondaryButtonCompact}
        data-testid="going-undo-button"
        disabled={undoing.pending}
        onClick={undoing.undo}
        type="button"
      >
        Undo
      </button>
    ) : null;
  const refusal =
    undoing.refusal === undefined ? null : (
      <p className={`text-xs ${dangerText}`} role="alert">
        {undoing.refusal}
      </p>
    );
  if (shut) {
    return (
      <section
        aria-label="Plan"
        className="space-y-1"
        data-folded="true"
        data-plan={plan.plan}
        data-state={plan.state}
        data-testid="going-plan"
      >
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <button
            aria-expanded={false}
            className={`flex min-h-6 items-center gap-1 text-left text-xs hover:underline ${textSecondaryOnSurface}`}
            data-testid="going-plan-title"
            onClick={() => setOpen(true)}
            type="button"
          >
            <span>{title}</span>
            <span aria-hidden="true">›</span>
          </button>
          {undo === null ? null : (
            <>
              <span aria-hidden="true" className={`text-xs ${textMutedOnSurface}`}>
                ·
              </span>
              {undo}
            </>
          )}
        </div>
        {refusal}
      </section>
    );
  }
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
          aria-live="polite"
          className={`text-sm font-semibold ${textPrimaryOnSurface}`}
          data-testid="going-plan-title"
        >
          {folded && !waiting ? (
            <button
              aria-expanded
              className="text-left hover:underline"
              onClick={() => setOpen(false)}
              type="button"
            >
              {title}
            </button>
          ) : (
            title
          )}
        </h3>
        {waiting ? <ModeLink mode={plan.mode} /> : null}
      </header>
      {plan.summary === "" ? null : (
        <p className={`text-sm ${textSecondaryOnSurface}`}>{plan.summary}</p>
      )}
      {rows.length === 0 ? null : (
        // The rows take the card's keys (`onRowKey`): the focus reaches them from the box.
        <ul data-testid="going-plan-card-rows" onKeyDown={onRowKey} ref={list}>
          {rows.map((row) => (
            <PlanRowView
              draft={draft}
              key={row.row}
              onFix={folded ? undefined : focusConversationBox}
              plan={plan}
              row={row}
              undo={lines?.find((line) => line.plan === plan.plan && line.row === row.row)}
            />
          ))}
        </ul>
      )}
      {onPage.size > 0 ? (
        <p className={`text-xs ${textMutedOnSurface}`} data-testid="going-plan-on-page-note">
          {onPage.size === 1 ? "1 change shows" : `${onPage.size} changes show`} on this page
        </p>
      ) : null}
      {unmatched.length === 0 ? null : (
        <ul className={`text-xs ${textSecondaryOnSurface}`}>
          {unmatched.map((line) => (
            <li data-testid="going-undo-line" key={`${line.plan}:${line.row}:${line.tool}`}>
              <UndoResult line={line} />
            </li>
          ))}
        </ul>
      )}
      <PlanControls answering={answering} draft={draft} enterApproves />
      {undo}
      {refusal}
    </section>
  );
}
