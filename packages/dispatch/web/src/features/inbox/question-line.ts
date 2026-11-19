/** A link or an image as its own words: `[text](target)` is its text, `![alt](src)` its alt. */
const LINK = /!?\[([^\]]*)\]\([^)]*\)/g;
/** A leading heading marker, which markdown allows up to three spaces of indent before. */
const HEADING = /^ {0,3}#{1,6}\s+/;
/** `**bold**` and `__bold__`, which stand wherever they are written. */
const STRONG = /(\*\*|__)(?=\S)([\s\S]*?\S)\1/g;
/** `*emphasis*`, which stands wherever it is written. */
const STAR_EMPHASIS = /\*(?=\S)([^*]*?\S)\*/g;
/** `_emphasis_`, which markdown reads only between words: `snake_case_name` is one word. */
const UNDERSCORE_EMPHASIS = /(?<![\w_])_(?=\S)([^_]*?\S)_(?!\w)/g;
/** The ASCII punctuation a backslash escapes in markdown prose: `1\.` is `1.`, `\*` is `*`. */
const PUNCTUATION = /[!-/:-@[-`{-~]/;
/** The code points a held part of the line stands as while the decoration around its words is
 *  stripped: the Basic Multilingual Plane's private-use area, one code point a part, which no
 *  markdown rule below reads as a marker. A line may hold such characters itself (a font's icon,
 *  say), so each part takes a code point the line does not hold. */
const RESERVED_FIRST = 0xe000;
const RESERVED_LAST = 0xf8ff;
const RESERVED = /[\ue000-\uf8ff]/g;

/**
 * The line with each code span and each escaped punctuation mark held, read left to right as
 * markdown reads them: a backslash escapes a punctuation mark in prose and nothing in code, and
 * a run of backticks opens a code span only where a run of exactly as many closes it. A run
 * that nothing closes is backticks as written. A span is held as its code, without its
 * delimiters and as markdown trims it: one space off each end, where it has one at both and is
 * not spaces alone; an escape is held as the mark alone. `tokens` gets what each code point
 * this call made stands for, and only those: a private-use character the line held already is
 * left out of it, and so is given back as it is. A line holding every private-use code point
 * leaves the rest of its parts as written.
 */
function hold(line: string, tokens: Map<string, string>): string {
  const taken = new Set(line.match(RESERVED));
  let free = RESERVED_FIRST;
  const token = (text: string, written: string): string => {
    while (free <= RESERVED_LAST && taken.has(String.fromCharCode(free))) free += 1;
    if (free > RESERVED_LAST) return written;
    const char = String.fromCharCode(free);
    free += 1;
    tokens.set(char, text);
    return char;
  };
  let held = "";
  let at = 0;
  while (at < line.length) {
    const char = line.charAt(at);
    const next = line.charAt(at + 1);
    if (char === "\\" && PUNCTUATION.test(next)) {
      held += token(next, char + next);
      at += 2;
    } else if (char === "`") {
      let run = 1;
      while (line.charAt(at + run) === "`") run += 1;
      const closing = new RegExp(`(?<!\`)\`{${run}}(?!\`)`, "g");
      closing.lastIndex = at + run;
      const end = closing.exec(line);
      if (end === null) {
        held += line.slice(at, at + run);
      } else {
        const code = line.slice(at + run, end.index);
        held += token(
          /^ [\s\S]* $/.test(code) && code.trim() !== "" ? code.slice(1, -1) : code,
          line.slice(at, end.index + run)
        );
        at = end.index;
      }
      at += run;
    } else {
      held += char;
      at += 1;
    }
  }
  return held;
}

/**
 * A question's first line of plain text: what a short row in the Inbox's list column shows of it.
 *
 * The row has one line to say what the question is, and a question is markdown - a heading, a
 * sentence with a bold word, a link to the document it is about. Rendering it would need the
 * markdown engine for every row of the list and would draw block-level decoration into a cell
 * one line tall, so the row takes the first line that holds anything and strips the decoration
 * markdown writes around its words: a heading marker, emphasis, a link's target (the text stays,
 * the target goes), inline code's backticks, and the backslash that escapes a punctuation mark
 * (`1\. Verify` reads `1. Verify`). The code inside backticks is kept as written, backslashes
 * and stars included. Nothing is escaped or re-encoded - the result is the question's own words,
 * as text, with its spacing collapsed to one line.
 */
export function questionLine(question: string): string {
  for (const line of question.split("\n")) {
    const tokens = new Map<string, string>();
    const plain = hold(line, tokens)
      .replace(HEADING, "")
      .replace(LINK, "$1")
      .replace(STRONG, "$2")
      .replace(STAR_EMPHASIS, "$1")
      .replace(UNDERSCORE_EMPHASIS, "$1")
      .replace(RESERVED, (held) => tokens.get(held) ?? held)
      .replace(/\s+/g, " ")
      .trim();
    if (plain !== "") {
      return plain;
    }
  }
  return "";
}
