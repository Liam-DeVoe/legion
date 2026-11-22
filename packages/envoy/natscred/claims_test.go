package natscred

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// jwtOf is an unsigned token whose payload is claims.
func jwtOf(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".x"
}

// ReadClaims reads who issued a token and when it lapses; a token that names no issuer or no expiry
// is an error, and so is a raw string that is no JWT.
func TestReadClaims(t *testing.T) {
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, c := range []struct {
		name, issuer string
		claims       map[string]any
	}{
		{"a projected Kubernetes token", "https://kubernetes.default.svc", map[string]any{"iss": "https://kubernetes.default.svc", "sub": "system:serviceaccount:a:b", "aud": []string{"x"}, "exp": exp.Unix()}},
		{"a Google identity token", "https://accounts.google.com", map[string]any{"iss": "https://accounts.google.com", "email": "probe@example.iam.gserviceaccount.com", "aud": "x", "exp": exp.Unix()}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReadClaims(jwtOf(t, c.claims))
			if err != nil {
				t.Fatal(err)
			}
			if got.Issuer != c.issuer || !got.Expiry.Equal(exp) {
				t.Fatalf("ReadClaims = %+v, want issuer %q, expiry %s", got, c.issuer, exp)
			}
		})
	}
	for _, c := range []struct {
		name   string
		claims map[string]any
	}{
		{"no issuer", map[string]any{"sub": "s", "exp": exp.Unix()}},
		{"no expiry", map[string]any{"iss": "i", "sub": "s"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got, err := ReadClaims(jwtOf(t, c.claims)); err == nil {
				t.Fatalf("ReadClaims = %+v, want an error", got)
			}
		})
	}
	if _, err := ReadClaims("not-a-token"); err == nil {
		t.Fatal("ReadClaims of a raw string answered no error")
	}
}
