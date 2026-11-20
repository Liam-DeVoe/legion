/**
 * The case context of a capture (plan, "The case context"): what one case's arm installs before
 * Oh My Pi builds its request, rebuilt from the case's `profile`, since a transcript keeps no
 * system prompt. `buildContext` writes it under one scratch directory, the same path for every arm
 * of a case, so the paths Oh My Pi puts in its prompt are the same in every arm:
 *
 * - `skills/`: the arm's installed skills (`arm.profileSkills`), one directory each under its
 *   name. The case's skill comes from the arm's own directory; every other skill is its
 *   repository's directory at its commit, read through `source` and kept under the cache
 *   (`skills/<key of repo, commit, path>`).
 * - `plugins/<name>/`: each plugin of `profile.plugins` that is an Oh My Pi extension (its
 *   directory holds a `package.json` with an `omp` field), built as the worker image builds the
 *   Legion plugins: the repository at the commit (`trees/<key of repo, commit>`), its packages
 *   installed from its own lockfile (`bun install --frozen-lockfile --ignore-scripts`, only the
 *   plugin's workspace when the repository is a workspace), `bun run build` when the plugin has a
 *   build script, then the files its `files` list ships with each `omp.extensions` entry pointed at
 *   its bundle in `dist/` (`plugins/<key of repo, commit, path>`). Each skills path the plugin's
 *   manifest names (`omp.skills`, `dist/skills` for the Legion plugins, where the Envoy plugin
 *   also looks for its skills at load) is a link to `skills/`, so a plugin lists the arm's skills
 *   and no copy of its own: only the case's skill differs between arms. A plugin with no
 *   `package.json` of that kind installs skills alone (a verification-skills plugin), which the
 *   profile's skills already hold. A plugin whose build fails is left out, with its reason.
 * - `work/`: the session's working directory. Each context file named `AGENTS.md` or `CLAUDE.md`
 *   (the session repository's, at its default branch's commit at the session's start) is written
 *   there at its path, where Oh My Pi reads context files.
 * - `system-prompt.md`, the one `--append-system-prompt` file, as the daemon writes it
 *   (packages/daemon/internal/runtime/systemprompt.go, `ComposePrompt`): for a Legion context
 *   (`legion:<project>:<role>`, the role `architect-root` for a tree's root architect), the role
 *   prompt, then every other context file (the project's instructions file, and `shared.md` when
 *   the project's prompt includes it), each with its trailing newlines dropped and joined by a
 *   blank line. The role prompt is `profile.appendSystemPrompt` when the case holds it, else the
 *   parts `prompts.Composer.Compose(role, isRoot)` (packages/daemon/internal/prompts/prompts.go)
 *   names at the commit of the plugin repository that holds that file: Compose answers with paths
 *   of the parts it embeds, so it is run once per distinct `prompts.go` blob and role, by a small
 *   Go test written into the cached copy of the repository (never the workspace) and removed after
 *   (`composes/<blob>-<role>`), and each part is then read at the session's commit. For the
 *   controller (`legion:<project>:controller`) the parts are those `ControllerPromptPaths(true)`
 *   names: a Legion store's controller runs headless in a pod. The daemon's addressing sentence,
 *   which sits between the two, names the session's claim, which a case does not hold, so it is
 *   left out and the record says so. A subagent's context (`subagent:<agent>`) has no role prompt:
 *   its case carries the system prompt its session recorded, which the capture sends whole.
 *
 * `source` reads the repositories: `ghSource` with read-only `gh api` calls (a directory from the
 * commit's tree and its blobs, a whole repository from its tarball), `localSource` from a
 * directory per repository and commit (the tests' made-up repositories). Repository and owner
 * names come only from the case. Every cache entry is `<cacheDir>/<kind>s/<key>` (`skills`,
 * `trees`, `plugins`, `composes`, and `files` for each single file read), made into a fresh path
 * and renamed into place. `record` says what was used: each commit and cache key, each plugin
 * left out, and each build with its time and whether the cache already held it.
 */
import { createHash, randomBytes } from "node:crypto";
import {
  chmodSync,
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  renameSync,
  rmSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import path from "node:path";
import { messageFor } from "@legion/envoy-client/errors";
import type { Arm } from "./arms";
import type { Case } from "./cases";

/** Where the Legion daemon's role prompt composer is, in the repository that holds it. */
export const PROMPTS_GO = "packages/daemon/internal/prompts/prompts.go";

/** The repositories a context is read from. */
export interface RepoSource {
  /** What the record calls this source. */
  readonly name: string;
  /** The bytes of `file` of `repo` (`owner/name`) at `commit`. Throws when it holds none. */
  file(repo: string, commit: string, file: string): Promise<Uint8Array>;
  /** Writes each file of `repo` at `commit` under `dir` (a path inside the repository, or "" for
   *  all of it) into `dest`, at its path below `dir`. Throws when `dir` holds no file. */
  tree(repo: string, commit: string, dir: string, dest: string): Promise<void>;
}

/** One build the context needed: what it is, its cache key, whether the cache held it already,
 *  and how long it took, in milliseconds. */
export interface Build {
  readonly kind: "skill" | "tree" | "plugin" | "compose" | "file";
  readonly key: string;
  readonly hit: boolean;
  readonly ms: number;
}

/** What a capture's context used. */
export interface ContextRecord {
  readonly source: string;
  readonly skills: readonly {
    readonly name: string;
    /** The arm's own directory, or the repository directory and its cache key. */
    readonly from:
      | "arm"
      | { readonly repo: string; readonly commit: string; readonly path: string; key: string };
  }[];
  readonly plugins: readonly {
    readonly name: string;
    readonly repo: string;
    readonly commit: string;
    readonly path: string;
    /** `extension`: built and loaded with `--extension`; `skills only`: nothing to load beyond
     *  the profile's skills; `left out`: its build failed (`reason`). */
    readonly as: "extension" | "skills only" | "left out";
    readonly key?: string;
    readonly reason?: string;
  }[];
  readonly contextFiles: readonly {
    readonly repo: string;
    readonly commit: string;
    readonly path: string;
    readonly key: string;
    readonly as: "working directory" | "system prompt";
  }[];
  /** The role prompt: the case's own text, or the parts Compose named, or none. */
  readonly rolePrompt:
    | "case"
    | {
        readonly repo: string;
        readonly commit: string;
        readonly role: string;
        readonly isRoot: boolean;
        readonly promptsBlob: string;
        readonly key: string;
        readonly parts: readonly string[];
      }
    | null;
  /** What of the real session's context the capture does not reproduce. */
  readonly leftOut: readonly string[];
  readonly builds: readonly Build[];
}

/** A skill the context installs: its name, its directory, its frontmatter `description`, and
 *  whether Oh My Pi leaves it out of the system prompt's listing (`hidden`). */
export interface InstalledSkill {
  readonly name: string;
  readonly dir: string;
  readonly description: string;
  readonly hidden: boolean;
}

/** The context of one capture, written under `root`. */
export interface CaseContext {
  readonly skillsDir: string;
  readonly skills: readonly InstalledSkill[];
  /** Each built plugin's directory, as `--extension` takes it. */
  readonly extensions: readonly string[];
  readonly workspace: string;
  /** The `--append-system-prompt` file, when the context has one. */
  readonly appendSystemPrompt: string | undefined;
  readonly record: ContextRecord;
}

export interface ContextOptions {
  readonly source: RepoSource;
  /** The cache every build is kept in. */
  readonly cacheDir: string;
  /** The case's scratch directory: the same path for every arm of the case. */
  readonly root: string;
}

/** What a context reads of a case: its id, its context and its profile. */
export type ContextCase = Pick<Case, "id" | "context" | "profile">;

/** The hex SHA-256 of `parts`, each ended by a NUL, cut to 32 characters: a cache key. */
export function cacheKey(...parts: string[]): string {
  const hash = createHash("sha256");
  for (const part of parts) hash.update(part).update("\0");
  return hash.digest("hex").slice(0, 32);
}

/** The git blob id of `bytes`, as `git hash-object` gives it. */
export function gitBlobId(bytes: Uint8Array): string {
  return createHash("sha1").update(`blob ${bytes.length}\0`).update(bytes).digest("hex");
}

/** The frontmatter of the skill in `dir`, as Oh My Pi reads it for the listing: its `description`
 *  ("" when it has none), and whether it is hidden from the listing, which Oh My Pi does for
 *  `hide: true` or `disable-model-invocation: true` (extensibility/skills.ts at the pinned tag). */
export function skillFrontmatter(dir: string): { description: string; hidden: boolean } {
  const text = readFileSync(path.join(dir, "SKILL.md"), "utf8");
  const frontmatter = /^---\r?\n([\s\S]*?)\r?\n---(\r?\n|$)/.exec(text)?.[1];
  const parsed: unknown = frontmatter === undefined ? undefined : Bun.YAML.parse(frontmatter);
  const field = (name: string): unknown =>
    typeof parsed === "object" && parsed !== null ? Reflect.get(parsed, name) : undefined;
  const description = field("description");
  return {
    description: typeof description === "string" ? description : "",
    hidden:
      field("hide") === true ||
      field("disable-model-invocation") === true ||
      field("disableModelInvocation") === true,
  };
}

/** What a finished command gave. */
interface Ran {
  readonly code: number;
  readonly stdout: Uint8Array;
  readonly stderr: string;
}

/** Runs `argv` to its end. */
async function run(
  argv: readonly string[],
  options: { readonly cwd?: string; readonly env?: Record<string, string | undefined> } = {}
): Promise<Ran> {
  const child = Bun.spawn([...argv], {
    cwd: options.cwd,
    env: options.env ?? process.env,
    stdin: "ignore",
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).arrayBuffer(),
    new Response(child.stderr).text(),
    child.exited,
  ]);
  return { code, stdout: new Uint8Array(stdout), stderr };
}

/** Runs `argv` and throws, naming it and its last error lines, when it fails. */
async function mustRun(
  argv: readonly string[],
  options: { readonly cwd?: string; readonly env?: Record<string, string | undefined> } = {}
): Promise<Ran> {
  const ran = await run(argv, options);
  if (ran.code !== 0) {
    const tail = ran.stderr.trim().split("\n").slice(-12).join("\n");
    throw new Error(`\`${argv.join(" ")}\` failed with exit ${ran.code}: ${tail}`);
  }
  return ran;
}

/** One entry of a git tree, as `git/trees?recursive=1` lists it. */
interface TreeEntry {
  readonly path: string;
  readonly mode: string;
  readonly type: string;
  readonly sha: string;
}

/** Writes a blob of `mode` (git's: 100755 executable, 120000 a link) at `file`. */
function writeBlob(file: string, mode: string, bytes: Uint8Array): void {
  mkdirSync(path.dirname(file), { recursive: true });
  if (mode === "120000") {
    symlinkSync(new TextDecoder().decode(bytes), file);
    return;
  }
  writeFileSync(file, bytes);
  if (mode === "100755") chmodSync(file, 0o755);
}

/** Runs `work` on each item, at most `limit` at once. */
async function eachLimited<T>(
  items: readonly T[],
  limit: number,
  work: (item: T) => Promise<void>
): Promise<void> {
  let next = 0;
  const lane = async (): Promise<void> => {
    while (next < items.length) {
      const item = items[next] as T;
      next += 1;
      await work(item);
    }
  };
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, lane));
}

/**
 * The repositories as GitHub serves them, read with `gh api` GET calls only: a file from its
 * contents, a directory from the commit's recursive tree and each blob, the whole repository from
 * its tarball. A tree GitHub cuts short (`truncated`) is read from the tarball instead.
 */
export function ghSource(gh = "gh"): RepoSource {
  const trees = new Map<string, Promise<{ entries: TreeEntry[]; truncated: boolean }>>();
  const api = async (route: string, raw: boolean): Promise<Uint8Array> => {
    const accept = raw ? ["-H", "Accept: application/vnd.github.raw"] : [];
    return (await mustRun([gh, "api", "--method", "GET", ...accept, route])).stdout;
  };
  const tarball = async (repo: string, commit: string, dest: string): Promise<void> => {
    mkdirSync(dest, { recursive: true });
    const archive = `${dest}.tar.gz`;
    try {
      const child = Bun.spawn([gh, "api", "--method", "GET", `repos/${repo}/tarball/${commit}`], {
        stdin: "ignore",
        stdout: Bun.file(archive),
        stderr: "pipe",
      });
      const [stderr, code] = await Promise.all([new Response(child.stderr).text(), child.exited]);
      if (code !== 0) {
        throw new Error(`gh api repos/${repo}/tarball/${commit} failed: ${stderr.trim()}`);
      }
      await mustRun(["tar", "-xzf", archive, "-C", dest, "--strip-components=1"]);
    } finally {
      rmSync(archive, { force: true });
    }
  };
  return {
    name: "gh",
    file: (repo, commit, file) => {
      const route = file.split("/").map(encodeURIComponent).join("/");
      return api(`repos/${repo}/contents/${route}?ref=${encodeURIComponent(commit)}`, true);
    },
    async tree(repo, commit, dir, dest) {
      if (dir === "") return tarball(repo, commit, dest);
      const id = `${repo}@${commit}`;
      let listing = trees.get(id);
      if (listing === undefined) {
        listing = api(`repos/${repo}/git/trees/${commit}?recursive=1`, false).then((bytes) => {
          const parsed = JSON.parse(new TextDecoder().decode(bytes)) as {
            tree: TreeEntry[];
            truncated: boolean;
          };
          return { entries: parsed.tree, truncated: parsed.truncated };
        });
        trees.set(id, listing);
      }
      const { entries, truncated } = await listing;
      const prefix = `${dir.replace(/\/+$/, "")}/`;
      if (truncated) {
        const whole = mkdtempSync(`${dest}.whole-`);
        try {
          await tarball(repo, commit, whole);
          if (!existsSync(path.join(whole, prefix))) {
            throw new Error(`${repo} at ${commit} holds no directory ${dir}`);
          }
          cpSync(path.join(whole, prefix), dest, { recursive: true, verbatimSymlinks: true });
        } finally {
          rmSync(whole, { recursive: true, force: true });
        }
        return;
      }
      const blobs = entries.filter(
        (entry) => entry.type === "blob" && entry.path.startsWith(prefix)
      );
      if (blobs.length === 0) throw new Error(`${repo} at ${commit} holds no file under ${dir}`);
      await eachLimited(blobs, 4, async (entry) => {
        const bytes = await api(`repos/${repo}/git/blobs/${entry.sha}`, true);
        writeBlob(path.join(dest, entry.path.slice(prefix.length)), entry.mode, bytes);
      });
    },
  };
}

/** Repositories as directories: `root/<owner>/<name>/<commit>/` holds the repository at that
 *  commit. */
export function localSource(root: string): RepoSource {
  const at = (repo: string, commit: string) => path.join(root, repo, commit);
  return {
    name: `local directory ${root}`,
    async file(repo, commit, file) {
      const where = path.join(at(repo, commit), file);
      if (!existsSync(where)) throw new Error(`${repo} at ${commit} holds no file ${file}`);
      return new Uint8Array(readFileSync(where));
    },
    async tree(repo, commit, dir, dest) {
      const where = path.join(at(repo, commit), dir);
      if (!existsSync(where)) throw new Error(`${repo} at ${commit} holds no directory ${dir}`);
      cpSync(where, dest, { recursive: true, verbatimSymlinks: true });
    },
  };
}

/** The builds of one context, each timed and kept in the cache. */
class Builds {
  readonly list: Build[] = [];
  constructor(readonly cacheDir: string) {}

  /** `cacheDir/<kind>s/<key>`, made by `make` into a fresh path that is then renamed into place,
   *  unless the cache holds it already. */
  async entry(
    kind: Build["kind"],
    key: string,
    make: (dest: string) => Promise<void>
  ): Promise<string> {
    const started = performance.now();
    const where = path.join(this.cacheDir, `${kind}s`, key);
    let hit = existsSync(where);
    if (!hit) {
      mkdirSync(path.dirname(where), { recursive: true });
      const fresh = `${where}.part-${randomBytes(4).toString("hex")}`;
      try {
        await make(fresh);
        renameSync(fresh, where);
      } catch (error) {
        rmSync(fresh, { recursive: true, force: true });
        // Another capture made the same entry first: it is the same build.
        if (!existsSync(where)) throw error;
        hit = true;
      }
    }
    this.list.push({ kind, key, hit, ms: Math.round(performance.now() - started) });
    return where;
  }
}

/** The text of `file` of `repo` at `commit`, through the cache. */
async function fileText(
  builds: Builds,
  source: RepoSource,
  repo: string,
  commit: string,
  file: string
): Promise<{ text: string; key: string }> {
  const key = cacheKey(repo, commit, file);
  const where = await builds.entry("file", key, async (dest) => {
    writeFileSync(dest, await source.file(repo, commit, file));
  });
  return { text: readFileSync(where, "utf8"), key };
}

/** The whole of `repo` at `commit`, through the cache. */
function checkout(builds: Builds, source: RepoSource, repo: string, commit: string) {
  return builds.entry("tree", cacheKey(repo, commit), (dest) =>
    source.tree(repo, commit, "", dest)
  );
}

/** A plugin's `package.json`, as far as the build reads it. */
interface Manifest {
  files?: unknown;
  scripts?: { build?: unknown };
  workspaces?: unknown;
  omp?: { extensions?: unknown; skills?: unknown };
  [field: string]: unknown;
}

/** The strings of `value` when it is a list of them, else none. */
function strings(value: unknown): string[] {
  return Array.isArray(value)
    ? value.filter((item): item is string => typeof item === "string")
    : [];
}

/** A path a manifest names, without its leading `./`. */
function manifestPath(entry: string): string {
  return path.normalize(entry).replace(/^\.\//, "");
}

/** The plugin `path` of the repository checked out at `tree`, built into `dest` (see the file's
 *  header). */
async function buildPlugin(tree: string, pluginPath: string, dest: string): Promise<void> {
  const dir = path.join(tree, pluginPath);
  const manifest = JSON.parse(readFileSync(path.join(dir, "package.json"), "utf8")) as Manifest;
  const lockfile = (at: string) =>
    existsSync(path.join(at, "bun.lock")) || existsSync(path.join(at, "bun.lockb"));
  const install = ["bun", "install", "--frozen-lockfile", "--ignore-scripts"];
  const rootFile = path.join(tree, "package.json");
  const root: Manifest = existsSync(rootFile) ? JSON.parse(readFileSync(rootFile, "utf8")) : {};
  if (pluginPath !== "" && root.workspaces !== undefined && lockfile(tree)) {
    await mustRun([...install, "--filter", `./${pluginPath}`], { cwd: tree });
  } else if (lockfile(dir)) {
    await mustRun(install, { cwd: dir });
  }
  if (typeof manifest.scripts?.build === "string") {
    await mustRun(["bun", "run", "build"], { cwd: dir });
  }
  const files = strings(manifest.files);
  if (files.length === 0) {
    throw new Error("its package.json has no `files` list, so what the plugin ships is not known");
  }
  const skills = strings(manifest.omp?.skills).map((entry) => path.join(dir, manifestPath(entry)));
  mkdirSync(dest, { recursive: true });
  for (const entry of files) {
    const from = path.join(dir, manifestPath(entry));
    if (!existsSync(from)) continue;
    cpSync(from, path.join(dest, manifestPath(entry)), {
      recursive: true,
      verbatimSymlinks: true,
      filter: (source) =>
        !skills.some((skill) => source === skill || source.startsWith(`${skill}/`)),
    });
  }
  const extensions = strings(manifest.omp?.extensions).map((entry) => {
    const bundle = path.join("dist", path.basename(entry).replace(/\.[cm]?tsx?$/, ".js"));
    if (/\.[cm]?tsx?$/.test(entry) && existsSync(path.join(dest, bundle))) return bundle;
    if (existsSync(path.join(dest, manifestPath(entry)))) return manifestPath(entry);
    throw new Error(`its extension ${entry} has no built file among the files it ships`);
  });
  writeFileSync(
    path.join(dest, "package.json"),
    `${JSON.stringify({ ...manifest, omp: { ...manifest.omp, extensions } }, null, 2)}\n`
  );
}

/** The role of a Legion context (`legion:<project>:<role>`), as Compose takes it, or undefined
 *  for a context that is not Legion's. */
export function legionRole(
  context: string
): { readonly role: string; readonly isRoot: boolean } | undefined {
  const match = /^legion:[^:\s]+:([^:\s]+)$/.exec(context);
  if (match?.[1] === undefined) return undefined;
  return match[1] === "architect-root"
    ? { role: "architect", isRoot: true }
    : { role: match[1], isRoot: false };
}

/** The Go test that asks the composer of `module` for a role's parts: it writes the paths Compose
 *  answers, relative to the state directory it gave `New`, as a JSON list. For the controller it
 *  asks `ControllerPromptPaths(true)`, the parts of a controller the daemon runs headless in a pod,
 *  as it runs every controller of a Legion store's sessions. */
function composeTest(module: string, controller: boolean): string {
  const call = controller
    ? "paths, err := composer.ControllerPromptPaths(true)"
    : `parts, err := composer.Compose(claim.Role(os.Getenv("SKILL_CUTS_ROLE")), os.Getenv("SKILL_CUTS_ROOT") == "1")
\tpaths := parts.RolePromptPaths`;
  return `package zzskillcuts

import (
\t"encoding/json"
\t"os"
\t"path/filepath"
\t"testing"
${controller ? "" : `\n\t"${module}/internal/claim"`}
\t"${module}/internal/prompts"
)

func TestSkillCutsCompose(t *testing.T) {
\tstate := os.Getenv("SKILL_CUTS_STATE")
\tcomposer, err := prompts.New(state)
\tif err != nil {
\t\tt.Fatal(err)
\t}
\t${call}
\tif err != nil {
\t\tt.Fatal(err)
\t}
\trelative := make([]string, 0, len(paths))
\tfor _, part := range paths {
\t\tname, err := filepath.Rel(state, part)
\t\tif err != nil {
\t\t\tt.Fatal(err)
\t\t}
\t\trelative = append(relative, filepath.ToSlash(name))
\t}
\tout, err := json.Marshal(relative)
\tif err != nil {
\t\tt.Fatal(err)
\t}
\tif err := os.WriteFile(os.Getenv("SKILL_CUTS_OUT"), out, 0o600); err != nil {
\t\tt.Fatal(err)
\t}
}
`;
}

/** The parts Compose names for `role` in the repository checked out at `tree`, as paths inside
 *  the repository, each checked to be the file `New` wrote for it. */
async function composeParts(
  tree: string,
  role: string,
  isRoot: boolean,
  scratch: string
): Promise<string[]> {
  const promptsDir = path.dirname(path.join(tree, PROMPTS_GO));
  let moduleDir = promptsDir;
  while (!existsSync(path.join(moduleDir, "go.mod"))) {
    if (moduleDir === tree) throw new Error(`no go.mod holds ${PROMPTS_GO}`);
    moduleDir = path.dirname(moduleDir);
  }
  const module = /^module\s+(\S+)/m.exec(readFileSync(path.join(moduleDir, "go.mod"), "utf8"))?.[1];
  if (module === undefined) throw new Error(`the go.mod above ${PROMPTS_GO} names no module`);
  const name = `zz_skill_cuts_${randomBytes(4).toString("hex")}`;
  const testDir = path.join(moduleDir, "internal", name);
  mkdirSync(scratch, { recursive: true });
  const state = mkdtempSync(path.join(scratch, "compose-state-"));
  const out = path.join(state, "parts.json");
  try {
    mkdirSync(testDir, { recursive: true });
    writeFileSync(
      path.join(testDir, "compose_test.go"),
      composeTest(module, role === "controller")
    );
    await mustRun(
      ["go", "test", "-count=1", "-run", "^TestSkillCutsCompose$", `./internal/${name}/`],
      {
        cwd: moduleDir,
        env: {
          ...process.env,
          SKILL_CUTS_STATE: state,
          SKILL_CUTS_ROLE: role,
          SKILL_CUTS_ROOT: isRoot ? "1" : "0",
          SKILL_CUTS_OUT: out,
        },
      }
    );
    const written = JSON.parse(readFileSync(out, "utf8")) as string[];
    // `New` snapshots the embedded `roles/` parts under prompts/shared and the `go/` parts under
    // prompts/go; each part named is checked against that file of the repository.
    return written.map((part) => {
      const [top, kind, ...rest] = part.split("/");
      const embedded = kind === "shared" ? "roles" : kind === "go" ? "go" : undefined;
      if (top !== "prompts" || embedded === undefined || rest.length === 0) {
        throw new Error(`Compose named ${part}, which is no part this reader knows`);
      }
      const inRepo = path.relative(tree, path.join(promptsDir, embedded, ...rest));
      const repoBytes = readFileSync(path.join(tree, inRepo));
      if (!repoBytes.equals(readFileSync(path.join(state, part)))) {
        throw new Error(`Compose's part ${part} is not the repository's ${inRepo}`);
      }
      return inRepo;
    });
  } finally {
    rmSync(testDir, { recursive: true, force: true });
    rmSync(state, { recursive: true, force: true });
  }
}

/** The role prompt of `c`'s Legion context and its record: the parts Compose names at the commit
 *  of the plugin repository that holds PROMPTS_GO, joined as `ComposePrompt` joins them. */
async function rolePrompt(
  c: ContextCase,
  role: { readonly role: string; readonly isRoot: boolean },
  builds: Builds,
  options: ContextOptions
): Promise<{ text: string; record: ContextRecord["rolePrompt"] }> {
  if (c.profile.appendSystemPrompt !== undefined) {
    return { text: c.profile.appendSystemPrompt, record: "case" };
  }
  const { source } = options;
  const repositories = [
    ...new Map(c.profile.plugins.map((p) => [`${p.repo}@${p.commit}`, p])).values(),
  ];
  for (const { repo, commit } of repositories) {
    let promptsGo: Uint8Array;
    try {
      promptsGo = await source.file(repo, commit, PROMPTS_GO);
    } catch {
      continue;
    }
    const promptsBlob = gitBlobId(promptsGo);
    const key = `${promptsBlob}-${role.role}${role.isRoot ? "-root" : ""}`;
    const where = await builds.entry("compose", key, async (dest) => {
      const tree = await checkout(builds, source, repo, commit);
      const parts = await composeParts(
        tree,
        role.role,
        role.isRoot,
        path.join(options.cacheDir, "tmp")
      );
      writeFileSync(dest, `${JSON.stringify(parts)}\n`);
    });
    const parts = JSON.parse(readFileSync(where, "utf8")) as string[];
    let text = "";
    for (const part of parts) text += (await fileText(builds, source, repo, commit, part)).text;
    return {
      text: text.replace(/\n+$/, ""),
      record: { repo, commit, role: role.role, isRoot: role.isRoot, promptsBlob, key, parts },
    };
  }
  throw new Error(
    `case ${c.id}: its Legion context needs a role prompt, and no plugin repository holds ${PROMPTS_GO}`
  );
}

/** Writes the context of `c`'s arm `arm` under `options.root`, replacing one written there. */
export async function buildContext(
  c: ContextCase,
  arm: Arm,
  options: ContextOptions
): Promise<CaseContext> {
  const { source, root } = options;
  const builds = new Builds(options.cacheDir);
  const skillsDir = path.join(root, "skills");
  const pluginsDir = path.join(root, "plugins");
  const workspace = path.join(root, "work");
  const promptFile = path.join(root, "system-prompt.md");
  for (const owned of [skillsDir, pluginsDir, workspace, promptFile]) {
    rmSync(owned, { recursive: true, force: true });
  }
  mkdirSync(skillsDir, { recursive: true });
  mkdirSync(workspace, { recursive: true });

  const skills: InstalledSkill[] = [];
  const skillRecords: ContextRecord["skills"][number][] = [];
  for (const skill of arm.profileSkills) {
    const dir = path.join(skillsDir, skill.name);
    if (existsSync(dir)) throw new Error(`case ${c.id}: the profile installs ${skill.name} twice`);
    if (skill.name === arm.skill) {
      if (arm.skillDir === undefined) throw new Error(`case ${c.id}: arm ${arm.name} has no skill`);
      cpSync(arm.skillDir, dir, { recursive: true, verbatimSymlinks: true });
      skillRecords.push({ name: skill.name, from: "arm" });
    } else {
      const key = cacheKey(skill.repo, skill.commit, skill.path);
      const cached = await builds.entry("skill", key, (dest) =>
        source.tree(skill.repo, skill.commit, skill.path, dest)
      );
      cpSync(cached, dir, { recursive: true, verbatimSymlinks: true });
      skillRecords.push({
        name: skill.name,
        from: { repo: skill.repo, commit: skill.commit, path: skill.path, key },
      });
    }
    skills.push({ name: skill.name, dir, ...skillFrontmatter(dir) });
  }

  const extensions: string[] = [];
  const pluginRecords: ContextRecord["plugins"][number][] = [];
  for (const plugin of c.profile.plugins) {
    const where = {
      name: plugin.name,
      repo: plugin.repo,
      commit: plugin.commit,
      path: plugin.path,
    };
    const key = cacheKey(plugin.repo, plugin.commit, plugin.path);
    try {
      const tree = await checkout(builds, source, plugin.repo, plugin.commit);
      const manifestFile = path.join(tree, plugin.path, "package.json");
      const manifest = existsSync(manifestFile)
        ? (JSON.parse(readFileSync(manifestFile, "utf8")) as Manifest)
        : undefined;
      if (manifest?.omp === undefined) {
        pluginRecords.push({ ...where, as: "skills only" });
        continue;
      }
      const built = await builds.entry("plugin", key, (dest) =>
        buildPlugin(tree, plugin.path, dest)
      );
      const dir = path.join(pluginsDir, plugin.name);
      if (existsSync(dir)) throw new Error(`a plugin named ${plugin.name} is installed already`);
      cpSync(built, dir, { recursive: true, verbatimSymlinks: true });
      for (const entry of strings(manifest.omp.skills)) {
        const link = path.join(dir, manifestPath(entry));
        rmSync(link, { recursive: true, force: true });
        mkdirSync(path.dirname(link), { recursive: true });
        symlinkSync(skillsDir, link, "dir");
      }
      extensions.push(dir);
      pluginRecords.push({ ...where, as: "extension", key });
    } catch (error) {
      pluginRecords.push({ ...where, as: "left out", key, reason: messageFor(error) });
    }
  }

  const prompts: string[] = [];
  const leftOut: string[] = [];
  let roleRecord: ContextRecord["rolePrompt"] = null;
  const role = legionRole(c.context);
  if (role !== undefined) {
    const composed = await rolePrompt(c, role, builds, options);
    prompts.push(composed.text);
    roleRecord = composed.record;
    leftOut.push(
      "the Legion addressing sentence, which names the session's claim and repository, is left out of the system prompt: a case does not name the claim"
    );
  } else if (c.profile.appendSystemPrompt !== undefined) {
    prompts.push(c.profile.appendSystemPrompt);
    roleRecord = "case";
  }
  const fileRecords: ContextRecord["contextFiles"][number][] = [];
  for (const file of c.profile.contextFiles) {
    const { text, key } = await fileText(builds, source, file.repo, file.commit, file.path);
    // Oh My Pi reads these context files from the working directory.
    if (["AGENTS.md", "CLAUDE.md"].includes(path.basename(file.path))) {
      const dest = path.join(workspace, file.path);
      mkdirSync(path.dirname(dest), { recursive: true });
      writeFileSync(dest, text);
      fileRecords.push({ ...file, key, as: "working directory" });
    } else {
      prompts.push(text.replace(/\n+$/, ""));
      fileRecords.push({ ...file, key, as: "system prompt" });
    }
  }
  if (prompts.length > 0) writeFileSync(promptFile, prompts.join("\n\n"));

  return {
    skillsDir,
    skills,
    extensions,
    workspace,
    appendSystemPrompt: prompts.length > 0 ? promptFile : undefined,
    record: {
      source: source.name,
      skills: skillRecords,
      plugins: pluginRecords,
      contextFiles: fileRecords,
      rolePrompt: roleRecord,
      leftOut,
      builds: builds.list,
    },
  };
}
