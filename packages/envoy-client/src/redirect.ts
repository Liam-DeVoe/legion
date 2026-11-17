/**
 * No request that carries a credential follows a redirect. Left to itself, fetch sends the request
 * again to the origin a 3xx's `Location` names, and Bun and Node drop only `Authorization` and
 * `Cookie` on the way: a DPoP proof, an `x-api-key` header, and a 307's or 308's body all reach
 * that origin. So such a request is sent with `redirect: "manual"`, its 3xx is its answer, and the
 * caller refuses that answer, naming its status and, where there is one, the origin below.
 */

/**
 * The origin a redirect points at, for the line that refuses it: the scheme and the host (with a
 * port that is not the scheme's own) of `response`'s `Location`, read against `url`, the request
 * it answers. Never its path, query, fragment or user, any of which can carry a secret. Undefined
 * for an answer that is no redirect, and for a `Location` that is missing or names no http(s)
 * origin.
 */
export function redirectOrigin(response: Response, url: string): string | undefined {
  const location = response.headers.get("Location");
  if (response.status < 300 || response.status > 399 || location === null) return undefined;
  try {
    const target = new URL(location, url);
    return target.protocol === "http:" || target.protocol === "https:" ? target.origin : undefined;
  } catch {
    return undefined;
  }
}
