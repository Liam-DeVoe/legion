package docs

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/store/storetest"
)

// TestARoomReadRefreshesAnExpiredListenerAnswerFirst: a session's file follows the session
// content rule (access.DocumentClause through access.PartyClause), which reads a listener list
// only from a stored listener answer that has not passed its expires_at, and judges a listed
// session of an expired one as a session with no list, which a session tied to nobody passes for
// no viewer but its own. So the document service's read of which rooms a person still reads
// (readableRooms: each access re-check, the re-check of a late admission, and a socket's re-ask)
// runs the API's refresh first (SetRosterRefresh, refreshRosterForReader).
//
// A session tied to nobody, listed with DOC (public), has a file; HID is private, so alice, an
// open viewer who sees DOC and not HID, does not see every document. The stored answer is an hour
// past its expires_at, and the refresh this test hands the service makes it fresh, as the API's
// does. alice reads the file's room. With no project private she reads every document, and the
// read refreshes nothing (R9).
//
// Controls: readableRooms without its refreshRosterForReader call, and alice does not read the
// file's room; refreshRosterForReader without its readsEveryDocument guard, and the read with no
// project private refreshes once.
func TestARoomReadRefreshesAnExpiredListenerAnswerFirst(t *testing.T) {
	database := storetest.Open(t)
	ctx := context.Background()
	createDocument(t, database, "# First")
	service := newTestServiceOn(t, database)
	const session = "session-listed-untied"
	var file string
	if err := database.Pool.QueryRow(ctx, `
		insert into artifacts (session_id, slug, name, kind, created_by)
		values ($1, 'photo-png', 'photo.png', 'image', jsonb_build_object('kind', 'session', 'id', $1::text))
		returning id::text
	`, session).Scan(&file); err != nil {
		t.Fatalf("store the session's file: %v", err)
	}
	if _, err := database.Pool.Exec(ctx, `
		insert into session_projects_filled default values;
		insert into route_roster (answered, sessions, sent_at, expires_at)
		values (true, '[{"session_id":"`+session+`","projects":["DOC"]}]', now() - interval '2 hours', now() - interval '1 hour');
		insert into projects (key, name, default_assignee) values ('HID', 'Hidden', 'carol@example.com');
		update projects set visibility = 'private' where key = 'HID';
	`); err != nil {
		t.Fatalf("seed the listener's expired answer and a private project: %v", err)
	}
	var refreshes atomic.Int64
	service.SetRosterRefresh(func(ctx context.Context) {
		refreshes.Add(1)
		if _, err := database.Pool.Exec(ctx, `update route_roster set expires_at = now() + interval '1 hour'`); err != nil {
			t.Errorf("refresh the stored listener answer: %v", err)
		}
	})
	probe := []*schemaAdmission{{key: schemaAdmissionKey{room: file}}}
	readable, err := service.readableRooms(ctx, "alice", probe)
	if err != nil {
		t.Fatalf("read which rooms alice reads: %v", err)
	}
	if !readable[file] || refreshes.Load() != 1 {
		t.Errorf("HID private, the stored answer expired: alice reads the file's room %t after %d refreshes, want true after 1: the read must refresh the answer before the content rule reads it",
			readable[file], refreshes.Load())
	}

	if _, err := database.Pool.Exec(ctx, `
		update projects set visibility = 'public' where key = 'HID';
		update route_roster set expires_at = now() - interval '1 hour';
	`); err != nil {
		t.Fatalf("make every project public and expire the answer: %v", err)
	}
	refreshes.Store(0)
	if readable, err = service.readableRooms(ctx, "alice", probe); err != nil {
		t.Fatalf("read which rooms alice reads, every project public: %v", err)
	}
	if !readable[file] || refreshes.Load() != 0 {
		t.Errorf("every project public: alice reads the file's room %t after %d refreshes, want true after none (R9)", readable[file], refreshes.Load())
	}
}
