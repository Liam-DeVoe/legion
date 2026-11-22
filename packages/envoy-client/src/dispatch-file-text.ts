// How the `dispatch` command reads the text of every `--<field>-file <path>` and
// `--<field>-json-file <path>`, and of stdin for `-`: as bytes, decoded as UTF-8 strictly. A lenient
// read writes each byte that is not UTF-8 as U+FFFD, which the call then stores in place of the
// writer's byte; this read throws `NotUtf8Error` instead, and `parseCommand` refuses the command
// with Dispatch's own refusal of such a byte (`notUtf8Refusal`), its dry run and its call alike. A
// byte order mark is kept, as U+FEFF, as the lenient read kept it. The `dispatch` entry
// (`bin/dispatch.ts`) reads through `readFileText`, and a test hands it stdin's bytes.
import { readFileSync } from "node:fs";

const decoder = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true });

/** A byte as Dispatch's refusal writes it: `0x` and two upper-case hex digits (`%02X`). */
function byteText(byte: number): string {
  return `0x${byte.toString(16).toUpperCase().padStart(2, "0")}`;
}

/**
 * Bytes that are not UTF-8: `byte` is the first byte that is not, and `character` where it
 * stands, counted from 1 in UTF-16 units, as Dispatch counts it (`unstorableText`,
 * packages/envoy/internal/dispatch/api/storable_text.go, which counts `itemblocks.Units16` of the
 * text before the byte, plus one).
 */
export class NotUtf8Error extends Error {
  override readonly name = "NotUtf8Error";

  constructor(
    readonly byte: number,
    readonly character: number
  ) {
    super(`a byte that is not UTF-8 (${byteText(byte)}) at character ${character}`);
  }
}

/**
 * Where the first byte of `bytes` that is not UTF-8 stands, as Go's `utf8.DecodeRune` finds it,
 * which answers such a byte as a RuneError of width 1, or -1 where every byte is UTF-8. It is a
 * byte no sequence opens with (a continuation byte, 0xC0, 0xC1, 0xF5 to 0xFF), or one that opens a
 * sequence the bytes after it cut short or break: too few of them, or one outside the range its
 * place takes, which also refuses an overlong form, a surrogate and a code point past U+10FFFF.
 */
function firstNotUtf8(bytes: Uint8Array): number {
  for (let at = 0; at < bytes.length; ) {
    const lead = bytes[at] as number;
    if (lead < 0x80) {
      at += 1;
      continue;
    }
    // The width of the sequence `lead` opens, and the range of the byte after it.
    let width: number;
    let low = 0x80;
    let high = 0xbf;
    if (lead >= 0xc2 && lead <= 0xdf) {
      width = 2;
    } else if (lead >= 0xe0 && lead <= 0xef) {
      width = 3;
      if (lead === 0xe0) low = 0xa0;
      if (lead === 0xed) high = 0x9f;
    } else if (lead >= 0xf0 && lead <= 0xf4) {
      width = 4;
      if (lead === 0xf0) low = 0x90;
      if (lead === 0xf4) high = 0x8f;
    } else {
      return at;
    }
    if (at + width > bytes.length) return at;
    const second = bytes[at + 1] as number;
    if (second < low || second > high) return at;
    for (let next = at + 2; next < at + width; next += 1) {
      if (((bytes[next] as number) & 0xc0) !== 0x80) return at;
    }
    at += width;
  }
  return -1;
}

/** `bytes` decoded as UTF-8, a byte order mark kept as U+FEFF; `NotUtf8Error` for bytes that are
 *  not UTF-8, naming the first such byte and where it stands. */
export function utf8Text(bytes: Uint8Array): string {
  try {
    return decoder.decode(bytes);
  } catch (error) {
    const at = firstNotUtf8(bytes);
    if (at === -1) throw error;
    // Every byte before `at` is UTF-8, so the decoder takes them.
    throw new NotUtf8Error(bytes[at] as number, decoder.decode(bytes.subarray(0, at)).length + 1);
  }
}

/** The text of the file `path` names, or of all of stdin for `-` (`readStdin`: file descriptor
 *  0, unless a test hands its own bytes), read as bytes and decoded by `utf8Text`. */
export function readFileText(
  path: string,
  readStdin: () => Uint8Array = () => readFileSync(0)
): string {
  return utf8Text(path === "-" ? readStdin() : readFileSync(path));
}

/** Dispatch's refusal of `field` holding the bytes `error` names, in the code and words it refuses
 *  such a byte in a form field with (`unstorableText`'s INVALID_UTF8), which `messageFor` prints
 *  as it prints that refusal from Dispatch. */
export function notUtf8Refusal(field: string, error: NotUtf8Error): Error {
  return new Error(
    `INVALID_UTF8: ${field} holds a byte that is not UTF-8 (${byteText(error.byte)}) at character ${error.character}, which Dispatch cannot store`
  );
}
