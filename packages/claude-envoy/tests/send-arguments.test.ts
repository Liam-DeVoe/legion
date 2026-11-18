import { expect, test } from "bun:test"

test("parses a destination and a multiword message", async () => {
  // given
  const module = await import("../src/send-arguments")
  const candidate = Reflect.get(module, "parseSendArguments")

  // when
  const output =
    typeof candidate === "function" ? candidate(["ses_receiver", "ready", "for", "QA"]) : undefined

  // then
  expect(output).toEqual({ targetSession: "ses_receiver", message: "ready for QA" })
})

test("takes the key an unconfirmed send named, ahead of the target", async () => {
  const { parseSendArguments } = await import("../src/send-arguments")
  expect(
    parseSendArguments(["--idempotency-key", "k-1", "ses_receiver", "ready", "for", "QA"]),
  ).toEqual({ targetSession: "ses_receiver", message: "ready for QA", idempotencyKey: "k-1" })
  // A message whose words begin with the flag's name, after the target, is the message.
  expect(parseSendArguments(["ses_receiver", "--idempotency-key", "x"])).toEqual({
    targetSession: "ses_receiver",
    message: "--idempotency-key x",
  })
  expect(() => parseSendArguments(["--idempotency-key"])).toThrow()
})

test("the resend command quotes each word for a POSIX shell", async () => {
  const { resendCommand } = await import("../src/send-arguments")
  expect(resendCommand("/p/envoy-send.ts", "k-1", "ses_receiver", "it's here; done")).toBe(
    "bun /p/envoy-send.ts --idempotency-key k-1 ses_receiver 'it'\\''s here; done'",
  )
})
