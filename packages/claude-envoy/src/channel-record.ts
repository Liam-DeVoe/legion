import { appendFileSync } from "node:fs"

/**
 * Tests only: the file the channel server appends one JSON line to for each step below, so a test
 * on the other side of the process boundary reads the bound the server chose for a step and what
 * ended it, from the server's own record rather than from a clock: the settings it dialled NATS
 * with (`dial`), each status event nats.js reported on that connection (`status`), and, on the way
 * out, each bounded wait of its shutdown with the delay it armed and which side ended it (`wait`).
 * Unset, as Claude Code runs the server, nothing is written.
 */
export const CHANNEL_RECORD_FILE = "ENVOY_CHANNEL_RECORD_FILE"

/** One line of the record. */
export type ChannelRecordLine =
  | {
      readonly dial: {
        readonly pingInterval: number | undefined
        readonly maxPingOut: number | undefined
        readonly timeout: number | undefined
        readonly reconnectTimeWait: number | undefined
        readonly reconnectJitter: number | undefined
      }
    }
  | { readonly status: string; readonly data: unknown }
  | {
      /** `drain`: the NATS drain, raced against its timeout; `settle`: the registration writes
       *  still in flight, raced against the shutdown's settle budget. */
      readonly wait: "drain" | "settle"
      readonly armedMs: number
      /** `fact`: what the wait was for came first; `bound`: the armed delay did. */
      readonly endedBy: "fact" | "bound"
    }

/** Appends `line` to the record; never throws. */
export type ChannelRecord = (line: ChannelRecordLine) => void

/** The record `env` names, or undefined where it names none. */
export function channelRecord(env: Record<string, string | undefined>): ChannelRecord | undefined {
  const file = env[CHANNEL_RECORD_FILE]
  if (file === undefined || file === "") return undefined
  return (line) => {
    try {
      appendFileSync(file, `${JSON.stringify(line)}\n`)
    } catch {
      // The tests' seam only: a path it cannot write never changes what the server does.
    }
  }
}
