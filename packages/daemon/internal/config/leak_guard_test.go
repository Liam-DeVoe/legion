package config

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// leak_guard.knives_registries settles in file order, the first row the registry and the second
// its overlay, and its value for the guard is the JSON list of the three fields of each row in that
// order: a decoder that refuses a field it does not know, as the guard's does
// (leakguard.knivesRows), reads every row back.
func TestLeakGuardKnivesRegistriesSettleInFileOrder(t *testing.T) {
	cfg, err := Load(writeConfigFile(t, minimalFile+"leak_guard:\n"+
		"  knives_registries:\n"+
		"    - {repo: sjawhar/dotfiles, path: knives/repos.toml, ref: main}\n"+
		"    - repo: acme/private-registry\n      path: knives/repos.local.toml\n      ref: v4.0.1\n"), noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []KnivesRegistry{
		{Repo: "sjawhar/dotfiles", Path: "knives/repos.toml", Ref: "main"},
		{Repo: "acme/private-registry", Path: "knives/repos.local.toml", Ref: "v4.0.1"},
	}
	if !slices.Equal(cfg.LeakGuard.KnivesRegistries, want) {
		t.Fatalf("LeakGuard.KnivesRegistries = %v, want %v in file order", cfg.LeakGuard.KnivesRegistries, want)
	}
	value := cfg.LeakGuard.KnivesRegistriesValue()
	const wantValue = `[{"repo":"sjawhar/dotfiles","path":"knives/repos.toml","ref":"main"},` +
		`{"repo":"acme/private-registry","path":"knives/repos.local.toml","ref":"v4.0.1"}]`
	if value != wantValue {
		t.Errorf("KnivesRegistriesValue() = %s, want %s", value, wantValue)
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	var rows []struct{ Repo, Path, Ref string }
	if err := decoder.Decode(&rows); err != nil {
		t.Fatalf("a decoder that refuses unknown fields reads %s: %v", value, err)
	}
	if len(rows) != len(want) {
		t.Fatalf("the value decodes to %d rows, want %d", len(rows), len(want))
	}
	for i, row := range rows {
		if row.Repo != want[i].Repo || row.Path != want[i].Path || row.Ref != want[i].Ref {
			t.Errorf("row %d decodes to %+v, want %+v", i, row, want[i])
		}
	}
}

// A file that names no leak_guard, names it with no value or an empty mapping, or gives
// knives_registries no value or an empty list configures no registry: the zero value, whose value
// for the guard is "", so no runtime sets the variable.
func TestLeakGuardIsTheZeroValueWithoutRows(t *testing.T) {
	for _, body := range []string{
		"",
		"leak_guard:\n",
		"leak_guard: {}\n",
		"leak_guard: {knives_registries: }\n",
		"leak_guard: {knives_registries: []}\n",
	} {
		cfg, err := Load(writeConfigFile(t, minimalFile+body), noEnv)
		if err != nil {
			t.Fatalf("Load(%q): %v", body, err)
		}
		if cfg.LeakGuard.KnivesRegistries != nil {
			t.Errorf("Load(%q) LeakGuard.KnivesRegistries = %v, want nil", body, cfg.LeakGuard.KnivesRegistries)
		}
		if value := cfg.LeakGuard.KnivesRegistriesValue(); value != "" {
			t.Errorf("Load(%q) KnivesRegistriesValue() = %q, want empty", body, value)
		}
	}
}

// Every refusal of leak_guard names the key, the row and the value: a block that is no mapping, a
// member it does not know, a list that is no list, a row that is no mapping, a field it does not
// know or names twice, a missing or empty field, a repo Parse refuses, a path from the root or with
// a .. component, a second row naming a file an earlier row names, case aside in the repository,
// and one naming it under the other name of a configured repository_renames pair, which only the
// settled renames tell (checkKnivesRegistryRenames).
func TestLeakGuardRefusals(t *testing.T) {
	const key = "leak_guard.knives_registries"
	for _, tc := range []struct{ name, body, want string }{
		{"not a mapping", "leak_guard: [a/b]\n",
			"leak_guard must be a mapping"},
		{"an unknown member", "leak_guard: {knives: []}\n",
			"unknown key leak_guard.knives"},
		{"not a list", "leak_guard: {knives_registries: sjawhar/dotfiles}\n",
			key + " must be an array"},
		{"not a list of mappings", "leak_guard: {knives_registries: [sjawhar/dotfiles]}\n",
			key + "[0] must be a mapping"},
		{"an unknown field", "leak_guard: {knives_registries: [{repo: a/b, path: r.toml, ref: main, private: true}]}\n",
			"unknown key " + key + "[0].private"},
		{"a field twice", "leak_guard: {knives_registries: [{repo: a/b, repo: c/d, path: r.toml, ref: main}]}\n",
			key + "[0] names repo twice"},
		{"no repo", "leak_guard: {knives_registries: [{path: r.toml, ref: main}]}\n",
			key + "[0].repo is required: the GitHub repository the registry file is in"},
		{"an empty repo", "leak_guard: {knives_registries: [{repo: '', path: r.toml, ref: main}]}\n",
			key + "[0].repo must not be empty"},
		{"a repo Parse refuses", "leak_guard: {knives_registries: [{repo: dotfiles, path: r.toml, ref: main}]}\n",
			key + `[0].repo must be "owner/name" (got "dotfiles")`},
		{"no path", "leak_guard: {knives_registries: [{repo: a/b, ref: main}]}\n",
			key + "[0].path is required: the registry file's path in that repository"},
		{"an empty path", "leak_guard: {knives_registries: [{repo: a/b, path: '', ref: main}]}\n",
			key + "[0].path must not be empty"},
		{"a path from the root", "leak_guard: {knives_registries: [{repo: a/b, path: /knives/repos.toml, ref: main}]}\n",
			key + `[0].path "/knives/repos.toml" begins with /: name the file from the repository's root, with no leading slash`},
		{"a path with a .. component", "leak_guard: {knives_registries: [{repo: a/b, path: knives/../repos.toml, ref: main}]}\n",
			key + `[0].path "knives/../repos.toml" has a .. component: name the file itself`},
		{"a path that is ..", "leak_guard: {knives_registries: [{repo: a/b, path: .., ref: main}]}\n",
			key + `[0].path ".." has a .. component: name the file itself`},
		{"no ref", "leak_guard: {knives_registries: [{repo: a/b, path: r.toml}]}\n",
			key + "[0].ref is required: the branch or tag the file is read at"},
		{"an empty ref", "leak_guard: {knives_registries: [{repo: a/b, path: r.toml, ref: ''}]}\n",
			key + "[0].ref must not be empty"},
		{"one file twice", "leak_guard: {knives_registries: [{repo: a/b, path: r.toml, ref: main}, {repo: A/B, path: r.toml, ref: v2}]}\n",
			key + "[0] and " + key + "[1] both name r.toml in A/B: each row is one file, named once"},
		{"one file under the two names of a configured rename",
			"leak_guard: {knives_registries: [{repo: acme/widgets, path: r.toml, ref: main}, {repo: Acme/Gadgets, path: r.toml, ref: main}]}\n" +
				"repository_renames: [{from: acme/widgets, to: acme/gadgets}]\n",
			key + "[0] (acme/widgets) and " + key + "[1] (Acme/Gadgets) both name r.toml in one repository: " +
				"repository_renames makes acme/widgets and Acme/Gadgets one repository; each row is one file, named once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfigFile(t, minimalFile+tc.body), noEnv)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("Load error = %v, want %q", err, tc.want)
			}
		})
	}
	// Two rows of one repository naming two files, or one path in two repositories, are two files,
	// with a configured rename too: two paths under its two names, and one path in a repository it
	// does not name.
	for _, body := range []string{
		"leak_guard: {knives_registries: [{repo: a/b, path: r.toml, ref: main}, {repo: a/b, path: R.toml, ref: main}]}\n",
		"leak_guard: {knives_registries: [{repo: a/b, path: r.toml, ref: main}, {repo: a/c, path: r.toml, ref: main}]}\n",
		"leak_guard: {knives_registries: [{repo: acme/widgets, path: r.toml, ref: main}, {repo: acme/gadgets, path: R.toml, ref: main}, {repo: acme/other, path: r.toml, ref: main}]}\n" +
			"repository_renames: [{from: acme/widgets, to: acme/gadgets}]\n",
	} {
		if _, err := Load(writeConfigFile(t, minimalFile+body), noEnv); err != nil {
			t.Errorf("Load(%q): %v, want two files accepted", body, err)
		}
	}
}
