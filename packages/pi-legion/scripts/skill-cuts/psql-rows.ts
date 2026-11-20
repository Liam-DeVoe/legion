/**
 * Reads what the session store's read command prints: psql's default aligned table, under the
 * command's own flags (`-X -q -w -v ON_ERROR_STOP=1 --single-transaction -c 'SET TRANSACTION READ
 * ONLY' -f -`), for a query of this runner. Every query of the runner returns one column whose
 * every value is one JSON value (`json_build_object(…)::text`), so each row is one line: JSON
 * escapes every newline, and a `|` stays inside a string. The table is a header line, a rule of
 * dashes, one line per row padded with spaces, and the footer `(N rows)`.
 *
 * `psqlRows` returns the rows, parsed. It refuses output that is not one such table: no rule, a
 * rule of more than one column, no footer, a row count that differs from the footer's, text after
 * the footer, or a row line that is not one JSON value, naming that line's number.
 *
 * `pullText` joins the rows of a transcript pull (pull.sql, every call's rows together) into the
 * file's text: the file row's content (offset -1) first, then each part in offset order. It refuses
 * a row of another shape, an offset given twice, and a part that does not start where the one
 * before it ends (start_offset is a byte offset), so a page left out cannot pass as a whole file.
 */
import { z } from "zod";

const RULE = /^-+$/;
const FOOTER = /^\((\d+) rows?\)$/;

/** The rows of one psql aligned table holding one JSON value per row; throws when `output` is not
 *  one, naming the line at fault (lines count from 1). */
export function psqlRows(output: string): unknown[] {
  const lines = output.split("\n");
  const rule = lines.findIndex((line) => /^-[-+]*$/.test(line));
  if (rule < 1) throw new Error("psql output: no header rule (a line of dashes under a header)");
  if (!RULE.test(lines[rule] ?? "")) {
    throw new Error(`psql output line ${rule + 1}: the table has more than one column`);
  }
  const rows: unknown[] = [];
  for (let index = rule + 1; index < lines.length; index++) {
    const line = lines[index] ?? "";
    const footer = FOOTER.exec(line);
    if (footer) {
      const count = Number(footer[1]);
      if (count !== rows.length) {
        throw new Error(
          `psql output line ${index + 1}: the footer gives ${count} rows, the table holds ${rows.length}`
        );
      }
      const after = lines.slice(index + 1).findIndex((rest) => rest.trim() !== "");
      if (after !== -1) {
        throw new Error(`psql output line ${index + 2 + after}: text after the table's footer`);
      }
      return rows;
    }
    const text = line.trim();
    if (text === "" && lines.slice(index).every((rest) => rest.trim() === "")) break;
    try {
      rows.push(JSON.parse(text));
    } catch {
      throw new Error(
        `psql output line ${index + 1}: not one JSON value: ${JSON.stringify(text.slice(0, 120))}`
      );
    }
  }
  throw new Error("psql output: no footer (N rows) after the table");
}

/** One row of a transcript pull: a piece's start offset (-1 for the file row) and its text. */
export const PullRow = z.strictObject({
  o: z.number().int().min(-1),
  c: z.string(),
});
export type PullRow = z.infer<typeof PullRow>;

/** The file's text from the rows of every call of its transcript pull. */
export function pullText(rows: readonly unknown[]): string {
  const pieces = rows
    .map((row, index) => {
      const parsed = PullRow.safeParse(row);
      if (!parsed.success) {
        throw new Error(
          `pull row ${index + 1} is not {"o": <offset>, "c": <text>}: ${parsed.error.message}`
        );
      }
      return parsed.data;
    })
    .sort((a, b) => a.o - b.o);
  let next = 0;
  let seen = -2;
  const text: string[] = [];
  for (const piece of pieces) {
    if (piece.o === seen) throw new Error(`pull rows give offset ${piece.o} twice`);
    seen = piece.o;
    if (piece.o >= 0) {
      if (piece.o !== next) {
        throw new Error(
          `pull rows: the part at offset ${piece.o} does not start where the text before it ends (${next})`
        );
      }
      next += Buffer.byteLength(piece.c, "utf8");
    }
    text.push(piece.c);
  }
  return text.join("");
}
