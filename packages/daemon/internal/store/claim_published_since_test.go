package store

import (
	"context"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/claim"
	"github.com/sjawhar/legion/daemon/internal/supervise"
)

// crossings reads the two times upsertClaim stamps on a claim: when it last entered the states in
// which the daemon publishes its session as its role's holder, and when it last left them.
func crossings(t *testing.T, store *Store, token claim.Token) (published, unpublished *time.Time) {
	t.Helper()
	if err := store.Pool().QueryRow(context.Background(),
		`select published_since, unpublished_since from claims where token = $1`, string(token)).Scan(&published, &unpublished); err != nil {
		t.Fatalf("read the crossings of %s: %v", token, err)
	}
	return published, unpublished
}

// sameTime says whether a and b are both unset or both the same moment.
func sameTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

// Each crossing of the states in which the daemon publishes a claim's session as its role's holder
// (registered, ready, working and idle: supervise.CapabilityStates) writes its own column, and a
// move between two published states or between two unpublished ones writes neither, so the repair
// can judge a session's write by the holders published when Dispatch recorded it
// (record.RoleHolderAt.PublishedAt). An insert in a published state is a crossing in, and an insert
// in another state writes neither. A move out writes unpublished_since and keeps the entry; a move
// in after it writes published_since anew, after that leave, and keeps the leave. idle to working,
// suspended to launching and launching to failed write neither.
func TestAClaimCrossingWritesItsPublicationTime(t *testing.T) {
	ctx := context.Background()
	store := migratedStore(t)
	c := tmuxClaim("legion-LEGION-209-implementer")
	put := func(state supervise.ClaimState) (published, unpublished *time.Time) {
		t.Helper()
		c.State = state
		if err := store.PutClaim(ctx, c); err != nil {
			t.Fatalf("put the claim at %s: %v", state, err)
		}
		return crossings(t, store, c.Token)
	}

	entered, unpublished := put(supervise.StateIdle)
	if entered == nil || unpublished != nil {
		t.Fatalf("an insert at idle left published %v, unpublished %v; want the insert's entry and no leave", entered, unpublished)
	}
	if published, unpublished := put(supervise.StateWorking); !sameTime(published, entered) || unpublished != nil {
		t.Fatalf("idle to working left published %v, unpublished %v; want %v kept and no leave", published, unpublished, entered)
	}
	published, left := put(supervise.StateSuspended)
	if !sameTime(published, entered) || left == nil || !left.After(*entered) {
		t.Fatalf("the move out to suspended left published %v, unpublished %v; want %v kept and a leave stamped after it", published, left, entered)
	}
	for _, state := range []supervise.ClaimState{supervise.StateLaunching, supervise.StateFailed} {
		if published, unpublished := put(state); !sameTime(published, entered) || !sameTime(unpublished, left) {
			t.Fatalf("the move to %s between unpublished states left published %v, unpublished %v; want %v and %v kept", state, published, unpublished, entered, left)
		}
	}
	reentered, unpublished := put(supervise.StateRegistered)
	if reentered == nil || !reentered.After(*left) || !sameTime(unpublished, left) {
		t.Fatalf("the move in to registered left published %v, unpublished %v; want an entry stamped after the leave %v, and the leave kept", reentered, unpublished, left)
	}

	queued := tmuxClaim("legion-LEGION-211-implementer")
	queued.Issue, queued.Session, queued.SessionFile, queued.State = "LEGION-211", "ses_queued", "/state/sessions/queued.jsonl", supervise.StateQueued
	if err := store.PutClaim(ctx, queued); err != nil {
		t.Fatalf("put a claim first written queued: %v", err)
	}
	if published, unpublished := crossings(t, store, queued.Token); published != nil || unpublished != nil {
		t.Fatalf("a claim first written queued holds published %v, unpublished %v; want neither: it has crossed nothing", published, unpublished)
	}
}
