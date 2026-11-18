// The model a tmux proof's agents reach: an Anthropic Messages stand-in on loopback, so a run
// takes no model and no key from outside the runner. stage2-tmux-supervision.sh and
// clean-session-stop-tmux.sh start it, and both harnesses call it: Oh My Pi through its profile's
// anthropic provider, whose baseUrl names this server, and Claude Code through ANTHROPIC_BASE_URL.
//
//   bun scripts/e2e/lib/messages-standin.ts <port-file> <key-file> <requests-file> [<reply>]
//
// It serves on a port the kernel chose and writes that port to <port-file> once it answers, so the
// caller reads it as it reads a held port (lib/port-file.sh). Every request must carry the
// placeholder key <key-file> holds, as `x-api-key` or as an `Authorization: Bearer`, which is how
// each harness sends the key its key command printed; any other is answered 401, so a turn that
// reached the model proves the pane's key route did. It answers:
//   GET  …/v1/models                 an empty listing, as the daemon's own stand-ins do;
//   POST …/v1/messages/count_tokens  a count;
//   POST …/v1/messages               one text block, <reply> (default `ready`): streamed when the
//                                    request streams, in the events `messageStream` of
//                                    @legion/pi-shared/test/omp-harness writes for the plugins'
//                                    own Oh My Pi tests, and as one message when it does not;
// and 404 for anything else. Each request is one JSON line in <requests-file>: its method, path,
// model, whether it carried the key, and the end of its last user text, which a proof reads to
// say what the stand-in served. It runs until it is signalled.
import { appendFileSync, readFileSync, renameSync, writeFileSync } from "node:fs";
import { messageStream } from "../../../packages/pi-shared/test/omp-harness.ts";

const [portFile, keyFile, requestsFile, replyArg] = process.argv.slice(2);
if (portFile === undefined || keyFile === undefined || requestsFile === undefined) {
  process.stderr.write(
    "usage: bun scripts/e2e/lib/messages-standin.ts <port-file> <key-file> <requests-file> [<reply>]\n"
  );
  process.exit(2);
}
const key = readFileSync(keyFile, "utf8").trim();
if (key === "") {
  process.stderr.write(`messages-standin: ${keyFile} holds no key\n`);
  process.exit(2);
}
const reply = replyArg ?? "ready";

/** The text of the last user message of a Messages request, its blocks' text joined. */
function lastUserText(body: Record<string, unknown>): string {
  const messages: unknown[] = Array.isArray(body.messages) ? body.messages : [];
  for (const message of [...messages].reverse()) {
    if (typeof message !== "object" || message === null) continue;
    if (!("role" in message) || message.role !== "user") continue;
    const content = "content" in message ? message.content : undefined;
    if (typeof content === "string") return content;
    if (!Array.isArray(content)) return "";
    return content
      .map((block: unknown) =>
        typeof block === "object" &&
        block !== null &&
        "text" in block &&
        typeof block.text === "string"
          ? block.text
          : ""
      )
      .join(" ");
  }
  return "";
}

let answered = 0;
const server = Bun.serve({
  port: 0,
  hostname: "127.0.0.1",
  async fetch(request) {
    const url = new URL(request.url);
    const text = request.method === "POST" ? await request.text() : "";
    const body = text === "" ? {} : (JSON.parse(text) as Record<string, unknown>);
    // The placeholder key, in either header a harness sends it in.
    const carried =
      request.headers.get("x-api-key") === key ||
      request.headers.get("authorization") === `Bearer ${key}`;
    const model = typeof body.model === "string" ? body.model : "";
    appendFileSync(
      requestsFile,
      `${JSON.stringify({
        at: new Date().toISOString(),
        method: request.method,
        path: url.pathname,
        model,
        keyed: carried,
        text: lastUserText(body).slice(-4000),
      })}\n`
    );
    if (!carried) {
      return Response.json(
        { type: "error", error: { type: "authentication_error", message: "invalid x-api-key" } },
        { status: 401 }
      );
    }
    if (request.method === "GET" && url.pathname.endsWith("/v1/models")) {
      return Response.json({ data: [], has_more: false, first_id: null, last_id: null });
    }
    if (request.method !== "POST") {
      return Response.json(
        { type: "error", error: { type: "not_found_error", message: `no route ${url.pathname}` } },
        { status: 404 }
      );
    }
    if (url.pathname.endsWith("/v1/messages/count_tokens")) {
      return Response.json({ input_tokens: 5 });
    }
    if (!url.pathname.endsWith("/v1/messages")) {
      return Response.json(
        { type: "error", error: { type: "not_found_error", message: `no route ${url.pathname}` } },
        { status: 404 }
      );
    }
    answered += 1;
    const id = `msg_standin_${answered}`;
    if (body.stream === true) {
      return new Response(messageStream([{ type: "text", text: reply }], id), {
        headers: { "content-type": "text/event-stream" },
      });
    }
    return Response.json({
      id,
      type: "message",
      role: "assistant",
      model,
      content: [{ type: "text", text: reply }],
      stop_reason: "end_turn",
      stop_sequence: null,
      usage: { input_tokens: 5, output_tokens: 1 },
    });
  },
});
// The port is written whole, by a rename, so a reader never takes half of it.
writeFileSync(`${portFile}.tmp`, `${server.port}\n`);
renameSync(`${portFile}.tmp`, portFile);

for (const signal of ["SIGTERM", "SIGINT", "SIGHUP"] as const) {
  process.on(signal, () => {
    void server.stop(true).finally(() => process.exit(0));
  });
}
