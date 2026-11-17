package config

import (
	"slices"
	"strings"
	"testing"
)

// `models` lists the models a role may name, in the file's order, and the file that names none
// lists none; each value the key cannot take is refused naming the key and the entry at fault.
func TestTheModelsKeyIsReadAndRefused(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		want      []string
		refusal   string
	}{
		{name: "two models", key: "models: [m-one, m-two]\n", want: []string{"m-one", "m-two"}},
		{name: "no key"},
		{name: "a scalar", key: "models: m-one\n", refusal: "models must be a list of model names, e.g. [anthropic/claude-sonnet-4]"},
		{name: "a number", key: "models: [7]\n", refusal: "models[0] must be a model's name"},
		{name: "a mapping entry", key: "models: [m-one, {name: m-two}]\n", refusal: "models[1] must be a model's name"},
		{name: "an empty entry", key: "models: [\"\"]\n", refusal: "models[0] is empty"},
		{name: "white space", key: "models: [\"a b\"]\n", refusal: "models[0] is a b, which holds white space: a model's name is one word"},
		{name: "a model named twice", key: "models: [m, m]\n", refusal: "models names m twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadForValidation(writeConfigFile(t, tc.key+kubernetesFile), noEnv)
			if tc.refusal != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refusal) {
					t.Fatalf("LoadForValidation with %q = %v, want a refusal naming %q", tc.key, err, tc.refusal)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadForValidation with %q: %v", tc.key, err)
			}
			if !slices.Equal(cfg.Models, tc.want) || (tc.want == nil) != (cfg.Models == nil) {
				t.Fatalf("Models = %#v, want %#v", cfg.Models, tc.want)
			}
		})
	}
}
