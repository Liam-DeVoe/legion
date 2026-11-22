/**
 * The extension's two stop gates (R24 of dispatch://INFRA-3589). Once the session's stop has
 * begun, the session tells its pane, its transcript and its log nothing (the tell gate), and
 * writes no ledger entry and no role record (the write gate), apart from the stop's own acts. The
 * listener calls have a gate of their own, in the client (`EnvoyClientConfig.stopping`).
 */
export interface StopGates {
  /**
   * The tell gate: whether `line` may be told now. Every line may until the stop begins. From
   * then on only the stop's own lines may (`stopLines`). Every pane notice, held notice, notice
   * to the model and log line of the extension's own asks it first.
   */
  readonly tells: (line: string) => boolean;
  /** `logger` with each of its log methods asking the tell gate for its line first: a line the
   *  gate drops is not written. */
  readonly gatedLogger: <T extends object>(logger: T) => T;
  /** The write gate: whether a ledger entry or a role record may be written now. False once the
   *  stop has begun. */
  readonly writes: () => boolean;
}

const LOG_METHODS = new Set<string | symbol>(["trace", "debug", "info", "warn", "error", "log"]);

/** The gates of one extension instance: `stopping` is its stop reading, and `stopLines` the
 *  stop's own lines, which the tell gate lets through. */
export function createStopGates(
  stopping: () => boolean,
  stopLines: ReadonlySet<string>
): StopGates {
  const tells = (line: string): boolean => !stopping() || stopLines.has(line);
  return {
    tells,
    gatedLogger: (logger) =>
      new Proxy(logger, {
        get(object, key) {
          const value = Reflect.get(object, key, object) as unknown;
          if (typeof value !== "function") return value;
          if (!LOG_METHODS.has(key)) return value.bind(object);
          return (line: unknown, ...rest: unknown[]) => {
            if (typeof line === "string" && !tells(line)) return undefined;
            return value.call(object, line, ...rest);
          };
        },
      }),
    writes: () => !stopping(),
  };
}
