package api

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/store"
)

// pageDrawn is what the Inbox page draws for login in one state, read through the one read (GET
// /api/v1/me/inbox?state=): the thread ids of its Thread and Agent rows and the ids of its Ask
// rows, each sorted.
func pageDrawn(t *testing.T, handler http.Handler, login, state string) (threads, asks []string) {
	t.Helper()
	threads, asks = []string{}, []string{}
	for _, entry := range readMeInboxAs(t, handler, login, url.Values{"state": {state}}).Entries {
		switch {
		case entry.Thread != nil:
			threads = append(threads, entry.Thread.ThreadID)
		case entry.Agent != nil:
			threads = append(threads, entry.Agent.ThreadID)
		case entry.Ask != nil:
			asks = append(asks, entry.Node.ID)
		}
	}
	slices.Sort(threads)
	slices.Sort(asks)
	return threads, asks
}

// threadRowStored is everything a write over the Inbox's rows can change in one user_threads row,
// read as text so two readings compare whole: the read mark, whether it is unread, the dismissal
// (empty when there is none) and the version a dismissal is held to.
func threadRowStored(t *testing.T, database *store.Store, login, threadID string) string {
	t.Helper()
	var stored string
	if err := database.Pool.QueryRow(context.Background(), `
		select concat_ws(' ', 'read_through=' || read_through::text, 'unread=' || unread::text,
		                 'dismissed_at=' || coalesce(dismissed_at::text, ''), 'version=' || version::text)
		from user_threads where login = $1 and thread_id = $2
	`, login, threadID).Scan(&stored); err != nil {
		t.Fatalf("read %s's row on %s: %v", login, threadID, err)
	}
	return stored
}

// Mark all read and the dismissal write over the rows the page draws and over no other row: both
// read their rows through the page's own fragment (meInboxPageRows), so each marks exactly the rows
// the page drew. The seed holds, for Alice, four unread Thread rows the page draws - comment threads
// on two projects' issues and on a project document - and an unread Agent row, and one unread
// user_threads row the one read answers that the page draws no Thread row for: the thread of an
// open ask on an open issue assigned to her, which the page draws as that ask's Ask row instead.
//
// Mark all read marks every row the page drew under state=unread and leaves that row's read state
// as it was. A dismissal naming a row the page drew takes it out of the page and into the Dismissed
// view; a dismissal naming the ask's thread, carrying the version its row holds, writes nothing; and
// a filtered dismissal takes every row the page still draws and leaves that row alone too.
func TestTheInboxWritesMarkTheRowsThePageDrawsAndNoOther(t *testing.T) {
	seed := seedInboxRead(t)
	handler, database := seed.handler, seed.database

	assignIssue(t, handler, seed.firstIssue, "alice")
	ask := createAnswerListAsk(t, handler, seed.firstIssue, "Which way on the deploy?")
	createThreadComment(t, handler, seed.firstIssue, map[string]any{"body": "Why ask?", "ask_id": ask.ID}, "alice")
	sessionComment(t, handler, "/api/v1/issues/"+seed.firstIssue+"/comments", map[string]any{"body": "Because.", "ask_id": ask.ID})
	notDrawn := threadRowStored(t, database, "alice", ask.ID)
	if _, unread := threadMark(t, database, "alice", ask.ID); !unread {
		t.Fatalf("the open ask's thread row is read before any write: %s, want it unread, so a write that marked it would show", notDrawn)
	}

	drawn := []string{seed.first, seed.second, seed.other, seed.document, seed.agent}
	slices.Sort(drawn)
	if threads, _ := pageDrawn(t, handler, "alice", myRepliesStateUnread); !slices.Equal(threads, drawn) {
		t.Fatalf("the page draws %v under state=unread, want the four comment threads and the agent thread %v", threads, drawn)
	}
	if threads, asks := pageDrawn(t, handler, "alice", myRepliesStateInbox); slices.Contains(threads, ask.ID) || !slices.Contains(asks, ask.ID) {
		t.Fatalf("the page draws threads %v and asks %v under state=inbox, want %s as an Ask row and as no Thread row", threads, asks, ask.ID)
	}

	// Mark all read through a mark after every write, so only the fragment's filter keeps the
	// ask's thread out of what it marks.
	if marked := markedThrough(t, handler, "alice", time.Now(), nil); marked != len(drawn) {
		t.Fatalf("Mark all read marked %d threads, want the %d rows the page drew", marked, len(drawn))
	}
	for _, thread := range []string{seed.first, seed.second, seed.other, seed.document} {
		if _, unread := threadMark(t, database, "alice", thread); unread {
			t.Fatalf("thread %s is unread after Mark all read, want every Thread row the page drew marked", thread)
		}
	}
	if threads, _ := pageDrawn(t, handler, "alice", myRepliesStateUnread); len(threads) != 0 {
		t.Fatalf("the page still draws %v under state=unread after Mark all read, want none", threads)
	}
	if stored := threadRowStored(t, database, "alice", ask.ID); stored != notDrawn {
		t.Fatalf("Mark all read changed the open ask's thread row, which the page does not draw: %s, want %s", stored, notDrawn)
	}

	// A dismissal naming a row the page drew takes that row alone out of the page.
	if dismissed := dismissRows(t, handler, database, "alice", inboxDismissReplyPrefix+seed.first); dismissed != 1 {
		t.Fatalf("the dismissal of %s changed %d threads, want 1", seed.first, dismissed)
	}
	if threads, _ := pageDrawn(t, handler, "alice", myRepliesStateInbox); slices.Contains(threads, seed.first) {
		t.Fatalf("the page still draws the dismissed thread %s under state=inbox: %v", seed.first, threads)
	}
	if threads, _ := pageDrawn(t, handler, "alice", myRepliesStateDismissed); !slices.Equal(threads, []string{seed.first}) {
		t.Fatalf("the Dismissed view draws %v, want the dismissed thread %s alone", threads, seed.first)
	}

	// A dismissal naming the ask's thread, with the version its row holds, writes nothing.
	if dismissed := dismissRows(t, handler, database, "alice", inboxDismissReplyPrefix+ask.ID); dismissed != 0 {
		t.Fatalf("the dismissal naming the open ask's thread changed %d threads, want none: the page draws no Thread row for it", dismissed)
	}
	if stored := threadRowStored(t, database, "alice", ask.ID); stored != notDrawn {
		t.Fatalf("the dismissal naming the open ask's thread changed its row: %s, want %s", stored, notDrawn)
	}

	// A filtered dismissal takes every row the page still draws, and that row stays as it was.
	if dismissed := dismissThrough(t, handler, "alice", time.Now(), nil); dismissed != len(drawn)-1 {
		t.Fatalf("the filtered dismissal changed %d threads, want the %d rows the page still drew", dismissed, len(drawn)-1)
	}
	if threads, _ := pageDrawn(t, handler, "alice", myRepliesStateInbox); len(threads) != 0 {
		t.Fatalf("the page still draws %v under state=inbox after the filtered dismissal, want none", threads)
	}
	if stored := threadRowStored(t, database, "alice", ask.ID); stored != notDrawn {
		t.Fatalf("the filtered dismissal changed the open ask's thread row: %s, want %s", stored, notDrawn)
	}
}
