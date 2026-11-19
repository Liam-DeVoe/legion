import { appendFileSync } from "node:fs"

/**
 * Tests only: the file the channel server appends one JSON line to for each step below, so a test
 * on the other side of the process boundary reads the bound the server chose for a step and what
 * ended it, from the server's own record rather than from a clock: the settings it dialled NATS
 * with (`dial`), each status event nats.js reported on that connection (`status`), each
 * registration the listener answered on consumer delivery with the epoch it named (`registered`),
 * each Envoy event the session was shown (`shown`), and, on the way out, its start with the bound
 * it holds the whole way to (`shutdown`), each bounded wait of the way with the delay it armed and
 * which side ended it (`wait`), and how the way ended (`shutdown`). The lines are in the order the
 * server took the steps, which is what a test reads of when each happened. Unset, as Claude Code
 * runs the server, nothing is written.
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
      /** The epoch the registration named (`delivery.epoch`), which the listener answered on
       *  consumer delivery. */
      readonly registered: string
    }
  | {
      /** The Envoy event the session was shown (the mod's `session.message` `eventId`): no text
       *  of the message, which may be a person's own. */
      readonly shown: string
    }
  | {
      /** One bounded wait of the way out, in the order the shutdown takes them: `drain`, the NATS
       *  drain; `close`, the drained connection's close; `settle`, the registration writes still
       *  in flight, against the shutdown's settle budget; `deregister`, the DELETE of the
       *  session's record, against the whole of one listener call; `state` and `turns`, the last
       *  writes of the state and turn records; `mod`, the mod link's close; `server`, the MCP
       *  server's. */
      readonly wait:
        | "drain"
        | "close"
        | "settle"
        | "deregister"
        | "state"
        | "turns"
        | "mod"
        | "server"
      readonly armedMs: number
      /** `fact`: what the wait was for came first; `bound`: the armed delay did. */
      readonly endedBy: "fact" | "bound"
    }
  | {
      /** The signal or the end of stdin that stops the server came: the way out starts, and the
       *  process exits once `exitBoundMs` have passed, whatever is still running. */
      readonly shutdown: "began"
      readonly exitBoundMs: number
    }
  | {
      /** `completed`: every wait of the way out ended and nothing is left for the server to do;
       *  `bound`: the exit bound came first, and the process exits on it; `exited`: the
       *  process's `exit` event, the last thing it runs. */
      readonly shutdown: "completed" | "bound" | "exited"
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
