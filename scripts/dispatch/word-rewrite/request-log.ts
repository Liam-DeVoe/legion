import { appendFileSync } from "node:fs";
import { setTimeout as sleep } from "node:timers/promises";

/**
 * The record of every HTTP request a run makes, one line `METHOD path status` each, never a
 * header, a body or a host: check k8 reads it to show that report mode only reads. Every request
 * of the tool goes through `loggingFetch`, Dispatch's and each daemon's alike, which also keeps
 * the census rate of at most five requests a second (`scripts/dispatch/README.md`, display read
 * census).
 */

export interface RequestLog {
  record(method: string, path: string, status: number | "error"): void;
  /** Every line recorded so far, in order. */
  lines(): readonly string[];
}

/** A log kept in memory, and appended to `file` line by line when one is named, so a run that
 *  stops early still leaves every request it made. */
export function requestLog(file?: string): RequestLog {
  const kept: string[] = [];
  return {
    record(method, path, status) {
      const line = `${method} ${path} ${status}`;
      kept.push(line);
      if (file !== undefined) appendFileSync(file, `${line}\n`);
    },
    lines: () => kept,
  };
}

/** The clock a rate waits on, which a test replaces with one that does not wait. */
export interface Clock {
  now(): number;
  sleep(ms: number): Promise<void>;
}

export const SYSTEM_CLOCK: Clock = { now: () => Date.now(), sleep: (ms) => sleep(ms) };

/** The fewest milliseconds between the starts of two requests: five a second. */
export const REQUEST_GAP_MS = 200;

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
export function pathOf(input: Parameters<typeof fetch>[0]): string {
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
