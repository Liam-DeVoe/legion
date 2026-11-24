package supervise

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/phase"
	"github.com/sjawhar/legion/daemon/internal/runtime"
)

// workingCopyRecord is the issue's record as a test's Deps.WorkingCopy reads it, as the daemon's
// answer reads it at a send (workingCopy): the directory the runtime gives the repository the
// record names now, or the error a read of it fails with, and every delivery the send asked about.
type workingCopyRecord struct {
	mu    sync.Mutex
	dir   string
	err   error
	asked []string
}

// directoryClause is the directory a task's working copy line names, which the answer writes again.
var directoryClause = regexp.MustCompile(`, directory [^ ]+\.`)

func (r *workingCopyRecord) answer(_ context.Context, _ Claim, d Delivery) (string, []byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, d.ID)
	if r.err != nil {
		return "", nil, r.err
	}
	return directoryClause.ReplaceAllLiteralString(d.Task, ", directory "+r.dir+"."), d.News, nil
}

// moves is a correction of the issue's repository landing on the record, or the read of it failing.
func (r *workingCopyRecord) moves(dir string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dir, r.err = dir, err
}

func (r *workingCopyRecord) askedAbout() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.asked...)
}

// workingCopyTask is a phase worker's task whose working copy line names dir.
func workingCopyTask(dir string) string {
	return "Continue Workflow. Issue: LEGION-209. Phase: implementing. Working copy: branch legion/LEGION-209, directory " + dir +
		". Implement the plan."
}

const (
	leftDirectory  = "/workspace/acme/widgets/LEGION-209"
	movedDirectory = "/workspace/zeta/tools/LEGION-209"
)

// A phase worker's task is composed when its start is written and held until a send, and the
// issue's repository can move in between. startSend, the one place a prompt goes out, writes the
// task's working copy line from the record first (currentWorkingCopy, Deps.WorkingCopy), so every
// send names the directory the record gives at that send: the first send of a task the record
// moved under, and a resend after a send the transport lost, which went out before the move. The
// task it writes is persisted before it goes, under a new id, so a restart reads what was sent and
// the shim forwards the new text. A read that fails sends nothing and drops nothing: the task waits,
// charged nothing, and the next trigger sends it naming the record's directory. A task of no phase,
// an operator's own, is never asked about.
func TestEverySendOfAPhaseTaskNamesTheWorkingCopyTheRecordGivesAtThatSend(t *testing.T) {
	ready := func(t *testing.T, record *workingCopyRecord) *harness {
		h := newBareHarness(t)
		h.deps.WorkingCopy = record.answer
		if err := h.store.PutClaim(h.ctx, queuedClaim()); err != nil {
			t.Fatal(err)
		}
		h.start(queuedClaim())
		h.reach(StateReady)
		return h
	}
	task := RequestDeliver{Claim: testToken, ID: "outbox:30", Task: workingCopyTask(leftDirectory), Phase: phase.Implementing, Generation: 1}
	wantSentMoved := func(t *testing.T, h *harness, sent string) {
		t.Helper()
		if want := workingCopyTask(movedDirectory); sent != want {
			t.Fatalf("the worker was sent %q, naming the directory of the repository the issue left; want %q", sent, want)
		}
		p := h.pending()
		stored, ok := h.store.delivery(testToken)
		if p.Task != workingCopyTask(movedDirectory) || !ok || stored.Task != p.Task || stored.ID != p.ID || p.ID == task.ID {
			t.Fatalf("the delivery after the send = %+v, stored %+v (%v); want the task naming %s, persisted, under a new id",
				p, stored, ok, movedDirectory)
		}
	}

	t.Run("a task written before the move is sent naming the directory the record names at its send", func(t *testing.T) {
		record := &workingCopyRecord{dir: movedDirectory}
		h := ready(t, record)
		h.must(task)
		wantSentMoved(t, h, h.wantPrompts(1)[0].Message)
	})

	t.Run("a task sent before the move is sent again naming the directory the record names then", func(t *testing.T) {
		record := &workingCopyRecord{dir: leftDirectory}
		h := ready(t, record)
		h.conn.FailPrompt(errors.New("the stream to the shim closed"))
		h.must(task)
		if sent := h.wantPrompts(1)[0]; sent.Message != task.Task || sent.DeliveryID != task.ID {
			t.Fatalf("the send before the move = %+v, want the task as it was queued: the case's own premise", sent)
		}
		h.wantBudgets(Budgets{})

		record.moves(movedDirectory, nil)
		h.conn.FailPrompt(nil)
		h.observe(runtime.Alive)
		wantSentMoved(t, h, h.wantPrompts(2)[1].Message)
	})

	t.Run("a read of the record that fails sends nothing and drops nothing", func(t *testing.T) {
		record := &workingCopyRecord{err: errors.New("the database is not answering")}
		h := ready(t, record)
		if err := h.handle(task); err == nil || !strings.Contains(err.Error(), "the database is not answering") {
			t.Fatalf("the send whose read failed answered %v, want the read's error", err)
		}
		h.wantPrompts(0)
		if p := h.pending(); p.ID != task.ID || p.Task != task.Task {
			t.Fatalf("the delivery after the failed read = %+v, want the task kept as it was queued", p)
		}
		h.wantBudgets(Budgets{})

		record.moves(movedDirectory, nil)
		h.observe(runtime.Alive)
		wantSentMoved(t, h, h.wantPrompts(1)[0].Message)
	})

	t.Run("a task of no phase is sent as it was queued", func(t *testing.T) {
		record := &workingCopyRecord{dir: movedDirectory}
		h := ready(t, record)
		operators := RequestDeliver{Claim: testToken, Task: workingCopyTask(leftDirectory)}
		h.must(operators)
		if sent := h.wantPrompts(1)[0]; sent.Message != operators.Task {
			t.Fatalf("the operator's task was sent as %q, want it as the operator wrote it", sent.Message)
		}
		if asked := record.askedAbout(); len(asked) != 0 {
			t.Fatalf("the send asked the record about %v, want nothing asked of a task of no phase", asked)
		}
	})
}
