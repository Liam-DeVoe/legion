package natscallout

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
	"github.com/sjawhar/envoy/internal/cipher/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/natsgrant"
)

// An answer the Dispatch client kept from before a refresh does not clear a refusal of the
// broker's token: only Dispatch answering during the refresh does. cmd/cipherd shares one
// projectaccess.Client between the token exchange and the held refresh, and the client keeps an
// answer projectaccess.CacheTTL (30 s), so a person's trade can leave the answer a held daemon's
// question needs in its cache.
//
// Here, with the real client against a stand-in Dispatch, a trade asks whether entryPerson sees
// PUB, and the answer is kept. Then Dispatch refuses the broker's token on every request, and a
// refresh (PUB from the cache, OTHER refused) refuses PUB's daemon. Then Dispatch can no longer be
// reached, and a refresh gets PUB from the cache again and nothing else: Dispatch answered nothing,
// so the refusal stands. A refresh after the cache lapsed answers nothing too, which keeps any
// refusal (TestDispatchRefusingTheBrokersTokenRefusesAHeldConnection's outage step), so the second
// refresh is where the rule is decided. The no-trade row is the control that the cache is the
// cause: with nothing kept, both refreshes refuse under any rule. The control for the fix counts
// every answer as Dispatch answering: the trade row's last decision then admits PUB's daemon.
func TestACachedAnswerKeepsTheRefusalOfTheBrokersToken(t *testing.T) {
	for _, row := range []struct {
		name  string
		trade bool
	}{{"an answer a trade left in the cache", true}, {"control: no trade, nothing in the cache", false}} {
		t.Run(row.name, func(t *testing.T) {
			var mode atomic.Int32        // 0 answers, 1 refuses the broker's token, 2 is down
			var requests [3]atomic.Int32 // the requests Dispatch got in each mode
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				m := mode.Load()
				requests[m].Add(1)
				switch m {
				case 1:
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"code":"ACCESS_ACCOUNT_REQUIRED"}`))
				case 2:
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"code":"UNAVAILABLE"}`))
				default:
					key := strings.TrimPrefix(r.URL.Path, "/api/v1/access/projects/")
					_, _ = w.Write([]byte(`{"project":"` + key + `","visibility":"private","owner":"","person":"` + r.URL.Query().Get("person") + `","member_kind":"full","visible":true}`))
				}
			}))
			t.Cleanup(server.Close)
			client := projectaccess.New(server.URL, projectaccesstest.StaticAccount("bearer"), nil)
			services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: otherDaemonAccount}}
			deps := Deps{Exchange: exchangeOf(map[string][]string{pubDaemonAccount: {"PUB"}, otherDaemonAccount: {"OTHER"}}), Access: client.Access}
			deps.held = newHeldAccess(deps, services)
			require.NotNil(t, deps.held)
			decide := func() error {
				_, err := scopeGrant(deps, services[0], natsgrant.Daemon())
				return err
			}

			if row.trade {
				_, err := client.Access(t.Context(), "PUB", entryPerson)
				require.NoError(t, err)
			}
			mode.Store(1)
			deps.held.refresh(t.Context())
			requireRefused(t, decide(), labelAccessRefused, "Dispatch refuses the broker's own account token")
			mode.Store(2)
			deps.held.refresh(t.Context())
			err := decide()
			t.Logf("after the outage's refresh: %v; requests answered 200: %d, refused 403: %d, failed 503: %d", err, requests[0].Load(), requests[1].Load(), requests[2].Load())
			require.Error(t, err, "PUB's daemon was admitted while Dispatch, refusing the broker's token, answered nothing: PUB")
			requireRefused(t, err, labelAccessRefused, "Dispatch refuses the broker's own account token")
		})
	}
}
