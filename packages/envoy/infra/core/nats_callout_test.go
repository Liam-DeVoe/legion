package core_test

import (
	"strings"
	"testing"

	"github.com/nats-io/nkeys"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/core"
)

// The callout goldens' keys: the auth service's user, the account whose seed signs its answers,
// and the curve key its answers are sealed to. Public keys only; their seeds were discarded.
const (
	authServiceKey = "UD5BKU3BU7COAOUCXHGXWOSLNVLL4TEXCWPJHDUQE4C47ZNOYFOZVKUN"
	calloutIssuer  = "AB2TCXFBYSIN24KFPVJARBN5CHCL4ZCV5SBWSONTZ2WYXS3XTAEK6S3G"
	calloutXKey    = "XBVCTZK3PV5Y7TBIVYTDZBP3O2YGHUUL7MMENN4DTZIREJCPGLTAF6EL"
)

// authService is the user the auth service connects as: it answers the server's authorization
// requests and publishes nothing else.
func authService() core.User {
	return core.User{
		Name:      "example-auth",
		NKey:      authServiceKey,
		Publish:   &core.Rules{Allow: []string{"$SYS._INBOX.>"}},
		Subscribe: &core.Rules{Allow: []string{"$SYS.REQ.USER.AUTH"}},
		HasSeed:   true,
	}
}

// withCallout is hubShaped with the auth service's user and the callout: every static user of
// today's hub stays, so the hub serves them as before while the service admits every other client.
func withCallout() core.Authorization {
	a := hubShaped()
	a.Users = append(a.Users, authService())
	a.Callout = &core.AuthCallout{Issuer: calloutIssuer, XKey: calloutXKey, ServiceUser: "example-auth"}
	return a
}

// privateOnly is the one authorization R12 lets hold a private project: the auth service's user
// alone, so every client connects through the service, and no user stands for a client presenting
// no credential.
func privateOnly() core.Authorization {
	return core.Authorization{
		Users:           []core.User{authService()},
		Callout:         &core.AuthCallout{Issuer: calloutIssuer, ServiceUser: "example-auth"},
		PrivateProjects: true,
	}
}

// The callout block sits inside the authorization block after its users: the authorization
// timeout, then auth_callout naming the issuer, every keyed user's key in users' order (the pending
// user has none, so it is not there), and the curve key.
func TestAuthCalloutRendersTheCalloutBlock(t *testing.T) {
	text, err := withCallout().Render(hubHeader)
	require.NoError(t, err)
	requireSameText(t, golden(t, "authorization-callout.golden"), text)
}

// Without an xkey the block has no xkey line, and the server sends its requests unsealed.
func TestAuthCalloutWithoutAnXKeyRendersNoXKeyLine(t *testing.T) {
	text, err := privateOnly().Render(nil)
	require.NoError(t, err)
	require.Equal(t, "authorization {\n"+
		"  users: [\n"+
		"    # example-auth\n"+
		`    {"nkey": "`+authServiceKey+`", "permissions": {"publish": {"allow": ["$SYS._INBOX.>"]}, "subscribe": {"allow": ["$SYS.REQ.USER.AUTH"]}}}`+"\n"+
		"  ]\n"+
		"  timeout: 5\n"+
		"  auth_callout {\n"+
		`    issuer: "`+calloutIssuer+`"`+"\n"+
		`    auth_users: ["`+authServiceKey+`"]`+"\n"+
		"  }\n"+
		"}\n", text)
}

// CalloutBlock is exactly the callout's lines of Render's text, which a component that reloads its
// servers rolls them on, and empty without a callout.
func TestCalloutBlockIsTheRenderedCalloutLines(t *testing.T) {
	text, err := withCallout().Render(hubHeader)
	require.NoError(t, err)
	block := withCallout().CalloutBlock()
	require.True(t, strings.HasPrefix(block, "  timeout: 5\n  auth_callout {\n"), "%q", block)
	require.Contains(t, text, "  ]\n"+block+"}\n")
	require.Empty(t, hubShaped().CalloutBlock())
}

func TestValidateRefusesACalloutThatCannotKeepAProjectApart(t *testing.T) {
	user, err := nkeys.CreateUser()
	require.NoError(t, err)
	userSeed, err := user.Seed()
	require.NoError(t, err)
	userKey, err := user.PublicKey()
	require.NoError(t, err)
	account, err := nkeys.CreateAccount()
	require.NoError(t, err)
	accountSeed, err := account.Seed()
	require.NoError(t, err)
	curve, err := nkeys.CreateCurveKeys()
	require.NoError(t, err)
	curveSeed, err := curve.Seed()
	require.NoError(t, err)

	callout := func(a *core.Authorization) *core.AuthCallout { return a.Callout }
	issuer := func(key string) func(a *core.Authorization) {
		return func(a *core.Authorization) { callout(a).Issuer = key }
	}
	xkey := func(key string) func(a *core.Authorization) {
		return func(a *core.Authorization) { callout(a).XKey = key }
	}
	sessions := func(a *core.Authorization) {
		a.Users = append(a.Users, hubShaped().Users[4])
		a.NoAuthUser = "example-sessions"
	}
	rows := []struct {
		name   string
		base   func() core.Authorization
		change func(a *core.Authorization)
		err    string
	}{
		// A seed is refused before anything quotes the field, and never echoed.
		{"an account seed for the issuer", withCallout, issuer(string(accountSeed)), "callout.issuer holds an nkey seed (not echoed)"},
		{"a curve seed for the xkey", withCallout, xkey(string(curveSeed)), "callout.xkey holds an nkey seed (not echoed)"},
		{"a seed for the service user", withCallout, func(a *core.Authorization) { callout(a).ServiceUser = string(userSeed) }, "callout.serviceUser holds an nkey seed (not echoed)"},

		{"no issuer", withCallout, issuer(""), "callout.issuer is not an account public key"},
		{"a user key for the issuer", withCallout, issuer(userKey), "callout.issuer is not an account public key"},
		{"a curve key for the issuer", withCallout, issuer(calloutXKey), "callout.issuer is not an account public key"},
		{"an issuer whose checksum fails", withCallout, issuer(calloutIssuer[:55] + "A"), "callout.issuer is not an account public key"},
		{"a lower-case issuer", withCallout, issuer(strings.ToLower(calloutIssuer)), "callout.issuer is not an account public key"},
		{"a user key for the xkey", withCallout, xkey(userKey), "callout.xkey is not a curve public key"},
		{"an account key for the xkey", withCallout, xkey(calloutIssuer), "callout.xkey is not a curve public key"},
		{"an xkey of another length", withCallout, xkey(calloutXKey[:55]), "callout.xkey is not a curve public key"},
		{"no service user", withCallout, func(a *core.Authorization) { callout(a).ServiceUser = "" }, `callout.serviceUser "" names no keyed user`},
		{"a service user naming nobody", withCallout, func(a *core.Authorization) { callout(a).ServiceUser = "example-nobody" }, `callout.serviceUser "example-nobody" names no keyed user`},
		{"a pending service user", withCallout, func(a *core.Authorization) { callout(a).ServiceUser = "example-pending" }, `callout.serviceUser "example-pending" names no keyed user`},
		// A client presenting no credential would hold the service's grant and answer every
		// connection's authorization itself.
		{"the session user as the service user", withCallout, func(a *core.Authorization) { callout(a).ServiceUser = "example-sessions" },
			`callout.serviceUser "example-sessions" is noAuthUser: a client presenting no credential would become the auth service`},

		{"private projects without the callout", privateOnly, func(a *core.Authorization) { a.Callout = nil },
			"privateProjects: a project can be private only while the server admits each client through the auth service, and the authorization has no callout"},
		{"private projects beside a session user", privateOnly, sessions,
			`privateProjects: noAuthUser "example-sessions": a client presenting no credential would become a user while a project is private`},
		{"private projects beside a client user with a seed", privateOnly, func(a *core.Authorization) { a.Users = append(a.Users, hubShaped().Users[1]) },
			`privateProjects: users[1] "example-dispatch": a stored client seed while a project is private`},
		{"private projects beside a pending client user", privateOnly, func(a *core.Authorization) { a.Users = append(a.Users, hubShaped().Users[3]) },
			`privateProjects: users[1] "example-pending": a stored client seed while a project is private`},
		{"private projects beside a keyed client user without a seed", privateOnly, func(a *core.Authorization) {
			u := hubShaped().Users[1]
			u.HasSeed = false
			a.Users = append(a.Users, u)
		}, `privateProjects: users[1] "example-dispatch": a static client user while a project is private`},
		{"private projects beside a client user named before the service's", privateOnly, func(a *core.Authorization) {
			a.Users = append([]core.User{hubShaped().Users[0]}, a.Users...)
		}, `privateProjects: users[0] "example-hub": a stored client seed while a project is private`},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			a := r.base()
			require.NoError(t, a.Validate(), "the row's base must be accepted for its refusal to mean anything")
			r.change(&a)
			require.ErrorContains(t, a.Validate(), r.err)
			// A component that carries the callout or privateProjects under its own input name finds
			// which was refused with errors.As: each refusal but a seed's names its input.
			for prefix, input := range map[string]string{"callout.": "callout", "privateProjects": "privateProjects"} {
				if strings.HasPrefix(r.err, prefix) && !strings.Contains(r.err, "seed (not echoed)") {
					requireInputError(t, a.Validate(), input, r.err)
				}
			}
			_, err := a.Render(hubHeader)
			require.ErrorContains(t, err, r.err, "Render renders only what Validate accepts")
			_, err = a.StartScript(hubHeader, nil, "/tmp/nats-server.conf")
			requireInputError(t, err, "authorization", r.err)
			require.EqualError(t, err, "authorization: "+a.Validate().Error(), "StartScript renders only what Validate accepts")
			if strings.Contains(r.err, "seed (not echoed)") {
				for _, seed := range [][]byte{userSeed, accountSeed, curveSeed} {
					require.NotContains(t, err.Error(), string(seed))
				}
			}
		})
	}
}

func TestValidateAcceptsACallout(t *testing.T) {
	rows := []struct {
		name string
		a    func() core.Authorization
	}{
		{"today's users beside the callout", withCallout},
		{"the callout without an xkey", func() core.Authorization {
			a := withCallout()
			a.Callout.XKey = ""
			return a
		}},
		{"private projects with the service user alone", privateOnly},
		{"private projects with a sealed callout", func() core.Authorization {
			a := privateOnly()
			a.Callout.XKey = calloutXKey
			return a
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			require.NoError(t, r.a().Validate())
		})
	}
}
