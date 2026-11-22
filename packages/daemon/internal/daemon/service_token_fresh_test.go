package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/sjawhar/envoy/consumernames"
	"github.com/sjawhar/legion/daemon/internal/testnats"
	"github.com/sjawhar/legion/daemon/internal/testnkeys"
)

// A daemon whose NATS credential is its service token, the one the NATS auth service decides and so
// the one a grant held to its projects (natsgrant.WithinProjects) reaches, boots on fresh names:
// each generation's connection goes through the daemon's one dial (workflowRuntime.dial) and
// presents the token, as the daemon's own does. Behind a stand-in auth callout that admits only a
// token, the fresh boot opens its three durables, and the callout is shown four tokens: the daemon's
// own connection and one generation of each durable. The fixed boot on the same token is the row's
// pass control. The control that fails is a generation dial that presents the seed alone, as before
// this change: the auth service refuses a connection with no credential, and the fresh boot ends on
// "Authorization Violation".
func TestAFreshDaemonOnItsServiceTokenOpensEachGeneration(t *testing.T) {
	const account = "system:serviceaccount:legion-pub:legion-daemon"
	for _, tc := range []struct {
		mode    consumernames.Mode
		minimum int
	}{
		{consumernames.ModeFixed, 1},
		{consumernames.ModeFresh, 4},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			s := testnats.StartCallout(t, 0, ">")
			js, err := jetstream.New(s.Connect(t))
			if err != nil {
				t.Fatal(err)
			}
			createDaemonNotificationStream(t, js)
			tokenFile := filepath.Join(t.TempDir(), "nats-token")
			if err := os.WriteFile(tokenFile, []byte(testnkeys.ServiceToken(account, time.Now().Add(time.Hour))), 0o600); err != nil {
				t.Fatal(err)
			}
			logs := &syncBuffer{}
			w := grantWorkflow(t, logs)
			cfg := daemonGrantConfig(s.URL)
			cfg.ConsumerNames = tc.mode
			if tc.mode == consumernames.ModeFresh {
				writeConvertedSentinel(t, w, cfg.Project)
			}
			if err := w.connect(t.Context(), cfg, natsConnection{tokenFile: tokenFile, user: account, source: natsServiceToken}); err != nil {
				t.Fatalf("the daemon on its service token, on %s names: %v\n%s", tc.mode, err, logs.String())
			}
			defer w.closeNATS()
			if presented := len(s.Presented()); presented < tc.minimum {
				t.Fatalf("the callout was shown %d tokens on %s names, want at least %d: the daemon's own connection and each generation's", presented, tc.mode, tc.minimum)
			}
		})
	}
}
