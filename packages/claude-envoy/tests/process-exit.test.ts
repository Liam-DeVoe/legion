// The channel server's process ends by exitProcess once its run returns (bin/envoy-channel.ts): a
// handle nothing will use can outlive the run, such as a dial a stop ended. What it already wrote
// to stdout (an MCP reply, which Claude Code reads there) and to stderr must reach the reader all
// the same, even when the reader is slow and the pipe is full.
import { expect, test } from "bun:test"
import { existsSync } from "node:fs"
import { join, resolve } from "node:path"
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories"
import { settled, until } from "@legion/test-fixtures/wait"

const helper = resolve(import.meta.dir, "..", "src", "process-exit.ts")
const MIB = 1024 * 1024
const scratch = temporaryDirectory("claude-envoy-process-exit")

test("a process ended by exitProcess first writes out everything queued on stdout and stderr", async () => {
  // The child says when it has queued its output and reached exitProcess, with nobody reading
  // its pipes yet: the pipe is full, and the rest waits in the writer.
  const queued = join(scratch, "queued")
  const script = `
    import { writeFileSync } from "node:fs"
    import { exitProcess } from ${JSON.stringify(helper)}
    process.stdout.write("o".repeat(${MIB}))
    process.stderr.write("e".repeat(${MIB}))
    // A handle nothing will use, as a dial a stop ended is, and one that never ends: the exit
    // must not wait for it, so a child that exits at all did not.
    setInterval(() => undefined, 60_000)
    writeFileSync(${JSON.stringify(queued)}, "")
    await exitProcess(0)
  `
  const child = Bun.spawn(["bun", "-e", script], { stdout: "pipe", stderr: "pipe" })
  // A reader that starts only once the child has queued what it wrote and called exitProcess.
  await until("the child's output queued, and exitProcess called", () => existsSync(queued))
  const [out, err, code] = await settled(
    "the child's output read whole and its exit",
    Promise.all([
      new Response(child.stdout).text(),
      new Response(child.stderr).text(),
      child.exited,
    ]),
  )
  expect(code).toBe(0)
  expect(out.length).toBe(MIB)
  expect(err.length).toBe(MIB)
})
