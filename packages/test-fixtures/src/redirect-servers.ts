/** The redirect statuses whose `Location` fetch follows, each a row of a redirect test. */
export const REDIRECT_CODES = [301, 302, 303, 307, 308] as const;

/** One of {@link REDIRECT_CODES}. */
export type RedirectCode = (typeof REDIRECT_CODES)[number];

/** A request the other server received: what reached the origin a redirect named. */
export interface ReceivedRequest {
  readonly method: string;
  readonly path: string;
  readonly headers: Readonly<Record<string, string>>;
  readonly body: string;
}

/** The two servers of {@link redirectServers}. */
export interface RedirectServers {
  /** The origin the client under test is pointed at, `http://127.0.0.1:<port>`. */
  readonly origin: string;
  /** The other server's origin: the same host on another port, so another origin. */
  readonly elsewhere: string;
  /** Every request the other server received, oldest first. */
  readonly received: readonly ReceivedRequest[];
  /** The status the redirects answer with, 307 until a test sets it. */
  code: RedirectCode;
  stop(): void;
}

type Handler = (request: Request) => Response | Promise<Response>;

/**
 * Two servers on 127.0.0.1, each on a port the kernel chooses, for a test that a client which
 * carries a credential follows no redirect. The server at `origin` answers each request whose path
 * `redirects` selects (every request, unless given) with the status `code` holds and a `Location`
 * naming the same path and query at `elsewhere`, and answers every other request with `direct`.
 * The server at `elsewhere` records each request it receives, headers and body included, and
 * answers it with `atElsewhere`, so a client that follows the redirect gets an answer it can take
 * as a success there. The test sets `code`, calls the client, and expects `received` to stay
 * empty.
 */
export function redirectServers(
  options: {
    readonly direct?: Handler;
    readonly redirects?: (path: string) => boolean;
    readonly atElsewhere?: Handler;
  } = {}
): RedirectServers {
  const received: ReceivedRequest[] = [];
  const atElsewhere = options.atElsewhere ?? (() => Response.json({}));
  const other = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    async fetch(request) {
      const url = new URL(request.url);
      received.push({
        method: request.method,
        path: `${url.pathname}${url.search}`,
        headers: Object.fromEntries(request.headers),
        body: await request.text(),
      });
      return atElsewhere(request);
    },
  });
  const elsewhere = `http://127.0.0.1:${other.port}`;
  const direct = options.direct ?? (() => new Response("no direct answer", { status: 500 }));
  const redirects = options.redirects ?? (() => true);
  let code: RedirectCode = 307;
  const first = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch(request) {
      const url = new URL(request.url);
      if (!redirects(url.pathname)) return direct(request);
      return new Response(null, {
        status: code,
        headers: { Location: `${elsewhere}${url.pathname}${url.search}` },
      });
    },
  });
  return {
    origin: `http://127.0.0.1:${first.port}`,
    elsewhere,
    received,
    get code() {
      return code;
    },
    set code(value) {
      code = value;
    },
    stop() {
      first.stop(true);
      other.stop(true);
    },
  };
}
