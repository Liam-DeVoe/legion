import { appendFileSync } from "node:fs";
import { setTimeout as sleep } from "node:timers/promises";

/**
 * The record of every HTTP request a run makes, one line `METHOD path status` each, never a
 * header, a body or a host: check k8 reads it to show that report mode only reads. Every request
 * of the tool goes through `loggingFetch`, Dispatch's and each daemon's alike, which also keeps
 * the census rate of at most five requests a second (`scripts/dispatch/README.md`, display read
 * census). The run keeps only the count of its requests; each line goes to the `--request-log`
 * file, or to a test's own recorder.
 */

export interface RequestLog {
  record(method: string, path: string, status: number | "error"): void;
  /** How many requests were recorded so far. */
  count(): number;
}

/** A count of the requests, each line given to `sink` where one is named, as it is recorded. */
export function requestLog(sink?: (line: string) => void): RequestLog {
  let count = 0;
  return {
    record(method, path, status) {
      count += 1;
      sink?.(`${method} ${path} ${status}`);
    },
    count: () => count,
  };
}

/** A sink that appends each line to `file` as it comes, so a run that stops early still leaves
 *  every request it made. */
export function appendTo(file: string): (line: string) => void {
  return (line) => appendFileSync(file, `${line}\n`);
}

/** The clock a rate waits on, which a test replaces with one that does not wait. */
export interface Clock {
  now(): number;
  sleep(ms: number): Promise<void>;
}

export const SYSTEM_CLOCK: Clock = { now: () => Date.now(), sleep: (ms) => sleep(ms) };

/** The fewest milliseconds between the starts of two requests: five a second. */
const REQUEST_GAP_MS = 200;

/** Spaces the starts of requests at least `gapMs` apart, whoever asks. */
export class RequestRate {
  #next = 0;

  constructor(
    private readonly clock: Clock = SYSTEM_CLOCK,
    private readonly gapMs = REQUEST_GAP_MS
  ) {}

  /** Resolves when the caller's request may start, and books the slot after it. */
  async turn(): Promise<void> {
    const now = this.clock.now();
    const start = Math.max(now, this.#next);
    this.#next = start + this.gapMs;
    if (start > now) await this.clock.sleep(start - now);
  }
}

/** The path and query of a request, without its scheme, host or credentials. */
function pathOf(input: Parameters<typeof fetch>[0]): string {
  const url = new URL(input instanceof Request ? input.url : String(input));
  return `${url.pathname}${url.search}`;
}

/** `inner`, with each request waiting its turn at `rate` and recorded in `log` once it ends. */
export function loggingFetch(
  inner: typeof fetch,
  log: RequestLog,
  rate: RequestRate
): typeof fetch {
  const logged = async (input: Parameters<typeof fetch>[0], init?: RequestInit) => {
    const method = (
      init?.method ?? (input instanceof Request ? input.method : "GET")
    ).toUpperCase();
    const path = pathOf(input);
    await rate.turn();
    try {
      const response = await inner(input, init);
      log.record(method, path, response.status);
      return response;
    } catch (error) {
      log.record(method, path, "error");
      throw error;
    }
  };
  return Object.assign(logged, { preconnect: inner.preconnect });
}
