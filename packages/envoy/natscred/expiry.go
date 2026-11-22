package natscred

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// claims are the claims of a platform identity token this package reads, unverified: the client
// holds the token it presents and reads only when it lapses and whom it names, which the auth
// service decides for itself when the token is presented.
type claims struct {
	Subject  string   `json:"sub"`
	Email    string   `json:"email"`
	Expiry   int64    `json:"exp"`
	Audience audience `json:"aud"`
}

// audience is a token's `aud`, which a JWT writes as one string or as a list of them.
type audience []string

func (a *audience) UnmarshalJSON(data []byte) error {
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// errNotAJWT is a token whose payload this package cannot read.
var errNotAJWT = errors.New("natscred: the token is not a JWT whose claims can be read")

func claimsOf(raw string) (claims, error) {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	if len(parts) != 3 {
		return claims{}, errNotAJWT
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims{}, errNotAJWT
	}
	var read claims
	if err := json.Unmarshal(payload, &read); err != nil {
		return claims{}, errNotAJWT
	}
	return read, nil
}

// expiryOf is when raw, a platform identity token, lapses, read from its 'exp' unverified.
func expiryOf(raw string) (time.Time, error) {
	read, err := claimsOf(raw)
	if err != nil {
		return time.Time{}, err
	}
	if read.Expiry <= 0 {
		return time.Time{}, errors.New("natscred: the token names no expiry")
	}
	return time.Unix(read.Expiry, 0), nil
}

// Identity is the account raw, a platform identity token, names, read unverified: a Google
// identity token's email, and otherwise its subject, which for a projected Kubernetes token is
// system:serviceaccount:<namespace>:<name>. It is what a service says it connects as; the auth
// service verifies the token and decides for itself.
func Identity(raw string) (string, error) {
	read, err := claimsOf(raw)
	if err != nil {
		return "", err
	}
	switch {
	case read.Email != "":
		return read.Email, nil
	case read.Subject != "":
		return read.Subject, nil
	}
	return "", errors.New("natscred: the token names no account")
}

// TokenClaims are what a platform identity token says of itself, read unverified: the account it
// names (Identity's), the audiences it is for, and when it lapses. A client reads them to tell one
// credential from another and when one ends; the auth service verifies the token and decides.
type TokenClaims struct {
	Identity string
	Audience []string
	Expiry   time.Time
}

// ReadClaims reads raw's TokenClaims; a token that names no account or no expiry is an error.
func ReadClaims(raw string) (TokenClaims, error) {
	identity, err := Identity(raw)
	if err != nil {
		return TokenClaims{}, err
	}
	expiry, err := expiryOf(raw)
	if err != nil {
		return TokenClaims{}, err
	}
	read, err := claimsOf(raw)
	if err != nil {
		return TokenClaims{}, err
	}
	return TokenClaims{Identity: identity, Audience: read.Audience, Expiry: expiry}, nil
}
