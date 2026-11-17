package config

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// readModels is `models`: the models a role may name, in the file's order. A value that is not a
// list, an entry that is not a string, an empty entry, an entry holding white space and a model
// named twice are each refused naming the key and the entry: a model's name is the one word a
// launch passes to its harness, so a list that names one twice, or a name that is two words, is
// an operator's mistake rather than a model. A file that names no models lists none.
func readModels(value *yaml.Node, key string) ([]string, error) {
	if value.Tag == "!!null" {
		return nil, nil
	}
	if value.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%s must be a list of model names, e.g. [anthropic/claude-sonnet-4]", key)
	}
	models := make([]string, 0, len(value.Content))
	for index, entry := range value.Content {
		var model string
		if entry.Kind != yaml.ScalarNode || entry.Tag != "!!str" || entry.Decode(&model) != nil {
			return nil, fmt.Errorf("%s[%d] must be a model's name", key, index)
		}
		switch {
		case model == "":
			return nil, fmt.Errorf("%s[%d] is empty", key, index)
		case strings.ContainsFunc(model, unicode.IsSpace):
			return nil, fmt.Errorf("%s[%d] is %s, which holds white space: a model's name is one word", key, index, model)
		case slices.Contains(models, model):
			return nil, fmt.Errorf("%s names %s twice", key, model)
		}
		models = append(models, model)
	}
	return models, nil
}
