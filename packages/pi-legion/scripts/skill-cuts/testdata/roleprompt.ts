/**
 * A made-up plugin repository that holds a Legion daemon's role prompt composer, at two commits
 * whose implementer part differs by one line: the fixture of R8's arm, which composes each arm's
 * role prompt at a commit it names (cli.ts's `--old-role-prompt-at` and `--new-role-prompt-at`).
 * Written as context.ts's `localSource` reads repositories, `<root>/<owner>/<name>/<commit>/`.
 * The composer has the API of packages/daemon/internal/prompts (`New(stateDir)`,
 * `Compose(role, isRoot)`, the `roles/` parts snapshotted under prompts/shared and the `go/` parts
 * under prompts/go), as context.test.ts's made-up daemon has. Every name and line here is made up.
 */
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";

/** The made-up plugin repository. */
export const ROLE_REPO = "acme/legion";
/** Its commit at the branch's base, whose implementer part holds ROLE_LINE. */
export const BASE_COMMIT = "b".repeat(40);
/** Its commit at the head, whose implementer part is the base's less ROLE_LINE. */
export const HEAD_COMMIT = "c".repeat(40);
/** A commit the repository does not hold. */
export const MISSING_COMMIT = "d".repeat(40);
/** The part of the implementer's role prompt that differs between the two commits. */
export const ROLE_PART = "packages/daemon/internal/prompts/roles/implementer.md";
/** The line of ROLE_PART at BASE_COMMIT that HEAD_COMMIT leaves out: line 2 of 3. */
export const ROLE_LINE = "Keep each reply to one line.";
/** A plugin of the repository that installs nothing but skills (no package.json), so a profile
 *  that names it reads the repository and builds nothing. */
export const SKILLS_ONLY_PLUGIN = {
  repo: ROLE_REPO,
  commit: BASE_COMMIT,
  path: "plugins/skills-only",
  name: "skills-only",
} as const;

/** The parts Compose names for the implementer, in order. */
export const IMPLEMENTER_PARTS = [
  "packages/daemon/internal/prompts/roles/core/common.md",
  ROLE_PART,
  "packages/daemon/internal/prompts/go/worker.md",
] as const;

/** The implementer's role prompt composed at each commit: its parts joined, the trailing newlines
 *  dropped (context.ts's `rolePrompt`). */
export const COMPOSED = {
  base: `Core part.\nImplementer part.\n${ROLE_LINE}\nEnd with the word done.\nWorker part.`,
  head: "Core part.\nImplementer part.\nEnd with the word done.\nWorker part.",
} as const;

/** The made-up daemon module: its composer and the parts it embeds, but the implementer part. */
const DAEMON: Record<string, string> = {
  "packages/daemon/go.mod": "module example.com/acme/daemon\n\ngo 1.22\n",
  "packages/daemon/internal/claim/claim.go": `package claim

type Role string

const (
	RoleArchitect   Role = "architect"
	RoleImplementer Role = "implementer"
)
`,
  "packages/daemon/internal/prompts/prompts.go": `package prompts

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
	if role == claim.RoleImplementer {
		return Parts{RolePromptPaths: []string{filepath.Join(c.sharedDir, "core", "common.md"), filepath.Join(c.sharedDir, "implementer.md"), filepath.Join(c.goDir, "worker.md")}}, nil
	}
	return Parts{}, fmt.Errorf("unsupported role %q", role)
}

func (c *Composer) ControllerPromptPaths(headless bool) ([]string, error) {
	return []string{filepath.Join(c.goDir, "worker.md")}, nil
}
`,
  "packages/daemon/internal/prompts/roles/core/common.md": "Core part.\n",
  "packages/daemon/internal/prompts/go/worker.md": "Worker part.\n\n",
  "plugins/skills-only/README.md": "A plugin of skills alone.\n",
};

/** ROLE_PART at each commit. */
const IMPLEMENTER = {
  base: `Implementer part.\n${ROLE_LINE}\nEnd with the word done.\n`,
  head: "Implementer part.\nEnd with the word done.\n",
} as const;

/** Writes ROLE_REPO at BASE_COMMIT and HEAD_COMMIT under `root`, as `localSource` reads it. */
export function writeRolePromptRepository(root: string): void {
  for (const [commit, implementer] of [
    [BASE_COMMIT, IMPLEMENTER.base],
    [HEAD_COMMIT, IMPLEMENTER.head],
  ] as const) {
    const files = { ...DAEMON, [ROLE_PART]: implementer };
    for (const [file, text] of Object.entries(files)) {
      const where = path.join(root, ROLE_REPO, commit, file);
      mkdirSync(path.dirname(where), { recursive: true });
      writeFileSync(where, text);
    }
  }
}
