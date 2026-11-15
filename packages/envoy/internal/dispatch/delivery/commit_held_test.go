package delivery

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// PullRequestOpenOrMerged answers yes for a pull request that is open or merged, whichever of
// `merged` and `merged_at` carries the merge, and no for one that closed without a merge. A pull
// request GitHub answers 404 for is the error, never a no: Dispatch cannot tell it from a pull
// request the App cannot reach.
func TestPullRequestOpenOrMergedReadsTheStateGitHubAnswers(t *testing.T) {
	fake := newFakeGitHub(t)
	answers := map[string]string{
		"1": `{"number":1,"state":"open","merged":false,"merged_at":null}`,
		"2": `{"number":2,"state":"closed","merged":true,"merged_at":"2026-11-14T00:00:00Z"}`,
		"3": `{"number":3,"state":"closed","merged_at":"2026-11-14T00:00:00Z"}`,
		"4": `{"number":4,"state":"closed","merged":false,"merged_at":null}`,
	}
	fake.handle("GET /repos/acme/widgets/pulls/{number}", func(w http.ResponseWriter, r *http.Request) {
		answer, found := answers[r.PathValue("number")]
		if !found {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		fmt.Fprint(w, answer)
	})
	client := fake.newTestClient()

	for _, row := range []struct {
		name   string
		number int
		want   bool
	}{
		{"an open pull request", 1, true},
		{"a pull request merged, as the single read says it", 2, true},
		{"a pull request merged, as only merged_at says it", 3, true},
		{"a pull request closed without a merge", 4, false},
	} {
		got, err := PullRequestOpenOrMerged(t.Context(), client, "acme/widgets", row.number)
		if err != nil || got != row.want {
			t.Errorf("%s: PullRequestOpenOrMerged = %v, %v, want %v and no error", row.name, got, err, row.want)
		}
	}
	if got, err := PullRequestOpenOrMerged(t.Context(), client, "acme/widgets", 5); !errors.Is(err, ErrPullRequestNotFound) {
		t.Errorf("a pull request GitHub answers 404 for: PullRequestOpenOrMerged = %v, %v, want ErrPullRequestNotFound", got, err)
	}
}
