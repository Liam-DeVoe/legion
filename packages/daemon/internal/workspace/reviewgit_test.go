package workspace

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/lookpath"
)

// plantedPrograms are the programs a head can have git run, each named by the marker file it
// leaves when it runs: a diff driver's external command, a diff driver's text conversion, a hook
// and an fsmonitor command.
var plantedPrograms = []string{"diff", "fsmonitor", "hook", "textconv"}

// plantedConfig is git configuration that names each planted program, which writes its marker
// under markers when it runs: the external command and the text conversion of the diff driver
// `planted`, which the head's .gitattributes gives every .txt file, and the fsmonitor command;
// hooks, where it is set, is the hooks directory it points git at.
func plantedConfig(markers, hooks string) string {
	touch := func(name string) string { return "touch '" + filepath.Join(markers, name) + "'" }
	config := "[core]\n\tfsmonitor = \"" + touch("fsmonitor") + "; false\"\n"
	if hooks != "" {
		config += "\thooksPath = " + hooks + "\n"
	}
	return config + "[diff \"planted\"]\n\tcommand = \"" + touch("diff") + "; true\"\n\ttextconv = \"" + touch("textconv") + "; cat\"\n"
}

// plantReviewHooks writes into dir the hooks a reviewer's git reaches, post-checkout and
// reference-transaction, each leaving the hook marker.
func plantReviewHooks(t *testing.T, dir, markers string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"post-checkout", "reference-transaction"} {
		script := "#!/bin/sh\ntouch '" + filepath.Join(markers, "hook") + "'\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// writeFiles writes each file of files, by its path under dir, making its directory.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// markersLeft is every planted program's marker under markers, which it then clears for the
// next reading.
func markersLeft(t *testing.T, markers string) []string {
	t.Helper()
	entries, err := os.ReadDir(markers)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for _, entry := range entries {
		left = append(left, entry.Name())
		if err := os.Remove(filepath.Join(markers, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	return left
}

// probeValue is a configuration value the review commands pass with `-c` in front of their
// subcommand, and read back, so the shims' rebuilding of those words is exercised: it holds
// spaces and single quotes, which their quoting for eval must carry through unchanged.
const probeValue = "it's a 'quoted' value"

// reviewCommands are the git commands of a review, in the review clone (root) and in the
// repository the head commits under planted.git (embedded), which every git run below that
// directory reads as its own: the diffs of the change and of the working tree, the log, show,
// format-patch, whatchanged, blame and annotate that read through the diff machinery, a status, a
// checkout away and back, a ref update, one that names the planted hooks itself in front of its
// subcommand, which the reviewer's own settings after it must override, and a configuration read
// with global options in front of it. whatchanged is its words as the git under test runs it
// (whatchangedWords).
func reviewCommands(base, head, plantedHooks string, embedded bool, whatchanged []string) [][]string {
	blamed, at := "notes.txt", head
	if embedded {
		blamed, at = "../notes.txt", base
	}
	commands := [][]string{
		{"diff", base, at}, {"diff", base}, {"log", "-p", "-1"}, {"show", at},
		{"format-patch", "-1", "--stdout", at}, whatchanged, {"blame", blamed},
		{"annotate", blamed}, {"status"},
	}
	if !embedded {
		commands = append(commands, []string{"checkout", "--quiet", "--detach", base}, []string{"checkout", "--quiet", "--detach", head})
	}
	return append(commands,
		[]string{"branch", "--force", "review-probe", at},
		[]string{"-c", "core.hooksPath=" + plantedHooks, "branch", "--force", "review-probe", at},
		[]string{"-C", ".", "-c", "legion.probe=" + probeValue, "--no-pager", "config", "legion.probe"},
	)
}

// whatchangedRefusal is what a git that deprecates whatchanged answers a run of it that does not
// name --i-still-use-this (git 2.56.1 does; git 2.43 has no such option and runs it).
const whatchangedRefusal = "refusing to run without --i-still-use-this"

// whatchangedWords is `whatchanged -p -1` as git runs it in dir: with --i-still-use-this where
// git refuses it without, and as it is where git runs it. git reads that option wherever it
// stands, so the reviewer's --no-ext-diff and --no-textconv in front of it take nothing away. It
// answers too whether this git refuses the bare words.
func whatchangedWords(t *testing.T, run fixturegit.Runner, dir, git string) ([]string, bool) {
	t.Helper()
	bare := []string{"whatchanged", "-p", "-1"}
	output, err := run.Try(dir, append([]string{git}, bare...)...)
	switch {
	case err == nil:
		return bare, false
	case strings.Contains(output, whatchangedRefusal):
		return []string{"whatchanged", "--i-still-use-this", "-p", "-1"}, true
	}
	t.Fatalf("git whatchanged -p -1 in %s: %v\n%s", dir, err, output)
	return nil, false
}

// envGit is the git first on the PATH env names, which the row runs as the reviewer's own.
func envGit(t *testing.T, env []string) string {
	t.Helper()
	path := ""
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path = value
		}
	}
	git, ok := lookpath.On(path, "git")
	if !ok {
		t.Fatalf("no git on PATH=%s", path)
	}
	return git
}

// runReview runs commands with the git env's PATH names first, in dir, and answers each
// command's combined output by its words. Every command must succeed: one that failed would run
// no planted program either, and the row would read a refusal as a clean review.
func runReview(t *testing.T, env []string, dir string, commands [][]string) map[string]string {
	t.Helper()
	git := envGit(t, env)
	runner := fixturegit.Runner{TB: t, Env: env}
	outputs := map[string]string{}
	for _, command := range commands {
		output, err := runner.Try(dir, append([]string{git}, command...)...)
		if err != nil {
			t.Fatalf("git %q in %s: %v\n%s", command, dir, err, output)
		}
		outputs[strings.Join(command, " ")] = output
	}
	return outputs
}

// reviewCloneState is what another role's run could change of the review clone: its git
// configuration, its hooks, its HEAD and the tracked files of its working tree.
func reviewCloneState(t *testing.T, dir string) string {
	t.Helper()
	var state strings.Builder
	for _, name := range []string{".git/config", ".git/HEAD", "notes.txt", ".gitattributes"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		state.WriteString(name + ":\n" + string(body) + "\n")
	}
	hooks, err := os.ReadDir(filepath.Join(dir, ".git", "hooks"))
	if err != nil {
		t.Fatal(err)
	}
	for _, hook := range hooks {
		state.WriteString("hook " + hook.Name() + "\n")
	}
	return state.String()
}

// podGitWrapper is a copy of the pod's git wrapper, packages/daemon/docker/git, in a directory
// of its own, with the real git it runs repointed at this test's, and answers that directory: the
// wrapper is a shell script, so the row runs it rather than reading it. Its list of the
// subcommands that read a diff must be reviewDiffCommands, which the tmux shim holds.
func podGitWrapper(t *testing.T, git string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "docker", "git"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	if list := strings.Join(reviewDiffCommands, " | ") + ")\n"; !strings.Contains(script, list) {
		t.Fatalf("the pod's git wrapper holds no line %q, the subcommands the tmux shim runs without a diff driver", list)
	}
	const realGit = "real=/usr/bin/git\n"
	if !strings.Contains(script, realGit) {
		t.Fatalf("the pod's git wrapper holds no line %q to repoint", realGit)
	}
	script = strings.Replace(script, realGit, "real='"+git+"'\n", 1)
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// The reviewer's git reads the change from a clone of its own and runs no program the repository
// supplies. The head plants a diff driver's command, its text conversion, two
// hooks and an fsmonitor command the way a head can: in a repository it commits under
// planted.git, whose configuration, attributes and hooks every git run below that directory reads
// as its own (its objects are the clone's, through a relative alternates file), and through a run
// of another role, which writes them into the shared clone's configuration and hooks and into the
// global and system configuration of the machine the roles share. The review clone is made from
// the shared clone after those plants, and then another role's run plants the shared clone again
// and commits there; the review clone does not change. Before the reviewer's git runs, the same
// commands under a git with none of the reviewer's settings run every one of the planted programs
// in each place, so the row can see a plant run. Then the reviewer's git runs them, on the tmux
// runtime through the shim InstallReviewGit writes, first on PATH, and in a pod through the
// image's git wrapper for LEGION_ROLE=reviewer, each with the environment its runtime gives the
// reviewer, and no planted program runs.
func TestReviewerGitReadsNoRepositoryConfiguration(t *testing.T) {
	git := fixturegit.CompiledGit(t)
	jj := realTool(t, "jj")
	root := t.TempDir()
	home := filepath.Join(root, "home")
	for _, entry := range fixtureenv.Home(home) {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
	t.Setenv("JJ_USER", "Legion test")
	t.Setenv("JJ_EMAIL", "legion-test@example.invalid")
	markers := filepath.Join(root, "markers")
	if err := os.MkdirAll(markers, 0o755); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Dir(git) + ":/usr/bin:/bin"
	setup := fixturegit.Isolated(t, home).With("PATH=" + tools)
	repo := ghrepo.MustParse("acme/widgets")
	const issue = "ACME-1"
	state := filepath.Join(root, "state")

	// The head: one change to notes.txt, the attribute that hands every .txt file to the diff
	// driver `planted`, and the repository under planted.git that defines that driver, an
	// fsmonitor command and two hooks for every git run below it.
	origin := filepath.Join(root, "origin")
	initOrigin := fixturegit.InitWorkTree(origin, "main")
	initOrigin[0] = git
	setup.Run(root, initOrigin...)
	writeFiles(t, origin, map[string]string{"notes.txt": "base\n"})
	setup.Run(origin, git, "add", "--all")
	setup.Run(origin, git, "commit", "--quiet", "--message", "base")
	base := setup.Run(origin, git, "rev-parse", "HEAD")
	setup.Run(origin, git, "checkout", "--quiet", "-b", Bookmark(issue))
	writeFiles(t, origin, map[string]string{
		"notes.txt":                           "base\nhead\n",
		".gitattributes":                      "*.txt diff=planted\n",
		"planted.git/HEAD":                    "ref: refs/heads/main\n",
		"planted.git/refs/heads/main":         base + "\n",
		"planted.git/objects/info/alternates": "../../.git/objects\n",
		"planted.git/info/attributes":         "*.txt diff=planted\n",
		"planted.git/config":                  "[core]\n\trepositoryformatversion = 0\n\tbare = false\n\tworktree = ..\n" + plantedConfig(markers, ""),
	})
	plantReviewHooks(t, filepath.Join(origin, "planted.git", "hooks"), markers)
	setup.Run(origin, git, "add", "--all")
	setup.Run(origin, git, "commit", "--quiet", "--message", "head")
	head := setup.Run(origin, git, "rev-parse", "HEAD")
	setup.Run(origin, git, "checkout", "--quiet", "main")
	// The review's whatchanged as this git runs it, read in origin at main, which holds no plant.
	whatchanged, deprecated := whatchangedWords(t, setup, origin, git)

	// The shared clone every other role works in, and what another role's run of the head leaves
	// there and in the configuration the machine's roles share.
	located, err := Location(state, repo, issue)
	if err != nil {
		t.Fatal(err)
	}
	// It stands on the head, as the implementer's and the tester's workspaces of it do: git reads
	// a driver's attribute from the working tree, not from the commits it compares.
	setup.Run(root, git, "clone", "--quiet", "--branch", Bookmark(issue), origin, located.Clone)
	sharedHooks := filepath.Join(root, "shared-hooks")
	plantShared := func() {
		t.Helper()
		config, err := os.OpenFile(filepath.Join(located.Clone, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := config.WriteString(plantedConfig(markers, sharedHooks)); err != nil {
			t.Fatal(err)
		}
		if err := config.Close(); err != nil {
			t.Fatal(err)
		}
		plantReviewHooks(t, filepath.Join(located.Clone, ".git", "hooks"), markers)
	}
	plantShared()
	plantReviewHooks(t, sharedHooks, markers)
	globalHooks := filepath.Join(root, "global-hooks")
	plantReviewHooks(t, globalHooks, markers)
	system := filepath.Join(root, "system-gitconfig")
	writeFiles(t, root, map[string]string{
		"home/.config/git/config": plantedConfig(markers, globalHooks),
		"system-gitconfig":        plantedConfig(markers, globalHooks),
	})
	// A git with none of the reviewer's settings: it reads the planted global and system
	// configuration as well as the repository's own.
	exposed := slices.DeleteFunc(slices.Clone(setup.Env), func(entry string) bool {
		return strings.HasPrefix(entry, "GIT_CONFIG_GLOBAL=") || strings.HasPrefix(entry, "GIT_CONFIG_NOSYSTEM=")
	})
	exposed = append(exposed, "GIT_CONFIG_SYSTEM="+system)

	reviewDir, err := ReviewLocation(state, repo, issue)
	if err != nil {
		t.Fatal(err)
	}
	run := NewRunner(testTimeout, map[string]string{"git": git, "jj": jj})
	if err := ProvisionReviewClone(context.Background(), run, ReviewRequest{
		Clone: located.Clone, Dir: reviewDir, Repo: repo, Issue: issue, DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("ProvisionReviewClone: %v", err)
	}
	if left := markersLeft(t, markers); len(left) > 0 {
		t.Fatalf("provisioning the review clone ran the planted %v", left)
	}

	// A clone of its own, at the head, with none of what was planted in the shared clone.
	if reviewDir == located.Clone || reviewDir == located.Dir || strings.HasPrefix(reviewDir, located.Clone+"/") || strings.HasPrefix(reviewDir, located.Dir+"/") {
		t.Fatalf("the review clone %s is the shared clone %s or its workspace %s, or lies under one", reviewDir, located.Clone, located.Dir)
	}
	inspect := setup.With("GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=core.fsmonitor", "GIT_CONFIG_VALUE_0=false",
		"GIT_CONFIG_KEY_1=core.hooksPath", "GIT_CONFIG_VALUE_1=/dev/null")
	if common := inspect.Run(reviewDir, git, "rev-parse", "--path-format=absolute", "--git-common-dir"); common != filepath.Join(reviewDir, ".git") {
		t.Fatalf("the review clone's git directory is %s, want its own %s", common, filepath.Join(reviewDir, ".git"))
	}
	if at := inspect.Run(reviewDir, git, "rev-parse", "HEAD"); at != head {
		t.Fatalf("the review clone stands on %s, want the head %s", at, head)
	}
	if url := inspect.Run(reviewDir, git, "remote", "get-url", "origin"); url != GitHubURL(repo) {
		t.Fatalf("the review clone's origin is %s, want %s", url, GitHubURL(repo))
	}
	if info, err := os.Stat(filepath.Join(reviewDir, ".jj")); err != nil || !info.IsDir() {
		t.Fatalf("the review clone holds no jj repository beside its git one, which the shim's adoption runs jj in: %v", err)
	}
	config, err := os.ReadFile(filepath.Join(reviewDir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	for _, planted := range []string{"planted", "fsmonitor", "hooksPath"} {
		if strings.Contains(string(config), planted) {
			t.Fatalf("the review clone's configuration names %s, which the shared clone's planted:\n%s", planted, config)
		}
	}
	hooks, err := os.ReadDir(filepath.Join(reviewDir, ".git", "hooks"))
	if err != nil {
		t.Fatal(err)
	}
	for _, hook := range hooks {
		if !strings.HasSuffix(hook.Name(), ".sample") {
			t.Fatalf("the review clone holds the hook %s", hook.Name())
		}
	}

	// Another role's run after the review clone was made writes the shared clone, and the
	// review clone does not change.
	before := reviewCloneState(t, reviewDir)
	plantShared()
	writeFiles(t, located.Clone, map[string]string{"other-role.txt": "work\n"})
	inspect.Run(located.Clone, git, "add", "--all")
	inspect.Run(located.Clone, git, "commit", "--quiet", "--message", "another role's commit")
	if after := reviewCloneState(t, reviewDir); after != before {
		t.Fatalf("another role's run changed the review clone:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	// Every plant is live: a git with none of the reviewer's settings runs each planted program
	// in each place the reviewer's git runs.
	embedded := filepath.Join(reviewDir, "planted.git")
	for _, place := range []struct {
		name, dir string
		embedded  bool
	}{
		{"the shared clone", located.Clone, false},
		{"the review clone", reviewDir, false},
		{"the repository the head commits", embedded, true},
	} {
		outputs := runReview(t, exposed, place.dir, reviewCommands(base, head, sharedHooks, place.embedded, whatchanged))
		if ran := markersLeft(t, markers); !slices.Equal(ran, plantedPrograms) {
			t.Fatalf("in %s a git with none of the reviewer's settings ran the planted %v, want all of %v, so this row could not see one run there: %v",
				place.name, ran, plantedPrograms, outputs)
		}
	}

	for _, runtime := range []struct {
		name string
		env  func(t *testing.T) []string
	}{
		{"tmux pane: the shim first on PATH", func(t *testing.T) []string {
			reviewGit, err := InstallReviewGit(ReviewGitDir(state))
			if err != nil {
				t.Fatalf("InstallReviewGit: %v", err)
			}
			env := slices.Clone(exposed)
			for name, value := range reviewGit.Env(tools, nil) {
				env = append(env, name+"="+value)
			}
			return env
		}},
		{"pod: the image's git wrapper", func(t *testing.T) []string {
			env := append(slices.Clone(exposed), ReviewGitEnv(ReviewGitConfig())...)
			return append(env, "LEGION_ROLE=reviewer", "PATH="+podGitWrapper(t, git)+":"+tools)
		}},
	} {
		t.Run(runtime.name, func(t *testing.T) {
			env := runtime.env(t)
			commands := reviewCommands(base, head, sharedHooks, false, whatchanged)
			outputs := runReview(t, env, reviewDir, commands)
			if diff := outputs["diff "+base+" "+head]; !strings.Contains(diff, "+head") {
				t.Fatalf("the reviewer's git diff of the change printed no +head line, so it read nothing:\n%s", diff)
			}
			if probe := strings.TrimSpace(outputs[strings.Join(commands[len(commands)-1], " ")]); probe != probeValue {
				t.Fatalf("the reviewer's git read the -c in front of its subcommand as %q, want %q", probe, probeValue)
			}
			// The value the reviewer's git reads, and not only the absence of a planted hook:
			// core.hooksPath is /dev/null, under which no hook is ever found.
			if hooksPath := strings.TrimSpace(runReview(t, env, reviewDir, [][]string{{"config", "core.hooksPath"}})["config core.hooksPath"]); hooksPath != "/dev/null" {
				t.Errorf("the reviewer's git config core.hooksPath printed %q, want /dev/null", hooksPath)
			}
			if ran := markersLeft(t, markers); len(ran) > 0 {
				t.Errorf("the reviewer's git in its clone ran the planted %v: %v", ran, outputs)
			}
			outputs = runReview(t, env, embedded, reviewCommands(base, head, sharedHooks, true, whatchanged))
			if log := outputs["log -p -1"]; !strings.Contains(log, "+base") {
				t.Fatalf("the reviewer's git log in the repository the head commits printed no +base line, so it read nothing:\n%s", log)
			}
			if ran := markersLeft(t, markers); len(ran) > 0 {
				t.Errorf("the reviewer's git in the repository the head commits ran the planted %v: %v", ran, outputs)
			}
			// A command that names --textconv or --ext-diff itself would take the reviewer's
			// flags back, so it is refused before git runs.
			for _, again := range []string{"--textconv", "--ext-diff"} {
				output, err := (fixturegit.Runner{TB: t, Env: env}).Try(reviewDir, envGit(t, env), "log", "-p", "-1", again)
				if err == nil || !strings.Contains(output, "refusing log "+again) {
					t.Errorf("the reviewer's git log %s = %v, want it refused:\n%s", again, err, output)
				}
			}
			if ran := markersLeft(t, markers); len(ran) > 0 {
				t.Fatalf("a reviewer's git log that names --textconv or --ext-diff ran the planted %v", ran)
			}
			// On a git that runs whatchanged only with --i-still-use-this, the reviewer's git
			// leaves git's own refusal of the bare words as it is: it adds no option of its own,
			// so a git with no such option takes its words as they come, and the refusal runs no
			// planted program either.
			if deprecated {
				output, err := (fixturegit.Runner{TB: t, Env: env}).Try(reviewDir, envGit(t, env), "whatchanged", "-p", "-1")
				if err == nil || !strings.Contains(output, whatchangedRefusal) {
					t.Errorf("the reviewer's git whatchanged -p -1 on a git that deprecates it = %v, want git's own refusal:\n%s", err, output)
				}
				if ran := markersLeft(t, markers); len(ran) > 0 {
					t.Fatalf("a reviewer's git whatchanged that git refused ran the planted %v", ran)
				}
			}
		})
	}
}
