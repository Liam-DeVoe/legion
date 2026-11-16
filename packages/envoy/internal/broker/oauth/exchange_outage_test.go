package oauth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sjawhar/envoy/internal/broker/oauth"
	"github.com/sjawhar/envoy/internal/broker/oauthwire"
	"github.com/sjawhar/envoy/internal/brokerclient"
)

// A cold read of the cluster's key set slower than the listener's 3 s wait but inside its 10 s
// timeout is waited out: the exchange answers the token, where a wait that gave up first would
// have answered the pod a refusal it never trades past (review B4).
func TestAnExchangeWaitsOutASlowKeySetRead(t *testing.T) {
	f := newExchangeFixture(t, func(*oauth.Config) {})
	key := newKey(t)
	f.registers(t, account, "pod-uid-1", "implementer-g1")
	f.cluster.DelayKeys(3500 * time.Millisecond)
	start := time.Now()
	status, body := f.exchange(t, exchangeForm(f.podToken(t, "pod-uid-1", nil), oauthwire.SubjectTokenTypeKubernetes, "implementer-g1"), key)
	if status != http.StatusOK {
		t.Fatalf("exchange while the key set takes 3.5 s to answer = %d %v after %s, want 200", status, body, time.Since(start).Round(time.Millisecond))
	}
}

// A subject token the exchange cannot judge because its issuer's key set cannot be read is
// answered 503 temporarily_unavailable, never invalid_request: brokerclient, which every pod and
// daemon trades with, stops for good on a 4xx refusal (RefusedError.Permanent) and trades again
// after a 5xx, so an outage answered as a refusal ended the trade where the next one lands
// (review B4). The client here is brokerclient itself against this broker's token endpoint, and
// the first answer it was given is read back as it went out.
func TestAKeySetOutageAtTheExchangeIsTradedAgain(t *testing.T) {
	f := newExchangeFixture(t, func(*oauth.Config) {})
	key := newKey(t)
	f.registers(t, account, "pod-uid-1", "implementer-g1")
	podToken := f.podToken(t, "pod-uid-1", nil)
	f.cluster.FailKeys(http.StatusInternalServerError)

	var (
		mu         sync.Mutex
		trades     int
		refusals   int
		first      int
		firstError any
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+brokerclient.TokenPath, func(w http.ResponseWriter, r *http.Request) {
		recorder := httptest.NewRecorder()
		f.provider.Token(recorder, r, oauth.TokenHooks{})
		mu.Lock()
		trades++
		if recorder.Code != http.StatusOK {
			refusals++
		}
		if trades == 1 {
			var body map[string]any
			_ = json.Unmarshal(recorder.Body.Bytes(), &body)
			first, firstError = recorder.Code, body["error"]
		}
		mu.Unlock()
		for name, values := range recorder.Header() {
			w.Header()[name] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	})
	broker := httptest.NewServer(mux)
	t.Cleanup(broker.Close)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(podToken), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := brokerclient.New(brokerclient.Config{
		BrokerURL: broker.URL, Key: key, Subject: brokerclient.NewProjectedToken(tokenFile), Session: "implementer-g1",
		// Each retry waits a tenth of a second rather than the 15 s the loop starts at.
		After: func(time.Duration) <-chan time.Time { return time.After(100 * time.Millisecond) },
	})
	if err != nil {
		t.Fatalf("brokerclient.New: %v", err)
	}
	// The issuer comes back after the client has been answered the outage at least once.
	time.AfterFunc(300*time.Millisecond, func() { f.cluster.FailKeys(0) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var landed *brokerclient.Token
	err = client.Run(ctx, func(_ context.Context, token *brokerclient.Token) error {
		landed = token
		cancel()
		return nil
	})
	mu.Lock()
	defer mu.Unlock()
	if first != http.StatusServiceUnavailable || firstError != "temporarily_unavailable" {
		t.Errorf("the first trade, while the key set answered 500, was answered %d %v, want 503 temporarily_unavailable: the token was never judged", first, firstError)
	}
	if err != nil {
		t.Fatalf("Run = %v after %d trades; want the outage traded through, not the end of the loop", err, trades)
	}
	if landed == nil || landed.AccessToken == "" {
		t.Fatalf("Run ended with no token after %d trades, %d of them refused: want one once the key set reads again", trades, refusals)
	}
	if refusals < 1 {
		t.Errorf("the client traded %d times and none was answered the outage: this test proves nothing about trading past one", trades)
	}
}
