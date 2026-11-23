package config

import (
	"reflect"
	"slices"
	"testing"

	"github.com/sjawhar/legion/daemon/internal/plan"
)

// `models` lists the models a role may name, in the file's order, and the file that names none
// lists none. Each value the key cannot take is warned about, naming the key and the entry at
// fault and that the next release refuses it, and the file loads: an entry that cannot be a
// model's name is left out of the listed models, a model named twice is listed once, and a value
// that is not a list lists none. A file whose `models` is whole raises no warning.
func TestTheModelsKeyIsReadAndWarned(t *testing.T) {
	const refuses = ", and the next release refuses it; "
	for _, tc := range []struct {
		name, key string
		want      []string
		warnings  []string
	}{
		{name: "two models", key: "models: [m-one, m-two]\n", want: []string{"m-one", "m-two"}},
		{name: "no key"},
		{name: "a scalar", key: "models: m-one\n",
			warnings: []string{"models must be a list of model names, e.g. [anthropic/claude-sonnet-4]: this release lists no model from it" + refuses + "write models as a list in legion.yaml"}},
		{name: "a number", key: "models: [7]\n", want: []string{},
			warnings: []string{"models[0] must be a model's name: this release leaves it out of the listed models" + refuses + "remove it from legion.yaml"}},
		{name: "a mapping entry", key: "models: [m-one, {name: m-two}]\n", want: []string{"m-one"},
			warnings: []string{"models[1] must be a model's name: this release leaves it out of the listed models" + refuses + "remove it from legion.yaml"}},
		{name: "an empty entry", key: "models: [\"\", m-one]\n", want: []string{"m-one"},
			warnings: []string{"models[0] is empty: this release leaves it out of the listed models" + refuses + "remove it from legion.yaml"}},
		{name: "white space", key: "models: [\"a b\"]\n", want: []string{},
			warnings: []string{`models[0] is "a b", which holds white space, and a model's name is one word: this release leaves it out of the listed models` + refuses + "remove it from legion.yaml"}},
		{name: "a model named twice", key: "models: [m, n, m]\n", want: []string{"m", "n"},
			warnings: []string{"models[2] names m, which models[0] names already: this release lists it once" + refuses + "remove it from legion.yaml"}},
		{name: "two faults", key: "models: [m, \"\", m]\n", want: []string{"m"}, warnings: []string{
			"models[1] is empty: this release leaves it out of the listed models" + refuses + "remove it from legion.yaml",
			"models[2] names m, which models[0] names already: this release lists it once" + refuses + "remove it from legion.yaml",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for name, load := range map[string]func(string, func(string) string) (Config, error){"Load": Load, "LoadForValidation": LoadForValidation} {
				cfg, err := load(writeConfigFile(t, tc.key+minimalFile), noEnv)
				if err != nil {
					t.Fatalf("%s with %q: %v, want the file loaded", name, tc.key, err)
				}
				if !slices.Equal(cfg.Models, tc.want) || (tc.want == nil) != (cfg.Models == nil) {
					t.Errorf("%s with %q: Models = %#v, want %#v", name, tc.key, cfg.Models, tc.want)
				}
				if !slices.Equal(cfg.Warnings, tc.warnings) {
					t.Errorf("%s with %q: Warnings = %#v, want %#v", name, tc.key, cfg.Warnings, tc.warnings)
				}
			}
		})
	}
}

// The deployment a plan is judged against lists its models, `models` then each model a role of
// `roles` names that `models` lacks, so a deployment that set a role's model before `models`
// existed lists that model; it gives each role the harness `worker_backends` names, Oh My Pi where
// it names none, and each role of `roles` its model; and the owner's role is the architect's, on
// the default model and the architect's harness. The names no role of the deployment takes are the
// one list a role of a plan cannot take either (plan.LegionNames), and `owner` is in neither.
func TestTheDeploymentListsItsModelsAndHarnesses(t *testing.T) {
	const checker = "roles: {checker: {type: testing, prompt_file: checker.md, model: m-two}}\n"
	cfg, err := Load(rolesFile(t, "models: [m-one]\n"+checker+"worker_backends: {implementer: claude}\n", "checker.md"), noEnv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.ListedModels(), []string{"m-one", "m-two"}; !slices.Equal(got, want) {
		t.Errorf("ListedModels() = %v, want %v", got, want)
	}
	deployment := cfg.Deployment()
	if got, want := deployment.Models, []string{"m-one", "m-two"}; !slices.Equal(got, want) {
		t.Errorf("Deployment().Models = %v, want %v", got, want)
	}
	if got := deployment.Roles["implementer"].Backend; got != "claude" {
		t.Errorf("Deployment().Roles[implementer].Backend = %q, want claude", got)
	}
	if got, want := deployment.Roles["checker"], (plan.RoleDef{Name: "checker", Type: plan.TypeTesting, Model: "m-two", Backend: "omp"}); got != want {
		t.Errorf("Deployment().Roles[checker] = %+v, want %+v", got, want)
	}
	if got, want := deployment.Roles["reviewer"], (plan.RoleDef{Name: "reviewer", Type: plan.TypeReview, Backend: "omp"}); got != want {
		t.Errorf("Deployment().Roles[reviewer] = %+v, want %+v", got, want)
	}

	cfg, err = Load(rolesFile(t, "models: [m-two]\n"+checker, "checker.md"), noEnv)
	if err != nil {
		t.Fatalf("Load with the role's model listed: %v", err)
	}
	if got, want := cfg.ListedModels(), []string{"m-two"}; !slices.Equal(got, want) {
		t.Errorf("ListedModels() of a file that lists the role's model = %v, want %v", got, want)
	}

	for _, name := range plan.LegionNames() {
		_, err := Load(rolesFile(t, "roles: {"+name+": {type: testing, prompt_file: checker.md}}\n", "checker.md"), noEnv)
		if want := "roles." + name + " names a role of Legion's own: a deployment defines roles of its own names"; err == nil || err.Error() != want {
			t.Errorf("Load of roles.%s = %v, want %q", name, err, want)
		}
		p := plan.Plan{Tasks: []plan.Task{{ID: "a", Steps: []plan.Step{{Role: "implementer"}}}}, Roles: []plan.PlanRole{{Name: name, Type: plan.TypeTesting}}}
		want := []plan.Refusal{{Reason: "the plan defines the role " + name + ", which the deployment or Legion defines already; a role of a plan takes a name of its own."}}
		if got := plan.Validate(p, deployment); !reflect.DeepEqual(got, want) {
			t.Errorf("Validate of a plan role %s = %+v, want %+v", name, got, want)
		}
	}
	cfg, err = Load(rolesFile(t, "roles: {owner: {type: testing, prompt_file: owner.md}}\n", "owner.md"), noEnv)
	if err != nil {
		t.Fatalf("Load of roles.owner: %v", err)
	}
	if _, ok := cfg.Deployment().Roles["owner"]; !ok {
		t.Error("Deployment() of roles.owner holds no role owner")
	}
}
