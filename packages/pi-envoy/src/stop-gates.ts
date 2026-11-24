/**
 * The extension's tell gate over its log (R24 of dispatch://INFRA-3589). Once the session's stop
 * has begun, the session tells its pane, its transcript and its log nothing (the tell gate), and
 * writes no ledger entry and no role record (the write gate). Each pane notice, held notice,
 * notice to the model, ledger entry and role record reads the stop itself before it is told or
 * written; the log lines go through the logger below. The stop's own lines are written through
 * the host's logger, which has no gate (`releaseRoleClaims`, `HandBackLedger.handBackAll`). The
 * listener calls have a gate of their own, in the client (`EnvoyClientConfig.stopping`).
 */
export interface StopGates {
  /** `logger` with each of its log methods writing nothing once the stop has begun. */
  readonly gatedLogger: <T extends object>(logger: T) => T;
}

const LOG_METHODS = new Set<string | symbol>(["trace", "debug", "info", "warn", "error", "log"]);

/** The gates of one extension instance: `stopping` is its stop reading. */
export function createStopGates(stopping: () => boolean): StopGates {
  return {
    gatedLogger: (logger) =>
      new Proxy(logger, {
        get(object, key) {
          const value = Reflect.get(object, key, object) as unknown;
          if (typeof value !== "function") return value;
          if (!LOG_METHODS.has(key)) return value.bind(object);
          return (...args: unknown[]) => (stopping() ? undefined : value.apply(object, args));
        },
      }),
  };
}
