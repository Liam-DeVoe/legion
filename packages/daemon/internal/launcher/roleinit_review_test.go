package launcher

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
	"github.com/sjawhar/legion/daemon/internal/lookpath"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// reviewStageEnv carries TestReviewStageHelper's plan, and reviewAgentEnv the label and reviews
// directory TestReviewAgentHelper reports on. Each is a test input fixtureenv.Guard keeps.
const (
	reviewStageEnv = "LEGION_TEST_REVIEW_STAGE"
	reviewAgentEnv = "LEGION_TEST_REVIEW_AGENT"
)

// reviewStagePlan is the stage's input: this test binary, the directory it mounts its layout on,
// and the PATH whose git and jj role-init and the agents run.
type reviewStagePlan struct{ Self, Mount, Path string }

// In a pod the reviewer works in its own clone of the issue on the issue's volume, which no
// other role's process sees or writes. Its role-init, in the reviewer's own namespace once it is
// built, makes the clone from the pod's shared clone at the head the daemon gives it
// (LEGION_REVIEW_HEAD), and starts the reviewer's shim in it; the next start of the reviewer
// brings the clone to the new head it is given, though another role moved the shared clone's
// record of the issue's branch elsewhere; a head the shared clone does not hold is fetched into it
// from its origin first; and a start at a head origin does not hold either is refused before the
// shim runs. Every other role's role-init covers the volume's reviews directory
// with an empty filesystem of the role's own and starts the role in the issue's workspace: the
// role sees no clone there, and what it writes there never reaches the volume.
func TestARolesInitMakesTheReviewersOwnCloneThatNoOtherRoleSees(t *testing.T) {
	requireRoleNamespaces(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The git and jj themselves, never a wrapper of this machine's in front of them: role-init
	// takes the first compiled one on PATH, and a shim that runs another tool from its own home
	// finds none under the role's.
	path := []string{"/usr/bin", "/bin"}
	for _, tool := range []string{"jj", "git"} {
		found, _ := lookpath.Others(os.Getenv("PATH"), tool, "")
		for _, candidate := range found {
			if _, compiled, err := lookpath.Kind(candidate.Path); err == nil && compiled && !strings.Contains(candidate.Path, "/shims/") {
				path = append([]string{filepath.Dir(candidate.Path)}, path...)
				break
			}
		}
	}
	plan, err := json.Marshal(reviewStagePlan{Self: self, Mount: stageMountPoint(t), Path: strings.Join(path, ":")})
	if err != nil {
		t.Fatal(err)
	}
	stage := exec.Command(self, "-test.run=^TestReviewStageHelper$")
	stage.Env = append(os.Environ(), reviewStageEnv+"="+string(plan))
	stage.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	output, err := stage.CombinedOutput()
	if err != nil {
		t.Fatalf("the stage: %v\n%s", err, output)
	}
	report := reported(string(output))
	for _, want := range []struct{ key, value, why string }{
		{"stage reviewer status", "0", "the reviewer's role-init made its clone and ran its shim"},
		{"agent-reviewer pwd", report["stage review"], "the reviewer's shim starts in its own clone"},
		{"agent-reviewer head", report["stage first"], "the reviewer's clone stands on the head it is given"},
		{"stage reviewer2 status", "0", "the reviewer's next role-init brought its clone to the new head"},
		{"agent-reviewer2 head", report["stage second"], "the reviewer's next start reviews the head it is given, not the shared clone's moved record"},
		{"stage reviewer3 status", "0", "a start at a head the shared clone lacks fetches it from origin and runs the shim"},
		{"agent-reviewer3 head", report["stage third"], "the reviewer stands on the head origin gave the shared clone"},
		{"stage reviewer4 status", "none", "a start at a head origin does not hold either is refused before the shim runs"},
		{"stage review-kept", "present", "the reviewer's clone is still on the volume after the other roles ran"},
	} {
		if got, ok := report[want.key]; !ok || got != want.value || got == "" {
			t.Errorf("%s = %q, want %q: %s\n%s", want.key, got, want.value, want.why, output)
		}
	}
	if pwd, ran := report["agent-reviewer4 pwd"]; ran {
		t.Errorf("the reviewer's shim ran in %s at a head no repository holds\n%s", pwd, output)
	}
	if nowhere := report["stage nowhere"]; nowhere == "" || !strings.Contains(string(output), "the head under review "+nowhere) {
		t.Errorf("the refused start names no head under review %q\n%s", nowhere, output)
	}
	for _, role := range []string{"tester", "implementer"} {
		for _, want := range []struct{ key, value, why string }{
			{"stage " + role + " status", "0", "the role's role-init covered the reviews directory and ran its shim"},
			{"agent-" + role + " pwd", report["stage workspace"], "every other role starts in the issue's workspace"},
			{"agent-" + role + " reviews", "0", "no other role sees a reviewer's clone"},
			{"agent-" + role + " planted", "written", "what another role writes there lands on its own cover"},
			{"stage " + role + "-planted", "absent", "what another role writes there never reaches the volume"},
		} {
			if got, ok := report[want.key]; !ok || got != want.value || got == "" {
				t.Errorf("%s = %q, want %q: %s\n%s", want.key, got, want.value, want.why, output)
			}
		}
	}
}

// TestReviewStageHelper is the issue pod's roles container: a roles directory, a fixed root and a
// home on a memory filesystem of its own, and an issue volume whose shared clone holds the
// issue's branch at origin and whose workspace is the issue's. It runs the reviewer's role-init
// twice, the branch moving between them, then the tester's, and reports what it can see.
func TestReviewStageHelper(t *testing.T) {
	encoded := os.Getenv(reviewStageEnv)
	if encoded == "" {
		return
	}
	var plan reviewStagePlan
	if err := json.Unmarshal([]byte(encoded), &plan); err != nil {
		fmt.Println("stage error:", err)
		os.Exit(2)
	}
	if err := runReviewStage(t, plan); err != nil {
		fmt.Println("stage error:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runReviewStage(t *testing.T, plan reviewStagePlan) error {
	if err := privateMounts(); err != nil {
		return err
	}
	// The test binary is read before the stage's own filesystem goes over the mount point, which
	// may hold the directory `go test` built it in.
	body, err := os.ReadFile(plan.Self)
	if err != nil {
		return err
	}
	if err := mountTmpfs(plan.Mount, "size=256m"); err != nil {
		return err
	}
	self := filepath.Join(plan.Mount, "launcher.test")
	if err := os.WriteFile(self, body, 0o755); err != nil {
		return err
	}
	rolesDir, fixedRoot := filepath.Join(plan.Mount, "roles"), filepath.Join(plan.Mount, "fixed")
	home, homeMounts := filepath.Join(plan.Mount, "home"), filepath.Join(plan.Mount, "home-mounts")
	for _, dir := range []string{home, homeMounts} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	for _, name := range []string{RoleGHDir, RolePrivateDir, RoleStateDir, RoleAgentSecretsDir} {
		if err := os.MkdirAll(filepath.Join(fixedRoot, name), 0o755); err != nil {
			return err
		}
	}
	for _, role := range []string{"reviewer", "tester", "implementer"} {
		for _, name := range []string{RoleLauncherDir, RoleGHDir, RoleStateDir, filepath.Join(RolePrivateDir, "g1")} {
			if err := os.MkdirAll(filepath.Join(rolesDir, role, name), 0o755); err != nil {
				return err
			}
		}
	}
	// The issue's volume: the shared clone of origin standing on the issue's branch, and the
	// issue's workspace, where provisioning puts both.
	state := filepath.Join(plan.Mount, "legion")
	repo := ghrepo.MustParse("acme/widgets")
	located, err := workspace.Location(state, repo, "ACME-1")
	if err != nil {
		return err
	}
	review, err := workspace.ReviewLocation(state, repo, "ACME-1")
	if err != nil {
		return err
	}
	env := append(os.Environ(), "PATH="+plan.Path, "HOME="+filepath.Join(plan.Mount, "stage-home"), "TMPDIR=/tmp",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_ALLOW_PROTOCOL=file",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test",
		"JJ_USER=t", "JJ_EMAIL=t@example.test", "JJ_CONFIG="+filepath.Join(plan.Mount, "no-jj-config.toml"))
	gitPath, found := lookpath.On(plan.Path, "git")
	if !found {
		return fmt.Errorf("no git on %s", plan.Path)
	}
	runner := fixturegit.Runner{TB: t, Env: env}
	git := func(dir string, args ...string) (string, error) {
		if args[0] == "git" {
			args = args[1:]
		}
		out, err := runner.Try(dir, append([]string{gitPath}, args...)...)
		if err != nil {
			return "", fmt.Errorf("git %v in %s: %v: %s", args, dir, err, out)
		}
		return strings.TrimSpace(out), nil
	}
	origin := filepath.Join(plan.Mount, "origin")
	commit := func(body, message string) (string, error) {
		if err := os.WriteFile(filepath.Join(origin, "notes.txt"), []byte(body), 0o644); err != nil {
			return "", err
		}
		for _, args := range [][]string{{"add", "--all"}, {"commit", "--quiet", "--message", message}} {
			if _, err := git(origin, args...); err != nil {
				return "", err
			}
		}
		return git(origin, "rev-parse", "HEAD")
	}
	if err := os.MkdirAll(origin, 0o755); err != nil {
		return err
	}
	if _, err := git(origin, fixturegit.InitWorkTree("", "main")...); err != nil {
		return err
	}
	if _, err := commit("base\n", "base"); err != nil {
		return err
	}
	if _, err := git(origin, "checkout", "--quiet", "-b", workspace.Bookmark("ACME-1")); err != nil {
		return err
	}
	first, err := commit("base\nfirst\n", "first")
	if err != nil {
		return err
	}
	if _, err := git(plan.Mount, "clone", "--quiet", "--branch", workspace.Bookmark("ACME-1"), origin, located.Clone); err != nil {
		return err
	}
	if err := os.MkdirAll(located.Dir, 0o755); err != nil {
		return err
	}
	fmt.Printf("stage review=%s\nstage workspace=%s\nstage first=%s\n", review, located.Dir, first)

	roleInitLaunch = roleInitHelperLaunch(self)
	cover := RoleCover{RolesDir: rolesDir, Home: home, HomeMounts: homeMounts, FixedRoot: fixedRoot}
	generation := func(role, label, workspaceDir, head string) error {
		roleEnv := append(slices.Clip(env), "LEGION_WORKSPACE="+workspaceDir, "LEGION_ISSUE=ACME-1", "LEGION_DEFAULT_BRANCH=main",
			reviewAgentEnv+"="+label+" "+workspace.ReviewRoot(state))
		if head != "" {
			roleEnv = append(roleEnv, "LEGION_REVIEW_HEAD="+head)
		}
		process, err := newRoleProcess(RoleInitConfig{
			RoleCover: cover, Role: role, Generation: 1,
			Argv: []string{self, "-test.run=^TestReviewAgentHelper$"},
		}, roleEnv, os.Stdout, os.Stdout)
		if err != nil {
			return err
		}
		if err := process.start(); err != nil {
			return err
		}
		status, _ := io.ReadAll(process.status)
		_ = process.status.Close()
		_ = process.cmd.Wait()
		reportedStatus := "none"
		if parsed := parseStatus(status); parsed != nil {
			reportedStatus = strconv.Itoa(parsed.ExitStatus())
		}
		fmt.Printf("stage %s status=%s\n", strings.TrimPrefix(label, "agent-"), reportedStatus)
		return nil
	}
	if err := generation("reviewer", "agent-reviewer", review, first); err != nil {
		return err
	}
	second, err := commit("base\nfirst\nsecond\n", "second")
	if err != nil {
		return err
	}
	if _, err := git(located.Clone, "fetch", "--quiet", "origin"); err != nil {
		return err
	}
	// Another role moves the shared clone's record of the issue's branch back to the first head.
	if _, err := git(located.Clone, "update-ref", "refs/remotes/origin/"+workspace.Bookmark("ACME-1"), first); err != nil {
		return err
	}
	fmt.Printf("stage second=%s\n", second)
	if err := generation("reviewer", "agent-reviewer2", review, second); err != nil {
		return err
	}
	// The branch moves at origin again, and the shared clone does not fetch it.
	third, err := commit("base\nfirst\nsecond\nthird\n", "third")
	if err != nil {
		return err
	}
	fmt.Printf("stage third=%s\n", third)
	if err := generation("reviewer", "agent-reviewer3", review, third); err != nil {
		return err
	}
	nowhere := strings.Repeat("ab", 20)
	fmt.Printf("stage nowhere=%s\n", nowhere)
	if err := generation("reviewer", "agent-reviewer4", review, nowhere); err != nil {
		return err
	}
	for _, role := range []string{"tester", "implementer"} {
		if err := generation(role, "agent-"+role, located.Dir, ""); err != nil {
			return err
		}
		fmt.Printf("stage %s-planted=%s\n", role, presence(filepath.Join(workspace.ReviewRoot(state), "planted-by-agent-"+role)))
	}
	fmt.Printf("stage review-kept=%s\n", presence(filepath.Join(review, ".jj")))
	return nil
}

// TestReviewAgentHelper is a role's shim in the review stage: it reports the directory it starts
// in, the head of the clone there, how many entries the reviews directory shows it, and whether it
// could write there.
func TestReviewAgentHelper(t *testing.T) {
	label, reviews, ok := strings.Cut(os.Getenv(reviewAgentEnv), " ")
	if !ok {
		return
	}
	cwd, err := os.Getwd()
	fmt.Printf("%s pwd=%s\n", label, cwd)
	if err != nil {
		fmt.Printf("%s pwd-error=%v\n", label, err)
	}
	if head, err := (fixturegit.Runner{TB: t, Env: os.Environ()}).Try(cwd, "git", "rev-parse", "HEAD"); err == nil {
		fmt.Printf("%s head=%s\n", label, strings.TrimSpace(head))
	}
	entries, err := os.ReadDir(reviews)
	fmt.Printf("%s reviews=%d\n", label, len(entries))
	if err == nil && os.WriteFile(filepath.Join(reviews, "planted-by-"+label), []byte("x"), 0o644) == nil {
		fmt.Printf("%s planted=written\n", label)
	}
	os.Exit(0)
}
