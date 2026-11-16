/**
 * The mention rule (INFRA-1702 R4 and R8), the one place every reader of a record and the
 * document editor take it from. A mention of a party - an agent's session, a role or a person -
 * is a reference written into the text as a link whose words open with the mention sign and
 * whose address names the party: `[@Planner](dispatch://agent/…)`. Such a link reads as the sign
 * and the party's current name wherever it is shown - a rendered body, the plain-text
 * projection, the document editor's titles and a draft's chips - so a session renamed since the
 * mention was written reads as it is called now. Every other worded link keeps its author's
 * words, and an address standing on its own, or a link whose words are that address, reads as
 * the target's current name.
 *
 * Formatting decides this much, the same way for every reader (`WordsFormatting`): code
 * anywhere in the words keeps the words; emphasis, strong or strikethrough over all of the
 * link's words is a mention drawn in that formatting; formatting over some of the words makes
 * them the author's words; every check is local to the link. In the parsed document
 * `[**@x**](…)` and `**[@x](…)**` are one text - the same words under a strong mark and a link -
 * so a mark over all of a link's words cannot be read as the author formatting the words, and a
 * mention inside a bold sentence keeps its current name.
 *
 * Which address names a party is the reader's own grammar, so each caller says that much
 * (`WrittenReference.party`); everything else about the rule is here, so a reader that drew a
 * mention one way and an editor that offered it another cannot both compile.
 */

/** The sign a mention's words open with. */
export const MENTION_SIGN = "@";

/** What a reference reads as, by the one rule every record is read under (R8):
 *  - `name`: the target's current name - an address standing on its own, and a link whose words
 *    are that same address, which is what a Markdown autolink is stored as;
 *  - `mention`: the mention sign and the party's current name - a party's link whose words open
 *    with that sign (R4);
 *  - `words`: the words its author wrote, for every other link with words of its own. */
export type ReferenceReading = "mention" | "name" | "words";

/** How a link's words are formatted, which is all of their formatting a reading depends on:
 *  - `plain`: none of them is code, and whatever emphasis, strong or strikethrough there is
 *    covers all of them alike, which is formatting around the link rather than of its words;
 *  - `code`: all of them are code;
 *  - `mixed`: some of them are formatted as the rest are not. */
export type WordsFormatting = "plain" | "code" | "mixed";

/** One stretch of a link's words with the formatting over it, as a reader of a parsed text
 *  meets it: whether it is code, and `formatting`, the emphasis, strong and strikethrough over
 *  it in any spelling that is the same for two stretches formatted alike. A mark that is no
 *  formatting of the author's - a search hit's highlight, a comment's anchor - is left out. */
export interface WordsPiece {
  readonly code: boolean;
  readonly formatting: string;
}

/** The marks of Proof's schema that format words, beside its code mark
 *  (`packages/envoy/internal/dispatch/pmdoc/schema/blocks.json`). */
const WORD_FORMATTING_MARKS: readonly string[] = ["emphasis", "strong", "strike_through"];

/** The stretch of words a text node under `marks` is, for a reader of a document in Proof's
 *  schema - the document editor and the dashboard's plain-text projection - so the two tell
 *  formatting from every other mark by one list. */
export function wordsPieceUnder(
  marks: readonly {
    readonly type: { readonly name: string; readonly spec: { readonly code?: boolean } };
  }[]
): WordsPiece {
  return {
    code: marks.some((mark) => mark.type.spec.code === true),
    formatting: marks
      .map((mark) => mark.type.name)
      .filter((name) => WORD_FORMATTING_MARKS.includes(name))
      .sort()
      .join(" "),
  };
}

/** How the words `pieces` make up are formatted (`WordsFormatting`): mixed where two of them
 *  differ, and otherwise whatever every one of them is. */
export function wordsFormatting(pieces: readonly WordsPiece[]): WordsFormatting {
  const [first, ...rest] = pieces;
  if (first === undefined) {
    return "plain";
  }
  if (rest.some((piece) => piece.code !== first.code || piece.formatting !== first.formatting)) {
    return "mixed";
  }
  return first.code ? "code" : "plain";
}

/** CommonMark's ASCII punctuation, which a backslash before it escapes; its whitespace; and its
 *  punctuation, which with whitespace decides whether a run of `*`, `_` or `~` can open or
 *  close formatting. */
const ASCII_PUNCTUATION = /[!-/:-@[-`{-~]/;
const WHITESPACE = /\s/u;
const PUNCTUATION = /[\p{P}\p{S}]/u;

/** A code span of written words: where its backticks start and end, and its content between. */
interface CodeSpan {
  readonly start: number;
  readonly contentStart: number;
  readonly contentEnd: number;
  readonly end: number;
}

/** The code spans of `written`, as CommonMark reads them: a run of backticks opens one and the
 *  next run of as many closes it, a backslash takes the backtick after it as a literal
 *  character, and a run nothing closes is literal. One space at each end of the content is the
 *  span's own where both stand there. */
function codeSpans(written: string): CodeSpan[] {
  const spans: CodeSpan[] = [];
  const runEnd = (from: number): number => {
    let end = from;
    while (written.charAt(end) === "`") end += 1;
    return end;
  };
  for (let at = 0; at < written.length; ) {
    const character = written.charAt(at);
    if (character !== "`") {
      at += character === "\\" ? 2 : 1;
      continue;
    }
    const opened = runEnd(at);
    let close = -1;
    for (let scan = opened; scan < written.length && close === -1; ) {
      if (written.charAt(scan) !== "`") {
        scan += 1;
        continue;
      }
      const closed = runEnd(scan);
      if (closed - scan === opened - at) close = scan;
      scan = closed;
    }
    if (close === -1) {
      at = opened;
      continue;
    }
    const content = written.slice(opened, close);
    const padded = content.startsWith(" ") && content.endsWith(" ") && content.trim() !== "";
    spans.push({
      contentEnd: padded ? close - 1 : close,
      contentStart: padded ? opened + 1 : opened,
      end: close + (opened - at),
      start: at,
    });
    at = close + (opened - at);
  }
  return spans;
}

/** A run of `*`, `_` or `~` in written words, and whether it can open or close formatting. */
interface DelimiterRun {
  readonly character: string;
  readonly start: number;
  readonly end: number;
  readonly opens: boolean;
  readonly closes: boolean;
}

/** The runs of `*`, `_` and `~` in `written` outside its code spans, each read by CommonMark's
 *  flanking rule: a run opens where what follows it is no whitespace, and no punctuation unless
 *  whitespace or punctuation stands before it; it closes by the mirror of that; and a `_` does
 *  neither inside a word. The start and the end of the words count as whitespace. */
function delimiterRuns(written: string, spans: readonly CodeSpan[]): DelimiterRun[] {
  const runs: DelimiterRun[] = [];
  let span = 0;
  for (let at = 0; at < written.length; ) {
    while ((spans[span]?.end ?? Number.POSITIVE_INFINITY) <= at) span += 1;
    const code = spans[span];
    if (code !== undefined && code.start <= at) {
      at = code.end;
      continue;
    }
    const character = written.charAt(at);
    if (!"*_~".includes(character)) {
      at += character === "\\" ? 2 : 1;
      continue;
    }
    let end = at;
    while (written.charAt(end) === character) end += 1;
    const before = at === 0 ? " " : written.charAt(at - 1);
    const after = end === written.length ? " " : written.charAt(end);
    const leftFlanking =
      !WHITESPACE.test(after) &&
      (!PUNCTUATION.test(after) || WHITESPACE.test(before) || PUNCTUATION.test(before));
    const rightFlanking =
      !WHITESPACE.test(before) &&
      (!PUNCTUATION.test(before) || WHITESPACE.test(after) || PUNCTUATION.test(after));
    const inWord = character === "_";
    runs.push({
      character,
      closes: rightFlanking && (!inWord || !leftFlanking || PUNCTUATION.test(after)),
      end,
      opens: leftFlanking && (!inWord || !rightFlanking || PUNCTUATION.test(before)),
      start: at,
    });
    at = end;
  }
  return runs;
}

/** A stretch of written words under formatting: `[from, to)`, between the runs that open and
 *  close it, and the formatting those runs write, by the names a parsed document gives it. */
interface FormattedStretch {
  readonly from: number;
  readonly to: number;
  readonly marks: readonly string[];
}

/** The formatting a run of `length` of `character` writes: a `~` strikethrough, one `*` or `_`
 *  emphasis, two strong, and three both. */
function delimiterMarks(character: string, length: number): readonly string[] {
  if (character === "~") return ["strike_through"];
  return length === 1 ? ["emphasis"] : length === 2 ? ["strong"] : ["emphasis", "strong"];
}

/**
 * A link's words as a reader sees them, and how they are formatted (`WordsFormatting`), from
 * the words as Markdown writes them - for a reader of a text nothing has parsed, a draft in a
 * writing box. Each backslash escape is the character it escapes (`\@Planner` reads `@Planner`),
 * a code span is its content, and a run of `*`, `_` or `~` that opens formatting another closes
 * is no word of its own. So `**@Planner**` reads `@Planner`, plain, as the same words under a
 * strong mark read in a parsed document; `@Planner **now**` reads `@Planner now`, mixed; and
 * `` `@Planner` `` reads `@Planner`, code. A rendered body's words come from the parser, which
 * read the same syntax, so the two read one link one way.
 */
export function readWrittenWords(written: string): { words: string; formatting: WordsFormatting } {
  const spans = codeSpans(written);
  const runs = delimiterRuns(written, spans);
  // Each closing run takes the nearest run before it that opens with the same character, and
  // for a strikethrough the same length, as GFM pairs one.
  const openers: DelimiterRun[] = [];
  const paired = new Set<DelimiterRun>();
  const stretches: FormattedStretch[] = [];
  for (const run of runs) {
    const length = run.end - run.start;
    let opener: DelimiterRun | undefined;
    for (let index = openers.length - 1; run.closes && index >= 0; index -= 1) {
      const candidate = openers[index];
      if (
        candidate?.character === run.character &&
        (run.character !== "~" || candidate.end - candidate.start === length)
      ) {
        opener = candidate;
        openers.length = index;
        break;
      }
    }
    if (opener === undefined) {
      if (run.opens) openers.push(run);
      continue;
    }
    paired.add(opener).add(run);
    stretches.push({
      from: opener.end,
      marks: delimiterMarks(run.character, Math.min(length, opener.end - opener.start)),
      to: run.start,
    });
  }

  let words = "";
  const pieces: WordsPiece[] = [];
  const see = (character: string, at: number, code: boolean): void => {
    words += character;
    pieces.push({
      code,
      formatting: [
        ...new Set(
          stretches
            .filter((stretch) => stretch.from <= at && at < stretch.to)
            .flatMap((stretch) => stretch.marks)
        ),
      ]
        .sort()
        .join(" "),
    });
  };
  let span = 0;
  let run = 0;
  for (let at = 0; at < written.length; ) {
    while ((spans[span]?.end ?? Number.POSITIVE_INFINITY) <= at) span += 1;
    while ((runs[run]?.end ?? Number.POSITIVE_INFINITY) <= at) run += 1;
    const code = spans[span];
    const delimiter = runs[run];
    if (code !== undefined && code.start <= at) {
      for (let inside = code.contentStart; inside < code.contentEnd; inside += 1) {
        see(written.charAt(inside), inside, true);
      }
      at = code.end;
    } else if (delimiter !== undefined && delimiter.start <= at && paired.has(delimiter)) {
      at = delimiter.end;
    } else if (written.charAt(at) === "\\" && ASCII_PUNCTUATION.test(written.charAt(at + 1))) {
      see(written.charAt(at + 1), at, false);
      at += 2;
    } else {
      see(written.charAt(at), at, false);
      at += 1;
    }
  }
  return { formatting: wordsFormatting(pieces), words };
}

/** A reference as its text writes it, which is everything its reading depends on. */
export interface WrittenReference {
  /** The address as its author wrote it: a link's destination, or the reference standing on its
   *  own. */
  readonly address: string;
  /** Whether the address names a party, by the caller's own reading of it. */
  readonly party: boolean;
  /** The words of the link that holds the address, as a reader sees them (`readWrittenWords`
   *  for a text nothing has parsed), or undefined for an address standing on its own. */
  readonly words?: string;
  /** How those words are formatted (`WordsFormatting`); plain where it is left out. */
  readonly wordsFormatting?: WordsFormatting;
}

/** Whether a link's words are its own address: the form a document stores an autolink in, and
 *  the bare `www.` address a Markdown autolink gives an `http://` destination. */
export function wordsAreAddress(words: string, address: string): boolean {
  return address === words || address === `http://${words}`;
}

/** Whether a link's words make it a mention, where its address names a party: they open with the
 *  mention sign, and they are plain - no code among them, which is the code's characters and
 *  never a sign its writer wrote, and no formatting over a part of them. */
export function isMentionWords(words: string, formatting: WordsFormatting = "plain"): boolean {
  return formatting === "plain" && words.startsWith(MENTION_SIGN);
}

/** What `reference` reads as (`ReferenceReading`). Words formatted in part are their author's
 *  words whatever they spell; words that are all code still name their target where they are
 *  its address, the form a document stores an autolink written as code in. */
export function referenceReading({
  address,
  party,
  words,
  wordsFormatting: formatting = "plain",
}: WrittenReference): ReferenceReading {
  if (words === undefined) {
    return "name";
  }
  if (formatting === "mixed") {
    return "words";
  }
  if (wordsAreAddress(words, address)) {
    return "name";
  }
  return party && isMentionWords(words, formatting) ? "mention" : "words";
}

/** The words a mention of a party named `name` draws: the mention sign, then the name. */
export function mentionName(name: string): string {
  return `${MENTION_SIGN}${name}`;
}

/** What a party's target answered, as far as a mention of it reads: its current name, and
 *  whether Dispatch answered that the party exists with no name of its own - a session it holds
 *  a record of that the registry does not title. */
export interface MentionedParty {
  readonly title: string | undefined;
  readonly known: boolean;
}

/** What a mention of `party` draws in place of its words, in every place a mention is shown:
 *  the mention sign and the party's current name; for a party that exists with no name, the
 *  sign and `shortForm`, its address written short, so the words its writer chose
 *  (`[@Alice](…)`) never pass for a name Dispatch holds; and undefined while the party has not
 *  answered, where the mention keeps its writer's words. */
export function mentionDrawn(party: MentionedParty, shortForm: string): string | undefined {
  if (party.title !== undefined) {
    return mentionName(party.title);
  }
  return party.known ? mentionName(shortForm) : undefined;
}
