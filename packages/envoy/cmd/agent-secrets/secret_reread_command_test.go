package main

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sjawhar/envoy/internal/broker/policy"
	"github.com/sjawhar/envoy/internal/broker/policy/policytest"
	"github.com/sjawhar/envoy/internal/broker/secrets"
)

// TestTheRereadAnOpenOutcomeNamesReachesTheBroker: a retag whose tag write's answer is lost, and
// whose reread the broker then refuses, prints the reread to ask for once the tags hold what the
// person wants (rereadCommand). Run in bash, with an environment holding PATH and HOME alone, so no
// curlrc and no BASH_ENV of the runner's can send it elsewhere, the command:
//   - makes one reread where the shell's AGENT_SECRETS_URL holds the broker's address;
//   - makes one where that address ends in slashes, as an address typed by hand may, which the
//     CLI strips (brokerclient.WithoutTrailingSlashes): a doubled slash would be answered with a
//     redirect curl does not follow, and the command would exit 0 having asked nothing;
//   - stops naming the config file, asking nothing, where the address was in that file alone;
//   - exits non-zero naming the status where the broker refuses the reread, so a person does not
//     read a refusal as the reread done.
func TestTheRereadAnOpenOutcomeNamesReachesTheBroker(t *testing.T) {
	for _, c := range []struct {
		name               string
		fromFile           bool
		suffix             string
		refuse             int
		wantReread         bool
		wantFail, wantSaid string
	}{
		{name: "the address in AGENT_SECRETS_URL", wantReread: true},
		{name: "an address ending in slashes", suffix: "//", wantReread: true},
		{name: "the address in the config file alone", fromFile: true, wantSaid: "~/.config/agent-secrets/config"},
		{name: "a broker refusing the reread", refuse: http.StatusTooManyRequests, wantSaid: "429"},
	} {
		t.Run(c.name, func(t *testing.T) {
			local := secrets.NewLocal(policytest.Secret("API_KEY", "ada@example.com", policy.TierHuman, "v1"))
			broker := startSecretBroker(t, servedBy(local))
			url := os.Getenv(brokerURLVar)
			home := t.TempDir()
			t.Setenv("HOME", home)
			if c.fromFile {
				dir := filepath.Join(home, ".config", "agent-secrets")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "config"), []byte(brokerURLVar+"="+url+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv(brokerURLVar, "")
			}
			t.Cleanup(func() { clientConfig = nil })
			if err := loadClientConfig(); err != nil {
				t.Fatal(err)
			}
			broker.failRereads(http.StatusServiceUnavailable)
			useAWS(t, &raceOnTag{secretsAPI: local, loseAnswer: "tag"}, testAccount, adaSignIn)
			_, stderr, code := runSecret("retag", "API_KEY", "--tier", policy.TierAgent)
			printed := regexp.MustCompile("ask for the reread with `([^`]*)`").FindStringSubmatch(stderr)
			if code != 1 || printed == nil || printed[1] != rereadCommand("API_KEY") {
				t.Fatalf("retag: exit %d stderr %q; want 1 and the reread command %q", code, stderr, rereadCommand("API_KEY"))
			}
			for _, claim := range []string{"stands", "after the write"} {
				if strings.Contains(stderr, claim) {
					t.Fatalf("retag: stderr %q says %q of a write whose outcome is unknown", stderr, claim)
				}
			}
			broker.failRereads(c.refuse)
			env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
			if !c.fromFile {
				env = append(env, brokerURLVar+"="+url+c.suffix)
			}
			cmd := exec.Command("bash", "--noprofile", "--norc", "-c", printed[1])
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			rereads, _ := broker.recorded()
			if c.wantReread {
				if err != nil || len(rereads) != 1 || rereads[0] != "API_KEY" {
					t.Fatalf("the printed command: err %v, output %q, rereads %v; want one reread of API_KEY", err, out, rereads)
				}
				return
			}
			if err == nil || !strings.Contains(string(out), c.wantSaid) || len(rereads) != 0 {
				t.Fatalf("the printed command: err %v, output %q, rereads %v; want it to fail saying %q and record no reread", err, out, rereads, c.wantSaid)
			}
		})
	}
}
