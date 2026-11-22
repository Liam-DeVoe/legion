package natscred

import (
	"encoding/base64"
	"encoding/json"
	"slices"
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

// ReadClaims reads the account a token names (its email before its subject, as Identity does), its
// audiences whether `aud` is one string or a list, and its expiry; a token that names no account or
// no expiry is an error.
func TestReadClaims(t *testing.T) {
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, c := range []struct {
		name     string
		claims   map[string]any
		identity string
		audience []string
	}{
		{"a projected Kubernetes token, aud a list", map[string]any{"sub": "system:serviceaccount:a:b", "aud": []string{"x", "y"}, "exp": exp.Unix()}, "system:serviceaccount:a:b", []string{"x", "y"}},
		{"a Google identity token, aud one string", map[string]any{"sub": "123", "email": "probe@example.iam.gserviceaccount.com", "aud": "x", "exp": exp.Unix()}, "probe@example.iam.gserviceaccount.com", []string{"x"}},
		{"no aud", map[string]any{"sub": "s", "exp": exp.Unix()}, "s", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := ReadClaims(jwtOf(t, c.claims))
			if err != nil {
				t.Fatal(err)
			}
			if got.Identity != c.identity || !slices.Equal(got.Audience, c.audience) || !got.Expiry.Equal(exp) {
				t.Fatalf("ReadClaims = %+v, want identity %q, audience %v, expiry %s", got, c.identity, c.audience, exp)
			}
		})
	}
	for _, c := range []struct {
		name   string
		claims map[string]any
	}{
		{"no account", map[string]any{"aud": "x", "exp": exp.Unix()}},
		{"no expiry", map[string]any{"sub": "s", "aud": "x"}},
		{"aud of another type", map[string]any{"sub": "s", "aud": 7, "exp": exp.Unix()}},
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
