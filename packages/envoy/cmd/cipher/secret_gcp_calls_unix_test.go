//go:build unix

// packages/envoy/cmd/cipher/secret_gcp_calls_unix_test.go
//
// The part of a gcloud call that is another process's work, for the rows that time code which
// makes gcloud calls: from the moments runRecorded reports (onGcloudStep) and the kill its call's
// deadline sends (signalGroup).
package main

import (
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"
)

// gcloudCall is one gcloud call as recordGcloudCalls saw it: when runRecorded had started it
// (gcloudStarted), when its deadline's kill sent its process group SIGKILL (killGroupOnCancel; zero
// where the call ended by itself), and when runRecorded had waited for it (gcloudWaited), the
// process reaped and its output closed.
type gcloudCall struct {
	started, killed, waited time.Time
}

// inAnotherProcess is how long call spent on another process's work, which the stall meter, counting
// only the time this process was held up, does not see: for a call its deadline killed, from the
// kill to the wait, the process group (on Linux, gcloud's supervisor, a run of this binary, among
// it) torn down and reaped; for a call that ended by itself, from its start to its wait, the
// supervisor's start and end among it. The time before the kill of a killed call is the deadline's,
// which the row times itself.
func (c gcloudCall) inAnotherProcess() time.Duration {
	if !c.killed.IsZero() {
		return c.waited.Sub(c.killed)
	}
	return c.waited.Sub(c.started)
}

// recordGcloudCalls records, for the rest of t, each gcloud call runRecorded makes: its start and
// its wait (onGcloudStep), and the first SIGKILL sent to a process group between them (signalGroup,
// which sends it on), the kill of the call's deadline. Calls run one at a time, as the forms make
// them. It answers how to read the calls back, in the order they started; t fails there where a
// call started before the one before it had been waited for.
func recordGcloudCalls(t *testing.T) func() []gcloudCall {
	t.Helper()
	var mu sync.Mutex
	var calls []gcloudCall
	open, overlapped := false, false
	restoreStep, restoreSignal := onGcloudStep, signalGroup
	t.Cleanup(func() { onGcloudStep, signalGroup = restoreStep, restoreSignal })
	onGcloudStep = func(step string) {
		now := time.Now()
		mu.Lock()
		defer mu.Unlock()
		switch step {
		case gcloudStarted:
			overlapped = overlapped || open
			calls, open = append(calls, gcloudCall{started: now}), true
		case gcloudWaited:
			if open {
				calls[len(calls)-1].waited, open = now, false
			}
		}
	}
	signalGroup = func(pgid int, sig syscall.Signal) error {
		now := time.Now()
		mu.Lock()
		if open && sig == syscall.SIGKILL && calls[len(calls)-1].killed.IsZero() {
			calls[len(calls)-1].killed = now
		}
		mu.Unlock()
		return restoreSignal(pgid, sig)
	}
	return func() []gcloudCall {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if overlapped {
			t.Fatal("a gcloud call started before the one before it had been waited for")
		}
		return slices.Clone(calls)
	}
}

// inAnotherProcessAll is the sum of each call's inAnotherProcess. t fails where a call has not been
// waited for.
func inAnotherProcessAll(t *testing.T, calls []gcloudCall) time.Duration {
	t.Helper()
	var in time.Duration
	for i, c := range calls {
		if c.waited.IsZero() {
			t.Fatalf("gcloud call %d of %d had not been waited for", i+1, len(calls))
		}
		in += c.inAnotherProcess()
	}
	return in.Round(time.Millisecond)
}
