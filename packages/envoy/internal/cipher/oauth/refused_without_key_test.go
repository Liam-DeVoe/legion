package oauth

import (
	"testing"
	"time"

	"authelia.com/provider/oauth2/token/jwt"
)

// TestRefusedWithoutKeyMirrorsTheLibrarysChecks: refusedWithoutKey refuses exactly the claims the
// library refuses once a signature checks: no exp claim, an exp not a number, past, or more than
// the window away, and an iss that is not the type's issuer. It admits a live token, a token at its
// exp, an exp of 0 or null (which the library holds neither past nor too far ahead), an exp the
// window still covers, and any iss where the type names no issuer.
func TestRefusedWithoutKeyMirrorsTheLibrarysChecks(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	const issuer, window = "https://issuer.example", time.Hour
	for _, row := range []struct {
		name    string
		claims  jwt.MapClaims
		issuer  string
		refused bool
	}{
		{"a live token", jwt.MapClaims{"iss": issuer, "exp": now.Add(time.Minute).Unix()}, issuer, false},
		{"a token at its exp", jwt.MapClaims{"iss": issuer, "exp": now.Unix()}, issuer, false},
		{"an exp of 0", jwt.MapClaims{"iss": issuer, "exp": float64(0)}, issuer, false},
		{"an exp of null", jwt.MapClaims{"iss": issuer, "exp": nil}, issuer, false},
		{"an exp as JSON decodes it", jwt.MapClaims{"iss": issuer, "exp": float64(now.Add(time.Minute).Unix())}, issuer, false},
		{"an expired exp as JSON decodes it", jwt.MapClaims{"iss": issuer, "exp": float64(now.Add(-time.Minute).Unix())}, issuer, true},
		{"an exp the window just covers", jwt.MapClaims{"iss": issuer, "exp": now.Add(window).Unix()}, issuer, false},
		{"any iss where the type names none", jwt.MapClaims{"iss": "https://anyone.example", "exp": now.Add(time.Minute).Unix()}, "", false},
		{"no exp", jwt.MapClaims{"iss": issuer}, issuer, true},
		{"an exp that is not a number", jwt.MapClaims{"iss": issuer, "exp": "tomorrow"}, issuer, true},
		{"an exp a second past", jwt.MapClaims{"iss": issuer, "exp": now.Add(-time.Second).Unix()}, issuer, true},
		{"an exp past the window", jwt.MapClaims{"iss": issuer, "exp": now.Add(window + time.Second).Unix()}, issuer, true},
		{"another iss", jwt.MapClaims{"iss": "https://issuer.elsewhere.example", "exp": now.Add(time.Minute).Unix()}, issuer, true},
		{"an iss that is not a string", jwt.MapClaims{"iss": 7, "exp": now.Add(time.Minute).Unix()}, issuer, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			err := refusedWithoutKey(row.claims, row.issuer, window, now)
			if refused := err != nil; refused != row.refused {
				t.Fatalf("refusedWithoutKey = %v, want refused %v", err, row.refused)
			}
		})
	}
}
