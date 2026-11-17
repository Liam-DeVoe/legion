import { afterEach, expect, test } from "bun:test";
import { randomBytes } from "node:crypto";
import { holdSecret } from "@legion/contracts/held-secrets";
import { createEnvoyClient } from "@legion/envoy-client/transport";
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

// Bun's console prints an error's own fields beside its message: an `EnvoyApiError` holds the
// listener's raw body in `details.responseBody`, which is scrubbed with the message (#4140, R-b).
test("an EnvoyApiError whose body echoes a held value prints none of it", async () => {
  const held = holdSecret(`dsp_${randomBytes(24).toString("base64url")}`);
  const client = createEnvoyClient({
    baseUrl: "http://listener.test",
    fetch: (async () =>
      new Response(`no session holds ${held}`, { status: 404 })) as unknown as Parameters<
      typeof createEnvoyClient
    >[0]["fetch"],
    retry: false,
  });
  const failure = await client.getInterest("ses_scrub").then(
    () => undefined,
    (error: unknown) => error
  );
  const printed: string[] = [];
  console.warn = (...args: unknown[]) => {
    printed.push(args.map((arg) => Bun.inspect(arg)).join(" "));
  };
  scrubbedConsole.warn("[envoy] failed to deliver envelope", failure);
  expect(printed.join("\n")).toContain("responseBody");
  expect(printed.join("\n")).not.toContain(held.slice(4, 18));
});
