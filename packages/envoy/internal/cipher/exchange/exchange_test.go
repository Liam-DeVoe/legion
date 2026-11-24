package exchange_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/cipher/exchange"
	"github.com/sjawhar/envoy/internal/cipher/identity"
	"github.com/sjawhar/envoy/internal/cipher/identity/github"
	"github.com/sjawhar/envoy/internal/cipher/identity/google"
	"github.com/sjawhar/envoy/internal/cipher/identity/kubernetes"
)

const (
	workerAccount = "system:serviceaccount:example-ns:example-worker"
	daemonAccount = "system:serviceaccount:example-ns:example-daemon"
	// readerAccount is the account the read entry lists, readerService the service its token
	// names and readerSession the session it fixes.
	readerAccount = "system:serviceaccount:example-ns:example-reader"
	readerService = "example-reader"
	readerSession = "example-reader-session"
	repository    = "example-org/example-repo"
	workflow      = ".github/workflows/release.yaml"
	nightly       = ".github/workflows/nightly.yaml"
	ref           = "refs/heads/main"
	project       = "ACME"
	person        = "ada@example.com"
	// fixedSession is the session name the daemon account's entry fixes, nightlySession the one
	// the nightly workflow's entry fixes, and machineSession the one the fixed fleet's entry
	// fixes: one name belongs to one principal, so two entries never fix the same one.
	fixedSession   = "legion-daemon:ACME"
	nightlySession = "ci-nightly"
	machineSession = "example-fleet-1"
	podUID         = "11111111-2222-3333-4444-555555555555"
	credentialID   = "66666666-7777-8888-9999-aaaaaaaaaaaa"
	// The three Google accounts the set lists, each a placeholder: pinnedAccount is held to one
	// Compute Engine project and one instance-name pattern and writes for a person, poolAccount
	// is pinned to nothing and names no person, so its machines are its service and nothing
	// else, and fixedAccount is pinned to nothing and fixes the session its machines write
	// under. strangerAccount is listed nowhere.
	pinnedAccount   = "example-fleet@example-project.iam.gserviceaccount.com"
	poolAccount     = "example-pool@example-project.iam.gserviceaccount.com"
	fixedAccount    = "example-fixed@example-project.iam.gserviceaccount.com"
	strangerAccount = "example-stranger@example-project.iam.gserviceaccount.com"
	cloudProject    = "example-project"
	instanceGlob    = "example-vm-*"
	instanceName    = "example-vm-7"
	pinnedService   = "example-fleet"
	poolService     = "example-pool"
	// signInDomain is the Google Workspace domain BROKER_SIGNIN_DOMAIN names in these tests, the
	// one a person signing in on a machine must belong to, and stranger a person of another one.
	signInDomain = "example.com"
	stranger     = "mallory@other.example"
)

// The principals the listed entries' session names belong to, as the exchange spells them.
const (
	workerPrincipal  = "account:" + workerAccount
	daemonPrincipal  = "account:" + daemonAccount
	readerPrincipal  = "account:" + readerAccount
	releasePrincipal = "workflow:" + repository + "/" + workflow + "@" + ref
	nightlyPrincipal = "workflow:" + repository + "/" + nightly + "@" + ref
	personPrincipal  = "person:" + person
	pinnedPrincipal  = "google:" + pinnedAccount
	poolPrincipal    = "google:" + poolAccount
	fixedPrincipal   = "google:" + fixedAccount
)

// listedSet is the set every provider's entries compose, with each provider's decision beside
// it, so a test reads a decision and the set it was listed in side by side, as the exchange does.
type listedSet struct {
	*exchange.Set
	pods     *kubernetes.Provider
	runs     *github.Provider
	machines *google.Provider
}

func (l listedSet) Pod(token map[string]any, session string, projects []string) (exchange.Claims, error) {
	return l.pods.Decide(identity.Trade{TokenType: kubernetes.SubjectTokenType, Subject: token, Session: session, Projects: projects})
}

func (l listedSet) Workflow(token map[string]any, session string, projects []string) (exchange.Claims, error) {
	return l.runs.Decide(identity.Trade{TokenType: github.SubjectTokenType, Subject: token, Session: session, Projects: projects})
}

func (l listedSet) Google(token map[string]any, session string, projects []string) (exchange.Claims, error) {
	return l.machines.Decide(identity.Trade{TokenType: google.SubjectTokenType, Subject: token, Session: session, Projects: projects})
}

func (l listedSet) Person(token, actor map[string]any, session string, projects []string) (exchange.Claims, error) {
	return l.machines.Decide(identity.Trade{TokenType: google.PersonTokenType, Subject: token, Actor: actor, Session: session, Projects: projects})
}

// build composes the providers of the entries given into one set, as the identity registry does.
func build(accounts []exchange.AccountEntry, reads []exchange.ReadEntry, workflows []exchange.WorkflowEntry, machines []exchange.GoogleEntry) (listedSet, error) {
	pods, err := kubernetes.New(kubernetes.Config{Accounts: accounts, Reads: reads})
	if err != nil {
		return listedSet{}, err
	}
	runs, err := github.New(github.Config{Workflows: workflows})
	if err != nil {
		return listedSet{}, err
	}
	fleets, err := google.New(google.Config{Machines: machines, SignInDomain: signInDomain})
	if err != nil {
		return listedSet{}, err
	}
	registry, err := identity.NewRegistry([]identity.Provider{pods, fleets, runs}, nil)
	if err != nil {
		return listedSet{}, err
	}
	return listedSet{Set: registry.Policy(), pods: pods, runs: runs, machines: fleets}, nil
}

// listed is the set every decision test asks: one account whose sessions name themselves, one
// account whose session the entry fixes, one read entry, the same pair for one workflow of one
// repository on one ref, and three Google accounts — one pinned to a project and an
// instance-name pattern that writes for a person, one pinned to nothing that names no person,
// and one that fixes the session its machines write under.
func listed(t *testing.T) listedSet {
	t.Helper()
	set, err := build(
		[]exchange.AccountEntry{
			{Account: workerAccount, Project: project, Person: person},
			{Account: daemonAccount, Project: project, Person: person, Session: fixedSession},
		},
		[]exchange.ReadEntry{{Account: readerAccount, Service: readerService, Session: readerSession}},
		[]exchange.WorkflowEntry{
			{Repository: repository, Workflow: workflow, Ref: ref, Project: project, Person: person},
			{Repository: repository, Workflow: nightly, Ref: ref, Project: project, Person: person, Session: nightlySession},
		},
		[]exchange.GoogleEntry{
			{Account: pinnedAccount, Service: pinnedService, CloudProject: cloudProject, Instance: instanceGlob, Project: project, Person: person},
			{Account: poolAccount, Service: poolService},
			{Account: fixedAccount, Service: poolService, Session: machineSession},
		},
	)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}
	return set
}

// podToken is a projected service-account token's claims as the exchange is handed them, bound to
// podUID unless uid is empty.
func podToken(account, uid string) map[string]any {
	token := map[string]any{"sub": account}
	if uid != "" {
		token["kubernetes.io"] = map[string]any{"pod": map[string]any{"uid": uid}}
	}
	return token
}

// workflowToken is a GitHub workflow run's token claims. jobRef is the workflow the job itself is
// written in, which differs from workflowRef only for a run that called a reusable workflow.
func workflowToken(repo, workflowRef, jobRef, gitRef string) map[string]any {
	return map[string]any{
		"repository":       repo,
		"workflow_ref":     workflowRef,
		"job_workflow_ref": jobRef,
		"ref":              gitRef,
		"run_id":           "4242",
	}
}

// runRef is the workflow_ref a run of path in repository on ref carries.
func runRef(path string) string { return repository + "/" + path + "@" + ref }

// TestNewSetRefusesAnEntryItCannotServe holds what the set refuses across providers, each
// provider having refused its own entries already (internal/cipher/identity/<kind>): a session
// name entries of two providers fix, and an identity two listings name.
func TestNewSetRefusesAnEntryItCannotServe(t *testing.T) {
	account := exchange.AccountEntry{Account: workerAccount, Project: project, Person: person, Session: fixedSession}
	flow := exchange.WorkflowEntry{Repository: repository, Workflow: workflow, Ref: ref, Project: project, Person: person, Session: fixedSession}
	machine := exchange.GoogleEntry{Account: pinnedAccount, Service: pinnedService, Session: fixedSession}
	for _, c := range []struct {
		name      string
		accounts  []exchange.AccountEntry
		workflows []exchange.WorkflowEntry
		machines  []exchange.GoogleEntry
		names     string
	}{
		{name: "a session name an account and a workflow fix",
			accounts: []exchange.AccountEntry{account}, workflows: []exchange.WorkflowEntry{flow}, names: workerAccount},
		{name: "a session name a machine and an account fix",
			accounts: []exchange.AccountEntry{account}, machines: []exchange.GoogleEntry{machine}, names: pinnedAccount},
	} {
		t.Run(c.name, func(t *testing.T) {
			set, err := build(c.accounts, nil, c.workflows, c.machines)
			if err == nil {
				t.Fatalf("NewSet(%s) = %v, want a refusal", c.name, set)
			}
			if !strings.Contains(err.Error(), c.names) {
				t.Errorf("NewSet(%s) refused with %q, which does not name %q", c.name, err, c.names)
			}
		})
	}
	t.Run("an identity two listings name", func(t *testing.T) {
		set, err := exchange.NewSet([]exchange.Listing{
			{Where: "first entry", Principal: "account:" + workerAccount},
			{Where: "second entry", Principal: "account:" + workerAccount},
		})
		if err == nil || !strings.Contains(err.Error(), "first entry") || !strings.Contains(err.Error(), "second entry") {
			t.Fatalf("NewSet(one principal twice) = %v, %v; want a refusal naming both entries", set, err)
		}
	})
}

// TestListedAnswersEachEntrysListing: a reader holding no token reads what an operator's entry
// says of an identity, a machine's service among it, and nothing of an identity no entry lists.
func TestListedAnswersEachEntrysListing(t *testing.T) {
	set := listed(t)
	if got, ok := set.Listed(pinnedPrincipal); !ok || got.Service != pinnedService || got.Person != person || got.Project != project {
		t.Errorf("Listed(%s) = %+v, %t; want its service, person and project", pinnedPrincipal, got, ok)
	}
	if got, ok := set.Listed(readerPrincipal); !ok || got.Service != readerService || got.Session != readerSession {
		t.Errorf("Listed(%s) = %+v, %t; want its service and session", readerPrincipal, got, ok)
	}
	if got, ok := set.Listed(personPrincipal); ok {
		t.Errorf("Listed(%s) = %+v, true; want no listing of a person's own principal", personPrincipal, got)
	}
}

// TestTheReservationsAreTheNamesTheEntriesFix: every session name an entry fixes is reserved for
// that entry's principal, in the order the entries were listed, and a name no entry fixes is not
// reserved at all. The answer is the caller's own copy, so the set stays as it was built.
func TestTheReservationsAreTheNamesTheEntriesFix(t *testing.T) {
	set := listed(t)
	// The providers in rank order, kubernetes, google, github, and each provider's entries in
	// its own order.
	want := []exchange.Reservation{
		{Session: fixedSession, Principal: daemonPrincipal},
		{Session: readerSession, Principal: readerPrincipal},
		{Session: machineSession, Principal: fixedPrincipal},
		{Session: nightlySession, Principal: nightlyPrincipal},
	}
	got := set.Reservations()
	if !slices.Equal(got, want) {
		t.Fatalf("Reservations = %+v, want %+v", got, want)
	}
	got[0].Principal = "account:changed"
	if again := set.Reservations(); !slices.Equal(again, want) {
		t.Errorf("Reservations after the caller changed its copy = %+v, want %+v", again, want)
	}
}

func TestMachineActsForTheApproverAndWritesEveryProjectThePersonMay(t *testing.T) {
	set := listed(t)
	claims, err := set.Machine(credentialID, person, "ada-devbox", nil)
	if err != nil {
		t.Fatalf("Machine: %v", err)
	}
	want := exchange.Claims{Session: "ada-devbox", Person: person, Scope: exchange.ProjectScopePerson, Principal: personPrincipal}
	if !reflect.DeepEqual(claims, want) {
		t.Fatalf("Machine = %+v, want %+v: a machine credential that asked for no project names none and says which absence that is", claims, want)
	}
	// Another machine of the same person is the same principal: the name belongs to the person.
	other, err := set.Machine("77777777-8888-9999-aaaa-bbbbbbbbbbbb", person, "ada-devbox", nil)
	if err != nil || !reflect.DeepEqual(other, want) {
		t.Fatalf("Machine(another credential of %s) = %+v, %v; want %+v", person, other, err, want)
	}
}

// TestEveryDecisionCarriesItsProjects: the zero ProjectScope is what a Claims nobody built
// carries, so no constructor may answer it; a named scope carries its entry's one project and a
// person scope the list its trade asked for, so a later reader never has to guess which a list
// is. A read entry's decision says that it writes nothing, carries no project, and reads apart
// from the zero value in a refusal or a log line too.
func TestEveryDecisionCarriesItsProjects(t *testing.T) {
	set := listed(t)
	pod, err := set.Pod(podToken(workerAccount, podUID), "implementer-g1", nil)
	if err != nil {
		t.Fatalf("Pod: %v", err)
	}
	run, err := set.Workflow(workflowToken(repository, runRef(workflow), runRef(workflow), ref), "release-bot", nil)
	if err != nil {
		t.Fatalf("Workflow: %v", err)
	}
	read, err := set.Pod(podToken(readerAccount, podUID), "", nil)
	if err != nil {
		t.Fatalf("Pod of the read account: %v", err)
	}
	fleet, err := set.Google(googleToken(pinnedAccount, true, cloudProject, instanceName), "example-machine", nil)
	if err != nil {
		t.Fatalf("Google: %v", err)
	}
	service, err := set.Google(googleToken(poolAccount, true, "", ""), "example-pool-1", nil)
	if err != nil {
		t.Fatalf("Google: %v", err)
	}
	machine, err := set.Machine(credentialID, person, "ada-devbox", []string{"PRIV", "PUB"})
	if err != nil {
		t.Fatalf("Machine: %v", err)
	}
	signIn, err := set.Person(personToken(person, true, signInDomain), googleToken(poolAccount, true, "", ""), "example-box", []string{"PRIV"})
	if err != nil {
		t.Fatalf("Person: %v", err)
	}
	for _, c := range []struct {
		name     string
		claims   exchange.Claims
		scope    exchange.ProjectScope
		projects []string
	}{
		{name: "Pod", claims: pod, scope: exchange.ProjectScopeNamed, projects: []string{project}},
		{name: "Workflow", claims: run, scope: exchange.ProjectScopeNamed, projects: []string{project}},
		{name: "Google with a person", claims: fleet, scope: exchange.ProjectScopeNamed, projects: []string{project}},
		{name: "Google naming no person", claims: service, scope: exchange.ProjectScopePerson, projects: nil},
		{name: "Machine", claims: machine, scope: exchange.ProjectScopePerson, projects: []string{"PRIV", "PUB"}},
		{name: "Person", claims: signIn, scope: exchange.ProjectScopePerson, projects: []string{"PRIV"}},
		{name: "Pod of the read account", claims: read, scope: exchange.ProjectScopeNone, projects: nil},
	} {
		if c.claims.Scope != c.scope || !slices.Equal(c.claims.Projects, c.projects) {
			t.Errorf("%s = scope %v, projects %v; want scope %v, projects %v", c.name, c.claims.Scope, c.claims.Projects, c.scope, c.projects)
		}
	}
	if got := exchange.ProjectScopeUnset.String(); !strings.Contains(got, "no project scope") {
		t.Errorf("ProjectScopeUnset.String() = %q, want it to say the scope was never set", got)
	}
	if none, unset := exchange.ProjectScopeNone.String(), exchange.ProjectScopeUnset.String(); none == unset || strings.Contains(none, "no project scope") {
		t.Errorf("ProjectScopeNone.String() = %q beside ProjectScopeUnset's %q, want a decision that writes nothing to read apart from one never made", none, unset)
	}
}

func TestMachineRefusesWhatItCannotDecide(t *testing.T) {
	set := listed(t)
	for _, c := range []struct {
		name       string
		credential string
		approver   string
		session    string
		want       error
		names      string
	}{
		{name: "a credential with no id", credential: " ", approver: person, session: "ada-devbox", want: exchange.ErrClaimMissing, names: "id"},
		{name: "a credential with no approver", credential: credentialID, approver: " ", session: "ada-devbox", want: exchange.ErrClaimMissing, names: "approver"},
		{name: "a request naming no session", credential: credentialID, approver: person, session: "", want: exchange.ErrSessionRequired, names: person},
		{name: "a request naming a session with whitespace", credential: credentialID, approver: person, session: "ada devbox", want: exchange.ErrSessionInvalid, names: person},
		{name: "a request naming a session past the cap", credential: credentialID, approver: person, session: strings.Repeat("s", exchange.MaxSessionBytes+1), want: exchange.ErrSessionInvalid, names: person},
		{name: "a request naming the wildcard session *", credential: credentialID, approver: person, session: "*", want: exchange.ErrSessionInvalid, names: person},
		{name: "a request naming the wildcard session >", credential: credentialID, approver: person, session: ">", want: exchange.ErrSessionInvalid, names: person},
		{name: "a request naming a session holding a wildcard", credential: credentialID, approver: person, session: "ada-*", want: exchange.ErrSessionInvalid, names: person},
	} {
		t.Run(c.name, func(t *testing.T) {
			claims, err := set.Machine(c.credential, c.approver, c.session, nil)
			if !errors.Is(err, c.want) {
				t.Fatalf("Machine(%s) = %+v, %v; want %v", c.name, claims, err, c.want)
			}
			if !strings.Contains(err.Error(), c.names) {
				t.Errorf("Machine(%s) refused with %q, which does not name %q", c.name, err, c.names)
			}
		})
	}
}

// TestASessionNameHoldsNoWildcard: ValidSession takes every shape of session name a deployment
// trades under, now that it refuses a name holding a NATS wildcard ('*' or '>', whose rows are
// TestMachineRefusesWhatItCannotDecide's): an Envoy session id, the Legion daemon's fixed session,
// a run pod's fixed session holding a slash, the read entry's, and a person's sign-in
// (cipherclient.SessionFor), which holds the person's address and so a '.'.
func TestASessionNameHoldsNoWildcard(t *testing.T) {
	for _, name := range []string{"01930000-0000-7000-8000-000000000001", "ses_agent", fixedSession, "run-holds/example-run", readerSession, "signin-ada@example.com-abcdefghijkl"} {
		if !exchange.ValidSession(name) {
			t.Errorf("ValidSession(%q) = false, want true", name)
		}
	}
}

// googleToken is a Google identity token's claims as the exchange is handed them: the account it
// was minted for, whether Google holds the address verified, and the Compute Engine instance it
// was minted on — none when instance is empty, which is what a token minted through a workload
// identity pool and a pod's own metadata token carry.
func googleToken(account string, verified any, cloud, instance string) map[string]any {
	token := map[string]any{"email": account, "email_verified": verified}
	if instance != "" {
		token["google"] = map[string]any{"compute_engine": map[string]any{
			"project_id":    cloud,
			"zone":          "example-zone-a",
			"instance_name": instance,
			"instance_id":   "1234567890",
		}}
	}
	return token
}

// personToken is a person's own Google identity token's claims as the exchange is handed them:
// the subject Google gives the account, the address it was minted for, whether Google holds that
// address verified, and the Workspace domain in `hd`.
func personToken(login string, verified any, domain string) map[string]any {
	token := map[string]any{"sub": "109" + login, "email": login, "email_verified": verified}
	if domain != "" {
		token["hd"] = domain
	}
	return token
}
