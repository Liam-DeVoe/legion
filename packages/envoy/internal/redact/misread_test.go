package redact

import (
	"context"
	"errors"
	"fmt"
	"net/url"
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

// A request's error reads as net/http wrote it when URL prints the configured base whole: the
// request's URL is then the base and the path and query the client wrote, and a deadline says
// "deadline". A base with a user part, or one URL will not print, is named by Error's class alone.
// The user and the password are made for this test.
func TestRequestErrorKeepsAnHonestURLsError(t *testing.T) {
	const (
		user     = "made-up-user-6d2f"
		password = "not-a-secret-0a4e"
	)
	failed := func(base string) error {
		return &url.Error{Op: "Get", URL: base + "/repos/acme/widgets?per_page=1", Err: context.DeadlineExceeded}
	}
	honest := failed("http://127.0.0.1:1")
	if got := RequestError(honest, "http://127.0.0.1:1"); got != honest {
		t.Errorf("an honest base: %q, want net/http's error itself, %q", got, honest)
	}
	withUser := "http://" + user + ":" + password + "@127.0.0.1:1"
	got := RequestError(failed(withUser), withUser)
	if !strings.Contains(got.Error(), "timed out") || strings.Contains(got.Error(), user) || strings.Contains(got.Error(), password) {
		t.Errorf("a base with a user part: %q, want the failure's class and no part of the user part", got)
	}
	if !errors.Is(got, context.DeadlineExceeded) {
		t.Errorf("a base with a user part: %v does not unwrap to the deadline", got)
	}
}

// A URL with a ?, # or \ before the @ that ends its user part, which pgx keeps in the user part and
// net/url reads otherwise (net/url ends the host there, or fails), is printed by URL, Error,
// RequestError and Refusal with no part of its user part, as a Postgres URL and as an http one.
// URL prints no Postgres URL at all, so the control is an honest http URL, which URL prints whole.
// The user, the password and the token are made for this test.
func TestAUserPartWithARawQuestionMarkOrHashIsPrintedByNoFunction(t *testing.T) {
	const (
		user   = "made-up-user-9f4b"
		first  = "not-a-secret-8a3c"
		second = "not-a-secret-7b2d"
		token  = "not-a-secret-tok6c1"
	)
	if got := URL("http://127.0.0.1:5432/db"); got != "http://127.0.0.1:5432/db" {
		t.Fatalf("UNKNOWN: URL does not print an honest URL whole (%q), so this row saw nothing", got)
	}
	for _, scheme := range []string{"postgres://", "http://"} {
		for _, userPart := range []string{
			user + ":" + first + "?" + second,
			user + ":" + first + "#" + second,
			user + ":" + first + `\` + second,
			token + "?" + second,
			token + "#" + second,
		} {
			raw := scheme + userPart + "@127.0.0.1:5432/db"
			failed := errors.New("dial " + raw + ": connection refused")
			for name, text := range map[string]string{
				"URL":          URL(raw),
				"Error":        Error(failed, raw).Error(),
				"RequestError": RequestError(failed, raw).Error(),
				"Refusal":      Refusal("DATABASE_URL", raw, Unparsed).Error(),
			} {
				for _, part := range []string{user, first, second, token} {
					if strings.Contains(text, part) {
						t.Errorf("%d characters, %s: %q holds a part of the user part", len(raw), name, text)
					}
				}
			}
		}
	}
}
