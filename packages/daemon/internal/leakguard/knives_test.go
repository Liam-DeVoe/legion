package leakguard

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// The knives pass of a push (Guard.knives). The fixtures and the case of a line that holds a term
// are in knives_refusal_test.go, which says why they are a file of their own.

// knivesOverlayRow is an overlay from a private repository, which adds one term to the entry of
// theKnivesRegistry: the shape of the second row in production, whose repository, path and terms
// no message may carry.
var knivesOverlayRow = knivesServed{
	Repo: "invented-org/private-registry",
	Path: "repos.local.toml",
	Ref:  "main",
	file: "[repos.invented-repo]\nforbidden = [\"Marrowfen\"]\n",
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

// Where LEGION_KNIVES_REGISTRIES is not set there is no knives pass: a line that holds a term
// passes, and gh is never run.
func TestNoKnivesRegistriesIsNoKnivesPass(t *testing.T) {
	_, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "the QUILLWORTHY build\n"})
	path, _, ran := servingKnives(t, theKnivesRow)
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path)
	if err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin)); err != nil {
		t.Fatalf("a push with no registries listed: %v", err)
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
// failure: before the pass reads the destination, so even a push no entry would name is refused.
// The failure names the row by its number, and the entry only where the first registry has it, and
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
	_, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "the QUILLWORTHY build\n"})

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
			// The URL git hands the hook is a local path and the repository has no remote named
			// origin, so nothing names the destination: the registries are read all the same.
			err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", filepath.Join(t.TempDir(), "remote.git")))
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
// remote's URL names the entry's origin, and to a push to a URL typed as the remote. The URL the
// remote pushes to is its push URL where it has one, and a push whose remote and URLs name no
// entry's origin or release passes, although a line holds the term.
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
			name:   "git hands a local bare path, and the remote's URL names another repository",
			config: [][2]string{{"remote.origin.url", another}, {"url.<bare>.pushInsteadOf", another}},
			remote: "origin",
			url:    "<bare>",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "the QUILLWORTHY build\n"})
			bare := filepath.Join(t.TempDir(), "remote.git")
			for _, setting := range tc.config {
				fixture.Run(repo, "git", "config", strings.ReplaceAll(setting[0], "<bare>", bare), setting[1])
			}
			url := strings.ReplaceAll(tc.url, "<bare>", bare)
			if tc.remote == "origin" {
				if hands := fixture.Run(repo, "git", "remote", "get-url", "--push", "origin"); hands != url {
					t.Fatalf("git pushes origin to %q, want %q", hands, url)
				}
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

// A merge of 52 parents, as cybermonklabs/oh-my-pi's release merges are, is read from its files
// for the knives pass as for the rules (TestAnOctopusMergeIsReadFromItsFiles): a line of the
// merge's own resolution that holds a term is refused at its number in the merge's file, and a
// line that holds one in a parent's file is text the destination holds already, since it holds
// every parent.
func TestAnOctopusMergesOwnLinesAreReadForKnivesTerms(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	remote, repo := filepath.Join(root, "remote.git"), filepath.Join(root, "repo")
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitBare(remote, "main")...)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, "shared.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tree := func() string {
		t.Helper()
		fixture.Run(repo, "git", "add", "-A")
		return fixture.Run(repo, "git", "write-tree")
	}
	const shared, parents = "the shared file\n", 52
	write(shared)
	fixture.Run(repo, "git", "add", "-A")
	fixture.Run(repo, "git", "commit", "--quiet", "-m", "the commit the destination holds")
	base := fixture.Run(repo, "git", "rev-parse", "HEAD")
	every := shared
	var merged []string
	push := []string{"git", "push", "--quiet", "origin", "main"}
	for i := range parents {
		n := strconv.Itoa(i + 1)
		line := "a line of parent " + n
		if i == 16 {
			line = "the build of parent " + n + " is QUILLWORTHY"
		}
		write(shared + line + "\n")
		parent := fixture.Run(repo, "git", "commit-tree", tree(), "-p", base, "-m", "parent "+n)
		every += line + "\n"
		merged = append(merged, "-p", parent)
		push = append(push, parent+":refs/heads/parent-"+n)
	}
	fixture.Run(repo, "git", "remote", "add", "origin", remote)
	fixture.Run(repo, push...)
	octopus := func(sharedFile string) string {
		t.Helper()
		write(sharedFile)
		head := fixture.Run(repo, append([]string{"git", "commit-tree", tree(), "-m", "the merge"}, merged...)...)
		if listed := fixture.Run(repo, "git", "rev-list", "--parents", "-n", "1", head); len(strings.Fields(listed)) != parents+1 {
			t.Fatalf("the fixture's merge is %q, want a commit of %d parents", listed, parents)
		}
		return head
	}

	for _, tc := range []struct {
		name string
		head string
		// want is the report, and nil where the push passes.
		want []string
	}{
		{
			name: "the resolution adds a line that holds a term",
			head: octopus(every + "the resolution names quillwort\n"),
			// The shared file's first line, each parent's, and then the resolution's.
			want: []string{"shared.md:" + strconv.Itoa(1+parents+1) + ": forbidden term 2"},
		},
		{
			name: "a parent's line holds the term and the merge adds none",
			head: octopus(every),
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
	row := knivesServed{Repo: "invented-org/knives-registry", Path: "repos.toml", Ref: "main", file: registry}
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
