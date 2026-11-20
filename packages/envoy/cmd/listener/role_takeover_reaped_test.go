package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/internal/session"
)

// The role claim reaper frees the claim of a holder that stopped, and it writes the takeover
// record a claim from another session writes before it does (reapedClaimRecord): the claim it
// frees names no holder after it, so the next holder's claim writes no record of its own, and a
// role message forwarded to the stopped holder would otherwise stay unread on that holder's own
// subject. The previous holder here registers through the routes of a machine whose listener is
// gone (crashRig.away), so no session relay reads its subject and the copy the next holder gets is
// the record's pass's alone.

// reapedHolderRow makes previous hold role with one role message forwarded to it and, when
// reads, read it; then previous stops (its session record deleted, its claim aged past the live
// window), and a pass of the role claim reaper frees its claim with the record main.go gives it.
// It returns the message's event id once the role has no holder.
func reapedHolderRow(rig *crashRig, previous, role string, reads bool) string {
	rig.t.Helper()
	var holder *sessionStandIn
	if reads {
		holder = rig.relayed(previous, answerDone)
		rig.claim(rig.mux, previous, role)
	} else {
		rig.gone(previous)
		rig.claim(rig.away, previous, role)
	}
	item := rig.onLane(role, "forwarded to "+previous+" before it stopped")
	rig.awaitStored(contracts.LegacyAgentSubject(previous), item.EventID, "the role lane's forward to "+previous)
	if reads {
		crashAwaitEvents(rig.t, holder, []string{item.EventID}, nil, previous+", which reads what it is forwarded")
		holder.conn.Close()
	}
	if recorder := rig.send(rig.mux, http.MethodDelete, "/v1/sessions/"+previous, ""); recorder.Code != http.StatusOK {
		rig.t.Fatalf("DELETE /v1/sessions/%s: status %d %s", previous, recorder.Code, recorder.Body.String())
	}
	waitFor(rig.t, 10*time.Second, previous+" read as not live", func() bool {
		_, state := rig.harness.sessions.Lookup(previous)
		return state != session.Live
	})
	ageRoleClaim(rig.t, rig.deps, role, 2*session.LiveWindow)
	isAlive := func(sessionID string) bool {
		return roleHolderMayBeLive(rig.harness.config.logger, rig.harness.sessions, sessionID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if sweep, err := rig.harness.registry.ReapRoleClaims(ctx, isAlive, rig.harness.sessions.TTL(), session.LiveWindow, reapedClaimRecord(rig.cfg())); err != nil || sweep.Reaped != 1 {
		rig.t.Fatalf("reap the role claims = %+v, %v; want role %s's claim freed", sweep, err, role)
	}
	if claimed := rig.holder(role); claimed != "" {
		rig.t.Fatalf("role %s's claim names %q after the reaper's pass; want none", role, claimed)
	}
	return item.EventID
}

func TestARoleMessageForwardedToAHolderThatStoppedReachesTheHolderAfterTheReap(t *testing.T) {
	rig := newCrashRig(t)
	const previous, next, role = "ses_reaped_a", "ses_reaped_b", "reaped"
	event := reapedHolderRow(rig, previous, role, false)
	standIn := rig.relayed(next, answerDone)
	rig.claim(rig.mux, next, role)
	got := crashAwaitEvents(t, standIn, []string{event}, nil, "the role's next holder, which claimed it after the reaper freed the stopped holder's claim")
	got = append(got, crashDrain(standIn, 3*time.Second)...)
	events := make([]string, len(got))
	for i, frame := range got {
		events[i] = frame.item.EventID
	}
	crashRequireOneEach(t, "the frames "+next+" was handed", events, []string{event})
}

// The control: the previous holder read the message before it stopped, so the reaper's record
// finds nothing unread, and the next holder is handed nothing.
func TestARoleMessageTheReapedHolderReadIsNotHandedToTheNextHolder(t *testing.T) {
	rig := newCrashRig(t)
	const previous, next, role = "ses_reaped_read_a", "ses_reaped_read_b", "reaped-read"
	reapedHolderRow(rig, previous, role, true)
	standIn := rig.relayed(next, answerDone)
	rig.claim(rig.mux, next, role)
	if got := crashDrain(standIn, 5*time.Second); len(got) != 0 {
		t.Fatalf("%s was handed %v; want nothing, since %s read the one message it was forwarded", next, frameEvents(got), previous)
	}
}
