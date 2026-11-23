package launcher

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/sjawhar/legion/daemon/internal/procgroup"
	"github.com/sjawhar/legion/daemon/internal/redact"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// The launcher's workspace read (R6 of dispatch://INFRA-3271): the daemon releases a pod no claim
// owns, or closes a left pod with no live claim, only after a read of the issue workspace found
// nothing that origin does not hold. No role process runs in such a pod, so no role's shim can
// read it; the launcher runs there for the pod's whole life, and its working directory is the
// issue's workspace (internal/runtime/sandbox's rolesDirLauncher, WorkingDir). It makes the read a
// shim makes (workspace.ReadWork), so the two count the same work, and answers on the connection
// of the role the daemon asked through. Its hello says it answers (shimwire.LauncherHello's
// ReadsWorkspace), since a launcher before it closes its connection on the frame.

// launcherReadConfig are the jj options the launcher's read runs with, ahead of each command's
// own. The read's first command snapshots the working copy, which writes a commit, and a repository
// configuration a role wrote could have that write run a signing program (signing.behavior force
// with a program of the role's choosing) or a file monitor. The launcher holds every role's
// launcher token, which no role may read, so it runs neither: command-line configuration outranks
// the repository's and the user's.
var launcherReadConfig = []string{"--config=signing.behavior=drop", "--config=core.fsmonitor=none"}

// maxReadErrorRunes bounds the reason a refused read carries: jj's standard error has no bound of
// its own, and the answer goes out as one line under shimwire.MaxFrameBytes.
const maxReadErrorRunes = 2048

// readWorkspace answers one shimwire.WorkspaceWork on this role's connection, writer. It runs on a
// goroutine of its own (serve), so the role's start and stop commands do not wait behind jj; the
// writer has a mutex of its own, so its answer and the command loop's never interleave. A request
// with no id names nothing to answer, and is logged and dropped.
func (m *manager) readWorkspace(ctx context.Context, writer *shimwire.Writer, request shimwire.WorkspaceWork) {
	result := shimwire.WorkspaceWorkResult{ID: request.ID, OK: true}
	if err := request.Validate(); err != nil {
		if request.ID == "" {
			m.logf("launcher: refusing a workspace-work frame: %v", err)
			return
		}
		result.OK, result.Error = false, err.Error()
	} else if head, changed, work, err := m.workspaceWork(ctx, request); err != nil {
		result.OK, result.Error = false, redact.Runes(err.Error(), maxReadErrorRunes)
	} else {
		result.Head, result.Changed, result.Count = head, changed, len(work)
		result.Commits = work[:min(len(work), shimwire.MaxWorkspaceWorkCommits)]
	}
	if err := writer.WriteFrame(result); err != nil {
		m.logf("launcher: answer the workspace read %s: %v", request.ID, err)
	}
}

// workspaceWork runs the read in the issue workspace, within the request's budget and the
// container's context: Config.Workspace where a test names one, and otherwise the launcher's own
// working directory, which the pod sets to the issue's workspace. The jj is PATH's, the image's own,
// and each command starts through the launcher's pid guard, so PID 1's reaper leaves its exit to
// the command's own wait (pidGuard.starter).
func (m *manager) workspaceWork(ctx context.Context, request shimwire.WorkspaceWork) (head string, changed bool, work []string, err error) {
	dir := m.cfg.Workspace
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return "", false, nil, fmt.Errorf("launcher: the launcher's working directory, which is the issue's workspace, cannot be read: %w", err)
		}
	}
	jj, err := exec.LookPath("jj")
	if err != nil {
		return "", false, nil, fmt.Errorf("launcher: jj is not on PATH (%s); no jj to read the workspace with", os.Getenv("PATH"))
	}
	start := procgroup.NoReaper
	if m.guard != nil {
		start = m.guard.starter()
	}
	budget := time.Duration(request.TimeoutMs) * time.Millisecond
	bounded, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	started, env := time.Now(), os.Environ()
	run := func(args ...string) (string, error) {
		return workspace.RunCommand(bounded, start, budget, started, env, dir, nil, args)
	}
	return workspace.ReadWork(run, jj, dir, launcherReadConfig...)
}
