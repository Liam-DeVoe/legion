package requests

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/airlock"
	"github.com/sjawhar/envoy/internal/logtest"
)

// TestAListWhoseCallerWentAwayLogsNoRowOfItsOwn: the caller of a pending or a grant list goes
// away while the list still asks Dispatch about the reader's own rows, which are read on the
// list's context: an Inbox tab or the Live grants page closed. Dispatch was not left unanswering,
// so no row is logged as one Dispatch could not be asked about (rowUnansweredMessage, written with
// error=context canceled before). The rows are requests and grants that name no responsible person
// and no approver, read by carol, and touch TEAM by their reason, so each asks Dispatch a question
// that takes 300 ms; the caller leaves 400 ms in.
func TestAListWhoseCallerWentAwayLogsNoRowOfItsOwn(t *testing.T) {
	const rows = 5
	for _, list := range []struct {
		name string
		make func(t *testing.T, m *Machine, i int)
		read func(ctx context.Context, m *Machine) error
	}{
		{
			name: "the pending list",
			make: func(t *testing.T, m *Machine, i int) {
				pod, key := everyonesPod(t, m, fmt.Sprintf("gone-own-%02d", i), "")
				if req, err := m.Create(t.Context(), pod, signRequest(t, m, key, "TEAM-7: rotate the deploy key", "SHARED_KEY"), "", nil); err != nil || req.State != "pending" {
					t.Fatalf("Create = %+v, %v; want it pending", req, err)
				}
			},
			read: func(ctx context.Context, m *Machine) error {
				_, err := m.PendingForApprover(ctx, insider, PendingStates{})
				return err
			},
		},
		{
			name: "the grant list",
			make: func(t *testing.T, m *Machine, i int) {
				pod, key := newServiceEnrollment(t, m.Store, fixtureService, fixtureSubject, fmt.Sprintf("pod-gone-own-%02d-%s", i, t.Name()))
				grantOf(t, m, pod, key, "TEAM-7: deploy the broker", "SERVICE_KEY")
			},
			read: func(ctx context.Context, m *Machine) error { _, err := m.GrantsForApprover(ctx, insider); return err },
		},
	} {
		t.Run(list.name, func(t *testing.T) {
			m, _, _, _ := newProjectFixture(t, airlock.Rules{}, publicTeam)
			for i := range rows {
				list.make(t, m, i)
			}
			answerFrom(t, m, dispatchRows(publicTeam))
			var asked atomic.Int32
			m.Projects = slowAccess{inner: m.Projects, takes: 300 * time.Millisecond, asked: &asked}
			capture := &logtest.Recorder{}
			logtest.SetDefault(t, capture)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			time.AfterFunc(400*time.Millisecond, cancel)
			_ = list.read(ctx, m)
			if asked.Load() == 0 {
				t.Fatal("the list asked Dispatch nothing, so the caller left before any row was read and the row proves nothing")
			}
			if lines := linesOf(capture, rowUnansweredMessage); len(lines) != 0 {
				t.Errorf("%s whose caller went away wrote %d lines saying Dispatch could not be asked: %v; want none", list.name, len(lines), lines)
			}
		})
	}
}
