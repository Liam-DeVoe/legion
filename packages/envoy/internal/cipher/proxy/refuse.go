// packages/envoy/internal/cipher/proxy/refuse.go
// How a refusal of a grant read is answered: the 407 that asks a command for its credential
// again, the one rule a failed read of a grant is answered by (grantRefusal), and the codes of
// those answers. The writer of every refusal of this port is refuse (bound.go).
package proxy

import (
	"errors"
	"net/http"

	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
	"github.com/sjawhar/envoy/internal/cipher/requests"
	"github.com/sjawhar/envoy/internal/cipher/secrets"
)

// refuseCredential answers 407 and asks for the credential again (refuse, with the challenge a
// client sends its credential again on). Nothing is tunnelled and nothing is dialled: a request
// that reaches here never left the broker.
func (s *Server) refuseCredential(w http.ResponseWriter, r *http.Request, c call, code, why string) {
	w.Header().Set("Proxy-Authenticate", `Basic realm="airlock"`)
	s.refuse(w, r, c, http.StatusProxyAuthRequired, code, why)
}

// refuseRead answers a proxy request whose grant read failed, by the one rule for a failed read
// (grantRefusal). A 407 is the broker's own words for why this credential opens nothing - an
// unknown or ended grant, an enrollment whose lease lapsed, a grant every one of whose names is
// withheld - and never the placeholder itself, which a command's own proxy error line would
// otherwise print and keep. A 503 is a read that did not happen, which grantRefusal has logged: a
// failure of the broker's, and no refusal of the caller. Any other status is the broker's own
// refusal of this grant, in its own words.
func (s *Server) refuseRead(w http.ResponseWriter, r *http.Request, c call, read string, err error) {
	status, code, why := s.grantRefusal(read, err)
	switch status {
	case http.StatusProxyAuthRequired:
		s.refuseCredential(w, r, c, code, why)
	case http.StatusServiceUnavailable:
		fail(w, http.StatusServiceUnavailable, code, why)
	default:
		s.refuse(w, r, c, status, code, why)
	}
}

// grantUnreadable is what a command is told when a read of its grant did not answer at all. It
// names nothing of the read that failed - a Postgres connect error carries the database's user,
// its name and its address - and it says the call can be made again, because nothing in such a
// failure says the grant has ended.
const grantUnreadable = "the broker could not read this grant just now; make the call again"

// The codes of a grant read's answers (grantRefusal): the API port's own code where it answers
// the same refusal of a grant's release (api's grantValues), and one of this port's own for a
// refusal the API port names no code for and for a read that did not happen.
const (
	codeGrantNotLive       = "GRANT_NOT_LIVE"
	codeAirlockNoNames     = "AIRLOCK_NO_NAMES"
	codeGrantChain         = "GRANT_CHAIN_INVALID"
	codeSecretNotInStore   = "SECRET_NOT_IN_STORE"
	codeMintFailed         = "MINT_FAILED"
	codeProfileMintRefused = "PROFILE_MINT_REFUSED"
	codeTargetRefused      = "TARGET_REFUSED"
	codeTargetRepositories = "TARGET_REPOSITORIES_REFUSED"
	codeValueShape         = "AIRLOCK_VALUE_SHAPE"
	codeGrantRefused       = "GRANT_REFUSED"
	codeGrantUnreadable    = "GRANT_UNREADABLE"
)

// grantRefusal is the status, the code and the words one failed grant read is answered with,
// and it is the one rule for every read of a grant this package makes. Three answers, by what
// the read said:
//
//   - A grant that has ended, and a grant holding no name this session may have, are the read's
//     own answer about that grant, so they are 407 in the broker's own words, which is what
//     tells a command to ask for a new grant.
//   - A mint the secret's own minter could not even ask for - the upstream did not answer, or
//     answered a 5xx, a 408 or a rate limit (secrets.ErrMintUpstream) - is a read that did not
//     happen, and it falls to the last answer below. It reaches here wrapped in ErrMintFailed,
//     which is a refusal of the package the line below asks, so it has to be read first: a
//     re-mint of a github-app, google-login or azure-login name behind the airlock fails this way
//     whenever its upstream has a bad minute, and answering that 403 would tell a command no retry
//     clears it and would close a WebSocket the grant still carries.
//   - Every other refusal the broker made about this grant - a chain that no longer verifies, a
//     secret the store no longer holds, a mint the secret's own minter refused, a profile's scope
//     that minter refused (secrets.ErrMintNarrowingRefused, which the release hands over as it
//     is), a target the live profile does not hold, a value whose shape the airlock cannot take
//     apart - is 403 in those words. No retry clears one of them, so telling a command to make
//     the call again would be untrue, and none of them carries anything of the store the read
//     went to. Which errors those are is the read's own package to say (requests.IsRefusal),
//     since a sentinel added there would be missed by a list kept here; the seven named here have
//     a code of their own, and any other is GRANT_REFUSED.
//   - A release the broker refused because it could not learn whether Dispatch calls the
//     session's person a restricted member (*projectaccess.MemberKindUncheckable) is 503 in its
//     own fixed words under the code the API port answers it with: nothing is released while
//     that cannot be told, and the call can be made again.
//   - Everything left is a read that did not happen - a database that did not answer, most of
//     them - and is 503 and grantUnreadable, with the error itself in the log, where it is
//     wanted: a failed read is no proof that a grant ended, and a database failover must not
//     send every agent in the middle of a call to ask for a grant it already holds, which on
//     the human tier is a person approving each one. Nor may the answer carry the read's own
//     words, which for a Postgres connect error are the database's user, name and address.
func (s *Server) grantRefusal(read string, err error) (int, string, string) {
	var uncheckable *projectaccess.MemberKindUncheckable
	switch {
	case errors.As(err, &uncheckable):
		if uncheckable.CallerEnded() {
			s.log.Info("airlock proxy: the caller ended before Dispatch said whether the person is a restricted member, so the release is refused",
				"read", read, "person", uncheckable.Person, "error", uncheckable.Err)
		} else {
			s.log.Error("airlock proxy: whether Dispatch calls a person a restricted member cannot be read, so the release is refused",
				"read", read, "person", uncheckable.Person, "error", uncheckable.Err)
		}
		return http.StatusServiceUnavailable, projectaccess.UncheckableCode, uncheckable.Error()
	case errors.Is(err, requests.ErrGrantNotLive):
		return http.StatusProxyAuthRequired, codeGrantNotLive, err.Error()
	case errors.Is(err, requests.ErrAirlockNoNames):
		return http.StatusProxyAuthRequired, codeAirlockNoNames, err.Error()
	case errors.Is(err, secrets.ErrMintUpstream):
		// The upstream, not the grant: this one falls through to the 503 below, ahead of the
		// refusal line, which would otherwise take it for one.
	case errors.Is(err, requests.ErrGrantChainInvalid):
		return http.StatusForbidden, codeGrantChain, err.Error()
	case errors.Is(err, requests.ErrSecretNotInStore):
		return http.StatusForbidden, codeSecretNotInStore, err.Error()
	case errors.Is(err, requests.ErrMintFailed):
		return http.StatusForbidden, codeMintFailed, err.Error()
	case errors.Is(err, secrets.ErrMintNarrowingRefused):
		return http.StatusForbidden, codeProfileMintRefused, err.Error()
	case errors.Is(err, requests.ErrTargetRepositoriesRefused):
		return http.StatusForbidden, codeTargetRepositories, err.Error()
	case errors.Is(err, requests.ErrTargetRefused):
		return http.StatusForbidden, codeTargetRefused, err.Error()
	case errors.Is(err, requests.ErrAirlockValueShape):
		return http.StatusForbidden, codeValueShape, err.Error()
	case requests.IsRefusal(err):
		return http.StatusForbidden, codeGrantRefused, err.Error()
	}
	s.log.Error("airlock proxy: a grant could not be read", "read", read, "error", err)
	return http.StatusServiceUnavailable, codeGrantUnreadable, grantUnreadable
}
