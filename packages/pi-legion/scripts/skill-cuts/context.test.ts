import { describe, expect, test } from "bun:test";
import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  readlinkSync,
  realpathSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import path from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import type { SessionMessage } from "../skill-scenarios/transcript";
import { type Arm, buildArm } from "./arms";
import { type Case, pathHash } from "./cases";
import {
  buildContext,
  type ContextOptions,
  cacheKey,
  ghSource,
  gitBlobId,
  legionRole,
  localSource,
  NoSuchFile,
  PROMPTS_GO,
  type RepoSource,
  readFrontmatter,
  skillFrontmatter,
  UnreadableDirectory,
} from "./context";

// The case context (context.ts) built from made-up repositories, read from local
// directories in place of `gh`: `acme/skills` (the skills alpha and beta), `acme/project` (an
// AGENTS.md and a CLAUDE.md at its root, Claude Code's user memory at .claude/CLAUDE.md, and the
// AGENTS.md, .claude/CLAUDE.md and .github/copilot-instructions.md of its directory agents/vm),
// `acme/infra` (a project's instructions file) and `acme/legion`, which holds a
// made-up daemon whose role prompt composer has the API of packages/daemon/internal/prompts
// (`New(stateDir)`, `Compose(role, isRoot)`, the `roles/` parts snapshotted under
// prompts/shared and the `go/` parts under prompts/go) and two made-up plugins. The Legion row
// runs `go test` in a copy of that repository; a run without Go skips it, except on GitHub
// Actions, where the pi-legion job sets Go up.
const COMMIT = "89abcdef0123456789abcdef0123456789abcdef";
const goMissing = Bun.which("go") === null;
const onActions = process.env.GITHUB_ACTIONS === "true";
if (goMissing && !onActions) {
  console.log("context.test.ts: no `go` on PATH, so the Legion role prompt row is skipped");
}

const dir = temporaryDirectory("skill-cuts-context", { real: true });

/** Writes `files` (path to text) under `root`. */
function writeTree(root: string, files: Record<string, string>): void {
  for (const [file, text] of Object.entries(files)) {
    mkdirSync(path.dirname(path.join(root, file)), { recursive: true });
    writeFileSync(path.join(root, file), text);
  }
}

const ALPHA =
  "---\nname: alpha\ndescription: >-\n  Use when the alpha list\n  is asked for.\n---\n\n# Alpha\n\n1. One.\n";
const ALPHA_ARM = ALPHA.replace("1. One.", "1. One, from the arm.");
const BETA = "---\nname: beta\ndescription: Use for the beta greeting.\n---\n\n# Beta\n";
const AGENTS = "# Project\n\nKeep answers short.\n";
/** Claude Code's user memory in a made-up repository: a devbox profile's user-level file. */
const USER_RULES = "# Made-up user rules\n\nAnswer in one line.\n";
/** A made-up repository's root CLAUDE.md: the project context file of a devbox session whose
 *  working directory is a checkout of it. */
const PROJECT_RULES = "# Made-up project rules\n\nName each file you change.\n";
/** The made-up acme/project's context files of its directory agents/vm. */
const VM_AGENTS = "# VM\n\nRun the VM steps in order.\n";
const VM_CLAUDE = "# VM for Claude\n\nKeep the VM notes short.\n";
const VM_COPILOT = "# VM for Copilot\n\nName the VM in each reply.\n";
const INSTRUCTIONS = "# Acme instructions\n\nWork the acme way.\n\n\n";

/** The made-up daemon module: its role prompt composer and the parts it embeds. */
const DAEMON: Record<string, string> = {
  "packages/daemon/go.mod": "module example.com/acme/daemon\n\ngo 1.22\n",
  "packages/daemon/internal/claim/claim.go": `package claim

type Role string

const (
	RoleArchitect   Role = "architect"
	RoleImplementer Role = "implementer"
)
`,
  [PROMPTS_GO]: `package prompts

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"example.com/acme/daemon/internal/claim"
)

//go:embed roles
var roleParts embed.FS

//go:embed go/*.md
var goParts embed.FS

type Parts struct{ RolePromptPaths []string }

type Composer struct{ sharedDir, goDir string }

func write(parts embed.FS, dir, to string) error {
	return fs.WalkDir(parts, dir, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := parts.ReadFile(name)
		if err != nil {
			return err
		}
		target := filepath.Join(to, name[len(dir)+1:])
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o600)
	})
}

func New(stateDir string) (*Composer, error) {
	shared := filepath.Join(stateDir, "prompts", "shared")
	goDir := filepath.Join(stateDir, "prompts", "go")
	if err := write(roleParts, "roles", shared); err != nil {
		return nil, err
	}
	if err := write(goParts, "go", goDir); err != nil {
		return nil, err
	}
	return &Composer{sharedDir: shared, goDir: goDir}, nil
}

func (c *Composer) Compose(role claim.Role, isRoot bool) (Parts, error) {
	switch {
	case role == claim.RoleArchitect && isRoot:
		return Parts{RolePromptPaths: []string{filepath.Join(c.sharedDir, "architect-root.md"), filepath.Join(c.goDir, "architect.md")}}, nil
	case role == claim.RoleImplementer:
		return Parts{RolePromptPaths: []string{filepath.Join(c.sharedDir, "core", "common.md"), filepath.Join(c.sharedDir, "implementer.md"), filepath.Join(c.goDir, "worker.md")}}, nil
	}
	return Parts{}, fmt.Errorf("unsupported role %q", role)
}

func (c *Composer) ControllerPromptPaths(headless bool) ([]string, error) {
	launch := "controller-interactive.md"
	if headless {
		launch = "controller-headless.md"
	}
	return []string{filepath.Join(c.sharedDir, "controller-root.md"), filepath.Join(c.goDir, launch)}, nil
}
`,
  "packages/daemon/internal/prompts/roles/core/common.md": "Core part.\n",
  "packages/daemon/internal/prompts/roles/implementer.md": "Implementer part.\n",
  "packages/daemon/internal/prompts/roles/architect-root.md": "Root architect part.\n",
  "packages/daemon/internal/prompts/roles/controller-root.md": "Controller part.\n",
  "packages/daemon/internal/prompts/go/worker.md": "Worker part.\n\n",
  "packages/daemon/internal/prompts/go/architect.md": "Architect part.\n",
  "packages/daemon/internal/prompts/go/controller-headless.md": "Headless controller part.\n",
  "packages/daemon/internal/prompts/go/controller-interactive.md": "Interactive controller part.\n",
};

/** A made-up plugin that builds a bundle, ships `dist`, and names `dist/skills` for its skills,
 *  and one whose build fails. */
const PLUGINS: Record<string, string> = {
  "packages/plugin-a/package.json": JSON.stringify({
    name: "plugin-a",
    files: ["dist"],
    scripts: { build: "bun build extensions/a.ts --outdir dist --target bun" },
    omp: { extensions: ["extensions/a.ts"], skills: ["dist/skills"] },
  }),
  "packages/plugin-a/extensions/a.ts": "export default function plugin(): void {}\n",
  "packages/plugin-a/dist/skills/stale/SKILL.md":
    "---\nname: stale\ndescription: A stale copy.\n---\n",
  "packages/plugin-b/package.json": JSON.stringify({
    name: "plugin-b",
    files: ["dist"],
    scripts: { build: "exit 3" },
    omp: { extensions: ["extensions/b.ts"] },
  }),
  "packages/plugin-b/extensions/b.ts": "export default function plugin(): void {}\n",
};

/** Writes the made-up repositories under `root`. */
function writeRepositories(root: string): void {
  writeTree(path.join(root, "acme/skills", COMMIT), {
    "skills/alpha/SKILL.md": ALPHA,
    "skills/beta/SKILL.md": BETA,
    "skills/beta/references/r.md": "Beta reference.\n",
  });
  writeTree(path.join(root, "acme/project", COMMIT), {
    "AGENTS.md": AGENTS,
    "CLAUDE.md": PROJECT_RULES,
    ".claude/CLAUDE.md": USER_RULES,
    "agents/vm/AGENTS.md": VM_AGENTS,
    "agents/vm/.claude/CLAUDE.md": VM_CLAUDE,
    "agents/vm/.github/copilot-instructions.md": VM_COPILOT,
  });
  writeTree(path.join(root, "acme/infra", COMMIT), {
    "legion/instructions/acme.md": INSTRUCTIONS,
  });
  writeTree(path.join(root, "acme/legion", COMMIT), {
    "package.json": JSON.stringify({ name: "acme-legion", private: true }),
    ...DAEMON,
    ...PLUGINS,
    "plugins/skills-only/skills/beta/SKILL.md": BETA,
  });
}

/** A read of skill://alpha and its result, then the step. */
const messages: SessionMessage[] = [
  { role: "user", content: [{ type: "text", text: "Write the alpha list." }] },
  {
    role: "assistant",
    content: [
      { type: "toolCall", id: "toolu_1", name: "read", arguments: { path: "skill://alpha" } },
    ],
  },
  {
    role: "toolResult",
    toolCallId: "toolu_1",
    toolName: "read",
    content: [{ type: "text", text: `[Skill file: /skills/alpha/SKILL.md]\n${ALPHA.trimEnd()}` }],
    isError: false,
  },
];

/** A case of alpha in `context`, its profile naming `plugins` and `contextFiles`. */
function aCase(context: string, profile: Partial<Case["profile"]>): Case {
  return {
    id: `case-${context}`,
    cut: "acme-skills-1",
    skill: "alpha",
    baseFile: "SKILL.md",
    line: { text: "1. One.", lineNumber: 9, lineCount: 9 },
    criterion: { pass: "It lists one.", fail: "It lists none." },
    model: "anthropic/claude-sonnet-5-5",
    thinking: "off",
    context,
    profile: {
      skills: [
        { repo: "acme/skills", commit: COMMIT, path: "skills/alpha", name: "alpha" },
        { repo: "acme/skills", commit: COMMIT, path: "skills/beta", name: "beta" },
      ],
      plugins: [],
      contextFiles: [{ repo: "acme/project", commit: COMMIT, path: "AGENTS.md" }],
      ...profile,
    },
    messages,
    skillReads: [2],
    realStep: { role: "assistant", content: [{ type: "text", text: "1. One." }] },
    violatingStep: { role: "assistant", content: [{ type: "text", text: "None." }] },
    source: { store: "store_a", pathHash: pathHash("/sessions/a.jsonl") },
    sessionStart: "2026-11-20T01:00:00Z",
    secretsRemoved: 0,
    sessionDigest: null,
  };
}

/** The options and the old arm of `c`, under `root`. */
function setUp(root: string, c: Case): { options: ContextOptions; arm: Arm } {
  writeRepositories(path.join(root, "repos"));
  writeTree(path.join(root, "old"), { "SKILL.md": ALPHA_ARM });
  const arm = buildArm(
    c,
    "old",
    { old: path.join(root, "old"), new: path.join(root, "old") },
    root
  );
  return {
    options: {
      source: localSource(path.join(root, "repos")),
      cacheDir: path.join(root, "cache"),
      root: path.join(root, "scratch"),
    },
    arm,
  };
}

test("the git blob id, the frontmatter description and the role of a context are read as their owners read them", () => {
  // `printf 'hello\n' | git hash-object --stdin`
  expect(gitBlobId(new TextEncoder().encode("hello\n"))).toBe(
    "ce013625030ba8dba906f756967f9e9ca394464a"
  );
  writeTree(path.join(dir, "folded"), { "SKILL.md": ALPHA });
  expect(skillFrontmatter(path.join(dir, "folded"))).toEqual({
    description: "Use when the alpha list is asked for.",
    hidden: false,
  });
  writeTree(path.join(dir, "hidden"), {
    "SKILL.md": "---\nname: h\ndescription: d\ndisable-model-invocation: true\n---\n",
  });
  expect(skillFrontmatter(path.join(dir, "hidden")).hidden).toBe(true);
  writeTree(path.join(dir, "bare"), { "SKILL.md": "# No frontmatter\n" });
  expect(skillFrontmatter(path.join(dir, "bare"))).toEqual({ description: "", hidden: false });
  expect(legionRole("legion:acme:implementer")).toEqual({ role: "implementer", isRoot: false });
  expect(legionRole("legion:acme:architect-root")).toEqual({ role: "architect", isRoot: true });
  expect(legionRole("devbox")).toBeUndefined();
});

test("each skill is its repository's directory at its commit, through the cache, and the case's skill is the arm's", async () => {
  const root = path.join(dir, "skills");
  const c = aCase("devbox", {
    contextFiles: [{ repo: "acme/project", commit: COMMIT, path: ".claude/CLAUDE.md", user: true }],
  });
  const { options, arm } = setUp(root, c);
  const first = await buildContext(c, arm, options);
  expect(first.skills.map((skill) => skill.name)).toEqual(["alpha", "beta"]);
  expect(readFileSync(path.join(first.skillsDir, "alpha/SKILL.md"), "utf8")).toBe(ALPHA_ARM);
  expect(readFileSync(path.join(first.skillsDir, "beta/references/r.md"), "utf8")).toBe(
    "Beta reference.\n"
  );
  expect(first.skills[1]?.description).toBe("Use for the beta greeting.");
  expect(first.record.skills[0]).toEqual({ name: "alpha", from: "arm" });
  expect(first.record.builds.map((build) => [build.kind, build.hit])).toEqual([
    ["skill", false],
    ["file", false],
  ]);
  const none = buildArm(
    c,
    "none",
    { old: path.join(root, "old"), new: path.join(root, "old") },
    root
  );
  const again = await buildContext(c, none, options);
  expect(again.skills.map((skill) => skill.name)).toEqual(["beta"]);
  expect(existsSync(path.join(again.skillsDir, "alpha"))).toBe(false);
  expect(again.record.builds.map((build) => [build.kind, build.hit])).toEqual([
    ["skill", true],
    ["file", true],
  ]);
  // A devbox's context file is its user-level one: CLAUDE.md in the scratch Claude Code config
  // directory the host's run names, not a file of the working directory; and it has no role
  // prompt.
  expect(again.claudeConfigDir).toBe(path.join(options.root, "claude-config"));
  expect(readFileSync(path.join(again.claudeConfigDir ?? "", "CLAUDE.md"), "utf8")).toBe(
    USER_RULES
  );
  expect(existsSync(path.join(again.workspace, ".claude"))).toBe(false);
  expect(again.record.contextFiles.map((file) => file.as)).toEqual(["user level"]);
  expect(again.appendSystemPrompt).toBeUndefined();
  expect(again.record.rolePrompt).toBeNull();
});

// Catches: the arm of ours installed under their skill's name, their skill installed beside it,
// or a profile that already holds a skill of our name installing that name twice.
test("installs ours under its own name and never a name twice", async () => {
  const root = path.join(dir, "ours");
  const c = aCase("devbox", {});
  const { options } = setUp(root, c);
  const OURS = "---\nname: alpha-ours\ndescription: Use for our alpha list.\n---\n\n# Ours\n";
  writeTree(path.join(root, "alpha-ours"), { "SKILL.md": OURS });
  const dirs = {
    old: path.join(root, "old"),
    ours: { dir: path.join(root, "alpha-ours"), name: "alpha-ours" },
  };
  const ours = buildArm(c, "ours", dirs, root);
  const built = await buildContext(c, ours, options);
  expect(built.skills.map((skill) => [skill.name, skill.description])).toEqual([
    ["alpha-ours", "Use for our alpha list."],
    ["beta", "Use for the beta greeting."],
  ]);
  expect(readFileSync(path.join(built.skillsDir, "alpha-ours/SKILL.md"), "utf8")).toBe(OURS);
  expect(existsSync(path.join(built.skillsDir, "alpha"))).toBe(false);
  expect(built.record.skills[0]).toEqual({ name: "alpha-ours", from: "arm" });

  // A profile that holds a skill of our name: the --ours text is installed once, in its place.
  const holding = aCase("devbox", {
    skills: [
      ...c.profile.skills,
      { repo: "acme/skills", commit: COMMIT, path: "skills/beta", name: "alpha-ours" },
    ],
  });
  const bundled = await buildContext(holding, buildArm(holding, "ours", dirs, root), options);
  expect(bundled.skills.map((skill) => skill.name)).toEqual(["beta", "alpha-ours"]);
  expect(readFileSync(path.join(bundled.skillsDir, "alpha-ours/SKILL.md"), "utf8")).toBe(OURS);
  expect(existsSync(path.join(bundled.skillsDir, "alpha"))).toBe(false);

  // The guard stays: an arm whose profile installs one name twice is refused.
  const [first] = ours.profileSkills;
  const twice: Arm = { ...ours, profileSkills: [...ours.profileSkills, ...(first ? [first] : [])] };
  expect(await buildContext(c, twice, options).then(() => "built", String)).toContain(
    "installs alpha-ours twice"
  );
});

test("a devbox profile's user-level .claude/CLAUDE.md (user: true) goes in the Claude Code config directory, and the root CLAUDE.md or AGENTS.md of its session's repository in the working directory; two user-level files are refused", async () => {
  const root = path.join(dir, "devbox-files");
  const c = aCase("devbox", {
    contextFiles: [
      { repo: "acme/project", commit: COMMIT, path: ".claude/CLAUDE.md", user: true },
      { repo: "acme/project", commit: COMMIT, path: "CLAUDE.md" },
    ],
  });
  const { options, arm } = setUp(root, c);
  const built = await buildContext(c, arm, options);
  expect(built.claudeConfigDir).toBe(path.join(options.root, "claude-config"));
  expect(readFileSync(path.join(built.claudeConfigDir ?? "", "CLAUDE.md"), "utf8")).toBe(
    USER_RULES
  );
  // Oh My Pi reads the repository's own file from the working directory, as for a Legion pane.
  expect(readFileSync(path.join(built.workspace, "CLAUDE.md"), "utf8")).toBe(PROJECT_RULES);
  expect(existsSync(path.join(built.workspace, ".claude"))).toBe(false);
  expect(built.record.contextFiles.map((file) => file.as)).toEqual([
    "user level",
    "working directory",
  ]);
  expect(built.appendSystemPrompt).toBeUndefined();
  // A devbox profile with no user-level file has no Claude Code config directory.
  const projectOnly = aCase("devbox", {
    contextFiles: [{ repo: "acme/project", commit: COMMIT, path: "AGENTS.md" }],
  });
  const alone = await buildContext(projectOnly, arm, options);
  expect(alone.claudeConfigDir).toBeUndefined();
  expect(existsSync(path.join(options.root, "claude-config"))).toBe(false);
  expect(readFileSync(path.join(alone.workspace, "AGENTS.md"), "utf8")).toBe(AGENTS);
  expect(alone.record.contextFiles.map((file) => file.as)).toEqual(["working directory"]);
  // Oh My Pi reads one user-level context file, so a profile of two is refused.
  const two = aCase("devbox", {
    contextFiles: [
      { repo: "acme/project", commit: COMMIT, path: ".claude/CLAUDE.md", user: true },
      { repo: "acme/infra", commit: COMMIT, path: ".claude/CLAUDE.md", user: true },
    ],
  });
  expect(await buildContext(two, arm, options).catch(String)).toContain(
    "its profile holds 2 user-level context files, and Oh My Pi reads one"
  );
});

test("the session repository's context files are written at their paths in its checkout and the working directory is the profile's workingDirectory there, so each is at the depth the session read it; its own .claude/CLAUDE.md is no user-level file", async () => {
  const root = path.join(dir, "levels");
  const c = aCase("devbox", {
    contextFiles: [
      { repo: "acme/project", commit: COMMIT, path: ".claude/CLAUDE.md", user: true },
      { repo: "acme/project", commit: COMMIT, path: "CLAUDE.md" },
      { repo: "acme/project", commit: COMMIT, path: "agents/vm/AGENTS.md" },
      { repo: "acme/project", commit: COMMIT, path: "agents/vm/.claude/CLAUDE.md" },
      { repo: "acme/project", commit: COMMIT, path: "agents/vm/.github/copilot-instructions.md" },
    ],
    workingDirectory: "agents/vm",
  });
  const { options, arm } = setUp(root, c);
  const built = await buildContext(c, arm, options);
  const checkout = path.join(options.root, "work");
  expect(built.checkout).toBe(checkout);
  expect(built.workspace).toBe(path.join(checkout, "agents", "vm"));
  expect(readFileSync(path.join(checkout, "CLAUDE.md"), "utf8")).toBe(PROJECT_RULES);
  expect(readFileSync(path.join(built.workspace, "AGENTS.md"), "utf8")).toBe(VM_AGENTS);
  expect(readFileSync(path.join(built.workspace, ".claude", "CLAUDE.md"), "utf8")).toBe(VM_CLAUDE);
  expect(
    readFileSync(path.join(built.workspace, ".github", "copilot-instructions.md"), "utf8")
  ).toBe(VM_COPILOT);
  // Only the file marked user: true is the user-level one.
  expect(readFileSync(path.join(built.claudeConfigDir ?? "", "CLAUDE.md"), "utf8")).toBe(
    USER_RULES
  );
  expect(existsSync(path.join(checkout, ".claude"))).toBe(false);
  expect(built.record.contextFiles.map((file) => file.as)).toEqual([
    "user level",
    "working directory",
    "working directory",
    "working directory",
    "working directory",
  ]);
  expect(built.appendSystemPrompt).toBeUndefined();
});

describe.skipIf(goMissing && !onActions)("a Legion context", () => {
  test("its system prompt file is the role prompt Compose names at the plugin repository's commit, then the instructions file, and its plugins are built or left out", async () => {
    const root = path.join(dir, "legion");
    const c = aCase("legion:acme:implementer", {
      plugins: [
        { repo: "acme/legion", commit: COMMIT, path: "packages/plugin-a", name: "plugin-a" },
        { repo: "acme/legion", commit: COMMIT, path: "packages/plugin-b", name: "plugin-b" },
        { repo: "acme/legion", commit: COMMIT, path: "plugins/skills-only", name: "skills-only" },
      ],
      contextFiles: [
        { repo: "acme/infra", commit: COMMIT, path: "legion/instructions/acme.md" },
        { repo: "acme/project", commit: COMMIT, path: "AGENTS.md" },
      ],
    });
    const { options, arm } = setUp(root, c);
    const built = await buildContext(c, arm, options);
    expect(readFileSync(built.appendSystemPrompt ?? "", "utf8")).toBe(
      "Core part.\nImplementer part.\nWorker part.\n\n# Acme instructions\n\nWork the acme way."
    );
    expect(built.record.rolePrompt).toMatchObject({
      repo: "acme/legion",
      commit: COMMIT,
      role: "implementer",
      isRoot: false,
      promptsBlob: gitBlobId(new TextEncoder().encode(DAEMON[PROMPTS_GO])),
      parts: [
        "packages/daemon/internal/prompts/roles/core/common.md",
        "packages/daemon/internal/prompts/roles/implementer.md",
        "packages/daemon/internal/prompts/go/worker.md",
      ],
    });
    expect(built.record.contextFiles.map((file) => file.as)).toEqual([
      "system prompt",
      "working directory",
    ]);
    expect(built.record.leftOut.join(" ")).toContain("addressing sentence");
    // Compose ran once, in the cached copy of the repository, and its Go test is gone from it.
    expect(built.record.builds.filter((build) => build.kind === "compose")).toMatchObject([
      { hit: false },
    ]);
    const internal = path.join(
      options.cacheDir,
      "trees",
      cacheKey("acme/legion", COMMIT),
      "packages/daemon/internal"
    );
    expect(readdirSync(internal).sort()).toEqual(["claim", "prompts"]);

    // plugin-a is built, ships its bundle, and its skills path is the profile's skills.
    const plugins = Object.fromEntries(built.record.plugins.map((p) => [p.name, p]));
    expect(plugins["plugin-a"]?.as).toBe("extension");
    expect(plugins["plugin-b"]?.as).toBe("left out");
    expect(plugins["plugin-b"]?.reason).toContain("exit 3");
    expect(plugins["skills-only"]?.as).toBe("skills only");
    const pluginA = path.join(options.root, "plugins/plugin-a");
    expect(built.extensions).toEqual([pluginA]);
    const manifest = JSON.parse(readFileSync(path.join(pluginA, "package.json"), "utf8"));
    expect(manifest.omp.extensions).toEqual(["dist/a.js"]);
    expect(existsSync(path.join(pluginA, "dist/a.js"))).toBe(true);
    expect(lstatSync(path.join(pluginA, "dist/skills")).isSymbolicLink()).toBe(true);
    expect(realpathSync(path.join(pluginA, "dist/skills"))).toBe(realpathSync(built.skillsDir));

    // Built again, every build is the cache's.
    const again = await buildContext(c, arm, options);
    expect(readFileSync(again.appendSystemPrompt ?? "", "utf8")).toBe(
      readFileSync(built.appendSystemPrompt ?? "", "utf8")
    );
    const kinds = again.record.builds.filter((build) => build.kind !== "tree");
    expect(kinds.every((build) => build.hit)).toBe(true);
    expect(kinds.some((build) => build.kind === "compose")).toBe(true);
  }, 300_000);

  test("a controller's role prompt is the parts of a headless controller, and the instructions file follows it", async () => {
    const root = path.join(dir, "controller");
    const c = aCase("legion:acme:controller", {
      plugins: [
        { repo: "acme/legion", commit: COMMIT, path: "plugins/skills-only", name: "skills-only" },
      ],
      contextFiles: [{ repo: "acme/infra", commit: COMMIT, path: "legion/instructions/acme.md" }],
    });
    const { options, arm } = setUp(root, c);
    const built = await buildContext(c, arm, options);
    expect(readFileSync(built.appendSystemPrompt ?? "", "utf8")).toBe(
      "Controller part.\nHeadless controller part.\n\n# Acme instructions\n\nWork the acme way."
    );
    expect(built.record.rolePrompt).toMatchObject({
      role: "controller",
      isRoot: false,
      parts: [
        "packages/daemon/internal/prompts/roles/controller-root.md",
        "packages/daemon/internal/prompts/go/controller-headless.md",
      ],
    });
  }, 300_000);
});

// A SKILL.md's frontmatter as Oh My Pi 18.18.6 reads it (packages/utils/src/frontmatter.ts at the
// pin): a block plain YAML refuses is repaired, or read line by line, never a throw.
test("a description holding ': ' in a plain scalar is read whole, as Oh My Pi repairs it", () => {
  const text =
    "---\nname: lookup\ndescription: Use to look things up. Examples: current events, prices\n---\n\n# Lookup\n";
  expect(() =>
    Bun.YAML.parse("description: Use to look things up. Examples: current events")
  ).toThrow();
  expect(readFrontmatter(text)).toEqual({
    name: "lookup",
    description: "Use to look things up. Examples: current events, prices",
  });
  writeTree(path.join(dir, "colon"), { "SKILL.md": text });
  expect(skillFrontmatter(path.join(dir, "colon"))).toEqual({
    description: "Use to look things up. Examples: current events, prices",
    hidden: false,
  });
});

test("a tab in the frontmatter is read as two spaces, and kebab-case keys as camelCase", () => {
  const text =
    "---\r\nname: tabbed\r\nmetadata:\r\n\tkind: made-up\r\ndescription: Tabbed.\r\ndisable-model-invocation: true\r\n---\r\n";
  expect(readFrontmatter(text)).toEqual({
    name: "tabbed",
    metadata: { kind: "made-up" },
    description: "Tabbed.",
    disableModelInvocation: true,
  });
});

test("a block no repair parses is read one key: value line at a time, a value YAML refuses kept as its text", () => {
  const text =
    "<!-- a made-up comment -->\n---\nname: broken\ndescription: [unclosed: list\nhide-me: x\n  nested: [also\ndisable-model-invocation: true\n---\nBody.\n";
  expect(readFrontmatter(text)).toEqual({
    name: "broken",
    description: "[unclosed: list",
    hideMe: "x",
    disableModelInvocation: true,
  });
  writeTree(path.join(dir, "broken"), { "SKILL.md": text });
  expect(skillFrontmatter(path.join(dir, "broken"))).toEqual({
    description: "[unclosed: list",
    hidden: true,
  });
  expect(readFrontmatter("# No frontmatter\n")).toEqual({});
});

test("comments inside the block are dropped in one left-to-right pass, as Oh My Pi drops them", () => {
  const text =
    "---\nname: x<!-- gone -->y\ndescription: keep <!<!---->-- this\nnote: open <!-- no end\n---\nBody.\n";
  expect(readFrontmatter(text)).toEqual({
    name: "xy",
    description: "keep <!-- this",
    note: "open <!-- no end",
  });
});

/** A stand-in `gh` that answers its first `fails` calls with a network timeout on stderr, then
 *  with `answer`; each call is counted in the file `count`. */
function standinGh(name: string, fails: number, answer: string): { gh: string; count: string } {
  const root = path.join(dir, name);
  const count = path.join(root, "count");
  const gh = path.join(root, "gh");
  writeTree(root, {
    gh: `#!/bin/sh
n=$(cat ${JSON.stringify(count)} 2>/dev/null || echo 0)
n=$((n + 1))
echo "$n" > ${JSON.stringify(count)}
if [ "$n" -le ${fails} ]; then
  echo 'Get "https://api.example.test/repos/acme/project/contents/AGENTS.md": dial tcp: lookup api.example.test: i/o timeout' >&2
  exit 1
fi
printf '%s' ${JSON.stringify(answer)}
`,
  });
  chmodSync(gh, 0o755);
  return { gh, count };
}

test("a gh read that times out twice is sent again after 5 s and 15 s, as copies.ts's reads are, and answers; the source counts both retries", async () => {
  const { gh, count } = standinGh("gh-twice", 2, "# Project");
  const waits: number[] = [];
  const said: string[] = [];
  const source = ghSource(gh, {
    sleep: async (ms) => void waits.push(ms),
    err: (line) => void said.push(line),
  });
  const bytes = await source.file("acme/project", COMMIT, "AGENTS.md");
  expect(new TextDecoder().decode(bytes)).toBe("# Project");
  expect(readFileSync(count, "utf8").trim()).toBe("3");
  expect(waits).toEqual([5_000, 15_000]);
  expect(said).toHaveLength(2);
  expect(said[1]).toEndWith("sending it again in 15 s, retry 2 of 4");
  expect(source.reads?.()).toMatchObject({ calls: 3, retries: { "i/o timeout": 2 } });
});

test("a gh read that always fails stops after 4 tries more, naming the command and each try's error", async () => {
  const { gh, count } = standinGh("gh-always", 99, "");
  const waits: number[] = [];
  const source = ghSource(gh, { sleep: async (ms) => void waits.push(ms), err: () => {} });
  const failed = await source
    .file("acme/project", COMMIT, "AGENTS.md")
    .then(() => "answered")
    .catch((error: unknown) => String(error));
  expect(failed).toContain(
    `\`${gh} api --method GET -H Accept: application/vnd.github.raw repos/acme/project/contents/AGENTS.md?ref=${COMMIT}\` failed: 5 tries: `
  );
  for (const n of [1, 2, 3, 4, 5]) expect(failed).toContain(`try ${n}: Get "https://`);
  expect(failed.match(/i\/o timeout/g)?.length).toBe(5);
  expect(readFileSync(count, "utf8").trim()).toBe("5");
  expect(waits).toEqual([5_000, 15_000, 45_000, 135_000]);
});

test("a gh read that fails for good, such as a 404, is NoSuchFile at once: one call, no wait", async () => {
  const root = path.join(dir, "gh-missing");
  const count = path.join(root, "count");
  writeTree(root, {
    gh: `#!/bin/sh\necho x >> ${JSON.stringify(count)}\necho 'gh: Not Found (HTTP 404)' >&2\nexit 1\n`,
  });
  chmodSync(path.join(root, "gh"), 0o755);
  const waits: number[] = [];
  const said: string[] = [];
  const source = ghSource(path.join(root, "gh"), {
    sleep: async (ms) => void waits.push(ms),
    err: (line) => void said.push(line),
  });
  const failed = await source
    .file("acme/project", COMMIT, "missing.md")
    .catch((error: unknown) => error);
  // A 404 is a file the commit does not hold, told apart from a read that failed.
  expect(failed).toBeInstanceOf(NoSuchFile);
  expect(String(failed)).toContain("failed: gh: Not Found (HTTP 404)");
  expect(readFileSync(count, "utf8")).toBe("x\n");
  expect(waits).toEqual([]);
  expect(said).toEqual([]);
  expect(source.reads?.().calls).toBe(1);
  expect(source.reads?.().retries).toEqual({});
});

// Catches: a read of prompts.go that failed taken as a repository that holds none.
test("a Legion context's role prompt: a plugin repository that holds no prompts.go is passed over, and a read of it that failed stops the context, naming why", async () => {
  const root = path.join(dir, "prompts-read");
  const c = aCase("legion:acme:implementer", {
    plugins: [
      { repo: "acme/legion", commit: COMMIT, path: "plugins/skills-only", name: "skills-only" },
    ],
    contextFiles: [],
  });
  const { options, arm } = setUp(root, c);
  const local = options.source;
  /** The local source, but its read of prompts.go throws `error`. */
  const failing = (error: Error): RepoSource => ({
    ...local,
    name: "a source whose read of prompts.go fails",
    file: (repo, commit, file) =>
      file === PROMPTS_GO ? Promise.reject(error) : local.file(repo, commit, file),
  });
  const failed = await buildContext(c, arm, {
    ...options,
    source: failing(new Error("gh: connection reset by peer")),
  }).catch(String);
  expect(failed).toContain("gh: connection reset by peer");
  expect(failed).not.toContain("no plugin repository holds");
  const absent = await buildContext(c, arm, {
    ...options,
    source: failing(new NoSuchFile("acme/legion holds no prompts.go")),
  }).catch(String);
  expect(absent).toContain(`no plugin repository holds ${PROMPTS_GO}`);
});

// Catches: a plugin whose repository read failed left out of one arm's context while the next
// arm, reading it again, installs it; and a left-out plugin whose reason does not say which step
// failed.
test("a plugin whose repository read fails stops the context, naming why; a plugin that cannot be installed is left out, its reason naming the step that failed", async () => {
  const root = path.join(dir, "plugin-reads");
  const c = aCase("devbox", {
    plugins: [
      { repo: "acme/legion", commit: COMMIT, path: "packages/plugin-b", name: "plugin-b" },
      { repo: "acme/legion", commit: COMMIT, path: "plugins/broken", name: "broken" },
    ],
    contextFiles: [],
  });
  const { options, arm } = setUp(root, c);
  writeTree(path.join(root, "repos", "acme/legion", COMMIT), {
    "plugins/broken/package.json": "{ not JSON",
  });
  const local = options.source;
  const unread: RepoSource = {
    ...local,
    name: "a source whose read of acme/legion fails",
    tree: (repo, commit, inside, dest) =>
      repo === "acme/legion"
        ? Promise.reject(new Error("gh: connection reset by peer"))
        : local.tree(repo, commit, inside, dest),
  };
  const failed = await buildContext(c, arm, { ...options, source: unread }).then(
    () => "built",
    String
  );
  expect(failed).toContain("plugin-b");
  expect(failed).toContain("gh: connection reset by peer");
  const built = await buildContext(c, arm, options);
  const reasons = Object.fromEntries(built.record.plugins.map((p) => [p.name, p.reason]));
  expect(built.record.plugins.map((p) => p.as)).toEqual(["left out", "left out"]);
  expect(reasons["plugin-b"]).toStartWith("its build failed: ");
  expect(reasons["plugin-b"]).toContain("exit 3");
  expect(reasons.broken).toStartWith("its package.json does not parse: ");
}, 120_000);

/** A file of the stand-in repository: its path, its bytes, and whether a GraphQL batch calls it
 *  binary. */
interface StandinFile {
  readonly path: string;
  readonly bytes: Uint8Array;
  readonly binary?: boolean;
}

/**
 * A stand-in `gh` holding one made-up repository at one commit, a Bun script that logs each call's
 * arguments as one JSON line to its `log` and answers as GitHub does: the recursive tree; a
 * GraphQL batch of blobs by oid (a binary file as `isBinary`, any other as its text decoded as
 * UTF-8, as GitHub decodes it, so bytes that are no UTF-8 come back changed); and a blob alone.
 */
function standinRepoGh(name: string, files: readonly StandinFile[]): { gh: string; log: string } {
  const root = path.join(dir, name);
  const log = path.join(root, "calls.jsonl");
  const gh = path.join(root, "gh");
  const listed = files.map((file) => ({
    path: file.path,
    sha: gitBlobId(file.bytes),
    size: file.bytes.length,
    b64: Buffer.from(file.bytes).toString("base64"),
    binary: file.binary === true,
  }));
  writeTree(root, {
    gh: `#!${process.execPath}
import { appendFileSync } from "node:fs";
const files = ${JSON.stringify(listed)};
const args = process.argv.slice(2);
appendFileSync(${JSON.stringify(log)}, JSON.stringify(args) + "\\n");
const route = args.at(-1) ?? "";
if (args[1] === "graphql") {
  const query = (args.find((arg) => arg.startsWith("query=")) ?? "").slice(6);
  const repository = {};
  for (const match of query.matchAll(/b(\\d+): object\\(oid: "([0-9a-f]{40})"\\)/g)) {
    const file = files.find((each) => each.sha === match[2]);
    repository["b" + match[1]] = file === undefined ? null : file.binary
      ? { isBinary: true, isTruncated: false, text: null }
      : { isBinary: false, isTruncated: false, text: Buffer.from(file.b64, "base64").toString("utf8") };
  }
  process.stdout.write(JSON.stringify({ data: { repository } }));
} else if (route.includes("/git/trees/")) {
  process.stdout.write(JSON.stringify({
    tree: files.map((file) => ({ path: file.path, mode: "100644", type: "blob", sha: file.sha, size: file.size })),
    truncated: false,
  }));
} else if (/\\/git\\/blobs\\/[0-9a-f]{40}$/.test(route)) {
  const file = files.find((each) => route.endsWith(each.sha));
  process.stdout.write(Buffer.from(file.b64, "base64"));
} else {
  process.stderr.write("gh: Not Found (HTTP 404)\\n");
  process.exit(1);
}
`,
  });
  chmodSync(gh, 0o755);
  return { gh, log };
}

/** The stand-in's calls, by kind. */
function callsOf(log: string): { tree: number; graphql: number; blob: number; other: number } {
  const calls = readFileSync(log, "utf8")
    .split("\n")
    .filter((line) => line !== "")
    .map((line) => JSON.parse(line) as string[]);
  const count = (test: (args: string[]) => boolean) => calls.filter(test).length;
  const tree = count((args) => (args.at(-1) ?? "").includes("/git/trees/"));
  const graphql = count((args) => args[1] === "graphql");
  const blob = count((args) => (args.at(-1) ?? "").includes("/git/blobs/"));
  return { tree, graphql, blob, other: calls.length - tree - graphql - blob };
}

/** A case of alpha whose profile also installs each of `dirs` of acme/batch at COMMIT, and its old
 *  arm, under `root`. */
function batchCase(root: string, dirs: readonly string[]): { c: Case; arm: Arm } {
  const c = aCase("devbox", {
    skills: [
      { repo: "acme/skills", commit: COMMIT, path: "skills/alpha", name: "alpha" },
      ...dirs.map((each) => ({
        repo: "acme/batch",
        commit: COMMIT,
        path: each,
        name: path.basename(each),
      })),
    ],
    contextFiles: [],
  });
  writeTree(path.join(root, "old"), { "SKILL.md": ALPHA_ARM });
  const arm = buildArm(
    c,
    "old",
    { old: path.join(root, "old"), new: path.join(root, "old") },
    root
  );
  return { c, arm };
}

test("a profile of N files at one commit is read with its tree and ceil(N/50) GraphQL batches, byte for byte", async () => {
  const dirs = ["skills/s1", "skills/s2", "skills/s3"];
  const files: StandinFile[] = dirs.flatMap((each) => [
    {
      path: `${each}/SKILL.md`,
      bytes: new TextEncoder().encode(
        `---\nname: ${path.basename(each)}\ndescription: Made-up ${each}.\n---\n`
      ),
    },
    ...Array.from({ length: 39 }, (_, index) => ({
      path: `${each}/references/r${index}.md`,
      bytes: new TextEncoder().encode(`Reference ${index} of ${each}, é ✓.\n`),
    })),
  ]);
  const { gh, log } = standinRepoGh("gh-batch", files);
  const root = path.join(dir, "batch");
  const { c, arm } = batchCase(root, dirs);
  const options = {
    source: ghSource(gh),
    cacheDir: path.join(root, "cache"),
    root: path.join(root, "scratch"),
  };
  const built = await buildContext(c, arm, options);
  // The build wrote exactly the profile's 120 files under the three skills' directories.
  const written = dirs.flatMap((each) => {
    const skill = path.join(built.skillsDir, path.basename(each));
    return readdirSync(skill, { recursive: true, encoding: "utf8" })
      .filter((name) => statSync(path.join(skill, name)).isFile())
      .map((name) => `${each}/${name}`);
  });
  expect(written.toSorted()).toEqual(files.map((file) => file.path).toSorted());
  expect(callsOf(log)).toEqual({ tree: 1, graphql: Math.ceil(120 / 50), blob: 0, other: 0 });
  for (const file of files) {
    const at = path.join(built.skillsDir, file.path.replace(/^skills\//, ""));
    expect([...readFileSync(at)]).toEqual([...file.bytes]);
  }
  // Built again from the cache, it reads nothing.
  await buildContext(c, arm, options);
  expect(callsOf(log)).toEqual({ tree: 1, graphql: 3, blob: 0, other: 0 });
});

test("a blob a batch does not give whole, binary or no UTF-8, is read alone over REST, byte for byte", async () => {
  const files: StandinFile[] = [
    {
      path: "skills/mixed/SKILL.md",
      bytes: new TextEncoder().encode("---\nname: mixed\ndescription: Made-up mixed.\n---\n"),
    },
    { path: "skills/mixed/notes.md", bytes: new TextEncoder().encode("Plain notes.\n") },
    { path: "skills/mixed/logo.bin", bytes: new Uint8Array([0, 1, 2, 255]), binary: true },
    { path: "skills/mixed/latin.txt", bytes: new Uint8Array([0x63, 0x61, 0x66, 0xe9, 0x0a]) },
  ];
  const { gh, log } = standinRepoGh("gh-mixed", files);
  const root = path.join(dir, "mixed");
  const { c, arm } = batchCase(root, ["skills/mixed"]);
  const built = await buildContext(c, arm, {
    source: ghSource(gh),
    cacheDir: path.join(root, "cache"),
    root: path.join(root, "scratch"),
  });
  expect(callsOf(log)).toEqual({ tree: 1, graphql: 1, blob: 2, other: 0 });
  for (const file of files) {
    const at = path.join(built.skillsDir, file.path.replace(/^skills\//, ""));
    expect([...readFileSync(at)]).toEqual([...file.bytes]);
  }
});

test("a tree read that failed is not kept: the next context at that commit reads it again, and the source counts each gh call", async () => {
  const bytes = new TextEncoder().encode("---\nname: again\ndescription: Made-up again.\n---\n");
  const inner = standinRepoGh("gh-again-inner", [{ path: "skills/again/SKILL.md", bytes }]);
  const root = path.join(dir, "again");
  const failedOnce = path.join(root, "tree-failed-once");
  // A gh whose first tree read fails for good (a 404); every other call goes to the stand-in.
  writeTree(root, {
    gh: `#!/bin/sh
case "$*" in *git/trees/*)
  if [ ! -e ${JSON.stringify(failedOnce)} ]; then
    : > ${JSON.stringify(failedOnce)}
    echo 'gh: Not Found (HTTP 404)' >&2
    exit 1
  fi;;
esac
exec ${JSON.stringify(inner.gh)} "$@"
`,
  });
  chmodSync(path.join(root, "gh"), 0o755);
  const { c, arm } = batchCase(root, ["skills/again"]);
  const source = ghSource(path.join(root, "gh"));
  const options = { source, cacheDir: path.join(root, "cache"), root: path.join(root, "scratch") };
  const first = await buildContext(c, arm, options).then(
    () => "built",
    (error: unknown) => String(error)
  );
  expect(first).toContain("gh: Not Found (HTTP 404)");
  const built = await buildContext(c, arm, options);
  expect([...readFileSync(path.join(built.skillsDir, "again/SKILL.md"))]).toEqual([...bytes]);
  expect(callsOf(inner.log)).toEqual({ tree: 1, graphql: 1, blob: 0, other: 0 });
  // The failed tree read, the tree read again and one GraphQL batch.
  expect(source.reads?.().calls).toBe(3);
});

/** A made-up skill's SKILL.md at `dir`, and one reference beside it. */
const skillFiles = (dir: string): StandinFile[] => [
  {
    path: `${dir}/SKILL.md`,
    bytes: new TextEncoder().encode(
      `---\nname: ${path.basename(dir)}\ndescription: Made-up ${dir}.\n---\n`
    ),
  },
  { path: `${dir}/notes.md`, bytes: new TextEncoder().encode(`Notes of ${dir}.\n`) },
];

// Catches: a blob read ahead and held for the life of the source when its skill's files are never
// written (the build stops first, or another capture made the skill's cache entry first), or
// dropped before its last file is written (a skill directory inside another).
test("each blob read ahead is dropped once its last file is written, or once the context's skills are installed or it fails, on every path", async () => {
  // The build stops between the read ahead and the second skill's files: two skills named dup.
  const twice = standinRepoGh("gh-held-twice", [
    ...skillFiles("skills/dup"),
    ...skillFiles("other/dup"),
  ]);
  const twiceSource = ghSource(twice.gh);
  const twiceRoot = path.join(dir, "held-twice");
  const { c: twiceCase, arm: twiceArm } = batchCase(twiceRoot, ["skills/dup", "other/dup"]);
  const stopped = await buildContext(twiceCase, twiceArm, {
    source: twiceSource,
    cacheDir: path.join(twiceRoot, "cache"),
    root: path.join(twiceRoot, "scratch"),
  }).then(() => "built", String);
  expect(stopped).toContain("installs dup twice");
  expect(twiceSource.held()).toBe(0);

  // Another capture makes the skill's cache entry while this one reads ahead, so its tree never
  // runs here.
  const madeRoot = path.join(dir, "held-made");
  const made = standinRepoGh("gh-held-made-inner", skillFiles("skills/made"));
  const entry = path.join(
    madeRoot,
    "cache",
    "skills",
    cacheKey("acme/batch", COMMIT, "skills/made")
  );
  const staged = path.join(madeRoot, "staged");
  writeTree(staged, { "SKILL.md": "---\nname: made\ndescription: Made-up made.\n---\n" });
  writeTree(madeRoot, {
    gh: `#!/bin/sh
case "$*" in *graphql*)
  if [ ! -e ${JSON.stringify(entry)} ]; then
    mkdir -p ${JSON.stringify(path.dirname(entry))}
    cp -R ${JSON.stringify(staged)} ${JSON.stringify(entry)}
  fi;;
esac
exec ${JSON.stringify(made.gh)} "$@"
`,
  });
  chmodSync(path.join(madeRoot, "gh"), 0o755);
  const madeSource = ghSource(path.join(madeRoot, "gh"));
  const { c: madeCase, arm: madeArm } = batchCase(madeRoot, ["skills/made"]);
  const built = await buildContext(madeCase, madeArm, {
    source: madeSource,
    cacheDir: path.join(madeRoot, "cache"),
    root: path.join(madeRoot, "scratch"),
  });
  expect(built.record.builds.find((each) => each.kind === "skill")?.hit).toBe(true);
  expect(madeSource.held()).toBe(0);

  // A skill directory inside another: each file of the inner one is written twice, once for each.
  const nested = standinRepoGh("gh-held-nested", [
    ...skillFiles("skills/outer"),
    ...skillFiles("skills/outer/inner"),
  ]);
  const nestedSource = ghSource(nested.gh);
  const nestedRoot = path.join(dir, "held-nested");
  const { c: nestedCase, arm: nestedArm } = batchCase(nestedRoot, [
    "skills/outer",
    "skills/outer/inner",
  ]);
  await buildContext(nestedCase, nestedArm, {
    source: nestedSource,
    cacheDir: path.join(nestedRoot, "cache"),
    root: path.join(nestedRoot, "scratch"),
  });
  // Each blob came from the batch: none was read alone because it was dropped too soon.
  expect(callsOf(nested.log)).toEqual({ tree: 1, graphql: 1, blob: 0, other: 0 });
  expect(nestedSource.held()).toBe(0);
}, 120_000);

// Catches: one transport error on a GitHub read failing the context, where copies.ts sends the
// read again (a build-cases re-profile failed so on 2026-11-21).
test("a tree read that fails once with a TLS handshake timeout is sent again 5 s later, as copies.ts's reads are, and the context record and the source count the retry", async () => {
  const bytes = new TextEncoder().encode("---\nname: flaky\ndescription: Made-up flaky.\n---\n");
  const inner = standinRepoGh("gh-flaky-inner", [{ path: "skills/flaky/SKILL.md", bytes }]);
  const root = path.join(dir, "flaky");
  const failedOnce = path.join(root, "tree-failed-once");
  const timeout = `Get "https://api.example.test/repos/acme/batch/git/trees/${COMMIT}?recursive=1": net/http: TLS handshake timeout`;
  // A gh whose first tree read fails as the real read did; every other call goes to the stand-in.
  writeTree(root, {
    gh: `#!/bin/sh
case "$*" in *git/trees/*)
  if [ ! -e ${JSON.stringify(failedOnce)} ]; then
    : > ${JSON.stringify(failedOnce)}
    echo ${JSON.stringify(timeout)} >&2
    exit 1
  fi;;
esac
exec ${JSON.stringify(inner.gh)} "$@"
`,
  });
  chmodSync(path.join(root, "gh"), 0o755);
  const { c, arm } = batchCase(root, ["skills/flaky"]);
  const waits: number[] = [];
  const said: string[] = [];
  const source = ghSource(path.join(root, "gh"), {
    sleep: async (ms) => void waits.push(ms),
    err: (line) => void said.push(line),
  });
  const built = await buildContext(c, arm, {
    source,
    cacheDir: path.join(root, "cache"),
    root: path.join(root, "scratch"),
  });
  expect([...readFileSync(path.join(built.skillsDir, "flaky/SKILL.md"))]).toEqual([...bytes]);
  expect(waits).toEqual([5_000]);
  expect(said).toEqual([
    `context: a gh call failed (${timeout}); sending it again in 5 s, retry 1 of 4`,
  ]);
  expect(built.record.githubRetries).toEqual({ "TLS handshake timeout": 1 });
  // The failed tree read, the tree read again and one GraphQL batch.
  expect(source.reads?.()).toMatchObject({ calls: 3, retries: { "TLS handshake timeout": 1 } });
});

/** A file of the made-up acme/deep: its path, its mode (git's: 100755 executable, 120000 a link
 *  whose target is its text) and its bytes. */
interface DeepFile {
  readonly path: string;
  readonly mode: "100644" | "100755" | "120000";
  readonly bytes: Uint8Array;
}

const text = (value: string) => new TextEncoder().encode(value);

/** acme/deep at COMMIT: a skill directory with an executable, a link, a nested directory and bytes
 *  that are no UTF-8, beside a sibling skill and the rest of a large repository. */
const DEEP_FILES: readonly DeepFile[] = [
  { path: "README.md", mode: "100644", bytes: text("# Deep\n") },
  { path: "data/proofs/p1.v", mode: "100644", bytes: text("Lemma one : True.\n") },
  { path: "skills/other/SKILL.md", mode: "100644", bytes: text("---\nname: other\n---\n") },
  {
    path: "skills/deep/SKILL.md",
    mode: "100644",
    bytes: text("---\nname: deep\ndescription: Made-up deep.\n---\n"),
  },
  { path: "skills/deep/scripts/run.sh", mode: "100755", bytes: text("#!/bin/sh\necho run\n") },
  { path: "skills/deep/link.md", mode: "120000", bytes: text("SKILL.md") },
  { path: "skills/deep/references/a/b.md", mode: "100644", bytes: text("Reference b.\n") },
  { path: "skills/deep/latin.bin", mode: "100644", bytes: new Uint8Array([0, 0x63, 0xe9, 255]) },
];

/** The made-up id of the tree of `dir` ("" the root) in the stand-in. */
const treeId = (dir: string) => gitBlobId(text(`tree ${dir}`));

/**
 * A stand-in `gh` holding one made-up repository (`files`) at COMMIT under any name, whose recursive tree GitHub cuts short, a
 * Bun script that logs each call's arguments as one JSON line to its `log` and answers as GitHub
 * does: a tree's recursive listing, cut short with its first entry only for the commit and for
 * each directory of `cutShort`; one level of a tree, cut short so for each directory of
 * `cutAlone`; a blob alone; and the repository's tarball, made with `tar` from the files, so a
 * tarball read gives the same files and only the call log tells it apart.
 */
function standinDeepGh(
  name: string,
  files: readonly DeepFile[],
  cut: { readonly cutShort?: readonly string[]; readonly cutAlone?: readonly string[] } = {}
): { gh: string; log: string } {
  const root = path.join(dir, name);
  const log = path.join(root, "calls.jsonl");
  const gh = path.join(root, "gh");
  // Each tree's one level, by its id.
  const trees: Record<
    string,
    { name: string; mode: string; type: string; sha: string; size?: number }[]
  > = { [treeId("")]: [] };
  const blobs: Record<string, string> = {};
  const top = path.join(root, "tarball", `acme-deep-${COMMIT.slice(0, 7)}`);
  for (const file of files) {
    const parts = file.path.split("/");
    for (let depth = 1; depth <= parts.length; depth++) {
      const parent = treeId(parts.slice(0, depth - 1).join("/"));
      const here = parts.slice(0, depth).join("/");
      const level = trees[parent] ?? [];
      trees[parent] = level;
      if (level.some((entry) => entry.name === parts[depth - 1])) continue;
      if (depth < parts.length) {
        level.push({
          name: parts[depth - 1] ?? "",
          mode: "040000",
          type: "tree",
          sha: treeId(here),
        });
        trees[treeId(here)] ??= [];
      } else {
        const sha = gitBlobId(file.bytes);
        level.push({
          name: parts[depth - 1] ?? "",
          mode: file.mode,
          type: "blob",
          sha,
          size: file.bytes.length,
        });
        blobs[sha] = Buffer.from(file.bytes).toString("base64");
      }
    }
    const at = path.join(top, file.path);
    mkdirSync(path.dirname(at), { recursive: true });
    if (file.mode === "120000") symlinkSync(new TextDecoder().decode(file.bytes), at);
    else {
      writeFileSync(at, file.bytes);
      if (file.mode === "100755") chmodSync(at, 0o755);
    }
  }
  const archive = path.join(root, "repository.tar.gz");
  const tar = Bun.spawnSync(["tar", "-czf", archive, "-C", path.dirname(top), path.basename(top)]);
  if (tar.exitCode !== 0) throw new Error(`tar failed: ${tar.stderr.toString()}`);
  const data = {
    commit: COMMIT,
    root: treeId(""),
    trees,
    blobs,
    cutShort: (cut.cutShort ?? []).map(treeId),
    cutAlone: (cut.cutAlone ?? []).map(treeId),
  };
  writeTree(root, {
    gh: `#!${process.execPath}
import { appendFileSync, readFileSync } from "node:fs";
const data = ${JSON.stringify(data)};
const args = process.argv.slice(2);
appendFileSync(${JSON.stringify(log)}, JSON.stringify(args) + "\\n");
const route = args.at(-1) ?? "";
const notFound = () => {
  process.stderr.write("gh: Not Found (HTTP 404)\\n");
  process.exit(1);
};
const listed = (entry, at) => ({ path: at, mode: entry.mode, type: entry.type, sha: entry.sha, ...(entry.size === undefined ? {} : { size: entry.size }) });
const tree = /^repos\\/[^/]+\\/[^/]+\\/git\\/trees\\/([0-9a-f]{40})(\\?recursive=1)?$/.exec(route);
const blob = /^repos\\/[^/]+\\/[^/]+\\/git\\/blobs\\/([0-9a-f]{40})$/.exec(route);
if (tree !== null) {
  const id = tree[1] === data.commit ? data.root : tree[1];
  const level = data.trees[id];
  if (level === undefined) notFound();
  if (tree[2] !== undefined) {
    const all = [];
    const walk = (sha, prefix) => {
      for (const entry of data.trees[sha]) {
        all.push(listed(entry, prefix + entry.name));
        if (entry.type === "tree") walk(entry.sha, prefix + entry.name + "/");
      }
    };
    walk(id, "");
    const truncated = tree[1] === data.commit || data.cutShort.includes(id);
    process.stdout.write(JSON.stringify({ sha: id, tree: truncated ? all.slice(0, 1) : all, truncated }));
  } else {
    const truncated = data.cutAlone.includes(id);
    const entries = level.map((entry) => listed(entry, entry.name));
    process.stdout.write(JSON.stringify({ sha: id, tree: truncated ? entries.slice(0, 1) : entries, truncated }));
  }
} else if (blob !== null && data.blobs[blob[1]] !== undefined) {
  process.stdout.write(Buffer.from(data.blobs[blob[1]], "base64"));
} else if (/^repos\\/[^/]+\\/[^/]+\\/tarball\\/[0-9a-f]{40}$/.test(route) && route.endsWith(data.commit)) {
  process.stdout.write(readFileSync(${JSON.stringify(archive)}));
} else notFound();
`,
  });
  chmodSync(gh, 0o755);
  return { gh, log };
}

/** The route of each call the stand-in logged, in order. */
function routesOf(log: string): string[] {
  return readFileSync(log, "utf8")
    .split("\n")
    .filter((line) => line !== "")
    .map((line) => (JSON.parse(line) as string[]).at(-1) ?? "");
}

/** The routes of `log` by kind: tree reads (recursive and one level), blobs, tarballs, other. */
function routeKinds(log: string): Record<string, number> {
  const kinds = { recursive: 0, level: 0, blob: 0, tarball: 0, other: 0 };
  for (const route of routesOf(log)) {
    if (/\/git\/trees\/[0-9a-f]{40}\?recursive=1$/.test(route)) kinds.recursive++;
    else if (/\/git\/trees\/[0-9a-f]{40}$/.test(route)) kinds.level++;
    else if (route.includes("/git/blobs/")) kinds.blob++;
    else if (route.includes("/tarball/")) kinds.tarball++;
    else kinds.other++;
  }
  return kinds;
}

/** Each file under `root`, by its path below it: its mode as git writes it (120000 a link, its
 *  bytes the target) and its bytes. */
function filesUnder(root: string): Record<string, { mode: string; bytes: number[] }> {
  const found: Record<string, { mode: string; bytes: number[] }> = {};
  for (const name of readdirSync(root, { recursive: true, encoding: "utf8" })) {
    const at = path.join(root, name);
    const stat = lstatSync(at);
    if (stat.isDirectory()) continue;
    found[name] = stat.isSymbolicLink()
      ? { mode: "120000", bytes: [...text(readlinkSync(at))] }
      : { mode: (stat.mode & 0o111) !== 0 ? "100755" : "100644", bytes: [...readFileSync(at)] };
  }
  return found;
}

/** What `filesUnder` gives for the files of `files` under `inside`. */
function expectedUnder(
  files: readonly DeepFile[],
  inside: string
): Record<string, { mode: string; bytes: number[] }> {
  return Object.fromEntries(
    files
      .filter((file) => file.path.startsWith(`${inside}/`))
      .map((file) => [
        file.path.slice(inside.length + 1),
        { mode: file.mode, bytes: [...file.bytes] },
      ])
  );
}

// Catches: a commit whose recursive tree GitHub cuts short read from the whole repository's
// tarball to write one directory (3.26 GB for one skill of a large proof repository, 2026-11-22).
test("a directory of a commit whose tree GitHub cuts short is read along its path and written with its modes, links and bytes, with no tarball read", async () => {
  const { gh, log } = standinDeepGh("gh-deep", DEEP_FILES);
  const root = path.join(dir, "deep");
  const { c, arm } = batchCase(root, ["skills/deep"]);
  const source = ghSource(gh, { sleep: async () => {}, err: () => {} });
  const options = { source, cacheDir: path.join(root, "cache"), root: path.join(root, "scratch") };
  const built = await buildContext(c, arm, options);
  expect(routeKinds(log)).toEqual({ recursive: 2, level: 2, blob: 5, tarball: 0, other: 0 });
  const key = cacheKey("acme/batch", COMMIT, "skills/deep");
  // The cache holds that one entry, and it holds exactly the directory's files.
  expect(readdirSync(path.join(options.cacheDir, "skills"))).toEqual([key]);
  expect(filesUnder(path.join(options.cacheDir, "skills", key))).toEqual(
    expectedUnder(DEEP_FILES, "skills/deep")
  );
  expect(filesUnder(path.join(built.skillsDir, "deep"))).toEqual(
    expectedUnder(DEEP_FILES, "skills/deep")
  );
  expect(source.reads?.().calls).toBe(9);
});

test("a directory whose own recursive tree GitHub cuts short too is walked one level at a time, with no tarball read", async () => {
  const { gh, log } = standinDeepGh("gh-deep-levels", DEEP_FILES, { cutShort: ["skills/deep"] });
  const source = ghSource(gh, { sleep: async () => {}, err: () => {} });
  const dest = path.join(dir, "deep-levels", "out", "deep");
  mkdirSync(path.dirname(dest), { recursive: true });
  await source.tree("acme/deep", COMMIT, "skills/deep", dest);
  expect(filesUnder(dest)).toEqual(expectedUnder(DEEP_FILES, "skills/deep"));
  // Nothing beside the directory is written.
  expect(readdirSync(path.dirname(dest))).toEqual(["deep"]);
  // The commit's tree and the directory's, cut short; the root and skills/ to find it; then its
  // four directories one level each: skills/deep, scripts, references and references/a.
  expect(routeKinds(log)).toEqual({ recursive: 2, level: 6, blob: 5, tarball: 0, other: 0 });
});

test("a directory the commit does not hold, or one a level of which GitHub cuts short even read alone, fails naming the repository, the commit and the path, with no tarball read", async () => {
  const missing = standinDeepGh("gh-deep-missing", DEEP_FILES);
  const missingSource = ghSource(missing.gh, { sleep: async () => {}, err: () => {} });
  const out = path.join(dir, "deep-missing");
  mkdirSync(out, { recursive: true });
  for (const inside of ["skills/none", "skills/deep/SKILL.md", "nowhere/deep"]) {
    const failed = await missingSource.tree("acme/deep", COMMIT, inside, path.join(out, "x")).then(
      () => "written",
      (error: unknown) => error
    );
    expect(failed).toBeInstanceOf(UnreadableDirectory);
    expect(failed).toMatchObject({ repo: "acme/deep", commit: COMMIT, dir: inside });
    expect(String(failed)).toContain(`acme/deep at ${COMMIT} holds no directory ${inside}`);
  }
  expect(routeKinds(missing.log).tarball).toBe(0);
  expect(existsSync(path.join(out, "x"))).toBe(false);

  const cut = standinDeepGh("gh-deep-cut", DEEP_FILES, {
    cutShort: ["skills/deep"],
    cutAlone: ["skills/deep/references"],
  });
  const cutSource = ghSource(cut.gh, { sleep: async () => {}, err: () => {} });
  const cutOut = path.join(dir, "deep-cut");
  mkdirSync(cutOut, { recursive: true });
  const failed = await cutSource
    .tree("acme/deep", COMMIT, "skills/deep", path.join(cutOut, "x"))
    .then(
      () => "written",
      (error: unknown) => error
    );
  expect(failed).toBeInstanceOf(UnreadableDirectory);
  expect(failed).toMatchObject({ repo: "acme/deep", commit: COMMIT, dir: "skills/deep" });
  expect(String(failed)).toContain(
    `acme/deep at ${COMMIT}: skills/deep cannot be read: GitHub cuts the tree of skills/deep/references short even one level at a time`
  );
  expect(routeKinds(cut.log).tarball).toBe(0);
});
