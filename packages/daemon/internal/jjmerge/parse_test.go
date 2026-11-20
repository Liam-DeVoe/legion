package jjmerge

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The blobs the captured histories share: f's base, ours and theirs (capture.sh), whose ids are
// git's ids of those three texts.
const (
	blobBase   = "df967b96a579e45a18b8251732d16804b2e56a55" // "base\n"
	blobOurs   = "b19a1e93bec1317dc6097229e12afaffbfa74dc2" // "ours\n"
	blobTheirs = "950b81b7eee953d050aa05a641f8e056c85dd1bd" // "theirs\n"
)

// oddPath is the odd-path shape's path: a leading dash, a space, a tab and a newline, which -z
// prints as they are.
const oddPath = "-odd path\twith tab\nand newline"

// Row P1 of dispatch://INFRA-4149's plan: the parser reads what the real git printed for each
// shape of merge, captured by testdata/merge-tree/capture.sh with git 2.47.3, the worker image's.
// Each tree id is the one git printed first in that capture. When the image's git moves, the
// script captures again and these ids follow.
func TestParseMergeTreeReadsEachCapturedShape(t *testing.T) {
	for _, tc := range []struct {
		shape string
		want  MergeTree
		paths []string
	}{
		{
			shape: "clean",
			want:  MergeTree{Tree: "245798040e3241d17c86a8b2fc7faa519b261aa8"},
		},
		{
			shape: "content",
			want: MergeTree{
				Tree: "25047204965ddac7c788bba6e118d3286698ad6d",
				Conflicts: []Entry{
					{Mode: "100644", OID: blobBase, Stage: 1, Path: "f"},
					{Mode: "100644", OID: blobOurs, Stage: 2, Path: "f"},
					{Mode: "100644", OID: blobTheirs, Stage: 3, Path: "f"},
				},
				Messages: []Message{
					{Paths: []string{"f"}, Type: "Auto-merging", Text: "Auto-merging f"},
					{Paths: []string{"f"}, Type: "CONFLICT (contents)", Text: "CONFLICT (content): Merge conflict in f"},
				},
			},
			paths: []string{"f"},
		},
		{
			shape: "modify-delete",
			want: MergeTree{
				Tree: "3004572edbfe43cf2e3e1e98ee8fc8af1ecfbf34",
				Conflicts: []Entry{
					{Mode: "100644", OID: blobBase, Stage: 1, Path: "f"},
					{Mode: "100644", OID: blobOurs, Stage: 2, Path: "f"},
				},
				Messages: []Message{{
					Paths: []string{"f"},
					Type:  "CONFLICT (modify/delete)",
					Text:  "CONFLICT (modify/delete): f deleted in b and modified in a.  Version a of f left in tree.",
				}},
			},
			paths: []string{"f"},
		},
		{
			shape: "rename-rename",
			want: MergeTree{
				Tree: "9b68fcd10b9cf1922c65560f0ddb92a5d985b137",
				Conflicts: []Entry{
					{Mode: "100644", OID: "4cb29ea38f70d7c61b2a3a25b02e3bdf44905402", Stage: 1, Path: "f"},
					{Mode: "100644", OID: "4cb29ea38f70d7c61b2a3a25b02e3bdf44905402", Stage: 2, Path: "g"},
					{Mode: "100644", OID: "4cb29ea38f70d7c61b2a3a25b02e3bdf44905402", Stage: 3, Path: "h"},
				},
				Messages: []Message{{
					Paths: []string{"f", "g", "h"},
					Type:  "CONFLICT (rename/rename)",
					Text:  "CONFLICT (rename/rename): f renamed to g in a and to h in b.",
				}},
			},
			paths: []string{"f", "g", "h"},
		},
		{
			shape: "file-directory",
			want: MergeTree{
				Tree: "236f3adc68df2e3acebdb5da6afdc683a9edaa51",
				Conflicts: []Entry{
					{Mode: "100644", OID: "f73f3093ff865c514c6c51f867e35f693487d0d3", Stage: 2, Path: "d~a"},
				},
				Messages: []Message{{
					Paths: []string{"d~a", "d"},
					Type:  "CONFLICT (file/directory)",
					Text:  "CONFLICT (file/directory): directory in the way of d from a; moving it to d~a instead.",
				}},
			},
			paths: []string{"d~a"},
		},
		{
			shape: "binary",
			want: MergeTree{
				Tree: "335a911b0d22b77e39fed7952713655bd1317205",
				Conflicts: []Entry{
					{Mode: "100644", OID: "c1011510a5248dda6856ecff4139cb218b92ad0c", Stage: 1, Path: "bin"},
					{Mode: "100644", OID: "52682ff6c8bc57a97d1b28ce8f03354bf3f463c3", Stage: 2, Path: "bin"},
					{Mode: "100644", OID: "5bc3c30d64b10fb169a1aad5269d1104d30a298e", Stage: 3, Path: "bin"},
				},
				Messages: []Message{
					{Paths: []string{"bin"}, Type: "CONFLICT (binary)", Text: "warning: Cannot merge binary files: bin (a vs. b)"},
					{Paths: []string{"bin"}, Type: "Auto-merging", Text: "Auto-merging bin"},
					{Paths: []string{"bin"}, Type: "CONFLICT (contents)", Text: "CONFLICT (content): Merge conflict in bin"},
				},
			},
			paths: []string{"bin"},
		},
		{
			shape: "odd-path",
			want: MergeTree{
				Tree: "ae2264bbf832aa3f9cdf346e770907c7a6e2aebd",
				Conflicts: []Entry{
					{Mode: "100644", OID: blobBase, Stage: 1, Path: oddPath},
					{Mode: "100644", OID: blobOurs, Stage: 2, Path: oddPath},
					{Mode: "100644", OID: blobTheirs, Stage: 3, Path: oddPath},
				},
				// Each text ends in the path, whose own newline stays: only git's last one goes.
				Messages: []Message{
					{Paths: []string{oddPath}, Type: "Auto-merging", Text: "Auto-merging " + oddPath},
					{Paths: []string{oddPath}, Type: "CONFLICT (contents)", Text: "CONFLICT (content): Merge conflict in " + oddPath},
				},
			},
			paths: []string{oddPath},
		},
	} {
		t.Run(tc.shape, func(t *testing.T) {
			out, err := os.ReadFile(filepath.Join("testdata", "merge-tree", tc.shape+".z"))
			if err != nil {
				t.Fatal(err)
			}
			got, err := parseMergeTree(out)
			if err != nil {
				t.Fatalf("parseMergeTree: %v", err)
			}
			if got.Tree != tc.want.Tree {
				t.Errorf("tree = %q, want %q", got.Tree, tc.want.Tree)
			}
			if !reflect.DeepEqual(got.Conflicts, tc.want.Conflicts) {
				t.Errorf("conflicts =\n%#v\nwant\n%#v", got.Conflicts, tc.want.Conflicts)
			}
			if !reflect.DeepEqual(got.Messages, tc.want.Messages) {
				t.Errorf("messages =\n%#v\nwant\n%#v", got.Messages, tc.want.Messages)
			}
			if paths := got.ConflictedPaths(); !reflect.DeepEqual(paths, tc.paths) {
				t.Errorf("ConflictedPaths() = %q, want %q", paths, tc.paths)
			}
		})
	}
}

// A clean merge's output can hold messages too, when git is asked for them: the tree, the empty
// field that begins the messages, and the messages, with no conflict record. An empty messages
// section reads as none.
func TestParseMergeTreeReadsMessagesOfACleanMerge(t *testing.T) {
	const tree = "245798040e3241d17c86a8b2fc7faa519b261aa8"
	for _, tc := range []struct {
		name string
		out  string
		want []Message
	}{
		{
			name: "one message",
			out:  tree + "\x00\x00" + "1\x00f\x00Auto-merging\x00Auto-merging f\n\x00",
			want: []Message{{Paths: []string{"f"}, Type: "Auto-merging", Text: "Auto-merging f"}},
		},
		{name: "no message", out: tree + "\x00\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMergeTree([]byte(tc.out))
			if err != nil {
				t.Fatalf("parseMergeTree: %v", err)
			}
			want := MergeTree{Tree: tree, Messages: tc.want}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("parseMergeTree = %#v, want %#v", got, want)
			}
			if paths := got.ConflictedPaths(); len(paths) != 0 {
				t.Errorf("ConflictedPaths() = %q, want none", paths)
			}
		})
	}
}

// Output git merge-tree would not print is refused, and the error names what is wrong in it.
func TestParseMergeTreeRefusesMalformedOutput(t *testing.T) {
	const tree = "245798040e3241d17c86a8b2fc7faa519b261aa8"
	record := func(mode, oid, stage, path string) string {
		return mode + " " + oid + " " + stage + "\t" + path + "\x00"
	}
	for _, tc := range []struct {
		name string
		out  string
		want string
	}{
		{name: "nothing", out: "", want: "it is empty, with no tree id"},
		{name: "the tree id ends in a newline", out: tree + "\n", want: `it ends with bytes that no NUL ends: "` + tree + `\n"`},
		{name: "a short tree id", out: tree[:39] + "\x00", want: `the tree id "` + tree[:39] + `" is not an object id`},
		{name: "an upper-case tree id", out: strings.ToUpper(tree) + "\x00", want: "is not an object id"},
		{name: "no tree id", out: "\x00", want: `the tree id "" is not an object id`},
		{
			name: "a record with no tab",
			out:  tree + "\x00100644 " + blobBase + " 1 f\x00",
			want: "conflict record 1 \"100644 " + blobBase + " 1 f\": it has no tab before its path",
		},
		{name: "a record with an empty path", out: tree + "\x00" + record("100644", blobBase, "1", ""), want: "its path is empty"},
		{
			name: "a record with two fields",
			out:  tree + "\x00100644 " + blobBase + "\tf\x00",
			want: "is not <mode> <oid> <stage>",
		},
		{name: "a bad mode", out: tree + "\x00" + record("100664", blobBase, "1", "f"), want: `the mode "100664" is not one an index entry has`},
		{name: "a bad object id", out: tree + "\x00" + record("100644", "f00", "1", "f"), want: `the object id "f00" is not an object id`},
		{name: "stage 0", out: tree + "\x00" + record("100644", blobBase, "0", "f"), want: `the stage "0" is not 1, 2 or 3`},
		{name: "stage 4", out: tree + "\x00" + record("100644", blobBase, "4", "f"), want: `the stage "4" is not 1, 2 or 3`},
		{
			name: "the second record's error names it",
			out:  tree + "\x00" + record("100644", blobBase, "1", "f") + record("100644", blobOurs, "x", "f"),
			want: "conflict record 2",
		},
		{
			name: "a stage repeated",
			out:  tree + "\x00" + record("100644", blobBase, "2", "f") + record("100644", blobOurs, "2", "f"),
			want: "conflict record 2 \"100644 " + blobOurs + " 2\\tf\" gives stage 2 of \"f\" a second time",
		},
		{
			name: "a path count that is no number",
			out:  tree + "\x00\x00x\x00f\x00Auto-merging\x00Auto-merging f\n\x00",
			want: `message 1: the path count "x" is not a number above 0`,
		},
		{
			name: "a path count of 0",
			out:  tree + "\x00\x000\x00Auto-merging\x00Auto-merging f\n\x00",
			want: `message 1: the path count "0" is not a number above 0`,
		},
		{
			name: "a path count past the end",
			out:  tree + "\x00\x003\x00f\x00g\x00CONFLICT (rename/rename)\x00",
			want: "message 1: its path count 3 runs past the end of the output",
		},
		{
			name: "a message with no text",
			out:  tree + "\x00\x00" + "1\x00f\x00Auto-merging\x00Auto-merging f\n\x00" + "1\x00f\x00Auto-merging\x00",
			want: "message 2: its path count 1 runs past the end of the output",
		},
		{
			name: "an extra NUL after the messages",
			out:  tree + "\x00\x00" + "1\x00f\x00Auto-merging\x00Auto-merging f\n\x00\x00",
			want: `message 2: the path count "" is not a number above 0`,
		},
		{
			name: "bytes after the last NUL",
			out:  tree + "\x00\x00" + "1\x00f\x00Auto-merging\x00Auto-merging f\n\x00junk",
			want: `it ends with bytes that no NUL ends: "junk"`,
		},
		{name: "bytes after a clean merge's tree id", out: tree + "\x00x", want: `it ends with bytes that no NUL ends: "x"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMergeTree([]byte(tc.out))
			if err == nil {
				t.Fatalf("parseMergeTree = %#v, want an error naming %q", got, tc.want)
			}
			if !strings.HasPrefix(err.Error(), "git merge-tree output: ") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want git merge-tree output: ... %q", err, tc.want)
			}
		})
	}
}
