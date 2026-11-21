package daemon

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/githubrest"
	"github.com/sjawhar/legion/daemon/internal/testreason"
)

// TestARateLimitLineNamesGitHubsHeadersAsNumbersOrAsTheRuleReadsThem: GitHub, or whatever answers
// in its place, refuses a read with its rate limit, and two of the rate-limit headers it sends
// quote a credential's URL rather than name a number or a budget. The line the daemon logs of the
// limit (limitAttributes) and the summary a boot probe reports (limitSummary) name each header
// that is a number as that number and each other as the rule reads it (githubrest's limitHeaders,
// oauth2call.Header), and none of what the stand-in wrote there. Captures
// daemon.answer-words/rate-limit.
func TestARateLimitLineNamesGitHubsHeadersAsNumbersOrAsTheRuleReadsThem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", testreason.Reason)
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.Header().Set("X-RateLimit-Resource", testreason.Reason)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	t.Cleanup(server.Close)
	response, err := http.Get(server.URL + "/repos/acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	answer := githubrest.AnswerOf(http.MethodGet, "/repos/acme/widgets", response, []byte(`{"message":"API rate limit exceeded"}`))
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Warn("limited", limitAttributes(limitedRead{answer: answer, err: errors.New("refused"), wait: time.Minute})...)
	said := logged.String() + " | " + limitSummary(answer)
	testreason.RequireNone(t, "daemon.answer-words/rate-limit", said)
	for _, want := range []string{"rateLimit=5000", "rateLimitRemaining=0", "rateLimitReset=1700000000", "issuer.example/help", `X-RateLimit-Remaining "0"`} {
		if !strings.Contains(said, want) {
			t.Errorf("the limit is named %q, want %q in it", said, want)
		}
	}
}
