package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

// A read of the results whose caller hangs up while GitHub is answering still stores the head.
// The read takes the claim of the commit's read-through (delivery.CommitPullReads) before it asks
// GitHub, so a head write that the caller's end cancelled left the result at no head for the whole
// claim: production lost cybermonklabs/legion#4490's head that way at 2026-11-19T17:09:59Z
// ("store the head of cybermonklabs/legion#4490: context canceled"). Here GitHub holds its answer
// naming #7 at k1's commit until the caller has hung up, and the head must be stored all the same.
func TestAResultsReadWhoseCallerHangsUpStillStoresTheHead(t *testing.T) {
	handler, database, served, fake := readThroughGitHub(t, "acme/widgets")
	issue := issueInRepository(t, handler, "HANG", "acme/widgets", "The caller hangs up")
	uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "verification.md", twoChecks)
	recordResult(t, handler, issue.Key, resultBody("k1", "pass", headCommit, nil))
	if rows := headRowsOf(t, database, 7); rows != 0 {
		t.Fatalf("SETUP: %d rows of #7 before the read, want none: GitHub named nothing at the record", rows)
	}

	asked, hungUp := make(chan struct{}), make(chan struct{})
	var askedOnce sync.Once
	answer := openPullAt(7, issue.Key)
	fake.answers(func(repo, sha string) string {
		if sha != headCommit {
			return `[]`
		}
		askedOnce.Do(func() { close(asked) })
		<-hungUp
		return answer(repo, sha)
	})
	rewindCommitPullReads(t, served)

	ctx, hangUp := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/issues/"+issue.Key+"/results", nil).WithContext(ctx)
	request.Header.Set("X-Dispatch-User", "alice")
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), request)
	}()
	select {
	case <-asked:
	case <-time.After(30 * time.Second):
		t.Fatal("SETUP: the read never asked GitHub for the pull requests of k1's commit")
	}
	hangUp()
	close(hungUp)
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("the read of the results did not return once GitHub had answered")
	}

	if rows := headRowsOf(t, database, 7); rows != 1 {
		t.Fatalf("rows of #7 after a read whose caller hung up while GitHub answered = %d, want 1: the caller's end cut the head write short, and the read's claim then holds the commit off for CommitPullReadStaleAfter with nothing stored", rows)
	}
	if compared := pullRequestsComparedWith(checkOf(t, readResults(t, handler, issue.Key), "k1")); !slices.Equal(compared, []int{7}) {
		t.Fatalf("the next read compares k1 with %v, want [7]", compared)
	}
}

// A read-through whose answer the database could not store gives its claim back, so the very next
// read asks GitHub again and stores the head, rather than waiting out CommitPullReadStaleAfter with
// the result at no head. A trigger refuses every write of pull_request_heads for the first read and
// is dropped before the second. The control is GitHub's own failure: a 500 to the list of the
// commit's pull requests keeps the claim, as delivery.CommitPullReads.Claim says a read that fails
// must, so the second read asks GitHub nothing.
func TestAReadThroughWhoseStoreFailsGivesItsClaimBack(t *testing.T) {
	for _, tc := range []struct {
		name       string
		storeFails bool // else GitHub answers 500 to the list
	}{
		{"the_store_fails", true},
		{"control_github_fails", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, database, served, fake := readThroughGitHub(t, "acme/widgets")
			issue := issueInRepository(t, handler, "STOF", "acme/widgets", "The store fails")
			uploadDocument(t, handler, "/api/v1/issues/"+issue.Key, "verification.md", twoChecks)
			recordResult(t, handler, issue.Key, resultBody("k1", "pass", headCommit, nil))
			fake.answers(func(repo, sha string) string {
				if sha != headCommit {
					return `[]`
				}
				return openPullAt(7, issue.Key)(repo, sha)
			})
			rewindCommitPullReads(t, served)
			ctx := context.Background()
			if tc.storeFails {
				if _, err := database.Pool.Exec(ctx, `
					create function refuse_head_writes() returns trigger language plpgsql as
						$$ begin raise exception 'the test refuses this write'; end $$;
					create trigger refuse_head_writes before insert or update on pull_request_heads
						for each row execute function refuse_head_writes();
				`); err != nil {
					t.Fatalf("SETUP: refuse the writes of pull_request_heads: %v", err)
				}
			} else {
				fake.refusesCommitPulls(headCommit, http.StatusInternalServerError)
			}

			spent := fake.asks("acme/widgets")
			readResults(t, handler, issue.Key)
			first := fake.asks("acme/widgets") - spent
			if rows := headRowsOf(t, database, 7); first < 1 || rows != 0 {
				t.Fatalf("SETUP: the first read asked GitHub %d times and left %d rows of #7, want at least 1 and 0", first, rows)
			}
			if tc.storeFails {
				if _, err := database.Pool.Exec(ctx, `
					drop trigger refuse_head_writes on pull_request_heads;
					drop function refuse_head_writes();
				`); err != nil {
					t.Fatalf("let pull_request_heads be written again: %v", err)
				}
			} else {
				fake.mu.Lock()
				delete(fake.refused, headCommit)
				fake.mu.Unlock()
			}

			spent = fake.asks("acme/widgets")
			k1 := checkOf(t, readResults(t, handler, issue.Key), "k1")
			second, compared := fake.asks("acme/widgets")-spent, pullRequestsComparedWith(k1)
			t.Logf("%s: first read asks=%d; second read asks=%d compared_with=%v", tc.name, first, second, compared)
			if !tc.storeFails {
				if second != 0 {
					t.Fatalf("control: the read after GitHub's own failure asked GitHub %d times, want 0: the claim of a read GitHub failed holds", second)
				}
				return
			}
			if second != 1 || !slices.Equal(compared, []int{7}) {
				t.Fatalf("the read after a read-through whose store failed asked GitHub %d times and compares k1 with %v, want 1 and [7]: the claim of a read that stored nothing was not given back", second, compared)
			}
		})
	}
}
