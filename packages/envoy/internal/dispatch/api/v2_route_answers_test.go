package api

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/sjawhar/envoy/internal/broker/wire"
)

// PUT /api/v2/credential-requests/{id}/state answers the request as the broker records it once
// decided, read by the code the caller typed. A code the broker reads as another record - a code
// a later login took, which machine.LookupByCode answers newest first - is no answer for this
// request, and an answer of the broker's Dispatch cannot read is Dispatch's own failure: each is a
// logged 500, never the other record nor the 503 of a broker out of reach.
func TestTheCredentialStateAnswersItsOwnRecordAlone(t *testing.T) {
	tour := newRouteTour(t)
	route := tour.requireMounted(t, http.MethodPut, "/credential-requests/{id}/state")
	approve := func(t *testing.T, record string) (int, string) {
		t.Helper()
		tour.secrets.answer(http.MethodPost, "/v1/credential-requests/"+record+"/approve", http.StatusOK,
			map[string]any{"credential_id": "tour-credential", "state": "approved"})
		response := tour.send(t, route, tourRequest{
			Path: map[string]string{"id": record}, Body: map[string]any{"state": "approved", "code": "ABCD-1234"}, As: tourPerson("alice"),
		}, "")
		code, _ := refusalOf(t, response)
		return response.Code, code
	}

	t.Run("a code that reads another record", func(t *testing.T) {
		tour.secrets.answer(http.MethodPost, "/v1/machine-logins/lookup", http.StatusOK, tourLoginRecord("tour-record-"+uuid.NewString(), "pending"))
		if status, code := approve(t, "tour-record-"+uuid.NewString()); status != http.StatusInternalServerError || code != "INTERNAL" {
			t.Fatalf("an approval whose code reads another record answered %d %s, want 500 INTERNAL", status, code)
		}
	})

	t.Run("a lookup Dispatch cannot read", func(t *testing.T) {
		tour.secrets.answer(http.MethodPost, "/v1/machine-logins/lookup", http.StatusOK, []string{"no record"})
		if status, code := approve(t, "tour-record-"+uuid.NewString()); status != http.StatusInternalServerError || code != "INTERNAL" {
			t.Fatalf("an approval whose lookup answered no record Dispatch reads answered %d %s, want 500 INTERNAL", status, code)
		}
	})
}

// GET /api/v2/machine-logins answers each machine login's person as the secrets broker lists it
// (#4452): a person's pod login, the login of a pod whose Kubernetes account the broker's
// BROKER_POD_OPERATORS maps to a person, names that person, and every other login, a service's
// among them, answers person null. The page reads the broker's rows into its own type before it
// slices them, so a member that type lacked would never reach the Machine logins section, which
// reads a pod login as the viewer's own by its person.
func TestAPersonsPodLoginNamesItsPersonOnV2(t *testing.T) {
	tour := newRouteTour(t)
	route := tour.requireMounted(t, http.MethodGet, "/machine-logins")
	tour.secrets.answer(http.MethodGet, "/v1/launcher-credentials", http.StatusOK,
		wire.LauncherCredentialsResponse{Credentials: tourMachineLogins("alice")})
	response := tour.send(t, route, tourRequest{As: tourPerson("alice")}, "")
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/v2/machine-logins answered %d %s, want 200", response.Code, response.Body.String())
	}
	persons := map[any]any{}
	for _, login := range decodeBody[page[map[string]any]](t, response).Items {
		person, named := login["person"]
		if !named {
			t.Errorf("GET /api/v2/machine-logins answered a login with no person: %v", login)
		}
		persons[login["credential_id"]] = person
	}
	if want := map[any]any{"tour-person-pod-login": "alice", "tour-service-login": nil}; !reflect.DeepEqual(persons, want) {
		t.Errorf("GET /api/v2/machine-logins answered the persons %v, want %v: %s", persons, want, response.Body.String())
	}
}

// GET /api/v2/asks?source=<kind>:<id> names its source by a whole uuid or a prefix of 8 characters
// or more, and any other source is 400 INVALID_QUERY, as its description says, rather than the 404
// of a source on no document.
func TestAnAskSourceThatIsNoIDIsRefused(t *testing.T) {
	tour := newRouteTour(t)
	route := tour.requireMounted(t, http.MethodGet, "/asks")
	for _, source := range []string{"ask:abc", "comment:not-an-id"} {
		response := tour.send(t, route, tourRequest{Query: url.Values{"source": {source}}, As: tourPerson("alice")}, "")
		if code, _ := refusalOf(t, response); response.Code != http.StatusBadRequest || code != "INVALID_QUERY" {
			t.Errorf("GET /api/v2/asks?source=%s answered %d %s, want 400 INVALID_QUERY", source, response.Code, response.Body.String())
		}
	}
}
