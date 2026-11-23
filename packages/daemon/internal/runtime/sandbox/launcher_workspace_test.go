package sandbox

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/shimwire"
)

// R6 of dispatch://INFRA-3271: the daemon asks an issue pod's launcher to read the workspace where
// no role process runs, and asks only a launcher whose hello said it reads one, since a launcher
// before it closes its connection on the frame (launchers.workspaceReader). Of a pod's two
// connected launchers, the one that said nothing is passed over, a launcher of another pod is never
// asked, and the one that said readsWorkspace is sent the read and its answer comes back to the
// request (ServeLauncher's workspace-work-result case).
func TestOnlyALauncherThatSaysItReadsAWorkspaceIsAskedToReadIt(t *testing.T) {
	launchers := testLaunchers()
	serve := func(token claim.Token, hello shimwire.LauncherHello) (*launcherSession, net.Conn) {
		t.Helper()
		session := launchers.accept(token, testTree, hello)
		server, peer := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
		go session.ServeLauncher(server, bufio.NewReader(server), shimwire.NewWriter(server))
		if err := shimwire.NewWriter(peer).WriteFrame(shimwire.LauncherState{}); err != nil {
			t.Fatalf("write the launcher's state: %v", err)
		}
		return session, peer
	}
	reader := claim.Token("legion-legion-legion-208-planner")
	serve(workerToken, shimwire.LauncherHello{PodUID: "pod-a", LauncherID: "older"})
	serve(claim.Token("legion-legion-legion-208-architect"), shimwire.LauncherHello{PodUID: "pod-b", LauncherID: "other-pod", ReadsWorkspace: true})
	session, peer := serve(reader, shimwire.LauncherHello{PodUID: "pod-a", LauncherID: "reader", ReadsWorkspace: true})

	deadline := time.Now().Add(5 * time.Second)
	var chosen *launcherSession
	for chosen == nil && time.Now().Before(deadline) {
		chosen = launchers.workspaceReader(testTree, "pod-a")
		time.Sleep(5 * time.Millisecond)
	}
	if chosen != session {
		t.Fatalf("the launcher asked to read pod-a's workspace = %v, want the one of pod-a that said it reads one (%s)", chosen, reader)
	}
	if none := launchers.workspaceReader(testTree, "pod-c"); none != nil {
		t.Fatalf("a pod with no launcher was given launcher %s to read through", none.token)
	}

	answered := make(chan error, 1)
	go func() {
		frames := shimwire.NewReader(bufio.NewReader(peer))
		line, err := frames.ReadLine()
		if err != nil {
			answered <- err
			return
		}
		frame, err := shimwire.Decode(line)
		if err != nil {
			answered <- err
			return
		}
		request, ok := frame.(shimwire.WorkspaceWork)
		if !ok {
			answered <- &unexpectedFrame{frame}
			return
		}
		answered <- shimwire.NewWriter(peer).WriteFrame(shimwire.WorkspaceWorkResult{ID: request.ID, OK: true,
			Head: "f2f134541b30dfa5160a5d1ce5e17b76c967c661", Count: 0})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	answer, _, err := launchers.requestSession(ctx, chosen, shimwire.WorkspaceWork{ID: "read-1", Issue: testTree, TimeoutMs: 1000}, "read-1")
	if err != nil {
		t.Fatalf("the read through the launcher: %v", err)
	}
	if err := <-answered; err != nil {
		t.Fatalf("the launcher's side of the read: %v", err)
	}
	if result, ok := answer.(shimwire.WorkspaceWorkResult); !ok || !result.OK || result.ID != "read-1" {
		t.Fatalf("the answer = %#v, want the launcher's result of read-1", answer)
	}
}

// A launcher that refuses the read is a read that could not be made (INFRA-341's reviewer's point
// 4 on R6). The daemon keeps the pod for any error of the read, and takes a read with no error as
// the disk's word, so a refusal answered as an empty read would have the disk deleted. The read
// through the pod's launcher (Runtime.launcherWorkspaceWork, PodWorkspaceWork's read of the pod it
// chose) answers the launcher's refusal as an error that names the pod and the launcher's reason,
// and names the pod on its empty answer too. The control is a launcher that read one commit, whose
// answer is that work in that pod.
func TestALauncherThatRefusesTheReadIsAReadThatCouldNotBeMade(t *testing.T) {
	const commit = "f2f134541b30dfa5160a5d1ce5e17b76c967c661"
	for _, tc := range []struct {
		name    string
		answer  shimwire.WorkspaceWorkResult
		refused string
	}{
		{"a_refusal", shimwire.WorkspaceWorkResult{OK: false, Error: "jj answered: the working copy is stale"}, "the working copy is stale"},
		{"control:_a_read_that_found_work", shimwire.WorkspaceWorkResult{OK: true, Head: commit, Count: 1, Commits: []string{commit}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launchers := testLaunchers()
			session := launchers.accept(claim.Token("legion-legion-legion-208-planner"), testTree,
				shimwire.LauncherHello{PodUID: "pod-a", LauncherID: "reader", ReadsWorkspace: true})
			server, peer := net.Pipe()
			t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
			go session.ServeLauncher(server, bufio.NewReader(server), shimwire.NewWriter(server))
			if err := shimwire.NewWriter(peer).WriteFrame(shimwire.LauncherState{}); err != nil {
				t.Fatalf("write the launcher's state: %v", err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for launchers.workspaceReader(testTree, "pod-a") == nil && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			answered := make(chan error, 1)
			go func() {
				line, err := shimwire.NewReader(bufio.NewReader(peer)).ReadLine()
				if err != nil {
					answered <- err
					return
				}
				frame, err := shimwire.Decode(line)
				if err != nil {
					answered <- err
					return
				}
				request, ok := frame.(shimwire.WorkspaceWork)
				if !ok {
					answered <- &unexpectedFrame{frame}
					return
				}
				result := tc.answer
				result.ID = request.ID
				answered <- shimwire.NewWriter(peer).WriteFrame(result)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			work, err := (&Runtime{launchers: launchers}).launcherWorkspaceWork(ctx, testTree, "pod-a")
			if sent := <-answered; sent != nil {
				t.Fatalf("the launcher's side of the read: %v", sent)
			}
			if work.PodUID != "pod-a" {
				t.Fatalf("the read names pod %q, want pod-a, the pod it read", work.PodUID)
			}
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("the read of a launcher that refused = %+v, %v; want an error naming the refusal and its reason", work, err)
				}
				if work.HoldsWork() || work.Count != 0 {
					t.Fatalf("the refused read answered work %+v, want none", work)
				}
				return
			}
			if err != nil || !work.HoldsWork() || work.Count != 1 || work.Head != commit {
				t.Fatalf("the read of a launcher that found one commit = %+v, %v; want that commit", work, err)
			}
		})
	}
}

// unexpectedFrame is a frame the launcher's side of a test did not expect.
type unexpectedFrame struct{ frame shimwire.Frame }

func (e *unexpectedFrame) Error() string { return "unexpected " + e.frame.FrameType() + " frame" }
