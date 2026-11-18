package leakguard

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// destinationAt is a bare repository of the test's own holding each of refspecs, which a push
// from repo by the bare's path writes there as `git push` reads one ("<commit>:refs/heads/main"):
// a push by path writes no remote-tracking ref of repo's. It answers the bare's path, the URL a
// push to it is made to. The guard reads what a push's destination holds with git ls-remote of
// that URL, run outside the repository and with no git configuration but its own command line's
// (Guard.advertised), so the URL reaches the bare by itself; it names no repository on
// github.com, so the guard scans the push without reading a visibility (Guard.scanThrough).
func destinationAt(t *testing.T, fixture fixturegit.Runner, repo string, refspecs ...string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "destination.git")
	fixture.Run(repo, fixturegit.InitBare(bare, "main")...)
	if len(refspecs) > 0 {
		fixture.Run(repo, append([]string{"git", "push", "--quiet", bare}, refspecs...)...)
	}
	return bare
}

// commitFiles writes files into repo's working tree, each path to its content, a directory a path
// names made where it is not there, commits the working tree with message, and answers the commit.
// It is the commit of every fixture of these tests that commits what it wrote on top of HEAD, a
// merge git holds unfinished included; a fixture whose commit has parents of its own choosing
// writes its tree and runs commit-tree itself.
func commitFiles(t *testing.T, fixture fixturegit.Runner, repo string, files map[string]string, message string) string {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fixture.Run(repo, "git", "add", "-A")
	fixture.Run(repo, "git", "commit", "--quiet", "-m", message)
	return fixture.Run(repo, "git", "rev-parse", "HEAD")
}

// A push's records are read from the commits it would add, out of a real repository: the
// message, the author identity, each added line of each file, and the ref name are each read,
// and a commit the destination already holds is not read at all. Every case here pushes one commit
// that names something private in one of those places, and the last one names nothing anywhere
// and is passed.
func TestAPushsRecordsAreReadFromItsCommits(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	remote, repo := filepath.Join(root, "remote.git"), filepath.Join(root, "repo")
	// The fixture's own git runs under an environment that reaches no repository, remote,
	// credential or configuration but the test's (docs/testing.md).
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitBare(remote, "main")...)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	base := commitFiles(t, fixture, repo, map[string]string{"README.md": "one\n"}, "the commit the destination holds")
	// The push's destination holds the base commit on its main, which it advertises: that is what
	// makes it a commit no push adds. The push is made to it by its path.
	fixture.Run(repo, "git", "remote", "add", "origin", remote)
	fixture.Run(repo, "git", "push", "--quiet", "origin", "main")
	branch := func(t *testing.T, name string) {
		t.Helper()
		fixture.Run(repo, "git", "checkout", "--quiet", "-B", name, base)
	}

	for _, tc := range []struct {
		name string
		// build makes the case's commit on a branch of its own and answers its sha.
		build func(t *testing.T) string
		// ref is the ref the destination would hold, "refs/heads/main" where the case leaves it
		// empty.
		ref string
		// newRef is whether the destination holds that ref already: a new one has no sha of its
		// own to read the push's commits from, so they are read from the destination's other refs.
		newRef bool
		// want is the report, as a function of the commit's sha, and nil where the push passes.
		want func(head string) []string
	}{
		{
			name: "the message names a host",
			build: func(t *testing.T) string {
				branch(t, "message")
				return commitFiles(t, fixture, repo, map[string]string{"README.md": "one\ntwo\n"}, "moved the run to "+theHost+" this morning")
			},
			want: func(head string) []string {
				return []string{"commit " + short(head) + " message: " + theClass}
			},
		},
		{
			name: "the author identity names a host",
			build: func(t *testing.T) string {
				branch(t, "author")
				author := fixture.With("GIT_AUTHOR_EMAIL=builder@" + theHost)
				return commitFiles(t, author, repo, map[string]string{"README.md": "one\nthree\n"}, "a message of no interest")
			},
			want: func(head string) []string {
				return []string{"commit " + short(head) + " author: " + theClass}
			},
		},
		{
			name: "an added line of a tracked file names a host",
			build: func(t *testing.T) string {
				branch(t, "line")
				return commitFiles(t, fixture, repo, map[string]string{"README.md": "one\nthe runner answers at " + theHost + "\n"}, "a message of no interest")
			},
			want: func(string) []string { return []string{"README.md: " + theClass} },
		},
		{
			name: "the ref name names a host",
			build: func(t *testing.T) string {
				branch(t, "ref-name")
				return commitFiles(t, fixture, repo, map[string]string{"README.md": "one\nfour\n"}, "a message of no interest")
			},
			ref:    "refs/heads/legion/" + theHost,
			newRef: true,
			want:   func(string) []string { return []string{"the ref name: " + theClass} },
		},
		{
			name: "a commit that names nothing",
			build: func(t *testing.T) string {
				branch(t, "clean")
				return commitFiles(t, fixture, repo, map[string]string{"docs/notes.md": "a line of ordinary prose\n"}, "a message of no interest")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := tc.build(t)
			ref, remoteSHA := tc.ref, base
			if ref == "" {
				ref = "refs/heads/main"
			}
			if tc.newRef {
				remoteSHA = noSHA
			}
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			err := guard.Push(bound(t), PushRequest{
				Dir:    repo,
				Remote: "origin",
				URL:    remote,
				Updates: []Update{{
					LocalRef:  ref,
					LocalSHA:  head,
					RemoteRef: ref,
					RemoteSHA: remoteSHA,
				}},
			})
			var want []string
			if tc.want != nil {
				want = tc.want(head)
			}
			wantRefused(t, err, want)
		})
	}
}

// An annotated tag publishes its name, its tagger and its message with it, so a push of one reads
// each, as records labelled as a commit's identity and message are, and reads the tag a tag is of
// in turn, whose name no ref of the push need name.
// Every tag here is of the commit the destination holds on its main, so the push adds no commit,
// and each is pushed to refs/tags/pushed, a ref the destination does not hold, which names nothing.
// A tag the destination already holds, at a ref of its own, is not read again: its message names
// a host here, and a push of it to another ref passes.
func TestAnAnnotatedTagsTaggerAndMessageAreRead(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	repo := filepath.Join(root, "repo")
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	base := commitFiles(t, fixture, repo, map[string]string{"README.md": "one\n"}, "the commit the destination holds")
	// tag writes the annotated tag name of target with message, made by tagger's identity, and
	// answers the tag object.
	tag := func(t *testing.T, tagger fixturegit.Runner, name, target, message string) string {
		t.Helper()
		tagger.Run(repo, "git", "-c", "advice.nestedTag=false", "tag", "--annotate", "--message", message, name, target)
		return tagger.Run(repo, "git", "rev-parse", "refs/tags/"+name)
	}
	held := tag(t, fixture, "held", base, "moved the run to "+theHost+" this morning")
	destination := destinationAt(t, fixture, repo, base+":refs/heads/main", "refs/tags/held:refs/tags/held")
	for _, tc := range []struct {
		name string
		// build writes the case's tags and answers the one the push is of, and the report, nil
		// where the push passes.
		build func(t *testing.T) (pushed string, want []string)
	}{
		{
			name: "the message names a host",
			build: func(t *testing.T) (string, []string) {
				pushed := tag(t, fixture, "message", base, "moved the run to "+theHost+" this morning")
				return pushed, []string{"tag " + short(pushed) + " message: " + theClass}
			},
		},
		{
			name: "the tagger identity names a host",
			build: func(t *testing.T) (string, []string) {
				pushed := tag(t, fixture.With("GIT_COMMITTER_EMAIL=builder@"+theHost), "tagger", base, "a message of no interest")
				return pushed, []string{"tag " + short(pushed) + " tagger: " + theClass}
			},
		},
		{
			name: "the message of the tag a tag is of names a host",
			build: func(t *testing.T) (string, []string) {
				inner := tag(t, fixture, "inner", base, "moved the run to "+theHost+" this morning")
				return tag(t, fixture, "outer", inner, "a message of no interest"), []string{"tag " + short(inner) + " message: " + theClass}
			},
		},
		{
			name: "the name of the tag a tag is of names a host",
			build: func(t *testing.T) (string, []string) {
				inner := tag(t, fixture, "inner-"+theHost, base, "a message of no interest")
				return tag(t, fixture, "named", inner, "a message of no interest"), []string{"tag " + short(inner) + " name: " + theClass}
			},
		},
		{
			name: "a tag that names nothing",
			build: func(t *testing.T) (string, []string) {
				return tag(t, fixture, "clean", base, "a message of no interest"), nil
			},
		},
		{
			name: "a tag the destination holds",
			build: func(t *testing.T) (string, []string) {
				return held, nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pushed, want := tc.build(t)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			err := guard.Push(bound(t), PushRequest{
				Dir:     repo,
				Remote:  "origin",
				URL:     destination,
				Updates: []Update{{LocalRef: "refs/tags/pushed", LocalSHA: pushed, RemoteRef: "refs/tags/pushed", RemoteSHA: noSHA}},
			})
			wantRefused(t, err, want)
		})
	}
}

// pushOf is a repository whose base the destination holds on its main, the base writing the
// files held, and head, the commit the push adds, on that base, which makes edit's change to the
// working tree, where edit is not nil, and then writes the files added, each path to its content.
// Where side is not nil, head is a merge, in the shape of TestAMergesOwnResolutionIsRead: of
// main, at a commit of its own on the base, and of a branch side, whose commit on the base writes
// the files side names, with the files added written by the merge's resolution. The destination
// holds side's commit too, at a branch of its own, so that the push adds main's commit and the
// merge alone: a file side writes is read as a parent's file of the merge, and never as a file of
// a commit the push adds. It answers too the URL the push is made to, the destination's path
// (destinationAt).
func pushOf(t *testing.T, held, side, added map[string]string, edit func(t *testing.T, repo string)) (repo, base, head, destination string) {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	repo = filepath.Join(root, "repo")
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	files := map[string]string{"README.md": "one\n"}
	maps.Copy(files, held)
	base = commitFiles(t, fixture, repo, files, "the commit the destination holds")
	if side != nil {
		fixture.Run(repo, "git", "checkout", "--quiet", "-b", "side")
		files := map[string]string{"side.md": "a line of the side branch\n"}
		maps.Copy(files, side)
		commitFiles(t, fixture, repo, files, "the side commit")
		fixture.Run(repo, "git", "checkout", "--quiet", "main")
		commitFiles(t, fixture, repo, map[string]string{"main.md": "a line of the main branch\n"}, "the main commit")
		fixture.Run(repo, "git", "merge", "--quiet", "--no-ff", "--no-commit", "side")
	}
	if edit != nil {
		edit(t, repo)
	}
	head = commitFiles(t, fixture, repo, added, "the commit the push adds")
	refspecs := []string{base + ":refs/heads/main"}
	if side != nil {
		refspecs = append(refspecs, "side:refs/heads/side")
	}
	return repo, base, head, destinationAt(t, fixture, repo, refspecs...)
}

// mainPush is the push of head to main, where the destination holds base, to remote at url.
func mainPush(repo, base, head, remote, url string) PushRequest {
	return PushRequest{
		Dir:     repo,
		Remote:  remote,
		URL:     url,
		Updates: []Update{{LocalRef: "refs/heads/main", LocalSHA: head, RemoteRef: "refs/heads/main", RemoteSHA: base}},
	}
}

// What git answers is read from its standard output alone, and a warning git writes to its
// standard error is no part of it. git writes a warning at once and its output in blocks, so a
// warning can land in the middle of a path in the list of a commit's changes, and read together
// with the output, it would cut that path in two: the line the commit adds to the file would be
// read as a line of a file of another name. The stand-in git here writes the warning diff-tree
// writes of a commit with too many files to find its renames among, in the middle of the path of
// the file the commit adds a line to, and runs the real git for everything else.
func TestAWarningGitWritesIsNoPartOfWhatItAnswers(t *testing.T) {
	bin := t.TempDir()
	listed := filepath.Join(t.TempDir(), "listed")
	writeProgram(t, bin, "git", `git=`+shQuote(fixturegit.CompiledGit(t))+` listed=`+shQuote(listed)+`
case " $* " in
*" diff-tree "*)
  "$git" "$@" >"$listed" || exit
  at=$(grep -boa notes "$listed" | head -n 1 | cut -d: -f1)
  head -c "$((at + 5))" "$listed"
  echo 'warning: exhaustive rename detection was skipped due to too many files.' >&2
  tail -c "+$((at + 6))" "$listed"
  exit 0 ;;
esac
exec "$git" "$@"
`)
	repo, base, head, destination := pushOf(t, map[string]string{"notes.md": "one\n"}, nil, map[string]string{"notes.md": "one\nthe runner answers at " + theHost + "\n"}, nil)
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, pathWith(bin))
	err := guard.Push(bound(t), mainPush(repo, base, head, "origin", destination))
	wantRefused(t, err, []string{"notes.md: " + theClass})
}

// octopusParents is how many parents the merges octopusRepo writes have: as many as
// cybermonklabs/oh-my-pi's release merges have, which run from 51 to 53.
const octopusParents = 52

// octopusRepo is the fixture of the octopus merge tests: a repository whose base, which the
// destination holds, has one file, shared.md, and octopusParents parents on that base, each of
// which adds a line of its own to shared.md, parent 17's being line17. The destination, which a
// push is made to by its path, holds every parent at a branch of its own, so a merge of them is
// the whole of what a push of it adds. It answers the repository, the base, every, shared.md as
// it holds every parent's line, which is what each merge's shared file starts from, the
// destination's path, and octopus, which writes a merge of every parent whose shared.md is as
// shared has it, and whose resolution.md is as added has it, where added is not empty: a file no
// parent has. A merge's commit is written with commit-tree over a tree of the fixture's own, so no
// octopus strategy resolves 52 heads.
func octopusRepo(t *testing.T, line17 string) (repo, base, every, destination string, octopus func(shared, added string) string) {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	remote := filepath.Join(root, "remote.git")
	repo = filepath.Join(root, "repo")
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitBare(remote, "main")...)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// tree is the tree of the working tree as it stands, which a commit-tree is made over.
	tree := func() string {
		t.Helper()
		fixture.Run(repo, "git", "add", "-A")
		return fixture.Run(repo, "git", "write-tree")
	}
	const first = "the shared file\n"
	base = commitFiles(t, fixture, repo, map[string]string{"shared.md": first}, "the commit the destination holds")
	every = first
	var merged []string
	push := []string{"git", "push", "--quiet", "origin", "main"}
	for i := range octopusParents {
		n := strconv.Itoa(i + 1)
		line := "a line of parent " + n
		if i == 16 {
			line = line17
		}
		write("shared.md", first+line+"\n")
		parent := fixture.Run(repo, "git", "commit-tree", tree(), "-p", base, "-m", "parent "+n)
		every += line + "\n"
		merged = append(merged, "-p", parent)
		push = append(push, parent+":refs/heads/parent-"+n)
	}
	fixture.Run(repo, "git", "remote", "add", "origin", remote)
	fixture.Run(repo, push...)
	octopus = func(shared, added string) string {
		t.Helper()
		write("shared.md", shared)
		if err := os.RemoveAll(filepath.Join(repo, "resolution.md")); err != nil {
			t.Fatal(err)
		}
		if added != "" {
			write("resolution.md", added)
		}
		head := fixture.Run(repo, append([]string{"git", "commit-tree", tree(), "-m", "the merge"}, merged...)...)
		if listed := fixture.Run(repo, "git", "rev-list", "--parents", "-n", "1", head); len(strings.Fields(listed)) != octopusParents+1 {
			t.Fatalf("the fixture's merge is %q, want a commit of %d parents", listed, octopusParents)
		}
		if adds := fixture.Run(repo, "git", "rev-list", head, "--not", "--remotes=origin"); adds != head {
			t.Fatalf("a push of the fixture's merge adds %q, want the merge alone", adds)
		}
		return head
	}
	return repo, base, every, remote, octopus
}

// ParseUpdates reads git's own pre-push lines, which is how a `legion leak-check` standing in for
// a pre-push hook reads what the push would move, and refuses a line of another shape rather than
// guessing at it.
func TestParseUpdates(t *testing.T) {
	lines := "refs/heads/main abc refs/heads/main def\n\nrefs/heads/work 123 refs/heads/work " + noSHA + "\n"
	updates, err := ParseUpdates(strings.NewReader(lines))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []Update{
		{LocalRef: "refs/heads/main", LocalSHA: "abc", RemoteRef: "refs/heads/main", RemoteSHA: "def"},
		{LocalRef: "refs/heads/work", LocalSHA: "123", RemoteRef: "refs/heads/work", RemoteSHA: noSHA},
	}
	if !slices.Equal(updates, want) {
		t.Errorf("read %v, want %v", updates, want)
	}
	if updates[0].Line() != "refs/heads/main abc refs/heads/main def" {
		t.Errorf("the line is %q", updates[0].Line())
	}
	if _, err := ParseUpdates(strings.NewReader("refs/heads/main abc\n")); err == nil {
		t.Error("a line of three fields was read as a pre-push line")
	}
}
