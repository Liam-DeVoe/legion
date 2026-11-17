package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sjawhar/legion/daemon/internal/ghrepo"
)

// leakGuardKey is `leak_guard`, and knivesRegistriesKey its one member's path, the keys every
// refusal of the block names.
const (
	leakGuardKey        = "leak_guard"
	knivesRegistriesKey = leakGuardKey + ".knives_registries"
)

// LeakGuard is the `leak_guard` block: what the push guard every role's `git push` runs in a pod
// (`legion leak-check`, internal/leakguard) reads besides the shared public leak check and the
// private-name rules. The zero value, no knives registry, while the file configures none. Under
// `runtime: tmux` it reaches no pane: a pane's push runs no leak guard.
type LeakGuard struct {
	// KnivesRegistries is `leak_guard.knives_registries`, in file order: the first row is the
	// knives registry and each row after it an overlay, as knives' own repos.local.toml is one
	// beside its repos.toml. Nil while the file names none, an empty list included.
	KnivesRegistries []KnivesRegistry
}

// KnivesRegistry is one row of `leak_guard.knives_registries`: the file at Path, at Ref, in the
// GitHub repository Repo. Its JSON is the row of leakguard.KnivesRegistriesVar, whose decoder
// refuses a field it does not know, so this type's three fields are the whole of the row.
type KnivesRegistry struct {
	Repo string `json:"repo"`
	Path string `json:"path"`
	Ref  string `json:"ref"`
}

// KnivesRegistriesValue is the rows as leakguard.KnivesRegistriesVar carries them to every role
// process the Kubernetes runtime launches: a JSON list of {"repo", "path", "ref"} rows in file
// order, and "" with no row, where the runtime sets no such variable at all and a push has no
// knives pass.
func (g LeakGuard) KnivesRegistriesValue() string {
	if len(g.KnivesRegistries) == 0 {
		return ""
	}
	value, err := json.Marshal(g.KnivesRegistries)
	if err != nil {
		// A list of structs of three strings always marshals.
		panic("config: marshal leak_guard.knives_registries: " + err.Error())
	}
	return string(value)
}

// readLeakGuard is `leak_guard`: a mapping whose one member, `knives_registries`, is a list of
// mappings, each exactly a `repo` Parse takes, a `path` and a `ref`. A row that is not a mapping, a
// field that is not one of the three, a missing or empty field, a repo Parse refuses, a path
// checkKnivesRegistryPath refuses, and a second row naming a file an earlier row names are each
// refused naming the row. An absent key, a null one, a null or absent member and an empty list are
// each no registry. Two rows naming one path under the two names of a configured rename are
// refused once the renames settle (checkKnivesRegistryRenames).
func readLeakGuard(value *yaml.Node, key string) (LeakGuard, error) {
	if value.Tag == "!!null" {
		return LeakGuard{}, nil
	}
	if value.Kind != yaml.MappingNode {
		return LeakGuard{}, fmt.Errorf("%s must be a mapping", key)
	}
	fields, err := describedMembers(value, key, leakGuardKey)
	if err != nil {
		return LeakGuard{}, err
	}
	list, listKey := fields["knives_registries"], key+".knives_registries"
	if list == nil {
		return LeakGuard{}, nil
	}
	if list.Kind != yaml.SequenceNode {
		return LeakGuard{}, fmt.Errorf("%s must be an array", listKey)
	}
	var block LeakGuard
	for index, entry := range list.Content {
		row, err := readKnivesRegistry(entry, fmt.Sprintf("%s[%d]", listKey, index))
		if err != nil {
			return LeakGuard{}, err
		}
		for earlier, named := range block.KnivesRegistries {
			// GitHub compares a repository's name without case, and a file's path with it.
			if strings.EqualFold(named.Repo, row.Repo) && named.Path == row.Path {
				return LeakGuard{}, fmt.Errorf("%s[%d] and %s[%d] both name %s in %s: each row is one file, named once",
					listKey, earlier, listKey, index, row.Path, row.Repo)
			}
		}
		block.KnivesRegistries = append(block.KnivesRegistries, row)
	}
	return block, nil
}

// readKnivesRegistry is one row of `leak_guard.knives_registries`, field its path in the file.
func readKnivesRegistry(entry *yaml.Node, field string) (KnivesRegistry, error) {
	if entry.Kind != yaml.MappingNode {
		return KnivesRegistry{}, fmt.Errorf("%s must be a mapping", field)
	}
	fields, err := describedMembers(entry, field, knivesRegistriesKey+"[]")
	if err != nil {
		return KnivesRegistry{}, err
	}
	repo, err := requiredString(fields["repo"], field+".repo", ": the GitHub repository the registry file is in")
	if err != nil {
		return KnivesRegistry{}, err
	}
	parsed, err := ghrepo.Parse(field+".repo", repo)
	if err != nil {
		return KnivesRegistry{}, err
	}
	path, err := requiredString(fields["path"], field+".path", ": the registry file's path in that repository")
	if err != nil {
		return KnivesRegistry{}, err
	}
	if err := checkKnivesRegistryPath(path, field+".path"); err != nil {
		return KnivesRegistry{}, err
	}
	ref, err := requiredString(fields["ref"], field+".ref", ": the branch or tag the file is read at")
	if err != nil {
		return KnivesRegistry{}, err
	}
	return KnivesRegistry{Repo: parsed.String(), Path: path, Ref: ref}, nil
}

// checkKnivesRegistryPath refuses a registry file's path that names no file from the repository's
// root: one that begins with `/`, and one with a `..` component. Every other character is the
// guard's to escape where it writes the path into its read (leakguard's knivesRegistry).
func checkKnivesRegistryPath(path, key string) error {
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("%s %q begins with /: name the file from the repository's root, with no leading slash", key, path)
	}
	for component := range strings.SplitSeq(path, "/") {
		if component == ".." {
			return fmt.Errorf("%s %q has a .. component: name the file itself", key, path)
		}
	}
	return nil
}

// checkKnivesRegistryRenames refuses two `leak_guard.knives_registries` rows naming one path in two
// repositories a configured rename makes one repository. readLeakGuard already refuses one file
// named twice (the repository compared without case), but it reads the block before the renames
// settle and knows nothing of them, so the pair is caught here, as checkCommitIssueTrailerRenames
// catches its own. GitHub serves a moved repository under its old name as well, so the guard
// would read the one file twice, and the second read adds the terms of the first to the same
// entries again: a term listed twice, which the guard refuses at every push it scans.
func checkKnivesRegistryRenames(cfg *Config) error {
	if cfg.RepositoryRenames.IsZero() {
		return nil
	}
	rows := cfg.LeakGuard.KnivesRegistries
	for index, row := range rows {
		for earlier, named := range rows[:index] {
			if named.Path != row.Path || !cfg.RepositoryRenames.Same(named.Repo, row.Repo) {
				continue
			}
			return fmt.Errorf("%s[%d] (%s) and %s[%d] (%s) both name %s in one repository: %s makes %s and %s one repository; each row is one file, named once",
				knivesRegistriesKey, earlier, named.Repo, knivesRegistriesKey, index, row.Repo, row.Path, repositoryRenamesKey, named.Repo, row.Repo)
		}
	}
	return nil
}
