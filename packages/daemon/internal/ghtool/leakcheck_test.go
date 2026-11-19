package ghtool

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/leakguard"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// standInSource is the Kubernetes runtime's reader of the shared check as these tests serve it
// (LeakCheckSource): body, packed, at any tag, with each tag it was read at recorded.
type standInSource struct {
	body []byte
	tags []string
}

func (s *standInSource) Get(_ context.Context, tag string) (leakguard.Served, error) {
	s.tags = append(s.tags, tag)
	return leakguard.Served{Tag: tag, Body: s.body}, nil
}

// servedCheck is the check the daemon serves, packed as it packs one (a gzipped tarball of the
// leak-check directory's own paths, leakguard.PackTarball): a launcher that answers the install's
// one `--help` run as the real check does, counting it in runs (leakguardtest.AnswersHelp), and
// clears every call it is handed after, recording it (checkPasses).
func servedCheck(t *testing.T, runs string) []byte {
	t.Helper()
	launcher := "#!/bin/sh\nif [ \"$1\" = --help ]; then\n" + leakguardtest.AnswersHelp(runs) + "fi\n" + checkPasses
	return leakguardtest.Tarball(t, map[string]leakguardtest.Member{
		leakguardtest.CheckName: {Body: launcher, Mode: 0o755},
		"public_leak_check.py":  {Body: "# the check itself\n", Mode: 0o644},
	})
}

// daemonEnviron is a daemon's own environment as the guard's children start from it (Host.Environ)
// with extra after it, and with no extra one holding none of the three places the guard resolves a
// check from: no PUBLIC_LEAK_CHECK, a PATH of the system's own directories, and a configuration
// home with no pointer file. That is a Kubernetes daemon's, whose image carries the check's pin and
// not the check. It carries the file the stand-in checks record their runs in, and a state home of
// its own.
func daemonEnviron(t *testing.T, f *fixture, extra ...string) []string {
	t.Helper()
	home := t.TempDir()
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_STATE_HOME=" + t.TempDir(), "LEAK_CHECK_RECORD=" + f.record}
	if check, err := leakguard.New(leakguard.Options{Env: env}).Resolve(); err == nil {
		t.Fatalf("the daemon's environment resolves the check %s, where it must hold none", check)
	}
	return append(env, extra...)
}

// A Kubernetes daemon's gh tool runs the check the daemon installed for it at boot, read through
// the runtime's own reader at the pin (InstallLeakCheck): a call the installed check clears runs
// gh, with no setup failure, in an environment that holds no check of its own. The install is a
// pod's launcher's: one `--help` run of the served check, the tag recorded, at
// <state_dir>/leak-check. Left to resolve a check itself, the same guard finds none there and
// refuses every call with the setup failure naming it, gh never started.
func TestACallRunsUnderTheCheckAKubernetesDaemonInstalled(t *testing.T) {
	const tag = "leak-check-v18"
	runs := filepath.Join(t.TempDir(), "help-runs")
	source := &standInSource{body: servedCheck(t, runs)}
	stateDir := t.TempDir()
	check, err := InstallLeakCheck(context.Background(), source, tag, stateDir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("InstallLeakCheck: %v", err)
	}
	if want := filepath.Join(stateDir, "leak-check", leakguardtest.CheckName); check != want || !slices.Equal(source.tags, []string{tag}) {
		t.Fatalf("the install is at %s after reads at %q, want %s from one read at %s", check, source.tags, want, tag)
	}
	if verified, _ := os.ReadFile(runs); string(verified) != "help\n" {
		t.Fatalf("the install ran the served check %q, want its one --help run", verified)
	}
	argv := []string{"pr", "comment", "5", "-R", "acme/widgets", "--body", "Done."}

	t.Run("the installed check clears the call and gh runs", func(t *testing.T) {
		f := newFixture(t, implementer, behaviour{Stdout: "posted\n"})
		f.answer.host.LeakCheck, f.answer.host.Environ = check, daemonEnviron(t, f)
		got := f.run(1, argv...)
		if !got.Ran || got.Exit != 0 || got.Output != "posted\n" {
			t.Fatalf("gh %q = %+v; want gh's answer, exit 0, and no setup failure", argv, got)
		}
		if ran := f.invocations(false); len(ran) != 1 {
			t.Fatalf("gh %q ran gh %d times: %+v", argv, len(ran), ran)
		}
		if checked := f.checked(); !strings.Contains(checked, "checked gh-guard "+strings.Join(argv[:4], " ")) {
			t.Errorf("the installed check recorded %q, want the call", checked)
		}
	})

	t.Run("no check named leaves the guard none to run", func(t *testing.T) {
		f := newFixture(t, implementer, behaviour{Stdout: "posted\n"})
		f.answer.host.LeakCheck, f.answer.host.Environ = "", daemonEnviron(t, f)
		got := f.run(1, argv...)
		const want = "LEAK CHECK SETUP ERROR: the gh call: the shared public leak check could not be found"
		if got.Ran || got.Exit != -1 || !strings.HasPrefix(got.Output, "refused: ") || !strings.Contains(got.Output, want) {
			t.Fatalf("gh %q = %+v; want a refusal containing %q, gh never run", argv, got, want)
		}
		if ran := f.invocations(false); len(ran) != 0 {
			t.Errorf("gh ran for a call no check cleared: %+v", ran)
		}
	})
}

// A re-pin is a boot at another tag: its install replaces the copy of the tag before at the same
// path, logging the one line that says so, and the path reads back as the new tag's.
func TestAnInstallAtANewPinReplacesTheOneBefore(t *testing.T) {
	runs := filepath.Join(t.TempDir(), "help-runs")
	source := &standInSource{body: servedCheck(t, runs)}
	stateDir := t.TempDir()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	first, err := InstallLeakCheck(context.Background(), source, "leak-check-v18", stateDir, log)
	if err != nil {
		t.Fatal(err)
	}
	second, err := InstallLeakCheck(context.Background(), source, "leak-check-v19", stateDir, log)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(stateDir, "leak-check")
	if second != first {
		t.Fatalf("the second install is at %s, want the first's path %s", second, first)
	}
	if _, err := leakguard.Installed([]string{leakguard.TagVariable + "=leak-check-v19"}, dir); err != nil {
		t.Fatalf("the install at %s does not read back as leak-check-v19: %v", dir, err)
	}
	if want := "answers: the install of leak-check-v19 replaced the install of leak-check-v18 at " + dir; !strings.Contains(logs.String(), want) {
		t.Errorf("the re-pin logged %q, want the line %q", logs.String(), want)
	}
}

// A tmux daemon's gh tool runs the machine's own check, resolved in the daemon's environment as a
// pane's is (MachineLeakCheck): a call it clears runs gh, the machine's check having run for it.
func TestACallRunsUnderTheMachinesCheckResolvedFromTheDaemonsEnvironment(t *testing.T) {
	f := newFixture(t, implementer, behaviour{Stdout: "posted\n"})
	machine := leakguardtest.Write(t, t.TempDir(), checkPasses)
	env := daemonEnviron(t, f, leakguard.CheckVar+"="+machine)
	check, err := MachineLeakCheck(env)
	if err != nil || check != machine {
		t.Fatalf("MachineLeakCheck = %q, %v; want the machine's %s", check, err, machine)
	}
	f.answer.host.LeakCheck, f.answer.host.Environ = check, env
	argv := []string{"pr", "comment", "5", "-R", "acme/widgets", "--body", "Done."}
	if got := f.run(1, argv...); !got.Ran || got.Exit != 0 || got.Output != "posted\n" {
		t.Fatalf("gh %q = %+v; want gh's answer, exit 0, and no setup failure", argv, got)
	}
	if checked := f.checked(); !strings.Contains(checked, "checked gh-guard "+strings.Join(argv[:4], " ")) {
		t.Errorf("the machine's check recorded %q, want the call", checked)
	}
}
