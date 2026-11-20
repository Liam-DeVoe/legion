// packages/envoy/cmd/dev-airlock-stands/github_test.go
//
// The GitHub stand as this command serves it, beyond the four routes of the stand-in package's
// own: what each installation of the App holds, which --installation-permissions sets, and the
// three routes that answer by what a token holds, reached at the github-tls stand over TLS as a
// command behind the proxy reaches them. Each row runs the real compiled binary, as the local
// stack's check scripts do.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestTheInstallationPermissionsFlagSetsWhatAnInstallationHolds: --installation-permissions
// gives one owner's installation the permissions it names, which the installations listing
// names and the token route holds a request to, and an owner it names nothing for holds nothing;
// an owner it does not name keeps the stand's own map.
func TestTheInstallationPermissionsFlagSetsWhatAnInstallationHolds(t *testing.T) {
	out := startStands(t, "--dir", t.TempDir(),
		"--github-owner", "acme-org=7", "--github-owner", "ada=8", "--github-owner", "Ada=9",
		"--installation-permissions", "acme-org=contents:read,administration:read",
		"--installation-permissions", "ada=")
	github := out.Stands[standGitHub].URL

	held := installationsHeld(t, github)
	if want := map[string]string{"contents": "read", "administration": "read"}; !maps.Equal(held["acme-org"], want) {
		t.Errorf("acme-org's installation holds %v; want %v, what the flag named", held["acme-org"], want)
	}
	if len(held["ada"]) != 0 {
		t.Errorf("ada's installation holds %v; want nothing, since the flag named nothing for it", held["ada"])
	}
	if held["Ada"]["contents"] != "write" {
		t.Errorf("Ada's installation holds %v; want the stand's own map, contents at write among it, since the flag names nothing of Ada's", held["Ada"])
	}

	for _, c := range []struct {
		name         string
		installation int
		asked        string
		want         int
	}{
		{"a permission the installation holds", 7, `{"permissions":{"contents":"read"}}`, http.StatusCreated},
		{"a level past the one it holds", 7, `{"permissions":{"contents":"write"}}`, http.StatusUnprocessableEntity},
		{"a permission it does not hold", 7, `{"permissions":{"issues":"read"}}`, http.StatusUnprocessableEntity},
		{"anything of an installation that holds nothing", 8, `{"permissions":{"metadata":"read"}}`, http.StatusUnprocessableEntity},
		{"the stand's own map, where the flag names nothing", 9, `{"permissions":{"contents":"write"}}`, http.StatusCreated},
	} {
		if status, body := mintAt(t, github, c.installation, c.asked); status != c.want {
			t.Errorf("%s: the token request answered %d %s; want %d", c.name, status, body, c.want)
		}
	}
}

// TestTheInstallationPermissionsFlagRefusesWhatItCannotRead: each malformed value, an owner
// named twice, and an owner no --github-owner installs the App on are usage errors, exit 2, each
// naming what it refuses, before any stand is served.
func TestTheInstallationPermissionsFlagRefusesWhatItCannotRead(t *testing.T) {
	binary := buildStands(t)
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"no owner", []string{"--installation-permissions", "=contents:read"}, "names no owner"},
		{"no level", []string{"--installation-permissions", "acme-org=contents"}, `"contents" names no level`},
		{"a level GitHub gives no permission", []string{"--installation-permissions", "acme-org=contents:owner"}, `the level "owner" is none of read, write and admin`},
		{"a permission no App permission is named", []string{"--installation-permissions", "acme-org=Contents:read"}, `"Contents" is no permission's name`},
		{"a permission named twice", []string{"--installation-permissions", "acme-org=contents:read,contents:write"}, "names contents twice"},
		{"an owner named twice", []string{"--installation-permissions", "acme-org=contents:read", "--installation-permissions", "acme-org=issues:read"}, "names acme-org twice"},
		{"an owner the App is not installed on", []string{"--installation-permissions", "other-org=contents:read"}, "other-org, on which no --github-owner installs the App"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, append([]string{"--dir", t.TempDir()}, c.args...)...)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != exitUsageError {
				t.Fatalf("the command exited %v; want exit %d\n%s", err, exitUsageError, output)
			}
			if !strings.Contains(string(output), c.want) {
				t.Errorf("the command said %q; want it to name %q", output, c.want)
			}
		})
	}
}

// TestTheGitHubStandsRoutesAnswerByWhatATokenHolds: the three routes a GitHub profile's reach is
// read at answer by the permissions the token holds, as GitHub does: a deploy-key list needs
// administration, an organization's installations need organization_administration, and a
// commit status needs statuses at write, with 422 for a commit the repository does not hold. A
// token of another account, or of other repositories, finds nothing there; a credential the
// stand never minted, and one the minter gave back, are refused as GitHub refuses bad
// credentials. The routes are read at the github-tls stand, over TLS under the stands' own
// authority, which is where a command behind the proxy reaches them, and the tokens are minted
// at the plain github stand, where the broker's minter mints them: one stand-in serves both.
func TestTheGitHubStandsRoutesAnswerByWhatATokenHolds(t *testing.T) {
	out := startStands(t, "--dir", t.TempDir(), "--github-owner", "acme-org=7", "--github-owner", "ada=8",
		"--installation-permissions", "acme-org=metadata:read,administration:read,organization_administration:read,statuses:write")
	plain := out.Stands[standGitHub].URL
	if len(out.GitHubCommitSHA) != 40 {
		t.Fatalf("the report names the commit %q; want the 40 hex digits of the one commit the stand holds", out.GitHubCommitSHA)
	}
	mint := func(asked string) string {
		t.Helper()
		status, body := mintAt(t, plain, 7, asked)
		if status != http.StatusCreated {
			t.Fatalf("the token request %s answered %d %s; want 201", asked, status, body)
		}
		var answer struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(body), &answer); err != nil || answer.Token == "" {
			t.Fatalf("the token request answered no token: %v %s", err, body)
		}
		return answer.Token
	}
	administrator := mint(`{"repositories":["proofs"],"permissions":{"administration":"read"}}`)
	reader := mint(`{"repositories":["proofs"],"permissions":{"metadata":"read"}}`)
	organization := mint(`{"permissions":{"organization_administration":"read"}}`)
	statusWriter := mint(`{"repositories":["proofs"],"permissions":{"statuses":"write"}}`)
	statusReader := mint(`{"repositories":["proofs"],"permissions":{"statuses":"read"}}`)
	givenBack := mint(`{"repositories":["proofs"],"permissions":{"administration":"read"}}`)
	if status, body := callGitHub(t, http.DefaultClient, plain, http.MethodDelete, "/installation/token", givenBack); status != http.StatusNoContent {
		t.Fatalf("the revoke answered %d %s; want 204", status, body)
	}

	tlsStand := out.Stands[standGitHubTLS]
	if !strings.HasPrefix(tlsStand.URL, "https://") || tlsStand.CertFile == "" {
		t.Fatalf("the github-tls stand is %+v; want an https stand with a certificate of its own", tlsStand)
	}
	client := trustingTheStands(t, out.CACertFile)
	commit := "/repos/acme-org/proofs/statuses/" + out.GitHubCommitSHA
	for _, c := range []struct {
		name, token, method, path string
		want                      int
	}{
		{"a deploy-key list, to a token holding administration", administrator, http.MethodGet, "/repos/acme-org/proofs/keys", http.StatusOK},
		{"a deploy-key list, to a token without it", reader, http.MethodGet, "/repos/acme-org/proofs/keys", http.StatusForbidden},
		{"a deploy-key list of a repository outside the token", administrator, http.MethodGet, "/repos/acme-org/widgets/keys", http.StatusNotFound},
		{"a deploy-key list of another account's repository", administrator, http.MethodGet, "/repos/ada/proofs/keys", http.StatusNotFound},
		{"an organization's installations, to a token holding organization_administration", organization, http.MethodGet, "/orgs/acme-org/installations", http.StatusOK},
		{"an organization's installations, to a token without it", administrator, http.MethodGet, "/orgs/acme-org/installations", http.StatusForbidden},
		{"another organization's installations", organization, http.MethodGet, "/orgs/ada/installations", http.StatusNotFound},
		{"a commit status, to a token holding statuses at write", statusWriter, http.MethodPost, commit, http.StatusCreated},
		{"a commit status for a commit the repository does not hold", statusWriter, http.MethodPost, "/repos/acme-org/proofs/statuses/" + strings.Repeat("0", 40), http.StatusUnprocessableEntity},
		{"a commit status, to a token holding statuses at read", statusReader, http.MethodPost, commit, http.StatusForbidden},
		{"a credential the stand never minted", "ghs_minted_by_nobody", http.MethodGet, "/repos/acme-org/proofs/keys", http.StatusUnauthorized},
		{"a token the minter gave back", givenBack, http.MethodGet, "/repos/acme-org/proofs/keys", http.StatusUnauthorized},
	} {
		if status, body := callGitHub(t, client, tlsStand.URL, c.method, c.path, c.token); status != c.want {
			t.Errorf("%s: %s %s answered %d %s; want %d", c.name, c.method, c.path, status, body, c.want)
		}
	}
	// The repository read of the stand-in package's own routes answers at the github-tls stand
	// as well, so one host serves every call a command behind the proxy makes.
	if status, body := callGitHub(t, client, tlsStand.URL, http.MethodGet, "/repos/acme-org/proofs", reader); status != http.StatusOK {
		t.Errorf("GET /repos/acme-org/proofs at the github-tls stand answered %d %s; want 200", status, body)
	}
	if records := recordsOf(t, tlsStand.Records); len(records) != 13 {
		t.Errorf("the github-tls stand recorded %d requests; want the 13 this row made there", len(records))
	}
}

// installationsHeld reads the GitHub stand's installations listing as what each owner's
// installation holds, by owner.
func installationsHeld(t *testing.T, url string) map[string]map[string]string {
	t.Helper()
	status, body := callGitHub(t, http.DefaultClient, url, http.MethodGet, "/app/installations", "")
	if status != http.StatusOK {
		t.Fatalf("GET /app/installations answered %d %s; want 200", status, body)
	}
	var listing []struct {
		Account struct {
			Login string `json:"login"`
		} `json:"account"`
		Permissions map[string]string `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(body), &listing); err != nil {
		t.Fatalf("GET /app/installations answered no listing: %v %s", err, body)
	}
	held := map[string]map[string]string{}
	for _, installation := range listing {
		held[installation.Account.Login] = installation.Permissions
	}
	return held
}

// mintAt makes one access_tokens request of one installation, the call the broker's minter
// makes, and answers its status and body.
func mintAt(t *testing.T, url string, installation int, asked string) (int, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		url+"/app/installations/"+strconv.Itoa(installation)+"/access_tokens", strings.NewReader(asked))
	if err != nil {
		t.Fatal(err)
	}
	return send(t, http.DefaultClient, request)
}

// callGitHub makes one call of the GitHub stand with token as its bearer, none for "", and
// answers its status and body.
func callGitHub(t *testing.T, client *http.Client, url, method, path, token string) (int, string) {
	t.Helper()
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(`{"state":"success","context":"stand-in"}`)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, url+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	return send(t, client, request)
}

// send makes one request and answers its status and its body, trimmed.
func send(t *testing.T, client *http.Client, request *http.Request) (int, string) {
	t.Helper()
	bounded := *client
	bounded.Timeout = 10 * time.Second
	answer, err := bounded.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", request.Method, request.URL, err)
	}
	defer answer.Body.Close()
	read, err := io.ReadAll(answer.Body)
	if err != nil {
		t.Fatalf("read the answer to %s %s: %v", request.Method, request.URL, err)
	}
	return answer.StatusCode, strings.TrimSpace(string(read))
}

// trustingTheStands is a client that trusts the stands' authority alone, as a command behind the
// proxy trusts the certificate the proxy presents for a bound host.
func trustingTheStands(t *testing.T, caFile string) *http.Client {
	t.Helper()
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatalf("%s holds no PEM certificate", caFile)
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
}
