package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/admission"
	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// An issue's references read takes no heavy read lane since it reads one hop in each direction
// (refs.Closure): its statements run one after another on its one connection, so a flood of it is
// bounded by the read class alone, as an ordinary read is. Here the flood fills the read class to
// its cap, every request of it admitted and running at once, where a lane of one place, the width
// this limiter gives every lane, would have run one of them; and the reads of other routes sent
// behind the flood wait for a unit and are admitted within their one wait, once the flood's reads
// give theirs back. A lock on issues, which each references read's first statement (loadIssue)
// waits for, holds the flood in flight until the other reads have queued behind it.
func TestAFloodOfIssueReferencesAtTheReadCapStillAdmitsOtherReads(t *testing.T) {
	const readCap = 4
	limiter := admission.New(admission.Limits{Read: readCap, Write: 4, HeavyReadLane: 1, ReadWait: 5 * time.Second})
	handler, database, _ := newTestServer(t, testServerOptions{admission: limiter})
	createReferenceAPIProject(t, handler, "CORE")
	issue := createReferenceAPIIssue(t, handler, "CORE")
	design := createProjectDocument(t, handler, "CORE", "Design notes", "# Design\n")
	createReferenceAPIComment(t, handler, issue.Key, "dispatch://CORE/artifact/design-notes")

	release := lockTables(t, database, "issues")
	flood := make(chan *httptest.ResponseRecorder, readCap)
	for range readCap {
		go func() { flood <- issueReferencesRequest(t, handler, issue.Key, "") }()
	}
	pollUntil(t, 10*time.Second, "the read class full of references reads", func() bool {
		return limiter.Stats()[admission.Read].InFlight == readCap
	})
	others := []string{"/api/v1/issues/" + issue.Key, "/api/v1/issues/" + issue.Key + "/events"}
	answered := make(chan *httptest.ResponseRecorder, len(others))
	for _, target := range others {
		go func() { answered <- dispatchRequest(t, handler, http.MethodGet, target, nil, "alice") }()
	}
	pollUntil(t, 10*time.Second, "the other reads queued behind the flood", func() bool {
		return limiter.Stats()[admission.Read].Queued == len(others)
	})
	release()

	for range readCap {
		response := awaitFloodResponse(t, flood)
		if response.Code != http.StatusOK {
			t.Fatalf("a references read of the flood: status=%d body=%s, want 200", response.Code, response.Body.String())
		}
		if answer := decodeBody[model.IssueReferences](t, response); len(answer.Members) != 1 || answer.Members[0].Artifact.ID != design.ID {
			t.Fatalf("a references read of the flood answered %#v, want Design notes alone", answer)
		}
	}
	for range others {
		if response := awaitFloodResponse(t, answered); response.Code != http.StatusOK {
			t.Fatalf("a read behind the flood: status=%d body=%s, want 200", response.Code, response.Body.String())
		}
	}
	if stats := limiter.Stats()[admission.Read]; stats.QueueFull != 0 || stats.WaitExceeded != 0 {
		t.Fatalf("the read class refused reads during the flood: %+v, want none", stats)
	}
	for _, lane := range limiter.LaneStats() {
		if lane.QueueFull != 0 || lane.WaitExceeded != 0 {
			t.Fatalf("the %s lane refused requests during a flood of references reads: %+v", lane.Lane, lane)
		}
	}
}

// awaitFloodResponse is awaitResponse with room for a loaded box: the flood's reads and the reads
// behind them all answer once the lock goes, one read class's worth at a time.
func awaitFloodResponse(t *testing.T, responses <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case response := <-responses:
		return response
	case <-time.After(10 * time.Second):
		t.Fatal("a read did not answer within 10 s of the lock's release")
		return nil
	}
}
