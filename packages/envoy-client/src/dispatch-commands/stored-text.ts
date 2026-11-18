// The stored-text check of an inline upload: whether Dispatch would store the text under another
// spelling, read from the store's own parse and render. `dispatch artifact` and its dry run both
// make it (`artifactCommand` and `artifactDryRun` in documents.ts), so the two answer alike.
import { PATH_KEEPS_BYTES, pathKeepsBytesOf } from "@legion/contracts";
import type { DispatchClient, DispatchServiceError } from "../dispatch-http";
import { messageFor } from "../errors";
import { ToolInputError } from "../tool-input-errors";
import { dispatchAnswered } from "./failures";

/**
 * Text Go's `strings.TrimSpace` empties: no characters at all, or only characters Go's
 * `unicode.IsSpace` reports - `\t`, `\n`, `\v`, `\f`, `\r`, a space, U+0085 and U+00A0, and the
 * Unicode `White_Space` property above Latin-1, which is U+1680, U+2000 to U+200A, U+2028,
 * U+2029, U+202F, U+205F and U+3000 (`IsSpace` in Go's `unicode/graphic.go`, reading the
 * `_White_Space` table of its `unicode/tables.go`).
 *
 * The set is written out because it is Go's and not JavaScript's: walked over every Unicode
 * scalar, `String.prototype.trim` and `strings.TrimSpace` differ on exactly two characters.
 * `trim` removes U+FEFF, which Go keeps, so `trim` calls U+FEFF blank where the server reads
 * content; Go removes U+0085, which `trim` keeps, so `trim` calls U+0085 content where the
 * server reads blank. This is the one place that pair is written out, and the lines that turn
 * on it cite this comment rather than repeating it.
 *
 * `packages/envoy/internal/dispatch/text/refs.go` records a split of the same class in another
 * pair of sets ("The whitespace is spelled out rather than written `\s`, which means another
 * set in each language: JavaScript's also matches a vertical tab and U+FEFF"). That pair is the
 * two languages' `\s`, and it differs in a vertical tab and U+FEFF, not in the two characters
 * above.
 *
 * `packages/contracts/fixtures/go-unicode-isspace.json` holds the 25 scalars `unicode.IsSpace`
 * reports. This pattern is held to that list over every scalar by `goBlankPattern` > `calls
 * blank exactly the scalars the committed list holds`, and Go's own predicate by
 * TestUnicodeIsSpaceReportsTheCommittedScalars in
 * `packages/envoy/internal/dispatch/text/whitespace_fixture_test.go`: a character either side
 * gains or loses reddens its test, and the list and this pattern are corrected together.
 */
export const goBlankPattern =
  /^[\t\n\v\f\r \u0085\u00A0\u1680\u2000-\u200A\u2028\u2029\u202F\u205F\u3000]*$/u;

/**
 * How much of one line a refusal quotes whole. A line of a document is bounded by the document
 * itself, so one differing line can be as long as the whole text, and a quote nobody can read
 * to the end is one nobody acts on — the same reason STORED_TEXT_PRINTED bounds the stored
 * text below, and the cut is named the same way.
 */
const STORED_LINE_QUOTED = 200;

/** `line` as a refusal quotes it: its JSON spelling whole while the line is short, and
 *  otherwise that of its first STORED_LINE_QUOTED characters, with the cut named after it. */
function quotedStoredLine(line: string): string {
  if (line.length <= STORED_LINE_QUOTED) return JSON.stringify(line);
  const head = JSON.stringify(line.slice(0, STORED_LINE_QUOTED));
  return `${head} (cut after ${STORED_LINE_QUOTED} of its ${line.length} characters)`;
}

/** The first line the store would write otherwise, which is the line a reader fixes first. The
 *  two texts differ, so one of the three arms answers. Each quote is bounded, since the line it
 *  names can be as long as the document itself. */
function firstStoredDifference(given: string, stored: string): string {
  const written = given.split("\n");
  const holds = stored.split("\n");
  // Each side is read as `string | undefined`, since a line past the end of its own text is
  // undefined and the strict packages that compile this file say so. The two are not equal
  // here, so exactly one arm below answers and the pair can never both be undefined.
  for (let line = 0; line < Math.max(written.length, holds.length); line += 1) {
    const wrote = written[line];
    const held = holds[line];
    if (wrote === held) continue;
    if (wrote === undefined && held !== undefined) {
      return `the store writes a line ${line + 1}, ${quotedStoredLine(held)}`;
    }
    if (held === undefined && wrote !== undefined) {
      return `the store drops line ${line + 1}, ${quotedStoredLine(wrote)}`;
    }
    if (wrote !== undefined && held !== undefined) {
      return `line ${line + 1} reads ${quotedStoredLine(wrote)} and is stored as ${quotedStoredLine(held)}`;
    }
  }
  return "the stored text differs from the text given";
}

/**
 * How much of the stored text a refusal prints whole. Past it the refusal prints the first whole
 * lines that fit and says how much it cut: a document is up to 1 MiB, and a refusal nobody can
 * read to the end is one nobody acts on. The line a reader fixes first is named before the text
 * either way, so a cut refusal still says what to change.
 */
const STORED_TEXT_PRINTED = 2000;

/** The stored text as a refusal prints it, and what the refusal says of the cut: whole while it
 *  is short, and otherwise its first whole lines up to STORED_TEXT_PRINTED characters. */
function storedTextPrinted(stored: string): { readonly text: string; readonly cut: string } {
  if (stored.length <= STORED_TEXT_PRINTED) return { text: stored, cut: "" };
  const head = stored.slice(0, STORED_TEXT_PRINTED);
  const lastLineFeed = head.lastIndexOf("\n");
  const text = lastLineFeed === -1 ? head : head.slice(0, lastLineFeed + 1);
  return {
    text,
    cut: ` The text below is cut after ${text.length} of its ${stored.length} characters: change the line named above and send the text again.`,
  };
}

/**
 * The refusal of inline text the store would change: where the two first differ, the way out
 * this name has, and the text the store writes, so the writer can send exactly that text. The
 * markers hold the stored text apart from the prose around it. A name `--path` would store as
 * bytes has a second way out, which keeps the text as it was written; a Markdown name has none,
 * since `--path` parses and renders it the same way (`pathKeepsBytesOf`).
 */
function storedTextProblem(given: string, stored: string, name: string): string {
  const asBytes = pathKeepsBytesOf(name)
    ? ` To keep the text as you wrote it, send it as a file with ${PATH_KEEPS_BYTES}.`
    : "";
  const printed = storedTextPrinted(stored);
  return [
    `Dispatch stores this text under another spelling: ${firstStoredDifference(given, stored)}.`,
    ` Send the text Dispatch stores, which is between the two marker lines below.${asBytes}`,
    `${printed.cut}\n--- the text Dispatch stores ---\n${printed.text}`,
    "--- end of the text Dispatch stores ---",
  ].join("");
}

/** What a refusal or a warning says of a round trip that failed with `error`. */
export function roundTripFailure(error: unknown): string {
  return `POST /api/v1/markdown/round-trip failed with ${messageFor(error)}`;
}

/**
 * The refusal of an upload whose stored-text check could not run. The check is the one thing that
 * says whether the store would write this text under another spelling, so a failure that is not
 * the route's absence leaves that question open, and an upload sent anyway would store the
 * changed text this check exists to stop.
 *
 * A body Dispatch itself refused as too large is the one such failure a repeat can never get
 * through, so that refusal names what to do instead of saying to send the upload again: the
 * text is past what the check reads, and a name whose extension keeps the bytes has `--path`,
 * which stores the file and asks this route nothing.
 */
export function storedTextCheckFailed(error: unknown, name: string): string {
  const failure = `Dispatch could not say how it would store this text, so nothing was uploaded: ${roundTripFailure(error)}.`;
  if (!dispatchAnswered(error, 413)) {
    return `${failure} Send the upload again once that call is answered.`;
  }
  // Only a body past markdownRoundTripLimit reaches here, and that is always text past the
  // megabyte a markdown document may hold, so every remedy this names has to be one that
  // takes the text out of one document: a document of its own is no remedy at all, since the
  // upload refused is already one and a second would be refused the same way.
  const asBytes = pathKeepsBytesOf(name)
    ? ` Send it as a file with ${PATH_KEEPS_BYTES}, which asks this check nothing.`
    : "";
  return `${failure} This text is longer than the check reads, and sending the upload again does not change that: a markdown document is at most 1 MiB, so shorten it or split it across documents of at most that.${asBytes}`;
}

/**
 * Whether the round trip failed because Dispatch itself holds no such route: the 404 or 405 a
 * Dispatch older than POST /api/v1/markdown/round-trip answers, as its own JSON. Such a server
 * cannot say what it would store, and refused the upload itself before this check existed, so
 * the upload goes on and that server makes its own answer.
 *
 * A 404 or 405 page from an ingress, a proxy or a load balancer is `DispatchGatewayError`,
 * which says nothing about the route, so `dispatchAnswered` is what this asks: reading such a
 * page as an absent route would skip the check and store the respelled text it exists to stop.
 */
function roundTripRouteAbsent(error: unknown): error is DispatchServiceError {
  return dispatchAnswered(error, 404) || dispatchAnswered(error, 405);
}

/** `text` without a final line feed, which is what the stored-text check compares. The store
 *  ends every document it holds with one, so text a writer gave without one comes back with one
 *  and nothing else changed (`markdown_round_trip_test.go` pins "one" stored as "one\n"). That
 *  one line feed is the only difference the check forgives. */
function withoutFinalLineFeed(text: string): string {
  return text.endsWith("\n") ? text.slice(0, -1) : text;
}

/**
 * What Dispatch says of the inline text an upload would store (`storedTextCheck`):
 * - `kept`: the store writes the text as given, but for one final line feed, or the text is
 *   blank, which goes to the server unchecked;
 * - `respelled`: the store writes it under another spelling, and `refusal` is what the call
 *   throws for it;
 * - `no-route`: this Dispatch holds no round-trip route (`roundTripRouteAbsent`), so the upload
 *   goes on unchecked and the server makes its own answer;
 * - `failed`: any other failure, which refuses the upload (`storedTextCheckFailed`).
 */
export type StoredTextCheck =
  | { readonly kind: "kept" }
  | { readonly kind: "respelled"; readonly refusal: ToolInputError }
  | { readonly kind: "no-route"; readonly error: DispatchServiceError }
  | { readonly kind: "failed"; readonly error: unknown };

/**
 * The stored-text check of inline `content` uploaded under `name` by `tool`. `dispatch artifact`
 * and its dry run both make it, so the two never answer differently. Inline text is stored as
 * Markdown: Dispatch parses it and stores its rendering, so a text it would write under another
 * spelling is refused rather than stored changed. The round trip is the store's own parse and
 * render (POST /api/v1/markdown/round-trip), which writes nothing. Every failure of the round
 * trip is returned, never thrown; `client` is called once, for the client the round trip goes
 * through, and what it throws is thrown.
 *
 * Blank text (`goBlankPattern`) is `kept` without calling `client`, so nothing is asked for it,
 * and a dry run of it builds no client: the round trip answers the empty string for
 * every text that pattern calls blank, since storedMarkdown returns "" for whatever
 * `strings.TrimSpace` empties before it parses anything (that package's
 * markdown_round_trip.go), and the empty document is what such a create asks for. That short
 * answer is not what the store writes for each of them: the parse and render a seed runs keep
 * every character of this set but a tab, a line feed, a carriage return and a space, each of the
 * rest rendering to itself. So blank text goes to the server unchecked.
 */
export async function storedTextCheck(
  client: () => DispatchClient,
  tool: string,
  name: string,
  content: string
): Promise<StoredTextCheck> {
  if (goBlankPattern.test(content)) return { kind: "kept" };
  const dispatch = client();
  let stored: string;
  try {
    stored = await dispatch.markdownRoundTrip(content);
  } catch (error) {
    return roundTripRouteAbsent(error) ? { kind: "no-route", error } : { kind: "failed", error };
  }
  // The store ends a document with a line feed the writer's own text need not carry, and that
  // one line feed is no other spelling: both sides lose at most one before they are compared.
  // Every other difference is refused.
  return withoutFinalLineFeed(stored) === withoutFinalLineFeed(content)
    ? { kind: "kept" }
    : {
        kind: "respelled",
        refusal: new ToolInputError(tool, [storedTextProblem(content, stored, name)]),
      };
}
