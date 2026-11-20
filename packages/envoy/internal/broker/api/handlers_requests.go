package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sjawhar/envoy/internal/broker/answer"
	"github.com/sjawhar/envoy/internal/broker/policy"
	"github.com/sjawhar/envoy/internal/broker/record"
	"github.com/sjawhar/envoy/internal/broker/requests"
	"github.com/sjawhar/envoy/internal/broker/secrets"
)

// gitHubAppFieldRemovedMessage is the refusal of a body carrying github_app, the narrowing a
// command line of an earlier release sends (refusedGitHubApp).
const gitHubAppFieldRemovedMessage = "github_app was removed: name a profile and narrow it with --target ACCOUNT[/REPOSITORY]"

// refusedGitHubApp answers 400 GITHUB_APP_FIELD_REMOVED, and reports true, for a body that
// carries a top-level github_app, whatever its value, null included, and whatever else the body
// holds; it reports false, with r.Body left to be read whole again, for every other body. It
// reads the body before the route's own reader (readJSON), which answers a field it does not take
// 400 INVALID_REQUEST on POST /v1/requests: agent-secrets of an earlier release reads that code as
// a broker from before github_app and sends the same request again without it, which would run
// its command with a token wider than the one it asked for. A body that is no JSON object, or past
// the size readJSON allows, is left to readJSON, which answers it as before.
func refusedGitHubApp(w http.ResponseWriter, r *http.Request) bool {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBodyBytes))
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(data), errorReader{err}))
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(data, &fields) != nil {
		return false
	}
	if _, carried := fields["github_app"]; !carried {
		return false
	}
	writeError(w, http.StatusBadRequest, "GITHUB_APP_FIELD_REMOVED", gitHubAppFieldRemovedMessage)
	return true
}

// errorReader answers err at its first read, nil err being io.EOF: what remains of a body
// refusedGitHubApp read, so readJSON meets the read's own error (a body past its size among them)
// where the body ended.
type errorReader struct{ err error }

func (e errorReader) Read([]byte) (int, error) {
	if e.err == nil {
		return 0, io.EOF
	}
	return 0, e.err
}

// createRequestBody is POST /v1/requests's exact shape: a signed request object plus one optional,
// unsigned field, session_id (wake-only). The v8 "secrets"/"reason"/"issue" top-level fields are
// gone — they now live inside the signed request object itself. A body carrying github_app, the
// narrowing an earlier release took, is refused 400 GITHUB_APP_FIELD_REMOVED (refusedGitHubApp).
type createRequestBody struct {
	// The session's signed request: a compact ES256 JWS, signed with the session's key and
	// addressed to the broker's public URL, naming the secrets (authorization_details) and the
	// reason.
	Request string `json:"request"`
	// Optional: the Envoy session the broker notifies if this request expires undecided.
	SessionID *string `json:"session_id"`
}

// createRequestResponse is POST /v1/requests's exact wire shape: request_id, state, secrets,
// grant_id, record_id, approver, lifetime_seconds, coalesced, detail, approve_url — nothing else.
// requests.Request itself carries additional fields (decided_at, decided_by) this route's contract
// does not define, so the handler below builds this dedicated response rather than marshaling the
// Request it gets back from Machine.Create directly.
type createRequestResponse struct {
	// The request's id, which GET /v1/requests/{id} and its cancel route take.
	RequestID string `json:"request_id"`
	// "granted", "pending" (a person must decide it) or "denied".
	State string `json:"state"`
	// How the policy decided each name asked for.
	Secrets []requests.SecretDecision `json:"secrets"`
	// Once granted, the grant to read the values from; null otherwise.
	GrantID *string `json:"grant_id"`
	// The credential-request record the approver decides; null when nobody needs to.
	RecordID *string `json:"record_id"`
	// Who decides it, as its record names them: a person's login (their lowercase email), or
	// "anyone" for a shared secret, which anyone signed in to the broker's approval page may
	// decide. While it is pending it is listed in that person's Dispatch Inbox, linking
	// approve_url; for "anyone", in the Inbox of the person responsible for the agent that asked
	// (the session's operator, a fleet machine's person, or a Legion pod's issue assignee), or in
	// everyone's when no such person is found. Null when the policy decided it at once, so no
	// record names one.
	Approver *string `json:"approver"`
	// How many seconds its grant lasts, as the signed request asked; or, when it asked for none, 12
	// hours; null when every name is tagged default-grant=session and the grant lasts until the
	// session ends. A reused or coalesced request answers
	// its own, which equals the one asked for. A client that asked for a lifetime and reads null,
	// or no such field from a broker before it, was not given that lifetime.
	LifetimeSeconds *int `json:"lifetime_seconds"`
	// True when the request joined an identical one this session already had pending.
	Coalesced bool `json:"coalesced,omitempty"`
	// The broker's one sentence about why it denied the request: the secret that refused it and
	// the rule that did, as "GCLOUD_DEMO: requester-not-listed", several joined with "; ". Null
	// when the broker recorded none — every request it did not deny, and a denial whose rule it
	// cannot name. A session reads it to learn that asking for that secret again will not help.
	Detail *string `json:"detail"`
	// While it is pending, its record's page on the broker, <BROKER_PUBLIC_URL>/approve/<record>,
	// where a person signed in to the broker decides it; absent when the policy decided it at once,
	// on a broker with BROKER_SIGNIN=off, which serves no page, and while the model judge holds
	// it, which judge_deciding says instead.
	ApproveURL string `json:"approve_url,omitempty"`
	// True while the model judge holds the request: its keys are ones the judge decides, and no
	// person is asked unless the judge cannot decide it, when the request comes back with its
	// approve_url. Absent for every other request.
	JudgeDeciding bool `json:"judge_deciding,omitempty"`
}

// createRequest requests secrets for the proof-verified caller, never for an enrollment named in
// the body: a session may only ever request secrets for itself. The request object itself (not
// this handler) names the secrets, the reason, and — implicitly, via the policy — the approver.
func (s *server) createRequest(w http.ResponseWriter, r *http.Request, enrollmentID string) {
	if refusedGitHubApp(w, r) {
		return
	}
	var body createRequestBody
	if !readJSON(w, r, &body, "INVALID_REQUEST") {
		return
	}
	req, err := s.deps.Machine.Create(r.Context(), enrollmentID, body.Request, derefOr(body.SessionID, ""))
	switch {
	case errors.Is(err, record.ErrRequestInvalid):
		writeError(w, http.StatusBadRequest, "REQUEST_INVALID", err.Error())
		return
	case errors.Is(err, requests.ErrMixedApprovers):
		writeError(w, http.StatusBadRequest, "MIXED_APPROVERS", err.Error())
		return
	case errors.Is(err, requests.ErrGrantRequiresLifetime):
		// A secret's max-grant tag bounds the lifetime its request may sign.
		writeError(w, http.StatusBadRequest, "GRANT_REQUIRES_LIFETIME", err.Error())
		return
	case errors.Is(err, requests.ErrRequestPending):
		writeError(w, http.StatusConflict, "REQUEST_PENDING", err.Error())
		return
	case errors.Is(err, requests.ErrTargetRefused):
		// The target the request's signed locations name is one its profile cannot serve: outside
		// the profile, on a name with no github block, or missing on a value-token profile of
		// several accounts. The message names the profile's accounts and repositories, so the
		// caller names a target inside them; nobody was asked and nothing was written.
		writeError(w, http.StatusBadRequest, "TARGET_REFUSED", err.Error())
		return
	case errors.Is(err, requests.ErrSessionIDInvalid):
		writeError(w, http.StatusBadRequest, "SESSION_ID_INPUT", err.Error())
		return
	case errors.Is(err, policy.ErrServedOnlyAsProfiles):
		// A source asked for by its own name, which profiles of other names use: the message
		// names them, so the caller asks for one of those instead. It is final, as UNKNOWN_SECRET
		// is, and asked before that code, since the policy's error is that one too.
		writeError(w, http.StatusForbidden, "SERVED_ONLY_AS_PROFILES", err.Error())
		return
	case errors.Is(err, policy.ErrProfileRefused):
		// A profile the policy refuses against its source, or a github-app secret no profile
		// names: the message gives the reason and its detail, which the profiles file or the
		// source's tags fix.
		writeError(w, http.StatusForbidden, "PROFILE_REFUSED", err.Error())
		return
	case errors.Is(err, policy.ErrUnknownSecret):
		writeError(w, http.StatusBadRequest, "UNKNOWN_SECRET", err.Error())
		return
	case errors.Is(err, policy.ErrPendingValue):
		// A secret `agent-secrets secret create` made and has not yet filled: not final, unlike
		// UNKNOWN_SECRET, so the caller may ask again once the owner stores the value.
		writeError(w, http.StatusBadRequest, "PENDING_VALUE", err.Error())
		return
	case errors.Is(err, secrets.ErrMintNarrowingRefused) || errors.Is(err, secrets.ErrMintNoInstallation):
		// Create's check mint refused a GitHub profile the request names before anybody was asked
		// to approve it: its permissions cut to what the installation holds leave none, GitHub
		// refused a repository it names, or the App has no installation on an account in effect.
		// The release route answers the first two alike and not the last: it wraps that refusal
		// in requests.ErrMintFailed and answers it 502 MINT_FAILED, as it answers every refused
		// mint. So this arm is the request route's own.
		writeError(w, http.StatusUnprocessableEntity, "PROFILE_MINT_REFUSED", err.Error())
		return
	case errors.Is(err, pgx.ErrNoRows):
		// The proof was verified moments ago against a live enrollment, but it was revoked (or
		// its lease lapsed) before Create read it or locked it to write — the same race
		// enroll.Renew guards against. The caller's proof is no longer good for anything.
		writeError(w, http.StatusUnauthorized, "PROOF_INVALID", "enrollment is not live")
		return
	case err != nil:
		writeInternal(w, "create request", err)
		return
	}
	writeJSON(w, http.StatusOK, createRequestResponse{
		RequestID:       req.ID,
		State:           req.State,
		Secrets:         req.Secrets,
		GrantID:         req.GrantID,
		RecordID:        req.RecordID,
		Approver:        req.Approver,
		LifetimeSeconds: req.LifetimeSeconds,
		Coalesced:       req.Coalesced,
		Detail:          req.Detail,
		ApproveURL:      s.pendingApproveURL(req),
		JudgeDeciding:   req.JudgeDeciding,
	})
}

// requestDecision is GET /v1/requests/{id}'s nested "decision" object: who decided the request
// and when.
type requestDecision struct {
	// Who decided it: the approver's login, "session:<enrollment id>" (the session cancelled it),
	// "launcher:<credential id>" (its launcher ended the session, or its operator ended its machine
	// login from one of their machines) or "broker" (the session's lease lapsed).
	By string `json:"by"`
	// When it was decided.
	At time.Time `json:"at"`
}

// requestStatusResponse is GET /v1/requests/{id}'s answer: state, grant_id, record_id, approver,
// lifetime_seconds, decided_at, decision, detail and, while it is pending, approve_url — nothing
// else.
type requestStatusResponse struct {
	// "pending", "granted", "denied", "expired" or "cancelled".
	State string `json:"state"`
	// Once granted, the grant to read the values from; null otherwise.
	GrantID *string `json:"grant_id"`
	// The credential-request record the approver decides; null when nobody needs to.
	RecordID *string `json:"record_id"`
	// Who decides it, as its record names them: a person's login (their lowercase email), or
	// "anyone" for a shared secret, which anyone signed in to the broker's approval page may
	// decide. The record fixes it when the request is made, so a later change to the secret's
	// owner does not move it, and it stays once the request is decided. Null when the policy
	// decided it at once, so no record names one.
	Approver *string `json:"approver"`
	// How many seconds its grant lasts: as the signed request asked, else 15 minutes when any name
	// is human tier and 12 hours otherwise; null for a
	// grant that lasts until the session ends (every name tagged default-grant=session).
	LifetimeSeconds *int `json:"lifetime_seconds"`
	// When it left "pending"; null while pending.
	DecidedAt *time.Time `json:"decided_at"`
	// Who decided it and when; null while pending, when the policy decided it at once, and when it
	// expired.
	Decision *requestDecision `json:"decision"`
	// The broker's one sentence about why it denied the request, as POST /v1/requests answers it:
	// the secret that refused it and the rule that did, as "GCLOUD_DEMO: requester-not-listed".
	// Null when the broker recorded none, a request it did not deny among them.
	Detail *string `json:"detail"`
	// While it is pending, its record's page on the broker, <BROKER_PUBLIC_URL>/approve/<record>,
	// where a person signed in to the broker decides it; absent once it is no longer pending, when
	// the policy decided it at once, on a broker with BROKER_SIGNIN=off, which serves no page, and
	// while the model judge holds it.
	ApproveURL string `json:"approve_url,omitempty"`
	// True while the model judge holds the request, as POST /v1/requests answers it; absent for
	// every other request.
	JudgeDeciding bool `json:"judge_deciding,omitempty"`
}

// grantValuesBody is POST /v1/grants/{id}/values's optional body: no body at all releases every
// value as the grant holds it, and so does one naming only what the session says of the release. A
// body carrying github_app, the narrowing an earlier release took, is refused 400
// GITHUB_APP_FIELD_REMOVED (refusedGitHubApp), and one the route cannot read 400 INVALID_RELEASE.
type grantValuesBody struct {
	// Optional: the command this release starts, as its argument vector. The broker logs it with
	// the grant's reason: its first 32 arguments, each cut to 256 bytes, with `[redacted]` in place
	// of each value of 8 bytes or more that the release hands out, each string of 8 bytes or more
	// in a stored JSON value, and each credential of a well-known shape. Any other secret in it is
	// logged as sent. It decides nothing about the release. A broker from before this field
	// refuses a body that holds it, as INVALID_NARROWING naming the unknown field, and releases
	// nothing.
	Command []string `json:"command"`
	// Optional: the reason this call of the session gave, which can differ from the reason of the
	// request the grant came of when the call got a live grant back. The broker logs it beside the
	// grant's reason where the two differ, held to a request's rules: the runes a request's
	// reason may not carry dropped and the rest cut to 400 runes. It decides nothing about the
	// release. A broker from before this field refuses a body that holds it, as
	// INVALID_NARROWING naming the unknown field, and releases nothing.
	Reason string `json:"reason"`
}

// grantValuesResponse is POST /v1/grants/{id}/values's answer.
type grantValuesResponse struct {
	// When the grant expires; the session can read the values again until then. Null for a grant
	// that lasts until the session ends.
	ExpiresAt *time.Time `json:"expires_at"`
	// Each granted name's value, read fresh from the secret store.
	Values map[string]string `json:"values"`
	// For each GitHub profile whose permissions this release cut to what an installation holds,
	// by profile name and then account, every entry the cut left out: "<permission>" for a
	// permission the installation holds none of, "<permission>:<level>" for one it holds at a
	// lower level, which the token holds at that level instead. Absent when no cut left anything
	// out.
	GitHubLeftOut map[string]map[string][]string `json:"github_left_out,omitempty"`
	// How to reach the values this release did not hand over: the names the airlock holds back
	// are not in "values" at all, and this says where the proxy that holds them is. Absent when
	// no granted name is behind the airlock, which is every release of a broker with no rules
	// file.
	Airlock *airlockRelease `json:"airlock,omitempty"`
}

// airlockRelease is how a session reaches the values the airlock holds back: the proxy it sends
// their requests through, the certificate authority that proxy's certificates are signed by, the
// password every connection to it opens with, and one entry per name held back.
type airlockRelease struct {
	// The URL the command points HTTPS_PROXY at (BROKER_AIRLOCK_PROXY_URL).
	ProxyURL string `json:"proxy_url"`
	// The PEM certificate the command must trust to reach the proxy, which signs a certificate
	// for each host it carries a request to.
	CACertificate string `json:"ca_certificate"`
	// The password every proxy connection authenticates with: one of this grant's own
	// placeholders, the first (name, slot) pair in sorted order.
	ProxyPassword string `json:"proxy_password"`
	// Each name the airlock holds back, by the name the command knows it as.
	Names map[string]airlockName `json:"names"`
}

// airlockName is one name the airlock holds back.
type airlockName struct {
	// What the command's environment variable of that name holds: the released shape of the
	// secret with a placeholder wherever its value would be.
	Value string `json:"value"`
	// Each slot of the secret and the placeholder standing for it, so a command that takes one
	// value of the secret apart knows which placeholder is which.
	Slots map[string]string `json:"slots"`
	// The hosts the secret's airlock entry allows, which the command keeps off its NO_PROXY so
	// their requests reach the proxy.
	Hosts []string `json:"hosts"`
}

// requestWaitStep is how often a held read reads its request again.
const requestWaitStep = 500 * time.Millisecond

// readRequest answers one request to any enrollment of the principal that made it: the same
// person, the same Legion claim, the same machine (requests.Machine.OwnerAndState), and a request
// of an enrollment that records no owner to that enrollment alone. Everyone else is 403 NOT_YOURS.
// With ?wait=N, N a whole number of seconds from 1 to record.MaxRequestWaitSeconds, it holds the
// read while the request is pending (readHeld) and answers the first state that is not pending, or
// pending once N seconds have passed since the request reached the broker
// (answer.Recorder.Arrived), so a slow proof check shortens the hold rather than lengthening it.
// The state it reads is the database's, so a decision made on either broker process — the
// approval page's, the model judge's, the sweeper's expiry, the session's own cancel — is seen
// within one step of its commit. Any other wait is refused REQUEST_WAIT_INPUT before any read; an
// absent or empty one answers at once, as before the parameter.
//
// A store that fails is 503 DATABASE_UNAVAILABLE, logged at ERROR and counted failed, which
// agent-secrets reads as a failed read and waits through (retryable), where a 500 would end its
// wait with nothing run while the request stays pending. A held read whose caller went away before
// the broker owed its answer, before its wait ran out or in the one step (half a second) after it,
// is 503 REQUEST_ENDED, logs nothing, as the reread's does, and is counted cancelled
// (answer.Recorder.NoteCallerEnded). Any other read that ends early is a store failure: a read
// with no wait owes its answer at once, and the pool sets no statement timeout, so a store that
// stalls holds a read until its caller gives up or the request deadline ends it. That stall is
// logged and counted failed, as it is when it holds the proof check. The wait counts from the
// request's arrival, so a caller that goes away while its proof is still being checked leaves
// inside its wait. One that leaves while the broker looks its enrollment up or records the
// proof's jti does not reach this handler: the proof check ends instead, as `broker: verify proof
// failed`, a failed 503 (sessionCaller). One that leaves during Replay's prune of expired jtis,
// whose error the check ignores, reaches it, and its first read fails inside the window, so it is
// REQUEST_ENDED and counted cancelled, by the rule above.
func (s *server) readRequest(w http.ResponseWriter, r *http.Request, enrollmentID string) {
	ctx := r.Context()
	id, ok := pathUUID(w, r, "id", "REQUEST_ID_INPUT", "request")
	if !ok {
		return
	}
	wait, ok := requestWait(w, r.URL.Query().Get("wait"))
	if !ok {
		return
	}
	recorder := answer.FromContext(ctx)
	deadline := recorder.Arrived().Add(wait)
	req, err := s.readHeld(ctx, id, enrollmentID, deadline)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such request")
		return
	case errors.Is(err, requests.ErrNotYours):
		writeError(w, http.StatusForbidden, "NOT_YOURS", err.Error())
		return
	case err != nil && wait > 0 && errors.Is(ctx.Err(), context.Canceled) && time.Now().Before(deadline.Add(requestWaitStep)):
		recorder.NoteCallerEnded()
		writeError(w, http.StatusServiceUnavailable, "REQUEST_ENDED", "the request ended before the read finished; send it again")
		return
	case err != nil:
		writeUnavailable(w, "DATABASE_UNAVAILABLE", "read request", err)
		return
	}
	resp := requestStatusResponse{
		State: req.State, GrantID: req.GrantID, RecordID: req.RecordID, Approver: req.Approver, LifetimeSeconds: req.LifetimeSeconds,
		DecidedAt: req.DecidedAt, Detail: req.Detail, ApproveURL: s.pendingApproveURL(req), JudgeDeciding: req.JudgeDeciding,
	}
	if req.DecidedBy != nil && req.DecidedAt != nil {
		resp.Decision = &requestDecision{By: *req.DecidedBy, At: *req.DecidedAt}
	}
	writeJSON(w, http.StatusOK, resp)
}

// requestWait reads GET /v1/requests/{id}'s ?wait: 0 for none, else the wait it names, refusing
// anything but a whole number of seconds from 1 to record.MaxRequestWaitSeconds.
func requestWait(w http.ResponseWriter, raw string) (time.Duration, bool) {
	if raw == "" {
		return 0, true
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 1 || seconds > record.MaxRequestWaitSeconds {
		writeError(w, http.StatusBadRequest, "REQUEST_WAIT_INPUT", fmt.Sprintf("wait is a whole number of seconds from 1 to %d", record.MaxRequestWaitSeconds))
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

// readHeld reads request id for enrollmentID. It reads the caller's owner once
// (Machine.EnrollmentOwner), then the request's owner, state and whether the model judge holds it
// (Machine.OwnerAndState, one statement), refusing requests.ErrNotOwner when the request's owner is
// not the caller's; while the state is pending and deadline has not passed, it reads them again
// every requestWaitStep; then it reads the whole request (Machine.Get) once. A request the judge
// held at the first read and holds no more has been handed to its person, still pending: that ends
// the hold too, so the caller learns at once who decides it now and where. A deadline already
// passed is the one read. The owner is checked at every step, so a request that moves to an
// enrollment of another owner while the read is held is refused at the next step, as a new read of
// it is. Any other error is the store's, or ctx's when ctx ended first.
func (s *server) readHeld(ctx context.Context, id, enrollmentID string, deadline time.Time) (requests.Request, error) {
	caller, err := s.deps.Machine.EnrollmentOwner(ctx, enrollmentID)
	if err != nil {
		return requests.Request{}, fmt.Errorf("read the caller's owner: %v", err)
	}
	first, heldByJudge := true, false
	for {
		owner, state, judging, err := s.deps.Machine.OwnerAndState(ctx, id)
		if err != nil {
			return requests.Request{}, err
		}
		if owner != caller {
			return requests.Request{}, requests.ErrNotOwner
		}
		if first {
			first, heldByJudge = false, judging
		}
		left := time.Until(deadline)
		if state != "pending" || left <= 0 || (heldByJudge && !judging) {
			return s.deps.Machine.Get(ctx, id)
		}
		select {
		case <-ctx.Done():
			return requests.Request{}, ctx.Err()
		case <-time.After(min(requestWaitStep, left)):
		}
	}
}

func (s *server) cancelRequest(w http.ResponseWriter, r *http.Request, enrollmentID string) {
	id, ok := pathUUID(w, r, "id", "REQUEST_ID_INPUT", "request")
	if !ok {
		return
	}
	err := s.deps.Machine.Cancel(r.Context(), id, enrollmentID)
	switch {
	case errors.Is(err, requests.ErrNotYours):
		writeError(w, http.StatusForbidden, "NOT_YOURS", err.Error())
		return
	case errors.Is(err, requests.ErrTerminal):
		writeError(w, http.StatusConflict, "REQUEST_TERMINAL", err.Error())
		return
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such request")
		return
	case err != nil:
		writeInternal(w, "cancel request", err)
		return
	}
	writeJSON(w, http.StatusOK, stateResponse{State: "cancelled"})
}

func (s *server) grantValues(w http.ResponseWriter, r *http.Request, enrollmentID string) {
	id, ok := pathUUID(w, r, "id", "GRANT_ID_INPUT", "grant")
	if !ok {
		return
	}
	var body grantValuesBody
	if r.ContentLength != 0 && (refusedGitHubApp(w, r) || !readJSON(w, r, &body, "INVALID_RELEASE")) {
		return
	}
	release, err := s.deps.Machine.Values(r.Context(), id, enrollmentID, requests.ReleaseCall{Command: body.Command, Reason: body.Reason})
	switch {
	case errors.Is(err, requests.ErrNotYours):
		writeError(w, http.StatusForbidden, "NOT_YOURS", err.Error())
		return
	case errors.Is(err, requests.ErrGrantNotLive):
		writeError(w, http.StatusForbidden, "GRANT_NOT_LIVE", err.Error())
		return
	case errors.Is(err, requests.ErrGrantChainInvalid):
		writeError(w, http.StatusForbidden, "GRANT_CHAIN_INVALID", err.Error())
		return
	case errors.Is(err, requests.ErrSecretNotInStore):
		writeError(w, http.StatusNotFound, "SECRET_NOT_IN_STORE", err.Error())
		return
	case errors.Is(err, requests.ErrMintFailed):
		// The type's upstream (GitHub or Google) refused or failed a mint the profile's scope is
		// not at fault for: an App with no installation, a Google login that needs a new sign-in,
		// an upstream that could not be asked. Its message says why and never holds the stored
		// value.
		writeError(w, http.StatusBadGateway, "MINT_FAILED", err.Error())
		return
	case errors.Is(err, secrets.ErrMintNarrowingRefused):
		// The release's mint refused a GitHub profile's scope: its permissions cut to what the
		// installation holds leave none, or GitHub refused a repository it names. The message
		// says which; the profiles file or the caller's target fixes it, and nothing was released.
		writeError(w, http.StatusUnprocessableEntity, "PROFILE_MINT_REFUSED", err.Error())
		return
	case err != nil:
		writeInternal(w, "read grant values", err)
		return
	}
	values := release.Values
	if values == nil {
		values = map[string]string{}
	}
	answer := grantValuesResponse{ExpiresAt: release.Expires, Values: values, GitHubLeftOut: release.GitHubLeftOut}
	if release.Airlock != nil {
		answer.Airlock = &airlockRelease{
			ProxyURL:      release.Airlock.ProxyURL,
			CACertificate: release.Airlock.CACertificate,
			ProxyPassword: release.Airlock.ProxyPassword,
			Names:         map[string]airlockName{},
		}
		for name, held := range release.Airlock.Names {
			answer.Airlock.Names[name] = airlockName{Value: held.Value, Slots: held.Slots, Hosts: held.Hosts}
		}
	}
	writeJSON(w, http.StatusOK, answer)
}

// revokeGrant ends a grant for the session that holds it — session proof only, per the shared
// broker contract (human revocation is the UI's revoke-by-approver route, which Dispatch's server
// calls with the human's login).
func (s *server) revokeGrant(w http.ResponseWriter, r *http.Request, enrollmentID string) {
	id, ok := pathUUID(w, r, "id", "GRANT_ID_INPUT", "grant")
	if !ok {
		return
	}
	err := s.deps.Machine.RevokeGrant(r.Context(), id, enrollmentID)
	switch {
	case errors.Is(err, requests.ErrNotYours):
		writeError(w, http.StatusForbidden, "NOT_YOURS", err.Error())
		return
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such grant")
		return
	case err != nil:
		writeInternal(w, "revoke grant", err)
		return
	}
	writeJSON(w, http.StatusOK, stateResponse{State: "revoked"})
}
