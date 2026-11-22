package main

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/ghconfig"
	"github.com/sjawhar/legion/daemon/internal/podsafety"
)

// ghStackData is a data directory holding gh's extension entry gh-stack as a link to target, the
// way the worker shim links the image's copy (podsafety.EnsureGhExtensions).
func ghStackData(t *testing.T, target string) string {
	t.Helper()
	data := t.TempDir()
	extensions := filepath.Join(data, "gh", "extensions")
	if err := os.MkdirAll(extensions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(extensions, "gh-stack")); err != nil {
		t.Fatal(err)
	}
	return data
}

// remotesAre is a working directory whose git remotes are lines, each a fetch and a push line of
// one remote named and URL'd as given, then markers, each a `git config --get-regexp` line of a
// gh-resolved setting.
func remotesAre(urls map[string]string, markers ...string) func() ([]ghRemote, error) {
	return func() ([]ghRemote, error) {
		var remotes []ghRemote
		for _, name := range slices.Sorted(maps.Keys(urls)) {
			remotes = append(remotes, ghRemote{name: name, url: urls[name]}, ghRemote{name: name, url: urls[name], push: true})
		}
		for _, marker := range markers {
			remotes = append(remotes, ghMarkerLine(marker))
		}
		return remotes, nil
	}
}

// `gh stack` runs the image's gh-stack extension, which acts on the one repository go-gh's
// repository.Current answers: GH_REPO where set, else one of the working directory's github.com
// remotes or a gh-resolved marker's repository. So it runs with the directory of that
// repository's owner, whatever its arguments say (a pull request URL is read for its number, and
// --remote picks a git remote whose credential git asks for by its own path), and is refused
// where the working directory names repositories of two owners, as any command naming two is.
func TestGhStackRunsWithTheDirectoryOfTheWorkingDirectorysOwner(t *testing.T) {
	image := filepath.Join(podsafety.GhExtensionsDir, "gh-stack")
	linked := ghStackData(t, image)
	acme := remotesAre(map[string]string{"origin": "https://github.com/acme/widgets.git"})
	for _, tc := range []struct {
		name    string
		argv    []string
		env     map[string]string
		remotes func() ([]ghRemote, error)
		dir     string
		refuse  string
	}{
		{name: "a view", argv: []string{"stack", "view", "--json"}, remotes: acme, dir: ghconfig.Dir("/owners", "acme")},
		{name: "a submit to another remote", argv: []string{"stack", "submit", "--auto", "--remote", "fork"}, remotes: acme, dir: ghconfig.Dir("/owners", "acme")},
		{name: "a link naming another repository's pull request URL", argv: []string{"stack", "link", "https://github.com/other/thing/pull/1", "2"}, remotes: acme, dir: ghconfig.Dir("/owners", "acme")},
		{name: "a merge", argv: []string{"stack", "merge", "42", "--yes", "--squash"}, remotes: acme, dir: ghconfig.Dir("/owners", "acme")},
		{name: "the extension's help", argv: []string{"stack", "--help"}, remotes: acme, dir: ghconfig.Dir("/owners", "acme")},
		{name: "GH_REPO over the remotes", argv: []string{"stack", "view", "--json"}, env: map[string]string{"GH_REPO": "sjawhar/legion"}, remotes: acme, dir: ghconfig.Dir("/owners", "sjawhar")},
		{name: "a working directory that names no repository", argv: []string{"stack", "init", "feature"}, remotes: noGitRepository, dir: ""},
		{name: "the data directory under HOME", argv: []string{"stack", "view", "--json"}, env: map[string]string{"XDG_DATA_HOME": "", "HOME": "home"}, remotes: acme, dir: ghconfig.Dir("/owners", "acme")},
		{name: "two owners' remotes", argv: []string{"stack", "view", "--json"},
			remotes: remotesAre(map[string]string{"origin": "https://github.com/acme/widgets.git", "upstream": "https://github.com/sjawhar/legion.git"}),
			refuse:  "more than one owner"},
		{name: "a marker naming another owner's repository", argv: []string{"stack", "view", "--json"},
			remotes: remotesAre(map[string]string{"origin": "https://github.com/acme/widgets.git"}, "remote.origin.gh-resolved sjawhar/legion"),
			refuse:  "more than one owner"},
		{name: "GH_REPO on another host", argv: []string{"stack", "view", "--json"}, env: map[string]string{"GH_REPO": "gitlab.com/acme/widgets"}, remotes: acme, refuse: "another host than github.com"},
		{name: "an ssh remote on another host", argv: []string{"stack", "view", "--json"},
			remotes: remotesAre(map[string]string{"origin": "git@forge.internal.example:acme/widgets.git"}), refuse: "ssh remote"},
		{name: "no extension linked", argv: []string{"stack", "view", "--json"}, env: map[string]string{"XDG_DATA_HOME": t.TempDir()}, remotes: acme, refuse: "not the image's link"},
		{name: "another program installed as gh-stack", argv: []string{"stack", "view", "--json"}, env: map[string]string{"XDG_DATA_HOME": ghStackData(t, filepath.Join(t.TempDir(), "gh-stack"))}, remotes: acme, refuse: "not the image's link"},
		{name: "an option before stack", argv: []string{"-R", "acme/widgets", "stack", "view"}, remotes: acme, refuse: "comes before `stack`"},
		{name: "a caller's token", argv: []string{"stack", "view", "--json"}, env: map[string]string{"GH_TOKEN": "t"}, remotes: acme, refuse: "GH_TOKEN is set"},
		{name: "any other extension", argv: []string{"poi", "list"}, remotes: acme, refuse: "is no command gh's own help lists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"XDG_DATA_HOME": linked}
			for name, value := range tc.env {
				env[name] = value
			}
			if env["HOME"] == "home" {
				env["HOME"] = filepath.Join(t.TempDir(), "home")
				data := filepath.Join(env["HOME"], ".local", "share", "gh", "extensions")
				if err := os.MkdirAll(data, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(image, filepath.Join(data, "gh-stack")); err != nil {
					t.Fatal(err)
				}
			}
			target, err := ghTargetOf(tc.argv, env, savedGhHelp, tc.remotes, ownersOwnDirectories)
			switch {
			case tc.refuse != "":
				if err == nil || !strings.Contains(err.Error(), tc.refuse) || !strings.Contains(err.Error(), "sends no token") {
					t.Errorf("ghTargetOf(%q) = %+v, %v; want a refusal containing %q", tc.argv, target, err, tc.refuse)
				}
			case err != nil || target.dir != tc.dir || target.files == nil:
				t.Errorf("ghTargetOf(%q) = %+v, %v; want the directory %q", tc.argv, target, err, tc.dir)
			}
		})
	}
}

// gh-stack's update check reads github/gh-stack's latest release with the call's token on every
// command but help, so every gh `legion gh` runs has it off, whatever the caller set.
func TestTheGhLegionGhRunsChecksForNoGhStackUpdate(t *testing.T) {
	got := ghChildEnviron([]string{"GH_STACK_NO_UPDATE_NOTIFIER=", "PATH=/usr/bin"}, nil)
	if !slices.Contains(got, "GH_STACK_NO_UPDATE_NOTIFIER=1") || slices.Contains(got, "GH_STACK_NO_UPDATE_NOTIFIER=") {
		t.Errorf("ghChildEnviron = %q, want GH_STACK_NO_UPDATE_NOTIFIER=1 and not the caller's empty value", got)
	}
}
