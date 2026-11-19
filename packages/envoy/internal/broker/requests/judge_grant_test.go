// packages/envoy/internal/broker/requests/judge_grant_test.go
// The life of a grant the model judge decided, after the decision: what keeps releasing it — its
// airlock entry rather than any person — what ends it, who sees it on the Live grants page, and
// who may end it there.
package requests

import (
	"crypto/ecdsa"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/airlock"
	"github.com/sjawhar/envoy/internal/broker/policy"
	"github.com/sjawhar/envoy/internal/broker/policy/policytest"
	"github.com/sjawhar/envoy/internal/broker/record"
	"github.com/sjawhar/envoy/internal/broker/secrets"
)

// judgedGrant is one grant the model judge approved for names on enrollmentID, the state every
// test in this file starts from.
func judgedGrant(t *testing.T, m *Machine, enrollmentID string, key *ecdsa.PrivateKey, lifetime int, names ...string) string {
	t.Helper()
	req := pendingJudgeable(t, m, enrollmentID, key, "INFRA-1353 wants it", lifetime, names...)
	dec, err := m.ApplyJudgeDecision(t.Context(), *req.RecordID, judgeModel, map[string]any{"verdict": "grant", "issue": "ACME-1353"})
	if err != nil || dec.GrantID == "" {
		t.Fatalf("ApplyJudgeDecision(%v) = %+v, %v; want a grant", names, dec, err)
	}
	return dec.GrantID
}

// releasedValue is what one release handed the session for name: the value itself, or, for a
// name the airlock holds back, what the command's variable of that name holds. Empty means the
// release carried nothing for it. A judged name is always an airlock entry's — the rules are
// what let the judge decide it — so the same grant answers through the other half of a release
// once that entry is gone.
func releasedValue(release Release, name string) string {
	if release.Airlock != nil {
		if held, ok := release.Airlock.Names[name]; ok {
			return held.Value
		}
	}
	return release.Values[name]
}

// withJudge answers the rules file an operator rewrote: name's entry giving the judge seconds,
// or, for a nil seconds, an entry with no judge at all. Every other entry is left as it was.
func withJudge(name string, seconds *uint32) func(airlock.Rules) airlock.Rules {
	return func(rules airlock.Rules) airlock.Rules {
		changed := airlock.Rules{Secrets: slices.Clone(rules.Secrets)}
		for i := range changed.Secrets {
			if changed.Secrets[i].Name == name {
				changed.Secrets[i].Judge = seconds
			}
		}
		return changed
	}
}

// withoutEntry answers the rules file with name's entry gone from it altogether.
func withoutEntry(name string) func(airlock.Rules) airlock.Rules {
	return func(rules airlock.Rules) airlock.Rules {
		var changed airlock.Rules
		for _, s := range rules.Secrets {
			if s.Name != name {
				changed.Secrets = append(changed.Secrets, s)
			}
		}
		return changed
	}
}

// asDecided is the rules file the grant was decided under, left alone.
func asDecided(rules airlock.Rules) airlock.Rules { return rules }

// TestAJudgedGrantLivesByItsRulesEntryAndNotByAnOwner: the judge approves an owned human-tier
// secret in its owner's stead, so the grant is decided by no login any owner rule admits. Once
// the policy version moves, every release re-checks what it was granted, and what is asked of
// such a grant is the rules entry its approval rested on: the judge the entry gives this name
// must still cover the lifetime the grant holds. So a policy move that leaves the entry alone
// goes on releasing it, and a file that takes the judge off the name, lowers it below the grant's
// lifetime, or drops the name altogether ends it. The owner's own grant of the same secret is
// decided by a person and is held to the person's rule throughout: none of this moves it.
func TestAJudgedGrantLivesByItsRulesEntryAndNotByAnOwner(t *testing.T) {
	for name, c := range map[string]struct {
		rules func(airlock.Rules) airlock.Rules
		live  bool
	}{
		"the entry is the one the grant was decided under":  {rules: asDecided, live: true},
		"the entry's judge is exactly the grant's lifetime": {rules: withJudge("DEEL_API_KEY", new(uint32(600))), live: true},
		"the entry's judge is below the grant's lifetime":   {rules: withJudge("DEEL_API_KEY", new(uint32(599)))},
		"the entry has no judge at all":                     {rules: withJudge("DEEL_API_KEY", nil)},
		"the rules no longer name the secret":               {rules: withoutEntry("DEEL_API_KEY")},
	} {
		t.Run(name, func(t *testing.T) {
			m, session, key := newJudgeFixture(t)
			judged := judgedGrant(t, m, session, key, 600, "DEEL_API_KEY")

			// Another session of the owner's holds a grant of the same secret that the owner
			// approved themselves, which every assertion below answers for too.
			own, ownKey := newEnrollment(t, m.Store, "session", "box-b-"+t.Name(), new(fixtureOperator), nil)
			pending := pendingJudgeable(t, m, own, ownKey, "INFRA-1353 wants it on the other box", 600, "DEEL_API_KEY")
			handOver(t, m, *pending.RecordID)
			byOwner, err := m.ApplyDecision(t.Context(), *pending.RecordID, true, fixtureOperator)
			if err != nil || byOwner.GrantID == "" {
				t.Fatalf("ApplyDecision by the owner = %+v, %v; want a grant", byOwner, err)
			}

			// A secret neither grant holds is retagged, which moves the policy version and so
			// makes both releases re-check every name they were granted. DEEL_API_KEY's own
			// tags, owner and tier are untouched.
			before := m.Policy.Get().Version
			retag(t, m, policytest.Secret("UNRELATED_KEY", otherPerson, policy.TierAgent, "unrelated-v1"))
			if m.Policy.Get().Version == before {
				t.Fatalf("the policy version stood still at %s, so the re-checks this test is about never ran", before)
			}
			// The rules the release reads. The chain verifier keeps the bound the fixture built
			// it with, so what answers below is the release's own re-check of the entry and not
			// the chain's re-verification of the approval; a pod that booted on the rewritten
			// file refuses such a grant at both.
			m.Airlock = c.rules(m.Airlock)

			release, err := m.Values(t.Context(), judged, session, nil, ReleaseCall{})
			switch {
			case c.live && (err != nil || releasedValue(release, "DEEL_API_KEY") == ""):
				t.Fatalf("Values(the judge's grant) = %+v, %v; want DEEL_API_KEY released", release, err)
			case !c.live && !errors.Is(err, ErrGrantNotLive):
				t.Fatalf("Values(the judge's grant) = %+v, %v; want ErrGrantNotLive", release, err)
			}
			if release, err := m.Values(t.Context(), byOwner.GrantID, own, nil, ReleaseCall{}); err != nil || releasedValue(release, "DEEL_API_KEY") == "" {
				t.Fatalf("Values(the owner's own grant) = %+v, %v; want DEEL_API_KEY released whatever the rules now say of the judge", release, err)
			}
		})
	}
}

// bootedJudgeFixture is newJudgeFixture wired as cmd/broker wires its one rules file: the policy
// loader fingerprints each entry of the rules the broker booted on (policy.Loader.AirlockDigest,
// whose fingerprints are in the policy version), and the chain verifier holds a judge's approval
// to that same file. boot is a pod booting on rules, which the loader, m.Airlock and the chain
// all read from then on.
func bootedJudgeFixture(t *testing.T) (m *Machine, session string, key *ecdsa.PrivateKey, boot func(airlock.Rules)) {
	t.Helper()
	m, session, key = newJudgeFixture(t)
	booted := m.Airlock
	loader := policytest.Loader(fixtureStore(m), fixtureService)
	loader.AirlockDigest = func(name string) string { return booted.Digest(name) }
	current, err := policy.NewCurrent(t.Context(), loader, time.Hour)
	if err != nil {
		t.Fatalf("policy.NewCurrent: %v", err)
	}
	m.Policy = current
	return m, session, key, func(rules airlock.Rules) {
		t.Helper()
		booted, m.Airlock = rules, rules
		m.Chain = NewChainVerifier(m.Store, testAudience, time.Minute, rules)
		if err := m.Policy.Refresh(t.Context()); err != nil {
			t.Fatalf("policy refresh: %v", err)
		}
	}
}

// TestARulesEditUnderAJudgedGrantEndsItForGood: an edit to the rules entry of the key the judge
// decided — its judge taken off, lowered below the grant's lifetime, or the entry dropped — ends
// a judged grant for good, as every edit to an entry ends the grants that entry affects, at the
// release and at the next request that would reuse it. The grant also holds AUTO_TOKEN, which its
// session got at once. The pod boots on the edited file, so its chain, which holds a judge's
// approval to the rules in force, refuses the grant as well, and so does stillAllowed's judge arm;
// either refusal alone would leave the grant live, and booting the decided file again would
// release it. No request reuses the grant whose judge was lowered: a request for its 600 seconds
// is refused when it is written, the judge now granting at most 599.
func TestARulesEditUnderAJudgedGrantEndsItForGood(t *testing.T) {
	names := []string{"DEEL_API_KEY", "AUTO_TOKEN"}
	for _, c := range []struct {
		name  string
		edit  func(airlock.Rules) airlock.Rules
		reuse bool
	}{
		{name: "the entry has no judge at all", edit: withJudge("DEEL_API_KEY", nil)},
		{name: "the entry has no judge at all", edit: withJudge("DEEL_API_KEY", nil), reuse: true},
		{name: "the entry's judge is below the grant's lifetime", edit: withJudge("DEEL_API_KEY", new(uint32(599)))},
		{name: "the rules no longer name the secret", edit: withoutEntry("DEEL_API_KEY")},
		{name: "the rules no longer name the secret", edit: withoutEntry("DEEL_API_KEY"), reuse: true},
	} {
		path := map[bool]string{false: "the release", true: "the next request"}[c.reuse]
		t.Run(c.name+", at "+path, func(t *testing.T) {
			m, session, key, boot := bootedJudgeFixture(t)
			judged := judgedGrant(t, m, session, key, 600, names...)
			decided := m.Airlock
			boot(c.edit(decided))

			if c.reuse {
				again, err := m.Create(t.Context(), session, signJudgeable(t, m, key, "post the release notes again", 600, names...), "", nil)
				if err != nil || again.GrantID != nil || again.State != "pending" {
					t.Fatalf("Create under the edited rules = %+v, %v; want no grant handed back and a new pending request", again, err)
				}
			} else if release, err := m.Values(t.Context(), judged, session, nil, ReleaseCall{}); !errors.Is(err, ErrGrantNotLive) || releasedValue(release, "DEEL_API_KEY") != "" || releasedValue(release, "AUTO_TOKEN") != "" {
				t.Fatalf("Values under the edited rules = %+v, %v; want ErrGrantNotLive and nothing released", release, err)
			}
			if by, audits := endedForGood(t, m, judged); by != GrantEndedActor || audits != 1 {
				t.Fatalf("after %s under the edited rules: revoked_by %q with %d grant.revoked rows by %s; want the grant ended once for good", path, by, audits, GrantEndedActor)
			}

			boot(decided)
			if release, err := m.Values(t.Context(), judged, session, nil, ReleaseCall{}); !errors.Is(err, ErrGrantNotLive) || releasedValue(release, "DEEL_API_KEY") != "" || releasedValue(release, "AUTO_TOKEN") != "" {
				t.Fatalf("Values once the decided rules are booted again = %+v, %v; want the grant still ended", release, err)
			}
		})
	}
}

// deelAsAdmin is the fixture's own DEEL_API_KEY tagged as an administrator's secret: the same
// owner, tier and value, now carrying admin: "true" and, beside it, the two things policy.TagRules
// demands of such a secret — a requesters list naming the person every session below is signed
// in as, and a max-grant, an hour here (policytest.FixtureMaxGrant), which is longer than any
// grant this file makes. Nothing else about the secret moves, so what answers below is the tag
// and not a secret that changed hands or shrank its cap.
func deelAsAdmin() secrets.LocalSecret {
	admin := policytest.ListedSecret("DEEL_API_KEY", fixtureOperator, policy.TierHuman, fixtureOperator, "deel-v1")
	admin.Tags[policy.TagAdmin] = policy.AdminTrue
	return admin
}

// TestAnAdministratorsTagEndsAJudgedGrantAndLeavesAPersonsAlone: a person approves every release
// of a secret its owner tags as an administrator's (policy.TagAdmin). The model judge may have
// decided a grant of it before the tag was written — the rules file gave the name judge seconds,
// and the judge read a secret that was not yet an administrator's — and the tag moves the policy
// version, so every release of that grant and every reuse of it re-reads what it was granted.
// Both refuse it and end the grant for good, rather than let a model's approval go on releasing
// an administrator's credential for the rest of the seconds the rules gave it. Taking the tag
// off again leaves the grant ended: the session asks afresh, as a new request. A grant of the
// same secret that its owner approved releases throughout, a person's approval being all the
// tag asks.
func TestAnAdministratorsTagEndsAJudgedGrantAndLeavesAPersonsAlone(t *testing.T) {
	t.Run("a release refuses it and ends it for good", func(t *testing.T) {
		m, session, key := newJudgeFixture(t)
		judged := judgedGrant(t, m, session, key, 600, "DEEL_API_KEY")

		// Another session of the owner's holds a grant of the same secret that the owner
		// approved themselves, which every assertion below answers for too.
		own, ownKey := newEnrollment(t, m.Store, "session", "box-b-"+t.Name(), new(fixtureOperator), nil)
		pending := pendingJudgeable(t, m, own, ownKey, "INFRA-1353 wants it on the other box", 600, "DEEL_API_KEY")
		handOver(t, m, *pending.RecordID)
		byOwner, err := m.ApplyDecision(t.Context(), *pending.RecordID, true, fixtureOperator)
		if err != nil || byOwner.GrantID == "" {
			t.Fatalf("ApplyDecision by the owner = %+v, %v; want a grant", byOwner, err)
		}

		before := m.Policy.Get().Version
		retag(t, m, deelAsAdmin())
		if m.Policy.Get().Version == before {
			t.Fatalf("the policy version stood still at %s, so the re-checks this test is about never ran", before)
		}

		release, err := m.Values(t.Context(), judged, session, nil, ReleaseCall{})
		if !errors.Is(err, ErrGrantNotLive) || releasedValue(release, "DEEL_API_KEY") != "" {
			t.Fatalf("Values(the judge's grant of what is now an administrator's secret) = %+v, %v; want ErrGrantNotLive and nothing released", release, err)
		}
		if by, audits := endedForGood(t, m, judged); by != GrantEndedActor || audits != 1 {
			t.Fatalf("the judged grant's revoked_by = %q with %d grant.revoked rows by %s; want it ended once for good", by, audits, GrantEndedActor)
		}
		if release, err := m.Values(t.Context(), judged, session, nil, ReleaseCall{}); !errors.Is(err, ErrGrantNotLive) || releasedValue(release, "DEEL_API_KEY") != "" {
			t.Fatalf("a second Values of the judged grant = %+v, %v; want it refused as ended, not merely passed over once", release, err)
		}
		if release, err := m.Values(t.Context(), byOwner.GrantID, own, nil, ReleaseCall{}); err != nil || releasedValue(release, "DEEL_API_KEY") == "" {
			t.Fatalf("Values(the owner's own grant) = %+v, %v; want DEEL_API_KEY released: a person approved it, which is all the tag asks", release, err)
		}

		// The tag taken off again does not bring the judged grant back.
		retag(t, m, policytest.Secret("DEEL_API_KEY", fixtureOperator, policy.TierHuman, "deel-v1"))
		if _, err := m.Values(t.Context(), judged, session, nil, ReleaseCall{}); !errors.Is(err, ErrGrantNotLive) {
			t.Fatalf("Values of the judged grant once the tag is gone = %v; want it still ended", err)
		}
		if by, audits := endedForGood(t, m, judged); by != GrantEndedActor || audits != 1 {
			t.Fatalf("once the tag is gone: revoked_by %q with %d grant.revoked rows; want the one end", by, audits)
		}
	})

	t.Run("the next request ends it rather than handing it back", func(t *testing.T) {
		m, session, key := newJudgeFixture(t)
		judged := judgedGrant(t, m, session, key, 600, "DEEL_API_KEY")
		reused, err := m.Create(t.Context(), session, signJudgeable(t, m, key, "INFRA-1353 wants it again", 600, "DEEL_API_KEY"), "", nil)
		if err != nil || reused.GrantID == nil || *reused.GrantID != judged {
			t.Fatalf("a request for the same name and lifetime while the secret is an ordinary one = %+v, %v; want the judged grant %s handed back", reused, err, judged)
		}

		retag(t, m, deelAsAdmin())
		again, err := m.Create(t.Context(), session, signJudgeable(t, m, key, "INFRA-1353 wants it once more", 600, "DEEL_API_KEY"), "", nil)
		if err != nil || again.GrantID != nil || again.State != "pending" {
			t.Fatalf("Create once the secret is an administrator's = %+v, %v; want no grant handed back and a card its person answers", again, err)
		}
		if by, audits := endedForGood(t, m, judged); by != GrantEndedActor || audits != 1 {
			t.Fatalf("the grant the reuse found = revoked_by %q with %d grant.revoked rows by %s; want the reuse to have ended it once", by, audits, GrantEndedActor)
		}
	})
}

// autoTokenAsAdmin is the fixture's own AUTO_TOKEN, the agent-tier key the operator's session gets
// at once, tagged as an administrator's secret: the same owner and value, now human tier, since
// policy.TagRules refuses an admin secret below it, and carrying the requesters list and the
// max-grant the tag demands beside it, as deelAsAdmin does.
func autoTokenAsAdmin() secrets.LocalSecret {
	admin := policytest.ListedSecret("AUTO_TOKEN", fixtureOperator, policy.TierHuman, fixtureOperator, "auto-v1")
	admin.Tags[policy.TagAdmin] = policy.AdminTrue
	return admin
}

// TestAnAdministratorsTagOnAKeyGrantedAtOnceEndsTheJudgedGrantBesideIt: a judged grant may hold,
// beside the key the judge decided, a key its session got at once, which needed no decision
// (TestTheJudgesGrantBesideAKeyGrantedAtOnceReleasesBoth). When that key's owner tags it as an
// administrator's, the grant holds an administrator's secret that no person approved, exactly as
// when the judged key itself is tagged: the release and the next request that would reuse the
// grant both refuse it and end it for good, and taking the tag off again does not bring it back.
// A grant of the same two keys that the owner approved is not ended: its key granted at once now
// wants an approval nobody gave, so it releases nothing while the tag stands, and it releases
// again once the tag is off.
func TestAnAdministratorsTagOnAKeyGrantedAtOnceEndsTheJudgedGrantBesideIt(t *testing.T) {
	names := []string{"DEEL_API_KEY", "AUTO_TOKEN"}
	released := func(release Release) bool {
		return releasedValue(release, "DEEL_API_KEY") != "" || releasedValue(release, "AUTO_TOKEN") != ""
	}
	untag := func(t *testing.T, m *Machine) {
		t.Helper()
		retag(t, m, policytest.Secret("AUTO_TOKEN", fixtureOperator, policy.TierAgent, "auto-v1"))
	}

	t.Run("a release refuses it and ends it for good", func(t *testing.T) {
		m, session, key := newJudgeFixture(t)
		judged := judgedGrant(t, m, session, key, 600, names...)
		if release, err := m.Values(t.Context(), judged, session, nil, ReleaseCall{}); err != nil || releasedValue(release, "DEEL_API_KEY") == "" || releasedValue(release, "AUTO_TOKEN") == "" {
			t.Fatalf("Values(the judge's grant) before any tag = %+v, %v; want both keys released", release, err)
		}

		// Another session of the owner's holds a grant of the same two keys that the owner
		// approved themselves.
		own, ownKey := newEnrollment(t, m.Store, "session", "box-b-"+t.Name(), new(fixtureOperator), nil)
		pending := pendingJudgeable(t, m, own, ownKey, "post the release notes from the other box", 600, names...)
		handOver(t, m, *pending.RecordID)
		byOwner, err := m.ApplyDecision(t.Context(), *pending.RecordID, true, fixtureOperator)
		if err != nil || byOwner.GrantID == "" {
			t.Fatalf("ApplyDecision by the owner = %+v, %v; want a grant", byOwner, err)
		}

		before := m.Policy.Get().Version
		retag(t, m, autoTokenAsAdmin())
		if m.Policy.Get().Version == before {
			t.Fatalf("the policy version stood still at %s, so the re-checks this test is about never ran", before)
		}

		release, err := m.Values(t.Context(), judged, session, nil, ReleaseCall{})
		if !errors.Is(err, ErrGrantNotLive) || released(release) {
			t.Fatalf("Values(the judge's grant of a key now an administrator's) = %+v, %v; want ErrGrantNotLive and nothing released", release, err)
		}
		if by, audits := endedForGood(t, m, judged); by != GrantEndedActor || audits != 1 {
			t.Fatalf("the judged grant's revoked_by = %q with %d grant.revoked rows by %s; want it ended once for good", by, audits, GrantEndedActor)
		}
		if release, err := m.Values(t.Context(), byOwner.GrantID, own, nil, ReleaseCall{}); !errors.Is(err, ErrGrantNotLive) || released(release) {
			t.Fatalf("Values(the owner's grant) while AUTO_TOKEN is an administrator's = %+v, %v; want ErrGrantNotLive: nobody approved AUTO_TOKEN", release, err)
		}
		if by, audits := endedForGood(t, m, byOwner.GrantID); by != "" || audits != 0 {
			t.Fatalf("the owner's grant: revoked_by %q with %d grant.revoked rows by %s; want it not ended", by, audits, GrantEndedActor)
		}

		// The tag taken off again does not bring the judged grant back, and the owner's grant
		// releases both keys again.
		untag(t, m)
		if release, err := m.Values(t.Context(), judged, session, nil, ReleaseCall{}); !errors.Is(err, ErrGrantNotLive) || released(release) {
			t.Fatalf("Values of the judged grant once the tag is gone = %+v, %v; want it still ended", release, err)
		}
		if by, audits := endedForGood(t, m, judged); by != GrantEndedActor || audits != 1 {
			t.Fatalf("once the tag is gone: revoked_by %q with %d grant.revoked rows; want the one end", by, audits)
		}
		if release, err := m.Values(t.Context(), byOwner.GrantID, own, nil, ReleaseCall{}); err != nil || releasedValue(release, "DEEL_API_KEY") == "" || releasedValue(release, "AUTO_TOKEN") == "" {
			t.Fatalf("Values(the owner's grant) once the tag is gone = %+v, %v; want both keys released", release, err)
		}
	})

	t.Run("the next request ends it rather than handing it back", func(t *testing.T) {
		m, session, key := newJudgeFixture(t)
		judged := judgedGrant(t, m, session, key, 600, names...)
		reused, err := m.Create(t.Context(), session, signJudgeable(t, m, key, "post the release notes again", 600, names...), "", nil)
		if err != nil || reused.GrantID == nil || *reused.GrantID != judged {
			t.Fatalf("a request for the same names and lifetime while AUTO_TOKEN is an ordinary key = %+v, %v; want the judged grant %s handed back", reused, err, judged)
		}

		retag(t, m, autoTokenAsAdmin())
		again, err := m.Create(t.Context(), session, signJudgeable(t, m, key, "post the release notes once more", 600, names...), "", nil)
		if err != nil || again.GrantID != nil || again.State != "pending" {
			t.Fatalf("Create once AUTO_TOKEN is an administrator's = %+v, %v; want no grant handed back and a card its person answers", again, err)
		}
		if by, audits := endedForGood(t, m, judged); by != GrantEndedActor || audits != 1 {
			t.Fatalf("the grant the reuse found = revoked_by %q with %d grant.revoked rows by %s; want the reuse to have ended it once", by, audits, GrantEndedActor)
		}

		untag(t, m)
		if release, err := m.Values(t.Context(), judged, session, nil, ReleaseCall{}); !errors.Is(err, ErrGrantNotLive) || released(release) {
			t.Fatalf("Values of the judged grant once the tag is gone = %+v, %v; want it still ended", release, err)
		}
	})
}

// TestAJudgedGrantOfASharedSecretIsEverySignedInPersonsToSeeAndEnd: the judge approves a shared
// human-tier secret in anyone's stead, so the grant names anyone as its approver and no person at
// all, and it sits on a pod, which acts for no person either. Without this nobody could see what
// the judge had handed a pod, which is the reason every signed-in person already sees a grant a
// pod took without asking. Each of them lists both pods' grants, and either of them ends one; the
// sentinel is no signed-in person and lists nothing. Neither revoke is the session's operator's —
// a pod has none — so neither withholds anything from the pod it ended a grant on.
func TestAJudgedGrantOfASharedSecretIsEverySignedInPersonsToSeeAndEnd(t *testing.T) {
	m, _, _ := newJudgeFixture(t)
	first, firstKey := newServiceEnrollment(t, m.Store, fixtureService, fixtureSubject, "pod-one-"+t.Name())
	second, secondKey := newServiceEnrollment(t, m.Store, fixtureService, fixtureSubject, "pod-two-"+t.Name())
	one := judgedGrant(t, m, first, firstKey, 600, "SHARED_KEY")
	two := judgedGrant(t, m, second, secondKey, 600, "SHARED_KEY")

	for _, viewer := range []string{otherPerson, carol} {
		listed, err := m.GrantsForApprover(t.Context(), viewer)
		if err != nil || len(listed) != 2 {
			t.Fatalf("GrantsForApprover(%s) = %+v, %v; want both pods' judged grants of the shared secret", viewer, listed, err)
		}
		for _, g := range listed {
			if g.Approver != record.AnyoneApprover || g.Granted != policy.Approval || g.RecordID == nil {
				t.Errorf("GrantsForApprover(%s) listed %+v; want it granted by approval, under the %q the judge decided in the stead of, on the record it rests on", viewer, g, record.AnyoneApprover)
			}
		}
	}
	if listed, err := m.GrantsForApprover(t.Context(), record.AnyoneApprover); err != nil || len(listed) != 0 {
		t.Fatalf("GrantsForApprover(%s) = %+v, %v; want nothing: the sentinel is no signed-in person", record.AnyoneApprover, listed, err)
	}

	for _, revoke := range []struct{ grant, login string }{{one, otherPerson}, {two, carol}} {
		if err := m.RevokeByApprover(t.Context(), revoke.grant, revoke.login, record.HumanActor(revoke.login)); err != nil {
			t.Fatalf("RevokeByApprover(%s, %s) = %v; want the grant ended", revoke.grant, revoke.login, err)
		}
	}
	for _, viewer := range []string{otherPerson, carol} {
		if listed, err := m.GrantsForApprover(t.Context(), viewer); err != nil || len(listed) != 0 {
			t.Fatalf("GrantsForApprover(%s) after both revokes = %+v, %v; want nothing left", viewer, listed, err)
		}
	}
	var withheld int
	if err := m.Store.Pool.QueryRow(t.Context(), `select count(*) from withheld_secrets where enrollment_id in ($1,$2)`, first, second).Scan(&withheld); err != nil {
		t.Fatalf("read withheld_secrets: %v", err)
	}
	if withheld != 0 {
		t.Errorf("withheld_secrets rows = %d; want none: whoever ends such a grant stands in for its approver, not for the session", withheld)
	}
}

// TestAJudgedGrantOfAnOwnedSecretIsItsOwnersAlone: the judge approves an owned secret in its
// owner's stead, so that grant records the owner as its approver and is the owner's to see and to
// end, exactly as one the owner approved. It is on no other person's page, and no other person
// ends it, though it too sits on a pod that acts for nobody.
func TestAJudgedGrantOfAnOwnedSecretIsItsOwnersAlone(t *testing.T) {
	m, _, _ := newJudgeFixture(t)
	pod, podKey := newServiceEnrollment(t, m.Store, fixtureService, fixtureSubject, "pod-"+t.Name())
	grantID := judgedGrant(t, m, pod, podKey, 600, "DEEL_API_KEY")

	listed, err := m.GrantsForApprover(t.Context(), fixtureOperator)
	if err != nil || len(listed) != 1 || listed[0].GrantID != grantID || listed[0].Approver != fixtureOperator {
		t.Fatalf("GrantsForApprover(%s) = %+v, %v; want the one grant the judge approved in their stead", fixtureOperator, listed, err)
	}
	for _, viewer := range []string{otherPerson, carol} {
		if listed, err := m.GrantsForApprover(t.Context(), viewer); err != nil || len(listed) != 0 {
			t.Fatalf("GrantsForApprover(%s) = %+v, %v; want nothing: the grant is the owner's", viewer, listed, err)
		}
		if err := m.RevokeByApprover(t.Context(), grantID, viewer, record.HumanActor(viewer)); !errors.Is(err, ErrNotApprover) {
			t.Fatalf("RevokeByApprover(%s) = %v; want ErrNotApprover", viewer, err)
		}
	}
	if err := m.RevokeByApprover(t.Context(), grantID, fixtureOperator, record.HumanActor(fixtureOperator)); err != nil {
		t.Fatalf("RevokeByApprover(%s) = %v; want the owner's own revoke to end it", fixtureOperator, err)
	}
	if listed, err := m.GrantsForApprover(t.Context(), fixtureOperator); err != nil || len(listed) != 0 {
		t.Fatalf("GrantsForApprover(%s) after the revoke = %+v, %v; want nothing left", fixtureOperator, listed, err)
	}
}
