// packages/envoy-client/src/excerpt.ts
//
// One line of someone else's text, safe to print: a body or a reason phrase that a gateway, a proxy
// or anything else in a server's place wrote. Every client that names such a body in an error
// names it through `bodyForText`: the Dispatch client (`dispatch-http.ts`) a gateway's page, the
// Envoy transport (`transport.ts`) a body that is not the listener's error JSON, pi-shared's daemon
// client one that is not the daemon's refusal, and the operator's live-events probe
// (`scripts/e2e/lib/dispatch-live-events.ts`) a whoami answer with no event_head.

import { textHead } from "./ask-answer";

const REDACTED = "[redacted]";

/**
 * The most of a body an excerpt scans. A gateway page's inline styles and scripts can run to tens
 * of kilobytes before its first words, so this reaches well past them, and it bounds the work a
 * body of any size can cost.
 */
export const EXCERPT_SCAN_LIMIT = 64 * 1024;

/**
 * The shortest run of the client's bearer's characters an excerpt redacts wherever it stands. A
 * gateway that echoes the header can cut it short, split it with markup, or write it twice with the
 * copies overlapping (a bearer can end in its own first characters), so redacting only whole copies
 * leaves most of one behind; a bearer shorter than this is redacted only whole.
 */
const BEARER_PIECE = 8;

/**
 * The first `EXCERPT_SCAN_LIMIT` characters of `text`, each run of `bearer`'s characters at least
 * `BEARER_PIECE` long replaced with one `REDACTED`. Every piece of that length starting inside the
 * limit is found (one that runs past it reads up to `BEARER_PIECE - 1` characters beyond), so a
 * run is redacted whole however the limit or another copy cuts it. Redaction changes the length,
 * so the text is cut to the limit only afterwards, and nothing from past the limit moves into it.
 * Each distinct piece is one native scan of that slice, and no two distinct pieces of one length
 * match at the same position, so the marking is bounded by the slice and the scans by the limit and
 * the bearer's length, whatever the size of the body. Searching again for a repeated piece would
 * mark the same matches again: a bearer of one character repeated would mark every position of the
 * slice once per piece.
 */
function redactBearer(text: string, bearer: string): string {
  if (bearer === "") return text.slice(0, EXCERPT_SCAN_LIMIT);
  const length = Math.min(BEARER_PIECE, bearer.length);
  const scanned = text.slice(0, EXCERPT_SCAN_LIMIT + length - 1);
  const covered = new Uint8Array(scanned.length);
  const pieces = new Set<string>();
  for (let at = 0; at + length <= bearer.length; at++) pieces.add(bearer.slice(at, at + length));
  for (const piece of pieces) {
    let found = scanned.indexOf(piece);
    while (found !== -1) {
      covered.fill(1, found, found + length);
      found = scanned.indexOf(piece, found + 1);
    }
  }
  let redacted = "";
  let kept = 0;
  for (let start = covered.indexOf(1); start !== -1; start = covered.indexOf(1, kept)) {
    redacted += `${scanned.slice(kept, start)}${REDACTED}`;
    const end = covered.indexOf(0, start);
    kept = end === -1 ? scanned.length : end;
  }
  return `${redacted}${scanned.slice(kept, EXCERPT_SCAN_LIMIT)}`.slice(0, EXCERPT_SCAN_LIMIT);
}

/**
 * Text a gateway chose (a body, or the reason phrase) as one line of plain text safe to quote. A
 * misconfigured gateway echoes the request back, so two kinds of credential are redacted: first
 * every piece of the client's own bearer, trimmed as fetch sends it (`redactBearer`); then, after
 * scripts, styles and tags are dropped, the value after `Authorization:` (its scheme kept) or
 * `Bearer`. All of it runs before the cut to one line, so a cut never leaves a credential's first
 * characters behind. The text is the answerer's, and the excerpt is built on the host's event loop,
 * so only the first `EXCERPT_SCAN_LIMIT` characters are read and every pattern runs in linear time:
 * a tag holds no `<`, and a script or style block left open runs to the end.
 */
export function excerpt(text: string, token: string): string {
  const plain = redactBearer(text, token.trim())
    .replace(/<(script|style)\b[^<>]*>(?:[\s\S]*?<\/\1\s*>|[\s\S]*$)/gi, " ")
    .replace(/<[^<>]*>/g, " ");
  return textHead(
    plain
      .replace(
        /(\bauthorization["']?\s*[:=]\s*["']?(?:(?:bearer|basic|digest|token)\s+)?)[^\s"'<>,;]+/gi,
        `$1${REDACTED}`
      )
      .replace(/(\bbearer\s+)[^\s"'<>,;]+/gi, `$1${REDACTED}`)
  );
}

/**
 * `text`, an answer's body that is not what its server writes (`notWhat`, such as "Dispatch's
 * error JSON"), as an error names it: `a body that is not <notWhat> ("<excerpt>")`, one line with
 * `token` and any `Authorization` or `Bearer` value redacted (`excerpt`); `an empty body`; or,
 * where the excerpt reads no text, the body's size and that it holds none (within the first
 * `EXCERPT_SCAN_LIMIT` characters where it runs past them). Never the body whole.
 */
export function bodyForText(text: string, token: string, notWhat: string): string {
  const quoted = excerpt(text, token);
  if (quoted !== "") return `a body that is not ${notWhat} (${JSON.stringify(quoted)})`;
  if (text === "") return "an empty body";
  const within =
    text.length > EXCERPT_SCAN_LIMIT ? ` in its first ${EXCERPT_SCAN_LIMIT / 1024} KiB` : "";
  const size = Buffer.byteLength(text).toLocaleString("en-US");
  return `a body of ${size} bytes and no readable text${within}`;
}

/**
 * The origin a 3xx answer's `Location` names, read against `url`, the request it answers: scheme,
 * host and port, never a path, a query or user info, through `excerpt` so nothing of `token` is
 * printed. Undefined for any other status, and for a `Location` that is absent or does not parse.
 */
export function redirectOrigin(response: Response, url: string, token: string): string | undefined {
  if (response.status < 300 || response.status > 399) return undefined;
  const location = response.headers.get("Location");
  if (location === null || location.trim() === "") return undefined;
  let origin: string;
  try {
    origin = new URL(location, url).origin;
  } catch {
    return undefined;
  }
  if (origin === "null") return undefined;
  const shown = excerpt(origin, token);
  return shown === "" ? undefined : shown;
}
