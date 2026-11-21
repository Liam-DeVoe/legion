package githubrest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GraphQL posts its query and variables as one JSON body to GitHub's GraphQL endpoint under the
// API root, with the installation token, and reads the answer's data. An answer carrying errors is
// an error naming them, never data a caller would read as an empty list; an answer other than 2xx
// is an *Answer naming POST /graphql and the status, so a caller reads GitHub's rate limit from it
// as from any REST answer (RetryAfter).
func TestGraphQLPostsTheQueryAndReadsItsData(t *testing.T) {
	const token = "graphql-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sent struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if r.Method != http.MethodPost || r.URL.Path != "/graphql" || r.Header.Get("Authorization") != "Bearer "+token ||
			r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&sent) != nil {
			http.Error(w, `{"message":"bad request"}`, http.StatusBadRequest)
			return
		}
		switch sent.Variables["name"] {
		case "widgets":
			w.Write([]byte(`{"data":{"repository":{"nameWithOwner":"` + sent.Variables["owner"].(string) + `/widgets","query":` + jsonString(sent.Query) + `}}}`))
		case "gone":
			w.Write([]byte(`{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a Repository with the name 'acme/gone'."}]}`))
		default:
			w.Header().Set("Retry-After", "17")
			http.Error(w, `{"message":"You have exceeded a secondary rate limit."}`, http.StatusForbidden)
		}
	}))
	defer server.Close()
	const query = "query($owner:String!,$name:String!){repository(owner:$owner,name:$name){nameWithOwner}}"
	var read struct {
		Repository *struct {
			NameWithOwner string `json:"nameWithOwner"`
			Query         string `json:"query"`
		} `json:"repository"`
	}

	if err := GraphQL(context.Background(), server.URL, token, query, map[string]any{"owner": "acme", "name": "widgets"}, &read); err != nil ||
		read.Repository == nil || read.Repository.NameWithOwner != "acme/widgets" || read.Repository.Query != query {
		t.Fatalf("GraphQL = %+v, %v; want the repository read with the query and variables sent", read.Repository, err)
	}
	err := GraphQL(context.Background(), server.URL, token, query, map[string]any{"owner": "acme", "name": "gone"}, &read)
	if err == nil || !strings.Contains(err.Error(), "NOT_FOUND") || !strings.Contains(err.Error(), "Could not resolve to a Repository") {
		t.Fatalf("GraphQL of an answer with errors = %v, want an error naming GitHub's error", err)
	}
	err = GraphQL(context.Background(), server.URL, token, query, map[string]any{"owner": "acme", "name": "limited"}, &read)
	var answer *Answer
	if !errors.As(err, &answer) || answer.Method != http.MethodPost || answer.Path != "/graphql" || answer.Status != http.StatusForbidden || !answer.RateLimited {
		t.Fatalf("GraphQL of a 403 = %v, want an *Answer for POST /graphql with 403, read as a rate limit", err)
	}
	if got := GraphQLAPI(""); got != "https://api.github.com/graphql" {
		t.Fatalf("GraphQLAPI with no root = %q, want api.github.com's", got)
	}
}

// jsonString is s as a JSON string.
func jsonString(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}
