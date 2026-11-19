package gcp_test

import (
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/property"
	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/infra/gcp"
)

// dispatchOIDCAgentArgs are dispatchArgs with agentAuth oidc and the audience it requires.
func dispatchOIDCAgentArgs() *gcp.DispatchArgs {
	args := dispatchArgs()
	args.OidcAudience = dispatchOidcAudience
	args.AgentAuth = "oidc"
	return args
}

// Under agentAuth oidc no static agent token exists anywhere: no RandomPassword
// <hostname>-agent-token is registered, the Dispatch Secret holds no DISPATCH_AGENT_TOKEN, the app
// is given none, and it is given DISPATCH_OIDC_ISSUER (the cluster's own issuer) and
// DISPATCH_OIDC_AUDIENCE, which Dispatch then boots with alone. Control: the same arguments with
// agentAuth unset render the token in all three places, so each check can see a token.
func TestDispatchAgentAuthOIDCRendersNoStaticToken(t *testing.T) {
	for _, row := range []struct {
		name  string
		args  func() *gcp.DispatchArgs
		token bool
	}{
		{"agentAuth oidc", dispatchOIDCAgentArgs, false},
		{"control: agentAuth unset", func() *gcp.DispatchArgs {
			args := dispatchOIDCAgentArgs()
			args.AgentAuth = ""
			return args
		}, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			m, err := runDispatch(t, row.args(), true)
			require.NoError(t, err)
			var passwords []string
			for _, registration := range registrationsOf(m, "random:index/randomPassword:RandomPassword") {
				passwords = append(passwords, registration.Name)
			}
			keys := secretDataKeys(dispatchSecretData(t, m))
			env := appEnv(t, m)
			has := require.NotContains
			if row.token {
				has = require.Contains
			}
			has(t, passwords, "example-dispatch-agent-token")
			has(t, keys, "DISPATCH_AGENT_TOKEN")
			has(t, env, "DISPATCH_AGENT_TOKEN")
			require.Contains(t, passwords, "example-dispatch-signing-key", "the signing key stays in both modes")
			require.Contains(t, keys, "DISPATCH_SIGNING_KEY")
			require.Equal(t, azureIssuer, env["DISPATCH_OIDC_ISSUER"])
			require.Equal(t, dispatchOidcAudience, env["DISPATCH_OIDC_AUDIENCE"])
		})
	}
}

// agentAuth token, written out, renders exactly what agentAuth unset renders: the default is
// token, and naming it changes nothing. Control: agentAuth oidc renders something else.
func TestDispatchAgentAuthTokenIsTheDefault(t *testing.T) {
	render := func(agentAuth string) string {
		args := dispatchOIDCAgentArgs()
		args.AgentAuth = agentAuth
		m, err := runDispatch(t, args, true)
		require.NoError(t, err)
		return dispatchRegistrations(m)
	}
	unset := render("")
	require.Equal(t, unset, render("token"))
	require.NotEqual(t, unset, render("oidc"))
}

// The agentToken output is an explicit "" under agentAuth oidc, known in a preview as in an
// update, as clusterOnly gives it: never an unknown, and never a token. Control: with agentAuth
// unset the output is the generated token, a non-empty value.
func TestConstructOutputsNoAgentTokenUnderAgentAuthOIDC(t *testing.T) {
	for _, row := range []struct {
		name   string
		dryRun bool
	}{{"an update", false}, {"a preview", true}} {
		t.Run(row.name, func(t *testing.T) {
			inputs := dispatchConstructBaseInputs()
			inputs["oidcAudience"] = property.New(dispatchOidcAudience)
			inputs["agentAuth"] = property.New("oidc")
			_, response, err := constructDispatch(t, inputs, row.dryRun)
			require.NoError(t, err)
			value, ok := response.State.GetOk("agentToken")
			require.True(t, ok, "agentToken is in the state")
			if value.Secret() {
				value = value.WithSecret(false)
			}
			require.True(t, value.IsString(), "agentToken is a known string, not %s", value)
			require.Equal(t, "", value.AsString())
		})
	}
	_, response, err := constructDispatch(t, dispatchConstructBaseInputs(), false)
	require.NoError(t, err)
	value, ok := response.State.GetOk("agentToken")
	require.True(t, ok, "control: agentToken is in the state")
	value = value.WithSecret(false)
	require.True(t, value.IsString() && value.AsString() != "", "control: with agentAuth unset agentToken is the generated token, not %s", value)
}

// agentAuth oidc with no oidcAudience would leave no agent credential but a personal token, so it
// is refused by name before anything registers; a value that is neither token nor oidc is
// refused naming the two. An oidcAudience or oidcIssuer with whitespace around it is refused in
// either mode, since Dispatch trims the variable it renders as: a blank one would render the pair
// and leave Dispatch half of it. Control: oidc with oidcAudience resolves.
func TestDispatchRefusesAgentAuthItCannotRender(t *testing.T) {
	for _, row := range []struct {
		name string
		edit func(*gcp.DispatchArgs)
		want string
	}{
		{"oidc with no audience", func(a *gcp.DispatchArgs) { a.OidcAudience = "" },
			"agentAuth oidc requires oidcAudience: with no shared agent token, a projected ServiceAccount token for that audience is how an agent authenticates; set oidcAudience, or unset agentAuth"},
		{"oidc with an issuer and no audience", func(a *gcp.DispatchArgs) { a.OidcAudience, a.OidcIssuer = "", dispatchOidcIssuer },
			"oidcAudience is required when oidcIssuer is set"},
		{"another value", func(a *gcp.DispatchArgs) { a.AgentAuth = "OIDC" }, `agentAuth "OIDC" must be token or oidc`},
		{"a value with spaces", func(a *gcp.DispatchArgs) { a.AgentAuth = " oidc" }, `agentAuth " oidc" must be token or oidc`},
		{"oidc with a blank audience", func(a *gcp.DispatchArgs) { a.OidcAudience = "  " },
			"oidcAudience must have no whitespace around it: Dispatch trims DISPATCH_OIDC_AUDIENCE before reading it"},
		{"oidc with an audience padded", func(a *gcp.DispatchArgs) { a.OidcAudience = " " + a.OidcAudience + "\n" },
			"oidcAudience must have no whitespace around it: Dispatch trims DISPATCH_OIDC_AUDIENCE before reading it"},
		{"oidc with a blank issuer", func(a *gcp.DispatchArgs) { a.OidcIssuer = "\t" },
			"oidcIssuer must have no whitespace around it: Dispatch trims DISPATCH_OIDC_ISSUER before reading it"},
		{"token with a blank audience", func(a *gcp.DispatchArgs) { a.AgentAuth, a.OidcAudience = "", "  " },
			"oidcAudience must have no whitespace around it: Dispatch trims DISPATCH_OIDC_AUDIENCE before reading it"},
		{"token with an issuer and a blank audience", func(a *gcp.DispatchArgs) {
			a.AgentAuth, a.OidcAudience, a.OidcIssuer = "token", " ", dispatchOidcIssuer
		},
			"oidcAudience must have no whitespace around it: Dispatch trims DISPATCH_OIDC_AUDIENCE before reading it"},
		{"token with an audience and a blank issuer", func(a *gcp.DispatchArgs) { a.AgentAuth, a.OidcIssuer = "", "  " },
			"oidcIssuer must have no whitespace around it: Dispatch trims DISPATCH_OIDC_ISSUER before reading it"},
	} {
		t.Run(row.name, func(t *testing.T) {
			args := dispatchOIDCAgentArgs()
			row.edit(args)
			m, err := runDispatch(t, args, true)
			require.ErrorContains(t, err, "envoy-infra:gcp:Dispatch example-dispatch: "+row.want)
			require.ElementsMatch(t, []string{gcpProviderType, randomProviderType, tailscaleProviderType}, m.Types())
		})
	}
	_, err := gcp.ResolveDispatch(dispatchOIDCAgentArgs())
	require.NoError(t, err)
}
