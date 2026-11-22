/**
 * The pins of the skill sets that `set-count` measures (README.md, "set-count"; the
 * configuration's `sets` and `dotfilesRepo`): the commit of each set's repository that the dotfiles
 * repository installs, the set's skills at that commit, and each dotfiles commit inside the window
 * at which a pin changed. set-count.ts reads them; nothing here reads a store or calls a model.
 *
 * A set is pinned in one of two ways at a dotfiles commit:
 *   - `omp/plugins/package.json`: a dependency `"<package>": "github:<owner>/<repo>#<ref>"` whose
 *     owner and repository are the set's, in any case, whatever its package key (`packagePins`).
 *     Oh My Pi installs it under `node_modules/<package>/`.
 *   - `installers/omp.sh`: a line `ensure_vendor <url> <dir> [<commit>]` whose url is
 *     `https://github.com/<owner>/<repo>` with or without `.git` (`vendorPins`). The installer
 *     clones it into `vendor/<dir>/`.
 * A set pinned by no line, or by more than one, fails the read. A pin's ref resolves to a commit
 * (`SetReads.resolve`): a 40-hex ref is the commit; a tag is read with `git/ref/tags/<tag>`, whose
 * object is the commit for a lightweight tag, or a tag object whose own object is the commit for
 * an annotated tag. Any other ref fails the read.
 *
 * A set's skills are the `skills/<name>/SKILL.md` blobs of its repository's tree at that commit,
 * by `<name>`. So the place the set's installer reads skills from must be `skills/`: a package's
 * `omp.skills`, else its `pi.skills`, else (no declaration, or no package.json) `skills/`, as Oh My
 * Pi reads a plugin package (`packageSkills`); and for a vendor checkout, the one
 * `skills-sources.json` pool `vendor/<dir>`, whose skills Oh My Pi reads at `<root>/skills`
 * (`vendorPool`). Any other place fails the read, since the tree's `skills/` would not be what the
 * set installs. The set's upstreams are its repository's fork parent and source, which a copy
 * that is a URL may name.
 *
 * The pin changes (`pinChanges`): for each of the two manifests, each commit GitHub lists for the
 * manifest's path in the window (`commits?path=…&since=…&until=…`), its manifest set against its
 * first parent's, set by set; a set whose pins differ is a change, with both pins.
 *
 * It reads GitHub only through `gh api <route>`, a GET of the route with its query, through the
 * injected `Gh` (copies.ts's; a test's stand-in), each call sent again by common.ts's `GhRetries`.
 * It names the route's query in the route rather than in `-f` fields with `-X GET`, as build.ts's
 * `GithubReads` does, because the pod's gh refuses that form for an owner its GitHub App is not
 * installed on, and a set's repository need not be one (observed 2026-11-22: `gh api -X GET
 * repos/<owner>/<repo>/contents/package.json -f ref=<commit>` is refused, `gh api
 * 'repos/<owner>/<repo>/contents/package.json?ref=<commit>'` answers).
 */
import { z } from "zod";
import { firstLine, type GhRetries, schemaFaults } from "./common";
import type { SkillCutsConfig } from "./config";
import { type Gh, type GhResult, parseJson } from "./copies";

export const PLUGINS_MANIFEST = "omp/plugins/package.json";
export const INSTALLER = "installers/omp.sh";
export const SKILL_POOLS = "skills-sources.json";

/** A skill set of the configuration. */
export type SkillSet = NonNullable<SkillCutsConfig["sets"]>[number];

/** One line of a dotfiles manifest that pins a set: its line number (from 1), its text (a package
 *  pin as `"<package>": "<spec>"`, an installer line trimmed), and the ref it pins (`""` when the
 *  line pins none); a package pin names its package, a vendor pin its directory. */
export type PinLine =
  | {
      readonly kind: "package";
      readonly set: string;
      readonly manifest: typeof PLUGINS_MANIFEST;
      readonly line: number;
      readonly text: string;
      readonly package: string;
      readonly ref: string;
    }
  | {
      readonly kind: "vendor";
      readonly set: string;
      readonly manifest: typeof INSTALLER;
      readonly line: number;
      readonly text: string;
      readonly dir: string;
      readonly ref: string;
    };

/** A set at the dotfiles commit read: its pin, the commit the pin resolves to and how, where its
 *  skills are read from, and its skills there, by name in order. */
export interface SetAtPin {
  readonly id: string;
  readonly repo: string;
  readonly upstreams: readonly string[];
  readonly pin: PinLine;
  readonly commit: string;
  readonly resolvedBy: string;
  readonly skillsFrom: string;
  readonly skills: readonly string[];
}

/** The pins at one dotfiles commit. */
export interface Pins {
  readonly commit: string;
  /** How the commit was chosen. */
  readonly commitFrom: string;
  readonly sets: readonly SetAtPin[];
}

/** A dotfiles commit at which a set's pins in one manifest changed: each pin's text before and
 *  after (joined by `; ` where a manifest holds several), `none` where there was none. */
export interface PinChange {
  readonly commit: string;
  readonly date: string;
  readonly manifest: string;
  readonly set: string;
  readonly before: string;
  readonly after: string;
}

const COMMIT = /^[0-9a-f]{40}$/;
const NOT_FOUND = /\b404\b|Not Found/;
const RAW = "Accept: application/vnd.github.raw";
const SKILL_FILE = /^skills\/([^/]+)\/SKILL\.md$/;
/** `ensure_vendor <url> <dir> [<commit>]`, a comment after it allowed. */
const ENSURE_VENDOR = /^\s*ensure_vendor\s+(\S+)\s+(\S+)(?:\s+([^\s#]+))?\s*(?:#.*)?$/;
const GITHUB_URL = /^https:\/\/github\.com\/([^/\s]+)\/([^/\s]+?)(?:\.git)?\/?$/i;
const GITHUB_SPEC = /^github:([^/#\s]+)\/([^#\s]+?)(?:\.git)?(?:#(.*))?$/;

// GitHub's answers and the manifests, parsed at the boundary: only the fields read here.
const PluginsManifest = z.looseObject({
  dependencies: z.record(z.string(), z.string()).optional(),
});
const SkillsDeclared = z.looseObject({ skills: z.array(z.string()).optional() }).nullish();
const PluginPackage = z.looseObject({ omp: SkillsDeclared, pi: SkillsDeclared });
const SkillPools = z.looseObject({
  sources: z.array(
    z.looseObject({
      root: z.string(),
      ompSkillsDir: z.string().optional(),
      omp: z.boolean().optional(),
    })
  ),
});
const GitRef = z.looseObject({
  object: z.looseObject({ sha: z.string().regex(COMMIT), type: z.string() }),
});
const GitTree = z.looseObject({
  tree: z.array(z.looseObject({ path: z.string(), type: z.string() })),
  truncated: z.boolean().optional(),
});
const DefaultBranch = z.looseObject({ default_branch: z.string().min(1) });
const Forked = z.looseObject({
  parent: z.looseObject({ full_name: z.string() }).nullish(),
  source: z.looseObject({ full_name: z.string() }).nullish(),
});
const Commits = z.array(
  z.looseObject({
    sha: z.string().regex(COMMIT),
    commit: z.looseObject({ committer: z.looseObject({ date: z.string() }) }),
    parents: z.array(z.looseObject({ sha: z.string() })),
  })
);
/** The commits GitHub gives in one page of a listing; a page with fewer is the last. */
const PAGE = 100;

/** Whether `a` and `b` name one repository: GitHub's owner and repository names ignore case. */
const sameRepo = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

/** The pins of `sets` in the text of an `omp/plugins/package.json`; throws when it is not one. */
export function packagePins(text: string, sets: readonly SkillSet[]): PinLine[] {
  const parsed = PluginsManifest.safeParse(parseJson(text));
  if (!parsed.success) {
    throw new Error(`${PLUGINS_MANIFEST} is not a package.json of string dependencies`);
  }
  const lines = text.split("\n");
  const pins: PinLine[] = [];
  for (const [name, spec] of Object.entries(parsed.data.dependencies ?? {})) {
    const github = GITHUB_SPEC.exec(spec);
    if (github === null) continue;
    const set = sets.find((each) => sameRepo(each.repo, `${github[1]}/${github[2]}`));
    if (set === undefined) continue;
    const key = JSON.stringify(name);
    const value = JSON.stringify(spec);
    const at = lines.findIndex((line) => line.trimStart().startsWith(key) && line.includes(value));
    pins.push({
      kind: "package",
      set: set.id,
      manifest: PLUGINS_MANIFEST,
      line: at + 1,
      text: `${key}: ${value}`,
      package: name,
      ref: github[3] ?? "",
    });
  }
  return pins;
}

/** The pins of `sets` in the text of an `installers/omp.sh`. */
export function vendorPins(text: string, sets: readonly SkillSet[]): PinLine[] {
  const pins: PinLine[] = [];
  for (const [index, line] of text.split("\n").entries()) {
    const vendor = ENSURE_VENDOR.exec(line);
    const url = GITHUB_URL.exec(vendor?.[1] ?? "");
    if (vendor === null || url === null) continue;
    const set = sets.find((each) => sameRepo(each.repo, `${url[1]}/${url[2]}`));
    if (set === undefined) continue;
    pins.push({
      kind: "vendor",
      set: set.id,
      manifest: INSTALLER,
      line: index + 1,
      text: line.trim(),
      dir: vendor[2] ?? "",
      ref: vendor[3] ?? "",
    });
  }
  return pins;
}

/** A directory as a manifest writes it, less a leading `./` and a trailing `/`. */
const plainDir = (dir: string) => dir.replace(/^\.\//, "").replace(/\/+$/, "");

/** Where a plugin package's `package.json` text (undefined: the commit holds none) says its
 *  skills are, as Oh My Pi reads it: `omp.skills`, else `pi.skills`, else `skills/`. Throws when
 *  it names any directory but `skills`. */
export function packageSkills(text: string | undefined): string {
  if (text === undefined) return "no package.json, so skills/";
  const parsed = PluginPackage.safeParse(parseJson(text));
  if (!parsed.success) throw new Error("its package.json is not a JSON object");
  const key = parsed.data.omp != null ? "omp" : "pi";
  const entries = parsed.data[key]?.skills ?? [];
  if (entries.length === 0) {
    return "its package.json declares no omp.skills or pi.skills, so skills/";
  }
  if (entries.some((entry) => plainDir(entry) !== "skills")) {
    throw new Error(
      `its package.json declares its skills at ${entries.join(", ")} (${key}.skills), not skills/; set-count lists skills/<name>/SKILL.md only`
    );
  }
  return `${key}.skills ${entries.join(", ")}`;
}

/** The `skills-sources.json` pool (its text; undefined: the commit holds none) that gives Oh My
 *  Pi the skills of the vendor checkout `vendor/<dir>`. Throws unless it is one pool whose root is
 *  that directory, whose skills are at its `skills/`. */
export function vendorPool(text: string | undefined, dir: string): string {
  const root = `vendor/${dir}`;
  if (text === undefined) {
    throw new Error(`the dotfiles commit holds no ${SKILL_POOLS}, so no pool gives ${root}`);
  }
  const parsed = SkillPools.safeParse(parseJson(text));
  if (!parsed.success) {
    throw new Error(`${SKILL_POOLS} is not a list of sources: ${schemaFaults(parsed.error)}`);
  }
  const pools = parsed.data.sources.filter((pool) => {
    const at = plainDir(pool.root);
    return at === root || at.startsWith(`${root}/`);
  });
  const pool = pools[0];
  if (
    pools.length !== 1 ||
    pool === undefined ||
    plainDir(pool.root) !== root ||
    pool.ompSkillsDir !== undefined ||
    pool.omp === false
  ) {
    const given =
      pools.length === 0
        ? "no pool"
        : `the pools ${pools
            .map(
              (each) =>
                each.root +
                (each.ompSkillsDir === undefined ? "" : ` (ompSkillsDir ${each.ompSkillsDir})`) +
                (each.omp === false ? " (omp false)" : "")
            )
            .join(", ")}`;
    throw new Error(
      `${SKILL_POOLS} gives ${root} ${given}, not one pool ${root} whose skills are at skills/; set-count lists skills/<name>/SKILL.md only`
    );
  }
  return `${SKILL_POOLS} pool ${root}`;
}

/** The GitHub reads of the pins, through `gh`, each sent again as `retries` says. */
export class SetReads {
  constructor(
    private readonly gh: Gh,
    private readonly retries: GhRetries
  ) {}

  /** gh's answer to `gh api [-H <raw accept>] <route>`. */
  private answer(route: string, raw = false): Promise<GhResult> {
    const args = ["api", ...(raw ? ["-H", RAW] : []), route];
    return this.retries.send(() => this.gh(args));
  }

  /** The answer `result` of `route` as JSON of the shape `schema`; throws naming the route. */
  private parsed<T>(route: string, result: GhResult, schema: z.ZodType<T>): T {
    if (result.code !== 0) throw new Error(`gh api ${route} failed: ${firstLine(result.stderr)}`);
    const parsed = schema.safeParse(parseJson(result.stdout));
    if (!parsed.success) {
      throw new Error(`gh api ${route} gave no answer of its shape: ${schemaFaults(parsed.error)}`);
    }
    return parsed.data;
  }

  /** The JSON answer of `route`, of the shape `schema`; throws naming the route. */
  private async json<T>(route: string, schema: z.ZodType<T>): Promise<T> {
    return this.parsed(route, await this.answer(route), schema);
  }

  /** The text of `file` of `repo` at `commit`, or undefined when the commit holds none. */
  async text(repo: string, commit: string, file: string): Promise<string | undefined> {
    const route = `repos/${repo}/contents/${file.split("/").map(encodeURIComponent).join("/")}?ref=${commit}`;
    const result = await this.answer(route, true);
    if (result.code === 0) return result.stdout;
    if (NOT_FOUND.test(result.stderr)) return undefined;
    throw new Error(`gh api ${route} failed: ${firstLine(result.stderr)}`);
  }

  /** The commit of `repo`'s default branch now, and the branch. */
  async defaultCommit(repo: string): Promise<{ commit: string; branch: string }> {
    const branch = (await this.json(`repos/${repo}`, DefaultBranch)).default_branch;
    const ref = await this.json(`repos/${repo}/git/ref/heads/${branch}`, GitRef);
    if (ref.object.type !== "commit") {
      throw new Error(`the branch ${branch} of ${repo} names a ${ref.object.type}, not a commit`);
    }
    return { commit: ref.object.sha, branch };
  }

  /** The commit the ref `ref` of `repo` names, and how: a 40-hex commit as it is, a tag through
   *  its ref and, for an annotated tag, its tag object. */
  async resolve(repo: string, ref: string): Promise<{ commit: string; by: string }> {
    if (COMMIT.test(ref)) return { commit: ref, by: "a commit" };
    if (ref === "") throw new Error(`the pin of ${repo} names no ref`);
    const route = `repos/${repo}/git/ref/tags/${ref.split("/").map(encodeURIComponent).join("/")}`;
    const result = await this.answer(route);
    if (result.code !== 0 && NOT_FOUND.test(result.stderr)) {
      throw new Error(
        `${repo} has no tag ${ref}: set-count resolves a pin that is a tag or a 40-hex commit`
      );
    }
    const tag = this.parsed(route, result, GitRef);
    if (tag.object.type === "commit") {
      return { commit: tag.object.sha, by: `the lightweight tag ${ref}` };
    }
    if (tag.object.type === "tag") {
      const object = await this.json(`repos/${repo}/git/tags/${tag.object.sha}`, GitRef);
      if (object.object.type === "commit") {
        return {
          commit: object.object.sha,
          by: `the annotated tag ${ref} (tag object ${tag.object.sha})`,
        };
      }
    }
    throw new Error(`the tag ${ref} of ${repo} names no commit`);
  }

  /** The skills of `repo` at `commit`: each `skills/<name>/SKILL.md` of its tree, by name. */
  async skills(repo: string, commit: string): Promise<string[]> {
    const tree = await this.json(`repos/${repo}/git/trees/${commit}?recursive=1`, GitTree);
    if (tree.truncated === true) {
      throw new Error(
        `GitHub cuts the tree of ${repo} at ${commit} short, so its skills are not listed`
      );
    }
    return tree.tree
      .flatMap((entry) => {
        const skill = entry.type === "blob" ? SKILL_FILE.exec(entry.path) : null;
        return skill === null ? [] : [skill[1] ?? ""];
      })
      .sort();
  }

  /** The repositories `repo` is a fork of: its parent and its source, each once. */
  async upstreams(repo: string): Promise<string[]> {
    const answer = await this.json(`repos/${repo}`, Forked);
    const names = [answer.parent?.full_name, answer.source?.full_name].filter(
      (name): name is string => name !== undefined && !sameRepo(name, repo)
    );
    return [...new Set(names)];
  }

  /** Each commit of `repo` that GitHub lists for the path `file` from `since` to `until`, newest
   *  first, every page. */
  async commits(repo: string, file: string, since: string, until: string) {
    const all: z.infer<typeof Commits> = [];
    for (let page = 1; ; page++) {
      const route = `repos/${repo}/commits?path=${file}&since=${since}&until=${until}&per_page=${PAGE}&page=${page}`;
      const commits = await this.json(route, Commits);
      all.push(...commits);
      if (commits.length < PAGE) return all;
    }
  }
}

/** The pins of `sets` at the commit `commit` of `dotfilesRepo`, or at its default branch's commit
 *  now when `commit` is undefined. Throws on a set no line or more than one line pins, a pin that
 *  does not resolve, a set whose skills are not at `skills/`, and a read that fails. */
export async function readPins(
  reads: SetReads,
  sets: readonly SkillSet[],
  dotfilesRepo: string,
  commit: string | undefined
): Promise<Pins> {
  let at = commit;
  let commitFrom = "--dotfiles-commit";
  if (at === undefined) {
    const head = await reads.defaultCommit(dotfilesRepo);
    at = head.commit;
    commitFrom = `the default branch ${head.branch} of ${dotfilesRepo} when the count ran`;
  }
  const plugins = await reads.text(dotfilesRepo, at, PLUGINS_MANIFEST);
  const installer = await reads.text(dotfilesRepo, at, INSTALLER);
  const pins = [
    ...(plugins === undefined ? [] : packagePins(plugins, sets)),
    ...(installer === undefined ? [] : vendorPins(installer, sets)),
  ];
  let pools: Promise<string | undefined> | undefined;
  const read: SetAtPin[] = [];
  for (const set of sets) {
    const mine = pins.filter((pin) => pin.set === set.id);
    const pin = mine[0];
    if (pin === undefined) {
      throw new Error(
        `the dotfiles commit ${at} pins no ${set.id} (${set.repo}): neither ${PLUGINS_MANIFEST} as a github: dependency nor ${INSTALLER} by an ensure_vendor line`
      );
    }
    if (mine.length > 1) {
      throw new Error(
        `the dotfiles commit ${at} pins ${set.id} ${mine.length} times (${mine.map((each) => `${each.manifest}:${each.line}`).join(", ")}); set-count reads one pin a set`
      );
    }
    const resolved = await reads.resolve(set.repo, pin.ref);
    let skillsFrom: string;
    try {
      if (pin.kind === "package") {
        skillsFrom = packageSkills(await reads.text(set.repo, resolved.commit, "package.json"));
      } else {
        pools ??= reads.text(dotfilesRepo, at, SKILL_POOLS);
        skillsFrom = vendorPool(await pools, pin.dir);
      }
    } catch (error) {
      throw new Error(`${set.id} at ${resolved.commit}: ${(error as Error).message}`);
    }
    read.push({
      id: set.id,
      repo: set.repo,
      upstreams: await reads.upstreams(set.repo),
      pin,
      commit: resolved.commit,
      resolvedBy: resolved.by,
      skillsFrom,
      skills: await reads.skills(set.repo, resolved.commit),
    });
  }
  return { commit: at, commitFrom, sets: read };
}

/** Each commit of `dotfilesRepo` in [`since`, `until`) at which a pin of one of `sets` changed, in
 *  the order of their dates. */
export async function pinChanges(
  reads: SetReads,
  sets: readonly SkillSet[],
  dotfilesRepo: string,
  since: string,
  until: string
): Promise<PinChange[]> {
  const changes: PinChange[] = [];
  for (const [manifest, pinsOf] of [
    [PLUGINS_MANIFEST, packagePins],
    [INSTALLER, vendorPins],
  ] as const) {
    for (const commit of await reads.commits(dotfilesRepo, manifest, since, until)) {
      const parent = commit.parents[0]?.sha;
      const before =
        parent === undefined ? undefined : await reads.text(dotfilesRepo, parent, manifest);
      const after = await reads.text(dotfilesRepo, commit.sha, manifest);
      /** The text of the pins of `set` in the manifest's text `text` at `at`. */
      const pinned = (text: string | undefined, at: string | undefined, set: SkillSet) => {
        try {
          const pins = text === undefined ? [] : pinsOf(text, [set]);
          return pins.map((pin) => pin.text).join("; ") || "none";
        } catch (error) {
          throw new Error(`${manifest} at ${at}: ${(error as Error).message}`);
        }
      };
      for (const set of sets) {
        const was = pinned(before, parent, set);
        const is = pinned(after, commit.sha, set);
        if (was === is) continue;
        changes.push({
          commit: commit.sha,
          date: commit.commit.committer.date,
          manifest,
          set: set.id,
          before: was,
          after: is,
        });
      }
    }
  }
  return changes.sort(
    (a, b) =>
      a.date.localeCompare(b.date) ||
      a.manifest.localeCompare(b.manifest) ||
      a.set.localeCompare(b.set)
  );
}
