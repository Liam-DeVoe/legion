// How a client of this workspace prints a field another party wrote: a server's refusal, an
// issuer's error description, a code, a body. Such a field is the reason a person acts on, so it
// prints as written, but a URL it quotes can carry a credential, which must not reach a log or an
// error a session prints. The rule fails closed: a field it cannot read with certainty prints as
// WORDS_NOT_READABLE. Go's half is `redact.Remote` in packages/envoy/internal/redact, and both
// halves are held to the fixture at packages/envoy/internal/redact/testdata/remote.json.

/** What a message prints in place of a field the rule cannot read as clean: Go's
 *  `redact.WordsNotReadable`. */
export const WORDS_NOT_READABLE =
  "(not printed: the answer's words hold a URL the rule cannot read as clean)";

/** The escapes the rule's step 7 rejects, lower-cased: of `/`, `:`, `@`, `?`, `#`, `=`, `&`, `%`
 *  and `\`, each as `%XX`, `\u00XX` and `\xXX`. */
const ENCODED_SEPARATORS = ["2f", "3a", "40", "3f", "23", "3d", "26", "25", "5c"].flatMap((hex) => [
  `%${hex}`,
  `\\u00${hex}`,
  `\\x${hex}`,
]);

/**
 * The rule for one field another party wrote. It answers the field with each URL's user
 * information removed, each query and fragment value removed (the names stay) and each bare query
 * or fragment segment removed, only when it can read every URL in the field with certainty, and
 * `undefined` otherwise. The grammar is Go's `redact.Remote`, step for step:
 *  1. A URL starts at a scheme (`[A-Za-z][A-Za-z0-9+.-]*`), `:` and two slashes, or at two
 *     slashes not preceded by a scheme character, `:` or a slash. Either slash may be a
 *     backslash; step 3 rejects such a start.
 *  2. A run is the text from a URL start to the next ASCII whitespace or the end of the field.
 *     When it does not end inside a query or a fragment, the characters `.,;:!?)]}'"` at its end
 *     are not part of it; when it does, they are part of the last value.
 *  3. Not readable when a run holds `%`, `\`, a quote, `<`, `>`, a backtick, a control character
 *     or any non-ASCII character; when its authority holds more than one `@`; when it holds an
 *     `@` after its authority; or when a port is not all digits.
 *  4. Not readable when a query or fragment segment (split on `&` and `;`) has a value that holds
 *     `=` or `?`.
 *  5. Not readable when a token, trimmed as in 2, holds an `@` with a `:` before it; in a token
 *     that holds a URL start this reads the text before the start.
 *  6. Not readable when a token after the token that holds the first URL start holds an `@`;
 *     when the token right after a run holds `?`, `#` or `=`; or when a run ends inside a query
 *     or a fragment and more text follows it.
 *  7. Not readable when the field holds an encoded `/`, `:`, `@`, `?`, `#`, `=`, `&`, `%` or `\`
 *     (`%2F`, `\u002F`, `\x2F` and their kin, in any case).
 *  8. Not readable when the field, read with its ASCII whitespace removed, holds a `user:secret@`
 *     user information right after a scheme's `://` or a key's `=`, or a query or fragment value
 *     of a URL, whose characters the field itself splits with whitespace ({@link splitCredential}).
 *     A label, a `:` and an address with no scheme and no key before them, as in
 *     `sign-in: ada@example.com`, is text: no syntax tells a split `user:secret@host` from it, and
 *     a refusal prints identities.
 *
 * Its answer is not meant to be read by the rule again.
 */
export function readRemoteText(field: string): string | undefined {
  const lower = field.toLowerCase();
  if (ENCODED_SEPARATORS.some((encoded) => lower.includes(encoded))) return undefined;
  let out = "";
  let seenUrl = false;
  let afterRun = false;
  for (let i = 0; i < field.length; ) {
    if (isAsciiSpace(field.charCodeAt(i))) {
      out += field[i];
      i++;
      continue;
    }
    let end = i;
    while (end < field.length && !isAsciiSpace(field.charCodeAt(end))) end++;
    const token = field.slice(i, end);
    if ((seenUrl && token.includes("@")) || (afterRun && /[?#=]/.test(token))) return undefined;
    const start = urlStart(token);
    const outside = start >= 0 ? token.slice(0, start) : token;
    const bare = trimRunEnd(outside);
    const colon = bare.indexOf(":");
    if (colon >= 0 && bare.lastIndexOf("@") > colon) return undefined;
    out += outside;
    afterRun = false;
    if (start >= 0) {
      const read = readRun(token.slice(start));
      if (read === undefined) return undefined;
      if (read.inQuery && /[^ \t\n\v\f\r]/.test(field.slice(end))) return undefined;
      out += read.shown + token.slice(start + read.length);
      seenUrl = true;
      afterRun = true;
    }
    i = end;
  }
  return splitCredential(field) ? undefined : out;
}

/** The characters a `user:secret@` user information cannot hold, which end the piece
 *  {@link splitCredential} reads one for. */
const USER_INFO_STOP = "/?#&;=,\"'<>()[]{}`\\|";

/**
 * Whether `field`, read with its ASCII whitespace removed, holds a credential-shaped piece whose
 * characters the field splits with whitespace (step 8): a `user:secret@` user information, a `:`
 * and the next `@` with none of {@link USER_INFO_STOP} between them, right after a scheme's `://`
 * or a key's `=` ({@link userInfoLead}) and before a host's first character (a letter, a digit or
 * an IPv6 address's `[`, Go's `hostStart`), or a query or fragment value, one that is not empty,
 * of a URL that starts where step 1 finds one in the field, read in the whitespace-removed text
 * from that start to the first quote, `<`, `>` or backtick. Go's `splitCredential`.
 */
function splitCredential(field: string): boolean {
  let squeezed = "";
  const at: number[] = [];
  for (let i = 0; i < field.length; i++) {
    if (!isAsciiSpace(field.charCodeAt(i))) {
      squeezed += field[i];
      at.push(i);
    }
  }
  if (squeezed.length === field.length) return false;
  const split = (from: number, to: number): boolean =>
    to > from && (at[to - 1] as number) - (at[from] as number) !== to - 1 - from;
  let from = 0;
  let colon = -1;
  for (let i = 0; i < squeezed.length; i++) {
    const c = squeezed[i] as string;
    if (USER_INFO_STOP.includes(c)) {
      from = i + 1;
      colon = -1;
    } else if (c === ":" && colon < 0) colon = i;
    else if (c === "@") {
      // The user information comes before a host: its first character is a letter, a digit or `[`.
      const host = /^[A-Za-z0-9[]$/.test(squeezed[i + 1] ?? "");
      if (colon >= 0 && host && split(from, i + 1) && userInfoLead(squeezed, from)) return true;
      // A user information ends at its `@`.
      from = i + 1;
      colon = -1;
    }
  }
  // Each URL start step 1 finds in the field, as an index into squeezed, one per token.
  const starts: number[] = [];
  for (let t = 0; t < squeezed.length; ) {
    let end = t;
    while (end + 1 < squeezed.length && at[end + 1] === (at[end] as number) + 1) end++;
    const u = urlStart(squeezed.slice(t, end + 1));
    if (u >= 0) starts.push(t + u);
    t = end + 1;
  }
  for (let n = 0; n < starts.length; n++) {
    const start = starts[n] as number;
    let end = n + 1 < starts.length ? (starts[n + 1] as number) : squeezed.length;
    const stop = squeezed.slice(start + 1, end).search(/["'<>`]/);
    if (stop >= 0) end = start + 1 + stop;
    const q = squeezed.slice(start, end).search(/[?#]/);
    if (q < 0) continue;
    for (let seg = start + q + 1; seg < end; ) {
      let next = seg;
      while (next < end && !"&;#?".includes(squeezed[next] as string)) next++;
      const eq = squeezed.slice(seg, next).indexOf("=");
      if (eq >= 0 && seg + eq + 1 < next && split(start, next)) return true;
      seg = next + 1;
    }
  }
  return false;
}

/** Whether the piece at `from` in `squeezed`, the whitespace-removed field, follows a scheme and
 *  its `://`, either slash a backslash too (`https://user: pw@host`), or a key and its `=`
 *  (`proxy=user: pw@host`): what tells a split `user:secret@` from a label and an address
 *  (`sign-in: ada@example.com`), which step 8 reads as text. Go's `userInfoLead`. */
function userInfoLead(squeezed: string, from: number): boolean {
  if (from >= 2 && squeezed[from - 1] === "=")
    return !USER_INFO_STOP.includes(squeezed[from - 2] as string);
  if (
    from >= 4 &&
    isSlash(squeezed[from - 1]) &&
    isSlash(squeezed[from - 2]) &&
    squeezed[from - 3] === ":"
  ) {
    return schemeStart(squeezed, from - 3) >= 0;
  }
  return false;
}

/** A field another party wrote as a message prints it: what {@link readRemoteText} answers, on
 *  one line ({@link oneLine}), or {@link WORDS_NOT_READABLE} where it cannot read the field. Go's
 *  `redact.Field`. */
export function redactRemoteText(field: string): string {
  const read = readRemoteText(field);
  return read === undefined ? WORDS_NOT_READABLE : oneLine(read);
}

/**
 * One field another party wrote, held so that it redacts itself: every way JavaScript prints it
 * prints {@link redactRemoteText} of it, never the field. The text sits in a private field, which
 * `JSON.stringify`, an object spread and an inspection do not reach, and its own methods answer
 * the rest: `toString`, `toJSON`, `Symbol.toPrimitive` (a template string, `String()`, `+`) and
 * Node's and Bun's inspection hook. Only this class reads the text, and each of its methods reads
 * it in one of three ways alone: as the argument of the rule, as an operand of a comparison or a
 * parse whose answer is a boolean or a parsed value, or in the one assignment the constructor
 * makes. Code that tells fields apart compares through {@link equals},
 * {@link equalsIgnoringCase}, {@link startsWith} and {@link isEmpty}, and reads a time through
 * {@link date}; none answers the text. Go's `redact.RemoteText`.
 */
export class RemoteText {
  readonly #text: string;

  constructor(text: string) {
    this.#text = text;
  }

  /** The field as a message may print it ({@link redactRemoteText}). */
  toString(): string {
    return redactRemoteText(this.#text);
  }

  /** {@link toString}, so `JSON.stringify` writes the field as a message may print it. */
  toJSON(): string {
    return this.toString();
  }

  [Symbol.toPrimitive](): string {
    return this.toString();
  }

  [Symbol.for("nodejs.util.inspect.custom")](): string {
    return this.toString();
  }

  /** Whether the field is empty. It answers no text. */
  get isEmpty(): boolean {
    return this.#text === "";
  }

  /** Whether the field is `text`, compared as it came. It answers no text. */
  equals(text: string): boolean {
    return this.#text === text;
  }

  /** Whether the field begins with `prefix`, compared as it came. It answers no text. */
  startsWith(prefix: string): boolean {
    return this.#text.startsWith(prefix);
  }

  /** Whether the field is `text` under Unicode case folding, compared as it came, as Go's
   *  `strings.EqualFold` compares. It answers no text. */
  equalsIgnoringCase(text: string): boolean {
    const literal = text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    return new RegExp(`^${literal}$`, "iu").test(this.#text);
  }

  /** The field parsed as a time (`new Date`), or `undefined` where it names none. A field that
   *  does not parse answers no text. */
  date(): Date | undefined {
    const at = new Date(this.#text);
    return Number.isNaN(at.getTime()) ? undefined : at;
  }
}

/**
 * An error a library threw — openid-client and the oauth4webapi it is built on — as
 * {@link callLibrary} holds it once it has crossed that boundary. Such an error can carry an
 * issuer's or the broker's words in `error`, `error_description` and `error_uri`, the answer's
 * body (a `ResponseBodyError`'s `cause`; an `UnsupportedOperationError`'s `cause.body`, which
 * holds a token the answer minted), the answer itself (`response`), and an error of its own
 * under `cause`. This one keeps none of them: the status, the library's name for the error, and
 * each of its words as a {@link RemoteText} — the code, the description, the library's message and
 * the message of the error directly under it — so every way JavaScript prints it, an inspection
 * that follows `cause` included, prints only the rule's reading of them. Go's `redact.OAuthAnswer`.
 */
export class LibraryError extends Error {
  /** The HTTP status of the answer behind the error, 0 where it names none. */
  readonly status: number;
  /** The answer's `error`, empty where it names none. */
  readonly code: RemoteText;
  /** The answer's `error_description`, empty where it names none. */
  readonly description: RemoteText;
  /** The message of the error the library's own error was thrown over, empty where none. */
  readonly causeMessage: RemoteText;

  constructor(thrown: unknown) {
    const error = thrown instanceof Error ? thrown : undefined;
    super(redactRemoteText(error === undefined ? String(thrown) : error.message));
    this.name = error !== undefined && /^\w+$/.test(error.name) ? error.name : "Error";
    this.status = statusOfThrown(thrown);
    this.code = new RemoteText(stringMember(thrown, "error"));
    this.description = new RemoteText(stringMember(thrown, "error_description"));
    const cause = error?.cause;
    this.causeMessage = new RemoteText(cause instanceof Error ? cause.message : "");
  }
}

/**
 * The one way this workspace calls openid-client: every call of its, any of which can throw, is
 * written `callLibrary(() => call(...))`, and the census in
 * packages/envoy/internal/redact/testdata/wholeuse/boundary.ts, which reads the library's own
 * exported API, fails on any that is not. What the call throws, or what the promise it answers
 * rejects with, is a {@link LibraryError} before the caller sees it; what it answers comes back
 * as it is. Go's `redact.OAuth2`.
 */
export function callLibrary<T>(call: () => Promise<T>): Promise<T>;
export function callLibrary<T>(call: () => T): T;
export function callLibrary(call: () => unknown): unknown {
  let result: unknown;
  try {
    result = call();
  } catch (error) {
    throw error instanceof LibraryError ? error : new LibraryError(error);
  }
  if (result instanceof Promise) {
    return result.catch((error: unknown) => {
      throw error instanceof LibraryError ? error : new LibraryError(error);
    });
  }
  return result;
}

/** The status of the first value down `thrown`'s `cause` chain, itself first, that names a
 *  numeric one — a `ResponseBodyError`, or the `Response` an error hangs off its cause — or 0. */
function statusOfThrown(thrown: unknown): number {
  let value = thrown;
  for (let depth = 0; depth < 4; depth += 1) {
    if (typeof value !== "object" || value === null) return 0;
    if ("status" in value && typeof value.status === "number") return value.status;
    if (!("cause" in value)) return 0;
    value = value.cause;
  }
  return 0;
}

/** `value[name]` where it is a string, and "" otherwise. */
function stringMember(value: unknown, name: string): string {
  if (typeof value !== "object" || value === null || !(name in value)) return "";
  const member: unknown = Reflect.get(value, name);
  return typeof member === "string" ? member : "";
}

/** `text` as one line of a message may print it: as it is where it holds no control character
 *  (C0, DEL or C1) and no lone surrogate, and otherwise quoted as Go's `strconv.Quote` quotes it
 *  (goQuote), every character that is not printable escaped, so a line break, an escape sequence
 *  or a line or paragraph separator cannot start a line of its own. Go's `redact.OneLine`; both
 *  read packages/envoy/internal/redact/testdata/oneline.json. */
export function oneLine(text: string): string {
  for (let i = 0; i < text.length; i++) {
    const code = text.charCodeAt(i);
    const next = i + 1 < text.length ? text.charCodeAt(i + 1) : 0;
    const control = code <= 0x1f || (code >= 0x7f && code <= 0x9f);
    const high = code >= 0xd800 && code <= 0xdbff;
    const low = code >= 0xdc00 && code <= 0xdfff;
    if (control || low || (high && !(next >= 0xdc00 && next <= 0xdfff))) return goQuote(text);
    if (high) i++;
  }
  return text;
}

/** A character Go's `strconv.IsPrint` prints as it is: a letter, mark, number, punctuation or
 *  symbol, or the ASCII space. */
const printable = /^[\p{L}\p{M}\p{N}\p{P}\p{S} ]$/u;

/** The escapes `strconv.Quote` writes for the C0 controls that have one. */
const shortEscapes: Record<number, string> = {
  7: "\\a",
  8: "\\b",
  9: "\\t",
  10: "\\n",
  11: "\\v",
  12: "\\f",
  13: "\\r",
};

/** `text` quoted as Go's `strconv.Quote` quotes it: `"` and `\` escaped, a printable character as
 *  it is, a control with a short escape by it, any other below U+0080 as `\xNN`, and every other
 *  character as `\uNNNN` or `\UNNNNNNNN`, in lower-case hexadecimal. A lone surrogate, which Go's
 *  JSON decoding reads as U+FFFD, is U+FFFD. */
function goQuote(text: string): string {
  let out = '"';
  for (const ch of text) {
    let code = ch.codePointAt(0) as number;
    if (code >= 0xd800 && code <= 0xdfff) code = 0xfffd;
    const char = String.fromCodePoint(code);
    if (code === 0x22 || code === 0x5c) out += `\\${char}`;
    else if (printable.test(char)) out += char;
    else if (shortEscapes[code] !== undefined) out += shortEscapes[code];
    else if (code < 0x20 || code === 0x7f) out += `\\x${code.toString(16).padStart(2, "0")}`;
    else if (code < 0x10000) out += `\\u${code.toString(16).padStart(4, "0")}`;
    else out += `\\U${code.toString(16).padStart(8, "0")}`;
  }
  return `${out}"`;
}

/** Where the first URL in `token` starts (step 1), or -1. */
function urlStart(token: string): number {
  for (let i = 0; i + 1 < token.length; i++) {
    if (
      token[i] === ":" &&
      i + 2 < token.length &&
      isSlash(token[i + 1]) &&
      isSlash(token[i + 2])
    ) {
      const start = schemeStart(token, i);
      if (start >= 0) return start;
      continue;
    }
    if (
      isSlash(token[i]) &&
      isSlash(token[i + 1]) &&
      (i === 0 || !/[A-Za-z0-9+.\-:/\\]/.test(token[i - 1] as string))
    ) {
      return i;
    }
  }
  return -1;
}

/** The run at the start of `text`, which runs to its end, read (steps 2, 3 and 4): as it prints,
 *  its length in `text`, and whether it ends inside a query or a fragment; `undefined` where it is
 *  not readable. */
function readRun(text: string): { shown: string; length: number; inQuery: boolean } | undefined {
  const head = isSlash(text[0]) ? 2 : text.indexOf(":") + 3;
  const inQuery = /[?#]/.test(text.slice(head));
  const run = inQuery ? text : trimRunEnd(text);
  for (let i = 0; i < run.length; i++) {
    const code = run.charCodeAt(i);
    if (code < 0x20 || code >= 0x7f || "%\\\"'<>`".includes(run[i] as string)) return undefined;
  }
  const rest = run.slice(head);
  const authEnd = rest.search(/[/?#]/);
  const authority = authEnd < 0 ? rest : rest.slice(0, authEnd);
  const after = authEnd < 0 ? "" : rest.slice(authEnd);
  if (authority.split("@").length > 2 || after.includes("@")) return undefined;
  const host = authority.slice(authority.indexOf("@") + 1);
  if (!digitPort(host)) return undefined;
  let path = after;
  let query = "";
  let fragment = "";
  const hash = after.indexOf("#");
  if (hash >= 0) {
    path = after.slice(0, hash);
    fragment = after.slice(hash);
  }
  const question = path.indexOf("?");
  if (question >= 0) {
    query = path.slice(question);
    path = path.slice(0, question);
  }
  let shown = run.slice(0, head) + host + path;
  for (const part of [query, fragment]) {
    if (part === "") continue;
    shown += part[0];
    let segments = part.slice(1);
    for (;;) {
      const end = segments.search(/[&;]/);
      const segment = end < 0 ? segments : segments.slice(0, end);
      const equals = segment.indexOf("=");
      if (equals >= 0) {
        if (/[=?]/.test(segment.slice(equals + 1))) return undefined;
        shown += `${segment.slice(0, equals)}=`;
      }
      if (end < 0) break;
      shown += segments[end];
      segments = segments.slice(end + 1);
    }
  }
  return { shown, length: run.length, inQuery };
}

/** Whether `host`'s port, if it names one, is all digits: after `]:` for a bracketed IPv6 host,
 *  and after the first `:` otherwise. A `[` with no `]` is not read. */
function digitPort(host: string): boolean {
  let port = "";
  if (host.startsWith("[")) {
    const end = host.indexOf("]");
    if (end < 0) return false;
    const rest = host.slice(end + 1);
    if (rest !== "") {
      if (rest[0] !== ":") return false;
      port = rest.slice(1);
    }
  } else {
    const colon = host.indexOf(":");
    if (colon >= 0) port = host.slice(colon + 1);
  }
  return /^[0-9]*$/.test(port);
}

/** `s` without the characters `.,;:!?)]}'"` it ends in (steps 2 and 5). */
function trimRunEnd(s: string): string {
  let end = s.length;
  while (end > 0 && `.,;:!?)]}'"`.includes(s[end - 1] as string)) end--;
  return s.slice(0, end);
}

/** Where the scheme ending at `colon` starts in `s`: the first letter of the longest run of
 *  scheme characters before `colon`, or -1 when that run holds no letter. */
function schemeStart(s: string, colon: number): number {
  let start = colon;
  while (start > 0 && /[A-Za-z0-9+.-]/.test(s[start - 1] as string)) start--;
  while (start < colon && !/[A-Za-z]/.test(s[start] as string)) start++;
  return start === colon ? -1 : start;
}

function isSlash(character: string | undefined): boolean {
  return character === "/" || character === "\\";
}

/** Whether the UTF-16 code `code` is ASCII whitespace, which ends a run and a token. */
function isAsciiSpace(code: number): boolean {
  return code === 0x20 || (code >= 0x09 && code <= 0x0d);
}
