/**
 * `inFront` of `@legion/envoy-client/words-block`, written a second time for the mod's prompt at
 * the rows a turn never read (`unreadRowPrompt`, `./mod-protocol.ts`). A mod imports nothing from
 * outside `hooks/` (`./mod-protocol.ts` says why), so it cannot take the shared one;
 * `../tests/words-in-front.test.ts` checks that the two give the same text for the same input.
 * Change the two together. Like `./mod-protocol.ts`, this file stays import-free: plain functions
 * over their arguments that read no global.
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
 * is the parts and `text` joined as they are; so is a text with no drawn block.
 */
export function inFront(parts: readonly string[], text: string): string {
  if (parts.length === 0) return text
  const front = `${parts.join("\n\n")}\n\n`
  const found = drawnBlocks(text)
  const first = found[0]
  const last = found[found.length - 1]
  if (first === undefined || last === undefined) return front + text
  const head = text.slice(0, first.start)
  const tail = text.slice(last.end)
  const tags = blockTags([...parts, head, tail], found)
  const blocks = found.map(
    ({ attributes, text: words }, index) =>
      `<${tags[index] ?? ""}${attributes}>\n${words}\n</${tags[index] ?? ""}>`,
  )
  return `${front}${head}${blocks.join("\n")}${tail}`
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

/** The tag of each of `blocks`, the blocks of one text in their order, where `before` is the rest
 *  of the text: the first of `name`, `name-1`, `name-2` and on whose `<tag` and `</tag` neither a
 *  part of `before` nor the words of any block hold, and that no earlier block took. A part is
 *  scanned on its own, which finds what the joined text holds, since a tag holds no new line. */
function blockTags(before: readonly string[], blocks: readonly DrawnBlock[]): string[] {
  const texts = [...before, ...blocks.map(({ text }) => text)]
  // No tag can take a suffix above this: each suffix the texts hold takes a character of them,
  // and each block takes one suffix.
  const cap = texts.reduce((length, text) => length + text.length, blocks.length) + 1
  // For each name, the suffixes held or taken (0 is the bare name), and the least that may be
  // free: every suffix below it is held or taken.
  const used = new Map<string, { readonly suffixes: Set<number>; next: number }>()
  return blocks.map(({ name }) => {
    let named = used.get(name)
    if (named === undefined) {
      named = { suffixes: heldSuffixes(name, texts, cap), next: 0 }
      used.set(name, named)
    }
    while (named.suffixes.has(named.next)) named.next++
    const suffix = named.next
    named.suffixes.add(suffix)
    return suffix === 0 ? name : `${name}-${suffix}`
  })
}

/** The suffixes of `name` whose tags `texts` hold, up to `cap`: 0 where a text holds `<name` or
 *  `</name`, and n where it holds `<name-n` or `</name-n`, so `<answer-12` holds 0, 1 and 12. One
 *  scan of each text for each of the two marks, so the time is linear in the texts' length. */
function heldSuffixes(name: string, texts: readonly string[], cap: number): Set<number> {
  const held = new Set<number>()
  for (const text of texts) {
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
          if (suffix > cap) break
          held.add(suffix)
        }
      }
    }
  }
  return held
}
