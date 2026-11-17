// The backstop under every rule of `log-text.ts`: the secrets this process holds, and the scrub
// every line the clients print passes. The rules name nothing a peer or a setting wrote; the scrub
// catches what a rule let through anyway, such as a server at the wrong URL that echoes the
// bearer it was sent inside a refusal the client prints as guidance. A line that holds no held
// secret comes back as the same string, byte for byte.

/** What stands in for each held secret, or part of one, the scrub finds. */
export const WITHHELD = "<withheld>";

/** The shortest run of a held secret the scrub takes out: 10 characters. A held value is
 *  scrubbed where it stands whole, and every run of 10 or more of its characters is too. */
export const SCRUB_RUN = 10;

/** The shortest value held: a shorter one cannot be told from ordinary words of a line, and the
 *  secrets held (tokens, keys, seeds) are far longer. */
const SHORTEST_HELD = SCRUB_RUN;

interface Held {
  /** Each held value. */
  readonly values: Set<string>;
  /** Each run of {@link SCRUB_RUN} characters of a held value, and the values it is a run of. */
  readonly runs: Map<string, string[]>;
}

/** One registry per process, whichever copy of this module a bundle carries. */
const KEY = Symbol.for("@legion/contracts/held-secrets");

function held(): Held {
  const global = globalThis as { [KEY]?: Held };
  global[KEY] ??= { values: new Set(), runs: new Map() };
  return global[KEY];
}

/**
 * Holds `value`, a secret this process read or was handed (a broker token, a Dispatch token, an
 * identity token, a session's JWT, the contents of a secret file), so the scrub takes it out of
 * every line printed from now on. A value shorter than 10 characters, or one held already, is
 * left as it is. Returns `value`, so a reader can hold what it answers in one step.
 */
export function holdSecret(value: string): string {
  const registry = held();
  if (value.length < SHORTEST_HELD || registry.values.has(value)) return value;
  registry.values.add(value);
  for (let at = 0; at + SCRUB_RUN <= value.length; at += 1) {
    const run = value.slice(at, at + SCRUB_RUN);
    const values = registry.runs.get(run);
    if (values === undefined) registry.runs.set(run, [value]);
    else if (!values.includes(value)) values.push(value);
  }
  return value;
}

/** Forgets every held secret: for a test, which holds its own from nothing. */
export function forgetHeldSecrets(): void {
  const registry = held();
  registry.values.clear();
  registry.runs.clear();
}

/**
 * `text` with every held secret, and every run of 10 or more characters of one, replaced by
 * `<withheld>`. Overlapping or touching runs are one. A text with none comes back as itself.
 */
export function scrub(text: string): string {
  const { runs } = held();
  if (runs.size === 0 || text.length < SCRUB_RUN) return text;
  const spans: [number, number][] = [];
  for (let at = 0; at + SCRUB_RUN <= text.length; at += 1) {
    const values = runs.get(text.slice(at, at + SCRUB_RUN));
    if (values === undefined) continue;
    let end = at + SCRUB_RUN;
    for (const value of values) {
      let reach = at + SCRUB_RUN;
      while (reach < text.length && value.includes(text.slice(at, reach + 1))) reach += 1;
      if (reach > end) end = reach;
    }
    const last = spans.at(-1);
    if (last !== undefined && at <= last[1]) last[1] = Math.max(last[1], end);
    else spans.push([at, end]);
  }
  if (spans.length === 0) return text;
  let out = "";
  let from = 0;
  for (const [start, end] of spans) {
    out += text.slice(from, start) + WITHHELD;
    from = end;
  }
  return out + text.slice(from);
}

/** `error` with its message and stack scrubbed in place, and every cause it holds, for an error
 *  a host prints whole. Anything that is no error is left as it is. */
export function scrubError<T>(error: T): T {
  for (let at: unknown = error, depth = 0; at instanceof Error && depth < 8; depth += 1) {
    const message = scrub(at.message);
    if (message !== at.message) at.message = message;
    if (typeof at.stack === "string") {
      const stack = scrub(at.stack);
      if (stack !== at.stack) at.stack = stack;
    }
    at = at.cause;
  }
  return error;
}

type Write = (chunk: unknown, ...rest: unknown[]) => boolean;

/**
 * Passes every write to this process's stdout and stderr through the scrub, for a process of its
 * own (Claude Code's channel server and hooks): a string or a UTF-8 chunk that holds no held
 * secret is written as it came, byte for byte. Installing twice installs once.
 */
export function scrubProcessStreams(): void {
  for (const stream of [process.stdout, process.stderr]) {
    const marked = stream as typeof stream & { [KEY]?: true };
    if (marked[KEY]) continue;
    marked[KEY] = true;
    const write = stream.write.bind(stream) as Write;
    (stream as { write: Write }).write = (chunk: unknown, ...rest: unknown[]) => {
      if (typeof chunk === "string") return write(scrub(chunk), ...rest);
      if (chunk instanceof Uint8Array) {
        const text = new TextDecoder().decode(chunk);
        const scrubbed = scrub(text);
        return write(scrubbed === text ? chunk : scrubbed, ...rest);
      }
      return write(chunk, ...rest);
    };
  }
}
