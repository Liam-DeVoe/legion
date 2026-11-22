package leakguard

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/fixturegit"
	"github.com/sjawhar/legion/daemon/internal/leakguard/leakguardtest"
)

// namedBase64 is one line of base64 that holds, as any signature's bytes can by chance, a run the
// repository rule refuses: "+" and "/" are base64's own characters, and to the rule the edges of a
// word. It is built out of two pieces for the reason theHost is.
const namedBase64 = "iQEzBAABCAAdFiEE+agen" + "tc/Rk9vYmFyYmF6"

// repositoryClass is what the rules call a match of the repository rule: its description.
const repositoryClass = "names the private deployment repository or its Dispatch project key"

// signature is a signature block as git writes one at the end of a tag's message and as a
// commit's gpgsig field, with lines as its body. No key made it, and no git here checks it.
func signature(lines ...string) string {
	return "-----BEGIN PGP SIGNATURE-----\n\n" + strings.Join(lines, "\n") + "\n=Rk9v\n-----END PGP SIGNATURE-----\n"
}

// continued is value as a field of an object's header writes it: each line after the first
// starts with a space. value ends with a line break, which ends the field.
func continued(value string) string {
	return strings.ReplaceAll(strings.TrimSuffix(value, "\n"), "\n", "\n ") + "\n"
}

// stored writes raw into repo's object store as an object of kind, and answers its id: how a
// test makes a tag or a commit field by field, with a signature no key made or a field git does
// not know, which no command that makes a tag or a commit writes.
func stored(t *testing.T, fixture fixturegit.Runner, repo, kind, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), kind)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return fixture.Run(repo, "git", "hash-object", "-t", kind, "-w", path)
}

// heldRepo is a repository whose one commit the destination holds, on origin/main, and the id of
// that commit. Its message names a host, so a push that read it would be refused: what a push of a
// ref to it is refused for is what the push adds of its own.
func heldRepo(t *testing.T) (fixturegit.Runner, string, string) {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	remote, repo := filepath.Join(root, "remote.git"), filepath.Join(root, "repo")
	fixture := fixturegit.Isolated(t, home)
	fixture.Run(root, fixturegit.InitBare(remote, "main")...)
	fixture.Run(root, fixturegit.InitWorkTree(repo, "main")...)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.Run(repo, "git", "add", "-A")
	fixture.Run(repo, "git", "commit", "--quiet", "-m", "moved the run to "+theHost)
	held := fixture.Run(repo, "git", "rev-parse", "HEAD")
	fixture.Run(repo, "git", "remote", "add", "origin", remote)
	fixture.Run(repo, "git", "push", "--quiet", "origin", "main")
	return fixture, repo, held
}

// pushOf is the guard's answer to a push of update from repo to a public destination.
func pushOf(t *testing.T, repo string, update Update) error {
	t.Helper()
	guard := guardWith(t, leakguardtest.Write(t, t.TempDir(), passes), Public)
	return guard.Push(bound(t), PushRequest{
		Dir:     repo,
		Remote:  "origin",
		URL:     "https://github.com/invented-org/invented-repo.git",
		Updates: []Update{update},
	})
}

// A pushed annotated tag is read: its message, its tagger and the name it was made with, which the
// push sends to the destination with the tag. rev-list follows a tag to its commit and reads
// nothing of the tag, and the commit here is one the destination holds, so each of these is the
// whole of what the push adds. A tag that points to a tag is read down to the inner one, which the
// push sends too. A tag's signature is not read as text: its base64 can hold a run a rule refuses
// by chance, and the same run in the message is refused. A lightweight tag is a ref that holds the
// commit, and adds nothing; nor does a tag the destination holds at the ref already.
func TestAPushedAnnotatedTagIsRead(t *testing.T) {
	fixture, repo, held := heldRepo(t)
	// tag makes an annotated tag of target named name, with message, under env, and answers it.
	tag := func(t *testing.T, name, target, message string, env ...string) string {
		t.Helper()
		fixture.With(env...).Run(repo, "git", "tag", "-a", name, "-m", message, target)
		return fixture.Run(repo, "git", "rev-parse", "refs/tags/"+name)
	}
	// madeTag is a tag of the held commit written field by field, with message as its message.
	madeTag := func(t *testing.T, name, message string) string {
		t.Helper()
		sha := stored(t, fixture, repo, "tag", "object "+held+"\ntype commit\ntag "+name+
			"\ntagger T <t@example.invalid> 1700000000 +0000\n\n"+message)
		fixture.Run(repo, "git", "update-ref", "refs/tags/"+name, sha)
		return sha
	}

	for _, tc := range []struct {
		name string
		// build makes the case's tag and answers the update that pushes it and the report, nil
		// where the push passes.
		build func(t *testing.T) (Update, []string)
	}{
		{
			name: "the message names a host",
			build: func(t *testing.T) (Update, []string) {
				sha := tag(t, "message", held, "moved the run to "+theHost)
				return newRef("refs/tags/message", sha), []string{"tag " + short(sha) + " message: " + theClass}
			},
		},
		{
			name: "the tagger names a host",
			build: func(t *testing.T) (Update, []string) {
				sha := tag(t, "tagger", held, "a message of no interest", "GIT_COMMITTER_EMAIL=builder@"+theHost)
				return newRef("refs/tags/tagger", sha), []string{"tag " + short(sha) + " tagger: " + theClass}
			},
		},
		{
			// Pushed to a ref of another name, the tag still carries the one it was made with.
			name: "the name the tag was made with names a host",
			build: func(t *testing.T) (Update, []string) {
				sha := tag(t, theHost, held, "a message of no interest")
				update := newRef("refs/tags/"+theHost, sha)
				update.RemoteRef = "refs/tags/renamed"
				return update, []string{"tag " + short(sha) + " header: " + theClass}
			},
		},
		{
			name: "a tag of a tag whose message names a host",
			build: func(t *testing.T) (Update, []string) {
				inner := tag(t, "inner", held, "moved the run to "+theHost)
				outer := tag(t, "outer", "inner", "a message of no interest")
				if raw := fixture.Run(repo, "git", "cat-file", "tag", outer); !strings.HasPrefix(raw, "object "+inner+"\ntype tag\n") {
					t.Fatalf("the outer tag does not point to the inner one:\n%s", raw)
				}
				return newRef("refs/tags/outer", outer), []string{"tag " + short(inner) + " message: " + theClass}
			},
		},
		{
			name: "a signature's bytes that read as a name",
			build: func(t *testing.T) (Update, []string) {
				sha := madeTag(t, "signed", "a message of no interest\n"+signature(namedBase64))
				return newRef("refs/tags/signed", sha), nil
			},
		},
		{
			name: "the same bytes in the message",
			build: func(t *testing.T) (Update, []string) {
				sha := madeTag(t, "unsigned", "a message of no interest\n"+namedBase64+"\n")
				return newRef("refs/tags/unsigned", sha), []string{"tag " + short(sha) + " message: " + repositoryClass}
			},
		},
		{
			// The destination holds the tag at that ref already, so the push moves nothing.
			name: "a tag the destination holds at that ref",
			build: func(t *testing.T) (Update, []string) {
				sha := tag(t, "held", held, "moved the run to "+theHost)
				return Update{LocalRef: "refs/tags/held", LocalSHA: sha, RemoteRef: "refs/tags/held", RemoteSHA: sha}, nil
			},
		},
		{
			name: "a lightweight tag",
			build: func(t *testing.T) (Update, []string) {
				fixture.Run(repo, "git", "tag", "light", held)
				return newRef("refs/tags/light", held), nil
			},
		},
		{
			name: "an annotated tag that names nothing",
			build: func(t *testing.T) (Update, []string) {
				return newRef("refs/tags/clean", tag(t, "clean", held, "a message of no interest")), nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			update, want := tc.build(t)
			err := pushOf(t, repo, update)
			if want == nil {
				if err != nil {
					t.Fatalf("a push that names nothing: %v", err)
				}
				return
			}
			if refused := refusal(t, err); !slices.Equal(refused.Lines, want) {
				t.Errorf("the report is %q, want %q", refused.Lines, want)
			}
		})
	}
}

// newRef is the update of a push that creates ref at sha.
func newRef(ref, sha string) Update {
	return Update{LocalRef: ref, LocalSHA: sha, RemoteRef: ref, RemoteSHA: noSHA}
}

// A commit's whole header is read, not only its identities and message. A merge of a signed tag
// carries that tag in its header (mergetag), its tagger and message included, and the push sends
// the commit alone: the tag is read there as a pushed tag is. A field git does not know is read
// too, and a commit's encoding: a message stored in another encoding than UTF-8 is read as git
// shows it. A signature's bytes are not read as text, whether a commit's (gpgsig) or a merged
// tag's: the same bytes in a field git does not know are refused, and a line of a signature block
// that is not base64 is read. Each commit here adds no line of a file, so its header and message
// are the whole of what it adds.
func TestACommitsWholeHeaderIsRead(t *testing.T) {
	fixture, repo, held := heldRepo(t)
	tree := fixture.Run(repo, "git", "rev-parse", held+"^{tree}")
	// merged is a merge onto the held commit of a side commit through a signed tag of it, made
	// field by field with tagger and message, as `git merge` of a signed tag writes it.
	merged := func(t *testing.T, name, tagger, message string) string {
		t.Helper()
		fixture.Run(repo, "git", "checkout", "--quiet", "-B", "side-"+name, held)
		if err := os.WriteFile(filepath.Join(repo, name+".md"), []byte("a line of the side branch\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fixture.Run(repo, "git", "add", "-A")
		fixture.Run(repo, "git", "commit", "--quiet", "-m", "a side commit")
		side := fixture.Run(repo, "git", "rev-parse", "HEAD")
		tag := stored(t, fixture, repo, "tag", "object "+side+"\ntype commit\ntag "+name+
			"\ntagger "+tagger+" 1700000000 +0000\n\n"+message)
		fixture.Run(repo, "git", "update-ref", "refs/tags/"+name, tag)
		fixture.Run(repo, "git", "checkout", "--quiet", "-B", "merge-"+name, held)
		fixture.Run(repo, "git", "merge", "--quiet", "--no-ff", "-m", "a merge of no interest", name)
		merge := fixture.Run(repo, "git", "rev-parse", "HEAD")
		if raw := fixture.Run(repo, "git", "cat-file", "commit", merge); !strings.Contains(raw, "\nmergetag object "+side+"\n") {
			t.Fatalf("git merge wrote no mergetag field into %s:\n%s", merge, raw)
		}
		return merge
	}
	// madeOf is a commit of the held commit's tree on top of it, written field by field with fields
	// after its committer and message after them; made is one of a message of no interest.
	madeOf := func(t *testing.T, fields, message string) string {
		t.Helper()
		return stored(t, fixture, repo, "commit", "tree "+tree+"\nparent "+held+
			"\nauthor A <a@example.invalid> 1700000000 +0000\ncommitter C <c@example.invalid> 1700000000 +0000\n"+
			fields+"\n"+message)
	}
	made := func(t *testing.T, fields string) string {
		t.Helper()
		return madeOf(t, fields, "a message of no interest\n")
	}
	const tagger = "T <t@example.invalid>"

	for _, tc := range []struct {
		name string
		// build makes the case's commit and answers it, and the report as a function of it, nil
		// where the push passes.
		build func(t *testing.T) (string, func(head string) []string)
	}{
		{
			name: "a merged tag's message names a host",
			build: func(t *testing.T) (string, func(string) []string) {
				return merged(t, "message", tagger, "moved the run to "+theHost+"\n"+signature("Rk9vYmFy")),
					func(head string) []string {
						return []string{"commit " + short(head) + " mergetag message: " + theClass}
					}
			},
		},
		{
			name: "a merged tag's tagger names a host",
			build: func(t *testing.T) (string, func(string) []string) {
				return merged(t, "tagger", "T <t@"+theHost+">", "a message of no interest\n"+signature("Rk9vYmFy")),
					func(head string) []string {
						return []string{"commit " + short(head) + " mergetag tagger: " + theClass}
					}
			},
		},
		{
			name: "a merged tag's signature bytes that read as a name",
			build: func(t *testing.T) (string, func(string) []string) {
				return merged(t, "signed", tagger, "a message of no interest\n"+signature(namedBase64)), nil
			},
		},
		{
			name: "a field git does not know names a host",
			build: func(t *testing.T) (string, func(string) []string) {
				return made(t, "x-note the run moved to "+theHost+"\n"), func(head string) []string {
					return []string{"commit " + short(head) + " header: " + theClass}
				}
			},
		},
		{
			name: "a commit's signature bytes that read as a name",
			build: func(t *testing.T) (string, func(string) []string) {
				return made(t, "gpgsig "+continued(signature(namedBase64))), nil
			},
		},
		{
			name: "the same bytes in a field git does not know",
			build: func(t *testing.T) (string, func(string) []string) {
				return made(t, "x-note "+namedBase64+"\n"), func(head string) []string {
					return []string{"commit " + short(head) + " header: " + repositoryClass}
				}
			},
		},
		{
			name: "a line of a signature block that is not base64",
			build: func(t *testing.T) (string, func(string) []string) {
				return made(t, "gpgsig "+continued(signature("Comment: the run moved to "+theHost, "Rk9vYmFy"))),
					func(head string) []string {
						return []string{"commit " + short(head) + " header: " + theClass}
					}
			},
		},
		{
			// The no-break space is Latin-1's byte, which is no character of UTF-8: the stored
			// bytes do not name the repository to the rules, and the text git shows does. Built
			// out of two pieces for the reason theHost is.
			name: "a message in another encoding names the repository",
			build: func(t *testing.T) (string, func(string) []string) {
				return madeOf(t, "encoding ISO-8859-1\n", "the run moved to agen"+"t\xa0c today\n"),
					func(head string) []string {
						return []string{"commit " + short(head) + " message in UTF-8: " + repositoryClass}
					}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head, want := tc.build(t)
			err := pushOf(t, repo, Update{LocalRef: "refs/heads/main", LocalSHA: head, RemoteRef: "refs/heads/main", RemoteSHA: held})
			if want == nil {
				if err != nil {
					t.Fatalf("a push that names nothing: %v", err)
				}
				return
			}
			if refused := refusal(t, err); !slices.Equal(refused.Lines, want(head)) {
				t.Errorf("the report is %q, want %q", refused.Lines, want(head))
			}
		})
	}
}

// A tag that could not be read refuses the push, as every read of a push's records that fails
// does: a tag read as clean because git did not answer would pass what it carries. The stand-in
// git fails the one read and runs the real git for every other command.
func TestAFailedTagReadRefusesThePush(t *testing.T) {
	git := fixturegit.CompiledGit(t)
	fixture, repo, held := heldRepo(t)
	fixture.Run(repo, "git", "tag", "-a", "clean", "-m", "a message of no interest", held)
	sha := fixture.Run(repo, "git", "rev-parse", "refs/tags/clean")
	for _, read := range []string{"cat-file -t", "cat-file tag"} {
		t.Run(read, func(t *testing.T) {
			guard := gitFirst(t, "case \" $* \" in\n"+
				"*\" "+read+" \"*) echo 'fatal: the stand-in fails this read' >&2; exit 128 ;;\n"+
				"*) exec '"+git+"' \"$@\" ;;\nesac\n")
			err := guard.Push(bound(t), PushRequest{
				Dir:     repo,
				Remote:  "origin",
				URL:     "https://github.com/invented-org/invented-repo.git",
				Updates: []Update{newRef("refs/tags/clean", sha)},
			})
			says(t, setupFailure(t, err), "the push's commits and tags could not be read: git cat-file exited 128: fatal: the stand-in fails this read")
		})
	}
}
