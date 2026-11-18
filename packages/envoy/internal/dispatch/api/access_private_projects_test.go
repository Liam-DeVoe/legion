package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/dispatch/model"
)

const privateProjectsPath = "/api/v1/access/private-projects"

// TestTheBrokerListsThePrivateProjectsChannels (c-a-slack-channels): the secrets broker's own
// service-account token reads, for every private project and sorted by key, its key, its Slack
// channels ([] for none) and when that list last changed (null until its first write), and
// nothing of a public project, whatever channels that one names. A dsp_ token and a person's
// browser are 403 ACCESS_ACCOUNT_REQUIRED. Control: admit any token, and the dsp_ token's 200
// fails the test naming it.
func TestTheBrokerListsThePrivateProjectsChannels(t *testing.T) {
	issuer := newServiceTokenIssuer(t)
	handler, database, _ := newTestServer(t, testServerOptions{oidc: issuer.verifier(t), accessAccounts: []string{listedServiceAccount}})
	for _, key := range []string{"PUB", "PRIV", "ALPHA"} {
		createAccessProject(t, handler, key, "alice")
	}
	patch := func(key string, channels ...string) {
		t.Helper()
		if response := dispatchRequest(t, handler, http.MethodPatch, "/api/v1/projects/"+key+"/access",
			map[string]any{"slack_channels": channels}, "alice"); response.Code != http.StatusOK {
			t.Fatalf("alice names %s's channels: %d %s", key, response.Code, response.Body.String())
		}
	}
	// PUB names a channel while public; PRIV names two once private; ALPHA is private and names none.
	patch("PUB", "C0PUBLIC001")
	makePrivate(t, database, "PRIV")
	makePrivate(t, database, "ALPHA")
	patch("PRIV", "GPRIV000002", "C0PRIV00001")
	var changedAt time.Time
	if err := database.Pool.QueryRow(context.Background(), `select slack_channels_changed_at from projects where key = 'PRIV'`).Scan(&changedAt); err != nil {
		t.Fatalf("read PRIV's channels time: %v", err)
	}
	stamp, err := json.Marshal(changedAt)
	if err != nil {
		t.Fatalf("encode PRIV's channels time: %v", err)
	}

	bearer := issuer.Mint(t, issuer.signing, issuer.Claims(listedServiceAccount, serviceTokenAudience))
	response := agentRequest(t, handler, http.MethodGet, privateProjectsPath, nil, bearer)
	want := `{"projects":[` +
		`{"project":"ALPHA","slack_channels":[],"slack_channels_changed_at":null},` +
		`{"project":"PRIV","slack_channels":["C0PRIV00001","GPRIV000002"],"slack_channels_changed_at":` + string(stamp) + `}` +
		"]}\n"
	if response.Code != http.StatusOK || response.Body.String() != want {
		t.Fatalf("the broker's GET %s = %d %s, want 200 %s", privateProjectsPath, response.Code, response.Body.String(), want)
	}

	dsp := personalTokenOwnedBy(t, database, "alice", model.ScopePerson)
	wantRefusal(t, agentRequest(t, handler, http.MethodGet, privateProjectsPath, nil, dsp),
		http.StatusForbidden, "ACCESS_ACCOUNT_REQUIRED", "alice's dsp_ token", "GET "+privateProjectsPath)
	wantRefusal(t, dispatchRequest(t, handler, http.MethodGet, privateProjectsPath, nil, "alice"),
		http.StatusForbidden, "ACCESS_ACCOUNT_REQUIRED", "alice at her browser", "GET "+privateProjectsPath)
}

// TestThePrivateProjectsListIsAnEmptyArrayWhenNoneIsPrivate (c-a-slack-channels): with no private
// project, a public one naming a channel included, the broker's token reads 200 and the body
// {"projects":[]} byte for byte, then the newline every JSON answer ends with. projects is never
// null, missing or another type, because plan E's t-e-51 reads any of those as the membership
// answer being down. Control: start the list from a nil slice, and the body reads
// {"projects":null}, and the test fails naming projects.
func TestThePrivateProjectsListIsAnEmptyArrayWhenNoneIsPrivate(t *testing.T) {
	issuer := newServiceTokenIssuer(t)
	handler, _, _ := newTestServer(t, testServerOptions{oidc: issuer.verifier(t), accessAccounts: []string{listedServiceAccount}})
	createAccessProject(t, handler, "PUB", "alice")
	if response := dispatchRequest(t, handler, http.MethodPatch, "/api/v1/projects/PUB/access",
		map[string]any{"slack_channels": []string{"C0PUBLIC001"}}, "alice"); response.Code != http.StatusOK {
		t.Fatalf("alice names PUB's channel: %d %s", response.Code, response.Body.String())
	}
	bearer := issuer.Mint(t, issuer.signing, issuer.Claims(listedServiceAccount, serviceTokenAudience))
	response := agentRequest(t, handler, http.MethodGet, privateProjectsPath, nil, bearer)
	if want := "{\"projects\":[]}\n"; response.Code != http.StatusOK || response.Body.String() != want {
		t.Fatalf("the broker's GET %s with no private project = %d %q, want 200 %q: projects must be an empty array", privateProjectsPath, response.Code, response.Body.String(), want)
	}
}
