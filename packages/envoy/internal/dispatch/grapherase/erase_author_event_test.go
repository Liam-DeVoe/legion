package grapherase

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// R5: the erase takes the bytes of its node and nothing another node holds. A comment's own event
// names, beside the body its author wrote, who wrote it: the session, the environment that session
// reported when it wrote (author.origin: its host, its working directory, its session title), the
// token it wrote with (author.act.token_name), and, in an ask's thread, the sessions that own the
// ask (ask_sessions). None of those is the comment's text; each is a fact of another node, and the
// event row's own actor column keeps them as written (redactEvents moves the payload alone). The
// comment erased here reads `approved`, a word each of those fields happens to hold. The erase
// replaces the body with the marker and leaves every one of those fields as the event wrote it.
func TestTheCommentEraseLeavesWhoWroteItInItsEvent(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	seeded := seedAgreeingStore(t, database)
	if _, err := database.Pool.Exec(ctx, `update comments set body = 'approved' where id = $1::uuid`, seeded.comment); err != nil {
		t.Fatalf("write the comment's body: %v", err)
	}
	const (
		cwd        = "/work/approved-specs"
		host       = "approved-host"
		title      = "approved specs sweep"
		tokenName  = "approved-bot"
		askSession = "approved-sweep-session"
	)
	event := appendEvent(t, database, "CORE-1", "comment.created", fmt.Sprintf(`{"id": %q, "body": "approved",
		"author": {"kind": "session", "id": "sweep-session", "origin": {"host": %q, "machine": "build-1", "cwd": %q, "session_title": %q},
		           "act": {"token_name": %q}},
		"ask_sessions": [%q]}`, seeded.comment, host, cwd, title, tokenName, askSession))

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
	act, _ := author["act"].(map[string]any)
	sessions, _ := payload["ask_sessions"].([]any)
	for _, field := range []struct {
		name      string
		got, want any
	}{
		{"author.origin.cwd", origin["cwd"], cwd},
		{"author.origin.host", origin["host"], host},
		{"author.origin.session_title", origin["session_title"], title},
		{"author.act.token_name", act["token_name"], tokenName},
		{"ask_sessions[0]", first(sessions), askSession},
	} {
		if field.got != field.want {
			t.Errorf("after the erase of comment %s its event's %s reads %q, want it as the event wrote it, %q: it says who wrote the comment, which is no byte of the comment's own text",
				seeded.comment, field.name, field.got, field.want)
		}
	}
	if strings.Count(raw, "approved") < 5 {
		t.Errorf("after the erase of comment %s its event holds %d of the five fields that name its author's facts as written: %s", seeded.comment, strings.Count(raw, "approved"), raw)
	}
}

// first is the first element of list, nil where it holds none.
func first(list []any) any {
	if len(list) == 0 {
		return nil
	}
	return list[0]
}
