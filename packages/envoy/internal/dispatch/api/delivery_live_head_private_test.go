package api

import (
	"strconv"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// TestADeliveryViewsChangePositionIsTheNewestChangeItsReaderSees: the change position a delivery
// view's snapshot names (seq, and changed_at, when that change was recorded) is a head its reader
// reads of the delivery change log, so it is the newest change of a repository that reader sees.
// A pull request of acme/secret, PRIV's repository, moves carol's position and leaves alice's
// where it stood, while a pull request of acme/widgets, PUB's, moves alice's.
//
// Control: name the log's newest change to every reader (read.seq); alice's position then moves
// with acme/secret's pull request, and the test fails naming it.
func TestADeliveryViewsChangePositionIsTheNewestChangeItsReaderSees(t *testing.T) {
	seed := seedPrivateProject(t)
	seedHubSettings(t, seed.database)
	if _, err := seed.database.Pool.Exec(t.Context(), `
		insert into repo_projects (repo, project, created_by) values
			('acme/widgets', 'PUB', '{"kind":"user","id":"operator"}'),
			('acme/secret', 'PRIV', '{"kind":"user","id":"operator"}')
	`); err != nil {
		t.Fatalf("map the two repositories: %v", err)
	}
	clock := newNoonHubClock()
	now := clock.Now()
	upsertPR(t, seed.database, hubPR(1, now.Add(-3*time.Hour)))
	rig := hubOn(t, seed.handler, seed.database, seed.deps, clock, nil)
	reader := func(login string) model.Actor {
		t.Helper()
		resolved, err := seed.server.readerFor(t.Context(), model.BrowserActor(login))
		if err != nil {
			t.Fatalf("resolve what %s sees: %v", login, err)
		}
		return resolved
	}
	alice, carol := reader(seedAlice), reader(seedCarol)
	// Each call subscribes a view no subscription holds yet (its window ends a minute later than
	// the last), so its snapshot is read now rather than kept from an earlier read.
	views := 0
	position := func(who model.Actor, what string) (int64, *time.Time) {
		t.Helper()
		views++
		conn := newFakeLiveConn()
		rig.hub.Subscribe(conn, deliverySubscription{Sub: "s", View: timelineRange(now.Add(-7*24*time.Hour), now.Add(time.Duration(views)*time.Minute), ""), Reader: who})
		snapshot := conn.snapshot(t, 5*time.Second, "s")
		seq, err := strconv.ParseInt(snapshot.Seq, 10, 64)
		if err != nil {
			t.Fatalf("%s: seq %q is no change position: %v", what, snapshot.Seq, err)
		}
		return seq, snapshot.ChangedAt
	}
	changeAt := func(seq int64) time.Time {
		t.Helper()
		var at time.Time
		if err := seed.database.Pool.QueryRow(t.Context(), `select at from delivery_changes where seq = $1`, seq).Scan(&at); err != nil {
			t.Fatalf("read when change %d was recorded: %v", seq, err)
		}
		return at
	}

	visible := latestChange(t, seed.database)
	before, beforeAt := position(alice, "alice before acme/secret's pull request")
	if before != visible {
		t.Fatalf("alice's position before acme/secret's pull request = %d, want acme/widgets#1's change %d", before, visible)
	}

	secret := hubPR(1, now.Add(-2*time.Hour))
	secret.Repo, secret.URL = "acme/secret", "https://github.com/acme/secret/pull/1"
	upsertPR(t, seed.database, secret)
	hidden := latestChange(t, seed.database)
	if hidden <= before {
		t.Fatalf("acme/secret's pull request logged no change above %d (newest %d)", before, hidden)
	}
	if seq, at := position(alice, "alice after acme/secret's pull request"); seq != before || at == nil || beforeAt == nil || !at.Equal(*beforeAt) {
		t.Errorf("alice's position after acme/secret's pull request = %d at %v, want it where it stood, %d at %v: it moved with a repository of PRIV", seq, at, before, beforeAt)
	}
	if seq, at := position(carol, "carol after acme/secret's pull request"); seq != hidden || at == nil || !at.Equal(changeAt(hidden)) {
		t.Errorf("carol's position = %d at %v, want acme/secret's change %d at %v", seq, at, hidden, changeAt(hidden))
	}

	upsertPR(t, seed.database, hubPR(2, now.Add(-time.Hour)))
	if seq, _ := position(alice, "alice after acme/widgets#2"); seq != latestChange(t, seed.database) {
		t.Errorf("alice's position after acme/widgets#2 = %d, want that change %d", seq, latestChange(t, seed.database))
	}
}
