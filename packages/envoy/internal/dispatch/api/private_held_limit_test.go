package api

import (
	"fmt"
	"net/http"
	"testing"
)

// TestACommentPastThePrivateHeldLimitAnswers503 (events.Broker.HoldPrivate, ErrPrivateHeldFull):
// while the private-delivery hold is on, carol's three comments on PRIV's issue are written, and
// once the outbox holds as many of the private projects' events as it may - what those three left
// unpublished, the limit lowered through HoldPrivate's own argument - her fourth answers 503
// PRIVATE_PROJECTS_UNAVAILABLE and writes no comment and no event, and her comment on PUB's issue
// is written as before.
//
// Control, in the test: the same fourth comment on a server whose broker was given no hold is
// written, so the refusal is the hold's and not the seed's.
func TestACommentPastThePrivateHeldLimitAnswers503(t *testing.T) {
	held := seedPrivateProject(t)
	// The seed's own events are published, as an outbox would have them, so what the hold counts
	// is what carol writes here.
	if _, err := held.database.Pool.Exec(t.Context(), `update events set published_at = now() where published_at is null`); err != nil {
		t.Fatalf("publish the seed's events: %v", err)
	}
	unproven := func() bool { return false }
	held.deps.Events.HoldPrivate(unproven, 1<<30)
	for n := 1; n <= 3; n++ {
		held.mustRequest(t, callerCarol, http.MethodPost, "/api/v1/issues/"+held.issue[seedPrivate]+"/comments",
			map[string]string{"body": fmt.Sprintf("Held comment %d", n)}, http.StatusCreated)
	}
	// The limit is what the three comments left held: the next write of PRIV's is past it.
	var waiting int
	if err := held.database.Pool.QueryRow(t.Context(),
		`select count(*) from events where published_at is null and issue_key = $1`, held.issue[seedPrivate]).Scan(&waiting); err != nil || waiting < 3 {
		t.Fatalf("the three comments left %d events of %s unpublished (%v), want three at least", waiting, seedPrivate, err)
	}
	held.deps.Events.HoldPrivate(unproven, waiting)
	comments, events := privateWrites(t, held)
	refused := held.request(t, callerCarol, http.MethodPost, "/api/v1/issues/"+held.issue[seedPrivate]+"/comments",
		map[string]string{"body": "The fourth comment"})
	if refused.Code != http.StatusServiceUnavailable || decodeBody[errorBody](t, refused).Code != "PRIVATE_PROJECTS_UNAVAILABLE" {
		t.Fatalf("the fourth comment on %s answered %d %s, want 503 PRIVATE_PROJECTS_UNAVAILABLE", held.issue[seedPrivate], refused.Code, refused.Body)
	}
	if afterComments, afterEvents := privateWrites(t, held); afterComments != comments || afterEvents != events {
		t.Fatalf("the refused comment left %s with %d comments and %d events, want %d and %d: it writes nothing",
			seedPrivate, afterComments, afterEvents, comments, events)
	}
	held.mustRequest(t, callerCarol, http.MethodPost, "/api/v1/issues/"+held.issue[seedPublic]+"/comments",
		map[string]string{"body": "A public comment"}, http.StatusCreated)

	unheld := seedPrivateProject(t)
	for n := 1; n <= 4; n++ {
		unheld.mustRequest(t, callerCarol, http.MethodPost, "/api/v1/issues/"+unheld.issue[seedPrivate]+"/comments",
			map[string]string{"body": fmt.Sprintf("Comment %d", n)}, http.StatusCreated)
	}
}

// privateWrites is how many comments and events PRIV's issue holds in seed's database.
func privateWrites(t *testing.T, seed privateSeed) (comments, events int) {
	t.Helper()
	if err := seed.database.Pool.QueryRow(t.Context(), `
		select (select count(*) from comments where issue_key = $1), (select count(*) from events where issue_key = $1)
	`, seed.issue[seedPrivate]).Scan(&comments, &events); err != nil {
		t.Fatalf("count the writes of %s: %v", seed.issue[seedPrivate], err)
	}
	return comments, events
}
