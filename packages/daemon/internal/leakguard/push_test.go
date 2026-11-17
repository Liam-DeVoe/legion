package leakguard

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// reach points url at the bare repository bare in repo's configuration, as
// url.<file URL of bare>.insteadOf. The guard reads what a push's destination, or a knives
// entry's upstream, holds with git ls-remote of its URL, run in the repository
// (Guard.advertised), so a test can name a repository on github.com and have the read reach a
// bare of its own, never the network.
func reach(t *testing.T, fixture fixturegit.Runner, repo, url, bare string) {
	t.Helper()
	fixture.Run(repo, "git", "config", "url.file://"+bare+".insteadOf", url)
}

// destinationAt is a bare repository of the test's own that url reaches from repo (reach),
// holding each of refspecs, which a push from repo by the bare's path writes there as `git push`
// reads one ("<commit>:refs/heads/main"): a push by path writes no remote-tracking ref of repo's.
func destinationAt(t *testing.T, fixture fixturegit.Runner, repo, url string, refspecs ...string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "destination.git")
	fixture.Run(repo, fixturegit.InitBare(bare, "main")...)
	if len(refspecs) > 0 {
		fixture.Run(repo, append([]string{"git", "push", "--quiet", bare}, refspecs...)...)
	}
	reach(t, fixture, repo, url, bare)
	return bare
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
	write := func(t *testing.T, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(t *testing.T, message string, env ...string) string {
		t.Helper()
		fixture.Run(repo, "git", "add", "-A")
		fixture.With(env...).Run(repo, "git", "commit", "--quiet", "-m", message)
		return fixture.Run(repo, "git", "rev-parse", "HEAD")
	}
	write(t, "README.md", "one\n")
	base := commit(t, "the commit the destination holds")
	// The push's destination holds the base commit on its main, which it advertises: that is what
	// makes it a commit no push adds. It is named on github.com, and this repository's
	// configuration reaches it at the local bare (reach).
	fixture.Run(repo, "git", "remote", "add", "origin", remote)
	fixture.Run(repo, "git", "push", "--quiet", "origin", "main")
	reach(t, fixture, repo, theURL, remote)
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
				write(t, "README.md", "one\ntwo\n")
				return commit(t, "moved the run to "+theHost+" this morning")
			},
			want: func(head string) []string {
				return []string{"commit " + short(head) + " message: " + theClass}
			},
		},
		{
			name: "the author identity names a host",
			build: func(t *testing.T) string {
				branch(t, "author")
				write(t, "README.md", "one\nthree\n")
				return commit(t, "a message of no interest", "GIT_AUTHOR_EMAIL=builder@"+theHost)
			},
			want: func(head string) []string {
				return []string{"commit " + short(head) + " author: " + theClass}
			},
		},
		{
			name: "an added line of a tracked file names a host",
			build: func(t *testing.T) string {
				branch(t, "line")
				write(t, "README.md", "one\nthe runner answers at "+theHost+"\n")
				return commit(t, "a message of no interest")
			},
			want: func(string) []string { return []string{"README.md: " + theClass} },
		},
		{
			name: "the ref name names a host",
			build: func(t *testing.T) string {
				branch(t, "ref-name")
				write(t, "README.md", "one\nfour\n")
				return commit(t, "a message of no interest")
			},
			ref:    "refs/heads/legion/" + theHost,
			newRef: true,
			want:   func(string) []string { return []string{"the ref name: " + theClass} },
		},
		{
			name: "a commit that names nothing",
			build: func(t *testing.T) string {
				branch(t, "clean")
				if err := os.MkdirAll(filepath.Join(repo, "docs"), 0o700); err != nil {
					t.Fatal(err)
				}
				write(t, "docs/notes.md", "a line of ordinary prose\n")
				return commit(t, "a message of no interest")
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
				URL:    theURL,
				Updates: []Update{{
					LocalRef:  ref,
					LocalSHA:  head,
					RemoteRef: ref,
					RemoteSHA: remoteSHA,
				}},
			})
			if tc.want == nil {
				if err != nil {
					t.Fatalf("a push that names nothing: %v", err)
				}
				return
			}
			if refused := refusal(t, err); !slices.Equal(refused.Lines, tc.want(head)) {
				t.Errorf("the report is %q, want %q", refused.Lines, tc.want(head))
			}
		})
	}
}

// A push the destination already holds everything of adds no commit and so has no record but its
// ref name, which is what a push of an existing commit to a new name is.
func TestAPushThatAddsNoCommitReadsNoCommit(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	repo := filepath.Join(root, "repo")
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("the file names "+theHost+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.Run(repo, "git", "add", "-A")
	fixture.Run(repo, "git", "commit", "--quiet", "-m", "a commit naming "+theHost)
	head := fixture.Run(repo, "git", "rev-parse", "HEAD")
	// The destination holds that commit at that ref already, so the push moves nothing and the
	// commit's own text is none of its business.
	destinationAt(t, fixture, repo, theURL, head+":refs/heads/main")
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	err := guard.Push(bound(t), PushRequest{
		Dir:     repo,
		Remote:  "origin",
		URL:     theURL,
		Updates: []Update{{LocalRef: "refs/heads/main", LocalSHA: head, RemoteRef: "refs/heads/main", RemoteSHA: head}},
	})
	if err != nil {
		t.Fatalf("a push that adds no commit: %v", err)
	}
}

// A root commit's own added lines are read. A root commit has no parent to diff against, so a
// diff-tree asked for one answers nothing at all, and the first push of a repository — the one
// push that is all root commit — would carry every line of every file past the rules. The
// repository here is the whole of what the push publishes, as a `legion workspace-init` clone
// pushed to a new remote is.
func TestARootCommitsAddedLinesAreRead(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	repo := filepath.Join(root, "repo")
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("the runner answers at "+theHost+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.Run(repo, "git", "add", "-A")
	fixture.Run(repo, "git", "commit", "--quiet", "-m", "the first commit")
	head := fixture.Run(repo, "git", "rev-parse", "HEAD")
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	// The destination holds nothing: a ref the push creates, in a repository with no branch and
	// no tag that could say otherwise.
	destinationAt(t, fixture, repo, theURL)
	err := guard.Push(bound(t), PushRequest{
		Dir:     repo,
		Remote:  "origin",
		URL:     theURL,
		Updates: []Update{{LocalRef: "refs/heads/main", LocalSHA: head, RemoteRef: "refs/heads/main", RemoteSHA: noSHA}},
	})
	want := []string{"README.md: " + theClass}
	if refused := refusal(t, err); !slices.Equal(refused.Lines, want) {
		t.Errorf("the report is %q, want %q", refused.Lines, want)
	}
}

// What a merge's own resolution writes is read. A merge's diff-tree against its first parent is
// empty unless the shape is asked for, so a line that stands in neither parent — the text a
// person wrote while resolving, which is text no other commit of the push carries — would reach
// the destination unread. The merge here is clean and its resolution adds a file of its own,
// which is the "evil merge" every rebase-free branch can hold.
func TestAMergesOwnResolutionIsRead(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	repo := filepath.Join(root, "repo")
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(message string) string {
		t.Helper()
		fixture.Run(repo, "git", "add", "-A")
		fixture.Run(repo, "git", "commit", "--quiet", "-m", message)
		return fixture.Run(repo, "git", "rev-parse", "HEAD")
	}
	write("README.md", "one\n")
	base := commit("the commit the destination holds")
	fixture.Run(repo, "git", "checkout", "--quiet", "-b", "side")
	write("side.md", "a line of the side branch\n")
	commit("the side commit")
	fixture.Run(repo, "git", "checkout", "--quiet", "main")
	write("main.md", "a line of the main branch\n")
	commit("the main commit")
	// The merge itself is clean; what the resolution adds is this file, which stands in neither
	// parent and is the whole of what the merge commit publishes of its own.
	fixture.Run(repo, "git", "merge", "--quiet", "--no-ff", "--no-commit", "side")
	write("resolution.md", "the runner answers at "+theHost+"\n")
	head := commit("the merge")
	if parents := fixture.Run(repo, "git", "rev-list", "--parents", "-n", "1", head); len(strings.Fields(parents)) != 3 {
		t.Fatalf("the fixture's head is %q, want a commit of two parents", parents)
	}
	destinationAt(t, fixture, repo, theURL, base+":refs/heads/main")
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	err := guard.Push(bound(t), PushRequest{
		Dir:     repo,
		Remote:  "origin",
		URL:     theURL,
		Updates: []Update{{LocalRef: "refs/heads/main", LocalSHA: head, RemoteRef: "refs/heads/main", RemoteSHA: base}},
	})
	want := []string{"resolution.md: " + theClass}
	if refused := refusal(t, err); !slices.Equal(refused.Lines, want) {
		t.Errorf("the report is %q, want %q", refused.Lines, want)
	}
}

// pushOf is a repository whose base the destination at theURL holds on its main, the base
// writing the files held, and head, the commit the push adds, on that base, which writes the
// files added, each path to its content. Where side is not nil, head is a merge, in the shape of
// TestAMergesOwnResolutionIsRead: of main, at a commit of its own on the base, and of a branch
// side, whose commit on the base writes the files side names, with the files added written by
// the merge's resolution. The destination holds side's commit too, at a branch of its own, so that
// the push adds main's commit and the merge alone: a file side writes is read as a parent's file
// of the merge, and never as a file of a commit the push adds.
func pushOf(t *testing.T, held, side, added map[string]string) (repo, base, head string) {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	repo = filepath.Join(root, "repo")
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	write := func(files map[string]string) {
		t.Helper()
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	commit := func(message string) string {
		t.Helper()
		fixture.Run(repo, "git", "add", "-A")
		fixture.Run(repo, "git", "commit", "--quiet", "-m", message)
		return fixture.Run(repo, "git", "rev-parse", "HEAD")
	}
	write(map[string]string{"README.md": "one\n"})
	write(held)
	base = commit("the commit the destination holds")
	if side != nil {
		fixture.Run(repo, "git", "checkout", "--quiet", "-b", "side")
		write(map[string]string{"side.md": "a line of the side branch\n"})
		write(side)
		commit("the side commit")
		fixture.Run(repo, "git", "checkout", "--quiet", "main")
		write(map[string]string{"main.md": "a line of the main branch\n"})
		commit("the main commit")
		fixture.Run(repo, "git", "merge", "--quiet", "--no-ff", "--no-commit", "side")
	}
	write(added)
	head = commit("the commit the push adds")
	refspecs := []string{base + ":refs/heads/main"}
	if side != nil {
		refspecs = append(refspecs, "side:refs/heads/side")
	}
	destinationAt(t, fixture, repo, theURL, refspecs...)
	return repo, base, head
}

// pushedTo is the push of head to main, where the destination holds base, from repo to origin at
// theURL.
func pushedTo(repo, base, head string) PushRequest {
	return PushRequest{
		Dir:     repo,
		Remote:  "origin",
		URL:     theURL,
		Updates: []Update{{LocalRef: "refs/heads/main", LocalSHA: head, RemoteRef: "refs/heads/main", RemoteSHA: base}},
	}
}

// A file is binary by its own content alone, whatever an attribute says: a file .gitattributes
// marks -diff or binary is read as text, the .gitattributes the pushed commit adds itself
// included, where git's own diff would write "Binary files ... differ" for it and no line of it
// would reach the rules. A merge reads its files as blobs, which no attribute touches, so a merge's
// resolution is read alike. A file that holds a zero byte in its first 8000 bytes adds no line,
// in a commit of one parent and in a merge's resolution alike (mergeFiles), and a commit that adds
// a line to such a file adds none, although the zero byte is in no line it adds.
func TestAFileIsBinaryByItsOwnContentAlone(t *testing.T) {
	const named = "the runner answers at " + theHost + "\n"
	refused := []string{"notes.lock: " + theClass}
	for _, tc := range []struct {
		name              string
		held, side, added map[string]string
		// want is the report, and nil where the push passes.
		want []string
	}{
		{
			name:  "a file the commit's .gitattributes marks -diff",
			added: map[string]string{".gitattributes": "notes.lock -diff\n", "notes.lock": named},
			want:  refused,
		},
		{
			name:  "a file the commit's .gitattributes marks binary",
			added: map[string]string{".gitattributes": "notes.lock binary\n", "notes.lock": named},
			want:  refused,
		},
		{
			name:  "a file a merge's resolution adds, its .gitattributes marking it -diff",
			side:  map[string]string{},
			added: map[string]string{".gitattributes": "notes.lock -diff\n", "notes.lock": named},
			want:  refused,
		},
		{
			name:  "a file with a zero byte, in a commit of one parent",
			added: map[string]string{"notes.lock": "\x00\n" + named},
		},
		{
			name:  "a file with a zero byte, in a merge's resolution",
			side:  map[string]string{},
			added: map[string]string{"notes.lock": "\x00\n" + named},
		},
		{
			name:  "a line added to a file whose zero byte the commit does not add",
			held:  map[string]string{"notes.lock": "\x00\n"},
			added: map[string]string{"notes.lock": "\x00\n" + named},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, base, head := pushOf(t, tc.held, tc.side, tc.added)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			err := guard.Push(bound(t), pushedTo(repo, base, head))
			if tc.want == nil {
				if err != nil {
					t.Fatalf("a push whose one named line is in a binary file: %v", err)
				}
				return
			}
			if refused := refusal(t, err); !slices.Equal(refused.Lines, tc.want) {
				t.Errorf("the report is %q, want %q", refused.Lines, tc.want)
			}
		})
	}
}

// A merge's text file above blobBound refuses the push as a setup failure that names the file and
// its size, whether it is the merge's own or a parent's at a path the merge changes: a file passed
// over, or read in part, would pass its lines unread. A binary file above the bound is read no
// further than its first 8000 bytes and adds no line, and a text file at the bound is read. The
// bound here is 64 bytes.
func TestAMergesFileAboveTheBoundRefusesThePush(t *testing.T) {
	kept := blobBound
	blobBound = 64
	t.Cleanup(func() { blobBound = kept })
	big := strings.Repeat("a line of ordinary prose\n", 4)
	line := "at " + theHost
	atBound := line + strings.Repeat(" ", 63-len(line)) + "\n"
	const failed = "LEAK CHECK SETUP ERROR: the push to origin: the push's commits could not be read: "
	for _, tc := range []struct {
		name        string
		side, added map[string]string
		// failure is the setup failure for the merge, "" where there is none.
		failure func(merge string) string
		// want is the report, and nil where the push passes or fails.
		want []string
	}{
		{
			name:  "the merge's own file",
			added: map[string]string{"big.md": big},
			failure: func(merge string) string {
				return failed + "merge " + short(merge) + "'s file big.md is 100 bytes, more than the 64 bytes a merge's file is read up to"
			},
		},
		{
			name:  "a parent's file at a path the merge changes",
			side:  map[string]string{"big.md": big},
			added: map[string]string{"big.md": "a line of the resolution\n"},
			failure: func(merge string) string {
				return failed + "a parent of merge " + short(merge) + "'s file big.md is 100 bytes, more than the 64 bytes a merge's file is read up to"
			},
		},
		{
			name:  "a binary file above the bound",
			added: map[string]string{"big.bin": "\x00" + big + "the runner answers at " + theHost + "\n"},
		},
		{
			name:  "a text file at the bound",
			added: map[string]string{"edge.md": atBound},
			want:  []string{"edge.md: " + theClass},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			side := tc.side
			if side == nil {
				side = map[string]string{}
			}
			repo, base, head := pushOf(t, nil, side, tc.added)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			err := guard.Push(bound(t), pushedTo(repo, base, head))
			switch {
			case tc.failure != nil:
				setupFailure(t, err)
				if want := tc.failure(head); err.Error() != want {
					t.Errorf("the failure is\n  %s\nwant\n  %s", err, want)
				}
			case tc.want != nil:
				if refused := refusal(t, err); !slices.Equal(refused.Lines, tc.want) {
					t.Errorf("the report is %q, want %q", refused.Lines, tc.want)
				}
			case err != nil:
				t.Fatalf("a merge whose one named line is in a binary file: %v", err)
			}
		})
	}
}

// diffRecord is the PATH entry of a stand-in git that runs the real git for every call, and keeps
// in the file record what each diff of a commit's lines answers, its patch: what of a commit's
// files the guard's diffs read. No diff, no file.
func diffRecord(t *testing.T) (path, record string) {
	t.Helper()
	bin := t.TempDir()
	record = filepath.Join(t.TempDir(), "patches")
	writeProgram(t, bin, "git", `git=`+shQuote(fixturegit.CompiledGit(t))+` record=`+shQuote(record)+`
case " $* " in
*" --unified=0 "*)
  "$git" "$@" >"$record.out" || exit
  cat "$record.out" >>"$record"
  exec cat "$record.out" ;;
esac
exec "$git" "$@"
`)
	return "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"), record
}

// A commit of one parent reads its files bounded, as a merge does: each file a line could come
// from is classed by its own first 8000 bytes before any is diffed. A binary file adds no line
// and is in no diff, whatever its size, whether the commit's file is binary or the parent's file
// it replaces is. A text file above blobBound refuses the push as a setup failure that names the
// file and its size, whether it is the commit's own or the parent's file the commit's is diffed
// against: a file passed over, or read in part, would pass its lines unread. A text file at the
// bound is read, a text file that replaces a binary one adds every line it has, and of a commit
// that changes a binary file and a text file, the text file alone is diffed. The bound here is 64
// bytes, and what the guard's diffs answer is kept (diffRecord), to say what they read.
func TestACommitsFileAboveTheBoundRefusesThePush(t *testing.T) {
	kept := blobBound
	blobBound = 64
	t.Cleanup(func() { blobBound = kept })
	big := strings.Repeat("a line of ordinary prose\n", 4)
	named := "the runner answers at " + theHost + "\n"
	line := "at " + theHost
	atBound := line + strings.Repeat(" ", 63-len(line)) + "\n"
	// unseen is a binary file's text, which no diff of the guard's may hold.
	const unseen = "the bytes of a binary file"
	const failed = "LEAK CHECK SETUP ERROR: the push to origin: the push's commits could not be read: "
	for _, tc := range []struct {
		name        string
		held, added map[string]string
		// failure is the setup failure for the commit, "" where there is none.
		failure func(commit string) string
		// want is the report, and nil where the push passes or fails.
		want []string
	}{
		{
			name:  "a binary file above the bound",
			added: map[string]string{"big.bin": "\x00" + unseen + "\n" + big + named},
		},
		{
			name:  "a text file above the bound",
			added: map[string]string{"big.md": big},
			failure: func(commit string) string {
				return failed + "commit " + short(commit) + "'s file big.md is 100 bytes, more than the 64 bytes a commit's file is read up to"
			},
		},
		{
			name:  "the parent's text file above the bound, which the commit's is diffed against",
			held:  map[string]string{"big.md": big},
			added: map[string]string{"big.md": "a line of the commit\n"},
			failure: func(commit string) string {
				return failed + "the parent of commit " + short(commit) + "'s file big.md is 100 bytes, more than the 64 bytes a commit's file is read up to"
			},
		},
		{
			name:  "a text file at the bound",
			added: map[string]string{"edge.md": atBound},
			want:  []string{"edge.md: " + theClass},
		},
		{
			name:  "a binary file and a text file the commit changes",
			held:  map[string]string{"image.bin": "\x00one\n", "notes.md": "one\n"},
			added: map[string]string{"image.bin": "\x00" + unseen + "\n" + named, "notes.md": "one\n" + named},
			want:  []string{"notes.md: " + theClass},
		},
		{
			name:  "a text file in place of a binary one",
			held:  map[string]string{"notes.lock": "\x00" + unseen + "\n"},
			added: map[string]string{"notes.lock": named},
			want:  []string{"notes.lock: " + theClass},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, base, head := pushOf(t, tc.held, nil, tc.added)
			path, record := diffRecord(t)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path)
			err := guard.Push(bound(t), pushedTo(repo, base, head))
			switch {
			case tc.failure != nil:
				setupFailure(t, err)
				if want := tc.failure(head); err.Error() != want {
					t.Errorf("the failure is\n  %s\nwant\n  %s", err, want)
				}
			case tc.want != nil:
				if refused := refusal(t, err); !slices.Equal(refused.Lines, tc.want) {
					t.Errorf("the report is %q, want %q", refused.Lines, tc.want)
				}
			case err != nil:
				t.Fatalf("a commit whose one named line is in a binary file: %v", err)
			}
			patches, err := os.ReadFile(record)
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				t.Fatal(err)
			}
			if strings.Contains(string(patches), unseen) {
				t.Errorf("a diff of the guard's read a binary file:\n%s", patches)
			}
		})
	}
}

// What git answers is read from its standard output alone, and a warning git writes to its
// standard error is no part of it. git writes a warning at once and its output in blocks, so a
// warning can land in the middle of a line of the patch a commit's lines are read from, and read
// together with the output, it would cut that line in two: the half after it would be no added
// line. The stand-in git here writes the warning diff-tree writes of a commit with too many files
// to find its renames among, in the middle of the line the commit adds, and runs the real git for
// everything else.
func TestAWarningGitWritesIsNoPartOfWhatItAnswers(t *testing.T) {
	bin := t.TempDir()
	writeProgram(t, bin, "git", `git=`+shQuote(fixturegit.CompiledGit(t))+`
case " $* " in
*" --unified=0 "*)
  patch=$("$git" "$@" && echo .) || exit
  patch=${patch%.}
  before=${patch%%"answers at"*}
  printf '%s' "$before"
  echo 'warning: exhaustive rename detection was skipped due to too many files.' >&2
  printf '%s' "${patch#"$before"}"
  exit 0 ;;
esac
exec "$git" "$@"
`)
	repo, base, head := pushOf(t, map[string]string{"notes.md": "one\n"}, nil, map[string]string{"notes.md": "one\nthe runner answers at " + theHost + "\n"})
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := guard.Push(bound(t), pushedTo(repo, base, head))
	want := []string{"notes.md: " + theClass}
	if refused := refusal(t, err); !slices.Equal(refused.Lines, want) {
		t.Errorf("the report is %q, want %q", refused.Lines, want)
	}
}

// The read of what the destination holds fetches nothing. In a partial clone, as an issue pod's
// fork clones are (--filter=blob:none), git fetches an object the clone lacks from the promisor
// remote the moment anything asks for it. The read of the destination's refs asks for every
// commit they are at (Guard.advertised), and the read of the commits an update adds asks for the
// one its remote side names (Guard.holds): a commit the clone lacks is answered missing instead,
// and holds nothing. The promisor here has a commit the clone never fetched, which the destination
// holds at its branch later; once the push is read, the clone still lacks it, and the push's own
// commit is read and refused. The push moves main, whose remote side is a commit the clone has,
// or later itself, whose remote side is the commit the clone lacks.
func TestTheReadOfWhatTheDestinationHoldsFetchesNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		// ref is the ref the push moves, and atLater is whether its remote side is the commit the
		// clone lacks rather than the base.
		ref     string
		atLater bool
	}{
		{name: "a branch of the destination's", ref: "refs/heads/main"},
		{name: "the commit the update's remote side names", ref: "refs/heads/later", atLater: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			promisor, destination := filepath.Join(root, "promisor.git"), filepath.Join(root, "destination.git")
			seed, repo := filepath.Join(root, "seed"), filepath.Join(root, "repo")
			fixture := fixturegit.Isolated(t, home)
			fixture.Run(root, fixturegit.InitBare(promisor, "main")...)
			fixture.Run(promisor, "git", "config", "uploadpack.allowFilter", "true")
			fixture.Run(promisor, "git", "config", "uploadpack.allowAnySHA1InWant", "true")
			fixture.Run(root, fixturegit.InitBare(destination, "main")...)
			fixture.Run(root, fixturegit.InitWorkTree(seed, "main")...)
			commit := func(dir, name, content, message string) string {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				fixture.Run(dir, "git", "add", "-A")
				fixture.Run(dir, "git", "commit", "--quiet", "-m", message)
				return fixture.Run(dir, "git", "rev-parse", "HEAD")
			}
			base := commit(seed, "README.md", "one\n", "the commit the destination holds")
			fixture.Run(seed, "git", "push", "--quiet", promisor, "main")
			fixture.Run(root, "git", "clone", "--quiet", "--filter=blob:none", "file://"+promisor, repo)
			later := commit(seed, "README.md", "one\ntwo\n", "a commit the clone never fetched")
			fixture.Run(seed, "git", "push", "--quiet", promisor, later+":refs/heads/later")
			fixture.Run(seed, "git", "push", "--quiet", destination, base+":refs/heads/main", later+":refs/heads/later")
			reach(t, fixture, repo, theURL, destination)
			// lacks is whether the clone lacks the later commit, asked so that the asking fetches
			// nothing.
			lacks := func() bool {
				_, err := fixture.With("GIT_NO_LAZY_FETCH=1").Try(repo, "git", "cat-file", "-e", later)
				return err != nil
			}
			if !lacks() {
				t.Fatalf("the clone has %s before the push is read, so this test proves nothing", short(later))
			}
			head := commit(repo, "notes.md", "the runner answers at "+theHost+"\n", "a message of no interest")
			remoteSHA := base
			if tc.atLater {
				remoteSHA = later
			}
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			err := guard.Push(bound(t), PushRequest{
				Dir:     repo,
				Remote:  "origin",
				URL:     theURL,
				Updates: []Update{{LocalRef: tc.ref, LocalSHA: head, RemoteRef: tc.ref, RemoteSHA: remoteSHA}},
			})
			want := []string{"notes.md: " + theClass}
			if refused := refusal(t, err); !slices.Equal(refused.Lines, want) {
				t.Errorf("the report is %q, want %q", refused.Lines, want)
			}
			if !lacks() {
				t.Errorf("the read of the push fetched %s, the commit the destination's later branch is at, into the clone", short(later))
			}
		})
	}
}

// A failure that names a URL carries no credential. The URL git hands a pre-push hook, and the
// upstream a knives registry entry names, can hold a user and a token
// (https://user:token@host/...), and a read of either that fails names the URL and quotes git,
// which names it too: both are written without the user and the token. The upstream is read for
// a push the knives pass would refuse and for one the private-name rules would. A push made to a
// URL rather than to a remote, `git push https://user:token@host/...`, hands the hook that URL as
// the remote's name too, which every failure of the push names (PushName). The stand-in git here
// fails every call with a URL that holds a user, as a git that cannot reach the host does, and
// runs the real git for every other call.
func TestAFailedReadNamesItsURLWithoutItsCredential(t *testing.T) {
	// The userinfo is built apart from the scheme, so no line of this file is a URL that carries
	// one, which the shared check would refuse at the push.
	const user, token = "invented-user", "invented-token"
	userinfo := user + ":" + token + "@"
	credentialed := func(url string) string { return strings.Replace(url, "https://", "https://"+userinfo, 1) }
	bin := t.TempDir()
	writeProgram(t, bin, "git", `for arg; do
  case "$arg" in
  *://*@*) echo "fatal: unable to access '$arg/': Could not resolve host: github.com" >&2; exit 128 ;;
  esac
done
exec `+shQuote(fixturegit.CompiledGit(t))+` "$@"
`)
	unreachable := func(url string) string {
		return " exited 128: fatal: unable to access '" + url + "/': Could not resolve host: github.com"
	}
	for _, tc := range []struct {
		name string
		// remote is the remote's name git hands the hook, "origin" where the case leaves it empty;
		// url is the URL it hands it, and upstream the one the registry entry names. line is the
		// line the pushed commit adds, one holding a knives term where the case leaves it empty.
		remote, url, upstream, line string
		want                        string
	}{
		{
			name:     "the destination",
			url:      credentialed(knivesOrigin),
			upstream: knivesUpstream,
			want:     "LEAK CHECK SETUP ERROR: the push to origin: the destination's refs could not be read: git ls-remote " + knivesOrigin + unreachable(knivesOrigin),
		},
		{
			name:     "the destination named by its URL",
			remote:   credentialed(knivesOrigin),
			url:      credentialed(knivesOrigin),
			upstream: knivesUpstream,
			want:     "LEAK CHECK SETUP ERROR: the push to " + knivesOrigin + ": the destination's refs could not be read: git ls-remote " + knivesOrigin + unreachable(knivesOrigin),
		},
		{
			name:     "the registry entry's upstream",
			url:      knivesOrigin,
			upstream: credentialed(knivesUpstream),
			want:     "LEAK CHECK SETUP ERROR: the push to origin: the refs of the registry entry's upstream could not be read: git ls-remote " + knivesUpstream + unreachable(knivesUpstream),
		},
		{
			name:     "the registry entry's upstream, for a push the private-name rules would refuse",
			url:      knivesOrigin,
			upstream: credentialed(knivesUpstream),
			line:     upstreamNamesAHost,
			want:     "LEAK CHECK SETUP ERROR: the push to origin: the refs of the registry entry's upstream could not be read: git ls-remote " + knivesUpstream + unreachable(knivesUpstream),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := tc.line
			if line == "" {
				line = "the QUILLWORTHY build\n"
			}
			_, repo, base, head := knivesCommit(t, map[string]string{"notes.md": line})
			registry := theKnivesRow
			registry.file = strings.ReplaceAll(theKnivesRegistry, knivesUpstream, tc.upstream)
			path, listed, _ := servingKnives(t, registry)
			path = "PATH=" + bin + string(os.PathListSeparator) + strings.TrimPrefix(path, "PATH=")
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path, listed)
			remote := tc.remote
			if remote == "" {
				remote = "origin"
			}
			err := guard.Push(bound(t), knivesPush(repo, base, head, remote, tc.url))
			setupFailure(t, err)
			if err.Error() != tc.want {
				t.Errorf("the failure is\n  %s\nwant\n  %s", err, tc.want)
			}
			if strings.Contains(err.Error(), user) || strings.Contains(err.Error(), token) {
				t.Errorf("the failure carries the URL's credential: %v", err)
			}
		})
	}
}

// octopusParents is how many parents the merges octopusRepo writes have: as many as
// cybermonklabs/oh-my-pi's release merges have, which run from 51 to 53.
const octopusParents = 52

// octopusRepo is the fixture of the octopus merge tests: a repository whose base, which the
// destination holds, has one file, shared.md, and octopusParents parents on that base, each of
// which adds a line of its own to shared.md, parent 17's being line17. The destination, which
// theURL reaches (reach), holds every parent at a branch of its own, so a merge of them is the
// whole of what a push of it adds. It answers the repository, the base, every, shared.md as it
// holds every parent's line, which is what each merge's shared file starts from, and octopus,
// which writes a merge of every parent whose shared.md is as shared has it, and whose
// resolution.md is as added has it, where added is not empty: a file no parent has. A merge's
// commit is written with commit-tree over a tree of the fixture's own, so no octopus strategy
// resolves 52 heads.
func octopusRepo(t *testing.T, line17 string) (repo, base, every string, octopus func(shared, added string) string) {
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
	write("shared.md", first)
	fixture.Run(repo, "git", "add", "-A")
	fixture.Run(repo, "git", "commit", "--quiet", "-m", "the commit the destination holds")
	base = fixture.Run(repo, "git", "rev-parse", "HEAD")
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
	reach(t, fixture, repo, theURL, remote)
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
	return repo, base, every, octopus
}

// An octopus merge is read from its files, as every merge is: git's combined diff misreads a
// merge of 33 parents or more (mergeFiles' doc). The merges here are octopusRepo's, and each
// one's shared file holds every parent's line. One parent's line names a host: the destination
// holds every parent, so that line is text it already has, and the merge is the whole of what
// each push adds.
func TestAnOctopusMergeIsReadFromItsFiles(t *testing.T) {
	repo, base, every, octopus := octopusRepo(t, "the runner of parent 17 answers at "+theHost)

	for _, tc := range []struct {
		name string
		head string
		// want is the report, and nil where the push passes.
		want []string
	}{
		{
			name: "the resolution adds a line to the file every parent changes",
			head: octopus(every+"the resolution answers at "+theHost+"\n", ""),
			want: []string{"shared.md: " + theClass},
		},
		{
			name: "the host is one parent's line and the merge adds none",
			head: octopus(every, ""),
		},
		{
			name: "the resolution adds a file",
			head: octopus(every, "the runner answers at "+theHost+"\n"),
			want: []string{"resolution.md: " + theClass},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			err := guard.Push(bound(t), PushRequest{
				Dir:     repo,
				Remote:  "origin",
				URL:     theURL,
				Updates: []Update{{LocalRef: "refs/heads/main", LocalSHA: tc.head, RemoteRef: "refs/heads/main", RemoteSHA: base}},
			})
			if tc.want == nil {
				if err != nil {
					t.Fatalf("a merge that adds nothing of its own: %v", err)
				}
				return
			}
			if refused := refusal(t, err); !slices.Equal(refused.Lines, tc.want) {
				t.Errorf("the report is %q, want %q", refused.Lines, tc.want)
			}
		})
	}
}

// A record of a file names the file and, for an added line, the line's number in the pushed
// commit's version of it: from the hunk's header in a commit of one parent, and from the merge's
// own file in a merge. The records of paths come before the records of lines, each in path order
// and a file's lines in line order, and a blank line is no record. Of a commit of one parent that
// changes a binary file beside a rename, the rename alone adds lines, and a submodule adds the one
// line git's diff writes of it.
func TestAFilesRecordsCarryItsPathAndLineNumber(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	repo := filepath.Join(root, "repo")
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tree := func() string {
		t.Helper()
		fixture.Run(repo, "git", "add", "-A")
		return fixture.Run(repo, "git", "write-tree")
	}
	write("README.md", "one\ntwo\nthree\nfour\n")
	base := fixture.Run(repo, "git", "commit-tree", tree(), "-m", "the base")
	write("README.md", "one\nthe second\nthree\nfour\nfive\n")
	write("new.md", "first\n\nthird\n")
	edit := fixture.Run(repo, "git", "commit-tree", tree(), "-p", base, "-m", "an edit")
	if err := os.Remove(filepath.Join(repo, "new.md")); err != nil {
		t.Fatal(err)
	}
	write("README.md", "one\ntwo\nthree\nfour\nfrom the side\n")
	side := fixture.Run(repo, "git", "commit-tree", tree(), "-p", base, "-m", "the side")
	// new.md is as the edit has it, so it is that parent's and none of the merge's own.
	write("README.md", "one\nthe second\nthree\nfour\nfive\nthe resolution's\nfrom the side\n")
	write("new.md", "first\n\nthird\n")
	write("merged.md", "only the merge\n")
	merge := fixture.Run(repo, "git", "commit-tree", tree(), "-p", edit, "-p", side, "-m", "the merge")
	// mixed, of one parent, changes a binary file and renames README.md with a line added.
	for _, name := range []string{"new.md", "merged.md"} {
		if err := os.Remove(filepath.Join(repo, name)); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "one\ntwo\nthree\nfour\n")
	write("image.bin", "\x00one\n")
	binaries := fixture.Run(repo, "git", "commit-tree", tree(), "-p", base, "-m", "a binary file")
	write("image.bin", "\x00two\nthree\n")
	if err := os.Rename(filepath.Join(repo, "README.md"), filepath.Join(repo, "NOTES.md")); err != nil {
		t.Fatal(err)
	}
	write("NOTES.md", "one\ntwo\nthree\nfour\nfive\n")
	mixed := fixture.Run(repo, "git", "commit-tree", tree(), "-p", binaries, "-m", "a rename beside a binary file")
	// submodule, of one parent, adds a submodule at vendor/lib, at the base's commit.
	fixture.Run(repo, "git", "read-tree", base)
	fixture.Run(repo, "git", "update-index", "--add", "--cacheinfo", "160000,"+base+",vendor/lib")
	submodule := fixture.Run(repo, "git", "commit-tree", fixture.Run(repo, "git", "write-tree"), "-p", base, "-m", "a submodule")

	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	for _, tc := range []struct {
		name   string
		commit string
		want   []Record
	}{
		{
			name:   "a commit of one parent",
			commit: edit,
			want: []Record{
				{Label: "new.md", Text: "new.md", File: "new.md"},
				{Label: "README.md", Text: "the second", File: "README.md", Line: 2},
				{Label: "README.md", Text: "five", File: "README.md", Line: 5},
				{Label: "new.md", Text: "first", File: "new.md", Line: 1},
				{Label: "new.md", Text: "third", File: "new.md", Line: 3},
			},
		},
		{
			name:   "a merge",
			commit: merge,
			want: []Record{
				{Label: "merged.md", Text: "merged.md", File: "merged.md"},
				{Label: "README.md", Text: "the resolution's", File: "README.md", Line: 6},
				{Label: "merged.md", Text: "only the merge", File: "merged.md", Line: 1},
			},
		},
		{
			name:   "a commit of one parent that changes a binary file beside a rename",
			commit: mixed,
			want: []Record{
				{Label: "NOTES.md", Text: "NOTES.md", File: "NOTES.md"},
				{Label: "NOTES.md", Text: "five", File: "NOTES.md", Line: 5},
			},
		},
		{
			name:   "a commit of one parent that adds a submodule",
			commit: submodule,
			want: []Record{
				{Label: "vendor/lib", Text: "vendor/lib", File: "vendor/lib"},
				{Label: "vendor/lib", Text: "Subproject commit " + base, File: "vendor/lib", Line: 1},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records, err := guard.commitRecords(bound(t), repo, tc.commit)
			if err != nil {
				t.Fatalf("read the commit: %v", err)
			}
			// The identities and the message are records of no file.
			files := slices.DeleteFunc(records, func(record Record) bool { return record.File == "" })
			if !slices.Equal(files, tc.want) {
				t.Errorf("the records of files are %+v, want %+v", files, tc.want)
			}
		})
	}
}

// A forward merge, the default branch merged onto an older tip of a branch the destination
// holds, reads no commit the destination's branches reach. The default branch's commit here adds
// a line the rules refuse, and the destination's main holds it, so the push of the merge does not
// publish it, whichever ref the push moves. A commit that adds the same line on top of the merge
// is on no branch of the destination, so it is read and refused, and it is the whole of the
// report.
func TestAForwardMergeReadsNoCommitTheDestinationHolds(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	remote, repo := filepath.Join(root, "remote.git"), filepath.Join(root, "repo")
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitBare(remote, "main")...)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(message string) string {
		t.Helper()
		fixture.Run(repo, "git", "add", "-A")
		fixture.Run(repo, "git", "commit", "--quiet", "-m", message)
		return fixture.Run(repo, "git", "rev-parse", "HEAD")
	}
	write("README.md", "one\n")
	commit("the first commit")
	fixture.Run(repo, "git", "checkout", "--quiet", "-b", "work")
	write("work.md", "a line of the branch\n")
	tip := commit("the branch's own commit")
	fixture.Run(repo, "git", "checkout", "--quiet", "main")
	write("main.md", "the runner answers at "+theHost+"\n")
	commit("a commit of the default branch")
	// The destination holds both branches: its work is the old tip the push moves, and its main
	// holds the default branch's commit.
	fixture.Run(repo, "git", "remote", "add", "origin", remote)
	fixture.Run(repo, "git", "push", "--quiet", "origin", "main", "work")
	reach(t, fixture, repo, theURL, remote)
	fixture.Run(repo, "git", "checkout", "--quiet", "work")
	fixture.Run(repo, "git", "merge", "--quiet", "--no-ff", "-m", "merge main into work", "main")
	merge := fixture.Run(repo, "git", "rev-parse", "HEAD")
	if parents := fixture.Run(repo, "git", "rev-list", "--parents", "-n", "1", merge); len(strings.Fields(parents)) != 3 {
		t.Fatalf("the fixture's merge is %q, want a commit of two parents", parents)
	}
	write("copy.md", "the runner answers at "+theHost+"\n")
	onTop := commit("a commit the destination does not hold")

	for _, tc := range []struct {
		name string
		head string
		// want is the report, and nil where the push passes.
		want []string
	}{
		{name: "the merge alone", head: merge},
		{
			name: "a commit of the same line on top of the merge",
			head: onTop,
			want: []string{"copy.md: " + theClass},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			err := guard.Push(bound(t), PushRequest{
				Dir:     repo,
				Remote:  "origin",
				URL:     theURL,
				Updates: []Update{{LocalRef: "refs/heads/work", LocalSHA: tc.head, RemoteRef: "refs/heads/work", RemoteSHA: tip}},
			})
			if tc.want == nil {
				if err != nil {
					t.Fatalf("the push of the merge alone: %v", err)
				}
				return
			}
			if refused := refusal(t, err); !slices.Equal(refused.Lines, tc.want) {
				t.Errorf("the report is %q, want %q", refused.Lines, tc.want)
			}
		})
	}
}

// A commit a rehearsal holds is still one the real push publishes. Before the real push of a
// release branch, a role rehearses it on a local bare repository: through a remote of its own, or
// by pointing the pushed remote's push URL at the bare for one run. Either way git writes a
// remote-tracking ref under the pushed remote's own refs/remotes/origin/ — for a remote named
// origin/rehearsal at refs/remotes/origin/rehearsal/*, and for origin's own push URL at
// refs/remotes/origin/* — and that ref says nothing of what the destination holds. The commit
// here adds a line the rules refuse, and the destination, whose main is at the commit before it,
// does not hold it, so the push to origin reads it and is refused. A push to the rehearsal itself
// reads none of what the rehearsal holds.
func TestACommitARehearsalHoldsIsReadAtThePushToTheDestination(t *testing.T) {
	for _, tc := range []struct {
		name string
		// rehearse pushes HEAD to rehearsal's refs/heads/rehearsal, and answers the remote that
		// push named.
		rehearse func(run func(string, ...string) string, repo, rehearsal string) string
		// tracked is the remote-tracking ref the rehearsal's push wrote.
		tracked string
	}{
		{
			// git 2.56 refuses `git remote add` of a name that is another remote's and a slash
			// ("is a subset of existing remote"); a configuration written by hand still holds one.
			name: "a remote named origin/rehearsal",
			rehearse: func(run func(string, ...string) string, repo, rehearsal string) string {
				run(repo, "git", "config", "remote.origin/rehearsal.url", rehearsal)
				run(repo, "git", "config", "remote.origin/rehearsal.fetch", "+refs/heads/*:refs/remotes/origin/rehearsal/*")
				run(repo, "git", "push", "--quiet", "origin/rehearsal", "HEAD:refs/heads/rehearsal")
				return "origin/rehearsal"
			},
			tracked: "refs/remotes/origin/rehearsal/rehearsal",
		},
		{
			name: "origin's push URL pointed at the rehearsal for one push",
			rehearse: func(run func(string, ...string) string, repo, rehearsal string) string {
				run(repo, "git", "remote", "set-url", "--push", "origin", rehearsal)
				run(repo, "git", "push", "--quiet", "origin", "HEAD:refs/heads/rehearsal")
				run(repo, "git", "config", "--unset", "remote.origin.pushurl")
				return "origin"
			},
			tracked: "refs/remotes/origin/rehearsal",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			remote, rehearsal, repo := filepath.Join(root, "remote.git"), filepath.Join(root, "rehearsal.git"), filepath.Join(root, "repo")
			fixture := fixturegit.Isolated(t, home)
			fixture.Run(root, fixturegit.InitBare(remote, "main")...)
			fixture.Run(root, fixturegit.InitBare(rehearsal, "main")...)
			fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
			write := func(content string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			commit := func(message string) string {
				t.Helper()
				fixture.Run(repo, "git", "add", "-A")
				fixture.Run(repo, "git", "commit", "--quiet", "-m", message)
				return fixture.Run(repo, "git", "rev-parse", "HEAD")
			}
			write("one\n")
			base := commit("the commit the destination holds")
			fixture.Run(repo, "git", "remote", "add", "origin", remote)
			fixture.Run(repo, "git", "push", "--quiet", "origin", "main")
			reach(t, fixture, repo, theURL, remote)
			write("one\nthe runner answers at " + theHost + "\n")
			head := commit("a message of no interest")
			rehearsed := tc.rehearse(fixture.Run, repo, rehearsal)
			if held := fixture.Run(repo, "git", "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes/"); held != "refs/remotes/origin/main "+base+"\n"+tc.tracked+" "+head {
				t.Fatalf("the fixture's remote-tracking refs are %q, want origin/main at the base and %s at the commit", held, tc.tracked)
			}
			for _, push := range []struct {
				name string
				// remote and url are what git hands the hook; remoteSHA is the commit the push's
				// destination holds at refs/heads/main.
				remote, url, remoteSHA string
				// want is the report, and nil where the push passes.
				want []string
			}{
				{name: "the push to origin", remote: "origin", url: theURL, remoteSHA: base, want: []string{"README.md: " + theClass}},
				{name: "the push to the rehearsal", remote: rehearsed, url: rehearsal, remoteSHA: noSHA},
			} {
				guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
				err := guard.Push(bound(t), PushRequest{
					Dir:     repo,
					Remote:  push.remote,
					URL:     push.url,
					Updates: []Update{{LocalRef: "refs/heads/main", LocalSHA: head, RemoteRef: "refs/heads/main", RemoteSHA: push.remoteSHA}},
				})
				if push.want == nil {
					if err != nil {
						t.Errorf("%s, of a commit the rehearsal holds: %v", push.name, err)
					}
					continue
				}
				if refused := refusal(t, err); !slices.Equal(refused.Lines, push.want) {
					t.Errorf("%s: the report is %q, want %q", push.name, refused.Lines, push.want)
				}
			}
		})
	}
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
