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
/** The longest delay a timer holds: Bun fires a longer one at once. */
const longestTimerMs = 2 ** 31 - 1;

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
/** `watch`'s window, in milliseconds: a whole number of seconds, at least one, and short enough
 *  that its answer's bound, which adds it, still fits a timer. */
const watchMs = mode === "watch" ? Number(fifth) * 1000 : 0;
if (
  mode === "watch" &&
  !(
    /^[1-9][0-9]*$/.test(fifth ?? "") &&
    answerWithinMs + writtenWithinMs + watchMs <= longestTimerMs
  )
) {
  usage();
}
const url = new URL(server);
if (url.protocol !== "http:" && url.protocol !== "https:") usage();

const origin = url.origin;
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
    ? `/ws/doc/${encodeURIComponent(third)}?schema_version=${encodeURIComponent(fourth)}`
    : "/ws/live";

/** A chunked body's text, once its last chunk has come, and otherwise undefined. A chunk's size
 *  counts bytes, so the body is read as bytes and decoded whole. */
function dechunked(body: Buffer): string | undefined {
  let rest = body;
  const chunks: Buffer[] = [];
  for (;;) {
    const line = rest.indexOf("\r\n");
    if (line < 0) return undefined;
    const size = Number.parseInt(rest.subarray(0, line).toString(), 16);
    if (Number.isNaN(size)) return undefined;
    if (size === 0) return Buffer.concat(chunks).toString();
    const end = line + 2 + size;
    if (rest.length < end + 2) return undefined;
    chunks.push(rest.subarray(line + 2, end));
    rest = rest.subarray(end + 2);
  }
}

/** The server's answer to a WebSocket upgrade of `path`: its status, and its body when it is not
 *  101. Asked over a connection of its own, TLS for an `https://` server, since only a raw read of
 *  the response sees a refusal. A refusal's body is read to its length, to its last chunk, or to
 *  the connection's close. */
async function upgradeAnswer(): Promise<{ status: number; body: string }> {
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
  // Bytes, not text: a chunk's size and a Content-Length count bytes, and a character a read
  // splits in two decodes whole only once both halves have come.
  let raw = Buffer.alloc(0);
  const settle = (connection: { end(): void }) => {
    const head = raw.indexOf("\r\n\r\n");
    if (head < 0) return;
    const status = Number(raw.subarray(9, 12).toString());
    if (status === 101) {
      received.resolve({ status, body: "" });
      connection.end();
      return;
    }
    const fields = raw.subarray(0, head).toString();
    const length = /\r\ncontent-length: *(\d+)/i.exec(fields);
    const body = raw.subarray(head + 4);
    if (length?.[1] !== undefined && body.length >= Number(length[1])) {
      received.resolve({ status, body: body.toString() });
      connection.end();
      return;
    }
    const chunks = /\r\ntransfer-encoding: *chunked/i.test(fields) ? dechunked(body) : undefined;
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
        raw = Buffer.concat([raw, chunk]);
        settle(socket);
      },
      close() {
        const head = raw.indexOf("\r\n\r\n");
        if (head < 0) received.reject(new Error(`the upgrade of ${path} closed with no answer`));
        else {
          received.resolve({
            status: Number(raw.subarray(9, 12).toString()),
            body: raw.subarray(head + 4).toString(),
          });
        }
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
function documentAnswer(
  socket: WebSocket,
  documentId: string,
  schemaVersion: string
): Promise<string> {
  const answer = Promise.withResolvers<string>();
  const token = createEncoder();
  writeVarString(token, documentId);
  writeVarUint(token, authMessage);
  writeVarUint(token, tokenSubtype);
  writeVarString(token, schemaVersion);
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
function liveAnswer(socket: WebSocket, readyFile: string, writtenFile: string): Promise<string> {
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
        await Bun.write(readyFile, "ready\n");
        while (!(await Bun.file(writtenFile).exists())) await Bun.sleep(50);
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

/** The `type` of a feed frame the server wrote as `data`, or undefined for a frame that is no JSON
 *  object, which `watch` keeps as the server wrote it, as it keeps every frame but a heartbeat. */
function frameType(data: string): unknown {
  let frame: unknown;
  try {
    frame = JSON.parse(data);
  } catch {
    return undefined;
  }
  return typeof frame === "object" && frame !== null && "type" in frame ? frame.type : undefined;
}

/** What the event feed sends this caller in a window, as the frames it printed: the feed starts
 *  with no resume, the ready file says it has, and every frame but a heartbeat is kept as the
 *  server wrote it until `windowMs` after the written file appeared. A driver that makes no
 *  written file within `writtenWithinMs`, or a ready file that cannot be written, rejects the
 *  answer, which exits 1. A feed that closes first is answered at its close. */
function watchAnswer(
  socket: WebSocket,
  readyFile: string,
  writtenFile: string,
  windowMs: number
): Promise<string> {
  const answer = Promise.withResolvers<string>();
  const lines: string[] = [];
  // Once the answer is in, the wait for the written file and the window stop, so neither keeps
  // this process running past it.
  let done = false;
  let windowTimer: Timer | undefined;
  const settled = () => {
    done = true;
    clearTimeout(windowTimer);
    answer.resolve(lines.join("\n"));
  };
  const failed = (error: unknown) => {
    done = true;
    clearTimeout(windowTimer);
    answer.reject(error);
  };
  const awaitWritten = async () => {
    await Bun.write(readyFile, "ready\n");
    const deadline = Date.now() + writtenWithinMs;
    while (!done && !(await Bun.file(writtenFile).exists())) {
      if (Date.now() > deadline) {
        throw new Error(`the written file did not appear within ${writtenWithinMs / 1000} s`);
      }
      await Bun.sleep(50);
    }
  };
  let started = false;
  socket.addEventListener("open", () =>
    socket.send(JSON.stringify({ type: "subscribe", sub: "e", view: { kind: "events" } }))
  );
  socket.addEventListener("message", (event) => {
    if (typeof event.data !== "string") return;
    const type = frameType(event.data);
    if (type === "heartbeat") return;
    lines.push(event.data);
    // The window follows the first `events_start` alone: a second one starts no second wait.
    if (type !== "events_start" || started) return;
    started = true;
    void awaitWritten().then(() => {
      if (!done) windowTimer = setTimeout(settled, windowMs);
    }, failed);
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
    // However the wait ends, `watch`'s rejection included, the bound and the socket stop with it:
    // either would keep this process running, and the bound would print a second failure.
    try {
      console.log(
        await (mode === "doc"
          ? documentAnswer(socket, third, fourth)
          : mode === "live"
            ? liveAnswer(socket, third, fourth)
            : watchAnswer(socket, third, fourth, watchMs))
      );
    } finally {
      clearTimeout(timer);
      socket.close();
    }
  }
} catch (error) {
  console.error(error instanceof Error ? error.message : String(error));
  process.exitCode = 1;
}
