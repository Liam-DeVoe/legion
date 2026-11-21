package natscallout

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/cipher/projectaccess"
	"github.com/sjawhar/envoy/internal/cipher/projectaccess/projectaccesstest"
	"github.com/sjawhar/envoy/internal/testwait"
	"github.com/sjawhar/envoy/natsgrant"
)

// The modes of standInDispatch.
const (
	dispatchAnswers200  int32 = iota // answers every project visible
	dispatchRefuses403               // refuses the broker's token on every request
	dispatchDown503                  // cannot be reached
	dispatchSplitPUB200              // answers PUB visible and refuses the broker's token on the rest
)

// standInDispatch is Dispatch's project-access route behind the real projectaccess.Client, as
// cmd/cipherd shares one client between the token exchange and the held refresh. It answers by the
// mode it holds, and counts the requests it got in each mode.
type standInDispatch struct {
	client   *projectaccess.Client
	mode     atomic.Int32
	requests [4]atomic.Int32
}

func newStandInDispatch(t *testing.T) *standInDispatch {
	t.Helper()
	d := &standInDispatch{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		m := d.mode.Load()
		d.requests[m].Add(1)
		key := strings.TrimPrefix(r.URL.Path, "/api/v1/access/projects/")
		switch {
		case m == dispatchRefuses403 || m == dispatchSplitPUB200 && key != "PUB":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"ACCESS_ACCOUNT_REQUIRED"}`))
		case m == dispatchDown503:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":"UNAVAILABLE"}`))
		default:
			_, _ = w.Write([]byte(`{"project":"` + key + `","visibility":"private","owner":"","person":"` + r.URL.Query().Get("person") + `","member_kind":"full","visible":true}`))
		}
	}))
	t.Cleanup(server.Close)
	d.client = projectaccess.New(server.URL, projectaccesstest.StaticAccount("bearer"), nil)
	return d
}

// heldOnStandIn are Deps for PUB's and OTHER's held daemons whose Dispatch is d's client, and a
// decision of PUB's daemon. No refresh is made.
func heldOnStandIn(t *testing.T, d *standInDispatch) (Deps, func() error) {
	t.Helper()
	services := []ServiceEntry{{Kind: KindDaemon, Account: pubDaemonAccount}, {Kind: KindDaemon, Account: otherDaemonAccount}}
	deps := Deps{Exchange: exchangeOf(map[string][]string{pubDaemonAccount: {"PUB"}, otherDaemonAccount: {"OTHER"}}), Access: d.client.Access}
	deps.held = newHeldAccess(deps, services)
	require.NotNil(t, deps.held)
	return deps, func() error {
		_, err := scopeGrant(deps, services[0], natsgrant.Daemon())
		return err
	}
}

// An answer the Dispatch client kept from before a refusal of the broker's token does not clear
// it: only an answer whose question was asked after the refusal does. The client keeps an answer
// projectaccess.CacheTTL (30 s), so a person's trade can leave the answer a held daemon's question
// needs in its cache.
//
// Here a trade asks whether entryPerson sees PUB, and the answer is kept. Then Dispatch refuses
// the broker's token on every request, and a refresh (PUB from the cache, OTHER refused) refuses
// PUB's daemon. Then Dispatch can no longer be reached, and a refresh gets PUB from the cache again
// and nothing else: Dispatch answered nothing after the refusal, so it stands. A refresh after the
// cache lapsed answers nothing too, which keeps any refusal (the outage step of
// TestDispatchRefusingTheBrokersTokenRefusesAHeldConnection). The defect showed within the
// client's 30 s, so the row runs in real time. The no-trade row is the control that the cache is
// the cause: with nothing kept, both refreshes refuse under any rule. The control for the rule
// counts every answer as clearing: the trade row's last decision then admits PUB's daemon.
func TestACachedAnswerKeepsTheRefusalOfTheBrokersToken(t *testing.T) {
	for _, row := range []struct {
		name  string
		trade bool
	}{{"an answer a trade left in the cache", true}, {"control: no trade, nothing in the cache", false}} {
		t.Run(row.name, func(t *testing.T) {
			d := newStandInDispatch(t)
			deps, decide := heldOnStandIn(t, d)
			if row.trade {
				_, err := d.client.Access(t.Context(), "PUB", entryPerson)
				require.NoError(t, err)
			}
			d.mode.Store(dispatchRefuses403)
			deps.held.refresh(t.Context())
			requireRefused(t, decide(), labelAccessRefused, "Dispatch refuses the broker's own account token")
			d.mode.Store(dispatchDown503)
			deps.held.refresh(t.Context())
			err := decide()
			t.Logf("after the outage's refresh: %v; requests answered 200: %d, refused 403: %d, failed 503: %d", err, d.requests[0].Load(), d.requests[1].Load(), d.requests[2].Load())
			require.Error(t, err, "PUB's daemon was admitted while Dispatch, refusing the broker's token, answered nothing: PUB")
			requireRefused(t, err, labelAccessRefused, "Dispatch refuses the broker's own account token")
		})
	}
}

// A refusal of the broker's token clears once Dispatch answers a question asked after it, even
// when that answer reaches the held refresh through the cache it shares with the token exchange.
// Trades re-ask both held questions through the shared client every 100 ms. Dispatch refuses the
// broker's token, a refresh refuses PUB's daemon, and then Dispatch accepts the token again: the
// trades' next asks are answered and kept, and the next refresh gets those kept answers, asked
// after the refusal, and admits PUB's daemon. The no-trade row asks Dispatch itself and is admitted
// too. The control clears only on an answer asked during the refresh: the kept answers, asked
// before it began, never count while the trades keep the cache full, and the test fails naming PUB.
func TestARefusalOfTheBrokersTokenClearsOnAnAnswerAskedAfterIt(t *testing.T) {
	for _, row := range []struct {
		name   string
		trades bool
	}{{"trades keep the shared cache full", true}, {"no trades: the refresh asks Dispatch itself", false}} {
		t.Run(row.name, func(t *testing.T) {
			d := newStandInDispatch(t)
			d.mode.Store(dispatchRefuses403)
			deps, decide := heldOnStandIn(t, d)
			deps.held.refresh(t.Context())
			requireRefused(t, decide(), labelAccessRefused, "Dispatch refuses the broker's own account token")

			if row.trades {
				trades, stop := context.WithCancel(t.Context())
				t.Cleanup(stop)
				go func() {
					tick := time.NewTicker(100 * time.Millisecond)
					defer tick.Stop()
					for {
						for _, project := range []string{"PUB", "OTHER"} {
							_, _ = d.client.Access(trades, project, entryPerson)
						}
						select {
						case <-trades.Done():
							return
						case <-tick.C:
						}
					}
				}()
			}
			d.mode.Store(dispatchAnswers200)
			if row.trades {
				testwait.Eventually(t, "the trades' answers to both held questions kept", func() bool { return d.requests[dispatchAnswers200].Load() >= 2 })
			}
			answeredBefore := d.requests[dispatchAnswers200].Load()
			deps.held.refresh(t.Context())
			err := decide()
			t.Logf("after the refresh: %v; answers Dispatch gave before it %d, by it %d", err, answeredBefore, d.requests[dispatchAnswers200].Load()-answeredBefore)
			require.NoError(t, err, "PUB's daemon stayed refused after Dispatch answered with the broker's token: PUB")
		})
	}
}

// An answer Dispatch gave during the refresh that also got the refusal does not clear it later:
// it may have been asked before the refusal. Here Dispatch answers PUB and refuses the broker's
// token on OTHER in one refresh, and the client keeps PUB's answer. Then Dispatch can no longer be
// reached, and a refresh gets that kept answer and nothing else: the refusal stands. The control
// keys the clear to when the refusing refresh began, not to when it had all its answers: the kept
// answer, asked during that refresh, clears the refusal, and the test fails naming PUB.
func TestAnAnswerFromTheRefusingRefreshKeepsTheRefusal(t *testing.T) {
	d := newStandInDispatch(t)
	deps, decide := heldOnStandIn(t, d)
	d.mode.Store(dispatchSplitPUB200)
	deps.held.refresh(t.Context())
	requireRefused(t, decide(), labelAccessRefused, "Dispatch refuses the broker's own account token")
	d.mode.Store(dispatchDown503)
	deps.held.refresh(t.Context())
	err := decide()
	require.Error(t, err, "PUB's daemon was admitted on an answer from the refresh that got the refusal: PUB")
	requireRefused(t, err, labelAccessRefused, "Dispatch refuses the broker's own account token")
}
