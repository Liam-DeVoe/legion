package textcheck

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/workflowstore"
)

// daemonRatingBounds is the file the daemon's TestPlanRatingGolden writes from the bounds it holds a
// retro's rating to (packages/daemon/internal/api/handoff.go), and that the contract's own test holds
// its bounds equal to.
const daemonRatingBounds = "../../../contracts/fixtures/daemon-api/plan-rating.json"

// Dispatch records every rating the daemon takes: the scale and the most characters of a reason that
// the store holds a rating to are the daemon's, read from the file the daemon writes them to. A bound
// moved on one side alone fails here, rather than as a rating the daemon took and Dispatch then
// refuses to record. A bound the file leaves out, or one it adds, fails too, since this test cannot
// compare it.
func TestDispatchsRatingBoundsAreTheDaemons(t *testing.T) {
	text, err := os.ReadFile(daemonRatingBounds)
	if err != nil {
		t.Fatalf("read %s: %v", daemonRatingBounds, err)
	}
	var daemon struct {
		Least      *int `json:"least"`
		Most       *int `json:"most"`
		ReasonMost *int `json:"reasonMost"`
	}
	decoder := json.NewDecoder(bytes.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&daemon); err != nil {
		t.Fatalf("decode %s: %v; this test compares least, most and reasonMost alone", daemonRatingBounds, err)
	}
	for _, bound := range []struct {
		name     string
		daemon   *int
		dispatch int
	}{
		{name: "least", daemon: daemon.Least, dispatch: workflowstore.RatingLeast},
		{name: "most", daemon: daemon.Most, dispatch: workflowstore.RatingMost},
		{name: "reasonMost", daemon: daemon.ReasonMost, dispatch: workflowstore.RatingReasonMost},
	} {
		switch {
		case bound.daemon == nil:
			t.Errorf("%s names no %s, so Dispatch's %d is held to nothing", daemonRatingBounds, bound.name, bound.dispatch)
		case *bound.daemon != bound.dispatch:
			t.Errorf("%s: the daemon's is %d and Dispatch's %d; the two take the same ratings only where they are equal",
				bound.name, *bound.daemon, bound.dispatch)
		}
	}
}
