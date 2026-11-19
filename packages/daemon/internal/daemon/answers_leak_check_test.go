package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/ghtool"
	"github.com/sjawhar/legion/daemon/internal/leakguard"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// servedCheckReader is the Sandbox runtime's reader of the shared check as a test serves it
// (sandbox.LeakCheckReader): one packed check at any tag, a launcher that answers the install's
// `--help` run as the real check does and clears every call after.
type servedCheckReader struct{ body []byte }

func (r servedCheckReader) Get(_ context.Context, tag string) (leakguard.Served, error) {
	return leakguard.Served{Tag: tag, Body: r.body}, nil
}

func newServedCheckReader(t *testing.T) servedCheckReader {
	t.Helper()
	launcher := "#!/bin/sh\nif [ \"$1\" = --help ]; then\n" + leakguardtest.AnswersHelp(filepath.Join(t.TempDir(), "help-runs")) + "fi\n" + leakguardtest.Passes
	return servedCheckReader{body: leakguardtest.Tarball(t, map[string]leakguardtest.Member{
		leakguardtest.CheckName: {Body: launcher, Mode: 0o755},
	})}
}

// A Kubernetes daemon installs the check its gh tool runs from the Sandbox runtime's reader, at the
// pin boot read, under its state directory, and logs the tag and the path. A daemon with no GitHub
// App has no reader, and its boot is refused in the sentence the runtime refuses its pods with.
func TestAKubernetesDaemonInstallsItsGhToolsCheckFromTheRuntimesReader(t *testing.T) {
	stateDir := t.TempDir()
	var logs bytes.Buffer
	check, err := installAnswersLeakCheck(context.Background(), stateDir, leakCheckTag, newServedCheckReader(t), slog.New(slog.NewTextHandler(&logs, nil)))
	if want := filepath.Join(stateDir, "leak-check", leakguardtest.CheckName); err != nil || check != want {
		t.Fatalf("installAnswersLeakCheck = %q, %v; want %s", check, err, want)
	}
	if line := logs.String(); !strings.Contains(line, "answers: installed the shared public leak check the gh tool's leak guard runs") ||
		!strings.Contains(line, "tag="+leakCheckTag) || !strings.Contains(line, "check="+check) {
		t.Errorf("the install logged %q, want its line naming %s and %s", line, leakCheckTag, check)
	}
	_, err = installAnswersLeakCheck(context.Background(), t.TempDir(), leakCheckTag, nil, quietLogger())
	if want := leakguard.RefusalPrefix + "the daemon runs with no GitHub App, so it cannot read " + leakguard.CheckSlug; err == nil || err.Error() != want {
		t.Fatalf("installAnswersLeakCheck with no reader = %v, want %q", err, want)
	}
}

// A tmux daemon's gh tool is handed the machine's check as it is when each answer opens, as a pane
// is at each launch: an answer opened after the check was republished elsewhere, its pointer file
// moved with it, runs the new copy, and one opened while the machine carries none is handed none,
// so its guard refuses every call naming the missing check, with one line that says so. A
// Kubernetes daemon's answers keep the copy it installed at boot, whatever the machine holds.
func TestEachAnswerIsHandedTheMachinesCheckAsItIsWhenTheAnswerOpens(t *testing.T) {
	configHome := t.TempDir()
	pointer := filepath.Join(configHome, "public-leak-check", "path")
	point := func(check string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(pointer), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pointer, []byte(check+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first := leakguardtest.Write(t, t.TempDir(), leakguardtest.Passes)
	republished := leakguardtest.Write(t, t.TempDir(), leakguardtest.Passes)
	var logs bytes.Buffer
	tools := ghTools{machineCheck: true, host: ghtool.Host{
		Environ: []string{"PATH=" + t.TempDir(), "XDG_CONFIG_HOME=" + configHome}, Log: slog.New(slog.NewTextHandler(&logs, nil)),
	}}

	point(first)
	if got := tools.hostFor("dispatch:m-1").LeakCheck; got != first {
		t.Fatalf("the first answer is handed %q, want the machine's check %s", got, first)
	}
	point(republished)
	if got := tools.hostFor("dispatch:m-2").LeakCheck; got != republished {
		t.Fatalf("the answer after the check moved is handed %q, want its new copy %s", got, republished)
	}
	if err := os.Remove(pointer); err != nil {
		t.Fatal(err)
	}
	if got := tools.hostFor("dispatch:m-3").LeakCheck; got != "" {
		t.Fatalf("an answer on a machine with no check is handed %q, want none", got)
	}
	if lines := strings.Count(logs.String(), "answers: no shared public leak check on this machine"); lines != 1 || !strings.Contains(logs.String(), "dispatch:m-3") {
		t.Errorf("the answers logged %q, want one line for the answer that found no check", logs.String())
	}

	point(first)
	installed := ghTools{host: ghtool.Host{LeakCheck: filepath.Join(t.TempDir(), "leak-check", leakguardtest.CheckName), Environ: tools.host.Environ}}
	if got := installed.hostFor("dispatch:m-4").LeakCheck; got != installed.host.LeakCheck {
		t.Fatalf("a Kubernetes daemon's answer is handed %q, want the copy it installed, %s", got, installed.host.LeakCheck)
	}
}
