import type { Model } from "./gateway.ts";
import { applies, type MapEntry, type Reach } from "./name-map.ts";
import { countWord, holdsWord, matches } from "./pattern.ts";
import { ALLOWED_NAMES, type AllowedName } from "./verdicts.ts";

/**
 * The judge of write mode: one model call per paragraph that holds occurrences no program-told
 * place leaves, and `confined`, which refuses an answer that changes more than the sentences of
 * those occurrences. The prompt below is one of the places the word may stand in the repository
 * (R1, kind 4): it must name the word to ask about it.
 */

/** A sentence's span in a text, as JavaScript indexes it. */
export interface Span {
  readonly start: number;
  readonly end: number;
}

/** The sentences of a text, end to end, so that they cover it whole: a sentence ends after `.`,
 *  `!` or `?` followed by white space, or at a line end. */
export function sentenceSpans(text: string): Span[] {
  const spans: Span[] = [];
  let start = 0;
  const boundary = /[.!?](?=\s)|\n/g;
  for (let hit = boundary.exec(text); hit !== null; hit = boundary.exec(text)) {
    const end = hit.index + hit[0].length;
    spans.push({ start, end });
    start = end;
  }
  if (start < text.length || spans.length === 0) spans.push({ start, end: text.length });
  return spans;
}

/** The sentence of `text` that holds the index `at`. */
export function sentenceAt(text: string, at: number): Span {
  return (
    sentenceSpans(text).find((span) => at >= span.start && at < span.end) ?? {
      start: 0,
      end: text.length,
    }
  );
}

/**
 * Why `after` is not `before` with only the sentences that hold the judged occurrences rewritten,
 * or null when it is. The answer must hold no trace of the word but the occurrences the
 * paragraph keeps for a program-told reason (`kept`, each a span of `before`, which must stay as
 * they are).
 *
 * The sentences of `before` that hold a judged occurrence are the rewritable parts; every other
 * part must stand in `after` unchanged and in its order: the text before the first rewritable
 * sentence as a prefix, the text after the last as a suffix, and each stretch between two of them
 * after the one before it. The reason quotes the part that changed, for the model's second ask.
 */
export function confinedProblem(
  before: string,
  after: string,
  judged: readonly Span[],
  kept: readonly Span[] = []
): string | null {
  if (judged.length === 0) return before === after ? null : "the paragraph had to stay as it was";
  if (countWord(after) !== kept.length) {
    return "the rewrite still holds the word where it had to say what the word meant";
  }
  const spans = sentenceSpans(before);
  const hot = spans.filter((span) =>
    judged.some((occurrence) => occurrence.start >= span.start && occurrence.start < span.end)
  );
  for (const span of kept) {
    const text = before.slice(span.start, span.end);
    if (!after.includes(text))
      return `the rewrite changed ${JSON.stringify(text)}, which had to stay`;
  }
  const first = hot[0];
  const last = hot.at(-1);
  if (first === undefined || last === undefined) return "no sentence holds a marked occurrence";
  const prefix = before.slice(0, first.start);
  const suffix = before.slice(last.end);
  if (!after.startsWith(prefix)) {
    return `the rewrite changed the text before the marked sentences, which had to stay exactly ${JSON.stringify(prefix)}`;
  }
  if (!after.endsWith(suffix) || prefix.length + suffix.length > after.length) {
    return `the rewrite changed the text after the marked sentences, which had to stay exactly ${JSON.stringify(suffix)}`;
  }
  const middle = after.slice(prefix.length, after.length - suffix.length);
  let cursor = 0;
  for (let index = 0; index + 1 < hot.length; index += 1) {
    const current = hot[index];
    const next = hot[index + 1];
    if (current === undefined || next === undefined) break;
    const between = before.slice(current.end, next.start);
    if (between === "") continue;
    const found = middle.indexOf(between, cursor);
    if (found === -1) {
      return `the rewrite changed the text between two marked sentences, which had to stay exactly ${JSON.stringify(between)}`;
    }
    cursor = found + between.length;
  }
  return null;
}

/** Whether `after` changes only the sentences of the judged occurrences (`confinedProblem`). */
export function confined(
  before: string,
  after: string,
  judged: readonly Span[],
  kept: readonly Span[] = []
): boolean {
  return confinedProblem(before, after, judged, kept) === null;
}

/** The judge's answer to one paragraph. A refused answer says why, for the second ask. */
export type Judgement =
  | { readonly kind: "rewrite"; readonly paragraph: string }
  | { readonly kind: "allowed"; readonly allowed: AllowedName }
  | { readonly kind: "judge-refused"; readonly reason: string }
  /** No answer came (the gateway's fault): nothing is decided, and the item waits. */
  | { readonly kind: "no-answer" };

const NOT_THE_SHAPE =
  'the answer was not one JSON object, either {"rewrite": "<the paragraph>"} or {"allowed": "<one of the five reasons>"}';

/** Reads the model's text as the JSON the prompt asks for: exactly `{"rewrite": "<paragraph>"}`
 *  or `{"allowed": "<reason>"}`. Anything else is refused. */
export function readAnswer(text: string): Judgement {
  const refused = { kind: "judge-refused", reason: NOT_THE_SHAPE } as const;
  const open = text.indexOf("{");
  const close = text.lastIndexOf("}");
  if (open === -1 || close < open) return refused;
  let value: unknown;
  try {
    value = JSON.parse(text.slice(open, close + 1));
  } catch {
    return refused;
  }
  if (typeof value !== "object" || value === null || Array.isArray(value)) return refused;
  if (Object.keys(value).length !== 1) return refused;
  if ("rewrite" in value && typeof value.rewrite === "string") {
    return { kind: "rewrite", paragraph: value.rewrite };
  }
  if ("allowed" in value) {
    const allowed = ALLOWED_NAMES.find((name) => name === value.allowed);
    if (allowed !== undefined) return { kind: "allowed", allowed };
  }
  return refused;
}

/** What the judge is told of one paragraph. */
export interface Question {
  readonly paragraph: string;
  /** The occurrences to judge, as spans of the paragraph. */
  readonly judged: readonly Span[];
  /** The occurrences a program-told place keeps, which the answer must keep as they are. */
  readonly kept: readonly Span[];
  /** `title`, `issue document` or `project document`. */
  readonly kind: string;
  /** The issue's status, or null for a project document. */
  readonly status: string | null;
  /** The issue's last five status moves, oldest first, each `<time> <status>`. */
  readonly statusEvents: readonly string[];
  /** The map entries that apply in the item's project. */
  readonly entries: readonly MapEntry[];
}

const SYSTEM = `You edit text that AI agents wrote in Dispatch, an issue tracker. The word "park"
(park, parks, parked, parking, and names that hold it, such as park_child or isParked) is being
removed, because it hides what happened. Each use gets the words of its own meaning:

- Where it meant that an issue went to the backlog or the icebox, write that status: "moved to
  backlog", "in backlog or icebox". Where done or triage are meant too, "out of the workflow".
- A relaunch that the Legion daemon's wake pacing holds back is "delayed", never "backlog".
- A launch that waits for the issue pod's start gate "waits for the pod".
- A person's decision kept for a tree that does not run is a "kept decision".
- For any other meaning, write what happens there: blocked, waits, paused, held, pending.
- A renamed code name gets its new name exactly, from the list of names you are given. A name
  that is not on the list stays as it is.

Some uses must stay. Answer with the reason instead of a rewrite when the marked occurrence is:
- "quote": inside a quote of a person's words;
- "word-as-word": the word named as a word, in quotation marks, such as a record of the rule
  against it;
- "other-team-technical": another team's own technical term, such as a parked Linux kernel
  thread or kthread_park;
- "third-party-name": a name that code outside these repositories defines (a crate, an API, a
  field of another program);
- "record": a record kept as the fixed input or expected output of a test or a measurement.

Rewrite only the sentences that hold the marked occurrences. Change nothing else: keep every
other sentence, every space, every link and every markdown mark exactly. Keep each occurrence
listed under "keep" exactly as it is. If any marked occurrence must stay, answer with its reason.

Answer with one JSON object and nothing else, either {"rewrite": "<the whole paragraph, rewritten>"}
or {"allowed": "quote" | "word-as-word" | "other-team-technical" | "third-party-name" | "record"}.`;

/** The prompt of one question; `refusal` is why the first answer was refused, for a second ask. */
export function buildPrompt(
  question: Question,
  refusal?: string
): { system: string; user: string } {
  const quoted = (span: Span) => JSON.stringify(question.paragraph.slice(span.start, span.end));
  const names = question.entries.map(
    (entry) => `- ${entry.old} -> ${entry.new}${entry.codeName ? " (a code name)" : ""}`
  );
  const lines = [
    `Kind of text: ${question.kind}.`,
    `The issue's status: ${question.status ?? "none (a project document)"}.`,
    `Its last status moves: ${question.statusEvents.length === 0 ? "none" : question.statusEvents.join("; ")}.`,
    "",
    "Renamed names that apply here:",
    ...(names.length === 0 ? ["- none"] : names),
    "",
    "The paragraph, between the lines:",
    "-----",
    question.paragraph,
    "-----",
    "",
    "Marked occurrences (character offsets in the paragraph):",
    ...question.judged.map((span) => `- at ${span.start}: ${quoted(span)}`),
    ...(question.kept.length === 0
      ? []
      : ["Keep exactly:", ...question.kept.map((span) => `- at ${span.start}: ${quoted(span)}`)]),
    ...(refusal === undefined
      ? []
      : [
          "",
          `Your earlier answer to this paragraph was refused: ${refusal}. Answer again, changing only the sentences that hold the marked occurrences.`,
        ]),
  ];
  return { system: SYSTEM, user: lines.join("\n") };
}

/** The entries of the map that apply in a project. */
export function applyingEntries(entries: readonly MapEntry[], reach: Reach): MapEntry[] {
  return entries.filter((entry) => applies(entry, reach));
}

/** One ask of the model, its answer held to the shape and to `confinedProblem`. */
async function askOnce(question: Question, model: Model, refusal?: string): Promise<Judgement> {
  const text = await model.ask(buildPrompt(question, refusal));
  if (text === null) return { kind: "no-answer" };
  const answer = readAnswer(text);
  if (answer.kind !== "rewrite") return answer;
  const problem = confinedProblem(
    question.paragraph,
    answer.paragraph,
    question.judged,
    question.kept
  );
  return problem === null ? answer : { kind: "judge-refused", reason: problem };
}

/** The judgement of one paragraph and how many asks it took: a refused first answer is asked
 *  once more, naming why it was refused. */
export interface Judged {
  readonly judgement: Judgement;
  readonly asks: 1 | 2;
}

/** One question put to the model, and once more when its first answer is refused. A second
 *  refusal is the judgement; it is no verdict, and a later run asks again. */
export async function judge(question: Question, model: Model): Promise<Judged> {
  const first = await askOnce(question, model);
  if (first.kind !== "judge-refused") return { judgement: first, asks: 1 };
  return { judgement: await askOnce(question, model, first.reason), asks: 2 };
}

/** Whether a text holds an occurrence at all, which a rewrite of a title must not. */
export function stillHoldsWord(text: string): boolean {
  return holdsWord(text);
}

/** The spans of every occurrence of a text. */
export function occurrenceSpans(text: string): Span[] {
  return matches(text).map((match) => ({ start: match.start, end: match.end }));
}
