package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// theLeakCheckHost is the one refusable literal these tests need: a Tailscale MagicDNS host built
// out of invented words, which names no machine and no tailnet, and which this repository's own
// rules refuse (tailnet_host_rule, packages/daemon/internal/leakguard/private-names.toml). It is
// spelled in two pieces, the way that rule file and the guard's own tests spell every literal of
// their own, so that this file passes .github/scripts/check-private-names.sh: joined here, it
// would put a name the rules refuse into a tracked file of a public repository.
const theLeakCheckHost = "amber-kite.still-meadow.ts" + ".net"

// theLeakCheckClass is what a report calls that host, and the whole of what it says about the
// text: the description of the rule that matched.
const theLeakCheckClass = "names a Tailscale MagicDNS host"

// noSHA is git's "this ref is not there", which a pre-push line carries for the remote side of a
// ref the push creates.
const noSHA = "0000000000000000000000000000000000000000"

// noSharedCheck leaves the command no check to resolve at all: nothing in PUBLIC_LEAK_CHECK, no
// public-leak-check.sh on any PATH entry, and no pointer file under a configuration home of its
// own. Every fixture a case needs is built before this, since it empties the PATH a child of the
// test would be found on.
func noSharedCheck(t *testing.T) {
	t.Helper()
	fixtureenv.Unset(t, "PUBLIC_LEAK_CHECK")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// aPushFixture is a repository holding one commit whose message is message, and the line git
// hands a pre-push hook for a push of it to a remote that holds the ref yet.
func aPushFixture(t *testing.T, message string) (dir, line string) {
	t.Helper()
	dir = t.TempDir()
	fixture := fixturegit.Isolated(t, t.TempDir())
	fixture.Run(dir, fixturegit.InitWorkTree("", "main")...)
	fixture.Run(dir, "git", "commit", "--allow-empty", "--quiet", "-m", message)
	sha := fixture.Run(dir, "git", "rev-parse", "HEAD")
	return dir, "refs/heads/main " + sha + " refs/heads/main " + noSHA + "\n"
}

// leakCheck runs the command as git's hook and a pod's scripts run it: stdin is what they pipe
// in, and the status and what reached stderr are what they read. Standard output is the hook's
// own, and the command writes nothing to it.
func leakCheck(t *testing.T, stdin string, args ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	previous := leakCheckStdin
	t.Cleanup(func() { leakCheckStdin = previous })
	leakCheckStdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	code := runLeakCheck(ctx, args, &stdout, &stderr)
	if stdout.Len() > 0 {
		t.Errorf("printed %q on standard output, want nothing", stdout.String())
	}
	return code, stderr.String()
}

// says fails the test unless printed holds every one of wants, and unless it is empty where
// there is nothing to want.
func says(t *testing.T, printed string, wants []string) {
	t.Helper()
	if len(wants) == 0 {
		if printed != "" {
			t.Errorf("printed %q, want nothing", printed)
		}
		return
	}
	for _, want := range wants {
		if !strings.Contains(printed, want) {
			t.Errorf("printed %q, want it to hold %q", printed, want)
		}
	}
}

// `legion leak-check pre-push` is what the image's hook runs: git's update lines on standard
// input, the repository it ran the hook in as the working directory, and the remote and its URL
// as its arguments. Either check refuses the push, and the statuses are the shared check's own —
// 0 clean, 3 refused, 2 a check that could not be made to run.
func TestLeakCheckPrePushRunsTheGuardOverGitsUpdateLines(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		// standIn is the stand-in shared check's body, and "" is a call with no check at all.
		standIn string
		code    int
		says    []string
	}{
		{
			name:    "the shared check refuses the push",
			message: "a commit of nothing in particular",
			standIn: leakguardtest.Refuses,
			code:    3,
			says:    []string{"LEAK CHECK: refusing the push to origin: the shared public leak check refused it", "one of ours"},
		},
		{
			name:    "this repository's own rules refuse the push",
			message: "a commit that names " + theLeakCheckHost,
			standIn: leakguardtest.Passes,
			code:    3,
			says:    []string{"LEAK CHECK: refusing the push to origin: this repository's own private-name rules match it", theLeakCheckClass},
		},
		{
			name:    "a clean push",
			message: "a commit of nothing in particular",
			standIn: leakguardtest.Passes,
			code:    0,
		},
		{
			name:    "no shared check resolves",
			message: "a commit of nothing in particular",
			standIn: "",
			code:    2,
			says:    []string{"LEAK CHECK SETUP ERROR: the push to origin: the shared public leak check could not be found"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, line := aPushFixture(t, tc.message)
			// git runs its hook in the repository the push is made from, and that working
			// directory is where the command reads the push's commits from.
			t.Chdir(dir)
			if tc.standIn == "" {
				noSharedCheck(t)
			} else {
				t.Setenv("PUBLIC_LEAK_CHECK", leakguardtest.Write(t, t.TempDir(), tc.standIn))
			}
			// A local path names no github.com repository, so the guard reads no visibility and
			// this test starts no gh.
			code, printed := leakCheck(t, line, "pre-push", "origin", dir+".git")
			if code != tc.code {
				t.Errorf("exited %d, want %d: %s", code, tc.code, printed)
			}
			says(t, printed, tc.says)
		})
	}
}

// `legion leak-check text` is the shape anything about to publish a piece of text runs: the text
// on standard input, and the kind it is called by in the refusal, which is the one thing the
// caller knows better than the guard does.
func TestLeakCheckTextRunsTheGuardOverWhatItIsPiped(t *testing.T) {
	const kind = "the pull request body"
	for _, tc := range []struct {
		name    string
		text    string
		standIn string
		code    int
		says    []string
	}{
		{
			name:    "the shared check refuses the text",
			text:    "a title\na body of nothing in particular\n",
			standIn: leakguardtest.Refuses,
			code:    3,
			says:    []string{"LEAK CHECK: refusing " + kind + ": the shared public leak check refused it", "one of ours"},
		},
		{
			name:    "this repository's own rules refuse the text",
			text:    "a title\na body that names " + theLeakCheckHost + "\n",
			standIn: leakguardtest.Passes,
			code:    3,
			says:    []string{"LEAK CHECK: refusing " + kind + ": this repository's own private-name rules match it", theLeakCheckClass},
		},
		{
			name:    "a clean text",
			text:    "a title\na body of nothing in particular\n",
			standIn: leakguardtest.Passes,
			code:    0,
		},
		{
			name:    "no shared check resolves",
			text:    "a title\na body of nothing in particular\n",
			standIn: "",
			code:    2,
			says:    []string{"LEAK CHECK SETUP ERROR: " + kind + ": the shared public leak check could not be found"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.standIn == "" {
				noSharedCheck(t)
			} else {
				t.Setenv("PUBLIC_LEAK_CHECK", leakguardtest.Write(t, t.TempDir(), tc.standIn))
			}
			code, printed := leakCheck(t, tc.text, "text", kind)
			if code != tc.code {
				t.Errorf("exited %d, want %d: %s", code, tc.code, printed)
			}
			says(t, printed, tc.says)
		})
	}
}

// A call the command cannot read is a usage error, exit 2, and no write is checked by it: a hook
// or a script that called it wrongly has cleared nothing. A help request is the one call that
// prints the usage and exits 0.
func TestLeakCheckRefusesACallItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		code int
		says string
	}{
		{name: "no shape at all", code: 2, says: subcommandUsage("leak-check", leakCheckCommands)},
		{name: "a shape it does not know", args: []string{"pre-receive"}, code: 2, says: `legion leak-check: unknown subcommand "pre-receive"`},
		{name: "a pre-push with no url", args: []string{"pre-push", "origin"}, code: 2, says: leakCheckPrePushUsage},
		{name: "a pre-push with a third argument", args: []string{"pre-push", "origin", "url", "extra"}, code: 2, says: leakCheckPrePushUsage},
		{name: "a text with no kind", args: []string{"text"}, code: 2, says: leakCheckTextUsage},
		{name: "a help request", args: []string{"--help"}, code: 0, says: subcommandUsage("leak-check", leakCheckCommands)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, printed := leakCheck(t, "", tc.args...)
			if code != tc.code {
				t.Errorf("exited %d, want %d: %s", code, tc.code, printed)
			}
			says(t, printed, []string{tc.says})
		})
	}
}

// `legion leak-check pre-push` to a repository whose owner the gh bundle holds no directory for:
// the guard's REST read goes through the pod's `gh`, `legion gh`, which refuses it, exit 2, naming
// the owner, and the real gh never runs. The guard then reads the visibility with git's
// ls-remote and no credential, once. Where ls-remote lists the refs, the repository is public and
// the push goes on to the scan, which refuses this push's ref name, exit 3. Where GitHub asks
// ls-remote for a credential, as it does for a private repository, the push is refused as a setup
// failure naming both reads, exit 2. A push to a repository of an owner the bundle holds is read
// by the real gh, as before, and git never runs.
func TestLeakCheckPrePushReadsAnUnservedOwnersVisibilityWithNoCredential(t *testing.T) {
	root, defaultDir := gitCredentialBundle(t)
	help, err := filepath.Abs(ghHelpDir)
	if err != nil {
		t.Fatal(err)
	}
	lists := "printf '%s\\tHEAD\\n' " + strings.Repeat("1", 40) + "\nexit 0\n"
	refused := "echo 'fatal: could not read Username for '\\''https://github.com'\\'': terminal prompts disabled' >&2\nexit 128\n"
	for _, tc := range []struct {
		name, repo, git string
		// code and says are the command's exit status and what it prints; reads is what the
		// stand-ins recorded, in order: rest for the real gh's read, git for ls-remote.
		code  int
		says  []string
		reads string
	}{
		{
			name: "an unserved owner's repository ls-remote lists", repo: "other-owner/invented-repo", git: lists,
			code: 3, says: []string{"LEAK CHECK: refusing the push to origin: this repository's own private-name rules match it", theLeakCheckClass},
			reads: "git\n",
		},
		{
			name: "an unserved owner's repository GitHub asks ls-remote a credential for", repo: "other-owner/invented-repo", git: refused,
			code: 2,
			says: []string{
				"LEAK CHECK SETUP ERROR: the push to origin: the visibility of other-owner/invented-repo could not be read: " +
					`gh api repos/other-owner/invented-repo exited 2: legion gh: "other-owner" is not an owner of this project: ` + root + " holds no gh directory for it, " +
					"and git ls-remote with no credential could not answer: git ls-remote of other-owner/invented-repo exited 128",
				"could not read Username",
			},
			reads: "git\n",
		},
		{name: "a served owner's private repository", repo: "acme/widgets", git: lists, code: 0, reads: "rest\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, record := t.TempDir(), filepath.Join(t.TempDir(), "reads")
			stub := filepath.Join(bin, "real-gh")
			script := "#!/bin/sh\nfor last; do :; done\n" +
				"if [ \"$last\" = --help ]; then\n" +
				"  path=$(printf '%s ' \"$@\" | sed -e 's/--help $//' -e 's/ $//' -e 's/ /_/g')\n" +
				"  exec cat '" + help + "'/\"${path:-gh}\".txt\nfi\n" +
				"echo rest >> \"$GH_STANDIN_RECORD\"\n" +
				"printf '%s\\r\\n' 'HTTP/2.0 200 OK' 'X-Ratelimit-Remaining: 4999' ''\nprintf '%s\\n' private\nexit 0\n"
			if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho git >> \"$GH_STANDIN_RECORD\"\n"+tc.git), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Chdir(t.TempDir())
			for name, value := range map[string]string{
				testMainEnv: "1", "PATH": ghWrapperDir(t) + ":" + bin + ":" + os.Getenv("PATH"),
				"LEGION_GH_PATH": stub, "GH_CONFIG_DIR": defaultDir, "LEGION_GH_OWNERS_DIR": root,
				"GH_TOKEN": "", "GITHUB_TOKEN": "", "GH_STANDIN_RECORD": record, "XDG_STATE_HOME": t.TempDir(),
				"PUBLIC_LEAK_CHECK": leakguardtest.Write(t, t.TempDir(), leakguardtest.Passes),
			} {
				t.Setenv(name, value)
			}
			// A ref the push deletes, named for a host the rules refuse: its name is the push's one
			// record, so a scan refuses the push and no scan passes it, and no git reads a commit.
			line := "refs/heads/work " + noSHA + " refs/heads/legion/" + theLeakCheckHost + " " + noSHA + "\n"
			code, printed := leakCheck(t, line, "pre-push", "origin", "https://github.com/"+tc.repo+".git")
			if code != tc.code {
				t.Errorf("exited %d, want %d: %s", code, tc.code, printed)
			}
			says(t, printed, tc.says)
			if got := readRecord(t, record); got != tc.reads {
				t.Errorf("the stand-ins recorded the reads %q, want %q", got, tc.reads)
			}
		})
	}
}
