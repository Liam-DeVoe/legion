import { runEnvoyChannelServer } from "../src/envoy-channel-server"

try {
  await runEnvoyChannelServer()
  // The server's teardown has run. A handle nothing will use can outlive it (a NATS dial that a
  // stop ended waits out nats.js's own dial timeout), so the process ends here rather than when
  // that handle lets go; the timer keeps nothing alive when nothing else does.
  setTimeout(() => process.exit(0), 0).unref()
} catch (error) {
  const message = error instanceof Error ? error.message : "unknown channel server failure"
  // The server's own teardown has run (its `finally`): the mod link is closed and the session, if
  // one started, deregistered. A failed start ends the process, rather than leaving it alive on
  // whatever handle is left (its stdin, a timer) with no session behind it; the exit waits for
  // the message to be written.
  process.stderr.write(`Claude Envoy channel server failed: ${message}\n`, () => process.exit(1))
}
