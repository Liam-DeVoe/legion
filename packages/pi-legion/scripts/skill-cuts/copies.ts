/**
 * R4 of INFRA-4676 without reads: every skill copy of the configured GitHub owners' repositories,
 * the manifests and installers that ship, install or load each, the files outside a skill that name
 * one, and the copy-path prefix table R1's attribution reads.
 *
 *   bun scripts/skill-cuts/copies.ts --config <skill-cuts.json> --at <ISO instant> --out <dir>
 *     [--only <owner/repo>]... [--max-search-calls <n>]
 *
 * Task t7 adds it to cli.ts as `skill-cuts copies`; `main(argv, io)` and the pure parts are exported
 * for that and for copies.test.ts, which injects the command runner (`io.gh`).
 *
 * It reads GitHub only through `gh` (`gh repo list`, `gh api`), with whatever token the pod's `gh`
 * already uses: this file holds no token, sets no GH_TOKEN and needs none of its own. The pod's gh
 * sends each call with the token of the one owner the call names, so every call here names exactly
 * one owner (a search names one `org:`, `user:` or `repo:` qualifier). Blobs are read in batches of
 * up to 50 with `gh api graphql`, whose `-f owner=<owner>` field (the query's `$owner` variable) is
 * what the pod's gh routes a GraphQL call by, so it takes that owner's token; a blob a batch does
 * not give whole (binary, truncated, missing) is read alone with `gh api repos/<r>/git/blobs/<sha>`.
 *
 * 1. Repositories. `gh repo list <owner> --limit 2000 --json nameWithOwner,isFork,isArchived,
 *    defaultBranchRef,parent` for each configured owner (with `--only`, for the owners of the named
 *    repositories, keeping those alone). For each repository, the default branch's commit at `--at`
 *    (`repos/<r>/commits?sha=<branch>&until=<at>&per_page=1`) and that commit's tree
 *    (`git/trees/<tree>?recursive=1`). A tree GitHub answers `truncated` is read again by
 *    directory: each subtree is read recursively, and one that is truncated too is read one level
 *    down. A listing, commit or tree read that fails names its repository with gh's error. Forks
 *    and archived repositories stay, marked (a fork with its parent).
 * 2. Skills. Each `SKILL.md` blob outside a configured `excludePaths` prefix of its repository: its
 *    frontmatter `name` (else its directory's name) and `description`, read from the blob. The
 *    frontmatter is read as YAML (Bun.YAML); one that is not valid YAML is read by `lenientFields`
 *    (top-level `key: value`, plain or quoted, folded `>` or literal `|` blocks, flow lists).
 * 3. Shared names. Each name in two or more repositories, and each name twice in one repository,
 *    forks and archived copies included and marked.
 * 4. Manifests, read whole from the trees, by path (`MANIFEST_KINDS`). Each gives installer rows:
 *    which skills it ships, installs, links or loads, into which unit and host. A `package.json` is
 *    read only in a repository that holds a `SKILL.md` (a package that ships skills ships them from
 *    such a repository); every other kind is read wherever its path is. Every manifest read is
 *    resolved (a package.json without omp.skills still names the package an npm-sourced plugin
 *    comes from); only those of their kind are listed, the rest counted.
 * 5. Loaders by code search. For each skill name, one `gh api -i -X GET search/code` for the quoted
 *    name, once per owner whose listing succeeded (`org:` or `user:`, as `users/<owner>` gives its
 *    type) or, with `--only`, once per named repository (`repo:`). One search stands for the brief's
 *    five patterns (`skill://<name>`, `/skill:<name>`, `skills/<name>`, `<plugin>:<name>` and the
 *    quoted name): `searchFor` says why. Each hit file is read, at its repository's commit of
 *    step 1, and kept only where the skill's name is in it as a whole name (not inside a longer
 *    name), outside a skill; `mentions` then sorts each mention by those patterns. Each query's
 *    `total_count` and `incomplete_results`, its hits and their verdicts, and each failure go to
 *    `queries.jsonl`. A query whose `total_count` reaches 1,000, and each query of a name in
 *    `COMMON_WORD_NAMES` (a short list of plain English words kept in this file, `dispatch` and
 *    `envoy` among them; a plain word not on it is run again only when it reaches 1,000), is run
 *    again once per repository of that owner that holds a skill. A fork, which code search leaves
 *    out, is read from its tree instead when it holds a skill: each text blob (`TEXT_EXTENSIONS`,
 *    or a name in `TEXT_NAMES`) of at most `FORK_SCAN_MAX_BYTES` bytes outside a skill is read and
 *    searched for every skill name. Pacing: GitHub allows 10 code searches a minute per token, and
 *    each owner has its own token, so the owners' searches run at once, each owner's one at a time
 *    (GitHub asks for one client's requests to be serial) and paced by its own limit: at most
 *    `x-ratelimit-limit` (10 when absent) calls started in any 60 seconds, a wait for
 *    `x-ratelimit-reset` when `x-ratelimit-remaining` is 0, and a call refused for its rate limit
 *    run again after its `retry-after`, else its reset, else the reset `gh api rate_limit` gives.
 *    `--max-search-calls <n>` stops searching after n calls; every query it leaves out is in
 *    `queries.jsonl` as not run. A query whose hits GitHub counts past its 1,000-result cap (or the
 *    budget cut short) keeps how many it left unread (`unread`), and skill-copies.md lists it.
 * 6. The prefix table. From the installers: a skill an Oh My Pi plugin package ships is at
 *    `**\/node_modules/<package>/<dir>/<name>/`, and, for a package the worker image carries (one
 *    the prepack script stages or whose directory holds the Claude Code build script), at
 *    `/opt/legion/<package's last name>/<dir>/<name>/` (packages/daemon/docker/worker.Dockerfile
 *    puts pi-legion and pi-envoy there); a Claude Code marketplace plugin's skill at
 *    `**\/plugins/cache/plugins/<marketplace>___<plugin>___*\/<dir>/` (Oh My Pi's cache) and
 *    `**\/plugins/cache/<marketplace>/<plugin>/*\/<dir>/` (Claude Code's); the plugin the build
 *    script generates, in a directory named as the plugin, at
 *    `/opt/legion/<package directory>/<plugin>/skills/<name>/`; a skill an installer links into a
 *    host's user skills at `**\/.omp/agent/skills/<name>/` or `**\/.claude/skills/<name>/`. Each
 *    row maps the prefix to its repository copy. A row whose repository the configuration's
 *    `targets` do not name is left out of the table and reported in `skill-copies.md`, so the
 *    implementer adds it.
 * 7. Same-job pairs and the follow-the-code sentence are the `read` command's (task t3):
 *    `readerSkills` is the seam that takes the skills list, and `skills.jsonl` holds it. No model is
 *    called here.
 *
 * Output under `--out`. Each `.tsv` starts with a row of its column names; no cell holds a tab or
 * a line break (`tsvCell` writes one as `\t`, `\n` or `\r`, and a backslash as `\\`; `parseTsv`
 * reads them back). An empty path cell is written `.` (the repository's root).
 *   skill-copies.tsv  one row per copy, columns `SKILL_COPIES_COLUMNS`, in this order: repository,
 *                     path (the skill's directory), name, description_bytes (UTF-8 bytes of the
 *                     frontmatter description), fork, archived (yes or no), parent (a fork's),
 *                     installers (`<action> <unit> (<repository>:<manifest path>)`, joined by
 *                     `; `), loaded_by (each file that loads the skill: `<repository>:<path>
 *                     (<how>)`, joined by `; `). Task t7's attribute.ts reads these columns in this
 *                     order and adds its reads columns after them.
 *   prefix-table.tsv  `PREFIX_TABLE_COLUMNS`: prefix (a pattern: `**\/` at its start is any
 *                     leading directories, `*` is one path segment's text; `prefixMatches` is the
 *                     one matcher), repository, path, name, via (the unit and manifest it comes from).
 *   repositories.tsv  `REPOSITORIES_COLUMNS`: each repository read, its commit, fork and archived
 *                     marks, how its tree was read, its skills, or the error.
 *   loaders.tsv       `LOADERS_COLUMNS`: each file outside a skill that names a skill: repository,
 *                     path, skill, match (the kinds of mention, `MATCH_KINDS`, or the manifest kind
 *                     that names it), source.
 *   installers.tsv    `INSTALLERS_COLUMNS`: each installer row, one per manifest, unit and copy.
 *   manifests.tsv     `MANIFESTS_COLUMNS`: each manifest file read, its kind and what it says.
 *   queries.jsonl     every search query (and every fork scan) with its counts, hits, failures.
 *   skills.jsonl      `readerSkills`: each copy with its whole description, for t3's readers.
 *   skill-copies.md   the repositories read and not listable, the shared names, the loaders and the
 *                     installers, the prefix rows outside the targets, and every failure.
 */
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { parseArgs } from "node:util";
import { z } from "zod";
import { loadConfig, type SkillCutsConfig } from "./config";

// ---------------------------------------------------------------------------------------------
// gh

/** What one gh call returned. */
export interface GhResult {
  readonly code: number;
  readonly stdout: string;
  readonly stderr: string;
}

/** Runs gh with `args`; the test injects a stand-in. */
export type Gh = (args: readonly string[]) => Promise<GhResult>;

/** The pod's own gh, with the environment this process has. */
export const spawnGh: Gh = async (args) => {
  const child = Bun.spawn(["gh", ...args], { stdin: "ignore", stdout: "pipe", stderr: "pipe" });
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ]);
  return { code, stdout, stderr };
};

/** What `main` uses of the world. */
export interface CopiesIo {
  readonly gh: Gh;
  sleep(ms: number): Promise<void>;
  /** Epoch milliseconds. */
  now(): number;
  out(text: string): void;
  /** One progress or refusal line. */
  err(text: string): void;
}

const RATE_LIMITED = /rate limit|HTTP 429/i;
const MAX_RATE_RETRIES = 5;
const REST_JOBS = 4;
/** The most tree reads in flight at once, across a run: a truncated tree's subtrees are read at
 *  once under this cap, which keeps a run under GitHub's 15,000 REST calls an hour per token. */
export const TREE_JOBS = 8;
/** The most blobs, and bytes of blobs, one GraphQL read asks for. */
const BLOB_BATCH = 50;
const BLOB_BATCH_BYTES = 2_000_000;

function firstLine(text: string): string {
  return text.trim().split("\n")[0] ?? "";
}

function parseJson(text: string): unknown {
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return undefined;
  }
}

/** One line naming each fault of a value that is not of its schema. */
function schemaFaults(error: z.ZodError): string {
  return error.issues
    .map((issue) => `${issue.path.join(".") || "the value"}: ${issue.message}`)
    .join("; ");
}

// GitHub's answers, parsed at the boundary: only the fields this file reads.
const Listing = z.array(
  z.looseObject({
    nameWithOwner: z.string(),
    isFork: z.boolean(),
    isArchived: z.boolean(),
    defaultBranchRef: z.looseObject({ name: z.string() }).nullable(),
    parent: z
      .looseObject({ name: z.string(), owner: z.looseObject({ login: z.string() }) })
      .nullable(),
  })
);
const Commits = z.array(
  z.looseObject({
    sha: z.string(),
    commit: z.looseObject({ tree: z.looseObject({ sha: z.string() }) }),
  })
);
const GitTree = z.looseObject({
  tree: z.array(
    z.looseObject({
      path: z.string(),
      mode: z.string(),
      type: z.string(),
      sha: z.string(),
      size: z.number().optional(),
    })
  ),
  truncated: z.boolean().optional(),
});
const GitBlob = z.looseObject({ content: z.string(), encoding: z.string() });
const SearchAnswer = z.looseObject({
  total_count: z.number(),
  incomplete_results: z.boolean(),
  items: z.array(
    z.looseObject({ path: z.string(), repository: z.looseObject({ full_name: z.string() }) })
  ),
});
const RateLimit = z.looseObject({
  resources: z.record(z.string(), z.looseObject({ reset: z.number() })),
});
const GithubUser = z.looseObject({ type: z.string() });
const BlobBatch = z.looseObject({
  data: z.looseObject({
    repository: z
      .record(
        z.string(),
        z
          .looseObject({
            isBinary: z.boolean().nullable(),
            isTruncated: z.boolean(),
            text: z.string().nullable(),
          })
          .nullable()
      )
      .nullable(),
  }),
});

/** gh's reads, with a blob cache and a sleep to the reset when the core limit is spent. */
class Github {
  private readonly blobs = new Map<string, Promise<string>>();
  private treeReads = 0;
  private readonly treeReadQueue: (() => void)[] = [];

  constructor(readonly io: CopiesIo) {}

  /** `read`, run once fewer than TREE_JOBS tree reads are in flight; a read that ends hands its
   *  slot to the next one waiting. */
  async treeRead<T>(read: () => Promise<T>): Promise<T> {
    if (this.treeReads < TREE_JOBS) this.treeReads++;
    else {
      const slot = Promise.withResolvers<void>();
      this.treeReadQueue.push(slot.resolve);
      await slot.promise;
    }
    try {
      return await read();
    } finally {
      const next = this.treeReadQueue.shift();
      if (next === undefined) this.treeReads--;
      else next();
    }
  }

  /** gh's answer to `args`, parsed by `schema`; throws gh's first error line, or the faults. */
  async json<T>(args: readonly string[], schema: z.ZodType<T>): Promise<T> {
    for (let attempt = 0; ; attempt++) {
      const result = await this.io.gh(args);
      if (result.code === 0) {
        const parsed = schema.safeParse(parseJson(result.stdout));
        if (!parsed.success)
          throw new Error(`gh's answer is not of its shape: ${schemaFaults(parsed.error)}`);
        return parsed.data;
      }
      if (attempt < MAX_RATE_RETRIES && RATE_LIMITED.test(result.stderr)) {
        await this.io.sleep(await this.waitForReset("core"));
        continue;
      }
      throw new Error(firstLine(result.stderr) || `gh exited ${result.code}`);
    }
  }

  api<T>(endpoint: string, schema: z.ZodType<T>): Promise<T> {
    return this.json(["api", endpoint], schema);
  }

  /** The milliseconds to the reset `gh api rate_limit` gives for `resource`, at most an hour; a
   *  minute when it gives none or one already past. */
  async waitForReset(resource: "core" | "code_search"): Promise<number> {
    const result = await this.io.gh(["api", "rate_limit"]);
    const parsed = RateLimit.safeParse(result.code === 0 ? parseJson(result.stdout) : undefined);
    const reset = parsed.success ? parsed.data.resources[resource]?.reset : undefined;
    const until = reset === undefined ? 0 : reset * 1000 + 1000;
    const wait = until > this.io.now() ? Math.min(until - this.io.now(), 3_600_000) : 60_000;
    this.io.err(`copies: the ${resource} rate limit is spent; waiting ${Math.ceil(wait / 1000)} s`);
    return wait;
  }

  /** The text of the blob `sha` of `repository`; cached by sha, since a blob id is its content. */
  blob(repository: string, sha: string): Promise<string> {
    let text = this.blobs.get(sha);
    if (text === undefined) {
      text = this.api(`repos/${repository}/git/blobs/${sha}`, GitBlob).then((blob) => {
        if (blob.encoding !== "base64")
          throw new Error(`the blob is encoded ${blob.encoding}, not base64`);
        return Buffer.from(blob.content, "base64").toString("utf8");
      });
      this.blobs.set(sha, text);
    }
    return text;
  }

  /** Reads the blobs `entries` of `repository` into the cache in GraphQL batches, up to BLOB_BATCH
   *  blobs and BLOB_BATCH_BYTES bytes a call: one call where each blob alone would take one. The
   *  owner and name go in as the fields `owner` and `name` (the query's variables): a `gh api
   *  graphql` call names no repository, and the pod's gh routes one by its `owner` field, so the
   *  call takes that owner's token (GH_REPO does not route it: gh reads GH_REPO only for a
   *  placeholder). A blob a batch does not give whole (binary, truncated, missing, or in a batch
   *  that fails) is left to `blob`, which reads it alone. */
  async prefetch(repository: string, entries: readonly TreeEntry[]): Promise<void> {
    const [owner = "", name = ""] = repository.split("/");
    const wanted = new Map<string, TreeEntry>();
    for (const entry of entries) {
      if (!this.blobs.has(entry.sha) && /^[0-9a-f]{40}$/.test(entry.sha))
        wanted.set(entry.sha, entry);
    }
    const batches: TreeEntry[][] = [];
    let bytes = 0;
    for (const entry of wanted.values()) {
      const last = batches[batches.length - 1];
      if (
        last === undefined ||
        last.length >= BLOB_BATCH ||
        bytes + (entry.size ?? 0) > BLOB_BATCH_BYTES
      ) {
        batches.push([entry]);
        bytes = entry.size ?? 0;
      } else {
        last.push(entry);
        bytes += entry.size ?? 0;
      }
    }
    for (const batch of batches) {
      const fields = batch
        .map(
          (entry, index) =>
            `b${index}: object(oid: "${entry.sha}") { ... on Blob { isBinary isTruncated text } }`
        )
        .join(" ");
      const query = `query($owner: String!, $name: String!) { repository(owner: $owner, name: $name) { ${fields} } }`;
      const result = await this.io.gh([
        "api",
        "graphql",
        "-f",
        `owner=${owner}`,
        "-f",
        `name=${name}`,
        "-f",
        `query=${query}`,
      ]);
      const parsed = BlobBatch.safeParse(result.code === 0 ? parseJson(result.stdout) : undefined);
      if (!parsed.success) {
        const why = firstLine(result.stderr) || "an answer not of its shape";
        this.io.err(
          `copies: a batch of ${batch.length} blobs of ${repository} failed (${why}); each is read alone`
        );
        continue;
      }
      batch.forEach((entry, index) => {
        const blob = parsed.data.data.repository?.[`b${index}`];
        if (blob?.isBinary === false && !blob.isTruncated && blob.text !== null) {
          this.blobs.set(entry.sha, Promise.resolve(blob.text));
        }
      });
    }
  }
}

async function mapLimit<T, R>(
  items: readonly T[],
  limit: number,
  fn: (item: T, index: number) => Promise<R>
): Promise<R[]> {
  const results = new Array<R>(items.length);
  let next = 0;
  const worker = async () => {
    for (let index = next++; index < items.length; index = next++) {
      results[index] = await fn(items[index] as T, index);
    }
  };
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
  return results;
}

// ---------------------------------------------------------------------------------------------
// paths

/** A repository path from parts: normalized, no leading `./`, and `""` for the root. */
export function joinPath(...parts: string[]): string {
  const joined = path.posix.normalize(path.posix.join(...parts.filter((part) => part !== "")));
  const trimmed = joined.replace(/\/+$/, "").replace(/^\.\//, "");
  return trimmed === "." ? "" : trimmed;
}

/** A path's directory, `""` for the root. */
function dirOf(file: string): string {
  const dir = path.posix.dirname(file);
  return dir === "." ? "" : dir;
}

/** A repository path as the output writes it: `.` for the root. */
function shown(dir: string): string {
  return dir === "" ? "." : dir;
}

function unquote(text: string): string {
  const match = /^(["'])(.*)\1$/s.exec(text);
  return match === null ? text : (match[2] ?? "");
}

/** A list of strings given as a list, one string, or a YAML-style flow list in one string. */
function strings(value: unknown): string[] {
  if (Array.isArray(value)) return value.filter((item): item is string => typeof item === "string");
  if (typeof value !== "string" || value.trim() === "") return [];
  const flow = /^\[(.*)\]$/s.exec(value.trim());
  if (flow === null) return [value.trim()];
  return (flow[1] ?? "")
    .split(",")
    .map((item) => unquote(item.trim()))
    .filter((item) => item !== "");
}

// ---------------------------------------------------------------------------------------------
// repositories and trees

/** One entry of a git tree, as GitHub's trees API gives it. */
export interface TreeEntry {
  readonly path: string;
  readonly mode: string;
  readonly type: string;
  readonly sha: string;
  readonly size?: number;
}

/** A repository the listing gave, and what was read of it. */
export interface Repository {
  readonly repository: string;
  readonly owner: string;
  readonly fork: boolean;
  readonly archived: boolean;
  readonly parent: string;
  readonly branch: string;
  commit: string;
  tree: TreeEntry[];
  /** `whole`, or `by directory, <n> reads` for a truncated tree. */
  treeRead: string;
  error: string;
}

/** The tree `sha` of `repository`, whole; a truncated answer is read again by directory: each
 *  subtree recursively, and one truncated too one level at a time. A truncated tree's subtrees
 *  are read at once, at most TREE_JOBS reads in flight across the run (a data repository's
 *  directory of a hundred samples is a hundred reads). */
export async function readTree(
  github: Github,
  repository: string,
  sha: string
): Promise<{ entries: TreeEntry[]; reads: number; truncated: boolean }> {
  let reads = 0;
  const read = async (treeSha: string, prefix: string, recursive: boolean) => {
    reads++;
    const endpoint = `repos/${repository}/git/trees/${treeSha}${recursive ? "?recursive=1" : ""}`;
    const tree = await github.treeRead(() => github.api(endpoint, GitTree));
    const entries = tree.tree.map(
      (entry): TreeEntry => ({
        path: prefix === "" ? entry.path : `${prefix}/${entry.path}`,
        mode: entry.mode,
        type: entry.type,
        sha: entry.sha,
        ...(entry.size === undefined ? {} : { size: entry.size }),
      })
    );
    return { entries, truncated: tree.truncated === true };
  };
  const walk = async (
    treeSha: string,
    prefix: string
  ): Promise<{ entries: TreeEntry[]; truncated: boolean }> => {
    const whole = await read(treeSha, prefix, true);
    if (!whole.truncated) return whole;
    const level = await read(treeSha, prefix, false);
    if (level.truncated)
      throw new Error(`the tree of ${shown(prefix)} is truncated even one level at a time`);
    const below = await Promise.all(
      level.entries.map((entry) =>
        entry.type === "tree" ? walk(entry.sha, entry.path) : undefined
      )
    );
    const entries: TreeEntry[] = [];
    level.entries.forEach((entry, index) => {
      entries.push(entry, ...(below[index]?.entries ?? []));
    });
    return { entries, truncated: true };
  };
  const { entries, truncated } = await walk(sha, "");
  return { entries, reads, truncated };
}

async function readRepository(github: Github, repo: Repository, at: string): Promise<void> {
  if (repo.branch === "") {
    repo.error = "the listing gives no default branch (an empty repository)";
    return;
  }
  let treeSha: string;
  try {
    const commits = await github.api(
      `repos/${repo.repository}/commits?sha=${encodeURIComponent(repo.branch)}&until=${encodeURIComponent(at)}&per_page=1`,
      Commits
    );
    const first = commits[0];
    if (first === undefined) {
      repo.error = `no commit on ${repo.branch} at or before ${at}`;
      return;
    }
    repo.commit = first.sha;
    treeSha = first.commit.tree.sha;
  } catch (error) {
    repo.error = `commit read failed: ${(error as Error).message}`;
    return;
  }
  try {
    const tree = await readTree(github, repo.repository, treeSha);
    repo.tree = tree.entries;
    repo.treeRead = tree.truncated ? `by directory, ${tree.reads} reads` : "whole";
  } catch (error) {
    repo.error = `tree read failed: ${(error as Error).message}`;
  }
}

// ---------------------------------------------------------------------------------------------
// frontmatter and skills

/** The YAML frontmatter block of a markdown text, or undefined when it has none. */
function frontmatterBlock(text: string): string | undefined {
  const lines = text.replace(/^\uFEFF/, "").split(/\r?\n/);
  if (lines[0]?.trimEnd() !== "---") return undefined;
  const end = lines.findIndex((line, index) => index > 0 && /^(---|\.\.\.)\s*$/.test(line));
  return end < 0 ? undefined : lines.slice(1, end).join("\n");
}

/** A block scalar's text (`|` or `>`, with its chomping indicator) from its lines. */
function blockScalar(header: string, lines: readonly string[]): string {
  const chomp = header.includes("-") ? "strip" : header.includes("+") ? "keep" : "clip";
  const content = [...lines];
  let trailing = 0;
  while (content.length > 0 && (content[content.length - 1] ?? "").trim() === "") {
    content.pop();
    trailing++;
  }
  const indent = Math.min(
    ...content.filter((line) => line.trim() !== "").map((line) => /^ */.exec(line)?.[0].length ?? 0)
  );
  const stripped = content.map((line) => (line.trim() === "" ? "" : line.slice(indent)));
  let body = "";
  if (header.startsWith("|")) {
    body = stripped.join("\n");
  } else {
    for (const line of stripped) {
      if (line === "") body += "\n";
      else body += body === "" || body.endsWith("\n") ? line : ` ${line}`;
    }
  }
  if (body === "" || chomp === "strip") return body;
  return chomp === "keep" ? body + "\n".repeat(trailing + 1) : `${body}\n`;
}

/** A quoted scalar's line breaks folded as YAML folds them: one break a space, n breaks n-1. */
function foldQuoted(text: string): string {
  return text.replace(/[ \t]*\n[ \t]*(\n[ \t]*)*/g, (run) => {
    const breaks = run.split("\n").length - 2;
    return breaks > 0 ? "\n".repeat(breaks) : " ";
  });
}

/** One top-level value of `lenientFields`: its text after `key:` and its continuation lines. */
function lenientValue(first: string, more: readonly string[]): unknown {
  if (/^[|>][-+0-9]*$/.test(first)) return blockScalar(first, more);
  const joined = [first, ...more.map((line) => line.trim())].join("\n").trim();
  if (first.startsWith('"')) {
    const inner = foldQuoted(/^"((?:[^"\\]|\\.)*)"/s.exec(joined)?.[1] ?? joined.slice(1));
    const parsed = parseJson(`"${inner.replace(/\n/g, "\\n").replace(/\t/g, "\\t")}"`);
    return typeof parsed === "string" ? parsed : inner;
  }
  if (first.startsWith("'")) {
    return foldQuoted(/^'((?:[^']|'')*)'/s.exec(joined)?.[1] ?? joined.slice(1)).replace(
      /''/g,
      "'"
    );
  }
  if (first.startsWith("[")) return strings(joined.replace(/\s*\n\s*/g, " "));
  if (first === "" && more.some((line) => /^\s*-\s/.test(line))) {
    return more
      .map((line) => /^\s*-\s+(.*)$/.exec(line)?.[1])
      .filter((item): item is string => item !== undefined)
      .map((item) => unquote(item.trim()));
  }
  return [first, ...more.map((line) => line.trim())]
    .map((line) => line.replace(/\s+#.*$/, ""))
    .filter((line) => line !== "")
    .join(" ");
}

/**
 * The top-level fields of a frontmatter block that is not valid YAML, read leniently: each line
 * `key: value` at the left margin starts a field, and the indented or blank lines after it continue
 * it. A value is plain (its lines joined by spaces), double- or single-quoted (over lines, folded),
 * a folded `>` or literal `|` block with its chomping indicator, a flow list `[a, b]` or a block
 * list of `- item` lines.
 */
export function lenientFields(block: string): Record<string, unknown> {
  const lines = block.split(/\r?\n/);
  const fields: Record<string, unknown> = {};
  let index = 0;
  while (index < lines.length) {
    const key = /^([A-Za-z_][\w-]*):(?:[ \t]+(.*)|[ \t]*)$/.exec(lines[index] ?? "");
    index++;
    if (key === null) continue;
    const more: string[] = [];
    while (index < lines.length && /^(\s+\S|\s*$)/.test(lines[index] ?? "")) {
      more.push(lines[index] ?? "");
      index++;
    }
    fields[key[1] ?? ""] = lenientValue((key[2] ?? "").trim(), more);
  }
  return fields;
}

const YamlMapping = z.record(z.string(), z.unknown());

/** The frontmatter fields of a markdown text: YAML, else `lenientFields`; undefined with none. */
export function frontmatter(text: string): Record<string, unknown> | undefined {
  const block = frontmatterBlock(text);
  if (block === undefined) return undefined;
  try {
    const parsed = YamlMapping.safeParse(Bun.YAML.parse(block));
    if (parsed.success) return parsed.data;
  } catch {
    // Not valid YAML (a description holding `: ` unquoted is common): read it leniently below.
  }
  return lenientFields(block);
}

/** A skill copy: one `SKILL.md` of one repository at the commit read. */
export interface SkillCopy {
  readonly repository: string;
  /** The skill's directory, `""` for the root. */
  readonly path: string;
  readonly file: string;
  readonly blob: string;
  readonly fork: boolean;
  readonly archived: boolean;
  readonly parent: string;
  readonly name: string;
  readonly nameFrom: "frontmatter" | "directory";
  readonly description: string;
  readonly descriptionBytes: number;
  /** Why the name or description could not be read, or `""`. */
  readonly fault: string;
}

/** What a `SKILL.md` text gives of its skill. */
export type SkillFields = Pick<
  SkillCopy,
  "name" | "nameFrom" | "description" | "descriptionBytes" | "fault"
>;

/** A skill's name and description from its `SKILL.md` text, its directory's name standing in for
 *  a missing frontmatter name. */
export function skillFields(dir: string, repository: string, text: string): SkillFields {
  const fields = frontmatter(text);
  const fallback = dir === "" ? (repository.split("/")[1] ?? repository) : path.posix.basename(dir);
  if (fields === undefined) {
    return {
      name: fallback,
      nameFrom: "directory",
      description: "",
      descriptionBytes: 0,
      fault: "no frontmatter",
    };
  }
  const name = typeof fields.name === "string" ? fields.name.trim() : "";
  const raw = fields.description;
  const description =
    typeof raw === "string" ? raw : raw === undefined || raw === null ? "" : String(raw);
  return {
    name: name === "" ? fallback : name,
    nameFrom: name === "" ? "directory" : "frontmatter",
    description,
    descriptionBytes: Buffer.byteLength(description, "utf8"),
    fault: raw === undefined ? "no description" : "",
  };
}

// ---------------------------------------------------------------------------------------------
// manifests

/**
 * The manifest kinds read from the trees, each by its path. `listed` is `always` for a kind every
 * file of which is shown (the few files that say where skills go), and `when of its kind` for one
 * shown only when it names skills (a package.json with omp.skills, an agent that loads skills, an
 * installer that installs a plugin or links skills); the others are counted.
 */
export const MANIFEST_KINDS = [
  {
    kind: "claude-marketplace",
    about: "a Claude Code marketplace: the plugins it lists and their sources",
    path: /(^|\/)\.claude-plugin\/marketplace\.json$/,
    listed: "always",
  },
  {
    kind: "claude-plugin",
    about: "a Claude Code plugin manifest: its name and skills",
    path: /(^|\/)\.claude-plugin\/plugin\.json$/,
    listed: "always",
  },
  {
    kind: "omp-plugins",
    about: "the Oh My Pi plugins a profile installs (a package.json of dependencies)",
    path: /(^|\/)(omp-plugins|omp\/plugins|\.omp\/plugins)\/package\.json$/,
    listed: "always",
  },
  {
    kind: "omp-package",
    about: "an Oh My Pi plugin package: a package.json with omp.skills",
    path: /(^|\/)package\.json$/,
    listed: "when of its kind",
  },
  {
    kind: "claude-settings",
    about: "Claude Code settings: the plugins enabled and the marketplaces known",
    path: /(^|\/)\.claude\/settings(\.local)?\.json$/,
    listed: "when of its kind",
  },
  {
    kind: "omp-config",
    about: "an Oh My Pi config.yml: skills.ignoredSkills",
    path: /(^|\/)(omp|\.omp|\.omp\/agent)\/config\.yml$/,
    listed: "when of its kind",
  },
  {
    kind: "agent-definition",
    about:
      "an agent definition whose frontmatter loads skills (autoloadSkills, or Claude Code's skills)",
    path: /(^|\/)agents\/[^/]+\.md$/,
    listed: "when of its kind",
  },
  {
    kind: "legion-project",
    about: "a Legion project file: verificationSkillsLeaveOut",
    path: /(^|\/)legion\/projects\/[^/]+\.ya?ml$/,
    listed: "always",
  },
  {
    kind: "prepack-script",
    about: "the prepack script that copies skills/ into the Oh My Pi plugin packages",
    path: /(^|\/)scripts\/pi-plugin-prepack\.sh$/,
    listed: "always",
  },
  {
    kind: "build-script",
    about: "the Claude Code plugin build that stages a package's skills into a generated plugin",
    path: /(^|\/)packages\/claude-envoy\/scripts\/build\.ts$/,
    listed: "always",
  },
  {
    kind: "installer-script",
    about: "an installer script that installs plugins or links skills into a host",
    path: /(^|\/)installers\/[^/]+\.sh$/,
    listed: "when of its kind",
  },
  {
    kind: "python-loader",
    about: "a Python loader naming the skills a session loads (a skills = [...] list)",
    path: /(^|\/)tree\/init\.py$/,
    listed: "always",
  },
  {
    kind: "skill-link",
    about: "a symbolic link (mode 120000) in a skills/ directory, pointing at a skill",
    path: /(^|\/)skills\/[^/]+$/,
    listed: "always",
  },
] as const;

export type ManifestKind = (typeof MANIFEST_KINDS)[number]["kind"];

/** The manifest kind of a tree entry, by its path (and, for a link, its mode). */
export function manifestKind(
  entry: Pick<TreeEntry, "path" | "mode" | "type">
): ManifestKind | undefined {
  if (entry.type !== "blob" || entry.path.split("/").includes("node_modules")) return undefined;
  if (entry.mode === "120000")
    return /(^|\/)skills\/[^/]+$/.test(entry.path) ? "skill-link" : undefined;
  return MANIFEST_KINDS.find((kind) => kind.kind !== "skill-link" && kind.path.test(entry.path))
    ?.kind;
}

export type Host = "omp" | "claude-code" | "any";

/** What a manifest says, before it is resolved against the copies. */
type Fact =
  | {
      readonly type: "marketplace-plugin";
      readonly marketplace: string;
      readonly plugin: string;
      readonly root: string | undefined;
      readonly repo: string;
      readonly npm: string;
      readonly skills: readonly string[];
      readonly other: string;
    }
  | {
      readonly type: "plugin-manifest";
      readonly plugin: string;
      readonly root: string;
      readonly skills: readonly string[];
    }
  | {
      readonly type: "package";
      readonly name: string;
      readonly root: string;
      readonly ompSkills: readonly string[];
    }
  | {
      readonly type: "plugin-switch";
      readonly host: Host;
      readonly plugin: string;
      readonly marketplace: string;
      readonly action: "enables" | "disables" | "installs";
    }
  | { readonly type: "marketplace-source"; readonly marketplace: string; readonly repo: string }
  | { readonly type: "ignores"; readonly patterns: readonly string[] }
  | {
      readonly type: "loads";
      readonly host: Host;
      readonly unit: string;
      readonly skills: readonly string[];
    }
  | { readonly type: "leaves-out"; readonly project: string; readonly plugins: readonly string[] }
  | { readonly type: "installs-package"; readonly package: string; readonly version: string }
  | {
      readonly type: "stages";
      readonly package: string;
      readonly sourceDir: string;
      readonly destDir: string;
      readonly skills: readonly string[];
    }
  | {
      readonly type: "generated-plugin";
      readonly plugin: string;
      readonly package: string;
      readonly packageRoot: string;
    }
  | {
      readonly type: "links-skills";
      readonly host: Host;
      readonly dir: string;
      readonly assumed: string;
    }
  | { readonly type: "links-config"; readonly file: string }
  | { readonly type: "skill-link"; readonly link: string; readonly target: string };

/** What one manifest file says. */
export interface ParsedManifest {
  readonly facts: readonly Fact[];
  /** Whether the file is of its kind (it names skills, plugins or packages). */
  readonly ofKind: boolean;
  /** What it says, in one line. */
  readonly says: string;
}

/** One manifest file read. */
export interface Manifest extends ParsedManifest {
  readonly repository: string;
  readonly path: string;
  readonly kind: ManifestKind;
  readonly source: "tree" | "code search";
  readonly error: string;
  rows: number;
}

/** A string list as manifests write one: a list, or one string. */
const StringList = z.union([z.string(), z.array(z.string())]).optional();
const MarketplaceFile = z.looseObject({
  name: z.string(),
  metadata: z.looseObject({ pluginRoot: z.string().optional() }).optional(),
  plugins: z.array(
    z.looseObject({
      name: z.string(),
      source: z.union([
        z.string(),
        z.looseObject({
          source: z.string(),
          repo: z.string().optional(),
          package: z.string().optional(),
          url: z.string().optional(),
        }),
      ]),
      skills: StringList,
    })
  ),
});
const PluginFile = z.looseObject({
  name: z.string().optional(),
  version: z.string().optional(),
  skills: StringList,
});
const PackageFile = z.looseObject({
  name: z.string().optional(),
  omp: z.looseObject({ skills: StringList }).optional(),
});
const PluginListFile = z.looseObject({
  name: z.string().optional(),
  dependencies: z.record(z.string(), z.string()).optional(),
});
const SettingsFile = z.looseObject({
  enabledPlugins: z.record(z.string(), z.boolean()).optional(),
  extraKnownMarketplaces: z
    .record(
      z.string(),
      z.looseObject({
        source: z
          .looseObject({ repo: z.string().optional(), url: z.string().optional() })
          .optional(),
      })
    )
    .optional(),
});
const OmpConfigFile = z.looseObject({
  skills: z.looseObject({ ignoredSkills: z.array(z.string()).optional() }).nullish(),
});
const ProjectFile = z.looseObject({
  key: z.string().optional(),
  verificationSkillsLeaveOut: z.array(z.string()).nullish(),
});

/** `text` as JSON or YAML, parsed by `schema`; throws naming each fault. */
function parseFile<T>(schema: z.ZodType<T>, text: string, format: "json" | "yaml"): T {
  const value = format === "json" ? (JSON.parse(text) as unknown) : Bun.YAML.parse(text);
  const parsed = schema.safeParse(value);
  if (!parsed.success) throw new Error(`not of its shape: ${schemaFaults(parsed.error)}`);
  return parsed.data;
}

/** The `owner/name` of a GitHub URL, or `""`. */
function githubRepository(url: string): string {
  return /github\.com[/:]([\w.-]+\/[\w.-]+?)(?:\.git)?\/?$/.exec(url)?.[1] ?? "";
}

function parseMarketplace(file: string, text: string): ParsedManifest {
  const value = parseFile(MarketplaceFile, text, "json");
  const base = dirOf(dirOf(file));
  const facts: Fact[] = [];
  const named: string[] = [];
  for (const entry of value.plugins) {
    const source = entry.source;
    let root: string | undefined;
    let repo = "";
    let npm = "";
    let other = "";
    if (typeof source === "string" && !source.includes("://")) {
      root = joinPath(
        base,
        source.startsWith(".") ? "" : (value.metadata?.pluginRoot ?? ""),
        source
      );
    } else if (typeof source === "string") {
      repo = githubRepository(source);
      if (repo === "") other = source;
    } else {
      if (source.source === "github") repo = source.repo ?? "";
      else if (source.source === "npm") npm = source.package ?? "";
      else if (source.source === "url" || source.source === "git")
        repo = githubRepository(source.url ?? "");
      if (repo === "" && npm === "") other = JSON.stringify(source);
    }
    const plugin = entry.name;
    facts.push({
      type: "marketplace-plugin",
      marketplace: value.name,
      plugin,
      root,
      repo,
      npm,
      skills: strings(entry.skills),
      other,
    });
    const from =
      root !== undefined
        ? shown(root)
        : repo !== ""
          ? `github ${repo}`
          : npm !== ""
            ? `npm ${npm}`
            : other;
    named.push(`${plugin} (${from})`);
  }
  return {
    facts,
    ofKind: true,
    says: `marketplace ${value.name}: ${named.join(", ") || "no plugins"}`,
  };
}

function parsePluginManifest(file: string, text: string): ParsedManifest {
  const value = parseFile(PluginFile, text, "json");
  const root = dirOf(dirOf(file));
  const plugin = value.name ?? path.posix.basename(root);
  const skills = strings(value.skills);
  return {
    facts: [{ type: "plugin-manifest", plugin, root, skills }],
    ofKind: true,
    says: `plugin ${plugin}${value.version ? ` ${value.version}` : ""}: skills ${skills.length > 0 ? skills.join(", ") : "from skills/"}`,
  };
}

function parsePackage(file: string, text: string): ParsedManifest {
  const value = parseFile(PackageFile, text, "json");
  const ompSkills = strings(value.omp?.skills);
  const name = value.name ?? "";
  return {
    facts: [{ type: "package", name, root: dirOf(file), ompSkills }],
    ofKind: ompSkills.length > 0,
    says: `package ${name}: omp.skills ${ompSkills.join(", ") || "none"}`,
  };
}

function parsePluginList(file: string, text: string): ParsedManifest {
  const value = parseFile(PluginListFile, text, "json");
  const dependencies = Object.entries(value.dependencies ?? {});
  const facts: Fact[] = dependencies.map(([name, version]) => ({
    type: "installs-package",
    package: name,
    version,
  }));
  facts.push({ type: "package", name: value.name ?? "", root: dirOf(file), ompSkills: [] });
  return {
    facts,
    ofKind: true,
    says: `installs ${dependencies.map(([name, version]) => `${name}@${version}`).join(", ") || "nothing"}`,
  };
}

function parseClaudeSettings(text: string): ParsedManifest {
  const value = parseFile(SettingsFile, text, "json");
  const facts: Fact[] = [];
  const enabled = Object.entries(value.enabledPlugins ?? {});
  for (const [key, on] of enabled) {
    const [plugin = "", marketplace = ""] = key.split("@");
    facts.push({
      type: "plugin-switch",
      host: "claude-code",
      plugin,
      marketplace,
      action: on ? "enables" : "disables",
    });
  }
  const known = Object.entries(value.extraKnownMarketplaces ?? {});
  for (const [marketplace, entry] of known) {
    const repo = entry.source?.repo ?? githubRepository(entry.source?.url ?? "");
    if (repo !== "") facts.push({ type: "marketplace-source", marketplace, repo });
  }
  return {
    facts,
    ofKind: facts.length > 0,
    says: `plugins ${enabled.map(([key, on]) => `${key} ${on ? "on" : "off"}`).join(", ") || "none"}; marketplaces ${
      known.map(([marketplace]) => marketplace).join(", ") || "none"
    }`,
  };
}

function parseOmpConfig(text: string): ParsedManifest {
  const patterns = parseFile(OmpConfigFile, text, "yaml").skills?.ignoredSkills ?? [];
  return {
    facts: patterns.length > 0 ? [{ type: "ignores", patterns }] : [],
    ofKind: patterns.length > 0,
    says: `skills.ignoredSkills ${patterns.join(", ") || "none"}`,
  };
}

function parseAgent(file: string, text: string): ParsedManifest {
  const fields = frontmatter(text) ?? {};
  const agent = typeof fields.name === "string" ? fields.name : path.posix.basename(file, ".md");
  const autoload = strings(fields.autoloadSkills);
  const claude = strings(fields.skills);
  const facts: Fact[] = [];
  if (autoload.length > 0)
    facts.push({ type: "loads", host: "omp", unit: `agent:${agent}`, skills: autoload });
  if (claude.length > 0)
    facts.push({ type: "loads", host: "claude-code", unit: `agent:${agent}`, skills: claude });
  const says = [
    autoload.length > 0 ? `autoloadSkills ${autoload.join(", ")}` : "",
    claude.length > 0 ? `skills ${claude.join(", ")}` : "",
  ].filter((part) => part !== "");
  return {
    facts,
    ofKind: facts.length > 0,
    says: `agent ${agent}: ${says.join("; ") || "loads no skill"}`,
  };
}

function parseLegionProject(file: string, text: string): ParsedManifest {
  const value = parseFile(ProjectFile, text, "yaml");
  const project = value.key ?? path.posix.basename(file).replace(/\.ya?ml$/, "");
  const plugins = value.verificationSkillsLeaveOut ?? [];
  return {
    facts: plugins.length > 0 ? [{ type: "leaves-out", project, plugins }] : [],
    ofKind: true,
    says: `project ${project}: verificationSkillsLeaveOut ${plugins.join(", ") || "none"}`,
  };
}

/** The prepack script: each `case` arm's package name and the `skills=( … )` list under it,
 *  copied from `$root/<dir>/$skill` (the repository's `skills/`) into the directory its last
 *  `stage_skills <dir>` names (`dist/skills`). */
function parsePrepack(file: string, text: string): ParsedManifest {
  const sourceDir = joinPath(
    dirOf(dirOf(file)),
    /"\$root\/([\w./-]+)\/\$skill"/.exec(text)?.[1] ?? "skills"
  );
  const destDir = /^\s*stage_skills\s+([^\s"'$]+)\s*$/m.exec(text)?.[1] ?? "dist/skills";
  const lines = text.split("\n");
  const facts: Fact[] = [];
  const says: string[] = [];
  let arm = "";
  for (let index = 0; index < lines.length; index++) {
    const line = (lines[index] ?? "").trim();
    const armLine = /^([@\w./-]+)\)$/.exec(line);
    if (armLine !== null) {
      arm = armLine[1] ?? "";
      continue;
    }
    if (line === ";;") arm = "";
    if (arm === "" || !line.startsWith("skills=(")) continue;
    let body = line.slice("skills=(".length);
    while (!body.includes(")") && index + 1 < lines.length) {
      index++;
      body += ` ${lines[index] ?? ""}`;
    }
    const skills = body
      .slice(0, body.indexOf(")"))
      .split(/\s+/)
      .filter((word) => word !== "");
    facts.push({ type: "stages", package: arm, sourceDir, destDir, skills });
    says.push(`${arm}: ${skills.join(" ")}`);
  }
  return {
    facts,
    ofKind: facts.length > 0,
    says: `stages ${shown(sourceDir)}/<name> into ${destDir}: ${says.join("; ")}`,
  };
}

/** The Claude Code build script: the package whose staged skills its generated plugin ships. */
function parseBuildScript(file: string, text: string): ParsedManifest {
  const staged = /"--stage-skills",\s*"([^"]+)"/.exec(text)?.[1];
  const plugin = /"plugin\.json"\s*\)[\s\S]{0,400}?\bname:\s*"([^"]+)"/.exec(text)?.[1];
  if (staged === undefined || plugin === undefined) {
    return {
      facts: [],
      ofKind: false,
      says: "no --stage-skills package or generated plugin name found",
    };
  }
  return {
    facts: [{ type: "generated-plugin", plugin, package: staged, packageRoot: dirOf(dirOf(file)) }],
    ofKind: true,
    says: `generates the plugin ${plugin} from the skills prepack stages for ${staged}`,
  };
}

/** Shell variable assignments of a script, the last one of each name winning. */
function shellVariables(text: string): Map<string, string> {
  const vars = new Map<string, string>();
  for (const match of text.matchAll(
    /^\s*(?:local\s+|export\s+|readonly\s+)?([A-Za-z_]\w*)=(\S.*)$/gm
  )) {
    const value = (match[2] ?? "").trim();
    if (!value.startsWith("(")) vars.set(match[1] ?? "", unquote(value));
  }
  return vars;
}

/** A shell path as a repository path: a leading variable is replaced by its assignment, and one
 *  that is unknown or computes the script's own directory is taken as the repository's root. */
function shellPath(
  text: string,
  vars: Map<string, string>,
  depth = 0
): { dir: string; assumed: string } {
  const bare = text.replace(/["']/g, "");
  const variable = /^\$\{?([A-Za-z_]\w*)\}?(.*)$/.exec(bare);
  if (variable === null) return { dir: joinPath(bare), assumed: "" };
  const name = variable[1] ?? "";
  const rest = variable[2] ?? "";
  const value = vars.get(name);
  if (value === undefined || depth > 5 || /dirname|BASH_SOURCE|pwd/.test(value)) {
    return { dir: joinPath(rest.replace(/^\/+/, "")), assumed: value === undefined ? name : "" };
  }
  return shellPath(value + rest, vars, depth + 1);
}

/** An installer script: plugin installs (`<plugin>@<marketplace>` on a line holding `plugin`),
 *  loops that link each skill of a directory into a host's user skills, and the Oh My Pi
 *  config.yml it links. */
function parseInstaller(file: string, text: string): ParsedManifest {
  const script = path.posix.basename(file);
  const scriptHost: Host = script.includes("omp")
    ? "omp"
    : script.includes("claude")
      ? "claude-code"
      : "any";
  const vars = shellVariables(text);
  const lines = text.split("\n");
  const facts: Fact[] = [];
  const says: string[] = [];
  for (const raw of lines) {
    const line = raw.trim();
    if (line.startsWith("#") || /^(echo|printf)\b/.test(line) || !/plugin/i.test(line)) continue;
    for (const match of line.matchAll(
      /(?<![\w.@/$%{-])([A-Za-z0-9][\w.-]*)@([A-Za-z0-9][\w-]*)(?![\w.@-])/g
    )) {
      const host: Host = /\bomp\b/.test(line)
        ? "omp"
        : /\bclaude\b/.test(line)
          ? "claude-code"
          : scriptHost;
      const plugin = match[1] ?? "";
      const marketplace = match[2] ?? "";
      facts.push({ type: "plugin-switch", host, plugin, marketplace, action: "installs" });
      says.push(`installs ${plugin}@${marketplace} (${host})`);
      const repo = line
        .split(/\s+/)
        .map(unquote)
        .find((word) => /^[A-Za-z0-9][\w-]*\/[\w.-]+$/.test(word));
      if (repo !== undefined) facts.push({ type: "marketplace-source", marketplace, repo });
    }
  }
  for (let index = 0; index < lines.length; index++) {
    const loop = /^\s*for\s+\w+\s+in\s+(.+?)\s*;\s*do\b/.exec(lines[index] ?? "");
    if (loop === null) continue;
    let depth = 1;
    const body: string[] = [];
    for (let next = index + 1; next < lines.length && depth > 0; next++) {
      const line = lines[next] ?? "";
      if (/^\s*(for|while|until)\b.*\bdo\b/.test(line)) depth++;
      if (/^\s*done\b/.test(line)) depth--;
      body.push(line);
    }
    const hosts: Host[] = [];
    if (body.some((line) => line.includes(".claude/skills"))) hosts.push("claude-code");
    if (body.some((line) => line.includes(".omp/agent/skills"))) hosts.push("omp");
    const { dir, assumed } = shellPath((loop[1] ?? "").replace(/\/?\*\/?$/, ""), vars);
    for (const host of hosts) {
      facts.push({ type: "links-skills", host, dir, assumed });
      const root = assumed === "" ? "" : ` (taking $${assumed} as the repository root)`;
      says.push(`links each skill of ${shown(dir)}/ into the ${host} user skills${root}`);
    }
  }
  for (const raw of lines) {
    if (!raw.includes(".omp/agent/config.yml") || raw.trim().startsWith("#")) continue;
    const source = raw
      .trim()
      .split(/\s+/)
      .map(unquote)
      .find((word) => word.endsWith("config.yml") && !word.includes(".omp/agent/config.yml"));
    if (source === undefined || !source.startsWith("$")) continue;
    const { dir } = shellPath(source, vars);
    facts.push({ type: "links-config", file: dir });
    says.push(`links ${dir} as the Oh My Pi config.yml`);
  }
  return {
    facts,
    ofKind: facts.length > 0,
    says: says.join("; ") || "installs no skill or plugin",
  };
}

const SKILLS_LIST = /["']?\bskills["']?\s*[=:]\s*\[([^\]]*)\]/;

/** A Python loader: each `skills = [ … ]` list of quoted names (in code, or in a TOML template it
 *  holds), with the `[roles.<role>]` table it sits under. */
function parsePythonLoader(text: string): ParsedManifest {
  const facts: Fact[] = [];
  const says: string[] = [];
  const roles = [...text.matchAll(/^\[roles\.([\w-]+)\]/gm)];
  for (const match of text.matchAll(new RegExp(SKILLS_LIST.source, "g"))) {
    const skills = [...(match[1] ?? "").matchAll(/["']([^"'\n]+)["']/g)].map(
      (item) => item[1] ?? ""
    );
    if (skills.length === 0) continue;
    const role = roles.filter((header) => (header.index ?? 0) < (match.index ?? 0)).pop()?.[1];
    const unit = role === undefined ? "list" : `roles.${role}`;
    facts.push({ type: "loads", host: "any", unit, skills });
    says.push(`${unit}: ${skills.join(", ")}`);
  }
  return {
    facts,
    ofKind: facts.length > 0,
    says: says.length === 0 ? "no skills list" : `loads ${says.join("; ")}`,
  };
}

/** The facts of one manifest file's text; throws when the text is not of its format. */
export function parseManifest(kind: ManifestKind, file: string, text: string): ParsedManifest {
  switch (kind) {
    case "claude-marketplace":
      return parseMarketplace(file, text);
    case "claude-plugin":
      return parsePluginManifest(file, text);
    case "omp-plugins":
      return parsePluginList(file, text);
    case "omp-package":
      return parsePackage(file, text);
    case "claude-settings":
      return parseClaudeSettings(text);
    case "omp-config":
      return parseOmpConfig(text);
    case "agent-definition":
      return parseAgent(file, text);
    case "legion-project":
      return parseLegionProject(file, text);
    case "prepack-script":
      return parsePrepack(file, text);
    case "build-script":
      return parseBuildScript(file, text);
    case "installer-script":
      return parseInstaller(file, text);
    case "python-loader":
      return parsePythonLoader(text);
    case "skill-link": {
      const target = joinPath(dirOf(file), text.trim());
      return {
        facts: [{ type: "skill-link", link: file, target }],
        ofKind: true,
        says: `links ${text.trim()} (${shown(target)})`,
      };
    }
  }
}

// ---------------------------------------------------------------------------------------------
// installers: the units that ship skills, and what installs each unit

/** One installer row: a manifest's action on one skill copy (or on a name no copy has). */
export interface InstallerRow {
  readonly manifestRepository: string;
  readonly manifestPath: string;
  readonly kind: ManifestKind;
  readonly action: string;
  readonly unit: string;
  readonly host: Host;
  readonly skill: string;
  readonly copyRepository: string;
  /** The copy's directory, or undefined when the row reaches no copy read. */
  readonly copyPath: string | undefined;
  readonly note: string;
}

/** A skill a unit ships: the copy, and its path inside the unit as installed. */
interface Member {
  readonly copy: SkillCopy;
  readonly rel: string;
}

/** Something that ships skills into sessions: a plugin, a package, a host's user skills. */
interface Unit {
  readonly id: string;
  readonly host: Host;
  readonly manifest: Manifest;
  readonly action: string;
  readonly members: readonly Member[];
  /** Where the unit is installed: prefix patterns, each ending in `/`, a member's `rel` after. */
  readonly locations: readonly string[];
  readonly note: string;
}

/** A row of the copy-path prefix table. */
export interface PrefixRow {
  readonly prefix: string;
  readonly repository: string;
  readonly path: string;
  readonly name: string;
  readonly via: string;
}

/** The copies of every repository read, indexed for the installers' lookups. */
export class CopyIndex {
  private readonly byDir = new Map<string, SkillCopy>();
  private readonly links = new Map<string, string>();
  readonly byName = new Map<string, SkillCopy[]>();

  constructor(readonly copies: readonly SkillCopy[]) {
    for (const copy of copies) {
      this.byDir.set(`${copy.repository}\0${copy.path}`, copy);
      this.byName.set(copy.name, [...(this.byName.get(copy.name) ?? []), copy]);
    }
  }

  addLink(repository: string, link: string, target: string): void {
    this.links.set(`${repository}\0${link}`, target);
  }

  /** The copy whose directory is `dir`, directly or through a link. */
  at(repository: string, dir: string): SkillCopy | undefined {
    const target = this.links.get(`${repository}\0${dir}`);
    return (
      this.byDir.get(`${repository}\0${dir}`) ??
      (target === undefined ? undefined : this.byDir.get(`${repository}\0${target}`))
    );
  }

  /** The copies directly under `dir` (a skills directory), each with its child name. */
  childrenOf(repository: string, dir: string): { child: string; copy: SkillCopy }[] {
    const found = new Map<string, SkillCopy>();
    for (const copy of this.copies) {
      if (copy.repository === repository && copy.path !== "" && dirOf(copy.path) === dir) {
        found.set(path.posix.basename(copy.path), copy);
      }
    }
    for (const [key, target] of this.links) {
      const [repo = "", link = ""] = key.split("\0");
      const copy = this.byDir.get(`${repo}\0${target}`);
      if (repo === repository && dirOf(link) === dir && copy !== undefined)
        found.set(path.posix.basename(link), copy);
    }
    return [...found]
      .map(([child, copy]) => ({ child, copy }))
      .sort((a, b) => a.child.localeCompare(b.child));
  }

  /** The members a skill path list gives under a root: each path a skill directory, or a
   *  directory of skill directories. */
  membersAt(repository: string, root: string, rels: readonly string[]): Member[] {
    const members = new Map<string, Member>();
    for (const rel of rels) {
      const dir = joinPath(root, rel);
      const copy = this.at(repository, dir);
      const found =
        copy !== undefined
          ? [{ copy, rel: joinPath(rel) }]
          : this.childrenOf(repository, dir).map((child) => ({
              copy: child.copy,
              rel: joinPath(rel, child.child),
            }));
      for (const member of found)
        members.set(`${member.copy.repository}\0${member.copy.path}\0${member.rel}`, member);
    }
    return [...members.values()];
  }
}

/** A glob of skill names (`*` any run of characters) as a regular expression. */
function nameGlob(pattern: string): RegExp {
  return new RegExp(`^${pattern.split("*").map(escapeRegExp).join(".*")}$`);
}

/** What the installers give: their rows, the units that ship skills, and the prefix rows. */
export interface Resolved {
  readonly rows: InstallerRow[];
  readonly units: Unit[];
  readonly prefixes: PrefixRow[];
}

/** The facts of one type across the manifests, each with its manifest. */
function factsOf<T extends Fact["type"]>(
  manifests: readonly Manifest[],
  type: T
): { manifest: Manifest; fact: Extract<Fact, { type: T }> }[] {
  return manifests.flatMap((manifest) =>
    manifest.facts
      .filter((fact): fact is Extract<Fact, { type: T }> => fact.type === type)
      .map((fact) => ({ manifest, fact }))
  );
}

/** The installer rows, units and prefix rows of the manifests read, against the copies. */
export function resolveInstallers(manifests: readonly Manifest[], index: CopyIndex): Resolved {
  const rows: InstallerRow[] = [];
  const units: Unit[] = [];
  const row = (
    manifest: Manifest,
    action: string,
    unit: string,
    host: Host,
    skill: string,
    copy: SkillCopy | undefined,
    note = ""
  ) =>
    rows.push({
      manifestRepository: manifest.repository,
      manifestPath: manifest.path,
      kind: manifest.kind,
      action,
      unit,
      host,
      skill,
      copyRepository: copy?.repository ?? "",
      copyPath: copy?.path,
      note,
    });

  const links = factsOf(manifests, "skill-link");
  for (const { manifest, fact } of links)
    index.addLink(manifest.repository, fact.link, fact.target);
  for (const { manifest, fact } of links) {
    const copy = index.at(manifest.repository, fact.target);
    row(
      manifest,
      "links",
      `link:${fact.link}`,
      "any",
      copy?.name ?? "",
      copy,
      copy ? "" : `no skill at ${shown(fact.target)}`
    );
  }

  // Every package.json read, by name, for npm sources and plugin lists.
  const packages = new Map<
    string,
    { manifest: Manifest; root: string; ompSkills: readonly string[] }
  >();
  for (const { manifest, fact } of factsOf(manifests, "package")) {
    if (fact.name !== "" && !packages.has(fact.name))
      packages.set(fact.name, { manifest, root: fact.root, ompSkills: fact.ompSkills });
  }
  const stages = factsOf(manifests, "stages");
  const builds = factsOf(manifests, "generated-plugin");
  /** The copies the prepack script stages for a package, at `<dir>/<name>` (dist/skills). */
  const staged = (name: string, dir?: string): Member[] =>
    stages
      .filter((stage) => stage.fact.package === name)
      .flatMap((stage) =>
        stage.fact.skills.flatMap((skill) => {
          const copy = index.at(stage.manifest.repository, joinPath(stage.fact.sourceDir, skill));
          return copy === undefined
            ? []
            : [{ copy, rel: joinPath(dir ?? stage.fact.destDir, skill) }];
        })
      );
  const imageRoot = (name: string) => `/opt/legion/${name.split("/").pop() ?? name}/`;

  // Oh My Pi plugin packages (package.json with omp.skills): skills in the tree, or staged.
  const packageUnits = new Map<string, Unit>();
  for (const [name, entry] of packages) {
    if (entry.ompSkills.length === 0) continue;
    const members = [
      ...index.membersAt(entry.manifest.repository, entry.root, entry.ompSkills),
      ...staged(name),
    ];
    const inImage =
      stages.some((stage) => stage.fact.package === name) ||
      builds.some((build) => build.fact.packageRoot === entry.root);
    const unit: Unit = {
      id: `package:${name}`,
      host: "omp",
      manifest: entry.manifest,
      action: "ships",
      members,
      locations: inImage
        ? [`**/node_modules/${name}/`, imageRoot(name)]
        : [`**/node_modules/${name}/`],
      note:
        members.length > 0
          ? ""
          : `omp.skills ${entry.ompSkills.join(", ")} holds no SKILL.md in the tree, and no prepack script stages it`,
    };
    packageUnits.set(name, unit);
    units.push(unit);
  }
  for (const { manifest, fact } of stages) {
    for (const skill of fact.skills) {
      const copy = index.at(manifest.repository, joinPath(fact.sourceDir, skill));
      const note =
        copy === undefined
          ? `no SKILL.md at ${joinPath(fact.sourceDir, skill)}`
          : packageUnits.has(fact.package)
            ? ""
            : `no package.json of ${fact.package} with omp.skills was read`;
      row(manifest, "stages", `package:${fact.package}`, "omp", skill, copy, note);
    }
  }

  // The plugin the build script generates from a package's staged skills.
  for (const { manifest, fact } of builds) {
    const members = staged(fact.package, "skills");
    units.push({
      id: `plugin:${fact.plugin}`,
      host: "claude-code",
      manifest,
      action: "ships",
      members,
      locations: [`/opt/legion/${path.posix.basename(fact.packageRoot)}/${fact.plugin}/`],
      note: members.length > 0 ? "" : `the prepack script stages nothing for ${fact.package}`,
    });
  }

  // Claude Code plugins: from marketplaces, and plugin.json files no marketplace names.
  const pluginManifests = factsOf(manifests, "plugin-manifest");
  const claimed = new Set<Manifest>();
  const pluginUnits = new Map<string, Unit>();
  for (const { manifest, fact } of factsOf(manifests, "marketplace-plugin")) {
    let repository = manifest.repository;
    let root = fact.root;
    let note = "";
    if (root === undefined && fact.repo !== "") {
      repository = fact.repo;
      root = "";
    } else if (root === undefined && fact.npm !== "") {
      const found = packages.get(fact.npm);
      if (found !== undefined) {
        repository = found.manifest.repository;
        root = found.root;
      } else note = `the npm package ${fact.npm} is in no package.json read`;
    } else if (root === undefined) note = `the source ${fact.other} is not read`;
    const own = pluginManifests.find(
      (entry) => entry.manifest.repository === repository && entry.fact.root === root
    );
    if (own !== undefined) claimed.add(own.manifest);
    const members =
      root === undefined
        ? []
        : index.membersAt(repository, root, [
            "skills",
            ...fact.skills,
            ...(own?.fact.skills ?? []),
          ]);
    const id = `plugin:${fact.plugin}@${fact.marketplace}`;
    const locations = [
      `**/plugins/cache/plugins/${fact.marketplace}___${fact.plugin}___*/`,
      `**/plugins/cache/${fact.marketplace}/${fact.plugin}/*/`,
    ];
    if (fact.npm !== "" && builds.some((build) => build.fact.packageRoot === root))
      locations.push(imageRoot(fact.npm));
    const unit: Unit = {
      id,
      host: "claude-code",
      manifest,
      action: "ships",
      members,
      locations,
      note:
        note ||
        (members.length === 0 ? `no SKILL.md under ${repository} ${shown(root ?? "")}/skills` : ""),
    };
    pluginUnits.set(id, unit);
    units.push(unit);
  }
  for (const { manifest, fact } of pluginManifests) {
    if (claimed.has(manifest)) continue;
    units.push({
      id: `plugin:${fact.plugin}`,
      host: "claude-code",
      manifest,
      action: "ships",
      members: index.membersAt(manifest.repository, fact.root, ["skills", ...fact.skills]),
      locations: [],
      note: "no marketplace read names this plugin, so where it is installed is not known",
    });
  }

  // A host's user skills, linked by an installer.
  for (const { manifest, fact } of factsOf(manifests, "links-skills")) {
    const members = index
      .childrenOf(manifest.repository, fact.dir)
      .map(({ child, copy }) => ({ copy, rel: child }));
    units.push({
      id: `user-skills:${fact.host}`,
      host: fact.host,
      manifest,
      action: "links",
      members,
      locations: [fact.host === "omp" ? "**/.omp/agent/skills/" : "**/.claude/skills/"],
      note: members.length > 0 ? "" : `no SKILL.md under ${shown(fact.dir)}/ in the tree`,
    });
  }

  for (const unit of units) {
    if (unit.members.length === 0)
      row(unit.manifest, unit.action, unit.id, unit.host, "", undefined, unit.note);
    for (const member of unit.members)
      row(unit.manifest, unit.action, unit.id, unit.host, member.copy.name, member.copy, unit.note);
  }

  // What installs a unit: settings and installers (plugins), plugin lists (packages), projects.
  const marketplaces = new Set(
    factsOf(manifests, "marketplace-plugin").map(({ fact }) => fact.marketplace)
  );
  for (const { manifest, fact } of factsOf(manifests, "plugin-switch")) {
    const id = `plugin:${fact.plugin}@${fact.marketplace}`;
    const unit = pluginUnits.get(id);
    if (unit === undefined || unit.members.length === 0) {
      const why = marketplaces.has(fact.marketplace)
        ? `the marketplace ${fact.marketplace} lists no such plugin with skills`
        : `the marketplace ${fact.marketplace} is not in the trees read`;
      row(manifest, fact.action, id, fact.host, "", undefined, why);
    }
    for (const member of unit?.members ?? [])
      row(manifest, fact.action, id, fact.host, member.copy.name, member.copy);
  }
  for (const { manifest, fact } of factsOf(manifests, "installs-package")) {
    const id = `package:${fact.package}`;
    const members = packageUnits.get(fact.package)?.members ?? [];
    if (members.length === 0)
      row(
        manifest,
        "installs",
        id,
        "omp",
        "",
        undefined,
        `no package ${fact.package} with skills was read`
      );
    for (const member of members)
      row(
        manifest,
        "installs",
        id,
        "omp",
        member.copy.name,
        member.copy,
        `version ${fact.version}`
      );
  }
  for (const { manifest, fact } of factsOf(manifests, "leaves-out")) {
    for (const plugin of fact.plugins) {
      const matching = units.filter((unit) => unit.id.startsWith(`plugin:${plugin}@`));
      const id = `project:${fact.project}`;
      if (matching.length === 0)
        row(
          manifest,
          "leaves out",
          id,
          "omp",
          "",
          undefined,
          `the plugin ${plugin} is in no marketplace read`
        );
      for (const unit of matching) {
        for (const member of unit.members)
          row(manifest, "leaves out", id, "omp", member.copy.name, member.copy, unit.id);
      }
    }
  }
  for (const { manifest, fact } of factsOf(manifests, "ignores")) {
    for (const pattern of fact.patterns) {
      const glob = nameGlob(pattern);
      const matched = index.copies.filter((copy) => glob.test(copy.name));
      if (matched.length === 0)
        row(
          manifest,
          "ignores",
          "omp-settings",
          "omp",
          pattern,
          undefined,
          "no copy of this name was read"
        );
      for (const copy of matched)
        row(manifest, "ignores", "omp-settings", "omp", copy.name, copy, `pattern ${pattern}`);
    }
  }
  for (const { manifest, fact } of factsOf(manifests, "links-config")) {
    row(
      manifest,
      "links config",
      "omp-settings",
      "omp",
      "",
      undefined,
      `${fact.file} is the Oh My Pi config.yml`
    );
  }
  for (const { manifest, fact } of factsOf(manifests, "loads")) {
    const unit = fact.unit.startsWith("agent:")
      ? fact.unit
      : `loader:${manifest.path}#${fact.unit}`;
    for (const skill of fact.skills) {
      const named = index.byName.get(skill) ?? [];
      const own = named.filter((copy) => copy.repository === manifest.repository);
      const chosen = own.length > 0 ? own : named;
      if (chosen.length === 0)
        row(manifest, "loads", unit, fact.host, skill, undefined, "no copy of this name was read");
      for (const copy of chosen) row(manifest, "loads", unit, fact.host, skill, copy);
    }
  }

  const prefixes = new Map<string, PrefixRow>();
  for (const unit of units) {
    const via = `${unit.id} (${unit.manifest.repository}:${unit.manifest.path})`;
    for (const member of unit.members) {
      for (const location of unit.locations) {
        const prefix = `${location}${member.rel}/`;
        const key = `${prefix}\0${member.copy.repository}\0${member.copy.path}`;
        const known = prefixes.get(key);
        prefixes.set(
          key,
          known === undefined
            ? {
                prefix,
                repository: member.copy.repository,
                path: member.copy.path,
                name: member.copy.name,
                via,
              }
            : {
                ...known,
                via: known.via.split("; ").includes(via) ? known.via : `${known.via}; ${via}`,
              }
        );
      }
    }
  }
  return {
    rows,
    units,
    prefixes: [...prefixes.values()].sort(
      (a, b) => a.prefix.localeCompare(b.prefix) || a.repository.localeCompare(b.repository)
    ),
  };
}

function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/** The regular expression of a prefix pattern: `**\/` at its start any leading directories, `*`
 *  one path segment's text, the rest literal; it matches a path that starts with the prefix. */
export function prefixRegExp(pattern: string): RegExp {
  const anywhere = pattern.startsWith("**/");
  const body = (anywhere ? pattern.slice(3) : pattern).split("*").map(escapeRegExp).join("[^/]+");
  return new RegExp(`^${anywhere ? "(?:.*/)?" : ""}${body}`);
}

/** Whether `copyPath` (a read's path) is under the prefix `pattern`. */
export function prefixMatches(pattern: string, copyPath: string): boolean {
  return prefixRegExp(pattern).test(copyPath);
}

// ---------------------------------------------------------------------------------------------
// names in files

/** The kinds of mention of a skill name in a file. */
export const MATCH_KINDS = [
  "skill-uri",
  "slash-skill",
  "plugin-qualified",
  "skills-path",
  "quoted-string",
  "name",
  "code-load",
] as const;
export type MatchKind = (typeof MATCH_KINDS)[number];

const NAME_CHAR = "[A-Za-z0-9_-]";
/** The extensions of code files, where a quoted name on a skills line is a load. */
const CODE_EXTENSIONS: Readonly<Record<string, true>> = {
  ".ts": true,
  ".tsx": true,
  ".js": true,
  ".mjs": true,
  ".cjs": true,
  ".py": true,
  ".go": true,
  ".rs": true,
  ".rb": true,
  ".lua": true,
  ".sh": true,
  ".bash": true,
};

/**
 * The kinds of whole-name mention of `name` in `text`: each place the name stands with no name
 * character (a letter, digit, `_` or `-`) on either side, so `tool-loop-alpha` is no mention of
 * `alpha`. Each is read by what stands before it: `skill://`, `/skill:`, `<plugin>:` (one of
 * `plugins`), `skills/`, a quote that closes after it, or nothing of these (`name`). In a code file
 * (`CODE_EXTENSIONS`) a quoted mention on a line that also says `skills`, `Skills` or `SKILL.md`
 * is also `code-load`: code that loads the skill (a `skills = [...]` list, a path to its SKILL.md).
 */
export function mentions(
  text: string,
  name: string,
  plugins: readonly string[],
  file = ""
): MatchKind[] {
  const kinds = new Set<MatchKind>();
  const code = Object.hasOwn(CODE_EXTENSIONS, path.posix.extname(file));
  for (const match of text.matchAll(
    new RegExp(`(?<!${NAME_CHAR})${escapeRegExp(name)}(?!${NAME_CHAR})`, "g")
  )) {
    const at = match.index ?? 0;
    const before = text.slice(Math.max(0, at - 80), at);
    const quote = before.slice(-1);
    if (before.endsWith("skill://")) kinds.add("skill-uri");
    else if (before.endsWith("/skill:")) kinds.add("slash-skill");
    else if (plugins.some((plugin) => before.endsWith(`${plugin}:`))) kinds.add("plugin-qualified");
    else if (before.endsWith("skills/")) kinds.add("skills-path");
    else if (quote !== "" && "\"'`".includes(quote) && text[at + name.length] === quote) {
      kinds.add("quoted-string");
      const lineEnd = text.indexOf("\n", at);
      const line = text.slice(text.lastIndexOf("\n", at) + 1, lineEnd < 0 ? text.length : lineEnd);
      if (code && /skills|Skills|SKILL\.md/.test(line)) kinds.add("code-load");
    } else kinds.add("name");
  }
  return MATCH_KINDS.filter((kind) => kinds.has(kind));
}

// ---------------------------------------------------------------------------------------------
// code search

/** Plain English words that are, or may be, skill names: each query of such a name is run again
 *  once per repository that holds a skill. A short list, not a dictionary: a plain word not on it
 *  is run again only when its query reaches 1,000 hits. */
export const COMMON_WORD_NAMES: Readonly<Record<string, true>> = {
  dispatch: true,
  envoy: true,
  architect: true,
  autonomous: true,
  brainstorming: true,
  browser: true,
  commit: true,
  controller: true,
  debugging: true,
  deploy: true,
  design: true,
  docs: true,
  engineering: true,
  memory: true,
  merge: true,
  oracle: true,
  plan: true,
  planning: true,
  proof: true,
  refactor: true,
  release: true,
  research: true,
  retro: true,
  review: true,
  search: true,
  security: true,
  simplify: true,
  spec: true,
  tasks: true,
  testing: true,
  triage: true,
  verification: true,
  worker: true,
  writing: true,
};

/**
 * The search terms for a skill name: the quoted name, each run between colons quoted alone, since
 * the pod's gh refuses a quoted search word that holds a colon (`a:b` is sent as `"a" "b"`).
 *
 * One search for the quoted name, per owner, stands for the five patterns the brief names
 * (`skill://<name>`, `/skill:<name>`, `skills/<name>`, `<plugin>:<name>` and the quoted name):
 *   - every one of the five holds the name, and GitHub's code search reads punctuation as a break
 *     between words, so a file that matches any of them holds the name's own words in a row, which
 *     is what the quoted name matches;
 *   - every hit file is read and kept only where the name stands whole (`mentions`), and
 *     `mentions` sorts each mention by those five patterns, so a pattern's own search would find
 *     no file, and no kind of mention, that the one search misses;
 *   - a search whose count reaches GitHub's cap of 1,000 results is run again per repository, so
 *     the cap on the one search hides no repository a narrower pattern's search would reach.
 * Checked on cybermonklabs/legion on 2026-11-20, with all five searched: for each of
 * `agent-secrets`, `approval-review` and `asd-ste100`, every file the four other patterns found
 * (54, 19 and 24 files) was among the quoted name's hits. One search instead of five takes a run
 * over three owners' 632 names from about 8,100 searches to about 1,900.
 */
export function searchTerms(name: string): string {
  return name
    .replace(/"/g, "")
    .split(":")
    .filter((part) => part !== "")
    .map((part) => `"${part}"`)
    .join(" ");
}

/** One search query: a skill name in one scope. */
interface SearchJob {
  readonly name: string;
  /** `org:<o>`, `user:<o>` or `repo:<r>`. */
  readonly scope: string;
  readonly owner: string;
  readonly reason: string;
}

/** A search hit and what reading it found. */
export interface SearchHit {
  readonly repository: string;
  readonly path: string;
  verdict: string;
  match: string;
}

/** One line of queries.jsonl for a search. */
export interface QueryRecord {
  readonly type: "search";
  readonly name: string;
  readonly q: string;
  readonly scope: string;
  readonly reason: string;
  status: "ok" | "failed" | "not run" | "partial";
  total_count: number | null;
  incomplete_results: boolean | null;
  pages: number;
  calls: number;
  rate_limit_retries: number;
  items_read: number;
  /** Hits GitHub counted that were not read: past its 1,000-result cap, or past the budget. */
  unread: number;
  hits: SearchHit[];
  error: string;
}

/** One line of queries.jsonl for a fork read from its tree. */
export interface ForkScanRecord {
  readonly type: "fork-scan";
  readonly repository: string;
  readonly max_bytes: number;
  files_read: number;
  skipped_large: number;
  skipped_binary: number;
  files_naming_a_skill: number;
  failures: string[];
}

/** One page of a search's answer. */
interface SearchPage {
  readonly total: number;
  readonly incomplete: boolean;
  readonly items: z.infer<typeof SearchAnswer>["items"];
}

/** HTTP status, headers and body of a `gh api -i` answer. */
function splitIncluded(text: string): {
  status: number;
  headers: Map<string, string>;
  body: string;
} {
  const gap = /\r?\n\r?\n/.exec(text);
  if (!text.startsWith("HTTP/") || gap === null)
    return { status: 0, headers: new Map(), body: text };
  const head = text.slice(0, gap.index).split(/\r?\n/);
  const headers = new Map<string, string>();
  for (const line of head.slice(1)) {
    const colon = line.indexOf(":");
    if (colon > 0)
      headers.set(line.slice(0, colon).trim().toLowerCase(), line.slice(colon + 1).trim());
  }
  return {
    status: Number(/^HTTP\/\S+\s+(\d+)/.exec(head[0] ?? "")?.[1] ?? 0),
    headers,
    body: text.slice(gap.index + gap[0].length),
  };
}

/** One owner's code-search pacing: the start times of its calls in the last minute, its limit a
 *  minute, and when its spent limit resets. */
interface OwnerPace {
  starts: number[];
  limit: number;
  blockedUntil: number;
}

/** The calls a minute GitHub allows code search when an answer gives no `x-ratelimit-limit`. */
const SEARCH_LIMIT = 10;
const MINUTE_MS = 60_000;

/** Code search calls under a call budget, each owner paced by its own token's limit (the file
 *  header, step 5). */
class CodeSearch {
  calls = 0;
  private readonly paces = new Map<string, OwnerPace>();

  constructor(
    private readonly github: Github,
    readonly budget: number
  ) {}

  private pace(owner: string): OwnerPace {
    let pace = this.paces.get(owner);
    if (pace === undefined) {
      pace = { starts: [], limit: SEARCH_LIMIT, blockedUntil: 0 };
      this.paces.set(owner, pace);
    }
    return pace;
  }

  /** Waits until `owner` may start a call: fewer than its limit started in the last minute, and
   *  past any reset its answers or a refusal named. */
  private async waitFor(owner: string): Promise<void> {
    const io = this.github.io;
    const pace = this.pace(owner);
    for (;;) {
      const now = io.now();
      pace.starts = pace.starts.filter((start) => start > now - MINUTE_MS);
      const windowOpens = pace.starts.length < pace.limit ? 0 : (pace.starts[0] ?? now) + MINUTE_MS;
      const until = Math.max(pace.blockedUntil, windowOpens);
      if (until <= now) return;
      io.err(
        `copies: code search of ${owner} waits ${Math.ceil((until - now) / 1000)} s for its rate limit`
      );
      await io.sleep(until - now);
    }
  }

  /** One page of a search; undefined when the call budget is spent before it. */
  async page(
    job: SearchJob,
    q: string,
    page: number,
    record: QueryRecord
  ): Promise<SearchPage | undefined> {
    const io = this.github.io;
    const pace = this.pace(job.owner);
    for (;;) {
      await this.waitFor(job.owner);
      if (this.calls >= this.budget) return undefined;
      this.calls++;
      record.calls++;
      pace.starts.push(io.now());
      const result = await io.gh([
        "api",
        "-i",
        "-X",
        "GET",
        "search/code",
        "-f",
        `q=${q}`,
        "-f",
        "per_page=100",
        "-f",
        `page=${page}`,
      ]);
      const answer = splitIncluded(result.stdout);
      const header = (name: string) => {
        const value = Number(answer.headers.get(name));
        return Number.isFinite(value) && value > 0 ? value : undefined;
      };
      const limit = header("x-ratelimit-limit");
      if (limit !== undefined) pace.limit = limit;
      const reset = header("x-ratelimit-reset");
      const resetMs = reset === undefined ? undefined : reset * 1000 + 1000;
      if (answer.headers.get("x-ratelimit-remaining") === "0" && resetMs !== undefined)
        pace.blockedUntil = resetMs;
      if (result.code === 0 && answer.status < 300) {
        const parsed = SearchAnswer.safeParse(parseJson(answer.body));
        if (!parsed.success)
          throw new Error(`the search answer is not of its shape: ${schemaFaults(parsed.error)}`);
        return {
          total: parsed.data.total_count,
          incomplete: parsed.data.incomplete_results,
          items: parsed.data.items,
        };
      }
      if (
        record.rate_limit_retries < MAX_RATE_RETRIES &&
        (answer.status === 429 || RATE_LIMITED.test(`${result.stderr}\n${answer.body}`))
      ) {
        record.rate_limit_retries++;
        const retryAfter = header("retry-after");
        pace.blockedUntil =
          retryAfter !== undefined
            ? io.now() + retryAfter * 1000
            : (resetMs ?? io.now() + (await this.github.waitForReset("code_search")));
        continue;
      }
      throw new Error(firstLine(result.stderr) || `HTTP ${answer.status}`);
    }
  }
}

function newRecord(job: SearchJob): QueryRecord {
  return {
    type: "search",
    name: job.name,
    q: `${searchTerms(job.name)} ${job.scope}`,
    scope: job.scope,
    reason: job.reason,
    status: "not run",
    total_count: null,
    incomplete_results: null,
    pages: 0,
    calls: 0,
    rate_limit_retries: 0,
    items_read: 0,
    unread: 0,
    hits: [],
    error: "",
  };
}

/** Runs one search job, every page up to GitHub's 1,000 results. */
async function runSearch(search: CodeSearch, job: SearchJob, record: QueryRecord): Promise<void> {
  for (let page = 1; page <= 10; page++) {
    let answer: SearchPage | undefined;
    try {
      answer = await search.page(job, record.q, page, record);
    } catch (error) {
      record.status = "failed";
      record.error = (error as Error).message;
      return;
    }
    if (answer === undefined) {
      record.status = page === 1 ? "not run" : "partial";
      record.error = `the search-call budget (${search.budget}) is spent`;
      record.unread = Math.max(0, (record.total_count ?? 0) - record.items_read);
      return;
    }
    record.pages = page;
    record.total_count = answer.total;
    record.incomplete_results = (record.incomplete_results ?? false) || answer.incomplete;
    for (const item of answer.items) {
      record.hits.push({
        repository: item.repository.full_name,
        path: item.path,
        verdict: "",
        match: "",
      });
    }
    record.items_read += answer.items.length;
    if (answer.items.length < 100 || record.items_read >= Math.min(answer.total, 1000)) break;
  }
  record.status = "ok";
  record.unread = Math.max(0, (record.total_count ?? 0) - record.items_read);
}

/** Runs the jobs: each owner's in order, one at a time, and the owners' at once, so each owner's
 *  token is paced by its own limit while the others' run. `followUps` gives, for a job that ran,
 *  more jobs, each queued at the end of its own owner's queue. Returns one record per job: the
 *  jobs' in their order, then the follow-ups' in the order they were queued. */
async function runSearches(
  search: CodeSearch,
  jobs: readonly SearchJob[],
  io: CopiesIo,
  followUps: (job: SearchJob, record: QueryRecord) => readonly SearchJob[] = () => []
): Promise<QueryRecord[]> {
  const queued = jobs.map((job) => ({ job, record: newRecord(job) }));
  const queues = new Map<string, typeof queued>();
  const enqueue = (entry: (typeof queued)[number]) => {
    const queue = queues.get(entry.job.owner);
    if (queue === undefined) queues.set(entry.job.owner, [entry]);
    else queue.push(entry);
  };
  for (const entry of queued) enqueue(entry);
  const started = io.now();
  let done = 0;
  await Promise.all(
    [...queues.values()].map(async (queue) => {
      // The queue grows while it runs: a follow-up joins the end of its owner's own queue.
      for (let index = 0; index < queue.length; index++) {
        const entry = queue[index];
        if (entry === undefined) continue;
        if (search.calls >= search.budget)
          entry.record.error = `the search-call budget (${search.budget}) is spent`;
        else await runSearch(search, entry.job, entry.record);
        for (const job of followUps(entry.job, entry.record)) {
          const next = { job, record: newRecord(job) };
          queued.push(next);
          enqueue(next);
        }
        done++;
        if (done % 25 === 0) {
          const minutes = Math.round((io.now() - started) / MINUTE_MS);
          io.err(
            `copies: ${done}/${queued.length} searches, ${search.calls} calls, ${minutes} min`
          );
        }
      }
    })
  );
  return queued.map((entry) => entry.record);
}

// ---------------------------------------------------------------------------------------------
// forks read from their trees

/** The largest text blob a fork's scan reads, in bytes. */
export const FORK_SCAN_MAX_BYTES = 100_000;
/** The extensions of the text files a fork's scan reads. */
export const TEXT_EXTENSIONS: Readonly<Record<string, true>> = {
  ".md": true,
  ".mdx": true,
  ".txt": true,
  ".rst": true,
  ".ts": true,
  ".tsx": true,
  ".js": true,
  ".mjs": true,
  ".cjs": true,
  ".jsx": true,
  ".json": true,
  ".jsonc": true,
  ".yaml": true,
  ".yml": true,
  ".toml": true,
  ".ini": true,
  ".cfg": true,
  ".conf": true,
  ".py": true,
  ".sh": true,
  ".bash": true,
  ".zsh": true,
  ".fish": true,
  ".go": true,
  ".rs": true,
  ".rb": true,
  ".lua": true,
  ".nix": true,
  ".el": true,
  ".vim": true,
  ".tmpl": true,
};
/** Whole file names a fork's scan reads beside the extensions. */
export const TEXT_NAMES: Readonly<Record<string, true>> = {
  Dockerfile: true,
  Makefile: true,
  Justfile: true,
  AGENTS: true,
  README: true,
};

// ---------------------------------------------------------------------------------------------
// loaders and the readers' seam

/** One file outside a skill that names a skill. */
export interface LoaderRow {
  readonly repository: string;
  readonly path: string;
  readonly skill: string;
  match: string;
  source: string;
}

/** The copy of `copies` (one repository's) that `file` is inside: under its directory, or, for a
 *  skill at the root, its SKILL.md alone. */
function insideSkill(copies: readonly SkillCopy[], file: string): SkillCopy | undefined {
  return copies.find((copy) =>
    copy.path === "" ? file === copy.file : file.startsWith(`${copy.path}/`)
  );
}

/** The skills list the same-job and follow-the-code readers of task t3 take: each copy with its
 *  whole description. This is the seam; no model is called here. */
export interface ReaderSkill {
  readonly repository: string;
  readonly path: string;
  readonly file: string;
  readonly name: string;
  readonly description: string;
  readonly fork: boolean;
  readonly archived: boolean;
}

export function readerSkills(copies: readonly SkillCopy[]): ReaderSkill[] {
  return copies.map((copy) => ({
    repository: copy.repository,
    path: shown(copy.path),
    file: copy.file,
    name: copy.name,
    description: copy.description,
    fork: copy.fork,
    archived: copy.archived,
  }));
}

/** Each name in two or more repositories, or twice in one, with its copies. */
export function sharedNames(
  copies: readonly SkillCopy[]
): { name: string; copies: SkillCopy[]; why: string }[] {
  const byName = new Map<string, SkillCopy[]>();
  for (const copy of copies) byName.set(copy.name, [...(byName.get(copy.name) ?? []), copy]);
  const shared: { name: string; copies: SkillCopy[]; why: string }[] = [];
  for (const [name, named] of byName) {
    const repositories = [...new Set(named.map((copy) => copy.repository))];
    const twice = repositories.filter(
      (repository) => named.filter((copy) => copy.repository === repository).length > 1
    );
    const why = [
      repositories.length > 1 ? `in ${repositories.length} repositories` : "",
      twice.length > 0 ? `twice or more in ${twice.join(", ")}` : "",
    ].filter((part) => part !== "");
    if (why.length > 0) shared.push({ name, copies: named, why: why.join("; ") });
  }
  return shared.sort((a, b) => a.name.localeCompare(b.name));
}

// ---------------------------------------------------------------------------------------------
// output

export const SKILL_COPIES_COLUMNS = [
  "repository",
  "path",
  "name",
  "description_bytes",
  "fork",
  "archived",
  "parent",
  "installers",
  "loaded_by",
] as const;
export const PREFIX_TABLE_COLUMNS = ["prefix", "repository", "path", "name", "via"] as const;
export const REPOSITORIES_COLUMNS = [
  "repository",
  "branch",
  "commit",
  "fork",
  "archived",
  "parent",
  "tree_entries",
  "tree_read",
  "skills",
  "error",
] as const;
export const LOADERS_COLUMNS = ["repository", "path", "skill", "match", "source"] as const;
export const INSTALLERS_COLUMNS = [
  "manifest_repository",
  "manifest_path",
  "kind",
  "action",
  "unit",
  "host",
  "skill",
  "copy_repository",
  "copy_path",
  "note",
] as const;
export const MANIFESTS_COLUMNS = [
  "repository",
  "path",
  "kind",
  "source",
  "rows",
  "says",
  "error",
] as const;

const CELL_ESCAPES: Readonly<Record<string, string>> = {
  "\\": "\\\\",
  "\t": "\\t",
  "\n": "\\n",
  "\r": "\\r",
};
const CELL_UNESCAPES: Readonly<Record<string, string>> = { "\\": "\\", t: "\t", n: "\n", r: "\r" };

/** A TSV cell: a backslash, tab, line feed or carriage return written as `\\`, `\t`, `\n`, `\r`. */
export function tsvCell(value: string | number | boolean): string {
  return String(value).replace(/[\\\t\n\r]/g, (char) => CELL_ESCAPES[char] ?? char);
}

/** A TSV of `columns` and `rows`, the header row first. */
export function tsv(
  columns: readonly string[],
  rows: readonly (readonly (string | number | boolean)[])[]
): string {
  return `${[columns, ...rows].map((row) => row.map(tsvCell).join("\t")).join("\n")}\n`;
}

/** The rows of a TSV this file writes, each keyed by the header row's column names. */
export function parseTsv(text: string): Record<string, string>[] {
  const lines = text.split("\n").filter((line) => line !== "");
  const header = (lines[0] ?? "").split("\t");
  return lines.slice(1).map((line) => {
    const cells = line.split("\t");
    return Object.fromEntries(
      header.map((column, index) => [
        column,
        (cells[index] ?? "").replace(
          /\\([\\tnr])/g,
          (_, char: string) => CELL_UNESCAPES[char] ?? char
        ),
      ])
    );
  });
}

const yesNo = (value: boolean) => (value ? "yes" : "no");

/** A copy's or repository's fork and archived marks, in parentheses, or `""`. */
function marks(copy: Pick<SkillCopy, "fork" | "archived" | "parent">): string {
  const said = [
    copy.fork ? `fork of ${copy.parent || "an unnamed parent"}` : "",
    copy.archived ? "archived" : "",
  ]
    .filter((part) => part !== "")
    .join(", ");
  return said === "" ? "" : ` (${said})`;
}

/** Everything one run read and found; `main` writes it. */
export interface CopiesResult {
  readonly at: string;
  readonly scope: string;
  readonly listingErrors: { owner: string; error: string }[];
  readonly missing: string[];
  readonly repositories: Repository[];
  readonly copies: SkillCopy[];
  readonly excluded: { repository: string; file: string }[];
  readonly manifests: Manifest[];
  readonly notOfKind: Map<string, number>;
  readonly rows: InstallerRow[];
  readonly prefixes: PrefixRow[];
  readonly offTargets: PrefixRow[];
  readonly targetsUnused: string[];
  readonly loaders: LoaderRow[];
  readonly records: QueryRecord[];
  readonly forkScans: ForkScanRecord[];
  readonly forksNotScanned: string[];
  readonly failures: string[];
  readonly searchCalls: number;
  readonly budget: number;
}

function installersCell(rows: readonly InstallerRow[], copy: SkillCopy): string {
  const items = new Set(
    rows
      .filter(
        (row) =>
          row.copyRepository === copy.repository &&
          row.copyPath === copy.path &&
          row.action !== "loads"
      )
      .map((row) => `${row.action} ${row.unit} (${row.manifestRepository}:${row.manifestPath})`)
  );
  return [...items].sort().join("; ");
}

function loadedByCell(
  rows: readonly InstallerRow[],
  loaders: readonly LoaderRow[],
  copy: SkillCopy
): string {
  const items = new Set<string>();
  for (const row of rows) {
    if (
      row.action === "loads" &&
      row.copyRepository === copy.repository &&
      row.copyPath === copy.path
    ) {
      items.add(`${row.manifestRepository}:${row.manifestPath} (${row.kind} ${row.unit})`);
    }
  }
  for (const loader of loaders) {
    if (loader.skill === copy.name && loader.match.split(",").includes("code-load")) {
      items.add(`${loader.repository}:${loader.path} (code-load, ${loader.source})`);
    }
  }
  return [...items].sort().join("; ");
}

function markdown(result: CopiesResult): string {
  const lines: string[] = [];
  const say = (...text: string[]) => lines.push(...text);
  const readable = result.repositories.filter((repo) => repo.error === "");
  const failed = result.repositories.filter((repo) => repo.error !== "");
  say(
    "# Skill copies and loaders",
    "",
    `Each repository is read at its default branch's last commit at or before ${result.at}. ${result.scope}`,
    "",
    `${result.repositories.length} repositories listed: ${readable.length} read, ${failed.length} not read. ` +
      `${result.copies.length} skill copies, ${new Set(result.copies.map((copy) => copy.name)).size} names. ` +
      `${result.manifests.length} manifests, ${result.rows.length} installer rows, ${result.loaders.length} loader rows. ` +
      `${result.searchCalls} code search calls (budget ${Number.isFinite(result.budget) ? result.budget : "none"}).`,
    "",
    "Same-job pairs and the follow-the-code sentence are not in this list yet: they wait for the `read` command (task t3), which reads skills.jsonl.",
    "",
    "## Repositories that could not be listed or read",
    ""
  );
  if (result.listingErrors.length + result.missing.length + failed.length === 0) say("None.");
  for (const { owner, error } of result.listingErrors)
    say(`- owner ${owner}: the listing failed: ${error}`);
  for (const repository of result.missing) say(`- ${repository}: not in its owner's listing`);
  for (const repo of failed) say(`- ${repo.repository}${marks(repo)}: ${repo.error}`);
  say("", "## Repositories read", "");
  for (const repo of readable) {
    const count = result.copies.filter((copy) => copy.repository === repo.repository).length;
    say(
      `- ${repo.repository}${marks(repo)} at ${repo.commit} (${repo.branch}; tree read ${repo.treeRead}): ${count} skills`
    );
  }
  if (result.excluded.length > 0) {
    const where = [...new Set(result.excluded.map((entry) => entry.repository))].join(", ");
    say("", `Left out by excludePaths: ${result.excluded.length} SKILL.md files (${where}).`);
  }
  say("", "## Shared names", "");
  const shared = sharedNames(result.copies);
  if (shared.length === 0) say("None.");
  for (const entry of shared) {
    const copies = entry.copies
      .map((copy) => `${copy.repository} ${shown(copy.path)}${marks(copy)}`)
      .join("; ");
    say(`- \`${entry.name}\` (${entry.why}): ${copies}`);
  }
  say("", "## Installers and manifests", "");
  for (const manifest of result.manifests) {
    const found = manifest.source === "tree" ? "" : `, found by ${manifest.source}`;
    const says = manifest.error === "" ? manifest.says : `read failed: ${manifest.error}`;
    say(
      `- ${manifest.repository}:${manifest.path} (${manifest.kind}${found}): ${says} [${manifest.rows} rows]`
    );
  }
  if (result.notOfKind.size > 0) {
    const counts = [...result.notOfKind].map(([kind, count]) => `${count} ${kind}`).join(", ");
    say("", `Read by path and naming no skill, plugin or package, so not listed: ${counts}.`);
  }
  say("", "Installer rows, by copy (installers.tsv holds each row):", "");
  for (const copy of result.copies) {
    const cell = installersCell(result.rows, copy);
    if (cell !== "") say(`- ${copy.repository} ${shown(copy.path)} (\`${copy.name}\`): ${cell}`);
  }
  const unresolved = result.rows.filter((row) => row.copyPath === undefined);
  if (unresolved.length > 0) {
    say("", "Installer rows that reach no copy read:", "");
    for (const row of unresolved) {
      say(
        `- ${row.manifestRepository}:${row.manifestPath} ${row.action} ${row.unit}${row.skill ? ` \`${row.skill}\`` : ""}: ${row.note}`
      );
    }
  }
  say("", "## Loaders: files outside a skill that name a skill", "");
  const bySkill = new Map<string, LoaderRow[]>();
  for (const loader of result.loaders)
    bySkill.set(loader.skill, [...(bySkill.get(loader.skill) ?? []), loader]);
  if (bySkill.size === 0) say("None found.");
  for (const [skill, found] of bySkill) {
    say(
      `- \`${skill}\`: ${found.map((loader) => `${loader.repository}:${loader.path} (${loader.match}; ${loader.source})`).join("; ")}`
    );
  }
  say("", "## Code search", "");
  const counts = new Map<string, number>();
  for (const record of result.records)
    counts.set(record.status, (counts.get(record.status) ?? 0) + 1);
  say(
    `${result.records.length} queries: ${[...counts].map(([status, count]) => `${count} ${status}`).join(", ") || "none"}. ` +
      "queries.jsonl holds each query, its total_count and incomplete_results, every hit with its verdict, and every failure."
  );
  for (const record of result.records.filter((candidate) => candidate.status === "failed"))
    say(`- failed: ${record.q}: ${record.error}`);
  for (const record of result.records.filter((candidate) => candidate.unread > 0)) {
    const why =
      record.status === "partial"
        ? "the budget ended it"
        : (record.total_count ?? 0) > 1000
          ? "GitHub gives at most 1,000 results a query"
          : "GitHub's pages held fewer hits than its count";
    say(
      `- ${record.q}: ${record.total_count} hits, ${record.items_read} read, ${record.unread} not read (${why})`
    );
  }
  for (const scan of result.forkScans) {
    say(
      `- the fork ${scan.repository}, read from its tree: ${scan.files_read} text files of at most ${scan.max_bytes} bytes read, ` +
        `${scan.files_naming_a_skill} naming a skill, ${scan.skipped_large} larger left out, ${scan.failures.length} reads failed`
    );
  }
  if (result.forksNotScanned.length > 0)
    say(
      `- forks holding no skill, so not read from their trees: ${result.forksNotScanned.join(", ")}`
    );
  say(
    "",
    "## Prefix table",
    "",
    `${result.prefixes.length} rows in prefix-table.tsv, each to a repository the configuration's targets name.`
  );
  if (result.offTargets.length > 0) {
    const repositories = [...new Set(result.offTargets.map((row) => row.repository))].sort();
    say(
      "",
      `${result.offTargets.length} rows point at repositories the configuration's targets do not name, so they are not in the table: ` +
        `${repositories.join(", ")}. Add each to targets to keep its rows:`,
      ""
    );
    for (const row of result.offTargets)
      say(`- \`${row.prefix}\` -> ${row.repository} ${shown(row.path)}`);
  }
  if (result.targetsUnused.length > 0)
    say("", `Targets no row maps to: ${result.targetsUnused.join(", ")}.`);
  say("", "## Failures", "");
  if (result.failures.length === 0) say("None.");
  for (const failure of result.failures) say(`- ${failure}`);
  return `${lines.join("\n")}\n`;
}

/** Writes every output file under `out`; returns their paths. */
export function writeOutputs(out: string, result: CopiesResult): string[] {
  mkdirSync(out, { recursive: true });
  const copiesOf = (repository: string) =>
    result.copies.filter((copy) => copy.repository === repository).length;
  const files: [string, string][] = [
    [
      "skill-copies.tsv",
      tsv(
        SKILL_COPIES_COLUMNS,
        result.copies.map((copy) => [
          copy.repository,
          shown(copy.path),
          copy.name,
          copy.descriptionBytes,
          yesNo(copy.fork),
          yesNo(copy.archived),
          copy.parent,
          installersCell(result.rows, copy),
          loadedByCell(result.rows, result.loaders, copy),
        ])
      ),
    ],
    [
      "prefix-table.tsv",
      tsv(
        PREFIX_TABLE_COLUMNS,
        result.prefixes.map((row) => [
          row.prefix,
          row.repository,
          shown(row.path),
          row.name,
          row.via,
        ])
      ),
    ],
    [
      "repositories.tsv",
      tsv(
        REPOSITORIES_COLUMNS,
        result.repositories.map((repo) => [
          repo.repository,
          repo.branch,
          repo.commit,
          yesNo(repo.fork),
          yesNo(repo.archived),
          repo.parent,
          repo.tree.length,
          repo.treeRead,
          copiesOf(repo.repository),
          repo.error,
        ])
      ),
    ],
    [
      "loaders.tsv",
      tsv(
        LOADERS_COLUMNS,
        result.loaders.map((loader) => [
          loader.repository,
          loader.path,
          loader.skill,
          loader.match,
          loader.source,
        ])
      ),
    ],
    [
      "installers.tsv",
      tsv(
        INSTALLERS_COLUMNS,
        result.rows.map((row) => [
          row.manifestRepository,
          row.manifestPath,
          row.kind,
          row.action,
          row.unit,
          row.host,
          row.skill,
          row.copyRepository,
          row.copyPath === undefined ? "" : shown(row.copyPath),
          row.note,
        ])
      ),
    ],
    [
      "manifests.tsv",
      tsv(
        MANIFESTS_COLUMNS,
        result.manifests.map((manifest) => [
          manifest.repository,
          manifest.path,
          manifest.kind,
          manifest.source,
          manifest.rows,
          manifest.says,
          manifest.error,
        ])
      ),
    ],
    [
      "queries.jsonl",
      [...result.records, ...result.forkScans]
        .map((record) => `${JSON.stringify(record)}\n`)
        .join(""),
    ],
    [
      "skills.jsonl",
      readerSkills(result.copies)
        .map((skill) => `${JSON.stringify(skill)}\n`)
        .join(""),
    ],
    ["skill-copies.md", markdown(result)],
  ];
  for (const [name, text] of files) writeFileSync(path.join(out, name), text);
  return files.map(([name]) => path.join(out, name));
}

// ---------------------------------------------------------------------------------------------
// the run

/** The options of one run. */
export interface CopiesOptions {
  readonly at: string;
  readonly only: readonly string[];
  readonly maxSearchCalls: number;
}

/** Reads the repositories, skills, manifests and loaders of one run. */
export async function listCopies(
  config: SkillCutsConfig,
  options: CopiesOptions,
  io: CopiesIo
): Promise<CopiesResult> {
  const github = new Github(io);
  const failures: string[] = [];
  /** Reads the blobs of `wanted` into the cache, a GraphQL batch at a time per repository. */
  const prefetch = async (wanted: readonly { repository: string; entry: TreeEntry }[]) => {
    const byRepository = new Map<string, TreeEntry[]>();
    for (const { repository, entry } of wanted) {
      const entries = byRepository.get(repository);
      if (entries === undefined) byRepository.set(repository, [entry]);
      else entries.push(entry);
    }
    await mapLimit([...byRepository], REST_JOBS, ([repository, entries]) =>
      github.prefetch(repository, entries)
    );
  };
  const only = options.only.map((repository) => repository.toLowerCase());
  const owners =
    only.length > 0
      ? [...new Set(options.only.map((repository) => repository.split("/")[0] ?? ""))]
      : config.owners;

  // 1. Repositories.
  const listingErrors: { owner: string; error: string }[] = [];
  let repositories: Repository[] = [];
  for (const owner of owners) {
    io.err(`copies: listing ${owner}`);
    try {
      const listed = await github.json(
        [
          "repo",
          "list",
          owner,
          "--limit",
          "2000",
          "--json",
          "nameWithOwner,isFork,isArchived,defaultBranchRef,parent",
        ],
        Listing
      );
      for (const entry of listed) {
        repositories.push({
          repository: entry.nameWithOwner,
          owner: entry.nameWithOwner.split("/")[0] ?? "",
          fork: entry.isFork,
          archived: entry.isArchived,
          parent: entry.parent === null ? "" : `${entry.parent.owner.login}/${entry.parent.name}`,
          branch: entry.defaultBranchRef?.name ?? "",
          commit: "",
          tree: [],
          treeRead: "",
          error: "",
        });
      }
    } catch (error) {
      listingErrors.push({ owner, error: (error as Error).message });
    }
  }
  const missing: string[] = [];
  if (only.length > 0) {
    repositories = repositories.filter((repo) => only.includes(repo.repository.toLowerCase()));
    const found = new Set(repositories.map((repo) => repo.repository.toLowerCase()));
    missing.push(...options.only.filter((repository) => !found.has(repository.toLowerCase())));
  }
  repositories.sort((a, b) => a.repository.localeCompare(b.repository));
  io.err(`copies: reading ${repositories.length} repositories at ${options.at}`);
  await mapLimit(repositories, REST_JOBS, (repo) => readRepository(github, repo, options.at));

  // 2. Skills.
  const excluded: { repository: string; file: string }[] = [];
  const skillFiles: { repo: Repository; entry: TreeEntry }[] = [];
  for (const repo of repositories) {
    for (const entry of repo.tree) {
      if (entry.type !== "blob" || path.posix.basename(entry.path) !== "SKILL.md") continue;
      const left = config.excludePaths.some(
        (exclude) =>
          exclude.repo.toLowerCase() === repo.repository.toLowerCase() &&
          entry.path.startsWith(exclude.prefix)
      );
      if (left) excluded.push({ repository: repo.repository, file: entry.path });
      else skillFiles.push({ repo, entry });
    }
  }
  io.err(`copies: reading ${skillFiles.length} SKILL.md files`);
  await prefetch(skillFiles.map(({ repo, entry }) => ({ repository: repo.repository, entry })));
  const copies = await mapLimit(
    skillFiles,
    REST_JOBS,
    async ({ repo, entry }): Promise<SkillCopy> => {
      const dir = dirOf(entry.path);
      const base = {
        repository: repo.repository,
        path: dir,
        file: entry.path,
        blob: entry.sha,
        fork: repo.fork,
        archived: repo.archived,
        parent: repo.parent,
      };
      try {
        return {
          ...base,
          ...skillFields(dir, repo.repository, await github.blob(repo.repository, entry.sha)),
        };
      } catch (error) {
        const fault = `SKILL.md read failed: ${(error as Error).message}`;
        failures.push(`${repo.repository}:${entry.path}: ${fault}`);
        const name = dir === "" ? (repo.repository.split("/")[1] ?? "") : path.posix.basename(dir);
        return {
          ...base,
          name,
          nameFrom: "directory",
          description: "",
          descriptionBytes: 0,
          fault,
        };
      }
    }
  );
  copies.sort((a, b) => a.repository.localeCompare(b.repository) || a.path.localeCompare(b.path));
  const copiesByRepository = new Map<string, SkillCopy[]>();
  for (const copy of copies)
    copiesByRepository.set(copy.repository, [
      ...(copiesByRepository.get(copy.repository) ?? []),
      copy,
    ]);
  const copiesIn = (repository: string) => copiesByRepository.get(repository) ?? [];
  const withSkill = new Set(
    repositories
      .filter((repo) => repo.tree.some((entry) => path.posix.basename(entry.path) === "SKILL.md"))
      .map((repo) => repo.repository)
  );

  // 4. Manifests.
  const candidates: { repo: Repository; entry: TreeEntry; kind: ManifestKind }[] = [];
  for (const repo of repositories) {
    for (const entry of repo.tree) {
      const kind = manifestKind(entry);
      if (kind === undefined || (kind === "omp-package" && !withSkill.has(repo.repository)))
        continue;
      if (kind !== "skill-link" && insideSkill(copiesIn(repo.repository), entry.path) !== undefined)
        continue;
      candidates.push({ repo, entry, kind });
    }
  }
  io.err(`copies: reading ${candidates.length} manifest files`);
  await prefetch(candidates.map(({ repo, entry }) => ({ repository: repo.repository, entry })));
  const read = await mapLimit(
    candidates,
    REST_JOBS,
    async ({ repo, entry, kind }): Promise<Manifest> => {
      const base = {
        repository: repo.repository,
        path: entry.path,
        kind,
        source: "tree" as const,
        rows: 0,
      };
      try {
        return {
          ...base,
          ...parseManifest(kind, entry.path, await github.blob(repo.repository, entry.sha)),
          error: "",
        };
      } catch (error) {
        const message = (error as Error).message;
        failures.push(`${repo.repository}:${entry.path} (${kind}): ${message}`);
        return { ...base, facts: [], ofKind: true, says: "", error: message };
      }
    }
  );
  const alwaysListed = new Set<ManifestKind>(
    MANIFEST_KINDS.filter((kind) => kind.listed === "always").map((kind) => kind.kind)
  );
  // Every manifest read is resolved (a package.json without omp.skills still names the package an
  // npm-sourced plugin comes from); only those of their kind are listed.
  const manifests = read.filter((manifest) => manifest.ofKind || alwaysListed.has(manifest.kind));
  const notOfKind = new Map<string, number>();
  for (const manifest of read) {
    if (!manifest.ofKind && !alwaysListed.has(manifest.kind))
      notOfKind.set(manifest.kind, (notOfKind.get(manifest.kind) ?? 0) + 1);
  }

  // 5. Code search: each name, with the plugins that ship it.
  const names = [...new Set(copies.map((copy) => copy.name))].sort();
  const pluginsOf = new Map<string, string[]>();
  for (const unit of resolveInstallers(read, new CopyIndex(copies)).units) {
    if (!unit.id.startsWith("plugin:")) continue;
    const plugin = unit.id.slice("plugin:".length).split("@")[0] ?? "";
    for (const member of unit.members) {
      const known = pluginsOf.get(member.copy.name) ?? [];
      if (!known.includes(plugin)) pluginsOf.set(member.copy.name, [...known, plugin].sort());
    }
  }
  const search = new CodeSearch(github, options.maxSearchCalls);
  const scopes: { scope: string; owner: string; reason: string }[] = [];
  if (only.length > 0) {
    for (const repo of repositories) {
      if (!repo.fork)
        scopes.push({ scope: `repo:${repo.repository}`, owner: repo.owner, reason: "--only" });
    }
  } else {
    // Each owner whose listing succeeded: one not listed has no repository read, so no hit there
    // could be checked.
    const listed = owners.filter(
      (owner) => !listingErrors.some((failed) => failed.owner === owner)
    );
    for (const owner of listed) {
      let qualifier = "user";
      try {
        if ((await github.api(`users/${owner}`, GithubUser)).type === "Organization")
          qualifier = "org";
      } catch (error) {
        failures.push(`users/${owner}: ${(error as Error).message}; searched as user:${owner}`);
      }
      scopes.push({ scope: `${qualifier}:${owner}`, owner, reason: "owner" });
    }
  }
  // One search per name and scope (`searchTerms` says why one stands for the brief's five).
  const baseJobs: SearchJob[] = names.flatMap((name) =>
    scopes.map((scope) => ({ name, ...scope }))
  );
  io.err(`copies: ${baseJobs.length} code searches for ${names.length} names`);
  // An owner's search of a name at the 1,000 cap, or of a common word, is run again once per
  // repository of that owner holding a skill, queued behind that owner's own searches.
  const reruns = (job: SearchJob, record: QueryRecord): SearchJob[] => {
    if (job.reason !== "owner") return [];
    const capped = (record.total_count ?? 0) >= 1000;
    if (!capped && !Object.hasOwn(COMMON_WORD_NAMES, job.name)) return [];
    const reason = capped
      ? "run again per repository: total_count reached 1,000"
      : "run again per repository: a common word";
    return repositories
      .filter((repo) => repo.owner === job.owner && !repo.fork && withSkill.has(repo.repository))
      .map((repo) => ({
        name: job.name,
        scope: `repo:${repo.repository}`,
        owner: repo.owner,
        reason,
      }));
  };
  const records = await runSearches(search, baseJobs, io, reruns);

  // Each hit file, read at its repository's commit, kept where it names the skill.
  const byRepository = new Map(repositories.map((repo) => [repo.repository.toLowerCase(), repo]));
  const treeIndex = new Map<Repository, Map<string, TreeEntry>>();
  const entryAt = (repo: Repository, file: string) => {
    let entries = treeIndex.get(repo);
    if (entries === undefined) {
      entries = new Map(
        repo.tree.filter((entry) => entry.type === "blob").map((entry) => [entry.path, entry])
      );
      treeIndex.set(repo, entries);
    }
    return entries.get(file);
  };
  const loaders = new Map<string, LoaderRow>();
  const addLoader = (
    repository: string,
    file: string,
    skill: string,
    kinds: readonly string[],
    source: string
  ) => {
    const key = `${repository}\0${file}\0${skill}`;
    const known = loaders.get(key);
    if (known === undefined) {
      loaders.set(key, { repository, path: file, skill, match: kinds.join(","), source });
      return;
    }
    known.match = [...new Set([...known.match.split(","), ...kinds])].join(",");
    if (!known.source.split(", ").includes(source)) known.source = `${known.source}, ${source}`;
  };
  const hits = records.flatMap((record) => record.hits.map((hit) => ({ record, hit })));
  io.err(`copies: checking ${hits.length} search hits`);
  const toRead: { repository: string; entry: TreeEntry }[] = [];
  for (const { hit } of hits) {
    const repo = byRepository.get(hit.repository.toLowerCase());
    const entry = repo === undefined || repo.error !== "" ? undefined : entryAt(repo, hit.path);
    if (
      repo !== undefined &&
      entry !== undefined &&
      insideSkill(copiesIn(repo.repository), hit.path) === undefined
    ) {
      toRead.push({ repository: repo.repository, entry });
    }
  }
  await prefetch(toRead);
  const extraManifests: Manifest[] = [];
  await mapLimit(hits, REST_JOBS, async ({ record, hit }) => {
    const repo = byRepository.get(hit.repository.toLowerCase());
    if (repo === undefined || repo.error !== "") {
      hit.verdict = "repository not read";
      return;
    }
    const entry = entryAt(repo, hit.path);
    if (entry === undefined) {
      hit.verdict = `not in the tree at ${repo.commit}`;
      return;
    }
    const inside = insideSkill(copiesIn(repo.repository), hit.path);
    if (inside !== undefined) {
      hit.verdict = `inside the skill ${shown(inside.path)}`;
      return;
    }
    let body: string;
    try {
      body = await github.blob(repo.repository, entry.sha);
    } catch (error) {
      hit.verdict = `read failed: ${(error as Error).message}`;
      failures.push(`${repo.repository}:${hit.path} (a search hit): ${(error as Error).message}`);
      return;
    }
    const kinds = mentions(body, record.name, pluginsOf.get(record.name) ?? [], hit.path);
    if (kinds.length === 0) {
      hit.verdict = body.includes(record.name)
        ? "dropped: only inside a longer name"
        : "dropped: the name is not in it";
      return;
    }
    hit.verdict = "kept";
    hit.match = kinds.join(",");
    addLoader(repo.repository, hit.path, record.name, kinds, "code search");
    const kind =
      manifestKind(entry) ??
      (hit.path.endsWith(".py") && SKILLS_LIST.test(body) ? "python-loader" : undefined);
    const known = [...manifests, ...extraManifests].some(
      (manifest) => manifest.repository === repo.repository && manifest.path === hit.path
    );
    if (kind === undefined || known) return;
    try {
      const parsed = parseManifest(kind, hit.path, body);
      if (parsed.ofKind) {
        extraManifests.push({
          repository: repo.repository,
          path: hit.path,
          kind,
          source: "code search",
          ...parsed,
          error: "",
          rows: 0,
        });
      }
    } catch (error) {
      failures.push(
        `${repo.repository}:${hit.path} (${kind}, a search hit): ${(error as Error).message}`
      );
    }
  });

  // Forks: read from their trees when they hold a skill.
  const forkScans: ForkScanRecord[] = [];
  const forksNotScanned: string[] = [];
  const longestFirst = [...names].sort((a, b) => b.length - a.length).map(escapeRegExp);
  const nameRe = new RegExp(`(?<!${NAME_CHAR})(${longestFirst.join("|")})(?!${NAME_CHAR})`, "g");
  for (const repo of repositories.filter((candidate) => candidate.fork && candidate.error === "")) {
    if (!withSkill.has(repo.repository) || names.length === 0) {
      forksNotScanned.push(repo.repository);
      continue;
    }
    const scan: ForkScanRecord = {
      type: "fork-scan",
      repository: repo.repository,
      max_bytes: FORK_SCAN_MAX_BYTES,
      files_read: 0,
      skipped_large: 0,
      skipped_binary: 0,
      files_naming_a_skill: 0,
      failures: [],
    };
    const text = repo.tree.filter(
      (entry) =>
        entry.type === "blob" &&
        entry.mode !== "120000" &&
        (Object.hasOwn(TEXT_EXTENSIONS, path.posix.extname(entry.path).toLowerCase()) ||
          Object.hasOwn(TEXT_NAMES, path.posix.basename(entry.path))) &&
        insideSkill(copiesIn(repo.repository), entry.path) === undefined
    );
    const small = text.filter((entry) => (entry.size ?? 0) <= FORK_SCAN_MAX_BYTES);
    scan.skipped_large = text.length - small.length;
    io.err(
      `copies: reading the fork ${repo.repository} from its tree (${small.length} text files)`
    );
    await github.prefetch(repo.repository, small);
    await mapLimit(small, REST_JOBS, async (entry) => {
      let body: string;
      try {
        body = await github.blob(repo.repository, entry.sha);
      } catch (error) {
        scan.failures.push(`${entry.path}: ${(error as Error).message}`);
        return;
      }
      scan.files_read++;
      if (body.includes("\u0000")) {
        scan.skipped_binary++;
        return;
      }
      let named = false;
      for (const name of new Set([...body.matchAll(nameRe)].map((match) => match[1] ?? ""))) {
        const kinds = mentions(body, name, pluginsOf.get(name) ?? [], entry.path);
        if (kinds.length === 0) continue;
        named = true;
        addLoader(repo.repository, entry.path, name, kinds, "fork scan");
      }
      if (named) scan.files_naming_a_skill++;
    });
    for (const failure of scan.failures) failures.push(`${repo.repository}:${failure} (fork scan)`);
    forkScans.push(scan);
  }

  // Resolve with the manifests the hits found too, and list what each manifest names as a loader.
  const allManifests = [...manifests, ...extraManifests].sort(
    (a, b) => a.repository.localeCompare(b.repository) || a.path.localeCompare(b.path)
  );
  const index = new CopyIndex(copies);
  const resolved = resolveInstallers([...read, ...extraManifests], index);
  for (const manifest of allManifests) {
    manifest.rows = resolved.rows.filter(
      (row) => row.manifestRepository === manifest.repository && row.manifestPath === manifest.path
    ).length;
  }
  for (const row of resolved.rows) {
    if (
      row.skill !== "" &&
      index.byName.has(row.skill) &&
      (row.action === "loads" || row.action === "stages" || row.action === "ignores")
    ) {
      addLoader(
        row.manifestRepository,
        row.manifestPath,
        row.skill,
        [`manifest:${row.kind}`],
        "manifest"
      );
    }
  }

  // 6. The prefix table, held to the configuration's targets.
  const targets = new Set(config.targets.map((target) => target.toLowerCase()));
  const prefixes = resolved.prefixes.filter((row) => targets.has(row.repository.toLowerCase()));
  const mapped = new Set(prefixes.map((row) => row.repository.toLowerCase()));

  return {
    at: options.at,
    scope:
      only.length > 0
        ? `Only ${options.only.join(", ")} (--only).`
        : `Owners: ${config.owners.join(", ")}.`,
    listingErrors,
    missing,
    repositories,
    copies,
    excluded,
    manifests: allManifests,
    notOfKind,
    rows: resolved.rows,
    prefixes,
    offTargets: resolved.prefixes.filter((row) => !targets.has(row.repository.toLowerCase())),
    targetsUnused: config.targets.filter((target) => !mapped.has(target.toLowerCase())),
    loaders: [...loaders.values()].sort(
      (a, b) =>
        a.skill.localeCompare(b.skill) ||
        a.repository.localeCompare(b.repository) ||
        a.path.localeCompare(b.path)
    ),
    records,
    forkScans,
    forksNotScanned,
    failures,
    searchCalls: search.calls,
    budget: options.maxSearchCalls,
  };
}

const USAGE =
  "usage: copies.ts --config <skill-cuts.json> --at <ISO instant> --out <dir> [--only <owner/repo>]... [--max-search-calls <n>]";

/** The command. Returns the exit status: 0 (the list is written), 1 (the run failed), 2 (refused). */
export async function main(argv: readonly string[], io: CopiesIo): Promise<number> {
  const refuse = (why: string) => {
    io.err(`copies: ${why}`);
    return 2;
  };
  let values: {
    config?: string;
    at?: string;
    out?: string;
    only?: string[];
    "max-search-calls"?: string;
  };
  try {
    ({ values } = parseArgs({
      args: [...argv],
      options: {
        config: { type: "string" },
        at: { type: "string" },
        out: { type: "string" },
        only: { type: "string", multiple: true },
        "max-search-calls": { type: "string" },
      },
      strict: true,
      allowPositionals: false,
    }));
  } catch (error) {
    return refuse(`${(error as Error).message}. ${USAGE}`);
  }
  if (values.config === undefined || values.at === undefined || values.out === undefined) {
    return refuse(`--config, --at and --out are required. ${USAGE}`);
  }
  if (
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$/.test(values.at) ||
    Number.isNaN(Date.parse(values.at))
  ) {
    return refuse(`--at ${JSON.stringify(values.at)} is not an ISO instant ending in Z`);
  }
  const only = values.only ?? [];
  const badRepository = only.find(
    (repository) => !/^[A-Za-z0-9][A-Za-z0-9-]*\/[A-Za-z0-9._-]+$/.test(repository)
  );
  if (badRepository !== undefined)
    return refuse(`--only ${JSON.stringify(badRepository)} is not owner/name`);
  const budget = values["max-search-calls"];
  if (budget !== undefined && !/^\d+$/.test(budget))
    return refuse(`--max-search-calls ${JSON.stringify(budget)} is not a whole number`);
  let config: SkillCutsConfig;
  try {
    config = loadConfig(values.config);
  } catch (error) {
    return refuse((error as Error).message);
  }
  try {
    const result = await listCopies(
      config,
      {
        at: values.at,
        only,
        maxSearchCalls: budget === undefined ? Number.POSITIVE_INFINITY : Number(budget),
      },
      io
    );
    const written = writeOutputs(values.out, result);
    io.out(
      `${result.repositories.length} repositories, ${result.copies.length} skill copies, ${result.manifests.length} manifests, ` +
        `${result.loaders.length} loader rows, ${result.prefixes.length} prefix rows, ${result.searchCalls} code search calls\n` +
        written.map((file) => `${file}\n`).join("")
    );
    return 0;
  } catch (error) {
    io.err(`copies: ${(error as Error).stack ?? (error as Error).message}`);
    return 1;
  }
}

if (import.meta.main) {
  process.exit(
    await main(Bun.argv.slice(2), {
      gh: spawnGh,
      sleep: (ms) => Bun.sleep(ms),
      now: () => Date.now(),
      out: (text) => process.stdout.write(text),
      err: (text) => process.stderr.write(`${text}\n`),
    })
  );
}
