package main

import (
	"fmt"
	"strings"
	"testing"
)

// A DATABASE_URL whose user part pgx and net/url can read apart differently is refused at every
// boot, named by its length. pgx ends the user part at the first @ before any /, and net/url at the
// last @ and at the first /, ? or #, so each such form hands pgx a piece of the user part as a host
// or a database name, which its connect error, logged at every boot that does not reach the
// database, and the dev sign-in fence's refusal would print. Each form is refused with the dev
// sign-in and without it. Its control is the same value with the @ or / of its user part
// percent-encoded: that boots, and under the dev sign-in it is admitted on a loopback host and
// refused naming a remote one. The user, the passwords and the token are made for this test.
func TestBootRefusesAMisreadDatabaseURLByItsLength(t *testing.T) {
	const (
		user   = "made-up-user-0d3b"
		first  = "not-a-secret-a5f1"
		second = "not-a-secret-b27e"
		token  = "not-a-secret-tok3n4c9a"
	)
	for _, tc := range []struct {
		name, value, encoded string
		loopback             bool
	}{
		{name: "a / in the password", value: "postgres://" + user + ":" + first + "/" + second + "@db.example:5432/x", encoded: "postgres://" + user + ":" + first + "%2F" + second + "@db.example:5432/x"},
		{name: "a token and a / as the user part", value: "postgres://" + token + "/" + first + "@db.example/x", encoded: "postgres://" + token + "%2F" + first + "@db.example/x"},
		{name: "an @ in the password", value: "postgres://" + user + ":" + first + "@" + second + "@db.example:5432/x", encoded: "postgres://" + user + ":" + first + "%40" + second + "@db.example:5432/x"},
		{name: "an @ and a , in the password", value: "postgres://" + user + ":" + first + "@" + second + ",x@db.example:5432/x", encoded: "postgres://" + user + ":" + first + "%40" + second + "%2Cx@db.example:5432/x"},
		{name: "an @ in the password, on a loopback host", value: "postgresql://" + user + ":" + first + "@" + second + "@127.0.0.1:5432/x", encoded: "postgresql://" + user + ":" + first + "%40" + second + "@127.0.0.1:5432/x", loopback: true},
	} {
		for _, devSignIn := range []bool{false, true} {
			name := tc.name
			environment := func(value string) settingValues {
				return bootEnvironment(map[string]string{"DATABASE_URL": value})
			}
			if devSignIn {
				name += ", under the dev sign-in"
				environment = func(value string) settingValues {
					return devSignInEnvironment(map[string]string{"DATABASE_URL": value})
				}
			}
			t.Run(name, func(t *testing.T) {
				_, err := resolveBootConfig(environment(tc.value))
				want := fmt.Sprintf("DATABASE_URL, a value of %d characters,", len(tc.value))
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %v, want a refusal that names %q", err, want)
				}
				for _, part := range []string{user, first, second, token} {
					if strings.Contains(err.Error(), part) {
						t.Errorf("the refusal holds a part of the user part: %q", err)
					}
				}
				_, err = resolveBootConfig(environment(tc.encoded))
				switch {
				case !devSignIn || tc.loopback:
					if err != nil {
						t.Errorf("the control with the user part percent-encoded: %v, want it admitted", err)
					}
				case err == nil || !strings.Contains(err.Error(), `DATABASE_URL names host "db.example"`):
					t.Errorf("the control with the user part percent-encoded: err = %v, want the refusal of its remote host db.example", err)
				}
			})
		}
	}
}

// The acceptance test's red test of a DATABASE_URL whose password holds a raw @, kept as it ran, its
// made-up passwords marked for the leak checker: under DISPATCH_DEV_SIGNIN=1, pgx reads the user
// part as ending at the FIRST @, so the host it reads is the password's second half and the host
// after it; the refusal must hold no part of the user part. The control inside the test: a remote
// DATABASE_URL with a plain password is refused naming its host, so the line under test is reached.
func TestADatabaseURLWhosePasswordHoldsAnAtNamesNoPartOfIt(t *testing.T) {
	const (
		user   = "made-up-user-3c9e"
		first  = "not-a-secret-pwa71f0"
		second = "not-a-secret-pwb52d8"
	)
	boot := func(databaseURL string) string {
		_, err := resolveBootConfig(devSignInEnvironment(map[string]string{"DATABASE_URL": databaseURL}))
		if err == nil {
			return "(accepted)"
		}
		return err.Error()
	}
	control := boot("postgres://" + user + ":" + first + "@db.example:5432/dispatch_local")
	t.Logf("control (a plain password, a remote host): %s", strings.NewReplacer(user, "<USER>", first, "<P1>").Replace(control))
	if !strings.Contains(control, `names host "db.example"`) {
		t.Fatalf("UNKNOWN: the control is not refused naming its host, so this test saw nothing: %q", control)
	}
	for _, host := range []string{"127.0.0.1:5432", "db.example:5432"} {
		text := boot("postgres://" + user + ":" + first + "@" + second + "@" + host + "/dispatch_local")
		shown := strings.NewReplacer(user, "<USER>", first, "<P1>", second, "<P2>").Replace(text)
		t.Logf("an @ in the password, host %s: %s", host, shown)
		for _, part := range []string{user, first, second} {
			if strings.Contains(text, part) {
				t.Errorf("RED host %s: the boot refusal holds a part of DATABASE_URL's password: %s", host, shown)
				break
			}
		}
	}
}

// ENVOY_URL and DISPATCH_AGENT_SECRETS_URL are each refused at boot, named by its length, when an @
// is left once net/url takes out its user part. A token written with a raw #, ? or / as the whole
// user part (http://<a>#<b>@host) is otherwise read with <a> as the host, so the server asks DNS
// for the token's first half and connects to it. The control is the same value with that character
// percent-encoded, which boots. The token's halves are made for this test.
func TestBootRefusesAServiceURLWithAnAtLeftOverByItsLength(t *testing.T) {
	const (
		first  = "not-a-secret-tok1e4"
		second = "not-a-secret-tok2b7"
	)
	for _, setting := range []string{"ENVOY_URL", "DISPATCH_AGENT_SECRETS_URL"} {
		environment := func(value string) settingValues {
			overrides := map[string]string{setting: value}
			if setting == "DISPATCH_AGENT_SECRETS_URL" {
				overrides["DISPATCH_AGENT_SECRETS_ACCOUNT_TOKEN_FILE"] = "/var/run/made-up/account-token"
			}
			return bootEnvironment(overrides)
		}
		for _, form := range []struct{ raw, encoded string }{{"#", "%23"}, {"?", "%3F"}, {"/", "%2F"}} {
			t.Run(setting+" with a "+form.raw+" in its user part", func(t *testing.T) {
				value := "http://" + first + form.raw + second + "@127.0.0.1:9020"
				_, err := resolveBootConfig(environment(value))
				want := fmt.Sprintf("%s, a value of %d characters,", setting, len(value))
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %v, want a refusal that names %q", err, want)
				}
				if strings.Contains(err.Error(), first) || strings.Contains(err.Error(), second) {
					t.Errorf("the refusal holds a part of the token: %q", err)
				}
				encoded := "http://" + first + form.encoded + second + "@127.0.0.1:9020"
				if _, err := resolveBootConfig(environment(encoded)); err != nil {
					t.Errorf("the control %s: %v, want it admitted", encoded, err)
				}
			})
		}
	}
}
