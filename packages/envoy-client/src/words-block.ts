/**
 * A words block: the words of another author, drawn after a text's fields in a block of their
 * own. The block names the author and the time where the fields of the text do not, nothing else
 * shares it, and nothing else in the text can open or close an element of its tag. The Legion
 * daemon draws the blocks of its texts by the same rule (`Render`, and `InFront` for notes put in
 * front of a text, `packages/daemon/internal/texts`), so one reader reads the blocks of a card and
 * of a daemon text:
 *
 *     <answer by="user:sami" at="2026-11-14T22:44:10Z">
 *     Use a structured message here, not prose.
 *     </answer>
 *
 * The tag of a block is the first of `name`, `name-1`, `name-2` and on, where `name` is the name
 * of the field that holds the words, such that neither the text before the blocks nor the words
 * of any block of the text hold its opening tag `<tag` or its closing tag `</tag`, and no earlier
 * block of the text took it. The text before the blocks is a card's fields, which hold other
 * authors' words too: an ask's question, an issue's title. So nothing in the text opens or closes
 * the tag of a block but that block's own lines. The test is on the plain text, so text that
 * holds `<answers` or `<answer-12` also moves the tag of `answer` on, past `answer-1` and
 * `answer-12` too. The opening tag carries the attributes `by`, `at`, `field`, `rest` and `more`,
 * in that order, each only where it is set. Each value escapes `&`, `<`, `>`, `"` and each control
 * character below U+0020 as an XML character reference, so no value ends its attribute or its
 * tag. A new line, the words exactly as given, a new line and the closing tag follow the opening
 * tag, and one new line joins a block to the next.
 */

/** The words of one block, and what its opening tag says of them. */
export interface Words {
  /** The words exactly as their event carried them. */
  readonly text: string;
  /** The author, where the fields of the text do not name the author. */
  readonly by?: string | undefined;
  /** The time of the act, as its source wrote it, where the fields of the text do not give it. */
  readonly at?: string | undefined;
  /** The path of the words in their payload, such as `kept[1].answer`, where they are a field
   *  inside a list or an object of the payload. */
  readonly field?: string | undefined;
  /** The count of UTF-16 units that a bound cut off the words. */
  readonly rest?: number | undefined;
  /** The event marked its text as cut, so more follows and the count of the rest is not known. */
  readonly more?: boolean | undefined;
}

/** One block of a text: the name of the field that holds the words, and the words. */
export interface NamedWords {
  readonly name: string;
  readonly words: Words;
}

/** One block as a reader found it. */
export interface ReadBlock extends Words {
  /** The tag as written: the name, or the name with the suffix the words made it take. */
  readonly tag: string;
  /** The name of the field that holds the words. */
  readonly name: string;
}

/** A text split at its first block. */
export interface SplitText {
  /** All of the text before its first block, less the new line that joins it to the block. For
   *  a card with fields and blocks this is TOON that TOON's `decode` reads. The whole text where
   *  it holds no block: a card with a diff after its fields, and a card that is one sentence, are
   *  returned whole. */
  readonly head: string;
  /** The blocks that end the text, in their order. */
  readonly blocks: readonly ReadBlock[];
}

/** The blocks of one text, in their order, each joined to the next by one new line, that follow
 *  `before`: the whole of the text ahead of them, such as a card's fields, which the caller joins
 *  to the first block with one new line. Each block takes the tag the rule gives it against
 *  `before` and the words of every block (`blockTags`). */
export function wordsBlocks(before: string, blocks: readonly NamedWords[]): string {
  const tags = blockTags(
    [before],
    blocks.map(({ name, words }) => ({ name, text: words.text }))
  );
  return blocks
    .map(({ words }, index) => {
      let attributes = "";
      if (words.by !== undefined && words.by !== "") attributes += attribute("by", words.by);
      if (words.at !== undefined && words.at !== "") attributes += attribute("at", words.at);
      if (words.field !== undefined && words.field !== "") {
        attributes += attribute("field", words.field);
      }
      if (words.rest !== undefined && words.rest > 0) {
        attributes += attribute("rest", String(words.rest));
      }
      if (words.more === true) attributes += attribute("more", "true");
      return drawBlock(tags[index] ?? "", attributes, words.text);
    })
    .join("\n");
}

/**
 * `parts` and then `text`, each joined to the next by a blank line: what a host puts in front of
 * a card it hands on (a line that says where messages came from, a label, the messages before
 * it), and the card. A part can hold another author's words, so the blocks of `text`
 * (`drawnBlocks`) are drawn again, each with its attributes as written and its words as they
 * were, under the tag the rule gives it against the whole of what is sent: the parts, the text of
 * `text` before and after its blocks, and the words of every block. Nothing in front of a block
 * then opens or closes its tag, as nothing does in the card alone (`wordsBlocks`). Where neither
 * the parts nor the text after the blocks hold a tag the blocks could take, each block keeps its
 * tag and the result is the parts and `text` joined as they are; so is a text with no drawn
 * block. The Legion daemon puts notes in front of a task by the same rule (`InFront`,
 * `packages/daemon/internal/texts/words.go`).
 */
export function inFront(parts: readonly string[], text: string): string {
  if (parts.length === 0) return text;
  const front = `${parts.join("\n\n")}\n\n`;
  const found = drawnBlocks(text);
  const first = found[0];
  const last = found[found.length - 1];
  if (first === undefined || last === undefined) return front + text;
  const head = text.slice(0, first.start);
  const tail = text.slice(last.end);
  const tags = blockTags([...parts, head, tail], found);
  const blocks = found.map(({ attributes, text: words }, index) =>
    drawBlock(tags[index] ?? "", attributes, words)
  );
  return `${front}${head}${blocks.join("\n")}${tail}`;
}

/** One block of a text as `drawnBlocks` found it: where its opening line starts and where its
 *  closing tag ends, the name of the field that holds the words (its tag without the `-n` a step
 *  past held tags added), the attributes of its opening tag as written, and its words. */
interface DrawnBlock {
  readonly start: number;
  readonly end: number;
  readonly name: string;
  readonly attributes: string;
  readonly text: string;
}

/**
 * The blocks of `text`, a card `wordsBlocks` drew, in their order. No line of a card ahead of its
 * blocks starts with `<`, since its fields are TOON lines, so its first such line opens its first
 * block. A block closes on the first line after its opening that is its closing tag, since no
 * words of a block hold that tag (`blockTags`), and the next block opens on the line after. The
 * run ends at the first line that opens no block. It is empty where the first line that starts
 * with `<` opens none, or opens a block that never closes, as in a text no card drew, which
 * `inFront` then joins as it is. One pass over `text`.
 */
function drawnBlocks(text: string): DrawnBlock[] {
  let at = 0;
  if (!text.startsWith("<")) {
    const line = text.indexOf("\n<");
    if (line < 0) return [];
    at = line + 1;
  }
  const blocks: DrawnBlock[] = [];
  while (text[at] === "<") {
    const opened = text.indexOf("\n", at);
    if (opened < 0) break;
    const opening = OPENING.exec(text.slice(at, opened));
    if (opening === null) break;
    const name = opening[1] ?? "";
    const closing = `\n</${opening[2] === undefined ? name : `${name}-${opening[2]}`}>`;
    const closed = text.indexOf(closing, opened + 1);
    if (closed < 0) break;
    const end = closed + closing.length;
    if (end < text.length && text[end] !== "\n") break;
    blocks.push({
      start: at,
      end,
      name,
      attributes: opening[3] ?? "",
      text: text.slice(opened + 1, closed),
    });
    at = end + 1;
  }
  return blocks;
}

/** The block of `text` under `tag`: the opening tag with `attributes` as written, a new line, the
 *  words, a new line and the closing tag. */
function drawBlock(tag: string, attributes: string, text: string): string {
  return `<${tag}${attributes}>\n${text}\n</${tag}>`;
}

/** The tag of each of `blocks`, the blocks of one text in their order, where `before` is the rest
 *  of the text: the first of `name`, `name-1`, `name-2` and on whose `<tag` and `</tag` neither a
 *  part of `before` nor the words of any block hold, and that no earlier block took. A part is
 *  scanned on its own, which finds what the joined text holds, since a tag holds no new line. */
function blockTags(
  before: readonly string[],
  blocks: readonly { readonly name: string; readonly text: string }[]
): string[] {
  const texts = [...before, ...blocks.map(({ text }) => text)];
  // No tag can take a suffix above this: each suffix the texts hold takes a character of them,
  // and each block takes one suffix.
  const cap = texts.reduce((length, text) => length + text.length, blocks.length) + 1;
  // For each name, the suffixes held or taken (0 is the bare name), and the least that may be
  // free: every suffix below it is held or taken.
  const used = new Map<string, { readonly suffixes: Set<number>; next: number }>();
  return blocks.map(({ name }) => {
    let named = used.get(name);
    if (named === undefined) {
      named = { suffixes: heldSuffixes(name, texts, cap), next: 0 };
      used.set(name, named);
    }
    while (named.suffixes.has(named.next)) named.next++;
    const suffix = named.next;
    named.suffixes.add(suffix);
    return suffix === 0 ? name : `${name}-${suffix}`;
  });
}

/** The suffixes of `name` whose tags `texts` hold, up to `cap`: 0 where a text holds `<name` or
 *  `</name`, and n where it holds `<name-n` or `</name-n`, so `<answer-12` holds 0, 1 and 12. One
 *  scan of each text for each of the two marks, so the time is linear in the texts' length. */
function heldSuffixes(name: string, texts: readonly string[], cap: number): Set<number> {
  const held = new Set<number>();
  for (const text of texts) {
    for (const mark of [`<${name}`, `</${name}`]) {
      for (let at = text.indexOf(mark); at !== -1; at = text.indexOf(mark, at + mark.length)) {
        held.add(0);
        const dash = at + mark.length;
        if (text[dash] !== "-") continue;
        let suffix = 0;
        for (let index = dash + 1; index < text.length; index++) {
          const digit = text.charCodeAt(index) - 48;
          // A tag's suffix has no leading zero, so `-0` holds none.
          if (digit < 0 || digit > 9 || (suffix === 0 && digit === 0)) break;
          suffix = suffix * 10 + digit;
          if (suffix > cap) break;
          held.add(suffix);
        }
      }
    }
  }
  return held;
}

/** One attribute of an opening tag, with its value escaped. */
function attribute(key: string, value: string): string {
  let escaped = "";
  for (let index = 0; index < value.length; index++) {
    const unit = value.charCodeAt(index);
    switch (value[index]) {
      case "&":
        escaped += "&amp;";
        break;
      case "<":
        escaped += "&lt;";
        break;
      case ">":
        escaped += "&gt;";
        break;
      case '"':
        escaped += "&quot;";
        break;
      default:
        escaped += unit < 0x20 ? `&#${unit};` : value[index];
    }
  }
  return ` ${key}="${escaped}"`;
}

const OPENING = /^<([A-Za-z_][A-Za-z0-9_]*)(?:-([1-9][0-9]*))?((?: [a-z]+="[^"<>]*")*)>$/;
const ATTRIBUTE = / ([a-z]+)="([^"<>]*)"/g;
const ATTRIBUTE_ORDER = ["by", "at", "field", "rest", "more"] as const;
const REFERENCE = /&(?:(amp|lt|gt|quot)|#([0-9]+));/y;
const NAMED_REFERENCES: Readonly<Record<string, string>> = {
  amp: "&",
  lt: "<",
  gt: ">",
  quot: '"',
};

/**
 * `text` split into the part before its first block and the blocks that end it. The first block
 * opens at the first line that is an opening tag. From there the text holds blocks alone, each
 * joined to the next by one new line, to its end. A block closes at the first closing tag of its
 * tag after its opening, since no words of the text hold one. Each block's tag is the tag the
 * rule gives it against the text before the blocks and the words of every block (`blockTags`),
 * and its attributes are the rule's, in the rule's order. A text that breaks one of these throws
 * an error that names the break.
 */
export function splitWordsBlocks(text: string): SplitText {
  let start = -1;
  for (let line = 0; ; ) {
    const end = lineEnd(text, line);
    if (OPENING.test(text.slice(line, end))) {
      start = line;
      break;
    }
    if (end === text.length) break;
    line = end + 1;
  }
  if (start < 0) return { head: text, blocks: [] };

  const blocks: ReadBlock[] = [];
  for (let line = start; ; ) {
    const end = lineEnd(text, line);
    const opening = OPENING.exec(text.slice(line, end));
    if (opening === null) {
      throw new Error(`text after a block is no block: ${JSON.stringify(text.slice(line, end))}`);
    }
    const name = opening[1] ?? "";
    const tag = opening[2] === undefined ? name : `${name}-${opening[2]}`;
    if (end === text.length) throw new Error(`block <${tag}> ends at its opening tag`);
    const words = end + 1;
    const close = text.indexOf(`</${tag}`, words);
    if (close <= words || text[close - 1] !== "\n" || !text.startsWith(`</${tag}>`, close)) {
      throw new Error(`block <${tag}> has no closing tag on a line of its own`);
    }
    const after = close + tag.length + 3;
    if (after < text.length && text[after] !== "\n") {
      throw new Error(`block <${tag}> has text after its closing tag on the same line`);
    }
    const said = text.slice(words, close - 1);
    blocks.push({ tag, name, text: said, ...attributes(tag, opening[3] ?? "") });
    if (after >= text.length) break;
    line = after + 1;
  }
  const head = start === 0 ? "" : text.slice(0, start - 1);
  const tags = blockTags([head], blocks);
  for (const [index, { tag }] of blocks.entries()) {
    if (tags[index] !== tag) {
      throw new Error(`block <${tag}> has a tag the rule does not give it: <${tags[index]}>`);
    }
  }
  return { head, blocks };
}

/** The end of the line that starts at `start`: the index of its new line, or the text's end. */
function lineEnd(text: string, start: number): number {
  const end = text.indexOf("\n", start);
  return end < 0 ? text.length : end;
}

/** The attributes of the opening tag of `tag`, read from `written`. */
function attributes(tag: string, written: string): Omit<Words, "text"> {
  const read: { by?: string; at?: string; field?: string; rest?: number; more?: boolean } = {};
  let last = -1;
  for (const [, key = "", raw = ""] of written.matchAll(ATTRIBUTE)) {
    const place = ATTRIBUTE_ORDER.indexOf(key as (typeof ATTRIBUTE_ORDER)[number]);
    if (place < 0) throw new Error(`block <${tag}> has an attribute the rule has not: ${key}`);
    if (place <= last) throw new Error(`block <${tag}> has its attributes out of order`);
    last = place;
    const value = unescaped(tag, raw);
    switch (key) {
      case "rest":
        if (!/^[1-9][0-9]*$/.test(value)) throw new Error(`block <${tag}> has rest="${value}"`);
        read.rest = Number(value);
        break;
      case "more":
        if (value !== "true") throw new Error(`block <${tag}> has more="${value}"`);
        read.more = true;
        break;
      case "by":
      case "at":
      case "field":
        if (value === "") throw new Error(`block <${tag}> has an empty ${key}`);
        read[key] = value;
        break;
    }
  }
  return read;
}

/** An attribute's value with its character references read back. */
function unescaped(tag: string, raw: string): string {
  let value = "";
  for (let index = 0; index < raw.length; ) {
    const char = raw[index] ?? "";
    if (char.charCodeAt(0) < 0x20) {
      throw new Error(`block <${tag}> has a control character in an attribute`);
    }
    if (char !== "&") {
      value += char;
      index++;
      continue;
    }
    REFERENCE.lastIndex = index;
    const reference = REFERENCE.exec(raw);
    if (reference === null) throw new Error(`block <${tag}> has an & that starts no reference`);
    const named = reference[1];
    value +=
      named === undefined ? String.fromCharCode(Number(reference[2])) : NAMED_REFERENCES[named];
    index += reference[0].length;
  }
  return value;
}
