import { expect, test } from "bun:test";
import { projectRefusal } from "@legion/envoy-client/test/subscribe-answer";
import {
  createPi,
  responseWithRegistration,
  sessionContext,
  settle,
  until,
  useExtensionHarness,
} from "../test/extension-harness";
import type { SessionContext } from "../test/stand-in-host";

// A stopping session registers nothing and is told nothing (R24 of dispatch://INFRA-3589), also
// where a registration it sent before its stop is answered only after the stop began
// (`registerWithRelay`'s `stopping`). The row drives the extension through the harness that
// `envoy.test.ts` uses. It lives in a file of its own, since `envoy.test.ts` is close to the
// file size above which Biome checks nothing in it.

useExtensionHarness();

// A heartbeat's registration is on the wire when the session's stop begins, and only then does
// an edge listener answer it a project refusal: the rest is not registered again, and the
// session is told nothing of the refused topic, in its transcript or in its pane.
test("a project refusal answered after the stop began registers nothing again and tells nothing", async () => {
  const refusedRoot = "notifications.github.acme.priv";
  let holding = false;
  const held: (() => void)[] = [];
  const registrations: string[][] = [];
  globalThis.fetch = async (input, init) => {
    const url = new URL(input.toString());
    const body = init?.body === undefined ? undefined : JSON.parse(init.body.toString());
    if (url.pathname === "/v1/interests/subscribe") {
      const topics: string[] = Array.isArray(body?.topics) ? body.topics : [];
      registrations.push(topics);
      const refused = topics.filter((topic) => topic.startsWith(refusedRoot));
      if (holding && refused.length > 0) {
        // Answered only once the test lets it go, after the stop began.
        const { promise, resolve } = Promise.withResolvers<void>();
        held.push(resolve);
        await promise;
        return Response.json(projectRefusal(refused), { status: 403 });
      }
    }
    return responseWithRegistration(input, init, {});
  };
  // A fresh instance of the entry, as each row of `envoy.test.ts` loads: the query makes it a
  // module of its own, with state no other row shares, which a static import cannot give.
  const { default: envoyExtension } = await import("./envoy.ts?refused-while-stopping");
  const fixture = createPi();
  const intervals: (() => void)[] = [];
  const notifications: string[] = [];
  const base = sessionContext("ses_refused_while_stopping");
  const context: SessionContext = {
    ...base,
    setInterval: (callback) => intervals.push(callback),
    ui: { ...base.ui, notify: (message) => notifications.push(message) },
  };
  envoyExtension(fixture.pi);
  await fixture.handlers.get("session_start")?.({}, context);
  const subscribeTool = fixture.tools.find((tool) => tool.name === "envoy_subscribe");
  if (subscribeTool === undefined) throw new Error("envoy_subscribe was not registered");

  try {
    const subscribed = await subscribeTool.execute("", {
      topics: [`${refusedRoot}.>`, "notifications.dispatch.issue.PRIV-1.>"],
    });
    expect(subscribed.isError).toBeFalsy();
    holding = true;
    intervals[0]?.();
    await until(() => held.length === 1, "the heartbeat's registration on the wire", 10_000);
    const heldAt = registrations.length;

    const stopping = fixture.handlers.get("session_shutdown")?.({}, context);
    for (const answer of held.splice(0)) answer();
    await stopping;
    await settle();

    expect(registrations.slice(heldAt)).toEqual([]);
    expect(
      fixture.entries
        .map((entry) => ("data" in entry ? entry.data : undefined))
        .map((data) =>
          typeof data === "object" &&
          data !== null &&
          "content" in data &&
          typeof data.content === "string"
            ? data.content
            : ""
        )
        .filter((content) => content.includes(refusedRoot))
    ).toEqual([]);
    expect(notifications.filter((message) => message.includes(refusedRoot))).toEqual([]);
  } finally {
    for (const answer of held.splice(0)) answer();
    await fixture.handlers.get("session_shutdown")?.({}, context);
  }
});
