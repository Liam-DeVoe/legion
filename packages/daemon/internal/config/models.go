package config

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// modelsRefusedNext ends each warning readModels raises: this release accepts the value and the
// next release refuses it, as the warning's first words say it will.
const modelsRefusedNext = ", and the next release refuses it; "

// readModels is `models`: the models a role may name, in the file's order, and the warnings the
// key raises (Config.Warnings). A model's name is the one word a launch passes to its harness, so
// a value that is not a list, an entry that is not a string, an empty entry, an entry holding
// white space and a model named twice are each an operator's mistake rather than a model. This
// release takes each of them with one warning that names the key and the entry and says the next
// release refuses it: a value that is not a list lists no model, an entry that cannot be a model's
// name is left out of the listed models, and a model named twice is listed once. A file that names
// no models lists none.
func readModels(value *yaml.Node, key string) ([]string, []string) {
	if value.Tag == "!!null" {
		return nil, nil
	}
	if value.Kind != yaml.SequenceNode {
		return nil, []string{fmt.Sprintf("%s must be a list of model names, e.g. [anthropic/claude-sonnet-4]: this release lists no model from it%swrite %s as a list in legion.yaml",
			key, modelsRefusedNext, key)}
	}
	models := make([]string, 0, len(value.Content))
	var warnings []string
	for index, entry := range value.Content {
		var model, fault string
		switch {
		case entry.Kind != yaml.ScalarNode || entry.Tag != "!!str" || entry.Decode(&model) != nil:
			fault = fmt.Sprintf("%s[%d] must be a model's name: this release leaves it out of the listed models", key, index)
		case model == "":
			fault = fmt.Sprintf("%s[%d] is empty: this release leaves it out of the listed models", key, index)
		case strings.ContainsFunc(model, unicode.IsSpace):
			fault = fmt.Sprintf("%s[%d] is %q, which holds white space, and a model's name is one word: this release leaves it out of the listed models", key, index, model)
		case slices.Contains(models, model):
			first := slices.IndexFunc(value.Content, func(other *yaml.Node) bool { return other.Tag == "!!str" && other.Value == model })
			fault = fmt.Sprintf("%s[%d] names %s, which %s[%d] names already: this release lists it once", key, index, model, key, first)
		}
		if fault != "" {
			warnings = append(warnings, fault+modelsRefusedNext+"remove it from legion.yaml")
			continue
		}
		models = append(models, model)
	}
	return models, warnings
}

// ListedModels are the models a role of an owner's plan may name: `models`, then the model of
// each role of `roles` that `models` does not name yet, in the file's order. A deployment that set
// a role's model before it listed models so lists that model, and a plan that names it is taken.
// nil for a file that lists none and whose roles name none.
func (c Config) ListedModels() []string {
	listed := slices.Clone(c.Models)
	for _, role := range c.Roles {
		if role.Model != "" && !slices.Contains(listed, role.Model) {
			listed = append(listed, role.Model)
		}
	}
	return listed
}
