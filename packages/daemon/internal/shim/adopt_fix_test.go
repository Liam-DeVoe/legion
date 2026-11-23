package shim_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// copyRig is an issue's working copies as a pod holds them, with the real git and jj: a shared
// clone, colocated with its git repository, and the issue's own workspace of it, LEGION_WORKSPACE,
// whose working copy stands on the task's head (a commit of the task over the clone's base). The
// shim runs on that workspace, and the jj first on its PATH records each command it is given and
// runs the real one.
type copyRig struct {
	t         *testing.T
	jj        string // the real jj
	home      string
	clone     string
	workspace string // LEGION_WORKSPACE
	fixDir    string // workspace.FixDir(workspace)
	record    string // the file the PATH's jj writes each command's arguments to, one line each
	head      string // the task's head: the commit under the workspace's working copy
	p         *peer
}

// The identity the daemon names on each adoption here, and the one every command of the test's
// own runs under, so an author tells the two apart.
const (
	adoptingUser  = "implementer"
	adoptingEmail = "implementer@example.test"
	setupUser     = "setup"
)

func newCopyRig(t *testing.T) *copyRig {
	t.Helper()
	jj, err := exec.LookPath("jj")
	if err != nil {
		t.Fatalf("jj is required: %v", err)
	}
	state := t.TempDir()
	r := &copyRig{
		t:         t,
		jj:        jj,
		home:      t.TempDir(),
		clone:     filepath.Join(state, "repos", "github.com", "acme", "widgets"),
		workspace: filepath.Join(state, "workspaces", "acme", "widgets", "widgets-42"),
		record:    filepath.Join(t.TempDir(), "jj-commands"),
	}
	r.fixDir = workspace.FixDir(r.workspace)
	for _, dir := range []string{r.clone, filepath.Dir(r.workspace)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The init takes no -R and opens no workspace, so with no Dir jj would walk up from the test
	// process's directory to the closest .jj and write that repository.
	fixturegit.Isolated(t, r.home).With("JJ_USER="+setupUser, "JJ_EMAIL="+setupUser+"@example.test").
		Run(r.clone, append([]string{jj}, fixturegit.JJInit(r.clone, true)[1:]...)...)
	r.write(r.clone, "README.md", "widgets\n")
	r.jjIn(r.clone, "commit", "-m", "chore: the base")
	r.jjIn(r.clone, "workspace", "add", r.workspace, "--name", filepath.Base(r.workspace), "--revision", r.commitOf(r.clone, "@-"))
	r.write(r.workspace, "feature.txt", "the task's change\n")
	r.jjIn(r.workspace, "commit", "-m", "feat: the task's change")
	r.head = r.commitOf(r.workspace, "@-")

	// The shim's jj, first on PATH, where the shim looks. It hands the real jj the PATH past its own
	// directory, so a jj on PATH that is itself a wrapper never finds this one again.
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + r.record + "\nPATH=\"${PATH#*:}\" exec " + jj + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "jj"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	path := socketPath(t)
	daemon := listen(t, path)
	child := newOMP(t)
	cfg := config(t, path, child)
	cfg.Env = append(fixtureenv.Isolated(r.home), fakeOMPEnv+"=1", "FAKE_OMP_MARKER="+child.marker,
		"FAKE_OMP_LOG="+child.log, "LEGION_WORKSPACE="+r.workspace)
	run(t, cfg, newClock())
	r.p = daemon.accept(t)
	r.p.open(t)
	return r
}

// try runs the real jj in dir on dir's own repository, as the test's own identity, and answers
// what it printed and how it ended. It runs in dir, since jj reads the repository of the
// directory it runs in whatever -R names.
func (r *copyRig) try(dir string, args ...string) (string, error) {
	r.t.Helper()
	command := exec.Command(r.jj, append(args, "-R", dir)...)
	command.Dir = dir
	command.Env = append(fixtureenv.Isolated(r.home), "JJ_USER="+setupUser, "JJ_EMAIL="+setupUser+"@example.test")
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

// jjIn is try that fails the test when jj does.
func (r *copyRig) jjIn(dir string, args ...string) string {
	r.t.Helper()
	output, err := r.try(dir, args...)
	if err != nil {
		r.t.Fatalf("jj %v in %s: %v\n%s", args, dir, err, output)
	}
	return output
}

// commitOf is the commit id of rev in dir's working copies' repository.
func (r *copyRig) commitOf(dir, rev string) string {
	r.t.Helper()
	return r.jjIn(dir, "log", "-r", rev, "--no-graph", "-T", "commit_id")
}

// authorOf is the author's name of rev and its description, as one string.
func (r *copyRig) authorOf(dir, rev string) string {
	r.t.Helper()
	return r.jjIn(dir, "log", "-r", rev, "--no-graph", "-T", `author.name() ++ "|" ++ description`)
}

func (r *copyRig) write(dir, name, body string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// commit writes name in dir's working copy and commits it there, as a role's step does, and
// answers the commit.
func (r *copyRig) commit(dir, name, message string) string {
	r.t.Helper()
	r.write(dir, name, message+"\n")
	r.jjIn(dir, "commit", "-m", message)
	return r.commitOf(dir, "@-")
}

// adopt sends the daemon's adoption with frame's fixFrom and moveTo and answers the shim's reply.
func (r *copyRig) adopt(id string, frame shimwire.AdoptWorkingCopy) shimwire.AdoptWorkingCopyResult {
	r.t.Helper()
	frame.ID, frame.JJUser, frame.JJEmail, frame.TimeoutMs = id, adoptingUser, adoptingEmail, 30000
	r.p.send(r.t, frame)
	got, ok := r.p.next(r.t).(shimwire.AdoptWorkingCopyResult)
	if !ok || got.ID != id {
		r.t.Fatalf("the shim answered %#v, want the result of %s", got, id)
	}
	return got
}

// commands is every jj command the shim has run so far, by its arguments.
func (r *copyRig) commands() []string {
	r.t.Helper()
	body, err := os.ReadFile(r.record)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		r.t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
}

// verbs is what each command did: its subcommand, with the workspace subcommand's own.
func verbs(commands []string) []string {
	var done []string
	for _, command := range commands {
		words := strings.Fields(command)
		verb := words[0]
		if verb == "workspace" && len(words) > 1 {
			verb += " " + words[1]
		}
		done = append(done, verb)
	}
	return done
}

// The adoption that precedes a fix's task makes the fix's working copy, beside the issue's
// workspace, on a fresh commit over the head the fix starts from, authored by the role the daemon
// names, and answers its directory. The issue's workspace is not adopted: its working copy stays
// the commit it was, authored as it was, for the step that runs beside the fix.
func TestAnAdoptionMakesTheFixCopyAtItsHead(t *testing.T) {
	r := newCopyRig(t)
	shared := `commit_id ++ " " ++ change_id ++ " " ++ author.name()`
	before := r.jjIn(r.workspace, "log", "-r", "@", "--no-graph", "-T", shared)

	got := r.adopt("fix", shimwire.AdoptWorkingCopy{FixFrom: r.head})
	if !got.OK || got.FixDir != r.fixDir || got.MovedTo != "" {
		t.Fatalf("the shim answered %#v, want ok with fixDir %s and no movedTo", got, r.fixDir)
	}
	if parent := r.commitOf(r.fixDir, "@-"); parent != r.head {
		t.Errorf("the fix's working copy stands on %s, want the head it starts from, %s", parent, r.head)
	}
	if author := r.authorOf(r.fixDir, "@"); author != adoptingUser+"|" {
		t.Errorf("the fix's working copy is %q, want an undescribed commit authored by %s", author, adoptingUser)
	}
	if after := r.jjIn(r.workspace, "log", "-r", "@", "--no-graph", "-T", shared); after != before {
		t.Errorf("the issue's workspace's working copy went from %q to %q, want it unchanged beside the fix", before, after)
	}
	add := "workspace add --name " + filepath.Base(r.fixDir) + " -r " + r.head + " " + r.fixDir + " -R " + r.workspace
	if ran := r.commands(); len(ran) != 2 || ran[0] != add || !slices.Equal(verbs(ran), []string{"workspace add", "metaedit"}) || !strings.HasSuffix(ran[1], "-R "+r.fixDir) {
		t.Errorf("the adoption ran jj\n%s\nwant the fix's workspace added (%s), then the adoption of that workspace alone", strings.Join(ran, "\n"), add)
	}
}

// A later fix of the same task finds the fix's working copy there and moves it onto the head it
// starts from, a fresh commit authored by the role, and adds no second workspace.
func TestASecondFixMovesTheCopy(t *testing.T) {
	r := newCopyRig(t)
	if got := r.adopt("first", shimwire.AdoptWorkingCopy{FixFrom: r.head}); !got.OK || got.FixDir != r.fixDir {
		t.Fatalf("the first fix's adoption answered %#v, want ok with fixDir %s", got, r.fixDir)
	}
	pushed := r.commit(r.fixDir, "fix.txt", "fix: the first failure")
	before := len(r.commands())

	got := r.adopt("second", shimwire.AdoptWorkingCopy{FixFrom: pushed})
	if !got.OK || got.FixDir != r.fixDir {
		t.Fatalf("the second fix's adoption answered %#v, want ok with fixDir %s", got, r.fixDir)
	}
	if parent := r.commitOf(r.fixDir, "@-"); parent != pushed {
		t.Errorf("the fix's working copy stands on %s, want the second fix's head, %s", parent, pushed)
	}
	if author := r.authorOf(r.fixDir, "@"); author != adoptingUser+"|" {
		t.Errorf("the fix's working copy is %q, want an undescribed commit authored by %s", author, adoptingUser)
	}
	ran := r.commands()[before:]
	if want := []string{"workspace update-stale", "new", "metaedit"}; !slices.Equal(verbs(ran), want) {
		t.Errorf("the second fix's adoption ran jj\n%s\nwant %v in the fix's working copy and no second workspace add", strings.Join(ran, "\n"), want)
	}
	for _, command := range ran {
		if !strings.HasSuffix(command, "-R "+r.fixDir) {
			t.Errorf("the second fix's adoption ran %q, want each command on the fix's working copy", command)
		}
	}
}

// An adoption naming the commit the task's working copy moves to starts the step on a fresh commit
// over it, in the issue's workspace, then adopts that workspace as an adoption with no move does,
// and answers the commit.
func TestAnAdoptionMovesTheTaskCopyToTheHead(t *testing.T) {
	r := newCopyRig(t)
	// Another working copy of the clone pushed a commit over the task's head.
	r.jjIn(r.clone, "new", r.head, "-m", "fix: pushed from another working copy")
	moved := r.commitOf(r.clone, "@")

	got := r.adopt("move", shimwire.AdoptWorkingCopy{MoveTo: moved})
	if !got.OK || got.MovedTo != moved || got.FixDir != "" {
		t.Fatalf("the shim answered %#v, want ok with movedTo %s and no fixDir", got, moved)
	}
	if parent := r.commitOf(r.workspace, "@-"); parent != moved {
		t.Errorf("the task's working copy stands on %s, want the commit it moved to, %s", parent, moved)
	}
	if author := r.authorOf(r.workspace, "@"); author != adoptingUser+"|" {
		t.Errorf("the task's working copy is %q, want an undescribed commit authored by %s", author, adoptingUser)
	}
	if ran := r.commands(); !slices.Equal(verbs(ran), []string{"log", "new", "log", "metaedit"}) {
		t.Errorf("the adoption ran jj\n%s\nwant the read of @-, the move, then the adoption as without one", strings.Join(ran, "\n"))
	}
}

// A move to the commit the task's working copy already stands on reads it and moves nothing: the
// one read, then the adoption as without a move, and no `jj new`.
func TestAMoveToWhereItStandsRunsNoJJNew(t *testing.T) {
	r := newCopyRig(t)
	got := r.adopt("stay", shimwire.AdoptWorkingCopy{MoveTo: r.head})
	if !got.OK || got.MovedTo != r.head {
		t.Fatalf("the shim answered %#v, want ok with movedTo %s", got, r.head)
	}
	if parent := r.commitOf(r.workspace, "@-"); parent != r.head {
		t.Errorf("the task's working copy stands on %s, want %s, where it stood", parent, r.head)
	}
	ran := r.commands()
	if want := []string{"log", "log", "metaedit"}; !slices.Equal(verbs(ran), want) {
		t.Errorf("the adoption ran jj\n%s\nwant %v: one read of @-, no jj new", strings.Join(ran, "\n"), want)
	}
	if len(ran) > 0 && ran[0] != "log -r @- --no-graph -T commit_id -R "+r.workspace {
		t.Errorf("the adoption's first command is %q, want the read of the working copy's parent", ran[0])
	}
}

// Every workspace of the clone shares its store, so a commit a step makes in the issue's
// workspace while the fix runs is there in the fix's working copy by its id, for the fix to take.
func TestACommitOfTheSharedCopyIsSeenInTheFixCopy(t *testing.T) {
	r := newCopyRig(t)
	if got := r.adopt("fix", shimwire.AdoptWorkingCopy{FixFrom: r.head}); !got.OK || got.FixDir != r.fixDir {
		t.Fatalf("the fix's adoption answered %#v, want ok with fixDir %s", got, r.fixDir)
	}
	red := r.commit(r.workspace, "red_test.txt", "test: the red test")
	if got := r.jjIn(r.fixDir, "log", "-r", red, "--no-graph", "-T", "commit_id ++ \" \" ++ description.first_line()"); got != red+" test: the red test" {
		t.Errorf("the fix's working copy reads %s as %q, want the shared copy's commit", red, got)
	}
}

// The fix takes a commit made under the issue's working copy by copying it (`jj duplicate
// --onto`, the flag of the jj CI pins), which rewrites nothing of the issue's workspace: its
// working copy stays fresh, and the next adoption of it, moving it to the fix's head, succeeds.
func TestADuplicatedCommitLeavesTheSharedCopyFresh(t *testing.T) {
	r := newCopyRig(t)
	if got := r.adopt("fix", shimwire.AdoptWorkingCopy{FixFrom: r.head}); !got.OK || got.FixDir != r.fixDir {
		t.Fatalf("the fix's adoption answered %#v, want ok with fixDir %s", got, r.fixDir)
	}
	red := r.commit(r.workspace, "red_test.txt", "test: the red test")
	fixed := r.commit(r.fixDir, "fix.txt", "fix: the failure")
	r.jjIn(r.fixDir, "duplicate", red, "--onto", fixed)
	copied := r.commitOf(r.fixDir, "children("+fixed+") ~ @")
	if status, err := r.try(r.workspace, "status"); err != nil || strings.Contains(status, "stale") {
		t.Fatalf("jj status in the issue's workspace after the copy = %v:\n%s\nwant a fresh working copy", err, status)
	}

	got := r.adopt("move", shimwire.AdoptWorkingCopy{MoveTo: copied})
	if !got.OK || got.MovedTo != copied {
		t.Fatalf("the next adoption of the issue's workspace answered %#v, want ok with movedTo %s", got, copied)
	}
	if parent := r.commitOf(r.workspace, "@-"); parent != copied {
		t.Errorf("the task's working copy stands on %s, want the fix's head, %s", parent, copied)
	}
}

// A fix that rewrites a commit under the issue's working copy from its own (`jj rebase -r`) leaves
// that working copy stale, and jj refuses every command there until `jj workspace update-stale`
// runs. So where the fix's working copy exists, an adoption of the issue's workspace brings it up
// to date first, and the next move succeeds. The control takes the update-stale out: the
// adoption's first read, run alone, fails on the stale working copy.
func TestARewriteFromTheFixCopyIsBroughtUpToDate(t *testing.T) {
	rewritten := func(t *testing.T) (*copyRig, string) {
		t.Helper()
		r := newCopyRig(t)
		if got := r.adopt("fix", shimwire.AdoptWorkingCopy{FixFrom: r.head}); !got.OK || got.FixDir != r.fixDir {
			t.Fatalf("the fix's adoption answered %#v, want ok with fixDir %s", got, r.fixDir)
		}
		red := r.commit(r.workspace, "red_test.txt", "test: the red test")
		fixed := r.commit(r.fixDir, "fix.txt", "fix: the failure")
		r.jjIn(r.fixDir, "rebase", "-r", red, "--onto", fixed)
		return r, r.commitOf(r.fixDir, "children("+fixed+") ~ @")
	}
	t.Run("the next adoption brings the working copy up to date and moves it", func(t *testing.T) {
		r, moved := rewritten(t)
		before := len(r.commands())
		got := r.adopt("move", shimwire.AdoptWorkingCopy{MoveTo: moved})
		if !got.OK || got.MovedTo != moved {
			t.Fatalf("the next adoption of the issue's workspace answered %#v, want ok with movedTo %s", got, moved)
		}
		if parent := r.commitOf(r.workspace, "@-"); parent != moved {
			t.Errorf("the task's working copy stands on %s, want the rewritten commit, %s", parent, moved)
		}
		ran := r.commands()[before:]
		if len(ran) == 0 || ran[0] != "workspace update-stale -R "+r.workspace {
			t.Errorf("the adoption ran jj\n%s\nwant the update-stale of the issue's workspace first", strings.Join(ran, "\n"))
		}
	})
	t.Run("without the update-stale, jj refuses the stale working copy", func(t *testing.T) {
		r, _ := rewritten(t)
		output, err := r.try(r.workspace, "log", "-r", "@-", "--no-graph", "-T", "commit_id")
		if err == nil || !strings.Contains(output, "stale") {
			t.Fatalf("the adoption's first read with no update-stale ended %v:\n%s\nwant jj's refusal of a stale working copy", err, output)
		}
	})
}
