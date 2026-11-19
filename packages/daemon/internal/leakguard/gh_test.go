package leakguard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// theProgram is the public_leak_check.py of a stand-in check these tests count as proven: its
// sha256 (hexSHA256) is the value a caller names in GhRequest.ProvenCheck.
const theProgram = "# stands for the shared check a read rule was proven against\n"

// A call its caller found writes nothing (GhRequest.WritesNothing), under the check its caller's
// rule was proven against (GhRequest.ProvenCheck), runs the shared check and nothing more. A check
// that clears it clears it with no visibility read and no private-name pass, though its argv
// names a host the rules refuse: a read leaves no text in any repository. A check that refuses
// it, one that cannot finish, and one its bound stops refuse it as they refuse any call. Under a
// check that is not the proven one — its program another file, or no file — and as a write, the
// same call reads the destination's visibility and is refused by the rules.
func TestAGhCallThatWritesNothingRunsTheSharedCheckAlone(t *testing.T) {
	const repo = "invented-org/invented-repo"
	args := []string{"search", "code", leakguardtest.RulesHost}
	proven := hexSHA256(theProgram)
	call := func(t *testing.T, ctx context.Context, check, program string, writesNothing bool) ([]string, GhAnswer, error) {
		t.Helper()
		var asked []string
		script := leakguardtest.Write(t, t.TempDir(), check)
		if program != "" {
			if err := os.WriteFile(filepath.Join(filepath.Dir(script), checkProgram), []byte(program), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		guard := New(Options{
			Check:      script,
			Visibility: answers(Public, &asked),
			Env:        withRules(t, fixtureenv.Isolated(t.TempDir())),
		})
		answer, err := guard.GhCall(ctx, GhRequest{Args: args, Repo: repo, Files: []GhFile{}, WritesNothing: writesNothing, ProvenCheck: proven})
		return asked, answer, err
	}
	t.Run("a read the check clears", func(t *testing.T) {
		if asked, answer, err := call(t, bound(t), passes, theProgram, true); err != nil || len(asked) != 0 || !answer.CheckedOnly {
			t.Fatalf("GhCall = %+v, %v, visibility asked of %q; want the read cleared by the check alone", answer, err, asked)
		}
	})
	t.Run("a read the check refuses", func(t *testing.T) {
		asked, _, err := call(t, bound(t), refuses, theProgram, true)
		refusal(t, err)
		if len(asked) != 0 {
			t.Fatalf("visibility asked of %q for a read the check refused", asked)
		}
	})
	t.Run("a read whose check cannot finish", func(t *testing.T) {
		_, _, err := call(t, bound(t), cannotFinish, theProgram, true)
		setupFailure(t, err)
	})
	t.Run("a read whose check runs past its bound", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		started := time.Now()
		asked, _, err := call(t, ctx, "cat >/dev/null 2>&1\nsleep 30\n", theProgram, true)
		answeredWithin10s(t, started)
		says(t, setupFailure(t, err), "the shared public leak check", "ran 2 s", "bound of 2 s")
		if len(asked) != 0 {
			t.Fatalf("visibility asked of %q for a read whose check was stopped", asked)
		}
	})
	for _, tc := range []struct {
		name          string
		program       string
		writesNothing bool
	}{
		{"a read under a check whose program is another file", theProgram + "# an edit\n", true},
		{"a read under a check with no program beside it", "", true},
		{"the same call as a write", theProgram, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked, answer, err := call(t, bound(t), passes, tc.program, tc.writesNothing)
			if report := refusal(t, err); len(report.Lines) != 1 || !namesClass(report.Lines[0], leakguardtest.RulesHostClass) {
				t.Fatalf("the refusal does not name the class: %v", report)
			}
			if !slices.Equal(asked, []string{repo}) || answer.CheckedOnly {
				t.Fatalf("visibility asked of %q and checked only %v, want %q asked once and the whole guard", asked, answer.CheckedOnly, repo)
			}
		})
	}
}

// hexSHA256 is the sha256 of text, in hex.
func hexSHA256(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// A gh call that carries its body in a file publishes the file's text, not the file's name, and
// the deployment's private-name rules must read that text.
//
// The shared public leak check already does: measured at leak-check-v15 from a pod on
// 2026-11-10, `public-leak-check.sh gh-guard pr comment 1 --body-file <file>` over a file holding
// a credential exits 3, so the check opens the file a body option names (its own BODY_FILES, and
// the `-F <key>=@<path>` form `gh api` takes). What it does not read is a deployment's private
// names: with PUBLIC_LEAK_CHECK_REPO_NAMES and PUBLIC_LEAK_CHECK_INTERNAL off, which R6 of
// dispatch://INFRA-1527 keeps them, the same call over a file holding a Tailscale MagicDNS host
// exits 0. So for those names the rule file is the whole of the cover, and a body option's file is
// a hole in it: the same host inline in `--body` is refused.
//
// This is the surface the issue was written for — 43 of its 60 measured refusals are text an
// agent posted to GitHub — and a file is how a Legion role posts it:
// skills/legion-retro/references/pr-body-content.md writes a pull request's body with
// `gh api --method PATCH repos/<owner>/<repo>/pulls/<n> -F body=@"$dir/body.md"`.
func TestAGhCallsBodyFileIsRead(t *testing.T) {
	body := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(body, []byte("the box is at "+leakguardtest.RulesHost+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "--body-file", args: []string{"pr", "comment", "1", "--body-file", body}},
		{name: "--notes-file", args: []string{"release", "create", "v0", "--notes-file", body}},
		{name: "-F body=@", args: []string{"api", "repos/invented-org/invented-repo/pulls/1", "--method", "PATCH", "-F", "body=@" + body}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			_, err := guard.GhCall(bound(t), GhRequest{Args: tc.args, Repo: "invented-org/invented-repo"})
			if err == nil {
				t.Fatal("the call was cleared, so the file's text reaches GitHub unread")
			}
			report := refusal(t, err)
			if !strings.Contains(report.Error(), leakguardtest.RulesHostClass) {
				t.Fatalf("the refusal does not name the class: %v", report)
			}
		})
	}
}

// A file a body option names that the guard cannot read is a setup failure naming the option,
// never a call passed through: a guard that did not read what the call publishes has cleared
// nothing, and a pass here would be the hole the test above closes by another door. The bound on
// the read (maxGhFile) is the second door: a file past it is refused rather than pulled into
// memory.
func TestAGhCallsBodyFileThatCannotBeReadIsASetupFailure(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(dir, "absent.md")
	huge := filepath.Join(dir, "huge.md")
	if err := os.WriteFile(huge, bytes.Repeat([]byte("a"), maxGhFile+1), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "a file that is not there",
			args: []string{"pr", "comment", "1", "--body-file", absent},
			want: "the file --body-file names",
		},
		{
			name: "a file past the bound",
			args: []string{"release", "create", "v0", "--notes-file", huge},
			want: "the file --notes-file names",
		},
		{
			name: "a field's file that is not there",
			args: []string{"api", "repos/invented-org/invented-repo/pulls/1", "-F", "body=@" + absent},
			want: "the file -F names",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			_, err := guard.GhCall(bound(t), GhRequest{Args: tc.args, Repo: "invented-org/invented-repo"})
			failed := setupFailure(t, err)
			if !strings.Contains(failed.Error(), tc.want) {
				t.Fatalf("the failure is %q, want it to name the option in %q", failed, tc.want)
			}
		})
	}
}

// A body option whose value is `-` is standard input, which the shared check's exit-4 handshake
// buffers and the guard records already (TestTheGhShapeScansWhatItBuffered): there is no file of
// that name to open, and a guard that tried would turn every such call into a setup failure.
func TestAGhCallsBodyFileOfStandardInputOpensNoFile(t *testing.T) {
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	if _, err := guard.GhCall(bound(t), GhRequest{
		Args: []string{"pr", "comment", "1", "--body-file", "-"},
		Repo: "invented-org/invented-repo",
	}); err != nil {
		t.Fatalf("a call whose body is standard input: %v", err)
	}
}

// gh 2.104.0's --attach uploads a file this guard does not read, so a bare argv naming it, the
// fallback's input, is refused naming the option, as `legion gh` refuses it before the guard:
// separately and with its value attached, on every leaf that carries it. The same argv past a
// bare `--` names no option gh reads and is cleared, and so is the same call without --attach.
func TestAGhCallsUploadTheGuardDoesNotReadIsASetupFailure(t *testing.T) {
	for _, args := range [][]string{
		{"pr", "comment", "1", "--body", "hi", "--attach", "notes.svg"},
		{"issue", "create", "--title", "t", "--body", "b", "--attach=notes.svg#alt"},
		{"issue", "artifact", "edit", "142", "2", "--attach", "./flow.png"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			_, err := guard.GhCall(bound(t), GhRequest{Args: args, Repo: "invented-org/invented-repo"})
			failed := setupFailure(t, err)
			if !strings.Contains(failed.Error(), "--attach uploads the file it names, which this guard does not read") {
				t.Fatalf("the failure is %q, want it to name --attach", failed)
			}
		})
	}
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	for _, args := range [][]string{
		{"pr", "comment", "1", "--body", "hi"},
		{"repo", "clone", "invented-org/invented-repo", "--", "--attach", "notes.svg"},
	} {
		if _, err := guard.GhCall(bound(t), GhRequest{Args: args, Repo: "invented-org/invented-repo"}); err != nil {
			t.Fatalf("GhCall(%q): %v, want it cleared", args, err)
		}
	}
}

// A body file naming nothing private is cleared, which is what makes the refusal above a report
// of the text rather than of the option.
func TestAGhCallsCleanBodyFileIsCleared(t *testing.T) {
	body := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(body, []byte("the box is in the other room\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	if _, err := guard.GhCall(bound(t), GhRequest{
		Args: []string{"pr", "comment", "1", "--body-file", body},
		Repo: "invented-org/invented-repo",
	}); err != nil {
		t.Fatalf("a clean body file: %v", err)
	}
}

// The option table itself: which arguments name a file gh would send as content, in both
// spellings of each, and which name no file at all, and which option names standard input. The
// entries that answer nothing are the table's boundary, and each is a call that must not be
// refused over a file nobody named.
func TestTheFilesAGhCallWouldSend(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		want  []GhFile
		stdin string
	}{
		{
			name: "a separate --body-file",
			args: []string{"pr", "comment", "1", "--body-file", "body.md"},
			want: []GhFile{{Option: "--body-file", Path: "body.md"}},
		},
		{
			name: "an attached --body-file",
			args: []string{"pr", "create", "--body-file=body.md"},
			want: []GhFile{{Option: "--body-file", Path: "body.md"}},
		},
		{
			name: "--notes-file",
			args: []string{"release", "create", "v0", "--notes-file", "notes.md"},
			want: []GhFile{{Option: "--notes-file", Path: "notes.md"}},
		},
		{
			name: "gh api's --input",
			args: []string{"api", "repos/invented-org/invented-repo/pulls", "--input", "request.json"},
			want: []GhFile{{Option: "--input", Path: "request.json"}},
		},
		{
			name: "gh agent-task create's --from-file",
			args: []string{"agent-task", "create", "--from-file", "task.md"},
			want: []GhFile{{Option: "--from-file", Path: "task.md"}},
		},
		{
			name: "a short -F whose value holds no =",
			args: []string{"pr", "comment", "1", "-F", "body.md"},
			want: []GhFile{{Option: "-F", Path: "body.md"}},
		},
		{
			name: "a short -F with its value attached",
			args: []string{"pr", "comment", "1", "-Fbody.md"},
			want: []GhFile{{Option: "-F", Path: "body.md"}},
		},
		{
			// pflag reads `-F=<value>` as `-F <value>`, and so does this reader (ghFieldArg).
			name: "a short -F with its value attached by an equals sign",
			args: []string{"pr", "create", "-F=body.md"},
			want: []GhFile{{Option: "-F", Path: "body.md"}},
		},
		{
			name: "a field read from a file",
			args: []string{"api", "repos/invented-org/invented-repo/pulls/1", "-F", "body=@body.md"},
			want: []GhFile{{Option: "-F", Path: "body.md", Field: "body"}},
		},
		{
			name: "a field read from a file, attached to -F by an equals sign",
			args: []string{"api", "repos/invented-org/invented-repo/pulls/1", "-F=body=@notes.md"},
			want: []GhFile{{Option: "-F", Path: "notes.md", Field: "body"}},
		},
		{
			name: "a GraphQL query read from a file, attached to -F by an equals sign",
			args: []string{"api", "graphql", "-F=query=@q.graphql"},
			want: []GhFile{{Option: "-F", Path: "q.graphql", Field: "query"}},
		},
		{
			name: "an attached --field read from a file",
			args: []string{"api", "repos/invented-org/invented-repo/pulls/1", "--field=body=@body.md"},
			want: []GhFile{{Option: "--field", Path: "body.md", Field: "body"}},
		},
		{
			name: "a separate --field read from a file",
			args: []string{"api", "graphql", "--field", "query=@query.graphql"},
			want: []GhFile{{Option: "--field", Path: "query.graphql", Field: "query"}},
		},
		{
			// gh api's short letters are all known, so its cluster is read as pflag reads it: `-i`
			// is --include, and the -F that ends the cluster is --field (ghFieldArg).
			name: "a field read from a file, at the end of gh api's short cluster",
			args: []string{"api", "repos/invented-org/invented-repo/pulls/1", "-iF", "body=@body.md"},
			want: []GhFile{{Option: "-F", Path: "body.md", Field: "body"}},
		},
		{
			name: "a field read from a file, attached to gh api's short cluster",
			args: []string{"api", "repos/invented-org/invented-repo/pulls/1", "-iFbody=@notes.md"},
			want: []GhFile{{Option: "-F", Path: "notes.md", Field: "body"}},
		},
		{
			name: "the same file named twice",
			args: []string{"api", "repos/invented-org/invented-repo/pulls/1", "-F", "body=@body.md", "--body-file", "body.md"},
			want: []GhFile{{Option: "-F", Path: "body.md", Field: "body"}},
		},
		{name: "a field whose value is a literal", args: []string{"api", "x", "-F", "body=text"}},
		{name: "a raw field, which sends its value literally", args: []string{"api", "x", "-f", "body=@body.md"}},
		{name: "an attached raw field, which sends its value literally", args: []string{"api", "x", "-f=body=@body.md"}},
		{name: "a --field naming a project's field", args: []string{"project", "field-list", "1", "--field", "Status"}},
		{name: "a body option whose value is standard input", args: []string{"pr", "comment", "1", "--body-file", "-"}, stdin: "--body-file"},
		{name: "a field read from standard input", args: []string{"api", "x", "-F", "body=@-"}, stdin: "--field"},
		{name: "a long field read from standard input", args: []string{"api", "x", "--field", "body=@-"}, stdin: "--field"},
		{name: "a field read from standard input, at the end of gh api's short cluster", args: []string{"api", "graphql", "-iF", "query=@-"}, stdin: "--field"},
		// A cluster whose first value letter is another option's: `-iXF` is --include and the
		// method F, so the argument after it is a positional of gh's and names no file.
		{name: "gh api's short cluster ending in another option", args: []string{"api", "x", "-iXF", "body=@body.md"}},
		{name: "gh api's request body on standard input", args: []string{"api", "x", "--input", "-"}, stdin: "--input"},
		{name: "gh api's attached request body on standard input", args: []string{"api", "x", "--input=-"}, stdin: "--input"},
		{name: "release notes on standard input", args: []string{"release", "create", "v0", "--notes-file", "-"}, stdin: "--notes-file"},
		// -F with no `=` is one of three options whose input is text, and the leaf alone says which.
		{name: "a short -F of standard input", args: []string{"pr", "comment", "1", "-F", "-"}, stdin: "-F"},
		{
			name:  "a file beside standard input",
			args:  []string{"api", "x", "--input", "-", "-F", "body=@body.md"},
			want:  []GhFile{{Option: "-F", Path: "body.md", Field: "body"}},
			stdin: "--input",
		},
		// Only one of two options can have the input, so the guard is told it does not know.
		{name: "two options of standard input that differ", args: []string{"api", "x", "--input", "-", "-F", "body=@-"}},
		{name: "one option of standard input twice", args: []string{"api", "x", "-F", "title=@-", "-F", "body=@-"}, stdin: "--field"},
		{name: "a secret's env file", args: []string{"secret", "set", "NAME", "--env-file", "secrets.env"}},
		{name: "a template of the repository", args: []string{"pr", "create", "--template", "pull_request_template.md"}},
		{name: "a gist's positional files", args: []string{"gist", "create", "note.md"}},
		{name: "a short option whose own value is F", args: []string{"gist", "create", "-dF", "note.md"}},
		// The shape this reader cannot tell apart from a call that names no file, and the reason
		// `legion gh` hands the files it parsed instead (GhRequest.Files): a short cluster's last
		// letter is the option in `gh pr create` (`--draft --body-file`) and the value of the
		// option before it in `gh gist create` (`--desc F`), and only the leaf's own help says
		// which.
		{name: "a short cluster whose last letter is the option", args: []string{"pr", "create", "-dF", "body.md"}},
		{name: "an option past a double dash", args: []string{"repo", "clone", "invented-org/invented-repo", "--", "--body-file", "body.md"}},
		{name: "standard input past a double dash", args: []string{"repo", "clone", "invented-org/invented-repo", "--", "--input", "-"}},
		{name: "an option with no value at all", args: []string{"pr", "comment", "1", "--body-file"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, stdin := ghFileArgs(tc.args); !slices.Equal(got, tc.want) || stdin != tc.stdin {
				t.Fatalf("ghFileArgs(%q) = %v, %q; want %v, %q", tc.args, got, stdin, tc.want, tc.stdin)
			}
		})
	}
}

// The files the caller parsed are the files read, and the table is not consulted for a caller
// that handed any: `legion gh` reads the command with gh's own help of the leaf, which knows
// shapes the table cannot (cmd/legion's ghSentFiles), and two readers of one argv would be two
// answers to one question. A caller that hands none at all — nil, which is what a bare argv is
// — still gets the table.
func TestTheFilesTheCallerParsedAreTheFilesRead(t *testing.T) {
	dir := t.TempDir()
	named := filepath.Join(dir, "named.md")
	unnamed := filepath.Join(dir, "unnamed.md")
	for _, path := range []string{named, unnamed} {
		if err := os.WriteFile(path, []byte("the box is at "+leakguardtest.RulesHost+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("a file the argv never spells is read", func(t *testing.T) {
		// The shape the table misses and the parse does not: `-dF <path>` is `--draft
		// --body-file <path>` to gh, and the caller hands the file it read out of that.
		guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
		_, err := guard.GhCall(bound(t), GhRequest{
			Args:  []string{"pr", "create", "-dF", unnamed},
			Repo:  "invented-org/invented-repo",
			Files: []GhFile{{Option: "--body-file", Path: unnamed}},
		})
		if report := refusal(t, err); !strings.Contains(report.Error(), leakguardtest.RulesHostClass) {
			t.Fatalf("the refusal does not name the class: %v", report)
		}
	})
	t.Run("a caller that parsed no file reads none", func(t *testing.T) {
		guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
		if _, err := guard.GhCall(bound(t), GhRequest{
			Args:  []string{"pr", "comment", "1", "--body-file", named},
			Repo:  "invented-org/invented-repo",
			Files: []GhFile{},
		}); err != nil {
			t.Fatalf("a call whose caller parsed no file: %v", err)
		}
	})
	t.Run("a caller that parsed nothing gets the table", func(t *testing.T) {
		guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
		_, err := guard.GhCall(bound(t), GhRequest{
			Args: []string{"pr", "comment", "1", "--body-file", named},
			Repo: "invented-org/invented-repo",
		})
		if report := refusal(t, err); !strings.Contains(report.Error(), leakguardtest.RulesHostClass) {
			t.Fatalf("the refusal does not name the class: %v", report)
		}
	})
}

// gh api's short cluster (ghFieldArg) is gh api's alone. In `gh pr create`, `-dF <path>` is
// `--draft --body-file <path>`: the file the caller parsed out of it is read as the body it is,
// and the argv makes no field of it. The file's name holds an `=`, so a reader that took `-dF`
// for gh api's field option would make a field of the path.
func TestAShortClusterOutsideGhAPIIsNoField(t *testing.T) {
	body := fieldFile(t, "notes=draft.md", "the "+leakguardtest.PairName+" and the "+leakguardtest.PairWord)
	args := []string{"pr", "create", "-dF", body}
	for _, record := range ghArgRecords(args, false) {
		if strings.HasPrefix(record.Label, "the gh call's field ") {
			t.Fatalf("ghArgRecords(%q) made the field record %q", args, record.Label)
		}
	}
	err := ghFieldCall(t, GhRequest{Args: args, Files: []GhFile{{Option: "--body-file", Path: body}}})
	reports(t, refusal(t, err).Lines, body+": "+leakguardtest.PairClass)
}

// In a `gh api` call a short cluster is read as pflag reads it (ghFieldArg): `-iF query=<v>`,
// `-iFquery=<v>` and `-if query=<v>` are the query field after --include. So a GraphQL query given
// so is read one literal at a time: the fixture's two words in two literals of one mutation pass,
// though a read of the argument whole joins them, and the two in one literal are refused, the
// report naming the literal.
func TestGhAPIShortClusterQueryReadsOneLiteralAtATime(t *testing.T) {
	apart := graphQLMutation("the "+leakguardtest.PairName+" notes", "a "+leakguardtest.PairWord+" aside")
	together := graphQLMutation("a clean title", "the "+leakguardtest.PairName+" and the "+leakguardtest.PairWord)
	refusedWhole(t, apart)
	for _, tc := range []struct {
		name string
		args func(query string) []string
	}{
		{"-iF query=", func(query string) []string { return []string{"api", "graphql", "-iF", "query=" + query} }},
		{"-iFquery=", func(query string) []string { return []string{"api", "graphql", "-iFquery=" + query} }},
		{"-if query=", func(query string) []string { return []string{"api", "graphql", "-if", "query=" + query} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cleared(t, ghFieldCall(t, GhRequest{Args: tc.args(apart)}))
			err := ghFieldCall(t, GhRequest{Args: tc.args(together)})
			reports(t, refusal(t, err).Lines, "the gh call's field query, GraphQL string 3: "+leakguardtest.PairClass)
		})
	}
}

// Standard input a field other than `query` takes in a `gh api graphql` call is text GitHub stores
// as it stands, one record: a mutation on it holding the fixture's two words in two literals is
// refused, since that input is a body and no query. A query and another field both of standard
// input are one input gh reads into one of them, so it is read both ways, raw and one literal at
// a time: the two words apart are refused by the raw record, and a name escaped inside one literal
// by that literal. A query of standard input that a caller's parse names no option for, where
// the argv names the field, is read one literal at a time.
func TestGhAPIGraphQLStandardInputIsReadByTheFieldsThatTakeIt(t *testing.T) {
	apart := graphQLMutation("the "+leakguardtest.PairName+" notes", "a "+leakguardtest.PairWord+" aside")
	escaped := graphQLMutation("the "+strings.Replace(leakguardtest.PairName, "l", `\u006c`, 1)+" and the "+leakguardtest.PairWord, "a clean body")
	refusedWhole(t, apart)
	if strings.Contains(escaped, leakguardtest.PairName) {
		t.Fatalf("the query %q spells the name in its own bytes, so it proves nothing", escaped)
	}
	const stdin = "the gh call's standard input"
	for _, tc := range []struct{ name, stdinOption string }{
		{"read out of the argv", ""},
		{"named by the caller's parse", "--field"},
	} {
		t.Run("another field's input is text, "+tc.name, func(t *testing.T) {
			err := ghFieldCall(t, GhRequest{Args: []string{"api", "graphql", "-F", "body=@-"}, Stdin: strings.NewReader(apart), StdinOption: tc.stdinOption})
			reports(t, refusal(t, err).Lines, stdin+": "+leakguardtest.PairClass)
		})
	}
	t.Run("a query and another field are read both ways", func(t *testing.T) {
		args := []string{"api", "graphql", "-F", "query=@-", "-F", "body=@-"}
		err := ghFieldCall(t, GhRequest{Args: args, Stdin: strings.NewReader(apart)})
		reports(t, refusal(t, err).Lines, stdin+": "+leakguardtest.PairClass)
		err = ghFieldCall(t, GhRequest{Args: args, Stdin: strings.NewReader(escaped)})
		reports(t, refusal(t, err).Lines, stdin+", GraphQL string 2: "+leakguardtest.PairClass)
	})
	t.Run("a query no option is known for", func(t *testing.T) {
		call := func(query string) GhRequest {
			return GhRequest{Args: []string{"api", "graphql", "-F", "query=@-"}, Stdin: strings.NewReader(query), Files: []GhFile{}}
		}
		cleared(t, ghFieldCall(t, call(apart)))
		reports(t, refusal(t, ghFieldCall(t, call(escaped))).Lines, stdin+", GraphQL string 2: "+leakguardtest.PairClass)
	})
}

// A JSON body is read decoded. A string's escapes are text GitHub stores and shows — `\u0074` is
// the letter t — and an escape inside a name parts a word the rules read whole, so a request body
// that spells a host in escapes names that host as surely as one that spells it out. Both doors a
// JSON body comes through are driven here: `gh api --input <file>` and the standard input the
// shared check's exit-4 handshake buffers.
func TestAJsonBodysEscapesAreRead(t *testing.T) {
	// The host, with the first letter of each of its three labels written as a JSON escape, so
	// no pattern of the rule file matches the bytes of the document itself.
	escaped := `{"body": "the box is at \u0061mber-kite.\u0073till-meadow.ts.\u006eet"}`
	if strings.Contains(escaped, leakguardtest.RulesHost) {
		t.Fatal("the fixture spells the host in the document's own bytes, so it proves nothing")
	}
	t.Run("gh api --input", func(t *testing.T) {
		body := filepath.Join(t.TempDir(), "request.json")
		if err := os.WriteFile(body, []byte(escaped), 0o600); err != nil {
			t.Fatal(err)
		}
		guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
		_, err := guard.GhCall(bound(t), GhRequest{
			Args: []string{"api", "repos/invented-org/invented-repo/pulls/1", "--method", "PATCH", "--input", body},
			Repo: "invented-org/invented-repo",
		})
		report := refusal(t, err)
		reports(t, report.Lines, body+", JSON .body: "+leakguardtest.RulesHostClass)
	})
	t.Run("the buffered standard input", func(t *testing.T) {
		guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), leakguardtest.Reading(0, "")), Public)
		_, err := guard.GhCall(bound(t), GhRequest{
			Args:  []string{"api", "repos/invented-org/invented-repo/pulls/1", "--method", "PATCH", "--input", "-"},
			Stdin: strings.NewReader(escaped),
			Repo:  "invented-org/invented-repo",
		})
		report := refusal(t, err)
		reports(t, report.Lines, "the gh call's standard input, JSON .body: "+leakguardtest.RulesHostClass)
	})
	t.Run("a JSON body naming nothing is cleared", func(t *testing.T) {
		body := filepath.Join(t.TempDir(), "request.json")
		if err := os.WriteFile(body, []byte(`{"body": "the box is in the other \u0072oom"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
		if _, err := guard.GhCall(bound(t), GhRequest{
			Args: []string{"api", "repos/invented-org/invented-repo/pulls/1", "--input", body},
			Repo: "invented-org/invented-repo",
		}); err != nil {
			t.Fatalf("a clean JSON body: %v", err)
		}
	})
}

// The standard input the check asks for is bounded as a named file is: the bytes are held in
// memory and scanned there. Input past the bound is a setup failure, which refuses the call,
// never a truncated buffer handed to gh as though the guard had read the whole.
func TestStandardInputPastTheBoundIsASetupFailure(t *testing.T) {
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), leakguardtest.Reading(0, "")), Public)
	answer, err := guard.GhCall(bound(t), GhRequest{
		Args:  []string{"pr", "create", "--body-file", "-"},
		Stdin: bytes.NewReader(bytes.Repeat([]byte("a"), maxGhFile+1)),
		Repo:  "invented-org/invented-repo",
	})
	failed := setupFailure(t, err)
	if !strings.Contains(failed.Error(), "standard input is longer than") {
		t.Fatalf("the failure is %q, want it to name the bound", failed)
	}
	if answer.Stdin != nil {
		t.Errorf("the caller got %d bytes back of an input the guard never read whole", len(answer.Stdin))
	}
}
