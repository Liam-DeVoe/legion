package gcp_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/core"
	"github.com/sjawhar/envoy/infra/gcp"
	"github.com/sjawhar/envoy/infra/nats"
)

// The callout keys of the tests in this package: the auth service's user, the account whose seed
// signs its answers, and the curve key its requests are sealed to. Public keys only; their seeds
// were discarded when they were made.
const (
	keyAuth       = "UDOLL5K64CAO7IWO3XJTHILQNTX2L4MQ7IWIAATEEPU42KK2I4QTPOT4"
	calloutIssuer = "AB2TCXFBYSIN24KFPVJARBN5CHCL4ZCV5SBWSONTZ2WYXS3XTAEK6S3G"
	calloutXKey   = "XBVCTZK3PV5Y7TBIVYTDZBP3O2YGHUUL7MMENN4DTZIREJCPGLTAF6EL"
)

// authServiceUser is the user the auth service connects as: it answers the server's authorization
// requests and publishes nothing else.
func authServiceUser() gcp.NATSUserArgs {
	return gcp.NATSUserArgs{Name: "example-auth", NKey: keyAuth, Permissions: &gcp.PermissionsArgs{
		Publish:   nats.RulesArgs{Allow: []string{"$SYS._INBOX.>"}},
		Subscribe: nats.RulesArgs{Allow: []string{"$SYS.REQ.USER.AUTH"}},
	}}
}

// authCallout is a callout naming authServiceUser.
func authCallout() *nats.AuthCalloutArgs {
	return &nats.AuthCalloutArgs{Issuer: calloutIssuer, XKey: calloutXKey, ServiceUser: "example-auth"}
}

// privateNatsHub is the one gcp:NatsHub R12 lets hold a private project: the auth service's user
// alone behind the callout, with no listener of its own and no session user.
func privateNatsHub() *gcp.NatsHubArgs {
	args := exampleArgs()
	args.Users = []gcp.NATSUserArgs{authServiceUser()}
	args.Listener, args.SessionUser = nil, nil
	args.AuthCallout, args.PrivateProjects = authCallout(), true
	return args
}

// privateClusteredHub is the one gcp:ClusteredNatsHub R12 lets hold a private project, as
// privateNatsHub is gcp:NatsHub's.
func privateClusteredHub() *gcp.ClusteredNatsHubArgs {
	args := withoutTailnet(clusteredArgs())
	args.Users = []gcp.NATSUserArgs{authServiceUser()}
	args.SessionUser = nil
	args.AuthCallout, args.PrivateProjects = authCallout(), true
	return args
}

// privateProjectsRefusal is one hub privateProjects cannot be set on: the private hub with a
// session user, with a client user that has a seed, or with no callout. who names what a client
// could connect as past the auth service, and want is the refusal.
type privateProjectsRefusal struct {
	name, who                  string
	session, client, noCallout bool
	want                       string
}

// apply makes the row of a private hub's users, session user and callout.
func (row privateProjectsRefusal) apply(users *[]gcp.NATSUserArgs, session **gcp.SessionUserArgs, callout **nats.AuthCalloutArgs) {
	if row.session {
		*session = &gcp.SessionUserArgs{Name: "example-sessions", NKey: keySessions}
	}
	if row.client {
		*users = append(*users, exampleArgs().Users[0])
	}
	if row.noCallout {
		*callout = nil
	}
}

// privateProjectsRefusals are the three ways a hub could hold a private project while a client can
// connect past the auth service, which c-private-needs-callout names: a session user, whom a client
// presenting no credential becomes; a client user with a seed; and no callout at all.
func privateProjectsRefusals() []privateProjectsRefusal {
	return []privateProjectsRefusal{
		{
			name: "beside a session user", who: `the session user "example-sessions"`, session: true,
			want: `privateProjects: noAuthUser "example-sessions": a client presenting no credential would become a user while a project is private`,
		},
		{
			name: "beside a client user with a seed", who: `the user "example-host-a", whose seed its host stores`, client: true,
			want: `privateProjects: users[1] "example-host-a": a stored client seed while a project is private`,
		},
		{
			name: "without authCallout", who: "no callout", noCallout: true,
			want: "privateProjects: a project can be private only while the server admits each client through the auth service, and the authorization has no callout",
		},
	}
}

// requirePrivateProjectsRefusal requires err to refuse privateProjects as row says, as a
// core.InputError naming privateProjects. A hub that took the row instead fails naming who it
// would have admitted past the auth service.
func requirePrivateProjectsRefusal(t *testing.T, row privateProjectsRefusal, err error) {
	t.Helper()
	require.ErrorContains(t, err, row.want, "privateProjects was taken beside %s", row.who)
	var input *core.InputError
	require.ErrorAs(t, err, &input)
	require.Equal(t, "privateProjects", input.Input, "%v", err)
}

// TestPrivateProjectsNeedTheAuthService (c-private-needs-callout, R12): each hub component refuses
// privateProjects beside a session user, beside a client user with a seed, and without authCallout,
// naming the input and who could connect past the auth service, and registers nothing; with
// authCallout and the service's user alone, it registers. aws:NatsService's rows are in
// aws/natsservice_callout_test.go, under the same name.
//
// Negative controls, each run on its own in core/nats_callout.go's validateCallout, and each making
// the component take the configuration R12 forbids rather than refuse it in other words:
//   - Let the session user through: delete the noAuthUser refusal, and make the users loop skip
//     a.NoAuthUser beside the service user. The session-user rows of all three components fail
//     with "privateProjects was taken beside the session user …", naming it.
//   - Let a client user with a seed through: delete the users loop. The seed rows fail with
//     "privateProjects was taken beside the user …", naming "example-host-a" ("example-listener"
//     on aws:NatsService).
//   - Let privateProjects through without a callout: make the a.Callout == nil branch return nil
//     (deleting it would dereference the nil callout in the users loop). The callout rows fail
//     with "privateProjects was taken beside no callout".
func TestPrivateProjectsNeedTheAuthService(t *testing.T) {
	t.Run("gcp:NatsHub", func(t *testing.T) {
		for _, row := range privateProjectsRefusals() {
			t.Run(row.name, func(t *testing.T) {
				args := privateNatsHub()
				row.apply(&args.Users, &args.SessionUser, &args.AuthCallout)
				requirePrivateProjectsRefusal(t, row, gcp.ResolveNatsHub(args))
				m, err := run(t, args, nil, true)
				require.ErrorContains(t, err, row.want)
				require.Equal(t, []string{gcpProviderType}, m.Types(), "nothing registered beyond the test's provider")
			})
		}
		t.Run("with authCallout and the service's user alone, it registers", func(t *testing.T) {
			require.NoError(t, gcp.ResolveNatsHub(privateNatsHub()))
			m, err := run(t, privateNatsHub(), nil, true)
			require.NoError(t, err)
			require.Contains(t, m.Types(), hubType)
		})
	})
	t.Run("gcp:ClusteredNatsHub", func(t *testing.T) {
		for _, row := range privateProjectsRefusals() {
			t.Run(row.name, func(t *testing.T) {
				args := privateClusteredHub()
				row.apply(&args.Users, &args.SessionUser, &args.AuthCallout)
				_, err := gcp.ResolveClusteredNatsHub(args)
				requirePrivateProjectsRefusal(t, row, err)
				m, err := runClustered(t, "example-clustered-hub", args, nil)
				require.ErrorContains(t, err, row.want)
				require.Empty(t, m.Registrations(), "nothing registered")
			})
		}
		t.Run("with authCallout and the service's user alone, it registers", func(t *testing.T) {
			_, err := gcp.ResolveClusteredNatsHub(privateClusteredHub())
			require.NoError(t, err)
			m, err := runClustered(t, "example-clustered-hub", privateClusteredHub(), nil)
			require.NoError(t, err)
			require.Contains(t, m.Types(), clusteredHubType)
		})
	})
}
