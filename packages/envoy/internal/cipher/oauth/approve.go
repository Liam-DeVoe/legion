package oauth

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"time"

	"authelia.com/provider/oauth2"
	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/cipher/oauthstore"
	"github.com/sjawhar/envoy/internal/cipher/oauthwire"
	"github.com/sjawhar/envoy/internal/cipher/placeholder"
)

// formRequestedTokenType is the request's requested_token_type (RFC 8693 Section 2.1), which the
// library reads too: absent, it is the access token.
const formRequestedTokenType = "requested_token_type"

// ApproveGrantMaxLifetime is the longest an approve grant lasts: long enough for a session to make
// the approvals one run of an agent needs, short enough that a placeholder a session holds opens
// the proxy for no longer than that.
const ApproveGrantMaxLifetime = 15 * time.Minute

// ApproveScope is the one OAuth scope of an approve grant and of the token the broker mints for it.
const ApproveScope = oauthwire.ScopeApprove

// ApproveSettings is the approve capability (requests.MintedKind DISPATCH_APPROVE_TOKEN) as the
// token exchange grants it: the capability's name and word, the digest of the dpop block and
// private destination every grant is made under (config.MintedEntryDigest), and what the session
// is handed beside its placeholder - where Cipher's proxy is, the certificate authority to trust
// it by, and the one host the capability's dpop block binds.
type ApproveSettings struct {
	Name, Word    string
	EntryDigest   string
	ProxyURL      string
	CACertificate string
	Host          string
}

// approveGrantLifetime is how long an approve grant lasts: ApproveGrantMaxLifetime, or the access
// lifetime when that is shorter, so a grant never outlives the tokens this broker mints.
func (p *Provider) approveGrantLifetime() time.Duration {
	if p.accessLifetime > 0 && p.accessLifetime < ApproveGrantMaxLifetime {
		return p.accessLifetime
	}
	return ApproveGrantMaxLifetime
}

// grantApprove answers a trade that asked for the proxy placeholder, on the exchange's open
// transaction txCtx. Only a person's sign-in is granted it, and only by a broker that serves the
// approve capability: every other trade is refused invalid_request, the subject token being sound
// and the request asking for what its identity is not given. The grant holds the session the
// exchange decided - its subject, principal and extra claims, the person, the project scope person
// and no projects - with the scope approve alone, the sign-in's credential, which ends it when
// revoked, and the capability's entry digest. Nobody approves it: the person's sign-in is its
// authority. The session name is claimed until the grant's end, the transaction commits, and the
// answer is the placeholder as access_token, its type as issued_token_type, the token_type N_A of
// a token that is no access token (RFC 8693 Section 2.2.1), the seconds the grant lasts, the
// grant's id and the proxy release a session runs with, with no-store. The library mints
// nothing, and no approve-capable token ever leaves the broker.
func (p *Provider) grantApprove(ctx, txCtx context.Context, w http.ResponseWriter, request oauth2.AccessRequester, session *oauthstore.ExchangeSession, subjectTokenType, credentialID string, committed *bool) {
	if subjectTokenType != oauthwire.SubjectTokenTypePerson || p.approve == nil || credentialID == "" {
		p.writeError(ctx, w, request, oauth2.ErrInvalidRequest.WithHint("Only a person's sign-in is traded for the approve capability's proxy placeholder, on a broker that serves it."))
		return
	}
	person, _ := session.Extra[ClaimPerson].(string)
	if person == "" {
		p.writeError(ctx, w, request, oauth2.ErrInvalidRequest.WithHint("The sign-in names no person to approve as."))
		return
	}
	held, err := placeholder.Mint(placeholder.MintedPrefix(p.approve.Word))
	if err != nil {
		p.writeError(ctx, w, request, oauth2.ErrServerError.WithWrap(err).WithDebugError(err))
		return
	}
	lifetime := p.approveGrantLifetime()
	expires := time.Now().UTC().Add(lifetime).Truncate(time.Microsecond)
	extra := maps.Clone(session.Extra)
	extra[ClaimProjectScope] = oauthwire.ProjectScopePerson
	delete(extra, ClaimProjects)
	sessionName, _ := extra[ClaimSession].(string)
	grant, err := p.store.CreateApproveGrant(txCtx, oauthstore.MintedGrant{
		ID: uuid.NewString(), Subject: session.Subject, Session: sessionName, Principal: session.Principal,
		Scopes: []string{ApproveScope}, Extra: extra, ExpiresAt: expires, EntryDigest: p.approve.EntryDigest,
	}, credentialID, held)
	if err != nil {
		p.writeError(ctx, w, request, oauth2.ErrServerError.WithWrap(err).WithDebugError(err))
		return
	}
	// The session name is held until the grant's end, as a token's claim holds it until the
	// token's end (claimSession reads the session's access-token expiry).
	session.SetExpiresAt(oauth2.AccessToken, expires)
	if _, _, err := p.claimSession(txCtx, session); err != nil {
		p.writeError(ctx, w, request, err)
		return
	}
	if err := p.store.Commit(txCtx); err != nil {
		p.writeError(ctx, w, request, oauth2.ErrServerError.WithWrap(err).WithDebugError(err))
		return
	}
	*committed = true
	answer := map[string]any{
		"access_token":         held,
		"issued_token_type":    oauthwire.TokenTypeProxyPlaceholder,
		"token_type":           "N_A",
		"expires_in":           int64(lifetime / time.Second),
		oauthwire.ExtraGrantID: p.approve.Word + ":" + grant.ID,
		oauthwire.ExtraProxy: oauthwire.AirlockRelease{
			ProxyURL: p.approve.ProxyURL, CACertificate: p.approve.CACertificate, ProxyPassword: held,
			Names: map[string]oauthwire.AirlockName{p.approve.Name: {Value: held, Slots: map[string]string{"value": held}, Hosts: []string{p.approve.Host}}},
		},
	}
	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(answer)
}
