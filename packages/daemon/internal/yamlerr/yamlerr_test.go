package yamlerr

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// Each of yaml.v3's error kinds that quotes the document, with a made-up credential where a
// person can paste one, becomes the fixed refusal naming the file and, where yaml.v3 names one,
// the line: a scalar where a mapping goes (a TypeError quotes the scalar's head), an unknown
// anchor (its error names the anchor), and a syntax error. The control is yaml.v3's own text,
// which holds the credential for the first two.
func TestRefusalQuotesNothingOfTheDocument(t *testing.T) {
	const credential = "madeUpYamlCredential0123"
	for _, tc := range []struct {
		name, document, want string
		into                 any
	}{
		{"a scalar where a mapping goes", "github.com: " + credential + "\n",
			"hosts.yml cannot be read at line 1: it does not parse as YAML, or holds a value of another kind there than the file takes",
			&struct {
				GitHub struct{ Token string } `yaml:"github.com"`
			}{}},
		{"an unknown anchor", "token: *" + credential + "\n",
			"hosts.yml cannot be read: it does not parse as YAML", &yaml.Node{}},
		{"a syntax error", "token: [" + credential + "\n",
			"hosts.yml cannot be read at line 1: it does not parse as YAML, or holds a value of another kind there than the file takes", &yaml.Node{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := yaml.Unmarshal([]byte(tc.document), tc.into)
			if err == nil {
				t.Fatalf("yaml.Unmarshal(%q) = nil, want an error", tc.document)
			}
			if got := Refusal("hosts.yml", err).Error(); got != tc.want {
				t.Fatalf("Refusal = %q, want %q (yaml.v3's own text: %q)", got, tc.want, err)
			}
		})
	}
}
