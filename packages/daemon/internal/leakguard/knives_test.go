package leakguard

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sjawhar/legion/daemon/internal/fixtureenv"
	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// The knives pass of a push (Guard.knives). The fixtures and the case of a line that holds a term
// are in knives_refusal_test.go, which says why they are a file of their own.

// knivesOverlayRow is an overlay from a private repository, which adds one term to the entry of
// theKnivesRegistry: a row can name a private repository, whose repository, path and terms no
// message may carry.
var knivesOverlayRow = knivesServed{
	KnivesRegistry: KnivesRegistry{Repo: "invented-org/private-registry", Path: "repos.local.toml", Ref: "main"},
	file:           "[repos.invented-repo]\nforbidden = [\"Marrowfen\"]\n",
}

// knivesRefusal is err as a refusal of the knives pass, failing the test where it is anything
// else, or where its message holds any term these tests list, in any case.
func knivesRefusal(t *testing.T, err error) *Refusal {
	t.Helper()
	refused := refusal(t, err)
	if refused.By != byKnives {
		t.Fatalf("the push was refused by %q, want the knives pass: %v", refused.By, err)
	}
	for _, term := range []string{knivesTerm, "Bramblecote", "Marrowfen"} {
		if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(term)) {
			t.Fatalf("the refusal holds a term: %v", err)
		}
	}
	return refused
}

// The commit of TestAKnivesTermRefusesAPushToTheEntrysOrigin without the line that holds the term
// passes, after the pass read the registry.
func TestAPushThatAddsNoKnivesTermPasses(t *testing.T) {
	_, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "a line of ordinary prose\n"})
	path, listed, ran := servingKnives(t, theKnivesRow)
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
	if err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin)); err != nil {
		t.Fatalf("a push that adds no term: %v", err)
	}
	if runs := countLines(t, ran); runs != 1 {
		t.Errorf("gh ran %d times, want one read of the one registry", runs)
	}
}

// Where LEGION_KNIVES_REGISTRIES is not set, and where it lists no row, no entry names the push's
// destination: a line that holds a term passes, and gh is never run.
func TestNoKnivesRegistriesIsNoKnivesPass(t *testing.T) {
	_, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "the QUILLWORTHY build\n"})
	path, _, ran := servingKnives(t, theKnivesRow)
	for name, env := range map[string][]string{
		"no registries listed":        {path},
		"an empty list of registries": {path, KnivesRegistriesVar + "=[]"},
	} {
		guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, env...)
		if err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin)); err != nil {
			t.Errorf("a push with %s: %v", name, err)
		}
	}
	if runs := countLines(t, ran); runs != 0 {
		t.Errorf("gh ran %d times, want none", runs)
	}
}

// An overlay's terms are added to the end of its entry's list, so the overlay's one term is the
// entry's third, after the registry's two.
func TestAnOverlaysTermFollowsTheRegistrysInTheEntrysList(t *testing.T) {
	_, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "a MARROWFEN note\n"})
	path, listed, _ := servingKnives(t, theKnivesRow, knivesOverlayRow)
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
	err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin))
	want := []string{"notes.md:1: forbidden term 3"}
	if refused := knivesRefusal(t, err); !slices.Equal(refused.Lines, want) {
		t.Errorf("the report is %q, want %q", refused.Lines, want)
	}
}

// A registry that cannot be read, or that is not what knives writes, refuses the push as a setup
// failure where no list is kept, as none is for any guard here (each has a state home of its own):
// before the pass reads the destination, so even a push no entry would name is refused. The
// failure names the row by its number, and the entry only where the first registry has it, and
// never a row's repository or path, what gh printed, or a term.
func TestAKnivesRegistryThatCannotBeReadRefusesThePush(t *testing.T) {
	const unread = "LEAK CHECK SETUP ERROR: the push to origin: the knives forbidden lists could not be read: "
	const notTOML = " is not TOML of [repos.<name>] tables with origin and release strings and a forbidden list of strings"
	overlay := func(file string) knivesServed {
		row := knivesOverlayRow
		row.file = file
		return row
	}
	registry := func(file string) knivesServed {
		row := theKnivesRow
		row.file = file
		return row
	}
	unserved := knivesOverlayRow
	unserved.unserved = true
	noRef := knivesOverlayRow
	noRef.Ref = ""
	noOwner := knivesOverlayRow
	noOwner.Repo = "private-registry"
	fixture, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "the QUILLWORTHY build\n"})

	for _, tc := range []struct {
		name string
		rows []knivesServed
		// listed, where it is not empty, is the LEGION_KNIVES_REGISTRIES entry in place of the
		// rows' own.
		listed string
		want   string
	}{
		{
			name: "gh cannot read the overlay",
			rows: []knivesServed{theKnivesRow, unserved},
			want: unread + "knives registry 2 of LEGION_KNIVES_REGISTRIES: gh exited 1",
		},
		{
			name:   "the variable is one row rather than a list",
			listed: `LEGION_KNIVES_REGISTRIES={"repo":"invented-org/private-registry","path":"repos.local.toml","ref":"main"}`,
			want:   unread + `LEGION_KNIVES_REGISTRIES is not a JSON list of {"repo", "path", "ref"} rows`,
		},
		{
			name:   "a row carries a field no row has",
			listed: `LEGION_KNIVES_REGISTRIES=[{"repo":"invented-org/private-registry","path":"repos.local.toml","ref":"main","sha":"0"}]`,
			want:   unread + `LEGION_KNIVES_REGISTRIES is not a JSON list of {"repo", "path", "ref"} rows`,
		},
		{
			name: "a row names no ref",
			rows: []knivesServed{theKnivesRow, noRef},
			want: unread + "knives registry 2 of LEGION_KNIVES_REGISTRIES names no ref",
		},
		{
			name: "a row's repo has no owner",
			rows: []knivesServed{theKnivesRow, noOwner},
			want: unread + "knives registry 2 of LEGION_KNIVES_REGISTRIES names a repo that is not <owner>/<name>",
		},
		{
			name: "the overlay is not TOML",
			rows: []knivesServed{theKnivesRow, overlay("the Marrowfen terms\n")},
			want: unread + "knives registry 2 of LEGION_KNIVES_REGISTRIES" + notTOML + " (line 1)",
		},
		{
			name: "the registry's forbidden list is one string",
			rows: []knivesServed{registry("[repos.invented-repo]\norigin = \"" + knivesOrigin + "\"\nforbidden = \"Quillwort\"\n")},
			want: unread + "knives registry 1 of LEGION_KNIVES_REGISTRIES" + notTOML + " (line 3)",
		},
		{
			name: "an entry of the registry names no origin",
			rows: []knivesServed{registry("[repos.invented-repo]\nforbidden = [\"Quillwort\"]\n")},
			want: unread + "knives registry 1 of LEGION_KNIVES_REGISTRIES: [repos.invented-repo] names no origin",
		},
		{
			name: "an entry of the registry names an upstream that is not a string",
			rows: []knivesServed{registry("[repos.invented-repo]\nupstream = [\"https://github.com/upstream-org/invented-repo\"]\norigin = \"" + knivesOrigin + "\"\nforbidden = [\"Quillwort\"]\n")},
			want: unread + "knives registry 1 of LEGION_KNIVES_REGISTRIES: [repos.invented-repo] upstream is not a string",
		},
		{
			name: "the overlay adds to an entry the registry does not have",
			rows: []knivesServed{theKnivesRow, overlay("[repos.private-entry]\nforbidden = [\"Marrowfen\"]\n")},
			want: unread + "knives registry 2 of LEGION_KNIVES_REGISTRIES adds to an entry no earlier registry has; an overlay only adds to an entry the registry already has",
		},
		{
			name: "a term of the overlay is blank",
			rows: []knivesServed{theKnivesRow, overlay("[repos.invented-repo]\nforbidden = [\"Marrowfen\", \" \"]\n")},
			want: unread + "knives registry 2 of LEGION_KNIVES_REGISTRIES: the forbidden list of [repos.invented-repo] holds an empty term",
		},
		{
			name: "the overlay lists a term of the registry's again, in another case",
			rows: []knivesServed{theKnivesRow, overlay("[repos.invented-repo]\nforbidden = [\"QUILLWORT\"]\n")},
			want: unread + "knives registry 2 of LEGION_KNIVES_REGISTRIES: the forbidden list of [repos.invented-repo] lists a term twice, case aside",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, listed, _ := servingKnives(t, tc.rows...)
			if tc.listed != "" {
				listed = tc.listed
			}
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
			// The URL git hands the hook is a local path, an empty bare repository, and the
			// repository has no remote named origin, so nothing names the destination: the
			// registries are read all the same.
			bare := filepath.Join(t.TempDir(), "remote.git")
			fixture.Run(repo, fixturegit.InitBare(bare, "main")...)
			err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", bare))
			setupFailure(t, err)
			if err.Error() != tc.want {
				t.Errorf("the failure is\n  %s\nwant\n  %s", err, tc.want)
			}
			for _, private := range []string{"private-registry", "repos.local.toml", "private-entry", "quillwort", "marrowfen", "bramblecote"} {
				if strings.Contains(strings.ToLower(err.Error()), private) {
					t.Errorf("the failure holds %q: %v", private, err)
				}
			}
		})
	}
}

// The destination is named by what git hands the hook and by the remote's own configuration, so
// an entry applies to a push whose URL git rewrote to a local bare path (pushInsteadOf) where the
// remote's URL names the entry's origin, to a push to a URL typed as the remote, and to a push
// whose push URL is a local bare path where the remote's URL names the entry's origin. The URL
// the remote pushes to is its push URL where that names a GitHub repository, and a push whose
// remote and URLs name no entry's origin or release passes, although a line holds the term.
func TestTheKnivesPassNamesTheDestinationByTheRemoteAsWell(t *testing.T) {
	another := "https://github.com/invented-org/another-repo.git"
	for _, tc := range []struct {
		name string
		// config is the repository's configuration, a key and a value each.
		config [][2]string
		// remote and url are what git hands the hook. Where the remote is origin, git's own
		// `remote get-url --push` is checked to answer url.
		remote, url string
		refused     bool
	}{
		{
			name:    "git hands a local bare path, and the remote's URL names the entry's origin",
			config:  [][2]string{{"remote.origin.url", knivesOrigin}, {"url.<bare>.pushInsteadOf", knivesOrigin}},
			remote:  "origin",
			url:     "<bare>",
			refused: true,
		},
		{
			name:    "the remote is the entry's origin as typed, and git hands a local bare path",
			config:  [][2]string{{"url.<bare>.pushInsteadOf", knivesOrigin}},
			remote:  knivesOrigin,
			url:     "<bare>",
			refused: true,
		},
		{
			name:   "the remote's URL names the entry's origin, and its push URL another repository",
			config: [][2]string{{"remote.origin.url", knivesOrigin}, {"remote.origin.pushurl", another}},
			remote: "origin",
			url:    another,
		},
		{
			name:    "the remote's URL names the entry's origin, and its push URL is a local bare path",
			config:  [][2]string{{"remote.origin.url", knivesOrigin}, {"remote.origin.pushurl", "<bare>"}},
			remote:  "origin",
			url:     "<bare>",
			refused: true,
		},
		{
			name:   "git hands a local bare path, and the remote's URL names another repository",
			config: [][2]string{{"remote.origin.url", another}, {"url.<bare>.pushInsteadOf", another}},
			remote: "origin",
			url:    "<bare>",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "the QUILLWORTHY build\n"})
			bare := filepath.Join(t.TempDir(), "remote.git")
			fixture.Run(repo, fixturegit.InitBare(bare, "main")...)
			for _, setting := range tc.config {
				fixture.Run(repo, "git", "config", strings.ReplaceAll(setting[0], "<bare>", bare), strings.ReplaceAll(setting[1], "<bare>", bare))
			}
			url := strings.ReplaceAll(tc.url, "<bare>", bare)
			if tc.remote == "origin" {
				if hands := fixture.Run(repo, "git", "remote", "get-url", "--push", "origin"); hands != url {
					t.Fatalf("git pushes origin to %q, want %q", hands, url)
				}
			}
			// The bare stands in for a URL on github.com that git hands the hook, once git's own
			// answer is checked.
			if url != bare {
				reach(t, fixture, repo, url, bare)
			}
			path, listed, _ := servingKnives(t, theKnivesRow)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
			err := guard.Push(bound(t), knivesPush(repo, base, head, tc.remote, url))
			if !tc.refused {
				if err != nil {
					t.Fatalf("a push no entry names: %v", err)
				}
				return
			}
			want := []string{"notes.md:1: forbidden term 2"}
			if refused := knivesRefusal(t, err); !slices.Equal(refused.Lines, want) {
				t.Errorf("the report is %q, want %q", refused.Lines, want)
			}
		})
	}
}

// An entry applies by its release as by its origin, each named case aside, and where two entries
// apply each line of the report names its entry, in the order of the entries' names. An entry
// whose remotes name another repository does not apply, and its terms are not read.
func TestWhereTwoKnivesEntriesApplyTheReportNamesEach(t *testing.T) {
	registry := theKnivesRow
	registry.file = `[repos.beta]
origin = "https://github.com/invented-org/another-repo"
release = "git@github.com:Invented-Org/Invented-Repo.git"
forbidden = ["Bramblecote", "Quillwort"]

[repos.alpha]
origin = "https://github.com/invented-org/invented-repo"
forbidden = ["Quillwort"]

[repos.gamma]
origin = "https://github.com/invented-org/third-repo"
forbidden = ["Marrowfen"]
`
	_, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "quillwort beside bramblecote and marrowfen\n"})
	path, listed, _ := servingKnives(t, registry)
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
	err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin))
	want := []string{
		"notes.md:1: forbidden term 1 of [repos.alpha]",
		"notes.md:1: forbidden term 1 of [repos.beta]",
		"notes.md:1: forbidden term 2 of [repos.beta]",
	}
	if refused := knivesRefusal(t, err); !slices.Equal(refused.Lines, want) {
		t.Errorf("the report is %q, want %q", refused.Lines, want)
	}
}

// A path the commit adds that holds a term is refused as a path, and the report writes each
// component of it that holds a term as <forbidden>, case aside.
func TestAnAddedPathThatHoldsAKnivesTermIsRefusedMasked(t *testing.T) {
	_, repo, base, head := knivesCommit(t, map[string]string{"notes/Quillwort-Plans/today.md": "a line of ordinary prose\n"})
	path, listed, _ := servingKnives(t, theKnivesRow)
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
	err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin))
	want := []string{"notes/<forbidden>/today.md: the path: forbidden term 2"}
	if refused := knivesRefusal(t, err); !slices.Equal(refused.Lines, want) {
		t.Errorf("the report is %q, want %q", refused.Lines, want)
	}
}

// A file .gitattributes marks -diff, the pushed commit's own .gitattributes saying so, is read
// for knives terms as every file is (TestAFileIsBinaryByItsOwnContentAlone): git's own diff would
// write "Binary files ... differ" for it, and no line of it would reach the pass.
func TestAFileMarkedDashDiffIsReadForKnivesTerms(t *testing.T) {
	_, repo, base, head := knivesCommit(t, map[string]string{".gitattributes": "notes.lock -diff\n", "notes.lock": "the QUILLWORTHY build\n"})
	path, listed, _ := servingKnives(t, theKnivesRow)
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
	err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin))
	want := []string{"notes.lock:1: forbidden term 2"}
	if refused := knivesRefusal(t, err); !slices.Equal(refused.Lines, want) {
		t.Errorf("the report is %q, want %q", refused.Lines, want)
	}
}

// A merge of 52 parents, as cybermonklabs/oh-my-pi's release merges are, is read from its files
// for the knives pass as for the rules (TestAnOctopusMergeIsReadFromItsFiles, whose fixture,
// octopusRepo, this is): a line of the merge's own resolution that holds a term is refused at its
// number in the merge's file, and a line that holds one in a parent's file is text the
// destination holds already, since it holds every parent. The entry's upstream holds nothing.
func TestAnOctopusMergesOwnLinesAreReadForKnivesTerms(t *testing.T) {
	repo, base, every, octopus := octopusRepo(t, "the build of parent 17 is QUILLWORTHY")
	destinationAt(t, fixturegit.Isolated(t, t.TempDir()), repo, knivesUpstream)

	for _, tc := range []struct {
		name string
		head string
		// want is the report, and nil where the push passes.
		want []string
	}{
		{
			name: "the resolution adds a line that holds a term",
			head: octopus(every+"the resolution names quillwort\n", ""),
			// The shared file's first line, each parent's, and then the resolution's.
			want: []string{"shared.md:" + strconv.Itoa(1+octopusParents+1) + ": forbidden term 2"},
		},
		{
			name: "a parent's line holds the term and the merge adds none",
			head: octopus(every, ""),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, listed, _ := servingKnives(t, theKnivesRow)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
			err := guard.Push(bound(t), knivesPush(repo, base, tc.head, "origin", knivesOrigin))
			if tc.want == nil {
				if err != nil {
					t.Fatalf("a merge that adds nothing of its own: %v", err)
				}
				return
			}
			if refused := knivesRefusal(t, err); !slices.Equal(refused.Lines, tc.want) {
				t.Errorf("the report is %q, want %q", refused.Lines, tc.want)
			}
		})
	}
}

// The knives pass reads a commit's added lines and added paths alone, as knives does, and never a
// ref name, an identity or a message. Every branch a Legion pod pushes to a fork is named
// legion/<KEY>, and every commit a pod makes carries the Legion-Role and Omp-Session trailers,
// so a pass over either would refuse every push a tree makes to a fork whose entry lists the
// word, as the oh-my-pi fork's entry does. The same word on an added line is refused, so the
// entry's term is in force in both halves.
func TestAKnivesTermInARefNameOrAMessageIsNotRead(t *testing.T) {
	const registry = "[repos.invented-repo]\norigin = \"https://github.com/invented-org/invented-repo\"\nforbidden = [\"Legion\"]\n"
	row := knivesServed{KnivesRegistry: KnivesRegistry{Repo: "invented-org/knives-registry", Path: "repos.toml", Ref: "main"}, file: registry}
	for _, tc := range []struct {
		name, line string
		want       []string
	}{
		{name: "a clean line passes", line: "a line of ordinary prose\n"},
		{name: "the term on a line is refused", line: "a line about the legion build\n", want: []string{"notes.md:1: forbidden term 1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, repo, _, _ := knivesCommit(t, map[string]string{"notes.md": tc.line})
			fixture.Run(repo, "git", "commit", "--amend", "--quiet", "-m",
				"docs: a note\n\nLegion-Role: implementer\nOmp-Session: 01a1d194-28c4-75ed-8af2-76ddf59101e7")
			head := fixture.Run(repo, "git", "rev-parse", "HEAD")
			path, listed, _ := servingKnives(t, row)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
			ref := "refs/heads/legion/INVENT-1"
			err := guard.Push(bound(t), PushRequest{
				Dir:     repo,
				Remote:  "origin",
				URL:     knivesOrigin,
				Updates: []Update{{LocalRef: ref, LocalSHA: head, RemoteRef: ref, RemoteSHA: noSHA}},
			})
			if tc.want == nil {
				if err != nil {
					t.Fatalf("a push of legion/INVENT-1 whose message carries the trailers: %v", err)
				}
				return
			}
			if refused := knivesRefusal(t, err); !slices.Equal(refused.Lines, tc.want) {
				t.Errorf("the report is %q, want %q", refused.Lines, tc.want)
			}
		})
	}
}

// A member's commit on top of a commit the upstream's main holds is read for its own lines alone:
// the upstream commit adds a line holding a term as well, and the report names only the member's.
// The refusal says, after its report, that a commit the destination's or the upstream's refs reach
// is not read, and it names no term. It says to fetch nothing: a commit an upstream's ref reaches
// is held whether or not the clone fetched that ref, wherever the clone has the commit it is at.
func TestAMemberCommitOnAnUpstreamCommitIsRefusedForItsOwnLinesAlone(t *testing.T) {
	fixture, repo, base, upstream := knivesCommit(t, map[string]string{"upstream.md": "the QUILLWORTHY build\n"})
	fixture.Run(repo, "git", "push", "--quiet", knivesUpstream, upstream+":refs/heads/main")
	if err := os.WriteFile(filepath.Join(repo, "member.md"), []byte("a line of ordinary prose\nthe member's bramblecote note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.Run(repo, "git", "add", "-A")
	fixture.Run(repo, "git", "commit", "--quiet", "-m", "the member's commit")
	head := fixture.Run(repo, "git", "rev-parse", "HEAD")
	path, listed, _ := servingKnives(t, theKnivesRow)
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
	err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin))
	want := []string{"member.md:2: forbidden term 1"}
	refused := knivesRefusal(t, err)
	if !slices.Equal(refused.Lines, want) {
		t.Errorf("the report is %q, want %q", refused.Lines, want)
	}
	if !strings.HasSuffix(err.Error(), "\n  "+want[0]+"\n"+knivesAdvice) || strings.Contains(knivesAdvice, "fetch") {
		t.Errorf("the refusal does not end with its report and then the advice, which names no fetch:\n%v", err)
	}
}

// knivesReadAt is when the first push of each kept-list test below reads the registries: an
// invented moment, in UTC, which a notice names.
var knivesReadAt = time.Date(2026, 11, 17, 9, 52, 0, 0, time.UTC)

// knivesGuardAt is a guard whose knives pass reads the registries listed with the gh on path,
// keeps its list under the state home state, tells the time by clock, and adds each notice it
// gives to notices. Guards of one state and one listed share a kept list, as the pushes of a pod
// do, each of which builds a guard of its own.
func knivesGuardAt(t *testing.T, state string, clock Clock, notices *[]string, path, listed string) *Guard {
	t.Helper()
	return New(Options{
		Check:      leakguardtest.Write(t, t.TempDir(), passes),
		Visibility: answers(Public, nil),
		Clock:      clock,
		Notice:     func(line string) { *notices = append(*notices, line) },
		Env:        append(fixtureenv.Isolated(t.TempDir()), path, listed, "XDG_STATE_HOME="+state),
	})
}

// knivesFailing is the gh of servingKnives where the overlay can no longer be read, with the
// LEGION_KNIVES_REGISTRIES entry of the registry and the overlay, whose value a kept list is found
// by: the one a guard that read both keeps its list under.
func knivesFailing(t *testing.T) (path, listed, ran string) {
	t.Helper()
	unserved := knivesOverlayRow
	unserved.unserved = true
	return servingKnives(t, theKnivesRow, unserved)
}

// knivesKeptFile is guard's kept list's file for listed, a LEGION_KNIVES_REGISTRIES entry of
// servingKnives, whose value names the file.
func knivesKeptFile(guard *Guard, listed string) string {
	return guard.knivesKeptPath(strings.TrimPrefix(listed, KnivesRegistriesVar+"="))
}

// A push whose read passed keeps the merged list, in a directory and a file only this user can
// read, and a push up to ten minutes after it matches against that list and reads no registry:
// gh runs once for the two pushes, and the second is refused for the term all the same.
func TestAFreshKeptKnivesListIsUsedWithNoRead(t *testing.T) {
	_, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "the QUILLWORTHY build\n"})
	path, listed, ran := servingKnives(t, theKnivesRow)
	clock := &visibilityTestClock{now: knivesReadAt}
	state := t.TempDir()
	var notices []string
	want := []string{"notes.md:1: forbidden term 2"}
	for _, at := range []time.Duration{0, knivesKeptFor} {
		clock.now = knivesReadAt.Add(at)
		err := knivesGuardAt(t, state, clock, &notices, path, listed).Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin))
		if refused := knivesRefusal(t, err); !slices.Equal(refused.Lines, want) {
			t.Errorf("%s after the read, the report is %q, want %q", at, refused.Lines, want)
		}
		if runs := countLines(t, ran); runs != 1 {
			t.Fatalf("%s after the read, gh ran %d times, want the first push's one read alone", at, runs)
		}
	}
	kept := knivesKeptFile(knivesGuardAt(t, state, clock, &notices, path, listed), listed)
	for file, mode := range map[string]os.FileMode{filepath.Dir(kept): 0o700, kept: 0o600} {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s is %o, want %o", file, info.Mode().Perm(), mode)
		}
	}
	if len(notices) != 0 {
		t.Errorf("the guard gave the notices %q, want none", notices)
	}
}

// The kept list's file is the JSON of the read's time and the merged entries, each entry's name,
// origin, release, upstream and forbidden list in that order and under those names, and a
// newline: the bytes the guard wrote while the file had an entry type of its own, so a list a
// role kept before a release is the one the next release reads, and a guard of either release
// reads a file the other kept. The file reads back as the entries the read merged.
func TestTheKeptKnivesListsFileIsTheMergedEntriesAsJSON(t *testing.T) {
	_, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "a line of ordinary prose\n"})
	path, listed, _ := servingKnives(t, theKnivesRow, knivesOverlayRow)
	var notices []string
	guard := knivesGuardAt(t, t.TempDir(), &visibilityTestClock{now: knivesReadAt}, &notices, path, listed)
	if err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin)); err != nil {
		t.Fatalf("a push that adds no term, its registries read in full: %v", err)
	}
	kept, err := os.ReadFile(knivesKeptFile(guard, listed))
	if err != nil {
		t.Fatalf("the read that passed kept no list: %v", err)
	}
	const want = `{"read_at":"2026-11-17T09:52:00Z","entries":[{"name":"invented-repo",` +
		`"origin":"https://github.com/invented-org/invented-repo","release":"",` +
		`"upstream":"https://github.com/upstream-org/invented-repo","forbidden":["Bramblecote","Quillwort","Marrowfen"]}]}` + "\n"
	if string(kept) != want {
		t.Errorf("the kept list's file is\n  %s\nwant\n  %s", kept, want)
	}
	read, ok := guard.keptKnives(strings.TrimPrefix(listed, KnivesRegistriesVar+"="))
	merged := []knivesEntry{{Name: "invented-repo", Origin: "https://github.com/invented-org/invented-repo", Upstream: knivesUpstream,
		Forbidden: []string{"Bramblecote", knivesTerm, "Marrowfen"}}}
	if !ok || !read.ReadAt.Equal(knivesReadAt) || !reflect.DeepEqual(read.Entries, merged) {
		t.Errorf("the kept list reads back as %+v (kept: %t), want %+v read at %s", read, ok, merged, knivesReadAt)
	}
}

// A kept list read more than ten minutes before a push, or after it, which only a clock set back
// can show, is not used: the push reads every registry again.
func TestAStaleKeptKnivesListIsReadAgain(t *testing.T) {
	for _, tc := range []struct {
		name string
		at   time.Duration
	}{
		{name: "ten minutes and one second after the read", at: knivesKeptFor + time.Second},
		{name: "one second before the read", at: -time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "the QUILLWORTHY build\n"})
			path, listed, ran := servingKnives(t, theKnivesRow)
			clock := &visibilityTestClock{now: knivesReadAt}
			state := t.TempDir()
			var notices []string
			for i, at := range []time.Duration{0, tc.at} {
				clock.now = knivesReadAt.Add(at)
				knivesRefusal(t, knivesGuardAt(t, state, clock, &notices, path, listed).Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin)))
				if runs := countLines(t, ran); runs != i+1 {
					t.Errorf("after push %d, gh ran %d times, want %d", i+1, runs, i+1)
				}
			}
		})
	}
}

// A read that fails, where a list of any age is kept, refuses a push whose destination an entry
// of the kept list names, as the failed read refuses every push where none is kept: the failure
// names the row that could not be read. The commit adds no term, so it is the read that refuses
// it. The kept list stays the one the read that passed made.
func TestAFailedKnivesReadRefusesAPushTheKeptListNames(t *testing.T) {
	_, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "a line of ordinary prose\n"})
	path, listed, _ := servingKnives(t, theKnivesRow, knivesOverlayRow)
	failing, failingListed, _ := knivesFailing(t)
	if failingListed != listed {
		t.Fatalf("the failing gh's rows are %s, want the kept list's %s", failingListed, listed)
	}
	clock := &visibilityTestClock{now: knivesReadAt}
	state := t.TempDir()
	var notices []string
	push := knivesPush(repo, base, head, "origin", knivesOrigin)
	if err := knivesGuardAt(t, state, clock, &notices, path, listed).Push(bound(t), push); err != nil {
		t.Fatalf("a push that adds no term, its registries read in full: %v", err)
	}
	guard := knivesGuardAt(t, state, clock, &notices, failing, listed)
	kept := knivesKeptFile(guard, listed)
	before, err := os.ReadFile(kept)
	if err != nil {
		t.Fatalf("the read that passed kept no list: %v", err)
	}
	clock.now = knivesReadAt.Add(time.Hour)
	err = guard.Push(bound(t), push)
	setupFailure(t, err)
	const want = "LEAK CHECK SETUP ERROR: the push to origin: the knives forbidden lists could not be read: knives registry 2 of LEGION_KNIVES_REGISTRIES: gh exited 1"
	if err.Error() != want {
		t.Errorf("the failure is\n  %s\nwant\n  %s", err, want)
	}
	if after, err := os.ReadFile(kept); err != nil || string(after) != string(before) {
		t.Errorf("after the failed read the kept list is %q (%v), want the one the read that passed made, %q", after, err, before)
	}
	if len(notices) != 0 {
		t.Errorf("the guard gave the notices %q, want none", notices)
	}
}

// A read that fails, where a list is kept, lets a push go on whose destination no entry of the
// kept list names, however old the list, and the guard says so in one notice. The notice
// names the row by its number and the kept list by its time, and no repository, path, ref or
// term. The commit holds a term of the entry the kept list has for another repository.
func TestAFailedKnivesReadLetsAPushTheKeptListDoesNotNameGoOn(t *testing.T) {
	fixture, repo, base, head := knivesCommit(t, map[string]string{"notes/plans.md": "the QUILLWORTHY build\n"})
	destinationAt(t, fixture, repo, "https://github.com/invented-org/another-repo.git", base+":refs/heads/main")
	path, listed, _ := servingKnives(t, theKnivesRow, knivesOverlayRow)
	failing, _, failed := knivesFailing(t)
	clock := &visibilityTestClock{now: knivesReadAt}
	state := t.TempDir()
	var notices []string
	push := knivesPush(repo, base, head, "origin", "https://github.com/invented-org/another-repo.git")
	if err := knivesGuardAt(t, state, clock, &notices, path, listed).Push(bound(t), push); err != nil {
		t.Fatalf("a push no entry names, its registries read in full: %v", err)
	}
	clock.now = knivesReadAt.Add(24 * time.Hour)
	if err := knivesGuardAt(t, state, clock, &notices, failing, listed).Push(bound(t), push); err != nil {
		t.Fatalf("a push the kept list names no entry for, after a read that failed: %v", err)
	}
	if runs := countLines(t, failed); runs != 2 {
		t.Errorf("the failing gh ran %d times, want one read of each of the two registries", runs)
	}
	want := []string{"LEAK CHECK: the knives forbidden lists could not be read (knives registry 2 of LEGION_KNIVES_REGISTRIES: gh exited 1); " +
		"the list read at 2026-11-17T09:52:00Z names no entry for this push's destination, so the push goes on"}
	if !slices.Equal(notices, want) {
		t.Errorf("the notices are %q, want %q", notices, want)
	}
	said := strings.ToLower(strings.Join(notices, "\n"))
	for _, private := range []string{"invented-org", "another-repo", "knives-registry", "private-registry", "repos.toml", "repos.local.toml",
		"refs/heads", "notes/", "plans.md", "quillwort", "marrowfen", "bramblecote"} {
		if strings.Contains(said, private) {
			t.Errorf("the notice holds %q: %q", private, notices)
		}
	}
}

// A kept list in a directory others can read is not this user's alone, so the pass does not use
// it, neither in place of a read nor after one that fails: a push a minute after the read reads
// every registry again, and where that read fails the push is refused, although no entry of the
// kept list names its destination.
func TestAKeptKnivesListInADirectoryOthersCanReadIsNotUsed(t *testing.T) {
	fixture, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "a line of ordinary prose\n"})
	destinationAt(t, fixture, repo, "https://github.com/invented-org/another-repo.git", base+":refs/heads/main")
	path, listed, _ := servingKnives(t, theKnivesRow, knivesOverlayRow)
	failing, _, failed := knivesFailing(t)
	clock := &visibilityTestClock{now: knivesReadAt}
	state := t.TempDir()
	var notices []string
	push := knivesPush(repo, base, head, "origin", "https://github.com/invented-org/another-repo.git")
	guard := knivesGuardAt(t, state, clock, &notices, path, listed)
	if err := guard.Push(bound(t), push); err != nil {
		t.Fatalf("a push no entry names, its registries read in full: %v", err)
	}
	if err := os.Chmod(filepath.Dir(knivesKeptFile(guard, listed)), 0o755); err != nil {
		t.Fatal(err)
	}
	clock.now = knivesReadAt.Add(time.Minute)
	err := knivesGuardAt(t, state, clock, &notices, failing, listed).Push(bound(t), push)
	if runs := countLines(t, failed); runs != 2 {
		t.Errorf("the failing gh ran %d times, want one read of each of the two registries", runs)
	}
	setupFailure(t, err)
	if len(notices) != 0 {
		t.Errorf("the guard gave the notices %q, want none", notices)
	}
}
