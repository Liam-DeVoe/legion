import { existsSync } from "node:fs";
import type { ApprovalSkipReason } from "./approval-skips.ts";
import { eventMoved, eventStatus } from "./authorship.ts";
import {
  Artifact,
  type DispatchReads,
  type DispatchWrites,
  type Event,
  IssueDetails,
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
import {
  applyingEntries,
  checkAnswer,
  type Judged,
  judge,
  type Question,
  readAnswer,
  type Span,
} from "./judge.ts";
import type { MapEntry, Reach } from "./name-map.ts";
import type { Pace, PaceLog } from "./pace.ts";
import { approvalPlace } from "./places.ts";
import type { Clock } from "./request-log.ts";
import type { HeldWrite, RunState } from "./state.ts";
import { VERDICT_CODES, type VerdictCode, verdictKey } from "./verdicts.ts";

/**
 * Each item's write, for write mode and for the settle command (spec version 43), which is write
 * mode with the tester's answers in place of the model's. A document gets one
 * `POST /api/v1/artifacts/{id}/edits` with every `replace` of that document, the token read as its
 * precondition and the run's summary; a title one `PATCH /api/v1/issues/{key}` with
 * `expected_sequence`. Just before each write the item is read again, and an item that a place a
 * program tells now holds whole is not written. A refusal that no other writer's edit caused
 * (413, a 5xx) is tried once more at once; a refusal is then listed and left for the next run,
 * never forced.
 */

/** Why a run may not write, or null where it may: it needs an agent credential that acts for no
 *  person, and a session to act as. A person's credential, and a personal agent token (`owner`
 *  set), record each write as the person, which notifies the issue's route, claimant and authors;
 *  a run with no session (`DISPATCH_SESSION_ID`) could not keep its own id in the state, so a
 *  report could not set its edits aside. */
export function writerRefusal(whoami: Whoami, session: string | undefined): string | null {
  if (whoami.kind === "user") return "whoami kind=user";
  if (whoami.owner !== null) return "whoami kind=agent owner=set";
  if (session === undefined) return "DISPATCH_SESSION_ID names no session to write as";
  return null;
}

/** What one item's turn came to, as listed. */
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

/** Who answers the paragraphs of an item: the model, in write mode, or the tester, in the settle
 *  command, whose answers have the model's shape and are keyed by the verdict key of an
 *  occurrence the paragraph holds; `why` hears why an answer failed the check. */
export type Answerer =
  | { readonly kind: "model"; readonly model: Model; readonly concurrency: number }
  | {
      readonly kind: "tester";
      readonly answers: ReadonlyMap<string, unknown>;
      readonly why: (line: string) => void;
    };

/** Everything a write needs beside the item. */
export interface WriteContext {
  readonly reads: DispatchReads;
  readonly writes: DispatchWrites;
  readonly answerer: Answerer;
  readonly pace: Pace;
  /** What the tool's sessions wrote, from Dispatch's events, for the pace. */
  readonly paceLog: PaceLog;
  /** Runs just before each write: it saves the running session's id in the state the first
   *  time, so a write whose later save fails is still the tool's own edit. */
  readonly beforeWrite: () => Promise<void>;
  readonly clock: Clock;
  readonly state: RunState;
  readonly entries: readonly MapEntry[];
  reachOf(project: string): Reach;
  readonly summary: string;
  /** How many more writes this run may make; Infinity without `--max-writes`. */
  remaining: number;
  /** A file whose presence each write waits for (`--pause-before-write`). */
  readonly pauseFile: string | undefined;
  /** Each issue's last five status moves, by issue key, read once a run. */
  readonly statusLines: Map<string, Promise<string[]>>;
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
  return { project: item.project, issue: item.issue?.key ?? null, id: item.id, keys, retry };
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

/** An issue's last five status moves, oldest first, each `<time> <status>`. */
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

/** A document's place as Dispatch holds it just before the write: its issue closed, a person's
 *  approval (`GET /api/v1/artifacts/{id}`), or an open approval request, since the read leaves it
 *  after all; a document that is gone is refused. Null for a document that may be written. */
async function documentPlaceNow(
  reads: Pick<DispatchReads, "artifact" | "issue">,
  examined: Examined,
  artifactId: string
): Promise<WriteResult | null> {
  const issue = examined.item.issue;
  if (issue !== null) {
    const details = IssueDetails.parse(await reads.issue(issue.key));
    if (details.closed_at !== null || details.status === "done") return "issue-closed";
  }
  const answer = await reads.artifact(artifactId);
  if (answer === null) return "refused:ARTIFACT_NOT_FOUND";
  const now = Artifact.parse(answer);
  return approvalPlace({
    primary: now.primary === true,
    approval: now.approval,
    blocks: examined.blocks,
  });
}

/**
 * The sequence a title's write expects, read again just before the write: the issue's last event
 * now, where the title is still the one the tool read and judged. An event that changed nothing of
 * the title since the read (a comment, a status move) is no reason to leave a judged rewrite to
 * the next run, which would judge it again; a changed title is a new text, and a closed issue
 * takes no title. The write's `expected_sequence` still refuses any event after this read.
 */
async function titleSequence(
  reads: Pick<DispatchReads, "issue">,
  examined: Examined
): Promise<number | WriteResult> {
  const details = IssueDetails.parse(await reads.issue(examined.item.id));
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

/** Whether a refusal has no edit behind it, so the same write may be tried again at once: a body
 *  past Dispatch's cap (413 `CAP_EXCEEDED`) or a fault of the server (a 5xx). */
function triedAgain(outcome: WriteOutcome): boolean {
  return !outcome.ok && (outcome.status === 413 || outcome.status >= 500);
}

/** A paragraph's occurrences to judge, and their keys. */
interface Group {
  readonly paragraph: Paragraph;
  readonly judged: Occurrence[];
  readonly keys: string[];
}

/** The tester's answer to one paragraph, held to the check a model's answer meets. */
function testerJudgement(
  answers: ReadonlyMap<string, unknown>,
  group: Group,
  question: Question
): Judged {
  const key = group.keys.find((candidate) => answers.has(candidate));
  const answer = key === undefined ? undefined : answers.get(key);
  return { judgement: checkAnswer(question, readAnswer(JSON.stringify(answer))), asks: 1 };
}

/**
 * One item's turn. The occurrences no place leaves and no earlier verdict decided are judged only
 * once the pace admits the write, so an item the pace holds back is held unjudged and its
 * occurrences are judged once, by the run that writes them. The settle command's answerer judges
 * the occurrences a code keeps as judge-refused in the paragraphs it answers, and nothing else;
 * an answer of it that fails the check leaves the item as it is.
 */
export async function writeItem(context: WriteContext, examined: Examined): Promise<WriteResult> {
  const item = examined.item;
  const issueKey = item.issue?.key;
  const answerer = context.answerer;
  const state = context.state;
  /** An issue's status moves, from its events, which also show the pace the tool's older writes. */
  const history = (events: readonly Event[]) => {
    context.paceLog.saw(events);
    return statusLines(events);
  };
  // A title's events were read when it was examined: its issue's history comes from them.
  if (item.kind === "title" && examined.events.length > 0 && !context.statusLines.has(item.id)) {
    context.statusLines.set(item.id, Promise.resolve(history(examined.events)));
  }
  // The occurrences to judge, grouped by paragraph: no place leaves them, no verdict decided them.
  const byParagraph = new Map<number, Group>();
  for (const occurrence of examined.occurrences) {
    const { paragraph, key } = occurrence;
    if (occurrence.reason !== null || key === null || paragraph === null) continue;
    const code = state.verdicts.get(key);
    const again = answerer.kind === "tester" && code === VERDICT_CODES["judge-refused"];
    if (code !== undefined && !again) continue;
    const group = byParagraph.get(paragraph.start) ?? { paragraph, judged: [], keys: [] };
    group.judged.push(occurrence);
    group.keys.push(key);
    byParagraph.set(paragraph.start, group);
  }
  const groups = [...byParagraph.values()].filter(
    (group) => answerer.kind === "model" || group.keys.some((key) => answerer.answers.has(key))
  );
  const keys = groups.flatMap((group) => group.keys);
  if (keys.length === 0) return "nothing-to-judge";
  const hold = (): WriteResult => {
    state.held.push(heldWrite(item, keys));
    return "held";
  };
  if (context.remaining <= 0) return hold();

  let moves: readonly string[] = [];
  if (issueKey !== undefined) {
    let read = context.statusLines.get(issueKey);
    if (read === undefined) {
      read = issueEvents(context.reads, issueKey).then(history);
      context.statusLines.set(issueKey, read);
    }
    moves = await read;
  }
  await context.paceLog.refresh();
  const paced = { owner: issueKey ?? item.id, project: item.project };
  if (!context.pace.admits(paced)) return hold();

  const entries = applyingEntries(context.entries, context.reachOf(item.project));
  const questionOf = (group: Group): Question => {
    const relative = (occurrence: Occurrence): Span => ({
      start: occurrence.start - group.paragraph.start,
      end: occurrence.end - group.paragraph.start,
    });
    const kept = examined.occurrences.filter(
      (occurrence) =>
        occurrence.paragraph?.start === group.paragraph.start && !group.judged.includes(occurrence)
    );
    return {
      paragraph: group.paragraph.text,
      judged: group.judged.map(relative),
      kept: kept.map(relative),
      kind: item.kind.replace("-", " "),
      status: item.issue?.status ?? null,
      statusEvents: moves,
      entries,
    };
  };
  let answers: { group: Group; judgement: Judged["judgement"] }[];
  if (answerer.kind === "tester") {
    answers = groups.map((group) => ({
      group,
      judgement: testerJudgement(answerer.answers, group, questionOf(group)).judgement,
    }));
    // The tester's answers of one item stand or fall together: one that fails the check leaves
    // the item as it is, and says why.
    let failed = false;
    for (const { group, judgement } of answers) {
      if (judgement.kind !== "judge-refused") continue;
      failed = true;
      answerer.why(`answer for key ${group.keys.join(",")}: ${judgement.reason}`);
    }
    if (failed) return "judge-refused";
  } else {
    const model = answerer.model;
    answers = await inPool(groups, answerer.concurrency, async (group) => {
      const again = group.keys.filter((key) => context.retried.has(key)).length;
      const answer = await judge(questionOf(group), model);
      if (again > 0) {
        context.calls.again += 1;
        context.calls.againOccurrences += again;
      } else {
        context.calls.first += 1;
      }
      if (answer.asks === 2) context.calls.second += 1;
      return { group, judgement: answer.judgement };
    });
  }

  /** The state's judge-refused entries of these keys go: an answer replaced their code. */
  const settled = (settledKeys: readonly string[]) => {
    state.refused = state.refused.filter(
      (entry) => entry.id !== item.id || !settledKeys.includes(entry.key)
    );
  };
  // Each rewrite, with the verdict of each occurrence it leaves in its paragraph, in their order:
  // the answer's place for a marked one it names as left, the earlier verdict of one the run did
  // not ask about, and none for one a place a program tells keeps.
  const rewrites: {
    group: Group;
    after: string;
    left: (VerdictCode | undefined)[];
  }[] = [];
  let unanswered = false;
  let refused = false;
  for (const { group, judgement } of answers) {
    if (judgement.kind === "allowed") {
      for (const key of group.keys) state.verdicts.set(key, VERDICT_CODES[judgement.allowed]);
      settled(group.keys);
    }
    // Two failed answers of the model are kept as `judge-refused`: an occurrence for the tester's
    // settle command, which no later run asks the model about.
    if (judgement.kind === "judge-refused") {
      refused = true;
      for (const key of group.keys) {
        state.verdicts.set(key, VERDICT_CODES["judge-refused"]);
        state.refused.push({
          project: item.project,
          issue: item.issue?.key ?? null,
          id: item.id,
          key,
        });
      }
    }
    if (judgement.kind === "rewrite") {
      const start = group.paragraph.start;
      const leftAt = new Map(
        (judgement.left ?? []).map((one) => [start + one.start, one.allowed] as const)
      );
      const left = examined.occurrences
        .filter(
          (occurrence) =>
            occurrence.paragraph?.start === start &&
            (!group.judged.includes(occurrence) || leftAt.has(occurrence.start))
        )
        .map((occurrence) => {
          const answered = leftAt.get(occurrence.start);
          if (answered !== undefined) return VERDICT_CODES[answered];
          const code = occurrence.key === null ? undefined : state.verdicts.get(occurrence.key);
          return code === VERDICT_CODES["judge-refused"] ? undefined : code;
        });
      rewrites.push({ group, after: judgement.paragraph, left });
    }
    if (judgement.kind === "no-answer") unanswered = true;
  }
  // A paragraph the gateway gave no answer for, and a write Dispatch refuses, are kept in the
  // state's held-back writes: the next run reads the item again and asks or tries again. The
  // rewrites this run judged are written now, since a later run would judge them again.
  const keep = (retry: boolean) => {
    const at = state.held.findIndex((write) => write.id === item.id);
    if (at === -1) state.held.push(heldWrite(item, keys, retry));
    else if (retry) state.held[at] = heldWrite(item, keys, true);
  };
  if (unanswered) keep(false);
  if (rewrites.length === 0) {
    if (unanswered) return "held";
    return refused ? "judge-refused" : "judged-left";
  }

  // The item read again just before the write.
  let sequence = 0;
  const artifact = item.artifact;
  if (artifact === null) {
    const fresh = await titleSequence(context.reads, examined);
    if (typeof fresh !== "number") {
      keep(true);
      return fresh;
    }
    sequence = fresh;
  } else {
    const place = await documentPlaceNow(context.reads, examined, artifact.id);
    if (place !== null) return place;
  }
  await pause(context);
  await context.pace.gap();
  await context.beforeWrite();
  context.remaining -= 1;
  const write = (): Promise<WriteOutcome> => {
    if (artifact === null) {
      return context.writes.retitle(item.id, rewrites[0]?.after ?? examined.text, sequence);
    }
    const ops = rewrites
      .sort((a, b) => a.group.paragraph.start - b.group.paragraph.start)
      .map((rewrite) => replaceOp(examined.text, rewrite.group.paragraph, rewrite.after));
    return context.writes.edit(item.id, ops, examined.token ?? "", context.summary);
  };
  let outcome = await write();
  if (triedAgain(outcome)) outcome = await write();
  if (outcome.ok) {
    context.pace.wrote(paced);
    // Each rewritten paragraph keeps, under its new hash, the verdict of each occurrence it left
    // there (spec versions 39 and 40), and the codes of the paragraph it replaced go.
    for (const rewrite of rewrites) {
      rewrite.left.forEach((code, index) => {
        if (code !== undefined) state.verdicts.set(verdictKey(item.id, rewrite.after, index), code);
      });
      for (const key of rewrite.group.keys) state.verdicts.delete(key);
      settled(rewrite.group.keys);
    }
  } else {
    keep(true);
  }
  return resultOf(outcome, artifact === null);
}
