package ghbranch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/oauth2call/oauth2calltest"
	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// notABranch is githubrest.NotABranch, written out so the row pins the text.
const notABranch = "(a branch name the daemon does not print)"

// TestABaseThatIsNoBranchNameIsNamedByAFixedPhrase: the daemon records a repository's default
// branch as GitHub's answer named it (Read), and something in GitHub's place echoed the read's
// installation token there. Create is then asked to start an issue's branch at that base, and the
// read of the base is answered 404 (refused), answered with no commit (no-commit), or gets no
// answer at all (unanswered). Each refusal names the base by its fixed phrase (notABranch), in its
// words and in the read's own name, and holds no run of the token. The control's base, a branch's
// name, prints as it came. Captures ghbranch.answer-words/base-<case>.
func TestABaseThatIsNoBranchNameIsNamedByAFixedPhrase(t *testing.T) {
	refused := func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found","status":"404"}`)
	}
	for _, tc := range []struct {
		name, base, named string
		answer            http.HandlerFunc
	}{
		{"refused", installationToken, notABranch, refused},
		{"no-commit", installationToken, notABranch, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"ref":"refs/heads/main","object":{"type":"commit"}}`)
		}},
		{"unanswered", installationToken, notABranch, func(w http.ResponseWriter, _ *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack the base's read: %v", err)
				return
			}
			_ = conn.Close()
		}},
		{"control", "release/v7.1", "release/v7.1", refused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(tc.answer)
			t.Cleanup(server.Close)
			err := Create(context.Background(), widgets(server.URL), ghrepo.MustParse("acme/widgets"), "legion/LEGION-12", tc.base)
			if err == nil {
				t.Fatal("Create answered no error, though the base's read failed")
			}
			oauth2calltest.RequireNoPiece(t, "ghbranch.answer-words/base-"+tc.name, err.Error(), installationToken)
			if want := "read refs/heads/" + tc.named + " of acme/widgets"; !strings.Contains(err.Error(), want) {
				t.Errorf("Create error = %q, want it to say %q", err, want)
			}
		})
	}
}
