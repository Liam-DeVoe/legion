/** `bun socket-access.ts doc <server> <header file> <document id> <schema version>`,
 *  `bun socket-access.ts live <server> <header file> <ready file> <written file>` and
 *  `bun socket-access.ts watch <server> <header file> <ready file> <written file> <seconds>`: opens
 *  one of Dispatch's two sockets as the caller whose credential the header file holds - one
 *  `Name: value` line, a `Cookie` or an `Authorization` - and prints what that caller is answered,
 *  in one form for every outcome, so a driver compares two callers' answers, or the answers to two
 *  ids, byte for byte. The server is an `http://` or an `https://` origin; an `https://` one is
 *  asked over TLS, on port 443 when it names none.
 *
 *  - An upgrade the server refuses prints `http <status> <body>`. Bun's WebSocket reports a
 *    refused upgrade as an error with no status, so the upgrade is asked once over a connection of
 *    its own first, and the socket is opened only when that answer is 101.
 *  - `doc` sends the one token frame the browser's Hocuspocus provider sends (as socket-scope.ts
 *    does) and prints `authenticated <scope>` or `denied <reason>`, or `closed <code> <reason>`
 *    for a socket closed before either.
 *  - `live` subscribes to the event feed from now, with no resume, writes the ready file once the
 *    feed's `events_start` frame has come, waits for the driver to make the written file (its
 *    writes have committed), and prints the `events_start` frame and then every frame the server
 *    sent, one per line, until no frame has come for `quietMs`: what this caller is sent live of
 *    the writes the driver made. Each event's id, in the frame and in the event, is printed as
 *    `<event>`: two servers number their events apart, and which events a caller is sent is the
 *    answer, not their numbers.
 *  - `watch` subscribes as `live` does and writes the ready file at the same moment, then prints
 *    every frame the server sent but heartbeats, each as the server wrote it, from the
 *    `events_start` frame until `<seconds>` after the written file appeared, however busy or quiet
 *    the feed was: what this caller was sent in that window. The driver has `writtenWithinMs` to
 *    make the written file. e2e/acceptance/restricted-sweep.sh runs it.
 *
 *  Exit 0 for any answer, a refusal included; 1 when the server could not be reached or gave no
 *  answer within ten seconds (for `watch`, within ten seconds beyond the written file's wait and
 *  the window); 2 for a wrong call. e2e/acceptance/project-access.sh runs `doc` and `live`. */
import { createDecoder, readVarString, readVarUint } from "lib0/decoding";
import { createEncoder, toUint8Array, writeVarString, writeVarUint } from "lib0/encoding";

// Bun's WebSocket client accepts request headers; the repository's DOM WebSocket type does not.
const BunWebSocket = WebSocket as unknown as new (
  url: string,
  options: { readonly headers: Record<string, string> }
) => WebSocket;

/** Hocuspocus's message type for authentication, and its token and answer subtypes. */
const authMessage = 2;
const tokenSubtype = 0;
const permissionDeniedSubtype = 1;
const authenticatedSubtype = 2;

const answerWithinMs = 10_000;
/** How long the event feed must be quiet before its replay is taken as whole. */
const quietMs = 1_500;
/** How long `watch` waits for its driver's written file once its feed has started. */
const writtenWithinMs = 60_000;

function usage(): never {
  console.error(
    "usage: bun socket-access.ts doc <server> <header file> <document id> <schema version>\n" +
      "       bun socket-access.ts live <server> <header file> <ready file> <written file>\n" +
      "       bun socket-access.ts watch <server> <header file> <ready file> <written file> <seconds>"
  );
  process.exit(2);
}

const [mode, server, headerFile, third, fourth, fifth] = Bun.argv.slice(2);
if (server === undefined || headerFile === undefined) usage();
if (mode !== "doc" && mode !== "live" && mode !== "watch") usage();
if (third === undefined || fourth === undefined) usage();
const [documentId, schemaVersion] = mode === "doc" ? [third, fourth] : [undefined, undefined];
const [readyFile, writtenFile] = mode === "doc" ? [undefined, undefined] : [third, fourth];
/** `watch`'s window, in milliseconds: a whole number of seconds, at least one. */
const watchMs = mode === "watch" ? Number(fifth) * 1000 : 0;
if (mode === "watch" && !(/^[1-9][0-9]*$/.test(fifth ?? "") && Number.isSafeInteger(watchMs))) {
  usage();
}
const protocol = new URL(server).protocol;
if (protocol !== "http:" && protocol !== "https:") usage();

const origin = new URL(server).origin;
const credential = (await Bun.file(headerFile).text()).trim();
const separator = credential.indexOf(":");
if (separator < 1) {
  console.error(`${headerFile} holds no "Name: value" line`);
  process.exit(2);
}
const headers: Record<string, string> = {
  [credential.slice(0, separator).trim()]: credential.slice(separator + 1).trim(),
  Origin: origin,
};
const path =
  mode === "doc"
    ? `/ws/doc/${encodeURIComponent(documentId ?? "")}?schema_version=${encodeURIComponent(schemaVersion ?? "")}`
    : "/ws/live";

/** A chunked body's bytes, once the last chunk has come, and otherwise undefined. */
function dechunked(body: string): string | undefined {
  let rest = body;
  let whole = "";
  for (;;) {
    const line = rest.indexOf("\r\n");
    if (line < 0) return undefined;
    const size = Number.parseInt(rest.slice(0, line), 16);
    if (Number.isNaN(size)) return undefined;
    if (size === 0) return whole;
    const end = line + 2 + size;
    if (rest.length < end + 2) return undefined;
    whole += rest.slice(line + 2, end);
    rest = rest.slice(end + 2);
  }
}

/** The server's answer to a WebSocket upgrade of `path`: its status, and its body when it is not
 *  101. Asked over a connection of its own, TLS for an `https://` server, since only a raw read of
 *  the response sees a refusal. A refusal's body is read to its length, to its last chunk, or to
 *  the connection's close. */
async function upgradeAnswer(): Promise<{ status: number; body: string }> {
  const url = new URL(server ?? "");
  const secure = url.protocol === "https:";
  const key = Buffer.from(crypto.getRandomValues(new Uint8Array(16))).toString("base64");
  const request = [
    `GET ${path} HTTP/1.1`,
    `Host: ${url.host}`,
    "Connection: Upgrade",
    "Upgrade: websocket",
    "Sec-WebSocket-Version: 13",
    `Sec-WebSocket-Key: ${key}`,
    ...Object.entries(headers).map(([name, value]) => `${name}: ${value}`),
    "",
    "",
  ].join("\r\n");
  const received = Promise.withResolvers<{ status: number; body: string }>();
  let raw = "";
  const settle = (connection: { end(): void }) => {
    const head = raw.indexOf("\r\n\r\n");
    if (head < 0) return;
    const status = Number(raw.slice(9, 12));
    if (status === 101) {
      received.resolve({ status, body: "" });
      connection.end();
      return;
    }
    const length = /\r\ncontent-length: *(\d+)/i.exec(raw.slice(0, head));
    const body = raw.slice(head + 4);
    if (length?.[1] !== undefined && Buffer.byteLength(body) >= Number(length[1])) {
      received.resolve({ status, body });
      connection.end();
      return;
    }
    const chunks = /\r\ntransfer-encoding: *chunked/i.test(raw.slice(0, head))
      ? dechunked(body)
      : undefined;
    if (chunks !== undefined) {
      received.resolve({ status, body: chunks });
      connection.end();
    }
  };
  const connection = await Bun.connect({
    // A bracketed IPv6 literal is the URL's spelling; the connect takes the address alone.
    hostname: url.hostname.replace(/^\[(.*)\]$/, "$1"),
    port: Number(url.port || (secure ? 443 : 80)),
    tls: secure,
    socket: {
      data(socket, chunk) {
        raw += chunk.toString();
        settle(socket);
      },
      close() {
        const head = raw.indexOf("\r\n\r\n");
        if (head < 0) received.reject(new Error(`the upgrade of ${path} closed with no answer`));
        else received.resolve({ status: Number(raw.slice(9, 12)), body: raw.slice(head + 4) });
      },
      error(_socket, error) {
        received.reject(error);
      },
    },
  });
  connection.write(request);
  const timer = setTimeout(
    () =>
      received.reject(
        new Error(`no answer to the upgrade of ${path} within ${answerWithinMs / 1000} s`)
      ),
    answerWithinMs
  );
  try {
    return await received.promise;
  } finally {
    clearTimeout(timer);
    connection.end();
  }
}

/** The document socket's authentication answer. */
function documentAnswer(socket: WebSocket): Promise<string> {
  const answer = Promise.withResolvers<string>();
  const token = createEncoder();
  writeVarString(token, documentId ?? "");
  writeVarUint(token, authMessage);
  writeVarUint(token, tokenSubtype);
  writeVarString(token, schemaVersion ?? "");
  socket.addEventListener("open", () => socket.send(toUint8Array(token)));
  socket.addEventListener("message", (event) => {
    if (!(event.data instanceof ArrayBuffer)) return;
    const frame = createDecoder(new Uint8Array(event.data));
    readVarString(frame);
    if (readVarUint(frame) !== authMessage) return;
    const subtype = readVarUint(frame);
    const text = readVarString(frame);
    if (subtype === authenticatedSubtype) answer.resolve(`authenticated ${text}`);
    if (subtype === permissionDeniedSubtype) answer.resolve(`denied ${text}`);
  });
  socket.addEventListener("close", (event) =>
    answer.resolve(`closed ${event.code} ${event.reason}`)
  );
  return answer.promise;
}

/** What the event feed sends this caller of the driver's writes, as the lines it printed: the
 *  feed starts with no resume, the ready file says it has, and the frames are read until the feed
 *  has been quiet for `quietMs` after the written file appeared. */
function liveAnswer(socket: WebSocket): Promise<string> {
  const answer = Promise.withResolvers<string>();
  const lines: string[] = [];
  let quiet: Timer | undefined;
  let written = false;
  const settled = () => answer.resolve(lines.join("\n"));
  const awaitQuiet = () => {
    clearTimeout(quiet);
    if (written) quiet = setTimeout(settled, quietMs);
  };
  socket.addEventListener("open", () =>
    socket.send(JSON.stringify({ type: "subscribe", sub: "e", view: { kind: "events" } }))
  );
  socket.addEventListener("message", (event) => {
    if (typeof event.data !== "string") return;
    const frame = JSON.parse(event.data) as {
      type?: string;
      id?: string;
      event?: { id?: unknown };
    };
    if (frame.type === "heartbeat") return;
    if (frame.type === "event") {
      frame.id = "<event>";
      if (frame.event !== undefined) frame.event.id = "<event>";
    }
    lines.push(JSON.stringify(frame));
    if (frame.type === "events_start") {
      void (async () => {
        await Bun.write(readyFile ?? "", "ready\n");
        while (!(await Bun.file(writtenFile ?? "").exists())) await Bun.sleep(50);
        written = true;
        awaitQuiet();
      })();
    }
    awaitQuiet();
  });
  socket.addEventListener("close", (event) => {
    lines.push(`closed ${event.code} ${event.reason}`);
    settled();
  });
  return answer.promise;
}

/** What the event feed sends this caller in a window, as the frames it printed: the feed starts
 *  with no resume, the ready file says it has, and every frame but a heartbeat is kept as the
 *  server wrote it until `watchMs` after the written file appeared. A driver that makes no written
 *  file within `writtenWithinMs` gets no answer, which exits 1. */
function watchAnswer(socket: WebSocket): Promise<string> {
  const answer = Promise.withResolvers<string>();
  const lines: string[] = [];
  const settled = () => answer.resolve(lines.join("\n"));
  socket.addEventListener("open", () =>
    socket.send(JSON.stringify({ type: "subscribe", sub: "e", view: { kind: "events" } }))
  );
  socket.addEventListener("message", (event) => {
    if (typeof event.data !== "string") return;
    const frame = JSON.parse(event.data) as { type?: string };
    if (frame.type === "heartbeat") return;
    lines.push(event.data);
    if (frame.type !== "events_start") return;
    void (async () => {
      await Bun.write(readyFile ?? "", "ready\n");
      const deadline = Date.now() + writtenWithinMs;
      while (!(await Bun.file(writtenFile ?? "").exists())) {
        if (Date.now() > deadline) {
          answer.reject(
            new Error(`the written file did not appear within ${writtenWithinMs / 1000} s`)
          );
          return;
        }
        await Bun.sleep(50);
      }
      setTimeout(settled, watchMs);
    })();
  });
  socket.addEventListener("close", (event) => {
    lines.push(`closed ${event.code} ${event.reason}`);
    settled();
  });
  return answer.promise;
}

try {
  const upgrade = await upgradeAnswer();
  if (upgrade.status !== 101) {
    console.log(`http ${upgrade.status} ${upgrade.body}`);
  } else {
    const socket = new BunWebSocket(`${origin.replace(/^http/, "ws")}${path}`, { headers });
    socket.binaryType = "arraybuffer";
    // `watch` answers only after its driver's write and its window, so its bound adds both.
    const answerMs = mode === "watch" ? answerWithinMs + writtenWithinMs + watchMs : answerWithinMs;
    const timer = setTimeout(() => {
      console.error(`no answer on ${path} within ${answerMs / 1000} s`);
      process.exit(1);
    }, answerMs);
    const answer = await (mode === "doc"
      ? documentAnswer(socket)
      : mode === "live"
        ? liveAnswer(socket)
        : watchAnswer(socket));
    clearTimeout(timer);
    console.log(answer);
    socket.close();
  }
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
}
