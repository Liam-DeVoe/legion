// packages/envoy/internal/broker/airlockproxy/azure_bound_test.go
//
// The bound on the azure kind's exchange: the federated token is the broker pod's own, and
// Entra takes it as the assertion of every application whose federated credential trusts the
// broker, so the proxy exchanges it only as the application, and only in the tenant, the
// grant's own secret document names. Every row counts what reached the stand-in token endpoint,
// because a refusal that still made the exchange would be no bound at all.
package airlockproxy

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/broker/airlock/airlocktest"
	"github.com/sjawhar/envoy/internal/broker/requests"
)

// The application, the tenant and the scope a row names that the document does not: another
// application federated with the broker's identity, the tenant it lives in, and another resource
// the document's own application asks a token for.
const (
	azureOtherClientID = "99999999-8888-7777-6666-000000000000"
	azureOtherTenantID = "another-tenant-id"
	azureOtherScope    = "https://graph.example.com/.default"
)

// boundRefusal fails t unless answer is a 403 whose words name the entry and field and carry
// neither the real federated token, nor the document's value of that field, nor sent, the one
// the request carried instead, and unless the one refusal record the proxy wrote for the call
// carries code and gives the same reason. The ids are no secrets, but a refusal is written to
// the command and to the broker's log, and neither is a place an id - or whatever a command put
// in that field - is to be copied to. A failure names what leaked and never prints it, and
// prints words only when they carry none of the three.
func boundRefusal(t *testing.T, r *rig, answer *http.Response, body, code, field, stored, sent string) {
	t.Helper()
	// refuse writes its record before the answer, so a row that has its answer reads it at once.
	var reason string
	if records := r.logs.refusals(); len(records) != 1 {
		t.Errorf("the proxy wrote %d refusal records; want one", len(records))
	} else {
		reason, _ = records[0].fields["reason"].(string)
		if got := records[0].fields["code"]; got != code {
			t.Errorf("the refusal record's code is %v; want %s", got, code)
		}
	}
	never := []struct{ what, value string }{
		{"the real federated token", azureRealFederated},
		{"the document's " + field, stored},
		{"the request's " + field, sent},
	}
	for _, place := range []struct{ what, words string }{{"the answer", body}, {"the logged reason", reason}} {
		clean := true
		for _, n := range never {
			if n.value != "" && strings.Contains(place.words, n.value) {
				clean = false
				t.Errorf("%s carries %s; want neither the federated token nor either value of %s", place.what, n.what, field)
			}
		}
		if clean && (!strings.Contains(place.words, azureName) || !strings.Contains(place.words, field)) {
			t.Errorf("%s %q does not name the entry %s and the field %s", place.what, place.words, azureName, field)
		}
	}
	// Not fatal: the row's noExchange then says what reached the endpoint as well.
	if answer.StatusCode != http.StatusForbidden {
		t.Errorf("the request answered %d; want 403", answer.StatusCode)
	}
}

// noExchange fails t unless nothing reached the token endpoint or any other part of the bound
// host, and the broker was asked for no access placeholder.
func noExchange(t *testing.T, r *rig, stand *airlocktest.MicrosoftStand) {
	t.Helper()
	if sent := stand.Exchanges(); len(sent) != 0 {
		t.Errorf("the token endpoint was sent %d exchanges; want none", len(sent))
	}
	if seen := r.requests(); len(seen) != 0 {
		t.Errorf("the bound host was sent %d requests; want none", len(seen))
	}
	if mints := r.grants.accessMints(); mints != 0 {
		t.Errorf("the broker was asked for %d access placeholders; want none", mints)
	}
}

// TestAzureExchangeIsBoundToItsDocument: a command's token request is exchanged when its
// client_id and the tenant of the endpoint it posts to are the document's, whatever scope it
// asks for, since one application asks for tokens of several resources. One that names another
// application, or posts to another tenant's endpoint, is refused before the broker puts its
// federated token in the form, and nothing reaches the endpoint.
func TestAzureExchangeIsBoundToItsDocument(t *testing.T) {
	for _, row := range []struct {
		name string
		path string
		// form writes the row's own fields over tokenForm's.
		form func(url.Values)
		// scope is the scope the exchange the endpoint is sent carries, for a row that is
		// exchanged; field is the field a refused row's refusal names, and stored and sent
		// the document's value of it and the request's, which that refusal must carry neither
		// of.
		scope, field, stored, sent string
	}{
		{
			name:  "the document's own application and tenant",
			path:  azureTokenPath,
			scope: azureScope,
		},
		{
			name:  "another scope, as the document's application and in its tenant",
			path:  azureTokenPath,
			form:  func(f url.Values) { f.Set("scope", azureOtherScope) },
			scope: azureOtherScope,
		},
		{
			name:  "another application",
			path:  azureTokenPath,
			form:  func(f url.Values) { f.Set("client_id", azureOtherClientID) },
			field: "client_id", stored: azureClientID, sent: azureOtherClientID,
		},
		{
			// Of two client_ids the endpoint and not the proxy would choose the one that
			// counts, so the document's beside another is no bound.
			name:  "the document's application and another one",
			path:  azureTokenPath,
			form:  func(f url.Values) { f.Add("client_id", azureOtherClientID) },
			field: "client_id", stored: azureClientID, sent: azureOtherClientID,
		},
		{
			name:  "no application at all",
			path:  azureTokenPath,
			form:  func(f url.Values) { f.Del("client_id") },
			field: "client_id", stored: azureClientID,
		},
		{
			name:  "another tenant",
			path:  "/" + azureOtherTenantID + "/oauth2/v2.0/token",
			field: "tenant", stored: azureTenantID, sent: azureOtherTenantID,
		},
		{
			name:  "another tenant on the v1 endpoint",
			path:  "/" + azureOtherTenantID + "/oauth2/token",
			field: "tenant", stored: azureTenantID, sent: azureOtherTenantID,
		},
		{
			// A tenant that only opens with the document's is another tenant: the path is read
			// whole, not by its prefix.
			name:  "a tenant that opens with the document's own",
			path:  "/" + azureTenantID + "-and-more/oauth2/v2.0/token",
			field: "tenant", stored: azureTenantID,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			r, stand := newAzureRig(t, Options{})
			form := tokenForm(azureFederatedPlaceholder)
			if row.form != nil {
				row.form(form)
			}
			answer, body := tokenRequestAt(t, r, r.client(azureFederatedPlaceholder), row.path, form)

			if row.field != "" {
				boundRefusal(t, r, answer, body, codeTokenApplication, row.field, row.stored, row.sent)
				noExchange(t, r, stand)
				return
			}
			if answer.StatusCode != http.StatusOK {
				t.Fatalf("the exchange answered %d %q; want 200", answer.StatusCode, body)
			}
			placeholderTokenOf(t, body)
			exchanges := stand.Exchanges()
			if len(exchanges) != 1 {
				t.Fatalf("the endpoint was sent %d exchanges; want one", len(exchanges))
			}
			if seen := r.requests(); len(seen) != 1 || seen[0].Method != http.MethodPost || seen[0].Path != row.path {
				t.Fatalf("the bound host saw %d requests; want the one exchange, a POST to the document's tenant's endpoint %s", len(seen), row.path)
			}
			sent := exchanges[0]
			if sent.Get("client_assertion") != azureRealFederated {
				t.Errorf("the endpoint was not sent the real federated token as the assertion")
			}
			if got := sent.Get("client_id"); got != azureClientID {
				t.Errorf("the endpoint was sent the client_id %q; want the document's %q", got, azureClientID)
			}
			if got := sent.Get("scope"); got != row.scope {
				t.Errorf("the endpoint was sent the scope %q; want the command's own %q", got, row.scope)
			}
			if mints := r.grants.accessMints(); mints != 1 {
				t.Errorf("the broker was asked for %d access placeholders; want one", mints)
			}
		})
	}
}

// TestAnAccessSlotOfAnotherApplicationOrTenantIsNotExchanged: a pod that holds no token of an
// access slot exchanges for itself from what the slot carries, so the slot is held to the
// document as the command's own request is. A slot is a row of the broker's own, which only an
// exchange the bound let through writes - but a row written before the bound existed lives as
// long as its grant, and is refused all the same, with nothing reaching the endpoint or the
// service.
func TestAnAccessSlotOfAnotherApplicationOrTenantIsNotExchanged(t *testing.T) {
	for _, row := range []struct {
		name                string
		exchange            azureExchange
		field, stored, sent string
	}{
		{
			name:     "the document's own application and tenant",
			exchange: azureExchange{Endpoint: "https://" + boundHost + azureTokenPath, Scope: azureScope, ClientID: azureClientID},
		},
		{
			name:     "another application",
			exchange: azureExchange{Endpoint: "https://" + boundHost + azureTokenPath, Scope: azureScope, ClientID: azureOtherClientID},
			field:    "client_id", stored: azureClientID, sent: azureOtherClientID,
		},
		{
			name:     "another tenant",
			exchange: azureExchange{Endpoint: "https://" + boundHost + "/" + azureOtherTenantID + "/oauth2/v2.0/token", Scope: azureScope, ClientID: azureClientID},
			field:    "tenant", stored: azureTenantID, sent: azureOtherTenantID,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			r, stand := newAzureRig(t, Options{})
			access := requests.PlaceholderPrefix + "slotAccessSlotAccessSlotAccess01"
			r.grants.connections[azureFederatedPlaceholder].Names[azureName].Slots[azureAccessSlot(row.exchange)] = access
			answer, body := r.get(r.client(azureFederatedPlaceholder), "https://"+boundHost+"/v1/subscriptions", "Authorization", "Bearer "+access)

			if row.field != "" {
				boundRefusal(t, r, answer, body, codeAccessToken, row.field, row.stored, row.sent)
				noExchange(t, r, stand)
				return
			}
			seen := r.requests()
			if len(seen) != 2 {
				t.Fatalf("the host saw %d calls; want the pod's own exchange and the API call", len(seen))
			}
			if seen[1].Header.Get("Authorization") != "Bearer "+azureRealAccess {
				t.Fatalf("the service was not sent the real access token the pod exchanged for itself")
			}
			exchanges := stand.Exchanges()
			if len(exchanges) != 1 {
				t.Fatalf("the endpoint was sent %d exchanges; want one", len(exchanges))
			}
			if got := exchanges[0].Get("client_id"); got != azureClientID {
				t.Errorf("the pod exchanged as %q; want the document's application %q", got, azureClientID)
			}
		})
	}
}

// TestAzureBoundReadsTheWholePathAndTheDocumentsOwnIDs: the bound itself, on what no request
// through the proxy reaches it with. A dot segment the decision refuses first in a command's own
// request, but a slot's endpoint never passes the decision; a document that names no tenant or
// no application the release refuses to split, and the bound holds an exchange to no empty id
// all the same.
func TestAzureBoundReadsTheWholePathAndTheDocumentsOwnIDs(t *testing.T) {
	app := requests.AzureApplication{TenantID: azureTenantID, ClientID: azureClientID}
	one := []string{azureClientID}
	for _, row := range []struct {
		name      string
		app       requests.AzureApplication
		clientIDs []string
		path      string
		// field is the field the refusal names, "" for an exchange the bound lets through.
		field string
	}{
		{"the v2.0 endpoint of the document's tenant", app, one, azureTokenPath, ""},
		{"the v1 endpoint of the document's tenant", app, one, "/" + azureTenantID + "/oauth2/token", ""},
		{"another tenant behind the document's own and a dot segment", app, one, "/" + azureTenantID + "/../" + azureOtherTenantID + "/oauth2/v2.0/token", "tenant"},
		{"a segment before the document's tenant", app, one, "/" + azureOtherTenantID + "/" + azureTenantID + "/oauth2/v2.0/token", "tenant"},
		{"the document's tenant in another case", app, one, "/" + strings.ToUpper(azureTenantID) + "/oauth2/v2.0/token", "tenant"},
		{"another endpoint of the document's tenant", app, one, "/" + azureTenantID + "/oauth2/v2.0/devicecode", "tenant"},
		{"no client_id", app, nil, azureTokenPath, "client_id"},
		{"a document that names no application", requests.AzureApplication{TenantID: azureTenantID}, []string{""}, azureTokenPath, "client_id"},
		{"a document that names no tenant", requests.AzureApplication{ClientID: azureClientID}, one, "//oauth2/v2.0/token", "tenant"},
	} {
		t.Run(row.name, func(t *testing.T) {
			err := azureBound(azureName, row.app, row.clientIDs, row.path)
			if row.field == "" {
				if err != nil {
					t.Fatalf("azureBound = %v; want the exchange let through", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("azureBound let the exchange through; want a refusal naming %s", row.field)
			}
			if words := err.Error(); !strings.Contains(words, azureName) || !strings.Contains(words, row.field) ||
				strings.Contains(words, azureTenantID) || strings.Contains(words, azureClientID) || strings.Contains(words, azureOtherTenantID) {
				t.Errorf("azureBound = %q; want the entry and the field named and no id of the document's or the request's", words)
			}
		})
	}
}
