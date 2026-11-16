import { runEnvoyChannelServer } from "../src/envoy-channel-server"

try {
  await runEnvoyChannelServer()
} catch (error) {
  const message = error instanceof Error ? error.message : "unknown channel server failure"
  // The server's own teardown has run (its `finally`): the mod link is closed and the session, if
  // one started, deregistered. A failed start ends the process, rather than leaving it alive on
  // whatever handle is left (its stdin, a timer) with no session behind it; the exit waits for
  // the message to be written.
  process.stderr.write(`Claude Envoy channel server failed: ${message}\n`, () => process.exit(1))
}
