/**
 * The two rules of the priorities seed (`priorities-seed.ts`) that its check
 * (`priorities-seed-check.sh`) reads a row by as well: the id a row's title gives (`priorityId`)
 * and the issue keys the row names (`rowKeys`). The check loads this module through `bun --eval`,
 * so neither rule is written a second time. It imports `dispatch-api-limits.ts` alone, which
 * imports nothing, so that load reads these two files and no other.
 */
import { PRIORITY_ID_MAX } from "../packages/contracts/src/dispatch-api-limits";

/** Every issue key written in a row, a key inside a `dispatch://` reference included. */
const KEY_PATTERN = /\b[A-Z][A-Z0-9]*-[0-9]+\b/g;

/** One row of the table, each cell as it is written. */
export interface TableRow {
  /** The `#` cell: the row number, which is the seed's idempotency key. */
  readonly number: string;
  readonly title: string;
  readonly owner: string;
  readonly where: string;
}

/**
 * The id the seed names for a row's priority: the id `POST /api/v1/priorities` makes from the
 * same title when a body names none (`priorityIDFromTitle` in `priorities.go`), in its two steps.
 *
 * First the slug, as `artifactSlug` writes one: the title lower-cased by Go's simple case
 * mapping, each run of characters other than `a`-`z` and `0`-`9` one hyphen, and no hyphen at
 * either end. A title holding no ASCII letter or digit has no slug, and the empty string is
 * answered for it. The guard reads the raw title, so a title whose only letter is U+212A, which
 * lower-cases to `k`, has no slug, as in Go. Go maps U+0130 to `i`, where JavaScript's
 * `toLowerCase` maps it to `i` and U+0307, which the slug would write as `i-`; so U+0130 is
 * mapped to `i` first, and every other code point is lower-cased on its own. A literal `i`
 * followed by U+0307 stays two code points, and gives `i-` here as it does in Go.
 *
 * Then the cut: a slug of at most `PRIORITY_ID_MAX` characters is the id whole, a longer one is
 * cut to its longest start of at most that many characters that ends at the end of a word,
 * which is where the next character of the slug is a hyphen, and when the first word is longer
 * than the bound, the id is its first `PRIORITY_ID_MAX` characters.
 * `packages/contracts/fixtures/priority-ids.json` holds the cases.
 */
export function priorityId(title: string): string {
  if (!/[A-Za-z0-9]/.test(title)) return "";
  let lowered = "";
  for (const point of title) lowered += point === "\u0130" ? "i" : point.toLowerCase();
  const slug = lowered.replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
  if (slug.length <= PRIORITY_ID_MAX) return slug;
  // slug[PRIORITY_ID_MAX] is the character after the longest start that fits, so the last
  // hyphen at it or before it ends the last word within the bound. The slug never begins with
  // one, so a hyphen found at 0 is none.
  const end = slug.lastIndexOf("-", PRIORITY_ID_MAX);
  return end > 0 ? slug.slice(0, end) : slug.slice(0, PRIORITY_ID_MAX);
}

/** Every issue key the row names, each once, in the order they are written. */
export function rowKeys(row: TableRow): string[] {
  const text = [row.number, row.title, row.owner, row.where].join("\n");
  return [...new Set(text.match(KEY_PATTERN) ?? [])];
}
