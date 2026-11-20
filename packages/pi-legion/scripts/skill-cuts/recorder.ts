/**
 * A stand-in Anthropic Messages endpoint on 127.0.0.1 (plan, "Where things run"): a host process
 * pointed at it sends its model requests here, never to the gateway. The recorder keeps each
 * request, its body byte for byte and its headers less every auth header and every header that
 * names a key, a token, a secret, a team or a user, and answers it: with what `route` returns, or
 * with END_TURN_REPLY, a fixed end-of-turn reply streamed as Server-Sent Events, when `route`
 * returns nothing. capture.ts records Oh My Pi's request bodies with it, and `cli.test.ts` builds
 * its stand-in gateway on it.
 *
 * `messageSse` writes a reply message as the event stream the Messages API sends for a request
 * that sets `stream` (the event shapes of the F5 replies): a `message_start`, each content block as
 * a start, one delta and a stop, a `message_delta` with the stop reason and the output tokens, and
 * a `message_stop`. It writes text, `tool_use` and `thinking` blocks.
 */

/** A request the recorder took: its number from 1, method, path with its query, the headers it
 *  keeps, and the body as text. */
export interface RecordedRequest {
  readonly n: number;
  readonly method: string;
  readonly path: string;
  readonly headers: Readonly<Record<string, string>>;
  readonly body: string;
}

/** The answer to a request, or undefined for the recorder's own: END_TURN_REPLY to a POST to
 *  `/v1/messages`, a 404 to anything else. */
export type Route = (
  request: RecordedRequest
) => Response | undefined | Promise<Response | undefined>;

export interface Recorder {
  /** The base URL, `http://127.0.0.1:<port>`, with no trailing slash. */
  readonly url: string;
  /** Every request taken so far, in the order they came. */
  readonly requests: readonly RecordedRequest[];
  /** Stops the server; requests still open are ended. */
  close(): Promise<void>;
}

/** A header the recorder never keeps: an auth header, or one that names a key, a token, a secret,
 *  a team or a user (the gateway's `x-litellm-*` key alias and team headers among them). */
const LEFT_OUT_HEADER =
  /^(authorization|proxy-authorization|cookie|set-cookie)$|key|token|secret|auth|team|user/i;

/** The headers of `headers` the recorder keeps, by lower-case name. */
export function keptHeaders(headers: Headers): Record<string, string> {
  const kept: Record<string, string> = {};
  for (const [name, value] of headers) {
    if (!LEFT_OUT_HEADER.test(name)) kept[name.toLowerCase()] = value;
  }
  return kept;
}

/** A block of a reply message that `messageSse` writes. */
export type ReplyBlock =
  | { readonly type: "text"; readonly text: string }
  | {
      readonly type: "tool_use";
      readonly id: string;
      readonly name: string;
      readonly input: Readonly<Record<string, unknown>>;
    }
  | { readonly type: "thinking"; readonly thinking: string; readonly signature: string };

/** The reply message `messageSse` streams. */
export interface ReplyMessage {
  readonly id: string;
  readonly model: string;
  readonly content: readonly ReplyBlock[];
  readonly stopReason: "end_turn" | "tool_use" | "max_tokens";
  readonly inputTokens?: number;
  readonly outputTokens?: number;
}

/** One Server-Sent Event: its name and its data as one JSON line. */
function event(name: string, data: unknown): string {
  return `event: ${name}\ndata: ${JSON.stringify(data)}\n\n`;
}

/** `message` as the event stream the Messages API sends for a request that sets `stream`. */
export function messageSse(message: ReplyMessage): string {
  const events = [
    event("message_start", {
      type: "message_start",
      message: {
        id: message.id,
        type: "message",
        role: "assistant",
        model: message.model,
        content: [],
        stop_reason: null,
        stop_sequence: null,
        usage: {
          input_tokens: message.inputTokens ?? 10,
          cache_creation_input_tokens: 0,
          cache_read_input_tokens: 0,
          output_tokens: 1,
        },
      },
    }),
  ];
  message.content.forEach((block, index) => {
    const started =
      block.type === "text"
        ? { type: "text", text: "" }
        : block.type === "tool_use"
          ? { type: "tool_use", id: block.id, name: block.name, input: {} }
          : { type: "thinking", thinking: "", signature: "" };
    events.push(
      event("content_block_start", { type: "content_block_start", index, content_block: started })
    );
    const deltas =
      block.type === "text"
        ? [{ type: "text_delta", text: block.text }]
        : block.type === "tool_use"
          ? [{ type: "input_json_delta", partial_json: JSON.stringify(block.input) }]
          : [
              { type: "thinking_delta", thinking: block.thinking },
              { type: "signature_delta", signature: block.signature },
            ];
    for (const delta of deltas) {
      events.push(event("content_block_delta", { type: "content_block_delta", index, delta }));
    }
    events.push(event("content_block_stop", { type: "content_block_stop", index }));
  });
  events.push(
    event("message_delta", {
      type: "message_delta",
      delta: { stop_reason: message.stopReason, stop_sequence: null },
      usage: { output_tokens: message.outputTokens ?? 3 },
    }),
    event("message_stop", { type: "message_stop" })
  );
  return events.join("");
}

/** The recorder's own answer to a Messages request: one text block, `Recorded.`, ending the
 *  turn. */
export const END_TURN_REPLY = messageSse({
  id: "msg_recorder_end_turn",
  model: "recorder",
  content: [{ type: "text", text: "Recorded." }],
  stopReason: "end_turn",
});

/** `sse` as the response a streamed Messages request gets. */
export function sseResponse(sse: string, requestId?: string): Response {
  const headers: Record<string, string> = { "content-type": "text/event-stream" };
  if (requestId !== undefined) headers["request-id"] = requestId;
  return new Response(sse, { headers });
}

/** Starts a recorder on 127.0.0.1 at a free port. */
export function startRecorder(options: { readonly route?: Route } = {}): Recorder {
  const requests: RecordedRequest[] = [];
  const server = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    // A route may hold a request open for minutes (capture.ts holds Oh My Pi's turn until its
    // skill hints are written), so the connection is never ended for being idle.
    idleTimeout: 0,
    async fetch(request) {
      const url = new URL(request.url);
      const recorded: RecordedRequest = {
        n: requests.length + 1,
        method: request.method,
        path: url.pathname + url.search,
        headers: keptHeaders(request.headers),
        body: request.method === "GET" || request.method === "HEAD" ? "" : await request.text(),
      };
      requests.push(recorded);
      const answer = await options.route?.(recorded);
      if (answer !== undefined) return answer;
      if (recorded.method === "POST" && url.pathname === "/v1/messages") {
        return sseResponse(END_TURN_REPLY, `req_recorder_${recorded.n}`);
      }
      return new Response("not found", { status: 404 });
    },
  });
  return {
    url: `http://127.0.0.1:${server.port}`,
    requests,
    close: async () => {
      await server.stop(true);
    },
  };
}
