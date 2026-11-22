import { expect, test } from "bun:test"
import { mkdtemp, rm } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join, resolve } from "node:path"
import { consumerDelivery } from "@legion/envoy-client/test/subscribe-answer"
import { loopbackHost } from "@legion/test-fixtures/loopback-host"
import { readStandinBody } from "@legion/test-fixtures/standin-body"
import { FakeNatsServer } from "./fake-nats-server"
import { PROCESS_WAIT_MS, ROW_TIMEOUT_MS } from "./reopen-server"

const entry = resolve(import.meta.dir, "..", "bin", "envoy-channel.ts")

/**
 * `promise`, or a failure naming `what` once `PROCESS_WAIT_MS` has passed without it: a hang
 * guard only, far above what a whole server process takes on a loaded machine, so the fact the
 * row waits for decides it, never this bound. A real timer on purpose: what it guards is another
 * process, which no fake clock moves.
 */
async function within<T>(promise: Promise<T>, what: string): Promise<T> {
  const { promise: guard, reject } = Promise.withResolvers<never>()
  const timer = setTimeout(
    () => reject(new Error(`timed out after ${PROCESS_WAIT_MS} ms waiting for ${what}`)),
    PROCESS_WAIT_MS,
  )
  try {
    return await Promise.race([promise, guard])
  } finally {
    clearTimeout(timer)
  }
}

test(
  "a hangup from the Claude process group deregisters the session before the server exits",
  async () => {
    // tmux kill-session (and a closing terminal) hang up the whole process group;
    // the channel server shares it with `claude`, so SIGHUP arrives directly and
    // must still lead to `DELETE /v1/sessions/<id>` rather than a silent death
    // that leaves the session live in the registry until its TTL.
    const broker = new FakeNatsServer()
    const calls: string[] = []
    const registered = Promise.withResolvers<void>()
    // The stand-in listens at an address of its own, out of reach on Linux of a client of another
    // test that still dials its port at 127.0.0.1, and refuses by name a POST body that is no JSON
    // object before it records the request (`loopbackHost`, `readStandinBody`).
    const listener = Bun.serve({
      port: 0,
      hostname: loopbackHost(),
      fetch: async (request) => {
        const read = await readStandinBody(request)
        if (read.refusal !== undefined) return read.refusal
        const url = new URL(request.url)
        calls.push(`${request.method} ${url.pathname}`)
        if (request.method === "POST" && url.pathname === "/v1/interests/subscribe") {
          const body = read.body as { session_id: string; topics: string[] }
          registered.resolve()
          return Response.json({
            session_id: body.session_id,
            machine_id: "test",
            dir: "/tmp",
            topics: body.topics,
            // Every stand-in listener answers the session's own consumer
            // (`@legion/envoy-client/test/subscribe-answer`): one that answers no delivery leaves
            // its session refused inbound, which is a state of its own and not this case's.
            delivery: consumerDelivery(body.session_id),
          })
        }
        if (request.method === "DELETE") return new Response(null, { status: 200 })
        return Response.json({ error: "not found" }, { status: 404 })
      },
    })
    const stateDirectory = await mkdtemp(join(tmpdir(), "claude-envoy-lifecycle-"))
    const server = Bun.spawn(["bun", entry], {
      cwd: stateDirectory,
      env: {
        PATH: process.env["PATH"] ?? "",
        HOME: stateDirectory,
        ENVOY_NATS_URL: broker.url,
        ENVOY_URL: listener.url.origin,
        CLAUDE_CODE_SESSION_ID: "ses_hangup",
        CLAUDE_PLUGIN_DATA: stateDirectory,
      },
      stdin: "pipe",
      stdout: "pipe",
      stderr: "pipe",
    })

    try {
      await within(registered.promise, "the server's registration")
      server.kill("SIGHUP")
      const exitCode = await within(server.exited, "the server's exit after the hangup")

      expect(exitCode).toBe(0)
      expect(calls).toContain("DELETE /v1/sessions/ses_hangup")
    } finally {
      server.kill("SIGKILL")
      listener.stop(true)
      await broker.stop()
      await rm(stateDirectory, { recursive: true, force: true })
    }
    // A whole server process, started and ended on a machine that may be loaded: the row ends on
    // the facts above, each under its own hang guard, and its timeout is only above their chain
    // (`ROW_TIMEOUT_MS`). A fixed 20 s here decided the row at a load of about 2,400.
  },
  ROW_TIMEOUT_MS,
)
