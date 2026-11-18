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

// The fixtures of the knives pass's tests (knives_test.go), and the one case of them that names
// nothing this package held before the pass: a push to a fork whose knives registry entry lists a
// term, of a commit that adds a line holding it. It is a file of its own so that it compiles
// against a guard with no such pass as well, where it fails, because that guard passes the push.

// knivesTerm is the term the entry of theKnivesRegistry lists second, so a report names it as
// forbidden term 2. It is an invented word, as are the entry's other terms and the overlays'.
const knivesTerm = "Quillwort"

// knivesOrigin is the fork theKnivesRegistry's entry names as its origin, as the URL git hands a
// pre-push hook for a remote that pushes to it.
const knivesOrigin = "https://github.com/invented-org/invented-repo.git"

// knivesUpstream is the repository theKnivesRegistry's entry names as its upstream, as the
// registry writes it: the URL the pass reads the upstream's refs from (Guard.knives).
const knivesUpstream = "https://github.com/upstream-org/invented-repo"

// theKnivesRegistry is a registry file in the shape knives writes one: one entry, for the fork at
// knivesOrigin, listing two terms and naming its upstream, whose branches and tags hold commits
// the pass does not read (Guard.knives).
const theKnivesRegistry = `[repos.invented-repo]
upstream = "` + knivesUpstream + `"
origin = "https://github.com/invented-org/invented-repo"
forbidden = ["Bramblecote", "` + knivesTerm + `"]
`

// knivesServed is one registry a stand-in gh serves (servingKnives): its row of
// LEGION_KNIVES_REGISTRIES, and the file.
type knivesServed struct {
	KnivesRegistry
	// file is the registry file's content.
	file string
	// unserved is a row the stand-in answers as it answers any read it does not know: exit 1.
	unserved bool
}

// theKnivesRow is the registry most of these tests read: theKnivesRegistry, from the default
// branch of a public repository.
var theKnivesRow = knivesServed{KnivesRegistry: KnivesRegistry{Repo: "invented-org/knives-registry", Path: "repos.toml", Ref: "main"}, file: theKnivesRegistry}

// servingKnives is what a guard's environment needs to read registries: path, a PATH whose first
// gh is a stand-in that answers the guard's read of each registry with its file, and exits 1 for
// any other call, printing the call as gh prints a read it was refused; and listed, the
// LEGION_KNIVES_REGISTRIES that lists the registries in order, as the daemon writes it
// (KnivesRegistriesValue). The stand-in records nothing but that it ran, one line in the file ran
// for each run.
func servingKnives(t *testing.T, registries ...knivesServed) (path, listed, ran string) {
	t.Helper()
	bin, files := t.TempDir(), t.TempDir()
	ran = filepath.Join(files, "ran")
	var script strings.Builder
	script.WriteString("echo ran >>" + shQuote(ran) + "\n")
	script.WriteString(`if [ "$#" = 4 ] && [ "$1" = api ] && [ "$2" = -H ] && [ "$3" = 'Accept: application/vnd.github.raw' ] &&` + "\n")
	script.WriteString(`  [ "${GH_HOST-}" = github.com ] && [ "${NO_COLOR-}" = 1 ] && [ "${CLICOLOR_FORCE-}" = 0 ]; then` + "\n")
	script.WriteString("  case \"$4\" in\n")
	rows := make([]KnivesRegistry, 0, len(registries))
	for i, registry := range registries {
		rows = append(rows, registry.KnivesRegistry)
		if registry.unserved {
			continue
		}
		file := filepath.Join(files, "registry-"+strconv.Itoa(i+1))
		if err := os.WriteFile(file, []byte(registry.file), 0o600); err != nil {
			t.Fatal(err)
		}
		read := "repos/" + registry.Repo + "/contents/" + registry.Path + "?ref=" + registry.Ref
		script.WriteString("  " + shQuote(read) + ") exec cat " + shQuote(file) + " ;;\n")
	}
	script.WriteString("  esac\nfi\n")
	// The read, which names the registry's repository and path, on both of gh's outputs.
	script.WriteString(`echo "{\"message\":\"Not Found\",\"read\":\"$4\"}"` + "\n")
	script.WriteString(`echo "gh: Not Found (HTTP 404): $4" >&2` + "\nexit 1\n")
	writeProgram(t, bin, "gh", script.String())
	return pathWith(bin), "LEGION_KNIVES_REGISTRIES=" + KnivesRegistriesValue(rows), ran
}

// pathFirst is path, a PATH entry such as servingKnives answers, with dir in front of it, so
// that a stand-in of dir's is found before any of path's.
func pathFirst(dir, path string) string {
	return "PATH=" + dir + string(os.PathListSeparator) + strings.TrimPrefix(path, "PATH=")
}

// knivesCommit is a repository of two commits on main: a base the destination holds, and on it a
// commit that writes files, each path to its content. It answers the fixture's runner, the
// repository and the two commits. The fork at knivesOrigin and the upstream at knivesUpstream are
// two bare repositories the repository's configuration reaches (destinationAt): the fork holds
// base on its main, and the upstream holds nothing.
func knivesCommit(t *testing.T, files map[string]string) (fixture fixturegit.Runner, repo, base, head string) {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	repo = filepath.Join(root, "repo")
	fixture = fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	commit := func(message string) string {
		t.Helper()
		fixture.Run(repo, "git", "add", "-A")
		fixture.Run(repo, "git", "commit", "--quiet", "-m", message)
		return fixture.Run(repo, "git", "rev-parse", "HEAD")
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base = commit("the commit the destination holds")
	for name, content := range files {
		path := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	head = commit("a message of no interest")
	destinationAt(t, fixture, repo, knivesOrigin, base+":refs/heads/main")
	destinationAt(t, fixture, repo, knivesUpstream)
	return fixture, repo, base, head
}

// knivesPush is the push of head to main, where the destination holds base, to remote at url.
func knivesPush(repo, base, head, remote, url string) PushRequest {
	return PushRequest{
		Dir:     repo,
		Remote:  remote,
		URL:     url,
		Updates: []Update{{LocalRef: "refs/heads/main", LocalSHA: head, RemoteRef: "refs/heads/main", RemoteSHA: base}},
	}
}

// A push to a fork whose knives registry entry lists a term is refused where the commit adds a
// line that holds it, inside other text and in another case, as `knives audit` matches a term.
// The report names the file, the line and the term's number in the entry's list, and the refusal
// holds the term in no case.
func TestAKnivesTermRefusesAPushToTheEntrysOrigin(t *testing.T) {
	_, repo, base, head := knivesCommit(t, map[string]string{"notes.md": "a line of ordinary prose\nthe QUILLWORTHY build\n"})
	path, listed, _ := servingKnives(t, theKnivesRow)
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
	err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", knivesOrigin))
	want := []string{"notes.md:2: forbidden term 2"}
	if refused := refusal(t, err); !slices.Equal(refused.Lines, want) {
		t.Errorf("the report is %q, want %q", refused.Lines, want)
	}
	if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(knivesTerm)) {
		t.Errorf("the refusal holds the term it refused: %v", err)
	}
}
