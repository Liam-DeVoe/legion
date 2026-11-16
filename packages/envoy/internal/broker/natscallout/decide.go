// Package natscallout is the NATS auth service: the hub sends it the authorization of every
// connection its configuration lists no static user for (nats-server's auth callout, config mode),
// and it answers with a NATS user of that connection's own, or refuses it.
//
// A connection presents one of two credentials. A session sends its broker access token in the
// CONNECT's token field and, in its signature field, a proof of the key the token is bound to over
// the server's own per-connection nonce (brokertoken.VerifyConnectionProof); it is answered with
// its own delivery channel and nothing of another session's (natsgrant.Session). A service sends
// user "service" (natscred.ServiceUser) and its platform identity token as the password: a
// projected Kubernetes ServiceAccount token or a VM's Google identity token, which the broker
// already verifies for its own callers; it is answered with its kind's grant, for the account
// BROKER_NATS_SERVICES lists it under (ParseServices). Every other connection is refused.
//
// The service runs inside the secrets broker (cmd/broker), whose replicas each subscribe the
// authorization subject in one queue group, so a stopped replica delays no connection.
package natscallout

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"time"

	"github.com/sjawhar/envoy/internal/broker/machine"
	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/broker/projectaccess"
	"github.com/sjawhar/envoy/internal/brokertoken"
	"github.com/sjawhar/envoy/natscred"
	"github.com/sjawhar/envoy/natsgrant"
)

// Deps is what the service asks of the broker it runs in.
type Deps struct {
	// Introspect is what the broker knows of one of its access tokens: the provider's own
	// introspection, in the broker's process.
	Introspect func(ctx context.Context, token string) (oauthwire.Introspection, error)
	// ServiceToken verifies a service's platform identity token for the NATS audiences
	// (BROKER_NATS_K8S_AUDIENCE, BROKER_NATS_GOOGLE_AUDIENCE) and answers the account it names,
	// whether that is a Google account, and when the token was issued and expires.
	ServiceToken func(ctx context.Context, raw string) (account string, google bool, issued, expiry time.Time, err error)
	// Exchange is the projects the broker's own exchange list gives account, and false for an
	// account no entry lists (exchange.Set.ProjectsForAccount). Nil, with Access, holds no
	// service to its projects, and refuses every entry that is not an all-projects one.
	Exchange func(account string) ([]string, bool)
	// Access is Dispatch's answer about one project (projectaccess).
	Access func(ctx context.Context, project, person string) (projectaccess.Answer, error)
	// Now is time.Now unless a test replaces it.
	Now func() time.Time
	// Log records each refusal with the connection's host, never its credential. Nil logs
	// nothing.
	Log *slog.Logger
	// Metrics is the registry the service's decisions are counted on (NewMetrics); nil counts
	// nothing.
	Metrics *Metrics
}

func (d Deps) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

// Request is what the server asks about one connection, read from its authorization request.
type Request struct {
	// UserNkey is the user key the server made for this connection, which the answer's user
	// must name; ServerID is the server, which the answer's audience must name; ServerXKey is
	// the server's curve key, when it sealed the request.
	UserNkey, ServerID, ServerXKey string
	// Nonce is the nonce the server sent this connection, which a session's proof signs.
	Nonce string
	// Token and Sig are the CONNECT's token and signature fields: a session's access token
	// and its proof, which nats.go sends base64url-encoded.
	Token, Sig string
	// User and Password are the CONNECT's user and password: natscred.ServiceUser and a
	// service's platform identity token.
	User, Password string
	// Host is the client's address, for the log.
	Host string
}

// Decision is the user the service answers a connection with.
type Decision struct {
	// Name is the user's name, "session/<id>" or "<kind>/<machine id or account>", which the
	// server reports as the connection's authorized user (/connz).
	Name string
	// Grant is what the user may publish and subscribe.
	Grant natsgrant.Permissions
	// Expires is when the user expires: when the credential it was issued for does.
	Expires time.Time
}

// The credential kinds a request is decided as, the kind label of the decision counter.
const (
	credentialSession = "session"
	credentialService = "service"
	credentialNone    = "none"
)

// Refusal is a connection the service refuses: Reason is the one line the server is answered with
// and the broker logs, and names nothing of the credential; Kind is the credential it presented.
type Refusal struct {
	Kind, Reason string
}

func (r *Refusal) Error() string { return r.Reason }

func refuse(kind, reason string) *Refusal { return &Refusal{Kind: kind, Reason: reason} }

// credentialKind is which credential req presents: a session's token beside a proof and no user,
// a service's user and password, or neither.
func credentialKind(req Request) string {
	switch {
	case req.Token != "" && req.Sig != "" && req.User == "":
		return credentialSession
	case req.User == natscred.ServiceUser && req.Password != "":
		return credentialService
	}
	return credentialNone
}

// grantSource is where a decision's grants come from: natsgrant's, unless a test of the guard
// replaces them.
type grantSource struct {
	session func(sessionID string) (natsgrant.Permissions, error)
	service func(entry ServiceEntry) natsgrant.Permissions
}

var natsGrants = grantSource{session: natsgrant.Session, service: serviceGrant}

// serviceGrant is entry's kind's grant, before it is held to the entry's projects.
func serviceGrant(entry ServiceEntry) natsgrant.Permissions {
	switch entry.Kind {
	case KindDispatch:
		return natsgrant.Dispatch(entry.MachineID)
	case KindListener:
		return natsgrant.Listener(entry.MachineID)
	case KindDaemon:
		return natsgrant.Daemon()
	case KindMeasure:
		return natsgrant.Measure()
	}
	return natsgrant.Permissions{}
}

// Decide is the user the service answers req with, or the *Refusal it refuses it with. A session
// is answered with natsgrant.Session of the session its live token names, once its proof verifies
// over req's nonce; a service with its kind's grant, held to its projects (scopeGrant), once its
// platform token verifies and lives no longer than a token login may (an hour, beside the proof
// clock skew) and its account is listed in services. Whatever decided it, an answer whose grant
// names a system subject is refused (natsgrant.NamesSystemSubject).
func Decide(ctx context.Context, deps Deps, services []ServiceEntry, req Request) (Decision, error) {
	return decide(ctx, deps, services, req, natsGrants)
}

// decide is Decide on the grants of grants: decideCredential, then the guard.
func decide(ctx context.Context, deps Deps, services []ServiceEntry, req Request, grants grantSource) (Decision, error) {
	decision, err := decideCredential(ctx, deps, services, req, grants)
	if err != nil {
		return Decision{}, err
	}
	// The last guard, whatever branch decided it. The server merges a deny of the publish of
	// $SYS.REQ.USER.AUTH into every client of the callout account (nats-server server/auth.go,
	// mergeDenyPermissions), and that deny is publish only, so a grant that named a system
	// subject would give a connection a subscribe to the authorization requests that nothing
	// takes away.
	if entry, named := natsgrant.NamesSystemSubject(decision.Grant); named {
		return Decision{}, refuse(credentialKind(req), "grant names a system subject: "+entry)
	}
	return decision, nil
}

// decideCredential is the decision of the credential req presents, before the guard.
func decideCredential(ctx context.Context, deps Deps, services []ServiceEntry, req Request, grants grantSource) (Decision, error) {
	switch credentialKind(req) {
	case credentialSession:
		return decideSession(ctx, deps, req, grants)
	case credentialService:
		return decideService(ctx, deps, services, req, grants)
	}
	return Decision{}, refuse(credentialNone, "no credential this hub admits")
}

// sessionNamePrefix opens the name of a session's user.
const sessionNamePrefix = "session/"

func decideSession(ctx context.Context, deps Deps, req Request, grants grantSource) (Decision, error) {
	answer, err := deps.Introspect(ctx, req.Token)
	if err != nil {
		if ctx.Err() != nil {
			return Decision{}, budgetRefusal(credentialSession, "the token's introspection")
		}
		return Decision{}, refuse(credentialSession, "broker unreachable: the token's introspection failed")
	}
	caller, err := brokertoken.SessionCaller(answer)
	switch {
	case errors.Is(err, brokertoken.ErrNotActive):
		return Decision{}, refuse(credentialSession, "token not live")
	case errors.Is(err, brokertoken.ErrKeyMismatch):
		return Decision{}, refuse(credentialSession, "key mismatch")
	case err != nil:
		return Decision{}, refuse(credentialSession, "token is no session's credential")
	}
	proof, err := base64.RawURLEncoding.DecodeString(req.Sig)
	if err != nil {
		return Decision{}, refuse(credentialSession, "proof invalid: jws")
	}
	if err := brokertoken.VerifyConnectionProof(string(proof), req.Token, req.Nonce, answer.Thumbprint, deps.now()); err != nil {
		var claim *brokertoken.ProofClaimError
		if errors.As(err, &claim) {
			return Decision{}, refuse(credentialSession, "proof invalid: "+claim.Claim)
		}
		return Decision{}, refuse(credentialSession, "key mismatch")
	}
	if answer.ExpiresAt <= 0 {
		return Decision{}, refuse(credentialSession, "token names no expiry")
	}
	grant, err := grants.session(caller.Session)
	if err != nil {
		return Decision{}, refuse(credentialSession, "session id is not one subject token")
	}
	return Decision{Name: sessionNamePrefix + caller.Session, Grant: grant, Expires: time.Unix(answer.ExpiresAt, 0)}, nil
}

// longestServiceToken is the longest life a service's platform token may have from its issue: a
// token login's (machine.MaxTokenLoginLifetime), beside the proof clock skew, the bound the
// broker's API already holds its listed callers to.
const longestServiceToken = machine.MaxTokenLoginLifetime + brokertoken.ProofClockSkew

func decideService(ctx context.Context, deps Deps, services []ServiceEntry, req Request, grants grantSource) (Decision, error) {
	account, google, issued, expiry, err := deps.ServiceToken(ctx, req.Password)
	if err != nil {
		if ctx.Err() != nil {
			return Decision{}, budgetRefusal(credentialService, "the service token's verification")
		}
		return Decision{}, refuse(credentialService, "service token not verified")
	}
	if expiry.Sub(issued) > longestServiceToken {
		return Decision{}, refuse(credentialService, "token lives longer than an hour")
	}
	if !expiry.After(deps.now()) {
		return Decision{}, refuse(credentialService, "token not live")
	}
	var entry *ServiceEntry
	for index := range services {
		if services[index].Account == account && services[index].Google == google {
			entry = &services[index]
			break
		}
	}
	if entry == nil {
		return Decision{}, refuse(credentialService, "service not listed: "+account)
	}
	grant, err := scopeGrant(ctx, deps, *entry, grants.service(*entry))
	if err != nil {
		return Decision{}, err
	}
	return Decision{Name: entry.name(), Grant: grant, Expires: expiry}, nil
}
