package oauth_test

import (
	"context"
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/exchange"
	"github.com/sjawhar/envoy/internal/broker/oauth"
	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/internal/brokertoken/brokertokentest"
	"github.com/sjawhar/envoy/internal/dispatch/auth"
)

// The entries TestTheBrokerGivesLegionStatusToTheDaemonAlone lists beside the fixture's own
// account, daemon, read and sign-in entries. Each one mints a token whose session name looks like a
// Legion daemon's or a Legion role's, and none of them is a daemon's entry.
const (
	// legionStatusScope is the OAuth scope that lets a token write a Legion issue's status. The
	// test spells the word out, so it holds the word on the wire and not a constant beside it.
	legionStatusScope = "legion-status"
	// otherProjectAccount's entry names the project OTHERP and fixes no session, so its pod
	// names its session in the request.
	otherProjectAccount = "system:serviceaccount:example-ns:example-otherp"
	// crossProjectAccount's entry names the project P2 and fixes the daemon session of P3.
	crossProjectAccount = "system:serviceaccount:example-ns:example-cross"
	// noProjectAccount's entry names no project and fixes the daemon session prefix alone.
	noProjectAccount = "system:serviceaccount:example-ns:example-noproject"
	// roleAccount's entry names the project ACME and fixes the session of a Legion role.
	roleAccount = "system:serviceaccount:example-ns:example-role"
	roleSession = "legion-acme-acme-1-implementer"
	// legionWorkflow's entry names the project WF and fixes the daemon session of WF.
	legionWorkflow = ".github/workflows/legion.yaml"
	// vmMachine's entry names a person and the project VM, and fixes the daemon session of VM.
	vmMachine = "example-vm@example-project.iam.gserviceaccount.com"
	vmService = "example-vm"
	// dispatchOrigin is the public origin of the Dispatch whose verifier reads each token.
	dispatchOrigin = "https://dispatch.example"
)

// TestTheBrokerGivesLegionStatusToTheDaemonAlone: the broker grants the OAuth scope legion-status
// to one kind of token alone. An account entry mints it when the entry names a project and fixes
// the session legion-daemon:<that project>, as a Legion daemon's entry does. Dispatch lets a token
// that holds the scope write a Legion issue's status, so no other token may hold it. Each row
// below is one other entry or trade. Some carry a daemon's session name, and some fix a daemon's
// name for another project or for no project. Each of them mints a token that holds no scope. The
// read entry's token holds read alone. A request that names legion-status in its scope parameter
// is refused invalid_scope, and the broker mints nothing for it.
//
// The broker's answer about each token then reaches a verifier built with the options Dispatch
// gives it (cmd/dispatch, brokerTokenVerifier), through a stub of the broker's introspection
// route. The live route answers the provider's answer unchanged (internal/broker/api,
// handlers_introspect.go), so the stub gives the verifier what the route gives it. Verifier.Live
// reads that answer as Verify does, without the proof check. The Caller it answers has
// LegionStatus set for the daemon's token alone.
func TestTheBrokerGivesLegionStatusToTheDaemonAlone(t *testing.T) {
	legion := func(claims map[string]any) {
		claims["workflow_ref"] = repository + "/" + legionWorkflow + "@" + ref
		claims["job_workflow_ref"] = claims["workflow_ref"]
	}
	// One policy holds every entry, so one broker mints every row's token. NewSet refuses two
	// entries that fix one session name, so each entry below fixes a name of its own.
	f := newSignInFixtureWith(t, func(c *oauth.Config) {
		c.Exchange.Policy = newPolicyReading(t,
			[]exchange.AccountEntry{
				{Account: account, Project: project, Person: person},
				{Account: daemonAccount, Project: project, Person: person, Session: daemonSession},
				{Account: otherProjectAccount, Project: "OTHERP", Person: person},
				{Account: crossProjectAccount, Project: "P2", Person: person, Session: "legion-daemon:P3"},
				{Account: noProjectAccount, Person: person, Session: "legion-daemon:"},
				{Account: roleAccount, Project: project, Person: person, Session: roleSession},
			},
			[]exchange.ReadEntry{{Account: readAccount, Service: readService, Session: readSession}},
			[]exchange.WorkflowEntry{
				{Repository: repository, Workflow: legionWorkflow, Ref: ref, Project: "WF", Person: person, Session: "legion-daemon:WF"},
			},
			exchange.GoogleEntry{Account: signInMachine, Service: signInFleet},
			exchange.GoogleEntry{Account: vmMachine, Service: vmService, Project: "VM", Person: person, Session: "legion-daemon:VM"},
		)
	})
	broker, verifier := newDispatchVerifier(t)

	for _, c := range []struct {
		name string
		// trade mints the row's token through the broker's token endpoint, bound to key.
		trade func(t *testing.T, key *ecdsa.PrivateKey) string
		// scope is the OAuth scope the broker's introspection answers for the token.
		scope string
		// caller is what Dispatch's verifier reads from that answer.
		caller brokertoken.Caller
	}{
		{name: "the daemon's entry, an account of ACME fixing legion-daemon:ACME",
			trade: func(t *testing.T, key *ecdsa.PrivateKey) string {
				return f.exchanged(t, exchangeForm(f.podTokenOf(t, daemonAccount, "pod-uid-daemon"), oauthwire.SubjectTokenTypeKubernetes, ""), key)
			},
			scope: legionStatusScope,
			caller: brokertoken.Caller{Session: daemonSession, Person: person, Projects: []string{project},
				ProjectScope: brokertoken.ProjectScopeNamed, LegionStatus: true}},
		{name: "the plain account entry, which fixes no session",
			trade: func(t *testing.T, key *ecdsa.PrivateKey) string {
				f.registers(t, account, "pod-uid-plain", "implementer-g1")
				return f.exchanged(t, exchangeForm(f.podTokenOf(t, account, "pod-uid-plain"), oauthwire.SubjectTokenTypeKubernetes, "implementer-g1"), key)
			},
			caller: brokertoken.Caller{Session: "implementer-g1", Person: person, Projects: []string{project}, ProjectScope: brokertoken.ProjectScopeNamed}},
		{name: "an account of OTHERP fixing no session, whose request names legion-daemon:OTHERP",
			trade: func(t *testing.T, key *ecdsa.PrivateKey) string {
				f.registers(t, otherProjectAccount, "pod-uid-otherp", "legion-daemon:OTHERP")
				return f.exchanged(t, exchangeForm(f.podTokenOf(t, otherProjectAccount, "pod-uid-otherp"), oauthwire.SubjectTokenTypeKubernetes, "legion-daemon:OTHERP"), key)
			},
			caller: brokertoken.Caller{Session: "legion-daemon:OTHERP", Person: person, Projects: []string{"OTHERP"}, ProjectScope: brokertoken.ProjectScopeNamed}},
		{name: "an account of P2 fixing legion-daemon:P3",
			trade: func(t *testing.T, key *ecdsa.PrivateKey) string {
				return f.exchanged(t, exchangeForm(f.podTokenOf(t, crossProjectAccount, "pod-uid-cross"), oauthwire.SubjectTokenTypeKubernetes, ""), key)
			},
			caller: brokertoken.Caller{Session: "legion-daemon:P3", Person: person, Projects: []string{"P2"}, ProjectScope: brokertoken.ProjectScopeNamed}},
		{name: "an account naming no project, fixing legion-daemon:",
			trade: func(t *testing.T, key *ecdsa.PrivateKey) string {
				return f.exchanged(t, exchangeForm(f.podTokenOf(t, noProjectAccount, "pod-uid-noproject"), oauthwire.SubjectTokenTypeKubernetes, ""), key)
			},
			caller: brokertoken.Caller{Session: "legion-daemon:", Person: person, ProjectScope: brokertoken.ProjectScopeNamed}},
		{name: "an account of ACME fixing the role's session legion-acme-acme-1-implementer",
			trade: func(t *testing.T, key *ecdsa.PrivateKey) string {
				return f.exchanged(t, exchangeForm(f.podTokenOf(t, roleAccount, "pod-uid-role"), oauthwire.SubjectTokenTypeKubernetes, ""), key)
			},
			caller: brokertoken.Caller{Session: roleSession, Person: person, Projects: []string{project}, ProjectScope: brokertoken.ProjectScopeNamed}},
		{name: "a workflow of WF fixing legion-daemon:WF",
			trade: func(t *testing.T, key *ecdsa.PrivateKey) string {
				return f.exchanged(t, exchangeForm(f.workflowToken(t, "7001", legion), oauthwire.SubjectTokenTypeGitHub, ""), key)
			},
			caller: brokertoken.Caller{Session: "legion-daemon:WF", Person: person, Projects: []string{"WF"}, ProjectScope: brokertoken.ProjectScopeNamed}},
		{name: "a VM entry with a person, of VM, fixing legion-daemon:VM",
			trade: func(t *testing.T, key *ecdsa.PrivateKey) string {
				return f.exchanged(t, exchangeForm(f.machineToken(t, vmMachine, machineAudience, true), oauthwire.SubjectTokenTypeGoogle, ""), key)
			},
			caller: brokertoken.Caller{Session: "legion-daemon:VM", Person: person, Projects: []string{"VM"}, ProjectScope: brokertoken.ProjectScopeNamed}},
		{name: "a machine login's trade, naming legion-daemon:DEV",
			trade: func(t *testing.T, key *ecdsa.PrivateKey) string {
				return f.exchanged(t, exchangeForm(f.devBoxToken(t, key, person), oauthwire.SubjectTokenTypeAccessToken, "legion-daemon:DEV"), key)
			},
			caller: brokertoken.Caller{Session: "legion-daemon:DEV", Person: person, ProjectScope: brokertoken.ProjectScopePerson}},
		{name: "a person's sign-in trade, naming legion-daemon:BOX",
			trade: func(t *testing.T, key *ecdsa.PrivateKey) string {
				return f.exchanged(t, signInForm(
					f.personToken(t, person, deviceAudience, true, signInDomain),
					f.machineToken(t, signInMachine, machineAudience, true),
					"legion-daemon:BOX"), key)
			},
			caller: brokertoken.Caller{Session: "legion-daemon:BOX", Person: person, ProjectScope: brokertoken.ProjectScopePerson}},
		{name: "the read entry",
			trade: func(t *testing.T, key *ecdsa.PrivateKey) string {
				return f.exchanged(t, f.readPodForm(t), key)
			},
			scope:  oauthwire.ScopeRead,
			caller: brokertoken.Caller{Session: readSession, Service: readService, ProjectScope: brokertoken.ProjectScopeNone, ReadOnly: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			token := c.trade(t, newKey(t))
			answer := f.introspected(t, token)
			if answer.Scope != c.scope {
				t.Errorf("the broker's introspection answered the scope %q, want %q", answer.Scope, c.scope)
			}
			broker.Says(token, answer)
			caller := liveCaller(t, verifier, token)
			switch {
			case caller.LegionStatus != c.caller.LegionStatus:
				t.Errorf("Dispatch's verifier read LegionStatus %t from %+v, want %t", caller.LegionStatus, answer, c.caller.LegionStatus)
			case !reflect.DeepEqual(caller, c.caller):
				t.Errorf("Dispatch's verifier read %+v, want %+v", caller, c.caller)
			}
		})
	}

	t.Run("the daemon's entry, whose request names legion-status in its scope parameter", func(t *testing.T) {
		before := f.count(t, "oauth_access_tokens")
		asked := exchangeForm(f.podTokenOf(t, daemonAccount, "pod-uid-daemon"), oauthwire.SubjectTokenTypeKubernetes, "")
		asked.Set("scope", legionStatusScope)
		status, body := f.exchange(t, asked, newKey(t))
		if status != http.StatusBadRequest || body["error"] != "invalid_scope" || body["access_token"] != nil {
			t.Fatalf("an exchange naming the scope %s = %d %v, want 400 invalid_scope and no access token", legionStatusScope, status, body)
		}
		if after := f.count(t, "oauth_access_tokens"); after != before {
			t.Fatalf("access tokens after the refused exchange = %d, want the %d there were", after, before)
		}
	})
}

// newDispatchVerifier is a broker-token verifier built with the options Dispatch gives its own
// (cmd/dispatch, brokerTokenVerifier), over a stub of the broker's introspection route. Only the
// replay store differs: Dispatch's is a table of its database, and this one is the test's own.
// Verifier.Live checks no proof, so it never writes to that store.
func newDispatchVerifier(t *testing.T) (*brokertokentest.StubBroker, *brokertoken.Verifier) {
	t.Helper()
	broker := brokertokentest.NewStubBroker(t)
	accountToken := filepath.Join(t.TempDir(), "account-token")
	if err := os.WriteFile(accountToken, []byte("dispatch-account-token\n"), 0o600); err != nil {
		t.Fatalf("write Dispatch's own account token: %v", err)
	}
	verifier, err := brokertoken.New(brokertoken.Options{
		BrokerURL:      broker.Server.URL,
		Account:        brokertoken.NewAccountToken(accountToken),
		Origin:         dispatchOrigin,
		Replay:         &spentProofs{},
		Address:        auth.ForwardedAddress,
		ServesReadOnly: true,
	})
	if err != nil {
		t.Fatalf("brokertoken.New: %v", err)
	}
	return broker, verifier
}

// liveCaller is the Caller verifier reads for a request that presents token under the DPoP scheme,
// failing the test when the verifier refuses it.
func liveCaller(t *testing.T, verifier *brokertoken.Verifier, token string) brokertoken.Caller {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	request.Header.Set("Authorization", "DPoP "+token)
	caller, err := verifier.Live(t.Context(), request)
	if err != nil {
		t.Fatalf("Verifier.Live: %v", err)
	}
	return caller
}

// spentProofs is a DPoP replay store in memory. It records each proof by its key, its jti and its
// target URI, and answers a proof it saw before as used.
type spentProofs struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (s *spentProofs) CheckAndSetDPoPProofUsed(_ context.Context, jti, jkt, _, _, htu string, _ time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	key := jkt + "\x00" + jti + "\x00" + htu
	used := s.seen[key]
	s.seen[key] = true
	return used, nil
}
