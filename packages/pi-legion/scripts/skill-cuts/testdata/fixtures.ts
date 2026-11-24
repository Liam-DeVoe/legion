/**
 * Builders of made-up fixtures that more than one test file of the runner uses. Each repository,
 * commit and route they give is made up.
 */
import { writeFileSync } from "node:fs";
import { type Manifest, type ManifestKind, parseManifest } from "../copies";

/** A manifest of `repository` at `file` in its tree, parsed as `kind`. */
export const madeManifest = (
  repository: string,
  file: string,
  kind: ManifestKind,
  text: string
): Manifest => ({
  repository,
  path: file,
  kind,
  source: "tree",
  ...parseManifest(kind, file, text),
  error: "",
  rows: 0,
});

/** GitHub's answers, by route, for the made-up acme/skills at `commit` as GitHub lists a
 *  repository of too many entries: its recursive tree cut short, then skills/beta's, and
 *  skills/beta's one level even read alone. */
export function truncatedSkillsTree(commit: string): Record<string, object> {
  const [skills, beta] = ["1".repeat(40), "2".repeat(40)];
  return {
    [`repos/acme/skills/git/trees/${commit}?recursive=1`]: { tree: [], truncated: true },
    [`repos/acme/skills/git/trees/${commit}`]: {
      tree: [{ path: "skills", mode: "040000", type: "tree", sha: skills }],
      truncated: false,
    },
    [`repos/acme/skills/git/trees/${skills}`]: {
      tree: [{ path: "beta", mode: "040000", type: "tree", sha: beta }],
      truncated: false,
    },
    [`repos/acme/skills/git/trees/${beta}?recursive=1`]: { tree: [], truncated: true },
    [`repos/acme/skills/git/trees/${beta}`]: { tree: [], truncated: true },
  };
}

/** Writes at `file` a stand-in gh that appends each call's route (its last argument) to `log`
 *  and answers it with the JSON `answers` holds for it, or as GitHub answers a route it does not
 *  hold (exit 1, `gh: Not Found (HTTP 404)`). */
export function writeGhStandIn(file: string, log: string, answers: Record<string, object>): void {
  writeFileSync(
    file,
    `#!${process.execPath}
import { appendFileSync } from "node:fs";
const answers = ${JSON.stringify(answers)};
const route = process.argv.at(-1) ?? "";
appendFileSync(${JSON.stringify(log)}, route + "\\n");
if (answers[route] === undefined) {
  process.stderr.write("gh: Not Found (HTTP 404)\\n");
  process.exit(1);
}
process.stdout.write(JSON.stringify(answers[route]));
`,
    { mode: 0o755 }
  );
}

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
