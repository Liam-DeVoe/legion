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

/**
 * What a line may show of the value of a setting that names an http or https URL, which may carry
 * a user and a password: the value with neither (`withoutUserinfo`) when the URL parser reads it as
 * an http or https URL with a host, and undefined for any other value, of which a line names the
 * length alone. Any other reading cannot be trusted to have found the password: the parser reads
 * `user:password@host` and `mailto:user:password@host` with no user part at all, the password
 * in the path, and its error for a value it refuses holds the value whole. Each line that names
 * such a setting's value, or a URL made from it, asks this first.
 */
export function shownSettingUrl(value: string): string | undefined {
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    return undefined;
  }
  const http = parsed.protocol === "http:" || parsed.protocol === "https:";
  return http && parsed.host !== "" ? withoutUserinfo(value) : undefined;
}
