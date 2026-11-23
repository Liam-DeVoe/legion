package githubtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/appauth"
)

// checksAuthRepo and checksAuthHead are the repository and commit the credential tests read.
const (
	checksAuthRepo = "acme/widgets"
	checksAuthHead = "0123456789abcdef0123456789abcdef01234567"
)

// checksAuthClientID is the client id the scratch Dispatch's App names as its JWT's issuer.
const checksAuthClientID = "Iv1.scratchdispatch"

// A read of the repository or of a commit's check runs is GitHub's to answer only for a token that
// GitHub minted: one with no token is answered 401 Requires authentication, and one with a token
// the fake did not mint 401 Bad credentials, as GitHub answers each.
func TestTheChecksFakeRefusesAReadWithoutATokenItMinted(t *testing.T) {
	t.Parallel()
	c := ServeChecks(t)
	c.SetCheckRun(checksAuthRepo, checksAuthHead, CheckRun{ID: 1, Name: "unit", Status: "completed", Conclusion: "failure"})
	for _, read := range []struct{ name, path string }{
		{"the check runs", "/repos/" + checksAuthRepo + "/commits/" + checksAuthHead + "/check-runs"},
		{"the repository", "/repos/" + checksAuthRepo},
	} {
		for _, credential := range []struct{ name, authorization, message string }{
			{"no token", "", "Requires authentication"},
			{"a token it did not mint", "Bearer never-minted", "Bad credentials"},
		} {
			t.Run(read.name+" with "+credential.name, func(t *testing.T) {
				t.Parallel()
				status, message := checksRequest(t, http.MethodGet, c.URL+read.path, credential.authorization)
				if status != http.StatusUnauthorized || message != credential.message {
					t.Errorf("GET %s with %s answered %d %q, want %d %q", read.path, credential.name, status, message,
						http.StatusUnauthorized, credential.message)
				}
			})
		}
	}
}

// The App's installation is looked up and its token minted only with a JWT the App's own key
// signed: a request with no JWT, or with one another key signed for the App's client id, is
// answered 401 A JSON web token could not be decoded, as GitHub answers it.
func TestTheChecksFakeMintsATokenOnlyForTheAppsJWT(t *testing.T) {
	t.Parallel()
	c := ServeChecks(t)
	stranger, err := appauth.GenerateJWT(checksAuthClientID, checksAuthStrangerKey(t), time.Now())
	if err != nil {
		t.Fatalf("sign a JWT with a stranger's key: %v", err)
	}
	for _, route := range []struct{ name, method, path string }{
		{"the installation", http.MethodGet, "/repos/" + checksAuthRepo + "/installation"},
		{"a token", http.MethodPost, "/app/installations/1/access_tokens"},
	} {
		for _, credential := range []struct{ name, authorization string }{
			{"no JWT", ""},
			{"a JWT another key signed", "Bearer " + stranger},
		} {
			t.Run(route.name+" with "+credential.name, func(t *testing.T) {
				t.Parallel()
				status, message := checksRequest(t, route.method, c.URL+route.path, credential.authorization)
				if want := "A JSON web token could not be decoded"; status != http.StatusUnauthorized || message != want {
					t.Errorf("%s %s with %s answered %d %q, want %d %q", route.method, route.path, credential.name, status, message,
						http.StatusUnauthorized, want)
				}
			})
		}
	}
}

// checksAuthStrangerKey is an App private key, a PEM, that no App the fake knows holds.
func checksAuthStrangerKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("make a stranger's App key: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

// checksRequest makes one request of the fake with authorization as its Authorization header,
// none where it is empty, and answers the status and the message GitHub's JSON body carries.
func checksRequest(t *testing.T, method, target, authorization string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatalf("make the request %s %s: %v", method, target, err)
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the answer to %s %s: %v", method, target, err)
	}
	var answer struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &answer)
	return response.StatusCode, answer.Message
}
