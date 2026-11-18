package leakguard

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

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
		// A file the destination holds, which .gitattributes marks -diff or binary, is diffed when
		// the commit changes it, and git's own diff of it writes "Binary files ... differ" and no
		// line where the diff is not read as text: the line the commit adds is read all the same.
		{
			name:  "a line added to a file the destination holds, which .gitattributes marks -diff",
			held:  map[string]string{".gitattributes": "notes.lock -diff\n", "notes.lock": "a line of ordinary prose\n"},
			added: map[string]string{"notes.lock": "a line of ordinary prose\n" + named},
			want:  refused,
		},
		{
			name:  "a line added to a file the destination holds, which .gitattributes marks binary",
			held:  map[string]string{".gitattributes": "notes.lock binary\n", "notes.lock": "a line of ordinary prose\n"},
			added: map[string]string{"notes.lock": "a line of ordinary prose\n" + named},
			want:  refused,
		},
		// git reads a file as binary where a zero byte is in its first 8000 bytes, wherever in
		// them it is (xdiff-interface.c's buffer_is_binary), and as text where the first is past
		// them.
		{
			name:  "a file whose first zero byte is at 300, inside its first 8000 bytes",
			added: map[string]string{"notes.lock": named + strings.Repeat("a", 300-len(named)) + "\x00\n"},
		},
		{
			name:  "a file whose first zero byte is at 8000, past its first 8000 bytes",
			added: map[string]string{"notes.lock": named + strings.Repeat("a", 8000-len(named)) + "\x00\n"},
			want:  refused,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, base, head := pushOf(t, tc.held, tc.side, tc.added, nil)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
			err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", theURL))
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
	return pathWith(bin), record
}

// A commit of one parent reads its files bounded, as a merge does: each file a line could come
// from is classed by its own first 8000 bytes before any is diffed. A binary file adds no line
// and is in no diff, whatever its size, whether the commit's file is binary or the parent's file
// it replaces is: a binary file above the bound that the commit changes is left out of the diff
// of the text file the commit changes beside it. A text file above blobBound refuses the push as
// a setup failure that names the file and its size, whether it is the commit's own or the
// parent's file the commit's is diffed against: a file passed over, or read in part, would pass
// its lines unread. A rename or a mode change that leaves such a file as the parent has it adds
// no line, and nothing of the file is read. A text file at the bound is read, a text file that
// replaces a binary one adds every line it has, and of a commit that changes a binary file and a
// text file, the text file alone is diffed. The bound here is 64 bytes, and what the guard's
// diffs answer is kept (diffRecord), to say what they read.
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
		// edit is the change the commit makes to the working tree beyond added, nil for none.
		edit func(t *testing.T, repo string)
		// failure is the setup failure for the commit, "" where there is none.
		failure func(commit string) string
		// want is the report, and nil where the push passes or fails.
		want []string
	}{
		{
			name:  "a binary file above the bound, which the commit changes beside a text file",
			held:  map[string]string{"big.bin": "\x00" + big, "notes.md": "one\n"},
			added: map[string]string{"big.bin": "\x00" + unseen + "\n" + big + named, "notes.md": "one\ntwo\n"},
		},
		{
			name: "a text file above the bound, renamed whole",
			held: map[string]string{"big.md": big + named},
			edit: func(t *testing.T, repo string) {
				if err := os.Rename(filepath.Join(repo, "big.md"), filepath.Join(repo, "moved.md")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "a text file above the bound whose mode the commit changes",
			held: map[string]string{"big.md": big + named},
			edit: func(t *testing.T, repo string) {
				if err := os.Chmod(filepath.Join(repo, "big.md"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
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
			name:  "the commit's text file above the bound, which the parent's file it is diffed against is not",
			held:  map[string]string{"big.md": "a line of the parent\n"},
			added: map[string]string{"big.md": big},
			failure: func(commit string) string {
				return failed + "commit " + short(commit) + "'s file big.md is 100 bytes, more than the 64 bytes a commit's file is read up to"
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
			repo, base, head := pushOf(t, tc.held, nil, tc.added, tc.edit)
			path, record := diffRecord(t)
			guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public, path)
			err := guard.Push(bound(t), knivesPush(repo, base, head, "origin", theURL))
			// Read first, so that a binary file a diff read is said whatever the push's answer.
			patches, read := os.ReadFile(record)
			if read != nil && !errors.Is(read, fs.ErrNotExist) {
				t.Fatal(read)
			}
			if strings.Contains(string(patches), unseen) {
				t.Errorf("a diff of the guard's read a binary file:\n%s", patches)
			}
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
				t.Errorf("a commit that adds no line the rules refuse: %v", err)
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
