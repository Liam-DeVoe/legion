package policy_test

import (
	"context"
	"testing"

	"github.com/sjawhar/envoy/internal/broker/policy"
	"github.com/sjawhar/envoy/internal/broker/policy/policytest"
	"github.com/sjawhar/envoy/internal/broker/secrets"
)

// channelsSecret is a namespace secret carrying the project-channels tag, owned by owner at tier.
func channelsSecret(name, owner, tier, value string) secrets.LocalSecret {
	s := policytest.Secret(name, owner, tier, "v")
	s.Tags[policy.TagProjectChannels] = value
	return s
}

// TestProjectChannelsTagIsLoaded: the tag's one value is slack, read into Secret.ProjectChannels;
// any other value, the empty tag and the word none included, refuses the secret
// project-channels-malformed, since a value the broker cannot read names no limit it could apply.
func TestProjectChannelsTagIsLoaded(t *testing.T) {
	store := secrets.NewLocal(
		channelsSecret("SLACK_TOKEN", policy.OwnerShared, policy.TierAgent, policy.ProjectChannelsSlack),
		channelsSecret("UPPER_TOKEN", policy.OwnerShared, policy.TierAgent, "SLACK"),
		channelsSecret("NONE_TOKEN", policy.OwnerShared, policy.TierAgent, "none"),
		channelsSecret("EMPTY_TOKEN", policy.OwnerShared, policy.TierAgent, ""),
		channelsSecret("OTHER_TOKEN", policy.OwnerShared, policy.TierAgent, "discord"),
		policytest.Secret("PLAIN_TOKEN", policy.OwnerShared, policy.TierAgent, "v"),
	)
	set := loadProjects(t, store)
	if got := set.Secrets["SLACK_TOKEN"].ProjectChannels; got != policy.ProjectChannelsSlack {
		t.Errorf("SLACK_TOKEN's ProjectChannels = %q, want %q", got, policy.ProjectChannelsSlack)
	}
	if got := set.Secrets["PLAIN_TOKEN"].ProjectChannels; got != "" {
		t.Errorf("PLAIN_TOKEN's ProjectChannels = %q, want none", got)
	}
	for _, name := range []string{"UPPER_TOKEN", "NONE_TOKEN", "EMPTY_TOKEN", "OTHER_TOKEN"} {
		want := policy.Refusal{Reason: policy.ReasonProjectChannelsMalformed, Owner: policy.OwnerShared}
		if got := set.Refused[policytest.ID(name)]; got != want {
			t.Errorf("%s refused as %+v, want %+v", name, got, want)
		}
	}
}

// TestAProjectChannelsSecretOutsideTheAirlockIsDeniedToAPrivateProject: such a secret with no
// airlock entry is denied, project-channels-need-airlock, to a credential that names a private
// project, because a release outside the airlock hands the session the real token and the session
// could then post anywhere. The same secret behind the airlock, and the same secret asked for by a
// credential that names only a public project, follow owner and tier as before.
func TestAProjectChannelsSecretOutsideTheAirlockIsDeniedToAPrivateProject(t *testing.T) {
	store := secrets.NewLocal(channelsSecret("SLACK_TOKEN", policy.OwnerShared, policy.TierAgent, policy.ProjectChannelsSlack))
	loader := projectLoader(store)
	bare, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	loader.AirlockDigest = func(string) string { return "entry-one" }
	behind, err := loader.Load(context.Background())
	if err != nil {
		t.Fatalf("Load with an airlock entry: %v", err)
	}
	source := secrets.LocalARN(policytest.ID("SLACK_TOKEN"))
	for _, c := range []struct {
		name      string
		set       *policy.Set
		requester policy.Requester
		want      policy.Decision
	}{
		{"outside the airlock, a credential naming the private project", bare, credential(carol, "", privProject),
			policy.Decision{Outcome: policy.Deny, Reason: policy.ReasonProjectChannelsNeedAirlock, Source: source}},
		{"outside the airlock, a credential naming the public project", bare, credential(carol, "", pubProject),
			policy.Decision{Outcome: policy.Automatic, Source: source}},
		{"outside the airlock, a credential naming no project", bare, credential(carol, ""),
			policy.Decision{Outcome: policy.Automatic, Source: source}},
		{"behind the airlock, a credential naming the private project", behind, credential(carol, "", privProject),
			policy.Decision{Outcome: policy.Automatic, Source: source}},
	} {
		got, err := c.set.Evaluate("SLACK_TOKEN", c.requester)
		if err != nil || got != c.want {
			t.Errorf("Evaluate(SLACK_TOKEN) %s = %+v, %v; want %+v", c.name, got, err, c.want)
		}
	}
}

// TestListsPrivateReadsTheCredentialsOwnProjects: a requester is limited by the projects its
// credential names, read before the visibility filter, and not by the projects of the secret it
// asks for. So a session naming a private project is limited while asking for a secret carrying no
// dispatch-project tag at all, and stays limited once its person can no longer see that project,
// which is exactly when the limit must not fall away.
func TestListsPrivateReadsTheCredentialsOwnProjects(t *testing.T) {
	for _, c := range []struct {
		name      string
		requester policy.Requester
		want      bool
	}{
		{"names the private project, and may see it", policy.Requester{Named: []string{privProject}, Projects: []string{privProject}, ProjectStates: states}, true},
		{"names the private project, and may no longer see it", policy.Requester{Named: []string{privProject}, ProjectStates: states}, true},
		{"names the public project alone", policy.Requester{Named: []string{pubProject}, Projects: []string{pubProject}, ProjectStates: states}, false},
		{"names a project Dispatch does not hold", policy.Requester{Named: []string{goneProject}, ProjectStates: states}, false},
		{"names no project", policy.Requester{ProjectStates: states}, false},
	} {
		if got := c.requester.ListsPrivate(); got != c.want {
			t.Errorf("a requester that %s: ListsPrivate() = %v, want %v", c.name, got, c.want)
		}
	}
}
