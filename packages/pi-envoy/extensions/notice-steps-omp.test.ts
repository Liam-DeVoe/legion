import { afterEach, expect, test } from "bun:test";
import * as path from "node:path";
import { envoyToolSpecs } from "@legion/envoy-client/tool-contract";
import {
  type Cleanup,
  deliveryFor,
  messageStream,
  ompRoot,
  type Request,
  serveStandin,
  spawnRpc,
  writeStandinProfile,
} from "@legion/pi-shared/test/omp-harness";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";

// dispatch://INFRA-3686 R2, and the architect's ruling that
// each step of Envoy's own notices is in the description of the tool it names). ae79500d took the
// step out of each of Envoy's own notices and put it in envoy_inbox's and envoy_whoami's
// descriptions (`ENVOY_NOTICE_STEPS`, packages/envoy-client/src/tool-contract.ts). The Oh My Pi
// that .omp-pin names mounts an extension's tool that names no loadMode as an xd:// device
// (`tools.xdev`, on by default), and its system prompt gives each device the first line of its
// description cut to 200 bytes (`tools.xdevDocs`, `catalog` by default; an inline device's
// description is cut at 200 too). Legion sets neither (internal/podsafety writes no settings
// overlay; deploy/kubernetes/operator-route/overlay.yml sets no tools key). envoy_inbox's steps
// start 78 characters in and envoy_whoami's 330, so of the six notices' steps only the gap
// notice's reaches what the model is sent: a session told `notice: Envoy inbound unavailable`,
// `Envoy inbound restored`, `unread messages`, `message not shown` or `session id changed` has
// the step neither in the notice nor in its prompt. R2: "a step that a text gives today goes into
// the reader's prompt where the prompt does not give it".
// LEGION_TEST_OMP names the binary and LEGION_TEST_NATS_URL the NATS server; without both the
// case skips.
const omp = process.env.LEGION_TEST_OMP;
const nats = process.env.LEGION_TEST_NATS_URL;

const cleanup: Cleanup = [];
afterEach(async () => {
  for (const step of cleanup.splice(0).reverse()) await step();
});

/** The step sentences of Envoy's own notices, as the two descriptions give them. */
const STEPS = ["envoy_inbox", "envoy_whoami"].flatMap((name) => {
  const description = envoyToolSpecs.find((spec) => spec.name === name)?.description ?? "";
  return description
    .split(/(?<=\.) (?=On notice: )/)
    .filter((sentence) => sentence.startsWith("On notice: "));
});

test.skipIf(omp === undefined || nats === undefined)(
  "every step of Envoy's own notices reaches the model a session sends its first turn to",
  async () => {
    expect(STEPS).toHaveLength(6);
    const binary = omp ?? "";
    const { home, workspace, sessions } = await ompRoot(binary, "notice-steps-omp-", cleanup);
    const { requests, base } = serveStandin(cleanup, async (url, body) => {
      if (url.pathname === "/anthropic/v1/messages") {
        return new Response(messageStream([{ type: "text", text: "Read." }], "msg_0"), {
          headers: { "content-type": "text/event-stream" },
        });
      }
      if (url.pathname.startsWith("/anthropic/")) return Response.json({ data: [] });
      if (url.pathname === "/api/v1/asks/open") return new Response("unavailable", { status: 503 });
      const answers = /^\/api\/v1\/agents\/([^/]+)\/answers$/.exec(url.pathname);
      if (answers !== null) {
        return Response.json({
          session_id: decodeURIComponent(answers[1] ?? ""),
          since: new Date(Date.now() - 72 * 3_600_000).toISOString(),
          total: 0,
          items: [],
        });
      }
      const id = typeof body.session_id === "string" ? body.session_id : "";
      const interest = { session_id: id, machine_id: "notice-steps-machine", dir: workspace };
      return Response.json({ ...interest, topics: [], ...deliveryFor(url.pathname, body, id) });
    });
    await writeStandinProfile(home, base);
    const rpc = spawnRpc(
      binary,
      {
        extensions: [path.join(import.meta.dir, "envoy.ts")],
        home,
        workspace,
        sessions,
        env: {
          ENVOY_URL: base,
          ENVOY_NATS_URL: nats ?? "",
          DISPATCH_URL: base,
          DISPATCH_TOKEN: fixtureSecret("dsp"),
        },
        onFrame: () => undefined,
      },
      cleanup
    );
    // A real binary's startup and first request are awaited by polling what the stand-in recorded,
    // as card-delivery-omp.test.ts's `until` does: neither gives a promise to await.
    const registered = async () => {
      for (const deadline = Date.now() + 120_000; Date.now() < deadline; ) {
        if (requests.some((request) => request.path === "/v1/interests/subscribe")) return;
        await Bun.sleep(50);
      }
      throw new Error("the session never registered with the listener");
    };
    await Promise.race([registered(), rpc.closed]);
    rpc.send({ type: "prompt", message: "Go on." });
    const turn = async (): Promise<Request> => {
      for (const deadline = Date.now() + 60_000; Date.now() < deadline; ) {
        const sent = requests.find((request) => request.path === "/anthropic/v1/messages");
        if (sent !== undefined) return sent;
        await Bun.sleep(50);
      }
      throw new Error("the session sent the model no turn");
    };
    const sent = await Promise.race([turn(), rpc.closed]);
    // All the model is sent of the session's own setup: the system prompt and the tools.
    const prompt = JSON.stringify({ system: sent.body.system, tools: sent.body.tools });
    const shown = JSON.stringify(sent.body.system)
      .split("\\n")
      .filter((line) => /xd:\/\/envoy_(inbox|whoami)\b/.test(line))
      .join("\n");

    expect(prompt).toContain("envoy_inbox");
    expect(
      STEPS.filter((step) => !prompt.includes(JSON.stringify(step).slice(1, -1))),
      shown
    ).toEqual([]);
  },
  240_000
);
