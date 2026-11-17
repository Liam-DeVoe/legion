import { afterEach, expect, test } from "bun:test";
import { randomBytes } from "node:crypto";
import { holdSecret } from "@legion/contracts/held-secrets";
import { scrubbedConsole } from "./scrubbed-host";

// The extension's own console prints (`[envoy] failed to deliver envelope …`, with the error that
// failed it) go through the scrub of the secrets this process holds: Bun's console writes past any
// stream wrapper, so a held value in their text or in the error prints as `<withheld>`.
const warn = console.warn;
afterEach(() => {
  console.warn = warn;
});

test("a held value in a warning's text and its error prints as <withheld>", () => {
  const held = holdSecret(`dsp_${randomBytes(24).toString("base64url")}`);
  const printed: string[] = [];
  console.warn = (...args: unknown[]) => {
    printed.push(args.map((arg) => Bun.inspect(arg)).join(" "));
  };
  scrubbedConsole.warn(
    `[envoy] failed to deliver envelope ev-${held.slice(4, 18)}`,
    new Error(`the listener answered ${held}`)
  );
  const line = printed.join("\n");
  expect(line).toContain("[envoy] failed to deliver envelope ev-<withheld>");
  expect(line).toContain("the listener answered <withheld>");
  expect(line).not.toContain(held.slice(4, 18));
});

test("a warning that holds no held value prints as it was", () => {
  holdSecret(`dsp_${randomBytes(24).toString("base64url")}`);
  const printed: unknown[][] = [];
  console.warn = (...args: unknown[]) => {
    printed.push(args);
  };
  const error = new Error("the listener did not answer");
  scrubbedConsole.warn("[envoy] failed to deliver envelope ev_1", error);
  expect(printed).toEqual([["[envoy] failed to deliver envelope ev_1", error]]);
});
