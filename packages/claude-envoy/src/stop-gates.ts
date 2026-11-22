/**
 * A channel server's two stop gates (R24 of dispatch://INFRA-3589). Once the session's stop has
 * begun, the server tells the session and its stderr nothing (the tell gate), and writes no role
 * record and no ledger entry (the write gate), apart from the stop's own acts. The listener calls
 * have a gate of their own, in the client (`EnvoyClientConfig.stopping`).
 */
export interface StopGates {
  /**
   * The tell gate: whether `line` may be told now. Every line may until the stop begins. From
   * then on only the stop's own lines may (`STOP_LINES`). Every notice card and stderr line of the
   * server's own asks it first.
   */
  readonly tells: (line: string) => boolean
  /** Writes `line` to stderr, through the tell gate. */
  readonly stderr: (line: string) => void
  /** The write gate: whether a role record or a ledger entry may be written now. False once the
   *  stop has begun. */
  readonly writes: () => boolean
}

/** The stop's own stderr lines, each by how it begins: the only lines the tell gate lets through
 *  once the stop has begun. The stop's DELETE that failed is the one. */
export const STOP_LINES: readonly string[] = ["envoy-channel: session deregistration failed — "]

/** The gates of one stop reading, `stopping`: a session's, or its server's. */
export function createStopGates(stopping: () => boolean): StopGates {
  const tells = (line: string): boolean =>
    !stopping() || STOP_LINES.some((start) => line.startsWith(start))
  return {
    tells,
    stderr: (line) => {
      if (tells(line)) process.stderr.write(line)
    },
    writes: () => !stopping(),
  }
}
