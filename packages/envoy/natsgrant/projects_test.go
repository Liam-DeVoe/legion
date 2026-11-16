package natsgrant_test

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/bus"
	"github.com/sjawhar/envoy/internal/contracts"
	"github.com/sjawhar/envoy/natsgrant"
)

// pubScope is one daemon's scope: project PUB, two repositories (one whose name holds a dot, which
// a subject spells as an underscore), and the account that names its reply prefix.
var pubScope = natsgrant.Scope{
	Projects:     []string{"PUB"},
	Repositories: []string{"acme/widgets", "acme/gadgets.js"},
	Identity:     "legion-daemon-PUB",
}

// A reply prefix is "_INBOX_" and the identity with every byte outside [A-Za-z0-9-] escaped
// as _hh, including underscores, so it is one subject token and distinct identities stay distinct.
func TestInboxPrefixIsOneTokenOfTheIdentity(t *testing.T) {
	for identity, want := range map[string]string{
		"legion-daemon-PUB": "_INBOX_legion-daemon-PUB",
		"system:serviceaccount:legion-pub:legion-pub-daemon": "_INBOX_system_3aserviceaccount_3alegion-pub_3alegion-pub-daemon",
		"daemon@example.iam.gserviceaccount.com":             "_INBOX_daemon_40example_2eiam_2egserviceaccount_2ecom",
		"a.b*c>d e\tf":                                       "_INBOX_a_2eb_2ac_3ed_20e_09f",
		"é":                                                  "_INBOX__c3_a9",
		"\x00\r\n*>.":                                        "_INBOX__00_0d_0a_2a_3e_2e",
		"a.b":                                                "_INBOX_a_2eb",
		"a_b":                                                "_INBOX_a_5fb",
		"daemon@example.com":                                 "_INBOX_daemon_40example_2ecom",
		"daemon_example_com":                                 "_INBOX_daemon_5fexample_5fcom",
		"a.b@x.com":                                          "_INBOX_a_2eb_40x_2ecom",
		"a_b@x.com":                                          "_INBOX_a_5fb_40x_2ecom",
		"daemon-1":                                           "_INBOX_daemon-1",
		"daemon-2":                                           "_INBOX_daemon-2",
		"Az09_-":                                             "_INBOX_Az09_5f-",
	} {
		got := natsgrant.InboxPrefix(identity)
		require.Equal(t, want, got, "InboxPrefix(%q)", identity)
		require.True(t, natsgrant.SessionToken(got), "InboxPrefix(%q) = %q is one subject token", identity, got)
		options := natsgo.GetDefaultOptions()
		require.NoError(t, natsgo.CustomInboxPrefix(got)(&options), "nats.go accepts InboxPrefix(%q)", identity)
		grant, err := natsgrant.WithinProjects(natsgrant.Daemon(), natsgrant.Scope{Projects: []string{"PUB"}, Identity: identity})
		require.NoError(t, err)
		require.Contains(t, grant.Subscribe, want+".>", "the grant names the prefix the client uses")
	}
	// The control is the old byte-replacement rule, not the implementation under test: each
	// pair must be one that the old rule merged before we trust its new prefixes differ.
	oldPrefix := func(identity string) string {
		prefix := []byte("_INBOX_")
		for i := range len(identity) {
			c := identity[i]
			if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '_' || c == '-' {
				prefix = append(prefix, c)
			} else {
				prefix = append(prefix, '_')
			}
		}
		return string(prefix)
	}
	for _, pair := range [][2]string{{"a.b@x.com", "a_b@x.com"}, {"a.b", "a_b"}, {"daemon@example.com", "daemon_example_com"}} {
		require.Equal(t, oldPrefix(pair[0]), oldPrefix(pair[1]), "the old rule must merge the collision control")
		require.NotEqual(t, natsgrant.InboxPrefix(pair[0]), natsgrant.InboxPrefix(pair[1]),
			"distinct accounts %q and %q must not share replies", pair[0], pair[1])
	}
}

// The daemon's grant held to PUB: its reply prefix in place of _INBOX.>, the role-lane exceptions
// of PUB alone, and for each consumer the daemon opens under PUB (t-c-10's names, each create
// subject naming its one filter) the create, info, pull and acknowledgement of that consumer and
// no other. The stream's info and stored-message get are gone. Written out in full, so the test
// reads the subjects, not the code that builds them.
func TestWithinProjectsHoldsTheDaemonGrantToItsProjects(t *testing.T) {
	got, err := natsgrant.WithinProjects(natsgrant.Daemon(), pubScope)
	require.NoError(t, err)

	const (
		api  = "$JS.API.CONSUMER."
		ack  = "$JS.ACK.ENVOY_NOTIFICATIONS."
		pre  = "ENVOY_NOTIFICATIONS.legion-go-PUB-"
		gads = "notifications.github.acme.gadgets_js.>"
		wids = "notifications.github.acme.widgets.>"
	)
	const (
		gadgets = "legion-go-PUB-github-acme_2fgadgets_2ejs"
		widgets = "legion-go-PUB-github-acme_2fwidgets"
	)
	require.Equal(t, gadgets, natsgrant.GitHubConsumerName("PUB", "acme/gadgets.js"))
	require.Equal(t, widgets, natsgrant.GitHubConsumerName("PUB", "acme/widgets"))
	// The filter each consumer's create names is the one Envoy's listener publishes a repository's
	// events under (contracts.GithubSubject), a dot in the name written "_".
	require.Equal(t, gads, natsgrant.GitHubFilter("acme/gadgets.js"))
	require.Equal(t, wids, natsgrant.GitHubFilter("acme/widgets"))
	require.True(t, strings.HasPrefix(contracts.GithubSubject("acme", "gadgets.js", "pr.1"), strings.TrimSuffix(gads, ">")),
		"a published event of acme/gadgets.js falls under its filter")
	consumers := []string{"dispatch", "mapping", "wake", "ready", "github-acme_2fgadgets_2ejs", "github-acme_2fwidgets"}
	var info, next, acks []string
	for _, consumer := range consumers {
		info = append(info, api+"INFO."+pre+consumer)
		next = append(next, api+"MSG.NEXT."+pre+consumer)
		acks = append(acks, ack+"legion-go-PUB-"+consumer+".>")
	}
	creates := []string{
		api + "CREATE." + pre + "dispatch.notifications.dispatch.issue.PUB.>",
		api + "CREATE." + pre + "mapping.notifications.dispatch.project.PUB.settings.repo_project.updated",
		api + "CREATE." + pre + "wake.notifications.legion.pub.wake",
		api + "CREATE." + pre + "ready.notifications.legion.wake",
		api + "CREATE." + pre + "github-acme_2fgadgets_2ejs." + gads,
		api + "CREATE." + pre + "github-acme_2fwidgets." + wids,
	}
	require.Equal(t, natsgrant.Permissions{
		Publish: slices.Concat(info, creates, next, acks),
		Subscribe: []string{
			"notifications.envoy.exceptions.notifications.role.PUB.>",
			"_INBOX_legion-daemon-PUB.>",
		},
	}, got)
}

// Each kind of entry beside the reply inboxes: rewritten to the scope's subjects, dropped, or
// refused naming the entry. A refusal is the whole answer, never a grant with the entry left out.
// The reply inboxes keep each direction from coming out empty, which is refused on its own
// (TestWithinProjectsRefusesAGrantHeldToNothingInADirection).
func TestWithinProjectsRewritesOrRefusesEachEntryKind(t *testing.T) {
	scope := natsgrant.Scope{Projects: []string{"PUB"}, Repositories: []string{"acme/widgets"}, Identity: "id"}
	pubConsumers := []string{"dispatch", "mapping", "wake", "ready", "github-acme_2fwidgets"}
	each := func(format string) []string {
		out := make([]string, len(pubConsumers))
		for i, consumer := range pubConsumers {
			out[i] = fmt.Sprintf(format, "legion-go-PUB-"+consumer)
		}
		return out
	}
	creates := []string{
		"$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.legion-go-PUB-dispatch.notifications.dispatch.issue.PUB.>",
		"$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.legion-go-PUB-mapping.notifications.dispatch.project.PUB.settings.repo_project.updated",
		"$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.legion-go-PUB-wake.notifications.legion.pub.wake",
		"$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.legion-go-PUB-ready.notifications.legion.wake",
		"$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.legion-go-PUB-github-acme_2fwidgets.notifications.github.acme.widgets.>",
	}
	for _, row := range []struct {
		entry   string
		want    []string // nil with refused empty: dropped
		refused string   // a word the refusal holds beside the entry
	}{
		{entry: "_INBOX.>", want: []string{"_INBOX_id.>"}},
		{entry: "$JS.API.STREAM.INFO.ENVOY_NOTIFICATIONS"},
		{entry: "$JS.API.STREAM.MSG.GET.ENVOY_NOTIFICATIONS"},
		{entry: "$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.>", want: creates},
		{entry: "$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.*.>", want: creates},
		{entry: "$JS.API.CONSUMER.INFO.ENVOY_NOTIFICATIONS.>", want: each("$JS.API.CONSUMER.INFO.ENVOY_NOTIFICATIONS.%s")},
		{entry: "$JS.API.CONSUMER.INFO.ENVOY_NOTIFICATIONS.*", want: each("$JS.API.CONSUMER.INFO.ENVOY_NOTIFICATIONS.%s")},
		{entry: "$JS.API.CONSUMER.MSG.NEXT.ENVOY_NOTIFICATIONS.>", want: each("$JS.API.CONSUMER.MSG.NEXT.ENVOY_NOTIFICATIONS.%s")},
		{entry: "$JS.API.CONSUMER.MSG.NEXT.ENVOY_NOTIFICATIONS.*", want: each("$JS.API.CONSUMER.MSG.NEXT.ENVOY_NOTIFICATIONS.%s")},
		{entry: "$JS.ACK.ENVOY_NOTIFICATIONS.>", want: each("$JS.ACK.ENVOY_NOTIFICATIONS.%s.>")},
		{entry: "$JS.ACK.ENVOY_NOTIFICATIONS.*.>", want: each("$JS.ACK.ENVOY_NOTIFICATIONS.%s.>")},
		{entry: "notifications.dispatch.issue.>", want: []string{"notifications.dispatch.issue.PUB.>"}},
		{entry: "notifications.dispatch.issue.> example-queue", want: []string{"notifications.dispatch.issue.PUB.> example-queue"}},
		{entry: "notifications.envoy.exceptions.notifications.role.>", want: []string{"notifications.envoy.exceptions.notifications.role.PUB.>"}},
		{entry: "notifications.legion.>", want: []string{"notifications.legion.pub.>"}},
		{entry: "notifications.github.>", want: []string{"notifications.github.acme.widgets.>"}},
		// The stored-message reads of any other stream, and direct gets of this one.
		{entry: "$JS.API.STREAM.MSG.GET.OTHER", refused: "stored message"},
		{entry: "$JS.API.DIRECT.GET.ENVOY_NOTIFICATIONS.>", refused: "stored message"},
		{entry: "$JS.API.DIRECT.GET.ENVOY_NOTIFICATIONS", refused: "stored message"},
		// A consumer create whose subject cannot name a filter.
		{entry: "$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.*", refused: "no filter"},
		{entry: "$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.legion-go-PUB-dispatch", refused: "no filter"},
		{entry: "$JS.API.CONSUMER.DURABLE.CREATE.ENVOY_NOTIFICATIONS.legion-go-PUB-dispatch", refused: "no filter"},
		{entry: "$JS.API.CONSUMER.DURABLE.CREATE.ENVOY_NOTIFICATIONS.>", refused: "no filter"},
		// A KV bucket's calls.
		{entry: "$KV.envoy_roles.>", refused: "KV bucket"},
		{entry: "$JS.API.STREAM.INFO.KV_envoy_roles", refused: "KV bucket"},
		{entry: "$JS.API.STREAM.MSG.GET.KV_envoy_roles", refused: "KV bucket"},
		{entry: "$JS.API.DIRECT.GET.KV_envoy_roles.>", refused: "KV bucket"},
		{entry: "$JS.API.CONSUMER.CREATE.KV_envoy_roles.>", refused: "KV bucket"},
		{entry: "$JS.API.CONSUMER.DELETE.KV_envoy_roles.>", refused: "KV bucket"},
		{entry: "$JS.API.CONSUMER.DURABLE.CREATE.KV_envoy_roles.watcher", refused: "KV bucket"},
		{entry: "$JS.API.STREAM.MSG.DELETE.KV_envoy_roles", refused: "KV bucket"},
		{entry: "$JS.FC.KV_envoy_roles.>", refused: "KV bucket"},
		// Every project's notifications at once.
		{entry: "notifications.>", refused: "every project"},
		{entry: "notifications.> example-queue", refused: "every project"},
		// Everything else is refused rather than passed through: an entry this function does not
		// know how to hold to projects is one it cannot vouch for.
		{entry: "notifications.dispatch.>", refused: "cannot be held"},
		{entry: "notifications.agent.>", refused: "cannot be held"},
		{entry: "notifications.role.>", refused: "cannot be held"},
		{entry: "notifications.dispatch.issue.*.>", refused: "cannot be held"},
		{entry: "_INBOX.abc.>", refused: "cannot be held"},
		{entry: "$JS.API.INFO", refused: "cannot be held"},
		{entry: "$JS.API.STREAM.INFO.OTHER", refused: "cannot be held"},
		{entry: "$JS.API.CONSUMER.DELETE.ENVOY_NOTIFICATIONS.*", refused: "cannot be held"},
		{entry: "$JS.API.CONSUMER.LIST.ENVOY_NOTIFICATIONS", refused: "cannot be held"},
		{entry: "$JS.API.CONSUMER.INFO.ENVOY_NOTIFICATIONS.envoy-listener-example-host-a", refused: "cannot be held"},
		{entry: "$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.legion-go-PUB-dispatch.notifications.dispatch.issue.PUB.>", refused: "cannot be held"},
		{entry: "$JS.ACK.ENVOY_NOTIFICATIONS.delivery-events.>", refused: "cannot be held"},
		{entry: "$SYS.REQ.USER.AUTH", refused: "cannot be held"},
		{entry: ">", refused: "cannot be held"},
		{entry: "", refused: "cannot be held"},
	} {
		for _, direction := range []string{"publish", "subscribe"} {
			const inboxes, heldInboxes = "_INBOX.>", "_INBOX_id.>"
			grant := natsgrant.Permissions{Publish: []string{inboxes}, Subscribe: []string{inboxes}}
			if direction == "publish" {
				grant.Publish = append(grant.Publish, row.entry)
			} else {
				grant.Subscribe = append(grant.Subscribe, row.entry)
			}
			got, err := natsgrant.WithinProjects(grant, scope)
			name := direction + " " + strconv.Quote(row.entry)
			if row.refused != "" {
				require.Error(t, err, name)
				require.ErrorContains(t, err, strconv.Quote(row.entry), name)
				require.ErrorContains(t, err, row.refused, name)
				require.Equal(t, natsgrant.Permissions{}, got, name)
				continue
			}
			require.NoError(t, err, name)
			held, other := got.Publish, got.Subscribe
			if direction == "subscribe" {
				held, other = got.Subscribe, got.Publish
			}
			require.Equal(t, []string{heldInboxes}, other, name)
			// A dropped entry (want nil) leaves the reply prefix alone.
			require.Equal(t, slices.Compact(slices.Concat([]string{heldInboxes}, row.want)), held, name)
		}
	}
}

// A grant holding _INBOX.> needs the identity that names its reply prefix, and a scope names
// project keys and owner/name repositories only: anything else would widen a subject the grant
// names, so it is refused naming the value.
func TestWithinProjectsRefusesAScopeItCannotName(t *testing.T) {
	_, err := natsgrant.WithinProjects(natsgrant.Daemon(), natsgrant.Scope{Projects: []string{"PUB"}})
	require.ErrorContains(t, err, strconv.Quote("_INBOX.>"))
	require.ErrorContains(t, err, "identity")

	for _, project := range []string{"pub", "*", ">", "_", "PUB.X", "PUB X", "", "P", "ABCDEFGHIJK"} {
		_, err := natsgrant.WithinProjects(natsgrant.Daemon(), natsgrant.Scope{Projects: []string{"PUB", project}, Identity: "id"})
		require.ErrorContains(t, err, "project "+strconv.Quote(project), "project %q", project)
	}
	for _, repo := range []string{"acme", "acme/", "/widgets", "acme/widgets/x", "*/widgets", "acme/>", "ac me/widgets", "acme/wid\tgets", ""} {
		_, err := natsgrant.WithinProjects(natsgrant.Daemon(), natsgrant.Scope{Projects: []string{"PUB"}, Repositories: []string{repo}, Identity: "id"})
		require.ErrorContains(t, err, "repository "+strconv.Quote(repo), "repository %q", repo)
	}
	_, err = natsgrant.WithinProjects(natsgrant.Daemon(), natsgrant.Scope{Repositories: []string{"acme/widgets"}, Identity: "id"})
	require.ErrorContains(t, err, "no project", "repositories with no project")
	require.ErrorContains(t, err, "acme/widgets", "repositories with no project")
}

// The longest and the digit-bearing project keys the contract allows (contracts.ProjectKeyPattern)
// are held like any other.
func TestWithinProjectsTakesEveryProjectKeyShape(t *testing.T) {
	for _, project := range []string{"A0", "ABCDEFGHIJ"} {
		got, err := natsgrant.WithinProjects(natsgrant.Daemon(), natsgrant.Scope{Projects: []string{project}, Identity: "id"})
		require.NoError(t, err, project)
		require.Contains(t, got.Publish, "$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.legion-go-"+project+"-dispatch.notifications.dispatch.issue."+project+".>", project)
		require.Contains(t, got.Subscribe, "notifications.envoy.exceptions.notifications.role."+project+".>", project)
	}
}

// Two projects: every project's consumers, each paired with its own project's filter and with each
// repository of the scope, written out in full.
func TestWithinProjectsHoldsEachProjectToItsOwnFilters(t *testing.T) {
	got, err := natsgrant.WithinProjects(natsgrant.Daemon(), natsgrant.Scope{
		Projects: []string{"PUB", "OTHER"}, Repositories: []string{"acme/widgets"}, Identity: "id",
	})
	require.NoError(t, err)
	var info, creates, next, acks []string
	for _, project := range []string{"OTHER", "PUB"} {
		for _, consumer := range []struct{ kind, filter string }{
			{"dispatch", "notifications.dispatch.issue." + project + ".>"},
			{"mapping", "notifications.dispatch.project." + project + ".settings.repo_project.updated"},
			{"wake", "notifications.legion." + strings.ToLower(project) + ".wake"},
			{"ready", "notifications.legion.wake"},
			{"github-acme_2fwidgets", "notifications.github.acme.widgets.>"},
		} {
			name := "legion-go-" + project + "-" + consumer.kind
			info = append(info, "$JS.API.CONSUMER.INFO.ENVOY_NOTIFICATIONS."+name)
			creates = append(creates, "$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS."+name+"."+consumer.filter)
			next = append(next, "$JS.API.CONSUMER.MSG.NEXT.ENVOY_NOTIFICATIONS."+name)
			acks = append(acks, "$JS.ACK.ENVOY_NOTIFICATIONS."+name+".>")
		}
	}
	require.Equal(t, natsgrant.Permissions{
		Publish: slices.Concat(info, creates, next, acks),
		Subscribe: []string{
			"notifications.envoy.exceptions.notifications.role.OTHER.>",
			"notifications.envoy.exceptions.notifications.role.PUB.>",
			"_INBOX_id.>",
		},
	}, got)
}

// A refusal is the whole answer wherever the refused entry is: after a held entry of the same
// list, and in the subscribe list after a publish list that held.
func TestWithinProjectsRefusesTheWholeGrant(t *testing.T) {
	for name, grant := range map[string]natsgrant.Permissions{
		"refused after a held publish entry":     {Publish: []string{"_INBOX.>", "notifications.>"}, Subscribe: []string{"_INBOX.>"}},
		"refused after a held subscribe entry":   {Publish: []string{"_INBOX.>"}, Subscribe: []string{"_INBOX.>", "notifications.>"}},
		"refused subscribe after a held publish": {Publish: natsgrant.Daemon().Publish, Subscribe: []string{"notifications.>"}},
	} {
		got, err := natsgrant.WithinProjects(grant, pubScope)
		require.ErrorContains(t, err, strconv.Quote("notifications.>"), name)
		require.Equal(t, natsgrant.Permissions{}, got, name)
	}
}

// NATS checks no push consumer's deliver subject and no pull's reply subject against the caller's
// subscribe grant, so whoever may create a consumer or pull one can send that consumer's messages
// to any subject. Only operator services hold those calls: the Legion daemon, the listener and
// Dispatch, each with a credential of its own (and, on a hub of static users, the host user a
// host's listener and sessions share). A session's own grant and the measurement job's hold
// neither, so no agent's credential can redirect a delivery.
func TestOnlyOperatorServicesCreateOrPullConsumers(t *testing.T) {
	session, err := natsgrant.Session("ses_a")
	require.NoError(t, err)
	consumerCall := func(grant natsgrant.Permissions) (string, bool) {
		for _, entry := range slices.Concat(grant.Publish, grant.Subscribe) {
			if strings.Contains(entry, "CONSUMER.CREATE.") || strings.Contains(entry, "CONSUMER.DURABLE.CREATE.") || strings.Contains(entry, "CONSUMER.MSG.NEXT.") {
				return entry, true
			}
		}
		return "", false
	}
	for name, grant := range map[string]natsgrant.Permissions{"Session(ses_a)": session, "SessionPattern()": natsgrant.SessionPattern(), "Measure()": natsgrant.Measure()} {
		entry, found := consumerCall(grant)
		require.False(t, found, "%s holds %q", name, entry)
	}
	for name, grant := range map[string]natsgrant.Permissions{
		"Daemon()": natsgrant.Daemon(), "Listener()": natsgrant.Listener("h"), "ListenerHost()": natsgrant.ListenerHost("h"),
		"Dispatch()": natsgrant.Dispatch("h"), "Host()": natsgrant.Host("h"),
	} {
		_, found := consumerCall(grant)
		require.True(t, found, "%s is listed as an operator service that creates consumers; it holds no such call now, so drop it from this list", name)
	}
}

// With no project a daemon's grant keeps no publish entry. nats-server holds a user JWT to nothing
// in a direction whose allow list is empty (server/accounts.go, buildPermissionsFromJwt), and the
// auth service writes the grant's lists into the JWT as they are, so WithinProjects refuses it
// naming the direction rather than answering a grant that may publish to every subject. A
// direction whose every entry is dropped, or that held no entry to begin with, is refused the same
// way.
func TestWithinProjectsRefusesAGrantHeldToNothingInADirection(t *testing.T) {
	for name, row := range map[string]struct {
		grant     natsgrant.Permissions
		scope     natsgrant.Scope
		direction string
	}{
		"Daemon() of no project": {grant: natsgrant.Daemon(), scope: natsgrant.Scope{Identity: "id"}, direction: "publish"},
		"every publish entry dropped": {
			grant: natsgrant.Permissions{
				Publish:   []string{"$JS.API.STREAM.INFO.ENVOY_NOTIFICATIONS", "$JS.API.STREAM.MSG.GET.ENVOY_NOTIFICATIONS"},
				Subscribe: []string{"_INBOX.>"},
			},
			scope:     pubScope,
			direction: "publish",
		},
		"no subscribe entry": {grant: natsgrant.Permissions{Publish: []string{"_INBOX.>"}}, scope: pubScope, direction: "subscribe"},
	} {
		got, err := natsgrant.WithinProjects(row.grant, row.scope)
		require.ErrorContains(t, err, "no "+row.direction+" entry", name)
		require.Equal(t, natsgrant.Permissions{}, got, name)
	}
}

// The order of a scope's lists and a repeat in them change nothing.
func TestWithinProjectsReadsAScopeAsASet(t *testing.T) {
	want, err := natsgrant.WithinProjects(natsgrant.Daemon(), natsgrant.Scope{
		Projects: []string{"OTHER", "PUB"}, Repositories: []string{"acme/gadgets.js", "acme/widgets"}, Identity: "id",
	})
	require.NoError(t, err)
	got, err := natsgrant.WithinProjects(natsgrant.Daemon(), natsgrant.Scope{
		Projects: []string{"PUB", "OTHER", "PUB"}, Repositories: []string{"acme/widgets", "acme/gadgets.js", "acme/widgets"}, Identity: "id",
	})
	require.NoError(t, err)
	require.Equal(t, want, got)
}

// Narrowing a grant changes no other holder's: the grant handed in keeps every entry, and the
// daemon's static grant is still, element for element, what the hub rendered before this package
// (the merge-base fixture), so a deployment that never runs WithinProjects keeps today's daemon
// user byte for byte (R9).
func TestWithinProjectsLeavesTheGrantItNarrows(t *testing.T) {
	grant := natsgrant.Daemon()
	before := natsgrant.Permissions{Publish: slices.Clone(grant.Publish), Subscribe: slices.Clone(grant.Subscribe)}
	_, err := natsgrant.WithinProjects(grant, pubScope)
	require.NoError(t, err)
	require.Equal(t, before, grant)

	var base mergeBase
	readJSON(t, "testdata/merge-base-grants.json", &base)
	requireSet(t, base.Daemon, natsgrant.Daemon(), "Daemon() after WithinProjects")
}

// What the grant names is read back through the readers every other part of the bus uses: each
// notification subject, and each filter a create subject names, belongs to a project of the scope
// (contracts.SubjectProject) or to a repository of it; each consumer is one the daemon opens under
// a project of the scope; and the reply prefix is the identity's. The one subject of no project is
// the wake lane every daemon shares (bus.LegionWakeTopic): a subject grant cannot narrow it, so
// what it carries is the listener's to limit (WithinProjects' doc comment says when it may be granted).
func TestWithinProjectsNamesOnlyItsScope(t *testing.T) {
	every := natsgrant.Permissions{
		Publish: []string{
			"$JS.API.STREAM.INFO.ENVOY_NOTIFICATIONS",
			"$JS.API.STREAM.MSG.GET.ENVOY_NOTIFICATIONS",
			"$JS.API.CONSUMER.CREATE.ENVOY_NOTIFICATIONS.>",
			"$JS.API.CONSUMER.INFO.ENVOY_NOTIFICATIONS.*",
			"$JS.API.CONSUMER.MSG.NEXT.ENVOY_NOTIFICATIONS.>",
			"$JS.ACK.ENVOY_NOTIFICATIONS.>",
			"notifications.legion.>",
		},
		Subscribe: []string{
			"_INBOX.>",
			"notifications.dispatch.issue.>",
			"notifications.envoy.exceptions.notifications.role.>",
			"notifications.legion.>",
			"notifications.github.>",
		},
	}
	for _, scope := range []natsgrant.Scope{
		pubScope,
		{Projects: []string{"PUB", "OTHER"}, Repositories: []string{"acme/widgets", "beta/a.b", "Gamma/Delta"}, Identity: "system:serviceaccount:legion-pub:legion-pub-daemon"},
		{Projects: []string{"ZZ"}, Identity: "daemon@example.com"},
	} {
		for name, grant := range map[string]natsgrant.Permissions{"Daemon()": natsgrant.Daemon(), "every kind": every} {
			got, err := natsgrant.WithinProjects(grant, scope)
			require.NoError(t, err, "%s within %v", name, scope)
			require.NotEmpty(t, got.Publish, "%s within %v", name, scope)
			require.NotEmpty(t, got.Subscribe, "%s within %v", name, scope)
			entry, found := natsgrant.NamesSystemSubject(got)
			require.False(t, found, "%s within %v names %q", name, scope, entry)
			for _, entry := range slices.Concat(got.Publish, got.Subscribe) {
				requireInScope(t, scope, entry)
			}
		}
	}
}

// daemonConsumerName matches the name of a consumer a Legion daemon opens, legion-go-<P>-<kind>,
// capturing the project and, for a GitHub consumer, its repository's spelling.
var daemonConsumerName = regexp.MustCompile(`^legion-go-([A-Z][A-Z0-9]+)-(dispatch|mapping|wake|ready|github-([A-Za-z0-9_-]+))$`)

// requireInScope fails unless entry reaches only what scope names.
func requireInScope(t *testing.T, scope natsgrant.Scope, entry string) {
	t.Helper()
	subject, _, _ := strings.Cut(entry, " ")
	// ownConsumer fails unless name is a consumer the daemon opens under a project of the scope,
	// and answers the repository a GitHub consumer is named for.
	ownConsumer := func(name string) (repository string) {
		t.Helper()
		match := daemonConsumerName.FindStringSubmatch(name)
		require.NotNil(t, match, "%q names consumer %q, which no daemon opens", entry, name)
		require.Contains(t, scope.Projects, match[1], "%q names another project's consumer", entry)
		if match[3] == "" {
			return ""
		}
		for _, r := range scope.Repositories {
			if natsgrant.GitHubConsumerName(match[1], r) == name {
				return r
			}
		}
		t.Fatalf("%q names GitHub consumer %q of no repository of the scope", entry, name)
		return ""
	}
	inScope := func(subject string) {
		t.Helper()
		if subject == bus.LegionWakeTopic {
			return
		}
		project, repo, ok := contracts.SubjectProject(subject)
		require.True(t, ok, "%q names %q, which is no notification subject", entry, subject)
		if repo != "" {
			var repos []string
			for _, r := range scope.Repositories {
				owner, name, _ := strings.Cut(r, "/")
				repos = append(repos, contracts.SanitizeSubjectSegment(owner)+"/"+contracts.SanitizeSubjectSegment(name))
			}
			require.Contains(t, repos, repo, "%q names a repository outside the scope", entry)
			return
		}
		require.Contains(t, scope.Projects, project, "%q names subject %q of project %q", entry, subject, project)
	}
	const stream = bus.Stream + "."
	switch {
	case subject == natsgrant.InboxPrefix(scope.Identity)+".>":
	case strings.HasPrefix(subject, "$JS.API.CONSUMER.CREATE."+stream):
		name, filter, found := strings.Cut(strings.TrimPrefix(subject, "$JS.API.CONSUMER.CREATE."+stream), ".")
		require.True(t, found, "%q creates a consumer with no filter", entry)
		if repository := ownConsumer(name); repository != "" {
			owner, repo, _ := strings.Cut(repository, "/")
			require.Equal(t, contracts.GithubRepositoryPrefix(owner, repo)+".>", filter, "%q pairs a GitHub consumer with another repository's lane", entry)
		}
		inScope(filter)
	case strings.HasPrefix(subject, "$JS.API.CONSUMER.INFO."+stream):
		ownConsumer(strings.TrimPrefix(subject, "$JS.API.CONSUMER.INFO."+stream))
	case strings.HasPrefix(subject, "$JS.API.CONSUMER.MSG.NEXT."+stream):
		ownConsumer(strings.TrimPrefix(subject, "$JS.API.CONSUMER.MSG.NEXT."+stream))
	case strings.HasPrefix(subject, "$JS.ACK."+stream):
		name, rest, _ := strings.Cut(strings.TrimPrefix(subject, "$JS.ACK."+stream), ".")
		require.Equal(t, ">", rest, "%q", entry)
		ownConsumer(name)
	case strings.HasPrefix(subject, bus.NotificationTopicPrefix):
		inScope(subject)
	default:
		t.Fatalf("%q is no entry a grant held to %v may hold", entry, scope)
	}
}
