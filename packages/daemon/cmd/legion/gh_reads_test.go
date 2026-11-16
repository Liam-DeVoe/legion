package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/ghroute"
	"github.com/sjawhar/legion/daemon/internal/ghroute/ghroutetest"
	"github.com/sjawhar/legion/daemon/internal/leakguard"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
	"github.com/sjawhar/legion/daemon/internal/testwait"
)

// runGh decides each call it passes with no guard before a call that writes nothing, so none of
// them is ever taken for one (ghCallModeOf): the exact `gh api rate_limit`, which the gh api read
// grammar would admit, is the rate-limit call, and a guard's own visibility read is that read,
// whatever ghroute.Target.WritesNothing says of either. Nor is the shared check's own visibility
// read, which runGh answers before it reads the command (sharedCheckVisibilityRead), or either of
// the guard's own reads in a read grammar: each gives --hostname, and the GraphQL read is a POST
// to `graphql` with fields, so none would be taken for a read if the order changed.
func TestGhCallModeDecidesTheUncheckedCallsBeforeTheReadGrammar(t *testing.T) {
	for _, tc := range []struct {
		rateLimitRead, visibilityRead, writesNothing bool
		want                                         ghCallMode
	}{
		{true, false, true, ghRateLimitCall},
		{true, false, false, ghRateLimitCall},
		{false, true, true, ghVisibilityCall},
		{false, true, false, ghVisibilityCall},
		{false, false, true, ghReadCall},
		{false, false, false, ghGuardedCall},
	} {
		if got := ghCallModeOf(tc.rateLimitRead, tc.visibilityRead, tc.writesNothing); got != tc.want {
			t.Errorf("ghCallModeOf(rate limit %v, visibility read %v, writes nothing %v) = %d, want %d", tc.rateLimitRead, tc.visibilityRead, tc.writesNothing, got, tc.want)
		}
	}
	for _, argv := range [][]string{
		leakguard.VisibilityReadArgs("acme/widgets"),
		leakguard.VisibilityReadWithHeadersArgs("acme/widgets"),
		leakguard.VisibilityGraphQLArgs("acme/widgets"),
	} {
		if target, err := ghroute.TargetOf(argv, nil, savedGhHelp, noGitRepository); err != nil || target.WritesNothing {
			t.Errorf("ghroute.TargetOf(%q) = %+v, %v; want a visibility read outside every read grammar", argv, target, err)
		}
	}
}

// ghFakeToken is a credential of the shape the shared check refuses in every call the wrapper
// counts as a read, a GitHub token (its LEAK_SECRET's `gh[pousr]_[A-Za-z0-9]{36,}`, which its own
// self-test fills with 36 drawn characters), built here at run time so that this file holds no
// string of that shape.
var ghFakeToken = "ghp_" + strings.Repeat("0aZ9", 9)

// ghCheckRefusesACredential is a stand-in shared check that refuses, as the installed check does
// (its gh_guard refuses a SECRET before it returns for a read), a gh call any argument of which
// carries a GitHub token, and clears every other call. It records each run and reads its standard
// input, as leakguardtest's stand-ins do, and prints the check's own refusal, which names no part
// of the token.
var ghCheckRefusesACredential = leakguardtest.Recording("cat >/dev/null 2>&1\n" +
	"if printf '%s\\n' \"$@\" | grep -Eq '(^|[^A-Za-z0-9_])gh[pousr]_[A-Za-z0-9]{36,}'; then\n" +
	"  echo 'LEAK CHECK: refusing this gh call: it carries a credential, which is refused whatever repository the call goes to'\n" +
	"  exit 3\n" +
	"fi\n" +
	"exit 0\n")

// ghGuardRecord is what one `legion gh` run of ghGuardRig left: its status and standard error,
// what the stand-in gh recorded (`gh <args>`, `env <its pager and browser variables>` and `stdin
// <text>` for the call, `visibility <args>` for each read of the guard's), what the stand-in
// shared check recorded of each run, and the call log.
type ghGuardRecord struct {
	code                     int
	stderr, gh, checked, log string
}

// ghRigEnv is the pager and browser the rig's caller names, and what the stand-in gh records of
// them when it is given them unchanged; ghRigReadEnv is what it records for a call that writes
// nothing, which runs with no pager and no browser program from the environment.
const (
	ghRigEnv     = "env GH_PAGER=rig-pager GH_BROWSER=rig-gh-browser BROWSER=rig-browser\n"
	ghRigReadEnv = "env GH_PAGER=cat GH_BROWSER=unset BROWSER=unset\n"
)

// ghGuardRig answers a run of `legion gh args…` with stdin against the shared check at the path
// check (ghCheckAt), a stand-in gh that answers the visibility read `public` and every call with
// exit 7 after recording it, and a state directory of the run's own, so no visibility is kept
// from another run. The caller's environment names a pager and two browsers.
func ghGuardRig(t *testing.T) func(t *testing.T, check, stdin string, args ...string) ghGuardRecord {
	t.Helper()
	root, defaultDir := gitCredentialBundle(t)
	help, err := filepath.Abs(ghHelpDir)
	if err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(t.TempDir(), "gh")
	script := ghStandInPrefix(help) +
		"printf 'gh %s\\n' \"$*\" >>\"$GH_STANDIN_RECORD\"\n" +
		"printf 'env GH_PAGER=%s GH_BROWSER=%s BROWSER=%s\\n' \"${GH_PAGER-unset}\" \"${GH_BROWSER-unset}\" \"${BROWSER-unset}\" >>\"$GH_STANDIN_RECORD\"\n" +
		"printf 'stdin %s\\n' \"$(cat)\" >>\"$GH_STANDIN_RECORD\"\n" +
		"exit 7\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return func(t *testing.T, check, stdin string, args ...string) ghGuardRecord {
		t.Helper()
		records := t.TempDir()
		ghRecord, checkRecord, state := filepath.Join(records, "gh"), filepath.Join(records, "check"), filepath.Join(records, "state")
		env := map[string]string{
			"LEGION_GH_PATH": stub, "GH_CONFIG_DIR": defaultDir, "LEGION_GH_OWNERS_DIR": root,
			"PUBLIC_LEAK_CHECK": check, testProvenCheckEnv: ghProvenProgramSHA256,
			"GH_STANDIN_RECORD": ghRecord, "LEAK_CHECK_RECORD": checkRecord, "XDG_STATE_HOME": state,
			"LEGION_ROLE": "merger", "LEGION_ISSUE": "issue-9", "LEAK_CHECK_STANDIN_TARGET": "acme/widgets",
			"GH_PAGER": "rig-pager", "GH_BROWSER": "rig-gh-browser", "BROWSER": "rig-browser",
		}
		code, _, stderr := legionGhIn(t, t.TempDir(), env, stdin, args...)
		return ghGuardRecord{
			code: code, stderr: stderr,
			gh: readRecord(t, ghRecord), checked: readRecord(t, checkRecord),
			log: readRecord(t, filepath.Join(state, "legion-gh", "calls.log")),
		}
	}
}

// testProvenCheckEnv names, to this package's test binary run as `legion` (TestMain), the sha256
// of the public_leak_check.py its tests count as proven, in place of the pinned check's
// (ghProvenCheckSHA256, ghroute.ReadsProvenCheckSHA256).
const testProvenCheckEnv = "LEGION_TEST_PROVEN_CHECK_SHA256"

// ghProvenProgram is the public_leak_check.py of the stand-in checks these tests count as the one
// the read rule was proven against, and ghProvenProgramSHA256 is its sha256, which the rig names
// to `legion gh` under testProvenCheckEnv.
const ghProvenProgram = "# stands for the shared check the read rule was proven against\n"

var ghProvenProgramSHA256 = func() string {
	sum := sha256.Sum256([]byte(ghProvenProgram))
	return hex.EncodeToString(sum[:])
}()

// ghCheckAt is the path of a stand-in shared check whose body is script, in a directory of the
// test's own, with ghProvenProgram beside it as its public_leak_check.py.
func ghCheckAt(t *testing.T, script string) string {
	t.Helper()
	return ghCheckWithProgram(t, script, ghProvenProgram)
}

// ghCheckWithProgram is ghCheckAt with program as its public_leak_check.py, and none where
// program is empty.
func ghCheckWithProgram(t *testing.T, script, program string) string {
	t.Helper()
	check := leakguardtest.Write(t, t.TempDir(), script)
	if program != "" {
		if err := os.WriteFile(filepath.Join(filepath.Dir(check), "public_leak_check.py"), []byte(program), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return check
}

// A call that writes nothing, a call of every leaf ghroute reads as a read among them
// (ghroutetest.ReadCalls), runs the shared check and no visibility read: the stand-in check
// records its run of the call and clears it, the stand-in gh records no visibility read, only the
// call, which gets the run's standard input whole and whose status comes back. gh runs it with
// GH_PAGER=cat and with neither GH_BROWSER nor BROWSER, though the caller set all three. Its line
// in the call log says `visibility="checked-only"` and names the repository the call resolved.
func TestLegionGhChecksAReadAndReadsNoVisibility(t *testing.T) {
	run := ghGuardRig(t)
	const stdin = "a line on standard input\n"
	for _, args := range ghroutetest.ReadCalls() {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got := run(t, ghCheckAt(t, ghCheckRefusesACredential), stdin, args...)
			if got.code != 7 {
				t.Errorf("legion gh %q = exit %d, stderr %q; want gh's own 7", args, got.code, got.stderr)
			}
			if want := "checked gh-guard " + strings.Join(args, " ") + "\n"; got.checked != want {
				t.Errorf("the shared check recorded %q, want its one run of the call, %q", got.checked, want)
			}
			if strings.Contains(got.gh, "visibility ") {
				t.Errorf("gh was asked the repository's visibility for a call that writes nothing: %q", got.gh)
			}
			if want := "gh " + strings.Join(args, " ") + "\n" + ghRigReadEnv + "stdin " + strings.TrimSuffix(stdin, "\n") + "\n"; got.gh != want {
				t.Errorf("gh recorded %q, want %q", got.gh, want)
			}
			target, err := ghroute.TargetOf(args, nil, savedGhHelp, noGitRepository)
			if err != nil {
				t.Fatalf("ghroute.TargetOf(%q) = %v", args, err)
			}
			repository := ""
			if target.Repo != "" {
				repository = target.Owner + "/" + target.Repo
			}
			if wants := []string{`visibility="checked-only"`, "repository=" + ghCallLogValue(repository), "status=7"}; strings.Count(got.log, "\n") != 1 || slices.ContainsFunc(wants, func(want string) bool { return !strings.Contains(got.log, want) }) {
				t.Errorf("the call log reads %q, want one line naming %q", got.log, wants)
			}
		})
	}
}

// A read whose arguments carry a credential is refused by the shared check, exit 2 with the
// check's refusal, and never reaches gh: the check runs for a call that writes nothing, and it
// refuses a credential in every call the wrapper counts as a read (ghroute.Target.WritesNothing).
// Each of these is in a read grammar as typed, and the refusal comes before any visibility read.
func TestLegionGhRefusesAReadThatCarriesACredential(t *testing.T) {
	run := ghGuardRig(t)
	for _, args := range [][]string{
		{"pr", "list", "-R", "acme/widgets", "--search", ghFakeToken},
		{"api", "repos/acme/widgets/issues?q=" + ghFakeToken},
		{"search", "code", ghFakeToken},
		{"api", "-H", "Authorization: token " + ghFakeToken, "repos/acme/widgets/pulls"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			if target, err := ghroute.TargetOf(args, nil, savedGhHelp, noGitRepository); err != nil || !target.WritesNothing {
				t.Fatalf("ghroute.TargetOf(%q) = %+v, %v; want a call that writes nothing", args, target, err)
			}
			got := run(t, ghCheckAt(t, ghCheckRefusesACredential), "", args...)
			if got.code != 2 || !strings.Contains(got.stderr, "LEAK CHECK: refusing") || !strings.Contains(got.stderr, "it carries a credential") {
				t.Errorf("legion gh %q = exit %d, stderr %q; want exit 2 and the check's refusal of a credential", args, got.code, got.stderr)
			}
			if want := "checked gh-guard " + strings.Join(args, " ") + "\n"; got.checked != want {
				t.Errorf("the shared check recorded %q, want its run of the call", got.checked)
			}
			if got.gh != "" {
				t.Errorf("gh ran for a call the check refused: %q", got.gh)
			}
		})
	}
}

// A read whose shared check cannot run is refused as every guarded call is, exit 2 with the
// guard's setup error, and never reaches gh: a check that is not there, and one that exits with
// its own setup error (exit 2), have cleared nothing.
func TestLegionGhRefusesAReadWhoseGuardCannotRun(t *testing.T) {
	run := ghGuardRig(t)
	for _, tc := range []struct {
		name  string
		check func(t *testing.T) string
	}{
		{"no shared check", func(t *testing.T) string { return filepath.Join(t.TempDir(), "gone", leakguardtest.CheckName) }},
		{"a shared check that cannot finish", func(t *testing.T) string { return ghCheckAt(t, leakguardtest.CannotFinish) }},
	} {
		for _, args := range [][]string{
			{"pr", "view", "1", "-R", "acme/widgets"},
			{"api", "repos/acme/widgets/pulls"},
		} {
			t.Run(tc.name+": "+strings.Join(args, " "), func(t *testing.T) {
				got := run(t, tc.check(t), "", args...)
				if got.code != 2 || !strings.Contains(got.stderr, "LEAK CHECK SETUP ERROR") {
					t.Errorf("legion gh %q = exit %d, stderr %q; want exit 2 and the guard's setup error", args, got.code, got.stderr)
				}
				if got.gh != "" {
					t.Errorf("gh ran for a call whose check could not run: %q", got.gh)
				}
			})
		}
	}
}

// A read whose shared check is stopped before it answers is refused, exit 2 with the guard's
// setup error, and never reaches gh. `legion gh` gives the check no bound of its own, as it gives
// a write's none; what ends a check that hangs is this process's own end, a SIGTERM to its pid,
// which cancels the context the check runs under (main's signal.NotifyContext), and the guard
// kills the check's process group (procgroup). The bound the guard reads a check's stop against
// is leakguard's own test of a read past its bound
// (TestAGhCallThatWritesNothingRunsTheSharedCheckAlone).
func TestLegionGhRefusesAReadWhoseGuardIsStopped(t *testing.T) {
	root, defaultDir := gitCredentialBundle(t)
	help, err := filepath.Abs(ghHelpDir)
	if err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(stub, []byte(ghStandInPrefix(help)+"printf 'gh %s\\n' \"$*\" >>\"$GH_STANDIN_RECORD\"\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	records := t.TempDir()
	ghRecord, checkRecord := filepath.Join(records, "gh"), filepath.Join(records, "check")
	// The check records its run and then waits in ticks for thirty seconds before it clears the
	// call, so a stop that never reaches it is a gh run this test reads, not a test that hangs.
	hangs := leakguardtest.Recording("cat >/dev/null 2>&1\nticks=0\nwhile [ \"$ticks\" -lt 600 ]; do sleep 0.05; ticks=$((ticks+1)); done\nexit 0\n")
	command := legionGhCommand(t, t.TempDir(), map[string]string{
		"LEGION_GH_PATH": stub, "GH_CONFIG_DIR": defaultDir, "LEGION_GH_OWNERS_DIR": root,
		"PUBLIC_LEAK_CHECK": ghCheckAt(t, hangs), testProvenCheckEnv: ghProvenProgramSHA256,
		"GH_STANDIN_RECORD": ghRecord, "LEAK_CHECK_RECORD": checkRecord,
	}, "pr", "view", "1", "-R", "acme/widgets")
	printed, err := os.Create(filepath.Join(records, "printed"))
	if err != nil {
		t.Fatal(err)
	}
	defer printed.Close()
	command.Stdout, command.Stderr = printed, printed
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	testwait.Eventually(t, "the shared check to be running", func() bool {
		_, err := os.Stat(checkRecord)
		return err == nil
	})
	// To this one pid, never to the group, which is this test binary's own.
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waited := command.Wait()
	exit, ok := waited.(*exec.ExitError)
	if !ok || exit.ExitCode() != 2 {
		t.Fatalf("legion gh under a stopped check = %v, want exit 2: %s", waited, readRecord(t, printed.Name()))
	}
	if out := readRecord(t, printed.Name()); !strings.Contains(out, "LEAK CHECK SETUP ERROR") {
		t.Errorf("legion gh printed %q, want the guard's setup error", out)
	}
	if ran := readRecord(t, ghRecord); ran != "" {
		t.Errorf("gh ran for a call whose check was stopped: %q", ran)
	}
}

// A listed read under a shared check that is not the one the read rule was proven against — its
// public_leak_check.py another file, or no such file, as a tmux pane's check of another version
// may be — gets the whole guard, the visibility read included, and its log line is no
// checked-only one.
func TestLegionGhGuardsAReadWholeUnderAnUnprovenCheck(t *testing.T) {
	run := ghGuardRig(t)
	for _, program := range []struct{ name, text string }{
		{"another program", ghProvenProgram + "# an edit under the same tag\n"},
		{"no program", ""},
	} {
		for _, args := range [][]string{
			{"pr", "view", "1", "-R", "acme/widgets"},
			{"api", "repos/acme/widgets/pulls"},
		} {
			t.Run(program.name+": "+strings.Join(args, " "), func(t *testing.T) {
				guardedWhole(t, run(t, ghCheckWithProgram(t, ghCheckPasses, program.text), "", args...), args)
			})
		}
	}
}

// guardedWhole fails t unless got is a call the guard read whole under a check that clears it:
// the check recorded its run of the call, the guard asked gh the visibility of the repository the
// call names (none for a call naming none, such as a GraphQL call, which the guard scans for that
// reason), and only then did gh run, with the pager and the browsers the caller set, and the log
// line is no checked-only one.
func guardedWhole(t *testing.T, got ghGuardRecord, args []string) {
	t.Helper()
	if got.code != 7 {
		t.Errorf("legion gh %q = exit %d, stderr %q; want the guard to clear it and gh's own 7", args, got.code, got.stderr)
	}
	if want := "checked gh-guard " + strings.Join(args, " ") + "\n"; !strings.Contains(got.checked, want) {
		t.Errorf("the shared check recorded %q, want its run of the call, %q", got.checked, want)
	}
	wantReads := 0
	if strings.Contains(strings.Join(args, " "), "acme/widgets") {
		wantReads = 1
	}
	if reads := strings.Count(got.gh, "visibility "); reads != wantReads {
		t.Errorf("the guard read the visibility %d times, want %d: %q", reads, wantReads, got.gh)
	}
	if want := "gh " + strings.Join(args, " ") + "\n" + ghRigEnv; !strings.Contains(got.gh, want) {
		t.Errorf("gh recorded %q, want the call %q after the guard, with the caller's pager and browsers", got.gh, want)
	}
	if strings.Contains(got.log, `visibility="checked-only"`) {
		t.Errorf("the call log reads a call the guard read whole as checked-only: %q", got.log)
	}
}

// Every other call runs the whole guard as before, one visibility read included: the calls of
// commands other than gh api, calls of listed leaves typed outside their grammar, and gh api calls
// with a guarded option, the spellings review 5911414081 named among them, or a guarded endpoint
// (ghroutetest.GuardedCalls, GuardedLeafSpellings and GuardedAPICalls).
func TestLegionGhGuardsEveryWrite(t *testing.T) {
	run := ghGuardRig(t)
	for _, args := range slices.Concat(ghroutetest.GuardedCalls, ghroutetest.GuardedLeafSpellings, ghroutetest.GuardedAPICalls()) {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			guardedWhole(t, run(t, ghCheckAt(t, ghCheckPasses), "", args...), args)
		})
	}

	// The guard still refuses what its check refuses, so gh never runs a write the check
	// refused.
	args := []string{"pr", "comment", "1", "-R", "acme/widgets", "--body", "a comment"}
	got := run(t, ghCheckAt(t, ghCheckRefuses), "", args...)
	if got.code != 2 || !strings.Contains(got.stderr, "LEAK CHECK: refusing") || strings.Contains(got.gh, "gh ") {
		t.Errorf("legion gh %q under a check that refuses = exit %d, stderr %q, gh %q; want exit 2, the guard's refusal and no gh run", args, got.code, got.stderr, got.gh)
	}
}
