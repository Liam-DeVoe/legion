package shim

import (
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/sjawhar/legion/daemon/internal/shimwire"
	"github.com/sjawhar/legion/daemon/internal/workspace"
)

// The workspace read: the shim of one role answers whether the workspace every role of its issue
// shares holds work that no branch or tag on origin holds (workspace.ReadWork, which counts
// workspace.WorkRevset). A pod update round that replaces the pod without waiting for every role
// reads it to name what goes with the pod, and the idle release reads it to take the pod only when
// it holds no work. The shim runs the jj of the workspace it already works in; where no role
// process runs, the pod's launcher makes the same read (internal/launcher's readWorkspace).

// workspaceWork answers `workspace-work`: the read of the work the workspace this shim runs in
// holds that origin does not. A request the shim cannot act on is answered with the reason when it
// names an id to answer. The answer lists the first shimwire.MaxWorkspaceWorkCommits work commits
// and counts them all, and a refusal carries at most maxAnswerErrorBytes of its reason
// (cutAnswerError), so the answer always fits one line.
func (s *shim) workspaceWork(request shimwire.WorkspaceWork) {
	result := shimwire.WorkspaceWorkResult{ID: request.ID, OK: true}
	if err := request.Validate(); err != nil {
		if request.ID == "" {
			s.log.Printf("[worker-shim] refusing a workspace-work frame: %v", err)
			return
		}
		result.OK, result.Error = false, cutAnswerError(err.Error())
	} else if head, changed, work, err := s.readWorkspaceWork(request); err != nil {
		result.OK, result.Error = false, cutAnswerError(err.Error())
	} else {
		result.Head, result.Changed, result.Count = head, changed, len(work)
		result.Commits = work[:min(len(work), shimwire.MaxWorkspaceWorkCommits)]
	}
	s.toDaemon(result)
}

// maxAnswerErrorBytes is the most of a refusal's reason that a one-line answer to the daemon
// carries: the answer to `workspace-work` (workspaceWork) and to `adopt-working-copy` (adopt).
// A failed command's reason holds all of jj's standard error (workspace.RunCommand), which has no
// bound of its own. Such an answer goes out as one line, and the writer refuses a line over
// shimwire.MaxFrameBytes: the outbox then closes the connection and sends that line first again
// at each reconnect, so the role's stream stays down (ASVS v5.0.0-15.2.2). A cut reason keeps its
// start, which names the command, its exit and jj's first words, and its end, where
// workspace.RunCommand puts the note it adds to a failure (workspace.StaleGitLockNote, which tells
// the role what to do about a git lock). The daemon names the reason on its log lines, and for the
// read in the message on the issue too, which a short reason keeps readable.
const maxAnswerErrorBytes = 4 << 10

// cutAnswerError is reason as a one-line answer carries it: all of it when it is at most
// maxAnswerErrorBytes, and otherwise its first and its last half of that bound, each cut where a
// UTF-8 character starts, with a line between them that says how many bytes the shim cut there and
// how long the reason was.
func cutAnswerError(reason string) string {
	if len(reason) <= maxAnswerErrorBytes {
		return reason
	}
	head := maxAnswerErrorBytes / 2
	for head > 0 && !utf8.RuneStart(reason[head]) {
		head--
	}
	tail := len(reason) - maxAnswerErrorBytes/2
	for tail < len(reason) && !utf8.RuneStart(reason[tail]) {
		tail++
	}
	return fmt.Sprintf("%s\n[The shim cut %d bytes of this error here. The error had %d bytes.]\n%s",
		reason[:head], tail-head, len(reason), reason[tail:])
}

// readWorkspaceWork runs the read (workspace.ReadWork) in the workspace the shim's environment
// names in LEGION_WORKSPACE, with the jj of PATH and within the request's budget, as the adoption
// does (openWorkspace). It answers the working copy's commit, whether that commit holds a change,
// and every work commit (workspace.WorkRevset), newest first; the one thing it writes is jj's
// snapshot of the working copy.
func (s *shim) readWorkspaceWork(request shimwire.WorkspaceWork) (head string, changed bool, work []string, err error) {
	opened, done, err := s.openWorkspace("read", s.cfg.Env, time.Duration(request.TimeoutMs)*time.Millisecond)
	if err != nil {
		return "", false, nil, err
	}
	defer done()
	run := func(args ...string) (string, error) { return opened.run(nil, args...) }
	return workspace.ReadWork(run, opened.jj, opened.dir)
}
