package main

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/cipher/proof"
	"github.com/sjawhar/envoy/internal/cipher/record"
	"github.com/sjawhar/envoy/internal/oidc/oidctest"
)

// podBroker speaks HTTP to a running broker binary at base as a service and its pods do: a
// service logs in with its token, its launcher credential enrolls pods that prove their accounts
// with projected tokens the issuer signs with key for audience, and a pod asks for a secret and
// reads a grant's values. Each step logs its request and response, and fails t when the broker
// answers outside the protocol.
type podBroker struct {
	t        *testing.T
	base     string
	issuer   *oidctest.Issuer
	key      *oidctest.Key
	audience string
}

// launcher is a service's launcher credential and the key that proves it.
type launcher struct {
	key          *ecdsa.PrivateKey
	credentialID string
}

// pod is an enrolled pod's runtime ID, enrollment and the key that proves it.
type pod struct {
	runtimeID    string
	key          *ecdsa.PrivateKey
	enrollmentID string
}

// requestReply is the broker's answer to POST /v1/requests.
type requestReply struct {
	State    string          `json:"state"`
	GrantID  *string         `json:"grant_id"`
	RecordID *string         `json:"record_id"`
	Approver json.RawMessage `json:"approver"`
}

// logIn logs service in with a token for account and returns its launcher credential.
func (b podBroker) logIn(service, account string) launcher {
	b.t.Helper()
	key := proofKey(b.t)
	signing := time.Now()
	loginRequest, err := record.Sign(key, b.base, []record.AuthorizationDetail{
		{Type: record.KindLauncherCredential, Identifier: "example-host-" + service, Service: service},
	}, "", "", nil, signing)
	if err != nil {
		b.t.Fatal(err)
	}
	status, answer := b.signedPost(signing, "/v1/launcher-credentials/token", nil, map[string]any{
		"token":   b.issuer.Mint(b.t, b.key, b.issuer.Claims(account, b.audience)),
		"request": loginRequest,
	})
	var login struct {
		CredentialID string `json:"credential_id"`
	}
	b.decode(answer, &login)
	if status != http.StatusOK || login.CredentialID == "" {
		b.t.Fatalf("%s's token login = %d %s, want 200 with a credential_id", service, status, answer)
	}
	return launcher{key: key, credentialID: login.CredentialID}
}

// enrolPod has by's credential enrol the pod runtimeID, proving with its projected token that it
// runs as account, under the session name sessionID, "" for none: the request's session_id is a
// pointer, so an empty name and no name differ, and "" sends no name at all. A name the pod states
// is held under its account's principal (exchange.AccountPrincipal), as the Legion daemon enrols a
// role's pod, and it is the name a grant follows. It answers the pod, holding the enrollment the
// broker named on a 201, beside the broker's status and body, so a caller drives the refusal as
// well as the enrollment.
func (b podBroker) enrolPod(by launcher, runtimeID, account, sessionID string) (pod, int, []byte) {
	b.t.Helper()
	key := proofKey(b.t)
	thumbprint, err := proof.Thumbprint(&key.PublicKey)
	if err != nil {
		b.t.Fatal(err)
	}
	claims := b.issuer.Claims(account, b.audience)
	claims["kubernetes.io"] = map[string]any{"pod": map[string]any{"uid": runtimeID}}
	signing := time.Now()
	signed, err := proof.SignLauncher(by.key, by.credentialID, http.MethodPost, b.base+"/v1/enrollments", signing)
	if err != nil {
		b.t.Fatal(err)
	}
	body := map[string]any{
		"kind": "pod", "runtime_id": runtimeID, "thumbprint": thumbprint, "pod_token": b.issuer.Mint(b.t, b.key, claims),
	}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	status, answer := b.signedPost(signing, "/v1/enrollments", map[string]string{"Proof": signed}, body)
	var enrolled struct {
		EnrollmentID string `json:"enrollment_id"`
	}
	if status == http.StatusCreated {
		b.decode(answer, &enrolled)
	}
	return pod{runtimeID: runtimeID, key: key, enrollmentID: enrolled.EnrollmentID}, status, answer
}

// enroll is enrolPod held to the answer a pod must get: 201 and an enrollment its own key signs
// for, which is what every later secrets call presents.
func (b podBroker) enroll(by launcher, runtimeID, account, sessionID string) pod {
	b.t.Helper()
	enrolled, status, answer := b.enrolPod(by, runtimeID, account, sessionID)
	if status != http.StatusCreated || enrolled.enrollmentID == "" {
		b.t.Fatalf("enroll %s as %s under session %q = %d %s, want 201 with an enrollment_id", runtimeID, account, sessionID, status, bytes.TrimSpace(answer))
	}
	return enrolled
}

// askRaw has p request the agent secret secretName and answers the broker's status and body,
// for a caller proving that a session whose enrollment has ended reaches no secret at all. POST
// /v1/requests holds the request open after its proof check (heldRequestWait), so it goes out on
// held, with no deadline from the signing.
func (b podBroker) askRaw(p pod, secretName string) (int, []byte) {
	b.t.Helper()
	request, err := record.Sign(p.key, b.base, []record.AuthorizationDetail{
		{Type: record.KindAgentSecret, Identifier: secretName, Actions: []string{"inject"}},
	}, p.runtimeID+" needs "+secretName, "", nil, time.Now())
	if err != nil {
		b.t.Fatal(err)
	}
	return b.held(p, "/v1/requests", map[string]any{"request": request, "session_id": nil})
}

// ask has p request the agent secret secretName and returns the broker's reply.
func (b podBroker) ask(p pod, secretName string) requestReply {
	b.t.Helper()
	status, answer := b.askRaw(p, secretName)
	if status != http.StatusOK {
		b.t.Fatalf("%s's POST /v1/requests for %s = %d %s, want 200", p.runtimeID, secretName, status, answer)
	}
	var reply requestReply
	b.decode(answer, &reply)
	return reply
}

// values has p read the values of its grant grantID.
func (b podBroker) values(p pod, grantID string) map[string]string {
	b.t.Helper()
	status, answer := b.signed(p, "/v1/grants/"+grantID+"/values", nil)
	if status != http.StatusOK {
		b.t.Fatalf("%s's grant values = %d %s, want 200", p.runtimeID, status, answer)
	}
	var values struct {
		Values map[string]string `json:"values"`
	}
	b.decode(answer, &values)
	return values.Values
}

// signed POSTs body to path with p's proof of its enrollment, for a route that answers at once
// (POST /v1/grants/{id}/values, internal/cipher/api/handlers_requests.go's grantValues; POST
// /v1/enrollments/{id}/renew, handlers_enroll.go's renewEnrollment): the request is given up
// brokerRequestWait after the proof's signing (signedPost).
func (b podBroker) signed(p pod, path string, body any) (int, []byte) {
	b.t.Helper()
	signing := time.Now()
	proofHeader, err := proof.Sign(p.key, p.enrollmentID, http.MethodPost, b.base+path, signing)
	if err != nil {
		b.t.Fatal(err)
	}
	return b.signedPost(signing, path, map[string]string{"Proof": proofHeader}, body)
}

// held is signed for a route that holds the request open after its proof check
// (heldRequestWait): no deadline from the signing, on heldClient.
func (b podBroker) held(p pod, path string, body any) (int, []byte) {
	b.t.Helper()
	proofHeader, err := proof.Sign(p.key, p.enrollmentID, http.MethodPost, b.base+path, time.Now())
	if err != nil {
		b.t.Fatal(err)
	}
	return b.postOn(heldClient, time.Time{}, path, map[string]string{"Proof": proofHeader}, body)
}

// post is signedPost for a request carrying nothing the test signed.
func (b podBroker) post(path string, auth map[string]string, body any) (int, []byte) {
	b.t.Helper()
	return b.signedPost(time.Time{}, path, auth, body)
}

// signedPost is postOn for a route that answers at once, on brokerClient: signing is the moment
// the caller began signing what the request carries, zero for a request carrying no signature,
// and the request is given up brokerRequestWait after it (signedContext).
func (b podBroker) signedPost(signing time.Time, path string, auth map[string]string, body any) (int, []byte) {
	b.t.Helper()
	return b.postOn(brokerClient, signing, path, auth, body)
}

// postOn POSTs body as JSON to path with the auth headers on client, logs the exchange and
// returns the status and response body. A non-zero signing gives the request up
// brokerRequestWait after it (signedContext); a zero one sets no deadline but client's.
func (b podBroker) postOn(client *http.Client, signing time.Time, path string, auth map[string]string, body any) (int, []byte) {
	b.t.Helper()
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			b.t.Fatal(err)
		}
	}
	ctx, cancel := signedContext(signing)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, b.base+path, bytes.NewReader(payload))
	if err != nil {
		b.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range auth {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		b.t.Fatalf("POST %s: %v", path, err)
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(response.Body)
	if err != nil {
		b.t.Fatal(err)
	}
	b.t.Logf("> POST %s\n< %d %s", path, response.StatusCode, bytes.TrimSpace(answer))
	return response.StatusCode, answer
}

// decode unmarshals the JSON answer into into, failing t if it is not.
func (b podBroker) decode(answer []byte, into any) {
	b.t.Helper()
	if err := json.Unmarshal(answer, into); err != nil {
		b.t.Fatalf("decode %s: %v", answer, err)
	}
}

// enrolSession has the launcher credential credentialID, held by launcherKey, enrol a session of
// its own: the step a machine takes with the credential its token exchange minted, whose key is
// the key its DPoP proof presented. It answers the session key it generated and the broker's
// status and body, so a caller can drive the refusal as well as the enrollment.
func (b podBroker) enrolSession(launcherKey *ecdsa.PrivateKey, credentialID, runtimeID string) (*ecdsa.PrivateKey, int, []byte) {
	b.t.Helper()
	key := proofKey(b.t)
	thumbprint, err := proof.Thumbprint(&key.PublicKey)
	if err != nil {
		b.t.Fatal(err)
	}
	signing := time.Now()
	signed, err := proof.SignLauncher(launcherKey, credentialID, http.MethodPost, b.base+"/v1/enrollments", signing)
	if err != nil {
		b.t.Fatal(err)
	}
	status, answer := b.signedPost(signing, "/v1/enrollments", map[string]string{"Proof": signed}, map[string]any{
		"kind": "session", "runtime_id": runtimeID, "thumbprint": thumbprint,
	})
	return key, status, answer
}

// enrolMachine is enrolSession held to the answer a machine must get: 201 and an enrollment its
// own key signs for, which is what every later secrets call presents.
func (b podBroker) enrolMachine(launcherKey *ecdsa.PrivateKey, credentialID, runtimeID string) pod {
	b.t.Helper()
	key, status, answer := b.enrolSession(launcherKey, credentialID, runtimeID)
	var enrolled struct {
		EnrollmentID string `json:"enrollment_id"`
	}
	b.decode(answer, &enrolled)
	if status != http.StatusCreated || enrolled.EnrollmentID == "" {
		b.t.Fatalf("the machine's session enrollment under credential %s = %d %s, want 201 with an enrollment_id", credentialID, status, answer)
	}
	return pod{runtimeID: runtimeID, key: key, enrollmentID: enrolled.EnrollmentID}
}
