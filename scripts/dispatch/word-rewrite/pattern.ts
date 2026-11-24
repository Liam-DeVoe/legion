/**
 * The word this tool finds and rewrites, as the repository's own checks find it:
 * `(?i:park)(?<![Ss]park)(?<!SPARK)` (INFRA-2017's plan, "The search of every check"). It finds
 * the word in each case and inside each name (`park`, `Parked`, `PARK_CHILD`, `isParked`,
 * `releasesParkedClaims`), and leaves out "spark" in each spelling the measurement met (`spark`,
 * `Spark`, `SPARK`, `SPARK_HOME`, `Genspark`, `sparkline`). This file is one of the places the
 * word may stand in the repository: what finds the word must name it (R1, kind 4).
 */

/** Four letters in any case, not after the letters that make them "spark". The look-behinds are
 *  case-sensitive on purpose: they are the checks' own, character for character. */
const WORD_SOURCE = "[Pp][Aa][Rr][Kk](?<![Ss]park)(?<!SPARK)";

/** A fresh global pattern, so no caller shares another's `lastIndex`. */
function wordPattern(): RegExp {
  return new RegExp(WORD_SOURCE, "g");
}

/** One match of the word in a text: its place as JavaScript indexes it (UTF-16 code units) and
 *  as Dispatch's block ranges index it (UTF-8 bytes of the canonical markdown). */
export interface Match {
  readonly start: number;
  readonly end: number;
  readonly byteStart: number;
  readonly byteEnd: number;
}

/** Every match of the word in `text`, in order. The byte offsets are counted once, from the end
 *  of the previous match, so a long text costs one pass. A match is four ASCII letters, so no
 *  slice taken here splits a surrogate pair. */
export function matches(text: string): Match[] {
  const found: Match[] = [];
  const pattern = wordPattern();
  let index = 0;
  let bytes = 0;
  for (let hit = pattern.exec(text); hit !== null; hit = pattern.exec(text)) {
    bytes += Buffer.byteLength(text.slice(index, hit.index), "utf8");
    const end = hit.index + hit[0].length;
    found.push({ start: hit.index, end, byteStart: bytes, byteEnd: bytes + hit[0].length });
    index = end;
    bytes += hit[0].length;
  }
  return found;
}

/** Whether `text` holds the word anywhere. */
export function holdsWord(text: string): boolean {
  return wordPattern().test(text);
}

/** How many times `text` holds the word. */
export function countWord(text: string): number {
  return text.match(wordPattern())?.length ?? 0;
}

/** The JavaScript index of a UTF-8 byte offset into `text`. An offset inside a multi-byte
 *  character answers the index of that character. Each character is counted by its code point,
 *  as `TextEncoder` encodes it (a lone surrogate as the three bytes of U+FFFD), with nothing
 *  allocated. */
export function indexOfByte(text: string, byte: number): number {
  let bytes = 0;
  let index = 0;
  while (index < text.length) {
    if (bytes >= byte) return index;
    const point = text.codePointAt(index) ?? 0;
    bytes += point < 0x80 ? 1 : point < 0x800 ? 2 : point < 0x10000 ? 3 : 4;
    index += point > 0xffff ? 2 : 1;
  }
  return index;
}
