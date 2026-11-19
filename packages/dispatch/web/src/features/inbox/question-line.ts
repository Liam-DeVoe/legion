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
/** Where a part of the line waits while the decoration around its words is stripped: one code
 *  point of the private-use area each. An escaped punctuation mark is held at its own code
 *  above `HELD_MARK`, so an escaped `*` is never read as emphasis; a code span is held as its
 *  index above `HELD_CODE`, so nothing inside it is read as markdown at all. */
const HELD_MARK = 0xe000;
const HELD_CODE = 0xe100;
const HELD = /[\ue021-\ue07e\ue100-\uf8ff]/g;

/**
 * The line with each code span and each escaped punctuation mark held, read left to right as
 * markdown reads them: a backslash escapes a punctuation mark in prose and nothing in code, and
 * a run of backticks opens a code span only where a run of exactly as many closes it. A run
 * that nothing closes is backticks as written. Each span's code goes into `codes`, without its
 * delimiters and as markdown trims it: one space off each end, where it has one at both and is
 * not spaces alone.
 */
function hold(line: string, codes: string[]): string {
  let held = "";
  let at = 0;
  while (at < line.length) {
    const char = line.charAt(at);
    const next = line.charAt(at + 1);
    if (char === "\\" && PUNCTUATION.test(next)) {
      held += String.fromCharCode(HELD_MARK + next.charCodeAt(0));
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
        codes.push(/^ [\s\S]* $/.test(code) && code.trim() !== "" ? code.slice(1, -1) : code);
        held += String.fromCharCode(HELD_CODE + codes.length - 1);
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
    const codes: string[] = [];
    const plain = hold(line, codes)
      .replace(HEADING, "")
      .replace(LINK, "$1")
      .replace(STRONG, "$2")
      .replace(STAR_EMPHASIS, "$1")
      .replace(UNDERSCORE_EMPHASIS, "$1")
      .replace(HELD, (held) => {
        const point = held.charCodeAt(0);
        return point >= HELD_CODE
          ? (codes[point - HELD_CODE] ?? "")
          : String.fromCharCode(point - HELD_MARK);
      })
      .replace(/\s+/g, " ")
      .trim();
    if (plain !== "") {
      return plain;
    }
  }
  return "";
}
