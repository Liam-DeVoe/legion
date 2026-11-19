/**
 * `inFront` and `TextInFront` of `@legion/envoy-client/words-block`, written a second time for the
 * mod's prompt at the rows a turn never read (`unreadRowPrompt`, `./mod-protocol.ts`). A mod
 * imports nothing from outside `hooks/` (`./mod-protocol.ts` says why), so it cannot take the
 * shared ones; `../tests/words-in-front.test.ts` checks that the two give the same text for the
 * same input. Change the two together. Like `./mod-protocol.ts`, this file stays import-free:
 * plain functions and classes over their arguments that read no global.
 */

/**
 * `parts` and then `text`, each joined to the next by a blank line: the lines and the messages
 * the prompt puts in front of a card, and the card. A card draws another author's words after its
 * fields, each in a block of their own under a tag that nothing else in the card opens or closes.
 * A part can hold another author's words too, so the blocks of `text` (`drawnBlocks`) are drawn
 * again, each with its attributes as written and its words as they were, under the tag the rule
 * gives it against the whole of what is sent: the first of its name, `name-1`, `name-2` and on
 * that neither a part, nor the text of `text` before and after its blocks, nor the words of any
 * block hold as `<tag` or `</tag`, and that no earlier block took. Where neither the parts nor the
 * text after the blocks hold a tag the blocks could take, each block keeps its tag and the result
 * is the parts and `text` joined as they are; so is a text with no drawn block. A prompt of
 * several cards is built with `TextInFront`, which gives each card the text this gives it.
 */
export function inFront(parts: readonly string[], text: string): string {
  const [first, ...rest] = parts
  if (first === undefined) return text
  const sent = new TextInFront(first)
  for (const part of rest) sent.part(part)
  sent.card(text)
  return sent.text()
}

/**
 * A text built of parts and cards, each joined to the one before by a blank line: the prompt at
 * the rows a turn never read. A part is joined as it is. A card is joined as `inFront` joins it
 * behind the whole text before it, every earlier card as it was drawn there included, so the text
 * is byte for byte the one that joining each part and taking `inFront([text so far], card)` for
 * each card gives.
 *
 * What that draw scans the text before a card for is the tags of the names the card's blocks
 * have, and all that a part or a card adds to it is its own text. So the text keeps, for each
 * name a block has had, the tags of that name it holds: a name is scanned for over the text once,
 * when a block first has it, and each part and card after that is scanned for it on its own. The
 * whole is drawn in time linear in its length for each name its cards' blocks have, where taking
 * `inFront` of the text so far for each card scans all of it again at every card, every earlier
 * card's words included.
 */
export class TextInFront {
  readonly #held: HeldTags

  /** A text that starts with `first`, as it is. */
  constructor(first: string) {
    this.#held = new HeldTags([first])
  }

  /** Joins `text` after a blank line, as it is. */
  part(text: string): void {
    this.#held.add(`\n\n${text}`)
  }

  /** Joins `text`, a card, after a blank line, with its blocks (`drawnBlocks`) drawn again, each
   *  with its attributes as written and its words as they were, under the tag the rule gives it
   *  against the whole text before the card and the card (`inFront`). */
  card(text: string): void {
    const found = drawnBlocks(text)
    const first = found[0]
    const last = found[found.length - 1]
    if (first === undefined || last === undefined) {
      this.part(text)
      return
    }
    const head = text.slice(0, first.start)
    const tail = text.slice(last.end)
    const tags = blockTags(this.#held, [head, tail], found)
    const blocks = found.map(
      ({ attributes, text: words }, index) =>
        `<${tags[index] ?? ""}${attributes}>\n${words}\n</${tags[index] ?? ""}>`,
    )
    this.part(`${head}${blocks.join("\n")}${tail}`)
  }

  /** The text joined so far. */
  text(): string {
    return this.#held.texts.join("")
  }
}

/** The opening line of a block as a card draws it: the tag, then each attribute, whose value
 *  holds no double quote and no angle bracket. */
const OPENING = /^<([A-Za-z_][A-Za-z0-9_]*)(?:-([1-9][0-9]*))?((?: [a-z]+="[^"<>]*")*)>$/

/** One block of a text as `drawnBlocks` found it: where its opening line starts and where its
 *  closing tag ends, the name of the field that holds the words (its tag without the `-n` a step
 *  past held tags added), the attributes of its opening tag as written, and its words. */
interface DrawnBlock {
  readonly start: number
  readonly end: number
  readonly name: string
  readonly attributes: string
  readonly text: string
}

/**
 * The blocks of `text`, a card, in their order. No line of a card ahead of its blocks starts with
 * `<`, since its fields are TOON lines, so its first such line opens its first block. A block
 * closes on the first line after its opening that is its closing tag, since no words of a block
 * hold that tag, and the next block opens on the line after. The run ends at the first line that
 * opens no block. It is empty where the first line that starts with `<` opens none, or opens a
 * block that never closes, as in a text no card drew, which `inFront` then joins as it is. One
 * pass over `text`.
 */
function drawnBlocks(text: string): DrawnBlock[] {
  let at = 0
  if (!text.startsWith("<")) {
    const line = text.indexOf("\n<")
    if (line < 0) return []
    at = line + 1
  }
  const blocks: DrawnBlock[] = []
  while (text[at] === "<") {
    const opened = text.indexOf("\n", at)
    if (opened < 0) break
    const opening = OPENING.exec(text.slice(at, opened))
    if (opening === null) break
    const name = opening[1] ?? ""
    const closing = `\n</${opening[2] === undefined ? name : `${name}-${opening[2]}`}>`
    const closed = text.indexOf(closing, opened + 1)
    if (closed < 0) break
    const end = closed + closing.length
    if (end < text.length && text[end] !== "\n") break
    blocks.push({
      start: at,
      end,
      name,
      attributes: opening[3] ?? "",
      text: text.slice(opened + 1, closed),
    })
    at = end + 1
  }
  return blocks
}

/**
 * A text in pieces, and for each name it was asked of, the suffixes whose tags it holds
 * (`holdSuffixes`) and the least suffix it may not hold: it holds every suffix below that one. A
 * name is scanned for over every piece when it is first asked of, and each piece added after that
 * is scanned for every name asked of so far, so no piece is scanned twice for one name. A piece is
 * scanned on its own, which finds what the joined text holds, since a tag holds no new line.
 */
class HeldTags {
  readonly texts: string[]
  readonly #names = new Map<string, { readonly suffixes: Set<number>; least: number }>()

  constructor(texts: readonly string[]) {
    this.texts = [...texts]
  }

  /** Adds `text` after the pieces, scanned for every name asked of so far. */
  add(text: string): void {
    this.texts.push(text)
    for (const [name, held] of this.#names) holdSuffixes(held.suffixes, name, text)
  }

  /** What the text holds of the tags of `name`. */
  of(name: string): { readonly suffixes: ReadonlySet<number>; readonly least: number } {
    let held = this.#names.get(name)
    if (held === undefined) {
      held = { suffixes: new Set(), least: 0 }
      for (const text of this.texts) holdSuffixes(held.suffixes, name, text)
      this.#names.set(name, held)
    }
    while (held.suffixes.has(held.least)) held.least++
    return held
  }
}

/** The tag of each of `blocks`, the blocks of one text in their order, behind `front`, the text
 *  in front of that one, where `beside` is the rest of that text: the first of `name`, `name-1`,
 *  `name-2` and on whose `<tag` and `</tag` neither `front`, nor a text of `beside`, nor the
 *  words of any block hold, and that no earlier block took. A text is scanned on its own, which
 *  finds what the joined text holds, since a tag holds no new line. */
function blockTags(
  front: HeldTags,
  beside: readonly string[],
  blocks: readonly DrawnBlock[],
): string[] {
  const texts = [...beside, ...blocks.map(({ text }) => text)]
  // For each name, the suffixes these texts hold or a block took, and the least that may be
  // free: every suffix below it is held, in front or here, or taken.
  const named = new Map<string, { readonly suffixes: Set<number>; next: number }>()
  return blocks.map(({ name }) => {
    const held = front.of(name)
    let own = named.get(name)
    if (own === undefined) {
      own = { suffixes: new Set(), next: held.least }
      for (const text of texts) holdSuffixes(own.suffixes, name, text)
      named.set(name, own)
    }
    while (held.suffixes.has(own.next) || own.suffixes.has(own.next)) own.next++
    const suffix = own.next
    own.suffixes.add(suffix)
    return suffix === 0 ? name : `${name}-${suffix}`
  })
}

/** The largest suffix a scan keeps. A block takes the least suffix that is neither held nor
 *  taken, so never one above the count of those held and taken, and each suffix held takes a
 *  unit of the text and each one taken a block. No text comes near this many units, so no suffix
 *  a block could take lies past it, and stopping here keeps every number exact. */
const SUFFIX_CAP = Number.MAX_SAFE_INTEGER

/** Adds to `held` the suffixes of `name` whose tags `text` holds: 0 where it holds `<name` or
 *  `</name`, and n where it holds `<name-n` or `</name-n`, so `<answer-12` holds 0, 1 and 12. One
 *  scan of `text` for each of the two marks, so the time is linear in its length. */
function holdSuffixes(held: Set<number>, name: string, text: string): void {
  for (const mark of [`<${name}`, `</${name}`]) {
    for (let at = text.indexOf(mark); at !== -1; at = text.indexOf(mark, at + mark.length)) {
      held.add(0)
      const dash = at + mark.length
      if (text[dash] !== "-") continue
      let suffix = 0
      for (let index = dash + 1; index < text.length; index++) {
        const digit = text.charCodeAt(index) - 48
        // A tag's suffix has no leading zero, so `-0` holds none.
        if (digit < 0 || digit > 9 || (suffix === 0 && digit === 0)) break
        suffix = suffix * 10 + digit
        if (suffix > SUFFIX_CAP) break
        held.add(suffix)
      }
    }
  }
}
