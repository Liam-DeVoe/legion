import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { createHash } from "node:crypto";
import { mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import {
  type GhResult,
  lenientFields,
  MANIFEST_KINDS,
  type ManifestKind,
  main,
  manifestKind,
  mentions,
  PREFIX_TABLE_COLUMNS,
  parseManifest,
  parseTsv,
  prefixMatches,
  type QueryRecord,
  type ReaderSkill,
  SKILL_COPIES_COLUMNS,
  searchTerms,
  skillFields,
  type TreeEntry,
  tsv,
} from "./copies";

// copies.ts on F6 (testdata/CAPTURE.md, "F6: GitHub's answers for the lister, and made-up
// manifests"): GitHub's own answers for the public cybermonklabs/legion, captured with read-only gh
// calls (CAPTURE.md gives each command), and the made-up files of testdata/F6-madeup/, each in the
// shape of a real file of its kind that no public repository of that owner holds (CAPTURE.md's
// table says which). gh is the stand-in `World.gh` below: no test reaches the network.
//
// The world the stand-in answers for, beside F6:
// - cybermonklabs/legion: F6-repo-list.json, F6-commits.json (its tree is F6-tree.json, cut to the
//   skills and the manifests) and the F6-contents-*.json blobs. Each SKILL.md and agent definition
//   F6 holds no content for gets a stand-in frontmatter (its directory's name, a made-up
//   description), but skills/asd-ste100/SKILL.md, whose read fails, to show a failed read named.
// - cybermonklabs/oh-my-pi (F6's fork): its tree read fails (HTTP 404), to show a repository named
//   with its error.
// - acme/skills, a made-up repository: the F6-madeup files, plus files made here, each following
//   a real file: `.claude-plugin/marketplace.json` (F6-contents-marketplace.json's shape, and F3's
//   made-up acme-market marketplace of CAPTURE.md), SKILL.md files in each frontmatter shape,
//   `packages/acme-envoy/package.json` (F6-contents-pi-envoy-package.json's `omp.skills`), a
//   symbolic link `packages/acme-envoy/skills/beta` (as packages/claude-envoy/skills/* link to
//   skills/* in cybermonklabs/legion), a draft skill under the excluded `pipeline/candidates/`,
//   and a guide naming skills.
// - acme/old-skills, a made-up archived fork of acme/skills holding one skill, read from its tree.
// - acme-gone, an owner whose listing fails.

const TESTDATA = path.join(import.meta.dir, "testdata");
const AT = "2026-11-20T00:00:00Z";
const CLOCK_START = 1_795_140_000_000;

const sha1 = (text: string) => createHash("sha1").update(text).digest("hex");
/** A git blob id: what GitHub's trees give for a file of this text. */
const blobSha = (text: string) => sha1(`blob ${Buffer.byteLength(text)}\0${text}`);
const fixture = (name: string) => readFileSync(path.join(TESTDATA, name), "utf8");
const fixtureJson = (name: string): unknown => JSON.parse(fixture(name));

/** A made-up file of a made-up repository. */
interface MadeFile {
  readonly path: string;
  readonly text: string;
  readonly mode?: string;
}

/** A tree of git entries for files: blobs, and a tree entry for each directory. */
function treeOf(repository: string, files: readonly MadeFile[]): TreeEntry[] {
  const dirs = new Set<string>();
  for (const file of files) {
    const parts = file.path.split("/");
    for (let depth = 1; depth < parts.length; depth++) dirs.add(parts.slice(0, depth).join("/"));
  }
  const entries: TreeEntry[] = [...dirs].map((dir) => ({
    path: dir,
    mode: "040000",
    type: "tree",
    sha: sha1(`tree ${repository} ${dir}`),
  }));
  for (const file of files) {
    entries.push({
      path: file.path,
      mode: file.mode ?? "100644",
      type: "blob",
      sha: blobSha(file.text),
      size: Buffer.byteLength(file.text),
    });
  }
  return entries.sort((a, b) => a.path.localeCompare(b.path));
}

/** Every file of testdata/F6-madeup, as one made-up repository's files. */
function madeUpFiles(): MadeFile[] {
  const root = path.join(TESTDATA, "F6-madeup");
  const walk = (dir: string): string[] =>
    readdirSync(dir).flatMap((name) => {
      const full = path.join(dir, name);
      return statSync(full).isDirectory() ? walk(full) : [full];
    });
  return walk(root).map((full) => ({
    path: path.relative(root, full),
    text: readFileSync(full, "utf8"),
  }));
}

const skill = (frontmatter: string, body = "# A made-up skill\n") =>
  `---\n${frontmatter}\n---\n\n${body}`;

/** acme/skills beside the F6-madeup files. */
const ACME_FILES: MadeFile[] = [
  {
    path: ".claude-plugin/marketplace.json",
    text: `${JSON.stringify(
      {
        name: "acme-market",
        owner: { name: "Acme Fixtures" },
        description: "A made-up marketplace of a fixture.",
        plugins: [
          { name: "acme-tools", source: "./plugins/acme-tools", description: "Made-up tools." },
          {
            name: "plugin-gamma",
            source: "./plugins/plugin-gamma",
            description: "A made-up plugin a project leaves out.",
          },
          {
            name: "acme-envoy",
            source: { source: "npm", package: "@acme/acme-envoy", version: ">=1.0.0" },
          },
          // In the shape of cybermonklabs/legion's claude-envoy: an npm package with no
          // omp.skills, whose plugin.json and skills/ links sit in its directory.
          { name: "acme-claude", source: { source: "npm", package: "@acme/acme-claude" } },
        ],
      },
      null,
      2
    )}\n`,
  },
  {
    path: "plugins/acme-tools/skills/alpha/SKILL.md",
    text: skill(
      "name: alpha\ndescription: Use when the alpha checklist of a made-up project is needed."
    ),
  },
  {
    path: "plugins/acme-tools/skills/tool-loop-alpha/SKILL.md",
    text: skill(
      'name: tool-loop-alpha\ndescription: "Use when the \\"loop\\" of alpha runs:\\nstep by step."'
    ),
  },
  {
    path: "plugins/plugin-gamma/skills/gamma-one/SKILL.md",
    text: skill("description: |\n  Line one of gamma.\n  Line two of gamma."),
  },
  {
    path: "skills/alpha/SKILL.md",
    text: skill("name: alpha\ndescription: >-\n  Use when the alpha\n  steps are needed."),
  },
  {
    path: "skills/beta/SKILL.md",
    text: skill("name: beta\ndescription: 'Use when beta''s greeting is needed.'"),
  },
  {
    path: "skills/legion-worker/SKILL.md",
    text: skill("name: legion-worker\ndescription: A made-up copy of a shared name."),
  },
  {
    path: "skills/dispatch/SKILL.md",
    text: skill("name: dispatch\ndescription: Use when: a made-up task needs a ticket"),
  },
  {
    path: "pipeline/candidates/draft-one/SKILL.md",
    text: skill("name: draft-one\ndescription: A draft no plugin ships."),
  },
  {
    path: "packages/acme-envoy/package.json",
    text: `${JSON.stringify({ name: "@acme/acme-envoy", version: "1.0.0", omp: { skills: ["skills"] } }, null, 2)}\n`,
  },
  { path: "packages/acme-envoy/skills/beta", text: "../../../skills/beta", mode: "120000" },
  {
    path: "packages/acme-claude/package.json",
    text: `${JSON.stringify({ name: "@acme/acme-claude", version: "1.0.0" }, null, 2)}\n`,
  },
  {
    path: "packages/acme-claude/.claude-plugin/plugin.json",
    text: `${JSON.stringify({ name: "acme-claude", description: "Made-up." }, null, 2)}\n`,
  },
  { path: "packages/acme-claude/skills/alpha", text: "../../../skills/alpha", mode: "120000" },
  {
    path: "docs/guide.md",
    text: "# Guide\n\nLoad skill://beta first. The tool-loop-alpha skill runs the loop.\n",
  },
];

const OLD_FILES: MadeFile[] = [
  {
    path: "skills/alpha/SKILL.md",
    text: skill("name: alpha\ndescription: >-\n  Use when the alpha\n  steps are needed."),
  },
  { path: "README.md", text: "# Old skills\n\nRead skill://beta before you start.\n" },
  { path: "notes/big.md", text: `${"x".repeat(200_001)}\n` },
  { path: "assets/data.json", text: '{"bytes": "\u0000\u0001"}\n' },
];

interface SearchReply {
  readonly status?: number;
  readonly code?: number;
  readonly stderr?: string;
  readonly headers?: Record<string, string>;
  readonly body?: unknown;
}

const EMPTY_SEARCH = { total_count: 0, incomplete_results: false, items: [] };

/** The stand-in gh and the world it answers for. */
class World {
  readonly calls: string[][] = [];
  readonly sleeps: number[] = [];
  clock = CLOCK_START;
  readonly listings = new Map<string, unknown>();
  readonly users = new Map<string, string>();
  readonly commits = new Map<string, unknown>();
  /** `<repository> <sha>` or `<repository> <sha> recursive`: a tree answer, or an error line. */
  readonly trees = new Map<string, unknown>();
  readonly blobs = new Map<string, string>();
  readonly failingBlobs = new Set<string>();
  /** Each GraphQL blob batch: the repository its `owner` and `name` fields name, and its blobs. */
  readonly batches: { repository: string; blobs: number }[] = [];
  search: (q: string, page: number, call: number) => SearchReply = () => ({ body: EMPTY_SEARCH });
  /** When set, each owner's first search answer is held until this many owners have a search in
   *  flight: a run that searched the owners one after another would never release it. */
  holdFirstSearches = 0;
  /** Each search call: the owner its scope names, the clock when it started, searches in flight. */
  readonly searchLog: { owner: string; at: number; inFlight: number; ownerInFlight: number }[] = [];
  private readonly inFlight = new Map<string, number>();
  private readonly searched = new Set<string>();
  private readonly firstSearches = Promise.withResolvers<void>();
  private searchCalls = 0;

  readonly now = () => this.clock;
  readonly sleep = async (ms: number) => {
    this.sleeps.push(ms);
    this.clock += ms;
  };

  /** A repository whose tree is `entries`, at a made-up commit. */
  addRepository(
    repository: string,
    entries: readonly TreeEntry[],
    rootSha = sha1(`root ${repository}`)
  ) {
    this.commits.set(repository, [
      { sha: sha1(`commit ${repository}`), commit: { tree: { sha: rootSha } } },
    ]);
    this.trees.set(`${repository} ${rootSha} recursive`, {
      sha: rootSha,
      tree: entries,
      truncated: false,
    });
  }

  addFiles(files: readonly MadeFile[]) {
    for (const file of files) this.blobs.set(blobSha(file.text), file.text);
  }

  readonly gh = async (args: readonly string[]): Promise<GhResult> => {
    this.calls.push([...args]);
    const ok = (value: unknown): GhResult => ({
      code: 0,
      stdout: JSON.stringify(value),
      stderr: "",
    });
    const fail = (line: string): GhResult => ({ code: 1, stdout: "", stderr: `${line}\n` });
    const answer = (value: unknown) => (typeof value === "string" ? fail(value) : ok(value));
    if (args[0] === "repo" && args[1] === "list") {
      const listing = this.listings.get(args[2] ?? "");
      return listing === undefined
        ? fail(`GraphQL: Could not resolve to a User with the login of '${args[2]}'.`)
        : answer(listing);
    }
    if (args[0] !== "api") return fail(`stand-in gh: no answer for ${args.join(" ")}`);
    if (args[1] === "graphql") {
      // As the pod's gh routes it: a GraphQL call takes the token of the owner its `owner` field
      // names, and any other token cannot see a private repository. A blob batch as GitHub answers
      // it: a blob it cannot give is null, a binary one has no text.
      const field = (name: string) =>
        args.find((arg) => arg.startsWith(`${name}=`))?.slice(name.length + 1);
      const query = field("query") ?? "";
      const [owner, name] = [field("owner"), field("name")];
      if (
        owner === undefined ||
        name === undefined ||
        !query.includes("repository(owner: $owner, name: $name)")
      ) {
        return fail(`gh: Could not resolve to a Repository with the name '${query}'.`);
      }
      const fields = [...query.matchAll(/(b\d+): object\(oid: "([0-9a-f]{40})"\)/g)];
      this.batches.push({ repository: `${owner}/${name}`, blobs: fields.length });
      const repository = Object.fromEntries(
        fields.map(([, alias = "", sha = ""]) => {
          const text = this.blobs.get(sha);
          if (text === undefined || this.failingBlobs.has(sha)) return [alias, null];
          const binary = text.includes("\u0000");
          return [alias, { isBinary: binary, isTruncated: false, text: binary ? null : text }];
        })
      );
      return ok({ data: { repository } });
    }
    if (args.includes("search/code")) {
      const field = (name: string) =>
        args.find((arg) => arg.startsWith(`${name}=`))?.slice(name.length + 1) ?? "";
      this.searchCalls++;
      const q = field("q");
      const owner = /(?:org|user|repo):([^/\s]+)/.exec(q)?.[1] ?? "";
      const ownerInFlight = (this.inFlight.get(owner) ?? 0) + 1;
      this.inFlight.set(owner, ownerInFlight);
      const inFlight = [...this.inFlight.values()].reduce((sum, count) => sum + count, 0);
      this.searchLog.push({ owner, at: this.clock, inFlight, ownerInFlight });
      const reply = this.search(q, Number(field("page")), this.searchCalls);
      if (this.holdFirstSearches > 0 && !this.searched.has(owner)) {
        this.searched.add(owner);
        if (this.searched.size >= this.holdFirstSearches) this.firstSearches.resolve();
        await this.firstSearches.promise;
      }
      this.inFlight.set(owner, (this.inFlight.get(owner) ?? 1) - 1);
      const status = reply.status ?? 200;
      const headers = {
        "Content-Type": "application/json",
        "X-Ratelimit-Limit": "10",
        "X-Ratelimit-Remaining": "9",
        "X-Ratelimit-Reset": String(Math.floor(this.clock / 1000) + 60),
        "X-Ratelimit-Resource": "code_search",
        ...reply.headers,
      };
      const head = [
        `HTTP/2.0 ${status} ${status === 200 ? "OK" : "Forbidden"}`,
        ...Object.entries(headers).map(([k, v]) => `${k}: ${v}`),
      ];
      return {
        code: reply.code ?? (status < 300 ? 0 : 1),
        stdout: `${head.join("\r\n")}\r\n\r\n${JSON.stringify(reply.body ?? {})}`,
        stderr: reply.stderr ?? "",
      };
    }
    const endpoint = args[1] ?? "";
    if (endpoint === "rate_limit") {
      const reset = Math.floor(this.clock / 1000) + 45;
      return ok({
        resources: {
          core: { limit: 15000, remaining: 0, reset },
          code_search: { limit: 10, remaining: 0, reset },
        },
      });
    }
    const user = /^users\/([^/]+)$/.exec(endpoint);
    if (user !== null) {
      const type = this.users.get(user[1] ?? "");
      return type === undefined ? fail("gh: Not Found (HTTP 404)") : ok({ login: user[1], type });
    }
    const commits =
      /^repos\/([^/]+\/[^/]+)\/commits\?sha=main&until=2026-11-20T00%3A00%3A00Z&per_page=1$/.exec(
        endpoint
      );
    if (commits !== null) {
      const found = this.commits.get(commits[1] ?? "");
      return found === undefined
        ? fail("gh: No commit found for SHA: main (HTTP 422)")
        : answer(found);
    }
    const tree = /^repos\/([^/]+\/[^/]+)\/git\/trees\/([0-9a-f]+)(\?recursive=1)?$/.exec(endpoint);
    if (tree !== null) {
      const found = this.trees.get(`${tree[1]} ${tree[2]}${tree[3] ? " recursive" : ""}`);
      return found === undefined ? fail("gh: Not Found (HTTP 404)") : answer(found);
    }
    const blob = /^repos\/([^/]+\/[^/]+)\/git\/blobs\/([0-9a-f]+)$/.exec(endpoint);
    if (blob !== null) {
      const sha = blob[2] ?? "";
      const text = this.blobs.get(sha);
      if (text === undefined || this.failingBlobs.has(sha)) return fail("gh: Not Found (HTTP 404)");
      return ok({
        sha,
        size: Buffer.byteLength(text),
        content: Buffer.from(text).toString("base64"),
        encoding: "base64",
      });
    }
    return fail(`stand-in gh: no answer for ${args.join(" ")}`);
  };
}

/** F6's tree of cybermonklabs/legion. */
function legionTree(): { sha: string; entries: TreeEntry[] } {
  const tree = fixtureJson("F6-tree.json") as { sha: string; tree: TreeEntry[] };
  return { sha: tree.sha, entries: tree.tree };
}

/** F6's commit's tree id: the stand-in answers F6-tree.json for it. */
const LEGION_ROOT =
  (fixtureJson("F6-commits.json") as { commit: { tree: { sha: string } } }[])[0]?.commit.tree.sha ??
  "";

/** The world of F6 and the made-up repositories. */
function world(): World {
  const w = new World();
  w.listings.set("cybermonklabs", fixtureJson("F6-repo-list.json"));
  w.listings.set("acme", [
    {
      nameWithOwner: "acme/skills",
      isFork: false,
      isArchived: false,
      defaultBranchRef: { name: "main" },
      parent: null,
    },
    {
      nameWithOwner: "acme/old-skills",
      isFork: true,
      isArchived: true,
      defaultBranchRef: { name: "main" },
      parent: { id: "R_madeup", name: "skills", owner: { id: "U_madeup", login: "acme" } },
    },
  ]);
  w.users.set("cybermonklabs", "Organization");
  w.users.set("acme", "User");

  w.commits.set("cybermonklabs/legion", fixtureJson("F6-commits.json"));
  const legion = legionTree();
  w.trees.set(`cybermonklabs/legion ${LEGION_ROOT} recursive`, fixtureJson("F6-tree.json"));
  for (const name of readdirSync(TESTDATA).filter((file) => file.startsWith("F6-contents-"))) {
    const contents = fixtureJson(name) as { sha: string; content: string };
    w.blobs.set(
      contents.sha,
      Buffer.from(contents.content.replace(/\n/g, ""), "base64").toString("utf8")
    );
  }
  for (const entry of legion.entries) {
    if (entry.type !== "blob" || w.blobs.has(entry.sha)) continue;
    const dir = path.posix.dirname(entry.path);
    if (entry.path.endsWith("/SKILL.md")) {
      w.blobs.set(
        entry.sha,
        skill(
          `name: ${path.posix.basename(dir)}\ndescription: A stand-in description of ${path.posix.basename(dir)}.`
        )
      );
    } else if (dir === "packages/pi-legion/agents") {
      w.blobs.set(
        entry.sha,
        skill(
          `name: ${path.posix.basename(entry.path, ".md")}\ndescription: A stand-in agent.`,
          "# Agent\n"
        )
      );
    }
  }
  const failing = legion.entries.find((entry) => entry.path === "skills/asd-ste100/SKILL.md");
  if (failing !== undefined) w.failingBlobs.add(failing.sha);

  w.commits.set("cybermonklabs/oh-my-pi", [
    { sha: sha1("commit oh-my-pi"), commit: { tree: { sha: sha1("root oh-my-pi") } } },
  ]);

  const acme = [...madeUpFiles(), ...ACME_FILES];
  w.addFiles(acme);
  w.addRepository("acme/skills", treeOf("acme/skills", acme));
  w.addFiles(OLD_FILES);
  w.addRepository("acme/old-skills", treeOf("acme/old-skills", OLD_FILES));
  w.search = acmeSearch;
  return w;
}

const CONFIG = {
  owners: ["cybermonklabs", "acme", "acme-gone"],
  stores: [{ name: "store_a", context: "devbox" }],
  psqlCommand: "/usr/bin/psql",
  gatewayBaseUrl: "http://127.0.0.1:4000",
  excludePaths: [{ repo: "acme/skills", prefix: "pipeline/candidates/" }],
  targets: ["cybermonklabs/legion", "acme/skills"],
  cuts: [{ id: "skills-1", repo: "acme/skills", pr: 1 }],
  windowStart: "2026-10-23T00:00:00Z",
  windowEnd: AT,
};

const scratch: string[] = [];
afterAll(() => {
  for (const dir of scratch) rmSync(dir, { recursive: true, force: true });
});

interface Run {
  readonly code: number;
  readonly errors: string[];
  text(name: string): string;
  table(name: string): Record<string, string>[];
  queries(): Record<string, unknown>[];
}

async function run(
  w: World,
  args: readonly string[] = [],
  config: Record<string, unknown> = {}
): Promise<Run> {
  const dir = mkdtempSync(path.join(tmpdir(), "copies-test-"));
  scratch.push(dir);
  const file = path.join(dir, "skill-cuts.json");
  writeFileSync(file, JSON.stringify({ ...CONFIG, ...config }));
  const out = path.join(dir, "out");
  const errors: string[] = [];
  const code = await main(["--config", file, "--at", AT, "--out", out, ...args], {
    gh: w.gh,
    sleep: w.sleep,
    now: w.now,
    out: () => {},
    err: (line) => errors.push(line),
  });
  const text = (name: string) => readFileSync(path.join(out, name), "utf8");
  return {
    code,
    errors,
    text,
    table: (name) => parseTsv(text(name)),
    queries: () =>
      text("queries.jsonl")
        .split("\n")
        .filter((line) => line !== "")
        .map((line) => JSON.parse(line) as Record<string, unknown>),
  };
}

/** The installer rows of a manifest, as `<action> <unit> <skill> <copy repository> <copy path>`. */
function installerRows(r: Run, repository: string, manifestPath: string): string[] {
  return r
    .table("installers.tsv")
    .filter((row) => row.manifest_repository === repository && row.manifest_path === manifestPath)
    .map((row) =>
      [row.action, row.unit, row.skill, row.copy_repository, row.copy_path].join(" ").trim()
    );
}

/** One example of each manifest kind in the world, what it says, and a row it gives. A kind added
 *  to MANIFEST_KINDS without an example here fails to typecheck and fails the test below. */
const KIND_EXAMPLES: Record<
  ManifestKind,
  { repository: string; path: string; says: string; row: string }
> = {
  "claude-marketplace": {
    repository: "acme/skills",
    path: ".claude-plugin/marketplace.json",
    says: "marketplace acme-market: acme-tools (plugins/acme-tools), plugin-gamma (plugins/plugin-gamma), acme-envoy (npm @acme/acme-envoy), acme-claude (npm @acme/acme-claude)",
    row: "ships plugin:acme-tools@acme-market tool-loop-alpha acme/skills plugins/acme-tools/skills/tool-loop-alpha",
  },
  "claude-plugin": {
    repository: "acme/skills",
    path: "plugins/acme-tools/.claude-plugin/plugin.json",
    says: "plugin acme-tools 0.3.0: skills ./skills/tool-loop-alpha, ./skills/alpha",
    row: "",
  },
  "omp-plugins": {
    repository: "cybermonklabs/legion",
    path: "packages/daemon/docker/omp-plugins/package.json",
    says: "installs @bopstack/pi-codegraph@0.1.1",
    row: "installs package:@bopstack/pi-codegraph",
  },
  "omp-package": {
    repository: "cybermonklabs/legion",
    path: "packages/pi-legion/package.json",
    says: "package @acme/pi-legion: omp.skills dist/skills",
    row: "ships package:@acme/pi-legion legion-worker cybermonklabs/legion skills/legion-worker",
  },
  "claude-settings": {
    repository: "acme/skills",
    path: ".claude/settings.json",
    says: "plugins acme-tools@acme-market on, beta-pack@acme-market off; marketplaces acme-market",
    row: "enables plugin:acme-tools@acme-market alpha acme/skills plugins/acme-tools/skills/alpha",
  },
  "omp-config": {
    repository: "acme/skills",
    path: "omp/config.yml",
    says: "skills.ignoredSkills beta, gamma-*",
    row: "ignores omp-settings gamma-one acme/skills plugins/plugin-gamma/skills/gamma-one",
  },
  "agent-definition": {
    repository: "cybermonklabs/legion",
    path: "packages/pi-legion/agents/thermonuclear-deep-review.md",
    says: "agent thermonuclear-deep-review: autoloadSkills thermonuclear-deep-review",
    row: "loads agent:thermonuclear-deep-review thermonuclear-deep-review cybermonklabs/legion skills/thermonuclear-deep-review",
  },
  "legion-project": {
    repository: "acme/skills",
    path: "legion/projects/10-ACME.yaml",
    says: "project ACME: verificationSkillsLeaveOut plugin-gamma, plugin-delta",
    row: "leaves out project:ACME gamma-one acme/skills plugins/plugin-gamma/skills/gamma-one",
  },
  "prepack-script": {
    repository: "cybermonklabs/legion",
    path: "scripts/pi-plugin-prepack.sh",
    says: "stages skills/<name> into dist/skills: @acme/pi-envoy: agent-secrets approval-review asd-ste100 dispatch",
    row: "stages package:@acme/pi-legion legion-worker cybermonklabs/legion skills/legion-worker",
  },
  "build-script": {
    repository: "cybermonklabs/legion",
    path: "packages/claude-envoy/scripts/build.ts",
    says: "generates the plugin legion from the skills prepack stages for @acme/pi-legion",
    row: "ships plugin:legion legion-worker cybermonklabs/legion skills/legion-worker",
  },
  "installer-script": {
    repository: "acme/skills",
    path: "installers/skills.sh",
    says: "links each skill of skills/ into the claude-code user skills; links each skill of skills/ into the omp user skills; links omp/config.yml as the Oh My Pi config.yml",
    row: "links user-skills:omp beta acme/skills skills/beta",
  },
  "python-loader": {
    repository: "acme/skills",
    path: "tree/init.py",
    says: "loads roles.proposer: alpha, beta; roles.prover: alpha, tool-loop-alpha; roles.critic: alpha",
    row: "loads loader:tree/init.py#roles.prover tool-loop-alpha acme/skills plugins/acme-tools/skills/tool-loop-alpha",
  },
  "skill-link": {
    repository: "acme/skills",
    path: "packages/acme-envoy/skills/beta",
    says: "links ../../../skills/beta (skills/beta)",
    row: "links link:packages/acme-envoy/skills/beta beta acme/skills skills/beta",
  },
};

describe("the full run over the owners", () => {
  const w = world();
  let r: Run;
  beforeAll(async () => {
    r = await run(w);
  });

  test("it exits 0 and writes every output", () => {
    expect(r.code).toBe(0);
    for (const name of [
      "skill-copies.tsv",
      "prefix-table.tsv",
      "repositories.tsv",
      "loaders.tsv",
      "queries.jsonl",
      "skill-copies.md",
    ]) {
      expect(r.text(name).length).toBeGreaterThan(0);
    }
  });

  test.each(
    MANIFEST_KINDS.map((kind) => kind.kind)
  )("the %s manifest kind is read and shown", (kind) => {
    const example = KIND_EXAMPLES[kind];
    const shown = r
      .table("manifests.tsv")
      .find((row) => row.repository === example.repository && row.path === example.path);
    expect(shown?.kind).toBe(kind);
    expect(shown?.error).toBe("");
    expect(shown?.says ?? "").toContain(example.says);
    if (example.row !== "")
      expect(installerRows(r, example.repository, example.path)).toContain(example.row);
  });

  test("every tree file of a manifest kind that names skills is shown (the class inventory)", () => {
    const shown = new Set(
      r.table("manifests.tsv").map((row) => `${row.repository} ${row.path} ${row.kind}`)
    );
    const trees: [string, TreeEntry[]][] = [
      ["cybermonklabs/legion", legionTree().entries],
      ["acme/skills", treeOf("acme/skills", [...madeUpFiles(), ...ACME_FILES])],
      ["acme/old-skills", treeOf("acme/old-skills", OLD_FILES)],
    ];
    let checked = 0;
    for (const [repository, entries] of trees) {
      for (const entry of entries) {
        const kind = manifestKind(entry);
        const text = w.blobs.get(entry.sha);
        if (kind === undefined || text === undefined) continue;
        if (!parseManifest(kind, entry.path, text).ofKind) continue;
        checked++;
        expect(shown).toContain(`${repository} ${entry.path} ${kind}`);
      }
    }
    expect(checked).toBeGreaterThanOrEqual(MANIFEST_KINDS.length);
  });

  test("the installers resolve through plugins, packages, links, settings and projects", () => {
    expect(installerRows(r, "cybermonklabs/legion", "scripts/pi-plugin-prepack.sh")).toContain(
      "stages package:@acme/pi-envoy dispatch cybermonklabs/legion skills/dispatch"
    );
    expect(installerRows(r, "cybermonklabs/legion", ".claude-plugin/marketplace.json")).toContain(
      "ships plugin:claude-envoy@legion-plugins"
    );
    expect(
      r.table("installers.tsv").find((row) => row.unit === "plugin:claude-envoy@legion-plugins")
        ?.note
    ).toBe("the npm package @acme/claude-envoy is in no package.json read");
    expect(installerRows(r, "acme/skills", ".claude-plugin/marketplace.json")).toContain(
      "ships plugin:acme-envoy@acme-market beta acme/skills skills/beta"
    );
    // An npm-sourced plugin whose package.json has no omp.skills: the package is still found, its
    // plugin.json is that plugin's, and its skills/ link reaches the copy.
    expect(installerRows(r, "acme/skills", ".claude-plugin/marketplace.json")).toContain(
      "ships plugin:acme-claude@acme-market alpha acme/skills skills/alpha"
    );
    expect(r.text("skill-copies.md")).toContain("1 omp-package");
    expect(installerRows(r, "acme/skills", ".claude/settings.json")).toContain(
      "disables plugin:beta-pack@acme-market"
    );
    expect(installerRows(r, "acme/skills", "legion/projects/10-ACME.yaml")).toContain(
      "leaves out project:ACME"
    );
    expect(installerRows(r, "acme/skills", "omp/config.yml")).toContain(
      "ignores omp-settings beta acme/skills skills/beta"
    );
    expect(installerRows(r, "acme/skills", "installers/skills.sh")).toEqual(
      expect.arrayContaining([
        "links user-skills:claude-code alpha acme/skills skills/alpha",
        "links user-skills:omp legion-worker acme/skills skills/legion-worker",
        "links config omp-settings",
      ])
    );
    expect(installerRows(r, "acme/skills", "packages/acme-envoy/package.json")).toContain(
      "ships package:@acme/acme-envoy beta acme/skills skills/beta"
    );
    // The plugin.json a marketplace names is read into that marketplace's plugin, not a unit of its own.
    expect(
      r
        .table("installers.tsv")
        .some((row) => row.unit === "plugin:acme-tools" || row.unit === "plugin:acme-claude")
    ).toBe(false);
  });

  test("the copies, their marks, installers and loaders are one row each in skill-copies.tsv", () => {
    expect(r.text("skill-copies.tsv").split("\n")[0]).toBe(SKILL_COPIES_COLUMNS.join("\t"));
    const copies = r.table("skill-copies.tsv");
    const old = copies.find((row) => row.repository === "acme/old-skills");
    expect(old).toMatchObject({
      path: "skills/alpha",
      name: "alpha",
      fork: "yes",
      archived: "yes",
      parent: "acme/skills",
    });
    const alpha = copies.find(
      (row) => row.repository === "acme/skills" && row.path === "skills/alpha"
    );
    expect(alpha?.description_bytes).toBe(
      String(Buffer.byteLength("Use when the alpha steps are needed."))
    );
    expect(alpha?.installers).toContain("links user-skills:omp (acme/skills:installers/skills.sh)");
    expect(alpha?.loaded_by).toContain(
      "acme/skills:tree/init.py (python-loader loader:tree/init.py#roles.proposer)"
    );
    const legion = copies.find(
      (row) => row.repository === "cybermonklabs/legion" && row.path === "skills/legion-worker"
    );
    expect(legion?.installers).toContain(
      "ships plugin:legion (cybermonklabs/legion:packages/claude-envoy/scripts/build.ts)"
    );
    expect(legion?.installers).toContain(
      "stages package:@acme/pi-legion (cybermonklabs/legion:scripts/pi-plugin-prepack.sh)"
    );
    expect(
      copies.find((row) => row.path === "skills/thermonuclear-deep-review")?.loaded_by
    ).toContain(
      "cybermonklabs/legion:packages/pi-legion/agents/thermonuclear-deep-review.md (agent-definition agent:thermonuclear-deep-review)"
    );
    expect(copies.some((row) => row.path.startsWith("pipeline/candidates/"))).toBe(false);
    expect(copies.find((row) => row.path === "plugins/plugin-gamma/skills/gamma-one")?.name).toBe(
      "gamma-one"
    );
    expect(
      copies.find((row) => row.repository === "acme/skills" && row.path === "skills/dispatch")
        ?.description_bytes
    ).toBe(String(Buffer.byteLength("Use when: a made-up task needs a ticket")));
  });

  test("shared names: across repositories and twice in one, forks and archived copies marked", () => {
    const md = r.text("skill-copies.md");
    expect(md).toContain(
      "- `alpha` (in 2 repositories; twice or more in acme/skills): acme/old-skills skills/alpha (fork of acme/skills, archived); acme/skills plugins/acme-tools/skills/alpha; acme/skills skills/alpha"
    );
    expect(md).toContain(
      "- `legion-worker` (in 2 repositories): acme/skills skills/legion-worker; cybermonklabs/legion skills/legion-worker"
    );
    expect(md).toContain("- `dispatch` (in 2 repositories)");
  });

  test("a repository whose tree read fails, an owner whose listing fails and a failed read are each named with the error", () => {
    const omp = r
      .table("repositories.tsv")
      .find((row) => row.repository === "cybermonklabs/oh-my-pi");
    expect(omp).toMatchObject({
      fork: "yes",
      parent: "upstream-owner/oh-my-pi",
      error: "tree read failed: gh: Not Found (HTTP 404)",
    });
    const md = r.text("skill-copies.md");
    expect(md).toContain(
      "- cybermonklabs/oh-my-pi (fork of upstream-owner/oh-my-pi): tree read failed: gh: Not Found (HTTP 404)"
    );
    expect(md).toContain(
      "- owner acme-gone: the listing failed: GraphQL: Could not resolve to a User with the login of 'acme-gone'."
    );
    expect(md).toContain(
      "- cybermonklabs/legion:skills/asd-ste100/SKILL.md: SKILL.md read failed: gh: Not Found (HTTP 404)"
    );
    const legion = r
      .table("repositories.tsv")
      .find((row) => row.repository === "cybermonklabs/legion");
    expect(legion).toMatchObject({
      commit: "63b3a613feb15cb191f87f4239467cdd37b62c82",
      tree_read: "whole",
      tree_entries: "105",
      error: "",
    });
  });

  test("the prefix table maps each installer kind to its repository copy", () => {
    expect(r.text("prefix-table.tsv").split("\n")[0]).toBe(PREFIX_TABLE_COLUMNS.join("\t"));
    const rows = new Set(
      r.table("prefix-table.tsv").map((row) => `${row.prefix} -> ${row.repository} ${row.path}`)
    );
    for (const expected of [
      // An Oh My Pi plugin package the prepack script stages, in the worker image and installed.
      "/opt/legion/pi-legion/dist/skills/legion-worker/ -> cybermonklabs/legion skills/legion-worker",
      "/opt/legion/pi-envoy/dist/skills/dispatch/ -> cybermonklabs/legion skills/dispatch",
      "**/node_modules/@acme/pi-legion/dist/skills/legion-worker/ -> cybermonklabs/legion skills/legion-worker",
      // The plugin the build script generates.
      "/opt/legion/claude-envoy/legion/skills/legion-worker/ -> cybermonklabs/legion skills/legion-worker",
      // A marketplace plugin, in Oh My Pi's cache and Claude Code's.
      "**/plugins/cache/plugins/acme-market___acme-tools___*/skills/alpha/ -> acme/skills plugins/acme-tools/skills/alpha",
      "**/plugins/cache/acme-market/acme-tools/*/skills/tool-loop-alpha/ -> acme/skills plugins/acme-tools/skills/tool-loop-alpha",
      // An npm-sourced marketplace plugin and its package, through a link.
      "**/plugins/cache/acme-market/acme-envoy/*/skills/beta/ -> acme/skills skills/beta",
      "**/node_modules/@acme/acme-envoy/skills/beta/ -> acme/skills skills/beta",
      "**/plugins/cache/acme-market/acme-claude/*/skills/alpha/ -> acme/skills skills/alpha",
      // An installer's links into each host's user skills.
      "**/.omp/agent/skills/beta/ -> acme/skills skills/beta",
      "**/.claude/skills/alpha/ -> acme/skills skills/alpha",
    ]) {
      expect(rows).toContain(expected);
    }
    expect(r.text("skill-copies.md")).not.toContain(
      "rows point at repositories the configuration's targets do not name"
    );
  });

  test("a code-search hit is kept only where it names the skill, and a substring of a longer name is not", () => {
    const alpha = r
      .queries()
      .find((query) => query.q === '"alpha" user:acme') as unknown as QueryRecord;
    expect(alpha.hits).toEqual([
      {
        repository: "acme/skills",
        path: "docs/guide.md",
        verdict: "dropped: only inside a longer name",
        match: "",
      },
      {
        repository: "acme/skills",
        path: "plugins/acme-tools/skills/alpha/SKILL.md",
        verdict: "inside the skill plugins/acme-tools/skills/alpha",
        match: "",
      },
      {
        repository: "acme/skills",
        path: "tree/init.py",
        verdict: "kept",
        match: "quoted-string,code-load",
      },
      {
        repository: "acme/skills",
        path: "docs/gone.md",
        verdict: `not in the tree at ${sha1("commit acme/skills")}`,
        match: "",
      },
    ]);
    const loaders = r.table("loaders.tsv");
    expect(loaders).toContainEqual({
      repository: "acme/skills",
      path: "docs/guide.md",
      skill: "beta",
      match: "skill-uri",
      source: "code search",
    });
    expect(loaders.some((row) => row.path === "docs/guide.md" && row.skill === "alpha")).toBe(
      false
    );
    expect(loaders).toContainEqual({
      repository: "acme/skills",
      path: "docs/guide.md",
      skill: "tool-loop-alpha",
      match: "name",
      source: "code search",
    });
    expect(
      loaders.find((row) => row.path === "tree/init.py" && row.skill === "alpha")
    ).toMatchObject({
      match: "quoted-string,code-load,manifest:python-loader",
      source: "code search, manifest",
    });
  });

  test("each name is searched once per owner listed, as the quoted name, and each query keeps its counts", () => {
    const queries = r.queries().filter((query) => query.type === "search");
    const names = [...new Set(r.table("skill-copies.tsv").map((row) => row.name))].sort();
    const owners = queries.filter((query) => query.reason === "owner");
    // acme-gone's listing failed, so it is not searched.
    expect(owners.map((query) => query.q).sort()).toEqual(
      names.flatMap((name) => [`"${name}" org:cybermonklabs`, `"${name}" user:acme`]).sort()
    );
    expect(queries.every((query) => query.status === "ok" && query.total_count !== null)).toBe(
      true
    );
  });

  test("a common word's queries run again per repository holding a skill, and no query word is one the pod's gh refuses", () => {
    const queries = r.queries().filter((query) => query.type === "search");
    const reruns = queries.filter(
      (query) => query.reason === "run again per repository: a common word"
    );
    expect(new Set(reruns.map((query) => query.name))).toEqual(new Set(["dispatch", "envoy"]));
    expect(new Set(reruns.map((query) => query.scope))).toEqual(
      new Set(["repo:cybermonklabs/legion", "repo:acme/skills"])
    );
    for (const query of queries) {
      for (const word of String(query.q).split(/\s+/)) {
        if (word.includes(":")) expect(word).toMatch(/^[A-Za-z][A-Za-z-]*:[^\s"'()\\]+$/);
      }
    }
  });

  test("blobs are read in GraphQL batches routed by their owner field, and only a blob no batch gives is read alone", () => {
    expect(w.batches.length).toBeGreaterThan(0);
    const read = new Set(["cybermonklabs/legion", "acme/skills", "acme/old-skills"]);
    for (const batch of w.batches) {
      expect(read).toContain(batch.repository);
      expect(batch.blobs).toBeLessThanOrEqual(50);
    }
    const alone = w.calls
      .map((call) => /\/git\/blobs\/([0-9a-f]{40})$/.exec(call[1] ?? "")?.[1])
      .filter((sha): sha is string => sha !== undefined);
    const failing = legionTree().entries.find(
      (entry) => entry.path === "skills/asd-ste100/SKILL.md"
    );
    const binary = OLD_FILES.find((file) => file.path === "assets/data.json");
    expect(new Set(alone)).toEqual(new Set([failing?.sha ?? "", blobSha(binary?.text ?? "")]));
  });

  test("a fork that holds a skill is read from its tree, under the size bound", () => {
    const scan = r.queries().find((query) => query.type === "fork-scan");
    expect(scan).toEqual({
      type: "fork-scan",
      repository: "acme/old-skills",
      max_bytes: 100_000,
      files_read: 2,
      skipped_large: 1,
      skipped_binary: 1,
      files_naming_a_skill: 1,
      failures: [],
    });
    expect(r.table("loaders.tsv")).toContainEqual({
      repository: "acme/old-skills",
      path: "README.md",
      skill: "beta",
      match: "skill-uri",
      source: "fork scan",
    });
    expect(w.calls.some((call) => call.join(" ").includes("repo:acme/old-skills"))).toBe(false);
  });

  test("same-job pairs and the sentence wait for the read command, with the skills list beside the copies", () => {
    expect(r.text("skill-copies.md")).toContain(
      "they wait for the `read` command (task t3), which reads skills.jsonl"
    );
    // skills.jsonl is written by copies.ts from readerSkills; read each line back as that type.
    const skills = r
      .text("skills.jsonl")
      .split("\n")
      .filter((line) => line !== "")
      .map((line) => JSON.parse(line) as ReaderSkill);
    expect(
      skills.find((entry) => entry.repository === "acme/skills" && entry.path === "skills/beta")
        ?.description
    ).toBe("Use when beta's greeting is needed.");
  });
});

// The stand-in's code search answers for the full run above.
function acmeSearch(q: string): SearchReply {
  const item = (repository: string, file: string) => ({
    name: path.posix.basename(file),
    path: file,
    sha: sha1(file),
    repository: { full_name: repository, fork: false },
  });
  if (q === '"alpha" user:acme') {
    return {
      body: {
        total_count: 4,
        incomplete_results: false,
        items: [
          item("acme/skills", "docs/guide.md"),
          item("acme/skills", "plugins/acme-tools/skills/alpha/SKILL.md"),
          item("acme/skills", "tree/init.py"),
          item("acme/skills", "docs/gone.md"),
        ],
      },
    };
  }
  if (q === '"beta" user:acme')
    return {
      body: {
        total_count: 1,
        incomplete_results: false,
        items: [item("acme/skills", "docs/guide.md")],
      },
    };
  if (q === '"tool-loop-alpha" user:acme') {
    return {
      body: {
        total_count: 1,
        incomplete_results: false,
        items: [item("acme/skills", "docs/guide.md")],
      },
    };
  }
  return { body: EMPTY_SEARCH };
}

describe("one repository with --only, on F6's code search answer", () => {
  const w = world();
  // F6-search-code.json as GitHub gave it, with one more hit, made here, on the omp-plugins
  // package.json of the same tree, whose name `legion-worker-omp-plugins` holds the skill's name
  // inside a longer one.
  const f6 = fixtureJson("F6-search-code.json") as {
    total_count: number;
    incomplete_results: boolean;
    items: unknown[];
  };
  const substring = {
    name: "package.json",
    path: "packages/daemon/docker/omp-plugins/package.json",
    sha: "760e942cfe64bd19e53a564b563047f345e06ecb",
    repository: { full_name: "cybermonklabs/legion", fork: false },
  };
  w.search = (q) =>
    q === '"legion-worker" repo:cybermonklabs/legion'
      ? { body: { ...f6, items: [...f6.items, substring] } }
      : { body: EMPTY_SEARCH };
  let r: Run;
  beforeAll(async () => {
    r = await run(w, ["--only", "cybermonklabs/legion"]);
  });

  test("only the named repository is read, and its queries name it", () => {
    expect(r.code).toBe(0);
    expect(r.table("repositories.tsv").map((row) => row.repository)).toEqual([
      "cybermonklabs/legion",
    ]);
    expect(w.calls.some((call) => call[0] === "repo" && call[2] === "acme")).toBe(false);
    expect(r.queries().every((query) => query.scope === "repo:cybermonklabs/legion")).toBe(true);
  });

  test("F6's hits: kept where the name stands whole, dropped inside a longer name, inside a skill or not in the tree", () => {
    // queries.jsonl is written by copies.ts from its QueryRecord values; read one back as that type.
    const query = r
      .queries()
      .find(
        (entry) => entry.q === '"legion-worker" repo:cybermonklabs/legion'
      ) as unknown as QueryRecord;
    expect(query.total_count).toBe(325);
    expect(query.incomplete_results).toBe(false);
    expect(query.items_read).toBe(31);
    // F6's answer is GitHub's first page of 30 (gh's default page size) of 325: the hits it did not
    // give are counted and listed, never dropped.
    expect(query.unread).toBe(294);
    expect(r.text("skill-copies.md")).toContain(
      '- "legion-worker" repo:cybermonklabs/legion: 325 hits, 31 read, 294 not read (GitHub\'s pages held fewer hits than its count)'
    );
    const verdict = (file: string) => query.hits.find((hit) => hit.path === file);
    expect(verdict("scripts/pi-plugin-prepack.sh")).toMatchObject({
      verdict: "kept",
      match: "name",
    });
    expect(verdict("packages/daemon/docker/omp-plugins/package.json")?.verdict).toBe(
      "dropped: only inside a longer name"
    );
    expect(verdict("skills/legion-worker/SKILL.md")?.verdict).toBe(
      "inside the skill skills/legion-worker"
    );
    expect(verdict("README.md")?.verdict).toBe(
      "not in the tree at 63b3a613feb15cb191f87f4239467cdd37b62c82"
    );
    const loaders = r.table("loaders.tsv").filter((row) => row.skill === "legion-worker");
    expect(loaders.map((row) => row.path)).toEqual(["scripts/pi-plugin-prepack.sh"]);
  });

  test("a target the configuration does not name is reported, and its rows are left out of the table", async () => {
    const other = await run(world(), [], { targets: ["cybermonklabs/legion"] });
    const md = other.text("skill-copies.md");
    expect(md).toContain(
      "rows point at repositories the configuration's targets do not name, so they are not in the table: acme/skills."
    );
    expect(md).toContain("- `**/.omp/agent/skills/beta/` -> acme/skills skills/beta");
    expect(
      other.table("prefix-table.tsv").every((row) => row.repository === "cybermonklabs/legion")
    ).toBe(true);
  });
});

describe("a truncated tree", () => {
  /** The F6 tree served the way GitHub serves a truncated one: the root's recursive read and the
   *  skills/ directory's are truncated, so both are read one level down. */
  function truncatedWorld(): World {
    const w = world();
    const { entries } = legionTree();
    const dirs = new Map<string, string>();
    for (const entry of entries) {
      const parts = entry.path.split("/");
      for (let depth = 1; depth < parts.length; depth++) {
        const dir = parts.slice(0, depth).join("/");
        if (!dirs.has(dir)) dirs.set(dir, sha1(`tree legion ${dir}`));
      }
      if (entry.type === "tree") dirs.set(entry.path, entry.sha);
    }
    const all: TreeEntry[] = [
      ...entries.filter((entry) => entry.type === "blob"),
      ...[...dirs].map(([dir, sha]) => ({ path: dir, mode: "040000", type: "tree", sha })),
    ];
    const relative = (dir: string, deep: boolean) =>
      all
        .filter((entry) => (dir === "" ? true : entry.path.startsWith(`${dir}/`)))
        .map((entry) => ({
          ...entry,
          path: dir === "" ? entry.path : entry.path.slice(dir.length + 1),
        }))
        .filter((entry) => deep || !entry.path.includes("/"));
    w.trees.set(`cybermonklabs/legion ${LEGION_ROOT} recursive`, {
      sha: LEGION_ROOT,
      tree: relative("", true).slice(0, 10),
      truncated: true,
    });
    w.trees.set(`cybermonklabs/legion ${LEGION_ROOT}`, {
      sha: LEGION_ROOT,
      tree: relative("", false),
      truncated: false,
    });
    for (const [dir, sha] of dirs) {
      const truncated = dir === "skills";
      w.trees.set(`cybermonklabs/legion ${sha} recursive`, {
        sha,
        tree: truncated ? [] : relative(dir, true),
        truncated,
      });
      w.trees.set(`cybermonklabs/legion ${sha}`, {
        sha,
        tree: relative(dir, false),
        truncated: false,
      });
    }
    return w;
  }

  test("it is read by directory into the same tree and the same copies", async () => {
    const whole = await run(world(), ["--only", "cybermonklabs/legion"]);
    const w = truncatedWorld();
    const byDirectory = await run(w, ["--only", "cybermonklabs/legion"]);
    const legion = byDirectory.table("repositories.tsv")[0];
    expect(legion?.tree_read).toMatch(/^by directory, \d+ reads$/);
    expect(legion?.error).toBe("");
    expect(
      w.calls.some((call) => call[1] === `repos/cybermonklabs/legion/git/trees/${LEGION_ROOT}`)
    ).toBe(true);
    expect(byDirectory.text("skill-copies.tsv")).toBe(whole.text("skill-copies.tsv"));
    expect(byDirectory.text("manifests.tsv")).toBe(whole.text("manifests.tsv"));
  });
});

describe("code search pacing and the call budget", () => {
  const w = world();
  const resetIn = (seconds: number) => String(Math.floor(w.clock / 1000) + seconds);
  w.search = (_q, _page, call) => {
    if (call === 1) {
      return {
        status: 403,
        stderr: "gh: API rate limit exceeded for installation. (HTTP 403)\n",
        headers: { "X-Ratelimit-Remaining": "0", "X-Ratelimit-Reset": resetIn(30) },
        body: { message: "API rate limit exceeded for installation." },
      };
    }
    if (call === 2)
      return {
        headers: { "X-Ratelimit-Remaining": "0", "X-Ratelimit-Reset": resetIn(50) },
        body: EMPTY_SEARCH,
      };
    return { body: EMPTY_SEARCH };
  };
  let r: Run;
  beforeAll(async () => {
    r = await run(w, ["--only", "cybermonklabs/legion", "--max-search-calls", "4"]);
  });

  test("a refused call waits for the reset and runs again; a spent limit waits before the next call", () => {
    expect(r.code).toBe(0);
    expect(w.sleeps).toEqual([31_000, 51_000]);
    const first = r.queries()[0];
    expect(first).toMatchObject({ status: "ok", rate_limit_retries: 1, calls: 2 });
  });

  test("the budget stops the searches, and each query it leaves out is kept as not run", () => {
    const searches = w.calls.filter((call) => call.includes("search/code"));
    expect(searches.length).toBe(4);
    const queries = r.queries().filter((query) => query.type === "search");
    expect(queries.filter((query) => query.status === "ok").length).toBe(3);
    const left = queries.filter((query) => query.status === "not run");
    expect(left.length).toBe(queries.length - 3);
    expect(left.every((query) => query.error === "the search-call budget (4) is spent")).toBe(true);
    expect(r.text("skill-copies.md")).toContain("4 code search calls (budget 4)");
  });
});

describe("the owners' searches at once, each paced by its own limit", () => {
  // Each owner's first search is held until both owners have one in flight: searching the owners
  // one after another would never release it, and the run would not end.
  const w = world();
  w.holdFirstSearches = 2;
  let r: Run;
  beforeAll(async () => {
    r = await run(w);
  });
  const callsOf = (owner: string) => w.searchLog.filter((call) => call.owner === owner);

  test("both owners' searches are in flight at once, and each owner's one at a time", () => {
    expect(r.code).toBe(0);
    expect(Math.max(...w.searchLog.map((call) => call.inFlight))).toBe(2);
    expect(w.searchLog.every((call) => call.ownerInFlight === 1)).toBe(true);
    expect(new Set(w.searchLog.map((call) => call.owner))).toEqual(
      new Set(["cybermonklabs", "acme"])
    );
  });

  test("no owner starts more than its limit of 10 searches in any minute", () => {
    for (const owner of ["cybermonklabs", "acme"]) {
      const starts = callsOf(owner).map((call) => call.at);
      expect(starts.length).toBeGreaterThan(20);
      for (let index = 10; index < starts.length; index++) {
        expect((starts[index] ?? 0) - (starts[index - 10] ?? 0)).toBeGreaterThanOrEqual(60_000);
      }
    }
    expect(
      r.errors.some((line) =>
        /^copies: code search of acme waits \d+ s for its rate limit$/.test(line)
      )
    ).toBe(true);
  });

  test("each owner's minutes overlap the other's: the searches take the busier owner's time, not the sum", () => {
    const minutes = (owner: string) => Math.ceil(callsOf(owner).length / 10) - 1;
    const first = Math.min(...w.searchLog.map((call) => call.at));
    const last = Math.max(...w.searchLog.map((call) => call.at));
    expect(last - first).toBe(Math.max(minutes("cybermonklabs"), minutes("acme")) * 60_000);
    expect(last - first).toBeLessThan((minutes("cybermonklabs") + minutes("acme")) * 60_000);
  });
});

describe("the pure parts", () => {
  test.each([
    [
      "plain",
      "name: alpha\ndescription: Use when alpha is needed.",
      "alpha",
      "Use when alpha is needed.",
    ],
    [
      "double-quoted, escapes",
      'name: "alpha"\ndescription: "A \\"quoted\\" line\\nand a second"',
      "alpha",
      'A "quoted" line\nand a second',
    ],
    ["single-quoted", "name: 'alpha'\ndescription: 'It''s alpha.'", "alpha", "It's alpha."],
    [
      "folded",
      "name: alpha\ndescription: >\n  One line\n  folded.\n\n  A new paragraph.",
      "alpha",
      "One line folded.\nA new paragraph.\n",
    ],
    [
      "folded, stripped",
      "name: alpha\ndescription: >-\n  One line\n  folded.",
      "alpha",
      "One line folded.",
    ],
    [
      "literal",
      "name: alpha\ndescription: |\n  Line one.\n  Line two.",
      "alpha",
      "Line one.\nLine two.\n",
    ],
    [
      "not valid YAML, read leniently",
      "name: alpha\ndescription: Use when: the alpha list\n  is needed.",
      "alpha",
      "Use when: the alpha list is needed.",
    ],
    [
      "no name: the directory's",
      "description: Only a description.",
      "dir-name",
      "Only a description.",
    ],
  ])("frontmatter %s", (_shape, frontmatter, name, description) => {
    const fields = skillFields(
      "skills/dir-name",
      "acme/skills",
      `---\n${frontmatter}\n---\n# Body\n`
    );
    expect(fields.name).toBe(name);
    expect(fields.description).toBe(description);
    expect(fields.descriptionBytes).toBe(Buffer.byteLength(description));
  });

  test("a SKILL.md with no frontmatter takes its directory's name", () => {
    expect(skillFields("skills/gamma", "acme/skills", "# Gamma\n")).toMatchObject({
      name: "gamma",
      nameFrom: "directory",
      fault: "no frontmatter",
    });
  });

  test("lenient fields read quoted, folded, literal and list values of a block YAML refuses", () => {
    const block = [
      "name: alpha: one",
      'description: "over',
      '  two lines"',
      "single: 'it''s'",
      "folded: >",
      "  a",
      "  b",
      "literal: |-",
      "  x",
      "  y",
      "flow: [a, 'b', \"c\"]",
      "block:",
      "  - d",
      "  - e",
    ].join("\n");
    expect(lenientFields(block)).toEqual({
      name: "alpha: one",
      description: "over two lines",
      single: "it's",
      folded: "a b\n",
      literal: "x\ny",
      flow: ["a", "b", "c"],
      block: ["d", "e"],
    });
  });

  test.each([
    ["Read skill://alpha now.", ["skill-uri"]],
    ["Run /skill:alpha.", ["slash-skill"]],
    ["See skills/alpha/SKILL.md.", ["skills-path"]],
    ["Load acme-tools:alpha.", ["plugin-qualified"]],
    ['const name = "alpha";', ["quoted-string"]],
    ["The alpha skill.", ["name"]],
    ["The tool-loop-alpha skill, alpha-two and alpha_x.", []],
  ])("mentions in %j", (text, kinds) => {
    expect(mentions(text, "alpha", ["acme-tools"], "notes.md")).toEqual(kinds as never);
  });

  test("a quoted name on a skills line of a code file is a load", () => {
    expect(
      mentions('return join(skillsDirectory, "alpha", "SKILL.md")', "alpha", [], "src/load.ts")
    ).toEqual(["quoted-string", "code-load"]);
    expect(mentions('skills = ["alpha", "beta"]', "alpha", [], "tree/init.py")).toEqual([
      "quoted-string",
      "code-load",
    ]);
    expect(mentions('skills = ["alpha", "beta"]', "alpha", [], "docs/notes.md")).toEqual([
      "quoted-string",
    ]);
  });

  test("a name is searched as its quoted words, with no quoted word holding a colon", () => {
    expect(searchTerms("alpha")).toBe('"alpha"');
    expect(searchTerms("tool-loop-alpha")).toBe('"tool-loop-alpha"');
    expect(searchTerms('"a:b"')).toBe('"a" "b"');
  });

  test.each([
    [
      "/opt/legion/pi-legion/dist/skills/alpha/",
      "/opt/legion/pi-legion/dist/skills/alpha/SKILL.md",
      true,
    ],
    ["**/.omp/agent/skills/alpha/", "/home/someone/.omp/agent/skills/alpha/references/x.md", true],
    ["**/.omp/agent/skills/alpha/", "/home/someone/.omp/agent/skills/alpha-two/SKILL.md", false],
    [
      "**/plugins/cache/plugins/acme-market___acme-tools___*/skills/alpha/",
      "/h/.omp/plugins/cache/plugins/acme-market___acme-tools___0.3.0/skills/alpha/SKILL.md",
      true,
    ],
    [
      "**/plugins/cache/acme-market/acme-tools/*/skills/alpha/",
      "/c/plugins/cache/acme-market/acme-tools/0.1.0/skills/alpha/SKILL.md",
      true,
    ],
    [
      "**/plugins/cache/acme-market/acme-tools/*/skills/alpha/",
      "/c/plugins/cache/acme-market/acme-tools/0.1/extra/skills/alpha/SKILL.md",
      false,
    ],
    [
      "/opt/legion/pi-legion/dist/skills/alpha/",
      "/srv/opt/legion/pi-legion/dist/skills/alpha/SKILL.md",
      false,
    ],
  ])("the prefix %s against %s", (prefix, copyPath, matches) => {
    expect(prefixMatches(prefix, copyPath)).toBe(matches);
  });

  test("a TSV cell keeps a tab, a line break and a backslash", () => {
    const text = tsv(["a", "b"], [["one\ttwo", "x\\y\nz"]]);
    expect(text.split("\n").length).toBe(3);
    expect(parseTsv(text)).toEqual([{ a: "one\ttwo", b: "x\\y\nz" }]);
  });

  test("a manifest of the wrong shape is refused naming the field", () => {
    expect(() =>
      parseManifest(
        "claude-marketplace",
        ".claude-plugin/marketplace.json",
        '{"name": "m", "plugins": [{"source": "./p"}]}'
      )
    ).toThrow("plugins.0.name");
  });

  test("main refuses a bad command line", async () => {
    const errors: string[] = [];
    const io = {
      gh: new World().gh,
      sleep: async () => {},
      now: () => 0,
      out: () => {},
      err: (line: string) => errors.push(line),
    };
    expect(await main(["--config", "x.json", "--at", "yesterday", "--out", "o"], io)).toBe(2);
    expect(
      await main(["--config", "x.json", "--at", AT, "--out", "o", "--only", "no-slash"], io)
    ).toBe(2);
    expect(await main(["--at", AT, "--out", "o"], io)).toBe(2);
    expect(errors).toEqual([
      'copies: --at "yesterday" is not an ISO instant ending in Z',
      'copies: --only "no-slash" is not owner/name',
      expect.stringContaining("copies: --config, --at and --out are required."),
    ]);
  });
});
