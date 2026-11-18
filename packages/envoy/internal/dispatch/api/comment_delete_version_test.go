// comment_delete_version_test.go holds R3 of dispatch://INFRA-1523 on a document's versions: an
// accept names the version it makes after the suggestion's body (acceptedVersionName), and the
// delete of that comment renames the version (renameAcceptedVersion, comment_delete.go).
package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

// documentVersionNames reads the document's versions as GET /api/v1/artifacts/{id} serves them,
// each version's name by its number.
func documentVersionNames(t *testing.T, handler http.Handler, artifactID string) map[int]string {
	t.Helper()
	response := dispatchRequest(t, handler, http.MethodGet, "/api/v1/artifacts/"+artifactID, nil, "alice")
	if response.Code != http.StatusOK {
		t.Fatalf("read the document: status=%d body=%s", response.Code, response.Body.String())
	}
	read := decodeBody[struct {
		Versions []model.Version `json:"versions"`
	}](t, response)
	names := make(map[int]string, len(read.Versions))
	for _, version := range read.Versions {
		if version.Summary != nil {
			names[version.Number] = *version.Summary
		}
	}
	return names
}

// versionNamed is the number of the one version whose name holds text, failing t unless exactly
// one does.
func versionNamed(t *testing.T, names map[int]string, text string) int {
	t.Helper()
	found := 0
	for number, name := range names {
		if strings.Contains(name, text) {
			if found != 0 {
				t.Fatalf("versions %d and %d are both named with %q: %v", found, number, text, names)
			}
			found = number
		}
	}
	if found == 0 {
		t.Fatalf("no version is named with %q: %v", text, names)
	}
	return found
}

// acceptSuggestionNamed opens a suggestion on quote whose body holds phrase, accepts it, and
// returns the comment.
func acceptSuggestionNamed(t *testing.T, handler http.Handler, issueKey, quote, phrase string) model.Comment {
	t.Helper()
	comment := createThreadComment(t, handler, issueKey, map[string]any{
		"body": "Say this instead, " + phrase + ".", "anchor": map[string]any{"artifact": "spec", "quote": quote},
		"suggestion": map[string]string{"replace_with": "slow"},
	}, "alice")
	if accepted := dispatchRequest(t, handler, http.MethodPost, "/api/v1/comments/"+comment.ID+"/accept", map[string]any{}, "alice"); accepted.Code != http.StatusOK {
		t.Fatalf("accept: status=%d body=%s", accepted.Code, accepted.Body.String())
	}
	return comment
}

// The delete renames the version the accept made to the name an accept gives a suggestion with no
// body, and touches no other version; the artifact.version event keeps the name it carried, as
// every event keeps its payload.
func TestDeletingAnAcceptedSuggestionRenamesTheVersionItMade(t *testing.T) {
	_, handler, database := browserDocumentService(t)
	issue := createInteractionIssue(t, handler, "TEST", "Rename the accepted version", "The quick brown fox")
	const phrase = "zarquonrenamedversion"
	comment := acceptSuggestionNamed(t, handler, issue.Key, "quick", phrase)
	before := documentVersionNames(t, handler, issue.PrimaryArtifactID)
	accepted := versionNamed(t, before, phrase)

	deleteThreadComment(t, handler, comment.ID, "alice")

	after := documentVersionNames(t, handler, issue.PrimaryArtifactID)
	if want := `Accepted suggestion: "quick" → "slow"`; after[accepted] != want {
		t.Fatalf("version %d after the delete is named %q, want %q", accepted, after[accepted], want)
	}
	for number, name := range before {
		if number != accepted && after[number] != name {
			t.Fatalf("version %d was renamed from %q to %q; the delete renames the accepted version alone", number, name, after[number])
		}
	}
	var kept int
	if err := database.Pool.QueryRow(context.Background(), `
		select count(*) from events
		where issue_key = $1 and type = 'artifact.version' and strpos(payload::text, $2) > 0
	`, issue.Key, phrase).Scan(&kept); err != nil {
		t.Fatalf("read the artifact.version events: %v", err)
	}
	if kept != 1 {
		t.Fatalf("%d artifact.version events hold the name the accept gave, want the accept's own one, kept as written", kept)
	}
}

// The version is found by the time its accept wrote it, so another named version of the document
// carrying that same time makes the lookup ambiguous: the delete renames neither, rather than one
// it cannot tell is the accept's, and stands.
func TestADeleteRenamesNoVersionWhenTwoCarryTheAcceptsTime(t *testing.T) {
	_, handler, database := browserDocumentService(t)
	issue := createInteractionIssue(t, handler, "TEST", "Two versions at one time", "The quick brown fox")
	const byHand = "A version named by hand"
	if named := dispatchRequest(t, handler, http.MethodPost, "/api/v1/artifacts/"+issue.PrimaryArtifactID+"/versions",
		map[string]any{"summary": byHand}, "alice"); named.Code != http.StatusCreated {
		t.Fatalf("name a version: status=%d body=%s", named.Code, named.Body.String())
	}
	const phrase = "zarquonambiguousversion"
	comment := acceptSuggestionNamed(t, handler, issue.Key, "quick", phrase)
	before := documentVersionNames(t, handler, issue.PrimaryArtifactID)
	accepted, other := versionNamed(t, before, phrase), versionNamed(t, before, byHand)
	if _, err := database.Pool.Exec(context.Background(), `
		update artifact_versions set created_at = (
			select created_at from artifact_versions where artifact_id = $1 and number = $2
		) where artifact_id = $1 and number = $3
	`, issue.PrimaryArtifactID, accepted, other); err != nil {
		t.Fatalf("give version %d the accepted version's time: %v", other, err)
	}

	deleteThreadComment(t, handler, comment.ID, "alice")

	after := documentVersionNames(t, handler, issue.PrimaryArtifactID)
	if after[accepted] != before[accepted] || after[other] != byHand {
		t.Fatalf("after the delete the versions are named %v, want %v: an ambiguous lookup renames none", after, before)
	}
}
