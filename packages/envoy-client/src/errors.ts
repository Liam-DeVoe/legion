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
 * Coerce an unknown thrown value into a human-readable message.
 *
 * A refusal Dispatch decided reads `<CODE>: <message>`, because `skill://dispatch` has every agent
 * branch on an error's code rather than on its words, and the code was in no line the command
 * printed: a reader had to run the same call through curl to see it (reported by the tree of
 * dispatch://INFRA-1523). A message that already opens with its own code is left as it is, and
 * every other thrown value prints as it always did.
 */
export function messageFor(error: unknown): string {
  if (!(error instanceof Error)) return String(error);
  const code = dispatchCode(error);
  return code === undefined || error.message.startsWith(code)
    ? error.message
    : `${code}: ${error.message}`;
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

/** Whether `status` is a redirect's: no request of this client follows one or reads its body. */
export function isRedirectStatus(status: number): boolean {
  return status >= 300 && status < 400;
}

/**
 * The origin a redirect points at, for the line that refuses it: the scheme and the host (with a
 * port that is not the scheme's own) of `response`'s `Location`, read against `url`, the request
 * it answers. Never its path, query, fragment or user, any of which can carry a secret. Undefined
 * for an answer that is no redirect, and for a `Location` that is missing or names no http(s)
 * origin.
 */
export function redirectOrigin(response: Response, url: string): string | undefined {
  const location = response.headers.get("Location");
  if (!isRedirectStatus(response.status) || location === null) return undefined;
  try {
    const target = new URL(location, url);
    return target.protocol === "http:" || target.protocol === "https:" ? target.origin : undefined;
  } catch {
    return undefined;
  }
}
