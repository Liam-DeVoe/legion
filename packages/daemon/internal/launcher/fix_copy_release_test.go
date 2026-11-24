package launcher

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/shim"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/testbin"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// A pod can be released while a fix of its issue runs beside the other steps, in a working copy of
// its own beside the shared one (workspace.FixDir). Nothing of the fix is lost by the release: the
// fix's working copy comes back in the pod that replaces the released one, at its own last working
// copy, with the fix's commit under it and the edit its agent made after its last jj command; and
// the next adoption of the fix's task, which names the head the fix started from, keeps the copy
// there. A step's commit in the shared working copy, which nobody pushed, comes back too. The
// control is TestAReleaseWithNoFixCopyMakesNone.
func TestAReleaseKeepsTheRunningFixsCopy(t *testing.T) {
	testbin.RequireJJFilters(t)
	f := newBundleFixture(t)
	head, step := f.stepWork()
	fixDir := workspace.FixDir(f.ws)
	if got := f.adoptIn(f.ws, shimwire.AdoptWorkingCopy{FixFrom: head}); !got.OK || got.FixDir != fixDir {
		t.Fatalf("the fix's adoption in the released pod answered %#v, want ok with fixDir %s", got, fixDir)
	}
	f.write(filepath.Join(fixDir, "fix.txt"), "fix\n")
	f.run(fixDir, "jj", "commit", "-R", fixDir, "-m", "fix work")
	fixed := f.run(fixDir, "jj", "log", "-R", fixDir, "-r", "@-", "--no-graph", "-T", "commit_id")
	// The fix's agent edits a file and runs no jj command after it: nothing has recorded the edit.
	f.write(filepath.Join(fixDir, "fix-wip.txt"), "fix wip\n")
	saved := f.bundleWorkspaceOne()

	ws := f.secondPod("ws2")
	if notice := f.restoreInto(ws, saved, 1); !strings.Contains(notice, "is restored") {
		t.Fatalf("the restore's notice is %q, want the saved work restored", notice)
	}
	for _, problem := range f.stepCameBack(ws, step) {
		t.Errorf("after the release: %s", problem)
	}
	if found, err := f.try(ws, "jj", "log", "-R", ws, "--ignore-working-copy", "-r", `files(root:"fix-wip.txt")`, "--no-graph", "-T", `commit_id ++ "\n"`); err != nil || found == "" {
		t.Errorf("the restored repository holds no commit with the fix's edit that no jj command recorded (%v): the release lost it\n%s", err, found)
	}
	restored := workspace.FixDir(ws)
	for _, problem := range f.fixCopyProblems(ws, restored, fixed) {
		t.Errorf("after the restore: %s", problem)
	}
	if got := f.adoptIn(ws, shimwire.AdoptWorkingCopy{FixFrom: head}); !got.OK || got.FixDir != restored {
		t.Errorf("the fix's next adoption answered %#v, want ok with fixDir %s", got, restored)
	}
	for _, problem := range f.fixCopyProblems(ws, restored, fixed) {
		t.Errorf("after the fix's next adoption: %s", problem)
	}
}

// A release of a pod whose issue runs no fix brings back no fix's working copy: the pod that
// replaces it holds the shared working copy alone, with the step's commit nobody pushed.
func TestAReleaseWithNoFixCopyMakesNone(t *testing.T) {
	testbin.RequireJJFilters(t)
	f := newBundleFixture(t)
	_, step := f.stepWork()
	saved := f.bundleWorkspaceOne()

	ws := f.secondPod("ws2")
	if notice := f.restoreInto(ws, saved, 1); !strings.Contains(notice, "is restored") {
		t.Fatalf("the restore's notice is %q, want the saved work restored", notice)
	}
	for _, problem := range f.stepCameBack(ws, step) {
		t.Errorf("after the release: %s", problem)
	}
	if _, err := os.Lstat(workspace.FixDir(ws)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the restore made %s (%v), want no fix's working copy for an issue that ran no fix", workspace.FixDir(ws), err)
	}
}

// stepWork leaves in the released pod's shared working copy a step's commit that nobody pushed,
// over the head of the issue's branch on the remote, and answers that head and the commit.
func (f *bundleFixture) stepWork() (head, step string) {
	f.t.Helper()
	head = f.run(f.ws, "jj", "log", "-R", f.ws, "--ignore-working-copy", "-r", `remote_bookmarks(exact:"legion/`+bundleIssue+`")`,
		"--no-graph", "-T", "commit_id")
	f.run(f.ws, "jj", "new", "-R", f.ws, head)
	f.write(filepath.Join(f.ws, "step.txt"), "step\n")
	f.run(f.ws, "jj", "commit", "-R", f.ws, "-m", "step work")
	return head, f.run(f.ws, "jj", "log", "-R", f.ws, "-r", "@-", "--no-graph", "-T", "commit_id")
}

// stepCameBack is what the shared working copy of ws lacks of the step's commit step: it stands
// under the working copy, with its file.
func (f *bundleFixture) stepCameBack(ws, step string) []string {
	f.t.Helper()
	var problems []string
	if parent, err := f.try(ws, "jj", "log", "-R", ws, "--ignore-working-copy", "-r", "@-", "--no-graph", "-T", "commit_id"); err != nil || parent != step {
		problems = append(problems, "the shared working copy of "+ws+" stands on "+parent+", want the step's commit "+step+" that nobody pushed")
	}
	if body, err := os.ReadFile(filepath.Join(ws, "step.txt")); err != nil || string(body) != "step\n" {
		problems = append(problems, "the shared working copy of "+ws+" holds step.txt as "+string(body)+", want the step's file")
	}
	return problems
}

// fixCopyProblems is what dir, the fix's working copy beside the shared workspace ws, lacks of the
// fix: a jj workspace of the repository named as the adoption names it, standing over the fix's
// commit fixed, with the fix's file and the edit that no jj command recorded before the release.
func (f *bundleFixture) fixCopyProblems(ws, dir, fixed string) []string {
	f.t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".jj")); err != nil {
		return []string{"there is no fix's working copy at " + dir + ": " + err.Error()}
	}
	var problems []string
	if listed, err := f.try(ws, "jj", "workspace", "list", "-R", ws, "--ignore-working-copy", "-T", `name ++ "\n"`); err != nil ||
		!strings.Contains("\n"+listed+"\n", "\n"+filepath.Base(dir)+"\n") {
		problems = append(problems, "the repository's workspaces are "+strings.ReplaceAll(listed, "\n", ", ")+", want "+filepath.Base(dir)+" among them")
	}
	if under, err := f.try(dir, "jj", "log", "-R", dir, "--ignore-working-copy", "-r", fixed+" & ::@", "--no-graph", "-T", "commit_id"); err != nil || under != fixed {
		problems = append(problems, "the fix's working copy at "+dir+" does not stand over the fix's commit "+fixed)
	}
	for name, want := range map[string]string{"fix.txt": "fix\n", "fix-wip.txt": "fix wip\n"} {
		if body, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(body) != want {
			problems = append(problems, "the fix's working copy at "+dir+" holds "+name+" as "+string(body)+", want "+strings.TrimSpace(want))
		}
	}
	return problems
}

// try runs one jj or git command in dir as run does, and answers its trimmed output and its error
// rather than failing the test: what a check that reports every problem it finds runs.
func (f *bundleFixture) try(dir string, args ...string) (string, error) {
	f.t.Helper()
	out, err := fixturegit.Runner{TB: f.t, Env: f.env, Ctx: f.ctx}.Try(dir, args...)
	return strings.TrimSpace(out), err
}

// adoptIn runs the real shim in ws, as the shim of a pod's role runs there, and answers its reply to
// one adopt-working-copy frame: the adoption the daemon sends before a fix's task.
func (f *bundleFixture) adoptIn(ws string, request shimwire.AdoptWorkingCopy) shimwire.AdoptWorkingCopyResult {
	f.t.Helper()
	bound := beforeDeadline(f.t)
	request.ID, request.JJUser, request.JJEmail = "adopt-1", "implementer", "implementer@example.test"
	request.TimeoutMs = int(bound.Milliseconds())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		f.t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(f.ctx)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_, _ = shim.Run(ctx, shim.Config{
			Network: "tcp", Address: listener.Addr().String(), BootToken: "boot",
			Argv: []string{"/bin/sh", "-c", "exec cat"},
			Env:  append(f.env, "LEGION_WORKSPACE="+ws),
		})
	}()
	defer func() {
		cancel()
		select {
		case <-exited:
		case <-time.After(cleanupBeforeDeadline(f.t)):
			f.t.Error("the shim did not leave after its context ended")
		}
	}()
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(bound)); err != nil {
		f.t.Fatal(err)
	}
	conn, err := listener.Accept()
	if err != nil {
		f.t.Fatalf("the shim's connection: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(bound)); err != nil {
		f.t.Fatal(err)
	}
	reader := shimwire.NewReader(bufio.NewReader(conn))
	if line, err := reader.ReadLine(); err != nil {
		f.t.Fatalf("the shim's hello: %v", err)
	} else if frame, err := shimwire.Decode(line); err != nil || frame.FrameType() != shimwire.TypeHello2 {
		f.t.Fatalf("the shim opened with %v (%v)", frame, err)
	}
	writer := shimwire.NewWriter(conn)
	if err := writer.WriteFrame(shimwire.HelloAck{}); err != nil {
		f.t.Fatal(err)
	}
	if err := writer.WriteFrame(request); err != nil {
		f.t.Fatal(err)
	}
	for {
		line, err := reader.ReadLine()
		if err != nil {
			f.t.Fatalf("read the shim's answer to the adoption: %v", err)
		}
		if frame, err := shimwire.Decode(line); err == nil {
			if result, ok := frame.(shimwire.AdoptWorkingCopyResult); ok && result.ID == request.ID {
				return result
			}
		}
	}
}
