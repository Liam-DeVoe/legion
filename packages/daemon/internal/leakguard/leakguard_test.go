package leakguard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// noSHA is git's "this ref is not there", which a pre-push line carries for a ref the push
// creates and for one it deletes.
const noSHA = "0000000000000000000000000000000000000000"

// The three stand-in shared checks these tests drive the guard against, which are the shared
// ones: each reads its standard input, as the real check does, so that nothing is left in a pipe
// when it exits.
const (
	passes       = leakguardtest.Passes
	refuses      = leakguardtest.Refuses
	cannotFinish = leakguardtest.CannotFinish
)

// answers is a visibility reader that answers visibility for every slug it is asked about, and
// counts what it was asked.
func answers(visibility Visibility, asked *[]string) func(context.Context, string) (Visibility, error) {
	return func(_ context.Context, slug string) (Visibility, error) {
		if asked != nil {
			*asked = append(*asked, slug)
		}
		return visibility, nil
	}
}

// guardWith is a guard whose shared check is check, whose destinations are all visibility, and
// whose children run under an environment of the test's own: no credential, no repository and no
// workspace but the test's (fixtureenv.Isolated), the fixture rule file named (withRules), and
// extra on top.
func guardWith(t *testing.T, check string, visibility Visibility, extra ...string) *Guard {
	t.Helper()
	return New(Options{
		Check:      check,
		Visibility: answers(visibility, nil),
		Env:        withRules(t, append(fixtureenv.Isolated(t.TempDir()), extra...)),
	})
}

// withRules is env with RulesFileVar naming the fixture rule file (leakguardtest.Rules), written
// into a directory of the test's own: the guard runs the rules a deployment names, so a test of
// the rule pass names the fixture as a deployment names its file.
func withRules(t *testing.T, env []string) []string {
	t.Helper()
	return append(env, RulesFileVar+"="+leakguardtest.WriteRules(t, t.TempDir()))
}

// fixtureRules is the fixture rule file, compiled.
func fixtureRules(t *testing.T) *RuleSet {
	t.Helper()
	rules, err := LoadRules([]byte(leakguardtest.Rules))
	if err != nil {
		t.Fatalf("the fixture rules did not compile: %v", err)
	}
	return rules
}

// bound is a context every call of these tests runs under, so that a stand-in that hangs fails
// the test rather than the package's ten minutes.
func bound(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return ctx
}

// refusal is err as a *Refusal, failing the test where it is anything else.
func refusal(t *testing.T, err error) *Refusal {
	t.Helper()
	var refused *Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("got %v, want a refusal", err)
	}
	return refused
}

// reports fails the test unless lines, a rule pass's report, is one line for each of want, in
// order, each naming the record and the class want names ("<record>: <class>"). A line is read by
// its front: what the report writes after the class, the match and its line, is not this test's
// (TestPushRefusalShowsMatch and TestRefusalShowsMatchWithinItsBounds read whole lines).
func reports(t *testing.T, lines []string, want ...string) {
	t.Helper()
	same := len(lines) == len(want)
	for i := 0; same && i < len(want); i++ {
		same = lines[i] == want[i] || strings.HasPrefix(lines[i], want[i]+": ")
	}
	if !same {
		t.Errorf("the report is %q, want a line of each of %q", lines, want)
	}
}

// namesClass is whether one line of a rule pass's report names class, whatever its record.
func namesClass(line, class string) bool {
	return strings.HasSuffix(line, ": "+class) || strings.Contains(line, ": "+class+": ")
}

// setupFailure is err as a *SetupFailure, failing the test where it is anything else.
func setupFailure(t *testing.T, err error) *SetupFailure {
	t.Helper()
	var failed *SetupFailure
	if !errors.As(err, &failed) {
		t.Fatalf("got %v, want a setup failure", err)
	}
	return failed
}

// Every entry point runs its own shape of the shared check and refuses what that check refuses,
// and every one of them refuses a record of its own that names something private. Rules is the
// one that runs no check at all: it refuses and passes under a stand-in that refuses everything,
// which is how this says that the daemon's branch create reaches no check.
func TestEachEntryPointRefusesWhatTheCheckRefusesAndWhatTheRulesMatch(t *testing.T) {
	cleanPush := PushRequest{
		Dir:     t.TempDir(),
		Remote:  "origin",
		URL:     "https://github.com/invented-org/invented-repo.git",
		Updates: []Update{{LocalRef: "refs/heads/main", LocalSHA: noSHA, RemoteRef: "refs/heads/main", RemoteSHA: noSHA}},
	}
	namedPush := cleanPush
	namedPush.Updates = []Update{{LocalRef: "refs/heads/work", LocalSHA: noSHA, RemoteRef: "refs/heads/" + leakguardtest.RulesHost, RemoteSHA: noSHA}}
	for _, entry := range []struct {
		name string
		// checked runs the entry point against guard, with a record that names something
		// private when named.
		checked func(ctx context.Context, guard *Guard, named bool) error
		// readsCheck is whether the entry point runs the shared check at all.
		readsCheck bool
	}{
		{
			name: "Push",
			checked: func(ctx context.Context, guard *Guard, named bool) error {
				request := cleanPush
				if named {
					request = namedPush
				}
				return guard.Push(ctx, request)
			},
			readsCheck: true,
		},
		{
			name: "GhCall",
			checked: func(ctx context.Context, guard *Guard, named bool) error {
				args := []string{"pr", "create", "--title", "a clean title"}
				if named {
					args = []string{"pr", "create", "--title", "a title naming " + leakguardtest.RulesHost}
				}
				_, err := guard.GhCall(ctx, GhRequest{Args: args, Repo: "invented-org/invented-repo"})
				return err
			},
			readsCheck: true,
		},
		{
			name: "Text",
			checked: func(ctx context.Context, guard *Guard, named bool) error {
				text := "a clean title\n\na clean body\n"
				if named {
					text = "a clean title\n\na body naming " + leakguardtest.RulesHost + "\n"
				}
				return guard.Text(ctx, TextRequest{Repo: "invented-org/invented-repo", Text: text})
			},
			readsCheck: true,
		},
		{
			name: "Rules",
			checked: func(ctx context.Context, guard *Guard, named bool) error {
				ref := "legion/INVENTED-1"
				if named {
					ref = "legion/" + leakguardtest.RulesHost
				}
				return guard.Rules(ctx, RefRecord(ref))
			},
		},
	} {
		t.Run(entry.name, func(t *testing.T) {
			ctx := bound(t)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			if err := entry.checked(ctx, guard, false); err != nil {
				t.Fatalf("a clean write: %v", err)
			}
			refused := refusal(t, entry.checked(ctx, guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public), true))
			if refused.By != byRules {
				t.Errorf("a write naming a host was refused by %q, want the rules", refused.By)
			}
			if len(refused.Lines) != 1 || !namesClass(refused.Lines[0], leakguardtest.RulesHostClass) {
				t.Errorf("the report is %q, want one line naming the class", refused.Lines)
			}
			byCheck := guardWith(t, leakguardtest.Write(t, t.TempDir(), refuses), Public)
			err := entry.checked(ctx, byCheck, false)
			if !entry.readsCheck {
				if err != nil {
					t.Fatalf("a clean write reached the shared check: %v", err)
				}
				return
			}
			if refused := refusal(t, err); refused.By != bySharedCheck {
				t.Errorf("a write the check refused was refused by %q, want the shared check", refused.By)
			}
		})
	}
}

// The gh shape's second ask: the stand-in exits 4 to say the call reads standard input, the guard
// buffers that input to a regular file it hands over as a descriptor, the stand-in reads the file
// twice over, and the caller gets the bytes back to hand gh in its place.
func TestTheGhShapeBuffersStandardInputWhenTheCheckAsksForIt(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	asksForStdin := `if [ -z "${PUBLIC_LEAK_CHECK_STDIN}" ]; then
  echo 'this call reads standard input'
  exit 4
fi
{
  echo "buffers=${PUBLIC_LEAK_CHECK_STDIN_BUFFERS}"
  if [ -f "${PUBLIC_LEAK_CHECK_STDIN}" ]; then echo "regular=yes"; else echo "regular=no"; fi
  echo "first=$(cat "${PUBLIC_LEAK_CHECK_STDIN}")"
  echo "again=$(cat "${PUBLIC_LEAK_CHECK_STDIN}")"
} > "${STANDIN_RECORD}"
exit 0
`
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), asksForStdin), Public, "STANDIN_RECORD="+record)
	body := "a body gh reads on its standard input"
	answer, err := guard.GhCall(bound(t), GhRequest{
		Args:  []string{"pr", "create", "--body-file", "-"},
		Stdin: strings.NewReader(body),
		Repo:  "invented-org/invented-repo",
	})
	if err != nil {
		t.Fatalf("a clean call: %v", err)
	}
	if string(answer.Stdin) != body {
		t.Errorf("the caller got %q back, want the bytes it gave", answer.Stdin)
	}
	read, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"buffers=1", "regular=yes", "first=" + body, "again=" + body} {
		if !strings.Contains(string(read), want) {
			t.Errorf("the check read %q, want it to hold %q", read, want)
		}
	}
}

// What the gh shape buffered is scanned as a record of its own, so a body that never reaches an
// argument is read all the same.
func TestTheGhShapeScansWhatItBuffered(t *testing.T) {
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), leakguardtest.Reading(0, "")), Public)
	body := "a body naming " + leakguardtest.RulesHost + "\n"
	answer, err := guard.GhCall(bound(t), GhRequest{
		Args:  []string{"pr", "create", "--body-file", "-"},
		Stdin: strings.NewReader(body),
		Repo:  "invented-org/invented-repo",
	})
	if string(answer.Stdin) != body {
		t.Errorf("the caller got %q back, want the bytes it gave", answer.Stdin)
	}
	refused := refusal(t, err)
	reports(t, refused.Lines, "the gh call's standard input: "+leakguardtest.RulesHostClass)
}

// A destination whose visibility cannot be read refuses the write, as a failure of setup rather
// than of the text: a guard that cannot tell a public repository from a private one has checked
// nothing.
func TestAVisibilityThatCannotBeReadRefuses(t *testing.T) {
	guard := New(Options{
		Check: leakguardtest.Write(t, t.TempDir(), passes),
		Visibility: func(context.Context, string) (Visibility, error) {
			return "", errors.New("gh api exited 1")
		},
		Env: withRules(t, fixtureenv.Isolated(t.TempDir())),
	})
	err := guard.Text(bound(t), TextRequest{Repo: "invented-org/invented-repo", Text: "a clean title\n"})
	failed := setupFailure(t, err)
	if !strings.Contains(failed.Why, "visibility") {
		t.Errorf("the setup failure is %q, want it to name the visibility", failed.Error())
	}
}

// A destination GitHub reports private or internal runs the shared check and skips the rule pass:
// a name of ours in a repository nobody outside the company can read is no leak, and that is the
// same line the shared check draws for its own content scan.
func TestAPrivateOrInternalDestinationSkipsTheRules(t *testing.T) {
	for _, visibility := range []Visibility{Private, Internal} {
		t.Run(string(visibility), func(t *testing.T) {
			var asked []string
			record := filepath.Join(t.TempDir(), "record")
			guard := New(Options{
				Check:      leakguardtest.Write(t, t.TempDir(), leakguardtest.Recording(leakguardtest.Passes)),
				Visibility: answers(visibility, &asked),
				Env:        withRules(t, append(fixtureenv.Isolated(t.TempDir()), "LEAK_CHECK_RECORD="+record)),
			})
			text := "a title naming " + leakguardtest.RulesHost + "\n"
			if err := guard.Text(bound(t), TextRequest{Repo: "invented-org/invented-repo", Text: text}); err != nil {
				t.Fatalf("a write to a %s repository: %v", visibility, err)
			}
			if _, err := os.Stat(record); err != nil {
				t.Errorf("the shared check did not run: %v", err)
			}
			if len(asked) != 1 {
				t.Errorf("the visibility was read %d times, want once", len(asked))
			}
		})
	}
}

// Public answers remain for a role's pod life. Private and internal answers are read for every
// guarded call, and a failed read leaves no answer behind for a later call to mistake for a
// completed check.
func TestVisibilityKeepsAnswersButNeverFailures(t *testing.T) {
	t.Run("public", func(t *testing.T) {
		var reads int
		env := append(fixtureenv.Isolated(t.TempDir()), "XDG_STATE_HOME="+t.TempDir())
		read := func(context.Context, string) (Visibility, error) {
			reads++
			return Public, nil
		}
		for range 2 {
			visibility, err := New(Options{Visibility: read, Env: env}).Visibility(bound(t), "invented-org/invented-repo")
			if err != nil || visibility != Public {
				t.Fatalf("read visibility = %q, %v; want public", visibility, err)
			}
		}
		if reads != 1 {
			t.Errorf("the visibility was read %d times, want once", reads)
		}
	})

	t.Run("private and internal", func(t *testing.T) {
		for _, expected := range []Visibility{Private, Internal} {
			t.Run(string(expected), func(t *testing.T) {
				var reads int
				env := append(fixtureenv.Isolated(t.TempDir()), "XDG_STATE_HOME="+t.TempDir())
				read := func(context.Context, string) (Visibility, error) {
					reads++
					return expected, nil
				}
				for range 2 {
					visibility, err := New(Options{Visibility: read, Env: env}).Visibility(bound(t), "invented-org/invented-repo")
					if err != nil || visibility != expected {
						t.Fatalf("read visibility = %q, %v; want %q", visibility, err, expected)
					}
				}
				if reads != 2 {
					t.Errorf("the %s visibility was read %d times, want twice", expected, reads)
				}
			})
		}
	})

	t.Run("failed", func(t *testing.T) {
		var reads int
		env := append(fixtureenv.Isolated(t.TempDir()), "XDG_STATE_HOME="+t.TempDir())
		read := func(context.Context, string) (Visibility, error) {
			reads++
			if reads == 1 {
				return "", errors.New("the read failed")
			}
			return Public, nil
		}
		if _, err := New(Options{Visibility: read, Env: env}).Visibility(bound(t), "invented-org/invented-repo"); err == nil {
			t.Fatal("the first visibility read passed, want its failure")
		}
		if visibility, err := New(Options{Visibility: read, Env: env}).Visibility(bound(t), "invented-org/invented-repo"); err != nil || visibility != Public {
			t.Fatalf("the second visibility read = %q, %v; want public", visibility, err)
		}
		if reads != 2 {
			t.Errorf("the failed visibility read was kept: read %d times, want twice", reads)
		}
	})
}

// A push reads a private destination again after a preceding guarded call: only public answers
// are kept, so a repository made public cannot skip the rule pass.
func TestPushUsesTheRoleKeptVisibility(t *testing.T) {
	state := t.TempDir()
	env := append(fixtureenv.Isolated(t.TempDir()), "XDG_STATE_HOME="+state)
	var reads int
	read := func(context.Context, string) (Visibility, error) {
		reads++
		return Private, nil
	}
	if visibility, err := New(Options{Visibility: read, Env: env}).Visibility(bound(t), "invented-org/invented-repo"); err != nil || visibility != Private {
		t.Fatalf("read visibility = %q, %v; want private", visibility, err)
	}
	err := New(Options{
		Check:      leakguardtest.Write(t, t.TempDir(), passes),
		Visibility: read,
		Env:        withRules(t, env),
	}).Push(bound(t), PushRequest{
		Dir:    t.TempDir(),
		Remote: "origin",
		URL:    "https://github.com/invented-org/invented-repo.git",
	})
	if err != nil {
		t.Fatalf("push after the visibility read: %v", err)
	}
	if reads != 2 {
		t.Errorf("the visibility was read %d times, want twice", reads)
	}
}

// The two shared-check failures: no check resolves at all, and a check that exits 2. Each refuses
// the write, and each says it is a failure of setup rather than of the text.
func TestTheSharedChecksFailuresAreSetupFailures(t *testing.T) {
	t.Run("no check resolves", func(t *testing.T) {
		// An environment with no PUBLIC_LEAK_CHECK, a PATH with no check on it, and a
		// configuration home holding no pointer file: the three places Resolve looks.
		guard := New(Options{
			Visibility: answers(Public, nil),
			Env:        append(fixtureenv.Isolated(t.TempDir()), "PATH="+t.TempDir()),
		})
		err := guard.Text(bound(t), TextRequest{Repo: "invented-org/invented-repo", Text: "a clean title\n"})
		failed := setupFailure(t, err)
		if failed.Why != whyNoCheck {
			t.Errorf("the setup failure is %q, want the missing check", failed.Error())
		}
	})
	t.Run("the check exits 2", func(t *testing.T) {
		guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), cannotFinish), Public)
		err := guard.Text(bound(t), TextRequest{Repo: "invented-org/invented-repo", Text: "a clean title\n"})
		failed := setupFailure(t, err)
		if !strings.Contains(failed.Why, "exit 2") {
			t.Errorf("the setup failure is %q, want it to name the status", failed.Error())
		}
		if !strings.Contains(failed.Error(), "LEAK CHECK SETUP ERROR") {
			t.Errorf("the message is %q, want the shared check's own setup-error line", failed.Error())
		}
	})
}
