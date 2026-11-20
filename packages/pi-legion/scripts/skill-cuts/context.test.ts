import { describe, expect, test } from "bun:test";
import {
  existsSync,
  lstatSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  realpathSync,
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
  gitBlobId,
  legionRole,
  localSource,
  PROMPTS_GO,
  skillFrontmatter,
} from "./context";

// The case context (plan, "The case context") built from made-up repositories, read from local
// directories in place of `gh`: `acme/skills` (the skills alpha and beta), `acme/project` (an
// AGENTS.md), `acme/infra` (a project's instructions file) and `acme/legion`, which holds a
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
  writeTree(path.join(root, "acme/project", COMMIT), { "AGENTS.md": AGENTS });
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
  const c = aCase("devbox", {});
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
  // The repository's AGENTS.md is in the working directory, and a devbox has no role prompt.
  expect(readFileSync(path.join(again.workspace, "AGENTS.md"), "utf8")).toBe(AGENTS);
  expect(again.appendSystemPrompt).toBeUndefined();
  expect(again.record.rolePrompt).toBeNull();
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
