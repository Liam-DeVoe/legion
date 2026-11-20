package ghtool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// refusingSource is a reader whose every read fails with err, as GitHub's refusal of the read
// reaches InstallLeakCheck.
type refusingSource struct{ err error }

func (s refusingSource) Get(context.Context, string) (leakguard.Served, error) {
	return leakguard.Served{}, s.err
}

// servedCheck is the check the daemon serves, packed as it packs one (a gzipped tarball of the
// leak-check directory's own paths, leakguard.PackTarball): a launcher that answers the install's
// one `--help` run as the real check does, counting it in runs (leakguardtest.AnswersHelp), and
// clears every call it is handed after, recording it in record (checkPasses, recordsIn).
func servedCheck(t *testing.T, runs, record string) []byte {
	t.Helper()
	launcher := "#!/bin/sh\nif [ \"$1\" = --help ]; then\n" + leakguardtest.AnswersHelp(runs) + "fi\n" + recordsIn(record, checkPasses)
	return leakguardtest.Tarball(t, map[string]leakguardtest.Member{
		leakguardtest.CheckName: {Body: launcher, Mode: 0o755},
		"public_leak_check.py":  {Body: "# the check itself\n", Mode: 0o644},
	})
}

// installedAt is a Kubernetes daemon's check once its install landed at check
// (LeakCheck.Installed).
func installedAt(check string) *LeakCheck {
	installed := NewLeakCheck("leak-check-v18")
	installed.Installed(check)
	return installed
}

// daemonEnviron is a daemon's own environment as the guard's children start from it (Host.Environ)
// with extra after it, and with no extra one holding none of the three places the guard resolves a
// check from: no PUBLIC_LEAK_CHECK, a PATH of the system's own directories, and a configuration
// home with no pointer file. That is a Kubernetes daemon's, whose image carries the check's pin and
// not the check. It carries a state home of its own.
func daemonEnviron(t *testing.T, extra ...string) []string {
	t.Helper()
	home := t.TempDir()
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_STATE_HOME=" + t.TempDir()}
	if check, err := leakguard.New(leakguard.Options{Env: env}).Resolve(); err == nil {
		t.Fatalf("the daemon's environment resolves the check %s, where it must hold none", check)
	}
	return append(env, extra...)
}

// A Kubernetes daemon's gh tool runs the check the daemon installed for it, read through the
// runtime's own reader at the pin (InstallLeakCheck): a call the installed check clears runs gh,
// with no setup failure, in an environment that holds no check of its own. The install is a
// pod's launcher's: one `--help` run of the served check, the tag recorded, at
// <state_dir>/leak-check. Left to resolve a check itself, the same guard finds none there and
// refuses every call with the setup failure naming it, gh never started.
func TestACallRunsUnderTheCheckAKubernetesDaemonInstalled(t *testing.T) {
	const tag = "leak-check-v18"
	runs, record := filepath.Join(t.TempDir(), "help-runs"), filepath.Join(t.TempDir(), "leak-check-record")
	source := &standInSource{body: servedCheck(t, runs, record)}
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
		// The installed check records in the file written into it, which is what f.checked reads.
		f.record = record
		f.answer.host.LeakCheck, f.answer.host.Environ = installedAt(check), daemonEnviron(t)
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
		f.answer.host.LeakCheck, f.answer.host.Environ = nil, daemonEnviron(t)
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

// A failure of the install itself, after the reader served the check, is an *InstallError: here a
// served check whose one `--help` run names none of its shapes, which leakguard.InstallServed
// refuses, in the cause sentence a pod's start is refused with. A read that fails is the reader's
// own error as it came, and no *InstallError: installing again reads again, where a failed
// install of a body the reader keeps fails the same way again.
func TestAFailedInstallIsAnInstallErrorAndAFailedReadIsNot(t *testing.T) {
	const tag = "leak-check-v18"
	silent := leakguardtest.Tarball(t, map[string]leakguardtest.Member{
		leakguardtest.CheckName: {Body: "#!/bin/sh\nexit 2\n", Mode: 0o755},
	})
	_, err := InstallLeakCheck(context.Background(), &standInSource{body: silent}, tag, t.TempDir(), slog.New(slog.DiscardHandler))
	var install *InstallError
	if !errors.As(err, &install) || !strings.HasPrefix(err.Error(), leakguard.RefusalPrefix+"the served check") {
		t.Fatalf("the install of a check whose --help fails = %v, want an *InstallError in the cause sentence %q…", err, leakguard.RefusalPrefix+"the served check")
	}

	read := errors.New(leakguard.RefusalPrefix + "GitHub answered the read of " + leakguard.CheckSlug + " at " + tag + " with HTTP 502")
	_, err = InstallLeakCheck(context.Background(), refusingSource{err: read}, tag, t.TempDir(), slog.New(slog.DiscardHandler))
	if !errors.Is(err, read) || errors.As(err, &install) {
		t.Fatalf("an install whose read failed = %v, want the read's own error and no *InstallError", err)
	}
}

// A call that comes before a Kubernetes daemon's install lands is refused, gh never started, not
// even for its help, with the setup failure naming the pin and why the install's last try failed,
// recorded and logged as the guard's own refusals are. The first call after the install lands, in
// the same answer, runs under the installed check.
func TestACallBeforeTheInstallLandsIsRefusedAndTheNextAfterItRuns(t *testing.T) {
	const tag = "leak-check-v18"
	f := newFixture(t, implementer, behaviour{Stdout: "posted\n"})
	pending := NewLeakCheck(tag)
	cause := errors.New(leakguard.RefusalPrefix + "GitHub answered the read of " + leakguard.CheckSlug + " at " + tag + " with HTTP 502")
	pending.Failed(cause)
	f.answer.host.LeakCheck, f.answer.host.Environ = pending, daemonEnviron(t)
	argv := []string{"pr", "comment", "5", "-R", "acme/widgets", "--body", "Done."}

	got := f.run(1, argv...)
	want := "refused: LEAK CHECK SETUP ERROR: the gh call: the shared public leak check at " + tag + ", the pin " + leakguard.TagVariable +
		" names, is not installed yet, and the daemon tries the install again until it is: " + cause.Error()
	if got.Ran || got.Exit != -1 || got.Output != want {
		t.Fatalf("gh %q before the install = %+v; want the refusal %q, gh never run", argv, got, want)
	}
	if ran := f.invocations(true); len(ran) != 0 {
		t.Errorf("gh ran for a call refused before the install landed: %+v", ran)
	}
	row, recorded := f.calls.row(question, 1)
	if !recorded || row.Result == nil || *row.Result != got.Output || row.Exit != nil || row.Owner != "" {
		t.Errorf("the refused call is recorded as %+v (%t); want its refusal, no exit and no owner", row, recorded)
	}
	if lines := f.logLines("answers: gh call refused by the leak guard"); len(lines) != 1 || !strings.Contains(fmt.Sprint(lines[0]["why"]), "is not installed yet") {
		t.Errorf("the refusal logged %v; want one line naming the pending install", lines)
	}

	pending.Installed(f.path)
	if got := f.run(2, argv...); !got.Ran || got.Exit != 0 || got.Output != "posted\n" {
		t.Fatalf("gh %q after the install = %+v; want gh's answer, exit 0", argv, got)
	}
	if checked := f.checked(); !strings.Contains(checked, "checked gh-guard "+strings.Join(argv[:4], " ")) {
		t.Errorf("the installed check recorded %q, want the call after the install", checked)
	}
}

// A tmux daemon's gh tool names no check (Host.LeakCheck nil), so the guard resolves the machine's
// own in the daemon's environment at each call, as the tmux runtime resolves a pane's at each
// launch: a call runs the check the machine's pointer file names when the call is made, the next
// call after the check was republished elsewhere runs the new copy, and a call made while the
// machine carries none is refused with the setup failure naming the missing check, gh never run.
func TestEachCallRunsTheMachinesCheckAsItIsWhenTheCallIsMade(t *testing.T) {
	f := newFixture(t, implementer, behaviour{Stdout: "posted\n"})
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
	firstRecord, againRecord := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "again")
	first := leakguardtest.Write(t, t.TempDir(), recordsIn(firstRecord, checkPasses))
	republished := leakguardtest.Write(t, t.TempDir(), recordsIn(againRecord, checkPasses))
	f.answer.host.LeakCheck = nil
	f.answer.host.Environ = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "XDG_CONFIG_HOME=" + configHome, "XDG_STATE_HOME=" + t.TempDir()}
	argv := []string{"pr", "comment", "5", "-R", "acme/widgets", "--body", "Done."}
	checked := func(record string) int {
		t.Helper()
		recorded, err := os.ReadFile(record)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		return bytes.Count(recorded, []byte("checked gh-guard "+strings.Join(argv[:4], " ")))
	}

	point(first)
	if got := f.run(1, argv...); !got.Ran || got.Exit != 0 || checked(firstRecord) != 1 {
		t.Fatalf("the first call = %+v, checked %d times by the machine's check; want it run, checked once", got, checked(firstRecord))
	}
	point(republished)
	if got := f.run(2, argv...); !got.Ran || got.Exit != 0 || checked(againRecord) != 1 || checked(firstRecord) != 1 {
		t.Fatalf("the call after the check moved = %+v, checked %d times by the new copy and %d by the old; want it run under the new copy alone",
			got, checked(againRecord), checked(firstRecord))
	}
	if err := os.Remove(pointer); err != nil {
		t.Fatal(err)
	}
	got := f.run(3, argv...)
	const want = "LEAK CHECK SETUP ERROR: the gh call: the shared public leak check could not be found"
	if got.Ran || got.Exit != -1 || !strings.Contains(got.Output, want) {
		t.Fatalf("the call on a machine with no check = %+v; want a refusal containing %q", got, want)
	}
	if ran := f.invocations(false); len(ran) != 2 {
		t.Errorf("gh ran %d calls, want the two the machine's check cleared: %+v", len(ran), ran)
	}
}

// A re-pin is a boot at another tag: its install replaces the copy of the tag before at the same
// path, logging the one line that says so, and the path reads back as the new tag's.
func TestAnInstallAtANewPinReplacesTheOneBefore(t *testing.T) {
	runs := filepath.Join(t.TempDir(), "help-runs")
	source := &standInSource{body: servedCheck(t, runs, filepath.Join(t.TempDir(), "leak-check-record"))}
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
