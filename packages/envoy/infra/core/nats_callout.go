package core

import (
	"fmt"
	"strings"

	"github.com/nats-io/nkeys"

	"github.com/sjawhar/envoy/internal/bus"
)

// AuthCallout is a NATS server's authorization callout: every client that is not one of the
// authorization's keyed users is admitted, or refused, by the auth service (dispatch://INFRA-840),
// which reads the client's token and answers with that connection's grant. The server runs in
// config mode, with no accounts block, so the service places each client in the global account,
// $G, beside the static users.
type AuthCallout struct {
	// Issuer is the public key of the account (A and 55 base32 characters) whose seed signs the
	// auth service's answers; the server refuses an answer another key signed.
	Issuer string `json:"issuer"`
	// XKey is "" or a curve public key (X and 55 base32 characters): with one, the server seals
	// each request to the service with it, so only the service reads a client's token.
	XKey string `json:"xkey"`
	// ServiceUser is the Name of the keyed user the auth service connects as.
	ServiceUser string `json:"serviceUser"`
}

// calloutTimeout is the authorization timeout, in seconds, of a server with a callout. nats-server
// waits that long for the auth service's answer to a client (v2.15.1 server/auth_callout.go:386 and
// :462), and its default is 2 (server/const.go AUTH_TIMEOUT), which a service that checks a token
// against its issuer can exceed.
const calloutTimeout = 5

// seedField names, by its path, the first of the callout's fields that holds an nkey seed, or
// returns "".
func (c AuthCallout) seedField() string {
	for _, f := range []struct{ field, value string }{
		{"callout.issuer", c.Issuer}, {"callout.xkey", c.XKey}, {"callout.serviceUser", c.ServiceUser},
	} {
		if bus.HoldsSeed(f.value) {
			return f.field
		}
	}
	return ""
}

// validate refuses a callout the server would read otherwise than it says, naming the field: an
// issuer that is not an account public key, an xkey that is set and is not a curve public key, a
// service user that names no keyed user of a, and a service user that is a's noAuthUser, as whom a
// client presenting no credential would answer every connection's authorization. Neither key is
// quoted: seedField has refused a seed in either, and a public key is checked by its shape alone.
// Each refusal is a core.InputError naming callout, so a component whose input carries the callout
// under another name can name that input.
func (c AuthCallout) validate(a Authorization) error {
	if !isPublicKey(c.Issuer, nkeys.PrefixByteAccount) {
		return InputErrorf("callout", "callout.issuer is not an account public key (A and 55 base32 characters with a valid checksum); give the public key of the account whose seed signs the auth service's answers, never the seed")
	}
	if c.XKey != "" && !isPublicKey(c.XKey, nkeys.PrefixByteCurve) {
		return InputErrorf("callout", "callout.xkey is not a curve public key (X and 55 base32 characters with a valid checksum); give the public key, never the seed, or leave it empty")
	}
	if u, ok := a.user(c.ServiceUser); !ok || u.NKey == "" {
		return InputErrorf("callout", "callout.serviceUser %q names no keyed user: the auth service connects as one of users, with an nkey", c.ServiceUser)
	}
	if c.ServiceUser == a.NoAuthUser {
		return InputErrorf("callout", "callout.serviceUser %q is noAuthUser: a client presenting no credential would become the auth service, and answer every connection's authorization", c.ServiceUser)
	}
	return nil
}

// validateCallout refuses the callout (AuthCallout.validate) and, with PrivateProjects, every
// authorization that could admit a client past the auth service (R12 of dispatch://INFRA-3862): no
// callout; a noAuthUser, whom a client presenting no credential becomes; and any user but the
// service's, each a static client user whose seed a client stores or could store, a pending one
// included. Each refusal names the field, and a user by its index and name; each of
// PrivateProjects' is a core.InputError naming privateProjects.
func (a Authorization) validateCallout() error {
	if a.Callout != nil {
		if err := a.Callout.validate(a); err != nil {
			return err
		}
	}
	if !a.PrivateProjects {
		return nil
	}
	if a.Callout == nil {
		return InputErrorf("privateProjects", "privateProjects: a project can be private only while the server admits each client through the auth service, and the authorization has no callout")
	}
	if a.NoAuthUser != "" {
		return InputErrorf("privateProjects", "privateProjects: noAuthUser %q: a client presenting no credential would become a user while a project is private", a.NoAuthUser)
	}
	for i, u := range a.Users {
		if u.Name == a.Callout.ServiceUser {
			continue
		}
		what := "a stored client seed"
		if !u.HasSeed {
			what = "a static client user"
		}
		return InputErrorf("privateProjects", "privateProjects: %s %q: %s while a project is private; every client connects through the auth service, so callout.serviceUser %q is the only user", u.label(i), u.Name, what, a.Callout.ServiceUser)
	}
	return nil
}

// CalloutBlock is the callout's lines of Render's text, "" without a callout, for an authorization
// Validate accepts. nats-server v2.15.1 refuses a whole reload that changes its callout ("config
// reload not supported for AuthCallout": server/reload.go diffOptions has no case for it), so a
// server takes a changed block only when it restarts; a component that reloads its servers rolls
// them when this text changes. auth_users is in it, so a keyed user added or removed changes it.
func (a Authorization) CalloutBlock() string {
	if a.Callout == nil {
		return ""
	}
	var b strings.Builder
	a.Callout.render(&b, a.Users)
	return b.String()
}

// render writes the callout's lines of the authorization block, which follow its users: the
// authorization timeout, then auth_callout naming the issuer, the key of every keyed user of users
// in their order, and the xkey when there is one. auth_users is derived from users and never
// given: in config mode nats-server sends a keyed user it does not list to the callout, even one
// whose nkey signature was valid (v2.15.1 server/auth.go:739-761), so a static user left out of it
// would be refused at connect.
func (c AuthCallout) render(b *strings.Builder, users []User) {
	keys := []string{}
	for _, u := range users {
		if u.NKey != "" {
			keys = append(keys, u.NKey)
		}
	}
	ascii := PyJSONOptions{EnsureASCII: true}
	fmt.Fprintf(b, "  timeout: %d\n", calloutTimeout)
	b.WriteString("  auth_callout {\n")
	b.WriteString("    issuer: " + PyJSON(c.Issuer, ascii) + "\n")
	b.WriteString("    auth_users: " + PyJSON(keys, ascii) + "\n")
	if c.XKey != "" {
		b.WriteString("    xkey: " + PyJSON(c.XKey, ascii) + "\n")
	}
	b.WriteString("  }\n")
}
