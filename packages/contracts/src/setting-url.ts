/**
 * What a line may show of the value of a setting that names an http or https URL: an error, a log
 * line, a notice or a link. Such a value may carry a user and a password, since the config accepts
 * such a URL, and a user alone can be a token, so a line names neither; requests still go to the
 * URL as configured. The value is shown only when the URL parser reads it as an http or https URL
 * with a host, and undefined is answered for any other value, of which a line names the length
 * alone. Any other reading cannot be trusted to have found the password: the parser reads
 * `user:password@host` and `mailto:user:password@host` with no user part at all, the password in
 * the path, and its error for a value it refuses holds the value whole.
 *
 * A URL that carries neither user nor password comes back as written. One that carries them comes
 * back as the URL parser writes it: its scheme and host lower-cased and a default port dropped,
 * still naming the same server, and no `/` added after a bare origin. Each line that names such a
 * setting's value, or a URL made from it, asks this first; it is the one function that decides.
 */
export function shownSettingUrl(value: string): string | undefined {
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    return undefined;
  }
  const http = parsed.protocol === "http:" || parsed.protocol === "https:";
  if (!http || parsed.host === "") return undefined;
  if (parsed.username === "" && parsed.password === "") return value;
  parsed.username = "";
  parsed.password = "";
  const shown = parsed.toString();
  return shown.endsWith("/") && !value.endsWith("/") ? shown.slice(0, -1) : shown;
}
