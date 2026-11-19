package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/config"
)

// The workflow's Dispatch bearer reads dispatch_token_file at every call: after the file is
// rewritten (a projected service-account token the kubelet renewed) the next call answers the new
// token, and a file gone blank is an error naming the key and the path, never a token. Control: a
// value read once, as boot reads it, stays the first token.
func TestTheWorkflowsDispatchBearerReadsTheTokenFileAtEachCall(t *testing.T) {
	file := filepath.Join(t.TempDir(), "dispatch-token")
	write := func(token string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("first-projected-not-a-secret")
	bootRead, err := config.ReadSecretPointer("dispatch_token_file", file)
	if err != nil {
		t.Fatal(err)
	}
	bearer := dispatchBearer(config.Config{DispatchTokenFile: file})
	if got, err := bearer(); err != nil || got != "first-projected-not-a-secret" {
		t.Fatalf("the first call = %q, %v; want the first token", got, err)
	}
	write("second-projected-not-a-secret")
	if got, err := bearer(); err != nil || got != "second-projected-not-a-secret" {
		t.Fatalf("the call after the file changed = %q, %v; want the second token", got, err)
	}
	if bootRead != "first-projected-not-a-secret" {
		t.Fatalf("control: the value boot read once = %q, want the first token", bootRead)
	}
	write("")
	_, err = bearer()
	if err == nil || !strings.Contains(err.Error(), "dispatch_token_file") || !strings.Contains(err.Error(), file) ||
		strings.Contains(err.Error(), "projected-not-a-secret") {
		t.Fatalf("the call with a blank file = %v, want an error naming dispatch_token_file and %s and no token", err, file)
	}
}
