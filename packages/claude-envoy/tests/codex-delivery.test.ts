import { describe, expect, test } from "bun:test"
import { existsSync, mkdirSync, readdirSync } from "node:fs"
import { join } from "node:path"
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories"
import { createCodexDelivery } from "../src/codex/codex-delivery"
import {
  type CodexInboxItem,
  codexInboxDirectory,
  discardWaiting,
  HOOK_CONTEXT_MAX_BYTES,
  inboxContext,
  putInbox,
  takeInbox,
  takenBy,
} from "../src/codex/codex-inbox"

const root = temporaryDirectory("codex-delivery")
let count = 0
/** A fresh inbox directory for one test. */
function inbox(): string {
  const directory = codexInboxDirectory(join(root, String(count++)), "session/1")
  mkdirSync(directory, { recursive: true })
  return directory
}

/** The lines waiting in `directory`, none before its first line made the waiting directory. */
function waitingLines(directory: string): string[] {
  const waiting = join(directory, "waiting")
  return existsSync(waiting) ? readdirSync(waiting) : []
}

/**
 * Waits until `ready()` holds, looking every 5 ms, and fails naming `what` when it does not within
 * 2 s. A row waits so for a file operation the delivery runs on its own, such as the save of a
 * line, which hands the row no promise to await. A real clock, because a fake one does not
 * advance file operations; 2 s is generous for a pod whose disk and CPU other suites share, and a
 * condition that holds ends the wait at once.
 */
async function waitFor(what: string, ready: () => boolean): Promise<void> {
  const deadline = Date.now() + 2000
  while (!ready()) {
    if (Date.now() >= deadline) throw new Error(`waited 2 s for ${what}`)
    await Bun.sleep(5)
  }
}

describe("the Codex inbox", () => {
  test("a message is taken once, by whichever taker renames it first, oldest first", async () => {
    const directory = inbox()
    const first = await putInbox(directory, { content: "one" })
    const second = await putInbox(directory, { content: "two", eventId: "ev-2" })
    expect(await takenBy(directory, first)).toBeUndefined()
    const hook = await takeInbox(directory, "hook")
    expect(hook.map(({ item }) => item)).toEqual([
      { content: "one" },
      { content: "two", eventId: "ev-2" },
    ])
    expect(await takeInbox(directory, "server")).toEqual([])
    expect(await takenBy(directory, first)).toBe("hook")
    expect(await takenBy(directory, second)).toBe("hook")
  })

  test("a hook takes only what fits under Codex's cap, and leaves the rest waiting in order", async () => {
    const directory = inbox()
    await putInbox(directory, { content: "short one" })
    const long = await putInbox(directory, { content: "x".repeat(HOOK_CONTEXT_MAX_BYTES) })
    const after = await putInbox(directory, { content: "short two" })
    const hook = await takeInbox(directory, "hook", HOOK_CONTEXT_MAX_BYTES)
    expect(hook.map(({ item }) => item.content)).toEqual(["short one"])
    expect(Buffer.byteLength(inboxContext(hook.map(({ item }) => item)))).toBeLessThanOrEqual(
      HOOK_CONTEXT_MAX_BYTES,
    )
    // The long message and the one behind it stay for `codex queue`, which takes any length.
    expect(await takenBy(directory, long)).toBeUndefined()
    expect(await takenBy(directory, after)).toBeUndefined()
    const server = await takeInbox(directory, "server")
    expect(server.map(({ item }) => item.content)).toEqual([
      "x".repeat(HOOK_CONTEXT_MAX_BYTES),
      "short two",
    ])
  })

  test("a server that binds drops the lines an earlier one left, except those it waits on", async () => {
    const directory = inbox()
    const left = await putInbox(directory, { content: "left behind" })
    const mine = await putInbox(directory, { content: "this process's" })
    expect(await discardWaiting(directory, (name) => name === mine)).toBe(1)
    expect((await takeInbox(directory, "hook")).map(({ name }) => name)).toEqual([mine])
    expect(await takenBy(directory, left)).toBeUndefined()
    expect(await discardWaiting(join(root, "missing"))).toBe(0)
  })

  test("the session id is one path segment, whatever it holds", () => {
    expect(codexInboxDirectory("/state", "a/../b")).toBe("/state/codex-inbox/a%2F..%2Fb")
  })

  test("an empty or missing inbox yields nothing", async () => {
    expect(await takeInbox(join(root, "missing"), "hook")).toEqual([])
  })

  test("the hook's context quotes every message whole, in order", () => {
    expect(inboxContext([{ content: "one" }, { content: "two\nlines" }])).toBe(
      "Messages delivered to this session while it worked (Envoy, through the Envoy plugin):\n\none\n\ntwo\nlines",
    )
  })
})

describe("createCodexDelivery", () => {
  function harness(directory: string, failQueue = false) {
    const queued: string[] = []
    const taken: CodexInboxItem[] = []
    const delivery = createCodexDelivery({
      inboxDirectory: () => directory,
      sessionId: () => "session/1",
      pollMs: 10,
      queue: async (text) => {
        if (failQueue) throw new Error("codex queue exited 1")
        queued.push(text)
      },
      taken: (item) => taken.push(item),
      warn: () => undefined,
    })
    return { delivery, queued, taken }
  }

  test("an idle session takes a line through codex queue at once", async () => {
    const { delivery, queued } = harness(inbox())
    await delivery.deliver({ content: "hello" })
    expect(queued).toEqual(["hello"])
    delivery.stop()
  })

  test("during a turn the line waits until the hook takes it, and is never queued as well", async () => {
    const directory = inbox()
    const { delivery, queued, taken } = harness(directory)
    delivery.turnStarted()
    let done = false
    const written = delivery.deliver({ content: "mid-turn", dispatchMessageId: "m1" }).then(() => {
      done = true
    })
    await waitFor("the line in waiting/", () => waitingLines(directory).length === 1)
    // A fixed sleep, because the row asserts the write has not resolved: three of the timer's
    // 10 ms looks, while nothing took the line.
    await Bun.sleep(30)
    expect(done).toBe(false)
    expect((await takeInbox(directory, "hook")).map(({ item }) => item.content)).toEqual([
      "mid-turn",
    ])
    await written
    expect(taken).toEqual([{ content: "mid-turn", dispatchMessageId: "m1" }])
    await delivery.turnEnded()
    expect(queued).toEqual([])
    delivery.stop()
  })

  test("a line no tool call took goes to codex queue when the turn ends", async () => {
    const directory = inbox()
    const { delivery, queued, taken } = harness(directory)
    delivery.turnStarted()
    const written = delivery.deliver({ content: "late" })
    await waitFor("the line in waiting/", () => waitingLines(directory).length === 1)
    // A fixed sleep, because the row asserts nothing was queued yet: two of the timer's 10 ms
    // looks during the turn.
    await Bun.sleep(20)
    expect(queued).toEqual([])
    await delivery.turnEnded()
    await written
    expect(queued).toEqual(["late"])
    expect(taken).toEqual([])
    delivery.stop()
  })

  test("a codex queue failure rejects the write, so the frame is sent again", async () => {
    const idle = harness(inbox(), true)
    await expect(idle.delivery.deliver({ content: "x" })).rejects.toThrow("codex queue exited 1")
    idle.delivery.stop()
    const busyDirectory = inbox()
    const busy = harness(busyDirectory, true)
    busy.delivery.turnStarted()
    const written = busy.delivery.deliver({ content: "y" })
    // The 10 ms timer's sweep can reject the write before `turnEnded` returns, and Bun fails a
    // test on a rejection that has no handler yet. The handler goes on now, and the expectation
    // below still sees the rejection: `expect(...).rejects` cannot go here, since it blocks until
    // the promise settles, and nothing settles this one before the turn ends.
    written.catch(() => undefined)
    await waitFor("the line in waiting/", () => waitingLines(busyDirectory).length === 1)
    await busy.delivery.turnEnded()
    await expect(written).rejects.toThrow("codex queue exited 1")
    busy.delivery.stop()
  })

  test("after /new a line waiting for the old session is queued for the new one", async () => {
    const base = join(root, String(count++))
    let session = "old"
    const queued: string[] = []
    const delivery = createCodexDelivery({
      inboxDirectory: (id) => codexInboxDirectory(base, id),
      pollMs: 10,
      queue: async (text) => {
        queued.push(`${session}:${text}`)
      },
      sessionId: () => session,
      warn: () => undefined,
    })
    delivery.turnStarted()
    const written = delivery.deliver({ content: "waiting" })
    await waitFor(
      "the line in the old session's waiting/",
      () => waitingLines(codexInboxDirectory(base, "old")).length === 1,
    )
    session = "new"
    await delivery.rebind()
    await written
    expect(queued).toEqual(["new:waiting"])
    delivery.stop()
  })

  test("a sweep that fails is tried again, and the write still settles", async () => {
    const directory = inbox()
    const queued: string[] = []
    const warnings: string[] = []
    let failures = 1
    const delivery = createCodexDelivery({
      inboxDirectory: () => directory,
      pollMs: 10,
      queue: async (text) => {
        queued.push(text)
      },
      sessionId: () => "session/1",
      warn: (text) => warnings.push(text),
    })
    delivery.turnStarted()
    const written = delivery.deliver({ content: "late" })
    await waitFor("the line in waiting/", () => waitingLines(directory).length === 1)
    // The first sweep finds a file where the server's directory should be and fails.
    const blocker = join(directory, "queued")
    await Bun.write(blocker, "not a directory")
    await delivery.turnEnded()
    expect(warnings.some((text) => text.includes("could not be swept"))).toBe(true)
    expect(queued).toEqual([])
    failures -= 1
    await Bun.file(blocker).delete()
    await written
    expect(queued).toEqual(["late"])
    expect(failures).toBe(0)
    delivery.stop()
  })

  test("after SessionEnd every write is refused until the next session binds", async () => {
    const { delivery, queued } = harness(inbox())
    delivery.sessionEnded()
    await expect(delivery.deliver({ content: "to nobody" })).rejects.toThrow("session ended")
    expect(queued).toEqual([])
    await delivery.rebind()
    await delivery.deliver({ content: "to the next session" })
    expect(queued).toEqual(["to the next session"])
    // A rebind to a session that has already ended stays refused, and a resume takes writes again.
    await delivery.rebind(true)
    await expect(delivery.deliver({ content: "into an ended session" })).rejects.toThrow("ended")
    await delivery.sessionResumed()
    await delivery.deliver({ content: "after /resume" })
    expect(queued).toEqual(["to the next session", "after /resume"])
    await delivery.stop()
  })

  test("stopping rejects a line still waiting and removes it, so Envoy's resend is its one copy", async () => {
    const directory = inbox()
    const { delivery } = harness(directory)
    delivery.turnStarted()
    const written = delivery.deliver({ content: "never" })
    await waitFor("the line in waiting/", () => waitingLines(directory).length === 1)
    expect(readdirSync(join(directory, "waiting"))).toHaveLength(1)
    delivery.stop()
    await expect(written).rejects.toThrow("stopped before the message was taken")
    await waitFor("waiting/ to be empty", () => waitingLines(directory).length === 0)
    expect(readdirSync(join(directory, "waiting"))).toEqual([])
    expect(await takeInbox(directory, "hook")).toEqual([])
  })

  test("stopping while a line is still being saved rejects its write and removes the line, so Envoy's resend is its one copy", async () => {
    const directory = inbox()
    const { delivery } = harness(directory)
    delivery.turnStarted()
    const written = delivery.deliver({ content: "mid-save" })
    // The handler goes on at once, so a rejection cannot fail the row as unhandled before the
    // race below reads it.
    written.catch(() => undefined)
    // `deliver` has returned at its first wait, the save of the line, so `stop()` runs before the
    // save finishes and finds no write waiting yet.
    await delivery.stop()
    // A real-clock bound, because the save and the removal are real file operations, which a fake
    // clock does not advance; a write that settles ends the race at once. 2 s: those operations
    // take milliseconds, so the bound is generous for a pod whose disk and CPU other suites share,
    // and it only tells a write that never settles from a slow one.
    const settled = await Promise.race([
      written.then(
        () => "resolved",
        (error: Error) => error.message,
      ),
      Bun.sleep(2000).then(() => "unsettled"),
    ])
    expect(settled).toBe("the Codex session's delivery stopped before the message was taken")
    expect(readdirSync(join(directory, "waiting"))).toEqual([])
  })
})
