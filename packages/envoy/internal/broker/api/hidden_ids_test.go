// hidden_ids_test.go: the routes that take a credential-request record id or a grant id from the
// listed caller that relays a person (Dispatch's server), or from a person's own machine login,
// answer a person the person rule hides the record or grant from (requests.Machine.HideUnseen:
// here, one whose reason names an issue of the private project PRIV) exactly as they answer an
// id nothing has, the same status, code and body, and write nothing; while Dispatch cannot say
// whether the person may see it, they answer 503 PROJECT_ACCESS_UNAVAILABLE and write nothing. A
// person on PRIV's list gets what they got before.
package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/broker/api"
	"github.com/sjawhar/envoy/internal/broker/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/broker/record"
)

const (
	// hiddenOutsider is on no list of the Dispatch these tests read.
	hiddenOutsider = "mallory@example.com"
	// hiddenInsider is on PRIV's list, and approves and operates nothing here.
	hiddenInsider = "carol@example.com"
	// privReason names an issue of PRIV, so a request signed with it is PRIV's to everyone the
	// person rule is read for (requests.Machine.MaySee).
	privReason = "PRIV-12: rotate the deploy key"
	// elsewhere is a broker address other than the test server's.
	elsewhere = "https://the-old-public-url.example"
)

// missingRecord is a record id no record has.
var missingRecord = strings.Repeat("0", 64)

// privDispatch is a Dispatch stand-in holding PRIV, private, owned by testApprover, with carol on
// its list.
func privDispatch(t *testing.T) *projectaccesstest.Server {
	t.Helper()
	return projectaccesstest.NewServer(t, map[string]projectaccesstest.Row{
		"PRIV": {Visibility: "private", Owner: testApprover, Listed: []string{hiddenInsider}},
	}, nil)
}

// hiddenServer is a test server reading privDispatch through a Swappable, so a test can take
// Dispatch down (downDispatch) and bring it back.
func hiddenServer(t *testing.T) (*testServer, *projectaccesstest.Swappable) {
	t.Helper()
	access := &projectaccesstest.Swappable{}
	access.Serve(privDispatch(t))
	return newTestServerWith(t, func(d *api.Deps) { d.Machine.Projects = access }), access
}

// askPrivate has a session of testApprover's, on a box of its own, ask for SHARED_KEY with
// privReason, and answers the request it opened, pending on a record whose approver is anyone.
func (ts *testServer) askPrivate(t *testing.T, box string) wireCreateRequestResponse {
	t.Helper()
	enrollmentID, key := ts.newSessionEnrollment(t, "session-"+box+"-"+t.Name(), testApprover)
	status, body := ts.session(t, key, enrollmentID, http.MethodPost, "/v1/requests",
		map[string]any{"request": signAgentSecretRequest(t, key, ts.URL, privReason, "SHARED_KEY"), "session_id": nil})
	created := decode[wireCreateRequestResponse](t, body)
	if status != http.StatusOK || created.State != "pending" || created.RecordID == nil {
		t.Fatalf("POST /v1/requests for SHARED_KEY (%q) on %s = %d %s; want a pending request on a record", privReason, box, status, body)
	}
	return created
}

// askPrivateElsewhere is askPrivate signed for the broker address elsewhere, as when a session
// asked a broker of another BROKER_PUBLIC_URL that shares this database: a record this broker
// does not verify (requests.Machine.ReadRecord refuses it) and no list of it shows. It answers
// the request's id and its record's.
func (ts *testServer) askPrivateElsewhere(t *testing.T, box string) (requestID, recordID string) {
	t.Helper()
	enrollmentID, key := ts.newSessionEnrollment(t, "session-"+box+"-"+t.Name(), testApprover)
	current := ts.machine.Audience
	ts.machine.Audience = elsewhere
	created, err := ts.machine.Create(t.Context(), enrollmentID, signAgentSecretRequestFor(t, key, elsewhere, privReason, 600, "SHARED_KEY"), "", nil)
	ts.machine.Audience = current
	if err != nil || created.RecordID == nil || created.State != "pending" {
		t.Fatalf("Create under %s = %+v, %v; want a pending request on a record", elsewhere, created, err)
	}
	return created.ID, *created.RecordID
}

// relayed is a request of method for path with raw as its JSON body, from the listed caller that
// relays a person's actions, presenting its own ServiceAccount token.
func (ts *testServer) relayed(t *testing.T, method, path, raw string) *http.Request {
	t.Helper()
	return request(t, method, ts.URL+path, strings.NewReader(raw),
		map[string]string{"Authorization": "Bearer " + ts.uiToken, "Content-Type": "application/json"})
}

// answerOf is status and body as readAnswer reads an answer: the body as JSON with its answer_id
// taken out.
func answerOf(t *testing.T, status int, body []byte) answerRead {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("answer %d %s is not JSON: %v", status, body, err)
	}
	delete(fields, "answer_id")
	normal, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return answerRead{status: status, body: string(normal)}
}

// requireAnswered fails t unless got is status with code, read as what.
func requireAnswered(t *testing.T, what string, got answerRead, status int, code string) {
	t.Helper()
	if got.status != status || decode[wireError](t, []byte(got.body)).Code != code {
		t.Fatalf("%s = %d %s; want %d %s", what, got.status, got.body, status, code)
	}
}

// writtenRows counts what a decision, a dismissal or a revoke writes, for a test to compare
// before and after calls that must write nothing.
func (ts *testServer) writtenRows(t *testing.T) []string {
	t.Helper()
	return ts.column(t, `select concat_ws(' ',
		(select count(*) from credential_request_events), (select count(*) from grants),
		(select count(*) from grants where revoked_at is not null), (select count(*) from audit),
		(select count(*) from credential_request_dismissals), (select count(*) from withheld_secrets))`)
}

// TestTheDecideRoutesAnswerAPersonAPrivateRequestIsHiddenFromAsForAnIDNoRecordHas: a request whose
// reason names a PRIV issue, and one signed for another broker address that this broker does not
// verify, are hidden from every person outside PRIV. Approve and deny, relayed for such a person
// — mallory, named by the body's approver, or no person at all: no approver, the anyone
// sentinel, a body that is no decision and one that is not JSON — answer for each the one answer
// of an id no record has, 404 NOT_FOUND, byte for byte. Before, both answered 403
// DECIDE_ON_BROKER_PAGE naming their page, so any person Dispatch relays for could tell which ids
// named a private project's request. testApprover and carol, on PRIV's list, still get 403
// DECIDE_ON_BROKER_PAGE naming the page, and 403 JUDGE_DECIDES while the model judge holds the
// request, of which mallory still reads only 404. While Dispatch cannot answer, the request is 503
// PROJECT_ACCESS_UNAVAILABLE to its insider and to mallory alike, and an id no record has is still
// 404. Nothing is written by any of them.
func TestTheDecideRoutesAnswerAPersonAPrivateRequestIsHiddenFromAsForAnIDNoRecordHas(t *testing.T) {
	ts, access := hiddenServer(t)
	hidden := *ts.askPrivate(t, "box-hidden").RecordID
	_, stale := ts.askPrivateElsewhere(t, "box-stale")
	ids := map[string]string{
		"a request of PRIV": hidden,
		"a request of PRIV signed for another broker address": stale,
		"an id no record has": missingRecord,
	}
	before := ts.writtenRows(t)
	bodies := map[string]string{
		"mallory, outside PRIV":      `{"approver":"` + hiddenOutsider + `"}`,
		"mallory, with a code":       `{"approver":"` + hiddenOutsider + `","code":"ABCD-EFGH"}`,
		"no approver":                `{}`,
		"the anyone sentinel":        `{"approver":"` + record.AnyoneApprover + `"}`,
		"a body that is no decision": `{"approve":true}`,
		"a body that is not JSON":    `not json`,
	}
	requireHidden := func(t *testing.T) {
		t.Helper()
		for _, route := range []string{"approve", "deny"} {
			for who, raw := range bodies {
				answers := map[string]answerRead{}
				for what, id := range ids {
					answers[what] = readAnswer(t, ts.relayed(t, http.MethodPost, "/v1/credential-requests/"+id+"/"+route, raw))
				}
				requireSameAnswers(t, route+" relayed for "+who, answers)
				requireAnswered(t, route+" relayed for "+who, answers["an id no record has"], http.StatusNotFound, "NOT_FOUND")
			}
		}
	}

	t.Run("a person outside PRIV reads the answer of an id no record has", requireHidden)
	t.Run("a person on PRIV's list is still sent to the page", func(t *testing.T) {
		for _, insider := range []string{testApprover, hiddenInsider} {
			for _, route := range []string{"approve", "deny"} {
				for what, id := range map[string]string{"a request of PRIV": hidden, "one signed elsewhere": stale} {
					status, body := ts.ui(t, http.MethodPost, "/v1/credential-requests/"+id+"/"+route, map[string]any{"approver": insider})
					if refusal := decode[wirePageRefusal](t, body); status != http.StatusForbidden || refusal.Code != "DECIDE_ON_BROKER_PAGE" || refusal.ApproveURL != ts.URL+"/approve/"+id {
						t.Fatalf("%s of %s relayed for %s = %d %s; want 403 DECIDE_ON_BROKER_PAGE naming its page", route, what, insider, status, body)
					}
				}
			}
		}
	})

	if _, err := ts.Store.Pool.Exec(context.Background(), `update requests set judge_until = now() + interval '10 minutes' where record_id=$1`, hidden); err != nil {
		t.Fatalf("hold the request for the model judge: %v", err)
	}
	t.Run("while the model judge holds it, a person on PRIV's list reads JUDGE_DECIDES", func(t *testing.T) {
		status, body := ts.ui(t, http.MethodPost, "/v1/credential-requests/"+hidden+"/approve", map[string]any{"approver": hiddenInsider})
		if refusal := decode[wirePageRefusal](t, body); status != http.StatusForbidden || refusal.Code != "JUDGE_DECIDES" {
			t.Fatalf("approve of the held request relayed for %s = %d %s; want 403 JUDGE_DECIDES", hiddenInsider, status, body)
		}
	})
	t.Run("while the model judge holds it, a person outside PRIV reads the answer of an id no record has", requireHidden)

	down := downDispatch(t)
	access.Serve(down)
	t.Run("while Dispatch cannot answer, the request is 503 to everyone and a missing id 404", func(t *testing.T) {
		for _, who := range []string{testApprover, hiddenOutsider} {
			status, body := ts.ui(t, http.MethodPost, "/v1/credential-requests/"+hidden+"/deny", map[string]any{"approver": who})
			requireAnswered(t, "deny of the request relayed for "+who+" while Dispatch is down", answerOf(t, status, body), http.StatusServiceUnavailable, "PROJECT_ACCESS_UNAVAILABLE")
		}
		status, body := ts.ui(t, http.MethodPost, "/v1/credential-requests/"+missingRecord+"/deny", map[string]any{"approver": hiddenOutsider})
		requireAnswered(t, "deny of an id no record has while Dispatch is down", answerOf(t, status, body), http.StatusNotFound, "NOT_FOUND")
		if down.Calls() == 0 {
			t.Fatal("the down Dispatch was never asked: the rows above are blind")
		}
	})

	if after := ts.writtenRows(t); !slices.Equal(after, before) {
		t.Fatalf("rows written = %v after the refused decisions, want %v as before", after, before)
	}
	if got := ts.record(t, hidden); got.State != "pending" || got.Decided != nil {
		t.Fatalf("the request after the refused decisions = %+v, want still pending", got)
	}
}

// TestARevokeAnswersAPersonAPrivateGrantIsHiddenFromAsForAGrantIDNoGrantHas: two grants of
// SHARED_KEY, each on a session of testApprover's whose request named a PRIV issue: one
// testApprover approved, and one whose approver is anyone, as the model judge's grant of a shared
// secret records it. mallory, outside PRIV, through Dispatch's revoke and through her own machine
// login's operator route, is answered for each the one answer of a grant id no grant has, 404
// NOT_FOUND, byte for byte, where the first answered 403 NOT_APPROVER and the second ended the
// grant; both stay live. While Dispatch cannot answer, testApprover's revoke of either, as its
// operator, is 503 PROJECT_ACCESS_UNAVAILABLE and ends nothing. Once it answers, carol, on PRIV's
// list, ends the anyone grant through Dispatch, and testApprover's machine login ends the other.
func TestARevokeAnswersAPersonAPrivateGrantIsHiddenFromAsForAGrantIDNoGrantHas(t *testing.T) {
	ts, access := hiddenServer(t)
	credID, machineKey := ts.mintLauncherCredential(t, testApprover, "example-host-devbox")
	malloryID, malloryKey := ts.mintLauncherCredential(t, hiddenOutsider, "example-host-devbox-m")
	byPerson := ts.approveAs(t, ts.askPrivate(t, "box-person"), testApprover)
	byAnyone := ts.approveAs(t, ts.askPrivate(t, "box-anyone"), testApprover)
	if _, err := ts.Store.Pool.Exec(context.Background(), `update grants set approver=$2 where id=$1`, byAnyone, record.AnyoneApprover); err != nil {
		t.Fatalf("record anyone as the grant's approver: %v", err)
	}
	missingGrant := uuid.NewString()
	grants := map[string]string{"testApprover's grant": byPerson, "anyone's grant": byAnyone}
	liveGrants := func(t *testing.T) []string {
		t.Helper()
		return ts.column(t, `select id::text from grants where id in ($1, $2) and revoked_at is null order by id`, byPerson, byAnyone)
	}
	bothLive := liveGrants(t)
	before := ts.writtenRows(t)

	t.Run("a person outside PRIV reads the answer of a grant id no grant has", func(t *testing.T) {
		for name, grantID := range grants {
			answers := map[string]answerRead{}
			for what, id := range map[string]string{name: grantID, "a grant id no grant has": missingGrant} {
				status, body := ts.ui(t, http.MethodPost, "/v1/grants/"+id+"/revoke-by-approver", map[string]any{"approver": hiddenOutsider})
				answers[what] = answerOf(t, status, body)
			}
			requireSameAnswers(t, "revoke-by-approver relayed for "+hiddenOutsider, answers)
			requireAnswered(t, "revoke-by-approver of a grant id no grant has", answers["a grant id no grant has"], http.StatusNotFound, "NOT_FOUND")

			answers = map[string]answerRead{}
			for what, id := range map[string]string{name: grantID, "a grant id no grant has": missingGrant} {
				status, body := ts.launcher(t, malloryKey, malloryID, http.MethodPost, "/v1/operator/grants/"+id+"/revoke", nil)
				answers[what] = answerOf(t, status, body)
			}
			requireSameAnswers(t, "the operator revoke by "+hiddenOutsider+"'s machine login", answers)
			requireAnswered(t, "the operator revoke of a grant id no grant has", answers["a grant id no grant has"], http.StatusNotFound, "NOT_FOUND")
		}
		if got := liveGrants(t); !slices.Equal(got, bothLive) {
			t.Fatalf("live grants after %s's revokes = %v, want %v", hiddenOutsider, got, bothLive)
		}
	})

	down := downDispatch(t)
	access.Serve(down)
	t.Run("while Dispatch cannot answer, a revoke ends nothing", func(t *testing.T) {
		status, body := ts.ui(t, http.MethodPost, "/v1/grants/"+byPerson+"/revoke-by-approver", map[string]any{"approver": testApprover})
		requireAnswered(t, "testApprover's revoke-by-approver while Dispatch is down", answerOf(t, status, body), http.StatusServiceUnavailable, "PROJECT_ACCESS_UNAVAILABLE")
		status, body = ts.launcher(t, machineKey, credID, http.MethodPost, "/v1/operator/grants/"+byAnyone+"/revoke", nil)
		requireAnswered(t, "testApprover's operator revoke while Dispatch is down", answerOf(t, status, body), http.StatusServiceUnavailable, "PROJECT_ACCESS_UNAVAILABLE")
		if got := liveGrants(t); !slices.Equal(got, bothLive) {
			t.Fatalf("live grants after the revokes Dispatch could not answer for = %v, want %v", got, bothLive)
		}
		if down.Calls() == 0 {
			t.Fatal("the down Dispatch was never asked: the rows above are blind")
		}
	})
	if after := ts.writtenRows(t); !slices.Equal(after, before) {
		t.Fatalf("rows written = %v after the refused revokes, want %v as before", after, before)
	}

	access.Serve(privDispatch(t))
	t.Run("a person on PRIV's list and the session's operator still revoke", func(t *testing.T) {
		if status, body := ts.ui(t, http.MethodPost, "/v1/grants/"+byAnyone+"/revoke-by-approver", map[string]any{"approver": hiddenInsider}); status != http.StatusOK || decode[stateBody](t, body).State != "revoked" {
			t.Fatalf("%s's revoke of anyone's grant = %d %s; want 200 revoked", hiddenInsider, status, body)
		}
		if status, body := ts.launcher(t, machineKey, credID, http.MethodPost, "/v1/operator/grants/"+byPerson+"/revoke", nil); status != http.StatusOK || decode[stateBody](t, body).State != "revoked" {
			t.Fatalf("testApprover's operator revoke = %d %s; want 200 revoked", status, body)
		}
		if got := liveGrants(t); len(got) != 0 {
			t.Fatalf("live grants after the two revokes = %v, want none", got)
		}
	})
}

// TestADismissalAnswersAPersonAPrivateRequestIsHiddenFromAsForAnIDNoRecordHas: two requests of
// PRIV past their deadline, one signed for another broker address. mallory's dismissal of either
// answers the one answer of an id no record has, 404 NOT_FOUND, byte for byte; the one signed
// elsewhere answered 500, since whether she may see it was read from its signed request, which
// this broker does not verify. While Dispatch cannot answer, testApprover's dismissal is 503
// PROJECT_ACCESS_UNAVAILABLE and writes no dismissal, the dismissal's fail-closed branch; once it
// answers, testApprover dismisses it.
func TestADismissalAnswersAPersonAPrivateRequestIsHiddenFromAsForAnIDNoRecordHas(t *testing.T) {
	ts, access := hiddenServer(t)
	created := ts.askPrivate(t, "box-hidden")
	ts.overdue(t, created.RequestID)
	hidden := *created.RecordID
	staleRequest, stale := ts.askPrivateElsewhere(t, "box-stale")
	ts.overdue(t, staleRequest)
	before := ts.writtenRows(t)

	t.Run("a person outside PRIV reads the answer of an id no record has", func(t *testing.T) {
		answers := map[string]answerRead{}
		for what, id := range map[string]string{"a request of PRIV": hidden, "one signed elsewhere": stale, "an id no record has": missingRecord} {
			answers[what] = readAnswer(t, ts.relayed(t, http.MethodPost, "/v1/credential-requests/"+id+"/dismiss", `{"approver":"`+hiddenOutsider+`"}`))
		}
		requireSameAnswers(t, "dismiss relayed for "+hiddenOutsider, answers)
		requireAnswered(t, "dismiss of an id no record has", answers["an id no record has"], http.StatusNotFound, "NOT_FOUND")
	})

	down := downDispatch(t)
	access.Serve(down)
	t.Run("while Dispatch cannot answer, a dismissal is 503 and writes nothing", func(t *testing.T) {
		status, body := ts.ui(t, http.MethodPost, "/v1/credential-requests/"+hidden+"/dismiss", map[string]any{"approver": testApprover})
		requireAnswered(t, "testApprover's dismissal while Dispatch is down", answerOf(t, status, body), http.StatusServiceUnavailable, "PROJECT_ACCESS_UNAVAILABLE")
		if down.Calls() == 0 {
			t.Fatal("the down Dispatch was never asked: the row above is blind")
		}
	})
	if after := ts.writtenRows(t); !slices.Equal(after, before) {
		t.Fatalf("rows written = %v after the refused dismissals, want %v as before", after, before)
	}

	access.Serve(privDispatch(t))
	if status, body := ts.ui(t, http.MethodPost, "/v1/credential-requests/"+hidden+"/dismiss", map[string]any{"approver": testApprover}); status != http.StatusOK {
		t.Fatalf("testApprover's dismissal once Dispatch answers = %d %s; want 200", status, body)
	}
}
