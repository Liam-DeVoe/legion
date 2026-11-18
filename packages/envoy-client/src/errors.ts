/**
 * The code of the refusal `error` carries, or undefined for an error carrying none.
 *
 * A refusal Dispatch decided (`DispatchServiceError`, and the gateway answer that stands in for
 * one) carries the code of the first rule the call broke beside its message, and `fromDispatch`
 * is what tells such an error from a Node errno error, which carries a `code` of its own (ENOENT,
 * EPERM, …) that says nothing about what Dispatch decided. The check is structural rather than an
 * `instanceof`, because those classes live in `dispatch-http.ts`, which reads this module's
 * neighbours.
 */
function dispatchCode(error: Error): string | undefined {
  if (!("fromDispatch" in error) || !("code" in error)) return undefined;
  const code = error.code;
  return typeof code === "string" && code !== "" ? code : undefined;
}

/**
 * The `hint` Dispatch gave beside its refusal `error`, or undefined for an error carrying none.
 * Only Dispatch's answer to a path no route serves carries one ("GET /api/v1 lists every route").
 * Structural like `dispatchCode`, for the same reason.
 */
function dispatchHint(error: Error): string | undefined {
  if (!("fromDispatch" in error) || !("hint" in error)) return undefined;
  const hint = error.hint;
  return typeof hint === "string" && hint !== "" ? hint : undefined;
}

/**
 * Coerce an unknown thrown value into a human-readable message.
 *
 * A refusal Dispatch decided reads `<CODE>: <message>`, because `skill://dispatch` has every agent
 * branch on an error's code rather than on its words, and the code was in no line the command
 * printed: a reader had to run the same call through curl to see it (reported by the tree of
 * dispatch://INFRA-1523). A message that already opens with its own code is left as it is, and
 * every other thrown value prints as it always did. Where Dispatch gave a hint beside its refusal,
 * it follows the message after "; ", since the hint says where to look next.
 */
export function messageFor(error: unknown): string {
  if (!(error instanceof Error)) return String(error);
  const code = dispatchCode(error);
  const message =
    code === undefined || error.message.startsWith(code)
      ? error.message
      : `${code}: ${error.message}`;
  const hint = dispatchHint(error);
  return hint === undefined ? message : `${message}; ${hint}`;
}

/** True when `error` is a Node errno error carrying `code` (ENOENT, EPERM, …). */
export function hasErrnoCode(error: unknown, code: string): boolean {
  return error instanceof Error && "code" in error && error.code === code;
}

/**
 * `url` with no user and no password, for any text that names it: an error, a log line, a notice
 * or a link. A configured Dispatch or Envoy URL may carry both, since the config accepts such a
 * URL, and a user alone can be a token, so both go; requests still go to the URL as configured. A
 * URL that carries neither comes back as written. One that carries them comes back as the URL
 * parser writes it: its scheme and host lower-cased and a default port dropped, still naming the
 * same server, and no `/` added after a bare origin. A string the URL parser refuses comes back as
 * what follows its last `@`, since what precedes it may be a password.
 */
export function withoutUserinfo(url: string): string {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return url.slice(url.lastIndexOf("@") + 1);
  }
  if (parsed.username === "" && parsed.password === "") return url;
  parsed.username = "";
  parsed.password = "";
  const shown = parsed.toString();
  return shown.endsWith("/") && !url.endsWith("/") ? shown.slice(0, -1) : shown;
}
