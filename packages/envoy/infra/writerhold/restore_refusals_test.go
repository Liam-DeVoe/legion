package writerhold

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sjawhar/envoy/internal/holdrecord"
)

// refusingBroker is a broker whose POST /v1/nats-hold/restore answers 409 code to every request,
// and which records the phase of each restore sent.
type refusingBroker struct {
	mu     sync.Mutex
	phases []string
}

func newRefusingBroker(t *testing.T, code string) (*refusingBroker, Deps) {
	t.Helper()
	b := &refusingBroker{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/nats-hold/restore" {
			http.Error(w, "not in this row", http.StatusNotFound)
			return
		}
		var body struct {
			Phase string `json:"phase"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		b.mu.Lock()
		b.phases = append(b.phases, body.Phase)
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": "the restore stores nothing: " + code + " in this row"})
	}))
	t.Cleanup(srv.Close)
	_, key, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	d := Deps{Records: func(context.Context, HubFlags, bool) (*holdrecord.Client, error) {
		return holdrecord.New(holdrecord.Config{
			BrokerURL:  srv.URL,
			Token:      func(context.Context) (string, error) { return "operator-token", nil },
			SigningKey: key,
			VerifyKeys: []ed25519.PublicKey{key.Public().(ed25519.PublicKey)},
		})
	}}
	return b, d
}

func (b *refusingBroker) sent() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.phases...)
}

// #4679's round 6 refuses a restore whose signed_at is outside the clock-skew allowance 409
// clock_skew, and one judged against a log or journal that does not verify 409 below_floor; each
// stores nothing. `hold restore` (and `--bootstrap`) reads each as UNKNOWN (exit 2) naming the code,
// and neither is a reason to send `held`: one restore is sent, released. The control for
// clock_skew is restore at e7790f6d22, which returned the route's error as a refusal (exit 1); for
// below_floor it already read UNKNOWN there, through holdrecord's own refusal mapping, which this
// row pins.
func TestHoldARestoreRefusedForTheClockOrTheFloorIsUnknown(t *testing.T) {
	for _, code := range []string{"clock_skew", "below_floor"} {
		for _, args := range [][]string{{"--reason", "the row is gone"}, {"--bootstrap", "--reason", "the first row"}} {
			t.Run(code+" "+args[0], func(t *testing.T) {
				b, d := newRefusingBroker(t, code)
				var stdout, stderr bytes.Buffer
				full := append(append([]string{actionRestore}, args...), commonArgs...)
				exit := run(context.Background(), full, &stdout, &stderr, d)
				require.Equal(t, 2, exit, "restore answered %s: %s%s", code, stdout.String(), stderr.String())
				require.True(t, strings.Contains(stderr.String(), "UNKNOWN"), "the line is not UNKNOWN: %s", stderr.String())
				require.Contains(t, stderr.String(), code)
				require.Equal(t, []string{"released"}, b.sent(), "the restores sent")
			})
		}
	}
}
