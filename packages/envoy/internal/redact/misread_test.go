package redact

import (
	"fmt"
	"strings"
	"testing"
)

// A setting's URL that net/url misreads is refused naming the cause the check found, one cause per
// row, by Refusal, which names the value by its length and holds no part of it. A value with no @
// has no user part and nothing is found in it; neither is anything in a user part net/url reads
// whole. The user, the passwords and the token are made for this test.
func TestEachMisreadingIsNamedByItsCause(t *testing.T) {
	const (
		user     = "made-up-user-4e1a"
		password = "not-a-secret-9c2b"
		token    = "not-a-secret-tok5d7"
	)
	for _, tc := range []struct {
		name, raw string
		want      Misreading
	}{
		{"no @, a port that does not parse", "http://envoy.example:42x22", ""},
		{"no @, a line ending", "http://envoy.example:9020\n", ""},
		{"a user part net/url reads whole", "http://" + user + ":" + password + "@envoy.example:9020", ""},
		{"a line ending after a user part", "http://" + user + ":" + password + "@envoy.example:9020\n", ControlCharacter},
		{"a tab in the password", "http://" + user + ":" + password + "\t@envoy.example:9020", ControlCharacter},
		{"a / in the password, whose rest reads as a port", "http://" + user + ":ab/" + password + "@envoy.example:9020", Unparsed},
		{"a # in a token that is the user part", "http://" + token + "#" + password + "@envoy.example:9020", AtOutsideUserPart},
		{"a ? in a token that is the user part", "http://" + token + "?" + password + "@envoy.example:9020", AtOutsideUserPart},
		{"an @ in the query and no user part", "http://envoy.example:9020/?via=a@b", AtOutsideUserPart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Misread(tc.raw)
			if got != tc.want {
				t.Fatalf("Misread = %q, want %q", got, tc.want)
			}
			if got == "" {
				return
			}
			err := Refusal("ENVOY_URL", tc.raw, got)
			want := fmt.Sprintf("ENVOY_URL, a value of %d characters, %s", len(tc.raw), tc.want)
			if err.Error() != want {
				t.Errorf("Refusal = %q, want %q", err, want)
			}
			for _, part := range []string{user, password, token} {
				if strings.Contains(err.Error(), part) {
					t.Errorf("the refusal holds a part of the value: %q", err)
				}
			}
		})
	}
}
