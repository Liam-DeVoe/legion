/**
 * A words block: the words of another author, drawn after a text's fields in a block of their
 * own. The block names the author and the time where the fields of the text do not, nothing else
 * shares it, and its words cannot close it. The Legion daemon draws the blocks of its texts by the
 * same rule (`Block`, `packages/daemon/internal/texts/words.go`), so one reader reads the blocks
 * of a card and of a daemon text:
 *
 *     <answer by="user:sami" at="2026-11-14T22:44:10Z">
 *     Use a structured message here, not prose.
 *     </answer>
 *
 * The tag is the name of the field that holds the words. Where the words hold `</name`, the tag is
 * the first of `name-1`, `name-2` and on whose closing tag the words do not hold. The opening tag
 * carries the attributes `by`, `at`, `field`, `rest` and `more`, in that order, each only where it
 * is set. Each value escapes `&`, `<`, `>`, `"` and each control character below U+0020 as an XML
 * character reference, so no value ends its attribute or its tag. A new line, the words exactly as
 * given, a new line and the closing tag follow the opening tag.
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

/** The block of `words`, which the field `name` holds. */
export function wordsBlock(name: string, words: Words): string {
  const tag = blockTag(name, words.text);
  let open = `<${tag}`;
  if (words.by !== undefined && words.by !== "") open += attribute("by", words.by);
  if (words.at !== undefined && words.at !== "") open += attribute("at", words.at);
  if (words.field !== undefined && words.field !== "") open += attribute("field", words.field);
  if (words.rest !== undefined && words.rest > 0) open += attribute("rest", String(words.rest));
  if (words.more === true) open += attribute("more", "true");
  return `${open}>\n${words.text}\n</${tag}>`;
}

/** The first of `name`, `name-1`, `name-2` and on whose closing tag `text` does not hold. */
function blockTag(name: string, text: string): string {
  if (!text.includes(`</${name}`)) return name;
  for (let n = 1; ; n++) {
    const tag = `${name}-${n}`;
    if (!text.includes(`</${tag}`)) return tag;
  }
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
 * joined to the next by one new line, to its end. Each block's tag is the tag the rule gives its
 * name and its words, and its attributes are the rule's, in the rule's order. A text that breaks
 * one of these throws an error that names the break.
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
    if (blockTag(name, said) !== tag) {
      throw new Error(`block <${tag}> holds words whose tag is <${blockTag(name, said)}>`);
    }
    blocks.push({ tag, name, text: said, ...attributes(tag, opening[3] ?? "") });
    if (after >= text.length) break;
    line = after + 1;
  }
  return { head: start === 0 ? "" : text.slice(0, start - 1), blocks };
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
