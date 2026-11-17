// packages/envoy/internal/brokerclient/printed_test.go
//
// What this package prints of a setting, a credential file or a server's answer: never a part of
// a credential a person put where a setting expected a URL, a file's path or a plain value, and
// never the content of a file that holds one. Each refusal still names the setting to look at,
// and a clean value prints as it did, which is each test's control.

package brokerclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// printedSecret stands for a credential in a place it does not belong. No printed text holds it.
const printedSecret = "hunter2pwPRINTED"

// exampleFlow is a device-flow client of a made-up client at issuer; its made-up client value is
// built rather than written as a literal beside a credential's field name.
func exampleFlow(issuer string) *DeviceFlow {
	client := "example-device-client"
	return &DeviceFlow{Issuer: issuer, ClientID: client, ClientSecret: client + "-value"}
}

// holdsNoSecret fails t when text holds printedSecret, or the user name the URL probes carry.
func holdsNoSecret(t *testing.T, what, text string) {
	t.Helper()
	if strings.Contains(text, printedSecret) || strings.Contains(text, "tokuser9f3") {
		t.Errorf("%s = %q, which holds the credential", what, text)
	}
}

// TestATokenFilePrintsItsSettingAndNeverItsPath: AGENT_SECRETS_MACHINE_TOKEN_FILE names a file
// that holds a credential, and a person who pasted the token itself there would see it in every
// refusal and in every log line naming the identity. So the file prints as its setting: in the
// read error, in the empty-file refusal, in Describe, and in the trade's error and log line.
func TestATokenFilePrintsItsSettingAndNeverItsPath(t *testing.T) {
	dir := t.TempDir()
	missing := NewProjectedToken(filepath.Join(dir, printedSecret))
	_, _, err := missing.Token(context.Background())
	if err == nil {
		t.Fatal("Token read a token file that is not there")
	}
	holdsNoSecret(t, "the missing file's error", err.Error())
	for _, want := range []string{MachineTokenFileVar, "no such file or directory"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the missing file's error = %q, want it to name %q", err, want)
		}
	}
	holdsNoSecret(t, "Describe", missing.Describe())
	if !strings.Contains(missing.Describe(), MachineTokenFileVar) {
		t.Errorf("Describe = %q, want it to name %s", missing.Describe(), MachineTokenFileVar)
	}

	empty := filepath.Join(dir, "empty-"+printedSecret)
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewProjectedToken(empty).Token(context.Background()); err == nil || !strings.Contains(err.Error(), MachineTokenFileVar) {
		t.Errorf("the empty file's error = %v, want one naming %s", err, MachineTokenFileVar)
	} else {
		holdsNoSecret(t, "the empty file's error", err.Error())
	}

	broker := newStubBroker(t, func(int, url.Values) (int, map[string]any) {
		return 200, map[string]any{"access_token": "never", "token_type": "DPoP", "expires_in": 60}
	})
	var log bytes.Buffer
	client, err := New(Config{BrokerURL: broker.Address(), Key: testKey(t), Subject: missing, Log: slog.New(slog.NewTextHandler(&log, nil))})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Token(context.Background())
	if err == nil {
		t.Fatal("the trade went ahead with no token")
	}
	holdsNoSecret(t, "the trade's error", err.Error())
	holdsNoSecret(t, "the client's log", log.String())
}

// TestASecretFileThatCannotBeReadIsNamedBySetting: the sign-in client's secret is read from the
// file AGENT_SECRETS_SIGNIN_CLIENT_SECRET_FILE names, and os.ReadFile's error repeats that path,
// which for a person who pasted the secret itself is the secret. The refusal names the setting
// and the system's reason alone.
func TestASecretFileThatCannotBeReadIsNamedBySetting(t *testing.T) {
	env := map[string]string{
		SignInClientIDVar:         "example-device-client",
		SignInClientSecretFileVar: filepath.Join(t.TempDir(), printedSecret),
	}
	_, err := SignInFlow(func(name string) string { return env[name] })
	if err == nil {
		t.Fatal("SignInFlow read a secret file that is not there")
	}
	holdsNoSecret(t, "SignInFlow's error", err.Error())
	for _, want := range []string{SignInClientSecretFileVar, "no such file or directory"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("SignInFlow's error = %q, want it to name %q", err, want)
		}
	}
	// Control: a file that is there is read.
	secretFile := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(secretFile, []byte("example-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env[SignInClientSecretFileVar] = secretFile
	flow, err := SignInFlow(func(name string) string { return env[name] })
	if err != nil || flow.ClientSecret != "example-secret" {
		t.Fatalf("SignInFlow = %+v, %v; want the secret the file holds", flow, err)
	}
}

// TestARefusedSettingIsNamedAndNotEchoed: a value this package refuses from the environment - an
// identity kind it does not know, a project list that is not one - is where a credential pasted
// into the wrong variable shows up, so the refusal names the setting and what it takes, and not
// the value. A value it accepts is read as before.
func TestARefusedSettingIsNamedAndNotEchoed(t *testing.T) {
	env := map[string]string{IdentityVar: printedSecret}
	getenv := func(name string) string { return env[name] }
	_, err := MachineIdentity(getenv)
	if err == nil {
		t.Fatal("MachineIdentity took an identity kind it does not know")
	}
	holdsNoSecret(t, "MachineIdentity's error", err.Error())
	for _, want := range []string{IdentityVar, IdentityKubernetes, IdentityMetadata, IdentityPool} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("MachineIdentity's error = %q, want it to name %q", err, want)
		}
	}

	for _, projects := range []string{"lower-" + printedSecret, "HUNTER2 HUNTER2"} {
		env[ProjectsVar] = projects
		if _, err := TradeProjects(getenv); err == nil {
			t.Errorf("TradeProjects took %q", projects)
		} else {
			holdsNoSecret(t, "TradeProjects' error", err.Error())
			if strings.Contains(err.Error(), "HUNTER2") || !strings.Contains(err.Error(), ProjectsVar) {
				t.Errorf("TradeProjects' error = %q, want one naming %s and not the value", err, ProjectsVar)
			}
		}
	}
	env[ProjectsVar] = "INFRA BET"
	if projects, err := TradeProjects(getenv); err != nil || strings.Join(projects, " ") != "BET INFRA" {
		t.Errorf("TradeProjects = %v, %v; want BET INFRA", projects, err)
	}
	broker := newStubBroker(t, func(int, url.Values) (int, map[string]any) { return 500, nil })
	if _, err := New(Config{BrokerURL: broker.Address(), Key: testKey(t), Subject: &scriptedSource{}, Projects: []string{"lower-" + printedSecret}}); err == nil {
		t.Error("New took a project list that is not one")
	} else {
		holdsNoSecret(t, "New's error", err.Error())
	}
}

// TestAnIssuerThatCarriesACredentialIsNotPrinted: AGENT_SECRETS_SIGNIN_ISSUER is a URL, and every
// refusal of the sign-in prints it only as internal/redact's allow-list does - whole when it is a
// plain URL, not at all when it carries a user name, a password or a query - beside the setting's
// name, with the failure's class rather than go-oidc's or net/http's own text, which repeats the
// URL. A plain issuer that answers nothing prints whole, with its class: the control.
func TestAnIssuerThatCarriesACredentialIsNotPrinted(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := listener.Addr().String()
	listener.Close()
	for _, issuer := range []string{
		"http://tokuser9f3:" + printedSecret + "@" + closed,
		"http://" + printedSecret + "@" + closed,
		"http://" + closed + "/?token=" + printedSecret,
		"http://" + closed + "/%zz" + printedSecret,
	} {
		t.Run(issuer, func(t *testing.T) {
			flow := exampleFlow(issuer)
			for name, call := range map[string]func() error{
				"Start":   func() error { _, err := flow.Start(context.Background()); return err },
				"Refresh": func() error { _, err := flow.Refresh(context.Background(), "a-refresh-token"); return err },
			} {
				err := call()
				if err == nil {
					t.Fatalf("%s reached an issuer that is not there", name)
				}
				holdsNoSecret(t, name+"'s error", err.Error())
				if !strings.Contains(err.Error(), SignInIssuerVar) {
					t.Errorf("%s's error = %q, want it to name %s", name, err, SignInIssuerVar)
				}
			}
		})
	}
	t.Run("a plain issuer", func(t *testing.T) {
		flow := exampleFlow("http://" + closed)
		_, err := flow.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "http://"+closed) || !strings.Contains(err.Error(), "connection refused") {
			t.Errorf("Start's error = %v, want one naming http://%s and the refused connection", err, closed)
		}
	})
}

// TestAnIssuersAnswerPrintsItsStatusAndCodeAlone: what an issuer answers - a discovery document it
// could not serve, a refusal of the device code or of a refresh - prints as its status and, for
// an OAuth refusal, its error code, which is one plain word. Never its body or its description,
// which a server can fill with anything, a request it echoes included.
func TestAnIssuersAnswerPrintsItsStatusAndCodeAlone(t *testing.T) {
	var base string
	discoveryFails := true
	base = newRawServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			if discoveryFails {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte("echo of client_secret=" + printedSecret))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"issuer":"` + base + `","device_authorization_endpoint":"` + base + `/device","token_endpoint":"` + base + `/token","authorization_endpoint":"` + base + `/auth","jwks_uri":"` + base + `/jwks"}`))
		case "/device":
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("echo of " + printedSecret))
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid_client","error_description":"echo of ` + printedSecret + `"}`))
		}
	})
	flow := exampleFlow(base)
	if _, err := flow.Start(context.Background()); err == nil {
		t.Fatal("Start read a discovery document the issuer did not serve")
	} else {
		holdsNoSecret(t, "the failed discovery", err.Error())
		if !strings.Contains(err.Error(), "500") {
			t.Errorf("the failed discovery = %q, want its status", err)
		}
	}
	discoveryFails = false
	flow = exampleFlow(base)
	if _, err := flow.Start(context.Background()); err == nil {
		t.Fatal("Start took a device code the issuer refused")
	} else {
		holdsNoSecret(t, "the refused device code", err.Error())
		if !strings.Contains(err.Error(), "502") {
			t.Errorf("the refused device code = %q, want its status", err)
		}
	}
	if _, err := flow.Refresh(context.Background(), "a-refresh-token"); err == nil {
		t.Fatal("Refresh took a refresh the issuer refused")
	} else {
		holdsNoSecret(t, "the refused refresh", err.Error())
		for _, want := range []string{"400", "invalid_client"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refused refresh = %q, want it to name %q", err, want)
			}
		}
	}
}

// TestAnIdentityTokenThatDoesNotParseIsNotQuoted: the identity token a sign-in answers is a
// credential, and go-jose's and encoding/json's errors quote the parts of it they could not read
// - an algorithm name in its header, a value in its claims - so neither reaches the refusal.
func TestAnIdentityTokenThatDoesNotParseIsNotQuoted(t *testing.T) {
	segment := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for _, token := range []string{
		segment(`{"alg":"`+printedSecret+`"}`) + "." + segment(`{"email":"ada@example.com"}`) + ".c2ln",
		segment(`{"alg":"RS256"}`) + "." + segment(`{"email":`+printedSecret+`}`) + ".c2ln",
		segment(`{"alg":"RS256"}`) + "." + segment(`{"email":123456789012345678901234567890`+"9"+`}`) + ".c2ln",
	} {
		if _, err := PersonOf(token); err == nil {
			t.Errorf("PersonOf read a token that does not parse: %q", token)
		} else {
			holdsNoSecret(t, "PersonOf's error", err.Error())
			if strings.Contains(err.Error(), "1234567890") {
				t.Errorf("PersonOf's error = %q, which quotes the token's claims", err)
			}
		}
	}
}

// TestTheSignInStorePrintsItsSettingAndNeverItsPathOrContent: the sign-in records hold a refresh
// token and a box key, so they are credential files: no refusal names their directory's path
// (AGENT_SECRETS_SIGNIN_DIR's value) or quotes their content. The store prints as its setting.
func TestTheSignInStorePrintsItsSettingAndNeverItsPathOrContent(t *testing.T) {
	parent := t.TempDir()
	blocked := filepath.Join(parent, printedSecret)
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	store := SignInStore{Dir: filepath.Join(blocked, "signins")}
	if err := store.Write(signedIn(t, "ada@example.com")); err == nil {
		t.Fatal("Write kept a record under a file")
	} else {
		holdsNoSecret(t, "Write's error", err.Error())
		if !strings.Contains(err.Error(), SignInDirVar) {
			t.Errorf("Write's error = %q, want it to name %s", err, SignInDirVar)
		}
	}
	if _, err := (SignInStore{Dir: blocked}).List(); err == nil {
		t.Fatal("List read a directory that is a file")
	} else {
		holdsNoSecret(t, "List's error", err.Error())
		if !strings.Contains(err.Error(), SignInDirVar) {
			t.Errorf("List's error = %q, want it to name %s", err, SignInDirVar)
		}
	}

	dir := filepath.Join(parent, "store-"+printedSecret)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{
		`{"person":"ada@example.com","refresh_token":"` + printedSecret + `","box_key":"k","signed_in_at":"` + printedSecret + `"}`,
		`{"person":"ada@example.com","refresh_token":` + printedSecret + `}`,
		`{"person":"ada@example.com"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, "record.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (SignInStore{Dir: dir}).List(); err == nil {
			t.Errorf("List read a record that is not one: %s", content)
		} else {
			holdsNoSecret(t, "the bad record's error", err.Error())
		}
	}
}
