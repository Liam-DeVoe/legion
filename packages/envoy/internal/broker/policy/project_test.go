package policy_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/policy"
	"github.com/sjawhar/envoy/internal/broker/policy/policytest"
	"github.com/sjawhar/envoy/internal/broker/record"
	"github.com/sjawhar/envoy/internal/broker/secrets"
)

// The people and projects of the project tag's tests. carol owns PRIV and bob is on its access
// list; alice is neither. PUB is a public project, and GONE a key Dispatch holds no project for.
const (
	carol       = "carol@example.com"
	bob         = "bob@example.com"
	alice       = "alice@example.com"
	privProject = "PRIV"
	pubProject  = "PUB"
	goneProject = "GONE"
	projectSvc  = "priv-daemon"
)

// projectSecret is a namespace secret tagged with project, owned by owner at tier.
func projectSecret(name, owner, tier, project string) secrets.LocalSecret {
	s := policytest.Secret(name, owner, tier, "v")
	s.Tags[policy.TagProject] = project
	return s
}

// projectLoader is the Loader of a broker that holds a K3 client, so it serves a tagged secret.
func projectLoader(store *secrets.Local) policy.Loader {
	loader := policytest.Loader(store, projectSvc)
	loader.ProjectChecks = true
	return loader
}

// loadProjects loads store through projectLoader, failing the test on an error.
func loadProjects(t *testing.T, store *secrets.Local) *policy.Set {
	t.Helper()
	set, err := projectLoader(store).Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return set
}

// states is the plain data a caller resolves before it evaluates: PRIV private, PUB public and
// GONE a project Dispatch does not hold.
var states = map[string]policy.ProjectState{
	privProject: policy.ProjectPrivate,
	pubProject:  policy.ProjectPublic,
	goneProject: policy.ProjectMissing,
}

// credential is a requester whose credential names projects, with the states its caller resolved.
func credential(operator, service string, projects ...string) policy.Requester {
	return policy.Requester{Operator: operator, Service: service, Projects: projects, ProjectStates: states}
}

// TestAPrivateProjectSecretGoesOnlyToACredentialThatNamesIt is R11 in the policy: a secret tagged
// with a private project is released to a credential that names that project, and refused,
// project-not-named, to one naming another project, to one naming none, and to the dev-box
// session of a person on the project's access list whose credential does not name it. Who the
// person is decides nothing here: naming the project is what the rule reads. A pod of the
// project's service whose credential names it gets the secret too.
func TestAPrivateProjectSecretGoesOnlyToACredentialThatNamesIt(t *testing.T) {
	set := loadProjects(t, secrets.NewLocal(projectSecret("PRIV_KEY", policy.OwnerShared, policy.TierAgent, privProject)))
	source := secrets.LocalARN(policytest.ID("PRIV_KEY"))
	released := policy.Decision{Outcome: policy.Automatic, Source: source}
	refused := policy.Decision{Outcome: policy.Deny, Reason: policy.ReasonProjectNotNamed, Source: source}
	for _, c := range []struct {
		credential string
		requester  policy.Requester
		want       policy.Decision
	}{
		{"carol's session naming PRIV", credential(carol, "", privProject), released},
		{"bob's session naming PUB and PRIV", credential(bob, "", privProject, pubProject), released},
		{"the PRIV daemon's pod naming PRIV", credential("", projectSvc, privProject), released},
		{"bob's session naming another project, PUB", credential(bob, "", pubProject), refused},
		{"alice's session naming no project", credential(alice, ""), refused},
		{"carol's dev-box session, carol a member of PRIV, naming no project", credential(carol, ""), refused},
		{"a pod naming no project", credential("", projectSvc), refused},
	} {
		got, err := set.Evaluate("PRIV_KEY", c.requester)
		if err != nil {
			t.Errorf("Evaluate(PRIV_KEY) for %s = %v; want %+v", c.credential, err, c.want)
			continue
		}
		if got != c.want {
			if got.Outcome != policy.Deny && c.want.Outcome == policy.Deny {
				t.Errorf("%s got the private project's secret PRIV_KEY (%+v), but its credential does not name %s; want %+v",
					c.credential, got, privProject, c.want)
				continue
			}
			t.Errorf("Evaluate(PRIV_KEY) for %s = %+v; want %+v", c.credential, got, c.want)
		}
	}
}

// TestAPrivateProjectSecretOwnedByAPersonStillAsksItsOwner is the rule's other half: a credential
// that names the project passes the project rule and nothing more. Owner, tier, the requesters
// tag and a withheld name answer after it exactly as they answer for an untagged secret, so a
// person's human-tier secret of PRIV still asks its owner, and its agent-tier one goes at once to
// its owner's sessions alone.
func TestAPrivateProjectSecretOwnedByAPersonStillAsksItsOwner(t *testing.T) {
	listed := projectSecret("PRIV_LISTED", carol, policy.TierHuman, privProject)
	listed.Tags[policy.TagRequesters] = carol
	set := loadProjects(t, secrets.NewLocal(
		projectSecret("PRIV_HUMAN", carol, policy.TierHuman, privProject),
		projectSecret("PRIV_AGENT", carol, policy.TierAgent, privProject),
		listed,
	))
	approval := policy.Decision{Outcome: policy.Approval, Approver: carol}
	withheld := credential(carol, "", privProject)
	withheld.Withheld = []string{"PRIV_AGENT"}
	for _, c := range []struct {
		name, credential string
		requester        policy.Requester
		want             policy.Decision
	}{
		{"PRIV_HUMAN", "carol's session naming PRIV", credential(carol, "", privProject), approval},
		{"PRIV_HUMAN", "bob's session naming PRIV", credential(bob, "", privProject), approval},
		{"PRIV_AGENT", "carol's session naming PRIV", credential(carol, "", privProject), policy.Decision{Outcome: policy.Automatic}},
		{"PRIV_AGENT", "bob's session naming PRIV", credential(bob, "", privProject), approval},
		{"PRIV_AGENT", "carol's session naming PRIV, PRIV_AGENT withheld", withheld, approval},
		{"PRIV_LISTED", "bob's session naming PRIV", credential(bob, "", privProject), policy.Decision{Outcome: policy.Deny, Reason: policy.ReasonRequesterNotListed}},
		{"PRIV_LISTED", "carol's session naming PRIV", credential(carol, "", privProject), approval},
		{"PRIV_HUMAN", "carol's session naming no project", credential(carol, ""), policy.Decision{Outcome: policy.Deny, Reason: policy.ReasonProjectNotNamed}},
	} {
		got, err := set.Evaluate(c.name, c.requester)
		c.want.Source = secrets.LocalARN(policytest.ID(c.name))
		if err != nil || got != c.want {
			t.Errorf("Evaluate(%s) for %s = %+v, %v; want %+v", c.name, c.credential, got, err, c.want)
		}
	}
}

// TestAPrivateProjectSecretOfAPublicProjectFollowsOwnerAndTier is R9 for a tagged secret: a
// secret whose project is public answers every credential, whatever projects it names or does
// not, exactly as owner and tier answer an untagged one.
func TestAPrivateProjectSecretOfAPublicProjectFollowsOwnerAndTier(t *testing.T) {
	set := loadProjects(t, secrets.NewLocal(
		projectSecret("PUB_SHARED", policy.OwnerShared, policy.TierAgent, pubProject),
		projectSecret("PUB_PERSON", carol, policy.TierAgent, pubProject),
		projectSecret("PUB_HUMAN", policy.OwnerShared, policy.TierHuman, pubProject),
		projectSecret("PUB_SERVICE", projectSvc, policy.TierAgent, pubProject),
	))
	automatic := policy.Decision{Outcome: policy.Automatic}
	for _, c := range []struct {
		name, credential string
		requester        policy.Requester
		want             policy.Decision
	}{
		{"PUB_SHARED", "alice's session naming no project", credential(alice, ""), automatic},
		{"PUB_SHARED", "bob's session naming PRIV", credential(bob, "", privProject), automatic},
		{"PUB_SHARED", "a pod naming no project", credential("", ""), automatic},
		{"PUB_PERSON", "carol's session naming no project", credential(carol, ""), automatic},
		{"PUB_PERSON", "alice's session naming PUB", credential(alice, "", pubProject), policy.Decision{Outcome: policy.Approval, Approver: carol}},
		{"PUB_HUMAN", "alice's session naming no project", credential(alice, ""), policy.Decision{Outcome: policy.Approval, Approver: record.AnyoneApprover}},
		{"PUB_SERVICE", "the service's pod naming no project", credential("", projectSvc), automatic},
		{"PUB_SERVICE", "alice's session naming PUB", credential(alice, "", pubProject), policy.Decision{Outcome: policy.Deny}},
	} {
		got, err := set.Evaluate(c.name, c.requester)
		c.want.Source = secrets.LocalARN(policytest.ID(c.name))
		if err != nil || got != c.want {
			t.Errorf("Evaluate(%s) for %s = %+v, %v; want %+v", c.name, c.credential, got, err, c.want)
		}
	}
}

// TestAPrivateProjectSecretTaggedWithAMissingProjectGoesToNobody is the tag that names a project
// Dispatch does not hold — a typo, a renamed key: it fails closed, denied project-missing to
// every credential, the one naming that key included. The caller is told the same reason as for
// an unnamed private project, so a refusal never says whether a project exists; the broker's own
// log keeps project-missing apart.
func TestAPrivateProjectSecretTaggedWithAMissingProjectGoesToNobody(t *testing.T) {
	set := loadProjects(t, secrets.NewLocal(projectSecret("GONE_KEY", policy.OwnerShared, policy.TierAgent, goneProject)))
	want := policy.Decision{Outcome: policy.Deny, Reason: policy.ReasonProjectMissing, Source: secrets.LocalARN(policytest.ID("GONE_KEY"))}
	for who, r := range map[string]policy.Requester{
		"carol's session naming GONE":   credential(carol, "", goneProject),
		"carol's session naming none":   credential(carol, ""),
		"a pod of the service":          credential("", projectSvc, goneProject),
		"bob's session naming PRIV":     credential(bob, "", privProject),
		"alice's session naming PUB":    credential(alice, "", pubProject),
		"carol's session naming both":   credential(carol, "", goneProject, privProject),
		"the service's pod naming GONE": credential("", projectSvc, goneProject),
	} {
		if got, err := set.Evaluate("GONE_KEY", r); err != nil || got != want {
			t.Errorf("Evaluate(GONE_KEY) for %s = %+v, %v; want %+v: a tag naming no project goes to nobody", who, got, err, want)
		}
	}
	if got, want := policy.CallerReason(policy.ReasonProjectMissing), policy.CallerReason(policy.ReasonProjectNotNamed); got != want {
		t.Errorf("a caller is told %q for a missing project and %q for an unnamed private one; want one text, so the refusal does not say whether the project exists", got, want)
	}
	if got := policy.CallerReason(policy.ReasonProjectNotNamed); got != policy.ReasonProjectNotNamed {
		t.Errorf("CallerReason(%s) = %q; want it unchanged", policy.ReasonProjectNotNamed, got)
	}
	if got := policy.CallerReason(policy.ReasonRequesterNotListed); got != policy.ReasonRequesterNotListed {
		t.Errorf("CallerReason(%s) = %q; want every other reason unchanged", policy.ReasonRequesterNotListed, got)
	}
}

// TestAPrivateProjectSecretWithNoStateIsAnError is the rule's own guard: Evaluate does no I/O,
// so a caller that evaluates a tagged secret without having resolved its project's state gets
// ErrNoProjectState rather than a guess — neither a release nor a denial it did not decide.
func TestAPrivateProjectSecretWithNoStateIsAnError(t *testing.T) {
	set := loadProjects(t, secrets.NewLocal(
		projectSecret("PRIV_KEY", policy.OwnerShared, policy.TierAgent, privProject),
		policytest.Secret("PLAIN_KEY", policy.OwnerShared, policy.TierAgent, "v"),
	))
	for who, r := range map[string]policy.Requester{
		"no states at all":       {Operator: carol, Projects: []string{privProject}},
		"states for another key": {Operator: carol, Projects: []string{privProject}, ProjectStates: map[string]policy.ProjectState{pubProject: policy.ProjectPublic}},
		"the state unknown":      {Operator: carol, Projects: []string{privProject}, ProjectStates: map[string]policy.ProjectState{privProject: policy.ProjectStateUnknown}},
	} {
		if got, err := set.Evaluate("PRIV_KEY", r); !errors.Is(err, policy.ErrNoProjectState) {
			t.Errorf("Evaluate(PRIV_KEY) with %s = %+v, %v; want ErrNoProjectState", who, got, err)
		}
	}
	// An untagged secret reads no state, so a caller that resolved none is answered as today.
	if got, err := set.Evaluate("PLAIN_KEY", policy.Requester{Operator: carol}); err != nil || got.Outcome != policy.Automatic {
		t.Errorf("Evaluate(PLAIN_KEY) with no states = %+v, %v; want automatic", got, err)
	}
}

// TestAPrivateProjectSecretTagMovesTheVersionOnlyWhenPresent pins the digest's bytes: a secret
// with no project tag writes exactly the line it wrote before the tag existed, in each of
// NewSet's three forms, so no deployed version moves; a tagged one ends its line with
// "\tproject=<KEY>" after the shape field, and so moves the version, which ends the grants the
// policy decided before the tag.
func TestAPrivateProjectSecretTagMovesTheVersionOnlyWhenPresent(t *testing.T) {
	const arn = "arn:aws:secretsmanager:local:000000000000:secret:example/agent-secrets/"
	digest := func(lines string) string {
		sum := sha256.Sum256([]byte(lines))
		return hex.EncodeToString(sum[:])
	}
	for name, c := range map[string]struct {
		secret         policy.Secret
		untagged, line string
	}{
		"a plain secret": {
			secret:   policy.Secret{Name: "A_KEY", Owner: carol, Tier: policy.TierAgent, Source: arn + "a-key"},
			untagged: "A_KEY\tcarol@example.com\tagent\t" + arn + "a-key\n",
			line:     "A_KEY\tcarol@example.com\tagent\t" + arn + "a-key\t0s\t\tproject=PRIV\n",
		},
		"a secret with a shape and a max-grant": {
			secret:   policy.Secret{Name: "A_KEY", Owner: carol, Tier: policy.TierAgent, Source: arn + "a-key", Shape: policy.ShapeToken, MaxGrant: time.Hour},
			untagged: "A_KEY\tcarol@example.com\tagent\t" + arn + "a-key\t1h0m0s\t\tshape=token\n",
			line:     "A_KEY\tcarol@example.com\tagent\t" + arn + "a-key\t1h0m0s\t\tshape=token\tproject=PRIV\n",
		},
		"a session-default secret": {
			secret:   policy.Secret{Name: "A_KEY", Owner: carol, Tier: policy.TierAgent, Source: arn + "a-key", SessionDefault: true},
			untagged: "A_KEY\tcarol@example.com\tagent\t" + arn + "a-key\t0s\t\tdefault-grant=session\n",
			line:     "A_KEY\tcarol@example.com\tagent\t" + arn + "a-key\t0s\t\tdefault-grant=session\tproject=PRIV\n",
		},
	} {
		untagged := policy.NewSet(map[string]policy.Secret{c.secret.Name: c.secret})
		if want := digest(c.untagged); untagged.Version != want {
			t.Errorf("%s with no project tag: Version = %s, want %s, the digest of the line it wrote before the tag existed", name, untagged.Version, want)
		}
		tagged := c.secret
		tagged.Project = privProject
		set := policy.NewSet(map[string]policy.Secret{tagged.Name: tagged})
		if want := digest(c.line); set.Version != want {
			t.Errorf("%s tagged PRIV: Version = %s, want %s, the digest of %q", name, set.Version, want, c.line)
		}
		if set.Version == untagged.Version {
			t.Errorf("%s: tagging it PRIV left the version at %s", name, set.Version)
		}
	}
	// The golden namespace loads to the same version whether or not the broker checks projects.
	checked, err := projectLoader(goldenFixture()).Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	const golden = "252a3be479774f1163b99a7fbeeec17d523eb5e7048d54f90b8cb5e702fc16ca"
	if checked.Version != golden {
		t.Errorf("the golden namespace under a broker that checks projects has version %s, want %s, TestGoldenDigest's", checked.Version, golden)
	}
}

// TestAPrivateProjectSecretTagIsLoaded is the loader's half: a tag naming a project key is read
// as written into Secret.Project, and the loader refuses, by name, a tag that is no project key
// (project-malformed, the word none and an empty tag among them, since retag --project none
// removes the tag rather than writing it), and any tagged secret on a broker that holds no K3
// client (project-unchecked), which could not tell whether the project is private and would
// otherwise release it to every session.
func TestAPrivateProjectSecretTagIsLoaded(t *testing.T) {
	store := secrets.NewLocal(
		projectSecret("PRIV_KEY", policy.OwnerShared, policy.TierAgent, privProject),
		projectSecret("LOWER_KEY", policy.OwnerShared, policy.TierAgent, "priv"),
		projectSecret("NONE_KEY", policy.OwnerShared, policy.TierAgent, "none"),
		projectSecret("EMPTY_KEY", policy.OwnerShared, policy.TierAgent, ""),
		projectSecret("LONG_KEY", policy.OwnerShared, policy.TierAgent, "ABCDEFGHIJK"),
		projectSecret("SPACED_KEY", policy.OwnerShared, policy.TierAgent, " PRIV"),
		policytest.Secret("PLAIN_KEY", policy.OwnerShared, policy.TierAgent, "v"),
	)
	set := loadProjects(t, store)
	if got := set.Secrets["PRIV_KEY"].Project; got != privProject {
		t.Errorf("PRIV_KEY's Project = %q, want %q", got, privProject)
	}
	if got := set.Secrets["PLAIN_KEY"].Project; got != "" {
		t.Errorf("PLAIN_KEY's Project = %q, want none", got)
	}
	for _, name := range []string{"LOWER_KEY", "NONE_KEY", "EMPTY_KEY", "LONG_KEY", "SPACED_KEY"} {
		want := policy.Refusal{Reason: policy.ReasonProjectMalformed, Owner: policy.OwnerShared}
		if got := set.Refused[policytest.ID(name)]; got != want {
			t.Errorf("%s refused as %+v, want %+v", name, got, want)
		}
	}
	unchecked, err := policytest.Loader(store, projectSvc).Load(context.Background())
	if err != nil {
		t.Fatalf("Load with no K3 client: %v", err)
	}
	if got, want := unchecked.Refused[policytest.ID("PRIV_KEY")], (policy.Refusal{Reason: policy.ReasonProjectUnchecked, Owner: policy.OwnerShared}); got != want {
		t.Errorf("PRIV_KEY on a broker with no K3 client refused as %+v, want %+v", got, want)
	}
	if _, served := unchecked.Secrets["PLAIN_KEY"]; !served {
		t.Error("PLAIN_KEY is not served by a broker with no K3 client; an untagged secret is served as today")
	}
	lookup, err := projectLoader(store).LoadOne(context.Background(), "PRIV_KEY")
	if err != nil || !lookup.Served || lookup.Secret.Project != privProject {
		t.Errorf("LoadOne(PRIV_KEY) = %+v, %v; want it served with Project %s", lookup, err, privProject)
	}
	for raw, want := range map[string]string{"PRIV": "PRIV", "AB": "AB", "A123456789": "A123456789"} {
		if got, err := policy.ParseProject(raw); err != nil || got != want {
			t.Errorf("ParseProject(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "none", "NONE ", "priv", "P", "1PRIV", "PRIV-1", "A1234567890", "PRIV PUB"} {
		if got, err := policy.ParseProject(raw); !errors.Is(err, policy.ErrProjectMalformed) {
			t.Errorf("ParseProject(%q) = %q, %v; want ErrProjectMalformed", raw, got, err)
		}
	}
}
