import { existsSync } from "node:fs";
import { z } from "zod";
import { type ApprovalSkipReason, isApprovalSkip } from "./approval-skips.ts";
import { eventMoved, eventStatus } from "./authorship.ts";
import {
  Artifact,
  type DispatchReads,
  type DispatchWrites,
  type Event,
  IssueDetails,
  ownerName,
  type ReplaceOp,
  type Whoami,
  type WriteOutcome,
} from "./dispatch-api.ts";
import type { Model } from "./gateway.ts";
import {
  type Examined,
  type Item,
  inPool,
  issueEvents,
  type Occurrence,
  type Paragraph,
} from "./inventory.ts";
import { applyingEntries, judge, type Question, type Span } from "./judge.ts";
import type { MapEntry, Reach } from "./name-map.ts";
import type { Pace } from "./pace.ts";
import { itemPlace } from "./places.ts";
import type { Clock } from "./request-log.ts";
import type { HeldWrite, RunState } from "./state.ts";
import { VERDICT_CODES } from "./verdicts.ts";

/**
 * Write mode's part of a run: who may write, and each item's write. A document gets one
 * `POST /api/v1/artifacts/{id}/edits` with every `replace` of that document, the token read as its
 * precondition and the run's summary; a title one `PATCH /api/v1/issues/{key}` with
 * `expected_sequence`. A refusal is listed and left for the next run, never forced.
 */

/** Whether `whoami` may write: an agent credential that acts for no person. A person's
 *  credential, and a personal agent token (`owner` set), record each write as the person, which
 *  notifies the issue's route, claimant and authors. */
export function writerRefusal(whoami: Whoami): string | null {
  if (whoami.kind === "user") return "whoami kind=user";
  if (whoami.owner !== null) return "whoami kind=agent owner=set";
  return null;
}

/** What one item's turn in write mode came to, as listed. */
export type WriteResult =
  | "nothing-to-judge"
  | "judged-left"
  | "judge-refused"
  | "written"
  | "held"
  | "precondition-failed"
  | "issue-changed"
  | "issue-closed"
  | ApprovalSkipReason
  | "approval-request"
  | `refused:${string}`;

/** Everything a write needs beside the item. */
export interface WriteContext {
  readonly reads: DispatchReads;
  readonly writes: DispatchWrites;
  readonly model: Model;
  readonly pace: Pace;
  readonly clock: Clock;
  readonly state: RunState;
  readonly entries: readonly MapEntry[];
  reachOf(project: string): Reach;
  readonly summary: string;
  readonly concurrency: number;
  /** How many more writes this run may make; Infinity without `--max-writes`. */
  remaining: number;
  /** A file whose presence each write waits for (`--pause-before-write`). */
  readonly pauseFile: string | undefined;
  /** Each issue's events, read once a run. */
  readonly events: Map<string, Promise<Event[]>>;
  /** The keys of the occurrences of the refused writes the state held back, which this run tries
   *  again: judging one of them is judging it again. */
  readonly retried: ReadonlySet<string>;
  /** The model calls of this run. */
  readonly calls: JudgeCalls;
}

/** The model calls of a run: first asks, second asks after a refused first answer, and first asks
 *  of occurrences judged again for a retried refused write, with the count of those occurrences. */
export interface JudgeCalls {
  first: number;
  second: number;
  again: number;
  againOccurrences: number;
}

/** The write a held item stands for in the state. An item held before it was judged carries no
 *  keys; `retry` marks a write Dispatch refused, whose occurrences the next run judges again. */
export function heldWrite(item: Item, keys: readonly string[], retry = false): HeldWrite {
  return {
    kind: item.kind === "title" ? "t" : item.kind === "issue-document" ? "i" : "p",
    project: item.project,
    issue: item.issue?.key ?? null,
    id: item.id,
    keys,
    retry,
  };
}

const PREFIX_BREAK = /\s/;

/**
 * The `replace` that turns `before` into `after` inside `markdown`: the stretch that differs,
 * widened to whole words, then widened a word at a time on both sides until it matches once in
 * the document. Where even the whole paragraph matches more than once, `occurrence` names which.
 */
export function replaceOp(markdown: string, paragraph: Paragraph, after: string): ReplaceOp {
  const before = paragraph.text;
  let prefix = 0;
  while (prefix < before.length && prefix < after.length && before[prefix] === after[prefix]) {
    prefix += 1;
  }
  let suffix = 0;
  while (
    suffix < before.length - prefix &&
    suffix < after.length - prefix &&
    before[before.length - 1 - suffix] === after[after.length - 1 - suffix]
  ) {
    suffix += 1;
  }
  const widen = () => {
    while (prefix > 0 && !PREFIX_BREAK.test(before.charAt(prefix - 1))) prefix -= 1;
    while (suffix > 0 && !PREFIX_BREAK.test(before.charAt(before.length - suffix))) suffix -= 1;
  };
  widen();
  const count = (find: string) => markdown.split(find).length - 1;
  let find = before.slice(prefix, before.length - suffix);
  while (count(find) > 1 && (prefix > 0 || suffix > 0)) {
    prefix = Math.max(0, prefix - 1);
    suffix = Math.max(0, suffix - 1);
    widen();
    find = before.slice(prefix, before.length - suffix);
  }
  const replacement = after.slice(prefix, after.length - suffix);
  if (count(find) <= 1) return { op: "replace", find, with: replacement };
  const at = paragraph.start + prefix;
  let occurrence = 0;
  for (let index = markdown.indexOf(find); index !== -1 && index < at; ) {
    occurrence += 1;
    index = markdown.indexOf(find, index + 1);
  }
  return { op: "replace", find, with: replacement, occurrence };
}

function statusLines(events: readonly Event[]): string[] {
  return events
    .filter((event) => eventMoved(event, "status"))
    .slice(-5)
    .map((event) => `${event.created_at} ${eventStatus(event) ?? "unknown"}`);
}

/** Waits until the pause file exists, when the run names one. */
async function pause(context: WriteContext): Promise<void> {
  if (context.pauseFile === undefined) return;
  while (!existsSync(context.pauseFile)) await context.clock.sleep(1_000);
}

/** The document's approval as Dispatch holds it just before the write: a person who approved it,
 *  or opened an approval request on it, since it was read leaves it after all. */
async function approvalNow(context: WriteContext, examined: Examined): Promise<WriteResult | null> {
  const item = examined.item;
  const artifact = item.artifact;
  if (artifact === null) return null;
  const listed = z
    .array(Artifact)
    .parse(
      item.issue === null
        ? await context.reads.projectDocuments(item.project)
        : await context.reads.issueArtifacts(item.issue.key)
    )
    .find((candidate) => candidate.id === artifact.id);
  if (listed === undefined) return "refused:ARTIFACT_NOT_FOUND";
  const reason = itemPlace({
    outOfScope: false,
    thisTree: false,
    closed: false,
    primary: listed.primary === true,
    approval: listed.approval,
    blocks: examined.blocks,
  });
  if (reason === "approval-request" || (reason !== null && isApprovalSkip(reason))) return reason;
  return null;
}

/**
 * The sequence a title's write expects, read again just before the write: the issue's last event
 * now, where the title is still the one the tool read and judged. An event that changed nothing of
 * the title since the read (a comment, a status move) is no reason to leave a judged rewrite to
 * the next run, which would judge it again; a changed title is a new text, and a closed issue
 * takes no title. The write's `expected_sequence` still refuses any event after this read.
 */
async function titleSequence(
  context: WriteContext,
  examined: Examined
): Promise<number | WriteResult> {
  const details = IssueDetails.parse(await context.reads.issue(examined.item.id));
  if (details.closed_at !== null || details.status === "done") return "issue-closed";
  if (details.title !== examined.text) return "issue-changed";
  return details.last_seq;
}

function resultOf(outcome: WriteOutcome, title: boolean): WriteResult {
  if (outcome.ok) return "written";
  if (!title && outcome.status === 409 && outcome.code === "PRECONDITION_FAILED") {
    return "precondition-failed";
  }
  if (title && outcome.status === 409 && outcome.code === "ISSUE_CHANGED") return "issue-changed";
  if (title && outcome.status === 409 && outcome.code === "ISSUE_CLOSED") return "issue-closed";
  return `refused:${outcome.code}`;
}

/**
 * One item's turn in write mode. The occurrences no place leaves and no earlier verdict decided
 * are judged only once the pace admits the write, so an item the pace holds back is held unjudged
 * and its occurrences are judged once, by the run that writes them.
 */
export async function writeItem(context: WriteContext, examined: Examined): Promise<WriteResult> {
  const item = examined.item;
  // The occurrences to judge, grouped by paragraph: no place leaves them, no verdict decided them.
  const byParagraph = new Map<number, { paragraph: Paragraph; judged: Occurrence[] }>();
  const keys: string[] = [];
  for (const occurrence of examined.occurrences) {
    const { paragraph, key } = occurrence;
    if (occurrence.reason !== null || key === null || paragraph === null) continue;
    if (context.state.verdicts.has(key)) continue;
    keys.push(key);
    const group = byParagraph.get(paragraph.start) ?? { paragraph, judged: [] };
    group.judged.push(occurrence);
    byParagraph.set(paragraph.start, group);
  }
  if (keys.length === 0) return "nothing-to-judge";
  const hold = (): WriteResult => {
    context.state.held.push(heldWrite(item, keys));
    return "held";
  };
  if (context.remaining <= 0) return hold();
  const paced = { owner: ownerName(item.owner), project: item.project };
  if (!context.pace.admits(paced)) return hold();

  const issueKey = item.issue?.key;
  let events: readonly Event[] = examined.events;
  if (issueKey !== undefined && events.length === 0) {
    let read = context.events.get(issueKey);
    if (read === undefined) {
      read = issueEvents(context.reads, issueKey);
      context.events.set(issueKey, read);
    }
    events = await read;
  }
  const entries = applyingEntries(context.entries, context.reachOf(item.project));
  const groups = [...byParagraph.values()];
  const answers = await inPool(groups, context.concurrency, async (group) => {
    const relative = (occurrence: Occurrence): Span => ({
      start: occurrence.start - group.paragraph.start,
      end: occurrence.end - group.paragraph.start,
    });
    const kept = examined.occurrences.filter(
      (occurrence) =>
        occurrence.paragraph?.start === group.paragraph.start && !group.judged.includes(occurrence)
    );
    const question: Question = {
      paragraph: group.paragraph.text,
      judged: group.judged.map(relative),
      kept: kept.map(relative),
      kind: item.kind.replace("-", " "),
      status: item.issue?.status ?? null,
      statusEvents: statusLines(events),
      entries,
    };
    const judged = group.judged.flatMap((occurrence) =>
      occurrence.key === null ? [] : [occurrence.key]
    );
    const again = judged.filter((key) => context.retried.has(key)).length;
    const answer = await judge(question, context.model);
    if (again > 0) {
      context.calls.again += 1;
      context.calls.againOccurrences += again;
    } else {
      context.calls.first += 1;
    }
    if (answer.asks === 2) context.calls.second += 1;
    return { group, judged, judgement: answer.judgement };
  });

  const rewrites: { paragraph: Paragraph; after: string }[] = [];
  let unanswered = false;
  let refused = false;
  for (const { group, judged, judgement } of answers) {
    if (judgement.kind === "allowed") {
      for (const key of judged) context.state.verdicts.set(key, VERDICT_CODES[judgement.allowed]);
    }
    // Two failed answers are kept as `judge-refused`: an occurrence for a person to change by
    // hand, which every report lists until its text changes and no later run asks about again.
    if (judgement.kind === "judge-refused") {
      refused = true;
      for (const key of judged) {
        context.state.verdicts.set(key, VERDICT_CODES["judge-refused"]);
        context.state.refused.push({
          project: item.project,
          issue: item.issue?.key ?? null,
          id: item.id,
          key,
        });
      }
    }
    if (judgement.kind === "rewrite") {
      rewrites.push({ paragraph: group.paragraph, after: judgement.paragraph });
    }
    if (judgement.kind === "no-answer") unanswered = true;
  }
  // A paragraph the gateway gave no answer for, and a write Dispatch refuses, are kept in the
  // state's held-back writes: the next run reads the item again and asks or tries again. The
  // rewrites this run judged are written now, since a later run would judge them again.
  const keep = (retry: boolean) => {
    const held = context.state.held;
    const at = held.findIndex((write) => write.id === item.id);
    if (at === -1) held.push(heldWrite(item, keys, retry));
    else if (retry) held[at] = heldWrite(item, keys, true);
  };
  if (unanswered) keep(false);
  if (rewrites.length === 0) {
    if (unanswered) return "held";
    return refused ? "judge-refused" : "judged-left";
  }

  let sequence = 0;
  if (item.kind === "title") {
    const fresh = await titleSequence(context, examined);
    if (typeof fresh !== "number") {
      keep(true);
      return fresh;
    }
    sequence = fresh;
  } else {
    const approval = await approvalNow(context, examined);
    if (approval !== null) return approval;
  }
  await pause(context);
  await context.pace.gap();
  context.remaining -= 1;
  let outcome: WriteOutcome;
  if (item.kind === "title") {
    const title = rewrites[0]?.after ?? examined.text;
    outcome = await context.writes.retitle(item.id, title, sequence);
  } else {
    const ops = rewrites
      .sort((a, b) => a.paragraph.start - b.paragraph.start)
      .map((rewrite) => replaceOp(examined.text, rewrite.paragraph, rewrite.after));
    outcome = await context.writes.edit(item.id, ops, examined.token ?? "", context.summary);
  }
  if (outcome.ok) context.pace.wrote(paced);
  else keep(true);
  return resultOf(outcome, item.kind === "title");
}
