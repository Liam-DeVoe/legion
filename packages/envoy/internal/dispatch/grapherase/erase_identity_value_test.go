package grapherase

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// R5: the redaction never rewrites an identity value of a comment's own event, even one that is
// the erased text whole. TestTheCommentEraseLeavesWhoWroteItInItsEvent holds each identity field
// to the erased word inside a longer line, which the quoted-words class leaves too, since it
// rewrites an erased text only where it stands as a block of its own (redactBlockQuote), so it
// cannot tell an identity path from a quoted one. Here the comment reads one word, and both the
// host its author's session reported (author.origin.host) and the session that owns the ask its
// thread is in (ask_sessions[0]) read exactly that word: a value that is the erased text whole
// stands as a block of its own, so quoted words would take the marker there, and own words would
// take it anywhere. The erase replaces the body with the marker and leaves both values as the
// event wrote them.
func TestTheCommentEraseLeavesAnIdentityValueThatIsItsWholeText(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seeded := seedAgreeingStore(t, database)
	const word = "kestrel7ridge"
	if _, err := database.Pool.Exec(ctx, `update comments set body = $2 where id = $1::uuid`, seeded.comment, word); err != nil {
		t.Fatalf("write the comment's body: %v", err)
	}
	event := appendEvent(t, database, "CORE-1", "comment.created", fmt.Sprintf(`{"id": %q, "body": %q,
		"author": {"kind": "session", "id": "sweep-session", "origin": {"host": %q, "machine": "build-1"}},
		"ask_sessions": [%q]}`, seeded.comment, word, word, word))

	erasure, err := confirmedErase(t, database, commentNodeKind, seeded.comment, time.Millisecond)
	if err != nil {
		t.Fatalf("erase %s: %v", seeded.comment, err)
	}
	requireTheCommentErased(t, database, erasure, seeded.comment)
	payload, raw := eventPayload(t, database, event)
	if payload["body"] != erasure.marker() {
		t.Fatalf("after the erase the comment's event body reads %v, want the marker: the run redacted no body and this test holds nothing: %s", payload["body"], raw)
	}
	author, _ := payload["author"].(map[string]any)
	origin, _ := author["origin"].(map[string]any)
	var session any
	if sessions, _ := payload["ask_sessions"].([]any); len(sessions) > 0 {
		session = sessions[0]
	}
	for _, field := range []struct {
		name string
		got  any
	}{
		{"author.origin.host", origin["host"]},
		{"ask_sessions[0]", session},
	} {
		if field.got != word {
			t.Errorf("after the erase of comment %s its event's %s reads %q, want it as the event wrote it, %q, the comment's whole text: it is identity, which the redaction never rewrites, and the one class that leaves a value that is the erased text whole: %s",
				seeded.comment, field.name, field.got, word, raw)
		}
	}
}
