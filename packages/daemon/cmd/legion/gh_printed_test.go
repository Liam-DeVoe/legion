package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// madeUpGhToken is the made-up token these rows write where a person or a CI checkout could.
const madeUpGhToken = "zqtoken0123"

// ghPrinted is err as legion gh writes it to stderr (printGhRefusal).
func ghPrinted(err error) string {
	var stderr bytes.Buffer
	printGhRefusal(&stderr, err)
	return stderr.String()
}

// A token in a URL a person puts where legion gh reads a repository - GH_REPO, -R, a remote's
// gh-resolved marker - reaches the refusal that names that value, and legion gh writes the refusal
// through the URL rule, as every other command's (printRefusal): the line holds no part of the
// token, and still says what was refused.
func TestLegionGhPrintsNoTokenOfASelectorOrAMarker(t *testing.T) {
	at := "@"
	pane := map[string]string{"GH_CONFIG_DIR": "/owners/acme", "LEGION_GH_OWNERS_DIR": "/owners"}
	with := func(extra map[string]string) map[string]string {
		env := map[string]string{}
		for k, v := range pane {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}
	tokenURL := func(host string) string {
		return "https://x-access-token:" + madeUpGhToken + at + host + "/acme/widgets"
	}
	marker := func() ([]ghRemote, error) {
		return []ghRemote{{name: "origin", url: "https://github.com/acme/widgets.git"},
			ghMarkerLine("remote.origin.gh-resolved " + tokenURL("ghe.example.com"))}, nil
	}
	for name, tc := range map[string]struct {
		argv    []string
		env     map[string]string
		remotes func() ([]ghRemote, error)
		names   string
	}{
		"GH_REPO on another host":    {[]string{"pr", "list"}, with(map[string]string{"GH_REPO": tokenURL("ghe.example.com")}), noGitRepository, "names another host than github.com"},
		"-R on another host":         {[]string{"pr", "list", "-R", tokenURL("ghe.example.com")}, with(nil), noGitRepository, "names another host than github.com"},
		"a gh-resolved marker's URL": {[]string{"pr", "list"}, with(nil), marker, "remote.origin.gh-resolved https://[a URL with credentials]/acme/widgets"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ghTargetOfKept(t, tc.argv, tc.env, savedGhHelp, tc.remotes, ownersOwnDirectories)
			if err == nil {
				t.Fatal("ghTargetOf = nil error, want a refusal")
			}
			printed := ghPrinted(err)
			if strings.Contains(printed, madeUpGhToken) {
				t.Errorf("legion gh printed %q, which holds the token", printed)
			}
			if !strings.HasPrefix(printed, "legion gh: ") || !strings.Contains(printed, tc.names) {
				t.Errorf("legion gh printed %q, want it to name %q", printed, tc.names)
			}
		})
	}
}

// A URL-scoped git config key (`http.<url>.proxy`) is refused by its key, and a token written in
// that URL prints in no part of the line legion gh writes, though the refusal names the key twice
// (the advice to unset it among them). Real git sets the key.
func TestLegionGhPrintsNoTokenOfAURLScopedGitConfigKey(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	repository := t.TempDir()
	t.Chdir(repository)
	env := append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	for _, args := range [][]string{{"init", "-q"}, {"config", "http.https://ghuser:" + madeUpGhToken + "@" + "ghe.example.com/.proxy", "http://proxy.example:3128"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = repository, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	err := ghRepoGitConfigRefusal(env)
	if err == nil {
		t.Fatal("ghRepoGitConfigRefusal = nil, want the key refused")
	}
	printed := ghPrinted(err)
	if strings.Contains(printed, madeUpGhToken) {
		t.Errorf("legion gh printed %q, which holds the token", printed)
	}
	if !strings.Contains(printed, "http.https://[a URL with credentials]/.proxy") {
		t.Errorf("legion gh printed %q, want the key named with its URL as the mark", printed)
	}
}

// A `git remote -v` line legion gh cannot read as gh does - a remote URL holding whitespace, where
// the URL rule ends a URL - is refused by the remote's name alone, the line not printed: a token
// before a space and an "@", a token holding a space, an scp-like remote with a token user and a
// space, and a URL followed by a second one. Real git writes each remote.
func TestReadGitRemotesNamesAnUnreadableLineByItsRemoteAlone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on PATH")
	}
	at := "@"
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for name, remote := range map[string]string{
		"a space before the @":     "https://x-access-token:" + madeUpGhToken + " " + at + "github.com/acme/widgets",
		"a token holding a space":  "https://x-access-token:zq tok0123" + at + "github.com/o/r",
		"an scp token and a space": madeUpGhToken + at + "github.com:o/r x",
		"a second URL after it":    "https://x-access-token:" + madeUpGhToken + at + "github.com/a/b https://github.com/evilown/y",
	} {
		t.Run(name, func(t *testing.T) {
			repository := t.TempDir()
			t.Chdir(repository)
			for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", remote}} {
				cmd := exec.Command("git", args...)
				cmd.Dir = repository
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
			}
			_, err := readGitRemotes()
			if err == nil {
				t.Fatal("readGitRemotes = nil error, want the line refused")
			}
			printed := ghPrinted(err)
			for _, part := range []string{madeUpGhToken, "tok0123", "zq tok"} {
				if strings.Contains(printed, part) {
					t.Errorf("legion gh printed %q, which holds %q", printed, part)
				}
			}
			if !strings.Contains(printed, `for the remote "origin"`) {
				t.Errorf("legion gh printed %q, want the remote named", printed)
			}
		})
	}
}

// An ssh remote's user name alone (`git@host:owner/repo`, `ssh://git@host/…`) is an SSH login,
// not a credential: legion gh prints it with the host, the one the alias-host refusal is about,
// and leaves it so through the URL rule it writes refusals through. It prints the URL as gh reads
// it, which drops a port and reads git+ssh as ssh. A password in an ssh remote is a credential,
// and that remote prints as the mark.
func TestLegionGhPrintsAnSSHLoginsUserAndHost(t *testing.T) {
	at := "@"
	for raw, want := range map[string]string{
		"git" + at + "gh-work:sjawhar/legion.git":                  "ssh://git" + at + "gh-work/sjawhar/legion.git",
		"ssh://git" + at + "github.com/sjawhar/legion.git":         "ssh://git" + at + "github.com/sjawhar/legion.git",
		"ssh://git" + at + "ghe.example.com:2222/acme/widgets":     "ssh://git" + at + "ghe.example.com/acme/widgets",
		"git+ssh://deploy" + at + "github.com/acme/widgets":        "ssh://deploy" + at + "github.com/acme/widgets",
		"ssh://git:zqpass0123" + at + "gh-work/sjawhar/legion.git": "ssh://[a URL with credentials]/sjawhar/legion.git",
	} {
		if got := remoteShown(raw); got != want {
			t.Errorf("remoteShown(%q) = %q, want %q", raw, got, want)
		}
	}
	s := &selectors{}
	err := s.fromWorkingDirectory(func() ([]ghRemote, error) {
		return []ghRemote{{name: "work", url: "git" + at + "gh-work:sjawhar/legion.git"}}, nil
	}, false)
	if err == nil {
		t.Fatal("fromWorkingDirectory = nil, want the alias-host refusal")
	}
	if want := `remote "work" (ssh://git` + at + `gh-work/sjawhar/legion.git)`; !strings.Contains(ghPrinted(err), want) {
		t.Errorf("legion gh printed %q, want it to name %q, the host the refusal is about", ghPrinted(err), want)
	}
}

// printGhRefusal writes every URL through the URL rule but an SSH login: a URL with user info and
// a password, one with a query, and an ssh URL whose user holds a password or whose path runs on
// past a character the rule ends a URL at, each lose their credential; the SSH login stays.
func TestPrintGhRefusalLeavesOnlyAnSSHLogin(t *testing.T) {
	at := "@"
	text := "a https://u:" + madeUpGhToken + at + "h.example/x, b https://h.example/x?t=" + madeUpGhToken +
		", c ssh://u:" + madeUpGhToken + at + "h.example/x, d ssh://git" + at + "h.example/o/r.git and e ssh://git" + at + "h.example/o?t=" + madeUpGhToken
	printed := ghPrinted(errors.New(text))
	if strings.Contains(printed, madeUpGhToken) {
		t.Errorf("printGhRefusal wrote %q, which holds the token", printed)
	}
	if want := "d ssh://git" + at + "h.example/o/r.git and"; !strings.Contains(printed, want) {
		t.Errorf("printGhRefusal wrote %q, want the SSH login %q kept", printed, want)
	}
}
