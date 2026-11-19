import { describe, expect, test } from "bun:test"
import { existsSync, mkdirSync, readdirSync } from "node:fs"
import { join } from "node:path"
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories"
import { span, until } from "@legion/test-fixtures/wait"
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

/** Waits until `directory`'s inbox holds a waiting line: the write in flight has put it there. */
async function lineWaiting(directory: string): Promise<void> {
  const waiting = join(directory, "waiting")
  await until(
    "the line waiting in the inbox",
    () => existsSync(waiting) && readdirSync(waiting).length > 0,
  )
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
    // What the delivery had told of the hook's take when the write resolved: a write resolves
    // only once the hook took its line, and the take is told before it, so a write that resolved
    // while the line still waited, or through `codex queue`, finds nothing told.
    let takenWhenWritten: CodexInboxItem[] | undefined
    const written = delivery.deliver({ content: "mid-turn", dispatchMessageId: "m1" }).then(() => {
      takenWhenWritten = [...taken]
    })
    await lineWaiting(directory)
    expect((await takeInbox(directory, "hook")).map(({ item }) => item.content)).toEqual([
      "mid-turn",
    ])
    await written
    expect(takenWhenWritten).toEqual([{ content: "mid-turn", dispatchMessageId: "m1" }])
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
    await lineWaiting(directory)
    // No poll queues it while the turn runs. The poll is the delivery's own timer: the gate it
    // reads, `turnRunning()`, says what it should do and not what it did, a poll that leaves the
    // line waiting tells this test nothing, and no later action's effect is ordered after a poll's
    // sweep, which starts only once its check is done.
    await span(
      30,
      "a poll that leaves the line waiting tells nothing, and its sweep starts after its check " +
        "with no effect a later action could be ordered behind: three polls of 10 ms run in it",
    )
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
    const directory = inbox()
    const busy = harness(directory, true)
    busy.delivery.turnStarted()
    const written = busy.delivery.deliver({ content: "y" })
    // The refusal comes from the sweep the turn's end runs, which can settle the write before
    // `turnEnded` returns: a handler from now on keeps it from counting as unhandled meanwhile.
    written.catch(() => undefined)
    await lineWaiting(directory)
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
    await lineWaiting(codexInboxDirectory(base, "old"))
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
    await lineWaiting(directory)
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
    await lineWaiting(directory)
    expect(readdirSync(join(directory, "waiting"))).toHaveLength(1)
    const stopped = delivery.stop()
    await expect(written).rejects.toThrow("stopped before the message was taken")
    // `stop` resolves once every waiting line is withdrawn or found taken.
    await stopped
    expect(readdirSync(join(directory, "waiting"))).toEqual([])
    expect(await takeInbox(directory, "hook")).toEqual([])
  })
})
