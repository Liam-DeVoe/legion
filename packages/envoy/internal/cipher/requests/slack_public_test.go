// packages/envoy/internal/cipher/requests/slack_public_test.go
// The release half of the public Slack fence: a project-channels secret released to a credential
// that names no private project carries Excluded, every channel a private project's owner named,
// read from Dispatch's list of private projects once this broker knows a project is private, and
// nothing at all, with no call to Dispatch, while it knows none. Outside the airlock, where no
// fence can hold, such a secret goes to no credential while a private project names a channel.
package requests

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/policy"
	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
	"github.com/sjawhar/envoy/internal/cipher/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/cipher/proxyrules"
)

// TestAProjectChannelsSecretLeavesTheAirlockToNoCredentialWhilePrivateChannelsAreNamed: the Slack
// token with no airlock entry, asked for by a session that names no project, is denied
// project-channels-need-airlock while a private project's owner names a channel, since released
// outside the airlock it could read that channel; it is granted as before while no private
// project names one; a fence the broker cannot read decides nothing (503); a fence answer aged
// past projectaccess.CacheTTL is not decided on but asked again; and a grant made before a private
// project named a channel is not released after it.
func TestAProjectChannelsSecretLeavesTheAirlockToNoCredentialWhilePrivateChannelsAreNamed(t *testing.T) {
	if (proxyrules.Rules{}).Digest(slackToken) != "" {
		t.Fatalf("empty rules give %s an airlock entry; this test needs it to have none", slackToken)
	}
	noChannels := func() map[string]projectaccesstest.Row {
		rows := fenceRows()
		for _, key := range []string{privProject, synthProject} {
			row := rows[key]
			row.SlackChannels = nil
			rows[key] = row
		}
		return rows
	}

	t.Run("denied while a private project names a channel", func(t *testing.T) {
		b := newOutsideBroker(t, fenceRows())
		request, err := b.create(t)
		deniedFor(t, "a session naming no project asking for "+slackToken+" outside the airlock", request, err, slackToken, policy.ReasonProjectChannelsNeedAirlock)
	})

	t.Run("granted while no private project names a channel", func(t *testing.T) {
		b := newOutsideBroker(t, noChannels())
		if request, err := b.create(t); err != nil || request.GrantID == nil {
			t.Fatalf("%s outside the airlock with no private channel named = %+v, %v; want granted at once, as before", slackToken, request, err)
		}
	})

	t.Run("undecided while the fence cannot be read", func(t *testing.T) {
		b := newOutsideBroker(t, fenceRows())
		b.dispatch.SetPrivateProjectsDown(true)
		if request, err := b.create(t); !errors.Is(err, ErrProjectAccessUnavailable) {
			t.Fatalf("Create(%s) while the private projects' list fails = %+v, %v; want ErrProjectAccessUnavailable", slackToken, request, err)
		}
	})

	t.Run("a fence answer aged past the bound is asked again", func(t *testing.T) {
		b := newOutsideBroker(t, fenceRows())
		// The first list is one kept since before PRIV named a channel, and aged past the bound
		// by the time the decision rests on it; Dispatch, asked again, names the channel.
		lister := &agedFence{answers: []agedList{
			{asked: time.Now().Add(-projectaccess.CacheTTL - time.Second), projects: []projectaccess.PrivateProject{{Key: privProject, SlackChannels: []string{}, SlackChannelsPresent: true}}},
			{asked: time.Now(), projects: []projectaccess.PrivateProject{{Key: privProject, SlackChannels: []string{privChannel}, SlackChannelsPresent: true}}},
		}}
		b.m.PrivateList = lister
		request, err := b.create(t)
		deniedFor(t, "a session naming no project asking for "+slackToken+" outside the airlock, on a fence answer aged past the bound", request, err, slackToken, policy.ReasonProjectChannelsNeedAirlock)
		if lister.calls != 2 {
			t.Fatalf("the fence was read %d time(s); want 2: the aged answer, then Dispatch's own", lister.calls)
		}
	})

	t.Run("a grant made before a private project named a channel is not released after it", func(t *testing.T) {
		b := newOutsideBroker(t, noChannels())
		request, err := b.create(t)
		if err != nil || request.GrantID == nil {
			t.Fatalf("%s outside the airlock with no private channel named = %+v, %v; want granted", slackToken, request, err)
		}
		b.serve(t, fenceRows())
		if release, err := b.m.Values(t.Context(), *request.GrantID, b.enr, nil, ReleaseCall{}); !errors.Is(err, ErrGrantNotLive) {
			t.Fatalf("Values(%s) once PRIV names a channel = %+v, %v; want ErrGrantNotLive", slackToken, release, err)
		}
	})
}

// agedList is one answer agedFence gives: the list, and when its question was asked.
type agedList struct {
	asked    time.Time
	projects []projectaccess.PrivateProject
}

// agedFence is a PrivateLister that answers its lists in turn, the last again for every call
// after, each with its own time, as a *projectaccess.Client answers a list it kept: how a row
// gives the fence an answer that aged before the decision rests on it.
type agedFence struct {
	answers []agedList
	calls   int
}

func (f *agedFence) PrivateProjectsAsOf(context.Context) ([]projectaccess.PrivateProject, time.Time, error) {
	answer := f.answers[min(f.calls, len(f.answers)-1)]
	f.calls++
	return answer.projects, answer.asked, nil
}

func (f *agedFence) PrivateProjects(ctx context.Context) ([]projectaccess.PrivateProject, error) {
	projects, _, err := f.PrivateProjectsAsOf(ctx)
	return projects, err
}

func (f *agedFence) PrivateProjectsFresh(ctx context.Context) ([]projectaccess.PrivateProject, error) {
	return f.PrivateProjects(ctx)
}

func (f *agedFence) SawPrivate() bool { return true }

// outsideBroker is a broker whose rules give the Slack token no airlock entry, whose settings
// declare PRIV and SYNTH private, and whose session names no project.
type outsideBroker struct {
	m        *Machine
	enr      string
	key      *ecdsa.PrivateKey
	dispatch *projectaccesstest.Server
}

func newOutsideBroker(t *testing.T, rows map[string]projectaccesstest.Row) *outsideBroker {
	t.Helper()
	m, enr, key, _ := newProjectFixture(t, proxyrules.Rules{}, publicTeam)
	m.PrivateDeclared = func() []string { return []string{privProject, synthProject} }
	b := &outsideBroker{m: m, enr: enr, key: key}
	b.serve(t, rows)
	return b
}

// serve points the broker at a fresh client of a stand-in serving rows, as a broker reads Dispatch
// once its client's cache has let the last answer go.
func (b *outsideBroker) serve(t *testing.T, rows map[string]projectaccesstest.Row) {
	t.Helper()
	b.dispatch = projectaccesstest.NewServer(t, rows, nil)
	client := projectaccess.New(b.dispatch.URL, b.dispatch.Account, nil)
	b.m.Projects, b.m.PrivateList = client, client
}

// create is the session's request for the token.
func (b *outsideBroker) create(t *testing.T) (Request, error) {
	t.Helper()
	return b.m.Create(t.Context(), b.enr, signRequest(t, b.m, b.key, "post the release notes", slackToken), "", nil)
}

// The channels of the public fence's rows: PRIV's and SYNTH's own, one both named, and one a
// public project named, which no fence holds.
const (
	synthProject  = "SYNTH"
	privChannel   = "C0PRIV00001"
	synthChannel  = "C0SYNTH0001"
	sharedChannel = "C0SHARED001"
	teamChannel   = "C0TEAM00001"
)

// fenceRows is Dispatch holding PRIV and SYNTH private, each naming its own channel and the
// shared one, and TEAM public, naming one of its own.
func fenceRows() map[string]projectaccesstest.Row {
	return map[string]projectaccesstest.Row{
		privProject:  {Visibility: "private", Owner: otherPerson, Listed: []string{insider}, SlackChannels: []string{sharedChannel, privChannel}},
		synthProject: {Visibility: "private", Owner: otherPerson, Listed: []string{insider}, SlackChannels: []string{synthChannel, sharedChannel}},
		teamProject:  {Visibility: "public", Owner: otherPerson, SlackChannels: []string{teamChannel}},
	}
}

// publicRelease is one release of the Slack token to the fixture's session, which names named, as
// the proxy reads it: on a broker whose client of Dispatch is new, as a broker's is after a
// restart, asking a stand-in that holds rows, and whose settings declare declared private. It
// answers the token's value, the stand-in, and the time before the read.
func publicRelease(t *testing.T, rows map[string]projectaccesstest.Row, declared []string, named ...string) (AirlockSecret, *projectaccesstest.Server, time.Time) {
	t.Helper()
	rules := proxyrules.Rules{Secrets: []proxyrules.SecretRule{rule(slackToken, "")}}
	m, enr, key, _ := newProjectFixture(t, rules, publicTeam)
	nameProjects(t, m, enr, named...)
	dispatch := projectaccesstest.NewServer(t, rows, nil)
	client := projectaccess.New(dispatch.URL, dispatch.Account, nil)
	m.Projects, m.PrivateList = client, client
	if declared != nil {
		m.PrivateDeclared = func() []string { return declared }
	}
	_, grantID := grantOf(t, m, enr, key, "post the release notes", slackToken)
	read := time.Now()
	values, _, err := m.AirlockValues(t.Context(), grantID)
	if err != nil {
		t.Fatalf("AirlockValues(%s): %v", slackToken, err)
	}
	return values[slackToken], dispatch, read
}

// TestAPublicSlackGrantCarriesEveryPrivateChannel: the Slack token released to a credential that
// names no private project carries Excluded, the sorted union of every private project's channels
// in Dispatch's list, bounded to airlockProjectRecheck; after a restart the first release reads
// that list from the broker's declared private projects alone, with no answer of Dispatch's before
// it; with no private project known, it carries nothing and Dispatch is asked nothing; a list it
// cannot read is ExcludedUnread, read again within a minute; and a credential that names a private
// project is Limited instead.
func TestAPublicSlackGrantCarriesEveryPrivateChannel(t *testing.T) {
	union := []string{privChannel, sharedChannel, synthChannel}
	fenced := func(t *testing.T, what string, got AirlockSecret, read time.Time) {
		t.Helper()
		if got.Limited || got.Channels != nil || got.ExcludedUnread || !slices.Equal(got.Excluded, union) {
			t.Fatalf("%s: %s = Limited %v Channels %#v Excluded %#v ExcludedUnread %v; want unlimited and Excluded %#v, the union of PRIV's and SYNTH's channels", what, slackToken, got.Limited, got.Channels, got.Excluded, got.ExcludedUnread, union)
		}
		if got.Expires.Before(read.Add(airlockProjectRecheck)) || got.Expires.After(time.Now().Add(airlockProjectRecheck)) {
			t.Fatalf("%s: %s expires %v; want the read time plus %v", what, slackToken, got.Expires, airlockProjectRecheck)
		}
	}

	t.Run("after a restart, the declared projects seed the fence from the list with no earlier trade", func(t *testing.T) {
		got, dispatch, read := publicRelease(t, fenceRows(), []string{privProject, synthProject})
		fenced(t, "a session naming no project", got, read)
		if calls, list := dispatch.Calls(), dispatch.PrivateProjectsCalls(); calls != 1 || list != 1 {
			t.Fatalf("the release asked Dispatch %d time(s), %d of them for the list; want the list once and nothing else", calls, list)
		}
	})

	t.Run("a credential naming a public project is fenced the same", func(t *testing.T) {
		got, _, read := publicRelease(t, fenceRows(), []string{privProject, synthProject}, teamProject)
		fenced(t, "a session naming TEAM", got, read)
	})

	t.Run("an answer of Dispatch's that called a project private turns the fence on", func(t *testing.T) {
		rules := proxyrules.Rules{Secrets: []proxyrules.SecretRule{rule(slackToken, "")}}
		m, enr, key, _ := newProjectFixture(t, rules, publicTeam)
		dispatch := projectaccesstest.NewServer(t, fenceRows(), nil)
		client := projectaccess.New(dispatch.URL, dispatch.Account, nil)
		m.Projects, m.PrivateList = client, client
		if _, err := client.Access(t.Context(), privProject, insider); err != nil || !client.SawPrivate() {
			t.Fatalf("Access(%s) = %v, SawPrivate %v; want a private answer seen", privProject, err, client.SawPrivate())
		}
		_, grantID := grantOf(t, m, enr, key, "post the release notes", slackToken)
		read := time.Now()
		values, _, err := m.AirlockValues(t.Context(), grantID)
		if err != nil {
			t.Fatalf("AirlockValues(%s): %v", slackToken, err)
		}
		fenced(t, "a session naming no project, on a broker that has seen PRIV private", values[slackToken], read)
	})

	t.Run("with no private project known, nothing is fenced and Dispatch is asked nothing", func(t *testing.T) {
		got, dispatch, read := publicRelease(t, fenceRows(), nil)
		if got.Limited || got.Channels != nil || got.Excluded != nil || got.ExcludedUnread {
			t.Fatalf("%s = Limited %v Channels %#v Excluded %#v ExcludedUnread %v; want unlimited and unfenced, as before any project is private", slackToken, got.Limited, got.Channels, got.Excluded, got.ExcludedUnread)
		}
		if got.Expires.Before(read.Add(airlockProjectRecheck)) || got.Expires.After(time.Now().Add(airlockProjectRecheck)) {
			t.Fatalf("%s expires %v; want the read time plus %v, so the value learns of the first private project", slackToken, got.Expires, airlockProjectRecheck)
		}
		if calls := dispatch.Calls(); calls != 0 {
			t.Fatalf("a release with no private project known asked Dispatch %d time(s); want none", calls)
		}
	})

	for _, c := range []struct {
		name string
		fail func(*projectaccesstest.Server)
		rows map[string]projectaccesstest.Row
	}{
		{"the list fails", func(s *projectaccesstest.Server) { s.SetPrivateProjectsDown(true) }, fenceRows()},
		{"a private project's entry carries no slack_channels", nil, func() map[string]projectaccesstest.Row {
			rows := fenceRows()
			synth := rows[synthProject]
			synth.OmitSlackChannels = true
			rows[synthProject] = synth
			return rows
		}()},
	} {
		t.Run("unread: "+c.name, func(t *testing.T) {
			rules := proxyrules.Rules{Secrets: []proxyrules.SecretRule{rule(slackToken, "")}}
			m, enr, key, _ := newProjectFixture(t, rules, publicTeam)
			dispatch := projectaccesstest.NewServer(t, c.rows, nil)
			if c.fail != nil {
				c.fail(dispatch)
			}
			client := projectaccess.New(dispatch.URL, dispatch.Account, nil)
			m.Projects, m.PrivateList = client, client
			m.PrivateDeclared = func() []string { return []string{privProject, synthProject} }
			_, grantID := grantOf(t, m, enr, key, "post the release notes", slackToken)
			read := time.Now()
			values, _, err := m.AirlockValues(t.Context(), grantID)
			if err != nil {
				t.Fatalf("AirlockValues(%s): %v", slackToken, err)
			}
			got := values[slackToken]
			if !got.ExcludedUnread || got.Excluded != nil || got.Limited {
				t.Fatalf("%s when %s = ExcludedUnread %v Excluded %#v Limited %v; want ExcludedUnread, which the proxy refuses every call on", slackToken, c.name, got.ExcludedUnread, got.Excluded, got.Limited)
			}
			// The proxy drops a value AirlockMintAgainBefore before its expiry, so it reads an
			// unread fence again a minute after this read.
			if want := AirlockMintAgainBefore + time.Minute; got.Expires.Before(read.Add(want)) || got.Expires.After(time.Now().Add(want)) {
				t.Fatalf("%s when %s expires %v; want the read time plus %v", slackToken, c.name, got.Expires, want)
			}
		})
	}

	t.Run("a credential naming a private project is Limited, not fenced", func(t *testing.T) {
		got, _, _ := publicRelease(t, fenceRows(), []string{privProject, synthProject}, privProject)
		if !got.Limited || got.Excluded != nil || got.ExcludedUnread {
			t.Fatalf("%s for a session naming PRIV = Limited %v Excluded %#v ExcludedUnread %v; want Limited, with no fence of its own", slackToken, got.Limited, got.Excluded, got.ExcludedUnread)
		}
	})
}
