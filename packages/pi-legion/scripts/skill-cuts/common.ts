/**
 * What the skill-cuts commands share: a whole-file write, JSON text, a SHA-256 in hex, a zod error's
 * faults on one line, one line of text as a schema, strict option parsing, the configuration as a
 * refusal, the two errors a command's `main` turns into its exit status (`Refused` 2, `Stopped`
 * 3), the one rule by which a gh call that failed is sent again (`GhRetries`), and a read started
 * beside others (`started`). It imports no other command's file, so every one of them can import
 * it.
 */
import { createHash } from "node:crypto";
import { mkdirSync, renameSync, writeFileSync } from "node:fs";
import path from "node:path";
import { parseArgs } from "node:util";
import { messageFor } from "@legion/envoy-client/errors";
import { z } from "zod";
import { loadConfig, type SkillCutsConfig } from "./config";

/** A command's refusal of its arguments or inputs, before any store read or model call: its
 *  `main` exits 2. */
export class Refused extends Error {}

/** A run that stops on its own rule (the budget is spent, or the memory passed its limit): its
 *  `main` exits 3. */
export class Stopped extends Error {}

/** Writes `text` to `file` whole: into a fresh file beside it that is then renamed over it. */
export function writeWhole(file: string, text: string): void {
  mkdirSync(path.dirname(file), { recursive: true });
  const fresh = `${file}.${process.pid}.tmp`;
  writeFileSync(fresh, text);
  renameSync(fresh, file);
}

/** `value` as JSON, two spaces deep, with a final newline. */
export function json(value: unknown): string {
  return `${JSON.stringify(value, null, 2)}\n`;
}

/** The SHA-256 of `bytes`, in hex. */
export function sha256(bytes: string | Uint8Array): string {
  return createHash("sha256").update(bytes).digest("hex");
}

/** Each file of `files`: its path and the SHA-256 of its text. */
export function fileDigests(
  files: readonly { readonly path: string; readonly text: string }[]
): { path: string; sha256: string }[] {
  return files.map((file) => ({ path: file.path, sha256: sha256(file.text) }));
}

/** One line of text: not blank, no line break. */
export const OneLine = z
  .string()
  .refine((text) => text.trim() !== "" && !text.includes("\n"), "is not one non-blank line");

/** Each issue of a zod error as `<path>: <message>`, joined by `; `; an issue of the whole value
 *  is named `root`. */
export function schemaFaults(error: z.ZodError, root = "the value"): string {
  return error.issues
    .map((issue) => `${issue.path.join(".") || root}: ${issue.message}`)
    .join("; ");
}

/** The options `parse` takes, as `parseArgs` takes them. */
type ParseOptions = NonNullable<Parameters<typeof parseArgs>[0]>["options"];

/** The values `parse` gives for the options `O`: each option's value, or undefined. */
export type OptionValues<O extends ParseOptions> = ReturnType<
  typeof parseArgs<{ args: string[]; options: O; strict: true; allowPositionals: false }>
>["values"];

/** Parses `argv` with `options`, strictly, with no positional argument. Throws a refusal that
 *  ends with `usage`. */
export function parse<O extends ParseOptions>(
  argv: readonly string[],
  options: O,
  usage: string
): OptionValues<O> {
  try {
    return parseArgs({ args: [...argv], options, strict: true, allowPositionals: false }).values;
  } catch (error) {
    throw new Refused(`${messageFor(error)}. ${usage}`);
  }
}

/** The value of each option `names` names, or a refusal naming the ones missing. */
export function required<K extends string>(
  values: Partial<Record<K, unknown>>,
  names: readonly K[],
  usage: string
): Record<K, string> {
  const missing = names.filter((name) => typeof values[name] !== "string");
  if (missing.length > 0) {
    throw new Refused(`${missing.map((name) => `--${name}`).join(", ")} required. ${usage}`);
  }
  return values as Record<K, string>;
}

/** The configuration in `file`. Throws a refusal naming why it is none. */
export function configFrom(file: string): SkillCutsConfig {
  try {
    return loadConfig(file);
  } catch (error) {
    throw new Refused(messageFor(error));
  }
}

/** The first line of `text` once the whole text is trimmed: its leading white space goes, and so
 *  does its trailing white space when it is the last line; "" when it has none. */
export function firstLine(text: string): string {
  return text.trim().split("\n")[0] ?? "";
}

/** A rate limit, primary or secondary, as gh reports it. */
export const RATE_LIMITED = /rate limit|HTTP 429/i;
/** The most times one call GitHub refused for its rate limit is sent again. */
export const MAX_RATE_RETRIES = 5;
/** gh's error text for a failure the same call may not meet again, with its reason: the union of
 *  the two lists the runner had before it shared one rule, copies.ts's and context.ts's. The
 *  connection timed out, was reset or refused, or ended early; GitHub's front end answered a 5xx;
 *  the gh process ran past the time limit its caller gave it and was ended (context.ts's reads
 *  have one); a name did not resolve, or did not resolve yet; a pipe broke; or the text names a
 *  timeout in any other words. The first pattern that matches gives the reason, so a more
 *  particular reason comes before a more general one. A call that fails so is sent again
 *  (`GhRetries.waitToRetry`); no other failure is. */
const TRANSIENT_FAILURES: readonly (readonly [reason: string, pattern: RegExp])[] = [
  ["TLS handshake timeout", /TLS handshake timeout/i],
  ["connection reset", /connection reset|reset by peer|ECONNRESET/i],
  ["connection refused", /connection refused|ECONNREFUSED/i],
  ["unexpected EOF", /unexpected EOF/i],
  ["i/o timeout", /i\/o timeout/i],
  ["HTTP 502", /HTTP 502\b/],
  ["HTTP 503", /HTTP 503\b/],
  ["HTTP 504", /HTTP 504\b/],
  ["other HTTP 5xx", /HTTP 5\d\d/i],
  ["time limit", /timed out after \d+ ms/],
  ["timed out", /timed out/i],
  ["TLS handshake", /TLS handshake/i],
  ["no such host", /no such host/i],
  ["temporary failure", /temporary failure/i],
  ["broken pipe", /broken pipe/i],
  ["timeout", /timeout/i],
];
/** The reasons of TRANSIENT_FAILURES that are GitHub's answer to a GraphQL batch too large: a batch
 *  of more than one item that fails so is split by its caller at once, not sent again. Beside a
 *  502 and a 504 that is `timeout`, which GitHub's refusal of a query too costly matches ("This
 *  may be the result of a timeout"); a TLS handshake or i/o timeout matches its own reason first,
 *  so a batch that meets one is sent again. */
const BATCH_TOO_LARGE: Readonly<Record<string, true>> = {
  "HTTP 502": true,
  "HTTP 504": true,
  timeout: true,
};
/** The waits before each try again of a call that failed for a transient reason: at most 4 tries
 *  more. */
const TRANSIENT_WAITS_MS: readonly number[] = [5_000, 15_000, 45_000, 135_000];

/** The reason of TRANSIENT_FAILURES that the error text `stderr` names, or undefined when it names
 *  none. */
export function transientReason(stderr: string): string | undefined {
  return TRANSIENT_FAILURES.find(([, pattern]) => pattern.test(stderr))?.[0];
}

/** The error of a call whose tries failed with `errors`, in their order: the one error, or each
 *  try's. */
export function triesError(errors: readonly string[]): string {
  if (errors.length === 1) return errors[0] ?? "";
  return `${errors.length} tries: ${errors.map((error, index) => `try ${index + 1}: ${error}`).join("; ")}`;
}

/** What one try of a gh call gave, as `GhRetries` reads it. */
export interface GhTry {
  readonly code: number;
  readonly stderr: string;
}

/** Where `GhRetries` waits, and says that it does. */
export interface RetryIo {
  sleep(ms: number): Promise<void>;
  /** Writes one line to standard error. */
  err(line: string): void;
}

/** Where a command says each retry of its gh calls, and how it waits. */
export interface CommandRetryIo {
  /** Writes one line to standard error. */
  err(line: string): void;
  /** The wait before a gh call is sent again; Bun.sleep unless given. */
  sleep?(ms: number): Promise<void>;
}

/** The GhRetries of the command `who`: each retry said on `io.err`, each wait `io.sleep`'s, else
 *  Bun.sleep's. */
export function commandRetries(io: CommandRetryIo, who: string): GhRetries {
  return new GhRetries(
    { sleep: (ms) => io.sleep?.(ms) ?? Bun.sleep(ms), err: (line) => io.err(line) },
    who
  );
}

/** `promise`, a read started now beside others and awaited later in its turn: its rejection is
 *  marked handled, so a read not yet awaited when an earlier one throws is no unhandled
 *  rejection. Awaiting it still throws. */
export function started<T>(promise: Promise<T>): Promise<T> {
  promise.catch(() => undefined);
  return promise;
}

/**
 * The one rule by which the runner sends a gh call again (copies.ts's reads, build.ts's
 * `GithubReads`, context.ts's `ghSource`), with its count of the calls it sent again. A call whose
 * error names a transient failure (TRANSIENT_FAILURES) is sent again after 5, 15, 45 and 135 s, at
 * most 4 times more (TRANSIENT_WAITS_MS), each retry counted by its reason and said on standard
 * error; but a GraphQL batch of more than one item that GitHub answers as a batch too large
 * (BATCH_TOO_LARGE: a 502, a 504, or its refusal of a query too costly) is not, so that its caller
 * splits it at once. A call GitHub refused for its rate limit is sent again after the wait its
 * caller gives (`send`'s `rateWait`), at most MAX_RATE_RETRIES times. No other failure is sent
 * again: a 404, a 401 or 403 that is no rate limit, and an answer not of its shape fail at once. A
 * call that fails on every try fails as it would on one, its error naming each try's.
 */
export class GhRetries {
  /** The calls sent again after a transient failure, by reason, in TRANSIENT_FAILURES' order. */
  readonly counts = new Map(TRANSIENT_FAILURES.map(([reason]): [string, number] => [reason, 0]));

  /** `who` names the command whose calls they are on each line said. */
  constructor(
    private readonly io: RetryIo,
    private readonly who: string
  ) {}

  /** Whether a call that failed with `stderr`, sent again `retried` times so far, is sent again:
   *  when `stderr` names a transient failure, and not one of BATCH_TOO_LARGE for a batch its caller
   *  `splits`, and fewer than 4 retries were made, this counts the retry by its reason, says so on
   *  standard error and sleeps the next wait of TRANSIENT_WAITS_MS. A caller that holds a read slot
   *  calls it outside the slot, so the wait holds none. */
  async waitToRetry(stderr: string, retried: number, splits = false): Promise<boolean> {
    const reason = transientReason(stderr);
    const wait = TRANSIENT_WAITS_MS[retried];
    if (
      reason === undefined ||
      wait === undefined ||
      (splits && Object.hasOwn(BATCH_TOO_LARGE, reason))
    )
      return false;
    this.counts.set(reason, (this.counts.get(reason) ?? 0) + 1);
    this.io.err(
      `${this.who}: a gh call failed (${firstLine(stderr)}); sending it again in ${wait / 1000} s, retry ${retried + 1} of ${TRANSIENT_WAITS_MS.length}`
    );
    await this.io.sleep(wait);
    return true;
  }

  /** The first try of the call `send` makes that exits 0, else the last try, which no rule sends
   *  again, its `stderr` each try's error (`triesError`) when there were several. `splits`: the
   *  call is a GraphQL batch of more than one item, which its caller splits. `rateWait`: the
   *  milliseconds to wait before a call GitHub refused for its rate limit is sent again, at its
   *  `refused`-th refusal (from 0); without it a rate limit is not waited out. `send` takes any
   *  read slot itself, so no wait holds one. */
  async send<T extends GhTry>(
    send: () => Promise<T>,
    options: {
      readonly splits?: boolean;
      readonly rateWait?: (stderr: string, refused: number) => Promise<number>;
    } = {}
  ): Promise<T> {
    const errors: string[] = [];
    let limited = 0;
    let transient = 0;
    for (;;) {
      const result = await send();
      if (result.code === 0) return result;
      errors.push(firstLine(result.stderr) || `gh exited ${result.code}`);
      if (
        options.rateWait !== undefined &&
        limited < MAX_RATE_RETRIES &&
        RATE_LIMITED.test(result.stderr)
      ) {
        await this.io.sleep(await options.rateWait(result.stderr, limited));
        limited++;
      } else if (await this.waitToRetry(result.stderr, transient, options.splits)) transient++;
      else return errors.length === 1 ? result : { ...result, stderr: `${triesError(errors)}\n` };
    }
  }

  /** The retries counted, with those of `before` (an earlier run's record) added: each reason with
   *  at least one, in TRANSIENT_FAILURES' order, as a summary records them. */
  record(before: Readonly<Record<string, number>> = {}): Record<string, number> {
    const record: Record<string, number> = {};
    for (const [reason, count] of this.counts) {
      const all = count + (before[reason] ?? 0);
      if (all > 0) record[reason] = all;
    }
    return record;
  }
}

/** Runs at most `limit` tasks at once: each task past the limit waits until one ends, the first to
 *  wait starting first. */
export class Limiter {
  #running = 0;
  readonly #waiting: (() => void)[] = [];

  constructor(readonly limit: number) {
    if (!Number.isInteger(limit) || limit < 1) {
      throw new Error(`a limit of ${limit} tasks at once is not a whole number of 1 or more`);
    }
  }

  /** `task`'s result, once a place is free and the task has run. */
  async run<T>(task: () => Promise<T>): Promise<T> {
    if (this.#running < this.limit) this.#running += 1;
    else await new Promise<void>((resolve) => this.#waiting.push(resolve));
    try {
      return await task();
    } finally {
      // The place passes to the first task that waits, or is freed.
      const next = this.#waiting.shift();
      if (next === undefined) this.#running -= 1;
      else next();
    }
  }
}

/** The environment variable that bounds the gh processes the runner has at once. */
export const GH_CONCURRENCY_ENV = "SKILL_CUTS_GH_CONCURRENCY";
/** The gh processes the runner has at once when GH_CONCURRENCY_ENV is unset: a pod's gh is a
 *  wrapper of about 145 MiB a call (a binary and a leak check), so the bound keeps the runner's
 *  processes near 600 MiB however many reads one profile starts. */
export const GH_CONCURRENCY = 4;
let ghLimit: Limiter | undefined;

/** The one bound on concurrent gh processes of the whole runner: every gh spawn runs inside
 *  `ghLimiter().run(...)`. Its limit is GH_CONCURRENCY_ENV's value (a whole number of 1 or more),
 *  else GH_CONCURRENCY; throws on another value. */
export function ghLimiter(): Limiter {
  if (ghLimit === undefined) {
    const given = process.env[GH_CONCURRENCY_ENV];
    if (given !== undefined && !/^[1-9]\d*$/.test(given)) {
      throw new Error(`${GH_CONCURRENCY_ENV}=${given} is not a whole number of 1 or more`);
    }
    ghLimit = new Limiter(given === undefined ? GH_CONCURRENCY : Number(given));
  }
  return ghLimit;
}
