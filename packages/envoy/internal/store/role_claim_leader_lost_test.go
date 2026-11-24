package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/testnats"
)

// leaderReadLostBound is the bound the row holds its read to, internal/bus's lostReadBound: well
// above the reconnect the row cuts the connection into (nats.go dials the same server again after
// its ReconnectWait, 1 s), which the row requires to take at most a quarter of it, so a read that
// fails before half of it failed at the reconnect and not at its bound.
const leaderReadLostBound = 8 * time.Second

// A read of a role's claim from the role bucket stream's leader (Registry.RoleClaimFromLeader, the
// read the session relay judges a role message by and the kept lane's handover resolves the role's
// holder with) whose request reached the socket and died with it fails with
// bus.ErrRequestLostWithConnection at the reconnect, rather than waiting out its bound for an
// answer nothing will send: nats.go fails no request its connection drops. The read is a stream
// message get, `$JS.API.STREAM.MSG.GET.KV_<role bucket>`, on the registry's handle, which is
// opened on the connection of the client that dialled through the relay; the relay drops that
// request, and the cut comes once the request is out. The reconnect takes at most a quarter of the
// bound, and the read fails before half of it; the read made again on the connection that came
// back answers the claim. It is internal/bus's
// TestALeaderReadLostWithItsConnectionFailsAtTheReconnectNotAtItsBound for the registry's read.
// Control: with the read made without bus.BoundedOn, it waits out its bound and the row fails.
func TestARoleClaimFromTheLeaderLostWithItsConnectionFailsAtTheReconnectNotAtItsBound(t *testing.T) {
	const (
		holder = "ses_role_claim_leader_lost"
		role   = "reviewer"
	)
	relay := testnats.StartUnansweredRelayOn(t, testnats.URL(t), "$JS.API.STREAM.MSG.GET.KV_"+bus.RoleClaimBucket)
	client, err := bus.Connect([]string{relay.URL()})
	if err != nil {
		t.Fatalf("connect through the relay: %v", err)
	}
	t.Cleanup(client.Close)
	registry := openRegistry(t, client.Conn)
	if _, err := registry.SetRole(holder, "test-machine", role, false); err != nil {
		t.Fatalf("%s claims %s: %v", holder, role, err)
	}
	conn := client.Connection()

	// The relay drops the next stream message get of the role bucket, the read's, and the links are
	// cut once it is out: the order of a request whose bytes reached the socket and died with it.
	relay.Arm(1)
	began := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), leaderReadLostBound)
	defer cancel()
	type answer struct {
		err error
		at  time.Time
	}
	answers := make(chan answer, 1)
	go func() {
		_, err := registry.RoleClaimFromLeader(ctx, role)
		answers <- answer{err: err, at: time.Now()}
	}()
	waitFor(t, leaderReadLostBound, func() bool { return relay.Sent() >= 1 })
	before := conn.Stats().Reconnects
	relay.Drop()
	waitFor(t, leaderReadLostBound, func() bool { return conn.Stats().Reconnects > before })
	reconnected := time.Now()
	if took := reconnected.Sub(began); took > leaderReadLostBound/4 {
		t.Fatalf("the connection reconnected %s after the read began, past a quarter of its %s bound: the row cannot tell a read that failed at the reconnect from one that failed at its bound",
			took.Round(time.Millisecond), leaderReadLostBound)
	}
	var got answer
	select {
	case got = <-answers:
	case <-time.After(leaderReadLostBound + time.Second):
		t.Fatalf("the leader read of %s's claim whose connection was cut has not answered %s after it began", role, leaderReadLostBound+time.Second)
	}
	if took := got.at.Sub(began); !errors.Is(got.err, bus.ErrRequestLostWithConnection) || took >= leaderReadLostBound/2 {
		t.Fatalf("the leader read of %s's claim whose connection was cut under it answered %v, %s after it began and %s after the reconnect; want %v before half its %s bound",
			role, got.err, took.Round(time.Millisecond), got.at.Sub(reconnected).Round(time.Millisecond), bus.ErrRequestLostWithConnection, leaderReadLostBound)
	}

	// The read made again on the connection that came back is answered.
	again, cancelAgain := context.WithTimeout(context.Background(), leaderReadLostBound)
	defer cancelAgain()
	if claim, err := registry.RoleClaimFromLeader(again, role); err != nil || claim.HolderSessionID != holder {
		t.Fatalf("the leader read of %s's claim again after the reconnect = %+v, %v; want %s's claim", role, claim, err, holder)
	}
}
