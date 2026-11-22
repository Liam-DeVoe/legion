import { expect, spyOn, test } from "bun:test"
import { createStopGates } from "../src/stop-gates"

test("once the stop has begun, a stderr line of the server's own is dropped, the stop's own failed DELETE is written, and nothing may be recorded", () => {
  let stopping = false
  const gates = createStopGates(() => stopping)
  const written: string[] = []
  const stderr = spyOn(process.stderr, "write").mockImplementation((chunk) => {
    written.push(String(chunk))
    return true
  })
  try {
    gates.stderr("envoy-channel: before the stop\n")
    expect(gates.writes()).toBe(true)
    stopping = true
    gates.stderr("envoy-channel: registry heartbeat failed (x); retrying at the next heartbeat\n")
    gates.stderr("envoy-channel: session deregistration failed — the listener did not answer\n")

    expect(written).toEqual([
      "envoy-channel: before the stop\n",
      "envoy-channel: session deregistration failed — the listener did not answer\n",
    ])
    expect(gates.tells("Envoy inbound unavailable")).toBe(false)
    expect(gates.writes()).toBe(false)
  } finally {
    stderr.mockRestore()
  }
})

test("once the stop has begun, the stop's own failed drain and close of its connection are written", () => {
  const gates = createStopGates(() => true)

  expect(gates.tells("envoy-channel: draining the broker connection failed — timeout\n")).toBe(true)
  expect(gates.tells("envoy-channel: closing the broker connection failed — closed\n")).toBe(true)
  expect(gates.tells("envoy-channel: a delivery failed — the mod link closed\n")).toBe(false)
})
