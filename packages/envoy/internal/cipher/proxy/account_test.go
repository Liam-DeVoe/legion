// packages/envoy/internal/cipher/proxy/account_test.go
//
// The rows about which account's token a GitHub call gets. A github-app name holds one token per
// installation, one slot per account, and a command sends the placeholder of one of them: the
// one its gh configuration names. The proxy swaps in the token of the account the request's path
// names when the grant holds that account, and otherwise the token of the account whose
// placeholder the request carries.
package proxy

import (
	"encoding/base64"
	"net"
	"net/http"
	"net/netip"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/proxyrules"
	"github.com/sjawhar/envoy/internal/cipher/proxyrules/proxyrulestest"
)

// gitHost is the host git's own paths are read on: github.com serves a repository's git
// transport at /{owner}/{repo}, and the API hosts serve none.
const gitHost = "github.com"

// githubRules binds the rig's one name, made a github-app name by useGitHubAppGrant, to the bound
// host and to github.com, with no method or path rule, so every path below is decided alike.
func githubRules() proxyrules.Rules {
	return proxyrules.Rules{Secrets: []proxyrules.SecretRule{{
		Name: workerName, Hosts: []string{boundHost, gitHost}, Headers: []string{"authorization"},
	}}}
}

// serveGitHost puts a stand-in service behind github.com, signed by the authority the proxy
// trusts upstream, and answers it.
func (r *rig) serveGitHost() *proxyrulestest.Service {
	r.t.Helper()
	service, err := proxyrulestest.NewService(proxyrulestest.ServiceOptions{Hosts: []string{gitHost}, Authority: r.service.Authority()})
	if err != nil {
		r.t.Fatalf("the stand-in github.com: %v", err)
	}
	r.t.Cleanup(service.Close)
	r.addresses[gitHost] = []netip.Addr{netip.MustParseAddr("93.184.216.38")}
	r.dialTo[net.JoinHostPort(gitHost, "443")] = service.Addr()
	return service
}

// gitBasic is the Authorization header git sends a token in: a Basic credential whose password
// is the token.
func gitBasic(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
}

// TestTheTokenIsTheAccountsThePathNames: on an API host, /repos/{owner}/..., /orgs/{owner} and
// /users/{owner} name the account, compared without regard to case, as GitHub compares logins.
// A path that names an account the grant holds gets that account's token whichever placeholder
// the request carries; a path that names none, or names one the grant holds no installation on,
// gets the carried placeholder's. git's /{owner}/{repo} is no account on an API host. The line
// names the slot whose token went to the service. The named revert: proxy-account-from-placeholder
// (the token of the carried placeholder's account alone, today's rule before this one).
func TestTheTokenIsTheAccountsThePathNames(t *testing.T) {
	r := newRig(t, Options{Rules: githubRules()})
	r.useGitHubAppGrant()
	for i, row := range []struct {
		path, carried, token, slot string
	}{
		{"/repos/zed/widget", workerPlaceholder, githubZedToken, githubZedSlot},
		{"/repos/zed/widget/pulls/7", workerPlaceholder, githubZedToken, githubZedSlot},
		{"/orgs/zed", workerPlaceholder, githubZedToken, githubZedSlot},
		{"/users/zed/repos", workerPlaceholder, githubZedToken, githubZedSlot},
		{"/repos/ZED/widget", workerPlaceholder, githubZedToken, githubZedSlot},
		{"/repos/acme/widget", githubZedHolder, githubAcmeToken, githubAcmeSlot},
		{"/graphql", workerPlaceholder, githubAcmeToken, githubAcmeSlot},
		{"/graphql", githubZedHolder, githubZedToken, githubZedSlot},
		{"/user", githubZedHolder, githubZedToken, githubZedSlot},
		{"/repos/nobody/widget", githubZedHolder, githubZedToken, githubZedSlot},
		{"/repos", workerPlaceholder, githubAcmeToken, githubAcmeSlot},
		{"/zed/widget.git/info/refs", workerPlaceholder, githubAcmeToken, githubAcmeSlot},
	} {
		answer, body := r.get(r.client(workerPlaceholder), "https://"+boundHost+row.path, "Authorization", "Bearer "+row.carried)
		if answer.StatusCode != http.StatusOK {
			t.Fatalf("%s answered %d %q; want 200", row.path, answer.StatusCode, body)
		}
		seen := r.requests()
		if got, want := seen[i].Header.Get("Authorization"), "Bearer "+row.token; got != want {
			t.Errorf("%s with %s's placeholder: the service saw Authorization %q; want %q", row.path, carriedSlot(row.carried), got, want)
		}
		if slot := r.logs.callsAtLeast(t, i+1)[i].fields["slot"]; slot != row.slot {
			t.Errorf("%s: the call logged the slot %v; want %s", row.path, slot, row.slot)
		}
	}
}

// TestGitsPathNamesTheAccountOnGitHub: on github.com, git's /{owner}/{repo} names the account, and
// git sends its token as the password of a Basic credential, which the swap reaches inside. A path
// of one segment names no repository, so it is no account's.
func TestGitsPathNamesTheAccountOnGitHub(t *testing.T) {
	r := newRig(t, Options{Rules: githubRules()})
	r.useGitHubAppGrant()
	git := r.serveGitHost()
	for i, row := range []struct {
		path, carried, token string
	}{
		{"/zed/widget.git/info/refs", workerPlaceholder, githubZedToken},
		{"/zed/widget/git-upload-pack", workerPlaceholder, githubZedToken},
		{"/Zed/widget.git/info/refs", workerPlaceholder, githubZedToken},
		{"/acme/widget.git/info/refs", githubZedHolder, githubAcmeToken},
		{"/zed", workerPlaceholder, githubAcmeToken},
		{"/nobody/widget.git/info/refs", githubZedHolder, githubZedToken},
	} {
		answer, body := r.get(r.client(workerPlaceholder), "https://"+gitHost+row.path, "Authorization", gitBasic(row.carried))
		if answer.StatusCode != http.StatusOK {
			t.Fatalf("%s answered %d %q; want 200", row.path, answer.StatusCode, body)
		}
		seen := git.Requests()
		if got, want := seen[i].Header.Get("Authorization"), gitBasic(row.token); got != want {
			t.Errorf("%s with %s's placeholder: github.com saw Authorization %q; want %q", row.path, carriedSlot(row.carried), got, want)
		}
	}
}

// carriedSlot names the slot a placeholder of useGitHubAppGrant stands for, for a row's words.
func carriedSlot(placeholder string) string {
	if placeholder == githubZedHolder {
		return githubZedSlot
	}
	return githubAcmeSlot
}
