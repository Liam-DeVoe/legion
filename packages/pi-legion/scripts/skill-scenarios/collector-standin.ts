/**
 * A stand-in spend collector for the tests of spend-record.ts's recorder (skill-cuts' and this
 * directory's): an HTTP server on 127.0.0.1 that keeps each post it takes, and the reader of the
 * one span a post holds. Nothing is sent anywhere.
 */

/** One post a stand-in collector took: its path, its headers by lower-case name, and its body. */
export interface CollectedPost {
  readonly path: string;
  readonly headers: Readonly<Record<string, string>>;
  readonly body: string;
}

/** A stand-in spend collector (`startCollector`). */
export interface Collector {
  /** The base URL, `http://127.0.0.1:<port>`, as OTEL_EXPORTER_OTLP_ENDPOINT names one. */
  readonly url: string;
  /** Every post taken so far, in the order their bodies were read whole. */
  readonly posts: CollectedPost[];
  /** The status each answer has, 200 at first. */
  status: number;
  /** How long each answer waits before it goes, 0 ms at first. */
  delayMs: number;
  /** Stops the server; posts still open are ended. */
  close(): void;
}

/** A stand-in spend collector on 127.0.0.1, which keeps each post it takes and answers it with
 *  `status` after `delayMs`. */
export function startCollector(): Collector {
  const posts: CollectedPost[] = [];
  const server = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    idleTimeout: 0,
    async fetch(request) {
      posts.push({
        path: new URL(request.url).pathname,
        headers: Object.fromEntries(request.headers),
        body: await request.text(),
      });
      if (collector.delayMs > 0) await Bun.sleep(collector.delayMs);
      return new Response("{}", { status: collector.status });
    },
  });
  const collector: Collector = {
    url: `http://127.0.0.1:${server.port}`,
    posts,
    status: 200,
    delayMs: 0,
    close: () => {
      server.stop(true);
    },
  };
  return collector;
}

/** The one span of a post a recorder sent (spend-record.ts), and its attributes by key, each as
 *  the text or the number it holds. */
export function postedSpan(post: CollectedPost): {
  readonly span: Record<string, unknown>;
  readonly attributes: Record<string, string | number>;
} {
  const span = JSON.parse(post.body).resourceSpans[0].scopeSpans[0].spans[0];
  const attributes: Record<string, string | number> = {};
  for (const { key, value } of span.attributes) {
    attributes[key] = value.stringValue ?? value.intValue ?? value.doubleValue;
  }
  return { span, attributes };
}
