package dispatch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// A client made with NewReading asks its bearer for the token at every request, so a token file
// rewritten between two calls (a projected service-account token the kubelet renewed) reaches
// Dispatch as it is then: the first call carries the first token, the second the second. A bearer
// that fails sends nothing and returns its error. Control: a client made with New over the token
// read before presents the first token twice, so this test sees a token kept from an earlier read.
func TestAReadingClientPresentsTheBearerItReadsAtEachRequest(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Header.Get("Authorization"))
		mu.Unlock()
		writeJSON(t, w, []map[string]any{})
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "token")
	write := func(token string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func() (string, error) {
		contents, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		token := strings.TrimSpace(string(contents))
		if token == "" {
			return "", errors.New("the token file is empty")
		}
		return token, nil
	}
	taken := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := slices.Clone(asked)
		asked = nil
		return out
	}

	write("first-projected-not-a-secret")
	first, err := read()
	if err != nil {
		t.Fatal(err)
	}
	client := NewReading(server.URL, read)
	if _, err := client.ListIssues(context.Background(), "LEGION", nil); err != nil {
		t.Fatalf("list issues: %v", err)
	}
	write("second-projected-not-a-secret")
	if _, err := client.ListIssues(context.Background(), "LEGION", nil); err != nil {
		t.Fatalf("list issues after the file changed: %v", err)
	}
	if got, want := taken(), []string{"Bearer first-projected-not-a-secret", "Bearer second-projected-not-a-secret"}; !slices.Equal(got, want) {
		t.Fatalf("the reading client presented %q, want %q", got, want)
	}

	write("  ")
	if _, err := client.ListIssues(context.Background(), "LEGION", nil); err == nil || !strings.Contains(err.Error(), "the token file is empty") {
		t.Fatalf("list issues with a blank token file = %v, want the bearer's error", err)
	}
	if got := taken(); len(got) != 0 {
		t.Fatalf("a request whose bearer failed was sent with %q, want none sent", got)
	}

	write("second-projected-not-a-secret")
	cached := New(server.URL, first)
	for range 2 {
		if _, err := cached.ListIssues(context.Background(), "LEGION", nil); err != nil {
			t.Fatalf("list issues: %v", err)
		}
	}
	if got, want := taken(), []string{"Bearer first-projected-not-a-secret", "Bearer first-projected-not-a-secret"}; !slices.Equal(got, want) {
		t.Fatalf("control: the client over the token read before presented %q, want %q", got, want)
	}
}
