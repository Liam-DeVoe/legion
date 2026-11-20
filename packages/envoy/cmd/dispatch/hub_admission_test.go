package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeHubAdmission is a private-hub admission proof main can run (hubAdmission): it admits every
// proof, answers ready as its last result, and closes started once its loop runs.
type fakeHubAdmission struct {
	ready   bool
	started chan struct{}
	once    sync.Once
}

func newFakeHubAdmission(ready bool) *fakeHubAdmission {
	return &fakeHubAdmission{ready: ready, started: make(chan struct{})}
}

func (a *fakeHubAdmission) Prove(context.Context) error { return nil }

func (a *fakeHubAdmission) Ready() bool { return a.ready }

func (a *fakeHubAdmission) Run(ctx context.Context) {
	a.once.Do(func() { close(a.started) })
	<-ctx.Done()
}

// waitRunning fails the test unless the proof's loop was started; the bound only ends a test whose
// loop never starts, and decides nothing that passes.
func (a *fakeHubAdmission) waitRunning(t *testing.T) {
	t.Helper()
	select {
	case <-a.started:
	case <-time.After(30 * time.Second):
		t.Fatal("startHubAdmission never started the proof's loop (Run)")
	}
}
