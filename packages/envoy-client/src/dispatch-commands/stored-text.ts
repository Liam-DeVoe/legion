// The stored-text check of an upload Dispatch stores as a Markdown document: whether the call
// refuses its text for its size, and, for inline text, whether Dispatch would store the text under
// another spelling, read from the store's own parse and render. `dispatch artifact` and its dry run
// both make it, chosen by one function (`artifactTextCheck`, which `artifactCommand` and
// `artifactDryRun` in documents.ts call), so the two answer alike. A document's bytes, a `--path`
// file's or inline text's that are not UTF-8, are refused in the upload's order, their size first
// and then the first U+0000 or byte that is not UTF-8, by one function (`documentBytesRefusal`).
import {
  MARKDOWN_DOCUMENT_MAX_BYTES,
  MARKDOWN_DOCUMENT_TOO_LARGE,
  utf8Bytes,
} from "@legion/contracts";
import { goBlankPattern } from "@legion/contracts/go-blank";
import type { BunFile } from "bun";
import { unstorableBytes } from "../dispatch-file-text";
import { type DispatchClient, DispatchServiceError } from "../dispatch-http";
import { messageFor } from "../errors";
import { ToolInputError } from "../tool-input-errors";
import { dispatchAnswered } from "./failures";
import { storedUpload } from "./stored-spelling";

/** The two round trips the stored-text check asks, as a refusal or a warning names them: inline
 *  text's and a `--path` file's. Each writes nothing. */
const MARKDOWN_ROUND_TRIP = "POST /api/v1/markdown/round-trip";
const FILE_ROUND_TRIP = "POST /api/v1/markdown/file-round-trip";

/**
 * The refusal of an upload whose stored-text check could not run, `failed` its check
 * (`storedTextCheck`). The check is the one thing that says whether the store would write this
 * text under another spelling, so a failure that is not the route's absence leaves that question
 * open, and an upload sent anyway would store the changed text this check exists to stop. A text
 * Dispatch refused, its answer marked as a refusal of the text (`TEXT_REFUSAL_HEADER`), is no such
 * failure: the check answers it with Dispatch's own refusal (`askRoundTrip`).
 */
export function storedTextCheckFailed(failed: Extract<StoredTextCheck, { kind: "failed" }>): Error {
  return new Error(
    `Dispatch could not say how it would store this text, so nothing was uploaded: ${failed.failure}. Send the upload again once that call is answered.`,
    { cause: failed.error }
  );
}

/**
 * Whether the round trip failed because Dispatch itself holds no such route: the 404 or 405 a
 * Dispatch older than the route answers, as its own JSON. Such a server cannot say what it would
 * store, and refused the upload itself before this check existed, so the upload goes on and that
 * server makes its own answer.
 *
 * A 404 or 405 page from an ingress, a proxy or a load balancer is `DispatchGatewayError`,
 * which says nothing about the route, so `dispatchAnswered` is what this asks: reading such a
 * page as an absent route would skip the check and store the respelled text it exists to stop.
 */
function roundTripRouteAbsent(error: unknown): error is DispatchServiceError {
  return dispatchAnswered(error, 404) || dispatchAnswered(error, 405);
}

/**
 * What Dispatch says of the text an upload would store as a Markdown document (`storedTextCheck`
 * for inline text, `storedFileCheck` for a `--path` file), and so what the call does:
 * - `kept`: the store takes the text: inline, as given but for one final line feed, or blank,
 *   which is held to no spelling; from a file, however the store spells it. The call uploads it.
 * - `reordered`: inline text that differs from what the store writes only in the order of typed
 *   blocks' attributes (`storedUpload`). The call uploads `sent`, the text as the store writes it,
 *   and its answer carries `lines`, one for each such block.
 * - `refused`: the call refuses the text, and `refusal` is what it throws for it: a
 *   `ToolInputError` for inline text the store writes under another spelling
 *   (`storedUpload`'s refusal), an `Error` in Dispatch's words for text past the bytes one document
 *   may hold (`documentTooLarge`), and Dispatch's own refusal of the round trip, which is its
 *   upload's refusal of the same text (`askRoundTrip`).
 * - `unchecked`: the round trip gave no answer about the text, and the call uploads it unchecked,
 *   leaving every refusal to the upload: `failure` says why, and `routeAbsent` whether this
 *   Dispatch holds no such route (`roundTripRouteAbsent`). Blank text and a file are asked for a
 *   refusal alone, which their upload makes too, so every failure of theirs is `unchecked`; inline
 *   text that is not blank is only where the route is absent.
 * - `failed`: inline text that is not blank, whose round trip failed otherwise: the call refuses
 *   the upload (`storedTextCheckFailed`), since a check that did not run says nothing about what
 *   the store would write.
 */
export type StoredTextCheck =
  | { readonly kind: "kept" }
  | { readonly kind: "reordered"; readonly sent: string; readonly lines: readonly string[] }
  | { readonly kind: "refused"; readonly refusal: Error }
  | { readonly kind: "unchecked"; readonly failure: string; readonly routeAbsent: boolean }
  | { readonly kind: "failed"; readonly failure: string; readonly error: unknown };

/**
 * The refusal of a Markdown document's text past MARKDOWN_DOCUMENT_MAX_BYTES, or undefined for
 * text within it. `measured` is the text, inline text counted in UTF-8 as the server counts the
 * text it is sent (`utf8Bytes`), never in the UTF-16 units of its `length`; a `--path` file's
 * bytes as they are sent; or a file's size in bytes before it is read. The refusal is the server's
 * own `413 CAP_EXCEEDED` in its own words (MARKDOWN_DOCUMENT_TOO_LARGE), which `messageFor` prints
 * as it prints that answer, for an upload within the server's 25 MiB file cap; past that cap the
 * server answers `artifact blob exceeds 25 MB` instead (errUploadTooLarge). The server refuses
 * such an upload whatever name it carries, so `dispatch artifact` refuses such text first: the
 * call before it reads whether the owner holds the name (`artifactCommand`), the call and its dry
 * run before they ask the round trip (`storedTextCheck`, and `documentFileBytes` for a file), and
 * a document's bytes before the first U+0000 or byte that is not UTF-8 in them
 * (`documentBytesRefusal`).
 */
function documentTooLarge(measured: string | Uint8Array | number): Error | undefined {
  const bytes =
    typeof measured === "number"
      ? measured
      : typeof measured === "string"
        ? utf8Bytes(measured)
        : measured.byteLength;
  return bytes > MARKDOWN_DOCUMENT_MAX_BYTES
    ? new Error(`CAP_EXCEEDED: ${MARKDOWN_DOCUMENT_TOO_LARGE}`)
    : undefined;
}

/**
 * The refusal of a Markdown document's bytes, in the order Dispatch's upload refuses a file's
 * (`documentTextRefusal`, packages/envoy/internal/dispatch/api/artifacts.go), or undefined for
 * bytes that order passes: first the size (`documentTooLarge`), and then the first U+0000 or
 * byte that is not UTF-8, whichever comes first (`unstorableBytes`), in Dispatch's own code,
 * words and count, under `field`. `field` is the field the upload names: `file` for a `--path`
 * file (`documentFileBytes`), `content` for inline text a flag read from a file or stdin whose
 * bytes are not UTF-8 (`runDispatchCli`). Both call this, so the same bytes get the same refusal
 * but for the field, whichever way they are given.
 */
export function documentBytesRefusal(field: string, bytes: Uint8Array): Error | undefined {
  return documentTooLarge(bytes) ?? unstorableBytes(field, bytes);
}

/**
 * The file `dispatch artifact --path` sends for the file at `local`, under the type its upload
 * declares, which Dispatch stores it by: an upload whose file part is declared `text/markdown`,
 * its parameters and its case aside, is a document, and any other is a picture or a file
 * (`artifactKind`, packages/envoy/internal/dispatch/api/artifacts.go). It declares the type Bun
 * reads from the path's extension, `text/markdown` for .md and .markdown (PATH_KEEPS_BYTES). The
 * call uploads this file, or the bytes it read of it under its type, and the call and its dry run
 * read from it whether the file is Markdown before they check it (`documentFileBytes`), so the
 * upload and its check take one rule of which files are Markdown.
 */
export function uploadedFile(local: string): BunFile {
  return Bun.file(local);
}

/**
 * The bytes of an upload's `--path` file that Dispatch stores as a Markdown document, or undefined
 * for a file it stores as its bytes, which is not read here. `file` is the file the call sends
 * (`uploadedFile`), under the type its upload declares, and Dispatch stores an upload as a
 * document exactly when that type is `text/markdown`, its parameters and its case aside. The bytes
 * are the ones the call checks and sends: `held`, those the CLI read for AskGate's check where it
 * read them, or else the file's own, read once. They are refused before they are answered, with
 * the refusal this throws, so no caller checks them again: a file whose size is past the bytes
 * one document holds (`documentTooLarge`) before it is read, and the bytes in hand, held or read,
 * by `documentBytesRefusal`, which refuses bytes past that size, from a file whose size could not
 * be read or that grew since, and then the first U+0000 or byte that is not UTF-8 in them. So such
 * a file is refused in Dispatch's own words before anything is asked, as its upload refuses it.
 */
export async function documentFileBytes(
  file: BunFile,
  held?: Uint8Array<ArrayBuffer>
): Promise<Uint8Array<ArrayBuffer> | undefined> {
  if (file.type.split(";", 1)[0]?.trim().toLowerCase() !== "text/markdown") return undefined;
  if (held === undefined) {
    const size = await file.stat().then(
      (stats) => stats.size,
      () => undefined
    );
    const tooLarge = size === undefined ? undefined : documentTooLarge(size);
    if (tooLarge !== undefined) throw tooLarge;
  }
  const bytes = held ?? (await file.bytes());
  const refusal = documentBytesRefusal("file", bytes);
  if (refusal !== undefined) throw refusal;
  return bytes;
}

/** What a round trip answers for a text: the markdown Dispatch would store for it, Dispatch's
 *  refusal of it, or no answer about it, with what the round trip failed with. */
type RoundTripAnswer =
  | { readonly kind: "stored"; readonly stored: string }
  | { readonly kind: "refused"; readonly refusal: Error }
  | {
      readonly kind: "unanswered";
      readonly failure: string;
      readonly error: unknown;
      readonly routeAbsent: boolean;
    };

/** The check of a text whose round trip gave no answer about it, which the call uploads
 *  unchecked: what the round trip failed with, and whether the route is absent. */
function unchecked(answer: Extract<RoundTripAnswer, { kind: "unanswered" }>): StoredTextCheck {
  return { kind: "unchecked", failure: answer.failure, routeAbsent: answer.routeAbsent };
}

/**
 * Asks `route`, a round trip, which writes nothing, through `ask`: the text inline
 * (`markdownRoundTrip`) or a file's bytes (`fileRoundTrip`). `client` is called once, for the
 * client the round trip goes through, and what it throws is thrown; every failure of the round
 * trip is returned.
 *
 * An answer Dispatch marks as its refusal of the text (`TEXT_REFUSAL_HEADER`, read into
 * `DispatchServiceError.textRefusal`), whatever its status, is the call's refusal, as Dispatch
 * worded it, printed as the call prints the upload's refusal (`CAP_EXCEEDED: <message>`,
 * `INVALID_MARKDOWN: <message>`). Dispatch marks every refusal of the checks an upload makes of
 * the text alone, in that upload's own order, status, code and words, and makes those only a new
 * document's seed makes only for a request that asks for them (`new_document`), which the check
 * asks where the call makes a new document, without `--force`: a replacement is judged against
 * the document it replaces, which relaxes those rules. So every marked refusal the check gets
 * holds for the call, the call and its dry run refuse such a text as the upload would, and a
 * check Dispatch adds to that upload, at any status, reaches them with no change here.
 *
 * Every unmarked failure says nothing of the text, and is `unanswered`: a 404 or 405 is a
 * Dispatch without the route (`roundTripRouteAbsent`), and anything else is the route's own
 * refusal of its body, a 401, a 403, a 429, a 5xx of Dispatch's own, a gateway's page or no
 * answer at all.
 */
async function askRoundTrip(
  client: () => DispatchClient,
  route: string,
  ask: (dispatch: DispatchClient) => Promise<string>
): Promise<RoundTripAnswer> {
  const dispatch = client();
  try {
    return { kind: "stored", stored: await ask(dispatch) };
  } catch (error) {
    const routeAbsent = roundTripRouteAbsent(error);
    if (!routeAbsent && error instanceof DispatchServiceError && error.textRefusal) {
      return { kind: "refused", refusal: error };
    }
    return {
      kind: "unanswered",
      failure: `${route} failed with ${messageFor(error)}`,
      error,
      routeAbsent,
    };
  }
}

/**
 * The stored-text check of inline `content` uploaded under `name` by `tool`, `force` its
 * `--force`. `dispatch artifact` and its dry run both make it, so the two never answer
 * differently, and it is the one place that decides what the call does with blank text. Inline
 * text is stored as Markdown: Dispatch parses it and stores its rendering, so a text it would write
 * under another spelling is refused rather than stored changed, but for one whose typed blocks'
 * attributes differ only in their order, which goes up as Dispatch writes it (`storedUpload`), and
 * a text Dispatch refuses is refused with its refusal (`askRoundTrip`). Without `force` the call makes a new document, so the
 * round trip is asked for a new document's refusals; with it, it is not, and Dispatch answers as
 * the most permissive replacement takes the text: with the rendering that replacement stores,
 * which is compared as any other, or with a refusal every replacement makes, which is the call's.
 *
 * Text past MARKDOWN_DOCUMENT_MAX_BYTES is `refused` (`documentTooLarge`) without calling
 * `client`, since the server refuses an upload of such text whatever it holds, so nothing is asked
 * for it and a dry run of it builds no client. The empty string is `kept` without calling `client`
 * too: it is a create of an empty document, which no check of the text refuses.
 *
 * Any other blank text (`goBlankPattern`) is asked, and only a refusal counts: the upload refuses
 * blank text as it refuses any, too many elements among it, so `refused` is returned as it came,
 * every answer is `kept`, never refused for its spelling, and a round trip that gave no answer
 * leaves the upload to make its own refusal (`unchecked`). The round trip answers "" for every
 * text that pattern calls blank and that it does not refuse, since markdownRoundTrip answers ""
 * for whatever `strings.TrimSpace` empties (that package's markdown_round_trip.go), and that short
 * answer is not what the store writes for each of them: the parse and render a seed runs keep
 * every character of this set but a tab, a line feed, a carriage return and a space, each of the
 * rest rendering to itself.
 */
async function storedTextCheck(
  client: () => DispatchClient,
  tool: string,
  name: string,
  content: string,
  force: boolean
): Promise<StoredTextCheck> {
  const tooLarge = documentTooLarge(content);
  if (tooLarge !== undefined) return { kind: "refused", refusal: tooLarge };
  if (content === "") return { kind: "kept" };
  const answer = await askRoundTrip(client, MARKDOWN_ROUND_TRIP, (dispatch) =>
    dispatch.markdownRoundTrip(content, !force)
  );
  const blank = goBlankPattern.test(content);
  switch (answer.kind) {
    case "refused":
      return answer;
    case "unanswered":
      return blank || answer.routeAbsent
        ? unchecked(answer)
        : { kind: "failed", failure: answer.failure, error: answer.error };
    case "stored": {
      if (blank) return { kind: "kept" };
      // What goes up, and what is refused, is storedUpload's rule (stored-spelling.ts).
      const upload = storedUpload(content, answer.stored, name);
      if ("refusal" in upload) {
        return { kind: "refused", refusal: new ToolInputError(tool, [upload.refusal]) };
      }
      return upload.reordered.length === 0
        ? { kind: "kept" }
        : { kind: "reordered", sent: upload.sent, lines: upload.reordered };
    }
  }
}

/**
 * The stored-text check of a `--path` file Dispatch stores as a Markdown document, `bytes` its
 * content as `documentFileBytes` answers it, already held to the bytes one document holds and
 * free of U+0000 and of bytes that are not UTF-8, and `force` the upload's `--force`. `dispatch
 * artifact` and its dry run both make it, with the bytes the call sends, so the two never answer
 * differently. The file round trip is asked about the file's bytes as they are (`fileRoundTrip`),
 * for a new document's refusals too where the call makes a new document, without `force`, and only
 * its refusal is taken (`askRoundTrip`): Dispatch refuses there, in the upload's own words, the
 * bytes the upload of that file refuses for the text alone - the parse's refusal, an ask that
 * breaks its content rule, a rendering too large, and the size, a NUL and a byte that is not UTF-8,
 * which `documentFileBytes` refused already in the same words - and the call throws that refusal
 * before it uploads.
 * Whatever spelling it answers is `kept`: the call stores a Markdown file as the store spells it,
 * with no refusal for its spelling. The upload refuses the file for everything the round trip
 * would refuse its bytes for, so a round trip that gave no answer - none in time, a failure, or a
 * Dispatch without the route, which answers 404 or 405 - is `unchecked`, and the call uploads the
 * file, leaving its refusal to the upload. The call uploads the file whatever the round trip failed
 * with, so asking it again would only delay that upload: `client` asks it once in the call, as in
 * the dry run (`artifactTextCheck`).
 */
async function storedFileCheck(
  client: () => DispatchClient,
  bytes: Uint8Array,
  force: boolean
): Promise<StoredTextCheck> {
  const answer = await askRoundTrip(client, FILE_ROUND_TRIP, (dispatch) =>
    dispatch.fileRoundTrip(bytes, !force)
  );
  switch (answer.kind) {
    case "stored":
      return { kind: "kept" };
    case "refused":
      return answer;
    case "unanswered":
      return unchecked(answer);
  }
}

/**
 * The check of the text `dispatch artifact` uploads as a Markdown document, chosen here for the
 * call and its dry run alike (`artifactCommand`, `artifactDryRun`), so the two refuse a text alike:
 * inline `content` takes storedTextCheck, asked through `textClient`; a `--path` file's `bytes`, as
 * `documentFileBytes` answers them, take storedFileCheck, asked through `fileClient`; and an upload
 * holding neither - a file Dispatch stores as its bytes, or a name alone - is asked nothing
 * (undefined). The call hands its own client for inline text, since a failed check of such text
 * refuses the upload, so the check asks again as every request of the call does; for a file it
 * hands a client that asks once within DRY_RUN_DEADLINE_MS (`askOnceWithinDryRun`), since the
 * file goes up whatever its check failed with. The dry run hands its own client for both. The
 * check is returned unasked, so its caller decides what becomes of an error building a client
 * throws.
 */
export function artifactTextCheck(upload: {
  readonly tool: string;
  readonly name: string;
  readonly content: string | undefined;
  readonly bytes: Uint8Array | undefined;
  readonly force: boolean;
  readonly textClient: () => DispatchClient;
  readonly fileClient: () => DispatchClient;
}): (() => Promise<StoredTextCheck>) | undefined {
  const { tool, name, content, bytes, force } = upload;
  if (content !== undefined) {
    return () => storedTextCheck(upload.textClient, tool, name, content, force);
  }
  if (bytes !== undefined) return () => storedFileCheck(upload.fileClient, bytes, force);
  return undefined;
}
